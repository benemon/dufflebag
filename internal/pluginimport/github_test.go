package pluginimport

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// githubRelease stands in for api.github.com and its release assets. The
// release body is the captured ethanmdavidson/packer-plugin-git v0.6.3
// response (testdata, trimmed to the fields an import reads) with its asset
// URLs pointed at this server; the linux_arm64 zip and the SHA256SUMS are
// generated so their digests agree.
func githubRelease(t *testing.T, withSums bool) (*httptest.Server, *int) {
	t.Helper()
	captured, err := os.ReadFile("testdata/github-release-packer-plugin-git-v0.6.3.json")
	if err != nil {
		t.Fatal(err)
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
		Draft      bool   `json:"draft"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int    `json:"size"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(captured, &release); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, _ := writer.Create("packer-plugin-git_v0.6.3_x5.0_linux_arm64")
	_, _ = entry.Write([]byte("binary"))
	_ = writer.Close()
	files := map[string][]byte{"packer-plugin-git_v0.6.3_x5.0_linux_arm64.zip": archive.Bytes(), "packer-plugin-git_v0.6.3_SHA256SUMS.sig": []byte("signature")}
	var sums strings.Builder
	for _, asset := range release.Assets {
		if strings.HasSuffix(asset.Name, ".zip") {
			digest := strings.Repeat("0", 64)
			if data, ok := files[asset.Name]; ok {
				sum := sha256.Sum256(data)
				digest = hex.EncodeToString(sum[:])
			}
			fmt.Fprintf(&sums, "%s  %s\n", digest, asset.Name)
		}
	}
	files["packer-plugin-git_v0.6.3_SHA256SUMS"] = []byte(sums.String())
	apiCalls := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/ethanmdavidson/packer-plugin-git/releases/tags/v0.6.3", "/repos/ethanmdavidson/packer-plugin-git/releases/latest":
			apiCalls++
			for i := range release.Assets {
				release.Assets[i].URL = server.URL + "/download/" + release.Assets[i].Name
			}
			assets := release.Assets
			if !withSums {
				assets = nil
				for _, asset := range release.Assets {
					if !strings.HasSuffix(asset.Name, "_SHA256SUMS") {
						assets = append(assets, asset)
					}
				}
			}
			body := release
			body.Assets = assets
			_ = json.NewEncoder(w).Encode(body)
		case "/repos/ratelimited/packer-plugin-git/releases/latest":
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "1791572098")
			w.WriteHeader(http.StatusForbidden)
		default:
			if data, ok := files[strings.TrimPrefix(r.URL.Path, "/download/")]; ok {
				_, _ = w.Write(data)
				return
			}
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &apiCalls
}

func TestResolveGitHubReleaseLinks(t *testing.T) {
	server, calls := githubRelease(t, true)
	github := NewGitHub(server.Client(), server.URL)
	for _, link := range []string{
		"https://github.com/ethanmdavidson/packer-plugin-git/releases/tag/v0.6.3",
		"https://github.com/ethanmdavidson/packer-plugin-git/releases/latest",
	} {
		release, err := github.Resolve(context.Background(), link)
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		if release.Repository != "ethanmdavidson/packer-plugin-git" || release.Name != "git" || release.Tag != "v0.6.3" ||
			release.Version != "0.6.3" || !release.HasChecksum || len(release.Platforms) != 14 {
			t.Fatalf("%s resolved to %+v", link, release)
		}
	}
	if *calls != 2 {
		t.Fatalf("resolving two links made %d API calls, want one each", *calls)
	}
	for _, link := range []string{
		"https://github.com/hashicorp/terraform/releases/tag/v1.9.0",
		"https://github.com/ethanmdavidson/packer-plugin-git",
		"https://gitlab.com/ethanmdavidson/packer-plugin-git/releases/tag/v0.6.3",
	} {
		if _, err := github.Resolve(context.Background(), link); !errors.Is(err, ErrNotAReleaseLink) {
			t.Fatalf("%s = %v, want ErrNotAReleaseLink", link, err)
		}
	}
	_, err := github.Resolve(context.Background(), "https://github.com/ratelimited/packer-plugin-git/releases/latest")
	if !errors.Is(err, ErrGitHubRateLimited) || !strings.Contains(err.Error(), "2026-10-09T") {
		t.Fatalf("rate limited = %v, want ErrGitHubRateLimited naming the reset time", err)
	}
}

func TestImportGitHubVersion(t *testing.T) {
	server, _ := githubRelease(t, true)
	repository := &fakeRepository{}
	importer := NewImporter(NewUpstream(server.Client(), server.URL, server.URL), NewGitHub(server.Client(), server.URL), nil, repository)
	outcome := importer.ImportGitHubVersion(context.Background(), store.OrganizationTenant{}, "ethanmdavidson/packer-plugin-git", "v0.6.3", []string{"linux_arm64"})
	if outcome.Outcome != OutcomeImported || outcome.Version != "0.6.3" || len(repository.published) != 1 {
		t.Fatalf("outcome = %+v, published = %d", outcome, len(repository.published))
	}
	input := repository.published[0]
	if input.Source != (store.PluginSource{Kind: "github", Repository: "ethanmdavidson/packer-plugin-git"}) || input.Signature == nil || len(input.Listed) != 14 {
		t.Fatalf("input = %+v", input)
	}

	unsummed, _ := githubRelease(t, false)
	importer = NewImporter(NewUpstream(unsummed.Client(), unsummed.URL, unsummed.URL), NewGitHub(unsummed.Client(), unsummed.URL), nil, &fakeRepository{})
	outcome = importer.ImportGitHubVersion(context.Background(), store.OrganizationTenant{}, "ethanmdavidson/packer-plugin-git", "v0.6.3", []string{"linux_arm64"})
	if outcome.Outcome != OutcomeFailed || !strings.Contains(outcome.Error, "no SHA256SUMS asset") {
		t.Fatalf("release without SHA256SUMS = %+v", outcome)
	}
}
