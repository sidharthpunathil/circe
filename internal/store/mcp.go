package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// MCP servers are the third thing a project needs from us, alongside skills and
// rules. A skill teaches the agent a procedure, a rule constrains how it works,
// and an MCP server gives it a live connection to something outside the repo.
//
// They install differently again: not a directory and not prose, but entries in
// the project's .mcp.json. One server is a JSON file at data/mcps/<name>.json,
// and a folder of them is data/mcpsets/<name>.json.

// Transports Claude Code understands. Omitting the type means stdio, but for
// anything with a URL the type is required: without it Claude Code reads the
// entry as a misconfigured stdio server and skips it.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
	TransportSSE   = "sse"
	TransportWS    = "ws"
)

// MCPTransports lists the transports in the order the UI offers them. SSE is
// deprecated in favour of http and websocket is rare, so both come last.
func MCPTransports() []string {
	return []string{TransportStdio, TransportHTTP, TransportSSE, TransportWS}
}

// ValidTransport reports whether name is a transport we can write.
func ValidTransport(name string) bool {
	switch name {
	case TransportStdio, TransportHTTP, TransportSSE, TransportWS:
		return true
	}
	return false
}

// NormalizeTransport maps what people paste from vendor docs onto what we
// store. The MCP spec calls the http transport "streamable-http" and Claude
// Code accepts that spelling, so configs copied verbatim keep working.
func NormalizeTransport(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "streamable-http", "streamablehttp", "http":
		return TransportHTTP
	case "sse":
		return TransportSSE
	case "ws", "websocket":
		return TransportWS
	case "stdio", "":
		return TransportStdio
	}
	return s
}

// MCPCategories are the buckets the catalog and list pages group by.
func MCPCategories() []string {
	return []string{"development", "data", "cloud", "productivity", "search", "design", "automation", "other"}
}

// EnvVar is one environment variable a server needs before it will start.
//
// We deliberately store the variable's NAME, never its value. The generated
// .mcp.json references it as ${NAME}, which Claude Code expands from the
// developer's own environment. That keeps secrets out of this registry, out of
// any share code, and out of the file the project commits.
type EnvVar struct {
	Name     string `json:"name"`
	Required bool   `json:"required,omitempty"`
	Help     string `json:"help,omitempty"`
}

// MCPServer is one server definition.
type MCPServer struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description"`
	Category    string `json:"category,omitempty"`

	Transport string `json:"transport"`

	// stdio
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`

	// http and sse
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// Extra environment entries written verbatim into the config, on top of the
	// ${NAME} references derived from Env.
	Extra map[string]string `json:"extra,omitempty"`

	Env      []EnvVar `json:"env,omitempty"`
	Homepage string   `json:"homepage,omitempty"`
	Official bool     `json:"official,omitempty"`
	// CatalogID records which catalog entry this came from, so the UI can show
	// that it is a known server rather than something hand written.
	CatalogID string   `json:"catalogId,omitempty"`
	Tags      []string `json:"tags,omitempty"`

	UpdatedAt time.Time `json:"updatedAt,omitempty"`
	Size      int64     `json:"-"`
}

// DisplayTitle falls back to the name when no title is set.
func (m *MCPServer) DisplayTitle() string {
	if strings.TrimSpace(m.Title) != "" {
		return m.Title
	}
	return m.Name
}

// TransportOrDefault treats a missing transport as stdio, which is what an
// omitted "type" means in .mcp.json.
func (m *MCPServer) TransportOrDefault() string {
	if ValidTransport(m.Transport) {
		return m.Transport
	}
	return TransportStdio
}

// IsRemote reports whether the server is reached over the network rather than
// launched as a local process.
func (m *MCPServer) IsRemote() bool { return m.TransportOrDefault() != TransportStdio }

// ArgsLine renders args for a single-line form field.
func (m *MCPServer) ArgsLine() string { return strings.Join(m.Args, " ") }

// TagsCSV renders tags for a form field.
func (m *MCPServer) TagsCSV() string { return strings.Join(m.Tags, ", ") }

// EnvNames lists just the variable names, for compact display.
func (m *MCPServer) EnvNames() []string {
	out := make([]string, 0, len(m.Env))
	for _, e := range m.Env {
		out = append(out, e.Name)
	}
	return out
}

// RequiredEnv lists only the variables without which the server will not run.
func (m *MCPServer) RequiredEnv() []EnvVar {
	var out []EnvVar
	for _, e := range m.Env {
		if e.Required {
			out = append(out, e)
		}
	}
	return out
}

// CommandLine renders how the server starts, for display only.
func (m *MCPServer) CommandLine() string {
	if m.IsRemote() {
		return m.URL
	}
	return strings.TrimSpace(m.Command + " " + strings.Join(m.Args, " "))
}

// Config is the server as it appears inside .mcp.json. The shape is transport
// dependent: stdio takes a command to run, the other two take a URL to call.
func (m *MCPServer) Config() map[string]any {
	cfg := map[string]any{}
	switch m.TransportOrDefault() {
	case TransportHTTP, TransportSSE, TransportWS:
		cfg["type"] = m.TransportOrDefault()
		cfg["url"] = m.URL
		if len(m.Headers) > 0 {
			cfg["headers"] = sortedCopy(m.Headers)
		}
	default:
		// "type" is optional for stdio, and leaving it out keeps the generated
		// file close to what people write by hand.
		cfg["command"] = m.Command
		if len(m.Args) > 0 {
			cfg["args"] = append([]string(nil), m.Args...)
		}
		if env := m.envBlock(); len(env) > 0 {
			cfg["env"] = env
		}
	}
	return cfg
}

// envBlock builds the env map: every declared variable is passed through as a
// ${NAME} reference so the value comes from the developer's shell, and Extra
// entries are written as given.
func (m *MCPServer) envBlock() map[string]string {
	out := map[string]string{}
	for _, e := range m.Env {
		if e.Name == "" {
			continue
		}
		out[e.Name] = "${" + e.Name + "}"
	}
	for k, v := range m.Extra {
		if k != "" {
			out[k] = v
		}
	}
	return out
}

func sortedCopy(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// MCPSet is a named group of servers, the unit the CLI installs.
type MCPSet struct {
	Name        string    `json:"name"`
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description,omitempty"`
	Servers     []string  `json:"servers"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Has reports whether the folder already contains the named server.
func (s *MCPSet) Has(name string) bool {
	for _, n := range s.Servers {
		if n == name {
			return true
		}
	}
	return false
}

// DisplayTitle falls back to the name when no title is set.
func (s *MCPSet) DisplayTitle() string {
	if strings.TrimSpace(s.Title) != "" {
		return s.Title
	}
	return s.Name
}

func (s *Store) mcpsDir() string    { return filepath.Join(s.root, "mcps") }
func (s *Store) mcpSetsDir() string { return filepath.Join(s.root, "mcpsets") }
func (s *Store) mcpPath(n string) string {
	return filepath.Join(s.mcpsDir(), n+".json")
}

// ---------- loading ----------

// reloadMCPs rebuilds the server and folder indexes from disk.
func (s *Store) reloadMCPs() error {
	servers := map[string]*MCPServer{}
	if entries, err := os.ReadDir(s.mcpsDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".json")
			if ValidateName(name) != nil {
				continue
			}
			m, err := s.loadMCP(name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "skillbuilder: skipping mcp server %q: %v\n", name, err)
				continue
			}
			servers[name] = m
		}
	}

	sets := map[string]*MCPSet{}
	if entries, err := os.ReadDir(s.mcpSetsDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".json")
			if ValidateName(name) != nil {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(s.mcpSetsDir(), e.Name()))
			if err != nil {
				continue
			}
			var set MCPSet
			if err := json.Unmarshal(raw, &set); err != nil {
				fmt.Fprintf(os.Stderr, "skillbuilder: skipping mcp folder %q: %v\n", name, err)
				continue
			}
			set.Name = name
			if set.Servers == nil {
				set.Servers = []string{}
			}
			sets[name] = &set
		}
	}

	s.mu.Lock()
	s.mcps, s.mcpSets = servers, sets
	s.mu.Unlock()
	return nil
}

func (s *Store) loadMCP(name string) (*MCPServer, error) {
	path := s.mcpPath(name)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m MCPServer
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m.Name = name
	m.Size = int64(len(raw))
	if info, err := os.Stat(path); err == nil {
		m.UpdatedAt = info.ModTime()
	}
	return &m, nil
}

// ---------- queries ----------

// MCPServers returns every server sorted by name.
func (s *Store) MCPServers() []*MCPServer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*MCPServer, 0, len(s.mcps))
	for _, m := range s.mcps {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// MCPServer returns one server by name.
func (s *Store) MCPServer(name string) (*MCPServer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.mcps[name]
	if !ok {
		return nil, fmt.Errorf("mcp server %q: %w", name, ErrNotFound)
	}
	return m, nil
}

// MCPCount returns how many servers are stored.
func (s *Store) MCPCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.mcps)
}

// SearchMCPServers filters servers by free text.
func (s *Store) SearchMCPServers(query string) []*MCPServer {
	q := strings.ToLower(strings.TrimSpace(query))
	all := s.MCPServers()
	if q == "" {
		return all
	}
	var out []*MCPServer
	for _, m := range all {
		hay := strings.ToLower(strings.Join([]string{
			m.Name, m.Title, m.Description, m.Category, m.CommandLine(), strings.Join(m.Tags, " "),
		}, " "))
		if strings.Contains(hay, q) {
			out = append(out, m)
		}
	}
	return out
}

// MCPSets returns every server folder sorted by name.
func (s *Store) MCPSets() []*MCPSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*MCPSet, 0, len(s.mcpSets))
	for _, set := range s.mcpSets {
		out = append(out, set)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// MCPSet returns one server folder by name.
func (s *Store) MCPSet(name string) (*MCPSet, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set, ok := s.mcpSets[name]
	if !ok {
		return nil, fmt.Errorf("mcp folder %q: %w", name, ErrNotFound)
	}
	return set, nil
}

// MCPSetCount returns how many server folders are stored.
func (s *Store) MCPSetCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.mcpSets)
}

// MCPSetServers resolves a folder's servers, skipping any that no longer exist.
func (s *Store) MCPSetServers(set *MCPSet) []*MCPServer {
	out := make([]*MCPServer, 0, len(set.Servers))
	for _, name := range set.Servers {
		if m, err := s.MCPServer(name); err == nil {
			out = append(out, m)
		}
	}
	return out
}

// MCPSetsContaining returns folders that include the named server.
func (s *Store) MCPSetsContaining(name string) []*MCPSet {
	var out []*MCPSet
	for _, set := range s.MCPSets() {
		if set.Has(name) {
			out = append(out, set)
		}
	}
	return out
}

// ---------- mutations ----------

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateMCPServer checks a server is complete enough to write.
func ValidateMCPServer(m *MCPServer) error {
	if err := ValidateName(m.Name); err != nil {
		return err
	}
	if strings.TrimSpace(m.Description) == "" {
		return fmt.Errorf("add a description so you can tell servers apart later")
	}
	if !ValidTransport(m.Transport) {
		return fmt.Errorf("pick how the server is reached: stdio, http or sse")
	}
	if m.IsRemote() {
		u := strings.TrimSpace(m.URL)
		if u == "" {
			return fmt.Errorf("a %s server needs a URL", m.Transport)
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("the URL should start with http:// or https://")
		}
	} else if strings.TrimSpace(m.Command) == "" {
		return fmt.Errorf("a stdio server needs a command to run")
	}
	for _, e := range m.Env {
		if !envNamePattern.MatchString(e.Name) {
			return fmt.Errorf("%q is not a valid environment variable name", e.Name)
		}
	}
	return nil
}

// SaveMCPServer writes a server to disk.
func (s *Store) SaveMCPServer(m *MCPServer) error {
	if err := ValidateMCPServer(m); err != nil {
		return err
	}

	// Write a copy so a later edit by the caller cannot reach the index.
	rec := *m
	rec.Args = append([]string(nil), m.Args...)
	rec.Env = append([]EnvVar(nil), m.Env...)
	rec.Tags = append([]string(nil), m.Tags...)
	rec.Headers = sortedCopy(m.Headers)
	rec.Extra = sortedCopy(m.Extra)
	rec.Description = strings.TrimSpace(rec.Description)
	rec.UpdatedAt = time.Now()

	// Fields the transport does not use are dropped rather than carried around,
	// so switching a server from stdio to http does not leave a stale command
	// sitting in the file.
	if rec.IsRemote() {
		rec.Command, rec.Args = "", nil
	} else {
		rec.URL, rec.Headers = "", nil
	}

	raw, err := json.MarshalIndent(&rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.mcpsDir(), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(s.mcpPath(rec.Name), append(raw, '\n')); err != nil {
		return err
	}
	return s.refreshMCP(rec.Name)
}

// CreateMCPServer saves a new server, failing if the name is taken.
func (s *Store) CreateMCPServer(m *MCPServer) error {
	if err := ValidateName(m.Name); err != nil {
		return err
	}
	if _, err := os.Stat(s.mcpPath(m.Name)); err == nil {
		return fmt.Errorf("mcp server %q: %w", m.Name, ErrExists)
	}
	return s.SaveMCPServer(m)
}

// DeleteMCPServer removes a server and drops it from every folder.
func (s *Store) DeleteMCPServer(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if _, err := s.MCPServer(name); err != nil {
		return err
	}
	if err := os.Remove(s.mcpPath(name)); err != nil && !os.IsNotExist(err) {
		return err
	}

	s.mu.Lock()
	delete(s.mcps, name)
	var affected []*MCPSet
	for _, set := range s.mcpSets {
		if set.Has(name) {
			kept := set.Servers[:0]
			for _, n := range set.Servers {
				if n != name {
					kept = append(kept, n)
				}
			}
			set.Servers = kept
			affected = append(affected, set)
		}
	}
	s.mu.Unlock()

	for _, set := range affected {
		if err := s.SaveMCPSet(set); err != nil {
			return err
		}
	}
	return nil
}

// RenameMCPServer moves a server and updates folder references.
func (s *Store) RenameMCPServer(oldName, newName string) error {
	if err := ValidateName(newName); err != nil {
		return err
	}
	if oldName == newName {
		return nil
	}
	m, err := s.MCPServer(oldName)
	if err != nil {
		return err
	}
	if _, err := os.Stat(s.mcpPath(newName)); err == nil {
		return fmt.Errorf("mcp server %q: %w", newName, ErrExists)
	}

	renamed := *m
	renamed.Name = newName
	if err := s.SaveMCPServer(&renamed); err != nil {
		return err
	}
	if err := os.Remove(s.mcpPath(oldName)); err != nil && !os.IsNotExist(err) {
		return err
	}

	s.mu.Lock()
	delete(s.mcps, oldName)
	var affected []*MCPSet
	for _, set := range s.mcpSets {
		for i, n := range set.Servers {
			if n == oldName {
				set.Servers[i] = newName
				affected = append(affected, set)
			}
		}
	}
	s.mu.Unlock()

	for _, set := range affected {
		if err := s.SaveMCPSet(set); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) refreshMCP(name string) error {
	m, err := s.loadMCP(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.mcps[name] = m
	s.mu.Unlock()
	return nil
}

// SaveMCPSet writes a server folder, dropping references to missing servers.
func (s *Store) SaveMCPSet(set *MCPSet) error {
	if err := ValidateName(set.Name); err != nil {
		return err
	}
	kept := make([]string, 0, len(set.Servers))
	seen := map[string]bool{}
	for _, name := range set.Servers {
		if seen[name] {
			continue
		}
		if _, err := s.MCPServer(name); err != nil {
			continue
		}
		seen[name] = true
		kept = append(kept, name)
	}
	set.Servers = kept
	set.UpdatedAt = time.Now()

	raw, err := json.MarshalIndent(set, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.mcpSetsDir(), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(s.mcpSetsDir(), set.Name+".json"), append(raw, '\n')); err != nil {
		return err
	}

	s.mu.Lock()
	s.mcpSets[set.Name] = set
	s.mu.Unlock()
	return nil
}

// CreateMCPSet saves a new server folder, failing if the name is taken.
func (s *Store) CreateMCPSet(set *MCPSet) error {
	if err := ValidateName(set.Name); err != nil {
		return err
	}
	if _, err := s.MCPSet(set.Name); err == nil {
		return fmt.Errorf("mcp folder %q: %w", set.Name, ErrExists)
	}
	return s.SaveMCPSet(set)
}

// DeleteMCPSet removes a server folder. The servers themselves are kept.
func (s *Store) DeleteMCPSet(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if _, err := s.MCPSet(name); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.mcpSetsDir(), name+".json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	s.mu.Lock()
	delete(s.mcpSets, name)
	s.mu.Unlock()
	return nil
}

// RenderMCPConfig builds the .mcp.json body for a set of servers.
func RenderMCPConfig(servers []*MCPServer) ([]byte, error) {
	cfg := map[string]any{}
	for _, m := range servers {
		cfg[m.Name] = m.Config()
	}
	raw, err := json.MarshalIndent(map[string]any{"mcpServers": cfg}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// RenderMCPSet builds the .mcp.json body for a folder.
func (s *Store) RenderMCPSet(set *MCPSet) ([]byte, error) {
	return RenderMCPConfig(s.MCPSetServers(set))
}

// MergeMCPConfig folds servers into an existing .mcp.json, preserving both any
// servers the project already had and any top level keys we do not model.
// Returns the merged document and the names that were replaced.
func MergeMCPConfig(existing []byte, servers []*MCPServer, overwrite bool) ([]byte, []string, error) {
	doc := map[string]any{}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &doc); err != nil {
			return nil, nil, fmt.Errorf("the existing .mcp.json is not valid JSON: %w", err)
		}
	}
	current, _ := doc["mcpServers"].(map[string]any)
	if current == nil {
		current = map[string]any{}
	}

	var replaced []string
	for _, m := range servers {
		if _, exists := current[m.Name]; exists {
			if !overwrite {
				continue
			}
			replaced = append(replaced, m.Name)
		}
		current[m.Name] = m.Config()
	}
	doc["mcpServers"] = current

	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(replaced)
	return append(raw, '\n'), replaced, nil
}

// ParseMCPConfig reads a .mcp.json document into server definitions, so a
// config someone already wrote can be pulled straight into the registry.
func ParseMCPConfig(raw []byte) ([]*MCPServer, error) {
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	if len(doc.MCPServers) == 0 {
		// Some people keep a bare map of servers with no wrapper key.
		var bare map[string]json.RawMessage
		if err := json.Unmarshal(raw, &bare); err != nil {
			return nil, fmt.Errorf("no mcpServers in this file")
		}
		delete(bare, "mcpServers")
		doc.MCPServers = bare
	}

	var out []*MCPServer
	for name, blob := range doc.MCPServers {
		var entry struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		}
		if err := json.Unmarshal(blob, &entry); err != nil {
			continue
		}
		if entry.Command == "" && entry.URL == "" {
			continue
		}

		m := &MCPServer{
			Name:      Slugify(name),
			Title:     name,
			Transport: NormalizeTransport(entry.Type),
			Command:   entry.Command,
			Args:      entry.Args,
			URL:       entry.URL,
			Headers:   entry.Headers,
		}
		if !ValidTransport(m.Transport) {
			if entry.URL != "" {
				m.Transport = TransportHTTP
			} else {
				m.Transport = TransportStdio
			}
		}
		// A value of ${VAR} is a reference, so record it as a variable the user
		// must supply. Anything else is a literal we keep as written.
		for k, v := range entry.Env {
			if ref := envReference(v); ref != "" {
				m.Env = append(m.Env, EnvVar{Name: ref, Required: true})
				continue
			}
			if m.Extra == nil {
				m.Extra = map[string]string{}
			}
			m.Extra[k] = v
		}
		sort.Slice(m.Env, func(i, j int) bool { return m.Env[i].Name < m.Env[j].Name })

		if m.Description == "" {
			m.Description = fmt.Sprintf("Imported from an existing .mcp.json, reached over %s.", m.TransportOrDefault())
		}
		if err := ValidateName(m.Name); err != nil {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// envReference pulls NAME out of ${NAME} or ${NAME:-default}.
func envReference(v string) string {
	if !strings.HasPrefix(v, "${") || !strings.HasSuffix(v, "}") {
		return ""
	}
	inner := v[2 : len(v)-1]
	if i := strings.Index(inner, ":-"); i >= 0 {
		inner = inner[:i]
	}
	if envNamePattern.MatchString(inner) {
		return inner
	}
	return ""
}
