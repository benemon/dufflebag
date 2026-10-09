package v1

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

type uploadPart struct {
	field, filename string
	data            []byte
}

// probeZip is goreleaser-shaped, as packer-plugin-git v0.6.3 ships: the
// protocol is in the zip name and the binary inside carries the same name.
func probeZip(t *testing.T, version, platform string) uploadPart {
	t.Helper()
	return probeZipWith(t, version, platform, "binary")
}

func probeZipWith(t *testing.T, version, platform, content string) uploadPart {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	entry, err := archive.Create(fmt.Sprintf("packer-plugin-probe_v%s_x5.0_%s", version, platform))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte(content))
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return uploadPart{"zips", fmt.Sprintf("packer-plugin-probe_v%s_x5.0_%s.zip", version, platform), buffer.Bytes()}
}

func sumsFor(parts ...uploadPart) uploadPart {
	var lines strings.Builder
	for _, part := range parts {
		sum := sha256.Sum256(part.data)
		fmt.Fprintf(&lines, "%s  %s\n", hex.EncodeToString(sum[:]), part.filename)
	}
	return uploadPart{"sha256sums", "packer-plugin-probe_v1.0.0_SHA256SUMS", []byte(lines.String())}
}

func publishRequest(t *testing.T, handler http.Handler, version string, parts ...uploadPart) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for _, part := range parts {
		writer, err := form.CreateFormFile(part.field, part.filename)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = writer.Write(part.data)
	}
	_ = form.Close()
	request := httptest.NewRequest(http.MethodPut,
		"/api/v1/organizations/"+testOrgID+"/plugin-registry/plugins/probe/versions/"+version, &body)
	request.Host = "dufflebag.example.com:8443"
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+testToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func publisherHandler(repository *fakeTenancyRepository, role identity.Role) http.Handler {
	return pluginRegistryHandler(role, repository)
}

func TestPublishPluginVersionStoresTheVerifiedFileSet(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	handler, trail := auditedPlatform(t, publisherHandler(repository, identity.RolePublisher))
	linux := probeZip(t, "1.0.0", "linux_amd64")
	darwin := probeZip(t, "1.0.0", "darwin_arm64")
	sums := sumsFor(linux, darwin)

	response := publishRequest(t, handler, "1.0.0", sums, linux)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d %s", response.Code, response.Body.String())
	}
	var published PublishedPluginVersion
	if err := json.Unmarshal(response.Body.Bytes(), &published); err != nil {
		t.Fatal(err)
	}
	if published.Stanza.Source != "dufflebag.example.com/plugins/organization/probe" || published.Stanza.Version != "1.0.0" ||
		!strings.Contains(published.Stanza.Hcl, `source  = "dufflebag.example.com/plugins/organization/probe"`) {
		t.Fatalf("stanza = %+v", published.Stanza)
	}
	if len(published.Version.ListedPlatforms) != 2 || len(published.Version.StoredPlatforms) != 1 {
		t.Fatalf("coverage = %+v", published.Version)
	}
	if len(repository.publishedPlugins) != 1 {
		t.Fatalf("repository received %d versions", len(repository.publishedPlugins))
	}
	input := repository.publishedPlugins[0]
	if input.Source.Kind != "upload" || len(input.Zips) != 1 || input.Zips[0].OS != "linux" || input.Protocol != "" {
		t.Fatalf("input = %+v", input)
	}
	if !bytes.Equal(repository.publishedSums[0], sums.data) {
		t.Fatalf("stored sums differ from the uploaded bytes")
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{
		"operation": "plugin.version.publish", "outcome": "success", "reason": "published",
	})
}

func TestPublishPluginVersionRoleAxis(t *testing.T) {
	linux := probeZip(t, "1.0.0", "linux_amd64")
	for _, tc := range []struct {
		role identity.Role
		want int
	}{
		{identity.RoleReader, http.StatusForbidden},
		{identity.RoleBuilder, http.StatusForbidden},
		{identity.RolePublisher, http.StatusCreated},
		{identity.RoleMaintainer, http.StatusCreated},
	} {
		t.Run(string(tc.role), func(t *testing.T) {
			repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
			handler, trail := auditedPlatform(t, publisherHandler(repository, tc.role))
			response := publishRequest(t, handler, "1.0.0", sumsFor(linux), linux)
			if response.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, tc.want, response.Body.String())
			}
			if tc.want == http.StatusForbidden {
				assertPlatformAudit(t, trail.response(t), map[string]any{"outcome": "refused", "reason": "role_refused"})
				if len(repository.publishedPlugins) != 0 {
					t.Fatal("a refused caller published")
				}
			}
		})
	}
}

func TestPublishPluginVersionTenancyAxis(t *testing.T) {
	linux := probeZip(t, "1.0.0", "linux_amd64")
	for name, roles := range map[string]testRoles{
		"foreign organization": {role: identity.RolePublisher, scope: identity.Scope{OrganizationID: uuid.New()}},
		"project-bound publisher": {role: identity.RolePublisher, scope: identity.Scope{
			OrganizationID: uuid.MustParse(testOrgID), ProjectID: uuid.MustParse(testProjID),
		}},
	} {
		t.Run(name, func(t *testing.T) {
			repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
			handler := newHandler(repository, &fakeInstanceRepository{}, testAuth{}, roles, testLogger(),
				func() time.Time { return initTestTime })
			response := publishRequest(t, handler, "1.0.0", sumsFor(linux), linux)
			if response.Code != http.StatusNotFound || len(repository.publishedPlugins) != 0 {
				t.Fatalf("status = %d, published = %d; want 404 and nothing stored", response.Code, len(repository.publishedPlugins))
			}
		})
	}
}

func TestPublishPluginVersionRefusals(t *testing.T) {
	linux := probeZip(t, "1.0.0", "linux_amd64")
	altered := probeZipWith(t, "1.0.0", "linux_amd64", "a different binary")
	notZip := uploadPart{"zips", linux.filename, []byte("not a zip")}
	for _, tc := range []struct {
		name  string
		parts []uploadPart
		want  string
	}{
		{"no sums", []uploadPart{linux}, "sha256sums: is required"},
		{"field outside the allowlist", []uploadPart{sumsFor(linux), linux, {"index", "index.json", []byte("{}")}}, "index: is not one of"},
		{"digest mismatch", []uploadPart{sumsFor(linux), altered}, linux.filename + ": digest does not match"},
		{"zip not listed", []uploadPart{sumsFor(probeZip(t, "1.0.0", "darwin_arm64")), linux}, linux.filename + ": is not listed"},
		{"not a zip", []uploadPart{sumsFor(notZip), notZip}, linux.filename + ": is not a zip archive"},
		{"zip without a filename", []uploadPart{sumsFor(linux), {"zips", "", linux.data}}, "every zip needs a filename"},
		{"sums uploaded twice", []uploadPart{sumsFor(linux), sumsFor(linux), linux}, "sha256sums: is uploaded twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
			handler, trail := auditedPlatform(t, publisherHandler(repository, identity.RolePublisher))
			response := publishRequest(t, handler, "1.0.0", tc.parts...)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), tc.want) {
				t.Fatalf("status = %d %s, want 400 naming %q", response.Code, response.Body.String(), tc.want)
			}
			if len(repository.publishedPlugins) != 0 {
				t.Fatal("a refused upload was stored")
			}
			assertPlatformAudit(t, trail.response(t), map[string]any{"outcome": "failure", "reason": "invalid_request"})
		})
	}
}

func TestPublishPluginVersionConflictsAndUnavailableStorage(t *testing.T) {
	linux := probeZip(t, "1.0.0", "linux_amd64")
	for _, tc := range []struct {
		name   string
		state  store.PluginRegistry
		err    error
		status int
		want   string
	}{
		{"not enabled", store.PluginRegistry{}, nil, http.StatusConflict, "not enabled"},
		{"version exists", store.PluginRegistry{Enabled: true}, store.ErrPluginVersionExists, http.StatusConflict, "versions are immutable"},
		{"source held", store.PluginRegistry{Enabled: true},
			store.HeldSourceError{Source: store.PluginSource{Kind: "github", Repository: "someone/packer-plugin-probe"}},
			http.StatusConflict, "held by another source (github someone/packer-plugin-probe); a name belongs to one source per organization and is freed when every version is removed"},
		{"object storage", store.PluginRegistry{Enabled: true}, store.ErrObjectStorageNotConfigured, http.StatusServiceUnavailable, "object storage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository := pluginRegistryRepository(tc.state)
			repository.publishPluginErr = tc.err
			response := publishRequest(t, publisherHandler(repository, identity.RolePublisher), "1.0.0", sumsFor(linux), linux)
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.want) {
				t.Fatalf("status = %d %s, want %d containing %q", response.Code, response.Body.String(), tc.status, tc.want)
			}
		})
	}
}

func TestPublishPluginVersionRefusesAnOversizedUpload(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	actor := testRoles{role: identity.RolePublisher, scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)}}
	handler := newHandlerWithServices(repository, &fakeInstanceRepository{}, testAuth{}, actor, testLogger(),
		nil, nil, nil, nil, nil, nil, BuildInfo{}, 1024, func() time.Time { return initTestTime })
	large := uploadPart{"zips", "packer-plugin-probe_v1.0.0_x5.0_linux_amd64.zip", bytes.Repeat([]byte("x"), 4096)}
	response := publishRequest(t, handler, "1.0.0", sumsFor(large), large)
	if response.Code != http.StatusRequestEntityTooLarge || len(repository.publishedPlugins) != 0 {
		t.Fatalf("status = %d %s, want 413 and nothing stored", response.Code, response.Body.String())
	}
}

func TestListPluginsAndVersions(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	repository.plugins = []store.PluginSummary{{Name: "probe", Source: store.PluginSource{Kind: "upload"}, PublishedVersions: []string{"1.9.0", "1.10.0"}}}
	repository.pluginVersions = map[string][]store.PluginVersionSummary{"probe": {{
		Version: "1.9.0", Listed: []string{"linux_amd64"}, Stored: []string{"linux_amd64"}, CreatedAt: initTestTime,
	}, {
		Version: "1.10.0", Listed: []string{"linux_amd64", "darwin_arm64"}, Stored: []string{"linux_amd64"}, CreatedAt: initTestTime,
	}}}
	handler := pluginRegistryHandler(identity.RoleReader, repository)

	response := call(t, handler, http.MethodGet, pluginRegistryPath("plugins"), nil, testToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"newest_version":"1.10.0"`) ||
		!strings.Contains(response.Body.String(), `"published_versions":2`) {
		t.Fatalf("plugins = %d %s", response.Code, response.Body.String())
	}
	response = call(t, handler, http.MethodGet, pluginRegistryPath("plugins/probe/versions"), nil, testToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"stored_platforms":[{"arch":"amd64","os":"linux"}]`) ||
		strings.Index(response.Body.String(), `"1.10.0"`) > strings.Index(response.Body.String(), `"1.9.0"`) {
		t.Fatalf("versions = %d %s", response.Code, response.Body.String())
	}
	response = call(t, handler, http.MethodGet, pluginRegistryPath("plugins/absent/versions"), nil, testToken)
	if response.Code != http.StatusNotFound {
		t.Fatalf("absent plugin = %d, want 404", response.Code)
	}
}
