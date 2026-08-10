package keyring

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Key purposes. Distinct entries with independent rotation: ADR-0020 requires
// the audit key be distinct from the signing key, and payload confidentiality
// and row integrity are different jobs.
const (
	PurposePayload      = "payload"
	PurposeIntegrity    = "integrity"
	PurposeTokenSigning = "token_signing"
	PurposeAuditHMAC    = "audit_hmac"
)

var purposes = []string{PurposePayload, PurposeIntegrity, PurposeTokenSigning, PurposeAuditHMAC}

const keyBytes = 32 // AES-256 and HMAC-SHA256 alike

// envelopeMagic versions the ciphertext and MAC encodings. It is also what
// keeps them distinguishable from plaintext JSON, whose first byte is never
// 0x01.
const envelopeMagic = 0x01

var (
	// ErrDecrypt covers every decryption failure — wrong AAD, truncated or
	// tampered ciphertext, unknown key version. One error, deliberately: the
	// distinctions matter to an investigator reading logs, not to a caller
	// deciding what to serve, and AES-GCM itself does not say why a tag failed.
	ErrDecrypt = errors.New("payload decryption failed")
	// ErrMAC reports a row whose integrity MAC is absent or does not verify.
	ErrMAC = errors.New("row integrity verification failed")
)

// Sealed reports whether stored bytes carry the envelope encoding — used by
// readers whose column legitimately holds either a plaintext default or a
// sealed payload.
func Sealed(b []byte) bool { return len(b) > 0 && b[0] == envelopeMagic }

// Entry is one stored keyring row: a wrapped key and the KEK reference that
// wrapped it (which KEK version, so rotation knows what still needs rewrapping).
type Entry struct {
	Purpose   string
	Version   uint32
	Wrapped   []byte
	KEKRef    string
	WrappedAt time.Time
}

// Keyring holds the unwrapped keys in memory. Built once at startup; if the
// provider is unreachable then, the instance refuses to serve — sealed, in
// Vault's vocabulary — and per-write operations never call out.
type Keyring struct {
	mu     sync.RWMutex
	keys   map[string]map[uint32][]byte
	active map[string]uint32
}

// New assembles a keyring from unwrapped keys. Every purpose must be present:
// a partial keyring would serve some guarantees and silently drop others.
func New(keys map[string]map[uint32][]byte) (*Keyring, error) {
	ring := &Keyring{keys: keys, active: make(map[string]uint32, len(purposes))}
	for _, purpose := range purposes {
		versions := keys[purpose]
		if len(versions) == 0 {
			return nil, fmt.Errorf("keyring: no %s key", purpose)
		}
		newest := uint32(0)
		for version, key := range versions {
			if len(key) != keyBytes {
				return nil, fmt.Errorf("keyring: %s v%d is %d bytes, want %d", purpose, version, len(key), keyBytes)
			}
			if version > newest {
				newest = version
			}
		}
		ring.active[purpose] = newest
	}
	return ring, nil
}

// Generate mints a fresh key set locally (crypto/rand — DEK generation is
// deliberately not the provider's job, ADR-0024) and wraps each entry for
// storage. The plaintext keys live in the returned Keyring and nowhere else.
func Generate(ctx context.Context, provider Provider) (*Keyring, []Entry, error) {
	keys := make(map[string]map[uint32][]byte, len(purposes))
	entries := make([]Entry, 0, len(purposes))
	for _, purpose := range purposes {
		key := make([]byte, keyBytes)
		if _, err := rand.Read(key); err != nil {
			return nil, nil, fmt.Errorf("generate %s key: %w", purpose, err)
		}
		wrapped, kekRef, err := provider.Wrap(ctx, key)
		if err != nil {
			return nil, nil, fmt.Errorf("wrap %s key: %w", purpose, err)
		}
		keys[purpose] = map[uint32][]byte{1: key}
		entries = append(entries, Entry{Purpose: purpose, Version: 1, Wrapped: wrapped, KEKRef: kekRef})
	}
	ring, err := New(keys)
	if err != nil {
		return nil, nil, err
	}
	return ring, entries, nil
}

// Load unwraps stored entries into a usable keyring.
func Load(ctx context.Context, provider Provider, entries []Entry) (*Keyring, error) {
	keys := make(map[string]map[uint32][]byte)
	for _, entry := range entries {
		key, err := provider.Unwrap(ctx, entry.Wrapped, entry.KEKRef)
		if err != nil {
			return nil, fmt.Errorf("unwrap %s v%d: %w", entry.Purpose, entry.Version, err)
		}
		if keys[entry.Purpose] == nil {
			keys[entry.Purpose] = make(map[uint32][]byte)
		}
		keys[entry.Purpose][entry.Version] = key
	}
	return New(keys)
}

// Encrypt seals plaintext under the active payload DEK with AES-256-GCM,
// binding the additional authenticated data — tenant and row identity — so a
// valid ciphertext moved to another row or tenant fails to decrypt.
// Envelope: magic | key version (uint32 BE) | nonce | ciphertext+tag.
func (r *Keyring) Encrypt(plaintext, aad []byte) ([]byte, error) {
	r.mu.RLock()
	version := r.active[PurposePayload]
	key := append([]byte(nil), r.keys[PurposePayload][version]...)
	r.mu.RUnlock()
	return encryptWithKey(plaintext, aad, key, version)
}

// EncryptWithKey seals a payload with an environment-supplied AES-256 key
// using the same versioned envelope as Keyring.Encrypt. Version 1 is the sole
// environment-key version; keyring-backed deployments carry their rotations
// in the wrapped keyring instead.
func EncryptWithKey(plaintext, aad, key []byte) ([]byte, error) {
	return encryptWithKey(plaintext, aad, key, 1)
}

func encryptWithKey(plaintext, aad, key []byte, version uint32) ([]byte, error) {
	sealer, err := newSealer(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, sealer.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}
	envelope := make([]byte, 0, 5+len(nonce)+len(plaintext)+sealer.Overhead())
	envelope = append(envelope, envelopeMagic)
	envelope = binary.BigEndian.AppendUint32(envelope, version)
	envelope = append(envelope, nonce...)
	return sealer.Seal(envelope, nonce, plaintext, aad), nil
}

// Decrypt opens an envelope with the DEK version it names, under the same AAD
// it was sealed with.
func (r *Keyring) Decrypt(envelope, aad []byte) ([]byte, error) {
	if len(envelope) < 5 || envelope[0] != envelopeMagic {
		return nil, ErrDecrypt
	}
	version := binary.BigEndian.Uint32(envelope[1:5])
	r.mu.RLock()
	key, ok := r.keys[PurposePayload][version]
	key = append([]byte(nil), key...)
	r.mu.RUnlock()
	if !ok {
		return nil, ErrDecrypt
	}
	return decryptWithKey(envelope, aad, key, version)
}

// DecryptWithKey opens a version-1 environment-key envelope.
func DecryptWithKey(envelope, aad, key []byte) ([]byte, error) {
	return decryptWithKey(envelope, aad, key, 1)
}

func decryptWithKey(envelope, aad, key []byte, version uint32) ([]byte, error) {
	if len(envelope) < 5 || envelope[0] != envelopeMagic ||
		binary.BigEndian.Uint32(envelope[1:5]) != version {
		return nil, ErrDecrypt
	}
	sealer, err := newSealer(key)
	if err != nil {
		return nil, ErrDecrypt
	}
	rest := envelope[5:]
	if len(rest) < sealer.NonceSize() {
		return nil, ErrDecrypt
	}
	plaintext, err := sealer.Open(nil, rest[:sealer.NonceSize()], rest[sealer.NonceSize():], aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

func newSealer(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// MAC authenticates a row: HMAC-SHA256 over the caller's canonical message
// (which must include tenant and row identity, the same binding Encrypt gets
// from AAD), prefixed magic | key version.
func (r *Keyring) MAC(message []byte) []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
	version := r.active[PurposeIntegrity]
	mac := make([]byte, 0, 5+sha256.Size)
	mac = append(mac, envelopeMagic)
	mac = binary.BigEndian.AppendUint32(mac, version)
	digest := hmac.New(sha256.New, r.keys[PurposeIntegrity][version])
	_, _ = digest.Write(message)
	return digest.Sum(mac)
}

// VerifyMAC checks a stored MAC against the recomputed message. An absent MAC
// fails: on an encrypted deployment a row without one was not written by this
// application, which is exactly what the check exists to catch.
func (r *Keyring) VerifyMAC(mac, message []byte) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(mac) != 5+sha256.Size || mac[0] != envelopeMagic {
		return ErrMAC
	}
	key, ok := r.keys[PurposeIntegrity][binary.BigEndian.Uint32(mac[1:5])]
	if !ok {
		return ErrMAC
	}
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write(message)
	if !hmac.Equal(mac[5:], digest.Sum(nil)) {
		return ErrMAC
	}
	return nil
}

// TokenSigningKey is the active token signing key (PurposeTokenSigning).
func (r *Keyring) TokenSigningKey() []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]byte(nil), r.keys[PurposeTokenSigning][r.active[PurposeTokenSigning]]...)
}

// TokenSigningKeys returns every signing key newest first. Callers use the
// first for new tokens and retain the rest for the lifetime of older tokens.
func (r *Keyring) TokenSigningKeys() [][]byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
	versions := make([]int, 0, len(r.keys[PurposeTokenSigning]))
	for version := range r.keys[PurposeTokenSigning] {
		versions = append(versions, int(version))
	}
	sort.Sort(sort.Reverse(sort.IntSlice(versions)))
	keys := make([][]byte, 0, len(versions))
	for _, version := range versions {
		keys = append(keys, append([]byte(nil), r.keys[PurposeTokenSigning][uint32(version)]...))
	}
	return keys
}

// AuditHMACKey is the active audit HMAC key and its version label, distinct
// from the signing key per ADR-0020.
func (r *Keyring) AuditHMACKey() ([]byte, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	version := r.active[PurposeAuditHMAC]
	return append([]byte(nil), r.keys[PurposeAuditHMAC][version]...), fmt.Sprintf("keyring-v%d", version)
}

func (r *Keyring) has(purpose string, version uint32) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.keys[purpose][version]
	return ok
}

func (r *Keyring) adopt(keys map[string]map[uint32][]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for purpose, versions := range keys {
		if r.keys[purpose] == nil {
			r.keys[purpose] = make(map[uint32][]byte)
		}
		for version, key := range versions {
			r.keys[purpose][version] = append([]byte(nil), key...)
			if version > r.active[purpose] {
				r.active[purpose] = version
			}
		}
	}
}
