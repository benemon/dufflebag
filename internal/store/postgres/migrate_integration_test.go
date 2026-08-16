//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"net/url"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestMigrationBackfillsBucketScope(t *testing.T) {
	_, databaseURL, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()

	adminURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminURL.User = url.UserPassword("postgres", "postgres")
	admin, err := sql.Open("pgx", adminURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	driver, err := migratepostgres.WithInstance(admin, &migratepostgres.Config{})
	if err != nil {
		t.Fatal(err)
	}
	migrator, err := migrate.NewWithDatabaseInstance("file://migrations", "postgres", driver)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.Migrate(28); err != nil {
		t.Fatalf("step back to pre-enforcement schema: %v", err)
	}

	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	for _, statement := range []string{
		`INSERT INTO buckets (organization_id, project_id, id, name, created_at, updated_at)
		 VALUES ('` + orgA + `','` + projectA + `','bucket-migration','migration', $1, $1)`,
		`INSERT INTO versions (organization_id, project_id, id, bucket_id, fingerprint, template_type, complete, sequence, created_at, updated_at)
		 VALUES ('` + orgA + `','` + projectA + `','version-migration','bucket-migration','fingerprint','HCL2',true,1,$1,$1)`,
		`INSERT INTO builds (organization_id, project_id, id, version_id, component_type, status, platform, metadata_seen, created_at, updated_at)
		 VALUES ('` + orgA + `','` + projectA + `','build-migration','version-migration','docker','done','docker',true,$1,$1)`,
		`INSERT INTO artifacts (organization_id, project_id, id, build_id, external_identifier, region, created_at)
		 VALUES ('` + orgA + `','` + projectA + `','artifact-migration','build-migration','image','global',$1)`,
		`INSERT INTO channels (organization_id, project_id, id, bucket_id, name, created_at, updated_at)
		 VALUES ('` + orgA + `','` + projectA + `','channel-migration','bucket-migration','production',$1,$1)`,
		`INSERT INTO channel_assignments (organization_id, project_id, id, channel_id, version_id, assigned_at)
		 VALUES ('` + orgA + `','` + projectA + `','assignment-migration','channel-migration','version-migration',$1)`,
		`INSERT INTO sboms (organization_id, project_id, id, build_id, name, format, object_key, created_at)
		 VALUES ('` + orgA + `','` + projectA + `','sbom-migration','build-migration','sbom','SPDX','object',$1)`,
		`INSERT INTO sbom_packages (organization_id, project_id, sbom_id, name, version, purl)
		 SELECT '` + orgA + `','` + projectA + `','sbom-migration','busybox','1','pkg:test/busybox@1'
		 WHERE $1::timestamptz IS NOT NULL`,
		`INSERT INTO scan_runs (organization_id, project_id, id, build_id, run_sequence, status, adapter, engine, database_revision, observed_at, transcript_digest, created_at)
		 VALUES ('` + orgA + `','` + projectA + `','run-migration','build-migration',1,'succeeded','stub','stub','1',$1,'digest',$1)`,
		`INSERT INTO scan_findings (organization_id, project_id, run_id, sbom_id, package_name, package_version, purl, advisory_id, derived_severity, first_seen_at)
		 VALUES ('` + orgA + `','` + projectA + `','run-migration','sbom-migration','busybox','1','pkg:test/busybox@1','ADV-1','high',$1)`,
		`INSERT INTO scan_transcripts (organization_id, project_id, run_id, object_key, expires_at)
		 VALUES ('` + orgA + `','` + projectA + `','run-migration','transcript',$1)`,
		`INSERT INTO build_scan_state (organization_id, project_id, build_id, current_findings_run_id, latest_attempt_run_id)
		 SELECT '` + orgA + `','` + projectA + `','build-migration','run-migration','run-migration'
		 WHERE $1::timestamptz IS NOT NULL`,
		`INSERT INTO pending_scans (organization_id, project_id, build_id, enqueued_at, reason)
		 VALUES ('` + orgA + `','` + projectA + `','build-migration',$1,'manual_rescan')`,
		`INSERT INTO pins (organization_id, project_id, bucket_name, pinned_at, pinned_by)
		 VALUES ('` + orgA + `','` + projectA + `','migration',$1,'principal')`,
	} {
		if _, err := admin.ExecContext(ctx, statement, at); err != nil {
			t.Fatalf("write pre-enforcement state: %v", err)
		}
	}
	if err := migrator.Up(); err != nil {
		t.Fatalf("apply bucket enforcement migration: %v", err)
	}

	for _, table := range []string{
		"builds", "artifacts", "channel_assignments", "sboms", "sbom_packages",
		"scan_runs", "scan_findings", "scan_transcripts", "build_scan_state",
		"pending_scans", "pins",
	} {
		var bucketID string
		if err := admin.QueryRowContext(ctx, "SELECT bucket_id FROM "+table).Scan(&bucketID); err != nil {
			t.Fatalf("read %s bucket: %v", table, err)
		}
		if bucketID != "bucket-migration" {
			t.Fatalf("%s bucket_id = %q, want bucket-migration", table, bucketID)
		}
	}

	var constraints int
	if err := admin.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_constraint
		WHERE convalidated AND conname = ANY($1)
	`, []string{
		"builds_bucket_version_fkey", "artifacts_bucket_build_fkey",
		"channel_assignments_bucket_version_fkey", "sboms_bucket_build_fkey",
		"sbom_packages_bucket_sbom_fkey", "scan_runs_bucket_build_fkey",
		"scan_findings_bucket_run_fkey", "scan_transcripts_bucket_run_fkey",
		"build_scan_state_bucket_build_fkey", "pending_scans_bucket_build_fkey",
	}).Scan(&constraints); err != nil {
		t.Fatal(err)
	}
	if constraints != 10 {
		t.Fatalf("validated bucket parent constraints = %d, want 10", constraints)
	}
}
