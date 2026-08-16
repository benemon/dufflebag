package v1

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/google/uuid"
)

func instanceServer(roles testRoles) http.Handler {
	instance := &fakeInstanceRepository{claimed: true}
	principal, err := identity.NewPrincipal(
		"initial", "initial administrator", "client", identity.Scope{},
		identity.RoleRoot, initTestTime,
	)
	if err != nil {
		panic(err)
	}
	instance.principal = principal
	return newHandlerWithBuildAndAudit(
		&fakeTenancyRepository{objectStorageState: "ok"}, instance,
		testAuth{}, roles, testLogger(), nil, nil, nil, nil,
		BuildInfo{
			Version: "1.2.3", Commit: "abc123",
			APIVersions: []string{"/packer/2023-01-01", "/api/v1"},
		},
		func() time.Time { return initTestTime },
	)
}

func TestGetInstanceRequiresAuthentication(t *testing.T) {
	handler := instanceServer(testRoles{
		role:  identity.RoleReader,
		scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
	})
	response := call(t, handler, http.MethodGet, "/api/v1/instance", nil, "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous instance read = %d, want 401; body %s", response.Code, response.Body)
	}
}

func TestGetInstanceAllowsReaderAndReturnsDerivedShape(t *testing.T) {
	handler := instanceServer(testRoles{
		role:  identity.RoleReader,
		scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
	})
	response := call(t, handler, http.MethodGet, "/api/v1/instance", nil, testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("reader instance read = %d, want 200; body %s", response.Code, response.Body)
	}
	var body Instance
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode instance: %v", err)
	}
	if body.Version != "1.2.3" || body.Commit != "abc123" {
		t.Fatalf("build identity = %q/%q", body.Version, body.Commit)
	}
	if len(body.ApiVersions) != 2 || body.ApiVersions[0] != "/packer/2023-01-01" {
		t.Fatalf("api versions = %v", body.ApiVersions)
	}
	if body.InitializedAt == nil || !body.InitializedAt.Equal(initTestTime) {
		t.Fatalf("initialized_at = %v, want %v", body.InitializedAt, initTestTime)
	}
	if !body.Store || body.ObjectStorage != InstanceObjectStorageOk || body.Audit != InstanceAuditDisabled ||
		body.Encryption != InstanceEncryptionUnconfigured || body.Scanner.Configured || body.Scanner.Adapter != nil {
		t.Fatalf("backing state = %#v", body)
	}
}

func TestGetInstanceReportsScannerAdapterWithoutRootOnlyEndpoint(t *testing.T) {
	handler := scannerServer(testRoles{
		role:  identity.RoleReader,
		scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
	}, healthyScanner())
	response := call(t, handler, http.MethodGet, "/api/v1/instance", nil, testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("reader instance read = %d, want 200; body %s", response.Code, response.Body)
	}
	var body Instance
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode instance: %v", err)
	}
	if !body.Scanner.Configured || body.Scanner.Adapter == nil || *body.Scanner.Adapter != "osv" {
		t.Fatalf("scanner = %#v, want configured osv adapter", body.Scanner)
	}
	if bytes.Contains(response.Body.Bytes(), []byte("endpoint")) ||
		bytes.Contains(response.Body.Bytes(), []byte("api.osv.dev")) {
		t.Fatalf("instance exposed root-only scanner endpoint: %s", response.Body)
	}
}

func TestGetSelfReturnsServerResolvedBinding(t *testing.T) {
	handler := instanceServer(testRoles{
		role: identity.RoleReader,
		scope: identity.Scope{
			OrganizationID: uuid.MustParse(testOrgID),
			ProjectID:      uuid.MustParse(testProjID),
			BucketID:       testBucketID,
		},
	})
	response := call(t, handler, http.MethodGet, "/api/v1/self", nil, testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("self read = %d, want 200; body %s", response.Code, response.Body)
	}
	var body Self
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode self: %v", err)
	}
	if body.PrincipalId != testPrincID || body.Name != "test" || body.Role != Reader {
		t.Fatalf("self = %#v", body)
	}
	if body.OrganizationId == nil || body.OrganizationId.String() != testOrgID ||
		body.ProjectId == nil || body.ProjectId.String() != testProjID {
		t.Fatalf("self binding = %#v", body)
	}
	if body.BucketId == nil || *body.BucketId != testBucketID {
		t.Fatalf("self bucket_id = %v, want %s", body.BucketId, testBucketID)
	}
}

func TestGetSelfRequiresAuthentication(t *testing.T) {
	response := call(t, instanceServer(testRoles{}), http.MethodGet, "/api/v1/self", nil, "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous self read = %d, want 401; body %s", response.Code, response.Body)
	}
}
