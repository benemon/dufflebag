package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/keyring"
	"github.com/google/uuid"
)

type fakeEncryptionService struct {
	state        string
	latestKEK    string
	refreshedKEK string
	entries      []keyring.Entry
	rewrapErr    error
	rotateErr    error
}

func (s *fakeEncryptionService) State() string     { return s.state }
func (s *fakeEncryptionService) LatestKEK() string { return s.latestKEK }
func (s *fakeEncryptionService) RefreshLatestKEK(context.Context) string {
	s.latestKEK = s.refreshedKEK
	return s.latestKEK
}
func (s *fakeEncryptionService) Entries(context.Context) ([]keyring.Entry, error) {
	return s.entries, nil
}
func (s *fakeEncryptionService) Rewrap(context.Context) ([]keyring.Entry, error) {
	return s.entries, s.rewrapErr
}
func (s *fakeEncryptionService) Rotate(context.Context) ([]keyring.Entry, error) {
	return s.entries, s.rotateErr
}

func encryptionHandler(role identity.Role, encryption EncryptionService) http.Handler {
	scope := identity.Scope{}
	if role != identity.RoleRoot {
		scope.OrganizationID = uuid.MustParse(testOrgID)
	}
	return newHandlerWithBuildAndAudit(
		&fakeTenancyRepository{}, &fakeInstanceRepository{claimed: true},
		testAuth{}, testRoles{role: role, scope: scope}, testLogger(), nil, nil, encryption,
		nil, BuildInfo{}, func() time.Time { return initTestTime },
	)
}

func configuredEncryption() *fakeEncryptionService {
	return &fakeEncryptionService{state: "ok", entries: []keyring.Entry{{
		Purpose: keyring.PurposePayload, Version: 2, KEKRef: "v3", WrappedAt: initTestTime,
	}}}
}

func TestEncryptionOperationsRequireRootAndAuditRoleRefusal(t *testing.T) {
	operations := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/encryption"},
		{http.MethodPost, "/api/v1/encryption/rewrap"},
		{http.MethodPost, "/api/v1/encryption/rotate"},
	}
	for _, operation := range operations {
		t.Run(operation.path, func(t *testing.T) {
			root := encryptionHandler(identity.RoleRoot, configuredEncryption())
			if response := call(t, root, operation.method, operation.path, nil, testToken); response.Code == http.StatusForbidden {
				t.Fatalf("root was role-refused: %s", response.Body)
			}
			for _, role := range []identity.Role{identity.RoleMaintainer, identity.RoleReader} {
				handler, trail := auditedPlatform(t, encryptionHandler(role, configuredEncryption()))
				response := call(t, handler, operation.method, operation.path, nil, testToken)
				if response.Code != http.StatusForbidden {
					t.Fatalf("%s = %d, want 403; body %s", role, response.Code, response.Body)
				}
				if got := trail.response(t)["reason"]; got != "role_refused" {
					t.Fatalf("%s audit reason = %v, want role_refused", role, got)
				}
			}
		})
	}
}

func TestEncryptionUnconfiguredShapes(t *testing.T) {
	handler := encryptionHandler(identity.RoleRoot, nil)
	response := call(t, handler, http.MethodGet, "/api/v1/encryption", nil, testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("GET unconfigured = %d, want 200: %s", response.Code, response.Body)
	}
	var body Encryption
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.State != EncryptionStateUnconfigured || body.Keyring == nil || len(body.Keyring) != 0 {
		t.Fatalf("unconfigured body = %#v", body)
	}
	for _, path := range []string{"/api/v1/encryption/rewrap", "/api/v1/encryption/rotate"} {
		response := call(t, handler, http.MethodPost, path, nil, testToken)
		if response.Code != http.StatusConflict {
			t.Fatalf("POST %s unconfigured = %d, want 409: %s", path, response.Code, response.Body)
		}
		var failure Error
		if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		if failure.Message != "this instance does not have encryption at rest" {
			t.Fatalf("POST %s message = %q", path, failure.Message)
		}
	}
}

func TestGetEncryptionIncludesLatestKEKOnlyWhenKnown(t *testing.T) {
	for _, test := range []struct {
		name      string
		latestKEK string
	}{
		{name: "known", latestKEK: "v6"},
		{name: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := configuredEncryption()
			service.refreshedKEK = test.latestKEK
			response := call(t, encryptionHandler(identity.RoleRoot, service), http.MethodGet, "/api/v1/encryption", nil, testToken)
			if response.Code != http.StatusOK {
				t.Fatalf("GET encryption = %d, want 200: %s", response.Code, response.Body)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			encoded, present := body["kek_latest"]
			if test.latestKEK == "" {
				if present {
					t.Fatalf("unknown kek_latest was emitted as %s", encoded)
				}
				return
			}
			if !present {
				t.Fatal("known kek_latest was omitted")
			}
			var latest string
			if err := json.Unmarshal(encoded, &latest); err != nil {
				t.Fatal(err)
			}
			if latest != test.latestKEK {
				t.Fatalf("kek_latest = %q, want %q", latest, test.latestKEK)
			}
		})
	}
}

func TestGetEncryptionRefreshesLatestKEKRatherThanServingRemembered(t *testing.T) {
	service := configuredEncryption()
	service.latestKEK = "v5"
	service.refreshedKEK = "v6"
	response := call(t, encryptionHandler(identity.RoleRoot, service), http.MethodGet, "/api/v1/encryption", nil, testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("GET encryption = %d, want 200: %s", response.Code, response.Body)
	}
	var body struct {
		KekLatest string `json:"kek_latest"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.KekLatest != "v6" {
		t.Fatalf("kek_latest = %q, want the refreshed v6, not the remembered v5", body.KekLatest)
	}
}

func TestRewrapProviderFailureIsGenericBadGateway(t *testing.T) {
	service := configuredEncryption()
	service.rewrapErr = errors.Join(keyring.ErrKeyService, errors.New("canary vault host and token detail"))
	handler, trail := auditedPlatform(t, encryptionHandler(identity.RoleRoot, service))
	response := call(t, handler, http.MethodPost, "/api/v1/encryption/rewrap", nil, testToken)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("rewrap failure = %d, want 502: %s", response.Code, response.Body)
	}
	if got := response.Body.String(); got == "" || strings.Contains(got, "canary") {
		t.Fatalf("rewrap leaked provider detail: %s", got)
	}
	if got := trail.response(t)["reason"]; got != "key_service_failure" {
		t.Fatalf("audit reason = %v, want key_service_failure", got)
	}
}

func TestRotateConflictIsAuditedConflict(t *testing.T) {
	service := configuredEncryption()
	service.rotateErr = keyring.ErrRotationConflict
	handler, trail := auditedPlatform(t, encryptionHandler(identity.RoleRoot, service))
	response := call(t, handler, http.MethodPost, "/api/v1/encryption/rotate", nil, testToken)
	if response.Code != http.StatusConflict {
		t.Fatalf("rotate conflict = %d, want 409: %s", response.Code, response.Body)
	}
	if got := trail.response(t)["reason"]; got != "rotation_conflict" {
		t.Fatalf("audit reason = %v, want rotation_conflict", got)
	}
}

func TestHealthAndInstanceReportEncryptionWithoutFailingReadiness(t *testing.T) {
	for _, state := range []string{"unconfigured", "ok", "degraded"} {
		t.Run(state, func(t *testing.T) {
			var service EncryptionService
			if state != "unconfigured" {
				service = &fakeEncryptionService{state: state}
			}
			handler := encryptionHandler(identity.RoleRoot, service)
			health := call(t, handler, http.MethodGet, "/sys/health", nil, "")
			if health.Code != http.StatusOK {
				t.Fatalf("health encryption %s = %d, want 200: %s", state, health.Code, health.Body)
			}
			var healthBody Health
			if err := json.Unmarshal(health.Body.Bytes(), &healthBody); err != nil {
				t.Fatal(err)
			}
			if string(healthBody.Encryption) != state {
				t.Fatalf("health encryption = %q, want %q", healthBody.Encryption, state)
			}

			instance := call(t, handler, http.MethodGet, "/api/v1/instance", nil, testToken)
			if instance.Code != http.StatusOK {
				t.Fatalf("instance encryption %s = %d: %s", state, instance.Code, instance.Body)
			}
			var instanceBody Instance
			if err := json.Unmarshal(instance.Body.Bytes(), &instanceBody); err != nil {
				t.Fatal(err)
			}
			if string(instanceBody.Encryption) != state {
				t.Fatalf("instance encryption = %q, want %q", instanceBody.Encryption, state)
			}
		})
	}
}
