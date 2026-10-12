package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
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
		nil, nil, nil, nil, nil, nil, BuildInfo{}, DefaultPluginUploadBytes, DefaultPluginUpdateInterval, catalogue, func() time.Time { return initTestTime })
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
	if job.Origin != PluginImportOriginImport || job.CreatedBy == "" || job.CreatedBy != repository.imports[0].Origin.CreatedBy || job.BatchSize != 1 {
		t.Fatalf("job origin = %s by %q, batch %d of %d", job.Origin, job.CreatedBy, job.BatchIndex, job.BatchSize)
	}
	read := call(t, importHandler(identity.RoleReader, repository, fakeCatalogue{}), http.MethodGet, pluginRegistryPath("imports/"+job.Id.String()), nil, testToken)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"outcomes":[]`) || !strings.Contains(read.Body.String(), `"queued_ahead":0`) {
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
	// Well-formed but not a platform HashiCorp builds plugins for: defaults refuse it, imports do not.
	unknown := call(t, importHandler(identity.RoleMaintainer, repository, fakeCatalogue{}), http.MethodPut, pluginRegistryPath("default-platforms"),
		map[string]any{"platforms": []string{"linux_mips64"}}, testToken)
	if unknown.Code != http.StatusBadRequest || !strings.Contains(unknown.Body.String(), "HashiCorp builds Packer plugins for") {
		t.Fatalf("unknown platform as a default = %d %s, want 400", unknown.Code, unknown.Body.String())
	}
}

func TestResolveGithubRelease(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	repository.plugins = []store.PluginSummary{{Name: "git", Source: store.PluginSource{Kind: "releases-hashicorp", Repository: "packer-plugin-git"}}}
	release := pluginimport.GitHubRelease{
		Repository: "ethanmdavidson/packer-plugin-git", Name: "git", Tag: "v0.6.3", Version: "0.6.3", Platforms: []string{"linux_amd64"},
		HasChecksum: true, ChecksumAsset: "packer-plugin-git_v0.6.3_SHA256SUMS", PublishedAt: initTestTime,
	}
	path := pluginRegistryPath("catalogue/github/resolve")
	link := map[string]any{"release_url": "https://github.com/ethanmdavidson/packer-plugin-git/releases/latest"}

	response := call(t, importHandler(identity.RolePublisher, repository, fakeCatalogue{release: release}), http.MethodPost, path, link, testToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"tag":"v0.6.3"`) ||
		!strings.Contains(response.Body.String(), `"checksum_asset":"packer-plugin-git_v0.6.3_SHA256SUMS"`) ||
		!strings.Contains(response.Body.String(), `"published_at":"2026-07-30T10:00:00Z"`) ||
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

	// The name is held by releases.hashicorp.com, so the GitHub import is refused at queue time, not by the worker.
	held := importHandler(identity.RolePublisher, repository, fakeCatalogue{})
	github := map[string]any{"source": "github", "product": "ethanmdavidson/packer-plugin-git", "versions": []string{"v0.6.3"}, "platforms": []string{"linux_amd64"}}
	if response := call(t, held, http.MethodPost, pluginRegistryPath("imports"), github, testToken); response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), "git comes from releases-hashicorp packer-plugin-git") {
		t.Fatalf("github import of a held name = %d %s, want 409", response.Code, response.Body.String())
	}
	handler := importHandler(identity.RolePublisher, pluginRegistryRepository(store.PluginRegistry{Enabled: true}), fakeCatalogue{})
	if response := call(t, handler, http.MethodPost, pluginRegistryPath("imports"), github, testToken); response.Code != http.StatusAccepted {
		t.Fatalf("github import = %d %s", response.Code, response.Body.String())
	}
	github["product"] = "packer-plugin-git"
	if response := call(t, handler, http.MethodPost, pluginRegistryPath("imports"), github, testToken); response.Code != http.StatusBadRequest {
		t.Fatalf("github import without an owner = %d, want 400", response.Code)
	}
}

func TestSyncPluginQueuesOrderedChanges(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	repository.pluginVersions = map[string][]store.PluginVersionSummary{"amazon": {{Version: "1.8.1"}, {Version: "1.8.2"}}}
	repository.pluginSource = store.PluginSource{Kind: "releases-hashicorp", Repository: "packer-plugin-amazon"}
	handler, trail := auditedPlatform(t, importHandler(identity.RolePublisher, repository, fakeCatalogue{}))
	sync := pluginRegistryPath("plugins/amazon/sync")
	changes := []map[string]any{
		{"version": "1.8.2", "action": "add", "platforms": []string{"linux_arm64"}},
		{"version": "1.8.1", "action": "revoke"},
		{"version": "1.8.3", "action": "add", "platforms": []string{"linux_amd64"}},
	}
	response := call(t, handler, http.MethodPost, sync, map[string]any{"changes": changes}, testToken)
	var job PluginImport
	if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &job) != nil ||
		job.Source != "releases-hashicorp" || job.Product != "amazon" || len(job.Changes) != 3 || job.Changes[1].Action != Revoke {
		t.Fatalf("sync = %d %s", response.Code, response.Body.String())
	}
	if queued := repository.imports[len(repository.imports)-1].Request; len(queued.Versions) != 0 || queued.Changes[0].Platforms[0] != "linux_arm64" {
		t.Fatalf("queued request = %+v", queued)
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{"operation": "plugin.sync", "outcome": "success", "reason": "queued"})

	for name, tc := range map[string]struct {
		change map[string]any
		text   string
	}{
		"platform removal":  {map[string]any{"version": "1.8.2", "action": "revoke", "platforms": []string{"linux_amd64"}}, "a mirrored platform cannot be removed"},
		"revoke not stored": {map[string]any{"version": "9.9.9", "action": "revoke"}, "which is not stored"},
		"add no platforms":  {map[string]any{"version": "1.8.2", "action": "add"}, "without 1 to 32 distinct OS_ARCH platforms"},
		"unknown action":    {map[string]any{"version": "1.8.2", "action": "delete"}, "unknown action"},
	} {
		response := call(t, handler, http.MethodPost, sync, map[string]any{"changes": []map[string]any{tc.change}}, testToken)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), tc.text) {
			t.Fatalf("%s = %d %s, want 400 containing %q", name, response.Code, response.Body.String(), tc.text)
		}
	}
	twice := []map[string]any{{"version": "1.8.2", "action": "revoke"}, {"version": "1.8.2", "action": "restore"}}
	if response := call(t, handler, http.MethodPost, sync, map[string]any{"changes": twice}, testToken); response.Code != http.StatusBadRequest {
		t.Fatalf("one version changed twice = %d", response.Code)
	}

	repository.pluginSource = store.PluginSource{Kind: "github", Repository: "ethanmdavidson/packer-plugin-amazon"}
	newVersion := []map[string]any{{"version": "1.8.3", "action": "add", "platforms": []string{"linux_amd64"}}}
	if response := call(t, handler, http.MethodPost, sync, map[string]any{"changes": newVersion}, testToken); response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), "only releases.hashicorp.com plugins take new versions by sync") {
		t.Fatalf("new GitHub version by sync = %d %s", response.Code, response.Body.String())
	}
	if response := call(t, handler, http.MethodPost, pluginRegistryPath("plugins/nope/sync"), map[string]any{"changes": twice}, testToken); response.Code != http.StatusNotFound {
		t.Fatalf("unknown plugin = %d", response.Code)
	}
	builder := importHandler(identity.RoleBuilder, repository, fakeCatalogue{})
	if response := call(t, builder, http.MethodPost, sync, map[string]any{"changes": twice[:1]}, testToken); response.Code != http.StatusForbidden {
		t.Fatalf("builder sync = %d, want 403", response.Code)
	}
}

const pluginSyncJobFixture = "../../../web/tests/fixtures/plugin-sync-job.json"

// The console's sync job view is tested against this handler's response for
// a finished sync, written as a fixture (DUFFLEBAG_UPDATE_FIXTURES=1).
func TestPluginSyncJobFixtureMatchesTheHandler(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	repository.pluginVersions = map[string][]store.PluginVersionSummary{"amazon": {{Version: "1.8.1", Revoked: true}, {Version: "1.8.2"}}}
	repository.pluginSource = store.PluginSource{Kind: "releases-hashicorp", Repository: "packer-plugin-amazon"}
	handler := importHandler(identity.RolePublisher, repository, fakeCatalogue{})
	changes := []map[string]any{
		{"version": "1.8.3", "action": "add", "platforms": []string{"linux_amd64", "windows_386"}},
		{"version": "1.8.2", "action": "add", "platforms": []string{"linux_arm64"}},
		{"version": "1.8.1", "action": "restore"},
	}
	if response := call(t, handler, http.MethodPost, pluginRegistryPath("plugins/amazon/sync"), map[string]any{"changes": changes}, testToken); response.Code != http.StatusAccepted {
		t.Fatalf("sync = %d %s", response.Code, response.Body.String())
	}
	outcomes, err := json.Marshal([]pluginimport.VersionOutcome{
		{Version: "1.8.3", Outcome: pluginimport.OutcomeImported, Platforms: []pluginimport.PlatformOutcome{
			{Platform: "linux_amd64", Outcome: pluginimport.OutcomeImported},
			{Platform: "windows_386", Outcome: pluginimport.OutcomeFailed, Error: "not published upstream"},
		}},
		{Version: "1.8.2", Outcome: pluginimport.OutcomeFailed, Error: "packer-plugin-amazon_1.8.2_linux_arm64.zip: digest does not match SHA256SUMS"},
		{Version: "1.8.1", Outcome: pluginimport.OutcomeRestored},
	})
	if err != nil {
		t.Fatal(err)
	}
	job := &repository.imports[len(repository.imports)-1]
	job.State, job.Outcomes, job.FinishedAt = pluginimport.StatePartiallySucceeded, outcomes, &initTestTime
	response := call(t, handler, http.MethodGet, pluginRegistryPath("imports/"+job.ID.String()), nil, testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("read = %d %s", response.Code, response.Body.String())
	}
	var rendered map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &rendered); err != nil {
		t.Fatal(err)
	}
	rendered["id"] = "00000000-0000-0000-0000-000000000000"
	pretty, err := json.MarshalIndent(rendered, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	pretty = append(pretty, '\n')
	if os.Getenv("DUFFLEBAG_UPDATE_FIXTURES") != "" {
		if err := os.WriteFile(pluginSyncJobFixture, pretty, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fixture, err := os.ReadFile(pluginSyncJobFixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fixture, pretty) {
		t.Fatalf("web fixture drifted from the handler's response; regenerate with DUFFLEBAG_UPDATE_FIXTURES=1:\n%s", pretty)
	}
}

func TestPluginUpdateChecksAndCatalogueSync(t *testing.T) {
	checked := initTestTime
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	repository.plugins = []store.PluginSummary{
		{Name: "amazon", Source: store.PluginSource{Kind: "releases-hashicorp", Repository: "packer-plugin-amazon"},
			PublishedVersions: []string{"1.8.1"}, StoredVersions: []string{"1.8.1", "1.8.2"},
			Update: store.PluginUpdateCheck{Enabled: true, CheckedAt: &checked, Latest: "1.8.3", LatestTag: "1.8.3"}},
		{Name: "git", Source: store.PluginSource{Kind: "github", Repository: "ethanmdavidson/packer-plugin-git"},
			StoredVersions: []string{"0.6.3"}, Update: store.PluginUpdateCheck{Enabled: true, CheckedAt: &checked, Latest: "0.6.4", LatestTag: "v0.6.4"}},
		{Name: "docker", Source: store.PluginSource{Kind: "releases-hashicorp", Repository: "packer-plugin-docker"},
			StoredVersions: []string{"1.1.4"}, Update: store.PluginUpdateCheck{Enabled: true, CheckedAt: &checked, Latest: "1.1.4", Error: "releases.hashicorp.com could not be reached"}},
		{Name: "probe", Source: store.PluginSource{Kind: "upload"}, StoredVersions: []string{"1.0.0"}},
	}
	repository.pluginVersions = map[string][]store.PluginVersionSummary{
		"amazon": {{Version: "1.8.2", Stored: []string{"linux_amd64"}}, {Version: "1.8.1", Stored: []string{"darwin_arm64", "linux_amd64"}}},
		"git":    {{Version: "0.6.3", Stored: []string{"linux_arm64"}}},
	}
	handler, trail := auditedPlatform(t, importHandler(identity.RolePublisher, repository, fakeCatalogue{}))

	listed := call(t, handler, http.MethodGet, pluginRegistryPath("plugins"), nil, testToken)
	var body struct{ Plugins []Plugin }
	if listed.Code != http.StatusOK || json.Unmarshal(listed.Body.Bytes(), &body) != nil || len(body.Plugins) != 4 {
		t.Fatalf("list = %d %s", listed.Code, listed.Body.String())
	}
	for _, rendered := range body.Plugins {
		want := map[string]bool{"amazon": true, "git": true}[rendered.Name]
		if rendered.UpdateAvailable != want {
			t.Fatalf("%s update_available = %v, want %v", rendered.Name, rendered.UpdateAvailable, want)
		}
	}
	if docker := body.Plugins[2]; docker.UpdateCheck.Error == nil || *docker.UpdateCheck.Latest != "1.1.4" {
		t.Fatalf("docker's quiet failure = %+v", docker.UpdateCheck)
	}

	response := call(t, handler, http.MethodPost, pluginRegistryPath("sync"), map[string]any{"plugins": []string{"amazon", "probe", "git", "nope"}}, testToken)
	var synced struct{ Results []CatalogueSyncResult }
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &synced) != nil || len(synced.Results) != 4 {
		t.Fatalf("catalogue sync = %d %s", response.Code, response.Body.String())
	}
	if synced.Results[0].ImportId == nil || *synced.Results[0].Version != "1.8.3" ||
		*synced.Results[1].Refused != "no update available" || synced.Results[2].ImportId == nil || *synced.Results[3].Refused != "no such plugin" {
		t.Fatalf("results = %s", response.Body.String())
	}
	amazon, git := repository.imports[0].Request, repository.imports[1].Request
	if len(repository.imports) != 2 || amazon.SourceKind != "releases-hashicorp" || amazon.Product != "packer-plugin-amazon" ||
		amazon.Versions[0] != "1.8.3" || strings.Join(amazon.Platforms, ",") != "darwin_arm64,linux_amd64" {
		t.Fatalf("amazon job = %+v", amazon)
	}
	if git.SourceKind != "github" || git.Product != "ethanmdavidson/packer-plugin-git" || git.Versions[0] != "v0.6.4" || git.Platforms[0] != "linux_arm64" {
		t.Fatalf("git job = %+v, want the pinned tag", git)
	}
	// Each job of a Sync selected knows its place in the batch, counting only the jobs queued.
	first, second := repository.imports[0].Origin, repository.imports[1].Origin
	if first.Kind != "catalogue" || first.BatchIndex != 1 || first.BatchSize != 2 || second.BatchIndex != 2 || second.BatchSize != 2 || first.CreatedBy == "" {
		t.Fatalf("batch origins = %+v, %+v", first, second)
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{"operation": "plugin.catalogue.sync", "outcome": "success", "reason": "queued 2 of 4"})

	set := pluginRegistryPath("plugins/probe/update-check")
	if response := call(t, handler, http.MethodPut, set, map[string]any{"enabled": true}, testToken); response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), "no upstream") {
		t.Fatalf("update check on an upload = %d %s", response.Code, response.Body.String())
	}
	if response := call(t, handler, http.MethodPut, pluginRegistryPath("plugins/amazon/update-check"), map[string]any{"enabled": false}, testToken); response.Code != http.StatusNoContent || repository.plugins[0].Update.Enabled {
		t.Fatalf("turn off = %d", response.Code)
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{"operation": "plugin.update_check.set", "outcome": "success", "reason": "disabled"})
	builder := importHandler(identity.RoleBuilder, repository, fakeCatalogue{})
	for path, method := range map[string]string{pluginRegistryPath("sync"): http.MethodPost, pluginRegistryPath("plugins/amazon/update-check"): http.MethodPut} {
		if response := call(t, builder, method, path, map[string]any{"plugins": []string{"git"}, "enabled": true}, testToken); response.Code != http.StatusForbidden {
			t.Fatalf("builder %s %s = %d, want 403", method, path, response.Code)
		}
	}
}

const pluginCatalogueFixture = "../../../web/tests/fixtures/plugin-catalogue.json"

// The console's catalogue is tested against this handler's response, written
// as a fixture (DUFFLEBAG_UPDATE_FIXTURES=1).
func TestPluginCatalogueFixtureMatchesTheHandler(t *testing.T) {
	checked := initTestTime
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true, Exposed: true})
	repository.plugins = []store.PluginSummary{
		{Name: "amazon", Source: store.PluginSource{Kind: "releases-hashicorp", Repository: "packer-plugin-amazon"},
			PublishedVersions: []string{"1.8.2"}, StoredVersions: []string{"1.8.2"},
			Update: store.PluginUpdateCheck{Enabled: true, CheckedAt: &checked, Latest: "1.8.3", LatestTag: "1.8.3"}},
		{Name: "docker", Source: store.PluginSource{Kind: "releases-hashicorp", Repository: "packer-plugin-docker"},
			PublishedVersions: []string{"1.1.4"}, StoredVersions: []string{"1.1.4"},
			Update: store.PluginUpdateCheck{Enabled: true, CheckedAt: &checked, Latest: "1.1.4", Error: "releases.hashicorp.com could not be reached"}},
		{Name: "git", Source: store.PluginSource{Kind: "github", Repository: "ethanmdavidson/packer-plugin-git"},
			PublishedVersions: []string{"0.6.3"}, StoredVersions: []string{"0.6.3"}},
		{Name: "probe", Source: store.PluginSource{Kind: "upload"}, PublishedVersions: []string{"1.0.0"}, StoredVersions: []string{"1.0.0"}},
	}
	response := call(t, importHandler(identity.RoleReader, repository, fakeCatalogue{}), http.MethodGet, pluginRegistryPath("plugins"), nil, testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("list = %d %s", response.Code, response.Body.String())
	}
	var rendered any
	if err := json.Unmarshal(response.Body.Bytes(), &rendered); err != nil {
		t.Fatal(err)
	}
	pretty, err := json.MarshalIndent(rendered, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	pretty = append(pretty, '\n')
	if os.Getenv("DUFFLEBAG_UPDATE_FIXTURES") != "" {
		if err := os.WriteFile(pluginCatalogueFixture, pretty, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fixture, err := os.ReadFile(pluginCatalogueFixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fixture, pretty) {
		t.Fatalf("web fixture drifted from the handler's response; regenerate with DUFFLEBAG_UPDATE_FIXTURES=1:\n%s", pretty)
	}
}

func TestPluginImportsListNewestFirstWithFilters(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	handler, trail := auditedPlatform(t, importHandler(identity.RolePublisher, repository, fakeCatalogue{}))
	queue := func(body map[string]any) PluginImport {
		t.Helper()
		response := call(t, handler, http.MethodPost, pluginRegistryPath("imports"), body, testToken)
		var job PluginImport
		if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &job) != nil {
			t.Fatalf("queue = %d %s", response.Code, response.Body.String())
		}
		return job
	}
	amazon := queue(map[string]any{"source": "releases-hashicorp", "product": "packer-plugin-amazon", "versions": []string{"1.8.2"}, "platforms": []string{"linux_amd64"}})
	docker := queue(map[string]any{"source": "github", "product": "acme-infra/packer-plugin-docker", "versions": []string{"v1.0.0"}, "platforms": []string{"linux_amd64"}})
	if amazon.Plugin != "amazon" || docker.Plugin != "docker" || amazon.StartedAt != nil {
		t.Fatalf("plugin names = %q %q, started %v", amazon.Plugin, docker.Plugin, amazon.StartedAt)
	}
	list := func(role identity.Role, query string) (int, ListPluginImports200JSONResponse) {
		t.Helper()
		response := call(t, importHandler(role, repository, fakeCatalogue{}), http.MethodGet, pluginRegistryPath("imports"+query), nil, testToken)
		var page ListPluginImports200JSONResponse
		if response.Code == http.StatusOK && json.Unmarshal(response.Body.Bytes(), &page) != nil {
			t.Fatalf("list %s: %s", query, response.Body.String())
		}
		return response.Code, page
	}
	if code, page := list(identity.RoleReader, ""); code != http.StatusOK || page.Total != 2 || len(page.Imports) != 2 || page.Imports[0].Id != docker.Id || page.Imports[1].Id != amazon.Id {
		t.Fatalf("list = %d total %d %+v, want newest first", code, page.Total, page.Imports)
	}
	for query, want := range map[string]int{"?state=queued": 2, "?state=unfinished": 2, "?state=succeeded": 0, "?plugin=amazon": 1, "?plugin=qemu": 0} {
		if code, page := list(identity.RoleReader, query); code != http.StatusOK || page.Total != want || len(page.Imports) != want {
			t.Fatalf("list %s = %d total %d, %d rows, want %d", query, code, page.Total, len(page.Imports), want)
		}
	}
	if code, page := list(identity.RoleReader, "?limit=1&offset=1"); code != http.StatusOK || page.Total != 2 || len(page.Imports) != 1 || page.Imports[0].Id != amazon.Id {
		t.Fatalf("second page = %d total %d %+v", code, page.Total, page.Imports)
	}
	for _, query := range []string{"?limit=0", "?limit=101", "?offset=-1"} {
		if code, _ := list(identity.RoleReader, query); code != http.StatusBadRequest {
			t.Fatalf("list %s = %d, want 400", query, code)
		}
	}
	call(t, handler, http.MethodGet, pluginRegistryPath("imports"), nil, testToken)
	assertPlatformAudit(t, trail.response(t), map[string]any{"operation": "plugin.import.list", "outcome": "success"})
}

func TestPluginImportRefusesDuplicatesAndReservedNames(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	handler, trail := auditedPlatform(t, importHandler(identity.RolePublisher, repository, fakeCatalogue{plugins: []string{"packer-plugin-qemu"}}))
	imports := pluginRegistryPath("imports")
	qemu := map[string]any{"source": "github", "product": "acme-infra/packer-plugin-qemu", "versions": []string{"v1.0.0"}, "platforms": []string{"linux_arm64", "linux_amd64"}}
	first := call(t, handler, http.MethodPost, imports, qemu, testToken)
	var job PluginImport
	if first.Code != http.StatusAccepted || json.Unmarshal(first.Body.Bytes(), &job) != nil {
		t.Fatalf("first = %d %s", first.Code, first.Body.String())
	}
	// The same request, platforms in another order, is the same job.
	qemu["platforms"] = []string{"linux_amd64", "linux_arm64"}
	var conflict PluginImportConflict
	if again := call(t, handler, http.MethodPost, imports, qemu, testToken); again.Code != http.StatusConflict ||
		json.Unmarshal(again.Body.Bytes(), &conflict) != nil || conflict.ImportId == nil || *conflict.ImportId != job.Id || !strings.Contains(conflict.Message, "identical job for qemu is already queued") {
		t.Fatalf("identical import = %d %s", again.Code, again.Body.String())
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{"operation": "plugin.import.create", "outcome": "failure", "reason": "already_queued"})
	// One more platform is a different job and queues behind the first.
	qemu["platforms"] = []string{"linux_amd64", "linux_arm64", "darwin_arm64"}
	if more := call(t, handler, http.MethodPost, imports, qemu, testToken); more.Code != http.StatusAccepted {
		t.Fatalf("overlapping import = %d %s", more.Code, more.Body.String())
	}
	// The name is reserved for GitHub while the import is unfinished: HashiCorp's qemu is refused and shown as held.
	hashicorp := map[string]any{"source": "releases-hashicorp", "product": "packer-plugin-qemu", "versions": []string{"1.1.0"}, "platforms": []string{"linux_amd64"}}
	if reserved := call(t, handler, http.MethodPost, imports, hashicorp, testToken); reserved.Code != http.StatusConflict ||
		json.Unmarshal(reserved.Body.Bytes(), &conflict) != nil || conflict.ImportId == nil || *conflict.ImportId != job.Id ||
		!strings.Contains(conflict.Message, "qemu is reserved by a queued job from github acme-infra/packer-plugin-qemu") {
		t.Fatalf("reserved name = %d %s", reserved.Code, reserved.Body.String())
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{"operation": "plugin.import.create", "outcome": "failure", "reason": "name_reserved"})
	if catalogue := call(t, handler, http.MethodGet, pluginRegistryPath("catalogue/hashicorp"), nil, testToken); catalogue.Code != http.StatusOK ||
		!strings.Contains(catalogue.Body.String(), `"held_by":{"kind":"github","repository":"acme-infra/packer-plugin-qemu"}`) {
		t.Fatalf("catalogue = %d %s, want qemu held by the queued GitHub import", catalogue.Code, catalogue.Body.String())
	}
	// retry_of must name one of the organization's jobs, and is echoed.
	retry := map[string]any{"source": "releases-hashicorp", "product": "packer-plugin-amazon", "versions": []string{"1.8.2"}, "platforms": []string{"linux_amd64"}, "retry_of": uuid.NewString()}
	if unknown := call(t, handler, http.MethodPost, imports, retry, testToken); unknown.Code != http.StatusBadRequest || !strings.Contains(unknown.Body.String(), "retry_of names no job") {
		t.Fatalf("unknown retry_of = %d %s", unknown.Code, unknown.Body.String())
	}
	retry["retry_of"] = job.Id.String()
	var retried PluginImport
	if response := call(t, handler, http.MethodPost, imports, retry, testToken); response.Code != http.StatusAccepted ||
		json.Unmarshal(response.Body.Bytes(), &retried) != nil || retried.RetryOf == nil || *retried.RetryOf != job.Id {
		t.Fatalf("retry = %d %s", response.Code, response.Body.String())
	}
}

func TestSyncPluginRefusesAnIdenticalUnfinishedSync(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	repository.pluginVersions = map[string][]store.PluginVersionSummary{"amazon": {{Version: "1.8.2"}}}
	repository.pluginSource = store.PluginSource{Kind: "releases-hashicorp", Repository: "packer-plugin-amazon"}
	handler := importHandler(identity.RolePublisher, repository, fakeCatalogue{})
	sync := pluginRegistryPath("plugins/amazon/sync")
	body := map[string]any{"changes": []map[string]any{{"version": "1.8.3", "action": "add", "platforms": []string{"linux_amd64"}}}}
	first := call(t, handler, http.MethodPost, sync, body, testToken)
	var job PluginImport
	if first.Code != http.StatusAccepted || json.Unmarshal(first.Body.Bytes(), &job) != nil || job.Plugin != "amazon" {
		t.Fatalf("sync = %d %s", first.Code, first.Body.String())
	}
	var conflict PluginImportConflict
	if again := call(t, handler, http.MethodPost, sync, body, testToken); again.Code != http.StatusConflict ||
		json.Unmarshal(again.Body.Bytes(), &conflict) != nil || conflict.ImportId == nil || *conflict.ImportId != job.Id {
		t.Fatalf("identical sync = %d %s", again.Code, again.Body.String())
	}
	body["changes"] = []map[string]any{{"version": "1.8.3", "action": "add", "platforms": []string{"linux_arm64"}}}
	body["retry_of"] = job.Id.String()
	var retried PluginImport
	if other := call(t, handler, http.MethodPost, sync, body, testToken); other.Code != http.StatusAccepted ||
		json.Unmarshal(other.Body.Bytes(), &retried) != nil || retried.RetryOf == nil || *retried.RetryOf != job.Id {
		t.Fatalf("a different sync with retry_of = %d %s", other.Code, other.Body.String())
	}
}

func TestPluginImportsClearUnlessRunning(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	handler, trail := auditedPlatform(t, importHandler(identity.RolePublisher, repository, fakeCatalogue{}))
	queue := func(product string) PluginImport {
		t.Helper()
		body := map[string]any{"source": "releases-hashicorp", "product": product, "versions": []string{"1.0.0"}, "platforms": []string{"linux_amd64"}}
		response := call(t, handler, http.MethodPost, pluginRegistryPath("imports"), body, testToken)
		var job PluginImport
		if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &job) != nil {
			t.Fatalf("queue = %d %s", response.Code, response.Body.String())
		}
		return job
	}
	queued, running := queue("packer-plugin-amazon"), queue("packer-plugin-qemu")
	repository.imports[1].State = "running"
	for name, tc := range map[string]struct {
		role identity.Role
		id   string
		want int
	}{
		"reader cannot clear":  {identity.RoleReader, queued.Id.String(), http.StatusForbidden},
		"builder cannot clear": {identity.RoleBuilder, queued.Id.String(), http.StatusForbidden},
		"unknown job":          {identity.RolePublisher, uuid.NewString(), http.StatusNotFound},
		"running job":          {identity.RolePublisher, running.Id.String(), http.StatusConflict},
	} {
		if response := call(t, importHandler(tc.role, repository, fakeCatalogue{}), http.MethodDelete, pluginRegistryPath("imports/"+tc.id), nil, testToken); response.Code != tc.want {
			t.Fatalf("%s = %d %s, want %d", name, response.Code, response.Body.String(), tc.want)
		}
	}
	if response := call(t, handler, http.MethodDelete, pluginRegistryPath("imports/"+queued.Id.String()), nil, testToken); response.Code != http.StatusNoContent {
		t.Fatalf("clear a queued job = %d %s", response.Code, response.Body.String())
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{"operation": "plugin.import.delete", "outcome": "success", "reason": "cleared"})
	if len(repository.imports) != 1 || repository.imports[0].ID != running.Id {
		t.Fatalf("imports after clear = %+v", repository.imports)
	}
	if response := call(t, handler, http.MethodGet, pluginRegistryPath("imports/"+queued.Id.String()), nil, testToken); response.Code != http.StatusNotFound {
		t.Fatalf("a cleared job reads as %d, want 404", response.Code)
	}
}

func TestPluginImportsListTakesSeveralStatesAndPlugins(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	handler := importHandler(identity.RolePublisher, repository, fakeCatalogue{})
	for _, product := range []string{"packer-plugin-amazon", "packer-plugin-qemu", "packer-plugin-vsphere"} {
		body := map[string]any{"source": "releases-hashicorp", "product": product, "versions": []string{"1.0.0"}, "platforms": []string{"linux_amd64"}}
		if response := call(t, handler, http.MethodPost, pluginRegistryPath("imports"), body, testToken); response.Code != http.StatusAccepted {
			t.Fatalf("queue %s = %d", product, response.Code)
		}
	}
	repository.imports[0].State = "failed"
	repository.imports[1].State = "succeeded"
	for query, want := range map[string]int{
		"?state=failed,succeeded": 2, "?state=queued": 1, "?state=unfinished,failed": 2,
		"?plugin=amazon,vsphere": 2, "?plugin=amazon,vsphere&state=failed": 1, "?plugin=qemu&state=failed": 0,
	} {
		response := call(t, handler, http.MethodGet, pluginRegistryPath("imports"+query), nil, testToken)
		var page ListPluginImports200JSONResponse
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Total != want {
			t.Fatalf("list %s = %d total %d, want %d: %s", query, response.Code, page.Total, want, response.Body.String())
		}
	}
}

func TestPluginPruneScheduleAndPruneNow(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	repository.prune = store.PluginPrune{AgeDays: 30, Schedule: "0 3 * * *"}
	old, recent := initTestTime.AddDate(0, 0, -40), initTestTime.AddDate(0, 0, -2)
	repository.imports = []store.PluginImport{
		{ID: uuid.New(), Plugin: "amazon", State: "succeeded", FinishedAt: &old, Outcomes: []byte("[]"), CreatedAt: old},
		{ID: uuid.New(), Plugin: "qemu", State: "failed", FinishedAt: &recent, Outcomes: []byte("[]"), CreatedAt: recent},
		{ID: uuid.New(), Plugin: "vsphere", State: "queued", Outcomes: []byte("[]"), CreatedAt: initTestTime},
	}
	handler, trail := auditedPlatform(t, importHandler(identity.RoleMaintainer, repository, fakeCatalogue{}))
	read := call(t, handler, http.MethodGet, pluginRegistryPath("prune"), nil, testToken)
	var prune PluginPrune
	if read.Code != http.StatusOK || json.Unmarshal(read.Body.Bytes(), &prune) != nil || prune.Enabled || prune.AgeDays != 30 || prune.NextRunAt != nil ||
		prune.WouldClear != 1 || prune.Unfinished != 1 || prune.Kept != 3 {
		t.Fatalf("prune = %d %s", read.Code, read.Body.String())
	}
	for _, role := range []identity.Role{identity.RoleReader, identity.RolePublisher} {
		if response := call(t, importHandler(role, repository, fakeCatalogue{}), http.MethodGet, pluginRegistryPath("prune"), nil, testToken); response.Code != http.StatusForbidden {
			t.Fatalf("%s reads the prune schedule = %d, want 403", role, response.Code)
		}
	}
	for name, body := range map[string]map[string]any{
		"age off the list": {"enabled": true, "age_days": 31, "schedule": "0 3 * * *"},
		"four fields":      {"enabled": true, "age_days": 30, "schedule": "0 3 * *"},
		"hour 25":          {"enabled": true, "age_days": 30, "schedule": "0 25 * * *"},
	} {
		if response := call(t, handler, http.MethodPut, pluginRegistryPath("prune"), body, testToken); response.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s, want 400", name, response.Code, response.Body.String())
		}
	}
	on := call(t, handler, http.MethodPut, pluginRegistryPath("prune"), map[string]any{"enabled": true, "age_days": 7, "schedule": "0 */6 * * *"}, testToken)
	if on.Code != http.StatusOK || json.Unmarshal(on.Body.Bytes(), &prune) != nil || !prune.Enabled || prune.AgeDays != 7 || prune.Schedule != "0 */6 * * *" ||
		prune.NextRunAt == nil || !prune.NextRunAt.Equal(time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)) || prune.WouldClear != 1 {
		t.Fatalf("turn on = %d %s", on.Code, on.Body.String())
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{"operation": "plugin_registry.prune.configure", "outcome": "success", "reason": "on"})
	off := call(t, handler, http.MethodPut, pluginRegistryPath("prune"), map[string]any{"enabled": false, "age_days": 7, "schedule": "0 */6 * * *"}, testToken)
	prune = PluginPrune{}
	if off.Code != http.StatusOK || json.Unmarshal(off.Body.Bytes(), &prune) != nil || prune.Enabled || prune.NextRunAt != nil {
		t.Fatalf("turn off = %d %s, want no next run", off.Code, off.Body.String())
	}
	// Prune now clears the finished job older than the limit, keeps the recent and the queued, and records the run.
	run := call(t, handler, http.MethodPost, pluginRegistryPath("prune/run"), nil, testToken)
	prune = PluginPrune{}
	if run.Code != http.StatusOK || json.Unmarshal(run.Body.Bytes(), &prune) != nil || prune.LastRunCleared != 1 || prune.LastRunAt == nil || prune.LastError != "" ||
		prune.WouldClear != 0 || prune.Kept != 2 || prune.Unfinished != 1 {
		t.Fatalf("prune now = %d %s", run.Code, run.Body.String())
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{"operation": "plugin.import.prune", "outcome": "success", "reason": "cleared 1"})
	if len(repository.imports) != 2 || repository.imports[0].Plugin != "qemu" || repository.imports[1].Plugin != "vsphere" {
		t.Fatalf("imports after prune = %+v", repository.imports)
	}
	if response := call(t, importHandler(identity.RolePublisher, repository, fakeCatalogue{}), http.MethodPost, pluginRegistryPath("prune/run"), nil, testToken); response.Code != http.StatusForbidden {
		t.Fatalf("publisher prunes = %d, want 403", response.Code)
	}
	disabled := pluginRegistryRepository(store.PluginRegistry{})
	if response := call(t, importHandler(identity.RoleMaintainer, disabled, fakeCatalogue{}), http.MethodGet, pluginRegistryPath("prune"), nil, testToken); response.Code != http.StatusConflict {
		t.Fatalf("prune of a disabled registry = %d, want 409", response.Code)
	}
}

func TestPluginListSaysWhenTheNextCheckIsDue(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	checked := initTestTime.Add(-20 * time.Hour)
	repository.plugins = []store.PluginSummary{
		{Name: "amazon", Source: store.PluginSource{Kind: "releases-hashicorp", Repository: "packer-plugin-amazon"}, Update: store.PluginUpdateCheck{Enabled: true, CheckedAt: &checked}},
		{Name: "docker", Source: store.PluginSource{Kind: "github", Repository: "acme-infra/packer-plugin-docker"}, Update: store.PluginUpdateCheck{Enabled: true}},
		{Name: "qemu", Source: store.PluginSource{Kind: "releases-hashicorp", Repository: "packer-plugin-qemu"}, Update: store.PluginUpdateCheck{Enabled: false, CheckedAt: &checked}},
		{Name: "git", Source: store.PluginSource{Kind: "upload"}, Update: store.PluginUpdateCheck{Enabled: true}},
	}
	response := call(t, importHandler(identity.RoleReader, repository, fakeCatalogue{}), http.MethodGet, pluginRegistryPath("plugins"), nil, testToken)
	var body struct{ Plugins []Plugin }
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body.Plugins) != 4 {
		t.Fatalf("plugins = %d %s", response.Code, response.Body.String())
	}
	next := map[string]*time.Time{}
	for _, plugin := range body.Plugins {
		next[plugin.Name] = plugin.UpdateCheck.NextCheckAt
	}
	if next["amazon"] == nil || !next["amazon"].Equal(checked.Add(DefaultPluginUpdateInterval)) {
		t.Fatalf("amazon next check = %v, want the last check plus the interval", next["amazon"])
	}
	if next["docker"] == nil || !next["docker"].Equal(initTestTime) {
		t.Fatalf("a never-checked plugin is due now, got %v", next["docker"])
	}
	if next["qemu"] != nil || next["git"] != nil {
		t.Fatalf("checks off or an upload carry no next check: %v %v", next["qemu"], next["git"])
	}
}
