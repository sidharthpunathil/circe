package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/sidharthp/skillbuilder/internal/api"
	"github.com/sidharthp/skillbuilder/internal/store"
)

func logError(what string, err error) {
	if err != nil {
		log.Printf("skillbuilder: %s: %v", what, err)
	}
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

// toAPISkill converts a stored skill to its wire form. Body and bundle
// membership are only included on the single-skill endpoint, so lists stay small.
func toAPISkill(sk *store.Skill, full bool) api.Skill {
	out := api.Skill{
		Name:        sk.Name,
		Description: sk.Description,
		Tags:        sk.Tags,
		Size:        sk.Size,
		UpdatedAt:   sk.UpdatedAt,
	}
	for _, f := range sk.Files {
		out.Files = append(out.Files, api.FileRef{Path: f.Path, Size: f.Size})
	}
	if full {
		out.Body = sk.Body
	}
	return out
}

func toAPIBundle(b *store.Bundle) api.Bundle {
	return api.Bundle{
		Name:        b.Name,
		Title:       b.Title,
		Description: b.Description,
		Skills:      b.Skills,
		UpdatedAt:   b.UpdatedAt,
	}
}

func (s *Server) apiHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": api.Version,
		"skills":  s.store.SkillCount(),
		"bundles": s.store.BundleCount(),
	})
}

func (s *Server) apiSkills(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	skills := s.store.Search(q.Get("q"), q.Get("tag"))
	out := api.SkillListResponse{Skills: make([]api.Skill, 0, len(skills)), Total: s.store.SkillCount()}
	for _, sk := range skills {
		out.Skills = append(out.Skills, toAPISkill(sk, false))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiSkill(w http.ResponseWriter, r *http.Request) {
	sk, err := s.store.Skill(r.PathValue("name"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	out := toAPISkill(sk, true)
	for _, b := range s.store.BundlesContaining(sk.Name) {
		out.Bundles = append(out.Bundles, b.Name)
	}
	writeJSON(w, http.StatusOK, out)
}

// skillPayload is the JSON body accepted when pushing a skill from the CLI.
type skillPayload struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Body          string            `json:"body"`
	Tags          []string          `json:"tags,omitempty"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	AllowedTools  []string          `json:"allowedTools,omitempty"`
	Files         map[string]string `json:"files,omitempty"`
}

func decodeSkillPayload(r *http.Request) (*skillPayload, error) {
	var p skillPayload
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 16<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	return &p, nil
}

func (p *skillPayload) apply(sk *store.Skill) {
	sk.Name = p.Name
	sk.Description = strings.TrimSpace(p.Description)
	sk.Body = p.Body
	sk.Tags = store.ParseTags(strings.Join(p.Tags, ","))
	sk.Front.License = p.License
	sk.Front.Compatibility = p.Compatibility
	sk.Front.AllowedTools.Items = p.AllowedTools
	sk.Front.AllowedTools.Inline = len(p.AllowedTools) > 0
}

// writeFiles saves the bundled files carried in a push payload.
func (s *Server) writeFiles(name string, files map[string]string) error {
	for path, content := range files {
		if err := s.store.WriteFile(name, path, []byte(content)); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

func (s *Server) apiCreateSkill(w http.ResponseWriter, r *http.Request) {
	p, err := decodeSkillPayload(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	sk := &store.Skill{}
	p.apply(sk)

	if err := s.store.CreateSkill(sk); err != nil {
		status := http.StatusBadRequest
		code := "bad_request"
		if errors.Is(err, store.ErrExists) {
			status, code = http.StatusConflict, "already_exists"
		}
		writeJSONError(w, status, code, err.Error())
		return
	}
	if err := s.writeFiles(sk.Name, p.Files); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	saved, err := s.store.Skill(sk.Name)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toAPISkill(saved, true))
}

func (s *Server) apiUpdateSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	existing, err := s.store.Skill(name)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	p, err := decodeSkillPayload(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if p.Name == "" {
		p.Name = name
	}
	if p.Name != name {
		writeJSONError(w, http.StatusBadRequest, "bad_request",
			"the name in the body must match the name in the URL")
		return
	}

	updated := *existing
	p.apply(&updated)
	if err := s.store.SaveSkill(&updated); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := s.writeFiles(name, p.Files); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	saved, err := s.store.Skill(name)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toAPISkill(saved, true))
}

func (s *Server) apiBundles(w http.ResponseWriter, r *http.Request) {
	bundles := s.store.Bundles()
	out := api.BundleListResponse{Bundles: make([]api.Bundle, 0, len(bundles)), Total: len(bundles)}
	for _, b := range bundles {
		out.Bundles = append(out.Bundles, toAPIBundle(b))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiBundle(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.Bundle(r.PathValue("name"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	out := api.BundleManifest{Bundle: toAPIBundle(b), Registry: s.baseURL(r)}
	for _, sk := range s.store.BundleSkills(b) {
		out.Skills = append(out.Skills, toAPISkill(sk, false))
	}
	if out.Skills == nil {
		out.Skills = []api.Skill{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiBundleArchive(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.Bundle(r.PathValue("name"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", b.Name+".tar.gz"))
	if err := s.store.WriteSkillsTarGz(w, b.Skills); err != nil {
		logError("write bundle archive", err)
	}
}

// apiDownload streams an arbitrary set of skills as one archive.
func (s *Server) apiDownload(w http.ResponseWriter, r *http.Request) {
	names := r.URL.Query()["skill"]
	if len(names) == 0 {
		writeJSONError(w, http.StatusBadRequest, "bad_request",
			"specify at least one ?skill= parameter")
		return
	}
	for _, n := range names {
		if _, err := s.store.Skill(n); err != nil {
			writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("skill %q not found", n))
			return
		}
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="skills.tar.gz"`)
	if err := s.store.WriteSkillsTarGz(w, names); err != nil {
		logError("write archive", err)
	}
}
