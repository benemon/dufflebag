//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

func TestPrincipalRepositoryRoundTripsProjectScopeAndBothSecrets(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	principal, err := identity.NewPrincipal(
		"principal-project",
		"project automation",
		"client-project",
		identity.Scope{
			OrganizationID: uuid.MustParse(orgA),
			ProjectID:      uuid.MustParse(projectA),
		},
		identity.RoleBuilder,
		at,
	)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	first, err := principal.IssueSecret("secret-first", nil, at)
	if err != nil {
		t.Fatalf("IssueSecret: %v", err)
	}
	second, err := principal.IssueSecret("secret-second", nil, at.Add(time.Hour))
	if err != nil {
		t.Fatalf("IssueSecret: %v", err)
	}

	if err := repository.CreatePrincipal(ctx, principal); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repository.GetPrincipalByClientID(ctx, principal.ClientID)
	if err != nil {
		t.Fatalf("ByClientID: %v", err)
	}
	if got.ID != principal.ID ||
		got.Name != principal.Name ||
		got.ClientID != principal.ClientID ||
		got.Scope != principal.Scope ||
		!got.CreatedAt.Equal(at) {
		t.Fatalf("principal round trip = %#v, want %#v", got, principal)
	}
	if len(got.Secrets()) != 2 {
		t.Fatalf("secret count = %d, want 2", len(got.Secrets()))
	}
	firstOK, secondOK := false, false
	if _, ok := got.Authenticate(first, at); ok {
		firstOK = true
	}
	if _, ok := got.Authenticate(second, at); ok {
		secondOK = true
	}
	if !firstOK || !secondOK {
		t.Fatal("both rotated secrets must authenticate after persistence")
	}
}

func TestPrincipalRepositoryRoundTripsOrganizationScopeAsNull(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	principal, err := identity.NewPrincipal(
		"principal-organization",
		"organization automation",
		"client-organization",
		identity.Scope{OrganizationID: uuid.MustParse(orgA)},
		identity.RoleBuilder,
		at,
	)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	plaintext, err := principal.IssueSecret("secret-organization", nil, at)
	if err != nil {
		t.Fatalf("IssueSecret: %v", err)
	}

	if err := repository.CreatePrincipal(ctx, principal); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var storedProjectID sql.NullString
	if err := db.QueryRowContext(
		ctx,
		"SELECT project_id::text FROM principals WHERE id = $1",
		principal.ID,
	).Scan(&storedProjectID); err != nil {
		t.Fatalf("read stored project_id: %v", err)
	}
	if storedProjectID.Valid {
		t.Fatalf("stored project_id = %q, want NULL", storedProjectID.String)
	}

	got, err := repository.GetPrincipalByClientID(ctx, principal.ClientID)
	if err != nil {
		t.Fatalf("ByClientID: %v", err)
	}
	if got.Scope.ProjectID != uuid.Nil {
		t.Fatalf("loaded project id = %s, want uuid.Nil", got.Scope.ProjectID)
	}
	if !got.Scope.OrganizationScoped() {
		t.Fatal("loaded principal is not organization-scoped")
	}
	if _, ok := got.Authenticate(plaintext, at); !ok {
		t.Fatal("organization-scoped principal secret does not authenticate")
	}
}

func TestPrincipalRepositoryLookupAndConflictErrors(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	if _, err := repository.GetPrincipalByClientID(ctx, "unknown-client"); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("unknown ByClientID = %v, want ErrNotFound", err)
	}

	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	first, err := identity.NewPrincipal(
		"principal-first",
		"first",
		"duplicate-client",
		identity.Scope{OrganizationID: uuid.MustParse(orgA)},
		identity.RoleBuilder,
		at,
	)
	if err != nil {
		t.Fatalf("first NewPrincipal: %v", err)
	}
	if err := repository.CreatePrincipal(ctx, first); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	second, err := identity.NewPrincipal(
		"principal-second",
		"second",
		first.ClientID,
		identity.Scope{OrganizationID: uuid.MustParse(orgB)},
		identity.RoleBuilder,
		at,
	)
	if err != nil {
		t.Fatalf("second NewPrincipal: %v", err)
	}
	if err := repository.CreatePrincipal(ctx, second); !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("duplicate client id Create = %v, want ErrConflict", err)
	}
}

func TestPrincipalRepositoryRejectsStoredZeroProjectID(t *testing.T) {
	db, appURL, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	principal, err := identity.NewPrincipal(
		"principal-corrupt",
		"corrupt",
		"client-corrupt",
		identity.Scope{OrganizationID: uuid.MustParse(orgA)},
		identity.RoleBuilder,
		at,
	)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	// The database refuses a zero project id outright, which is the first line of
	// defence and worth asserting before testing the second.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO principals
			(id, name, client_id, organization_id, project_id, role, created_at)
		VALUES ($1, $2, $3, $4, $5, 'builder', $6)
	`, principal.ID, principal.Name, principal.ClientID, orgA, uuid.Nil, at); err == nil {
		t.Fatal("database accepted a zero project id")
	}

	// Drop the constraint to reach the repository's own check, which still has to
	// hold for rows that arrive by some route the constraint never saw — a
	// restore, or a manual repair. Same reasoning as the RLS sabotage hooks.
	//
	// The application role deliberately cannot alter the schema, so this needs the
	// owner. Reuse the container's URL rather than plumbing a second handle
	// through the shared helper for one test.
	ownerURL, err := url.Parse(appURL)
	if err != nil {
		t.Fatalf("parse app url: %v", err)
	}
	ownerURL.User = url.UserPassword("postgres", "postgres")
	owner, err := sql.Open("pgx", ownerURL.String())
	if err != nil {
		t.Fatalf("open owner database: %v", err)
	}
	defer func() { _ = owner.Close() }()
	if _, err := owner.ExecContext(ctx,
		"ALTER TABLE principals DROP CONSTRAINT principals_project_id_not_zero",
	); err != nil {
		t.Fatalf("drop constraint: %v", err)
	}
	// Migration 000021's scope FK also refuses the zero project on the way in;
	// drop it for the same reason as the CHECK above — the restore guard under
	// test must hold for rows that predate or bypass both constraints.
	if _, err := owner.ExecContext(ctx,
		"ALTER TABLE principals DROP CONSTRAINT principals_project_scope_fkey",
	); err != nil {
		t.Fatalf("drop scope fk: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO principals
			(id, name, client_id, organization_id, project_id, role, created_at)
		VALUES ($1, $2, $3, $4, $5, 'builder', $6)
	`, principal.ID, principal.Name, principal.ClientID, orgA, uuid.Nil, at); err != nil {
		t.Fatalf("insert zero-project principal: %v", err)
	}

	_, err = store.NewRepository(db).GetPrincipalByClientID(ctx, principal.ClientID)
	if !errors.Is(err, identity.ErrInvalid) {
		t.Fatalf("ByClientID zero project id = %v, want ErrInvalid", err)
	}
}

// A platform-scoped root principal round-trips, which is what /init creates.
func TestPrincipalRepositoryRoundTripsPlatformRoot(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	principal, err := identity.NewPrincipal(
		"principal-root", "initial administrator", "client-root",
		identity.Scope{}, identity.RoleRoot, at,
	)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	plaintext, err := principal.IssueSecret("secret-root", nil, at)
	if err != nil {
		t.Fatalf("IssueSecret: %v", err)
	}
	if err := repository.CreatePrincipal(ctx, principal); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	var organizationID, projectID sql.NullString
	if err := db.QueryRowContext(ctx,
		"SELECT organization_id::text, project_id::text FROM principals WHERE id = $1", principal.ID,
	).Scan(&organizationID, &projectID); err != nil {
		t.Fatalf("read stored tenancy: %v", err)
	}
	if organizationID.Valid || projectID.Valid {
		t.Fatalf("platform scope stored as %v/%v, want NULL/NULL", organizationID, projectID)
	}

	got, err := repository.GetPrincipalByClientID(ctx, principal.ClientID)
	if err != nil {
		t.Fatalf("GetPrincipalByClientID: %v", err)
	}
	if !got.Scope.PlatformScoped() {
		t.Fatalf("loaded scope = %#v, want platform", got.Scope)
	}
	if got.Role != identity.RoleRoot {
		t.Fatalf("loaded role = %s, want root", got.Role)
	}
	if _, ok := got.Authenticate(plaintext, at); !ok {
		t.Fatal("the loaded root principal does not authenticate")
	}
	// Root outranks the tenancy question rather than satisfying it.
	verdict := got.Authorize(
		identity.RoleMaintainer, uuid.MustParse(orgA), uuid.MustParse(projectA),
	)
	if verdict != identity.AuthorizationAllowed {
		t.Fatal("root cannot act on an arbitrary tenancy")
	}
}

// The database refuses the two shapes that are not scopes: a role other than
// root at platform scope, and root inside a tenancy.
func TestDatabaseRefusesIncoherentScopeAndRole(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name           string
		organizationID any
		role           string
	}{
		{"builder at platform scope", nil, "builder"},
		{"root inside an organization", orgA, "root"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := db.ExecContext(ctx, `
				INSERT INTO principals
					(id, name, client_id, organization_id, project_id, role, created_at)
				VALUES ($1, $2, $3, $4, NULL, $5, $6)
			`, uuid.NewString(), c.name, uuid.NewString(), c.organizationID, c.role, at)
			if err == nil {
				t.Fatalf("database accepted %s", c.name)
			}
		})
	}
}

func TestPrincipalRepositoryManagementLifecycle(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	at := time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC)
	principal, err := identity.NewPrincipal(
		"principal-managed", "managed", "client-managed",
		identity.Scope{
			OrganizationID: uuid.MustParse(orgA),
			ProjectID:      uuid.MustParse(projectA),
		},
		identity.RoleBuilder, at,
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := principal.IssueSecret("secret-first", nil, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreatePrincipal(ctx, principal); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	listed, err := repository.ListPrincipals(ctx, principal, principal.Scope)
	if err != nil {
		t.Fatalf("ListPrincipals: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != principal.ID {
		t.Fatalf("project-scoped list = %#v, want principal %s", listed, principal.ID)
	}

	second, issued, err := repository.IssuePrincipalSecret(
		ctx, principal.ID, "secret-second", nil, at.Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("IssuePrincipalSecret: %v", err)
	}
	if issued.ID != "secret-second" || second == "" {
		t.Fatalf("issued secret = %#v / %q", issued, second)
	}
	got, err := repository.GetPrincipalByID(ctx, principal.ID)
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	firstOK, secondOK := false, false
	if _, ok := got.Authenticate(first, at); ok {
		firstOK = true
	}
	if _, ok := got.Authenticate(second, at); ok {
		secondOK = true
	}
	if !firstOK || !secondOK {
		t.Fatal("both active secrets must authenticate")
	}

	if err := repository.RevokePrincipalSecret(ctx, principal.ID, "secret-first", at); err != nil {
		t.Fatalf("RevokePrincipalSecret: %v", err)
	}
	got, err = repository.GetPrincipalByID(ctx, principal.ID)
	if err != nil {
		t.Fatalf("GetPrincipalByID after revoke: %v", err)
	}
	_, revokedWorks := got.Authenticate(first, at)
	_, retainedWorks := got.Authenticate(second, at)
	if revokedWorks || !retainedWorks {
		t.Fatal("revoked secret authenticated or retained secret did not")
	}
	// A builder may be left with no secrets at all — only root's last one is
	// protected (ADR-0004, amended 2026-08-02). Asserted through the repository
	// rather than the domain alone, because the refusal is applied inside the
	// locked transaction and a stale copy of the rule there would not surface
	// in a unit test.
	if err := repository.RevokePrincipalSecret(ctx, principal.ID, "secret-second", at); err != nil {
		t.Fatalf("revoke a builder's only secret = %v, want it permitted", err)
	}
	got, err = repository.GetPrincipalByID(ctx, principal.ID)
	if err != nil {
		t.Fatalf("GetPrincipalByID after revoking the last secret: %v", err)
	}
	if len(got.Secrets()) != 0 {
		t.Fatalf("holds %d secrets, want none", len(got.Secrets()))
	}
	if _, ok := got.Authenticate(second, at); ok {
		t.Fatal("the revoked secret still authenticates")
	}

	if err := repository.DeletePrincipal(ctx, principal.ID); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}
	if _, err := repository.GetPrincipalByID(ctx, principal.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("deleted GetPrincipalByID = %v, want ErrNotFound", err)
	}
}

func TestPrincipalRepositoryRefusesLastRootAndDeletesOneOfTwo(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	at := time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC)
	first, err := identity.NewPrincipal(
		"root-first", "first root", "root-client-first",
		identity.Scope{}, identity.RoleRoot, at,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreatePrincipal(ctx, first); err != nil {
		t.Fatalf("create first root: %v", err)
	}
	if err := repository.DeletePrincipal(ctx, first.ID); !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("delete last root = %v, want ErrConflict", err)
	}

	second, err := identity.NewPrincipal(
		"root-second", "second root", "root-client-second",
		identity.Scope{}, identity.RoleRoot, at.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreatePrincipal(ctx, second); err != nil {
		t.Fatalf("create second root: %v", err)
	}
	if err := repository.DeletePrincipal(ctx, first.ID); err != nil {
		t.Fatalf("delete root while a second exists: %v", err)
	}
	if _, err := repository.GetPrincipalByID(ctx, second.ID); err != nil {
		t.Fatalf("remaining root: %v", err)
	}
	if err := repository.DeletePrincipal(ctx, second.ID); !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("delete remaining root = %v, want ErrConflict", err)
	}
}

// Listing selects EXACTLY one scope, never a subtree (duf-4qr): the platform
// selection excludes tenancy principals, an organisation selection excludes
// its projects' principals, and a project selection excludes its
// organisation's. This is the SQL-level gate for that rule — the handler and
// fake tests cannot see a WHERE clause that quietly widens.
func TestListPrincipalsSelectsExactScope(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	at := time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC)

	for _, seed := range []struct {
		id    string
		scope identity.Scope
		role  identity.Role
		at    time.Time
	}{
		{"scope-platform-old", identity.Scope{}, identity.RoleRoot, at},
		{"scope-platform-new", identity.Scope{}, identity.RoleRoot, at.Add(time.Second)},
		{"scope-org-a-old", identity.Scope{OrganizationID: uuid.MustParse(orgA)}, identity.RoleMaintainer, at},
		{"scope-org-a-new", identity.Scope{OrganizationID: uuid.MustParse(orgA)}, identity.RoleMaintainer, at.Add(time.Second)},
		{"scope-project-a-old", identity.Scope{
			OrganizationID: uuid.MustParse(orgA), ProjectID: uuid.MustParse(projectA),
		}, identity.RoleBuilder, at},
		{"scope-project-a-new", identity.Scope{
			OrganizationID: uuid.MustParse(orgA), ProjectID: uuid.MustParse(projectA),
		}, identity.RoleBuilder, at.Add(time.Second)},
		{"scope-org-b", identity.Scope{OrganizationID: uuid.MustParse(orgB)}, identity.RoleMaintainer, at},
	} {
		principal, err := identity.NewPrincipal(
			seed.id, seed.id, "client-"+seed.id, seed.scope, seed.role, seed.at,
		)
		if err != nil {
			t.Fatalf("NewPrincipal %s: %v", seed.id, err)
		}
		if err := repository.CreatePrincipal(ctx, principal); err != nil {
			t.Fatalf("CreatePrincipal %s: %v", seed.id, err)
		}
	}

	root := listingCaller(t, identity.Scope{}, identity.RoleRoot)
	for _, c := range []struct {
		name      string
		selection identity.Scope
		want      []string
	}{
		{"platform", identity.Scope{}, []string{"scope-platform-new", "scope-platform-old"}},
		{"organisation A", identity.Scope{OrganizationID: uuid.MustParse(orgA)}, []string{"scope-org-a-new", "scope-org-a-old"}},
		{"project A", identity.Scope{
			OrganizationID: uuid.MustParse(orgA), ProjectID: uuid.MustParse(projectA),
		}, []string{"scope-project-a-new", "scope-project-a-old"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			listed, err := repository.ListPrincipals(ctx, root, c.selection)
			if err != nil {
				t.Fatalf("ListPrincipals: %v", err)
			}
			got := make([]string, 0, len(listed))
			for _, principal := range listed {
				got = append(got, principal.ID)
			}
			if len(got) != len(c.want) {
				t.Fatalf("selection %s listed %v, want %v", c.name, got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("selection %s listed %v, want %v", c.name, got, c.want)
				}
			}
		})
	}

	// A tenancy caller listing its own standing still works — the filter
	// defaulting and the entitlement check must not lock a maintainer out of
	// the one scope it administers.
	maintainer := listingCaller(
		t, identity.Scope{OrganizationID: uuid.MustParse(orgA)}, identity.RoleMaintainer,
	)
	own, err := repository.ListPrincipals(ctx, maintainer, maintainer.Scope)
	if err != nil {
		t.Fatalf("ListPrincipals own scope: %v", err)
	}
	if len(own) != 2 || own[0].ID != "scope-org-a-new" || own[1].ID != "scope-org-a-old" {
		t.Fatalf("own-scope list = %#v, want newest-first organisation principals", own)
	}
}

func TestConcurrentRootDeletesLeaveOneRoot(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	at := time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC)
	ids := []string{"root-concurrent-a", "root-concurrent-b"}
	for i, id := range ids {
		principal, err := identity.NewPrincipal(
			id, id, "client-"+id, identity.Scope{}, identity.RoleRoot,
			at.Add(time.Duration(i)*time.Second),
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.CreatePrincipal(ctx, principal); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	start := make(chan struct{})
	results := make(chan error, len(ids))
	var ready sync.WaitGroup
	ready.Add(len(ids))
	for _, id := range ids {
		go func() {
			ready.Done()
			<-start
			results <- repository.DeletePrincipal(ctx, id)
		}()
	}
	ready.Wait()
	close(start)

	var deleted, refused int
	for range ids {
		err := <-results
		switch {
		case err == nil:
			deleted++
		case errors.Is(err, identity.ErrConflict):
			refused++
		default:
			t.Fatalf("concurrent delete: %v", err)
		}
	}
	if deleted != 1 || refused != 1 {
		t.Fatalf("concurrent deletes = %d deleted, %d refused; want 1 and 1", deleted, refused)
	}
	principals, err := repository.ListPrincipals(
		ctx, listingCaller(t, identity.Scope{}, identity.RoleRoot), identity.Scope{},
	)
	if err != nil {
		t.Fatalf("ListPrincipals: %v", err)
	}
	if len(principals) != 1 || principals[0].Role != identity.RoleRoot {
		t.Fatalf("roots after concurrent deletion = %#v, want exactly one", principals)
	}
}

// A principal persists with NO secrets, and its FIRST secret is issued through
// the ordinary issuance path (duf-4ac).
//
// Covered here rather than only in the domain because this is the state the
// storage layer newly has to hold: before the split, every persisted principal
// arrived carrying a secret, so a zero-secret row never round-tripped. The
// handler tests use a fake repository and would not catch a schema or loader
// that quietly assumed at least one.
func TestPrincipalRepositoryPersistsWithNoSecretsThenIssuesTheFirst(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	principal, err := identity.NewPrincipal(
		"principal-secretless", "created empty", "client-secretless",
		identity.Scope{OrganizationID: uuid.MustParse(orgA)}, identity.RoleBuilder, at,
	)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	if err := repository.CreatePrincipal(ctx, principal); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	stored, err := repository.GetPrincipalByClientID(ctx, principal.ClientID)
	if err != nil {
		t.Fatalf("ByClientID: %v", err)
	}
	if len(stored.Secrets()) != 0 {
		t.Fatalf("round-tripped %d secrets, want none", len(stored.Secrets()))
	}
	// It exists and is entirely unusable, which is the point of the state.
	if _, ok := stored.Authenticate("", at); ok {
		t.Fatal("a secretless principal authenticated")
	}

	plaintext, secret, err := repository.IssuePrincipalSecret(
		ctx, principal.ID, "secret-first", nil, at.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("IssuePrincipalSecret: %v", err)
	}
	if plaintext == "" || secret.ID != "secret-first" {
		t.Fatalf("issued = %q / %#v", plaintext, secret)
	}

	reloaded, err := repository.GetPrincipalByClientID(ctx, principal.ClientID)
	if err != nil {
		t.Fatalf("ByClientID after issue: %v", err)
	}
	if len(reloaded.Secrets()) != 1 {
		t.Fatalf("holds %d secrets after the first issue, want 1", len(reloaded.Secrets()))
	}
	if _, ok := reloaded.Authenticate(plaintext, at); !ok {
		t.Fatal("the first issued secret does not authenticate after persistence")
	}
}

// duf-2rw: expiry rides the secret through persistence, an expired secret
// authenticates nothing, and the cap counts USABLE secrets so a replacement
// issues without revoking the expired one first.
func TestPrincipalSecretExpiryRoundTripsAndExpiredSecretsFreeTheCap(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	principal, err := identity.NewPrincipal(
		"principal-expiry", "expiring", "client-expiry",
		identity.Scope{
			OrganizationID: uuid.MustParse(orgA),
			ProjectID:      uuid.MustParse(projectA),
		},
		identity.RoleBuilder, at,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreatePrincipal(ctx, principal); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	expiry := at.Add(time.Hour)
	plaintext, issued, err := repository.IssuePrincipalSecret(
		ctx, principal.ID, "secret-expiring", &expiry, at,
	)
	if err != nil {
		t.Fatalf("IssuePrincipalSecret: %v", err)
	}
	if issued.ExpiresAt == nil || !issued.ExpiresAt.Equal(expiry) {
		t.Fatalf("issued expiry = %v, want %v", issued.ExpiresAt, expiry)
	}

	stored, err := repository.GetPrincipalByClientID(ctx, "client-expiry")
	if err != nil {
		t.Fatalf("GetPrincipalByClientID: %v", err)
	}
	secrets := stored.Secrets()
	if len(secrets) != 1 || secrets[0].ExpiresAt == nil || !secrets[0].ExpiresAt.Equal(expiry) {
		t.Fatalf("round-tripped secrets = %#v, want one carrying %v", secrets, expiry)
	}
	if _, ok := stored.Authenticate(plaintext, at.Add(30*time.Minute)); !ok {
		t.Fatal("an unexpired secret must authenticate")
	}
	if _, ok := stored.Authenticate(plaintext, at.Add(2*time.Hour)); ok {
		t.Fatal("an expired secret authenticated")
	}

	// Fill the cap while both are usable, then watch expiry free it with no
	// revocation step.
	if _, _, err := repository.IssuePrincipalSecret(
		ctx, principal.ID, "secret-permanent", nil, at.Add(time.Minute),
	); err != nil {
		t.Fatalf("second secret: %v", err)
	}
	if _, _, err := repository.IssuePrincipalSecret(
		ctx, principal.ID, "secret-blocked", nil, at.Add(2*time.Minute),
	); !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("third usable secret should be refused, got %v", err)
	}
	if _, _, err := repository.IssuePrincipalSecret(
		ctx, principal.ID, "secret-replacement", nil, at.Add(2*time.Hour),
	); err != nil {
		t.Fatalf("replacement after expiry should issue without revocation: %v", err)
	}
	replaced, err := repository.GetPrincipalByClientID(ctx, "client-expiry")
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced.Secrets()) != 3 {
		t.Fatalf("stored secrets = %d, want 3 (one expired, kept for diagnosis)", len(replaced.Secrets()))
	}
}
