//go:build integration

package keyring

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	vault "github.com/hashicorp/vault/api"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestTransitKubernetesAuth(t *testing.T) {
	ctx := context.Background()
	const rootToken = "dufflebag-integration-root"
	// The Kubernetes auth plugin verifies pem_keys locally, then asks the
	// TokenReview API whether the signed service account still exists. A small
	// stub keeps this oracle independent of a live cluster; the wrong-key case
	// below proves local signature rejection by asserting the stub is not called.
	var tokenReviews atomic.Int64
	reviewer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/apis/authentication.k8s.io/v1/tokenreviews" {
			http.Error(w, "unexpected TokenReview request", http.StatusNotFound)
			return
		}
		var request struct {
			Spec struct {
				Token string `json:"token"`
			} `json:"spec"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Spec.Token == "" {
			http.Error(w, "invalid TokenReview", http.StatusBadRequest)
			return
		}
		tokenReviews.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"apiVersion": "authentication.k8s.io/v1",
			"kind":       "TokenReview",
			"status": map[string]any{
				"authenticated": true,
				"audiences":     []string{"vault"},
				"user": map[string]any{
					"username": "system:serviceaccount:dufflebag-system:dufflebag",
					"uid":      "00000000-0000-4000-8000-000000000001",
					"groups":   []string{"system:serviceaccounts", "system:serviceaccounts:dufflebag-system"},
				},
			},
		})
	}))
	t.Cleanup(reviewer.Close)
	reviewerURL, err := url.Parse(reviewer.URL)
	if err != nil {
		t.Fatal(err)
	}
	reviewerPort, err := strconv.Atoi(reviewerURL.Port())
	if err != nil {
		t.Fatal(err)
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:           "hashicorp/vault:1.17",
			ExposedPorts:    []string{"8200/tcp"},
			HostAccessPorts: []int{reviewerPort},
			Env: map[string]string{
				"VAULT_DEV_ROOT_TOKEN_ID":  rootToken,
				"VAULT_DEV_LISTEN_ADDRESS": "0.0.0.0:8200",
			},
			WaitingFor: wait.ForHTTP("/v1/sys/health").WithPort("8200/tcp").WithStartupTimeout(time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start Vault: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Errorf("terminate Vault: %v", err)
		}
	})
	port, err := container.MappedPort(ctx, "8200/tcp")
	if err != nil {
		t.Fatalf("Vault mapped port: %v", err)
	}
	address := fmt.Sprintf("http://127.0.0.1:%s", port.Port())
	client, err := vault.NewClient(&vault.Config{Address: address, HttpClient: vault.DefaultConfig().HttpClient})
	if err != nil {
		t.Fatal(err)
	}
	client.SetToken(rootToken)

	signingKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate service-account signing key: %v", err)
	}
	publicKey, err := x509.MarshalPKIXPublicKey(&signingKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal service-account public key: %v", err)
	}
	publicPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicKey}))

	if err := client.Sys().EnableAuthWithOptions("kubernetes", &vault.EnableAuthOptions{Type: "kubernetes"}); err != nil {
		t.Fatalf("enable Kubernetes auth: %v", err)
	}
	if _, err := client.Logical().Write("auth/kubernetes/config", map[string]any{
		"kubernetes_host":        fmt.Sprintf("http://host.testcontainers.internal:%d", reviewerPort),
		"pem_keys":               []string{publicPEM},
		"disable_local_ca_jwt":   true,
		"disable_iss_validation": false,
		"issuer":                 "https://kubernetes.default.svc",
	}); err != nil {
		t.Fatalf("configure Kubernetes auth: %v", err)
	}
	if err := client.Sys().Mount("transit", &vault.MountInput{Type: "transit"}); err != nil {
		t.Fatalf("enable transit: %v", err)
	}
	if _, err := client.Logical().Write("transit/keys/dufflebag", nil); err != nil {
		t.Fatalf("create transit key: %v", err)
	}
	if err := client.Sys().PutPolicy("dufflebag-transit", `
path "transit/encrypt/dufflebag" { capabilities = ["update"] }
path "transit/decrypt/dufflebag" { capabilities = ["update"] }
`); err != nil {
		t.Fatalf("create transit policy: %v", err)
	}
	if _, err := client.Logical().Write("auth/kubernetes/role/dufflebag", map[string]any{
		"bound_service_account_names":      []string{"dufflebag"},
		"bound_service_account_namespaces": []string{"dufflebag-system"},
		"audience":                         "vault",
		"token_policies":                   []string{"dufflebag-transit"},
		"token_ttl":                        "10s",
		"token_max_ttl":                    "30s",
	}); err != nil {
		t.Fatalf("create Kubernetes auth role: %v", err)
	}

	mintToken := func(t *testing.T, key *rsa.PrivateKey) string {
		t.Helper()
		now := time.Now()
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": "https://kubernetes.default.svc",
			"sub": "system:serviceaccount:dufflebag-system:dufflebag",
			"aud": []string{"vault"},
			"iat": now.Unix(),
			"exp": now.Add(5 * time.Minute).Unix(),
			"kubernetes.io": map[string]any{
				"namespace": "dufflebag-system",
				"serviceaccount": map[string]any{
					"name": "dufflebag",
					"uid":  "00000000-0000-4000-8000-000000000001",
				},
			},
		})
		signed, err := token.SignedString(key)
		if err != nil {
			t.Fatalf("sign service-account token: %v", err)
		}
		return signed
	}
	newProvider := func(t *testing.T, token string) (*transitProvider, context.CancelFunc, error) {
		t.Helper()
		tokenPath := t.TempDir() + "/token"
		if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
			t.Fatalf("write service-account token: %v", err)
		}
		t.Setenv("VAULT_ADDR", address)
		t.Setenv("VAULT_TOKEN", "")
		t.Setenv("VAULT_AGENT_ADDR", "")
		t.Setenv("VAULT_PROXY_ADDR", "")
		t.Setenv(vaultAuthMethodEnv, "kubernetes")
		t.Setenv(vaultKubernetesRoleEnv, "dufflebag")
		t.Setenv(vaultKubernetesMountEnv, "kubernetes")
		t.Setenv(vaultKubernetesTokenPathEnv, tokenPath)
		providerCtx, cancel := context.WithCancel(context.Background())
		provider, err := transitFromEnvironment(providerCtx)
		if err != nil {
			cancel()
			return nil, func() {}, err
		}
		return provider.(*transitProvider), cancel, nil
	}

	t.Run("login and round trip", func(t *testing.T) {
		provider, cancel, err := newProvider(t, mintToken(t, signingKey))
		if err != nil {
			t.Fatalf("construct provider: %v", err)
		}
		defer cancel()
		plaintext := []byte("wrapped through an authenticated transit client")
		wrapped, keyVersion, err := provider.Wrap(ctx, plaintext)
		if err != nil {
			t.Fatalf("Wrap: %v", err)
		}
		unwrapped, err := provider.Unwrap(ctx, wrapped, keyVersion)
		if err != nil {
			t.Fatalf("Unwrap: %v", err)
		}
		if string(unwrapped) != string(plaintext) {
			t.Fatalf("Unwrap = %q, want %q", unwrapped, plaintext)
		}
	})

	t.Run("wrong signing key is refused", func(t *testing.T) {
		wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("generate wrong signing key: %v", err)
		}
		before := tokenReviews.Load()
		if _, cancel, err := newProvider(t, mintToken(t, wrongKey)); err == nil {
			cancel()
			t.Fatal("provider accepted a service-account token signed by an unconfigured key")
		}
		if after := tokenReviews.Load(); after != before {
			t.Fatalf("wrong-key JWT reached TokenReview: calls changed from %d to %d", before, after)
		}
	})

	t.Run("re-login after maximum token lifetime", func(t *testing.T) {
		provider, cancel, err := newProvider(t, mintToken(t, signingKey))
		if err != nil {
			t.Fatalf("construct provider: %v", err)
		}
		defer cancel()
		firstAccessor := tokenAccessor(t, provider.client)
		time.Sleep(40 * time.Second)
		if _, _, err := provider.Wrap(ctx, []byte("after the original token maximum lifetime")); err != nil {
			t.Fatalf("Wrap after renewal boundary: %v", err)
		}
		if accessor := tokenAccessor(t, provider.client); accessor == firstAccessor {
			t.Fatalf("token accessor remained %q past max TTL; want a re-login", accessor)
		}
	})
}

func tokenAccessor(t *testing.T, client *vault.Client) string {
	t.Helper()
	secret, err := client.Auth().Token().LookupSelf()
	if err != nil {
		t.Fatalf("look up Vault token: %v", err)
	}
	accessor, _ := secret.Data["accessor"].(string)
	if accessor == "" {
		t.Fatal("Vault token lookup returned no accessor")
	}
	return accessor
}
