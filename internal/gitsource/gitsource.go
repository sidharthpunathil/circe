// Package gitsource discovers Claude skills inside a GitHub repository and
// loads their content into memory, so that an import survives the upstream
// repository being renamed, made private or deleted.
//
// The whole repository is fetched as a single tarball rather than walked
// through the contents API: an unauthenticated caller gets 60 API requests an
// hour, which a per-file walk of any real skill library exhausts before it
// finishes discovering anything.
//
// Nothing here touches the store. Fetch returns plain data and the caller
// decides what, if anything, to persist — that keeps this package free of an
// import cycle and testable without a filesystem.
package gitsource

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sidharthp/skillbuilder/internal/skillfile"
)

// skillFileName marks a directory as a skill.
const skillFileName = "SKILL.md"

// Repo identifies a GitHub repository, optionally pinned to a ref and narrowed
// to a subdirectory.
type Repo struct {
	Owner string
	Name  string
	Ref   string // branch, tag or SHA; empty means the default branch

	// Subpath restricts discovery to one directory of the repository, which is
	// what a pasted /tree/<ref>/<dir> URL means. Empty means the whole repo.
	Subpath string
}

// String renders the repository as owner/name.
func (r Repo) String() string { return r.Owner + "/" + r.Name }

// HTMLURL is the browsable page for the repository.
func (r Repo) HTMLURL() string { return "https://github.com/" + r.Owner + "/" + r.Name }

func (r Repo) validate() error {
	if !nameRe.MatchString(r.Owner) || !nameRe.MatchString(r.Name) {
		return fmt.Errorf("invalid repository %q", r.String())
	}
	return nil
}

// Metadata is repository-level information shown alongside the candidates.
type Metadata struct {
	FullName    string    `json:"fullName"`
	Description string    `json:"description"`
	Ref         string    `json:"ref"` // the branch/tag actually fetched
	CommitSHA   string    `json:"commitSha"`
	UpdatedAt   time.Time `json:"updatedAt"` // repository pushed_at
	Stars       int       `json:"stars"`
	HTMLURL     string    `json:"htmlUrl"`
}

// Candidate is one discovered skill with its files already in memory.
type Candidate struct {
	Name        string            `json:"name"`        // frontmatter name, else the directory name
	Description string            `json:"description"` // frontmatter description, else the first body line
	Path        string            `json:"path"`        // path within the repository, e.g. "skills/foo"
	Files       map[string][]byte `json:"-"`           // keyed relative to the skill dir; includes SKILL.md
	Size        int64             `json:"size"`
	UpdatedAt   time.Time         `json:"updatedAt"` // best-effort last commit touching Path
}

// Result is everything one Fetch produced.
type Result struct {
	Meta       Metadata    `json:"meta"`
	Candidates []Candidate `json:"candidates"`
}

// Fetch downloads a repository and discovers every skill in it, using the
// default client.
func Fetch(ctx context.Context, repo Repo) (*Result, error) {
	return NewClient().Fetch(ctx, repo)
}

// Fetch downloads a repository and discovers every skill in it.
//
// Repository metadata is a nice-to-have, not a precondition: if that call fails
// (rate limit, private repo, GitHub having a bad day) the candidates are still
// returned with whatever could be determined from the archive itself.
func (c *Client) Fetch(ctx context.Context, repo Repo) (*Result, error) {
	if err := repo.validate(); err != nil {
		return nil, err
	}

	meta, metaErr := c.repoMetadata(ctx, repo)
	meta.HTMLURL = repo.HTMLURL()
	if meta.FullName == "" {
		meta.FullName = repo.String()
	}

	// An explicit ref wins; otherwise use the default branch we just learned,
	// and if metadata failed, let GitHub pick by omitting the ref entirely.
	ref := repo.Ref
	if ref == "" {
		ref = meta.Ref
	}

	arc, err := c.downloadTarball(ctx, repo, ref)
	if err != nil {
		// A failed metadata call usually explains a failed download (private
		// repo, rate limit) but the download error is the more specific of the
		// two, so it is the one that surfaces.
		return nil, err
	}
	meta.Ref = ref
	if sha := commitFromRoot(arc.Root, repo); sha != "" {
		meta.CommitSHA = sha
	}

	cands := discover(arc.Files, repo.Name)
	for i := range cands {
		cands[i].UpdatedAt = meta.UpdatedAt
	}

	// Skip the per-skill lookups entirely when we already know the token bucket
	// is empty — N more doomed requests only delay the import.
	if !IsRateLimited(metaErr) {
		c.annotateUpdatedAt(ctx, repo, ref, cands)
	}

	return &Result{Meta: meta, Candidates: cands}, nil
}

// annotateUpdatedAt fills in each candidate's UpdatedAt from the last commit
// that touched its path.
//
// This is one request per skill, so it is bounded, best-effort and never fatal:
// the repository's pushed_at is already a defensible answer, and a rate-limited
// lookup must not cost the user the whole import.
func (c *Client) annotateUpdatedAt(ctx context.Context, repo Repo, ref string, cands []Candidate) {
	if len(cands) == 0 {
		return
	}
	sem := make(chan struct{}, maxCommitLookups)
	var stopped atomic.Bool
	var wg sync.WaitGroup

	for i := range cands {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if stopped.Load() || ctx.Err() != nil {
				return
			}
			at, err := c.lastCommitTime(ctx, repo, ref, cands[i].Path)
			if err != nil {
				// One rate-limited lookup means every sibling will be too.
				if IsRateLimited(err) {
					stopped.Store(true)
				}
				return
			}
			if !at.IsZero() {
				cands[i].UpdatedAt = at
			}
		}(i)
	}
	wg.Wait()
}

// discover groups the archive's files into skills. Any directory holding a
// SKILL.md is one, at any depth.
//
// Every file is assigned to its *nearest* enclosing skill directory, so a
// repository that is itself a skill (SKILL.md at the root) alongside a
// skills/ directory does not swallow its own children.
func discover(files map[string][]byte, rootName string) []Candidate {
	skillDirs := map[string]bool{}
	for p := range files {
		if path.Base(p) == skillFileName {
			skillDirs[dirOf(p)] = true
		}
	}
	if len(skillDirs) == 0 {
		return nil
	}

	grouped := make(map[string]map[string][]byte, len(skillDirs))
	for d := range skillDirs {
		grouped[d] = map[string][]byte{}
	}
	for p, b := range files {
		dir, ok := nearestSkillDir(p, skillDirs)
		if !ok {
			continue
		}
		rel := p
		if dir != "" {
			rel = p[len(dir)+1:]
		}
		if excludedInSkill(rel) {
			continue
		}
		grouped[dir][rel] = b
	}

	out := make([]Candidate, 0, len(grouped))
	for dir, fs := range grouped {
		raw, ok := fs[skillFileName]
		if !ok {
			continue // its SKILL.md was excluded; not a skill after all
		}
		c := Candidate{Path: dir, Files: fs}
		for _, b := range fs {
			c.Size += int64(len(b))
		}
		c.Name, c.Description = describe(raw)
		if c.Name == "" {
			if dir == "" {
				c.Name = rootName
			} else {
				c.Name = path.Base(dir)
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// describe pulls the display name and description out of a SKILL.md. A file
// with broken frontmatter is not dropped — the caller can see and fix it — it
// simply falls back to the directory name.
func describe(raw []byte) (name, desc string) {
	f, err := skillfile.Parse(raw)
	if err != nil {
		return "", ""
	}
	desc = strings.TrimSpace(f.Description)
	if desc == "" {
		desc = f.Summary(160)
	}
	return strings.TrimSpace(f.Name), desc
}

// nearestSkillDir walks up from a file to the closest directory containing a
// SKILL.md, reporting false when the file belongs to no skill at all.
func nearestSkillDir(p string, skillDirs map[string]bool) (string, bool) {
	d := dirOf(p)
	for {
		if skillDirs[d] {
			return d, true
		}
		if d == "" {
			return "", false
		}
		if i := strings.LastIndex(d, "/"); i >= 0 {
			d = d[:i]
		} else {
			d = ""
		}
	}
}

// dirOf is path.Dir with the repository root spelled "" rather than ".".
func dirOf(p string) string {
	d := path.Dir(p)
	if d == "." || d == "/" {
		return ""
	}
	return d
}

// URL parsing.

var (
	// GitHub owners and repository names are alphanumerics plus - _ . and a
	// name must not be a dot segment.
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

	// scpSyntax matches git's scp-like remote form, git@github.com:owner/repo.
	scpSyntax = regexp.MustCompile(`^(?:[\w.-]+@)?([\w.-]+):(.+)$`)
)

// ParseRepoURL accepts the many forms a repository gets pasted in:
//
//	https://github.com/owner/repo            (with or without .git, trailing /)
//	https://github.com/owner/repo/tree/main
//	https://github.com/owner/repo/tree/main/skills/foo
//	git@github.com:owner/repo.git
//	github.com/owner/repo
//	owner/repo
//
// A /tree/ or /blob/ URL contributes both the ref and, when it points deeper, a
// Subpath that limits discovery to that directory.
func ParseRepoURL(s string) (Repo, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Repo{}, errors.New("repository URL is required")
	}

	var host, p string
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil {
			return Repo{}, fmt.Errorf("parse %q: %w", s, err)
		}
		// EscapedPath, not Path: a ref containing an encoded slash
		// (release%2F1.0) must stay one segment until splitPath decodes it.
		host, p = u.Hostname(), u.EscapedPath()
	case scpSyntax.MatchString(s):
		m := scpSyntax.FindStringSubmatch(s)
		host, p = m[1], m[2]
	default:
		// A bare string is either github.com/owner/repo or just owner/repo.
		host, p = "github.com", s
		for _, prefix := range []string{"github.com/", "www.github.com/"} {
			if rest, ok := strings.CutPrefix(s, prefix); ok {
				p = rest
				break
			}
		}
	}

	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	if host != "github.com" {
		return Repo{}, fmt.Errorf("unsupported host %q — only github.com repositories are supported", host)
	}

	parts := splitPath(p)
	if len(parts) < 2 {
		return Repo{}, fmt.Errorf("cannot find owner/repo in %q — expected something like https://github.com/owner/repo", s)
	}

	r := Repo{Owner: parts[0], Name: strings.TrimSuffix(parts[1], ".git")}
	if err := r.validate(); err != nil {
		return Repo{}, fmt.Errorf("cannot parse %q: %w", s, err)
	}

	// github.com/owner/repo/<kind>/<ref>/<path...>
	if len(parts) >= 4 {
		rest := parts[3:]
		switch parts[2] {
		case "tree", "blob":
			// A ref containing a slash (feature/x) is indistinguishable from a
			// ref plus a subdirectory without asking GitHub, so the first
			// segment wins — that is what the overwhelming majority of pasted
			// links mean.
			r.Ref = rest[0]
			sub := rest[1:]
			// A blob link names a file; the skill is the directory holding it.
			if parts[2] == "blob" && len(sub) > 0 && strings.Contains(sub[len(sub)-1], ".") {
				sub = sub[:len(sub)-1]
			}
			r.Subpath = path.Join(sub...)
		case "commit", "commits":
			r.Ref = rest[0]
		}
	}
	return r, nil
}

func splitPath(p string) []string {
	var out []string
	for _, seg := range strings.Split(strings.Trim(p, "/"), "/") {
		if seg == "" {
			continue
		}
		// url.Parse leaves %2F-style escapes decoded already, but the scp and
		// bare forms have not been through it.
		if un, err := url.PathUnescape(seg); err == nil {
			seg = un
		}
		out = append(out, seg)
	}
	return out
}

// commitFromRoot recovers the commit SHA from the tarball's generated root
// directory, which GitHub names owner-repo-<sha>. Reading it here saves a
// request compared with resolving the ref through the API.
func commitFromRoot(root string, repo Repo) string {
	prefix := repo.Owner + "-" + repo.Name + "-"
	sha := strings.TrimPrefix(root, prefix)
	if sha == root {
		// Not the expected shape (a hand-rolled archive, or a name GitHub
		// normalized); fall back to the last hyphen-separated token.
		if i := strings.LastIndex(root, "-"); i >= 0 {
			sha = root[i+1:]
		} else {
			return ""
		}
	}
	if len(sha) < 7 || !isHex(sha) {
		return ""
	}
	return sha
}

func isHex(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return s != ""
}
