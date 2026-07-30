package server

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/sidharthpunathil/skillbuilder/internal/gitsource"
	"github.com/sidharthpunathil/skillbuilder/internal/store"
)

// SeedOptions describes an optional first-boot import.
type SeedOptions struct {
	// Repo is any source gitsource.ParseRepoURL accepts. Empty disables seeding.
	Repo string
	// Folder, when set, is created and every seeded skill is added to it, so a
	// fresh deployment has something installable immediately.
	Folder string
	// FolderTitle is the display name for that folder.
	FolderTitle string
}

// Seed imports skills from a repository the first time the registry is empty.
//
// It is deliberately a no-op once anything exists, so a redeploy never
// overwrites edits, and the source repo is configuration rather than something
// baked into the binary.
func Seed(ctx context.Context, st *store.Store, sourcesPath string, opts SeedOptions) error {
	if strings.TrimSpace(opts.Repo) == "" {
		return nil
	}
	if st.SkillCount() > 0 {
		return nil
	}

	repo, err := gitsource.ParseRepoURL(opts.Repo)
	if err != nil {
		return fmt.Errorf("seed repo %q: %w", opts.Repo, err)
	}

	log.Printf("skillbuilder: registry is empty, seeding from %s", repo.String())
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	res, err := gitsource.Fetch(ctx, repo)
	if err != nil {
		return fmt.Errorf("seed from %s: %w", repo.String(), err)
	}

	sources, err := gitsource.LoadSources(sourcesPath)
	if err != nil {
		sources = map[string]gitsource.Source{}
	}

	var imported []string
	for _, c := range res.Candidates {
		if err := st.WriteSkillFiles(c.Name, c.Files, false); err != nil {
			log.Printf("skillbuilder: seed %s: %v", c.Name, err)
			continue
		}
		sources[c.Name] = gitsource.NewSource(c.Name, repo, res.Meta, c)
		imported = append(imported, c.Name)
	}
	if err := gitsource.SaveSources(sourcesPath, sources); err != nil {
		log.Printf("skillbuilder: save sources: %v", err)
	}

	if opts.Folder != "" && len(imported) > 0 {
		b := &store.Bundle{
			Name:   opts.Folder,
			Title:  opts.FolderTitle,
			Skills: imported,
		}
		if err := st.SaveBundle(b); err != nil {
			log.Printf("skillbuilder: seed folder: %v", err)
		}
	}

	log.Printf("skillbuilder: seeded %d skills from %s", len(imported), repo.String())
	return nil
}

// SourcesPath exposes where provenance is stored, for callers doing the seed
// outside a running server.
func SourcesPath(st *store.Store) string {
	return (&Server{store: st}).sourcesPath()
}
