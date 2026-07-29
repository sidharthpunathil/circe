package server

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/sidharthp/skillbuilder/internal/store"
)

// Per-page SEO metadata.
//
// The whole thing is derived from the request path rather than from each
// handler, so no handler has to remember to fill it in: base() already has the
// request, and the route shape tells us everything we need. That keeps this
// concern in one file and makes an un-tagged page impossible.

// ---------- site identity ----------
//
// Nothing here is hardcoded to a person, a domain or a repository. The origin
// comes from Options.BaseURL or the request; the name and description come from
// optional env vars with neutral product defaults.

const (
	envSiteName        = "SKILLBUILDER_SITE_NAME"
	envSiteDescription = "SKILLBUILDER_SITE_DESCRIPTION"
	envTwitterSite     = "SKILLBUILDER_TWITTER_SITE"

	defaultSiteName        = "Skillbuilder"
	defaultSiteDescription = "A registry for Claude agent skills. Browse, organise and install them into Claude Code with one command."

	// metaDescriptionMax is the point past which search engines and social
	// cards truncate for you, usually mid-word.
	metaDescriptionMax = 160
	metaTitleMax       = 70
)

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func (s *Server) siteName() string { return envOrDefault(envSiteName, defaultSiteName) }

func (s *Server) siteDescription() string {
	return envOrDefault(envSiteDescription, defaultSiteDescription)
}

func (s *Server) twitterSite() string { return envOrDefault(envTwitterSite, "") }

// ---------- text helpers ----------

// squashSpace flattens newlines, tabs and runs of spaces into single spaces.
// Descriptions come out of YAML frontmatter, where a folded scalar happily
// contains hard line breaks that would otherwise land inside a meta attribute.
func squashSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// clipRunes truncates at a word boundary and appends an ellipsis. It counts
// runes, not bytes, so a description in any script gets the same budget.
func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	// Leave room for the ellipsis so the result is never longer than max.
	cut := string(r[:max-1])
	if i := strings.LastIndexAny(cut, " \t"); i > max/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:—-") + "…"
}

// metaDescription normalises arbitrary prose into a description attribute.
func metaDescription(s string) string {
	return clipRunes(squashSpace(s), metaDescriptionMax)
}

// pageTitle appends the site name unless the page is the site itself.
func pageTitle(title, site string) string {
	title = squashSpace(title)
	switch {
	case title == "", title == site:
		return site
	default:
		return clipRunes(title+" — "+site, metaTitleMax)
	}
}

// hostOf pulls the bare host out of an origin for display on a card.
func hostOf(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	}
	return u.Host
}

// ---------- the model ----------

// PageMeta is everything the <head> partial needs. It is a value, not a
// pointer, so a zero view model still renders valid (if empty) markup.
type PageMeta struct {
	Title       string
	Description string
	Canonical   string
	Type        string // og:type
	Image       string // absolute URL
	ImageAlt    string
	ImageWidth  int
	ImageHeight int
	SiteName    string
	TwitterSite string
	NoIndex     bool
	JSONLD      template.JS
}

// metaInput is the small per-page description the builder works from.
type metaInput struct {
	Title       string // page title without the site suffix
	Description string
	Path        string // canonical path; defaults to the request path
	Query       string // canonical query string, without the leading "?"
	Type        string // og:type, defaults to "website"
	ImagePath   string // path of the OG card, defaults to the site card
	ImageAlt    string
	NoIndex     bool
	JSONLD      any // marshalled into a ld+json block when non-nil
}

// meta builds the final metadata for a request.
func (s *Server) meta(r *http.Request, in metaInput) PageMeta {
	base := s.baseURL(r)
	site := s.siteName()

	path := in.Path
	if path == "" {
		path = r.URL.Path
	}
	if path != "/" {
		path = "/" + strings.Trim(path, "/")
	}
	canonical := base + path
	if in.Query != "" {
		canonical += "?" + in.Query
	}

	imagePath := in.ImagePath
	if imagePath == "" {
		imagePath = "/og.png"
	}

	ogType := in.Type
	if ogType == "" {
		ogType = "website"
	}

	desc := metaDescription(in.Description)
	if desc == "" {
		desc = metaDescription(s.siteDescription())
	}

	m := PageMeta{
		Title:       pageTitle(in.Title, site),
		Description: desc,
		Canonical:   canonical,
		Type:        ogType,
		Image:       base + imagePath,
		ImageAlt:    squashSpace(in.ImageAlt),
		ImageWidth:  ogWidth,
		ImageHeight: ogHeight,
		SiteName:    site,
		TwitterSite: s.twitterSite(),
		// A credential-protected registry is private by definition; letting a
		// crawler index its URLs would leak the shape of someone's library even
		// though the bodies are behind auth.
		NoIndex: in.NoIndex || s.opts.Auth.Enabled(),
	}
	if m.ImageAlt == "" {
		m.ImageAlt = m.Title
	}
	if in.JSONLD != nil && !m.NoIndex {
		// json.Marshal escapes <, > and & to \u00xx, so the payload cannot break
		// out of the surrounding <script> element.
		if b, err := json.Marshal(in.JSONLD); err == nil {
			m.JSONLD = template.JS(b)
		} else {
			logError("marshal json-ld", err)
		}
	}
	return m
}

// ---------- route-driven metadata ----------

// pageMeta maps a request onto its metadata. Editor, import and error routes
// are marked noindex; everything a visitor could legitimately share is not.
func (s *Server) pageMeta(r *http.Request) PageMeta {
	p := "/" + strings.Trim(r.URL.Path, "/")
	if r.URL.Path == "/" {
		p = "/"
	}

	switch {
	case p == "/":
		return s.homeMeta(r)
	case p == "/skills":
		return s.skillsMeta(r)
	case strings.HasPrefix(p, "/skills/"):
		return s.skillRouteMeta(r, strings.TrimPrefix(p, "/skills/"))
	case p == "/bundles":
		return s.bundlesMeta(r)
	case strings.HasPrefix(p, "/bundles/"):
		return s.bundleRouteMeta(r, strings.TrimPrefix(p, "/bundles/"))
	default:
		// Import, unknown paths and anything rendered by renderError.
		return s.meta(r, metaInput{
			Title:   titleForUnindexedPath(p),
			NoIndex: true,
		})
	}
}

func titleForUnindexedPath(p string) string {
	switch {
	case p == "/import":
		return "Import skills"
	case strings.HasPrefix(p, "/import/github"):
		return "Import from GitHub"
	case strings.HasPrefix(p, "/import"):
		return "Import skills"
	default:
		return ""
	}
}

func (s *Server) homeMeta(r *http.Request) PageMeta {
	base := s.baseURL(r)
	site := s.siteName()
	return s.meta(r, metaInput{
		Path:        "/",
		Description: s.siteDescription(),
		ImageAlt:    site,
		JSONLD: map[string]any{
			"@context":    "https://schema.org",
			"@type":       "WebSite",
			"name":        site,
			"url":         base + "/",
			"description": metaDescription(s.siteDescription()),
			"potentialAction": map[string]any{
				"@type":       "SearchAction",
				"target":      map[string]any{"@type": "EntryPoint", "urlTemplate": base + "/skills?q={search_term_string}"},
				"query-input": "required name=search_term_string",
			},
		},
	})
}

func (s *Server) skillsMeta(r *http.Request) PageMeta {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))

	in := metaInput{
		Path:  "/skills",
		Title: "Skills",
		Description: fmt.Sprintf("Every skill in this %s registry — %d in total, searchable by name, description and tag.",
			s.siteName(), s.store.SkillCount()),
	}
	switch {
	case q != "":
		// A search result page is thin, duplicative and infinite. Keep it out of
		// the index but let the canonical point at the real list.
		in.Title = "Search: " + q
		in.Description = fmt.Sprintf("Skills matching %q.", q)
		in.NoIndex = true
	case tag != "":
		// A tag view is a real, stable collection, so it earns its own URL.
		in.Query = "tag=" + url.QueryEscape(tag)
		in.Title = "Skills tagged " + tag
		in.Description = fmt.Sprintf("Skills tagged %q in this %s registry.", tag, s.siteName())
	}
	if !in.NoIndex {
		in.JSONLD = s.collectionLD(r, in.Title, in.Description, "/skills")
	}
	return s.meta(r, in)
}

// skillRouteMeta covers /skills/{name} and everything nested under it.
func (s *Server) skillRouteMeta(r *http.Request, rest string) PageMeta {
	name, sub, _ := strings.Cut(rest, "/")

	// /skills/new and /skills/{name}/{edit,files,...} are editor surfaces.
	if name == "new" || name == "bulk" || sub != "" {
		title := "Edit skill"
		if name == "new" {
			title = "New skill"
		} else if sk, err := s.store.Skill(name); err == nil {
			title = "Edit " + sk.Name
		}
		return s.meta(r, metaInput{Title: title, NoIndex: true})
	}

	sk, err := s.store.Skill(name)
	if err != nil {
		// The 404 page renders through the same base(); never index it.
		return s.meta(r, metaInput{Title: "Not found", NoIndex: true})
	}

	return s.meta(r, metaInput{
		Path:        "/skills/" + sk.Name,
		Title:       sk.Name,
		Description: sk.Description,
		Type:        "article",
		ImagePath:   ogPath("skill", sk.Name),
		ImageAlt:    sk.Name + " — " + metaDescription(sk.Description),
		JSONLD:      s.skillLD(r, sk),
	})
}

func (s *Server) bundlesMeta(r *http.Request) PageMeta {
	in := metaInput{
		Path:  "/bundles",
		Title: "Folders",
		Description: fmt.Sprintf("Skill folders in this %s registry — %d groups you can install together in one command.",
			s.siteName(), s.store.BundleCount()),
	}
	in.JSONLD = s.collectionLD(r, in.Title, in.Description, "/bundles")
	return s.meta(r, in)
}

func (s *Server) bundleRouteMeta(r *http.Request, rest string) PageMeta {
	name, sub, _ := strings.Cut(rest, "/")

	if name == "new" || sub != "" {
		title := "Edit folder"
		if name == "new" {
			title = "New folder"
		} else if b, err := s.store.Bundle(name); err == nil {
			title = "Edit " + b.DisplayTitle()
		}
		return s.meta(r, metaInput{Title: title, NoIndex: true})
	}

	b, err := s.store.Bundle(name)
	if err != nil {
		return s.meta(r, metaInput{Title: "Not found", NoIndex: true})
	}

	skills := s.store.BundleSkills(b)
	desc := b.Description
	if strings.TrimSpace(desc) == "" {
		desc = fmt.Sprintf("%d %s grouped as %s, installable together in one command.",
			len(skills), plural(len(skills), "skill", "skills"), b.DisplayTitle())
	}
	return s.meta(r, metaInput{
		Path:        "/bundles/" + b.Name,
		Title:       b.DisplayTitle(),
		Description: desc,
		ImagePath:   ogPath("folder", b.Name),
		ImageAlt:    b.DisplayTitle(),
		JSONLD:      s.bundleLD(r, b, skills, desc),
	})
}

// ---------- structured data ----------

func (s *Server) websiteRefLD(r *http.Request) map[string]any {
	return map[string]any{
		"@type": "WebSite",
		"name":  s.siteName(),
		"url":   s.baseURL(r) + "/",
	}
}

func (s *Server) collectionLD(r *http.Request, name, desc, path string) map[string]any {
	return map[string]any{
		"@context":    "https://schema.org",
		"@type":       "CollectionPage",
		"name":        name,
		"description": metaDescription(desc),
		"url":         s.baseURL(r) + path,
		"isPartOf":    s.websiteRefLD(r),
	}
}

func (s *Server) skillLD(r *http.Request, sk *store.Skill) map[string]any {
	ld := map[string]any{
		"@context":            "https://schema.org",
		"@type":               "SoftwareApplication",
		"name":                sk.Name,
		"description":         metaDescription(sk.Description),
		"url":                 s.baseURL(r) + "/skills/" + sk.Name,
		"applicationCategory": "DeveloperApplication",
		"operatingSystem":     "Any",
		"isPartOf":            s.websiteRefLD(r),
		// A SoftwareApplication without an offer is valid but ineligible for
		// rich results; these are free, so say so.
		"offers": map[string]any{"@type": "Offer", "price": "0", "priceCurrency": "USD"},
	}
	if !sk.UpdatedAt.IsZero() {
		ld["dateModified"] = sk.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if len(sk.Tags) > 0 {
		ld["keywords"] = strings.Join(sk.Tags, ", ")
	}
	if sk.Front.License != "" {
		ld["license"] = sk.Front.License
	}
	return ld
}

func (s *Server) bundleLD(r *http.Request, b *store.Bundle, skills []*store.Skill, desc string) map[string]any {
	base := s.baseURL(r)
	items := make([]map[string]any, 0, len(skills))
	for i, sk := range skills {
		items = append(items, map[string]any{
			"@type":    "ListItem",
			"position": i + 1,
			"name":     sk.Name,
			"url":      base + "/skills/" + sk.Name,
		})
	}
	return map[string]any{
		"@context":    "https://schema.org",
		"@type":       "CollectionPage",
		"name":        b.DisplayTitle(),
		"description": metaDescription(desc),
		"url":         base + "/bundles/" + b.Name,
		"isPartOf":    s.websiteRefLD(r),
		"mainEntity": map[string]any{
			"@type":           "ItemList",
			"numberOfItems":   len(items),
			"itemListElement": items,
		},
	}
}

// ---------- routes ----------

// registerMetaRoutes wires every SEO surface in one call so server.go only
// needs a single line.
func (s *Server) registerMetaRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /robots.txt", s.handleRobots)
	m.HandleFunc("GET /sitemap.xml", s.handleSitemap)
	m.HandleFunc("GET /og.png", s.handleOGDefault)
	m.HandleFunc("GET /og/{kind}/{name}", s.handleOGImage)
}

// disallowedPrefixes are the editor and machine surfaces that must never be
// crawled. They mirror the noindex decisions in pageMeta.
var disallowedPrefixes = []string{
	"/api/",
	"/import",
	"/skills/new",
	"/skills/bulk",
	"/bundles/new",
	"/.claude-plugin/",
}

func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")

	var b strings.Builder
	b.WriteString("User-agent: *\n")
	if s.opts.Auth.Enabled() {
		// Private deployment: nothing here is for a crawler.
		b.WriteString("Disallow: /\n")
		fmt.Fprintf(w, "%s", b.String())
		return
	}
	b.WriteString("Allow: /\n")
	for _, p := range disallowedPrefixes {
		fmt.Fprintf(&b, "Disallow: %s\n", p)
	}
	// Wildcards are honoured by every major crawler and are the only way to
	// express "the edit page of any skill".
	b.WriteString("Disallow: /*/edit\n")
	b.WriteString("Disallow: /*/files/\n")
	b.WriteString("Disallow: /*/download\n")
	b.WriteString("Disallow: /*?q=\n")
	b.WriteString("\nSitemap: " + s.baseURL(r) + "/sitemap.xml\n")
	fmt.Fprintf(w, "%s", b.String())
}

type sitemapEntry struct {
	Loc        string `xml:"loc"`
	LastMod    string `xml:"lastmod,omitempty"`
	ChangeFreq string `xml:"changefreq,omitempty"`
	Priority   string `xml:"priority,omitempty"`
}

type sitemapURLSet struct {
	XMLName xml.Name       `xml:"urlset"`
	XMLNS   string         `xml:"xmlns,attr"`
	URLs    []sitemapEntry `xml:"url"`
}

func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	set := sitemapURLSet{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}

	if s.opts.Auth.Enabled() {
		// Still valid XML, just empty — a 404 here reads as a misconfiguration.
		writeSitemap(w, set)
		return
	}

	add := func(loc string, mod time.Time, freq, prio string) {
		e := sitemapEntry{Loc: loc, ChangeFreq: freq, Priority: prio}
		if !mod.IsZero() {
			e.LastMod = mod.UTC().Format("2006-01-02")
		}
		set.URLs = append(set.URLs, e)
	}

	skills := s.store.Skills()
	var newest time.Time
	for _, sk := range skills {
		if sk.UpdatedAt.After(newest) {
			newest = sk.UpdatedAt
		}
	}

	add(base+"/", newest, "daily", "1.0")
	add(base+"/skills", newest, "daily", "0.9")
	add(base+"/bundles", newest, "weekly", "0.8")
	for _, sk := range skills {
		add(base+"/skills/"+sk.Name, sk.UpdatedAt, "weekly", "0.7")
	}
	for _, b := range s.store.Bundles() {
		add(base+"/bundles/"+b.Name, b.UpdatedAt, "weekly", "0.6")
	}

	writeSitemap(w, set)
}

func writeSitemap(w http.ResponseWriter, set sitemapURLSet) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if _, err := w.Write([]byte(xml.Header)); err != nil {
		logError("write sitemap", err)
		return
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(set); err != nil {
		logError("encode sitemap", err)
	}
}
