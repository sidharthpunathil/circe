package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sidharthpunathil/skillbuilder/internal/gitsource"
	"github.com/sidharthpunathil/skillbuilder/internal/store"
)

// sourcesPath is where provenance for remotely imported skills is recorded. It
// lives inside the data directory so it is part of the backup.
func (s *Server) sourcesPath() string {
	return filepath.Join(s.store.Root(), "sources.json")
}

func (s *Server) loadSources() map[string]gitsource.Source {
	m, err := gitsource.LoadSources(s.sourcesPath())
	if err != nil {
		logError("load sources", err)
		return map[string]gitsource.Source{}
	}
	return m
}

// ghCandidate decorates a discovered skill with whether we already have it.
type ghCandidate struct {
	gitsource.Candidate
	Exists    bool
	FileCount int
}

type githubView struct {
	base
	RepoURL    string
	Meta       *gitsource.Metadata
	Candidates []ghCandidate
	Folders    []*store.Bundle
	Error      string
	Searched   bool
	HasToken   bool
}

// handleImportGitHub discovers the skills in a repository without importing.
func (s *Server) handleImportGitHub(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("repo"))
	view := githubView{
		base:     s.base(w, r, "import"),
		RepoURL:  raw,
		Folders:  s.folderNames(),
		HasToken: gitsource.EnvToken() != "",
	}
	if raw == "" {
		s.tmpl.render(w, r, http.StatusOK, "github", view)
		return
	}

	repo, err := gitsource.ParseRepoURL(raw)
	if err != nil {
		view.Error = err.Error()
		s.tmpl.render(w, r, http.StatusOK, "github", view)
		return
	}

	// A cold tarball fetch of a large repo can take a while; bound it so a
	// hung request cannot pin the handler forever.
	ctx, cancel := contextWithTimeout(r, 90*time.Second)
	defer cancel()

	res, err := gitsource.Fetch(ctx, repo)
	if err != nil {
		view.Error = err.Error()
		s.tmpl.render(w, r, http.StatusOK, "github", view)
		return
	}

	view.Searched = true
	view.Meta = &res.Meta
	for _, c := range res.Candidates {
		gc := ghCandidate{Candidate: c, FileCount: len(c.Files)}
		if _, err := s.store.Skill(c.Name); err == nil {
			gc.Exists = true
		}
		view.Candidates = append(view.Candidates, gc)
	}
	sort.Slice(view.Candidates, func(i, j int) bool {
		return view.Candidates[i].Name < view.Candidates[j].Name
	})
	s.tmpl.render(w, r, http.StatusOK, "github", view)
}

// handleImportGitHubRun copies the selected skills into the registry and
// records where each one came from.
func (s *Server) handleImportGitHubRun(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	raw := strings.TrimSpace(r.FormValue("repo"))
	names := r.Form["names"]
	overwrite := r.FormValue("overwrite") == "1"
	folder := strings.TrimSpace(r.FormValue("folder"))

	back := "/import/github?repo=" + urlQueryEscape(raw)
	if len(names) == 0 {
		s.setFlash(w, "error", "Select at least one skill to import.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}

	repo, err := gitsource.ParseRepoURL(raw)
	if err != nil {
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/import/github", http.StatusSeeOther)
		return
	}

	ctx, cancel := contextWithTimeout(r, 90*time.Second)
	defer cancel()

	// Re-fetch rather than trusting anything round-tripped through the form:
	// file contents must never come from the browser.
	res, err := gitsource.Fetch(ctx, repo)
	if err != nil {
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}

	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}

	sources := s.loadSources()
	var imported, skipped, failed []string

	for _, c := range res.Candidates {
		if !want[c.Name] {
			continue
		}
		if err := s.store.WriteSkillFiles(c.Name, c.Files, overwrite); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				skipped = append(skipped, c.Name)
			} else {
				failed = append(failed, c.Name+": "+err.Error())
			}
			continue
		}
		sources[c.Name] = gitsource.NewSource(c.Name, repo, res.Meta, c)
		imported = append(imported, c.Name)
	}

	if err := gitsource.SaveSources(s.sourcesPath(), sources); err != nil {
		logError("save sources", err)
	}

	// Optionally drop everything imported straight into a folder.
	if folder != "" && len(imported) > 0 {
		if b, err := s.store.Bundle(folder); err == nil {
			updated := *b
			updated.Skills = append([]string(nil), b.Skills...)
			for _, n := range imported {
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
	if n := len(imported); n > 0 {
		parts = append(parts, fmt.Sprintf("imported %d from %s", n, repo.String()))
	}
	if n := len(skipped); n > 0 {
		parts = append(parts, fmt.Sprintf("skipped %d already present", n))
	}
	if n := len(failed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", n))
	}
	msg := "Nothing to do."
	if len(parts) > 0 {
		msg = strings.Join(parts, ", ")
		msg = strings.ToUpper(msg[:1]) + msg[1:] + "."
	}
	kind := "ok"
	if len(failed) > 0 {
		kind = "error"
		msg += " " + strings.Join(failed, "; ")
	}
	s.setFlash(w, kind, msg)
	http.Redirect(w, r, "/skills", http.StatusSeeOther)
}

// handleSkillResync re-fetches a single skill from the repository it came from,
// so an upstream update can be pulled in without redoing the whole import.
func (s *Server) handleSkillResync(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sources := s.loadSources()
	src, ok := sources[name]
	if !ok {
		s.setFlash(w, "error", "This skill was not imported from a repository.")
		http.Redirect(w, r, "/skills/"+name, http.StatusSeeOther)
		return
	}

	repo, err := gitsource.ParseRepoURL(src.URL)
	if err != nil {
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/skills/"+name, http.StatusSeeOther)
		return
	}

	ctx, cancel := contextWithTimeout(r, 90*time.Second)
	defer cancel()

	res, err := gitsource.Fetch(ctx, repo)
	if err != nil {
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/skills/"+name, http.StatusSeeOther)
		return
	}

	for _, c := range res.Candidates {
		if c.Name != name {
			continue
		}
		if err := s.store.WriteSkillFiles(name, c.Files, true); err != nil {
			s.setFlash(w, "error", err.Error())
			http.Redirect(w, r, "/skills/"+name, http.StatusSeeOther)
			return
		}
		sources[name] = gitsource.NewSource(name, repo, res.Meta, c)
		if err := gitsource.SaveSources(s.sourcesPath(), sources); err != nil {
			logError("save sources", err)
		}
		s.setFlash(w, "ok", fmt.Sprintf("Re-synced %s from %s.", name, repo.String()))
		http.Redirect(w, r, "/skills/"+name, http.StatusSeeOther)
		return
	}

	s.setFlash(w, "error", fmt.Sprintf("%s is no longer in %s. Your local copy is unchanged.", name, repo.String()))
	http.Redirect(w, r, "/skills/"+name, http.StatusSeeOther)
}

// folderNames lists bundles for the "import straight into a folder" selector.
func (s *Server) folderNames() []*store.Bundle { return s.store.Bundles() }

// contextWithTimeout bounds a slow upstream fetch while still cancelling if the
// client goes away.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
