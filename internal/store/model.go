package store

import (
	"github.com/sidharthpunathil/circe/internal/model"
)

// What a skill, rule, server or folder *is* lives in internal/model. The store
// only knows where to put one.
//
// That split exists for a concrete reason: the CLI needs the types in order to
// install things, and needs no database at all. If the types lived here, every
// `circe` binary would link SQLite and the whole PocketBase runtime to render a
// folder listing, and would roughly double in size for it.
//
// Everything is re-exported below so that the rest of the server keeps spelling
// these as store.Skill and store.ValidateName. An alias is the same type, not a
// wrapper around it, so a *model.Skill and a *store.Skill are interchangeable.

type (
	// Skill is a stored skill: its parsed SKILL.md plus any bundled files.
	Skill = model.Skill
	// Bundle is a named set of everything a project needs at once.
	Bundle = model.Bundle
	// Rule is one piece of project guidance.
	Rule = model.Rule
	// MCPServer is one server definition.
	MCPServer = model.MCPServer
	// EnvVar is one environment variable a server needs before it will start.
	EnvVar = model.EnvVar
	// FileRef describes one bundled file inside a skill.
	FileRef = model.FileRef
	// Share records one shared resource.
	Share = model.Share
)

const (
	TargetClaude = model.TargetClaude
	TargetAgents = model.TargetAgents

	TransportStdio = model.TransportStdio
	TransportHTTP  = model.TransportHTTP
	TransportSSE   = model.TransportSSE
	TransportWS    = model.TransportWS

	ShareKindFolder = model.ShareKindFolder
	ShareKindSkill  = model.ShareKindSkill
)

// ValidateName reports whether s is usable as a skill or folder name.
func ValidateName(s string) error { return model.ValidateName(s) }

// Slugify converts arbitrary text into a valid name.
func Slugify(s string) string { return model.Slugify(s) }

// ParseTags splits a comma-separated tag input into a normalized list.
func ParseTags(s string) []string { return model.ParseTags(s) }

// RuleTargets lists the files a rule can be merged into.
func RuleTargets() []string { return model.RuleTargets() }

// ValidRuleTarget reports whether name is a target we write.
func ValidRuleTarget(name string) bool { return model.ValidRuleTarget(name) }

// MCPTransports lists the transports in the order the UI offers them.
func MCPTransports() []string { return model.MCPTransports() }

// ValidTransport reports whether name is a transport we can write.
func ValidTransport(name string) bool { return model.ValidTransport(name) }

// NormalizeTransport maps what people paste from vendor docs onto what we store.
func NormalizeTransport(s string) string { return model.NormalizeTransport(s) }

// MCPCategories are the buckets the MCP page groups servers by.
func MCPCategories() []string { return model.MCPCategories() }

// ValidateMCPServer checks a server definition before it is stored.
func ValidateMCPServer(m *MCPServer) error { return model.ValidateMCPServer(m) }

// RenderMCPConfig renders servers as a project's .mcp.json.
func RenderMCPConfig(servers []*MCPServer) ([]byte, error) { return model.RenderMCPConfig(servers) }

// MergeMCPConfig merges servers into an existing .mcp.json.
func MergeMCPConfig(existing []byte, servers []*MCPServer, overwrite bool) ([]byte, []string, error) {
	return model.MergeMCPConfig(existing, servers, overwrite)
}

// ParseMCPConfig reads servers out of a pasted .mcp.json.
func ParseMCPConfig(raw []byte) ([]*MCPServer, error) { return model.ParseMCPConfig(raw) }

// EncodeShareCode packs an origin and share id into one pasteable token.
func EncodeShareCode(origin, id string) string { return model.EncodeShareCode(origin, id) }

// DecodeShareCode accepts either a share code or a plain share URL.
func DecodeShareCode(input string) (origin, id string, err error) {
	return model.DecodeShareCode(input)
}
