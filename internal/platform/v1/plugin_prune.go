package v1

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/pluginimport"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

func (s *server) GetPluginPrune(ctx context.Context, request GetPluginPruneRequestObject) (GetPluginPruneResponseObject, error) {
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
	prune, err := s.repository.GetPluginPrune(ctx, store.ParseOrganizationTenant(organizationID))
	if errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		audited.failed("not_enabled")
		return GetPluginPrune409JSONResponse{Message: "plugin registry is not enabled"}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, "")
	return GetPluginPrune200JSONResponse(renderPluginPrune(prune)), nil
}

func (s *server) SetPluginPrune(ctx context.Context, request SetPluginPruneRequestObject) (SetPluginPruneResponseObject, error) {
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
	body := request.Body
	if body == nil || !pluginimport.ValidPruneAge(int(body.AgeDays)) {
		audited.failed("invalid_request")
		return badRequestResponse{message: "age_days must be one of 1, 7, 14, 30, 60, 90, 180 or 365"}, nil
	}
	next, err := pluginimport.NextPrune(body.Schedule, s.now())
	if err != nil {
		audited.failed("invalid_request")
		return badRequestResponse{message: err.Error()}, nil
	}
	var nextAt *time.Time
	if body.Enabled {
		nextAt = &next
	}
	tenant := store.ParseOrganizationTenant(organizationID)
	err = s.repository.SetPluginPrune(ctx, tenant, body.Enabled, int(body.AgeDays), body.Schedule, nextAt)
	if errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		audited.failed("not_enabled")
		return SetPluginPrune409JSONResponse{Message: "plugin registry is not enabled"}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	prune, err := s.repository.GetPluginPrune(ctx, tenant)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, map[bool]string{true: "on", false: "off"}[body.Enabled])
	return SetPluginPrune200JSONResponse(renderPluginPrune(prune)), nil
}

// RunPluginPrune is Prune now: one run with the stored age limit, recorded
// like a scheduled one. The HTTP audit pair covers it; the worker's system
// emitter is not used here.
func (s *server) RunPluginPrune(ctx context.Context, request RunPluginPruneRequestObject) (RunPluginPruneResponseObject, error) {
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
	prune, err := s.repository.GetPluginPrune(ctx, tenant)
	if errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		audited.failed("not_enabled")
		return RunPluginPrune409JSONResponse{Message: "plugin registry is not enabled"}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	cleared, pruneErr := pluginimport.PruneOnce(ctx, s.repository, nil, tenant, prune.AgeDays, s.now())
	runErr := ""
	if pruneErr != nil {
		runErr = pruneErr.Error()
	}
	var nextAt *time.Time
	if prune.Enabled {
		if next, err := pluginimport.NextPrune(prune.Schedule, s.now()); err == nil {
			nextAt = &next
		}
	}
	if err := s.repository.RecordPluginPrune(ctx, tenant, cleared, runErr, nextAt); err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if pruneErr != nil {
		audited.failed("storage_failed")
		return nil, pruneErr
	}
	after, err := s.repository.GetPluginPrune(ctx, tenant)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, "cleared "+strconv.Itoa(cleared))
	return RunPluginPrune200JSONResponse(renderPluginPrune(after)), nil
}

func renderPluginPrune(prune store.PluginPrune) PluginPrune {
	return PluginPrune{
		Enabled: prune.Enabled, AgeDays: prune.AgeDays, Schedule: prune.Schedule,
		NextRunAt: prune.NextAt, LastRunAt: prune.LastRunAt, LastRunCleared: prune.LastCleared, LastError: prune.LastError,
		WouldClear: prune.WouldClear, Unfinished: prune.Unfinished, Kept: prune.Kept, OldestFinishedAt: prune.OldestFinishedAt,
	}
}
