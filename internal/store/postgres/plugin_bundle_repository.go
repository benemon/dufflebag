package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/benemon/dufflebag/internal/domain/plugin"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
)

var (
	ErrPluginBundleBusy     = errors.New("plugin bundle export is already queued or running")
	ErrPluginBundleNotFound = errors.New("plugin bundle export not found")
)

// PluginBundle is the current export job and its last successful archive.
type PluginBundle struct {
	State       string
	RequestedBy string
	CreatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	Error       string
	Bundle      *PluginBundleResult
}

// PluginBundleResult describes one stored bundle archive.
type PluginBundleResult struct {
	ExportedAt   time.Time
	ObjectKey    string
	Filename     string
	Size         int64
	SHA256       string
	PluginCount  int
	VersionCount int
}

// ClaimedPluginBundle is an export a worker now owns.
type ClaimedPluginBundle struct {
	Tenant            OrganizationTenant
	OrganizationName  string
	RequestedHost     string
	CreatedAt         time.Time
	PreviousObjectKey string
}

// ExportablePluginFile is one object included in a bundle.
type ExportablePluginFile struct {
	Filename  string
	OS        string
	Arch      string
	SHA256    string
	Size      int64
	ObjectKey string
}

// ExportablePluginVersion is one non-revoked stored version.
type ExportablePluginVersion struct {
	Version   string
	Sums      ExportablePluginFile
	Signature *ExportablePluginFile
	Manifest  *ExportablePluginFile
	Zips      []ExportablePluginFile
}

// ExportablePlugin is one plugin with at least one non-revoked version.
type ExportablePlugin struct {
	Name     string
	Source   PluginSource
	Versions []ExportablePluginVersion
}

// QueuePluginBundle queues a new export unless one is already active.
func (r *Repository) QueuePluginBundle(ctx context.Context, tenant OrganizationTenant, requestedBy, host string) error {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	changed, err := q.QueuePluginBundle(ctx, postgresdb.QueuePluginBundleParams{
		OrganizationID: tenant.OrganizationID, RequestedBy: requestedBy, RequestedHost: host,
	})
	if err != nil {
		return fmt.Errorf("queue plugin bundle: %w", err)
	}
	if changed == 0 {
		if _, err := q.GetPluginRegistry(ctx, tenant.OrganizationID); errors.Is(err, sql.ErrNoRows) {
			return ErrPluginRegistryNotEnabled
		} else if err != nil {
			return fmt.Errorf("get plugin registry: %w", err)
		}
		return ErrPluginBundleBusy
	}
	return tx.Commit()
}

// ClaimPluginBundle hands one queued or stale export to the caller.
func (r *Repository) ClaimPluginBundle(ctx context.Context, stale time.Duration) (*ClaimedPluginBundle, error) {
	organizations, err := postgresdb.New(r.db).ListOrganizationIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	for _, organizationID := range organizations {
		claimed, err := r.claimPluginBundleFor(ctx, OrganizationTenant{OrganizationID: organizationID}, stale)
		if err != nil || claimed != nil {
			return claimed, err
		}
	}
	return nil, nil
}

func (r *Repository) claimPluginBundleFor(ctx context.Context, tenant OrganizationTenant, stale time.Duration) (*ClaimedPluginBundle, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.ClaimPluginBundle(ctx, postgresdb.ClaimPluginBundleParams{OrganizationID: tenant.OrganizationID, StaleSeconds: stale.Seconds()})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim plugin bundle: %w", err)
	}
	organization, err := q.GetOrganization(ctx, tenant.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("get plugin bundle organization: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit plugin bundle claim: %w", err)
	}
	return &ClaimedPluginBundle{
		Tenant: tenant, OrganizationName: organization.Name, RequestedHost: row.RequestedHost,
		CreatedAt: utc(row.CreatedAt), PreviousObjectKey: row.ObjectKey.String,
	}, nil
}

// RecordPluginBundle records either a successful archive or a failed run.
func (r *Repository) RecordPluginBundle(ctx context.Context, tenant OrganizationTenant, result *PluginBundleResult, runErr string) error {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	params := postgresdb.RecordPluginBundleParams{OrganizationID: tenant.OrganizationID, State: "failed", Error: runErr}
	if result != nil {
		params.State, params.Error = "succeeded", ""
		params.ObjectKey = sql.NullString{String: result.ObjectKey, Valid: true}
		params.Filename = sql.NullString{String: result.Filename, Valid: true}
		params.Size = sql.NullInt64{Int64: result.Size, Valid: true}
		params.Sha256 = sql.NullString{String: result.SHA256, Valid: true}
		params.PluginCount = sql.NullInt32{Int32: int32(result.PluginCount), Valid: true}
		params.VersionCount = sql.NullInt32{Int32: int32(result.VersionCount), Valid: true}
	}
	if err := q.RecordPluginBundle(ctx, params); err != nil {
		return fmt.Errorf("record plugin bundle: %w", err)
	}
	return tx.Commit()
}

// GetPluginBundle reads the current job and last successful archive.
func (r *Repository) GetPluginBundle(ctx context.Context, tenant OrganizationTenant) (PluginBundle, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return PluginBundle{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.GetPluginBundle(ctx, tenant.OrganizationID)
	if errors.Is(err, sql.ErrNoRows) {
		if _, registryErr := q.GetPluginRegistry(ctx, tenant.OrganizationID); errors.Is(registryErr, sql.ErrNoRows) {
			return PluginBundle{}, ErrPluginRegistryNotEnabled
		} else if registryErr != nil {
			return PluginBundle{}, fmt.Errorf("get plugin registry: %w", registryErr)
		}
		return PluginBundle{}, ErrPluginBundleNotFound
	}
	if err != nil {
		return PluginBundle{}, fmt.Errorf("get plugin bundle: %w", err)
	}
	bundle := PluginBundle{
		State: row.State, RequestedBy: row.RequestedBy, CreatedAt: utc(row.CreatedAt),
		StartedAt: nullTime(row.StartedAt), FinishedAt: nullTime(row.FinishedAt), Error: row.Error,
	}
	if row.ObjectKey.Valid {
		bundle.Bundle = &PluginBundleResult{
			ExportedAt: utc(row.ExportedAt.Time), ObjectKey: row.ObjectKey.String, Filename: row.Filename.String,
			Size: row.Size.Int64, SHA256: row.Sha256.String, PluginCount: int(row.PluginCount.Int32), VersionCount: int(row.VersionCount.Int32),
		}
	}
	return bundle, nil
}

// ExportablePluginVersions returns every non-revoked stored version.
func (r *Repository) ExportablePluginVersions(ctx context.Context, tenant OrganizationTenant) ([]ExportablePlugin, error) {
	tx, q, err := r.beginOrganization(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := q.ListExportablePluginVersions(ctx, tenant.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("list exportable plugin versions: %w", err)
	}
	plugins := make([]ExportablePlugin, 0)
	for _, row := range rows {
		if len(plugins) == 0 || plugins[len(plugins)-1].Name != row.Name {
			plugins = append(plugins, ExportablePlugin{Name: row.Name, Source: PluginSource{Kind: row.SourceKind, Repository: row.SourceRepository.String}})
		}
		version := ExportablePluginVersion{
			Version: row.Version,
			Sums:    ExportablePluginFile{Size: row.SumsSize, ObjectKey: row.SumsKey},
		}
		if row.SignatureKey.Valid {
			version.Signature = &ExportablePluginFile{Size: row.SignatureSize.Int64, ObjectKey: row.SignatureKey.String}
		}
		if row.ManifestKey.Valid {
			version.Manifest = &ExportablePluginFile{Size: row.ManifestSize.Int64, ObjectKey: row.ManifestKey.String}
		}
		files, err := q.ListStoredPluginVersionFiles(ctx, row.ID)
		if err != nil {
			return nil, fmt.Errorf("list exportable plugin files: %w", err)
		}
		version.Zips = make([]ExportablePluginFile, 0, len(files))
		for _, file := range files {
			version.Zips = append(version.Zips, ExportablePluginFile{
				Filename: file.Filename, OS: file.Os, Arch: file.Arch, SHA256: file.Sha256, Size: file.Size, ObjectKey: file.ObjectKey,
			})
		}
		last := len(plugins) - 1
		plugins[last].Versions = append(plugins[last].Versions, version)
	}
	for i := range plugins {
		sort.Slice(plugins[i].Versions, func(a, b int) bool {
			return plugin.Compare(plugins[i].Versions[a].Version, plugins[i].Versions[b].Version) > 0
		})
	}
	return plugins, nil
}

// PutPluginBundle stores one bundle archive.
func (r *Repository) PutPluginBundle(ctx context.Context, _ OrganizationTenant, key string, body io.ReadSeeker, size int64) error {
	objects, err := r.objectStore()
	if err != nil {
		return err
	}
	if err := objects.PutFile(ctx, key, body, size); err != nil {
		return fmt.Errorf("put plugin bundle: %w", err)
	}
	return nil
}

// DeletePluginBundleObject removes a replaced bundle archive.
func (r *Repository) DeletePluginBundleObject(ctx context.Context, _ OrganizationTenant, key string) error {
	objects, err := r.objectStore()
	if err != nil {
		return err
	}
	if err := objects.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete plugin bundle: %w", err)
	}
	return nil
}
