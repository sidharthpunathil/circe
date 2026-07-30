package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/sidharthpunathil/circe/internal/installer"
	"github.com/sidharthpunathil/circe/internal/store"
)

// MCP servers install differently again: a skill is a directory copied into
// .claude/skills, a rule is prose merged into a file the project already owns,
// and a server is an entry in the project's .mcp.json. That file is usually
// already there and already working, so this command merges into it and never
// touches an entry it did not put there.

// mcpConfigName is where Claude Code looks for project scoped servers.
const mcpConfigName = ".mcp.json"

func newMCPCmd() *cobra.Command {
	var (
		opts     mcpOptions
		yes, all bool
	)
	cmd := &cobra.Command{
		Use:   "mcp <folder>",
		Short: "Add a folder of MCP servers to this project",
		Long: "Merge a folder of MCP servers into this project's .mcp.json.\n\n" +
			"Servers the project already has are left exactly as they are and reported,\n" +
			"never replaced, unless you pass --force. Keys we do not model are carried\n" +
			"through untouched.\n\n" +
			"Without a terminal (CI, a pipe) there is nothing to pick with, so pass -y.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return listMCPFolders(cmd)
			}
			opts.takeAll = yes || all || opts.force
			return runMCP(cmd, args[0], opts)
		},
	}
	cmd.Flags().StringVarP(&opts.out, "out", "o", mcpConfigName, "config file to merge the servers into")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "install every server in the folder without prompting")
	cmd.Flags().BoolVar(&all, "all", false, "alias for --yes")
	cmd.Flags().BoolVar(&opts.force, "force", false,
		"take the whole folder and replace servers this project already has")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "report what would change without writing anything")
	return cmd
}

// mcpOptions are the flags one run of `mcp <folder>` works from.
type mcpOptions struct {
	out     string
	takeAll bool
	force   bool
	dryRun  bool
}

// mcpSetManifest mirrors what GET /api/v1/mcpsets/{name} returns.
type mcpSetManifest struct {
	Name        string             `json:"name"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	Servers     []*store.MCPServer `json:"servers"`
}

func (m *mcpSetManifest) label() string {
	if strings.TrimSpace(m.Title) != "" {
		return m.Title
	}
	return m.Name
}

func runMCP(cmd *cobra.Command, folder string, opts mcpOptions) error {
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

	manifest, err := fetchMCPSet(ctx, client, folder)
	if err != nil {
		return err
	}
	if len(manifest.Servers) == 0 {
		return fmt.Errorf("mcp folder %q has nothing in it yet", folder)
	}

	fmt.Fprintln(out, dimStyle.Render(fmt.Sprintf("%s · %d %s",
		manifest.label(), len(manifest.Servers),
		pluralWord(len(manifest.Servers), "server", "servers"))))

	chosen, err := chooseMCPServers(manifest, opts.takeAll)
	if err != nil {
		return err
	}
	if len(chosen) == 0 {
		fmt.Fprintln(out, dimStyle.Render("no servers selected"))
		return nil
	}

	// The default target is relative to where the developer is standing, because
	// .mcp.json is project scoped and the project is the current directory.
	target := opts.out
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}

	results, written, err := applyMCPServers(target, chosen, opts.force, opts.dryRun)
	if err != nil {
		return err
	}
	printMCPResults(out, results, opts.out, opts.dryRun)
	printMCPEnv(out, written, os.Getenv)
	return nil
}

// fetchMCPSet reads a server folder from the registry.
func fetchMCPSet(ctx context.Context, c *installer.Client, name string) (*mcpSetManifest, error) {
	body, err := getRaw(ctx, c, "/api/v1/mcpsets/"+name)
	if err != nil {
		if installer.IsNotFound(err) {
			return nil, fmt.Errorf("no mcp folder named %q at %s. Try `circe mcp` to list them: %w",
				name, c.BaseURL, err)
		}
		return nil, err
	}
	var m mcpSetManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("decode mcp folder %q: %w", name, err)
	}
	return &m, nil
}

func listMCPFolders(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	client, err := newClient()
	if err != nil {
		return err
	}
	body, err := getRaw(cmd.Context(), client, "/api/v1/mcpsets")
	if err != nil {
		return err
	}
	var resp struct {
		MCPSets []struct {
			Name        string   `json:"name"`
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Servers     []string `json:"servers"`
		} `json:"mcpsets"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return err
	}
	if len(resp.MCPSets) == 0 {
		fmt.Fprintln(out, dimStyle.Render("no mcp folders in the registry yet"))
		return nil
	}
	sort.Slice(resp.MCPSets, func(i, j int) bool { return resp.MCPSets[i].Name < resp.MCPSets[j].Name })

	col := 0
	for _, s := range resp.MCPSets {
		if len(s.Name) > col {
			col = len(s.Name)
		}
	}
	for _, s := range resp.MCPSets {
		pad := strings.Repeat(" ", col-len(s.Name))
		desc := s.Description
		if desc == "" {
			desc = s.Title
		}
		fmt.Fprintf(out, "  %s%s  %s\n", s.Name, pad,
			dimStyle.Render(fmt.Sprintf("%d %s  %s", len(s.Servers),
				pluralWord(len(s.Servers), "server", "servers"), truncate(desc, 48))))
	}
	return nil
}

// chooseMCPServers picks which of the folder's servers to install.
func chooseMCPServers(manifest *mcpSetManifest, takeAll bool) ([]*store.MCPServer, error) {
	if takeAll {
		return manifest.Servers, nil
	}
	if !interactive() {
		return nil, errors.New("stdin is not a terminal, so the server picker cannot run. " +
			"Pass -y to install every server in the folder")
	}
	// A folder is already a curated set, so everything starts selected and the
	// user opts out rather than in.
	names, err := selectMCPServers(fmt.Sprintf("%s · %d %s", manifest.label(),
		len(manifest.Servers), pluralWord(len(manifest.Servers), "server", "servers")),
		manifest.Servers)
	if err != nil {
		return nil, err
	}
	keep := make(map[string]bool, len(names))
	for _, n := range names {
		keep[n] = true
	}
	var chosen []*store.MCPServer
	for _, m := range manifest.Servers {
		if keep[m.Name] {
			chosen = append(chosen, m)
		}
	}
	return chosen, nil
}

// selectMCPServers shows the multi-select picker, seeded with everything.
func selectMCPServers(title string, servers []*store.MCPServer) ([]string, error) {
	width := termWidth()
	names := make([]string, len(servers))
	for i, m := range servers {
		names[i] = m.Name
	}
	col := nameColumn(names)

	opts := make([]huh.Option[string], 0, len(servers))
	for _, m := range servers {
		label := pad(m.Name, col)
		if desc := truncate(m.Description, width-col-14); desc != "" {
			label += "  " + desc
		}
		opts = append(opts, huh.NewOption(label, m.Name).Selected(true))
	}

	height := len(opts) + 4
	if height > 20 {
		height = 20
	}

	var chosen []string
	err := huh.NewMultiSelect[string]().
		Title(title).
		Description("space toggles · ctrl+a all · / filters · enter confirms").
		Options(opts...).
		Filterable(true).
		Height(height).
		Value(&chosen).
		Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return nil, errCancelled
	}
	if err != nil {
		return nil, fmt.Errorf("server picker: %w", err)
	}
	sort.Strings(chosen)
	return chosen, nil
}

// mcpResult is what happened to one server in the project's config.
type mcpResult struct {
	Name   string
	Action string // added, replaced or kept
	Reason string
}

// applyMCPServers merges servers into the config at path and reports both what
// changed and which servers were actually written, so the caller can tell the
// developer which environment variables those servers still want.
func applyMCPServers(path string, servers []*store.MCPServer, force, dryRun bool) ([]mcpResult, []*store.MCPServer, error) {
	existing, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		existing = nil
	case err != nil:
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	// A file someone created and never filled in is the same as no file.
	if len(bytes.TrimSpace(existing)) == 0 {
		existing = nil
	}

	// Read what the file already says first: the merge silently declines a
	// collision, and a collision is exactly what the developer needs told.
	present, err := mcpServerEntries(existing)
	if err != nil {
		return nil, nil, mcpConfigError(path, err)
	}

	merged, replaced, err := store.MergeMCPConfig(existing, servers, force)
	if err != nil {
		return nil, nil, mcpConfigError(path, err)
	}
	wasReplaced := make(map[string]bool, len(replaced))
	for _, n := range replaced {
		wasReplaced[n] = true
	}

	var results []mcpResult
	var written []*store.MCPServer
	changed := 0
	for _, m := range servers {
		current, exists := present[m.Name]
		switch {
		case exists && sameMCPEntry(current, m.Config()):
			// The file already says exactly this, so claiming a change would be
			// a lie either way round.
			results = append(results, mcpResult{Name: m.Name, Action: "unchanged"})
			written = append(written, m)
		case wasReplaced[m.Name]:
			results = append(results, mcpResult{Name: m.Name, Action: "replaced", Reason: "was already in this project"})
			written = append(written, m)
			changed++
		case exists:
			results = append(results, mcpResult{Name: m.Name, Action: "kept",
				Reason: "this project already has one; --force replaces it"})
		default:
			results = append(results, mcpResult{Name: m.Name, Action: "added"})
			written = append(written, m)
			changed++
		}
	}

	// Nothing new to say means nothing to write: reformatting a file to record
	// no change is a diff the developer did not ask for.
	if dryRun || changed == 0 {
		return results, written, nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, merged, 0o644); err != nil {
		return nil, nil, fmt.Errorf("write %s: %w", path, err)
	}
	return results, written, nil
}

// mcpConfigError explains an unusable config file in terms of what to do about
// it, because the file is the developer's and only they can decide.
func mcpConfigError(path string, err error) error {
	return fmt.Errorf("cannot merge into %s. Fix that file or move it aside, then run this again: %w", path, err)
}

// mcpServerEntries returns the servers a config already defines, as written.
func mcpServerEntries(raw []byte) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return out, nil
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	for name, entry := range doc.MCPServers {
		out[name] = entry
	}
	return out, nil
}

// sameMCPEntry reports whether the file already says exactly what we would
// write. Key order and whitespace are not differences, so compare the values.
func sameMCPEntry(current json.RawMessage, next map[string]any) bool {
	raw, err := json.Marshal(next)
	if err != nil {
		return false
	}
	var a, b any
	if json.Unmarshal(current, &a) != nil || json.Unmarshal(raw, &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

// mcpMark returns the leading glyph and label style for one result.
func mcpMark(action string) (string, lipgloss.Style) {
	switch action {
	case "added", "replaced":
		return okStyle.Render("✓"), okStyle
	case "kept":
		return warnStyle.Render("!"), warnStyle
	default: // unchanged
		return dimStyle.Render("·"), dimStyle
	}
}

// printMCPResults renders one line per server plus a summary.
func printMCPResults(w io.Writer, results []mcpResult, path string, dryRun bool) {
	if len(results) == 0 {
		fmt.Fprintln(w, dimStyle.Render("nothing to install"))
		return
	}
	names := make([]string, len(results))
	for i, r := range results {
		names[i] = r.Name
	}
	col := nameColumn(names)
	width := termWidth()

	counts := map[string]int{}
	for _, r := range results {
		counts[r.Action]++
		mark, style := mcpMark(r.Action)
		line := "  " + mark + " " + pad(r.Name, col) + "  " + style.Render(r.Action)
		if r.Reason != "" {
			if reason := truncate(r.Reason, width-lipgloss.Width(line)-4); reason != "" {
				line += dimStyle.Render(" · " + reason)
			}
		}
		fmt.Fprintln(w, line)
	}

	var parts []string
	for _, action := range []string{"added", "replaced", "unchanged", "kept"} {
		if n := counts[action]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, action))
		}
	}
	summary := strings.Join(parts, ", ")
	if dryRun {
		fmt.Fprintln(w, dimStyle.Render("dry run: "+summary+"; nothing was written"))
		return
	}
	fmt.Fprintln(w, dimStyle.Render(summary+" in "+path))
}

// mcpEnvNeed is one environment variable the written servers reference and the
// developer's shell does not have.
type mcpEnvNeed struct {
	Name     string
	Servers  []string
	Help     string
	Required bool
}

// missingMCPEnv finds the variables the config points at with ${NAME} that are
// not set. Without them the server starts and immediately fails, which is a
// confusing way to find out, so it is worth saying up front.
func missingMCPEnv(servers []*store.MCPServer, lookup func(string) string) []mcpEnvNeed {
	index := map[string]*mcpEnvNeed{}
	var order []string
	for _, m := range servers {
		for _, e := range m.Env {
			if e.Name == "" || strings.TrimSpace(lookup(e.Name)) != "" {
				continue
			}
			need, ok := index[e.Name]
			if !ok {
				need = &mcpEnvNeed{Name: e.Name, Help: e.Help}
				index[e.Name] = need
				order = append(order, e.Name)
			}
			need.Servers = append(need.Servers, m.Name)
			need.Required = need.Required || e.Required
			if need.Help == "" {
				need.Help = e.Help
			}
		}
	}

	out := make([]mcpEnvNeed, 0, len(order))
	for _, name := range order {
		out = append(out, *index[name])
	}
	// Required first, because those are the ones that stop a server dead.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Required != out[j].Required {
			return out[i].Required
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// printMCPEnv is the most useful thing this command prints: the config only
// references ${NAME}, so a server with an unset variable will not start.
func printMCPEnv(w io.Writer, servers []*store.MCPServer, lookup func(string) string) {
	declared := 0
	for _, m := range servers {
		declared += len(m.Env)
	}
	if declared == 0 {
		return
	}
	missing := missingMCPEnv(servers, lookup)
	if len(missing) == 0 {
		fmt.Fprintln(w, dimStyle.Render("every environment variable these servers need is already set"))
		return
	}

	names := make([]string, len(missing))
	for i, e := range missing {
		names[i] = e.Name
	}
	col := nameColumn(names)
	width := termWidth()

	fmt.Fprintln(w, warnStyle.Render("set these before the servers will start:"))
	for _, e := range missing {
		notes := []string{strings.Join(e.Servers, ", ")}
		if !e.Required {
			notes = append(notes, "optional")
		}
		if e.Help != "" {
			notes = append(notes, e.Help)
		}
		line := "  " + nameStyle.Render(pad(e.Name, col))
		if note := truncate(strings.Join(notes, " · "), width-col-6); note != "" {
			line += "  " + dimStyle.Render(note)
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w, dimStyle.Render("export them in your shell, then restart Claude Code so it picks them up"))
}
