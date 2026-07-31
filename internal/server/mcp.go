package server

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sidharthpunathil/circe/internal/mcpcatalog"
	"github.com/sidharthpunathil/circe/internal/store"
)

// routesMCP registers everything MCP. It lives here rather than in routes() so
// the whole content type is one file.
func (s *Server) routesMCP(m *http.ServeMux) {
	m.HandleFunc("GET /mcp", s.handleMCP)
	m.HandleFunc("GET /mcp/new", s.handleMCPNew)
	m.HandleFunc("POST /mcp/new", s.handleMCPCreate)
	// The catalog is a section of /mcp now rather than a page of its own, but
	// this is still where its form posts to.
	m.HandleFunc("POST /mcp/catalog/add", s.handleMCPCatalogAdd)

	m.HandleFunc("GET /mcp/{name}", s.handleMCPServer)
	m.HandleFunc("GET /mcp/{name}/edit", s.handleMCPEdit)
	m.HandleFunc("POST /mcp/{name}/edit", s.handleMCPUpdate)
	m.HandleFunc("POST /mcp/{name}/delete", s.handleMCPDelete)

	m.HandleFunc("GET /api/v1/mcps", s.apiMCPs)
	m.HandleFunc("GET /api/v1/mcps/{name}", s.apiMCP)
	m.HandleFunc("GET /api/v1/mcpsets", s.apiMCPSets)
	m.HandleFunc("GET /api/v1/mcpsets/{name}", s.apiMCPSet)
	m.HandleFunc("GET /api/v1/mcpsets/{name}/config", s.apiMCPSetConfig)
}

// ---------- mcp servers ----------

type mcpView struct {
	base
	Servers []*store.MCPServer
	Total   int
	// The catalog sits on this page, below the servers already added, so
	// browsing what exists is not a trip to somewhere else.
	Groups  []catalogGroup
	Added   map[string]bool
	Catalog int
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	s.tmpl.render(w, r, http.StatusOK, "mcp", mcpView{
		base:    s.base(w, r, "mcp"),
		Servers: s.store.SearchMCPServers(q),
		Total:   s.store.MCPCount(),
		Groups:  catalogGroups(),
		Added:   s.catalogAdded(),
		Catalog: mcpcatalog.Count(),
	})
}

type mcpDetailView struct {
	base
	Server  *store.MCPServer
	Folders []*store.Bundle
	Config  string
}

func (s *Server) handleMCPServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	m, err := s.store.MCPServer(name)
	if err != nil {
		s.notFound(w, r, "mcp server")
		return
	}
	// The snippet is the real thing, generated the same way the install does it,
	// so what you read here is what lands in the project.
	var config string
	if raw, err := store.RenderMCPConfig([]*store.MCPServer{m}); err != nil {
		logError("render mcp config", err)
	} else {
		config = string(raw)
	}
	s.tmpl.render(w, r, http.StatusOK, "mcp_detail", mcpDetailView{
		base:    s.base(w, r, "mcp"),
		Server:  m,
		Folders: s.store.BundlesContainingServer(name),
		Config:  config,
	})
}

// envRow and headerRow are one line of a repeated form field. They exist so the
// form works with JavaScript switched off: a fixed number of rows is rendered,
// blanks are ignored on save, and a couple of spares are always left over.
type envRow struct {
	Name     string
	Required bool
	Help     string
}

type headerRow struct {
	Name  string
	Value string
}

type mcpForm struct {
	Name        string
	Title       string
	Description string
	Category    string
	Transport   string
	Command     string
	Args        string
	URL         string
	Tags        string
	Env         []envRow
	Headers     []headerRow
	Folders     []string
}

// server builds the record this form describes. Blank rows are dropped here
// rather than at parse time, so the row a value came from keeps its index while
// the form is being redisplayed.
func (f mcpForm) server() *store.MCPServer {
	m := &store.MCPServer{
		Name:        f.Name,
		Title:       f.Title,
		Description: f.Description,
		Category:    f.Category,
		Transport:   f.Transport,
		Command:     strings.TrimSpace(f.Command),
		Args:        strings.Fields(f.Args),
		URL:         strings.TrimSpace(f.URL),
		Tags:        store.ParseTags(f.Tags),
	}
	for _, e := range f.Env {
		if e.Name == "" {
			continue
		}
		m.Env = append(m.Env, store.EnvVar{Name: e.Name, Required: e.Required, Help: e.Help})
	}
	for _, h := range f.Headers {
		if h.Name == "" {
			continue
		}
		if m.Headers == nil {
			m.Headers = map[string]string{}
		}
		m.Headers[h.Name] = h.Value
	}
	return m
}

type mcpEditView struct {
	base
	IsNew      bool
	Server     *store.MCPServer
	Form       mcpForm
	Error      string
	Transports []string
	Categories []string
	AllFolders []*store.Bundle
	InFolder   map[string]bool
}

func mcpFormFrom(m *store.MCPServer) mcpForm {
	f := mcpForm{
		Name:        m.Name,
		Title:       m.Title,
		Description: m.Description,
		Category:    m.Category,
		Transport:   m.TransportOrDefault(),
		Command:     m.Command,
		Args:        m.ArgsLine(),
		URL:         m.URL,
		Tags:        m.TagsCSV(),
	}
	for _, e := range m.Env {
		f.Env = append(f.Env, envRow{Name: e.Name, Required: e.Required, Help: e.Help})
	}
	names := make([]string, 0, len(m.Headers))
	for k := range m.Headers {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		f.Headers = append(f.Headers, headerRow{Name: k, Value: m.Headers[k]})
	}
	return f
}

// withSpareRows leaves blank rows at the end of the repeated field groups, and a
// minimum to start from, so there is always somewhere to type.
func (f mcpForm) withSpareRows() mcpForm {
	const spare, minimum = 2, 3
	f.Env = append(append([]envRow(nil), f.Env...), make([]envRow, spare)...)
	for len(f.Env) < minimum {
		f.Env = append(f.Env, envRow{})
	}
	f.Headers = append(append([]headerRow(nil), f.Headers...), make([]headerRow, spare)...)
	for len(f.Headers) < minimum {
		f.Headers = append(f.Headers, headerRow{})
	}
	return f
}

func (s *Server) handleMCPNew(w http.ResponseWriter, r *http.Request) {
	selected := map[string]bool{}
	for _, n := range r.URL.Query()["folder"] {
		if _, err := s.store.Bundle(n); err == nil {
			selected[n] = true
		}
	}
	form := mcpForm{Transport: store.TransportStdio, Command: "npx", Folders: keysOf(selected)}
	s.tmpl.render(w, r, http.StatusOK, "mcp_edit", mcpEditView{
		base:       s.base(w, r, "mcp"),
		IsNew:      true,
		Server:     &store.MCPServer{},
		Form:       form.withSpareRows(),
		Transports: store.MCPTransports(),
		Categories: store.MCPCategories(),
		AllFolders: s.store.Bundles(),
		InFolder:   selected,
	})
}

func (s *Server) handleMCPEdit(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.MCPServer(r.PathValue("name"))
	if err != nil {
		s.notFound(w, r, "mcp server")
		return
	}
	inFolder := map[string]bool{}
	var current []string
	for _, b := range s.store.BundlesContainingServer(m.Name) {
		inFolder[b.Name] = true
		current = append(current, b.Name)
	}
	form := mcpFormFrom(m)
	form.Folders = current
	s.tmpl.render(w, r, http.StatusOK, "mcp_edit", mcpEditView{
		base:       s.base(w, r, "mcp"),
		Server:     m,
		Form:       form.withSpareRows(),
		Transports: store.MCPTransports(),
		Categories: store.MCPCategories(),
		AllFolders: s.store.Bundles(),
		InFolder:   inFolder,
	})
}

// normalizeTransport accepts the spellings people copy out of vendor docs, such
// as "streamable-http". An empty value is left empty so the form still says to
// pick one rather than quietly settling on stdio.
func normalizeTransport(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	return store.NormalizeTransport(v)
}

func readMCPForm(r *http.Request) mcpForm {
	_ = r.ParseForm()
	f := mcpForm{
		Name:        strings.TrimSpace(r.FormValue("name")),
		Title:       strings.TrimSpace(r.FormValue("title")),
		Description: strings.TrimSpace(r.FormValue("description")),
		Category:    strings.TrimSpace(r.FormValue("category")),
		Transport:   normalizeTransport(r.FormValue("transport")),
		Command:     strings.TrimSpace(r.FormValue("command")),
		Args:        strings.TrimSpace(r.FormValue("args")),
		URL:         strings.TrimSpace(r.FormValue("url")),
		Tags:        r.FormValue("tags"),
		Folders:     r.Form["folders"],
	}

	// An unchecked checkbox sends nothing, so "required" travels as the row's
	// index instead of as a parallel array that would slip out of alignment.
	required := map[string]bool{}
	for _, i := range r.Form["env_required"] {
		required[i] = true
	}
	helps := r.Form["env_help"]
	for i, name := range r.Form["env_name"] {
		row := envRow{Name: strings.TrimSpace(name), Required: required[strconv.Itoa(i)]}
		if i < len(helps) {
			row.Help = strings.TrimSpace(helps[i])
		}
		f.Env = append(f.Env, row)
	}

	values := r.Form["header_value"]
	for i, name := range r.Form["header_name"] {
		row := headerRow{Name: strings.TrimSpace(name)}
		if i < len(values) {
			row.Value = strings.TrimSpace(values[i])
		}
		f.Headers = append(f.Headers, row)
	}
	return f
}

func (s *Server) mcpEditViewFrom(w http.ResponseWriter, r *http.Request, f mcpForm, isNew bool, m *store.MCPServer) mcpEditView {
	return mcpEditView{
		base:       s.base(w, r, "mcp"),
		IsNew:      isNew,
		Server:     m,
		Form:       f.withSpareRows(),
		Transports: store.MCPTransports(),
		Categories: store.MCPCategories(),
		AllFolders: s.store.Bundles(),
		InFolder:   selectedSet(f.Folders),
	}
}

// syncServerFolders makes the server's folder memberships exactly `want`.
func (s *Server) syncServerFolders(server string, want []string) error {
	wanted := selectedSet(want)
	for _, b := range s.store.Bundles() {
		has, should := b.HasServer(server), wanted[b.Name]
		if has == should {
			continue
		}
		updated := *b
		updated.Servers = append([]string(nil), b.Servers...)
		if should {
			updated.Servers = append(updated.Servers, server)
		} else {
			kept := updated.Servers[:0]
			for _, n := range updated.Servers {
				if n != server {
					kept = append(kept, n)
				}
			}
			updated.Servers = kept
		}
		if err := s.store.SaveBundle(&updated); err != nil {
			return fmt.Errorf("update folder %q: %w", b.Name, err)
		}
	}
	return nil
}

func (s *Server) handleMCPCreate(w http.ResponseWriter, r *http.Request) {
	form := readMCPForm(r)
	view := s.mcpEditViewFrom(w, r, form, true, &store.MCPServer{})

	m := form.server()
	if err := s.store.CreateMCPServer(m); err != nil {
		if errors.Is(err, store.ErrExists) {
			view.Error = fmt.Sprintf("There is already a server called %q.", form.Name)
		} else {
			view.Error = err.Error()
		}
		s.tmpl.render(w, r, http.StatusBadRequest, "mcp_edit", view)
		return
	}
	if err := s.syncServerFolders(m.Name, form.Folders); err != nil {
		logError("update server folders", err)
	}
	s.setFlash(w, "ok", fmt.Sprintf("Added %s.", m.Name))
	if len(form.Folders) == 1 {
		http.Redirect(w, r, "/bundles/"+form.Folders[0], http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/mcp/"+m.Name, http.StatusSeeOther)
}

func (s *Server) handleMCPUpdate(w http.ResponseWriter, r *http.Request) {
	current := r.PathValue("name")
	existing, err := s.store.MCPServer(current)
	if err != nil {
		s.notFound(w, r, "mcp server")
		return
	}
	form := readMCPForm(r)
	view := s.mcpEditViewFrom(w, r, form, false, existing)

	if form.Name != current {
		if err := s.store.RenameMCPServer(current, form.Name); err != nil {
			if errors.Is(err, store.ErrExists) {
				view.Error = fmt.Sprintf("There is already a server called %q.", form.Name)
			} else {
				view.Error = err.Error()
			}
			s.tmpl.render(w, r, http.StatusBadRequest, "mcp_edit", view)
			return
		}
	}

	updated := form.server()
	// Where it came from is not something the form edits, so carry it across
	// rather than quietly dropping it on the first save.
	updated.Homepage = existing.Homepage
	updated.Official = existing.Official
	updated.CatalogID = existing.CatalogID
	updated.Extra = existing.Extra

	if err := s.store.SaveMCPServer(updated); err != nil {
		view.Error = err.Error()
		s.tmpl.render(w, r, http.StatusBadRequest, "mcp_edit", view)
		return
	}
	if err := s.syncServerFolders(updated.Name, form.Folders); err != nil {
		logError("update server folders", err)
	}
	s.setFlash(w, "ok", fmt.Sprintf("Saved %s.", updated.Name))
	http.Redirect(w, r, "/mcp/"+updated.Name, http.StatusSeeOther)
}

func (s *Server) handleMCPDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.DeleteMCPServer(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w, r, "mcp server")
			return
		}
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/mcp/"+name, http.StatusSeeOther)
		return
	}
	s.setFlash(w, "ok", fmt.Sprintf("Deleted %s.", name))
	http.Redirect(w, r, "/mcp", http.StatusSeeOther)
}

// ---------- catalog ----------

type catalogGroup struct {
	Category string
	Entries  []mcpcatalog.Entry
}

// catalogGroups orders the catalog the way the categories are declared, then
// appends anything the catalog knows about that the list does not.
func catalogGroups() []catalogGroup {
	byCategory := mcpcatalog.ByCategory()
	var out []catalogGroup
	seen := map[string]bool{}
	for _, c := range store.MCPCategories() {
		seen[c] = true
		if entries := byCategory[c]; len(entries) > 0 {
			out = append(out, catalogGroup{Category: c, Entries: entries})
		}
	}
	var extra []string
	for c := range byCategory {
		if !seen[c] {
			extra = append(extra, c)
		}
	}
	sort.Strings(extra)
	for _, c := range extra {
		out = append(out, catalogGroup{Category: c, Entries: byCategory[c]})
	}
	return out
}

// catalogAdded marks the entries already in the registry, so the list can show
// them as taken rather than offering to add them twice.
func (s *Server) catalogAdded() map[string]bool {
	added := map[string]bool{}
	for _, m := range s.store.MCPServers() {
		if m.CatalogID != "" {
			added[m.CatalogID] = true
		}
		added[m.Name] = true
	}
	return added
}

func (s *Server) handleMCPCatalogAdd(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	ids := r.Form["entry"]
	if len(ids) == 0 {
		s.setFlash(w, "error", "Pick at least one server first.")
		http.Redirect(w, r, "/mcp#catalog", http.StatusSeeOther)
		return
	}

	var added, skipped []string
	for _, id := range ids {
		entry, ok := mcpcatalog.Get(id)
		if !ok {
			continue
		}
		if err := s.store.CreateMCPServer(entry.ToServer()); err != nil {
			if errors.Is(err, store.ErrExists) {
				skipped = append(skipped, id)
				continue
			}
			logError("add catalog server "+id, err)
			skipped = append(skipped, id)
			continue
		}
		added = append(added, id)
	}

	switch {
	case len(added) == 1 && len(skipped) == 0:
		s.setFlash(w, "ok", fmt.Sprintf("Added %s.", added[0]))
		http.Redirect(w, r, "/mcp/"+added[0], http.StatusSeeOther)
		return
	case len(added) == 0:
		s.setFlash(w, "ok", "Nothing to add. Those are already in your registry.")
	case len(skipped) == 0:
		s.setFlash(w, "ok", fmt.Sprintf("Added %d %s.", len(added), plural(len(added), "server", "servers")))
	default:
		s.setFlash(w, "ok", fmt.Sprintf("Added %d %s, skipped %d already here.",
			len(added), plural(len(added), "server", "servers"), len(skipped)))
	}
	http.Redirect(w, r, "/mcp", http.StatusSeeOther)
}

// requiredEnv collects, without duplicates, every variable a set of servers
// needs before they will start.
func requiredEnv(servers []*store.MCPServer) []store.EnvVar {
	var out []store.EnvVar
	seen := map[string]bool{}
	for _, m := range servers {
		for _, e := range m.Env {
			if seen[e.Name] {
				continue
			}
			seen[e.Name] = true
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ---------- API ----------

// Servers used to live in their own kind of folder, served at /api/v1/mcpsets.
// There is one kind of folder now, but a CLI that is already installed still
// asks at the old address, so these endpoints answer from bundles and keep
// `circe mcp <folder>` working against a freshly updated registry.

// mcpSetManifest is what the CLI resolves before writing a project's .mcp.json.
// The servers are included in full so the CLI does not have to fetch each one.
type mcpSetManifest struct {
	Name        string             `json:"name"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	UpdatedAt   time.Time          `json:"updatedAt"`
	Servers     []*store.MCPServer `json:"servers"`
}

func (s *Server) apiMCPs(w http.ResponseWriter, r *http.Request) {
	servers := s.store.SearchMCPServers(r.URL.Query().Get("q"))
	if servers == nil {
		servers = []*store.MCPServer{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"mcps": servers})
}

func (s *Server) apiMCP(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.MCPServer(r.PathValue("name"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// apiMCPSets lists the folders that carry servers. A folder with none of them
// installs nothing through `circe mcp`, so offering it would waste a choice.
func (s *Server) apiMCPSets(w http.ResponseWriter, r *http.Request) {
	sets := []*store.Bundle{}
	for _, b := range s.store.Bundles() {
		if len(b.Servers) > 0 {
			sets = append(sets, b)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"mcpsets": sets})
}

func (s *Server) apiMCPSet(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.Bundle(r.PathValue("name"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	servers := s.store.BundleServers(b)
	if servers == nil {
		servers = []*store.MCPServer{}
	}
	writeJSON(w, http.StatusOK, mcpSetManifest{
		Name:        b.Name,
		Title:       b.Title,
		Description: b.Description,
		UpdatedAt:   b.UpdatedAt,
		Servers:     servers,
	})
}

func (s *Server) apiMCPSetConfig(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.Bundle(r.PathValue("name"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	raw, err := s.store.RenderBundleMCP(b)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(raw); err != nil {
		logError("write mcp config", err)
	}
}
