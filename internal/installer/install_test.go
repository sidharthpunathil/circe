package installer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func skillArchive(t *testing.T, body string) []byte {
	t.Helper()
	return makeTarGz(t,
		file("alpha/SKILL.md", body),
		file("alpha/refs/a.md", "a\n"),
		file("beta/SKILL.md", "# beta\n"),
	)
}

func statuses(results []Result) map[string]Status {
	out := map[string]Status{}
	for _, r := range results {
		out[r.Name] = r.Status
	}
	return out
}

func TestInstallArchive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".claude", "skills")

	results, err := InstallArchive(bytes.NewReader(skillArchive(t, "# alpha\n")), Options{Dir: dir})
	if err != nil {
		t.Fatalf("InstallArchive: %v", err)
	}
	if got := statuses(results); got["alpha"] != StatusInstalled || got["beta"] != StatusInstalled {
		t.Fatalf("statuses = %v, want both installed", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "alpha", "refs", "a.md")); err != nil {
		t.Fatalf("nested file not installed: %v", err)
	}
	// No staging directory may survive a successful install.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("skills dir holds %d entries, want 2", len(entries))
	}

	// Re-installing identical content is a no-op, not a conflict.
	results, err = InstallArchive(bytes.NewReader(skillArchive(t, "# alpha\n")), Options{Dir: dir})
	if err != nil {
		t.Fatalf("InstallArchive (repeat): %v", err)
	}
	if got := statuses(results)["alpha"]; got != StatusUnchanged {
		t.Errorf("alpha = %q, want %q", got, StatusUnchanged)
	}
}

func TestInstallArchiveSkipsExistingWithoutForce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	if _, err := InstallArchive(bytes.NewReader(skillArchive(t, "# alpha\n")), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}

	local := filepath.Join(dir, "alpha", "SKILL.md")
	if err := os.WriteFile(local, []byte("# my local edits\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := InstallArchive(bytes.NewReader(skillArchive(t, "# alpha v2\n")), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	r := results[0]
	if r.Status != StatusSkipped {
		t.Fatalf("alpha = %q, want %q", r.Status, StatusSkipped)
	}
	if r.Reason == "" {
		t.Error("a skipped result must explain itself")
	}
	got, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# my local edits\n" {
		t.Errorf("local edits were clobbered: %q", got)
	}

	// The recorded checksum must describe what is on disk, not what was
	// downloaded, or the lockfile would claim content the project never got.
	onDisk := mustChecksum(t, filepath.Join(dir, "alpha"))
	if r.Checksum != onDisk {
		t.Errorf("skipped checksum = %s, want the on-disk %s", r.Checksum, onDisk)
	}

	results, err = InstallArchive(bytes.NewReader(skillArchive(t, "# alpha v2\n")), Options{Dir: dir, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := statuses(results)["alpha"]; got != StatusUpdated {
		t.Fatalf("alpha with --force = %q, want %q", got, StatusUpdated)
	}
	if got, _ := os.ReadFile(local); string(got) != "# alpha v2\n" {
		t.Errorf("forced install did not replace the file: %q", got)
	}
}

func TestInstallArchiveDryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "skills")

	results, err := InstallArchive(bytes.NewReader(skillArchive(t, "# alpha\n")), Options{Dir: dir, DryRun: true})
	if err != nil {
		t.Fatalf("InstallArchive: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.Status != StatusInstalled {
			t.Errorf("%s = %q, want %q", r.Name, r.Status, StatusInstalled)
		}
		if r.Checksum == "" {
			t.Errorf("%s has no checksum; a dry run should still know what it would write", r.Name)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dry run created %s", dir)
	}
}

func TestInstallArchiveRejectsUnsafeArchive(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "skills")
	archive := makeTarGz(t, file("alpha/SKILL.md", "ok\n"), file("../evil.md", "pwned"))

	if _, err := InstallArchive(bytes.NewReader(archive), Options{Dir: dir}); err == nil {
		t.Fatal("expected an error for an unsafe archive")
	}
	if _, err := os.Stat(filepath.Join(root, "evil.md")); err == nil {
		t.Error("unsafe archive escaped the destination")
	}
	// The good half of a bad archive must not be installed either.
	if _, err := os.Stat(filepath.Join(dir, "alpha")); err == nil {
		t.Error("a partially unsafe archive was partially installed")
	}
}

func TestRemove(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	if _, err := InstallArchive(bytes.NewReader(skillArchive(t, "# alpha\n")), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	removed, err := Remove(dir, "alpha")
	if err != nil || !removed {
		t.Fatalf("Remove = %v, %v; want true, nil", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "alpha")); !os.IsNotExist(err) {
		t.Error("skill directory still present after Remove")
	}
	if removed, err := Remove(dir, "alpha"); err != nil || removed {
		t.Errorf("Remove of a missing skill = %v, %v; want false, nil", removed, err)
	}
}
