package store

import (
	"encoding/json"

	"github.com/pocketbase/pocketbase/core"
)

// The kv collection holds small operational state that is not content: git sync
// bookkeeping, and the marker saying the pre-database directory tree has
// already been read. It is deliberately separate from everything the export
// writes, because none of it belongs in a backup or a commit.

// setKV stores a JSON-encodable value under key.
func (s *Store) setKV(key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.upsert(colKV, "key", key, func(rec *core.Record) {
		rec.Set("value", string(raw))
	})
}

// getKV decodes the value stored under key into out, reporting whether it was
// present.
func (s *Store) getKV(key string, out any) (bool, error) {
	rec, err := s.findByField(colKV, "key", key)
	if err != nil {
		return false, nil
	}
	raw := rec.GetString("value")
	if raw == "" {
		return false, nil
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return false, err
	}
	return true, nil
}

// kvBool reads a boolean flag.
func (s *Store) kvBool(key string) (bool, error) {
	var v bool
	ok, err := s.getKV(key, &v)
	if err != nil || !ok {
		return false, err
	}
	return v, nil
}

// GetState reads operational state stored under key, reporting whether it was
// present. This is how a feature that needs to remember something across
// restarts (the git sync and its last commit, say) keeps it without inventing
// its own file.
func (s *Store) GetState(key string, out any) (bool, error) {
	return s.getKV("state:"+key, out)
}

// SetState writes operational state under key.
func (s *Store) SetState(key string, v any) error {
	return s.setKV("state:"+key, v)
}
