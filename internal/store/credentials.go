package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// Credentials are access tokens set through the UI rather than the environment.
//
// They live in their own collection, and nothing that leaves this machine ever
// reads it: not the export, not the git sync, not a share code. Keeping them in
// a separate table rather than a field on something else is the point, because
// it makes "never send this anywhere" a property of the schema instead of a
// filter somebody has to remember to write.
//
// Older releases kept them in data/.credentials.json. That file is still read
// once on open so an upgrade carries the tokens over, and is then left alone.
const credentialsFile = ".credentials.json"

// Credentials maps a token name (GITHUB_TOKEN, …) to its value.
type Credentials map[string]string

func (s *Store) credentialsPath() string { return filepath.Join(s.root, credentialsFile) }

// Credentials reads the stored tokens.
func (s *Store) Credentials() Credentials {
	out := Credentials{}
	recs, err := s.all(colCreds)
	if err != nil {
		return out
	}
	for _, rec := range recs {
		if v := rec.GetString("value"); v != "" {
			out[rec.GetString("name")] = v
		}
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

	recs, err := s.all(colCreds)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, rec := range recs {
		name := rec.GetString("name")
		value, keep := clean[name]
		if !keep {
			if err := s.app.Delete(rec); err != nil {
				return err
			}
			continue
		}
		seen[name] = true
		rec.Set("value", value)
		if err := asStoreError(s.app.Save(rec)); err != nil {
			return err
		}
	}

	col, err := s.app.FindCollectionByNameOrId(colCreds)
	if err != nil {
		return err
	}
	for name, value := range clean {
		if seen[name] {
			continue
		}
		rec := core.NewRecord(col)
		rec.Set("name", name)
		rec.Set("value", value)
		if err := asStoreError(s.app.Save(rec)); err != nil {
			return err
		}
	}
	return nil
}

// adoptLegacyCredentials imports data/.credentials.json once, if the database
// has no tokens yet. The file is left in place: it is the operator's, and a
// downgrade should still find it.
func (s *Store) adoptLegacyCredentials() error {
	if len(s.Credentials()) > 0 {
		return nil
	}
	raw, err := os.ReadFile(s.credentialsPath())
	if err != nil {
		return nil
	}
	old := Credentials{}
	if err := json.Unmarshal(raw, &old); err != nil {
		return nil
	}
	if len(old) == 0 {
		return nil
	}
	return s.SaveCredentials(old)
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
