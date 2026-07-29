package gitsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.github.com"
	// GitHub asks every client to identify itself and to pin the API version;
	// unversioned requests are free to change shape under us.
	userAgent  = "skillbuilder"
	apiVersion = "2022-11-28"

	requestTimeout = 90 * time.Second // a large repository tarball is slow
	// maxCommitLookups bounds the per-skill commit requests in flight. These are
	// pure garnish, so they must not look like a burst to GitHub's abuse
	// detection or stall the import behind dozens of sockets.
	maxCommitLookups = 5
	// maxErrorBody caps how much of a non-2xx body is read looking for GitHub's
	// own message — an error page is not worth megabytes.
	maxErrorBody = 32 << 10
)

// Client talks to the GitHub REST API.
//
// The zero value is usable: BaseURL, HTTP and Token all fall back to sensible
// defaults, and BaseURL exists mainly so tests can point at an httptest server.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	Token   string
}

// NewClient returns a client authenticated from the environment when possible.
func NewClient() *Client {
	return &Client{
		BaseURL: defaultBaseURL,
		HTTP:    &http.Client{Timeout: requestTimeout},
		Token:   EnvToken(),
	}
}

// EnvToken returns the GitHub token from the environment. GITHUB_TOKEN is the
// Actions convention and GH_TOKEN the gh CLI's, and users have one or the other
// set far more often than they realize.
func EnvToken() string {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return defaultBaseURL
	}
	return strings.TrimSuffix(c.BaseURL, "/")
}

func (c *Client) http() *http.Client {
	if c.HTTP == nil {
		return &http.Client{Timeout: requestTimeout}
	}
	return c.HTTP
}

func (c *Client) token() string {
	if c.Token == "" {
		return EnvToken()
	}
	return c.Token
}

// APIError is a non-2xx response from GitHub. Its message is written for the
// person who pasted the URL, not for a log file.
type APIError struct {
	Status  int
	URL     string
	Message string // GitHub's own explanation, when it sent one

	// RateLimitReset is when the quota refills, from X-RateLimit-Reset.
	RateLimitReset time.Time
	rateLimited    bool
}

func (e *APIError) Error() string {
	switch {
	case e.Status == http.StatusNotFound:
		return "repository not found or private — set GITHUB_TOKEN to access private repos"
	case e.rateLimited:
		if e.RateLimitReset.IsZero() {
			return "GitHub rate limit reached — set GITHUB_TOKEN to raise it"
		}
		return fmt.Sprintf("GitHub rate limit reached, resets at %s — set GITHUB_TOKEN to raise it",
			e.RateLimitReset.Local().Format("15:04 MST"))
	case e.Status == http.StatusUnauthorized:
		return "GitHub rejected the credentials — check GITHUB_TOKEN"
	case e.Message != "":
		return fmt.Sprintf("GitHub returned %d: %s", e.Status, e.Message)
	default:
		return fmt.Sprintf("GitHub returned %d for %s", e.Status, e.URL)
	}
}

// IsNotFound reports whether err is a 404 from GitHub.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// IsRateLimited reports whether err is GitHub refusing on quota grounds.
func IsRateLimited(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.rateLimited
}

// get issues an authenticated GET and turns anything but a 2xx into an
// *APIError, so callers only ever see a body worth reading.
func (c *Client) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", u, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := c.token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", u, err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, apiErrorFrom(resp, u)
	}
	return resp, nil
}

func apiErrorFrom(resp *http.Response, u string) *APIError {
	e := &APIError{Status: resp.StatusCode, URL: u}
	if b, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody)); err == nil && len(b) > 0 {
		var wire struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(b, &wire) == nil {
			e.Message = wire.Message
		}
	}

	// GitHub answers an exhausted quota with 403 (primary limit) or 429
	// (secondary limit) and only distinguishes it from a permissions failure
	// through the headers and the message.
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		remaining := resp.Header.Get("X-RateLimit-Remaining")
		e.rateLimited = remaining == "0" ||
			strings.Contains(strings.ToLower(e.Message), "rate limit") ||
			resp.StatusCode == http.StatusTooManyRequests
	}
	if sec, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && sec > 0 {
		e.RateLimitReset = time.Unix(sec, 0)
	}
	return e
}

// repoJSON is the subset of GET /repos/{owner}/{repo} that the UI shows.
type repoJSON struct {
	FullName        string    `json:"full_name"`
	Description     string    `json:"description"`
	DefaultBranch   string    `json:"default_branch"`
	PushedAt        time.Time `json:"pushed_at"`
	StargazersCount int       `json:"stargazers_count"`
	HTMLURL         string    `json:"html_url"`
}

// repoMetadata reads the repository's description, default branch, push time
// and star count. The returned Metadata is usable even when the error is
// non-nil — it is then simply empty.
func (c *Client) repoMetadata(ctx context.Context, repo Repo) (Metadata, error) {
	u := fmt.Sprintf("%s/repos/%s/%s", c.base(), url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	resp, err := c.get(ctx, u)
	if err != nil {
		return Metadata{}, err
	}
	defer resp.Body.Close()

	var r repoJSON
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return Metadata{}, fmt.Errorf("decode repository metadata from %s: %w", u, err)
	}
	return Metadata{
		FullName:    r.FullName,
		Description: strings.TrimSpace(r.Description),
		Ref:         r.DefaultBranch,
		UpdatedAt:   r.PushedAt,
		Stars:       r.StargazersCount,
		HTMLURL:     r.HTMLURL,
	}, nil
}

// commitJSON is the subset of a commit list entry we need.
type commitJSON struct {
	SHA    string `json:"sha"`
	Commit struct {
		Author    struct{ Date time.Time } `json:"author"`
		Committer struct{ Date time.Time } `json:"committer"`
	} `json:"commit"`
}

// lastCommitTime returns when the given repository path was last touched.
func (c *Client) lastCommitTime(ctx context.Context, repo Repo, ref, repoPath string) (time.Time, error) {
	q := url.Values{"per_page": {"1"}}
	if repoPath != "" {
		q.Set("path", repoPath)
	}
	if ref != "" {
		q.Set("sha", ref)
	}
	u := fmt.Sprintf("%s/repos/%s/%s/commits?%s",
		c.base(), url.PathEscape(repo.Owner), url.PathEscape(repo.Name), q.Encode())

	resp, err := c.get(ctx, u)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()

	var commits []commitJSON
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&commits); err != nil {
		return time.Time{}, fmt.Errorf("decode commits from %s: %w", u, err)
	}
	if len(commits) == 0 {
		return time.Time{}, nil
	}
	if t := commits[0].Commit.Committer.Date; !t.IsZero() {
		return t, nil
	}
	return commits[0].Commit.Author.Date, nil
}
