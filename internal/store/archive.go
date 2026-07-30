package store

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Exclusion rules mirroring Anthropic's package_skill.py, plus .venv which that
// script omits. A single virtualenv can add thousands of files to a bundle.
var (
	excludeDirsAnyDepth = map[string]bool{
		"__pycache__": true, "node_modules": true, ".venv": true,
		"venv": true, ".git": true,
	}
	excludeFiles    = map[string]bool{".DS_Store": true, "Thumbs.db": true}
	excludeExts     = map[string]bool{".pyc": true, ".pyo": true}
	excludeRootDirs = map[string]bool{"evals": true}
)

func excluded(rel string, isDir bool) bool {
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return false
	}
	parts := strings.Split(rel, "/")
	if excludeRootDirs[parts[0]] {
		return true
	}
	for _, p := range parts {
		if excludeDirsAnyDepth[p] {
			return true
		}
	}
	base := parts[len(parts)-1]
	if !isDir {
		if excludeFiles[base] || excludeExts[strings.ToLower(filepath.Ext(base))] {
			return true
		}
	}
	return false
}

// walkSkillFiles yields every packable file in a skill as (absolute, relative).
func (s *Store) walkSkillFiles(name string, fn func(abs, rel string, info fs.FileInfo) error) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	dir := s.skillDir(name)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("skill %q: %w", name, ErrNotFound)
	}
	// walkResolved follows symlinks, so a symlinked skill or reference
	// directory is packaged by value rather than silently omitted.
	return walkResolved(dir, func(e WalkEntry) error {
		if excluded(e.Rel, false) {
			return nil
		}
		return fn(e.Abs, e.Rel, e.Info)
	})
}

// WriteSkillZip writes a single skill as a .skill archive: a ZIP whose entries
// are all nested under one top-level directory named after the skill.
func (s *Store) WriteSkillZip(w io.Writer, name string) error {
	zw := zip.NewWriter(w)
	if err := s.addSkillToZip(zw, name, name); err != nil {
		zw.Close()
		return err
	}
	return zw.Close()
}

// WriteBundleZip writes every skill in a bundle into one ZIP, each under its own
// top-level directory, alongside a marketplace manifest so the archive can be
// consumed directly by Claude Code.
func (s *Store) WriteBundleZip(w io.Writer, b *Bundle, manifest []byte) error {
	zw := zip.NewWriter(w)
	for _, name := range b.Skills {
		if err := s.addSkillToZip(zw, name, path.Join("skills", name)); err != nil {
			zw.Close()
			return err
		}
	}
	if len(manifest) > 0 {
		f, err := zw.Create(".claude-plugin/marketplace.json")
		if err != nil {
			zw.Close()
			return err
		}
		if _, err := f.Write(manifest); err != nil {
			zw.Close()
			return err
		}
	}
	return zw.Close()
}

func (s *Store) addSkillToZip(zw *zip.Writer, name, prefix string) error {
	return s.walkSkillFiles(name, func(abs, rel string, info fs.FileInfo) error {
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = path.Join(prefix, rel)
		hdr.Method = zip.Deflate
		out, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		in, err := os.Open(abs)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

// WriteSkillsTarGz streams the named skills as a gzipped tar, each under its own
// top-level directory. This is what the CLI downloads.
func (s *Store) WriteSkillsTarGz(w io.Writer, names []string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	for _, name := range names {
		err := s.walkSkillFiles(name, func(abs, rel string, info fs.FileInfo) error {
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = path.Join(name, rel)
			hdr.Uname, hdr.Gname = "", ""
			hdr.Uid, hdr.Gid = 0, 0
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			in, err := os.Open(abs)
			if err != nil {
				return err
			}
			defer in.Close()
			_, err = io.Copy(tw, in)
			return err
		})
		if err != nil {
			tw.Close()
			gz.Close()
			return err
		}
	}

	if err := tw.Close(); err != nil {
		gz.Close()
		return err
	}
	return gz.Close()
}
