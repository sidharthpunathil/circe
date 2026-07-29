package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tarEntry struct {
	name     string
	typeflag byte
	body     string
	link     string
	mode     int64
}

func file(name, body string) tarEntry {
	return tarEntry{name: name, typeflag: tar.TypeReg, body: body, mode: 0o644}
}

func makeTarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Mode:     mode,
			Size:     int64(len(e.body)),
			Linkname: e.link,
		}
		if e.typeflag != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", e.name, err)
		}
		if hdr.Size > 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write body %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func TestExtractTarGz(t *testing.T) {
	dest := t.TempDir()
	archive := makeTarGz(t,
		tarEntry{name: "alpha/", typeflag: tar.TypeDir, mode: 0o755},
		file("alpha/SKILL.md", "# alpha\n"),
		file("alpha/references/notes.md", "notes\n"),
		tarEntry{name: "beta/run.sh", typeflag: tar.TypeReg, body: "#!/bin/sh\n", mode: 0o755},
		file("./gamma/SKILL.md", "# gamma\n"), // GNU tar writes "./" prefixes
	)

	names, err := ExtractTarGz(bytes.NewReader(archive), dest)
	if err != nil {
		t.Fatalf("ExtractTarGz: %v", err)
	}
	want := []string{"alpha", "beta", "gamma"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("top-level names = %v, want %v", names, want)
	}

	got, err := os.ReadFile(filepath.Join(dest, "alpha", "references", "notes.md"))
	if err != nil {
		t.Fatalf("read nested file: %v", err)
	}
	if string(got) != "notes\n" {
		t.Errorf("nested file = %q, want %q", got, "notes\n")
	}

	info, err := os.Stat(filepath.Join(dest, "beta", "run.sh"))
	if err != nil {
		t.Fatalf("stat executable: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("executable mode = %v, want 0755", info.Mode().Perm())
	}
	info, err = os.Stat(filepath.Join(dest, "alpha", "SKILL.md"))
	if err != nil {
		t.Fatalf("stat regular file: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("regular file mode = %v, want 0644", info.Mode().Perm())
	}
}

// TestExtractTarGzRejectsUnsafeEntries is the important one: every shape of
// Zip-Slip must fail loudly and write nothing outside the destination.
func TestExtractTarGzRejectsUnsafeEntries(t *testing.T) {
	cases := []struct {
		name    string
		entries []tarEntry
	}{
		{
			name:    "parent traversal",
			entries: []tarEntry{file("../evil.md", "pwned")},
		},
		{
			name:    "traversal nested in a skill path",
			entries: []tarEntry{file("good/../../evil.md", "pwned")},
		},
		{
			name:    "traversal as an inner segment",
			entries: []tarEntry{file("good/../evil/SKILL.md", "pwned")},
		},
		{
			name:    "absolute path",
			entries: []tarEntry{file("/etc/passwd", "pwned")},
		},
		{
			name:    "absolute path with traversal",
			entries: []tarEntry{file("/tmp/../etc/passwd", "pwned")},
		},
		{
			name:    "windows drive letter",
			entries: []tarEntry{file(`C:/Windows/system32/evil.md`, "pwned")},
		},
		{
			name:    "backslash separator",
			entries: []tarEntry{file(`..\evil.md`, "pwned")},
		},
		{
			name:    "symlink",
			entries: []tarEntry{{name: "evil/link", typeflag: tar.TypeSymlink, link: "/etc/passwd"}},
		},
		{
			name:    "symlink escaping the destination",
			entries: []tarEntry{{name: "evil/link", typeflag: tar.TypeSymlink, link: "../../.."}},
		},
		{
			name:    "hard link",
			entries: []tarEntry{{name: "evil/link", typeflag: tar.TypeLink, link: "/etc/passwd"}},
		},
		{
			name:    "directory traversal entry",
			entries: []tarEntry{{name: "../evil/", typeflag: tar.TypeDir, mode: 0o755}},
		},
		{
			name:    "file outside any skill directory",
			entries: []tarEntry{file("SKILL.md", "no skill dir")},
		},
		{
			name:    "fifo",
			entries: []tarEntry{{name: "evil/pipe", typeflag: tar.TypeFifo}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A sentinel outside the destination proves nothing escaped.
			outer := t.TempDir()
			dest := filepath.Join(outer, "skills")
			sentinel := filepath.Join(outer, "evil.md")

			archive := makeTarGz(t, tc.entries...)
			_, err := ExtractTarGz(bytes.NewReader(archive), dest)
			if err == nil {
				t.Fatalf("ExtractTarGz accepted an unsafe archive")
			}
			var unsafe *UnsafeArchiveError
			if !errors.As(err, &unsafe) {
				t.Fatalf("error = %v (%T), want *UnsafeArchiveError", err, err)
			}
			if _, err := os.Lstat(sentinel); err == nil {
				t.Fatalf("archive wrote %s outside the destination", sentinel)
			}
			// Nothing anywhere in the destination either.
			var written []string
			_ = filepath.Walk(dest, func(p string, _ os.FileInfo, err error) error {
				if err == nil && p != dest {
					written = append(written, p)
				}
				return nil
			})
			for _, p := range written {
				if info, err := os.Lstat(p); err == nil && !info.IsDir() {
					t.Errorf("unsafe archive left %s behind", p)
				}
			}
		})
	}
}

func TestExtractTarGzRejectsNonGzip(t *testing.T) {
	if _, err := ExtractTarGz(strings.NewReader("not a gzip stream"), t.TempDir()); err == nil {
		t.Fatal("expected an error for a non-gzip stream")
	}
}

func TestEntryPath(t *testing.T) {
	ok := map[string]string{
		"alpha/SKILL.md":   "alpha/SKILL.md",
		"./alpha/SKILL.md": "alpha/SKILL.md",
		"alpha/":           "alpha",
		"alpha/a/b/c.txt":  "alpha/a/b/c.txt",
		"./":               "",
		".":                "",
	}
	for in, want := range ok {
		got, err := entryPath(in)
		if err != nil {
			t.Errorf("entryPath(%q) = error %v, want %q", in, err, want)
			continue
		}
		if got != want {
			t.Errorf("entryPath(%q) = %q, want %q", in, got, want)
		}
	}

	bad := []string{"", "..", "../x", "a/../b", "/a", "a//b", `a\b`, "C:/a", "a/./b"}
	for _, in := range bad {
		if got, err := entryPath(in); err == nil {
			t.Errorf("entryPath(%q) = %q, want an error", in, got)
		}
	}
}

func TestWriteFileAtomicReplacesInPlace(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeFileAtomic(dst, 0o644, strings.NewReader("new")); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}

	// The temporary file must not survive the rename.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want 1", len(entries))
	}
}
