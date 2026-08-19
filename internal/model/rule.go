package model

import (
	"strings"
	"time"

	"github.com/sidharthpunathil/circe/internal/skillfile"
)

// Rules are project guidance rather than agent capability: the text that ends
// up in a project's CLAUDE.md or AGENTS.md. They are deliberately a separate
// type from skills because they install differently: a skill is a directory
// copied into .claude/skills, a rule is prose merged into a file the project
// already owns.
//
// A rule is one markdown file with frontmatter, at data/rules/<name>.md. It
// needs no directory because, unlike a skill, it has no companion files.

// Targets a rule can be written into.
const (
	TargetClaude = "CLAUDE.md"
	TargetAgents = "AGENTS.md"
)

// RuleTargets lists the files a rule can be merged into.
func RuleTargets() []string { return []string{TargetClaude, TargetAgents} }

// ValidRuleTarget reports whether name is a target we write.
func ValidRuleTarget(name string) bool {
	return name == TargetClaude || name == TargetAgents
}

// Rule is one piece of project guidance.
type Rule struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	// Targets are the files this rule merges into. Empty means CLAUDE.md.
	Targets   []string  `json:"targets,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	Size      int64     `json:"size"`
}

// TargetsOrDefault returns the files to write, defaulting to CLAUDE.md.
func (r *Rule) TargetsOrDefault() []string {
	if len(r.Targets) == 0 {
		return []string{TargetClaude}
	}
	return r.Targets
}

// Writes reports whether the rule targets a given file.
func (r *Rule) Writes(target string) bool {
	for _, t := range r.TargetsOrDefault() {
		if t == target {
			return true
		}
	}
	return false
}

// TagsCSV renders tags for a form field.
func (r *Rule) TagsCSV() string { return strings.Join(r.Tags, ", ") }

func normalizeTargets(v any) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		for _, t := range RuleTargets() {
			if strings.EqualFold(s, t) {
				out = append(out, t)
				return
			}
		}
	}
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			if str, ok := item.(string); ok {
				add(str)
			}
		}
	case []string:
		for _, str := range t {
			add(str)
		}
	case string:
		for _, part := range strings.Split(t, ",") {
			add(part)
		}
	}
	return out
}

// ParseRule reads a rule document back into a Rule.
func ParseRule(name string, raw []byte) (*Rule, error) {
	f, err := skillfile.Parse(raw)
	if err != nil {
		return nil, err
	}
	r := &Rule{
		Name:        name,
		Description: strings.TrimSpace(f.Description),
		Body:        f.Body,
		Tags:        ExtractTags(f.Frontmatter),
		Size:        int64(len(raw)),
	}
	if r.Description == "" {
		r.Description = f.Summary(180)
	}
	// Targets live in the document's frontmatter, so a rule stays
	// self-describing wherever it is written out.
	if f.Extra != nil {
		r.Targets = normalizeTargets(f.Extra["targets"])
	}
	return r, nil
}

// Document renders the rule as the markdown file that represents it.
func (r *Rule) Document() []byte {
	front := skillfile.Frontmatter{
		Name:        r.Name,
		Description: strings.TrimSpace(r.Description),
		Extra:       map[string]any{},
	}
	if len(r.Targets) > 0 {
		front.Extra["targets"] = r.Targets
	}
	if len(r.Tags) > 0 {
		front.Metadata = map[string]any{"tags": append([]string(nil), r.Tags...)}
	}
	if len(front.Extra) == 0 {
		front.Extra = nil
	}
	f := &skillfile.File{Frontmatter: front, Body: r.Body}
	return f.Bytes()
}
