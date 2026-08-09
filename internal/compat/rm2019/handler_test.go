package rm2019

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

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

const (
	orgID     = "3f2b1c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	projectID = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	olderID   = "1a1b1c1d-2e2f-4a3b-8c4d-5e6f7a8b9c0d"
	token     = "test-token"
)

var epoch = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

type fakeRepository struct {
	organization    *store.Organization
	projects        []store.Project
	organizationErr error
}

// Mirrors the real repository's scoping, so a handler that stops passing the
// caller is observable here.
func (f fakeRepository) ListOrganizationsForPrincipal(
	_ context.Context, principal *identity.Principal,
) ([]store.Organization, error) {
	if f.organizationErr != nil {
		return nil, f.organizationErr
	}
	if f.organization == nil {
		return []store.Organization{}, nil
	}
	if principal.Scope.PlatformScoped() {
		return []store.Organization{*f.organization}, nil
	}
	if f.organization.ID != principal.Scope.OrganizationID.String() {
		return []store.Organization{}, nil
	}
	return []store.Organization{*f.organization}, nil
}

func (f fakeRepository) ListProjects(_ context.Context, organizationID string) ([]store.Project, error) {
	var projects []store.Project
	for _, project := range f.projects {
		if project.OrganizationID == organizationID {
			projects = append(projects, project)
		}
	}
	return projects, nil
}

func (f fakeRepository) GetProject(
	_ context.Context, organizationID, requestedProjectID string,
) (*store.Project, error) {
	for _, project := range f.projects {
		if project.OrganizationID == organizationID && project.ID == requestedProjectID {
			copy := project
			return &copy, nil
		}
	}
	return nil, registry.ErrNotFound
}

// fakePrincipals resolves authority for an already-authenticated caller.
type fakePrincipals struct {
	role    identity.Role
	scope   identity.Scope
	missing bool
}

func (f fakePrincipals) GetPrincipalByID(_ context.Context, id string) (*identity.Principal, error) {
	if f.missing {
		return nil, identity.ErrNotFound
	}
	role := f.role
	if role == "" {
		role = identity.RoleReader
	}
	return identity.RestorePrincipal(id, "test", "client", f.scope, role, epoch, testSecrets())
}

type scopedAuthenticator struct{ scope identity.Scope }

func (a scopedAuthenticator) Verify(presented string) (identity.Verified, error) {
	if presented != token {
		return identity.Verified{}, identity.ErrInvalid
	}
	return identity.Verified{PrincipalID: "p-1", Scope: a.scope, SecretID: testSecretID}, nil
}

func newServer(scope identity.Scope) http.Handler {
	return newServerWithLogger(scope, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func newServerWithLogger(scope identity.Scope, logger *slog.Logger) http.Handler {
	repository := fakeRepository{
		organization: &store.Organization{ID: orgID, Name: "orbital", CreatedAt: epoch},
		projects: []store.Project{
			{ID: projectID, OrganizationID: orgID, Name: "lab-registry", CreatedAt: epoch.Add(time.Hour)},
			{ID: olderID, OrganizationID: orgID, Name: "older", CreatedAt: epoch},
		},
	}
	return NewHandler(repository, fakePrincipals{scope: scope}, scopedAuthenticator{scope: scope}, logger)
}

func get(t *testing.T, handler http.Handler, path, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func orgScope() identity.Scope {
	return identity.Scope{OrganizationID: uuid.MustParse(orgID)}
}

func projectScope() identity.Scope {
	return identity.Scope{OrganizationID: uuid.MustParse(orgID), ProjectID: uuid.MustParse(projectID)}
}

// loadOrganizationID fails with "unexpected number of organizations" unless the
// list holds exactly one, so the count is a compatibility contract.
func TestListOrganizationsReturnsExactlyOne(t *testing.T) {
	w := get(t, newServer(orgScope()), basePath+"/organizations", token)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body)
	}
	var body struct {
		Organizations []struct {
			ID        string `json:"id"`
			CreatedAt string `json:"created_at"`
		} `json:"organizations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Organizations) != 1 {
		t.Fatalf("%d organizations, want exactly 1 — the CLI errors otherwise", len(body.Organizations))
	}
	if body.Organizations[0].ID != orgID {
		t.Fatalf("organization = %s, want the caller's own", body.Organizations[0].ID)
	}
}

// A project-scoped principal must be REFUSED, not helpfully given its one
// project: the CLI turns that 403 into "try setting HCP_PROJECT_ID", and
// answering would make a documented resolution path unreachable (ADR-0016).
func TestProjectScopedPrincipalIsRefusedProjectListing(t *testing.T) {
	w := get(t, newServer(projectScope()), basePath+"/projects", token)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body %s", w.Code, w.Body)
	}
	if body := w.Body.String(); !json.Valid([]byte(body)) {
		t.Fatalf("refusal is not valid JSON: %s", body)
	}
}

func TestAuthenticationAndProjectListingRefusalsUseAuditSchema(t *testing.T) {
	handler := newServer(projectScope())
	trail := &rmAuditTrail{}
	handler = audit.NewHTTPHandler(trail, handler.(audit.Resolver), handler, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))

	const rejectedToken = "known-rejected-bearer-token"
	get(t, handler, basePath+"/projects", rejectedToken)
	get(t, handler, basePath+"/projects", token)

	output := string(trail.raw)
	if strings.Contains(output, rejectedToken) {
		t.Fatalf("audit log contains rejected bearer token %q: %s", rejectedToken, output)
	}
	responses := trail.responses()
	if len(responses) != 2 {
		t.Fatalf("response records = %d, want 2", len(responses))
	}
	assertRMFields(t, responses[0], map[string]any{
		"operation": "project.list", "target_type": "project_collection",
		"principal_id": "unknown", "identity_kind": "unknown", "scope": "platform",
		"outcome": "refused", "reason": "invalid_token",
	}, "organization_id", "project_id", "target_id")
	assertRMFields(t, responses[1], map[string]any{
		"operation": "project.list", "target_type": "project_collection",
		"principal_id": "p-1", "identity_kind": "service_principal", "scope": "project",
		"organization_id": orgID, "project_id": projectID,
		"outcome": "refused", "reason": "project_scoped_principal",
	}, "target_id")
}

func TestOrganizationScopedPrincipalListsItsProjects(t *testing.T) {
	w := get(t, newServer(orgScope()), basePath+"/projects", token)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body)
	}
	var body struct {
		Projects []struct {
			ID        string `json:"id"`
			CreatedAt string `json:"created_at"`
			Parent    *struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"parent"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Projects) != 2 {
		t.Fatalf("%d projects, want 2", len(body.Projects))
	}
	for _, project := range body.Projects {
		// created_at decides which project an unpinned CLI selects, so an absent
		// timestamp silently changes tenant selection (ADR-0003).
		if project.CreatedAt == "" {
			t.Fatalf("project %s has no created_at", project.ID)
		}
		if project.Parent == nil || project.Parent.ID != orgID {
			t.Fatalf("project %s has no organization parent", project.ID)
		}
	}
}

// A scope filter naming someone else's organization returns nothing, rather
// than confirming that organization exists.
func TestScopeFilterForAnotherOrganizationReturnsNothing(t *testing.T) {
	w := get(t, newServer(orgScope()), basePath+"/projects?scope.id="+olderID, token)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body)
	}
	var body struct {
		Projects []json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Projects) != 0 {
		t.Fatalf("%d projects returned for another organization, want 0", len(body.Projects))
	}
}

func TestAllEndpointsRequireAToken(t *testing.T) {
	handler := newServer(orgScope())
	for _, path := range []string{
		basePath + "/organizations",
		basePath + "/projects",
		basePath + "/projects/" + projectID,
	} {
		for _, bearer := range []string{"", "not-the-token"} {
			w := get(t, handler, path, bearer)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s with %q: status = %d, want 401", path, bearer, w.Code)
			}
			if w.Header().Get("WWW-Authenticate") == "" {
				t.Fatalf("%s: 401 carries no challenge", path)
			}
		}
	}
}

func TestProjectGetReturnsTheRequestedProject(t *testing.T) {
	for _, scope := range []identity.Scope{orgScope(), projectScope()} {
		handler := newServer(scope)
		w := get(t, handler, basePath+"/projects/"+projectID, token)
		if w.Code != http.StatusOK {
			t.Fatalf("scope %#v: status = %d, want 200; body %s", scope, w.Code, w.Body)
		}
		var body struct {
			Project *struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				CreatedAt string `json:"created_at"`
				Parent    *struct {
					ID   string `json:"id"`
					Type string `json:"type"`
				} `json:"parent"`
			} `json:"project"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Project == nil || body.Project.ID != projectID || body.Project.Name != "lab-registry" ||
			body.Project.CreatedAt == "" || body.Project.Parent == nil ||
			body.Project.Parent.ID != orgID || body.Project.Parent.Type != "ORGANIZATION" {
			t.Fatalf("project response = %#v", body.Project)
		}
	}
}

// Mutation oracle for duf-lbr: if getProject skips the binding check, this
// project-scoped caller reads its sibling and the named authorization gate
// fails. The refusal is concealed as ordinary per-resource not-found.
func TestProjectGetRefusesProjectOutsideCallerBinding(t *testing.T) {
	w := get(t, newServer(projectScope()), basePath+"/projects/"+olderID, token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want concealed 404; body %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if strings.Count(body, `"code"`) != 1 || !strings.Contains(body, `"code":5`) {
		t.Fatalf("refusal = %s, want exactly one code 5", body)
	}
}

func TestProjectGetUsesStoredPrincipalScope(t *testing.T) {
	// The token claims organization scope, but storage has narrowed the caller to
	// one project. The sibling project must remain concealed.
	handler := NewHandler(
		fakeRepository{projects: []store.Project{
			{ID: projectID, OrganizationID: orgID, Name: "mine", CreatedAt: epoch},
			{ID: olderID, OrganizationID: orgID, Name: "sibling", CreatedAt: epoch},
		}},
		fakePrincipals{scope: projectScope()},
		scopedAuthenticator{scope: orgScope()},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	w := get(t, handler, basePath+"/projects/"+olderID, token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want stored-scope 404; body %s", w.Code, w.Body)
	}
}

func TestProjectGetMissingUsesPerResourceNotFoundCode(t *testing.T) {
	w := get(t, newServer(orgScope()), basePath+"/projects/"+uuid.NewString(), token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if strings.Count(body, `"code"`) != 1 || !strings.Contains(body, `"code":5`) {
		t.Fatalf("missing project = %s, want exactly one code 5", body)
	}
}

func TestEveryResourceManagerRouteRequiresReader(t *testing.T) {
	type expectedRoute struct {
		role                           identity.Role
		operation, target, targetParam string
	}
	expected := map[string]expectedRoute{
		"GET /organizations": {identity.RoleReader, "organization.list", "organization_collection", ""},
		"GET /projects":      {identity.RoleReader, "project.list", "project_collection", ""},
		"GET /projects/{id}": {identity.RoleReader, "project.read", "project", "id"},
	}
	seen := make(map[string]bool, len(expected))
	for _, route := range routes() {
		key := route.method + " " + route.path
		seen[key] = true
		want, ok := expected[key]
		if !ok {
			t.Errorf("registered resource-manager route %s has no expected descriptor", key)
			continue
		}
		if route.required != want.role || route.operation != identity.AuditOperation(want.operation) ||
			route.targetType != want.target || route.targetIDParam != want.targetParam {
			t.Errorf("route %s = role %s, operation %s, target %s/%s; want %#v",
				key, route.required, route.operation, route.targetType, route.targetIDParam, want)
		}
	}
	for key := range expected {
		if !seen[key] {
			t.Errorf("expected route %s is not registered", key)
		}
	}
}

func TestProjectGetTenancyRefusalUsesAuditSchema(t *testing.T) {
	handler := newServer(projectScope())
	trail := &rmAuditTrail{}
	handler = audit.NewHTTPHandler(trail, handler.(audit.Resolver), handler, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))
	get(t, handler, basePath+"/projects/"+olderID, token)
	assertRMFields(t, trail.responses()[0], map[string]any{
		"operation": "project.read", "target_type": "project", "target_id": olderID,
		"principal_id": "p-1", "identity_kind": "service_principal", "scope": "project",
		"organization_id": orgID, "project_id": olderID,
		"outcome": "refused", "reason": "outside_principal_scope",
	})
}

type rmAuditTrail struct {
	raw     []byte
	records []map[string]any
}

func (w *rmAuditTrail) Write(encoded []byte) error {
	w.raw = append(w.raw, encoded...)
	var record map[string]any
	if err := json.Unmarshal(encoded, &record); err != nil {
		return err
	}
	w.records = append(w.records, record)
	return nil
}

func (w *rmAuditTrail) responses() []map[string]any {
	var responses []map[string]any
	for _, record := range w.records {
		if record["kind"] == "response" {
			responses = append(responses, record)
		}
	}
	return responses
}

func assertRMFields(t *testing.T, record map[string]any, want map[string]any, absent ...string) {
	t.Helper()
	for field, value := range want {
		if record[field] != value {
			t.Errorf("%s = %v, want %v; record %#v", field, record[field], value, record)
		}
	}
	for _, field := range absent {
		if _, ok := record[field]; ok {
			t.Errorf("%s present, want absent; record %#v", field, record)
		}
	}
}

// A principal whose organization has been deleted sees nothing rather than an
// error: its scope resolves to nothing, which is not a failure.
func TestPrincipalWithoutASurvivingOrganizationSeesNothing(t *testing.T) {
	handler := NewHandler(
		fakeRepository{},
		fakePrincipals{scope: orgScope()},
		scopedAuthenticator{scope: orgScope()},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	w := get(t, handler, basePath+"/organizations", token)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body)
	}
	var body struct {
		Organizations []json.RawMessage `json:"organizations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Organizations) != 0 {
		t.Fatalf("%d organizations, want 0", len(body.Organizations))
	}
}

// Authenticated is not authorized. A caller whose principal no longer resolves
// is refused, and so is one below reader — enumerating tenancies is an action
// like any other (ADR-0019).
func TestTenancyEnumerationRequiresAuthorization(t *testing.T) {
	for _, c := range []struct {
		name       string
		principals fakePrincipals
		want       int
	}{
		{"reader may enumerate", fakePrincipals{role: identity.RoleReader, scope: orgScope()}, http.StatusOK},
		{"unresolvable principal is refused", fakePrincipals{missing: true}, http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			handler := NewHandler(
				fakeRepository{organization: &store.Organization{ID: orgID, Name: "orbital", CreatedAt: epoch}},
				c.principals,
				scopedAuthenticator{scope: orgScope()},
				slog.New(slog.NewTextHandler(io.Discard, nil)),
			)
			if w := get(t, handler, basePath+"/organizations", token); w.Code != c.want {
				t.Fatalf("status = %d, want %d; body %s", w.Code, c.want, w.Body)
			}
		})
	}
}

func TestUnresolvableResourceManagerPrincipalAuditHasNoInventedTenancy(t *testing.T) {
	handler := NewHandler(
		fakeRepository{}, fakePrincipals{missing: true}, scopedAuthenticator{scope: orgScope()},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	trail := &rmAuditTrail{}
	handler = audit.NewHTTPHandler(trail, handler.(audit.Resolver), handler, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))
	response := get(t, handler, basePath+"/organizations", token)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.Code)
	}
	assertRMFields(t, trail.responses()[0], map[string]any{
		"operation": "organization.list", "target_type": "organization_collection",
		"principal_id": "p-1", "identity_kind": "service_principal", "scope": "platform",
		"outcome": "refused", "reason": "principal_unresolvable",
	}, "organization_id", "project_id", "target_id")
}

// Review finding 13: this plane resolved the principal, then answered from the
// token's claims. Scope is immutable today so nothing was exploitable, but the
// day a scope-changing operation lands, a stale token would keep its old
// tenancy view for a full TTL — precisely what per-request resolution exists to
// prevent, and invisible because the interface comment claimed otherwise.
//
// The token here deliberately carries a DIFFERENT scope from the stored
// principal. Storage must win.
func TestAnswersFromStorageNotFromTheToken(t *testing.T) {
	stored := identity.Scope{OrganizationID: uuid.MustParse(orgID)}
	stale := identity.Scope{OrganizationID: uuid.MustParse(olderID)}
	repository := fakeRepository{
		organization: &store.Organization{ID: orgID, Name: "orbital", CreatedAt: epoch},
		projects: []store.Project{
			{ID: projectID, OrganizationID: orgID, Name: "lab-registry", CreatedAt: epoch},
		},
	}

	t.Run("organization listing uses stored organization", func(t *testing.T) {
		handler := NewHandler(
			repository,
			fakePrincipals{role: identity.RoleReader, scope: stored},
			// The token asserts a scope the principal no longer has.
			scopedAuthenticator{scope: stale},
			slog.New(slog.NewTextHandler(io.Discard, nil)),
		)

		w := get(t, handler, basePath+"/organizations", token)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body)
		}
		var body struct {
			Organizations []struct {
				ID string `json:"id"`
			} `json:"organizations"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.Organizations) != 1 {
			t.Fatalf("%d organizations, want 1", len(body.Organizations))
		}
		if body.Organizations[0].ID != orgID {
			t.Fatalf("answered with %s, the TOKEN's organization — storage must win",
				body.Organizations[0].ID)
		}
	})

	t.Run("project listing uses stored organization", func(t *testing.T) {
		handler := NewHandler(
			repository,
			fakePrincipals{role: identity.RoleReader, scope: stored},
			scopedAuthenticator{scope: stale},
			slog.New(slog.NewTextHandler(io.Discard, nil)),
		)

		w := get(t, handler, basePath+"/projects", token)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body)
		}
		var body struct {
			Projects []struct {
				ID string `json:"id"`
			} `json:"projects"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.Projects) != 1 {
			t.Fatalf("%d projects, want 1 from the stored organization", len(body.Projects))
		}
		if body.Projects[0].ID != projectID {
			t.Fatalf("answered with project %s, want %s from storage", body.Projects[0].ID, projectID)
		}
	})

	t.Run("project listing refuses stored project scope", func(t *testing.T) {
		handler := NewHandler(
			repository,
			fakePrincipals{role: identity.RoleReader, scope: projectScope()},
			// The stale token still claims the old organization-wide authority.
			scopedAuthenticator{scope: stored},
			slog.New(slog.NewTextHandler(io.Discard, nil)),
		)

		w := get(t, handler, basePath+"/projects", token)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 from the stored project scope; body %s", w.Code, w.Body)
		}
	})
}

func TestInternalFailuresKeepTheirDetailServerSide(t *testing.T) {
	const leak = `ERROR: insert or update on table "organizations" violates ` +
		`foreign key constraint "organizations_owner_id_fkey" (SQLSTATE 23503)`

	var logged bytes.Buffer
	handler := NewHandler(
		fakeRepository{organizationErr: errors.New(leak)},
		fakePrincipals{scope: orgScope()},
		scopedAuthenticator{scope: orgScope()},
		slog.New(slog.NewTextHandler(&logged, nil)),
	)
	audited := &responseCorrelationWriter{}
	handler = audit.NewHTTPHandler(audited, correlationResolver{}, handler, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))

	w := get(t, handler, basePath+"/organizations", token)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", w.Code, w.Body)
	}
	for _, secret := range []string{"organizations", "constraint", "SQLSTATE", "fkey"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("the response leaked %q: %s", secret, w.Body)
		}
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	const prefix = "internal error; correlation id "
	correlation := strings.TrimPrefix(body.Message, prefix)
	if correlation == body.Message {
		t.Fatalf("no correlation id to trace the failure by: %s", w.Body)
	}
	if _, err := uuid.Parse(correlation); err != nil {
		t.Fatalf("correlation id %q is not a UUID: %v", correlation, err)
	}
	if !strings.Contains(logged.String(), "SQLSTATE") {
		t.Fatalf("the detail did not reach the log: %s", logged.String())
	}
	if !strings.Contains(logged.String(), correlation) {
		t.Fatalf("the response correlation id did not reach the log: %s", logged.String())
	}
	if correlation != audited.correlation {
		t.Fatalf("response correlation = %q, audited correlation = %q", correlation, audited.correlation)
	}
}

type correlationResolver struct{}

func (correlationResolver) Resolve(*http.Request) audit.Descriptor {
	return audit.Descriptor{RouteID: "test.internal_failure"}
}

type responseCorrelationWriter struct {
	correlation string
}

func (w *responseCorrelationWriter) Write(encoded []byte) error {
	var record struct {
		Kind          string `json:"kind"`
		CorrelationID string `json:"correlation_id"`
	}
	if err := json.Unmarshal(encoded, &record); err != nil {
		return err
	}
	if record.Kind == "response" {
		w.correlation = record.CorrelationID
	}
	return nil
}

// testSecretID is the credential every test token is minted from. Fixtures must
// carry a secret with this ID or the request is refused as revoked, which is the
// point of review finding 14 — a token names the credential behind it.
const testSecretID = "s-test"

// testSecrets is the stored credential a fixture principal holds. Only the
// argon2id prefix is validated on restore, so this needs no derivation.
func testSecrets() []identity.Secret {
	secret, err := identity.RestoreSecret(
		testSecretID,
		"$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHQ$aGFzaGhhc2g",
		epoch, nil, nil,
	)
	if err != nil {
		panic("test secret: " + err.Error())
	}
	return []identity.Secret{secret}
}
