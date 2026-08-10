//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	orgA     = "00000000-0000-4000-8000-000000000001"
	projectA = "00000000-0000-4000-8000-000000000101"
	orgB     = "00000000-0000-4000-8000-000000000002"
	projectB = "00000000-0000-4000-8000-000000000102"
)

// listingCaller restores a valid principal for exercising the scope-filtered
// listings, which take a principal rather than a bare scope so that the
// platform branch cannot be reached by a forgotten zero value (duf-ueq).
func listingCaller(t *testing.T, scope identity.Scope, role identity.Role) *identity.Principal {
	t.Helper()
	principal, err := identity.RestorePrincipal(
		"caller-"+string(role), "caller", "caller-"+string(role), scope, role, time.Time{}, nil,
	)
	if err != nil {
		t.Fatalf("restore caller: %v", err)
	}
	return principal
}

func openTestDatabase(t *testing.T) (*sql.DB, string, func()) {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(
		ctx,
		"postgres:17-alpine",
		tcpostgres.WithDatabase("dufflebag"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	stop := func() {
		if err := container.Terminate(ctx); err != nil {
			t.Errorf("terminate postgres: %v", err)
		}
	}

	adminURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		stop()
		t.Fatalf("postgres connection string: %v", err)
	}
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		stop()
		t.Fatalf("open admin database: %v", err)
	}
	if err := store.Migrate(admin); err != nil {
		_ = admin.Close()
		stop()
		t.Fatalf("migrate database: %v", err)
	}
	if err := store.Migrate(admin); err != nil {
		_ = admin.Close()
		stop()
		t.Fatalf("repeat migration: %v", err)
	}
	if _, err := admin.Exec(`
		INSERT INTO organizations (id, name, created_at) VALUES
			($1, 'organization-a', now()),
			($2, 'organization-b', now())
	`, orgA, orgB); err != nil {
		_ = admin.Close()
		stop()
		t.Fatalf("seed organizations: %v", err)
	}
	if _, err := admin.Exec(`
		INSERT INTO projects (id, organization_id, name, created_at) VALUES
			($1, $2, 'project-a', now()),
			($3, $4, 'project-b', now())
	`, projectA, orgA, projectB, orgB); err != nil {
		_ = admin.Close()
		stop()
		t.Fatalf("seed projects: %v", err)
	}
	if _, err := admin.Exec(`
		CREATE ROLE dufflebag_app LOGIN PASSWORD 'app';
		GRANT USAGE ON SCHEMA public TO dufflebag_app;
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dufflebag_app;
	`); err != nil {
		_ = admin.Close()
		stop()
		t.Fatalf("create application role: %v", err)
	}
	// Two distinct sabotage hooks, proving two different properties.
	//
	// DROP_RLS removes the policies but leaves RLS forced, so Postgres denies
	// everything: proof that RLS is FORCE'd and no policy means no access.
	// It does not prove the isolation assertions have teeth, because nothing
	// ever becomes visible.
	//
	// DISABLE_RLS turns RLS off entirely, which makes every tenant's rows
	// readable by every other. That is the leak TestTenantIsolation exists to
	// catch, and the only hook that proves isolation comes from RLS rather
	// than from application predicates.
	rlsTables := []string{
		"buckets", "versions", "builds", "artifacts", "channels", "channel_assignments", "pins", "bagdrop_configs", "bagdrop_associations",
		"sboms", "sbom_packages", "scan_run_counters", "scan_runs", "scan_findings", "scan_transcripts",
		"build_scan_state", "pending_scans",
	}
	if os.Getenv("DUFFLEBAG_TEST_DROP_RLS") == "1" {
		for _, table := range rlsTables {
			if _, err := admin.Exec(fmt.Sprintf("DROP POLICY tenant_isolation ON %s", table)); err != nil {
				_ = admin.Close()
				stop()
				t.Fatalf("drop %s policy: %v", table, err)
			}
		}
	}
	if os.Getenv("DUFFLEBAG_TEST_DISABLE_RLS") == "1" {
		for _, table := range rlsTables {
			if _, err := admin.Exec(fmt.Sprintf("ALTER TABLE %s DISABLE ROW LEVEL SECURITY", table)); err != nil {
				_ = admin.Close()
				stop()
				t.Fatalf("disable RLS on %s: %v", table, err)
			}
		}
	}
	if os.Getenv("DUFFLEBAG_TEST_BREAK_SCHEMA") == "1" {
		if _, err := admin.Exec("ALTER TABLE builds DROP COLUMN platform"); err != nil {
			_ = admin.Close()
			stop()
			t.Fatalf("break schema: %v", err)
		}
	}
	if os.Getenv("DUFFLEBAG_TEST_EXPAND_SCHEMA") == "1" {
		if _, err := admin.Exec("ALTER TABLE builds ADD COLUMN source text"); err != nil {
			_ = admin.Close()
			stop()
			t.Fatalf("expand schema: %v", err)
		}
	}
	if err := admin.Close(); err != nil {
		stop()
		t.Fatalf("close admin database: %v", err)
	}

	appURL, err := url.Parse(adminURL)
	if err != nil {
		stop()
		t.Fatalf("parse postgres connection string: %v", err)
	}
	appURL.User = url.UserPassword("dufflebag_app", "app")
	db, err := sql.Open("pgx", appURL.String())
	if err != nil {
		stop()
		t.Fatalf("open application database: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		stop()
		t.Fatalf("ping application database: %v", err)
	}
	return db, appURL.String(), func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
		stop()
	}
}

func TestTenantIsolation(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()

	insertAggregate(t, ctx, db, orgA, projectA, "a")
	insertAggregate(t, ctx, db, orgB, projectB, "b")

	for _, tenant := range []struct {
		org, project, suffix string
	}{
		{orgA, projectA, "a"},
		{orgB, projectB, "b"},
	} {
		tx, err := store.BeginTenant(ctx, db, tenant.org, tenant.project)
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{
			"buckets", "versions", "builds", "artifacts", "channels", "channel_assignments", "pins", "bagdrop_configs", "bagdrop_associations",
			"scan_runs", "scan_findings", "scan_transcripts", "build_scan_state",
			"scan_run_counters", "pending_scans",
		} {
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
				_ = tx.Rollback()
				t.Fatalf("%s count for %s: %v", table, tenant.org, err)
			}
			if count != 1 {
				_ = tx.Rollback()
				t.Fatalf("%s exposed %d rows to %s; want exactly its own row", table, count, tenant.org)
			}
		}
		var visible bool
		if err := tx.QueryRowContext(
			ctx,
			"SELECT EXISTS (SELECT 1 FROM buckets WHERE id = $1)",
			"bucket-"+otherSuffix(tenant.suffix),
		).Scan(&visible); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if visible {
			_ = tx.Rollback()
			t.Fatalf("%s can read another tenant's bucket", tenant.org)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTenantRequiredForWrites(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	_, err := db.Exec(
		`INSERT INTO buckets
		 (organization_id, project_id, id, name, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, now(), now())`,
		orgA, projectA, "bucket-unscoped", "images",
	)
	if err == nil {
		t.Fatal("unscoped insert succeeded")
	}
}

func TestVersionCompletionInvariantIsPersistable(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tx, err := store.BeginTenant(ctx, db, orgA, projectA)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO buckets
			(organization_id, project_id, id, name, created_at, updated_at)
		VALUES ($1, $2, 'bucket-a', 'images', now(), now())
	`, orgA, projectA); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		complete bool
		sequence any
	}{
		{"incomplete with sequence", false, 1},
		{"complete without sequence", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO versions
					(organization_id, project_id, id, bucket_id, fingerprint,
					 template_type, complete, sequence, created_at, updated_at)
				VALUES ($1, $2, $3, 'bucket-a', $3, 'HCL2', $4, $5, now(), now())
			`, orgA, projectA, tc.name, tc.complete, tc.sequence)
			if err == nil {
				t.Fatal("inconsistent completion state was persisted")
			}
		})
	}
}

func TestVersionCompletionStateRoundTrips(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tx, err := store.BeginTenant(ctx, db, orgA, projectA)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	// One statement per Exec: pgx prepares, and Postgres rejects multiple
	// commands in a prepared statement (SQLSTATE 42601).
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO buckets
			(organization_id, project_id, id, name, created_at, updated_at)
		VALUES ($1, $2, 'bucket-a', 'images', now(), now())
	`, orgA, projectA); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO versions
			(organization_id, project_id, id, bucket_id, fingerprint, template_type,
			 complete, sequence, created_at, updated_at)
		VALUES
			($1, $2, 'version-incomplete', 'bucket-a', 'incomplete', 'HCL2', false, NULL, now(), now()),
			($1, $2, 'version-complete', 'bucket-a', 'complete', 'HCL2', true, 1, now(), now())
	`, orgA, projectA); err != nil {
		t.Fatal(err)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, complete, sequence
		FROM versions
		ORDER BY id
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = rows.Close()
	}()
	seen := 0
	for rows.Next() {
		var id string
		var complete bool
		var sequence sql.NullInt64
		if err := rows.Scan(&id, &complete, &sequence); err != nil {
			t.Fatal(err)
		}
		persistedSequence := 0
		if sequence.Valid {
			persistedSequence = int(sequence.Int64)
		}
		version, err := registry.RestoreVersion(
			registry.Version{ID: registry.ID(id)},
			complete,
			persistedSequence,
			nil,
		)
		if err != nil {
			t.Fatalf("restore %s: %v", id, err)
		}
		gotSequence, hasSequence := version.Sequence()
		if hasSequence != complete || gotSequence != persistedSequence {
			t.Fatalf("round trip %s: complete=%v sequence=%d,%v", id, complete, gotSequence, hasSequence)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != 2 {
		t.Fatalf("restored %d versions; want 2", seen)
	}
}

func TestTenantSettingDoesNotLeakThroughPool(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	db.SetMaxOpenConns(1)
	ctx := context.Background()

	tx, err := store.BeginTenant(ctx, db, orgA, projectA)
	if err != nil {
		t.Fatal(err)
	}
	var transactionPID int
	if err := tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&transactionPID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = conn.Close()
	}()
	var pooledPID int
	var organizationID, projectID string
	if err := conn.QueryRowContext(ctx, `
		SELECT pg_backend_pid(),
		       coalesce(current_setting('app.tenant_org', true), ''),
		       coalesce(current_setting('app.tenant_project', true), '')
	`).Scan(&pooledPID, &organizationID, &projectID); err != nil {
		t.Fatal(err)
	}
	if pooledPID != transactionPID {
		t.Fatalf("test did not reuse pooled connection: got backend %d, want %d", pooledPID, transactionPID)
	}
	if organizationID != "" || projectID != "" {
		t.Fatalf("tenant leaked through pool: organization=%q project=%q", organizationID, projectID)
	}
}

func TestChannelAssignmentHistoryIsAppendOnly(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	insertAggregate(t, ctx, db, orgA, projectA, "a")

	for _, statement := range []string{
		"UPDATE channel_assignments SET assigned_at = now() WHERE id = 'assignment-a'",
		"DELETE FROM channel_assignments WHERE id = 'assignment-a'",
	} {
		tx, err := store.BeginTenant(ctx, db, orgA, projectA)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, statement); err == nil {
			_ = tx.Rollback()
			t.Fatalf("append-only history accepted %q", statement)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPreviousReleaseAgainstNewSchema(t *testing.T) {
	previousApp := os.Getenv("PREVIOUS_APP")
	if previousApp == "" {
		t.Skip("PREVIOUS_APP is set by the expand-contract target")
	}
	_, databaseURL, cleanup := openTestDatabase(t)
	defer cleanup()

	cmd := exec.Command(previousApp)
	cmd.Env = append(os.Environ(), "DFBG_DATABASE_URL="+databaseURL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("previous release is incompatible with new schema: %v\n%s", err, output)
	}
}

func insertAggregate(t *testing.T, ctx context.Context, db *sql.DB, org, project, suffix string) {
	t.Helper()
	tx, err := store.BeginTenant(ctx, db, org, project)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	now := time.Now()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO buckets
			(organization_id, project_id, id, name, created_at, updated_at)
			VALUES ($1,$2,$3,'images',$4,$4)`, []any{org, project, "bucket-" + suffix, now}},
		{`INSERT INTO versions
			(organization_id, project_id, id, bucket_id, fingerprint, template_type,
			 complete, sequence, created_at, updated_at)
			VALUES ($1,$2,$3,$4,'fingerprint','HCL2',true,1,$5,$5)`,
			[]any{org, project, "version-" + suffix, "bucket-" + suffix, now}},
		{`INSERT INTO builds
			(organization_id, project_id, id, version_id, component_type, status,
			 platform, metadata_seen, created_at, updated_at)
			VALUES ($1,$2,$3,$4,'amazon-ebs','done','aws',true,$5,$5)`,
			[]any{org, project, "build-" + suffix, "version-" + suffix, now}},
		{`INSERT INTO artifacts VALUES ($1,$2,$3,$4,'ami-123','us-east-1',$5)`,
			[]any{org, project, "artifact-" + suffix, "build-" + suffix, now}},
		{`INSERT INTO channels
			(organization_id, project_id, id, bucket_id, name, created_at, updated_at)
			VALUES ($1,$2,$3,$4,'latest',$5,$5)`,
			[]any{org, project, "channel-" + suffix, "bucket-" + suffix, now}},
		{`INSERT INTO channel_assignments
			(organization_id, project_id, id, channel_id, version_id, assigned_at)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			[]any{org, project, "assignment-" + suffix, "channel-" + suffix, "version-" + suffix, now}},
		{`INSERT INTO sboms
			(organization_id, project_id, id, build_id, name, format, object_key, created_at)
			VALUES ($1,$2,$3,$4,'sbom.spdx.json','SPDX','key-'||$3,$5)`,
			[]any{org, project, "sbom-" + suffix, "build-" + suffix, now}},
		{`INSERT INTO sbom_packages
			(organization_id, project_id, sbom_id, name, version, purl)
			VALUES ($1,$2,$3,'busybox','1.36.1-r0','pkg:apk/alpine/busybox@1.36.1-r0')`,
			[]any{org, project, "sbom-" + suffix}},
		{`INSERT INTO scan_runs
			(organization_id, project_id, id, build_id, run_sequence, status, adapter,
			 engine, database_revision, observed_at, transcript_digest, created_at)
			VALUES ($1,$2,$3,$4,1,'succeeded','osv',
			 'https://api.osv.dev','unreported',$5,'digest',$5)`,
			[]any{org, project, "scan-run-" + suffix, "build-" + suffix, now}},
		{`INSERT INTO scan_findings
			(organization_id, project_id, run_id, sbom_id, package_name, package_version,
			 purl, advisory_id, derived_severity, first_seen_at)
			VALUES ($1,$2,$3,$4,'busybox','1.36.1-r0','pkg:apk/alpine/busybox@1.36.1-r0',
			 'ALPINE-CVE-2022-48174','critical',$5)`,
			[]any{org, project, "scan-run-" + suffix, "sbom-" + suffix, now}},
		{`INSERT INTO scan_transcripts
			(organization_id, project_id, run_id, object_key, expires_at)
			VALUES ($1,$2,$3,'transcript-key-'||$3,$4)`,
			[]any{org, project, "scan-run-" + suffix, now}},
		{`INSERT INTO build_scan_state
			(organization_id, project_id, build_id, current_findings_run_id, latest_attempt_run_id)
			VALUES ($1,$2,$3,$4,$4)`,
			[]any{org, project, "build-" + suffix, "scan-run-" + suffix}},
		{`INSERT INTO scan_run_counters (organization_id, project_id, next_sequence)
			VALUES ($1,$2,2)`, []any{org, project}},
		{`INSERT INTO pending_scans
			(organization_id, project_id, build_id, enqueued_at, reason)
			VALUES ($1,$2,$3,$4,'channel_assignment')`,
			[]any{org, project, "build-" + suffix, now}},
		{`INSERT INTO pins
			(organization_id, project_id, bucket_name, pinned_at, pinned_by)
			VALUES ($1,$2,'images',$3,$4)`,
			[]any{org, project, now, "principal-" + suffix}},
		{`INSERT INTO bagdrop_configs
			(organization_id, project_id, adapter, destination, sealed_secret, created_at, updated_at)
			VALUES ($1,$2,'hcp-packer',$3,$4,$5,$5)`,
			[]any{org, project, `{"organization_id":"hcp-org","project_id":"hcp-project","client_id":"client"}`, []byte("sealed-" + suffix), now}},
		{`INSERT INTO bagdrop_associations
			(organization_id, project_id, bucket_name, state, created_at, updated_at)
			VALUES ($1,$2,'images','active',$3,$3)`,
			[]any{org, project, now}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("insert aggregate for %s: %v", org, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func otherSuffix(suffix string) string {
	if suffix == "a" {
		return "b"
	}
	return "a"
}

// Review finding 4: connecting as a superuser or a BYPASSRLS role silently
// disables every tenancy policy — nothing errors, nothing logs, and the
// isolation tests pass because they create their own unprivileged role. The
// misconfiguration therefore has to be refused at startup.
func TestAssertRLSAppliesRefusesAPrivilegedRole(t *testing.T) {
	appDB, appURL, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()

	// The application role is unprivileged, which is the supported configuration.
	if err := store.AssertRLSApplies(ctx, appDB); err != nil {
		t.Fatalf("the application role was refused: %v", err)
	}

	// The container's superuser is the commonest misconfiguration — pointing
	// DFBG_DATABASE_URL at the stock postgres user.
	superURL, err := url.Parse(appURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	superURL.User = url.UserPassword("postgres", "postgres")
	superDB, err := sql.Open("pgx", superURL.String())
	if err != nil {
		t.Fatalf("open superuser database: %v", err)
	}
	defer func() { _ = superDB.Close() }()

	if err := store.AssertRLSApplies(ctx, superDB); err == nil {
		t.Fatal("a superuser connection was accepted; row-level security would not apply")
	}

	// And explicitly BYPASSRLS, which is the same hazard without superuser.
	if _, err := superDB.ExecContext(ctx,
		`CREATE ROLE bypasser LOGIN PASSWORD 'bypass' BYPASSRLS`,
	); err != nil {
		t.Fatalf("create bypass role: %v", err)
	}
	bypassURL, _ := url.Parse(appURL)
	bypassURL.User = url.UserPassword("bypasser", "bypass")
	bypassDB, err := sql.Open("pgx", bypassURL.String())
	if err != nil {
		t.Fatalf("open bypass database: %v", err)
	}
	defer func() { _ = bypassDB.Close() }()

	if err := store.AssertRLSApplies(ctx, bypassDB); err == nil {
		t.Fatal("a BYPASSRLS role was accepted; row-level security would not apply")
	}
}
