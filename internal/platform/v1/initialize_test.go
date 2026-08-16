package v1

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
)

// fakeInstanceRepository claims once, exactly as the singleton row does.
type fakeInstanceRepository struct {
	mu                sync.Mutex
	claimed           bool
	principal         *identity.Principal
	statusFail        bool
	recoveryDigest    []byte
	recoveryThreshold int
}

func (f *fakeInstanceRepository) InitializeInstance(
	_ context.Context, principal *identity.Principal, recoveryDigest []byte, recoveryThreshold int,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimed {
		return registry.ErrConflict
	}
	f.claimed = true
	f.principal = principal
	f.recoveryDigest = recoveryDigest
	f.recoveryThreshold = recoveryThreshold
	return nil
}

// RecoveryVerifier answers not-found until a claim stores a verifier, exactly
// as the store does for an unclaimed or pre-recovery instance.
func (f *fakeInstanceRepository) RecoveryVerifier(_ context.Context) ([]byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.recoveryDigest) == 0 {
		return nil, 0, identity.ErrNotFound
	}
	return f.recoveryDigest, f.recoveryThreshold, nil
}

// InstanceStatus mirrors the store: claimed-ness comes from the same state
// InitializeInstance guards, so a fake that answered independently could let a
// handler pass while disagreeing with the real repository.
func (f *fakeInstanceRepository) InstanceStatus(
	_ context.Context,
) (bool, *time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusFail {
		// Bait, like the audit target's id: a phrase belonging to no health
		// state, so seeing it in the response can only mean the reason leaked.
		// "unreachable" would not do — object_storage reports that legitimately.
		return false, nil, false, errors.New("canary-database-failure-reason")
	}
	if !f.claimed {
		return false, nil, true, nil
	}
	if f.principal == nil {
		return true, nil, true, nil
	}
	initializedAt := f.principal.CreatedAt
	return true, &initializedAt, true, nil
}

var initTestTime = time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)

func initServer(t *testing.T) (http.Handler, *fakeInstanceRepository) {
	t.Helper()
	return initServerWithLogger(t, testLogger())
}

func initServerWithLogger(t *testing.T, logger *slog.Logger) (http.Handler, *fakeInstanceRepository) {
	t.Helper()
	instance := &fakeInstanceRepository{}
	repository := &fakeTenancyRepository{}
	return newHandler(
		repository,
		instance,
		testAuth{},
		testRoles{},
		logger,
		func() time.Time { return initTestTime },
	), instance
}

func postInit(t *testing.T, handler http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/sys/init", nil))
	return w
}

func TestInitializeReturnsCredentialsOnce(t *testing.T) {
	handler, instance := initServer(t)

	w := postInit(t, handler)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body)
	}
	var body struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ClientID == "" || body.ClientSecret == "" {
		t.Fatalf("incomplete credentials: %s", w.Body)
	}

	// The credential must authenticate the principal that was actually stored,
	// or the caller has been handed a secret for nothing.
	if instance.principal == nil {
		t.Fatal("no principal was persisted")
	}
	if _, ok := instance.principal.Authenticate(body.ClientSecret, time.Now().UTC()); !ok {
		t.Fatal("the returned secret does not authenticate the stored principal")
	}
	if instance.principal.ClientID != body.ClientID {
		t.Fatalf("client id = %q, stored %q", body.ClientID, instance.principal.ClientID)
	}
}

// The whole point: the secret exists in that one response and nowhere else.
func TestInitializeDoesNotPersistThePlaintext(t *testing.T) {
	handler, instance := initServer(t)

	w := postInit(t, handler)
	var body struct {
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, secret := range instance.principal.Secrets() {
		if strings.Contains(secret.Encoded(), body.ClientSecret) {
			t.Fatal("the stored hash contains the plaintext secret")
		}
		if !strings.HasPrefix(secret.Encoded(), "$argon2id$") {
			t.Fatalf("stored credential is not argon2id: %s", secret.Encoded())
		}
	}
}

func TestInitializeAuditCoversOutcomesWithoutCredential(t *testing.T) {
	handler, _ := initServer(t)
	handler, trail := auditedPlatform(t, handler)

	first := postInit(t, handler)
	var body struct {
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	if body.ClientSecret == "" {
		t.Fatal("initialize response contains no client secret")
	}
	if second := postInit(t, handler); second.Code != http.StatusConflict {
		t.Fatalf("second initialize status = %d, want 409", second.Code)
	}

	output := string(trail.raw)
	if strings.Contains(output, body.ClientSecret) {
		t.Fatalf("audit log contains bootstrap credential %q: %s", body.ClientSecret, output)
	}
	var responses []map[string]any
	for _, record := range trail.records {
		if record["kind"] == "response" {
			responses = append(responses, record)
		}
	}
	if len(responses) != 2 {
		t.Fatalf("response records = %d, want success and refusal", len(responses))
	}
	for field, want := range map[string]any{
		"operation": "instance.initialize", "target_type": "instance", "target_id": "singleton",
		"principal_id": "anonymous", "identity_kind": "anonymous", "scope": "platform",
		"outcome": "success", "hmac_key_version": "test-v1",
	} {
		if responses[0][field] != want {
			t.Errorf("successful init %s = %v, want %v; record %#v", field, responses[0][field], want, responses[0])
		}
	}
	if responses[0]["bootstrap_secret_hmac"] == nil {
		t.Fatalf("successful init has no bootstrap_secret_hmac: %#v", responses[0])
	}
	for _, absent := range []string{
		"principal_name", "organization_id", "project_id", "client_secret_hmac", "access_token_hmac",
	} {
		if _, ok := responses[0][absent]; ok {
			t.Errorf("successful init unexpectedly carries %s: %#v", absent, responses[0])
		}
	}
	if responses[1]["outcome"] != "refused" || responses[1]["reason"] != "already_initialized" {
		t.Fatalf("refused init audit = %#v", responses[1])
	}
	if _, ok := responses[1]["bootstrap_secret_hmac"]; ok {
		t.Fatalf("refused init invented bootstrap_secret_hmac: %#v", responses[1])
	}
}

func TestInitializeAuditUsesOneOrdinaryPairPerAttempt(t *testing.T) {
	handler, _ := initServer(t)
	handler, trail := auditedPlatform(t, handler)

	if w := postInit(t, handler); w.Code != http.StatusOK {
		t.Fatalf("first initialize status = %d, want 200", w.Code)
	}
	if len(trail.records) != 2 {
		t.Fatalf("successful initialize emitted %d records, want one pair", len(trail.records))
	}
	if trail.records[0]["kind"] != "request" || trail.records[1]["kind"] != "response" {
		t.Fatalf("successful initialize kinds = %v, %v", trail.records[0]["kind"], trail.records[1]["kind"])
	}
	if strings.Contains(string(trail.raw), `"operation":"principal.create"`) || strings.Contains(string(trail.raw), `"operation":"secret.issue"`) {
		t.Fatalf("superseded bootstrap action event survived: %s", trail.raw)
	}

	if w := postInit(t, handler); w.Code != http.StatusConflict {
		t.Fatalf("second initialize status = %d, want 409", w.Code)
	}
	if len(trail.records) != 4 {
		t.Fatalf("two initialize attempts emitted %d records, want two pairs", len(trail.records))
	}
}

func TestInitializeIsRefusedOnceClaimed(t *testing.T) {
	handler, _ := initServer(t)

	if w := postInit(t, handler); w.Code != http.StatusOK {
		t.Fatalf("first call status = %d, want 200", w.Code)
	}
	second := postInit(t, handler)
	if second.Code != http.StatusConflict {
		t.Fatalf("second call status = %d, want 409; body %s", second.Code, second.Body)
	}
	if strings.Contains(second.Body.String(), "client_secret") {
		t.Fatalf("a refused initialize leaked credential fields: %s", second.Body)
	}
}

// Two callers racing must not both believe they own the deployment.
func TestConcurrentInitializeClaimsOnce(t *testing.T) {
	handler, _ := initServer(t)

	const callers = 8
	codes := make([]int, callers)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/sys/init", nil))
			codes[index] = w.Code
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for _, code := range codes {
		switch code {
		case http.StatusOK:
			succeeded++
		case http.StatusConflict:
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d callers were told they own the deployment, want exactly 1", succeeded)
	}
}

const (
	testToken   = "platform-test-token"
	testOrgID   = "3f2b1c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	testProjID  = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	testPrincID = "p-platform"
)

type testAuth struct{}

func (testAuth) Verify(token string) (identity.Verified, error) {
	if token != testToken {
		return identity.Verified{}, identity.ErrInvalid
	}
	now := time.Now().UTC()
	return identity.Verified{
		PrincipalID: testPrincID, SecretID: testSecretID,
		AuthTime: now.Add(-time.Hour), ExpiresAt: now.Add(5 * time.Minute),
	}, nil
}

func (testAuth) VerifyExpired(token string) (identity.Verified, error) {
	if token != "expired-token" {
		return identity.Verified{}, identity.ErrInvalid
	}
	now := time.Now().UTC()
	return identity.Verified{
		PrincipalID: testPrincID, SecretID: testSecretID,
		AuthTime: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute),
	}, nil
}

func (testAuth) Reissue(*identity.Principal, string, time.Time) (string, error) {
	return "renewed-token", nil
}

// testRoles resolves the caller's authority. Root by default, because most
// tests exercise operations rather than authorization; the boundaries have
// their own test.
type testRoles struct {
	role    identity.Role
	scope   identity.Scope
	missing bool
	revoked bool
}

func (r testRoles) GetPrincipalByID(_ context.Context, id string) (*identity.Principal, error) {
	if r.missing {
		return nil, identity.ErrNotFound
	}
	role := r.role
	if role == "" {
		role = identity.RoleRoot
	}
	secrets := testSecrets()
	if r.revoked {
		secrets = nil
	}
	return identity.RestorePrincipal(id, "test", "client", r.scope, role, initTestTime, secrets)
}

func (testRoles) TouchSecretLastUsed(context.Context, string, time.Time) error { return nil }

func authenticated(r *http.Request) *http.Request {
	r.Header.Set("Authorization", "Bearer "+testToken)
	return r
}

// testSecretID is the credential every test token is minted from. Fixtures must
// carry a secret with this ID or the request is refused as revoked, which is the
// point of review finding 14 — a token names the credential behind it.
const testSecretID = "s-test"

// testSecrets is the stored credential a fixture principal holds. Only the
// argon2id prefix is validated on restore, so this needs no derivation.
func testSecrets() []identity.Secret {
	secret, err := identity.RestoreSecret(
		testSecretID,
		"$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHQ$aGFzaGhhc2g",
		time.Unix(0, 0).UTC(), nil, nil,
	)
	if err != nil {
		panic("test secret: " + err.Error())
	}
	return []identity.Secret{secret}
}

// The health probe is how the console tells first run from sign-in without
// POSTing to /init, so its three states must be distinguishable by status code
// alone — a readiness probe reads nothing else.
func TestHealthReportsInitializationWithoutClaimingTheInstance(t *testing.T) {
	handler, instances := initServerWithLogger(t, slog.New(slog.NewTextHandler(io.Discard, nil)))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sys/health", nil))
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("unclaimed instance = %d, want 501: %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), `"initialized":false`) {
		t.Fatalf("unclaimed body = %s", response.Body)
	}
	// Audit is disabled because no target can be configured before the instance
	// is claimed. That is distinct from configured targets being healthy.
	if !strings.Contains(response.Body.String(), `"audit":"disabled"`) {
		t.Fatalf("unclaimed audit state = %s, want disabled", response.Body)
	}
	// Reading the state must not have claimed it: /init still has to work.
	if instances.claimed {
		t.Fatal("the health probe claimed the instance")
	}

	instances.claimed = true
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sys/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("claimed instance = %d, want 200: %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), `"audit":"disabled"`) {
		t.Fatalf("claimed instance audit state = %s, want disabled", response.Body)
	}

	instances.statusFail = true
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sys/health", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unreachable database = %d, want 503: %s", response.Code, response.Body)
	}
	// Audit stays disabled even here: the database is what failed, and
	// conflating the two would make the field useless.
	if !strings.Contains(response.Body.String(), `"audit":"disabled"`) {
		t.Fatalf("degraded-database response misreports audit: %s", response.Body)
	}
	// Unauthenticated, so the reason stays server-side (review finding 16).
	if strings.Contains(response.Body.String(), "canary-database-failure-reason") {
		t.Fatalf("health leaked the failure reason: %s", response.Body)
	}
}

func TestHealthReturns503WhenAuditDegradedAndDatabaseHealthy(t *testing.T) {
	instance := &fakeInstanceRepository{claimed: true}
	// The id is bait: it appears nowhere in the health vocabulary, so finding
	// it in the response can only mean a target leaked into an unauthenticated
	// answer. Do not name it after a state — object_storage reports one called
	// "unconfigured", and a canary that collides with real output cries wolf.
	broker := &spyAuditBroker{health: []audit.SinkHealth{{
		ID: "canary-target-id", Status: audit.SinkStatusFailing,
	}}}
	handler := newHandlerWithAudit(
		&fakeTenancyRepository{}, instance, testAuth{}, testRoles{}, testLogger(),
		&fakeAuditTargetRepository{}, broker, func() time.Time { return initTestTime },
	)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sys/health", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("degraded audit with healthy database = %d, want 503: %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), `"database":true`) ||
		!strings.Contains(response.Body.String(), `"audit":"degraded"`) {
		t.Fatalf("degraded audit response does not isolate broker health from database health: %s", response.Body)
	}
	if strings.Contains(response.Body.String(), "canary-target-id") {
		t.Fatalf("unauthenticated health response names an audit target: %s", response.Body)
	}
}

// Object storage state is reported, and none of its states fails the probe.
// Only SBOM upload needs the store, so a registry without one is still serving
// and must still say so — the same reasoning that lets a partial audit stay
// ready while only a fully degraded one returns 503.
func TestHealthReportsObjectStorageWithoutEverFailingTheProbe(t *testing.T) {
	for _, state := range []string{"unconfigured", "ok", "unreachable"} {
		t.Run(state, func(t *testing.T) {
			handler := newHandlerWithAudit(
				&fakeTenancyRepository{objectStorageState: state},
				&fakeInstanceRepository{claimed: true},
				testAuth{}, testRoles{}, testLogger(),
				&fakeAuditTargetRepository{}, &spyAuditBroker{},
				func() time.Time { return initTestTime },
			)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sys/health", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("object storage %s = %d, want 200: %s", state, response.Code, response.Body)
			}
			if !strings.Contains(response.Body.String(), `"object_storage":"`+state+`"`) {
				t.Fatalf("object storage %s not reported: %s", state, response.Body)
			}
		})
	}
}

func TestHealthReturns200WhenEveryAuditTargetIsHealthy(t *testing.T) {
	instance := &fakeInstanceRepository{claimed: true}
	broker := &spyAuditBroker{health: []audit.SinkHealth{
		{ID: "first", Status: audit.SinkStatusHealthy},
		{ID: "second", Status: audit.SinkStatusHealthy},
	}}
	handler := newHandlerWithAudit(
		&fakeTenancyRepository{}, instance, testAuth{}, testRoles{}, testLogger(),
		&fakeAuditTargetRepository{}, broker, func() time.Time { return initTestTime },
	)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sys/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("healthy audit targets = %d, want 200: %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), `"audit":"ok"`) {
		t.Fatalf("healthy audit state = %s, want ok", response.Body)
	}
}

func TestHealthReturns200WhenAuditIsPartial(t *testing.T) {
	instance := &fakeInstanceRepository{claimed: true}
	broker := &spyAuditBroker{health: []audit.SinkHealth{
		{ID: "healthy-target", Status: audit.SinkStatusHealthy},
		{ID: "failing-target", Status: audit.SinkStatusFailing},
	}}
	handler := newHandlerWithAudit(
		&fakeTenancyRepository{}, instance, testAuth{}, testRoles{}, testLogger(),
		&fakeAuditTargetRepository{}, broker, func() time.Time { return initTestTime },
	)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sys/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("partial audit = %d, want 200: %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), `"audit":"partial"`) {
		t.Fatalf("mixed audit state = %s, want partial", response.Body)
	}
	if strings.Contains(response.Body.String(), "healthy-target") ||
		strings.Contains(response.Body.String(), "failing-target") {
		t.Fatalf("unauthenticated health response names an audit target: %s", response.Body)
	}
}
