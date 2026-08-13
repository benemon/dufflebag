package v1

import (
	"context"
	"errors"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/webhook"
	"github.com/google/uuid"
)

func (s *server) ListWebhooks(ctx context.Context, request ListWebhooksRequestObject) (ListWebhooksResponseObject, error) {
	organizationID, projectID := request.OrganizationId.String(), request.ProjectId.String()
	if _, refused, err := s.admitBagDrop(ctx, organizationID, projectID); err != nil {
		return nil, err
	} else if refused != permitted {
		return newRefusal(refused), nil
	}
	records, err := s.webhooks.List(ctx, organizationID, projectID)
	if err != nil {
		return nil, err
	}
	response := ListWebhooks200JSONResponse{Webhooks: make([]Webhook, 0, len(records))}
	for i := range records {
		response.Webhooks = append(response.Webhooks, renderWebhook(records[i]))
	}
	return response, nil
}

func (s *server) CreateWebhook(ctx context.Context, request CreateWebhookRequestObject) (CreateWebhookResponseObject, error) {
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
	if request.Body == nil {
		audited.refused("invalid_request")
		return CreateWebhook400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse{Message: "webhook configuration is required"}}, nil
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
		return CreateWebhook400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse{Message: err.Error()}}, nil
	}
	if errors.Is(err, webhook.ErrSealUnavailable) {
		audited.failed("credential_sealing_unavailable")
		return CreateWebhook409JSONResponse{Message: err.Error()}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(record.ID, "")
	return CreateWebhook201JSONResponse(renderWebhook(*record)), nil
}

func (s *server) GetWebhook(ctx context.Context, request GetWebhookRequestObject) (GetWebhookResponseObject, error) {
	organizationID, projectID := request.OrganizationId.String(), request.ProjectId.String()
	if _, refused, err := s.admitBagDrop(ctx, organizationID, projectID); err != nil {
		return nil, err
	} else if refused != permitted {
		return newRefusal(refused), nil
	}
	record, err := s.webhooks.Get(ctx, organizationID, projectID, request.WebhookId.String())
	if errors.Is(err, webhook.ErrNotFound) {
		return GetWebhook404JSONResponse{NotFoundJSONResponse: NotFoundJSONResponse{Message: "webhook not found"}}, nil
	}
	if err != nil {
		return nil, err
	}
	return GetWebhook200JSONResponse(renderWebhook(*record)), nil
}

func (s *server) UpdateWebhook(ctx context.Context, request UpdateWebhookRequestObject) (UpdateWebhookResponseObject, error) {
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
	if request.Body == nil {
		audited.refused("invalid_request")
		return UpdateWebhook400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse{Message: "webhook update is required"}}, nil
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
		return UpdateWebhook400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse{Message: err.Error()}}, nil
	}
	if errors.Is(err, webhook.ErrNotFound) {
		audited.refused("not_found")
		return UpdateWebhook404JSONResponse{NotFoundJSONResponse: NotFoundJSONResponse{Message: "webhook not found"}}, nil
	}
	if errors.Is(err, webhook.ErrSealUnavailable) {
		audited.failed("credential_sealing_unavailable")
		return UpdateWebhook409JSONResponse{Message: err.Error()}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(record.ID, "")
	return UpdateWebhook200JSONResponse(renderWebhook(*record)), nil
}

func (s *server) DeleteWebhook(ctx context.Context, request DeleteWebhookRequestObject) (DeleteWebhookResponseObject, error) {
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
	err = s.webhooks.Delete(ctx, organizationID, projectID, request.WebhookId.String())
	if errors.Is(err, webhook.ErrNotFound) {
		audited.refused("not_found")
		return DeleteWebhook404JSONResponse{NotFoundJSONResponse: NotFoundJSONResponse{Message: "webhook not found"}}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(request.WebhookId.String(), "")
	return DeleteWebhook204Response{}, nil
}

func (s *server) VerifyWebhook(ctx context.Context, request VerifyWebhookRequestObject) (VerifyWebhookResponseObject, error) {
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
	record, err := s.webhooks.Verify(ctx, organizationID, projectID, request.WebhookId.String())
	if errors.Is(err, webhook.ErrNotFound) {
		audited.refused("not_found")
		return VerifyWebhook404JSONResponse{NotFoundJSONResponse: NotFoundJSONResponse{Message: "webhook not found"}}, nil
	}
	if err != nil {
		audited.failed("verification_failed")
		return nil, err
	}
	audited.succeeded(record.ID, "")
	return VerifyWebhook200JSONResponse(renderWebhook(*record)), nil
}

func (s *server) ListWebhookDeliveries(ctx context.Context, request ListWebhookDeliveriesRequestObject) (ListWebhookDeliveriesResponseObject, error) {
	organizationID, projectID := request.OrganizationId.String(), request.ProjectId.String()
	if _, refused, err := s.admitBagDrop(ctx, organizationID, projectID); err != nil {
		return nil, err
	} else if refused != permitted {
		return newRefusal(refused), nil
	}
	deliveries, err := s.webhooks.Deliveries(ctx, organizationID, projectID, request.WebhookId.String())
	if errors.Is(err, webhook.ErrNotFound) {
		return ListWebhookDeliveries404JSONResponse{NotFoundJSONResponse: NotFoundJSONResponse{Message: "webhook not found"}}, nil
	}
	if err != nil {
		return nil, err
	}
	response := ListWebhookDeliveries200JSONResponse{Deliveries: make([]WebhookDelivery, 0, len(deliveries))}
	for i := range deliveries {
		response.Deliveries = append(response.Deliveries, renderWebhookDelivery(deliveries[i]))
	}
	return response, nil
}

func renderWebhook(record webhook.Record) Webhook {
	events := make([]WebhookOperation, 0, len(record.Events))
	for _, operation := range record.Events {
		events = append(events, WebhookOperation(operation))
	}
	return Webhook{
		Id: uuid.MustParse(record.ID), Name: record.Name, Url: record.URL, Description: record.Description,
		HasSecret: len(record.SealedSecret) != 0, Events: events, State: WebhookState(record.State),
		LastVerificationAt: record.LastVerificationAt, LastVerificationError: record.LastVerificationError,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

func renderWebhookDelivery(delivery webhook.Delivery) WebhookDelivery {
	return WebhookDelivery{
		Id: uuid.MustParse(delivery.ID), EventId: delivery.EventID, Operation: delivery.Operation,
		Status: WebhookDeliveryStatus(delivery.Status), AttemptCount: delivery.AttemptCount,
		FirstAttemptedAt: delivery.FirstAttemptedAt, LastAttemptedAt: delivery.LastAttemptedAt,
		ResponseCode: delivery.ResponseCode, Detail: delivery.Detail, CreatedAt: delivery.CreatedAt,
	}
}

func webhookOperations(values []WebhookOperation) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}
