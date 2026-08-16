package postgres

import "time"

// SetPrincipalCacheClockForTest replaces the cache clock in external-package
// integration tests. It is absent from production builds.
func SetPrincipalCacheClockForTest(r *Repository, now func() time.Time) {
	r.now = now
}
