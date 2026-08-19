// Package gitsync keeps a Circe registry and a GitHub repository in step.
//
// Pushing writes the registry out as the plain directory tree it has always
// used (skills/<name>/SKILL.md, rules/<name>.md, mcps/<name>.json,
// bundles/<name>.json) and commits it. Pulling reads that same tree back. So
// the repository is not an export format or a backup blob: it is the registry,
// in a form you can read in a browser, diff in a pull request, and edit by hand.
//
// It talks to GitHub's Git Data API rather than shelling out to git, for the
// same reason the importer downloads tarballs: the deployed image is `scratch`
// and has no git binary, no shell and nowhere to put a working copy.
package gitsync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sidharthpunathil/circe/internal/gitsource"
	"github.com/sidharthpunathil/circe/internal/store"
)

// stateKey is where the connection and its bookkeeping live.
const stateKey = "gitsync"

// managedDirs are the top-level directories the registry owns in the
// repository. Anything else there (a README, a LICENSE, a workflow) is carried
// through untouched, so a sync target can be a repository you also use for
// other things.
var managedDirs = []string{"skills/", "rules/", "mcps/", "bundles/"}

// managedFiles are the individual files the registry owns.
var managedFiles = []string{"sources.json"}

// Config is the connection to a repository.
type Config struct {
	// Repo is "owner/name".
	Repo string `json:"repo"`
	// Branch defaults to the repository's default branch, or main when the
	// repository is empty.
	Branch string `json:"branch,omitempty"`
	// Path optionally nests the registry inside a subdirectory, so one
	// repository can hold more than one of them.
	Path string `json:"path,omitempty"`

	// Bookkeeping, written by a successful sync.
	LastCommit    string    `json:"lastCommit,omitempty"`
	LastSyncedAt  time.Time `json:"lastSyncedAt,omitempty"`
	LastDirection string    `json:"lastDirection,omitempty"`
	LastSummary   string    `json:"lastSummary,omitempty"`
}

// Connected reports whether a repository has been set.
func (c Config) Connected() bool { return strings.TrimSpace(c.Repo) != "" }

// BranchOrDefault is the branch to write to.
func (c Config) BranchOrDefault() string {
	if b := strings.TrimSpace(c.Branch); b != "" {
		return b
	}
	return "main"
}

// HTMLURL links to the repository.
func (c Config) HTMLURL() string {
	if !c.Connected() {
		return ""
	}
	return "https://github.com/" + c.Repo
}

// CommitURL links to the last commit this registry made.
func (c Config) CommitURL() string {
	if c.LastCommit == "" || !c.Connected() {
		return ""
	}
	return c.HTMLURL() + "/commit/" + c.LastCommit
}

// ShortCommit is the commit id as a person would quote it.
func (c Config) ShortCommit() string {
	if len(c.LastCommit) > 7 {
		return c.LastCommit[:7]
	}
	return c.LastCommit
}

// Load reads the stored connection. An unconfigured registry returns the zero
// value rather than an error.
func Load(st *store.Store) Config {
	var c Config
	if _, err := st.GetState(stateKey, &c); err != nil {
		return Config{}
	}
	return c
}

// Save writes the connection.
func Save(st *store.Store, c Config) error {
	c.Repo = strings.Trim(strings.TrimSpace(c.Repo), "/")
	c.Path = strings.Trim(strings.TrimSpace(c.Path), "/")
	c.Branch = strings.TrimSpace(c.Branch)
	return st.SetState(stateKey, c)
}

// ParseRepo accepts anything a person might paste: a full URL, an SSH remote,
// or just owner/name.
func ParseRepo(input string) (string, error) {
	in := strings.TrimSpace(input)
	if in == "" {
		return "", fmt.Errorf("give it a repository, like owner/name")
	}
	// owner/name, with nothing that looks like a host or a scheme.
	if !strings.Contains(in, "://") && !strings.Contains(in, "@") && strings.Count(in, "/") == 1 {
		owner, name, _ := strings.Cut(in, "/")
		if owner == "" || name == "" {
			return "", fmt.Errorf("that does not look like owner/name")
		}
		return owner + "/" + strings.TrimSuffix(name, ".git"), nil
	}
	repo, err := gitsource.ParseRepoURL(in)
	if err != nil {
		return "", err
	}
	if repo.Provider() != gitsource.ProviderGitHub {
		return "", fmt.Errorf("sync writes commits through GitHub's API, so the repository has to be on github.com")
	}
	return repo.Owner + "/" + repo.Name, nil
}

// Result describes what one sync did, in the words the UI shows.
type Result struct {
	Commit  string
	Summary string
	// Changed is false when the repository already matched, which is the
	// common case for a second push in a row.
	Changed bool
	// Imported, Skipped and Failed are filled in by a pull.
	Imported []string
	Skipped  []string
	Failed   []string
}

// Push writes the whole registry to the repository as one commit.
//
// Files outside the directories the registry owns are carried through
// unchanged, and a skill deleted here is deleted there: the commit contains
// exactly what the registry holds, not a merge of it with whatever was there
// before.
func Push(ctx context.Context, st *store.Store, cfg Config, message string) (Result, error) {
	var res Result
	if !cfg.Connected() {
		return res, fmt.Errorf("connect a repository first")
	}
	client, err := newClient(cfg.Repo)
	if err != nil {
		return res, err
	}

	snapshot, err := st.Snapshot()
	if err != nil {
		return res, err
	}
	if message == "" {
		message = summarize(st)
	}

	branch := cfg.BranchOrDefault()
	head, err := client.headCommit(ctx, branch)
	if err != nil {
		return res, err
	}

	entries := make([]treeEntry, 0, len(snapshot)+16)

	// What the repository already has, so unchanged files can be referenced
	// rather than uploaded, and files the registry does not own carried through.
	known := map[string]string{}
	if head != nil {
		existing, err := client.tree(ctx, head.Tree.SHA)
		if err != nil {
			return res, err
		}
		for _, e := range existing {
			if e.Type != "blob" {
				continue
			}
			if isManaged(cfg, e.Path) {
				known[e.Path] = e.SHA
				continue
			}
			entries = append(entries, treeEntry{Path: e.Path, Mode: e.Mode, Type: "blob", SHA: e.SHA})
		}
	}

	paths := make([]string, 0, len(snapshot))
	for rel := range snapshot {
		paths = append(paths, rel)
	}
	sort.Strings(paths)

	for _, rel := range paths {
		body := snapshot[rel]
		path := prefixed(cfg, rel)

		sha := blobID(body)
		if known[path] != sha {
			// Only what actually changed is sent.
			uploaded, err := client.createBlob(ctx, body)
			if err != nil {
				return res, fmt.Errorf("upload %s: %w", rel, err)
			}
			sha = uploaded
		}
		entries = append(entries, treeEntry{Path: path, Mode: "100644", Type: "blob", SHA: sha})
	}

	treeSHA, err := client.createTree(ctx, entries)
	if err != nil {
		return res, err
	}

	// An identical tree means there is nothing to say. Committing anyway would
	// fill the history with empty commits every time someone pressed the button.
	if head != nil && head.Tree.SHA == treeSHA {
		res.Commit = head.SHA
		res.Summary = "already up to date"
		return res, nil
	}

	var parents []string
	if head != nil {
		parents = []string{head.SHA}
	}
	commitSHA, err := client.createCommit(ctx, message, treeSHA, parents)
	if err != nil {
		return res, err
	}
	if err := client.setRef(ctx, branch, commitSHA, head == nil); err != nil {
		return res, err
	}

	res.Commit = commitSHA
	res.Summary = message
	res.Changed = true
	return res, nil
}

// Pull reads the repository back into the registry, replacing what is here.
//
// This is the direction that loses work, so it is never automatic: something
// edited in the browser and not yet pushed is overwritten by whatever the
// repository says.
func Pull(ctx context.Context, st *store.Store, cfg Config) (Result, error) {
	var res Result
	if !cfg.Connected() {
		return res, fmt.Errorf("connect a repository first")
	}
	owner, name, _ := strings.Cut(cfg.Repo, "/")
	repo := gitsource.Repo{Owner: owner, Name: name, Ref: cfg.Branch}

	// The importer already knows how to fetch a repository as a tarball from
	// any host without a git binary, which is exactly what is needed here.
	fetched, err := gitsource.Fetch(ctx, repo)
	if err != nil {
		return res, err
	}

	dir, err := os.MkdirTemp("", "circe-pull-*")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(dir)

	root := dir
	if cfg.Path != "" {
		root = filepath.Join(dir, filepath.FromSlash(cfg.Path))
	}
	for rel, body := range fetched.Files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		// Never let an archive path escape the temp directory.
		if !strings.HasPrefix(full, dir+string(os.PathSeparator)) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(full, body, 0o644); err != nil {
			return res, err
		}
	}

	imported, err := st.ImportTree(root, true)
	if err != nil {
		return res, err
	}
	res.Imported, res.Skipped, res.Failed = imported.Imported, imported.Skipped, imported.Failed
	res.Commit = fetched.Meta.CommitSHA
	res.Changed = len(imported.Imported) > 0
	res.Summary = fmt.Sprintf("pulled %d skills", len(imported.Imported))
	return res, nil
}

// Record stamps the outcome of a sync onto the stored connection.
func Record(st *store.Store, cfg Config, direction string, res Result) error {
	cfg.LastDirection = direction
	cfg.LastSyncedAt = time.Now()
	cfg.LastSummary = res.Summary
	if res.Commit != "" {
		cfg.LastCommit = res.Commit
	}
	return Save(st, cfg)
}

// prefixed places a snapshot path inside the configured subdirectory.
func prefixed(cfg Config, rel string) string {
	if cfg.Path == "" {
		return rel
	}
	return cfg.Path + "/" + rel
}

// isManaged reports whether a repository path is one the registry writes, and
// so should be replaced rather than carried through.
func isManaged(cfg Config, p string) bool {
	if cfg.Path != "" {
		if !strings.HasPrefix(p, cfg.Path+"/") {
			return false
		}
		p = strings.TrimPrefix(p, cfg.Path+"/")
	}
	for _, dir := range managedDirs {
		if strings.HasPrefix(p, dir) {
			return true
		}
	}
	for _, f := range managedFiles {
		if p == f {
			return true
		}
	}
	return false
}

// summarize writes the default commit message: what the registry holds, so the
// history reads as a series of states rather than a wall of "update".
func summarize(st *store.Store) string {
	parts := []string{}
	add := func(n int, one, many string) {
		if n == 0 {
			return
		}
		if n == 1 {
			parts = append(parts, "1 "+one)
			return
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, many))
	}
	add(st.SkillCount(), "skill", "skills")
	add(st.RuleCount(), "rule", "rules")
	add(st.MCPCount(), "server", "servers")
	add(st.BundleCount(), "folder", "folders")
	if len(parts) == 0 {
		return "circe: empty registry"
	}
	return "circe: " + strings.Join(parts, ", ")
}

// jsonBody marshals a request body, since every call here sends one.
func jsonBody(v any) ([]byte, error) { return json.Marshal(v) }
