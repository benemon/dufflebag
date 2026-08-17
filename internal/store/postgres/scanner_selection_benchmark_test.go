//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func BenchmarkScannerSelectionWalk(b *testing.B) {
	for _, tenantCount := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("tenants=%d", tenantCount), func(b *testing.B) {
			// b.Loop() times only the loop body and excludes everything before
			// it; manual timer control both ways trips its stopped-timer guard.
			db := openScannerSelectionBenchmarkDatabase(b, tenantCount)
			service := &ScannerService{repository: NewRepository(db)}
			ctx := context.Background()

			pending, err := service.pendingTenantsByAge(ctx)
			if err != nil {
				b.Fatalf("verify selection walk: %v", err)
			}
			if len(pending) != tenantCount {
				b.Fatalf("selection walk found %d tenants, want %d", len(pending), tenantCount)
			}

			b.ReportAllocs()
			for b.Loop() {
				if _, err := service.pendingTenantsByAge(ctx); err != nil {
					b.Fatalf("selection walk: %v", err)
				}
			}
		})
	}
}

func openScannerSelectionBenchmarkDatabase(b *testing.B, tenantCount int) *sql.DB {
	b.Helper()
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
		b.Fatalf("start postgres: %v", err)
	}
	b.Cleanup(func() {
		if err := container.Terminate(ctx); err != nil {
			b.Errorf("terminate postgres: %v", err)
		}
	})

	adminURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		b.Fatalf("postgres connection string: %v", err)
	}
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		b.Fatalf("open admin database: %v", err)
	}
	if err := Migrate(admin); err != nil {
		_ = admin.Close()
		b.Fatalf("migrate database: %v", err)
	}
	if _, err := admin.Exec(`
		CREATE ROLE dufflebag_app LOGIN PASSWORD 'app';
		GRANT USAGE ON SCHEMA public TO dufflebag_app;
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dufflebag_app;
	`); err != nil {
		_ = admin.Close()
		b.Fatalf("create application role: %v", err)
	}
	seedScannerSelectionTenants(b, admin, tenantCount)
	if err := admin.Close(); err != nil {
		b.Fatalf("close admin database: %v", err)
	}

	appURL, err := url.Parse(adminURL)
	if err != nil {
		b.Fatalf("parse postgres connection string: %v", err)
	}
	appURL.User = url.UserPassword("dufflebag_app", "app")
	db, err := sql.Open("pgx", appURL.String())
	if err != nil {
		b.Fatalf("open application database: %v", err)
	}
	b.Cleanup(func() {
		if err := db.Close(); err != nil {
			b.Errorf("close database: %v", err)
		}
	})
	if err := db.PingContext(ctx); err != nil {
		b.Fatalf("ping application database: %v", err)
	}
	return db
}

func seedScannerSelectionTenants(b *testing.B, admin *sql.DB, tenantCount int) {
	b.Helper()
	for _, query := range []string{
		`WITH tenants AS (
			SELECT tenant,
				md5('scanner-selection-org-' || tenant::text)::uuid AS organization_id,
				md5('scanner-selection-project-' || tenant::text)::uuid AS project_id
			FROM generate_series(1, $1) AS generated(tenant)
		)
		INSERT INTO organizations (id, name, created_at)
		SELECT organization_id, 'scanner-selection-org-' || tenant::text,
			TIMESTAMPTZ '2026-08-09 00:00:00+00' + tenant * INTERVAL '1 microsecond'
		FROM tenants`,
		`WITH tenants AS (
			SELECT tenant,
				md5('scanner-selection-org-' || tenant::text)::uuid AS organization_id,
				md5('scanner-selection-project-' || tenant::text)::uuid AS project_id
			FROM generate_series(1, $1) AS generated(tenant)
		)
		INSERT INTO projects (id, organization_id, name, created_at)
		SELECT project_id, organization_id, 'scanner-selection-project',
			TIMESTAMPTZ '2026-08-09 00:00:00+00' + tenant * INTERVAL '1 microsecond'
		FROM tenants`,
		`WITH tenants AS (
			SELECT tenant,
				md5('scanner-selection-org-' || tenant::text)::uuid AS organization_id,
				md5('scanner-selection-project-' || tenant::text)::uuid AS project_id
			FROM generate_series(1, $1) AS generated(tenant)
		)
		INSERT INTO buckets (organization_id, project_id, id, name, created_at, updated_at)
		SELECT organization_id, project_id, 'scanner-selection-bucket', 'scanner-selection-bucket',
			TIMESTAMPTZ '2026-08-09 00:00:00+00', TIMESTAMPTZ '2026-08-09 00:00:00+00'
		FROM tenants`,
		`WITH tenants AS (
			SELECT tenant,
				md5('scanner-selection-org-' || tenant::text)::uuid AS organization_id,
				md5('scanner-selection-project-' || tenant::text)::uuid AS project_id
			FROM generate_series(1, $1) AS generated(tenant)
		)
		INSERT INTO versions
			(organization_id, project_id, id, bucket_id, fingerprint, template_type,
			 complete, sequence, created_at, updated_at)
		SELECT organization_id, project_id, 'scanner-selection-version', 'scanner-selection-bucket',
			'scanner-selection-fingerprint', 'HCL2', true, 1,
			TIMESTAMPTZ '2026-08-09 00:00:00+00', TIMESTAMPTZ '2026-08-09 00:00:00+00'
		FROM tenants`,
		`WITH tenants AS (
			SELECT tenant,
				md5('scanner-selection-org-' || tenant::text)::uuid AS organization_id,
				md5('scanner-selection-project-' || tenant::text)::uuid AS project_id
			FROM generate_series(1, $1) AS generated(tenant)
		)
		INSERT INTO builds
			(organization_id, project_id, id, bucket_id, version_id, component_type, status, platform,
			 metadata_seen, created_at, updated_at)
		SELECT organization_id, project_id, 'scanner-selection-build', 'scanner-selection-bucket', 'scanner-selection-version',
			'docker', 'done', 'docker', true,
			TIMESTAMPTZ '2026-08-09 00:00:00+00', TIMESTAMPTZ '2026-08-09 00:00:00+00'
		FROM tenants`,
		`WITH tenants AS (
			SELECT tenant,
				md5('scanner-selection-org-' || tenant::text)::uuid AS organization_id,
				md5('scanner-selection-project-' || tenant::text)::uuid AS project_id
			FROM generate_series(1, $1) AS generated(tenant)
		)
		INSERT INTO pending_scans (organization_id, project_id, bucket_id, build_id, enqueued_at, reason)
		SELECT organization_id, project_id, 'scanner-selection-bucket', 'scanner-selection-build',
			TIMESTAMPTZ '2026-08-09 00:00:00+00' + tenant * INTERVAL '1 microsecond',
			'manual_rescan'
		FROM tenants`,
	} {
		if _, err := admin.Exec(query, tenantCount); err != nil {
			b.Fatalf("seed scanner selection tenants: %v", err)
		}
	}
}
