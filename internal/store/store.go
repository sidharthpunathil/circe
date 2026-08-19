// Package store persists skills, rules, servers and folders in a PocketBase
// database under the data directory.
//
// Every row keeps the canonical document it came from: the whole SKILL.md for a
// skill, the whole markdown file for a rule, the whole JSON for a server or
// folder. That is what makes the database an implementation detail rather than
// a cage. `Store.ExportTree` renders the registry back to the plain directory
// layout it has always used (skills/<name>/SKILL.md and friends), `ImportTree`
// reads that layout back, and the git sync commits it. So the content stays
// yours, and moving it somewhere else is a copy rather than an export format.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/sidharthpunathil/circe/internal/model"
)

// ErrNotFound is returned when a skill or bundle does not exist.
var ErrNotFound = errors.New("not found")

// ErrExists is returned when creating something that already exists.
var ErrExists = errors.New("already exists")

// maxFileSize caps any single bundled file that the store will load or write.
const maxFileSize = 4 << 20 // 4 MiB

// skipDirs are never treated as bundled content.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, ".DS_Store": true,
}

// Store is a PocketBase-backed collection of skills, rules, servers and
// folders, with an in-memory index for listing and search.
//
// The index is not a cache in front of a slow database, it is what keeps every
// read path in the server unchanged from when this was a directory of files:
// listings, search and ranking all run over parsed structs in memory, which is
// the right shape for a registry that is read constantly and written rarely.
type Store struct {
	root string
	app  core.App

	mu      sync.RWMutex
	skills  map[string]*Skill
	bundles map[string]*Bundle
	rules   map[string]*Rule
	mcps    map[string]*MCPServer
}

// newStore returns a Store with an empty index.
//
// The maps are created here rather than by the first Reload, because adopting
// an existing data directory writes through the normal save paths, and those
// update the index as they go. A nil map is fine to read and panics to write.
func newStore(root string) *Store {
	return &Store{
		root:    root,
		skills:  map[string]*Skill{},
		bundles: map[string]*Bundle{},
		rules:   map[string]*Rule{},
		mcps:    map[string]*MCPServer{},
	}
}

// Open loads (creating if needed) a store rooted at dir.
func Open(dir string) (*Store, error) {
	abs, err := absPath(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}

	s := newStore(abs)

	// Fold the old per-kind folders into bundles before anything reads them.
	// This still runs on the directory tree, because it is fixing up a layout
	// that predates the database.
	if err := s.migrateLegacyFolders(); err != nil {
		return nil, err
	}

	app, err := openApp(abs)
	if err != nil {
		return nil, err
	}
	s.app = app

	// A data directory written by an older release is a tree of files. Pull it
	// in once, then leave it alone: the tree stays on disk untouched, so
	// downgrading is always possible.
	if err := s.adoptLegacyTree(); err != nil {
		s.Close()
		return nil, err
	}

	if err := s.Reload(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// OpenWithApp wraps an already-bootstrapped PocketBase application. The server
// binary runs the full PocketBase app so the dashboard and its REST API are
// available, and hands the same app here rather than opening the database twice.
func OpenWithApp(app core.App) (*Store, error) {
	if err := ensureSchema(app); err != nil {
		return nil, err
	}
	s := newStore(app.DataDir())
	s.app = app
	if err := s.migrateLegacyFolders(); err != nil {
		return nil, err
	}
	if err := s.adoptLegacyTree(); err != nil {
		return nil, err
	}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// App exposes the underlying PocketBase application.
func (s *Store) App() core.App { return s.app }

// Close releases the database handles. Safe to call more than once.
func (s *Store) Close() error {
	if s.app == nil {
		return nil
	}
	err := s.app.ResetBootstrapState()
	s.app = nil
	return err
}

// Root returns the absolute data directory.
func (s *Store) Root() string { return s.root }

// Reload rebuilds the in-memory index from the database.
func (s *Store) Reload() error {
	skills, err := s.loadSkills()
	if err != nil {
		return err
	}
	bundles, err := s.loadBundles()
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.skills, s.bundles = skills, bundles
	s.mu.Unlock()

	if err := s.reloadRules(); err != nil {
		return err
	}
	return s.reloadMCPs()
}

// loadSkills reads every skill and its companion file list in two queries
// rather than one per skill.
func (s *Store) loadSkills() (map[string]*Skill, error) {
	recs, err := s.all(colSkills)
	if err != nil {
		return nil, err
	}
	files, err := s.all(colSkillFile)
	if err != nil {
		return nil, err
	}
	bySkill := map[string][]FileRef{}
	for _, f := range files {
		id := f.GetString("skill")
		bySkill[id] = append(bySkill[id], FileRef{
			Path: f.GetString("path"),
			Size: int64(f.GetInt("size")),
		})
	}

	out := map[string]*Skill{}
	for _, rec := range recs {
		name := rec.GetString("name")
		if ValidateName(name) != nil {
			continue
		}
		sk, err := skillFromRecord(rec, bySkill[rec.Id])
		if err != nil {
			// A malformed skill must not take down the whole index.
			fmt.Fprintf(os.Stderr, "circe: skipping skill %q: %v\n", name, err)
			continue
		}
		out[name] = sk
	}
	return out, nil
}

// skillFromRecord parses a stored SKILL.md back into a Skill. Parsing on read
// rather than trusting the derived columns keeps one source of truth: whatever
// the document says is what the registry serves.
func skillFromRecord(rec *core.Record, files []FileRef) (*Skill, error) {
	sk, err := model.ParseSkill(rec.GetString("name"), []byte(rec.GetString("source")))
	if err != nil {
		return nil, err
	}

	total := int64(len(rec.GetString("source")))
	if files != nil {
		sk.Files = files
		for _, fr := range files {
			total += fr.Size
		}
	}
	sort.Slice(sk.Files, func(i, j int) bool { return sk.Files[i].Path < sk.Files[j].Path })

	sk.UpdatedAt = rec.GetDateTime("updated").Time()
	sk.Size = total
	return sk, nil
}

// loadBundles reads every folder definition.
func (s *Store) loadBundles() (map[string]*Bundle, error) {
	recs, err := s.all(colBundles)
	if err != nil {
		return nil, err
	}
	out := map[string]*Bundle{}
	for _, rec := range recs {
		name := rec.GetString("name")
		if ValidateName(name) != nil {
			continue
		}
		var b Bundle
		if err := json.Unmarshal([]byte(rec.GetString("source")), &b); err != nil {
			fmt.Fprintf(os.Stderr, "circe: skipping bundle %q: %v\n", name, err)
			continue
		}
		b.Name = name
		if b.Skills == nil {
			b.Skills = []string{}
		}
		out[name] = &b
	}
	return out, nil
}

// ---------- skill queries ----------

// Skills returns all skills sorted by name.
func (s *Store) Skills() []*Skill {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Skill, 0, len(s.skills))
	for _, sk := range s.skills {
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Skill returns one skill by name.
func (s *Store) Skill(name string) (*Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sk, ok := s.skills[name]
	if !ok {
		return nil, fmt.Errorf("skill %q: %w", name, ErrNotFound)
	}
	return sk, nil
}

// SkillCount returns the number of stored skills.
func (s *Store) SkillCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.skills)
}

// Search filters skills by free-text query and optional tag. An empty query and
// tag returns everything. Matching is case-insensitive across name, description,
// tags and body, and results are ranked so name matches surface first.
func (s *Store) Search(query, tag string) []*Skill {
	all := s.Skills()
	q := strings.ToLower(strings.TrimSpace(query))
	tag = strings.TrimSpace(tag)

	type scored struct {
		sk    *Skill
		score int
	}
	var hits []scored

	for _, sk := range all {
		if tag != "" && !hasTag(sk.Tags, tag) {
			continue
		}
		if q == "" {
			hits = append(hits, scored{sk, 0})
			continue
		}
		score := 0
		name, desc := strings.ToLower(sk.Name), strings.ToLower(sk.Description)
		switch {
		case name == q:
			score = 100
		case strings.HasPrefix(name, q):
			score = 80
		case strings.Contains(name, q):
			score = 60
		}
		if strings.Contains(desc, q) {
			score += 30
		}
		for _, t := range sk.Tags {
			if strings.Contains(t, q) {
				score += 20
				break
			}
		}
		if score == 0 && strings.Contains(strings.ToLower(sk.Body), q) {
			score = 5
		}
		if score > 0 {
			hits = append(hits, scored{sk, score})
		}
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].sk.Name < hits[j].sk.Name
	})

	out := make([]*Skill, len(hits))
	for i, h := range hits {
		out[i] = h.sk
	}
	return out
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// TagCount is a tag with the number of skills carrying it.
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// Tags returns all tags in use, most common first.
func (s *Store) Tags() []TagCount {
	counts := map[string]int{}
	for _, sk := range s.Skills() {
		for _, t := range sk.Tags {
			counts[t]++
		}
	}
	out := make([]TagCount, 0, len(counts))
	for t, c := range counts {
		out = append(out, TagCount{t, c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Tag < out[j].Tag
	})
	return out
}

// ---------- skill mutations ----------

// SaveSkill writes a skill's SKILL.md. Files bundled alongside it are left
// untouched.
func (s *Store) SaveSkill(sk *Skill) error {
	return s.saveSkillAt(sk, time.Time{})
}

// saveSkillAt is SaveSkill with an explicit timestamp, so an import can keep
// the time the content actually last changed instead of claiming it is new.
func (s *Store) saveSkillAt(sk *Skill, at time.Time) error {
	if err := ValidateName(sk.Name); err != nil {
		return err
	}
	if strings.TrimSpace(sk.Description) == "" {
		return fmt.Errorf("description is required")
	}

	source := string(sk.Document())

	err := s.upsert(colSkills, "name", sk.Name, func(rec *core.Record) {
		rec.Set("source", source)
		rec.Set("description", strings.TrimSpace(sk.Description))
		rec.Set("tags", sk.Tags)
		rec.Set("size", len(source))
		setUpdated(rec, "updated", at)
	})
	if err != nil {
		return err
	}
	return s.refreshSkill(sk.Name)
}

// CreateSkill saves a new skill, failing if the name is taken.
func (s *Store) CreateSkill(sk *Skill) error {
	if err := ValidateName(sk.Name); err != nil {
		return err
	}
	if _, err := s.findByField(colSkills, "name", sk.Name); err == nil {
		return fmt.Errorf("skill %q: %w", sk.Name, ErrExists)
	}
	return s.SaveSkill(sk)
}

// DeleteSkill removes a skill and drops it from every bundle. Companion files
// go with it: the relation cascades.
func (s *Store) DeleteSkill(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if _, err := s.Skill(name); err != nil {
		return err
	}
	rec, err := s.findByField(colSkills, "name", name)
	if err != nil {
		return err
	}
	if err := s.app.Delete(rec); err != nil {
		return err
	}

	s.mu.Lock()
	delete(s.skills, name)
	affected := make([]*Bundle, 0)
	for _, b := range s.bundles {
		if b.Has(name) {
			kept := b.Skills[:0]
			for _, sn := range b.Skills {
				if sn != name {
					kept = append(kept, sn)
				}
			}
			b.Skills = kept
			affected = append(affected, b)
		}
	}
	s.mu.Unlock()

	for _, b := range affected {
		if err := s.SaveBundle(b); err != nil {
			return fmt.Errorf("update bundle %q: %w", b.Name, err)
		}
	}
	return nil
}

// RenameSkill renames a skill and updates bundle references. Companion files
// follow, because they are attached by record id rather than by name.
func (s *Store) RenameSkill(oldName, newName string) error {
	if err := ValidateName(newName); err != nil {
		return err
	}
	if oldName == newName {
		return nil
	}
	sk, err := s.Skill(oldName)
	if err != nil {
		return err
	}
	if _, err := s.findByField(colSkills, "name", newName); err == nil {
		return fmt.Errorf("skill %q: %w", newName, ErrExists)
	}

	rec, err := s.findByField(colSkills, "name", oldName)
	if err != nil {
		return err
	}
	rec.Set("name", newName)
	if err := asStoreError(s.app.Save(rec)); err != nil {
		return err
	}

	// Rewrite the frontmatter name to match. Work on a copy: sk is the pointer
	// held by the index and possibly by callers of Skill(), so renaming it in
	// place would change it under them.
	renamed := *sk
	renamed.Name = newName
	if err := s.SaveSkill(&renamed); err != nil {
		return err
	}

	s.mu.Lock()
	delete(s.skills, oldName)
	var affected []*Bundle
	for _, b := range s.bundles {
		for i, sn := range b.Skills {
			if sn == oldName {
				b.Skills[i] = newName
				affected = append(affected, b)
			}
		}
	}
	s.mu.Unlock()

	for _, b := range affected {
		if err := s.SaveBundle(b); err != nil {
			return err
		}
	}
	return s.refreshSkill(newName)
}

// refreshSkill re-reads one skill from the database into the index.
func (s *Store) refreshSkill(name string) error {
	rec, err := s.findByField(colSkills, "name", name)
	if err != nil {
		return err
	}
	files, err := s.skillFileRefs(rec.Id)
	if err != nil {
		return err
	}
	sk, err := skillFromRecord(rec, files)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.skills[name] = sk
	s.mu.Unlock()
	return nil
}

// skillFileRefs lists the companion files attached to a skill record.
func (s *Store) skillFileRefs(skillID string) ([]FileRef, error) {
	recs, err := s.app.FindRecordsByFilter(colSkillFile, "skill = {:id}", "path", 0, 0,
		map[string]any{"id": skillID})
	if err != nil {
		return nil, err
	}
	out := make([]FileRef, 0, len(recs))
	for _, r := range recs {
		out = append(out, FileRef{Path: r.GetString("path"), Size: int64(r.GetInt("size"))})
	}
	return out, nil
}

// ---------- bundled files ----------

// cleanRelPath validates a companion file path and returns it in slash form.
//
// This used to also resolve symlinks, because the path became a real location
// on disk and a symlinked directory could redirect a write anywhere on the
// host. Content lives in the database now, so a path is just a key and the
// whole class of escape is gone. The syntactic rules stay: they are what keeps
// an archive extracted from this registry from writing outside its own
// directory on someone else's machine.
func cleanRelPath(rel string) (string, error) {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("path must be relative")
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path escapes the skill directory")
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("invalid path segment %q", seg)
		}
		if strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("hidden files are not supported")
		}
	}
	return clean, nil
}

// safeJoin validates a skill name and a relative path together.
func (s *Store) safeJoin(skill, rel string) (string, error) {
	if err := ValidateName(skill); err != nil {
		return "", err
	}
	return cleanRelPath(rel)
}

// fileRecord finds one companion file row.
func (s *Store) fileRecord(skill, rel string) (*core.Record, error) {
	sk, err := s.findByField(colSkills, "name", skill)
	if err != nil {
		return nil, err
	}
	rec, err := s.app.FindFirstRecordByFilter(colSkillFile, "skill = {:id} && path = {:p}",
		map[string]any{"id": sk.Id, "p": rel})
	if err != nil {
		return nil, fmt.Errorf("file %q: %w", rel, ErrNotFound)
	}
	return rec, nil
}

// ReadFile returns the contents of a bundled file.
func (s *Store) ReadFile(skill, rel string) ([]byte, error) {
	if rel == "SKILL.md" {
		// Validate here too: this branch bypasses safeJoin, and the name is
		// used as a lookup key.
		if err := ValidateName(skill); err != nil {
			return nil, err
		}
		rec, err := s.findByField(colSkills, "name", skill)
		if err != nil {
			return nil, err
		}
		return []byte(rec.GetString("source")), nil
	}
	clean, err := s.safeJoin(skill, rel)
	if err != nil {
		return nil, err
	}
	rec, err := s.fileRecord(skill, clean)
	if err != nil {
		return nil, err
	}
	if int64(rec.GetInt("size")) > maxFileSize {
		return nil, fmt.Errorf("file is too large to display")
	}
	return s.readBlob(rec)
}

// WriteFile creates or replaces a bundled file.
func (s *Store) WriteFile(skill, rel string, data []byte) error {
	if int64(len(data)) > maxFileSize {
		return fmt.Errorf("file exceeds the %d MiB limit", maxFileSize>>20)
	}
	if _, err := s.Skill(skill); err != nil {
		return err
	}
	if rel == "SKILL.md" {
		return fmt.Errorf("edit SKILL.md through the skill editor")
	}
	clean, err := s.safeJoin(skill, rel)
	if err != nil {
		return err
	}
	sk, err := s.findByField(colSkills, "name", skill)
	if err != nil {
		return err
	}

	rec, err := s.fileRecord(skill, clean)
	if err != nil {
		c, cerr := s.app.FindCollectionByNameOrId(colSkillFile)
		if cerr != nil {
			return cerr
		}
		rec = core.NewRecord(c)
		rec.Set("skill", sk.Id)
		rec.Set("path", clean)
	}
	if err := setBlob(rec, path.Base(clean), data); err != nil {
		return err
	}
	if err := asStoreError(s.app.Save(rec)); err != nil {
		return err
	}
	return s.refreshSkill(skill)
}

// DeleteFile removes a bundled file.
func (s *Store) DeleteFile(skill, rel string) error {
	if rel == "SKILL.md" {
		return fmt.Errorf("SKILL.md cannot be deleted")
	}
	clean, err := s.safeJoin(skill, rel)
	if err != nil {
		return err
	}
	rec, err := s.fileRecord(skill, clean)
	if err != nil {
		// Deleting something that is not there is not a failure: the caller
		// wanted it gone, and it is gone. This also mirrors what removing a
		// directory used to do.
		if errors.Is(err, ErrNotFound) {
			return s.refreshSkill(skill)
		}
		return err
	}
	if err := s.app.Delete(rec); err != nil {
		return err
	}
	return s.refreshSkill(skill)
}

// ---------- bundles ----------

// Bundles returns all bundles sorted by name.
func (s *Store) Bundles() []*Bundle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Bundle, 0, len(s.bundles))
	for _, b := range s.bundles {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Bundle returns one bundle by name.
func (s *Store) Bundle(name string) (*Bundle, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.bundles[name]
	if !ok {
		return nil, fmt.Errorf("bundle %q: %w", name, ErrNotFound)
	}
	return b, nil
}

// BundleCount returns the number of stored bundles.
func (s *Store) BundleCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.bundles)
}

// SaveBundle writes a folder, dropping duplicates and references to anything
// that no longer exists, so a folder can never promise to install something
// that is not there.
func (s *Store) SaveBundle(b *Bundle) error {
	return s.saveBundleAt(b, time.Now())
}

// saveBundleAt is SaveBundle with an explicit last-changed time, so an import
// can keep the one the manifest already carries rather than claiming the folder
// changed just now.
func (s *Store) saveBundleAt(b *Bundle, at time.Time) error {
	if err := ValidateName(b.Name); err != nil {
		return err
	}
	if at.IsZero() {
		at = time.Now()
	}
	prune := func(names []string, exists func(string) bool) []string {
		kept := make([]string, 0, len(names))
		seen := map[string]bool{}
		for _, name := range names {
			if seen[name] || !exists(name) {
				continue
			}
			seen[name] = true
			kept = append(kept, name)
		}
		return kept
	}
	b.Skills = prune(b.Skills, func(n string) bool { _, err := s.Skill(n); return err == nil })
	b.Rules = prune(b.Rules, func(n string) bool { _, err := s.Rule(n); return err == nil })
	b.Servers = prune(b.Servers, func(n string) bool { _, err := s.MCPServer(n); return err == nil })
	b.UpdatedAt = at

	raw, err := b.Document()
	if err != nil {
		return err
	}

	err = s.upsert(colBundles, "name", b.Name, func(rec *core.Record) {
		rec.Set("source", string(raw))
		rec.Set("title", b.Title)
		rec.Set("description", b.Description)
		setUpdated(rec, "updated", b.UpdatedAt)
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.bundles[b.Name] = b
	s.mu.Unlock()
	return nil
}

// CreateBundle saves a new bundle, failing if the name is taken.
func (s *Store) CreateBundle(b *Bundle) error {
	if err := ValidateName(b.Name); err != nil {
		return err
	}
	if _, err := s.Bundle(b.Name); err == nil {
		return fmt.Errorf("bundle %q: %w", b.Name, ErrExists)
	}
	return s.SaveBundle(b)
}

// DeleteBundle removes a bundle. Skills are untouched.
func (s *Store) DeleteBundle(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if _, err := s.Bundle(name); err != nil {
		return err
	}
	if rec, err := s.findByField(colBundles, "name", name); err == nil {
		if err := s.app.Delete(rec); err != nil {
			return err
		}
	}
	// Context documents are keyed by folder name and would otherwise be orphaned.
	if err := s.deleteBundleContexts(name); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.bundles, name)
	s.mu.Unlock()
	return nil
}

// BundleSkills resolves a bundle's skill names to skills, skipping missing ones.
func (s *Store) BundleSkills(b *Bundle) []*Skill {
	out := make([]*Skill, 0, len(b.Skills))
	for _, name := range b.Skills {
		if sk, err := s.Skill(name); err == nil {
			out = append(out, sk)
		}
	}
	return out
}

// BundlesContaining returns bundles that include the named skill.
func (s *Store) BundlesContaining(skill string) []*Bundle {
	var out []*Bundle
	for _, b := range s.Bundles() {
		if b.Has(skill) {
			out = append(out, b)
		}
	}
	return out
}
