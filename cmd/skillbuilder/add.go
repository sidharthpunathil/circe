package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sidharthpunathil/skillbuilder/internal/installer"
)

func newAddCmd() *cobra.Command {
	var flags installFlags
	cmd := &cobra.Command{
		Use:   "add [skill...]",
		Short: "Install individual skills",
		Long: "Install skills by name. With no names, pick from everything in the\n" +
			"registry, which needs a terminal, so pass names explicitly in CI.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdd(cmd, args, flags)
		},
	}
	flags.register(cmd)
	return cmd
}

func runAdd(cmd *cobra.Command, names []string, flags installFlags) error {
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

	if len(names) == 0 {
		if !interactive() {
			return errors.New("stdin is not a terminal, so the skill picker cannot run. Name the skills to add, e.g. `skillbuilder add brand-design`")
		}
		resp, err := client.ListSkills(ctx, "", "")
		if err != nil {
			return err
		}
		if len(resp.Skills) == 0 {
			fmt.Fprintln(out, dimStyle.Render("the registry has no skills yet"))
			return nil
		}
		if names, err = selectSkills(fmt.Sprintf("%d skills in the registry", len(resp.Skills)), resp.Skills, false); err != nil {
			return err
		}
		if len(names) == 0 {
			fmt.Fprintln(out, dimStyle.Render("no skills selected"))
			return nil
		}
	}

	dir := flags.resolveDir(root)
	results, err := installer.Install(ctx, client, names, flags.optionsIn(dir))
	if err != nil {
		if installer.IsNotFound(err) {
			return fmt.Errorf("the registry does not have all of %v. Try `skillbuilder search`: %w", names, err)
		}
		return err
	}
	printResults(out, results, dir, flags.dryRun)
	if flags.dryRun {
		return nil
	}

	return recordInstall(root, client.BaseURL, "", dir, results)
}
