package main

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/sidharthpunathil/skillbuilder/internal/api"
	"github.com/sidharthpunathil/skillbuilder/internal/installer"
)

func newPullCmd() *cobra.Command {
	var flags installFlags
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Re-download every skill in the lockfile",
		Long: "Re-download everything the lockfile records and update whatever changed.\n\n" +
			"Skills edited locally are left alone unless --force is given, so a pull\n" +
			"never silently discards work.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPull(cmd, flags)
		},
	}
	flags.register(cmd)
	return cmd
}

func runPull(cmd *cobra.Command, flags installFlags) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	root, err := projectRoot()
	if err != nil {
		return err
	}
	lf, err := installer.LoadLockfile(root)
	if err != nil {
		return err
	}
	locked := installer.LockedSkills(lf)
	if len(locked) == 0 {
		fmt.Fprintln(out, dimStyle.Render("nothing to pull: "+installer.LockfileName+" records no skills"))
		return nil
	}

	client, err := pullClient(lf)
	if err != nil {
		return err
	}

	// Reuse the directory recorded at install time unless -d says otherwise.
	dir := installer.ResolveDir(flags.dir, flags.dirWasSet(), lf)

	// Decide up front which skills may be overwritten. Anything the user has
	// edited is held back rather than clobbered, and reported as skipped.
	var want []string
	var held []installer.Result
	for _, s := range locked {
		state, err := installer.Drift(dir, s)
		if err != nil {
			return err
		}
		if state == installer.DriftModified && !flags.force {
			held = append(held, installer.Result{
				Name:     s.Name,
				Status:   installer.StatusSkipped,
				Checksum: s.Checksum,
				Reason:   "edited locally; --force restores it",
			})
			continue
		}
		want = append(want, s.Name)
	}

	var results []installer.Result
	if len(want) > 0 {
		opts := flags.optionsIn(dir)
		// Everything here is already ours, so replacing it is the whole point
		// of the command; the drift check above is what protects local edits.
		opts.Force = true
		if results, err = installer.Install(ctx, client, want, opts); err != nil {
			return err
		}
	}
	results = append(results, held...)
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	printResults(out, results, dir, flags.dryRun)
	if flags.dryRun {
		return nil
	}

	// Preserve each skill's bundle by recording per skill rather than in bulk.
	for _, r := range results {
		if r.Status == installer.StatusSkipped {
			continue
		}
		installer.RecordSkill(lf, api.InstalledSkill{
			Name:     r.Name,
			Bundle:   lf.Skills[r.Name].Bundle,
			Registry: client.BaseURL,
			Checksum: r.Checksum,
		})
	}
	lf.Registry = client.BaseURL
	return installer.SaveLockfile(root, lf)
}

// pullClient prefers the registry recorded in the lockfile, because that is
// where these particular skills came from. An explicit --registry or
// $SKILLBUILDER_REGISTRY still wins; a personal config file does not.
func pullClient(lf *api.Lockfile) (*installer.Client, error) {
	url, source, err := resolveRegistry()
	if err != nil {
		return nil, err
	}
	if lf.Registry != "" && source != installer.SourceFlag && source != installer.SourceEnv {
		url = lf.Registry
	}
	c := installer.NewClient(url)
	if cfg, err := installer.LoadConfig(); err == nil {
		c.Token = installer.ResolveToken(cfg)
	}
	return c, nil
}
