package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

func (s *Store) rulesDir() string { return filepath.Join(s.root, "rules") }

func (s *Store) rulePath(n string) string {
	return filepath.Join(s.rulesDir(), n+".md")
}

// ---------- loading ----------

// reloadRules rebuilds the rule and rule-set indexes from disk.
func (s *Store) reloadRules() error {
	rules := map[string]*Rule{}
	if entries, err := os.ReadDir(s.rulesDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			if ValidateName(name) != nil {
				continue
			}
			r, err := s.loadRule(name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "circe: skipping rule %q: %v\n", name, err)
				continue
			}
			rules[name] = r
		}
	}

	s.mu.Lock()
	s.rules = rules
	s.mu.Unlock()
	return nil
}

func (s *Store) loadRule(name string) (*Rule, error) {
	path := s.rulePath(name)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := skillfile.Parse(raw)
	if err != nil {
		return nil, err
	}

	r := &Rule{
		Name:        name,
		Description: strings.TrimSpace(f.Description),
		Body:        f.Body,
		Tags:        extractTags(f.Frontmatter),
		Size:        int64(len(raw)),
	}
	if r.Description == "" {
		r.Description = f.Summary(180)
	}
	// Targets live in frontmatter so a rule file is self-describing on disk.
	if f.Extra != nil {
		r.Targets = normalizeTargets(f.Extra["targets"])
	}
	if info, err := os.Stat(path); err == nil {
		r.UpdatedAt = info.ModTime()
	}
	return r, nil
}

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

// ---------- queries ----------

// Rules returns every rule sorted by name.
func (s *Store) Rules() []*Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Rule, 0, len(s.rules))
	for _, r := range s.rules {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Rule returns one rule by name.
func (s *Store) Rule(name string) (*Rule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.rules[name]
	if !ok {
		return nil, fmt.Errorf("rule %q: %w", name, ErrNotFound)
	}
	return r, nil
}

// RuleCount returns how many rules are stored.
func (s *Store) RuleCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.rules)
}

// SearchRules filters rules by free text across name, description and body.
func (s *Store) SearchRules(query string) []*Rule {
	q := strings.ToLower(strings.TrimSpace(query))
	all := s.Rules()
	if q == "" {
		return all
	}
	var out []*Rule
	for _, r := range all {
		if strings.Contains(strings.ToLower(r.Name), q) ||
			strings.Contains(strings.ToLower(r.Description), q) ||
			strings.Contains(strings.ToLower(r.Body), q) {
			out = append(out, r)
		}
	}
	return out
}

// ---------- mutations ----------

// SaveRule writes a rule to disk.
func (s *Store) SaveRule(r *Rule) error {
	if err := ValidateName(r.Name); err != nil {
		return err
	}
	if strings.TrimSpace(r.Description) == "" {
		return fmt.Errorf("add a description so you can tell rules apart later")
	}
	if strings.TrimSpace(r.Body) == "" {
		return fmt.Errorf("a rule needs some content")
	}

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
	if err := os.MkdirAll(s.rulesDir(), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(s.rulePath(r.Name), f.Bytes()); err != nil {
		return err
	}
	return s.refreshRule(r.Name)
}

// CreateRule saves a new rule, failing if the name is taken.
func (s *Store) CreateRule(r *Rule) error {
	if err := ValidateName(r.Name); err != nil {
		return err
	}
	if _, err := os.Stat(s.rulePath(r.Name)); err == nil {
		return fmt.Errorf("rule %q: %w", r.Name, ErrExists)
	}
	return s.SaveRule(r)
}

// DeleteRule removes a rule and drops it from every folder.
func (s *Store) DeleteRule(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if _, err := s.Rule(name); err != nil {
		return err
	}
	if err := os.Remove(s.rulePath(name)); err != nil && !os.IsNotExist(err) {
		return err
	}

	s.mu.Lock()
	delete(s.rules, name)
	var affected []*Bundle
	for _, b := range s.bundles {
		if b.HasRule(name) {
			kept := make([]string, 0, len(b.Rules))
			for _, rn := range b.Rules {
				if rn != name {
					kept = append(kept, rn)
				}
			}
			b.Rules = kept
			affected = append(affected, b)
		}
	}
	s.mu.Unlock()

	for _, b := range affected {
		if err := s.SaveBundle(b); err != nil {
			return err
		}
	}
	return nil
}

// RenameRule moves a rule and updates folder references.
func (s *Store) RenameRule(oldName, newName string) error {
	if err := ValidateName(newName); err != nil {
		return err
	}
	if oldName == newName {
		return nil
	}
	r, err := s.Rule(oldName)
	if err != nil {
		return err
	}
	if _, err := os.Stat(s.rulePath(newName)); err == nil {
		return fmt.Errorf("rule %q: %w", newName, ErrExists)
	}
	if err := os.Rename(s.rulePath(oldName), s.rulePath(newName)); err != nil {
		return err
	}

	renamed := *r
	renamed.Name = newName
	if err := s.SaveRule(&renamed); err != nil {
		return err
	}

	s.mu.Lock()
	delete(s.rules, oldName)
	var affected []*Bundle
	for _, b := range s.bundles {
		for i, rn := range b.Rules {
			if rn == oldName {
				b.Rules[i] = newName
				affected = append(affected, b)
			}
		}
	}
	s.mu.Unlock()

	for _, b := range affected {
		if err := s.SaveBundle(b); err != nil {
			return err
		}
	}
	return s.refreshRule(newName)
}

func (s *Store) refreshRule(name string) error {
	r, err := s.loadRule(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.rules[name] = r
	s.mu.Unlock()
	return nil
}

// ---------- rules inside a folder ----------

// BundleRules resolves a folder's rules, skipping any that no longer exist.
func (s *Store) BundleRules(b *Bundle) []*Rule {
	out := make([]*Rule, 0, len(b.Rules))
	for _, name := range b.Rules {
		if r, err := s.Rule(name); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// BundlesContainingRule returns folders that include the named rule.
func (s *Store) BundlesContainingRule(rule string) []*Bundle {
	var out []*Bundle
	for _, b := range s.Bundles() {
		if b.HasRule(rule) {
			out = append(out, b)
		}
	}
	return out
}

// RenderBundleRules assembles a folder's rules into the text for each target
// file, keyed by filename. Rules are joined in folder order, so the author
// controls the sequence the agent reads them in, and a rule that only targets
// AGENTS.md never lands in CLAUDE.md.
func (s *Store) RenderBundleRules(b *Bundle) map[string]string {
	out := map[string]string{}
	for _, target := range RuleTargets() {
		var parts []string
		for _, r := range s.BundleRules(b) {
			if !r.Writes(target) {
				continue
			}
			parts = append(parts, strings.TrimRight(r.Body, "\n"))
		}
		if len(parts) > 0 {
			out[target] = strings.Join(parts, "\n\n")
		}
	}
	return out
}
