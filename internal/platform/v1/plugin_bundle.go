package v1

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

func (s *server) ExportPluginBundle(ctx context.Context, request ExportPluginBundleRequestObject) (ExportPluginBundleResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID := request.OrganizationId.String()
	caller, refused, err := s.admitOrganization(ctx, identity.RoleMaintainer, organizationID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	tenant := store.ParseOrganizationTenant(organizationID)
	host, _ := ctx.Value(requestHostKey{}).(string)
	err = s.repository.QueuePluginBundle(ctx, tenant, caller.Name, host)
	if errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		audited.failed("not_enabled")
		return ExportPluginBundle409JSONResponse{Message: "plugin registry is not enabled"}, nil
	}
	if errors.Is(err, store.ErrPluginBundleBusy) {
		audited.failed("busy")
		return ExportPluginBundle409JSONResponse{Message: "a plugin bundle export is already queued or running"}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	bundle, err := s.repository.GetPluginBundle(ctx, tenant)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, "queued")
	return ExportPluginBundle202JSONResponse(renderPluginBundle(bundle)), nil
}

func (s *server) GetPluginBundle(ctx context.Context, request GetPluginBundleRequestObject) (GetPluginBundleResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID := request.OrganizationId.String()
	caller, refused, err := s.admitOrganization(ctx, identity.RoleReader, organizationID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	bundle, err := s.repository.GetPluginBundle(ctx, store.ParseOrganizationTenant(organizationID))
	if errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		audited.failed("not_enabled")
		return GetPluginBundle409JSONResponse{Message: "plugin registry is not enabled"}, nil
	}
	if errors.Is(err, store.ErrPluginBundleNotFound) {
		audited.failed("not_found")
		return GetPluginBundle404JSONResponse{Message: "no plugin bundle export has been queued"}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, "")
	return GetPluginBundle200JSONResponse(renderPluginBundle(bundle)), nil
}

func (s *server) DownloadPluginBundle(ctx context.Context, request DownloadPluginBundleRequestObject) (DownloadPluginBundleResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID := request.OrganizationId.String()
	caller, refused, err := s.admitOrganization(ctx, identity.RoleMaintainer, organizationID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	bundle, err := s.repository.GetPluginBundle(ctx, store.ParseOrganizationTenant(organizationID))
	if errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		audited.failed("not_enabled")
		return DownloadPluginBundle409JSONResponse{Message: "plugin registry is not enabled"}, nil
	}
	if errors.Is(err, store.ErrPluginBundleNotFound) || (err == nil && bundle.Bundle == nil) {
		audited.failed("not_found")
		return DownloadPluginBundle404JSONResponse{Message: "no plugin bundle has been exported"}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	body, err := s.repository.OpenPluginObject(ctx, bundle.Bundle.ObjectKey)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, bundle.Bundle.Filename)
	return pluginBundleDownload{body: body, filename: bundle.Bundle.Filename, size: bundle.Bundle.Size}, nil
}

// pluginBundleDownload streams the archive and closes it, which the generated
// binary response cannot do.
type pluginBundleDownload struct {
	body     io.ReadCloser
	filename string
	size     int64
}

func (response pluginBundleDownload) VisitDownloadPluginBundleResponse(w http.ResponseWriter) error {
	defer func() { _ = response.body.Close() }()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Length", strconv.FormatInt(response.size, 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", response.filename))
	w.WriteHeader(http.StatusOK)
	_, err := io.Copy(w, response.body)
	return err
}

func renderPluginBundle(bundle store.PluginBundle) PluginBundle {
	rendered := PluginBundle{
		State: PluginBundleState(bundle.State), RequestedBy: bundle.RequestedBy, CreatedAt: bundle.CreatedAt,
		StartedAt: bundle.StartedAt, FinishedAt: bundle.FinishedAt, Error: bundle.Error,
	}
	if bundle.Bundle != nil {
		rendered.Bundle = &struct {
			ExportedAt   time.Time `json:"exported_at"`
			Filename     string    `json:"filename"`
			PluginCount  int       `json:"plugin_count"`
			Sha256       string    `json:"sha256"`
			Size         int64     `json:"size"`
			VersionCount int       `json:"version_count"`
		}{
			ExportedAt: bundle.Bundle.ExportedAt, Filename: bundle.Bundle.Filename, PluginCount: bundle.Bundle.PluginCount,
			Sha256: bundle.Bundle.SHA256, Size: bundle.Bundle.Size, VersionCount: bundle.Bundle.VersionCount,
		}
	}
	return rendered
}
