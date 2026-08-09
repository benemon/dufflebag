package v1

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
	if record := trail.response(t); record["access_token_hmac"] == nil {
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

// The session ends when the token does: a cookie holding a token the
// authenticator now refuses is cleared, never returned.
func TestReadSessionClearsAnExpiredCookie(t *testing.T) {
	handler := sessionHandler(t)
	request := httptest.NewRequest(http.MethodGet, SessionPath, nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "expired-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
	if body := recorder.Body.String(); strings.Contains(body, "expired-token") {
		t.Error("the refused token was echoed back")
	}
	cookie := sessionCookieFrom(t, recorder.Result())
	if cookie == nil || cookie.MaxAge >= 0 {
		t.Error("the dead cookie was not cleared")
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
