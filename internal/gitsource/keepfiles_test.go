package gitsource

import "testing"

// A project's MCP servers are configured in a hidden file, so the rule that
// drops hidden entries has to make room for it or an import can never find one.
func TestExcludedKeepsMCPConfig(t *testing.T) {
	cases := map[string]bool{
		".mcp.json":            false,
		"tools/.mcp.json":      false,
		"mcp.json":             false,
		".claude/.mcp.json":    true, // inside a hidden directory, still dropped
		".gitignore":           true,
		".env":                 true,
		"node_modules/.mcp.js": true,
	}
	for in, want := range cases {
		if got := excluded(in); got != want {
			t.Errorf("excluded(%q) = %v, want %v", in, got, want)
		}
	}
}
