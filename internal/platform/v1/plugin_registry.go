package v1

import (
	"context"
	"errors"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

func (s *server) admitPluginRegistryMutation(
	ctx context.Context, organizationID string,
) (*identity.Principal, refusal, error) {
	caller, refused := authorizeTenancy(ctx, identity.RoleMaintainer, organizationID, "")
	if refused != permitted {
		return nil, refused, nil
	}
	if _, err := s.repository.GetOrganization(ctx, organizationID); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return nil, refusedTenancy, nil
		}
		return nil, permitted, err
	}
	return caller, permitted, nil
}

func (s *server) GetPluginRegistry(
	ctx context.Context, request GetPluginRegistryRequestObject,
) (GetPluginRegistryResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID := request.OrganizationId.String()
	caller, refused := authorizeOrganizationVisibility(ctx, identity.RoleReader, request.OrganizationId)
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	if _, err := s.repository.GetOrganization(ctx, organizationID); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			audited.refused(refusedTenancy.reason())
			return newRefusal(refusedTenancy), nil
		}
		audited.failed("storage_failed")
		return nil, err
	}
	audited.actor(caller)
	state, err := s.repository.GetPluginRegistry(ctx, store.ParseOrganizationTenant(organizationID))
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, "")
	return GetPluginRegistry200JSONResponse(renderPluginRegistry(state)), nil
}

func (s *server) EnablePluginRegistry(
	ctx context.Context, request EnablePluginRegistryRequestObject,
) (EnablePluginRegistryResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID := request.OrganizationId.String()
	caller, refused, err := s.admitPluginRegistryMutation(ctx, organizationID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	state, err := s.repository.EnablePluginRegistry(ctx, store.ParseOrganizationTenant(organizationID))
	if errors.Is(err, store.ErrPluginRegistryAlreadyEnabled) {
		audited.failed("already_enabled")
		return EnablePluginRegistry409JSONResponse{Message: "plugin registry is already enabled"}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, "enabled")
	return EnablePluginRegistry201JSONResponse(renderPluginRegistry(state)), nil
}

func (s *server) ExposePluginRegistry(
	ctx context.Context, request ExposePluginRegistryRequestObject,
) (ExposePluginRegistryResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID := request.OrganizationId.String()
	caller, refused, err := s.admitPluginRegistryMutation(ctx, organizationID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	state, err := s.repository.ExposePluginRegistry(ctx, store.ParseOrganizationTenant(organizationID))
	switch {
	case errors.Is(err, store.ErrPluginRegistryNotEnabled):
		audited.failed("not_enabled")
		return ExposePluginRegistry409JSONResponse{Message: "plugin registry is not enabled"}, nil
	case errors.Is(err, store.ErrPluginRegistryAlreadyExposed):
		audited.failed("already_exposed")
		return ExposePluginRegistry409JSONResponse{Message: "plugin registry is already exposed"}, nil
	case err != nil:
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, "exposed")
	return ExposePluginRegistry200JSONResponse(renderPluginRegistry(state)), nil
}

func (s *server) UnexposePluginRegistry(
	ctx context.Context, request UnexposePluginRegistryRequestObject,
) (UnexposePluginRegistryResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID := request.OrganizationId.String()
	caller, refused, err := s.admitPluginRegistryMutation(ctx, organizationID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	state, err := s.repository.UnexposePluginRegistry(ctx, store.ParseOrganizationTenant(organizationID))
	switch {
	case errors.Is(err, store.ErrPluginRegistryNotEnabled):
		audited.failed("not_enabled")
		return UnexposePluginRegistry409JSONResponse{Message: "plugin registry is not enabled"}, nil
	case errors.Is(err, store.ErrPluginRegistryNotExposed):
		audited.failed("not_exposed")
		return UnexposePluginRegistry409JSONResponse{Message: "plugin registry is not exposed"}, nil
	case err != nil:
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, "unexposed")
	return UnexposePluginRegistry200JSONResponse(renderPluginRegistry(state)), nil
}

func (s *server) DisablePluginRegistry(
	ctx context.Context, request DisablePluginRegistryRequestObject,
) (DisablePluginRegistryResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID := request.OrganizationId.String()
	caller, refused, err := s.admitPluginRegistryMutation(ctx, organizationID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	err = s.repository.DisablePluginRegistry(ctx, store.ParseOrganizationTenant(organizationID))
	switch {
	case errors.Is(err, store.ErrPluginRegistryNotEnabled):
		audited.failed("not_enabled")
		return DisablePluginRegistry409JSONResponse{Message: "plugin registry is not enabled"}, nil
	case errors.Is(err, store.ErrPluginRegistryStillExposed):
		audited.failed("still_exposed")
		return DisablePluginRegistry409JSONResponse{Message: "plugin registry is still exposed; unexpose first"}, nil
	case err != nil:
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, "disabled")
	return DisablePluginRegistry204Response{}, nil
}

func renderPluginRegistry(state store.PluginRegistry) PluginRegistry {
	return PluginRegistry{Enabled: state.Enabled, Exposed: state.Exposed}
}
