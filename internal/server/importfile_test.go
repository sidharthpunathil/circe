package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sidharthpunathil/circe/internal/gitsource"
	"github.com/sidharthpunathil/circe/internal/store"
)

// upload posts files to the preview step the way a browser does.
func upload(t *testing.T, s *Server, files map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for name, content := range files {
		part, err := w.CreateFormFile("files", name)
		if err != nil {
			t.Fatalf("create part: %v", err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatalf("write part: %v", err)
		}
	}
	w.Close()

	req := httptest.NewRequest(http.MethodPost, "/import/file", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// create posts the confirmation step.
func create(t *testing.T, s *Server, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/import/file/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestImportFilePreviewOffersTheRightKindPerFile(t *testing.T) {
	s := newTestServer(t)
	rec := upload(t, s, map[string]string{
		"SKILL.md":  "---\nname: tidy\ndescription: Tidies things up.\n---\n\nTidy it.\n",
		"CLAUDE.md": "Always run the tests.\n",
		".mcp.json": `{"mcpServers":{"github":{"command":"npx","args":["-y","server-github"],` +
			`"env":{"GITHUB_TOKEN":"${GITHUB_TOKEN}"}},"linear":{"type":"http","url":"https://mcp.linear.app/sse"}}}`,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status = %d", rec.Code)
	}
	body := rec.Body.String()

	// Four rows: one skill, one rule, two servers.
	if n := strings.Count(body, `name="body"`); n != 4 {
		t.Errorf("rows = %d, want 4", n)
	}
	if n := strings.Count(body, `name="kind"`); n != 4 {
		t.Errorf("kind fields = %d, want one per row", n)
	}
	if !strings.Contains(body, `<option value="rule:CLAUDE.md" selected>`) {
		t.Error("a CLAUDE.md should default to being a rule for CLAUDE.md")
	}
	if !strings.Contains(body, `<option value="skill" selected>`) {
		t.Error("a SKILL.md should default to being a skill")
	}
	if n := strings.Count(body, `<input type="hidden" name="kind" value="mcp" />`); n != 2 {
		t.Errorf("mcp rows = %d, want 2", n)
	}
}

func TestImportFileCreatesEachKind(t *testing.T) {
	s := newTestServer(t)
	form := url.Values{}
	form.Add("name", "tidy")
	form.Add("description", "Tidies things up.")
	form.Add("body", "Tidy it.\n")
	form.Add("kind", "skill")

	form.Add("name", "house-style")
	form.Add("description", "How we write here.")
	form.Add("body", "Short sentences.\n")
	form.Add("kind", "rule:AGENTS.md")

	form.Add("name", "github")
	form.Add("description", "The GitHub server.")
	form.Add("body", `{"mcpServers":{"github":{"command":"npx","env":{"GITHUB_TOKEN":"${GITHUB_TOKEN}"}}}}`)
	form.Add("kind", "mcp")

	rec := create(t, s, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, body %s", rec.Code, rec.Body.String())
	}

	if _, err := s.store.Skill("tidy"); err != nil {
		t.Errorf("skill not created: %v", err)
	}
	rule, err := s.store.Rule("house-style")
	if err != nil {
		t.Fatalf("rule not created: %v", err)
	}
	if got := rule.TargetsOrDefault(); len(got) != 1 || got[0] != "AGENTS.md" {
		t.Errorf("rule targets = %v, want [AGENTS.md]", got)
	}
	server, err := s.store.MCPServer("github")
	if err != nil {
		t.Fatalf("server not created: %v", err)
	}
	if server.Command != "npx" {
		t.Errorf("server command = %q, want npx", server.Command)
	}
	if len(server.Env) != 1 || server.Env[0].Name != "GITHUB_TOKEN" {
		t.Errorf("server env = %+v, want GITHUB_TOKEN declared", server.Env)
	}
	if server.Description != "The GitHub server." {
		t.Errorf("server description = %q", server.Description)
	}
}

func TestImportFileLeavesExistingThingsAlone(t *testing.T) {
	s := newTestServer(t)
	form := url.Values{}
	form.Add("name", "code-review") // newTestServer already has this skill
	form.Add("description", "Something else entirely.")
	form.Add("body", "Replace me.\n")
	form.Add("kind", "skill")

	if rec := create(t, s, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d", rec.Code)
	}
	sk, err := s.store.Skill("code-review")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sk.Body, "Replace me") {
		t.Error("an existing skill was overwritten without being asked")
	}

	form.Set("overwrite", "1")
	if rec := create(t, s, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d", rec.Code)
	}
	sk, err = s.store.Skill("code-review")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sk.Body, "Replace me") {
		t.Error("overwrite was chosen and the skill was still not replaced")
	}
}

func TestImportFileRejectsMismatchedRows(t *testing.T) {
	s := newTestServer(t)
	form := url.Values{}
	form.Add("name", "one")
	form.Add("name", "two")
	form.Add("description", "Only one description.")
	form.Add("body", "Only one body.")
	form.Add("kind", "skill")

	rec := create(t, s, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	if _, err := s.store.Skill("one"); err == nil {
		t.Error("a malformed submission must not write anything")
	}
}

func TestImportTallyMessage(t *testing.T) {
	cases := []struct {
		tally importTally
		where string
		want  string
		kind  string
	}{
		{importTally{Skills: 3}, "from owner/repo", "Imported 3 skills from owner/repo.", "ok"},
		{importTally{Skills: 1, Rules: 2, Servers: 1}, "",
			"Imported 1 skill, 2 rules and 1 MCP server.", "ok"},
		{importTally{Rules: 1, Skipped: 2}, "", "Imported 1 rule. Skipped 2 already in your registry.", "ok"},
		{importTally{}, "", "Nothing to do.", "ok"},
		{importTally{Failed: []string{"foo: broke"}}, "", "1 problem: foo: broke", "error"},
	}
	for _, c := range cases {
		msg, kind := c.tally.message(c.where)
		if msg != c.want || kind != c.kind {
			t.Errorf("message() = %q/%s, want %q/%s", msg, kind, c.want, c.kind)
		}
	}
}

// The repo page has to hold three groups at once, and has to not look broken
// when a repository only has one of them.
func TestRepoPageGroupsEveryKind(t *testing.T) {
	s := newTestServer(t)
	view := githubView{
		base:    base{Build: "test"},
		RepoURL: "https://github.com/owner/repo",
		Candidates: []ghCandidate{{
			Candidate: gitsource.Candidate{
				Name: "code-review", Description: "Reviews a diff.", Path: "skills/code-review", Size: 42,
			},
			FileCount: 2, Exists: true,
		}},
		Rules: []ghRule{{FoundRule: store.FoundRule{
			Name: "house-style", Description: "How we write.", Target: store.TargetAgents, Path: "AGENTS.md",
		}}},
		Servers: []ghServer{{FoundMCP: store.FoundMCP{
			Server: &store.MCPServer{
				Name: "github", Description: "The GitHub server.", Transport: store.TransportStdio,
				Command: "npx", Env: []store.EnvVar{{Name: "GITHUB_TOKEN", Required: true}},
			},
			Path: ".mcp.json",
		}}},
	}

	rec := httptest.NewRecorder()
	s.tmpl.render(rec, httptest.NewRequest(http.MethodGet, "/import/repo", nil), http.StatusOK, "repo", view)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, body)
	}
	for _, want := range []string{
		"Skills · 1", "Rules · 1", "MCP servers · 1",
		`name="names"`, `value="code-review"`,
		`name="rules"`, `value="house-style"`,
		`name="mcps"`, `value="github"`,
		"AGENTS.md", "needs GITHUB_TOKEN in your environment", "3 things found",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("repo page missing %q", want)
		}
	}

	// Only rules: no empty skill or server headings.
	view.Candidates, view.Servers = nil, nil
	rec = httptest.NewRecorder()
	s.tmpl.render(rec, httptest.NewRequest(http.MethodGet, "/import/repo", nil), http.StatusOK, "repo", view)
	body = rec.Body.String()
	if strings.Contains(body, "Skills · 0") || strings.Contains(body, "MCP servers · 0") {
		t.Error("a repo with only rules should not render empty groups")
	}
	if !strings.Contains(body, "1 thing found") {
		t.Error("the count should read naturally for a single item")
	}
}
