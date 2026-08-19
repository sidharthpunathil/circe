package store

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/pocketbase/pocketbase/tools/types"

	_ "github.com/pocketbase/pocketbase/migrations"
)

// The registry is a PocketBase database. What that changes, and what it
// deliberately does not:
//
// Every row keeps the *canonical bytes* that used to be a file: the whole
// SKILL.md for a skill, the whole markdown file for a rule, the whole JSON
// document for a server or a folder. Those bytes stay the source of truth, and
// everything else on the row is derived from them on save. So serialization is
// unchanged, round-trips stay lossless, and rendering the registry back out to
// a plain directory tree is a copy rather than a re-encoding. See export.go:
// that tree is what the git sync commits, and what `circe-server export`
// writes, so the promise that your content stays yours survives the move.
//
// The derived columns (description, tags, transport, ...) exist so the
// PocketBase dashboard and its REST API are actually useful for browsing and
// filtering. Nothing reads them back as authority.

// Collection names. These are the table names in the data directory's data.db.
const (
	colSkills    = "skills"
	colSkillFile = "skill_files"
	colRules     = "rules"
	colMCPs      = "mcps"
	colBundles   = "bundles"
	colContexts  = "bundle_contexts"
	colShares    = "shares"
	colSources   = "sources"
	colCreds     = "credentials"
	colKV        = "kv"
)

// maxSource caps a stored document. Skills are prose, so this is generous
// rather than tight, and it matches maxFileSize for companion content.
const maxSource = 4 << 20

// openApp boots a headless PocketBase application rooted at dir and brings the
// schema up to date. The server binary runs the full PocketBase app (dashboard,
// REST API, hooks) around this same database; this path is what tests and the
// store itself use, and it deliberately pulls in no HTTP surface.
func openApp(dir string) (core.App, error) {
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: dir})
	if err := app.Bootstrap(); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	if err := app.RunAllMigrations(); err != nil {
		app.ResetBootstrapState()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := ensureSchema(app); err != nil {
		app.ResetBootstrapState()
		return nil, err
	}
	return app, nil
}

// ensureSchema creates any collection that does not exist yet. It is additive
// and idempotent, so it runs on every open and an older database simply gains
// whatever is new.
func ensureSchema(app core.App) error {
	skills, err := ensureCollection(app, colSkills, func(c *core.Collection) {
		c.Fields.Add(
			&core.TextField{Name: "name", Required: true, Max: 64, Presentable: true},
			&core.TextField{Name: "source", Max: maxSource},
			&core.TextField{Name: "description", Max: 5000},
			&core.JSONField{Name: "tags", MaxSize: 1 << 16},
			&core.NumberField{Name: "size"},
			&core.DateField{Name: "updated"},
		)
		c.AddIndex("idx_skills_name", true, "name", "")
	})
	if err != nil {
		return err
	}

	if _, err := ensureCollection(app, colSkillFile, func(c *core.Collection) {
		c.Fields.Add(
			&core.RelationField{
				Name: "skill", Required: true, CollectionId: skills.Id,
				MaxSelect: 1, CascadeDelete: true,
			},
			&core.TextField{Name: "path", Required: true, Max: 1024, Presentable: true},
			// Companion files are whatever a skill ships: markdown, scripts,
			// images, fonts. They go to blob storage rather than into a column
			// so a binary asset does not bloat the database, and so the
			// dashboard can preview them.
			&core.FileField{Name: "blob", MaxSize: maxFileSize, MaxSelect: 1},
			&core.NumberField{Name: "size"},
		)
		c.AddIndex("idx_skill_files_path", true, "skill, path", "")
	}); err != nil {
		return err
	}

	if _, err := ensureCollection(app, colRules, func(c *core.Collection) {
		c.Fields.Add(
			&core.TextField{Name: "name", Required: true, Max: 64, Presentable: true},
			&core.TextField{Name: "source", Max: maxSource},
			&core.TextField{Name: "description", Max: 5000},
			&core.JSONField{Name: "targets", MaxSize: 1 << 12},
			&core.JSONField{Name: "tags", MaxSize: 1 << 16},
			&core.NumberField{Name: "size"},
			&core.DateField{Name: "updated"},
		)
		c.AddIndex("idx_rules_name", true, "name", "")
	}); err != nil {
		return err
	}

	if _, err := ensureCollection(app, colMCPs, func(c *core.Collection) {
		c.Fields.Add(
			&core.TextField{Name: "name", Required: true, Max: 64, Presentable: true},
			&core.TextField{Name: "source", Max: maxSource},
			&core.TextField{Name: "description", Max: 5000},
			&core.TextField{Name: "category", Max: 64},
			&core.TextField{Name: "transport", Max: 32},
			&core.NumberField{Name: "size"},
			&core.DateField{Name: "updated"},
		)
		c.AddIndex("idx_mcps_name", true, "name", "")
	}); err != nil {
		return err
	}

	if _, err := ensureCollection(app, colBundles, func(c *core.Collection) {
		c.Fields.Add(
			&core.TextField{Name: "name", Required: true, Max: 64, Presentable: true},
			&core.TextField{Name: "source", Max: maxSource},
			&core.TextField{Name: "title", Max: 200},
			&core.TextField{Name: "description", Max: 5000},
			&core.DateField{Name: "updated"},
		)
		c.AddIndex("idx_bundles_name", true, "name", "")
	}); err != nil {
		return err
	}

	if _, err := ensureCollection(app, colContexts, func(c *core.Collection) {
		c.Fields.Add(
			&core.TextField{Name: "bundle", Required: true, Max: 64, Presentable: true},
			&core.TextField{Name: "file", Required: true, Max: 64},
			&core.TextField{Name: "content", Max: maxSource},
		)
		c.AddIndex("idx_contexts", true, "bundle, file", "")
	}); err != nil {
		return err
	}

	if _, err := ensureCollection(app, colShares, func(c *core.Collection) {
		c.Fields.Add(
			&core.TextField{Name: "shareId", Required: true, Max: 32, Presentable: true},
			&core.TextField{Name: "kind", Required: true, Max: 16},
			&core.TextField{Name: "target", Required: true, Max: 64},
			&core.DateField{Name: "created"},
		)
		c.AddIndex("idx_shares_id", true, "shareId", "")
		c.AddIndex("idx_shares_target", true, "kind, target", "")
	}); err != nil {
		return err
	}

	if _, err := ensureCollection(app, colSources, func(c *core.Collection) {
		c.Fields.Add(
			&core.TextField{Name: "name", Required: true, Max: 64, Presentable: true},
			&core.JSONField{Name: "value", MaxSize: 1 << 18},
		)
		c.AddIndex("idx_sources_name", true, "name", "")
	}); err != nil {
		return err
	}

	// Tokens are secrets. They live in their own collection so that export and
	// git sync can skip them wholesale rather than having to remember to filter
	// a field, which is the kind of thing that leaks a token exactly once.
	if _, err := ensureCollection(app, colCreds, func(c *core.Collection) {
		c.Fields.Add(
			&core.TextField{Name: "name", Required: true, Max: 64, Presentable: true},
			&core.TextField{Name: "value", Max: 4096, Hidden: true},
		)
		c.AddIndex("idx_creds_name", true, "name", "")
	}); err != nil {
		return err
	}

	// Small operational state that is not content: git sync bookkeeping and
	// anything else that wants a single row.
	if _, err := ensureCollection(app, colKV, func(c *core.Collection) {
		c.Fields.Add(
			&core.TextField{Name: "key", Required: true, Max: 64, Presentable: true},
			&core.JSONField{Name: "value", MaxSize: 1 << 18},
		)
		c.AddIndex("idx_kv_key", true, "key", "")
	}); err != nil {
		return err
	}

	return nil
}

// ensureCollection returns the named collection, creating it with build when it
// is missing.
func ensureCollection(app core.App, name string, build func(*core.Collection)) (*core.Collection, error) {
	if c, err := app.FindCollectionByNameOrId(name); err == nil {
		return c, nil
	}
	c := core.NewBaseCollection(name)
	build(c)
	if err := app.Save(c); err != nil {
		return nil, fmt.Errorf("create collection %q: %w", name, err)
	}
	return c, nil
}

// ---------- record helpers ----------

// findByField returns the single record whose field equals value, or
// ErrNotFound. Every lookup in this store is by a validated slug, so this is
// the only read shape needed.
func (s *Store) findByField(collection, field, value string) (*core.Record, error) {
	r, err := s.app.FindFirstRecordByFilter(collection, field+" = {:v}", map[string]any{"v": value})
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", strings.TrimSuffix(collection, "s"), value, ErrNotFound)
	}
	return r, nil
}

// all returns every record in a collection, unsorted. Callers sort, because the
// in-memory index they build is what serves listings.
func (s *Store) all(collection string) ([]*core.Record, error) {
	return s.app.FindAllRecords(collection)
}

// upsert finds the record with field == value or makes a new one, applies set,
// and saves. Unique-index violations become ErrExists so handlers can tell a
// name clash from a real failure.
func (s *Store) upsert(collection, field, value string, set func(*core.Record)) error {
	rec, err := s.findByField(collection, field, value)
	if err != nil {
		c, cerr := s.app.FindCollectionByNameOrId(collection)
		if cerr != nil {
			return cerr
		}
		rec = core.NewRecord(c)
		rec.Set(field, value)
	}
	set(rec)
	return asStoreError(s.app.Save(rec))
}

// asStoreError maps PocketBase's validation errors onto the store's own, so
// callers keep matching on ErrExists rather than on a string.
func asStoreError(err error) error {
	if err == nil {
		return nil
	}
	var verrs validation.Errors
	if errors.As(err, &verrs) {
		for _, e := range verrs {
			if strings.Contains(strings.ToLower(e.Error()), "unique") {
				return fmt.Errorf("%w", ErrExists)
			}
		}
	}
	return err
}

// setUpdated stamps a record's timestamp, defaulting to now. Import paths pass
// the original time so a restored backup does not claim to be new.
func setUpdated(rec *core.Record, field string, t time.Time) {
	if t.IsZero() {
		t = time.Now()
	}
	dt, err := types.ParseDateTime(t.UTC())
	if err != nil {
		dt = types.NowDateTime()
	}
	rec.Set(field, dt)
}

// ---------- blob helpers ----------

// readBlob returns the bytes of a skill_files record. A row with no blob is an
// empty file: PocketBase refuses to store a zero-byte upload, and an empty
// companion file is legitimate content that must survive a round-trip.
func (s *Store) readBlob(rec *core.Record) ([]byte, error) {
	key := rec.GetString("blob")
	if key == "" {
		return []byte{}, nil
	}
	fsys, err := s.app.NewFilesystem()
	if err != nil {
		return nil, err
	}
	defer fsys.Close()

	r, err := fsys.GetReader(rec.BaseFilesPath() + "/" + key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// setBlob attaches data to a skill_files record, handling the empty case.
func setBlob(rec *core.Record, name string, data []byte) error {
	rec.Set("size", len(data))
	if len(data) == 0 {
		rec.Set("blob", nil)
		return nil
	}
	f, err := filesystem.NewFileFromBytes(data, name)
	if err != nil {
		return err
	}
	rec.Set("blob", f)
	return nil
}
