package installer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultRegistry is used when nothing else names one.
	DefaultRegistry = "http://localhost:8080"
	// EnvRegistry overrides the config file.
	EnvRegistry = "SKILLBUILDER_REGISTRY"

	configDirName  = ".skillbuilder"
	configFileName = "config.json"
)

// Config is the user-level configuration at ~/.skillbuilder/config.json.
// EnvToken overrides the stored token.
const EnvToken = "SKILLBUILDER_TOKEN"

type Config struct {
	Registry string `json:"registry,omitempty"`
	// Token authenticates against a registry that has a password set. It is
	// sent as a bearer token, so no username is needed.
	Token string `json:"token,omitempty"`
}

// ConfigPath returns the absolute path of the user config file.
func ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, configDirName, configFileName), nil
}

// LoadConfig reads the user config. A missing file yields a zero Config, since
// running with no config at all is the normal first-use state.
func LoadConfig() (*Config, error) {
	p, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return &c, nil
}

// SaveConfig writes the user config, creating ~/.skillbuilder if needed.
func SaveConfig(c *Config) error {
	p, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(p), err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if _, err := writeFileAtomic(p, 0o600, &buf); err != nil {
		return err
	}
	return nil
}

// RegistrySource names where a resolved registry URL came from, so `config` can
// explain itself instead of just printing a value.
type RegistrySource string

const (
	SourceFlag    RegistrySource = "--registry flag"
	SourceEnv     RegistrySource = "$" + EnvRegistry
	SourceConfig  RegistrySource = "config file"
	SourceDefault RegistrySource = "default"
)

// ResolveRegistry applies the precedence flag > environment > config > default.
func ResolveRegistry(flag string, cfg *Config) (string, RegistrySource) {
	if v := strings.TrimSpace(flag); v != "" {
		return NormalizeRegistry(v), SourceFlag
	}
	if v := strings.TrimSpace(os.Getenv(EnvRegistry)); v != "" {
		return NormalizeRegistry(v), SourceEnv
	}
	if cfg != nil {
		if v := strings.TrimSpace(cfg.Registry); v != "" {
			return NormalizeRegistry(v), SourceConfig
		}
	}
	return DefaultRegistry, SourceDefault
}

// NormalizeRegistry makes a hand-typed registry usable: "localhost:8080" and a
// trailing slash are both things people write, and neither should be an error.
func NormalizeRegistry(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	return strings.TrimRight(s, "/")
}

// ValidateRegistry rejects a URL that could not possibly be a registry, so the
// error lands at `config set` rather than on the next request.
func ValidateRegistry(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid registry URL %q: %w", s, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("registry URL must use http or https, got %q", s)
	}
	if u.Host == "" {
		return fmt.Errorf("registry URL %q has no host", s)
	}
	return nil
}
