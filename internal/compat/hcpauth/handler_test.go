package hcpauth_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/compat/hcpauth"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/google/uuid"
)

const (
	clientID = "client-abc"
	orgID    = "00000000-0000-4000-8000-000000000001"
)

type principals struct {
	principal *identity.Principal
	err       error
	touch     func(context.Context, string, time.Time) error
}

func (p principals) TouchSecretLastUsed(ctx context.Context, secretID string, at time.Time) error {
	if p.touch != nil {
		return p.touch(ctx, secretID, at)
	}
	return nil
}

func (p principals) GetPrincipalByClientID(_ context.Context, id string) (*identity.Principal, error) {
	if p.err != nil {
		return nil, p.err
	}
	if p.principal == nil || id != p.principal.ClientID {
		return nil, identity.ErrNotFound
	}
	return p.principal, nil
}

func newServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	return newServerWithLogger(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func newServerWithLogger(t *testing.T, logger *slog.Logger) (http.Handler, string) {
	t.Helper()
	principal, err := identity.NewPrincipal(
		"p-1", "ci", clientID,
		identity.Scope{OrganizationID: uuid.MustParse(orgID)},
		identity.RoleBuilder,
		time.Now(),
	)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	secret, err := principal.IssueSecret("s-1", nil, time.Now())
	if err != nil {
		t.Fatalf("IssueSecret: %v", err)
	}
	issuer, err := identity.NewBasicAuthIssuer("https://dufflebag.test", []byte("0123456789abcdef0123456789abcdef"), 5*time.Minute)
	if err != nil {
		t.Fatalf("NewBasicAuthIssuer: %v", err)
	}
	return hcpauth.NewHandler(principals{principal: principal}, issuer, logger), secret
}

func post(t *testing.T, h http.Handler, form url.Values, basic [2]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, hcpauth.TokenPath, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if basic[0] != "" || basic[1] != "" {
		r.SetBasicAuth(basic[0], basic[1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func clientCredentials() url.Values {
	return url.Values{
		"grant_type": {"client_credentials"},
		"audience":   {identity.TokenAudience},
	}
}

// The SDK uses AuthStyleInHeader, so this is the path that actually matters.
func TestTokenViaAuthorizationHeader(t *testing.T) {
	h, secret := newServer(t)

	w := post(t, h, clientCredentials(), [2]string{clientID, secret})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store — a token must not be cached", got)
	}

	var body struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.TokenType != "Bearer" {
		t.Fatalf("token_type = %q, want Bearer", body.TokenType)
	}
	if body.ExpiresIn != 300 {
		t.Fatalf("expires_in = %d, want 300", body.ExpiresIn)
	}
	if body.AccessToken == "" {
		t.Fatal("no access token returned")
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("the response echoes the client secret")
	}
}

func TestTokenViaFormFields(t *testing.T) {
	h, secret := newServer(t)
	form := clientCredentials()
	form.Set("client_id", clientID)
	form.Set("client_secret", secret)

	if w := post(t, h, form, [2]string{}); w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestSuccessfulTokenTouchesOnlyTheMatchedSecret(t *testing.T) {
	principal, err := identity.NewPrincipal(
		"p-1", "ci", clientID,
		identity.Scope{OrganizationID: uuid.MustParse(orgID)}, identity.RoleBuilder, time.Now(),
	)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	if _, err := principal.IssueSecret("s-1", nil, time.Now()); err != nil {
		t.Fatalf("IssueSecret first: %v", err)
	}
	matchedPlaintext, err := principal.IssueSecret("s-2", nil, time.Now())
	if err != nil {
		t.Fatalf("IssueSecret second: %v", err)
	}
	issuer, err := identity.NewBasicAuthIssuer(
		"https://dufflebag.test", []byte("0123456789abcdef0123456789abcdef"), 5*time.Minute,
	)
	if err != nil {
		t.Fatalf("NewBasicAuthIssuer: %v", err)
	}
	var touched []string
	store := principals{
		principal: principal,
		touch: func(_ context.Context, secretID string, _ time.Time) error {
			touched = append(touched, secretID)
			return nil
		},
	}
	h := hcpauth.NewHandler(store, issuer, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if w := post(t, h, clientCredentials(), [2]string{clientID, matchedPlaintext}); w.Code != http.StatusOK {
		t.Fatalf("successful token status = %d: %s", w.Code, w.Body)
	}
	if len(touched) != 1 || touched[0] != "s-2" {
		t.Fatalf("touched secrets = %#v, want exactly s-2", touched)
	}
	if w := post(t, h, clientCredentials(), [2]string{clientID, "wrong"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("failed token status = %d: %s", w.Code, w.Body)
	}
	if len(touched) != 1 {
		t.Fatalf("failed authentication touched secrets: %#v", touched)
	}

	store.touch = func(context.Context, string, time.Time) error {
		return errors.New("database unavailable")
	}
	h = hcpauth.NewHandler(store, issuer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if w := post(t, h, clientCredentials(), [2]string{clientID, matchedPlaintext}); w.Code != http.StatusOK {
		t.Fatalf("touch failure changed token response to %d: %s", w.Code, w.Body)
	}
}

// An unknown client and a wrong secret must be indistinguishable, or valid
// client ids can be enumerated through the token endpoint.
func TestBadCredentialsAreIndistinguishable(t *testing.T) {
	h, secret := newServer(t)

	unknown := post(t, h, clientCredentials(), [2]string{"no-such-client", secret})
	wrong := post(t, h, clientCredentials(), [2]string{clientID, "not-the-secret"})

	for name, w := range map[string]*httptest.ResponseRecorder{"unknown client": unknown, "wrong secret": wrong} {
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", name, w.Code)
		}
		if w.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("%s: 401 carries no challenge", name)
		}
	}
	if unknown.Body.String() != wrong.Body.String() {
		t.Fatalf("responses differ:\n unknown: %s\n wrong:   %s", unknown.Body.String(), wrong.Body.String())
	}
}

func TestRequestsThatAreNotClientCredentials(t *testing.T) {
	h, secret := newServer(t)

	for _, c := range []struct {
		name   string
		mutate func(url.Values)
		status int
		code   string
	}{
		{"wrong grant", func(f url.Values) { f.Set("grant_type", "password") }, http.StatusBadRequest, "unsupported_grant_type"},
		{"absent grant", func(f url.Values) { f.Del("grant_type") }, http.StatusBadRequest, "unsupported_grant_type"},
		{"wrong audience", func(f url.Values) { f.Set("audience", "https://example.invalid") }, http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(c.name, func(t *testing.T) {
			form := clientCredentials()
			c.mutate(form)
			w := post(t, h, form, [2]string{clientID, secret})
			if w.Code != c.status {
				t.Fatalf("status = %d, want %d (%s)", w.Code, c.status, w.Body.String())
			}
			var body map[string]string
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if body["error"] != c.code {
				t.Fatalf("error = %q, want %q", body["error"], c.code)
			}
		})
	}
}

// Not every client sends an audience, so an absent one is accepted; only a
// mismatched one is refused.
func TestAbsentAudienceIsAccepted(t *testing.T) {
	h, secret := newServer(t)
	form := clientCredentials()
	form.Del("audience")

	if w := post(t, h, form, [2]string{clientID, secret}); w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestMissingCredentials(t *testing.T) {
	h, _ := newServer(t)
	if w := post(t, h, clientCredentials(), [2]string{}); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestTokenAuditCoversOutcomesWithoutCredentials(t *testing.T) {
	h, secret := newServer(t)
	trail := &tokenAuditTrail{}
	const keyVersion = "test-v1"
	key := []byte("test-audit-hmac-key")
	token := h.(interface {
		http.Handler
		Admit(http.Handler, ...string) http.Handler
	})
	h = token.Admit(audit.NewHTTPHandler(trail, tokenResolver{}, token, audit.StaticHMACKey(keyVersion, key)))

	success := post(t, h, clientCredentials(), [2]string{clientID, secret})
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(success.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode success: %v", err)
	}
	const refusedSecret = "known-refused-secret"
	if refused := post(t, h, clientCredentials(), [2]string{clientID, refusedSecret}); refused.Code != http.StatusUnauthorized {
		t.Fatalf("refused status = %d, want 401", refused.Code)
	}

	output := string(trail.raw)
	for name, credential := range map[string]string{
		"accepted client secret": secret,
		"refused client secret":  refusedSecret,
		"issued bearer token":    body.AccessToken,
	} {
		if strings.Contains(output, credential) {
			t.Fatalf("audit log contains %s %q: %s", name, credential, output)
		}
	}
	responses := trail.responses()
	if len(responses) != 2 {
		t.Fatalf("response records = %d, want 2", len(responses))
	}
	for _, record := range responses {
		for field, want := range map[string]any{
			"operation": "token.issue", "target_type": "access_token",
			"principal_id": "p-1", "identity_kind": "service_principal",
			"scope": "organization", "organization_id": orgID,
			"hmac_key_version": keyVersion,
		} {
			if record[field] != want {
				t.Errorf("%s = %v, want %v; record %#v", field, record[field], want, record)
			}
		}
		if _, ok := record["project_id"]; ok {
			t.Errorf("project_id present for organization-scoped token event: %#v", record)
		}
		if _, ok := record["target_id"]; ok {
			t.Errorf("target_id present for token event: %#v", record)
		}
	}
	if responses[0]["outcome"] != "success" || responses[0]["access_token_hmac"] != auditDigest(key, body.AccessToken) || responses[0]["client_secret_hmac"] != auditDigest(key, secret) {
		t.Fatalf("success audit = %#v", responses[0])
	}
	if responses[1]["outcome"] != "refused" || responses[1]["reason"] != "invalid_credentials" || responses[1]["client_secret_hmac"] != auditDigest(key, refusedSecret) {
		t.Fatalf("refusal audit = %#v", responses[1])
	}
	if _, ok := responses[1]["access_token_hmac"]; ok {
		t.Fatalf("refusal invented access_token_hmac: %#v", responses[1])
	}
}

func TestTokenStorageFailureOverridesHidden401Outcome(t *testing.T) {
	issuer, err := identity.NewBasicAuthIssuer("https://dufflebag.test", []byte("0123456789abcdef0123456789abcdef"), 5*time.Minute)
	if err != nil {
		t.Fatalf("new issuer: %v", err)
	}
	token := hcpauth.NewHandler(principals{err: errors.New("database unavailable")}, issuer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	trail := &tokenAuditTrail{}
	server := token.Admit(audit.NewHTTPHandler(trail, tokenResolver{}, token, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key"))))
	response := post(t, server, clientCredentials(), [2]string{clientID, "presented-secret"})
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wire status = %d, want deliberately hidden 401", response.Code)
	}
	record := trail.responses()[0]
	if record["outcome"] != "failure" || record["reason"] != "principal_lookup_failed" {
		t.Fatalf("storage failure audit = %#v, want failure/principal_lookup_failed", record)
	}
}

func TestTokenAuditDoesNotReadClientSecretBeforeBodyCap(t *testing.T) {
	handler, _ := newServer(t)
	token := handler.(interface {
		http.Handler
		Admit(http.Handler, ...string) http.Handler
	})
	trail := &tokenAuditTrail{}
	handler = token.Admit(audit.NewHTTPHandler(
		trail, tokenResolver{}, token, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")),
	))
	form := clientCredentials()
	form.Set("client_id", clientID)
	form.Set("client_secret", strings.Repeat("s", 16<<10))
	response := post(t, handler, form, [2]string{})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized token request status = %d, want 400", response.Code)
	}
	record := trail.responses()[0]
	if _, ok := record["client_secret_hmac"]; ok {
		t.Fatalf("oversized body was read for audit before the token cap: %#v", record)
	}
	if record["reason"] != "invalid_request" {
		t.Fatalf("oversized body reason = %v, want invalid_request", record["reason"])
	}
}

type tokenResolver struct{}

func (tokenResolver) Resolve(*http.Request) audit.Descriptor {
	return audit.Descriptor{RouteID: "root.token", Operation: identity.AuditOperationTokenIssue, TargetType: "access_token"}
}

type tokenAuditTrail struct {
	raw     []byte
	records []map[string]any
}

func (w *tokenAuditTrail) Write(encoded []byte) error {
	w.raw = append(w.raw, encoded...)
	var record map[string]any
	if err := json.Unmarshal(encoded, &record); err != nil {
		return err
	}
	w.records = append(w.records, record)
	return nil
}

func (w *tokenAuditTrail) responses() []map[string]any {
	var responses []map[string]any
	for _, record := range w.records {
		if record["kind"] == "response" {
			responses = append(responses, record)
		}
	}
	return responses
}

func auditDigest(key []byte, value string) string {
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write([]byte(value))
	return hex.EncodeToString(digest.Sum(nil))
}

// The issued token must verify, and must carry the principal's scope — that is
// what everything downstream authorizes against.
func TestIssuedTokenVerifiesAndCarriesScope(t *testing.T) {
	principal, err := identity.NewPrincipal(
		"p-1", "ci", clientID,
		identity.Scope{OrganizationID: uuid.MustParse(orgID)},
		identity.RoleBuilder,
		time.Now(),
	)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	secret, err := principal.IssueSecret("s-1", nil, time.Now())
	if err != nil {
		t.Fatalf("IssueSecret: %v", err)
	}
	issuer, err := identity.NewBasicAuthIssuer("https://dufflebag.test", []byte("0123456789abcdef0123456789abcdef"), 5*time.Minute)
	if err != nil {
		t.Fatalf("NewBasicAuthIssuer: %v", err)
	}
	h := hcpauth.NewHandler(principals{principal: principal}, issuer, slog.New(slog.NewTextHandler(io.Discard, nil)))

	w := post(t, h, clientCredentials(), [2]string{clientID, secret})
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	verified, err := issuer.Verify(body.AccessToken)
	if err != nil {
		t.Fatalf("the endpoint issued a token its own issuer rejects: %v", err)
	}
	if verified.PrincipalID != principal.ID {
		t.Fatalf("principal = %q, want %q", verified.PrincipalID, principal.ID)
	}
	if !verified.Scope.OrganizationScoped() {
		t.Fatal("an organization-scoped principal produced a project-scoped token")
	}
	if !verified.Scope.Permits(uuid.MustParse(orgID), uuid.New()) {
		t.Fatal("token does not permit its own organization")
	}
}
