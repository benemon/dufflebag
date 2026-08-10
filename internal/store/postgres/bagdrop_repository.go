package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/benemon/dufflebag/internal/bagdrop"
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
