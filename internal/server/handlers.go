package server

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sidharthp/skillbuilder/internal/gitsource"
	"github.com/sidharthp/skillbuilder/internal/skillfile"
	"github.com/sidharthp/skillbuilder/internal/store"
)

// bundleCard is a bundle plus a short preview of its skills, for list pages.
type bundleCard struct {
	Bundle  *store.Bundle
	Preview []*store.Skill
	More    int
}

func (s *Server) bundleCards(limit int) []bundleCard {
	bundles := s.store.Bundles()
	if limit > 0 && len(bundles) > limit {
		bundles = bundles[:limit]
	}
	out := make([]bundleCard, 0, len(bundles))
	for _, b := range bundles {
		skills := s.store.BundleSkills(b)
		card := bundleCard{Bundle: b}
		const previewN = 4
		if len(skills) > previewN {
			card.Preview, card.More = skills[:previewN], len(skills)-previewN
		} else {
			card.Preview = skills
		}
		out = append(out, card)
	}
	return out
}

// ---------- home ----------

type homeView struct {
	base
	Skills  []*store.Skill
	Bundles []bundleCard
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	skills := s.store.Skills()
	// Surface the most recently touched skills rather than alphabetical order.
	sort.SliceStable(skills, func(i, j int) bool {
		return skills[i].UpdatedAt.After(skills[j].UpdatedAt)
	})
	if len(skills) > 6 {
		skills = skills[:6]
	}
	s.tmpl.render(w, r, http.StatusOK, "home", homeView{
		base:    s.base(w, r, "home"),
		Skills:  skills,
		Bundles: s.bundleCards(3),
	})
}

// ---------- skills ----------

type skillsView struct {
	base
	Skills  []*store.Skill
	Tags    []store.TagCount
	Tag     string
	Total   int
	Bundles []*store.Bundle
	BackURL string
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	tag := r.URL.Query().Get("tag")
	s.tmpl.render(w, r, http.StatusOK, "skills", skillsView{
		base:    s.base(w, r, "skills"),
		Skills:  s.store.Search(q, tag),
		Tags:    s.store.Tags(),
		Tag:     tag,
		Total:   s.store.SkillCount(),
		Bundles: s.store.Bundles(),
		BackURL: r.URL.RequestURI(),
	})
}

// handleSkillsBulk adds many skills to a folder in one action, so grouping an
// existing library does not mean editing each skill in turn.
func (s *Server) handleSkillsBulk(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	names := r.Form["skills"]
	target := strings.TrimSpace(r.FormValue("bundle"))
	back := r.FormValue("back")
	if back == "" || !strings.HasPrefix(back, "/") {
		back = "/skills"
	}

	if len(names) == 0 {
		s.setFlash(w, "error", "Select at least one skill first.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}

	b, err := s.store.Bundle(target)
	if err != nil {
		s.setFlash(w, "error", fmt.Sprintf("No folder named %q.", target))
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}

	updated := *b
	updated.Skills = append([]string(nil), b.Skills...)
	added := 0
	for _, n := range names {
		if updated.Has(n) {
			continue
		}
		if _, err := s.store.Skill(n); err != nil {
			continue
		}
		updated.Skills = append(updated.Skills, n)
		added++
	}
	if err := s.store.SaveBundle(&updated); err != nil {
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}

	switch {
	case added == 0:
		s.setFlash(w, "ok", fmt.Sprintf("Everything selected was already in %s.", b.DisplayTitle()))
	default:
		s.setFlash(w, "ok", fmt.Sprintf("Added %d %s to %s.", added, plural(added, "skill", "skills"), b.DisplayTitle()))
	}
	http.Redirect(w, r, "/bundles/"+b.Name, http.StatusSeeOther)
}

type skillView struct {
	base
	Skill    *store.Skill
	Bundles  []*store.Bundle
	Problems []skillfile.Problem
	Source   *gitsource.Source
}

func (s *Server) handleSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sk, err := s.store.Skill(name)
	if err != nil {
		s.notFound(w, r, "skill")
		return
	}
	f := &skillfile.File{Frontmatter: sk.Front, Body: sk.Body}
	view := skillView{
		base:     s.base(w, r, "skills"),
		Skill:    sk,
		Bundles:  s.store.BundlesContaining(name),
		Problems: f.Validate(),
	}
	if src, ok := s.loadSources()[name]; ok {
		view.Source = &src
	}
	s.tmpl.render(w, r, http.StatusOK, "skill", view)
}

// skillForm mirrors the edit form fields.
type skillForm struct {
	Name          string
	Description   string
	Body          string
	Tags          string
	License       string
	Compatibility string
	AllowedTools  string
	Bundles       []string
}

type skillEditView struct {
	base
	IsNew   bool
	Skill   *store.Skill
	Form    skillForm
	Error   string
	Bundles []*store.Bundle
	InB     map[string]bool
}

func (s *Server) handleSkillNew(w http.ResponseWriter, r *http.Request) {
	// ?bundle=X pre-selects a folder, so "new skill in this folder" lands here
	// with the membership already ticked.
	selected := map[string]bool{}
	for _, b := range r.URL.Query()["bundle"] {
		if _, err := s.store.Bundle(b); err == nil {
			selected[b] = true
		}
	}
	s.tmpl.render(w, r, http.StatusOK, "skill_edit", skillEditView{
		base:  s.base(w, r, "skills"),
		IsNew: true,
		Skill: &store.Skill{},
		// Left blank so the placeholder shows; a prefilled "# " stub just gets
		// in the way.
		Form:    skillForm{Bundles: keysOf(selected)},
		Bundles: s.store.Bundles(),
		InB:     selected,
	})
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// syncBundleMembership makes the skill's bundle memberships exactly `want`.
func (s *Server) syncBundleMembership(skill string, want []string) error {
	wanted := map[string]bool{}
	for _, n := range want {
		wanted[n] = true
	}
	for _, b := range s.store.Bundles() {
		has, should := b.Has(skill), wanted[b.Name]
		if has == should {
			continue
		}
		updated := *b
		updated.Skills = append([]string(nil), b.Skills...)
		if should {
			updated.Skills = append(updated.Skills, skill)
		} else {
			kept := updated.Skills[:0]
			for _, n := range updated.Skills {
				if n != skill {
					kept = append(kept, n)
				}
			}
			updated.Skills = kept
		}
		if err := s.store.SaveBundle(&updated); err != nil {
			return fmt.Errorf("update folder %q: %w", b.Name, err)
		}
	}
	return nil
}

func (s *Server) handleSkillEdit(w http.ResponseWriter, r *http.Request) {
	sk, err := s.store.Skill(r.PathValue("name"))
	if err != nil {
		s.notFound(w, r, "skill")
		return
	}
	inB := map[string]bool{}
	current := []string{}
	for _, b := range s.store.BundlesContaining(sk.Name) {
		inB[b.Name] = true
		current = append(current, b.Name)
	}
	s.tmpl.render(w, r, http.StatusOK, "skill_edit", skillEditView{
		base:    s.base(w, r, "skills"),
		Skill:   sk,
		Bundles: s.store.Bundles(),
		InB:     inB,
		Form: skillForm{
			Bundles:       current,
			Name:          sk.Name,
			Description:   sk.Description,
			Body:          sk.Body,
			Tags:          sk.TagsCSV(),
			License:       sk.Front.License,
			Compatibility: sk.Front.Compatibility,
			AllowedTools:  strings.Join(sk.Front.AllowedTools.Items, ", "),
		},
	})
}

// readSkillForm pulls the form fields and applies them onto sk.
func readSkillForm(r *http.Request) skillForm {
	_ = r.ParseForm()
	return skillForm{
		Bundles:       r.Form["bundles"],
		Name:          strings.TrimSpace(r.FormValue("name")),
		Description:   strings.TrimSpace(r.FormValue("description")),
		Body:          normalizeNewlines(r.FormValue("body")),
		Tags:          r.FormValue("tags"),
		License:       strings.TrimSpace(r.FormValue("license")),
		Compatibility: strings.TrimSpace(r.FormValue("compatibility")),
		AllowedTools:  strings.TrimSpace(r.FormValue("allowed_tools")),
	}
}

func normalizeNewlines(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// applyForm writes form values onto a skill, preserving unmodeled frontmatter.
func applyForm(sk *store.Skill, f skillForm) {
	sk.Name = f.Name
	sk.Description = f.Description
	sk.Body = f.Body
	sk.Tags = store.ParseTags(f.Tags)
	sk.Front.License = f.License
	sk.Front.Compatibility = f.Compatibility

	items := splitCSV(f.AllowedTools)
	sk.Front.AllowedTools.Items = items
	// Reproduce the comma-separated scalar form, which is how real skills
	// author this field.
	sk.Front.AllowedTools.Inline = len(items) > 0
}

func splitCSV(s string) []string {
	var out []string
	depth := 0
	var cur strings.Builder
	for _, r := range s {
		switch r {
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				if v := strings.TrimSpace(cur.String()); v != "" {
					out = append(out, v)
				}
				cur.Reset()
				continue
			}
		}
		cur.WriteRune(r)
	}
	if v := strings.TrimSpace(cur.String()); v != "" {
		out = append(out, v)
	}
	return out
}

// validateSkillForm returns a user-facing message for the first problem found.
func validateSkillForm(f skillForm) string {
	if err := store.ValidateName(f.Name); err != nil {
		return "Name: " + err.Error()
	}
	if f.Description == "" {
		return "A description is required — it is how the agent decides when to use the skill."
	}
	if utf8.RuneCountInString(f.Description) > skillfile.MaxDescriptionLen {
		return fmt.Sprintf("Description must be %d characters or fewer.", skillfile.MaxDescriptionLen)
	}
	if strings.ContainsAny(f.Description, "<>") {
		return "Description must not contain < or > characters."
	}
	if utf8.RuneCountInString(f.Compatibility) > skillfile.MaxCompatibilityLen {
		return fmt.Sprintf("Compatibility must be %d characters or fewer.", skillfile.MaxCompatibilityLen)
	}
	return ""
}

func (s *Server) handleSkillCreate(w http.ResponseWriter, r *http.Request) {
	form := readSkillForm(r)
	view := skillEditView{
		base:    s.base(w, r, "skills"),
		IsNew:   true,
		Skill:   &store.Skill{},
		Form:    form,
		Bundles: s.store.Bundles(),
		InB:     selectedSet(form.Bundles),
	}

	if msg := validateSkillForm(form); msg != "" {
		view.Error = msg
		s.tmpl.render(w, r, http.StatusBadRequest, "skill_edit", view)
		return
	}

	sk := &store.Skill{}
	applyForm(sk, form)
	if err := s.store.CreateSkill(sk); err != nil {
		if errors.Is(err, store.ErrExists) {
			view.Error = fmt.Sprintf("A skill named %q already exists.", form.Name)
		} else {
			view.Error = err.Error()
		}
		s.tmpl.render(w, r, http.StatusBadRequest, "skill_edit", view)
		return
	}

	if err := s.syncBundleMembership(sk.Name, form.Bundles); err != nil {
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/skills/"+sk.Name, http.StatusSeeOther)
		return
	}

	msg := fmt.Sprintf("Created %s.", sk.Name)
	if n := len(form.Bundles); n > 0 {
		msg = fmt.Sprintf("Created %s and added it to %d %s.", sk.Name, n, plural(n, "folder", "folders"))
	}
	s.setFlash(w, "ok", msg)
	// Returning to the folder keeps a "create several into this folder" loop tight.
	if len(form.Bundles) == 1 {
		http.Redirect(w, r, "/bundles/"+form.Bundles[0], http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/skills/"+sk.Name, http.StatusSeeOther)
}

func (s *Server) handleSkillUpdate(w http.ResponseWriter, r *http.Request) {
	current := r.PathValue("name")
	sk, err := s.store.Skill(current)
	if err != nil {
		s.notFound(w, r, "skill")
		return
	}

	form := readSkillForm(r)
	view := skillEditView{
		base:    s.base(w, r, "skills"),
		Skill:   sk,
		Form:    form,
		Bundles: s.store.Bundles(),
		InB:     selectedSet(form.Bundles),
	}
	if msg := validateSkillForm(form); msg != "" {
		view.Error = msg
		s.tmpl.render(w, r, http.StatusBadRequest, "skill_edit", view)
		return
	}

	// A changed name means moving the directory and repointing bundles.
	if form.Name != current {
		if err := s.store.RenameSkill(current, form.Name); err != nil {
			if errors.Is(err, store.ErrExists) {
				view.Error = fmt.Sprintf("A skill named %q already exists.", form.Name)
			} else {
				view.Error = err.Error()
			}
			s.tmpl.render(w, r, http.StatusBadRequest, "skill_edit", view)
			return
		}
		if sk, err = s.store.Skill(form.Name); err != nil {
			s.notFound(w, r, "skill")
			return
		}
	}

	updated := *sk
	applyForm(&updated, form)
	if err := s.store.SaveSkill(&updated); err != nil {
		view.Error = err.Error()
		s.tmpl.render(w, r, http.StatusBadRequest, "skill_edit", view)
		return
	}

	if err := s.syncBundleMembership(updated.Name, form.Bundles); err != nil {
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/skills/"+updated.Name, http.StatusSeeOther)
		return
	}
	s.setFlash(w, "ok", fmt.Sprintf("Saved %s.", updated.Name))
	http.Redirect(w, r, "/skills/"+updated.Name, http.StatusSeeOther)
}

func (s *Server) handleSkillDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.DeleteSkill(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w, r, "skill")
			return
		}
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/skills/"+name, http.StatusSeeOther)
		return
	}
	s.setFlash(w, "ok", fmt.Sprintf("Deleted %s.", name))
	http.Redirect(w, r, "/skills", http.StatusSeeOther)
}

func (s *Server) handleSkillDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := s.store.Skill(name); err != nil {
		s.notFound(w, r, "skill")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".skill"))
	if err := s.store.WriteSkillZip(w, name); err != nil {
		// Headers are already sent; log rather than trying to write an error page.
		logError("write skill zip", err)
	}
}

// ---------- bundled files ----------

type fileEditView struct {
	base
	Skill   *store.Skill
	Path    string
	Content string
	Action  string
	IsNew   bool
	Binary  bool
	Size    int64
	Error   string
}

func (s *Server) handleFileNew(w http.ResponseWriter, r *http.Request) {
	sk, err := s.store.Skill(r.PathValue("name"))
	if err != nil {
		s.notFound(w, r, "skill")
		return
	}
	s.tmpl.render(w, r, http.StatusOK, "file_edit", fileEditView{
		base:   s.base(w, r, "skills"),
		Skill:  sk,
		IsNew:  true,
		Action: fmt.Sprintf("/skills/%s/files/new", sk.Name),
	})
}

func (s *Server) handleFileCreate(w http.ResponseWriter, r *http.Request) {
	sk, err := s.store.Skill(r.PathValue("name"))
	if err != nil {
		s.notFound(w, r, "skill")
		return
	}
	path := strings.TrimSpace(r.FormValue("path"))
	content := normalizeNewlines(r.FormValue("content"))

	view := fileEditView{
		base:    s.base(w, r, "skills"),
		Skill:   sk,
		IsNew:   true,
		Path:    path,
		Content: content,
		Action:  fmt.Sprintf("/skills/%s/files/new", sk.Name),
	}
	if err := s.store.WriteFile(sk.Name, path, []byte(content)); err != nil {
		view.Error = err.Error()
		s.tmpl.render(w, r, http.StatusBadRequest, "file_edit", view)
		return
	}
	s.setFlash(w, "ok", fmt.Sprintf("Created %s.", path))
	http.Redirect(w, r, "/skills/"+sk.Name, http.StatusSeeOther)
}

func (s *Server) handleFileEdit(w http.ResponseWriter, r *http.Request) {
	sk, err := s.store.Skill(r.PathValue("name"))
	if err != nil {
		s.notFound(w, r, "skill")
		return
	}
	path := r.PathValue("path")

	// The delete action is routed through this path pattern.
	if strings.HasSuffix(path, "/delete") {
		s.notFound(w, r, "file")
		return
	}

	raw, err := s.store.ReadFile(sk.Name, path)
	if err != nil {
		s.notFound(w, r, "file")
		return
	}

	view := fileEditView{
		base:   s.base(w, r, "skills"),
		Skill:  sk,
		Path:   path,
		Action: fmt.Sprintf("/skills/%s/files/%s", sk.Name, path),
		Size:   int64(len(raw)),
	}
	if isBinary(raw) {
		view.Binary = true
	} else {
		view.Content = string(raw)
	}
	s.tmpl.render(w, r, http.StatusOK, "file_edit", view)
}

func (s *Server) handleFileSave(w http.ResponseWriter, r *http.Request) {
	sk, err := s.store.Skill(r.PathValue("name"))
	if err != nil {
		s.notFound(w, r, "skill")
		return
	}
	path := r.PathValue("path")

	if deletePath, ok := strings.CutSuffix(path, "/delete"); ok {
		if err := s.store.DeleteFile(sk.Name, deletePath); err != nil {
			s.setFlash(w, "error", err.Error())
		} else {
			s.setFlash(w, "ok", fmt.Sprintf("Deleted %s.", deletePath))
		}
		http.Redirect(w, r, "/skills/"+sk.Name, http.StatusSeeOther)
		return
	}

	content := normalizeNewlines(r.FormValue("content"))
	if err := s.store.WriteFile(sk.Name, path, []byte(content)); err != nil {
		s.tmpl.render(w, r, http.StatusBadRequest, "file_edit", fileEditView{
			base:    s.base(w, r, "skills"),
			Skill:   sk,
			Path:    path,
			Content: content,
			Action:  fmt.Sprintf("/skills/%s/files/%s", sk.Name, path),
			Error:   err.Error(),
		})
		return
	}
	s.setFlash(w, "ok", fmt.Sprintf("Saved %s.", path))
	http.Redirect(w, r, "/skills/"+sk.Name, http.StatusSeeOther)
}

// isBinary reports whether data looks like non-text content.
func isBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	n := min(len(data), 8000)
	for _, b := range data[:n] {
		if b == 0 {
			return true
		}
	}
	return !utf8.Valid(data[:n])
}

// ---------- bundles ----------

type bundlesView struct {
	base
	Bundles []bundleCard
}

func (s *Server) handleBundles(w http.ResponseWriter, r *http.Request) {
	s.tmpl.render(w, r, http.StatusOK, "bundles", bundlesView{
		base:    s.base(w, r, "bundles"),
		Bundles: s.bundleCards(0),
	})
}

type bundleView struct {
	base
	Bundle         *store.Bundle
	Skills         []*store.Skill
	Size           int64
	MarketplaceURL string
}

func (s *Server) handleBundle(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.Bundle(r.PathValue("name"))
	if err != nil {
		s.notFound(w, r, "bundle")
		return
	}
	skills := s.store.BundleSkills(b)
	var size int64
	for _, sk := range skills {
		size += sk.Size
	}
	s.tmpl.render(w, r, http.StatusOK, "bundle", bundleView{
		base:           s.base(w, r, "bundles"),
		Bundle:         b,
		Skills:         skills,
		Size:           size,
		MarketplaceURL: s.baseURL(r),
	})
}

type bundleForm struct {
	Name        string
	Title       string
	Description string
	Skills      []string
}

type bundleEditView struct {
	base
	IsNew     bool
	Bundle    *store.Bundle
	Form      bundleForm
	AllSkills []*store.Skill
	Selected  map[string]bool
	Error     string
}

func (s *Server) handleBundleNew(w http.ResponseWriter, r *http.Request) {
	s.tmpl.render(w, r, http.StatusOK, "bundle_edit", bundleEditView{
		base:      s.base(w, r, "bundles"),
		IsNew:     true,
		Bundle:    &store.Bundle{},
		AllSkills: s.store.Skills(),
		Selected:  map[string]bool{},
	})
}

func (s *Server) handleBundleEdit(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.Bundle(r.PathValue("name"))
	if err != nil {
		s.notFound(w, r, "bundle")
		return
	}
	selected := map[string]bool{}
	for _, n := range b.Skills {
		selected[n] = true
	}
	s.tmpl.render(w, r, http.StatusOK, "bundle_edit", bundleEditView{
		base:   s.base(w, r, "bundles"),
		Bundle: b,
		Form: bundleForm{
			Name:        b.Name,
			Title:       b.Title,
			Description: b.Description,
			Skills:      b.Skills,
		},
		AllSkills: s.store.Skills(),
		Selected:  selected,
	})
}

func readBundleForm(r *http.Request) bundleForm {
	_ = r.ParseForm()
	return bundleForm{
		Name:        strings.TrimSpace(r.FormValue("name")),
		Title:       strings.TrimSpace(r.FormValue("title")),
		Description: strings.TrimSpace(r.FormValue("description")),
		Skills:      r.Form["skills"],
	}
}

func (s *Server) bundleEditViewFrom(w http.ResponseWriter, r *http.Request, form bundleForm, isNew bool, b *store.Bundle) bundleEditView {
	selected := map[string]bool{}
	for _, n := range form.Skills {
		selected[n] = true
	}
	return bundleEditView{
		base:      s.base(w, r, "bundles"),
		IsNew:     isNew,
		Bundle:    b,
		Form:      form,
		AllSkills: s.store.Skills(),
		Selected:  selected,
	}
}

func (s *Server) handleBundleCreate(w http.ResponseWriter, r *http.Request) {
	form := readBundleForm(r)
	view := s.bundleEditViewFrom(w, r, form, true, &store.Bundle{})

	if err := store.ValidateName(form.Name); err != nil {
		view.Error = "Name: " + err.Error()
		s.tmpl.render(w, r, http.StatusBadRequest, "bundle_edit", view)
		return
	}
	b := &store.Bundle{
		Name:        form.Name,
		Title:       form.Title,
		Description: form.Description,
		Skills:      form.Skills,
	}
	if err := s.store.CreateBundle(b); err != nil {
		if errors.Is(err, store.ErrExists) {
			view.Error = fmt.Sprintf("A bundle named %q already exists.", form.Name)
		} else {
			view.Error = err.Error()
		}
		s.tmpl.render(w, r, http.StatusBadRequest, "bundle_edit", view)
		return
	}
	s.setFlash(w, "ok", fmt.Sprintf("Created bundle %s.", b.Name))
	http.Redirect(w, r, "/bundles/"+b.Name, http.StatusSeeOther)
}

func (s *Server) handleBundleUpdate(w http.ResponseWriter, r *http.Request) {
	current := r.PathValue("name")
	b, err := s.store.Bundle(current)
	if err != nil {
		s.notFound(w, r, "bundle")
		return
	}
	form := readBundleForm(r)
	view := s.bundleEditViewFrom(w, r, form, false, b)

	if err := store.ValidateName(form.Name); err != nil {
		view.Error = "Name: " + err.Error()
		s.tmpl.render(w, r, http.StatusBadRequest, "bundle_edit", view)
		return
	}
	// Renaming a bundle is a create-then-delete since the filename is the key.
	if form.Name != current {
		if _, err := s.store.Bundle(form.Name); err == nil {
			view.Error = fmt.Sprintf("A bundle named %q already exists.", form.Name)
			s.tmpl.render(w, r, http.StatusBadRequest, "bundle_edit", view)
			return
		}
	}

	updated := &store.Bundle{
		Name:        form.Name,
		Title:       form.Title,
		Description: form.Description,
		Skills:      form.Skills,
	}
	if err := s.store.SaveBundle(updated); err != nil {
		view.Error = err.Error()
		s.tmpl.render(w, r, http.StatusBadRequest, "bundle_edit", view)
		return
	}
	if form.Name != current {
		if err := s.store.DeleteBundle(current); err != nil {
			logError("delete old bundle", err)
		}
	}

	s.setFlash(w, "ok", fmt.Sprintf("Saved bundle %s.", updated.Name))
	http.Redirect(w, r, "/bundles/"+updated.Name, http.StatusSeeOther)
}

func (s *Server) handleBundleDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.DeleteBundle(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w, r, "bundle")
			return
		}
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/bundles/"+name, http.StatusSeeOther)
		return
	}
	s.setFlash(w, "ok", fmt.Sprintf("Deleted bundle %s.", name))
	http.Redirect(w, r, "/bundles", http.StatusSeeOther)
}

func (s *Server) handleBundleDownload(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.Bundle(r.PathValue("name"))
	if err != nil {
		s.notFound(w, r, "bundle")
		return
	}
	manifest, err := s.marketplaceJSON(s.baseURL(r), b)
	if err != nil {
		logError("build manifest", err)
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", b.Name+".zip"))
	if err := s.store.WriteBundleZip(w, b, manifest); err != nil {
		logError("write bundle zip", err)
	}
}

// ---------- import ----------

type importView struct {
	base
	Source     string
	Candidates []store.ImportCandidate
	Scanned    bool
	Error      string
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	src := strings.TrimSpace(r.URL.Query().Get("src"))
	if src == "" {
		src = store.DefaultImportSource()
	}
	view := importView{base: s.base(w, r, "import"), Source: src}

	candidates, err := s.store.Scan(src)
	if err != nil {
		view.Error = err.Error()
	} else {
		view.Candidates = candidates
		view.Scanned = true
	}
	s.tmpl.render(w, r, http.StatusOK, "import", view)
}

func (s *Server) handleImportRun(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	src := strings.TrimSpace(r.FormValue("src"))
	names := r.Form["names"]
	overwrite := r.FormValue("overwrite") == "1"

	if len(names) == 0 {
		s.setFlash(w, "error", "Select at least one skill to import.")
		http.Redirect(w, r, "/import?src="+urlQueryEscape(src), http.StatusSeeOther)
		return
	}

	res, err := s.store.Import(src, names, overwrite)
	if err != nil {
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/import?src="+urlQueryEscape(src), http.StatusSeeOther)
		return
	}

	var parts []string
	if n := len(res.Imported); n > 0 {
		parts = append(parts, fmt.Sprintf("imported %d", n))
	}
	if n := len(res.Skipped); n > 0 {
		parts = append(parts, fmt.Sprintf("skipped %d already present", n))
	}
	if n := len(res.Failed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", n))
	}
	msg := "Nothing to do."
	if len(parts) > 0 {
		msg = strings.ToUpper(parts[0][:1]) + parts[0][1:]
		if len(parts) > 1 {
			msg = strings.Join(parts, ", ")
			msg = strings.ToUpper(msg[:1]) + msg[1:]
		}
		msg += "."
	}
	kind := "ok"
	if len(res.Failed) > 0 {
		kind = "error"
		msg += " " + strings.Join(res.Failed, "; ")
	}
	s.setFlash(w, kind, msg)
	http.Redirect(w, r, "/skills", http.StatusSeeOther)
}

// selectedSet turns a list of names into a lookup for template checkboxes.
func selectedSet(names []string) map[string]bool {
	out := map[string]bool{}
	for _, n := range names {
		out[n] = true
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
