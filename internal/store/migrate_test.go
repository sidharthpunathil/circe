package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The old build kept three parallel folder types. Anyone upgrading has data in
// data/rulesets and data/mcpsets, and it must end up inside bundles rather than
// quietly disappearing.

func writeLegacy(t *testing.T, root, dir, name string, body map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, dir, name+".json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyFoldersFoldIntoBundles(t *testing.T) {
	root := t.TempDir()

	// A folder that exists in all three old shapes under one name, which is the
	// case that matters: people named them after the same project.
	writeLegacy(t, root, "bundles", "web", map[string]any{
		"name": "web", "title": "Web", "skills": []string{"tidy"},
	})
	writeLegacy(t, root, "rulesets", "web", map[string]any{
		"name": "web", "rules": []string{"house-style"},
	})
	writeLegacy(t, root, "mcpsets", "web", map[string]any{
		"name": "web", "servers": []string{"github"},
	})
	// And one that only ever existed as a rule folder.
	writeLegacy(t, root, "rulesets", "solo", map[string]any{
		"name": "solo", "title": "Solo", "description": "just rules", "rules": []string{"house-style"},
	})

	st, err := Open(root)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	web, err := st.Bundle("web")
	if err != nil {
		t.Fatalf("bundle web: %v", err)
	}
	if web.Title != "Web" {
		t.Errorf("title = %q, want the bundle's own title to win", web.Title)
	}
	// The referenced skill, rule and server do not exist on disk, so SaveBundle
	// prunes them. What matters here is that the merge reached the file at all,
	// so assert on what was written rather than the pruned index.
	raw, err := os.ReadFile(filepath.Join(root, "bundles", "web.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk Bundle
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if !onDisk.HasRule("house-style") {
		t.Errorf("rules were lost in migration: %+v", onDisk.Rules)
	}
	if !onDisk.HasServer("github") {
		t.Errorf("servers were lost in migration: %+v", onDisk.Servers)
	}
	if !onDisk.Has("tidy") {
		t.Errorf("existing skills were clobbered: %+v", onDisk.Skills)
	}

	if _, err := st.Bundle("solo"); err != nil {
		t.Errorf("a rule-only folder did not become a bundle: %v", err)
	}

	// The old directories must be gone, or a second boot would fold them again.
	for _, dir := range []string{"rulesets", "mcpsets"} {
		if _, err := os.Stat(filepath.Join(root, dir)); !os.IsNotExist(err) {
			t.Errorf("%s still present after migration", dir)
		}
	}
}

func TestMigrationIsIdempotent(t *testing.T) {
	root := t.TempDir()
	writeLegacy(t, root, "rulesets", "x", map[string]any{"name": "x", "rules": []string{"a", "b"}})

	for i := 0; i < 3; i++ {
		if _, err := Open(root); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(root, "bundles", "x.json"))
	if err != nil {
		t.Fatal(err)
	}
	var b Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Rules) != 2 {
		t.Errorf("rules = %v, want each listed once after repeated boots", b.Rules)
	}
}

func TestMergeNamesKeepsOrderAndDropsDuplicates(t *testing.T) {
	got := mergeNames([]string{"a", "b"}, []string{"b", "c", ""})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("mergeNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mergeNames = %v, want %v", got, want)
		}
	}
}
