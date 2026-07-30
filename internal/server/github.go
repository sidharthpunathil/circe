package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sidharthpunathil/circe/internal/gitsource"
	"github.com/sidharthpunathil/circe/internal/store"
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

// ghRule and ghServer are the other two things a repository can hold, shown
// beside the skills so one visit brings in everything.
type ghRule struct {
	store.FoundRule
	Exists bool
}

type ghServer struct {
	store.FoundMCP
	Exists bool
}

type githubView struct {
	base
	RepoURL    string
	Meta       *gitsource.Metadata
	Candidates []ghCandidate
	Rules      []ghRule
	Servers    []ghServer
	Folders    []*store.Bundle
	Error      string
	Searched   bool
	HasToken   bool
	Providers  []string
}

// Total is everything found across the three kinds.
func (v githubView) Total() int { return len(v.Candidates) + len(v.Rules) + len(v.Servers) }

// handleImportRepo discovers what is in a repository without importing.
func (s *Server) handleImportRepo(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("repo"))
	view := githubView{
		base:      s.base(w, r, "import"),
		RepoURL:   raw,
		Folders:   s.folderNames(),
		HasToken:  gitsource.EnvToken() != "",
		Providers: gitsource.SupportedProviders(),
	}
	if raw == "" {
		s.tmpl.render(w, r, http.StatusOK, "repo", view)
		return
	}

	repo, err := gitsource.ParseRepoURL(raw)
	if err != nil {
		view.Error = err.Error()
		s.tmpl.render(w, r, http.StatusOK, "repo", view)
		return
	}

	// A cold tarball fetch of a large repo can take a while; bound it so a
	// hung request cannot pin the handler forever.
	ctx, cancel := contextWithTimeout(r, 90*time.Second)
	defer cancel()

	res, err := gitsource.Fetch(ctx, repo)
	if err != nil {
		view.Error = err.Error()
		s.tmpl.render(w, r, http.StatusOK, "repo", view)
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

	// Skills come from the discovery that already grouped the archive; the rest
	// of the tree is where the rules and MCP servers are.
	found := store.DetectContent(res.Files, repo.Name)
	for _, fr := range found.Rules {
		gr := ghRule{FoundRule: fr}
		if _, err := s.store.Rule(fr.Name); err == nil {
			gr.Exists = true
		}
		view.Rules = append(view.Rules, gr)
	}
	for _, fm := range found.MCPs {
		gs := ghServer{FoundMCP: fm}
		if _, err := s.store.MCPServer(fm.Server.Name); err == nil {
			gs.Exists = true
		}
		view.Servers = append(view.Servers, gs)
	}
	s.tmpl.render(w, r, http.StatusOK, "repo", view)
}

// handleImportRepoRun copies the selected skills, rules and servers into the
// registry, and records where each imported skill came from.
func (s *Server) handleImportRepoRun(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	raw := strings.TrimSpace(r.FormValue("repo"))
	names := r.Form["names"]
	wantRules := chosen(r.Form["rules"])
	wantServers := chosen(r.Form["mcps"])
	overwrite := r.FormValue("overwrite") == "1"
	folder := strings.TrimSpace(r.FormValue("folder"))

	back := "/import/repo?repo=" + urlQueryEscape(raw)
	if len(names)+len(wantRules)+len(wantServers) == 0 {
		s.setFlash(w, "error", "Pick at least one thing to import.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}

	repo, err := gitsource.ParseRepoURL(raw)
	if err != nil {
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/import/repo", http.StatusSeeOther)
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

	want := chosen(names)
	sources := s.loadSources()
	var tally importTally
	var imported []string

	for _, c := range res.Candidates {
		if !want[c.Name] {
			continue
		}
		if err := s.store.WriteSkillFiles(c.Name, c.Files, overwrite); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				tally.Skipped++
			} else {
				tally.Failed = append(tally.Failed, c.Name+": "+err.Error())
			}
			continue
		}
		sources[c.Name] = gitsource.NewSource(c.Name, repo, res.Meta, c)
		imported = append(imported, c.Name)
		tally.Skills++
	}

	// Names are derived from the tree, so the same tree has to be read again
	// rather than trusting anything the form said about what a rule is called.
	found := store.DetectContent(res.Files, repo.Name)
	for _, fr := range found.Rules {
		if !wantRules[fr.Name] {
			continue
		}
		written, err := s.saveFoundRule(fr, overwrite)
		switch {
		case err != nil:
			tally.Failed = append(tally.Failed, fr.Name+": "+err.Error())
		case written:
			tally.Rules++
		default:
			tally.Skipped++
		}
	}
	for _, fm := range found.MCPs {
		if !wantServers[fm.Server.Name] {
			continue
		}
		written, err := s.saveFoundServer(fm.Server, overwrite)
		switch {
		case err != nil:
			tally.Failed = append(tally.Failed, fm.Server.Name+": "+err.Error())
		case written:
			tally.Servers++
		default:
			tally.Skipped++
		}
	}

	if err := gitsource.SaveSources(s.sourcesPath(), sources); err != nil {
		logError("save sources", err)
	}

	// Optionally drop every imported skill straight into a folder.
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

	msg, kind := tally.message("from " + repo.String())
	s.setFlash(w, kind, msg)
	http.Redirect(w, r, s.afterImport(tally, back), http.StatusSeeOther)
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

// folderSource reports the repository a folder's skills came from, when they
// all came from the same one.
//
// This is derived rather than stored: a folder's contents change, and a
// remembered origin would quietly go stale. Deriving it means the answer is
// always about the skills actually in there right now.
func (s *Server) folderSource(b *store.Bundle) (repo string, synced int) {
	sources := s.loadSources()
	for _, name := range b.Skills {
		src, ok := sources[name]
		if !ok {
			continue
		}
		if repo == "" {
			repo = src.URL
		} else if repo != src.URL {
			return "", 0 // mixed origins: there is no single thing to sync from
		}
		synced++
	}
	if synced == 0 {
		return "", 0
	}
	return repo, synced
}

// handleBundleSync re-pulls every skill in a folder from wherever it came from.
func (s *Server) handleBundleSync(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	b, err := s.store.Bundle(name)
	if err != nil {
		s.notFound(w, r, "folder")
		return
	}

	sources := s.loadSources()
	ctx, cancel := contextWithTimeout(r, 3*time.Minute)
	defer cancel()

	// Fetch each distinct repository once, however many skills came from it.
	fetched := map[string]*gitsource.Result{}
	var updated, unchanged, failed []string

	for _, skillName := range b.Skills {
		src, ok := sources[skillName]
		if !ok {
			continue
		}
		res, seen := fetched[src.URL]
		if !seen {
			repo, err := gitsource.ParseRepoURL(src.URL)
			if err != nil {
				failed = append(failed, skillName+": "+err.Error())
				continue
			}
			res, err = gitsource.Fetch(ctx, repo)
			if err != nil {
				failed = append(failed, skillName+": "+err.Error())
				fetched[src.URL] = nil
				continue
			}
			fetched[src.URL] = res
		}
		if res == nil {
			continue
		}

		found := false
		for _, c := range res.Candidates {
			if c.Name != skillName {
				continue
			}
			found = true
			before, _ := s.store.Skill(skillName)
			if err := s.store.WriteSkillFiles(skillName, c.Files, true); err != nil {
				failed = append(failed, skillName+": "+err.Error())
				break
			}
			after, _ := s.store.Skill(skillName)
			if before != nil && after != nil && before.Body == after.Body && before.Size == after.Size {
				unchanged = append(unchanged, skillName)
			} else {
				updated = append(updated, skillName)
			}
			repo, _ := gitsource.ParseRepoURL(src.URL)
			sources[skillName] = gitsource.NewSource(skillName, repo, res.Meta, c)
			break
		}
		if !found {
			failed = append(failed, skillName+" is no longer in the source; your copy is unchanged")
		}
	}

	if err := gitsource.SaveSources(s.sourcesPath(), sources); err != nil {
		logError("save sources", err)
	}

	var parts []string
	if n := len(updated); n > 0 {
		parts = append(parts, fmt.Sprintf("updated %d", n))
	}
	if n := len(unchanged); n > 0 {
		parts = append(parts, fmt.Sprintf("%d already current", n))
	}
	if n := len(failed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", n))
	}
	msg, kind := "Nothing in here came from a repository.", "ok"
	if len(parts) > 0 {
		msg = strings.Join(parts, ", ")
		msg = strings.ToUpper(msg[:1]) + msg[1:] + "."
	}
	if len(failed) > 0 {
		kind = "error"
		msg += " " + strings.Join(failed, "; ")
	}
	s.setFlash(w, kind, msg)
	http.Redirect(w, r, "/bundles/"+name, http.StatusSeeOther)
}
