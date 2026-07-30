package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sidharthpunathil/skillbuilder/internal/store"
)

func stdioServer(name string, env ...store.EnvVar) *store.MCPServer {
	return &store.MCPServer{
		Name:        name,
		Description: name + " server",
		Transport:   store.TransportStdio,
		Command:     "npx",
		Args:        []string{"-y", "@example/" + name},
		Env:         env,
	}
}

func TestApplyMCPServers(t *testing.T) {
	tests := []struct {
		name     string
		existing string // empty means the file does not exist yet
		servers  []*store.MCPServer
		force    bool
		wantErr  []string
		want     map[string]string // server name to action
		check    func(t *testing.T, doc map[string]any)
	}{
		{
			name:    "writes a config that was not there",
			servers: []*store.MCPServer{stdioServer("github")},
			want:    map[string]string{"github": "added"},
			check: func(t *testing.T, doc map[string]any) {
				if _, ok := servers(t, doc)["github"]; !ok {
					t.Error("github is missing from the written config")
				}
			},
		},
		{
			name:     "merges into an empty file",
			existing: "   \n",
			servers:  []*store.MCPServer{stdioServer("github")},
			want:     map[string]string{"github": "added"},
			check: func(t *testing.T, doc map[string]any) {
				if len(servers(t, doc)) != 1 {
					t.Errorf("want 1 server, got %d", len(servers(t, doc)))
				}
			},
		},
		{
			name:     "keeps a server the project already had",
			existing: `{"mcpServers":{"linear":{"command":"linear-mcp"}}}`,
			servers:  []*store.MCPServer{stdioServer("github")},
			want:     map[string]string{"github": "added"},
			check: func(t *testing.T, doc map[string]any) {
				got := servers(t, doc)
				if len(got) != 2 {
					t.Fatalf("want 2 servers, got %d: %v", len(got), got)
				}
				linear, _ := got["linear"].(map[string]any)
				if linear["command"] != "linear-mcp" {
					t.Errorf("the project's own linear entry was rewritten: %v", linear)
				}
			},
		},
		{
			name:     "skips a name collision without force",
			existing: `{"mcpServers":{"github":{"command":"my-own-github","args":["--local"]}}}`,
			servers:  []*store.MCPServer{stdioServer("github"), stdioServer("sentry")},
			want:     map[string]string{"github": "kept", "sentry": "added"},
			check: func(t *testing.T, doc map[string]any) {
				github, _ := servers(t, doc)["github"].(map[string]any)
				if github["command"] != "my-own-github" {
					t.Errorf("a working config was overwritten without --force: %v", github)
				}
			},
		},
		{
			name:     "replaces a name collision with force",
			existing: `{"mcpServers":{"github":{"command":"my-own-github","args":["--local"]}}}`,
			servers:  []*store.MCPServer{stdioServer("github")},
			force:    true,
			want:     map[string]string{"github": "replaced"},
			check: func(t *testing.T, doc map[string]any) {
				github, _ := servers(t, doc)["github"].(map[string]any)
				if github["command"] != "npx" {
					t.Errorf("--force did not replace the entry: %v", github)
				}
			},
		},
		{
			name:     "carries through keys we do not model",
			existing: `{"$schema":"https://example.com/mcp.json","inputs":[{"id":"token"}],"mcpServers":{}}`,
			servers:  []*store.MCPServer{stdioServer("github")},
			want:     map[string]string{"github": "added"},
			check: func(t *testing.T, doc map[string]any) {
				if doc["$schema"] != "https://example.com/mcp.json" {
					t.Errorf("$schema was dropped: %v", doc["$schema"])
				}
				inputs, ok := doc["inputs"].([]any)
				if !ok || len(inputs) != 1 {
					t.Errorf("inputs was dropped: %v", doc["inputs"])
				}
			},
		},
		{
			name:     "refuses a config it cannot parse",
			existing: `{"mcpServers": {oops}`,
			servers:  []*store.MCPServer{stdioServer("github")},
			wantErr:  []string{"cannot merge into", "run this again"},
		},
		{
			name:     "refuses a config whose mcpServers is the wrong shape",
			existing: `{"mcpServers": ["github"]}`,
			servers:  []*store.MCPServer{stdioServer("github")},
			wantErr:  []string{"cannot merge into"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), mcpConfigName)
			if tc.existing != "" {
				if err := os.WriteFile(path, []byte(tc.existing), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			results, written, err := applyMCPServers(path, tc.servers, tc.force, false)
			if len(tc.wantErr) > 0 {
				if err == nil {
					t.Fatalf("want an error, got results %v", results)
				}
				for _, want := range tc.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %q", err, want)
					}
				}
				if got, _ := os.ReadFile(path); string(got) != tc.existing {
					t.Errorf("the file was rewritten despite the error:\n%s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("apply: %v", err)
			}

			got := map[string]string{}
			for _, r := range results {
				got[r.Name] = r.Action
			}
			if len(got) != len(tc.want) {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
			for name, action := range tc.want {
				if got[name] != action {
					t.Errorf("%s: want %q, got %q", name, action, got[name])
				}
			}
			for _, m := range written {
				if got[m.Name] == "kept" {
					t.Errorf("%s was reported kept but counted as written", m.Name)
				}
			}
			tc.check(t, readConfig(t, path))
		})
	}
}

func TestApplyMCPServersReportsUnchanged(t *testing.T) {
	// Written by hand in a different key order and with different spacing than
	// we would write it, because neither is a difference.
	original := "{\n\"mcpServers\": {\"github\": {\"args\":[\"-y\",\"@example/github\"],\n\"command\":\"npx\"}}}\n"

	for _, force := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), mcpConfigName)
		if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
			t.Fatal(err)
		}

		results, written, err := applyMCPServers(path, []*store.MCPServer{stdioServer("github")}, force, false)
		if err != nil {
			t.Fatalf("force=%v: %v", force, err)
		}
		if len(results) != 1 || results[0].Action != "unchanged" {
			t.Errorf("force=%v: want one unchanged server, got %v", force, results)
		}
		// It is in the file, so its environment variables still matter.
		if len(written) != 1 {
			t.Errorf("force=%v: an unchanged server should still be reported for env, got %v", force, written)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != original {
			t.Errorf("force=%v: the file was rewritten for no change:\n%s", force, got)
		}
	}
}

func TestApplyMCPServersDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, mcpConfigName)

	results, written, err := applyMCPServers(path, []*store.MCPServer{stdioServer("github")}, false, true)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(results) != 1 || results[0].Action != "added" {
		t.Fatalf("want one added server, got %v", results)
	}
	if len(written) != 1 {
		t.Fatalf("want the server reported as writable, got %v", written)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a dry run created %s", path)
	}
}

func TestApplyMCPServersLeavesFileAloneWhenNothingChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), mcpConfigName)
	original := `{"mcpServers":{"github":{"command":"my-own-github"}}}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := applyMCPServers(path, []*store.MCPServer{stdioServer("github")}, false, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("the file was rewritten for no change:\n%s", got)
	}
}

func TestMissingMCPEnv(t *testing.T) {
	set := map[string]string{"GITHUB_TOKEN": "ghp_live", "BLANK": "  "}
	lookup := func(name string) string { return set[name] }

	servers := []*store.MCPServer{
		stdioServer("github",
			store.EnvVar{Name: "GITHUB_TOKEN", Required: true},
			store.EnvVar{Name: "GITHUB_HOST"}),
		stdioServer("sentry",
			store.EnvVar{Name: "SENTRY_DSN", Required: true, Help: "from Settings, Client Keys"},
			store.EnvVar{Name: "GITHUB_HOST"},
			store.EnvVar{Name: "BLANK"}),
	}

	missing := missingMCPEnv(servers, lookup)
	var names []string
	for _, e := range missing {
		names = append(names, e.Name)
	}
	// Required first, then alphabetical. A variable that is set is not listed,
	// and one that is set to whitespace is treated as unset.
	want := []string{"SENTRY_DSN", "BLANK", "GITHUB_HOST"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("want %v, got %v", want, names)
	}
	if got := missing[2].Servers; strings.Join(got, ",") != "github,sentry" {
		t.Errorf("GITHUB_HOST should name both servers, got %v", got)
	}
	if missing[0].Help != "from Settings, Client Keys" {
		t.Errorf("help text was dropped: %q", missing[0].Help)
	}

	var buf bytes.Buffer
	printMCPEnv(&buf, servers, lookup)
	out := buf.String()
	for _, want := range []string{"set these before the servers will start", "SENTRY_DSN", "GITHUB_HOST", "optional", "export them in your shell"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "GITHUB_TOKEN") {
		t.Errorf("a variable that is already set was listed:\n%s", out)
	}
}

func TestPrintMCPEnvWhenEverythingIsSet(t *testing.T) {
	servers := []*store.MCPServer{stdioServer("github", store.EnvVar{Name: "GITHUB_TOKEN", Required: true})}
	lookup := func(string) string { return "set" }

	var buf bytes.Buffer
	printMCPEnv(&buf, servers, lookup)
	if got := buf.String(); !strings.Contains(got, "already set") {
		t.Errorf("want reassurance that nothing is missing, got %q", got)
	}

	buf.Reset()
	printMCPEnv(&buf, []*store.MCPServer{stdioServer("github")}, lookup)
	if got := buf.String(); got != "" {
		t.Errorf("a folder with no variables should say nothing, got %q", got)
	}
}

// The registry API is fetched over HTTP, so a fake one exercises the command
// end to end without needing the real server running.
func fakeRegistry(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/mcpsets":
			w.Write([]byte(`{"mcpsets":[
				{"name":"essentials","title":"Essentials","description":"What every project wants","servers":["github","sentry"],"updatedAt":"2026-01-01T00:00:00Z"},
				{"name":"analytics","title":"Analytics","description":"Warehouse access","servers":["duckdb"],"updatedAt":"2026-01-01T00:00:00Z"}]}`))
		case "/api/v1/mcpsets/essentials":
			w.Write([]byte(`{"name":"essentials","title":"Essentials","description":"What every project wants","updatedAt":"2026-01-01T00:00:00Z","servers":[
				{"name":"github","description":"Issues and pull requests","transport":"stdio","command":"npx","args":["-y","@example/github"],"env":[{"name":"SKILLBUILDER_TEST_GITHUB_TOKEN","required":true,"help":"a token with repo scope"}]},
				{"name":"sentry","description":"Errors and releases","transport":"http","url":"https://mcp.sentry.example"}]}`))
		default:
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runCLI drives the real root command so flag wiring and output are covered.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	// A temporary home keeps the developer's own config and token out of it.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SKILLBUILDER_TOKEN", "")
	t.Setenv("SKILLBUILDER_REGISTRY", "")
	registryFlag = ""

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return buf.String(), err
}

func TestMCPCommandListsFolders(t *testing.T) {
	srv := fakeRegistry(t)

	out, err := runCLI(t, "mcp", "--registry", srv.URL)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	for _, want := range []string{"analytics", "essentials", "2 servers", "1 server", "What every project wants"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing does not mention %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "analytics") > strings.Index(out, "essentials") {
		t.Errorf("folders should be sorted by name:\n%s", out)
	}
}

func TestMCPCommandInstallsFolder(t *testing.T) {
	srv := fakeRegistry(t)
	path := filepath.Join(t.TempDir(), mcpConfigName)

	out, err := runCLI(t, "mcp", "essentials", "-y", "-o", path, "--registry", srv.URL)
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	for _, want := range []string{"Essentials · 2 servers", "github", "sentry", "2 added",
		"set these before the servers will start", "SKILLBUILDER_TEST_GITHUB_TOKEN", "a token with repo scope"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}

	got := servers(t, readConfig(t, path))
	github, _ := got["github"].(map[string]any)
	if github["command"] != "npx" {
		t.Errorf("github was not written as a stdio server: %v", github)
	}
	env, _ := github["env"].(map[string]any)
	if env["SKILLBUILDER_TEST_GITHUB_TOKEN"] != "${SKILLBUILDER_TEST_GITHUB_TOKEN}" {
		t.Errorf("the env reference is wrong: %v", env)
	}
	sentry, _ := got["sentry"].(map[string]any)
	if sentry["type"] != "http" || sentry["url"] != "https://mcp.sentry.example" {
		t.Errorf("sentry was not written as an http server: %v", sentry)
	}
}

func TestMCPCommandDryRunWritesNothing(t *testing.T) {
	srv := fakeRegistry(t)
	path := filepath.Join(t.TempDir(), mcpConfigName)

	out, err := runCLI(t, "mcp", "essentials", "-y", "--dry-run", "-o", path, "--registry", srv.URL)
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing was written") {
		t.Errorf("a dry run should say so:\n%s", out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a dry run created %s", path)
	}
}

func TestMCPCommandUnknownFolder(t *testing.T) {
	srv := fakeRegistry(t)

	out, err := runCLI(t, "mcp", "nope", "-y", "--registry", srv.URL)
	if err == nil {
		t.Fatalf("want an error for a folder that does not exist:\n%s", out)
	}
	for _, want := range []string{`no mcp folder named "nope"`, "skillbuilder mcp"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func readConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the written config is not valid JSON: %v\n%s", err, raw)
	}
	return doc
}

func servers(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	got, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("no mcpServers in %v", doc)
	}
	return got
}
