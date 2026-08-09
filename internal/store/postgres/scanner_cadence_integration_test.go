//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/scan"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

func newCadenceService(
	t *testing.T, repository *store.Repository, adapter scan.Scanner,
	interval time.Duration, clock func() time.Time,
) *store.ScannerService {
	t.Helper()
	service, err := store.NewScannerService(repository, adapter, &scannerAuditWriter{}, store.ScannerServiceConfig{
		AdapterName: "stub", Engine: "stub://scanner", Workers: 2,
		PassTimeout: 5 * time.Second, RunRetention: 90 * 24 * time.Hour,
		Interval: interval, Clock: clock,
		CircuitBackoff: func() time.Duration { return time.Minute },
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// queuedReason returns the queue reason for one build, or "" when it is not
// queued. Assertions are PER BUILD: sibling subtests seed builds that have
// never been scanned and are therefore legitimately due, so a global count
// would measure the fixtures rather than the rule.
func queuedReason(t *testing.T, db *sql.DB, org, project, buildID string) string {
	t.Helper()
	tx, err := store.BeginTenant(context.Background(), db, org, project)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var reason string
	err = tx.QueryRow(`SELECT reason FROM pending_scans WHERE build_id = $1`, buildID).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return reason
}

// TestScannerCadence proves the freshness rule with FAKE TIME. Nothing here
// sleeps.
func TestScannerCadence(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	repository := store.NewRepositoryWithObjectStore(db, objects)
	tenant := store.ParseTenant(orgA, projectA)
	ctx := context.Background()

	const interval = 24 * time.Hour
	base := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	now := base
	clock := func() time.Time { return now }

	t.Run("a scanned build ages into being due", func(t *testing.T) {
		now = base
		// queued=true because a fixture-inserted channel assignment writes SQL
		// directly and so never passes through the enqueue path.
		seed := seedScannerBuild(t, db, "cadence-age", true, true)
		service := newCadenceService(t, repository, &scannerStub{clock: clock}, interval, clock)
		if err := service.DrainOnce(ctx); err != nil {
			t.Fatal(err)
		}
		state, err := repository.GetBuildScanState(ctx, tenant, seed.buildID)
		if err != nil || state == nil || state.CurrentFindingsRunID == "" {
			t.Fatalf("first scan did not land: %#v %v", state, err)
		}
		firstRun := state.CurrentFindingsRunID

		if _, err := service.SweepDueBuilds(ctx); err != nil {
			t.Fatal(err)
		}
		if reason := queuedReason(t, db, orgA, projectA, seed.buildID); reason != "" {
			t.Fatalf("a just-scanned build was queued as %q", reason)
		}

		// Past interval + the maximum jitter it is due however the jitter
		// fell: that boundary is the hard freshness cap.
		now = base.Add(interval + time.Hour + time.Minute)
		if _, err := service.SweepDueBuilds(ctx); err != nil {
			t.Fatal(err)
		}
		if reason := queuedReason(t, db, orgA, projectA, seed.buildID); reason != "freshness" {
			t.Fatalf("stale build queue reason = %q, want freshness", reason)
		}

		if err := service.DrainOnce(ctx); err != nil {
			t.Fatal(err)
		}
		state, err = repository.GetBuildScanState(ctx, tenant, seed.buildID)
		if err != nil {
			t.Fatal(err)
		}
		if state.CurrentFindingsRunID == firstRun {
			t.Fatal("the rescan did not advance current")
		}
	})

	t.Run("due time is stable across sweeps", func(t *testing.T) {
		now = base
		seed := seedScannerBuild(t, db, "cadence-stable", true, true)
		service := newCadenceService(t, repository, &scannerStub{clock: clock}, interval, clock)
		if err := service.DrainOnce(ctx); err != nil {
			t.Fatal(err)
		}
		// Inside the earliest possible due time. A jitter re-rolled per pass
		// would make the build flap between due and not due.
		now = base.Add(interval - 2*time.Hour)
		for pass := range 5 {
			if _, err := service.SweepDueBuilds(ctx); err != nil {
				t.Fatal(err)
			}
			if reason := queuedReason(t, db, orgA, projectA, seed.buildID); reason != "" {
				t.Fatalf("pass %d queued a build that is not yet due (%q)", pass, reason)
			}
		}
	})

	t.Run("an already queued build keeps its original reason", func(t *testing.T) {
		now = base
		seed := seedScannerBuild(t, db, "cadence-dup", true, true)
		service := newCadenceService(t, repository, &scannerStub{clock: clock}, interval, clock)
		if _, err := service.SweepDueBuilds(ctx); err != nil {
			t.Fatal(err)
		}
		// It is unscanned and so due, but already queued: the sweep must not
		// re-queue it or overwrite why it was queued.
		if reason := queuedReason(t, db, orgA, projectA, seed.buildID); reason != "channel_assignment" {
			t.Fatalf("queue reason = %q, want the original channel_assignment", reason)
		}
	})
}

// TestScannerCadenceFairness gets its own database because it asserts on the
// GLOBAL enqueue order, which sibling fixtures would otherwise perturb.
func TestScannerCadenceFairness(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	repository := store.NewRepositoryWithObjectStore(db, objects)
	ctx := context.Background()

	// Two never-scanned builds in tenant A and one in tenant B: all due.
	seedScannerBuild(t, db, "rr-a1", true, false)
	seedScannerBuild(t, db, "rr-a2", true, false)
	bSeed := seedScannerBuildForTenant(t, db, orgB, projectB, "rr-b1", true, false)

	// An ADVANCING clock, so each enqueue lands at a distinct time. With a
	// frozen clock every row would share a timestamp and the ordering
	// assertion below would hold vacuously.
	tick := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	advancing := func() time.Time {
		tick = tick.Add(time.Second)
		return tick
	}
	service := newCadenceService(t, repository, &scannerStub{clock: advancing}, 24*time.Hour, advancing)
	enqueued, err := service.SweepDueBuilds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if enqueued != 3 {
		t.Fatalf("swept %d builds, want the 3 due across both tenants", enqueued)
	}

	// Tenant B holds one build against tenant A's two. Round-robin must give
	// it the second slot; draining each tenant fully in turn would leave it
	// third, so a tenant with a single stale build would wait behind someone
	// else's entire estate.
	bAt := enqueuedAt(t, db, orgB, projectB, bSeed.buildID)
	ahead := countEnqueuedBefore(t, db, orgA, projectA, bAt)
	if ahead != 1 {
		t.Fatalf("%d of tenant A's builds were queued before tenant B's, want exactly 1", ahead)
	}
}

func enqueuedAt(t *testing.T, db *sql.DB, org, project, buildID string) time.Time {
	t.Helper()
	tx, err := store.BeginTenant(context.Background(), db, org, project)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var at time.Time
	if err := tx.QueryRow(
		`SELECT enqueued_at FROM pending_scans WHERE build_id = $1`, buildID).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func countEnqueuedBefore(t *testing.T, db *sql.DB, org, project string, before time.Time) int {
	t.Helper()
	tx, err := store.BeginTenant(context.Background(), db, org, project)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var count int
	if err := tx.QueryRow(
		`SELECT count(*) FROM pending_scans WHERE enqueued_at < $1`, before).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
