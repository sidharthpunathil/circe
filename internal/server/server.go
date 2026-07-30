// Package server implements the Skillbuilder web UI and JSON API.
package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sidharthpunathil/skillbuilder/internal/api"
	"github.com/sidharthpunathil/skillbuilder/internal/store"
	"github.com/sidharthpunathil/skillbuilder/web"
)

// Options configures a Server.
type Options struct {
	Store *store.Store
	// Dev re-parses templates on every request and serves static assets from
	// disk so edits appear without a rebuild.
	Dev bool
	// TemplateDir and StaticDir are only consulted when Dev is set.
	TemplateDir string
	StaticDir   string
	// Build is a cache-busting token appended to asset URLs.
	Build string
	// BaseURL is the externally reachable origin, used to render install
	// commands and marketplace URLs.
	BaseURL string
	// Auth optionally protects the whole site. Required for a public deploy.
	Auth Auth
	// Public relaxes the same-origin check for cross-origin API reads, needed
	// when the registry is served from a different host than the caller.
	Public bool
}

// Server holds the HTTP handlers and their dependencies.
type Server struct {
	store *store.Store
	tmpl  *templates
	opts  Options
	mux   *http.ServeMux
}

// New builds a Server with all routes registered.
func New(opts Options) (*Server, error) {
	if opts.Build == "" {
		opts.Build = fmt.Sprintf("%d", time.Now().Unix())
	}
	tmpl, err := loadTemplates(opts.Dev, opts.TemplateDir)
	if err != nil {
		return nil, err
	}

	s := &Server{store: opts.Store, tmpl: tmpl, opts: opts, mux: http.NewServeMux()}
	s.routes()
	return s, nil
}

func (s *Server) routes() {
	m := s.mux

	// Pages
	m.HandleFunc("GET /{$}", s.handleHome)
	m.HandleFunc("GET /login", s.handleLoginPage)
	m.HandleFunc("POST /login", s.handleLoginSubmit)
	m.HandleFunc("POST /logout", s.handleLogout)

	m.HandleFunc("GET /skills", s.handleSkills)
	m.HandleFunc("POST /skills/bulk", s.handleSkillsBulk)
	m.HandleFunc("GET /skills/new", s.handleSkillNew)
	m.HandleFunc("POST /skills/new", s.handleSkillCreate)
	m.HandleFunc("GET /skills/{name}", s.handleSkill)
	m.HandleFunc("GET /skills/{name}/edit", s.handleSkillEdit)
	m.HandleFunc("POST /skills/{name}/edit", s.handleSkillUpdate)
	m.HandleFunc("POST /skills/{name}/delete", s.handleSkillDelete)
	m.HandleFunc("GET /skills/{name}/download", s.handleSkillDownload)
	m.HandleFunc("GET /skills/{name}/files/new", s.handleFileNew)
	m.HandleFunc("POST /skills/{name}/files/new", s.handleFileCreate)
	m.HandleFunc("GET /skills/{name}/files/{path...}", s.handleFileEdit)
	m.HandleFunc("POST /skills/{name}/files/{path...}", s.handleFileSave)

	m.HandleFunc("GET /bundles", s.handleBundles)
	m.HandleFunc("GET /bundles/new", s.handleBundleNew)
	m.HandleFunc("POST /bundles/new", s.handleBundleCreate)
	m.HandleFunc("GET /bundles/{name}", s.handleBundle)
	m.HandleFunc("GET /bundles/{name}/edit", s.handleBundleEdit)
	m.HandleFunc("POST /bundles/{name}/edit", s.handleBundleUpdate)
	m.HandleFunc("POST /bundles/{name}/delete", s.handleBundleDelete)
	m.HandleFunc("GET /bundles/{name}/download", s.handleBundleDownload)
	m.HandleFunc("POST /bundles/{name}/share", s.handleShareCreate)
	m.HandleFunc("POST /bundles/{name}/unshare", s.handleShareRevoke)
	m.HandleFunc("POST /skills/{name}/share", s.handleShareCreate)
	m.HandleFunc("POST /skills/{name}/unshare", s.handleShareRevoke)

	// Public, read-only, and reachable without the registry password — but only
	// for resources the owner explicitly shared.
	m.HandleFunc("GET /s/{id}", s.handleSharePage)
	m.HandleFunc("GET /api/v1/share/{id}", s.apiShare)
	m.HandleFunc("GET /api/v1/share/{id}/archive.tar.gz", s.apiShareArchive)

	m.HandleFunc("GET /bundles/{name}/context/{file}", s.handleBundleContextEdit)
	m.HandleFunc("POST /bundles/{name}/context/{file}", s.handleBundleContextSave)

	m.HandleFunc("GET /import", s.handleImport)
	m.HandleFunc("POST /import", s.handleImportRun)
	m.HandleFunc("GET /import/code", s.handleImportCode)
	m.HandleFunc("POST /import/code", s.handleImportCodeRun)
	m.HandleFunc("GET /import/github", s.handleImportGitHub)
	m.HandleFunc("POST /import/github", s.handleImportGitHubRun)
	m.HandleFunc("POST /skills/{name}/resync", s.handleSkillResync)

	// Claude Code native compatibility
	m.HandleFunc("GET /.claude-plugin/marketplace.json", s.handleMarketplace)

	// JSON API consumed by the CLI
	m.HandleFunc("GET /api/v1/skills", s.apiSkills)
	m.HandleFunc("POST /api/v1/skills", s.apiCreateSkill)
	m.HandleFunc("GET /api/v1/skills/{name}", s.apiSkill)
	m.HandleFunc("PUT /api/v1/skills/{name}", s.apiUpdateSkill)
	m.HandleFunc("GET /api/v1/bundles", s.apiBundles)
	m.HandleFunc("GET /api/v1/bundles/{name}", s.apiBundle)
	m.HandleFunc("GET /api/v1/bundles/{name}/archive.tar.gz", s.apiBundleArchive)
	m.HandleFunc("GET /api/v1/download", s.apiDownload)
	m.HandleFunc("GET /api/v1/health", s.apiHealth)

	// robots.txt, sitemap.xml and the generated Open Graph cards
	s.registerMetaRoutes(m)

	// Static assets
	m.Handle("GET /static/", s.staticHandler())
}

// ServeHTTP applies cross-cutting middleware then dispatches.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")

	if !s.requireAuth(w, r) {
		return
	}

	// A browser on this machine can be pointed at the registry by any website,
	// so state-changing requests must prove they came from this origin.
	if isMutation(r.Method) && !s.sameOrigin(r) {
		s.renderError(w, r, http.StatusForbidden, "Blocked",
			"That request did not come from this site, so it was refused.")
		return
	}
	s.mux.ServeHTTP(w, r)
}

func isMutation(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// sameOrigin accepts a mutation when the Origin (or Referer) header matches the
// request host. Requests with neither header are accepted so that command-line
// clients such as curl and the CLI keep working; browsers always send Origin on
// cross-origin form posts, which is the case being defended against.
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func (s *Server) staticHandler() http.Handler {
	var fsys http.FileSystem
	if s.opts.Dev {
		fsys = http.Dir(s.opts.StaticDir)
	} else {
		sub, err := fs.Sub(web.Static, "static")
		if err != nil {
			log.Fatalf("static assets: %v", err)
		}
		fsys = http.FS(sub)
	}
	h := http.StripPrefix("/static/", http.FileServer(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Assets are content-addressed by the ?v= build token.
		if !s.opts.Dev {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		h.ServeHTTP(w, r)
	})
}

// ---------- shared view data ----------

// stats summarizes the registry for the footer and home page.
type stats struct {
	Skills  int
	Bundles int
	Tags    int
	Size    int64
}

// flash is a one-shot message shown after a redirect.
type flash struct {
	Kind    string // "ok" or "error"
	Message string
}

const flashCookie = "sb_flash"

func (s *Server) setFlash(w http.ResponseWriter, kind, msg string) {
	v := base64.RawURLEncoding.EncodeToString([]byte(kind + "|" + msg))
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookie,
		Value:    v,
		Path:     "/",
		MaxAge:   30,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) takeFlash(w http.ResponseWriter, r *http.Request) *flash {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: "/", MaxAge: -1})

	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	kind, msg, ok := strings.Cut(string(raw), "|")
	if !ok {
		return nil
	}
	return &flash{Kind: kind, Message: msg}
}

// base is embedded in every page's view model.
type base struct {
	// Bare drops the app chrome. Used by the sign-in page, and by public share
	// pages, where the nav links are unusable and the footer would otherwise
	// disclose the server's data directory to an anonymous visitor.
	Bare bool
	// SignedIn is true when a browser session is active, so the header can
	// offer a way out. Token-authenticated API calls never render chrome.
	SignedIn bool
	Nav      string
	Query    string
	Build    string
	Stats    stats
	DataDir  string
	Flash    *flash
	SEO      PageMeta
}

func (s *Server) base(w http.ResponseWriter, r *http.Request, nav string) base {
	skills := s.store.Skills()
	var size int64
	for _, sk := range skills {
		size += sk.Size
	}
	return base{
		SignedIn: s.opts.Auth.Enabled() && s.hasSession(r),
		Nav:      nav,
		Query:    r.URL.Query().Get("q"),
		Build:    s.opts.Build,
		DataDir:  s.store.Root(),
		Flash:    s.takeFlash(w, r),
		SEO:      s.pageMeta(r),
		Stats: stats{
			Skills:  len(skills),
			Bundles: s.store.BundleCount(),
			Tags:    len(s.store.Tags()),
			Size:    size,
		},
	}
}

// baseURL resolves the externally reachable origin for this request.
func (s *Server) baseURL(r *http.Request) string {
	if s.opts.BaseURL != "" {
		return strings.TrimRight(s.opts.BaseURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ---------- error rendering ----------

type errorView struct {
	base
	Status  int
	Title   string
	Message string
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, title, msg string) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSONError(w, status, title, msg)
		return
	}
	s.tmpl.render(w, r, status, "error", errorView{
		base:    s.base(w, r, ""),
		Status:  status,
		Title:   title,
		Message: msg,
	})
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request, what string) {
	s.renderError(w, r, http.StatusNotFound, "Not found",
		fmt.Sprintf("There is no %s here. It may have been renamed or deleted.", what))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		log.Printf("skillbuilder: write json: %v", err)
	}
}

func writeJSONError(w http.ResponseWriter, status int, err, msg string) {
	writeJSON(w, status, api.Error{Error: err, Message: msg})
}

// ListenAndServe starts the server on addr.
func (s *Server) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("skillbuilder listening on http://%s (data: %s)", ln.Addr(), s.store.Root())
	return srv.Serve(ln)
}
