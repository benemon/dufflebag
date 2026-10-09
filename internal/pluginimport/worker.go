package pluginimport

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// Job states, as the platform API reports them.
const (
	StateRunning            = "running"
	StateSucceeded          = "succeeded"
	StatePartiallySucceeded = "partially_succeeded"
	StateFailed             = "failed"
)

// A job whose worker has not reported for this long is claimed again. Every
// finished version renews the claim, so only one version's downloads need fit.
const staleAfter = 30 * time.Minute

// Jobs is where the worker finds and records import jobs.
type Jobs interface {
	ClaimPluginImport(context.Context, time.Duration) (*store.ClaimedPluginImport, error)
	RecordPluginImport(context.Context, store.OrganizationTenant, uuid.UUID, string, any) error
}

// Worker runs queued imports one at a time.
type Worker struct {
	jobs     Jobs
	importer *Importer
	interval time.Duration
	logger   *slog.Logger
}

// NewWorker polls jobs every interval while idle.
func NewWorker(jobs Jobs, importer *Importer, interval time.Duration, logger *slog.Logger) *Worker {
	return &Worker{jobs: jobs, importer: importer, interval: interval, logger: logger}
}

// NewHTTPClient is the client imports and catalogue reads share. It has no
// overall timeout, because a zip download may legitimately take minutes.
func NewHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: transport}
}

// Run works jobs until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	for {
		worked, err := w.RunOnce(ctx)
		if err != nil {
			w.logger.Error("plugin import claim failed", "error", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.interval):
		}
	}
}

// RunOnce works at most one job, reporting whether there was one.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	job, err := w.jobs.ClaimPluginImport(ctx, staleAfter)
	if err != nil || job == nil {
		return false, err
	}
	outcomes := []VersionOutcome{}
	record := func(state string) {
		if err := w.jobs.RecordPluginImport(ctx, job.Tenant, job.ID, state, outcomes); err != nil {
			w.logger.Error("plugin import record failed", "import_id", job.ID, "error", err)
		}
	}
	for _, version := range job.Request.Versions {
		var outcome VersionOutcome
		switch job.Request.SourceKind {
		case "releases-hashicorp":
			outcome = w.importer.ImportVersion(ctx, job.Tenant, job.Request.Product, version, job.Request.Platforms)
		case "github":
			outcome = w.importer.ImportGitHubVersion(ctx, job.Tenant, job.Request.Product, version, job.Request.Platforms)
		default:
			outcome = VersionOutcome{Version: version, Outcome: OutcomeFailed, Error: "unknown import source " + job.Request.SourceKind}
		}
		outcomes = append(outcomes, outcome)
		record(StateRunning)
	}
	record(finalState(outcomes))
	return true, nil
}

func finalState(outcomes []VersionOutcome) string {
	failed, partial := 0, false
	for _, outcome := range outcomes {
		if outcome.Outcome == OutcomeFailed {
			failed++
		}
		for _, platform := range outcome.Platforms {
			if platform.Outcome == OutcomeFailed {
				partial = true
			}
		}
	}
	switch {
	case failed == len(outcomes):
		return StateFailed
	case failed > 0 || partial:
		return StatePartiallySucceeded
	}
	return StateSucceeded
}
