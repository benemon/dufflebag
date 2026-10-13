//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	store "github.com/benemon/dufflebag/internal/store/postgres"
)

func TestPluginBundleImportQueueClaimAndRecord(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	ctx := context.Background()
	repository := store.NewRepositoryWithObjectStore(db, objects)
	tenant := store.ParseOrganizationTenant(orgA)
	upload := store.PluginBundleUpload{ObjectKey: "t/a/bi/abc", Filename: "dufflebag-plugins-acme-20261013T090040Z-abcdef012345.tar.gz", Size: 42, SHA256: "abcdef012345ff"}
	if err := repository.QueuePluginBundleImport(ctx, tenant, "alice", upload); !errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		t.Fatalf("queue with the registry off = %v, want ErrPluginRegistryNotEnabled", err)
	}
	if _, err := repository.EnablePluginRegistry(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetPluginBundleImport(ctx, tenant); !errors.Is(err, store.ErrPluginBundleImportNotFound) {
		t.Fatalf("import before any upload = %v, want ErrPluginBundleImportNotFound", err)
	}
	if err := repository.QueuePluginBundleImport(ctx, tenant, "alice", upload); err != nil {
		t.Fatal(err)
	}
	if err := repository.QueuePluginBundleImport(ctx, tenant, "bob", upload); !errors.Is(err, store.ErrPluginBundleImportBusy) {
		t.Fatalf("second queue = %v, want ErrPluginBundleImportBusy", err)
	}
	claimed, err := repository.ClaimPluginBundleImport(ctx, 30*time.Minute)
	if err != nil || claimed == nil || claimed.Upload.ObjectKey != upload.ObjectKey || claimed.Upload.Filename != upload.Filename || claimed.Tenant.OrganizationID != tenant.OrganizationID {
		t.Fatalf("claim = %+v %v", claimed, err)
	}
	if again, err := repository.ClaimPluginBundleImport(ctx, 30*time.Minute); err != nil || again != nil {
		t.Fatalf("second claim = %+v %v, want nothing while running", again, err)
	}
	exportedAt := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	origin := store.PluginBundleOrigin{Host: "connected.example.com", Organization: "acme", ExportedAt: &exportedAt, Version: "v0.5.0-rc5", PluginCount: 2, VersionCount: 3}
	outcomes := []map[string]string{{"plugin": "probe", "version": "1.0.0", "origin_kind": "upload", "outcome": "imported"}}
	if err := repository.RecordPluginBundleImport(ctx, tenant, "running", origin, outcomes, ""); err != nil {
		t.Fatal(err)
	}
	running, err := repository.GetPluginBundleImport(ctx, tenant)
	if err != nil || running.State != "running" || running.FinishedAt != nil || running.Origin.Organization != "acme" || running.Origin.VersionCount != 3 ||
		running.Origin.ExportedAt == nil || !running.Origin.ExportedAt.Equal(exportedAt) || string(running.Outcomes) == "[]" {
		t.Fatalf("running import = %+v %v", running, err)
	}
	if err := repository.RecordPluginBundleImport(ctx, tenant, "partially_succeeded", origin, outcomes, ""); err != nil {
		t.Fatal(err)
	}
	finished, err := repository.GetPluginBundleImport(ctx, tenant)
	if err != nil || finished.State != "partially_succeeded" || finished.FinishedAt == nil || finished.RequestedBy != "alice" || finished.Upload.SHA256 != upload.SHA256 {
		t.Fatalf("finished import = %+v %v", finished, err)
	}
	// A new upload may be queued once the previous import has finished, and it resets the outcome.
	if err := repository.QueuePluginBundleImport(ctx, tenant, "bob", store.PluginBundleUpload{ObjectKey: "t/a/bi/def", Filename: "next.tar.gz", Size: 7, SHA256: "def"}); err != nil {
		t.Fatal(err)
	}
	queued, err := repository.GetPluginBundleImport(ctx, tenant)
	if err != nil || queued.State != "queued" || queued.RequestedBy != "bob" || queued.Upload.Filename != "next.tar.gz" || queued.Origin.ExportedAt != nil || string(queued.Outcomes) != "[]" {
		t.Fatalf("requeued import = %+v %v", queued, err)
	}
	if claimed, err := repository.ClaimPluginBundleImport(ctx, 30*time.Minute); err != nil || claimed == nil {
		t.Fatalf("claim = %+v %v", claimed, err)
	}
	if claimed, err := repository.ClaimPluginBundleImport(ctx, 0); err != nil || claimed == nil {
		t.Fatalf("stale claim = %+v %v, want the running import taken over", claimed, err)
	}
	// The sums digest of a held version is the stored object key's last segment; absent reads as empty.
	if digest, err := repository.PluginVersionSumsDigest(ctx, tenant, "probe", "1.0.0"); err != nil || digest != "" {
		t.Fatalf("digest of an absent version = %q %v, want empty", digest, err)
	}
	version := pluginVersion("1.0.0", store.PluginSource{Kind: "upload"})
	if err := repository.PublishPluginVersion(ctx, tenant, version); err != nil {
		t.Fatal(err)
	}
	if digest, err := repository.PluginVersionSumsDigest(ctx, tenant, "probe", "1.0.0"); err != nil || digest != version.Sums.SHA256 {
		t.Fatalf("digest of a held version = %q %v, want %s", digest, err, version.Sums.SHA256)
	}
}
