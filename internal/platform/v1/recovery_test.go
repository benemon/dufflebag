package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
)

type initBody struct {
	ClientID          string   `json:"client_id"`
	ClientSecret      string   `json:"client_secret"`
	RecoveryShares    []string `json:"recovery_shares"`
	RecoveryThreshold int      `json:"recovery_threshold"`
}

// recoveryServer builds the plane with both fakes exposed: recovery mints its
// principal through the tenancy repository, not the instance claim.
func recoveryServer(t *testing.T) (http.Handler, *fakeInstanceRepository, *fakeTenancyRepository) {
	t.Helper()
	instance := &fakeInstanceRepository{}
	repository := &fakeTenancyRepository{}
	handler := newHandler(
		repository, instance, testAuth{}, testRoles{}, testLogger(),
		func() time.Time { return initTestTime },
	)
	return handler, instance, repository
}

func initializeWithShares(t *testing.T, handler http.Handler, body any) initBody {
	t.Helper()
	w := call(t, handler, http.MethodPost, "/sys/init", body, "")
	if w.Code != http.StatusOK {
		t.Fatalf("initialize status = %d, want 200; body %s", w.Code, w.Body)
	}
	var response initBody
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	return response
}

func TestInitializeReturnsOneRecoveryShareByDefault(t *testing.T) {
	handler, instance, _ := recoveryServer(t)

	response := initializeWithShares(t, handler, nil)
	if len(response.RecoveryShares) != 1 || response.RecoveryThreshold != 1 {
		t.Fatalf("default share parameters = %d shares, threshold %d, want 1-of-1; body %#v",
			len(response.RecoveryShares), response.RecoveryThreshold, response)
	}
	// What was stored is a verifier, never a share.
	if len(instance.recoveryDigest) == 0 || instance.recoveryThreshold != 1 {
		t.Fatalf("stored verifier digest %d bytes, threshold %d", len(instance.recoveryDigest), instance.recoveryThreshold)
	}
	for _, share := range response.RecoveryShares {
		if strings.Contains(string(instance.recoveryDigest), share) {
			t.Fatal("the stored digest contains a share")
		}
	}
}

func TestRecoveryMintsAFreshRootFromInitShares(t *testing.T) {
	handler, _, repository := recoveryServer(t)
	response := initializeWithShares(t, handler, nil)

	w := call(t, handler, http.MethodPost, "/sys/recovery",
		map[string]any{"shares": response.RecoveryShares}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("recovery status = %d, want 200; body %s", w.Code, w.Body)
	}
	var recovered struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &recovered); err != nil {
		t.Fatalf("decode recovery response: %v", err)
	}
	if recovered.ClientID == "" || recovered.ClientSecret == "" {
		t.Fatalf("incomplete recovered credentials: %s", w.Body)
	}
	// Fresh principal, not the bootstrap one resurrected (ADR-0024).
	if recovered.ClientID == response.ClientID {
		t.Fatal("recovery returned the original bootstrap client id")
	}
	if len(repository.principals) != 1 {
		t.Fatalf("recovery persisted %d principals, want 1", len(repository.principals))
	}
	minted := repository.principals[0]
	if minted.Role != identity.RoleRoot || !minted.Scope.PlatformScoped() {
		t.Fatalf("recovered principal role %q scope %#v, want platform root", minted.Role, minted.Scope)
	}
	if _, ok := minted.Authenticate(recovered.ClientSecret, time.Now().UTC()); !ok {
		t.Fatal("the recovered secret does not authenticate the minted principal")
	}

	// Custody proof, not a consumable: the same shares recover again.
	if again := call(t, handler, http.MethodPost, "/sys/recovery",
		map[string]any{"shares": response.RecoveryShares}, ""); again.Code != http.StatusOK {
		t.Fatalf("second recovery status = %d, want 200; body %s", again.Code, again.Body)
	}
}

func TestInitializeHonorsShareParametersAndThresholdRecovers(t *testing.T) {
	handler, _, repository := recoveryServer(t)
	response := initializeWithShares(t, handler,
		map[string]any{"recovery_share_count": 3, "recovery_threshold": 2})
	if len(response.RecoveryShares) != 3 || response.RecoveryThreshold != 2 {
		t.Fatalf("share parameters = %d shares, threshold %d, want 3 and 2",
			len(response.RecoveryShares), response.RecoveryThreshold)
	}

	// One share below the threshold refuses without minting anything.
	w := call(t, handler, http.MethodPost, "/sys/recovery",
		map[string]any{"shares": response.RecoveryShares[:1]}, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("below-threshold recovery = %d, want 400; body %s", w.Code, w.Body)
	}
	if len(repository.principals) != 0 {
		t.Fatal("a refused recovery minted a principal")
	}

	// Any two shares suffice — here the last two.
	w = call(t, handler, http.MethodPost, "/sys/recovery",
		map[string]any{"shares": response.RecoveryShares[1:]}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("threshold recovery = %d, want 200; body %s", w.Code, w.Body)
	}
}

func TestInitializeRefusesUnusableShareParameters(t *testing.T) {
	handler, instance, _ := recoveryServer(t)
	for _, body := range []map[string]any{
		{"recovery_share_count": 0},
		{"recovery_threshold": 0},
		{"recovery_share_count": 2, "recovery_threshold": 3},
		{"recovery_share_count": 256},
	} {
		w := call(t, handler, http.MethodPost, "/sys/init", body, "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("initialize with %v = %d, want 400; body %s", body, w.Code, w.Body)
		}
	}
	// Refused parameters must not have claimed the instance.
	if instance.claimed {
		t.Fatal("a refused initialize claimed the instance")
	}
	if w := call(t, handler, http.MethodPost, "/sys/init", nil, ""); w.Code != http.StatusOK {
		t.Fatalf("initialize after refusals = %d, want 200; body %s", w.Code, w.Body)
	}
}

func TestRecoveryRefusesForeignShares(t *testing.T) {
	handler, _, repository := recoveryServer(t)
	initializeWithShares(t, handler, nil)

	// A structurally valid share from a different instance: right encoding,
	// wrong secret.
	foreign, _, err := identity.NewRecoveryShares(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	w := call(t, handler, http.MethodPost, "/sys/recovery",
		map[string]any{"shares": foreign}, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign shares = %d, want 403; body %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "client_secret") {
		t.Fatalf("a refused recovery leaked credential fields: %s", w.Body)
	}
	if len(repository.principals) != 0 {
		t.Fatal("a refused recovery minted a principal")
	}
}

func TestRecoveryRefusesMalformedShares(t *testing.T) {
	handler, _, _ := recoveryServer(t)
	initializeWithShares(t, handler, nil)

	w := call(t, handler, http.MethodPost, "/sys/recovery",
		map[string]any{"shares": []string{"not-a-share"}}, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed share = %d, want 400; body %s", w.Code, w.Body)
	}
}

func TestRecoveryRefusesWhenNoVerifierExists(t *testing.T) {
	handler, _, _ := recoveryServer(t)

	// Unclaimed instance: nothing to recover.
	shares, _, err := identity.NewRecoveryShares(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	w := call(t, handler, http.MethodPost, "/sys/recovery",
		map[string]any{"shares": shares}, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("recovery on unclaimed instance = %d, want 409; body %s", w.Code, w.Body)
	}
}

// Recovery joins /sys/init on the unauthenticated list deliberately: it exists
// for the caller whose credential is lost, and the shares are the credential.
func TestRecoveryIsExemptFromBearerAuthentication(t *testing.T) {
	handler, _, _ := recoveryServer(t)
	response := initializeWithShares(t, handler, nil)

	r := httptest.NewRequest(http.MethodPost, "/sys/recovery",
		strings.NewReader(`{"shares":`+mustJSON(t, response.RecoveryShares)+`}`))
	r.Header.Set("Content-Type", "application/json")
	// No Authorization header at all.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("unauthenticated recovery = %d, want 200; body %s", w.Code, w.Body)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestRecoveryAuditCoversOutcomesWithShareAndSecretHMACs(t *testing.T) {
	handler, _, _ := recoveryServer(t)
	handler, trail := auditedPlatform(t, handler)

	response := initializeWithShares(t, handler, nil)
	if success := call(t, handler, http.MethodPost, "/sys/recovery",
		map[string]any{"shares": response.RecoveryShares}, ""); success.Code != http.StatusOK {
		t.Fatalf("recovery status = %d; body %s", success.Code, success.Body)
	}
	if refused := call(t, handler, http.MethodPost, "/sys/recovery",
		map[string]any{"shares": []string{"not-a-share"}}, ""); refused.Code != http.StatusBadRequest {
		t.Fatalf("malformed recovery status = %d", refused.Code)
	}
	foreign, _, err := identity.NewRecoveryShares(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rejected := call(t, handler, http.MethodPost, "/sys/recovery",
		map[string]any{"shares": foreign}, ""); rejected.Code != http.StatusForbidden {
		t.Fatalf("foreign recovery status = %d", rejected.Code)
	}

	// The trail must never contain a share or a secret in the clear.
	output := string(trail.raw)
	for _, share := range append(append([]string{}, response.RecoveryShares...), foreign...) {
		if strings.Contains(output, share) {
			t.Fatalf("audit trail contains a recovery share in the clear: %s", output)
		}
	}

	var responses []map[string]any
	for _, record := range trail.records {
		if record["kind"] == "response" && record["operation"] == "instance.recover" {
			responses = append(responses, record)
		}
	}
	if len(responses) != 3 {
		t.Fatalf("recovery response records = %d, want 3", len(responses))
	}
	success, malformed, rejected := responses[0], responses[1], responses[2]

	for field, want := range map[string]any{
		"outcome": "success", "operation": "instance.recover",
		"target_type": "instance", "principal_id": "anonymous",
		"identity_kind": "anonymous", "scope": "platform",
	} {
		if success[field] != want {
			t.Errorf("successful recovery %s = %v, want %v; record %#v", field, success[field], want, success)
		}
	}
	// The minted principal is the target, so the ceremony names what it created.
	if success["target_id"] == "singleton" || success["target_id"] == nil {
		t.Errorf("successful recovery target_id = %v, want the minted principal id", success["target_id"])
	}
	if success["bootstrap_secret_hmac"] == nil {
		t.Errorf("successful recovery has no bootstrap_secret_hmac: %#v", success)
	}
	if hmacs, ok := success["recovery_share_hmacs"].([]any); !ok || len(hmacs) != 1 {
		t.Errorf("successful recovery share hmacs = %#v, want 1", success["recovery_share_hmacs"])
	}

	if malformed["outcome"] != "refused" || malformed["reason"] != "invalid_shares" {
		t.Errorf("malformed recovery audit = %#v", malformed)
	}
	if rejected["outcome"] != "refused" || rejected["reason"] != "shares_rejected" {
		t.Errorf("rejected recovery audit = %#v", rejected)
	}
	// A refused ceremony still names the shares it was attempted with.
	if hmacs, ok := rejected["recovery_share_hmacs"].([]any); !ok || len(hmacs) != 1 {
		t.Errorf("rejected recovery share hmacs = %#v, want 1", rejected["recovery_share_hmacs"])
	}
	for _, refusal := range []map[string]any{malformed, rejected} {
		if _, ok := refusal["bootstrap_secret_hmac"]; ok {
			t.Errorf("refused recovery invented bootstrap_secret_hmac: %#v", refusal)
		}
	}
}

func TestInitializeAuditRecordsMintedShareHMACs(t *testing.T) {
	handler, _, _ := recoveryServer(t)
	handler, trail := auditedPlatform(t, handler)

	response := initializeWithShares(t, handler,
		map[string]any{"recovery_share_count": 3, "recovery_threshold": 2})
	output := string(trail.raw)
	for _, share := range response.RecoveryShares {
		if strings.Contains(output, share) {
			t.Fatalf("audit trail contains a recovery share in the clear: %s", output)
		}
	}
	var success map[string]any
	for _, record := range trail.records {
		if record["kind"] == "response" && record["operation"] == "instance.initialize" {
			success = record
		}
	}
	if hmacs, ok := success["recovery_share_hmacs"].([]any); !ok || len(hmacs) != 3 {
		t.Fatalf("initialize share hmacs = %#v, want 3", success["recovery_share_hmacs"])
	}
}
