package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/sidharthpunathil/skillbuilder/internal/installer"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show the resolved configuration",
		Long: "Show where skillbuilder is pointed and which source decided it.\n\n" +
			"Precedence is --registry, then $" + installer.EnvRegistry + ", then the config\n" +
			"file, then the built-in default.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigShow(cmd.OutOrStdout())
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value (keys: registry, token)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigSet(cmd.OutOrStdout(), args[0], args[1])
		},
	})
	return cmd
}

func runConfigShow(out io.Writer) error {
	url, source, err := resolveRegistry()
	if err != nil {
		return err
	}
	path, err := installer.ConfigPath()
	if err != nil {
		return err
	}

	row := func(k, v, note string) {
		line := "  " + pad(k, 10) + v
		if note != "" {
			line += "  " + dimStyle.Render("("+note+")")
		}
		fmt.Fprintln(out, line)
	}

	row("registry", url, string(source))

	note := ""
	if _, err := os.Stat(path); os.IsNotExist(err) {
		note = "not created yet"
	}
	row("config", path, note)

	root, err := projectRoot()
	if err != nil {
		return err
	}
	lf, lfErr := installer.LoadLockfile(root)
	switch {
	case lfErr != nil:
		row("lockfile", installer.LockfilePath(root), "unreadable: "+lfErr.Error())
	case len(lf.Skills) == 0:
		row("lockfile", installer.LockfilePath(root), "no skills recorded")
	default:
		row("lockfile", installer.LockfilePath(root), fmt.Sprintf("%d skills", len(lf.Skills)))
	}
	row("skills", installer.DefaultSkillsDir, "default install directory")
	return nil
}

func runConfigSet(out io.Writer, key, value string) error {
	if key == "token" {
		cfg, err := installer.LoadConfig()
		if err != nil {
			return err
		}
		cfg.Token = value
		if err := installer.SaveConfig(cfg); err != nil {
			return err
		}
		path, _ := installer.ConfigPath()
		fmt.Fprintln(out, "  "+okStyle.Render("✓")+" token set"+dimStyle.Render("  ("+path+")"))
		return nil
	}
	if key != "registry" {
		return fmt.Errorf("unknown config key %q — valid keys are `registry` and `token`", key)
	}

	url := installer.NormalizeRegistry(value)
	if err := installer.ValidateRegistry(url); err != nil {
		return err
	}

	cfg, err := installer.LoadConfig()
	if err != nil {
		return err
	}
	cfg.Registry = url
	if err := installer.SaveConfig(cfg); err != nil {
		return err
	}

	path, err := installer.ConfigPath()
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "  "+okStyle.Render("✓")+" registry "+url+dimStyle.Render("  ("+path+")"))
	return nil
}
