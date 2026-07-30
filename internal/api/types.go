// Package api defines the wire contract shared by the server and the CLI.
// Both sides import these types so the two cannot drift apart.
package api

import "time"

// Version is the API version served under /api/v1.
const Version = "v1"

// FileRef describes a file bundled inside a skill.
type FileRef struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Skill is the wire representation of a skill.
type Skill struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Tags        []string  `json:"tags,omitempty"`
	Files       []FileRef `json:"files,omitempty"`
	Size        int64     `json:"size"`
	UpdatedAt   time.Time `json:"updatedAt"`

	// Body is populated only on the single-skill endpoint.
	Body string `json:"body,omitempty"`
	// Bundles lists bundles containing this skill (single-skill endpoint only).
	Bundles []string `json:"bundles,omitempty"`
}

// Bundle is the wire representation of a bundle.
type Bundle struct {
	Name        string    `json:"name"`
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description,omitempty"`
	Skills      []string  `json:"skills"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// SkillListResponse is returned by GET /api/v1/skills.
type SkillListResponse struct {
	Skills []Skill `json:"skills"`
	Total  int     `json:"total"`
}

// BundleListResponse is returned by GET /api/v1/bundles.
type BundleListResponse struct {
	Bundles []Bundle `json:"bundles"`
	Total   int      `json:"total"`
}

// BundleManifest is returned by GET /api/v1/bundles/{name} and is what the CLI
// resolves before installing. Skills are fully expanded so the CLI can render a
// selection list without a second round trip.
type BundleManifest struct {
	Bundle Bundle  `json:"bundle"`
	Skills []Skill `json:"skills"`
	// Context holds project-level agent instructions the folder ships with,
	// keyed by filename (CLAUDE.md, AGENTS.md). The CLI merges these into the
	// target project rather than overwriting whatever is already there.
	Context map[string]string `json:"context,omitempty"`
	// Registry is the base URL the CLI should use for follow-up downloads.
	Registry string `json:"registry,omitempty"`
}

// Error is the JSON body returned for any non-2xx API response.
type Error struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// InstalledSkill records one installed skill in the project lockfile.
type InstalledSkill struct {
	Name        string    `json:"name"`
	Bundle      string    `json:"bundle,omitempty"`
	Registry    string    `json:"registry"`
	Checksum    string    `json:"checksum"`
	InstalledAt time.Time `json:"installedAt"`
}

// Lockfile is written to skillbuilder.json in a project root so that installs
// are reproducible and `pull` knows what to refresh.
type Lockfile struct {
	Version  int    `json:"version"`
	Registry string `json:"registry"`
	// Dir is the install directory relative to the project root. Recording it
	// means pull/list/remove find the skills without repeating -d.
	Dir     string                    `json:"dir,omitempty"`
	Bundles []string                  `json:"bundles,omitempty"`
	Skills  map[string]InstalledSkill `json:"skills"`
}

// DefaultInstallDir is where skills are installed when -d is not given.
const DefaultInstallDir = ".claude/skills"

// InstallDir returns the recorded directory, falling back to the default.
func (l *Lockfile) InstallDir() string {
	if l != nil && l.Dir != "" {
		return l.Dir
	}
	return DefaultInstallDir
}

// LockfileVersion is the current schema version of skillbuilder.json.
const LockfileVersion = 1
