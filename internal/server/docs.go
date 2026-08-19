package server

import (
	"net/http"

	"github.com/sidharthpunathil/circe/internal/gitsync"
)

// The Docs tab is the manual for the registry you are actually looking at,
// which is the part a README on GitHub cannot be. Every command on it is
// written with this deployment's own URL and, where it helps, the name of a
// folder that really exists here, so it can be copied and run rather than
// adapted first.
//
// It deliberately does not try to be the whole documentation. The deep
// reference lives in docs/ in the repository, and this page links to it.

// docsCommand is one row of the command reference.
type docsCommand struct {
	Command string
	What    string
}

type docsView struct {
	base
	// BaseURL is this registry's own origin, so `circe login` is copyable.
	BaseURL string
	// Folder is a real folder to use in examples, or "my-folder" when the
	// registry is still empty.
	Folder string
	// Sample names make the rest of the examples concrete too.
	Skill string
	// HasPassword changes the sign-in advice: without one there is nothing to
	// type when the CLI asks.
	HasPassword bool
	// Synced reports whether a repository is connected, so the sync section can
	// tell you where it points instead of how to set it up.
	Synced   bool
	SyncRepo string

	Commands []docsCommand
	Flags    []docsCommand
}

func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	view := docsView{
		base:        s.base(w, r, "docs"),
		BaseURL:     s.baseURL(r),
		Folder:      "my-folder",
		Skill:       "my-skill",
		HasPassword: s.opts.Auth.Enabled(),
		Commands:    docsCommands(),
		Flags:       docsFlags(),
	}

	// Prefer a folder that is actually here, so the examples are runnable.
	if bundles := s.store.Bundles(); len(bundles) > 0 {
		view.Folder = bundles[0].Name
		for _, b := range bundles {
			if !b.IsEmpty() {
				view.Folder = b.Name
				break
			}
		}
	}
	if skills := s.store.Skills(); len(skills) > 0 {
		view.Skill = skills[0].Name
	}

	if cfg := gitsync.Load(s.store); cfg.Connected() {
		view.Synced, view.SyncRepo = true, cfg.Repo
	}

	s.tmpl.render(w, r, http.StatusOK, "docs", view)
}

// docsCommands is every CLI command, in the order someone meets them.
func docsCommands() []docsCommand {
	return []docsCommand{
		{"circe login <url>", "Point the CLI at a registry. Asks for the passcode and checks it before saving."},
		{"circe <folder>", "Install everything in a folder: its skills, its rules, its MCP servers. Asks as it goes."},
		{"circe <folder> -y", "The same, taking all of it without prompting."},
		{"circe skills <folder>", "Only the skills. With no folder, lists the folders that have some."},
		{"circe rules <folder>", "Only the rules, merged into this project's CLAUDE.md and AGENTS.md."},
		{"circe mcp <folder>", "Only the MCP servers, merged into this project's .mcp.json."},
		{"circe add <skill>…", "Install specific skills. With no arguments, opens a picker."},
		{"circe list", "Show what is installed here and whether it has drifted. --remote lists the registry."},
		{"circe search <query>", "Search the registry from the terminal."},
		{"circe pull", "Re-download everything in the lockfile. Holds back anything you have edited."},
		{"circe remove <skill>…", "Delete skills and drop them from the lockfile."},
		{"circe push <dir>", "Publish from your machine: one skill, or every skill in a directory."},
		{"circe import <code>", "Install from someone else's share code. Needs no setup."},
		{"circe config", "Show the resolved registry, config path and install directory."},
		{"circe logout", "Forget the saved registry and passcode."},
	}
}

// docsFlags are the flags worth knowing before you need them.
func docsFlags() []docsCommand {
	return []docsCommand{
		{"-d, --dir", "Where skills go. Remembered after the first install."},
		{"-o, --out", "Which config the MCP servers merge into. Defaults to .mcp.json."},
		{"--context", "How the folder's CLAUDE.md is applied: merge, skip or overwrite."},
		{"--force", "Replace what is already there instead of holding it back."},
		{"--dry-run", "Say what would happen and change nothing."},
		{"--registry", "Use a different registry for this one command."},
	}
}

// storeKinds is what the three things are, for the page's opening section.
// Keeping it here rather than in the template means the wording is one string
// rather than three paragraphs of markup.
type docsKind struct {
	Name  string
	What  string
	Lands string
}

func docsKinds() []docsKind {
	return []docsKind{
		{"Skills", "A procedure the agent can follow.", ".claude/skills/<name>/"},
		{"Rules", "How you work, and what to avoid.", "CLAUDE.md and AGENTS.md"},
		{"MCP servers", "A live connection to something outside the repo.", ".mcp.json"},
	}
}

// Kinds is exposed to the template through the view.
func (v docsView) Kinds() []docsKind { return docsKinds() }

// InstallLine is the one-liner that installs the CLI.
func (v docsView) InstallLine() string {
	return "curl -fsSL https://raw.githubusercontent.com/sidharthpunathil/circe/main/install.sh | sh"
}

// LoginLine is `circe login` against this registry.
func (v docsView) LoginLine() string { return "circe login " + v.BaseURL }

// MarketplaceLine adds this registry to Claude Code without the CLI.
func (v docsView) MarketplaceLine() string {
	return "/plugin marketplace add " + v.BaseURL
}
