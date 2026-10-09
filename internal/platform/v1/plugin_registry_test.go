package v1

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

type pluginRegistryOperation struct {
	name, action, audit, successReason string
	want                               int
	state                              store.PluginRegistry
}

func pluginRegistryOperations() []pluginRegistryOperation {
	return []pluginRegistryOperation{
		{name: "enable", action: "enable", audit: "plugin_registry.enable", successReason: "enabled", want: http.StatusCreated},
		{name: "expose", action: "expose", audit: "plugin_registry.expose", successReason: "exposed", want: http.StatusOK, state: store.PluginRegistry{Enabled: true}},
		{name: "unexpose", action: "unexpose", audit: "plugin_registry.unexpose", successReason: "unexposed", want: http.StatusOK, state: store.PluginRegistry{Enabled: true, Exposed: true}},
		{name: "disable", action: "disable", audit: "plugin_registry.disable", successReason: "disabled", want: http.StatusNoContent, state: store.PluginRegistry{Enabled: true}},
	}
}

func pluginRegistryPath(action string) string {
	path := "/api/v1/organizations/" + testOrgID + "/plugin-registry"
	if action != "" {
		path += "/" + action
	}
	return path
}

func pluginRegistryRepository(state store.PluginRegistry) *fakeTenancyRepository {
	return &fakeTenancyRepository{
		organizations: []store.Organization{{
			ID: testOrgID, Name: "organization", CreatedAt: initTestTime,
		}},
		pluginRegistry: state,
	}
}

func pluginRegistryHandler(role identity.Role, repository *fakeTenancyRepository) http.Handler {
	actor := testRoles{
		role:  role,
		scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
	}
	return newHandler(
		repository, &fakeInstanceRepository{}, testAuth{}, actor, testLogger(),
		func() time.Time { return initTestTime },
	)
}

func TestPluginRegistryRoleAxisAndAuditEvents(t *testing.T) {
	for _, role := range []identity.Role{
		identity.RoleReader, identity.RoleBuilder, identity.RolePublisher,
	} {
		for _, operation := range pluginRegistryOperations() {
			t.Run(string(role)+"/"+operation.name, func(t *testing.T) {
				handler, trail := auditedPlatform(t, pluginRegistryHandler(
					role, pluginRegistryRepository(operation.state),
				))
				response := call(t, handler, http.MethodPost, pluginRegistryPath(operation.action), nil, testToken)
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

	for _, operation := range pluginRegistryOperations() {
		t.Run("maintainer/"+operation.name, func(t *testing.T) {
			handler, trail := auditedPlatform(t, pluginRegistryHandler(
				identity.RoleMaintainer, pluginRegistryRepository(operation.state),
			))
			response := call(t, handler, http.MethodPost, pluginRegistryPath(operation.action), nil, testToken)
			if response.Code != operation.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, operation.want, response.Body)
			}
			event := trail.response(t)
			if event["operation"] != operation.audit || event["outcome"] != "success" ||
				event["reason"] != operation.successReason || event["target_id"] != testOrgID {
				t.Fatalf("audit = %#v", event)
			}
		})
	}
}

func TestPluginRegistryTenancyAxis(t *testing.T) {
	target := pluginRegistryRepository(store.PluginRegistry{})
	cases := []struct {
		name       string
		roles      testRoles
		repository *fakeTenancyRepository
	}{
		{
			name: "foreign organization",
			roles: testRoles{
				role:  identity.RoleMaintainer,
				scope: identity.Scope{OrganizationID: uuid.New()},
			},
			repository: target,
		},
		{
			name: "absent organization",
			roles: testRoles{
				role:  identity.RoleMaintainer,
				scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
			},
			repository: &fakeTenancyRepository{},
		},
		{
			name: "project-bound maintainer",
			roles: testRoles{
				role: identity.RoleMaintainer,
				scope: identity.Scope{
					OrganizationID: uuid.MustParse(testOrgID), ProjectID: uuid.MustParse(testProjID),
				},
			},
			repository: target,
		},
		{
			name: "bucket-scoped caller",
			roles: testRoles{
				role: identity.RolePublisher,
				scope: identity.Scope{
					OrganizationID: uuid.MustParse(testOrgID), ProjectID: uuid.MustParse(testProjID),
					BucketID: testBucketID,
				},
			},
			repository: target,
		},
	}
	for _, tc := range cases {
		for _, operation := range pluginRegistryOperations() {
			t.Run(tc.name+"/"+operation.name, func(t *testing.T) {
				handler := newHandler(
					tc.repository, &fakeInstanceRepository{}, testAuth{}, tc.roles,
					testLogger(), func() time.Time { return initTestTime },
				)
				handler, trail := auditedPlatform(t, handler)
				response := call(t, handler, http.MethodPost, pluginRegistryPath(operation.action), nil, testToken)
				if response.Code != http.StatusNotFound || response.Body.String() != "{\"message\":\"not found\"}\n" {
					t.Fatalf("status/body = %d %q, want identical 404", response.Code, response.Body.String())
				}
				if event := trail.response(t); event["outcome"] != "refused" ||
					event["reason"] != "tenancy_refused" {
					t.Fatalf("audit = %#v", event)
				}
			})
		}
	}
}

func TestGetPluginRegistryNeverEnabled(t *testing.T) {
	handler, trail := auditedPlatform(t, pluginRegistryHandler(
		identity.RoleReader, pluginRegistryRepository(store.PluginRegistry{}),
	))
	response := call(t, handler, http.MethodGet, pluginRegistryPath(""), nil, testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body)
	}
	var state store.PluginRegistry
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state != (store.PluginRegistry{}) {
		t.Fatalf("state = %#v, want disabled and unexposed", state)
	}
	if event := trail.response(t); event["operation"] != "plugin_registry.read" ||
		event["outcome"] != "success" || event["target_id"] != testOrgID {
		t.Fatalf("audit = %#v", event)
	}
}

func TestPluginRegistryTransitionsAndConflicts(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{})
	handler, trail := auditedPlatform(t, pluginRegistryHandler(identity.RoleMaintainer, repository))

	assert := func(action string, want int, message, outcome, reason string) {
		t.Helper()
		response := call(t, handler, http.MethodPost, pluginRegistryPath(action), nil, testToken)
		if response.Code != want {
			t.Fatalf("%s status = %d, want %d: %s", action, response.Code, want, response.Body)
		}
		if message != "" && !strings.Contains(response.Body.String(), `"message":"`+message+`"`) {
			t.Fatalf("%s body = %s, want message %q", action, response.Body, message)
		}
		event := trail.response(t)
		if event["outcome"] != outcome || event["reason"] != reason {
			t.Fatalf("%s audit = %#v", action, event)
		}
	}

	assert("expose", http.StatusConflict, "plugin registry is not enabled", "failure", "not_enabled")
	assert("unexpose", http.StatusConflict, "plugin registry is not enabled", "failure", "not_enabled")
	assert("disable", http.StatusConflict, "plugin registry is not enabled", "failure", "not_enabled")
	assert("enable", http.StatusCreated, "", "success", "enabled")
	assert("enable", http.StatusConflict, "plugin registry is already enabled", "failure", "already_enabled")
	assert("unexpose", http.StatusConflict, "plugin registry is not exposed", "failure", "not_exposed")
	assert("expose", http.StatusOK, "", "success", "exposed")
	assert("expose", http.StatusConflict, "plugin registry is already exposed", "failure", "already_exposed")
	assert("disable", http.StatusConflict, "plugin registry is still exposed; unexpose first", "failure", "still_exposed")
	assert("unexpose", http.StatusOK, "", "success", "unexposed")
	assert("disable", http.StatusNoContent, "", "success", "disabled")
}
