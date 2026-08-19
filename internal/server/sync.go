package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sidharthpunathil/circe/internal/gitsync"
)

// Syncing points the registry at a GitHub repository and keeps the two in step.
//
// Push writes what is here; pull takes what is there. Neither runs on a timer.
// A registry is edited by a person, and a sync that fires on its own is a sync
// that overwrites something at the exact moment nobody is watching, so both
// directions are a button.

// syncPanel is where every sync action returns to, so a push does not bounce
// you back to the tokens panel to read its own result.
const syncPanel = "/settings?s=sync"

// syncTimeout bounds one sync. A push is one request per file plus four, so a
// large library is slow rather than stuck, and this stops a hung connection
// from holding the handler open forever.
const syncTimeout = 5 * time.Minute

// syncView is the settings page's view of the connection.
type syncView struct {
	Config    gitsync.Config
	Connected bool
	HasToken  bool
	LastSync  string
}

func (s *Server) syncData() syncView {
	cfg := gitsync.Load(s.store)
	v := syncView{Config: cfg, Connected: cfg.Connected()}
	for _, src := range s.store.TokenSources() {
		if src.Name == "GITHUB_TOKEN" {
			v.HasToken = src.HasValue
		}
	}
	if !cfg.LastSyncedAt.IsZero() {
		v.LastSync = cfg.LastSyncedAt.Format("2 Jan 2006, 15:04")
	}
	return v
}

// handleSyncConnect saves or clears the repository the registry syncs with.
func (s *Server) handleSyncConnect(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()

	if r.FormValue("disconnect") == "1" {
		if err := gitsync.Save(s.store, gitsync.Config{}); err != nil {
			s.setFlash(w, "error", err.Error())
		} else {
			s.setFlash(w, "ok", "Disconnected. Nothing was removed from the repository.")
		}
		http.Redirect(w, r, syncPanel, http.StatusSeeOther)
		return
	}

	repo, err := gitsync.ParseRepo(r.FormValue("repo"))
	if err != nil {
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, syncPanel, http.StatusSeeOther)
		return
	}

	// Keep the bookkeeping when the repository has not changed, so re-saving
	// the form to fix a branch does not lose the record of the last sync.
	cfg := gitsync.Load(s.store)
	if cfg.Repo != repo {
		cfg = gitsync.Config{}
	}
	cfg.Repo = repo
	cfg.Branch = strings.TrimSpace(r.FormValue("branch"))
	cfg.Path = strings.TrimSpace(r.FormValue("path"))

	if err := gitsync.Save(s.store, cfg); err != nil {
		s.setFlash(w, "error", err.Error())
	} else {
		s.setFlash(w, "ok", "Connected to "+repo+". Push when you're ready.")
	}
	http.Redirect(w, r, syncPanel, http.StatusSeeOther)
}

// handleSyncPush commits the whole registry to the repository.
func (s *Server) handleSyncPush(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	cfg := gitsync.Load(s.store)

	ctx, cancel := context.WithTimeout(r.Context(), syncTimeout)
	defer cancel()

	res, err := gitsync.Push(ctx, s.store, cfg, strings.TrimSpace(r.FormValue("message")))
	if err != nil {
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, syncPanel, http.StatusSeeOther)
		return
	}
	if err := gitsync.Record(s.store, cfg, "push", res); err != nil {
		logError("record sync", err)
	}

	if !res.Changed {
		s.setFlash(w, "ok", "Nothing to push: the repository already matches.")
	} else {
		s.setFlash(w, "ok", fmt.Sprintf("Pushed to %s as %s.", cfg.Repo, short(res.Commit)))
	}
	http.Redirect(w, r, syncPanel, http.StatusSeeOther)
}

// handleSyncPull replaces the registry with what the repository holds.
func (s *Server) handleSyncPull(w http.ResponseWriter, r *http.Request) {
	cfg := gitsync.Load(s.store)

	ctx, cancel := context.WithTimeout(r.Context(), syncTimeout)
	defer cancel()

	res, err := gitsync.Pull(ctx, s.store, cfg)
	if err != nil {
		s.setFlash(w, "error", err.Error())
		http.Redirect(w, r, syncPanel, http.StatusSeeOther)
		return
	}
	if err := gitsync.Record(s.store, cfg, "pull", res); err != nil {
		logError("record sync", err)
	}

	msg := fmt.Sprintf("Pulled %d skills from %s.", len(res.Imported), cfg.Repo)
	if n := len(res.Failed); n > 0 {
		msg += fmt.Sprintf(" %d could not be read: %s", n, strings.Join(res.Failed, "; "))
		s.setFlash(w, "error", msg)
	} else {
		s.setFlash(w, "ok", msg)
	}
	http.Redirect(w, r, syncPanel, http.StatusSeeOther)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
