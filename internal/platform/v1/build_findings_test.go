package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/scan"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

func buildFindingsPath() string {
	return "/api/v1/organizations/" + testOrgID + "/projects/" + testProjID +
		"/buckets/images/versions/fingerprint/builds/build-a/findings"
}

// The web console's fixture is this test's response body: the file is the
// producer's output, not a hand-written copy of it.
const buildFindingsFixture = "../../../web/tests/fixtures/build-findings.json"

func TestBuildFindingsGeneratedClientAndWebFixture(t *testing.T) {
	at := time.Date(2026, 9, 27, 14, 0, 0, 0, time.UTC)
	published := time.Date(2026, 4, 3, 3, 28, 56, 0, time.UTC)
	repository := &fakeTenancyRepository{buildFindings: &store.BuildFindings{
		Inventory: "parsed", PackagesTotal: 3, Scanned: true, PackagesAffected: 2,
		Run: &store.ScanRun{
			ID: "run-a", BuildID: "build-a", Adapter: "osv", Engine: "osv.example",
			DatabaseRevision: "unreported", ObservedAt: at, Coverage: scan.Coverage{Submitted: 3},
		},
		Advisories: []store.BuildAdvisory{{
			Identifier: "GHSA-78h2-9frx-2jm8", Severity: scan.SeverityHigh, Summary: "go-jose accepts unbounded input",
			Aliases: []string{"CVE-2026-34986"}, Published: &published, FixedVersions: []string{"4.1.4"},
			Packages: []store.BuildAdvisoryPackage{
				{Name: "github.com/go-jose/go-jose/v4", Version: "v4.1.1", Purl: "pkg:golang/github.com/go-jose/go-jose/v4@v4.1.1", SBOMID: "sbom-a", FixedVersion: "4.1.4"},
				{Name: "github.com/go-jose/go-jose/v4", Version: "v4.1.1", Purl: "pkg:golang/github.com/go-jose/go-jose/v4@v4.1.1", SBOMID: "sbom-b", FixedVersion: "4.1.4"},
			},
		}, {
			Identifier: "GO-2026-4945", Severity: scan.SeverityUnknown, Summary: "",
			Aliases: []string{}, FixedVersions: []string{},
			Packages: []store.BuildAdvisoryPackage{
				{Name: "golang.org/x/crypto", Version: "v0.31.0", Purl: "pkg:golang/golang.org/x/crypto@v0.31.0", SBOMID: "sbom-a", FixedVersion: ""},
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
	response, err := client.GetBuildFindingsWithResponse(
		context.Background(), uuid.MustParse(testOrgID), uuid.MustParse(testProjID), "images", "fingerprint", "build-a",
	)
	if err != nil {
		t.Fatalf("generated client: %v", err)
	}
	body := response.JSON200
	if body == nil || !body.Scanned || body.Run == nil || body.Run.Id != "run-a" ||
		body.PackagesTotal != 3 || body.PackagesAffected != 2 || len(body.Advisories) != 2 ||
		len(body.Advisories[0].Packages) != 2 || body.Advisories[1].Published != nil {
		t.Fatalf("generated client response = %#v", body)
	}

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, response.Body, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	if os.Getenv("DUFFLEBAG_UPDATE_FIXTURES") != "" {
		if err := os.WriteFile(buildFindingsFixture, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fixture, err := os.ReadFile(buildFindingsFixture)
	if err != nil {
		t.Fatal(err)
	}
	var got, want bytes.Buffer
	if err := json.Compact(&got, response.Body); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&want, fixture); err != nil {
		t.Fatal(err)
	}
	if got.String() != want.String() {
		t.Fatalf("web fixture drifted from the handler's response; regenerate with DUFFLEBAG_UPDATE_FIXTURES=1:\n%s", pretty.String())
	}
}

func TestBuildFindingsUnscannedBuildIsNotAbsent(t *testing.T) {
	repository := &fakeTenancyRepository{buildFindings: &store.BuildFindings{
		Inventory: "parsed", PackagesTotal: 4, Advisories: []store.BuildAdvisory{},
	}}
	handler := findingsSummaryServer(
		repository,
		pinIdentity{id: "reader-a", role: identity.RoleReader, scope: scannerTestScope()},
		healthyScanner(),
	)
	response := call(t, handler, http.MethodGet, buildFindingsPath(), nil, testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["scanned"] != false || body["run"] != nil || body["scanner_configured"] != true {
		t.Fatalf("unscanned build = %#v, want scanned=false, run=null, scanner_configured=true", body)
	}
	if advisories, ok := body["advisories"].([]any); !ok || len(advisories) != 0 {
		t.Fatalf("advisories = %#v, want an empty list, never null", body["advisories"])
	}
}

func TestBuildFindingsUnknownBuildIsNotFound(t *testing.T) {
	repository := &fakeTenancyRepository{buildFindingsErr: registry.ErrNotFound}
	handler := findingsSummaryServer(
		repository,
		pinIdentity{id: "reader-a", role: identity.RoleReader, scope: scannerTestScope()}, nil,
	)
	response := call(t, handler, http.MethodGet, buildFindingsPath(), nil, testToken)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown build = %d, want 404: %s", response.Code, response.Body)
	}
}

func TestBuildFindingsAuthorizationAxes(t *testing.T) {
	repository := &fakeTenancyRepository{buildFindings: &store.BuildFindings{Advisories: []store.BuildAdvisory{}}}
	foreign := findingsSummaryIdentity{
		pinIdentity: pinIdentity{
			id: "foreign", role: identity.RoleReader,
			scope: identity.Scope{OrganizationID: uuid.New(), ProjectID: uuid.New()},
		},
		effectiveRole: identity.Role("below-reader"),
	}
	response := call(t, findingsSummaryServer(repository, foreign, nil), http.MethodGet, buildFindingsPath(), nil, testToken)
	if response.Code != http.StatusNotFound {
		t.Fatalf("foreign tenancy with insufficient role = %d, want 404 before role disclosure: %s", response.Code, response.Body)
	}
	inScope := findingsSummaryIdentity{
		pinIdentity:   pinIdentity{id: "in-scope", role: identity.RoleReader, scope: scannerTestScope()},
		effectiveRole: identity.Role("below-reader"),
	}
	response = call(t, findingsSummaryServer(repository, inScope, nil), http.MethodGet, buildFindingsPath(), nil, testToken)
	if response.Code != http.StatusForbidden {
		t.Fatalf("role below reader = %d, want 403: %s", response.Code, response.Body)
	}
	response = call(t, findingsSummaryServer(repository, inScope, nil), http.MethodGet, buildFindingsPath(), nil, "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401: %s", response.Code, response.Body)
	}
}
