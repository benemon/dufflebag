package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/google/uuid"
)

// pgx renders timestamptz into Go's local location, so the same row reads back
// with a host-dependent offset. Every timestamp leaving this repository is
// pinned to UTC: project created_at drives default tenant selection, and a
// comparison that depends on where the server runs is a latent bug.
func utc(t time.Time) time.Time { return t.UTC() }

// Organization is a tenancy root.
type Organization struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

// Project is one tenant within an organization.
type Project struct {
	ID             string
	OrganizationID string
	Name           string
	CreatedAt      time.Time
}

// ListOrganizationsForPrincipal returns only the organisations a caller may see.
//
// Filtering lives here rather than in the handler because a handler that forgets
// leaks every tenant identifier on the instance — and organisation identifiers
// are half of every compatibility-plane path, so they are the thing the rest of
// the system works to keep secret (ADR-0016).
//
// A platform-scoped caller sees everything; that is what platform scope means,
// and only root may hold it. Any other caller sees exactly its own organisation,
// so the answer is a lookup rather than a filtered scan.
//
// The argument is a principal rather than a bare scope because Scope's zero
// value is the MOST privileged one: a forgotten Scope{} used to hand this
// method platform scope by accident. A principal cannot be constructed with an
// invalid scope/role pairing (validBinding, pinned by
// TestPlatformScopeIsReachableOnlyWithRoot), so the caller must present a value
// that validated itself (duf-ueq).
func (r *Repository) ListOrganizationsForPrincipal(
	ctx context.Context, principal *identity.Principal,
) ([]Organization, error) {
	scope := principal.Scope
	if scope.PlatformScoped() {
		// Redundant with validBinding, and deliberately so: where a mistake
		// discloses every organisation on the instance, construction-time
		// validation gets a second assertion at the point of disclosure
		// (duf-ueq).
		if principal.Role != identity.RoleRoot {
			return nil, fmt.Errorf(
				"list organizations: principal %s holds platform scope without root", principal.ID,
			)
		}
		return r.listAllOrganizations(ctx)
	}
	if scope.OrganizationID == uuid.Nil {
		// A caller bound to no organisation sees none. Deny by default.
		return []Organization{}, nil
	}
	organization, err := r.GetOrganization(ctx, scope.OrganizationID.String())
	if errors.Is(err, registry.ErrNotFound) {
		// Its organisation has been deleted; its scope resolves to nothing,
		// which is not a failure (ADR-0016).
		return []Organization{}, nil
	}
	if err != nil {
		return nil, err
	}
	return []Organization{*organization}, nil
}

func (r *Repository) listAllOrganizations(ctx context.Context) ([]Organization, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, created_at
		FROM organizations
		ORDER BY created_at, id
	`)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	organizations := make([]Organization, 0)
	for rows.Next() {
		var organization Organization
		if err := rows.Scan(&organization.ID, &organization.Name, &organization.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan organization: %w", err)
		}
		organization.CreatedAt = organization.CreatedAt.UTC()
		organization.CreatedAt = utc(organization.CreatedAt)
		organizations = append(organizations, organization)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	return organizations, nil
}

func (r *Repository) CreateOrganization(
	ctx context.Context,
	organization Organization,
) (*Organization, error) {
	var created Organization
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO organizations (id, name, created_at)
		VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING
		RETURNING id, name, created_at
	`, organization.ID, organization.Name, organization.CreatedAt).Scan(
		&created.ID,
		&created.Name,
		&created.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("create organization: %w", registry.ErrConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("create organization: %w", err)
	}
	created.CreatedAt = utc(created.CreatedAt)
	return &created, nil
}

func (r *Repository) GetOrganization(ctx context.Context, id string) (*Organization, error) {
	var organization Organization
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, created_at
		FROM organizations
		WHERE id = $1
	`, id).Scan(&organization.ID, &organization.Name, &organization.CreatedAt)
	if err != nil {
		return nil, mapNotFound("get organization", err)
	}
	organization.CreatedAt = utc(organization.CreatedAt)
	return &organization, nil
}

func (r *Repository) DeleteOrganization(ctx context.Context, id string) error {
	var result string
	err := r.db.QueryRowContext(ctx, `
		WITH target AS (
			SELECT id FROM organizations WHERE id = $1
		),
		deleted AS (
			DELETE FROM organizations
			WHERE id = $1
			  AND NOT EXISTS (
				  SELECT 1 FROM projects WHERE organization_id = $1
			  )
			  AND NOT EXISTS (
				  SELECT 1 FROM principals
				  WHERE organization_id = $1 AND project_id IS NULL
			  )
			RETURNING id
		)
		SELECT CASE
			WHEN EXISTS (SELECT 1 FROM deleted) THEN 'deleted'
			WHEN EXISTS (SELECT 1 FROM target) THEN 'conflict'
			ELSE 'not_found'
		END
	`, id).Scan(&result)
	if err != nil {
		return fmt.Errorf("delete organization: %w", err)
	}
	switch result {
	case "deleted":
		return nil
	case "conflict":
		return fmt.Errorf("delete organization: %w", registry.ErrConflict)
	default:
		return fmt.Errorf("delete organization: %w", registry.ErrNotFound)
	}
}

func (r *Repository) ListProjects(ctx context.Context, organizationID string) ([]Project, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, organization_id, name, created_at
		FROM projects
		WHERE organization_id = $1
		ORDER BY created_at, id
	`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	projects := make([]Project, 0)
	for rows.Next() {
		var project Project
		if err := rows.Scan(
			&project.ID,
			&project.OrganizationID,
			&project.Name,
			&project.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		project.CreatedAt = utc(project.CreatedAt)
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return projects, nil
}

// ListProjectsForPrincipal returns only projects visible to the caller within
// the selected organization. Organization-scoped callers and platform root see
// the organization collection; a project-scoped caller sees only its binding.
func (r *Repository) ListProjectsForPrincipal(
	ctx context.Context, principal *identity.Principal, organization uuid.UUID,
) ([]Project, error) {
	organizationID := organization.String()
	if principal.Scope.PlatformScoped() {
		if principal.Role != identity.RoleRoot {
			return nil, fmt.Errorf(
				"list projects for principal: principal %s holds platform scope without root", principal.ID,
			)
		}
		return r.ListProjects(ctx, organizationID)
	}
	if !principal.Scope.WithinOrganization(organization) {
		return nil, fmt.Errorf(
			"list projects for principal: principal %s is not visible within organization %s",
			principal.ID, organizationID,
		)
	}
	if principal.Scope.OrganizationScoped() {
		return r.ListProjects(ctx, organizationID)
	}
	project, err := r.GetProject(ctx, organizationID, principal.Scope.ProjectID.String())
	if errors.Is(err, registry.ErrNotFound) {
		return []Project{}, nil
	}
	if err != nil {
		return nil, err
	}
	return []Project{*project}, nil
}

func (r *Repository) CreateProject(ctx context.Context, project Project) (*Project, error) {
	var created Project
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO projects (id, organization_id, name, created_at)
		SELECT $1, $2, $3, $4
		FROM organizations
		WHERE id = $2
		ON CONFLICT DO NOTHING
		RETURNING id, organization_id, name, created_at
	`, project.ID, project.OrganizationID, project.Name, project.CreatedAt).Scan(
		&created.ID,
		&created.OrganizationID,
		&created.Name,
		&created.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("create project: %w", registry.ErrConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}
	created.CreatedAt = utc(created.CreatedAt)
	return &created, nil
}

func (r *Repository) GetProject(
	ctx context.Context,
	organizationID, projectID string,
) (*Project, error) {
	var project Project
	err := r.db.QueryRowContext(ctx, `
		SELECT id, organization_id, name, created_at
		FROM projects
		WHERE organization_id = $1 AND id = $2
	`, organizationID, projectID).Scan(
		&project.ID,
		&project.OrganizationID,
		&project.Name,
		&project.CreatedAt,
	)
	if err != nil {
		return nil, mapNotFound("get project", err)
	}
	project.CreatedAt = utc(project.CreatedAt)
	return &project, nil
}

func (r *Repository) DeleteProject(ctx context.Context, organizationID, projectID string) error {
	tx, err := BeginTenant(ctx, r.db, organizationID, projectID, "")
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var result string
	err = tx.QueryRowContext(ctx, `
		WITH target AS (
			SELECT id
			FROM projects
			WHERE organization_id = $1 AND id = $2
		),
		deleted AS (
			DELETE FROM projects
			WHERE organization_id = $1
			  AND id = $2
			  AND NOT EXISTS (
				  SELECT 1
				  FROM buckets
				  WHERE organization_id = $1 AND project_id = $2
			  )
			  AND NOT EXISTS (
				  SELECT 1
				  FROM principals
				  WHERE organization_id = $1 AND project_id = $2
			  )
			RETURNING id
		)
		SELECT CASE
			WHEN EXISTS (SELECT 1 FROM deleted) THEN 'deleted'
			WHEN EXISTS (SELECT 1 FROM target) THEN 'conflict'
			ELSE 'not_found'
		END
	`, organizationID, projectID).Scan(&result)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	switch result {
	case "deleted":
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit delete project: %w", err)
		}
		return nil
	case "conflict":
		return fmt.Errorf("delete project: %w", registry.ErrConflict)
	default:
		return fmt.Errorf("delete project: %w", registry.ErrNotFound)
	}
}
