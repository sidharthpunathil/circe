package store

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Sharing lets one self-hosted registry hand a folder or skill to another.
//
// There is no central index, so a share is just an unguessable id minted by the
// origin plus a public, read-only endpoint that serves *only* explicitly shared
// resources. Everything else stays behind the registry's password. Sharing is
// opt-in per resource and revocable, and the importer copies content in, so the
// copy survives the origin going away.
const (
	// ShareKindFolder and ShareKindSkill are the shareable resource types.
	ShareKindFolder = "folder"
	ShareKindSkill  = "skill"

	// codePrefix marks a share code so it is recognisable when pasted.
	codePrefix = "sb_"
)

// Share records one shared resource.
type Share struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Store) sharesPath() string { return filepath.Join(s.root, "shares.json") }

// newShareID returns an unguessable, URL-safe id. Ten base32 characters is
// 50 bits — far beyond brute-forcing a personal registry, and still short
// enough to paste.
func newShareID() (string, error) {
	buf := make([]byte, 7)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	return strings.ToLower(enc.EncodeToString(buf))[:10], nil
}

func (s *Store) loadShares() (map[string]Share, error) {
	raw, err := os.ReadFile(s.sharesPath())
	if os.IsNotExist(err) {
		return map[string]Share{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]Share{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse shares: %w", err)
	}
	return m, nil
}

func (s *Store) saveShares(m map[string]Share) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.sharesPath(), append(raw, '\n'))
}

// Shares returns every active share, newest first.
func (s *Store) Shares() []Share {
	m, err := s.loadShares()
	if err != nil {
		return nil
	}
	out := make([]Share, 0, len(m))
	for _, sh := range m {
		out = append(out, sh)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// ShareFor returns the existing share for a resource, if any.
func (s *Store) ShareFor(kind, name string) (Share, bool) {
	m, err := s.loadShares()
	if err != nil {
		return Share{}, false
	}
	for _, sh := range m {
		if sh.Kind == kind && sh.Name == name {
			return sh, true
		}
	}
	return Share{}, false
}

// ShareByID looks up a share. This is the only lookup the public endpoint uses,
// so an unshared resource is simply not reachable.
func (s *Store) ShareByID(id string) (Share, bool) {
	m, err := s.loadShares()
	if err != nil {
		return Share{}, false
	}
	sh, ok := m[id]
	return sh, ok
}

// CreateShare mints a code for a resource, or returns the existing one so that
// sharing twice does not invalidate a link already sent to someone.
func (s *Store) CreateShare(kind, name string) (Share, error) {
	switch kind {
	case ShareKindFolder:
		if _, err := s.Bundle(name); err != nil {
			return Share{}, err
		}
	case ShareKindSkill:
		if _, err := s.Skill(name); err != nil {
			return Share{}, err
		}
	default:
		return Share{}, fmt.Errorf("unknown share kind %q", kind)
	}

	if existing, ok := s.ShareFor(kind, name); ok {
		return existing, nil
	}

	id, err := newShareID()
	if err != nil {
		return Share{}, err
	}
	sh := Share{ID: id, Kind: kind, Name: name, CreatedAt: time.Now()}

	m, err := s.loadShares()
	if err != nil {
		return Share{}, err
	}
	m[id] = sh
	if err := s.saveShares(m); err != nil {
		return Share{}, err
	}
	return sh, nil
}

// RevokeShare removes a share, making the code stop working immediately.
func (s *Store) RevokeShare(kind, name string) error {
	m, err := s.loadShares()
	if err != nil {
		return err
	}
	for id, sh := range m {
		if sh.Kind == kind && sh.Name == name {
			delete(m, id)
		}
	}
	return s.saveShares(m)
}

// ---------- code encoding ----------

// EncodeShareCode packs the origin host and share id into one pasteable token,
// so the recipient needs nothing but the code to find the source registry.
func EncodeShareCode(origin, id string) string {
	payload := strings.TrimRight(origin, "/") + "|" + id
	return codePrefix + base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// DecodeShareCode accepts either a share code or a plain share URL, since
// people will paste whichever they were given.
func DecodeShareCode(input string) (origin, id string, err error) {
	in := strings.TrimSpace(input)
	if in == "" {
		return "", "", fmt.Errorf("paste a share code or link")
	}

	if strings.HasPrefix(in, codePrefix) {
		raw, decErr := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(in, codePrefix))
		if decErr != nil {
			return "", "", fmt.Errorf("that share code is not valid")
		}
		origin, id, ok := strings.Cut(string(raw), "|")
		if !ok || origin == "" || id == "" {
			return "", "", fmt.Errorf("that share code is not valid")
		}
		return normalizeOrigin(origin), id, nil
	}

	// A share URL: https://host/s/<id>
	if strings.Contains(in, "/s/") {
		before, after, _ := strings.Cut(in, "/s/")
		id = strings.Trim(strings.SplitN(after, "?", 2)[0], "/")
		if before == "" || id == "" {
			return "", "", fmt.Errorf("that share link is not valid")
		}
		return normalizeOrigin(before), id, nil
	}

	return "", "", fmt.Errorf("expected a share code starting with %s, or a share link", codePrefix)
}

// normalizeOrigin ensures a scheme, defaulting to https for anything that is
// not obviously a local address.
func normalizeOrigin(origin string) string {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if strings.HasPrefix(origin, "http://") || strings.HasPrefix(origin, "https://") {
		return origin
	}
	if strings.HasPrefix(origin, "localhost") || strings.HasPrefix(origin, "127.0.0.1") {
		return "http://" + origin
	}
	return "https://" + origin
}
