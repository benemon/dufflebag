package pluginimport

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// PruneAges are the age limits the console offers, in days.
var PruneAges = []int{1, 7, 14, 30, 60, 90, 180, 365}

// Schedules are five-field cron expressions, read in UTC.
var scheduleParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ValidPruneAge reports whether days is one of the offered limits.
func ValidPruneAge(days int) bool {
	return slices.Contains(PruneAges, days)
}

// NextPrune is when a schedule next fires after now, in UTC.
func NextPrune(schedule string, now time.Time) (time.Time, error) {
	parsed, err := scheduleParser.Parse(schedule)
	if err != nil {
		return time.Time{}, fmt.Errorf("schedule %q: %w", schedule, err)
	}
	return parsed.Next(now.UTC()), nil
}

// Pruning clears an organization's finished jobs; the API's Prune now and
// the scheduled pruner share it.
type Pruning interface {
	PrunePluginImports(ctx context.Context, tenant store.OrganizationTenant, before time.Time) (int, error)
}

// Prunes is where the pruner finds due organizations and records runs.
type Prunes interface {
	Pruning
	ClaimPluginPrune(ctx context.Context) (*store.ClaimedPluginPrune, error)
	RecordPluginPrune(ctx context.Context, tenant store.OrganizationTenant, cleared int, runErr string, nextAt *time.Time) error
}

// Pruner clears finished jobs on each organization's schedule. One
// organization's run happens on one replica: the claim stamps it run.
type Pruner struct {
	prunes   Prunes
	audit    *audit.SystemEmitter
	interval time.Duration
	logger   *slog.Logger
	now      func() time.Time
}

func NewPruner(prunes Prunes, emitter *audit.SystemEmitter, interval time.Duration, logger *slog.Logger) *Pruner {
	return &Pruner{prunes: prunes, audit: emitter, interval: interval, logger: logger, now: time.Now}
}

func (p *Pruner) Run(ctx context.Context) {
	for {
		worked, err := p.RunOnce(ctx)
		if err != nil {
			p.logger.Error("plugin prune failed", "error", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.interval):
		}
	}
}

// RunOnce prunes at most one organization, reporting whether one was due.
func (p *Pruner) RunOnce(ctx context.Context) (bool, error) {
	claimed, err := p.prunes.ClaimPluginPrune(ctx)
	if err != nil || claimed == nil {
		return false, err
	}
	next, scheduleErr := NextPrune(claimed.Schedule, p.now())
	var nextAt *time.Time
	if scheduleErr == nil {
		nextAt = &next
	}
	cleared, pruneErr := PruneOnce(ctx, p.prunes, p.audit, claimed.Tenant, claimed.AgeDays, p.now())
	runErr := ""
	if pruneErr != nil {
		runErr = pruneErr.Error()
	} else if scheduleErr != nil {
		runErr = scheduleErr.Error()
	}
	if err := p.prunes.RecordPluginPrune(ctx, claimed.Tenant, cleared, runErr, nextAt); err != nil {
		return true, err
	}
	return true, pruneErr
}

// PruneOnce clears the organization's finished jobs older than ageDays and
// writes the audit pair for the run; the API's Prune now shares it.
func PruneOnce(ctx context.Context, prunes Pruning, emitter *audit.SystemEmitter, tenant store.OrganizationTenant, ageDays int, now time.Time) (int, error) {
	event := audit.SystemEvent{
		Operation: identity.AuditOperationPluginImportPrune, TargetType: "plugin_import_collection", TargetID: tenant.OrganizationID.String(),
		Scope: identity.AuditScopeOrganization, OrganizationID: tenant.OrganizationID.String(),
		Detail: fmt.Sprintf("finished jobs older than %d days", ageDays),
	}
	var correlationID string
	if emitter != nil {
		id, err := emitter.Request(event)
		if err != nil {
			return 0, fmt.Errorf("audit prune request: %w", err)
		}
		correlationID = id
	}
	cleared, err := prunes.PrunePluginImports(ctx, tenant, now.AddDate(0, 0, -ageDays))
	if emitter != nil {
		outcome, reason := identity.AuditOutcomeSuccess, fmt.Sprintf("cleared %d", cleared)
		if err != nil {
			outcome, reason = identity.AuditOutcomeFailure, "storage_failed"
		}
		if auditErr := emitter.Response(correlationID, event, outcome, reason); auditErr != nil && err == nil {
			err = fmt.Errorf("audit prune response: %w", auditErr)
		}
	}
	return cleared, err
}
