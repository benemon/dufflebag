package v1

import (
	"context"
	"errors"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/keyring"
)

func (s *server) GetEncryption(
	ctx context.Context, _ GetEncryptionRequestObject,
) (GetEncryptionResponseObject, error) {
	if _, refused := authorizePlatform(ctx, identity.RoleRoot); refused != permitted {
		return newRefusal(refused), nil
	}
	if s.encryption == nil {
		return GetEncryption200JSONResponse{State: EncryptionStateUnconfigured, Keyring: []KeyringEntry{}}, nil
	}
	entries, err := s.encryption.Entries(ctx)
	if err != nil {
		return nil, err
	}
	return GetEncryption200JSONResponse(renderEncryption(s.encryption.State(), entries)), nil
}

func (s *server) RewrapEncryption(
	ctx context.Context, _ RewrapEncryptionRequestObject,
) (RewrapEncryptionResponseObject, error) {
	if _, refused := authorizePlatform(ctx, identity.RoleRoot); refused != permitted {
		return newRefusal(refused), nil
	}
	if s.encryption == nil {
		audit.FromContext(ctx).Enrich(audit.Enrichment{Reason: "encryption_unconfigured"})
		return RewrapEncryption409JSONResponse{Message: "this instance does not have encryption at rest"}, nil
	}
	entries, err := s.encryption.Rewrap(ctx)
	if errors.Is(err, keyring.ErrKeyService) {
		s.logger.Error("keyring rewrap failed", "error", err)
		audit.FromContext(ctx).Enrich(audit.Enrichment{Reason: "key_service_failure"})
		return RewrapEncryption502JSONResponse{Message: "the key service is unavailable"}, nil
	}
	if err != nil {
		return nil, err
	}
	return RewrapEncryption200JSONResponse(renderEncryption(s.encryption.State(), entries)), nil
}

func (s *server) RotateEncryption(
	ctx context.Context, _ RotateEncryptionRequestObject,
) (RotateEncryptionResponseObject, error) {
	if _, refused := authorizePlatform(ctx, identity.RoleRoot); refused != permitted {
		return newRefusal(refused), nil
	}
	if s.encryption == nil {
		audit.FromContext(ctx).Enrich(audit.Enrichment{Reason: "encryption_unconfigured"})
		return RotateEncryption409JSONResponse{Message: "this instance does not have encryption at rest"}, nil
	}
	entries, err := s.encryption.Rotate(ctx)
	if errors.Is(err, keyring.ErrRotationConflict) {
		audit.FromContext(ctx).Enrich(audit.Enrichment{Reason: "rotation_conflict"})
		return RotateEncryption409JSONResponse{Message: "a concurrent keyring rotation won"}, nil
	}
	if errors.Is(err, keyring.ErrKeyService) {
		s.logger.Error("keyring rotation failed", "error", err)
		audit.FromContext(ctx).Enrich(audit.Enrichment{Reason: "key_service_failure"})
		return RotateEncryption502JSONResponse{Message: "the key service is unavailable"}, nil
	}
	if err != nil {
		return nil, err
	}
	return RotateEncryption200JSONResponse(renderEncryption(s.encryption.State(), entries)), nil
}

func renderEncryption(state string, entries []keyring.Entry) Encryption {
	rendered := make([]KeyringEntry, 0, len(entries))
	for _, entry := range entries {
		rendered = append(rendered, KeyringEntry{
			Purpose: KeyringEntryPurpose(entry.Purpose), Version: int(entry.Version),
			KekRef: entry.KEKRef, WrappedAt: entry.WrappedAt,
		})
	}
	return Encryption{State: EncryptionState(state), Keyring: rendered}
}
