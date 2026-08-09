package hcp2023

import (
	"context"
	"net/http"
	"strings"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// Authenticator verifies a bearer token and reports who presented it.
type Authenticator interface {
	Verify(token string) (identity.Verified, error)
}

// Principals resolves an authenticated caller's authority.
type Principals interface {
	GetPrincipalByID(ctx context.Context, id string) (*identity.Principal, error)
}

type verifiedKey struct{}

// authenticate rejects any request without a valid bearer token.
//
// It authenticates only. Authorization happens in the tenant funnel, because
// the tenant lives in path parameters that the router has not matched yet at
// this point — middleware genuinely cannot see them.
func authenticate(auth Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			audit.FromContext(r.Context()).Enrich(audit.Enrichment{
				PrincipalID: "anonymous", IdentityKind: identity.IdentityKindAnonymous,
				Scope: identity.AuditScopePlatform, Reason: "missing_token",
			})
			unauthenticated(w, "a bearer token is required")
			return
		}
		verified, err := auth.Verify(token)
		if err != nil {
			// Every reason a token can fail — expired, tampered, wrong audience,
			// unparsable — produces one answer. Distinguishing them tells a caller
			// which part of a forgery to fix.
			audit.FromContext(r.Context()).Enrich(audit.Enrichment{
				PrincipalID:  "unknown",
				IdentityKind: identity.IdentityKindUnknown,
				Scope:        identity.AuditScopePlatform,
				Reason:       "invalid_token",
			})
			unauthenticated(w, "the bearer token was not accepted")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), verifiedKey{}, verified)))
	})
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	// RFC 6750 makes the scheme case-insensitive; oauth2.Transport sends
	// "Bearer", but a hand-rolled client may not.
	if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
		return "", false
	}
	token := strings.TrimSpace(header[7:])
	return token, token != ""
}

func unauthenticated(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="dufflebag"`)
	writeRPCError(w, http.StatusUnauthorized, 16, message)
}

type tenantKey struct{}
type principalIDKey struct{}
type principalNameKey struct{}

// scoped authorizes the tenant in the path against the authenticated principal,
// and refuses the request if it does not match.
//
// This is the fix for the hole ADR-0017 names. The tenant is no longer whatever
// the path claims; it is the path CHECKED AGAINST the caller. Row-level security
// proves isolation BETWEEN tenants and says nothing about entitlement TO one, so
// without this the caller simply chose which tenant they were inside.
//
// It runs per-route rather than as middleware because the tenant lives in path
// parameters, and http.ServeMux only populates those once it has matched a
// route — middleware wrapping the mux genuinely cannot see them.
//
// The refusal is not-found rather than forbidden, and carries the code that
// endpoint uses for its own missing resource, so a tenant the caller may not see
// is indistinguishable from one that does not exist. Confirming existence is
// itself a disclosure (ADR-0016).
func (h *handler) scoped(route route, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		verified, ok := r.Context().Value(verifiedKey{}).(identity.Verified)
		if !ok {
			// Reachable only if the authentication middleware was bypassed.
			// Indeterminate is deny (ADR-0017), never fall through to the path.
			unauthenticated(w, "a bearer token is required")
			return
		}

		// Authority is resolved from storage, not read from the token. A token
		// proves who you are; what you may do is looked up now, so a revoked or
		// lowered role takes effect on the next request rather than surviving
		// until the token expires (ADR-0019).
		principal, err := h.principals.GetPrincipalByID(r.Context(), verified.PrincipalID)
		// The same reasoning applied to the CREDENTIAL, not just the role: a
		// token minted from a secret that has since been revoked is refused now
		// rather than at expiry. Revoking a leaked credential and having it keep
		// working for a full TTL is the case ADR-0019 rejects, and it was
		// exactly what revocation did (review finding 14).
		if err == nil && !principal.HasActiveSecret(verified.SecretID, h.now().UTC()) {
			err = identity.ErrNotFound
		}
		if err != nil {
			// A token naming a principal that no longer resolves is refused, not
			// treated as anonymous or as its token claims suggest.
			audit.FromContext(r.Context()).Enrich(audit.Enrichment{
				PrincipalID:    verified.PrincipalID,
				IdentityKind:   identity.IdentityKindUnknown,
				Scope:          identity.AuditScopeProject,
				OrganizationID: r.PathValue("organization"),
				ProjectID:      r.PathValue("project"),
				Reason:         "principal_unresolvable",
			})
			writeRPCError(w, http.StatusNotFound, route.notFoundCode, "not found")
			return
		}
		audit.FromContext(r.Context()).Enrich(audit.Enrichment{
			PrincipalID: principal.ID, PrincipalName: principal.Name,
			IdentityKind:   identity.IdentityKindServicePrincipal,
			Scope:          identity.AuditScopeProject,
			OrganizationID: r.PathValue("organization"), ProjectID: r.PathValue("project"),
		})

		parsed := store.ParseTenant(r.PathValue("organization"), r.PathValue("project"))

		// TENANCY FIRST, and it answers not-found. Whether a tenancy exists is
		// itself a secret: a caller probing identifiers must not be able to tell a
		// real organization it cannot reach from one that does not exist
		// (ADR-0016). Root outranks this question rather than satisfying it.
		switch principal.Authorize(route.required, parsed.OrganizationID, parsed.ProjectID) {
		case identity.AuthorizationDeniedTenancy:
			auditRefusal(r, "outside_principal_scope")
			writeRPCError(w, http.StatusNotFound, route.notFoundCode, "not found")
			return
		case identity.AuthorizationDeniedRole:
			// ROLE SECOND, and it answers forbidden. The caller is bound to this
			// tenancy and may already read it, so there is no existence left to
			// conceal — and answering not-found would send someone hunting for a typo
			// in a name that is correct. Hiding a reason only helps when the reason is
			// a secret.
			auditRefusal(r, "insufficient_role")
			writeRPCError(
				w, http.StatusForbidden, 7,
				"the principal's role does not permit this operation",
			)
			return
		}

		ctx := context.WithValue(r.Context(), tenantKey{}, parsed)
		ctx = context.WithValue(ctx, principalIDKey{}, principal.ID)
		ctx = context.WithValue(ctx, principalNameKey{}, principal.Name)
		next(w, r.WithContext(ctx))
	}
}

// tenant returns the already-authorized tenant.
//
// A handler cannot reach a usable tenant without having passed scoped, so a
// route added later is authorized by construction rather than by someone
// remembering to authorize it. If the value is absent the answer is a denied
// tenant, which every repository operation refuses.
func tenant(r *http.Request) store.Tenant {
	authorized, ok := r.Context().Value(tenantKey{}).(store.Tenant)
	if !ok {
		return store.DeniedTenant()
	}
	return authorized
}

// principalID returns the authenticated caller stamped on created resources and
// manual channel assignment rows. scoped has already proved the value exists
// before a handler can run.
func principalID(r *http.Request) string {
	id, _ := r.Context().Value(principalIDKey{}).(string)
	return id
}

// principalName returns the caller's customer-chosen name — the value the wire
// documents for revocation_author, where an opaque ID would answer "who revoked
// this" with a lookup.
func principalName(r *http.Request) string {
	name, _ := r.Context().Value(principalNameKey{}).(string)
	return name
}

// auditRefusal records a refused request. Both reasons answer the caller
// differently but are recorded identically, because an operator reading the
// trail needs to know whether to grant a role or fix a tenancy.
func auditRefusal(
	r *http.Request, reason string,
) {
	audit.FromContext(r.Context()).Enrich(audit.Enrichment{Reason: reason})
}
