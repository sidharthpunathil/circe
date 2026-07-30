package gitsource

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseRepoURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Repo
	}{
		{"https", "https://github.com/emilkowalski/skills", Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}},
		{"http", "http://github.com/emilkowalski/skills", Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}},
		{"git suffix", "https://github.com/emilkowalski/skills.git", Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}},
		{"trailing slash", "https://github.com/emilkowalski/skills/", Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}},
		{"both", "https://github.com/emilkowalski/skills.git/", Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}},
		{"www", "https://www.github.com/emilkowalski/skills", Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}},
		{"surrounding space", "  https://github.com/emilkowalski/skills  ", Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}},
		{"query and fragment", "https://github.com/emilkowalski/skills?tab=readme#top", Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}},

		{"scp", "git@github.com:emilkowalski/skills.git", Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}},
		{"scp without user", "github.com:emilkowalski/skills", Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}},
		{"ssh url", "ssh://git@github.com/emilkowalski/skills.git", Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}},
		{"git protocol", "git://github.com/emilkowalski/skills.git", Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}},

		{"bare", "emilkowalski/skills", Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}},
		{"bare with host", "github.com/emilkowalski/skills", Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}},
		{"bare with git suffix", "emilkowalski/skills.git", Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}},

		{"tree ref", "https://github.com/emilkowalski/skills/tree/main", Repo{Owner: "emilkowalski", Name: "skills", Ref: "main", ProviderID: "github"}},
		{"tree ref trailing slash", "https://github.com/emilkowalski/skills/tree/v1.2.0/", Repo{Owner: "emilkowalski", Name: "skills", Ref: "v1.2.0", ProviderID: "github"}},
		{
			"tree subdirectory",
			"https://github.com/emilkowalski/skills/tree/main/skills/foo",
			Repo{Owner: "emilkowalski", Name: "skills", Ref: "main", Subpath: "skills/foo", ProviderID: "github"},
		},
		{
			"blob at a file",
			"https://github.com/emilkowalski/skills/blob/main/skills/foo/SKILL.md",
			Repo{Owner: "emilkowalski", Name: "skills", Ref: "main", Subpath: "skills/foo", ProviderID: "github"},
		},
		{
			"blob at a directory",
			"https://github.com/emilkowalski/skills/blob/main/skills/foo",
			Repo{Owner: "emilkowalski", Name: "skills", Ref: "main", Subpath: "skills/foo", ProviderID: "github"},
		},
		{
			"commit",
			"https://github.com/emilkowalski/skills/commit/9a1f2c3",
			Repo{Owner: "emilkowalski", Name: "skills", Ref: "9a1f2c3", ProviderID: "github"},
		},
		{
			// A slash in a ref is indistinguishable from a subdirectory, so the
			// first segment is the ref. Documented, deliberate.
			"slashed ref reads as ref plus subpath",
			"https://github.com/emilkowalski/skills/tree/feature/x",
			Repo{Owner: "emilkowalski", Name: "skills", Ref: "feature", Subpath: "x", ProviderID: "github"},
		},
		{
			"escaped ref",
			"https://github.com/emilkowalski/skills/tree/release%2F1.0",
			Repo{Owner: "emilkowalski", Name: "skills", Ref: "release/1.0", ProviderID: "github"},
		},
		{
			"unrelated trailing path",
			"https://github.com/emilkowalski/skills/issues/42",
			Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"},
		},
		{"dotted names", "https://github.com/some.org/my.repo.js", Repo{Owner: "some.org", Name: "my.repo.js", ProviderID: "github"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRepoURL(tc.in)
			if err != nil {
				t.Fatalf("ParseRepoURL(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseRepoURL(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseRepoURLErrors(t *testing.T) {
	cases := []struct {
		name, in, wantSubstr string
	}{
		{"empty", "", "required"},
		{"whitespace", "   ", "required"},
		{"owner only", "https://github.com/emilkowalski", "owner/repo"},
		{"no path", "https://github.com/", "owner/repo"},
		{"single word", "skills", "owner/repo"},
		{"dot owner", "https://github.com/../repo", "invalid repository"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRepoURL(tc.in)
			if err == nil {
				t.Fatalf("ParseRepoURL(%q) = nil error, want one", tc.in)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error %q does not mention %q", err, tc.wantSubstr)
			}
		})
	}
}

func TestRepoStringAndURL(t *testing.T) {
	r := Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}
	if r.String() != "emilkowalski/skills" {
		t.Errorf("String() = %q", r.String())
	}
	if r.HTMLURL() != "https://github.com/emilkowalski/skills" {
		t.Errorf("HTMLURL() = %q", r.HTMLURL())
	}
}

func TestDiscoverNestedSkills(t *testing.T) {
	files := map[string][]byte{
		"README.md":                  []byte("ignored, belongs to no skill"),
		"skills/foo/SKILL.md":        []byte(skillMD("foo", "does foo")),
		"skills/foo/references/a.md": []byte("a"),
		"skills/foo/evals/case.json": []byte("dropped"),
		"deep/nested/dir/SKILL.md":   []byte(skillMD("deep", "lives deep")),
		"deep/nested/dir/data.txt":   []byte("d"),
	}

	got := discover(files, "repo")
	if len(got) != 2 {
		t.Fatalf("discovered %d skills, want 2: %+v", len(got), got)
	}
	// Sorted by path.
	if got[0].Path != "deep/nested/dir" || got[1].Path != "skills/foo" {
		t.Fatalf("paths = %q, %q", got[0].Path, got[1].Path)
	}

	foo := got[1]
	if foo.Name != "foo" || foo.Description != "does foo" {
		t.Errorf("foo = %q / %q", foo.Name, foo.Description)
	}
	if _, ok := foo.Files["evals/case.json"]; ok {
		t.Error("evals/ at the skill root should be excluded")
	}
	if !bytes.Equal(foo.Files["references/a.md"], []byte("a")) {
		t.Error("references/a.md missing from the skill")
	}
	if got, want := len(foo.Files), 2; got != want {
		t.Errorf("foo has %d files, want %d: %v", got, want, keys(foo.Files))
	}
	if foo.Size != int64(len(skillMD("foo", "does foo"))+1) {
		t.Errorf("foo.Size = %d", foo.Size)
	}
}

// A repository that is itself a skill must not swallow the skills nested under
// it: every file belongs to its nearest enclosing SKILL.md.
func TestDiscoverRootSkillDoesNotSwallowChildren(t *testing.T) {
	files := map[string][]byte{
		"SKILL.md":            []byte(skillMD("root-skill", "the repo itself")),
		"notes.md":            []byte("mine"),
		"skills/foo/SKILL.md": []byte(skillMD("foo", "does foo")),
		"skills/foo/a.md":     []byte("theirs"),
	}

	got := discover(files, "fallback-repo-name")
	if len(got) != 2 {
		t.Fatalf("discovered %d skills, want 2", len(got))
	}
	root := got[0]
	if root.Path != "" {
		t.Fatalf("root skill path = %q, want empty", root.Path)
	}
	if !equal(keys(root.Files), []string{"SKILL.md", "notes.md"}) {
		t.Errorf("root files = %v", keys(root.Files))
	}
	if !equal(keys(got[1].Files), []string{"SKILL.md", "a.md"}) {
		t.Errorf("nested files = %v", keys(got[1].Files))
	}
}

func TestDiscoverNameFallbacks(t *testing.T) {
	files := map[string][]byte{
		// No frontmatter at all: the directory name and the first body line.
		"tools/my-tool/SKILL.md": []byte("# Title\n\nA tool that does things.\n"),
		// Frontmatter without a name.
		"tools/other/SKILL.md": []byte("---\ndescription: described\n---\n\nbody\n"),
		// Root skill with no name falls back to the repository name.
		"SKILL.md": []byte("body only\n"),
	}

	got := discover(files, "my-repo")
	byPath := map[string]Candidate{}
	for _, c := range got {
		byPath[c.Path] = c
	}
	if c := byPath["tools/my-tool"]; c.Name != "my-tool" || c.Description != "A tool that does things." {
		t.Errorf("my-tool = %q / %q", c.Name, c.Description)
	}
	if c := byPath["tools/other"]; c.Name != "other" || c.Description != "described" {
		t.Errorf("other = %q / %q", c.Name, c.Description)
	}
	if c := byPath[""]; c.Name != "my-repo" {
		t.Errorf("root name = %q, want the repository name", c.Name)
	}
}

func TestDiscoverNoSkills(t *testing.T) {
	if got := discover(map[string][]byte{"README.md": []byte("hi")}, "repo"); got != nil {
		t.Fatalf("discover found %+v in a repo with no SKILL.md", got)
	}
}

func TestCommitFromRoot(t *testing.T) {
	repo := Repo{Owner: "emilkowalski", Name: "skills", ProviderID: "github"}
	sha := "7fd1a60b01f91b314f59955a4e4d4e80d8edf11d"
	cases := []struct{ root, want string }{
		{"emilkowalski-skills-" + sha, sha},
		{"emilkowalski-skills-7fd1a60", "7fd1a60"},
		// Hyphenated names must not confuse the split.
		{"emilkowalski-skills-main", ""},
		{"skills", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := commitFromRoot(tc.root, repo); got != tc.want {
			t.Errorf("commitFromRoot(%q) = %q, want %q", tc.root, got, tc.want)
		}
	}
}

// Every supported host must parse, and carry the provider that will be used to
// fetch it. Getting the provider wrong means hitting the wrong API shape.
func TestParseRepoURLProviders(t *testing.T) {
	cases := []struct {
		name, in, wantHost, wantProvider, wantOwner, wantRepo string
	}{
		{"github", "https://github.com/owner/repo", "", "github", "owner", "repo"},
		{"github bare", "owner/repo", "", "github", "owner", "repo"},
		{"github scp", "git@github.com:owner/repo.git", "", "github", "owner", "repo"},
		{"gitlab", "https://gitlab.com/owner/repo", "gitlab.com", "gitlab", "owner", "repo"},
		{"gitlab scp", "git@gitlab.com:owner/repo.git", "gitlab.com", "gitlab", "owner", "repo"},
		{"gitlab nested group", "https://gitlab.com/group/sub/proj", "gitlab.com", "gitlab", "group/sub", "proj"},
		{"bitbucket", "https://bitbucket.org/owner/repo", "bitbucket.org", "bitbucket", "owner", "repo"},
		{"bitbucket scp", "git@bitbucket.org:owner/repo.git", "bitbucket.org", "bitbucket", "owner", "repo"},
		{"codeberg", "https://codeberg.org/owner/repo", "codeberg.org", "gitea", "owner", "repo"},
		{"self-hosted gitlab", "https://gitlab.example.com/owner/repo", "gitlab.example.com", "gitlab", "owner", "repo"},
		{"self-hosted gitea", "https://gitea.example.com/owner/repo", "gitea.example.com", "gitea", "owner", "repo"},
		{"unknown host", "https://git.example.com/owner/repo", "git.example.com", "gitea", "owner", "repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRepoURL(tc.in)
			if err != nil {
				t.Fatalf("ParseRepoURL(%q): %v", tc.in, err)
			}
			if got.Host != tc.wantHost {
				t.Errorf("host = %q, want %q", got.Host, tc.wantHost)
			}
			if got.ProviderID != tc.wantProvider {
				t.Errorf("provider = %q, want %q", got.ProviderID, tc.wantProvider)
			}
			if got.Owner != tc.wantOwner || got.Name != tc.wantRepo {
				t.Errorf("repo = %s/%s, want %s/%s", got.Owner, got.Name, tc.wantOwner, tc.wantRepo)
			}
		})
	}
}

// GitLab puts /-/ between the project and tree/blob; the ref must survive it.
func TestParseRepoURLGitLabRefAndSubpath(t *testing.T) {
	got, err := ParseRepoURL("https://gitlab.com/group/proj/-/tree/main/skills/foo")
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != "group" || got.Name != "proj" {
		t.Errorf("repo = %s/%s", got.Owner, got.Name)
	}
	if got.Ref != "main" {
		t.Errorf("ref = %q, want main", got.Ref)
	}
	if got.Subpath != "skills/foo" {
		t.Errorf("subpath = %q, want skills/foo", got.Subpath)
	}
}

// Each provider must build the archive URL its own API actually serves.
func TestProviderTarballURLs(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://github.com/o/r", "https://api.github.com/repos/o/r/tarball"},
		{"https://gitlab.com/o/r", "https://gitlab.com/api/v4/projects/o%2Fr/repository/archive.tar.gz"},
		{"https://codeberg.org/o/r", "https://codeberg.org/api/v1/repos/o/r/archive/HEAD.tar.gz"},
		{"https://bitbucket.org/o/r", "https://bitbucket.org/o/r/get/HEAD.tar.gz"},
	}
	for _, tc := range cases {
		repo, err := ParseRepoURL(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		p := repo.Provider()
		if got := p.tarballURL(p.apiBase(repo), repo); got != tc.want {
			t.Errorf("%s -> %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestProviderHTMLURLs(t *testing.T) {
	for in, want := range map[string]string{
		"https://gitlab.com/o/r":   "https://gitlab.com/o/r",
		"https://github.com/o/r":   "https://github.com/o/r",
		"https://codeberg.org/o/r": "https://codeberg.org/o/r",
	} {
		repo, err := ParseRepoURL(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got := repo.HTMLURL(); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}
