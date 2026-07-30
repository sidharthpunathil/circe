package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sidharthpunathil/skillbuilder/internal/api"
)

const (
	apiPrefix      = "/api/" + api.Version
	userAgent      = "skillbuilder-cli"
	requestTimeout = 60 * time.Second
	// maxErrorBody caps how much of a non-2xx body is read before giving up on
	// finding an api.Error in it — a misrouted request can return a whole HTML
	// page and none of it belongs in a CLI error message.
	maxErrorBody = 64 << 10
)

// Client talks to a skillbuilder registry.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// Token authenticates against a password-protected registry.
	Token string
}

// NewClient returns a client for a registry base URL.
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: NormalizeRegistry(baseURL),
		HTTP:    &http.Client{Timeout: requestTimeout},
	}
}

// UnreachableError means the request never got as far as an HTTP response.
type UnreachableError struct {
	Registry string
	Err      error
}

func (e *UnreachableError) Error() string {
	return fmt.Sprintf("cannot reach registry at %s — is the server running?", e.Registry)
}

func (e *UnreachableError) Unwrap() error { return e.Err }

// UnauthorizedError means the registry requires credentials we did not supply.
type UnauthorizedError struct{ Registry string }

func (e *UnauthorizedError) Error() string {
	return fmt.Sprintf("%s requires a token — set $%s or run `skillbuilder config set token <token>`",
		e.Registry, EnvToken)
}

// APIError is a non-2xx response. Message carries the server's own explanation
// when it sent one.
type APIError struct {
	Status  int
	Code    string
	Message string
	URL     string
}

func (e *APIError) Error() string {
	detail := e.Message
	if detail == "" {
		detail = e.Code
	}
	if detail == "" {
		detail = strings.ToLower(http.StatusText(e.Status))
	}
	return fmt.Sprintf("registry returned %d: %s", e.Status, detail)
}

// IsNotFound reports whether err is a 404 from the registry.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// ListSkills returns registry skills, optionally filtered by query and tag.
func (c *Client) ListSkills(ctx context.Context, query, tag string) (*api.SkillListResponse, error) {
	q := url.Values{}
	if query != "" {
		q.Set("q", query)
	}
	if tag != "" {
		q.Set("tag", tag)
	}
	return getJSON[api.SkillListResponse](ctx, c, apiPrefix+"/skills", q)
}

// GetSkill returns one skill including its body.
func (c *Client) GetSkill(ctx context.Context, name string) (*api.Skill, error) {
	return getJSON[api.Skill](ctx, c, apiPrefix+"/skills/"+url.PathEscape(name), nil)
}

// ListBundles returns every bundle in the registry.
func (c *Client) ListBundles(ctx context.Context) (*api.BundleListResponse, error) {
	return getJSON[api.BundleListResponse](ctx, c, apiPrefix+"/bundles", nil)
}

// GetBundle returns a bundle with its skills expanded.
func (c *Client) GetBundle(ctx context.Context, name string) (*api.BundleManifest, error) {
	return getJSON[api.BundleManifest](ctx, c, apiPrefix+"/bundles/"+url.PathEscape(name), nil)
}

// BundleArchive streams a bundle as a gzipped tar. The caller closes the reader.
func (c *Client) BundleArchive(ctx context.Context, name string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, apiPrefix+"/bundles/"+url.PathEscape(name)+"/archive.tar.gz", nil, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Download streams the named skills as a gzipped tar. The caller closes the
// reader.
func (c *Client) Download(ctx context.Context, names []string) (io.ReadCloser, error) {
	if len(names) == 0 {
		return nil, errors.New("no skills to download")
	}
	q := url.Values{}
	for _, n := range names {
		q.Add("skill", n)
	}
	resp, err := c.do(ctx, http.MethodGet, apiPrefix+"/download", q, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// PushRequest is the body accepted by POST /skills and PUT /skills/{name}.
type PushRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Body        string   `json:"body"`
	Tags        []string `json:"tags,omitempty"`
	// Files carries the skill's companion files (references/, scripts/, …)
	// keyed by slash-separated path relative to the skill directory, so a push
	// uploads the whole skill rather than just its SKILL.md.
	Files map[string]string `json:"files,omitempty"`
}

// CreateSkill publishes a new skill.
func (c *Client) CreateSkill(ctx context.Context, req PushRequest) error {
	resp, err := c.do(ctx, http.MethodPost, apiPrefix+"/skills", nil, req)
	if err != nil {
		return err
	}
	return drain(resp)
}

// UpdateSkill replaces an existing skill.
func (c *Client) UpdateSkill(ctx context.Context, req PushRequest) error {
	resp, err := c.do(ctx, http.MethodPut, apiPrefix+"/skills/"+url.PathEscape(req.Name), nil, req)
	if err != nil {
		return err
	}
	return drain(resp)
}

// Exists reports whether a skill is already in the registry.
func (c *Client) Exists(ctx context.Context, name string) (bool, error) {
	_, err := c.GetSkill(ctx, name)
	switch {
	case err == nil:
		return true, nil
	case IsNotFound(err):
		return false, nil
	default:
		return false, err
	}
}

func getJSON[T any](ctx context.Context, c *Client, path string, q url.Values) (*T, error) {
	resp, err := c.do(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response from %s: %w", c.BaseURL+path, err)
	}
	return &out, nil
}

// do issues a request and converts anything that is not a 2xx into an error, so
// callers only ever see a usable response body.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any) (*http.Response, error) {
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", u, err)
	}
	req.Header.Set("User-Agent", userAgent)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("request to %s: %w", u, err)
		}
		// Anything short of an HTTP response means the registry is not there,
		// which is nearly always "the server is not running" rather than a bug
		// worth showing a dial error for.
		return nil, &UnreachableError{Registry: c.BaseURL, Err: err}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		defer resp.Body.Close()
		return nil, &UnauthorizedError{Registry: c.BaseURL}
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, apiErrorFrom(resp, u)
	}
	return resp, nil
}

func apiErrorFrom(resp *http.Response, u string) error {
	e := &APIError{Status: resp.StatusCode, URL: u}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err == nil && len(b) > 0 {
		var wire api.Error
		if json.Unmarshal(b, &wire) == nil {
			e.Code, e.Message = wire.Error, wire.Message
		}
	}
	return e
}

func drain(resp *http.Response) error {
	defer resp.Body.Close()
	_, err := io.Copy(io.Discard, resp.Body)
	return err
}
