package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/scan"
)

const (
	scannerQueuePollInterval = time.Second
	scannerRetentionInterval = time.Hour
	scannerRetentionLimit    = 100
)

var ErrScanIneligible = errors.New("build is not in the current scan set")

type ScannerServiceConfig struct {
	AdapterName string
	// Interval is the freshness threshold: a scan-set member whose findings
	// are older than this comes due for rescanning. Zero disables the cadence
	// sweep, leaving assignments and the rescan button as the only triggers.
	Interval       time.Duration
	Engine         string
	Workers        int
	PassTimeout    time.Duration
	RunRetention   time.Duration
	Clock          func() time.Time
	CircuitBackoff func() time.Duration
	Logger         *slog.Logger
}

// ScannerService drains assignment work and exposes the shared direct rescan
// path used by the later HTTP endpoint.
type ScannerService struct {
	repository *Repository
	adapter    scan.Scanner
	audit      *audit.SystemEmitter
	config     ScannerServiceConfig

	circuitMu    sync.Mutex
	circuitUntil time.Time
	circuitTrial bool

	healthMu       sync.Mutex
	lastObservedAt time.Time
	lastDetail     string
}

func NewScannerService(
	repository *Repository, adapter scan.Scanner, writer audit.Writer, config ScannerServiceConfig,
) (*ScannerService, error) {
	if repository == nil || adapter == nil || writer == nil {
		return nil, errors.New("scanner service requires repository, adapter, and audit writer")
	}
	if config.Workers <= 0 {
		return nil, errors.New("scanner workers must be positive")
	}
	if config.PassTimeout <= 0 || config.RunRetention <= 0 {
		return nil, errors.New("scanner timeouts and retention must be positive")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.CircuitBackoff == nil {
		config.CircuitBackoff = func() time.Duration {
			return time.Minute + time.Duration(rand.Int64N(int64(14*time.Minute)+1))
		}
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &ScannerService{
		repository: repository, adapter: adapter, audit: audit.NewSystemEmitter(writer), config: config,
	}, nil
}

// Run drains promptly, schedules bounded retention, and returns on cancellation.
func (s *ScannerService) Run(ctx context.Context) {
	queueTimer := time.NewTimer(0)
	defer queueTimer.Stop()
	retentionTimer := time.NewTimer(0)
	defer retentionTimer.Stop()
	sweepTimer := time.NewTimer(s.nextSweepDelay(true))
	defer sweepTimer.Stop()
	if s.config.Interval <= 0 {
		sweepTimer.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-queueTimer.C:
			if err := s.DrainOnce(ctx); err != nil && ctx.Err() == nil {
				s.config.Logger.Warn("scanner queue pass failed", "error", err)
			}
			queueTimer.Reset(scannerQueuePollInterval)
		case <-retentionTimer.C:
			if err := s.runRetention(ctx); err != nil && ctx.Err() == nil {
				s.config.Logger.Warn("scanner retention pass failed", "error", err)
			}
			retentionTimer.Reset(scannerRetentionInterval)
		case <-sweepTimer.C:
			enqueued, err := s.runSweep(ctx)
			if err != nil && ctx.Err() == nil {
				s.config.Logger.Warn("scanner freshness sweep failed", "error", err)
			}
			if enqueued > 0 {
				s.config.Logger.Info("scanner freshness sweep queued stale builds", "builds", enqueued)
			}
			sweepTimer.Reset(s.nextSweepDelay(false))
		}
	}
}

// DrainOnce drains all work currently claimable with bounded concurrency.
//
// Selection walks the tenant list ONCE per pass and hands the ordered result
// to the whole pool: forced RLS means no query can order the queue globally,
// so a walk per worker would cost workers x tenants peeks to dispatch one
// pool's worth of scans (duf-xwrw). Tenants are ordered by the age of their
// oldest entry; a tenant's backlog then drains together rather than strictly
// interleaving with other tenants, which is deliberate — cross-tenant
// fairness belongs to the cadence loop that can actually flood the queue.
func (s *ScannerService) DrainOnce(ctx context.Context) error {
	tenants, err := s.pendingTenantsByAge(ctx)
	if err != nil {
		return err
	}
	if len(tenants) == 0 {
		return nil
	}

	// A shared cursor rather than one tenant per worker: several workers must
	// be able to claim from the same tenant, or a single-tenant deployment
	// would drain at concurrency one. SKIP LOCKED makes that safe.
	cursor := &tenantCursor{tenants: tenants}

	var wg sync.WaitGroup
	errorsByWorker := make(chan error, s.config.Workers)
	for range s.config.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				tenant, index, ok := cursor.current()
				if !ok {
					return
				}
				claim, err := s.claimNextForTenant(ctx, tenant)
				if err != nil {
					errorsByWorker <- err
					return
				}
				if claim == nil {
					cursor.exhausted(index)
					continue
				}
				if err := s.processClaim(ctx, claim); err != nil {
					errorsByWorker <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errorsByWorker)
	var failures []error
	for err := range errorsByWorker {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

// ManualRescan queues a build rather than scanning it inline. The console
// button is a request for work, not a private fast path: dispatching inline
// would let one caller pressing repeatedly consume workers ahead of another
// tenant's already-due entries. The queue's primary key coalesces repeats, so
// pressing twice before a worker arrives produces one execution.
func (s *ScannerService) ManualRescan(ctx context.Context, tenant Tenant, buildID string) error {
	tx, _, err := s.repository.begin(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, eligible, err := scanInventoryTx(ctx, tx, buildID)
	if err != nil {
		return err
	}
	if !eligible {
		return ErrScanIneligible
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO pending_scans (organization_id, project_id, bucket_id, build_id, enqueued_at, reason)
		SELECT $1, $2, builds.bucket_id, builds.id, $4, 'manual_rescan'
		FROM builds
		WHERE builds.id = $3
		ON CONFLICT (organization_id, project_id, build_id) DO NOTHING
	`, tenant.OrganizationID, tenant.ProjectID, buildID, s.config.Clock()); err != nil {
		return fmt.Errorf("enqueue manual rescan: %w", err)
	}
	return tx.Commit()
}

// ScannerHealth is the remembered provider state. Nothing here probes the
// provider: this feeds a health route, and a route that reaches a third party
// when polled turns monitoring into traffic against someone else's service.
type ScannerHealth struct {
	State                 string
	Adapter               string
	Endpoint              string
	LastObservedAt        time.Time
	Detail                string
	AuditCircuitOpenUntil time.Time
}

const (
	ScannerStateDisabled    = "disabled"
	ScannerStateOK          = "ok"
	ScannerStateDegraded    = "degraded"
	ScannerStateAuditPaused = "audit_paused"
)

// Health reports the last observed provider state. The audit circuit outranks
// a provider fault: when scanning has stopped to avoid unrecorded writes, that
// is the more important thing an operator needs to know.
func (s *ScannerService) Health() ScannerHealth {
	s.circuitMu.Lock()
	circuitUntil := s.circuitUntil
	s.circuitMu.Unlock()

	s.healthMu.Lock()
	health := ScannerHealth{
		State:          ScannerStateOK,
		Adapter:        s.config.AdapterName,
		Endpoint:       s.config.Engine,
		LastObservedAt: s.lastObservedAt,
		Detail:         s.lastDetail,
	}
	if s.lastDetail != "" {
		health.State = ScannerStateDegraded
	}
	s.healthMu.Unlock()

	if s.config.Clock().Before(circuitUntil) {
		health.State = ScannerStateAuditPaused
		health.AuditCircuitOpenUntil = circuitUntil
	}
	return health
}

// observeProvider records what the last interaction saw, so Health answers
// from memory rather than by probing.
func (s *ScannerService) observeProvider(detail string) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	s.lastObservedAt = s.config.Clock()
	s.lastDetail = detail
}

type pendingScanClaim struct {
	tenant  Tenant
	buildID string
	reason  string
}

type pendingTenant struct {
	tenant     Tenant
	enqueuedAt time.Time
}

// tenantCursor hands the pool the ordered tenant list, oldest head first,
// letting any number of workers claim from the tenant at the front until it
// reports empty.
type tenantCursor struct {
	mu       sync.Mutex
	tenants  []pendingTenant
	position int
}

func (c *tenantCursor) current() (Tenant, int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.position >= len(c.tenants) {
		return Tenant{}, 0, false
	}
	return c.tenants[c.position].tenant, c.position, true
}

// exhausted advances past a tenant that answered with no claimable work.
// The index guards against several workers each advancing for the same
// empty tenant and skipping tenants that still have work.
func (c *tenantCursor) exhausted(index int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if index == c.position {
		c.position++
	}
}

// pendingTenantsByAge is the one selection pass: it peeks each tenant's
// oldest queue entry and returns the tenants that have work, oldest head
// first.
func (s *ScannerService) pendingTenantsByAge(ctx context.Context) ([]pendingTenant, error) {
	tenants, err := s.repository.scannerTenants(ctx)
	if err != nil {
		return nil, err
	}
	pending := make([]pendingTenant, 0, len(tenants))
	for _, tenant := range tenants {
		tx, _, err := s.repository.begin(ctx, tenant)
		if err != nil {
			return nil, err
		}
		var enqueuedAt time.Time
		err = tx.QueryRowContext(ctx, `SELECT enqueued_at FROM pending_scans ORDER BY enqueued_at, build_id LIMIT 1`).Scan(&enqueuedAt)
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("peek pending scan: %w", err)
		}
		pending = append(pending, pendingTenant{tenant: tenant, enqueuedAt: enqueuedAt})
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].enqueuedAt.Before(pending[j].enqueuedAt) })
	return pending, nil
}

// claimNextForTenant claims one entry within a short tenant transaction.
// SKIP LOCKED lets several workers, and several replicas, work the same
// tenant without ever claiming the same row twice. A crashed worker's claim
// becomes available after one pass timeout.
func (s *ScannerService) claimNextForTenant(ctx context.Context, tenant Tenant) (*pendingScanClaim, error) {
	tx, _, err := s.repository.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	claim := &pendingScanClaim{tenant: tenant}
	err = tx.QueryRowContext(ctx, `
		SELECT build_id, reason FROM pending_scans
		WHERE claimed_at IS NULL
		   OR claimed_at < now() - make_interval(secs => $1)
		ORDER BY enqueued_at, build_id
		FOR UPDATE SKIP LOCKED LIMIT 1`, s.config.PassTimeout.Seconds()).Scan(&claim.buildID, &claim.reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim pending scan: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pending_scans SET claimed_at = now() WHERE build_id = $1`, claim.buildID); err != nil {
		return nil, fmt.Errorf("mark pending scan claimed: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit pending scan claim: %w", err)
	}
	return claim, nil
}

func (s *ScannerService) processClaim(ctx context.Context, claim *pendingScanClaim) error {
	tx, _, err := s.repository.begin(ctx, claim.tenant)
	if err != nil {
		return errors.Join(err, s.releasePendingScan(ctx, claim))
	}
	inventory, eligible, err := scanInventoryTx(ctx, tx, claim.buildID)
	if err == nil {
		err = tx.Commit()
		if err != nil {
			err = fmt.Errorf("commit scan inventory: %w", err)
		}
	} else {
		_ = tx.Rollback()
	}
	if err != nil {
		return errors.Join(err, s.releasePendingScan(ctx, claim))
	}

	if !eligible {
		return s.deletePendingScan(ctx, claim)
	}
	terminal, dispatchErr := s.dispatch(ctx, claim.tenant, claim.buildID, inventory)
	if terminal {
		return errors.Join(dispatchErr, s.deletePendingScan(ctx, claim))
	}
	return errors.Join(dispatchErr, s.releasePendingScan(ctx, claim))
}

func (s *ScannerService) deletePendingScan(ctx context.Context, claim *pendingScanClaim) error {
	tx, _, err := s.repository.begin(ctx, claim.tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM pending_scans WHERE build_id = $1`, claim.buildID); err != nil {
		return fmt.Errorf("delete pending scan: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete pending scan: %w", err)
	}
	return nil
}

func (s *ScannerService) releasePendingScan(ctx context.Context, claim *pendingScanClaim) error {
	tx, _, err := s.repository.begin(ctx, claim.tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE pending_scans SET claimed_at = NULL WHERE build_id = $1`, claim.buildID); err != nil {
		return fmt.Errorf("release pending scan: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit release pending scan: %w", err)
	}
	return nil
}

func scanInventoryTx(ctx context.Context, tx *sql.Tx, buildID string) (scan.Inventory, bool, error) {
	var eligible bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM builds
			JOIN versions ON versions.organization_id = builds.organization_id
			 AND versions.project_id = builds.project_id AND versions.id = builds.version_id
			WHERE builds.id = $1 AND builds.status = 'done' AND versions.complete
			AND EXISTS (
				SELECT 1 FROM channels
				JOIN LATERAL (
					SELECT version_id FROM channel_assignments
					WHERE channel_id = channels.id
					ORDER BY assigned_at DESC, id DESC LIMIT 1
				) assignment ON assignment.version_id = builds.version_id
			)
		)`, buildID).Scan(&eligible); err != nil {
		return scan.Inventory{}, false, fmt.Errorf("check scan eligibility: %w", err)
	}
	if !eligible {
		return scan.Inventory{}, false, nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT packages.sbom_id, packages.name, packages.version, packages.purl
		FROM sbom_packages packages
		JOIN sboms ON sboms.organization_id = packages.organization_id
		 AND sboms.project_id = packages.project_id AND sboms.id = packages.sbom_id
		WHERE sboms.build_id = $1
		ORDER BY packages.sbom_id, packages.name, packages.version, packages.purl`, buildID)
	if err != nil {
		return scan.Inventory{}, false, fmt.Errorf("list scan inventory: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var inventory scan.Inventory
	for rows.Next() {
		var pkg scan.Package
		if err := rows.Scan(&pkg.SBOMID, &pkg.Name, &pkg.Version, &pkg.Purl); err != nil {
			return scan.Inventory{}, false, fmt.Errorf("scan inventory package: %w", err)
		}
		inventory.Packages = append(inventory.Packages, pkg)
	}
	return inventory, true, rows.Err()
}

func (s *ScannerService) dispatch(ctx context.Context, tenant Tenant, buildID string, inventory scan.Inventory) (bool, error) {
	if !s.beginCircuitAttempt() {
		return false, errors.New("scanner audit circuit is open")
	}
	event := audit.SystemEvent{
		Operation: identity.AuditOperationScanExecute, TargetType: "build", TargetID: buildID,
		Scope: identity.AuditScopeProject, OrganizationID: tenant.OrganizationID.String(), ProjectID: tenant.ProjectID.String(),
	}

	tx, err := BeginTenant(ctx, s.repository.db, tenant.OrganizationID.String(), tenant.ProjectID.String(), tenant.BucketID)
	if err != nil {
		s.finishCircuitRequest(false)
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		tenant.OrganizationID.String()+"|"+tenant.ProjectID.String()+"|scanner"); err != nil {
		s.finishCircuitRequest(false)
		return false, fmt.Errorf("acquire tenant scanner lock: %w", err)
	}
	sequence, err := allocateScanRunSequenceTx(ctx, tx, tenant)
	if err != nil {
		s.finishCircuitRequest(false)
		return false, err
	}
	correlationID, err := s.audit.Request(event)
	if err != nil {
		s.finishCircuitRequest(false)
		return false, fmt.Errorf("scanner request audit: %w", err)
	}
	s.finishCircuitRequest(true)
	if err := tx.Commit(); err != nil {
		_ = s.audit.Response(correlationID, event, identity.AuditOutcomeFailure, "sequence_commit_failed")
		return false, fmt.Errorf("commit scan sequence: %w", err)
	}

	passCtx, cancel := context.WithTimeout(ctx, s.config.PassTimeout)
	result, scanErr := s.adapter.Scan(passCtx, inventory)
	if scanErr != nil {
		s.observeProvider(scanErr.Error())
	} else {
		s.observeProvider("")
	}
	cancel()
	now := s.config.Clock().UTC()
	observedAt := result.Attribution.ObservedAt
	if observedAt.IsZero() {
		observedAt = now
	}
	adapterName := result.Attribution.Adapter
	if adapterName == "" {
		adapterName = s.config.AdapterName
	}
	engine := result.Attribution.Engine
	if engine == "" {
		engine = s.config.Engine
	}
	run := ScanRun{
		ID: registry.NewID(now).String(), BuildID: buildID, RunSequence: sequence,
		Status: ScanRunSucceeded, Adapter: adapterName, Engine: engine,
		DatabaseRevision: result.Attribution.DatabaseRevision, ObservedAt: observedAt,
		TranscriptDigest: result.Transcript.Digest(), Coverage: result.Coverage, CreatedAt: now,
	}
	findings := result.Findings
	if scanErr != nil {
		run.Status, run.Error, findings = ScanRunFailed, scanErr.Error(), nil
	}
	recordErr := s.repository.RecordScanRun(ctx, tenant, run, findings, result.Transcript.Encode())
	terminal := recordErr == nil
	outcome, reason := identity.AuditOutcomeSuccess, ""
	if scanErr != nil || recordErr != nil {
		outcome = identity.AuditOutcomeFailure
		if scanErr != nil {
			reason = "adapter_failed"
		} else {
			reason = "record_failed"
		}
	}
	responseErr := s.audit.Response(correlationID, event, outcome, reason)
	if responseErr != nil {
		s.tripCircuit()
	}
	return terminal, errors.Join(scanErr, recordErr, responseErr)
}

func allocateScanRunSequenceTx(ctx context.Context, tx *sql.Tx, tenant Tenant) (int64, error) {
	var sequence int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO scan_run_counters (organization_id, project_id, next_sequence)
		VALUES ($1, $2, 2)
		ON CONFLICT (organization_id, project_id) DO UPDATE
			SET next_sequence = scan_run_counters.next_sequence + 1
		RETURNING next_sequence - 1`, tenant.OrganizationID, tenant.ProjectID).Scan(&sequence); err != nil {
		return 0, fmt.Errorf("allocate scan run sequence: %w", err)
	}
	return sequence, nil
}

func (s *ScannerService) beginCircuitAttempt() bool {
	s.circuitMu.Lock()
	defer s.circuitMu.Unlock()
	if s.circuitUntil.IsZero() {
		return true
	}
	if s.config.Clock().Before(s.circuitUntil) || s.circuitTrial {
		return false
	}
	s.circuitTrial = true
	return true
}

func (s *ScannerService) finishCircuitRequest(success bool) {
	s.circuitMu.Lock()
	defer s.circuitMu.Unlock()
	if !s.circuitTrial {
		return
	}
	s.circuitTrial = false
	if success {
		s.circuitUntil = time.Time{}
	}
}

func (s *ScannerService) tripCircuit() {
	s.circuitMu.Lock()
	defer s.circuitMu.Unlock()
	s.circuitTrial = false
	s.circuitUntil = s.config.Clock().Add(s.config.CircuitBackoff())
}

func (s *ScannerService) runRetention(ctx context.Context) error {
	tenants, err := s.repository.scannerTenants(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, tenant := range tenants {
		if !s.beginCircuitAttempt() {
			break
		}
		event := audit.SystemEvent{
			Operation: identity.AuditOperationScanRetention, TargetType: "project", TargetID: tenant.ProjectID.String(),
			Scope: identity.AuditScopeProject, OrganizationID: tenant.OrganizationID.String(), ProjectID: tenant.ProjectID.String(),
		}
		correlationID, requestErr := s.audit.Request(event)
		if requestErr != nil {
			s.finishCircuitRequest(false)
			failures = append(failures, requestErr)
			continue
		}
		s.finishCircuitRequest(true)
		now := s.config.Clock().UTC()
		_, expireErr := s.repository.ExpireScanTranscripts(ctx, tenant, now, scannerRetentionLimit)
		_, purgeErr := s.repository.PurgeSupersededScanRuns(ctx, tenant, now.Add(-s.config.RunRetention), scannerRetentionLimit)
		mutationErr := errors.Join(expireErr, purgeErr)
		outcome, reason := identity.AuditOutcomeSuccess, ""
		if mutationErr != nil {
			outcome, reason = identity.AuditOutcomeFailure, "retention_failed"
		}
		responseErr := s.audit.Response(correlationID, event, outcome, reason)
		if responseErr != nil {
			s.tripCircuit()
		}
		failures = append(failures, mutationErr, responseErr)
	}
	return errors.Join(failures...)
}

func (r *Repository) scannerTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT organization_id::text, id::text FROM projects ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var tenants []Tenant
	for rows.Next() {
		var organizationID, projectID string
		if err := rows.Scan(&organizationID, &projectID); err != nil {
			return nil, err
		}
		tenants = append(tenants, ParseTenant(organizationID, projectID))
	}
	return tenants, rows.Err()
}
