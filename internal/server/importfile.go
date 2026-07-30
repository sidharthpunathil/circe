package server

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/sidharthpunathil/skillbuilder/internal/skillfile"
	"github.com/sidharthpunathil/skillbuilder/internal/store"
)

// Importing a raw markdown file is the one path that needs no registry, no
// repository and no filesystem access — you have a SKILL.md, you paste it.
// Unlike the local-directory scan it therefore works on a hosted deployment.

// maxUploadSize caps a single multipart request.
const maxUploadSize = 8 << 20

// parsedSkill is one markdown file resolved into something creatable.
type parsedSkill struct {
	Name        string
	Description string
	Body        string
	Front       skillfile.Frontmatter
	Source      string // filename, or "pasted"
	Exists      bool
	Problem     string // why it cannot be created as-is
}

type importFileView struct {
	base
	Content string
	Parsed  []parsedSkill
	Folders []*store.Bundle
	Error   string
}

func (s *Server) handleImportFile(w http.ResponseWriter, r *http.Request) {
	s.tmpl.render(w, r, http.StatusOK, "import_file", importFileView{
		base:    s.base(w, r, "import"),
		Folders: s.folderNames(),
	})
}

// parseMarkdownSkill turns one markdown document into a skill, filling in what
// the file did not say. A SKILL.md written by hand often has no frontmatter at
// all, so falling back to the filename and the first heading is the difference
// between this working and not.
func parseMarkdownSkill(filename string, raw []byte) parsedSkill {
	out := parsedSkill{Source: filename}

	f, err := skillfile.Parse(raw)
	if err != nil {
		out.Problem = "couldn't read this file: " + err.Error()
		return out
	}
	out.Front, out.Body = f.Frontmatter, f.Body

	// Name: frontmatter, then filename, then the first heading.
	name := strings.TrimSpace(f.Name)
	if name == "" {
		base := strings.TrimSuffix(path.Base(filename), path.Ext(filename))
		if !strings.EqualFold(base, "SKILL") && base != "" && base != "pasted" {
			name = base
		}
	}
	if name == "" {
		name = firstHeading(f.Body)
	}
	out.Name = store.Slugify(name)

	// Description: frontmatter, then the first meaningful line of the body.
	out.Description = strings.TrimSpace(f.Description)
	if out.Description == "" {
		out.Description = f.Summary(300)
	}

	switch {
	case out.Name == "":
		out.Problem = "we couldn't work out a name; add one below, or give the file a # heading"
	case store.ValidateName(out.Name) != nil:
		out.Problem = store.ValidateName(out.Name).Error()
	case out.Description == "":
		out.Problem = "we couldn't work out a description; add one below"
	case strings.ContainsAny(out.Description, "<>"):
		out.Problem = "descriptions can't contain < or > characters"
	case strings.TrimSpace(out.Body) == "":
		out.Problem = "there are no instructions in this file"
	}
	return out
}

// firstHeading returns the first markdown heading, used to name an untitled file.
func firstHeading(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			if h := strings.TrimSpace(strings.TrimLeft(line, "#")); h != "" {
				return h
			}
		}
	}
	return ""
}

// collectUploads reads pasted text and any attached files into one list.
func (s *Server) collectUploads(r *http.Request) ([]parsedSkill, error) {
	var out []parsedSkill

	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(maxUploadSize); err != nil {
			return nil, fmt.Errorf("upload too large or malformed (limit %d MiB)", maxUploadSize>>20)
		}
		var headers []*multipart.FileHeader
		if r.MultipartForm != nil {
			headers = r.MultipartForm.File["files"]
		}
		for _, h := range headers {
			if h.Size > maxUploadSize {
				out = append(out, parsedSkill{Source: h.Filename, Problem: "file is too large"})
				continue
			}
			file, err := h.Open()
			if err != nil {
				out = append(out, parsedSkill{Source: h.Filename, Problem: err.Error()})
				continue
			}
			raw, err := io.ReadAll(io.LimitReader(file, maxUploadSize))
			file.Close()
			if err != nil {
				out = append(out, parsedSkill{Source: h.Filename, Problem: err.Error()})
				continue
			}
			out = append(out, parseMarkdownSkill(h.Filename, raw))
		}
	} else if err := r.ParseForm(); err != nil {
		return nil, err
	}

	if pasted := strings.TrimSpace(r.FormValue("content")); pasted != "" {
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			name = "pasted"
		}
		out = append(out, parseMarkdownSkill(name, []byte(pasted)))
	}
	return out, nil
}

// handleImportFilePreview parses without creating, so nothing is written on the
// strength of a guess about the file's name.
func (s *Server) handleImportFilePreview(w http.ResponseWriter, r *http.Request) {
	view := importFileView{base: s.base(w, r, "import"), Folders: s.folderNames()}

	parsed, err := s.collectUploads(r)
	if err != nil {
		view.Error = err.Error()
		s.tmpl.render(w, r, http.StatusBadRequest, "import_file", view)
		return
	}
	if len(parsed) == 0 {
		view.Error = "Paste a skill, or choose a file or two."
		s.tmpl.render(w, r, http.StatusBadRequest, "import_file", view)
		return
	}

	for i := range parsed {
		if _, err := s.store.Skill(parsed[i].Name); err == nil {
			parsed[i].Exists = true
		}
	}
	sort.SliceStable(parsed, func(i, j int) bool { return parsed[i].Name < parsed[j].Name })

	view.Parsed = parsed
	view.Content = r.FormValue("content")
	s.tmpl.render(w, r, http.StatusOK, "import_file", view)
}

// handleImportFileCreate writes the confirmed skills.
func (s *Server) handleImportFileCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		_ = r.ParseForm()
	}
	names := r.Form["name"]
	descs := r.Form["description"]
	bodies := r.Form["body"]
	overwrite := r.FormValue("overwrite") == "1"
	folder := strings.TrimSpace(r.FormValue("folder"))

	if len(names) == 0 || len(names) != len(descs) || len(names) != len(bodies) {
		s.setFlash(w, "error", "Something went missing along the way. Try again.")
		http.Redirect(w, r, "/import/file", http.StatusSeeOther)
		return
	}

	var created, skipped, failed []string
	for i := range names {
		name := store.Slugify(names[i])
		sk := &store.Skill{
			Name:        name,
			Description: strings.TrimSpace(descs[i]),
			Body:        normalizeNewlines(bodies[i]),
		}
		if msg := validateSkillForm(skillForm{Name: sk.Name, Description: sk.Description}); msg != "" {
			failed = append(failed, name+": "+msg)
			continue
		}

		_, exists := s.store.Skill(sk.Name)
		if exists == nil && !overwrite {
			skipped = append(skipped, name)
			continue
		}
		if err := s.store.SaveSkill(sk); err != nil {
			failed = append(failed, name+": "+err.Error())
			continue
		}
		created = append(created, name)
	}

	if folder != "" && len(created) > 0 {
		if b, err := s.store.Bundle(folder); err == nil {
			updated := *b
			updated.Skills = append([]string(nil), b.Skills...)
			for _, n := range created {
				if !updated.Has(n) {
					updated.Skills = append(updated.Skills, n)
				}
			}
			if err := s.store.SaveBundle(&updated); err != nil {
				logError("add imported skills to folder", err)
			}
		}
	}

	var parts []string
	if n := len(created); n > 0 {
		parts = append(parts, fmt.Sprintf("imported %d", n))
	}
	if n := len(skipped); n > 0 {
		parts = append(parts, fmt.Sprintf("skipped %d already present", n))
	}
	if n := len(failed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", n))
	}
	msg, kind := "Nothing to import.", "ok"
	if len(parts) > 0 {
		msg = strings.Join(parts, ", ")
		msg = strings.ToUpper(msg[:1]) + msg[1:] + "."
	}
	if len(failed) > 0 {
		kind = "error"
		msg += " " + strings.Join(failed, "; ")
	}
	s.setFlash(w, kind, msg)

	if len(created) == 1 {
		http.Redirect(w, r, "/skills/"+created[0], http.StatusSeeOther)
		return
	}
	if len(created) == 0 {
		http.Redirect(w, r, "/import/file", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/skills", http.StatusSeeOther)
}
