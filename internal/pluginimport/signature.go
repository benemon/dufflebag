// Package pluginimport mirrors plugin versions from upstream releases into an
// organization's plugin registry (ADR-0027 A1).
package pluginimport

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// hashicorp.asc is https://www.hashicorp.com/.well-known/pgp-key.txt, the key
// releases.hashicorp.com signs every SHA256SUMS with (ADR-0027 A3).
//
//go:embed hashicorp.asc
var hashicorpKey []byte

// A rotated key arrives as a dufflebag release that changes this pin, never
// as a silent change of trust.
const hashicorpFingerprint = "C874011F0AB405110D02105534365D9472D7468F"

// HashiCorpKeyring returns the pinned HashiCorp release-signing key.
func HashiCorpKeyring() (openpgp.EntityList, error) {
	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(hashicorpKey))
	if err != nil {
		return nil, fmt.Errorf("read HashiCorp key: %w", err)
	}
	if len(ring) != 1 {
		return nil, fmt.Errorf("HashiCorp key file holds %d keys, want 1", len(ring))
	}
	if got := strings.ToUpper(hex.EncodeToString(ring[0].PrimaryKey.Fingerprint)); got != hashicorpFingerprint {
		return nil, fmt.Errorf("HashiCorp key fingerprint is %s, want %s", got, hashicorpFingerprint)
	}
	return ring, nil
}

func verifySignature(ring openpgp.EntityList, sums, signature []byte) error {
	if _, err := openpgp.CheckDetachedSignature(ring, bytes.NewReader(sums), bytes.NewReader(signature), nil); err != nil {
		return fmt.Errorf("SHA256SUMS signature does not verify against the HashiCorp key: %w", err)
	}
	return nil
}
