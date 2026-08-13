package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/benemon/dufflebag/internal/credseal"
)

type DispatchRepository interface {
	ListWebhookProjects(context.Context) ([]Project, error)
	GetNextWebhookOutboxEvent(context.Context, Project, time.Time) (*OutboxEvent, error)
	ListWebhookEventDeliveries(context.Context, Project, string) ([]Delivery, error)
	GetWebhook(context.Context, string, string, string) (*Record, error)
	RecordWebhookDeliveryAttempt(context.Context, Delivery) error
	ScheduleWebhookOutboxEvent(context.Context, Project, string, time.Time) error
	DeleteWebhookOutboxEvent(context.Context, Project, string) error
}

type Dispatcher struct {
	repository DispatchRepository
	sealer     *credseal.Sealer
	client     *http.Client
	interval   time.Duration
	retryBase  time.Duration
	now        func() time.Time
	logger     *slog.Logger
	started    chan struct{}
	startOnce  sync.Once
	mu         sync.Mutex
	running    map[Project]bool
	workers    sync.WaitGroup
}

func NewDispatcher(repository DispatchRepository, sealer *credseal.Sealer, client *http.Client, interval, retryBase time.Duration, logger *slog.Logger) (*Dispatcher, error) {
	if repository == nil || sealer == nil || client == nil || interval <= 0 || retryBase <= 0 {
		return nil, errors.New("webhook dispatcher requires repository, sealer, client, and positive intervals")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{
		repository: repository, sealer: sealer, client: client, interval: interval, retryBase: retryBase,
		now: time.Now, logger: logger, started: make(chan struct{}), running: make(map[Project]bool),
	}, nil
}

func (d *Dispatcher) Started() <-chan struct{} { return d.started }

func (d *Dispatcher) Run(ctx context.Context) {
	d.startOnce.Do(func() { close(d.started) })
	defer d.workers.Wait()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := d.DispatchAll(ctx); err != nil && ctx.Err() == nil {
				d.logger.Warn("webhook dispatch pass failed", "error", err)
			}
			timer.Reset(d.interval)
		}
	}
}

func (d *Dispatcher) startProject(ctx context.Context, project Project) {
	d.mu.Lock()
	if d.running[project] {
		d.mu.Unlock()
		return
	}
	d.running[project] = true
	d.workers.Add(1)
	d.mu.Unlock()
	go func() {
		defer func() {
			d.mu.Lock()
			delete(d.running, project)
			d.mu.Unlock()
			d.workers.Done()
		}()
		if err := d.DispatchProject(ctx, project); err != nil && ctx.Err() == nil {
			d.logger.Warn("webhook project dispatch failed", "error", err,
				"organization_id", project.OrganizationID, "project_id", project.ProjectID)
		}
	}()
}

func (d *Dispatcher) DispatchAll(ctx context.Context) error {
	projects, err := d.repository.ListWebhookProjects(ctx)
	if err != nil {
		return err
	}
	for _, project := range projects {
		d.startProject(ctx, project)
	}
	return nil
}

func (d *Dispatcher) DispatchProject(ctx context.Context, project Project) error {
	for {
		event, err := d.repository.GetNextWebhookOutboxEvent(ctx, project, d.now().UTC())
		if err != nil || event == nil {
			return err
		}
		if err := d.dispatchEvent(ctx, project, *event); err != nil {
			return err
		}
	}
}

func (d *Dispatcher) dispatchEvent(ctx context.Context, project Project, event OutboxEvent) error {
	deliveries, err := d.repository.ListWebhookEventDeliveries(ctx, project, event.EventID)
	if err != nil {
		return err
	}
	if len(deliveries) == 0 {
		return d.repository.DeleteWebhookOutboxEvent(ctx, project, event.EventID)
	}
	body, err := json.Marshal(event.Envelope)
	if err != nil {
		return err
	}
	now := d.now().UTC()
	allTerminal := true
	var next time.Time
	for i := range deliveries {
		delivery := &deliveries[i]
		if terminal(delivery.Status) {
			continue
		}
		if delivery.NextAttemptAt != nil && delivery.NextAttemptAt.After(now) {
			allTerminal = false
			next = earliest(next, *delivery.NextAttemptAt)
			continue
		}
		d.attempt(ctx, event, body, delivery, now)
		if err := d.repository.RecordWebhookDeliveryAttempt(ctx, *delivery); err != nil {
			return err
		}
		if !terminal(delivery.Status) {
			allTerminal = false
			next = earliest(next, *delivery.NextAttemptAt)
		}
	}
	if allTerminal {
		return d.repository.DeleteWebhookOutboxEvent(ctx, project, event.EventID)
	}
	return d.repository.ScheduleWebhookOutboxEvent(ctx, project, event.EventID, next)
}

func (d *Dispatcher) attempt(ctx context.Context, event OutboxEvent, body []byte, delivery *Delivery, at time.Time) {
	delivery.AttemptCount++
	delivery.LastAttemptedAt = &at
	delivery.ResponseCode = nil
	webhookRecord, err := d.repository.GetWebhook(ctx, delivery.OrganizationID, delivery.ProjectID, delivery.WebhookID)
	var secret string
	if err == nil && webhookRecord.State != StateActive {
		err = errors.New("webhook is pending activation")
	}
	if err == nil && len(webhookRecord.SealedSecret) != 0 {
		secret, err = d.sealer.Unseal(
			webhookRecord.OrganizationID, webhookRecord.ProjectID, "webhook_secret", webhookRecord.ID,
			webhookRecord.SealedSecret,
		)
	}
	code := 0
	detail := ""
	if err == nil {
		code, detail, err = send(ctx, d.client, webhookRecord.URL, secret, event.Operation, event.EventID, body)
	}
	if code != 0 {
		delivery.ResponseCode = &code
	}
	if err == nil && code >= 200 && code < 300 {
		delivery.Status = DeliveryDelivered
		delivery.NextAttemptAt = nil
		if detail != "" {
			delivery.Detail = &detail
		}
		return
	}
	if err != nil {
		detail = err.Error()
	}
	if detail == "" {
		detail = fmt.Sprintf("endpoint returned HTTP %d", code)
	}
	delivery.Detail = &detail
	if errors.Is(err, ErrPrivateAddress) {
		delivery.Status = DeliveryRefused
		delivery.NextAttemptAt = nil
		return
	}
	if delivery.AttemptCount >= MaxAttempts {
		delivery.Status = DeliveryFailed
		delivery.NextAttemptAt = nil
		return
	}
	delivery.Status = DeliveryRetrying
	next := at.Add(retryDelay(d.retryBase, delivery.AttemptCount))
	delivery.NextAttemptAt = &next
}

func retryDelay(base time.Duration, attempt int) time.Duration {
	return base * time.Duration(1<<(attempt-1))
}

func terminal(status string) bool {
	return status == DeliveryDelivered || status == DeliveryFailed || status == DeliveryRefused
}

func earliest(current, candidate time.Time) time.Time {
	if current.IsZero() || candidate.Before(current) {
		return candidate
	}
	return current
}
