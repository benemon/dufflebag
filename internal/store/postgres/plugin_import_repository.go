package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/google/uuid"
)

// PluginImportRequest is what an operator asked an import to mirror.
type PluginImportRequest struct {
	SourceKind string
	Product    string
	Versions   []string
	Platforms  []string
}

// PluginImport is a recorded import job.
type PluginImport struct {
	ID         uuid.UUID
	Request    PluginImportRequest
	State      string
	Outcomes   json.RawMessage
	CreatedAt  time.Time
	FinishedAt *time.Time
}

// ClaimedPluginImport is a job a worker now owns.
type ClaimedPluginImport struct {
	Tenant  OrganizationTenant
	ID      uuid.UUID
	Request PluginImportRequest
}

// CreatePluginImport queues an import. The registry must be enabled.
func (r *Repository) CreatePluginImport(
	ctx context.Context, tenant OrganizationTenant, request PluginImportRequest,
) (uuid.UUID, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := q.GetPluginRegistry(ctx, tenant.OrganizationID); errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, ErrPluginRegistryNotEnabled
	} else if err != nil {
		return uuid.Nil, fmt.Errorf("get plugin registry: %w", err)
	}
	id := uuid.New()
	if err := q.InsertPluginImport(ctx, postgresdb.InsertPluginImportParams{
		ID: id, OrganizationID: tenant.OrganizationID, SourceKind: request.SourceKind,
		Product: request.Product, Versions: request.Versions, Platforms: request.Platforms,
	}); err != nil {
		return uuid.Nil, fmt.Errorf("insert plugin import: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return uuid.Nil, fmt.Errorf("commit plugin import: %w", err)
	}
	return id, nil
}

// GetPluginImport reads one job of the organization.
func (r *Repository) GetPluginImport(ctx context.Context, tenant OrganizationTenant, id uuid.UUID) (PluginImport, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return PluginImport{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.GetPluginImport(ctx, postgresdb.GetPluginImportParams{OrganizationID: tenant.OrganizationID, ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return PluginImport{}, fmt.Errorf("%w: plugin import %s", registry.ErrNotFound, id)
	}
	if err != nil {
		return PluginImport{}, fmt.Errorf("get plugin import: %w", err)
	}
	job := PluginImport{
		ID: row.ID, State: row.State, Outcomes: row.Outcomes, CreatedAt: row.CreatedAt,
		Request: PluginImportRequest{SourceKind: row.SourceKind, Product: row.Product, Versions: row.Versions, Platforms: row.Platforms},
	}
	if row.FinishedAt.Valid {
		job.FinishedAt = &row.FinishedAt.Time
	}
	return job, nil
}

// ClaimPluginImport hands the next queued job, or one whose worker stopped
// reporting for stale, to the caller. Organizations carry no RLS, so they are
// enumerated and each is claimed from inside its own tenant, as the scanner
// claims per project (ScannerService.claimNextForTenant). SKIP LOCKED keeps
// two workers, or two replicas, off the same job.
func (r *Repository) ClaimPluginImport(ctx context.Context, stale time.Duration) (*ClaimedPluginImport, error) {
	organizations, err := postgresdb.New(r.db).ListOrganizationIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	for _, organization := range organizations {
		claimed, err := r.claimPluginImportFor(ctx, OrganizationTenant{OrganizationID: organization}, stale)
		if err != nil || claimed != nil {
			return claimed, err
		}
	}
	return nil, nil
}

func (r *Repository) claimPluginImportFor(
	ctx context.Context, tenant OrganizationTenant, stale time.Duration,
) (*ClaimedPluginImport, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.ClaimPluginImport(ctx, postgresdb.ClaimPluginImportParams{
		OrganizationID: tenant.OrganizationID, StaleSeconds: stale.Seconds(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim plugin import: %w", err)
	}
	if err := q.MarkPluginImportRunning(ctx, row.ID); err != nil {
		return nil, fmt.Errorf("mark plugin import running: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit plugin import claim: %w", err)
	}
	return &ClaimedPluginImport{
		Tenant: tenant, ID: row.ID,
		Request: PluginImportRequest{SourceKind: row.SourceKind, Product: row.Product, Versions: row.Versions, Platforms: row.Platforms},
	}, nil
}

// RecordPluginImport stores a job's progress; a terminal state finishes it.
// Each record also renews the claim, so a working job is never stale.
func (r *Repository) RecordPluginImport(
	ctx context.Context, tenant OrganizationTenant, id uuid.UUID, state string, outcomes any,
) error {
	encoded, err := json.Marshal(outcomes)
	if err != nil {
		return fmt.Errorf("encode plugin import outcomes: %w", err)
	}
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := q.RecordPluginImport(ctx, postgresdb.RecordPluginImportParams{ID: id, State: state, Outcomes: encoded}); err != nil {
		return fmt.Errorf("record plugin import: %w", err)
	}
	return tx.Commit()
}

// PluginRegistryDefaultPlatforms are the platforms a first import selects.
func (r *Repository) PluginRegistryDefaultPlatforms(ctx context.Context, tenant OrganizationTenant) ([]string, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	platforms, err := q.GetPluginRegistryDefaultPlatforms(ctx, tenant.OrganizationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPluginRegistryNotEnabled
	}
	if err != nil {
		return nil, fmt.Errorf("get default platforms: %w", err)
	}
	return platforms, nil
}

// SetPluginRegistryDefaultPlatforms replaces the organization's defaults.
func (r *Repository) SetPluginRegistryDefaultPlatforms(
	ctx context.Context, tenant OrganizationTenant, platforms []string,
) ([]string, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	stored, err := q.SetPluginRegistryDefaultPlatforms(ctx, postgresdb.SetPluginRegistryDefaultPlatformsParams{
		OrganizationID: tenant.OrganizationID, DefaultPlatforms: platforms,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPluginRegistryNotEnabled
	}
	if err != nil {
		return nil, fmt.Errorf("set default platforms: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit default platforms: %w", err)
	}
	return stored, nil
}
