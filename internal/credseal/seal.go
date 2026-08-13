// Package credseal protects credentials stored outside the encrypted payload tables.
package credseal

import (
	"errors"
	"fmt"

	"github.com/benemon/dufflebag/internal/keyring"
)

var ErrUnavailable = errors.New("credential sealing unavailable")

type Sealer struct {
	ring           *keyring.Keyring
	environmentKey []byte
}

func New(ring *keyring.Keyring, environmentKey string) *Sealer {
	return &Sealer{ring: ring, environmentKey: []byte(environmentKey)}
}

func (s *Sealer) Mode() string {
	if s.ring != nil {
		return "keyring"
	}
	return "env_key"
}

func (s *Sealer) Seal(organizationID, projectID, kind, rowID, secret string) ([]byte, error) {
	aad := []byte(organizationID + "|" + projectID + "|" + kind + "|" + rowID)
	if s.ring != nil {
		sealed, err := s.ring.Encrypt([]byte(secret), aad)
		if err != nil {
			return nil, fmt.Errorf("%w: seal credential: %v", ErrUnavailable, err)
		}
		return sealed, nil
	}
	if err := s.requireEnvironmentKey(); err != nil {
		return nil, err
	}
	sealed, err := keyring.EncryptWithKey([]byte(secret), aad, s.environmentKey)
	if err != nil {
		return nil, fmt.Errorf("%w: seal credential: %v", ErrUnavailable, err)
	}
	return sealed, nil
}

func (s *Sealer) Unseal(organizationID, projectID, kind, rowID string, sealed []byte) (string, error) {
	aad := []byte(organizationID + "|" + projectID + "|" + kind + "|" + rowID)
	var plaintext []byte
	var err error
	if s.ring != nil {
		plaintext, err = s.ring.Decrypt(sealed, aad)
	} else {
		if err := s.requireEnvironmentKey(); err != nil {
			return "", err
		}
		plaintext, err = keyring.DecryptWithKey(sealed, aad, s.environmentKey)
	}
	if err != nil {
		return "", fmt.Errorf("%w: unseal credential: %v", ErrUnavailable, err)
	}
	return string(plaintext), nil
}

func (s *Sealer) requireEnvironmentKey() error {
	if len(s.environmentKey) == 0 {
		return fmt.Errorf("%w: %s is required to seal and unseal credentials on an unencrypted deployment", ErrUnavailable, CredentialKeyEnv)
	}
	if len(s.environmentKey) != 32 {
		return fmt.Errorf("%w: %s must be exactly 32 bytes for AES-256-GCM", ErrUnavailable, CredentialKeyEnv)
	}
	return nil
}
