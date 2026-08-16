package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

func platformServer(roles testRoles) http.Handler {
	return platformServerWithRepository(roles, &fakeTenancyRepository{})
}

func TestObjectStorageConfigurationIsNotAPlatformOperation(t *testing.T) {
	handler := platformServer(testRoles{role: identity.RoleRoot})
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		response := call(t, handler, method, "/api/v1/object-storage", map[string]string{}, testToken)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s removed object-storage operation = %d, want 404: %s",
				method, response.Code, response.Body)
		}
	}
}

func platformServerWithRepository(
	roles testRoles, repository *fakeTenancyRepository,
) http.Handler {
	return newHandler(
		repository, &fakeInstanceRepository{},
		testAuth{}, roles, testLogger(), func() time.Time { return initTestTime },
	)
}

func TestExpiredBearerStillReturnsUnauthorizedOnAnAPIRequest(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	handler, _, _ := realSessionHandler(t, now, 300*time.Second)
	expired := signedSessionToken(t, now, now.Add(-time.Minute), now.Add(-time.Hour), testSecretID)
	response := call(t, handler, http.MethodGet, "/api/v1/instance", nil, expired)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expired bearer status = %d, want 401: %s", response.Code, response.Body)
	}
}

func call(t *testing.T, handler http.Handler, method, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		encoded, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

type platformAuditTrail struct {
	raw     []byte
	records []map[string]any
}

func (w *platformAuditTrail) Write(encoded []byte) error {
	w.raw = append(w.raw, encoded...)
	var record map[string]any
	if err := json.Unmarshal(encoded, &record); err != nil {
		return err
	}
	w.records = append(w.records, record)
	return nil
}

func (w *platformAuditTrail) response(t *testing.T) map[string]any {
	t.Helper()
	for i := len(w.records) - 1; i >= 0; i-- {
		if w.records[i]["kind"] == "response" {
			return w.records[i]
		}
	}
	t.Fatal("no response audit record")
	return nil
}

func auditedPlatform(t *testing.T, handler http.Handler) (http.Handler, *platformAuditTrail) {
	t.Helper()
	resolver, ok := handler.(audit.Resolver)
	if !ok {
		t.Fatalf("platform handler %T does not expose its descriptor resolver", handler)
	}
	trail := &platformAuditTrail{}
	return audit.NewHTTPHandler(trail, resolver, handler, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key"))), trail
}

// Initialization and recovery are the unauthenticated routes on this plane
// (beside health and the self-authenticating session): requiring a credential
// to obtain the first credential is a loop (ADR-0012), and recovery exists for
// the caller whose credential is lost (ADR-0024).
func TestOnlyBootstrapAndRecoveryAreUnauthenticated(t *testing.T) {
	server := platformServer(testRoles{})

	if w := call(t, server, http.MethodPost, "/sys/init", nil, ""); w.Code != http.StatusOK {
		t.Fatalf("unauthenticated /init = %d, want 200; body %s", w.Code, w.Body)
	}
	// 409 rather than 401: the request reached the handler, which refused on
	// the missing verifier, not on a missing bearer token.
	if w := call(t, server, http.MethodPost, "/sys/recovery",
		map[string]any{"shares": []string{"x"}}, ""); w.Code == http.StatusUnauthorized {
		t.Fatalf("unauthenticated /sys/recovery = 401, want the handler's own refusal; body %s", w.Body)
	}
	for _, path := range []string{
		"/api/v1/instance",
		"/api/v1/self",
		"/api/v1/organizations",
		"/api/v1/organizations/" + testOrgID,
		"/api/v1/organizations/" + testOrgID + "/projects",
	} {
		if w := call(t, server, http.MethodGet, path, nil, ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s = %d, want 401; body %s", path, w.Code, w.Body)
		}
	}
}

// Creating and deleting organisations is instance-scoped, so it needs root. A
// maintainer holds authority within a tenancy, not over the set of them.
func TestOrganizationLifecycleRequiresRoot(t *testing.T) {
	maintainer := testRoles{
		role:  identity.RoleMaintainer,
		scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
	}
	server := platformServer(maintainer)

	w := call(t, server, http.MethodPost, "/api/v1/organizations",
		map[string]any{"name": "another"}, testToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("maintainer creating an organization = %d, want 403; body %s", w.Code, w.Body)
	}

	w = call(t, server, http.MethodDelete, "/api/v1/organizations/"+testOrgID, nil, testToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("maintainer deleting an organization = %d, want 403; body %s", w.Code, w.Body)
	}

	// Root may.
	root := platformServer(testRoles{role: identity.RoleRoot})
	if w := call(t, root, http.MethodPost, "/api/v1/organizations",
		map[string]any{"name": "another"}, testToken); w.Code == http.StatusForbidden {
		t.Fatalf("root was refused organization creation: %s", w.Body)
	}
}

// A tenancy the caller may not see answers not-found; a role it lacks within a
// tenancy it CAN see answers forbidden (ADR-0017).
func TestPlatformDistinguishesTenancyFromRoleRefusals(t *testing.T) {
	reader := testRoles{
		role:  identity.RoleReader,
		scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
	}
	server := platformServer(reader)

	// Its own organization, but creating a project needs maintainer.
	w := call(t, server, http.MethodPost, "/api/v1/organizations/"+testOrgID+"/projects",
		map[string]any{"name": "new"}, testToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("insufficient role = %d, want 403; body %s", w.Code, w.Body)
	}

	// Someone else's organization — must not confirm it exists.
	other := uuid.New().String()
	w = call(t, server, http.MethodPost, "/api/v1/organizations/"+other+"/projects",
		map[string]any{"name": "new"}, testToken)
	if w.Code != http.StatusNotFound {
		t.Fatalf("another organization = %d, want 404; body %s", w.Code, w.Body)
	}
}

// A token naming a principal that no longer resolves is refused, so deletion
// takes effect immediately rather than at token expiry.
func TestPlatformRefusesAnUnresolvablePrincipal(t *testing.T) {
	server := platformServer(testRoles{missing: true})

	if w := call(t, server, http.MethodGet, "/api/v1/organizations", nil, testToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body %s", w.Code, w.Body)
	}
}

func TestPlatformAuditsARevokedCredential(t *testing.T) {
	server, trail := auditedPlatform(t, platformServer(testRoles{revoked: true}))
	response := call(t, server, http.MethodGet, "/api/v1/organizations", nil, testToken)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body %s", response.Code, response.Body)
	}
	if reason := trail.response(t)["reason"]; reason != "credential_revoked" {
		t.Fatalf("audit reason = %v, want credential_revoked", reason)
	}
}

// Finding 1 of the 2026-07-31 review: any authenticated principal could
// enumerate every organisation on the instance. Organisation identifiers are
// half of every compatibility-plane tenant path, so listing them wholesale
// hands out what the rest of the system works to keep secret (ADR-0016).
func TestListOrganizationsShowsOnlyWhatTheCallerMaySee(t *testing.T) {
	mine := store.Organization{ID: testOrgID, Name: "mine", CreatedAt: initTestTime}
	theirs := store.Organization{ID: uuid.NewString(), Name: "theirs", CreatedAt: initTestTime}

	names := func(t *testing.T, w *httptest.ResponseRecorder) []string {
		t.Helper()
		var body struct {
			Organizations []struct{ Name string } `json:"organizations"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v; body %s", err, w.Body)
		}
		got := make([]string, 0, len(body.Organizations))
		for _, organization := range body.Organizations {
			got = append(got, organization.Name)
		}
		return got
	}

	for _, c := range []struct {
		name  string
		roles testRoles
		want  []string
	}{
		{
			name:  "a tenancy-scoped reader sees only its own",
			roles: testRoles{role: identity.RoleReader, scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)}},
			want:  []string{"mine"},
		},
		{
			name:  "a maintainer sees only its own",
			roles: testRoles{role: identity.RoleMaintainer, scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)}},
			want:  []string{"mine"},
		},
		{
			// Platform scope is what "sees everything" means, and only root holds it.
			name:  "platform-scoped root sees all",
			roles: testRoles{role: identity.RoleRoot},
			want:  []string{"mine", "theirs"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			repository := &fakeTenancyRepository{organizations: []store.Organization{mine, theirs}}
			handler := newHandler(repository, &fakeInstanceRepository{}, testAuth{}, c.roles,
				testLogger(), func() time.Time { return initTestTime })

			w := call(t, handler, http.MethodGet, "/api/v1/organizations", nil, testToken)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body)
			}
			got := names(t, w)
			if len(got) != len(c.want) {
				t.Fatalf("saw %v, want %v", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("saw %v, want %v", got, c.want)
				}
			}
		})
	}
}

func TestProjectScopedReaderSeesOrganizationAndOnlyItsProject(t *testing.T) {
	siblingProject := uuid.NewString()
	repository := &fakeTenancyRepository{
		organizations: []store.Organization{{
			ID: testOrgID, Name: "orbital-lab", CreatedAt: initTestTime,
		}},
		projects: []store.Project{
			{ID: testProjID, OrganizationID: testOrgID, Name: "images", CreatedAt: initTestTime},
			{ID: siblingProject, OrganizationID: testOrgID, Name: "secret-sibling", CreatedAt: initTestTime},
		},
	}
	handler := platformServerWithRepository(testRoles{
		role: identity.RoleBuilder,
		scope: identity.Scope{
			OrganizationID: uuid.MustParse(testOrgID),
			ProjectID:      uuid.MustParse(testProjID),
		},
	}, repository)

	organization := call(t, handler, http.MethodGet, "/api/v1/organizations/"+testOrgID, nil, testToken)
	if organization.Code != http.StatusOK || !strings.Contains(organization.Body.String(), "orbital-lab") {
		t.Fatalf("own organization = %d, body %s; want named 200", organization.Code, organization.Body)
	}

	listed := call(t, handler, http.MethodGet, "/api/v1/organizations/"+testOrgID+"/projects", nil, testToken)
	if listed.Code != http.StatusOK {
		t.Fatalf("own project listing = %d, body %s; want 200", listed.Code, listed.Body)
	}
	var body ListProjects200JSONResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode project listing: %v", err)
	}
	if len(body.Projects) != 1 || body.Projects[0].Id.String() != testProjID || body.Projects[0].Name != "images" {
		t.Fatalf("project listing = %#v, want only the caller's images project", body.Projects)
	}
}

func TestOrganizationVisibilityChecksTenancyBeforeRole(t *testing.T) {
	if _, refused := authorizeOrganizationVisibility(context.Background(), identity.RoleReader, uuid.MustParse(testOrgID)); refused != refusedTenancy {
		t.Fatalf("missing principal = %v, want refusedTenancy", refused)
	}

	principal := &identity.Principal{
		ID: "corrupt", Role: identity.Role(""),
		Scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
	}
	ctx := context.WithValue(context.Background(), principalKey{}, principal)

	if _, refused := authorizeOrganizationVisibility(ctx, identity.RoleReader, uuid.MustParse(testOrgID)); refused != refusedRole {
		t.Fatalf("own organization with insufficient role = %v, want refusedRole", refused)
	}
	if _, refused := authorizeOrganizationVisibility(ctx, identity.RoleReader, uuid.New()); refused != refusedTenancy {
		t.Fatalf("foreign organization with insufficient role = %v, want refusedTenancy", refused)
	}

	root := &identity.Principal{ID: "root", Role: identity.RoleRoot, Scope: identity.Scope{}}
	rootContext := context.WithValue(context.Background(), principalKey{}, root)
	if _, refused := authorizeOrganizationVisibility(rootContext, identity.RoleReader, uuid.Nil); refused != refusedTenancy {
		t.Fatalf("zero organization = %v, want refusedTenancy", refused)
	}
}

func TestProjectScopedReadsRefuseAnotherOrganizationAndAuditTheRefusal(t *testing.T) {
	foreignOrganization := uuid.NewString()
	foreignProject := uuid.NewString()
	repository := &fakeTenancyRepository{
		organizations: []store.Organization{{
			ID: foreignOrganization, Name: "foreign", CreatedAt: initTestTime,
		}},
		projects: []store.Project{{
			ID: foreignProject, OrganizationID: foreignOrganization, Name: "foreign-project", CreatedAt: initTestTime,
		}},
	}
	roles := testRoles{
		role: identity.RoleBuilder,
		scope: identity.Scope{
			OrganizationID: uuid.MustParse(testOrgID),
			ProjectID:      uuid.MustParse(testProjID),
		},
	}

	for _, test := range []struct {
		name, path, operation, scope string
		projectScope                 bool
	}{
		{"GetOrganization", "/api/v1/organizations/" + foreignOrganization, "organization.read", "organization", false},
		{"ListProjects", "/api/v1/organizations/" + foreignOrganization + "/projects", "project.list", "organization", false},
		{"GetProject", "/api/v1/organizations/" + foreignOrganization + "/projects/" + foreignProject, "project.read", "project", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, trail := auditedPlatform(t, platformServerWithRepository(roles, repository))
			response := call(t, handler, http.MethodGet, test.path, nil, testToken)
			if response.Code != http.StatusNotFound {
				t.Fatalf("foreign read = %d, body %s; want 404", response.Code, response.Body)
			}
			record := trail.response(t)
			want := map[string]any{
				"operation": test.operation, "scope": test.scope,
				"organization_id": foreignOrganization,
				"outcome":         "refused", "reason": "tenancy_refused",
			}
			absent := []string{"project_id"}
			if test.projectScope {
				want["project_id"] = foreignProject
				absent = nil
			}
			assertPlatformAudit(t, record, want, absent...)
		})
	}
}

func TestProjectScopedReadSuccessesCarryTheRightAuditScope(t *testing.T) {
	repository := &fakeTenancyRepository{
		organizations: []store.Organization{{ID: testOrgID, Name: "orbital-lab", CreatedAt: initTestTime}},
		projects:      []store.Project{{ID: testProjID, OrganizationID: testOrgID, Name: "images", CreatedAt: initTestTime}},
	}
	roles := testRoles{
		role: identity.RoleBuilder,
		scope: identity.Scope{
			OrganizationID: uuid.MustParse(testOrgID),
			ProjectID:      uuid.MustParse(testProjID),
		},
	}

	for _, test := range []struct {
		name, path, operation, scope string
		projectScope                 bool
	}{
		{"GetOrganization", "/api/v1/organizations/" + testOrgID, "organization.read", "organization", false},
		{"ListProjects", "/api/v1/organizations/" + testOrgID + "/projects", "project.list", "organization", false},
		{"GetProject", "/api/v1/organizations/" + testOrgID + "/projects/" + testProjID, "project.read", "project", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, trail := auditedPlatform(t, platformServerWithRepository(roles, repository))
			response := call(t, handler, http.MethodGet, test.path, nil, testToken)
			if response.Code != http.StatusOK {
				t.Fatalf("own read = %d, body %s; want 200", response.Code, response.Body)
			}
			record := trail.response(t)
			want := map[string]any{
				"operation": test.operation, "scope": test.scope,
				"organization_id": testOrgID, "outcome": "success",
			}
			absent := []string{"project_id", "reason"}
			if test.projectScope {
				want["project_id"] = testProjID
				absent = []string{"reason"}
			}
			assertPlatformAudit(t, record, want, absent...)
		})
	}
}

// Findings 9 and 10: an operation that names a tenancy must never be able to
// skip the tenancy check. authorizeTenancy refuses an empty organisation rather
// than treating it as "no tenancy to check".
func TestAuthorizeTenancyRefusesAnEmptyOrganization(t *testing.T) {
	root, err := identity.RestorePrincipal("p", "n", "c", identity.Scope{}, identity.RoleRoot, initTestTime, nil)
	if err != nil {
		t.Fatalf("RestorePrincipal: %v", err)
	}
	ctx := context.WithValue(context.Background(), principalKey{}, root)

	// Even root, which outranks every tenancy question, is refused — because the
	// caller has failed to say what it is acting on.
	if _, refused := authorizeTenancy(ctx, identity.RoleReader, "", ""); refused != refusedTenancy {
		t.Fatalf("empty organization = %v, want refusedTenancy", refused)
	}
	if _, refused := authorizeTenancy(ctx, identity.RoleReader, "", testProjID); refused != refusedTenancy {
		t.Fatalf("project without organization = %v, want refusedTenancy", refused)
	}
	// A platform operation is a different question and is answered separately.
	if _, refused := authorizePlatform(ctx, identity.RoleRoot); refused != permitted {
		t.Fatalf("root refused a platform operation: %v", refused)
	}
}

func TestMaintainerCannotCreateRootPrincipal(t *testing.T) {
	maintainer := testRoles{
		role:  identity.RoleMaintainer,
		scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
	}
	server := platformServer(maintainer)
	w := call(t, server, http.MethodPost, "/api/v1/principals",
		map[string]any{"name": "replacement root", "role": "root"}, testToken)
	// 404, not 403: the request names no organisation, so it asks to create at
	// PLATFORM scope, where a tenancy-bound caller has no standing at all. It
	// learns nothing about what does or does not exist there (duf-pln).
	if w.Code != http.StatusNotFound {
		t.Fatalf("maintainer creating root = %d, want 404; body %s", w.Code, w.Body)
	}
}

// Creation mints no credential, so it must record no issuance (duf-4ac).
//
// This closes an audit gap rather than merely preserving one: before the split,
// CreatePrincipal minted a real, usable secret and the trail recorded only
// principal.create — no secret.issue entry existed for it, so a credential
// entered the system unaccounted for. Every credential is now minted by
// CreatePrincipalSecret, which audits.
func TestCreatingAPrincipalRecordsNoIssuance(t *testing.T) {
	handler := newHandler(
		&fakeTenancyRepository{}, &fakeInstanceRepository{},
		testAuth{}, testRoles{role: identity.RoleRoot},
		testLogger(),
		func() time.Time { return initTestTime },
	)
	handler, trail := auditedPlatform(t, handler)

	w := call(t, handler, http.MethodPost, "/api/v1/principals", map[string]any{
		"name": "build pipeline", "role": "builder", "organization_id": testOrgID,
	}, testToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", w.Code, w.Body)
	}

	record := trail.response(t)
	if record["operation"] != "principal.create" {
		t.Fatalf("operation = %v, want principal.create; record %#v", record["operation"], record)
	}
	if strings.Contains(string(trail.raw), `"operation":"secret.issue"`) {
		t.Fatalf("creation recorded an issuance it did not perform: %s", trail.raw)
	}
	// And the response carries no credential, which the type also prevents.
	if strings.Contains(w.Body.String(), `"secret"`) {
		t.Fatalf("create response carried a secret: %s", w.Body)
	}
}

func TestMaintainerCannotDeleteRootPrincipal(t *testing.T) {
	root, err := identity.NewPrincipal(
		"root-target", "root", "root-client", identity.Scope{}, identity.RoleRoot,
		initTestTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.IssueSecret("root-secret", nil, initTestTime); err != nil {
		t.Fatal(err)
	}
	repository := &fakeTenancyRepository{principals: []*identity.Principal{root}}
	maintainer := testRoles{
		role:  identity.RoleMaintainer,
		scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
	}
	server := platformServerWithRepository(maintainer, repository)
	for _, request := range []struct {
		name, method, path string
	}{
		{"deleting", http.MethodDelete, "/api/v1/principals/" + root.ID},
		{"altering", http.MethodPost, "/api/v1/principals/" + root.ID + "/secrets"},
	} {
		w := call(t, server, request.method, request.path, nil, testToken)
		// 404 rather than 403: the target is platform-scoped, and a refusal that
		// said "forbidden" would confirm it exists (duf-pln).
		if w.Code != http.StatusNotFound {
			t.Fatalf("maintainer %s root = %d, want 404; body %s", request.name, w.Code, w.Body)
		}
	}
}

func TestReaderCannotCreatePrincipal(t *testing.T) {
	reader := testRoles{
		role:  identity.RoleReader,
		scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
	}
	server := platformServer(reader)
	w := call(t, server, http.MethodPost, "/api/v1/principals",
		map[string]any{
			"name": "reader-created", "role": "reader",
			"organization_id": testOrgID,
		}, testToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("reader creating principal = %d, want 403; body %s", w.Code, w.Body)
	}
}

func TestPrincipalInAnotherTenancyIsNotFound(t *testing.T) {
	otherOrganization := uuid.New()
	target, err := identity.NewPrincipal(
		"other-target", "other", "other-client",
		identity.Scope{OrganizationID: otherOrganization}, identity.RoleReader,
		initTestTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	repository := &fakeTenancyRepository{principals: []*identity.Principal{target}}
	maintainer := testRoles{
		role:  identity.RoleMaintainer,
		scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
	}
	server := platformServerWithRepository(maintainer, repository)
	w := call(t, server, http.MethodGet, "/api/v1/principals/"+target.ID, nil, testToken)
	if w.Code != http.StatusNotFound {
		t.Fatalf("principal in another tenancy = %d, want 404; body %s", w.Code, w.Body)
	}
}

// Listing selects EXACTLY the scope the request stands at (duf-4qr): the
// query names a selection, an unqualified request means the caller's own
// standing, and no selection is ever a subtree. The fake repository filters
// exactly like the real one, so a handler that ignored the selection — or
// passed the caller's scope regardless — fails here.
func TestListPrincipalsListsExactlyTheSelectedScope(t *testing.T) {
	platformRoot := &identity.Principal{
		ID: "at-platform", Name: "bootstrap", ClientID: "client-platform",
		Role: identity.RoleRoot, Scope: identity.Scope{}, CreatedAt: initTestTime,
	}
	atOrganization, err := identity.NewPrincipal(
		"at-organization", "org automation", "client-organization",
		identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
		identity.RoleMaintainer, initTestTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	atProject, err := identity.NewPrincipal(
		"at-project", "project automation", "client-project",
		identity.Scope{
			OrganizationID: uuid.MustParse(testOrgID),
			ProjectID:      uuid.MustParse(testProjID),
		},
		identity.RoleBuilder, initTestTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	seeded := []*identity.Principal{platformRoot, atOrganization, atProject}

	root := testRoles{role: identity.RoleRoot, scope: identity.Scope{}}
	organizationMaintainer := testRoles{
		role:  identity.RoleMaintainer,
		scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
	}

	for _, c := range []struct {
		name  string
		roles testRoles
		path  string
		want  string
	}{
		{
			name:  "an unqualified root request lists the platform principals only",
			roles: root,
			path:  "/api/v1/principals",
			want:  "at-platform",
		},
		{
			name:  "an organisation selection lists its organisation-scoped principals only",
			roles: root,
			path:  "/api/v1/principals?organization_id=" + testOrgID,
			want:  "at-organization",
		},
		{
			name:  "a project selection lists that project's principals only",
			roles: root,
			path:  "/api/v1/principals?organization_id=" + testOrgID + "&project_id=" + testProjID,
			want:  "at-project",
		},
		{
			// The default is the CALLER's standing, so a tenancy maintainer
			// naming nothing sees its own scope — never the platform.
			name:  "an unqualified tenancy request lists the caller's own standing",
			roles: organizationMaintainer,
			path:  "/api/v1/principals",
			want:  "at-organization",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := platformServerWithRepository(
				c.roles, &fakeTenancyRepository{principals: seeded},
			)
			w := call(t, server, http.MethodGet, c.path, nil, testToken)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body)
			}
			var body struct {
				Principals []struct{ Id string } `json:"principals"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v; body %s", err, w.Body)
			}
			if len(body.Principals) != 1 || body.Principals[0].Id != c.want {
				t.Fatalf("listed %#v, want exactly %q", body.Principals, c.want)
			}
		})
	}
}

// The selection is a filter, never an authority (duf-4qr): a caller naming a
// scope outside its own binding gets not-found — indistinguishable from a
// scope that does not exist — and nothing from the named scope leaks whatever
// the repository would have answered. Trusting the filter instead of the
// caller's binding makes every case here fail.
func TestListPrincipalsRefusesAForeignSelection(t *testing.T) {
	foreignOrg := "7c1e2d3f-4a5b-4c6d-8e9f-0a1b2c3d4e5f"
	foreign, err := identity.NewPrincipal(
		"foreign-target", "theirs", "client-foreign",
		identity.Scope{OrganizationID: uuid.MustParse(foreignOrg)},
		identity.RoleMaintainer, initTestTime,
	)
	if err != nil {
		t.Fatal(err)
	}

	organizationMaintainer := testRoles{
		role:  identity.RoleMaintainer,
		scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
	}
	projectMaintainer := testRoles{
		role: identity.RoleMaintainer,
		scope: identity.Scope{
			OrganizationID: uuid.MustParse(testOrgID),
			ProjectID:      uuid.MustParse(testProjID),
		},
	}

	for _, c := range []struct {
		name  string
		roles testRoles
		path  string
		code  int
	}{
		{
			name:  "an organisation maintainer naming a foreign organisation",
			roles: organizationMaintainer,
			path:  "/api/v1/principals?organization_id=" + foreignOrg,
			code:  http.StatusNotFound,
		},
		{
			name:  "a project maintainer naming organisation level of its own organisation",
			roles: projectMaintainer,
			path:  "/api/v1/principals?organization_id=" + testOrgID,
			code:  http.StatusNotFound,
		},
		{
			// A project without an organisation is a malformed scope, refused
			// closed rather than interpreted.
			name:  "a project selection with no organisation",
			roles: organizationMaintainer,
			path:  "/api/v1/principals?project_id=" + testProjID,
			code:  http.StatusNotFound,
		},
		{
			// Within a visible tenancy the refusal is the ROLE's, and says so.
			name: "a reader within its own tenancy",
			roles: testRoles{
				role:  identity.RoleReader,
				scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
			},
			path: "/api/v1/principals",
			code: http.StatusForbidden,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := platformServerWithRepository(
				c.roles, &fakeTenancyRepository{principals: []*identity.Principal{foreign}},
			)
			w := call(t, server, http.MethodGet, c.path, nil, testToken)
			if w.Code != c.code {
				t.Fatalf("status = %d, want %d; body %s", w.Code, c.code, w.Body)
			}
			if strings.Contains(w.Body.String(), "foreign-target") ||
				strings.Contains(w.Body.String(), "client-foreign") {
				t.Fatalf("the foreign principal leaked through a refusal: %s", w.Body)
			}
		})
	}
}

// Review finding 9: a maintainer of any organisation could read, delete and
// re-credential a PLATFORM-scoped root principal.
//
// The cause was that a platform-scoped subject has no organisation, so the
// tenancy check had nothing to compare and waved the request through — the same
// shape as finding 1, in a different place. Reaching a platform-scoped subject
// requires root, which is the only role that may be held there (ADR-0019).
func TestAPlatformScopedPrincipalIsReachableOnlyByRoot(t *testing.T) {
	root := &identity.Principal{
		ID: "root-principal", Name: "bootstrap", ClientID: "client-root",
		Role: identity.RoleRoot, Scope: identity.Scope{}, CreatedAt: initTestTime,
	}

	// A maintainer at the top of its own organisation — the most authority
	// anyone can hold without being platform root.
	maintainer := testRoles{
		role:  identity.RoleMaintainer,
		scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
	}

	for _, c := range []struct {
		name   string
		method string
		path   string
	}{
		{"read", http.MethodGet, "/api/v1/principals/root-principal"},
		{"delete", http.MethodDelete, "/api/v1/principals/root-principal"},
		{"issue a secret", http.MethodPost, "/api/v1/principals/root-principal/secrets"},
	} {
		t.Run(c.name, func(t *testing.T) {
			repository := &fakeTenancyRepository{principals: []*identity.Principal{root}}
			server := platformServerWithRepository(maintainer, repository)

			w := call(t, server, c.method, c.path, nil, testToken)
			// EXACTLY not-found, not "either refusal". The original wrote
			// `!= 404 && != 403`, which accepts both and so cannot pin the
			// convention it exists to protect — 403 here confirms the subject
			// exists, since an identifier naming nothing answers 404 (duf-pln).
			if w.Code != http.StatusNotFound {
				t.Fatalf(
					"a maintainer probing a platform-scoped root principal got %d, want 404; body %s",
					w.Code, w.Body,
				)
			}
			// The client id is half a credential. It must not appear whatever
			// the status.
			if strings.Contains(w.Body.String(), "client-root") {
				t.Fatalf("the root principal's client id leaked: %s", w.Body)
			}
		})
	}
}

// And root itself must still be able to, or the fix has locked the platform out
// of its own identities rather than protecting them.
func TestRootReachesAPlatformScopedPrincipal(t *testing.T) {
	root := &identity.Principal{
		ID: "root-principal", Name: "bootstrap", ClientID: "client-root",
		Role: identity.RoleRoot, Scope: identity.Scope{}, CreatedAt: initTestTime,
	}
	repository := &fakeTenancyRepository{principals: []*identity.Principal{root}}
	server := platformServerWithRepository(
		testRoles{role: identity.RoleRoot, scope: identity.Scope{}}, repository,
	)

	w := call(t, server, http.MethodGet, "/api/v1/principals/root-principal", nil, testToken)
	if w.Code != http.StatusOK {
		t.Fatalf("root could not read a platform-scoped principal: status %d, body %s", w.Code, w.Body)
	}
}

// Review finding 12, and duf-6xd which found the first fix for it half-done.
//
// The original test had two subtests and BOTH exercised the success path: the
// one named "a refused creation" posted role:root as a ROOT caller, so MayGrant
// succeeded. Deleting the refusal emission passed. So did deleting either secret
// emission, which had no test at all. The mutation that appeared to verify the
// fix deleted all seven emissions at once, which only proves that some emission
// is covered — not that each is.
//
// So this drives every operation and every outcome, and each case is checked
// against the specific entry it should produce rather than against "an entry
// appeared".
func TestEveryLifecycleOutcomeIsAudited(t *testing.T) {
	// Only the prefix is validated on restore, so this stands in for a stored
	// hash without spending an argon2 derivation per subtest.
	const testEncodedHash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHQ$aGFzaGhhc2g"

	const (
		targetOrg = "11111111-1111-1111-1111-111111111111"
		rootID    = "root-principal"
		builderID = "builder-principal"
		theSecret = "secret-1"
	)

	// A root principal at platform scope, and an ordinary one to act on.
	platformRoot := func() *identity.Principal {
		return &identity.Principal{
			ID: rootID, Name: "bootstrap", ClientID: "client-root",
			Role: identity.RoleRoot, Scope: identity.Scope{}, CreatedAt: initTestTime,
		}
	}
	// Root holding exactly one secret: the only principal whose last secret is
	// still refused, since nothing above it could re-issue (ADR-0004 as amended
	// 2026-08-02).
	soleSecretRoot := func(t *testing.T) *identity.Principal {
		t.Helper()
		secret, err := identity.RestoreSecret(theSecret, testEncodedHash, initTestTime, nil, nil)
		if err != nil {
			t.Fatalf("restore secret: %v", err)
		}
		principal, err := identity.RestorePrincipal(
			rootID, "bootstrap", "client-root",
			identity.Scope{}, identity.RoleRoot, initTestTime, []identity.Secret{secret},
		)
		if err != nil {
			t.Fatalf("restore principal: %v", err)
		}
		return principal
	}
	// The secret count matters per case: issuing needs fewer than two, or it
	// hits the cap. A single fixture silently exercised the cap for one case or
	// the other. Revoking a builder's last secret is no longer refused
	// (ADR-0004, amended 2026-08-02) — only root's is, which soleSecretRoot
	// above covers — so the count is about the issue cap, not the revoke rule.
	tenantBuilder := func(t *testing.T, secretCount int) *identity.Principal {
		t.Helper()
		secrets := make([]identity.Secret, 0, secretCount)
		for _, id := range []string{theSecret, "secret-2"}[:secretCount] {
			secret, err := identity.RestoreSecret(id, testEncodedHash, initTestTime, nil, nil)
			if err != nil {
				t.Fatalf("restore secret: %v", err)
			}
			secrets = append(secrets, secret)
		}
		principal, err := identity.RestorePrincipal(
			builderID, "ci", "client-ci",
			identity.Scope{OrganizationID: uuid.MustParse(targetOrg)},
			identity.RoleBuilder, initTestTime, secrets,
		)
		if err != nil {
			t.Fatalf("restore principal: %v", err)
		}
		return principal
	}

	// The caller acting on itself, for the self-deletion refusal.
	platformSelf := func() *identity.Principal {
		return &identity.Principal{
			ID: testPrincID, Name: "self", ClientID: "client-self",
			Role: identity.RoleRoot, Scope: identity.Scope{}, CreatedAt: initTestTime,
		}
	}

	maintainer := testRoles{
		role:  identity.RoleMaintainer,
		scope: identity.Scope{OrganizationID: uuid.MustParse(targetOrg)},
	}
	root := testRoles{role: identity.RoleRoot, scope: identity.Scope{}}

	for _, c := range []struct {
		name      string
		roles     testRoles
		principal *identity.Principal
		method    string
		path      string
		body      any
		operation string
		outcome   string
		reason    string
		// Who the entry names. "unknown" where the request was refused before
		// the caller was resolved — the honest answer, and one worth asserting:
		// an entry that names nobody cannot answer who did it.
		actor string
		// fault makes storage fail, so the entries a broken database produces
		// are covered too. Those are the entries that matter most during an
		// incident and the ones a happy-path table never reaches.
		fault func(*fakeTenancyRepository)
	}{
		{
			name:      "creation succeeds",
			roles:     root,
			method:    http.MethodPost,
			path:      "/api/v1/principals",
			body:      map[string]any{"name": "ci", "role": "builder", "organization_id": targetOrg},
			operation: "principal.create", outcome: "success", reason: "builder", actor: testPrincID,
		},
		{
			// A GENUINE refusal: a maintainer cannot grant root. The original
			// test used a root caller here and so never reached this branch.
			name:      "creation refused because the role exceeds the grantor",
			roles:     maintainer,
			method:    http.MethodPost,
			path:      "/api/v1/principals",
			body:      map[string]any{"name": "escalate", "role": "root", "organization_id": targetOrg},
			operation: "principal.create", outcome: "refused", reason: "role_exceeds_grantor", actor: testPrincID,
		},
		{
			name:      "reading a principal is audited",
			roles:     maintainer,
			principal: tenantBuilder(t, 1),
			method:    http.MethodGet,
			path:      "/api/v1/principals/" + builderID,
			operation: "principal.read", outcome: "success", reason: "", actor: testPrincID,
		},
		{
			// The finding-9 probe itself. Before duf-6xd this left no entry at
			// all — only a 403 to the client.
			name:      "a refused read leaves an entry, since that is the probe",
			roles:     maintainer,
			principal: platformRoot(),
			method:    http.MethodGet,
			path:      "/api/v1/principals/" + rootID,
			operation: "principal.read", outcome: "refused", reason: "tenancy_refused", actor: "unknown",
		},
		{
			name:      "deletion succeeds",
			roles:     root,
			principal: tenantBuilder(t, 1),
			method:    http.MethodDelete,
			path:      "/api/v1/principals/" + builderID,
			operation: "principal.delete", outcome: "success", reason: "builder", actor: testPrincID,
		},
		{
			name:      "deleting a principal above your own role is refused and recorded",
			roles:     maintainer,
			principal: platformRoot(),
			method:    http.MethodDelete,
			path:      "/api/v1/principals/" + rootID,
			operation: "principal.delete", outcome: "refused", reason: "tenancy_refused", actor: "unknown",
		},
		{
			name:      "issuing a secret is audited",
			roles:     root,
			principal: tenantBuilder(t, 1),
			method:    http.MethodPost,
			path:      "/api/v1/principals/" + builderID + "/secrets",
			operation: "secret.issue", outcome: "success", reason: "", actor: testPrincID,
		},
		{
			name:      "a refused issue is audited",
			roles:     maintainer,
			principal: platformRoot(),
			method:    http.MethodPost,
			path:      "/api/v1/principals/" + rootID + "/secrets",
			operation: "secret.issue", outcome: "refused", reason: "tenancy_refused", actor: "unknown",
		},
		{
			name:      "revoking a secret is audited",
			roles:     root,
			principal: tenantBuilder(t, 2),
			method:    http.MethodDelete,
			path:      "/api/v1/principals/" + builderID + "/secrets/" + theSecret,
			operation: "secret.revoke", outcome: "success", reason: "", actor: testPrincID,
		},
		{
			name:      "creating with an unnamed principal is refused and recorded",
			roles:     root,
			method:    http.MethodPost,
			path:      "/api/v1/principals",
			body:      map[string]any{"name": "", "role": "builder", "organization_id": targetOrg},
			operation: "principal.create", outcome: "refused", reason: "invalid_name", actor: testPrincID,
		},
		{
			name:      "deleting a principal that does not exist is recorded",
			roles:     root,
			method:    http.MethodDelete,
			path:      "/api/v1/principals/" + builderID,
			operation: "principal.delete", outcome: "refused", reason: "not_found", actor: "unknown",
		},
		{
			// Refused by ADR-0019, and an entry an investigation would want:
			// someone tried to remove their own accountability.
			name:      "self-deletion is refused and recorded",
			roles:     root,
			principal: platformSelf(),
			method:    http.MethodDelete,
			path:      "/api/v1/principals/" + testPrincID,
			operation: "principal.delete", outcome: "refused", reason: "self_deletion", actor: testPrincID,
		},
		{
			name:      "issuing past the two-secret cap is recorded",
			roles:     root,
			principal: tenantBuilder(t, 2),
			method:    http.MethodPost,
			path:      "/api/v1/principals/" + builderID + "/secrets",
			operation: "secret.issue", outcome: "failure", reason: "two_usable_secrets", actor: testPrincID,
		},
		{
			name:      "revoking a root's only remaining secret is refused and recorded",
			roles:     root,
			principal: soleSecretRoot(t),
			method:    http.MethodDelete,
			path:      "/api/v1/principals/" + rootID + "/secrets/" + theSecret,
			operation: "secret.revoke", outcome: "failure", reason: "last_usable_root_secret", actor: testPrincID,
		},
		{
			name:      "a principal that already exists is recorded",
			roles:     root,
			method:    http.MethodPost,
			path:      "/api/v1/principals",
			body:      map[string]any{"name": "ci", "role": "builder", "organization_id": targetOrg},
			fault:     func(r *fakeTenancyRepository) { r.createPrincipalErr = identity.ErrConflict },
			operation: "principal.create", outcome: "failure", reason: "already_exists", actor: testPrincID,
		},
		{
			name:   "a principal naming a missing bucket is recorded",
			roles:  root,
			method: http.MethodPost,
			path:   "/api/v1/principals",
			body: map[string]any{
				"name": "ci", "role": "builder",
				"organization_id": targetOrg,
				"project_id":      "11111111-1111-1111-1111-111111111112",
				"bucket_id":       testBucketID,
			},
			fault:     func(r *fakeTenancyRepository) { r.createPrincipalErr = identity.ErrNotFound },
			operation: "principal.create", outcome: "refused", reason: "scope_not_found", actor: testPrincID,
		},
		{
			name:      "storage failing to create is recorded",
			roles:     root,
			method:    http.MethodPost,
			path:      "/api/v1/principals",
			body:      map[string]any{"name": "ci", "role": "builder", "organization_id": targetOrg},
			fault:     func(r *fakeTenancyRepository) { r.createPrincipalErr = errors.New("database unavailable") },
			operation: "principal.create", outcome: "failure", reason: "storage_failed", actor: testPrincID,
		},
		{
			name:      "storage failing to look a principal up is recorded",
			roles:     root,
			principal: tenantBuilder(t, 1),
			method:    http.MethodDelete,
			path:      "/api/v1/principals/" + builderID,
			fault:     func(r *fakeTenancyRepository) { r.getPrincipalErr = errors.New("database unavailable") },
			operation: "principal.delete", outcome: "failure", reason: "lookup_failed", actor: "unknown",
		},
		{
			// The last-root protection, which ADR-0019 makes a hard refusal.
			name:      "refusing to delete the last root is recorded",
			roles:     root,
			principal: tenantBuilder(t, 1),
			method:    http.MethodDelete,
			path:      "/api/v1/principals/" + builderID,
			fault:     func(r *fakeTenancyRepository) { r.deletePrincipalErr = identity.ErrConflict },
			operation: "principal.delete", outcome: "failure", reason: "last_root", actor: testPrincID,
		},
		{
			name:      "storage failing to delete is recorded",
			roles:     root,
			principal: tenantBuilder(t, 1),
			method:    http.MethodDelete,
			path:      "/api/v1/principals/" + builderID,
			fault:     func(r *fakeTenancyRepository) { r.deletePrincipalErr = errors.New("database unavailable") },
			operation: "principal.delete", outcome: "failure", reason: "storage_failed", actor: testPrincID,
		},
		{
			name:      "storage failing to issue a secret is recorded",
			roles:     root,
			principal: tenantBuilder(t, 1),
			method:    http.MethodPost,
			path:      "/api/v1/principals/" + builderID + "/secrets",
			fault:     func(r *fakeTenancyRepository) { r.issueSecretErr = errors.New("database unavailable") },
			operation: "secret.issue", outcome: "failure", reason: "storage_failed", actor: testPrincID,
		},
		{
			name:      "storage failing to revoke a secret is recorded",
			roles:     root,
			principal: tenantBuilder(t, 2),
			method:    http.MethodDelete,
			path:      "/api/v1/principals/" + builderID + "/secrets/" + theSecret,
			fault:     func(r *fakeTenancyRepository) { r.revokeSecretErr = errors.New("database unavailable") },
			operation: "secret.revoke", outcome: "failure", reason: "storage_failed", actor: testPrincID,
		},
		{
			name:      "a secret that no longer exists is recorded",
			roles:     root,
			principal: tenantBuilder(t, 2),
			method:    http.MethodDelete,
			path:      "/api/v1/principals/" + builderID + "/secrets/" + theSecret,
			fault:     func(r *fakeTenancyRepository) { r.revokeSecretErr = identity.ErrNotFound },
			operation: "secret.revoke", outcome: "refused", reason: "not_found", actor: testPrincID,
		},
		{
			name:      "storage failing to look up on read is recorded",
			roles:     root,
			principal: tenantBuilder(t, 1),
			method:    http.MethodGet,
			path:      "/api/v1/principals/" + builderID,
			fault:     func(r *fakeTenancyRepository) { r.getPrincipalErr = errors.New("database unavailable") },
			operation: "principal.read", outcome: "failure", reason: "lookup_failed", actor: "unknown",
		},
		{
			name:      "storage failing to look up on issue is recorded",
			roles:     root,
			principal: tenantBuilder(t, 1),
			method:    http.MethodPost,
			path:      "/api/v1/principals/" + builderID + "/secrets",
			fault:     func(r *fakeTenancyRepository) { r.getPrincipalErr = errors.New("database unavailable") },
			operation: "secret.issue", outcome: "failure", reason: "lookup_failed", actor: "unknown",
		},
		{
			name:      "storage failing to look up on revoke is recorded",
			roles:     root,
			principal: tenantBuilder(t, 2),
			method:    http.MethodDelete,
			path:      "/api/v1/principals/" + builderID + "/secrets/" + theSecret,
			fault:     func(r *fakeTenancyRepository) { r.getPrincipalErr = errors.New("database unavailable") },
			operation: "secret.revoke", outcome: "failure", reason: "lookup_failed", actor: "unknown",
		},
		{
			// The principal vanished between the lookup and the delete.
			name:      "a principal deleted concurrently is recorded",
			roles:     root,
			principal: tenantBuilder(t, 1),
			method:    http.MethodDelete,
			path:      "/api/v1/principals/" + builderID,
			fault:     func(r *fakeTenancyRepository) { r.deletePrincipalErr = identity.ErrNotFound },
			operation: "principal.delete", outcome: "refused", reason: "not_found", actor: testPrincID,
		},
		{
			name:      "a principal that vanished before its secret was issued is recorded",
			roles:     root,
			principal: tenantBuilder(t, 1),
			method:    http.MethodPost,
			path:      "/api/v1/principals/" + builderID + "/secrets",
			fault:     func(r *fakeTenancyRepository) { r.issueSecretErr = identity.ErrNotFound },
			operation: "secret.issue", outcome: "refused", reason: "not_found", actor: testPrincID,
		},
		{
			// Creating at PLATFORM scope — no organisation named — by a caller
			// bound to a tenancy. Standing, not authority (duf-pln).
			name:      "creating a platform principal without standing is recorded",
			roles:     maintainer,
			method:    http.MethodPost,
			path:      "/api/v1/principals",
			body:      map[string]any{"name": "root2", "role": "root"},
			operation: "principal.create", outcome: "refused", reason: "tenancy_refused", actor: "unknown",
		},
		{
			name:      "reading a principal that does not exist is recorded",
			roles:     root,
			method:    http.MethodGet,
			path:      "/api/v1/principals/" + builderID,
			operation: "principal.read", outcome: "refused", reason: "not_found", actor: "unknown",
		},
		{
			name:      "issuing against a principal that does not exist is recorded",
			roles:     root,
			method:    http.MethodPost,
			path:      "/api/v1/principals/" + builderID + "/secrets",
			operation: "secret.issue", outcome: "refused", reason: "not_found", actor: "unknown",
		},
		{
			name:      "revoking against a principal that does not exist is recorded",
			roles:     root,
			method:    http.MethodDelete,
			path:      "/api/v1/principals/" + builderID + "/secrets/" + theSecret,
			operation: "secret.revoke", outcome: "refused", reason: "not_found", actor: "unknown",
		},
		{
			// Passes authorization and the grant check, then fails the domain
			// invariant: root is platform-only, so root WITH an organisation is
			// a malformed binding (ADR-0019, validBinding).
			name:      "a root bound to an organisation is refused and recorded",
			roles:     root,
			method:    http.MethodPost,
			path:      "/api/v1/principals",
			body:      map[string]any{"name": "misbound", "role": "root", "organization_id": targetOrg},
			operation: "principal.create", outcome: "refused", reason: "invalid_scope_or_role", actor: testPrincID,
		},
		{
			name:      "an unrecognised role is refused and recorded",
			roles:     root,
			method:    http.MethodPost,
			path:      "/api/v1/principals",
			body:      map[string]any{"name": "odd", "role": "archivist", "organization_id": targetOrg},
			operation: "principal.create", outcome: "refused", reason: "invalid_role", actor: testPrincID,
		},
		{
			// Asymmetric with the issue path before duf-6xd: the identical check
			// was audited on one and not the other.
			name:      "a refused revoke is audited",
			roles:     maintainer,
			principal: platformRoot(),
			method:    http.MethodDelete,
			path:      "/api/v1/principals/" + rootID + "/secrets/" + theSecret,
			operation: "secret.revoke", outcome: "refused", reason: "tenancy_refused", actor: "unknown",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			repository := &fakeTenancyRepository{}
			if c.principal != nil {
				repository.principals = []*identity.Principal{c.principal}
			}
			if c.fault != nil {
				c.fault(repository)
			}
			server := newHandler(
				repository, &fakeInstanceRepository{}, testAuth{}, c.roles,
				testLogger(),
				func() time.Time { return initTestTime },
			)
			server, trail := auditedPlatform(t, server)

			call(t, server, c.method, c.path, c.body, testToken)
			record := trail.response(t)
			wantActor := c.actor
			if wantActor == "unknown" {
				// Authentication resolved the caller before every lifecycle handler;
				// the response record must not discard attribution just because a
				// target lookup failed later.
				wantActor = testPrincID
			}
			for field, want := range map[string]any{
				"operation": c.operation, "outcome": c.outcome,
				"principal_id": wantActor,
			} {
				if record[field] != want {
					t.Fatalf("%s = %v, want %v; record %#v", field, record[field], want, record)
				}
			}
			if c.reason == "" {
				if _, ok := record["reason"]; ok {
					t.Fatalf("reason present on reasonless success; record %#v", record)
				}
			} else if record["reason"] != c.reason {
				t.Fatalf("reason = %v, want %s; record %#v", record["reason"], c.reason, record)
			}
		})
	}
}

// An issued secret's plaintext must never reach the trail.
//
// ADR-0020 requires sensitive values to be HMAC'd rather than written, and
// ADR-0012 returns credentials in the response body and nowhere else. Emission
// was correct already — it records the secret's identifier — but nothing
// asserted it, so nothing would notice if that changed.
func TestAnIssuedSecretNeverReachesTheAuditTrail(t *testing.T) {
	repository := &fakeTenancyRepository{principals: []*identity.Principal{{
		ID: "target", Name: "ci", ClientID: "client-ci", Role: identity.RoleBuilder,
		Scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)}, CreatedAt: initTestTime,
	}}}
	server := newHandler(
		repository, &fakeInstanceRepository{}, testAuth{},
		testRoles{role: identity.RoleRoot, scope: identity.Scope{}},
		testLogger(),
		func() time.Time { return initTestTime },
	)
	server, trail := auditedPlatform(t, server)

	w := call(t, server, http.MethodPost, "/api/v1/principals/target/secrets", nil, testToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("issue = %d, body %s", w.Code, w.Body)
	}

	var issued struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if issued.Secret == "" {
		t.Fatal("no secret was returned, so this asserts nothing")
	}
	if strings.Contains(string(trail.raw), issued.Secret) {
		t.Fatalf("the issued secret reached the audit trail: %q", trail.raw)
	}
	record := trail.response(t)
	if record["client_secret_hmac"] == nil || record["hmac_key_version"] != "test-v1" {
		t.Fatalf("issued secret audit lacks its versioned HMAC: %#v", record)
	}
}

// Review finding 16 on the platform plane, which 037fd8f silently reverted.
//
// That commit took the whole handler file from an unreviewed branch predating
// the fix, so the raw error detail came back with it and no gate noticed —
// because the redaction had no test on this plane (duf-9fh). This is that test.
func TestPlatformInternalFailuresKeepTheirDetailServerSide(t *testing.T) {
	// Shaped like what pgx returns: table, column and constraint names.
	const leak = `ERROR: insert or update on table "principals" violates ` +
		`foreign key constraint "principals_organization_id_fkey" (SQLSTATE 23503)`

	var logged bytes.Buffer
	repository := &fakeTenancyRepository{listOrganizationsErr: errors.New(leak)}
	server := newHandler(
		repository, &fakeInstanceRepository{}, testAuth{},
		testRoles{role: identity.RoleRoot, scope: identity.Scope{}},
		slog.New(slog.NewTextHandler(&logged, nil)),
		func() time.Time { return initTestTime },
	)
	audited := &responseCorrelationWriter{}
	server = audit.NewHTTPHandler(audited, correlationResolver{}, server, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))

	w := call(t, server, http.MethodGet, "/api/v1/organizations", nil, testToken)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", w.Code, w.Body)
	}
	for _, secret := range []string{"principals", "constraint", "SQLSTATE", "fkey"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("the response leaked %q: %s", secret, w.Body)
		}
	}
	if !strings.Contains(w.Body.String(), "correlation id") {
		t.Fatalf("no correlation id to trace the failure by: %s", w.Body)
	}
	var body Error
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode internal error: %v", err)
	}
	if body.Detail == nil {
		t.Fatalf("internal error has no correlation detail: %s", w.Body)
	}
	correlation := strings.TrimPrefix(*body.Detail, "correlation id ")
	if correlation != audited.correlation {
		t.Fatalf("response correlation = %q, audited correlation = %q", correlation, audited.correlation)
	}
	// And the detail must still reach the operator, or redaction has just lost it.
	if !strings.Contains(logged.String(), "SQLSTATE") {
		t.Fatalf("the detail did not reach the log: %s", logged.String())
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

// Deleting an organisation is a PLATFORM operation, and this pins which
// refusal shape that produces.
//
// 037fd8f swapped this from authorizeTenancy to authorizePlatform by taking a
// whole file from a branch predating 4f3281e — the same mechanism that reverted
// finding 16 in that commit, and equally unannounced. Ratified as the intended
// form (2026-07-31); the point of this test is that changing it again fails a
// gate rather than passing unnoticed.
//
// Both forms are safe. Neither is an existence oracle: the platform check
// refuses before any lookup, so no non-root caller learns whether the
// organisation exists.
func TestDeleteOrganizationIsAPlatformOperation(t *testing.T) {
	foreign := uuid.NewString()

	for _, c := range []struct {
		name  string
		roles testRoles
		path  string
	}{
		{
			// The distinguishing case. Tenancy-first would answer 404 here,
			// because the caller may not see that organisation at all.
			name: "a maintainer of another organisation is forbidden, not not-found",
			roles: testRoles{
				role:  identity.RoleMaintainer,
				scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
			},
			path: "/api/v1/organizations/" + foreign,
		},
		{
			name: "a maintainer of this organisation is forbidden too",
			roles: testRoles{
				role:  identity.RoleMaintainer,
				scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
			},
			path: "/api/v1/organizations/" + testOrgID,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := platformServer(c.roles)
			w := call(t, server, http.MethodDelete, c.path, nil, testToken)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body %s", w.Code, w.Body)
			}
		})
	}

	// And root still reaches it, or the check has locked the platform out of
	// its own organisations.
	server := platformServerWithRepository(
		testRoles{role: identity.RoleRoot, scope: identity.Scope{}},
		&fakeTenancyRepository{organizations: []store.Organization{
			{ID: testOrgID, Name: "mine", CreatedAt: initTestTime},
		}},
	)
	if w := call(t, server, http.MethodDelete, "/api/v1/organizations/"+testOrgID, nil, testToken); w.Code != http.StatusNoContent {
		t.Fatalf("root could not delete an organisation: status %d, body %s", w.Code, w.Body)
	}
}

// A refused probe and an identifier that names nothing are indistinguishable.
//
// duf-pln: finding 9's fix closed the disclosure but left its shape. A scoped
// maintainer probing a real root principal's identifier got 403 while a
// nonexistent one got 404, so the pair still answered "this exists" — the
// oracle finding 9's closing sentence warned about. Mitigated by unguessable
// identifiers, which is why it was not urgent, but ADR-0017 says a caller that
// may not see a thing learns nothing about it.
func TestAProbedPlatformPrincipalIsIndistinguishableFromNothing(t *testing.T) {
	root := &identity.Principal{
		ID: "root-principal", Name: "bootstrap", ClientID: "client-root",
		Role: identity.RoleRoot, Scope: identity.Scope{}, CreatedAt: initTestTime,
	}
	maintainer := testRoles{
		role:  identity.RoleMaintainer,
		scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
	}

	for _, c := range []struct {
		name   string
		method string
		suffix string
	}{
		{"read", http.MethodGet, ""},
		{"delete", http.MethodDelete, ""},
		{"issue a secret", http.MethodPost, "/secrets"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The same request against a real platform principal, and against an
			// identifier that names nothing.
			existing := platformServerWithRepository(
				maintainer, &fakeTenancyRepository{principals: []*identity.Principal{root}},
			)
			absent := platformServerWithRepository(maintainer, &fakeTenancyRepository{})

			real := call(t, existing, c.method,
				"/api/v1/principals/root-principal"+c.suffix, nil, testToken)
			nothing := call(t, absent, c.method,
				"/api/v1/principals/does-not-exist"+c.suffix, nil, testToken)

			if real.Code != nothing.Code {
				t.Fatalf(
					"probing a real platform principal answers %d but a nonexistent one answers %d, "+
						"so the pair confirms existence",
					real.Code, nothing.Code,
				)
			}
			if real.Body.String() != nothing.Body.String() {
				t.Fatalf("bodies differ: real %s, nonexistent %s", real.Body, nothing.Body)
			}
		})
	}
}

// The surfaces outside the deferred-event pattern also leave a trail.
//
// duf-i2u. c998aba covered the five principal operations completely, and three
// surfaces sat outside it: a refused ENUMERATION of principals left nothing
// though reading one was recorded; a body the server could not decode never
// reached the handler that would have opened an event; and authentication
// refusals were free-form logger.Warn while both compatibility planes already
// emitted structured events for the identical failure.
//
// The last of those is anonymous, and auditing it under ADR-0020's fail-closed
// rule is only survivable because the token endpoint is rate-limited. That
// coupling is recorded in the ADR; this test only pins that the entries exist.
func TestSurfacesOutsideTheHandlerAreAudited(t *testing.T) {
	for _, c := range []struct {
		name      string
		token     string
		method    string
		path      string
		body      any
		operation string
		reason    string
	}{
		{
			name:      "a refused enumeration of principals",
			token:     testToken,
			method:    http.MethodGet,
			path:      "/api/v1/principals",
			operation: "principal.list",
			reason:    "role_refused",
		},
		{
			// The tenancy axis of the same refusal (duf-4qr): naming a foreign
			// scope in the listing filter is the enumeration probe an
			// investigation wants recorded, distinctly from a role shortfall.
			name:      "a refused enumeration of a foreign tenancy",
			token:     testToken,
			method:    http.MethodGet,
			path:      "/api/v1/principals?organization_id=7c1e2d3f-4a5b-4c6d-8e9f-0a1b2c3d4e5f",
			operation: "principal.list",
			reason:    "tenancy_refused",
		},
		{
			name:      "a body the server cannot decode",
			token:     testToken,
			method:    http.MethodPost,
			path:      "/api/v1/principals",
			body:      "not-an-object",
			operation: "principal.create",
			reason:    "undecodable_request",
		},
		{
			name:      "a request carrying no token at all",
			token:     "",
			method:    http.MethodGet,
			path:      "/api/v1/principals",
			operation: "principal.list",
			reason:    "missing_token",
		},
		{
			name:      "a request carrying a token we do not recognise",
			token:     "not-the-test-token",
			method:    http.MethodGet,
			path:      "/api/v1/principals",
			operation: "principal.list",
			reason:    "invalid_token",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			// reader cannot list principals, so the enumeration case is refused.
			server := newHandler(
				&fakeTenancyRepository{}, &fakeInstanceRepository{}, testAuth{},
				testRoles{role: identity.RoleReader, scope: identity.Scope{
					OrganizationID: uuid.MustParse(testOrgID),
				}},
				testLogger(),
				func() time.Time { return initTestTime },
			)
			server, trail := auditedPlatform(t, server)

			call(t, server, c.method, c.path, c.body, c.token)
			record := trail.response(t)
			if record["operation"] != c.operation {
				t.Fatalf("operation = %v, want %s; record %#v", record["operation"], c.operation, record)
			}
			if record["reason"] != c.reason {
				t.Fatalf("reason = %v, want %s; record %#v", record["reason"], c.reason, record)
			}
			// A refusal must never echo the credential that was presented.
			if c.token != "" && strings.Contains(string(trail.raw), c.token) {
				t.Fatalf("the presented token reached the trail: %q", trail.raw)
			}
		})
	}
}

func TestPlatformAuditAttributionIncludesOnlyApplicableFields(t *testing.T) {
	t.Run("invalid token is platform scoped with no target", func(t *testing.T) {
		handler := platformServer(testRoles{role: identity.RoleReader})
		handler, trail := auditedPlatform(t, handler)
		call(t, handler, http.MethodGet, "/api/v1/principals", nil, "not-the-test-token")
		assertPlatformAudit(t, trail.response(t), map[string]any{
			"operation": "principal.list", "target_type": "principal_collection",
			"principal_id": "unknown", "identity_kind": "anonymous", "scope": "platform",
			"outcome": "refused", "reason": "invalid_token",
		}, "organization_id", "project_id", "target_id")
	})

	t.Run("collection refusal carries tenancy but no target", func(t *testing.T) {
		const foreignOrg = "7c1e2d3f-4a5b-4c6d-8e9f-0a1b2c3d4e5f"
		handler := platformServer(testRoles{
			role:  identity.RoleReader,
			scope: identity.Scope{OrganizationID: uuid.MustParse(testOrgID)},
		})
		handler, trail := auditedPlatform(t, handler)
		call(t, handler, http.MethodGet, "/api/v1/principals?organization_id="+foreignOrg, nil, testToken)
		assertPlatformAudit(t, trail.response(t), map[string]any{
			"operation": "principal.list", "target_type": "principal_collection",
			"principal_id": testPrincID, "identity_kind": "service_principal",
			"scope": "organization", "organization_id": foreignOrg,
			"outcome": "refused", "reason": "tenancy_refused",
		}, "project_id", "target_id")
	})

	t.Run("platform item carries target but no tenancy", func(t *testing.T) {
		const targetID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
		repository := &fakeTenancyRepository{principals: []*identity.Principal{{
			ID: targetID, Name: "root", ClientID: "root-client",
			Role: identity.RoleRoot, Scope: identity.Scope{}, CreatedAt: initTestTime,
		}}}
		handler := platformServerWithRepository(testRoles{role: identity.RoleRoot}, repository)
		handler, trail := auditedPlatform(t, handler)
		call(t, handler, http.MethodGet, "/api/v1/principals/"+targetID, nil, testToken)
		assertPlatformAudit(t, trail.response(t), map[string]any{
			"operation": "principal.read", "target_type": "principal", "target_id": targetID,
			"principal_id": testPrincID, "identity_kind": "service_principal", "scope": "platform",
			"outcome": "success",
		}, "organization_id", "project_id", "reason")
	})
}

func assertPlatformAudit(t *testing.T, record map[string]any, want map[string]any, absent ...string) {
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
