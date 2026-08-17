package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/google/uuid"
)

// ListAuditTargets returns the configured targets in slot order.
//
// Slot orders the result and is then discarded: it exists so a fourth target
// cannot be written (the baseline's three-slot constraint), not so a caller
// can reason about it.
func (r *Repository) ListAuditTargets(ctx context.Context) ([]identity.AuditTarget, error) {
	rows, err := postgresdb.New(r.db).ListAuditTargets(ctx)
	if err != nil {
		return nil, fmt.Errorf("list audit targets: %w", err)
	}
	targets := make([]identity.AuditTarget, 0, len(rows))
	for _, row := range rows {
		targets = append(targets, identity.AuditTarget{
			ID:        row.ID.String(),
			Path:      row.Path,
			CreatedAt: row.CreatedAt,
		})
	}
	return targets, nil
}

// CreateAuditTarget writes a target into the lowest free slot.
//
// The advisory lock serializes slot selection, so two concurrent creates cannot
// read the same free slot. It is held for the transaction, and the transaction
// does nothing but this insert, so the contention window is a single statement
// on a table of at most three rows.
//
// No retry. An earlier version retried a full table three times, on the theory
// that a concurrent delete might free a slot — but delete does not take this
// lock, so three immediate attempts bore no relation to when a delete would
// commit. It was a loop that could only turn a truthful conflict into a slower
// truthful conflict. A caller that wants a slot freed deletes one and asks
// again.
func (r *Repository) CreateAuditTarget(
	ctx context.Context,
	id string,
	path string,
	createdAt time.Time,
) (identity.AuditTarget, error) {
	targetID, err := uuid.Parse(id)
	if err != nil {
		return identity.AuditTarget{}, fmt.Errorf("parse audit target id: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return identity.AuditTarget{}, fmt.Errorf("begin create audit target: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	q := postgresdb.New(tx)
	if err := q.LockAuditTargetSlots(ctx); err != nil {
		return identity.AuditTarget{}, fmt.Errorf("lock audit target slots: %w", err)
	}

	row, err := q.CreateAuditTarget(ctx, postgresdb.CreateAuditTargetParams{
		ID:        targetID,
		Path:      path,
		CreatedAt: createdAt,
	})
	// No row means either that all three slots are taken, so the sub-select was
	// empty, or that ON CONFLICT DO NOTHING swallowed a duplicate id or slot.
	// Both are conflicts, and neither is a driver error worth surfacing.
	if errors.Is(err, sql.ErrNoRows) {
		return identity.AuditTarget{}, fmt.Errorf("create audit target: %w", registry.ErrConflict)
	}
	if err != nil {
		return identity.AuditTarget{}, fmt.Errorf("create audit target: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return identity.AuditTarget{}, fmt.Errorf("commit create audit target: %w", err)
	}
	return identity.AuditTarget{
		ID:        row.ID.String(),
		Path:      row.Path,
		CreatedAt: row.CreatedAt,
	}, nil
}

// DeleteAuditTarget removes a target, freeing its slot for the next create.
func (r *Repository) DeleteAuditTarget(ctx context.Context, id string) error {
	targetID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("parse audit target id: %w", err)
	}
	rows, err := postgresdb.New(r.db).DeleteAuditTarget(ctx, targetID)
	if err != nil {
		return fmt.Errorf("delete audit target: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("delete audit target: %w", registry.ErrNotFound)
	}
	return nil
}
