package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/pluginimport"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

type fakeCatalogue struct {
	plugins  []string
	versions []pluginimport.UpstreamVersion
	release  pluginimport.GitHubRelease
	err      error
}

func (f fakeCatalogue) Plugins(context.Context) ([]string, error) { return f.plugins, f.err }

func (f fakeCatalogue) Resolve(context.Context, string) (pluginimport.GitHubRelease, error) {
	return f.release, f.err
}

func (f fakeCatalogue) Versions(_ context.Context, product, _ string) ([]pluginimport.UpstreamVersion, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	return f.versions, "", nil
}

func importHandler(role identity.Role, repository *fakeTenancyRepository, catalogue PluginCatalogue) http.Handler {
	actor := testRoles{role: role, scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)}}
	return newHandlerWithServices(repository, &fakeInstanceRepository{}, testAuth{}, actor, testLogger(),
		nil, nil, nil, nil, nil, nil, BuildInfo{}, DefaultPluginUploadBytes, catalogue, func() time.Time { return initTestTime })
}

func TestHashicorpCatalogueMarksMirroredAndHeldPlugins(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	repository.plugins = []store.PluginSummary{
		{Name: "amazon", Source: store.PluginSource{Kind: "releases-hashicorp", Repository: "packer-plugin-amazon"}, PublishedVersions: []string{"1.8.2", "1.8.1"}},
		{Name: "docker", Source: store.PluginSource{Kind: "github", Repository: "acme-infra/packer-plugin-docker"}, PublishedVersions: []string{"1.0.11"}},
	}
	catalogue := fakeCatalogue{plugins: []string{"packer-plugin-amazon", "packer-plugin-docker", "packer-plugin-qemu"}}
	handler, trail := auditedPlatform(t, importHandler(identity.RolePublisher, repository, catalogue))
	response := call(t, handler, http.MethodGet, pluginRegistryPath("catalogue/hashicorp"), nil, testToken)
	var body struct{ Plugins []HashicorpPlugin }
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body.Plugins) != 3 {
		t.Fatalf("catalogue = %d %s", response.Code, response.Body.String())
	}
	if amazon := body.Plugins[0]; amazon.MirroredVersions != 2 || amazon.HeldBy != nil {
		t.Fatalf("amazon = %+v; want 2 mirrored versions and no conflict", amazon)
	}
	if docker := body.Plugins[1]; docker.HeldBy == nil || docker.HeldBy.Kind != "github" || docker.MirroredVersions != 0 {
		t.Fatalf("docker = %+v; want held by the GitHub source", docker)
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{"operation": "plugin.catalogue.list", "outcome": "success"})
}

func TestHashicorpCatalogueVersionsAndUnreachableUpstream(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	repository.pluginVersions = map[string][]store.PluginVersionSummary{"amazon": {{Version: "1.8.2"}}}
	catalogue := fakeCatalogue{versions: []pluginimport.UpstreamVersion{
		{Version: "1.8.3", Created: initTestTime, State: "supported", Platforms: []string{"linux_amd64"}},
		{Version: "1.8.2", Created: initTestTime, Platforms: []string{"linux_amd64"}},
	}}
	response := call(t, importHandler(identity.RolePublisher, repository, catalogue), http.MethodGet, pluginRegistryPath("catalogue/hashicorp/packer-plugin-amazon"), nil, testToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"mirrored":false,"platforms":["linux_amd64"],"prerelease":false,"state":"supported","version":"1.8.3"`) ||
		!strings.Contains(response.Body.String(), `"mirrored":true`) {
		t.Fatalf("versions = %d %s", response.Code, response.Body.String())
	}
	unreachable := fakeCatalogue{err: errors.Join(pluginimport.ErrUpstreamUnavailable, errors.New("dial tcp: no route"))}
	response = call(t, importHandler(identity.RolePublisher, repository, unreachable), http.MethodGet, pluginRegistryPath("catalogue/hashicorp"), nil, testToken)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "releases.hashicorp.com could not be reached") {
		t.Fatalf("unreachable upstream = %d %s", response.Code, response.Body.String())
	}
}

func TestPluginImportsQueueAndRead(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	handler, trail := auditedPlatform(t, importHandler(identity.RolePublisher, repository, fakeCatalogue{}))
	request := map[string]any{"source": "releases-hashicorp", "product": "packer-plugin-amazon", "versions": []string{"1.8.2"}, "platforms": []string{"linux_amd64"}}
	response := call(t, handler, http.MethodPost, pluginRegistryPath("imports"), request, testToken)
	var job PluginImport
	if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &job) != nil || job.State != "queued" || job.Product != "packer-plugin-amazon" {
		t.Fatalf("create = %d %s", response.Code, response.Body.String())
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{"operation": "plugin.import.create", "reason": "queued"})
	read := call(t, importHandler(identity.RoleReader, repository, fakeCatalogue{}), http.MethodGet, pluginRegistryPath("imports/"+job.Id.String()), nil, testToken)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"outcomes":[]`) {
		t.Fatalf("read = %d %s", read.Code, read.Body.String())
	}
	if missing := call(t, handler, http.MethodGet, pluginRegistryPath("imports/"+uuid.NewString()), nil, testToken); missing.Code != http.StatusNotFound {
		t.Fatalf("unknown import = %d", missing.Code)
	}
	for name, body := range map[string]map[string]any{
		"not a plugin product": {"source": "releases-hashicorp", "product": "terraform", "versions": []string{"1.0.0"}, "platforms": []string{"linux_amd64"}},
		"no versions":          {"source": "releases-hashicorp", "product": "packer-plugin-amazon", "versions": []string{}, "platforms": []string{"linux_amd64"}},
		"platform not OS_ARCH": {"source": "releases-hashicorp", "product": "packer-plugin-amazon", "versions": []string{"1.8.2"}, "platforms": []string{"linux"}},
		"duplicate platform":   {"source": "releases-hashicorp", "product": "packer-plugin-amazon", "versions": []string{"1.8.2"}, "platforms": []string{"linux_amd64", "linux_amd64"}},
	} {
		if response := call(t, handler, http.MethodPost, pluginRegistryPath("imports"), body, testToken); response.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s, want 400", name, response.Code, response.Body.String())
		}
	}
	disabled := pluginRegistryRepository(store.PluginRegistry{})
	if response := call(t, importHandler(identity.RolePublisher, disabled, fakeCatalogue{}), http.MethodPost, pluginRegistryPath("imports"), request, testToken); response.Code != http.StatusConflict {
		t.Fatalf("import into a disabled registry = %d, want 409", response.Code)
	}
}

func TestPluginImportRoleAxis(t *testing.T) {
	request := map[string]any{"source": "releases-hashicorp", "product": "packer-plugin-amazon", "versions": []string{"1.8.2"}, "platforms": []string{"linux_amd64"}}
	for _, tc := range []struct {
		role         identity.Role
		method, path string
		body         any
		want         int
	}{
		{identity.RoleBuilder, http.MethodPost, "imports", request, http.StatusForbidden},
		{identity.RoleBuilder, http.MethodGet, "catalogue/hashicorp", nil, http.StatusForbidden},
		{identity.RolePublisher, http.MethodPut, "default-platforms", map[string]any{"platforms": []string{"linux_amd64"}}, http.StatusForbidden},
		{identity.RoleMaintainer, http.MethodPut, "default-platforms", map[string]any{"platforms": []string{"linux_amd64"}}, http.StatusOK},
		{identity.RoleReader, http.MethodGet, "default-platforms", nil, http.StatusOK},
	} {
		repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
		response := call(t, importHandler(tc.role, repository, fakeCatalogue{}), tc.method, pluginRegistryPath(tc.path), tc.body, testToken)
		if response.Code != tc.want {
			t.Fatalf("%s %s %s = %d %s, want %d", tc.role, tc.method, tc.path, response.Code, response.Body.String(), tc.want)
		}
	}
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	response := call(t, importHandler(identity.RoleMaintainer, repository, fakeCatalogue{}), http.MethodPut, pluginRegistryPath("default-platforms"),
		map[string]any{"platforms": []string{"Linux_AMD64"}}, testToken)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("uppercase platform = %d, want 400", response.Code)
	}
}

func TestResolveGithubRelease(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	repository.plugins = []store.PluginSummary{{Name: "git", Source: store.PluginSource{Kind: "releases-hashicorp", Repository: "packer-plugin-git"}}}
	release := pluginimport.GitHubRelease{Repository: "ethanmdavidson/packer-plugin-git", Name: "git", Tag: "v0.6.3", Version: "0.6.3", Platforms: []string{"linux_amd64"}, HasChecksum: true}
	path := pluginRegistryPath("catalogue/github/resolve")
	link := map[string]any{"release_url": "https://github.com/ethanmdavidson/packer-plugin-git/releases/latest"}

	response := call(t, importHandler(identity.RolePublisher, repository, fakeCatalogue{release: release}), http.MethodPost, path, link, testToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"tag":"v0.6.3"`) ||
		!strings.Contains(response.Body.String(), `"held_by":{"kind":"releases-hashicorp","repository":"packer-plugin-git"}`) {
		t.Fatalf("resolve = %d %s", response.Code, response.Body.String())
	}
	for name, tc := range map[string]struct {
		err  error
		want int
		text string
	}{
		"not a release link": {pluginimport.ErrNotAReleaseLink, http.StatusBadRequest, "releases/tag"},
		"no such release":    {pluginimport.ErrUpstreamNotFound, http.StatusNotFound, "no public release"},
		"rate limited":       {fmt.Errorf("%w; it resets at 2026-10-09T18:00:00Z", pluginimport.ErrGitHubRateLimited), http.StatusBadGateway, "resets at 2026-10-09T18:00:00Z"},
	} {
		response := call(t, importHandler(identity.RolePublisher, repository, fakeCatalogue{err: tc.err}), http.MethodPost, path, link, testToken)
		if response.Code != tc.want || !strings.Contains(response.Body.String(), tc.text) {
			t.Fatalf("%s = %d %s, want %d containing %q", name, response.Code, response.Body.String(), tc.want, tc.text)
		}
	}
	if response := call(t, importHandler(identity.RoleBuilder, repository, fakeCatalogue{release: release}), http.MethodPost, path, link, testToken); response.Code != http.StatusForbidden {
		t.Fatalf("builder resolve = %d, want 403", response.Code)
	}

	handler := importHandler(identity.RolePublisher, repository, fakeCatalogue{})
	github := map[string]any{"source": "github", "product": "ethanmdavidson/packer-plugin-git", "versions": []string{"v0.6.3"}, "platforms": []string{"linux_amd64"}}
	if response := call(t, handler, http.MethodPost, pluginRegistryPath("imports"), github, testToken); response.Code != http.StatusAccepted {
		t.Fatalf("github import = %d %s", response.Code, response.Body.String())
	}
	github["product"] = "packer-plugin-git"
	if response := call(t, handler, http.MethodPost, pluginRegistryPath("imports"), github, testToken); response.Code != http.StatusBadRequest {
		t.Fatalf("github import without an owner = %d, want 400", response.Code)
	}
}
