package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/sidharthp/skillbuilder/internal/installer"
)

// login exists so connecting to a hosted registry is one command instead of
// two config pokes, and so a wrong URL or token fails here rather than
// mysteriously on the next install.
func newLoginCmd() *cobra.Command {
	var token string
	cmd := &cobra.Command{
		Use:   "login [registry-url]",
		Short: "Connect this machine to a registry",
		Long: "Save a registry URL and token, verifying both before writing them.\n\n" +
			"With no arguments it prompts. Pass --token to run non-interactively.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			url := ""
			if len(args) == 1 {
				url = args[0]
			}
			return runLogin(cmd, url, token)
		},
	}
	cmd.Flags().StringVar(&token, "token", "",
		"registry token (prompted for if omitted; also read from $"+installer.EnvToken+")")
	return cmd
}

func runLogin(cmd *cobra.Command, url, token string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	cfg, err := installer.LoadConfig()
	if err != nil {
		return err
	}

	// Fall back to whatever is already configured, so `login` with no arguments
	// is a way to re-authenticate against the current registry.
	if url == "" {
		url = cfg.Registry
	}
	if url == "" {
		url = installer.DefaultRegistry
	}
	if token == "" {
		token = installer.ResolveToken(cfg)
	}

	if interactive() {
		fields := []huh.Field{}
		if len(cmd.Flags().Args()) == 0 {
			fields = append(fields, huh.NewInput().
				Title("Registry URL").
				Placeholder("https://skills.example.com").
				Value(&url))
		}
		if token == "" {
			fields = append(fields, huh.NewInput().
				Title("Token").
				Description("The registry's SKILLBUILDER_PASSWORD. Leave empty if it has none.").
				EchoMode(huh.EchoModePassword).
				Value(&token))
		}
		if len(fields) > 0 {
			if err := huh.NewForm(huh.NewGroup(fields...)).Run(); err != nil {
				return errCancelled
			}
		}
	}

	url = installer.NormalizeRegistry(strings.TrimSpace(url))
	if url == "" {
		return fmt.Errorf("a registry URL is required")
	}

	// Verify before saving: a token written to disk that does not work is worse
	// than no token, because the failure surfaces somewhere unrelated later.
	client := installer.NewClient(url)
	client.Token = strings.TrimSpace(token)

	verifyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := client.ListSkills(verifyCtx, "", "")
	if err != nil {
		var unauthorized *installer.UnauthorizedError
		if errors.As(err, &unauthorized) {
			if client.Token == "" {
				return fmt.Errorf("%s needs a token — run `skillbuilder login %s` and paste it", url, url)
			}
			return fmt.Errorf("%s rejected that token", url)
		}
		return err
	}

	cfg.Registry = url
	cfg.Token = client.Token
	if err := installer.SaveConfig(cfg); err != nil {
		return err
	}
	path, _ := installer.ConfigPath()

	auth := "no token needed"
	if client.Token != "" {
		auth = "authenticated"
	}
	fmt.Fprintln(out, "  "+okStyle.Render("✓")+" connected to "+url+
		dimStyle.Render("  ("+auth+", "+fmt.Sprint(resp.Total)+" skills)"))
	fmt.Fprintln(out, "  "+dimStyle.Render("saved to "+path))
	return nil
}

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget the saved registry token",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := installer.LoadConfig()
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				fmt.Fprintln(cmd.OutOrStdout(), dimStyle.Render("no token was saved"))
				return nil
			}
			cfg.Token = ""
			if err := installer.SaveConfig(cfg); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "  "+okStyle.Render("✓")+" token removed")
			return nil
		},
	}
}
