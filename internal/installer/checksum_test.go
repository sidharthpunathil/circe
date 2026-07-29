package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSkill materializes a skill directory from a path -> content map.
func writeSkill(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func mustChecksum(t *testing.T, dir string) string {
	t.Helper()
	sum, err := Checksum(dir)
	if err != nil {
		t.Fatalf("Checksum(%s): %v", dir, err)
	}
	if !strings.HasPrefix(sum, ChecksumPrefix) {
		t.Fatalf("checksum %q is missing the %q prefix", sum, ChecksumPrefix)
	}
	return sum
}

func TestChecksumIsDeterministic(t *testing.T) {
	files := map[string]string{
		"SKILL.md":              "---\nname: alpha\n---\n\nbody\n",
		"references/api.md":     "reference\n",
		"scripts/run.sh":        "#!/bin/sh\necho hi\n",
		"a/deeply/nested/f.txt": "nested\n",
	}

	a := writeSkill(t, filepath.Join(t.TempDir(), "alpha"), files)
	b := writeSkill(t, filepath.Join(t.TempDir(), "alpha"), files)

	// Walk order, mtimes and permissions must not leak into the digest: the
	// same skill downloaded twice has to hash identically.
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(filepath.Join(b, "SKILL.md"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(b, "scripts", "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	sumA, sumB := mustChecksum(t, a), mustChecksum(t, b)
	if sumA != sumB {
		t.Errorf("checksums differ across identical trees:\n  %s\n  %s", sumA, sumB)
	}
	if again := mustChecksum(t, a); again != sumA {
		t.Errorf("checksum is not stable across calls: %s then %s", sumA, again)
	}
}

func TestChecksumDetectsChanges(t *testing.T) {
	base := map[string]string{"SKILL.md": "body\n", "refs/a.md": "a\n"}
	orig := mustChecksum(t, writeSkill(t, filepath.Join(t.TempDir(), "s"), base))

	cases := map[string]map[string]string{
		"edited content":  {"SKILL.md": "body!\n", "refs/a.md": "a\n"},
		"renamed file":    {"SKILL.md": "body\n", "refs/b.md": "a\n"},
		"moved file":      {"SKILL.md": "body\n", "a.md": "a\n"},
		"extra file":      {"SKILL.md": "body\n", "refs/a.md": "a\n", "refs/c.md": "c\n"},
		"removed file":    {"SKILL.md": "body\n"},
		"content shifted": {"SKILL.md": "body\na", "refs/a.md": "\n"},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			if got := mustChecksum(t, writeSkill(t, filepath.Join(t.TempDir(), "s"), files)); got == orig {
				t.Errorf("checksum unchanged after %s", name)
			}
		})
	}
}

// TestChecksumIsUnambiguous pins the length prefixing: without it, moving bytes
// across a file boundary would hash the same.
func TestChecksumIsUnambiguous(t *testing.T) {
	a := mustChecksum(t, writeSkill(t, filepath.Join(t.TempDir(), "s"), map[string]string{
		"x/a.txt": "hello", "x/b.txt": "world",
	}))
	b := mustChecksum(t, writeSkill(t, filepath.Join(t.TempDir(), "s"), map[string]string{
		"x/a.txt": "hell", "x/b.txt": "oworld",
	}))
	if a == b {
		t.Error("checksum collides when bytes move across a file boundary")
	}
}

func TestChecksumIgnoresNoise(t *testing.T) {
	files := map[string]string{"SKILL.md": "body\n"}
	clean := writeSkill(t, filepath.Join(t.TempDir(), "s"), files)
	noisy := writeSkill(t, filepath.Join(t.TempDir(), "s"), files)

	// The registry never ships these, so hashing them would make `list` report
	// permanent drift on any machine that creates them.
	writeSkill(t, noisy, map[string]string{
		".DS_Store":                   "junk",
		"__pycache__/mod.cpython.pyc": "junk",
		"node_modules/pkg/index.js":   "junk",
		"scripts/mod.pyc":             "junk",
	})

	if mustChecksum(t, clean) != mustChecksum(t, noisy) {
		t.Error("checksum changed after adding files the registry never packs")
	}
}

func TestChecksumSkipsSymlinks(t *testing.T) {
	dir := writeSkill(t, filepath.Join(t.TempDir(), "s"), map[string]string{"SKILL.md": "body\n"})
	before := mustChecksum(t, dir)
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if after := mustChecksum(t, dir); after != before {
		t.Error("checksum followed a symlink; skill content should be regular files only")
	}
}

func TestChecksumMissingDir(t *testing.T) {
	if _, err := Checksum(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected an error for a missing directory")
	}
}
