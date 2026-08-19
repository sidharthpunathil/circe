package model

import (
	"encoding/base64"
	"fmt"
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
		return "", "", fmt.Errorf("paste the code or link you were sent")
	}

	if strings.HasPrefix(in, codePrefix) {
		raw, decErr := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(in, codePrefix))
		if decErr != nil {
			return "", "", fmt.Errorf("that code doesn't look right")
		}
		origin, id, ok := strings.Cut(string(raw), "|")
		if !ok || origin == "" || id == "" {
			return "", "", fmt.Errorf("that code doesn't look right")
		}
		return normalizeOrigin(origin), id, nil
	}

	// A share URL: https://host/s/<id>
	if strings.Contains(in, "/s/") {
		before, after, _ := strings.Cut(in, "/s/")
		id = strings.Trim(strings.SplitN(after, "?", 2)[0], "/")
		if before == "" || id == "" {
			return "", "", fmt.Errorf("that link doesn't look right")
		}
		return normalizeOrigin(before), id, nil
	}

	return "", "", fmt.Errorf("expected a code starting with %s, or a share link", codePrefix)
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
