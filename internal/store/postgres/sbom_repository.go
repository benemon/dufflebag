package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
)

func replaceSbomProjection(
	ctx context.Context,
	tx *sql.Tx,
	tenant Tenant,
	sbomID string,
	packages []ReportedPackage,
	parseErr error,
) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM sbom_packages
		WHERE organization_id = $1 AND project_id = $2 AND sbom_id = $3
	`, tenant.OrganizationID, tenant.ProjectID, sbomID); err != nil {
		return fmt.Errorf("replace SBOM packages: %w", err)
	}

	for _, pkg := range packages {
		licenses, err := json.Marshal(pkg.Licenses)
		if err != nil {
			return fmt.Errorf("encode SBOM package licenses: %w", err)
		}
		paths, err := json.Marshal(pkg.ComponentPaths)
		if err != nil {
			return fmt.Errorf("encode SBOM component paths: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sbom_packages (
				organization_id, project_id, bucket_id, sbom_id, name, version, purl, licenses, component_paths
			)
			SELECT $1, $2, sboms.bucket_id, sboms.id, $4, $5, $6, $7, $8
			FROM sboms
			WHERE sboms.id = $3
		`, tenant.OrganizationID, tenant.ProjectID, sbomID,
			pkg.Name, pkg.Version, pkg.Purl, licenses, paths); err != nil {
			return fmt.Errorf("store SBOM package: %w", err)
		}
	}

	status, parseError := "parsed", ""
	if parseErr != nil {
		status, parseError = "unparseable", parseErr.Error()
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE sboms SET parse_status = $4, parse_error = $5
		WHERE organization_id = $1 AND project_id = $2 AND id = $3
	`, tenant.OrganizationID, tenant.ProjectID, sbomID, status, parseError); err != nil {
		return fmt.Errorf("record SBOM parse status: %w", err)
	}
	return nil
}

// ListSboms lists the raw document metadata attached to one build.
func (r *Repository) ListSboms(
	ctx context.Context,
	tenant Tenant,
	bucketName, fingerprint, buildID string,
) ([]Sbom, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := q.GetBuild(ctx, postgresdb.GetBuildParams{
		Name: bucketName, Fingerprint: fingerprint, ID: buildID,
	}); err != nil {
		return nil, mapNotFound("list SBOMs", err)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, build_id, name, format, object_key, created_at, parse_status, parse_error
		FROM sboms WHERE build_id = $1 ORDER BY name, id
	`, buildID)
	if err != nil {
		return nil, fmt.Errorf("list SBOMs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	sboms := make([]Sbom, 0)
	for rows.Next() {
		var row storedSbom
		if err := rows.Scan(
			&row.id, &row.buildID, &row.name, &row.format, &row.objectKey,
			&row.createdAt, &row.parseStatus, &row.parseError,
		); err != nil {
			return nil, fmt.Errorf("scan SBOM: %w", err)
		}
		restored, err := restoreSbom(row)
		if err != nil {
			return nil, err
		}
		sboms = append(sboms, *restored)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list SBOMs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit list SBOMs: %w", err)
	}
	return sboms, nil
}

// GetSbom retrieves one document's metadata.
func (r *Repository) GetSbom(
	ctx context.Context,
	tenant Tenant,
	bucketName, fingerprint, buildID, name string,
) (*Sbom, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := q.GetBuild(ctx, postgresdb.GetBuildParams{
		Name: bucketName, Fingerprint: fingerprint, ID: buildID,
	}); err != nil {
		return nil, mapNotFound("get SBOM", err)
	}
	var row storedSbom
	if err := tx.QueryRowContext(ctx, `
		SELECT id, build_id, name, format, object_key, created_at, parse_status, parse_error
		FROM sboms WHERE build_id = $1 AND name = $2
	`, buildID, name).Scan(
		&row.id, &row.buildID, &row.name, &row.format, &row.objectKey,
		&row.createdAt, &row.parseStatus, &row.parseError,
	); err != nil {
		return nil, mapNotFound("get SBOM", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit get SBOM: %w", err)
	}
	return restoreSbom(row)
}

// DownloadSbom retrieves one document's bytes from object storage.
func (r *Repository) DownloadSbom(
	ctx context.Context,
	tenant Tenant,
	bucketName, fingerprint, buildID, name string,
) ([]byte, error) {
	sbom, err := r.GetSbom(ctx, tenant, bucketName, fingerprint, buildID, name)
	if err != nil {
		return nil, err
	}
	if sbom.objectKey == "" {
		return nil, errors.New("download SBOM: payload predates object storage and was deliberately abandoned")
	}
	objects, err := r.objectStore()
	if err != nil {
		return nil, err
	}
	data, err := objects.Get(ctx, sbom.objectKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrObjectStorageUnavailable, err)
	}
	// On encrypted deployments every object was sealed before PutObject, so
	// this is unconditional: unsealed bytes here mean tampering or an object
	// this instance did not write, and both are refusals (ADR-0024).
	if r.ring != nil {
		opened, err := r.ring.Decrypt(data, payloadAAD(tenant, "sbom", sbom.objectKey))
		if err != nil {
			return nil, fmt.Errorf("download SBOM: %w", err)
		}
		data = opened
	}
	// Live HCP serves the DOCUMENT, not the provisioner's zstd envelope
	// (probed 2026-08-08: the presigned URL answered the byte-exact uploaded
	// JSON, Content-Length equal to the pre-compression original). Bytes that
	// do not open — an unparseable upload that was never zstd — are served as
	// stored, which is then already the document as uploaded.
	if document, err := decompressSbom(data); err == nil {
		return document, nil
	}
	return data, nil
}

// ListBuildPackages returns the flat package projection for one build. Current
// uploads parse before commit, so pending can only be compatibility residue
// written by the preceding release. Its Postgres bytes are deliberately
// abandoned and the row receives an honest terminal state.
func (r *Repository) ListBuildPackages(
	ctx context.Context,
	tenant Tenant,
	bucketName, fingerprint, buildID string,
) ([]ReportedPackage, []string, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := q.GetBuild(ctx, postgresdb.GetBuildParams{
		Name: bucketName, Fingerprint: fingerprint, ID: buildID,
	}); err != nil {
		return nil, nil, mapNotFound("list build packages", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE sboms
		SET parse_status = 'unparseable',
		    parse_error = 'payload predates object storage and was deliberately abandoned'
		WHERE build_id = $1 AND parse_status = 'pending'
	`, buildID); err != nil {
		return nil, nil, fmt.Errorf("finalize pending SBOMs: %w", err)
	}

	unparseableRows, err := tx.QueryContext(ctx, `
		SELECT name FROM sboms
		WHERE build_id = $1 AND parse_status = 'unparseable'
		ORDER BY name, id
	`, buildID)
	if err != nil {
		return nil, nil, fmt.Errorf("list unparseable SBOMs: %w", err)
	}
	unparseable := make([]string, 0)
	for unparseableRows.Next() {
		var name string
		if err := unparseableRows.Scan(&name); err != nil {
			_ = unparseableRows.Close()
			return nil, nil, fmt.Errorf("scan unparseable SBOM: %w", err)
		}
		unparseable = append(unparseable, name)
	}
	if err := unparseableRows.Err(); err != nil {
		_ = unparseableRows.Close()
		return nil, nil, fmt.Errorf("list unparseable SBOMs: %w", err)
	}
	if err := unparseableRows.Close(); err != nil {
		return nil, nil, fmt.Errorf("close unparseable SBOMs: %w", err)
	}
	if len(unparseable) > 0 {
		if err := tx.Commit(); err != nil {
			return nil, nil, fmt.Errorf("commit SBOM projection: %w", err)
		}
		return nil, unparseable, nil
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT packages.name, packages.version, packages.purl,
		       sboms.id, sboms.build_id, sboms.name, sboms.format, sboms.created_at
		FROM sbom_packages AS packages
		JOIN sboms ON sboms.organization_id = packages.organization_id
		          AND sboms.project_id = packages.project_id
		          AND sboms.id = packages.sbom_id
		WHERE sboms.build_id = $1
		ORDER BY packages.name, packages.version, packages.purl, sboms.name, sboms.id
	`, buildID)
	if err != nil {
		return nil, nil, fmt.Errorf("list build packages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type packageKey struct{ name, version, purl string }
	byIdentity := make(map[packageKey]*ReportedPackage)
	order := make([]packageKey, 0)
	for rows.Next() {
		var name, version, purl, sbomID, sbomBuildID, sbomName, format string
		var createdAt sql.NullTime
		if err := rows.Scan(
			&name, &version, &purl, &sbomID, &sbomBuildID, &sbomName, &format, &createdAt,
		); err != nil {
			return nil, nil, fmt.Errorf("scan build package: %w", err)
		}
		identity := packageKey{name, version, purl}
		pkg := byIdentity[identity]
		if pkg == nil {
			pkg = &ReportedPackage{Name: name, Version: version, Purl: purl}
			byIdentity[identity] = pkg
			order = append(order, identity)
		}
		id, err := registry.ParseID(sbomID)
		if err != nil {
			return nil, nil, fmt.Errorf("restore package SBOM id: %w", err)
		}
		restoredBuildID, err := registry.ParseID(sbomBuildID)
		if err != nil {
			return nil, nil, fmt.Errorf("restore package SBOM build id: %w", err)
		}
		pkg.Sboms = append(pkg.Sboms, Sbom{
			ID: id, BuildID: restoredBuildID, Name: sbomName, Format: format,
			CreatedAt: createdAt.Time,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("list build packages: %w", err)
	}
	packages := make([]ReportedPackage, 0, len(order))
	for _, identity := range order {
		packages = append(packages, *byIdentity[identity])
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit list build packages: %w", err)
	}
	return packages, nil, nil
}
