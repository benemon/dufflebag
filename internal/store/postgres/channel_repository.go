package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/benemon/dufflebag/internal/webhook"
	"github.com/google/uuid"
)

var (
	// ErrChannelExists lets the compatibility adapter distinguish the probed
	// AlreadyExists response from other channel conflicts.
	ErrChannelExists = errors.New("channel already exists")
	// ErrManagedChannel lets the adapter preserve the assign endpoint's probed
	// refusal shape even when the repository guard is the layer that fires.
	ErrManagedChannel = errors.New("cannot assign to managed channel")
)

// Channel is a named pointer to the latest version assigned to it.
type Channel struct {
	ID         registry.ID
	BucketName string
	Name       string
	Restricted bool
	// Managed marks a channel the service itself maintains — the per-bucket
	// "latest" that CreateBucket brings into existence and version completion
	// assigns to (dossier §7, Appendix A probes 04-06, 13-14). The compat
	// handler refuses client mutation of managed channels.
	Managed bool
	Version *registry.Version
	// AssignmentAuthorID comes from the current immutable assignment row.
	AssignmentAuthorID string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ChannelAssignment is one immutable channel assignment.
type ChannelAssignment struct {
	ID         registry.ID
	ChannelID  registry.ID
	Version    *registry.Version
	AuthorID   string
	AssignedAt time.Time
}

func (r *Repository) ListBuckets(ctx context.Context, tenant Tenant) ([]Bucket, error) {
	return r.listBuckets(ctx, tenant, "")
}

func (r *Repository) GetBucketWithLatestVersion(
	ctx context.Context,
	tenant Tenant,
	name string,
) (*Bucket, error) {
	buckets, err := r.listBuckets(ctx, tenant, name)
	if err != nil {
		return nil, err
	}
	if len(buckets) == 0 {
		return nil, fmt.Errorf("get bucket with latest version: %w", registry.ErrNotFound)
	}
	return &buckets[0], nil
}

func (r *Repository) listBuckets(ctx context.Context, tenant Tenant, name string) ([]Bucket, error) {
	tx, _, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT buckets.organization_id, buckets.project_id, buckets.id, buckets.name,
		       buckets.description, buckets.labels, buckets.created_at, buckets.updated_at,
		       latest.id, latest.fingerprint, latest.template_type, latest.complete,
		       latest.sequence, latest.created_at, latest.updated_at, latest.author_id,
		       latest.revoke_at, latest.revocation_message, latest.revocation_author,
		       latest.revocation_inherited_from_id, latest.revocation_inherited_from_bucket,
		       latest.revocation_inherited_from_fingerprint, latest.revocation_inherited_from_name,
		       relationships.has_descendants, relationships.parents_status,
		       relationships.children_status,
		       builds.id, builds.version_id, builds.component_type, builds.status,
		       builds.platform, builds.metadata_seen, builds.packer_run_uuid, builds.labels,
		       builds.source_external_identifier, builds.parent_version_id,
		       builds.parent_channel_id, builds.metadata, builds.created_at, builds.updated_at,
		       artifacts.id, artifacts.build_id, artifacts.external_identifier,
		       artifacts.region, artifacts.created_at
		FROM buckets
		LEFT JOIN LATERAL (
			SELECT versions.*
			FROM versions
			WHERE versions.bucket_id = buckets.id
			  AND versions.complete
			  AND versions.sequence IS NOT NULL
			ORDER BY versions.sequence DESC
			LIMIT 1
		) AS latest ON true
		LEFT JOIN LATERAL (
			WITH parents AS (
				SELECT builds.parent_version_id,
				       parent_versions.id AS existing_parent_id,
				       current_assignment.version_id AS channel_version_id
				FROM builds
				LEFT JOIN versions AS parent_versions ON parent_versions.id = builds.parent_version_id
				LEFT JOIN channels AS parent_channels ON parent_channels.id = builds.parent_channel_id
				LEFT JOIN LATERAL (
					SELECT assignments.version_id
					FROM channel_assignments AS assignments
					WHERE assignments.channel_id = parent_channels.id
					ORDER BY assignments.assigned_at DESC, assignments.id DESC
					LIMIT 1
				) AS current_assignment ON true
				WHERE builds.version_id = latest.id
				  AND builds.parent_version_id IS NOT NULL
			), children AS (
				SELECT builds.parent_version_id,
				       current_assignment.version_id AS channel_version_id
				FROM builds
				LEFT JOIN channels AS parent_channels ON parent_channels.id = builds.parent_channel_id
				LEFT JOIN LATERAL (
					SELECT assignments.version_id
					FROM channel_assignments AS assignments
					WHERE assignments.channel_id = parent_channels.id
					ORDER BY assignments.assigned_at DESC, assignments.id DESC
					LIMIT 1
				) AS current_assignment ON true
				WHERE builds.parent_version_id = latest.id
			)
			SELECT EXISTS (
				       SELECT 1 FROM children
			       ) AS has_descendants,
			       CASE
				       WHEN NOT EXISTS (SELECT 1 FROM parents) THEN ''
				       WHEN EXISTS (
					       SELECT 1 FROM parents
					       WHERE channel_version_id IS NOT NULL
					         AND channel_version_id <> parent_version_id
				       ) THEN 'out_of_date'
				       WHEN EXISTS (
					       SELECT 1 FROM parents
					       WHERE existing_parent_id IS NULL OR channel_version_id IS NULL
				       ) THEN 'undetermined'
				       ELSE 'up_to_date'
			       END AS parents_status,
			       CASE
				       WHEN NOT EXISTS (SELECT 1 FROM children) THEN ''
				       WHEN EXISTS (
					       SELECT 1 FROM children
					       WHERE channel_version_id IS NOT NULL
					         AND channel_version_id <> parent_version_id
				       ) THEN 'out_of_date'
				       WHEN EXISTS (
					       SELECT 1 FROM children
					       WHERE channel_version_id IS NULL
				       ) THEN 'undetermined'
				       ELSE 'up_to_date'
			       END AS children_status
		) AS relationships ON latest.id IS NOT NULL
		LEFT JOIN builds ON builds.version_id = latest.id
		LEFT JOIN artifacts ON artifacts.build_id = builds.id
		WHERE ($1 = '' OR buckets.name = $1)
		ORDER BY buckets.name, builds.id DESC, artifacts.id DESC
	`, name)
	if err != nil {
		return nil, fmt.Errorf("list buckets: %w", err)
	}
	defer func() { _ = rows.Close() }()

	buckets := make([]Bucket, 0)
	platforms := make(map[string]map[string]struct{})
	for rows.Next() {
		var (
			row postgresdb.Bucket

			latestID, latestFingerprint, latestTemplateType, latestAuthorID sql.NullString
			latestComplete                                                  sql.NullBool
			latestSequence                                                  sql.NullInt32
			latestCreatedAt, latestUpdatedAt                                sql.NullTime
			latestRevokeAt                                                  sql.NullTime
			latestRevocationMessage, latestRevocationAuthor                 sql.NullString
			latestAncestorID, latestAncestorBucket                          sql.NullString
			latestAncestorFingerprint, latestAncestorName                   sql.NullString
			hasDescendants                                                  sql.NullBool
			parentsStatus, childrenStatus                                   sql.NullString

			buildID, buildVersionID, componentType, buildStatus sql.NullString
			platform, packerRunUUID, sourceExternalIdentifier   sql.NullString
			parentVersionID, parentChannelID                    sql.NullString
			metadataSeen                                        sql.NullBool
			buildLabels, metadata                               []byte
			buildCreatedAt, buildUpdatedAt                      sql.NullTime

			artifactID, artifactBuildID, externalIdentifier, region sql.NullString
			artifactCreatedAt                                       sql.NullTime
		)
		if err := rows.Scan(
			&row.OrganizationID,
			&row.ProjectID,
			&row.ID,
			&row.Name,
			&row.Description,
			&row.Labels,
			&row.CreatedAt,
			&row.UpdatedAt,
			&latestID,
			&latestFingerprint,
			&latestTemplateType,
			&latestComplete,
			&latestSequence,
			&latestCreatedAt,
			&latestUpdatedAt,
			&latestAuthorID,
			&latestRevokeAt,
			&latestRevocationMessage,
			&latestRevocationAuthor,
			&latestAncestorID,
			&latestAncestorBucket,
			&latestAncestorFingerprint,
			&latestAncestorName,
			&hasDescendants,
			&parentsStatus,
			&childrenStatus,
			&buildID,
			&buildVersionID,
			&componentType,
			&buildStatus,
			&platform,
			&metadataSeen,
			&packerRunUUID,
			&buildLabels,
			&sourceExternalIdentifier,
			&parentVersionID,
			&parentChannelID,
			&metadata,
			&buildCreatedAt,
			&buildUpdatedAt,
			&artifactID,
			&artifactBuildID,
			&externalIdentifier,
			&region,
			&artifactCreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan bucket: %w", err)
		}

		if len(buckets) == 0 || buckets[len(buckets)-1].ID.String() != row.ID {
			bucket, err := restoreBucket(row)
			if err != nil {
				return nil, err
			}
			if latestID.Valid {
				id, err := registry.ParseID(latestID.String)
				if err != nil {
					return nil, fmt.Errorf("restore latest bucket version id: %w", err)
				}
				var parents *registry.VersionParents
				if parentsStatus.String != "" {
					status := registry.AncestryStatus(parentsStatus.String)
					switch status {
					case registry.AncestryUndetermined, registry.AncestryUpToDate, registry.AncestryOutOfDate:
						parents = &registry.VersionParents{Status: status}
					default:
						return nil, fmt.Errorf(
							"restore latest bucket version parents status %q: %w",
							status, registry.ErrInvalid,
						)
					}
				}
				revocation, err := rowRevocation(
					latestRevokeAt, latestRevocationMessage, latestRevocationAuthor,
					latestAncestorID, latestAncestorBucket, latestAncestorFingerprint,
					latestAncestorName,
				)
				if err != nil {
					return nil, err
				}
				bucket.LatestVersion, err = registry.RestoreVersion(registry.Version{
					ID:             id,
					BucketName:     bucket.Name,
					Fingerprint:    latestFingerprint.String,
					TemplateType:   registry.TemplateType(latestTemplateType.String),
					AuthorID:       latestAuthorID.String,
					HasDescendants: hasDescendants.Bool,
					Parents:        parents,
					CreatedAt:      latestCreatedAt.Time,
					UpdatedAt:      latestUpdatedAt.Time,
				}, latestComplete.Bool, int(latestSequence.Int32), revocation)
				if err != nil {
					return nil, fmt.Errorf("restore latest bucket version: %w", err)
				}
				if childrenStatus.String != "" {
					status := registry.AncestryStatus(childrenStatus.String)
					switch status {
					case registry.AncestryUndetermined, registry.AncestryUpToDate, registry.AncestryOutOfDate:
						bucket.ChildrenStatus = &status
					default:
						return nil, fmt.Errorf(
							"restore latest bucket version children status %q: %w",
							status, registry.ErrInvalid,
						)
					}
				}
			}
			buckets = append(buckets, *bucket)
			platforms[row.ID] = make(map[string]struct{})
		}

		bucket := &buckets[len(buckets)-1]
		if buildID.Valid && (len(bucket.LatestVersionBuilds) == 0 ||
			bucket.LatestVersionBuilds[len(bucket.LatestVersionBuilds)-1].ID.String() != buildID.String) {
			build, err := r.restoreBuildWithArtifacts(tenant, postgresdb.Build{
				ID:                       buildID.String,
				VersionID:                buildVersionID.String,
				ComponentType:            componentType.String,
				Status:                   buildStatus.String,
				Platform:                 platform.String,
				MetadataSeen:             metadataSeen.Bool,
				PackerRunUuid:            packerRunUUID.String,
				Labels:                   buildLabels,
				SourceExternalIdentifier: sourceExternalIdentifier.String,
				ParentVersionID:          parentVersionID,
				ParentChannelID:          parentChannelID,
				Metadata:                 metadata,
				CreatedAt:                buildCreatedAt.Time,
				UpdatedAt:                buildUpdatedAt.Time,
			}, nil)
			if err != nil {
				return nil, err
			}
			bucket.LatestVersionBuilds = append(bucket.LatestVersionBuilds, *build)
			bucket.LatestVersion.Builds = append(bucket.LatestVersion.Builds, build.Build)
			if build.Platform != "" {
				platforms[row.ID][build.Platform] = struct{}{}
			}
		}
		if artifactID.Valid {
			artifact, err := restoreArtifact(postgresdb.Artifact{
				ID:                 artifactID.String,
				BuildID:            artifactBuildID.String,
				ExternalIdentifier: externalIdentifier.String,
				Region:             region.String,
				CreatedAt:          artifactCreatedAt.Time,
			})
			if err != nil {
				return nil, err
			}
			last := len(bucket.LatestVersionBuilds) - 1
			bucket.LatestVersionBuilds[last].Artifacts = append(
				bucket.LatestVersionBuilds[last].Artifacts, artifact,
			)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list buckets: %w", err)
	}
	for i := range buckets {
		for platform := range platforms[buckets[i].ID.String()] {
			buckets[i].Platforms = append(buckets[i].Platforms, platform)
		}
		sort.Strings(buckets[i].Platforms)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit list buckets: %w", err)
	}
	return buckets, nil
}

func (r *Repository) CreateChannel(
	ctx context.Context,
	tenant Tenant,
	channel Channel,
	versionFingerprint, authorID string,
) (*Channel, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	bucket, err := q.GetBucketByName(ctx, channel.BucketName)
	if err != nil {
		return nil, mapNotFound("get bucket for channel", err)
	}
	inserted, err := tx.ExecContext(ctx, `
		INSERT INTO channels (
			organization_id, project_id, id, bucket_id, name, restricted, managed, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
		ON CONFLICT DO NOTHING
	`, tenant.OrganizationID, tenant.ProjectID, channel.ID.String(), bucket.ID,
		channel.Name, channel.Restricted, channel.Managed, channel.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create channel: %w", err)
	}
	if rows, err := inserted.RowsAffected(); err != nil {
		return nil, fmt.Errorf("create channel: %w", err)
	} else if rows == 0 {
		return nil, fmt.Errorf("%w: %s", ErrChannelExists, channel.Name)
	}
	if versionFingerprint != "" {
		if err := r.assignChannel(
			ctx, tx, q, tenant, channel.ID, channel.BucketName, versionFingerprint,
			authorID, channel.CreatedAt,
		); err != nil {
			return nil, err
		}
	}
	created, err := r.getChannel(ctx, tx, q, tenant, channel.BucketName, channel.Name)
	if err != nil {
		return nil, err
	}
	if err := enqueueWebhookEvent(ctx, q, tenant, webhook.OperationChannelCreated,
		webhook.Target{Type: "channel", Bucket: channel.BucketName, Name: channel.Name},
		channelWebhookPayload(created, nil), channel.CreatedAt,
	); err != nil {
		return nil, err
	}
	if versionFingerprint != "" {
		if err := enqueueWebhookEvent(ctx, q, tenant, webhook.OperationChannelAssigned,
			webhook.Target{Type: "channel", Bucket: channel.BucketName, Name: channel.Name},
			channelWebhookPayload(created, nil), channel.CreatedAt,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create channel: %w", err)
	}
	return created, nil
}

func (r *Repository) GetChannel(
	ctx context.Context,
	tenant Tenant,
	bucketName, channelName string,
) (*Channel, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	channel, err := r.getChannel(ctx, tx, q, tenant, bucketName, channelName)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit get channel: %w", err)
	}
	return channel, nil
}

func (r *Repository) ListChannels(
	ctx context.Context,
	tenant Tenant,
	bucketName string,
) ([]Channel, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := q.GetBucketByName(ctx, bucketName); err != nil {
		return nil, mapNotFound("get bucket for channels", err)
	}
	channels, err := r.listChannels(ctx, tx, q, tenant, bucketName)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit list channels: %w", err)
	}
	return channels, nil
}

func (r *Repository) listChannels(
	ctx context.Context,
	tx *sql.Tx,
	q *postgresdb.Queries,
	tenant Tenant,
	bucketName string,
) ([]Channel, error) {
	rows, err := tx.QueryContext(ctx, channelSelect+`
		WHERE buckets.name = $1
		ORDER BY channels.name
	`, bucketName)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer func() { _ = rows.Close() }()

	channelRows := make([]channelRow, 0)
	for rows.Next() {
		row, err := scanChannelRow(rows)
		if err != nil {
			return nil, err
		}
		channelRows = append(channelRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close channel rows: %w", err)
	}
	channels := make([]Channel, 0, len(channelRows))
	for i := range channelRows {
		channel, err := r.restoreChannel(ctx, q, tenant, channelRows[i])
		if err != nil {
			return nil, err
		}
		channels = append(channels, *channel)
	}
	return channels, nil
}

func (r *Repository) UpdateChannel(
	ctx context.Context,
	tenant Tenant,
	bucketName, channelName string,
	updateRestricted, restricted bool,
	updateVersion bool, versionFingerprint, authorID string,
	at time.Time,
) (*Channel, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	channel, err := r.getChannel(ctx, tx, q, tenant, bucketName, channelName)
	if err != nil {
		return nil, err
	}
	if updateRestricted {
		if _, err := tx.ExecContext(ctx, `
			UPDATE channels SET restricted = $2, updated_at = $3 WHERE id = $1
		`, channel.ID.String(), restricted, at); err != nil {
			return nil, fmt.Errorf("update channel restriction: %w", err)
		}
	}
	if updateVersion && versionFingerprint != "" {
		if err := r.assignChannel(
			ctx, tx, q, tenant, channel.ID, bucketName, versionFingerprint, authorID, at,
		); err != nil {
			return nil, err
		}
	} else if updateVersion && channel.Version != nil {
		// An empty fingerprint under the versionFingerprint mask clears the
		// assignment (duf-8em). History is append-only, so the clear is recorded
		// as a row with no version; the latest-row lateral join in channelSelect
		// then reads the channel as unassigned. Skipped when already unassigned,
		// so a repeated destroy does not pile up no-op history rows.
		clearedAt := assignmentWriteTime(at)
		clearedID := registry.NewID(at).String()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO channel_assignments (
				organization_id, project_id, id, channel_id, version_id, author_id, assigned_at, integrity_mac
			) VALUES ($1, $2, $3, $4, NULL, $5, $6, $7)
		`, tenant.OrganizationID, tenant.ProjectID, clearedID,
			channel.ID.String(), authorID, clearedAt,
			r.rowMAC(assignmentMACMessage(tenant, clearedID, channel.ID.String(), "", authorID, clearedAt))); err != nil {
			return nil, fmt.Errorf("clear channel assignment: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE channels SET updated_at = $2 WHERE id = $1
		`, channel.ID.String(), at); err != nil {
			return nil, fmt.Errorf("update cleared channel: %w", err)
		}
	}
	updated, err := r.getChannel(ctx, tx, q, tenant, bucketName, channelName)
	if err != nil {
		return nil, err
	}
	if updateVersion {
		if err := enqueueWebhookEvent(ctx, q, tenant, webhook.OperationChannelAssigned,
			webhook.Target{Type: "channel", Bucket: bucketName, Name: channelName},
			channelWebhookPayload(updated, channel), at,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit update channel: %w", err)
	}
	return updated, nil
}

func (r *Repository) AssignChannelVersion(
	ctx context.Context,
	tenant Tenant,
	bucketName, sourceName, targetName, authorID string,
	at time.Time,
) (*Channel, *Channel, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	source, err := r.getChannel(ctx, tx, q, tenant, bucketName, sourceName)
	if err != nil {
		return nil, nil, err
	}
	target, err := r.getChannel(ctx, tx, q, tenant, bucketName, targetName)
	if err != nil {
		return nil, nil, err
	}
	previous := *target
	// Defence in depth with the handler: callers other than the HTTP adapter
	// must not be able to rotate a version into the service-managed channel.
	// Live probe 40 settles this endpoint's refusal as 400/code 9 with
	// "Cannot assign to managed channel 'latest'".
	if target.Managed {
		return nil, nil, fmt.Errorf("%w: %s", ErrManagedChannel, targetName)
	}
	if source.Version == nil {
		return nil, nil, fmt.Errorf("%w: source channel %s has no assigned version", registry.ErrConflict, sourceName)
	}
	if err := r.assignChannel(
		ctx, tx, q, tenant, target.ID, bucketName, source.Version.Fingerprint, authorID, at,
	); err != nil {
		return nil, nil, err
	}
	target, err = r.getChannel(ctx, tx, q, tenant, bucketName, targetName)
	if err != nil {
		return nil, nil, err
	}
	if err := enqueueWebhookEvent(ctx, q, tenant, webhook.OperationChannelAssigned,
		webhook.Target{Type: "channel", Bucket: bucketName, Name: targetName},
		channelWebhookPayload(target, &previous), at,
	); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit assign channel version: %w", err)
	}
	return source, target, nil
}

func (r *Repository) DeleteChannel(
	ctx context.Context,
	tenant Tenant,
	bucketName, channelName string,
) error {
	at := time.Now().UTC()
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	channel, err := r.getChannel(ctx, tx, q, tenant, bucketName, channelName)
	if err != nil {
		return err
	}

	var id string
	err = tx.QueryRowContext(ctx, `
		DELETE FROM channels
		USING buckets
		WHERE channels.bucket_id = buckets.id
		  AND buckets.name = $1
		  AND channels.name = $2
		RETURNING channels.id
	`, bucketName, channelName).Scan(&id)
	if err != nil {
		return mapNotFound("delete channel", err)
	}
	if err := enqueueWebhookEvent(ctx, q, tenant, webhook.OperationChannelDeleted,
		webhook.Target{Type: "channel", Bucket: bucketName, Name: channelName},
		channelWebhookPayload(channel, nil), at,
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete channel: %w", err)
	}
	return nil
}

func (r *Repository) ListChannelAssignmentHistory(
	ctx context.Context,
	tenant Tenant,
	bucketName, channelName string,
) ([]ChannelAssignment, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	channel, err := r.getChannel(ctx, tx, q, tenant, bucketName, channelName)
	if err != nil {
		return nil, err
	}
	// The inner join deliberately omits unassignment rows (version_id IS NULL).
	// They are recorded for the trail, but HCP's shape for a cleared entry in
	// this listing is unverified, so nothing is invented here — only
	// version-bearing assignments are listed.
	rows, err := tx.QueryContext(ctx, `
		SELECT assignments.id, assignments.channel_id, assignments.author_id, assignments.assigned_at,
		       assignments.integrity_mac,
		       versions.organization_id, versions.project_id, versions.id, versions.bucket_id,
		       versions.fingerprint, versions.template_type, versions.complete, versions.sequence,
		       versions.created_at, versions.updated_at, versions.author_id, versions.integrity_mac,
		       versions.revoke_at, versions.revocation_message, versions.revocation_author,
		       versions.revocation_inherited_from_id, versions.revocation_inherited_from_bucket,
		       versions.revocation_inherited_from_fingerprint, versions.revocation_inherited_from_name
		FROM channel_assignments AS assignments
		JOIN versions ON versions.id = assignments.version_id
		WHERE assignments.channel_id = $1
		ORDER BY assignments.assigned_at DESC, assignments.id DESC
	`, channel.ID.String())
	if err != nil {
		return nil, fmt.Errorf("list channel assignment history: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type assignmentRow struct {
		assignmentID string
		channelID    string
		authorID     string
		assignedAt   time.Time
		integrityMac []byte
		version      postgresdb.GetVersionByFingerprintRow
	}
	assignmentRows := make([]assignmentRow, 0)
	for rows.Next() {
		var row assignmentRow
		if err := rows.Scan(
			&row.assignmentID,
			&row.channelID,
			&row.authorID,
			&row.assignedAt,
			&row.integrityMac,
			&row.version.OrganizationID,
			&row.version.ProjectID,
			&row.version.ID,
			&row.version.BucketID,
			&row.version.Fingerprint,
			&row.version.TemplateType,
			&row.version.Complete,
			&row.version.Sequence,
			&row.version.CreatedAt,
			&row.version.UpdatedAt,
			&row.version.AuthorID,
			&row.version.IntegrityMac,
			&row.version.RevokeAt,
			&row.version.RevocationMessage,
			&row.version.RevocationAuthor,
			&row.version.RevocationInheritedFromID,
			&row.version.RevocationInheritedFromBucket,
			&row.version.RevocationInheritedFromFingerprint,
			&row.version.RevocationInheritedFromName,
		); err != nil {
			return nil, fmt.Errorf("scan channel assignment: %w", err)
		}
		row.version.BucketName = bucketName
		assignmentRows = append(assignmentRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list channel assignment history: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close channel assignment rows: %w", err)
	}
	history := make([]ChannelAssignment, 0, len(assignmentRows))
	for i := range assignmentRows {
		row := assignmentRows[i]
		assignment, err := r.restoreAssignment(
			ctx, q, tenant, row.assignmentID, row.channelID, row.authorID, row.assignedAt,
			row.integrityMac, row.version,
		)
		if err != nil {
			return nil, err
		}
		history = append(history, *assignment)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit list channel assignment history: %w", err)
	}
	return history, nil
}

func (r *Repository) assignChannel(
	ctx context.Context,
	tx *sql.Tx,
	q *postgresdb.Queries,
	tenant Tenant,
	channelID registry.ID,
	bucketName, versionFingerprint, authorID string,
	at time.Time,
) error {
	row, err := q.GetVersionByFingerprint(ctx, postgresdb.GetVersionByFingerprintParams{
		Name:        bucketName,
		Fingerprint: versionFingerprint,
	})
	if err != nil {
		return mapNotFound("get version for channel assignment", err)
	}
	version, err := r.restoreVersion(ctx, q, tenant, row)
	if err != nil {
		return err
	}
	if err := version.AssignableToChannel(); err != nil {
		return err
	}
	if err := r.recordAssignment(ctx, tx, tenant, channelID, version.ID, authorID, at); err != nil {
		return err
	}
	return nil
}

// recordAssignment appends one history row and stamps the channel updated.
// The version's assignability is the caller's to have established.
func (r *Repository) recordAssignment(
	ctx context.Context,
	tx *sql.Tx,
	tenant Tenant,
	channelID, versionID registry.ID,
	authorID string,
	at time.Time,
) error {
	assignedAt := assignmentWriteTime(at)
	assignmentID := registry.NewID(at).String()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO channel_assignments (
			organization_id, project_id, id, channel_id, version_id, author_id, assigned_at, integrity_mac
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, tenant.OrganizationID, tenant.ProjectID, assignmentID,
		channelID.String(), versionID.String(), authorID, assignedAt,
		r.rowMAC(assignmentMACMessage(tenant, assignmentID, channelID.String(), versionID.String(), authorID, assignedAt))); err != nil {
		return fmt.Errorf("assign channel version: %w", err)
	}
	// Every completed build in the selected version enters the scan queue in
	// this transaction. The primary key coalesces repeated assignments without
	// moving the build behind newer work.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO pending_scans (
			organization_id, project_id, build_id, enqueued_at, reason
		)
		SELECT organization_id, project_id, id, $4, 'channel_assignment'
		FROM builds
		WHERE organization_id = $1 AND project_id = $2
		  AND version_id = $3 AND status = 'done'
		ON CONFLICT (organization_id, project_id, build_id) DO NOTHING
	`, tenant.OrganizationID, tenant.ProjectID, versionID.String(), assignedAt); err != nil {
		return fmt.Errorf("enqueue assigned version scans: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE channels SET updated_at = $2 WHERE id = $1
	`, channelID.String(), at); err != nil {
		return fmt.Errorf("update assigned channel: %w", err)
	}
	return nil
}

func (r *Repository) getChannel(
	ctx context.Context,
	tx *sql.Tx,
	q *postgresdb.Queries,
	tenant Tenant,
	bucketName, channelName string,
) (*Channel, error) {
	row := tx.QueryRowContext(ctx, channelSelect+`
		WHERE buckets.name = $1 AND channels.name = $2
	`, bucketName, channelName)
	channelRow, err := scanChannelRow(row)
	if err != nil {
		return nil, mapNotFound("get channel", err)
	}
	return r.restoreChannel(ctx, q, tenant, channelRow)
}

type scanner interface {
	Scan(...any) error
}

type channelRow struct {
	id, bucketName, name                                  string
	restricted, managed                                   bool
	createdAt, updatedAt                                  time.Time
	assignmentAuthorID                                    sql.NullString
	versionOrganizationID, versionProjectID               sql.NullString
	versionID, versionBucketID, fingerprint, templateType sql.NullString
	complete                                              sql.NullBool
	sequence                                              sql.NullInt32
	versionCreatedAt, versionUpdatedAt                    sql.NullTime
	versionAuthorID                                       sql.NullString
	versionIntegrityMac                                   []byte
	revokeAt                                              sql.NullTime
	revocationMessage, revocationAuthor                   sql.NullString
	ancestorID, ancestorBucket                            sql.NullString
	ancestorFingerprint, ancestorName                     sql.NullString
}

func scanChannelRow(row scanner) (channelRow, error) {
	var channel channelRow
	if err := row.Scan(
		&channel.id,
		&channel.bucketName,
		&channel.name,
		&channel.restricted,
		&channel.managed,
		&channel.createdAt,
		&channel.updatedAt,
		&channel.assignmentAuthorID,
		&channel.versionOrganizationID,
		&channel.versionProjectID,
		&channel.versionID,
		&channel.versionBucketID,
		&channel.fingerprint,
		&channel.templateType,
		&channel.complete,
		&channel.sequence,
		&channel.versionCreatedAt,
		&channel.versionUpdatedAt,
		&channel.versionAuthorID,
		&channel.versionIntegrityMac,
		&channel.revokeAt,
		&channel.revocationMessage,
		&channel.revocationAuthor,
		&channel.ancestorID,
		&channel.ancestorBucket,
		&channel.ancestorFingerprint,
		&channel.ancestorName,
	); err != nil {
		return channelRow{}, err
	}
	return channel, nil
}

func (r *Repository) restoreChannel(
	ctx context.Context,
	q *postgresdb.Queries,
	tenant Tenant,
	row channelRow,
) (*Channel, error) {
	id, err := registry.ParseID(row.id)
	if err != nil {
		return nil, fmt.Errorf("restore channel id: %w", err)
	}
	channel := &Channel{
		ID:                 id,
		BucketName:         row.bucketName,
		Name:               row.name,
		Restricted:         row.restricted,
		Managed:            row.managed,
		AssignmentAuthorID: row.assignmentAuthorID.String,
		CreatedAt:          row.createdAt,
		UpdatedAt:          row.updatedAt,
	}
	if row.versionID.Valid {
		// Columns are typed uuid and already tenant-scoped, so this cannot fail in
		// practice — but a parse error is returned rather than panicking, because a
		// panic in a read path takes the server down for a data problem.
		versionOrganization, err := uuid.Parse(row.versionOrganizationID.String)
		if err != nil {
			return nil, fmt.Errorf("parse version organization: %w", err)
		}
		versionProject, err := uuid.Parse(row.versionProjectID.String)
		if err != nil {
			return nil, fmt.Errorf("parse version project: %w", err)
		}
		version, err := r.restoreVersion(ctx, q, tenant, postgresdb.GetVersionByFingerprintRow{
			OrganizationID:                     versionOrganization,
			ProjectID:                          versionProject,
			ID:                                 row.versionID.String,
			BucketID:                           row.versionBucketID.String,
			Fingerprint:                        row.fingerprint.String,
			TemplateType:                       row.templateType.String,
			Complete:                           row.complete.Bool,
			Sequence:                           row.sequence,
			CreatedAt:                          row.versionCreatedAt.Time,
			UpdatedAt:                          row.versionUpdatedAt.Time,
			AuthorID:                           row.versionAuthorID.String,
			IntegrityMac:                       row.versionIntegrityMac,
			BucketName:                         row.bucketName,
			RevokeAt:                           row.revokeAt,
			RevocationMessage:                  row.revocationMessage,
			RevocationAuthor:                   row.revocationAuthor,
			RevocationInheritedFromID:          row.ancestorID,
			RevocationInheritedFromBucket:      row.ancestorBucket,
			RevocationInheritedFromFingerprint: row.ancestorFingerprint,
			RevocationInheritedFromName:        row.ancestorName,
		})
		if err != nil {
			return nil, err
		}
		channel.Version = version
	}
	return channel, nil
}

func (r *Repository) restoreAssignment(
	ctx context.Context,
	q *postgresdb.Queries,
	tenant Tenant,
	assignmentID, channelID, authorID string,
	assignedAt time.Time,
	integrityMac []byte,
	row postgresdb.GetVersionByFingerprintRow,
) (*ChannelAssignment, error) {
	if err := r.verifyRowMAC("channel assignment "+assignmentID, integrityMac,
		assignmentMACMessage(tenant, assignmentID, channelID, row.ID, authorID, assignedAt)); err != nil {
		return nil, err
	}
	id, err := registry.ParseID(assignmentID)
	if err != nil {
		return nil, fmt.Errorf("restore assignment id: %w", err)
	}
	parsedChannelID, err := registry.ParseID(channelID)
	if err != nil {
		return nil, fmt.Errorf("restore assignment channel id: %w", err)
	}
	version, err := r.restoreVersion(ctx, q, tenant, row)
	if err != nil {
		return nil, err
	}
	return &ChannelAssignment{
		ID:         id,
		ChannelID:  parsedChannelID,
		Version:    version,
		AuthorID:   authorID,
		AssignedAt: assignedAt,
	}, nil
}

const channelSelect = `
	SELECT channels.id, buckets.name, channels.name, channels.restricted, channels.managed,
	       channels.created_at, channels.updated_at, latest_assignment.author_id,
	       versions.organization_id, versions.project_id, versions.id, versions.bucket_id,
	       versions.fingerprint, versions.template_type, versions.complete, versions.sequence,
	       versions.created_at, versions.updated_at, versions.author_id, versions.integrity_mac,
	       versions.revoke_at, versions.revocation_message, versions.revocation_author,
	       versions.revocation_inherited_from_id, versions.revocation_inherited_from_bucket,
	       versions.revocation_inherited_from_fingerprint, versions.revocation_inherited_from_name
	FROM channels
	JOIN buckets ON buckets.id = channels.bucket_id
	LEFT JOIN LATERAL (
		SELECT assignments.version_id, assignments.author_id
		FROM channel_assignments AS assignments
		WHERE assignments.channel_id = channels.id
		ORDER BY assignments.assigned_at DESC, assignments.id DESC
		LIMIT 1
	) AS latest_assignment ON true
	LEFT JOIN versions ON versions.id = latest_assignment.version_id
`
