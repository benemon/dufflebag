package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/google/uuid"
)

func TestDummyHashUsesDomainCostParameters(t *testing.T) {
	principal, err := identity.NewPrincipal(
		"principal",
		"principal",
		"client",
		identity.Scope{OrganizationID: uuid.MustParse("00000000-0000-4000-8000-000000000001")},
		identity.RoleBuilder,
		time.Time{},
	)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	// The comparison needs a real domain-produced hash, which now comes from an
	// explicit issuance rather than construction.
	if _, err := principal.IssueSecret("secret", nil, time.Time{}); err != nil {
		t.Fatalf("IssueSecret: %v", err)
	}
	domainParts := strings.Split(principal.Secrets()[0].Encoded(), "$")
	dummyParts := strings.Split(identity.DummySecretHash, "$")
	if domainParts[3] != dummyParts[3] {
		t.Fatalf("dummy argon2id parameters = %q, domain uses %q", dummyParts[3], domainParts[3])
	}
}

// The platform branch answers with every principal on the instance, so it
// re-asserts what validBinding already guarantees: platform scope is held only
// by root. The caller is built by struct literal, bypassing both constructors —
// exactly the input the redundant assertion exists for (duf-ueq). The nil db
// proves the refusal happens before storage is touched.
func TestListPrincipalsRefusesPlatformScopeWithoutRoot(t *testing.T) {
	repository := NewRepository(nil)

	_, err := repository.ListPrincipals(
		context.Background(),
		&identity.Principal{ID: "forged", Role: identity.RoleReader},
		identity.Scope{},
	)
	if err == nil {
		t.Fatal("ListPrincipals answered a non-root platform principal")
	}
}

// The selection is a filter, not an authority: a tenancy caller naming a scope
// outside its own binding is refused HERE, not just in the handler, because
// this is the point of disclosure and the handler check can be forgotten
// (duf-ueq applied to duf-4qr). The nil db proves the refusal happens before
// storage is touched.
func TestListPrincipalsRefusesASelectionOutsideTheCallerBinding(t *testing.T) {
	repository := NewRepository(nil)
	mine := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	myProject := uuid.MustParse("00000000-0000-4000-8000-000000000101")
	theirs := uuid.MustParse("00000000-0000-4000-8000-000000000002")

	orgCaller, err := identity.RestorePrincipal(
		"caller-org", "caller", "client-org",
		identity.Scope{OrganizationID: mine}, identity.RoleMaintainer, time.Time{}, nil,
	)
	if err != nil {
		t.Fatalf("restore caller: %v", err)
	}
	projectCaller, err := identity.RestorePrincipal(
		"caller-project", "caller", "client-project",
		identity.Scope{OrganizationID: mine, ProjectID: myProject},
		identity.RoleMaintainer, time.Time{}, nil,
	)
	if err != nil {
		t.Fatalf("restore caller: %v", err)
	}

	for _, c := range []struct {
		name      string
		caller    *identity.Principal
		selection identity.Scope
	}{
		{"a foreign organisation", orgCaller, identity.Scope{OrganizationID: theirs}},
		{"a foreign organisation's project", orgCaller, identity.Scope{OrganizationID: theirs, ProjectID: myProject}},
		{"the platform", orgCaller, identity.Scope{}},
		{"organisation level from a project binding", projectCaller, identity.Scope{OrganizationID: mine}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := repository.ListPrincipals(
				context.Background(), c.caller, c.selection,
			); err == nil {
				t.Fatalf("ListPrincipals answered %s for a caller not entitled to it", c.name)
			}
		})
	}
}
