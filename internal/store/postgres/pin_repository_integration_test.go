//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

func insertPinBucket(t *testing.T, repository *store.Repository, name string) {
	t.Helper()
	at := time.Date(2026, 8, 9, 8, 0, 0, 0, time.UTC)
	if _, err := repository.CreateBucket(context.Background(), store.ParseTenant(orgA, projectA), store.Bucket{
		ID: registry.NewID(at), Name: name, Labels: map[string]string{}, CreatedAt: at,
	}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
}

func TestPinCascadeOnBucketDelete(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	tenant := store.ParseTenant(orgA, projectA)
	insertPinBucket(t, repository, "images")

	if _, err := repository.SetPin(context.Background(), tenant, "images", "principal-a", time.Now().UTC()); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	if err := repository.DeleteBucket(context.Background(), tenant, "images"); err != nil {
		t.Fatalf("delete pinned bucket: %v", err)
	}
	pins, err := repository.ListPins(context.Background(), tenant)
	if err != nil {
		t.Fatalf("list pins: %v", err)
	}
	if len(pins) != 0 {
		t.Fatalf("pins after bucket delete = %#v, want none", pins)
	}
}

func TestRePinPreservesOriginalAttribution(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	tenant := store.ParseTenant(orgA, projectA)
	insertPinBucket(t, repository, "images")
	original := time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC)

	if _, err := repository.SetPin(context.Background(), tenant, "images", "principal-a", original); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	repinned, err := repository.SetPin(
		context.Background(), tenant, "images", "principal-b", original.Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("re-pin: %v", err)
	}
	if !repinned.PinnedAt.Equal(original) || repinned.PinnedBy != "principal-a" {
		t.Fatalf("re-pin = %#v, want original attribution", repinned)
	}
}

func TestSetPinUnknownBucketMapsNotFound(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)

	_, err := repository.SetPin(
		context.Background(), store.ParseTenant(orgA, projectA), "missing", "principal-a", time.Now().UTC(),
	)
	if !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("set missing bucket error = %v, want registry.ErrNotFound", err)
	}
}
