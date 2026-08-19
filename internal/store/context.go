package store

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// Context files are project-level agent instructions that ship with a folder.
// Installing the folder merges them into the target project's own files.
//
// They are kept apart from the folder manifest rather than embedded in its
// JSON, so they stay readable and diffable wherever the registry is written
// out: export and git sync put them at bundles/<name>/CLAUDE.md, which is the
// layout every earlier release used.
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

// BundleContext returns one context file's content, or an empty string when the
// folder does not define it.
func (s *Store) BundleContext(bundle, file string) (string, error) {
	if err := ValidateName(bundle); err != nil {
		return "", err
	}
	if !ValidContextFile(file) {
		return "", fmt.Errorf("unsupported context file %q", file)
	}
	rec, err := s.app.FindFirstRecordByFilter(colContexts, "bundle = {:b} && file = {:f}",
		map[string]any{"b": bundle, "f": file})
	if err != nil {
		return "", nil
	}
	return rec.GetString("content"), nil
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

	existing, err := s.app.FindFirstRecordByFilter(colContexts, "bundle = {:b} && file = {:f}",
		map[string]any{"b": bundle, "f": file})

	if strings.TrimSpace(content) == "" {
		if err == nil {
			return s.app.Delete(existing)
		}
		return nil
	}

	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasSuffix(normalized, "\n") {
		normalized += "\n"
	}

	if err != nil {
		c, cerr := s.app.FindCollectionByNameOrId(colContexts)
		if cerr != nil {
			return cerr
		}
		existing = core.NewRecord(c)
		existing.Set("bundle", bundle)
		existing.Set("file", file)
	}
	existing.Set("content", normalized)
	return asStoreError(s.app.Save(existing))
}

// deleteBundleContexts drops a folder's context documents, used when the folder
// itself is deleted.
func (s *Store) deleteBundleContexts(bundle string) error {
	if err := ValidateName(bundle); err != nil {
		return err
	}
	recs, err := s.app.FindRecordsByFilter(colContexts, "bundle = {:b}", "", 0, 0,
		map[string]any{"b": bundle})
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if err := s.app.Delete(rec); err != nil {
			return err
		}
	}
	return nil
}

// MoveBundleContexts relocates context files when a folder is renamed.
func (s *Store) MoveBundleContexts(oldName, newName string) error {
	if err := ValidateName(oldName); err != nil {
		return err
	}
	if err := ValidateName(newName); err != nil {
		return err
	}
	recs, err := s.app.FindRecordsByFilter(colContexts, "bundle = {:b}", "", 0, 0,
		map[string]any{"b": oldName})
	if err != nil {
		return err
	}
	for _, rec := range recs {
		rec.Set("bundle", newName)
		if err := asStoreError(s.app.Save(rec)); err != nil {
			return err
		}
	}
	return nil
}
