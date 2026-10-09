//go:build integration

package postgres_test

import (
	"database/sql"
	"net/url"
	"strings"
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
	if version != 6 || dirty {
		t.Fatalf("migration state = version %d dirty %v, want version 6 clean", version, dirty)
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
		"build_findings_summary_bucket_build_fkey", "version_findings_summary_bucket_version_fkey",
	}).Scan(&constraints); err != nil {
		t.Fatal(err)
	}
	if constraints != 12 {
		t.Fatalf("validated bucket parent constraints = %d, want 12", constraints)
	}
}

func TestOrganizationNameConstraint(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	for _, name := range []string{"Acme Corp", "Acme", "acme\n", "a_b", "-acme"} {
		_, err := db.Exec(`
			INSERT INTO organizations (id, name, created_at)
			VALUES ('00000000-0000-4000-8000-000000000099', $1, now())
		`, name)
		if err == nil || !strings.Contains(err.Error(), "organizations_name_check") {
			t.Fatalf("insert organization %q error = %v, want organizations_name_check", name, err)
		}
	}
}

func TestOrganizationNameMigrationRefusesNonConformingRows(t *testing.T) {
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

	driver, err := migratepostgres.WithInstance(admin, &migratepostgres.Config{})
	if err != nil {
		t.Fatal(err)
	}
	migrator, err := migrate.NewWithDatabaseInstance("file://migrations", "postgres", driver)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.Migrate(4); err != nil {
		t.Fatalf("return to schema version 4: %v", err)
	}
	if _, err := admin.Exec(`
		INSERT INTO organizations (id, name, created_at) VALUES
			('00000000-0000-4000-8000-000000000098', 'Acme Corp', now()),
			('00000000-0000-4000-8000-000000000099', 'bad_name', now())
	`); err != nil {
		t.Fatalf("seed non-conforming organizations at schema version 4: %v", err)
	}

	err = migrator.Up()
	if err == nil {
		t.Fatal("migration with non-conforming organizations succeeded")
	}
	for _, want := range []string{
		"Acme Corp",
		"bad_name",
		"lowercase RFC 1123 DNS labels",
		"1 to 63 characters",
		"no leading or trailing hyphen",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("migration error %q does not contain %q", err, want)
		}
	}

	var constraint string
	if err := admin.QueryRow(`
		SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conname = 'organizations_name_check'
	`).Scan(&constraint); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(constraint, "char_length(name)") || strings.Contains(constraint, "a-z0-9") {
		t.Fatalf("organization constraint changed after refused migration: %s", constraint)
	}
	var invalidRows int
	if err := admin.QueryRow(`
		SELECT count(*)
		FROM organizations
		WHERE name IN ('Acme Corp', 'bad_name')
	`).Scan(&invalidRows); err != nil {
		t.Fatal(err)
	}
	if invalidRows != 2 {
		t.Fatalf("non-conforming rows after refused migration = %d, want 2", invalidRows)
	}
}
