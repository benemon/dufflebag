package v1

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/benemon/dufflebag/internal/webhook"
	"github.com/google/uuid"
)

// scopeRecordingWebhooks records the project each call names, so a test can
// see the organization handlers address the organization's own webhooks.
type scopeRecordingWebhooks struct {
	fakeWebhookService
	projects []string
}

func (s *scopeRecordingWebhooks) Create(ctx context.Context, organizationID, projectID string, write webhook.Create) (*webhook.Record, error) {
	s.projects = append(s.projects, projectID)
	return s.fakeWebhookService.Create(ctx, organizationID, projectID, write)
}

func (s *scopeRecordingWebhooks) List(ctx context.Context, organizationID, projectID string) ([]webhook.Record, error) {
	s.projects = append(s.projects, projectID)
	return s.fakeWebhookService.List(ctx, organizationID, projectID)
}

func organizationWebhookCases() []webhookOperationCase {
	cases := webhookOperationCases()
	for i := range cases {
		cases[i].path = strings.Replace(cases[i].path, "/projects/"+testProjID, "", 1)
		cases[i].audit = "organization_" + cases[i].audit
	}
	return cases
}

func organizationWebhookHandler(scope identity.Scope, role identity.Role, service WebhookService) http.Handler {
	actor := pinIdentity{id: "actor-" + string(role), role: role, scope: scope}
	return newHandlerWithServices(
		pluginRegistryRepository(store.PluginRegistry{}), &fakeInstanceRepository{}, actor, actor, testLogger(),
		nil, nil, nil, nil, nil, service, BuildInfo{}, DefaultPluginUploadBytes, nil, func() time.Time { return initTestTime },
	)
}

func TestOrganizationWebhooksRequireAnOrganizationMaintainer(t *testing.T) {
	organization := identity.Scope{OrganizationID: uuid.MustParse(testOrgID)}
	for _, operation := range organizationWebhookCases() {
		t.Run("maintainer/"+operation.name, func(t *testing.T) {
			service := &scopeRecordingWebhooks{}
			handler, trail := auditedPlatform(t, organizationWebhookHandler(organization, identity.RoleMaintainer, service))
			response := call(t, handler, operation.method, operation.path, operation.body, testToken)
			if response.Code != operation.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, operation.want, response.Body)
			}
			for _, project := range service.projects {
				if project != "" {
					t.Fatalf("an organization webhook call named project %q", project)
				}
			}
			if event := trail.response(t); event["operation"] != operation.audit {
				t.Fatalf("audit operation = %v, want %s", event["operation"], operation.audit)
			}
		})
		t.Run("reader/"+operation.name, func(t *testing.T) {
			handler, trail := auditedPlatform(t, organizationWebhookHandler(organization, identity.RolePublisher, &scopeRecordingWebhooks{}))
			if response := call(t, handler, operation.method, operation.path, operation.body, testToken); response.Code != http.StatusForbidden {
				t.Fatalf("publisher status = %d, want 403", response.Code)
			}
			if event := trail.response(t); event["reason"] != "role_refused" {
				t.Fatalf("audit = %#v", event)
			}
		})
		t.Run("project maintainer/"+operation.name, func(t *testing.T) {
			project := identity.Scope{OrganizationID: uuid.MustParse(testOrgID), ProjectID: uuid.MustParse(testProjID)}
			handler, trail := auditedPlatform(t, organizationWebhookHandler(project, identity.RoleMaintainer, &scopeRecordingWebhooks{}))
			if response := call(t, handler, operation.method, operation.path, operation.body, testToken); response.Code != http.StatusNotFound {
				t.Fatalf("a project maintainer reached organization webhooks: %d", response.Code)
			}
			if event := trail.response(t); event["reason"] != "tenancy_refused" {
				t.Fatalf("audit = %#v", event)
			}
		})
	}
}
