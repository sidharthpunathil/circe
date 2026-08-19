// Package model is what a skill, rule, MCP server and folder *are*: their
// shape, their validation, and how each one serializes to the document that
// represents it.
//
// It is deliberately separate from the store. The store opens a database; the
// CLI never does, and only needs to know what these things look like in order
// to install them. Keeping the two apart is what lets the CLI stay a small
// binary with no database engine linked into it.
package model

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sidharthpunathil/circe/internal/skillfile"
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

// Bundle is a named set of everything a project needs at once: skills to
// install, rules to merge into its CLAUDE.md, and MCP servers to add to its
// .mcp.json.
//
// There used to be three parallel folder types, one per kind. That was a
// modelling mistake: the reason to group things is that you reach for them
// together, and what you reach for together is rarely all one kind. One folder
// holds any mix, and the CLI installs whichever parts you ask for.
type Bundle struct {
	Name        string    `json:"name"`
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description,omitempty"`
	Skills      []string  `json:"skills"`
	Rules       []string  `json:"rules,omitempty"`
	Servers     []string  `json:"servers,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// Has reports whether the bundle already contains the named skill.
func (b *Bundle) Has(skill string) bool { return contains(b.Skills, skill) }

// HasRule reports whether the bundle already contains the named rule.
func (b *Bundle) HasRule(name string) bool { return contains(b.Rules, name) }

// HasServer reports whether the bundle already contains the named server.
func (b *Bundle) HasServer(name string) bool { return contains(b.Servers, name) }

// Count is how many things are in the folder, across all three kinds.
func (b *Bundle) Count() int { return len(b.Skills) + len(b.Rules) + len(b.Servers) }

// IsEmpty reports whether the folder would install nothing.
func (b *Bundle) IsEmpty() bool { return b.Count() == 0 }

// DisplayTitle falls back to the name when no title is set.
func (b *Bundle) DisplayTitle() string {
	if strings.TrimSpace(b.Title) != "" {
		return b.Title
	}
	return b.Name
}

// ---------- skill documents ----------

// ExtractTags pulls tags from either metadata.tags or a top-level tags key,
// since both conventions appear in real skill libraries.
func ExtractTags(f skillfile.Frontmatter) []string {
	var out []string
	add := func(v any) {
		switch t := v.(type) {
		case []any:
			for _, item := range t {
				if str, ok := item.(string); ok {
					if slug := Slugify(str); slug != "" {
						out = append(out, slug)
					}
				}
			}
		case []string:
			for _, str := range t {
				if slug := Slugify(str); slug != "" {
					out = append(out, slug)
				}
			}
		case string:
			out = append(out, ParseTags(t)...)
		}
	}
	if f.Metadata != nil {
		add(f.Metadata["tags"])
	}
	if f.Extra != nil {
		add(f.Extra["tags"])
	}

	seen := map[string]bool{}
	var dedup []string
	for _, t := range out {
		if !seen[t] {
			seen[t] = true
			dedup = append(dedup, t)
		}
	}
	sort.Strings(dedup)
	return dedup
}

// ParseSkill reads a SKILL.md back into a Skill. Size, file list and timestamp
// are the store's business and are left zero here.
func ParseSkill(name string, raw []byte) (*Skill, error) {
	f, err := skillfile.Parse(raw)
	if err != nil {
		return nil, err
	}
	sk := &Skill{
		Name:        name,
		Description: strings.TrimSpace(f.Description),
		Body:        f.Body,
		Front:       f.Frontmatter,
		Tags:        ExtractTags(f.Frontmatter),
		Files:       []FileRef{},
	}
	if sk.Description == "" {
		sk.Description = f.Summary(180)
	}
	return sk, nil
}

// Document renders the skill as the SKILL.md that represents it.
//
// The caller's frontmatter maps are cloned before being edited: sharing them
// would mutate the value the caller still holds, which for a value handed out
// by an in-memory index is also a data race.
func (s *Skill) Document() []byte {
	front := s.Front
	front.Metadata = cloneMap(s.Front.Metadata)
	front.Extra = cloneMap(s.Front.Extra)
	front.Name = s.Name
	front.Description = strings.TrimSpace(s.Description)

	if len(s.Tags) > 0 {
		if front.Metadata == nil {
			front.Metadata = map[string]any{}
		}
		front.Metadata["tags"] = append([]string(nil), s.Tags...)
	} else if front.Metadata != nil {
		delete(front.Metadata, "tags")
		if len(front.Metadata) == 0 {
			front.Metadata = nil
		}
	}
	// Tags live in metadata; drop any stale top-level copy so they cannot diverge.
	if front.Extra != nil {
		delete(front.Extra, "tags")
		if len(front.Extra) == 0 {
			front.Extra = nil
		}
	}

	f := &skillfile.File{Frontmatter: front, Body: s.Body}
	return f.Bytes()
}

// cloneMap returns a shallow copy so callers' maps are never mutated in place.
func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Document renders the folder as the JSON manifest that represents it.
func (b *Bundle) Document() ([]byte, error) {
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}
