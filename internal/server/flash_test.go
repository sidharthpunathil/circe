package server

import "testing"

func TestSplitFirstSentence(t *testing.T) {
	cases := []struct{ in, lead, rest string }{
		{"Saved. New tokens are in use straight away.", "Saved.", "New tokens are in use straight away."},
		{"Saved.", "Saved.", ""},
		{"", "", ""},
		{"e.g. something failed", "e.g. something failed", ""},
		{"i.e. the token expired. Sign in again.", "i.e. the token expired.", "Sign in again."},
		{"Vol. 2 could not be parsed.", "Vol. 2 could not be parsed.", ""},
		{`There is no folder called "a. b".`, `There is no folder called "a. b".`, ""},
		{"3.5 skills imported", "3.5 skills imported", ""},
		{"open /x/.claude/SKILL.md: no such file", "open /x/.claude/SKILL.md: no such file", ""},
		{"Imported 3 skills. Skipped 12 already in your registry.", "Imported 3 skills.", "Skipped 12 already in your registry."},
	}
	for _, c := range cases {
		lead, rest := splitFirstSentence(c.in)
		if lead != c.lead || rest != c.rest {
			t.Errorf("splitFirstSentence(%q)\n got lead=%q rest=%q\nwant lead=%q rest=%q", c.in, lead, rest, c.lead, c.rest)
		}
	}
}
