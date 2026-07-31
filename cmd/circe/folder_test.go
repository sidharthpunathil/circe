package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sidharthpunathil/circe/internal/server"
	"github.com/sidharthpunathil/circe/internal/store"
)

// The registry the tests talk to is the real one: a real store on a temp
// directory, served by the real HTTP handlers over a real socket.
//
// It would be less code to hand-write the JSON the CLI expects, but a
// hand-written registry only proves the CLI agrees with itself. The contract
// that matters is the one the server actually serves, and the point of these
// tests is to notice when the two drift apart.
func realRegistry(t *testing.T) *httptest.Server {
	t.Helper()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	skill := func(name, desc string) {
		t.Helper()
		body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n# " + name + "\n\nDo the thing.\n"
		if err := st.WriteSkillFiles(name, map[string][]byte{"SKILL.md": []byte(body)}, true); err != nil {
			t.Fatalf("write skill %s: %v", name, err)
		}
	}
	skill("design-taste", "Design judgment for interfaces")
	skill("number-formatting", "How numbers should be displayed")

	rule := func(name, desc, body string, targets ...string) {
		t.Helper()
		if err := st.SaveRule(&store.Rule{Name: name, Description: desc, Body: body, Targets: targets}); err != nil {
			t.Fatalf("save rule %s: %v", name, err)
		}
	}
	rule("house-style", "How we write", "Prefer plain words.", store.TargetClaude)
	rule("no-dashes", "Punctuation", "Never use a dash.", store.TargetClaude, store.TargetAgents)

	mcp := func(m *store.MCPServer) {
		t.Helper()
		if err := st.SaveMCPServer(m); err != nil {
			t.Fatalf("save server %s: %v", m.Name, err)
		}
	}
	mcp(&store.MCPServer{
		Name: "github", Description: "Issues and pull requests",
		Transport: store.TransportStdio, Command: "npx", Args: []string{"-y", "@example/github"},
		// Deliberately a name no real environment will have set, so the
		// "you still need to export this" report is exercised.
		Env: []store.EnvVar{{Name: "CIRCE_TEST_GITHUB_TOKEN", Required: true, Help: "a token with repo scope"}},
	})
	mcp(&store.MCPServer{
		Name: "sentry", Description: "Errors and releases",
		Transport: store.TransportHTTP, URL: "https://mcp.sentry.example",
	})
	mcp(&store.MCPServer{
		Name: "duckdb", Description: "Query the warehouse",
		Transport: store.TransportStdio, Command: "duckdb-mcp",
	})

	folder := func(b *store.Bundle) {
		t.Helper()
		if err := st.CreateBundle(b); err != nil {
			t.Fatalf("create folder %s: %v", b.Name, err)
		}
	}
	folder(&store.Bundle{
		Name: "everything", Title: "Everything", Description: "A bit of each",
		Skills:  []string{"design-taste", "number-formatting"},
		Rules:   []string{"house-style", "no-dashes"},
		Servers: []string{"github", "sentry"},
	})
	folder(&store.Bundle{
		Name: "skills-only", Title: "Skills only", Description: "Only skills here",
		Skills: []string{"design-taste"},
	})
	// Named to match how the MCP tests read: a folder you would actually make.
	folder(&store.Bundle{
		Name: "essentials", Title: "Essentials", Description: "What every project wants",
		Servers: []string{"github", "sentry"},
	})
	folder(&store.Bundle{Name: "analytics", Title: "Analytics", Description: "Warehouse access",
		Servers: []string{"duckdb"}})
	folder(&store.Bundle{
		Name: "house-rules", Title: "House rules", Description: "How we write",
		Rules: []string{"house-style", "no-dashes"},
	})
	folder(&store.Bundle{Name: "empty"})

	// A folder can carry instructions of its own alongside whatever it holds.
	instructions := func(folder, body string) {
		t.Helper()
		if err := st.SaveBundleContext(folder, store.TargetClaude, body); err != nil {
			t.Fatalf("save context for %s: %v", folder, err)
		}
	}
	instructions("everything", "Read the rules below.")
	instructions("skills-only", "Ship small changes.")

	srv, err := server.New(server.Options{Store: st, Build: "test"})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

// runCLI drives the real root command from inside a project directory, so flag
// wiring, the bare-folder shorthand and everything written to disk are covered.
func runCLI(t *testing.T, project string, args ...string) (string, error) {
	t.Helper()
	// A temporary home keeps the developer's own config and token out of it.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIRCE_TOKEN", "")
	t.Setenv("CIRCE_REGISTRY", "")
	registryFlag = ""
	t.Chdir(project)

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(expandShorthand(root, args))
	err := root.ExecuteContext(context.Background())
	return buf.String(), err
}

// read returns a file's content, failing the test if it is not there.
func read(t *testing.T, dir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

func missing(t *testing.T, dir, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Errorf("%s should not have been written", name)
	}
}

func TestFolderInstallsEveryKind(t *testing.T) {
	srv := realRegistry(t)
	project := t.TempDir()

	out, err := runCLI(t, project, "everything", "-y", "--registry", srv.URL)
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}

	// The summary names every kind, once for the folder and once for the run.
	for _, want := range []string{
		"Everything · 2 skills, 2 rules and 2 servers",
		"skills", "rules", "mcp servers",
		"installed 2 skills, 2 rules and 2 servers",
		"set these before the servers will start", "CIRCE_TEST_GITHUB_TOKEN",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}

	if body := read(t, project, ".claude/skills/design-taste/SKILL.md"); !strings.Contains(body, "design-taste") {
		t.Errorf("the skill was not installed:\n%s", body)
	}
	if _, err := os.Stat(filepath.Join(project, ".claude/skills/number-formatting")); err != nil {
		t.Errorf("the second skill was not installed: %v", err)
	}

	claude := read(t, project, "CLAUDE.md")
	for _, want := range []string{"circe:start everything", "Read the rules below.", "Prefer plain words.", "Never use a dash."} {
		if !strings.Contains(claude, want) {
			t.Errorf("CLAUDE.md does not contain %q:\n%s", want, claude)
		}
	}
	agents := read(t, project, "AGENTS.md")
	if !strings.Contains(agents, "Never use a dash.") {
		t.Errorf("AGENTS.md is missing the rule that targets it:\n%s", agents)
	}
	if strings.Contains(agents, "Prefer plain words.") {
		t.Errorf("a rule that only targets CLAUDE.md landed in AGENTS.md:\n%s", agents)
	}

	config := servers(t, readConfig(t, filepath.Join(project, mcpConfigName)))
	for _, name := range []string{"github", "sentry"} {
		if _, ok := config[name]; !ok {
			t.Errorf("%s is missing from %s: %v", name, mcpConfigName, config)
		}
	}

	lock := read(t, project, "circe.json")
	for _, want := range []string{"design-taste", "number-formatting", "everything"} {
		if !strings.Contains(lock, want) {
			t.Errorf("the lockfile does not record %q:\n%s", want, lock)
		}
	}
}

// A folder of nothing but skills is the case everyone already has a habit
// around, so it keeps the output it always had: no header, no section labels,
// no combined summary.
func TestFolderOfOnlySkillsReadsAsItAlwaysDid(t *testing.T) {
	srv := realRegistry(t)
	project := t.TempDir()

	out, err := runCLI(t, project, "skills-only", "-y", "--registry", srv.URL)
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1 installed in .claude/skills") {
		t.Errorf("output does not report the install:\n%s", out)
	}
	for _, unwanted := range []string{"Skills only ·", "mcp servers", "installed 1 skill,"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("a skills-only install should not print %q:\n%s", unwanted, out)
		}
	}
	if body := read(t, project, ".claude/skills/design-taste/SKILL.md"); body == "" {
		t.Error("the skill was not installed")
	}
	// The folder's own instructions still merge, as they always have.
	if claude := read(t, project, "CLAUDE.md"); !strings.Contains(claude, "Ship small changes.") {
		t.Errorf("the folder's context was not merged:\n%s", claude)
	}
	missing(t, project, mcpConfigName)
}

func TestFolderWithNothingInIt(t *testing.T) {
	srv := realRegistry(t)
	project := t.TempDir()

	out, err := runCLI(t, project, "empty", "-y", "--registry", srv.URL)
	if err == nil {
		t.Fatalf("want an error for an empty folder:\n%s", out)
	}
	for _, want := range []string{`folder "empty" is empty`, "then run this again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestFolderDryRunWritesNothing(t *testing.T) {
	srv := realRegistry(t)
	project := t.TempDir()

	out, err := runCLI(t, project, "everything", "-y", "--dry-run", "--registry", srv.URL)
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "dry run: 2 skills, 2 rules and 2 servers; nothing was written") {
		t.Errorf("a dry run should summarise every kind and say nothing was written:\n%s", out)
	}
	for _, name := range []string{".claude", "CLAUDE.md", "AGENTS.md", mcpConfigName, "circe.json"} {
		missing(t, project, name)
	}
}

// Narrowing to one kind must leave the other two alone.
func TestSubcommandsInstallOneKind(t *testing.T) {
	t.Run("skills", func(t *testing.T) {
		srv := realRegistry(t)
		project := t.TempDir()

		out, err := runCLI(t, project, "skills", "everything", "-y", "--registry", srv.URL)
		if err != nil {
			t.Fatalf("install: %v\n%s", err, out)
		}
		if body := read(t, project, ".claude/skills/design-taste/SKILL.md"); body == "" {
			t.Error("the skill was not installed")
		}
		missing(t, project, mcpConfigName)
		// The rules and the folder's own context share one block, so installing
		// skills alone leaves the whole block alone rather than shrinking it.
		missing(t, project, "CLAUDE.md")
		if !strings.Contains(out, "2 rules left alone") {
			t.Errorf("output should say the rules were not installed:\n%s", out)
		}
	})

	t.Run("rules", func(t *testing.T) {
		srv := realRegistry(t)
		project := t.TempDir()

		out, err := runCLI(t, project, "rules", "everything", "--registry", srv.URL)
		if err != nil {
			t.Fatalf("install: %v\n%s", err, out)
		}
		if !strings.Contains(out, "Everything · 2 rules") {
			t.Errorf("output does not name the folder and its rules:\n%s", out)
		}
		if claude := read(t, project, "CLAUDE.md"); !strings.Contains(claude, "Prefer plain words.") {
			t.Errorf("the rules were not merged:\n%s", claude)
		}
		missing(t, project, ".claude")
		missing(t, project, mcpConfigName)
	})

	t.Run("mcp", func(t *testing.T) {
		srv := realRegistry(t)
		project := t.TempDir()

		out, err := runCLI(t, project, "mcp", "everything", "-y", "--registry", srv.URL)
		if err != nil {
			t.Fatalf("install: %v\n%s", err, out)
		}
		if !strings.Contains(out, "Everything · 2 servers") {
			t.Errorf("output does not name the folder and its servers:\n%s", out)
		}
		if config := servers(t, readConfig(t, filepath.Join(project, mcpConfigName))); len(config) != 2 {
			t.Errorf("want both servers, got %v", config)
		}
		missing(t, project, ".claude")
		missing(t, project, "CLAUDE.md")
	})
}

func TestSubcommandOnAFolderWithoutThatKind(t *testing.T) {
	srv := realRegistry(t)
	project := t.TempDir()

	out, err := runCLI(t, project, "rules", "essentials", "--registry", srv.URL)
	if err == nil {
		t.Fatalf("want an error for a folder with no rules:\n%s", out)
	}
	for _, want := range []string{`folder "essentials" has no rules`, "2 servers", "circe essentials"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// A folder can carry a CLAUDE.md without holding a single rule, and that is
// still guidance, so `circe rules` merges it.
func TestRulesInstallsAFoldersOwnInstructions(t *testing.T) {
	srv := realRegistry(t)
	project := t.TempDir()

	out, err := runCLI(t, project, "rules", "skills-only", "--registry", srv.URL)
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if claude := read(t, project, "CLAUDE.md"); !strings.Contains(claude, "Ship small changes.") {
		t.Errorf("the folder's instructions were not merged:\n%s", claude)
	}
	missing(t, project, ".claude")
}

func TestSubcommandsListTheFoldersOfTheirKind(t *testing.T) {
	tests := []struct {
		command string
		want    []string
		absent  []string
	}{
		{"skills", []string{"everything", "skills-only"}, []string{"essentials", "house-rules", "empty"}},
		{"rules", []string{"everything", "house-rules"}, []string{"essentials", "skills-only", "empty"}},
		{"mcp", []string{"everything", "essentials", "analytics"}, []string{"skills-only", "house-rules", "empty"}},
	}
	for _, tc := range tests {
		t.Run(tc.command, func(t *testing.T) {
			srv := realRegistry(t)

			out, err := runCLI(t, t.TempDir(), tc.command, "--registry", srv.URL)
			if err != nil {
				t.Fatalf("list: %v\n%s", err, out)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("listing does not mention %q:\n%s", want, out)
				}
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(out, unwanted) {
					t.Errorf("listing should not mention %q:\n%s", unwanted, out)
				}
			}
			// The mix is spelled out, so a folder that holds more than the kind
			// you asked about says so.
			if !strings.Contains(out, "2 skills, 2 rules, 2 servers") {
				t.Errorf("listing does not show what each folder holds:\n%s", out)
			}
		})
	}
}

func TestUnknownFolder(t *testing.T) {
	srv := realRegistry(t)

	out, err := runCLI(t, t.TempDir(), "nope", "-y", "--registry", srv.URL)
	if err == nil {
		t.Fatalf("want an error for a folder that does not exist:\n%s", out)
	}
	for _, want := range []string{`no folder named "nope"`, "circe skills"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// The manifest is read whether the folder is served flat or wrapped in a
// "bundle" key, so the CLI does not have to be redeployed in step with a
// registry that changes its mind about the envelope.
func TestFolderManifestReadsEitherEnvelope(t *testing.T) {
	lists := `"skills":[{"name":"design-taste","description":"Design judgment"}],` +
		`"rules":[{"name":"house-style","description":"How we write","body":"Prefer plain words."}],` +
		`"servers":[{"name":"github","transport":"stdio","command":"npx"}],` +
		`"context":{"CLAUDE.md":"Read this."},"registry":"https://example.com"`

	shapes := map[string]string{
		"flat":   `{"name":"kit","title":"Kit","description":"A folder",` + lists + `}`,
		"nested": `{"bundle":{"name":"kit","title":"Kit","description":"A folder","skills":["design-taste"]},` + lists + `}`,
	}
	for name, raw := range shapes {
		t.Run(name, func(t *testing.T) {
			var m folderManifest
			if err := json.Unmarshal([]byte(raw), &m); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if m.Name != "kit" || m.label() != "Kit" {
				t.Errorf("folder is %q titled %q", m.Name, m.Title)
			}
			if len(m.Skills) != 1 || len(m.Rules) != 1 || len(m.Servers) != 1 {
				t.Fatalf("want one of each, got %d skills, %d rules, %d servers",
					len(m.Skills), len(m.Rules), len(m.Servers))
			}
			if m.Rules[0].Body != "Prefer plain words." {
				t.Errorf("the rule body did not survive: %q", m.Rules[0].Body)
			}
			if m.Context["CLAUDE.md"] != "Read this." || m.Registry != "https://example.com" {
				t.Errorf("context or registry was dropped: %v %q", m.Context, m.Registry)
			}
			if m.holds() != kindAll {
				t.Errorf("a folder with all three kinds should hold all three")
			}
		})
	}
}
