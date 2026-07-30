// Package installer contains everything the skillbuilder CLI needs to turn a
// registry into files on disk: the HTTP client, the safe archive extractor, the
// deterministic checksum used to detect drift, and the project lockfile.
//
// It is deliberately free of terminal concerns so the whole install path can be
// exercised from tests.
package installer

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
)

// DefaultSkillsDir is where Claude Code looks for project-local skills.
const DefaultSkillsDir = ".claude/skills"

// Status describes what happened to a single skill during an install.
type Status string

const (
	// StatusInstalled means the skill was not present and is now.
	StatusInstalled Status = "installed"
	// StatusUpdated means an existing skill was replaced with different content.
	StatusUpdated Status = "updated"
	// StatusUnchanged means the skill was already present with identical content.
	StatusUnchanged Status = "unchanged"
	// StatusSkipped means an existing skill differed but --force was not given.
	StatusSkipped Status = "skipped"
)

// Result reports the outcome for one skill.
type Result struct {
	Name     string
	Status   Status
	Checksum string
	// Reason explains a StatusSkipped result.
	Reason string
}

// Options controls where and how skills are written.
type Options struct {
	// Dir is the skills directory, relative to the project root or absolute.
	Dir string
	// Force replaces skills that already exist with different content.
	Force bool
	// DryRun performs the download and comparison but writes nothing.
	DryRun bool
}

func (o Options) dir() string {
	if o.Dir == "" {
		return filepath.FromSlash(DefaultSkillsDir)
	}
	return filepath.FromSlash(o.Dir)
}

// SkillDir returns the on-disk directory a skill installs into.
func SkillDir(dir, name string) string {
	if dir == "" {
		dir = DefaultSkillsDir
	}
	return filepath.Join(filepath.FromSlash(dir), name)
}

// Install downloads the named skills from the registry and writes them into
// opts.Dir. It is the entry point used by `add` and `pull`.
func Install(ctx context.Context, c *Client, names []string, opts Options) ([]Result, error) {
	if len(names) == 0 {
		return nil, nil
	}
	rc, err := c.Download(ctx, names)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return InstallArchive(rc, opts)
}

// InstallBundle downloads a whole bundle in one request. Callers installing only
// part of a bundle should use Install with the selected names instead.
func InstallBundle(ctx context.Context, c *Client, bundle string, opts Options) ([]Result, error) {
	rc, err := c.BundleArchive(ctx, bundle)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return InstallArchive(rc, opts)
}

// InstallArchive extracts a gzipped tar of skills into opts.Dir.
//
// The archive is unpacked into a staging directory first and each skill is then
// moved into place with a single rename. That keeps a half-downloaded or
// malicious archive from ever being visible at the destination, and makes
// --dry-run exact rather than a guess: the real bytes are compared against what
// is already installed.
func InstallArchive(r io.Reader, opts Options) ([]Result, error) {
	dir := opts.dir()

	// Stage inside the destination so the final move is a same-filesystem
	// rename. A dry run never renames, so it can stage in the system temp dir
	// and leave the project untouched.
	stageParent := dir
	if opts.DryRun {
		stageParent = ""
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	stage, err := os.MkdirTemp(stageParent, ".skillbuilder-stage-")
	if err != nil {
		return nil, fmt.Errorf("create staging directory: %w", err)
	}
	defer os.RemoveAll(stage)

	names, err := ExtractTarGz(r, stage)
	if err != nil {
		return nil, err
	}

	results := make([]Result, 0, len(names))
	for _, name := range names {
		src := filepath.Join(stage, name)
		sum, err := Checksum(src)
		if err != nil {
			return results, fmt.Errorf("checksum %s: %w", name, err)
		}
		dst := filepath.Join(dir, name)

		res := Result{Name: name, Checksum: sum}
		switch existing, err := checksumIfExists(dst); {
		case err != nil:
			return results, fmt.Errorf("inspect %s: %w", dst, err)
		case existing == "":
			res.Status = StatusInstalled
		case existing == sum:
			res.Status = StatusUnchanged
		case !opts.Force:
			res.Status = StatusSkipped
			res.Reason = "different content on disk; --force overwrites"
			// Report the checksum that is actually on disk so the lockfile
			// never claims content the project does not have.
			res.Checksum = existing
		default:
			res.Status = StatusUpdated
		}

		if !opts.DryRun && (res.Status == StatusInstalled || res.Status == StatusUpdated) {
			if err := replaceDir(src, dst); err != nil {
				return results, err
			}
		}
		results = append(results, res)
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	return results, nil
}

// checksumIfExists returns "" when dir does not exist.
func checksumIfExists(dir string) (string, error) {
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s exists and is not a directory", dir)
	}
	return Checksum(dir)
}

// replaceDir moves src onto dst. The old directory is renamed aside first so a
// failure to move the new one can be rolled back instead of leaving a hole.
func replaceDir(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
	}
	backup := dst + ".skillbuilder-old"
	_ = os.RemoveAll(backup)

	restore := false
	if _, err := os.Lstat(dst); err == nil {
		if err := os.Rename(dst, backup); err != nil {
			return fmt.Errorf("replace %s: %w", dst, err)
		}
		restore = true
	}
	if err := os.Rename(src, dst); err != nil {
		if restore {
			_ = os.Rename(backup, dst)
		}
		return fmt.Errorf("install %s: %w", dst, err)
	}
	_ = os.RemoveAll(backup)
	return nil
}

// Remove deletes an installed skill directory. It reports whether anything was
// there to delete.
func Remove(dir, name string) (bool, error) {
	target := SkillDir(dir, name)
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("inspect %s: %w", target, err)
	}
	if err := os.RemoveAll(target); err != nil {
		return false, fmt.Errorf("remove %s: %w", target, err)
	}
	return true, nil
}

// Counts tallies results by status for a one-line summary.
func Counts(results []Result) map[Status]int {
	out := map[Status]int{}
	for _, r := range results {
		out[r.Status]++
	}
	return out
}

// InstallFromURL installs skills from a publicly reachable archive.
//
// Share archives are served without authentication by the origin registry, so
// there is no client or token to thread through — the URL is the whole contract.
func InstallFromURL(ctx context.Context, url string, names []string, opts Options) ([]Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := (&http.Client{Timeout: requestTimeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: server returned %d", url, resp.StatusCode)
	}
	return InstallArchive(resp.Body, opts)
}
