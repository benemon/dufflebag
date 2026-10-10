package v1

import (
	"context"
	"errors"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/webhook"
)

func (s *server) ListOrganizationWebhooks(ctx context.Context, request ListOrganizationWebhooksRequestObject) (ListOrganizationWebhooksResponseObject, error) {
	organizationID, projectID := request.OrganizationId.String(), ""
	if _, refused, err := s.admitOrganization(ctx, identity.RoleMaintainer, organizationID); err != nil {
		return nil, err
	} else if refused != permitted {
		return newRefusal(refused), nil
	}
	records, err := s.webhooks.List(ctx, organizationID, projectID)
	if err != nil {
		return nil, err
	}
	response := ListOrganizationWebhooks200JSONResponse{Webhooks: make([]Webhook, 0, len(records))}
	for i := range records {
		response.Webhooks = append(response.Webhooks, renderWebhook(records[i]))
	}
	return response, nil
}

func (s *server) CreateOrganizationWebhook(ctx context.Context, request CreateOrganizationWebhookRequestObject) (CreateOrganizationWebhookResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID, projectID := request.OrganizationId.String(), ""
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
	if request.Body == nil {
		audited.refused("invalid_request")
		return CreateOrganizationWebhook400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse{Message: "webhook configuration is required"}}, nil
	}
	description := ""
	if request.Body.Description != nil {
		description = *request.Body.Description
	}
	secret := ""
	if request.Body.Secret != nil {
		secret = *request.Body.Secret
		audit.FromContext(ctx).ClientSecret(secret)
	}
	record, err := s.webhooks.Create(ctx, organizationID, projectID, webhook.Create{
		Name: request.Body.Name, URL: request.Body.Url, Description: description,
		Secret: secret, Events: webhookOperations(request.Body.Events),
	})
	if errors.Is(err, webhook.ErrInvalid) {
		audited.refused("invalid_request")
		return CreateOrganizationWebhook400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse{Message: err.Error()}}, nil
	}
	if errors.Is(err, webhook.ErrSealUnavailable) {
		audited.failed("credential_sealing_unavailable")
		return CreateOrganizationWebhook409JSONResponse{Message: err.Error()}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(record.ID, "")
	return CreateOrganizationWebhook201JSONResponse(renderWebhook(*record)), nil
}

func (s *server) GetOrganizationWebhook(ctx context.Context, request GetOrganizationWebhookRequestObject) (GetOrganizationWebhookResponseObject, error) {
	organizationID, projectID := request.OrganizationId.String(), ""
	if _, refused, err := s.admitOrganization(ctx, identity.RoleMaintainer, organizationID); err != nil {
		return nil, err
	} else if refused != permitted {
		return newRefusal(refused), nil
	}
	record, err := s.webhooks.Get(ctx, organizationID, projectID, request.WebhookId.String())
	if errors.Is(err, webhook.ErrNotFound) {
		return GetOrganizationWebhook404JSONResponse{NotFoundJSONResponse: NotFoundJSONResponse{Message: "webhook not found"}}, nil
	}
	if err != nil {
		return nil, err
	}
	return GetOrganizationWebhook200JSONResponse(renderWebhook(*record)), nil
}

func (s *server) UpdateOrganizationWebhook(ctx context.Context, request UpdateOrganizationWebhookRequestObject) (UpdateOrganizationWebhookResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID, projectID := request.OrganizationId.String(), ""
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
	if request.Body == nil {
		audited.refused("invalid_request")
		return UpdateOrganizationWebhook400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse{Message: "webhook update is required"}}, nil
	}
	if request.Body.Secret != nil {
		audit.FromContext(ctx).ClientSecret(*request.Body.Secret)
	}
	var events *[]string
	if request.Body.Events != nil {
		converted := webhookOperations(*request.Body.Events)
		events = &converted
	}
	record, err := s.webhooks.Update(ctx, organizationID, projectID, request.WebhookId.String(), webhook.Update{
		Name: request.Body.Name, URL: request.Body.Url, Description: request.Body.Description,
		Secret: request.Body.Secret, Events: events,
	})
	if errors.Is(err, webhook.ErrInvalid) {
		audited.refused("invalid_request")
		return UpdateOrganizationWebhook400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse{Message: err.Error()}}, nil
	}
	if errors.Is(err, webhook.ErrNotFound) {
		audited.refused("not_found")
		return UpdateOrganizationWebhook404JSONResponse{NotFoundJSONResponse: NotFoundJSONResponse{Message: "webhook not found"}}, nil
	}
	if errors.Is(err, webhook.ErrSealUnavailable) {
		audited.failed("credential_sealing_unavailable")
		return UpdateOrganizationWebhook409JSONResponse{Message: err.Error()}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(record.ID, "")
	return UpdateOrganizationWebhook200JSONResponse(renderWebhook(*record)), nil
}

func (s *server) DeleteOrganizationWebhook(ctx context.Context, request DeleteOrganizationWebhookRequestObject) (DeleteOrganizationWebhookResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID, projectID := request.OrganizationId.String(), ""
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
	err = s.webhooks.Delete(ctx, organizationID, projectID, request.WebhookId.String())
	if errors.Is(err, webhook.ErrNotFound) {
		audited.refused("not_found")
		return DeleteOrganizationWebhook404JSONResponse{NotFoundJSONResponse: NotFoundJSONResponse{Message: "webhook not found"}}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(request.WebhookId.String(), "")
	return DeleteOrganizationWebhook204Response{}, nil
}

func (s *server) VerifyOrganizationWebhook(ctx context.Context, request VerifyOrganizationWebhookRequestObject) (VerifyOrganizationWebhookResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID, projectID := request.OrganizationId.String(), ""
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
	record, err := s.webhooks.Verify(ctx, organizationID, projectID, request.WebhookId.String())
	if errors.Is(err, webhook.ErrNotFound) {
		audited.refused("not_found")
		return VerifyOrganizationWebhook404JSONResponse{NotFoundJSONResponse: NotFoundJSONResponse{Message: "webhook not found"}}, nil
	}
	if err != nil {
		audited.failed("verification_failed")
		return nil, err
	}
	audited.succeeded(record.ID, "")
	return VerifyOrganizationWebhook200JSONResponse(renderWebhook(*record)), nil
}

func (s *server) ListOrganizationWebhookDeliveries(ctx context.Context, request ListOrganizationWebhookDeliveriesRequestObject) (ListOrganizationWebhookDeliveriesResponseObject, error) {
	organizationID, projectID := request.OrganizationId.String(), ""
	if _, refused, err := s.admitOrganization(ctx, identity.RoleMaintainer, organizationID); err != nil {
		return nil, err
	} else if refused != permitted {
		return newRefusal(refused), nil
	}
	deliveries, err := s.webhooks.Deliveries(ctx, organizationID, projectID, request.WebhookId.String())
	if errors.Is(err, webhook.ErrNotFound) {
		return ListOrganizationWebhookDeliveries404JSONResponse{NotFoundJSONResponse: NotFoundJSONResponse{Message: "webhook not found"}}, nil
	}
	if err != nil {
		return nil, err
	}
	response := ListOrganizationWebhookDeliveries200JSONResponse{Deliveries: make([]WebhookDelivery, 0, len(deliveries))}
	for i := range deliveries {
		response.Deliveries = append(response.Deliveries, renderWebhookDelivery(deliveries[i]))
	}
	return response, nil
}
