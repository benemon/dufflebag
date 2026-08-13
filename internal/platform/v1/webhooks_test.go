package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/webhook"
	"github.com/google/uuid"
)

const testWebhookID = "30000000-0000-4000-8000-000000000001"

type webhookOperationCase struct {
	name, method, path, audit string
	body                      any
	want                      int
}

func webhookOperationCases() []webhookOperationCase {
	base := "/api/v1/organizations/" + testOrgID + "/projects/" + testProjID + "/webhooks"
	return []webhookOperationCase{
		{"list", http.MethodGet, base, "webhook.list", nil, http.StatusOK},
		{"create", http.MethodPost, base, "webhook.create", map[string]any{
			"name": "build events", "url": "https://example.com/hook", "secret": "audit-webhook-plaintext", "events": []string{},
		}, http.StatusCreated},
		{"get", http.MethodGet, base + "/" + testWebhookID, "webhook.read", nil, http.StatusOK},
		{"update", http.MethodPatch, base + "/" + testWebhookID, "webhook.update", map[string]any{
			"name": "updated", "secret": "audit-webhook-plaintext",
		}, http.StatusOK},
		{"delete", http.MethodDelete, base + "/" + testWebhookID, "webhook.delete", nil, http.StatusNoContent},
		{"verify", http.MethodPost, base + "/" + testWebhookID + "/verify", "webhook.verify", nil, http.StatusOK},
		{"deliveries", http.MethodGet, base + "/" + testWebhookID + "/deliveries", "webhook.delivery.list", nil, http.StatusOK},
	}
}

func TestWebhookEndpointsRequireMaintainerOnRoleAndTenancyAxes(t *testing.T) {
	for _, role := range []identity.Role{identity.RoleReader, identity.RoleBuilder, identity.RolePublisher} {
		for _, operation := range webhookOperationCases() {
			t.Run(string(role)+"/"+operation.name, func(t *testing.T) {
				handler, trail := auditedPlatform(t, webhookHandler(role, &fakeWebhookService{}))
				response := call(t, handler, operation.method, operation.path, operation.body, testToken)
				if response.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403: %s", response.Code, response.Body)
				}
				if event := trail.response(t); event["reason"] != "role_refused" {
					t.Fatalf("audit = %#v", event)
				}
			})
		}
	}
	for _, operation := range webhookOperationCases() {
		t.Run("maintainer/"+operation.name, func(t *testing.T) {
			handler := webhookHandler(identity.RoleMaintainer, &fakeWebhookService{})
			response := call(t, handler, operation.method, operation.path, operation.body, testToken)
			if response.Code != operation.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, operation.want, response.Body)
			}
		})
	}

	foreign := pinIdentity{id: "foreign", role: identity.RoleMaintainer, scope: identity.Scope{
		OrganizationID: uuid.New(), ProjectID: uuid.New(),
	}}
	for _, operation := range webhookOperationCases() {
		handler := newHandlerWithServices(
			bagDropProjectRepository(), &fakeInstanceRepository{}, foreign, foreign, testLogger(),
			nil, nil, nil, nil, nil, &fakeWebhookService{}, BuildInfo{}, time.Now,
		)
		audited, trail := auditedPlatform(t, handler)
		response := call(t, audited, operation.method, operation.path, operation.body, testToken)
		if response.Code != http.StatusNotFound {
			t.Fatalf("foreign %s status = %d, want 404: %s", operation.name, response.Code, response.Body)
		}
		if event := trail.response(t); event["reason"] != "tenancy_refused" {
			t.Fatalf("foreign %s audit = %#v", operation.name, event)
		}
	}
}

func TestWebhookWritesAuditSuccessAndRefusalWithoutEchoingSecret(t *testing.T) {
	for _, operation := range webhookOperationCases() {
		if operation.name != "create" && operation.name != "update" && operation.name != "delete" && operation.name != "verify" {
			continue
		}
		handler, trail := auditedPlatform(t, webhookHandler(identity.RoleMaintainer, &fakeWebhookService{}))
		response := call(t, handler, operation.method, operation.path, operation.body, testToken)
		if response.Code != operation.want {
			t.Fatalf("%s status = %d: %s", operation.name, response.Code, response.Body)
		}
		if event := trail.response(t); event["operation"] != operation.audit || event["outcome"] != "success" {
			t.Fatalf("%s audit = %#v", operation.name, event)
		}
		if bytes.Contains(response.Body.Bytes(), []byte("audit-webhook-plaintext")) || bytes.Contains(trail.raw, []byte("audit-webhook-plaintext")) {
			t.Fatalf("%s exposed secret", operation.name)
		}
	}
}

func webhookHandler(role identity.Role, service WebhookService) http.Handler {
	scope := identity.Scope{OrganizationID: uuid.MustParse(testOrgID), ProjectID: uuid.MustParse(testProjID)}
	actor := pinIdentity{id: "actor-" + string(role), role: role, scope: scope}
	return newHandlerWithServices(
		bagDropProjectRepository(), &fakeInstanceRepository{}, actor, actor, testLogger(),
		nil, nil, nil, nil, nil, service, BuildInfo{}, func() time.Time { return initTestTime },
	)
}

type fakeWebhookService struct{}

func fakeWebhookRecord() *webhook.Record {
	return &webhook.Record{
		OrganizationID: testOrgID, ProjectID: testProjID, ID: testWebhookID,
		Name: "build events", URL: "https://example.com/hook", Description: "",
		SealedSecret: []byte("sealed"), Events: []string{}, State: webhook.StateActive,
		CreatedAt: initTestTime, UpdatedAt: initTestTime,
	}
}

func (*fakeWebhookService) Create(context.Context, string, string, webhook.Create) (*webhook.Record, error) {
	return fakeWebhookRecord(), nil
}
func (*fakeWebhookService) Get(context.Context, string, string, string) (*webhook.Record, error) {
	return fakeWebhookRecord(), nil
}
func (*fakeWebhookService) List(context.Context, string, string) ([]webhook.Record, error) {
	return []webhook.Record{*fakeWebhookRecord()}, nil
}
func (*fakeWebhookService) Update(context.Context, string, string, string, webhook.Update) (*webhook.Record, error) {
	return fakeWebhookRecord(), nil
}
func (*fakeWebhookService) Delete(context.Context, string, string, string) error { return nil }
func (*fakeWebhookService) Verify(context.Context, string, string, string) (*webhook.Record, error) {
	return fakeWebhookRecord(), nil
}
func (*fakeWebhookService) Deliveries(context.Context, string, string, string) ([]webhook.Delivery, error) {
	return []webhook.Delivery{}, nil
}

func TestWebhookRenderedReadModelNeverContainsSecret(t *testing.T) {
	encoded, err := json.Marshal(renderWebhook(*fakeWebhookRecord()))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("sealed")) || bytes.Contains(encoded, []byte(`"secret"`)) {
		t.Fatalf("rendered webhook exposed secret: %s", encoded)
	}
}
