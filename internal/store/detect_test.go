package store

import (
	"os"
	"path/filepath"
	"testing"
)

// sampleTree is a repository that contains one of everything, plus the things
// that must be left alone.
func sampleTree() map[string][]byte {
	return map[string][]byte{
		"README.md": []byte("# The repo\n\nNot a rule.\n"),
		"CLAUDE.md": []byte("Always run the tests before you push.\n"),
		"AGENTS.md": []byte("Keep commits small.\n"),
		".mcp.json": []byte(`{
		  "mcpServers": {
		    "github": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"],
		              "env": {"GITHUB_TOKEN": "${GITHUB_TOKEN}"}},
		    "linear": {"type": "http", "url": "https://mcp.linear.app/sse"}
		  }
		}`),
		"rules/foo.md":                         []byte("---\ndescription: How we write Go.\n---\n\nUse table tests.\n"),
		"rules/README.md":                      []byte("Notes about the rules folder.\n"),
		"skills/code-review/SKILL.md":          []byte("---\nname: code-review\ndescription: Reviews a diff.\n---\n\nDo it.\n"),
		"skills/code-review/references/api.md": []byte("Reference material.\n"),
		"docs/guide.md":                        []byte("# Guide\n\nProse.\n"),
		"node_modules/pkg/SKILL.md":            []byte("---\nname: vendored\ndescription: No.\n---\n\nNo.\n"),
		"node_modules/pkg/CLAUDE.md":           []byte("No.\n"),
		".venv/lib/rules/bad.md":               []byte("No.\n"),
		".github/workflows/ci.yml":             []byte("name: ci\n"),
	}
}

func TestDetectContentFindsEachKind(t *testing.T) {
	found := DetectContent(sampleTree(), "skillbuilder")

	if got := len(found.Skills); got != 1 {
		t.Fatalf("skills = %d, want 1: %+v", got, found.Skills)
	}
	if got := found.Skills[0].Name; got != "code-review" {
		t.Errorf("skill name = %q, want code-review", got)
	}
	if got := found.Skills[0].Path; got != "skills/code-review" {
		t.Errorf("skill path = %q, want skills/code-review", got)
	}

	// CLAUDE.md, AGENTS.md and rules/foo.md. rules/README.md is a rule too:
	// it is markdown inside a rules directory, which is an explicit statement
	// of intent, unlike the README at the root.
	wantRules := map[string]string{
		"skillbuilder":        TargetClaude,
		"skillbuilder-agents": TargetAgents,
		"foo":                 TargetClaude,
		"skillbuilder-readme": TargetClaude,
	}
	if len(found.Rules) != len(wantRules) {
		t.Fatalf("rules = %d, want %d: %+v", len(found.Rules), len(wantRules), ruleNames(found))
	}
	for _, r := range found.Rules {
		target, ok := wantRules[r.Name]
		if !ok {
			t.Errorf("unexpected rule %q from %s", r.Name, r.Path)
			continue
		}
		if r.Target != target {
			t.Errorf("rule %q target = %q, want %q", r.Name, r.Target, target)
		}
		if r.Body == "" {
			t.Errorf("rule %q has no body", r.Name)
		}
		if r.Description == "" {
			t.Errorf("rule %q has no description", r.Name)
		}
	}

	if got := len(found.MCPs); got != 2 {
		t.Fatalf("mcp servers = %d, want 2: %+v", got, found.MCPs)
	}
	byName := map[string]*MCPServer{}
	for _, m := range found.MCPs {
		byName[m.Server.Name] = m.Server
		if m.Path != ".mcp.json" {
			t.Errorf("server %q path = %q, want .mcp.json", m.Server.Name, m.Path)
		}
	}
	gh, ok := byName["github"]
	if !ok {
		t.Fatalf("no github server: %v", byName)
	}
	if gh.Command != "npx" || gh.TransportOrDefault() != TransportStdio {
		t.Errorf("github server = %+v", gh)
	}
	if len(gh.Env) != 1 || gh.Env[0].Name != "GITHUB_TOKEN" {
		t.Errorf("github env = %+v, want GITHUB_TOKEN declared", gh.Env)
	}
	linear, ok := byName["linear"]
	if !ok {
		t.Fatalf("no linear server: %v", byName)
	}
	if linear.TransportOrDefault() != TransportHTTP || linear.URL == "" {
		t.Errorf("linear server = %+v", linear)
	}
}

func TestDetectContentIgnoresEverythingElse(t *testing.T) {
	found := DetectContent(sampleTree(), "skillbuilder")

	for _, r := range found.Rules {
		switch r.Path {
		case "README.md", "docs/guide.md":
			t.Errorf("%s must not become a rule", r.Path)
		}
		if got := r.Path; len(got) >= 12 && got[:12] == "node_modules" {
			t.Errorf("%s must not become a rule", got)
		}
	}
	for _, s := range found.Skills {
		if s.Path == "node_modules/pkg" {
			t.Error("a SKILL.md under node_modules must not become a skill")
		}
	}
	// A skill's own files belong to the skill, not to the registry twice over.
	tree := sampleTree()
	tree["skills/code-review/CLAUDE.md"] = []byte("Skill-local guidance.\n")
	tree["skills/code-review/.mcp.json"] = []byte(`{"mcpServers":{"local":{"command":"x"}}}`)
	found = DetectContent(tree, "skillbuilder")
	for _, r := range found.Rules {
		if r.Path == "skills/code-review/CLAUDE.md" {
			t.Error("a CLAUDE.md inside a skill must not become a separate rule")
		}
	}
	for _, m := range found.MCPs {
		if m.Path == "skills/code-review/.mcp.json" {
			t.Error("an .mcp.json inside a skill must not become a separate server")
		}
	}
}

func TestDetectContentNamesAreUniqueAndStable(t *testing.T) {
	tree := map[string][]byte{
		"CLAUDE.md":                   []byte("Root guidance.\n"),
		"backend/CLAUDE.md":           []byte("Backend guidance.\n"),
		"backend/AGENTS.md":           []byte("Backend agents.\n"),
		"rules/style.md":              []byte("Style.\n"),
		"packages/api/rules/style.md": []byte("API style.\n"),
		"mcp.json":                    []byte(`{"mcpServers":{"github":{"command":"npx"}}}`),
		"tools/.mcp.json":             []byte(`{"mcpServers":{"github":{"command":"uvx"}}}`),
	}

	first := DetectContent(tree, "acme")
	want := []string{"acme", "backend", "backend-agents", "style", "api-style"}
	got := ruleNames(first)
	if len(got) != len(want) {
		t.Fatalf("rule names = %v, want %v", got, want)
	}
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n] {
			t.Errorf("duplicate rule name %q in %v", n, got)
		}
		seen[n] = true
	}
	for _, n := range want {
		if !seen[n] {
			t.Errorf("missing rule name %q in %v", n, got)
		}
	}

	if len(first.MCPs) != 2 {
		t.Fatalf("mcp servers = %d, want 2", len(first.MCPs))
	}
	if a, b := first.MCPs[0].Server.Name, first.MCPs[1].Server.Name; a == b {
		t.Errorf("two servers both named %q", a)
	}

	// The preview and the import that follows it are separate passes over the
	// same tree, so the second pass has to name everything identically.
	second := DetectContent(tree, "acme")
	for i, r := range second.Rules {
		if r.Name != first.Rules[i].Name || r.Path != first.Rules[i].Path {
			t.Fatalf("second pass disagreed: %+v vs %+v", second.Rules, first.Rules)
		}
	}
	for i, m := range second.MCPs {
		if m.Server.Name != first.MCPs[i].Server.Name {
			t.Fatalf("second pass renamed a server: %q vs %q", m.Server.Name, first.MCPs[i].Server.Name)
		}
	}
}

func TestDetectContentRootNamesUseTheFallback(t *testing.T) {
	tree := map[string][]byte{
		"CLAUDE.md": []byte("Root guidance.\n"),
		"SKILL.md":  []byte("The whole repo is one skill.\n"),
	}
	found := DetectContent(tree, "my-repo")
	if len(found.Skills) != 1 || found.Skills[0].Name != "my-repo" {
		t.Errorf("skills = %+v, want one named my-repo", found.Skills)
	}
	// The SKILL.md at the root makes the whole tree that skill, so its CLAUDE.md
	// belongs to it.
	if len(found.Rules) != 0 {
		t.Errorf("rules = %+v, want none", found.Rules)
	}

	found = DetectContent(map[string][]byte{"CLAUDE.md": []byte("Root guidance.\n")}, "my-repo")
	if len(found.Rules) != 1 || found.Rules[0].Name != "my-repo" {
		t.Errorf("rules = %+v, want one named my-repo", found.Rules)
	}
	// With nothing to fall back on, the filename is all there is.
	found = DetectContent(map[string][]byte{"CLAUDE.md": []byte("Root guidance.\n")}, "")
	if len(found.Rules) != 1 || found.Rules[0].Name != "claude" {
		t.Errorf("rules = %+v, want one named claude", found.Rules)
	}
}

// TestDetectContentOnDisk walks a real directory, symlinks and all, to prove the
// same rules hold for a tree that is not already in memory.
func TestDetectContentOnDisk(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("CLAUDE.md", "Root guidance.\n")
	write("AGENTS.md", "Agent guidance.\n")
	write("mcp.json", `{"mcpServers":{"github":{"command":"npx"},"linear":{"url":"https://mcp.linear.app/sse"}}}`)
	write("rules/foo.md", "Rule text.\n")
	write("README.md", "# Readme\n")
	write("skills/one/SKILL.md", "---\nname: one\ndescription: One skill.\n---\n\nBody.\n")
	write("node_modules/pkg/SKILL.md", "---\nname: vendored\ndescription: No.\n---\n\nNo.\n")
	write(".venv/lib/rules/bad.md", "No.\n")

	// A symlinked skill directory: walkResolved follows these, filepath.WalkDir
	// does not, and losing them silently is a bug this project has had before.
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(elsewhere, "two"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "two", "SKILL.md"),
		[]byte("---\nname: two\ndescription: Two skill.\n---\n\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(elsewhere, "two"), filepath.Join(root, "skills", "two")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	files := map[string][]byte{}
	if err := walkResolved(root, func(e WalkEntry) error {
		raw, err := os.ReadFile(e.Abs)
		if err != nil {
			return err
		}
		files[e.Rel] = raw
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}

	found := DetectContent(files, "on-disk")
	if len(found.Skills) != 2 {
		t.Errorf("skills = %+v, want one/ and the symlinked two/", found.Skills)
	}
	if got := ruleNames(found); len(got) != 3 {
		t.Errorf("rules = %v, want three (CLAUDE.md, AGENTS.md, rules/foo.md)", got)
	}
	if len(found.MCPs) != 2 {
		t.Errorf("mcp servers = %+v, want 2", found.MCPs)
	}
}

func ruleNames(f *Found) []string {
	out := make([]string, 0, len(f.Rules))
	for _, r := range f.Rules {
		out = append(out, r.Name)
	}
	return out
}
