package gitsource

import (
	"testing"
	"time"
)

func TestNewSource(t *testing.T) {
	repo := Repo{Owner: "emilkowalski", Name: "skills", Ref: "main"}
	meta := Metadata{Ref: "main", CommitSHA: "abc1234"}
	cand := Candidate{Path: "skills/foo", UpdatedAt: time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC)}

	got := NewSource("foo", repo, meta, cand)
	if got.Skill != "foo" || got.Repo != "emilkowalski/skills" ||
		got.URL != "https://github.com/emilkowalski/skills" ||
		got.Ref != "main" || got.CommitSHA != "abc1234" || got.Path != "skills/foo" {
		t.Errorf("NewSource = %+v", got)
	}
	if !got.UpdatedAt.Equal(cand.UpdatedAt) {
		t.Errorf("UpdatedAt = %v", got.UpdatedAt)
	}
	if got.ImportedAt.IsZero() {
		t.Error("ImportedAt is zero")
	}
}
