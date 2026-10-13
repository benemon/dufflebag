package v1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func importBundleRequest(t *testing.T, handler http.Handler, filename string, data []byte, fields ...string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	// Extra fields come in pairs; a pair before the file goes before the file part.
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i+1] == "" {
			continue
		}
		_ = form.WriteField(fields[i], fields[i+1])
	}
	if filename != "" {
		writer, err := form.CreateFormFile("bundle", filename)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = writer.Write(data)
	}
	_ = form.Close()
	request := httptest.NewRequest(http.MethodPut, pluginRegistryPath("bundle/import"), &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+testToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestPluginBundleImportStoresTheUploadAndQueuesOnce(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	handler, trail := auditedPlatform(t, pluginRegistryHandler(identity.RoleMaintainer, repository))
	if response := call(t, handler, http.MethodGet, pluginRegistryPath("bundle/import"), nil, testToken); response.Code != http.StatusNotFound {
		t.Fatalf("import before any upload = %d, want 404", response.Code)
	}
	archive := []byte("gzip bytes of a bundle")
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])
	filename := "dufflebag-plugins-acme-20261013T090040Z-" + digest[:12] + ".tar.gz"
	queued := importBundleRequest(t, handler, filename, archive)
	var job PluginBundleImport
	if queued.Code != http.StatusAccepted || json.Unmarshal(queued.Body.Bytes(), &job) != nil ||
		job.State != PluginBundleImportStateQueued || job.Filename != filename || job.Sha256 != digest || job.Size != int64(len(archive)) ||
		job.FilenameMatches == nil || !*job.FilenameMatches || job.Bundle != nil || len(job.Outcomes) != 0 {
		t.Fatalf("import = %d %s", queued.Code, queued.Body.String())
	}
	if stored := repository.bundleObjects[repository.bundleImport.Upload.ObjectKey]; stored != string(archive) {
		t.Fatalf("stored bundle = %q, want the upload under its object key", stored)
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{
		"operation": "plugin_registry.bundle.import", "target_type": "plugin_registry", "outcome": "success", "reason": filename,
	})
	again := importBundleRequest(t, handler, "renamed.tar.gz", []byte("another"))
	if again.Code != http.StatusConflict {
		t.Fatalf("second import while queued = %d %s, want 409", again.Code, again.Body.String())
	}
	if len(repository.bundleObjects) != 1 {
		t.Fatalf("objects after a refused import = %d, want the refused upload removed", len(repository.bundleObjects))
	}
	if response := importBundleRequest(t, handler, "", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("import without a bundle part = %d, want 400", response.Code)
	}
}

func TestPluginBundleImportRendersOutcomesAndFlagsARenamedBundle(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	finished := initTestTime.Add(-time.Minute)
	exported := initTestTime.Add(-time.Hour)
	repository.bundleImport = &store.PluginBundleImport{
		State: "partially_succeeded", RequestedBy: "alice", CreatedAt: finished, FinishedAt: &finished,
		Upload: store.PluginBundleUpload{Filename: "dufflebag-plugins-acme-20261013T090040Z-000000000000.tar.gz", Size: 9, SHA256: "ffffffffffff" + "0000"},
		Origin: store.PluginBundleOrigin{Host: "dufflebag.example.com", Organization: "acme", ExportedAt: &exported, Version: "v0.5.0-rc5", PluginCount: 2, VersionCount: 3},
		Outcomes: []byte(`[{"plugin":"amazon","version":"1.8.2","origin_kind":"releases-hashicorp","origin_repository":"packer-plugin-amazon","outcome":"imported"},` +
			`{"plugin":"git","version":"0.6.3","origin_kind":"upload","outcome":"skipped"},` +
			`{"plugin":"git","version":"0.6.2","origin_kind":"upload","outcome":"refused","error":"git is held by another source (github)"}]`),
	}
	handler := pluginRegistryHandler(identity.RoleReader, repository)
	response := call(t, handler, http.MethodGet, pluginRegistryPath("bundle/import"), nil, testToken)
	var job PluginBundleImport
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &job) != nil {
		t.Fatalf("import = %d %s", response.Code, response.Body.String())
	}
	if job.FilenameMatches == nil || *job.FilenameMatches || job.Bundle == nil || job.Bundle.Organization != "acme" || job.Bundle.VersionCount != 3 ||
		len(job.Outcomes) != 3 || job.Outcomes[0].Outcome != PluginBundleVersionImported || job.Outcomes[0].OriginRepository == nil ||
		job.Outcomes[2].Outcome != PluginBundleVersionRefused || job.Outcomes[2].Error == nil {
		t.Fatalf("import = %+v", job)
	}
	for _, role := range []identity.Role{identity.RoleReader, identity.RolePublisher} {
		if response := importBundleRequest(t, pluginRegistryHandler(role, repository), "x.tar.gz", []byte("x")); response.Code != http.StatusForbidden {
			t.Fatalf("%s imports = %d, want 403", role, response.Code)
		}
	}
	outsider := newHandler(repository, &fakeInstanceRepository{}, testAuth{}, testRoles{
		role: identity.RoleMaintainer, scope: identity.Scope{OrganizationID: uuid.New()},
	}, testLogger(), func() time.Time { return initTestTime })
	if response := call(t, outsider, http.MethodGet, pluginRegistryPath("bundle/import"), nil, testToken); response.Code != http.StatusNotFound {
		t.Fatalf("caller outside the organization = %d, want 404", response.Code)
	}
}

func TestPluginBundleImportHonoursAnExpectedDigest(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	handler, trail := auditedPlatform(t, pluginRegistryHandler(identity.RoleMaintainer, repository))
	archive := []byte("gzip bytes of a bundle")
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])
	wrong := importBundleRequest(t, handler, "bundle.tar.gz", archive, "sha256", strings.Repeat("a", 64))
	if wrong.Code != http.StatusBadRequest || !strings.Contains(wrong.Body.String(), "not the expected") || len(repository.bundleObjects) != 0 || repository.bundleImport != nil {
		t.Fatalf("wrong digest = %d %s, objects %d, want 400 and nothing stored", wrong.Code, wrong.Body.String(), len(repository.bundleObjects))
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{"operation": "plugin_registry.bundle.import", "outcome": "failure", "reason": "digest_mismatch"})
	if response := importBundleRequest(t, handler, "bundle.tar.gz", archive, "sha256", "not-hex"); response.Code != http.StatusBadRequest {
		t.Fatalf("malformed digest = %d, want 400", response.Code)
	}
	right := importBundleRequest(t, handler, "bundle.tar.gz", archive, "sha256", "  "+strings.ToUpper(digest)+"\n")
	if right.Code != http.StatusAccepted {
		t.Fatalf("right digest (upper case, padded) = %d %s, want 202", right.Code, right.Body.String())
	}
}

func TestPluginBundleImportRefusesAnOversizedBundle(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	actor := testRoles{role: identity.RoleMaintainer, scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)}}
	handler := newHandlerWithServices(repository, &fakeInstanceRepository{}, testAuth{}, actor, testLogger(),
		nil, nil, nil, nil, nil, nil, BuildInfo{}, 1024, DefaultPluginUpdateInterval, nil, func() time.Time { return initTestTime })
	response := importBundleRequest(t, handler, "big.tar.gz", bytes.Repeat([]byte("x"), 4096))
	if response.Code != http.StatusRequestEntityTooLarge || len(repository.bundleObjects) != 0 {
		t.Fatalf("status = %d %s, want 413 and nothing stored", response.Code, response.Body.String())
	}
}
