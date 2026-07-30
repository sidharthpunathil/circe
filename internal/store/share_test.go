package store

import (
	"strings"
	"testing"
)

func TestShareCodeRoundTrip(t *testing.T) {
	code := EncodeShareCode("https://skills.example.com", "abc123xyz0")
	if !strings.HasPrefix(code, "sb_") {
		t.Errorf("code should be prefixed: %q", code)
	}
	origin, id, err := DecodeShareCode(code)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "https://skills.example.com" || id != "abc123xyz0" {
		t.Errorf("got origin=%q id=%q", origin, id)
	}
}

func TestDecodeShareURL(t *testing.T) {
	cases := []struct{ in, origin, id string }{
		{"https://skills.example.com/s/abc123", "https://skills.example.com", "abc123"},
		{"https://skills.example.com/s/abc123/", "https://skills.example.com", "abc123"},
		{"http://localhost:8080/s/xyz", "http://localhost:8080", "xyz"},
		{"skills.example.com/s/abc123", "https://skills.example.com", "abc123"},
	}
	for _, c := range cases {
		origin, id, err := DecodeShareCode(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if origin != c.origin || id != c.id {
			t.Errorf("%s -> origin=%q id=%q, want %q/%q", c.in, origin, id, c.origin, c.id)
		}
	}
}

func TestDecodeShareCodeRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "   ", "hello", "sb_!!!!", "sb_", "https://example.com"} {
		if _, _, err := DecodeShareCode(in); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

func TestShareLifecycle(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sk := &Skill{Name: "shared-skill", Description: "A skill used to exercise the sharing lifecycle.", Body: "x\n"}
	if err := st.CreateSkill(sk); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateBundle(&Bundle{Name: "pack", Skills: []string{"shared-skill"}}); err != nil {
		t.Fatal(err)
	}

	sh, err := st.CreateShare(ShareKindFolder, "pack")
	if err != nil {
		t.Fatal(err)
	}
	if len(sh.ID) != 10 {
		t.Errorf("id = %q, want 10 chars", sh.ID)
	}

	// Sharing again must not mint a new id, or links already sent would break.
	again, err := st.CreateShare(ShareKindFolder, "pack")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != sh.ID {
		t.Errorf("re-share changed id: %q -> %q", sh.ID, again.ID)
	}

	if got, ok := st.ShareByID(sh.ID); !ok || got.Name != "pack" {
		t.Errorf("lookup failed: %+v ok=%v", got, ok)
	}

	if err := st.RevokeShare(ShareKindFolder, "pack"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.ShareByID(sh.ID); ok {
		t.Errorf("revoked share still resolves")
	}
}

// Anything not explicitly shared must be invisible to the public endpoint.
func TestUnsharedResourceIsNotResolvable(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sk := &Skill{Name: "private-skill", Description: "Never shared, so it must not be publicly reachable.", Body: "x\n"}
	if err := st.CreateSkill(sk); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.ShareFor(ShareKindSkill, "private-skill"); ok {
		t.Errorf("unshared skill reported as shared")
	}
}

func TestCreateShareRejectsMissingResource(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateShare(ShareKindFolder, "nope"); err == nil {
		t.Errorf("expected error for missing folder")
	}
	if _, err := st.CreateShare("bogus", "x"); err == nil {
		t.Errorf("expected error for unknown kind")
	}
}

func TestShareIDsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id, err := newShareID()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q after %d draws", id, i)
		}
		seen[id] = true
	}
}
