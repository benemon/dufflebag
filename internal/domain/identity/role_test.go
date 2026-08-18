package identity

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestRoleOrdering(t *testing.T) {
	ordered := []Role{RoleReader, RoleBuilder, RolePublisher, RoleMaintainer, RoleRoot}
	for actorRank, actor := range ordered {
		for targetRank, target := range ordered {
			want := actorRank >= targetRank
			if got := actor.AtLeast(target); got != want {
				t.Fatalf("%s.AtLeast(%s) = %v, want %v", actor, target, got, want)
			}
			if got := actor.MayGrant(target); got != want {
				t.Fatalf("%s.MayGrant(%s) = %v, want %v", actor, target, got, want)
			}
			if got := actor.MayModifyHolderOf(target); got != want {
				t.Fatalf("%s.MayModifyHolderOf(%s) = %v, want %v", actor, target, got, want)
			}
		}
	}
}

// A value that is not a role must not compare as the lowest tier, which is what
// would happen if an unknown role were treated as rank zero.
func TestUnknownRoleSatisfiesNothingAndIsSatisfiedByNothing(t *testing.T) {
	unknown := Role("superuser")

	for _, known := range []Role{RoleReader, RoleBuilder, RolePublisher, RoleMaintainer, RoleRoot} {
		if unknown.AtLeast(known) {
			t.Fatalf("unknown role satisfied %s", known)
		}
		if known.AtLeast(unknown) {
			t.Fatalf("%s satisfied an unknown requirement", known)
		}
		if known.MayGrant(unknown) {
			t.Fatalf("%s granted an unknown role", known)
		}
		if known.MayModifyHolderOf(unknown) {
			t.Fatalf("%s modified the holder of an unknown role", known)
		}
		if unknown.MayGrant(known) {
			t.Fatalf("unknown role granted %s", known)
		}
		if unknown.MayModifyHolderOf(known) {
			t.Fatalf("unknown role modified the holder of %s", known)
		}
	}
	if unknown.MayGrant(unknown) {
		t.Fatal("unknown role granted an unknown role")
	}
	if unknown.MayModifyHolderOf(unknown) {
		t.Fatal("unknown role modified the holder of an unknown role")
	}
	if Role("").AtLeast(RoleReader) {
		t.Fatal("the empty role satisfied reader")
	}
}

func TestParseRoleRefusesAnythingUnrecognised(t *testing.T) {
	for _, valid := range []string{"reader", "builder", "publisher", "maintainer", "root"} {
		if _, err := ParseRole(valid); err != nil {
			t.Fatalf("ParseRole(%q): %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "admin", "user", "ROOT", "Root", "owner", "operator"} {
		if _, err := ParseRole(invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ParseRole(%q) = %v, want ErrInvalid", invalid, err)
		}
	}
}

// The escalation defence: never grant above your own level, but equality is
// allowed or delegation would be impossible.
func TestMayGrantRefusesUpwardButPermitsEqual(t *testing.T) {
	if !RoleMaintainer.MayGrant(RoleMaintainer) {
		t.Fatal("a maintainer cannot appoint another maintainer, so delegation is impossible")
	}
	if RoleMaintainer.MayGrant(RoleRoot) {
		t.Fatal("a maintainer granted root")
	}
	if RoleBuilder.MayGrant(RolePublisher) {
		t.Fatal("a builder granted publisher")
	}
	if !RoleRoot.MayGrant(RoleRoot) {
		t.Fatal("root cannot appoint root, which would make the last-root rule unescapable")
	}
}

// Escalation by removal: taking out a higher tier leaves you at the top, so the
// grant rule alone is not enough.
func TestMayModifyHolderRefusesActingOnAHigherRole(t *testing.T) {
	if RoleMaintainer.MayModifyHolderOf(RoleRoot) {
		t.Fatal("a maintainer could delete a root holder — escalation by removal")
	}
	if RolePublisher.MayModifyHolderOf(RoleMaintainer) {
		t.Fatal("a publisher could act on a maintainer")
	}
	if !RoleMaintainer.MayModifyHolderOf(RoleBuilder) {
		t.Fatal("a maintainer cannot manage a builder, which is its whole purpose")
	}
	if !RoleRoot.MayModifyHolderOf(RoleRoot) {
		t.Fatal("root cannot act on root, so a compromised root could never be removed")
	}
}

func TestOnlyRootIsPlatformOnly(t *testing.T) {
	if !RoleRoot.PlatformOnly() {
		t.Fatal("root is not marked platform-only")
	}
	for _, role := range []Role{RoleReader, RoleBuilder, RolePublisher, RoleMaintainer} {
		if role.PlatformOnly() {
			t.Fatalf("%s is marked platform-only", role)
		}
	}
}

// The role a Packer principal holds must be the smallest that completes a
// build, and must NOT reach promotion (ADR-0019).
func TestBuilderIsTheSmallestRoleThatCompletesABuild(t *testing.T) {
	if !RoleBuilder.AtLeast(RoleReader) {
		t.Fatal("builder cannot read, so it cannot check for an existing version")
	}
	if RoleBuilder.AtLeast(RolePublisher) {
		t.Fatal("builder can promote — a CI credential must not assign channels")
	}
	if RoleBuilder.AtLeast(RoleMaintainer) {
		t.Fatal("builder can manage identities")
	}
}

// Authorize asks both halves of the authorization question at once: enough
// authority, and entitled to this tenancy. Checking one and forgetting the
// other is the shape of most authorization bugs, so they are not separable.
func TestAuthorizeRequiresBothAuthorityAndTenancy(t *testing.T) {
	principal, err := NewPrincipal(
		"p-1", "ci", "client-1",
		Scope{OrganizationID: orgA, ProjectID: projA}, RoleBuilder, epoch,
	)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}

	if principal.Authorize(RoleBuilder, orgA, projA) != AuthorizationAllowed {
		t.Fatal("a builder cannot build in its own project")
	}
	if principal.Authorize(RoleReader, orgA, projA) != AuthorizationAllowed {
		t.Fatal("a builder cannot read, though reader is below it")
	}
	if principal.Authorize(RolePublisher, orgA, projA) == AuthorizationAllowed {
		t.Fatal("a builder may promote in its own project")
	}
	// Right authority, wrong tenancy.
	if principal.Authorize(RoleBuilder, orgA, projB) == AuthorizationAllowed {
		t.Fatal("a builder may act on a sibling project")
	}
	if principal.Authorize(RoleBuilder, orgB, projA) == AuthorizationAllowed {
		t.Fatal("a builder may act on another organization")
	}
}

// Root outranks the tenancy question rather than satisfying it.
func TestRootActsOnAnyTenancy(t *testing.T) {
	root, err := NewPrincipal("p-root", "bootstrap", "client-root", Scope{}, RoleRoot, epoch)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	if !root.Scope.PlatformScoped() {
		t.Fatal("a root principal is not platform-scoped")
	}
	if root.Scope.Permits(orgA, projA) {
		t.Fatal("Permits answered true for a platform scope — it should answer the tenancy question only")
	}
	for _, c := range []struct{ org, project uuid.UUID }{{orgA, projA}, {orgB, projB}} {
		if root.Authorize(RoleMaintainer, c.org, c.project) != AuthorizationAllowed {
			t.Fatalf("root cannot act on %s/%s", c.org, c.project)
		}
	}
}

func TestAuthorizationVerdicts(t *testing.T) {
	projectScope := Scope{OrganizationID: orgA, ProjectID: projA}
	organizationScope := Scope{OrganizationID: orgA}
	tests := []struct {
		name           string
		principal      Principal
		required       Role
		organizationID uuid.UUID
		projectID      uuid.UUID
		visibility     bool
		want           AuthorizationVerdict
	}{
		{
			name:      "project authorization root bypasses tenancy and role",
			principal: Principal{Role: RoleRoot, Scope: Scope{}},
			required:  Role("above-root"), organizationID: orgB, projectID: projB,
			want: AuthorizationAllowed,
		},
		{
			name:      "project authorization denies tenancy before role",
			principal: Principal{Role: RoleReader, Scope: projectScope},
			required:  RoleBuilder, organizationID: orgA, projectID: projB,
			want: AuthorizationDeniedTenancy,
		},
		{
			name:      "project authorization denies role on exact match",
			principal: Principal{Role: RoleReader, Scope: projectScope},
			required:  RoleBuilder, organizationID: orgA, projectID: projA,
			want: AuthorizationDeniedRole,
		},
		{
			name:      "project authorization allows exact match",
			principal: Principal{Role: RoleBuilder, Scope: projectScope},
			required:  RoleBuilder, organizationID: orgA, projectID: projA,
			want: AuthorizationAllowed,
		},
		{
			name:      "organization authorization rejects project scope",
			principal: Principal{Role: RoleBuilder, Scope: projectScope},
			required:  RoleBuilder, organizationID: orgA,
			want: AuthorizationDeniedTenancy,
		},
		{
			name:      "organization authorization denies role",
			principal: Principal{Role: RoleReader, Scope: organizationScope},
			required:  RoleBuilder, organizationID: orgA,
			want: AuthorizationDeniedRole,
		},
		{
			name:      "organization authorization allows organization scope",
			principal: Principal{Role: RoleBuilder, Scope: organizationScope},
			required:  RoleBuilder, organizationID: orgA,
			want: AuthorizationAllowed,
		},
		{
			name:      "visibility authorization root bypasses tenancy and role",
			principal: Principal{Role: RoleRoot, Scope: Scope{}},
			required:  Role("above-root"), organizationID: orgB, visibility: true,
			want: AuthorizationAllowed,
		},
		{
			name:      "visibility authorization denies tenancy before role",
			principal: Principal{Role: RoleReader, Scope: projectScope},
			required:  RoleBuilder, organizationID: orgB, visibility: true,
			want: AuthorizationDeniedTenancy,
		},
		{
			name:      "visibility authorization denies role for project scope",
			principal: Principal{Role: RoleReader, Scope: projectScope},
			required:  RoleBuilder, organizationID: orgA, visibility: true,
			want: AuthorizationDeniedRole,
		},
		{
			name:      "visibility authorization allows project scope",
			principal: Principal{Role: RoleBuilder, Scope: projectScope},
			required:  RoleReader, organizationID: orgA, visibility: true,
			want: AuthorizationAllowed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.principal.Authorize(test.required, test.organizationID, test.projectID)
			if test.visibility {
				got = test.principal.AuthorizeVisibility(test.required, test.organizationID)
			}
			if got != test.want {
				t.Fatalf("authorization verdict = %v, want %v", got, test.want)
			}
		})
	}
}

// A role and a scope that cannot be held together are refused on the way in and
// on the way out of storage.
func TestRoleAndScopeMustAgree(t *testing.T) {
	for _, c := range []struct {
		name  string
		scope Scope
		role  Role
	}{
		{"root outside platform", Scope{OrganizationID: orgA}, RoleRoot},
		{"root in a project", Scope{OrganizationID: orgA, ProjectID: projA}, RoleRoot},
		{"builder at platform scope", Scope{}, RoleBuilder},
		{"maintainer at platform scope", Scope{}, RoleMaintainer},
		{"unknown role", Scope{OrganizationID: orgA}, Role("admin")},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewPrincipal("p", "n", "c", c.scope, c.role, epoch); !errors.Is(err, ErrInvalid) {
				t.Fatalf("NewPrincipal = %v, want ErrInvalid", err)
			}
			if _, err := RestorePrincipal("p", "n", "c", c.scope, c.role, epoch, nil); !errors.Is(err, ErrInvalid) {
				t.Fatalf("RestorePrincipal = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestBucketScopeRoleCeiling(t *testing.T) {
	bucketScope := Scope{OrganizationID: orgA, ProjectID: projA, BucketID: "bucket-1"}
	for _, test := range []struct {
		name        string
		scope       Scope
		role        Role
		wantInvalid bool
	}{
		{"bucket maintainer", bucketScope, RoleMaintainer, true},
		{"bucket publisher", bucketScope, RolePublisher, false},
		{"bucket builder", bucketScope, RoleBuilder, false},
		{"bucket reader", bucketScope, RoleReader, false},
		{"project maintainer", Scope{OrganizationID: orgA, ProjectID: projA}, RoleMaintainer, false},
		{"organization maintainer", Scope{OrganizationID: orgA}, RoleMaintainer, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, newErr := NewPrincipal("p", "n", "c", test.scope, test.role, epoch)
			_, restoreErr := RestorePrincipal("p", "n", "c", test.scope, test.role, epoch, nil)
			if test.wantInvalid {
				if !errors.Is(newErr, ErrInvalid) {
					t.Fatalf("NewPrincipal = %v, want ErrInvalid", newErr)
				}
				if !errors.Is(restoreErr, ErrInvalid) {
					t.Fatalf("RestorePrincipal = %v, want ErrInvalid", restoreErr)
				}
				return
			}
			if newErr != nil {
				t.Fatalf("NewPrincipal = %v, want nil", newErr)
			}
			if restoreErr != nil {
				t.Fatalf("RestorePrincipal = %v, want nil", restoreErr)
			}
		})
	}
}

// A service principal without a role is made impossible rather than handled: it
// could never do anything, so it is a mistake rather than a stage (ADR-0019).
// Refused on the way in AND on the way out of storage, since a row could be
// written by a route the constructor never saw.
func TestARolelessPrincipalCannotExist(t *testing.T) {
	scope := Scope{OrganizationID: orgA, ProjectID: projA}

	if _, err := NewPrincipal("p", "n", "c", scope, Role(""), epoch); !errors.Is(err, ErrInvalid) {
		t.Fatalf("NewPrincipal with no role = %v, want ErrInvalid", err)
	}
	if _, err := RestorePrincipal("p", "n", "c", scope, Role(""), epoch, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("RestorePrincipal with no role = %v, want ErrInvalid", err)
	}
	// And even if one somehow existed, it would grant nothing.
	if Role("").AtLeast(RoleReader) {
		t.Fatal("the empty role satisfies reader")
	}
}

// Organisation-level authority is a different question from project-level, and
// a project-scoped principal must not answer yes to it: it holds authority over
// one project, not over the organisation containing it.
func TestPermitsOrganizationRefusesAProjectScopedPrincipal(t *testing.T) {
	organizationScoped := Scope{OrganizationID: orgA}
	projectScoped := Scope{OrganizationID: orgA, ProjectID: projA}

	if !organizationScoped.PermitsOrganization(orgA) {
		t.Fatal("an organization-scoped principal cannot act on its own organization")
	}
	if projectScoped.PermitsOrganization(orgA) {
		t.Fatal("a project-scoped principal may act at organization level — it could create sibling projects")
	}
	if organizationScoped.PermitsOrganization(orgB) {
		t.Fatal("organization scope crossed organizations")
	}
	if organizationScoped.PermitsOrganization(uuid.Nil) {
		t.Fatal("a zero organization was permitted")
	}

	// Permits still requires a project, which is why the two questions differ.
	if organizationScoped.Permits(orgA, uuid.Nil) {
		t.Fatal("Permits answered true without a project — it should require one")
	}
}

func TestWithinOrganizationIsVisibilityNotOrganizationAuthority(t *testing.T) {
	organizationScoped := Scope{OrganizationID: orgA}
	projectScoped := Scope{OrganizationID: orgA, ProjectID: projA}

	if !organizationScoped.WithinOrganization(orgA) {
		t.Fatal("organization scope is not within its own organization")
	}
	if !projectScoped.WithinOrganization(orgA) {
		t.Fatal("project scope is not within its containing organization")
	}
	if projectScoped.WithinOrganization(orgB) {
		t.Fatal("project scope was visible in another organization")
	}
	if projectScoped.WithinOrganization(uuid.Nil) {
		t.Fatal("project scope was visible in a zero organization")
	}
	if (Scope{}).WithinOrganization(orgA) {
		t.Fatal("platform scope claimed membership in an organization")
	}
	if (Scope{}).WithinOrganization(uuid.Nil) {
		t.Fatal("zero scope claimed membership in a zero organization")
	}
	if projectScoped.PermitsOrganization(orgA) {
		t.Fatal("visibility granted project scope organization-level authority")
	}
}
