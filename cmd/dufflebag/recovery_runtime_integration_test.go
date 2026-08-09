//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/google/uuid"
)

// foreignShare is a structurally valid share from a different recovery secret,
// minted by the same producer /sys/init uses.
func foreignShare(t *testing.T) string {
	t.Helper()
	shares, _, err := identity.NewRecoveryShares(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return shares[0]
}

// recoveryRequest is runtimeJSONRequest without the 2xx assertion: refusals
// are the point of half these calls.
func recoveryRequest(t *testing.T, address, path string, body any) (int, []byte) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "http://"+address+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data
}

// requestRootToken drives the real token endpoint with the minted credentials:
// the ceremony is only proven when the recovered root can actually sign in.
func requestRootToken(t *testing.T, address, clientID, clientSecret string) (int, string) {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}, "audience": {"https://api.hashicorp.cloud"}}
	request, err := http.NewRequest(http.MethodPost, "http://"+address+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth(clientID, clientSecret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&token); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, token.AccessToken
}

// The whole ceremony against the real binary over real HTTP (ADR-0024,
// duf-9rr's oracle): init hands out shares, recovery at the threshold mints a
// fresh root, and that root authenticates at the token endpoint.
func TestRecoveryCeremonyMintsARootThatSignsIn(t *testing.T) {
	database := newRuntimeDatabase(t)
	process := startRuntimeProcess(t, database.appURL)
	defer process.stop(t)

	status, body := recoveryRequest(t, process.address, "/sys/init",
		map[string]int{"recovery_share_count": 3, "recovery_threshold": 2})
	if status != http.StatusOK {
		t.Fatalf("initialize = %d: %s", status, body)
	}
	var initialized struct {
		ClientID          string   `json:"client_id"`
		ClientSecret      string   `json:"client_secret"`
		RecoveryShares    []string `json:"recovery_shares"`
		RecoveryThreshold int      `json:"recovery_threshold"`
	}
	if err := json.Unmarshal(body, &initialized); err != nil {
		t.Fatal(err)
	}
	if len(initialized.RecoveryShares) != 3 || initialized.RecoveryThreshold != 2 {
		t.Fatalf("init shares = %d, threshold = %d, want 3 and 2: %s",
			len(initialized.RecoveryShares), initialized.RecoveryThreshold, body)
	}

	// Below the threshold: refused, nothing minted.
	status, body = recoveryRequest(t, process.address, "/sys/recovery",
		map[string]any{"shares": initialized.RecoveryShares[:1]})
	if status != http.StatusBadRequest {
		t.Fatalf("below-threshold recovery = %d: %s", status, body)
	}

	// Any two shares prove custody.
	status, body = recoveryRequest(t, process.address, "/sys/recovery",
		map[string]any{"shares": []string{initialized.RecoveryShares[0], initialized.RecoveryShares[2]}})
	if status != http.StatusOK {
		t.Fatalf("recovery = %d: %s", status, body)
	}
	var recovered struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(body, &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.ClientID == "" || recovered.ClientID == initialized.ClientID {
		t.Fatalf("recovered client id %q must exist and differ from the bootstrap root %q",
			recovered.ClientID, initialized.ClientID)
	}

	if status, token := requestRootToken(t, process.address, recovered.ClientID, recovered.ClientSecret); status != http.StatusOK || token == "" {
		t.Fatalf("recovered root token request = %d, token %q", status, token)
	}
}

func TestRecoveryRefusesForeignSharesAgainstRealBinary(t *testing.T) {
	database := newRuntimeDatabase(t)
	process := startRuntimeProcess(t, database.appURL)
	defer process.stop(t)

	if status, body := recoveryRequest(t, process.address, "/sys/init", map[string]int{}); status != http.StatusOK {
		t.Fatalf("initialize = %d: %s", status, body)
	}

	// A share minted by a DIFFERENT init — captured from a second throwaway
	// instance would be ideal, but the identity package is the same producer
	// /sys/init uses, so a locally minted foreign share is the same artifact.
	status, body := recoveryRequest(t, process.address, "/sys/recovery",
		map[string]any{"shares": []string{foreignShare(t)}})
	if status != http.StatusForbidden {
		t.Fatalf("foreign share recovery = %d, want 403: %s", status, body)
	}
	if strings.Contains(string(body), "client_secret") {
		t.Fatalf("refused recovery leaked credentials: %s", body)
	}
}

// The break-glass runbook (docs/deployment.md, "Break glass" section) is the
// floor on unencrypted deployments and ships tested or it is nothing
// (conventions rule 7): the documented SQL, executed against a live schema,
// must mint a root that actually signs in.
func TestBreakGlassRunbookMintsARootThatSignsIn(t *testing.T) {
	database := newRuntimeDatabase(t)
	process := startRuntimeProcess(t, database.appURL)
	defer process.stop(t)

	if status, body := recoveryRequest(t, process.address, "/sys/init", map[string]int{}); status != http.StatusOK {
		t.Fatalf("initialize = %d: %s", status, body)
	}

	// Captured from the runbook's documented hash step (fixture from the
	// producing tool, 2026-08-05):
	//
	//	printf %s "break-glass-test-secret" | argon2 breakglasssalt01 -id -t 2 -k 65536 -p 1 -l 32 -e
	//
	// run with argon2 CLI 20190702 (alpine:3.20 package). The server verifies
	// with the parameters the hash itself carries, so the CLI's parameters need
	// not match the server's own hashing constants.
	const (
		breakGlassSecret = "break-glass-test-secret"
		breakGlassHash   = "$argon2id$v=19$m=65536,t=2,p=1$YnJlYWtnbGFzc3NhbHQwMQ$6JE7aLqQDbhrwraovLkpDLrVDJoIUJetdNPFriO1lCg"
	)
	clientID := uuid.NewString()
	// The runbook's SQL, statement for statement: server-generated ids, a
	// platform-scoped root, the secret row keyed by client_id lookup, one
	// transaction. Only the shell interpolation becomes bind parameters.
	tx, err := database.admin.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`
		INSERT INTO principals (id, name, client_id, organization_id, project_id, role, created_at)
		VALUES (gen_random_uuid()::text, 'break-glass administrator', $1, NULL, NULL, 'root', now())
	`, clientID); err != nil {
		t.Fatalf("insert break-glass principal: %v", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO principal_secrets (id, principal_id, encoded_hash, created_at)
		SELECT gen_random_uuid()::text, id, $1, now()
		FROM principals WHERE client_id = $2
	`, breakGlassHash, clientID); err != nil {
		t.Fatalf("insert break-glass secret: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if status, token := requestRootToken(t, process.address, clientID, breakGlassSecret); status != http.StatusOK || token == "" {
		t.Fatalf("break-glass root token request = %d, token %q", status, token)
	}
}
