package store

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/sidharthpunathil/circe/internal/skillfile"
)

// Content detection, shared by every import path: a repository tarball, an
// uploaded file, a directory on disk. It answers one question, "what in this
// tree is worth importing", for all three things this registry stores.
//
// The rules are deliberately narrow, because the cost of a wrong guess is a
// registry full of junk somebody has to delete by hand:
//
//	SKILL.md                  a skill, rooted at the directory holding it
//	.mcp.json or mcp.json     one or more MCP servers
//	CLAUDE.md or AGENTS.md    a rule, targeting the file it is named after
//	*.md under a rules/ dir   a rule targeting CLAUDE.md
//
// Nothing else counts. Treating any stray markdown file as guidance would turn
// every README in a repository into a rule, which is worse than missing one.

// skillFileName marks a directory as a skill.
const skillFileName = "SKILL.md"

// mcpConfigNames are the filenames a project's MCP servers are configured in.
var mcpConfigNames = map[string]bool{".mcp.json": true, "mcp.json": true}

// FoundSkill is a directory holding a SKILL.md.
type FoundSkill struct {
	Name        string
	Description string
	Path        string // directory within the tree; "" is the tree root
}

// FoundRule is one markdown file that is project guidance rather than a skill.
type FoundRule struct {
	Name        string
	Description string
	Body        string
	Target      string // CLAUDE.md or AGENTS.md
	Path        string // the file it was read from
}

// FoundMCP is one server read out of an MCP config.
type FoundMCP struct {
	Server *MCPServer
	Path   string // the config file it was read from
}

// Found is everything importable in one tree.
type Found struct {
	Skills []FoundSkill
	Rules  []FoundRule
	MCPs   []FoundMCP
}

// Total counts every item across the three kinds.
func (f *Found) Total() int { return len(f.Skills) + len(f.Rules) + len(f.MCPs) }

// DetectContent classifies a tree of files, keyed by slash-separated path
// relative to the tree root, into skills, rules and MCP servers.
//
// fallback names anything the tree itself does not name, and is normally the
// repository or directory the files came from: a CLAUDE.md at the root says
// nothing about whose rules they are, but the repository it came from does.
//
// Names are unique within the returned batch. Collisions with names already in
// the registry are deliberately left alone, so that the name shown on a preview
// is the name that gets written and "overwrite" means what it says.
func DetectContent(files map[string][]byte, fallback string) *Found {
	paths := make([]string, 0, len(files))
	for p := range files {
		if ignoredInTree(p) {
			continue
		}
		paths = append(paths, p)
	}
	// A name depends on what earlier files already claimed, so a stable order
	// is what makes a preview and the import that follows it agree.
	sort.Strings(paths)

	found := &Found{}

	skillDirs := map[string]bool{}
	for _, p := range paths {
		if path.Base(p) == skillFileName {
			skillDirs[treeDir(p)] = true
		}
	}

	skillNames := newNamer()
	for _, p := range paths {
		if path.Base(p) != skillFileName {
			continue
		}
		dir := treeDir(p)
		name, desc, _ := readMarkdown(files[p])
		found.Skills = append(found.Skills, FoundSkill{
			Name:        skillNames.take(name, dirName(dir, fallback), fallback),
			Description: desc,
			Path:        dir,
		})
	}

	serverNames := newNamer()
	var rulePaths []string
	for _, p := range paths {
		// Files inside a skill are that skill's, and travel with it.
		if underSkill(p, skillDirs) {
			continue
		}
		if mcpConfigNames[strings.ToLower(path.Base(p))] {
			servers, err := ParseMCPConfig(files[p])
			if err != nil {
				continue
			}
			for _, m := range servers {
				m.Name = serverNames.take(m.Name, dirName(treeDir(p), fallback)+"-"+m.Name)
				found.MCPs = append(found.MCPs, FoundMCP{Server: m, Path: p})
			}
			continue
		}
		if _, ok := ruleTarget(p); ok {
			rulePaths = append(rulePaths, p)
		}
	}

	// CLAUDE.md is the one people mean, so it gets first claim on the plain
	// name and an AGENTS.md beside it takes the qualified one. Shallower files
	// win over deeper ones for the same reason: what sits at the top of a
	// repository is what the repository is about.
	sort.SliceStable(rulePaths, func(i, j int) bool {
		a, b := rulePriority(rulePaths[i]), rulePriority(rulePaths[j])
		if a != b {
			return a < b
		}
		if da, db := strings.Count(rulePaths[i], "/"), strings.Count(rulePaths[j], "/"); da != db {
			return da < db
		}
		return rulePaths[i] < rulePaths[j]
	})

	ruleNames := newNamer()
	for _, p := range rulePaths {
		target, _ := ruleTarget(p)
		_, desc, body := readMarkdown(files[p])
		if strings.TrimSpace(body) == "" {
			continue // a rule with nothing in it is not a rule
		}
		if desc == "" {
			desc = "Project guidance that merges into " + target + "."
		}
		found.Rules = append(found.Rules, FoundRule{
			Name:        ruleNames.take(ruleNameCandidates(p, fallback)...),
			Description: desc,
			Body:        body,
			Target:      target,
			Path:        p,
		})
	}
	return found
}

// RuleTargetForPath reports whether a markdown path is project guidance rather
// than a skill, and which file it merges into. It is the same test the tree
// scan uses, so an uploaded CLAUDE.md is treated exactly like one found in a
// repository.
func RuleTargetForPath(p string) (string, bool) { return ruleTarget(p) }

// ruleTarget reports which file a markdown path is guidance for, and whether it
// is guidance at all.
func ruleTarget(p string) (string, bool) {
	base := path.Base(p)
	switch {
	case strings.EqualFold(base, TargetClaude):
		return TargetClaude, true
	case strings.EqualFold(base, TargetAgents):
		return TargetAgents, true
	case strings.HasSuffix(strings.ToLower(base), ".md") && hasSegment(treeDir(p), "rules"):
		// A rules/ directory is an explicit statement of intent, which is why
		// its markdown counts and markdown anywhere else does not.
		return TargetClaude, true
	}
	return "", false
}

// rulePriority orders rule files so that the plain name goes to the file most
// likely to be the project's main guidance.
func rulePriority(p string) int {
	switch {
	case strings.EqualFold(path.Base(p), TargetClaude):
		return 0
	case strings.EqualFold(path.Base(p), TargetAgents):
		return 1
	}
	return 2
}

// ruleNameCandidates lists the names for a rule file, best first.
func ruleNameCandidates(p, fallback string) []string {
	base := path.Base(p)
	stem := strings.TrimSuffix(base, path.Ext(base))
	dir := treeDir(p)

	// A rules/ directory is a container, not a subject, so the name worth
	// having is the one above it.
	owner := dirName(dir, fallback)
	if genericRuleNames[strings.ToLower(owner)] {
		owner = dirName(treeDir(dir), fallback)
	}

	// CLAUDE.md says nothing about what is in it, so the directory holding it,
	// or the repository itself, is the more useful name.
	if genericRuleNames[strings.ToLower(stem)] {
		return []string{owner, owner + "-" + stem, fallback + "-" + stem, stem}
	}
	return []string{stem, owner + "-" + stem, fallback + "-" + stem}
}

// genericRuleNames are filenames that describe the destination rather than the
// content, so they never make a good name on their own.
var genericRuleNames = map[string]bool{
	"claude": true, "agents": true, "index": true, "readme": true, "rules": true,
}

// readMarkdown pulls the name, description and body out of a markdown file with
// optional frontmatter. A file that will not parse is treated as plain body.
func readMarkdown(raw []byte) (name, description, body string) {
	f, err := skillfile.Parse(raw)
	if err != nil {
		return "", "", string(raw)
	}
	description = strings.TrimSpace(f.Description)
	if description == "" {
		description = f.Summary(200)
	}
	return strings.TrimSpace(f.Name), description, f.Body
}

// namer hands out names that are unique within one import batch.
type namer struct{ taken map[string]bool }

func newNamer() *namer { return &namer{taken: map[string]bool{}} }

// take returns the first free candidate, or the best one with a counter
// appended when they are all spoken for.
func (n *namer) take(candidates ...string) string {
	first := ""
	for _, c := range candidates {
		c = Slugify(c)
		if c == "" {
			continue
		}
		if first == "" {
			first = c
		}
		if !n.taken[c] {
			n.taken[c] = true
			return c
		}
	}
	if first == "" {
		first = "untitled"
	}
	// Leave room for the counter: Slugify truncates at 64 characters, and a
	// suffix that got truncated away would never become unique.
	if len(first) > 58 {
		first = strings.Trim(first[:58], "-")
	}
	for i := 2; ; i++ {
		c := Slugify(fmt.Sprintf("%s-%d", first, i))
		if !n.taken[c] {
			n.taken[c] = true
			return c
		}
	}
}

// ignoredInTree reports paths that are never content of ours: build output,
// dependency directories, and hidden files other than an MCP config.
func ignoredInTree(p string) bool {
	if strings.TrimSpace(p) == "" {
		return true
	}
	parts := strings.Split(p, "/")
	for _, seg := range parts[:len(parts)-1] {
		if seg == "" || strings.HasPrefix(seg, ".") || excludeDirsAnyDepth[seg] {
			return true
		}
	}
	base := parts[len(parts)-1]
	if strings.HasPrefix(base, ".") && !mcpConfigNames[strings.ToLower(base)] {
		return true
	}
	return excludeFiles[base] || excludeExts[strings.ToLower(path.Ext(base))]
}

// underSkill reports whether a file belongs to one of the skill directories.
func underSkill(p string, skillDirs map[string]bool) bool {
	for d := treeDir(p); ; d = treeDir(d) {
		if skillDirs[d] {
			return true
		}
		if d == "" {
			return false
		}
	}
}

// treeDir is path.Dir with the tree root spelled "" rather than ".".
func treeDir(p string) string {
	d := path.Dir(p)
	if d == "." || d == "/" {
		return ""
	}
	return d
}

// dirName names a directory, falling back for the tree root itself.
func dirName(dir, fallback string) string {
	if dir == "" {
		return fallback
	}
	return path.Base(dir)
}

// hasSegment reports whether a directory path contains the named segment.
func hasSegment(dir, want string) bool {
	if dir == "" {
		return false
	}
	for _, seg := range strings.Split(dir, "/") {
		if strings.EqualFold(seg, want) {
			return true
		}
	}
	return false
}
