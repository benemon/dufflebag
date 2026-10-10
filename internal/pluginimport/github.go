package pluginimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrGitHubRateLimited is GitHub refusing an unauthenticated caller that has
// spent its hourly allowance (60 calls per egress address).
var ErrGitHubRateLimited = errors.New("GitHub rate limit reached")

// ErrNotAReleaseLink is a link that names no packer-plugin release.
var ErrNotAReleaseLink = errors.New("not a release link of a packer-plugin repository")

var (
	releaseLink    = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9-]+)/(packer-plugin-[a-z0-9]+(?:-[a-z0-9]+)*)/releases/(?:tag/([^/?#]+)|latest)/?$`)
	repositoryName = regexp.MustCompile(`^[A-Za-z0-9-]+/packer-plugin-[a-z0-9]+(-[a-z0-9]+)*$`)
	zipPlatform    = regexp.MustCompile(`_([a-z0-9]+)_([a-z0-9]+)\.zip$`)
)

// ValidRepository reports whether repository is owner/packer-plugin-<name>.
func ValidRepository(repository string) bool {
	return repositoryName.MatchString(repository)
}

// GitHub reads public releases from the GitHub API, unauthenticated (ADR-0027 A4).
type GitHub struct {
	client *http.Client
	api    string
}

// NewGitHub reads releases from the API at base.
func NewGitHub(client *http.Client, base string) *GitHub {
	return &GitHub{client: client, api: strings.TrimSuffix(base, "/")}
}

// GitHubRelease is a release resolved to an exact tag.
type GitHubRelease struct {
	Repository    string
	Name          string
	Tag           string
	Version       string
	Prerelease    bool
	PublishedAt   time.Time
	Platforms     []string
	HasChecksum   bool
	ChecksumAsset string
	assets        map[string]string
}

// Resolve reads the release a link names. A /releases/latest link is
// resolved here, once, so an import fetches the exact tag it was shown.
func (g *GitHub) Resolve(ctx context.Context, link string) (GitHubRelease, error) {
	parts := releaseLink.FindStringSubmatch(strings.TrimSpace(link))
	if parts == nil {
		return GitHubRelease{}, ErrNotAReleaseLink
	}
	repository := parts[1] + "/" + parts[2]
	if parts[3] == "" {
		return g.release(ctx, repository, "latest")
	}
	tag, err := url.PathUnescape(parts[3])
	if err != nil {
		return GitHubRelease{}, ErrNotAReleaseLink
	}
	return g.release(ctx, repository, "tags/"+url.PathEscape(tag))
}

// Release reads the release of repository at tag.
func (g *GitHub) Release(ctx context.Context, repository, tag string) (GitHubRelease, error) {
	return g.release(ctx, repository, "tags/"+url.PathEscape(tag))
}

func (g *GitHub) release(ctx context.Context, repository, which string) (GitHubRelease, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, g.api+"/repos/"+repository+"/releases/"+which, nil)
	if err != nil {
		return GitHubRelease{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := g.client.Do(request)
	if err != nil {
		return GitHubRelease{}, fmt.Errorf("%w: %v", ErrUpstreamUnavailable, err)
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case (response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests) &&
		response.Header.Get("X-RateLimit-Remaining") == "0":
		reset := "an hour"
		if seconds, err := strconv.ParseInt(response.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			reset = time.Unix(seconds, 0).UTC().Format(time.RFC3339)
		}
		return GitHubRelease{}, fmt.Errorf("%w; it resets at %s", ErrGitHubRateLimited, reset)
	case response.StatusCode == http.StatusNotFound:
		return GitHubRelease{}, fmt.Errorf("%w: %s %s", ErrUpstreamNotFound, repository, which)
	case response.StatusCode != http.StatusOK:
		return GitHubRelease{}, fmt.Errorf("%w: GitHub answered %d", ErrUpstreamUnavailable, response.StatusCode)
	}
	var body struct {
		Tag         string    `json:"tag_name"`
		Prerelease  bool      `json:"prerelease"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&body); err != nil {
		return GitHubRelease{}, fmt.Errorf("%w: decode release: %v", ErrUpstreamUnavailable, err)
	}
	_, product, _ := strings.Cut(repository, "/")
	release := GitHubRelease{
		Repository: repository, Name: strings.TrimPrefix(product, "packer-plugin-"),
		Tag: body.Tag, Version: strings.TrimPrefix(body.Tag, "v"), Prerelease: body.Prerelease, PublishedAt: body.PublishedAt,
		assets: map[string]string{},
	}
	for _, asset := range body.Assets {
		release.assets[asset.Name] = asset.URL
		if strings.HasSuffix(asset.Name, "_SHA256SUMS") {
			release.HasChecksum, release.ChecksumAsset = true, asset.Name
		}
		if m := zipPlatform.FindStringSubmatch(asset.Name); m != nil {
			release.Platforms = append(release.Platforms, m[1]+"_"+m[2])
		}
	}
	sort.Strings(release.Platforms)
	return release, nil
}

// Catalogue is what the import screens read: HashiCorp's release service and
// GitHub releases.
type Catalogue struct {
	*Upstream
	*GitHub
}
