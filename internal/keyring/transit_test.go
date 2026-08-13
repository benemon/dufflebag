package keyring

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	vault "github.com/hashicorp/vault/api"
)

func TestTransitRewrapUsesServerSideEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The SDK's Logical writer sends PUT; Vault's HTTP layer treats PUT and
		// POST identically on logical write paths, and the live-Vault
		// integration lane proves the pairing for real.
		if r.Method != http.MethodPut || r.URL.Path != "/v1/transit/rewrap/dufflebag" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["ciphertext"] != "vault:v1:old" {
			t.Fatalf("ciphertext = %v", body["ciphertext"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ciphertext":"vault:v2:new"}}`))
	}))
	defer server.Close()

	client, err := vault.NewClient(&vault.Config{Address: server.URL, HttpClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &transitProvider{client: client, mount: "transit", key: "dufflebag"}
	wrapped, kekRef, err := provider.Rewrap(context.Background(), []byte("vault:v1:old"), "v1")
	if err != nil {
		t.Fatalf("Rewrap: %v", err)
	}
	if string(wrapped) != "vault:v2:new" || kekRef != "v2" {
		t.Fatalf("Rewrap = %q, %q", wrapped, kekRef)
	}
}

func TestVaultAuthSelector(t *testing.T) {
	tests := []struct {
		name             string
		method           string
		role             string
		appRoleID        string
		appRoleSecretID  string
		authNamespace    string
		wantMethod       string
		wantMount        string
		wantErrorStrings []string
	}{
		{name: "unset defaults to token", wantMethod: "token"},
		{name: "env names the rename", method: "env", wantErrorStrings: []string{"env", "token"}},
		{name: "kubernetes requires role", method: "kubernetes", wantErrorStrings: []string{vaultKubernetesRoleEnv}},
		{name: "approle requires role id", method: "approle", wantErrorStrings: []string{vaultAppRoleRoleIDEnv}},
		{
			name:             "approle requires secret-id file",
			method:           "approle",
			appRoleID:        "dufflebag",
			wantErrorStrings: []string{vaultAppRoleSecretIDFileEnv},
		},
		{
			name:            "approle mount defaults to approle",
			method:          "approle",
			appRoleID:       "dufflebag",
			appRoleSecretID: "/run/secrets/dufflebag-vault-secret-id",
			wantMethod:      "approle",
			wantMount:       "approle",
		},
		{
			name:             "unknown lists valid methods",
			method:           "ldap",
			wantErrorStrings: []string{"valid methods: token, kubernetes, approle"},
		},
		{
			name:             "token refuses auth namespace",
			method:           "token",
			authNamespace:    "auth-ns",
			wantErrorStrings: []string{vaultAuthNamespaceEnv, vaultAuthMethodEnv},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(vaultAuthMethodEnv, test.method)
			t.Setenv(vaultKubernetesRoleEnv, test.role)
			t.Setenv(vaultAppRoleRoleIDEnv, test.appRoleID)
			t.Setenv(vaultAppRoleSecretIDFileEnv, test.appRoleSecretID)
			t.Setenv(vaultAppRoleMountEnv, "")
			t.Setenv(vaultAuthNamespaceEnv, test.authNamespace)
			config, err := vaultAuthConfigurationFromEnvironment()
			if len(test.wantErrorStrings) != 0 {
				if err == nil {
					t.Fatalf("configuration error = nil, want an error containing %q", test.wantErrorStrings)
				}
				for _, want := range test.wantErrorStrings {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("configuration error = %v, want an error containing %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if config.method != test.wantMethod {
				t.Fatalf("method = %q, want %q", config.method, test.wantMethod)
			}
			if config.mount != test.wantMount {
				t.Fatalf("mount = %q, want %q", config.mount, test.wantMount)
			}
		})
	}
}

// An unset VAULT_ADDR must refuse with a message naming it rather than letting
// the SDK's localhost default report as "sealed" (duf-tsp6). Agent and proxy
// addresses are each an accepted substitute; an explicit localhost is honoured.
func TestTransitRefusesAnUndiagnosedVaultAddress(t *testing.T) {
	t.Setenv(vaultAuthMethodEnv, "")
	t.Setenv(vaultAuthNamespaceEnv, "")
	for _, name := range []string{"VAULT_ADDR", "VAULT_AGENT_ADDR", "VAULT_PROXY_ADDR"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := transitFromEnvironment(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "VAULT_ADDR is required") {
		t.Fatalf("unset address = %v, want a refusal naming VAULT_ADDR", err)
	}

	for _, accepted := range []string{"VAULT_ADDR", "VAULT_AGENT_ADDR", "VAULT_PROXY_ADDR"} {
		t.Run(accepted, func(t *testing.T) {
			t.Setenv(accepted, "https://127.0.0.1:8200")
			if _, err := transitFromEnvironment(context.Background()); err != nil {
				t.Fatalf("%s set = %v, want the provider to construct", accepted, err)
			}
		})
	}

	t.Run("kubernetes requires VAULT_ADDR", func(t *testing.T) {
		t.Setenv(vaultAuthMethodEnv, "kubernetes")
		t.Setenv(vaultKubernetesRoleEnv, "dufflebag")
		t.Setenv("VAULT_AGENT_ADDR", "https://127.0.0.1:8200")
		if _, err := transitFromEnvironment(context.Background()); err == nil ||
			!strings.Contains(err.Error(), "VAULT_ADDR is required") {
			t.Fatalf("agent-only Kubernetes address = %v, want a refusal naming VAULT_ADDR", err)
		}
	})

	t.Run("approle requires VAULT_ADDR", func(t *testing.T) {
		t.Setenv(vaultAuthMethodEnv, "approle")
		t.Setenv(vaultAppRoleRoleIDEnv, "dufflebag")
		t.Setenv(vaultAppRoleSecretIDFileEnv, "/run/secrets/dufflebag-vault-secret-id")
		t.Setenv("VAULT_AGENT_ADDR", "https://127.0.0.1:8200")
		if _, err := transitFromEnvironment(context.Background()); err == nil ||
			!strings.Contains(err.Error(), "VAULT_ADDR is required") {
			t.Fatalf("agent-only AppRole address = %v, want a refusal naming VAULT_ADDR", err)
		}
	})
}

type recordedVaultRequest struct {
	method    string
	path      string
	namespace string
	token     string
	body      map[string]any
}

type fakeTransitVault struct {
	mu              sync.Mutex
	requests        []recordedVaultRequest
	loginCount      int
	shortFirstLease bool
}

func (f *fakeTransitVault) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	request := recordedVaultRequest{
		method:    r.Method,
		path:      r.URL.Path,
		namespace: r.Header.Get("X-Vault-Namespace"),
		token:     r.Header.Get("X-Vault-Token"),
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&request.body)
	}

	f.mu.Lock()
	f.requests = append(f.requests, request)
	login := strings.HasPrefix(r.URL.Path, "/v1/auth/") && strings.HasSuffix(r.URL.Path, "/login")
	loginCount := f.loginCount
	if login {
		f.loginCount++
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if login {
		token := "token-1"
		leaseDuration := 3600
		renewable := true
		if loginCount > 0 {
			token = "token-2"
		}
		if f.shortFirstLease && loginCount == 0 {
			leaseDuration = 1
			renewable = false
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"auth": map[string]any{
				"client_token":   token,
				"lease_duration": leaseDuration,
				"renewable":      renewable,
			},
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": map[string]any{"ciphertext": "vault:v1:wrapped"},
	})
}

func (f *fakeTransitVault) snapshot() []recordedVaultRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedVaultRequest(nil), f.requests...)
}

func configureFakeVaultEnvironment(t *testing.T, address string) {
	t.Helper()
	t.Setenv("VAULT_ADDR", address)
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("VAULT_NAMESPACE", "")
	t.Setenv("VAULT_AGENT_ADDR", "")
	t.Setenv("VAULT_PROXY_ADDR", "")
	t.Setenv(vaultAuthMethodEnv, "")
	t.Setenv(vaultAuthNamespaceEnv, "")
	t.Setenv(vaultKubernetesRoleEnv, "")
	t.Setenv(vaultKubernetesMountEnv, "")
	t.Setenv(vaultKubernetesTokenPathEnv, "")
	t.Setenv(vaultAppRoleRoleIDEnv, "")
	t.Setenv(vaultAppRoleSecretIDFileEnv, "")
	t.Setenv(vaultAppRoleMountEnv, "")
}

func requestAtPath(t *testing.T, requests []recordedVaultRequest, path string) recordedVaultRequest {
	t.Helper()
	for _, request := range requests {
		if request.path == path {
			return request
		}
	}
	t.Fatalf("no request at %s; requests = %#v", path, requests)
	return recordedVaultRequest{}
}

func TestTransitAppRoleSplitNamespaces(t *testing.T) {
	fake := &fakeTransitVault{}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	configureFakeVaultEnvironment(t, server.URL)

	secretIDPath := t.TempDir() + "/secret-id"
	if err := os.WriteFile(secretIDPath, []byte("secret-from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VAULT_NAMESPACE", "operating-ns")
	t.Setenv(vaultAuthMethodEnv, "approle")
	t.Setenv(vaultAuthNamespaceEnv, "auth-ns")
	t.Setenv(vaultAppRoleRoleIDEnv, "role-from-environment")
	t.Setenv(vaultAppRoleSecretIDFileEnv, secretIDPath)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	provider, err := transitFromEnvironment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := provider.Wrap(ctx, []byte("plaintext")); err != nil {
		t.Fatal(err)
	}

	requests := fake.snapshot()
	login := requestAtPath(t, requests, "/v1/auth/approle/login")
	// The SDK's Logical writer sends PUT; Vault's HTTP layer treats PUT and
	// POST identically on logical write paths.
	if login.method != http.MethodPut {
		t.Fatalf("login method = %s, want PUT", login.method)
	}
	if login.namespace != "auth-ns" {
		t.Fatalf("login namespace = %q, want auth-ns", login.namespace)
	}
	if login.body["role_id"] != "role-from-environment" || login.body["secret_id"] != "secret-from-file" {
		t.Fatalf("login body = %#v", login.body)
	}
	wrap := requestAtPath(t, requests, "/v1/transit/encrypt/dufflebag")
	if wrap.namespace != "operating-ns" {
		t.Fatalf("transit namespace = %q, want operating-ns", wrap.namespace)
	}
	if wrap.token != "token-1" {
		t.Fatalf("transit token = %q, want token-1", wrap.token)
	}
}

func TestTransitKubernetesSplitNamespaces(t *testing.T) {
	fake := &fakeTransitVault{}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	configureFakeVaultEnvironment(t, server.URL)

	tokenPath := t.TempDir() + "/token"
	if err := os.WriteFile(tokenPath, []byte("projected-jwt"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VAULT_NAMESPACE", "operating-ns")
	t.Setenv(vaultAuthMethodEnv, "kubernetes")
	t.Setenv(vaultAuthNamespaceEnv, "auth-ns")
	t.Setenv(vaultKubernetesRoleEnv, "dufflebag")
	t.Setenv(vaultKubernetesTokenPathEnv, tokenPath)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	provider, err := transitFromEnvironment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := provider.Wrap(ctx, []byte("plaintext")); err != nil {
		t.Fatal(err)
	}

	requests := fake.snapshot()
	login := requestAtPath(t, requests, "/v1/auth/kubernetes/login")
	if login.method != http.MethodPut {
		t.Fatalf("login method = %s, want PUT", login.method)
	}
	if login.namespace != "auth-ns" {
		t.Fatalf("login namespace = %q, want auth-ns", login.namespace)
	}
	if login.body["role"] != "dufflebag" || login.body["jwt"] != "projected-jwt" {
		t.Fatalf("login body = %#v", login.body)
	}
	wrap := requestAtPath(t, requests, "/v1/transit/encrypt/dufflebag")
	if wrap.namespace != "operating-ns" {
		t.Fatalf("transit namespace = %q, want operating-ns", wrap.namespace)
	}
	if wrap.token != "token-1" {
		t.Fatalf("transit token = %q, want token-1", wrap.token)
	}
}

func TestTransitAuthNamespaceDefaultsToOperatingNamespace(t *testing.T) {
	fake := &fakeTransitVault{}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	configureFakeVaultEnvironment(t, server.URL)

	secretIDPath := t.TempDir() + "/secret-id"
	if err := os.WriteFile(secretIDPath, []byte("secret-id"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VAULT_NAMESPACE", "operating-ns")
	t.Setenv(vaultAuthMethodEnv, "approle")
	t.Setenv(vaultAppRoleRoleIDEnv, "dufflebag")
	t.Setenv(vaultAppRoleSecretIDFileEnv, secretIDPath)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if _, err := transitFromEnvironment(ctx); err != nil {
		t.Fatal(err)
	}
	login := requestAtPath(t, fake.snapshot(), "/v1/auth/approle/login")
	if login.namespace != "operating-ns" {
		t.Fatalf("login namespace = %q, want operating-ns", login.namespace)
	}
}

func TestTransitTokenModeUsesSDKCredentialWithoutLogin(t *testing.T) {
	fake := &fakeTransitVault{}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	configureFakeVaultEnvironment(t, server.URL)
	t.Setenv("VAULT_TOKEN", "static-token")

	provider, err := transitFromEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := provider.Wrap(context.Background(), []byte("plaintext")); err != nil {
		t.Fatal(err)
	}

	requests := fake.snapshot()
	for _, request := range requests {
		if strings.HasPrefix(request.path, "/v1/auth/") {
			t.Fatalf("token mode made an auth request: %#v", request)
		}
	}
	wrap := requestAtPath(t, requests, "/v1/transit/encrypt/dufflebag")
	if wrap.token != "static-token" {
		t.Fatalf("transit token = %q, want static-token", wrap.token)
	}
}

func TestTransitReloginPropagatesToken(t *testing.T) {
	fake := &fakeTransitVault{shortFirstLease: true}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	configureFakeVaultEnvironment(t, server.URL)

	secretIDPath := t.TempDir() + "/secret-id"
	if err := os.WriteFile(secretIDPath, []byte("secret-id"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(vaultAuthMethodEnv, "approle")
	t.Setenv(vaultAppRoleRoleIDEnv, "dufflebag")
	t.Setenv(vaultAppRoleSecretIDFileEnv, secretIDPath)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	provider, err := transitFromEnvironment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, err := provider.Wrap(ctx, []byte("after re-login")); err != nil {
			t.Fatal(err)
		}
		for _, request := range fake.snapshot() {
			if request.path == "/v1/transit/encrypt/dufflebag" && request.token == "token-2" {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no transit request used token-2; requests = %#v", fake.snapshot())
}
