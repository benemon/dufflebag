package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/keyring"
	"github.com/benemon/dufflebag/internal/store/objectstore"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/benemon/dufflebag/internal/webhook"
	"github.com/google/uuid"
)

// Tenant is the explicit RLS scope for one repository operation.
type Tenant struct {
	OrganizationID uuid.UUID
	ProjectID      uuid.UUID

	// denied records a tenant the caller is not entitled to. It is separate from
	// malformed so the two reasons stay distinguishable in logs, even though both
	// produce the same answer to the client (ADR-0017).
	denied bool

	// malformed records tenant identifiers that did not parse as UUIDs.
	//
	// Packer sends whatever HCP_ORGANIZATION_ID contains and the SDK validates
	// nothing, so a misconfigured environment reaches us as a non-UUID path
	// parameter. It is carried rather than rejected at construction so that
	// every operation answers not-found — a malformed identifier is
	// indistinguishable, to the client, from one that does not exist, which is
	// both the compatible and the correct disclosure posture (ADR-0016).
	malformed bool
}

// DeniedTenant is a tenant that no operation will resolve.
//
// Authorization failures are represented as a tenant rather than as an error at
// the call site, so every existing not-found path answers them with its own
// endpoint-appropriate code — and so a handler cannot obtain a usable tenant
// without having been authorized for it.
func DeniedTenant() Tenant { return Tenant{denied: true} }

// ParseTenant reads the two tenant path parameters.
func ParseTenant(organizationID, projectID string) Tenant {
	organization, organizationErr := uuid.Parse(organizationID)
	project, projectErr := uuid.Parse(projectID)
	if organizationErr != nil || projectErr != nil {
		return Tenant{malformed: true}
	}
	return Tenant{OrganizationID: organization, ProjectID: project}
}

// Bucket is the persisted bucket metadata used by the API adapters.
type Bucket struct {
	ID                  registry.ID
	Name                string
	Description         string
	Labels              map[string]string
	LatestVersion       *registry.Version
	LatestVersionBuilds []StoredBuild
	ChildrenStatus      *registry.AncestryStatus
	Platforms           []string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Artifact is one external artifact recorded for a build.
type Artifact struct {
	ID                 registry.ID
	BuildID            registry.ID
	ExternalIdentifier string
	Region             string
	CreatedAt          time.Time
}

// Sbom is one stored SBOM document attached to a build. The bytes are kept
// exactly as the client sent them even though a package projection is parsed
// from them; the projection is client-reported inventory, not verified fact.
type Sbom struct {
	ID             registry.ID
	BuildID        registry.ID
	Name           string
	Format         string
	CompressedData []byte
	ParseStatus    string
	ParseError     string
	CreatedAt      time.Time
	objectKey      string
}

// ReportedPackage is package inventory asserted by one or more client-supplied
// SBOMs. Dufflebag parses and attributes it but does not verify it against the
// image, matching the treatment of forwarded_for and ancestry input.
type ReportedPackage struct {
	Name           string
	Version        string
	Purl           string
	Licenses       []string
	ComponentPaths [][]string
	Sboms          []Sbom
}

// StoredBuild is the domain build plus persisted compatibility-neutral metadata.
type StoredBuild struct {
	registry.Build
	VersionID                registry.ID
	PackerRunUUID            string
	Labels                   map[string]string
	Metadata                 json.RawMessage
	SourceExternalIdentifier string
	ParentVersionID          string
	ParentChannelID          string
	Artifacts                []Artifact
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// VersionAssignedError identifies the user channels that must be unassigned
// before a version can be deleted.
type VersionAssignedError struct {
	Channels []string
}

func (e *VersionAssignedError) Error() string {
	return "Version is assigned by channels: " + strings.Join(e.Channels, ", ") +
		". Please, remove the channels assignment before deleting the version."
}

// Repository persists registry aggregates in tenant-scoped transactions.
type Repository struct {
	db      *sql.DB
	objects *objectstore.Store
	// ring is present only on encrypted deployments (ADR-0024). It is set
	// once at startup, after the mode marker admits the configuration; nil
	// means every payload is stored and read as plaintext.
	ring *keyring.Keyring
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// SetKeyring arms payload encryption and row integrity. It is called after
// construction because loading the keyring needs this repository: the wrapped
// entries live in the database it fronts.
func (r *Repository) SetKeyring(ring *keyring.Keyring) { r.ring = ring }

// payloadAAD binds a ciphertext to its tenant and row so a valid envelope
// relocated to another row or tenant fails to decrypt (ADR-0024).
func payloadAAD(tenant Tenant, kind, id string) []byte {
	return []byte(tenant.OrganizationID.String() + "|" + tenant.ProjectID.String() + "|" + kind + "|" + id)
}

// NewRepositoryWithObjectStore enables the object-only SBOM store configured
// by the deployment. A repository without one refuses SBOM transfers.
func NewRepositoryWithObjectStore(db *sql.DB, objects *objectstore.Store) *Repository {
	return &Repository{db: db, objects: objects}
}

func (r *Repository) CreateBucket(ctx context.Context, tenant Tenant, bucket Bucket) (*Bucket, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	labels, err := json.Marshal(bucket.Labels)
	if err != nil {
		return nil, fmt.Errorf("marshal bucket labels: %w", err)
	}
	row, err := q.CreateBucket(ctx, postgresdb.CreateBucketParams{
		OrganizationID: tenant.OrganizationID,
		ProjectID:      tenant.ProjectID,
		ID:             bucket.ID.String(),
		Name:           bucket.Name,
		Description:    bucket.Description,
		Labels:         labels,
		CreatedAt:      bucket.CreatedAt,
	})
	// ON CONFLICT DO NOTHING returns no row for a duplicate name. Packer upserts
	// the bucket at the start of every build and tolerates AlreadyExists, so the
	// duplicate must surface as ErrConflict, not as a failure (duf-ano).
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("create bucket: %w", registry.ErrConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("create bucket: %w", err)
	}
	// Every bucket carries a managed "latest" channel from the instant it
	// exists: live CreateBucket auto-creates it with managed:true,
	// restricted:true, unassigned, in the same instant (dossier §7, Appendix A
	// probes 04-06). Same transaction, so no client can observe a channel-less
	// bucket. Migration 000008 establishes the same invariant for buckets that
	// predate it.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO channels (
			organization_id, project_id, id, bucket_id, name, restricted, managed, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'latest', true, true, $5, $5)
	`, tenant.OrganizationID, tenant.ProjectID, registry.NewID(bucket.CreatedAt).String(),
		row.ID, bucket.CreatedAt); err != nil {
		return nil, fmt.Errorf("create managed latest channel: %w", err)
	}
	created, err := restoreBucket(row)
	if err != nil {
		return nil, err
	}
	if err := enqueueWebhookEvent(ctx, q, tenant, webhook.OperationBucketCreated,
		webhook.Target{Type: "bucket", Bucket: created.Name}, bucketWebhookPayload(tenant, *created), bucket.CreatedAt,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create bucket: %w", err)
	}
	return created, nil
}

func (r *Repository) GetBucket(ctx context.Context, tenant Tenant, name string) (*Bucket, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := q.GetBucketByName(ctx, name)
	if err != nil {
		return nil, mapNotFound("get bucket", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit get bucket: %w", err)
	}
	return restoreBucket(row)
}

// DeleteBucket removes a bucket and everything under it.
//
// Versions, builds, artifacts, channels and the sboms follow by cascade, and
// migration 000006 lets the cascade through the append-only trigger on
// channel_assignments. Unassignment markers (rows with no version) are left
// behind deliberately: like history for a deleted channel, they outlive what
// they describe, and nothing lists them once the channel is gone.
func (r *Repository) DeleteBucket(ctx context.Context, tenant Tenant, name string) error {
	at := time.Now().UTC()
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	bucketRow, err := q.GetBucketByName(ctx, name)
	if err != nil {
		return mapNotFound("get bucket for deletion", err)
	}
	bucket, err := restoreBucket(bucketRow)
	if err != nil {
		return err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT sboms.object_key
		FROM sboms
		JOIN builds ON builds.id = sboms.build_id
		JOIN versions ON versions.id = builds.version_id
		JOIN buckets ON buckets.id = versions.bucket_id
		WHERE buckets.name = $1
	`, name)
	if err != nil {
		return fmt.Errorf("list bucket SBOM objects: %w", err)
	}
	objectKeys, err := scanObjectKeys(rows)
	if err != nil {
		return fmt.Errorf("list bucket SBOM objects: %w", err)
	}
	if _, err := q.DeleteBucketByName(ctx, name); err != nil {
		return mapNotFound("delete bucket", err)
	}
	if err := enqueueWebhookEvent(ctx, q, tenant, webhook.OperationBucketDeleted,
		webhook.Target{Type: "bucket", Bucket: name}, bucketWebhookPayload(tenant, *bucket), at,
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete bucket: %w", err)
	}
	r.deleteSBOMObjects(ctx, objectKeys)
	return nil
}

// DeleteVersion removes a version aggregate after refusing any current user
// channel assignment. The managed latest channel is re-pointed using the same
// newest-valid assignment selection as revocation rollback.
func (r *Repository) DeleteVersion(
	ctx context.Context,
	tenant Tenant,
	bucketName, fingerprint string,
	at time.Time,
) error {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// This is deliberately the first data query. A current user-channel
	// assignment is the endpoint's authoritative refusal, while managed latest
	// is service-owned tracking and never blocks deletion.
	rows, err := tx.QueryContext(ctx, `
		SELECT channels.name
		FROM channels
		JOIN buckets ON buckets.id = channels.bucket_id
		JOIN LATERAL (
			SELECT assignments.version_id
			FROM channel_assignments AS assignments
			WHERE assignments.channel_id = channels.id
			ORDER BY assignments.assigned_at DESC, assignments.id DESC
			LIMIT 1
		) AS current_assignment ON true
		JOIN versions ON versions.id = current_assignment.version_id
		WHERE buckets.name = $1
		  AND versions.fingerprint = $2
		  AND NOT channels.managed
		ORDER BY channels.name
	`, bucketName, fingerprint)
	if err != nil {
		return fmt.Errorf("list assigning channels: %w", err)
	}
	channels := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan assigning channel: %w", err)
		}
		channels = append(channels, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("list assigning channels: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close assigning channels: %w", err)
	}
	if len(channels) != 0 {
		return &VersionAssignedError{Channels: channels}
	}

	versionRow, err := q.GetVersionByFingerprint(ctx, postgresdb.GetVersionByFingerprintParams{
		Name: bucketName, Fingerprint: fingerprint,
	})
	if err != nil {
		return mapNotFound("delete version", err)
	}
	version, err := r.restoreVersion(ctx, q, tenant, versionRow)
	if err != nil {
		return err
	}

	var latestChannelID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT channels.id
		FROM channels
		JOIN LATERAL (
			SELECT assignments.version_id
			FROM channel_assignments AS assignments
			WHERE assignments.channel_id = channels.id
			ORDER BY assignments.assigned_at DESC, assignments.id DESC
			LIMIT 1
		) AS current_assignment ON true
		WHERE channels.bucket_id = $1 AND channels.managed
		  AND current_assignment.version_id = $2
	`, versionRow.BucketID, versionRow.ID).Scan(&latestChannelID)
	if errors.Is(err, sql.ErrNoRows) {
		latestChannelID = sql.NullString{}
	} else if err != nil {
		return fmt.Errorf("find managed latest assignment: %w", err)
	}

	rows, err = tx.QueryContext(ctx, `
		SELECT sboms.object_key
		FROM sboms
		JOIN builds ON builds.id = sboms.build_id
		WHERE builds.version_id = $1
	`, versionRow.ID)
	if err != nil {
		return fmt.Errorf("list version SBOM objects: %w", err)
	}
	objectKeys, err := scanObjectKeys(rows)
	if err != nil {
		return fmt.Errorf("list version SBOM objects: %w", err)
	}
	var latestBefore *Channel
	if latestChannelID.Valid {
		latestBefore, err = r.getChannel(ctx, tx, q, tenant, bucketName, "latest")
		if err != nil {
			return fmt.Errorf("read managed latest before version delete: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM versions WHERE id = $1`, versionRow.ID); err != nil {
		return fmt.Errorf("delete version: %w", err)
	}
	if latestChannelID.Valid {
		channelID, err := registry.ParseID(latestChannelID.String)
		if err != nil {
			return fmt.Errorf("parse managed latest channel id: %w", err)
		}
		rollbackID, found, err := r.newestValidAssignedVersion(ctx, tx, channelID)
		if err != nil {
			return err
		}
		if found {
			if err := r.recordAssignment(ctx, tx, tenant, channelID, rollbackID, "Dufflebag", at); err != nil {
				return fmt.Errorf("rollback managed latest assignment: %w", err)
			}
		} else {
			unassignedAt := assignmentWriteTime(at)
			assignmentID := registry.NewID(at).String()
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO channel_assignments (
					organization_id, project_id, id, channel_id, version_id, author_id, assigned_at, integrity_mac
				) VALUES ($1, $2, $3, $4, NULL, 'Dufflebag', $5, $6)
			`, tenant.OrganizationID, tenant.ProjectID, assignmentID, channelID.String(), unassignedAt,
				r.rowMAC(assignmentMACMessage(tenant, assignmentID, channelID.String(), "", "Dufflebag", unassignedAt))); err != nil {
				return fmt.Errorf("clear managed latest assignment: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE channels SET updated_at = $2 WHERE id = $1`, channelID.String(), at); err != nil {
				return fmt.Errorf("update cleared managed latest channel: %w", err)
			}
		}
		latestAfter, err := r.getChannel(ctx, tx, q, tenant, bucketName, "latest")
		if err != nil {
			return fmt.Errorf("read managed latest after version delete: %w", err)
		}
		if err := enqueueWebhookEvent(ctx, q, tenant, webhook.OperationChannelAssigned,
			webhook.Target{Type: "channel", Bucket: bucketName, Name: "latest"},
			channelWebhookPayload(latestAfter, latestBefore), at,
		); err != nil {
			return err
		}
	}
	if err := enqueueWebhookEvent(ctx, q, tenant, webhook.OperationVersionDeleted,
		webhook.Target{Type: "version", Bucket: bucketName, Fingerprint: fingerprint},
		versionWebhookPayload(version, at), at,
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete version: %w", err)
	}
	r.deleteSBOMObjects(ctx, objectKeys)
	return nil
}

// DeleteBuild removes one build aggregate without changing the parent
// version's completion, sequence, or revocation state.
func (r *Repository) DeleteBuild(
	ctx context.Context,
	tenant Tenant,
	bucketName, fingerprint, buildID string,
) error {
	tx, _, err := r.begin(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var persistedID string
	err = tx.QueryRowContext(ctx, `
		SELECT builds.id
		FROM builds
		JOIN versions ON versions.id = builds.version_id
		JOIN buckets ON buckets.id = versions.bucket_id
		WHERE buckets.name = $1 AND versions.fingerprint = $2 AND builds.id = $3
	`, bucketName, fingerprint, buildID).Scan(&persistedID)
	if err != nil {
		return mapNotFound("delete build", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT object_key FROM sboms WHERE build_id = $1`, persistedID)
	if err != nil {
		return fmt.Errorf("list build SBOM objects: %w", err)
	}
	objectKeys, err := scanObjectKeys(rows)
	if err != nil {
		return fmt.Errorf("list build SBOM objects: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM builds WHERE id = $1`, persistedID); err != nil {
		return fmt.Errorf("delete build: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete build: %w", err)
	}
	r.deleteSBOMObjects(ctx, objectKeys)
	return nil
}

func scanObjectKeys(rows *sql.Rows) ([]string, error) {
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			_ = rows.Close()
			return nil, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return keys, nil
}

// deleteSBOMObjects is shared by every aggregate deletion. Rows are already
// committed away before it runs, so a failed delete can only leave a harmless
// orphan; it can never strand a surviving locator whose object is gone.
func (r *Repository) deleteSBOMObjects(ctx context.Context, keys []string) {
	if len(keys) == 0 {
		return
	}
	if r.objects == nil {
		slog.Default().Warn("delete SBOM objects", "error", ErrObjectStorageNotConfigured, "count", len(keys))
		return
	}
	for _, key := range keys {
		if err := r.objects.Delete(ctx, key); err != nil {
			slog.Default().Warn("delete SBOM object", "object_key", key, "error", err)
		}
	}
}

func (r *Repository) UpdateBucket(
	ctx context.Context,
	tenant Tenant,
	name, description string,
	labels map[string]string,
	at time.Time,
) (*Bucket, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	encodedLabels, err := json.Marshal(labels)
	if err != nil {
		return nil, fmt.Errorf("marshal bucket labels: %w", err)
	}
	row, err := q.UpdateBucket(ctx, postgresdb.UpdateBucketParams{
		Name:        name,
		Description: description,
		Labels:      encodedLabels,
		UpdatedAt:   at,
	})
	if err != nil {
		return nil, mapNotFound("update bucket", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit update bucket: %w", err)
	}
	return restoreBucket(row)
}

func (r *Repository) CreateVersion(
	ctx context.Context,
	tenant Tenant,
	version *registry.Version,
) (*registry.Version, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	sequence := sql.NullInt32{}
	if value, ok := version.Sequence(); ok {
		sequence = sql.NullInt32{Int32: int32(value), Valid: true}
	}
	row, err := q.CreateVersion(ctx, postgresdb.CreateVersionParams{
		OrganizationID: tenant.OrganizationID,
		ProjectID:      tenant.ProjectID,
		ID:             version.ID.String(),
		Name:           version.BucketName,
		Fingerprint:    version.Fingerprint,
		TemplateType:   string(version.TemplateType),
		Complete:       version.Complete(),
		Sequence:       sequence,
		CreatedAt:      version.CreatedAt,
		UpdatedAt:      version.UpdatedAt,
		AuthorID:       version.AuthorID,
	})
	if err != nil {
		return nil, mapNotFound("create version", err)
	}
	mac := r.rowMAC(versionMACMessage(row))
	if r.ring != nil {
		if err := q.SetVersionIntegrityMAC(ctx, postgresdb.SetVersionIntegrityMACParams{
			ID: row.ID, IntegrityMac: mac,
		}); err != nil {
			return nil, fmt.Errorf("seal version row: %w", err)
		}
	}

	persisted, err := r.restoreVersion(ctx, q, tenant, postgresdb.GetVersionByFingerprintRow{
		OrganizationID: row.OrganizationID,
		ProjectID:      row.ProjectID,
		ID:             row.ID,
		BucketID:       row.BucketID,
		Fingerprint:    row.Fingerprint,
		TemplateType:   row.TemplateType,
		Complete:       row.Complete,
		Sequence:       row.Sequence,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
		AuthorID:       row.AuthorID,
		IntegrityMac:   mac,
		BucketName:     version.BucketName,
	})
	if err != nil {
		return nil, err
	}
	if err := persisted.EnsureTemplateType(version.TemplateType); err != nil {
		return nil, err
	}
	if err := enqueueWebhookEvent(ctx, q, tenant, webhook.OperationVersionCreated,
		webhook.Target{Type: "version", Bucket: version.BucketName, Fingerprint: version.Fingerprint},
		versionWebhookPayload(persisted, version.CreatedAt), version.CreatedAt,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create version: %w", err)
	}
	return persisted, nil
}

func (r *Repository) GetVersion(
	ctx context.Context,
	tenant Tenant,
	bucketName, fingerprint string,
) (*registry.Version, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := q.GetVersionByFingerprint(ctx, postgresdb.GetVersionByFingerprintParams{
		Name:        bucketName,
		Fingerprint: fingerprint,
	})
	if err != nil {
		return nil, mapNotFound("get version", err)
	}
	version, err := r.restoreVersion(ctx, q, tenant, row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit get version: %w", err)
	}
	return version, nil
}

// ListVersions returns a bucket's versions, newest first.
//
// Ordered by sequence descending so the most recent complete version leads.
// Incomplete versions carry no sequence, so created_at breaks the tie and keeps
// them in a stable order rather than whatever the planner returns.
func (r *Repository) ListVersions(
	ctx context.Context,
	tenant Tenant,
	bucketName string,
) ([]*registry.Version, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := q.ListVersionsByBucket(ctx, bucketName)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	versions := make([]*registry.Version, 0, len(rows))
	for _, row := range rows {
		version, err := r.restoreVersion(ctx, q, tenant, postgresdb.GetVersionByFingerprintRow(row))
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit list versions: %w", err)
	}
	return versions, nil
}

func (r *Repository) CreateBuild(
	ctx context.Context,
	tenant Tenant,
	bucketName, fingerprint string,
	templateType registry.TemplateType,
	build StoredBuild,
	versionName func(*registry.Version) string,
) (*StoredBuild, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	versionRow, err := q.GetVersionByFingerprint(ctx, postgresdb.GetVersionByFingerprintParams{
		Name:        bucketName,
		Fingerprint: fingerprint,
	})
	if err != nil {
		return nil, mapNotFound("get version for build", err)
	}
	version, err := r.restoreVersion(ctx, q, tenant, versionRow)
	if err != nil {
		return nil, err
	}
	if err := version.EnsureTemplateType(templateType); err != nil {
		return nil, err
	}

	labels, err := json.Marshal(build.Labels)
	if err != nil {
		return nil, fmt.Errorf("marshal build labels: %w", err)
	}
	row, err := q.CreateBuild(ctx, postgresdb.CreateBuildParams{
		OrganizationID:           tenant.OrganizationID,
		ProjectID:                tenant.ProjectID,
		ID:                       build.ID.String(),
		Name:                     bucketName,
		Fingerprint:              fingerprint,
		ComponentType:            build.ComponentType,
		Status:                   string(build.Status),
		Platform:                 build.Platform,
		MetadataSeen:             build.MetadataSeen,
		PackerRunUuid:            build.PackerRunUUID,
		Labels:                   labels,
		SourceExternalIdentifier: build.SourceExternalIdentifier,
		ParentVersionID:          sql.NullString{String: build.ParentVersionID, Valid: build.ParentVersionID != ""},
		ParentChannelID:          sql.NullString{String: build.ParentChannelID, Valid: build.ParentChannelID != ""},
		CreatedAt:                build.CreatedAt,
	})
	if err != nil {
		return nil, mapNotFound("create build", err)
	}
	if err := r.sealBuildRow(ctx, q, &row); err != nil {
		return nil, err
	}
	if err := r.inheritParentRevocation(
		ctx, q, tenant, version, row.ParentVersionID.String, versionName, build.CreatedAt,
	); err != nil {
		return nil, err
	}
	for _, artifact := range build.Artifacts {
		artifactRow, err := q.CreateArtifact(ctx, postgresdb.CreateArtifactParams{
			OrganizationID:     tenant.OrganizationID,
			ProjectID:          tenant.ProjectID,
			ID:                 artifact.ID.String(),
			BuildID:            row.ID,
			ExternalIdentifier: artifact.ExternalIdentifier,
			Region:             artifact.Region,
			CreatedAt:          artifact.CreatedAt,
		})
		if err != nil {
			return nil, fmt.Errorf("create build artifact: %w", err)
		}
		if err := r.sealArtifactRow(ctx, q, &artifactRow); err != nil {
			return nil, err
		}
	}
	restored, err := r.restoreBuild(ctx, q, tenant, row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create build: %w", err)
	}
	return restored, nil
}

func (r *Repository) ListBuilds(
	ctx context.Context,
	tenant Tenant,
	bucketName, fingerprint string,
) ([]StoredBuild, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := q.GetVersionByFingerprint(ctx, postgresdb.GetVersionByFingerprintParams{
		Name:        bucketName,
		Fingerprint: fingerprint,
	}); err != nil {
		return nil, mapNotFound("get version for builds", err)
	}
	builds, err := r.listBuilds(ctx, q, tenant, bucketName, fingerprint)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit list builds: %w", err)
	}
	return builds, nil
}

func (r *Repository) GetBuild(
	ctx context.Context,
	tenant Tenant,
	bucketName, fingerprint, buildID string,
) (*StoredBuild, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := q.GetBuild(ctx, postgresdb.GetBuildParams{
		Name:        bucketName,
		Fingerprint: fingerprint,
		ID:          buildID,
	})
	if err != nil {
		return nil, mapNotFound("get build", err)
	}
	build, err := r.restoreBuild(ctx, q, tenant, row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit get build: %w", err)
	}
	return build, nil
}

func (r *Repository) UpdateBuild(
	ctx context.Context,
	tenant Tenant,
	bucketName, fingerprint string,
	build StoredBuild,
	versionName func(*registry.Version) string,
	at time.Time,
) (*StoredBuild, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	labels, err := json.Marshal(build.Labels)
	if err != nil {
		return nil, fmt.Errorf("marshal build labels: %w", err)
	}
	metadata := build.Metadata
	if r.ring != nil && len(metadata) > 0 {
		sealed, err := r.ring.Encrypt(metadata, payloadAAD(tenant, "build", build.ID.String()))
		if err != nil {
			return nil, fmt.Errorf("seal build metadata: %w", err)
		}
		metadata = sealed
	}
	row, err := q.UpdateBuild(ctx, postgresdb.UpdateBuildParams{
		OrganizationID:           tenant.OrganizationID,
		ProjectID:                tenant.ProjectID,
		ID:                       build.ID.String(),
		Status:                   string(build.Status),
		Platform:                 build.Platform,
		MetadataSeen:             build.MetadataSeen,
		PackerRunUuid:            build.PackerRunUUID,
		Labels:                   labels,
		SourceExternalIdentifier: build.SourceExternalIdentifier,
		ParentVersionID:          sql.NullString{String: build.ParentVersionID, Valid: build.ParentVersionID != ""},
		ParentChannelID:          sql.NullString{String: build.ParentChannelID, Valid: build.ParentChannelID != ""},
		Metadata:                 metadata,
		UpdatedAt:                at,
	})
	if err != nil {
		return nil, mapNotFound("update build", err)
	}
	if err := r.sealBuildRow(ctx, q, &row); err != nil {
		return nil, err
	}
	for _, artifact := range build.Artifacts {
		artifactRow, err := q.CreateArtifact(ctx, postgresdb.CreateArtifactParams{
			OrganizationID:     tenant.OrganizationID,
			ProjectID:          tenant.ProjectID,
			ID:                 artifact.ID.String(),
			BuildID:            row.ID,
			ExternalIdentifier: artifact.ExternalIdentifier,
			Region:             artifact.Region,
			CreatedAt:          artifact.CreatedAt,
		})
		if err != nil {
			return nil, fmt.Errorf("update build artifact: %w", err)
		}
		if err := r.sealArtifactRow(ctx, q, &artifactRow); err != nil {
			return nil, err
		}
	}

	versionRow, err := q.GetVersionByFingerprint(ctx, postgresdb.GetVersionByFingerprintParams{
		Name:        bucketName,
		Fingerprint: fingerprint,
	})
	if err != nil {
		return nil, mapNotFound("get updated version", err)
	}
	version, err := r.restoreVersion(ctx, q, tenant, versionRow)
	if err != nil {
		return nil, err
	}
	if err := r.inheritParentRevocation(
		ctx, q, tenant, version, row.ParentVersionID.String, versionName, at,
	); err != nil {
		return nil, err
	}
	completedNow := false
	var assignedLatest, previousLatest *Channel
	if !version.Complete() && version.ReadyToComplete() {
		if _, err := q.LockBucketForVersionSequence(ctx, versionRow.BucketID); err != nil {
			return nil, fmt.Errorf("lock bucket for version sequence: %w", err)
		}
		sequence, err := q.NextVersionSequence(ctx, versionRow.BucketID)
		if err != nil {
			return nil, fmt.Errorf("allocate version sequence: %w", err)
		}
		if err := version.MarkComplete(int(sequence), at); err != nil {
			return nil, err
		}
		completedNow = true
		completed, err := q.CompleteVersion(ctx, postgresdb.CompleteVersionParams{
			ID: version.ID.String(),
			// Nullable because an incomplete version has no sequence; it is
			// allocated only here, at completion.
			Sequence:  sql.NullInt32{Int32: sequence, Valid: true},
			UpdatedAt: at,
		})
		if err != nil {
			return nil, fmt.Errorf("complete version: %w", err)
		}
		if r.ring != nil {
			if err := q.SetVersionIntegrityMAC(ctx, postgresdb.SetVersionIntegrityMACParams{
				ID: completed.ID, IntegrityMac: r.rowMAC(versionMACMessage(completed)),
			}); err != nil {
				return nil, fmt.Errorf("seal completed version row: %w", err)
			}
		}
		// Completion assigns the version to the bucket's managed "latest"
		// channel with no client call, in the same transaction: the live probe
		// observed the channel's version flip and its updated_at land on the
		// same sub-second instant as the completing UpdateBuild (Appendix A
		// probes 13-14). The channel exists for every bucket (CreateBucket
		// above, migration 000008), so a miss here is data corruption, not a
		// client condition.
		if revocation := version.Revocation(); revocation == nil || revocation.RevokeAt.After(at) {
			latest, err := r.getChannel(ctx, tx, q, tenant, bucketName, "latest")
			if err != nil {
				return nil, fmt.Errorf("managed latest channel for completion: %w", err)
			}
			if err := r.recordAssignment(ctx, tx, tenant, latest.ID, version.ID, "Dufflebag", at); err != nil {
				return nil, err
			}
			previous := *latest
			previousLatest = &previous
			assignedLatest, err = r.getChannel(ctx, tx, q, tenant, bucketName, "latest")
			if err != nil {
				return nil, fmt.Errorf("read assigned managed latest channel: %w", err)
			}
		}
	}
	if completedNow {
		if err := enqueueWebhookEvent(ctx, q, tenant, webhook.OperationVersionCompleted,
			webhook.Target{Type: "version", Bucket: bucketName, Fingerprint: fingerprint},
			versionWebhookPayload(version, at), at,
		); err != nil {
			return nil, err
		}
		if assignedLatest != nil {
			if err := enqueueWebhookEvent(ctx, q, tenant, webhook.OperationChannelAssigned,
				webhook.Target{Type: "channel", Bucket: bucketName, Name: "latest"},
				channelWebhookPayload(assignedLatest, previousLatest), at,
			); err != nil {
				return nil, err
			}
		}
	}

	restored, err := r.restoreBuild(ctx, q, tenant, row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit update build: %w", err)
	}
	return restored, nil
}

// inheritParentRevocation applies a stored build's explicit parent-version
// edge. Missing, foreign, and self-referential IDs are tolerated like ancestry
// reads, and an existing child revocation always wins.
func (r *Repository) inheritParentRevocation(
	ctx context.Context,
	q *postgresdb.Queries,
	tenant Tenant,
	child *registry.Version,
	parentVersionID string,
	versionName func(*registry.Version) string,
	at time.Time,
) error {
	if parentVersionID == "" || parentVersionID == child.ID.String() || child.Revocation() != nil {
		return nil
	}
	row, err := q.GetVersionByID(ctx, postgresdb.GetVersionByIDParams{
		OrganizationID: tenant.OrganizationID,
		ProjectID:      tenant.ProjectID,
		ID:             parentVersionID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get parent version for revocation inheritance: %w", err)
	}
	parent, err := r.restoreVersion(ctx, q, tenant, postgresdb.GetVersionByFingerprintRow(row))
	if err != nil {
		return err
	}
	parentRevocation := parent.Revocation()
	if parentRevocation == nil {
		return nil
	}
	ancestor := registry.RevokedAncestor{
		VersionID: parent.ID, BucketName: parent.BucketName, Fingerprint: parent.Fingerprint,
		VersionName: versionName(parent),
	}
	inherited := registry.Revocation{
		RevokeAt: parentRevocation.RevokeAt, Message: parentRevocation.Message,
		Author: parentRevocation.Author, InheritedFrom: &ancestor,
	}
	if err := child.Revoke(inherited, at); err != nil {
		if errors.Is(err, registry.ErrConflict) {
			return nil
		}
		return err
	}
	if err := r.revokeRow(ctx, q, child.ID.String(), inherited, at); err != nil {
		if errors.Is(err, registry.ErrConflict) {
			row, reloadErr := q.GetVersionByID(ctx, postgresdb.GetVersionByIDParams{
				OrganizationID: tenant.OrganizationID,
				ProjectID:      tenant.ProjectID,
				ID:             child.ID.String(),
			})
			if reloadErr != nil {
				return fmt.Errorf("reload concurrently revoked child version: %w", reloadErr)
			}
			persisted, reloadErr := r.restoreVersion(
				ctx, q, tenant, postgresdb.GetVersionByFingerprintRow(row),
			)
			if reloadErr != nil {
				return reloadErr
			}
			*child = *persisted
			return nil
		}
		return err
	}
	return nil
}

// RevocationRequest is a manual revocation as the compat plane received it.
type RevocationRequest struct {
	RevokeAt                time.Time
	Message                 string
	Author                  string
	SkipDescendants         bool
	DisableRollbackChannels bool
}

// RevokeVersion revokes a version and, unless skipped, marks every transitive
// descendant revoked as inherited from it — one transaction, so a reader can
// never see a revoked ancestor with unrevoked descendants.
//
// versionName renders a version's wire name. It is passed in because the v0/vN
// collapse is the compat plane's rule and the denormalized ancestor identity
// stores the wire name (ADR-0002: the domain never sees the sentinel).
func (r *Repository) RevokeVersion(
	ctx context.Context,
	tenant Tenant,
	bucketName, fingerprint string,
	req RevocationRequest,
	versionName func(*registry.Version) string,
	at time.Time,
) (*registry.Version, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := q.GetVersionByFingerprint(ctx, postgresdb.GetVersionByFingerprintParams{
		Name:        bucketName,
		Fingerprint: fingerprint,
	})
	if err != nil {
		return nil, mapNotFound("get version for revocation", err)
	}
	version, err := r.restoreVersion(ctx, q, tenant, row)
	if err != nil {
		return nil, err
	}
	// Truncated to the microseconds Postgres stores, so the MACed value
	// survives the round trip (the assignment-timestamp precedent).
	effectAt := req.RevokeAt.Truncate(time.Microsecond)
	if err := version.Revoke(registry.Revocation{
		RevokeAt: effectAt, Message: req.Message, Author: req.Author,
	}, at); err != nil {
		return nil, err
	}
	if err := r.revokeRow(ctx, q, version.ID.String(), registry.Revocation{
		RevokeAt: effectAt, Message: req.Message, Author: req.Author,
	}, at); err != nil {
		return nil, err
	}

	if !req.SkipDescendants {
		ancestor := registry.RevokedAncestor{
			VersionID:   version.ID,
			BucketName:  bucketName,
			Fingerprint: fingerprint,
			VersionName: versionName(version),
		}
		descendants, err := q.ListVersionDescendants(ctx, version.ID.String())
		if err != nil {
			return nil, fmt.Errorf("list version descendants: %w", err)
		}
		for _, d := range descendants {
			// An already-revoked descendant keeps its own record: a manual
			// revocation must not be overwritten by an inherited one.
			if d.RevokeAt.Valid {
				continue
			}
			descendant, err := r.restoreVersion(ctx, q, tenant, postgresdb.GetVersionByFingerprintRow(d))
			if err != nil {
				return nil, err
			}
			inherited := registry.Revocation{
				RevokeAt: effectAt, Message: req.Message, Author: req.Author,
				InheritedFrom: &ancestor,
			}
			if err := descendant.Revoke(inherited, at); err != nil {
				return nil, err
			}
			// A descendant revoked concurrently since the listing keeps its own
			// record — the same rule as the pre-check above, so a race cannot
			// abort the ancestor's revocation with a conflict naming a version
			// the caller never targeted.
			if err := r.revokeRow(ctx, q, descendant.ID.String(), inherited, at); err != nil {
				if errors.Is(err, registry.ErrConflict) {
					continue
				}
				return nil, err
			}
		}
	}

	if !req.DisableRollbackChannels {
		rows, err := tx.QueryContext(ctx, `
			SELECT channels.id, buckets.name, channels.name
			FROM channels
			JOIN buckets ON buckets.id = channels.bucket_id
			JOIN LATERAL (
				SELECT assignments.version_id
				FROM channel_assignments AS assignments
				WHERE assignments.channel_id = channels.id
				ORDER BY assignments.assigned_at DESC, assignments.id DESC
				LIMIT 1
			) AS current_assignment ON true
			JOIN versions ON versions.id = current_assignment.version_id
			WHERE versions.id = $1
			   OR versions.revocation_inherited_from_id = $1
		`, version.ID.String())
		if err != nil {
			return nil, fmt.Errorf("list channels requiring rollback: %w", err)
		}
		type rollbackChannel struct {
			id         registry.ID
			bucketName string
			name       string
		}
		channels := make([]rollbackChannel, 0)
		for rows.Next() {
			var channelID, rollbackBucket, channelName string
			if err := rows.Scan(&channelID, &rollbackBucket, &channelName); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan channel requiring rollback: %w", err)
			}
			parsedChannelID, err := registry.ParseID(channelID)
			if err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("parse rollback channel id: %w", err)
			}
			channels = append(channels, rollbackChannel{
				id: parsedChannelID, bucketName: rollbackBucket, name: channelName,
			})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("list channels requiring rollback: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close channels requiring rollback: %w", err)
		}
		for _, channel := range channels {
			previous, err := r.getChannel(ctx, tx, q, tenant, channel.bucketName, channel.name)
			if err != nil {
				return nil, err
			}
			rollbackID, found, err := r.newestValidAssignedVersion(ctx, tx, channel.id)
			if err != nil {
				return nil, err
			}
			if !found {
				continue
			}
			if err := r.recordAssignment(
				ctx, tx, tenant, channel.id, rollbackID, "Dufflebag", at,
			); err != nil {
				return nil, fmt.Errorf("rollback channel assignment: %w", err)
			}
			updated, err := r.getChannel(ctx, tx, q, tenant, channel.bucketName, channel.name)
			if err != nil {
				return nil, err
			}
			if err := enqueueWebhookEvent(ctx, q, tenant, webhook.OperationChannelAssigned,
				webhook.Target{Type: "channel", Bucket: channel.bucketName, Name: channel.name},
				channelWebhookPayload(updated, previous), at,
			); err != nil {
				return nil, err
			}
		}
		// A channel whose entire history is revoked has no honest rollback
		// target. Its current assignment is left in place rather than inventing
		// an unassignment state that has not been observed from HCP.
	}
	operation := webhook.OperationVersionRevoked
	if effectAt.After(at) {
		operation = webhook.OperationVersionRevocationScheduled
	}
	if err := enqueueWebhookEvent(ctx, q, tenant, operation,
		webhook.Target{Type: "version", Bucket: bucketName, Fingerprint: fingerprint},
		versionWebhookPayload(version, at), at,
	); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit revoke version: %w", err)
	}
	return version, nil
}

// newestValidAssignedVersion is the single rollback selector for revocation
// and managed-latest deletion. It walks assignment history newest-first and
// skips versions that no longer exist or are revoked.
func (r *Repository) newestValidAssignedVersion(
	ctx context.Context,
	tx *sql.Tx,
	channelID registry.ID,
) (registry.ID, bool, error) {
	var versionID string
	err := tx.QueryRowContext(ctx, `
		SELECT assignments.version_id
		FROM channel_assignments AS assignments
		JOIN versions ON versions.id = assignments.version_id
		WHERE assignments.channel_id = $1
		  AND versions.revoke_at IS NULL
		ORDER BY assignments.assigned_at DESC, assignments.id DESC
		LIMIT 1
	`, channelID.String()).Scan(&versionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("select newest valid channel assignment: %w", err)
	}
	parsed, err := registry.ParseID(versionID)
	if err != nil {
		return "", false, fmt.Errorf("parse rollback version id: %w", err)
	}
	return parsed, true, nil
}

// RestoreRevokedVersion clears a version's revocation and every inherited
// descendant revocation that names it, in one transaction.
func (r *Repository) RestoreRevokedVersion(
	ctx context.Context,
	tenant Tenant,
	bucketName, fingerprint string,
	at time.Time,
) (*registry.Version, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := q.GetVersionByFingerprint(ctx, postgresdb.GetVersionByFingerprintParams{
		Name:        bucketName,
		Fingerprint: fingerprint,
	})
	if err != nil {
		return nil, mapNotFound("get version for restore", err)
	}
	version, err := r.restoreVersion(ctx, q, tenant, row)
	if err != nil {
		return nil, err
	}
	if err := version.Restore(at); err != nil {
		return nil, err
	}
	if err := r.unrevokeRow(ctx, q, version.ID.String(), "", at); err != nil {
		return nil, err
	}

	descendants, err := q.ListVersionDescendants(ctx, version.ID.String())
	if err != nil {
		return nil, fmt.Errorf("list version descendants: %w", err)
	}
	for _, descendant := range descendants {
		if err := r.unrevokeRow(ctx, q, descendant.ID, version.ID.String(), at); err != nil {
			return nil, err
		}
	}
	if err := enqueueWebhookEvent(ctx, q, tenant, webhook.OperationVersionRestored,
		webhook.Target{Type: "version", Bucket: bucketName, Fingerprint: fingerprint},
		versionWebhookPayload(version, at), at,
	); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit restore version: %w", err)
	}
	return version, nil
}

// unrevokeRow clears one revocation and re-seals its integrity MAC. An empty
// inheritedFrom restores the directly requested version; a non-empty value
// clears only a descendant whose inherited ancestor still matches.
func (r *Repository) unrevokeRow(
	ctx context.Context,
	q *postgresdb.Queries,
	id, inheritedFrom string,
	at time.Time,
) error {
	var restored postgresdb.Version
	var err error
	if inheritedFrom == "" {
		restored, err = q.UnrevokeVersion(ctx, postgresdb.UnrevokeVersionParams{
			ID: id, UpdatedAt: at,
		})
	} else {
		restored, err = q.UnrevokeInheritedVersion(ctx, postgresdb.UnrevokeInheritedVersionParams{
			ID: id, RestoredID: inheritedFrom, UpdatedAt: at,
		})
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if inheritedFrom != "" {
				return nil
			}
			return fmt.Errorf("%w: version %s is not revoked", registry.ErrConflict, id)
		}
		return fmt.Errorf("restore version %s: %w", id, err)
	}
	if r.ring != nil {
		if err := q.SetVersionIntegrityMAC(ctx, postgresdb.SetVersionIntegrityMACParams{
			ID: restored.ID, IntegrityMac: r.rowMAC(versionMACMessage(restored)),
		}); err != nil {
			return fmt.Errorf("seal restored version row: %w", err)
		}
	}
	return nil
}

// revokeRow persists one version's revocation and re-seals its integrity MAC.
func (r *Repository) revokeRow(
	ctx context.Context,
	q *postgresdb.Queries,
	id string,
	rev registry.Revocation,
	at time.Time,
) error {
	params := postgresdb.RevokeVersionParams{
		ID:                id,
		RevokeAt:          sql.NullTime{Time: rev.RevokeAt, Valid: true},
		RevocationMessage: sql.NullString{String: rev.Message, Valid: rev.Message != ""},
		RevocationAuthor:  sql.NullString{String: rev.Author, Valid: true},
		UpdatedAt:         at,
	}
	if a := rev.InheritedFrom; a != nil {
		params.RevocationInheritedFromID = sql.NullString{String: a.VersionID.String(), Valid: true}
		params.RevocationInheritedFromBucket = sql.NullString{String: a.BucketName, Valid: true}
		params.RevocationInheritedFromFingerprint = sql.NullString{String: a.Fingerprint, Valid: true}
		params.RevocationInheritedFromName = sql.NullString{String: a.VersionName, Valid: true}
	}
	revoked, err := q.RevokeVersion(ctx, params)
	if err != nil {
		// The guard in the statement (revoke_at IS NULL) makes a concurrent
		// revocation surface here rather than silently overwrite.
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: version %s is already revoked", registry.ErrConflict, id)
		}
		return fmt.Errorf("revoke version %s: %w", id, err)
	}
	if r.ring != nil {
		if err := q.SetVersionIntegrityMAC(ctx, postgresdb.SetVersionIntegrityMACParams{
			ID: revoked.ID, IntegrityMac: r.rowMAC(versionMACMessage(revoked)),
		}); err != nil {
			return fmt.Errorf("seal revoked version row: %w", err)
		}
	}
	return nil
}

// UploadSbom stores an SBOM under a build, replacing one of the same name.
//
// A replace rather than a conflict, because UploadSbom is a PUT and a re-run
// build re-uploads under the same name — and any error here fails the whole
// build before it can be marked complete (dossier §5.6).
func (r *Repository) UploadSbom(
	ctx context.Context,
	tenant Tenant,
	bucketName, fingerprint, buildID string,
	sbom Sbom,
) (*Sbom, error) {
	packages, parseErr := parseSbom(sbom.CompressedData, sbom.Format)
	objects, err := r.objectStore()
	if err != nil {
		return nil, err
	}
	objectKey := objectstore.Key(
		tenant.OrganizationID.String(), tenant.ProjectID.String(), buildID, sbom.Name, sbom.CompressedData,
	)
	// The object lands first. A later database failure can leave an invisible
	// orphan; reversing the order can leave a visible row whose bytes do not exist.
	//
	// On encrypted deployments the bytes are sealed HERE, before PutObject: a
	// dump of the bucket is not a disclosure regardless of the bucket's own
	// configuration (ADR-0024). The key is derived from the plaintext, so the
	// AAD binds the ciphertext to the row that will name it.
	payload := sbom.CompressedData
	if r.ring != nil {
		sealed, err := r.ring.Encrypt(payload, payloadAAD(tenant, "sbom", objectKey))
		if err != nil {
			return nil, fmt.Errorf("seal sbom: %w", err)
		}
		payload = sealed
	}
	if err := objects.Put(ctx, objectKey, payload); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrObjectStorageUnavailable, err)
	}
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row, err := q.UpsertSbom(ctx, postgresdb.UpsertSbomParams{
		OrganizationID: tenant.OrganizationID,
		ProjectID:      tenant.ProjectID,
		ID:             sbom.ID.String(),
		Name:           bucketName,
		Fingerprint:    fingerprint,
		ID_2:           buildID,
		Name_2:         sbom.Name,
		Format:         sbom.Format,
		ObjectKey:      objectKey,
		CreatedAt:      sbom.CreatedAt,
	})
	// The insert selects from builds, so a missing bucket, version or build
	// yields no row rather than a violation.
	if err != nil {
		return nil, mapNotFound("upload sbom", err)
	}
	if err := replaceSbomProjection(ctx, tx, tenant, row.ID, packages, parseErr); err != nil {
		return nil, err
	}
	row.ParseStatus = "parsed"
	row.ParseError = ""
	if parseErr != nil {
		row.ParseStatus = "unparseable"
		row.ParseError = parseErr.Error()
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit upload sbom: %w", err)
	}
	restored, err := restoreSbom(storedSbom{
		id: row.ID, buildID: row.BuildID, name: row.Name, format: row.Format,
		parseStatus: row.ParseStatus, parseError: row.ParseError, createdAt: row.CreatedAt,
	})
	if err != nil {
		return nil, err
	}
	// The upload response may carry the caller's in-hand bytes without reading
	// the abandoned Postgres column or making a redundant object-store request.
	restored.CompressedData = sbom.CompressedData
	return restored, nil
}

type storedSbom struct {
	id, buildID, name, format, objectKey, parseStatus, parseError string
	createdAt                                                     time.Time
}

func restoreSbom(row storedSbom) (*Sbom, error) {
	id, err := registry.ParseID(row.id)
	if err != nil {
		return nil, fmt.Errorf("restore sbom id: %w", err)
	}
	buildID, err := registry.ParseID(row.buildID)
	if err != nil {
		return nil, fmt.Errorf("restore sbom build id: %w", err)
	}
	return &Sbom{
		ID:          id,
		BuildID:     buildID,
		Name:        row.name,
		Format:      row.format,
		ParseStatus: row.parseStatus,
		ParseError:  row.parseError,
		CreatedAt:   row.createdAt,
		objectKey:   row.objectKey,
	}, nil
}

func (r *Repository) begin(
	ctx context.Context,
	tenant Tenant,
) (*sql.Tx, *postgresdb.Queries, error) {
	// Every repository operation funnels through here, so this is the one place
	// a malformed tenant needs catching. It answers not-found rather than an
	// error, so a misconfigured HCP_ORGANIZATION_ID yields an empty registry
	// instead of a cast failure (ADR-0016).
	if tenant.malformed || tenant.denied {
		return nil, nil, fmt.Errorf("%w: tenant", registry.ErrNotFound)
	}
	tx, err := BeginTenant(ctx, r.db, tenant.OrganizationID.String(), tenant.ProjectID.String())
	if err != nil {
		return nil, nil, err
	}
	return tx, postgresdb.New(tx), nil
}

func (r *Repository) restoreVersion(
	ctx context.Context,
	q *postgresdb.Queries,
	tenant Tenant,
	row postgresdb.GetVersionByFingerprintRow,
) (*registry.Version, error) {
	if err := r.verifyRowMAC("version "+row.ID, row.IntegrityMac, versionMACMessage(postgresdb.Version{
		OrganizationID: row.OrganizationID, ProjectID: row.ProjectID, ID: row.ID,
		BucketID: row.BucketID, Fingerprint: row.Fingerprint, TemplateType: row.TemplateType,
		Complete: row.Complete, Sequence: row.Sequence, AuthorID: row.AuthorID,
		RevokeAt: row.RevokeAt, RevocationAuthor: row.RevocationAuthor,
		RevocationInheritedFromID: row.RevocationInheritedFromID,
	})); err != nil {
		return nil, err
	}
	id, err := registry.ParseID(row.ID)
	if err != nil {
		return nil, fmt.Errorf("restore version id: %w", err)
	}
	builds, err := r.listBuilds(ctx, q, tenant, row.BucketName, row.Fingerprint)
	if err != nil {
		return nil, err
	}
	domainBuilds := make([]registry.Build, len(builds))
	for i := range builds {
		domainBuilds[i] = builds[i].Build
	}
	relationships, err := q.GetVersionRelationships(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("get version relationships: %w", err)
	}
	var parents *registry.VersionParents
	if relationships.ParentsStatus != "" {
		status := registry.AncestryStatus(relationships.ParentsStatus)
		switch status {
		case registry.AncestryUndetermined, registry.AncestryUpToDate, registry.AncestryOutOfDate:
			parents = &registry.VersionParents{Status: status}
		default:
			return nil, fmt.Errorf("restore version parents status %q: %w", status, registry.ErrInvalid)
		}
	}
	sequence := 0
	if row.Sequence.Valid {
		sequence = int(row.Sequence.Int32)
	}
	revocation, err := rowRevocation(
		row.RevokeAt, row.RevocationMessage, row.RevocationAuthor,
		row.RevocationInheritedFromID, row.RevocationInheritedFromBucket,
		row.RevocationInheritedFromFingerprint, row.RevocationInheritedFromName,
	)
	if err != nil {
		return nil, err
	}
	version, err := registry.RestoreVersion(registry.Version{
		ID:             id,
		BucketName:     row.BucketName,
		Fingerprint:    row.Fingerprint,
		TemplateType:   registry.TemplateType(row.TemplateType),
		AuthorID:       row.AuthorID,
		HasDescendants: relationships.HasDescendants,
		Parents:        parents,
		Builds:         domainBuilds,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, row.Complete, sequence, revocation)
	if err != nil {
		return nil, fmt.Errorf("restore version: %w", err)
	}
	return version, nil
}

// rowRevocation rebuilds a version's revocation state from its columns.
func rowRevocation(
	revokeAt sql.NullTime,
	message, author, ancestorID, ancestorBucket, ancestorFingerprint, ancestorName sql.NullString,
) (*registry.Revocation, error) {
	if !revokeAt.Valid {
		return nil, nil
	}
	revocation := &registry.Revocation{
		RevokeAt: revokeAt.Time,
		Message:  message.String,
		Author:   author.String,
	}
	if ancestorID.Valid {
		id, err := registry.ParseID(ancestorID.String)
		if err != nil {
			return nil, fmt.Errorf("restore revocation ancestor id: %w", err)
		}
		revocation.InheritedFrom = &registry.RevokedAncestor{
			VersionID:   id,
			BucketName:  ancestorBucket.String,
			Fingerprint: ancestorFingerprint.String,
			VersionName: ancestorName.String,
		}
	}
	return revocation, nil
}

func restoreBucket(row postgresdb.Bucket) (*Bucket, error) {
	id, err := registry.ParseID(row.ID)
	if err != nil {
		return nil, fmt.Errorf("restore bucket id: %w", err)
	}
	var labels map[string]string
	if err := json.Unmarshal(row.Labels, &labels); err != nil {
		return nil, fmt.Errorf("restore bucket labels: %w", err)
	}
	return &Bucket{
		ID:          id,
		Name:        row.Name,
		Description: row.Description,
		Labels:      labels,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}, nil
}

func (r *Repository) listBuilds(
	ctx context.Context,
	q *postgresdb.Queries,
	tenant Tenant,
	bucketName, fingerprint string,
) ([]StoredBuild, error) {
	rows, err := q.ListBuildsByVersion(ctx, postgresdb.ListBuildsByVersionParams{
		Name:        bucketName,
		Fingerprint: fingerprint,
	})
	if err != nil {
		return nil, fmt.Errorf("list version builds: %w", err)
	}
	builds := make([]StoredBuild, 0, len(rows))
	for _, row := range rows {
		build, err := r.restoreBuild(ctx, q, tenant, row)
		if err != nil {
			return nil, err
		}
		builds = append(builds, *build)
	}
	return builds, nil
}

func (r *Repository) restoreBuild(
	ctx context.Context,
	q *postgresdb.Queries,
	tenant Tenant,
	row postgresdb.Build,
) (*StoredBuild, error) {
	if err := r.verifyRowMAC("build "+row.ID, row.IntegrityMac, buildMACMessage(row)); err != nil {
		return nil, err
	}
	artifactRows, err := q.ListArtifactsByBuild(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("list build artifacts: %w", err)
	}
	artifacts := make([]Artifact, 0, len(artifactRows))
	for _, artifactRow := range artifactRows {
		if err := r.verifyRowMAC("artifact "+artifactRow.ID, artifactRow.IntegrityMac, artifactMACMessage(artifactRow)); err != nil {
			return nil, err
		}
		artifact, err := restoreArtifact(artifactRow)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	return r.restoreBuildWithArtifacts(tenant, row, artifacts)
}

// sealBuildRow writes the row MAC computed from the authoritative stored
// values and reflects it into the in-hand row.
func (r *Repository) sealBuildRow(ctx context.Context, q *postgresdb.Queries, row *postgresdb.Build) error {
	if r.ring == nil {
		return nil
	}
	mac := r.rowMAC(buildMACMessage(*row))
	if err := q.SetBuildIntegrityMAC(ctx, postgresdb.SetBuildIntegrityMACParams{ID: row.ID, IntegrityMac: mac}); err != nil {
		return fmt.Errorf("seal build row: %w", err)
	}
	row.IntegrityMac = mac
	return nil
}

func (r *Repository) sealArtifactRow(ctx context.Context, q *postgresdb.Queries, row *postgresdb.Artifact) error {
	if r.ring == nil {
		return nil
	}
	mac := r.rowMAC(artifactMACMessage(*row))
	if err := q.SetArtifactIntegrityMAC(ctx, postgresdb.SetArtifactIntegrityMACParams{ID: row.ID, IntegrityMac: mac}); err != nil {
		return fmt.Errorf("seal artifact row: %w", err)
	}
	row.IntegrityMac = mac
	return nil
}

func (r *Repository) restoreBuildWithArtifacts(tenant Tenant, row postgresdb.Build, artifacts []Artifact) (*StoredBuild, error) {
	id, err := registry.ParseID(row.ID)
	if err != nil {
		return nil, fmt.Errorf("restore build id: %w", err)
	}
	versionID, err := registry.ParseID(row.VersionID)
	if err != nil {
		return nil, fmt.Errorf("restore build version id: %w", err)
	}
	var labels map[string]string
	if err := json.Unmarshal(row.Labels, &labels); err != nil {
		return nil, fmt.Errorf("restore build labels: %w", err)
	}
	// Sealed on encrypted deployments; the plain '{}' column default is the
	// one legitimate unsealed value. Alteration of stored bytes is the row
	// MAC's job, not decryption's.
	metadata := append(json.RawMessage(nil), row.Metadata...)
	if r.ring != nil && keyring.Sealed(metadata) {
		opened, err := r.ring.Decrypt(metadata, payloadAAD(tenant, "build", row.ID))
		if err != nil {
			return nil, fmt.Errorf("restore build metadata: %w", err)
		}
		metadata = opened
	}
	return &StoredBuild{
		Build: registry.Build{
			ID:            id,
			ComponentType: row.ComponentType,
			Status:        registry.BuildStatus(row.Status),
			Platform:      row.Platform,
			MetadataSeen:  row.MetadataSeen,
		},
		VersionID:                versionID,
		PackerRunUUID:            row.PackerRunUuid,
		Labels:                   labels,
		Metadata:                 metadata,
		SourceExternalIdentifier: row.SourceExternalIdentifier,
		ParentVersionID:          row.ParentVersionID.String,
		ParentChannelID:          row.ParentChannelID.String,
		Artifacts:                artifacts,
		CreatedAt:                row.CreatedAt,
		UpdatedAt:                row.UpdatedAt,
	}, nil
}

func restoreArtifact(row postgresdb.Artifact) (Artifact, error) {
	id, err := registry.ParseID(row.ID)
	if err != nil {
		return Artifact{}, fmt.Errorf("restore artifact id: %w", err)
	}
	buildID, err := registry.ParseID(row.BuildID)
	if err != nil {
		return Artifact{}, fmt.Errorf("restore artifact build id: %w", err)
	}
	return Artifact{
		ID:                 id,
		BuildID:            buildID,
		ExternalIdentifier: row.ExternalIdentifier,
		Region:             row.Region,
		CreatedAt:          row.CreatedAt,
	}, nil
}

func mapNotFound(action string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", action, registry.ErrNotFound)
	}
	return fmt.Errorf("%s: %w", action, err)
}
