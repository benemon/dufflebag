//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

func TestPluginRegistryConditionalTransitions(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	repository := store.NewRepository(db)
	tenant := store.ParseOrganizationTenant(orgA)

	assertState := func(want store.PluginRegistry) {
		t.Helper()
		got, err := repository.GetPluginRegistry(ctx, tenant)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("plugin registry = %#v, want %#v", got, want)
		}
	}
	assertState(store.PluginRegistry{})
	if _, err := repository.ExposePluginRegistry(ctx, tenant); !errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		t.Fatalf("ExposePluginRegistry before enable = %v", err)
	}
	if _, err := repository.UnexposePluginRegistry(ctx, tenant); !errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		t.Fatalf("UnexposePluginRegistry before enable = %v", err)
	}
	if err := repository.DisablePluginRegistry(ctx, tenant); !errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		t.Fatalf("DisablePluginRegistry before enable = %v", err)
	}
	if got, err := repository.EnablePluginRegistry(ctx, tenant); err != nil || got != (store.PluginRegistry{Enabled: true}) {
		t.Fatalf("EnablePluginRegistry = %#v, %v", got, err)
	}
	if _, err := repository.EnablePluginRegistry(ctx, tenant); !errors.Is(err, store.ErrPluginRegistryAlreadyEnabled) {
		t.Fatalf("repeat EnablePluginRegistry = %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			_, err := repository.ExposePluginRegistry(ctx, tenant)
			errs <- err
		}()
	}
	ready.Wait()
	close(start)
	var succeeded, conflicted int
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			succeeded++
		case errors.Is(err, store.ErrPluginRegistryAlreadyExposed):
			conflicted++
		default:
			t.Fatalf("concurrent ExposePluginRegistry = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent expose: %d succeeded, %d conflicted", succeeded, conflicted)
	}
	assertState(store.PluginRegistry{Enabled: true, Exposed: true})
	if err := repository.DisablePluginRegistry(ctx, tenant); !errors.Is(err, store.ErrPluginRegistryStillExposed) {
		t.Fatalf("DisablePluginRegistry while exposed = %v", err)
	}
	if got, err := repository.UnexposePluginRegistry(ctx, tenant); err != nil || got != (store.PluginRegistry{Enabled: true}) {
		t.Fatalf("UnexposePluginRegistry = %#v, %v", got, err)
	}
	if _, err := repository.UnexposePluginRegistry(ctx, tenant); !errors.Is(err, store.ErrPluginRegistryNotExposed) {
		t.Fatalf("repeat UnexposePluginRegistry = %v", err)
	}
	if err := repository.DisablePluginRegistry(ctx, tenant); err != nil {
		t.Fatalf("DisablePluginRegistry = %v", err)
	}
	assertState(store.PluginRegistry{})
}

func TestDeleteOrganizationRefusesAPluginRegistry(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	repository := store.NewRepository(db)
	organization := store.Organization{
		ID: uuid.NewString(), Name: "plugin-registry-owner", CreatedAt: time.Now().UTC(),
	}
	if _, err := repository.CreateOrganization(ctx, organization); err != nil {
		t.Fatal(err)
	}
	tenant := store.ParseOrganizationTenant(organization.ID)
	if _, err := repository.EnablePluginRegistry(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteOrganization(ctx, organization.ID); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("DeleteOrganization with plugin registry = %v, want ErrConflict", err)
	}
	if err := repository.DisablePluginRegistry(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteOrganization(ctx, organization.ID); err != nil {
		t.Fatalf("DeleteOrganization after disable = %v", err)
	}
}
