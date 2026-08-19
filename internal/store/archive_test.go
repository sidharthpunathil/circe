package store

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// readTarGz decodes an archive into name -> content.
func readTarGz(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gz.Close()

	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = string(body)
	}
	return out
}

func readZip(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		out[f.Name] = string(body)
	}
	return out
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// junkySkill creates a skill stuffed with content that must and must not be
// packaged.
func junkySkill(t *testing.T, s *Store, name string) {
	t.Helper()
	mustCreate(t, s, &Skill{Name: name, Description: "A packable skill", Body: "Packable body"})
	// Seeded the way a repository import lands, which is the only way junk like
	// a vendored virtualenv gets in: the editor refuses these paths.
	seedFiles(t, s, name, map[string]string{
		"references/keep.md":            "keep me",
		"scripts/run.sh":                "#!/bin/sh\necho hi\n",
		"__pycache__/x.pyc":             "junk",
		"__pycache__/nested/y.txt":      "junk",
		".DS_Store":                     "junk",
		"references/.DS_Store":          "junk",
		"node_modules/y/index.js":       "junk",
		"evals/z.md":                    "junk",
		"evals/deep/z2.md":              "junk",
		"nested/__pycache__/deep.pyc":   "junk",
		"stray.pyc":                     "junk",
		"stray.PYO":                     "junk",
		"Thumbs.db":                     "junk",
		".venv/lib/thing.py":            "junk",
		"venv/lib/thing.py":             "junk",
		".git/config":                   "junk",
		"references/evals/nested-ok.md": "kept: the evals rule is root-only",
	})
}

func TestExcluded(t *testing.T) {
	cases := []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{".", true, false},
		{"SKILL.md", false, false},
		{"references", true, false},
		{"references/keep.md", false, false},
		{"references/evals/x.md", false, false}, // evals only excluded at the root
		{"evals", true, true},
		{"evals/z.md", false, true},
		{"__pycache__", true, true},
		{"a/__pycache__/b.txt", false, true},
		{"node_modules/x/y.js", false, true},
		{".venv/x", false, true},
		{"venv/x", false, true},
		{".git/config", false, true},
		{".DS_Store", false, true},
		{"a/.DS_Store", false, true},
		{"Thumbs.db", false, true},
		{"x.pyc", false, true},
		{"x.PYO", false, true},
		{"x.py", false, false},
		{".DS_Store", true, false}, // directories are matched by the dir rules only
	}
	for _, c := range cases {
		if got := excluded(c.rel, c.isDir); got != c.want {
			t.Errorf("excluded(%q, isDir=%v) = %v, want %v", c.rel, c.isDir, got, c.want)
		}
	}
}

func TestWriteSkillsTarGz(t *testing.T) {
	s, _ := openStore(t)
	junkySkill(t, s, "packme")

	var buf bytes.Buffer
	if err := s.WriteSkillsTarGz(&buf, []string{"packme"}); err != nil {
		t.Fatalf("WriteSkillsTarGz: %v", err)
	}
	entries := readTarGz(t, buf.Bytes())

	want := map[string]string{
		"packme/SKILL.md":                      "",
		"packme/references/keep.md":            "keep me",
		"packme/scripts/run.sh":                "#!/bin/sh\necho hi\n",
		"packme/references/evals/nested-ok.md": "kept: the evals rule is root-only",
	}
	for name, content := range want {
		got, ok := entries[name]
		if !ok {
			t.Errorf("missing entry %q; archive has %v", name, keys(entries))
			continue
		}
		if content != "" && got != content {
			t.Errorf("entry %q = %q, want %q", name, got, content)
		}
	}
	if body := entries["packme/SKILL.md"]; !strings.Contains(body, "name: packme") {
		t.Errorf("SKILL.md content = %q", body)
	}

	forbidden := []string{
		"packme/__pycache__/x.pyc",
		"packme/__pycache__/nested/y.txt",
		"packme/.DS_Store",
		"packme/references/.DS_Store",
		"packme/node_modules/y/index.js",
		"packme/evals/z.md",
		"packme/evals/deep/z2.md",
		"packme/nested/__pycache__/deep.pyc",
		"packme/stray.pyc",
		"packme/stray.PYO",
		"packme/Thumbs.db",
		"packme/.venv/lib/thing.py",
		"packme/venv/lib/thing.py",
		"packme/.git/config",
	}
	for _, name := range forbidden {
		if _, ok := entries[name]; ok {
			t.Errorf("excluded path %q was packaged", name)
		}
	}
	for name := range entries {
		if !strings.HasPrefix(name, "packme/") {
			t.Errorf("entry %q is not under the skill directory", name)
		}
		for _, bad := range []string{"__pycache__", "node_modules", ".DS_Store", ".venv", ".git/"} {
			if strings.Contains(name, bad) {
				t.Errorf("entry %q contains excluded component %q", name, bad)
			}
		}
	}
	if len(entries) != 4 {
		t.Errorf("archive has %d entries (%v), want 4", len(entries), keys(entries))
	}
}

func TestWriteSkillsTarGzMultipleSkills(t *testing.T) {
	s, _ := openStore(t)
	mustCreate(t, s, &Skill{Name: "one", Description: "First skill", Body: "One"})
	mustCreate(t, s, &Skill{Name: "two", Description: "Second skill", Body: "Two"})
	if err := s.WriteFile("two", "references/x.md", []byte("x")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := s.WriteSkillsTarGz(&buf, []string{"one", "two"}); err != nil {
		t.Fatal(err)
	}
	got := keys(readTarGz(t, buf.Bytes()))
	want := []string{"one/SKILL.md", "two/SKILL.md", "two/references/x.md"}
	if !sameStrings(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

func TestWriteSkillsTarGzErrors(t *testing.T) {
	s, _ := openStore(t)
	mustCreate(t, s, &Skill{Name: "real", Description: "Real skill", Body: "Body"})

	var buf bytes.Buffer
	if err := s.WriteSkillsTarGz(&buf, []string{"real", "ghost"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing skill = %v, want ErrNotFound", err)
	}
	buf.Reset()
	if err := s.WriteSkillsTarGz(&buf, []string{"../escape"}); err == nil {
		t.Errorf("traversing skill name = nil, want error")
	}
	buf.Reset()
	if err := s.WriteSkillsTarGz(&buf, nil); err != nil {
		t.Errorf("empty name list = %v, want nil", err)
	}
	if entries := readTarGz(t, buf.Bytes()); len(entries) != 0 {
		t.Errorf("empty archive has entries: %v", keys(entries))
	}
}

func TestWriteSkillZipSingleTopLevelDir(t *testing.T) {
	s, _ := openStore(t)
	junkySkill(t, s, "zipme")

	var buf bytes.Buffer
	if err := s.WriteSkillZip(&buf, "zipme"); err != nil {
		t.Fatalf("WriteSkillZip: %v", err)
	}
	entries := readZip(t, buf.Bytes())
	if len(entries) == 0 {
		t.Fatal("zip is empty")
	}

	tops := map[string]bool{}
	for name := range entries {
		parts := strings.SplitN(name, "/", 2)
		if len(parts) != 2 {
			t.Errorf("entry %q is not nested under a top-level directory", name)
			continue
		}
		tops[parts[0]] = true
	}
	if len(tops) != 1 || !tops["zipme"] {
		t.Errorf("top-level dirs = %v, want exactly {zipme}", tops)
	}
	if _, ok := entries["zipme/SKILL.md"]; !ok {
		t.Errorf("zip missing SKILL.md: %v", keys(entries))
	}
	if got := entries["zipme/references/keep.md"]; got != "keep me" {
		t.Errorf("zip content = %q", got)
	}
	for name := range entries {
		if strings.Contains(name, "__pycache__") || strings.Contains(name, "node_modules") ||
			strings.Contains(name, ".DS_Store") || strings.HasPrefix(name, "zipme/evals/") {
			t.Errorf("excluded path %q was zipped", name)
		}
	}
}

func TestWriteSkillZipMissingSkill(t *testing.T) {
	s, _ := openStore(t)
	var buf bytes.Buffer
	if err := s.WriteSkillZip(&buf, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("WriteSkillZip(ghost) = %v, want ErrNotFound", err)
	}
}

func TestWriteBundleZip(t *testing.T) {
	s, _ := openStore(t)
	mustCreate(t, s, &Skill{Name: "one", Description: "First skill", Body: "One"})
	mustCreate(t, s, &Skill{Name: "two", Description: "Second skill", Body: "Two"})
	b := &Bundle{Name: "pack", Skills: []string{"one", "two"}}
	if err := s.CreateBundle(b); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	manifest := []byte(`{"name":"pack"}`)
	if err := s.WriteBundleZip(&buf, b, manifest); err != nil {
		t.Fatalf("WriteBundleZip: %v", err)
	}
	entries := readZip(t, buf.Bytes())
	got := keys(entries)
	want := []string{".claude-plugin/marketplace.json", "skills/one/SKILL.md", "skills/two/SKILL.md"}
	if !sameStrings(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
	if entries[".claude-plugin/marketplace.json"] != string(manifest) {
		t.Errorf("manifest = %q", entries[".claude-plugin/marketplace.json"])
	}
}

// TestArchiveIncludesSymlinkedSkillContent is the export-side regression test
// for the WalkDir-on-a-symlinked-root problem: a plugin-linked skill must not
// download as an empty archive.
func TestArchiveIncludesSymlinkedSkillContent(t *testing.T) {
	s, base := openStore(t)
	real := filepath.Join(base, "real-skill")
	writeTree(t, real, map[string]string{
		"SKILL.md":          "---\nname: linked\ndescription: A symlinked skill directory\n---\n\nBody\n",
		"references/api.md": "reference",
	})
	tree := filepath.Join(base, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(tree, "skills", "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportTree(tree, true); err != nil {
		t.Fatalf("ImportTree: %v", err)
	}

	var buf bytes.Buffer
	if err := s.WriteSkillsTarGz(&buf, []string{"linked"}); err != nil {
		t.Fatalf("WriteSkillsTarGz: %v", err)
	}
	entries := readTarGz(t, buf.Bytes())
	got := keys(entries)
	want := []string{"linked/SKILL.md", "linked/references/api.md"}
	if !sameStrings(got, want) {
		t.Errorf("entries = %v, want %v (a symlinked skill dir must be packaged by value)", got, want)
	}
	if entries["linked/references/api.md"] != "reference" {
		t.Errorf("content = %q", entries["linked/references/api.md"])
	}
}

// seedFiles stores raw file contents against a skill that already exists,
// keeping whatever SKILL.md it already has.
func seedFiles(t *testing.T, s *Store, name string, files map[string]string) {
	t.Helper()
	raw := map[string][]byte{"SKILL.md": []byte(mustRead(t, s, name, "SKILL.md"))}
	for rel, body := range files {
		raw[rel] = []byte(body)
	}
	if err := s.WriteSkillFiles(name, raw, true); err != nil {
		t.Fatalf("seedFiles(%q): %v", name, err)
	}
}
