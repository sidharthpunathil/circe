package installer

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Guards against decompression bombs. A few kilobytes of gzip can expand into
// gigabytes, and the CLI runs unattended in CI, so cap both dimensions.
const (
	maxArchiveBytes = 512 << 20 // 512 MiB of decompressed content
	maxArchiveFiles = 20000
)

// UnsafeArchiveError reports a tar entry that was refused before anything was
// written for it.
type UnsafeArchiveError struct {
	Entry  string
	Reason string
}

func (e *UnsafeArchiveError) Error() string {
	return fmt.Sprintf("refusing unsafe archive entry %q: %s", e.Entry, e.Reason)
}

// ExtractTarGz extracts a gzipped tar of skills into dest and returns the
// top-level directory names it wrote, sorted.
//
// Entries are validated, never sanitized: an archive containing an absolute
// path, a ".." segment or a link is either hostile or broken, and extracting
// the safe half of it is worse than refusing the whole thing. Extraction
// therefore aborts on the first bad entry.
func ExtractTarGz(r io.Reader, dest string) ([]string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	defer gz.Close()

	root, err := filepath.Abs(dest)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", dest, err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", root, err)
	}

	tr := tar.NewReader(gz)
	tops := map[string]bool{}
	var written int64
	var files int

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}

		// PAX metadata carries no content of its own.
		if hdr.Typeflag == tar.TypeXHeader || hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}

		rel, err := entryPath(hdr.Name)
		if err != nil {
			return nil, err
		}
		if rel == "" {
			continue // the archive root itself
		}

		target := filepath.Join(root, filepath.FromSlash(rel))
		// Belt and braces: even though entryPath rejected traversal, verify the
		// joined path really lands under root before touching the filesystem.
		if !within(root, target) {
			return nil, &UnsafeArchiveError{hdr.Name, "path escapes the destination directory"}
		}

		switch hdr.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			return nil, &UnsafeArchiveError{hdr.Name, "symlinks and hard links are not allowed in skill archives"}

		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, fmt.Errorf("create %s: %w", target, err)
			}
			tops[topLevel(rel)] = true

		case tar.TypeReg:
			if !strings.Contains(rel, "/") {
				return nil, &UnsafeArchiveError{hdr.Name, "file is not inside a skill directory"}
			}
			if files++; files > maxArchiveFiles {
				return nil, fmt.Errorf("archive contains more than %d files", maxArchiveFiles)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, fmt.Errorf("create %s: %w", filepath.Dir(target), err)
			}
			// The declared size is untrusted, so cap by what is left of the
			// budget and check the bytes actually copied.
			budget := maxArchiveBytes - written
			n, err := writeFileAtomic(target, entryMode(hdr), io.LimitReader(tr, budget+1))
			if err != nil {
				return nil, err
			}
			if written += n; written > maxArchiveBytes {
				return nil, fmt.Errorf("archive expands to more than %d bytes", int64(maxArchiveBytes))
			}
			tops[topLevel(rel)] = true

		default:
			return nil, &UnsafeArchiveError{hdr.Name, fmt.Sprintf("unsupported entry type %q", hdr.Typeflag)}
		}
	}

	names := make([]string, 0, len(tops))
	for n := range tops {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// entryPath validates a tar entry name and returns it as a clean, relative,
// slash-separated path. An empty name with a nil error means the entry refers to
// the archive root and can be skipped.
func entryPath(name string) (string, error) {
	unsafe := func(reason string) (string, error) {
		return "", &UnsafeArchiveError{name, reason}
	}

	n := strings.TrimSpace(name)
	switch {
	case n == "":
		return unsafe("empty entry name")
	case strings.ContainsRune(n, 0):
		return unsafe("entry name contains a NUL byte")
	case strings.ContainsRune(n, '\\'):
		// A backslash separates paths on Windows, so an entry containing one
		// could escape the destination there even though it looks inert here.
		return unsafe("entry name contains a backslash")
	case strings.HasPrefix(n, "/"), filepath.IsAbs(n), hasDriveLetter(n):
		return unsafe("absolute paths are not allowed")
	}

	n = strings.TrimSuffix(n, "/")
	// GNU tar prefixes entries with "./"; tolerate that one form and reject
	// every other dot segment.
	n = strings.TrimPrefix(n, "./")
	if n == "" || n == "." {
		return "", nil
	}

	for _, seg := range strings.Split(n, "/") {
		switch seg {
		case "":
			return unsafe("entry name contains an empty path segment")
		case ".", "..":
			return unsafe(`entry name contains a "." or ".." segment`)
		}
	}
	if path.Clean(n) != n {
		return unsafe("entry name is not a clean relative path")
	}
	return n, nil
}

// hasDriveLetter reports whether s starts with a Windows drive prefix such as
// "C:". filepath.IsAbs does not catch these when the CLI runs on Unix.
func hasDriveLetter(s string) bool {
	if len(s) < 2 || s[1] != ':' {
		return false
	}
	c := s[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// within reports whether target is root itself or lives inside it. Both paths
// must already be absolute and clean.
func within(root, target string) bool {
	if target == root {
		return true
	}
	return strings.HasPrefix(target, root+string(os.PathSeparator))
}

func topLevel(rel string) string {
	if i := strings.Index(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return rel
}

// entryMode derives a file mode from a tar header. Archive permissions are
// advisory only: the executable bit is honoured and everything else is dropped
// so a hostile archive cannot land a setuid or world-writable file.
func entryMode(hdr *tar.Header) fs.FileMode {
	if hdr.FileInfo().Mode().Perm()&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// writeFileAtomic writes r to dst through a temporary file in the same
// directory followed by a rename, so an interrupted write can never leave a
// truncated file that a later checksum would silently accept.
func writeFileAtomic(dst string, mode fs.FileMode, r io.Reader) (int64, error) {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".skillbuilder-tmp-*")
	if err != nil {
		return 0, fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // a no-op once the rename has succeeded

	n, err := io.Copy(tmp, r)
	if err != nil {
		tmp.Close()
		return n, fmt.Errorf("write %s: %w", dst, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return n, fmt.Errorf("set mode on %s: %w", dst, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return n, fmt.Errorf("flush %s: %w", dst, err)
	}
	if err := tmp.Close(); err != nil {
		return n, fmt.Errorf("close %s: %w", dst, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return n, fmt.Errorf("write %s: %w", dst, err)
	}
	return n, nil
}
