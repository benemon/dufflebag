package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"time"
)

// The cadence loop is a PRODUCER into the same queue channel assignments and
// the rescan button write to. It adds no dispatch path and no execution
// semantics of its own: it decides which builds have gone stale and enqueues
// them, and the drainer treats them like any other work.
//
// Without it the registry answers "everything that lands and everything
// promoted is scanned". With it, it answers "the March image shows its June
// findings" — advisories are published long after a build stops changing, so
// a build that never changes still needs re-examining.

const (
	// scannerFreshnessJitter spreads due times so a fleet of builds scanned
	// together does not come due together and arrive as one burst.
	scannerFreshnessJitter = time.Hour
	// scannerStartupJitterMax staggers replicas restarted at the same moment,
	// e.g. by a rolling deploy.
	scannerStartupJitterMax = 5 * time.Minute
	// scannerSweepLockKey serialises the sweep across replicas. Duplicate
	// sweeps would be harmless — the queue's primary key coalesces them — but
	// they would each pay the full enumeration, so one sweeper is enough.
	scannerSweepLockKey = 1646664801
)

// dueBuild is one stale scan-set member awaiting enqueue.
type dueBuild struct {
	tenant  Tenant
	buildID string
}

// SweepDueBuilds enqueues every scan-set member whose findings have aged past
// the freshness threshold. Returns how many builds it enqueued.
//
// Fairness is round-robin ACROSS TENANTS: a sweep can enqueue one tenant's
// entire estate, and a queue ordered purely by enqueue time would then put
// every other tenant behind it. Taking one build per tenant per round means a
// tenant with a single stale build waits for one round, not for someone
// else's thousand. Event traffic cannot create that problem, which is why
// fairness lives here rather than with the drainer.
func (s *ScannerService) SweepDueBuilds(ctx context.Context) (int, error) {
	tenants, err := s.repository.scannerTenants(ctx)
	if err != nil {
		return 0, err
	}

	queues := make([][]dueBuild, 0, len(tenants))
	for _, tenant := range tenants {
		due, err := s.dueBuildsForTenant(ctx, tenant)
		if err != nil {
			return 0, err
		}
		if len(due) > 0 {
			queues = append(queues, due)
		}
	}

	enqueued := 0
	for len(queues) > 0 {
		remaining := queues[:0]
		for _, queue := range queues {
			if ctx.Err() != nil {
				return enqueued, ctx.Err()
			}
			if err := s.enqueueDue(ctx, queue[0]); err != nil {
				return enqueued, err
			}
			enqueued++
			if rest := queue[1:]; len(rest) > 0 {
				remaining = append(remaining, rest)
			}
		}
		queues = remaining
	}
	return enqueued, nil
}

// dueBuildsForTenant lists this tenant's stale scan-set members.
//
// Due time is the last SUCCESSFUL observation plus the interval; a build that
// has never been scanned successfully is due immediately. The jitter is
// derived per build rather than drawn fresh each pass, so a build does not
// re-roll its due time on every sweep and drift indefinitely; and the
// interval plus the maximum jitter is a hard cap, past which a build is due
// however the jitter fell.
func (s *ScannerService) dueBuildsForTenant(ctx context.Context, tenant Tenant) ([]dueBuild, error) {
	tx, _, err := s.repository.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := s.config.Clock()
	rows, err := tx.QueryContext(ctx, `
		SELECT builds.id, runs.observed_at
		FROM builds
		JOIN versions ON versions.organization_id = builds.organization_id
		 AND versions.project_id = builds.project_id AND versions.id = builds.version_id
		LEFT JOIN build_scan_state state ON state.organization_id = builds.organization_id
		 AND state.project_id = builds.project_id AND state.build_id = builds.id
		LEFT JOIN scan_runs runs ON runs.organization_id = state.organization_id
		 AND runs.project_id = state.project_id AND runs.id = state.current_findings_run_id
		WHERE builds.status = 'done' AND versions.complete
		  AND EXISTS (
			SELECT 1 FROM channels
			JOIN LATERAL (
				SELECT version_id FROM channel_assignments
				WHERE channel_id = channels.id
				ORDER BY assigned_at DESC, id DESC LIMIT 1
			) assignment ON assignment.version_id = builds.version_id
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM pending_scans
			WHERE pending_scans.organization_id = builds.organization_id
			  AND pending_scans.project_id = builds.project_id
			  AND pending_scans.build_id = builds.id
		  )
		ORDER BY runs.observed_at NULLS FIRST, builds.id`)
	if err != nil {
		return nil, fmt.Errorf("list due builds: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var due []dueBuild
	for rows.Next() {
		var buildID string
		var observedAt sql.NullTime
		if err := rows.Scan(&buildID, &observedAt); err != nil {
			return nil, fmt.Errorf("due build row: %w", err)
		}
		if observedAt.Valid && now.Before(s.dueAt(tenant, buildID, observedAt.Time)) {
			continue
		}
		due = append(due, dueBuild{tenant: tenant, buildID: buildID})
	}
	return due, rows.Err()
}

// dueAt is deterministic per build: the same build always draws the same
// offset, so its due time is stable across sweeps and across replicas.
func (s *ScannerService) dueAt(tenant Tenant, buildID string, lastObserved time.Time) time.Time {
	seed := fnv64(tenant.OrganizationID.String() + "|" + tenant.ProjectID.String() + "|" + buildID)
	// Map the hash onto [-jitter, +jitter].
	span := int64(2*scannerFreshnessJitter) + 1
	offset := time.Duration(int64(seed%uint64(span))) - scannerFreshnessJitter
	return lastObserved.Add(s.config.Interval + offset)
}

func fnv64(value string) uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	hash := uint64(offset)
	for i := 0; i < len(value); i++ {
		hash ^= uint64(value[i])
		hash *= prime
	}
	return hash
}

func (s *ScannerService) enqueueDue(ctx context.Context, build dueBuild) error {
	tx, _, err := s.repository.begin(ctx, build.tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO pending_scans (organization_id, project_id, bucket_id, build_id, enqueued_at, reason)
		SELECT $1, $2, builds.bucket_id, builds.id, $4, 'freshness'
		FROM builds
		WHERE builds.id = $3
		ON CONFLICT (organization_id, project_id, build_id) DO NOTHING
	`, build.tenant.OrganizationID, build.tenant.ProjectID, build.buildID, s.config.Clock()); err != nil {
		return fmt.Errorf("enqueue due build: %w", err)
	}
	return tx.Commit()
}

// runSweep takes the cluster sweeper lock for the duration of one pass. A
// replica that cannot take it simply skips: another replica is already
// enumerating, and the queue would coalesce the duplicates anyway.
func (s *ScannerService) runSweep(ctx context.Context) (int, error) {
	conn, err := s.repository.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close() }()

	var acquired bool
	if err := conn.QueryRowContext(ctx,
		`SELECT pg_try_advisory_lock($1)`, scannerSweepLockKey).Scan(&acquired); err != nil {
		return 0, fmt.Errorf("take scanner sweep lock: %w", err)
	}
	if !acquired {
		return 0, nil
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock($1)`, scannerSweepLockKey)
	}()
	return s.SweepDueBuilds(ctx)
}

// nextSweepDelay staggers replicas at startup and keeps them from converging
// afterwards.
func (s *ScannerService) nextSweepDelay(first bool) time.Duration {
	if first {
		return time.Duration(rand.Int64N(int64(scannerStartupJitterMax) + 1))
	}
	span := int64(2*scannerFreshnessJitter) + 1
	return s.config.Interval + time.Duration(rand.Int64N(span)) - scannerFreshnessJitter
}
