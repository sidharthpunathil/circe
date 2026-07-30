package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sidharthpunathil/circe/internal/api"
	"github.com/sidharthpunathil/circe/internal/installer"
)

func newInitCmd() *cobra.Command {
	var (
		contextMode string
		flags       installFlags
		yes, all    bool
	)
	cmd := &cobra.Command{
		Use:   "init <bundle>",
		Short: "Install a bundle's skills into this project",
		Long: "Fetch a bundle, pick which of its skills to install, and write them into\n" +
			"the project's skills directory.\n\n" +
			"Without a terminal (CI, a pipe) there is nothing to pick with, so pass -y.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(cmd, args[0], yes || all, flags, contextMode)
		},
	}
	flags.register(cmd)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "install every skill in the bundle without prompting")
	cmd.Flags().BoolVar(&all, "all", false, "alias for --yes")
	cmd.Flags().StringVar(&contextMode, "context", "merge",
		"how to apply the folder's CLAUDE.md/AGENTS.md: merge, skip or overwrite")
	return cmd
}

func runInit(cmd *cobra.Command, bundle string, takeAll bool, flags installFlags, contextMode string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	root, err := projectRoot()
	if err != nil {
		return err
	}
	client, err := newClient()
	if err != nil {
		return err
	}
	manifest, err := client.GetBundle(ctx, bundle)
	if err != nil {
		if installer.IsNotFound(err) {
			return fmt.Errorf("no folder named %q at %s. Try `circe list --remote`: %w", bundle, client.BaseURL, err)
		}
		return err
	}
	if len(manifest.Skills) == 0 {
		return fmt.Errorf("bundle %q contains no skills", bundle)
	}
	// The manifest may point downloads at a different host than the one that
	// served it, so honour it for everything that follows.
	if manifest.Registry != "" {
		token := client.Token
		client = installer.NewClient(manifest.Registry)
		client.Token = token
	}

	chosen, err := chooseBundleSkills(manifest, takeAll)
	if err != nil {
		return err
	}
	if len(chosen) == 0 {
		fmt.Fprintln(out, dimStyle.Render("no skills selected"))
		return nil
	}

	dir := flags.resolveDir(root)
	results, err := installSelection(ctx, client, bundle, chosen, len(chosen) == len(manifest.Skills), flags.optionsIn(dir))
	if err != nil {
		return err
	}
	printResults(out, results, dir, flags.dryRun)
	if flags.dryRun {
		return nil
	}

	if err := applyContext(out, root, bundle, manifest.Context, contextMode, flags.dryRun); err != nil {
		return err
	}
	return recordInstall(root, client.BaseURL, bundle, dir, results)
}

func chooseBundleSkills(manifest *api.BundleManifest, takeAll bool) ([]string, error) {
	if takeAll {
		return skillNames(manifest.Skills), nil
	}
	if !interactive() {
		return nil, errors.New("stdin is not a terminal, so the skill picker cannot run. Pass -y to install every skill in the folder")
	}
	title := manifest.Bundle.Title
	if title == "" {
		title = manifest.Bundle.Name
	}
	// A bundle is already a curated set, so everything starts selected and the
	// user opts out rather than in.
	return selectSkills(fmt.Sprintf("%s · %d skills", title, len(manifest.Skills)), manifest.Skills, true)
}

// installSelection downloads through the bundle archive when the whole bundle
// was chosen — one request the server can cache — and falls back to the
// per-skill download endpoint for a subset.
func installSelection(ctx context.Context, client *installer.Client, bundle string, names []string, whole bool, opts installer.Options) ([]installer.Result, error) {
	if whole {
		return installer.InstallBundle(ctx, client, bundle, opts)
	}
	return installer.Install(ctx, client, names, opts)
}
