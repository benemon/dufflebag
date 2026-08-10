package bagdrop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/keyring"
)

const (
	testOrganization = "00000000-0000-4000-8000-000000000001"
	testProject      = "00000000-0000-4000-8000-000000000101"
	testKey          = "0123456789abcdef0123456789abcdef"
)

type testProvider struct{}

func (testProvider) Wrap(_ context.Context, plaintext []byte) ([]byte, string, error) {
	return append([]byte(nil), plaintext...), "test", nil
}

func (testProvider) Unwrap(_ context.Context, blob []byte, _ string) ([]byte, error) {
	return append([]byte(nil), blob...), nil
}

func TestCredentialSealingRoundTripsBothPosturesAndRefusesAADSwap(t *testing.T) {
	ring, _, err := keyring.Generate(context.Background(), testProvider{})
	if err != nil {
		t.Fatalf("generate keyring: %v", err)
	}
	for name, sealer := range map[string]*CredentialSealer{
		"encrypted keyring": NewCredentialSealer(ring, ""),
		"unencrypted env":   NewCredentialSealer(nil, testKey),
	} {
		t.Run(name, func(t *testing.T) {
			sealed, err := sealer.Seal(testOrganization, testProject, "credential-value")
			if err != nil {
				t.Fatalf("seal: %v", err)
			}
			opened, err := sealer.Unseal(testOrganization, testProject, sealed)
			if err != nil || opened != "credential-value" {
				t.Fatalf("unseal = %q, %v", opened, err)
			}
			if _, err := sealer.Unseal(
				testOrganization, "00000000-0000-4000-8000-000000000102", sealed,
			); err == nil {
				t.Fatal("credential moved to another project decrypted")
			}
		})
	}
}

func TestPutRefusesWithoutBagDropCredentialKey(t *testing.T) {
	service := NewService(&memoryRepository{}, NewCredentialSealer(nil, ""), Registry{})
	_, _, err := service.Put(context.Background(), testOrganization, testProject, testWrite("secret"))
	if !errors.Is(err, ErrCredentialSeal) || !strings.Contains(err.Error(), CredentialKeyEnv) {
		t.Fatalf("Put error = %v, want refusal naming %s", err, CredentialKeyEnv)
	}
}

func TestReadAndDeleteDoNotRequireBagDropCredentialKey(t *testing.T) {
	repository := &memoryRepository{record: &Record{
		OrganizationID: testOrganization, ProjectID: testProject, Adapter: AdapterHCPPacker,
		HCPPacker: HCPPackerConfig{
			OrganizationID: "hcp-org", ProjectID: "hcp-project", ClientID: "client",
		},
		SealedSecret: []byte("opaque-existing-envelope"), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
	service := NewService(repository, NewCredentialSealer(nil, ""), nil)
	if config, err := service.Get(context.Background(), testOrganization, testProject); err != nil || !config.SecretSet {
		t.Fatalf("Get = %#v, %v", config, err)
	}
	if err := service.Delete(context.Background(), testOrganization, testProject); err != nil {
		t.Fatalf("Delete without credential key: %v", err)
	}
}

func TestEnableRefusesWhenResolveFails(t *testing.T) {
	sealer := NewCredentialSealer(nil, testKey)
	sealed, err := sealer.Seal(testOrganization, testProject, "secret")
	if err != nil {
		t.Fatal(err)
	}
	repository := &memoryRepository{record: &Record{
		OrganizationID: testOrganization, ProjectID: testProject,
		Adapter:      AdapterHCPPacker,
		HCPPacker:    HCPPackerConfig{OrganizationID: "hcp-org", ProjectID: "hcp-project", ClientID: "client"},
		SealedSecret: sealed, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
	adapter := &fakeAdapter{result: VerificationResult{
		Outcome: OutcomeFailed, Reason: ReasonCredentialRefused,
	}}
	service := NewService(repository, sealer, Registry{AdapterHCPPacker: adapter})
	config, verification, err := service.Enable(context.Background(), testOrganization, testProject)
	if config != nil || !errors.Is(err, ErrResolution) || verification == nil ||
		verification.Reason != ReasonCredentialRefused {
		t.Fatalf("Enable = %#v, %#v, %v", config, verification, err)
	}
	if adapter.calls != 1 {
		t.Fatalf("Resolve calls = %d, want 1", adapter.calls)
	}
	if repository.enableCalls != 0 || repository.record.Enabled {
		t.Fatalf("failed enable changed repository: calls=%d record=%#v", repository.enableCalls, repository.record)
	}
}

func TestDeleteWhileEnabledRefuses(t *testing.T) {
	repository := &memoryRepository{record: &Record{Enabled: true}}
	service := NewService(repository, NewCredentialSealer(nil, testKey), nil)
	err := service.Delete(context.Background(), testOrganization, testProject)
	if !errors.Is(err, ErrEnabled) || repository.deleteCalls != 0 {
		t.Fatalf("Delete = %v, calls=%d", err, repository.deleteCalls)
	}
}

func TestEnabledPutRefusesUnresolvableReplacementWithoutChangingConfig(t *testing.T) {
	sealer := NewCredentialSealer(nil, testKey)
	sealed, err := sealer.Seal(testOrganization, testProject, "old-secret")
	if err != nil {
		t.Fatal(err)
	}
	original := &Record{
		OrganizationID: testOrganization, ProjectID: testProject, Adapter: AdapterHCPPacker,
		HCPPacker: HCPPackerConfig{
			OrganizationID: "hcp-org", ProjectID: "hcp-project", ClientID: "old-client",
		},
		SealedSecret: sealed, Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	repository := &memoryRepository{record: original}
	adapter := &fakeAdapter{result: VerificationResult{Outcome: OutcomeFailed, Reason: ReasonUnreachable}}
	service := NewService(repository, sealer, Registry{AdapterHCPPacker: adapter})
	replacement := testWrite("")
	replacement.ClientSecret = nil
	replacement.HCPPacker.ClientID = "new-client"
	config, verification, err := service.Put(
		context.Background(), testOrganization, testProject, replacement,
	)
	if config != nil || !errors.Is(err, ErrResolution) || verification == nil ||
		verification.Reason != ReasonUnreachable || adapter.calls != 1 {
		t.Fatalf("Put = %#v, %#v, %v; Resolve calls=%d", config, verification, err, adapter.calls)
	}
	if repository.record.HCPPacker.ClientID != "old-client" || !repository.record.Enabled {
		t.Fatalf("failed replacement changed stored config: %#v", repository.record)
	}
}

func TestUpdateWithoutSecretKeepsOldSecretWorking(t *testing.T) {
	repository := &memoryRepository{}
	adapter := &fakeAdapter{result: VerificationResult{Outcome: OutcomeResolved}}
	service := NewService(
		repository, NewCredentialSealer(nil, testKey), Registry{AdapterHCPPacker: adapter},
	)
	service.now = func() time.Time { return time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC) }
	if _, _, err := service.Put(
		context.Background(), testOrganization, testProject, testWrite("known-old-secret"),
	); err != nil {
		t.Fatalf("create: %v", err)
	}
	update := testWrite("")
	update.ClientSecret = nil
	update.HCPPacker.ClientID = "updated-client"
	if _, _, err := service.Put(context.Background(), testOrganization, testProject, update); err != nil {
		t.Fatalf("update without secret: %v", err)
	}
	if _, err := service.Verify(context.Background(), testOrganization, testProject); err != nil {
		t.Fatalf("verify updated configuration: %v", err)
	}
	if adapter.destination.ClientSecret != "known-old-secret" ||
		adapter.destination.ClientID != "updated-client" {
		t.Fatalf("adapter destination = %#v", adapter.destination)
	}
}

func testWrite(secret string) Write {
	return Write{
		Adapter: AdapterHCPPacker,
		HCPPacker: HCPPackerConfig{
			OrganizationID: "hcp-org", ProjectID: "hcp-project", ClientID: "client",
		},
		ClientSecret: &secret,
	}
}

type fakeAdapter struct {
	result      VerificationResult
	destination Destination
	calls       int
}

func (a *fakeAdapter) Resolve(_ context.Context, destination Destination) VerificationResult {
	a.calls++
	a.destination = destination
	return a.result
}

type memoryRepository struct {
	record      *Record
	enableCalls int
	deleteCalls int
}

func (r *memoryRepository) GetBagDropConfig(context.Context, string, string) (*Record, error) {
	if r.record == nil {
		return nil, ErrNotFound
	}
	copy := *r.record
	copy.SealedSecret = append([]byte(nil), r.record.SealedSecret...)
	return &copy, nil
}

func (r *memoryRepository) PutBagDropConfig(_ context.Context, record *Record) (*Record, error) {
	copy := *record
	copy.SealedSecret = append([]byte(nil), record.SealedSecret...)
	r.record = &copy
	return r.GetBagDropConfig(context.Background(), record.OrganizationID, record.ProjectID)
}

func (r *memoryRepository) DeleteBagDropConfig(context.Context, string, string) error {
	r.deleteCalls++
	r.record = nil
	return nil
}

func (r *memoryRepository) RecordBagDropVerification(
	_ context.Context, _, _ string, result VerificationResult, at time.Time,
) (*Record, error) {
	r.record.LastVerification = &LastVerification{VerificationResult: result, VerifiedAt: at}
	r.record.UpdatedAt = at
	return r.GetBagDropConfig(context.Background(), r.record.OrganizationID, r.record.ProjectID)
}

func (r *memoryRepository) SetBagDropEnabled(
	_ context.Context, _, _ string, enabled bool, result *VerificationResult, at time.Time,
) (*Record, error) {
	if r.record == nil {
		return nil, ErrNotFound
	}
	r.enableCalls++
	r.record.Enabled = enabled
	r.record.UpdatedAt = at
	if result != nil {
		r.record.LastVerification = &LastVerification{VerificationResult: *result, VerifiedAt: at}
	}
	return r.GetBagDropConfig(context.Background(), r.record.OrganizationID, r.record.ProjectID)
}
