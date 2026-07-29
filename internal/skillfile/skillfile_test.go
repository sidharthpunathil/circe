package skillfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBasic(t *testing.T) {
	src := []byte("---\nname: caveman\ndescription: Terse mode\n---\n\nBody here.\n")
	f, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "caveman" {
		t.Errorf("name = %q", f.Name)
	}
	if f.Description != "Terse mode" {
		t.Errorf("description = %q", f.Description)
	}
	if f.Body != "Body here.\n" {
		t.Errorf("body = %q", f.Body)
	}
}

func TestParseNoFrontmatter(t *testing.T) {
	f, err := Parse([]byte("# Just markdown\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "" || f.Body != "# Just markdown\n" {
		t.Errorf("got name=%q body=%q", f.Name, f.Body)
	}
}

func TestPreservesUnknownKeys(t *testing.T) {
	src := []byte("---\nname: x\ndescription: y\ncustom-key: hello\ntags:\n  - a\n  - b\n---\n\nBody\n")
	f, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if f.Extra["custom-key"] != "hello" {
		t.Errorf("extra custom-key = %v", f.Extra["custom-key"])
	}
	out := string(f.Bytes())
	if !strings.Contains(out, "custom-key: hello") {
		t.Errorf("unknown key dropped on serialize:\n%s", out)
	}
	if !strings.Contains(out, "- a") {
		t.Errorf("unknown list key dropped:\n%s", out)
	}
}

func TestRoundTripStable(t *testing.T) {
	src := []byte("---\nname: x\ndescription: y\nallowed-tools:\n  - Read\n  - Bash\n---\n\n# Head\n\nText\n")
	f1, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := Parse(f1.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if f2.Name != f1.Name || f2.Description != f1.Description || f2.Body != f1.Body {
		t.Errorf("round trip changed content:\n1=%#v\n2=%#v", f1, f2)
	}
	if len(f2.AllowedTools.Items) != 2 || f2.AllowedTools.Items[0] != "Read" {
		t.Errorf("allowed-tools = %v", f2.AllowedTools.Items)
	}
	// A second serialization must be byte-identical to the first.
	if string(f1.Bytes()) != string(f2.Bytes()) {
		t.Errorf("serialization not idempotent:\n%s\n---\n%s", f1.Bytes(), f2.Bytes())
	}
}

// TestAgainstRealSkills parses every SKILL.md in the user's skill library. This
// is the real compatibility bar: the parser must handle multi-line folded
// descriptions, unusual keys and odd spacing that appear in the wild.
func TestAgainstRealSkills(t *testing.T) {
	root := filepath.Join(os.Getenv("HOME"), ".claude", "skills")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Skipf("no skill library at %s", root)
	}

	var parsed, noName, withExtra int
	extraKeys := map[string]int{}

	for _, e := range entries {
		// Nearly half the library is symlinks into plugin directories, so
		// resolve rather than trusting DirEntry.IsDir.
		info, err := os.Stat(filepath.Join(root, e.Name()))
		if err != nil || !info.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name(), "SKILL.md")
		src, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		f, err := Parse(src)
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		parsed++
		if f.Name == "" {
			noName++
			t.Logf("%s: no name in frontmatter", e.Name())
		}
		if len(f.Extra) > 0 {
			withExtra++
			for k := range f.Extra {
				extraKeys[k]++
			}
		}
		// Re-parsing our own output must yield identical values.
		f2, err := Parse(f.Bytes())
		if err != nil {
			t.Errorf("%s: reparse failed: %v", e.Name(), err)
			continue
		}
		if f2.Name != f.Name {
			t.Errorf("%s: name changed %q -> %q", e.Name(), f.Name, f2.Name)
		}
		if strings.TrimSpace(f2.Description) != strings.TrimSpace(f.Description) {
			t.Errorf("%s: description changed", e.Name())
		}
		if strings.TrimSpace(f2.Body) != strings.TrimSpace(f.Body) {
			t.Errorf("%s: body changed", e.Name())
		}
	}

	t.Logf("parsed %d skills, %d without name, %d with extra keys", parsed, noName, withExtra)
	for k, n := range extraKeys {
		t.Logf("  extra key %q in %d skills", k, n)
	}
	if parsed == 0 {
		t.Fatal("parsed no skills")
	}
}
