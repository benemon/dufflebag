package pluginimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/plugin"
	"github.com/benemon/dufflebag/internal/store/objectstore"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// BundleExports is where the exporter finds content and stores archives.
type BundleExports interface {
	ClaimPluginBundle(context.Context, time.Duration) (*store.ClaimedPluginBundle, error)
	ExportablePluginVersions(context.Context, store.OrganizationTenant) ([]store.ExportablePlugin, error)
	OpenPluginObject(context.Context, string) (io.ReadCloser, error)
	PutPluginBundle(context.Context, store.OrganizationTenant, string, io.ReadSeeker, int64) error
	DeletePluginBundleObject(context.Context, store.OrganizationTenant, string) error
	RecordPluginBundle(context.Context, store.OrganizationTenant, *store.PluginBundleResult, string) error
}

// Exporter builds queued plugin bundles.
type Exporter struct {
	exports  BundleExports
	audit    *audit.SystemEmitter
	interval time.Duration
	logger   *slog.Logger
	now      func() time.Time

	DufflebagVersion string
}

// NewExporter polls queued bundle exports.
func NewExporter(exports BundleExports, emitter *audit.SystemEmitter, interval time.Duration, logger *slog.Logger) *Exporter {
	return &Exporter{exports: exports, audit: emitter, interval: interval, logger: logger, now: time.Now}
}

// Run works queued exports until ctx ends.
func (e *Exporter) Run(ctx context.Context) {
	for {
		worked, err := e.RunOnce(ctx)
		if err != nil {
			e.logger.Error("plugin bundle export failed", "error", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(e.interval):
		}
	}
}

// RunOnce builds at most one queued bundle.
func (e *Exporter) RunOnce(ctx context.Context) (bool, error) {
	claimed, err := e.exports.ClaimPluginBundle(ctx, staleAfter)
	if err != nil || claimed == nil {
		return false, err
	}
	err = e.export(ctx, claimed)
	if err != nil {
		if recordErr := e.exports.RecordPluginBundle(ctx, claimed.Tenant, nil, err.Error()); recordErr != nil {
			err = errors.Join(err, recordErr)
		}
	}
	return true, err
}

func (e *Exporter) export(ctx context.Context, claimed *store.ClaimedPluginBundle) (runErr error) {
	event := audit.SystemEvent{
		Operation: identity.AuditOperationPluginRegistryBundleBuild, TargetType: "plugin_registry", TargetID: claimed.Tenant.OrganizationID.String(),
		Scope: identity.AuditScopeOrganization, OrganizationID: claimed.Tenant.OrganizationID.String(),
	}
	correlationID := ""
	if e.audit != nil {
		id, err := e.audit.Request(event)
		if err != nil {
			return fmt.Errorf("audit plugin bundle request: %w", err)
		}
		correlationID = id
		defer func() {
			outcome, reason := identity.AuditOutcomeSuccess, "built"
			if runErr != nil {
				outcome, reason, event.Detail = identity.AuditOutcomeFailure, "build_failed", runErr.Error()
			}
			if err := e.audit.Response(correlationID, event, outcome, reason); err != nil && runErr == nil {
				runErr = fmt.Errorf("audit plugin bundle response: %w", err)
			}
		}()
	}
	exportedAt := e.now().UTC()
	plugins, err := e.exports.ExportablePluginVersions(ctx, claimed.Tenant)
	if err != nil {
		return err
	}
	manifest, versionCount, err := e.manifest(ctx, claimed, exportedAt, plugins)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(os.TempDir(), "dufflebag-plugin-bundle-*.tar.gz")
	if err != nil {
		return fmt.Errorf("create plugin bundle temp file: %w", err)
	}
	defer func() {
		_ = temp.Close()
		_ = os.Remove(temp.Name())
	}()
	hash := sha256.New()
	if err := plugin.WriteBundle(io.MultiWriter(temp, hash), manifest, func(file plugin.BundleFile) (io.ReadCloser, error) {
		return e.exports.OpenPluginObject(ctx, file.ObjectKey)
	}); err != nil {
		return err
	}
	stat, err := temp.Stat()
	if err != nil {
		return fmt.Errorf("stat plugin bundle: %w", err)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	filename := fmt.Sprintf("dufflebag-plugins-%s-%s-%s.tar.gz", claimed.OrganizationName, exportedAt.Format("20060102T150405Z"), digest[:12])
	key := objectstore.BundleKey(claimed.Tenant.OrganizationID.String(), digest)
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind plugin bundle: %w", err)
	}
	if err := e.exports.PutPluginBundle(ctx, claimed.Tenant, key, temp, stat.Size()); err != nil {
		return err
	}
	result := &store.PluginBundleResult{
		ObjectKey: key, Filename: filename, Size: stat.Size(), SHA256: digest,
		PluginCount: len(manifest.Plugins), VersionCount: versionCount,
	}
	if err := e.exports.RecordPluginBundle(ctx, claimed.Tenant, result, ""); err != nil {
		return err
	}
	event.Detail = fmt.Sprintf("%d plugins, %d versions, %d bytes", result.PluginCount, result.VersionCount, result.Size)
	if claimed.PreviousObjectKey != "" && claimed.PreviousObjectKey != key {
		if err := e.exports.DeletePluginBundleObject(ctx, claimed.Tenant, claimed.PreviousObjectKey); err != nil {
			return err
		}
	}
	return nil
}

func (e *Exporter) manifest(
	ctx context.Context, claimed *store.ClaimedPluginBundle, exportedAt time.Time, exported []store.ExportablePlugin,
) (plugin.BundleManifest, int, error) {
	manifest := plugin.BundleManifest{
		Format: 1, ExportedAt: exportedAt,
		Instance:         plugin.BundleInstance{Host: claimed.RequestedHost, Organization: claimed.OrganizationName},
		DufflebagVersion: e.DufflebagVersion,
		Plugins:          make([]plugin.BundlePlugin, 0, len(exported)),
	}
	versionCount := 0
	for _, storedPlugin := range exported {
		bundledPlugin := plugin.BundlePlugin{
			Name:     storedPlugin.Name,
			Origin:   plugin.BundleOrigin{Kind: storedPlugin.Source.Kind, Repository: storedPlugin.Source.Repository},
			Versions: make([]plugin.BundleVersion, 0, len(storedPlugin.Versions)),
		}
		for _, storedVersion := range storedPlugin.Versions {
			prefix := fmt.Sprintf("packer-plugin-%s_v%s_", storedPlugin.Name, storedVersion.Version)
			version := plugin.BundleVersion{Version: storedVersion.Version, Platforms: make([]string, 0, len(storedVersion.Zips))}
			metadata := []struct {
				name string
				file *store.ExportablePluginFile
			}{
				{name: prefix + "SHA256SUMS", file: &storedVersion.Sums},
				{name: prefix + "SHA256SUMS.sig", file: storedVersion.Signature},
				{name: prefix + "manifest.json", file: storedVersion.Manifest},
			}
			for _, entry := range metadata {
				if entry.file == nil {
					continue
				}
				digest, err := e.hashObject(ctx, *entry.file)
				if err != nil {
					return plugin.BundleManifest{}, 0, err
				}
				version.Files = append(version.Files, plugin.BundleFile{
					Name: entry.name, SHA256: digest, Size: entry.file.Size, ObjectKey: entry.file.ObjectKey,
				})
			}
			for _, file := range storedVersion.Zips {
				version.Platforms = append(version.Platforms, file.OS+"_"+file.Arch)
				version.Files = append(version.Files, plugin.BundleFile{
					Name: file.Filename, SHA256: file.SHA256, Size: file.Size, ObjectKey: file.ObjectKey,
				})
			}
			sort.Strings(version.Platforms)
			bundledPlugin.Versions = append(bundledPlugin.Versions, version)
			versionCount++
		}
		manifest.Plugins = append(manifest.Plugins, bundledPlugin)
	}
	return manifest, versionCount, nil
}

func (e *Exporter) hashObject(ctx context.Context, file store.ExportablePluginFile) (string, error) {
	body, err := e.exports.OpenPluginObject(ctx, file.ObjectKey)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(hash, body)
	closeErr := body.Close()
	if copyErr != nil {
		return "", fmt.Errorf("hash plugin object %q: %w", file.ObjectKey, copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close plugin object %q: %w", file.ObjectKey, closeErr)
	}
	if written != file.Size {
		return "", fmt.Errorf("hash plugin object %q: got %d bytes, want %d", file.ObjectKey, written, file.Size)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
