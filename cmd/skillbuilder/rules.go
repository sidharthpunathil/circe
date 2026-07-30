package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sidharthpunathil/skillbuilder/internal/installer"
)

// Rules install differently from skills: a skill is a directory copied into
// .claude/skills, a rule is prose merged into a file the project already owns.
// They therefore get their own verb rather than being folded into `add`.

func newRulesCmd() *cobra.Command {
	var (
		contextMode string
		dryRun      bool
	)
	cmd := &cobra.Command{
		Use:   "rules <folder>",
		Short: "Add a folder of rules to this project",
		Long: "Merge a rule folder into this project's CLAUDE.md and AGENTS.md.\n\n" +
			"Your own content is left alone: the rules go in their own marked block,\n" +
			"which is replaced rather than duplicated when you run this again.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return listRuleFolders(cmd)
			}
			return runRules(cmd, args[0], contextMode, dryRun)
		},
	}
	cmd.Flags().StringVar(&contextMode, "mode", "merge",
		"how to apply: merge, skip or overwrite")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without writing")
	return cmd
}

// ruleSetManifest mirrors what GET /api/v1/rulesets/{name} returns.
type ruleSetManifest struct {
	Set struct {
		Name        string   `json:"name"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Rules       []string `json:"rules"`
	} `json:"set"`
	Rules []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"rules"`
	Rendered map[string]string `json:"rendered"`
}

func runRules(cmd *cobra.Command, folder, mode string, dryRun bool) error {
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

	manifest, err := fetchRuleSet(ctx, client, folder)
	if err != nil {
		return err
	}
	if len(manifest.Rendered) == 0 {
		return fmt.Errorf("rule folder %q has nothing in it yet", folder)
	}

	label := manifest.Set.Title
	if label == "" {
		label = manifest.Set.Name
	}
	fmt.Fprintln(out, dimStyle.Render(fmt.Sprintf("%s · %d %s",
		label, len(manifest.Rules), pluralWord(len(manifest.Rules), "rule", "rules"))))

	if err := applyContext(out, root, folder, manifest.Rendered, mode, dryRun); err != nil {
		return err
	}
	if dryRun {
		fmt.Fprintln(out, dimStyle.Render("dry run: nothing was written"))
	}
	return nil
}

// fetchRuleSet reads a rule folder from the registry.
func fetchRuleSet(ctx context.Context, c *installer.Client, name string) (*ruleSetManifest, error) {
	body, err := getRaw(ctx, c, "/api/v1/rulesets/"+name)
	if err != nil {
		if installer.IsNotFound(err) {
			return nil, fmt.Errorf("no rule folder named %q at %s. Try `skillbuilder rules` to list them: %w",
				name, c.BaseURL, err)
		}
		return nil, err
	}
	var m ruleSetManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("decode rule folder %q: %w", name, err)
	}
	return &m, nil
}

func listRuleFolders(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	client, err := newClient()
	if err != nil {
		return err
	}
	body, err := getRaw(cmd.Context(), client, "/api/v1/rulesets")
	if err != nil {
		return err
	}
	var resp struct {
		RuleSets []struct {
			Name        string   `json:"name"`
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Rules       []string `json:"rules"`
		} `json:"rulesets"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return err
	}
	if len(resp.RuleSets) == 0 {
		fmt.Fprintln(out, dimStyle.Render("no rule folders in the registry yet"))
		return nil
	}
	sort.Slice(resp.RuleSets, func(i, j int) bool { return resp.RuleSets[i].Name < resp.RuleSets[j].Name })

	col := 0
	for _, s := range resp.RuleSets {
		if len(s.Name) > col {
			col = len(s.Name)
		}
	}
	for _, s := range resp.RuleSets {
		pad := strings.Repeat(" ", col-len(s.Name))
		desc := s.Description
		if desc == "" {
			desc = s.Title
		}
		fmt.Fprintf(out, "  %s%s  %s\n", s.Name, pad,
			dimStyle.Render(fmt.Sprintf("%d %s  %s", len(s.Rules),
				pluralWord(len(s.Rules), "rule", "rules"), truncate(desc, 48))))
	}
	return nil
}

// getRaw performs an authenticated GET and returns the body.
func getRaw(ctx context.Context, c *installer.Client, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "skillbuilder-cli")
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

// newSkillsCmd is the explicit counterpart to `skillbuilder rules`, so the two
// resource types read symmetrically.
func newSkillsCmd() *cobra.Command {
	var (
		flags       installFlags
		yes, all    bool
		contextMode string
	)
	cmd := &cobra.Command{
		Use:   "skills <folder>",
		Short: "Add a folder of skills to this project",
		Long: "Install the skills in a folder into this project.\n\n" +
			"Same as `skillbuilder <folder>`, spelled out for symmetry with\n" +
			"`skillbuilder rules <folder>`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(cmd, args[0], yes || all, flags, contextMode)
		},
	}
	flags.register(cmd)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "install every skill in the folder without prompting")
	cmd.Flags().BoolVar(&all, "all", false, "alias for --yes")
	cmd.Flags().StringVar(&contextMode, "context", "merge",
		"how to apply the folder's CLAUDE.md/AGENTS.md: merge, skip or overwrite")
	return cmd
}
