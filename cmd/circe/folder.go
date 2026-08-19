package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sidharthpunathil/circe/internal/api"
	"github.com/sidharthpunathil/circe/internal/installer"
	"github.com/sidharthpunathil/circe/internal/model"
)

// A folder is one thing that holds any mix of skills, rules and MCP servers,
// so one request to /api/v1/bundles/{name} brings back everything it can
// install. `circe <folder>` installs all of it; `circe skills`, `circe rules`
// and `circe mcp` run the same install narrowed to one kind.
//
// The three kinds land in three different places, because they are three
// different things: a skill is a directory copied into .claude/skills, a rule
// is prose merged into a file the project already owns, and a server is an
// entry in the project's .mcp.json.

// folderKind is a set of the kinds one run installs.
type folderKind int

const (
	kindSkills folderKind = 1 << iota
	kindRules
	kindServers
)

// kindAll is the bare `circe <folder>`: everything the folder holds.
const kindAll = kindSkills | kindRules | kindServers

func (k folderKind) has(other folderKind) bool { return k&other != 0 }

// count is how many kinds are in the set.
func (k folderKind) count() int {
	n := 0
	for _, one := range []folderKind{kindSkills, kindRules, kindServers} {
		if k.has(one) {
			n++
		}
	}
	return n
}

// noun names one kind in prose.
func (k folderKind) noun() string {
	switch k {
	case kindSkills:
		return "skills"
	case kindRules:
		return "rules"
	case kindServers:
		return "MCP servers"
	default:
		return "content"
	}
}

// folderManifest is one folder as the registry serves it.
type folderManifest struct {
	Name        string
	Title       string
	Description string
	Skills      []api.Skill
	Rules       []*model.Rule
	Servers     []*model.MCPServer
	// Context is the folder's own CLAUDE.md and AGENTS.md, keyed by filename.
	Context map[string]string
	// Registry is the base URL to use for follow-up downloads.
	Registry string
}

// UnmarshalJSON reads the folder whether it is served flat or wrapped in a
// "bundle" key. The three lists always arrive expanded, because installing a
// name the CLI cannot see the body of is not possible.
func (m *folderManifest) UnmarshalJSON(raw []byte) error {
	type named struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	var wire struct {
		named
		Bundle   *named             `json:"bundle"`
		Skills   []api.Skill        `json:"skills"`
		Rules    []*model.Rule      `json:"rules"`
		Servers  []*model.MCPServer `json:"servers"`
		Context  map[string]string  `json:"context"`
		Registry string             `json:"registry"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	if wire.Bundle != nil {
		wire.named = *wire.Bundle
	}
	m.Name, m.Title, m.Description = wire.Name, wire.Title, wire.Description
	m.Skills, m.Rules, m.Servers = wire.Skills, wire.Rules, wire.Servers
	m.Context, m.Registry = wire.Context, wire.Registry
	return nil
}

func (m *folderManifest) label() string {
	if strings.TrimSpace(m.Title) != "" {
		return m.Title
	}
	return m.Name
}

// holds is what the folder can install. A folder with no rules of its own can
// still carry a CLAUDE.md, which installs the same way rules do.
func (m *folderManifest) holds() folderKind {
	k := m.counted()
	if len(m.Context) > 0 {
		k |= kindRules
	}
	return k
}

// counted is what the folder holds enough of to be worth naming in a summary.
func (m *folderManifest) counted() folderKind {
	var k folderKind
	if len(m.Skills) > 0 {
		k |= kindSkills
	}
	if len(m.Rules) > 0 {
		k |= kindRules
	}
	if len(m.Servers) > 0 {
		k |= kindServers
	}
	return k
}

// counts renders "3 skills, 2 rules and 1 server" for the kinds in the set.
func (m *folderManifest) counts(kinds folderKind) string {
	var parts []string
	if kinds.has(kindSkills) && len(m.Skills) > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", len(m.Skills), pluralWord(len(m.Skills), "skill", "skills")))
	}
	if kinds.has(kindRules) && len(m.Rules) > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", len(m.Rules), pluralWord(len(m.Rules), "rule", "rules")))
	}
	if kinds.has(kindServers) && len(m.Servers) > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", len(m.Servers), pluralWord(len(m.Servers), "server", "servers")))
	}
	return joinList(parts)
}

// joinList renders a list the way a sentence does.
func joinList(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

// ---------- fetching ----------

// fetchFolder reads one folder from the registry.
func fetchFolder(ctx context.Context, c *installer.Client, name string) (*folderManifest, error) {
	body, err := getRaw(ctx, c, "/api/v1/bundles/"+url.PathEscape(name))
	if err != nil {
		if installer.IsNotFound(err) {
			return nil, fmt.Errorf("no folder named %q at %s. Run `circe skills`, `circe rules` or `circe mcp` with no argument to list folders: %w",
				name, c.BaseURL, err)
		}
		return nil, err
	}
	var m folderManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("decode folder %q: %w", name, err)
	}
	if m.Name == "" {
		m.Name = name
	}
	return &m, nil
}

// ---------- flags ----------

// folderOptions are the flags one install works from, whatever mix of kinds it
// covers. Each flag means the same thing wherever it appears:
//
//	-d  where skills are written
//	-o  which config the MCP servers merge into
//	--context  how CLAUDE.md and AGENTS.md are applied
//
// so a mixed install needs no flag the single-kind commands do not have.
type folderOptions struct {
	installFlags        // dir, force, dryRun
	out          string // config file the servers merge into
	context      string // merge, skip or overwrite
	takeAll      bool
	all          bool
}

func (o *folderOptions) register(cmd *cobra.Command, kinds folderKind) {
	o.cmd = cmd
	fl := cmd.Flags()

	if kinds.has(kindSkills) {
		fl.StringVarP(&o.dir, "dir", "d", installer.DefaultSkillsDir, "directory to install skills into")
	}
	if kinds.has(kindServers) {
		fl.StringVarP(&o.out, "out", "o", mcpConfigName, "config file to merge MCP servers into")
	}
	// -y is accepted for every kind, even where there is nothing to prompt for.
	// Rules have no picker, so it is a no-op there, but rejecting it would mean
	// `circe rules <folder> -y` errors while the same flag works on the bare
	// command and on mcp, which is a trap for anyone with the habit or a script.
	fl.BoolVarP(&o.takeAll, "yes", "y", false, "take everything in the folder without prompting")
	fl.BoolVar(&o.all, "all", false, "alias for --yes")
	if kinds.has(kindSkills) || kinds.has(kindServers) {
		fl.BoolVar(&o.force, "force", false, o.forceHelp(kinds))
	}
	if kinds.has(kindSkills) || kinds.has(kindRules) {
		fl.StringVar(&o.context, "context", "merge",
			"how to apply the folder's CLAUDE.md and AGENTS.md: merge, skip or overwrite")
	}
	fl.BoolVar(&o.dryRun, "dry-run", false, "report what would change without writing anything")
}

func (o *folderOptions) forceHelp(kinds folderKind) string {
	switch {
	case kinds.has(kindSkills) && kinds.has(kindServers):
		return "overwrite skills this project already has, and replace its MCP servers"
	case kinds.has(kindServers):
		return "take the whole folder and replace servers this project already has"
	default:
		return "overwrite skills that already exist"
	}
}

// everything reports whether this run should skip the pickers.
func (o *folderOptions) everything() bool { return o.takeAll || o.all }

// ---------- installing ----------

// runFolder installs the kinds asked for from one folder.
func runFolder(cmd *cobra.Command, name string, kinds folderKind, opts *folderOptions) error {
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
	m, err := fetchFolder(ctx, client, name)
	if err != nil {
		return err
	}

	doing := kinds & m.holds()
	if doing == 0 {
		return folderMissing(m, kinds)
	}
	// The manifest may point downloads at a different host than the one that
	// served it, so honour it for everything that follows.
	if m.Registry != "" {
		token := client.Token
		client = installer.NewClient(m.Registry)
		client.Token = token
	}

	// A run over one kind reads exactly as it did when that kind had a folder of
	// its own: installing skills prints no header, and no run prints section
	// labels or a combined summary until there is more than one kind to tell
	// apart.
	shown := kinds & m.counted()
	if shown != 0 && shown != kindSkills {
		fmt.Fprintln(out, dimStyle.Render(m.label()+" · "+m.counts(shown)))
	}
	section := sectionLabeller(out, shown.count() > 1)

	var done []string
	var written []*model.MCPServer

	if doing.has(kindSkills) {
		section("skills")
		part, err := installFolderSkills(ctx, out, client, root, m, opts)
		if err != nil {
			return err
		}
		done = appendPart(done, part)
	}

	// CLAUDE.md and AGENTS.md take one marked block per folder, so the folder's
	// own context files and its rules go in together or not at all: writing half
	// of it would delete the other half on the next run.
	switch {
	case doing.has(kindRules):
		section("rules")
		part, err := installFolderRules(out, root, m, opts, true)
		if err != nil {
			return err
		}
		done = appendPart(done, part)
	case doing.has(kindSkills) && len(m.Rules) == 0:
		if _, err := installFolderRules(out, root, m, opts, false); err != nil {
			return err
		}
	case doing.has(kindSkills):
		fmt.Fprintln(out, dimStyle.Render(fmt.Sprintf("%d %s left alone; `circe rules %s` merges them",
			len(m.Rules), pluralWord(len(m.Rules), "rule", "rules"), m.Name)))
	}

	if doing.has(kindServers) {
		section("mcp servers")
		part, w, err := installFolderServers(out, root, m, opts)
		if err != nil {
			return err
		}
		done, written = appendPart(done, part), w
	}

	if shown.count() > 1 {
		summary := joinList(done)
		if summary == "" {
			summary = "nothing"
		}
		if opts.dryRun {
			fmt.Fprintln(out, dimStyle.Render("dry run: "+summary+"; nothing was written"))
		} else {
			fmt.Fprintln(out, dimStyle.Render("installed "+summary))
		}
	}
	// Last, because it is the only thing here the developer still has to act on.
	printMCPEnv(out, written, os.Getenv)
	return nil
}

func appendPart(parts []string, part string) []string {
	if part == "" {
		return parts
	}
	return append(parts, part)
}

// sectionLabeller returns a function that names each section, or one that says
// nothing when there is only one section to name.
func sectionLabeller(out io.Writer, wanted bool) func(string) {
	if !wanted {
		return func(string) {}
	}
	first := true
	return func(label string) {
		if !first {
			fmt.Fprintln(out)
		}
		first = false
		fmt.Fprintln(out, dimStyle.Render(label))
	}
}

// folderMissing explains a folder that holds nothing of the kind asked for.
func folderMissing(m *folderManifest, want folderKind) error {
	have := m.counted()
	if have == 0 {
		return fmt.Errorf("folder %q is empty. Add skills, rules or servers to it, then run this again", m.Name)
	}
	return fmt.Errorf("folder %q has no %s. It has %s: run `circe %s` to install those",
		m.Name, want.noun(), m.counts(have), m.Name)
}

// ---------- listing ----------

// folderSummary is one folder in the registry listing.
type folderSummary struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Skills      []string `json:"skills"`
	Rules       []string `json:"rules"`
	Servers     []string `json:"servers"`
}

func (f folderSummary) count(kind folderKind) int {
	switch kind {
	case kindSkills:
		return len(f.Skills)
	case kindRules:
		return len(f.Rules)
	default:
		return len(f.Servers)
	}
}

// mix renders what one folder holds, across all three kinds.
func (f folderSummary) mix() string {
	var parts []string
	for _, k := range []struct {
		n         int
		one, many string
	}{
		{len(f.Skills), "skill", "skills"},
		{len(f.Rules), "rule", "rules"},
		{len(f.Servers), "server", "servers"},
	} {
		if k.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", k.n, pluralWord(k.n, k.one, k.many)))
		}
	}
	if len(parts) == 0 {
		return "empty"
	}
	return strings.Join(parts, ", ")
}

// listFolders shows the folders holding something of one kind, which is what
// each subcommand does when given no folder to install.
func listFolders(cmd *cobra.Command, kind folderKind) error {
	out := cmd.OutOrStdout()
	client, err := newClient()
	if err != nil {
		return err
	}
	body, err := getRaw(cmd.Context(), client, "/api/v1/bundles")
	if err != nil {
		return err
	}
	var resp struct {
		Bundles []folderSummary `json:"bundles"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return err
	}

	var folders []folderSummary
	for _, f := range resp.Bundles {
		if f.count(kind) > 0 {
			folders = append(folders, f)
		}
	}
	if len(folders) == 0 {
		fmt.Fprintln(out, dimStyle.Render("no folders with "+kind.noun()+" in the registry yet"))
		return nil
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].Name < folders[j].Name })

	names := make([]string, len(folders))
	mixes := make([]string, len(folders))
	mixCol := 0
	for i, f := range folders {
		names[i], mixes[i] = f.Name, f.mix()
		if n := len(mixes[i]); n > mixCol {
			mixCol = n
		}
	}
	col := nameColumn(names)
	width := termWidth()

	for i, f := range folders {
		note := f.Description
		if note == "" {
			note = f.Title
		}
		note = truncate(note, width-col-mixCol-8)
		mix := mixes[i]
		if note != "" {
			mix = pad(mix, mixCol) + "  " + note
		}
		fmt.Fprintln(out, "  "+pad(f.Name, col)+"  "+dimStyle.Render(mix))
	}
	return nil
}
