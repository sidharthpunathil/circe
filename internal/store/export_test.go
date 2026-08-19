package store

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// populate builds a registry with one of everything, including a companion file
// that is not text, since that is the case a database is most likely to mangle.
func populate(t *testing.T, s *Store) {
	t.Helper()
	mustCreate(t, s, &Skill{
		Name:        "alpha",
		Description: "The first skill",
		Body:        "# Alpha\n\nDo the thing.\n",
		Tags:        []string{"one", "two"},
	})
	if err := s.WriteFile("alpha", "references/api.md", []byte("reference")); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteFile("alpha", "assets/logo.png", []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0xff}); err != nil {
		t.Fatal(err)
	}
	// An empty companion file: PocketBase refuses a zero-byte upload, so this
	// is the case that has to be handled rather than stored.
	if err := s.WriteFile("alpha", "empty.txt", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRule(&Rule{
		Name: "house-style", Description: "How we write", Body: "Use tabs.\n",
		Targets: []string{TargetClaude, TargetAgents},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateMCPServer(&MCPServer{
		Name: "github", Description: "The GitHub server", Transport: TransportStdio,
		Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"},
		Env: []EnvVar{{Name: "GITHUB_PERSONAL_ACCESS_TOKEN", Required: true}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBundle(&Bundle{
		Name: "starter", Title: "Starter Pack",
		Skills: []string{"alpha"}, Rules: []string{"house-style"}, Servers: []string{"github"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveBundleContext("starter", "CLAUDE.md", "Project instructions.\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSource("alpha", []byte(`{"skill":"alpha","repo":"o/n"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestExportWritesThePlainLayout(t *testing.T) {
	s, _ := openStore(t)
	populate(t, s)

	dst := t.TempDir()
	if err := s.ExportTree(dst); err != nil {
		t.Fatalf("ExportTree: %v", err)
	}

	want := []string{
		"bundles/starter.json",
		"bundles/starter/CLAUDE.md",
		"mcps/github.json",
		"rules/house-style.md",
		"skills/alpha/SKILL.md",
		"skills/alpha/assets/logo.png",
		"skills/alpha/empty.txt",
		"skills/alpha/references/api.md",
		"sources.json",
	}
	if got := treeOf(t, dst); !sameStrings(got, want) {
		t.Errorf("exported tree =\n%v\nwant\n%v", got, want)
	}

	// It is a real SKILL.md, readable without this program.
	raw, err := os.ReadFile(filepath.Join(dst, "skills", "alpha", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got[:4] != "---\n" {
		t.Errorf("SKILL.md does not start with frontmatter: %q", got)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	s, _ := openStore(t)
	populate(t, s)

	dst := t.TempDir()
	if err := s.ExportTree(dst); err != nil {
		t.Fatalf("ExportTree: %v", err)
	}
	before, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	// A brand new registry, fed only the exported files.
	s2, _ := openStore(t)
	res, err := s2.ImportTree(dst, true)
	if err != nil {
		t.Fatalf("ImportTree: %v", err)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("import failures: %v", res.Failed)
	}

	after, err := s2.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	if len(before) != len(after) {
		t.Errorf("round trip changed the file set:\nbefore %v\nafter  %v", keysOf(before), keysOf(after))
	}
	for rel, want := range before {
		got, ok := after[rel]
		if !ok {
			t.Errorf("%s did not survive the round trip", rel)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s changed:\n before %q\n after  %q", rel, want, got)
		}
	}

	// And the registry itself, not just the bytes.
	sk, err := s2.Skill("alpha")
	if err != nil {
		t.Fatalf("skill did not survive: %v", err)
	}
	if !sameStrings(sk.Tags, []string{"one", "two"}) {
		t.Errorf("tags = %v", sk.Tags)
	}
	if got := filePaths(sk.Files); !sameStrings(got, []string{"assets/logo.png", "empty.txt", "references/api.md"}) {
		t.Errorf("companion files = %v", got)
	}
	if got, _ := s2.ReadFile("alpha", "assets/logo.png"); len(got) != 6 || got[5] != 0xff {
		t.Errorf("binary companion file was mangled: %v", got)
	}
	if got, err := s2.ReadFile("alpha", "empty.txt"); err != nil || len(got) != 0 {
		t.Errorf("empty companion file = %q %v, want empty and no error", got, err)
	}
	b, err := s2.Bundle("starter")
	if err != nil {
		t.Fatalf("folder did not survive: %v", err)
	}
	if b.Title != "Starter Pack" || !sameStrings(b.Skills, []string{"alpha"}) ||
		!sameStrings(b.Rules, []string{"house-style"}) || !sameStrings(b.Servers, []string{"github"}) {
		t.Errorf("folder came back wrong: %+v", b)
	}
	if got := s2.BundleContexts("starter")["CLAUDE.md"]; got != "Project instructions.\n" {
		t.Errorf("folder context = %q", got)
	}
	if _, ok := s2.Source("alpha"); !ok {
		t.Error("provenance did not survive the round trip")
	}
}

func TestExportNeverWritesTokensOrShareLinks(t *testing.T) {
	s, _ := openStore(t)
	populate(t, s)
	if err := s.SaveCredentials(Credentials{"GITHUB_TOKEN": "ghp_secret_value"}); err != nil {
		t.Fatal(err)
	}
	share, err := s.CreateShare(ShareKindSkill, "alpha")
	if err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	if err := s.ExportTree(dst); err != nil {
		t.Fatalf("ExportTree: %v", err)
	}

	// A token in a repository is a token that has leaked, and a share id is a
	// bearer capability that works without the registry's password. Neither
	// belongs in an export, and by extension neither belongs in a commit.
	for _, rel := range treeOf(t, dst) {
		raw, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"ghp_secret_value", share.ID} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("%s leaked %q", rel, secret)
			}
		}
	}
}

// treeOf lists every file under root, slash separated and sorted.
func treeOf(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestAdoptsAnExistingDataDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "data")

	// A data directory exactly as an older release left it: a tree of files,
	// with tokens in the file they used to live in.
	writeTree(t, root, map[string]string{
		"skills/legacy/SKILL.md": "---\nname: legacy\ndescription: Written by an older release\n" +
			"metadata:\n  tags:\n    - old\n---\n\nBody\n",
		"skills/legacy/references/notes.md": "notes",
		"rules/legacy-rule.md":              "---\nname: legacy-rule\ndescription: An old rule\n---\n\nUse tabs.\n",
		"mcps/legacy-server.json":           `{"name":"legacy-server","description":"An old server","transport":"stdio","command":"npx"}`,
		"bundles/legacy-folder.json":        `{"name":"legacy-folder","title":"Old Folder","skills":["legacy"],"rules":["legacy-rule"],"servers":["legacy-server"]}`,
		"bundles/legacy-folder/CLAUDE.md":   "Old project instructions.\n",
		"sources.json":                      `{"legacy":{"skill":"legacy","repo":"o/n"}}`,
		".credentials.json":                 `{"GITHUB_TOKEN":"ghp_from_the_old_file"}`,
	})

	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	sk, err := s.Skill("legacy")
	if err != nil {
		t.Fatalf("the existing skill was not adopted: %v", err)
	}
	if !sameStrings(sk.Tags, []string{"old"}) {
		t.Errorf("tags = %v, want [old]", sk.Tags)
	}
	if got := mustRead(t, s, "legacy", "references/notes.md"); got != "notes" {
		t.Errorf("companion file = %q", got)
	}
	if _, err := s.Rule("legacy-rule"); err != nil {
		t.Errorf("the existing rule was not adopted: %v", err)
	}
	if _, err := s.MCPServer("legacy-server"); err != nil {
		t.Errorf("the existing server was not adopted: %v", err)
	}
	b, err := s.Bundle("legacy-folder")
	if err != nil {
		t.Fatalf("the existing folder was not adopted: %v", err)
	}
	if b.Title != "Old Folder" || !sameStrings(b.Skills, []string{"legacy"}) {
		t.Errorf("folder came back wrong: %+v", b)
	}
	if got := s.BundleContexts("legacy-folder")["CLAUDE.md"]; got != "Old project instructions.\n" {
		t.Errorf("folder context = %q", got)
	}
	if _, ok := s.Source("legacy"); !ok {
		t.Error("provenance was not adopted")
	}
	if got := s.Credentials()["GITHUB_TOKEN"]; got != "ghp_from_the_old_file" {
		t.Errorf("the token in the old credentials file was not carried over, got %q", got)
	}

	// The tree itself is left alone, so downgrading still finds everything.
	if _, err := os.Stat(filepath.Join(root, "skills", "legacy", "SKILL.md")); err != nil {
		t.Errorf("adoption must not remove the directory it read: %v", err)
	}

	// And adoption happens once. Editing here and restarting must not put the
	// old copy back.
	sk.Description = "Edited after the upgrade"
	if err := s.SaveSkill(sk); err != nil {
		t.Fatal(err)
	}
	s2, err := reopen(t, s)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s2.Skill("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if again.Description != "Edited after the upgrade" {
		t.Errorf("restarting re-adopted the old tree and lost the edit: %q", again.Description)
	}
}
