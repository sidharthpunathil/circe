package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Context files (CLAUDE.md, AGENTS.md) belong to the project, not to us, so a
// folder's contribution is written inside a clearly marked block. That makes
// installs idempotent, leaves everything the user wrote untouched, lets several
// folders coexist in one file, and is trivially reversible by hand.
const (
	markerPrefix = "<!-- skillbuilder:start "
	markerSuffix = "<!-- skillbuilder:end "
)

// ContextMode selects what happens to an existing file.
type ContextMode string

const (
	// ContextMerge writes our block, preserving everything around it.
	ContextMerge ContextMode = "merge"
	// ContextSkip leaves the project's file completely alone.
	ContextSkip ContextMode = "skip"
	// ContextOverwrite replaces the whole file with our content.
	ContextOverwrite ContextMode = "overwrite"
)

// ContextResult reports what happened to one file.
type ContextResult struct {
	File   string // e.g. "CLAUDE.md"
	Path   string
	Action string // "created", "updated", "unchanged", "skipped", "overwritten"
}

// blockPattern matches a previously written block for a specific folder.
func blockPattern(folder string) *regexp.Regexp {
	return regexp.MustCompile(
		`(?s)\n*` + regexp.QuoteMeta(markerPrefix+folder) + `.*?` +
			regexp.QuoteMeta(markerSuffix+folder+" -->") + `\n?`)
}

// renderBlock wraps content in markers. The opening marker states that the
// block is managed, so nobody has to guess why their edits vanished.
func renderBlock(folder, content string) string {
	body := strings.TrimRight(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	return fmt.Sprintf("%s%s -->\n<!-- Managed by skillbuilder. Edits inside this block are replaced on reinstall. -->\n\n%s\n\n%s%s -->\n",
		markerPrefix, folder, body, markerSuffix, folder)
}

// WriteContextFile merges a folder's context file into the project.
func WriteContextFile(root, folder, file, content string, mode ContextMode, dryRun bool) (ContextResult, error) {
	res := ContextResult{File: file, Path: filepath.Join(root, file)}

	if strings.TrimSpace(content) == "" {
		res.Action = "unchanged"
		return res, nil
	}

	existing, err := os.ReadFile(res.Path)
	switch {
	case os.IsNotExist(err):
		existing = nil
	case err != nil:
		return res, fmt.Errorf("read %s: %w", res.Path, err)
	}
	hadFile := existing != nil

	if hadFile && mode == ContextSkip {
		res.Action = "skipped"
		return res, nil
	}

	var next string
	switch {
	case mode == ContextOverwrite || !hadFile:
		next = renderBlock(folder, content)
		if hadFile {
			res.Action = "overwritten"
		} else {
			res.Action = "created"
		}
	default:
		current := string(existing)
		block := renderBlock(folder, content)
		if re := blockPattern(folder); re.MatchString(current) {
			next = re.ReplaceAllString(current, "\n\n"+block)
			res.Action = "updated"
		} else {
			next = strings.TrimRight(current, "\n") + "\n\n" + block
			res.Action = "updated"
		}
	}

	next = normalizeBlankLines(next)
	if hadFile && next == string(existing) {
		res.Action = "unchanged"
		return res, nil
	}
	if dryRun {
		return res, nil
	}
	if _, err := writeFileAtomic(res.Path, 0o644, strings.NewReader(next)); err != nil {
		return res, fmt.Errorf("write %s: %w", res.Path, err)
	}
	return res, nil
}

// RemoveContextBlock strips a folder's block, used when the folder is removed.
func RemoveContextBlock(root, folder, file string) (bool, error) {
	path := filepath.Join(root, file)
	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	re := blockPattern(folder)
	if !re.Match(existing) {
		return false, nil
	}
	next := normalizeBlankLines(re.ReplaceAllString(string(existing), "\n"))

	// A file that now holds nothing but whitespace was ours alone; remove it
	// rather than leaving an empty CLAUDE.md behind.
	if strings.TrimSpace(next) == "" {
		return true, os.Remove(path)
	}
	_, err = writeFileAtomic(path, 0o644, strings.NewReader(next))
	return true, err
}

// normalizeBlankLines collapses runs of blank lines and ensures a trailing one,
// so repeated merges do not slowly push the file apart.
func normalizeBlankLines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	s = strings.TrimLeft(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}
