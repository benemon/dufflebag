package registry

import (
	"errors"
	"sort"
	"testing"
	"time"
)

func TestNewIDIsAParsableULID(t *testing.T) {
	id := NewID(epoch)
	if len(id.String()) != 26 {
		t.Fatalf("ULID must be 26 characters, got %d: %q", len(id.String()), id)
	}
	if _, err := ParseID(id.String()); err != nil {
		t.Fatalf("freshly minted id does not parse: %v", err)
	}
}

// Ids minted in the same millisecond must still sort in creation order —
// callers may rely on ULID lexicographic ordering being creation ordering.
func TestNewIDIsMonotonicWithinAMillisecond(t *testing.T) {
	const n = 500
	ids := make([]string, n)
	for i := range ids {
		ids[i] = NewID(epoch).String()
	}

	if !sort.StringsAreSorted(ids) {
		for i := 1; i < len(ids); i++ {
			if ids[i-1] >= ids[i] {
				t.Fatalf("ids not monotonic at %d: %q >= %q", i, ids[i-1], ids[i])
			}
		}
	}

	seen := make(map[string]struct{}, n)
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestNewIDSortsByTimestamp(t *testing.T) {
	earlier := NewID(epoch).String()
	later := NewID(epoch.Add(time.Second)).String()
	if earlier >= later {
		t.Fatalf("later id %q does not sort after earlier %q", later, earlier)
	}
}

func TestParseIDRejectsNonULIDs(t *testing.T) {
	for _, s := range []string{
		"",
		"not-a-ulid",
		"550e8400-e29b-41d4-a716-446655440000", // a UUID
		"01ARZ3NDEKTSV4RRFFQ69G5FA",            // 25 chars
	} {
		if _, err := ParseID(s); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ParseID(%q) should fail with ErrInvalid, got %v", s, err)
		}
	}
}
