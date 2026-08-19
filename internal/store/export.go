package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/sidharthpunathil/circe/internal/model"
)

// The registry can always be written back out as ordinary files, in exactly the
// layout every earlier release kept on disk:
//
//	skills/<name>/SKILL.md      and any companion files beside it
//	rules/<name>.md
//	mcps/<name>.json
//	bundles/<name>.json         the folder manifest
//	bundles/<name>/CLAUDE.md    a folder's own context documents
//
// This is what `circe-server export` writes, what the git sync commits, and
// what ImportTree reads back. It is the reason the database is a detail: the
// content is still plain markdown and JSON, and getting it out is a copy.
//
// Two things are deliberately never exported. Access tokens, because a token in
// a repository is a token that has leaked. Share links, because a share id is a
// bearer capability: anyone holding it can read that folder without the
// registry's password, so it belongs to the deployment rather than the backup.

// legacyAdoptedKey marks that the pre-database directory tree has been read.
const legacyAdoptedKey = "legacy_tree_adopted"

// ExportTree writes the whole registry to dir as plain files, creating it if
// needed. Existing files are overwritten; nothing is deleted, so exporting over
// a working copy leaves unrelated files (a README, a LICENSE) alone.
func (s *Store) ExportTree(dir string) error {
	files, err := s.Snapshot()
	if err != nil {
		return err
	}
	for rel, data := range files {
		if err := writeFileAtomic(filepath.Join(dir, filepath.FromSlash(rel)), data); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
	}
	return nil
}

// Snapshot renders the registry into an in-memory tree keyed by slash-separated
// relative path. The git sync commits exactly this map, so what a commit
// contains and what an export writes cannot drift apart.
func (s *Store) Snapshot() (map[string][]byte, error) {
	out := map[string][]byte{}

	for _, sk := range s.Skills() {
		entries, err := s.skillContents(sk.Name)
		if err != nil {
			return nil, fmt.Errorf("skill %s: %w", sk.Name, err)
		}
		for _, e := range entries {
			out[treePath("skills", sk.Name, e.Rel)] = e.Data
		}
	}

	for _, r := range s.Rules() {
		rec, err := s.findByField(colRules, "name", r.Name)
		if err != nil {
			return nil, err
		}
		out[treePath("rules", r.Name+".md")] = []byte(rec.GetString("source"))
	}

	for _, m := range s.MCPServers() {
		rec, err := s.findByField(colMCPs, "name", m.Name)
		if err != nil {
			return nil, err
		}
		out[treePath("mcps", m.Name+".json")] = []byte(rec.GetString("source"))
	}

	for _, b := range s.Bundles() {
		rec, err := s.findByField(colBundles, "name", b.Name)
		if err != nil {
			return nil, err
		}
		out[treePath("bundles", b.Name+".json")] = []byte(rec.GetString("source"))
		for name, content := range s.BundleContexts(b.Name) {
			out[treePath("bundles", b.Name, name)] = []byte(content)
		}
	}

	// Provenance for imported skills, so a restored registry still knows where
	// its content came from and can check it for updates.
	if sources, err := s.all(colSources); err == nil && len(sources) > 0 {
		merged := map[string]json.RawMessage{}
		for _, rec := range sources {
			merged[rec.GetString("name")] = json.RawMessage(rec.GetString("value"))
		}
		raw, err := json.MarshalIndent(merged, "", "  ")
		if err != nil {
			return nil, err
		}
		out["sources.json"] = append(raw, '\n')
	}

	return out, nil
}

// treePath joins slash-separated components for the snapshot map.
func treePath(parts ...string) string { return strings.Join(parts, "/") }

// ImportTree reads a directory in the export layout back into the registry.
// Existing entries are replaced only when overwrite is true.
func (s *Store) ImportTree(dir string, overwrite bool) (ImportResult, error) {
	res := ImportResult{Imported: []string{}, Skipped: []string{}, Failed: []string{}}

	// Skills: one directory each, containing a SKILL.md.
	entries, err := os.ReadDir(filepath.Join(dir, "skills"))
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			if ValidateName(name) != nil {
				continue
			}
			src := filepath.Join(dir, "skills", name)
			// Resolve rather than trusting DirEntry.IsDir: a real skills
			// library is full of symlinks into plugin directories, and a
			// symlinked directory reports itself as a plain file here.
			if info, err := os.Stat(src); err != nil || !info.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err != nil {
				continue
			}
			if _, err := s.findByField(colSkills, "name", name); err == nil && !overwrite {
				res.Skipped = append(res.Skipped, name)
				continue
			}
			files, err := readTree(src)
			if err != nil {
				res.Failed = append(res.Failed, name+": "+err.Error())
				continue
			}
			if err := s.WriteSkillFilesAt(name, files, true, modTime(filepath.Join(src, "SKILL.md"))); err != nil {
				res.Failed = append(res.Failed, name+": "+err.Error())
				continue
			}
			res.Imported = append(res.Imported, name)
		}
	}

	// Rules and servers: one document each.
	for _, kind := range []struct {
		dir, ext string
		load     func(name string, raw []byte, at time.Time) error
	}{
		{"rules", ".md", s.importRuleSource},
		{"mcps", ".json", s.importMCPSource},
	} {
		files, err := os.ReadDir(filepath.Join(dir, kind.dir))
		if err != nil {
			continue
		}
		for _, e := range files {
			if e.IsDir() || !strings.HasSuffix(e.Name(), kind.ext) {
				continue
			}
			name := strings.TrimSuffix(e.Name(), kind.ext)
			if ValidateName(name) != nil {
				continue
			}
			full := filepath.Join(dir, kind.dir, e.Name())
			raw, err := os.ReadFile(full)
			if err != nil {
				res.Failed = append(res.Failed, name+": "+err.Error())
				continue
			}
			if err := kind.load(name, raw, modTime(full)); err != nil {
				res.Failed = append(res.Failed, name+": "+err.Error())
				continue
			}
		}
	}

	// Folders last, so the references they prune against already exist.
	if err := s.Reload(); err != nil {
		return res, err
	}
	bundles, err := os.ReadDir(filepath.Join(dir, "bundles"))
	if err == nil {
		for _, e := range bundles {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".json")
			if ValidateName(name) != nil {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, "bundles", e.Name()))
			if err != nil {
				res.Failed = append(res.Failed, name+": "+err.Error())
				continue
			}
			var b Bundle
			if err := json.Unmarshal(raw, &b); err != nil {
				res.Failed = append(res.Failed, name+": "+err.Error())
				continue
			}
			b.Name = name
			at := b.UpdatedAt
			if at.IsZero() {
				at = modTime(filepath.Join(dir, "bundles", e.Name()))
			}
			if err := s.saveBundleAt(&b, at); err != nil {
				res.Failed = append(res.Failed, name+": "+err.Error())
				continue
			}
			// A folder's context documents sit in a directory beside its manifest.
			for _, cf := range contextFiles {
				body, err := os.ReadFile(filepath.Join(dir, "bundles", name, cf))
				if err != nil {
					continue
				}
				if err := s.SaveBundleContext(name, cf, string(body)); err != nil {
					res.Failed = append(res.Failed, name+"/"+cf+": "+err.Error())
				}
			}
		}
	}

	if raw, err := os.ReadFile(filepath.Join(dir, "sources.json")); err == nil {
		merged := map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &merged); err == nil {
			for name, value := range merged {
				if err := s.SaveSource(name, []byte(value)); err != nil {
					res.Failed = append(res.Failed, "sources/"+name+": "+err.Error())
				}
			}
		}
	}

	sort.Strings(res.Imported)
	return res, s.Reload()
}

// importRuleSource stores a rule document verbatim, preserving whatever its
// author wrote rather than re-rendering it from the parsed struct.
func (s *Store) importRuleSource(name string, raw []byte, at time.Time) error {
	r, err := model.ParseRule(name, raw)
	if err != nil {
		return err
	}
	r.UpdatedAt = at
	return s.SaveRule(r)
}

// importMCPSource stores a server definition.
func (s *Store) importMCPSource(name string, raw []byte, at time.Time) error {
	var m MCPServer
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	m.Name = name
	// The document's own timestamp wins; the file's is only a fallback for a
	// hand-written one that has none.
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = at
	}
	return s.saveMCPServerAt(&m, m.UpdatedAt)
}

// modTime reports a file's modification time, or the zero time.
func modTime(p string) time.Time {
	if info, err := os.Stat(p); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

// ---------- provenance ----------

// Source returns the recorded provenance for a skill, if any.
func (s *Store) Source(name string) ([]byte, bool) {
	rec, err := s.findByField(colSources, "name", name)
	if err != nil {
		return nil, false
	}
	return []byte(rec.GetString("value")), true
}

// Sources returns all recorded provenance, keyed by skill name.
func (s *Store) Sources() map[string][]byte {
	out := map[string][]byte{}
	recs, err := s.all(colSources)
	if err != nil {
		return out
	}
	for _, rec := range recs {
		out[rec.GetString("name")] = []byte(rec.GetString("value"))
	}
	return out
}

// SaveSource records where a skill came from.
func (s *Store) SaveSource(name string, value []byte) error {
	return s.upsert(colSources, "name", name, func(rec *core.Record) {
		rec.Set("value", string(value))
	})
}

// DeleteSource forgets a skill's provenance.
func (s *Store) DeleteSource(name string) error {
	rec, err := s.findByField(colSources, "name", name)
	if err != nil {
		return nil
	}
	return s.app.Delete(rec)
}

// ---------- upgrade from the pre-database layout ----------

// adoptLegacyTree imports the directory tree an older release left in the data
// directory, once. The tree itself is not touched: it stays as a backup, and a
// downgrade still finds everything where it was.
func (s *Store) adoptLegacyTree() error {
	if err := s.adoptLegacyCredentials(); err != nil {
		return err
	}
	if done, _ := s.kvBool(legacyAdoptedKey); done {
		return nil
	}
	// Mark first. A tree that fails to import halfway is a problem to report,
	// not a reason to import it again on every restart and duplicate the half
	// that worked.
	if err := s.setKV(legacyAdoptedKey, true); err != nil {
		return err
	}

	found := false
	for _, sub := range []string{"skills", "rules", "mcps", "bundles"} {
		if entries, err := os.ReadDir(filepath.Join(s.root, sub)); err == nil && len(entries) > 0 {
			found = true
			break
		}
	}
	if !found {
		return nil
	}

	res, err := s.ImportTree(s.root, true)
	if err != nil {
		return fmt.Errorf("import existing data directory: %w", err)
	}
	fmt.Fprintf(os.Stderr, "circe: adopted %d skills from the existing data directory\n", len(res.Imported))
	for _, f := range res.Failed {
		fmt.Fprintf(os.Stderr, "circe: could not adopt %s\n", f)
	}
	return nil
}
