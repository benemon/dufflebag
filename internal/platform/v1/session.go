package v1

import (
	"context"
	"net/http"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
)

// SessionPath is the console's cookie-session endpoint. Exempt from the bearer
// middleware and enumerated alongside InitPath and StatusPath so the
// unauthenticated surface stays a list someone can read: its credential arrives
// as a cookie (GET, DELETE) or is being minted into one (POST), and each
// handler verifies it itself.
const SessionPath = "/sys/session"

// sessionCookieName carries the console's token between reloads. HttpOnly and
// SameSite=Strict, scoped to SessionPath alone — script cannot read it, and no
// other route ever receives it. The token in memory remains the credential the
// console actually uses; this cookie only lets a reload get it back (duf-1cn).
const sessionCookieName = "dufflebag_session"

type sessionRequestKey struct{}

// sessionRequest is what the middleware saw of a /sys/session request. The
// strict layer does not pass the raw request to handlers, so the pieces they
// need travel in the context.
type sessionRequest struct {
	bearer string
	cookie string
	// secure records whether the request arrived over TLS, directly or via a
	// terminating proxy, so the cookie carries Secure exactly when the browser
	// would accept it.
	secure bool
}

func sessionRequestFrom(ctx context.Context) sessionRequest {
	request, _ := ctx.Value(sessionRequestKey{}).(sessionRequest)
	return request
}

// A session cookie in the browser-lifetime sense: no MaxAge, so it survives
// reload and dies with the browser. Expiry is enforced by verification on read,
// not by cookie lifetime — the token inside is the thing that expires.
func liveSessionCookie(token string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     SessionPath,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	}
}

func clearedSessionCookie(secure bool) *http.Cookie {
	cookie := liveSessionCookie("", secure)
	cookie.MaxAge = -1
	return cookie
}

// The generated 2xx responses cannot carry a Set-Cookie, so these wrappers set
// the cookie and delegate the status line to them.
type createSessionSucceeded struct {
	token  string
	secure bool
}

func (response createSessionSucceeded) VisitCreateSessionResponse(w http.ResponseWriter) error {
	http.SetCookie(w, liveSessionCookie(response.token, response.secure))
	return CreateSession204Response{}.VisitCreateSessionResponse(w)
}

type readSessionCleared struct{ secure bool }

func (response readSessionCleared) VisitReadSessionResponse(w http.ResponseWriter) error {
	http.SetCookie(w, clearedSessionCookie(response.secure))
	return ReadSession204Response{}.VisitReadSessionResponse(w)
}

type deleteSessionDone struct{ secure bool }

func (response deleteSessionDone) VisitDeleteSessionResponse(w http.ResponseWriter) error {
	http.SetCookie(w, clearedSessionCookie(response.secure))
	return DeleteSession204Response{}.VisitDeleteSessionResponse(w)
}

// CreateSession verifies the bearer token and sets it as the session cookie.
// CSRF: a cross-site form cannot set the Authorization header this requires.
func (s *server) CreateSession(
	ctx context.Context, _ CreateSessionRequestObject,
) (CreateSessionResponseObject, error) {
	request := sessionRequestFrom(ctx)
	if request.bearer == "" {
		s.auditSession(ctx, nil, identity.AuditOutcomeRefused, "missing_token")
		return CreateSession401JSONResponse{
			UnauthorizedJSONResponse(Error{Message: "a bearer token is required"}),
		}, nil
	}
	verified, err := s.auth.Verify(request.bearer)
	if err != nil {
		s.auditSession(ctx, nil, identity.AuditOutcomeRefused, "invalid_token")
		return CreateSession401JSONResponse{
			UnauthorizedJSONResponse(Error{Message: "a bearer token is required"}),
		}, nil
	}
	s.auditSession(ctx, &verified, identity.AuditOutcomeSuccess, "session_created")
	audit.FromContext(ctx).AccessToken(request.bearer)
	return createSessionSucceeded{token: request.bearer, secure: request.secure}, nil
}

// ReadSession exchanges the cookie for the token it holds. An expired or
// invalid cookie is cleared and answers 204, not 401: no credential was
// presented wrongly, there is simply nothing to resume — which is also why the
// console can tell "your session ended" apart from a failed sign-in.
func (s *server) ReadSession(
	ctx context.Context, _ ReadSessionRequestObject,
) (ReadSessionResponseObject, error) {
	request := sessionRequestFrom(ctx)
	if request.cookie == "" {
		return ReadSession204Response{}, nil
	}
	if _, err := s.auth.Verify(request.cookie); err != nil {
		// Mirrors the middleware's posture: only refusals are audited, and the
		// common case here is nothing more sinister than a token aging out.
		s.auditSession(ctx, nil, identity.AuditOutcomeRefused, "invalid_token")
		return readSessionCleared{secure: request.secure}, nil
	}
	audit.FromContext(ctx).AccessToken(request.cookie)
	return ReadSession200JSONResponse{AccessToken: request.cookie}, nil
}

// DeleteSession clears the cookie. It works for a caller whose token has
// already expired — ending a session must never require a live credential.
func (s *server) DeleteSession(
	ctx context.Context, _ DeleteSessionRequestObject,
) (DeleteSessionResponseObject, error) {
	request := sessionRequestFrom(ctx)
	var actor *identity.Verified
	if verified, err := s.auth.Verify(request.cookie); request.cookie != "" && err == nil {
		actor = &verified
	}
	s.auditSession(ctx, actor, identity.AuditOutcomeSuccess, "session_deleted")
	return deleteSessionDone{secure: request.secure}, nil
}

// auditSession records a session lifecycle event. The actor is the token's
// subject when one verified, and honestly "unknown" when the request never
// established who was asking.
func (s *server) auditSession(
	ctx context.Context, verified *identity.Verified,
	outcome identity.AuditOutcome, reason string,
) {
	event := audit.Enrichment{
		PrincipalID:  "unknown",
		IdentityKind: identity.IdentityKindAnonymous,
		Scope:        identity.AuditScopePlatform,
		Outcome:      outcome,
		Reason:       reason,
	}
	if verified != nil {
		event.PrincipalID = verified.PrincipalID
		// The name is denormalised into the record at enrichment time, same as
		// every other audited route (duf-9dq): a session event must stay
		// attributable after the principal is deleted. A failed lookup leaves
		// the id standing alone — honest, and never a reason to fail a session.
		if s.principals != nil {
			if principal, err := s.principals.GetPrincipalByID(ctx, verified.PrincipalID); err == nil {
				event.PrincipalName = principal.Name
			}
		}
		event.IdentityKind = identity.IdentityKindServicePrincipal
		if !verified.Scope.PlatformScoped() {
			event.OrganizationID = verified.Scope.OrganizationID.String()
			event.Scope = identity.AuditScopeOrganization
			if !verified.Scope.OrganizationScoped() {
				event.ProjectID = verified.Scope.ProjectID.String()
				event.Scope = identity.AuditScopeProject
			}
		}
	}
	audit.FromContext(ctx).Enrich(event)
}
