package identity

import "fmt"

// Role is what an identity may do within a scope it is entitled to.
//
// Scope answers whether a caller may act on a tenancy; Role answers what they
// may do there. Keeping them separate stops one concept carrying two questions
// (ADR-0019).
//
// The tiers are strictly nested, which is what lets "no role more permissive
// than your own" be an ordering rather than a comparison of capability sets.
type Role string

const (
	// RoleReader sees buckets, versions, builds and unrestricted channels.
	RoleReader Role = "reader"
	// RoleBuilder additionally creates buckets, versions and builds, and consumes
	// restricted channels. This is what a Packer CLI principal holds — the
	// smallest role that completes a build.
	RoleBuilder Role = "builder"
	// RolePublisher additionally assigns channels. Separate from builder because
	// declaring a channel is asking to promote, and a CI credential that can
	// promote straight to production is fine until it is not.
	RolePublisher Role = "publisher"
	// RoleMaintainer additionally manages restricted channels, principals and role
	// bindings within its scope. Named to avoid OpenShift's meaning of "operator".
	RoleMaintainer Role = "maintainer"
	// RoleRoot may do anything, including creating organisations and configuring
	// authentication and audit. Platform scope only.
	RoleRoot Role = "root"
)

// rank orders the tiers. Absent from the map means not a role, which is how an
// unrecognised value from storage or a request fails closed rather than
// comparing as zero and appearing to be the lowest tier.
var rank = map[Role]int{
	RoleReader:     1,
	RoleBuilder:    2,
	RolePublisher:  3,
	RoleMaintainer: 4,
	RoleRoot:       5,
}

// ParseRole converts external input, refusing anything unrecognised.
func ParseRole(value string) (Role, error) {
	role := Role(value)
	if _, ok := rank[role]; !ok {
		return "", fmt.Errorf("%w: unknown role %q", ErrInvalid, value)
	}
	return role, nil
}

// AtLeast reports whether this role includes the authority of another.
//
// An unrecognised role on either side answers false: a value that is not a role
// cannot satisfy a requirement, and cannot be satisfied by one (ADR-0017).
func (r Role) AtLeast(required Role) bool {
	mine, ok := rank[r]
	if !ok {
		return false
	}
	theirs, ok := rank[required]
	if !ok {
		return false
	}
	return mine >= theirs
}

// PlatformOnly reports whether a role may only be held at platform scope.
func (r Role) PlatformOnly() bool { return r == RoleRoot }

// MayGrant reports whether a holder of this role may grant another.
//
// No identity may grant a role more permissive than its own — the ordinary
// escalation defence. Equality is permitted: a maintainer may appoint another
// maintainer, which is how delegation works everywhere.
//
// This answers the LEVEL question only. The caller must separately establish
// that the grantor holds this role IN THE SCOPE being granted, because holding
// maintainer in one organisation confers nothing in another (ADR-0019).
func (r Role) MayGrant(granted Role) bool {
	if _, ok := rank[granted]; !ok {
		return false
	}
	return r.AtLeast(granted)
}

// MayModifyHolderOf reports whether a holder of this role may modify or delete
// an identity holding another.
//
// MayGrant alone stops upward granting but not downward attack: an identity able
// to delete a holder of a higher role has escalated by removal, since taking the
// higher tier out leaves it at the top. Same rationale, separate rule.
func (r Role) MayModifyHolderOf(theirs Role) bool {
	if _, ok := rank[theirs]; !ok {
		// An identity whose stored role is unrecognised cannot be modified by
		// anyone. That is deliberately inconvenient: it should be investigated
		// rather than quietly overwritten.
		return false
	}
	return r.AtLeast(theirs)
}
