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
)

// InitializeInstance claims an uninitialized instance by creating its first
// platform root principal.
//
// The instance table is a transaction-scoped singleton lock, not the claimed
// predicate. Once one caller holds it, root existence is checked and the root
// is written before the lock is released. A second caller then observes that
// root and refuses. The last root cannot be deleted through the API, so the
// predicate is a one-way door without tying initialization to tenant lifetime.
func (r *Repository) InitializeInstance(
	ctx context.Context,
	principal *identity.Principal,
	recoveryDigest []byte,
	recoveryThreshold int,
) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin initialize: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	q := postgresdb.New(tx)
	if err := q.LockInitialization(ctx); err != nil {
		return fmt.Errorf("lock initialize: %w", err)
	}
	initialized, err := q.RootPrincipalExists(ctx)
	if err != nil {
		return fmt.Errorf("read initialization state: %w", err)
	}
	if initialized {
		return fmt.Errorf("initialize: %w", registry.ErrConflict)
	}

	if err := q.RecordInitialization(ctx, postgresdb.RecordInitializationParams{
		InitializedAt:     principal.CreatedAt.UTC(),
		RecoveryDigest:    recoveryDigest,
		RecoveryThreshold: sql.NullInt32{Int32: int32(recoveryThreshold), Valid: true},
	}); err != nil {
		return fmt.Errorf("record initialization: %w", err)
	}
	if err := r.createPrincipalTx(ctx, q, principal); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit initialize: %w", err)
	}
	return nil
}

// InstanceStatus answers the unauthenticated health probe: whether the
// instance is claimed, and whether the database responds at all.
//
// The failure reason stays server-side on the unauthenticated health path: the
// ping's error text carries host and driver detail (review finding 16).
func (r *Repository) InstanceStatus(
	ctx context.Context,
) (initialized bool, initializedAt *time.Time, database bool, err error) {
	if err := r.db.PingContext(ctx); err != nil {
		return false, nil, false, fmt.Errorf("ping database: %w", err)
	}
	q := postgresdb.New(r.db)
	initialized, err = q.RootPrincipalExists(ctx)
	if err != nil {
		return false, nil, false, fmt.Errorf("read initialization state: %w", err)
	}
	timestamp, err := q.GetInitializationTimestamp(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return initialized, nil, true, nil
	}
	if err != nil {
		return false, nil, false, fmt.Errorf("read initialization timestamp: %w", err)
	}
	timestamp = timestamp.UTC()
	return initialized, &timestamp, true, nil
}

// RecoveryVerifier reads the digest and threshold /sys/recovery checks shares
// against. identity.ErrNotFound covers both an unclaimed instance and one
// initialized before recovery existed: neither holds a verifier, and
// /sys/recovery refuses identically.
func (r *Repository) RecoveryVerifier(ctx context.Context) ([]byte, int, error) {
	row, err := postgresdb.New(r.db).GetRecoveryVerifier(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, fmt.Errorf("recovery verifier: %w", identity.ErrNotFound)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read recovery verifier: %w", err)
	}
	if len(row.RecoveryDigest) == 0 || !row.RecoveryThreshold.Valid {
		return nil, 0, fmt.Errorf("recovery verifier: %w", identity.ErrNotFound)
	}
	return row.RecoveryDigest, int(row.RecoveryThreshold.Int32), nil
}
