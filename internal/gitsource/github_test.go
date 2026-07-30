package gitsource

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub is a stand-in for api.github.com. Each field overrides one
// endpoint; the zero value serves a plausible repository with no skills.
type fakeGitHub struct {
	t *testing.T

	metaStatus   int    // non-zero to fail GET /repos/{o}/{r}
	metaBody     string // overrides the default metadata JSON
	tarStatus    int    // non-zero to fail the tarball
	tarball      []byte
	commitsFor   map[string]string // repo path -> RFC3339 commit date
	commitStatus int

	mu       sync.Mutex
	requests []*http.Request
	headers  []http.Header
}

func (f *fakeGitHub) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	f.headers = append(f.headers, r.Header.Clone())
}

// count returns how many requests hit paths containing substr.
func (f *fakeGitHub) count(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.Contains(r.URL.Path, substr) {
			n++
		}
	}
	return n
}

func (f *fakeGitHub) firstHeader(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.headers) == 0 {
		return ""
	}
	return f.headers[0].Get(key)
}

const defaultMetaJSON = `{
  "full_name": "emilkowalski/skills",
  "description": "A pile of skills",
  "default_branch": "main",
  "pushed_at": "2026-01-02T03:04:05Z",
  "stargazers_count": 1234,
  "html_url": "https://github.com/emilkowalski/skills"
}`

func (f *fakeGitHub) start() *Client {
	f.t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		p := r.URL.Path
		switch {
		case strings.Contains(p, "/tarball"):
			switch {
			case f.tarStatus == http.StatusForbidden:
				rateLimited(w, f.tarStatus)
				return
			case f.tarStatus != 0:
				failure(w, f.tarStatus, "Not Found")
				return
			}
			w.Header().Set("Content-Type", "application/x-gzip")
			w.Write(f.tarball)

		case strings.HasSuffix(p, "/commits"):
			if f.commitStatus != 0 {
				rateLimited(w, f.commitStatus)
				return
			}
			date, ok := f.commitsFor[r.URL.Query().Get("path")]
			if !ok {
				w.Write([]byte(`[]`))
				return
			}
			fmt.Fprintf(w, `[{"sha":"deadbeef","commit":{"committer":{"date":%q}}}]`, date)

		default: // GET /repos/{owner}/{repo}
			switch {
			case f.metaStatus == http.StatusForbidden:
				rateLimited(w, f.metaStatus)
			case f.metaStatus != 0:
				failure(w, f.metaStatus, "boom")
			default:
				body := f.metaBody
				if body == "" {
					body = defaultMetaJSON
				}
				w.Write([]byte(body))
			}
		}
	}))
	f.t.Cleanup(srv.Close)

	return &Client{BaseURL: srv.URL, HTTP: srv.Client()}
}

func failure(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"message":%q}`, msg)
}

func rateLimited(w http.ResponseWriter, status int) {
	w.Header().Set("X-RateLimit-Remaining", "0")
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(37*time.Minute).Unix(), 10))
	failure(w, status, "API rate limit exceeded for 1.2.3.4.")
}

func sampleRepo(t *testing.T) []byte {
	return repoTarGz(t, "emilkowalski-skills-7fd1a60b01f91b314f59955a4e4d4e80d8edf11d", map[string]string{
		"README.md":                    "a repo of skills",
		"skills/foo/SKILL.md":          skillMD("foo", "does foo when asked"),
		"skills/foo/references/a.md":   "reference a",
		"skills/foo/node_modules/x.js": "junk",
		"skills/bar/SKILL.md":          skillMD("bar", "does bar when asked"),
		"nested/deep/baz/SKILL.md":     skillMD("baz", "does baz when asked"),
	})
}

func testRepo() Repo { return Repo{Owner: "emilkowalski", Name: "skills"} }

func TestFetch(t *testing.T) {
	f := &fakeGitHub{
		t:       t,
		tarball: sampleRepo(t),
		commitsFor: map[string]string{
			"skills/foo": "2026-02-03T04:05:06Z",
		},
	}
	c := f.start()

	res, err := c.Fetch(context.Background(), testRepo())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if res.Meta.FullName != "emilkowalski/skills" ||
		res.Meta.Description != "A pile of skills" ||
		res.Meta.Stars != 1234 ||
		res.Meta.HTMLURL != "https://github.com/emilkowalski/skills" {
		t.Errorf("meta = %+v", res.Meta)
	}
	if res.Meta.Ref != "main" {
		t.Errorf("ref = %q, want the default branch", res.Meta.Ref)
	}
	if res.Meta.CommitSHA != "7fd1a60b01f91b314f59955a4e4d4e80d8edf11d" {
		t.Errorf("commit = %q, want the sha from the archive root", res.Meta.CommitSHA)
	}
	if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC); !res.Meta.UpdatedAt.Equal(want) {
		t.Errorf("meta.UpdatedAt = %v, want %v", res.Meta.UpdatedAt, want)
	}

	if len(res.Candidates) != 3 {
		t.Fatalf("found %d candidates, want 3", len(res.Candidates))
	}
	paths := []string{res.Candidates[0].Path, res.Candidates[1].Path, res.Candidates[2].Path}
	if !equal(paths, []string{"nested/deep/baz", "skills/bar", "skills/foo"}) {
		t.Fatalf("paths = %v", paths)
	}

	foo := res.Candidates[2]
	if foo.Name != "foo" || foo.Description != "does foo when asked" {
		t.Errorf("foo = %q / %q", foo.Name, foo.Description)
	}
	if !equal(keys(foo.Files), []string{"SKILL.md", "references/a.md"}) {
		t.Errorf("foo files = %v, want node_modules excluded", keys(foo.Files))
	}
	if foo.Size == 0 {
		t.Error("foo.Size = 0")
	}
	if want := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC); !foo.UpdatedAt.Equal(want) {
		t.Errorf("foo.UpdatedAt = %v, want the last commit touching its path", foo.UpdatedAt)
	}
	// A path with no commit answer falls back to the repository's push time.
	bar := res.Candidates[1]
	if !bar.UpdatedAt.Equal(res.Meta.UpdatedAt) {
		t.Errorf("bar.UpdatedAt = %v, want the repo pushed_at fallback", bar.UpdatedAt)
	}

	// The whole repository must cost exactly one tarball request.
	if n := f.count("/tarball"); n != 1 {
		t.Errorf("%d tarball requests, want 1", n)
	}
}

func TestFetchUsesExplicitRef(t *testing.T) {
	f := &fakeGitHub{t: t, tarball: sampleRepo(t)}
	c := f.start()

	repo := testRepo()
	repo.Ref = "release/1.0"
	res, err := c.Fetch(context.Background(), repo)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Meta.Ref != "release/1.0" {
		t.Errorf("ref = %q", res.Meta.Ref)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	var tarPath string
	for _, r := range f.requests {
		if strings.Contains(r.URL.Path, "/tarball") {
			tarPath = r.URL.Path
		}
	}
	if want := "/repos/emilkowalski/skills/tarball/release/1.0"; tarPath != want {
		t.Errorf("tarball path = %q, want %q", tarPath, want)
	}
}

// A repository whose metadata carries no default branch still fetches: the
// tarball endpoint picks HEAD when no ref is given.
func TestFetchWithoutDefaultBranch(t *testing.T) {
	f := &fakeGitHub{t: t, tarball: sampleRepo(t), metaBody: `{"full_name":"emilkowalski/skills"}`}
	c := f.start()

	res, err := c.Fetch(context.Background(), testRepo())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Candidates) != 3 {
		t.Fatalf("found %d candidates, want 3", len(res.Candidates))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if strings.Contains(r.URL.Path, "/tarball") && r.URL.Path != "/repos/emilkowalski/skills/tarball" {
			t.Errorf("tarball path = %q, want no ref", r.URL.Path)
		}
	}
}

func TestFetchSubpath(t *testing.T) {
	f := &fakeGitHub{t: t, tarball: sampleRepo(t)}
	c := f.start()

	repo := testRepo()
	repo.Subpath = "skills/foo"
	res, err := c.Fetch(context.Background(), repo)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Candidates) != 1 || res.Candidates[0].Path != "skills/foo" {
		t.Fatalf("candidates = %+v, want only skills/foo", res.Candidates)
	}
}

func TestFetchSubpathWithNothingUnderIt(t *testing.T) {
	f := &fakeGitHub{t: t, tarball: sampleRepo(t)}
	c := f.start()

	repo := testRepo()
	repo.Subpath = "skills/nope"
	if _, err := c.Fetch(context.Background(), repo); err == nil ||
		!strings.Contains(err.Error(), "no files found under") {
		t.Fatalf("err = %v, want a clear empty-subpath error", err)
	}
}

// Metadata is a nice-to-have. Losing it must not cost the user the import.
func TestFetchDegradesWhenMetadataFails(t *testing.T) {
	f := &fakeGitHub{t: t, metaStatus: http.StatusInternalServerError, tarball: sampleRepo(t)}
	c := f.start()

	res, err := c.Fetch(context.Background(), testRepo())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Candidates) != 3 {
		t.Fatalf("found %d candidates, want 3", len(res.Candidates))
	}
	if res.Meta.FullName != "emilkowalski/skills" {
		t.Errorf("FullName = %q, want it derived from the repo", res.Meta.FullName)
	}
	if res.Meta.HTMLURL != "https://github.com/emilkowalski/skills" {
		t.Errorf("HTMLURL = %q", res.Meta.HTMLURL)
	}
	if res.Meta.CommitSHA == "" {
		t.Error("CommitSHA is empty; it should still come from the archive root")
	}
	if res.Meta.Ref != "" {
		t.Errorf("Ref = %q, want empty when the default branch is unknown", res.Meta.Ref)
	}
	// With no default branch known, the tarball must be requested without a ref.
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if strings.Contains(r.URL.Path, "/tarball") && r.URL.Path != "/repos/emilkowalski/skills/tarball" {
			t.Errorf("tarball path = %q, want no ref", r.URL.Path)
		}
	}
}

// A rate-limited metadata call means every per-skill commit lookup would be
// rate-limited too, so none should be attempted.
func TestFetchSkipsCommitLookupsWhenRateLimited(t *testing.T) {
	f := &fakeGitHub{t: t, metaStatus: http.StatusForbidden, tarball: sampleRepo(t)}
	c := f.start()

	res, err := c.Fetch(context.Background(), testRepo())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Candidates) != 3 {
		t.Fatalf("found %d candidates, want 3", len(res.Candidates))
	}
	if n := f.count("/commits"); n != 0 {
		t.Errorf("%d commit requests after a rate limit, want 0", n)
	}
}

// A failing commit lookup is invisible: the candidate keeps the repo fallback.
func TestFetchToleratesFailingCommitLookups(t *testing.T) {
	f := &fakeGitHub{t: t, tarball: sampleRepo(t), commitStatus: http.StatusForbidden}
	c := f.start()

	res, err := c.Fetch(context.Background(), testRepo())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	for _, cand := range res.Candidates {
		if !cand.UpdatedAt.Equal(res.Meta.UpdatedAt) {
			t.Errorf("%s.UpdatedAt = %v, want the repo fallback", cand.Path, cand.UpdatedAt)
		}
	}
}

func TestFetchNotFound(t *testing.T) {
	f := &fakeGitHub{t: t, metaStatus: http.StatusNotFound, tarStatus: http.StatusNotFound}
	c := f.start()

	_, err := c.Fetch(context.Background(), testRepo())
	if err == nil {
		t.Fatal("want an error")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false", err)
	}
	for _, want := range []string{"not found or private", "GITHUB_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestFetchRateLimitError(t *testing.T) {
	f := &fakeGitHub{t: t, metaStatus: http.StatusForbidden, tarStatus: http.StatusForbidden}
	c := f.start()

	_, err := c.Fetch(context.Background(), testRepo())
	if err == nil {
		t.Fatal("want an error")
	}
	if !IsRateLimited(err) {
		t.Errorf("IsRateLimited(%v) = false", err)
	}
	for _, want := range []string{"rate limit reached", "resets at", "GITHUB_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestFetchWrapsNetworkErrorWithURL(t *testing.T) {
	// A server that is closed immediately: every request fails at the socket.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	srv.Close()

	_, err := c.Fetch(context.Background(), testRepo())
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), srv.URL+"/repos/emilkowalski/skills") {
		t.Errorf("error %q does not name the URL it tried", err)
	}
}

func TestRequestHeaders(t *testing.T) {
	f := &fakeGitHub{t: t, tarball: sampleRepo(t)}
	c := f.start()
	c.Token = "ghp_secret"

	if _, err := c.Fetch(context.Background(), testRepo()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.headers) == 0 {
		t.Fatal("no requests recorded")
	}
	for i, h := range f.headers {
		if got := h.Get("Authorization"); got != "Bearer ghp_secret" {
			t.Errorf("request %d Authorization = %q", i, got)
		}
		if got := h.Get("User-Agent"); got != "circe" {
			t.Errorf("request %d User-Agent = %q", i, got)
		}
		if got := h.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("request %d X-GitHub-Api-Version = %q", i, got)
		}
	}
}

func TestTokenFromEnvironment(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	if got := EnvToken(); got != "" {
		t.Fatalf("EnvToken() = %q with both unset", got)
	}

	t.Setenv("GH_TOKEN", "  gh_cli_token  ")
	if got := EnvToken(); got != "gh_cli_token" {
		t.Errorf("EnvToken() = %q, want the trimmed GH_TOKEN", got)
	}

	t.Setenv("GITHUB_TOKEN", "actions_token")
	if got := EnvToken(); got != "actions_token" {
		t.Errorf("EnvToken() = %q, want GITHUB_TOKEN to win", got)
	}

	f := &fakeGitHub{t: t, tarball: sampleRepo(t)}
	c := f.start() // no explicit token: it must come from the environment
	if _, err := c.Fetch(context.Background(), testRepo()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := f.firstHeader("Authorization"); got != "Bearer actions_token" {
		t.Errorf("Authorization = %q, want the environment token", got)
	}
}

func TestNoAuthorizationHeaderWithoutToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")

	f := &fakeGitHub{t: t, tarball: sampleRepo(t)}
	c := f.start()
	if _, err := c.Fetch(context.Background(), testRepo()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := f.firstHeader("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want none", got)
	}
}

func TestFetchRejectsInvalidRepo(t *testing.T) {
	if _, err := Fetch(context.Background(), Repo{Owner: "..", Name: "x"}); err == nil {
		t.Fatal("want an error for an invalid repository")
	}
}

func TestFetchHonoursContextCancellation(t *testing.T) {
	f := &fakeGitHub{t: t, tarball: sampleRepo(t)}
	c := f.start()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Fetch(ctx, testRepo()); err == nil {
		t.Fatal("want an error from a cancelled context")
	}
}
