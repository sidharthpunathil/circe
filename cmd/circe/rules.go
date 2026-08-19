package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sidharthpunathil/circe/internal/installer"
	"github.com/sidharthpunathil/circe/internal/model"
)

// Rules install differently from skills: a skill is a directory copied into
// .claude/skills, a rule is prose merged into a file the project already owns.
// They therefore get their own verb rather than being folded into `add`.

func newRulesCmd() *cobra.Command {
	var opts folderOptions
	cmd := &cobra.Command{
		Use:   "rules [folder]",
		Short: "Add a folder's rules to this project",
		Long: "Merge a folder's rules into this project's CLAUDE.md and AGENTS.md,\n" +
			"leaving its skills and MCP servers alone.\n\n" +
			"Your own content is left alone too: the rules go in their own marked\n" +
			"block, which is replaced rather than duplicated when you run this again.\n" +
			"The folder's own CLAUDE.md and AGENTS.md go into the same block.\n\n" +
			"With no folder, lists the folders that have rules in them.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return listFolders(cmd, kindRules)
			}
			return runFolder(cmd, args[0], kindRules, &opts)
		},
	}
	opts.register(cmd, kindRules)
	// The flag was called --mode when rules had a folder type of their own.
	cmd.Flags().StringVar(&opts.context, "mode", "merge", "alias for --context")
	_ = cmd.Flags().MarkHidden("mode")
	return cmd
}

// installFolderRules merges the folder's guidance into the project's own
// CLAUDE.md and AGENTS.md. It returns its share of the combined summary.
func installFolderRules(out io.Writer, root string, m *folderManifest,
	opts *folderOptions, withRules bool) (string, error) {

	files := folderGuidance(m, withRules)
	if len(files) == 0 {
		return "", nil
	}
	if err := applyContext(out, root, m.Name, files, opts.context, opts.dryRun); err != nil {
		return "", err
	}
	if !withRules || len(m.Rules) == 0 {
		return "", nil
	}
	return fmt.Sprintf("%d %s", len(m.Rules), pluralWord(len(m.Rules), "rule", "rules")), nil
}

// folderGuidance is everything the folder says to the agent, keyed by the file
// it says it in: the folder's own CLAUDE.md and AGENTS.md first, then its rules
// in folder order, so the author controls the sequence the agent reads them in.
//
// It is one string per file because it lands in one marked block per folder,
// which is what makes re-installing replace rather than duplicate.
func folderGuidance(m *folderManifest, withRules bool) map[string]string {
	out := map[string]string{}
	for file, body := range m.Context {
		if strings.TrimSpace(body) != "" {
			out[file] = strings.TrimRight(body, "\n")
		}
	}
	if !withRules {
		return out
	}
	for _, target := range model.RuleTargets() {
		var parts []string
		for _, r := range m.Rules {
			// A rule that only targets AGENTS.md never lands in CLAUDE.md.
			if r.Writes(target) {
				parts = append(parts, strings.TrimRight(r.Body, "\n"))
			}
		}
		if len(parts) == 0 {
			continue
		}
		body := strings.Join(parts, "\n\n")
		if existing := out[target]; existing != "" {
			body = existing + "\n\n" + body
		}
		out[target] = body
	}
	return out
}

// getRaw performs an authenticated GET and returns the body.
func getRaw(ctx context.Context, c *installer.Client, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "circe-cli")
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &installer.UnreachableError{Registry: c.BaseURL, Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, &installer.UnauthorizedError{Registry: c.BaseURL}
	}
	if resp.StatusCode/100 != 2 {
		return nil, &installer.APIError{Status: resp.StatusCode, URL: c.BaseURL + path}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}
