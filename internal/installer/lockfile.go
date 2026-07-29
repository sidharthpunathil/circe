package installer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sidharthp/skillbuilder/internal/api"
)

// LockfileName is the file skillbuilder writes in a project root.
const LockfileName = "skillbuilder.json"

// LockfilePath returns the lockfile path for a project root.
func LockfilePath(root string) string { return filepath.Join(root, LockfileName) }

// NewLockfile returns an empty lockfile for a registry.
func NewLockfile(registry string) *api.Lockfile {
	return &api.Lockfile{
		Version:  api.LockfileVersion,
		Registry: registry,
		Skills:   map[string]api.InstalledSkill{},
	}
}

// LoadLockfile reads a project's lockfile. A missing file is not an error: the
// first `init` in a project has nothing to read, and every command should behave
// the same way there as it does in a project with an empty lockfile.
func LoadLockfile(root string) (*api.Lockfile, error) {
	b, err := os.ReadFile(LockfilePath(root))
	if errors.Is(err, fs.ErrNotExist) {
		return NewLockfile(""), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", LockfileName, err)
	}

	var lf api.Lockfile
	if err := json.Unmarshal(b, &lf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", LockfileName, err)
	}
	if lf.Version > api.LockfileVersion {
		return nil, fmt.Errorf("%s was written by a newer skillbuilder (lockfile version %d, this build understands %d)", LockfileName, lf.Version, api.LockfileVersion)
	}
	if lf.Skills == nil {
		lf.Skills = map[string]api.InstalledSkill{}
	}
	return &lf, nil
}

// SaveLockfile writes a project's lockfile atomically. Keys are emitted in a
// stable order so the file diffs cleanly when it is committed to git.
func SaveLockfile(root string, lf *api.Lockfile) error {
	lf.Version = api.LockfileVersion
	if lf.Skills == nil {
		lf.Skills = map[string]api.InstalledSkill{}
	}
	lf.Bundles = dedupe(lf.Bundles)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(lf); err != nil {
		return fmt.Errorf("encode %s: %w", LockfileName, err)
	}
	if _, err := writeFileAtomic(LockfilePath(root), 0o644, &buf); err != nil {
		return err
	}
	return nil
}

// ResolveDir picks the install directory: an explicit -d always wins, then the
// directory recorded by a previous install, then the default. This is what lets
// `pull` and `list` work without repeating the -d used at install time.
func ResolveDir(explicit string, explicitSet bool, lf *api.Lockfile) string {
	if explicitSet && explicit != "" {
		return explicit
	}
	if lf != nil && lf.Dir != "" {
		return lf.Dir
	}
	if explicit != "" {
		return explicit
	}
	return DefaultSkillsDir
}

// RecordDir stores the directory skills were installed into.
func RecordDir(lf *api.Lockfile, dir string) {
	if lf == nil {
		return
	}
	if dir == "" || dir == DefaultSkillsDir {
		lf.Dir = ""
		return
	}
	lf.Dir = filepath.ToSlash(dir)
}

// RecordSkill adds or replaces a skill entry.
func RecordSkill(lf *api.Lockfile, s api.InstalledSkill) {
	if lf.Skills == nil {
		lf.Skills = map[string]api.InstalledSkill{}
	}
	if s.InstalledAt.IsZero() {
		s.InstalledAt = time.Now().UTC().Truncate(time.Second)
	} else {
		s.InstalledAt = s.InstalledAt.UTC().Truncate(time.Second)
	}
	// An existing entry keeps its bundle when the new one does not name it, so
	// re-running `add` on a bundled skill does not lose where it came from.
	if prev, ok := lf.Skills[s.Name]; ok && s.Bundle == "" {
		s.Bundle = prev.Bundle
	}
	lf.Skills[s.Name] = s
}

// ForgetSkill drops a skill and any bundle left with no members. It reports
// whether the skill was present.
func ForgetSkill(lf *api.Lockfile, name string) bool {
	if _, ok := lf.Skills[name]; !ok {
		return false
	}
	delete(lf.Skills, name)

	inUse := map[string]bool{}
	for _, s := range lf.Skills {
		if s.Bundle != "" {
			inUse[s.Bundle] = true
		}
	}
	kept := lf.Bundles[:0]
	for _, b := range lf.Bundles {
		if inUse[b] {
			kept = append(kept, b)
		}
	}
	lf.Bundles = kept
	return true
}

// AddBundle records that a bundle was installed into this project.
func AddBundle(lf *api.Lockfile, name string) {
	if name == "" {
		return
	}
	lf.Bundles = dedupe(append(lf.Bundles, name))
}

// LockedSkills returns the lockfile's skills sorted by name.
func LockedSkills(lf *api.Lockfile) []api.InstalledSkill {
	out := make([]api.InstalledSkill, 0, len(lf.Skills))
	for _, s := range lf.Skills {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LockedNames returns the lockfile's skill names, sorted.
func LockedNames(lf *api.Lockfile) []string {
	out := make([]string, 0, len(lf.Skills))
	for name := range lf.Skills {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// DriftState describes how an installed skill compares to its lockfile entry.
type DriftState string

const (
	// DriftNone means the directory matches the recorded checksum.
	DriftNone DriftState = ""
	// DriftMissing means the directory is gone.
	DriftMissing DriftState = "missing"
	// DriftModified means the directory's contents no longer match.
	DriftModified DriftState = "modified"
)

// Drift compares one locked skill against the copy on disk.
func Drift(dir string, s api.InstalledSkill) (DriftState, error) {
	sum, err := checksumIfExists(SkillDir(dir, s.Name))
	if err != nil {
		return DriftNone, err
	}
	switch {
	case sum == "":
		return DriftMissing, nil
	case sum != s.Checksum:
		return DriftModified, nil
	}
	return DriftNone, nil
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
