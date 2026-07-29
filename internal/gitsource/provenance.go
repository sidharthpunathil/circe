package gitsource

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Source records where an imported skill came from, so that a later "check for
// updates" knows what to compare against and the UI can link back to the
// original even after the local copy has been edited.
type Source struct {
	Skill      string    `json:"skill"`
	Repo       string    `json:"repo"` // owner/name
	URL        string    `json:"url"`
	Ref        string    `json:"ref"`
	CommitSHA  string    `json:"commitSha"`
	Path       string    `json:"path"`
	ImportedAt time.Time `json:"importedAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// LoadSources reads the provenance file, which is a JSON object keyed by skill
// name. A missing file is not an error: nothing has been imported yet.
func LoadSources(path string) (map[string]Source, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Source{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(raw) == 0 {
		return map[string]Source{}, nil
	}

	m := map[string]Source{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	// Tolerate a file whose entries predate the Skill field, so callers can
	// always trust s.Skill.
	for name, s := range m {
		if s.Skill == "" {
			s.Skill = name
			m[name] = s
		}
	}
	return m, nil
}

// SaveSources writes the provenance file through a temporary file and a rename,
// so an interrupted write cannot leave a truncated map that would make every
// imported skill look like it came from nowhere.
func SaveSources(path string, m map[string]Source) error {
	if m == nil {
		m = map[string]Source{}
	}
	// json.Marshal sorts map keys, so the file stays diff-friendly.
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode sources: %w", err)
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".sources-*")
	if err != nil {
		return fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // a no-op once the rename has succeeded

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("set mode on %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// NewSource builds a provenance record for one imported candidate.
func NewSource(skill string, repo Repo, meta Metadata, c Candidate) Source {
	return Source{
		Skill:      skill,
		Repo:       repo.String(),
		URL:        repo.HTMLURL(),
		Ref:        meta.Ref,
		CommitSHA:  meta.CommitSHA,
		Path:       c.Path,
		ImportedAt: time.Now().UTC(),
		UpdatedAt:  c.UpdatedAt,
	}
}
