package keyring

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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
		name       string
		method     string
		role       string
		wantMethod string
		wantError  string
	}{
		{name: "unset defaults to env", wantMethod: "env"},
		{name: "kubernetes requires role", method: "kubernetes", wantError: vaultKubernetesRoleEnv},
		{name: "unknown lists valid methods", method: "approle", wantError: "valid methods: env, kubernetes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(vaultAuthMethodEnv, test.method)
			t.Setenv(vaultKubernetesRoleEnv, test.role)
			config, err := vaultAuthConfigurationFromEnvironment()
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("configuration error = %v, want an error containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if config.method != test.wantMethod {
				t.Fatalf("method = %q, want %q", config.method, test.wantMethod)
			}
		})
	}
}

// An unset VAULT_ADDR must refuse with a message naming it rather than letting
// the SDK's localhost default report as "sealed" (duf-tsp6). Agent and proxy
// addresses are each an accepted substitute; an explicit localhost is honoured.
func TestTransitRefusesAnUndiagnosedVaultAddress(t *testing.T) {
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
}
