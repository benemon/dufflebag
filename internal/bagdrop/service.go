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
	ListBagDropAssociations(context.Context, string, string) ([]Association, error)
	PutBagDropAssociation(context.Context, Association) (*Association, error)
	RemoveBagDropAssociation(context.Context, string, string, string, time.Time) (RemovalOutcome, error)
	BagDropBucketExists(context.Context, string, string, string) (bool, error)
	HasBlockingBagDropAssociations(context.Context, string, string) (bool, error)
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

// CredentialProtection reports the sealer posture exposed with configuration responses.
func (s *Service) CredentialProtection() string { return s.sealer.Mode() }

func (s *Service) Get(ctx context.Context, organizationID, projectID string) (*Config, error) {
	record, err := s.repository.GetBagDropConfig(ctx, organizationID, projectID)
	if err != nil {
		return nil, err
	}
	return render(record), nil
}

func (s *Service) Delete(ctx context.Context, organizationID, projectID string) error {
	record, err := s.repository.GetBagDropConfig(ctx, organizationID, projectID)
	if err != nil {
		return err
	}
	if record.Enabled {
		return ErrEnabled
	}
	blocked, err := s.repository.HasBlockingBagDropAssociations(ctx, organizationID, projectID)
	if err != nil {
		return err
	}
	if blocked {
		return ErrCleanupPending
	}
	return s.repository.DeleteBagDropConfig(ctx, organizationID, projectID)
}

func (s *Service) ListAssociations(
	ctx context.Context, organizationID, projectID string,
) ([]Association, error) {
	return s.repository.ListBagDropAssociations(ctx, organizationID, projectID)
}

func (s *Service) Associate(
	ctx context.Context, organizationID, projectID, bucketName string,
) (*Association, error) {
	if _, err := s.repository.GetBagDropConfig(ctx, organizationID, projectID); err != nil {
		return nil, err
	}
	exists, err := s.repository.BagDropBucketExists(ctx, organizationID, projectID, bucketName)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrBucketNotFound
	}
	now := s.now().UTC()
	return s.repository.PutBagDropAssociation(ctx, Association{
		OrganizationID: organizationID, ProjectID: projectID, BucketName: bucketName,
		State: AssociationActive, CreatedAt: now, UpdatedAt: now,
	})
}

func (s *Service) Unassociate(
	ctx context.Context, organizationID, projectID, bucketName string,
) (RemovalOutcome, error) {
	return s.repository.RemoveBagDropAssociation(
		ctx, organizationID, projectID, bucketName, s.now().UTC(),
	)
}

func (s *Service) Status(
	ctx context.Context, organizationID, projectID string,
) (*Status, error) {
	record, err := s.repository.GetBagDropConfig(ctx, organizationID, projectID)
	if errors.Is(err, ErrNotFound) {
		return &Status{Associations: []Association{}}, nil
	}
	if err != nil {
		return nil, err
	}
	associations, err := s.repository.ListBagDropAssociations(ctx, organizationID, projectID)
	if err != nil {
		return nil, err
	}
	return &Status{Configured: true, Config: render(record), Associations: associations}, nil
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
	ctx context.Context, organizationID, projectID string, write *Write,
) (*Config, *VerificationResult, error) {
	if write == nil {
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
	if err := validateWrite(*write); err != nil {
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
		Adapter: write.Adapter, SealedSecret: sealed,
		CreatedAt: now, UpdatedAt: now,
	}
	if write.HCPPacker != nil {
		record.HCPPacker = *write.HCPPacker
	}
	if write.Dufflebag != nil {
		record.Dufflebag = *write.Dufflebag
	}
	if existing != nil {
		record.CreatedAt = existing.CreatedAt
	}

	result, err := s.resolve(ctx, record)
	if err != nil {
		return nil, nil, err
	}
	if result.Outcome != OutcomeResolved {
		return nil, &result, ErrResolution
	}
	record.Enabled = true
	record.LastVerification = &LastVerification{VerificationResult: result, VerifiedAt: now}
	stored, err := s.repository.PutBagDropConfig(ctx, record)
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
	return adapter.Resolve(ctx, destinationForRecord(record, secret)), nil
}

func validateWrite(write Write) error {
	switch write.Adapter {
	case AdapterHCPPacker:
		if write.HCPPacker == nil || write.Dufflebag != nil {
			return fmt.Errorf("%w: adapter %q requires exactly the hcp_packer connection block", ErrInvalid, write.Adapter)
		}
		if write.HCPPacker.OrganizationID == "" || write.HCPPacker.ProjectID == "" || write.HCPPacker.ClientID == "" {
			return fmt.Errorf("%w: hcp_packer organization_id, project_id and client_id are required", ErrInvalid)
		}
	case AdapterDufflebag:
		if write.Dufflebag == nil || write.HCPPacker != nil {
			return fmt.Errorf("%w: adapter %q requires exactly the dufflebag connection block", ErrInvalid, write.Adapter)
		}
		if write.Dufflebag.OrganizationID == "" || write.Dufflebag.ProjectID == "" || write.Dufflebag.ClientID == "" {
			return fmt.Errorf("%w: dufflebag organization_id, project_id and client_id are required", ErrInvalid)
		}
		if _, err := NewDufflebagAdapter(write.Dufflebag.Endpoint, write.Dufflebag.CAChain); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	default:
		return fmt.Errorf("%w: unsupported adapter %q", ErrInvalid, write.Adapter)
	}
	if write.ClientSecret != nil && *write.ClientSecret == "" {
		return fmt.Errorf("%w: client_secret must not be empty", ErrInvalid)
	}
	return nil
}

func destinationForRecord(record *Record, secret string) Destination {
	return Destination{
		HCPPackerConfig: record.HCPPacker,
		Dufflebag:       record.Dufflebag,
		ClientSecret:    secret,
	}
}
