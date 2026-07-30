package main

import (
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/sidharthpunathil/circe/internal/api"
	"github.com/sidharthpunathil/circe/internal/installer"
)

// registryFlag backs the global --registry flag.
var registryFlag string

// resolveRegistry applies flag > environment > config > default.
func resolveRegistry() (string, installer.RegistrySource, error) {
	cfg, err := installer.LoadConfig()
	if err != nil {
		return "", "", err
	}
	url, source := installer.ResolveRegistry(registryFlag, cfg)
	if err := installer.ValidateRegistry(url); err != nil {
		return "", "", fmt.Errorf("registry from %s: %w", source, err)
	}
	return url, source, nil
}

func newClient() (*installer.Client, error) {
	url, _, err := resolveRegistry()
	if err != nil {
		return nil, err
	}
	c := installer.NewClient(url)
	cfg, err := installer.LoadConfig()
	if err == nil {
		c.Token = installer.ResolveToken(cfg)
	}
	return c, nil
}

// projectRoot is the current directory: the lockfile belongs next to whatever
// the user is working on, not at some inferred repository root.
func projectRoot() (string, error) {
	root, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("locate the current directory: %w", err)
	}
	return root, nil
}

// installFlags are shared by every command that writes skills to disk.
type installFlags struct {
	dir    string
	force  bool
	dryRun bool
	cmd    *cobra.Command
}

func (f *installFlags) register(cmd *cobra.Command) {
	f.cmd = cmd
	fl := cmd.Flags()
	fl.StringVarP(&f.dir, "dir", "d", installer.DefaultSkillsDir, "directory to install skills into")
	fl.BoolVar(&f.force, "force", false, "overwrite skills that already exist")
	fl.BoolVar(&f.dryRun, "dry-run", false, "report what would change without writing anything")
}

// dirWasSet reports whether -d was given explicitly, as opposed to defaulted.
func (f *installFlags) dirWasSet() bool {
	return f.cmd != nil && f.cmd.Flags().Changed("dir")
}

// resolveDir honours an explicit -d, otherwise reuses the directory recorded by
// a previous install in this project.
func (f *installFlags) resolveDir(root string) string {
	lf, err := installer.LoadLockfile(root)
	if err != nil {
		return f.dir
	}
	return installer.ResolveDir(f.dir, f.dirWasSet(), lf)
}

func (f *installFlags) options() installer.Options {
	return installer.Options{Dir: f.dir, Force: f.force, DryRun: f.dryRun}
}

func (f *installFlags) optionsIn(dir string) installer.Options {
	return installer.Options{Dir: dir, Force: f.force, DryRun: f.dryRun}
}

func skillNames(skills []api.Skill) []string {
	out := make([]string, len(skills))
	for i, s := range skills {
		out[i] = s.Name
	}
	return out
}

// recordInstall folds install results into the project lockfile.
//
// Skipped skills are deliberately left untouched: the copy on disk is the
// user's, not ours, and rewriting its lockfile entry would erase exactly the
// drift that `list` exists to surface.
func recordInstall(root, registry, bundle, dir string, results []installer.Result) error {
	lf, err := installer.LoadLockfile(root)
	if err != nil {
		return err
	}
	lf.Registry = registry
	installer.RecordDir(lf, dir)

	recorded := 0
	for _, r := range results {
		if r.Status == installer.StatusSkipped {
			continue
		}
		installer.RecordSkill(lf, api.InstalledSkill{
			Name:     r.Name,
			Bundle:   bundle,
			Registry: registry,
			Checksum: r.Checksum,
		})
		recorded++
	}
	if recorded > 0 {
		installer.AddBundle(lf, bundle)
	}
	return installer.SaveLockfile(root, lf)
}

// applyContext merges a folder's CLAUDE.md / AGENTS.md into the project.
//
// These are the user's own files, so the default is a marked block that leaves
// everything around it untouched: re-installing replaces only our block, and
// several folders can contribute to one file without fighting.
func applyContext(out io.Writer, root, folder string, files map[string]string, mode string, dryRun bool) error {
	if len(files) == 0 {
		return nil
	}
	m := installer.ContextMode(mode)
	switch m {
	case installer.ContextMerge, installer.ContextSkip, installer.ContextOverwrite:
	default:
		return fmt.Errorf("unknown --context mode %q. Use merge, skip or overwrite", mode)
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		res, err := installer.WriteContextFile(root, folder, name, files[name], m, dryRun)
		if err != nil {
			return err
		}
		switch res.Action {
		case "unchanged":
			fmt.Fprintln(out, "  "+dimStyle.Render("· "+name+"  unchanged"))
		case "skipped":
			fmt.Fprintln(out, "  "+warnStyle.Render("!")+" "+name+"  "+
				dimStyle.Render("left alone (--context=skip)"))
		default:
			fmt.Fprintln(out, "  "+okStyle.Render("✓")+" "+name+"  "+okStyle.Render(res.Action))
		}
	}
	return nil
}

// pluralWord picks a singular or plural noun.
func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
