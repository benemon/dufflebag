package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/benemon/dufflebag/internal/webhook"
	"github.com/google/uuid"
)

// Organization webhooks are webhook records with no project. They live in
// their own tables under organization-only RLS, so the project policies,
// which also match app.tenant_project, are left as they are.

func (r *Repository) beginWebhookOrganization(ctx context.Context, organizationID string) (*sql.Tx, *postgresdb.Queries, OrganizationTenant, error) {
	tenant := ParseOrganizationTenant(organizationID)
	tx, q, err := r.beginOrganization(ctx, tenant)
	return tx, q, tenant, err
}

func (r *Repository) createOrganizationWebhook(ctx context.Context, record webhook.Record) (*webhook.Record, error) {
	tx, q, tenant, err := r.beginWebhookOrganization(ctx, record.OrganizationID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.CreateOrganizationWebhook(ctx, postgresdb.CreateOrganizationWebhookParams{
		OrganizationID: tenant.OrganizationID, ID: uuid.MustParse(record.ID), Name: record.Name, Url: record.URL,
		Description: record.Description, SealedSecret: record.SealedSecret, Events: record.Events, CreatedAt: record.CreatedAt,
	})
	if err != nil {
		return nil, fmt.Errorf("create organization webhook: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create organization webhook: %w", err)
	}
	return restoreOrganizationWebhook(row), nil
}

func (r *Repository) getOrganizationWebhook(ctx context.Context, organizationID, webhookID string) (*webhook.Record, error) {
	tx, q, _, err := r.beginWebhookOrganization(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := uuid.Parse(webhookID)
	if err != nil {
		return nil, webhook.ErrNotFound
	}
	row, err := q.GetOrganizationWebhook(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, webhook.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get organization webhook: %w", err)
	}
	return restoreOrganizationWebhook(row), tx.Commit()
}

func (r *Repository) listOrganizationWebhooks(ctx context.Context, organizationID string) ([]webhook.Record, error) {
	tx, q, _, err := r.beginWebhookOrganization(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := q.ListOrganizationWebhooks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organization webhooks: %w", err)
	}
	records := make([]webhook.Record, 0, len(rows))
	for _, row := range rows {
		records = append(records, *restoreOrganizationWebhook(row))
	}
	return records, tx.Commit()
}

func (r *Repository) updateOrganizationWebhook(ctx context.Context, record webhook.Record) (*webhook.Record, error) {
	tx, q, _, err := r.beginWebhookOrganization(ctx, record.OrganizationID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.UpdateOrganizationWebhook(ctx, postgresdb.UpdateOrganizationWebhookParams{
		ID: uuid.MustParse(record.ID), Name: record.Name, Url: record.URL,
		Description: record.Description, SealedSecret: record.SealedSecret,
		Events: record.Events, State: record.State,
		LastVerificationAt:    nullableTime(record.LastVerificationAt),
		LastVerificationError: nullableString(record.LastVerificationError), UpdatedAt: record.UpdatedAt,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, webhook.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update organization webhook: %w", err)
	}
	return restoreOrganizationWebhook(row), tx.Commit()
}

func (r *Repository) deleteOrganizationWebhook(ctx context.Context, organizationID, webhookID string) error {
	tx, q, _, err := r.beginWebhookOrganization(ctx, organizationID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := uuid.Parse(webhookID)
	if err != nil {
		return webhook.ErrNotFound
	}
	deleted, err := q.DeleteOrganizationWebhook(ctx, id)
	if err != nil {
		return fmt.Errorf("delete organization webhook: %w", err)
	}
	if deleted == 0 {
		return webhook.ErrNotFound
	}
	return tx.Commit()
}

func (r *Repository) recordOrganizationWebhookVerification(
	ctx context.Context, record webhook.Record, eventID, status string, responseCode *int, detail *string, at time.Time,
) (*webhook.Record, error) {
	tx, q, tenant, err := r.beginWebhookOrganization(ctx, record.OrganizationID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.RecordOrganizationWebhookVerification(ctx, postgresdb.RecordOrganizationWebhookVerificationParams{
		ID: uuid.MustParse(record.ID), State: record.State,
		LastVerificationAt:    sql.NullTime{Time: at, Valid: true},
		LastVerificationError: nullableString(record.LastVerificationError),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, webhook.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("record organization webhook verification: %w", err)
	}
	delivery, err := q.CreateOrganizationWebhookDelivery(ctx, postgresdb.CreateOrganizationWebhookDeliveryParams{
		OrganizationID: tenant.OrganizationID, ID: uuid.New(), WebhookID: uuid.MustParse(record.ID), EventID: eventID,
		Operation: webhook.OperationVerification, NextAttemptAt: sql.NullTime{Time: at, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("create organization webhook verification delivery: %w", err)
	}
	if _, err := q.RecordOrganizationWebhookDeliveryAttempt(ctx, postgresdb.RecordOrganizationWebhookDeliveryAttemptParams{
		ID: delivery.ID, Status: status, AttemptCount: 1,
		LastAttemptedAt: sql.NullTime{Time: at, Valid: true},
		ResponseCode:    nullableInt32(responseCode), Detail: nullableString(detail),
	}); err != nil {
		return nil, fmt.Errorf("record organization webhook verification delivery: %w", err)
	}
	if err := q.PruneOrganizationWebhookDeliveries(ctx, delivery.WebhookID); err != nil {
		return nil, fmt.Errorf("prune organization webhook deliveries: %w", err)
	}
	return restoreOrganizationWebhook(row), tx.Commit()
}

func (r *Repository) listOrganizationWebhookDeliveries(ctx context.Context, organizationID, webhookID string) ([]webhook.Delivery, error) {
	tx, q, _, err := r.beginWebhookOrganization(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := uuid.Parse(webhookID)
	if err != nil {
		return nil, webhook.ErrNotFound
	}
	if _, err := q.GetOrganizationWebhook(ctx, id); errors.Is(err, sql.ErrNoRows) {
		return nil, webhook.ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("get organization webhook for deliveries: %w", err)
	}
	rows, err := q.ListOrganizationWebhookDeliveries(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list organization webhook deliveries: %w", err)
	}
	deliveries := make([]webhook.Delivery, 0, len(rows))
	for _, row := range rows {
		deliveries = append(deliveries, restoreOrganizationWebhookDelivery(row))
	}
	return deliveries, tx.Commit()
}

func (r *Repository) getNextOrganizationWebhookOutboxEvent(ctx context.Context, organizationID string, at time.Time) (*webhook.OutboxEvent, error) {
	tx, q, _, err := r.beginWebhookOrganization(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.GetNextOrganizationWebhookOutboxEvent(ctx, at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get organization webhook outbox event: %w", err)
	}
	event, err := restoreOrganizationWebhookOutbox(row)
	if err != nil {
		return nil, err
	}
	return event, tx.Commit()
}

func (r *Repository) listOrganizationWebhookEventDeliveries(ctx context.Context, organizationID, eventID string) ([]webhook.Delivery, error) {
	tx, q, _, err := r.beginWebhookOrganization(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := q.ListOrganizationWebhookEventDeliveries(ctx, eventID)
	if err != nil {
		return nil, err
	}
	deliveries := make([]webhook.Delivery, 0, len(rows))
	for _, row := range rows {
		deliveries = append(deliveries, restoreOrganizationWebhookDelivery(row))
	}
	return deliveries, tx.Commit()
}

func (r *Repository) recordOrganizationWebhookDeliveryAttempt(ctx context.Context, delivery webhook.Delivery) error {
	tx, q, _, err := r.beginWebhookOrganization(ctx, delivery.OrganizationID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := q.RecordOrganizationWebhookDeliveryAttempt(ctx, postgresdb.RecordOrganizationWebhookDeliveryAttemptParams{
		ID: uuid.MustParse(delivery.ID), Status: delivery.Status, AttemptCount: int32(delivery.AttemptCount),
		LastAttemptedAt: nullableTime(delivery.LastAttemptedAt), NextAttemptAt: nullableTime(delivery.NextAttemptAt),
		ResponseCode: nullableInt32(delivery.ResponseCode), Detail: nullableString(delivery.Detail),
	}); err != nil {
		return fmt.Errorf("record organization webhook delivery attempt: %w", err)
	}
	if err := q.PruneOrganizationWebhookDeliveries(ctx, uuid.MustParse(delivery.WebhookID)); err != nil {
		return fmt.Errorf("prune organization webhook deliveries: %w", err)
	}
	return tx.Commit()
}

func (r *Repository) scheduleOrganizationWebhookOutboxEvent(ctx context.Context, organizationID, eventID string, at time.Time) error {
	tx, q, _, err := r.beginWebhookOrganization(ctx, organizationID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := q.SetOrganizationWebhookOutboxAvailableAt(ctx, postgresdb.SetOrganizationWebhookOutboxAvailableAtParams{EventID: eventID, AvailableAt: at}); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) deleteOrganizationWebhookOutboxEvent(ctx context.Context, organizationID, eventID string) error {
	tx, q, _, err := r.beginWebhookOrganization(ctx, organizationID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := q.DeleteOrganizationWebhookOutboxEvent(ctx, eventID); err != nil {
		return err
	}
	return tx.Commit()
}

// enqueueOrganizationWebhookEvent writes an event in the caller's transaction,
// so it commits or rolls back with the change it reports.
func enqueueOrganizationWebhookEvent(
	ctx context.Context, q *postgresdb.Queries, tenant OrganizationTenant, operation string,
	target webhook.Target, payload any, occurredAt time.Time,
) error {
	actor := webhook.Actor{PrincipalID: "system:dufflebag", Name: "Dufflebag"}
	targetJSON, err := json.Marshal(target)
	if err != nil {
		return fmt.Errorf("marshal organization webhook target: %w", err)
	}
	actorJSON, err := json.Marshal(actor)
	if err != nil {
		return fmt.Errorf("marshal organization webhook actor: %w", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal organization webhook payload: %w", err)
	}
	if err := q.EnqueueOrganizationWebhookEvent(ctx, postgresdb.EnqueueOrganizationWebhookEventParams{
		OrganizationID: tenant.OrganizationID, EventID: registry.NewID(occurredAt).String(), OccurredAt: occurredAt,
		Operation: operation, Target: targetJSON, Actor: actorJSON, Payload: payloadJSON,
	}); err != nil {
		return fmt.Errorf("enqueue %s organization webhook event: %w", operation, err)
	}
	return nil
}

func restoreOrganizationWebhook(row postgresdb.OrganizationWebhook) *webhook.Record {
	return &webhook.Record{
		OrganizationID: row.OrganizationID.String(), ID: row.ID.String(),
		Name: row.Name, URL: row.Url, Description: row.Description,
		SealedSecret: append([]byte(nil), row.SealedSecret...), Events: append([]string(nil), row.Events...),
		State: row.State, LastVerificationAt: timePointer(row.LastVerificationAt),
		LastVerificationError: stringPointer(row.LastVerificationError), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func restoreOrganizationWebhookDelivery(row postgresdb.OrganizationWebhookDelivery) webhook.Delivery {
	return webhook.Delivery{
		OrganizationID: row.OrganizationID.String(), ID: row.ID.String(),
		WebhookID: row.WebhookID.String(), EventID: row.EventID, Operation: row.Operation,
		Status: row.Status, AttemptCount: int(row.AttemptCount),
		FirstAttemptedAt: timePointer(row.FirstAttemptedAt), LastAttemptedAt: timePointer(row.LastAttemptedAt),
		NextAttemptAt: timePointer(row.NextAttemptAt), ResponseCode: intPointer(row.ResponseCode),
		Detail: stringPointer(row.Detail), CreatedAt: row.CreatedAt,
	}
}

func restoreOrganizationWebhookOutbox(row postgresdb.OrganizationWebhookOutbox) (*webhook.OutboxEvent, error) {
	var target webhook.Target
	var actor webhook.Actor
	if err := json.Unmarshal(row.Target, &target); err != nil {
		return nil, fmt.Errorf("unmarshal organization webhook target: %w", err)
	}
	if err := json.Unmarshal(row.Actor, &actor); err != nil {
		return nil, fmt.Errorf("unmarshal organization webhook actor: %w", err)
	}
	return &webhook.OutboxEvent{Envelope: webhook.Envelope{
		EventID: row.EventID, OccurredAt: row.OccurredAt, OrganizationID: row.OrganizationID.String(),
		Operation: row.Operation, Target: target, Actor: actor, Payload: append(json.RawMessage(nil), row.Payload...),
	}, AvailableAt: row.AvailableAt}, nil
}
