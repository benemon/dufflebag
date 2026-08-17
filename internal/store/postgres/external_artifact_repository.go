package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/google/uuid"
)

// ExternalArtifactMatch is the registry context for one matching build artifact.
type ExternalArtifactMatch struct {
	Bucket  Bucket
	Build   StoredBuild
	Version *registry.Version
}

type externalArtifactVersionRow struct {
	OrganizationID                     uuid.UUID  `json:"organization_id"`
	ProjectID                          uuid.UUID  `json:"project_id"`
	ID                                 string     `json:"id"`
	BucketID                           string     `json:"bucket_id"`
	Fingerprint                        string     `json:"fingerprint"`
	TemplateType                       string     `json:"template_type"`
	Complete                           bool       `json:"complete"`
	Sequence                           *int32     `json:"sequence"`
	CreatedAt                          time.Time  `json:"created_at"`
	UpdatedAt                          time.Time  `json:"updated_at"`
	AuthorID                           string     `json:"author_id"`
	IntegrityMac                       []byte     `json:"integrity_mac"`
	RevokeAt                           *time.Time `json:"revoke_at"`
	RevocationMessage                  *string    `json:"revocation_message"`
	RevocationAuthor                   *string    `json:"revocation_author"`
	RevocationInheritedFromID          *string    `json:"revocation_inherited_from_id"`
	RevocationInheritedFromBucket      *string    `json:"revocation_inherited_from_bucket"`
	RevocationInheritedFromFingerprint *string    `json:"revocation_inherited_from_fingerprint"`
	RevocationInheritedFromName        *string    `json:"revocation_inherited_from_name"`
}

type externalArtifactBuildRow struct {
	OrganizationID           uuid.UUID       `json:"organization_id"`
	ProjectID                uuid.UUID       `json:"project_id"`
	ID                       string          `json:"id"`
	VersionID                string          `json:"version_id"`
	ComponentType            string          `json:"component_type"`
	Status                   string          `json:"status"`
	Platform                 string          `json:"platform"`
	MetadataSeen             bool            `json:"metadata_seen"`
	PackerRunUUID            string          `json:"packer_run_uuid"`
	Labels                   json.RawMessage `json:"labels"`
	SourceExternalIdentifier string          `json:"source_external_identifier"`
	CreatedAt                time.Time       `json:"created_at"`
	UpdatedAt                time.Time       `json:"updated_at"`
	ParentVersionID          *string         `json:"parent_version_id"`
	ParentChannelID          *string         `json:"parent_channel_id"`
	Metadata                 []byte          `json:"metadata"`
	IntegrityMac             []byte          `json:"integrity_mac"`
}

// SearchExternalArtifacts finds builds that contain an exact external identifier.
func (r *Repository) SearchExternalArtifacts(
	ctx context.Context,
	tenant Tenant,
	externalIdentifier, platform, region string,
) ([]ExternalArtifactMatch, error) {
	tx, _, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT to_jsonb(buckets),
		       to_jsonb(versions) || jsonb_build_object(
			       'integrity_mac', encode(versions.integrity_mac, 'base64')
		       ),
		       to_jsonb(builds) || jsonb_build_object(
			       'integrity_mac', encode(builds.integrity_mac, 'base64'),
			       'metadata', encode(builds.metadata, 'base64')
		       ),
		       artifact_rows.artifacts
		FROM artifacts AS matched_artifacts
		JOIN builds ON builds.id = matched_artifacts.build_id
		JOIN versions ON versions.id = builds.version_id
		JOIN buckets ON buckets.id = versions.bucket_id
		JOIN LATERAL (
			SELECT coalesce(
				jsonb_agg(
					to_jsonb(all_artifacts) || jsonb_build_object(
						'integrity_mac', encode(all_artifacts.integrity_mac, 'base64')
					) ORDER BY all_artifacts.id DESC
				),
				'[]'::jsonb
			) AS artifacts
			FROM artifacts AS all_artifacts
			WHERE all_artifacts.build_id = builds.id
		) AS artifact_rows ON true
		WHERE matched_artifacts.external_identifier = $1
		  AND ($2 = '' OR builds.platform = $2)
		  AND ($3 = '' OR matched_artifacts.region = $3)
		ORDER BY versions.sequence DESC NULLS LAST,
		         versions.created_at DESC,
		         versions.id DESC,
		         builds.id DESC,
		         matched_artifacts.id DESC
	`, externalIdentifier, platform, region)
	if err != nil {
		return nil, fmt.Errorf("search external artifacts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	matches := make([]ExternalArtifactMatch, 0)
	for rows.Next() {
		var bucketJSON, versionJSON, buildJSON, artifactsJSON []byte
		if err := rows.Scan(&bucketJSON, &versionJSON, &buildJSON, &artifactsJSON); err != nil {
			return nil, fmt.Errorf("scan external artifact: %w", err)
		}
		var bucketRow postgresdb.Bucket
		if err := json.Unmarshal(bucketJSON, &bucketRow); err != nil {
			return nil, fmt.Errorf("decode external artifact bucket: %w", err)
		}
		var versionJSONRow externalArtifactVersionRow
		if err := json.Unmarshal(versionJSON, &versionJSONRow); err != nil {
			return nil, fmt.Errorf("decode external artifact version: %w", err)
		}
		var buildJSONRow externalArtifactBuildRow
		if err := json.Unmarshal(buildJSON, &buildJSONRow); err != nil {
			return nil, fmt.Errorf("decode external artifact build: %w", err)
		}
		var artifactRows []postgresdb.Artifact
		if err := json.Unmarshal(artifactsJSON, &artifactRows); err != nil {
			return nil, fmt.Errorf("decode external artifact artifacts: %w", err)
		}

		bucket, err := restoreBucket(bucketRow)
		if err != nil {
			return nil, err
		}
		buildRow := postgresdb.Build{
			OrganizationID:           buildJSONRow.OrganizationID,
			ProjectID:                buildJSONRow.ProjectID,
			ID:                       buildJSONRow.ID,
			VersionID:                buildJSONRow.VersionID,
			ComponentType:            buildJSONRow.ComponentType,
			Status:                   buildJSONRow.Status,
			Platform:                 buildJSONRow.Platform,
			MetadataSeen:             buildJSONRow.MetadataSeen,
			PackerRunUuid:            buildJSONRow.PackerRunUUID,
			Labels:                   buildJSONRow.Labels,
			SourceExternalIdentifier: buildJSONRow.SourceExternalIdentifier,
			CreatedAt:                buildJSONRow.CreatedAt,
			UpdatedAt:                buildJSONRow.UpdatedAt,
			ParentVersionID:          nullableString(buildJSONRow.ParentVersionID),
			ParentChannelID:          nullableString(buildJSONRow.ParentChannelID),
			Metadata:                 buildJSONRow.Metadata,
			IntegrityMac:             buildJSONRow.IntegrityMac,
		}
		build, err := r.restoreBuildFromRows(tenant, buildRow, artifactRows)
		if err != nil {
			return nil, err
		}
		versionProjection := postgresdb.GetVersionByFingerprintRow{
			OrganizationID:                     versionJSONRow.OrganizationID,
			ProjectID:                          versionJSONRow.ProjectID,
			ID:                                 versionJSONRow.ID,
			BucketID:                           versionJSONRow.BucketID,
			Fingerprint:                        versionJSONRow.Fingerprint,
			TemplateType:                       versionJSONRow.TemplateType,
			Complete:                           versionJSONRow.Complete,
			Sequence:                           nullableVersionSequence(versionJSONRow.Sequence),
			CreatedAt:                          versionJSONRow.CreatedAt,
			UpdatedAt:                          versionJSONRow.UpdatedAt,
			AuthorID:                           versionJSONRow.AuthorID,
			IntegrityMac:                       versionJSONRow.IntegrityMac,
			RevokeAt:                           nullableTime(versionJSONRow.RevokeAt),
			RevocationMessage:                  nullableString(versionJSONRow.RevocationMessage),
			RevocationAuthor:                   nullableString(versionJSONRow.RevocationAuthor),
			RevocationInheritedFromID:          nullableString(versionJSONRow.RevocationInheritedFromID),
			RevocationInheritedFromBucket:      nullableString(versionJSONRow.RevocationInheritedFromBucket),
			RevocationInheritedFromFingerprint: nullableString(versionJSONRow.RevocationInheritedFromFingerprint),
			RevocationInheritedFromName:        nullableString(versionJSONRow.RevocationInheritedFromName),
			BucketName:                         bucket.Name,
		}
		version, err := r.restoreVersionFromRows(versionProjection, []StoredBuild{*build}, false, "")
		if err != nil {
			return nil, err
		}
		matches = append(matches, ExternalArtifactMatch{
			Bucket: *bucket, Build: *build, Version: version,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search external artifacts: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit search external artifacts: %w", err)
	}
	return matches, nil
}

func nullableVersionSequence(value *int32) sql.NullInt32 {
	if value == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: *value, Valid: true}
}
