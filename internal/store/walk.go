package store

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxWalkDepth bounds recursion so a pathological symlink arrangement cannot
// walk forever even if the visited-set check is somehow evaded.
const maxWalkDepth = 32

// WalkEntry is one file found by walkResolved.
type WalkEntry struct {
	Abs  string      // absolute path on disk
	Rel  string      // slash-separated path relative to the walk root
	Info os.FileInfo // resolved (symlinks followed)
}

// walkResolved walks root and reports every regular file beneath it, following
// symlinked directories.
//
// filepath.WalkDir deliberately does not follow symlinks, which makes it the
// wrong tool here: roughly half of a real ~/.claude/skills library consists of
// symlinks into plugin directories, and walking them with WalkDir silently
// yields nothing. Directories already visited (by resolved path) are skipped so
// that a symlink cycle terminates.
func walkResolved(root string, fn func(WalkEntry) error) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", root, err)
	}
	info, err := os.Stat(realRoot)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fn(WalkEntry{Abs: realRoot, Rel: filepath.Base(realRoot), Info: info})
	}
	visited := map[string]bool{realRoot: true}
	return walkDir(realRoot, "", fn, visited, 0)
}

func walkDir(dir, prefix string, fn func(WalkEntry) error, visited map[string]bool, depth int) error {
	if depth > maxWalkDepth {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // an unreadable subdirectory should not abort the whole walk
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || skipDirs[name] {
			continue
		}
		abs := filepath.Join(dir, name)
		rel := name
		if prefix != "" {
			rel = path.Join(prefix, name)
		}

		info, err := os.Stat(abs) // resolves symlinks
		if err != nil {
			continue // dangling symlink
		}

		if info.IsDir() {
			real, err := filepath.EvalSymlinks(abs)
			if err != nil || visited[real] {
				continue
			}
			visited[real] = true
			if err := walkDir(abs, rel, fn, visited, depth+1); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if err := fn(WalkEntry{Abs: abs, Rel: rel, Info: info}); err != nil {
			return err
		}
	}
	return nil
}
