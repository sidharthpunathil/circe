// Command server runs the Circe web UI and JSON API.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"

	"github.com/sidharthpunathil/circe/internal/server"
	"github.com/sidharthpunathil/circe/internal/store"
)

func main() {
	log.SetFlags(0)

	// Two subcommands sit in front of the server: they are the answer to "what
	// if I want my content out of here", and the answer has to be a command
	// that exists rather than a paragraph in a README.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "export":
			runExport(os.Args[2:])
			return
		case "import":
			runImport(os.Args[2:])
			return
		}
	}

	var (
		addr    = flag.String("addr", defaultAddr(), "address to listen on")
		noDash  = flag.Bool("no-dashboard", envBool("NO_DASHBOARD"), "hide the PocketBase dashboard and its REST API")
		dataDir = flag.String("data", envOr("DATA", "./data"), "directory holding the registry database")
		dev     = flag.Bool("dev", false, "reload templates and static assets from disk on every request")
		baseURL = flag.String("base-url", env("BASE_URL"), "public base URL, if behind a proxy")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "circe-server: your own registry for Claude agent skills, rules and MCP servers\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n  %s [flags]\n\nFlags:\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Environment:
  PORT                    listen port (takes precedence, set by most hosts)
  CIRCE_ADDR              full listen address
  CIRCE_DATA              data directory
  CIRCE_BASE_URL          public origin behind a proxy
  CIRCE_USER              basic-auth username (optional; any user if unset)
  CIRCE_PASSWORD          basic-auth password, required to protect a public deploy
  CIRCE_SEED_REPO         repository to import from when the registry is empty
  CIRCE_SEED_FOLDER       folder to place seeded skills into
  CIRCE_SITE_NAME         site name used in titles and Open Graph tags
  CIRCE_SITE_DESCRIPTION  default meta description
  CIRCE_TWITTER_SITE      @handle for Twitter card attribution
  CIRCE_NO_DASHBOARD      set to 1 to remove the PocketBase dashboard at /_/
                          (when a password is set, the dashboard is behind it)
  GITHUB_TOKEN            raises the GitHub rate limit and reaches private repos

The SKILLBUILDER_ prefixed names this tool used before are still read, so an
existing deployment keeps working without being reconfigured.
`)
	}
	flag.Parse()
	dashboard := !*noDash

	// The registry runs as a PocketBase application: the same database the
	// store reads, plus the dashboard at /_/ and PocketBase's own REST API.
	// Bootstrapping it here rather than going through PocketBase's cobra CLI
	// keeps this binary's flags and environment exactly as they were.
	abs, err := filepath.Abs(*dataDir)
	if err != nil {
		log.Fatalf("data directory: %v", err)
	}
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  abs,
		DefaultDev:      *dev,
		HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		log.Fatalf("open data directory: %v", err)
	}
	st, err := store.OpenWithApp(app)
	if err != nil {
		log.Fatalf("open data directory: %v", err)
	}

	auth := server.Auth{
		User:     env("USER"),
		Password: env("PASSWORD"),
	}
	// Seeding is configuration, never baked in: an operator points a fresh
	// deployment at whichever repository they want it to start from.
	seed := server.SeedOptions{
		Repo:        env("SEED_REPO"),
		Folder:      env("SEED_FOLDER"),
		FolderTitle: env("SEED_FOLDER_TITLE"),
	}
	if err := server.Seed(context.Background(), st, seed); err != nil {
		// A failed seed must not stop the service from starting; the registry is
		// simply empty and the operator can import by hand.
		log.Printf("circe: seed skipped: %v", err)
	}

	opts := server.Options{
		Store:   st,
		Dev:     *dev,
		Build:   strconv.FormatInt(time.Now().Unix(), 36),
		BaseURL: *baseURL,
		Auth:    auth,
	}
	if *dev {
		root, err := findProjectRoot()
		if err != nil {
			log.Fatalf("dev mode: %v", err)
		}
		opts.TemplateDir = filepath.Join(root, "web", "templates")
		opts.StaticDir = filepath.Join(root, "web", "static")
		log.Printf("dev mode: serving templates from %s", opts.TemplateDir)
	}

	if !auth.Enabled() && !isLoopback(*addr) {
		log.Printf("WARNING: listening on %s with no password set. "+
			"Anyone who can reach this address can edit and delete your skills. "+
			"Set CIRCE_PASSWORD.", *addr)
	}

	srv, err := server.New(opts)
	if err != nil {
		log.Fatalf("start server: %v", err)
	}

	// Circe owns every path PocketBase has not already claimed. The priority
	// puts this last so that /_/ (the dashboard) and /api/collections (its REST
	// API) keep working, and everything else falls through to the registry.
	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func: func(e *core.ServeEvent) error {
			e.Router.BindFunc(func(re *core.RequestEvent) error {
				if !isPocketBasePath(re.Request.URL.Path) {
					return re.Next()
				}
				// Some deployments would rather not have a second sign-in page
				// on the internet at all.
				if !dashboard {
					return apis.NewNotFoundError("", nil)
				}
				// The dashboard reads the same database the registry does, so
				// it sits behind the same password. Its superuser account is
				// then a second lock on a door strangers cannot reach.
				if !srv.Authorize(re.Response, re.Request) {
					return nil // the challenge has already been written
				}
				return re.Next()
			})

			e.Router.Any("/{path...}", apis.WrapStdHandler(srv))
			log.Printf("circe listening on http://%s (data: %s)", *addr, st.Root())
			switch {
			case !dashboard:
				log.Printf("circe: PocketBase dashboard disabled")
			case auth.Enabled():
				log.Printf("circe: PocketBase dashboard at http://%s/_/ (behind CIRCE_PASSWORD)", *addr)
			default:
				log.Printf("circe: PocketBase dashboard at http://%s/_/", *addr)
			}
			return e.Next()
		},
		Priority: 999,
	})

	if err := apis.Serve(app, apis.ServeConfig{
		HttpAddr:        *addr,
		ShowStartBanner: false,
	}); err != nil {
		log.Fatalf("server: %v", err)
	}
}

// isPocketBasePath reports whether a path belongs to PocketBase rather than to
// the registry: its dashboard, the assets that dashboard loads, and its own
// REST API. Circe's API lives under /api/v1 and is not included.
func isPocketBasePath(p string) bool {
	if p == "/_" || strings.HasPrefix(p, "/_/") {
		return true
	}
	return strings.HasPrefix(p, "/api/") && !strings.HasPrefix(p, "/api/v1/")
}

// defaultAddr honours $PORT, which is how nearly every host tells a process
// where to listen, and binds all interfaces in that case since the platform
// terminates external traffic. Otherwise it stays on loopback.
func defaultAddr() string {
	if v := env("ADDR"); v != "" {
		return v
	}
	if port := os.Getenv("PORT"); port != "" {
		return "0.0.0.0:" + port
	}
	return "127.0.0.1:8080"
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// env reads CIRCE_<suffix>, falling back to the pre-rename SKILLBUILDER_<suffix>.
//
// This matters more than it looks. A deployment configured before the rename
// has SKILLBUILDER_PASSWORD set and nothing else; without this fallback the new
// binary would start with authentication silently disabled and the registry
// open to anyone, and with no data directory it would crash-loop on an image
// that has no writable working directory. The old names stay supported.
func env(suffix string) string {
	if v := strings.TrimSpace(os.Getenv("CIRCE_" + suffix)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("SKILLBUILDER_" + suffix))
}

// envBool reads a CIRCE_ flag that is set by being present and non-zero.
func envBool(suffix string) bool {
	switch strings.ToLower(env(suffix)) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

func envOr(suffix, fallback string) string {
	if v := env(suffix); v != "" {
		return v
	}
	return fallback
}

// findProjectRoot walks up from the working directory looking for go.mod, so
// dev mode works regardless of where the binary is launched from.
func findProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find go.mod above %s", dir)
		}
		dir = parent
	}
}

// runExport writes the whole registry out as plain files.
func runExport(args []string) {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	dataDir := fs.String("data", envOr("DATA", "./data"), "data directory to read")
	out := fs.String("out", "./circe-export", "directory to write")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "circe-server export: write every skill, rule, server and folder out as plain files\n\n"+
			"Usage:\n  %s export [-data DIR] [-out DIR]\n\nFlags:\n", os.Args[0])
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nTokens and share links are never exported.\n")
	}
	_ = fs.Parse(args)

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open data directory: %v", err)
	}
	defer st.Close()

	if err := st.ExportTree(*out); err != nil {
		log.Fatalf("export: %v", err)
	}
	log.Printf("wrote %d skills, %d rules, %d servers and %d folders to %s",
		st.SkillCount(), st.RuleCount(), st.MCPCount(), st.BundleCount(), *out)
}

// runImport reads a directory in that same layout back in.
func runImport(args []string) {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	dataDir := fs.String("data", envOr("DATA", "./data"), "data directory to write")
	in := fs.String("in", "./circe-export", "directory to read")
	force := fs.Bool("force", false, "replace entries that already exist")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "circe-server import: read a directory of skills, rules, servers and folders back in\n\n"+
			"Usage:\n  %s import [-data DIR] [-in DIR] [-force]\n\nFlags:\n", os.Args[0])
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open data directory: %v", err)
	}
	defer st.Close()

	res, err := st.ImportTree(*in, *force)
	if err != nil {
		log.Fatalf("import: %v", err)
	}
	log.Printf("imported %d, skipped %d", len(res.Imported), len(res.Skipped))
	for _, f := range res.Failed {
		log.Printf("failed: %s", f)
	}
	if len(res.Skipped) > 0 && !*force {
		log.Printf("pass -force to replace what is already there")
	}
}
