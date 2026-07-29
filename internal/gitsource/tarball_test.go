package gitsource

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"sort"
	"strings"
	"testing"
)

// tarEntry is one entry in a fixture archive. The zero typeflag means a regular
// file; declaredSize overrides the header size so a lying header can be tested.
type tarEntry struct {
	name         string
	body         string
	typeflag     byte
	link         string
	declaredSize int64
}

func file(name, body string) tarEntry {
	return tarEntry{name: name, body: body, typeflag: tar.TypeReg}
}

func dir(name string) tarEntry {
	return tarEntry{name: strings.TrimSuffix(name, "/") + "/", typeflag: tar.TypeDir}
}

func makeTarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		flag := e.typeflag
		if flag == 0 {
			flag = tar.TypeReg
		}
		size := int64(len(e.body))
		if flag != tar.TypeReg {
			size = 0
		}
		if e.declaredSize > 0 {
			size = e.declaredSize
		}
		hdr := &tar.Header{Name: e.name, Typeflag: flag, Mode: 0o644, Size: size, Linkname: e.link}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", e.name, err)
		}
		if flag == tar.TypeReg && size > 0 {
			body := e.body
			// Pad so the writer sees exactly the bytes it was promised.
			if int64(len(body)) < size {
				body += strings.Repeat("x", int(size)-len(body))
			}
			if _, err := tw.Write([]byte(body[:size])); err != nil {
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

// repoTarGz builds an archive shaped like GitHub's: every path nested under one
// generated owner-repo-<sha> directory.
func repoTarGz(t *testing.T, root string, files map[string]string) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)

	entries := []tarEntry{dir(root)}
	for _, n := range names {
		entries = append(entries, file(root+"/"+n, files[n]))
	}
	return makeTarGz(t, entries...)
}

func skillMD(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n\nDo the thing.\n"
}

func readFixture(t *testing.T, subpath string, lim limits, gz []byte) *archive {
	t.Helper()
	arc, err := readTarball(bytes.NewReader(gz), subpath, lim)
	if err != nil {
		t.Fatalf("readTarball: %v", err)
	}
	return arc
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestReadTarballStripsGeneratedRoot(t *testing.T) {
	gz := repoTarGz(t, "emilkowalski-skills-abc123", map[string]string{
		"README.md":            "hi",
		"skills/foo/SKILL.md":  skillMD("foo", "does foo"),
		"skills/foo/ref/a.txt": "a",
	})

	arc := readFixture(t, "", defaultLimits, gz)
	if arc.Root != "emilkowalski-skills-abc123" {
		t.Fatalf("root = %q", arc.Root)
	}
	want := []string{"README.md", "skills/foo/SKILL.md", "skills/foo/ref/a.txt"}
	if got := keys(arc.Files); !equal(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
}

func TestReadTarballExclusions(t *testing.T) {
	gz := repoTarGz(t, "o-r-sha", map[string]string{
		"skills/foo/SKILL.md":              skillMD("foo", "does foo"),
		"skills/foo/node_modules/lib/x.js": "junk",
		"skills/foo/__pycache__/m.pyc":     "junk",
		"skills/foo/.venv/lib/x.py":        "junk",
		"skills/foo/scripts/run.pyc":       "junk",
		"skills/foo/.DS_Store":             "junk",
		"skills/foo/scripts/run.py":        "keep",
		".git/config":                      "junk",
		".github/workflows/ci.yml":         "junk",
	})

	arc := readFixture(t, "", defaultLimits, gz)
	want := []string{"skills/foo/SKILL.md", "skills/foo/scripts/run.py"}
	if got := keys(arc.Files); !equal(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
}

func TestReadTarballSkipsNonRegularEntries(t *testing.T) {
	gz := makeTarGz(t,
		dir("o-r-sha"),
		file("o-r-sha/SKILL.md", skillMD("root", "a root skill")),
		tarEntry{name: "o-r-sha/link.md", typeflag: tar.TypeSymlink, link: "SKILL.md"},
		tarEntry{name: "o-r-sha/hard.md", typeflag: tar.TypeLink, link: "SKILL.md"},
		tarEntry{name: "o-r-sha/dev", typeflag: tar.TypeChar},
	)

	arc := readFixture(t, "", defaultLimits, gz)
	if got := keys(arc.Files); !equal(got, []string{"SKILL.md"}) {
		t.Fatalf("files = %v, want only SKILL.md", got)
	}
}

func TestReadTarballRejectsTraversal(t *testing.T) {
	cases := []struct{ name, entry string }{
		{"parent segment", "o-r-sha/../../etc/passwd"},
		{"leading parent", "../evil.md"},
		{"absolute", "/etc/passwd"},
		{"backslash", `o-r-sha\..\evil.md`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gz := makeTarGz(t,
				dir("o-r-sha"),
				file("o-r-sha/SKILL.md", skillMD("root", "a root skill")),
				file(tc.entry, "pwned"),
			)
			_, err := readTarball(bytes.NewReader(gz), "", defaultLimits)
			var ue *UnsafeArchiveError
			if !errors.As(err, &ue) {
				t.Fatalf("err = %v, want UnsafeArchiveError", err)
			}
		})
	}
}

func TestReadTarballTotalSizeCap(t *testing.T) {
	gz := repoTarGz(t, "o-r-sha", map[string]string{
		"a.txt": strings.Repeat("a", 600),
		"b.txt": strings.Repeat("b", 600),
	})

	lim := limits{file: 1 << 20, total: 1000, entries: 100}
	_, err := readTarball(bytes.NewReader(gz), "", lim)
	if err == nil || !strings.Contains(err.Error(), "expands to more than") {
		t.Fatalf("err = %v, want a size-cap error", err)
	}
}

// An excluded file still costs us the decompression, so it must count toward
// the archive budget even though it is never retained.
func TestReadTarballCapCountsExcludedFiles(t *testing.T) {
	gz := repoTarGz(t, "o-r-sha", map[string]string{
		"node_modules/big.js": strings.Repeat("x", 2000),
		"SKILL.md":            skillMD("root", "a root skill"),
	})

	lim := limits{file: 1 << 20, total: 1000, entries: 100}
	if _, err := readTarball(bytes.NewReader(gz), "", lim); err == nil {
		t.Fatal("want a size-cap error for an excluded but decompressed file")
	}
}

func TestReadTarballPerFileCap(t *testing.T) {
	gz := repoTarGz(t, "o-r-sha", map[string]string{
		"SKILL.md": skillMD("root", "a root skill"),
		"big.bin":  strings.Repeat("x", 500),
	})

	lim := limits{file: 100, total: 1 << 20, entries: 100}
	arc := readFixture(t, "", lim, gz)
	if _, ok := arc.Files["big.bin"]; ok {
		t.Fatal("oversized file was retained")
	}
	if _, ok := arc.Files["SKILL.md"]; !ok {
		t.Fatal("SKILL.md was dropped")
	}
}

// A header may declare a small size and then stream more; only what is actually
// read decides whether the file is kept.
func TestReadTarballRejectsUndersizedHeader(t *testing.T) {
	gz := makeTarGz(t,
		dir("o-r-sha"),
		tarEntry{name: "o-r-sha/big.bin", typeflag: tar.TypeReg, body: "x", declaredSize: 500},
	)

	lim := limits{file: 100, total: 1 << 20, entries: 100}
	arc := readFixture(t, "", lim, gz)
	if _, ok := arc.Files["big.bin"]; ok {
		t.Fatal("file exceeding the per-file cap was retained")
	}
}

func TestReadTarballEntryCap(t *testing.T) {
	gz := repoTarGz(t, "o-r-sha", map[string]string{
		"a.txt": "a", "b.txt": "b", "c.txt": "c",
	})

	lim := limits{file: 1 << 20, total: 1 << 20, entries: 2}
	_, err := readTarball(bytes.NewReader(gz), "", lim)
	if err == nil || !strings.Contains(err.Error(), "more than 2 files") {
		t.Fatalf("err = %v, want an entry-count error", err)
	}
}

func TestReadTarballSubpath(t *testing.T) {
	gz := repoTarGz(t, "o-r-sha", map[string]string{
		"skills/foo/SKILL.md":    skillMD("foo", "does foo"),
		"skills/foobar/SKILL.md": skillMD("foobar", "does foobar"),
		"skills/bar/SKILL.md":    skillMD("bar", "does bar"),
		"README.md":              "hi",
	})

	arc := readFixture(t, "skills/foo", defaultLimits, gz)
	if got := keys(arc.Files); !equal(got, []string{"skills/foo/SKILL.md"}) {
		t.Fatalf("files = %v, want only skills/foo/SKILL.md", got)
	}
}

func TestReadTarballRejectsNonGzip(t *testing.T) {
	if _, err := readTarball(strings.NewReader("not gzip at all"), "", defaultLimits); err == nil {
		t.Fatal("want an error for a non-gzip body")
	}
}

func TestExcluded(t *testing.T) {
	cases := map[string]bool{
		"SKILL.md":                 false,
		"references/guide.md":      false,
		"scripts/run.py":           false,
		"node_modules/a/b.js":      true,
		"a/node_modules/b.js":      true,
		"__pycache__/x.pyc":        true,
		".venv/lib/x.py":           true,
		"venv/lib/x.py":            true,
		".git/HEAD":                true,
		".DS_Store":                true,
		"nested/.DS_Store":         true,
		"scripts/compiled.pyc":     true,
		"scripts/compiled.PYC":     true,
		".github/workflows/ci.yml": true,
	}
	for in, want := range cases {
		if got := excluded(in); got != want {
			t.Errorf("excluded(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestExcludedInSkill(t *testing.T) {
	// evals/ is dropped from inside a skill, but the same name elsewhere in the
	// repository is only ever seen after the skill root has been established.
	if !excludedInSkill("evals/case1.json") {
		t.Error("evals/ at the skill root should be excluded")
	}
	if excludedInSkill("references/evals/case1.json") {
		t.Error("a nested evals/ should be kept")
	}
	if excludedInSkill("evals.md") {
		t.Error("a file named evals.md should be kept")
	}
}

func equal(a, b []string) bool {
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
