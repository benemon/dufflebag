package bagdrop

import (
	"fmt"

	"github.com/benemon/dufflebag/internal/keyring"
)

const CredentialKeyEnv = "DFBG_BAGDROP_CREDENTIAL_KEY"

const credentialAADKind = "bagdrop_credential"
const credentialRowID = "bagdrop_config"

type CredentialSealer struct {
	ring           *keyring.Keyring
	environmentKey []byte
}

func NewCredentialSealer(ring *keyring.Keyring, environmentKey string) *CredentialSealer {
	return &CredentialSealer{ring: ring, environmentKey: []byte(environmentKey)}
}

func credentialAAD(organizationID, projectID string) []byte {
	return []byte(organizationID + "|" + projectID + "|" + credentialAADKind + "|" + credentialRowID)
}

func (s *CredentialSealer) Seal(organizationID, projectID, secret string) ([]byte, error) {
	aad := credentialAAD(organizationID, projectID)
	if s.ring != nil {
		sealed, err := s.ring.Encrypt([]byte(secret), aad)
		if err != nil {
			return nil, fmt.Errorf("%w: seal credential: %v", ErrCredentialSeal, err)
		}
		return sealed, nil
	}
	if err := s.requireEnvironmentKey(); err != nil {
		return nil, err
	}
	sealed, err := keyring.EncryptWithKey([]byte(secret), aad, s.environmentKey)
	if err != nil {
		return nil, fmt.Errorf("%w: seal credential: %v", ErrCredentialSeal, err)
	}
	return sealed, nil
}

func (s *CredentialSealer) Unseal(organizationID, projectID string, sealed []byte) (string, error) {
	aad := credentialAAD(organizationID, projectID)
	var (
		plaintext []byte
		err       error
	)
	if s.ring != nil {
		plaintext, err = s.ring.Decrypt(sealed, aad)
	} else {
		if err := s.requireEnvironmentKey(); err != nil {
			return "", err
		}
		plaintext, err = keyring.DecryptWithKey(sealed, aad, s.environmentKey)
	}
	if err != nil {
		return "", fmt.Errorf("%w: unseal credential: %v", ErrCredentialSeal, err)
	}
	return string(plaintext), nil
}

func (s *CredentialSealer) requireEnvironmentKey() error {
	if len(s.environmentKey) == 0 {
		return fmt.Errorf("%w: %s is required to seal and unseal Bag Drop credentials on an unencrypted deployment", ErrCredentialSeal, CredentialKeyEnv)
	}
	if len(s.environmentKey) != 32 {
		return fmt.Errorf("%w: %s must be exactly 32 bytes for AES-256-GCM", ErrCredentialSeal, CredentialKeyEnv)
	}
	return nil
}
