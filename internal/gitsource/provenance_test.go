package gitsource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourcesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sources.json")
	in := map[string]Source{
		"foo": {
			Skill:      "foo",
			Repo:       "emilkowalski/skills",
			URL:        "https://github.com/emilkowalski/skills",
			Ref:        "main",
			CommitSHA:  "7fd1a60b01f91b314f59955a4e4d4e80d8edf11d",
			Path:       "skills/foo",
			ImportedAt: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC),
			UpdatedAt:  time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
		},
		"bar": {Skill: "bar", Repo: "other/repo", Path: ""},
	}
	if err := SaveSources(path, in); err != nil {
		t.Fatalf("SaveSources: %v", err)
	}

	out, err := LoadSources(path)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("loaded %d sources, want 2", len(out))
	}
	if out["foo"] != in["foo"] {
		t.Errorf("foo = %+v, want %+v", out["foo"], in["foo"])
	}
	if out["bar"] != in["bar"] {
		t.Errorf("bar = %+v, want %+v", out["bar"], in["bar"])
	}
}

func TestLoadSourcesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nothing-here.json")
	m, err := LoadSources(path)
	if err != nil {
		t.Fatalf("LoadSources on a missing file: %v", err)
	}
	if m == nil || len(m) != 0 {
		t.Fatalf("got %v, want an empty map", m)
	}
	// The empty map must be usable straight away.
	m["foo"] = Source{Skill: "foo"}
}

func TestLoadSourcesEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sources.json")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadSources(path)
	if err != nil || len(m) != 0 {
		t.Fatalf("LoadSources = %v, %v", m, err)
	}
}

func TestLoadSourcesInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sources.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSources(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want one naming the file", err)
	}
}

// An entry written before Source carried its own name still loads with one.
func TestLoadSourcesBackfillsSkillName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sources.json")
	if err := os.WriteFile(path, []byte(`{"foo":{"repo":"o/r"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadSources(path)
	if err != nil {
		t.Fatal(err)
	}
	if m["foo"].Skill != "foo" {
		t.Errorf("Skill = %q, want the map key", m["foo"].Skill)
	}
}

func TestSaveSourcesOverwritesAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.json")

	if err := SaveSources(path, map[string]Source{"a": {Skill: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSources(path, map[string]Source{"b": {Skill: "b"}}); err != nil {
		t.Fatal(err)
	}

	m, err := LoadSources(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["a"]; ok || len(m) != 1 {
		t.Fatalf("got %v, want only b", m)
	}

	// The temp file must not survive the rename.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want just the sources file", len(entries))
	}
}

func TestSaveSourcesCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "sources.json")
	if err := SaveSources(path, map[string]Source{"a": {Skill: "a"}}); err != nil {
		t.Fatalf("SaveSources: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat: %v", err)
	}
}

func TestSaveSourcesNilMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sources.json")
	if err := SaveSources(path, nil); err != nil {
		t.Fatalf("SaveSources(nil): %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != "{}" {
		t.Errorf("file = %q, want an empty object", raw)
	}
}

func TestNewSource(t *testing.T) {
	repo := Repo{Owner: "emilkowalski", Name: "skills", Ref: "main"}
	meta := Metadata{Ref: "main", CommitSHA: "abc1234"}
	cand := Candidate{Path: "skills/foo", UpdatedAt: time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC)}

	got := NewSource("foo", repo, meta, cand)
	if got.Skill != "foo" || got.Repo != "emilkowalski/skills" ||
		got.URL != "https://github.com/emilkowalski/skills" ||
		got.Ref != "main" || got.CommitSHA != "abc1234" || got.Path != "skills/foo" {
		t.Errorf("NewSource = %+v", got)
	}
	if !got.UpdatedAt.Equal(cand.UpdatedAt) {
		t.Errorf("UpdatedAt = %v", got.UpdatedAt)
	}
	if got.ImportedAt.IsZero() {
		t.Error("ImportedAt is zero")
	}
}
