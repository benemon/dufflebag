package postgres

import (
	"context"
	"testing"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/google/uuid"
)

// A principal bound to no organisation sees none — deny by default, rather than
// nothing-set matching everything (ADR-0016).
//
// The principal is built by struct literal because identity's constructors
// refuse this binding outright: an unbound non-root principal cannot arrive
// through the front door, so the branch is defence in depth against a bypassed
// constructor or corrupted value. The nil db proves the denial happens before
// storage is consulted — were the guard gone, the lookup would fail loudly
// instead of the test passing by coincidence of an empty database.
func TestListOrganizationsForPrincipalAnswersAnUnboundPrincipalWithNothing(t *testing.T) {
	repository := NewRepository(nil)

	organizations, err := repository.ListOrganizationsForPrincipal(
		context.Background(),
		&identity.Principal{
			ID:    "unbound",
			Scope: identity.Scope{ProjectID: uuid.New()},
			Role:  identity.RoleReader,
		},
	)
	if err != nil {
		t.Fatalf("ListOrganizationsForPrincipal: %v", err)
	}
	if len(organizations) != 0 {
		t.Fatalf("an unbound principal saw %d organizations, want 0", len(organizations))
	}
}

// The platform branch answers with every organisation on the instance, so it
// re-asserts what validBinding already guarantees: platform scope is held only
// by root. The principal is built by struct literal, bypassing both
// constructors — exactly the input the redundant assertion exists for
// (duf-ueq). The nil db proves the refusal happens before storage is touched.
func TestListOrganizationsForPrincipalRefusesPlatformScopeWithoutRoot(t *testing.T) {
	repository := NewRepository(nil)

	_, err := repository.ListOrganizationsForPrincipal(
		context.Background(),
		&identity.Principal{ID: "forged", Role: identity.RoleReader},
	)
	if err == nil {
		t.Fatal("ListOrganizationsForPrincipal answered a non-root platform principal")
	}
}

func TestListProjectsForPrincipalRefusesScopesOutsideTheOrganization(t *testing.T) {
	repository := NewRepository(nil)
	organization := uuid.New()

	for _, principal := range []*identity.Principal{
		{ID: "foreign", Role: identity.RoleReader, Scope: identity.Scope{OrganizationID: uuid.New()}},
		{ID: "forged", Role: identity.RoleReader, Scope: identity.Scope{}},
	} {
		if _, err := repository.ListProjectsForPrincipal(
			context.Background(), principal, organization,
		); err == nil {
			t.Fatalf("ListProjectsForPrincipal answered scope %#v", principal.Scope)
		}
	}
}
