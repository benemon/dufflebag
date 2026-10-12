package pluginimport

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

func TestNextPruneReadsFiveFieldCronInUTC(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 30, 0, 0, time.FixedZone("BST", 3600))
	next, err := NextPrune("0 3 * * *", now)
	if err != nil || !next.Equal(time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("daily at 03:00 UTC from 08:30 UTC = %v %v, want 13 Oct 03:00 UTC", next, err)
	}
	if next, err := NextPrune("0 */6 * * *", now); err != nil || !next.Equal(time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("every 6 hours from 08:30 UTC = %v %v", next, err)
	}
	for _, bad := range []string{"", "0 3 * *", "0 3 * * * *", "61 3 * * *", "@daily is-words", "0 25 * * *"} {
		if _, err := NextPrune(bad, now); err == nil {
			t.Fatalf("schedule %q accepted", bad)
		}
	}
	if !ValidPruneAge(30) || ValidPruneAge(31) || ValidPruneAge(0) {
		t.Fatal("age limits are the offered steps only")
	}
}

type fakePrunes struct {
	claims   []*store.ClaimedPluginPrune
	pruned   []time.Time
	recorded []struct {
		cleared int
		err     string
		next    *time.Time
	}
	pruneErr error
}

func (f *fakePrunes) ClaimPluginPrune(context.Context) (*store.ClaimedPluginPrune, error) {
	if len(f.claims) == 0 {
		return nil, nil
	}
	claim := f.claims[0]
	f.claims = f.claims[1:]
	return claim, nil
}

func (f *fakePrunes) PrunePluginImports(_ context.Context, _ store.OrganizationTenant, before time.Time) (int, error) {
	f.pruned = append(f.pruned, before)
	return 3, f.pruneErr
}

func (f *fakePrunes) RecordPluginPrune(_ context.Context, _ store.OrganizationTenant, cleared int, runErr string, nextAt *time.Time) error {
	f.recorded = append(f.recorded, struct {
		cleared int
		err     string
		next    *time.Time
	}{cleared, runErr, nextAt})
	return nil
}

func TestPrunerRunsOneClaimedOrganizationAndRecordsTheNextRun(t *testing.T) {
	now := time.Date(2026, 10, 12, 3, 0, 5, 0, time.UTC)
	tenant := store.OrganizationTenant{OrganizationID: uuid.New()}
	prunes := &fakePrunes{claims: []*store.ClaimedPluginPrune{{Tenant: tenant, AgeDays: 30, Schedule: "0 3 * * *"}}}
	pruner := NewPruner(prunes, nil, time.Minute, slog.Default())
	pruner.now = func() time.Time { return now }
	if worked, err := pruner.RunOnce(context.Background()); !worked || err != nil {
		t.Fatalf("run = %v %v", worked, err)
	}
	if len(prunes.pruned) != 1 || !prunes.pruned[0].Equal(now.AddDate(0, 0, -30)) {
		t.Fatalf("pruned before %v, want 30 days before now", prunes.pruned)
	}
	if len(prunes.recorded) != 1 || prunes.recorded[0].cleared != 3 || prunes.recorded[0].err != "" ||
		prunes.recorded[0].next == nil || !prunes.recorded[0].next.Equal(time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("recorded = %+v", prunes.recorded)
	}
	if worked, err := pruner.RunOnce(context.Background()); worked || err != nil {
		t.Fatalf("nothing due = %v %v", worked, err)
	}
	// A storage failure is recorded on the schedule and reported, and the next run still lands.
	failing := &fakePrunes{claims: []*store.ClaimedPluginPrune{{Tenant: tenant, AgeDays: 7, Schedule: "0 * * * *"}}, pruneErr: errors.New("disk full")}
	pruner = NewPruner(failing, nil, time.Minute, slog.Default())
	pruner.now = func() time.Time { return now }
	if _, err := pruner.RunOnce(context.Background()); err == nil || len(failing.recorded) != 1 || failing.recorded[0].err != "disk full" || failing.recorded[0].next == nil {
		t.Fatalf("failed run = %v recorded %+v", err, failing.recorded)
	}
}
