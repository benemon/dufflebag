package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/benemon/dufflebag/internal/domain/plugin"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/benemon/dufflebag/internal/webhook"
	"github.com/google/uuid"
)

// ErrPluginUploadHasNoUpdates refuses update checks on an uploaded plugin,
// which has no upstream to ask (ADR-0027 A6).
var ErrPluginUploadHasNoUpdates = errors.New("an uploaded plugin has no upstream to check for updates")

// ClaimedPluginUpdateCheck is a plugin whose update check a checker now owns.
type ClaimedPluginUpdateCheck struct {
	Tenant OrganizationTenant
	ID     uuid.UUID
	Name   string
	Source PluginSource
}

// SetPluginUpdateCheck turns a plugin's update check on or off.
func (r *Repository) SetPluginUpdateCheck(ctx context.Context, tenant OrganizationTenant, name string, enabled bool) error {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	plugin, err := q.GetPlugin(ctx, postgresdb.GetPluginParams{OrganizationID: tenant.OrganizationID, Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: plugin %s", registry.ErrNotFound, name)
	}
	if err != nil {
		return fmt.Errorf("get plugin: %w", err)
	}
	if enabled && plugin.SourceKind == "upload" {
		return ErrPluginUploadHasNoUpdates
	}
	if _, err := q.SetPluginUpdateCheck(ctx, postgresdb.SetPluginUpdateCheckParams{
		OrganizationID: tenant.OrganizationID, Name: name, UpdateCheck: enabled,
	}); err != nil {
		return fmt.Errorf("set plugin update check: %w", err)
	}
	return tx.Commit()
}

// ClaimPluginUpdateCheck hands the next plugin due a check to the caller and
// stamps it checked in the same transaction, so replicas never check one
// plugin twice in an interval. Organizations are enumerated as
// ClaimPluginImport does.
func (r *Repository) ClaimPluginUpdateCheck(
	ctx context.Context, interval time.Duration, includeGitHub bool,
) (*ClaimedPluginUpdateCheck, error) {
	organizations, err := postgresdb.New(r.db).ListOrganizationIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	for _, organization := range organizations {
		tenant := OrganizationTenant{OrganizationID: organization}
		claimed, err := r.claimPluginUpdateCheckFor(ctx, tenant, interval, includeGitHub)
		if err != nil || claimed != nil {
			return claimed, err
		}
	}
	return nil, nil
}

func (r *Repository) claimPluginUpdateCheckFor(
	ctx context.Context, tenant OrganizationTenant, interval time.Duration, includeGitHub bool,
) (*ClaimedPluginUpdateCheck, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.ClaimPluginUpdateCheck(ctx, postgresdb.ClaimPluginUpdateCheckParams{
		OrganizationID: tenant.OrganizationID, IncludeGithub: includeGitHub, IntervalSeconds: interval.Seconds(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim plugin update check: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit plugin update check claim: %w", err)
	}
	return &ClaimedPluginUpdateCheck{
		Tenant: tenant, ID: row.ID, Name: row.Name, Source: sourceOf(row.SourceKind, row.SourceRepository),
	}, nil
}

// RecordPluginUpdateCheck stores a check's result: the newest stable upstream
// version and its tag, or the error that stopped the check. A failed check
// keeps the last version seen.
func (r *Repository) RecordPluginUpdateCheck(
	ctx context.Context, tenant OrganizationTenant, id uuid.UUID, latest, tag, checkErr string,
) error {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	state, err := q.GetPluginUpdateState(ctx, id)
	if err != nil {
		return fmt.Errorf("get plugin update state: %w", err)
	}
	if err := q.RecordPluginUpdateCheck(ctx, postgresdb.RecordPluginUpdateCheckParams{
		ID: id, UpdateError: nullString(checkErr), UpdateLatest: nullString(latest), UpdateLatestTag: nullString(tag),
	}); err != nil {
		return fmt.Errorf("record plugin update check: %w", err)
	}
	// The event fires once, when a version newer than every one seen and
	// every one held first appears (ADR-0027 A6).
	newest := plugin.Newest(state.StoredVersions)
	if latest != "" && (!state.UpdateLatest.Valid || plugin.Compare(latest, state.UpdateLatest.String) > 0) &&
		(newest == "" || plugin.Compare(latest, newest) > 0) {
		source := sourceOf(state.SourceKind, state.SourceRepository)
		if err := enqueueOrganizationWebhookEvent(ctx, q, tenant, webhook.OperationPluginUpdateAvailable,
			webhook.Target{Type: "plugin", Name: state.Name},
			PluginUpdateAvailable{
				Plugin: state.Name, Source: PluginUpdateSource(source),
				Latest: latest, Tag: tag, NewestStored: newest,
			},
			time.Now().UTC(),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PluginUpdateAvailable is the payload of a plugin.update_available event.
type PluginUpdateAvailable struct {
	Plugin       string             `json:"plugin"`
	Source       PluginUpdateSource `json:"source"`
	Latest       string             `json:"latest"`
	Tag          string             `json:"tag,omitempty"`
	NewestStored string             `json:"newest_stored,omitempty"`
}

// PluginUpdateSource is where an update was seen.
type PluginUpdateSource struct {
	Kind       string `json:"kind"`
	Repository string `json:"repository,omitempty"`
}
