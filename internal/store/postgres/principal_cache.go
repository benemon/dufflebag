package postgres

import (
	"context"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
)

const principalCacheTTL = 60 * time.Second

type principalCacheEntry struct {
	principal *identity.Principal
	storedAt  time.Time
}

func (r *Repository) cachedPrincipalByID(
	ctx context.Context,
	id string,
	load func(context.Context, string) (*identity.Principal, error),
) (*identity.Principal, error) {
	r.principalCacheMu.Lock()
	defer r.principalCacheMu.Unlock()

	now := r.now()
	if entry, ok := r.principalCache[id]; ok && now.Sub(entry.storedAt) < principalCacheTTL {
		return entry.principal, nil
	}

	principal, err := load(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.principalCache == nil {
		r.principalCache = make(map[string]principalCacheEntry)
	}
	r.principalCache[id] = principalCacheEntry{principal: principal, storedAt: r.now()}
	return principal, nil
}

func (r *Repository) evictPrincipal(id string) {
	r.principalCacheMu.Lock()
	defer r.principalCacheMu.Unlock()
	delete(r.principalCache, id)
}
