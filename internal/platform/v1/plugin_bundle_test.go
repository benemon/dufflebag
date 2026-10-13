package v1

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

func TestPluginBundleExportIsQueuedWithTheRequestHostAndIsBusyUntilItFinishes(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	handler, trail := auditedPlatform(t, pluginRegistryHandler(identity.RoleMaintainer, repository))
	before := call(t, handler, http.MethodGet, pluginRegistryPath("bundle"), nil, testToken)
	if before.Code != http.StatusNotFound {
		t.Fatalf("bundle before any export = %d %s, want 404", before.Code, before.Body.String())
	}
	queued := call(t, handler, http.MethodPost, pluginRegistryPath("bundle/export"), nil, testToken)
	var bundle PluginBundle
	if queued.Code != http.StatusAccepted || json.Unmarshal(queued.Body.Bytes(), &bundle) != nil ||
		bundle.State != PluginBundleStateQueued || bundle.Bundle != nil || bundle.RequestedBy == "" {
		t.Fatalf("export = %d %s", queued.Code, queued.Body.String())
	}
	if repository.bundleHost != "example.com" {
		t.Fatalf("requested host = %q, want the request's host for the manifest", repository.bundleHost)
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{
		"operation": "plugin_registry.bundle.export", "target_type": "plugin_registry", "outcome": "success", "reason": "queued",
	})
	again := call(t, handler, http.MethodPost, pluginRegistryPath("bundle/export"), nil, testToken)
	if again.Code != http.StatusConflict {
		t.Fatalf("second export while queued = %d %s, want 409", again.Code, again.Body.String())
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{"operation": "plugin_registry.bundle.export", "outcome": "failure", "reason": "busy"})
	if response := call(t, handler, http.MethodGet, pluginRegistryPath("bundle/download"), nil, testToken); response.Code != http.StatusNotFound {
		t.Fatalf("download before a success = %d, want 404", response.Code)
	}
}

func TestPluginBundleDownloadStreamsTheArchiveWithItsFilename(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	exportedAt := initTestTime.Add(-time.Hour)
	repository.bundle = &store.PluginBundle{
		State: "succeeded", RequestedBy: "alice", CreatedAt: exportedAt, FinishedAt: &exportedAt,
		Bundle: &store.PluginBundleResult{
			ExportedAt: exportedAt, ObjectKey: "t/org/b/abc", Filename: "dufflebag-plugins-organization-20260730T120000Z-abcdef012345.tar.gz",
			Size: 11, SHA256: "abcdef012345" + "0000000000000000000000000000000000000000000000000000", PluginCount: 2, VersionCount: 3,
		},
	}
	repository.bundleObjects = map[string]string{"t/org/b/abc": "gzip bytes!"}
	handler, trail := auditedPlatform(t, pluginRegistryHandler(identity.RoleMaintainer, repository))
	read := call(t, handler, http.MethodGet, pluginRegistryPath("bundle"), nil, testToken)
	var bundle PluginBundle
	if read.Code != http.StatusOK || json.Unmarshal(read.Body.Bytes(), &bundle) != nil || bundle.Bundle == nil ||
		bundle.Bundle.PluginCount != 2 || bundle.Bundle.VersionCount != 3 || bundle.Bundle.Size != 11 || !bundle.Bundle.ExportedAt.Equal(exportedAt) {
		t.Fatalf("bundle = %d %s", read.Code, read.Body.String())
	}
	download := call(t, handler, http.MethodGet, pluginRegistryPath("bundle/download"), nil, testToken)
	if download.Code != http.StatusOK || download.Body.String() != "gzip bytes!" ||
		download.Header().Get("Content-Type") != "application/gzip" || download.Header().Get("Content-Length") != "11" ||
		download.Header().Get("Content-Disposition") != `attachment; filename="dufflebag-plugins-organization-20260730T120000Z-abcdef012345.tar.gz"` {
		t.Fatalf("download = %d %v %q", download.Code, download.Header(), download.Body.String())
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{
		"operation": "plugin_registry.bundle.download", "outcome": "success", "reason": repository.bundle.Bundle.Filename,
	})
	for _, role := range []identity.Role{identity.RoleReader, identity.RolePublisher} {
		if response := call(t, pluginRegistryHandler(role, repository), http.MethodPost, pluginRegistryPath("bundle/export"), nil, testToken); response.Code != http.StatusForbidden {
			t.Fatalf("%s exports = %d, want 403", role, response.Code)
		}
		if response := call(t, pluginRegistryHandler(role, repository), http.MethodGet, pluginRegistryPath("bundle/download"), nil, testToken); response.Code != http.StatusForbidden {
			t.Fatalf("%s downloads = %d, want 403", role, response.Code)
		}
	}
	if response := call(t, pluginRegistryHandler(identity.RoleReader, repository), http.MethodGet, pluginRegistryPath("bundle"), nil, testToken); response.Code != http.StatusOK {
		t.Fatalf("reader reads the bundle = %d, want 200", response.Code)
	}
}

func TestPluginBundleNeedsAnEnabledRegistry(t *testing.T) {
	handler := pluginRegistryHandler(identity.RoleMaintainer, pluginRegistryRepository(store.PluginRegistry{}))
	for _, request := range []struct{ method, action string }{{http.MethodPost, "bundle/export"}, {http.MethodGet, "bundle"}, {http.MethodGet, "bundle/download"}} {
		if response := call(t, handler, request.method, pluginRegistryPath(request.action), nil, testToken); response.Code != http.StatusConflict {
			t.Fatalf("%s %s with the registry off = %d, want 409", request.method, request.action, response.Code)
		}
	}
}
