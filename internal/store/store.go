// Package store persists skills and bundles on the filesystem. The directory
// layout is deliberately plain — data/skills/<name>/SKILL.md alongside any
// bundled files, and data/bundles/<name>.json — so the data directory is itself
// a usable backup that can be committed to git or copied into ~/.claude/skills.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sidharthpunathil/skillbuilder/internal/skillfile"
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

// Store is a filesystem-backed collection of skills and bundles, with an
// in-memory index for listing and search.
type Store struct {
	root string

	mu      sync.RWMutex
	skills  map[string]*Skill
	bundles map[string]*Bundle
}

// Open loads (creating if needed) a store rooted at dir.
func Open(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{root: abs}
	for _, sub := range []string{"skills", "bundles"} {
		if err := os.MkdirAll(filepath.Join(abs, sub), 0o755); err != nil {
			return nil, err
		}
	}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Root returns the absolute data directory.
func (s *Store) Root() string { return s.root }

func (s *Store) skillsDir() string        { return filepath.Join(s.root, "skills") }
func (s *Store) bundlesDir() string       { return filepath.Join(s.root, "bundles") }
func (s *Store) skillDir(n string) string { return filepath.Join(s.skillsDir(), n) }

// Reload rebuilds the in-memory index from disk. Safe to call at any time; it
// lets the user edit files directly on disk and refresh the site.
func (s *Store) Reload() error {
	skills := map[string]*Skill{}
	entries, err := os.ReadDir(s.skillsDir())
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || ValidateName(name) != nil {
			continue
		}
		// Resolve rather than trusting DirEntry.IsDir so symlinked skills load.
		info, err := os.Stat(filepath.Join(s.skillsDir(), name))
		if err != nil || !info.IsDir() {
			continue
		}
		sk, err := s.loadSkill(name)
		if err != nil {
			// A malformed skill must not take down the whole index.
			fmt.Fprintf(os.Stderr, "skillbuilder: skipping skill %q: %v\n", name, err)
			continue
		}
		skills[name] = sk
	}

	bundles := map[string]*Bundle{}
	bEntries, err := os.ReadDir(s.bundlesDir())
	if err != nil {
		return err
	}
	for _, e := range bEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if ValidateName(name) != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.bundlesDir(), e.Name()))
		if err != nil {
			continue
		}
		var b Bundle
		if err := json.Unmarshal(raw, &b); err != nil {
			fmt.Fprintf(os.Stderr, "skillbuilder: skipping bundle %q: %v\n", name, err)
			continue
		}
		b.Name = name
		if b.Skills == nil {
			b.Skills = []string{}
		}
		bundles[name] = &b
	}

	s.mu.Lock()
	s.skills, s.bundles = skills, bundles
	s.mu.Unlock()
	return nil
}

// loadSkill reads one skill directory from disk.
func (s *Store) loadSkill(name string) (*Skill, error) {
	dir := s.skillDir(name)
	raw, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return nil, err
	}
	f, err := skillfile.Parse(raw)
	if err != nil {
		return nil, err
	}

	sk := &Skill{
		Name:        name,
		Description: strings.TrimSpace(f.Description),
		Body:        f.Body,
		Front:       f.Frontmatter,
		Tags:        extractTags(f.Frontmatter),
		Files:       []FileRef{},
	}
	if sk.Description == "" {
		sk.Description = f.Summary(180)
	}

	var newest time.Time
	var total int64
	err = walkResolved(dir, func(e WalkEntry) error {
		if e.Info.ModTime().After(newest) {
			newest = e.Info.ModTime()
		}
		total += e.Info.Size()
		if e.Rel != "SKILL.md" {
			sk.Files = append(sk.Files, FileRef{Path: e.Rel, Size: e.Info.Size()})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(sk.Files, func(i, j int) bool { return sk.Files[i].Path < sk.Files[j].Path })
	sk.UpdatedAt, sk.Size = newest, total
	return sk, nil
}

// extractTags pulls tags from either metadata.tags or a top-level tags key,
// since both conventions appear in real skill libraries.
func extractTags(f skillfile.Frontmatter) []string {
	var out []string
	add := func(v any) {
		switch t := v.(type) {
		case []any:
			for _, item := range t {
				if str, ok := item.(string); ok {
					if slug := Slugify(str); slug != "" {
						out = append(out, slug)
					}
				}
			}
		case []string:
			for _, str := range t {
				if slug := Slugify(str); slug != "" {
					out = append(out, slug)
				}
			}
		case string:
			out = append(out, ParseTags(t)...)
		}
	}
	if f.Metadata != nil {
		add(f.Metadata["tags"])
	}
	if f.Extra != nil {
		add(f.Extra["tags"])
	}

	seen := map[string]bool{}
	var dedup []string
	for _, t := range out {
		if !seen[t] {
			seen[t] = true
			dedup = append(dedup, t)
		}
	}
	sort.Strings(dedup)
	return dedup
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

// SaveSkill writes a skill's SKILL.md, creating the directory if needed. Files
// bundled alongside it are left untouched.
func (s *Store) SaveSkill(sk *Skill) error {
	if err := ValidateName(sk.Name); err != nil {
		return err
	}
	if strings.TrimSpace(sk.Description) == "" {
		return fmt.Errorf("description is required")
	}

	// Copying the struct still shares its maps, so clone them before editing:
	// otherwise saving a skill mutates the caller's Metadata and Extra, which
	// also races with readers holding a pointer from the index.
	front := sk.Front
	front.Metadata = cloneMap(sk.Front.Metadata)
	front.Extra = cloneMap(sk.Front.Extra)
	front.Name = sk.Name
	front.Description = strings.TrimSpace(sk.Description)
	if len(sk.Tags) > 0 {
		if front.Metadata == nil {
			front.Metadata = map[string]any{}
		}
		front.Metadata["tags"] = append([]string(nil), sk.Tags...)
	} else if front.Metadata != nil {
		delete(front.Metadata, "tags")
		if len(front.Metadata) == 0 {
			front.Metadata = nil
		}
	}
	// Tags live in metadata; drop any stale top-level copy so they cannot diverge.
	if front.Extra != nil {
		delete(front.Extra, "tags")
		if len(front.Extra) == 0 {
			front.Extra = nil
		}
	}

	f := &skillfile.File{Frontmatter: front, Body: sk.Body}

	dir := s.skillDir(sk.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, "SKILL.md"), f.Bytes()); err != nil {
		return err
	}
	return s.refreshSkill(sk.Name)
}

// CreateSkill saves a new skill, failing if the name is taken.
func (s *Store) CreateSkill(sk *Skill) error {
	if err := ValidateName(sk.Name); err != nil {
		return err
	}
	if _, err := os.Stat(s.skillDir(sk.Name)); err == nil {
		return fmt.Errorf("skill %q: %w", sk.Name, ErrExists)
	}
	return s.SaveSkill(sk)
}

// DeleteSkill removes a skill directory and drops it from every bundle.
func (s *Store) DeleteSkill(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if _, err := s.Skill(name); err != nil {
		return err
	}
	if err := os.RemoveAll(s.skillDir(name)); err != nil {
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

// RenameSkill moves a skill directory and updates bundle references.
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
	if _, err := os.Stat(s.skillDir(newName)); err == nil {
		return fmt.Errorf("skill %q: %w", newName, ErrExists)
	}
	if err := os.Rename(s.skillDir(oldName), s.skillDir(newName)); err != nil {
		return err
	}

	// Rewrite the frontmatter name to match the new directory. Work on a copy:
	// sk is the pointer held by the index and possibly by callers of Skill(),
	// so renaming it in place would change it under them.
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

func (s *Store) refreshSkill(name string) error {
	sk, err := s.loadSkill(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.skills[name] = sk
	s.mu.Unlock()
	return nil
}

// ---------- bundled files ----------

// safeJoin resolves rel inside the skill directory, refusing anything that
// escapes it via traversal, absolute paths or symlinks.
func (s *Store) safeJoin(skill, rel string) (string, error) {
	if err := ValidateName(skill); err != nil {
		return "", err
	}
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

	dir := s.skillDir(skill)
	full := filepath.Join(dir, filepath.FromSlash(clean))

	base, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("skill %q: %w", skill, ErrNotFound)
	}

	// Guard against symlinks pointing outside the skill directory. The target
	// itself often does not exist yet (a file being created), and EvalSymlinks
	// fails on missing paths — so resolve the nearest ancestor that does exist.
	// Checking only `full` would skip this guard entirely for new files and let
	// a symlinked directory redirect a write anywhere on the filesystem.
	probe := full
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe || len(parent) < len(dir) {
			return "", fmt.Errorf("path escapes the skill directory")
		}
		probe = parent
	}
	resolved, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", clean, err)
	}
	if rp, err := filepath.Rel(base, resolved); err != nil || rp == ".." || strings.HasPrefix(rp, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the skill directory")
	}
	return full, nil
}

// ReadFile returns the contents of a bundled file.
func (s *Store) ReadFile(skill, rel string) ([]byte, error) {
	if rel == "SKILL.md" {
		// Validate here too: this branch bypasses safeJoin, and without the
		// check a crafted skill name would read any SKILL.md on the host.
		if err := ValidateName(skill); err != nil {
			return nil, err
		}
		return os.ReadFile(filepath.Join(s.skillDir(skill), "SKILL.md"))
	}
	full, err := s.safeJoin(skill, rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("file %q: %w", rel, ErrNotFound)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%q is a directory", rel)
	}
	if info.Size() > maxFileSize {
		return nil, fmt.Errorf("file is too large to display")
	}
	return os.ReadFile(full)
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
	full, err := s.safeJoin(skill, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(full, data); err != nil {
		return err
	}
	return s.refreshSkill(skill)
}

// DeleteFile removes a bundled file, pruning directories left empty.
func (s *Store) DeleteFile(skill, rel string) error {
	if rel == "SKILL.md" {
		return fmt.Errorf("SKILL.md cannot be deleted")
	}
	full, err := s.safeJoin(skill, rel)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(full); err != nil {
		return err
	}
	// Remove now-empty parent directories, stopping at the skill root.
	root := s.skillDir(skill)
	for dir := filepath.Dir(full); strings.HasPrefix(dir, root) && dir != root; dir = filepath.Dir(dir) {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(dir); err != nil {
			break
		}
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

// SaveBundle writes a bundle, dropping references to skills that no longer exist.
func (s *Store) SaveBundle(b *Bundle) error {
	if err := ValidateName(b.Name); err != nil {
		return err
	}
	kept := make([]string, 0, len(b.Skills))
	seen := map[string]bool{}
	for _, name := range b.Skills {
		if seen[name] {
			continue
		}
		if _, err := s.Skill(name); err != nil {
			continue
		}
		seen[name] = true
		kept = append(kept, name)
	}
	b.Skills = kept
	b.UpdatedAt = time.Now()

	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := writeFileAtomic(filepath.Join(s.bundlesDir(), b.Name+".json"), raw); err != nil {
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
	if err := os.Remove(filepath.Join(s.bundlesDir(), name+".json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Context files live in a sibling directory and would otherwise be orphaned.
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

// cloneMap returns a shallow copy so callers' maps are never mutated in place.
func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// writeFileAtomic writes via a temp file and rename so a crash mid-write cannot
// truncate an existing skill.
func writeFileAtomic(dst string, data []byte) error {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}
