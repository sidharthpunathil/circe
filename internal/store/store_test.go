package store

// Test suite for the store package.
//
// Tests whose names begin with TestBug_ document *genuine defects found in the
// non-test code*. They are expected to FAIL until the product code is fixed;
// they deliberately assert the correct behaviour rather than the current one,
// because pinning a security hole into the test suite would bless it. As of
// writing there are three:
//
//	TestBug_ReadFileSkillMdBypassesValidation        path traversal via skill name
//	TestBug_WriteFileEscapesThroughSymlinkedDirectory symlink escape on write
//	TestBug_MutationOfCallerOwnedState                shared-state mutation / data race
//
// Everything else should be green.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sidharthpunathil/skillbuilder/internal/skillfile"
)

// ---------- helpers ----------

// newStore opens a store rooted at <tmp>/data and returns it with the temp base
// directory, so tests can assert that nothing ever escapes into the base.
func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	base := t.TempDir()
	s, err := Open(filepath.Join(base, "data"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, base
}

func mustCreate(t *testing.T, s *Store, sk *Skill) {
	t.Helper()
	if err := s.CreateSkill(sk); err != nil {
		t.Fatalf("CreateSkill(%q): %v", sk.Name, err)
	}
}

// writeTree writes rel path -> content under dir, creating parents.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// snapshot lists every path under root (relative, slash separated, sorted).
// Symlinks are recorded but not followed.
func snapshot(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	sort.Strings(out)
	return out
}

func sameStrings(a, b []string) bool {
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

func skillNames(skills []*Skill) []string {
	out := make([]string, 0, len(skills))
	for _, sk := range skills {
		out = append(out, sk.Name)
	}
	return out
}

func filePaths(files []FileRef) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// ---------- 1. round trip + on-disk layout ----------

func TestCreateSkillRoundTripsThroughDisk(t *testing.T) {
	s, base := newStore(t)
	root := s.Root()

	want := &Skill{
		Name:        "my-skill",
		Description: "Turns arbitrary input into a knowledge graph",
		Body:        "# My Skill\n\nStep one.\nStep two.\n",
		Tags:        []string{"graph", "research"},
	}
	mustCreate(t, s, want)

	skillMd := filepath.Join(root, "skills", "my-skill", "SKILL.md")
	if _, err := os.Stat(skillMd); err != nil {
		t.Fatalf("expected SKILL.md at %s: %v", skillMd, err)
	}

	// Layout must be exactly data/skills/<name>/SKILL.md (plus the empty
	// bundles dir the store creates on Open).
	got := snapshot(t, base)
	wantPaths := []string{
		".",
		"data",
		"data/bundles",
		"data/skills",
		"data/skills/my-skill",
		"data/skills/my-skill/SKILL.md",
	}
	if !sameStrings(got, wantPaths) {
		t.Errorf("on-disk layout = %v, want %v", got, wantPaths)
	}

	// Reload from scratch: a second Open must see identical content.
	s2, err := Open(root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	sk, err := s2.Skill("my-skill")
	if err != nil {
		t.Fatalf("Skill after reopen: %v", err)
	}
	if sk.Name != want.Name {
		t.Errorf("name = %q, want %q", sk.Name, want.Name)
	}
	if sk.Description != want.Description {
		t.Errorf("description = %q, want %q", sk.Description, want.Description)
	}
	if sk.Body != want.Body {
		t.Errorf("body = %q, want %q", sk.Body, want.Body)
	}
	if !sameStrings(sk.Tags, want.Tags) {
		t.Errorf("tags = %v, want %v", sk.Tags, want.Tags)
	}
	if len(sk.Files) != 0 {
		t.Errorf("SKILL.md must not be listed as a bundled file, got %v", filePaths(sk.Files))
	}
	if sk.Size == 0 {
		t.Errorf("size = 0, want the size of SKILL.md")
	}
	if sk.UpdatedAt.IsZero() {
		t.Errorf("updatedAt is zero")
	}

	// Frontmatter on disk carries the name and description.
	raw, err := os.ReadFile(skillMd)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "---\nname: my-skill\n") {
		t.Errorf("unexpected frontmatter:\n%s", raw)
	}
}

func TestSaveSkillPreservesUnknownFrontmatter(t *testing.T) {
	s, _ := newStore(t)
	sk := &Skill{
		Name:        "extras",
		Description: "Keeps hand-authored frontmatter keys",
		Body:        "Body",
		Front: skillfile.Frontmatter{
			License: "MIT",
			Extra:   map[string]any{"argument-hint": "<file>"},
		},
	}
	mustCreate(t, s, sk)

	raw, err := os.ReadFile(filepath.Join(s.skillDir("extras"), "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"license: MIT", "argument-hint: <file>"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("frontmatter lost %q:\n%s", want, raw)
		}
	}
}

func TestReloadPicksUpDirectDiskEdits(t *testing.T) {
	s, _ := newStore(t)
	dir := s.skillDir("hand-written")
	writeTree(t, dir, map[string]string{
		"SKILL.md":           "---\nname: hand-written\ndescription: Authored by hand\n---\n\nBody\n",
		"references/api.md":  "reference",
		"scripts/run.sh":     "#!/bin/sh\n",
		".hidden/secret.txt": "ignored",
	})
	if err := s.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	sk, err := s.Skill("hand-written")
	if err != nil {
		t.Fatalf("Skill: %v", err)
	}
	if got, want := filePaths(sk.Files), []string{"references/api.md", "scripts/run.sh"}; !sameStrings(got, want) {
		t.Errorf("files = %v, want %v (hidden dirs must be skipped)", got, want)
	}
}

func TestReloadSkipsInvalidDirectoryNames(t *testing.T) {
	s, _ := newStore(t)
	for _, name := range []string{"Bad_Name", ".hidden-skill", "Upper"} {
		writeTree(t, filepath.Join(s.skillsDir(), name), map[string]string{
			"SKILL.md": "---\nname: x\ndescription: y\n---\n\nBody\n",
		})
	}
	writeTree(t, s.skillDir("good-one"), map[string]string{
		"SKILL.md": "---\nname: good-one\ndescription: Fine\n---\n\nBody\n",
	})
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := skillNames(s.Skills()); !sameStrings(got, []string{"good-one"}) {
		t.Errorf("skills = %v, want [good-one]", got)
	}
}

func TestReloadSkipsMalformedSkillWithoutFailing(t *testing.T) {
	s, _ := newStore(t)
	// Unparseable YAML frontmatter.
	writeTree(t, s.skillDir("broken"), map[string]string{
		"SKILL.md": "---\nname: [unclosed\n---\n\nBody\n",
	})
	writeTree(t, s.skillDir("healthy"), map[string]string{
		"SKILL.md": "---\nname: healthy\ndescription: Fine\n---\n\nBody\n",
	})
	if err := s.Reload(); err != nil {
		t.Fatalf("Reload must tolerate a malformed skill: %v", err)
	}
	if _, err := s.Skill("healthy"); err != nil {
		t.Errorf("healthy skill should still load: %v", err)
	}
	if _, err := s.Skill("broken"); !errors.Is(err, ErrNotFound) {
		t.Errorf("broken skill err = %v, want ErrNotFound", err)
	}
}

// ---------- 2. validation + slugify ----------

func TestValidateName(t *testing.T) {
	valid := []string{"my-skill", "a1", "a", "0", "a-b-c-1", strings.Repeat("a", 64)}
	for _, n := range valid {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	invalid := []string{
		"", "My-Skill", "my_skill", "-lead", "trail-", "dou--ble",
		"a/b", "..", ".", "a b", "a.b", "a\\b", "../x", "x/../y",
		"héllo", "a-", "-", strings.Repeat("a", 65), " my-skill", "my-skill ",
		"SKILL.md", "\x00", "a\nb",
	}
	for _, n := range invalid {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", n)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"my-skill", "my-skill"},
		{"My Skill", "my-skill"},
		{"  Spaced   Out  ", "spaced-out"},
		{"a///b", "a-b"},     // runs collapse to a single hyphen
		{"a___b", "a-b"},     // underscores are separators
		{"--lead--", "lead"}, // leading/trailing hyphens trimmed
		{"...", ""},          // nothing alphanumeric
		{"", ""},             // empty
		{"!!!", ""},          //
		{"Hello, World!", "hello-world"},
		{"C++ / Rust", "c-rust"},
		{"tag1, tag2", "tag1-tag2"},
		{"ÜNÏCØDÉ", "n-c-d"}, // non-ASCII letters are separators
		{strings.Repeat("a", 70), strings.Repeat("a", 64)},
		// Truncation must not leave a trailing hyphen.
		{strings.Repeat("a", 63) + " bcd", strings.Repeat("a", 63)},
	}
	for _, c := range cases {
		if got := Slugify(c.in); got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// Property: a non-empty slug is always a valid name.
	for _, c := range cases {
		got := Slugify(c.in)
		if got == "" {
			continue
		}
		if err := ValidateName(got); err != nil {
			t.Errorf("Slugify(%q) = %q which fails ValidateName: %v", c.in, got, err)
		}
	}
}

// ---------- 3. path traversal ----------

// evilPaths must all be refused by every path-taking API.
var evilPaths = []string{
	"../escape.txt",
	"../../etc/passwd",
	"../../../../../../etc/passwd",
	"/etc/passwd",
	"/",
	"foo/../../bar",
	".hidden",
	"a/./../../b",
	"..",
	".",
	"./",
	"",
	"   ",
	"..\\escape.txt",
	"..\\..\\etc\\passwd",
	"foo\\..\\..\\bar",
	"\\etc\\passwd",
	"a/../../b",
	"nested/../../../escape.txt",
	".ssh/authorized_keys",
	"sub/.hidden",
	"sub/../.hidden",
	"//etc/passwd",
	"a//../../b",
}

func TestPathTraversalIsRefusedAndTouchesNothing(t *testing.T) {
	s, base := newStore(t)
	mustCreate(t, s, &Skill{Name: "victim", Description: "Traversal target", Body: "Body"})
	mustCreate(t, s, &Skill{Name: "neighbour", Description: "Should be untouched", Body: "Body"})

	// Decoys the attacker might try to read, clobber or delete.
	writeTree(t, base, map[string]string{
		"escape.txt":      "outside the store",
		"secrets/key.pem": "private",
	})

	before := snapshot(t, base)

	for _, rel := range evilPaths {
		t.Run("rel="+strings.ReplaceAll(rel, "/", "|"), func(t *testing.T) {
			if err := s.WriteFile("victim", rel, []byte("pwned")); err == nil {
				t.Errorf("WriteFile(%q) = nil, want error", rel)
			}
			if _, err := s.ReadFile("victim", rel); err == nil {
				t.Errorf("ReadFile(%q) = nil, want error", rel)
			}
			if err := s.DeleteFile("victim", rel); err == nil {
				t.Errorf("DeleteFile(%q) = nil, want error", rel)
			}
		})
	}

	after := snapshot(t, base)
	if !sameStrings(before, after) {
		t.Errorf("filesystem changed during traversal attempts\nbefore: %v\nafter:  %v", before, after)
	}
	if b, err := os.ReadFile(filepath.Join(base, "escape.txt")); err != nil || string(b) != "outside the store" {
		t.Errorf("decoy file was modified: %q %v", b, err)
	}
}

func TestPathTraversalViaSkillName(t *testing.T) {
	s, base := newStore(t)
	mustCreate(t, s, &Skill{Name: "victim", Description: "Traversal target", Body: "Body"})
	writeTree(t, base, map[string]string{"outside/notes.md": "secret"})

	before := snapshot(t, base)
	badSkills := []string{"../outside", "../../outside", "/etc", "..", ".", "", "Up..Per", "a/b"}
	for _, skill := range badSkills {
		if err := s.WriteFile(skill, "notes.md", []byte("pwned")); err == nil {
			t.Errorf("WriteFile(skill=%q) = nil, want error", skill)
		}
		if _, err := s.ReadFile(skill, "notes.md"); err == nil {
			t.Errorf("ReadFile(skill=%q) = nil, want error", skill)
		}
		if err := s.DeleteFile(skill, "notes.md"); err == nil {
			t.Errorf("DeleteFile(skill=%q) = nil, want error", skill)
		}
	}
	if after := snapshot(t, base); !sameStrings(before, after) {
		t.Errorf("filesystem changed\nbefore: %v\nafter:  %v", before, after)
	}
}

// TestBug_ReadFileSkillMdBypassesValidation demonstrates that ReadFile special
// cases rel == "SKILL.md" and reads it without validating the skill name, so the
// skill name itself becomes a path traversal vector.
func TestBug_ReadFileSkillMdBypassesValidation(t *testing.T) {
	s, base := newStore(t)
	writeTree(t, base, map[string]string{"outside/SKILL.md": "TOP SECRET"})

	got, err := s.ReadFile("../../outside", "SKILL.md")
	if err == nil {
		t.Errorf("BUG (path traversal): ReadFile(%q, \"SKILL.md\") returned %q from outside the data root; "+
			"ReadFile short-circuits rel==\"SKILL.md\" and joins the unvalidated skill name instead of "+
			"going through safeJoin. Any file named SKILL.md on the host is readable. "+
			"Fix: call ValidateName(skill) (or route through safeJoin) before the short circuit.",
			"../../outside", string(got))
	}
	if _, err := s.ReadFile("../..", "SKILL.md"); err == nil {
		t.Errorf("BUG: ReadFile with a traversing skill name must be rejected")
	}
}

func TestLegitimateNestedFileLifecycle(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, &Skill{Name: "docs", Description: "Has reference files", Body: "Body"})

	if err := s.WriteFile("docs", "references/api.md", []byte("# API\n")); err != nil {
		t.Fatalf("WriteFile(references/api.md): %v", err)
	}
	got, err := s.ReadFile("docs", "references/api.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "# API\n" {
		t.Errorf("content = %q", got)
	}
	full := filepath.Join(s.skillDir("docs"), "references", "api.md")
	if _, err := os.Stat(full); err != nil {
		t.Errorf("expected file at %s: %v", full, err)
	}

	sk, err := s.Skill("docs")
	if err != nil {
		t.Fatal(err)
	}
	if got := filePaths(sk.Files); !sameStrings(got, []string{"references/api.md"}) {
		t.Errorf("index files = %v, want [references/api.md]", got)
	}

	// Overwrite in place.
	if err := s.WriteFile("docs", "references/api.md", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReadFile("docs", "references/api.md"); string(got) != "v2" {
		t.Errorf("after overwrite content = %q", got)
	}

	// A path that cleans back inside the skill dir is fine.
	if err := s.WriteFile("docs", "references/../notes.md", []byte("note")); err != nil {
		t.Errorf("WriteFile(references/../notes.md): %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.skillDir("docs"), "notes.md")); err != nil {
		t.Errorf("expected notes.md at the skill root: %v", err)
	}

	// Delete prunes the now-empty directory but keeps the skill dir.
	if err := s.DeleteFile("docs", "references/api.md"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.skillDir("docs"), "references")); !os.IsNotExist(err) {
		t.Errorf("empty references dir should have been pruned, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.skillDir("docs"), "SKILL.md")); err != nil {
		t.Errorf("SKILL.md must survive DeleteFile: %v", err)
	}
	if _, err := s.ReadFile("docs", "references/api.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadFile after delete = %v, want ErrNotFound", err)
	}
}

func TestSkillMdIsProtected(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, &Skill{Name: "guard", Description: "Protects SKILL.md", Body: "Body"})

	if err := s.WriteFile("guard", "SKILL.md", []byte("hijacked")); err == nil {
		t.Errorf("WriteFile(SKILL.md) = nil, want error")
	}
	if err := s.DeleteFile("guard", "SKILL.md"); err == nil {
		t.Errorf("DeleteFile(SKILL.md) = nil, want error")
	}
	raw, err := s.ReadFile("guard", "SKILL.md")
	if err != nil {
		t.Fatalf("ReadFile(SKILL.md): %v", err)
	}
	if !strings.Contains(string(raw), "name: guard") {
		t.Errorf("ReadFile(SKILL.md) = %q", raw)
	}
}

func TestWriteFileSizeLimit(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, &Skill{Name: "big", Description: "Size limits", Body: "Body"})
	if err := s.WriteFile("big", "blob.bin", make([]byte, maxFileSize+1)); err == nil {
		t.Errorf("WriteFile over the limit = nil, want error")
	}
	if _, err := os.Stat(filepath.Join(s.skillDir("big"), "blob.bin")); !os.IsNotExist(err) {
		t.Errorf("oversized file should not exist: %v", err)
	}
}

// ---------- 4. symlink escapes ----------

func TestSymlinkEscapeIsRefused(t *testing.T) {
	s, base := newStore(t)
	mustCreate(t, s, &Skill{Name: "victim", Description: "Symlink target", Body: "Body"})
	dir := s.skillDir("victim")

	outsideDir := filepath.Join(base, "outside")
	writeTree(t, outsideDir, map[string]string{"secret.txt": "classified"})

	if err := os.Symlink(filepath.Join(outsideDir, "secret.txt"), filepath.Join(dir, "leak.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(dir, "leakdir")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	t.Run("read through symlinked file", func(t *testing.T) {
		if got, err := s.ReadFile("victim", "leak.txt"); err == nil {
			t.Errorf("ReadFile through symlink = %q, want error", got)
		}
	})
	t.Run("read through symlinked dir", func(t *testing.T) {
		if got, err := s.ReadFile("victim", "leakdir/secret.txt"); err == nil {
			t.Errorf("ReadFile through symlinked dir = %q, want error", got)
		}
		if got, err := s.ReadFile("victim", "leakdir"); err == nil {
			t.Errorf("ReadFile of symlinked dir = %q, want error", got)
		}
	})
	t.Run("write through symlinked file", func(t *testing.T) {
		if err := s.WriteFile("victim", "leak.txt", []byte("clobbered")); err == nil {
			t.Errorf("WriteFile through symlink = nil, want error")
		}
		if b, _ := os.ReadFile(filepath.Join(outsideDir, "secret.txt")); string(b) != "classified" {
			t.Errorf("outside file was clobbered: %q", b)
		}
	})
	t.Run("write over an existing file through a symlinked dir", func(t *testing.T) {
		if err := s.WriteFile("victim", "leakdir/secret.txt", []byte("clobbered")); err == nil {
			t.Errorf("WriteFile through symlinked dir = nil, want error")
		}
		if b, _ := os.ReadFile(filepath.Join(outsideDir, "secret.txt")); string(b) != "classified" {
			t.Errorf("outside file was clobbered: %q", b)
		}
	})
	t.Run("delete through symlink", func(t *testing.T) {
		if err := s.DeleteFile("victim", "leakdir/secret.txt"); err == nil {
			t.Errorf("DeleteFile through symlinked dir = nil, want error")
		}
		if _, err := os.Stat(filepath.Join(outsideDir, "secret.txt")); err != nil {
			t.Errorf("outside file was deleted: %v", err)
		}
	})
}

// TestBug_WriteFileEscapesThroughSymlinkedDirectory shows that safeJoin's
// symlink guard only fires when the *final* path already exists, so creating a
// new file inside a symlinked directory writes outside the skill directory.
func TestBug_WriteFileEscapesThroughSymlinkedDirectory(t *testing.T) {
	s, base := newStore(t)
	mustCreate(t, s, &Skill{Name: "victim", Description: "Symlink target", Body: "Body"})

	outsideDir := filepath.Join(base, "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(s.skillDir("victim"), "leakdir")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := s.WriteFile("victim", "leakdir/pwned.txt", []byte("owned"))
	escaped := filepath.Join(outsideDir, "pwned.txt")
	if _, statErr := os.Stat(escaped); statErr == nil {
		t.Errorf("BUG (symlink escape on write): WriteFile(%q) returned err=%v and created %s, "+
			"outside the skill directory and outside the data root. safeJoin only checks "+
			"filepath.EvalSymlinks(full), which fails for a not-yet-existing file, so the guard "+
			"is skipped entirely. Fix: resolve the nearest existing ancestor of full (walk up until "+
			"EvalSymlinks succeeds) and require it to stay inside the resolved skill dir.",
			"leakdir/pwned.txt", err, escaped)
	}

	// Same hole for a nested directory created underneath the symlink.
	err = s.WriteFile("victim", "leakdir/deep/nested.txt", []byte("owned"))
	if _, statErr := os.Stat(filepath.Join(outsideDir, "deep", "nested.txt")); statErr == nil {
		t.Errorf("BUG (symlink escape on write): WriteFile(%q) err=%v created a whole directory tree "+
			"outside the skill directory", "leakdir/deep/nested.txt", err)
	}
}

// ---------- 5. tags ----------

func TestTagsFromMetadataAndTopLevel(t *testing.T) {
	cases := []struct {
		name     string
		frontYML string
		want     []string
	}{
		{
			name:     "metadata-tags",
			frontYML: "metadata:\n  tags:\n    - Alpha\n    - \"Beta Two\"\n",
			want:     []string{"alpha", "beta-two"},
		},
		{
			name:     "top-level-tags",
			frontYML: "tags:\n  - Gamma\n  - delta\n",
			want:     []string{"delta", "gamma"},
		},
		{
			name:     "both-deduped",
			frontYML: "tags:\n  - shared\n  - only-top\nmetadata:\n  tags:\n    - shared\n    - only-meta\n",
			want:     []string{"only-meta", "only-top", "shared"},
		},
		{
			name:     "inline-string",
			frontYML: "tags: \"one, Two, one\"\n",
			want:     []string{"one", "two"},
		},
		{
			name:     "junk-entries-dropped",
			frontYML: "metadata:\n  tags:\n    - \"!!!\"\n    - \"  \"\n    - ok\n",
			want:     []string{"ok"},
		},
		{
			name:     "no-tags",
			frontYML: "",
			want:     nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newStore(t)
			src := "---\nname: " + c.name + "\ndescription: A skill with tags\n" + c.frontYML + "---\n\nBody\n"
			writeTree(t, s.skillDir(c.name), map[string]string{"SKILL.md": src})
			if err := s.Reload(); err != nil {
				t.Fatal(err)
			}
			sk, err := s.Skill(c.name)
			if err != nil {
				t.Fatalf("Skill: %v", err)
			}
			if !sameStrings(sk.Tags, c.want) {
				t.Errorf("tags = %v, want %v", sk.Tags, c.want)
			}
		})
	}
}

func TestParseTags(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{",,,", nil},
		{"foo", []string{"foo"}},
		{"Foo, bar, foo, FOO", []string{"bar", "foo"}},         // dedupe, case-insensitive
		{"zeta, alpha, mid", []string{"alpha", "mid", "zeta"}}, // sorted
		{"Bar Baz, qux", []string{"bar-baz", "qux"}},           // slugified
		{" spaced , tabs\t", []string{"spaced", "tabs"}},       // trimmed
		{"a--b, a-b", []string{"a-b"}},                         // slug collapse then dedupe
	}
	for _, c := range cases {
		if got := ParseTags(c.in); !sameStrings(got, c.want) {
			t.Errorf("ParseTags(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestTagsCSV(t *testing.T) {
	sk := &Skill{Tags: []string{"a", "b-c"}}
	if got := sk.TagsCSV(); got != "a, b-c" {
		t.Errorf("TagsCSV = %q", got)
	}
	if got := (&Skill{}).TagsCSV(); got != "" {
		t.Errorf("empty TagsCSV = %q", got)
	}
}

func TestSaveSkillRemovesStaleMetadataTags(t *testing.T) {
	s, _ := newStore(t)
	writeTree(t, s.skillDir("stale"), map[string]string{
		"SKILL.md": "---\nname: stale\ndescription: Has tags to drop\nmetadata:\n  author: someone\n  tags:\n    - keep-me\n---\n\nBody\n",
	})
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	sk, err := s.Skill("stale")
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(sk.Tags, []string{"keep-me"}) {
		t.Fatalf("precondition: tags = %v", sk.Tags)
	}

	sk.Tags = nil
	if err := s.SaveSkill(sk); err != nil {
		t.Fatalf("SaveSkill: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(s.skillDir("stale"), "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tags:") {
		t.Errorf("stale metadata.tags survived the save:\n%s", raw)
	}
	if !strings.Contains(string(raw), "author: someone") {
		t.Errorf("other metadata keys must be preserved:\n%s", raw)
	}

	s2, err := Open(s.Root())
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := s2.Skill("stale")
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Tags) != 0 {
		t.Errorf("tags after reload = %v, want none", reloaded.Tags)
	}
}

func TestSaveSkillDropsMetadataMapWhenItBecomesEmpty(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, &Skill{Name: "only-tags", Description: "Tags are the only metadata", Body: "Body", Tags: []string{"x"}})

	sk, err := s.Skill("only-tags")
	if err != nil {
		t.Fatal(err)
	}
	sk.Tags = nil
	if err := s.SaveSkill(sk); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.skillDir("only-tags"), "SKILL.md"))
	if strings.Contains(string(raw), "metadata:") {
		t.Errorf("empty metadata block should be dropped:\n%s", raw)
	}
}

func TestSaveSkillDropsTopLevelTagsKey(t *testing.T) {
	s, _ := newStore(t)
	writeTree(t, s.skillDir("toptags"), map[string]string{
		"SKILL.md": "---\nname: toptags\ndescription: Top level tags key\ntags:\n  - one\n  - two\n---\n\nBody\n",
	})
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	sk, err := s.Skill("toptags")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSkill(sk); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.skillDir("toptags"), "SKILL.md"))
	if n := strings.Count(string(raw), "tags:"); n != 1 {
		t.Errorf("want exactly one tags key (under metadata), got %d:\n%s", n, raw)
	}
	if !strings.Contains(string(raw), "metadata:") {
		t.Errorf("tags should have moved under metadata:\n%s", raw)
	}
	// And they survive a round trip.
	s2, _ := Open(s.Root())
	again, err := s2.Skill("toptags")
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(again.Tags, []string{"one", "two"}) {
		t.Errorf("tags after round trip = %v", again.Tags)
	}
}

func TestTagsAggregation(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, &Skill{Name: "one", Description: "First", Body: "b", Tags: []string{"cli", "go"}})
	mustCreate(t, s, &Skill{Name: "two", Description: "Second", Body: "b", Tags: []string{"cli"}})
	mustCreate(t, s, &Skill{Name: "three", Description: "Third", Body: "b", Tags: []string{"web"}})

	got := s.Tags()
	want := []TagCount{{"cli", 2}, {"go", 1}, {"web", 1}}
	if len(got) != len(want) {
		t.Fatalf("Tags() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Tags()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// ---------- 6. search ----------

func searchFixture(t *testing.T) *Store {
	t.Helper()
	s, _ := newStore(t)
	mustCreate(t, s, &Skill{Name: "alpha", Description: "The first Greek letter, used for testing", Body: "Nothing notable here.", Tags: []string{"greek"}})
	mustCreate(t, s, &Skill{Name: "alpha-tools", Description: "Helper utilities", Body: "Nothing notable here.", Tags: []string{"greek"}})
	mustCreate(t, s, &Skill{Name: "beta", Description: "Mentions alpha inside the description", Body: "Nothing notable here.", Tags: []string{"greek"}})
	mustCreate(t, s, &Skill{Name: "gamma", Description: "Unrelated summary text", Body: "Deep in the body we mention alpha once.", Tags: []string{"greek"}})
	mustCreate(t, s, &Skill{Name: "delta", Description: "Unrelated too", Body: "Nothing notable here.", Tags: []string{"cli", "shell"}})
	return s
}

func TestSearchRanking(t *testing.T) {
	s := searchFixture(t)

	got := skillNames(s.Search("alpha", ""))
	want := []string{"alpha", "alpha-tools", "beta", "gamma"}
	if !sameStrings(got, want) {
		t.Errorf("Search(alpha) = %v, want %v (exact name > prefix > description > body)", got, want)
	}
	if len(got) > 0 && got[0] != "alpha" {
		t.Errorf("exact name match must rank first, got %q", got[0])
	}
	if len(got) > 0 && got[len(got)-1] != "gamma" {
		t.Errorf("body-only match must rank last, got %q", got[len(got)-1])
	}
}

func TestSearchCaseInsensitive(t *testing.T) {
	s := searchFixture(t)
	lower := skillNames(s.Search("alpha", ""))
	upper := skillNames(s.Search("ALPHA", ""))
	mixed := skillNames(s.Search("  AlPhA  ", ""))
	if !sameStrings(lower, upper) || !sameStrings(lower, mixed) {
		t.Errorf("case/whitespace sensitivity: %v vs %v vs %v", lower, upper, mixed)
	}
}

func TestSearchEmptyQueryReturnsAllSortedByName(t *testing.T) {
	s := searchFixture(t)
	got := skillNames(s.Search("", ""))
	want := []string{"alpha", "alpha-tools", "beta", "delta", "gamma"}
	if !sameStrings(got, want) {
		t.Errorf("Search(\"\") = %v, want %v", got, want)
	}
	if got2 := skillNames(s.Search("   ", "   ")); !sameStrings(got2, want) {
		t.Errorf("whitespace-only query = %v, want %v", got2, want)
	}
}

func TestSearchTagFilter(t *testing.T) {
	s := searchFixture(t)
	if got := skillNames(s.Search("", "cli")); !sameStrings(got, []string{"delta"}) {
		t.Errorf("Search(\"\", cli) = %v, want [delta]", got)
	}
	if got := skillNames(s.Search("", "nope")); len(got) != 0 {
		t.Errorf("unknown tag = %v, want empty", got)
	}
	if got := skillNames(s.Search("alpha", "cli")); len(got) != 0 {
		t.Errorf("Search(alpha, cli) = %v, want empty", got)
	}
	if got := skillNames(s.Search("alpha", "greek")); !sameStrings(got, []string{"alpha", "alpha-tools", "beta", "gamma"}) {
		t.Errorf("Search(alpha, greek) = %v", got)
	}
	// Tag matching is exact, not substring.
	if got := skillNames(s.Search("", "cl")); len(got) != 0 {
		t.Errorf("partial tag matched: %v", got)
	}
}

func TestSearchMatchesTags(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, &Skill{Name: "one", Description: "Nothing to see", Body: "nothing", Tags: []string{"kubernetes"}})
	mustCreate(t, s, &Skill{Name: "two", Description: "Nothing to see", Body: "nothing"})
	if got := skillNames(s.Search("kube", "")); !sameStrings(got, []string{"one"}) {
		t.Errorf("Search(kube) = %v, want [one]", got)
	}
}

func TestSearchNoMatches(t *testing.T) {
	s := searchFixture(t)
	if got := s.Search("zzzznothing", ""); len(got) != 0 {
		t.Errorf("Search(nonsense) = %v, want empty", skillNames(got))
	}
}

func TestSkillCount(t *testing.T) {
	s := searchFixture(t)
	if got := s.SkillCount(); got != 5 {
		t.Errorf("SkillCount = %d, want 5", got)
	}
}

// ---------- 7. bundles ----------

func bundleFixture(t *testing.T) *Store {
	t.Helper()
	s, _ := newStore(t)
	for _, n := range []string{"alpha", "beta", "gamma"} {
		mustCreate(t, s, &Skill{Name: n, Description: "Skill " + n, Body: "Body of " + n})
	}
	return s
}

func TestCreateBundle(t *testing.T) {
	s := bundleFixture(t)
	b := &Bundle{Name: "starter", Title: "Starter Pack", Description: "Getting going", Skills: []string{"alpha", "beta"}}
	if err := s.CreateBundle(b); err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}
	path := filepath.Join(s.Root(), "bundles", "starter.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected bundle at %s: %v", path, err)
	}

	s2, err := Open(s.Root())
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.Bundle("starter")
	if err != nil {
		t.Fatalf("Bundle after reopen: %v", err)
	}
	if got.Title != "Starter Pack" || got.Description != "Getting going" {
		t.Errorf("bundle metadata lost: %+v", got)
	}
	if !sameStrings(got.Skills, []string{"alpha", "beta"}) {
		t.Errorf("skills = %v", got.Skills)
	}
	if got.UpdatedAt.IsZero() {
		t.Errorf("updatedAt not stamped")
	}
	if s2.BundleCount() != 1 {
		t.Errorf("BundleCount = %d, want 1", s2.BundleCount())
	}
	if names := skillNames(s2.BundleSkills(got)); !sameStrings(names, []string{"alpha", "beta"}) {
		t.Errorf("BundleSkills = %v", names)
	}
}

func TestSaveBundleDropsUnknownSkillsAndDedupes(t *testing.T) {
	s := bundleFixture(t)
	b := &Bundle{Name: "messy", Skills: []string{"alpha", "ghost", "alpha", "beta", "beta", "does-not-exist"}}
	if err := s.SaveBundle(b); err != nil {
		t.Fatalf("SaveBundle: %v", err)
	}
	if !sameStrings(b.Skills, []string{"alpha", "beta"}) {
		t.Errorf("in-memory skills = %v, want [alpha beta]", b.Skills)
	}
	s2, _ := Open(s.Root())
	got, err := s2.Bundle("messy")
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(got.Skills, []string{"alpha", "beta"}) {
		t.Errorf("on-disk skills = %v, want [alpha beta]", got.Skills)
	}
}

func TestSaveBundleKeepsOrder(t *testing.T) {
	s := bundleFixture(t)
	b := &Bundle{Name: "ordered", Skills: []string{"gamma", "alpha", "beta"}}
	if err := s.SaveBundle(b); err != nil {
		t.Fatal(err)
	}
	if !sameStrings(b.Skills, []string{"gamma", "alpha", "beta"}) {
		t.Errorf("order not preserved: %v", b.Skills)
	}
}

func TestDeleteSkillRemovesItFromEveryBundle(t *testing.T) {
	s := bundleFixture(t)
	for _, name := range []string{"one", "two"} {
		if err := s.CreateBundle(&Bundle{Name: name, Skills: []string{"alpha", "beta"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateBundle(&Bundle{Name: "three", Skills: []string{"gamma"}}); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteSkill("beta"); err != nil {
		t.Fatalf("DeleteSkill: %v", err)
	}
	if _, err := os.Stat(s.skillDir("beta")); !os.IsNotExist(err) {
		t.Errorf("skill dir still present: %v", err)
	}
	if _, err := s.Skill("beta"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Skill(beta) = %v, want ErrNotFound", err)
	}

	s2, _ := Open(s.Root())
	for _, name := range []string{"one", "two"} {
		b, err := s2.Bundle(name)
		if err != nil {
			t.Fatal(err)
		}
		if b.Has("beta") {
			t.Errorf("bundle %q still references the deleted skill: %v", name, b.Skills)
		}
		if !sameStrings(b.Skills, []string{"alpha"}) {
			t.Errorf("bundle %q skills = %v, want [alpha]", name, b.Skills)
		}
	}
	if b, _ := s2.Bundle("three"); !sameStrings(b.Skills, []string{"gamma"}) {
		t.Errorf("unrelated bundle changed: %v", b.Skills)
	}
	if got := len(s2.BundlesContaining("alpha")); got != 2 {
		t.Errorf("BundlesContaining(alpha) = %d, want 2", got)
	}
}

func TestRenameSkillUpdatesBundlesAndFrontmatter(t *testing.T) {
	s := bundleFixture(t)
	if err := s.CreateBundle(&Bundle{Name: "pack", Skills: []string{"alpha", "beta"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteFile("alpha", "references/api.md", []byte("keep me")); err != nil {
		t.Fatal(err)
	}

	if err := s.RenameSkill("alpha", "alpha-renamed"); err != nil {
		t.Fatalf("RenameSkill: %v", err)
	}

	if _, err := os.Stat(s.skillDir("alpha")); !os.IsNotExist(err) {
		t.Errorf("old skill dir still exists: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(s.skillDir("alpha-renamed"), "SKILL.md"))
	if err != nil {
		t.Fatalf("new SKILL.md: %v", err)
	}
	if !strings.Contains(string(raw), "name: alpha-renamed") {
		t.Errorf("frontmatter name not rewritten:\n%s", raw)
	}
	if got, err := s.ReadFile("alpha-renamed", "references/api.md"); err != nil || string(got) != "keep me" {
		t.Errorf("bundled files not carried over: %q %v", got, err)
	}

	s2, _ := Open(s.Root())
	if _, err := s2.Skill("alpha"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old name still resolves: %v", err)
	}
	if _, err := s2.Skill("alpha-renamed"); err != nil {
		t.Errorf("new name missing: %v", err)
	}
	b, err := s2.Bundle("pack")
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(b.Skills, []string{"alpha-renamed", "beta"}) {
		t.Errorf("bundle refs = %v, want [alpha-renamed beta]", b.Skills)
	}
}

func TestRenameSkillErrors(t *testing.T) {
	s := bundleFixture(t)
	if err := s.RenameSkill("alpha", "Bad Name"); err == nil {
		t.Errorf("rename to invalid name = nil, want error")
	}
	if err := s.RenameSkill("alpha", "beta"); !errors.Is(err, ErrExists) {
		t.Errorf("rename onto existing = %v, want ErrExists", err)
	}
	if err := s.RenameSkill("nope", "fresh"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rename missing skill = %v, want ErrNotFound", err)
	}
	if err := s.RenameSkill("alpha", "alpha"); err != nil {
		t.Errorf("rename to same name = %v, want nil", err)
	}
	if _, err := s.Skill("alpha"); err != nil {
		t.Errorf("skill vanished after no-op rename: %v", err)
	}
}

func TestDeleteBundleLeavesSkillsIntact(t *testing.T) {
	s := bundleFixture(t)
	if err := s.CreateBundle(&Bundle{Name: "pack", Skills: []string{"alpha", "beta"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBundle("pack"); err != nil {
		t.Fatalf("DeleteBundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), "bundles", "pack.json")); !os.IsNotExist(err) {
		t.Errorf("bundle file still on disk: %v", err)
	}
	if _, err := s.Bundle("pack"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Bundle after delete = %v, want ErrNotFound", err)
	}
	if s.BundleCount() != 0 {
		t.Errorf("BundleCount = %d, want 0", s.BundleCount())
	}
	for _, n := range []string{"alpha", "beta", "gamma"} {
		if _, err := s.Skill(n); err != nil {
			t.Errorf("skill %q was removed with the bundle: %v", n, err)
		}
	}
	if got := skillNames(s.Skills()); len(got) != 3 {
		t.Errorf("skills = %v, want 3", got)
	}
}

func TestBundlesListingAndHelpers(t *testing.T) {
	s := bundleFixture(t)
	for _, n := range []string{"zeta", "alpha-pack"} {
		if err := s.CreateBundle(&Bundle{Name: n, Skills: []string{"alpha"}}); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Bundles()
	if len(got) != 2 || got[0].Name != "alpha-pack" || got[1].Name != "zeta" {
		t.Errorf("Bundles() not sorted by name: %v", got)
	}
	if title := (&Bundle{Name: "n"}).DisplayTitle(); title != "n" {
		t.Errorf("DisplayTitle fallback = %q", title)
	}
	if title := (&Bundle{Name: "n", Title: "T"}).DisplayTitle(); title != "T" {
		t.Errorf("DisplayTitle = %q", title)
	}
}

func TestReloadSkipsMalformedBundle(t *testing.T) {
	s := bundleFixture(t)
	if err := os.WriteFile(filepath.Join(s.Root(), "bundles", "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root(), "bundles", "Bad Name.json"), []byte(`{"skills":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root(), "bundles", "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBundle(&Bundle{Name: "fine", Skills: []string{"alpha"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err != nil {
		t.Fatalf("Reload must tolerate junk in bundles/: %v", err)
	}
	if s.BundleCount() != 1 {
		t.Errorf("BundleCount = %d, want 1", s.BundleCount())
	}
}

func TestBundleWithNullSkillsNormalizes(t *testing.T) {
	s := bundleFixture(t)
	if err := os.WriteFile(filepath.Join(s.Root(), "bundles", "nulls.json"), []byte(`{"name":"nulls","skills":null}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	b, err := s.Bundle("nulls")
	if err != nil {
		t.Fatal(err)
	}
	if b.Skills == nil {
		t.Errorf("nil skills should be normalized to an empty slice")
	}
	if b.Has("alpha") {
		t.Errorf("empty bundle claims to contain alpha")
	}
}

// ---------- 8. errors ----------

func TestNotFoundErrors(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Skill("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Skill = %v, want ErrNotFound", err)
	}
	if _, err := s.Bundle("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Bundle = %v, want ErrNotFound", err)
	}
	if err := s.DeleteSkill("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteSkill = %v, want ErrNotFound", err)
	}
	if err := s.DeleteBundle("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteBundle = %v, want ErrNotFound", err)
	}
	if err := s.WriteFile("missing", "a.md", []byte("x")); !errors.Is(err, ErrNotFound) {
		t.Errorf("WriteFile on missing skill = %v, want ErrNotFound", err)
	}
	mustCreate(t, s, &Skill{Name: "present", Description: "Exists", Body: "b"})
	if _, err := s.ReadFile("present", "nope.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadFile missing file = %v, want ErrNotFound", err)
	}
}

func TestExistsErrors(t *testing.T) {
	s, _ := newStore(t)
	sk := &Skill{Name: "dupe", Description: "First", Body: "b"}
	mustCreate(t, s, sk)
	err := s.CreateSkill(&Skill{Name: "dupe", Description: "Second", Body: "b"})
	if !errors.Is(err, ErrExists) {
		t.Errorf("CreateSkill duplicate = %v, want ErrExists", err)
	}
	// The original must be untouched.
	got, _ := s.Skill("dupe")
	if got.Description != "First" {
		t.Errorf("existing skill was overwritten: %q", got.Description)
	}

	if err := s.CreateBundle(&Bundle{Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBundle(&Bundle{Name: "b1"}); !errors.Is(err, ErrExists) {
		t.Errorf("CreateBundle duplicate = %v, want ErrExists", err)
	}
}

func TestSaveSkillValidation(t *testing.T) {
	s, _ := newStore(t)
	cases := []struct {
		name string
		sk   *Skill
	}{
		{"empty description", &Skill{Name: "x1", Description: "", Body: "b"}},
		{"blank description", &Skill{Name: "x2", Description: "   \n\t ", Body: "b"}},
		{"empty name", &Skill{Name: "", Description: "d", Body: "b"}},
		{"invalid name", &Skill{Name: "Bad Name", Description: "d", Body: "b"}},
		{"traversal name", &Skill{Name: "../evil", Description: "d", Body: "b"}},
		{"long name", &Skill{Name: strings.Repeat("a", 65), Description: "d", Body: "b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := s.SaveSkill(c.sk); err == nil {
				t.Errorf("SaveSkill(%+v) = nil, want error", c.sk)
			}
			if err := s.CreateSkill(c.sk); err == nil {
				t.Errorf("CreateSkill(%+v) = nil, want error", c.sk)
			}
		})
	}
	// Nothing should have been written.
	entries, err := os.ReadDir(s.skillsDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("invalid saves left directories behind: %v", names)
	}
}

func TestSaveBundleValidation(t *testing.T) {
	s, _ := newStore(t)
	for _, name := range []string{"", "Bad Name", "../evil", strings.Repeat("a", 65)} {
		if err := s.SaveBundle(&Bundle{Name: name}); err == nil {
			t.Errorf("SaveBundle(%q) = nil, want error", name)
		}
		if err := s.DeleteBundle(name); err == nil {
			t.Errorf("DeleteBundle(%q) = nil, want error", name)
		}
	}
	entries, _ := os.ReadDir(s.bundlesDir())
	if len(entries) != 0 {
		t.Errorf("invalid bundle saves wrote %d files", len(entries))
	}
}

func TestDeleteSkillValidatesName(t *testing.T) {
	s, base := newStore(t)
	writeTree(t, base, map[string]string{"outside/keep.txt": "keep"})
	for _, name := range []string{"../outside", "..", "", "Bad Name"} {
		if err := s.DeleteSkill(name); err == nil {
			t.Errorf("DeleteSkill(%q) = nil, want error", name)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "outside", "keep.txt")); err != nil {
		t.Errorf("outside file was deleted: %v", err)
	}
}

// ---------- model helpers ----------

func TestFileRefHelpers(t *testing.T) {
	cases := []struct {
		path, ext, dir string
	}{
		{"README.md", "md", ""},
		{"references/api.MD", "md", "references"},
		{"scripts/deep/run.sh", "sh", "scripts"},
		{"noext", "", ""},
		{"a/b/c", "", "a"},
		{"archive.tar.gz", "gz", ""},
	}
	for _, c := range cases {
		f := FileRef{Path: c.path}
		if got := f.Ext(); got != c.ext {
			t.Errorf("FileRef(%q).Ext() = %q, want %q", c.path, got, c.ext)
		}
		if got := f.Dir(); got != c.dir {
			t.Errorf("FileRef(%q).Dir() = %q, want %q", c.path, got, c.dir)
		}
	}
}

func TestDescriptionFallsBackToBodySummary(t *testing.T) {
	s, _ := newStore(t)
	writeTree(t, s.skillDir("nodesc"), map[string]string{
		"SKILL.md": "---\nname: nodesc\n---\n\n# Heading\n\nThe first meaningful line.\n\nMore text.\n",
	})
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	sk, err := s.Skill("nodesc")
	if err != nil {
		t.Fatal(err)
	}
	if sk.Description != "The first meaningful line." {
		t.Errorf("description = %q, want the body summary", sk.Description)
	}
}

// ---------- 10. importer ----------

// buildSource creates a fake ~/.claude/skills directory containing a plain
// skill and a skill that is a symlink to a directory elsewhere.
func buildSource(t *testing.T, base string) (src, elsewhere string) {
	t.Helper()
	src = filepath.Join(base, "src")
	elsewhere = filepath.Join(base, "elsewhere", "linked-skill")

	writeTree(t, filepath.Join(src, "plain-skill"), map[string]string{
		"SKILL.md":          "---\nname: plain-skill\ndescription: A plain skill on disk\n---\n\nPlain body\n",
		"references/api.md": "plain reference",
	})
	writeTree(t, elsewhere, map[string]string{
		"SKILL.md":          "---\nname: linked-skill\ndescription: A symlinked skill\n---\n\nLinked body\n",
		"references/api.md": "linked reference",
	})
	if err := os.Symlink(elsewhere, filepath.Join(src, "linked-skill")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// Junk that must be ignored.
	writeTree(t, filepath.Join(src, "not-a-skill"), map[string]string{"README.md": "no SKILL.md here"})
	writeTree(t, filepath.Join(src, ".hidden-skill"), map[string]string{"SKILL.md": "---\nname: h\ndescription: d\n---\n\nb\n"})
	if err := os.WriteFile(filepath.Join(src, "loose.md"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src, elsewhere
}

func TestScanFindsPlainAndSymlinkedSkills(t *testing.T) {
	s, base := newStore(t)
	src, _ := buildSource(t, base)

	got, err := s.Scan(src)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	names := make([]string, len(got))
	for i, c := range got {
		names[i] = c.Name
	}
	if !sameStrings(names, []string{"linked-skill", "plain-skill"}) {
		t.Fatalf("Scan names = %v, want [linked-skill plain-skill]", names)
	}
	byName := map[string]ImportCandidate{}
	for _, c := range got {
		byName[c.Name] = c
	}
	if !byName["linked-skill"].Symlink {
		t.Errorf("linked-skill should be flagged Symlink")
	}
	if byName["plain-skill"].Symlink {
		t.Errorf("plain-skill should not be flagged Symlink")
	}
	if byName["plain-skill"].Description != "A plain skill on disk" {
		t.Errorf("description = %q", byName["plain-skill"].Description)
	}
	if byName["linked-skill"].Description != "A symlinked skill" {
		t.Errorf("description = %q", byName["linked-skill"].Description)
	}
	if byName["plain-skill"].Files != 2 {
		t.Errorf("plain-skill files = %d, want 2", byName["plain-skill"].Files)
	}
	if byName["plain-skill"].Exists || byName["linked-skill"].Exists {
		t.Errorf("nothing is imported yet, Exists should be false")
	}
}

func TestScanErrors(t *testing.T) {
	s, base := newStore(t)
	if _, err := s.Scan(""); err == nil {
		t.Errorf("Scan(\"\") = nil, want error")
	}
	if _, err := s.Scan("   "); err == nil {
		t.Errorf("Scan(whitespace) = nil, want error")
	}
	if _, err := s.Scan(filepath.Join(base, "does-not-exist")); err == nil {
		t.Errorf("Scan(missing dir) = nil, want error")
	}
}

func TestScanSlugifiesNamesAndFlagsExisting(t *testing.T) {
	s, base := newStore(t)
	src := filepath.Join(base, "src")
	writeTree(t, filepath.Join(src, "My Weird Skill"), map[string]string{
		"SKILL.md": "---\nname: whatever\ndescription: Needs slugifying\n---\n\nBody\n",
	})
	mustCreate(t, s, &Skill{Name: "my-weird-skill", Description: "Already here", Body: "b"})

	got, err := s.Scan(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Scan = %v", got)
	}
	if got[0].Name != "my-weird-skill" {
		t.Errorf("name = %q, want my-weird-skill", got[0].Name)
	}
	if !got[0].Exists {
		t.Errorf("Exists should be true for an already-imported skill")
	}
}

func TestImportCopiesByValue(t *testing.T) {
	s, base := newStore(t)
	src := filepath.Join(base, "src")
	target := filepath.Join(base, "target")
	writeTree(t, target, map[string]string{"shared.md": "shared content"})
	writeTree(t, filepath.Join(src, "plain-skill"), map[string]string{
		"SKILL.md":          "---\nname: plain-skill\ndescription: A plain skill on disk\n---\n\nPlain body\n",
		"references/api.md": "plain reference",
	})
	// A symlinked *file* inside a normal skill directory must be copied by value.
	if err := os.Symlink(filepath.Join(target, "shared.md"), filepath.Join(src, "plain-skill", "linked.md")); err != nil {
		t.Fatal(err)
	}

	res, err := s.Import(src, nil, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !sameStrings(res.Imported, []string{"plain-skill"}) {
		t.Fatalf("imported = %v (failed: %v)", res.Imported, res.Failed)
	}

	dst := s.skillDir("plain-skill")
	for _, rel := range []string{"SKILL.md", "references/api.md", "linked.md"} {
		info, err := os.Lstat(filepath.Join(dst, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s was copied as a symlink, want a real file", rel)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "linked.md")); string(got) != "shared content" {
		t.Errorf("symlinked file content = %q", got)
	}
	// Mutating the source must not affect the imported copy.
	if err := os.WriteFile(filepath.Join(target, "shared.md"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReadFile("plain-skill", "linked.md"); string(got) != "shared content" {
		t.Errorf("import is not by value: %q", got)
	}

	sk, err := s.Skill("plain-skill")
	if err != nil {
		t.Fatalf("imported skill not indexed: %v", err)
	}
	if sk.Description != "A plain skill on disk" {
		t.Errorf("description = %q", sk.Description)
	}
}

func TestImportSkipsJunkAndHiddenFiles(t *testing.T) {
	s, base := newStore(t)
	src := filepath.Join(base, "src")
	writeTree(t, filepath.Join(src, "junky"), map[string]string{
		"SKILL.md":            "---\nname: junky\ndescription: Has junk alongside\n---\n\nBody\n",
		"keep.md":             "keep",
		".DS_Store":           "junk",
		".git/config":         "junk",
		"node_modules/dep.js": "junk",
		".hidden/inner.txt":   "junk",
	})
	if _, err := s.Import(src, nil, false); err != nil {
		t.Fatal(err)
	}
	dst := s.skillDir("junky")
	for _, rel := range []string{".DS_Store", ".git", "node_modules", ".hidden"} {
		if _, err := os.Lstat(filepath.Join(dst, rel)); !os.IsNotExist(err) {
			t.Errorf("%s should not have been imported (err=%v)", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "keep.md")); err != nil {
		t.Errorf("keep.md missing: %v", err)
	}
}

func TestImportOverwriteBehaviour(t *testing.T) {
	s, base := newStore(t)
	src := filepath.Join(base, "src")
	writeTree(t, filepath.Join(src, "dupe"), map[string]string{
		"SKILL.md": "---\nname: dupe\ndescription: From the import source\n---\n\nImported body\n",
		"new.md":   "from source",
	})
	mustCreate(t, s, &Skill{Name: "dupe", Description: "Already in the store", Body: "Local body"})
	if err := s.WriteFile("dupe", "local-only.md", []byte("local")); err != nil {
		t.Fatal(err)
	}

	res, err := s.Import(src, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(res.Skipped, []string{"dupe"}) || len(res.Imported) != 0 {
		t.Errorf("overwrite=false: imported=%v skipped=%v failed=%v", res.Imported, res.Skipped, res.Failed)
	}
	sk, _ := s.Skill("dupe")
	if sk.Description != "Already in the store" {
		t.Errorf("existing skill was replaced: %q", sk.Description)
	}

	res, err = s.Import(src, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(res.Imported, []string{"dupe"}) || len(res.Skipped) != 0 {
		t.Errorf("overwrite=true: imported=%v skipped=%v failed=%v", res.Imported, res.Skipped, res.Failed)
	}
	sk, _ = s.Skill("dupe")
	if sk.Description != "From the import source" {
		t.Errorf("description after overwrite = %q", sk.Description)
	}
	if _, err := os.Stat(filepath.Join(s.skillDir("dupe"), "local-only.md")); !os.IsNotExist(err) {
		t.Errorf("overwrite must replace the directory, local-only.md survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.skillDir("dupe"), "new.md")); err != nil {
		t.Errorf("new.md not imported: %v", err)
	}
}

func TestImportSubsetByName(t *testing.T) {
	s, base := newStore(t)
	src := filepath.Join(base, "src")
	for _, n := range []string{"one", "two", "three"} {
		writeTree(t, filepath.Join(src, n), map[string]string{
			"SKILL.md": "---\nname: " + n + "\ndescription: Skill " + n + "\n---\n\nBody\n",
		})
	}
	res, err := s.Import(src, []string{"two"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(res.Imported, []string{"two"}) {
		t.Errorf("imported = %v, want [two]", res.Imported)
	}
	if got := skillNames(s.Skills()); !sameStrings(got, []string{"two"}) {
		t.Errorf("store now has %v, want [two]", got)
	}

	// Unknown names simply select nothing.
	res, err = s.Import(src, []string{"nonexistent"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Imported)+len(res.Skipped)+len(res.Failed) != 0 {
		t.Errorf("selecting an unknown name did something: %+v", res)
	}
}

func TestImportResultIsNeverNil(t *testing.T) {
	s, base := newStore(t)
	src := filepath.Join(base, "empty-src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := s.Import(src, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported == nil || res.Skipped == nil || res.Failed == nil {
		t.Errorf("ImportResult slices must be non-nil for JSON: %+v", res)
	}
}

// TestImportSymlinkedSkillDirectory is a regression test: a skill directory that
// is itself a symlink (the common case for plugin-provided skills) must be
// imported by value, not skipped. filepath.WalkDir refuses to descend into a
// symlinked root, which used to make these imports produce an empty directory.
func TestImportSymlinkedSkillDirectory(t *testing.T) {
	s, base := newStore(t)
	src, elsewhere := buildSource(t, base)

	res, err := s.Import(src, nil, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(res.Failed) != 0 {
		t.Errorf("import failed: %v", res.Failed)
	}
	if !sameStrings(res.Imported, []string{"linked-skill", "plain-skill"}) {
		t.Errorf("imported = %v, want [linked-skill plain-skill]", res.Imported)
	}
	if got, err := s.ReadFile("linked-skill", "references/api.md"); err != nil || string(got) != "linked reference" {
		t.Errorf("symlinked skill content was not copied: %q %v", got, err)
	}
	sk, err := s.Skill("linked-skill")
	if err != nil {
		t.Fatalf("symlinked skill not indexed: %v", err)
	}
	if sk.Description != "A symlinked skill" {
		t.Errorf("description = %q", sk.Description)
	}
	// Copied by value: nothing in the store may still be a link, and edits to the
	// original must not leak through.
	for _, rel := range []string{"SKILL.md", "references/api.md"} {
		info, err := os.Lstat(filepath.Join(s.skillDir("linked-skill"), filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s is still a symlink", rel)
		}
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "references", "api.md"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReadFile("linked-skill", "references/api.md"); string(got) != "linked reference" {
		t.Errorf("import is not by value: %q", got)
	}
}

func TestImportSurvivesSymlinkCycles(t *testing.T) {
	s, base := newStore(t)
	src := filepath.Join(base, "src")
	dir := filepath.Join(src, "cyclic")
	writeTree(t, dir, map[string]string{
		"SKILL.md":          "---\nname: cyclic\ndescription: Contains a symlink loop\n---\n\nBody\n",
		"references/api.md": "reference",
	})
	// A directory that links back to its own ancestor.
	if err := os.Symlink(dir, filepath.Join(dir, "references", "loop")); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := s.Scan(src); err != nil {
			t.Errorf("Scan: %v", err)
		}
		if res, err := s.Import(src, nil, false); err != nil {
			t.Errorf("Import: %v", err)
		} else if !sameStrings(res.Imported, []string{"cyclic"}) {
			t.Errorf("imported = %v failed = %v", res.Imported, res.Failed)
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Scan/Import did not terminate on a symlink cycle")
	}
	if got, err := s.ReadFile("cyclic", "references/api.md"); err != nil || string(got) != "reference" {
		t.Errorf("content = %q err = %v", got, err)
	}
}

// TestSymlinkedSkillDirectoryIndexing is a regression test for the same
// WalkDir-on-a-symlinked-root problem inside the store: Reload explicitly
// supports symlinked skill directories, so their file index must be real.
func TestSymlinkedSkillDirectoryIndexing(t *testing.T) {
	s, base := newStore(t)
	real := filepath.Join(base, "real-skill")
	writeTree(t, real, map[string]string{
		"SKILL.md":          "---\nname: linked\ndescription: A symlinked skill directory\n---\n\nBody\n",
		"references/api.md": "reference",
	})
	if err := os.Symlink(real, s.skillDir("linked")); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	sk, err := s.Skill("linked")
	if err != nil {
		t.Fatalf("symlinked skill should load: %v", err)
	}
	if got := filePaths(sk.Files); !sameStrings(got, []string{"references/api.md"}) {
		t.Errorf("files for a symlinked skill dir = %v, want [references/api.md]", got)
	}
	if sk.Size == 0 {
		t.Errorf("size = 0 for a symlinked skill dir")
	}
	if got, err := s.ReadFile("linked", "references/api.md"); err != nil || string(got) != "reference" {
		t.Errorf("ReadFile inside a symlinked skill dir = %q, %v", got, err)
	}
}

// ---------- aliasing ----------

// TestBug_MutationOfCallerOwnedState documents that SaveSkill/RenameSkill mutate
// state the caller (and the in-memory index) still holds, which is also the
// source of a genuine data race between RenameSkill and concurrent readers.
func TestBug_MutationOfCallerOwnedState(t *testing.T) {
	s, _ := newStore(t)

	t.Run("SaveSkill mutates the caller's metadata map", func(t *testing.T) {
		meta := map[string]any{"author": "someone"}
		sk := &Skill{
			Name:        "aliased",
			Description: "Metadata aliasing",
			Body:        "Body",
			Tags:        []string{"one"},
			Front:       skillfile.Frontmatter{Metadata: meta},
		}
		mustCreate(t, s, sk)
		if _, ok := meta["tags"]; ok {
			t.Errorf("BUG: SaveSkill wrote into the caller's Front.Metadata map; `front := sk.Front` "+
				"copies the struct but shares the map, so the caller's map is now %v. "+
				"Fix: clone the metadata map before mutating it.", meta)
		}
	})

	t.Run("RenameSkill mutates a previously returned *Skill", func(t *testing.T) {
		mustCreate(t, s, &Skill{Name: "before", Description: "Rename me", Body: "Body"})
		held, err := s.Skill("before") // e.g. an in-flight HTTP handler
		if err != nil {
			t.Fatal(err)
		}
		if err := s.RenameSkill("before", "after"); err != nil {
			t.Fatal(err)
		}
		if held.Name != "before" {
			t.Errorf("BUG: RenameSkill mutates the *Skill it hands out: a value previously returned "+
				"by Skill() changed name to %q underneath its holder. Under concurrency this is a "+
				"genuine data race — RenameSkill writes sk.Name without the write lock while readers "+
				"read the same pointer after releasing the read lock. Fix: rename a copy.", held.Name)
		}
	})
}

// ---------- 11. concurrency ----------

func TestConcurrentSaveAndRead(t *testing.T) {
	s, _ := newStore(t)
	const skills = 6
	for i := 0; i < skills; i++ {
		mustCreate(t, s, &Skill{
			Name:        fmt.Sprintf("skill-%d", i),
			Description: fmt.Sprintf("Concurrent skill %d", i),
			Body:        "Body text with the word searchable in it.",
			Tags:        []string{"concurrent"},
		})
	}
	if err := s.CreateBundle(&Bundle{Name: "pack", Skills: []string{"skill-0", "skill-1"}}); err != nil {
		t.Fatal(err)
	}

	const iterations = 40
	var wg sync.WaitGroup

	for i := 0; i < skills; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("skill-%d", i)
			for n := 0; n < iterations; n++ {
				sk := &Skill{
					Name:        name,
					Description: fmt.Sprintf("Concurrent skill %d rev %d", i, n),
					Body:        "Body text with the word searchable in it.",
					Tags:        []string{"concurrent", fmt.Sprintf("rev-%d", n%3)},
				}
				if err := s.SaveSkill(sk); err != nil {
					t.Errorf("SaveSkill: %v", err)
					return
				}
			}
		}(i)
	}

	for i := 0; i < skills; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < iterations; n++ {
				for _, sk := range s.Skills() {
					_ = sk.Name
					_ = sk.Description
					_ = sk.Body
					_ = len(sk.Files)
					_ = sk.TagsCSV()
				}
				_ = s.Search("searchable", "")
				_ = s.Search("", "concurrent")
				_ = s.Tags()
				_ = s.SkillCount()
				for _, b := range s.Bundles() {
					_ = b.Has("skill-0")
					_ = b.DisplayTitle()
				}
				if _, err := s.Skill("skill-0"); err != nil {
					t.Errorf("Skill: %v", err)
					return
				}
			}
		}()
	}

	// Bundle writers, each publishing a fresh value under the same name.
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < iterations; n++ {
				b := &Bundle{Name: fmt.Sprintf("pack-%d", i), Skills: []string{"skill-0", "skill-1", "skill-2"}}
				if err := s.SaveBundle(b); err != nil {
					t.Errorf("SaveBundle: %v", err)
					return
				}
			}
		}(i)
	}

	// Concurrent bundled-file writes into a dedicated skill.
	mustCreate(t, s, &Skill{Name: "filesink", Description: "Concurrent file writes", Body: "Body"})
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rel := fmt.Sprintf("references/file-%d.md", i)
			for n := 0; n < iterations; n++ {
				if err := s.WriteFile("filesink", rel, []byte(fmt.Sprintf("rev %d", n))); err != nil {
					t.Errorf("WriteFile: %v", err)
					return
				}
				if _, err := s.ReadFile("filesink", rel); err != nil {
					t.Errorf("ReadFile: %v", err)
					return
				}
			}
		}(i)
	}

	wg.Wait()

	if got := s.SkillCount(); got != skills+1 {
		t.Errorf("SkillCount = %d, want %d", got, skills+1)
	}
	// Every SKILL.md must still be parseable — atomic writes must never leave a
	// truncated file or a stray temp file in the index.
	s2, err := Open(s.Root())
	if err != nil {
		t.Fatalf("reopen after concurrency: %v", err)
	}
	if got := s2.SkillCount(); got != skills+1 {
		t.Errorf("SkillCount after reopen = %d, want %d", got, skills+1)
	}
	for _, sk := range s2.Skills() {
		if sk.Description == "" {
			t.Errorf("skill %q lost its description", sk.Name)
		}
	}
	entries, err := os.ReadDir(s.skillDir("skill-0"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("atomic write left a temp file behind: %s", e.Name())
		}
	}
}

func TestWriteFileAtomicReplacesContent(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "f.txt")
	if err := writeFileAtomic(dst, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(dst, []byte("two")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "two" {
		t.Errorf("content = %q", got)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %d entries", len(entries))
	}
}
