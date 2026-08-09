// Package identity holds service principals and their credentials.
//
// A principal is modelled separately from its credentials so it can later carry
// an OIDC subject binding instead of, or alongside, a secret — swapping how a
// token is minted without touching anything that consumes one (ADR-0004).
package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

var (
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("conflict")
	ErrNotFound = errors.New("not found")
	// ErrRootPermanence marks the refusals that keep a root principal holding
	// at least one usable, never-expiring secret — without one, expiry locks
	// the instance out on a timer (duf-2rw). Distinguished so the platform
	// plane can answer it with its own message rather than the cap's.
	ErrRootPermanence = errors.New("a root principal must hold a usable secret that never expires")
	// ErrIntegrity reports an identity row failing keyring MAC verification on
	// an encrypted deployment (ADR-0024): the row was not written by this
	// application. Audited distinctly; answered indistinguishably on the wire.
	ErrIntegrity = errors.New("identity integrity verification failed")
)

// argon2id parameters. Deliberately named rather than inlined so a future
// change is a visible edit, and so the encoded hash can be read back against
// what produced it.
// RFC 9106's SECOND recommended profile. The first (m=2 GiB) is for offline
// key derivation; the second is the one intended for memory-constrained
// servers, and it is what a request-path verification should cost.
//
// Previously m=64 MiB, t=1, p=4. That is roughly equivalent strength for 3.5x
// the memory, and the memory is what an unauthenticated caller can spend on
// the token endpoint (duf-39p). Stored hashes carry the parameters they were
// written with and are verified against those, so hashes written before this
// change keep verifying.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024 // 19 MiB
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16

	secretBytes = 32 // 256 bits of entropy in the plaintext secret
)

// DummySecretHash carries the current verification parameters for timing
// equalisation. Authentication padding and the repository's client-id miss path
// must share this source so a parameter change cannot make their costs diverge.
const DummySecretHash = "$argon2id$v=19$m=19456,t=2,p=1$XlTPhjxTv+KIT+AriTHNpg$gPa3WcRFECTrrH3qzoICZ7jHlTnQju0Tt5CJicDsZcM"

var dummySecret = Secret{ID: "dummy-secret", encoded: DummySecretHash}

// MaxVerificationMemoryBytes is the most memory one authentication attempt can
// spend, so a caller admitting requests can size a budget against it.
//
// Multiplied by maxActiveSecrets because Authenticate always performs exactly
// that many verifications, padding missing usable secrets with DummySecretHash.
const MaxVerificationMemoryBytes = argonMemory * 1024 * maxActiveSecrets

// Secret is one credential belonging to a principal.
//
// The plaintext exists only in the response that issues it. What is retained is
// an argon2id hash and metadata — nothing here can reconstruct the secret.
type Secret struct {
	ID         string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	// ExpiresAt, when set, is the moment this credential stops granting
	// anything. Expiry lives on the SECRET, not the principal — that is what
	// makes overlap possible: the outgoing secret expires soon, the incoming
	// one later (duf-2rw).
	ExpiresAt *time.Time

	// encoded is the PHC-format argon2id hash. Unexported so a caller cannot
	// substitute one that did not come from newSecret or RestoreSecret.
	encoded string
}

// Encoded exposes the stored hash for persistence.
func (s Secret) Encoded() string { return s.encoded }

// Usable reports whether this credential still grants anything: an expired
// secret stays stored and visible — 'expired on the 4th' beats
// 'authentication failed' — but it authenticates nothing and does not count
// against the cap.
func (s Secret) Usable(now time.Time) bool {
	return s.ExpiresAt == nil || s.ExpiresAt.After(now)
}

// RestoreSecret rebuilds a Secret from storage.
func RestoreSecret(id, encoded string, createdAt time.Time, lastUsedAt, expiresAt *time.Time) (Secret, error) {
	if id == "" {
		return Secret{}, fmt.Errorf("%w: secret id is required", ErrInvalid)
	}
	if !strings.HasPrefix(encoded, "$argon2id$") {
		return Secret{}, fmt.Errorf("%w: secret hash is not argon2id", ErrInvalid)
	}
	return Secret{
		ID: id, CreatedAt: createdAt, LastUsedAt: lastUsedAt, ExpiresAt: expiresAt,
		encoded: encoded,
	}, nil
}

// newSecret mints a credential, returning the plaintext exactly once.
func newSecret(id string, at time.Time, expiresAt *time.Time) (Secret, string, error) {
	plaintext, err := randomString(secretBytes)
	if err != nil {
		return Secret{}, "", err
	}
	encoded, err := hashSecret(plaintext)
	if err != nil {
		return Secret{}, "", err
	}
	return Secret{ID: id, CreatedAt: at, ExpiresAt: expiresAt, encoded: encoded}, plaintext, nil
}

// matches reports whether plaintext produced this hash.
//
// Comparison is constant-time: a timing-variable check on a credential leaks
// how much of a guess was correct.
func (s Secret) matches(plaintext string) bool {
	hash, err := decodeHash(s.encoded)
	if err != nil {
		return false
	}
	// Verified with the parameters THE HASH CARRIES, not the current package
	// constants. Using the constants would mean that tuning them silently stops
	// every stored secret from verifying — the exact failure the encoded
	// parameters exist to prevent.
	got := argon2.IDKey(
		[]byte(plaintext), hash.salt,
		hash.iterations, hash.memory, hash.threads, uint32(len(hash.key)),
	)
	return subtle.ConstantTimeCompare(got, hash.key) == 1
}

func hashSecret(plaintext string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(plaintext), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// storedHash is a decoded PHC string: the salt, the digest, and the parameters
// that produced it.
type storedHash struct {
	salt       []byte
	key        []byte
	memory     uint32
	iterations uint32
	threads    uint8
}

// decodeHash reads the parameters back out of the PHC string, so a hash written
// with different settings still verifies after those settings change.
func decodeHash(encoded string) (storedHash, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return storedHash{}, fmt.Errorf("%w: malformed argon2id hash", ErrInvalid)
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return storedHash{}, fmt.Errorf("%w: unreadable argon2id version", ErrInvalid)
	}
	if version != argon2.Version {
		return storedHash{}, fmt.Errorf("%w: unsupported argon2id version %d", ErrInvalid, version)
	}
	var hash storedHash
	if _, err := fmt.Sscanf(
		parts[3], "m=%d,t=%d,p=%d", &hash.memory, &hash.iterations, &hash.threads,
	); err != nil {
		return storedHash{}, fmt.Errorf("%w: unreadable argon2id parameters", ErrInvalid)
	}
	// Zero parameters would make argon2.IDKey panic, and a hash claiming them is
	// malformed rather than cheap.
	if hash.memory == 0 || hash.iterations == 0 || hash.threads == 0 {
		return storedHash{}, fmt.Errorf("%w: argon2id parameters must be positive", ErrInvalid)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return storedHash{}, fmt.Errorf("%w: unreadable argon2id salt", ErrInvalid)
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return storedHash{}, fmt.Errorf("%w: unreadable argon2id hash", ErrInvalid)
	}
	if len(salt) == 0 || len(key) == 0 {
		return storedHash{}, fmt.Errorf("%w: argon2id salt and hash must be present", ErrInvalid)
	}
	hash.salt, hash.key = salt, key
	return hash, nil
}

func randomString(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
