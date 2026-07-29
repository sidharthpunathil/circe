package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/sidharthp/skillbuilder/internal/store"
)

// The marketplace manifest lets stock Claude Code consume this registry with
// `/plugin marketplace add <url>` — no custom client needed. Each bundle becomes
// a plugin entry using strict:false plus an explicit skills list, which is the
// documented way to expose bare SKILL.md directories without a plugin.json.
type marketplace struct {
	Schema      string              `json:"$schema"`
	Name        string              `json:"name"`
	Version     string              `json:"version"`
	Description string              `json:"description"`
	Owner       marketplaceOwner    `json:"owner"`
	Plugins     []marketplacePlugin `json:"plugins"`
}

type marketplaceOwner struct {
	Name string `json:"name"`
}

type marketplacePlugin struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Source      string   `json:"source"`
	Strict      bool     `json:"strict"`
	Skills      []string `json:"skills"`
	Category    string   `json:"category,omitempty"`
}

const marketplaceSchemaURL = "https://json.schemastore.org/claude-code-marketplace.json"

// marketplaceJSON builds the manifest. When only is non-nil the manifest
// contains just that bundle, which is what ships inside a bundle download.
func (s *Server) marketplaceJSON(baseURL string, only *store.Bundle) ([]byte, error) {
	bundles := s.store.Bundles()
	if only != nil {
		bundles = []*store.Bundle{only}
	}

	m := marketplace{
		Schema:      marketplaceSchemaURL,
		Name:        "skillbuilder",
		Version:     "1.0.0",
		Description: "A personal registry of Claude agent skills.",
		Owner:       marketplaceOwner{Name: "skillbuilder"},
		Plugins:     []marketplacePlugin{},
	}

	for _, b := range bundles {
		if len(b.Skills) == 0 {
			continue // an empty plugin entry would fail to install
		}
		skills := make([]string, 0, len(b.Skills))
		for _, name := range b.Skills {
			skills = append(skills, "./skills/"+name)
		}
		desc := b.Description
		if strings.TrimSpace(desc) == "" {
			desc = b.DisplayTitle()
		}
		m.Plugins = append(m.Plugins, marketplacePlugin{
			Name:        b.Name,
			Description: desc,
			Source:      "./",
			Strict:      false,
			Skills:      skills,
		})
	}

	return json.MarshalIndent(m, "", "  ")
}

func (s *Server) handleMarketplace(w http.ResponseWriter, r *http.Request) {
	raw, err := s.marketplaceJSON(s.baseURL(r), nil)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(raw)
	w.Write([]byte("\n"))
}
