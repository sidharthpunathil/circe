package store

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/sidharthpunathil/circe/internal/model"
	"github.com/sidharthpunathil/circe/internal/skillfile"
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
		files, err := readTree(c.Source)
		if err != nil {
			res.Failed = append(res.Failed, c.Name+": "+err.Error())
			continue
		}
		if err := s.WriteSkillFiles(c.Name, files, true); err != nil {
			res.Failed = append(res.Failed, c.Name+": "+err.Error())
			continue
		}
		res.Imported = append(res.Imported, c.Name)
	}
	return res, nil
}

// WriteSkillFiles creates a skill from files already held in memory, which is
// how remote sources (a repository tarball) land in the registry. Content is
// stored by value so the skill survives the upstream repository disappearing.
func (s *Store) WriteSkillFiles(name string, files map[string][]byte, overwrite bool) error {
	return s.WriteSkillFilesAt(name, files, overwrite, time.Time{})
}

// WriteSkillFilesAt is WriteSkillFiles with an explicit last-changed time, so
// restoring a backup does not make every skill look like it landed today.
func (s *Store) WriteSkillFilesAt(name string, files map[string][]byte, overwrite bool, at time.Time) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	source, ok := files["SKILL.md"]
	if !ok {
		return fmt.Errorf("skill %q has no SKILL.md", name)
	}

	existing, err := s.findByField(colSkills, "name", name)
	if err == nil && !overwrite {
		return fmt.Errorf("skill %q: %w", name, ErrExists)
	}

	// Replacing a skill means replacing it, not merging into it: a companion
	// file the new version dropped must not linger.
	if err == nil {
		old, err := s.app.FindRecordsByFilter(colSkillFile, "skill = {:id}", "", 0, 0,
			map[string]any{"id": existing.Id})
		if err != nil {
			return err
		}
		for _, rec := range old {
			if err := s.app.Delete(rec); err != nil {
				return err
			}
		}
	}

	if err := s.writeSkillSource(name, source, at); err != nil {
		return err
	}
	skRec, err := s.findByField(colSkills, "name", name)
	if err != nil {
		return err
	}

	fileCol, err := s.app.FindCollectionByNameOrId(colSkillFile)
	if err != nil {
		return err
	}
	for rel, content := range files {
		if rel == "SKILL.md" {
			continue
		}
		clean, err := safeRelPath(rel)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if int64(len(content)) > maxFileSize {
			continue
		}
		rec := core.NewRecord(fileCol)
		rec.Set("skill", skRec.Id)
		rec.Set("path", clean)
		if err := setBlob(rec, path.Base(clean), content); err != nil {
			return err
		}
		if err := asStoreError(s.app.Save(rec)); err != nil {
			return err
		}
	}
	return s.refreshSkill(name)
}

// writeSkillSource stores a SKILL.md verbatim, without going through SaveSkill.
//
// SaveSkill re-renders the frontmatter from the parsed struct, which is right
// when a person edits a skill in the browser and wrong when the bytes came from
// somewhere else: an import should preserve what the author wrote, including
// key order and any frontmatter this tool does not model.
func (s *Store) writeSkillSource(name string, source []byte, at time.Time) error {
	sk, err := model.ParseSkill(name, source)
	if err != nil {
		return fmt.Errorf("skill %q: %w", name, err)
	}
	return s.upsert(colSkills, "name", name, func(rec *core.Record) {
		rec.Set("source", string(source))
		rec.Set("description", sk.Description)
		rec.Set("tags", sk.Tags)
		rec.Set("size", len(source))
		setUpdated(rec, "updated", at)
	})
}

// readTree loads a skill directory from disk into memory, following symlinks at
// every level so that a symlinked skill is imported by value.
func readTree(src string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := walkResolved(src, func(e WalkEntry) error {
		if e.Info.Size() > maxFileSize {
			return nil
		}
		data, err := os.ReadFile(e.Abs)
		if err != nil {
			return err
		}
		out[e.Rel] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no files found under %s", src)
	}
	return out, nil
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

// parseForImport extracts just the description for the scan preview. The
// summary is shorter than the one a stored skill gets, because this is a line
// in a list of candidates rather than the skill's own description.
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

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}
