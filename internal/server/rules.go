package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/sidharthpunathil/skillbuilder/internal/store"
)

// ---------- rules ----------

type rulesView struct {
	base
	Rules []*store.Rule
	Sets  []ruleSetCard
	Total int
}

type ruleSetCard struct {
	Set     *store.RuleSet
	Preview []*store.Rule
	More    int
}

func (s *Server) ruleSetCards(limit int) []ruleSetCard {
	sets := s.store.RuleSets()
	if limit > 0 && len(sets) > limit {
		sets = sets[:limit]
	}
	out := make([]ruleSetCard, 0, len(sets))
	for _, set := range sets {
		rules := s.store.RuleSetRules(set)
		card := ruleSetCard{Set: set}
		const previewN = 4
		if len(rules) > previewN {
			card.Preview, card.More = rules[:previewN], len(rules)-previewN
		} else {
			card.Preview = rules
		}
		out = append(out, card)
	}
	return out
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	s.tmpl.render(w, r, http.StatusOK, "rules", rulesView{
		base:  s.base(w, r, "rules"),
		Rules: s.store.SearchRules(q),
		Sets:  s.ruleSetCards(0),
		Total: s.store.RuleCount(),
	})
}

type ruleView struct {
	base
	Rule *store.Rule
	Sets []*store.RuleSet
}

func (s *Server) handleRule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	rule, err := s.store.Rule(name)
	if err != nil {
		s.notFound(w, r, "rule")
		return
	}
	s.tmpl.render(w, r, http.StatusOK, "rule", ruleView{
		base: s.base(w, r, "rules"),
		Rule: rule,
		Sets: s.store.RuleSetsContaining(name),
	})
}

type ruleForm struct {
	Name        string
	Description string
	Body        string
	Tags        string
	Targets     []string
	Sets        []string
}

type ruleEditView struct {
	base
	IsNew    bool
	Rule     *store.Rule
	Form     ruleForm
	Error    string
	Targets  []string
	InTarget map[string]bool
	AllSets  []*store.RuleSet
	InSet    map[string]bool
}

func (s *Server) handleRuleNew(w http.ResponseWriter, r *http.Request) {
	selected := map[string]bool{}
	for _, n := range r.URL.Query()["set"] {
		if _, err := s.store.RuleSet(n); err == nil {
			selected[n] = true
		}
	}
	s.tmpl.render(w, r, http.StatusOK, "rule_edit", ruleEditView{
		base:     s.base(w, r, "rules"),
		IsNew:    true,
		Rule:     &store.Rule{},
		Form:     ruleForm{Targets: []string{store.TargetClaude}, Sets: keysOf(selected)},
		Targets:  store.RuleTargets(),
		InTarget: map[string]bool{store.TargetClaude: true},
		AllSets:  s.store.RuleSets(),
		InSet:    selected,
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
	inSet := map[string]bool{}
	var current []string
	for _, set := range s.store.RuleSetsContaining(rule.Name) {
		inSet[set.Name] = true
		current = append(current, set.Name)
	}
	s.tmpl.render(w, r, http.StatusOK, "rule_edit", ruleEditView{
		base:     s.base(w, r, "rules"),
		Rule:     rule,
		Form:     ruleForm{Name: rule.Name, Description: rule.Description, Body: rule.Body, Tags: rule.TagsCSV(), Targets: rule.TargetsOrDefault(), Sets: current},
		Targets:  store.RuleTargets(),
		InTarget: inTarget,
		AllSets:  s.store.RuleSets(),
		InSet:    inSet,
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
		Sets:        r.Form["sets"],
	}
}

func (s *Server) ruleEditViewFrom(w http.ResponseWriter, r *http.Request, f ruleForm, isNew bool, rule *store.Rule) ruleEditView {
	inTarget := map[string]bool{}
	for _, t := range f.Targets {
		inTarget[t] = true
	}
	return ruleEditView{
		base:     s.base(w, r, "rules"),
		IsNew:    isNew,
		Rule:     rule,
		Form:     f,
		Targets:  store.RuleTargets(),
		InTarget: inTarget,
		AllSets:  s.store.RuleSets(),
		InSet:    selectedSet(f.Sets),
	}
}

// syncRuleSetMembership makes the rule's folder memberships exactly `want`.
func (s *Server) syncRuleSetMembership(rule string, want []string) error {
	wanted := map[string]bool{}
	for _, n := range want {
		wanted[n] = true
	}
	for _, set := range s.store.RuleSets() {
		has, should := set.Has(rule), wanted[set.Name]
		if has == should {
			continue
		}
		updated := *set
		updated.Rules = append([]string(nil), set.Rules...)
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
		if err := s.store.SaveRuleSet(&updated); err != nil {
			return err
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
	if err := s.syncRuleSetMembership(rule.Name, form.Sets); err != nil {
		logError("update rule folders", err)
	}
	s.setFlash(w, "ok", fmt.Sprintf("Created %s.", rule.Name))
	if len(form.Sets) == 1 {
		http.Redirect(w, r, "/rulesets/"+form.Sets[0], http.StatusSeeOther)
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
	if err := s.syncRuleSetMembership(updated.Name, form.Sets); err != nil {
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

// ---------- rule folders ----------

type ruleSetView struct {
	base
	Set      *store.RuleSet
	Rules    []*store.Rule
	Rendered map[string]string
}

func (s *Server) handleRuleSet(w http.ResponseWriter, r *http.Request) {
	set, err := s.store.RuleSet(r.PathValue("name"))
	if err != nil {
		s.notFound(w, r, "rule folder")
		return
	}
	s.tmpl.render(w, r, http.StatusOK, "ruleset", ruleSetView{
		base:     s.base(w, r, "rules"),
		Set:      set,
		Rules:    s.store.RuleSetRules(set),
		Rendered: s.store.RenderRuleSet(set),
	})
}

type ruleSetForm struct {
	Name        string
	Title       string
	Description string
	Rules       []string
}

type ruleSetEditView struct {
	base
	IsNew    bool
	Set      *store.RuleSet
	Form     ruleSetForm
	AllRules []*store.Rule
	Selected map[string]bool
	Error    string
}

func (s *Server) handleRuleSetNew(w http.ResponseWriter, r *http.Request) {
	s.tmpl.render(w, r, http.StatusOK, "ruleset_edit", ruleSetEditView{
		base:     s.base(w, r, "rules"),
		IsNew:    true,
		Set:      &store.RuleSet{},
		AllRules: s.store.Rules(),
		Selected: map[string]bool{},
	})
}

func (s *Server) handleRuleSetEdit(w http.ResponseWriter, r *http.Request) {
	set, err := s.store.RuleSet(r.PathValue("name"))
	if err != nil {
		s.notFound(w, r, "rule folder")
		return
	}
	selected := map[string]bool{}
	for _, n := range set.Rules {
		selected[n] = true
	}
	s.tmpl.render(w, r, http.StatusOK, "ruleset_edit", ruleSetEditView{
		base:     s.base(w, r, "rules"),
		Set:      set,
		Form:     ruleSetForm{Name: set.Name, Title: set.Title, Description: set.Description, Rules: set.Rules},
		AllRules: s.store.Rules(),
		Selected: selected,
	})
}

func readRuleSetForm(r *http.Request) ruleSetForm {
	_ = r.ParseForm()
	return ruleSetForm{
		Name:        strings.TrimSpace(r.FormValue("name")),
		Title:       strings.TrimSpace(r.FormValue("title")),
		Description: strings.TrimSpace(r.FormValue("description")),
		Rules:       r.Form["rules"],
	}
}

func (s *Server) handleRuleSetCreate(w http.ResponseWriter, r *http.Request) {
	form := readRuleSetForm(r)
	view := ruleSetEditView{
		base: s.base(w, r, "rules"), IsNew: true, Set: &store.RuleSet{}, Form: form,
		AllRules: s.store.Rules(), Selected: selectedSet(form.Rules),
	}
	if err := store.ValidateName(form.Name); err != nil {
		view.Error = "Name: " + err.Error()
		s.tmpl.render(w, r, http.StatusBadRequest, "ruleset_edit", view)
		return
	}
	set := &store.RuleSet{Name: form.Name, Title: form.Title, Description: form.Description, Rules: form.Rules}
	if err := s.store.CreateRuleSet(set); err != nil {
		if errors.Is(err, store.ErrExists) {
			view.Error = fmt.Sprintf("There is already a folder called %q.", form.Name)
		} else {
			view.Error = err.Error()
		}
		s.tmpl.render(w, r, http.StatusBadRequest, "ruleset_edit", view)
		return
	}
	s.setFlash(w, "ok", fmt.Sprintf("Created %s.", set.Name))
	http.Redirect(w, r, "/rulesets/"+set.Name, http.StatusSeeOther)
}

func (s *Server) handleRuleSetUpdate(w http.ResponseWriter, r *http.Request) {
	current := r.PathValue("name")
	set, err := s.store.RuleSet(current)
	if err != nil {
		s.notFound(w, r, "rule folder")
		return
	}
	form := readRuleSetForm(r)
	view := ruleSetEditView{
		base: s.base(w, r, "rules"), Set: set, Form: form,
		AllRules: s.store.Rules(), Selected: selectedSet(form.Rules),
	}
	if err := store.ValidateName(form.Name); err != nil {
		view.Error = "Name: " + err.Error()
		s.tmpl.render(w, r, http.StatusBadRequest, "ruleset_edit", view)
		return
	}
	updated := &store.RuleSet{Name: form.Name, Title: form.Title, Description: form.Description, Rules: form.Rules}
	if err := s.store.SaveRuleSet(updated); err != nil {
		view.Error = err.Error()
		s.tmpl.render(w, r, http.StatusBadRequest, "ruleset_edit", view)
		return
	}
	if form.Name != current {
		if err := s.store.DeleteRuleSet(current); err != nil {
			logError("delete old rule folder", err)
		}
	}
	s.setFlash(w, "ok", fmt.Sprintf("Saved %s.", updated.Name))
	http.Redirect(w, r, "/rulesets/"+updated.Name, http.StatusSeeOther)
}

func (s *Server) handleRuleSetDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.DeleteRuleSet(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w, r, "rule folder")
			return
		}
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/rulesets/"+name, http.StatusSeeOther)
		return
	}
	s.setFlash(w, "ok", fmt.Sprintf("Deleted %s.", name))
	http.Redirect(w, r, "/rules", http.StatusSeeOther)
}

// ---------- API ----------

// ruleSetManifest is what the CLI resolves before writing a project's files.
// The rendered text is included so the CLI does not have to know how rules are
// ordered or which target each one belongs to.
type ruleSetManifest struct {
	Set      *store.RuleSet    `json:"set"`
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

func (s *Server) apiRuleSets(w http.ResponseWriter, r *http.Request) {
	sets := s.store.RuleSets()
	writeJSON(w, http.StatusOK, map[string]any{"rulesets": sets, "total": len(sets)})
}

func (s *Server) apiRuleSet(w http.ResponseWriter, r *http.Request) {
	set, err := s.store.RuleSet(r.PathValue("name"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	rules := s.store.RuleSetRules(set)
	if rules == nil {
		rules = []*store.Rule{}
	}
	writeJSON(w, http.StatusOK, ruleSetManifest{
		Set: set, Rules: rules,
		Rendered: s.store.RenderRuleSet(set),
		Registry: s.baseURL(r),
	})
}
