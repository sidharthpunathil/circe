package store

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// ---------- loading ----------

// reloadMCPs rebuilds the server index from the database.
func (s *Store) reloadMCPs() error {
	recs, err := s.all(colMCPs)
	if err != nil {
		return err
	}
	servers := map[string]*MCPServer{}
	for _, rec := range recs {
		name := rec.GetString("name")
		if ValidateName(name) != nil {
			continue
		}
		m, err := mcpFromRecord(rec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "circe: skipping mcp server %q: %v\n", name, err)
			continue
		}
		servers[name] = m
	}

	s.mu.Lock()
	s.mcps = servers
	s.mu.Unlock()
	return nil
}

// mcpFromRecord parses a stored server definition back into an MCPServer.
func mcpFromRecord(rec *core.Record) (*MCPServer, error) {
	raw := rec.GetString("source")
	var m MCPServer
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	m.Name = rec.GetString("name")
	m.Size = int64(len(raw))
	m.UpdatedAt = rec.GetDateTime("updated").Time()
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

// ---------- mutations ----------

// SaveMCPServer writes a server.
func (s *Store) SaveMCPServer(m *MCPServer) error {
	return s.saveMCPServerAt(m, time.Now())
}

// saveMCPServerAt is SaveMCPServer with an explicit last-changed time.
//
// A server's document carries its own timestamp, so stamping "now" onto an
// imported one would rewrite its document and make it look changed. That turns
// every pull into a diff on the next push, which is the kind of churn that
// makes a sync feel untrustworthy.
func (s *Store) saveMCPServerAt(m *MCPServer, at time.Time) error {
	if err := ValidateMCPServer(m); err != nil {
		return err
	}
	if at.IsZero() {
		at = time.Now()
	}

	// Persist a normalized copy, so a later edit by the caller cannot reach the
	// index and a switch of transport does not leave stale fields behind.
	rec := m.Normalized()
	rec.UpdatedAt = at

	raw, err := rec.Document()
	if err != nil {
		return err
	}
	source := string(raw)

	err = s.upsert(colMCPs, "name", rec.Name, func(r *core.Record) {
		r.Set("source", source)
		r.Set("description", rec.Description)
		r.Set("category", rec.Category)
		r.Set("transport", rec.Transport)
		r.Set("size", len(source))
		setUpdated(r, "updated", rec.UpdatedAt)
	})
	if err != nil {
		return err
	}
	return s.refreshMCP(rec.Name)
}

// CreateMCPServer saves a new server, failing if the name is taken.
func (s *Store) CreateMCPServer(m *MCPServer) error {
	if err := ValidateName(m.Name); err != nil {
		return err
	}
	if _, err := s.findByField(colMCPs, "name", m.Name); err == nil {
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
	if rec, err := s.findByField(colMCPs, "name", name); err == nil {
		if err := s.app.Delete(rec); err != nil {
			return err
		}
	}

	s.mu.Lock()
	delete(s.mcps, name)
	var affected []*Bundle
	for _, b := range s.bundles {
		if b.HasServer(name) {
			kept := make([]string, 0, len(b.Servers))
			for _, n := range b.Servers {
				if n != name {
					kept = append(kept, n)
				}
			}
			b.Servers = kept
			affected = append(affected, b)
		}
	}
	s.mu.Unlock()

	for _, b := range affected {
		if err := s.SaveBundle(b); err != nil {
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
	if _, err := s.findByField(colMCPs, "name", newName); err == nil {
		return fmt.Errorf("mcp server %q: %w", newName, ErrExists)
	}

	renamed := *m
	renamed.Name = newName
	if err := s.SaveMCPServer(&renamed); err != nil {
		return err
	}
	if old, err := s.findByField(colMCPs, "name", oldName); err == nil {
		if err := s.app.Delete(old); err != nil {
			return err
		}
	}

	s.mu.Lock()
	delete(s.mcps, oldName)
	var affected []*Bundle
	for _, b := range s.bundles {
		for i, n := range b.Servers {
			if n == oldName {
				b.Servers[i] = newName
				affected = append(affected, b)
			}
		}
	}
	s.mu.Unlock()

	for _, b := range affected {
		if err := s.SaveBundle(b); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) refreshMCP(name string) error {
	rec, err := s.findByField(colMCPs, "name", name)
	if err != nil {
		return err
	}
	m, err := mcpFromRecord(rec)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.mcps[name] = m
	s.mu.Unlock()
	return nil
}

func (s *Store) BundleServers(b *Bundle) []*MCPServer {
	out := make([]*MCPServer, 0, len(b.Servers))
	for _, name := range b.Servers {
		if m, err := s.MCPServer(name); err == nil {
			out = append(out, m)
		}
	}
	return out
}

// BundlesContainingServer returns folders that include the named server.
func (s *Store) BundlesContainingServer(name string) []*Bundle {
	var out []*Bundle
	for _, b := range s.Bundles() {
		if b.HasServer(name) {
			out = append(out, b)
		}
	}
	return out
}

// RenderBundleMCP builds the .mcp.json body for a folder's servers.
func (s *Store) RenderBundleMCP(b *Bundle) ([]byte, error) {
	return RenderMCPConfig(s.BundleServers(b))
}
