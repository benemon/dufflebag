//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	store "github.com/benemon/dufflebag/internal/store/postgres"
)

func TestPluginUpdateChecksClaimOncePerInterval(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	ctx := context.Background()
	repository := store.NewRepositoryWithObjectStore(db, objects)
	tenant := store.ParseOrganizationTenant(orgA)
	if _, err := repository.EnablePluginRegistry(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishPluginVersion(ctx, tenant, pluginVersion("1.0.0", store.PluginSource{Kind: "upload"})); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetPluginUpdateCheck(ctx, tenant, "probe", true); !errors.Is(err, store.ErrPluginUploadHasNoUpdates) {
		t.Fatalf("update check on an upload = %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_org', $1, true)`, orgA); err != nil {
		t.Fatal(err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE plugins SET update_check = true WHERE name = 'probe'`)
	if err == nil {
		updated, _ := result.RowsAffected()
		t.Fatalf("the schema let an uploaded plugin opt in to update checks (%d rows)", updated)
	}
	_ = tx.Rollback()
	if err := repository.DeletePluginVersion(ctx, tenant, "probe", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishPluginVersion(ctx, tenant, pluginVersion("1.0.0", store.PluginSource{Kind: "github", Repository: "someone/packer-plugin-probe"})); err != nil {
		t.Fatal(err)
	}

	if claimed, err := repository.ClaimPluginUpdateCheck(ctx, time.Hour, true); claimed != nil || err != nil {
		t.Fatalf("a plugin without update checks was claimed: %+v %v", claimed, err)
	}
	if err := repository.SetPluginUpdateCheck(ctx, tenant, "probe", true); err != nil {
		t.Fatal(err)
	}
	if claimed, _ := repository.ClaimPluginUpdateCheck(ctx, time.Hour, false); claimed != nil {
		t.Fatal("a GitHub plugin was claimed while GitHub checks are paused")
	}
	claimed, err := repository.ClaimPluginUpdateCheck(ctx, time.Hour, true)
	if err != nil || claimed == nil || claimed.Name != "probe" || claimed.Source.Repository != "someone/packer-plugin-probe" {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if again, _ := repository.ClaimPluginUpdateCheck(ctx, time.Hour, true); again != nil {
		t.Fatal("a second replica claimed a plugin already checked this interval")
	}
	if err := repository.RecordPluginUpdateCheck(ctx, claimed.Tenant, claimed.ID, "1.1.0", "v1.1.0", ""); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_org', $1, true)`, orgA); err != nil {
		t.Fatal(err)
	}
	if result, err := tx.ExecContext(ctx, `UPDATE plugins SET update_checked_at = now() - interval '2 hours'`); err != nil {
		t.Fatal(err)
	} else if updated, _ := result.RowsAffected(); updated != 1 {
		t.Fatalf("aged %d plugins, want 1", updated)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claimed, _ = repository.ClaimPluginUpdateCheck(ctx, time.Hour, true)
	if claimed == nil {
		t.Fatal("a plugin due again was not claimed")
	}
	if err := repository.RecordPluginUpdateCheck(ctx, claimed.Tenant, claimed.ID, "", "", "GitHub rate limit reached"); err != nil {
		t.Fatal(err)
	}
	plugins, err := repository.ListPlugins(ctx, tenant)
	if err != nil || len(plugins) != 1 {
		t.Fatalf("plugins = %+v, %v", plugins, err)
	}
	update := plugins[0].Update
	if !update.Enabled || update.CheckedAt == nil || update.Latest != "1.1.0" || update.LatestTag != "v1.1.0" || update.Error != "GitHub rate limit reached" {
		t.Fatalf("after a failed check = %+v, want the last version seen kept beside the error", update)
	}
	if err := repository.RecordPluginUpdateCheck(ctx, claimed.Tenant, claimed.ID, "1.1.0", "v1.1.0", ""); err != nil {
		t.Fatal(err)
	}
	if plugins, _ = repository.ListPlugins(ctx, tenant); plugins[0].Update.Error != "" {
		t.Fatalf("a successful check left the old error: %+v", plugins[0].Update)
	}
}
