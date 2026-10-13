// Package pluginread serves an organization's published plugins to Packer's
// remote getter: the anonymous third surface of ADR-0027. The four request
// shapes are fixed by hashicorp/packer packer/plugin-getter/remote/getter.go
// (v1.16.1), not chosen here.
package pluginread

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/plugin"
	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

// Prefix is the root path the read plane owns.
const Prefix = "/plugins/"

// Repository is what the read plane reads.
type Repository interface {
	ServedPluginVersions(ctx context.Context, organization, name string) ([]string, error)
	ServedPluginFile(ctx context.Context, organization, name, version string, kind store.PluginFileKind, filename string) (store.ServedPluginFile, error)
	OpenPluginObject(ctx context.Context, key string) (io.ReadCloser, error)
	CountPluginDownload(ctx context.Context, tenant store.OrganizationTenant, pluginID uuid.UUID, version, platform string, day time.Time) error
}

// NewHandler serves the read plane. Every path it does not serve answers the
// same 404 as an unexposed registry, so nothing about the registry's state or
// contents is disclosed (ADR-0017).
func NewHandler(repository Repository, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle := audit.FromContext(r.Context())
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, Prefix), "/")
		if r.Method != http.MethodGet || len(parts) < 3 || !strings.HasPrefix(parts[1], "packer-plugin-") {
			notFound(w, handle)
			return
		}
		organization, name := parts[0], strings.TrimPrefix(parts[1], "packer-plugin-")
		handle.Enrich(audit.Enrichment{TargetID: strings.TrimPrefix(r.URL.Path, Prefix)})

		var served store.ServedPluginFile
		var kind store.PluginFileKind
		var err error
		switch {
		case len(parts) == 3 && parts[2] == "index.json":
			serveIndex(w, r.Context(), repository, logger, handle, organization, name)
			return
		case len(parts) == 4:
			version, file := parts[2], parts[3]
			var ok bool
			kind, ok = fileKind(name, version, file)
			if !ok {
				notFound(w, handle)
				return
			}
			served, err = repository.ServedPluginFile(r.Context(), organization, name, version, kind, file)
		default:
			notFound(w, handle)
			return
		}
		if errors.Is(err, registry.ErrNotFound) {
			notFound(w, handle)
			return
		}
		if err != nil {
			unavailable(r.Context(), w, logger, handle, err)
			return
		}
		if !serveFile(w, r.Context(), repository, logger, handle, served, contentType(parts[3])) || kind != store.ServedZip {
			return
		}
		zip, ok := plugin.ParseZipName(name, parts[2], parts[3])
		if !ok {
			logger.Warn("served plugin zip has an invalid name", "filename", parts[3], "correlation_id", audit.CorrelationID(r.Context()))
			return
		}
		day := time.Now().UTC().Truncate(24 * time.Hour)
		platform := zip.Platform.OS + "_" + zip.Platform.Arch
		if err := repository.CountPluginDownload(r.Context(), store.OrganizationTenant{OrganizationID: served.OrganizationID}, served.PluginID, parts[2], platform, day); err != nil {
			logger.Warn("count plugin download", "error", err, "correlation_id", audit.CorrelationID(r.Context()))
		}
	})
}

func fileKind(name, version, file string) (store.PluginFileKind, bool) {
	switch {
	case file == plugin.SumsName(name, version):
		return store.ServedSums, true
	case file == plugin.SignatureName(name, version):
		return store.ServedSignature, true
	case file == plugin.ManifestName(name, version):
		return store.ServedManifest, true
	case strings.HasSuffix(file, ".zip"):
		return store.ServedZip, true
	}
	return 0, false
}

func contentType(file string) string {
	switch {
	case strings.HasSuffix(file, ".json"):
		return "application/json"
	case strings.HasSuffix(file, "_SHA256SUMS"):
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

// The getter reads only the keys of versions (parseIndex), and an index with
// no versions is an error to it, so an empty one is never served.
func serveIndex(
	w http.ResponseWriter, ctx context.Context, repository Repository, logger *slog.Logger,
	handle *audit.Handle, organization, name string,
) {
	versions, err := repository.ServedPluginVersions(ctx, organization, name)
	if errors.Is(err, registry.ErrNotFound) {
		notFound(w, handle)
		return
	}
	if err != nil {
		unavailable(ctx, w, logger, handle, err)
		return
	}
	index := struct {
		Versions map[string]struct{} `json:"versions"`
	}{Versions: map[string]struct{}{}}
	for _, version := range versions {
		index.Versions[version] = struct{}{}
	}
	body, err := json.Marshal(index)
	if err != nil {
		unavailable(ctx, w, logger, handle, err)
		return
	}
	handle.Enrich(audit.Enrichment{Reason: "served"})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}

func serveFile(
	w http.ResponseWriter, ctx context.Context, repository Repository, logger *slog.Logger,
	handle *audit.Handle, served store.ServedPluginFile, kind string,
) bool {
	w.Header().Set("Content-Type", kind)
	if served.Content != nil {
		handle.Enrich(audit.Enrichment{Reason: "served"})
		w.Header().Set("Content-Length", strconv.Itoa(len(served.Content)))
		_, _ = w.Write(served.Content)
		return true
	}
	body, err := repository.OpenPluginObject(ctx, served.ObjectKey)
	if err != nil {
		unavailable(ctx, w, logger, handle, err)
		return false
	}
	defer func() { _ = body.Close() }()
	w.Header().Set("Content-Length", strconv.FormatInt(served.Size, 10))
	written, err := io.Copy(w, body)
	if err != nil || written != served.Size {
		handle.Enrich(audit.Enrichment{Reason: "stream_interrupted"})
		logger.Warn("plugin download interrupted", "object_key", served.ObjectKey, "written", written, "expected", served.Size, "error", err)
		return false
	}
	handle.Enrich(audit.Enrichment{Reason: "served"})
	return true
}

func notFound(w http.ResponseWriter, handle *audit.Handle) {
	handle.Enrich(audit.Enrichment{Reason: "not_found"})
	http.NotFound(w, nil)
}

func unavailable(ctx context.Context, w http.ResponseWriter, logger *slog.Logger, handle *audit.Handle, err error) {
	handle.Enrich(audit.Enrichment{Reason: "unavailable"})
	logger.Error("plugin read failed", "error", err, "correlation_id", audit.CorrelationID(ctx))
	http.Error(w, "service unavailable", http.StatusServiceUnavailable)
}
