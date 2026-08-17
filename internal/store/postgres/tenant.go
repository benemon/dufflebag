package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

// BeginTenant starts a transaction whose RLS scope cannot escape to the pool.
// bucketID narrows the scope to one bucket; empty means the whole project.
// Every caller states it so an unscoped transaction is a visible decision,
// not a forgotten argument.
func BeginTenant(ctx context.Context, db *sql.DB, organizationID, projectID, bucketID string) (*sql.Tx, error) {
	if organizationID == "" || projectID == "" {
		return nil, fmt.Errorf("set tenant: organization and project are required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tenant transaction: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`SELECT set_config('app.tenant_org', $1, true), set_config('app.tenant_project', $2, true), set_config('app.tenant_bucket', $3, true)`,
		organizationID,
		projectID,
		bucketID,
	); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("set tenant: %w", err)
	}
	return tx, nil
}
