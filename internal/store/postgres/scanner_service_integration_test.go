//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/compat/hcpauth"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/scan"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type scannerStub struct {
	mu        sync.Mutex
	calls     int
	active    int
	highWater int
	started   chan int
	wait      func(int) <-chan struct{}
	err       error
	clock     func() time.Time
}

// observedAt lets fake-time tests keep attribution coherent: the service
// prefers the ADAPTER's observation, so a stub stamping real time would
// silently reintroduce wall-clock into a frozen-clock test.
func (s *scannerStub) observedAt() time.Time {
	if s.clock != nil {
		return s.clock().UTC()
	}
	return time.Now().UTC()
}

func (s *scannerStub) Scan(ctx context.Context, inventory scan.Inventory) (scan.Result, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.active++
	if s.active > s.highWater {
		s.highWater = s.active
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	if s.started != nil {
		s.started <- call
	}
	if s.wait != nil {
		if release := s.wait(call); release != nil {
			select {
			case <-release:
			case <-ctx.Done():
				return scan.Result{}, ctx.Err()
			}
		}
	}
	result := scan.Result{
		Attribution: scan.Attribution{
			Adapter: "stub", Engine: "stub://scanner", DatabaseRevision: "fixture",
			ObservedAt: s.observedAt(),
		},
		Coverage:   scan.Coverage{Submitted: len(inventory.Packages)},
		Transcript: scan.Transcript{Records: [][]byte{[]byte(fmt.Sprintf("stub-call-%d", call))}},
	}
	if len(inventory.Packages) > 0 && s.err == nil {
		result.Findings = []scan.Finding{{
			Package: inventory.Packages[0], ID: "OSV-STUB-1", Summary: "fixture finding",
			Severity: scan.SeverityHigh,
		}}
	}
	return result, s.err
}

func (s *scannerStub) Probe(context.Context) (scan.Health, error) {
	return scan.Health{OK: true}, nil
}

func (s *scannerStub) snapshot() (calls, highWater int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.highWater
}

type scannerAuditWriter struct {
	mu      sync.Mutex
	writes  int
	failAt  map[int]error
	records [][]byte
}

func (w *scannerAuditWriter) Write(record []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes++
	if err := w.failAt[w.writes]; err != nil {
		return err
	}
	w.records = append(w.records, append([]byte(nil), record...))
	return nil
}

func (w *scannerAuditWriter) setFailure(write int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failAt == nil {
		w.failAt = make(map[int]error)
	}
	w.failAt[write] = err
}

func (w *scannerAuditWriter) decoded(t *testing.T) []map[string]any {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	result := make([]map[string]any, 0, len(w.records))
	for _, encoded := range w.records {
		var record map[string]any
		if err := json.Unmarshal(encoded, &record); err != nil {
			t.Fatal(err)
		}
		result = append(result, record)
	}
	return result
}

type scannerSeed struct {
	bucketID, bucketName       string
	versionID, fingerprint     string
	buildID, sbomID, channelID string
}

// seedScannerBuild seeds into the default tenant; the cadence fairness test
// needs a second tenant, so the work lives in the parameterised form.
func seedScannerBuild(t *testing.T, db *sql.DB, suffix string, assigned, queued bool) scannerSeed {
	t.Helper()
	return seedScannerBuildForTenant(t, db, orgA, projectA, suffix, assigned, queued)
}

func seedScannerBuildForTenant(t *testing.T, db *sql.DB, org, project, suffix string, assigned, queued bool) scannerSeed {
	return seedScannerBuildInBucket(t, db, org, project, suffix, assigned, queued, "")
}

func seedScannerBuildInBucket(t *testing.T, db *sql.DB, org, project, suffix string, assigned, queued bool, bucketID string) scannerSeed {
	t.Helper()
	base := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC).Add(time.Duration(len(suffix)) * time.Second)
	seed := scannerSeed{
		bucketID: registry.NewID(base).String(), bucketName: "scanner-" + suffix,
		versionID: registry.NewID(base.Add(time.Millisecond)).String(), fingerprint: "scanner-fp-" + suffix,
		buildID:   registry.NewID(base.Add(2 * time.Millisecond)).String(),
		sbomID:    registry.NewID(base.Add(3 * time.Millisecond)).String(),
		channelID: registry.NewID(base.Add(4 * time.Millisecond)).String(),
	}
	createBucket := bucketID == ""
	sequence := 1
	if !createBucket {
		seed.bucketID = bucketID
		sequence = 2
	}
	tx, err := store.BeginTenant(context.Background(), db, org, project, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	type statement struct {
		query string
		args  []any
	}
	statements := []statement{}
	if createBucket {
		statements = append(statements, statement{`INSERT INTO buckets (organization_id, project_id, id, name, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$5)`, []any{org, project, seed.bucketID, seed.bucketName, base}})
	}
	statements = append(statements,
		statement{`INSERT INTO versions (organization_id, project_id, id, bucket_id, fingerprint, template_type, complete, sequence, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,'HCL2',true,$6,$7,$7)`, []any{org, project, seed.versionID, seed.bucketID, seed.fingerprint, sequence, base}},
		statement{`INSERT INTO builds (organization_id, project_id, id, bucket_id, version_id, component_type, status, platform, metadata_seen, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,'docker','done','docker',true,$6,$6)`, []any{org, project, seed.buildID, seed.bucketID, seed.versionID, base}},
		statement{`INSERT INTO sboms (organization_id, project_id, id, bucket_id, build_id, name, format, object_key, created_at)
			VALUES ($1,$2,$3,$4,$5,'sbom','SPDX','fixture-key-'||$3,$6)`, []any{org, project, seed.sbomID, seed.bucketID, seed.buildID, base}},
		statement{`INSERT INTO sbom_packages (organization_id, project_id, bucket_id, sbom_id, name, version, purl)
			VALUES ($1,$2,$3,$4,'busybox','1.36.1-r0','pkg:apk/alpine/busybox@1.36.1-r0')`, []any{org, project, seed.bucketID, seed.sbomID}},
	)
	if assigned {
		statements = append(statements,
			struct {
				query string
				args  []any
			}{`INSERT INTO channels (organization_id, project_id, id, bucket_id, name, created_at, updated_at)
				VALUES ($1,$2,$3,$4,'latest',$5,$5)`, []any{org, project, seed.channelID, seed.bucketID, base}},
			struct {
				query string
				args  []any
			}{`INSERT INTO channel_assignments (organization_id, project_id, id, bucket_id, channel_id, version_id, author_id, assigned_at)
				VALUES ($1,$2,$3,$4,$5,$6,'fixture',$7)`, []any{org, project, registry.NewID(base.Add(5 * time.Millisecond)).String(), seed.bucketID, seed.channelID, seed.versionID, base}},
		)
	}
	if queued {
		statements = append(statements, struct {
			query string
			args  []any
		}{`INSERT INTO pending_scans (organization_id, project_id, bucket_id, build_id, enqueued_at, reason)
			VALUES ($1,$2,$3,$4,$5,'channel_assignment')`, []any{org, project, seed.bucketID, seed.buildID, base}})
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(context.Background(), statement.query, statement.args...); err != nil {
			t.Fatalf("seed %s: %v", suffix, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return seed
}

func newScannerService(t *testing.T, repository *store.Repository, adapter scan.Scanner, writer audit.Writer, max int, clock func() time.Time) *store.ScannerService {
	t.Helper()
	service, err := store.NewScannerService(repository, adapter, writer, store.ScannerServiceConfig{
		AdapterName: "stub", Engine: "stub://scanner", Workers: max,
		PassTimeout: 5 * time.Second, RunRetention: 90 * 24 * time.Hour, Clock: clock,
		CircuitBackoff: func() time.Duration { return time.Minute },
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func pendingCount(t *testing.T, db *sql.DB, buildID string) int {
	t.Helper()
	tx, err := store.BeginTenant(context.Background(), db, orgA, projectA, "")
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM pending_scans WHERE build_id = $1`, buildID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// clearPendingScans empties the queue between subtests that deliberately
// leave work behind.
func clearPendingScans(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := store.BeginTenant(context.Background(), db, orgA, projectA, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM pending_scans`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestScannerHubIntegration(t *testing.T) {
	db, appURL, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	repository := store.NewRepositoryWithObjectStore(db, objects)
	tenant := store.ParseTenant(orgA, projectA)
	ctx := context.Background()

	t.Run("assignment enqueues and drainer records findings", func(t *testing.T) {
		seed := seedScannerBuild(t, db, "assignment", false, false)
		_, err := repository.CreateChannel(ctx, tenant, store.Channel{
			ID: registry.NewID(time.Now()), BucketName: seed.bucketName, Name: "production", CreatedAt: time.Now(),
		}, seed.fingerprint, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		if got := pendingCount(t, db, seed.buildID); got != 1 {
			t.Fatalf("pending rows = %d, want 1", got)
		}
		adapter, writer := &scannerStub{}, &scannerAuditWriter{}
		service := newScannerService(t, repository, adapter, writer, 2, time.Now)
		if err := service.DrainOnce(ctx); err != nil {
			t.Fatal(err)
		}
		state, err := repository.GetBuildScanState(ctx, tenant, seed.buildID)
		if err != nil || state == nil || state.CurrentFindingsRunID == "" {
			t.Fatalf("scan state = %#v, %v", state, err)
		}
		findings, err := repository.ListScanFindings(ctx, tenant, state.CurrentFindingsRunID)
		if err != nil || len(findings) != 1 || findings[0].ID != "OSV-STUB-1" {
			t.Fatalf("findings = %#v, %v", findings, err)
		}
		if pendingCount(t, db, seed.buildID) != 0 {
			t.Fatal("terminal queue row survived")
		}
		records := writer.decoded(t)
		if len(records) != 2 || records[0]["kind"] != "request" || records[1]["kind"] != "response" ||
			records[0]["correlation_id"] != records[1]["correlation_id"] ||
			records[0]["principal_id"] != "system:scanner" || records[1]["identity_kind"] != "system" ||
			records[0]["organization_id"] != orgA || records[1]["project_id"] != projectA ||
			records[0]["operation"] != "scan.execute" || records[1]["operation"] != "scan.execute" {
			t.Fatalf("audit pair = %#v", records)
		}
	})

	t.Run("repeated assignments coalesce to one scan", func(t *testing.T) {
		seed := seedScannerBuild(t, db, "coalesce", false, false)
		for i := range 2 {
			_, err := repository.CreateChannel(ctx, tenant, store.Channel{
				ID: registry.NewID(time.Now()), BucketName: seed.bucketName,
				Name: fmt.Sprintf("channel-%d", i), CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
			}, seed.fingerprint, "fixture")
			if err != nil {
				t.Fatal(err)
			}
		}
		if got := pendingCount(t, db, seed.buildID); got != 1 {
			t.Fatalf("pending rows = %d, want 1", got)
		}
		adapter := &scannerStub{}
		if err := newScannerService(t, repository, adapter, &scannerAuditWriter{}, 2, time.Now).DrainOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if calls, _ := adapter.snapshot(); calls != 1 {
			t.Fatalf("adapter calls = %d, want 1", calls)
		}
	})

	t.Run("rolled back assignment leaves no queue row", func(t *testing.T) {
		seed := seedScannerBuild(t, db, "rollback", false, false)
		parsed, err := url.Parse(appURL)
		if err != nil {
			t.Fatal(err)
		}
		parsed.User = url.UserPassword("postgres", "postgres")
		admin, err := sql.Open("pgx", parsed.String())
		if err != nil {
			t.Fatal(err)
		}
		defer admin.Close()
		for _, statement := range []string{
			`CREATE FUNCTION fail_rollback_channel() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.name = 'rollback-channel' THEN RAISE EXCEPTION 'forced rollback'; END IF; RETURN NEW; END $$`,
			`CREATE TRIGGER fail_rollback_channel_update BEFORE UPDATE ON channels FOR EACH ROW EXECUTE FUNCTION fail_rollback_channel()`,
		} {
			if _, err := admin.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() {
			_, _ = admin.Exec(`DROP TRIGGER IF EXISTS fail_rollback_channel_update ON channels`)
			_, _ = admin.Exec(`DROP FUNCTION IF EXISTS fail_rollback_channel()`)
		})
		_, err = repository.CreateChannel(ctx, tenant, store.Channel{
			ID: registry.NewID(time.Now()), BucketName: seed.bucketName, Name: "rollback-channel", CreatedAt: time.Now(),
		}, seed.fingerprint, "fixture")
		if err == nil {
			t.Fatal("forced rollback unexpectedly committed")
		}
		if got := pendingCount(t, db, seed.buildID); got != 0 {
			t.Fatalf("pending rows after rollback = %d", got)
		}
	})

	t.Run("SBOM upload alone does not enqueue", func(t *testing.T) {
		seed := seedScannerBuild(t, db, "sbom-only", false, false)
		tx, err := store.BeginTenant(ctx, db, tenant.OrganizationID.String(), tenant.ProjectID.String(), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE builds SET status = 'running' WHERE id = $1`, seed.buildID); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		data := compressIntegrationSBOM(t, `{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","packages":[]}`)
		_, err = repository.UploadSbom(ctx, tenant, seed.bucketName, seed.fingerprint, seed.buildID, store.Sbom{
			ID: registry.NewID(time.Now()), Name: "uploaded", Format: "SPDX", CompressedData: data, CreatedAt: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := pendingCount(t, db, seed.buildID); got != 0 {
			t.Fatalf("SBOM upload enqueued %d rows", got)
		}
	})

	t.Run("channel unassign does not enqueue", func(t *testing.T) {
		seed := seedScannerBuild(t, db, "unassign", false, false)
		channel, err := repository.CreateChannel(ctx, tenant, store.Channel{
			ID: registry.NewID(time.Now()), BucketName: seed.bucketName, Name: "staging", CreatedAt: time.Now(),
		}, seed.fingerprint, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		tx, _ := store.BeginTenant(ctx, db, orgA, projectA, "")
		_, _ = tx.Exec(`DELETE FROM pending_scans WHERE build_id = $1`, seed.buildID)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.UpdateChannel(ctx, tenant, seed.bucketName, channel.Name, false, false, true, "", "fixture", time.Now()); err != nil {
			t.Fatal(err)
		}
		if got := pendingCount(t, db, seed.buildID); got != 0 {
			t.Fatalf("unassign enqueued %d rows", got)
		}
	})

	// The button queues rather than scanning inline: dispatching directly
	// would let one caller pressing repeatedly consume workers ahead of
	// another tenant's already-due entries.
	t.Run("manual rescan queues and the drainer scans it", func(t *testing.T) {
		seed := seedScannerBuild(t, db, "manual", true, false)
		adapter := &scannerStub{}
		service := newScannerService(t, repository, adapter, &scannerAuditWriter{}, 1, time.Now)
		if err := service.ManualRescan(ctx, tenant, seed.buildID); err != nil {
			t.Fatal(err)
		}
		if calls, _ := adapter.snapshot(); calls != 0 {
			t.Fatalf("adapter calls before draining = %d, want 0: the button must not scan inline", calls)
		}
		// Pressing again before a worker arrives coalesces on the queue's key.
		if err := service.ManualRescan(ctx, tenant, seed.buildID); err != nil {
			t.Fatal(err)
		}
		if err := service.DrainOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if calls, _ := adapter.snapshot(); calls != 1 {
			t.Fatalf("adapter calls after draining = %d, want exactly 1", calls)
		}
	})

	t.Run("manual rescan refuses a build outside the scan set", func(t *testing.T) {
		seed := seedScannerBuild(t, db, "manual-ineligible", false, false)
		service := newScannerService(t, repository, &scannerStub{}, &scannerAuditWriter{}, 1, time.Now)
		if err := service.ManualRescan(ctx, tenant, seed.buildID); !errors.Is(err, store.ErrScanIneligible) {
			t.Fatalf("err = %v, want ErrScanIneligible", err)
		}
	})

	t.Run("failed adapter run is terminal and leaves the queue", func(t *testing.T) {
		seed := seedScannerBuild(t, db, "terminal-failure", true, true)
		adapter := &scannerStub{err: errors.New("provider unavailable")}
		err := newScannerService(t, repository, adapter, &scannerAuditWriter{}, 1, time.Now).DrainOnce(ctx)
		if err == nil {
			t.Fatal("failed adapter pass was not reported")
		}
		if pendingCount(t, db, seed.buildID) != 0 {
			t.Fatal("terminal failed run remained queued")
		}
		state, err := repository.GetBuildScanState(ctx, tenant, seed.buildID)
		if err != nil || state == nil || state.CurrentFindingsRunID != "" || state.LatestAttemptRunID == "" {
			t.Fatalf("failed state = %#v, %v", state, err)
		}
		run, err := repository.GetScanRun(ctx, tenant, state.LatestAttemptRunID)
		if err != nil || run.Status != store.ScanRunFailed {
			t.Fatalf("failed run = %#v, %v", run, err)
		}
	})

	t.Run("claimed scan does not block bucket deletion", func(t *testing.T) {
		seed := seedScannerBuild(t, db, "delete-during-dispatch", true, true)
		// Keep this assertion about the pending_scans row lock only. The
		// repository deletes SBOM objects after committing the bucket cascade,
		// and object-store latency is unrelated to the database hang at issue.
		tx, err := store.BeginTenant(ctx, db, orgA, projectA, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sboms WHERE build_id = $1`, seed.buildID); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseScan := func() { releaseOnce.Do(func() { close(release) }) }
		defer releaseScan()

		adapter := &scannerStub{
			started: make(chan int, 1),
			wait:    func(int) <-chan struct{} { return release },
		}
		service := newScannerService(t, repository, adapter, &scannerAuditWriter{}, 1, time.Now)
		drainDone := make(chan error, 1)
		go func() { drainDone <- service.DrainOnce(ctx) }()
		<-adapter.started

		deleteDone := make(chan error, 1)
		go func() { deleteDone <- repository.DeleteBucket(ctx, tenant, seed.bucketName) }()
		select {
		case err := <-deleteDone:
			if err != nil {
				t.Fatalf("DeleteBucket while scanner dispatch was blocked: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("DeleteBucket blocked on the claimed pending_scans row")
		}

		releaseScan()
		select {
		case err := <-drainDone:
			if err != nil {
				t.Fatalf("scanner terminal path after build deletion: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("scanner did not finish after the deleted build's adapter was released")
		}
		if got := pendingCount(t, db, seed.buildID); got != 0 {
			t.Fatalf("pending rows after bucket deletion = %d, want 0", got)
		}
	})

	t.Run("two concurrent claims never dispatch the same row", func(t *testing.T) {
		seed := seedScannerBuild(t, db, "double-claim", true, true)
		release := make(chan struct{})
		adapter := &scannerStub{started: make(chan int, 2), wait: func(int) <-chan struct{} { return release }}
		first := newScannerService(t, repository, adapter, &scannerAuditWriter{}, 1, time.Now)
		second := newScannerService(t, repository, adapter, &scannerAuditWriter{}, 1, time.Now)
		firstDone := make(chan error, 1)
		go func() { firstDone <- first.DrainOnce(ctx) }()
		<-adapter.started
		if err := second.DrainOnce(ctx); err != nil {
			t.Fatal(err)
		}
		close(release)
		if err := <-firstDone; err != nil {
			t.Fatal(err)
		}
		if calls, _ := adapter.snapshot(); calls != 1 {
			t.Fatalf("adapter calls = %d for %s", calls, seed.buildID)
		}
	})

	t.Run("stale claim is reclaimable after pass timeout", func(t *testing.T) {
		seed := seedScannerBuild(t, db, "stale-claim", true, true)
		tx, err := store.BeginTenant(ctx, db, orgA, projectA, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE pending_scans SET claimed_at = now() - interval '6 seconds' WHERE build_id = $1`, seed.buildID); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}

		adapter := &scannerStub{}
		if err := newScannerService(t, repository, adapter, &scannerAuditWriter{}, 1, time.Now).DrainOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if calls, _ := adapter.snapshot(); calls != 1 {
			t.Fatalf("adapter calls for stale claim = %d, want 1", calls)
		}
		if got := pendingCount(t, db, seed.buildID); got != 0 {
			t.Fatalf("pending rows after stale reclaim = %d, want 0", got)
		}
	})

	t.Run("configured concurrency is a hard cap", func(t *testing.T) {
		for i := range 5 {
			seedScannerBuild(t, db, fmt.Sprintf("cap-%d", i), true, true)
		}
		release := make(chan struct{})
		adapter := &scannerStub{started: make(chan int, 5), wait: func(int) <-chan struct{} { return release }}
		service := newScannerService(t, repository, adapter, &scannerAuditWriter{}, 2, time.Now)
		done := make(chan error, 1)
		go func() { done <- service.DrainOnce(ctx) }()
		<-adapter.started
		<-adapter.started
		select {
		case <-adapter.started:
			close(release)
			<-done
			t.Fatal("a third scan started above the configured cap")
		case <-time.After(250 * time.Millisecond):
		}
		if _, high := adapter.snapshot(); high != 2 {
			t.Fatalf("high-water mark = %d, want 2", high)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if calls, high := adapter.snapshot(); calls != 5 || high > 2 {
			t.Fatalf("calls/high-water = %d/%d, want 5/<=2", calls, high)
		}
	})

	// The overlapping-dispatch leg this subtest used to drive through an
	// inline ManualRescan is now unreachable BY CONSTRUCTION: a claim holds
	// the queue row locked and a re-enqueue coalesces on the primary key, so
	// one build cannot be scanned twice concurrently. The run_sequence
	// ordering guard itself is exercised at the store level, where it lives —
	// see TestScanStore/"round trip with ordering guard and first seen",
	// which records an older sequence completing last and asserts current
	// does not move.
	t.Run("failed run becomes latest attempt without erasing current", func(t *testing.T) {
		seed := seedScannerBuild(t, db, "ordering", true, false)
		good := newScannerService(t, repository, &scannerStub{}, &scannerAuditWriter{}, 1, time.Now)
		if err := good.ManualRescan(ctx, tenant, seed.buildID); err != nil {
			t.Fatal(err)
		}
		if err := good.DrainOnce(ctx); err != nil {
			t.Fatal(err)
		}
		state, err := repository.GetBuildScanState(ctx, tenant, seed.buildID)
		if err != nil {
			t.Fatal(err)
		}
		if state.CurrentFindingsRunID == "" {
			t.Fatal("a successful scan did not advance current")
		}

		failing := newScannerService(t, repository,
			&scannerStub{err: errors.New("provider unreachable")}, &scannerAuditWriter{}, 1, time.Now)
		if err := failing.ManualRescan(ctx, tenant, seed.buildID); err != nil {
			t.Fatal(err)
		}
		if err := failing.DrainOnce(ctx); err == nil {
			t.Fatal("a failing adapter drained without error")
		}
		afterFailure, err := repository.GetBuildScanState(ctx, tenant, seed.buildID)
		if err != nil {
			t.Fatal(err)
		}
		if afterFailure.CurrentFindingsRunID != state.CurrentFindingsRunID {
			t.Fatalf("a failed run erased current findings: before %#v after %#v", state, afterFailure)
		}
		if afterFailure.LatestAttemptRunID == state.LatestAttemptRunID {
			t.Fatal("a failed run did not become the latest attempt")
		}
	})

	t.Run("moving latest preserves old state and drops stale work", func(t *testing.T) {
		old := seedScannerBuild(t, db, "latest-old", true, true)
		adapter := &scannerStub{}
		service := newScannerService(t, repository, adapter, &scannerAuditWriter{}, 2, time.Now)
		if err := service.DrainOnce(ctx); err != nil {
			t.Fatal(err)
		}
		oldState, err := repository.GetBuildScanState(ctx, tenant, old.buildID)
		if err != nil {
			t.Fatal(err)
		}
		newBuild := seedScannerBuildInBucket(t, db, orgA, projectA, "latest-new", false, false, old.bucketID)
		tx, _ := store.BeginTenant(ctx, db, orgA, projectA, "")
		movedAt := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
		if _, err := tx.Exec(`INSERT INTO channel_assignments (organization_id, project_id, id, bucket_id, channel_id, version_id, author_id, assigned_at)
			VALUES ($1,$2,$3,$4,$5,$6,'fixture',$7)`, orgA, projectA, registry.NewID(movedAt).String(), old.bucketID, old.channelID, newBuild.versionID, movedAt); err != nil {
			t.Fatal(err)
		}
		for _, buildID := range []string{old.buildID, newBuild.buildID} {
			if _, err := tx.Exec(`INSERT INTO pending_scans (organization_id, project_id, bucket_id, build_id, enqueued_at, reason)
				SELECT $1,$2,builds.bucket_id,builds.id,$4,'channel_assignment' FROM builds WHERE builds.id = $3
				ON CONFLICT DO NOTHING`, orgA, projectA, buildID, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		before, _ := adapter.snapshot()
		if err := service.DrainOnce(ctx); err != nil {
			t.Fatal(err)
		}
		after, _ := adapter.snapshot()
		if after-before != 1 {
			t.Fatalf("adapter calls for stale+current queue = %d, want 1", after-before)
		}
		preserved, err := repository.GetBuildScanState(ctx, tenant, old.buildID)
		if err != nil || preserved.CurrentFindingsRunID != oldState.CurrentFindingsRunID {
			t.Fatalf("old state changed: before %#v after %#v, %v", oldState, preserved, err)
		}
		findings, err := repository.ListScanFindings(ctx, tenant, preserved.CurrentFindingsRunID)
		if err != nil || len(findings) != 1 {
			t.Fatalf("old figures lost: %#v, %v", findings, err)
		}
	})

	t.Run("audit request failure prevents egress and mutation", func(t *testing.T) {
		seed := seedScannerBuild(t, db, "audit-down", true, false)
		adapter := &scannerStub{}
		writer := &scannerAuditWriter{failAt: map[int]error{1: errors.New("down")}}
		service := newScannerService(t, repository, adapter, writer, 1, time.Now)
		if err := service.ManualRescan(ctx, tenant, seed.buildID); err != nil {
			t.Fatal(err)
		}
		if err := service.DrainOnce(ctx); err == nil {
			t.Fatal("audit failure was ignored")
		}
		if calls, _ := adapter.snapshot(); calls != 0 {
			t.Fatalf("adapter calls = %d", calls)
		}
		state, err := repository.GetBuildScanState(ctx, tenant, seed.buildID)
		if err != nil || state != nil {
			t.Fatalf("state = %#v, %v; want no mutation", state, err)
		}
		tx, _ := store.BeginTenant(ctx, db, orgA, projectA, "")
		var runs int
		if err := tx.QueryRow(`SELECT count(*) FROM scan_runs WHERE build_id = $1`, seed.buildID).Scan(&runs); err != nil {
			t.Fatal(err)
		}
		_ = tx.Rollback()
		if runs != 0 {
			t.Fatalf("scan_runs rows = %d, want zero", runs)
		}
		// The queue row correctly SURVIVES a pre-request audit failure: the
		// work is still owed once auditing recovers. That is production
		// behaviour, so the cleanup here is test hygiene only — siblings
		// share this database and a stray row would be drained by whichever
		// subtest ran next.
		clearPendingScans(t, db)
	})

	t.Run("response failure retains result and opens circuit until recovery", func(t *testing.T) {
		seed := seedScannerBuild(t, db, "audit-response", true, false)
		now := time.Now().UTC()
		clock := func() time.Time { return now }
		adapter := &scannerStub{}
		writer := &scannerAuditWriter{}
		writer.setFailure(2, errors.New("response down"))
		service := newScannerService(t, repository, adapter, writer, 1, clock)
		drain := func() error {
			if err := service.ManualRescan(ctx, tenant, seed.buildID); err != nil {
				return err
			}
			return service.DrainOnce(ctx)
		}
		if err := drain(); err == nil {
			t.Fatal("response audit failure was not reported")
		}
		if state, err := repository.GetBuildScanState(ctx, tenant, seed.buildID); err != nil || state == nil {
			t.Fatalf("committed result missing: %#v, %v", state, err)
		}
		if err := drain(); err == nil {
			t.Fatal("open circuit allowed a mutation")
		}
		if calls, _ := adapter.snapshot(); calls != 1 {
			t.Fatalf("adapter calls while open = %d", calls)
		}
		now = now.Add(time.Minute + time.Second)
		if err := drain(); err != nil {
			t.Fatalf("circuit did not recover: %v", err)
		}
		if calls, _ := adapter.snapshot(); calls != 2 {
			t.Fatalf("adapter calls after recovery = %d", calls)
		}
		clearPendingScans(t, db)
	})

	t.Run("disabled audit broker permits scanning", func(t *testing.T) {
		seed := seedScannerBuild(t, db, "audit-disabled", true, false)
		broker, err := audit.NewBroker(slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		adapter := &scannerStub{}
		service := newScannerService(t, repository, adapter, broker, 1, time.Now)
		if err := service.ManualRescan(ctx, tenant, seed.buildID); err != nil {
			t.Fatal(err)
		}
		if err := service.DrainOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if calls, _ := adapter.snapshot(); calls != 1 {
			t.Fatalf("adapter calls = %d", calls)
		}
	})

	t.Run("system scanner cannot authenticate over HTTP", func(t *testing.T) {
		principal, err := identity.NewPrincipal(
			registry.NewID(time.Now()).String(), "reserved scanner", identity.SystemScannerPrincipalID,
			identity.Scope{OrganizationID: tenant.OrganizationID, ProjectID: tenant.ProjectID},
			identity.RoleReader, time.Now(),
		)
		if err != nil {
			t.Fatal(err)
		}
		plaintext, err := principal.IssueSecret(registry.NewID(time.Now()).String(), nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.CreatePrincipal(ctx, principal); !errors.Is(err, identity.ErrInvalid) {
			t.Fatalf("reserved principal creation = %v, want ErrInvalid", err)
		}
		// Even a hand-inserted row cannot make the audit actor selectable.
		if _, err := db.Exec(`INSERT INTO principals
			(id, name, client_id, organization_id, project_id, role, created_at)
			VALUES ($1,'injected scanner',$2,$3,$4,'reader',$5)`,
			principal.ID, identity.SystemScannerPrincipalID,
			tenant.OrganizationID, tenant.ProjectID, time.Now()); err != nil {
			t.Fatal(err)
		}
		secret := principal.Secrets()[0]
		if _, err := db.Exec(`INSERT INTO principal_secrets
			(id, principal_id, encoded_hash, created_at)
			VALUES ($1,$2,$3,$4)`, secret.ID, principal.ID, secret.Encoded(), secret.CreatedAt); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = db.Exec(`DELETE FROM principals WHERE client_id = $1`, identity.SystemScannerPrincipalID)
		})
		if _, err := repository.GetPrincipalByClientID(ctx, identity.SystemScannerPrincipalID); !errors.Is(err, identity.ErrNotFound) {
			t.Fatalf("reserved actor lookup = %v, want ErrNotFound", err)
		}

		issuer, err := identity.NewBasicAuthIssuer("https://dufflebag.test", []byte("0123456789abcdef0123456789abcdef"), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		handler := hcpauth.NewHandler(repository, issuer, slog.New(slog.NewTextHandler(io.Discard, nil)))
		request := httptest.NewRequest(http.MethodPost, hcpauth.TokenPath, strings.NewReader("grant_type=client_credentials"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.SetBasicAuth(identity.SystemScannerPrincipalID, plaintext)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", response.Code)
		}
	})
}
