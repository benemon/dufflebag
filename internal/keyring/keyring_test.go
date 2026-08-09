package keyring

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
)

// xorProvider is a deliberately trivial Provider: enough to prove wrap state
// round-trips through Generate and Load without a real key service.
type xorProvider struct {
	calls int
	fail  bool
}

func (p *xorProvider) Wrap(_ context.Context, plaintext []byte) ([]byte, string, error) {
	p.calls++
	if p.fail {
		return nil, "", errors.New("key service unreachable")
	}
	blob := make([]byte, len(plaintext))
	for i, b := range plaintext {
		blob[i] = b ^ 0x5a
	}
	return blob, "kek-v1", nil
}

func (p *xorProvider) Unwrap(_ context.Context, blob []byte, kekRef string) ([]byte, error) {
	p.calls++
	if p.fail {
		return nil, errors.New("key service unreachable")
	}
	if kekRef != "kek-v1" {
		return nil, fmt.Errorf("unknown kek ref %q", kekRef)
	}
	plaintext := make([]byte, len(blob))
	for i, b := range blob {
		plaintext[i] = b ^ 0x5a
	}
	return plaintext, nil
}

func generatedRing(t *testing.T) (*Keyring, []Entry) {
	t.Helper()
	ring, entries, err := Generate(context.Background(), &xorProvider{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return ring, entries
}

func TestGenerateThenLoadRoundTripsEveryPurpose(t *testing.T) {
	ring, entries := generatedRing(t)
	if len(entries) != 4 {
		t.Fatalf("generated %d entries, want 4 purposes", len(entries))
	}
	for _, entry := range entries {
		if entry.KEKRef != "kek-v1" || entry.Version != 1 || len(entry.Wrapped) == 0 {
			t.Fatalf("entry %#v is incomplete", entry)
		}
	}

	loaded, err := Load(context.Background(), &xorProvider{}, entries)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The loaded ring holds the same keys: ciphertext from one decrypts under
	// the other, and MACs verify across.
	aad := []byte("org|project|build|b-1")
	sealed, err := ring.Encrypt([]byte(`{"cpu":"amd64"}`), aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	opened, err := loaded.Decrypt(sealed, aad)
	if err != nil {
		t.Fatalf("Decrypt after reload: %v", err)
	}
	if string(opened) != `{"cpu":"amd64"}` {
		t.Fatalf("round trip = %q", opened)
	}
	if err := loaded.VerifyMAC(ring.MAC([]byte("row")), []byte("row")); err != nil {
		t.Fatalf("MAC across reload: %v", err)
	}
}

func TestDecryptRefusesTheWrongAAD(t *testing.T) {
	ring, _ := generatedRing(t)
	sealed, err := ring.Encrypt([]byte("payload"), []byte("org-a|project-a|build|b-1"))
	if err != nil {
		t.Fatal(err)
	}
	// The ADR's stated gap-closer: a valid ciphertext relocated to another
	// tenant or row must fail, not decrypt somewhere it should not.
	for _, aad := range []string{
		"org-b|project-a|build|b-1", // another tenant
		"org-a|project-a|build|b-2", // another row
		"",
	} {
		if _, err := ring.Decrypt(sealed, []byte(aad)); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("aad %q: got %v, want ErrDecrypt", aad, err)
		}
	}
	if _, err := ring.Decrypt(sealed, []byte("org-a|project-a|build|b-1")); err != nil {
		t.Fatalf("correct aad refused: %v", err)
	}
}

func TestDecryptRefusesTamperedAndMalformedEnvelopes(t *testing.T) {
	ring, _ := generatedRing(t)
	aad := []byte("aad")
	sealed, err := ring.Encrypt([]byte("payload"), aad)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 0x01
	for name, envelope := range map[string][]byte{
		"tampered ciphertext": tampered,
		"empty":               {},
		"plaintext json":      []byte(`{"cpu":"amd64"}`),
		"truncated":           sealed[:8],
		"unknown key version": append([]byte{envelopeMagic, 0, 0, 0, 9}, sealed[5:]...),
	} {
		if _, err := ring.Decrypt(envelope, aad); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("%s: got %v, want ErrDecrypt", name, err)
		}
	}
}

func TestMACDetectsAlterationAndAbsence(t *testing.T) {
	ring, _ := generatedRing(t)
	message := []byte("org|project|versions|v-1|fp|1")
	mac := ring.MAC(message)
	if err := ring.VerifyMAC(mac, message); err != nil {
		t.Fatalf("genuine MAC refused: %v", err)
	}

	altered := bytes.Clone(message)
	altered[len(altered)-1] = '2'
	if err := ring.VerifyMAC(mac, altered); !errors.Is(err, ErrMAC) {
		t.Fatalf("altered row: got %v, want ErrMAC", err)
	}
	// The hand-inserted-row shape: no MAC at all must fail, never pass.
	if err := ring.VerifyMAC(nil, message); !errors.Is(err, ErrMAC) {
		t.Fatalf("absent MAC: got %v, want ErrMAC", err)
	}
	tamperedMAC := bytes.Clone(mac)
	tamperedMAC[len(tamperedMAC)-1] ^= 0x01
	if err := ring.VerifyMAC(tamperedMAC, message); !errors.Is(err, ErrMAC) {
		t.Fatalf("tampered MAC: got %v, want ErrMAC", err)
	}
}

func TestKeysAreDistinctPerPurpose(t *testing.T) {
	ring, _ := generatedRing(t)
	signing := ring.TokenSigningKey()
	audit, version := ring.AuditHMACKey()
	if version != "keyring-v1" {
		t.Fatalf("audit key version = %q", version)
	}
	// ADR-0020: the audit key must be distinct from the signing key; and
	// neither may double as the payload or integrity key.
	seen := map[string][]byte{
		"signing": signing, "audit": audit,
		"payload":   ring.keys[PurposePayload][1],
		"integrity": ring.keys[PurposeIntegrity][1],
	}
	for a, aKey := range seen {
		for b, bKey := range seen {
			if a != b && bytes.Equal(aKey, bKey) {
				t.Fatalf("%s and %s keys are identical", a, b)
			}
		}
	}
}

func TestLoadRefusesAPartialKeyring(t *testing.T) {
	_, entries := generatedRing(t)
	for drop := range 4 {
		partial := make([]Entry, 0, 3)
		for i, entry := range entries {
			if i != drop {
				partial = append(partial, entry)
			}
		}
		if _, err := Load(context.Background(), &xorProvider{}, partial); err == nil {
			t.Fatalf("keyring without %s loaded", entries[drop].Purpose)
		}
	}
}

func TestGenerateAndLoadFailClosedWhenProviderUnreachable(t *testing.T) {
	if _, _, err := Generate(context.Background(), &xorProvider{fail: true}); err == nil {
		t.Fatal("Generate succeeded without the key service")
	}
	_, entries := generatedRing(t)
	if _, err := Load(context.Background(), &xorProvider{fail: true}, entries); err == nil {
		t.Fatal("Load succeeded without the key service — sealed means refusing")
	}
}

func TestDecryptUsesTheVersionTheEnvelopeNames(t *testing.T) {
	ring, _ := generatedRing(t)
	// A second payload DEK becomes active; old envelopes still name v1 and
	// must keep decrypting without re-encryption (ADR-0024 rotation).
	old := ring.keys[PurposePayload][1]
	aad := []byte("aad")
	sealedV1, err := ring.Encrypt([]byte("old payload"), aad)
	if err != nil {
		t.Fatal(err)
	}
	fresh := bytes.Clone(old)
	fresh[0] ^= 0xff
	ring.keys[PurposePayload][2] = fresh
	ring.active[PurposePayload] = 2

	sealedV2, err := ring.Encrypt([]byte("new payload"), aad)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		envelope []byte
		want     string
	}{
		{sealedV1, "old payload"},
		{sealedV2, "new payload"},
	} {
		got, err := ring.Decrypt(tc.envelope, aad)
		if err != nil || string(got) != tc.want {
			t.Fatalf("decrypt = %q, %v; want %q", got, err, tc.want)
		}
	}
}
