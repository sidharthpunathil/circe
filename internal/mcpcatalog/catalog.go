// Package mcpcatalog is a small, verified list of MCP servers people can add to
// their registry without looking up a command line first.
//
// The list lives in catalog.json, embedded at build time. Its schema is one
// object with an "entries" array, and every entry looks like this:
//
//	id           stable slug; also the name the server is saved under
//	title        human name, shown on the card
//	description  one sentence: what this connects the agent to
//	category     one of store.MCPCategories()
//	transport    "stdio", "http" or "sse"
//	command      stdio only: the program to run, usually "npx"
//	args         stdio only: its arguments, as separate strings
//	url          http and sse only: the endpoint to call
//	headers      http and sse only: name/value pairs sent with every request
//	env          [{"name","required","help"}] variable NAMES the server needs
//	homepage     where to read more about it
//	official     true when the vendor of the connected system maintains it
//	tags         free text keywords, used by search
//
// Note what "env" does not contain: values. The registry stores only the name
// of each variable, and the generated .mcp.json refers to it as ${NAME}, so the
// secret comes from the developer's own shell and never from here.
package mcpcatalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/sidharthpunathil/circe/internal/store"
)

//go:embed catalog.json
var raw []byte

// Entry is one server in the catalog. It carries everything store.MCPServer
// needs, plus where it came from.
type Entry struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Category    string            `json:"category,omitempty"`
	Transport   string            `json:"transport"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Env         []store.EnvVar    `json:"env,omitempty"`
	Homepage    string            `json:"homepage,omitempty"`
	Official    bool              `json:"official,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
}

// CommandLine renders how the server is reached, for display only.
func (e Entry) CommandLine() string {
	if e.Transport != store.TransportStdio && e.URL != "" {
		return e.URL
	}
	return strings.TrimSpace(e.Command + " " + strings.Join(e.Args, " "))
}

// NeedsEnv reports whether the entry cannot start until a variable is set.
func (e Entry) NeedsEnv() bool {
	for _, v := range e.Env {
		if v.Required {
			return true
		}
	}
	return false
}

// EnvNames lists the variables the entry expects, for compact display.
func (e Entry) EnvNames() []string {
	out := make([]string, 0, len(e.Env))
	for _, v := range e.Env {
		out = append(out, v.Name)
	}
	return out
}

// ToServer converts a catalog entry into a server that can be saved. Slices and
// maps are copied so the caller can edit the result without reaching back into
// the embedded catalog.
func (e Entry) ToServer() *store.MCPServer {
	m := &store.MCPServer{
		Name:        e.ID,
		Title:       e.Title,
		Description: e.Description,
		Category:    e.Category,
		Transport:   e.Transport,
		Command:     e.Command,
		Args:        append([]string(nil), e.Args...),
		URL:         e.URL,
		Env:         append([]store.EnvVar(nil), e.Env...),
		Homepage:    e.Homepage,
		Official:    e.Official,
		CatalogID:   e.ID,
		Tags:        append([]string(nil), e.Tags...),
	}
	if len(e.Headers) > 0 {
		m.Headers = make(map[string]string, len(e.Headers))
		for k, v := range e.Headers {
			m.Headers[k] = v
		}
	}
	if !store.ValidTransport(m.Transport) {
		if m.URL != "" {
			m.Transport = store.TransportHTTP
		} else {
			m.Transport = store.TransportStdio
		}
	}
	return m
}

// entries parses the embedded catalog once. A malformed file leaves the catalog
// empty rather than stopping the server: the rest of the registry still works,
// and the operator sees why on stderr.
var entries = sync.OnceValue(func() []Entry {
	var doc struct {
		Entries []Entry `json:"entries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintf(os.Stderr, "circe: mcp catalog: %v\n", err)
		return nil
	}

	out := make([]Entry, 0, len(doc.Entries))
	seen := map[string]bool{}
	for _, e := range doc.Entries {
		e.ID = strings.TrimSpace(e.ID)
		if e.ID == "" || seen[e.ID] || store.ValidateName(e.ID) != nil {
			continue
		}
		seen[e.ID] = true
		if e.Title == "" {
			e.Title = e.ID
		}
		if e.Category == "" {
			e.Category = "other"
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out
})

// All returns every catalog entry, sorted by category then title.
func All() []Entry {
	src := entries()
	return append([]Entry(nil), src...)
}

// ByCategory groups the catalog for a page that shows one section per bucket.
func ByCategory() map[string][]Entry {
	out := map[string][]Entry{}
	for _, e := range entries() {
		out[e.Category] = append(out[e.Category], e)
	}
	return out
}

// Get looks up one entry by id.
func Get(id string) (Entry, bool) {
	for _, e := range entries() {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Count reports how many servers the catalog knows about.
func Count() int { return len(entries()) }
