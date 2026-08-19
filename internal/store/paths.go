package store

import (
	"os"
	"path/filepath"
)

// The registry's content lives in the database, but the data directory is still
// a real place. It is PocketBase's own root, so data.db, auxiliary.db and
// storage/ sit directly in it, alongside anything an older release left behind.
// These helpers name what is left of that older layout.

func absPath(dir string) (string, error) { return filepath.Abs(dir) }

// The pre-database layout, read once on open so an existing deployment upgrades
// without being told to do anything. It is also the layout ExportTree writes,
// which is why it is worth naming rather than spelling out at each use.
func (s *Store) bundlesDir() string { return filepath.Join(s.root, "bundles") }

// writeFileAtomic writes via a temp file and rename so a crash mid-write cannot
// truncate an existing file. Export is the only thing that writes files now,
// and it is often writing over a working copy, so a half-written skill there
// would be somebody's next commit.
func writeFileAtomic(dst string, data []byte) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
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
