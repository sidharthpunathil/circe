package gitsource

import "time"

// Source records where an imported skill came from, so that a later "check for
// updates" knows what to compare against and the UI can link back to the
// original even after the local copy has been edited.
//
// The registry stores these itself, one row per skill, so they are exported and
// committed alongside the content they describe. This package only builds them.
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
