package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/google/uuid"
)

var (
	ErrPluginRegistryAlreadyEnabled = errors.New("plugin registry already enabled")
	ErrPluginRegistryNotEnabled     = errors.New("plugin registry not enabled")
	ErrPluginRegistryAlreadyExposed = errors.New("plugin registry already exposed")
	ErrPluginRegistryNotExposed     = errors.New("plugin registry not exposed")
	ErrPluginRegistryStillExposed   = errors.New("plugin registry still exposed")
)

// OrganizationTenant is the RLS scope for organization-level repository operations.
type OrganizationTenant struct {
	OrganizationID uuid.UUID
	denied         bool
	malformed      bool
}

func DeniedOrganizationTenant() OrganizationTenant {
	return OrganizationTenant{denied: true}
}

func ParseOrganizationTenant(organizationID string) OrganizationTenant {
	organization, err := uuid.Parse(organizationID)
	if err != nil {
		return OrganizationTenant{malformed: true}
	}
	return OrganizationTenant{OrganizationID: organization}
}

// PluginRegistry is the organization registry lifecycle state.
type PluginRegistry struct {
	Enabled bool
	Exposed bool
}

func (r *Repository) GetPluginRegistry(
	ctx context.Context, tenant OrganizationTenant,
) (PluginRegistry, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return PluginRegistry{}, err
	}
	defer func() { _ = tx.Rollback() }()

	exposed, err := q.GetPluginRegistry(ctx, tenant.OrganizationID)
	if errors.Is(err, sql.ErrNoRows) {
		return PluginRegistry{}, nil
	}
	if err != nil {
		return PluginRegistry{}, fmt.Errorf("get plugin registry: %w", err)
	}
	return PluginRegistry{Enabled: true, Exposed: exposed}, nil
}

func (r *Repository) EnablePluginRegistry(
	ctx context.Context, tenant OrganizationTenant,
) (PluginRegistry, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return PluginRegistry{}, err
	}
	defer func() { _ = tx.Rollback() }()

	exposed, err := q.EnablePluginRegistry(ctx, tenant.OrganizationID)
	if errors.Is(err, sql.ErrNoRows) {
		return PluginRegistry{}, ErrPluginRegistryAlreadyEnabled
	}
	if err != nil {
		return PluginRegistry{}, fmt.Errorf("enable plugin registry: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PluginRegistry{}, fmt.Errorf("commit enable plugin registry: %w", err)
	}
	return PluginRegistry{Enabled: true, Exposed: exposed}, nil
}

func (r *Repository) ExposePluginRegistry(
	ctx context.Context, tenant OrganizationTenant,
) (PluginRegistry, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return PluginRegistry{}, err
	}
	defer func() { _ = tx.Rollback() }()

	exposed, err := q.ExposePluginRegistry(ctx, tenant.OrganizationID)
	if errors.Is(err, sql.ErrNoRows) {
		_, getErr := q.GetPluginRegistry(ctx, tenant.OrganizationID)
		if errors.Is(getErr, sql.ErrNoRows) {
			return PluginRegistry{}, ErrPluginRegistryNotEnabled
		}
		if getErr != nil {
			return PluginRegistry{}, fmt.Errorf("get plugin registry after expose conflict: %w", getErr)
		}
		return PluginRegistry{}, ErrPluginRegistryAlreadyExposed
	}
	if err != nil {
		return PluginRegistry{}, fmt.Errorf("expose plugin registry: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PluginRegistry{}, fmt.Errorf("commit expose plugin registry: %w", err)
	}
	return PluginRegistry{Enabled: true, Exposed: exposed}, nil
}

func (r *Repository) UnexposePluginRegistry(
	ctx context.Context, tenant OrganizationTenant,
) (PluginRegistry, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return PluginRegistry{}, err
	}
	defer func() { _ = tx.Rollback() }()

	exposed, err := q.UnexposePluginRegistry(ctx, tenant.OrganizationID)
	if errors.Is(err, sql.ErrNoRows) {
		_, getErr := q.GetPluginRegistry(ctx, tenant.OrganizationID)
		if errors.Is(getErr, sql.ErrNoRows) {
			return PluginRegistry{}, ErrPluginRegistryNotEnabled
		}
		if getErr != nil {
			return PluginRegistry{}, fmt.Errorf("get plugin registry after unexpose conflict: %w", getErr)
		}
		return PluginRegistry{}, ErrPluginRegistryNotExposed
	}
	if err != nil {
		return PluginRegistry{}, fmt.Errorf("unexpose plugin registry: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PluginRegistry{}, fmt.Errorf("commit unexpose plugin registry: %w", err)
	}
	return PluginRegistry{Enabled: true, Exposed: exposed}, nil
}

// DisablePluginRegistry removes the registry and everything in it. The row
// lock holds off a concurrent expose until the rows are gone; blobs follow
// the commit so a failure can only orphan them.
func (r *Repository) DisablePluginRegistry(ctx context.Context, tenant OrganizationTenant) error {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := q.LockUnexposedPluginRegistry(ctx, tenant.OrganizationID); errors.Is(err, sql.ErrNoRows) {
		exposed, getErr := q.GetPluginRegistry(ctx, tenant.OrganizationID)
		if errors.Is(getErr, sql.ErrNoRows) {
			return ErrPluginRegistryNotEnabled
		}
		if getErr != nil {
			return fmt.Errorf("get plugin registry after disable conflict: %w", getErr)
		}
		if exposed {
			return ErrPluginRegistryStillExposed
		}
		return ErrPluginRegistryNotEnabled
	} else if err != nil {
		return fmt.Errorf("lock plugin registry: %w", err)
	}
	keys, err := q.ListPluginObjectKeys(ctx, tenant.OrganizationID)
	if err != nil {
		return fmt.Errorf("list plugin objects: %w", err)
	}
	if err := q.DeletePluginVersions(ctx, tenant.OrganizationID); err != nil {
		return fmt.Errorf("delete plugin versions: %w", err)
	}
	if err := q.DeletePlugins(ctx, tenant.OrganizationID); err != nil {
		return fmt.Errorf("delete plugins: %w", err)
	}
	if _, err := q.DisablePluginRegistry(ctx, tenant.OrganizationID); err != nil {
		return fmt.Errorf("disable plugin registry: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit disable plugin registry: %w", err)
	}
	r.deletePluginObjects(ctx, keys)
	return nil
}

func (r *Repository) beginOrganization(
	ctx context.Context, tenant OrganizationTenant,
) (*sql.Tx, *postgresdb.Queries, error) {
	if tenant.malformed || tenant.denied {
		return nil, nil, fmt.Errorf("%w: organization tenant", registry.ErrNotFound)
	}
	tx, err := BeginOrganizationTenant(ctx, r.db, tenant.OrganizationID.String())
	if err != nil {
		return nil, nil, err
	}
	return tx, postgresdb.New(tx), nil
}
