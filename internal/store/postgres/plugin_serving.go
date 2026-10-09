package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"

	"github.com/benemon/dufflebag/internal/domain/plugin"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/google/uuid"
)

// ServedPluginFile is one file the read plane answers with: stored bytes by
// object key, or content the registry renders.
type ServedPluginFile struct {
	ObjectKey string
	Size      int64
	Content   []byte
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
	stored := func(key sql.NullString, size sql.NullInt64) (ServedPluginFile, error) {
		if !key.Valid {
			return ServedPluginFile{}, registry.ErrNotFound
		}
		return ServedPluginFile{ObjectKey: key.String, Size: size.Int64}, nil
	}
	switch kind {
	case ServedSums:
		return ServedPluginFile{ObjectKey: row.SumsKey, Size: row.SumsSize}, nil
	case ServedSignature:
		return stored(row.SignatureKey, row.SignatureSize)
	case ServedManifest:
		if row.ManifestKey.Valid {
			return stored(row.ManifestKey, row.ManifestSize)
		}
		if row.ProtocolVersion.Valid {
			return ServedPluginFile{Content: plugin.RenderManifest(row.ProtocolVersion.String)}, nil
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
	return ServedPluginFile{ObjectKey: zip.ObjectKey, Size: zip.Size}, nil
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
