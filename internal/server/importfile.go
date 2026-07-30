package server

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/sidharthpunathil/circe/internal/skillfile"
	"github.com/sidharthpunathil/circe/internal/store"
)

// Importing a file you already have is the one path that needs no registry, no
// repository and no filesystem access: you have the thing, you paste it. Unlike
// the local-directory scan it therefore works on a hosted deployment.
//
// A markdown file is a skill or a rule, and only the person holding it knows
// which, so we guess from the filename and let them change it. A JSON file is
// an MCP config, which needs no guessing.

// maxUploadSize caps a single multipart request.
const maxUploadSize = 8 << 20

// What an uploaded file can become.
const (
	kindSkill  = "skill"
	kindRule   = "rule"
	kindServer = "mcp"
)

// parsedItem is one uploaded or pasted file resolved into something creatable.
type parsedItem struct {
	Kind        string // kindSkill, kindRule or kindServer
	Target      string // rules only: the file the rule merges into
	Name        string
	Description string
	Body        string
	Source      string // filename, or "pasted"
	Exists      bool
	Problem     string // why it cannot be created as-is
}

// Choice is the value of the "save it as" selector, which folds a rule's target
// into the kind so that one control covers the whole decision.
func (p parsedItem) Choice() string {
	if p.Kind == kindRule {
		target := p.Target
		if !store.ValidRuleTarget(target) {
			target = store.TargetClaude
		}
		return kindRule + ":" + target
	}
	return p.Kind
}

// IsServer reports whether this row is an MCP server, which is edited as
// configuration rather than as prose.
func (p parsedItem) IsServer() bool { return p.Kind == kindServer }

// splitChoice reads the selector back into a kind and a rule target.
func splitChoice(v string) (kind, target string) {
	kind, target, _ = strings.Cut(v, ":")
	switch kind {
	case kindRule:
		if !store.ValidRuleTarget(target) {
			target = store.TargetClaude
		}
		return kindRule, target
	case kindServer:
		return kindServer, ""
	}
	return kindSkill, ""
}

type importFileView struct {
	base
	Content string
	Parsed  []parsedItem
	Folders []*store.Bundle
	Error   string
}

func (s *Server) handleImportFile(w http.ResponseWriter, r *http.Request) {
	s.tmpl.render(w, r, http.StatusOK, "import_file", importFileView{
		base:    s.base(w, r, "import"),
		Folders: s.folderNames(),
	})
}

// parseUpload turns one file into everything it holds: a markdown file is a
// single skill or rule, a JSON file is however many servers it configures.
func parseUpload(filename string, raw []byte) []parsedItem {
	if strings.EqualFold(path.Ext(filename), ".json") || looksLikeJSON(raw) {
		return parseServerConfig(filename, raw)
	}
	return []parsedItem{parseMarkdownItem(filename, raw)}
}

// looksLikeJSON reports whether pasted text is a config rather than markdown.
func looksLikeJSON(raw []byte) bool {
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}
	return json.Valid([]byte(trimmed))
}

// parseServerConfig reads an .mcp.json into one row per server. Each row keeps
// the configuration for just that server, so they can be edited and taken
// separately.
func parseServerConfig(filename string, raw []byte) []parsedItem {
	fail := func(problem string) []parsedItem {
		return []parsedItem{{Kind: kindServer, Source: filename, Problem: problem}}
	}
	servers, err := store.ParseMCPConfig(raw)
	if err != nil {
		return fail("couldn't read this config: " + err.Error())
	}
	if len(servers) == 0 {
		return fail("no servers in this file. It needs an mcpServers block, or a plain map of name to config")
	}

	out := make([]parsedItem, 0, len(servers))
	for _, m := range servers {
		item := parsedItem{
			Kind:        kindServer,
			Name:        m.Name,
			Description: strings.TrimSpace(m.Description),
			Source:      filename,
		}
		body, err := store.RenderMCPConfig([]*store.MCPServer{m})
		if err != nil {
			item.Problem = err.Error()
		} else {
			item.Body = string(body)
		}
		if err := store.ValidateMCPServer(m); err != nil && item.Problem == "" {
			item.Problem = err.Error()
		}
		out = append(out, item)
	}
	return out
}

// parseMarkdownItem turns one markdown document into a skill or a rule, filling
// in what the file did not say. A SKILL.md written by hand often has no
// frontmatter at all, so falling back to the filename and the first heading is
// the difference between this working and not.
func parseMarkdownItem(filename string, raw []byte) parsedItem {
	out := parsedItem{Kind: kindSkill, Source: filename}
	if target, ok := store.RuleTargetForPath(filename); ok {
		// CLAUDE.md, AGENTS.md and anything in a rules folder are guidance, not
		// capability, so they start on the other setting.
		out.Kind, out.Target = kindRule, target
	}

	f, err := skillfile.Parse(raw)
	if err != nil {
		out.Problem = "couldn't read this file: " + err.Error()
		return out
	}
	out.Body = f.Body

	// Name: frontmatter, then the filename, then the first heading.
	base := strings.TrimSuffix(path.Base(filename), path.Ext(filename))
	name := strings.TrimSpace(f.Name)
	if name == "" && !strings.EqualFold(base, "SKILL") && base != "" && base != "pasted" {
		name = base
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
func (s *Server) collectUploads(r *http.Request) ([]parsedItem, error) {
	var out []parsedItem

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
				out = append(out, parsedItem{Kind: kindSkill, Source: h.Filename, Problem: "file is too large"})
				continue
			}
			file, err := h.Open()
			if err != nil {
				out = append(out, parsedItem{Kind: kindSkill, Source: h.Filename, Problem: err.Error()})
				continue
			}
			raw, err := io.ReadAll(io.LimitReader(file, maxUploadSize))
			file.Close()
			if err != nil {
				out = append(out, parsedItem{Kind: kindSkill, Source: h.Filename, Problem: err.Error()})
				continue
			}
			out = append(out, parseUpload(h.Filename, raw)...)
		}
	} else if err := r.ParseForm(); err != nil {
		return nil, err
	}

	if pasted := strings.TrimSpace(r.FormValue("content")); pasted != "" {
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			name = "pasted"
		}
		out = append(out, parseUpload(name, []byte(pasted))...)
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
		view.Error = "Paste something, or choose a file or two."
		s.tmpl.render(w, r, http.StatusBadRequest, "import_file", view)
		return
	}

	for i := range parsed {
		parsed[i].Exists = s.alreadyHave(parsed[i].Kind, parsed[i].Name)
	}
	sort.SliceStable(parsed, func(i, j int) bool { return parsed[i].Name < parsed[j].Name })

	view.Parsed = parsed
	view.Content = r.FormValue("content")
	s.tmpl.render(w, r, http.StatusOK, "import_file", view)
}

// alreadyHave reports whether the registry holds something of that kind under
// that name.
func (s *Server) alreadyHave(kind, name string) bool {
	var err error
	switch kind {
	case kindRule:
		_, err = s.store.Rule(name)
	case kindServer:
		_, err = s.store.MCPServer(name)
	default:
		_, err = s.store.Skill(name)
	}
	return err == nil
}

// handleImportFileCreate writes the confirmed items.
func (s *Server) handleImportFileCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		_ = r.ParseForm()
	}
	names := r.Form["name"]
	descs := r.Form["description"]
	bodies := r.Form["body"]
	kinds := r.Form["kind"]
	overwrite := r.FormValue("overwrite") == "1"
	folder := strings.TrimSpace(r.FormValue("folder"))

	if len(names) == 0 || len(names) != len(descs) || len(names) != len(bodies) || len(names) != len(kinds) {
		s.setFlash(w, "error", "Something went missing along the way. Try again.")
		http.Redirect(w, r, "/import/file", http.StatusSeeOther)
		return
	}

	var tally importTally
	var createdSkills []string
	landing := ""

	for i := range names {
		kind, target := splitChoice(kinds[i])
		name := store.Slugify(names[i])
		desc := strings.TrimSpace(descs[i])
		body := normalizeNewlines(bodies[i])

		if err := store.ValidateName(name); err != nil {
			tally.Failed = append(tally.Failed, names[i]+": "+err.Error())
			continue
		}
		if kind != kindServer && !overwrite && s.alreadyHave(kind, name) {
			tally.Skipped++
			continue
		}

		switch kind {
		case kindRule:
			written, err := s.saveFoundRule(store.FoundRule{
				Name: name, Description: desc, Body: body, Target: target,
			}, overwrite)
			switch {
			case err != nil:
				tally.Failed = append(tally.Failed, name+": "+err.Error())
			case written:
				tally.Rules++
				landing = "/rules/" + name
			default:
				tally.Skipped++
			}

		case kindServer:
			n, err := s.saveServerConfig(name, desc, body, overwrite)
			switch {
			case err != nil:
				tally.Failed = append(tally.Failed, name+": "+err.Error())
			case n > 0:
				tally.Servers += n
			default:
				tally.Skipped++
			}

		default:
			if msg := validateSkillForm(skillForm{Name: name, Description: desc}); msg != "" {
				tally.Failed = append(tally.Failed, name+": "+msg)
				continue
			}
			if err := s.store.SaveSkill(&store.Skill{Name: name, Description: desc, Body: body}); err != nil {
				tally.Failed = append(tally.Failed, name+": "+err.Error())
				continue
			}
			tally.Skills++
			createdSkills = append(createdSkills, name)
			landing = "/skills/" + name
		}
	}

	if folder != "" && len(createdSkills) > 0 {
		if b, err := s.store.Bundle(folder); err == nil {
			updated := *b
			updated.Skills = append([]string(nil), b.Skills...)
			for _, n := range createdSkills {
				if !updated.Has(n) {
					updated.Skills = append(updated.Skills, n)
				}
			}
			if err := s.store.SaveBundle(&updated); err != nil {
				logError("add imported skills to folder", err)
			}
		}
	}

	msg, kind := tally.message("")
	s.setFlash(w, kind, msg)

	if tally.Imported() == 1 && landing != "" {
		http.Redirect(w, r, landing, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, s.afterImport(tally, "/import/file"), http.StatusSeeOther)
}

// saveServerConfig writes the servers a config block describes, naming the
// first one from the form. Anything else in the block is imported under its own
// name rather than silently dropped. It returns how many were written.
func (s *Server) saveServerConfig(name, desc, body string, overwrite bool) (int, error) {
	servers, err := store.ParseMCPConfig([]byte(body))
	if err != nil {
		return 0, err
	}
	if len(servers) == 0 {
		return 0, fmt.Errorf("no servers in this config")
	}
	servers[0].Name = name
	if desc != "" {
		servers[0].Description = desc
	}

	written := 0
	for _, m := range servers {
		ok, err := s.saveFoundServer(m, overwrite)
		if err != nil {
			return written, err
		}
		if ok {
			written++
		}
	}
	return written, nil
}
