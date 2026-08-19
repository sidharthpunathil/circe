package gitsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sidharthpunathil/circe/internal/store"
)

func TestParseRepo(t *testing.T) {
	ok := map[string]string{
		"owner/name":                              "owner/name",
		"  owner/name  ":                          "owner/name",
		"owner/name.git":                          "owner/name",
		"https://github.com/owner/name":           "owner/name",
		"https://github.com/owner/name.git":       "owner/name",
		"https://github.com/owner/name/tree/main": "owner/name",
		"git@github.com:owner/name.git":           "owner/name",
	}
	for in, want := range ok {
		got, err := ParseRepo(in)
		if err != nil {
			t.Errorf("ParseRepo(%q) = %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseRepo(%q) = %q, want %q", in, got, want)
		}
	}

	bad := []string{"", "   ", "owner", "/name", "owner/"}
	for _, in := range bad {
		if got, err := ParseRepo(in); err == nil {
			t.Errorf("ParseRepo(%q) = %q, want an error", in, got)
		}
	}

	// Sync writes through GitHub's API, so another host has to say so plainly
	// rather than fail later with a confusing 404.
	if _, err := ParseRepo("https://gitlab.com/owner/name"); err == nil {
		t.Error("ParseRepo(gitlab) = nil, want an error naming github.com")
	} else if !strings.Contains(err.Error(), "github.com") {
		t.Errorf("error should name github.com, got %v", err)
	}
}

func TestIsManaged(t *testing.T) {
	plain := Config{Repo: "o/n"}
	managed := []string{"skills/a/SKILL.md", "rules/x.md", "mcps/y.json", "bundles/z.json", "sources.json"}
	for _, p := range managed {
		if !isManaged(plain, p) {
			t.Errorf("isManaged(%q) = false, want true", p)
		}
	}
	// Anything the registry does not write is carried through a push untouched.
	for _, p := range []string{"README.md", "LICENSE", ".github/workflows/ci.yml", "docs/x.md", "skills.md"} {
		if isManaged(plain, p) {
			t.Errorf("isManaged(%q) = true, want false", p)
		}
	}

	nested := Config{Repo: "o/n", Path: "registry"}
	if !isManaged(nested, "registry/skills/a/SKILL.md") {
		t.Error("a nested registry must own its own skills/")
	}
	if isManaged(nested, "skills/a/SKILL.md") {
		t.Error("a nested registry must not claim skills/ at the repository root")
	}
}

func TestConfigHelpers(t *testing.T) {
	var zero Config
	if zero.Connected() {
		t.Error("the zero config must not look connected")
	}
	c := Config{Repo: "o/n", LastCommit: "0123456789abcdef"}
	if got := c.BranchOrDefault(); got != "main" {
		t.Errorf("BranchOrDefault = %q, want main", got)
	}
	trunk := Config{Repo: "o/n", Branch: "trunk"}
	if got := trunk.BranchOrDefault(); got != "trunk" {
		t.Errorf("BranchOrDefault = %q, want trunk", got)
	}
	if got := c.ShortCommit(); got != "0123456" {
		t.Errorf("ShortCommit = %q", got)
	}
	if got := c.CommitURL(); got != "https://github.com/o/n/commit/0123456789abcdef" {
		t.Errorf("CommitURL = %q", got)
	}
}

// ---------- a stand-in for GitHub ----------

// fakeGitHub implements just enough of the Git Data API to observe what a push
// actually sends: which blobs, which tree, and which ref it moves.
type fakeGitHub struct {
	t *testing.T

	// state
	headSHA   string
	headTree  string
	treeFiles []treeEntry // what the current head tree contains

	// observed
	blobs       map[string]string // sha -> content
	lastTree    []treeEntry
	lastMessage string
	lastParents []string
	refUpdates  int
	refCreates  int

	nextID int
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	return &fakeGitHub{t: t, blobs: map[string]string{}}
}

// commit records a branch move, so the next push sees the repository as it now
// stands rather than as it was.
func (f *fakeGitHub) commit(sha string) {
	f.headSHA = sha
	f.headTree = treeID(f.lastTree)
	f.treeFiles = f.lastTree
}

func (f *fakeGitHub) id(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s%040d", prefix, f.nextID)
}

func (f *fakeGitHub) serve() *httptest.Server {
	mux := http.NewServeMux()
	const base = "/repos/o/n"

	mux.HandleFunc(base+"/git/ref/heads/", func(w http.ResponseWriter, r *http.Request) {
		if f.headSHA == "" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"object": map[string]string{"sha": f.headSHA}})
	})

	mux.HandleFunc(base+"/git/commits/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"sha": f.headSHA, "tree": map[string]string{"sha": f.headTree}})
	})

	mux.HandleFunc(base+"/git/trees/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"tree": f.treeFiles, "truncated": false})
	})

	mux.HandleFunc(base+"/git/blobs", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Content, Encoding string }
		decode(f.t, r, &body)
		if body.Encoding != "base64" {
			f.t.Errorf("blob encoding = %q, want base64", body.Encoding)
		}
		raw, err := base64.StdEncoding.DecodeString(body.Content)
		if err != nil {
			f.t.Errorf("blob content is not base64: %v", err)
		}
		// Git blob ids are content addresses, so the stand-in must be one too:
		// a push that changes nothing depends on getting the same id back.
		sha := blobID(raw)
		f.blobs[sha] = string(raw)
		writeJSON(w, map[string]string{"sha": sha})
	})

	mux.HandleFunc(base+"/git/trees", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tree []treeEntry `json:"tree"`
		}
		decode(f.t, r, &body)
		f.lastTree = body.Tree

		// A tree with the same contents gets the same id, which is what lets a
		// push notice there is nothing to say.
		writeJSON(w, map[string]string{"sha": treeID(body.Tree)})
	})

	mux.HandleFunc(base+"/git/commits", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message string   `json:"message"`
			Tree    string   `json:"tree"`
			Parents []string `json:"parents"`
		}
		decode(f.t, r, &body)
		f.lastMessage, f.lastParents = body.Message, body.Parents
		writeJSON(w, map[string]string{"sha": f.id("c")})
	})

	mux.HandleFunc(base+"/git/refs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		}
		decode(f.t, r, &body)
		f.refCreates++
		f.commit(body.SHA)
		writeJSON(w, map[string]string{})
	})

	mux.HandleFunc(base+"/git/refs/heads/", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			SHA   string `json:"sha"`
			Force bool   `json:"force"`
		}
		decode(f.t, r, &body)
		if body.Force {
			f.t.Error("a sync must never force-push")
		}
		f.refUpdates++
		f.commit(body.SHA)
		writeJSON(w, map[string]string{})
	})

	return httptest.NewServer(mux)
}

// treeID is a content address for a tree, so an unchanged push is detectable.
func treeID(entries []treeEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, e.Path+":"+e.SHA)
	}
	sort.Strings(parts)
	return fmt.Sprintf("t%x", strings.Join(parts, "|"))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func decode(t *testing.T, r *http.Request, out any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		t.Fatalf("decode %s: %v", r.URL.Path, err)
	}
}

// pushedPaths lists what the tree the push built actually contains.
func (f *fakeGitHub) pushedPaths() []string {
	out := make([]string, 0, len(f.lastTree))
	for _, e := range f.lastTree {
		out = append(out, e.Path)
	}
	sort.Strings(out)
	return out
}

// ---------- push ----------

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestPushWritesTheWholeRegistry(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	fake := newFakeGitHub(t)
	srv := fake.serve()
	defer srv.Close()
	apiBase = srv.URL
	defer func() { apiBase = "https://api.github.com" }()

	st := newStore(t)
	if err := st.CreateSkill(&store.Skill{
		Name: "alpha", Description: "The first skill", Body: "Body\n",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteFile("alpha", "references/api.md", []byte("reference")); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRule(&store.Rule{
		Name: "house-style", Description: "How we write", Body: "Use tabs.\n",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateBundle(&store.Bundle{Name: "starter", Skills: []string{"alpha"}}); err != nil {
		t.Fatal(err)
	}

	// A repository that already holds something of its own.
	fake.headSHA, fake.headTree = "head1", "tree1"
	fake.treeFiles = []treeEntry{
		{Path: "README.md", Mode: "100644", Type: "blob", SHA: "readme-sha"},
		{Path: "skills/gone/SKILL.md", Mode: "100644", Type: "blob", SHA: "stale-sha"},
	}

	cfg := Config{Repo: "o/n"}
	res, err := Push(context.Background(), st, cfg, "")
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !res.Changed || res.Commit == "" {
		t.Fatalf("Push reported no change: %+v", res)
	}

	want := []string{
		"README.md",
		"bundles/starter.json",
		"rules/house-style.md",
		"skills/alpha/SKILL.md",
		"skills/alpha/references/api.md",
	}
	if got := fake.pushedPaths(); !equal(got, want) {
		t.Errorf("pushed tree = %v,\nwant %v", got, want)
	}
	if fake.refUpdates != 1 || fake.refCreates != 0 {
		t.Errorf("ref updates = %d, creates = %d; want 1 and 0", fake.refUpdates, fake.refCreates)
	}
	if !equal(fake.lastParents, []string{"head1"}) {
		t.Errorf("parents = %v, want [head1]", fake.lastParents)
	}
	if !strings.Contains(fake.lastMessage, "1 skill") {
		t.Errorf("default commit message = %q, want it to describe the registry", fake.lastMessage)
	}

	// Unchanged content is referenced, not re-uploaded.
	if _, ok := fake.blobs["readme-sha"]; ok {
		t.Error("a file the registry does not own must not be re-uploaded")
	}

	// Content, not just paths.
	var skillBody string
	for _, e := range fake.lastTree {
		if e.Path == "skills/alpha/SKILL.md" {
			skillBody = fake.blobs[e.SHA]
		}
	}
	if !strings.Contains(skillBody, "name: alpha") {
		t.Errorf("pushed SKILL.md = %q", skillBody)
	}
}

func TestPushSkipsAnUnchangedRepository(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	fake := newFakeGitHub(t)
	srv := fake.serve()
	defer srv.Close()
	apiBase = srv.URL
	defer func() { apiBase = "https://api.github.com" }()

	st := newStore(t)
	if err := st.CreateSkill(&store.Skill{Name: "alpha", Description: "The first skill", Body: "Body\n"}); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Repo: "o/n"}
	first, err := Push(context.Background(), st, cfg, "")
	if err != nil {
		t.Fatalf("first push: %v", err)
	}
	if !first.Changed {
		t.Fatal("the first push into an empty repository must commit")
	}
	if fake.refCreates != 1 {
		t.Errorf("an empty repository needs the branch created, got %d creates", fake.refCreates)
	}
	if len(fake.lastParents) != 0 {
		t.Errorf("the first commit has no parent, got %v", fake.lastParents)
	}

	second, err := Push(context.Background(), st, cfg, "")
	if err != nil {
		t.Fatalf("second push: %v", err)
	}
	if second.Changed {
		t.Error("pushing an unchanged registry must not make an empty commit")
	}
	if second.Commit != first.Commit {
		t.Errorf("commit moved on a no-op push: %q then %q", first.Commit, second.Commit)
	}
}

func TestPushNeedsATokenAndARepository(t *testing.T) {
	st := newStore(t)

	if _, err := Push(context.Background(), st, Config{}, ""); err == nil {
		t.Error("pushing with no repository = nil, want an error")
	}

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	_, err := Push(context.Background(), st, Config{Repo: "o/n"}, "")
	if err == nil {
		t.Fatal("pushing with no token = nil, want an error")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("the error should say a token is needed, got %v", err)
	}
}

func TestPushNestsUnderAConfiguredPath(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	fake := newFakeGitHub(t)
	srv := fake.serve()
	defer srv.Close()
	apiBase = srv.URL
	defer func() { apiBase = "https://api.github.com" }()

	st := newStore(t)
	if err := st.CreateSkill(&store.Skill{Name: "alpha", Description: "The first skill", Body: "Body\n"}); err != nil {
		t.Fatal(err)
	}

	if _, err := Push(context.Background(), st, Config{Repo: "o/n", Path: "registry"}, ""); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got := fake.pushedPaths(); !equal(got, []string{"registry/skills/alpha/SKILL.md"}) {
		t.Errorf("pushed tree = %v", got)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
