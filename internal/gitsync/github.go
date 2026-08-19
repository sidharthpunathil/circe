package gitsync

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sidharthpunathil/circe/internal/gitsource"
)

// Writing to GitHub goes through the Git Data API: upload each file as a blob,
// assemble the blobs into a tree, hang a commit off that tree, then move the
// branch. It is more calls than `git push`, but it needs no git binary, no
// working copy and no writable disk, which is what a `scratch` image has.

// apiBase is a variable rather than a constant only so tests can point the
// client at a stand-in for GitHub.
var apiBase = "https://api.github.com"

const (
	requestTimeout = 60 * time.Second
	maxErrorBody   = 32 << 10
	// maxTreeEntries bounds a single sync. A registry is a personal library,
	// not a monorepo, and a repository large enough to exceed this would also
	// have a truncated tree listing, which would silently drop files.
	maxTreeEntries = 10000
)

type client struct {
	owner, name string
	token       string
	http        *http.Client
}

func newClient(repo string) (*client, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return nil, fmt.Errorf("repository %q is not owner/name", repo)
	}
	token := gitsource.EnvToken()
	if token == "" {
		return nil, fmt.Errorf("set a GitHub token on the settings page before syncing; " +
			"writing commits needs one, and it must have Contents: read and write on this repository")
	}
	return &client{
		owner: owner, name: name, token: token,
		http: &http.Client{Timeout: requestTimeout},
	}, nil
}

func (c *client) url(format string, args ...any) string {
	return fmt.Sprintf(apiBase+"/repos/"+c.owner+"/"+c.name+format, args...)
}

// do performs one request and decodes the response into out.
func (c *client) do(ctx context.Context, method, url string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := jsonBody(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(resp, method, url)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// errNotFound is returned for a 404, which during a push is the normal answer
// for "this branch does not exist yet" rather than a failure.
var errNotFound = fmt.Errorf("not found")

// apiError turns a non-2xx response into a message written for the person who
// pressed the button.
func apiError(resp *http.Response, method, url string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	var body struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &body)

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("GitHub rejected the token. Check it has not expired")
	case http.StatusForbidden:
		if strings.Contains(strings.ToLower(body.Message), "rate limit") {
			return fmt.Errorf("GitHub rate limit reached; try again shortly")
		}
		return fmt.Errorf("the token cannot write to this repository. " +
			"A fine-grained token needs Contents: read and write on it")
	case http.StatusConflict, http.StatusUnprocessableEntity:
		if body.Message != "" {
			return fmt.Errorf("GitHub refused the update: %s. "+
				"This usually means someone else pushed since the last sync; pull first", body.Message)
		}
	}
	if body.Message != "" {
		return fmt.Errorf("GitHub said %d: %s", resp.StatusCode, body.Message)
	}
	return fmt.Errorf("GitHub said %d for %s %s", resp.StatusCode, method, url)
}

// ---------- git data ----------

type commitRef struct {
	SHA  string `json:"sha"`
	Tree struct {
		SHA string `json:"sha"`
	} `json:"tree"`
}

// headCommit returns the commit a branch points at, or nil when the branch (or
// the whole repository) has no commits yet.
func (c *client) headCommit(ctx context.Context, branch string) (*commitRef, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := c.do(ctx, http.MethodGet, c.url("/git/ref/heads/%s", branch), nil, &ref)
	if err == errNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var commit commitRef
	if err := c.do(ctx, http.MethodGet, c.url("/git/commits/%s", ref.Object.SHA), nil, &commit); err != nil {
		return nil, err
	}
	commit.SHA = ref.Object.SHA
	return &commit, nil
}

type treeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// tree lists a commit's tree recursively.
func (c *client) tree(ctx context.Context, sha string) ([]treeEntry, error) {
	var out struct {
		Tree      []treeEntry `json:"tree"`
		Truncated bool        `json:"truncated"`
	}
	if err := c.do(ctx, http.MethodGet, c.url("/git/trees/%s?recursive=1", sha), nil, &out); err != nil {
		if err == errNotFound {
			return nil, nil
		}
		return nil, err
	}
	if out.Truncated {
		// Carrying through a partial listing would delete every file the
		// listing did not reach, which is the worst possible failure mode.
		return nil, fmt.Errorf("the repository is too large to sync safely: " +
			"GitHub truncated its file listing, and continuing would drop the files it left out")
	}
	return out.Tree, nil
}

// blobID is the object id git would give this content: the SHA-1 of
// "blob <len>\0<content>". Knowing it locally means an unchanged file can be
// referenced by id rather than uploaded again, which turns a push that changes
// nothing into three requests instead of one per file.
func blobID(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d", len(data))
	h.Write([]byte{0})
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// createBlob uploads one file's contents and returns its object id.
func (c *client) createBlob(ctx context.Context, data []byte) (string, error) {
	// Base64 rather than utf-8, so a font or an image in a skill survives.
	body := map[string]string{
		"content":  base64.StdEncoding.EncodeToString(data),
		"encoding": "base64",
	}
	var out struct {
		SHA string `json:"sha"`
	}
	if err := c.do(ctx, http.MethodPost, c.url("/git/blobs"), body, &out); err != nil {
		return "", err
	}
	return out.SHA, nil
}

// createTree assembles entries into a tree. base_tree is deliberately omitted:
// the tree is the complete desired state, which is what makes a deletion here
// become a deletion there.
func (c *client) createTree(ctx context.Context, entries []treeEntry) (string, error) {
	if len(entries) > maxTreeEntries {
		return "", fmt.Errorf("this registry has %d files, more than the %d a single sync will write",
			len(entries), maxTreeEntries)
	}
	var out struct {
		SHA string `json:"sha"`
	}
	body := map[string]any{"tree": entries}
	if err := c.do(ctx, http.MethodPost, c.url("/git/trees"), body, &out); err != nil {
		return "", err
	}
	return out.SHA, nil
}

func (c *client) createCommit(ctx context.Context, message, tree string, parents []string) (string, error) {
	if parents == nil {
		parents = []string{}
	}
	body := map[string]any{"message": message, "tree": tree, "parents": parents}
	var out struct {
		SHA string `json:"sha"`
	}
	if err := c.do(ctx, http.MethodPost, c.url("/git/commits"), body, &out); err != nil {
		return "", err
	}
	return out.SHA, nil
}

// setRef moves the branch. create is set when the branch does not exist yet.
//
// The update is not forced: the commit's parent is the head that was read at
// the start, so a rejected update means someone else pushed in between, and
// overwriting them is the one thing this must not do.
func (c *client) setRef(ctx context.Context, branch, sha string, create bool) error {
	if create {
		body := map[string]string{"ref": "refs/heads/" + branch, "sha": sha}
		return c.do(ctx, http.MethodPost, c.url("/git/refs"), body, nil)
	}
	body := map[string]any{"sha": sha, "force": false}
	return c.do(ctx, http.MethodPatch, c.url("/git/refs/heads/%s", branch), body, nil)
}
