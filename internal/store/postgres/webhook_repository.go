package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/benemon/dufflebag/internal/store/postgres/postgresdb"
	"github.com/benemon/dufflebag/internal/webhook"
	"github.com/google/uuid"
)

func (r *Repository) CreateWebhook(ctx context.Context, record webhook.Record) (*webhook.Record, error) {
	tenant := ParseTenant(record.OrganizationID, record.ProjectID)
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.CreateWebhook(ctx, postgresdb.CreateWebhookParams{
		OrganizationID: tenant.OrganizationID, ProjectID: tenant.ProjectID,
		ID: uuid.MustParse(record.ID), Name: record.Name, Url: record.URL,
		Description: record.Description, SealedSecret: record.SealedSecret,
		Events: record.Events, CreatedAt: record.CreatedAt,
	})
	if err != nil {
		return nil, fmt.Errorf("create webhook: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create webhook: %w", err)
	}
	return restoreWebhook(row), nil
}

func (r *Repository) GetWebhook(ctx context.Context, organizationID, projectID, webhookID string) (*webhook.Record, error) {
	tx, q, err := r.begin(ctx, ParseTenant(organizationID, projectID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := uuid.Parse(webhookID)
	if err != nil {
		return nil, webhook.ErrNotFound
	}
	row, err := q.GetWebhook(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, webhook.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get webhook: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit get webhook: %w", err)
	}
	return restoreWebhook(row), nil
}

func (r *Repository) ListWebhooks(ctx context.Context, organizationID, projectID string) ([]webhook.Record, error) {
	tx, q, err := r.begin(ctx, ParseTenant(organizationID, projectID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := q.ListWebhooks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	records := make([]webhook.Record, 0, len(rows))
	for _, row := range rows {
		records = append(records, *restoreWebhook(row))
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit list webhooks: %w", err)
	}
	return records, nil
}

func (r *Repository) UpdateWebhook(ctx context.Context, record webhook.Record) (*webhook.Record, error) {
	tx, q, err := r.begin(ctx, ParseTenant(record.OrganizationID, record.ProjectID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.UpdateWebhook(ctx, postgresdb.UpdateWebhookParams{
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
		return nil, fmt.Errorf("update webhook: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit update webhook: %w", err)
	}
	return restoreWebhook(row), nil
}

func (r *Repository) DeleteWebhook(ctx context.Context, organizationID, projectID, webhookID string) error {
	tx, q, err := r.begin(ctx, ParseTenant(organizationID, projectID))
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := uuid.Parse(webhookID)
	if err != nil {
		return webhook.ErrNotFound
	}
	deleted, err := q.DeleteWebhook(ctx, id)
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	if deleted == 0 {
		return webhook.ErrNotFound
	}
	return tx.Commit()
}

func (r *Repository) RecordWebhookVerification(
	ctx context.Context, record webhook.Record, eventID, status string,
	responseCode *int, detail *string, at time.Time,
) (*webhook.Record, error) {
	tenant := ParseTenant(record.OrganizationID, record.ProjectID)
	tx, q, err := r.begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.RecordWebhookVerification(ctx, postgresdb.RecordWebhookVerificationParams{
		ID: uuid.MustParse(record.ID), State: record.State,
		LastVerificationAt:    sql.NullTime{Time: at, Valid: true},
		LastVerificationError: nullableString(record.LastVerificationError),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, webhook.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("record webhook verification: %w", err)
	}
	delivery, err := q.CreateWebhookDelivery(ctx, postgresdb.CreateWebhookDeliveryParams{
		OrganizationID: tenant.OrganizationID, ProjectID: tenant.ProjectID,
		ID: uuid.New(), WebhookID: uuid.MustParse(record.ID), EventID: eventID,
		Operation: webhook.OperationVerification, NextAttemptAt: sql.NullTime{Time: at, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("create webhook verification delivery: %w", err)
	}
	if _, err := q.RecordWebhookDeliveryAttempt(ctx, postgresdb.RecordWebhookDeliveryAttemptParams{
		ID: delivery.ID, Status: status, AttemptCount: 1,
		LastAttemptedAt: sql.NullTime{Time: at, Valid: true},
		ResponseCode:    nullableInt32(responseCode), Detail: nullableString(detail),
	}); err != nil {
		return nil, fmt.Errorf("record webhook verification delivery: %w", err)
	}
	if err := q.PruneWebhookDeliveries(ctx, delivery.WebhookID); err != nil {
		return nil, fmt.Errorf("prune webhook deliveries: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit webhook verification: %w", err)
	}
	return restoreWebhook(row), nil
}

func (r *Repository) ListWebhookDeliveries(ctx context.Context, organizationID, projectID, webhookID string) ([]webhook.Delivery, error) {
	tx, q, err := r.begin(ctx, ParseTenant(organizationID, projectID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := uuid.Parse(webhookID)
	if err != nil {
		return nil, webhook.ErrNotFound
	}
	if _, err := q.GetWebhook(ctx, id); errors.Is(err, sql.ErrNoRows) {
		return nil, webhook.ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("get webhook for deliveries: %w", err)
	}
	rows, err := q.ListWebhookDeliveries(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list webhook deliveries: %w", err)
	}
	deliveries := make([]webhook.Delivery, 0, len(rows))
	for _, row := range rows {
		deliveries = append(deliveries, restoreWebhookDelivery(row))
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit list webhook deliveries: %w", err)
	}
	return deliveries, nil
}

func (r *Repository) ListWebhookProjects(ctx context.Context) ([]webhook.Project, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT organization_id::text, id::text FROM projects ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list webhook projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var projects []webhook.Project
	for rows.Next() {
		var project webhook.Project
		if err := rows.Scan(&project.OrganizationID, &project.ProjectID); err != nil {
			return nil, fmt.Errorf("scan webhook project: %w", err)
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

func (r *Repository) GetNextWebhookOutboxEvent(ctx context.Context, project webhook.Project, at time.Time) (*webhook.OutboxEvent, error) {
	tx, q, err := r.begin(ctx, ParseTenant(project.OrganizationID, project.ProjectID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.GetNextWebhookOutboxEvent(ctx, at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get webhook outbox event: %w", err)
	}
	event, err := restoreWebhookOutbox(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return event, nil
}

func (r *Repository) ListWebhookEventDeliveries(ctx context.Context, project webhook.Project, eventID string) ([]webhook.Delivery, error) {
	tx, q, err := r.begin(ctx, ParseTenant(project.OrganizationID, project.ProjectID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := q.ListWebhookEventDeliveries(ctx, eventID)
	if err != nil {
		return nil, err
	}
	deliveries := make([]webhook.Delivery, 0, len(rows))
	for _, row := range rows {
		deliveries = append(deliveries, restoreWebhookDelivery(row))
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return deliveries, nil
}

func (r *Repository) RecordWebhookDeliveryAttempt(ctx context.Context, delivery webhook.Delivery) error {
	tx, q, err := r.begin(ctx, ParseTenant(delivery.OrganizationID, delivery.ProjectID))
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = q.RecordWebhookDeliveryAttempt(ctx, postgresdb.RecordWebhookDeliveryAttemptParams{
		ID: uuid.MustParse(delivery.ID), Status: delivery.Status, AttemptCount: int32(delivery.AttemptCount),
		LastAttemptedAt: nullableTime(delivery.LastAttemptedAt), NextAttemptAt: nullableTime(delivery.NextAttemptAt),
		ResponseCode: nullableInt32(delivery.ResponseCode), Detail: nullableString(delivery.Detail),
	})
	if err != nil {
		return fmt.Errorf("record webhook delivery attempt: %w", err)
	}
	if err := q.PruneWebhookDeliveries(ctx, uuid.MustParse(delivery.WebhookID)); err != nil {
		return fmt.Errorf("prune webhook deliveries: %w", err)
	}
	return tx.Commit()
}

func (r *Repository) ScheduleWebhookOutboxEvent(ctx context.Context, project webhook.Project, eventID string, at time.Time) error {
	tx, q, err := r.begin(ctx, ParseTenant(project.OrganizationID, project.ProjectID))
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := q.SetWebhookOutboxAvailableAt(ctx, postgresdb.SetWebhookOutboxAvailableAtParams{EventID: eventID, AvailableAt: at}); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) DeleteWebhookOutboxEvent(ctx context.Context, project webhook.Project, eventID string) error {
	tx, q, err := r.begin(ctx, ParseTenant(project.OrganizationID, project.ProjectID))
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := q.DeleteWebhookOutboxEvent(ctx, eventID); err != nil {
		return err
	}
	return tx.Commit()
}

func restoreWebhook(row postgresdb.Webhook) *webhook.Record {
	return &webhook.Record{
		OrganizationID: row.OrganizationID.String(), ProjectID: row.ProjectID.String(), ID: row.ID.String(),
		Name: row.Name, URL: row.Url, Description: row.Description,
		SealedSecret: append([]byte(nil), row.SealedSecret...), Events: append([]string(nil), row.Events...),
		State: row.State, LastVerificationAt: timePointer(row.LastVerificationAt),
		LastVerificationError: stringPointer(row.LastVerificationError), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func restoreWebhookDelivery(row postgresdb.WebhookDelivery) webhook.Delivery {
	return webhook.Delivery{
		OrganizationID: row.OrganizationID.String(), ProjectID: row.ProjectID.String(), ID: row.ID.String(),
		WebhookID: row.WebhookID.String(), EventID: row.EventID, Operation: row.Operation,
		Status: row.Status, AttemptCount: int(row.AttemptCount),
		FirstAttemptedAt: timePointer(row.FirstAttemptedAt), LastAttemptedAt: timePointer(row.LastAttemptedAt),
		NextAttemptAt: timePointer(row.NextAttemptAt), ResponseCode: intPointer(row.ResponseCode),
		Detail: stringPointer(row.Detail), CreatedAt: row.CreatedAt,
	}
}

func restoreWebhookOutbox(row postgresdb.WebhookOutbox) (*webhook.OutboxEvent, error) {
	var target webhook.Target
	var actor webhook.Actor
	if err := json.Unmarshal(row.Target, &target); err != nil {
		return nil, fmt.Errorf("unmarshal webhook target: %w", err)
	}
	if err := json.Unmarshal(row.Actor, &actor); err != nil {
		return nil, fmt.Errorf("unmarshal webhook actor: %w", err)
	}
	return &webhook.OutboxEvent{Envelope: webhook.Envelope{
		EventID: row.EventID, OccurredAt: row.OccurredAt, OrganizationID: row.OrganizationID.String(),
		ProjectID: row.ProjectID.String(), Operation: row.Operation, Target: target, Actor: actor,
		Payload: append(json.RawMessage(nil), row.Payload...),
	}, AvailableAt: row.AvailableAt}, nil
}

func nullableInt32(value *int) sql.NullInt32 {
	if value == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: int32(*value), Valid: true}
}

func intPointer(value sql.NullInt32) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int32)
	return &result
}
