package store

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// PocketBase is pre-1.0, and pre-1.0 in its case is not a formality: v0.22 to
// v0.23 rewrote the app, router and record APIs wholesale, and minor releases
// have moved things since. This package is written against one version of that
// surface.
//
// So the version is pinned here as well as in go.mod, and this test fails when
// they diverge. `go get -u`, a dependency bot, or a careless `go mod tidy` on a
// newer toolchain then breaks the build with an explanation rather than
// breaking the registry at runtime.
//
// Upgrading is fine. It is a decision, not a side effect: bump this constant,
// run the whole suite including `make e2e`, and read PocketBase's changelog for
// the releases you skipped.
const pinnedPocketBase = "v0.39.11"

func TestPocketBaseVersionIsPinned(t *testing.T) {
	got, source := pocketBaseVersion(t)
	if got != pinnedPocketBase {
		t.Fatalf("PocketBase is %s (from %s) but this package is written against %s.\n\n"+
			"PocketBase is pre-1.0 and changes its API across minor releases. If the upgrade "+
			"is deliberate, read its changelog, run the full suite including `make e2e`, and "+
			"update pinnedPocketBase in %s.",
			got, source, pinnedPocketBase, "internal/store/version_test.go")
	}
}

// pocketBaseVersion reports the version actually being built against.
//
// Build info is the honest answer because it reflects the resolved module
// graph, but it is empty under some toolchains and test runners, so go.mod is
// read as a fallback rather than skipping the check.
func pocketBaseVersion(t *testing.T) (version, source string) {
	t.Helper()
	const module = "github.com/pocketbase/pocketbase"

	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == module {
				if dep.Replace != nil {
					return dep.Replace.Version, "build info (replaced)"
				}
				return dep.Version, "build info"
			}
		}
	}

	root, err := repoRoot()
	if err != nil {
		t.Fatalf("cannot find go.mod to check the pinned version: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && fields[0] == module {
			return fields[1], "go.mod"
		}
	}
	t.Fatalf("%s is not required by go.mod", module)
	return "", ""
}

// repoRoot walks up from the working directory looking for go.mod.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
