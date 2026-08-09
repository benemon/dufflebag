package postgres

import (
	"errors"

	"github.com/benemon/dufflebag/internal/store/objectstore"
)

var (
	ErrObjectStorageNotConfigured = errors.New("object storage is not configured")
	ErrObjectStorageUnavailable   = errors.New("object storage is unavailable")
)

func (r *Repository) objectStore() (*objectstore.Store, error) {
	if r.objects == nil {
		return nil, ErrObjectStorageNotConfigured
	}
	return r.objects, nil
}

// ObjectStorageState is what the health probe reports: whether a store is
// configured at all, and if it is, what the last operation to touch it saw.
// Not configured is a deployment that never asked for one, which is a working
// registry for anyone not producing SBOMs — not a fault.
func (r *Repository) ObjectStorageState() string {
	switch {
	case r.objects == nil:
		return "unconfigured"
	case r.objects.Reachable():
		return "ok"
	default:
		return "unreachable"
	}
}
