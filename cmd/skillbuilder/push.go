package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/sidharthpunathil/skillbuilder/internal/installer"
	"github.com/sidharthpunathil/skillbuilder/internal/skillfile"
)

func newPushCmd() *cobra.Command {
	var (
		tags   []string
		dryRun bool
		force  bool
	)
	cmd := &cobra.Command{
		Use:   "push <dir>",
		Short: "Publish local skills to the registry",
		Long: "Publish one skill, or a whole directory of them.\n\n" +
			"If <dir> contains a SKILL.md it is published as a single skill. Otherwise\n" +
			"every subdirectory containing a SKILL.md is published, which is how you get\n" +
			"a local library such as ~/.claude/skills into a hosted registry.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPushPath(cmd, args[0], tags, dryRun, force)
		},
	}
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "tag to publish with (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be published without publishing it")
	cmd.Flags().BoolVar(&force, "force", false, "replace skills that already exist in the registry")
	return cmd
}

// runPushPath decides between a single skill and a library of them, so the
// user does not have to know which shape they are pointing at.
func runPushPath(cmd *cobra.Command, dir string, tags []string, dryRun, force bool) error {
	// A quoted "~/.claude/skills" reaches us unexpanded, and the docs tell
	// people to type exactly that.
	dir = expandTilde(dir)
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}

	if _, err := os.Stat(filepath.Join(abs, "SKILL.md")); err == nil {
		return runPush(cmd, abs, tags, dryRun)
	}

	children, err := skillDirsIn(abs)
	if err != nil {
		return err
	}
	if len(children) == 0 {
		return fmt.Errorf("no SKILL.md in %s, and no subdirectory contains one", dir)
	}
	return runPushMany(cmd, children, tags, dryRun, force)
}

// skillDirsIn lists immediate subdirectories that are skills. Symlinks are
// resolved, since a real skills library is largely symlinks into plugin dirs.
func skillDirsIn(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(root, e.Name())
		if info, err := os.Stat(full); err != nil || !info.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(full, "SKILL.md")); err == nil {
			out = append(out, full)
		}
	}
	sort.Strings(out)
	return out, nil
}

// runPushMany publishes a whole library, reporting per skill and continuing
// past failures so one malformed skill does not abandon the rest.
func runPushMany(cmd *cobra.Command, dirs []string, tags []string, dryRun, force bool) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	client, err := newClient()
	if err != nil {
		return err
	}
	fmt.Fprintln(out, dimStyle.Render(fmt.Sprintf("%d %s to publish to %s",
		len(dirs), pluralWord(len(dirs), "skill", "skills"), client.BaseURL)))

	col := 0
	type item struct {
		req   installer.PushRequest
		extra []string
		err   error
	}
	items := make([]item, 0, len(dirs))
	for _, d := range dirs {
		req, extra, err := readSkillDir(d, tags)
		items = append(items, item{req, extra, err})
		if n := len(req.Name); n > col {
			col = n
		}
		if n := len(filepath.Base(d)); err != nil && n > col {
			col = n
		}
	}

	var created, updated, skipped, failed int
	for i, it := range items {
		label := it.req.Name
		if label == "" {
			label = filepath.Base(dirs[i])
		}
		pad := strings.Repeat(" ", max(0, col-len(label)))

		if it.err != nil {
			failed++
			fmt.Fprintln(out, "  "+errStyle.Render("✗")+" "+label+pad+"  "+dimStyle.Render(it.err.Error()))
			continue
		}

		exists, err := client.Exists(ctx, it.req.Name)
		if err != nil {
			return err
		}
		if exists && !force {
			skipped++
			fmt.Fprintln(out, "  "+dimStyle.Render("· "+label+pad+"  already in the registry (--force to replace)"))
			continue
		}
		if dryRun {
			verb := "create"
			if exists {
				verb = "replace"
			}
			fmt.Fprintln(out, "  "+dimStyle.Render("· "+label+pad+"  would "+verb))
			continue
		}

		if exists {
			err = client.UpdateSkill(ctx, it.req)
		} else {
			err = client.CreateSkill(ctx, it.req)
		}
		if err != nil {
			failed++
			fmt.Fprintln(out, "  "+errStyle.Render("✗")+" "+label+pad+"  "+dimStyle.Render(err.Error()))
			continue
		}
		if exists {
			updated++
			fmt.Fprintln(out, "  "+okStyle.Render("✓")+" "+label+pad+"  "+okStyle.Render("updated"))
		} else {
			created++
			fmt.Fprintln(out, "  "+okStyle.Render("✓")+" "+label+pad+"  "+okStyle.Render("created"))
		}
	}

	var parts []string
	for _, p := range []struct {
		n int
		s string
	}{{created, "created"}, {updated, "updated"}, {skipped, "skipped"}, {failed, "failed"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.s))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "nothing to do")
	}
	fmt.Fprintln(out, dimStyle.Render(strings.Join(parts, ", ")))
	if failed > 0 {
		return fmt.Errorf("%d %s could not be published", failed, pluralWord(failed, "skill", "skills"))
	}
	return nil
}

func runPush(cmd *cobra.Command, dir string, tags []string, dryRun bool) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", dir, err)
	}
	req, extra, err := readSkillDir(abs, tags)
	if err != nil {
		return err
	}

	client, err := newClient()
	if err != nil {
		return err
	}
	exists, err := client.Exists(ctx, req.Name)
	if err != nil {
		return err
	}

	// Text companion files are uploaded; anything binary or oversized is not,
	// so name those explicitly rather than dropping them silently.
	if len(extra) > 0 {
		fmt.Fprintln(out, warnStyle.Render("!")+" "+dimStyle.Render(fmt.Sprintf(
			"%d file(s) could not be published: %s", len(extra), strings.Join(extra, ", "))))
	}

	verb := "created"
	if exists {
		verb = "updated"
	}
	if dryRun {
		fmt.Fprintln(out, dimStyle.Render(fmt.Sprintf("dry run — would have %s %s at %s (%d bytes, tags: %s)",
			verb, req.Name, client.BaseURL, len(req.Body), describeTags(req.Tags))))
		return nil
	}

	if exists {
		err = client.UpdateSkill(ctx, req)
	} else {
		err = client.CreateSkill(ctx, req)
	}
	if err != nil {
		return fmt.Errorf("publish %s: %w", req.Name, err)
	}
	fmt.Fprintln(out, "  "+okStyle.Render("✓")+" "+req.Name+"  "+okStyle.Render(verb)+dimStyle.Render(" at "+client.BaseURL))
	return nil
}

// readSkillDir parses a skill directory into a push request, returning the
// companion files that the wire format cannot carry.
func readSkillDir(dir string, tags []string) (installer.PushRequest, []string, error) {
	var req installer.PushRequest

	info, err := os.Stat(dir)
	if err != nil {
		return req, nil, fmt.Errorf("read %s: %w", dir, err)
	}
	if !info.IsDir() {
		return req, nil, fmt.Errorf("%s is not a directory — push takes a skill directory containing SKILL.md", dir)
	}

	src, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if errors.Is(err, fs.ErrNotExist) {
		return req, nil, fmt.Errorf("no SKILL.md in %s — a skill directory must contain one", dir)
	}
	if err != nil {
		return req, nil, fmt.Errorf("read SKILL.md: %w", err)
	}

	f, err := skillfile.Parse(src)
	if err != nil {
		return req, nil, fmt.Errorf("parse %s/SKILL.md: %w", dir, err)
	}
	if strings.TrimSpace(f.Name) == "" {
		f.Name = filepath.Base(dir)
	}
	if strings.TrimSpace(f.Description) == "" {
		f.Description = f.Summary(200)
	}

	var fatal []string
	for _, p := range f.Validate() {
		if p.Fatal {
			fatal = append(fatal, p.Field+": "+p.Message)
		}
	}
	if len(fatal) > 0 {
		return req, nil, fmt.Errorf("%s is not a valid skill — %s", dir, strings.Join(fatal, "; "))
	}

	extra, err := companionFiles(dir)
	if err != nil {
		return req, nil, err
	}

	// Companion files travel with the push so that references/ and scripts/
	// survive. Binary and oversized files are reported back to the caller
	// instead, since the payload is JSON text.
	files := map[string]string{}
	var unpublished []string
	for _, rel := range extra {
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			unpublished = append(unpublished, rel+" (unreadable)")
			continue
		}
		if len(raw) > maxPushFileSize {
			unpublished = append(unpublished, rel+" (too large)")
			continue
		}
		if !utf8.Valid(raw) {
			unpublished = append(unpublished, rel+" (binary)")
			continue
		}
		files[rel] = string(raw)
	}

	req = installer.PushRequest{
		Name:        f.Name,
		Description: f.Description,
		Body:        f.Body,
		Tags:        normalizeTags(append(tags, frontmatterTags(f)...)),
		Files:       files,
	}

	// The registry caps a push at 16 MiB. Say so here, naming the biggest
	// offenders, rather than letting the server answer with a bare 400.
	total := len(req.Body)
	for _, c := range files {
		total += len(c)
	}
	if total > maxPushPayload {
		return req, unpublished, fmt.Errorf(
			"%s is %s of text across %d files, over the %d MiB limit — %s",
			f.Name, humanSize(total), len(files)+1, maxPushPayload>>20,
			"trim it, or keep large assets out of the skill directory")
	}
	return req, unpublished, nil
}

// maxPushFileSize matches the server's per-file limit, and maxPushPayload its
// whole-request limit.
const (
	maxPushFileSize = 4 << 20
	maxPushPayload  = 16 << 20
)

func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// companionFiles lists everything in a skill directory except SKILL.md itself.
func companionFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); p != dir && (name == ".git" || name == "node_modules" || name == "__pycache__") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel != "SKILL.md" && d.Name() != ".DS_Store" {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	sort.Strings(out)
	return out, nil
}

// excludedFromPush mirrors the server's packaging exclusions. .venv matters
// most: a skill that vendors a virtualenv is hundreds of megabytes, which the
// registry rejects outright, and none of it is part of the skill.
func excludedFromPush(name string) bool {
	switch name {
	case ".git", "node_modules", "__pycache__", ".venv", "venv", "evals":
		return true
	}
	return false
}

// frontmatterTags reads a `tags:` key, which is not part of the official skill
// frontmatter but is common enough to be worth honouring.
func frontmatterTags(f *skillfile.File) []string {
	v, ok := f.Extra["tags"]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case string:
		return strings.Split(t, ",")
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// normalizeTags lowercases, hyphenates and dedupes so the registry sees the same
// shape whether a tag was typed on the command line or written in frontmatter.
func normalizeTags(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range in {
		for _, part := range strings.Split(raw, ",") {
			t := slugify(part)
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func slugify(s string) string {
	var b strings.Builder
	lastHyphen := true // suppress a leading hyphen
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		case !lastHyphen:
			b.WriteRune('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func describeTags(tags []string) string {
	if len(tags) == 0 {
		return "none"
	}
	return strings.Join(tags, ", ")
}

// expandTilde resolves a leading ~ that the shell did not.
func expandTilde(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
}
