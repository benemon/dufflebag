package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

// AssertRLSApplies refuses to serve as a role that row-level security cannot
// constrain.
//
// FORCE ROW LEVEL SECURITY covers the table owner, but NOT a superuser and not
// a role holding BYPASSRLS. Connecting as either — pointing DFBG_DATABASE_URL at the
// stock `postgres` user is the commonest way — silently disables every tenancy
// policy: every tenant sees every tenant, nothing errors, nothing logs, and the
// isolation tests still pass because they create their own unprivileged role.
//
// ADR-0017 requires misconfiguration to fail closed. This one fails silently
// OPEN, which is why it is checked at startup rather than left to be noticed.
func AssertRLSApplies(ctx context.Context, db *sql.DB) error {
	var bypasses bool
	err := db.QueryRowContext(ctx, `
		SELECT rolsuper OR rolbypassrls
		FROM pg_roles
		WHERE rolname = current_user
	`).Scan(&bypasses)
	if err != nil {
		return fmt.Errorf("check database role privileges: %w", err)
	}
	if bypasses {
		var role string
		_ = db.QueryRowContext(ctx, `SELECT current_user`).Scan(&role)
		return fmt.Errorf(
			"database role %q is a superuser or has BYPASSRLS, so row-level security "+
				"would not apply and every tenant would see every other tenant; "+
				"connect as an unprivileged role with SELECT, INSERT, UPDATE and DELETE "+
				"on the application tables", role,
		)
	}
	return nil
}
