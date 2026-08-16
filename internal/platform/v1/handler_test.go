package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

const (
	testOrganizationID = "00000000-0000-4000-8000-000000000001"
	testProjectID      = "00000000-0000-4000-8000-000000000101"
	testBucketID       = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
)

func TestTenancyHandlers(t *testing.T) {
	at := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	repository := &fakeTenancyRepository{
		organizations: []store.Organization{{
			ID:        testOrganizationID,
			Name:      "platform",
			CreatedAt: at,
		}},
		projects: []store.Project{{
			ID:             testProjectID,
			OrganizationID: testOrganizationID,
			Name:           "images",
			CreatedAt:      at,
		}},
	}
	handler := newHandler(repository, &fakeInstanceRepository{}, testAuth{}, testRoles{}, testLogger(), func() time.Time { return at })

	var listedOrganizations ListOrganizations200JSONResponse
	requestJSON(t, handler, http.MethodGet, "/api/v1/organizations", nil, http.StatusOK, &listedOrganizations)
	if len(listedOrganizations.Organizations) != 1 ||
		listedOrganizations.Organizations[0].Id.String() != testOrganizationID {
		t.Fatalf("ListOrganizations response = %#v", listedOrganizations)
	}

	var createdOrganization Organization
	requestJSON(
		t,
		handler,
		http.MethodPost,
		"/api/v1/organizations",
		map[string]string{"name": "security"},
		http.StatusCreated,
		&createdOrganization,
	)
	if createdOrganization.Id.String() == "" ||
		createdOrganization.Name != "security" ||
		!createdOrganization.CreatedAt.Equal(at) {
		t.Fatalf("CreateOrganization response = %#v", createdOrganization)
	}

	var gotOrganization Organization
	requestJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/organizations/"+testOrganizationID,
		nil,
		http.StatusOK,
		&gotOrganization,
	)
	if gotOrganization.Id.String() != testOrganizationID || gotOrganization.Name != "platform" {
		t.Fatalf("GetOrganization response = %#v", gotOrganization)
	}
	requestJSON(
		t,
		handler,
		http.MethodDelete,
		"/api/v1/organizations/"+testOrganizationID,
		nil,
		http.StatusNoContent,
		nil,
	)

	var listedProjects ListProjects200JSONResponse
	requestJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/organizations/"+testOrganizationID+"/projects",
		nil,
		http.StatusOK,
		&listedProjects,
	)
	if len(listedProjects.Projects) != 1 ||
		listedProjects.Projects[0].Id.String() != testProjectID ||
		listedProjects.Projects[0].OrganizationId.String() != testOrganizationID {
		t.Fatalf("ListProjects response = %#v", listedProjects)
	}

	var createdProject Project
	requestJSON(
		t,
		handler,
		http.MethodPost,
		"/api/v1/organizations/"+testOrganizationID+"/projects",
		map[string]string{"name": "releases"},
		http.StatusCreated,
		&createdProject,
	)
	if createdProject.Id.String() == "" ||
		createdProject.OrganizationId.String() != testOrganizationID ||
		createdProject.Name != "releases" ||
		!createdProject.CreatedAt.Equal(at) {
		t.Fatalf("CreateProject response = %#v", createdProject)
	}

	var gotProject Project
	requestJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/organizations/"+testOrganizationID+"/projects/"+testProjectID,
		nil,
		http.StatusOK,
		&gotProject,
	)
	if gotProject.Id.String() != testProjectID ||
		gotProject.OrganizationId.String() != testOrganizationID {
		t.Fatalf("GetProject response = %#v", gotProject)
	}
	requestJSON(
		t,
		handler,
		http.MethodDelete,
		"/api/v1/organizations/"+testOrganizationID+"/projects/"+testProjectID,
		nil,
		http.StatusNoContent,
		nil,
	)
}

func TestTenancyHandlerErrorsUsePlatformShape(t *testing.T) {
	at := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		method     string
		path       string
		body       any
		repository *fakeTenancyRepository
		status     int
		message    string
	}{
		{
			name:       "duplicate organization",
			method:     http.MethodPost,
			path:       "/api/v1/organizations",
			body:       map[string]string{"name": "platform"},
			repository: &fakeTenancyRepository{createOrganizationErr: registry.ErrConflict},
			status:     http.StatusConflict,
			message:    "organization already exists",
		},
		{
			name:       "organization not found",
			method:     http.MethodGet,
			path:       "/api/v1/organizations/" + testOrganizationID,
			repository: &fakeTenancyRepository{getOrganizationErr: registry.ErrNotFound},
			status:     http.StatusNotFound,
			message:    "organization not found",
		},
		{
			name:       "organization contains projects",
			method:     http.MethodDelete,
			path:       "/api/v1/organizations/" + testOrganizationID,
			repository: &fakeTenancyRepository{deleteOrganizationErr: registry.ErrConflict},
			status:     http.StatusConflict,
			message:    "organization still has projects or organization-scoped principals",
		},
		{
			name:       "duplicate project",
			method:     http.MethodPost,
			path:       "/api/v1/organizations/" + testOrganizationID + "/projects",
			body:       map[string]string{"name": "images"},
			repository: &fakeTenancyRepository{createProjectErr: registry.ErrConflict},
			status:     http.StatusConflict,
			message:    "project already exists or its organization is missing",
		},
		{
			name:       "project not found",
			method:     http.MethodGet,
			path:       "/api/v1/organizations/" + testOrganizationID + "/projects/" + testProjectID,
			repository: &fakeTenancyRepository{getProjectErr: registry.ErrNotFound},
			status:     http.StatusNotFound,
			message:    "project not found",
		},
		{
			name:       "project contains buckets",
			method:     http.MethodDelete,
			path:       "/api/v1/organizations/" + testOrganizationID + "/projects/" + testProjectID,
			repository: &fakeTenancyRepository{deleteProjectErr: registry.ErrConflict},
			status:     http.StatusConflict,
			message:    "project still has buckets or project-scoped principals",
		},
		{
			name:       "repository failure",
			method:     http.MethodGet,
			path:       "/api/v1/organizations",
			repository: &fakeTenancyRepository{listOrganizationsErr: errors.New("database unavailable")},
			status:     http.StatusInternalServerError,
			message:    "internal server error",
		},
		{
			name:       "invalid name",
			method:     http.MethodPost,
			path:       "/api/v1/organizations",
			body:       map[string]string{"name": ""},
			repository: &fakeTenancyRepository{},
			status:     http.StatusBadRequest,
			message:    "organization name must contain 1 to 200 characters",
		},
		{
			name:       "malformed organization id",
			method:     http.MethodGet,
			path:       "/api/v1/organizations/not-a-uuid",
			repository: &fakeTenancyRepository{},
			status:     http.StatusBadRequest,
			message:    "invalid request",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := newHandler(tc.repository, &fakeInstanceRepository{}, testAuth{}, testRoles{}, testLogger(), func() time.Time { return at })
			response := requestJSON(t, handler, tc.method, tc.path, tc.body, tc.status, nil)
			assertPlatformError(t, response, tc.message)
		})
	}
}

func TestPrincipalLifecycleNeverReturnsIssuedSecretAgain(t *testing.T) {
	at := time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC)
	repository := &fakeTenancyRepository{}
	handler := newHandler(
		repository, &fakeInstanceRepository{},
		testAuth{}, testRoles{}, testLogger(), func() time.Time { return at },
	)

	var created Principal
	requestJSON(
		t,
		handler,
		http.MethodPost,
		"/api/v1/principals",
		map[string]any{
			"name":            "build pipeline",
			"role":            "builder",
			"organization_id": testOrganizationID,
			"project_id":      testProjectID,
		},
		http.StatusCreated,
		&created,
	)
	// Creation mints nothing (duf-4ac). The principal exists and cannot yet
	// authenticate, which is an ordinary state rather than a half-built one.
	if len(created.Secrets) != 0 {
		t.Fatalf("created principal = %#v, want no secrets", created)
	}

	// Issuing the FIRST secret goes through the same endpoint as the second, so
	// there is one issuance path rather than a create-time special case.
	var firstSecret SecretIssued
	requestJSON(
		t, handler, http.MethodPost,
		"/api/v1/principals/"+created.Id+"/secrets",
		nil, http.StatusCreated, &firstSecret,
	)
	if firstSecret.Secret == "" {
		t.Fatalf("first issued secret = %#v", firstSecret)
	}
	issued := firstSecret.Secret

	// Listed at the scope it was created in: listing selects exactly one scope,
	// and the root caller's own standing (platform) would not include it.
	listed := requestJSON(
		t, handler, http.MethodGet,
		"/api/v1/principals?organization_id="+testOrganizationID+"&project_id="+testProjectID,
		nil, http.StatusOK, nil,
	)
	if !strings.Contains(listed.Body.String(), created.Id) {
		t.Fatalf("ListPrincipals at the created scope did not list %s: %s", created.Id, listed.Body)
	}
	if strings.Contains(listed.Body.String(), issued) {
		t.Fatalf("ListPrincipals returned previously issued secret %q: %s", issued, listed.Body)
	}
	got := requestJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/principals/"+created.Id,
		nil,
		http.StatusOK,
		nil,
	)
	if strings.Contains(got.Body.String(), issued) {
		t.Fatalf("GetPrincipal returned previously issued secret %q: %s", issued, got.Body)
	}

	var rotated SecretIssued
	requestJSON(
		t,
		handler,
		http.MethodPost,
		"/api/v1/principals/"+created.Id+"/secrets",
		nil,
		http.StatusCreated,
		&rotated,
	)
	if rotated.Secret == "" || rotated.Secret == issued {
		t.Fatalf("rotated secret = %#v", rotated)
	}
	requestJSON(
		t,
		handler,
		http.MethodDelete,
		"/api/v1/principals/"+created.Id+"/secrets/"+firstSecret.Id,
		nil,
		http.StatusNoContent,
		nil,
	)
	// A tenancy-scoped principal may be left with no secrets at all: a
	// maintainer issues a replacement for it, so revoking a leaked credential
	// need not wait for its successor (ADR-0004 as amended 2026-08-02). Only
	// root keeps the old refusal, which authorization_test covers.
	requestJSON(
		t,
		handler,
		http.MethodDelete,
		"/api/v1/principals/"+created.Id+"/secrets/"+rotated.Id,
		nil,
		http.StatusNoContent,
		nil,
	)

	requestJSON(
		t,
		handler,
		http.MethodDelete,
		"/api/v1/principals/"+created.Id,
		nil,
		http.StatusNoContent,
		nil,
	)
}

func TestCreatePrincipalWithBucket(t *testing.T) {
	at := time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC)
	repository := &fakeTenancyRepository{}
	handler := newHandler(
		repository, &fakeInstanceRepository{},
		testAuth{}, testRoles{}, testLogger(), func() time.Time { return at },
	)

	var created Principal
	requestJSON(
		t, handler, http.MethodPost, "/api/v1/principals",
		map[string]any{
			"name": "bucket pipeline", "role": "builder",
			"organization_id": testOrganizationID,
			"project_id":      testProjectID,
			"bucket_id":       testBucketID,
		},
		http.StatusCreated, &created,
	)
	if created.BucketId == nil || *created.BucketId != testBucketID {
		t.Fatalf("created principal bucket_id = %v, want %s", created.BucketId, testBucketID)
	}
	if len(repository.principals) != 1 || repository.principals[0].Scope.BucketID != testBucketID {
		t.Fatalf("stored principal scope = %#v", repository.principals)
	}
}

func TestCreatePrincipalWithMissingBucketIsNotFound(t *testing.T) {
	repository := &fakeTenancyRepository{createPrincipalErr: identity.ErrNotFound}
	handler := newHandler(
		repository, &fakeInstanceRepository{},
		testAuth{}, testRoles{}, testLogger(), func() time.Time { return time.Now().UTC() },
	)
	response := requestJSON(
		t, handler, http.MethodPost, "/api/v1/principals",
		map[string]any{
			"name": "missing bucket", "role": "builder",
			"organization_id": testOrganizationID,
			"project_id":      testProjectID,
			"bucket_id":       testBucketID,
		},
		http.StatusNotFound, nil,
	)
	assertPlatformError(t, response, "not found")
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func requestJSON(
	t *testing.T,
	handler http.Handler,
	method, path string,
	body any,
	wantStatus int,
	target any,
) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := authenticated(httptest.NewRequest(method, path, bytes.NewReader(encoded)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d: %s", method, path, response.Code, wantStatus, response.Body)
	}
	if target != nil {
		if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
			t.Fatalf("decode %s %s response: %v", method, path, err)
		}
	}
	return response
}

func assertPlatformError(t *testing.T, response *httptest.ResponseRecorder, wantMessage string) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body["message"] != wantMessage {
		t.Fatalf("error response = %#v, want message %q", body, wantMessage)
	}
	if _, ok := body["code"]; ok {
		t.Fatalf("error response used compatibility-plane shape: %#v", body)
	}
}

type fakeTenancyRepository struct {
	organizations         []store.Organization
	projects              []store.Project
	pins                  []store.Pin
	principals            []*identity.Principal
	listOrganizationsErr  error
	createOrganizationErr error
	getOrganizationErr    error
	deleteOrganizationErr error
	listProjectsErr       error
	createProjectErr      error
	getProjectErr         error
	deleteProjectErr      error
	listPinsErr           error
	setPinErr             error
	deletePinErr          error
	listPrincipalsErr     error
	createPrincipalErr    error
	deletePrincipalErr    error
	getPrincipalErr       error
	issueSecretErr        error
	revokeSecretErr       error
	objectStorageState    string
}

// ObjectStorageState defaults to the state of a registry nobody gave a bucket,
// because that is what most of these tests are: they never mention storage.
func (r *fakeTenancyRepository) ObjectStorageState() string {
	if r.objectStorageState == "" {
		return "unconfigured"
	}
	return r.objectStorageState
}

func (r *fakeTenancyRepository) ListOrganizationsForPrincipal(
	_ context.Context, principal *identity.Principal,
) ([]store.Organization, error) {
	if r.listOrganizationsErr != nil {
		return nil, r.listOrganizationsErr
	}
	if principal.Scope.PlatformScoped() {
		return r.organizations, nil
	}
	visible := make([]store.Organization, 0)
	for _, organization := range r.organizations {
		if organization.ID == principal.Scope.OrganizationID.String() {
			visible = append(visible, organization)
		}
	}
	return visible, nil
}

func (r *fakeTenancyRepository) CreateOrganization(
	_ context.Context,
	organization store.Organization,
) (*store.Organization, error) {
	if r.createOrganizationErr != nil {
		return nil, r.createOrganizationErr
	}
	r.organizations = append(r.organizations, organization)
	return &organization, nil
}

func (r *fakeTenancyRepository) GetOrganization(
	_ context.Context,
	id string,
) (*store.Organization, error) {
	if r.getOrganizationErr != nil {
		return nil, r.getOrganizationErr
	}
	for i := range r.organizations {
		if r.organizations[i].ID == id {
			return &r.organizations[i], nil
		}
	}
	return nil, registry.ErrNotFound
}

func (r *fakeTenancyRepository) DeleteOrganization(context.Context, string) error {
	return r.deleteOrganizationErr
}

func (r *fakeTenancyRepository) ListProjectsForPrincipal(
	_ context.Context, principal *identity.Principal, organization uuid.UUID,
) ([]store.Project, error) {
	if r.listProjectsErr != nil {
		return nil, r.listProjectsErr
	}
	visible := make([]store.Project, 0)
	organizationID := organization.String()
	for _, project := range r.projects {
		if project.OrganizationID != organizationID {
			continue
		}
		if principal.Scope.PlatformScoped() || principal.Scope.OrganizationScoped() ||
			project.ID == principal.Scope.ProjectID.String() {
			visible = append(visible, project)
		}
	}
	return visible, nil
}

func (r *fakeTenancyRepository) CreateProject(
	_ context.Context,
	project store.Project,
) (*store.Project, error) {
	if r.createProjectErr != nil {
		return nil, r.createProjectErr
	}
	r.projects = append(r.projects, project)
	return &project, nil
}

func (r *fakeTenancyRepository) GetProject(
	_ context.Context,
	organizationID, projectID string,
) (*store.Project, error) {
	if r.getProjectErr != nil {
		return nil, r.getProjectErr
	}
	for i := range r.projects {
		if r.projects[i].OrganizationID == organizationID && r.projects[i].ID == projectID {
			return &r.projects[i], nil
		}
	}
	return nil, registry.ErrNotFound
}

func (r *fakeTenancyRepository) DeleteProject(context.Context, string, string) error {
	return r.deleteProjectErr
}

func (r *fakeTenancyRepository) ListPins(context.Context, store.Tenant) ([]store.Pin, error) {
	if r.listPinsErr != nil {
		return nil, r.listPinsErr
	}
	return append([]store.Pin(nil), r.pins...), nil
}

func (r *fakeTenancyRepository) SetPin(
	_ context.Context, _ store.Tenant, bucketName, pinnedBy string, pinnedAt time.Time,
) (*store.Pin, error) {
	if r.setPinErr != nil {
		return nil, r.setPinErr
	}
	for i := range r.pins {
		if r.pins[i].BucketName == bucketName {
			return &r.pins[i], nil
		}
	}
	r.pins = append(r.pins, store.Pin{
		BucketName: bucketName, PinnedAt: pinnedAt, PinnedBy: pinnedBy,
	})
	return &r.pins[len(r.pins)-1], nil
}

func (r *fakeTenancyRepository) DeletePin(
	_ context.Context, _ store.Tenant, bucketName string,
) error {
	if r.deletePinErr != nil {
		return r.deletePinErr
	}
	for i := range r.pins {
		if r.pins[i].BucketName == bucketName {
			r.pins = append(r.pins[:i:i], r.pins[i+1:]...)
			break
		}
	}
	return nil
}

// ListPrincipals filters exactly like the real repository: principals bound to
// EXACTLY the selected scope, never a subtree (duf-4qr). A fake returning
// everything would let a handler that ignores the selection pass every test.
func (r *fakeTenancyRepository) ListPrincipals(
	_ context.Context, _ *identity.Principal, selection identity.Scope,
) ([]*identity.Principal, error) {
	if r.listPrincipalsErr != nil {
		return nil, r.listPrincipalsErr
	}
	visible := make([]*identity.Principal, 0, len(r.principals))
	for _, principal := range r.principals {
		if principal.Scope == selection {
			visible = append(visible, principal)
		}
	}
	return visible, nil
}

func (r *fakeTenancyRepository) CreatePrincipal(
	_ context.Context, principal *identity.Principal,
) error {
	if r.createPrincipalErr != nil {
		return r.createPrincipalErr
	}
	r.principals = append(r.principals, principal)
	return nil
}

func (r *fakeTenancyRepository) GetPrincipalByID(
	_ context.Context, id string,
) (*identity.Principal, error) {
	if r.getPrincipalErr != nil {
		return nil, r.getPrincipalErr
	}
	for _, principal := range r.principals {
		if principal.ID == id {
			return principal, nil
		}
	}
	return nil, identity.ErrNotFound
}

func (r *fakeTenancyRepository) DeletePrincipal(_ context.Context, id string) error {
	if r.deletePrincipalErr != nil {
		return r.deletePrincipalErr
	}
	for i, principal := range r.principals {
		if principal.ID == id {
			r.principals = append(r.principals[:i:i], r.principals[i+1:]...)
			return nil
		}
	}
	return identity.ErrNotFound
}

func (r *fakeTenancyRepository) IssuePrincipalSecret(
	ctx context.Context, principalID, secretID string, expiresAt *time.Time, at time.Time,
) (string, identity.Secret, error) {
	if r.issueSecretErr != nil {
		return "", identity.Secret{}, r.issueSecretErr
	}
	principal, err := r.GetPrincipalByID(ctx, principalID)
	if err != nil {
		return "", identity.Secret{}, err
	}
	plaintext, err := principal.IssueSecret(secretID, expiresAt, at)
	if err != nil {
		return "", identity.Secret{}, err
	}
	secrets := principal.Secrets()
	return plaintext, secrets[len(secrets)-1], nil
}

func (r *fakeTenancyRepository) RevokePrincipalSecret(
	ctx context.Context, principalID, secretID string, at time.Time,
) error {
	if r.revokeSecretErr != nil {
		return r.revokeSecretErr
	}
	principal, err := r.GetPrincipalByID(ctx, principalID)
	if err != nil {
		return err
	}
	return principal.RevokeSecret(secretID, at)
}
