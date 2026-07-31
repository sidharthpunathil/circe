// Command circe installs a folder from a personal registry into a project.
//
// A folder holds any mix of skills, rules and MCP servers. Skills are written
// to ./.claude/skills/<name>/ and recorded in ./circe.json, so any checkout of
// the project can be brought back to the same set with a single `circe pull`.
// Rules merge into ./CLAUDE.md and ./AGENTS.md, servers into ./.mcp.json.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sidharthpunathil/circe/internal/installer"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// A download or a prompt should die on the first Ctrl-C rather than run to
	// completion after the user has clearly asked it to stop.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	root := newRootCmd()
	root.SetArgs(expandShorthand(root, os.Args[1:]))
	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(report(err))
	}
}

// report prints an error the way a CLI should: one line, no stack, plus a hint
// when there is an obvious next move. It returns the exit code.
func report(err error) int {
	if errors.Is(err, errCancelled) || errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, dimStyle.Render("cancelled"))
		return 130
	}
	fmt.Fprintln(os.Stderr, errStyle.Render("error:")+" "+err.Error())

	var unreachable *installer.UnreachableError
	if errors.As(err, &unreachable) {
		fmt.Fprintln(os.Stderr, dimStyle.Render("  start the registry, or point elsewhere with --registry <url>"))
	}
	return 1
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "circe",
		Short: "Install skills, rules and MCP servers from your personal registry",
		Long: "circe installs a folder from your personal registry into a project.\n\n" +
			"A folder holds any mix of skills, rules and MCP servers. `circe <folder>`\n" +
			"installs all of it: skills into .claude/skills, rules into CLAUDE.md and\n" +
			"AGENTS.md, servers into .mcp.json. `circe skills`, `circe rules` and\n" +
			"`circe mcp` install one kind only.\n\n" +
			"Installed skills are recorded in circe.json so the same set can be\n" +
			"restored anywhere with `circe pull`.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&registryFlag, "registry", "",
		"registry base URL (overrides $"+installer.EnvRegistry+" and the config file)")

	root.AddCommand(
		newInitCmd(),
		newSkillsCmd(),
		newRulesCmd(),
		newMCPCmd(),
		newAddCmd(),
		newListCmd(),
		newSearchCmd(),
		newPullCmd(),
		newRemoveCmd(),
		newPushCmd(),
		newImportCmd(),
		newLoginCmd(),
		newLogoutCmd(),
		newConfigCmd(),
	)

	return root
}

// expandShorthand rewrites `circe <folder> [flags]` into
// `circe init <folder> [flags]`, so installing a folder is just its name. Done
// before cobra parses, because otherwise the init flags (-y, -d, …) would be
// rejected as unknown root flags.
func expandShorthand(root *cobra.Command, args []string) []string {
	if len(args) == 0 {
		return args
	}
	first := args[0]
	if strings.HasPrefix(first, "-") {
		return args // a flag such as --help or --version
	}
	if cmd, _, err := root.Find([]string{first}); err == nil && cmd != root {
		return args // a real command
	}
	return append([]string{"init"}, args...)
}
