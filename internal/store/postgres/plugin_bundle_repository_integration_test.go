//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	store "github.com/benemon/dufflebag/internal/store/postgres"
)

func TestPluginBundleQueueClaimRecordAndReplace(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	ctx := context.Background()
	repository := store.NewRepositoryWithObjectStore(db, objects)
	tenant := store.ParseOrganizationTenant(orgA)
	if err := repository.QueuePluginBundle(ctx, tenant, "alice", "dufflebag.example.com"); !errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		t.Fatalf("queue with the registry off = %v, want ErrPluginRegistryNotEnabled", err)
	}
	if _, err := repository.EnablePluginRegistry(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetPluginBundle(ctx, tenant); !errors.Is(err, store.ErrPluginBundleNotFound) {
		t.Fatalf("bundle before any export = %v, want ErrPluginBundleNotFound", err)
	}
	for _, version := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		if err := repository.PublishPluginVersion(ctx, tenant, pluginVersion(version, store.PluginSource{Kind: "upload"})); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.SetPluginVersionRevoked(ctx, tenant, "probe", "1.0.0", true); err != nil {
		t.Fatal(err)
	}
	if err := repository.QueuePluginBundle(ctx, tenant, "alice", "dufflebag.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := repository.QueuePluginBundle(ctx, tenant, "bob", "other.example.com"); !errors.Is(err, store.ErrPluginBundleBusy) {
		t.Fatalf("second queue = %v, want ErrPluginBundleBusy", err)
	}
	claimed, err := repository.ClaimPluginBundle(ctx, 30*time.Minute)
	if err != nil || claimed == nil || claimed.OrganizationName != "organization-a" || claimed.RequestedHost != "dufflebag.example.com" || claimed.PreviousObjectKey != "" {
		t.Fatalf("claim = %+v %v", claimed, err)
	}
	if again, err := repository.ClaimPluginBundle(ctx, 30*time.Minute); err != nil || again != nil {
		t.Fatalf("second claim = %+v %v, want nothing while running", again, err)
	}
	// Revoked versions stay out; the rest carry their stored files.
	exportable, err := repository.ExportablePluginVersions(ctx, tenant)
	if err != nil || len(exportable) != 1 || exportable[0].Name != "probe" || len(exportable[0].Versions) != 2 ||
		exportable[0].Versions[0].Version != "2.0.0" || exportable[0].Versions[1].Version != "1.1.0" {
		t.Fatalf("exportable = %+v %v, want probe 2.0.0 then 1.1.0", exportable, err)
	}
	if zips := exportable[0].Versions[0].Zips; len(zips) != 1 || zips[0].OS != "linux" || zips[0].Arch != "amd64" || zips[0].SHA256 == "" || zips[0].ObjectKey == "" {
		t.Fatalf("zips = %+v", zips)
	}
	if sums := exportable[0].Versions[0].Sums; sums.ObjectKey == "" || sums.Size == 0 {
		t.Fatalf("sums = %+v", sums)
	}
	first := "first archive"
	if err := repository.PutPluginBundle(ctx, tenant, "t/a/b/first", bytes.NewReader([]byte(first)), int64(len(first))); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordPluginBundle(ctx, tenant, &store.PluginBundleResult{
		ObjectKey: "t/a/b/first", Filename: "first.tar.gz", Size: int64(len(first)), SHA256: "f1", PluginCount: 1, VersionCount: 2,
	}, ""); err != nil {
		t.Fatal(err)
	}
	bundle, err := repository.GetPluginBundle(ctx, tenant)
	if err != nil || bundle.State != "succeeded" || bundle.Bundle == nil || bundle.Bundle.Filename != "first.tar.gz" ||
		bundle.Bundle.PluginCount != 1 || bundle.Bundle.VersionCount != 2 || bundle.Bundle.ExportedAt.IsZero() || bundle.FinishedAt == nil {
		t.Fatalf("bundle after success = %+v %v", bundle, err)
	}
	exportedAt := bundle.Bundle.ExportedAt
	// A failed export keeps the previous bundle downloadable and records why.
	if err := repository.QueuePluginBundle(ctx, tenant, "alice", "dufflebag.example.com"); err != nil {
		t.Fatal(err)
	}
	claimed, err = repository.ClaimPluginBundle(ctx, 30*time.Minute)
	if err != nil || claimed == nil || claimed.PreviousObjectKey != "t/a/b/first" {
		t.Fatalf("claim after a success = %+v %v, want the previous object key", claimed, err)
	}
	if err := repository.RecordPluginBundle(ctx, tenant, nil, "object storage unavailable"); err != nil {
		t.Fatal(err)
	}
	bundle, err = repository.GetPluginBundle(ctx, tenant)
	if err != nil || bundle.State != "failed" || bundle.Error != "object storage unavailable" || bundle.Bundle == nil ||
		bundle.Bundle.ObjectKey != "t/a/b/first" || !bundle.Bundle.ExportedAt.Equal(exportedAt) {
		t.Fatalf("bundle after failure = %+v %v, want the first bundle kept", bundle, err)
	}
	body, err := repository.OpenPluginObject(ctx, "t/a/b/first")
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := io.ReadAll(body)
	_ = body.Close()
	if string(stored) != first {
		t.Fatalf("stored bundle = %q", stored)
	}
	if err := repository.DeletePluginBundleObject(ctx, tenant, "t/a/b/first"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.OpenPluginObject(ctx, "t/a/b/first"); err == nil || !strings.Contains(err.Error(), "object storage") {
		t.Fatalf("open deleted bundle = %v, want a storage error", err)
	}
	// A stale running claim is taken over after the stale interval.
	if err := repository.QueuePluginBundle(ctx, tenant, "alice", "dufflebag.example.com"); err != nil {
		t.Fatal(err)
	}
	if claimed, err := repository.ClaimPluginBundle(ctx, 30*time.Minute); err != nil || claimed == nil {
		t.Fatalf("claim = %+v %v", claimed, err)
	}
	if claimed, err := repository.ClaimPluginBundle(ctx, 0); err != nil || claimed == nil {
		t.Fatalf("stale claim = %+v %v, want the running export taken over", claimed, err)
	}
}
