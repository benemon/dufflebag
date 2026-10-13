package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/pluginimport"
	"github.com/benemon/dufflebag/internal/store/objectstore"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// A bundle's filename ends in the first twelve hex characters of its digest
// (ADR-0029 D8), which the import reads back to flag a renamed or altered copy.
var bundleFilenameDigest = regexp.MustCompile(`-([0-9a-f]{12})\.tar\.gz$`)

func (s *server) ImportPluginBundle(ctx context.Context, request ImportPluginBundleRequestObject) (ImportPluginBundleResponseObject, error) {
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
	upload, expected, file, err := spoolPluginBundle(request.Body)
	if file != nil {
		defer func() {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}()
	}
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		audited.failed("too_large")
		return ImportPluginBundle413JSONResponse{Message: fmt.Sprintf("bundle exceeds %d bytes", tooLarge.Limit)}, nil
	case err != nil:
		audited.failed("invalid_request")
		return ImportPluginBundle400JSONResponse{Message: err.Error()}, nil
	}
	if expected != "" && expected != upload.SHA256 {
		audited.failed("digest_mismatch")
		return ImportPluginBundle400JSONResponse{Message: fmt.Sprintf("the bundle's SHA-256 is %s, not the expected %s; nothing was stored", upload.SHA256, expected)}, nil
	}
	upload.ObjectKey = objectstore.BundleImportKey(organizationID, upload.SHA256)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if err := s.repository.PutPluginBundle(ctx, tenant, upload.ObjectKey, file, upload.Size); err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	err = s.repository.QueuePluginBundleImport(ctx, tenant, caller.Name, upload)
	if err != nil {
		if removeErr := s.repository.DeletePluginBundleObject(ctx, tenant, upload.ObjectKey); removeErr != nil {
			s.logger.Warn("uploaded plugin bundle not removed after a refused import", "object_key", upload.ObjectKey, "error", removeErr)
		}
	}
	if errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		audited.failed("not_enabled")
		return ImportPluginBundle409JSONResponse{Message: "plugin registry is not enabled"}, nil
	}
	if errors.Is(err, store.ErrPluginBundleImportBusy) {
		audited.failed("busy")
		return ImportPluginBundle409JSONResponse{Message: "a plugin bundle import is already queued or running"}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	job, err := s.repository.GetPluginBundleImport(ctx, tenant)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, upload.Filename)
	return ImportPluginBundle202JSONResponse(renderPluginBundleImport(job)), nil
}

func (s *server) GetPluginBundleImport(ctx context.Context, request GetPluginBundleImportRequestObject) (GetPluginBundleImportResponseObject, error) {
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
	job, err := s.repository.GetPluginBundleImport(ctx, store.ParseOrganizationTenant(organizationID))
	if errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		audited.failed("not_enabled")
		return GetPluginBundleImport409JSONResponse{Message: "plugin registry is not enabled"}, nil
	}
	if errors.Is(err, store.ErrPluginBundleImportNotFound) {
		audited.failed("not_found")
		return GetPluginBundleImport404JSONResponse{Message: "no plugin bundle import has been queued"}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, "")
	return GetPluginBundleImport200JSONResponse(renderPluginBundleImport(job)), nil
}

// spoolPluginBundle reads the bundle part to a temp file, hashing it, and
// the optional sha256 part in whichever order the form sends them. The file
// is returned even on failure so the caller removes it.
func spoolPluginBundle(body *multipart.Reader) (upload store.PluginBundleUpload, expected string, file *os.File, err error) {
	if body == nil {
		return upload, "", nil, errors.New("a multipart body with a bundle part is required")
	}
	for {
		part, err := body.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return upload, "", file, err
		}
		switch part.FormName() {
		case "sha256":
			digest, err := io.ReadAll(io.LimitReader(part, 128))
			_ = part.Close()
			if err != nil {
				return upload, "", file, err
			}
			expected = strings.ToLower(strings.TrimSpace(string(digest)))
			if len(expected) != 64 || strings.Trim(expected, "0123456789abcdef") != "" {
				return upload, "", file, errors.New("sha256 must be the 64 hex characters of the bundle's digest")
			}
		case "bundle":
			if file != nil {
				_ = part.Close()
				return upload, "", file, errors.New("the request carries two bundle parts")
			}
			if file, err = os.CreateTemp("", "dufflebag-bundle-upload-*"); err != nil {
				_ = part.Close()
				return upload, "", nil, err
			}
			hash := sha256.New()
			size, err := io.Copy(io.MultiWriter(file, hash), part)
			_ = part.Close()
			if err != nil {
				return upload, "", file, err
			}
			if size == 0 {
				return upload, "", file, errors.New("the bundle part is empty")
			}
			upload = store.PluginBundleUpload{Filename: part.FileName(), Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}
		default:
			_ = part.Close()
			return upload, "", file, fmt.Errorf("%s is not the bundle or sha256 part", part.FormName())
		}
	}
	if file == nil {
		return upload, "", nil, errors.New("the request carries no bundle part")
	}
	return upload, expected, file, nil
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func renderPluginBundleImport(job store.PluginBundleImport) PluginBundleImport {
	rendered := PluginBundleImport{
		State: PluginBundleImportState(job.State), RequestedBy: job.RequestedBy,
		Filename: job.Upload.Filename, Size: job.Upload.Size, Sha256: job.Upload.SHA256,
		CreatedAt: job.CreatedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt, Error: job.Error,
		Outcomes: []PluginBundleVersionOutcome{},
	}
	if match := bundleFilenameDigest.FindStringSubmatch(job.Upload.Filename); match != nil {
		matches := len(job.Upload.SHA256) >= 12 && job.Upload.SHA256[:12] == match[1]
		rendered.FilenameMatches = &matches
	}
	if job.Origin.ExportedAt != nil {
		rendered.Bundle = &struct {
			DufflebagVersion string    `json:"dufflebag_version"`
			ExportedAt       time.Time `json:"exported_at"`
			Host             string    `json:"host"`
			Organization     string    `json:"organization"`
			PluginCount      int       `json:"plugin_count"`
			VersionCount     int       `json:"version_count"`
		}{
			DufflebagVersion: job.Origin.Version, ExportedAt: *job.Origin.ExportedAt, Host: job.Origin.Host, Organization: job.Origin.Organization,
			PluginCount: job.Origin.PluginCount, VersionCount: job.Origin.VersionCount,
		}
	}
	var outcomes []pluginimport.BundleVersionOutcome
	if len(job.Outcomes) > 0 {
		_ = json.Unmarshal(job.Outcomes, &outcomes)
	}
	for _, outcome := range outcomes {
		rendered.Outcomes = append(rendered.Outcomes, PluginBundleVersionOutcome{
			Plugin: outcome.Plugin, Version: outcome.Version, OriginKind: outcome.OriginKind,
			OriginRepository: optionalString(outcome.OriginRepository), Outcome: PluginBundleVersionOutcomeOutcome(outcome.Outcome),
			Error: optionalString(outcome.Error),
		})
	}
	return rendered
}
