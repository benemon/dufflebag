package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/scan"
)

// SeverityCounts contains every findings severity band, including zeroes.
type SeverityCounts struct {
	Unknown    int `json:"unknown"`
	Negligible int `json:"negligible"`
	Low        int `json:"low"`
	Medium     int `json:"medium"`
	High       int `json:"high"`
	Critical   int `json:"critical"`
}

// BuildFindingsSummary is one build's current verified findings summary.
type BuildFindingsSummary struct {
	RunID            string
	Scanned          int
	Findings         int
	AffectedPackages int
	Worst            scan.Severity
	Counts           SeverityCounts
	ComputedAt       time.Time
	ObservedAt       time.Time
	Adapter          string
	Engine           string
	DatabaseRevision string
	Coverage         scan.Coverage
}

// VersionFindingsSummary is one version's current verified findings summary.
type VersionFindingsSummary struct {
	Findings         int
	AffectedPackages int
	Worst            scan.Severity
	Counts           SeverityCounts
	BuildsSummarised int
	ComputedAt       time.Time
}

// VersionBuildFindingsSummary describes one build in a version summary response.
type VersionBuildFindingsSummary struct {
	BuildID       string
	ComponentType string
	Platform      string
	Inventory     string
	Packages      int
	Summary       *BuildFindingsSummary
}

// VersionFindingsSummaryResult contains the version rollup and all its builds.
type VersionFindingsSummaryResult struct {
	Version *VersionFindingsSummary
	Builds  []VersionBuildFindingsSummary
}

type findingsSummary struct {
	Findings         int
	AffectedPackages int
	Worst            scan.Severity
	Counts           SeverityCounts
}

type findingSummaryKey struct {
	name, version, purl, advisory string
}

type packageSummaryKey struct {
	name, version, purl string
}

func severityRank(severity scan.Severity) int {
	switch severity {
	case scan.SeverityCritical:
		return 5
	case scan.SeverityHigh:
		return 4
	case scan.SeverityMedium:
		return 3
	case scan.SeverityLow:
		return 2
	case scan.SeverityNegligible:
		return 1
	default:
		return 0
	}
}

func (counts *SeverityCounts) add(severity scan.Severity) {
	switch severity {
	case scan.SeverityCritical:
		counts.Critical++
	case scan.SeverityHigh:
		counts.High++
	case scan.SeverityMedium:
		counts.Medium++
	case scan.SeverityLow:
		counts.Low++
	case scan.SeverityNegligible:
		counts.Negligible++
	default:
		counts.Unknown++
	}
}

func deduplicateSummaryFindings(findings []StoredFinding) []StoredFinding {
	selected := make(map[findingSummaryKey]StoredFinding, len(findings))
	for _, finding := range findings {
		key := findingSummaryKey{
			name: finding.Package.Name, version: finding.Package.Version,
			purl: finding.Package.Purl, advisory: finding.ID,
		}
		current, exists := selected[key]
		if !exists || finding.Package.SBOMID < current.Package.SBOMID {
			selected[key] = finding
		}
	}
	deduplicated := make([]StoredFinding, 0, len(selected))
	for _, finding := range selected {
		deduplicated = append(deduplicated, finding)
	}
	return deduplicated
}

func summarizeBuildFindings(findings []StoredFinding) findingsSummary {
	findings = deduplicateSummaryFindings(findings)
	packages := make(map[packageSummaryKey]bool, len(findings))
	summary := findingsSummary{Findings: len(findings)}
	for _, finding := range findings {
		packages[packageSummaryKey{finding.Package.Name, finding.Package.Version, finding.Package.Purl}] = true
		summary.Counts.add(finding.Severity)
		if summary.Worst == "" || severityRank(finding.Severity) > severityRank(summary.Worst) {
			summary.Worst = finding.Severity
		}
	}
	summary.AffectedPackages = len(packages)
	return summary
}

func summarizeVersionFindings(builds [][]StoredFinding) findingsSummary {
	worstByFinding := make(map[findingSummaryKey]scan.Severity)
	packages := make(map[packageSummaryKey]bool)
	for _, findings := range builds {
		for _, finding := range deduplicateSummaryFindings(findings) {
			key := findingSummaryKey{
				name: finding.Package.Name, version: finding.Package.Version,
				purl: finding.Package.Purl, advisory: finding.ID,
			}
			if current, exists := worstByFinding[key]; !exists || severityRank(finding.Severity) > severityRank(current) {
				worstByFinding[key] = finding.Severity
			}
			packages[packageSummaryKey{finding.Package.Name, finding.Package.Version, finding.Package.Purl}] = true
		}
	}
	summary := findingsSummary{Findings: len(worstByFinding), AffectedPackages: len(packages)}
	for _, severity := range worstByFinding {
		summary.Counts.add(severity)
		if summary.Worst == "" || severityRank(severity) > severityRank(summary.Worst) {
			summary.Worst = severity
		}
	}
	return summary
}

type buildFindingsSummaryRow struct {
	BucketID string
	BuildID  string
	RunID    string
	Scanned  int
	findingsSummary
	ComputedAt time.Time
}

type versionFindingsSummaryRow struct {
	BucketID  string
	VersionID string
	findingsSummary
	BuildsSummarised int
	SourceRunIDs     []string
	ComputedAt       time.Time
}

func (r *Repository) recomputeFindingsSummaries(
	ctx context.Context, tx *sql.Tx, tenant Tenant, buildID, runID string, computedAt time.Time,
) error {
	var versionID, bucketID string
	var scanned int
	if err := tx.QueryRowContext(ctx, `
		SELECT builds.version_id, builds.bucket_id,
			count(DISTINCT (packages.name, packages.version, packages.purl))
		FROM builds
		LEFT JOIN sboms ON sboms.organization_id = builds.organization_id
			AND sboms.project_id = builds.project_id AND sboms.build_id = builds.id
		LEFT JOIN sbom_packages packages ON packages.organization_id = sboms.organization_id
			AND packages.project_id = sboms.project_id AND packages.sbom_id = sboms.id
		WHERE builds.organization_id = $1 AND builds.project_id = $2 AND builds.id = $3
		GROUP BY builds.version_id, builds.bucket_id`,
		tenant.OrganizationID, tenant.ProjectID, buildID,
	).Scan(&versionID, &bucketID, &scanned); err != nil {
		// A bucket deleted mid-scan takes its builds and their summaries with it;
		// the completion still records its run and has nothing left to summarise.
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("read build findings summary input: %w", err)
	}
	findings, err := queryScanFindings(ctx, tx, r, tenant, runID, "")
	if err != nil {
		return err
	}
	buildRow := buildFindingsSummaryRow{
		BucketID: bucketID, BuildID: buildID, RunID: runID, Scanned: scanned,
		findingsSummary: summarizeBuildFindings(findings), ComputedAt: scanWriteTime(computedAt),
	}
	if err := r.upsertBuildFindingsSummary(ctx, tx, tenant, buildRow); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		tenant.OrganizationID.String()+"|"+tenant.ProjectID.String()+"|"+versionID,
	); err != nil {
		return fmt.Errorf("acquire version findings lock: %w", err)
	}
	return r.recomputeVersionFindingsSummary(ctx, tx, tenant, bucketID, versionID)
}

func (r *Repository) upsertBuildFindingsSummary(
	ctx context.Context, tx *sql.Tx, tenant Tenant, row buildFindingsSummaryRow,
) error {
	counts, err := json.Marshal(row.Counts)
	if err != nil {
		return fmt.Errorf("encode build findings counts: %w", err)
	}
	mac := r.rowMAC(buildFindingsSummaryMACMessage(tenant, row))
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO build_findings_summary (
			organization_id, project_id, bucket_id, build_id, run_id, scanned,
			findings, affected_packages, worst, counts, computed_at, integrity_mac
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), $10, $11, $12)
		ON CONFLICT (organization_id, project_id, build_id) DO UPDATE SET
			bucket_id = EXCLUDED.bucket_id, run_id = EXCLUDED.run_id,
			scanned = EXCLUDED.scanned, findings = EXCLUDED.findings,
			affected_packages = EXCLUDED.affected_packages, worst = EXCLUDED.worst,
			counts = EXCLUDED.counts, computed_at = EXCLUDED.computed_at,
			integrity_mac = EXCLUDED.integrity_mac`,
		tenant.OrganizationID, tenant.ProjectID, row.BucketID, row.BuildID, row.RunID,
		row.Scanned, row.Findings, row.AffectedPackages, string(row.Worst), counts,
		row.ComputedAt, mac,
	); err != nil {
		return fmt.Errorf("upsert build findings summary: %w", err)
	}
	return nil
}

func (r *Repository) recomputeVersionFindingsSummary(
	ctx context.Context, tx *sql.Tx, tenant Tenant, bucketID, versionID string,
) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT builds.id, state.current_findings_run_id
		FROM builds
		JOIN build_scan_state state ON state.organization_id = builds.organization_id
			AND state.project_id = builds.project_id AND state.build_id = builds.id
		WHERE builds.organization_id = $1 AND builds.project_id = $2
			AND builds.version_id = $3 AND state.current_findings_run_id IS NOT NULL
		ORDER BY builds.id DESC`, tenant.OrganizationID, tenant.ProjectID, versionID)
	if err != nil {
		return fmt.Errorf("list version findings sources: %w", err)
	}
	type sourceID struct{ buildID, runID string }
	var ids []sourceID
	for rows.Next() {
		var id sourceID
		if err := rows.Scan(&id.buildID, &id.runID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan version findings source: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close version findings sources: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list version findings sources: %w", err)
	}

	buildFindings := make([][]StoredFinding, 0, len(ids))
	row := versionFindingsSummaryRow{
		BucketID: bucketID, VersionID: versionID,
		BuildsSummarised: len(ids), SourceRunIDs: make([]string, 0, len(ids)),
	}
	for _, id := range ids {
		run, err := readScanRun(ctx, tx, r, tenant, id.runID)
		if err != nil {
			return err
		}
		findings, err := queryScanFindings(ctx, tx, r, tenant, id.runID, "")
		if err != nil {
			return err
		}
		buildFindings = append(buildFindings, findings)
		row.SourceRunIDs = append(row.SourceRunIDs, id.runID)
		if row.ComputedAt.IsZero() || run.ObservedAt.After(row.ComputedAt) {
			row.ComputedAt = run.ObservedAt
		}
	}
	row.findingsSummary = summarizeVersionFindings(buildFindings)
	row.ComputedAt = scanWriteTime(row.ComputedAt)
	counts, err := json.Marshal(row.Counts)
	if err != nil {
		return fmt.Errorf("encode version findings counts: %w", err)
	}
	runIDs, err := json.Marshal(row.SourceRunIDs)
	if err != nil {
		return fmt.Errorf("encode version findings source runs: %w", err)
	}
	mac := r.rowMAC(versionFindingsSummaryMACMessage(tenant, row))
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO version_findings_summary (
			organization_id, project_id, bucket_id, version_id, findings,
			affected_packages, worst, counts, builds_summarised, source_run_ids,
			computed_at, integrity_mac
		) VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9, $10, $11, $12)
		ON CONFLICT (organization_id, project_id, version_id) DO UPDATE SET
			bucket_id = EXCLUDED.bucket_id, findings = EXCLUDED.findings,
			affected_packages = EXCLUDED.affected_packages, worst = EXCLUDED.worst,
			counts = EXCLUDED.counts, builds_summarised = EXCLUDED.builds_summarised,
			source_run_ids = EXCLUDED.source_run_ids, computed_at = EXCLUDED.computed_at,
			integrity_mac = EXCLUDED.integrity_mac`,
		tenant.OrganizationID, tenant.ProjectID, row.BucketID, row.VersionID,
		row.Findings, row.AffectedPackages, string(row.Worst), counts,
		row.BuildsSummarised, runIDs, row.ComputedAt, mac,
	); err != nil {
		return fmt.Errorf("upsert version findings summary: %w", err)
	}
	return nil
}

// BackfillFindingsSummaries fills summary rows missing for current scan state.
func (r *Repository) BackfillFindingsSummaries(ctx context.Context, tenant Tenant) (int, error) {
	tx, _, err := r.begin(ctx, tenant)
	if err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT state.build_id
		FROM build_scan_state state
		LEFT JOIN build_findings_summary summary
			ON summary.organization_id = state.organization_id
			AND summary.project_id = state.project_id AND summary.build_id = state.build_id
		WHERE state.current_findings_run_id IS NOT NULL AND summary.build_id IS NULL
		ORDER BY state.build_id`)
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("list findings summary backfill: %w", err)
	}
	var buildIDs []string
	for rows.Next() {
		var buildID string
		if err := rows.Scan(&buildID); err != nil {
			_ = rows.Close()
			_ = tx.Rollback()
			return 0, fmt.Errorf("scan findings summary backfill: %w", err)
		}
		buildIDs = append(buildIDs, buildID)
	}
	if err := rows.Close(); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := rows.Err(); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	created := 0
	for _, buildID := range buildIDs {
		wrote, err := r.backfillBuildFindingsSummary(ctx, tenant, buildID)
		if err != nil {
			return created, err
		}
		if wrote {
			created++
		}
	}
	return created, nil
}

func (r *Repository) backfillBuildFindingsSummary(ctx context.Context, tenant Tenant, buildID string) (bool, error) {
	tx, _, err := r.begin(ctx, tenant)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		tenant.OrganizationID.String()+"|"+tenant.ProjectID.String()+"|"+buildID,
	); err != nil {
		return false, fmt.Errorf("acquire build scan lock: %w", err)
	}
	state, err := lockBuildScanState(ctx, tx, r, tenant, buildID)
	if err != nil {
		return false, err
	}
	if state == nil || state.CurrentFindingsRunID == "" {
		return false, tx.Commit()
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM build_findings_summary WHERE build_id = $1)`, buildID,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check build findings summary: %w", err)
	}
	if exists {
		return false, tx.Commit()
	}
	run, err := readScanRun(ctx, tx, r, tenant, state.CurrentFindingsRunID)
	if err != nil {
		return false, err
	}
	if err := r.recomputeFindingsSummaries(ctx, tx, tenant, buildID, run.ID, run.ObservedAt); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// GetVersionFindingsSummary returns verified stored summaries for one version.
func (r *Repository) GetVersionFindingsSummary(
	ctx context.Context, tenant Tenant, bucketName, fingerprint string,
) (*VersionFindingsSummaryResult, error) {
	tx, _, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var versionID string
	if err := tx.QueryRowContext(ctx, `
		SELECT versions.id
		FROM versions
		JOIN buckets ON buckets.organization_id = versions.organization_id
			AND buckets.project_id = versions.project_id AND buckets.id = versions.bucket_id
		WHERE versions.organization_id = $1 AND versions.project_id = $2
			AND buckets.name = $3 AND versions.fingerprint = $4`,
		tenant.OrganizationID, tenant.ProjectID, bucketName, fingerprint,
	).Scan(&versionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, registry.ErrNotFound
		}
		return nil, fmt.Errorf("get findings summary version: %w", err)
	}
	result := &VersionFindingsSummaryResult{Builds: make([]VersionBuildFindingsSummary, 0)}
	version, err := readVersionFindingsSummary(ctx, tx, r, tenant, versionID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	result.Version = version

	rows, err := tx.QueryContext(ctx, `
		SELECT builds.id, builds.component_type, builds.platform,
			CASE WHEN EXISTS (
				SELECT 1 FROM sboms status_sbom
				WHERE status_sbom.build_id = builds.id
					AND status_sbom.parse_status IN ('pending', 'unparseable')
			) THEN 'unparseable' ELSE 'parsed' END,
			(SELECT count(DISTINCT (packages.name, packages.version, packages.purl)) FROM sbom_packages packages
				JOIN sboms package_sbom ON package_sbom.organization_id = packages.organization_id
					AND package_sbom.project_id = packages.project_id AND package_sbom.id = packages.sbom_id
				WHERE package_sbom.build_id = builds.id),
			summary.bucket_id, summary.run_id, summary.scanned, summary.findings, summary.affected_packages,
			summary.worst, summary.counts, summary.computed_at, summary.integrity_mac,
			runs.run_sequence, runs.status, runs.error, runs.adapter, runs.engine,
			runs.database_revision, runs.observed_at, runs.transcript_digest,
			runs.coverage, runs.created_at, runs.integrity_mac
		FROM builds
		LEFT JOIN build_findings_summary summary
			ON summary.organization_id = builds.organization_id
			AND summary.project_id = builds.project_id AND summary.build_id = builds.id
		LEFT JOIN scan_runs runs ON runs.organization_id = summary.organization_id
			AND runs.project_id = summary.project_id AND runs.id = summary.run_id
		WHERE builds.organization_id = $1 AND builds.project_id = $2 AND builds.version_id = $3
		ORDER BY builds.id DESC`, tenant.OrganizationID, tenant.ProjectID, versionID)
	if err != nil {
		return nil, fmt.Errorf("list version summary builds: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var build VersionBuildFindingsSummary
		var summaryBucketID, runID sql.NullString
		var scanned, findings, affected sql.NullInt64
		var worst sql.NullString
		var counts, summaryMAC []byte
		var computedAt sql.NullTime
		var runSequence sql.NullInt64
		var status, runError, adapter, engine, revision, digest sql.NullString
		var observedAt, createdAt sql.NullTime
		var coverage, runMAC []byte
		if err := rows.Scan(
			&build.BuildID, &build.ComponentType, &build.Platform, &build.Inventory, &build.Packages,
			&summaryBucketID, &runID, &scanned, &findings, &affected, &worst, &counts, &computedAt, &summaryMAC,
			&runSequence, &status, &runError, &adapter, &engine, &revision, &observedAt,
			&digest, &coverage, &createdAt, &runMAC,
		); err != nil {
			return nil, fmt.Errorf("scan version summary build: %w", err)
		}
		if runID.Valid {
			var decodedCounts SeverityCounts
			if err := json.Unmarshal(counts, &decodedCounts); err != nil {
				return nil, fmt.Errorf("decode build findings counts: %w", err)
			}
			row := buildFindingsSummaryRow{
				BucketID: summaryBucketID.String, BuildID: build.BuildID,
				RunID: runID.String, Scanned: int(scanned.Int64),
				findingsSummary: findingsSummary{
					Findings: int(findings.Int64), AffectedPackages: int(affected.Int64),
					Worst: scan.Severity(worst.String), Counts: decodedCounts,
				},
				ComputedAt: scanWriteTime(computedAt.Time),
			}
			if err := r.verifyRowMAC("build findings summary "+build.BuildID, summaryMAC,
				buildFindingsSummaryMACMessage(tenant, row)); err != nil {
				return nil, err
			}
			var decodedCoverage scan.Coverage
			if err := json.Unmarshal(coverage, &decodedCoverage); err != nil {
				return nil, fmt.Errorf("decode coverage: %w", err)
			}
			run := ScanRun{
				ID: runID.String, BuildID: build.BuildID, RunSequence: runSequence.Int64,
				Status: status.String, Error: runError.String, Adapter: adapter.String,
				Engine: engine.String, DatabaseRevision: revision.String,
				ObservedAt: scanWriteTime(observedAt.Time), TranscriptDigest: digest.String,
				Coverage: decodedCoverage, CreatedAt: scanWriteTime(createdAt.Time),
			}
			if err := r.verifyRowMAC("scan run "+run.ID, runMAC, scanRunMACMessage(tenant, run)); err != nil {
				return nil, err
			}
			build.Summary = &BuildFindingsSummary{
				RunID: row.RunID, Scanned: row.Scanned, Findings: row.Findings,
				AffectedPackages: row.AffectedPackages, Worst: row.Worst, Counts: row.Counts,
				ComputedAt: row.ComputedAt, ObservedAt: run.ObservedAt, Adapter: run.Adapter,
				Engine: run.Engine, DatabaseRevision: run.DatabaseRevision, Coverage: run.Coverage,
			}
		}
		result.Builds = append(result.Builds, build)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list version summary builds: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func readVersionFindingsSummary(
	ctx context.Context, tx *sql.Tx, r *Repository, tenant Tenant, versionID string,
) (*VersionFindingsSummary, error) {
	var row versionFindingsSummaryRow
	row.VersionID = versionID
	var worst sql.NullString
	var counts, sourceRunIDs, mac []byte
	if err := tx.QueryRowContext(ctx, `
		SELECT bucket_id, findings, affected_packages, worst, counts,
			builds_summarised, source_run_ids, computed_at, integrity_mac
		FROM version_findings_summary
		WHERE organization_id = $1 AND project_id = $2 AND version_id = $3`,
		tenant.OrganizationID, tenant.ProjectID, versionID,
	).Scan(&row.BucketID, &row.Findings, &row.AffectedPackages, &worst, &counts,
		&row.BuildsSummarised, &sourceRunIDs, &row.ComputedAt, &mac); err != nil {
		return nil, err
	}
	row.Worst = scan.Severity(worst.String)
	row.ComputedAt = scanWriteTime(row.ComputedAt)
	if err := json.Unmarshal(counts, &row.Counts); err != nil {
		return nil, fmt.Errorf("decode version findings counts: %w", err)
	}
	if err := json.Unmarshal(sourceRunIDs, &row.SourceRunIDs); err != nil {
		return nil, fmt.Errorf("decode version findings source runs: %w", err)
	}
	if err := r.verifyRowMAC("version findings summary "+versionID, mac,
		versionFindingsSummaryMACMessage(tenant, row)); err != nil {
		return nil, err
	}
	return &VersionFindingsSummary{
		Findings: row.Findings, AffectedPackages: row.AffectedPackages,
		Worst: row.Worst, Counts: row.Counts, BuildsSummarised: row.BuildsSummarised,
		ComputedAt: row.ComputedAt,
	}, nil
}
