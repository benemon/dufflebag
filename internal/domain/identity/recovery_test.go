package identity

import (
	"errors"
	"strings"
	"testing"
)

func TestRecoverySharesRoundTrip(t *testing.T) {
	for _, tc := range []struct{ shares, threshold int }{
		{1, 1}, // the single-operator default
		{3, 2},
		{5, 5},
	} {
		shares, digest, err := NewRecoveryShares(tc.shares, tc.threshold)
		if err != nil {
			t.Fatalf("NewRecoveryShares(%d,%d): %v", tc.shares, tc.threshold, err)
		}
		if len(shares) != tc.shares {
			t.Fatalf("got %d shares, want %d", len(shares), tc.shares)
		}
		for _, share := range shares {
			if !strings.HasPrefix(share, recoverySharePrefix) {
				t.Fatalf("share %q lacks the versioned prefix", share)
			}
		}
		if err := VerifyRecoveryShares(shares[:tc.threshold], tc.threshold, digest); err != nil {
			t.Fatalf("verify with exactly the threshold: %v", err)
		}
		// Any subset of the threshold size proves custody, not just the first.
		if err := VerifyRecoveryShares(shares[tc.shares-tc.threshold:], tc.threshold, digest); err != nil {
			t.Fatalf("verify with the last %d shares: %v", tc.threshold, err)
		}
	}
}

func TestRecoverySharesMoreThanThresholdStillVerify(t *testing.T) {
	shares, digest, err := NewRecoveryShares(3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRecoveryShares(shares, 2, digest); err != nil {
		t.Fatalf("verify with all three shares at threshold 2: %v", err)
	}
}

func TestRecoverySharesFromAnotherInstanceAreRejected(t *testing.T) {
	shares, _, err := NewRecoveryShares(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, otherDigest, err := NewRecoveryShares(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRecoveryShares(shares, 1, otherDigest); !errors.Is(err, ErrSharesRejected) {
		t.Fatalf("foreign shares: got %v, want ErrSharesRejected", err)
	}
}

func TestRecoverySharesInsufficientCountIsInvalid(t *testing.T) {
	shares, digest, err := NewRecoveryShares(3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRecoveryShares(shares[:1], 2, digest); !errors.Is(err, ErrInvalid) {
		t.Fatalf("one share at threshold 2: got %v, want ErrInvalid", err)
	}
}

func TestRecoverySharesMixedGoodAndBadAreRejected(t *testing.T) {
	shares, digest, err := NewRecoveryShares(3, 2)
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := NewRecoveryShares(3, 2)
	if err != nil {
		t.Fatal(err)
	}
	// One genuine share plus a foreign one recombines to the wrong secret.
	if err := VerifyRecoveryShares([]string{shares[0], foreign[1]}, 2, digest); !errors.Is(err, ErrSharesRejected) {
		t.Fatalf("mixed shares: got %v, want ErrSharesRejected", err)
	}
}

func TestRecoverySharesDuplicatesAreInvalidNotAPanic(t *testing.T) {
	shares, digest, err := NewRecoveryShares(3, 2)
	if err != nil {
		t.Fatal(err)
	}
	// CIRCL's Recover panics on duplicated share IDs; the domain must refuse
	// them as malformed input instead.
	if err := VerifyRecoveryShares([]string{shares[0], shares[0]}, 2, digest); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate share: got %v, want ErrInvalid", err)
	}
}

func TestRecoverySharesMalformedEncodingsAreInvalid(t *testing.T) {
	_, digest, err := NewRecoveryShares(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, share := range []string{
		"",
		"not-a-share",
		"dfbg-recovery-2:AAAA",       // unknown version
		recoverySharePrefix + "!!!!", // not base64url
		recoverySharePrefix + "AAAA", // too short
		recoverySharePrefix + strings.Repeat("A", 86), // 64 zero bytes: zero ID
	} {
		if err := VerifyRecoveryShares([]string{share}, 1, digest); !errors.Is(err, ErrInvalid) {
			t.Fatalf("share %q: got %v, want ErrInvalid", share, err)
		}
	}
}

func TestRecoverySharesTamperedShareIsRejected(t *testing.T) {
	shares, digest, err := NewRecoveryShares(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	tampered := []byte(shares[0])
	// Mid-string, not the final character: base64 decoding ignores the unused
	// trailing bits of the last character, so flipping it may change nothing.
	at := len(tampered) - 10
	if tampered[at] == 'A' {
		tampered[at] = 'B'
	} else {
		tampered[at] = 'A'
	}
	err = VerifyRecoveryShares([]string{string(tampered)}, 1, digest)
	// A flipped byte either breaks the scalar encoding or changes the secret;
	// both are refusals, never success.
	if err == nil {
		t.Fatal("tampered share verified")
	}
	if !errors.Is(err, ErrSharesRejected) && !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered share: got %v", err)
	}
}

func TestNewRecoverySharesRefusesUnusableParameters(t *testing.T) {
	for _, tc := range []struct{ shares, threshold int }{
		{0, 0},
		{1, 0},
		{0, 1},
		{2, 3}, // threshold above share count is unrecoverable by construction
		{maxRecoveryShares + 1, 1},
	} {
		if _, _, err := NewRecoveryShares(tc.shares, tc.threshold); !errors.Is(err, ErrInvalid) {
			t.Fatalf("NewRecoveryShares(%d,%d): got %v, want ErrInvalid", tc.shares, tc.threshold, err)
		}
	}
}

func TestVerifyRecoverySharesRefusesAbsentVerifier(t *testing.T) {
	shares, _, err := NewRecoveryShares(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRecoveryShares(shares, 1, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil digest: got %v, want ErrInvalid", err)
	}
	if err := VerifyRecoveryShares(shares, 0, []byte{1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero threshold: got %v, want ErrInvalid", err)
	}
}
