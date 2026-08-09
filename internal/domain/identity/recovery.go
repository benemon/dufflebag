package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudflare/circl/group"
	"github.com/cloudflare/circl/secretsharing"
)

// ErrSharesRejected reports shares that decoded and recombined but did not
// reproduce the stored verifier — the caller does not hold custody.
var ErrSharesRejected = errors.New("recovery shares rejected")

// recoverySharePrefix versions the share encoding so a share held for years is
// still unambiguous about how to read it.
const recoverySharePrefix = "dfbg-recovery-1:"

// maxRecoveryShares bounds n at initialization. Vault caps unseal shares the
// same way; past a few hundred custodians the ceremony is the wrong tool.
const maxRecoveryShares = 255

// recoveryGroup is the prime-order group the shares live over (ADR-0024). Its
// scalars marshal to 32 bytes, so a share is two of those plus the prefix.
var recoveryGroup = group.P256

// NewRecoveryShares mints the recovery secret and splits it into shareCount
// shares, any threshold of which prove custody to /sys/recovery.
//
// The shares appear here and nowhere else. What the caller stores is the
// digest: the secret is 256-bit random material, not a password, so a plain
// digest is the correct verifier — memory-hard hashing defends low-entropy
// inputs (ADR-0024).
func NewRecoveryShares(shareCount, threshold int) (shares []string, digest []byte, err error) {
	if threshold < 1 {
		return nil, nil, fmt.Errorf("%w: recovery threshold must be at least 1", ErrInvalid)
	}
	if shareCount < threshold {
		return nil, nil, fmt.Errorf("%w: recovery shares must be at least the threshold", ErrInvalid)
	}
	if shareCount > maxRecoveryShares {
		return nil, nil, fmt.Errorf("%w: recovery shares must be at most %d", ErrInvalid, maxRecoveryShares)
	}
	secret := recoveryGroup.RandomNonZeroScalar(rand.Reader)
	// CIRCL's (t,n) sharing recovers from t+1 shares, so threshold k maps to
	// polynomial degree k-1.
	sharer := secretsharing.New(rand.Reader, uint(threshold-1), secret)
	for _, share := range sharer.Share(uint(shareCount)) {
		encoded, err := encodeRecoveryShare(share)
		if err != nil {
			return nil, nil, err
		}
		shares = append(shares, encoded)
	}
	digest, err = recoveryDigest(secret)
	if err != nil {
		return nil, nil, err
	}
	return shares, digest, nil
}

// VerifyRecoveryShares recombines the presented shares and checks the result
// against the stored digest. Malformed or duplicated shares and too few of
// them wrap ErrInvalid; shares that recombine to the wrong secret return
// ErrSharesRejected.
func VerifyRecoveryShares(shares []string, threshold int, digest []byte) error {
	if len(digest) == 0 || threshold < 1 {
		return fmt.Errorf("%w: no recovery verifier", ErrInvalid)
	}
	if len(shares) < threshold {
		return fmt.Errorf("%w: recovery requires %d shares", ErrInvalid, threshold)
	}
	decoded := make([]secretsharing.Share, 0, len(shares))
	// Recover panics on duplicate share IDs rather than erroring, so duplicates
	// are refused here as malformed input.
	seen := make(map[string]struct{}, len(shares))
	for _, encoded := range shares {
		share, id, err := decodeRecoveryShare(encoded)
		if err != nil {
			return err
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("%w: duplicate recovery share", ErrInvalid)
		}
		seen[id] = struct{}{}
		decoded = append(decoded, share)
	}
	secret, err := secretsharing.Recover(uint(threshold-1), decoded)
	if err != nil {
		return fmt.Errorf("%w: recovery shares do not recombine", ErrInvalid)
	}
	got, err := recoveryDigest(secret)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(got, digest) != 1 {
		return ErrSharesRejected
	}
	return nil
}

func encodeRecoveryShare(share secretsharing.Share) (string, error) {
	id, err := share.ID.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("encode recovery share id: %w", err)
	}
	value, err := share.Value.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("encode recovery share value: %w", err)
	}
	return recoverySharePrefix + base64.RawURLEncoding.EncodeToString(append(id, value...)), nil
}

func decodeRecoveryShare(encoded string) (secretsharing.Share, string, error) {
	malformed := fmt.Errorf("%w: malformed recovery share", ErrInvalid)
	body, ok := strings.CutPrefix(encoded, recoverySharePrefix)
	if !ok {
		return secretsharing.Share{}, "", malformed
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || len(raw) != 64 {
		return secretsharing.Share{}, "", malformed
	}
	share := secretsharing.Share{ID: recoveryGroup.NewScalar(), Value: recoveryGroup.NewScalar()}
	if err := share.ID.UnmarshalBinary(raw[:32]); err != nil || share.ID.IsZero() {
		return secretsharing.Share{}, "", malformed
	}
	if err := share.Value.UnmarshalBinary(raw[32:]); err != nil {
		return secretsharing.Share{}, "", malformed
	}
	return share, string(raw[:32]), nil
}

func recoveryDigest(secret group.Scalar) ([]byte, error) {
	canonical, err := secret.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("encode recovery secret: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return digest[:], nil
}
