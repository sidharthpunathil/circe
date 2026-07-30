package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sidharthpunathil/circe/internal/api"
)

func TestLockfileRoundTrip(t *testing.T) {
	root := t.TempDir()
	installed := time.Date(2026, 7, 29, 10, 30, 0, 0, time.UTC)

	lf := NewLockfile("http://localhost:8080")
	AddBundle(lf, "writing")
	RecordSkill(lf, api.InstalledSkill{
		Name:        "brand-design",
		Bundle:      "writing",
		Registry:    "http://localhost:8080",
		Checksum:    ChecksumPrefix + "abc123",
		InstalledAt: installed,
	})
	RecordSkill(lf, api.InstalledSkill{
		Name:        "apple-design",
		Registry:    "http://localhost:8080",
		Checksum:    ChecksumPrefix + "def456",
		InstalledAt: installed,
	})

	if err := SaveLockfile(root, lf); err != nil {
		t.Fatalf("SaveLockfile: %v", err)
	}

	got, err := LoadLockfile(root)
	if err != nil {
		t.Fatalf("LoadLockfile: %v", err)
	}
	if got.Version != api.LockfileVersion {
		t.Errorf("version = %d, want %d", got.Version, api.LockfileVersion)
	}
	if got.Registry != lf.Registry {
		t.Errorf("registry = %q, want %q", got.Registry, lf.Registry)
	}
	if len(got.Skills) != 2 {
		t.Fatalf("loaded %d skills, want 2", len(got.Skills))
	}
	want := lf.Skills["brand-design"]
	if have := got.Skills["brand-design"]; have != want {
		t.Errorf("brand-design round-tripped as %+v, want %+v", have, want)
	}
	if len(got.Bundles) != 1 || got.Bundles[0] != "writing" {
		t.Errorf("bundles = %v, want [writing]", got.Bundles)
	}

	// Saving what was loaded must produce byte-identical output, otherwise a
	// committed lockfile churns on every command.
	first, err := os.ReadFile(LockfilePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveLockfile(root, got); err != nil {
		t.Fatalf("SaveLockfile (second): %v", err)
	}
	second, err := os.ReadFile(LockfilePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("re-saving changed the file:\n%s\n---\n%s", first, second)
	}
}

func TestLoadLockfileMissing(t *testing.T) {
	lf, err := LoadLockfile(t.TempDir())
	if err != nil {
		t.Fatalf("LoadLockfile on a fresh project: %v", err)
	}
	if lf.Skills == nil {
		t.Error("Skills map is nil; callers must be able to write to it")
	}
	if len(lf.Skills) != 0 {
		t.Errorf("expected no skills, got %d", len(lf.Skills))
	}
}

func TestLoadLockfileRejectsNewerVersion(t *testing.T) {
	root := t.TempDir()
	b, err := json.Marshal(api.Lockfile{Version: api.LockfileVersion + 1, Skills: map[string]api.InstalledSkill{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, LockfileName), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLockfile(root); err == nil {
		t.Error("expected an error for a lockfile from a newer CLI")
	}
}

func TestLoadLockfileRejectsGarbage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, LockfileName), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLockfile(root); err == nil {
		t.Error("expected an error for an unparseable lockfile")
	}
}

func TestRecordSkillKeepsBundle(t *testing.T) {
	lf := NewLockfile("http://localhost:8080")
	RecordSkill(lf, api.InstalledSkill{Name: "a", Bundle: "core", Checksum: "1"})
	RecordSkill(lf, api.InstalledSkill{Name: "a", Checksum: "2"})

	got := lf.Skills["a"]
	if got.Bundle != "core" {
		t.Errorf("bundle = %q, want %q — re-adding a skill should not forget where it came from", got.Bundle, "core")
	}
	if got.Checksum != "2" {
		t.Errorf("checksum = %q, want %q", got.Checksum, "2")
	}
	if got.InstalledAt.IsZero() {
		t.Error("InstalledAt was not filled in")
	}
}

func TestForgetSkillPrunesEmptyBundles(t *testing.T) {
	lf := NewLockfile("http://localhost:8080")
	AddBundle(lf, "core")
	AddBundle(lf, "core") // duplicates must collapse
	AddBundle(lf, "extra")
	RecordSkill(lf, api.InstalledSkill{Name: "a", Bundle: "core", Checksum: "1"})
	RecordSkill(lf, api.InstalledSkill{Name: "b", Bundle: "extra", Checksum: "2"})

	if len(lf.Bundles) != 2 {
		t.Fatalf("bundles = %v, want two entries", lf.Bundles)
	}
	if !ForgetSkill(lf, "a") {
		t.Fatal("ForgetSkill reported a known skill as absent")
	}
	if len(lf.Bundles) != 1 || lf.Bundles[0] != "extra" {
		t.Errorf("bundles = %v, want [extra]", lf.Bundles)
	}
	if ForgetSkill(lf, "a") {
		t.Error("ForgetSkill reported an unknown skill as present")
	}
}

func TestDrift(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "skills")
	writeSkill(t, filepath.Join(dir, "alpha"), map[string]string{"SKILL.md": "body\n"})

	sum := mustChecksum(t, filepath.Join(dir, "alpha"))
	locked := api.InstalledSkill{Name: "alpha", Checksum: sum}

	if state, err := Drift(dir, locked); err != nil || state != DriftNone {
		t.Errorf("Drift = %q, %v; want no drift", state, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if state, err := Drift(dir, locked); err != nil || state != DriftModified {
		t.Errorf("Drift = %q, %v; want %q", state, err, DriftModified)
	}

	if err := os.RemoveAll(filepath.Join(dir, "alpha")); err != nil {
		t.Fatal(err)
	}
	if state, err := Drift(dir, locked); err != nil || state != DriftMissing {
		t.Errorf("Drift = %q, %v; want %q", state, err, DriftMissing)
	}
}
