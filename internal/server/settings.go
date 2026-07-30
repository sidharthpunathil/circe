package server

import (
	"net/http"
	"strings"

	"github.com/sidharthpunathil/skillbuilder/internal/gitsource"
	"github.com/sidharthpunathil/skillbuilder/internal/store"
)

// tokenRow is one access token as the settings page shows it.
type tokenRow struct {
	Name     string
	Label    string
	Help     string
	FromEnv  bool
	HasValue bool
}

var tokenHelp = map[string]struct{ label, help string }{
	"GITHUB_TOKEN":    {"GitHub", "Private repos, and lifts the 60 requests/hour limit."},
	"GITLAB_TOKEN":    {"GitLab", "Private projects on gitlab.com or a self-hosted instance."},
	"GITEA_TOKEN":     {"Gitea, Forgejo, Codeberg", "Private repos on any Gitea-family host."},
	"BITBUCKET_TOKEN": {"Bitbucket", "Private repositories on bitbucket.org."},
}

type settingsView struct {
	base
	Tokens    []tokenRow
	DataDir   string
	Saved     bool
	Error     string
	Providers []string
}

func (s *Server) settingsData(w http.ResponseWriter, r *http.Request) settingsView {
	var rows []tokenRow
	for _, src := range s.store.TokenSources() {
		meta := tokenHelp[src.Name]
		rows = append(rows, tokenRow{
			Name: src.Name, Label: meta.label, Help: meta.help,
			FromEnv: src.FromEnv, HasValue: src.HasValue,
		})
	}
	return settingsView{
		base:      s.base(w, r, "settings"),
		Tokens:    rows,
		DataDir:   s.store.Root(),
		Providers: gitsource.SupportedProviders(),
	}
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	view := s.settingsData(w, r)
	view.Saved = r.URL.Query().Get("saved") == "1"
	s.tmpl.render(w, r, http.StatusOK, "settings", view)
}

func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()

	creds := s.store.Credentials()
	for _, name := range store.CredentialNames() {
		value := strings.TrimSpace(r.FormValue(name))
		switch {
		case r.FormValue("clear_"+name) == "1":
			delete(creds, name)
		case value != "":
			creds[name] = value
		}
		// An empty field with no clear flag leaves the stored token alone, so
		// saving the page does not wipe tokens the form never displays.
	}

	if err := s.store.SaveCredentials(creds); err != nil {
		view := s.settingsData(w, r)
		view.Error = err.Error()
		s.tmpl.render(w, r, http.StatusBadRequest, "settings", view)
		return
	}
	// Take effect immediately rather than on next boot.
	gitsource.SetFallbackTokens(s.store.Credentials())

	s.setFlash(w, "ok", "Saved. New tokens are in use straight away.")
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}
