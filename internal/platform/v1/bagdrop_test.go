package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/bagdrop"
	"github.com/benemon/dufflebag/internal/domain/identity"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

func TestBagDropRoleAxisAndAuditEvents(t *testing.T) {
	for _, role := range []identity.Role{
		identity.RoleReader, identity.RoleBuilder, identity.RolePublisher,
	} {
		for _, operation := range bagDropOperations() {
			t.Run(string(role)+"/"+operation.name, func(t *testing.T) {
				handler, trail := auditedPlatform(t, bagDropHandler(role, &fakeBagDropService{}))
				response := call(t, handler, operation.method, operation.path, operation.body, testToken)
				if response.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403: %s", response.Code, response.Body)
				}
				event := trail.response(t)
				if event["operation"] != operation.audit || event["outcome"] != "refused" ||
					event["reason"] != "role_refused" {
					t.Fatalf("audit = %#v", event)
				}
			})
		}
	}

	for _, operation := range bagDropOperations() {
		t.Run("maintainer/"+operation.name, func(t *testing.T) {
			handler, trail := auditedPlatform(t, bagDropHandler(identity.RoleMaintainer, &fakeBagDropService{}))
			response := call(t, handler, operation.method, operation.path, operation.body, testToken)
			if response.Code != operation.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, operation.want, response.Body)
			}
			event := trail.response(t)
			if event["operation"] != operation.audit || event["outcome"] != "success" {
				t.Fatalf("audit = %#v", event)
			}
		})
	}
}

func TestBagDropTenancyAxisConcealsForeignAndAbsentProjects(t *testing.T) {
	foreign := pinIdentity{
		id: "foreign", role: identity.RoleMaintainer,
		scope: identity.Scope{OrganizationID: uuid.New(), ProjectID: uuid.New()},
	}
	maintainer := pinIdentity{
		id: "maintainer", role: identity.RoleMaintainer,
		scope: identity.Scope{
			OrganizationID: uuid.MustParse(testOrgID), ProjectID: uuid.MustParse(testProjID),
		},
	}
	for _, test := range []struct {
		name       string
		actor      pinIdentity
		repository *fakeTenancyRepository
	}{
		{"foreign", foreign, bagDropProjectRepository()},
		{"absent", maintainer, &fakeTenancyRepository{}},
	} {
		for _, operation := range bagDropOperations() {
			t.Run(test.name+"/"+operation.name, func(t *testing.T) {
				handler := newHandlerWithBagDrop(
					test.repository, &fakeInstanceRepository{}, test.actor, test.actor,
					testLogger(), &fakeBagDropService{}, time.Now,
				)
				handler, trail := auditedPlatform(t, handler)
				response := call(t, handler, operation.method, operation.path, operation.body, testToken)
				if response.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want 404: %s", response.Code, response.Body)
				}
				if event := trail.response(t); event["outcome"] != "refused" ||
					event["reason"] != "tenancy_refused" {
					t.Fatalf("audit = %#v", event)
				}
			})
		}
	}
}

func TestBagDropConfigWriteAuditsClientSecretByHMACOnly(t *testing.T) {
	const secret = "audit-this-secret-but-never-store-it"
	handler, trail := auditedPlatform(t, bagDropHandler(identity.RoleMaintainer, &fakeBagDropService{}))
	body := bagDropWriteBody()
	body["hcp_packer"].(map[string]any)["client_secret"] = secret
	response := call(t, handler, http.MethodPut, bagDropPath(""), body, testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	if bytes.Contains(trail.raw, []byte(secret)) {
		t.Fatalf("audit trail contains plaintext secret: %s", trail.raw)
	}
	if event := trail.response(t); event["client_secret_hmac"] == nil || event["hmac_key_version"] != "test-v1" {
		t.Fatalf("audit response lacks versioned client secret HMAC: %#v", event)
	}
}

func TestBagDropHandlerEnableRefusesUnresolvableFakeAdapter(t *testing.T) {
	sealer := bagdrop.NewCredentialSealer(nil, "0123456789abcdef0123456789abcdef")
	sealed, err := sealer.Seal(testOrgID, testProjID, "destination-secret")
	if err != nil {
		t.Fatal(err)
	}
	repository := &handlerBagDropRepository{record: &bagdrop.Record{
		OrganizationID: testOrgID, ProjectID: testProjID, Adapter: bagdrop.AdapterHCPPacker,
		HCPPacker: bagdrop.HCPPackerConfig{
			OrganizationID: "hcp-org", ProjectID: "hcp-project", ClientID: "client",
		},
		SealedSecret: sealed, CreatedAt: initTestTime, UpdatedAt: initTestTime,
	}}
	adapter := &handlerBagDropAdapter{result: bagdrop.VerificationResult{
		Outcome: bagdrop.OutcomeFailed, Reason: bagdrop.ReasonCredentialRefused,
	}}
	service := bagdrop.NewService(
		repository, sealer, bagdrop.Registry{bagdrop.AdapterHCPPacker: adapter},
	)
	response := call(
		t, bagDropHandler(identity.RoleMaintainer, service),
		http.MethodPost, bagDropPath("enable"), nil, testToken,
	)
	if response.Code != http.StatusConflict || adapter.calls != 1 || repository.enableCalls != 0 {
		t.Fatalf("enable = %d, Resolve calls=%d, persistence calls=%d: %s",
			response.Code, adapter.calls, repository.enableCalls, response.Body)
	}
	if !strings.Contains(response.Body.String(), `"reason":"credential_refused"`) {
		t.Fatalf("enable conflict omitted verification: %s", response.Body)
	}
}

func TestSecretEchoGateBagDropReadResponsesNeverContainClientSecret(t *testing.T) {
	const secret = "SECRET-ECHO-GATE-known-client-secret"
	service := &fakeBagDropService{}
	handler := bagDropHandler(identity.RoleMaintainer, service)
	body := bagDropWriteBody()
	body["hcp_packer"].(map[string]any)["client_secret"] = secret
	requests := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPut, bagDropPath(""), body},
		{http.MethodGet, bagDropPath(""), nil},
		{http.MethodPost, bagDropPath("verify"), nil},
		{http.MethodPost, bagDropPath("enable"), nil},
		{http.MethodPost, bagDropPath("disable"), nil},
	}
	for _, request := range requests {
		response := call(t, handler, request.method, request.path, request.body, testToken)
		if response.Code != http.StatusOK {
			t.Fatalf("%s %s = %d: %s", request.method, request.path, response.Code, response.Body)
		}
		if bytes.Contains(response.Body.Bytes(), []byte(secret)) ||
			strings.Contains(response.Body.String(), "client_secret") {
			t.Fatalf("%s %s echoed secret in %s", request.method, request.path, response.Body)
		}
	}

	// Marshal the generated read model directly as a second oracle. A field
	// added to serialization without being populated by a handler still fails.
	encoded, err := json.Marshal(renderBagDropConfig(service.config()))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(secret)) || bytes.Contains(encoded, []byte("client_secret")) {
		t.Fatalf("generated BagDropConfig read shape exposed secret material: %s", encoded)
	}
}

type bagDropOperation struct {
	name, method, path, audit string
	body                      any
	want                      int
}

func bagDropOperations() []bagDropOperation {
	return []bagDropOperation{
		{"get", http.MethodGet, bagDropPath(""), "bagdrop.config.read", nil, http.StatusOK},
		{"put", http.MethodPut, bagDropPath(""), "bagdrop.config.write", bagDropWriteBody(), http.StatusOK},
		{"delete", http.MethodDelete, bagDropPath(""), "bagdrop.config.delete", nil, http.StatusNoContent},
		{"verify", http.MethodPost, bagDropPath("verify"), "bagdrop.verify", nil, http.StatusOK},
		{"enable", http.MethodPost, bagDropPath("enable"), "bagdrop.enable", nil, http.StatusOK},
		{"disable", http.MethodPost, bagDropPath("disable"), "bagdrop.disable", nil, http.StatusOK},
	}
}

func bagDropPath(action string) string {
	path := "/api/v1/organizations/" + testOrgID + "/projects/" + testProjID + "/bagdrop"
	if action != "" {
		path += "/" + action
	}
	return path
}

func bagDropWriteBody() map[string]any {
	return map[string]any{
		"adapter": "hcp-packer",
		"hcp_packer": map[string]any{
			"organization_id": "hcp-org", "project_id": "hcp-project",
			"client_id": "hcp-client", "client_secret": "secret",
		},
	}
}

func bagDropProjectRepository() *fakeTenancyRepository {
	return &fakeTenancyRepository{projects: []store.Project{{
		ID: testProjID, OrganizationID: testOrgID, Name: "project", CreatedAt: initTestTime,
	}}}
}

func bagDropHandler(role identity.Role, service BagDropService) http.Handler {
	scope := identity.Scope{
		OrganizationID: uuid.MustParse(testOrgID), ProjectID: uuid.MustParse(testProjID),
	}
	actor := pinIdentity{id: "actor-" + string(role), role: role, scope: scope}
	return newHandlerWithBagDrop(
		bagDropProjectRepository(), &fakeInstanceRepository{}, actor, actor,
		testLogger(), service, func() time.Time { return initTestTime },
	)
}

type fakeBagDropService struct{}

func (*fakeBagDropService) config() *bagdrop.Config {
	return &bagdrop.Config{
		Adapter: bagdrop.AdapterHCPPacker,
		HCPPacker: bagdrop.HCPPackerConfig{
			OrganizationID: "hcp-org", ProjectID: "hcp-project", ClientID: "hcp-client",
		},
		SecretSet: true, CreatedAt: initTestTime, UpdatedAt: initTestTime,
	}
}

func (s *fakeBagDropService) Get(context.Context, string, string) (*bagdrop.Config, error) {
	return s.config(), nil
}

func (s *fakeBagDropService) Put(
	context.Context, string, string, bagdrop.Write,
) (*bagdrop.Config, *bagdrop.VerificationResult, error) {
	return s.config(), nil, nil
}

func (*fakeBagDropService) Delete(context.Context, string, string) error { return nil }

func (*fakeBagDropService) Verify(context.Context, string, string) (bagdrop.VerificationResult, error) {
	return bagdrop.VerificationResult{Outcome: bagdrop.OutcomeResolved}, nil
}

func (s *fakeBagDropService) Enable(
	context.Context, string, string,
) (*bagdrop.Config, *bagdrop.VerificationResult, error) {
	config := s.config()
	config.Enabled = true
	return config, nil, nil
}

func (s *fakeBagDropService) Disable(context.Context, string, string) (*bagdrop.Config, error) {
	return s.config(), nil
}

type handlerBagDropAdapter struct {
	result bagdrop.VerificationResult
	calls  int
}

func (a *handlerBagDropAdapter) Resolve(context.Context, bagdrop.Destination) bagdrop.VerificationResult {
	a.calls++
	return a.result
}

type handlerBagDropRepository struct {
	record      *bagdrop.Record
	enableCalls int
}

func (r *handlerBagDropRepository) GetBagDropConfig(context.Context, string, string) (*bagdrop.Record, error) {
	if r.record == nil {
		return nil, bagdrop.ErrNotFound
	}
	copy := *r.record
	copy.SealedSecret = append([]byte(nil), r.record.SealedSecret...)
	return &copy, nil
}

func (r *handlerBagDropRepository) PutBagDropConfig(
	_ context.Context, record *bagdrop.Record,
) (*bagdrop.Record, error) {
	r.record = record
	return r.GetBagDropConfig(context.Background(), record.OrganizationID, record.ProjectID)
}

func (r *handlerBagDropRepository) DeleteBagDropConfig(context.Context, string, string) error {
	r.record = nil
	return nil
}

func (r *handlerBagDropRepository) RecordBagDropVerification(
	context.Context, string, string, bagdrop.VerificationResult, time.Time,
) (*bagdrop.Record, error) {
	return r.GetBagDropConfig(context.Background(), testOrgID, testProjID)
}

func (r *handlerBagDropRepository) SetBagDropEnabled(
	_ context.Context, _, _ string, enabled bool, result *bagdrop.VerificationResult, at time.Time,
) (*bagdrop.Record, error) {
	r.enableCalls++
	r.record.Enabled = enabled
	if result != nil {
		r.record.LastVerification = &bagdrop.LastVerification{
			VerificationResult: *result, VerifiedAt: at,
		}
	}
	return r.GetBagDropConfig(context.Background(), testOrgID, testProjID)
}
