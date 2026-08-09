// Package keyring implements encryption at rest (ADR-0024): a keyring of
// locally generated data-encryption keys held in memory, each stored wrapped
// by an external key service's key-encryption key. The external service is a
// startup and rotation dependency, never a write-path one — per-write
// encryption and MAC verification are purely local.
package keyring

import (
	"context"
	"fmt"
	"os"
)

// Provider wraps and unwraps DEKs with an external key-encryption key.
//
// Two methods only, deliberately: DEK generation stays local (crypto/rand),
// and native rewrap is an optional capability because not every provider has
// a server-side equivalent (ADR-0024).
type Provider interface {
	// Wrap encrypts a plaintext DEK, returning the opaque blob to store and a
	// reference identifying the KEK version that produced it.
	Wrap(ctx context.Context, plaintext []byte) (blob []byte, kekRef string, err error)
	// Unwrap reverses Wrap using the KEK version the blob names.
	Unwrap(ctx context.Context, blob []byte, kekRef string) ([]byte, error)
}

// Rewrapper is the optional server-side rewrap capability. Provider remains
// the portable two-method contract; providers without this capability use an
// unwrap-then-wrap fallback.
type Rewrapper interface {
	Rewrap(ctx context.Context, blob []byte, kekRef string) ([]byte, string, error)
}

// ProviderEnv selects the key provider. Empty means encryption at rest is not
// configured; the mode marker makes that a permanent property of the instance
// at first boot (ADR-0024).
const ProviderEnv = "DFBG_KEY_PROVIDER"

// ProviderFromEnvironment builds the configured provider, or returns nil when
// none is configured. Provider connection details stay in each provider's
// native vocabulary (VAULT_ADDR and kin); only dufflebag-born variables carry
// the DFBG_ prefix.
func ProviderFromEnvironment(ctx context.Context) (Provider, error) {
	switch name := os.Getenv(ProviderEnv); name {
	case "":
		return nil, nil
	case "vault":
		return transitFromEnvironment(ctx)
	default:
		return nil, fmt.Errorf("%s: unknown key provider %q (supported: vault)", ProviderEnv, name)
	}
}
