package store

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sidharthpunathil/skillbuilder/internal/skillfile"
)

// nameRe constrains skill and bundle names to a safe, filesystem-friendly slug.
// This is also the primary defense against path traversal: a name that matches
// this pattern cannot contain a separator or a dot segment.
var nameRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// ValidateName reports whether s is usable as a skill or bundle directory name.
func ValidateName(s string) error {
	switch {
	case s == "":
		return fmt.Errorf("give it a name")
	case len(s) > 64:
		return fmt.Errorf("that name is too long; keep it under 64 characters")
	case !nameRe.MatchString(s):
		return fmt.Errorf("use lowercase letters, digits and hyphens, like my-skill")
	}
	return nil
}

// Slugify converts arbitrary text into a valid name.
func Slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastHyphen := true // suppress leading hyphen
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		default:
			if !lastHyphen {
				b.WriteRune('-')
				lastHyphen = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 64 {
		out = strings.Trim(out[:64], "-")
	}
	return out
}

// FileRef describes one bundled file inside a skill directory.
type FileRef struct {
	Path string `json:"path"` // slash-separated, relative to the skill dir
	Size int64  `json:"size"`
}

// Ext returns the lowercase file extension without the dot.
func (f FileRef) Ext() string {
	i := strings.LastIndex(f.Path, ".")
	if i < 0 {
		return ""
	}
	return strings.ToLower(f.Path[i+1:])
}

// Dir returns the top-level directory of the file, or "" if at the root.
func (f FileRef) Dir() string {
	i := strings.Index(f.Path, "/")
	if i < 0 {
		return ""
	}
	return f.Path[:i]
}

// Skill is a stored skill: its parsed SKILL.md plus any bundled files.
type Skill struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Body        string                `json:"body"`
	Front       skillfile.Frontmatter `json:"-"`
	Tags        []string              `json:"tags,omitempty"`
	Files       []FileRef             `json:"files,omitempty"`
	UpdatedAt   time.Time             `json:"updatedAt"`
	Size        int64                 `json:"size"`
}

// TagsCSV renders tags for a form field.
func (s *Skill) TagsCSV() string { return strings.Join(s.Tags, ", ") }

// ParseTags splits a comma-separated tag input into a normalized, deduped list.
func ParseTags(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(s, ",") {
		t := Slugify(part)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Bundle is a named, ordered set of skills that the CLI can install together.
type Bundle struct {
	Name        string    `json:"name"`
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description,omitempty"`
	Skills      []string  `json:"skills"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Has reports whether the bundle already contains the named skill.
func (b *Bundle) Has(skill string) bool {
	for _, s := range b.Skills {
		if s == skill {
			return true
		}
	}
	return false
}

// DisplayTitle falls back to the name when no title is set.
func (b *Bundle) DisplayTitle() string {
	if strings.TrimSpace(b.Title) != "" {
		return b.Title
	}
	return b.Name
}
