package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Circe used to keep three parallel kinds of folder: bundles for skills,
// rulesets for rules, and mcpsets for servers. They are one thing now, because
// what you reach for together is rarely all one kind.
//
// Anyone who used the old build has data in the old directories. This folds it
// into bundles on the first boot of the new one, then removes the old
// directories so the migration cannot run twice and leave duplicates.
//
// A ruleset and a bundle with the same name merge into one folder, which is the
// common case: people named them after the same project.

type legacySet struct {
	Name      string    `json:"name"`
	Title     string    `json:"title"`
	Desc      string    `json:"description"`
	Rules     []string  `json:"rules"`
	Servers   []string  `json:"servers"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// migrateLegacyFolders runs before the indexes are built, so it works directly
// on disk rather than through the store.
func (s *Store) migrateLegacyFolders() error {
	moved := 0
	for _, dir := range []string{"rulesets", "mcpsets"} {
		n, err := s.foldLegacyDir(dir)
		if err != nil {
			return err
		}
		moved += n
	}
	if moved > 0 {
		fmt.Fprintf(os.Stderr,
			"circe: folded %d legacy folder(s) into bundles; rules, skills and servers now share one folder\n", moved)
	}
	return nil
}

func (s *Store) foldLegacyDir(dir string) (int, error) {
	path := filepath.Join(s.root, dir)
	entries, err := os.ReadDir(path)
	if err != nil {
		// Nothing to do on a fresh install, which is the usual case.
		return 0, nil
	}

	moved := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if ValidateName(name) != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(path, e.Name()))
		if err != nil {
			continue
		}
		var old legacySet
		if err := json.Unmarshal(raw, &old); err != nil {
			fmt.Fprintf(os.Stderr, "circe: skipping unreadable legacy folder %s/%s: %v\n", dir, e.Name(), err)
			continue
		}

		if err := s.foldIntoBundle(name, old); err != nil {
			return moved, err
		}
		moved++
	}

	// Only remove the directory once every file in it has been folded in.
	if err := os.RemoveAll(path); err != nil {
		return moved, fmt.Errorf("remove legacy %s: %w", dir, err)
	}
	return moved, nil
}

// foldIntoBundle merges one legacy folder into the bundle of the same name,
// creating it when there is not one already.
func (s *Store) foldIntoBundle(name string, old legacySet) error {
	bundlePath := filepath.Join(s.bundlesDir(), name+".json")

	b := &Bundle{Name: name, Skills: []string{}}
	if raw, err := os.ReadFile(bundlePath); err == nil {
		if err := json.Unmarshal(raw, b); err != nil {
			return fmt.Errorf("read bundle %q while migrating: %w", name, err)
		}
		b.Name = name
	} else {
		// Only inherit the label when there is no bundle to take it from.
		b.Title, b.Description = old.Title, old.Desc
	}

	b.Rules = mergeNames(b.Rules, old.Rules)
	b.Servers = mergeNames(b.Servers, old.Servers)
	if b.Skills == nil {
		b.Skills = []string{}
	}
	b.UpdatedAt = time.Now()

	out, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.bundlesDir(), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(bundlePath, append(out, '\n'))
}

// mergeNames unions two lists without reordering what was already there.
func mergeNames(have, add []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(have)+len(add))
	for _, v := range append(append([]string{}, have...), add...) {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return false })
	return out
}
