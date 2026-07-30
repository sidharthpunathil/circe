// Command server runs the Skillbuilder web UI and JSON API.
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
	"time"

	"github.com/sidharthpunathil/skillbuilder/internal/server"
	"github.com/sidharthpunathil/skillbuilder/internal/store"
)

func main() {
	log.SetFlags(0)

	var (
		addr    = flag.String("addr", defaultAddr(), "address to listen on")
		dataDir = flag.String("data", envOr("SKILLBUILDER_DATA", "./data"), "directory holding skills and folders")
		dev     = flag.Bool("dev", false, "reload templates and static assets from disk on every request")
		baseURL = flag.String("base-url", os.Getenv("SKILLBUILDER_BASE_URL"), "public base URL, if behind a proxy")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "skillbuilder-server — a personal registry for Claude agent skills\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n  %s [flags]\n\nFlags:\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Environment:
  PORT                     listen port (takes precedence, set by most hosts)
  SKILLBUILDER_ADDR        full listen address
  SKILLBUILDER_DATA        data directory
  SKILLBUILDER_BASE_URL    public origin behind a proxy
  SKILLBUILDER_USER        basic-auth username (optional; any user if unset)
  SKILLBUILDER_PASSWORD    basic-auth password — required to protect a public deploy
  SKILLBUILDER_SEED_REPO   repository to import from when the registry is empty
  SKILLBUILDER_SEED_FOLDER folder to place seeded skills into
  GITHUB_TOKEN             raises the GitHub rate limit and reaches private repos
  SKILLBUILDER_SITE_NAME   site name used in titles and Open Graph tags
  SKILLBUILDER_SITE_DESCRIPTION  default meta description
  SKILLBUILDER_TWITTER_SITE      @handle for Twitter card attribution
`)
	}
	flag.Parse()

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open data directory: %v", err)
	}

	auth := server.Auth{
		User:     os.Getenv("SKILLBUILDER_USER"),
		Password: os.Getenv("SKILLBUILDER_PASSWORD"),
	}
	// Seeding is configuration, never baked in: an operator points a fresh
	// deployment at whichever repository they want it to start from.
	seed := server.SeedOptions{
		Repo:        os.Getenv("SKILLBUILDER_SEED_REPO"),
		Folder:      os.Getenv("SKILLBUILDER_SEED_FOLDER"),
		FolderTitle: os.Getenv("SKILLBUILDER_SEED_FOLDER_TITLE"),
	}
	if err := server.Seed(context.Background(), st, server.SourcesPath(st), seed); err != nil {
		// A failed seed must not stop the service from starting; the registry is
		// simply empty and the operator can import by hand.
		log.Printf("skillbuilder: seed skipped: %v", err)
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
			"Set SKILLBUILDER_PASSWORD.", *addr)
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
	if v := os.Getenv("SKILLBUILDER_ADDR"); v != "" {
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
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
