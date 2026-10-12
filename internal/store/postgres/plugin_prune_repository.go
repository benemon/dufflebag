package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
)

// PluginPrune is an organization's job-pruning schedule and what a run would
// do now: WouldClear counts the finished jobs older than the age limit.
type PluginPrune struct {
	Enabled          bool
	AgeDays          int
	Schedule         string
	NextAt           *time.Time
	LastRunAt        *time.Time
	LastCleared      int
	LastError        string
	WouldClear       int
	Unfinished       int
	Kept             int
	OldestFinishedAt *time.Time
}

// ClaimedPluginPrune is a run a worker now owns.
type ClaimedPluginPrune struct {
	Tenant   OrganizationTenant
	AgeDays  int
	Schedule string
}

func (r *Repository) GetPluginPrune(ctx context.Context, tenant OrganizationTenant) (PluginPrune, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return PluginPrune{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.GetPluginPrune(ctx, tenant.OrganizationID)
	if errors.Is(err, sql.ErrNoRows) {
		return PluginPrune{}, ErrPluginRegistryNotEnabled
	}
	if err != nil {
		return PluginPrune{}, fmt.Errorf("get plugin prune: %w", err)
	}
	prune := PluginPrune{
		Enabled: row.PruneEnabled, AgeDays: int(row.PruneAgeDays), Schedule: row.PruneSchedule,
		LastCleared: int(row.PruneLastCleared), LastError: row.PruneLastError,
		WouldClear: int(row.WouldClear), Unfinished: int(row.Unfinished), Kept: int(row.Kept),
	}
	prune.NextAt = nullTime(row.PruneNextAt)
	prune.LastRunAt = nullTime(row.PruneLastRunAt)
	// COALESCE in the query stands in for NULL with the zero time, since sqlc types the aggregate as non-null.
	if !row.OldestFinishedAt.IsZero() {
		oldest := row.OldestFinishedAt
		prune.OldestFinishedAt = &oldest
	}
	return prune, nil
}

func nullTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}

// SetPluginPrune stores the schedule; nextAt is nil when pruning is off.
func (r *Repository) SetPluginPrune(ctx context.Context, tenant OrganizationTenant, enabled bool, ageDays int, schedule string, nextAt *time.Time) error {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := q.GetPluginRegistry(ctx, tenant.OrganizationID); errors.Is(err, sql.ErrNoRows) {
		return ErrPluginRegistryNotEnabled
	} else if err != nil {
		return fmt.Errorf("get plugin registry: %w", err)
	}
	if err := q.SetPluginPrune(ctx, postgresdb.SetPluginPruneParams{
		OrganizationID: tenant.OrganizationID, PruneEnabled: enabled, PruneAgeDays: int32(ageDays), PruneSchedule: schedule, PruneNextAt: toNullTime(nextAt),
	}); err != nil {
		return fmt.Errorf("set plugin prune: %w", err)
	}
	return tx.Commit()
}

func toNullTime(value *time.Time) sql.NullTime {
	if value == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *value, Valid: true}
}

// ClaimPluginPrune hands one organization whose run is due to the caller and
// stamps it run in the same transaction, so replicas never prune one
// organization twice. The claim also leases the next run an hour out, so a
// run that dies before recording fires again rather than never.
// Organizations are enumerated as ClaimPluginImport does.
func (r *Repository) ClaimPluginPrune(ctx context.Context) (*ClaimedPluginPrune, error) {
	organizations, err := postgresdb.New(r.db).ListOrganizationIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	for _, organization := range organizations {
		claimed, err := r.claimPluginPruneFor(ctx, OrganizationTenant{OrganizationID: organization})
		if err != nil || claimed != nil {
			return claimed, err
		}
	}
	return nil, nil
}

func (r *Repository) claimPluginPruneFor(ctx context.Context, tenant OrganizationTenant) (*ClaimedPluginPrune, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.ClaimPluginPrune(ctx, tenant.OrganizationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim plugin prune: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit plugin prune claim: %w", err)
	}
	return &ClaimedPluginPrune{Tenant: tenant, AgeDays: int(row.PruneAgeDays), Schedule: row.PruneSchedule}, nil
}

// PrunePluginImports clears the finished jobs that finished before the
// cutoff and reports how many; queued and running jobs are never touched.
func (r *Repository) PrunePluginImports(ctx context.Context, tenant OrganizationTenant, before time.Time) (int, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	cleared, err := q.PrunePluginImports(ctx, postgresdb.PrunePluginImportsParams{OrganizationID: tenant.OrganizationID, Before: before})
	if err != nil {
		return 0, fmt.Errorf("prune plugin imports: %w", err)
	}
	return int(cleared), tx.Commit()
}

// RecordPluginPrune stores a run's outcome and when the next one is due.
func (r *Repository) RecordPluginPrune(ctx context.Context, tenant OrganizationTenant, cleared int, runErr string, nextAt *time.Time) error {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := q.RecordPluginPrune(ctx, postgresdb.RecordPluginPruneParams{
		OrganizationID: tenant.OrganizationID, PruneLastCleared: int32(cleared), PruneLastError: runErr, PruneNextAt: toNullTime(nextAt),
	}); err != nil {
		return fmt.Errorf("record plugin prune: %w", err)
	}
	return tx.Commit()
}
