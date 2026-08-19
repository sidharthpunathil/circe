package store

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
	"time"
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

// skillEntry is one packable file: the SKILL.md itself or a companion.
type skillEntry struct {
	Rel  string
	Data []byte
	Mod  time.Time
}

// skillContents returns everything a skill ships, SKILL.md first and companions
// in path order, so an archive is byte-for-byte reproducible.
func (s *Store) skillContents(name string) ([]skillEntry, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	rec, err := s.findByField(colSkills, "name", name)
	if err != nil {
		return nil, err
	}
	mod := rec.GetDateTime("updated").Time()
	if mod.IsZero() {
		mod = time.Now()
	}

	out := []skillEntry{{Rel: "SKILL.md", Data: []byte(rec.GetString("source")), Mod: mod}}

	files, err := s.app.FindRecordsByFilter(colSkillFile, "skill = {:id}", "path", 0, 0,
		map[string]any{"id": rec.Id})
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		rel := f.GetString("path")
		if excluded(rel, false) {
			continue
		}
		data, err := s.readBlob(f)
		if err != nil {
			return nil, fmt.Errorf("read %s/%s: %w", name, rel, err)
		}
		out = append(out, skillEntry{Rel: rel, Data: data, Mod: mod})
	}
	return out, nil
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

// WriteBundleZip writes a whole folder into one ZIP: its skills each under
// their own directory, its rules rendered into the file each one targets, and
// its servers as a ready .mcp.json, alongside a marketplace manifest so the
// archive can be consumed directly by Claude Code.
//
// The layout mirrors where the CLI would put each thing in a project, so the
// archive can be read as documentation of what installing the folder does.
func (s *Store) WriteBundleZip(w io.Writer, b *Bundle, manifest []byte) error {
	zw := zip.NewWriter(w)
	add := func(name string, body []byte) error {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = f.Write(body)
		return err
	}

	for _, name := range b.Skills {
		if err := s.addSkillToZip(zw, name, path.Join("skills", name)); err != nil {
			zw.Close()
			return err
		}
	}

	for target, body := range s.RenderBundleRules(b) {
		if err := add(target, []byte(body+"\n")); err != nil {
			zw.Close()
			return err
		}
	}

	if servers := s.BundleServers(b); len(servers) > 0 {
		cfg, err := RenderMCPConfig(servers)
		if err != nil {
			zw.Close()
			return err
		}
		if err := add(".mcp.json", cfg); err != nil {
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
	entries, err := s.skillContents(name)
	if err != nil {
		return err
	}
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: path.Join(prefix, e.Rel), Method: zip.Deflate}
		hdr.SetMode(0o644)
		hdr.Modified = e.Mod
		out, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if _, err := out.Write(e.Data); err != nil {
			return err
		}
	}
	return nil
}

// WriteSkillsTarGz streams the named skills as a gzipped tar, each under its own
// top-level directory. This is what the CLI downloads.
func (s *Store) WriteSkillsTarGz(w io.Writer, names []string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	for _, name := range names {
		entries, err := s.skillContents(name)
		if err != nil {
			tw.Close()
			gz.Close()
			return err
		}
		for _, e := range entries {
			hdr := &tar.Header{
				Name:     path.Join(name, e.Rel),
				Mode:     0o644,
				Size:     int64(len(e.Data)),
				ModTime:  e.Mod,
				Typeflag: tar.TypeReg,
			}
			if err := tw.WriteHeader(hdr); err != nil {
				tw.Close()
				gz.Close()
				return err
			}
			if _, err := tw.Write(e.Data); err != nil {
				tw.Close()
				gz.Close()
				return err
			}
		}
	}

	if err := tw.Close(); err != nil {
		gz.Close()
		return err
	}
	return gz.Close()
}
