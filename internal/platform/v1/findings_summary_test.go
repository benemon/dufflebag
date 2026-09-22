package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/scan"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

func findingsSummaryPath() string {
	return "/api/v1/organizations/" + testOrgID + "/projects/" + testProjID +
		"/buckets/images/versions/fingerprint/findings-summary"
}

func findingsSummaryServer(
	repository *fakeTenancyRepository, actor Principals, scanner Scanner,
) http.Handler {
	return newHandlerWithBuildAndAudit(
		repository, &fakeInstanceRepository{claimed: true}, testAuth{}, actor,
		testLogger(), nil, nil, nil, scanner, BuildInfo{}, func() time.Time { return initTestTime },
	)
}

func TestVersionFindingsSummaryRepresentsAbsentScansAsAbsent(t *testing.T) {
	repository := &fakeTenancyRepository{findingsSummary: &store.VersionFindingsSummaryResult{
		Builds: []store.VersionBuildFindingsSummary{{
			BuildID: "build-a", ComponentType: "amazon-ebs", Platform: "aws",
			Inventory: "unparseable", Packages: 0,
		}},
	}}
	handler := findingsSummaryServer(
		repository,
		pinIdentity{id: "reader-a", role: identity.RoleReader, scope: scannerTestScope()},
		healthyScanner(),
	)
	response := call(t, handler, http.MethodGet, findingsSummaryPath(), nil, testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["scanner_configured"] != true {
		t.Fatalf("scanner_configured = %#v, want true", body["scanner_configured"])
	}
	if body["version"] != nil {
		t.Fatalf("version = %#v, want null when no build has a current scan", body["version"])
	}
	builds, ok := body["builds"].([]any)
	if !ok || len(builds) != 1 {
		t.Fatalf("builds = %#v, want one build", body["builds"])
	}
	build := builds[0].(map[string]any)
	if _, present := build["summary"]; present {
		t.Fatalf("unscanned build rendered summary: %#v; want field absent", build["summary"])
	}
	if build["inventory"] != "unparseable" || build["packages"] != float64(0) {
		t.Fatalf("unscanned build = %#v, want unparseable inventory with zero packages", build)
	}
}

func TestVersionFindingsSummaryGeneratedClient(t *testing.T) {
	at := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	repository := &fakeTenancyRepository{findingsSummary: &store.VersionFindingsSummaryResult{
		Version: &store.VersionFindingsSummary{
			Findings: 1, AffectedPackages: 1, Worst: scan.SeverityCritical,
			Counts: store.SeverityCounts{Critical: 1}, BuildsSummarised: 1, ComputedAt: at,
		},
		Builds: []store.VersionBuildFindingsSummary{{
			BuildID: "build-a", ComponentType: "docker", Platform: "linux",
			Inventory: "parsed", Packages: 2,
			Summary: &store.BuildFindingsSummary{
				RunID: "run-a", Scanned: 2, Findings: 1, AffectedPackages: 1,
				Worst: scan.SeverityCritical, Counts: store.SeverityCounts{Critical: 1},
				ComputedAt: at, ObservedAt: at, Adapter: "osv", Engine: "osv.example",
				DatabaseRevision: "unreported", Coverage: scan.Coverage{Submitted: 2},
			},
		}},
	}}
	handler := findingsSummaryServer(
		repository,
		pinIdentity{id: "reader-a", role: identity.RoleReader, scope: scannerTestScope()},
		healthyScanner(),
	)
	server := httptest.NewServer(handler)
	defer server.Close()
	client, err := NewClientWithResponses(server.URL, WithRequestEditorFn(
		func(_ context.Context, request *http.Request) error {
			request.Header.Set("Authorization", "Bearer "+testToken)
			return nil
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.GetVersionFindingsSummaryWithResponse(
		context.Background(), uuid.MustParse(testOrgID), uuid.MustParse(testProjID), "images", "fingerprint",
	)
	if err != nil {
		t.Fatalf("generated client: %v", err)
	}
	if response.JSON200 == nil || response.JSON200.Version == nil ||
		response.JSON200.Version.Findings != 1 || len(response.JSON200.Builds) != 1 ||
		response.JSON200.Builds[0].Summary == nil {
		t.Fatalf("generated client response = %#v", response.JSON200)
	}
}

func TestVersionFindingsSummaryUnknownVersionIsNotFound(t *testing.T) {
	repository := &fakeTenancyRepository{findingsSummaryErr: registry.ErrNotFound}
	handler := findingsSummaryServer(
		repository,
		pinIdentity{id: "reader-a", role: identity.RoleReader, scope: scannerTestScope()}, nil,
	)
	response := call(t, handler, http.MethodGet, findingsSummaryPath(), nil, testToken)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown fingerprint = %d, want 404: %s", response.Code, response.Body)
	}
}

type findingsSummaryIdentity struct {
	pinIdentity
	effectiveRole identity.Role
}

func (i findingsSummaryIdentity) GetPrincipalByID(ctx context.Context, id string) (*identity.Principal, error) {
	principal, err := i.pinIdentity.GetPrincipalByID(ctx, id)
	if err != nil {
		return nil, err
	}
	principal.Role = i.effectiveRole
	return principal, nil
}

func TestVersionFindingsSummaryAuthorizationAxes(t *testing.T) {
	repository := &fakeTenancyRepository{findingsSummary: &store.VersionFindingsSummaryResult{}}
	foreign := findingsSummaryIdentity{
		pinIdentity: pinIdentity{
			id: "foreign", role: identity.RoleReader,
			scope: identity.Scope{OrganizationID: uuid.New(), ProjectID: uuid.New()},
		},
		effectiveRole: identity.Role("below-reader"),
	}
	response := call(t, findingsSummaryServer(repository, foreign, nil), http.MethodGet, findingsSummaryPath(), nil, testToken)
	if response.Code != http.StatusNotFound {
		t.Fatalf("foreign tenancy with insufficient role = %d, want 404 before role disclosure: %s", response.Code, response.Body)
	}

	inScope := findingsSummaryIdentity{
		pinIdentity:   pinIdentity{id: "in-scope", role: identity.RoleReader, scope: scannerTestScope()},
		effectiveRole: identity.Role("below-reader"),
	}
	response = call(t, findingsSummaryServer(repository, inScope, nil), http.MethodGet, findingsSummaryPath(), nil, testToken)
	if response.Code != http.StatusForbidden {
		t.Fatalf("role below reader = %d, want 403: %s", response.Code, response.Body)
	}
}
