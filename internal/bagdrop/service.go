package bagdrop

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Repository interface {
	GetBagDropConfig(context.Context, string, string) (*Record, error)
	PutBagDropConfig(context.Context, *Record) (*Record, error)
	DeleteBagDropConfig(context.Context, string, string) error
	RecordBagDropVerification(context.Context, string, string, VerificationResult, time.Time) (*Record, error)
	SetBagDropEnabled(context.Context, string, string, bool, *VerificationResult, time.Time) (*Record, error)
}

type Service struct {
	repository Repository
	sealer     *CredentialSealer
	adapters   Registry
	now        func() time.Time
}

func NewService(repository Repository, sealer *CredentialSealer, adapters Registry) *Service {
	return &Service{repository: repository, sealer: sealer, adapters: adapters, now: time.Now}
}

func (s *Service) Get(ctx context.Context, organizationID, projectID string) (*Config, error) {
	record, err := s.repository.GetBagDropConfig(ctx, organizationID, projectID)
	if err != nil {
		return nil, err
	}
	return render(record), nil
}

func (s *Service) Put(
	ctx context.Context, organizationID, projectID string, write Write,
) (*Config, *VerificationResult, error) {
	if err := validateWrite(write); err != nil {
		return nil, nil, err
	}
	existing, err := s.repository.GetBagDropConfig(ctx, organizationID, projectID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, nil, err
	}
	if existing == nil && write.ClientSecret == nil {
		return nil, nil, fmt.Errorf("%w: client_secret is required when creating a Bag Drop configuration", ErrInvalid)
	}

	var sealed []byte
	if write.ClientSecret != nil {
		sealed, err = s.sealer.Seal(organizationID, projectID, *write.ClientSecret)
		if err != nil {
			return nil, nil, err
		}
	} else {
		sealed = append([]byte(nil), existing.SealedSecret...)
	}

	now := s.now().UTC()
	record := &Record{
		OrganizationID: organizationID, ProjectID: projectID,
		Adapter: write.Adapter, HCPPacker: write.HCPPacker, SealedSecret: sealed,
		CreatedAt: now, UpdatedAt: now,
	}
	changed := true
	if existing != nil {
		record.Enabled = existing.Enabled
		record.CreatedAt = existing.CreatedAt
		changed = write.ClientSecret != nil || existing.Adapter != write.Adapter || existing.HCPPacker != write.HCPPacker
		if !changed {
			record.LastVerification = existing.LastVerification
		}
	}

	if existing != nil && existing.Enabled && changed {
		result, resolveErr := s.resolve(ctx, record)
		if resolveErr != nil {
			return nil, nil, resolveErr
		}
		if result.Outcome != OutcomeResolved {
			return nil, &result, ErrResolution
		}
		record.LastVerification = &LastVerification{VerificationResult: result, VerifiedAt: now}
	}
	stored, err := s.repository.PutBagDropConfig(ctx, record)
	if err != nil {
		return nil, nil, err
	}
	return render(stored), nil, nil
}

func (s *Service) Delete(ctx context.Context, organizationID, projectID string) error {
	record, err := s.repository.GetBagDropConfig(ctx, organizationID, projectID)
	if err != nil {
		return err
	}
	if record.Enabled {
		return ErrEnabled
	}
	return s.repository.DeleteBagDropConfig(ctx, organizationID, projectID)
}

func (s *Service) Verify(
	ctx context.Context, organizationID, projectID string,
) (VerificationResult, error) {
	record, err := s.repository.GetBagDropConfig(ctx, organizationID, projectID)
	if err != nil {
		return VerificationResult{}, err
	}
	result, err := s.resolve(ctx, record)
	if err != nil {
		return VerificationResult{}, err
	}
	_, err = s.repository.RecordBagDropVerification(ctx, organizationID, projectID, result, s.now().UTC())
	return result, err
}

func (s *Service) Enable(
	ctx context.Context, organizationID, projectID string,
) (*Config, *VerificationResult, error) {
	record, err := s.repository.GetBagDropConfig(ctx, organizationID, projectID)
	if err != nil {
		return nil, nil, err
	}
	result, err := s.resolve(ctx, record)
	if err != nil {
		return nil, nil, err
	}
	if result.Outcome != OutcomeResolved {
		return nil, &result, ErrResolution
	}
	stored, err := s.repository.SetBagDropEnabled(
		ctx, organizationID, projectID, true, &result, s.now().UTC(),
	)
	if err != nil {
		return nil, nil, err
	}
	return render(stored), nil, nil
}

func (s *Service) Disable(ctx context.Context, organizationID, projectID string) (*Config, error) {
	stored, err := s.repository.SetBagDropEnabled(
		ctx, organizationID, projectID, false, nil, s.now().UTC(),
	)
	if err != nil {
		return nil, err
	}
	return render(stored), nil
}

func (s *Service) resolve(ctx context.Context, record *Record) (VerificationResult, error) {
	adapter, ok := s.adapters[record.Adapter]
	if !ok {
		return VerificationResult{}, fmt.Errorf("%w: unsupported adapter %q", ErrInvalid, record.Adapter)
	}
	secret, err := s.sealer.Unseal(record.OrganizationID, record.ProjectID, record.SealedSecret)
	if err != nil {
		return VerificationResult{}, err
	}
	return adapter.Resolve(ctx, Destination{HCPPackerConfig: record.HCPPacker, ClientSecret: secret}), nil
}

func validateWrite(write Write) error {
	if write.Adapter != AdapterHCPPacker {
		return fmt.Errorf("%w: adapter must be %q", ErrInvalid, AdapterHCPPacker)
	}
	if write.HCPPacker.OrganizationID == "" || write.HCPPacker.ProjectID == "" || write.HCPPacker.ClientID == "" {
		return fmt.Errorf("%w: hcp_packer organization_id, project_id and client_id are required", ErrInvalid)
	}
	if write.ClientSecret != nil && *write.ClientSecret == "" {
		return fmt.Errorf("%w: client_secret must not be empty", ErrInvalid)
	}
	return nil
}
