package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Context files are project-level agent instructions that ship with a folder.
// Installing the folder merges them into the target project's own files.
//
// They live beside the folder manifest as data/bundles/<name>/CLAUDE.md rather
// than inside the JSON, so they stay readable and diffable like everything else
// in the data directory. Reload already skips directories in bundles/, so this
// layout is backward compatible with folders that have no context files.
var contextFiles = []string{"CLAUDE.md", "AGENTS.md"}

// ValidContextFile reports whether name is a context file we manage.
func ValidContextFile(name string) bool {
	for _, f := range contextFiles {
		if f == name {
			return true
		}
	}
	return false
}

// ContextFileNames lists the supported context files in display order.
func ContextFileNames() []string { return append([]string(nil), contextFiles...) }

func (s *Store) contextDir(bundle string) string {
	return filepath.Join(s.bundlesDir(), bundle)
}

// BundleContext returns one context file's content, or an empty string when the
// folder does not define it.
func (s *Store) BundleContext(bundle, file string) (string, error) {
	if err := ValidateName(bundle); err != nil {
		return "", err
	}
	if !ValidContextFile(file) {
		return "", fmt.Errorf("unsupported context file %q", file)
	}
	raw, err := os.ReadFile(filepath.Join(s.contextDir(bundle), file))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// BundleContexts returns every context file the folder defines, keyed by name.
func (s *Store) BundleContexts(bundle string) map[string]string {
	out := map[string]string{}
	for _, f := range contextFiles {
		if c, err := s.BundleContext(bundle, f); err == nil && strings.TrimSpace(c) != "" {
			out[f] = c
		}
	}
	return out
}

// ContextSummary lists which context files a folder defines, for list pages.
func (s *Store) ContextSummary(bundle string) []string {
	var out []string
	for name := range s.BundleContexts(bundle) {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// SaveBundleContext writes a context file. Empty content removes it, so
// clearing the textarea in the UI is how you delete one.
func (s *Store) SaveBundleContext(bundle, file, content string) error {
	if err := ValidateName(bundle); err != nil {
		return err
	}
	if !ValidContextFile(file) {
		return fmt.Errorf("unsupported context file %q", file)
	}
	if _, err := s.Bundle(bundle); err != nil {
		return err
	}

	dir := s.contextDir(bundle)
	path := filepath.Join(dir, file)

	if strings.TrimSpace(content) == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		// Prune the directory once it holds nothing.
		if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
			os.Remove(dir)
		}
		return nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasSuffix(normalized, "\n") {
		normalized += "\n"
	}
	return writeFileAtomic(path, []byte(normalized))
}

// deleteBundleContexts removes a folder's context directory, used when the
// folder itself is deleted or renamed.
func (s *Store) deleteBundleContexts(bundle string) error {
	if err := ValidateName(bundle); err != nil {
		return err
	}
	return os.RemoveAll(s.contextDir(bundle))
}

// MoveBundleContexts relocates context files when a folder is renamed.
func (s *Store) MoveBundleContexts(oldName, newName string) error {
	if err := ValidateName(oldName); err != nil {
		return err
	}
	if err := ValidateName(newName); err != nil {
		return err
	}
	src := s.contextDir(oldName)
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil
	}
	return os.Rename(src, s.contextDir(newName))
}
