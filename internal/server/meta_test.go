package server

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sidharthpunathil/skillbuilder/internal/store"
)

// ---------- helpers ----------

// newTestServer builds a server over a throwaway store containing one skill and
// one folder, with a fixed BaseURL so canonical URLs are deterministic.
func newTestServer(t *testing.T, opts ...func(*Options)) *Server {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.CreateSkill(&store.Skill{
		Name: "code-review",
		// Deliberately contains hard line breaks and long prose: both have to be
		// cleaned up before this reaches a meta attribute.
		Description: "Reviews a diff for correctness and style.\nUse when the user asks for a code review,\tsays \"review this PR\", or wants feedback on a change before merging it into the main branch of the repository.",
		Body:        "# Code review\n\nDo the thing.\n",
		Tags:        []string{"review", "git"},
	}); err != nil {
		t.Fatalf("create skill: %v", err)
	}
	if err := st.CreateBundle(&store.Bundle{
		Name:        "backend",
		Title:       "Backend",
		Description: "Skills for server work.",
		Skills:      []string{"code-review"},
	}); err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	o := Options{
		Store:       st,
		Build:       "test",
		BaseURL:     "https://registry.example.com",
		TemplateDir: "../../web/templates",
		StaticDir:   "../../web/static",
	}
	for _, fn := range opts {
		fn(&o)
	}
	srv, err := New(o)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv
}

func get(t *testing.T, s *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// metaFor runs the same path pageMeta takes in production.
func metaFor(s *Server, target string) PageMeta {
	return s.pageMeta(httptest.NewRequest(http.MethodGet, target, nil))
}

// ---------- description handling ----------

func TestSquashSpaceStripsNewlinesAndRuns(t *testing.T) {
	in := "  first line\nsecond\r\nline\twith\ttabs   and   runs  "
	want := "first line second line with tabs and runs"
	if got := squashSpace(in); got != want {
		t.Errorf("squashSpace()\n got %q\nwant %q", got, want)
	}
}

func TestMetaDescriptionTruncatesAtWordBoundary(t *testing.T) {
	long := strings.Repeat("alpha bravo ", 40) // ~480 chars
	got := metaDescription(long)

	if n := len([]rune(got)); n > metaDescriptionMax {
		t.Errorf("length %d exceeds limit %d: %q", n, metaDescriptionMax, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated description should end in an ellipsis, got %q", got)
	}
	// A word-boundary cut means the last word before the ellipsis is intact.
	if strings.Contains(got, "alph…") || strings.Contains(got, "brav…") {
		t.Errorf("cut mid-word: %q", got)
	}
	if strings.ContainsAny(got, "\n\r\t") {
		t.Errorf("description contains raw whitespace control chars: %q", got)
	}
}

func TestMetaDescriptionShortStringIsUntouched(t *testing.T) {
	const in = "Reviews a diff for correctness and style."
	if got := metaDescription(in); got != in {
		t.Errorf("got %q, want %q", got, in)
	}
}

func TestSkillPageDescriptionComesFromTheSkill(t *testing.T) {
	s := newTestServer(t)
	m := metaFor(s, "/skills/code-review")

	if !strings.HasPrefix(m.Description, "Reviews a diff for correctness and style.") {
		t.Errorf("description should start from the skill's own text, got %q", m.Description)
	}
	if strings.ContainsAny(m.Description, "\n\r\t") {
		t.Errorf("newlines survived into the description: %q", m.Description)
	}
	if n := len([]rune(m.Description)); n > metaDescriptionMax {
		t.Errorf("description is %d runes, limit is %d", n, metaDescriptionMax)
	}
	if !strings.Contains(m.Title, "code-review") {
		t.Errorf("title should name the skill, got %q", m.Title)
	}
}

// ---------- indexability ----------

func TestNoIndexOnEditorAndImportRoutes(t *testing.T) {
	s := newTestServer(t)
	cases := []struct {
		path    string
		noindex bool
	}{
		{"/", false},
		{"/skills", false},
		{"/skills/code-review", false},
		{"/skills?tag=review", false},
		{"/bundles", false},
		{"/bundles/backend", false},

		{"/skills?q=review", true}, // search results
		{"/skills/new", true},
		{"/skills/code-review/edit", true},
		{"/skills/code-review/files/new", true},
		{"/skills/code-review/files/ref/guide.md", true},
		{"/bundles/new", true},
		{"/bundles/backend/edit", true},
		{"/import", true},
		{"/import/github", true},
		{"/skills/does-not-exist", true}, // renders the 404 page
		{"/bundles/does-not-exist", true},
		{"/nonsense/path", true},
	}
	for _, c := range cases {
		if got := metaFor(s, c.path).NoIndex; got != c.noindex {
			t.Errorf("%s: NoIndex = %v, want %v", c.path, got, c.noindex)
		}
	}
}

func TestAuthenticatedRegistryIsNeverIndexable(t *testing.T) {
	s := newTestServer(t, func(o *Options) { o.Auth = Auth{Password: "s3cret"} })
	for _, p := range []string{"/", "/skills", "/skills/code-review", "/bundles/backend"} {
		m := metaFor(s, p)
		if !m.NoIndex {
			t.Errorf("%s: a password-protected registry must be noindex", p)
		}
		if m.JSONLD != "" {
			t.Errorf("%s: no structured data should be emitted for a private site", p)
		}
	}
}

func TestNoIndexRendersRobotsMetaTag(t *testing.T) {
	s := newTestServer(t)

	body := get(t, s, "/import").Body.String()
	if !strings.Contains(body, `name="robots" content="noindex, nofollow"`) {
		t.Errorf("/import should render a noindex robots tag")
	}

	body = get(t, s, "/skills/code-review").Body.String()
	if strings.Contains(body, "noindex") {
		t.Errorf("a skill page must not be noindex")
	}
	if !strings.Contains(body, `name="robots" content="index, follow`) {
		t.Errorf("a skill page should render an index directive")
	}
}

// ---------- canonical URLs ----------

func TestCanonicalURLUsesConfiguredBaseURL(t *testing.T) {
	s := newTestServer(t)
	cases := map[string]string{
		"/":                             "https://registry.example.com/",
		"/skills":                       "https://registry.example.com/skills",
		"/skills/":                      "https://registry.example.com/skills",
		"/skills/code-review":           "https://registry.example.com/skills/code-review",
		"/bundles/backend":              "https://registry.example.com/bundles/backend",
		"/skills?tag=review":            "https://registry.example.com/skills?tag=review",
		"/skills?q=review":              "https://registry.example.com/skills",
		"/skills?q=review&tag=review":   "https://registry.example.com/skills",
		"/skills/code-review?utm_src=x": "https://registry.example.com/skills/code-review",
	}
	for path, want := range cases {
		if got := metaFor(s, path).Canonical; got != want {
			t.Errorf("%s: canonical = %q, want %q", path, got, want)
		}
	}
}

func TestCanonicalURLFallsBackToRequestHost(t *testing.T) {
	s := newTestServer(t, func(o *Options) { o.BaseURL = "" })

	r := httptest.NewRequest(http.MethodGet, "/skills/code-review", nil)
	r.Host = "skills.internal:8080"
	if got := s.pageMeta(r).Canonical; got != "http://skills.internal:8080/skills/code-review" {
		t.Errorf("canonical = %q", got)
	}

	// A TLS-terminating proxy is the normal deployment shape.
	r = httptest.NewRequest(http.MethodGet, "/skills", nil)
	r.Host = "skills.example.org"
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := s.pageMeta(r).Canonical; got != "https://skills.example.org/skills" {
		t.Errorf("canonical = %q", got)
	}
}

func TestOpenGraphImageIsAbsoluteAndPerPage(t *testing.T) {
	s := newTestServer(t)
	cases := map[string]string{
		"/":                   "https://registry.example.com/og.png",
		"/skills/code-review": "https://registry.example.com/og/skill/code-review.png",
		"/bundles/backend":    "https://registry.example.com/og/folder/backend.png",
	}
	for path, want := range cases {
		m := metaFor(s, path)
		if m.Image != want {
			t.Errorf("%s: og:image = %q, want %q", path, m.Image, want)
		}
		if m.ImageWidth != 1200 || m.ImageHeight != 630 {
			t.Errorf("%s: og:image dimensions = %dx%d, want 1200x630", path, m.ImageWidth, m.ImageHeight)
		}
	}
}

func TestHeadPartialRendersFullOpenGraphSet(t *testing.T) {
	s := newTestServer(t)
	body := get(t, s, "/skills/code-review").Body.String()

	for _, want := range []string{
		`property="og:title"`,
		`property="og:description"`,
		`property="og:type"`,
		`property="og:url"`,
		`property="og:image"`,
		`property="og:image:width" content="1200"`,
		`property="og:image:height" content="630"`,
		`property="og:site_name"`,
		`name="twitter:card" content="summary_large_image"`,
		`rel="canonical" href="https://registry.example.com/skills/code-review"`,
		`content="https://registry.example.com/og/skill/code-review.png"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("head is missing %s", want)
		}
	}
}

// ---------- structured data ----------

func TestJSONLDIsValidAndPageAppropriate(t *testing.T) {
	s := newTestServer(t)

	cases := map[string]string{
		"/":                   "WebSite",
		"/skills/code-review": "SoftwareApplication",
		"/bundles/backend":    "CollectionPage",
	}
	for path, wantType := range cases {
		raw := string(metaFor(s, path).JSONLD)
		if raw == "" {
			t.Fatalf("%s: no JSON-LD emitted", path)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatalf("%s: JSON-LD does not parse: %v\n%s", path, err, raw)
		}
		if doc["@context"] != "https://schema.org" {
			t.Errorf("%s: @context = %v", path, doc["@context"])
		}
		if doc["@type"] != wantType {
			t.Errorf("%s: @type = %v, want %s", path, doc["@type"], wantType)
		}
		// json.Marshal escapes these, which is what keeps the payload from
		// breaking out of the surrounding <script> element.
		if strings.ContainsAny(raw, "<>") {
			t.Errorf("%s: unescaped angle bracket in JSON-LD: %s", path, raw)
		}
	}
}

func TestJSONLDReachesThePage(t *testing.T) {
	s := newTestServer(t)
	body := get(t, s, "/").Body.String()
	if !strings.Contains(body, `type="application/ld+json"`) {
		t.Fatalf("home page has no ld+json block")
	}
	if !strings.Contains(body, `"@type":"WebSite"`) {
		t.Errorf("home page ld+json is not a WebSite")
	}
}

// ---------- OG image handler ----------

func TestOGHandlerServesPNGForKnownAndUnknownNames(t *testing.T) {
	s := newTestServer(t)

	cases := []string{
		"/og.png",
		"/og/skill/code-review.png",
		"/og/skill/code-review", // extension is optional
		"/og/folder/backend.png",
		"/og/bundle/backend.png", // legacy kind name
		"/og/skill/no-such-skill.png",
		"/og/folder/no-such-folder.png",
		"/og/nonsense/thing.png",
	}
	for _, path := range cases {
		rec := get(t, s, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200 (unknown names must fall back, not 404)", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
			t.Errorf("%s: Content-Type = %q, want image/png", path, ct)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s: empty body", path)
		}
		if got := rec.Body.Bytes(); len(got) < 8 || string(got[1:4]) != "PNG" {
			t.Errorf("%s: body is not a PNG", path)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "public") || !strings.Contains(cc, "max-age=") {
			t.Errorf("%s: Cache-Control = %q, want a public max-age", path, cc)
		}
		if rec.Header().Get("ETag") == "" {
			t.Errorf("%s: no ETag", path)
		}
	}
}

func TestOGHandlerHonoursIfNoneMatch(t *testing.T) {
	s := newTestServer(t)
	first := get(t, s, "/og/skill/code-review.png")
	etag := first.Header().Get("ETag")

	r := httptest.NewRequest(http.MethodGet, "/og/skill/code-review.png", nil)
	r.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)

	if rec.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 should have an empty body, got %d bytes", rec.Body.Len())
	}
}

func TestOGCardsDifferPerSkill(t *testing.T) {
	s := newTestServer(t)
	a := get(t, s, "/og/skill/code-review.png").Body.Bytes()
	b := get(t, s, "/og.png").Body.Bytes()
	if len(a) == len(b) && string(a) == string(b) {
		t.Errorf("the skill card and the site card render identically")
	}
}

// ---------- robots.txt and sitemap.xml ----------

func TestRobotsTxtAllowsContentAndBlocksEditors(t *testing.T) {
	s := newTestServer(t)
	rec := get(t, s, "/robots.txt")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"User-agent: *",
		"Allow: /",
		"Disallow: /import",
		"Disallow: /api/",
		"Disallow: /*/edit",
		"Sitemap: https://registry.example.com/sitemap.xml",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("robots.txt missing %q\n---\n%s", want, body)
		}
	}
}

func TestRobotsTxtBlocksEverythingWhenAuthenticated(t *testing.T) {
	s := newTestServer(t, func(o *Options) { o.Auth = Auth{Password: "s3cret"} })
	r := httptest.NewRequest(http.MethodGet, "/robots.txt", nil)
	r.SetBasicAuth("", "s3cret")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)

	if got := rec.Body.String(); !strings.Contains(got, "Disallow: /\n") || strings.Contains(got, "Allow: /") {
		t.Errorf("a private registry should disallow everything, got:\n%s", got)
	}
}

func TestSitemapListsIndexablePages(t *testing.T) {
	s := newTestServer(t)
	rec := get(t, s, "/sitemap.xml")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<loc>https://registry.example.com/</loc>",
		"<loc>https://registry.example.com/skills</loc>",
		"<loc>https://registry.example.com/bundles</loc>",
		"<loc>https://registry.example.com/skills/code-review</loc>",
		"<loc>https://registry.example.com/bundles/backend</loc>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sitemap missing %s\n---\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"/skills/new", "/edit", "/import", "/api/"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("sitemap should not list %s\n---\n%s", unwanted, body)
		}
	}

	var set sitemapURLSet
	if err := xml.Unmarshal([]byte(body), &set); err != nil {
		t.Fatalf("sitemap is not valid XML: %v", err)
	}
	if len(set.URLs) != 5 {
		t.Errorf("got %d urls, want 5", len(set.URLs))
	}
}

// ---------- site identity is configurable ----------

func TestSiteNameAndDescriptionComeFromEnv(t *testing.T) {
	t.Setenv(envSiteName, "Acme Skills")
	t.Setenv(envSiteDescription, "Internal agent skills for Acme.")

	s := newTestServer(t)
	m := metaFor(s, "/")

	if m.SiteName != "Acme Skills" {
		t.Errorf("SiteName = %q", m.SiteName)
	}
	if m.Title != "Acme Skills" {
		t.Errorf("Title = %q", m.Title)
	}
	if m.Description != "Internal agent skills for Acme." {
		t.Errorf("Description = %q", m.Description)
	}
	if got := metaFor(s, "/skills/code-review").Title; !strings.HasSuffix(got, "· Acme Skills") {
		t.Errorf("skill title should carry the configured site name, got %q", got)
	}
}

func TestDefaultsAreNotTiedToAnyPersonOrDomain(t *testing.T) {
	s := newTestServer(t, func(o *Options) { o.BaseURL = "" })
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "example.test"
	m := s.pageMeta(r)

	for _, banned := range []string{"github.com", "sidharthp", "localhost"} {
		blob := m.Title + m.Description + m.Canonical + m.Image + string(m.JSONLD)
		if strings.Contains(strings.ToLower(blob), banned) {
			t.Errorf("default metadata leaks %q: %s", banned, blob)
		}
	}
}
