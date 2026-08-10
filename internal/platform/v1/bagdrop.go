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

func (s *server) admitBagDropStatus(
	ctx context.Context, organizationID, projectID string,
) (*identity.Principal, refusal, error) {
	caller, refused := authorizeTenancy(
		ctx, identity.RoleReader, organizationID, projectID,
	)
	if refused != permitted {
		return nil, refused, nil
	}
	if _, err := s.repository.GetProject(ctx, organizationID, projectID); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			audit.FromContext(ctx).Enrich(audit.Enrichment{Reason: refusedTenancy.reason()})
			return nil, refusedTenancy, nil
		}
		return nil, permitted, err
	}
	return caller, permitted, nil
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
	case errors.Is(err, bagdrop.ErrCleanupPending):
		audited.refused("cleanup_pending")
		return DeleteBagDropConfig409JSONResponse{
			Message: "Bag Drop destination cleanup is pending",
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

func (s *server) ListBagDropAssociations(
	ctx context.Context, request ListBagDropAssociationsRequestObject,
) (ListBagDropAssociationsResponseObject, error) {
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
	associations, err := s.bagDrop.ListAssociations(ctx, organizationID, projectID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	response := ListBagDropAssociations200JSONResponse{
		Associations: make([]BagDropAssociation, 0, len(associations)),
	}
	for _, association := range associations {
		response.Associations = append(response.Associations, renderBagDropAssociation(association))
	}
	audited.succeeded("", "")
	return response, nil
}

func (s *server) SetBagDropAssociation(
	ctx context.Context, request SetBagDropAssociationRequestObject,
) (SetBagDropAssociationResponseObject, error) {
	audited := s.beginLifecycleAudit()
	audited.event.TargetID = request.BucketName
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
	association, err := s.bagDrop.Associate(ctx, organizationID, projectID, request.BucketName)
	switch {
	case errors.Is(err, bagdrop.ErrNotFound):
		audited.refused("not_configured")
		return SetBagDropAssociation404JSONResponse{Message: "Bag Drop is not configured"}, nil
	case errors.Is(err, bagdrop.ErrBucketNotFound):
		audited.refused("not_found")
		return SetBagDropAssociation404JSONResponse{Message: "bucket not found"}, nil
	case err != nil:
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(request.BucketName, "")
	return SetBagDropAssociation200JSONResponse(renderBagDropAssociation(*association)), nil
}

func (s *server) DeleteBagDropAssociation(
	ctx context.Context, request DeleteBagDropAssociationRequestObject,
) (DeleteBagDropAssociationResponseObject, error) {
	audited := s.beginLifecycleAudit()
	audited.event.TargetID = request.BucketName
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
	outcome, err := s.bagDrop.Unassociate(ctx, organizationID, projectID, request.BucketName)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(request.BucketName, string(outcome))
	return DeleteBagDropAssociation204Response{}, nil
}

func (s *server) GetBagDropStatus(
	ctx context.Context, request GetBagDropStatusRequestObject,
) (GetBagDropStatusResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID, projectID := request.OrganizationId.String(), request.ProjectId.String()
	caller, refused, err := s.admitBagDropStatus(ctx, organizationID, projectID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	status, err := s.bagDrop.Status(ctx, organizationID, projectID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded("", "")
	return GetBagDropStatus200JSONResponse(renderBagDropStatus(status)), nil
}

func renderBagDropAssociation(association bagdrop.Association) BagDropAssociation {
	return BagDropAssociation{
		BucketName:       association.BucketName,
		State:            BagDropAssociationState(association.State),
		FirstAttemptedAt: association.FirstAttemptedAt,
		LastAttemptAt:    association.LastAttemptAt,
		LastSyncedAt:     association.LastSyncedAt,
		LastSyncError:    association.LastSyncError,
		CreatedAt:        association.CreatedAt,
		UpdatedAt:        association.UpdatedAt,
		SyncStatus:       BagDropSyncStatus(association.SyncStatus()),
	}
}

func renderBagDropStatus(status *bagdrop.Status) BagDropStatus {
	response := BagDropStatus{
		Configured:   status.Configured,
		Associations: make([]BagDropAssociation, 0, len(status.Associations)),
	}
	for _, association := range status.Associations {
		response.Associations = append(response.Associations, renderBagDropAssociation(association))
	}
	if status.Config != nil {
		adapter := BagDropAdapter(status.Config.Adapter)
		response.Adapter = &adapter
		response.Enabled = &status.Config.Enabled
		response.LastVerification = renderBagDropConfig(status.Config).LastVerification
	}
	return response
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

func (s *server) ReconcileBagDrop(
	ctx context.Context, request ReconcileBagDropRequestObject,
) (ReconcileBagDropResponseObject, error) {
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
	reconciler, ok := s.bagDrop.(BagDropReconciler)
	if !ok {
		audited.failed("reconciler_unavailable")
		return ReconcileBagDrop503JSONResponse{Message: bagdrop.ErrReconcilerNotRunning.Error()}, nil
	}
	if err := reconciler.Trigger(ctx, organizationID, projectID); err != nil {
		if errors.Is(err, bagdrop.ErrReconcilerNotRunning) {
			audited.failed("reconciler_unavailable")
			return ReconcileBagDrop503JSONResponse{Message: err.Error()}, nil
		}
		audited.failed("trigger_failed")
		return nil, err
	}
	audited.succeeded("", "")
	return ReconcileBagDrop202JSONResponse{Message: "Bag Drop reconciliation requested"}, nil
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
