package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/sidharthpunathil/circe/internal/store"
)

// An import can now produce three different things at once, so the code that
// writes them and the sentence that reports it afterwards are shared by every
// import path rather than rewritten in each one.

// importTally counts what one import run did.
type importTally struct {
	Skills  int
	Rules   int
	Servers int
	Skipped int
	Failed  []string
}

// Imported is how many things were actually written.
func (t importTally) Imported() int { return t.Skills + t.Rules + t.Servers }

// message renders what happened. where is an optional trailing phrase such as
// "from owner/repo", and the second return is the flash kind.
func (t importTally) message(where string) (string, string) {
	var got []string
	if t.Skills > 0 {
		got = append(got, count(t.Skills, "skill", "skills"))
	}
	if t.Rules > 0 {
		got = append(got, count(t.Rules, "rule", "rules"))
	}
	if t.Servers > 0 {
		got = append(got, count(t.Servers, "MCP server", "MCP servers"))
	}

	var parts []string
	if len(got) > 0 {
		line := "Imported " + joinList(got)
		if where != "" {
			line += " " + where
		}
		parts = append(parts, line+".")
	}
	if t.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("Skipped %d already in your registry.", t.Skipped))
	}
	if len(t.Failed) == 0 {
		if len(parts) == 0 {
			return "Nothing to do.", "ok"
		}
		return strings.Join(parts, " "), "ok"
	}
	parts = append(parts, count(len(t.Failed), "problem", "problems")+": "+strings.Join(t.Failed, "; "))
	return strings.Join(parts, " "), "error"
}

// count renders "1 rule" or "3 rules".
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// joinList renders a list the way a sentence would: "a, b and c".
func joinList(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// chosen turns the selected values of a form field into a lookup.
func chosen(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, v := range values {
		out[v] = true
	}
	return out
}

// saveFoundRule writes one discovered rule. An existing rule of the same name
// is left alone unless overwrite was chosen; the bool reports whether anything
// was written.
func (s *Server) saveFoundRule(f store.FoundRule, overwrite bool) (bool, error) {
	rule := &store.Rule{
		Name:        f.Name,
		Description: f.Description,
		Body:        normalizeNewlines(f.Body),
	}
	if store.ValidRuleTarget(f.Target) {
		rule.Targets = []string{f.Target}
	}
	if _, err := s.store.Rule(rule.Name); err == nil {
		if !overwrite {
			return false, nil
		}
		return true, s.store.SaveRule(rule)
	}
	return true, s.store.CreateRule(rule)
}

// saveFoundServer writes one discovered MCP server, with the same rule about
// what is already there.
func (s *Server) saveFoundServer(m *store.MCPServer, overwrite bool) (bool, error) {
	if _, err := s.store.MCPServer(m.Name); err == nil {
		if !overwrite {
			return false, nil
		}
		return true, s.store.SaveMCPServer(m)
	}
	return true, s.store.CreateMCPServer(m)
}

// afterImport picks where to land someone, which is wherever the thing they
// just imported now lives.
func (s *Server) afterImport(t importTally, fallback string) string {
	switch {
	case t.Skills > 0:
		return "/skills"
	case t.Rules > 0:
		return "/rules"
	case t.Servers > 0 && s.hasRoute("/mcp"):
		return "/mcp"
	}
	return fallback
}

// hasRoute reports whether a path is served. The MCP pages are being built
// separately, so an import that produced only servers checks before sending
// anyone to a page that might not exist yet.
func (s *Server) hasRoute(path string) bool {
	req := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: path}, Host: "localhost"}
	_, pattern := s.mux.Handler(req)
	return pattern != ""
}
