// Package skillfile parses and serializes SKILL.md files — a YAML frontmatter
// block followed by a markdown body. Round-trips are lossless: frontmatter keys
// this package does not model are preserved in Extra and written back out.
package skillfile

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ToolList is a list of tool patterns that may be authored either as a YAML
// sequence or as a single comma-separated scalar. Both forms occur in the wild
// (e.g. `allowed-tools: Bash(railway:*), Bash(npm:*)`), so the original shape is
// remembered and reproduced on write.
type ToolList struct {
	Items  []string
	Inline bool // authored as one comma-separated scalar rather than a sequence
}

func (t *ToolList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		t.Inline = true
		t.Items = splitInlineTools(node.Value)
		return nil
	case yaml.SequenceNode:
		t.Inline = false
		return node.Decode(&t.Items)
	default:
		return fmt.Errorf("allowed-tools: unsupported YAML kind %v", node.Kind)
	}
}

func (t ToolList) MarshalYAML() (any, error) {
	if t.Inline {
		return strings.Join(t.Items, ", "), nil
	}
	return t.Items, nil
}

func (t ToolList) Len() int { return len(t.Items) }

// splitInlineTools splits on commas that are not nested inside parentheses, so
// that patterns like `Bash(railway:*)` survive intact.
func splitInlineTools(s string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				if v := strings.TrimSpace(cur.String()); v != "" {
					out = append(out, v)
				}
				cur.Reset()
				continue
			}
		}
		cur.WriteRune(r)
	}
	if v := strings.TrimSpace(cur.String()); v != "" {
		out = append(out, v)
	}
	return out
}

// Frontmatter holds the parsed YAML header of a SKILL.md.
//
// The official validator allows exactly: name, description, license,
// allowed-tools, metadata and compatibility. Real libraries also carry
// argument-hint, user-invokable and others, so unknown keys are preserved in
// Extra rather than rejected.
type Frontmatter struct {
	Name          string         `yaml:"name"`
	Description   string         `yaml:"description"`
	Version       string         `yaml:"version,omitempty"`
	License       string         `yaml:"license,omitempty"`
	Compatibility string         `yaml:"compatibility,omitempty"`
	AllowedTools  ToolList       `yaml:"allowed-tools,omitempty"`
	Metadata      map[string]any `yaml:"metadata,omitempty"`

	// Extra carries any frontmatter key not modeled above so that editing a
	// skill through the UI never silently drops fields authored by hand.
	Extra map[string]any `yaml:"-"`
}

// File is a fully parsed SKILL.md.
type File struct {
	Frontmatter
	Body string
}

var (
	// Opening delimiter must be the very first thing in the file. The closing
	// delimiter is the first line that is exactly --- (or ...) on its own.
	fmPattern = regexp.MustCompile(`(?s)\A---[ \t]*\r?\n(.*?)\r?\n(?:---|\.\.\.)[ \t]*(?:\r?\n|\z)`)

	knownKeys = map[string]bool{
		"name": true, "description": true, "version": true,
		"license": true, "allowed-tools": true, "metadata": true,
		"compatibility": true,
	}

	// officialKeys is the set accepted by Anthropic's own validator. Keys
	// outside it load fine but are reported as warnings.
	officialKeys = map[string]bool{
		"name": true, "description": true, "license": true,
		"allowed-tools": true, "metadata": true, "compatibility": true,
	}

	nameRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

// Limits enforced by the official validator.
const (
	MaxNameLen          = 64
	MaxDescriptionLen   = 1024
	MaxCompatibilityLen = 500
)

// Problem is a validation finding. Fatal problems make a skill invalid; the
// rest are advisory.
type Problem struct {
	Field   string `json:"field"`
	Message string `json:"message"`
	Fatal   bool   `json:"fatal"`
}

// Validate checks a parsed skill against the official rules plus the
// conventions observed across real skill libraries.
func (f *File) Validate() []Problem {
	var ps []Problem
	fatal := func(field, msg string) { ps = append(ps, Problem{field, msg, true}) }
	warn := func(field, msg string) { ps = append(ps, Problem{field, msg, false}) }

	switch {
	case strings.TrimSpace(f.Name) == "":
		fatal("name", "name is required")
	case len(f.Name) > MaxNameLen:
		fatal("name", fmt.Sprintf("name must be %d characters or fewer", MaxNameLen))
	case !nameRe.MatchString(f.Name):
		fatal("name", "name must be lowercase letters, digits and single hyphens")
	}

	desc := strings.TrimSpace(f.Description)
	switch {
	case desc == "":
		fatal("description", "description is required")
	case len(f.Description) > MaxDescriptionLen:
		fatal("description", fmt.Sprintf("description must be %d characters or fewer (currently %d)", MaxDescriptionLen, len(f.Description)))
	case strings.ContainsAny(f.Description, "<>"):
		fatal("description", "description must not contain < or > characters")
	}
	if desc != "" && len(desc) < 40 {
		warn("description", "a short description gives the model little to match on; describe what it does and when to use it")
	}

	if len(f.Compatibility) > MaxCompatibilityLen {
		fatal("compatibility", fmt.Sprintf("compatibility must be %d characters or fewer", MaxCompatibilityLen))
	}

	for k := range f.Extra {
		if !officialKeys[k] {
			warn(k, fmt.Sprintf("%q is not an officially supported frontmatter key; it is preserved but may be ignored", k))
		}
	}

	if lines := strings.Count(f.Body, "\n"); lines > 500 {
		warn("body", fmt.Sprintf("SKILL.md is %d lines; under 500 is recommended. Move detail into reference files", lines))
	}
	if strings.TrimSpace(f.Body) == "" {
		warn("body", "the body is empty; the skill will provide no instructions")
	}
	return ps
}

// Valid reports whether the skill has no fatal problems.
func (f *File) Valid() bool {
	for _, p := range f.Validate() {
		if p.Fatal {
			return false
		}
	}
	return true
}

// Parse reads a SKILL.md. A file without frontmatter is not an error: the whole
// content becomes the body, and the caller can fall back to the directory name.
func Parse(src []byte) (*File, error) {
	src = bytes.TrimPrefix(src, []byte("\xef\xbb\xbf")) // strip UTF-8 BOM

	m := fmPattern.FindSubmatchIndex(src)
	if m == nil {
		return &File{Body: string(normalizeNewlines(src))}, nil
	}

	rawYAML := src[m[2]:m[3]]
	body := src[m[1]:]

	f := &File{Body: strings.TrimLeft(string(normalizeNewlines(body)), "\n")}
	if err := yaml.Unmarshal(rawYAML, &f.Frontmatter); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	// Second pass into a generic map to capture keys the struct does not model.
	var all map[string]any
	if err := yaml.Unmarshal(rawYAML, &all); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	for k, v := range all {
		if !knownKeys[k] {
			if f.Extra == nil {
				f.Extra = map[string]any{}
			}
			f.Extra[k] = v
		}
	}

	// Some skills misspell the key with an underscore and author it as a
	// sequence. Fold it into the canonical field so consumers see one shape.
	if f.AllowedTools.Len() == 0 {
		if v, ok := f.Extra["allowed_tools"]; ok {
			if items := toStringSlice(v); len(items) > 0 {
				f.AllowedTools = ToolList{Items: items}
				delete(f.Extra, "allowed_tools")
			}
		}
	}

	return f, nil
}

func toStringSlice(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		return splitInlineTools(t)
	}
	return nil
}

// Bytes serializes back to SKILL.md form. Known keys are written in a stable
// canonical order, then extras alphabetically, so diffs stay readable.
func (f *File) Bytes() []byte {
	var buf bytes.Buffer
	buf.WriteString("---\n")

	// yaml.v3 sorts map keys, so emit one key at a time to control ordering.
	writeKey := func(k string, v any) {
		if v == nil {
			return
		}
		b, err := yaml.Marshal(map[string]any{k: v})
		if err != nil {
			return
		}
		buf.Write(b)
	}

	writeKey("name", nonEmpty(f.Name))
	writeKey("description", nonEmpty(f.Description))
	writeKey("version", nonEmpty(f.Version))
	writeKey("license", nonEmpty(f.License))
	writeKey("compatibility", nonEmpty(f.Compatibility))
	if f.AllowedTools.Len() > 0 {
		writeKey("allowed-tools", f.AllowedTools)
	}
	if len(f.Metadata) > 0 {
		writeKey("metadata", f.Metadata)
	}

	extraKeys := make([]string, 0, len(f.Extra))
	for k := range f.Extra {
		extraKeys = append(extraKeys, k)
	}
	sort.Strings(extraKeys)
	for _, k := range extraKeys {
		writeKey(k, f.Extra[k])
	}

	buf.WriteString("---\n\n")
	buf.WriteString(strings.TrimRight(f.Body, "\n"))
	buf.WriteString("\n")
	return buf.Bytes()
}

func nonEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func normalizeNewlines(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

// Summary returns the first meaningful line of the body, used as a preview when
// a skill has no description.
func (f *File) Summary(max int) string {
	for _, line := range strings.Split(f.Body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "---") {
			continue
		}
		return truncate(line, max)
	}
	return ""
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimRight(string(r[:max]), " ,.;:") + "…"
}
