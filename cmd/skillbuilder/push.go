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

	"github.com/sidharthp/skillbuilder/internal/installer"
	"github.com/sidharthp/skillbuilder/internal/skillfile"
)

func newPushCmd() *cobra.Command {
	var (
		tags   []string
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "push <dir>",
		Short: "Publish a local skill directory to the registry",
		Long: "Read a skill directory's SKILL.md and create or replace that skill in the\n" +
			"registry. The skill's name comes from its frontmatter, or from the\n" +
			"directory name when the frontmatter has none.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPush(cmd, args[0], tags, dryRun)
		},
	}
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "tag to publish with (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be published without publishing it")
	return cmd
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
	return req, unpublished, nil
}

// maxPushFileSize matches the server's per-file limit.
const maxPushFileSize = 4 << 20

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
