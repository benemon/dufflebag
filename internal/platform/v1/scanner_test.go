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
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

type fakeScanner struct {
	health    store.ScannerHealth
	rescanErr error
	rescans   []string
}

func (f *fakeScanner) Health() store.ScannerHealth { return f.health }

func (f *fakeScanner) ManualRescan(_ context.Context, _ store.Tenant, buildID string) error {
	f.rescans = append(f.rescans, buildID)
	return f.rescanErr
}

func scannerServer(roles testRoles, scanner Scanner) http.Handler {
	return newHandlerWithBuildAndAudit(
		&fakeTenancyRepository{}, &fakeInstanceRepository{claimed: true},
		testAuth{}, roles, testLogger(), nil, nil, nil, scanner,
		BuildInfo{}, func() time.Time { return initTestTime },
	)
}

// scannerTestScope is a real project scope: a non-root role with no
// organization fails principal restoration and the request never reaches
// authorization.
func scannerTestScope() identity.Scope {
	return identity.Scope{
		OrganizationID: uuid.MustParse(testOrgID),
		ProjectID:      uuid.MustParse(testProjID),
	}
}

func healthyScanner() *fakeScanner {
	return &fakeScanner{health: store.ScannerHealth{
		State: store.ScannerStateOK, Adapter: "osv", Endpoint: "https://api.osv.dev",
		LastObservedAt: initTestTime,
	}}
}

// TestScannerHealthDetailIsRootOnly covers the role axis. There is no tenancy
// axis: the adapter is instance-wide, so this is a platform-scoped operation.
func TestScannerHealthDetailIsRootOnly(t *testing.T) {
	for _, test := range []struct {
		name  string
		roles testRoles
		token string
		want  int
	}{
		{"anonymous", testRoles{role: identity.RoleRoot}, "", http.StatusUnauthorized},
		{"project reader", testRoles{role: identity.RoleReader, scope: scannerTestScope()}, testToken, http.StatusForbidden},
		{"project maintainer", testRoles{role: identity.RoleMaintainer, scope: scannerTestScope()}, testToken, http.StatusForbidden},
		{"root", testRoles{role: identity.RoleRoot}, testToken, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := scannerServer(test.roles, healthyScanner())
			response := call(t, handler, http.MethodGet, "/api/v1/scanner/health", nil, test.token)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body)
			}
		})
	}
}

func TestScannerHealthReportsAdapterDetail(t *testing.T) {
	scanner := healthyScanner()
	scanner.health.State = store.ScannerStateDegraded
	scanner.health.Detail = "provider unreachable"
	handler := scannerServer(testRoles{role: identity.RoleRoot}, scanner)

	response := call(t, handler, http.MethodGet, "/api/v1/scanner/health", nil, testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	var body struct {
		State    string `json:"state"`
		Adapter  string `json:"adapter"`
		Endpoint string `json:"endpoint"`
		Detail   string `json:"detail"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.State != "degraded" || body.Adapter != "osv" ||
		body.Endpoint != "https://api.osv.dev" || body.Detail != "provider unreachable" {
		t.Fatalf("body = %+v, want the degraded adapter detail", body)
	}
}

// TestScannerHealthWithoutAnAdapterIsDisabled proves an unconfigured
// deployment answers plainly rather than erroring: no scanner is the ordinary
// posture, not a fault.
func TestScannerHealthWithoutAnAdapterIsDisabled(t *testing.T) {
	handler := scannerServer(testRoles{role: identity.RoleRoot}, nil)
	response := call(t, handler, http.MethodGet, "/api/v1/scanner/health", nil, testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	var body struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.State != "disabled" {
		t.Fatalf("state = %q, want disabled", body.State)
	}
}

// TestPublicHealthNamesNoScannerInfrastructure holds the disclosure line: the
// unauthenticated probe carries the coarse state and nothing that names the
// external service.
func TestPublicHealthNamesNoScannerInfrastructure(t *testing.T) {
	scanner := healthyScanner()
	scanner.health.State = store.ScannerStateDegraded
	scanner.health.Detail = "dial tcp 10.0.0.1:443: connection refused"
	handler := scannerServer(testRoles{role: identity.RoleReader, scope: scannerTestScope()}, scanner)

	response := call(t, handler, http.MethodGet, "/sys/health", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	body := response.Body.String()
	var decoded struct {
		Scanner string `json:"scanner"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Scanner != "degraded" {
		t.Fatalf("scanner state = %q, want degraded", decoded.Scanner)
	}
	for _, secret := range []string{"api.osv.dev", "osv", "10.0.0.1", "connection refused"} {
		if strings.Contains(body, secret) {
			t.Fatalf("public health named infrastructure %q: %s", secret, body)
		}
	}
}

// TestScannerStateNeverFailsTheProbe: a registry serves builds, versions and
// channels perfectly well while its scanner is broken, and evicting replicas
// for it would remove the ones doing their actual job.
func TestScannerStateNeverFailsTheProbe(t *testing.T) {
	for _, state := range []string{
		store.ScannerStateDisabled, store.ScannerStateOK,
		store.ScannerStateDegraded, store.ScannerStateAuditPaused,
	} {
		scanner := healthyScanner()
		scanner.health.State = state
		handler := scannerServer(testRoles{role: identity.RoleReader, scope: scannerTestScope()}, scanner)
		response := call(t, handler, http.MethodGet, "/sys/health", nil, "")
		if response.Code != http.StatusOK {
			t.Fatalf("scanner %s made /sys/health answer %d, want 200", state, response.Code)
		}
	}
}

func rescanPath(organizationID, projectID, buildID string) string {
	return "/api/v1/organizations/" + organizationID + "/projects/" + projectID +
		"/builds/" + buildID + "/rescan"
}

// TestRescanAuthorizationBothAxes covers the tenancy axis and the role axis
// separately, because they fail independently.
func TestRescanAuthorizationBothAxes(t *testing.T) {
	inScope := scannerTestScope()
	foreignOrganization, foreignProject := uuid.NewString(), uuid.NewString()

	for _, test := range []struct {
		name       string
		roles      testRoles
		token      string
		org, proj  string
		want       int
		wantRescan bool
	}{
		{"anonymous", testRoles{role: identity.RoleMaintainer, scope: inScope}, "",
			testOrgID, testProjID, http.StatusUnauthorized, false},
		{"reader in scope", testRoles{role: identity.RoleReader, scope: inScope}, testToken,
			testOrgID, testProjID, http.StatusForbidden, false},
		{"builder in scope", testRoles{role: identity.RoleBuilder, scope: inScope}, testToken,
			testOrgID, testProjID, http.StatusForbidden, false},
		{"maintainer in scope", testRoles{role: identity.RoleMaintainer, scope: inScope}, testToken,
			testOrgID, testProjID, http.StatusAccepted, true},
		{"root", testRoles{role: identity.RoleRoot}, testToken,
			testOrgID, testProjID, http.StatusAccepted, true},
		// A tenancy refusal answers 404, not 403: saying "forbidden" would
		// confirm the project exists, and existence is a disclosure
		// (ADR-0017). The role axis above still answers 403, because there
		// the caller could already see the thing.
		{"maintainer wrong organization", testRoles{role: identity.RoleMaintainer, scope: inScope}, testToken,
			foreignOrganization, foreignProject, http.StatusNotFound, false},
		{"maintainer wrong project", testRoles{role: identity.RoleMaintainer, scope: inScope}, testToken,
			testOrgID, foreignProject, http.StatusNotFound, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			scanner := healthyScanner()
			handler := scannerServer(test.roles, scanner)
			response := call(t, handler, http.MethodPost,
				rescanPath(test.org, test.proj, "build-1"), nil, test.token)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body)
			}
			if got := len(scanner.rescans) > 0; got != test.wantRescan {
				t.Fatalf("rescan queued = %v, want %v", got, test.wantRescan)
			}
		})
	}
}

// TestRescanQueuesRatherThanScanning: the button is a request for work, not a
// private fast path, so it answers 202 and the queue decides when.
func TestRescanQueuesRatherThanScanning(t *testing.T) {
	scanner := healthyScanner()
	handler := scannerServer(
		testRoles{role: identity.RoleMaintainer, scope: scannerTestScope()}, scanner)

	for range 3 {
		response := call(t, handler, http.MethodPost,
			rescanPath(testOrgID, testProjID, "build-1"), nil, testToken)
		if response.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202: %s", response.Code, response.Body)
		}
	}
	// Every press reaches the queue; coalescing is the queue's primary key,
	// not something the handler fakes by dropping requests.
	if len(scanner.rescans) != 3 {
		t.Fatalf("queue calls = %d, want one per request", len(scanner.rescans))
	}
}

// TestRescanOfAnIneligibleBuildIsIndistinguishableFromAbsent: which builds a
// channel selects is information (ADR-0017).
func TestRescanOfAnIneligibleBuildIsIndistinguishableFromAbsent(t *testing.T) {
	scanner := healthyScanner()
	scanner.rescanErr = store.ErrScanIneligible
	handler := scannerServer(
		testRoles{role: identity.RoleMaintainer, scope: scannerTestScope()}, scanner)

	response := call(t, handler, http.MethodPost,
		rescanPath(testOrgID, testProjID, "no-such-build"), nil, testToken)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), "eligible") || strings.Contains(response.Body.String(), "channel") {
		t.Fatalf("the refusal disclosed why: %s", response.Body)
	}
}

func TestRescanWithoutAScannerConflicts(t *testing.T) {
	handler := scannerServer(
		testRoles{role: identity.RoleMaintainer, scope: scannerTestScope()}, nil)

	response := call(t, handler, http.MethodPost,
		rescanPath(testOrgID, testProjID, "build-1"), nil, testToken)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", response.Code, response.Body)
	}
}

func TestRescanFailureIsNotReportedAsSuccess(t *testing.T) {
	scanner := healthyScanner()
	scanner.rescanErr = errors.New("database unavailable")
	handler := scannerServer(
		testRoles{role: identity.RoleMaintainer, scope: scannerTestScope()}, scanner)

	response := call(t, handler, http.MethodPost,
		rescanPath(testOrgID, testProjID, "build-1"), nil, testToken)
	if response.Code == http.StatusAccepted {
		t.Fatal("a failed queue write answered 202")
	}
	if strings.Contains(response.Body.String(), "database unavailable") {
		t.Fatalf("the response disclosed the storage error: %s", response.Body)
	}
}
