package pluginimport

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/benemon/dufflebag/internal/domain/plugin"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// GitHub's unauthenticated allowance is 60 calls an hour per egress address,
// shared with imports (ADR-0027 A4); checks take at most half of it.
const githubCheckGap = 2 * time.Minute

// UpdateChecks is where the checker finds due plugins and records results.
type UpdateChecks interface {
	ClaimPluginUpdateCheck(ctx context.Context, interval time.Duration, includeGitHub bool) (*store.ClaimedPluginUpdateCheck, error)
	RecordPluginUpdateCheck(ctx context.Context, tenant store.OrganizationTenant, id uuid.UUID, latest, tag, checkErr string) error
}

// Checker asks each opted-in plugin's source for its newest stable release
// (ADR-0027 A6). It never imports.
type Checker struct {
	checks     UpdateChecks
	upstream   *Upstream
	github     *GitHub
	interval   time.Duration
	logger     *slog.Logger
	now        func() time.Time
	githubNext time.Time
}

// NewChecker checks each opted-in plugin once per interval.
func NewChecker(checks UpdateChecks, upstream *Upstream, github *GitHub, interval time.Duration, logger *slog.Logger) *Checker {
	return &Checker{checks: checks, upstream: upstream, github: github, interval: interval, logger: logger, now: time.Now}
}

// Run checks due plugins until ctx ends, looking for more once a minute.
func (c *Checker) Run(ctx context.Context) {
	for {
		worked, err := c.RunOnce(ctx)
		if err != nil {
			c.logger.Error("plugin update check claim failed", "error", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// RunOnce checks at most one due plugin, reporting whether there was one.
func (c *Checker) RunOnce(ctx context.Context) (bool, error) {
	claimed, err := c.checks.ClaimPluginUpdateCheck(ctx, c.interval, !c.now().Before(c.githubNext))
	if err != nil || claimed == nil {
		return false, err
	}
	latest, tag, err := c.newest(ctx, claimed.Source)
	if claimed.Source.Kind == "github" {
		c.githubNext = c.now().Add(githubCheckGap)
		if errors.Is(err, ErrGitHubRateLimited) {
			c.githubNext = c.now().Add(time.Hour)
		}
	}
	checkErr := ""
	if err != nil {
		checkErr = err.Error()
	}
	if err := c.checks.RecordPluginUpdateCheck(ctx, claimed.Tenant, claimed.ID, latest, tag, checkErr); err != nil {
		c.logger.Error("plugin update check record failed", "plugin", claimed.Name, "error", err)
	}
	return true, nil
}

// newest is the source's newest stable release. GitHub's latest release
// excludes prereleases by definition; HashiCorp's first page of versions,
// newest first, is searched for one.
func (c *Checker) newest(ctx context.Context, source store.PluginSource) (string, string, error) {
	switch source.Kind {
	case "releases-hashicorp":
		versions, _, err := c.upstream.Versions(ctx, source.Repository, "")
		if err != nil {
			return "", "", err
		}
		var stable []string
		for _, version := range versions {
			if !version.Prerelease {
				stable = append(stable, version.Version)
			}
		}
		if len(stable) == 0 {
			return "", "", errors.New("no stable release on the newest page of releases")
		}
		newest := plugin.Newest(stable)
		return newest, newest, nil
	case "github":
		release, err := c.github.release(ctx, source.Repository, "latest")
		if err != nil {
			return "", "", err
		}
		return release.Version, release.Tag, nil
	}
	return "", "", errors.New("an uploaded plugin has no upstream")
}
