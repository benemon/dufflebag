//go:build integration

package postgres_test

import (
	"database/sql"
	"net/url"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratepostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestMigrationsRoundTrip(t *testing.T) {
	_, databaseURL, cleanup := openTestDatabase(t)
	defer cleanup()

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

	var version int
	var dirty bool
	if err := admin.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != 2 || dirty {
		t.Fatalf("migration state = version %d dirty %v, want version 2 clean", version, dirty)
	}

	driver, err := migratepostgres.WithInstance(admin, &migratepostgres.Config{})
	if err != nil {
		t.Fatal(err)
	}
	migrator, err := migrate.NewWithDatabaseInstance("file://migrations", "postgres", driver)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.Down(); err != nil {
		t.Fatalf("apply baseline down migration: %v", err)
	}
	var bucketsTable *string
	if err := admin.QueryRow("SELECT to_regclass('public.buckets')::text").Scan(&bucketsTable); err != nil {
		t.Fatal(err)
	}
	if bucketsTable != nil {
		t.Fatalf("buckets table remains after baseline down migration: %q", *bucketsTable)
	}
	if err := migrator.Up(); err != nil {
		t.Fatalf("reapply baseline migration: %v", err)
	}

	var constraints int
	if err := admin.QueryRow(`
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
