package v1

import (
	"context"
	"net/http"
	"time"

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

const (
	sessionReissueWindow = 2 * time.Minute
	sessionAbsoluteCap   = 8 * time.Hour
)

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
// reload and dies with the browser. The token inside is renewed shortly before
// expiry, subject to credential revocation and the absolute session cap.
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

type readSessionRenewed struct {
	token  string
	secure bool
}

func (response readSessionRenewed) VisitReadSessionResponse(w http.ResponseWriter) error {
	http.SetCookie(w, liveSessionCookie(response.token, response.secure))
	return ReadSession200JSONResponse{AccessToken: response.token}.VisitReadSessionResponse(w)
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

// ReadSession exchanges the cookie for a live token. A token nearing expiry is
// renewed behind the cookie while its principal and originating secret remain
// active and the absolute session cap has not elapsed. An invalid or
// non-renewable cookie is cleared and answers 204, not 401.
func (s *server) ReadSession(
	ctx context.Context, _ ReadSessionRequestObject,
) (ReadSessionResponseObject, error) {
	request := sessionRequestFrom(ctx)
	if request.cookie == "" {
		return ReadSession204Response{}, nil
	}
	now := s.now().UTC()
	verified, err := s.auth.Verify(request.cookie)
	if err == nil && verified.ExpiresAt.Sub(now) >= sessionReissueWindow {
		audit.FromContext(ctx).AccessToken(request.cookie)
		return ReadSession200JSONResponse{AccessToken: request.cookie}, nil
	}
	if err != nil {
		verified, err = s.auth.VerifyExpired(request.cookie)
		if err != nil {
			s.auditSessionRenew(ctx, nil, identity.AuditOutcomeRefused, "invalid_signature")
			return readSessionCleared{secure: request.secure}, nil
		}
	}
	principal, err := s.principals.GetPrincipalByID(ctx, verified.PrincipalID)
	if err != nil {
		s.auditSessionRenew(ctx, &verified, identity.AuditOutcomeRefused, "principal_unresolvable")
		return readSessionCleared{secure: request.secure}, nil
	}
	if !principal.HasActiveSecret(verified.SecretID, now) {
		s.auditSessionRenew(ctx, &verified, identity.AuditOutcomeRefused, "revoked_secret")
		return readSessionCleared{secure: request.secure}, nil
	}
	if verified.AuthTime.IsZero() || now.Sub(verified.AuthTime) > sessionAbsoluteCap {
		s.auditSessionRenew(ctx, &verified, identity.AuditOutcomeRefused, "cap_exceeded")
		return readSessionCleared{secure: request.secure}, nil
	}
	renewed, err := s.auth.Reissue(principal, verified.SecretID, verified.AuthTime)
	if err != nil {
		s.auditSessionRenew(ctx, &verified, identity.AuditOutcomeFailure, "token_reissue_failed")
		return readSessionCleared{secure: request.secure}, nil
	}
	if err := s.principals.TouchSecretLastUsed(ctx, verified.SecretID, now); err != nil {
		s.logger.Warn("record principal secret use", "error", err)
	}
	s.auditSessionRenew(ctx, &verified, identity.AuditOutcomeSuccess, "session_renewed")
	audit.FromContext(ctx).AccessToken(renewed)
	return readSessionRenewed{token: renewed, secure: request.secure}, nil
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

func (s *server) auditSessionRenew(
	ctx context.Context, verified *identity.Verified,
	outcome identity.AuditOutcome, reason string,
) {
	s.auditSession(ctx, verified, outcome, reason)
	audit.FromContext(ctx).Enrich(audit.Enrichment{Operation: sessionRenewDescriptor.Operation})
}
