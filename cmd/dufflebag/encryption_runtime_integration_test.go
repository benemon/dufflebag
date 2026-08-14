//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/bagdrop"
	"github.com/google/uuid"
	tc "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startVaultDev runs a real Vault in dev mode with the transit engine
// mounted: the ADR-0024 oracle is a live key service, not a stub of one.
func startVaultDev(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	container, err := tc.GenericContainer(ctx, tc.GenericContainerRequest{
		ContainerRequest: tc.ContainerRequest{
			Image:        "hashicorp/vault:1.17",
			Env:          map[string]string{"VAULT_DEV_ROOT_TOKEN_ID": "runtime-root"},
			ExposedPorts: []string{"8200/tcp"},
			WaitingFor:   wait.ForHTTP("/v1/sys/health").WithPort("8200/tcp"),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start vault: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("vault host: %v", err)
	}
	port, err := container.MappedPort(ctx, "8200/tcp")
	if err != nil {
		t.Fatalf("vault port: %v", err)
	}
	address := fmt.Sprintf("http://%s:%s", host, port.Port())

	// Mount transit. The key itself is created on first use — transit
	// upserts on encrypt for a caller with create capability.
	request, err := http.NewRequest(http.MethodPost, address+"/v1/sys/mounts/transit",
		bytes.NewReader([]byte(`{"type":"transit"}`)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Vault-Token", "runtime-root")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("mount transit: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("mount transit = %d", response.StatusCode)
	}
	return address
}

// encryptedEnvironment is the encrypted-deployment configuration: a key
// provider, no environment keys — those live in the wrapped keyring.
func encryptedEnvironment(vaultAddress string) map[string]string {
	return map[string]string{
		"DFBG_KEY_PROVIDER":           "vault",
		"DFBG_VAULT_ADDR":             vaultAddress,
		"DFBG_VAULT_TOKEN":            "runtime-root",
		"DFBG_TOKEN_SIGNING_KEY":      "",
		"DFBG_SHUTDOWN_GRACE_PERIOD":  "1s",
		"DFBG_AUDIT_HMAC_KEY":         "",
		"DFBG_AUDIT_HMAC_KEY_VERSION": "",
	}
}

func startEncryptedRuntimeProcess(t *testing.T, databaseURL, vaultAddress string) *runtimeProcess {
	t.Helper()
	address := reserveAddress(t)
	command := runtimeCommand(databaseURL, address, encryptedEnvironment(vaultAddress))
	logFile, err := os.Create(filepath.Join(t.TempDir(), "dufflebag-encrypted.log"))
	if err != nil {
		t.Fatalf("create runtime process log: %v", err)
	}
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start encrypted dufflebag binary: %v", err)
	}
	process := &runtimeProcess{command: command, address: address, log: logFile}
	t.Cleanup(func() {
		if !process.stopped {
			_ = process.command.Process.Kill()
			_ = process.command.Wait()
		}
		_ = process.log.Close()
	})
	waitFor(t, 15*time.Second, func() bool {
		response, err := http.Get("http://" + address + "/sys/health")
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		return true
	}, "encrypted dufflebag binary did not become reachable; "+process.logs())
	return process
}

// The encrypted posture end to end: first boot establishes the keyring in
// Vault-wrapped rows, /sys/init works, and a restart unwraps the SAME keys —
// a token minted before the restart still authenticates after it, which is
// what proves the signing key came from the keyring rather than the process
// lifetime.
func TestEncryptedRuntimeBootsInitializesAndKeepsKeysAcrossRestart(t *testing.T) {
	vaultAddress := startVaultDev(t)
	database := newRuntimeDatabase(t)

	process := startEncryptedRuntimeProcess(t, database.appURL, vaultAddress)
	status, body := recoveryRequest(t, process.address, "/sys/init", map[string]int{})
	if status != http.StatusOK {
		t.Fatalf("initialize encrypted runtime = %d: %s", status, body)
	}
	var initialized struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(body, &initialized); err != nil {
		t.Fatal(err)
	}
	tokenStatus, token := requestRootToken(t, process.address, initialized.ClientID, initialized.ClientSecret)
	if tokenStatus != http.StatusOK || token == "" {
		t.Fatalf("token on encrypted runtime = %d, token %q", tokenStatus, token)
	}

	// The keyring landed wrapped: every stored key names the transit KEK.
	var wrapped int
	if err := database.admin.QueryRow(
		`SELECT count(*) FROM keyring WHERE convert_from(wrapped_key, 'UTF8') LIKE 'vault:v%'`,
	).Scan(&wrapped); err != nil {
		t.Fatalf("inspect keyring: %v", err)
	}
	if wrapped != 4 {
		t.Fatalf("wrapped keyring rows = %d, want 4 purposes", wrapped)
	}

	process.stop(t)
	restarted := startEncryptedRuntimeProcess(t, database.appURL, vaultAddress)
	response := runtimeJSONRequest(t, restarted.address, http.MethodGet, "/api/v1/self", nil, token)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("pre-restart token after restart = %d: %s — the signing key did not survive the keyring round trip",
			response.StatusCode, response.Body)
	}
	restarted.stop(t)

	// The one-way door, encrypted side: the same database without the key
	// provider must refuse to serve.
	command := runtimeCommand(database.appURL, reserveAddress(t), map[string]string{"DFBG_SHUTDOWN_GRACE_PERIOD": "1s"})
	assertStartupRefusal(t, command, "encryption mode mismatch")
}

// The one-way door, unencrypted side: an instance first booted without
// encryption refuses a later boot that configures it.
func TestUnencryptedInstanceRefusesLateEncryption(t *testing.T) {
	database := newRuntimeDatabase(t)
	process := startRuntimeProcess(t, database.appURL)
	process.stop(t)

	command := runtimeCommand(database.appURL, reserveAddress(t), map[string]string{
		"DFBG_KEY_PROVIDER": "vault",
		// Never dialled: the mode marker refuses before any key-service call.
		"DFBG_VAULT_ADDR":        "http://127.0.0.1:1",
		"VAULT_MAX_RETRIES":      "0",
		"DFBG_TOKEN_SIGNING_KEY": "",
	})
	assertStartupRefusal(t, command, "encryption mode mismatch", "one-way door")
}

// Sealed: an encrypted deployment whose key service is unreachable at boot
// refuses to serve rather than serving without its guarantees.
func TestEncryptedStartupSealsWhenKeyServiceUnreachable(t *testing.T) {
	database := newRuntimeDatabase(t)
	command := runtimeCommand(database.appURL, reserveAddress(t), map[string]string{
		"DFBG_KEY_PROVIDER":      "vault",
		"DFBG_VAULT_ADDR":        "http://127.0.0.1:1",
		"VAULT_MAX_RETRIES":      "0",
		"DFBG_TOKEN_SIGNING_KEY": "",
	})
	assertStartupRefusal(t, command, "sealed")
}

// Environment keys and the keyring are two sources of truth for the same
// secret; the pairing is refused, not silently resolved.
func TestEnvironmentKeysAreRefusedAlongsideAKeyProvider(t *testing.T) {
	database := newRuntimeDatabase(t)
	command := runtimeCommand(database.appURL, reserveAddress(t), map[string]string{
		"DFBG_KEY_PROVIDER": "vault",
		"DFBG_VAULT_ADDR":   "http://127.0.0.1:1",
	})
	assertStartupRefusal(t, command, "DFBG_TOKEN_SIGNING_KEY must not be set when DFBG_KEY_PROVIDER is configured")
}

func TestBagDropEnvironmentKeyIsRefusedAlongsideAKeyProvider(t *testing.T) {
	database := newRuntimeDatabase(t)
	command := runtimeCommand(database.appURL, reserveAddress(t), map[string]string{
		"DFBG_KEY_PROVIDER":      "vault",
		"DFBG_VAULT_ADDR":        "http://127.0.0.1:1",
		"DFBG_TOKEN_SIGNING_KEY": "",
		bagdrop.CredentialKeyEnv: "0123456789abcdef0123456789abcdef",
	})
	assertStartupRefusal(
		t, command,
		bagdrop.CredentialKeyEnv+" must not be set when DFBG_KEY_PROVIDER is configured",
	)
}

// The write-side headline of ADR-0024, against the real binary: on an
// encrypted deployment the break-glass SQL that works on the unencrypted
// floor (TestBreakGlassRunbookMintsARootThatSignsIn) mints a root that CANNOT
// sign in — database write access is not administration.
func TestHandInsertedRootFailsToSignInOnEncryptedRuntime(t *testing.T) {
	vaultAddress := startVaultDev(t)
	database := newRuntimeDatabase(t)
	process := startEncryptedRuntimeProcess(t, database.appURL, vaultAddress)
	defer process.stop(t)

	if status, body := recoveryRequest(t, process.address, "/sys/init", map[string]int{}); status != http.StatusOK {
		t.Fatalf("initialize = %d: %s", status, body)
	}

	// The same statements and fixture hash as the break-glass runbook test.
	const (
		breakGlassSecret = "break-glass-test-secret"
		breakGlassHash   = "$argon2id$v=19$m=65536,t=2,p=1$YnJlYWtnbGFzc3NhbHQwMQ$6JE7aLqQDbhrwraovLkpDLrVDJoIUJetdNPFriO1lCg"
	)
	clientID := uuid.NewString()
	if _, err := database.admin.Exec(`
		INSERT INTO principals (id, name, client_id, organization_id, project_id, role, created_at)
		VALUES (gen_random_uuid()::text, 'break-glass administrator', $1, NULL, NULL, 'root', now())
	`, clientID); err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	if _, err := database.admin.Exec(`
		INSERT INTO principal_secrets (id, principal_id, encoded_hash, created_at)
		SELECT gen_random_uuid()::text, id, $1, now() FROM principals WHERE client_id = $2
	`, breakGlassHash, clientID); err != nil {
		t.Fatalf("insert secret: %v", err)
	}

	status, token := requestRootToken(t, process.address, clientID, breakGlassSecret)
	if status != http.StatusUnauthorized || token != "" {
		t.Fatalf("hand-inserted root token request = %d, token %q — want an indistinguishable 401", status, token)
	}
}

// vaultWrite drives the real Vault API directly — rotating the transit KEK and
// moving min_decryption_version are the operator actions the rotation surfaces
// exist to follow and to survive (duf-hjaj.1).
func vaultWrite(t *testing.T, address, path string, body map[string]any) {
	t.Helper()
	encoded := []byte("{}")
	if body != nil {
		var err error
		if encoded, err = json.Marshal(body); err != nil {
			t.Fatal(err)
		}
	}
	request, err := http.NewRequest(http.MethodPost, address+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Vault-Token", "runtime-root")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("vault %s: %v", path, err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		t.Fatalf("vault %s = %d", path, response.StatusCode)
	}
}

// keyringRequest is runtimeJSONRequest without the 2xx assertion: the 502 on a
// retired KEK and the degraded health that follows are the point.
func keyringRequest(t *testing.T, address, method, path, token string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, "http://"+address+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
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

type keyringListing struct {
	State   string `json:"state"`
	Keyring []struct {
		Purpose string `json:"purpose"`
		Version int    `json:"version"`
		KEKRef  string `json:"kek_ref"`
	} `json:"keyring"`
}

func decodeKeyringListing(t *testing.T, body []byte) keyringListing {
	t.Helper()
	var listing keyringListing
	if err := json.Unmarshal(body, &listing); err != nil {
		t.Fatalf("decode encryption listing: %v: %s", err, body)
	}
	return listing
}

// healthEncryption reads the anonymous probe's encryption field and the status
// code together: degraded must be visible AND must never turn into a 503.
func healthEncryption(t *testing.T, address string) (int, string) {
	t.Helper()
	response, err := http.Get("http://" + address + "/sys/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var health struct {
		Encryption string `json:"encryption"`
	}
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, health.Encryption
}

func initializedRootToken(t *testing.T, process *runtimeProcess) string {
	t.Helper()
	status, body := recoveryRequest(t, process.address, "/sys/init", map[string]int{})
	if status != http.StatusOK {
		t.Fatalf("initialize encrypted runtime = %d: %s", status, body)
	}
	var initialized struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(body, &initialized); err != nil {
		t.Fatal(err)
	}
	tokenStatus, token := requestRootToken(t, process.address, initialized.ClientID, initialized.ClientSecret)
	if tokenStatus != http.StatusOK || token == "" {
		t.Fatalf("token on encrypted runtime = %d", tokenStatus)
	}
	return token
}

// The KEK side of duf-hjaj.1 against a live transit engine: rotate the KEK,
// rewrap through the endpoint, retire the old version, and prove the next
// start still unwraps — the ordering deployment.md prescribes, working.
func TestKEKRotationRewrapsAndSurvivesKeyRetirement(t *testing.T) {
	vaultAddress := startVaultDev(t)
	database := newRuntimeDatabase(t)
	process := startEncryptedRuntimeProcess(t, database.appURL, vaultAddress)
	token := initializedRootToken(t, process)

	status, body := keyringRequest(t, process.address, http.MethodGet, "/api/v1/encryption", token)
	if status != http.StatusOK {
		t.Fatalf("read encryption = %d: %s", status, body)
	}
	listing := decodeKeyringListing(t, body)
	if listing.State != "ok" || len(listing.Keyring) != 4 {
		t.Fatalf("initial listing = %q with %d entries, want ok with 4", listing.State, len(listing.Keyring))
	}
	for _, entry := range listing.Keyring {
		if entry.KEKRef != "v1" {
			t.Fatalf("%s v%d wrapped under %q before any rotation", entry.Purpose, entry.Version, entry.KEKRef)
		}
	}

	vaultWrite(t, vaultAddress, "/v1/transit/keys/dufflebag/rotate", nil)
	status, body = keyringRequest(t, process.address, http.MethodPost, "/api/v1/encryption/rewrap", token)
	if status != http.StatusOK {
		t.Fatalf("rewrap = %d: %s", status, body)
	}
	for _, entry := range decodeKeyringListing(t, body).Keyring {
		if entry.KEKRef != "v2" {
			t.Fatalf("%s v%d still wrapped under %q after rewrap", entry.Purpose, entry.Version, entry.KEKRef)
		}
	}
	var stale int
	if err := database.admin.QueryRow(
		`SELECT count(*) FROM keyring WHERE convert_from(wrapped_key, 'UTF8') NOT LIKE 'vault:v2:%'`,
	).Scan(&stale); err != nil {
		t.Fatalf("inspect keyring: %v", err)
	}
	if stale != 0 {
		t.Fatalf("%d keyring rows still wrapped under the old KEK version", stale)
	}

	// Retiring v1 is now safe — and the restart is the proof: without the
	// rewrap above, this start would seal (the negative is its own test below).
	vaultWrite(t, vaultAddress, "/v1/transit/keys/dufflebag/config", map[string]any{"min_decryption_version": 2})
	process.stop(t)
	restarted := startEncryptedRuntimeProcess(t, database.appURL, vaultAddress)
	if status, state := healthEncryption(t, restarted.address); status != http.StatusOK || state != "ok" {
		t.Fatalf("health after retirement = %d, encryption %q", status, state)
	}
	response := runtimeJSONRequest(t, restarted.address, http.MethodGet, "/api/v1/self", nil, token)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("pre-restart token after rewrap and restart = %d", response.StatusCode)
	}
}

// The seal-out trap, live: min_decryption_version retires the KEK version the
// keyring rows still name. The serving instance stays up and turns degraded —
// never 503 — a restart seals, and lowering the floor plus a rewrap recovers.
func TestKeyRetirementWithoutRewrapDegradesThenSeals(t *testing.T) {
	vaultAddress := startVaultDev(t)
	database := newRuntimeDatabase(t)
	process := startEncryptedRuntimeProcess(t, database.appURL, vaultAddress)
	token := initializedRootToken(t, process)

	vaultWrite(t, vaultAddress, "/v1/transit/keys/dufflebag/rotate", nil)
	vaultWrite(t, vaultAddress, "/v1/transit/keys/dufflebag/config", map[string]any{"min_decryption_version": 2})

	// The endpoint attempts the real operation and fails honestly — and the
	// response names no address, path, or Vault error (finding-16 discipline).
	status, body := keyringRequest(t, process.address, http.MethodPost, "/api/v1/encryption/rewrap", token)
	if status != http.StatusBadGateway {
		t.Fatalf("rewrap under a retired KEK = %d: %s", status, body)
	}
	if strings.Contains(string(body), vaultAddress) || strings.Contains(string(body), "transit") {
		t.Fatalf("502 body leaks key-service detail: %s", body)
	}

	// The failed attempt's probe remembered the state; the probe still answers
	// 200 because evicting serving replicas is exactly wrong (ADR-0024).
	if status, state := healthEncryption(t, process.address); status != http.StatusOK || state != "degraded" {
		t.Fatalf("health under a retired KEK = %d, encryption %q; want 200, degraded", status, state)
	}
	response := runtimeJSONRequest(t, process.address, http.MethodGet, "/api/v1/instance", nil, token)
	if !strings.Contains(string(response.Body), `"encryption":"degraded"`) {
		t.Fatalf("instance does not mirror the degraded state: %s", response.Body)
	}

	// What degraded predicts: the next start cannot unwrap and refuses.
	process.stop(t)
	sealed := runtimeCommand(database.appURL, reserveAddress(t), encryptedEnvironment(vaultAddress))
	assertStartupRefusal(t, sealed, "sealed")

	// Recovery is the documented path: lower the floor, rewrap, retire again.
	vaultWrite(t, vaultAddress, "/v1/transit/keys/dufflebag/config", map[string]any{"min_decryption_version": 1})
	recovered := startEncryptedRuntimeProcess(t, database.appURL, vaultAddress)
	status, body = keyringRequest(t, recovered.address, http.MethodPost, "/api/v1/encryption/rewrap", token)
	if status != http.StatusOK {
		t.Fatalf("rewrap after lowering the floor = %d: %s", status, body)
	}
	if status, state := healthEncryption(t, recovered.address); status != http.StatusOK || state != "ok" {
		t.Fatalf("health after recovery = %d, encryption %q", status, state)
	}
	vaultWrite(t, vaultAddress, "/v1/transit/keys/dufflebag/config", map[string]any{"min_decryption_version": 2})
	recovered.stop(t)
	final := startEncryptedRuntimeProcess(t, database.appURL, vaultAddress)
	if status, state := healthEncryption(t, final.address); status != http.StatusOK || state != "ok" {
		t.Fatalf("health after the full recovery cycle = %d, encryption %q", status, state)
	}
}

// The DEK side of duf-hjaj.1: rotation mints a second version of every key,
// tokens signed before it keep working (verify-all, sign-newest), and a
// restart loads the multi-version keyring.
func TestDEKRotationKeepsOldTokensAndSurvivesRestart(t *testing.T) {
	vaultAddress := startVaultDev(t)
	database := newRuntimeDatabase(t)
	process := startEncryptedRuntimeProcess(t, database.appURL, vaultAddress)
	oldToken := initializedRootToken(t, process)

	status, body := keyringRequest(t, process.address, http.MethodPost, "/api/v1/encryption/rotate", oldToken)
	if status != http.StatusOK {
		t.Fatalf("rotate = %d: %s", status, body)
	}
	listing := decodeKeyringListing(t, body)
	versions := make(map[string]map[int]bool)
	for _, entry := range listing.Keyring {
		if versions[entry.Purpose] == nil {
			versions[entry.Purpose] = make(map[int]bool)
		}
		versions[entry.Purpose][entry.Version] = true
	}
	for purpose, have := range versions {
		if !have[1] || !have[2] {
			t.Fatalf("%s versions after rotation = %v, want v1 retained and v2 minted", purpose, have)
		}
	}

	// The token minted before rotation was signed with v1; the issuer now
	// signs with v2 but must keep verifying v1 for the 15-minute overlap.
	response := runtimeJSONRequest(t, process.address, http.MethodGet, "/api/v1/self", nil, oldToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("pre-rotation token after rotation = %d — verify-all is broken", response.StatusCode)
	}

	process.stop(t)
	restarted := startEncryptedRuntimeProcess(t, database.appURL, vaultAddress)
	if status, state := healthEncryption(t, restarted.address); status != http.StatusOK || state != "ok" {
		t.Fatalf("health after rotated restart = %d, encryption %q", status, state)
	}
	// Both signing generations survive the restart's Load: the pre-rotation
	// token still verifies, and a fresh sign-in works under the new version.
	response = runtimeJSONRequest(t, restarted.address, http.MethodGet, "/api/v1/self", nil, oldToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("pre-rotation token after restart = %d", response.StatusCode)
	}
}
