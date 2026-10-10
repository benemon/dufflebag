package pluginimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// ErrUpstreamUnavailable is an upstream that could not be reached or answered
// with a server error; it is distinct from an answer that something is absent.
var ErrUpstreamUnavailable = errors.New("upstream unavailable")

// ErrUpstreamNotFound is an upstream answer that the product or version does
// not exist.
var ErrUpstreamNotFound = errors.New("not found upstream")

var productPattern = regexp.MustCompile(`^packer-plugin-[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidProduct reports whether product names a Packer plugin on
// releases.hashicorp.com.
func ValidProduct(product string) bool {
	return productPattern.MatchString(product)
}

// Upstream reads HashiCorp's release service: api.releases.hashicorp.com for
// browsing, releases.hashicorp.com for the files an import fetches.
type Upstream struct {
	client   *http.Client
	api      string
	releases string
}

// NewUpstream reads the release service at the given base URLs.
func NewUpstream(client *http.Client, api, releases string) *Upstream {
	return &Upstream{client: client, api: strings.TrimSuffix(api, "/"), releases: strings.TrimSuffix(releases, "/")}
}

// UpstreamVersion is one release of a plugin as the release service lists it.
type UpstreamVersion struct {
	Version    string
	Created    time.Time
	Prerelease bool
	State      string
	Changelog  string
	Platforms  []string
}

type build struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Filename string `json:"filename"`
}

type releaseFiles struct {
	Shasums    string   `json:"shasums"`
	Signatures []string `json:"shasums_signatures"`
	Builds     []build  `json:"builds"`
}

func (u *Upstream) get(ctx context.Context, address string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := u.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstreamUnavailable, err)
	}
	switch {
	case response.StatusCode == http.StatusNotFound:
		_ = response.Body.Close()
		return nil, fmt.Errorf("%w: %s", ErrUpstreamNotFound, address)
	case response.StatusCode != http.StatusOK:
		_ = response.Body.Close()
		return nil, fmt.Errorf("%w: %s answered %d", ErrUpstreamUnavailable, address, response.StatusCode)
	}
	return response, nil
}

func (u *Upstream) getJSON(ctx context.Context, address string, into any) error {
	response, err := u.get(ctx, address)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(into); err != nil {
		return fmt.Errorf("%w: decode %s: %v", ErrUpstreamUnavailable, address, err)
	}
	return nil
}

// A SHA256SUMS, signature or manifest larger than limit is not a real one.
func (u *Upstream) fetchSmall(ctx context.Context, address string, limit int64) ([]byte, error) {
	response, err := u.get(ctx, address)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %v", ErrUpstreamUnavailable, address, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", address, limit)
	}
	return data, nil
}

func (u *Upstream) download(ctx context.Context, address string, file *os.File) (string, int64, error) {
	response, err := u.get(ctx, address)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = response.Body.Close() }()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), response.Body)
	if err != nil {
		return "", 0, fmt.Errorf("%w: download %s: %v", ErrUpstreamUnavailable, address, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

// Plugins lists the Packer plugins the release service publishes.
func (u *Upstream) Plugins(ctx context.Context) ([]string, error) {
	var products []string
	if err := u.getJSON(ctx, u.api+"/v1/products", &products); err != nil {
		return nil, err
	}
	plugins := []string{}
	for _, product := range products {
		if ValidProduct(product) {
			plugins = append(plugins, product)
		}
	}
	sort.Strings(plugins)
	return plugins, nil
}

// Versions returns one page of a plugin's releases, newest first, and the
// cursor for the next page ("" on the last). The service pages at most 20.
func (u *Upstream) Versions(ctx context.Context, product, after string) ([]UpstreamVersion, string, error) {
	query := url.Values{"limit": {"20"}}
	if after != "" {
		query.Set("after", after)
	}
	var releases []struct {
		Version    string    `json:"version"`
		Created    time.Time `json:"timestamp_created"`
		Prerelease bool      `json:"is_prerelease"`
		Changelog  string    `json:"url_changelog"`
		Status     struct {
			State string `json:"state"`
		} `json:"status"`
		Builds []build `json:"builds"`
	}
	if err := u.getJSON(ctx, u.api+"/v1/releases/"+product+"?"+query.Encode(), &releases); err != nil {
		return nil, "", err
	}
	versions := make([]UpstreamVersion, 0, len(releases))
	for _, release := range releases {
		version := UpstreamVersion{
			Version: release.Version, Created: release.Created, Prerelease: release.Prerelease,
			State: release.Status.State, Changelog: release.Changelog,
		}
		for _, b := range release.Builds {
			version.Platforms = append(version.Platforms, b.OS+"_"+b.Arch)
		}
		versions = append(versions, version)
	}
	next := ""
	if len(releases) == 20 {
		next = releases[len(releases)-1].Created.Format(time.RFC3339Nano)
	}
	return versions, next, nil
}

func (u *Upstream) release(ctx context.Context, product, version string) (releaseFiles, error) {
	var index struct {
		Versions map[string]releaseFiles `json:"versions"`
	}
	if err := u.getJSON(ctx, u.releases+"/"+product+"/index.json", &index); err != nil {
		return releaseFiles{}, err
	}
	files, ok := index.Versions[version]
	if !ok {
		return releaseFiles{}, fmt.Errorf("%w: %s %s", ErrUpstreamNotFound, product, version)
	}
	return files, nil
}

func (u *Upstream) fileURL(product, version, filename string) string {
	return u.releases + "/" + product + "/" + version + "/" + url.PathEscape(filename)
}

func newBlob(filename string, data []byte) store.PluginBlob {
	sum := sha256.Sum256(data)
	return store.PluginBlob{Filename: filename, SHA256: hex.EncodeToString(sum[:]), Body: bytes.NewReader(data), Size: int64(len(data))}
}
