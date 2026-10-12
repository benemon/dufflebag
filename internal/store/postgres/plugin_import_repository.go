package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/google/uuid"
)

// PluginImportRequest is what an operator asked an import to do: mirror
// versions on the given platforms, or apply an ordered list of changes.
type PluginImportRequest struct {
	SourceKind string
	Product    string
	Versions   []string
	Platforms  []string
	Changes    []PluginChange
}

// PluginChange is one change a sync applies to one version.
type PluginChange struct {
	Version   string   `json:"version"`
	Action    string   `json:"action"`
	Platforms []string `json:"platforms,omitempty"`
}

func encodeChanges(changes []PluginChange) (json.RawMessage, error) {
	if changes == nil {
		changes = []PluginChange{}
	}
	return json.Marshal(changes)
}

func decodeChanges(raw json.RawMessage) ([]PluginChange, error) {
	var changes []PluginChange
	if err := json.Unmarshal(raw, &changes); err != nil {
		return nil, fmt.Errorf("decode plugin import changes: %w", err)
	}
	return changes, nil
}

// PluginImportOrigin is who queued a job and from where: an import form,
// a plugin's page, the catalogue's Sync selected (one job of a batch), or
// an upload.
type PluginImportOrigin struct {
	CreatedBy  string
	Kind       string
	BatchIndex int
	BatchSize  int
	RetryOf    *uuid.UUID
}

// PluginImport is a recorded import job.
type PluginImport struct {
	ID          uuid.UUID
	Plugin      string
	Request     PluginImportRequest
	Origin      PluginImportOrigin
	State       string
	Outcomes    json.RawMessage
	CreatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	QueuedAhead int
}

// PluginImportFilter narrows a job listing. Empty States means every state.
type PluginImportFilter struct {
	States []string
	Plugin string
	Limit  int
	Offset int
}

// PluginNameOf is the plugin a product names: packer-plugin-amazon and
// acme/packer-plugin-amazon are both amazon; an uploaded plugin's product
// is its name already.
func PluginNameOf(product string) string {
	if _, after, found := strings.Cut(product, "/"); found {
		product = after
	}
	return strings.TrimPrefix(product, "packer-plugin-")
}

// ClaimedPluginImport is a job a worker now owns.
type ClaimedPluginImport struct {
	Tenant  OrganizationTenant
	ID      uuid.UUID
	Request PluginImportRequest
}

// CreatePluginImport queues an import. The registry must be enabled.
func (r *Repository) CreatePluginImport(
	ctx context.Context, tenant OrganizationTenant, request PluginImportRequest, origin PluginImportOrigin,
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
	changes, err := encodeChanges(request.Changes)
	if err != nil {
		return uuid.Nil, err
	}
	var retryOf uuid.NullUUID
	if origin.RetryOf != nil {
		retryOf = uuid.NullUUID{UUID: *origin.RetryOf, Valid: true}
	}
	if err := q.InsertPluginImport(ctx, postgresdb.InsertPluginImportParams{
		ID: id, OrganizationID: tenant.OrganizationID, SourceKind: request.SourceKind, Product: request.Product, Plugin: PluginNameOf(request.Product),
		Versions: nonNil(request.Versions), Platforms: nonNil(request.Platforms), Changes: changes,
		CreatedBy: origin.CreatedBy, Origin: origin.Kind, BatchIndex: int32(max(origin.BatchIndex, 1)), BatchSize: int32(max(origin.BatchSize, 1)), RetryOf: retryOf,
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
	return pluginImportOf(postgresdb.ListPluginImportsRow(row))
}

// ListPluginImports is one page of the organization's jobs, newest first,
// with the count of every job the filter matches.
func (r *Repository) ListPluginImports(ctx context.Context, tenant OrganizationTenant, filter PluginImportFilter) ([]PluginImport, int, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := q.ListPluginImports(ctx, postgresdb.ListPluginImportsParams{
		OrganizationID: tenant.OrganizationID, States: nonNil(filter.States), Plugin: filter.Plugin,
		PageSize: int32(filter.Limit), PageOffset: int32(filter.Offset),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list plugin imports: %w", err)
	}
	jobs := make([]PluginImport, 0, len(rows))
	total := 0
	for _, row := range rows {
		job, err := pluginImportOf(row)
		if err != nil {
			return nil, 0, err
		}
		jobs = append(jobs, job)
		total = int(row.Total)
	}
	if len(rows) == 0 && filter.Offset > 0 {
		total, err = r.countPluginImports(ctx, q, tenant, filter)
		if err != nil {
			return nil, 0, err
		}
	}
	return jobs, total, nil
}

// A page past the end has no row to carry the window count.
func (r *Repository) countPluginImports(ctx context.Context, q *postgresdb.Queries, tenant OrganizationTenant, filter PluginImportFilter) (int, error) {
	rows, err := q.ListPluginImports(ctx, postgresdb.ListPluginImportsParams{
		OrganizationID: tenant.OrganizationID, States: nonNil(filter.States), Plugin: filter.Plugin, PageSize: 1, PageOffset: 0,
	})
	if err != nil {
		return 0, fmt.Errorf("count plugin imports: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return int(rows[0].Total), nil
}

// GetPluginImport and ListPluginImports select the same columns; the Get
// row is converted so one mapping serves both.
func pluginImportOf(row postgresdb.ListPluginImportsRow) (PluginImport, error) {
	changes, err := decodeChanges(row.Changes)
	if err != nil {
		return PluginImport{}, err
	}
	job := PluginImport{
		ID: row.ID, Plugin: row.Plugin, State: row.State, Outcomes: row.Outcomes, CreatedAt: row.CreatedAt, QueuedAhead: int(row.QueuedAhead),
		Request: PluginImportRequest{SourceKind: row.SourceKind, Product: row.Product, Versions: row.Versions, Platforms: row.Platforms, Changes: changes},
		Origin:  PluginImportOrigin{CreatedBy: row.CreatedBy, Kind: row.Origin, BatchIndex: int(row.BatchIndex), BatchSize: int(row.BatchSize)},
	}
	if row.RetryOf.Valid {
		retryOf := row.RetryOf.UUID
		job.Origin.RetryOf = &retryOf
	}
	if row.StartedAt.Valid {
		job.StartedAt = &row.StartedAt.Time
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
	changes, err := decodeChanges(row.Changes)
	if err != nil {
		return nil, err
	}
	return &ClaimedPluginImport{
		Tenant: tenant, ID: row.ID,
		Request: PluginImportRequest{SourceKind: row.SourceKind, Product: row.Product, Versions: row.Versions, Platforms: row.Platforms, Changes: changes},
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

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
