package server

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/sidharthpunathil/skillbuilder/internal/store"
)

// A share is the one thing a password-protected registry exposes publicly, and
// only for resources the owner explicitly shared. Everything here is read-only.

// sharePayload is what a recipient's registry fetches.
type sharePayload struct {
	Kind        string            `json:"kind"`
	Name        string            `json:"name"`
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	Skills      []string          `json:"skills"`
	Context     map[string]string `json:"context,omitempty"`
	Origin      string            `json:"origin"`
	SharedAt    time.Time         `json:"sharedAt"`
}

// isPublicPath reports whether a path bypasses authentication. Only share
// routes and the health check qualify; the prefixes are exact so that nothing
// else can be reached by crafting a URL.
func isPublicPath(p string) bool {
	switch {
	case p == "/api/v1/health":
		return true
	case p == "/login", p == "/logout":
		return true
	// Stylesheets, fonts and scripts are not secret, and the sign-in page
	// cannot render itself without them.
	case strings.HasPrefix(p, "/static/"):
		return true
	case p == "/og.png" || strings.HasPrefix(p, "/og/"):
		return true
	case strings.HasPrefix(p, "/s/"):
		return true
	case strings.HasPrefix(p, "/api/v1/share/"):
		return true
	}
	return false
}

// resolveShare looks up the share named in the URL. A miss is a flat 404 with
// no detail, so the endpoint cannot be used to probe for what exists.
func (s *Server) resolveShare(r *http.Request) (store.Share, bool) {
	return s.store.ShareByID(r.PathValue("id"))
}

func (s *Server) apiShare(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.resolveShare(r)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "no such share")
		return
	}
	out := sharePayload{
		Kind:     sh.Kind,
		Name:     sh.Name,
		Origin:   s.baseURL(r),
		SharedAt: sh.CreatedAt,
	}

	switch sh.Kind {
	case store.ShareKindFolder:
		b, err := s.store.Bundle(sh.Name)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, "not_found", "no such share")
			return
		}
		out.Title, out.Description = b.DisplayTitle(), b.Description
		out.Skills = b.Skills
		out.Context = s.store.BundleContexts(b.Name)
	case store.ShareKindSkill:
		sk, err := s.store.Skill(sh.Name)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, "not_found", "no such share")
			return
		}
		out.Title, out.Description = sk.Name, sk.Description
		out.Skills = []string{sk.Name}
	}
	writeJSON(w, http.StatusOK, out)
}

// apiShareArchive streams exactly the skills covered by the share.
func (s *Server) apiShareArchive(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.resolveShare(r)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "no such share")
		return
	}
	var names []string
	switch sh.Kind {
	case store.ShareKindFolder:
		b, err := s.store.Bundle(sh.Name)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, "not_found", "no such share")
			return
		}
		names = b.Skills
	case store.ShareKindSkill:
		names = []string{sh.Name}
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", sh.Name+".tar.gz"))
	if err := s.store.WriteSkillsTarGz(w, names); err != nil {
		logError("write share archive", err)
	}
}

// ---------- share landing page ----------

type shareView struct {
	base
	Share   store.Share
	Title   string
	Desc    string
	Skills  []*store.Skill
	Code    string
	URL     string
	Context []string
	Missing bool
}

func (s *Server) handleSharePage(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.resolveShare(r)
	b := s.base(w, r, "")
	b.Bare = true
	view := shareView{base: b}
	if !ok {
		view.Missing = true
		s.tmpl.render(w, r, http.StatusNotFound, "share", view)
		return
	}

	origin := s.baseURL(r)
	view.Share = sh
	view.Code = store.EncodeShareCode(origin, sh.ID)
	view.URL = origin + "/s/" + sh.ID

	switch sh.Kind {
	case store.ShareKindFolder:
		b, err := s.store.Bundle(sh.Name)
		if err != nil {
			view.Missing = true
			s.tmpl.render(w, r, http.StatusNotFound, "share", view)
			return
		}
		view.Title, view.Desc = b.DisplayTitle(), b.Description
		view.Skills = s.store.BundleSkills(b)
		view.Context = s.store.ContextSummary(b.Name)
	case store.ShareKindSkill:
		sk, err := s.store.Skill(sh.Name)
		if err != nil {
			view.Missing = true
			s.tmpl.render(w, r, http.StatusNotFound, "share", view)
			return
		}
		view.Title, view.Desc = sk.Name, sk.Description
		view.Skills = []*store.Skill{sk}
	}
	s.tmpl.render(w, r, http.StatusOK, "share", view)
}

// ---------- managing your own shares ----------

func (s *Server) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	kind, name, back := shareTarget(r)
	if _, err := s.store.CreateShare(kind, name); err != nil {
		s.setFlash(w, "error", err.Error())
	} else {
		s.setFlash(w, "ok", "Ready to share. Anyone with this code can take a copy until you stop sharing.")
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) handleShareRevoke(w http.ResponseWriter, r *http.Request) {
	kind, name, back := shareTarget(r)
	if err := s.store.RevokeShare(kind, name); err != nil {
		s.setFlash(w, "error", err.Error())
	} else {
		s.setFlash(w, "ok", "Sharing stopped. That code no longer works.")
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// shareTarget derives the resource from the route the request came in on.
func shareTarget(r *http.Request) (kind, name, back string) {
	name = r.PathValue("name")
	if strings.HasPrefix(r.URL.Path, "/bundles/") {
		return store.ShareKindFolder, name, "/bundles/" + name
	}
	return store.ShareKindSkill, name, "/skills/" + name
}

// ---------- importing someone else's share ----------

type importCodeView struct {
	base
	Code    string
	Error   string
	Preview *sharePayload
	Origin  string
	Exists  map[string]bool
}

func (s *Server) handleImportCode(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	view := importCodeView{base: s.base(w, r, "import"), Code: code}
	if code == "" {
		s.tmpl.render(w, r, http.StatusOK, "import_code", view)
		return
	}

	payload, origin, err := s.fetchShare(r, code)
	if err != nil {
		view.Error = err.Error()
		s.tmpl.render(w, r, http.StatusOK, "import_code", view)
		return
	}
	view.Preview, view.Origin = payload, origin
	view.Exists = map[string]bool{}
	for _, n := range payload.Skills {
		if _, err := s.store.Skill(n); err == nil {
			view.Exists[n] = true
		}
	}
	s.tmpl.render(w, r, http.StatusOK, "import_code", view)
}

// fetchShare resolves a code and reads the manifest from the origin registry.
func (s *Server) fetchShare(r *http.Request, code string) (*sharePayload, string, error) {
	origin, id, err := store.DecodeShareCode(code)
	if err != nil {
		return nil, "", err
	}

	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()

	url := origin + "/api/v1/share/" + id
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, origin, err
	}
	req.Header.Set("User-Agent", "skillbuilder")

	resp, err := shareClient.Do(req)
	if err != nil {
		return nil, origin, fmt.Errorf("couldn't reach %s; check the code, and that their registry is up", origin)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, origin, fmt.Errorf("that share is gone; whoever sent it may have stopped sharing")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, origin, fmt.Errorf("%s returned %d", origin, resp.StatusCode)
	}

	var payload sharePayload
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload); err != nil {
		return nil, origin, fmt.Errorf("%s did not return a valid share", origin)
	}
	if payload.Origin != "" {
		origin = payload.Origin
	}
	return &payload, origin, nil
}

var shareClient = &http.Client{Timeout: 60 * time.Second}

func (s *Server) handleImportCodeRun(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	code := strings.TrimSpace(r.FormValue("code"))
	overwrite := r.FormValue("overwrite") == "1"
	makeFolder := r.FormValue("folder") == "1"

	back := "/import/code?code=" + urlQueryEscape(code)

	payload, origin, err := s.fetchShare(r, code)
	if err != nil {
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/import/code", http.StatusSeeOther)
		return
	}

	_, id, _ := store.DecodeShareCode(code)
	imported, skipped, failed := s.installShared(r, origin, id, payload.Skills, overwrite)

	if makeFolder && payload.Kind == store.ShareKindFolder && len(imported) > 0 {
		name := store.Slugify(payload.Name)
		b := &store.Bundle{
			Name:        name,
			Title:       payload.Title,
			Description: payload.Description,
			Skills:      imported,
		}
		if existing, err := s.store.Bundle(name); err == nil {
			merged := *existing
			for _, n := range imported {
				if !merged.Has(n) {
					merged.Skills = append(merged.Skills, n)
				}
			}
			b = &merged
		}
		if err := s.store.SaveBundle(b); err != nil {
			logError("create folder from share", err)
		}
		for file, content := range payload.Context {
			if err := s.store.SaveBundleContext(b.Name, file, content); err != nil {
				logError("save shared context file", err)
			}
		}
	}

	var parts []string
	if n := len(imported); n > 0 {
		parts = append(parts, fmt.Sprintf("imported %d", n))
	}
	if n := len(skipped); n > 0 {
		parts = append(parts, fmt.Sprintf("skipped %d already present", n))
	}
	if n := len(failed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", n))
	}
	msg := "Nothing to import."
	kind := "ok"
	if len(parts) > 0 {
		msg = strings.Join(parts, ", ")
		msg = strings.ToUpper(msg[:1]) + msg[1:] + "."
	}
	if len(failed) > 0 {
		kind = "error"
		msg += " " + strings.Join(failed, "; ")
	}
	s.setFlash(w, kind, msg)
	if len(imported) == 0 {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/skills", http.StatusSeeOther)
}

// installShared downloads the shared archive and writes each skill in.
func (s *Server) installShared(r *http.Request, origin, id string, names []string, overwrite bool) (imported, skipped, failed []string) {
	ctx, cancel := contextWithTimeout(r, 120*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/api/v1/share/"+id+"/archive.tar.gz", nil)
	if err != nil {
		return nil, nil, []string{err.Error()}
	}
	resp, err := shareClient.Do(req)
	if err != nil {
		return nil, nil, []string{fmt.Sprintf("cannot reach %s", origin)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, []string{fmt.Sprintf("%s returned %d", origin, resp.StatusCode)}
	}

	files, err := readSharedArchive(resp.Body)
	if err != nil {
		return nil, nil, []string{err.Error()}
	}

	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	for name, content := range files {
		if len(want) > 0 && !want[name] {
			continue
		}
		if err := s.store.WriteSkillFiles(name, content, overwrite); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				skipped = append(skipped, name)
			} else {
				failed = append(failed, name+": "+err.Error())
			}
			continue
		}
		imported = append(imported, name)
	}
	return imported, skipped, failed
}

// readSharedArchive unpacks a share tarball into per-skill file maps. It is
// reading a remote archive, so entries are validated the same way the CLI
// validates downloads: no traversal, no absolute paths, no non-regular files.
func readSharedArchive(r io.Reader) (map[string]map[string][]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("share archive is not valid gzip")
	}
	defer gz.Close()

	const maxTotal = 64 << 20
	out := map[string]map[string][]byte{}
	tr := tar.NewReader(gz)
	var total int64

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("share archive is corrupt")
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		clean := path.Clean(hdr.Name)
		if strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("share archive contains an unsafe path: %s", hdr.Name)
		}
		skill, rel, ok := strings.Cut(clean, "/")
		if !ok || store.ValidateName(skill) != nil {
			continue
		}
		total += hdr.Size
		if total > maxTotal {
			return nil, fmt.Errorf("share archive is too large")
		}
		data, err := io.ReadAll(io.LimitReader(tr, 4<<20))
		if err != nil {
			return nil, err
		}
		if out[skill] == nil {
			out[skill] = map[string][]byte{}
		}
		out[skill][rel] = data
	}
	return out, nil
}
