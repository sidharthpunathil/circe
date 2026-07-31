package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/sidharthpunathil/circe/internal/store"
)

// ---------- rules ----------

type rulesView struct {
	base
	Rules []*store.Rule
	Total int
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	s.tmpl.render(w, r, http.StatusOK, "rules", rulesView{
		base:  s.base(w, r, "rules"),
		Rules: s.store.SearchRules(q),
		Total: s.store.RuleCount(),
	})
}

type ruleView struct {
	base
	Rule    *store.Rule
	Folders []*store.Bundle
}

func (s *Server) handleRule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	rule, err := s.store.Rule(name)
	if err != nil {
		s.notFound(w, r, "rule")
		return
	}
	s.tmpl.render(w, r, http.StatusOK, "rule", ruleView{
		base:    s.base(w, r, "rules"),
		Rule:    rule,
		Folders: s.store.BundlesContainingRule(name),
	})
}

type ruleForm struct {
	Name        string
	Description string
	Body        string
	Tags        string
	Targets     []string
	Folders     []string
}

type ruleEditView struct {
	base
	IsNew      bool
	Rule       *store.Rule
	Form       ruleForm
	Error      string
	Targets    []string
	InTarget   map[string]bool
	AllFolders []*store.Bundle
	InFolder   map[string]bool
}

func (s *Server) handleRuleNew(w http.ResponseWriter, r *http.Request) {
	selected := map[string]bool{}
	for _, n := range r.URL.Query()["folder"] {
		if _, err := s.store.Bundle(n); err == nil {
			selected[n] = true
		}
	}
	s.tmpl.render(w, r, http.StatusOK, "rule_edit", ruleEditView{
		base:       s.base(w, r, "rules"),
		IsNew:      true,
		Rule:       &store.Rule{},
		Form:       ruleForm{Targets: []string{store.TargetClaude}, Folders: keysOf(selected)},
		Targets:    store.RuleTargets(),
		InTarget:   map[string]bool{store.TargetClaude: true},
		AllFolders: s.store.Bundles(),
		InFolder:   selected,
	})
}

func (s *Server) handleRuleEdit(w http.ResponseWriter, r *http.Request) {
	rule, err := s.store.Rule(r.PathValue("name"))
	if err != nil {
		s.notFound(w, r, "rule")
		return
	}
	inTarget := map[string]bool{}
	for _, t := range rule.TargetsOrDefault() {
		inTarget[t] = true
	}
	inFolder := map[string]bool{}
	var current []string
	for _, b := range s.store.BundlesContainingRule(rule.Name) {
		inFolder[b.Name] = true
		current = append(current, b.Name)
	}
	s.tmpl.render(w, r, http.StatusOK, "rule_edit", ruleEditView{
		base: s.base(w, r, "rules"),
		Rule: rule,
		Form: ruleForm{
			Name:        rule.Name,
			Description: rule.Description,
			Body:        rule.Body,
			Tags:        rule.TagsCSV(),
			Targets:     rule.TargetsOrDefault(),
			Folders:     current,
		},
		Targets:    store.RuleTargets(),
		InTarget:   inTarget,
		AllFolders: s.store.Bundles(),
		InFolder:   inFolder,
	})
}

func readRuleForm(r *http.Request) ruleForm {
	_ = r.ParseForm()
	return ruleForm{
		Name:        strings.TrimSpace(r.FormValue("name")),
		Description: strings.TrimSpace(r.FormValue("description")),
		Body:        normalizeNewlines(r.FormValue("body")),
		Tags:        r.FormValue("tags"),
		Targets:     r.Form["targets"],
		Folders:     r.Form["folders"],
	}
}

func (s *Server) ruleEditViewFrom(w http.ResponseWriter, r *http.Request, f ruleForm, isNew bool, rule *store.Rule) ruleEditView {
	inTarget := map[string]bool{}
	for _, t := range f.Targets {
		inTarget[t] = true
	}
	return ruleEditView{
		base:       s.base(w, r, "rules"),
		IsNew:      isNew,
		Rule:       rule,
		Form:       f,
		Targets:    store.RuleTargets(),
		InTarget:   inTarget,
		AllFolders: s.store.Bundles(),
		InFolder:   selectedSet(f.Folders),
	}
}

// syncRuleFolders makes the rule's folder memberships exactly `want`.
func (s *Server) syncRuleFolders(rule string, want []string) error {
	wanted := selectedSet(want)
	for _, b := range s.store.Bundles() {
		has, should := b.HasRule(rule), wanted[b.Name]
		if has == should {
			continue
		}
		updated := *b
		updated.Rules = append([]string(nil), b.Rules...)
		if should {
			updated.Rules = append(updated.Rules, rule)
		} else {
			kept := updated.Rules[:0]
			for _, n := range updated.Rules {
				if n != rule {
					kept = append(kept, n)
				}
			}
			updated.Rules = kept
		}
		if err := s.store.SaveBundle(&updated); err != nil {
			return fmt.Errorf("update folder %q: %w", b.Name, err)
		}
	}
	return nil
}

func validRuleTargets(in []string) []string {
	var out []string
	for _, t := range in {
		if store.ValidRuleTarget(t) {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) handleRuleCreate(w http.ResponseWriter, r *http.Request) {
	form := readRuleForm(r)
	view := s.ruleEditViewFrom(w, r, form, true, &store.Rule{})

	rule := &store.Rule{
		Name:        form.Name,
		Description: form.Description,
		Body:        form.Body,
		Tags:        store.ParseTags(form.Tags),
		Targets:     validRuleTargets(form.Targets),
	}
	if err := s.store.CreateRule(rule); err != nil {
		if errors.Is(err, store.ErrExists) {
			view.Error = fmt.Sprintf("There is already a rule called %q.", form.Name)
		} else {
			view.Error = err.Error()
		}
		s.tmpl.render(w, r, http.StatusBadRequest, "rule_edit", view)
		return
	}
	if err := s.syncRuleFolders(rule.Name, form.Folders); err != nil {
		logError("update rule folders", err)
	}
	s.setFlash(w, "ok", fmt.Sprintf("Created %s.", rule.Name))
	// Going back to the folder keeps a "write several into this folder" loop tight.
	if len(form.Folders) == 1 {
		http.Redirect(w, r, "/bundles/"+form.Folders[0], http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/rules/"+rule.Name, http.StatusSeeOther)
}

func (s *Server) handleRuleUpdate(w http.ResponseWriter, r *http.Request) {
	current := r.PathValue("name")
	existing, err := s.store.Rule(current)
	if err != nil {
		s.notFound(w, r, "rule")
		return
	}
	form := readRuleForm(r)
	view := s.ruleEditViewFrom(w, r, form, false, existing)

	if form.Name != current {
		if err := s.store.RenameRule(current, form.Name); err != nil {
			if errors.Is(err, store.ErrExists) {
				view.Error = fmt.Sprintf("There is already a rule called %q.", form.Name)
			} else {
				view.Error = err.Error()
			}
			s.tmpl.render(w, r, http.StatusBadRequest, "rule_edit", view)
			return
		}
	}

	updated := &store.Rule{
		Name:        form.Name,
		Description: form.Description,
		Body:        form.Body,
		Tags:        store.ParseTags(form.Tags),
		Targets:     validRuleTargets(form.Targets),
	}
	if err := s.store.SaveRule(updated); err != nil {
		view.Error = err.Error()
		s.tmpl.render(w, r, http.StatusBadRequest, "rule_edit", view)
		return
	}
	if err := s.syncRuleFolders(updated.Name, form.Folders); err != nil {
		logError("update rule folders", err)
	}
	s.setFlash(w, "ok", fmt.Sprintf("Saved %s.", updated.Name))
	http.Redirect(w, r, "/rules/"+updated.Name, http.StatusSeeOther)
}

func (s *Server) handleRuleDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.DeleteRule(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w, r, "rule")
			return
		}
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/rules/"+name, http.StatusSeeOther)
		return
	}
	s.setFlash(w, "ok", fmt.Sprintf("Deleted %s.", name))
	http.Redirect(w, r, "/rules", http.StatusSeeOther)
}

// ---------- API ----------

// Rules used to live in their own kind of folder, served at /api/v1/rulesets.
// There is one kind of folder now, but a CLI that is already installed still
// asks at the old address, so these two endpoints answer from bundles and keep
// `circe rules <folder>` working against a freshly updated registry.

// ruleSetManifest is what the CLI resolves before writing a project's files.
// The rendered text is included so the CLI does not have to know how rules are
// ordered or which target each one belongs to.
type ruleSetManifest struct {
	Set      *store.Bundle     `json:"set"`
	Rules    []*store.Rule     `json:"rules"`
	Rendered map[string]string `json:"rendered"`
	Registry string            `json:"registry,omitempty"`
}

func (s *Server) apiRules(w http.ResponseWriter, r *http.Request) {
	rules := s.store.SearchRules(r.URL.Query().Get("q"))
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules, "total": s.store.RuleCount()})
}

func (s *Server) apiRule(w http.ResponseWriter, r *http.Request) {
	rule, err := s.store.Rule(r.PathValue("name"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

// apiRuleSets lists the folders that carry rules. A folder of nothing but
// skills is not a rule folder as far as `circe rules` is concerned, so listing
// it would only offer the user something that installs nothing.
func (s *Server) apiRuleSets(w http.ResponseWriter, r *http.Request) {
	sets := []*store.Bundle{}
	for _, b := range s.store.Bundles() {
		if len(b.Rules) > 0 {
			sets = append(sets, b)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rulesets": sets, "total": len(sets)})
}

func (s *Server) apiRuleSet(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.Bundle(r.PathValue("name"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	rules := s.store.BundleRules(b)
	if rules == nil {
		rules = []*store.Rule{}
	}
	writeJSON(w, http.StatusOK, ruleSetManifest{
		Set: b, Rules: rules,
		Rendered: s.store.RenderBundleRules(b),
		Registry: s.baseURL(r),
	})
}
