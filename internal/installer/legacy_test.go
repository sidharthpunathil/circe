package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tool was called skillbuilder before it was called circe. These tests pin
// the compatibility behaviour, because the artefacts involved live in other
// people's repositories and home directories: getting this wrong duplicates
// blocks in a project's CLAUDE.md, orphans them so no command can remove them,
// or makes an installed project look empty.

func TestLegacyContextBlockIsReplacedNotAppended(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "CLAUDE.md")
	existing := "# Project\n\nSome notes the author wrote.\n\n" +
		"<!-- skillbuilder:start design -->\nold guidance\n<!-- skillbuilder:end design -->\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := WriteContextFile(root, "design", "CLAUDE.md", "new guidance", ContextMerge, false); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(got)

	if strings.Count(body, ":start design") != 1 {
		t.Errorf("expected exactly one block, got:\n%s", body)
	}
	if strings.Contains(body, "old guidance") {
		t.Errorf("old content survived:\n%s", body)
	}
	if !strings.Contains(body, "new guidance") {
		t.Errorf("new content missing:\n%s", body)
	}
	if !strings.Contains(body, "Some notes the author wrote.") {
		t.Errorf("the author's own prose was destroyed:\n%s", body)
	}
}

func TestLegacyContextBlockCanBeRemoved(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "CLAUDE.md")
	existing := "# Project\n\nKeep me.\n\n" +
		"<!-- skillbuilder:start design -->\nmanaged\n<!-- skillbuilder:end design -->\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveContextBlock(root, "design", "CLAUDE.md")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed {
		t.Fatal("a block written before the rename could not be removed")
	}

	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "managed") {
		t.Errorf("block survived removal:\n%s", got)
	}
	if !strings.Contains(string(got), "Keep me.") {
		t.Errorf("surrounding content was lost:\n%s", got)
	}
}

// Two adjacent blocks, one in each spelling, is the case that a single combined
// pattern would corrupt: a non-greedy match could run from the old opening
// marker to the new closing one and swallow everything between them.
func TestAdjacentOldAndNewBlocksDoNotBleed(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "CLAUDE.md")
	existing := "<!-- skillbuilder:start alpha -->\nalpha body\n<!-- skillbuilder:end alpha -->\n\n" +
		"between the blocks\n\n" +
		"<!-- circe:start beta -->\nbeta body\n<!-- circe:end beta -->\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := RemoveContextBlock(root, "alpha", "CLAUDE.md"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	got, _ := os.ReadFile(path)
	body := string(got)
	if strings.Contains(body, "alpha body") {
		t.Errorf("alpha block was not removed:\n%s", body)
	}
	if !strings.Contains(body, "between the blocks") {
		t.Errorf("content between the two blocks was eaten:\n%s", body)
	}
	if !strings.Contains(body, "beta body") {
		t.Errorf("the unrelated beta block was destroyed:\n%s", body)
	}
}

func TestLegacyLockfileIsReadAndUpgraded(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "skillbuilder.json")
	body := `{"version":1,"registry":"https://example.com","skills":{"tidy":{"version":"1"}}}`
	if err := os.WriteFile(legacy, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	lf, err := LoadLockfile(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if lf.Registry != "https://example.com" {
		t.Errorf("registry = %q, want the value from the old lockfile", lf.Registry)
	}
	if _, ok := lf.Skills["tidy"]; !ok {
		t.Errorf("a project with an old lockfile looks empty: %+v", lf.Skills)
	}

	if err := SaveLockfile(root, lf); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, LockfileName)); err != nil {
		t.Errorf("new lockfile was not written: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("the old lockfile is still there, so the project now has two that can disagree")
	}
}

func TestNewLockfileWinsOverLegacy(t *testing.T) {
	root := t.TempDir()
	write := func(name, registry string) {
		body := `{"version":1,"registry":"` + registry + `","skills":{}}`
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("skillbuilder.json", "https://old.example.com")
	write(LockfileName, "https://new.example.com")

	lf, err := LoadLockfile(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if lf.Registry != "https://new.example.com" {
		t.Errorf("registry = %q, want the current lockfile to win", lf.Registry)
	}
}

func TestLegacyEnvVarsStillResolve(t *testing.T) {
	t.Setenv("CIRCE_TOKEN", "")
	t.Setenv("SKILLBUILDER_TOKEN", "legacy-token")
	if got := ResolveToken(&Config{}); got != "legacy-token" {
		t.Errorf("ResolveToken = %q, want the pre-rename variable to be honoured", got)
	}

	t.Setenv("CIRCE_TOKEN", "current-token")
	if got := ResolveToken(&Config{}); got != "current-token" {
		t.Errorf("ResolveToken = %q, want the current variable to win", got)
	}

	t.Setenv("CIRCE_REGISTRY", "")
	t.Setenv("SKILLBUILDER_REGISTRY", "https://legacy.example.com")
	reg, src := ResolveRegistry("", &Config{})
	if src != SourceEnv || !strings.Contains(reg, "legacy.example.com") {
		t.Errorf("ResolveRegistry = %q (%s), want the pre-rename variable to be honoured", reg, src)
	}
}
