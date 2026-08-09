package registry

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// ID identifies a registry resource.
//
// ULID, not UUID: both published HCP specs document resource ids as
// "Universally Unique Lexicographically Sortable Identifier (ULID)". Ids are
// wire-visible and referenced everywhere, so the choice is not ours to make.
type ID string

var (
	entropyMu sync.Mutex
	entropy   = ulid.Monotonic(rand.Reader, 0)
)

// NewID mints a ULID for the given instant. Ids minted within the same
// millisecond still sort in creation order, since ULID sort order is
// lexicographic and callers may rely on it.
func NewID(at time.Time) ID {
	ts := ulid.Timestamp(at)

	entropyMu.Lock()
	id, err := ulid.New(ts, entropy)
	entropyMu.Unlock()

	if err == nil {
		return ID(id.String())
	}

	// Monotonic entropy exhausted within this millisecond. Fall back to plain
	// randomness: ordering within a single millisecond is no longer guaranteed,
	// which is a far better outcome than panicking in the write path.
	return ID(ulid.MustNew(ts, rand.Reader).String())
}

// ParseID validates an id received from a client or read from storage.
func ParseID(s string) (ID, error) {
	if _, err := ulid.ParseStrict(s); err != nil {
		return "", fmt.Errorf("%w: %q is not a ULID", ErrInvalid, s)
	}
	return ID(s), nil
}

func (id ID) String() string { return string(id) }
