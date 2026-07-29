package gitsource

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"path/filepath"
	"strings"
)

// limits caps what a single repository may cost us. Everything is held in
// memory, and a hostile (or merely enormous) repository must not be able to
// exhaust it — a few hundred kilobytes of gzip can decompress into gigabytes.
type limits struct {
	file    int64 // largest single file kept
	total   int64 // total decompressed bytes, counted as we go
	entries int   // files kept
}

// defaultLimits is the policy for a real fetch; readTarball takes the limits as
// an argument only so that tests can exercise the caps without building a
// hundred-megabyte fixture.
var defaultLimits = limits{
	file:    4 << 20, // matches the store's own per-file limit
	total:   100 << 20,
	entries: 20000,
}

// Exclusion rules mirroring Anthropic's package_skill.py plus .venv, kept in
// step with internal/store/archive.go. They are duplicated rather than shared
// because this package deliberately does not import the store.
var (
	excludeDirsAnyDepth = map[string]bool{
		"node_modules": true, "__pycache__": true, "venv": true,
		".venv": true, ".git": true,
	}
	excludeFiles    = map[string]bool{".DS_Store": true, "Thumbs.db": true}
	excludeExts     = map[string]bool{".pyc": true, ".pyo": true}
	excludeRootDirs = map[string]bool{"evals": true} // relative to a skill, not the repo
)

// UnsafeArchiveError reports a tar entry that was refused outright.
type UnsafeArchiveError struct {
	Entry  string
	Reason string
}

func (e *UnsafeArchiveError) Error() string {
	return fmt.Sprintf("refusing unsafe archive entry %q: %s", e.Entry, e.Reason)
}

// archive is the retained content of a repository tarball.
type archive struct {
	Root  string            // the single generated top-level directory
	Files map[string][]byte // keyed by path relative to the repository root
}

// downloadTarball fetches the repository as one gzipped tar and reads it into
// memory. One request for the whole repository is the entire point: walking the
// contents API would spend a request per directory and hit the 60/hour
// unauthenticated limit long before finishing.
func (c *Client) downloadTarball(ctx context.Context, repo Repo, ref string) (*archive, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/tarball", c.base(), url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	if ref != "" {
		u += "/" + escapeRef(ref)
	}
	resp, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	arc, err := readTarball(resp.Body, repo.Subpath, defaultLimits)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", u, err)
	}
	if len(arc.Files) == 0 && repo.Subpath != "" {
		return nil, fmt.Errorf("no files found under %q in %s", repo.Subpath, repo.String())
	}
	return arc, nil
}

// escapeRef escapes a ref for use in a URL path while keeping the slashes in
// refs like "feature/x" or "refs/heads/main" intact.
func escapeRef(ref string) string {
	segs := strings.Split(ref, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// readTarball streams a gzipped tar and returns the regular files worth
// keeping, with the archive's generated root directory stripped. When subpath
// is non-empty only files under it are retained, which keeps memory
// proportional to what the user actually asked for.
func readTarball(r io.Reader, subpath string, lim limits) (*archive, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("open gzip stream: %w", err)
	}
	defer gz.Close()

	subpath = strings.Trim(path.Clean("/"+subpath), "/")
	if subpath == "." {
		subpath = ""
	}

	arc := &archive{Files: map[string][]byte{}}
	tr := tar.NewReader(gz)
	var decompressed int64
	var kept int

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar stream: %w", err)
		}
		if hdr.Typeflag == tar.TypeXHeader || hdr.Typeflag == tar.TypeXGlobalHeader {
			continue // PAX metadata carries no content of its own
		}

		// Validate before anything else. We only ever hold these bytes in
		// memory, but a "../" entry means the archive is hostile or broken and
		// trusting the rest of it is not a risk worth taking.
		name, err := entryPath(hdr.Name)
		if err != nil {
			return nil, err
		}
		if name == "" {
			continue
		}

		// GitHub wraps everything in one generated directory named
		// owner-repo-<sha>; the first entry establishes it.
		if arc.Root == "" {
			arc.Root = firstSegment(name)
		}
		rel, ok := strings.CutPrefix(name, arc.Root+"/")
		if !ok {
			continue // the root directory entry itself
		}

		// Directories, symlinks, hard links and devices carry nothing we want.
		// Skipping links rather than refusing them matters in practice: skill
		// libraries symlink shared references all the time, and the file they
		// point at is already in the archive on its own.
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		// Count every regular entry, including the ones we drop: the gzip
		// stream had to expand them either way, so that is the real cost.
		if decompressed += hdr.Size; decompressed > lim.total {
			return nil, fmt.Errorf("repository archive expands to more than %d MiB", lim.total>>20)
		}
		if excluded(rel) || hdr.Size > lim.file {
			continue
		}
		if subpath != "" && rel != subpath && !strings.HasPrefix(rel, subpath+"/") {
			continue
		}
		if kept++; kept > lim.entries {
			return nil, fmt.Errorf("repository archive contains more than %d files", lim.entries)
		}

		// The declared size is untrusted, so read one byte past the cap and
		// check what actually arrived.
		buf, err := io.ReadAll(io.LimitReader(tr, lim.file+1))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", hdr.Name, err)
		}
		if int64(len(buf)) > lim.file {
			continue
		}
		arc.Files[rel] = buf
	}
	return arc, nil
}

// entryPath validates a tar entry name and returns it as a clean, relative,
// slash-separated path. An empty name with a nil error means the entry refers
// to the archive root and can be skipped.
func entryPath(name string) (string, error) {
	unsafe := func(reason string) (string, error) {
		return "", &UnsafeArchiveError{Entry: name, Reason: reason}
	}

	n := strings.TrimSpace(name)
	switch {
	case n == "":
		return unsafe("empty entry name")
	case strings.ContainsRune(n, 0):
		return unsafe("entry name contains a NUL byte")
	case strings.ContainsRune(n, '\\'):
		// A backslash separates paths on Windows, so an entry containing one
		// could escape there even though it looks inert here.
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
// "C:". filepath.IsAbs does not catch these when we are running on Unix.
func hasDriveLetter(s string) bool {
	if len(s) < 2 || s[1] != ':' {
		return false
	}
	c := s[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func firstSegment(p string) string {
	if i := strings.Index(p, "/"); i >= 0 {
		return p[:i]
	}
	return p
}

// excluded reports whether a repository-relative file is never worth keeping.
// Hidden entries go at any depth: the store ignores them when it later indexes
// the copied skill, so carrying .github/, .idea/ or a stray .env would only add
// weight the user can never see.
func excluded(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, p := range parts {
		if strings.HasPrefix(p, ".") || excludeDirsAnyDepth[p] {
			return true
		}
	}
	base := parts[len(parts)-1]
	return excludeFiles[base] || excludeExts[strings.ToLower(path.Ext(base))]
}

// excludedInSkill applies the rules that only make sense once the skill's own
// root is known — evals/ is dropped from a skill, but a repository directory
// called evals/ that happens to contain skills is not.
func excludedInSkill(rel string) bool {
	if i := strings.Index(rel, "/"); i >= 0 {
		return excludeRootDirs[rel[:i]]
	}
	return false
}
