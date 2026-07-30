package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/sidharthpunathil/skillbuilder/internal/installer"
	"github.com/sidharthpunathil/skillbuilder/internal/store"
)

// import installs from a share code without needing a registry of your own —
// the code carries the origin, so trying someone else's folder takes one command.
func newImportCmd() *cobra.Command {
	var (
		flags       installFlags
		contextMode string
	)
	cmd := &cobra.Command{
		Use:   "import <code>",
		Short: "Install skills from someone else's share code",
		Long: "Install a folder or skill that someone shared with you.\n\n" +
			"Accepts a share code (sb_…) or a share link. The origin registry is\n" +
			"read from the code, so no configuration is needed.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(cmd, args[0], flags, contextMode)
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&contextMode, "context", "merge",
		"how to apply a shared CLAUDE.md/AGENTS.md: merge, skip or overwrite")
	return cmd
}

// sharePayload mirrors what the origin's public share endpoint returns.
type sharePayload struct {
	Kind        string            `json:"kind"`
	Name        string            `json:"name"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Skills      []string          `json:"skills"`
	Context     map[string]string `json:"context"`
	Origin      string            `json:"origin"`
}

func runImport(cmd *cobra.Command, code string, flags installFlags, contextMode string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	root, err := projectRoot()
	if err != nil {
		return err
	}
	origin, id, err := store.DecodeShareCode(code)
	if err != nil {
		return err
	}

	payload, err := fetchSharePayload(ctx, origin, id)
	if err != nil {
		return err
	}
	if len(payload.Skills) == 0 {
		return fmt.Errorf("that share contains no skills")
	}
	if payload.Origin != "" {
		origin = payload.Origin
	}

	label := payload.Title
	if label == "" {
		label = payload.Name
	}
	fmt.Fprintln(out, dimStyle.Render(fmt.Sprintf("%s · %d %s from %s",
		label, len(payload.Skills), pluralWord(len(payload.Skills), "skill", "skills"), origin)))

	dir := flags.resolveDir(root)
	// The share archive endpoint is public and carries exactly this share's
	// skills, so it is fetched directly rather than through the registry client.
	results, err := installer.InstallFromURL(ctx,
		origin+"/api/v1/share/"+id+"/archive.tar.gz",
		payload.Skills, flags.optionsIn(dir))
	if err != nil {
		return err
	}
	printResults(out, results, dir, flags.dryRun)
	if flags.dryRun {
		return nil
	}

	folder := store.Slugify(payload.Name)
	if err := applyContext(out, root, folder, payload.Context, contextMode, flags.dryRun); err != nil {
		return err
	}
	return recordInstall(root, origin, folder, dir, results)
}

func fetchSharePayload(ctx context.Context, origin, id string) (*sharePayload, error) {
	url := origin + "/api/v1/share/" + id
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "skillbuilder-cli")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s; check the code, and that the registry is online", origin)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("that share no longer exists; whoever shared it may have stopped")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %d", origin, resp.StatusCode)
	}

	var p sharePayload
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&p); err != nil {
		return nil, fmt.Errorf("%s did not return a valid share", origin)
	}
	sort.Strings(p.Skills)
	return &p, nil
}
