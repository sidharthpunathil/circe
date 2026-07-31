package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/sidharthpunathil/circe/internal/installer"
)

func newInitCmd() *cobra.Command {
	var opts folderOptions
	cmd := &cobra.Command{
		Use:   "init <folder>",
		Short: "Install everything in a folder into this project",
		Long: "Install a folder: its skills into .claude/skills, its rules into CLAUDE.md\n" +
			"and AGENTS.md, its MCP servers into .mcp.json.\n\n" +
			"`circe <folder>` is this command with the verb left out. To install one\n" +
			"kind only, use `circe skills`, `circe rules` or `circe mcp`.\n\n" +
			"Without a terminal (CI, a pipe) there is nothing to pick with, so pass -y.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFolder(cmd, args[0], kindAll, &opts)
		},
	}
	opts.register(cmd, kindAll)
	return cmd
}

// newSkillsCmd narrows the install to the folder's skills, and is the
// counterpart to `circe rules` and `circe mcp`.
func newSkillsCmd() *cobra.Command {
	var opts folderOptions
	cmd := &cobra.Command{
		Use:   "skills [folder]",
		Short: "Add a folder's skills to this project",
		Long: "Install the skills in a folder, leaving its rules and MCP servers alone.\n\n" +
			"With no folder, lists the folders that have skills in them.\n\n" +
			"Without a terminal (CI, a pipe) there is nothing to pick with, so pass -y.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return listFolders(cmd, kindSkills)
			}
			return runFolder(cmd, args[0], kindSkills, &opts)
		},
	}
	opts.register(cmd, kindSkills)
	return cmd
}

// installFolderSkills writes the folder's skills into the project and records
// them in the lockfile. It returns its share of the combined summary.
func installFolderSkills(ctx context.Context, out io.Writer, client *installer.Client,
	root string, m *folderManifest, opts *folderOptions) (string, error) {

	chosen, err := chooseFolderSkills(m, opts.everything())
	if err != nil {
		return "", err
	}
	if len(chosen) == 0 {
		fmt.Fprintln(out, dimStyle.Render("no skills selected"))
		return "", nil
	}

	dir := opts.resolveDir(root)
	results, err := installSelection(ctx, client, m.Name, chosen, len(chosen) == len(m.Skills), opts.optionsIn(dir))
	if err != nil {
		return "", err
	}
	printResults(out, results, dir, opts.dryRun)

	inPlace := 0
	for _, r := range results {
		if r.Status != installer.StatusSkipped {
			inPlace++
		}
	}
	part := fmt.Sprintf("%d %s", inPlace, pluralWord(inPlace, "skill", "skills"))
	if opts.dryRun {
		return part, nil
	}
	if err := recordInstall(root, client.BaseURL, m.Name, dir, results); err != nil {
		return "", err
	}
	return part, nil
}

func chooseFolderSkills(m *folderManifest, takeAll bool) ([]string, error) {
	if takeAll {
		return skillNames(m.Skills), nil
	}
	if !interactive() {
		return nil, errors.New("stdin is not a terminal, so the skill picker cannot run. Pass -y to install every skill in the folder")
	}
	// A folder is already a curated set, so everything starts selected and the
	// user opts out rather than in.
	return selectSkills(fmt.Sprintf("%s · %d skills", m.label(), len(m.Skills)), m.Skills, true)
}

// installSelection downloads through the folder archive when every skill was
// chosen, since that is one request the server can cache, and falls back to the
// per-skill download endpoint for a subset.
func installSelection(ctx context.Context, client *installer.Client, folder string, names []string, whole bool, opts installer.Options) ([]installer.Result, error) {
	if whole {
		return installer.InstallBundle(ctx, client, folder, opts)
	}
	return installer.Install(ctx, client, names, opts)
}
