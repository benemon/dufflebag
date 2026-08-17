package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

var dummyPrincipal = func() *identity.Principal {
	secret, err := identity.RestoreSecret("dummy-secret", identity.DummySecretHash, time.Time{}, nil, nil)
	if err != nil {
		panic(fmt.Sprintf("restore dummy secret: %v", err))
	}
	principal, err := identity.RestorePrincipal(
		"dummy-principal",
		"dummy",
		"dummy",
		identity.Scope{OrganizationID: uuid.MustParse("00000000-0000-4000-8000-000000000001")},
		// Role is immaterial — this principal exists only to be verified against
		// on the miss path, and is never authorized on.
		identity.RoleReader,
		time.Time{},
		[]identity.Secret{secret},
	)
	if err != nil {
		panic(fmt.Sprintf("restore dummy principal: %v", err))
	}
	return principal
}()

// TouchSecretLastUsed records successful credential use. last_used_at is not
// covered by principalSecretMACMessage, so no re-seal is needed.
func (r *Repository) TouchSecretLastUsed(ctx context.Context, secretID string, at time.Time) error {
	if err := postgresdb.New(r.db).TouchSecretLastUsed(ctx, postgresdb.TouchSecretLastUsedParams{
		ID: secretID, LastUsedAt: sql.NullTime{Time: at, Valid: true},
	}); err != nil {
		return fmt.Errorf("touch principal secret last used: %w", err)
	}
	return nil
}

// CreatePrincipal persists a principal and all of its active secrets atomically.
func (r *Repository) CreatePrincipal(ctx context.Context, principal *identity.Principal) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create principal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := r.createPrincipalTx(ctx, postgresdb.New(tx), principal); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create principal: %w", err)
	}
	return nil
}

// GetPrincipalByClientID loads a principal before its tenant is known.
func (r *Repository) GetPrincipalByClientID(ctx context.Context, clientID string) (*identity.Principal, error) {
	if clientID == identity.SystemScannerPrincipalID {
		_, _ = dummyPrincipal.Authenticate(clientID, time.Now().UTC())
		return nil, fmt.Errorf("get principal: %w", identity.ErrNotFound)
	}
	q := postgresdb.New(r.db)
	row, err := q.GetPrincipalByClientID(ctx, clientID)
	if errors.Is(err, sql.ErrNoRows) {
		// Deliberately spend one production-cost argon2id verification before
		// returning. Otherwise unknown client ids skip the expensive work and
		// become distinguishable from ids that reach credential verification.
		// This lookup has no credential argument; candidate contents do not
		// affect the argon2id cost.
		_, _ = dummyPrincipal.Authenticate(clientID, time.Now().UTC())
		return nil, fmt.Errorf("get principal: %w", identity.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get principal: %w", err)
	}
	return r.restorePrincipal(ctx, q, row)
}

// restorePrincipal rebuilds the domain aggregate from a row, refusing anything
// the domain would not have produced.
//
// On encrypted deployments the row and its secrets must carry valid keyring
// MACs (ADR-0024): a principal inserted by hand fails here, at every use, so
// database write access is not administration. The refusal error is the
// identity domain's, so authentication surfaces can audit it distinctly while
// answering the wire indistinguishably.
func (r *Repository) restorePrincipal(
	ctx context.Context, q *postgresdb.Queries, row postgresdb.Principal,
) (*identity.Principal, error) {
	if r.ring != nil {
		if err := r.ring.VerifyMAC(row.IntegrityMac, principalMACMessage(
			row.ID, row.ClientID, row.OrganizationID, row.ProjectID, row.BucketID, row.Role,
		)); err != nil {
			return nil, fmt.Errorf("restore principal %s: %w", row.ID, identity.ErrIntegrity)
		}
	}
	if (row.ProjectID.Valid && row.ProjectID.UUID == uuid.Nil) ||
		(row.OrganizationID.Valid && row.OrganizationID.UUID == uuid.Nil) {
		return nil, fmt.Errorf(
			"restore principal: %w: stored principal %s has a zero tenancy id",
			identity.ErrInvalid,
			row.ID,
		)
	}
	role, err := identity.ParseRole(row.Role)
	if err != nil {
		return nil, fmt.Errorf("restore principal %s: %w", row.ID, err)
	}

	secretRows, err := q.ListPrincipalSecrets(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("list principal secrets: %w", err)
	}
	secrets := make([]identity.Secret, 0, len(secretRows))
	for _, secretRow := range secretRows {
		if r.ring != nil {
			if err := r.ring.VerifyMAC(secretRow.IntegrityMac, principalSecretMACMessage(
				secretRow.ID, row.ID, secretRow.EncodedHash, secretRow.ExpiresAt,
			)); err != nil {
				return nil, fmt.Errorf("restore principal secret %s: %w", secretRow.ID, identity.ErrIntegrity)
			}
		}
		var lastUsedAt *time.Time
		if secretRow.LastUsedAt.Valid {
			value := utc(secretRow.LastUsedAt.Time)
			lastUsedAt = &value
		}
		var expiresAt *time.Time
		if secretRow.ExpiresAt.Valid {
			value := utc(secretRow.ExpiresAt.Time)
			expiresAt = &value
		}
		secret, err := identity.RestoreSecret(
			secretRow.ID,
			secretRow.EncodedHash,
			utc(secretRow.CreatedAt),
			lastUsedAt,
			expiresAt,
		)
		if err != nil {
			return nil, fmt.Errorf("restore principal secret: %w", err)
		}
		secrets = append(secrets, secret)
	}

	scope := identity.Scope{}
	if row.OrganizationID.Valid {
		scope.OrganizationID = row.OrganizationID.UUID
	}
	if row.ProjectID.Valid {
		scope.ProjectID = row.ProjectID.UUID
	}
	if row.BucketID.Valid {
		scope.BucketID = row.BucketID.String
	}
	principal, err := identity.RestorePrincipal(
		row.ID, row.Name, row.ClientID, scope, role, utc(row.CreatedAt), secrets,
	)
	if err != nil {
		return nil, fmt.Errorf("restore principal: %w", err)
	}
	return principal, nil
}

// createPrincipalTx writes a principal and its secrets inside a caller's
// transaction, so initialization can claim the instance and mint its
// administrator atomically.
func (r *Repository) createPrincipalTx(ctx context.Context, q *postgresdb.Queries, principal *identity.Principal) error {
	if principal.ClientID == identity.SystemScannerPrincipalID {
		return fmt.Errorf("create principal: %w: %s is reserved for scanner audit", identity.ErrInvalid, identity.SystemScannerPrincipalID)
	}
	// uuid.Nil means "no narrower than this", stored as NULL. Never store the
	// zero UUID: it reads as a real identifier to anything not special-casing it,
	// and the database refuses it outright (ADR-0016). A NULL organization is
	// platform scope, which only `root` may hold (ADR-0019).
	projectID := uuid.NullUUID{}
	if principal.Scope.ProjectID != uuid.Nil {
		projectID = uuid.NullUUID{UUID: principal.Scope.ProjectID, Valid: true}
	}
	organizationID := uuid.NullUUID{}
	if principal.Scope.OrganizationID != uuid.Nil {
		organizationID = uuid.NullUUID{UUID: principal.Scope.OrganizationID, Valid: true}
	}
	bucketID := sql.NullString{}
	if principal.Scope.BucketID != "" {
		bucketID = sql.NullString{String: principal.Scope.BucketID, Valid: true}
	}
	if _, err := q.CreatePrincipal(ctx, postgresdb.CreatePrincipalParams{
		ID:             principal.ID,
		Name:           principal.Name,
		ClientID:       principal.ClientID,
		OrganizationID: organizationID,
		ProjectID:      projectID,
		BucketID:       bucketID,
		Role:           string(principal.Role),
		CreatedAt:      principal.CreatedAt,
		IntegrityMac: r.rowMAC(principalMACMessage(
			principal.ID, principal.ClientID, organizationID, projectID, bucketID, string(principal.Role),
		)),
	}); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("create principal: %w", identity.ErrConflict)
	} else if err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "23503" {
			return fmt.Errorf("create principal: %w", identity.ErrNotFound)
		}
		return fmt.Errorf("create principal: %w", err)
	}

	for _, secret := range principal.Secrets() {
		lastUsedAt := sql.NullTime{}
		if secret.LastUsedAt != nil {
			lastUsedAt = sql.NullTime{Time: *secret.LastUsedAt, Valid: true}
		}
		expiresAt := storedExpiry(secret.ExpiresAt)
		if _, err := q.CreatePrincipalSecret(ctx, postgresdb.CreatePrincipalSecretParams{
			ID:          secret.ID,
			PrincipalID: principal.ID,
			EncodedHash: secret.Encoded(),
			CreatedAt:   secret.CreatedAt,
			LastUsedAt:  lastUsedAt,
			ExpiresAt:   expiresAt,
			IntegrityMac: r.rowMAC(principalSecretMACMessage(
				secret.ID, principal.ID, secret.Encoded(), expiresAt,
			)),
		}); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("create principal secret: %w", identity.ErrConflict)
		} else if err != nil {
			return fmt.Errorf("create principal secret: %w", err)
		}
	}
	r.evictPrincipal(principal.ID)
	return nil
}

// GetPrincipalByID loads a principal to resolve its authority.
//
// Separate from GetPrincipalByClientID because the questions differ: that one
// authenticates a credential and must equalise timing against enumeration, this
// one resolves authority for an ALREADY authenticated caller, where the
// identifier came from a token we signed and its existence is not a secret.
//
// Resolved per request rather than carried in the token, so revoking or
// lowering a role takes effect on the next request instead of at token expiry
// (ADR-0019).
func (r *Repository) GetPrincipalByID(ctx context.Context, id string) (*identity.Principal, error) {
	return r.cachedPrincipalByID(ctx, id, func(ctx context.Context, id string) (*identity.Principal, error) {
		q := postgresdb.New(r.db)
		row, err := q.GetPrincipalByID(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("get principal: %w", identity.ErrNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("get principal: %w", err)
		}
		return r.restorePrincipal(ctx, q, row)
	})
}

// ListPrincipals loads the principals bound to EXACTLY the selected scope —
// platform, one organization, or one project — never a subtree. Listing and
// creation follow the same where-you-stand rule, so what a console selection
// shows is what a principal created there would be (duf-4qr).
//
// The caller authorizes; the selection only filters. They are separate
// arguments because a root session browses scopes it does not stand in, while
// a tenancy caller must never see outside its own binding regardless of what
// selection it names. The handler already refuses that, and it is re-asserted
// here at the point of disclosure — the duf-ueq pattern — so a forgotten check
// upstream cannot leak another tenancy's principals.
//
// Takes the caller rather than a bare scope, for the reason documented on
// ListOrganizationsForPrincipal: Scope's zero value is the most privileged
// input, and a principal cannot be constructed holding it without root
// (duf-ueq).
func (r *Repository) ListPrincipals(
	ctx context.Context, caller *identity.Principal, selection identity.Scope,
) ([]*identity.Principal, error) {
	if caller.Scope.PlatformScoped() {
		// Redundant with validBinding, and deliberately so: where a mistake
		// discloses every principal on the instance, construction-time
		// validation gets a second assertion at the point of disclosure
		// (duf-ueq).
		if caller.Role != identity.RoleRoot {
			return nil, fmt.Errorf(
				"list principals: principal %s holds platform scope without root", caller.ID,
			)
		}
	} else {
		entitled := false
		switch {
		case selection.OrganizationScoped():
			entitled = caller.Scope.PermitsOrganization(selection.OrganizationID)
		case !selection.PlatformScoped():
			entitled = caller.Scope.Permits(selection.OrganizationID, selection.ProjectID)
		}
		// A tenancy caller naming the platform, another tenancy, or organization
		// level from a project binding falls through to deny.
		if !entitled {
			return nil, fmt.Errorf(
				"list principals: principal %s is not entitled to the selected scope", caller.ID,
			)
		}
	}

	q := postgresdb.New(r.db)
	var (
		rows []postgresdb.Principal
		err  error
	)
	switch {
	case selection.PlatformScoped():
		rows, err = q.ListPlatformPrincipals(ctx)
	case selection.OrganizationScoped():
		rows, err = q.ListPrincipalsByOrganization(ctx, uuid.NullUUID{
			UUID: selection.OrganizationID, Valid: true,
		})
	default:
		rows, err = q.ListPrincipalsByProject(ctx, postgresdb.ListPrincipalsByProjectParams{
			OrganizationID: uuid.NullUUID{UUID: selection.OrganizationID, Valid: true},
			ProjectID:      uuid.NullUUID{UUID: selection.ProjectID, Valid: true},
		})
	}
	if err != nil {
		return nil, fmt.Errorf("list principals: %w", err)
	}

	principals := make([]*identity.Principal, 0, len(rows))
	for _, row := range rows {
		principal, err := r.restorePrincipal(ctx, q, row)
		if err != nil {
			return nil, err
		}
		principals = append(principals, principal)
	}
	return principals, nil
}

// IssuePrincipalSecret locks the principal while the domain checks and updates
// its active-secret set, so concurrent rotations cannot exceed the domain's
// two-secret limit.
func (r *Repository) IssuePrincipalSecret(
	ctx context.Context, principalID, secretID string, expiresAt *time.Time, at time.Time,
) (string, identity.Secret, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", identity.Secret{}, fmt.Errorf("begin issue principal secret: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	q := postgresdb.New(tx)
	row, err := q.GetPrincipalByIDForUpdate(ctx, principalID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", identity.Secret{}, fmt.Errorf("issue principal secret: %w", identity.ErrNotFound)
	}
	if err != nil {
		return "", identity.Secret{}, fmt.Errorf("lock principal: %w", err)
	}
	principal, err := r.restorePrincipal(ctx, q, row)
	if err != nil {
		// Corrupt stored state, not a bad request: %v deliberately breaks the
		// error chain so this cannot be mistaken for the caller's expiry.
		return "", identity.Secret{}, fmt.Errorf("restore principal for secret issue: %v", err)
	}
	plaintext, err := principal.IssueSecret(secretID, expiresAt, at)
	if err != nil {
		return "", identity.Secret{}, err
	}
	secrets := principal.Secrets()
	issued := secrets[len(secrets)-1]
	storedExpiresAt := storedExpiry(issued.ExpiresAt)
	if _, err := q.CreatePrincipalSecret(ctx, postgresdb.CreatePrincipalSecretParams{
		ID:          issued.ID,
		PrincipalID: principal.ID,
		EncodedHash: issued.Encoded(),
		CreatedAt:   issued.CreatedAt,
		ExpiresAt:   storedExpiresAt,
		IntegrityMac: r.rowMAC(principalSecretMACMessage(
			issued.ID, principal.ID, issued.Encoded(), storedExpiresAt,
		)),
	}); errors.Is(err, sql.ErrNoRows) {
		return "", identity.Secret{}, fmt.Errorf("create principal secret: %w", identity.ErrConflict)
	} else if err != nil {
		return "", identity.Secret{}, fmt.Errorf("create principal secret: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", identity.Secret{}, fmt.Errorf("commit issue principal secret: %w", err)
	}
	r.evictPrincipal(principalID)
	return plaintext, issued, nil
}

// storedExpiry maps a domain expiry to its nullable column, micro-truncated so
// the MACed value equals what a later read recomputes.
func storedExpiry(expiresAt *time.Time) sql.NullTime {
	if expiresAt == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: expiresAt.UTC().Truncate(time.Microsecond), Valid: true}
}

// RevokePrincipalSecret locks the principal while the domain decides. A ROOT
// principal must keep at least one usable, never-expiring credential
// (ADR-0004, amended 2026-08-02; extended for expiry by duf-2rw); every other
// principal may be left with none.
func (r *Repository) RevokePrincipalSecret(
	ctx context.Context, principalID, secretID string, at time.Time,
) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin revoke principal secret: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	q := postgresdb.New(tx)
	row, err := q.GetPrincipalByIDForUpdate(ctx, principalID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("revoke principal secret: %w", identity.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("lock principal: %w", err)
	}
	principal, err := r.restorePrincipal(ctx, q, row)
	if err != nil {
		// As on the issue path: corrupt storage answers as an internal fault.
		return fmt.Errorf("restore principal for secret revoke: %v", err)
	}
	if err := principal.RevokeSecret(secretID, at); err != nil {
		return err
	}
	deleted, err := q.DeletePrincipalSecret(ctx, postgresdb.DeletePrincipalSecretParams{
		PrincipalID: principalID,
		ID:          secretID,
	})
	if err != nil {
		return fmt.Errorf("delete principal secret: %w", err)
	}
	if deleted != 1 {
		return fmt.Errorf("delete principal secret: %w", identity.ErrNotFound)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit revoke principal secret: %w", err)
	}
	r.evictPrincipal(principalID)
	return nil
}

// DeletePrincipal removes a principal. Root deletion takes a transaction-scoped
// advisory lock before counting and deleting, making "at least one root remains"
// one serialized decision across concurrent requests and server instances.
func (r *Repository) DeletePrincipal(ctx context.Context, principalID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete principal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	q := postgresdb.New(tx)
	row, err := q.GetPrincipalByIDForUpdate(ctx, principalID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("delete principal: %w", identity.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("lock principal: %w", err)
	}
	role, err := identity.ParseRole(row.Role)
	if err != nil {
		return fmt.Errorf("delete principal: %w", err)
	}
	if role == identity.RoleRoot {
		if err := q.LockRootPrincipalDeletion(ctx); err != nil {
			return fmt.Errorf("lock root principal deletion: %w", err)
		}
		count, err := q.CountRootPrincipals(ctx)
		if err != nil {
			return fmt.Errorf("count root principals: %w", err)
		}
		if count <= 1 {
			return fmt.Errorf(
				"%w: deleting the last root would leave the instance unadministerable",
				identity.ErrConflict,
			)
		}
	}
	deleted, err := q.DeletePrincipal(ctx, principalID)
	if err != nil {
		return fmt.Errorf("delete principal: %w", err)
	}
	if deleted != 1 {
		return fmt.Errorf("delete principal: %w", identity.ErrNotFound)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete principal: %w", err)
	}
	r.evictPrincipal(principalID)
	return nil
}
