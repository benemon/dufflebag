package identity

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// maxActiveSecrets is what makes gapless rotation possible: issue the second,
// redeploy, revoke the first. A third is refused so rotation stays a deliberate
// sequence rather than unbounded accumulation (ADR-0004).
const maxActiveSecrets = 2

// Scope is the tenancy a principal may act within.
//
// Project is optional: with an organization set, the zero project means
// organization-scoped, seeing every project in that organization. Bucket is
// optional too; until bucket-aware enforcement lands, it records a narrower
// binding without changing the project-level authorization predicates. Both
// project and organization scope exist because the Packer CLI distinguishes
// them — a project-scoped principal
// gets 403 from ProjectService_List, which the CLI turns into a
// set-HCP_PROJECT_ID message, while an organization-scoped one seeing several
// projects makes it warn and select the oldest (ADR-0016).
//
// # The zero value is the MOST privileged one
//
// Scope{} is platform scope: above every tenancy, and the store answers it with
// every organisation and every principal on the instance. That is a deny-by-
// default inversion, so it carries a rule — never construct a Scope literally
// and hand it to anything that authorizes. Pass a scope that came from a
// restored Principal, whose construction enforced the binding (duf-ueq).
type Scope struct {
	OrganizationID uuid.UUID
	ProjectID      uuid.UUID
	BucketID       string
}

// OrganizationScoped reports whether this scope spans every project in its
// organization.
func (s Scope) OrganizationScoped() bool {
	return s.OrganizationID != uuid.Nil && s.ProjectID == uuid.Nil
}

// PlatformScoped reports whether this scope sits above every tenancy.
//
// Only `root` may be held here (ADR-0019). Permits deliberately still answers
// false for a platform scope: it asks whether a caller is entitled to a
// TENANCY, and a platform identity is not entitled to one — it outranks the
// question. Principal.May handles that, so the tenancy check keeps a single
// meaning.
func (s Scope) PlatformScoped() bool {
	return s.OrganizationID == uuid.Nil && s.ProjectID == uuid.Nil
}

// Permits reports whether the principal may act on the named tenancy.
//
// This is the authorization primitive the compatibility plane needs: it derives
// its tenant from the request path, so the path must be checked against the
// caller rather than trusted (ADR-0017). Deny by default — an unset scope
// permits nothing.
func (s Scope) Permits(organizationID, projectID uuid.UUID) bool {
	if s.OrganizationID == uuid.Nil || organizationID == uuid.Nil {
		return false
	}
	if s.OrganizationID != organizationID {
		return false
	}
	if s.OrganizationScoped() {
		return projectID != uuid.Nil
	}
	return s.ProjectID == projectID
}

// WithinOrganization reports whether this scope is bound to the organization,
// either directly or through one of its projects.
//
// This is a membership and visibility question only. It does not grant
// authority to act at organization level; PermitsOrganization remains the
// predicate for that stricter question.
func (s Scope) WithinOrganization(organizationID uuid.UUID) bool {
	if s.OrganizationID == uuid.Nil || organizationID == uuid.Nil {
		return false
	}
	return s.OrganizationID == organizationID
}

// PermitsOrganization reports whether this scope may act at ORGANISATION level —
// on the organisation itself, or on the set of its projects.
//
// Distinct from Permits, which asks about a specific project and therefore
// requires one. An operation like "create a project in this organisation" has
// no project yet, so Permits would refuse it for want of an argument that
// cannot exist.
func (s Scope) PermitsOrganization(organizationID uuid.UUID) bool {
	if s.OrganizationID == uuid.Nil || organizationID == uuid.Nil {
		return false
	}
	if s.OrganizationID != organizationID {
		return false
	}
	return s.OrganizationScoped()
}

// Principal is a service identity: a client id, a scope, a role, and its
// secrets.
type Principal struct {
	ID        string
	Name      string
	ClientID  string
	Scope     Scope
	Role      Role
	CreatedAt time.Time

	// secrets is unexported so the active-count invariant cannot be bypassed by
	// appending directly.
	secrets []Secret
}

// AuthorizationVerdict answers the whole authorization question while
// preserving why it was refused.
//
// Authority and tenancy must never be checkable separately. Refusals still
// need to distinguish a tenancy denial, which conceals
// existence, from a role denial within a tenancy the caller may see. The
// verdict carries both truths without separating the checks.
type AuthorizationVerdict int

const (
	// AuthorizationAllowed means both tenancy and role checks passed.
	AuthorizationAllowed AuthorizationVerdict = iota
	// AuthorizationDeniedTenancy conceals a tenancy outside the principal's scope.
	AuthorizationDeniedTenancy
	// AuthorizationDeniedRole means the tenancy is visible but the role is insufficient.
	AuthorizationDeniedRole
)

// NewPrincipal creates a principal holding NO secrets.
//
// Creation and issuance are separate actions (duf-4ac): a principal that exists
// but cannot yet authenticate is an ordinary state, reached deliberately from
// the console and left behind whenever a non-root principal's last secret is
// revoked (ADR-0004, amended 2026-08-02). Call IssueSecret to give it a
// credential.
func NewPrincipal(
	id, name, clientID string, scope Scope, role Role, at time.Time,
) (*Principal, error) {
	switch {
	case id == "":
		return nil, fmt.Errorf("%w: principal id is required", ErrInvalid)
	case name == "":
		return nil, fmt.Errorf("%w: principal name is required", ErrInvalid)
	case clientID == "":
		return nil, fmt.Errorf("%w: client id is required", ErrInvalid)
	}
	if err := validBinding(scope, role); err != nil {
		return nil, err
	}

	return &Principal{
		ID: id, Name: name, ClientID: clientID, Scope: scope, Role: role, CreatedAt: at,
	}, nil
}

// validBinding refuses scope and role combinations the model does not permit.
//
// `root` is platform-only, and nothing else may be held at platform scope: a
// platform-scoped builder would be a principal entitled to no tenancy and able
// to do nothing, which is a configuration error rather than a safe default
// (ADR-0019).
func validBinding(scope Scope, role Role) error {
	if _, err := ParseRole(string(role)); err != nil {
		return err
	}
	// A project without an organization is not a narrower scope, it is a
	// malformed one — and it would pass the platform check below by being
	// neither platform nor tenancy scoped.
	if scope.OrganizationID == uuid.Nil && scope.ProjectID != uuid.Nil {
		return fmt.Errorf("%w: a project scope requires an organization", ErrInvalid)
	}
	if scope.BucketID != "" && scope.ProjectID == uuid.Nil {
		return fmt.Errorf("%w: a bucket scope requires a project", ErrInvalid)
	}
	if scope.BucketID != "" && rank[role] > rank[RolePublisher] {
		return fmt.Errorf("%w: a bucket scope cannot hold %s", ErrInvalid, role)
	}
	if role.PlatformOnly() != scope.PlatformScoped() {
		return fmt.Errorf(
			"%w: role %s and %s scope cannot be held together", ErrInvalid, role, scopeName(scope),
		)
	}
	return nil
}

func scopeName(s Scope) string {
	switch {
	case s.PlatformScoped():
		return "platform"
	case s.OrganizationScoped():
		return "organization"
	default:
		return "project"
	}
}

// RestorePrincipal rebuilds a principal from storage.
func RestorePrincipal(
	id, name, clientID string, scope Scope, role Role, createdAt time.Time, secrets []Secret,
) (*Principal, error) {
	if err := validBinding(scope, role); err != nil {
		return nil, fmt.Errorf("stored principal %s: %w", id, err)
	}
	// Expired secrets stay stored for diagnosability, so the total can
	// legitimately exceed the cap. What can never exceed it is the number of
	// never-expiring secrets — those are usable forever; the clock-dependent
	// half of the cap is enforced where a clock exists, at IssueSecret.
	permanent := 0
	for _, secret := range secrets {
		if secret.ExpiresAt == nil {
			permanent++
		}
	}
	if permanent > maxActiveSecrets {
		return nil, fmt.Errorf(
			"%w: stored principal %s holds %d never-expiring secrets, at most %d are valid",
			ErrInvalid, id, permanent, maxActiveSecrets,
		)
	}
	return &Principal{
		ID: id, Name: name, ClientID: clientID, Scope: scope, Role: role,
		CreatedAt: createdAt, secrets: secrets,
	}, nil
}

// Authorize reports whether this principal may perform an action requiring a
// role on a project or organization tenancy.
func (p *Principal) Authorize(
	required Role, organizationID, projectID uuid.UUID,
) AuthorizationVerdict {
	return p.authorize(required, func(scope Scope) bool {
		if projectID == uuid.Nil {
			return scope.PermitsOrganization(organizationID)
		}
		return scope.Permits(organizationID, projectID)
	})
}

// AuthorizeVisibility reports whether this principal may read an organization
// or a filtered collection within it. A project binding is visible within its
// containing organization without granting organization-level authority.
func (p *Principal) AuthorizeVisibility(
	required Role, organizationID uuid.UUID,
) AuthorizationVerdict {
	return p.authorize(required, func(scope Scope) bool {
		return scope.WithinOrganization(organizationID)
	})
}

func (p *Principal) authorize(required Role, permits func(Scope) bool) AuthorizationVerdict {
	// Root bypasses the tenancy half rather than satisfying it. A platform
	// identity is not entitled to a particular organization — it outranks the
	// question, and stretching the scope predicates to say otherwise would give
	// them two meanings (ADR-0019).
	if p.Role == RoleRoot && p.Scope.PlatformScoped() {
		return AuthorizationAllowed
	}
	if !permits(p.Scope) {
		return AuthorizationDeniedTenancy
	}
	if !p.Role.AtLeast(required) {
		return AuthorizationDeniedRole
	}
	return AuthorizationAllowed
}

// Secrets exposes secret metadata. The plaintext is not recoverable from these.
func (p *Principal) Secrets() []Secret { return append([]Secret(nil), p.secrets...) }

// IssueSecret mints an additional secret, returning the plaintext exactly once.
//
// expiresAt is nil for a credential that never expires. The cap counts USABLE
// secrets: an expired one grants nothing, so requiring its revocation before a
// replacement can be issued would add a step at exactly the moment someone is
// fixing an outage (duf-2rw).
func (p *Principal) IssueSecret(secretID string, expiresAt *time.Time, at time.Time) (string, error) {
	if secretID == "" {
		return "", fmt.Errorf("%w: secret id is required", ErrInvalid)
	}
	if expiresAt != nil {
		// Truncated to the microseconds Postgres stores BEFORE validation, so
		// the expiry that is checked, returned and persisted is one instant —
		// a sub-microsecond future value must not pass here and arrive expired.
		truncated := expiresAt.UTC().Truncate(time.Microsecond)
		expiresAt = &truncated
		if !expiresAt.After(at) {
			return "", fmt.Errorf("%w: a secret cannot be born expired", ErrInvalid)
		}
		// The other half of the revocation guard, or the invariant has a side
		// door: a root's FIRST secret arriving with an expiry creates a root
		// nothing permanent stands behind, and deleting the other root then
		// leaves the instance locked out on a timer (review finding, duf-2rw).
		if p.Role == RoleRoot {
			permanent := false
			for _, existing := range p.secrets {
				if existing.ExpiresAt == nil {
					permanent = true
				}
			}
			if !permanent {
				return "", fmt.Errorf("%w: %w", ErrConflict, ErrRootPermanence)
			}
		}
	}
	usable := 0
	for _, existing := range p.secrets {
		if existing.Usable(at) {
			usable++
		}
	}
	if usable >= maxActiveSecrets {
		return "", fmt.Errorf(
			"%w: principal %s already holds %d usable secrets; revoke one before issuing another",
			ErrConflict, p.ID, maxActiveSecrets,
		)
	}
	for _, existing := range p.secrets {
		if existing.ID == secretID {
			return "", fmt.Errorf("%w: secret %s already exists", ErrConflict, secretID)
		}
	}

	secret, plaintext, err := newSecret(secretID, at, expiresAt)
	if err != nil {
		return "", err
	}
	p.secrets = append(p.secrets, secret)
	return plaintext, nil
}

// RevokeSecret removes a secret, refusing to remove a root's last one.
//
// A principal with no secrets cannot authenticate and cannot mint a replacement
// FOR ITSELF — but it does not have to. A maintainer issues a replacement for
// any tenancy-scoped principal, so leaving one secretless is an ordinary,
// recoverable state.
//
// Root is the exception, and the only one (ADR-0004, amended 2026-08-02).
// Nothing sits above it to re-issue on its behalf, so a root left secretless
// can only be recovered by direct database access — the same failure the
// first-run wizard warns about.
//
// Expiry extends the same rule (duf-2rw): a root left holding only EXPIRING
// secrets is the same lockout on a timer, so a root must always keep at least
// one usable, never-expiring secret.
func (p *Principal) RevokeSecret(secretID string, now time.Time) error {
	if p.Role == RoleRoot {
		survivorNeverExpires := false
		for _, secret := range p.secrets {
			if secret.ID == secretID {
				continue
			}
			if secret.Usable(now) && secret.ExpiresAt == nil {
				survivorNeverExpires = true
			}
		}
		if !survivorNeverExpires {
			return fmt.Errorf(
				"%w: revoking secret %s would leave root principal %s without a usable, never-expiring secret",
				ErrConflict, secretID, p.ID,
			)
		}
	}
	for i, secret := range p.secrets {
		if secret.ID == secretID {
			p.secrets = append(p.secrets[:i:i], p.secrets[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("%w: secret %s", ErrNotFound, secretID)
}

// Authenticate reports whether plaintext matches any active secret.
//
// Every usable slot is checked even after a match, and missing slots are padded
// with a dummy verification, so the work does not reveal which one succeeded
// or whether the principal holds zero, one, or two usable secrets.
// Returns WHICH secret matched, so a token can name the credential that minted
// it and stop working when that credential is revoked (review finding 14).
//
// Expired secrets are skipped entirely rather than checked-then-rejected, then
// their missing usable slots are padded. Presenting a correct-but-expired
// secret therefore takes the same verification path as garbage. Exactly
// maxActiveSecrets verifications also stays within MaxVerificationMemoryBytes,
// whose admission budget already assumes that work (duf-2rw).
func (p *Principal) Authenticate(plaintext string, now time.Time) (string, bool) {
	matchedID := ""
	matched := false
	verified := 0
	for _, secret := range p.secrets {
		if !secret.Usable(now) {
			continue
		}
		verified++
		if secret.matches(plaintext) {
			matchedID = secret.ID
			matched = true
		}
	}
	for ; verified < maxActiveSecrets; verified++ {
		_ = dummySecret.matches(plaintext)
	}
	return matchedID, matched
}

// HasActiveSecret reports whether a secret is still current on this principal.
//
// ADR-0019 resolves authority per request so revocation is immediate; expiry gets the same immediacy — a token
// minted from a secret that has since expired stops working now, not at the
// token's own expiry.
func (p *Principal) HasActiveSecret(id string, now time.Time) bool {
	for _, secret := range p.secrets {
		if secret.ID == id {
			return secret.Usable(now)
		}
	}
	return false
}
