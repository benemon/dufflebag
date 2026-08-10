package v1

import (
	"context"
	"errors"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/bagdrop"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
)

// admitBagDrop preserves the disclosure order: tenancy standing first,
// project existence second, maintainer authority last. A low-role caller does
// not get a 403 oracle for a project that is absent.
func (s *server) admitBagDrop(
	ctx context.Context, organizationID, projectID string,
) (*identity.Principal, refusal, error) {
	if _, refused := authorizeTenancy(
		ctx, identity.RoleReader, organizationID, projectID,
	); refused != permitted {
		return nil, refused, nil
	}
	if _, err := s.repository.GetProject(ctx, organizationID, projectID); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			audit.FromContext(ctx).Enrich(audit.Enrichment{Reason: refusedTenancy.reason()})
			return nil, refusedTenancy, nil
		}
		return nil, permitted, err
	}
	caller, refused := authorizeTenancy(
		ctx, identity.RoleMaintainer, organizationID, projectID,
	)
	return caller, refused, nil
}

func (s *server) GetBagDropConfig(
	ctx context.Context, request GetBagDropConfigRequestObject,
) (GetBagDropConfigResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID, projectID := request.OrganizationId.String(), request.ProjectId.String()
	caller, refused, err := s.admitBagDrop(ctx, organizationID, projectID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	config, err := s.bagDrop.Get(ctx, organizationID, projectID)
	if errors.Is(err, bagdrop.ErrNotFound) {
		audited.refused("not_found")
		return GetBagDropConfig404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{Message: "Bag Drop configuration not found"},
		}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded("", "")
	return GetBagDropConfig200JSONResponse(renderBagDropConfig(config)), nil
}

func (s *server) PutBagDropConfig(
	ctx context.Context, request PutBagDropConfigRequestObject,
) (PutBagDropConfigResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	if request.Body == nil {
		audited.refused("invalid_request")
		return PutBagDropConfig400JSONResponse{Message: "Bag Drop configuration is required"}, nil
	}
	if request.Body.HcpPacker.ClientSecret != nil {
		audit.FromContext(ctx).ClientSecret(*request.Body.HcpPacker.ClientSecret)
	}
	organizationID, projectID := request.OrganizationId.String(), request.ProjectId.String()
	caller, refused, err := s.admitBagDrop(ctx, organizationID, projectID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	config, verification, err := s.bagDrop.Put(ctx, organizationID, projectID, bagdrop.Write{
		Adapter: bagdrop.AdapterKind(request.Body.Adapter),
		HCPPacker: bagdrop.HCPPackerConfig{
			OrganizationID: request.Body.HcpPacker.OrganizationId,
			ProjectID:      request.Body.HcpPacker.ProjectId,
			ClientID:       request.Body.HcpPacker.ClientId,
		},
		ClientSecret: request.Body.HcpPacker.ClientSecret,
	})
	switch {
	case errors.Is(err, bagdrop.ErrInvalid):
		audited.refused("invalid_request")
		return PutBagDropConfig400JSONResponse{Message: err.Error()}, nil
	case errors.Is(err, bagdrop.ErrCredentialSeal):
		audited.refused("credential_sealing_unavailable")
		return PutBagDropConfig409JSONResponse{Message: err.Error()}, nil
	case errors.Is(err, bagdrop.ErrResolution):
		audited.refused("resolution_failed")
		return PutBagDropConfig409JSONResponse{
			Message:      "destination did not resolve; configuration unchanged",
			Verification: renderBagDropVerificationPointer(verification),
		}, nil
	case errors.Is(err, bagdrop.ErrNotFound):
		audited.refused("not_found")
		return PutBagDropConfig404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{Message: "project not found"},
		}, nil
	case err != nil:
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded("", "")
	return PutBagDropConfig200JSONResponse(renderBagDropConfig(config)), nil
}

func (s *server) DeleteBagDropConfig(
	ctx context.Context, request DeleteBagDropConfigRequestObject,
) (DeleteBagDropConfigResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID, projectID := request.OrganizationId.String(), request.ProjectId.String()
	caller, refused, err := s.admitBagDrop(ctx, organizationID, projectID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	err = s.bagDrop.Delete(ctx, organizationID, projectID)
	switch {
	case errors.Is(err, bagdrop.ErrEnabled):
		audited.refused("enabled")
		return DeleteBagDropConfig409JSONResponse{
			Message: "Bag Drop is enabled; disable it first",
		}, nil
	case errors.Is(err, bagdrop.ErrNotFound):
		audited.refused("not_found")
		return DeleteBagDropConfig404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{Message: "Bag Drop configuration not found"},
		}, nil
	case err != nil:
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded("", "")
	return DeleteBagDropConfig204Response{}, nil
}

func (s *server) VerifyBagDrop(
	ctx context.Context, request VerifyBagDropRequestObject,
) (VerifyBagDropResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID, projectID := request.OrganizationId.String(), request.ProjectId.String()
	caller, refused, err := s.admitBagDrop(ctx, organizationID, projectID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	verification, err := s.bagDrop.Verify(ctx, organizationID, projectID)
	switch {
	case errors.Is(err, bagdrop.ErrCredentialSeal):
		audited.refused("credential_sealing_unavailable")
		return VerifyBagDrop409JSONResponse{Message: err.Error()}, nil
	case errors.Is(err, bagdrop.ErrNotFound):
		audited.refused("not_found")
		return VerifyBagDrop404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{Message: "Bag Drop configuration not found"},
		}, nil
	case err != nil:
		audited.failed("verification_failed")
		return nil, err
	}
	audited.succeeded("", "")
	return VerifyBagDrop200JSONResponse(renderBagDropVerification(verification)), nil
}

func (s *server) EnableBagDrop(
	ctx context.Context, request EnableBagDropRequestObject,
) (EnableBagDropResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID, projectID := request.OrganizationId.String(), request.ProjectId.String()
	caller, refused, err := s.admitBagDrop(ctx, organizationID, projectID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	config, verification, err := s.bagDrop.Enable(ctx, organizationID, projectID)
	switch {
	case errors.Is(err, bagdrop.ErrCredentialSeal):
		audited.refused("credential_sealing_unavailable")
		return EnableBagDrop409JSONResponse{Message: err.Error()}, nil
	case errors.Is(err, bagdrop.ErrResolution):
		audited.refused("resolution_failed")
		return EnableBagDrop409JSONResponse{
			Message:      "destination did not resolve; configuration remains disabled",
			Verification: renderBagDropVerificationPointer(verification),
		}, nil
	case errors.Is(err, bagdrop.ErrNotFound):
		audited.refused("not_found")
		return EnableBagDrop404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{Message: "Bag Drop configuration not found"},
		}, nil
	case err != nil:
		audited.failed("verification_failed")
		return nil, err
	}
	audited.succeeded("", "")
	return EnableBagDrop200JSONResponse(renderBagDropConfig(config)), nil
}

func (s *server) DisableBagDrop(
	ctx context.Context, request DisableBagDropRequestObject,
) (DisableBagDropResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID, projectID := request.OrganizationId.String(), request.ProjectId.String()
	caller, refused, err := s.admitBagDrop(ctx, organizationID, projectID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	config, err := s.bagDrop.Disable(ctx, organizationID, projectID)
	if errors.Is(err, bagdrop.ErrNotFound) {
		audited.refused("not_found")
		return DisableBagDrop404JSONResponse{
			NotFoundJSONResponse: NotFoundJSONResponse{Message: "Bag Drop configuration not found"},
		}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded("", "")
	return DisableBagDrop200JSONResponse(renderBagDropConfig(config)), nil
}

func renderBagDropConfig(config *bagdrop.Config) BagDropConfig {
	response := BagDropConfig{
		Adapter: BagDropAdapter(config.Adapter),
		HcpPacker: BagDropHCPPacker{
			OrganizationId: config.HCPPacker.OrganizationID,
			ProjectId:      config.HCPPacker.ProjectID,
			ClientId:       config.HCPPacker.ClientID,
		},
		SecretSet: config.SecretSet, Enabled: config.Enabled,
		CreatedAt: config.CreatedAt, UpdatedAt: config.UpdatedAt,
	}
	if config.LastVerification != nil {
		verification := renderBagDropVerification(config.LastVerification.VerificationResult)
		response.LastVerification = &BagDropLastVerification{
			Outcome: verification.Outcome, Reason: verification.Reason,
			Message: verification.Message, VerifiedAt: config.LastVerification.VerifiedAt,
		}
	}
	return response
}

func renderBagDropVerification(result bagdrop.VerificationResult) BagDropVerificationResult {
	response := BagDropVerificationResult{Outcome: BagDropVerificationOutcome(result.Outcome)}
	if result.Reason != "" {
		reason := BagDropVerificationReason(result.Reason)
		response.Reason = &reason
	}
	if result.Message != "" {
		response.Message = &result.Message
	}
	return response
}

func renderBagDropVerificationPointer(result *bagdrop.VerificationResult) *BagDropVerificationResult {
	if result == nil {
		return nil
	}
	rendered := renderBagDropVerification(*result)
	return &rendered
}
