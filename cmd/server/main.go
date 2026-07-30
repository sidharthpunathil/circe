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

	"github.com/sidharthpunathil/circe/internal/server"
	"github.com/sidharthpunathil/circe/internal/store"
)

func main() {
	log.SetFlags(0)

	var (
		addr    = flag.String("addr", defaultAddr(), "address to listen on")
		dataDir = flag.String("data", envOr("DATA", "./data"), "directory holding skills and folders")
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
  GITHUB_TOKEN            raises the GitHub rate limit and reaches private repos

The SKILLBUILDER_ prefixed names this tool used before are still read, so an
existing deployment keeps working without being reconfigured.
`)
	}
	flag.Parse()

	st, err := store.Open(*dataDir)
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
	if err := server.Seed(context.Background(), st, server.SourcesPath(st), seed); err != nil {
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
	if err := srv.ListenAndServe(*addr); err != nil {
		log.Fatalf("server: %v", err)
	}
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
