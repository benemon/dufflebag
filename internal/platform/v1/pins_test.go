package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/google/uuid"
)

type pinIdentity struct {
	id    string
	role  identity.Role
	scope identity.Scope
}

func (p pinIdentity) Verify(string) (identity.Verified, error) {
	return identity.Verified{
		PrincipalID: p.id, SecretID: testSecretID,
		AuthTime: initTestTime, ExpiresAt: initTestTime.Add(5 * time.Minute),
	}, nil
}

func (p pinIdentity) VerifyExpired(token string) (identity.Verified, error) { return p.Verify(token) }
func (pinIdentity) Reissue(*identity.Principal, string, time.Time) (string, error) {
	return testToken, nil
}

func (p pinIdentity) GetPrincipalByID(context.Context, string) (*identity.Principal, error) {
	return identity.RestorePrincipal(
		p.id, p.id, "client-"+p.id, p.scope, p.role, initTestTime, testSecrets(),
	)
}

func (pinIdentity) TouchSecretLastUsed(context.Context, string, time.Time) error { return nil }

func pinPath(bucket string) string {
	path := "/api/v1/organizations/" + testOrgID + "/projects/" + testProjID + "/pins"
	if bucket != "" {
		path += "/" + bucket
	}
	return path
}

func pinServer(repository *fakeTenancyRepository, actor pinIdentity, now func() time.Time) http.Handler {
	return newHandler(
		repository, &fakeInstanceRepository{}, actor, actor, testLogger(), now,
	)
}

func TestPinsRoleAxis(t *testing.T) {
	scope := identity.Scope{
		OrganizationID: uuid.MustParse(testOrgID),
		ProjectID:      uuid.MustParse(testProjID),
	}
	at := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	repository := &fakeTenancyRepository{}

	reader, readerTrail := auditedPlatform(t, pinServer(repository,
		pinIdentity{id: "reader-a", role: identity.RoleReader, scope: scope}, func() time.Time { return at }))
	if response := call(t, reader, http.MethodGet, pinPath(""), nil, testToken); response.Code != http.StatusOK {
		t.Fatalf("reader list = %d, want 200: %s", response.Code, response.Body)
	}
	if audit := readerTrail.response(t); audit["operation"] != "pin.list" || audit["outcome"] != "success" {
		t.Fatalf("reader list audit = %#v", audit)
	}

	reader, readerTrail = auditedPlatform(t, pinServer(repository,
		pinIdentity{id: "reader-a", role: identity.RoleReader, scope: scope}, func() time.Time { return at }))
	if response := call(t, reader, http.MethodPut, pinPath("images"), nil, testToken); response.Code != http.StatusForbidden {
		t.Fatalf("reader set = %d, want 403: %s", response.Code, response.Body)
	}
	if audit := readerTrail.response(t); audit["operation"] != "pin.set" ||
		audit["outcome"] != "refused" || audit["reason"] != "role_refused" {
		t.Fatalf("reader set audit = %#v", audit)
	}
	if response := call(t, reader, http.MethodDelete, pinPath("images"), nil, testToken); response.Code != http.StatusForbidden {
		t.Fatalf("reader delete = %d, want 403: %s", response.Code, response.Body)
	}
	if audit := readerTrail.response(t); audit["operation"] != "pin.delete" ||
		audit["outcome"] != "refused" || audit["reason"] != "role_refused" {
		t.Fatalf("reader delete audit = %#v", audit)
	}

	builder, builderTrail := auditedPlatform(t, pinServer(repository,
		pinIdentity{id: "builder-a", role: identity.RoleBuilder, scope: scope}, func() time.Time { return at }))
	var pin Pin
	response := call(t, builder, http.MethodPut, pinPath("images"), nil, testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("builder set = %d, want 200: %s", response.Code, response.Body)
	}
	if err := json.Unmarshal(response.Body.Bytes(), &pin); err != nil {
		t.Fatal(err)
	}
	if pin.PinnedBy == nil || *pin.PinnedBy != "builder-a" || !pin.PinnedAt.Equal(at) {
		t.Fatalf("set pin = %#v", pin)
	}
	if audit := builderTrail.response(t); audit["operation"] != "pin.set" || audit["outcome"] != "success" {
		t.Fatalf("builder set audit = %#v", audit)
	}
	if response := call(t, builder, http.MethodDelete, pinPath("images"), nil, testToken); response.Code != http.StatusNoContent {
		t.Fatalf("builder delete = %d, want 204: %s", response.Code, response.Body)
	}
	if audit := builderTrail.response(t); audit["operation"] != "pin.delete" || audit["outcome"] != "success" {
		t.Fatalf("builder delete audit = %#v", audit)
	}
}

func TestPinsTenancyAxis(t *testing.T) {
	foreign := pinIdentity{
		id:   "foreign-reader",
		role: identity.RoleBuilder,
		scope: identity.Scope{
			OrganizationID: uuid.New(),
			ProjectID:      uuid.New(),
		},
	}
	for _, request := range []struct{ method, bucket string }{
		{http.MethodGet, ""}, {http.MethodPut, "images"}, {http.MethodDelete, "images"},
	} {
		handler, trail := auditedPlatform(t, pinServer(&fakeTenancyRepository{}, foreign, time.Now))
		response := call(t, handler, request.method, pinPath(request.bucket), nil, testToken)
		if response.Code != http.StatusNotFound {
			t.Fatalf("foreign %s = %d, want 404: %s", request.method, response.Code, response.Body)
		}
		if audit := trail.response(t); audit["outcome"] != "refused" || audit["reason"] != "tenancy_refused" {
			t.Fatalf("foreign %s audit = %#v", request.method, audit)
		}
	}
}

func TestPinIsVisibleToSecondPrincipalAndSetIsIdempotent(t *testing.T) {
	scope := identity.Scope{
		OrganizationID: uuid.MustParse(testOrgID), ProjectID: uuid.MustParse(testProjID),
	}
	original := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	now := original
	repository := &fakeTenancyRepository{}
	principalA := pinServer(repository,
		pinIdentity{id: "principal-a", role: identity.RoleBuilder, scope: scope}, func() time.Time { return now })

	first := call(t, principalA, http.MethodPut, pinPath("images"), nil, testToken)
	if first.Code != http.StatusOK {
		t.Fatalf("first set = %d: %s", first.Code, first.Body)
	}
	now = original.Add(time.Hour)
	second := call(t, principalA, http.MethodPut, pinPath("images"), nil, testToken)
	if second.Code != http.StatusOK {
		t.Fatalf("second set = %d: %s", second.Code, second.Body)
	}
	var repinned Pin
	if err := json.Unmarshal(second.Body.Bytes(), &repinned); err != nil {
		t.Fatal(err)
	}
	if !repinned.PinnedAt.Equal(original) || repinned.PinnedBy == nil || *repinned.PinnedBy != "principal-a" {
		t.Fatalf("re-pin changed attribution = %#v", repinned)
	}

	principalB := pinServer(repository,
		pinIdentity{id: "principal-b", role: identity.RoleReader, scope: scope}, time.Now)
	listed := call(t, principalB, http.MethodGet, pinPath(""), nil, testToken)
	if listed.Code != http.StatusOK {
		t.Fatalf("second principal list = %d: %s", listed.Code, listed.Body)
	}
	var body ListPins200JSONResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Pins) != 1 || body.Pins[0].PinnedBy == nil || *body.Pins[0].PinnedBy != "principal-a" {
		t.Fatalf("second principal saw %#v", body.Pins)
	}

	for range 2 {
		if response := call(t, principalA, http.MethodDelete, pinPath("images"), nil, testToken); response.Code != http.StatusNoContent {
			t.Fatalf("idempotent delete = %d: %s", response.Code, response.Body)
		}
	}
}

func TestSetPinUnknownBucketReturnsNotFound(t *testing.T) {
	scope := identity.Scope{
		OrganizationID: uuid.MustParse(testOrgID), ProjectID: uuid.MustParse(testProjID),
	}
	handler := pinServer(
		&fakeTenancyRepository{setPinErr: registry.ErrNotFound},
		pinIdentity{id: "builder-a", role: identity.RoleBuilder, scope: scope}, time.Now,
	)
	response := call(t, handler, http.MethodPut, pinPath("missing"), nil, testToken)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown bucket = %d, want 404: %s", response.Code, response.Body)
	}
	var body Error
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Message != "No such bucket in this project" {
		t.Fatalf("unknown bucket message = %q", body.Message)
	}
}
