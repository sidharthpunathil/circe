package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Credentials are access tokens set through the UI rather than the environment.
//
// They deliberately do NOT live in settings.json or anywhere else inside the
// normal data tree, because that tree is meant to be committed as a backup.
// They go in data/.credentials.json, and creating it also writes a
// data/.gitignore that excludes it, so `git add data/` cannot sweep a token
// into a repository by accident.
const (
	credentialsFile = ".credentials.json"
	dataGitignore   = ".gitignore"
)

// Credentials maps a token name (GITHUB_TOKEN, …) to its value.
type Credentials map[string]string

func (s *Store) credentialsPath() string { return filepath.Join(s.root, credentialsFile) }

// Credentials reads the stored tokens. A missing file is not an error.
func (s *Store) Credentials() Credentials {
	raw, err := os.ReadFile(s.credentialsPath())
	if err != nil {
		return Credentials{}
	}
	out := Credentials{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Credentials{}
	}
	return out
}

// SaveCredentials replaces the stored tokens. Empty values are removed rather
// than persisted, so clearing a field in the UI actually deletes the token.
func (s *Store) SaveCredentials(c Credentials) error {
	clean := Credentials{}
	for k, v := range c {
		if v = strings.TrimSpace(v); v != "" {
			clean[k] = v
		}
	}

	if len(clean) == 0 {
		if err := os.Remove(s.credentialsPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	if err := s.ensureDataGitignore(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(clean, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.credentialsPath(), append(raw, '\n')); err != nil {
		return err
	}
	// Tokens are secrets; keep them off other local accounts.
	return os.Chmod(s.credentialsPath(), 0o600)
}

// ensureDataGitignore guarantees the credentials file cannot be committed with
// the rest of the data directory.
func (s *Store) ensureDataGitignore() error {
	path := filepath.Join(s.root, dataGitignore)
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(existing), credentialsFile) {
		return nil
	}
	body := string(existing)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += "# Access tokens. Never commit these.\n" + credentialsFile + "\n"
	return writeFileAtomic(path, []byte(body))
}

// CredentialNames lists the tokens the UI offers, in display order.
func CredentialNames() []string {
	return []string{"GITHUB_TOKEN", "GITLAB_TOKEN", "GITEA_TOKEN", "BITBUCKET_TOKEN"}
}

// TokenSource says where a token's value is coming from, so the UI can show
// that an environment variable is winning over what was typed in.
type TokenSource struct {
	Name     string
	FromEnv  bool
	HasValue bool
}

// TokenSources reports the state of every known token.
func (s *Store) TokenSources() []TokenSource {
	stored := s.Credentials()
	out := make([]TokenSource, 0, len(CredentialNames()))
	for _, name := range CredentialNames() {
		env := strings.TrimSpace(os.Getenv(name)) != ""
		out = append(out, TokenSource{
			Name:     name,
			FromEnv:  env,
			HasValue: env || stored[name] != "",
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return false })
	return out
}
