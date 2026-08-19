package store

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// Shares are minted and revoked here; what a share code *is*, and how it
// encodes, lives in the model alongside everything else that crosses the wire.

// newShareID returns an unguessable, URL-safe id. Ten base32 characters is
// 50 bits, far beyond brute-forcing a personal registry, and still short
// enough to paste.
func newShareID() (string, error) {
	buf := make([]byte, 7)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	return strings.ToLower(enc.EncodeToString(buf))[:10], nil
}

// shareFromRecord reads one row.
func shareFromRecord(rec *core.Record) Share {
	return Share{
		ID:        rec.GetString("shareId"),
		Kind:      rec.GetString("kind"),
		Name:      rec.GetString("target"),
		CreatedAt: rec.GetDateTime("created").Time(),
	}
}

// Shares returns every active share, newest first.
func (s *Store) Shares() []Share {
	recs, err := s.all(colShares)
	if err != nil {
		return nil
	}
	out := make([]Share, 0, len(recs))
	for _, rec := range recs {
		out = append(out, shareFromRecord(rec))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// ShareFor returns the existing share for a resource, if any.
func (s *Store) ShareFor(kind, name string) (Share, bool) {
	rec, err := s.app.FindFirstRecordByFilter(colShares, "kind = {:k} && target = {:t}",
		map[string]any{"k": kind, "t": name})
	if err != nil {
		return Share{}, false
	}
	return shareFromRecord(rec), true
}

// ShareByID looks up a share. This is the only lookup the public endpoint uses,
// so an unshared resource is simply not reachable.
func (s *Store) ShareByID(id string) (Share, bool) {
	rec, err := s.findByField(colShares, "shareId", id)
	if err != nil {
		return Share{}, false
	}
	return shareFromRecord(rec), true
}

// CreateShare mints a code for a resource, or returns the existing one so that
// sharing twice does not invalidate a link already sent to someone.
func (s *Store) CreateShare(kind, name string) (Share, error) {
	switch kind {
	case ShareKindFolder:
		if _, err := s.Bundle(name); err != nil {
			return Share{}, err
		}
	case ShareKindSkill:
		if _, err := s.Skill(name); err != nil {
			return Share{}, err
		}
	default:
		return Share{}, fmt.Errorf("unknown share kind %q", kind)
	}

	if existing, ok := s.ShareFor(kind, name); ok {
		return existing, nil
	}

	id, err := newShareID()
	if err != nil {
		return Share{}, err
	}
	sh := Share{ID: id, Kind: kind, Name: name, CreatedAt: time.Now()}

	c, err := s.app.FindCollectionByNameOrId(colShares)
	if err != nil {
		return Share{}, err
	}
	rec := core.NewRecord(c)
	rec.Set("shareId", sh.ID)
	rec.Set("kind", sh.Kind)
	rec.Set("target", sh.Name)
	setUpdated(rec, "created", sh.CreatedAt)
	if err := asStoreError(s.app.Save(rec)); err != nil {
		return Share{}, err
	}
	return sh, nil
}

// RevokeShare removes a share, making the code stop working immediately.
func (s *Store) RevokeShare(kind, name string) error {
	recs, err := s.app.FindRecordsByFilter(colShares, "kind = {:k} && target = {:t}", "", 0, 0,
		map[string]any{"k": kind, "t": name})
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
