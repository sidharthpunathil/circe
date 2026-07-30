package store

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sidharthpunathil/skillbuilder/internal/skillfile"
)

// ImportCandidate is a skill discovered in an external directory.
type ImportCandidate struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Files       int    `json:"files"`
	Size        int64  `json:"size"`
	Symlink     bool   `json:"symlink"`
	Exists      bool   `json:"exists"` // already present in this store
}

// ImportResult summarizes an import run.
type ImportResult struct {
	Imported []string `json:"imported"`
	Skipped  []string `json:"skipped"`
	Failed   []string `json:"failed"`
}

// DefaultImportSource is the conventional user skill directory.
func DefaultImportSource() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "skills")
}

// Scan lists importable skills in srcDir without copying anything. Symlinked
// skill directories are followed, since roughly half of a typical library is
// symlinks into plugin directories.
func (s *Store) Scan(srcDir string) ([]ImportCandidate, error) {
	if strings.TrimSpace(srcDir) == "" {
		return nil, fmt.Errorf("source directory is required")
	}
	srcDir = expandHome(srcDir)
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", srcDir, err)
	}

	var out []ImportCandidate
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(srcDir, name)
		info, err := os.Stat(full) // resolves symlinks
		if err != nil || !info.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(full, "SKILL.md"))
		if err != nil {
			continue // not a skill
		}

		c := ImportCandidate{Name: Slugify(name), Source: full}
		if lstat, err := os.Lstat(full); err == nil {
			c.Symlink = lstat.Mode()&os.ModeSymlink != 0
		}
		if f, err := parseForImport(raw); err == nil {
			c.Description = f
		}
		walkResolved(full, func(e WalkEntry) error {
			c.Files++
			c.Size += e.Info.Size()
			return nil
		})
		if _, err := s.Skill(c.Name); err == nil {
			c.Exists = true
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Import copies the named skills from srcDir into the store. When names is
// empty every discovered skill is imported. Existing skills are only replaced
// when overwrite is true.
func (s *Store) Import(srcDir string, names []string, overwrite bool) (ImportResult, error) {
	res := ImportResult{Imported: []string{}, Skipped: []string{}, Failed: []string{}}

	candidates, err := s.Scan(srcDir)
	if err != nil {
		return res, err
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}

	for _, c := range candidates {
		if len(want) > 0 && !want[c.Name] {
			continue
		}
		if err := ValidateName(c.Name); err != nil {
			res.Failed = append(res.Failed, c.Name+": "+err.Error())
			continue
		}
		if c.Exists && !overwrite {
			res.Skipped = append(res.Skipped, c.Name)
			continue
		}
		dst := s.skillDir(c.Name)
		if c.Exists {
			if err := os.RemoveAll(dst); err != nil {
				res.Failed = append(res.Failed, c.Name+": "+err.Error())
				continue
			}
		}
		if err := copyTree(c.Source, dst); err != nil {
			res.Failed = append(res.Failed, c.Name+": "+err.Error())
			continue
		}
		if err := s.refreshSkill(c.Name); err != nil {
			res.Failed = append(res.Failed, c.Name+": "+err.Error())
			continue
		}
		res.Imported = append(res.Imported, c.Name)
	}
	return res, nil
}

// WriteSkillFiles creates a skill from files already held in memory, which is
// how remote sources (a GitHub tarball) land on disk. Content is written by
// value so the skill survives the upstream repository disappearing.
func (s *Store) WriteSkillFiles(name string, files map[string][]byte, overwrite bool) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if _, ok := files["SKILL.md"]; !ok {
		return fmt.Errorf("skill %q has no SKILL.md", name)
	}

	dst := s.skillDir(name)
	if _, err := os.Stat(dst); err == nil {
		if !overwrite {
			return fmt.Errorf("skill %q: %w", name, ErrExists)
		}
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
	}

	// Stage into a temp directory and swap, so a failure part-way through
	// cannot leave a half-written skill behind.
	staging, err := os.MkdirTemp(s.skillsDir(), ".staging-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	for rel, content := range files {
		clean, err := safeRelPath(rel)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		full := filepath.Join(staging, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			return err
		}
	}

	if err := os.Rename(staging, dst); err != nil {
		return err
	}
	return s.refreshSkill(name)
}

// safeRelPath validates a path supplied by a remote archive before it is joined
// onto a directory we own.
func safeRelPath(rel string) (string, error) {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	if rel == "" || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("path must be relative")
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path escapes the skill directory")
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("invalid path segment %q", seg)
		}
	}
	return clean, nil
}

// parseForImport extracts just the description for the scan preview.
func parseForImport(raw []byte) (string, error) {
	f, err := skillfile.Parse(raw)
	if err != nil {
		return "", err
	}
	d := strings.TrimSpace(f.Description)
	if d == "" {
		d = f.Summary(160)
	}
	return d, nil
}

// copyTree recursively copies src into dst by value, following symlinks at
// every level so that a symlinked skill becomes a real, self-contained copy.
func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	copied := 0
	err := walkResolved(src, func(e WalkEntry) error {
		if e.Info.Size() > maxFileSize {
			return nil
		}
		if err := copyFile(e.Abs, filepath.Join(dst, filepath.FromSlash(e.Rel))); err != nil {
			return err
		}
		copied++
		return nil
	})
	if err != nil {
		return err
	}
	if copied == 0 {
		return fmt.Errorf("no files found under %s", src)
	}
	return nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}
