package keyring

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	vault "github.com/hashicorp/vault/api"
	vaultapprole "github.com/hashicorp/vault/api/auth/approle"
	vaultkubernetes "github.com/hashicorp/vault/api/auth/kubernetes"
)

// Transit-specific settings. Vault connection settings use the SDK's own
// environment (VAULT_ADDR, VAULT_TOKEN, VAULT_NAMESPACE, VAULT_CACERT, ...);
// dufflebag's settings select the native login method and transit paths.
const (
	transitMountEnv             = "DFBG_VAULT_TRANSIT_MOUNT"
	transitKeyEnv               = "DFBG_VAULT_TRANSIT_KEY"
	vaultAuthMethodEnv          = "DFBG_VAULT_AUTH_METHOD"
	vaultAuthNamespaceEnv       = "DFBG_VAULT_AUTH_NAMESPACE"
	vaultKubernetesRoleEnv      = "DFBG_VAULT_K8S_ROLE"
	vaultKubernetesMountEnv     = "DFBG_VAULT_K8S_MOUNT"
	vaultKubernetesTokenPathEnv = "DFBG_VAULT_K8S_TOKEN_PATH"
	vaultAppRoleRoleIDEnv       = "DFBG_VAULT_APPROLE_ROLE_ID"
	vaultAppRoleSecretIDFileEnv = "DFBG_VAULT_APPROLE_SECRET_ID_FILE"
	vaultAppRoleMountEnv        = "DFBG_VAULT_APPROLE_MOUNT"

	defaultTransitMount             = "transit"
	defaultTransitKey               = "dufflebag"
	defaultVaultKubernetesMount     = "kubernetes"
	defaultVaultKubernetesTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	defaultVaultAppRoleMount        = "approle"
)

type vaultAuthConfiguration struct {
	method              string
	authNamespace       string
	role                string
	mount               string
	tokenPath           string
	appRoleID           string
	appRoleSecretIDFile string
}

// transitProvider wraps DEKs with Vault's transit engine. Vault never returns
// the KEK; encrypt/decrypt happen server-side against the named key.
type transitProvider struct {
	client *vault.Client
	mount  string
	key    string
}

func transitFromEnvironment(ctx context.Context) (Provider, error) {
	auth, err := vaultAuthConfigurationFromEnvironment()
	if err != nil {
		return nil, err
	}
	// Checked against the environment, not client.Address(): DefaultConfig
	// always seeds https://127.0.0.1:8200, so an address-emptiness guard can
	// never fire and an unset VAULT_ADDR would silently target localhost and
	// surface as "sealed" (duf-tsp6). An operator genuinely running Vault on
	// localhost sets the address explicitly and is unaffected.
	if auth.method != "token" && os.Getenv("VAULT_ADDR") == "" {
		return nil, fmt.Errorf("VAULT_ADDR is required when %s=vault and %s=%s", ProviderEnv, vaultAuthMethodEnv, auth.method)
	}
	if os.Getenv("VAULT_ADDR") == "" &&
		os.Getenv("VAULT_AGENT_ADDR") == "" && os.Getenv("VAULT_PROXY_ADDR") == "" {
		return nil, fmt.Errorf(
			"VAULT_ADDR is required when %s=vault (or VAULT_AGENT_ADDR/VAULT_PROXY_ADDR for agent-injected auth)",
			ProviderEnv,
		)
	}
	config := vault.DefaultConfig()
	if err := config.Error; err != nil {
		return nil, fmt.Errorf("vault configuration: %w", err)
	}
	client, err := vault.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("vault client: %w", err)
	}
	mount := os.Getenv(transitMountEnv)
	if mount == "" {
		mount = defaultTransitMount
	}
	key := os.Getenv(transitKeyEnv)
	if key == "" {
		key = defaultTransitKey
	}
	provider := &transitProvider{client: client, mount: mount, key: key}
	if auth.method != "token" {
		loginClient := client
		if auth.authNamespace != "" {
			loginClient = client.WithNamespace(auth.authNamespace)
		}
		secret, err := login(ctx, loginClient, auth)
		if err != nil {
			return nil, fmt.Errorf("vault %s login: %w", auth.method, err)
		}
		client.SetToken(secret.Auth.ClientToken)
		go renewToken(ctx, loginClient, client, auth, secret)
	}
	return provider, nil
}

func vaultAuthConfigurationFromEnvironment() (vaultAuthConfiguration, error) {
	method := os.Getenv(vaultAuthMethodEnv)
	if method == "" {
		method = "token"
	}
	// AppRole is the VM-ambient baseline. Vault Agent is deliberately not the
	// answer here: the Agent shapes and injects secrets, and its sink token
	// carries the Agent's policy scope, not this process's identity. Further
	// ambient methods (aws, gcp, azure, cert, ldap, userpass) land as new cases
	// on this seam when a deployment asks.
	switch method {
	case "env":
		return vaultAuthConfiguration{}, fmt.Errorf("%s: %q was renamed to %q", vaultAuthMethodEnv, "env", "token")
	case "token":
		if os.Getenv(vaultAuthNamespaceEnv) != "" {
			return vaultAuthConfiguration{}, fmt.Errorf("%s cannot be set when %s=token", vaultAuthNamespaceEnv, vaultAuthMethodEnv)
		}
		return vaultAuthConfiguration{method: method}, nil
	case "kubernetes":
		role := os.Getenv(vaultKubernetesRoleEnv)
		if role == "" {
			return vaultAuthConfiguration{}, fmt.Errorf("%s is required when %s=kubernetes", vaultKubernetesRoleEnv, vaultAuthMethodEnv)
		}
		mount := os.Getenv(vaultKubernetesMountEnv)
		if mount == "" {
			mount = defaultVaultKubernetesMount
		}
		tokenPath := os.Getenv(vaultKubernetesTokenPathEnv)
		if tokenPath == "" {
			tokenPath = defaultVaultKubernetesTokenPath
		}
		return vaultAuthConfiguration{
			method:        method,
			authNamespace: os.Getenv(vaultAuthNamespaceEnv),
			role:          role,
			mount:         mount,
			tokenPath:     tokenPath,
		}, nil
	case "approle":
		roleID := os.Getenv(vaultAppRoleRoleIDEnv)
		if roleID == "" {
			return vaultAuthConfiguration{}, fmt.Errorf("%s is required when %s=approle", vaultAppRoleRoleIDEnv, vaultAuthMethodEnv)
		}
		secretIDFile := os.Getenv(vaultAppRoleSecretIDFileEnv)
		if secretIDFile == "" {
			return vaultAuthConfiguration{}, fmt.Errorf("%s is required when %s=approle", vaultAppRoleSecretIDFileEnv, vaultAuthMethodEnv)
		}
		mount := os.Getenv(vaultAppRoleMountEnv)
		if mount == "" {
			mount = defaultVaultAppRoleMount
		}
		return vaultAuthConfiguration{
			method:              method,
			authNamespace:       os.Getenv(vaultAuthNamespaceEnv),
			mount:               mount,
			appRoleID:           roleID,
			appRoleSecretIDFile: secretIDFile,
		}, nil
	default:
		return vaultAuthConfiguration{}, fmt.Errorf("%s: unknown Vault auth method %q (valid methods: token, kubernetes, approle)", vaultAuthMethodEnv, method)
	}
}

func login(ctx context.Context, client *vault.Client, config vaultAuthConfiguration) (*vault.Secret, error) {
	switch config.method {
	case "kubernetes":
		// The auth object reads the token file when it is constructed. Constructing
		// it for every login also picks up a rotated projected service-account token.
		auth, err := vaultkubernetes.NewKubernetesAuth(
			config.role,
			vaultkubernetes.WithMountPath(config.mount),
			vaultkubernetes.WithServiceAccountTokenPath(config.tokenPath),
		)
		if err != nil {
			return nil, err
		}
		return client.Auth().Login(ctx, auth)
	case "approle":
		auth, err := vaultapprole.NewAppRoleAuth(
			config.appRoleID,
			&vaultapprole.SecretID{FromFile: config.appRoleSecretIDFile},
			vaultapprole.WithMountPath(config.mount),
		)
		if err != nil {
			return nil, err
		}
		return client.Auth().Login(ctx, auth)
	default:
		return nil, fmt.Errorf("unsupported auth method %q", config.method)
	}
}

func renewToken(ctx context.Context, loginClient, operatingClient *vault.Client, config vaultAuthConfiguration, secret *vault.Secret) {
	for {
		watcher, err := loginClient.NewLifetimeWatcher(&vault.LifetimeWatcherInput{Secret: secret})
		if err == nil {
			go watcher.Start()
			watching := true
			for watching {
				select {
				case <-ctx.Done():
					watcher.Stop()
					return
				case <-watcher.RenewCh():
				case err = <-watcher.DoneCh():
					watching = false
				}
			}
			watcher.Stop()
		}
		if err != nil {
			slog.Default().Warn("Vault token renewal ended", "method", config.method, "error", err)
		}

		backoff := time.Second
		for {
			var loginErr error
			secret, loginErr = login(ctx, loginClient, config)
			if loginErr == nil {
				operatingClient.SetToken(secret.Auth.ClientToken)
				break
			}
			// The five-minute unwrap heartbeat reports encryption as degraded;
			// renewal failures themselves do not crash an otherwise serving process.
			slog.Default().Warn("Vault re-login failed", "method", config.method, "error", loginErr, "retry_in", backoff)
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if backoff < time.Minute {
				backoff *= 2
				if backoff > time.Minute {
					backoff = time.Minute
				}
			}
		}
	}
}

func (p *transitProvider) Wrap(ctx context.Context, plaintext []byte) ([]byte, string, error) {
	secret, err := p.client.Logical().WriteWithContext(ctx,
		p.mount+"/encrypt/"+p.key,
		map[string]any{"plaintext": base64.StdEncoding.EncodeToString(plaintext)},
	)
	if err != nil {
		return nil, "", fmt.Errorf("transit wrap: %w", err)
	}
	if secret == nil {
		return nil, "", fmt.Errorf("transit wrap: response carries no ciphertext")
	}
	ciphertext, _ := secret.Data["ciphertext"].(string)
	if ciphertext == "" {
		return nil, "", fmt.Errorf("transit wrap: response carries no ciphertext")
	}
	// Transit ciphertext is self-describing: "vault:v<N>:...". The KEK
	// reference is that version, recorded alongside the blob so rotation can
	// tell which rows still need rewrapping.
	return []byte(ciphertext), transitKeyVersion(ciphertext), nil
}

func (p *transitProvider) Unwrap(ctx context.Context, blob []byte, _ string) ([]byte, error) {
	secret, err := p.client.Logical().WriteWithContext(ctx,
		p.mount+"/decrypt/"+p.key,
		map[string]any{"ciphertext": string(blob)},
	)
	if err != nil {
		return nil, fmt.Errorf("transit unwrap: %w", err)
	}
	if secret == nil {
		return nil, fmt.Errorf("transit unwrap: response carries no plaintext")
	}
	encoded, _ := secret.Data["plaintext"].(string)
	if encoded == "" {
		return nil, fmt.Errorf("transit unwrap: response carries no plaintext")
	}
	plaintext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("transit unwrap: %w", err)
	}
	return plaintext, nil
}

// Rewrap re-encrypts a stored blob under the current KEK server-side — the
// DEK plaintext never leaves Vault. The endpoint is written like encrypt and
// decrypt above: Vault's HTTP layer treats PUT and POST identically on
// logical write paths, so the SDK's Logical writer serves all three.
func (p *transitProvider) Rewrap(ctx context.Context, blob []byte, _ string) ([]byte, string, error) {
	secret, err := p.client.Logical().WriteWithContext(ctx,
		p.mount+"/rewrap/"+p.key,
		map[string]any{"ciphertext": string(blob)},
	)
	if err != nil {
		return nil, "", fmt.Errorf("transit rewrap: %w", err)
	}
	if secret == nil {
		return nil, "", fmt.Errorf("transit rewrap: response carries no ciphertext")
	}
	ciphertext, _ := secret.Data["ciphertext"].(string)
	if ciphertext == "" {
		return nil, "", fmt.Errorf("transit rewrap: response carries no ciphertext")
	}
	return []byte(ciphertext), transitKeyVersion(ciphertext), nil
}

// transitKeyVersion reads the "v<N>" out of "vault:v<N>:<ciphertext>".
func transitKeyVersion(ciphertext string) string {
	parts := strings.SplitN(ciphertext, ":", 3)
	if len(parts) == 3 && parts[0] == "vault" {
		return parts[1]
	}
	return ""
}
