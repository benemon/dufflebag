package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/benemon/dufflebag/internal/credseal"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/google/uuid"
)

const (
	maxResponseBytes = 64 << 10
	maxDetailBytes   = 1024
)

type Repository interface {
	CreateWebhook(context.Context, Record) (*Record, error)
	GetWebhook(context.Context, string, string, string) (*Record, error)
	ListWebhooks(context.Context, string, string) ([]Record, error)
	UpdateWebhook(context.Context, Record) (*Record, error)
	DeleteWebhook(context.Context, string, string, string) error
	RecordWebhookVerification(context.Context, Record, string, string, *int, *string, time.Time) (*Record, error)
	ListWebhookDeliveries(context.Context, string, string, string) ([]Delivery, error)
}

type Service struct {
	repository Repository
	sealer     *credseal.Sealer
	client     *http.Client
	now        func() time.Time
}

func NewService(repository Repository, sealer *credseal.Sealer, client *http.Client) *Service {
	return &Service{repository: repository, sealer: sealer, client: client, now: time.Now}
}

func (s *Service) Create(ctx context.Context, organizationID, projectID string, write Create) (*Record, error) {
	if err := validateCreate(write); err != nil {
		return nil, err
	}
	at := s.now().UTC()
	record := Record{
		OrganizationID: organizationID, ProjectID: projectID, ID: uuid.NewString(),
		Name: write.Name, URL: write.URL, Description: write.Description,
		Events: append([]string{}, write.Events...), State: StatePending,
		CreatedAt: at, UpdatedAt: at,
	}
	if write.Secret != "" {
		sealed, err := s.sealer.Seal(organizationID, projectID, "webhook_secret", record.ID, write.Secret)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSealUnavailable, err)
		}
		record.SealedSecret = sealed
	}
	stored, err := s.repository.CreateWebhook(ctx, record)
	if err != nil {
		return nil, err
	}
	return s.verify(ctx, *stored)
}

func (s *Service) Get(ctx context.Context, organizationID, projectID, webhookID string) (*Record, error) {
	return s.repository.GetWebhook(ctx, organizationID, projectID, webhookID)
}

func (s *Service) List(ctx context.Context, organizationID, projectID string) ([]Record, error) {
	return s.repository.ListWebhooks(ctx, organizationID, projectID)
}

func (s *Service) Update(ctx context.Context, organizationID, projectID, webhookID string, write Update) (*Record, error) {
	if write.Name == nil && write.URL == nil && write.Description == nil && write.Secret == nil && write.Events == nil {
		return nil, fmt.Errorf("%w: at least one field is required", ErrInvalid)
	}
	record, err := s.repository.GetWebhook(ctx, organizationID, projectID, webhookID)
	if err != nil {
		return nil, err
	}
	urlChanged := false
	if write.Name != nil {
		record.Name = *write.Name
	}
	if write.URL != nil {
		urlChanged = record.URL != *write.URL
		record.URL = *write.URL
	}
	if write.Description != nil {
		record.Description = *write.Description
	}
	if write.Events != nil {
		record.Events = append([]string{}, (*write.Events)...)
	}
	if err := validateCreate(Create{Name: record.Name, URL: record.URL, Description: record.Description, Events: record.Events}); err != nil {
		return nil, err
	}
	if write.Secret != nil {
		if *write.Secret == "" {
			record.SealedSecret = nil
		} else {
			sealed, err := s.sealer.Seal(organizationID, projectID, "webhook_secret", record.ID, *write.Secret)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrSealUnavailable, err)
			}
			record.SealedSecret = sealed
		}
	}
	record.UpdatedAt = s.now().UTC()
	if urlChanged {
		record.State = StatePending
		record.LastVerificationAt = nil
		record.LastVerificationError = nil
	}
	stored, err := s.repository.UpdateWebhook(ctx, *record)
	if err != nil {
		return nil, err
	}
	if urlChanged {
		return s.verify(ctx, *stored)
	}
	return stored, nil
}

func (s *Service) Delete(ctx context.Context, organizationID, projectID, webhookID string) error {
	return s.repository.DeleteWebhook(ctx, organizationID, projectID, webhookID)
}

func (s *Service) Verify(ctx context.Context, organizationID, projectID, webhookID string) (*Record, error) {
	record, err := s.repository.GetWebhook(ctx, organizationID, projectID, webhookID)
	if err != nil {
		return nil, err
	}
	return s.verify(ctx, *record)
}

func (s *Service) Deliveries(ctx context.Context, organizationID, projectID, webhookID string) ([]Delivery, error) {
	return s.repository.ListWebhookDeliveries(ctx, organizationID, projectID, webhookID)
}

func (s *Service) verify(ctx context.Context, record Record) (*Record, error) {
	at := s.now().UTC()
	eventID := registry.NewID(at).String()
	body, err := json.Marshal(struct {
		Type      string `json:"type"`
		WebhookID string `json:"webhook_id"`
		Challenge string `json:"challenge"`
	}{Type: OperationVerification, WebhookID: record.ID, Challenge: eventID})
	if err != nil {
		return nil, err
	}
	secret, sendErr := s.secret(record)
	code, detail := 0, ""
	if sendErr == nil {
		code, detail, sendErr = send(ctx, s.client, record.URL, secret, OperationVerification, eventID, body)
	}
	status := DeliveryDelivered
	record.State = StateActive
	record.LastVerificationError = nil
	if sendErr != nil || code < 200 || code >= 300 {
		record.State = StatePending
		status = DeliveryFailed
		message := detail
		if sendErr != nil {
			message = sendErr.Error()
			if errors.Is(sendErr, ErrPrivateAddress) {
				status = DeliveryRefused
			}
		} else if message == "" {
			message = fmt.Sprintf("endpoint returned HTTP %d", code)
		}
		record.LastVerificationError = &message
	}
	var responseCode *int
	if code != 0 {
		responseCode = &code
	}
	var detailPointer *string
	if detail != "" {
		detailPointer = &detail
	}
	if record.LastVerificationError != nil {
		detailPointer = record.LastVerificationError
	}
	return s.repository.RecordWebhookVerification(ctx, record, eventID, status, responseCode, detailPointer, at)
}

func (s *Service) secret(record Record) (string, error) {
	if len(record.SealedSecret) == 0 {
		return "", nil
	}
	secret, err := s.sealer.Unseal(record.OrganizationID, record.ProjectID, "webhook_secret", record.ID, record.SealedSecret)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrSealUnavailable, err)
	}
	return secret, nil
}

func send(ctx context.Context, client *http.Client, endpoint, secret, operation, eventID string, body []byte) (int, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	SetHeaders(request, secret, operation, eventID, body)
	response, err := client.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = response.Body.Close() }()
	read, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return response.StatusCode, "", err
	}
	return response.StatusCode, boundedDetail(read), nil
}

func boundedDetail(value []byte) string {
	valid := []byte(strings.ToValidUTF8(string(value), "�"))
	if len(valid) <= maxDetailBytes {
		return string(valid)
	}
	valid = valid[:maxDetailBytes]
	for !utf8.Valid(valid) {
		valid = valid[:len(valid)-1]
	}
	return string(valid)
}

func validateCreate(write Create) error {
	if strings.TrimSpace(write.Name) == "" || utf8.RuneCountInString(write.Name) > 200 {
		return fmt.Errorf("%w: name must contain 1 to 200 characters", ErrInvalid)
	}
	if utf8.RuneCountInString(write.Description) > 1000 {
		return fmt.Errorf("%w: description must contain at most 1000 characters", ErrInvalid)
	}
	parsed, err := url.ParseRequestURI(write.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return fmt.Errorf("%w: url must be an http or https URL without user information", ErrInvalid)
	}
	allowed := make(map[string]bool, len(Operations))
	for _, operation := range Operations {
		allowed[operation] = true
	}
	seen := make(map[string]bool, len(write.Events))
	for _, operation := range write.Events {
		if !allowed[operation] || seen[operation] {
			return fmt.Errorf("%w: unsupported or duplicate event %q", ErrInvalid, operation)
		}
		seen[operation] = true
	}
	return nil
}
