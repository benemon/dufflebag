// Package bagdrop owns the platform-plane configuration and destination
// resolution lifecycle for the one-way registry mirror described by ADR-0025.
package bagdrop

import (
	"errors"
	"fmt"
	"time"
)

type AdapterKind string

const AdapterHCPPacker AdapterKind = "hcp-packer"

type VerificationOutcome string

const (
	OutcomeResolved VerificationOutcome = "resolved"
	OutcomeFailed   VerificationOutcome = "failed"
)

type VerificationReason string

const (
	ReasonCredentialRefused VerificationReason = "credential_refused"
	ReasonProjectNotFound   VerificationReason = "project_not_found"
	ReasonUnreachable       VerificationReason = "unreachable"
	ReasonTLSFailure        VerificationReason = "tls_failure"
)

var (
	ErrNotFound       = errors.New("bag drop configuration not found")
	ErrEnabled        = errors.New("bag drop configuration is enabled")
	ErrInvalid        = errors.New("invalid bag drop configuration")
	ErrResolution     = errors.New("bag drop destination did not resolve")
	ErrCredentialSeal = errors.New("bag drop credential sealing unavailable")
)

type HCPPackerConfig struct {
	OrganizationID string `json:"organization_id"`
	ProjectID      string `json:"project_id"`
	ClientID       string `json:"client_id"`
}

type Destination struct {
	HCPPackerConfig
	ClientSecret string
}

type VerificationResult struct {
	Outcome VerificationOutcome `json:"outcome"`
	Reason  VerificationReason  `json:"reason,omitempty"`
	Message string              `json:"message,omitempty"`
}

type LastVerification struct {
	VerificationResult
	VerifiedAt time.Time
}

// Record is the repository shape. SealedSecret is deliberately absent from
// Config, the only shape platform responses render.
type Record struct {
	OrganizationID   string
	ProjectID        string
	Adapter          AdapterKind
	HCPPacker        HCPPackerConfig
	SealedSecret     []byte
	Enabled          bool
	LastVerification *LastVerification
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Config struct {
	Adapter          AdapterKind
	HCPPacker        HCPPackerConfig
	SecretSet        bool
	Enabled          bool
	LastVerification *LastVerification
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Write struct {
	Adapter      AdapterKind
	HCPPacker    HCPPackerConfig
	ClientSecret *string
}

func render(record *Record) *Config {
	return &Config{
		Adapter: record.Adapter, HCPPacker: record.HCPPacker,
		SecretSet: len(record.SealedSecret) != 0, Enabled: record.Enabled,
		LastVerification: record.LastVerification,
		CreatedAt:        record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

// ValidateRecord keeps the adapter enum and destination requirements in the
// domain layer even when a row is restored from storage.
func ValidateRecord(record *Record) error {
	if record.Adapter != AdapterHCPPacker {
		return fmt.Errorf("%w: unsupported stored adapter %q", ErrInvalid, record.Adapter)
	}
	if record.HCPPacker.OrganizationID == "" || record.HCPPacker.ProjectID == "" ||
		record.HCPPacker.ClientID == "" || len(record.SealedSecret) == 0 {
		return fmt.Errorf("%w: incomplete stored hcp-packer configuration", ErrInvalid)
	}
	return nil
}
