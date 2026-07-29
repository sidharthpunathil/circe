// Command server runs the Skillbuilder web UI and JSON API.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/sidharthp/skillbuilder/internal/server"
	"github.com/sidharthp/skillbuilder/internal/store"
)

func main() {
	log.SetFlags(0)

	var (
		addr    = flag.String("addr", envOr("SKILLBUILDER_ADDR", "127.0.0.1:8080"), "address to listen on")
		dataDir = flag.String("data", envOr("SKILLBUILDER_DATA", "./data"), "directory holding skills and bundles")
		dev     = flag.Bool("dev", false, "reload templates and static assets from disk on every request")
		baseURL = flag.String("base-url", os.Getenv("SKILLBUILDER_BASE_URL"), "public base URL, if behind a proxy")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "skillbuilder-server — a personal registry for Claude agent skills\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n  %s [flags]\n\nFlags:\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open data directory: %v", err)
	}

	opts := server.Options{
		Store:   st,
		Dev:     *dev,
		Build:   strconv.FormatInt(time.Now().Unix(), 36),
		BaseURL: *baseURL,
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

	srv, err := server.New(opts)
	if err != nil {
		log.Fatalf("start server: %v", err)
	}
	if err := srv.ListenAndServe(*addr); err != nil {
		log.Fatalf("server: %v", err)
	}
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
