package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/benemon/dufflebag/internal/keyring"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
)

// EncryptionMode reads the stored mode marker. recorded=false means no boot
// has stamped one yet.
func (r *Repository) EncryptionMode(ctx context.Context) (recorded, encrypted bool, err error) {
	encrypted, err = postgresdb.New(r.db).GetEncryptionMode(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("read encryption mode: %w", err)
	}
	return true, encrypted, nil
}

// RecordEncryptionMode stamps the configured mode at first boot. The insert is
// idempotent; the caller re-reads and compares, so two racing first boots
// cannot each believe their own answer.
func (r *Repository) RecordEncryptionMode(ctx context.Context, encrypted bool, at time.Time) error {
	if _, err := postgresdb.New(r.db).RecordEncryptionMode(ctx, postgresdb.RecordEncryptionModeParams{
		Encrypted: encrypted, RecordedAt: at.UTC(),
	}); err != nil {
		return fmt.Errorf("record encryption mode: %w", err)
	}
	return nil
}

// ListKeyringEntries reads the wrapped keyring for unwrapping at startup.
func (r *Repository) ListKeyringEntries(ctx context.Context) ([]keyring.Entry, error) {
	rows, err := postgresdb.New(r.db).ListKeyringEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("list keyring: %w", err)
	}
	entries := make([]keyring.Entry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, keyring.Entry{
			Purpose: row.Purpose, Version: uint32(row.Version),
			Wrapped: row.WrappedKey, KEKRef: row.KekRef, WrappedAt: row.CreatedAt,
		})
	}
	return entries, nil
}

// RewrapKeyringEntries replaces every wrapped blob in one transaction.
func (r *Repository) RewrapKeyringEntries(ctx context.Context, entries []keyring.Entry, at time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin keyring rewrap: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := postgresdb.New(tx)
	for _, entry := range entries {
		updated, err := q.RewrapKeyringEntry(ctx, postgresdb.RewrapKeyringEntryParams{
			Purpose: entry.Purpose, Version: int32(entry.Version), WrappedKey: entry.Wrapped,
			KekRef: entry.KEKRef, CreatedAt: at.UTC(),
		})
		if err != nil {
			return fmt.Errorf("rewrap keyring %s v%d: %w", entry.Purpose, entry.Version, err)
		}
		if updated != 1 {
			return fmt.Errorf("rewrap keyring %s v%d: row not found", entry.Purpose, entry.Version)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit keyring rewrap: %w", err)
	}
	return nil
}

// CreateKeyringEntries stores a freshly generated keyring in one transaction.
// It reports false — writing nothing — when any entry already exists: another
// replica won the first-boot race, and the caller must load that keyring
// instead of serving with its own losing copy.
func (r *Repository) CreateKeyringEntries(ctx context.Context, entries []keyring.Entry, at time.Time) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin keyring write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := postgresdb.New(tx)
	for _, entry := range entries {
		inserted, err := q.CreateKeyringEntry(ctx, postgresdb.CreateKeyringEntryParams{
			Purpose: entry.Purpose, Version: int32(entry.Version),
			WrappedKey: entry.Wrapped, KekRef: entry.KEKRef, CreatedAt: at.UTC(),
		})
		if err != nil {
			return false, fmt.Errorf("store keyring %s v%d: %w", entry.Purpose, entry.Version, err)
		}
		if inserted == 0 {
			return false, nil
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit keyring write: %w", err)
	}
	return true, nil
}
