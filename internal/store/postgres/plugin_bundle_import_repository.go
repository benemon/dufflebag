package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
)

var (
	ErrPluginBundleImportBusy     = errors.New("plugin bundle import is already queued or running")
	ErrPluginBundleImportNotFound = errors.New("plugin bundle import not found")
)

// PluginBundleUpload is an uploaded bundle waiting in object storage.
type PluginBundleUpload struct {
	ObjectKey string
	Filename  string
	Size      int64
	SHA256    string
}

// PluginBundleOrigin is what the imported bundle's manifest said about itself.
type PluginBundleOrigin struct {
	Host         string
	Organization string
	ExportedAt   *time.Time
	Version      string
	PluginCount  int
	VersionCount int
}

// PluginBundleImport is the current import job and its outcome.
type PluginBundleImport struct {
	State       string
	RequestedBy string
	Upload      PluginBundleUpload
	CreatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	Error       string
	Origin      PluginBundleOrigin
	Outcomes    json.RawMessage
}

// ClaimedPluginBundleImport is an import a worker now owns.
type ClaimedPluginBundleImport struct {
	Tenant OrganizationTenant
	Upload PluginBundleUpload
}

// QueuePluginBundleImport queues an import of an uploaded bundle unless one
// is already active.
func (r *Repository) QueuePluginBundleImport(ctx context.Context, tenant OrganizationTenant, requestedBy string, upload PluginBundleUpload) error {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	changed, err := q.QueuePluginBundleImport(ctx, postgresdb.QueuePluginBundleImportParams{
		OrganizationID: tenant.OrganizationID, RequestedBy: requestedBy,
		ObjectKey: upload.ObjectKey, Filename: upload.Filename, Size: upload.Size, Sha256: upload.SHA256,
	})
	if err != nil {
		return fmt.Errorf("queue plugin bundle import: %w", err)
	}
	if changed == 0 {
		if _, err := q.GetPluginRegistry(ctx, tenant.OrganizationID); errors.Is(err, sql.ErrNoRows) {
			return ErrPluginRegistryNotEnabled
		} else if err != nil {
			return fmt.Errorf("get plugin registry: %w", err)
		}
		return ErrPluginBundleImportBusy
	}
	return tx.Commit()
}

// ClaimPluginBundleImport hands one queued or stale import to the caller.
func (r *Repository) ClaimPluginBundleImport(ctx context.Context, stale time.Duration) (*ClaimedPluginBundleImport, error) {
	organizations, err := postgresdb.New(r.db).ListOrganizationIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	for _, organizationID := range organizations {
		tenant := OrganizationTenant{OrganizationID: organizationID}
		tx, q, err := r.beginOrganization(ctx, tenant)
		if err != nil {
			return nil, err
		}
		row, err := q.ClaimPluginBundleImport(ctx, postgresdb.ClaimPluginBundleImportParams{OrganizationID: organizationID, StaleSeconds: stale.Seconds()})
		if errors.Is(err, sql.ErrNoRows) {
			_ = tx.Rollback()
			continue
		}
		if err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("claim plugin bundle import: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit plugin bundle import claim: %w", err)
		}
		return &ClaimedPluginBundleImport{Tenant: tenant, Upload: PluginBundleUpload{ObjectKey: row.ObjectKey, Filename: row.Filename, SHA256: row.Sha256}}, nil
	}
	return nil, nil
}

// RecordPluginBundleImport records progress or the final outcome of an import.
func (r *Repository) RecordPluginBundleImport(
	ctx context.Context, tenant OrganizationTenant, state string, origin PluginBundleOrigin, outcomes any, runErr string,
) error {
	encoded, err := json.Marshal(outcomes)
	if err != nil {
		return fmt.Errorf("encode plugin bundle import outcomes: %w", err)
	}
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	params := postgresdb.RecordPluginBundleImportParams{
		OrganizationID: tenant.OrganizationID, State: state, Error: runErr, Outcomes: encoded,
		BundleHost: origin.Host, BundleOrganization: origin.Organization, BundleVersion: origin.Version,
		PluginCount: int32(origin.PluginCount), VersionCount: int32(origin.VersionCount),
	}
	if origin.ExportedAt != nil {
		params.BundleExportedAt = sql.NullTime{Time: *origin.ExportedAt, Valid: true}
	}
	if err := q.RecordPluginBundleImport(ctx, params); err != nil {
		return fmt.Errorf("record plugin bundle import: %w", err)
	}
	return tx.Commit()
}

// GetPluginBundleImport reads the current import job.
func (r *Repository) GetPluginBundleImport(ctx context.Context, tenant OrganizationTenant) (PluginBundleImport, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return PluginBundleImport{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.GetPluginBundleImport(ctx, tenant.OrganizationID)
	if errors.Is(err, sql.ErrNoRows) {
		if _, registryErr := q.GetPluginRegistry(ctx, tenant.OrganizationID); errors.Is(registryErr, sql.ErrNoRows) {
			return PluginBundleImport{}, ErrPluginRegistryNotEnabled
		} else if registryErr != nil {
			return PluginBundleImport{}, fmt.Errorf("get plugin registry: %w", registryErr)
		}
		return PluginBundleImport{}, ErrPluginBundleImportNotFound
	}
	if err != nil {
		return PluginBundleImport{}, fmt.Errorf("get plugin bundle import: %w", err)
	}
	job := PluginBundleImport{
		State: row.State, RequestedBy: row.RequestedBy,
		Upload:    PluginBundleUpload{ObjectKey: row.ObjectKey, Filename: row.Filename, Size: row.Size, SHA256: row.Sha256},
		CreatedAt: utc(row.CreatedAt), StartedAt: nullTime(row.StartedAt), FinishedAt: nullTime(row.FinishedAt), Error: row.Error,
		Origin: PluginBundleOrigin{
			Host: row.BundleHost, Organization: row.BundleOrganization, ExportedAt: nullTime(row.BundleExportedAt), Version: row.BundleVersion,
			PluginCount: int(row.PluginCount), VersionCount: int(row.VersionCount),
		},
		Outcomes: row.Outcomes,
	}
	return job, nil
}

// PluginVersionSumsDigest is the digest of a held version's SHA256SUMS, or
// empty when the organization does not hold the version. The object key's
// last segment is the file's digest (objectstore.PluginKey).
func (r *Repository) PluginVersionSumsDigest(ctx context.Context, tenant OrganizationTenant, name, version string) (string, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	key, err := q.GetPluginVersionSumsKey(ctx, postgresdb.GetPluginVersionSumsKeyParams{OrganizationID: tenant.OrganizationID, Name: name, Version: version})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get plugin version sums: %w", err)
	}
	return path.Base(key), nil
}
