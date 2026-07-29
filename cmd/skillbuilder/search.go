package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sidharthp/skillbuilder/internal/api"
)

func newSearchCmd() *cobra.Command {
	var tag string
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search the registry",
		Long:  "Search the registry by free text, by tag, or both.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Multi-word queries are normal, so accept them unquoted.
			return runSearch(cmd, strings.Join(args, " "), tag)
		},
	}
	cmd.Flags().StringVar(&tag, "tag", "", "only skills carrying this tag")
	return cmd
}

func runSearch(cmd *cobra.Command, query, tag string) error {
	out := cmd.OutOrStdout()
	if query == "" && tag == "" {
		return errors.New("give something to search for, e.g. `skillbuilder search design` or `--tag writing`")
	}

	client, err := newClient()
	if err != nil {
		return err
	}
	resp, err := client.ListSkills(cmd.Context(), query, tag)
	if err != nil {
		return err
	}
	if len(resp.Skills) == 0 {
		fmt.Fprintln(out, dimStyle.Render("no skills match "+describeQuery(query, tag)))
		return nil
	}

	installed := installedNames()
	printSkills(out, resp.Skills, func(s api.Skill) string {
		if installed[s.Name] {
			return okStyle.Render("✓") + " "
		}
		return "  "
	})
	fmt.Fprintln(out, dimStyle.Render(fmt.Sprintf("%d of %d skills match %s", len(resp.Skills), resp.Total, describeQuery(query, tag))))
	return nil
}

func describeQuery(query, tag string) string {
	switch {
	case query != "" && tag != "":
		return fmt.Sprintf("%q tagged %q", query, tag)
	case tag != "":
		return fmt.Sprintf("tag %q", tag)
	default:
		return fmt.Sprintf("%q", query)
	}
}
