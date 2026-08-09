package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	store "github.com/benemon/dufflebag/internal/store/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Tenant identifiers are UUIDs (ADR-0016), so the probe must use real ones —
// the schema rejects anything else, and a probe that cannot write proves
// nothing about compatibility.
//
// Fixed rather than generated so a failure names the same rows every time, and
// so the write can be found by hand when the probe reports an incompatibility.
const (
	compatOrganization = "00000000-0000-4000-8000-0000000000c0"
	compatProject      = "00000000-0000-4000-8000-0000000000c1"
)

func run() error {
	databaseURL := os.Getenv("DFBG_DATABASE_URL")
	if databaseURL == "" {
		return fmt.Errorf("DFBG_DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() {
		_ = db.Close()
	}()

	ctx := context.Background()
	tx, err := store.BeginTenant(ctx, db, compatOrganization, compatProject)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	now := time.Now()
	statements := []struct {
		query string
		args  []any
	}{
		// The tenancy rows every domain table references by foreign key. Without
		// them the first bucket insert fails and the probe proves nothing.
		{`INSERT INTO organizations (id, name, created_at)
			VALUES ($1,'compat',$2) ON CONFLICT DO NOTHING`,
			[]any{compatOrganization, now}},
		{`INSERT INTO projects (id, organization_id, name, created_at)
			VALUES ($2,$1,'compat',$3) ON CONFLICT DO NOTHING`,
			[]any{compatOrganization, compatProject, now}},
		{`INSERT INTO buckets
			(organization_id, project_id, id, name, created_at, updated_at)
			VALUES ($1,$2,'compat-bucket','images',$3,$3)`,
			[]any{compatOrganization, compatProject, now}},
		{`INSERT INTO versions
			(organization_id, project_id, id, bucket_id, fingerprint, template_type,
			 complete, sequence, created_at, updated_at)
			VALUES ($1,$2,'compat-version','compat-bucket','fingerprint','HCL2',true,1,$3,$3)`,
			[]any{compatOrganization, compatProject, now}},
		{`INSERT INTO builds
			(organization_id, project_id, id, version_id, component_type, status,
			 platform, metadata_seen, created_at, updated_at)
			VALUES ($1,$2,'compat-build','compat-version','amazon-ebs','done','aws',true,$3,$3)`,
			[]any{compatOrganization, compatProject, now}},
		{`INSERT INTO artifacts
			(organization_id, project_id, id, build_id, external_identifier, region, created_at)
			VALUES ($1,$2,'compat-artifact','compat-build','ami-123','us-east-1',$3)`,
			[]any{compatOrganization, compatProject, now}},
		{`INSERT INTO channels
			(organization_id, project_id, id, bucket_id, name, managed, created_at, updated_at)
			VALUES ($1,$2,'compat-channel','compat-bucket','latest',true,$3,$3)`,
			[]any{compatOrganization, compatProject, now}},
		{`INSERT INTO channel_assignments
			(organization_id, project_id, id, channel_id, version_id, assigned_at)
			VALUES ($1,$2,'compat-assignment','compat-channel','compat-version',$3)`,
			[]any{compatOrganization, compatProject, now}},
		{`INSERT INTO sboms
			(organization_id, project_id, id, build_id, name, format, object_key, created_at)
			VALUES ($1,$2,'compat-sbom','compat-build','sbom','CYCLONEDX','compat-object-key',$3)`,
			[]any{compatOrganization, compatProject, now}},
		{`INSERT INTO sbom_packages
			(organization_id, project_id, sbom_id, name, version, purl)
			VALUES ($1,$2,'compat-sbom','compat-package','1.0.0','pkg:generic/compat-package@1.0.0')`,
			[]any{compatOrganization, compatProject}},
		{`INSERT INTO scan_runs
			(organization_id, project_id, id, build_id, run_sequence, status, adapter,
			 engine, database_revision, observed_at, transcript_digest, created_at)
			VALUES ($1,$2,'compat-scan-run','compat-build',1,
			 'succeeded','osv','https://api.osv.dev','unreported',$3,'compat-digest',$3)`,
			[]any{compatOrganization, compatProject, now}},
		{`INSERT INTO scan_findings
			(organization_id, project_id, run_id, sbom_id, package_name, package_version,
			 purl, advisory_id, derived_severity, first_seen_at)
			VALUES ($1,$2,'compat-scan-run','compat-sbom','compat-package','1.0.0',
			 'pkg:generic/compat-package@1.0.0','COMPAT-2026-0001','unknown',$3)`,
			[]any{compatOrganization, compatProject, now}},
		{`INSERT INTO scan_transcripts
			(organization_id, project_id, run_id, object_key, expires_at)
			VALUES ($1,$2,'compat-scan-run','compat-transcript-key',$3)`,
			[]any{compatOrganization, compatProject, now}},
		{`INSERT INTO scan_run_counters (organization_id, project_id, next_sequence)
			VALUES ($1,$2,2) ON CONFLICT DO NOTHING`,
			[]any{compatOrganization, compatProject}},
		{`INSERT INTO build_scan_state
			(organization_id, project_id, build_id, current_findings_run_id, latest_attempt_run_id)
			VALUES ($1,$2,'compat-build','compat-scan-run','compat-scan-run')`,
			[]any{compatOrganization, compatProject}},
		{`INSERT INTO pending_scans
			(organization_id, project_id, build_id, enqueued_at, reason)
			VALUES ($1,$2,'compat-build',$3,'channel_assignment')`,
			[]any{compatOrganization, compatProject, now}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return fmt.Errorf("previous release write: %w", err)
		}
	}

	rows := []struct {
		table   string
		columns string
	}{
		{"buckets", "organization_id, project_id, id, name, created_at, updated_at"},
		{"versions", "organization_id, project_id, id, bucket_id, fingerprint, template_type, complete, sequence, created_at, updated_at"},
		{"builds", "organization_id, project_id, id, version_id, component_type, status, platform, metadata_seen, created_at, updated_at"},
		{"artifacts", "organization_id, project_id, id, build_id, external_identifier, region, created_at"},
		{"channels", "organization_id, project_id, id, bucket_id, name, managed, created_at, updated_at"},
		{"channel_assignments", "organization_id, project_id, id, channel_id, version_id, assigned_at"},
		{"sboms", "organization_id, project_id, id, build_id, name, format, object_key, created_at"},
		{"sbom_packages", "organization_id, project_id, sbom_id, name, version, purl"},
		{"scan_runs", "organization_id, project_id, id, build_id, run_sequence, status, adapter, engine, database_revision, observed_at, transcript_digest, created_at"},
		{"scan_findings", "organization_id, project_id, run_id, sbom_id, package_name, package_version, purl, advisory_id, derived_severity, first_seen_at"},
		{"scan_transcripts", "organization_id, project_id, run_id, object_key, expires_at"},
		{"scan_run_counters", "organization_id, project_id, next_sequence"},
		{"build_scan_state", "organization_id, project_id, build_id, current_findings_run_id, latest_attempt_run_id"},
		{"pending_scans", "organization_id, project_id, build_id, enqueued_at, reason"},
	}
	for _, row := range rows {
		query := fmt.Sprintf("SELECT %s FROM %s LIMIT 1", row.columns, row.table)
		result, err := tx.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("previous release read %s: %w", row.table, err)
		}
		if err := result.Close(); err != nil {
			return fmt.Errorf("close previous release read %s: %w", row.table, err)
		}
	}
	return tx.Commit()
}
