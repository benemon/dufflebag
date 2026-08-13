// Package webhook implements project-scoped webhook activation and delivery.
package webhook

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	OperationVersionCreated             = "version.created"
	OperationVersionCompleted           = "version.completed"
	OperationVersionRevoked             = "version.revoked"
	OperationVersionRevocationScheduled = "version.revocation_scheduled"
	OperationVersionRestored            = "version.restored"
	OperationVersionDeleted             = "version.deleted"
	OperationChannelCreated             = "channel.created"
	OperationChannelDeleted             = "channel.deleted"
	OperationChannelAssigned            = "channel.assigned"
	OperationBucketCreated              = "bucket.created"
	OperationBucketDeleted              = "bucket.deleted"
	OperationVerification               = "webhook.verification"

	StatePending = "pending"
	StateActive  = "active"

	DeliveryPending   = "pending"
	DeliveryRetrying  = "retrying"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
	DeliveryRefused   = "refused"

	MaxAttempts = 5
)

var (
	ErrNotFound        = errors.New("webhook not found")
	ErrInvalid         = errors.New("invalid webhook")
	ErrSealUnavailable = errors.New("webhook secret sealing unavailable")
)

var Operations = []string{
	OperationVersionCreated, OperationVersionCompleted, OperationVersionRevoked,
	OperationVersionRevocationScheduled, OperationVersionRestored, OperationVersionDeleted,
	OperationChannelCreated, OperationChannelDeleted, OperationChannelAssigned,
	OperationBucketCreated, OperationBucketDeleted,
}

type Record struct {
	OrganizationID        string
	ProjectID             string
	ID                    string
	Name                  string
	URL                   string
	Description           string
	SealedSecret          []byte
	Events                []string
	State                 string
	LastVerificationAt    *time.Time
	LastVerificationError *string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type Create struct {
	Name        string
	URL         string
	Description string
	Secret      string
	Events      []string
}

type Update struct {
	Name        *string
	URL         *string
	Description *string
	Secret      *string
	Events      *[]string
}

type Target struct {
	Type        string `json:"type"`
	Bucket      string `json:"bucket,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Name        string `json:"name,omitempty"`
}

type Actor struct {
	PrincipalID string `json:"principal_id"`
	Name        string `json:"name"`
}

type Envelope struct {
	EventID        string          `json:"event_id"`
	OccurredAt     time.Time       `json:"occurred_at"`
	OrganizationID string          `json:"organization_id"`
	ProjectID      string          `json:"project_id"`
	Operation      string          `json:"operation"`
	Target         Target          `json:"target"`
	Actor          Actor           `json:"actor"`
	Payload        json.RawMessage `json:"payload"`
}

type OutboxEvent struct {
	Envelope
	AvailableAt time.Time
}

type Project struct {
	OrganizationID string
	ProjectID      string
}

type Delivery struct {
	OrganizationID   string
	ProjectID        string
	ID               string
	WebhookID        string
	EventID          string
	Operation        string
	Status           string
	AttemptCount     int
	FirstAttemptedAt *time.Time
	LastAttemptedAt  *time.Time
	NextAttemptAt    *time.Time
	ResponseCode     *int
	Detail           *string
	CreatedAt        time.Time
}

func Subscribed(events []string, operation string) bool {
	if len(events) == 0 {
		return true
	}
	for _, event := range events {
		if event == operation {
			return true
		}
	}
	return false
}
