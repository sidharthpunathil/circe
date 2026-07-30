package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func TestContextCreatesFile(t *testing.T) {
	root := t.TempDir()
	res, err := WriteContextFile(root, "design", "CLAUDE.md", "Use tabs.", ContextMerge, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "created" {
		t.Errorf("action = %q", res.Action)
	}
	got := read(t, filepath.Join(root, "CLAUDE.md"))
	if !strings.Contains(got, "Use tabs.") {
		t.Errorf("content missing:\n%s", got)
	}
	if !strings.Contains(got, "circe:start design") {
		t.Errorf("no start marker:\n%s", got)
	}
	if !strings.Contains(got, "circe:end design") {
		t.Errorf("no end marker:\n%s", got)
	}
}

// The user's own content must survive a merge untouched. That is the whole
// reason for the marker approach.
func TestContextPreservesUserContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "CLAUDE.md")
	original := "# My project\n\nAlways run the linter.\n"
	os.WriteFile(path, []byte(original), 0o644)

	if _, err := WriteContextFile(root, "design", "CLAUDE.md", "Use tabs.", ContextMerge, false); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, "Always run the linter.") {
		t.Errorf("user content lost:\n%s", got)
	}
	if !strings.Contains(got, "Use tabs.") {
		t.Errorf("folder content missing:\n%s", got)
	}
	if strings.Index(got, "Always run the linter.") > strings.Index(got, "Use tabs.") {
		t.Errorf("folder block should be appended after user content:\n%s", got)
	}
}

func TestContextIsIdempotent(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 4; i++ {
		if _, err := WriteContextFile(root, "design", "CLAUDE.md", "Use tabs.", ContextMerge, false); err != nil {
			t.Fatal(err)
		}
	}
	got := read(t, filepath.Join(root, "CLAUDE.md"))
	if n := strings.Count(got, "circe:start design"); n != 1 {
		t.Errorf("block written %d times, want 1:\n%s", n, got)
	}
	if n := strings.Count(got, "Use tabs."); n != 1 {
		t.Errorf("content appears %d times, want 1", n)
	}
}

func TestContextSecondInstallReportsUnchanged(t *testing.T) {
	root := t.TempDir()
	WriteContextFile(root, "design", "CLAUDE.md", "Use tabs.", ContextMerge, false)
	res, err := WriteContextFile(root, "design", "CLAUDE.md", "Use tabs.", ContextMerge, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "unchanged" {
		t.Errorf("action = %q, want unchanged", res.Action)
	}
}

func TestContextUpdatesInPlace(t *testing.T) {
	root := t.TempDir()
	WriteContextFile(root, "design", "CLAUDE.md", "Old rule.", ContextMerge, false)
	WriteContextFile(root, "design", "CLAUDE.md", "New rule.", ContextMerge, false)
	got := read(t, filepath.Join(root, "CLAUDE.md"))
	if strings.Contains(got, "Old rule.") {
		t.Errorf("stale content kept:\n%s", got)
	}
	if !strings.Contains(got, "New rule.") {
		t.Errorf("new content missing:\n%s", got)
	}
}

func TestContextMultipleFoldersCoexist(t *testing.T) {
	root := t.TempDir()
	WriteContextFile(root, "design", "CLAUDE.md", "Design rules.", ContextMerge, false)
	WriteContextFile(root, "backend", "CLAUDE.md", "Backend rules.", ContextMerge, false)
	got := read(t, filepath.Join(root, "CLAUDE.md"))
	for _, want := range []string{"Design rules.", "Backend rules.", "start design", "start backend"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}

	// Updating one folder must not disturb the other.
	WriteContextFile(root, "design", "CLAUDE.md", "Design rules v2.", ContextMerge, false)
	got = read(t, filepath.Join(root, "CLAUDE.md"))
	if !strings.Contains(got, "Backend rules.") {
		t.Errorf("sibling block damaged:\n%s", got)
	}
	if strings.Contains(got, "Design rules.\n") && !strings.Contains(got, "Design rules v2.") {
		t.Errorf("target block not updated:\n%s", got)
	}
}

func TestContextSkipLeavesFileAlone(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "CLAUDE.md")
	os.WriteFile(path, []byte("mine\n"), 0o644)
	res, _ := WriteContextFile(root, "design", "CLAUDE.md", "theirs", ContextSkip, false)
	if res.Action != "skipped" {
		t.Errorf("action = %q", res.Action)
	}
	if read(t, path) != "mine\n" {
		t.Errorf("file was modified")
	}
}

func TestContextOverwriteReplacesEverything(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "CLAUDE.md")
	os.WriteFile(path, []byte("# Mine\n\nkeep me\n"), 0o644)
	res, _ := WriteContextFile(root, "design", "CLAUDE.md", "theirs", ContextOverwrite, false)
	if res.Action != "overwritten" {
		t.Errorf("action = %q", res.Action)
	}
	got := read(t, path)
	if strings.Contains(got, "keep me") {
		t.Errorf("overwrite should have dropped user content:\n%s", got)
	}
}

func TestContextDryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	if _, err := WriteContextFile(root, "design", "CLAUDE.md", "x", ContextMerge, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("dry run created a file")
	}
}

func TestRemoveContextBlock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "CLAUDE.md")
	os.WriteFile(path, []byte("# Mine\n\nkeep me\n"), 0o644)
	WriteContextFile(root, "design", "CLAUDE.md", "theirs", ContextMerge, false)

	removed, err := RemoveContextBlock(root, "design", "CLAUDE.md")
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	got := read(t, path)
	if strings.Contains(got, "theirs") || strings.Contains(got, "circe:start") {
		t.Errorf("block not removed:\n%s", got)
	}
	if !strings.Contains(got, "keep me") {
		t.Errorf("user content lost on removal:\n%s", got)
	}
}

// A file that contained only our block should disappear entirely, rather than
// leaving an empty CLAUDE.md that confuses the agent.
func TestRemoveContextBlockDeletesFileWeCreated(t *testing.T) {
	root := t.TempDir()
	WriteContextFile(root, "design", "CLAUDE.md", "theirs", ContextMerge, false)
	if _, err := RemoveContextBlock(root, "design", "CLAUDE.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("file should have been removed")
	}
}

func TestRemoveContextBlockNoOpWhenAbsent(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("mine\n"), 0o644)
	removed, err := RemoveContextBlock(root, "design", "CLAUDE.md")
	if err != nil || removed {
		t.Errorf("removed=%v err=%v", removed, err)
	}
}

func TestContextDoesNotAccumulateBlankLines(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		WriteContextFile(root, "a", "CLAUDE.md", "one", ContextMerge, false)
		WriteContextFile(root, "b", "CLAUDE.md", "two", ContextMerge, false)
	}
	got := read(t, filepath.Join(root, "CLAUDE.md"))
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("blank lines accumulated:\n%q", got)
	}
}
