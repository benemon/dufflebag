package v1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/golang-jwt/jwt/v5"
)

const realSessionIssuer = "https://session.test"

var realSessionKey = []byte("session-test-signing-key-32-bytes!")

type sessionPrincipals struct {
	principal *identity.Principal
	touches   int
}

func (p *sessionPrincipals) GetPrincipalByID(_ context.Context, id string) (*identity.Principal, error) {
	if p.principal == nil || p.principal.ID != id {
		return nil, identity.ErrNotFound
	}
	return p.principal, nil
}

func (p *sessionPrincipals) TouchSecretLastUsed(_ context.Context, secretID string, _ time.Time) error {
	if secretID == testSecretID {
		p.touches++
	}
	return nil
}

func realSessionHandler(
	t *testing.T, now time.Time, ttl time.Duration,
) (http.Handler, *identity.BasicAuthIssuer, *sessionPrincipals) {
	t.Helper()
	principal, err := identity.RestorePrincipal(
		testPrincID, "test", "client", identity.Scope{}, identity.RoleRoot,
		initTestTime, testSecrets(),
	)
	if err != nil {
		t.Fatalf("RestorePrincipal: %v", err)
	}
	issuer, err := identity.NewBasicAuthIssuer(realSessionIssuer, realSessionKey, ttl)
	if err != nil {
		t.Fatalf("NewBasicAuthIssuer: %v", err)
	}
	principals := &sessionPrincipals{principal: principal}
	return newHandler(
		&fakeTenancyRepository{}, &fakeInstanceRepository{}, issuer, principals,
		testLogger(), func() time.Time { return now },
	), issuer, principals
}

func signedSessionToken(
	t *testing.T, now, expiresAt, authTime time.Time, secretID string,
) string {
	t.Helper()
	claims := identity.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: realSessionIssuer, Subject: testPrincID,
			Audience:  jwt.ClaimStrings{identity.TokenAudience},
			IssuedAt:  jwt.NewNumericDate(now.Add(-5 * time.Minute)),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
		AuthTime: jwt.NewNumericDate(authTime), SecretID: secretID,
		Scope: []string{}, Grants: []identity.Grant{},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(realSessionKey)
	if err != nil {
		t.Fatalf("sign session token: %v", err)
	}
	return token
}

func sessionHandler(t *testing.T) http.Handler {
	t.Helper()
	return newHandler(
		&fakeTenancyRepository{}, &fakeInstanceRepository{},
		testAuth{}, testRoles{}, testLogger(), time.Now,
	)
}

func sessionCookieFrom(t *testing.T, response *http.Response) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookieName {
			return cookie
		}
	}
	return nil
}

// The cookie is the whole point of the endpoint, so its attributes are the
// contract: httpOnly and SameSite=Strict keep it from script and cross-site
// requests, and the path keeps it off every other route.
func TestCreateSessionSetsAnHTTPOnlyCookie(t *testing.T) {
	handler := sessionHandler(t)
	handler, trail := auditedPlatform(t, handler)
	request := httptest.NewRequest(http.MethodPost, SessionPath, nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
	cookie := sessionCookieFrom(t, recorder.Result())
	if cookie == nil {
		t.Fatal("no session cookie was set")
	}
	if cookie.Value != testToken {
		t.Errorf("cookie value = %q, want the verified token", cookie.Value)
	}
	if !cookie.HttpOnly {
		t.Error("cookie is not HttpOnly, so script can read it")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", cookie.SameSite)
	}
	if cookie.Path != SessionPath {
		t.Errorf("Path = %q, want %q so no other route receives the credential", cookie.Path, SessionPath)
	}
	record := trail.response(t)
	for field, want := range map[string]any{
		"operation": "session.create", "target_type": "session", "target_id": SessionPath,
		"principal_id": testPrincID, "principal_name": "test",
		"identity_kind": "service_principal", "scope": "platform",
		"outcome": "success", "reason": "session_created",
	} {
		if record[field] != want {
			t.Errorf("session creation %s = %v, want %v; record %#v", field, record[field], want, record)
		}
	}
	for _, absent := range []string{"organization_id", "project_id"} {
		if _, ok := record[absent]; ok {
			t.Errorf("session creation unexpectedly carries %s: %#v", absent, record)
		}
	}
	if record["access_token_hmac"] == nil {
		t.Fatalf("session creation did not HMAC the persisted access token: %#v", record)
	}
	if strings.Contains(string(trail.raw), testToken) {
		t.Fatalf("session creation wrote the raw access token to audit: %s", trail.raw)
	}
}

// The guard fires: a token the authenticator refuses mints no cookie. Without
// this, the endpoint would launder any string into a persisted credential.
func TestCreateSessionRefusesAnUnverifiedToken(t *testing.T) {
	handler := sessionHandler(t)
	for name, authorization := range map[string]string{
		"missing bearer": "",
		"invalid token":  "Bearer not-the-token",
	} {
		request := httptest.NewRequest(http.MethodPost, SessionPath, nil)
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, recorder.Code)
		}
		if cookie := sessionCookieFrom(t, recorder.Result()); cookie != nil {
			t.Errorf("%s: a cookie was set for a refused token", name)
		}
	}
}

func TestReadSessionExchangesTheCookieForItsToken(t *testing.T) {
	handler := sessionHandler(t)
	handler, trail := auditedPlatform(t, handler)
	request := httptest.NewRequest(http.MethodGet, SessionPath, nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, testToken) {
		t.Errorf("body %q does not carry the token", body)
	}
	if cookie := sessionCookieFrom(t, recorder.Result()); cookie != nil {
		t.Fatal("a live token outside the renewal window unexpectedly set a new cookie")
	}
	if record := trail.response(t); record["access_token_hmac"] == nil || record["operation"] != "session.read" {
		t.Fatalf("session read did not HMAC the returned access token: %#v", record)
	}
	if strings.Contains(string(trail.raw), testToken) {
		t.Fatalf("session read wrote the raw access token to audit: %s", trail.raw)
	}
}

// No cookie answers 204, not 401: nothing was presented wrongly, there is
// simply nothing to resume. The console reads this as "fresh arrival".
func TestReadSessionWithoutACookieIsEmpty(t *testing.T) {
	handler := sessionHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, SessionPath, nil))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
	if cookie := sessionCookieFrom(t, recorder.Result()); cookie != nil {
		t.Error("a clearing cookie was sent when there was nothing to clear")
	}
}

func TestReadSessionRenewsAnExpiredValidCookie(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	handler, _, principals := realSessionHandler(t, now, 300*time.Second)
	handler, trail := auditedPlatform(t, handler)
	expired := signedSessionToken(t, now, now.Add(-time.Minute), now.Add(-time.Hour), testSecretID)
	request := httptest.NewRequest(http.MethodGet, SessionPath, nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: expired})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "access_token") || strings.Contains(body, expired) {
		t.Fatalf("renewal body = %q, want a new token", body)
	}
	cookie := sessionCookieFrom(t, recorder.Result())
	if cookie == nil || cookie.Value == expired || cookie.MaxAge != 0 {
		t.Fatal("the renewed token was not set as a browser-session cookie")
	}
	if principals.touches != 1 {
		t.Fatalf("secret touches = %d, want 1", principals.touches)
	}
	record := trail.response(t)
	if record["operation"] != "session.renew" || record["outcome"] != "success" || record["access_token_hmac"] == nil {
		t.Fatalf("renewal audit = %#v", record)
	}
	if strings.Contains(string(trail.raw), expired) || strings.Contains(string(trail.raw), cookie.Value) {
		t.Fatalf("renewal wrote a raw token to audit: %s", trail.raw)
	}
}

func TestReadSessionRenewsANearExpiryLiveToken(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	handler, _, _ := realSessionHandler(t, now, 300*time.Second)
	near := signedSessionToken(t, now, now.Add(time.Minute), now.Add(-time.Hour), testSecretID)
	request := httptest.NewRequest(http.MethodGet, SessionPath, nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: near})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if cookie := sessionCookieFrom(t, recorder.Result()); cookie == nil || cookie.Value == near {
		t.Fatal("near-expiry token was not renewed")
	}
}

func TestReadSessionRefusesARevokedSecretRenewal(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	handler, _, _ := realSessionHandler(t, now, 300*time.Second)
	handler, trail := auditedPlatform(t, handler)
	token := signedSessionToken(t, now, now.Add(-time.Minute), now.Add(-time.Hour), "revoked-secret")
	request := httptest.NewRequest(http.MethodGet, SessionPath, nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
	if cookie := sessionCookieFrom(t, recorder.Result()); cookie == nil || cookie.MaxAge >= 0 {
		t.Fatal("revoked-secret cookie was not cleared")
	}
	record := trail.response(t)
	if record["operation"] != "session.renew" || record["outcome"] != "refused" || record["reason"] != "revoked_secret" {
		t.Fatalf("revoked renewal audit = %#v", record)
	}
}

func TestReadSessionRefusesAReissuedTokenPastTheAbsoluteCap(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	handler, issuer, principals := realSessionHandler(t, now, time.Minute)
	token, err := issuer.Reissue(principals.principal, testSecretID, now.Add(-9*time.Hour))
	if err != nil {
		t.Fatalf("Reissue test token: %v", err)
	}
	handler, trail := auditedPlatform(t, handler)
	request := httptest.NewRequest(http.MethodGet, SessionPath, nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
	record := trail.response(t)
	if record["reason"] != "cap_exceeded" || record["operation"] != "session.renew" {
		t.Fatalf("cap refusal audit = %#v", record)
	}
}

func TestReadSessionClearsABadSignature(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	handler, _, _ := realSessionHandler(t, now, 300*time.Second)
	handler, trail := auditedPlatform(t, handler)
	request := httptest.NewRequest(http.MethodGet, SessionPath, nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "not-a-signed-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
	record := trail.response(t)
	if record["reason"] != "invalid_signature" || record["operation"] != "session.renew" {
		t.Fatalf("signature refusal audit = %#v", record)
	}
}

func TestDeleteSessionClearsTheCookie(t *testing.T) {
	handler := sessionHandler(t)
	request := httptest.NewRequest(http.MethodDelete, SessionPath, nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
	cookie := sessionCookieFrom(t, recorder.Result())
	if cookie == nil || cookie.MaxAge >= 0 {
		t.Error("the cookie was not cleared")
	}
}

// A session event stays attributable when the name cannot be resolved: the id
// stands alone rather than the session failing or the record fabricating a
// name (duf-9dq). The unresolvable-principal path is real — a principal can be
// deleted while its token is still verifying.
func TestCreateSessionSurvivesAnUnresolvableName(t *testing.T) {
	handler := newHandler(
		&fakeTenancyRepository{}, &fakeInstanceRepository{},
		testAuth{}, testRoles{missing: true}, testLogger(), time.Now,
	)
	handler, trail := auditedPlatform(t, handler)
	request := httptest.NewRequest(http.MethodPost, SessionPath, nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: a failed name lookup must not fail the session", recorder.Code)
	}
	record := trail.response(t)
	if record["principal_id"] != testPrincID {
		t.Errorf("principal_id = %v, want %v", record["principal_id"], testPrincID)
	}
	if name, ok := record["principal_name"]; ok && name != "" {
		t.Errorf("principal_name = %v, want absent when the lookup fails", name)
	}
}
