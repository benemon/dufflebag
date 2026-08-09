package v1

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/google/uuid"
)

// Authenticator verifies a bearer token and reports who presented it.
type Authenticator interface {
	Verify(token string) (identity.Verified, error)
}

// Principals resolves an authenticated caller's authority, per request rather
// than from the token (ADR-0019).
type Principals interface {
	GetPrincipalByID(ctx context.Context, id string) (*identity.Principal, error)
}

type principalKey struct{}

// InitPath is the one route on this plane that does not authenticate.
//
// Requiring a credential to obtain the first credential is a loop. It defends
// itself by refusing permanently once the instance is claimed (ADR-0012), and
// it is enumerated here rather than left implicit so the unauthenticated
// surface is a list someone can read.
const InitPath = "/sys/init"

// RecoveryPath is unauthenticated for the same reason as InitPath: it exists
// for the caller whose credential is lost. The shares are the credential, the
// verifier is checked server-side, and the surface sits behind the token
// endpoint's per-caller rate limit (ADR-0024).
const RecoveryPath = "/sys/recovery"

// StatusPath is the unauthenticated health probe. Exempt for the same reason
// as InitPath and one more: a Kubernetes probe holds no credential, and a
// readiness check that could fail on authentication would report the wrong
// thing about the instance (ADR-0005's rolling-deploy posture).
const StatusPath = "/sys/health"

// authenticate resolves the caller for every route except initialization.
//
// Resolution happens here and the principal travels in the context, so an
// operation cannot serve a request without one — a handler that forgets to
// authorize still cannot obtain a caller to act on behalf of.
func authenticate(
	auth Authenticator, principals Principals, now func() time.Time, next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == InitPath || r.URL.Path == RecoveryPath || r.URL.Path == StatusPath {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == SessionPath {
			// The session endpoints authenticate themselves (see SessionPath).
			// The strict layer does not hand them the raw request, so what they
			// need from it travels in the context.
			token, _ := bearerToken(r)
			value := ""
			if cookie, err := r.Cookie(sessionCookieName); err == nil {
				value = cookie.Value
			}
			secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
			session := sessionRequest{bearer: token, cookie: value, secure: secure}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionRequestKey{}, session)))
			return
		}

		token, ok := bearerToken(r)
		if !ok {
			auditUnauthenticated(r, "missing_token")
			unauthenticated(w)
			return
		}
		verified, err := auth.Verify(token)
		if err != nil {
			auditUnauthenticated(r, "invalid_token")
			unauthenticated(w)
			return
		}
		principal, err := principals.GetPrincipalByID(r.Context(), verified.PrincipalID)
		// A token minted from a since-revoked credential is refused now rather
		// than at expiry (review finding 14).
		if err == nil && !principal.HasActiveSecret(verified.SecretID, now().UTC()) {
			auditUnauthenticated(r, "credential_revoked")
			unauthenticated(w)
			return
		}
		if err != nil {
			// A token naming a principal that no longer resolves is refused, not
			// treated as anonymous (ADR-0017). A row failing keyring MAC
			// verification (ADR-0024) is named distinctly in the trail while the
			// wire answer stays the same 401.
			reason := "principal_unresolvable"
			if errors.Is(err, identity.ErrIntegrity) {
				reason = "identity_integrity_failed"
			}
			auditUnauthenticated(r, reason)
			unauthenticated(w)
			return
		}
		audit.FromContext(r.Context()).Enrich(audit.Enrichment{
			PrincipalID: principal.ID, PrincipalName: principal.Name,
			IdentityKind: identity.IdentityKindServicePrincipal,
		})
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal)))
	})
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
		return "", false
	}
	token := strings.TrimSpace(header[7:])
	return token, token != ""
}

func unauthenticated(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="dufflebag"`)
	writeError(w, http.StatusUnauthorized, Error{Message: "a bearer token is required"})
}

// authorize reports whether the caller may perform an action on a tenancy.
//
// Returns the two refusals separately because they are answered differently: a
// tenancy the caller may not see is not-found, since confirming a tenancy
// exists is a disclosure; a role it lacks within a tenancy it can see is
// forbidden, since there is nothing left to conceal (ADR-0017).
type refusal int

const (
	permitted refusal = iota
	refusedTenancy
	refusedRole
)

// reason names the refusal for the audit trail. Distinguishing the two matters
// in a record: a tenancy refusal says the caller could not see the thing at all,
// a role refusal says they could see it and lacked the authority (ADR-0017).
func (r refusal) reason() string {
	switch r {
	case refusedTenancy:
		return "tenancy_refused"
	case refusedRole:
		return "role_refused"
	default:
		return "permitted"
	}
}

// callerFrom returns the authenticated principal. Absent only if the
// authentication middleware was bypassed, which is indeterminate and therefore
// deny.
func callerFrom(ctx context.Context) (*identity.Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(*identity.Principal)
	return principal, ok
}

// authorizePlatform authorizes an operation that belongs to no tenancy —
// listing organisations, creating one, configuring the instance.
//
// Separate from authorizeTenancy rather than expressed as "no tenancy" through
// the same function. An earlier version took organisation and project strings
// and skipped the tenancy check whenever the organisation was empty, so a
// handler that meant "this is instance-scoped" and one that forgot to pass a
// tenancy were indistinguishable — and the second silently authorized nothing.
// Two functions make the caller say which it means.
func authorizePlatform(ctx context.Context, required identity.Role) (*identity.Principal, refusal) {
	principal, ok := callerFrom(ctx)
	if !ok {
		// Reachable only if the middleware was bypassed. Indeterminate is deny.
		return nil, refusedTenancy
	}
	audit.FromContext(ctx).Enrich(audit.Enrichment{
		PrincipalID: principal.ID, PrincipalName: principal.Name,
		IdentityKind: identity.IdentityKindServicePrincipal,
		Scope:        identity.AuditScopePlatform,
	})
	if !principal.Role.AtLeast(required) {
		audit.FromContext(ctx).Enrich(audit.Enrichment{Reason: refusedRole.reason()})
		return nil, refusedRole
	}
	return principal, permitted
}

// authorizeTenancy authorizes an operation on a specific tenancy.
//
// organizationID is REQUIRED. An empty one is refused rather than waved
// through: a tenancy-scoped operation that does not know its tenancy is a
// programming error, and deny by default applies to programming errors too
// (ADR-0017). projectID may be empty, which means the operation is
// organisation-level.
func authorizeTenancy(
	ctx context.Context, required identity.Role, organizationID, projectID string,
) (*identity.Principal, refusal) {
	principal, ok := ctx.Value(principalKey{}).(*identity.Principal)
	if !ok {
		return nil, refusedTenancy
	}
	scope := identity.AuditScopeOrganization
	if projectID != "" {
		scope = identity.AuditScopeProject
	}
	audit.FromContext(ctx).Enrich(audit.Enrichment{
		PrincipalID: principal.ID, PrincipalName: principal.Name,
		IdentityKind: identity.IdentityKindServicePrincipal,
		Scope:        scope, OrganizationID: organizationID, ProjectID: projectID,
	})

	// Tenancy first: it is the stronger secret. A caller outside a tenancy must
	// not learn that its role would also have been insufficient.
	organization, err := uuid.Parse(organizationID)
	if err != nil {
		return nil, refusedTenancy
	}
	project := uuid.Nil
	if projectID != "" {
		if project, err = uuid.Parse(projectID); err != nil {
			return nil, refusedTenancy
		}
	}

	switch principal.Authorize(required, organization, project) {
	case identity.AuthorizationDeniedTenancy:
		audit.FromContext(ctx).Enrich(audit.Enrichment{Reason: refusedTenancy.reason()})
		return nil, refusedTenancy
	case identity.AuthorizationDeniedRole:
		audit.FromContext(ctx).Enrich(audit.Enrichment{Reason: refusedRole.reason()})
		return nil, refusedRole
	}
	return principal, permitted
}

// authorizeOrganizationVisibility authorizes reading an organization or a
// filtered collection within it. A project binding is inside its organization
// for visibility, without carrying authority to act at organization level.
func authorizeOrganizationVisibility(
	ctx context.Context, required identity.Role, organization uuid.UUID,
) (*identity.Principal, refusal) {
	principal, ok := ctx.Value(principalKey{}).(*identity.Principal)
	if !ok {
		return nil, refusedTenancy
	}
	audit.FromContext(ctx).Enrich(audit.Enrichment{
		PrincipalID: principal.ID, PrincipalName: principal.Name,
		IdentityKind: identity.IdentityKindServicePrincipal,
		Scope:        identity.AuditScopeOrganization, OrganizationID: organization.String(),
	})

	if organization == uuid.Nil {
		return nil, refusedTenancy
	}
	switch principal.AuthorizeVisibility(required, organization) {
	case identity.AuthorizationDeniedTenancy:
		audit.FromContext(ctx).Enrich(audit.Enrichment{Reason: refusedTenancy.reason()})
		return nil, refusedTenancy
	case identity.AuthorizationDeniedRole:
		audit.FromContext(ctx).Enrich(audit.Enrichment{Reason: refusedRole.reason()})
		return nil, refusedRole
	}
	return principal, permitted
}

// authorizeScope authorizes an operation on a tenancy that may be the PLATFORM.
//
// A platform-scoped subject belongs to no tenancy, so there is no tenancy check
// that could constrain access to it — which is exactly how a maintainer of any
// organisation could read a root principal's record, including its client id
// (review finding 9).
//
// Two questions in ADR-0017's order, not one. STANDING: is the caller at
// platform scope at all? A tenancy-bound caller is not, and is refused as a
// tenancy failure, so it cannot tell a platform subject from an identifier that
// names nothing. AUTHORITY: does it hold root, the only role that may be held
// there (ADR-0019)? Only a platform-scoped caller is asked, and by then
// existence is no longer a secret from it.
//
// Collapsing these into the role question alone was the first fix, and it left
// a 403-versus-404 oracle behind the closed disclosure.
func authorizeScope(
	ctx context.Context, required identity.Role, scope identity.Scope,
) (*identity.Principal, refusal) {
	if scope.PlatformScoped() {
		caller, ok := callerFrom(ctx)
		if !ok {
			return nil, refusedTenancy
		}
		// Standing before authority, which is ADR-0017's ordering and the reason
		// it exists. A caller bound to a tenancy has no standing at platform
		// scope AT ALL, so its failure is a tenancy failure and answers
		// not-found — indistinguishable from an identifier that names nothing.
		//
		// Asking the role question first answered 403 here, and a nonexistent
		// identifier answers 404, so the pair confirmed the subject exists.
		// Finding 9 closed the contents; this closes the shape.
		if !caller.Scope.PlatformScoped() {
			return nil, refusedTenancy
		}
		return authorizePlatform(ctx, identity.RoleRoot)
	}
	organizationID := ""
	if scope.OrganizationID != uuid.Nil {
		organizationID = scope.OrganizationID.String()
	}
	projectID := ""
	if scope.ProjectID != uuid.Nil {
		projectID = scope.ProjectID.String()
	}
	return authorizeTenancy(ctx, required, organizationID, projectID)
}

// refusalResponse renders a refusal for any operation.
//
// A hand-written response rather than the generated per-operation types,
// because not every operation declares a 404 — and correctly so: an operation
// with no tenancy in its path has no existence to conceal, so it can only ever
// be role-refused. One type keeps the two refusals rendering identically
// wherever they occur.
type refusalResponse struct {
	status  int
	message string
}

func newRefusal(r refusal) refusalResponse {
	if r == refusedRole {
		return refusalResponse{
			status:  http.StatusForbidden,
			message: "the principal's role does not permit this operation",
		}
	}
	// A tenancy the caller may not see is indistinguishable from one that does
	// not exist (ADR-0016).
	return refusalResponse{status: http.StatusNotFound, message: "not found"}
}

func (response refusalResponse) write(w http.ResponseWriter) error {
	writeError(w, response.status, Error{Message: response.message})
	return nil
}

func (response refusalResponse) VisitGetScannerHealthResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitRescanBuildResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitListOrganizationsResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitCreateOrganizationResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitGetOrganizationResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitDeleteOrganizationResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitListProjectsResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitCreateProjectResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitGetProjectResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitDeleteProjectResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitListPinsResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitSetPinResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitDeletePinResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitListAuditTargetsResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitCreateAuditTargetResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitDeleteAuditTargetResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitGetEncryptionResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitRewrapEncryptionResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitRotateEncryptionResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitGetInstanceResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitGetSelfResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitRevokePrincipalSecretResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitListPrincipalsResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitCreatePrincipalResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitGetPrincipalResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitDeletePrincipalResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitCreatePrincipalSecretResponse(w http.ResponseWriter) error {
	return response.write(w)
}
func (response refusalResponse) VisitDeletePrincipalSecretResponse(w http.ResponseWriter) error {
	return response.write(w)
}

// lifecycleAudit accumulates one response refinement and applies it exactly
// once, on return, however the handler exits.
//
// Deliberately NOT a call at each interesting branch. The first version of this
// emitted from seven chosen points and missed eleven others — every
// authorizeScope refusal, self-deletion, and all three conflicts — so the
// finding-9 attack itself, a maintainer probing a root principal's identifier,
// left no entry at all. Choosing which branches to audit means being right about
// every branch, including the ones added later.
//
// Defaulting to REFUSED matters as much as the defer. A path that returns
// without setting an outcome records a refusal rather than nothing, so the
// failure mode of forgetting is an over-recorded entry, not a missing one.
//
// Same shape as the token endpoint (internal/compat/hcpauth): refine at the
// boundary of the operation, not at each decision inside it.
type lifecycleAudit struct {
	event audit.Enrichment
}

func (s *server) beginLifecycleAudit() *lifecycleAudit {
	return &lifecycleAudit{
		event: audit.Enrichment{
			Outcome: identity.AuditOutcomeRefused,
			Reason:  "unauthorized",
		},
	}
}

// actor names who is acting. Until this is called the entry says "unknown",
// which is the honest answer for a request refused before the caller was
// resolved.
func (a *lifecycleAudit) actor(caller *identity.Principal) {
	a.event.PrincipalID = caller.ID
	a.event.PrincipalName = caller.Name
	if caller.Scope.PlatformScoped() {
		return
	}
	a.event.OrganizationID = caller.Scope.OrganizationID.String()
	a.event.Scope = identity.AuditScopeOrganization
	if !caller.Scope.OrganizationScoped() {
		a.event.ProjectID = caller.Scope.ProjectID.String()
		a.event.Scope = identity.AuditScopeProject
	}
}

func (a *lifecycleAudit) refused(reason string) {
	a.event.Outcome = identity.AuditOutcomeRefused
	a.event.Reason = reason
}

// failed marks an entry the operation could not complete for a reason that is
// not a refusal — a conflict, or storage saying no.
func (a *lifecycleAudit) failed(reason string) {
	a.event.Outcome = identity.AuditOutcomeFailure
	a.event.Reason = reason
}

// succeeded takes the target identifier because it is often only known once the
// operation has happened — a created principal, an issued secret. NEVER the
// credential itself: the plaintext leaves in the response body and nowhere else
// (ADR-0012, ADR-0020).
func (a *lifecycleAudit) succeeded(targetID, reason string) {
	a.event.Outcome = identity.AuditOutcomeSuccess
	a.event.Reason = reason
	if targetID != "" {
		a.event.TargetID = targetID
	}
}

func (a *lifecycleAudit) log(ctx context.Context) {
	audit.FromContext(ctx).Enrich(a.event)
}

// auditMalformedRequest refines a request the server could not decode. Its
// operation still comes from the pre-authentication descriptor because the
// handler never ran.
func auditMalformedRequest(r *http.Request) {
	event := audit.Enrichment{
		PrincipalID:  "unknown",
		IdentityKind: identity.IdentityKindUnknown,
		Scope:        identity.AuditScopePlatform,
		Reason:       "undecodable_request",
	}
	// Authentication wraps the routed handler, so a caller is normally present.
	// "unknown" survives only for a path that bypassed it.
	if caller, ok := callerFrom(r.Context()); ok {
		event.PrincipalID = caller.ID
		event.PrincipalName = caller.Name
		event.IdentityKind = identity.IdentityKindServicePrincipal
		if !caller.Scope.PlatformScoped() {
			event.OrganizationID = caller.Scope.OrganizationID.String()
			event.Scope = identity.AuditScopeOrganization
			if !caller.Scope.OrganizationScoped() {
				event.ProjectID = caller.Scope.ProjectID.String()
				event.Scope = identity.AuditScopeProject
			}
		}
	}
	audit.FromContext(r.Context()).Enrich(event)
}

// auditUnauthenticated records a refusal that happened BEFORE the caller was
// established — a missing or invalid token, a principal that no longer
// resolves, a credential since revoked.
//
// These were free-form logger.Warn calls, which the compatibility planes had
// already outgrown: both now refine the ordinary response record for the
// identical failure. A trail that records the same event two different ways on
// two planes cannot be queried as one thing (duf-i2u).
//
// # This surface is anonymous, and that is deliberate
//
// ADR-0020 fails closed when an entry cannot be written, and applying that to a
// surface an anonymous caller can drive means the rate at which audit must
// absorb writes is set by an attacker. That is only survivable because the token
// endpoint is rate-limited per caller: the cap on anonymous request rate IS the
// cap on anonymous audit volume. See the amendment to ADR-0020 — the coupling is
// load-bearing, and relaxing the throttle for performance reopens it.
//
// Never the token itself, only why it failed. Distinguishing the reasons in the
// TRAIL while answering the caller identically is the point: the operator needs
// to tell a probe from an expired credential, and the caller must not.
func auditUnauthenticated(
	r *http.Request, reason string,
) {
	audit.FromContext(r.Context()).Enrich(audit.Enrichment{
		PrincipalID:  "unknown",
		IdentityKind: identity.IdentityKindAnonymous,
		Scope:        identity.AuditScopePlatform,
		Reason:       reason,
	})
}
