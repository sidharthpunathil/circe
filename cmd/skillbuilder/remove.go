package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sidharthpunathil/skillbuilder/internal/installer"
)

func newRemoveCmd() *cobra.Command {
	var (
		dir    string
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:     "remove <skill>...",
		Aliases: []string{"rm"},
		Short:   "Delete skills from this project",
		Long:    "Delete skill directories and drop them from the lockfile.",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRemove(cmd, args, dir, cmd.Flags().Changed("dir"), dryRun)
		},
	}
	cmd.Flags().StringVarP(&dir, "dir", "d", installer.DefaultSkillsDir, "directory skills are installed in")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be deleted without deleting it")
	return cmd
}

func runRemove(cmd *cobra.Command, names []string, dir string, dirSet bool, dryRun bool) error {
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

	col := nameColumn(names)
	removed := 0
	for _, name := range names {
		_, locked := lf.Skills[name]
		onDisk := skillExists(dir, name)
		if !dryRun {
			if onDisk, err = installer.Remove(dir, name); err != nil {
				return err
			}
			installer.ForgetSkill(lf, name)
		}

		label := "removed"
		if dryRun {
			label = "would remove"
		}
		switch {
		case onDisk || locked:
			removed++
			fmt.Fprintln(out, "  "+okStyle.Render("✓")+" "+pad(name, col)+"  "+okStyle.Render(label))
		default:
			fmt.Fprintln(out, "  "+warnStyle.Render("!")+" "+pad(name, col)+"  "+warnStyle.Render("not installed"))
		}
	}

	if dryRun {
		fmt.Fprintln(out, dimStyle.Render(fmt.Sprintf("dry run — %d of %d would be removed; nothing was deleted", removed, len(names))))
		return nil
	}
	fmt.Fprintln(out, dimStyle.Render(fmt.Sprintf("%d removed from %s", removed, dir)))
	return installer.SaveLockfile(root, lf)
}

func skillExists(dir, name string) bool {
	info, err := os.Stat(installer.SkillDir(dir, name))
	return err == nil && info.IsDir()
}
