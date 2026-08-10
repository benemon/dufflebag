package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/benemon/dufflebag/internal/bagdrop"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/jackc/pgx/v5/pgconn"
)

func (r *Repository) GetBagDropConfig(
	ctx context.Context, organizationID, projectID string,
) (*bagdrop.Record, error) {
	tx, q, err := r.begin(ctx, ParseTenant(organizationID, projectID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.GetBagDropConfig(ctx)
	if err != nil {
		return nil, mapBagDropNotFound("get Bag Drop configuration", err)
	}
	record, err := restoreBagDropConfig(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit get Bag Drop configuration: %w", err)
	}
	return record, nil
}

func (r *Repository) PutBagDropConfig(
	ctx context.Context, record *bagdrop.Record,
) (*bagdrop.Record, error) {
	if err := bagdrop.ValidateRecord(record); err != nil {
		return nil, err
	}
	tenant := ParseTenant(record.OrganizationID, record.ProjectID)
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	destination, err := json.Marshal(record.HCPPacker)
	if err != nil {
		return nil, fmt.Errorf("marshal Bag Drop destination: %w", err)
	}
	verification, verifiedAt, err := encodeBagDropVerification(record.LastVerification)
	if err != nil {
		return nil, err
	}
	row, err := q.UpsertBagDropConfig(ctx, postgresdb.UpsertBagDropConfigParams{
		OrganizationID:   tenant.OrganizationID,
		ProjectID:        tenant.ProjectID,
		Adapter:          string(record.Adapter),
		Destination:      destination,
		SealedSecret:     record.SealedSecret,
		Enabled:          record.Enabled,
		LastVerification: verification,
		LastVerifiedAt:   verifiedAt,
		CreatedAt:        record.CreatedAt,
		UpdatedAt:        record.UpdatedAt,
	})
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23503" {
		return nil, fmt.Errorf("put Bag Drop configuration: %w", bagdrop.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("put Bag Drop configuration: %w", err)
	}
	stored, err := restoreBagDropConfig(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit put Bag Drop configuration: %w", err)
	}
	return stored, nil
}

func (r *Repository) DeleteBagDropConfig(
	ctx context.Context, organizationID, projectID string,
) error {
	tx, q, err := r.begin(ctx, ParseTenant(organizationID, projectID))
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	deleted, err := q.DeleteBagDropConfig(ctx)
	if err != nil {
		return fmt.Errorf("delete Bag Drop configuration: %w", err)
	}
	if deleted == 0 {
		return fmt.Errorf("delete Bag Drop configuration: %w", bagdrop.ErrNotFound)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete Bag Drop configuration: %w", err)
	}
	return nil
}

func (r *Repository) RecordBagDropVerification(
	ctx context.Context, organizationID, projectID string,
	result bagdrop.VerificationResult, verifiedAt time.Time,
) (*bagdrop.Record, error) {
	tx, q, err := r.begin(ctx, ParseTenant(organizationID, projectID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal Bag Drop verification: %w", err)
	}
	row, err := q.RecordBagDropVerification(ctx, postgresdb.RecordBagDropVerificationParams{
		LastVerification: encoded,
		LastVerifiedAt:   sql.NullTime{Time: verifiedAt, Valid: true},
	})
	if err != nil {
		return nil, mapBagDropNotFound("record Bag Drop verification", err)
	}
	stored, err := restoreBagDropConfig(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit Bag Drop verification: %w", err)
	}
	return stored, nil
}

func (r *Repository) SetBagDropEnabled(
	ctx context.Context, organizationID, projectID string, enabled bool,
	result *bagdrop.VerificationResult, at time.Time,
) (*bagdrop.Record, error) {
	tx, q, err := r.begin(ctx, ParseTenant(organizationID, projectID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var row postgresdb.BagdropConfig
	if enabled {
		encoded, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return nil, fmt.Errorf("marshal Bag Drop verification: %w", marshalErr)
		}
		row, err = q.EnableBagDrop(ctx, postgresdb.EnableBagDropParams{
			LastVerification: encoded,
			LastVerifiedAt:   sql.NullTime{Time: at, Valid: true},
		})
	} else {
		row, err = q.DisableBagDrop(ctx, at)
	}
	if err != nil {
		return nil, mapBagDropNotFound("set Bag Drop enabled state", err)
	}
	stored, err := restoreBagDropConfig(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit Bag Drop enabled state: %w", err)
	}
	return stored, nil
}

func (r *Repository) ListBagDropAssociations(
	ctx context.Context, organizationID, projectID string,
) ([]bagdrop.Association, error) {
	tx, q, err := r.begin(ctx, ParseTenant(organizationID, projectID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := q.ListBagDropAssociations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Bag Drop associations: %w", err)
	}
	associations := make([]bagdrop.Association, 0, len(rows))
	for _, row := range rows {
		association, restoreErr := restoreBagDropAssociation(row)
		if restoreErr != nil {
			return nil, restoreErr
		}
		associations = append(associations, *association)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit list Bag Drop associations: %w", err)
	}
	return associations, nil
}

func (r *Repository) PutBagDropAssociation(
	ctx context.Context, association bagdrop.Association,
) (*bagdrop.Association, error) {
	tenant := ParseTenant(association.OrganizationID, association.ProjectID)
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.UpsertBagDropAssociation(ctx, postgresdb.UpsertBagDropAssociationParams{
		OrganizationID:   tenant.OrganizationID,
		ProjectID:        tenant.ProjectID,
		BucketName:       association.BucketName,
		State:            string(association.State),
		FirstAttemptedAt: nullableTime(association.FirstAttemptedAt),
		LastAttemptAt:    nullableTime(association.LastAttemptAt),
		LastSyncedAt:     nullableTime(association.LastSyncedAt),
		LastSyncError:    nullableString(association.LastSyncError),
		CreatedAt:        association.CreatedAt,
		UpdatedAt:        association.UpdatedAt,
	})
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23503" {
		return nil, fmt.Errorf("put Bag Drop association: %w", bagdrop.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("put Bag Drop association: %w", err)
	}
	stored, err := restoreBagDropAssociation(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit put Bag Drop association: %w", err)
	}
	return stored, nil
}

func (r *Repository) RemoveBagDropAssociation(
	ctx context.Context, organizationID, projectID, bucketName string, at time.Time,
) (bagdrop.RemovalOutcome, error) {
	tx, q, err := r.begin(ctx, ParseTenant(organizationID, projectID))
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	outcome, err := q.RemoveBagDropAssociation(ctx, postgresdb.RemoveBagDropAssociationParams{
		BucketName: bucketName, UpdatedAt: at,
	})
	if err != nil {
		return "", fmt.Errorf("remove Bag Drop association: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit remove Bag Drop association: %w", err)
	}
	return bagdrop.RemovalOutcome(outcome), nil
}

func (r *Repository) BagDropBucketExists(
	ctx context.Context, organizationID, projectID, bucketName string,
) (bool, error) {
	tx, q, err := r.begin(ctx, ParseTenant(organizationID, projectID))
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	exists, err := q.BagDropBucketExists(ctx, bucketName)
	if err != nil {
		return false, fmt.Errorf("check Bag Drop bucket: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit check Bag Drop bucket: %w", err)
	}
	return exists, nil
}

func (r *Repository) HasBlockingBagDropAssociations(
	ctx context.Context, organizationID, projectID string,
) (bool, error) {
	tx, q, err := r.begin(ctx, ParseTenant(organizationID, projectID))
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	blocked, err := q.HasBlockingBagDropAssociations(ctx)
	if err != nil {
		return false, fmt.Errorf("check blocking Bag Drop associations: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit check blocking Bag Drop associations: %w", err)
	}
	return blocked, nil
}

func restoreBagDropAssociation(
	row postgresdb.BagdropAssociation,
) (*bagdrop.Association, error) {
	state := bagdrop.AssociationState(row.State)
	if state != bagdrop.AssociationActive && state != bagdrop.AssociationPendingRemoval {
		return nil, fmt.Errorf("restore Bag Drop association: invalid state %q", row.State)
	}
	return &bagdrop.Association{
		OrganizationID:   row.OrganizationID.String(),
		ProjectID:        row.ProjectID.String(),
		BucketName:       row.BucketName,
		State:            state,
		FirstAttemptedAt: timePointer(row.FirstAttemptedAt),
		LastAttemptAt:    timePointer(row.LastAttemptAt),
		LastSyncedAt:     timePointer(row.LastSyncedAt),
		LastSyncError:    stringPointer(row.LastSyncError),
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}, nil
}

func nullableTime(value *time.Time) sql.NullTime {
	if value == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *value, Valid: true}
}

func nullableString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func timePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

func stringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

// ListBagDropProjects follows the scanner's privileged enumeration pattern:
// projects are globally enumerable system data, then each Bag Drop config is
// read through a tenant-scoped repository operation.
func (r *Repository) ListBagDropProjects(ctx context.Context) ([]bagdrop.Project, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT organization_id::text, id::text FROM projects ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list Bag Drop projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var candidates []bagdrop.Project
	for rows.Next() {
		var project bagdrop.Project
		if err := rows.Scan(&project.OrganizationID, &project.ProjectID); err != nil {
			return nil, fmt.Errorf("scan Bag Drop project: %w", err)
		}
		candidates = append(candidates, project)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close Bag Drop project rows: %w", err)
	}

	var projects []bagdrop.Project
	for _, project := range candidates {
		record, err := r.GetBagDropConfig(ctx, project.OrganizationID, project.ProjectID)
		if errors.Is(err, bagdrop.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if record.Enabled {
			projects = append(projects, project)
		}
	}
	return projects, nil
}

func (r *Repository) GetBagDropBucketSnapshot(
	ctx context.Context, organizationID, projectID, bucketName string,
) (*bagdrop.BucketSnapshot, error) {
	tenant := ParseTenant(organizationID, projectID)
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	bucketRow, err := q.GetBucketByName(ctx, bucketName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get Bag Drop bucket snapshot: %w", err)
	}
	bucket, err := restoreBucket(bucketRow)
	if err != nil {
		return nil, err
	}
	versionRows, err := q.ListVersionsByBucket(ctx, bucketName)
	if err != nil {
		return nil, fmt.Errorf("list Bag Drop snapshot versions: %w", err)
	}
	completed := make([]postgresdb.ListVersionsByBucketRow, 0, len(versionRows))
	for _, row := range versionRows {
		if row.Complete && row.Sequence.Valid {
			completed = append(completed, row)
		}
	}
	sort.Slice(completed, func(i, j int) bool { return completed[i].Sequence.Int32 < completed[j].Sequence.Int32 })
	snapshot := &bagdrop.BucketSnapshot{Name: bucket.Name, Description: bucket.Description}
	for _, row := range completed {
		versionRow := postgresdb.GetVersionByFingerprintRow(row)
		if err := r.verifyRowMAC("version "+row.ID, row.IntegrityMac, versionMACMessage(postgresdb.Version{
			OrganizationID: row.OrganizationID, ProjectID: row.ProjectID, ID: row.ID,
			BucketID: row.BucketID, Fingerprint: row.Fingerprint, TemplateType: row.TemplateType,
			Complete: row.Complete, Sequence: row.Sequence, AuthorID: row.AuthorID,
			RevokeAt: row.RevokeAt, RevocationAuthor: row.RevocationAuthor,
			RevocationInheritedFromID: row.RevocationInheritedFromID,
		})); err != nil {
			return nil, err
		}
		builds, err := r.listBuilds(ctx, q, tenant, bucketName, row.Fingerprint)
		if err != nil {
			return nil, err
		}
		versionSnapshot := bagdrop.VersionSnapshot{
			Fingerprint: versionRow.Fingerprint, TemplateType: versionRow.TemplateType,
		}
		for _, build := range builds {
			if build.Status != registry.BuildDone {
				continue
			}
			buildSnapshot := bagdrop.BuildSnapshot{
				ID: build.ID.String(), ComponentType: build.ComponentType,
				PackerRunUUID: build.PackerRunUUID, Platform: build.Platform,
				Labels: build.Labels, SourceExternalIdentifier: build.SourceExternalIdentifier,
				Metadata: append([]byte(nil), build.Metadata...),
			}
			for _, artifact := range build.Artifacts {
				buildSnapshot.Artifacts = append(buildSnapshot.Artifacts, bagdrop.ArtifactSnapshot{
					ExternalIdentifier: artifact.ExternalIdentifier, Region: artifact.Region,
				})
			}
			versionSnapshot.Builds = append(versionSnapshot.Builds, buildSnapshot)
		}
		snapshot.Versions = append(snapshot.Versions, versionSnapshot)
	}
	channels, err := r.listChannels(ctx, tx, q, tenant, bucketName)
	if err != nil {
		return nil, fmt.Errorf("list Bag Drop snapshot channels: %w", err)
	}
	for _, channel := range channels {
		if channel.Managed {
			continue
		}
		channelSnapshot := bagdrop.ChannelSnapshot{Name: channel.Name}
		if channel.Version != nil {
			fingerprint := channel.Version.Fingerprint
			channelSnapshot.AssignedVersionFingerprint = &fingerprint
		}
		snapshot.Channels = append(snapshot.Channels, channelSnapshot)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit Bag Drop bucket snapshot: %w", err)
	}
	return snapshot, nil
}

func (r *Repository) MarkBagDropAssociationAttempt(
	ctx context.Context, organizationID, projectID, bucketName string, at time.Time,
) error {
	tx, q, err := r.begin(ctx, ParseTenant(organizationID, projectID))
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := q.MarkBagDropAssociationAttempt(ctx, postgresdb.MarkBagDropAssociationAttemptParams{
		BucketName: bucketName, LastAttemptAt: sql.NullTime{Time: at, Valid: true},
	}); err != nil {
		return fmt.Errorf("mark Bag Drop association attempt: %w", err)
	}
	return tx.Commit()
}

func (r *Repository) RecordBagDropAssociationSuccess(
	ctx context.Context, organizationID, projectID, bucketName string, at time.Time,
) error {
	tx, q, err := r.begin(ctx, ParseTenant(organizationID, projectID))
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := q.RecordBagDropAssociationSuccess(ctx, postgresdb.RecordBagDropAssociationSuccessParams{
		BucketName: bucketName, LastSyncedAt: sql.NullTime{Time: at, Valid: true},
	}); err != nil {
		return fmt.Errorf("record Bag Drop association success: %w", err)
	}
	return tx.Commit()
}

func (r *Repository) RecordBagDropAssociationFailure(
	ctx context.Context, organizationID, projectID, bucketName, summary string, at time.Time,
) error {
	tx, q, err := r.begin(ctx, ParseTenant(organizationID, projectID))
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := q.RecordBagDropAssociationFailure(ctx, postgresdb.RecordBagDropAssociationFailureParams{
		BucketName: bucketName, LastSyncError: sql.NullString{String: summary, Valid: true}, UpdatedAt: at,
	}); err != nil {
		return fmt.Errorf("record Bag Drop association failure: %w", err)
	}
	return tx.Commit()
}

func restoreBagDropConfig(row postgresdb.BagdropConfig) (*bagdrop.Record, error) {
	var destination bagdrop.HCPPackerConfig
	if err := json.Unmarshal(row.Destination, &destination); err != nil {
		return nil, fmt.Errorf("unmarshal Bag Drop destination: %w", err)
	}
	var verification *bagdrop.LastVerification
	if len(row.LastVerification) != 0 {
		var result bagdrop.VerificationResult
		if err := json.Unmarshal(row.LastVerification, &result); err != nil {
			return nil, fmt.Errorf("unmarshal Bag Drop verification: %w", err)
		}
		if !row.LastVerifiedAt.Valid {
			return nil, errors.New("restore Bag Drop configuration: verification has no timestamp")
		}
		verification = &bagdrop.LastVerification{
			VerificationResult: result, VerifiedAt: row.LastVerifiedAt.Time,
		}
	}
	record := &bagdrop.Record{
		OrganizationID: row.OrganizationID.String(), ProjectID: row.ProjectID.String(),
		Adapter: bagdrop.AdapterKind(row.Adapter), HCPPacker: destination,
		SealedSecret: append([]byte(nil), row.SealedSecret...), Enabled: row.Enabled,
		LastVerification: verification, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if err := bagdrop.ValidateRecord(record); err != nil {
		return nil, fmt.Errorf("restore Bag Drop configuration: %w", err)
	}
	return record, nil
}

func encodeBagDropVerification(
	verification *bagdrop.LastVerification,
) ([]byte, sql.NullTime, error) {
	if verification == nil {
		return nil, sql.NullTime{}, nil
	}
	encoded, err := json.Marshal(verification.VerificationResult)
	if err != nil {
		return nil, sql.NullTime{}, fmt.Errorf("marshal Bag Drop verification: %w", err)
	}
	return encoded, sql.NullTime{Time: verification.VerifiedAt, Valid: true}, nil
}

func mapBagDropNotFound(action string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", action, bagdrop.ErrNotFound)
	}
	return fmt.Errorf("%s: %w", action, err)
}
