package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/benemon/dufflebag/internal/domain/registry"
)

// AncestryVersion is the version identity needed by the ancestry projection.
type AncestryVersion struct {
	ID          registry.ID
	BucketName  string
	Fingerprint string
	Sequence    int
}

// BucketAncestry is one parent-child relationship recorded by a child build.
type BucketAncestry struct {
	Parent               AncestryVersion
	ParentChannelName    string
	ParentChannelVersion *AncestryVersion
	Child                AncestryVersion
}

// ListBucketAncestry projects the parent identifiers Packer stored on builds.
func (r *Repository) ListBucketAncestry(
	ctx context.Context,
	tenant Tenant,
	bucketName, ancestryType, channelName, versionFingerprint string,
) ([]BucketAncestry, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := q.GetBucketByName(ctx, bucketName); err != nil {
		return nil, mapNotFound("get bucket for ancestry", err)
	}
	rows, err := tx.QueryContext(ctx, `
		WITH requested_child AS (
			SELECT versions.id
			FROM versions
			JOIN buckets ON buckets.id = versions.bucket_id
			WHERE buckets.name = $1
			  AND (($4 <> '' AND versions.fingerprint = $4)
			       OR ($4 = '' AND versions.complete))
			ORDER BY versions.sequence DESC NULLS LAST, versions.created_at DESC, versions.id DESC
			LIMIT 1
		)
		SELECT DISTINCT
		       parent_versions.id, parent_buckets.name, parent_versions.fingerprint,
		       parent_versions.sequence,
		       parent_channels.name,
		       channel_versions.id, channel_versions.fingerprint, channel_versions.sequence,
		       child_versions.id, child_buckets.name, child_versions.fingerprint,
		       child_versions.sequence
		FROM builds
		JOIN versions AS child_versions ON child_versions.id = builds.version_id
		JOIN buckets AS child_buckets ON child_buckets.id = child_versions.bucket_id
		JOIN versions AS parent_versions ON parent_versions.id = builds.parent_version_id
		JOIN buckets AS parent_buckets ON parent_buckets.id = parent_versions.bucket_id
		LEFT JOIN channels AS parent_channels ON parent_channels.id = builds.parent_channel_id
		LEFT JOIN LATERAL (
			SELECT assignments.version_id
			FROM channel_assignments AS assignments
			WHERE assignments.channel_id = parent_channels.id
			ORDER BY assignments.assigned_at DESC, assignments.id DESC
			LIMIT 1
		) AS current_assignment ON true
		LEFT JOIN versions AS channel_versions ON channel_versions.id = current_assignment.version_id
		WHERE builds.parent_version_id IS NOT NULL
		  AND (
			(($2 = 'ANCESTRY_TYPE_UNSET' OR $2 = 'ANCESTRY_TYPE_PARENTS')
			 AND child_buckets.name = $1
			 AND child_versions.id = (SELECT id FROM requested_child))
			OR
			(($2 = 'ANCESTRY_TYPE_UNSET' OR $2 = 'ANCESTRY_TYPE_CHILDREN')
			 AND parent_buckets.name = $1
			 AND ($3 = '' OR parent_channels.name = $3))
		  )
		ORDER BY child_buckets.name, child_versions.sequence DESC NULLS LAST, child_versions.id,
		         parent_buckets.name, parent_versions.sequence DESC NULLS LAST, parent_versions.id,
		         parent_channels.name, channel_versions.id
	`, bucketName, ancestryType, channelName, versionFingerprint)
	if err != nil {
		return nil, fmt.Errorf("list bucket ancestry: %w", err)
	}
	defer func() { _ = rows.Close() }()

	relations := make([]BucketAncestry, 0)
	for rows.Next() {
		var (
			parentID, parentBucket, parentFingerprint string
			parentSequence                            sql.NullInt32
			parentChannel                             sql.NullString
			channelVersionID, channelFingerprint      sql.NullString
			channelSequence                           sql.NullInt32
			childID, childBucket, childFingerprint    string
			childSequence                             sql.NullInt32
		)
		if err := rows.Scan(
			&parentID, &parentBucket, &parentFingerprint, &parentSequence,
			&parentChannel,
			&channelVersionID, &channelFingerprint, &channelSequence,
			&childID, &childBucket, &childFingerprint, &childSequence,
		); err != nil {
			return nil, fmt.Errorf("scan bucket ancestry: %w", err)
		}
		parent, err := restoreAncestryVersion(parentID, parentBucket, parentFingerprint, parentSequence)
		if err != nil {
			return nil, err
		}
		child, err := restoreAncestryVersion(childID, childBucket, childFingerprint, childSequence)
		if err != nil {
			return nil, err
		}
		relation := BucketAncestry{
			Parent:            parent,
			ParentChannelName: parentChannel.String,
			Child:             child,
		}
		if channelVersionID.Valid {
			version, err := restoreAncestryVersion(
				channelVersionID.String, parentBucket, channelFingerprint.String, channelSequence,
			)
			if err != nil {
				return nil, err
			}
			relation.ParentChannelVersion = &version
		}
		relations = append(relations, relation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bucket ancestry: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit list bucket ancestry: %w", err)
	}
	return relations, nil
}

func restoreAncestryVersion(
	id, bucketName, fingerprint string,
	sequence sql.NullInt32,
) (AncestryVersion, error) {
	parsedID, err := registry.ParseID(id)
	if err != nil {
		return AncestryVersion{}, fmt.Errorf("restore ancestry version id: %w", err)
	}
	return AncestryVersion{
		ID:          parsedID,
		BucketName:  bucketName,
		Fingerprint: fingerprint,
		Sequence:    int(sequence.Int32),
	}, nil
}
