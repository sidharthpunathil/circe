package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sidharthpunathil/skillbuilder/internal/api"
	"github.com/sidharthpunathil/skillbuilder/internal/installer"
)

func newListCmd() *cobra.Command {
	var (
		remote bool
		dir    string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the skills installed in this project",
		Long: "List what the project lockfile says is installed, flagging anything that\n" +
			"has been edited or deleted since. With --remote, list the registry instead.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if remote {
				return runListRemote(cmd, dir)
			}
			return runListLocal(cmd, dir, cmd.Flags().Changed("dir"))
		},
	}
	cmd.Flags().BoolVar(&remote, "remote", false, "list the registry's skills instead of this project's")
	cmd.Flags().StringVarP(&dir, "dir", "d", installer.DefaultSkillsDir, "directory skills are installed in")
	return cmd
}

func runListLocal(cmd *cobra.Command, dir string, dirSet bool) error {
	out := cmd.OutOrStdout()

	root, err := projectRoot()
	if err != nil {
		return err
	}
	lf, err := installer.LoadLockfile(root)
	if err != nil {
		return err
	}
	dir = installer.ResolveDir(dir, dirSet, lf)
	skills := installer.LockedSkills(lf)
	if len(skills) == 0 {
		fmt.Fprintln(out, dimStyle.Render("no skills installed — run `skillbuilder init <bundle>` or `skillbuilder add <skill>`"))
		return nil
	}

	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	col := nameColumn(names)

	drifted := 0
	for _, s := range skills {
		state, err := installer.Drift(dir, s)
		if err != nil {
			return err
		}
		mark := okStyle.Render("✓")
		if state != installer.DriftNone {
			mark = warnStyle.Render("!")
			drifted++
		}
		line := "  " + mark + " " + pad(s.Name, col)
		var notes []string
		if s.Bundle != "" {
			notes = append(notes, s.Bundle)
		}
		if state != installer.DriftNone {
			notes = append(notes, string(state))
		}
		if len(notes) > 0 {
			line += "  " + dimStyle.Render(strings.Join(notes, " · "))
		}
		fmt.Fprintln(out, strings.TrimRight(line, " "))
	}

	summary := fmt.Sprintf("%d installed in %s", len(skills), dir)
	if drifted > 0 {
		summary += fmt.Sprintf(", %d changed since install — `skillbuilder pull --force` restores them", drifted)
	}
	fmt.Fprintln(out, dimStyle.Render(summary))
	return nil
}

func runListRemote(cmd *cobra.Command, dir string) error {
	out := cmd.OutOrStdout()

	client, err := newClient()
	if err != nil {
		return err
	}
	resp, err := client.ListSkills(cmd.Context(), "", "")
	if err != nil {
		return err
	}
	if len(resp.Skills) == 0 {
		fmt.Fprintln(out, dimStyle.Render("the registry has no skills yet"))
		return nil
	}

	installed := installedNames()
	printSkills(out, resp.Skills, func(s api.Skill) string {
		if installed[s.Name] {
			return okStyle.Render("✓") + " "
		}
		return "  "
	})
	fmt.Fprintln(out, dimStyle.Render(fmt.Sprintf("%d skills at %s", resp.Total, client.BaseURL)))
	return nil
}

// installedNames is best effort: marking what is already in the project is a
// nicety, and failing to read the lockfile should not fail `list --remote`.
func installedNames() map[string]bool {
	root, err := projectRoot()
	if err != nil {
		return nil
	}
	lf, err := installer.LoadLockfile(root)
	if err != nil {
		return nil
	}
	out := make(map[string]bool, len(lf.Skills))
	for name := range lf.Skills {
		out[name] = true
	}
	return out
}
