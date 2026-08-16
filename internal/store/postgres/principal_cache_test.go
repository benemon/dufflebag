package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/google/uuid"
)

func TestPrincipalCacheDoesNotCacheLoadErrors(t *testing.T) {
	repository := NewRepository(nil)
	principal, err := identity.RestorePrincipal(
		"principal-retry", "retry", "client-retry",
		identity.Scope{OrganizationID: uuid.New()}, identity.RoleReader,
		time.Now().UTC(), nil,
	)
	if err != nil {
		t.Fatalf("RestorePrincipal: %v", err)
	}

	loads := 0
	load := func(context.Context, string) (*identity.Principal, error) {
		loads++
		if loads == 1 {
			return nil, identity.ErrNotFound
		}
		return principal, nil
	}
	if _, err := repository.cachedPrincipalByID(context.Background(), principal.ID, load); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("first load = %v, want ErrNotFound", err)
	}
	got, err := repository.cachedPrincipalByID(context.Background(), principal.ID, load)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if got != principal || loads != 2 {
		t.Fatalf("second load = %p after %d loader calls, want %p after 2", got, loads, principal)
	}
}

func TestPrincipalCacheConcurrentGetsAndEvictions(t *testing.T) {
	repository := NewRepository(nil)
	principal, err := identity.RestorePrincipal(
		"principal-concurrent", "concurrent", "client-concurrent",
		identity.Scope{OrganizationID: uuid.New()}, identity.RoleReader,
		time.Now().UTC(), nil,
	)
	if err != nil {
		t.Fatalf("RestorePrincipal: %v", err)
	}

	load := func(context.Context, string) (*identity.Principal, error) {
		return principal, nil
	}
	start := make(chan struct{})
	errCh := make(chan error, 32)
	var wg sync.WaitGroup
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 500 {
				got, err := repository.cachedPrincipalByID(context.Background(), principal.ID, load)
				if err != nil {
					errCh <- err
					return
				}
				if got != principal {
					errCh <- errors.New("cache returned a different principal")
					return
				}
			}
		}()
	}
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 500 {
				repository.evictPrincipal(principal.ID)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}
