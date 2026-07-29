// Command skillbuilder installs skills from a personal registry into a project.
//
// Skills are written to ./.claude/skills/<name>/ and recorded in
// ./skillbuilder.json, so any checkout of the project can be brought back to the
// same set with a single `skillbuilder pull`.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/sidharthp/skillbuilder/internal/installer"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// A download or a prompt should die on the first Ctrl-C rather than run to
	// completion after the user has clearly asked it to stop.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		os.Exit(report(err))
	}
}

// report prints an error the way a CLI should — one line, no stack, plus a hint
// when there is an obvious next move — and returns the exit code.
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
		Use:   "skillbuilder",
		Short: "Install skills from your personal registry",
		Long: "skillbuilder installs skills from your personal registry into a project.\n\n" +
			"Skills land in .claude/skills/ and are recorded in skillbuilder.json so the\n" +
			"same set can be restored anywhere with `skillbuilder pull`.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&registryFlag, "registry", "",
		"registry base URL (overrides $"+installer.EnvRegistry+" and the config file)")

	root.AddCommand(
		newInitCmd(),
		newAddCmd(),
		newListCmd(),
		newSearchCmd(),
		newPullCmd(),
		newRemoveCmd(),
		newPushCmd(),
		newConfigCmd(),
	)
	return root
}
