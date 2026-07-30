package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newRuleStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestRuleRoundTrip(t *testing.T) {
	st := newRuleStore(t)
	r := &Rule{
		Name:        "code-style",
		Description: "How code in this project should be written.",
		Body:        "Use tabs.\n",
		Targets:     []string{TargetClaude, TargetAgents},
		Tags:        []string{"style"},
	}
	if err := st.CreateRule(r); err != nil {
		t.Fatal(err)
	}

	// A rule is one plain file, discoverable without the app.
	path := filepath.Join(st.Root(), "rules", "code-style.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected %s: %v", path, err)
	}
	if !strings.Contains(string(raw), "name: code-style") {
		t.Errorf("frontmatter missing:\n%s", raw)
	}

	reopened, err := Open(st.Root())
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Rule("code-style")
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != r.Description || strings.TrimSpace(got.Body) != "Use tabs." {
		t.Errorf("round trip lost content: %+v", got)
	}
	if len(got.Targets) != 2 {
		t.Errorf("targets = %v, want both", got.Targets)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "style" {
		t.Errorf("tags = %v", got.Tags)
	}
}

// A rule with no explicit target belongs in CLAUDE.md, which is what almost
// everyone means.
func TestRuleDefaultsToClaudeMd(t *testing.T) {
	r := &Rule{Name: "x"}
	if got := r.TargetsOrDefault(); len(got) != 1 || got[0] != TargetClaude {
		t.Errorf("default targets = %v", got)
	}
	if !r.Writes(TargetClaude) || r.Writes(TargetAgents) {
		t.Errorf("default rule should write CLAUDE.md only")
	}
}

func TestRuleValidation(t *testing.T) {
	st := newRuleStore(t)
	if err := st.CreateRule(&Rule{Name: "no-desc", Body: "x"}); err == nil {
		t.Error("expected an error without a description")
	}
	if err := st.CreateRule(&Rule{Name: "no-body", Description: "A description long enough."}); err == nil {
		t.Error("expected an error without a body")
	}
	if err := st.CreateRule(&Rule{Name: "Bad Name", Description: "d", Body: "b"}); err == nil {
		t.Error("expected an error for an invalid name")
	}
}

func TestRuleSetLifecycle(t *testing.T) {
	st := newRuleStore(t)
	for _, n := range []string{"one", "two"} {
		if err := st.CreateRule(&Rule{Name: n, Description: "Rule " + n + " for testing.", Body: "# " + n + "\n"}); err != nil {
			t.Fatal(err)
		}
	}
	set := &RuleSet{Name: "house", Title: "House Style", Rules: []string{"one", "two", "ghost"}}
	if err := st.CreateRuleSet(set); err != nil {
		t.Fatal(err)
	}
	// A reference to a rule that does not exist is dropped rather than stored.
	if len(set.Rules) != 2 {
		t.Errorf("rules = %v, want the ghost dropped", set.Rules)
	}

	// Deleting a rule removes it from every folder holding it.
	if err := st.DeleteRule("one"); err != nil {
		t.Fatal(err)
	}
	got, err := st.RuleSet("house")
	if err != nil {
		t.Fatal(err)
	}
	if got.Has("one") {
		t.Errorf("deleted rule still referenced: %v", got.Rules)
	}

	// Deleting the folder leaves the rules alone.
	if err := st.DeleteRuleSet("house"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Rule("two"); err != nil {
		t.Errorf("deleting a folder should not delete its rules: %v", err)
	}
}

func TestRenameRuleUpdatesFolders(t *testing.T) {
	st := newRuleStore(t)
	if err := st.CreateRule(&Rule{Name: "old", Description: "A rule to be renamed.", Body: "x\n"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRuleSet(&RuleSet{Name: "set", Rules: []string{"old"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.RenameRule("old", "new"); err != nil {
		t.Fatal(err)
	}
	set, _ := st.RuleSet("set")
	if !set.Has("new") || set.Has("old") {
		t.Errorf("folder not repointed: %v", set.Rules)
	}
	if r, err := st.Rule("new"); err != nil || r.Name != "new" {
		t.Errorf("renamed rule = %+v err=%v", r, err)
	}
}

// Rendering is what the CLI writes; each target gets only the rules aimed at it,
// joined in folder order so the author controls what the agent reads first.
func TestRenderRuleSetSplitsByTarget(t *testing.T) {
	st := newRuleStore(t)
	st.CreateRule(&Rule{Name: "both", Description: "Applies everywhere.", Body: "BOTH\n",
		Targets: []string{TargetClaude, TargetAgents}})
	st.CreateRule(&Rule{Name: "agents-only", Description: "Only for agents.", Body: "AGENTS\n",
		Targets: []string{TargetAgents}})
	st.CreateRule(&Rule{Name: "claude-default", Description: "Defaults to Claude.", Body: "CLAUDE\n"})

	set := &RuleSet{Name: "all", Rules: []string{"both", "agents-only", "claude-default"}}
	if err := st.CreateRuleSet(set); err != nil {
		t.Fatal(err)
	}
	out := st.RenderRuleSet(set)

	claude := out[TargetClaude]
	if !strings.Contains(claude, "BOTH") || !strings.Contains(claude, "CLAUDE") {
		t.Errorf("CLAUDE.md = %q", claude)
	}
	if strings.Contains(claude, "AGENTS") {
		t.Errorf("CLAUDE.md picked up an agents-only rule: %q", claude)
	}
	agents := out[TargetAgents]
	if !strings.Contains(agents, "BOTH") || !strings.Contains(agents, "AGENTS") {
		t.Errorf("AGENTS.md = %q", agents)
	}
	if strings.Contains(agents, "CLAUDE\n") && !strings.Contains(agents, "BOTH") {
		t.Errorf("AGENTS.md picked up a claude-only rule: %q", agents)
	}
}

func TestSearchRules(t *testing.T) {
	st := newRuleStore(t)
	st.CreateRule(&Rule{Name: "testing", Description: "How we write tests here.", Body: "Table driven.\n"})
	st.CreateRule(&Rule{Name: "naming", Description: "Naming conventions.", Body: "Short names.\n"})

	if got := st.SearchRules("test"); len(got) != 1 || got[0].Name != "testing" {
		t.Errorf("search by name = %v", got)
	}
	if got := st.SearchRules("table"); len(got) != 1 {
		t.Errorf("search by body = %v", got)
	}
	if got := st.SearchRules(""); len(got) != 2 {
		t.Errorf("empty query should return all, got %v", got)
	}
}
