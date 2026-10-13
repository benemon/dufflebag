package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/benemon/dufflebag/internal/domain/plugin"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/google/uuid"
)

// ServedPluginFile is one file the read plane answers with: stored bytes by
// object key, or content the registry renders.
type ServedPluginFile struct {
	OrganizationID uuid.UUID
	PluginID       uuid.UUID
	ObjectKey      string
	Size           int64
	Content        []byte
}

// PluginFileKind names which of a version's files a read asks for.
type PluginFileKind int

const (
	ServedSums PluginFileKind = iota
	ServedSignature
	ServedManifest
	ServedZip
)

// The organization is resolved by name outside any tenant, because
// organizations carry no RLS; everything after runs under its tenant.
func (r *Repository) beginServedOrganization(
	ctx context.Context, organization string,
) (*sql.Tx, *postgresdb.Queries, uuid.UUID, error) {
	id, err := postgresdb.New(r.db).GetOrganizationIDByName(ctx, organization)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, uuid.Nil, registry.ErrNotFound
	}
	if err != nil {
		return nil, nil, uuid.Nil, fmt.Errorf("resolve organization: %w", err)
	}
	tx, q, err := r.beginOrganization(ctx, OrganizationTenant{OrganizationID: id})
	return tx, q, id, err
}

// ServedPluginVersions lists the versions an exposed registry serves for one
// plugin. Unexposed, unknown and fully revoked all answer ErrNotFound.
func (r *Repository) ServedPluginVersions(ctx context.Context, organization, name string) ([]string, error) {
	tx, q, id, err := r.beginServedOrganization(ctx, organization)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	versions, err := q.ServedPluginVersions(ctx, postgresdb.ServedPluginVersionsParams{OrganizationID: id, Name: name})
	if err != nil {
		return nil, fmt.Errorf("list served plugin versions: %w", err)
	}
	if len(versions) == 0 {
		return nil, registry.ErrNotFound
	}
	return versions, nil
}

// ServedPluginFile resolves one file of a served version.
func (r *Repository) ServedPluginFile(
	ctx context.Context, organization, name, version string, kind PluginFileKind, filename string,
) (ServedPluginFile, error) {
	tx, q, id, err := r.beginServedOrganization(ctx, organization)
	if err != nil {
		return ServedPluginFile{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.GetServedPluginVersion(ctx, postgresdb.GetServedPluginVersionParams{
		OrganizationID: id, Name: name, Version: version,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ServedPluginFile{}, registry.ErrNotFound
	}
	if err != nil {
		return ServedPluginFile{}, fmt.Errorf("get served plugin version: %w", err)
	}
	served := ServedPluginFile{OrganizationID: id, PluginID: row.PluginID}
	stored := func(key sql.NullString, size sql.NullInt64) (ServedPluginFile, error) {
		if !key.Valid {
			return ServedPluginFile{}, registry.ErrNotFound
		}
		served.ObjectKey, served.Size = key.String, size.Int64
		return served, nil
	}
	switch kind {
	case ServedSums:
		served.ObjectKey, served.Size = row.SumsKey, row.SumsSize
		return served, nil
	case ServedSignature:
		return stored(row.SignatureKey, row.SignatureSize)
	case ServedManifest:
		if row.ManifestKey.Valid {
			return stored(row.ManifestKey, row.ManifestSize)
		}
		if row.ProtocolVersion.Valid {
			served.Content = plugin.RenderManifest(row.ProtocolVersion.String)
			return served, nil
		}
		return ServedPluginFile{}, registry.ErrNotFound
	}
	zip, err := q.GetServedPluginZip(ctx, postgresdb.GetServedPluginZipParams{VersionID: row.ID, Filename: filename})
	if errors.Is(err, sql.ErrNoRows) {
		return ServedPluginFile{}, registry.ErrNotFound
	}
	if err != nil {
		return ServedPluginFile{}, fmt.Errorf("get served plugin zip: %w", err)
	}
	served.ObjectKey, served.Size = zip.ObjectKey, zip.Size
	return served, nil
}

// CountPluginDownload records one completed plugin zip serve.
func (r *Repository) CountPluginDownload(
	ctx context.Context, tenant OrganizationTenant, pluginID uuid.UUID, version, platform string, day time.Time,
) error {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := q.CountPluginDownload(ctx, postgresdb.CountPluginDownloadParams{
		OrganizationID: tenant.OrganizationID, PluginID: pluginID, Version: version,
		Platform: platform, Day: day,
	}); err != nil {
		return fmt.Errorf("count plugin download: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit plugin download: %w", err)
	}
	return nil
}

// OpenPluginObject streams a stored plugin file.
func (r *Repository) OpenPluginObject(ctx context.Context, key string) (io.ReadCloser, error) {
	objects, err := r.objectStore()
	if err != nil {
		return nil, err
	}
	body, err := objects.Open(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrObjectStorageUnavailable, err)
	}
	return body, nil
}
