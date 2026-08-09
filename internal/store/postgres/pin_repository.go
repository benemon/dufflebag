package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/jackc/pgx/v5/pgconn"
)

// Pin is shared project presentation state for one bucket.
type Pin struct {
	BucketName string
	PinnedAt   time.Time
	PinnedBy   string
}

func (r *Repository) ListPins(ctx context.Context, tenant Tenant) ([]Pin, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := q.ListPins(ctx)
	if err != nil {
		return nil, fmt.Errorf("list pins: %w", err)
	}
	pins := make([]Pin, 0, len(rows))
	for _, row := range rows {
		pins = append(pins, Pin{
			BucketName: row.BucketName,
			PinnedAt:   row.PinnedAt,
			PinnedBy:   row.PinnedBy,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit list pins: %w", err)
	}
	return pins, nil
}

func (r *Repository) SetPin(
	ctx context.Context, tenant Tenant, bucketName, pinnedBy string, pinnedAt time.Time,
) (*Pin, error) {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	err = q.InsertPin(ctx, postgresdb.InsertPinParams{
		OrganizationID: tenant.OrganizationID,
		ProjectID:      tenant.ProjectID,
		BucketName:     bucketName,
		PinnedAt:       pinnedAt,
		PinnedBy:       pinnedBy,
	})
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23503" {
		return nil, fmt.Errorf("set pin: %w", registry.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("set pin: %w", err)
	}
	row, err := q.GetPin(ctx, bucketName)
	if err != nil {
		return nil, mapNotFound("get pin after set", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit set pin: %w", err)
	}
	return &Pin{BucketName: row.BucketName, PinnedAt: row.PinnedAt, PinnedBy: row.PinnedBy}, nil
}

func (r *Repository) DeletePin(ctx context.Context, tenant Tenant, bucketName string) error {
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := q.DeletePin(ctx, bucketName); err != nil {
		return fmt.Errorf("delete pin: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete pin: %w", err)
	}
	return nil
}
