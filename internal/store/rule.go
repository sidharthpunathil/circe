package store

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"github.com/sidharthpunathil/circe/internal/model"
)

// ---------- loading ----------

// reloadRules rebuilds the rule index from the database.
func (s *Store) reloadRules() error {
	recs, err := s.all(colRules)
	if err != nil {
		return err
	}
	rules := map[string]*Rule{}
	for _, rec := range recs {
		name := rec.GetString("name")
		if ValidateName(name) != nil {
			continue
		}
		r, err := ruleFromRecord(rec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "circe: skipping rule %q: %v\n", name, err)
			continue
		}
		rules[name] = r
	}

	s.mu.Lock()
	s.rules = rules
	s.mu.Unlock()
	return nil
}

// ruleFromRecord parses a stored rule document back into a Rule.
func ruleFromRecord(rec *core.Record) (*Rule, error) {
	r, err := model.ParseRule(rec.GetString("name"), []byte(rec.GetString("source")))
	if err != nil {
		return nil, err
	}
	r.UpdatedAt = rec.GetDateTime("updated").Time()
	return r, nil
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

// SaveRule writes a rule.
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

	source := string(r.Document())

	err := s.upsert(colRules, "name", r.Name, func(rec *core.Record) {
		rec.Set("source", source)
		rec.Set("description", strings.TrimSpace(r.Description))
		rec.Set("targets", r.Targets)
		rec.Set("tags", r.Tags)
		rec.Set("size", len(source))
		setUpdated(rec, "updated", r.UpdatedAt)
	})
	if err != nil {
		return err
	}
	return s.refreshRule(r.Name)
}

// CreateRule saves a new rule, failing if the name is taken.
func (s *Store) CreateRule(r *Rule) error {
	if err := ValidateName(r.Name); err != nil {
		return err
	}
	if _, err := s.findByField(colRules, "name", r.Name); err == nil {
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
	if rec, err := s.findByField(colRules, "name", name); err == nil {
		if err := s.app.Delete(rec); err != nil {
			return err
		}
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
	if _, err := s.findByField(colRules, "name", newName); err == nil {
		return fmt.Errorf("rule %q: %w", newName, ErrExists)
	}
	rec, err := s.findByField(colRules, "name", oldName)
	if err != nil {
		return err
	}
	rec.Set("name", newName)
	if err := asStoreError(s.app.Save(rec)); err != nil {
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
	rec, err := s.findByField(colRules, "name", name)
	if err != nil {
		return err
	}
	r, err := ruleFromRecord(rec)
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
