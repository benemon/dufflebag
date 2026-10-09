package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/objectstore"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/google/uuid"
)

var (
	ErrPluginVersionExists = errors.New("plugin version already exists")
	ErrPluginSourceHeld    = errors.New("plugin name is held by another source")
)

// PluginSource is where a plugin's versions come from (ADR-0027 A2).
type PluginSource struct {
	Kind       string
	Repository string
}

// PluginBlob is one file of a version, readable more than once.
type PluginBlob struct {
	Filename string
	SHA256   string
	Body     io.ReadSeeker
	Size     int64
}

// PluginZip is a stored zip and the platform it serves.
type PluginZip struct {
	PluginBlob
	OS   string
	Arch string
}

// PluginVersionInput is a verified version ready to persist. Protocol is set
// only when the registry must render the manifest Packer will request.
type PluginVersionInput struct {
	Name      string
	Version   string
	Source    PluginSource
	Protocol  string
	Listed    []string
	Sums      PluginBlob
	Signature *PluginBlob
	Manifest  *PluginBlob
	Zips      []PluginZip
}

// PluginSummary is one plugin in an organization's catalogue.
type PluginSummary struct {
	Name              string
	Source            PluginSource
	PublishedVersions []string
}

// PluginVersionSummary is one version with the platforms its sums list and
// the platforms the registry holds zips for.
type PluginVersionSummary struct {
	Version   string
	Revoked   bool
	Listed    []string
	Stored    []string
	CreatedAt time.Time
}

// HeldSourceError names the source already holding a plugin name.
type HeldSourceError struct {
	Source PluginSource
}

func (e HeldSourceError) Error() string { return ErrPluginSourceHeld.Error() }

func (e HeldSourceError) Unwrap() error { return ErrPluginSourceHeld }

// PublishPluginVersion stores a verified version. Blobs land before rows:
// a later failure leaves invisible orphans, never a row without bytes.
func (r *Repository) PublishPluginVersion(
	ctx context.Context, tenant OrganizationTenant, input PluginVersionInput,
) error {
	objects, err := r.objectStore()
	if err != nil {
		return err
	}
	if err := r.admitPluginVersion(ctx, tenant, input); err != nil {
		return err
	}

	organizationID := tenant.OrganizationID.String()
	key := func(blob PluginBlob) string {
		return objectstore.PluginKey(organizationID, input.Name, input.Version, blob.Filename, blob.SHA256)
	}
	blobs := []PluginBlob{input.Sums}
	if input.Signature != nil {
		blobs = append(blobs, *input.Signature)
	}
	if input.Manifest != nil {
		blobs = append(blobs, *input.Manifest)
	}
	for _, zip := range input.Zips {
		blobs = append(blobs, zip.PluginBlob)
	}
	for _, blob := range blobs {
		if _, err := blob.Body.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind %s: %w", blob.Filename, err)
		}
		if err := objects.PutFile(ctx, key(blob), blob.Body, blob.Size); err != nil {
			return fmt.Errorf("%w: %v", ErrObjectStorageUnavailable, err)
		}
	}

	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	pluginID, err := q.InsertPlugin(ctx, postgresdb.InsertPluginParams{
		ID: uuid.New(), OrganizationID: tenant.OrganizationID, Name: input.Name,
		SourceKind: input.Source.Kind, SourceRepository: nullString(input.Source.Repository),
	})
	if errors.Is(err, sql.ErrNoRows) {
		existing, getErr := q.GetPlugin(ctx, postgresdb.GetPluginParams{OrganizationID: tenant.OrganizationID, Name: input.Name})
		if getErr != nil {
			return fmt.Errorf("get plugin %s: %w", input.Name, getErr)
		}
		if held := sourceOf(existing.SourceKind, existing.SourceRepository); held != input.Source {
			return HeldSourceError{Source: held}
		}
		pluginID = existing.ID
	} else if err != nil {
		return fmt.Errorf("insert plugin %s: %w", input.Name, err)
	}

	optional := func(blob *PluginBlob) (sql.NullString, sql.NullInt64) {
		if blob == nil {
			return sql.NullString{}, sql.NullInt64{}
		}
		return sql.NullString{String: key(*blob), Valid: true}, sql.NullInt64{Int64: blob.Size, Valid: true}
	}
	signatureKey, signatureSize := optional(input.Signature)
	manifestKey, manifestSize := optional(input.Manifest)
	versionID, err := q.InsertPluginVersion(ctx, postgresdb.InsertPluginVersionParams{
		ID: uuid.New(), OrganizationID: tenant.OrganizationID, PluginID: pluginID, Version: input.Version,
		ProtocolVersion: nullString(input.Protocol), ListedPlatforms: input.Listed,
		SumsKey: key(input.Sums), SumsSize: input.Sums.Size,
		SignatureKey: signatureKey, SignatureSize: signatureSize,
		ManifestKey: manifestKey, ManifestSize: manifestSize,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPluginVersionExists
	}
	if err != nil {
		return fmt.Errorf("insert plugin version %s %s: %w", input.Name, input.Version, err)
	}
	for _, zip := range input.Zips {
		if err := q.InsertPluginFile(ctx, postgresdb.InsertPluginFileParams{
			OrganizationID: tenant.OrganizationID, VersionID: versionID, Filename: zip.Filename,
			Os: zip.OS, Arch: zip.Arch, Sha256: zip.SHA256, Size: zip.Size, ObjectKey: key(zip.PluginBlob),
		}); err != nil {
			return fmt.Errorf("insert plugin file %s: %w", zip.Filename, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit plugin version %s %s: %w", input.Name, input.Version, err)
	}
	return nil
}

// admitPluginVersion refuses before any bytes reach the object store, so a
// predictable refusal never costs an upload. The insert re-checks under the
// transaction, which is what makes the refusal authoritative.
func (r *Repository) admitPluginVersion(
	ctx context.Context, tenant OrganizationTenant, input PluginVersionInput,
) error {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := q.GetPluginRegistry(ctx, tenant.OrganizationID); errors.Is(err, sql.ErrNoRows) {
		return ErrPluginRegistryNotEnabled
	} else if err != nil {
		return fmt.Errorf("get plugin registry: %w", err)
	}
	existing, err := q.GetPlugin(ctx, postgresdb.GetPluginParams{OrganizationID: tenant.OrganizationID, Name: input.Name})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("get plugin %s: %w", input.Name, err)
	}
	if held := sourceOf(existing.SourceKind, existing.SourceRepository); held != input.Source {
		return HeldSourceError{Source: held}
	}
	exists, err := q.PluginVersionExists(ctx, postgresdb.PluginVersionExistsParams{
		OrganizationID: tenant.OrganizationID, Name: input.Name, Version: input.Version,
	})
	if err != nil {
		return fmt.Errorf("check plugin version: %w", err)
	}
	if exists {
		return ErrPluginVersionExists
	}
	return nil
}

// ListPlugins returns the organization's plugins, empty when the registry
// is not enabled.
func (r *Repository) ListPlugins(ctx context.Context, tenant OrganizationTenant) ([]PluginSummary, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := q.ListPlugins(ctx, tenant.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("list plugins: %w", err)
	}
	plugins := make([]PluginSummary, 0, len(rows))
	for _, row := range rows {
		plugins = append(plugins, PluginSummary{
			Name: row.Name, Source: sourceOf(row.SourceKind, row.SourceRepository),
			PublishedVersions: row.PublishedVersions,
		})
	}
	return plugins, nil
}

// ListPluginVersions returns one plugin and its versions, newest first.
func (r *Repository) ListPluginVersions(
	ctx context.Context, tenant OrganizationTenant, name string,
) (PluginSource, []PluginVersionSummary, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return PluginSource{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	plugin, err := q.GetPlugin(ctx, postgresdb.GetPluginParams{OrganizationID: tenant.OrganizationID, Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return PluginSource{}, nil, fmt.Errorf("%w: plugin %s", registry.ErrNotFound, name)
	}
	if err != nil {
		return PluginSource{}, nil, fmt.Errorf("get plugin %s: %w", name, err)
	}
	rows, err := q.ListPluginVersions(ctx, plugin.ID)
	if err != nil {
		return PluginSource{}, nil, fmt.Errorf("list plugin versions: %w", err)
	}
	versions := make([]PluginVersionSummary, 0, len(rows))
	for _, row := range rows {
		versions = append(versions, PluginVersionSummary{
			Version: row.Version, Revoked: row.Revoked, Listed: row.ListedPlatforms,
			Stored: row.StoredPlatforms, CreatedAt: row.CreatedAt,
		})
	}
	return sourceOf(plugin.SourceKind, plugin.SourceRepository), versions, nil
}

func sourceOf(kind string, repository sql.NullString) PluginSource {
	return PluginSource{Kind: kind, Repository: repository.String}
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

// Rows are already committed away when this runs, so a failed delete leaves a
// harmless orphan rather than a row whose bytes are gone.
func (r *Repository) deletePluginObjects(ctx context.Context, keys []string) {
	if len(keys) == 0 {
		return
	}
	if r.objects == nil {
		slog.Default().Warn("delete plugin objects", "error", ErrObjectStorageNotConfigured, "count", len(keys))
		return
	}
	for _, key := range keys {
		if err := r.objects.Delete(ctx, key); err != nil {
			slog.Default().Warn("delete plugin object", "object_key", key, "error", err)
		}
	}
}

var (
	ErrPluginVersionRevoked    = errors.New("plugin version already revoked")
	ErrPluginVersionNotRevoked = errors.New("plugin version not revoked")
)

// SetPluginVersionRevoked revokes or restores one version. Revoked versions
// keep their bytes and their plugin's name; only serving stops.
func (r *Repository) SetPluginVersionRevoked(
	ctx context.Context, tenant OrganizationTenant, name, version string, revoked bool,
) error {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = q.SetPluginVersionRevoked(ctx, postgresdb.SetPluginVersionRevokedParams{
		OrganizationID: tenant.OrganizationID, Name: name, Version: version, Revoked: revoked,
	})
	if errors.Is(err, sql.ErrNoRows) {
		current, getErr := q.GetPluginVersionRevoked(ctx, postgresdb.GetPluginVersionRevokedParams{
			OrganizationID: tenant.OrganizationID, Name: name, Version: version,
		})
		switch {
		case errors.Is(getErr, sql.ErrNoRows):
			return fmt.Errorf("%w: plugin version %s %s", registry.ErrNotFound, name, version)
		case getErr != nil:
			return fmt.Errorf("get plugin version: %w", getErr)
		case current:
			return ErrPluginVersionRevoked
		default:
			return ErrPluginVersionNotRevoked
		}
	}
	if err != nil {
		return fmt.Errorf("set plugin version revoked: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit plugin version revocation: %w", err)
	}
	return nil
}

// DeletePluginVersion removes one version and its files. Removing a plugin's
// last version removes the plugin, which frees its name for another source
// (ADR-0027 A8).
func (r *Repository) DeletePluginVersion(ctx context.Context, tenant OrganizationTenant, name, version string) error {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	where := postgresdb.ListPluginVersionObjectKeysParams{OrganizationID: tenant.OrganizationID, Name: name, Version: version}
	keys, err := q.ListPluginVersionObjectKeys(ctx, where)
	if err != nil {
		return fmt.Errorf("list plugin version objects: %w", err)
	}
	pluginID, err := q.DeletePluginVersion(ctx, postgresdb.DeletePluginVersionParams(where))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: plugin version %s %s", registry.ErrNotFound, name, version)
	}
	if err != nil {
		return fmt.Errorf("delete plugin version: %w", err)
	}
	if err := q.DeletePluginIfEmpty(ctx, pluginID); err != nil {
		return fmt.Errorf("delete empty plugin: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit plugin version deletion: %w", err)
	}
	r.deletePluginObjects(ctx, keys)
	return nil
}
