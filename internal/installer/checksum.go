package installer

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ChecksumPrefix labels the digest algorithm so the lockfile stays readable and
// a future change of algorithm is detectable rather than silently mismatching.
const ChecksumPrefix = "sha256:"

// ignoredDirs and ignoredFiles mirror the exclusions the server applies when it
// packs a skill. Hashing anything the server never sends would make `pull` and
// `list` report drift forever on a machine that sprinkles .DS_Store files.
var (
	ignoredDirs = map[string]bool{
		"__pycache__": true, "node_modules": true, ".venv": true,
		"venv": true, ".git": true,
	}
	ignoredFiles = map[string]bool{".DS_Store": true, "Thumbs.db": true}
	ignoredExts  = map[string]bool{".pyc": true, ".pyo": true}
)

// Checksum computes a deterministic digest of a skill directory's contents.
//
// Files are hashed in sorted path order, and each contributes its relative path
// plus an explicit length before its bytes. The length prefix is what makes the
// digest unambiguous: without it, moving a byte from the end of one file to the
// start of the next would produce the same hash.
//
// Timestamps, ownership and permissions are deliberately excluded, because they differ
// between a fresh download and an installed copy without the content differing.
func Checksum(dir string) (string, error) {
	paths, err := skillFiles(dir)
	if err != nil {
		return "", err
	}

	h := sha256.New()
	var size [8]byte
	for _, rel := range paths {
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", fmt.Errorf("read %s: %w", rel, err)
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return "", fmt.Errorf("stat %s: %w", rel, err)
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		binary.BigEndian.PutUint64(size[:], uint64(info.Size()))
		h.Write(size[:])
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil {
			return "", fmt.Errorf("read %s: %w", rel, err)
		}
		if n != info.Size() {
			return "", fmt.Errorf("read %s: file changed while hashing", rel)
		}
	}
	return ChecksumPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

// skillFiles lists the hashable files in a skill directory as sorted,
// slash-separated relative paths.
func skillFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if ignoredDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		// Symlinks and devices are not part of a skill's content; a skill
		// directory containing one hashes as if it were absent, which matches
		// what the registry would have sent.
		if !d.Type().IsRegular() {
			return nil
		}
		if ignoredFiles[name] || ignoredExts[strings.ToLower(filepath.Ext(name))] {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}
	sort.Strings(out)
	return out, nil
}
