package gitsource

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
)

// Providers are the git hosts we can pull a repository archive from.
//
// Every one of them exposes a plain HTTPS tarball endpoint, which is why this
// works without a git binary, because the deployed image is `scratch` and has none.
// Adding a host means describing its URLs here, not writing new fetch logic.
type Provider struct {
	// ID is the stable identifier stored in provenance.
	ID string
	// Label is what a person sees.
	Label string
	// Host is the canonical public host, empty for self-hosted detection.
	Host string
	// EnvTokens are consulted in order for an access token.
	EnvTokens []string

	// tarballURL builds the archive URL for a ref. An empty ref means the
	// default branch.
	tarballURL func(base string, r Repo) string
	// metadataURL builds the API URL describing the repository, or "" when the
	// provider has none we can use anonymously.
	metadataURL func(base string, r Repo) string
	// commitsURL builds the API URL listing commits touching a path, or "".
	commitsURL func(base string, r Repo, ref, path string) string
	// authHeader returns the header name and value for a token.
	authHeader func(token string) (string, string)
	// metadataShape selects the JSON decoder for metadataURL.
	metadataShape string
}

// scheme+host for a repo, honouring self-hosted instances.
func (r Repo) baseURL() string {
	host := r.Host
	if host == "" {
		host = ProviderGitHub.Host
	}
	return "https://" + host
}

var (
	ProviderGitHub = &Provider{
		ID: "github", Label: "GitHub", Host: "github.com",
		EnvTokens:     []string{"GITHUB_TOKEN", "GH_TOKEN"},
		metadataShape: "github",
		tarballURL: func(base string, r Repo) string {
			u := base + "/repos/" + r.Owner + "/" + r.Name + "/tarball"
			if r.Ref != "" {
				u += "/" + r.Ref
			}
			return u
		},
		metadataURL: func(base string, r Repo) string {
			return base + "/repos/" + r.Owner + "/" + r.Name
		},
		commitsURL: func(base string, r Repo, ref, p string) string {
			q := url.Values{"path": {p}, "per_page": {"1"}}
			if ref != "" {
				q.Set("sha", ref)
			}
			return base + "/repos/" + r.Owner + "/" + r.Name + "/commits?" + q.Encode()
		},
		authHeader: func(t string) (string, string) { return "Authorization", "Bearer " + t },
	}

	ProviderGitLab = &Provider{
		ID: "gitlab", Label: "GitLab", Host: "gitlab.com",
		EnvTokens:     []string{"GITLAB_TOKEN", "CI_JOB_TOKEN"},
		metadataShape: "gitlab",
		tarballURL: func(base string, r Repo) string {
			// GitLab identifies a project by its URL-encoded full path.
			id := url.PathEscape(r.Owner + "/" + r.Name)
			u := base + "/api/v4/projects/" + id + "/repository/archive.tar.gz"
			if r.Ref != "" {
				u += "?sha=" + url.QueryEscape(r.Ref)
			}
			return u
		},
		metadataURL: func(base string, r Repo) string {
			return base + "/api/v4/projects/" + url.PathEscape(r.Owner+"/"+r.Name)
		},
		commitsURL: func(base string, r Repo, ref, p string) string {
			q := url.Values{"path": {p}, "per_page": {"1"}}
			if ref != "" {
				q.Set("ref_name", ref)
			}
			return base + "/api/v4/projects/" + url.PathEscape(r.Owner+"/"+r.Name) +
				"/repository/commits?" + q.Encode()
		},
		authHeader: func(t string) (string, string) { return "PRIVATE-TOKEN", t },
	}

	ProviderGitea = &Provider{
		ID: "gitea", Label: "Gitea", Host: "",
		EnvTokens:     []string{"GITEA_TOKEN", "FORGEJO_TOKEN", "CODEBERG_TOKEN"},
		metadataShape: "gitea",
		tarballURL: func(base string, r Repo) string {
			ref := r.Ref
			if ref == "" {
				ref = "HEAD"
			}
			return base + "/api/v1/repos/" + r.Owner + "/" + r.Name + "/archive/" + ref + ".tar.gz"
		},
		metadataURL: func(base string, r Repo) string {
			return base + "/api/v1/repos/" + r.Owner + "/" + r.Name
		},
		commitsURL: func(base string, r Repo, ref, p string) string {
			q := url.Values{"path": {p}, "limit": {"1"}}
			if ref != "" {
				q.Set("sha", ref)
			}
			return base + "/api/v1/repos/" + r.Owner + "/" + r.Name + "/commits?" + q.Encode()
		},
		authHeader: func(t string) (string, string) { return "Authorization", "token " + t },
	}

	ProviderBitbucket = &Provider{
		ID: "bitbucket", Label: "Bitbucket", Host: "bitbucket.org",
		EnvTokens:     []string{"BITBUCKET_TOKEN"},
		metadataShape: "bitbucket",
		tarballURL: func(base string, r Repo) string {
			ref := r.Ref
			if ref == "" {
				ref = "HEAD"
			}
			// Bitbucket serves archives from the web host, not the API host,
			// unless a base override points somewhere else.
			web := "https://bitbucket.org"
			if base != "" && !strings.Contains(base, "api.bitbucket.org") {
				web = base
			}
			return web + "/" + r.Owner + "/" + r.Name + "/get/" + ref + ".tar.gz"
		},
		metadataURL: func(base string, r Repo) string {
			api := "https://api.bitbucket.org"
			if base != "" && !strings.Contains(base, "bitbucket.org") {
				api = base
			}
			return api + "/2.0/repositories/" + r.Owner + "/" + r.Name
		},
		// Bitbucket's commit API does not filter by path in a way that is worth
		// a request per skill, so per-skill dates fall back to the repo date.
		commitsURL: nil,
		authHeader: func(t string) (string, string) { return "Authorization", "Bearer " + t },
	}
)

// knownProviders maps a canonical host to its provider.
var knownProviders = map[string]*Provider{
	"github.com":    ProviderGitHub,
	"gitlab.com":    ProviderGitLab,
	"bitbucket.org": ProviderBitbucket,
	"codeberg.org":  ProviderGitea,
}

// providerForHost picks a provider. Self-hosted instances are common enough
// that an unknown host is not an error: GitLab and Gitea are both guessed at,
// and the archive fetch decides which one was right.
func providerForHost(host string) *Provider {
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	if p, ok := knownProviders[host]; ok {
		return p
	}
	switch {
	case strings.Contains(host, "gitlab"):
		return ProviderGitLab
	case strings.Contains(host, "gitea"), strings.Contains(host, "forgejo"), strings.Contains(host, "codeberg"):
		return ProviderGitea
	case strings.Contains(host, "bitbucket"):
		return ProviderBitbucket
	}
	return nil // caller probes
}

// fallbackProviders are tried in order for an unrecognised host.
func fallbackProviders() []*Provider { return []*Provider{ProviderGitea, ProviderGitLab} }

// fallbackTokens is consulted when the environment has nothing. It lets tokens
// entered in the UI take effect immediately, without a restart or redeploy,
// while an environment variable still wins so deployment config is never
// silently overridden by something typed into a form.
var (
	fallbackMu     sync.RWMutex
	fallbackTokens = map[string]string{}
)

// SetFallbackTokens replaces the tokens used when the environment is empty.
func SetFallbackTokens(m map[string]string) {
	fallbackMu.Lock()
	defer fallbackMu.Unlock()
	fallbackTokens = map[string]string{}
	for k, v := range m {
		if v = strings.TrimSpace(v); v != "" {
			fallbackTokens[k] = v
		}
	}
}

func fallbackToken(name string) string {
	fallbackMu.RLock()
	defer fallbackMu.RUnlock()
	return fallbackTokens[name]
}

// Token returns the first configured token for the provider: environment
// first, then anything set through the UI.
func (p *Provider) Token() string {
	for _, k := range p.EnvTokens {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	for _, k := range p.EnvTokens {
		if v := fallbackToken(k); v != "" {
			return v
		}
	}
	return ""
}

// TokenEnvName is the variable to suggest when access is denied.
func (p *Provider) TokenEnvName() string {
	if len(p.EnvTokens) == 0 {
		return "a provider token"
	}
	return p.EnvTokens[0]
}

// apiBase is the host API calls go to, which for GitHub is a separate domain.
func (p *Provider) apiBase(r Repo) string {
	return p.apiBaseWithOverride(r, "")
}

// apiBaseWithOverride lets a Client force the API host, which is how tests
// point at an httptest server and how a custom endpoint is configured.
func (p *Provider) apiBaseWithOverride(r Repo, override string) string {
	if override != "" {
		return strings.TrimRight(override, "/")
	}
	return p.defaultAPIBase(r)
}

func (p *Provider) defaultAPIBase(r Repo) string {
	host := r.Host
	if host == "" {
		host = p.Host
	}
	switch p.ID {
	case "github":
		if host == "github.com" || host == "" {
			return "https://api.github.com"
		}
		// GitHub Enterprise mounts the API under /api/v3.
		return "https://" + host + "/api/v3"
	default:
		return "https://" + host
	}
}

// SupportedProviders lists what the UI can advertise.
func SupportedProviders() []string {
	return []string{"GitHub", "GitLab", "Bitbucket", "Codeberg", "Gitea", "Forgejo"}
}

func unsupportedHostError(host string) error {
	return fmt.Errorf("could not work out what kind of git host %q is. GitHub, GitLab, Bitbucket, Codeberg, Gitea and Forgejo are supported", host)
}
