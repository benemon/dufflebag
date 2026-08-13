package bagdrop

import (
	"fmt"

	"github.com/benemon/dufflebag/internal/credseal"
	"github.com/benemon/dufflebag/internal/keyring"
)

const CredentialKeyEnv = "DFBG_BAGDROP_CREDENTIAL_KEY"

const credentialAADKind = "bagdrop_credential"
const credentialRowID = "bagdrop_config"

type CredentialSealer struct {
	sealer *credseal.Sealer
}

func NewCredentialSealer(ring *keyring.Keyring, environmentKey string) *CredentialSealer {
	return &CredentialSealer{sealer: credseal.New(ring, environmentKey)}
}

// Mode reports which deployment key protects Bag Drop credentials.
func (s *CredentialSealer) Mode() string {
	return s.sealer.Mode()
}

func (s *CredentialSealer) Seal(organizationID, projectID, secret string) ([]byte, error) {
	sealed, err := s.sealer.Seal(organizationID, projectID, credentialAADKind, credentialRowID, secret)
	if err != nil {
		return nil, fmt.Errorf("%w: %v (%s is the supported alias)", ErrCredentialSeal, err, CredentialKeyEnv)
	}
	return sealed, nil
}

func (s *CredentialSealer) Unseal(organizationID, projectID string, sealed []byte) (string, error) {
	plaintext, err := s.sealer.Unseal(organizationID, projectID, credentialAADKind, credentialRowID, sealed)
	if err != nil {
		return "", fmt.Errorf("%w: %v (%s is the supported alias)", ErrCredentialSeal, err, CredentialKeyEnv)
	}
	return plaintext, nil
}
