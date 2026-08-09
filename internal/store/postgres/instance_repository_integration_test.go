//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

func bootstrapPrincipal(t *testing.T, at time.Time) *identity.Principal {
	t.Helper()
	principal, err := identity.NewPrincipal(
		uuid.NewString(), "initial administrator", uuid.NewString(),
		identity.Scope{}, identity.RoleRoot, at,
	)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	return principal
}

// testRecoveryDigest is produced by the same code /sys/init uses, not written
// by hand: the verifier's shape belongs to the identity package.
func testRecoveryDigest(t *testing.T) []byte {
	t.Helper()
	_, digest, err := identity.NewRecoveryShares(1, 1)
	if err != nil {
		t.Fatalf("NewRecoveryShares: %v", err)
	}
	return digest
}

func deleteSeedTenants(t *testing.T, repository *store.Repository) {
	t.Helper()
	ctx := context.Background()
	for _, tenant := range []struct{ organizationID, projectID string }{
		{orgA, projectA}, {orgB, projectB},
	} {
		if err := repository.DeleteProject(ctx, tenant.organizationID, tenant.projectID); err != nil {
			t.Fatalf("delete seeded project: %v", err)
		}
		if err := repository.DeleteOrganization(ctx, tenant.organizationID); err != nil {
			t.Fatalf("delete seeded organization: %v", err)
		}
	}
}

// The singleton lock is what makes the root-existence predicate atomic.
// Concurrent callers must not both be told they own the deployment.
func TestInitializeInstanceClaimsExactlyOnce(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	deleteSeedTenants(t, repository)
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin contention barrier: %v", err)
	}
	defer func() { _ = blocker.Rollback() }()
	if _, err := blocker.ExecContext(ctx, "LOCK TABLE principals IN SHARE MODE"); err != nil {
		t.Fatalf("lock principals: %v", err)
	}

	const callers = 6
	digest := testRecoveryDigest(t)
	results := make([]error, callers)
	principals := make([]*identity.Principal, callers)
	for i := range principals {
		// Argon2 work happens before the race. Doing it inside each goroutine
		// staggered repository entry enough to let a missing lock look safe.
		principals[i] = bootstrapPrincipal(t, at)
	}
	var wg sync.WaitGroup
	var ready sync.WaitGroup
	ready.Add(callers)
	start := make(chan struct{})
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ready.Done()
			<-start
			results[index] = repository.InitializeInstance(ctx, principals[index], digest, 1)
		}(i)
	}
	ready.Wait()
	close(start)

	deadline := time.Now().Add(10 * time.Second)
	for {
		var inserts, guards int
		err := blocker.QueryRowContext(ctx, `
			SELECT
				count(*) FILTER (WHERE relation = 'principals'::regclass AND mode = 'RowExclusiveLock'),
				count(*) FILTER (WHERE relation = 'instance'::regclass AND mode = 'ExclusiveLock')
			FROM pg_locks
			WHERE NOT granted
		`).Scan(&inserts, &guards)
		if err != nil {
			_ = blocker.Rollback()
			wg.Wait()
			t.Fatalf("observe initialization contenders: %v", err)
		}
		if inserts+guards == callers {
			break
		}
		if time.Now().After(deadline) {
			_ = blocker.Rollback()
			wg.Wait()
			t.Fatalf("initialization contenders did not reach database locks: inserts=%d guards=%d", inserts, guards)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatalf("release contention barrier: %v", err)
	}
	wg.Wait()

	claimed, conflicts := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			claimed++
		case errors.Is(err, registry.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if claimed != 1 {
		t.Fatalf("%d callers claimed the instance, want exactly 1 (%d conflicts)", claimed, conflicts)
	}

	stored, err := repository.ListPrincipals(
		ctx, listingCaller(t, identity.Scope{}, identity.RoleRoot), identity.Scope{},
	)
	if err != nil {
		t.Fatalf("ListPrincipals: %v", err)
	}
	if len(stored) != 1 || stored[0].Role != identity.RoleRoot {
		t.Fatalf("stored principals = %v, want exactly one root", stored)
	}
}

// A losing caller must leave no principal that could authenticate.
func TestLosingInitializeRollsBackCompletely(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	deleteSeedTenants(t, repository)
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	if err := repository.InitializeInstance(ctx, bootstrapPrincipal(t, at), testRecoveryDigest(t), 1); err != nil {
		t.Fatalf("first initialize: %v", err)
	}

	secondPrincipal := bootstrapPrincipal(t, at)
	if err := repository.InitializeInstance(ctx, secondPrincipal, testRecoveryDigest(t), 1); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("second initialize = %v, want ErrConflict", err)
	}

	if _, err := repository.GetPrincipalByClientID(ctx, secondPrincipal.ClientID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("the losing caller's principal survived: %v", err)
	}
}

func TestInstanceStatusReportsRecordedInitializationTime(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	deleteSeedTenants(t, repository)
	at := time.Date(2026, 7, 30, 12, 0, 0, 123000000, time.UTC)
	if err := repository.InitializeInstance(ctx, bootstrapPrincipal(t, at), testRecoveryDigest(t), 1); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	initialized, initializedAt, database, err := repository.InstanceStatus(ctx)
	if err != nil {
		t.Fatalf("InstanceStatus: %v", err)
	}
	if !initialized || !database {
		t.Fatalf("status = initialized %t, database %t", initialized, database)
	}
	if initializedAt == nil || !initializedAt.Equal(at) {
		t.Fatalf("initialized_at = %v, want %v", initializedAt, at)
	}
}

// The root is the claimed predicate. Replacing it with "any tenant exists"
// makes this second call succeed because initialization deliberately creates no
// organization or project.
func TestInitializeInstanceDoesNotReopenWithoutTenancy(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	deleteSeedTenants(t, repository)
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	if err := repository.InitializeInstance(ctx, bootstrapPrincipal(t, at), testRecoveryDigest(t), 1); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	organizations, err := repository.ListOrganizationsForPrincipal(
		ctx, listingCaller(t, identity.Scope{}, identity.RoleRoot),
	)
	if err != nil {
		t.Fatalf("ListOrganizationsForPrincipal: %v", err)
	}
	if len(organizations) != 0 {
		t.Fatalf("initialization created %d organizations, want none", len(organizations))
	}

	if err := repository.InitializeInstance(ctx, bootstrapPrincipal(t, at), testRecoveryDigest(t), 1); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("initialize with a root but no tenancy = %v, want ErrConflict", err)
	}
}

// The verifier is written inside the claiming transaction and read back for
// /sys/recovery; an unclaimed instance answers not-found rather than an empty
// digest a comparison could accidentally accept.
func TestRecoveryVerifierRoundTripsThroughTheClaim(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	deleteSeedTenants(t, repository)

	if _, _, err := repository.RecoveryVerifier(ctx); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("verifier before the claim = %v, want ErrNotFound", err)
	}

	at := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	shares, digest, err := identity.NewRecoveryShares(3, 2)
	if err != nil {
		t.Fatalf("NewRecoveryShares: %v", err)
	}
	if err := repository.InitializeInstance(ctx, bootstrapPrincipal(t, at), digest, 2); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	storedDigest, storedThreshold, err := repository.RecoveryVerifier(ctx)
	if err != nil {
		t.Fatalf("RecoveryVerifier: %v", err)
	}
	if storedThreshold != 2 {
		t.Fatalf("threshold = %d, want 2", storedThreshold)
	}
	// The stored verifier still verifies the shares minted with it — the whole
	// ceremony's persistence in one assertion.
	if err := identity.VerifyRecoveryShares(shares[:2], storedThreshold, storedDigest); err != nil {
		t.Fatalf("stored verifier rejects its own shares: %v", err)
	}
}

// Finding 1 of the 2026-07-31 review, at the layer where it actually lives.
// The handler test uses a fake, so filtering must also be asserted against real
// Postgres — the review demonstrated that collapsing this to an unscoped query
// passed every suite, including integration.
func TestListOrganizationsForPrincipalFiltersByCaller(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)

	// The fixture seeds organization-a and organization-b.
	all, err := repository.ListOrganizationsForPrincipal(
		ctx, listingCaller(t, identity.Scope{}, identity.RoleRoot),
	)
	if err != nil {
		t.Fatalf("platform scope: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("platform scope saw %d organizations, want the fixture's two or more", len(all))
	}

	// A tenancy-scoped caller sees exactly one: its own.
	scoped, err := repository.ListOrganizationsForPrincipal(
		ctx, listingCaller(t, identity.Scope{OrganizationID: uuid.MustParse(orgA)}, identity.RoleReader),
	)
	if err != nil {
		t.Fatalf("organization scope: %v", err)
	}
	if len(scoped) != 1 {
		t.Fatalf("organization scope saw %d organizations, want exactly 1", len(scoped))
	}
	if scoped[0].ID != orgA {
		t.Fatalf("saw organization %s, want its own %s", scoped[0].ID, orgA)
	}

	// And a project-scoped caller sees the organization containing its project,
	// and nothing else.
	project, err := repository.ListOrganizationsForPrincipal(ctx, listingCaller(t, identity.Scope{
		OrganizationID: uuid.MustParse(orgA),
		ProjectID:      uuid.MustParse(projectA),
	}, identity.RoleReader))
	if err != nil {
		t.Fatalf("project scope: %v", err)
	}
	if len(project) != 1 || project[0].ID != orgA {
		t.Fatalf("project scope saw %v, want only %s", project, orgA)
	}
}

func TestListProjectsForPrincipalFiltersAProjectBinding(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	sibling := store.Project{
		ID: uuid.NewString(), OrganizationID: orgA, Name: "sibling", CreatedAt: time.Now().UTC(),
	}
	if _, err := repository.CreateProject(ctx, sibling); err != nil {
		t.Fatalf("create sibling: %v", err)
	}

	projects, err := repository.ListProjectsForPrincipal(ctx, listingCaller(t, identity.Scope{
		OrganizationID: uuid.MustParse(orgA),
		ProjectID:      uuid.MustParse(projectA),
	}, identity.RoleReader), uuid.MustParse(orgA))
	if err != nil {
		t.Fatalf("ListProjectsForPrincipal: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != projectA {
		t.Fatalf("project scope saw %#v, want only %s", projects, projectA)
	}
}

func TestListProjectsForPrincipalAnswersADeletedBindingWithNothing(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	projects, err := repository.ListProjectsForPrincipal(ctx, listingCaller(t, identity.Scope{
		OrganizationID: uuid.MustParse(orgA),
		ProjectID:      uuid.New(),
	}, identity.RoleReader), uuid.MustParse(orgA))
	if err != nil {
		t.Fatalf("ListProjectsForPrincipal: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("deleted binding saw %#v, want nothing", projects)
	}
}

// A caller bound to an organization that has since been deleted sees nothing.
// Its scope resolves to nothing, which is not a failure (ADR-0016).
func TestListOrganizationsForPrincipalSurvivesADeletedOrganization(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	gone := uuid.New()

	organizations, err := repository.ListOrganizationsForPrincipal(
		ctx, listingCaller(t, identity.Scope{OrganizationID: gone}, identity.RoleReader),
	)
	if err != nil {
		t.Fatalf("ListOrganizationsForPrincipal: %v", err)
	}
	if len(organizations) != 0 {
		t.Fatalf("saw %d organizations for a scope that resolves to nothing, want 0", len(organizations))
	}
}
