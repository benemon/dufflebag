package hcp2023

import (
	"bytes"
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
	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

const otherOrg = "11111111-2222-4333-8444-555555555555"

// The companion ADR-0017 asks for: authenticate as one principal, request
// another tenant's path, expect not-found. Isolation tests prove rows do not
// leak between scopes; this proves a caller cannot choose its scope.
func TestTenantOutsidePrincipalScopeIsNotFound(t *testing.T) {
	server := newHandler(newFakeRepository(), testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	for _, c := range []struct {
		name string
		path string
		code float64
	}{
		{"another organization", "/packer/2023-01-01/organizations/" + otherOrg + "/projects/" + testProject + "/buckets", 5},
		{"another project", "/packer/2023-01-01/organizations/" + testOrg + "/projects/" + otherOrg + "/buckets", 5},
		{"a version in another organization", "/packer/2023-01-01/organizations/" + otherOrg + "/projects/" + testProject + "/buckets/b/versions/f", 10},
		{"vulnerabilities in another organization", "/packer/2023-01-01/organizations/" + otherOrg + "/projects/" + testProject + "/buckets/b/vulnerabilities", 5},
	} {
		t.Run(c.name, func(t *testing.T) {
			response := request(t, server, http.MethodGet, c.path, nil)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404; body %s", response.Code, response.Body)
			}
			var body map[string]any
			decodeResponse(t, response, &body)
			// The refusal must be indistinguishable from that endpoint's own
			// missing-resource answer, or its shape reveals that the tenant exists.
			if body["code"] != c.code {
				t.Fatalf("code = %v, want %v", body["code"], c.code)
			}
		})
	}
}

func TestBucketOutsidePrincipalScopeIsNotFound(t *testing.T) {
	repository := newFakeRepository()
	ownedID := registry.NewID(testTime)
	repository.buckets["owned"] = &store.Bucket{ID: ownedID, Name: "owned"}
	repository.buckets["sibling"] = &store.Bucket{
		ID: registry.NewID(testTime.Add(time.Second)), Name: "sibling",
	}
	principals := fakePrincipals{
		role: identity.RolePublisher,
		scope: identity.Scope{
			OrganizationID: uuid.MustParse(testOrg),
			ProjectID:      uuid.MustParse(testProject),
			BucketID:       ownedID.String(),
		},
	}
	server := newHandler(repository, principals, testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	if response := request(t, server, http.MethodGet, testBase+"/buckets/owned", nil); response.Code != http.StatusOK {
		t.Fatalf("owned bucket status = %d, want 200; body %s", response.Code, response.Body)
	}
	if response := request(t, server, http.MethodPatch, testBase+"/buckets/owned", map[string]any{
		"description": "owned update",
	}); response.Code != http.StatusOK {
		t.Fatalf("owned bucket update = %d, want 200; body %s", response.Code, response.Body)
	}
	for _, tc := range []struct {
		name, method, path string
		body               any
	}{
		{"sibling read", http.MethodGet, testBase + "/buckets/sibling", nil},
		{"bucket create", http.MethodPut, testBase + "/buckets", map[string]any{"name": "new"}},
		{"bucket delete", http.MethodDelete, testBase + "/buckets/owned", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := request(t, server, tc.method, tc.path, tc.body)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404; body %s", response.Code, response.Body)
			}
			var body map[string]any
			decodeResponse(t, response, &body)
			if body["code"] != float64(5) {
				t.Fatalf("code = %v, want 5", body["code"])
			}
		})
	}
}

func TestVulnerabilityReadsRequireReaderInScope(t *testing.T) {
	repository := newFakeRepository()
	seed := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	request(t, seed, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	request(t, seed, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp", "template_type": "HCL2"})
	request(t, seed, http.MethodPost, testBase+"/buckets/images/versions/fp/builds",
		map[string]any{"component_type": "docker"})
	buildID := repository.builds["images/fp"][0].ID.String()
	repository.scanStates[buildID] = &store.BuildScanState{
		BuildID: buildID, CurrentFindingsRunID: "run-current", LatestAttemptRunID: "run-current",
	}
	repository.scanRuns["run-current"] = &store.ScanRun{
		ID: "run-current", BuildID: buildID, Status: store.ScanRunSucceeded, ObservedAt: testTime,
	}
	path := testBase + "/buckets/images/vulnerabilities"

	reader := newHandler(repository, fakePrincipals{role: identity.RoleReader}, testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	if response := request(t, reader, http.MethodGet, path, nil); response.Code != http.StatusOK {
		t.Fatalf("reader in scope = %d %s, want 200", response.Code, response.Body)
	}
	belowReader := newHandler(repository, fakePrincipals{role: identity.RoleReader, belowReader: true}, testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	if response := request(t, belowReader, http.MethodGet, path, nil); response.Code != http.StatusForbidden {
		t.Fatalf("below-reader principal = %d %s, want 403", response.Code, response.Body)
	}
	wrongTenantPath := "/packer/2023-01-01/organizations/" + otherOrg + "/projects/" + testProject + "/buckets/images/vulnerabilities"
	if response := request(t, reader, http.MethodGet, wrongTenantPath, nil); response.Code != http.StatusNotFound {
		t.Fatalf("wrong-tenant reader = %d %s, want 404", response.Code, response.Body)
	}
}

func TestRestrictedChannelAuthorization(t *testing.T) {
	newServer := func(role identity.Role) (*fakeRepository, http.Handler) {
		repository := newFakeRepository()
		repository.buckets["images"] = &store.Bucket{
			ID: registry.NewID(testTime), Name: "images", Labels: map[string]string{},
			CreatedAt: testTime, UpdatedAt: testTime,
		}
		version, err := registry.RestoreVersion(registry.Version{
			ID: registry.NewID(testTime.Add(time.Second)), BucketName: "images",
			Fingerprint: "fp", TemplateType: registry.TemplateHCL2,
			CreatedAt: testTime, UpdatedAt: testTime,
		}, true, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		repository.versions["images/fp"] = version
		for i, channel := range []store.Channel{
			{Name: "latest", Restricted: true, Managed: true, Version: version},
			{Name: "open", Version: version},
			{Name: "restricted", Restricted: true},
		} {
			channel.ID = registry.NewID(testTime.Add(time.Duration(i+2) * time.Second))
			channel.BucketName = "images"
			channel.CreatedAt = testTime
			channel.UpdatedAt = testTime
			repository.channels["images/"+channel.Name] = &channel
		}
		return repository, newHandler(
			repository, fakePrincipals{role: role}, testAuthenticator{}, testLogger(),
			func() time.Time { return testTime },
		)
	}
	channelsPath := testBase + "/buckets/images/channels"

	t.Run("reader get is byte-identical to absent channel", func(t *testing.T) {
		_, server := newServer(identity.RoleReader)
		restricted := request(t, server, http.MethodGet, channelsPath+"/restricted", nil)
		absentRepository, absentServer := newServer(identity.RoleReader)
		delete(absentRepository.channels, "images/restricted")
		absent := request(t, absentServer, http.MethodGet, channelsPath+"/restricted", nil)
		if restricted.Code != absent.Code || restricted.Body.String() != absent.Body.String() {
			t.Fatalf("restricted and absent differ:\n restricted: %d %s\n absent: %d %s",
				restricted.Code, restricted.Body, absent.Code, absent.Body)
		}
	})

	t.Run("reader history is byte-identical to absent channel", func(t *testing.T) {
		_, server := newServer(identity.RoleReader)
		restricted := request(t, server, http.MethodGet, channelsPath+"/restricted/history", nil)
		absentRepository, absentServer := newServer(identity.RoleReader)
		delete(absentRepository.channels, "images/restricted")
		absent := request(t, absentServer, http.MethodGet, channelsPath+"/restricted/history", nil)
		if restricted.Code != absent.Code || restricted.Body.String() != absent.Body.String() {
			t.Fatalf("restricted and absent histories differ:\n restricted: %d %s\n absent: %d %s",
				restricted.Code, restricted.Body, absent.Code, absent.Body)
		}
	})

	t.Run("reader list filters all restricted channels", func(t *testing.T) {
		_, server := newServer(identity.RoleReader)
		response := request(t, server, http.MethodGet, channelsPath, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body %s", response.Code, response.Body)
		}
		var body struct {
			Channels []struct {
				Name string `json:"name"`
			} `json:"channels"`
		}
		decodeResponse(t, response, &body)
		var names []string
		for _, channel := range body.Channels {
			names = append(names, channel.Name)
		}
		if got, want := strings.Join(names, ","), "open"; got != want {
			t.Fatalf("channels = %q, want %q", got, want)
		}
	})

	t.Run("builder consumes restricted", func(t *testing.T) {
		_, server := newServer(identity.RoleBuilder)
		response := request(t, server, http.MethodGet, channelsPath+"/restricted", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body %s", response.Code, response.Body)
		}
	})

	t.Run("reader latest is byte-identical to absent channel", func(t *testing.T) {
		_, server := newServer(identity.RoleReader)
		restricted := request(t, server, http.MethodGet, channelsPath+"/latest", nil)
		absentRepository, absentServer := newServer(identity.RoleReader)
		delete(absentRepository.channels, "images/latest")
		absent := request(t, absentServer, http.MethodGet, channelsPath+"/latest", nil)
		if restricted.Code != absent.Code || restricted.Body.String() != absent.Body.String() {
			t.Fatalf("restricted latest and absent latest differ:\n restricted: %d %s\n absent: %d %s",
				restricted.Code, restricted.Body, absent.Code, absent.Body)
		}
	})

	t.Run("builder consumes managed latest", func(t *testing.T) {
		_, server := newServer(identity.RoleBuilder)
		response := request(t, server, http.MethodGet, channelsPath+"/latest", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body %s", response.Code, response.Body)
		}
	})

	assertRoleRefusal := func(t *testing.T, response *httptest.ResponseRecorder) {
		t.Helper()
		_, reader := newServer(identity.RoleReader)
		want := request(t, reader, http.MethodPut, testBase+"/buckets", map[string]any{"name": "new"})
		if response.Code != want.Code || response.Body.String() != want.Body.String() {
			t.Fatalf("response = %d %s, want role refusal %d %s",
				response.Code, response.Body, want.Code, want.Body)
		}
	}

	for _, c := range []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"assign restricted", http.MethodPost, channelsPath + "/assign",
			map[string]any{"source_channel": "open", "target_channel": "restricted"}},
		{"update restricted", http.MethodPatch, channelsPath + "/restricted",
			map[string]any{"update_mask": "versionFingerprint", "version_fingerprint": "fp"}},
		{"delete restricted", http.MethodDelete, channelsPath + "/restricted", nil},
		{"create restricted", http.MethodPost, channelsPath,
			map[string]any{"name": "private", "restricted": true}},
		{"set restricted mask", http.MethodPatch, channelsPath + "/open",
			map[string]any{"update_mask": "restricted", "restricted": true}},
		{"clear restricted mask", http.MethodPatch, channelsPath + "/restricted",
			map[string]any{"update_mask": "restricted", "restricted": false}},
	} {
		t.Run("publisher cannot "+c.name, func(t *testing.T) {
			_, server := newServer(identity.RolePublisher)
			assertRoleRefusal(t, request(t, server, c.method, c.path, c.body))
		})
	}

	for _, c := range []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"assign restricted", http.MethodPost, channelsPath + "/assign",
			map[string]any{"source_channel": "open", "target_channel": "restricted"}},
		{"update restricted", http.MethodPatch, channelsPath + "/restricted",
			map[string]any{"update_mask": "versionFingerprint", "version_fingerprint": "fp"}},
		{"delete restricted", http.MethodDelete, channelsPath + "/restricted", nil},
		{"create restricted", http.MethodPost, channelsPath,
			map[string]any{"name": "private", "restricted": true}},
		{"clear restricted mask", http.MethodPatch, channelsPath + "/restricted",
			map[string]any{"update_mask": "restricted", "restricted": false}},
	} {
		t.Run("maintainer may "+c.name, func(t *testing.T) {
			_, server := newServer(identity.RoleMaintainer)
			response := request(t, server, c.method, c.path, c.body)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", response.Code, response.Body)
			}
		})
	}
}

func TestRestrictedChannelRefusalAuditReasons(t *testing.T) {
	repository := newFakeRepository()
	repository.buckets["images"] = &store.Bucket{
		ID: registry.NewID(testTime), Name: "images", Labels: map[string]string{},
		CreatedAt: testTime, UpdatedAt: testTime,
	}
	repository.channels["images/restricted"] = &store.Channel{
		ID: registry.NewID(testTime.Add(time.Second)), BucketName: "images", Name: "restricted",
		Restricted: true, CreatedAt: testTime, UpdatedAt: testTime,
	}

	for _, c := range []struct {
		name, reason string
		role         identity.Role
		method, path string
	}{
		{"consumption", "restricted_channel_consumption", identity.RoleReader,
			http.MethodGet, testBase + "/buckets/images/channels/restricted"},
		{"management", "restricted_channel_management", identity.RolePublisher,
			http.MethodDelete, testBase + "/buckets/images/channels/restricted"},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := newHandler(repository, fakePrincipals{role: c.role}, testAuthenticator{},
				testLogger(), func() time.Time { return testTime })
			trail := &auditTrail{}
			audited := audit.NewHTTPHandler(
				trail, server.(audit.Resolver), server,
				audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")),
			)
			request(t, audited, c.method, c.path, nil)
			responses := trail.responses(t)
			if len(responses) != 1 {
				t.Fatalf("response records = %d, want 1", len(responses))
			}
			assertAuditFields(t, responses[0], map[string]any{
				"outcome": "refused", "reason": c.reason,
			})
		})
	}
}

func TestListRemainderRoutesRefuseAnotherTenant(t *testing.T) {
	repository := newFakeRepository()
	seed := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	request(t, seed, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	request(t, seed, http.MethodPost, testBase+"/buckets/images/channels", map[string]any{"name": "production"})
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	base := "/packer/2023-01-01/organizations/" + otherOrg + "/projects/" + testProject

	for _, path := range []string{
		base + "/buckets/images/channels/production/history",
		base + "/buckets/images/ancestry",
	} {
		response := request(t, server, http.MethodGet, path, nil)
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":5`) {
			t.Fatalf("GET %s = %d %s, want tenant-concealing 404/code 5",
				path, response.Code, response.Body)
		}
	}
}

// A malformed tenant and an unauthorized one must look identical, so a caller
// cannot tell a real organization from a typo.
func TestMalformedTenantMatchesUnauthorizedTenant(t *testing.T) {
	server := newHandler(newFakeRepository(), testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	malformed := request(t, server, http.MethodGet, "/packer/2023-01-01/organizations/not-a-uuid/projects/"+testProject+"/buckets", nil)
	unauthorized := request(t, server, http.MethodGet, "/packer/2023-01-01/organizations/"+otherOrg+"/projects/"+testProject+"/buckets", nil)

	if malformed.Code != unauthorized.Code || malformed.Body.String() != unauthorized.Body.String() {
		t.Fatalf("responses differ:\n malformed:    %d %s\n unauthorized: %d %s",
			malformed.Code, malformed.Body.String(), unauthorized.Code, unauthorized.Body.String())
	}
}

func TestRequestsWithoutAValidTokenAreRefused(t *testing.T) {
	server := newHandler(newFakeRepository(), testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	path := testBase + "/buckets"

	for _, c := range []struct{ name, header string }{
		{"no header", ""},
		{"empty bearer", "Bearer "},
		{"wrong scheme", "Basic dXNlcjpwYXNz"},
		{"rejected token", "Bearer not-the-test-token"},
		{"token as scheme", testToken},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			if c.header != "" {
				r.Header.Set("Authorization", c.header)
			}
			w := httptest.NewRecorder()
			server.ServeHTTP(w, r)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body %s", w.Code, w.Body)
			}
			if w.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("a 401 must carry a challenge")
			}
		})
	}
}

func TestAuthenticationAndScopeRefusalsUseAuditSchema(t *testing.T) {
	server := newHandler(newFakeRepository(), testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	trail := &auditTrail{}
	server = audit.NewHTTPHandler(
		trail, server.(audit.Resolver), server, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")),
	)

	const rejectedToken = "known-rejected-bearer-token"
	r := httptest.NewRequest(http.MethodGet, testBase+"/buckets", nil)
	r.Header.Set("Authorization", "Bearer "+rejectedToken)
	server.ServeHTTP(httptest.NewRecorder(), r)
	request(t, server, http.MethodGet,
		"/packer/2023-01-01/organizations/"+otherOrg+"/projects/"+testProject+"/buckets", nil)

	output := string(trail.raw)
	if strings.Contains(output, rejectedToken) {
		t.Fatalf("audit log contains rejected bearer token %q: %s", rejectedToken, output)
	}
	responses := trail.responses(t)
	if len(responses) != 2 {
		t.Fatalf("response records = %d, want 2", len(responses))
	}
	assertAuditFields(t, responses[0], map[string]any{
		"operation": "bucket.list", "target_type": "bucket_collection",
		"principal_id": "unknown", "identity_kind": "unknown", "scope": "platform",
		"outcome": "refused", "reason": "invalid_token",
	}, "organization_id", "project_id", "target_id")
	assertAuditFields(t, responses[1], map[string]any{
		"operation": "bucket.list", "target_type": "bucket_collection",
		"principal_id": "p-test", "identity_kind": "service_principal", "scope": "project",
		"organization_id": otherOrg, "project_id": testProject,
		"outcome": "refused", "reason": "outside_principal_scope",
	}, "target_id")
}

type auditTrail struct {
	raw     []byte
	records []map[string]any
}

func (w *auditTrail) Write(encoded []byte) error {
	w.raw = append(w.raw, encoded...)
	var record map[string]any
	if err := json.Unmarshal(encoded, &record); err != nil {
		return err
	}
	w.records = append(w.records, record)
	return nil
}

func (w *auditTrail) responses(t *testing.T) []map[string]any {
	t.Helper()
	var responses []map[string]any
	for _, record := range w.records {
		if record["kind"] == "response" {
			responses = append(responses, record)
		}
	}
	return responses
}

func assertAuditFields(t *testing.T, record map[string]any, want map[string]any, absent ...string) {
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

// RFC 6750 makes the scheme case-insensitive.
func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	server := newHandler(newFakeRepository(), testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	r := httptest.NewRequest(http.MethodGet, testBase+"/buckets", nil)
	r.Header.Set("Authorization", "bearer "+testToken)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body)
	}
}

// An organization-scoped principal reaches every project in its organization;
// a project-scoped one does not. Both kinds must work, because the Packer CLI
// behaves differently for each (ADR-0016).
func TestOrganizationScopedPrincipalReachesAnyProjectInItsOrganization(t *testing.T) {
	// Scope now comes from the RESOLVED principal rather than the token claim,
	// so the principals source is what makes this organization-scoped.
	orgScoped := fakePrincipals{
		role:  identity.RolePublisher,
		scope: identity.Scope{OrganizationID: uuid.MustParse(testOrg)},
	}
	server := newHandler(newFakeRepository(), orgScoped, orgScopedAuthenticator{}, testLogger(), func() time.Time { return testTime })

	within := request(t, server, http.MethodGet,
		"/packer/2023-01-01/organizations/"+testOrg+"/projects/"+uuid.New().String()+"/buckets", nil)
	if within.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; an organization-scoped principal must reach its own projects", within.Code)
	}

	outside := request(t, server, http.MethodGet,
		"/packer/2023-01-01/organizations/"+otherOrg+"/projects/"+testProject+"/buckets", nil)
	if outside.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; organization scope must not cross organizations", outside.Code)
	}
}

type orgScopedAuthenticator struct{}

func (orgScopedAuthenticator) Verify(token string) (identity.Verified, error) {
	if token != testToken {
		return identity.Verified{}, identity.ErrInvalid
	}
	return identity.Verified{
		PrincipalID: "p-org",
		Scope:       identity.Scope{OrganizationID: uuid.MustParse(testOrg)},
		SecretID:    testSecretID,
	}, nil
}

// Every route declares a required role and the request path enforces it. These
// are the boundaries that matter: a CI credential must not be able to promote,
// and a read-only credential must not be able to write (ADR-0019).
func TestRoutesEnforceTheirRequiredRole(t *testing.T) {
	channelBody := map[string]any{"name": "production"}
	for _, c := range []struct {
		name    string
		role    identity.Role
		method  string
		path    string
		body    any
		allowed bool
	}{
		{"reader may read", identity.RoleReader, http.MethodGet, "/buckets", nil, true},
		{"reader may not create a bucket", identity.RoleReader, http.MethodPut, "/buckets",
			map[string]any{"name": "images"}, false},
		{"reader may not create a version", identity.RoleReader, http.MethodPost,
			"/buckets/images/versions", map[string]any{"fingerprint": "fp", "template_type": "HCL2"}, false},

		{"builder may create a bucket", identity.RoleBuilder, http.MethodPut, "/buckets",
			map[string]any{"name": "images"}, true},
		{"builder may NOT promote", identity.RoleBuilder, http.MethodPost,
			"/buckets/images/channels", channelBody, false},
		{"builder may not delete a version", identity.RoleBuilder, http.MethodDelete,
			"/buckets/images/versions/missing", nil, false},
		{"builder may not delete a build", identity.RoleBuilder, http.MethodDelete,
			"/buckets/images/versions/missing/builds/missing", nil, false},

		{"publisher may promote", identity.RolePublisher, http.MethodPost,
			"/buckets/images/channels", channelBody, true},
		{"publisher may delete a version", identity.RolePublisher, http.MethodDelete,
			"/buckets/images/versions/missing", nil, true},
		{"publisher may delete a build", identity.RolePublisher, http.MethodDelete,
			"/buckets/images/versions/missing/builds/missing", nil, true},

		// Root outranks the tenancy question, so it reaches everything.
		{"root may promote anywhere", identity.RoleRoot, http.MethodPost,
			"/buckets/images/channels", channelBody, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			repository := newFakeRepository()
			principals := fakePrincipals{role: c.role}
			if c.role == identity.RoleRoot {
				principals.scope = identity.Scope{}
			}
			server := newHandler(repository, principals, testAuthenticator{}, testLogger(),
				func() time.Time { return testTime })

			// A bucket the channel routes can act on, created with authority that
			// is not under test here.
			seed := newHandler(repository, fakePrincipals{role: identity.RolePublisher},
				testAuthenticator{}, testLogger(), func() time.Time { return testTime })
			request(t, seed, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
			if c.method == http.MethodDelete && strings.Contains(c.path, "/versions/") {
				request(t, seed, http.MethodPost, testBase+"/buckets/images/versions",
					map[string]any{"fingerprint": "missing", "template_type": "HCL2"})
			}
			if c.method == http.MethodDelete && strings.Contains(c.path, "/builds/") {
				repository.builds["images/missing"] = []store.StoredBuild{{
					Build: registry.Build{ID: registry.ID("missing")},
				}}
			}

			response := request(t, server, c.method, testBase+c.path, c.body)
			if c.allowed {
				if response.Code == http.StatusForbidden || response.Code == http.StatusNotFound {
					t.Fatalf("%s was refused: %d %s", c.name, response.Code, response.Body)
				}
				return
			}
			// FORBIDDEN, not not-found. The caller is bound to this tenancy and
			// can already read it, so there is no existence to conceal — and a
			// not-found would send someone hunting for a typo in a correct name.
			if response.Code != http.StatusForbidden {
				t.Fatalf("%s: status = %d, want 403; body %s", c.name, response.Code, response.Body)
			}
		})
	}
}

func TestEveryPackerRouteHasItsExactAuditDescriptor(t *testing.T) {
	type semantic struct {
		operation, targetType, targetIDParam string
	}
	expected := map[string]semantic{
		"POST /_search/external_artifact":                                                   {"artifact.search", "artifact_collection", ""},
		"GET /registry":                                                                     {"registry.read", "registry", ""},
		"GET /enforced_blocks/bucket/{bucket}":                                              {"enforced_block.list", "bucket", "bucket"},
		"GET /buckets":                                                                      {"bucket.list", "bucket_collection", ""},
		"GET /buckets/{bucket}":                                                             {"bucket.read", "bucket", "bucket"},
		"PUT /buckets":                                                                      {"bucket.create", "bucket", ""},
		"PATCH /buckets/{bucket}":                                                           {"bucket.update", "bucket", "bucket"},
		"DELETE /buckets/{bucket}":                                                          {"bucket.delete", "bucket", "bucket"},
		"GET /buckets/{bucket}/ancestry":                                                    {"bucket_ancestry.list", "bucket", "bucket"},
		"GET /buckets/{bucket}/channels":                                                    {"channel.list", "channel_collection", ""},
		"POST /buckets/{bucket}/channels":                                                   {"channel.create", "channel", ""},
		"POST /buckets/{bucket}/channels/assign":                                            {"channel.assign", "channel", ""},
		"GET /buckets/{bucket}/channels/{channel}":                                          {"channel.read", "channel", "channel"},
		"PATCH /buckets/{bucket}/channels/{channel}":                                        {"channel.update", "channel", "channel"},
		"DELETE /buckets/{bucket}/channels/{channel}":                                       {"channel.delete", "channel", "channel"},
		"GET /buckets/{bucket}/channels/{channel}/history":                                  {"channel_history.list", "channel", "channel"},
		"GET /buckets/{bucket}/packages/vulnerability-summary":                              {"vulnerability.summary", "bucket", "bucket"},
		"GET /buckets/{bucket}/packages/with-vulnerabilities":                               {"vulnerability.package_list", "bucket", "bucket"},
		"GET /buckets/{bucket}/vulnerabilities":                                             {"vulnerability.list", "bucket", "bucket"},
		"GET /buckets/{bucket}/versions":                                                    {"version.list", "version_collection", ""},
		"GET /buckets/{bucket}/versions/{fingerprint}":                                      {"version.read", "version", "fingerprint"},
		"PATCH /buckets/{bucket}/versions/{fingerprint}":                                    {"version.update", "version", "fingerprint"},
		"DELETE /buckets/{bucket}/versions/{fingerprint}":                                   {"version.delete", "version", "fingerprint"},
		"POST /buckets/{bucket}/versions":                                                   {"version.create", "version", ""},
		"POST /buckets/{bucket}/versions/{fingerprint}/builds":                              {"build.create", "build", ""},
		"GET /buckets/{bucket}/versions/{fingerprint}/builds":                               {"build.list", "build_collection", ""},
		"GET /buckets/{bucket}/versions/{fingerprint}/builds/{build}/packages":              {"package.list", "build", "build"},
		"GET /buckets/{bucket}/versions/{fingerprint}/builds/{build}/sboms":                 {"sbom.list", "build", "build"},
		"GET /buckets/{bucket}/versions/{fingerprint}/builds/{build}/sboms/{sbom}":          {"sbom.read", "sbom", "sbom"},
		"GET /buckets/{bucket}/versions/{fingerprint}/builds/{build}/sboms/{sbom}/download": {"sbom.download", "sbom", "sbom"},
		"PATCH /buckets/{bucket}/versions/{fingerprint}/builds/{build}":                     {"build.update", "build", "build"},
		"DELETE /buckets/{bucket}/versions/{fingerprint}/builds/{build}":                    {"build.delete", "build", "build"},
		"PUT /buckets/{bucket}/versions/{fingerprint}/builds/{build}/sboms":                 {"sbom.upload", "build", "build"},
	}
	seen := make(map[string]bool, len(routes()))
	for _, route := range routes() {
		key := route.method + " " + route.path
		seen[key] = true
		want, ok := expected[key]
		if !ok {
			t.Errorf("registered Packer route %s has no expected audit descriptor", key)
			continue
		}
		got := semantic{string(route.operation), route.targetType, route.targetIDParam}
		if got != want {
			t.Errorf("Packer route %s descriptor = %#v, want %#v", key, got, want)
		}
	}
	for key := range expected {
		if !seen[key] {
			t.Errorf("expected described Packer route %s is not registered", key)
		}
	}
}

// A token naming a principal that no longer resolves is refused rather than
// trusted for what its claims say — deletion takes effect immediately.
func TestTokenForADeletedPrincipalIsRefused(t *testing.T) {
	server := newHandler(newFakeRepository(), fakePrincipals{missing: true},
		testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	trail := &auditTrail{}
	server = audit.NewHTTPHandler(trail, server.(audit.Resolver), server, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))

	response := request(t, server, http.MethodGet, testBase+"/buckets", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a principal that no longer exists", response.Code)
	}
	assertAuditFields(t, trail.responses(t)[0], map[string]any{
		"operation": "bucket.list", "target_type": "bucket_collection",
		"principal_id": "p-test", "identity_kind": "unknown", "scope": "project",
		"organization_id": testOrg, "project_id": testProject,
		"outcome": "refused", "reason": "principal_unresolvable",
	}, "target_id")
}

// The two refusals must stay distinguishable from each other and each must be
// the right one. A tenancy the caller may not see answers not-found, because
// its existence is a secret; a role it lacks within a tenancy it CAN see
// answers forbidden, because there is nothing left to conceal (ADR-0016).
func TestTenancyRefusalHidesExistenceButRoleRefusalDoesNot(t *testing.T) {
	reader := fakePrincipals{role: identity.RoleReader}
	server := newHandler(newFakeRepository(), reader, testAuthenticator{}, testLogger(),
		func() time.Time { return testTime })

	// Right tenancy, insufficient role.
	role := request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	if role.Code != http.StatusForbidden {
		t.Fatalf("insufficient role = %d, want 403; body %s", role.Code, role.Body)
	}

	// Wrong tenancy — must not confirm it exists, whatever the role.
	tenancy := request(t, server, http.MethodPut,
		"/packer/2023-01-01/organizations/"+otherOrg+"/projects/"+testProject+"/buckets",
		map[string]any{"name": "images"})
	if tenancy.Code != http.StatusNotFound {
		t.Fatalf("wrong tenancy = %d, want 404; body %s", tenancy.Code, tenancy.Body)
	}

	// Tenancy is checked FIRST: a caller outside the tenancy must not learn that
	// its role would also have been insufficient, since that confirms the
	// tenancy exists.
	if tenancy.Code == http.StatusForbidden {
		t.Fatal("a tenancy refusal leaked through as a role refusal")
	}
}

// Review finding 16: raw wrapped errors, including pgx messages carrying table,
// column and constraint names, were written into response bodies. An internal
// failure must not be chattier than a deliberate refusal (ADR-0017).
func TestInternalFailuresKeepTheirDetailServerSide(t *testing.T) {
	var logs bytes.Buffer
	repository := newFakeRepository()
	// A failure carrying exactly the kind of detail that must not escape.
	repository.listBucketsErr = errors.New(
		`ERROR: relation "buckets" violates constraint "buckets_organization_id_fkey" (SQLSTATE 23503)`)

	server := newHandler(repository, testPrincipals(), testAuthenticator{},
		slog.New(slog.NewJSONHandler(&logs, nil)), func() time.Time { return testTime })
	audited := &responseCorrelationWriter{}
	server = audit.NewHTTPHandler(audited, correlationResolver{}, server, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))

	response := request(t, server, http.MethodGet, testBase+"/buckets", nil)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", response.Code, response.Body)
	}

	body := response.Body.String()
	// Note "relation" is deliberately absent from this list: it is a substring of
	// "correlation id", which the body is supposed to contain.
	for _, leaked := range []string{"buckets_organization_id_fkey", "SQLSTATE", "violates constraint"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("response leaked %q: %s", leaked, body)
		}
	}
	// A correlation id is what makes the redaction diagnosable rather than
	// merely silent.
	if !strings.Contains(body, "correlation id") {
		t.Fatalf("response carries no correlation id: %s", body)
	}
	var status struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode internal error: %v", err)
	}
	correlation := strings.TrimPrefix(status.Message, "internal error; correlation id ")
	if correlation != audited.correlation {
		t.Fatalf("response correlation = %q, audited correlation = %q", correlation, audited.correlation)
	}
	if !strings.Contains(logs.String(), "buckets_organization_id_fkey") {
		t.Fatalf("the detail did not reach the log: %s", logs.String())
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

// Finding 15: createBucket reported every failure as AlreadyExists, which
// Packer's upsert TOLERATES — so a database outage was misclassified as
// success-adjacent at the opening step of every build.
func TestCreateBucketDistinguishesConflictFromFailure(t *testing.T) {
	repository := newFakeRepository()
	repository.createBucketErr = errors.New("connection refused")
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(),
		func() time.Time { return testTime })

	response := request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	if response.Code == http.StatusConflict {
		t.Fatal("an outage was reported as AlreadyExists, which Packer tolerates")
	}
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", response.Code, response.Body)
	}
}

// Every registered route is checked against an INDEPENDENT statement of what it
// should require.
//
// Review finding 18b: six write and promotion routes could be downgraded to
// reader with every suite still green. A first attempt at this test iterated
// the route table and asserted "the tier below is refused" — which reads the
// implementation back and skips entirely when a route is downgraded to reader,
// since nothing sits below it. It would not have caught the mutation it was
// written for.
//
// So the expectation is declared here, separately from the code under test. Two
// assertions follow: the table matches, and a route without an entry is a
// failure rather than a gap.
func TestEveryRouteRequiresTheRoleItShould(t *testing.T) {
	expected := map[string]identity.Role{
		"POST /_search/external_artifact":                                                   identity.RoleReader,
		"GET /registry":                                                                     identity.RoleReader,
		"GET /enforced_blocks/bucket/{bucket}":                                              identity.RoleReader,
		"GET /buckets":                                                                      identity.RoleReader,
		"GET /buckets/{bucket}":                                                             identity.RoleReader,
		"GET /buckets/{bucket}/ancestry":                                                    identity.RoleReader,
		"GET /buckets/{bucket}/channels":                                                    identity.RoleReader,
		"GET /buckets/{bucket}/channels/{channel}":                                          identity.RoleReader,
		"GET /buckets/{bucket}/channels/{channel}/history":                                  identity.RoleReader,
		"GET /buckets/{bucket}/packages/vulnerability-summary":                              identity.RoleReader,
		"GET /buckets/{bucket}/packages/with-vulnerabilities":                               identity.RoleReader,
		"GET /buckets/{bucket}/vulnerabilities":                                             identity.RoleReader,
		"GET /buckets/{bucket}/versions":                                                    identity.RoleReader,
		"GET /buckets/{bucket}/versions/{fingerprint}":                                      identity.RoleReader,
		"GET /buckets/{bucket}/versions/{fingerprint}/builds":                               identity.RoleReader,
		"GET /buckets/{bucket}/versions/{fingerprint}/builds/{build}/packages":              identity.RoleReader,
		"GET /buckets/{bucket}/versions/{fingerprint}/builds/{build}/sboms":                 identity.RoleReader,
		"GET /buckets/{bucket}/versions/{fingerprint}/builds/{build}/sboms/{sbom}":          identity.RoleReader,
		"GET /buckets/{bucket}/versions/{fingerprint}/builds/{build}/sboms/{sbom}/download": identity.RoleReader,

		// The push lifecycle: what a Packer principal holds, and no more.
		"PUT /buckets":                                                      identity.RoleBuilder,
		"PATCH /buckets/{bucket}":                                           identity.RoleBuilder,
		"POST /buckets/{bucket}/versions":                                   identity.RoleBuilder,
		"POST /buckets/{bucket}/versions/{fingerprint}/builds":              identity.RoleBuilder,
		"PATCH /buckets/{bucket}/versions/{fingerprint}/builds/{build}":     identity.RoleBuilder,
		"PUT /buckets/{bucket}/versions/{fingerprint}/builds/{build}/sboms": identity.RoleBuilder,

		// Promotion and destruction. A build credential must not reach these
		// (ADR-0019); bucket deletion sits with the same authority as channel
		// deletion, so one Terraform principal can destroy what it applied.
		"POST /buckets/{bucket}/channels":                                identity.RolePublisher,
		"POST /buckets/{bucket}/channels/assign":                         identity.RolePublisher,
		"PATCH /buckets/{bucket}/channels/{channel}":                     identity.RolePublisher,
		"DELETE /buckets/{bucket}/channels/{channel}":                    identity.RolePublisher,
		"DELETE /buckets/{bucket}":                                       identity.RolePublisher,
		"DELETE /buckets/{bucket}/versions/{fingerprint}":                identity.RolePublisher,
		"DELETE /buckets/{bucket}/versions/{fingerprint}/builds/{build}": identity.RolePublisher,
		// Revocation changes what consumers may use — the same authority that
		// blesses a version onto a channel, and never a build credential.
		"PATCH /buckets/{bucket}/versions/{fingerprint}": identity.RolePublisher,
	}

	below := map[identity.Role]identity.Role{
		identity.RoleBuilder:    identity.RoleReader,
		identity.RolePublisher:  identity.RoleBuilder,
		identity.RoleMaintainer: identity.RolePublisher,
		identity.RoleRoot:       identity.RoleMaintainer,
	}

	seen := make(map[string]bool, len(expected))
	for _, route := range routes() {
		key := route.method + " " + route.path
		seen[key] = true

		want, declared := expected[key]
		if !declared {
			t.Errorf("route %s has no expected role; add one rather than trusting the table", key)
			continue
		}
		if route.required != want {
			t.Errorf("route %s requires %s, want %s", key, route.required, want)
			continue
		}

		t.Run(key, func(t *testing.T) {
			path := testBase + strings.NewReplacer(
				"{bucket}", "images",
				"{channel}", "production",
				"{fingerprint}", "fp-1",
				"{build}", "build-1",
				"{sbom}", "manifest",
			).Replace(route.path)

			// The declared role must work, or the route is unusable by the role
			// it names.
			if permitted := roleRequest(t, fakePrincipals{role: want}, route.method, path); permitted.Code == http.StatusForbidden {
				t.Fatalf("a %s was refused though the route requires exactly that", want)
			}
			// And the tier below must not, where one exists.
			if lower, ok := below[want]; ok {
				if refused := roleRequest(t, fakePrincipals{role: lower}, route.method, path); refused.Code != http.StatusForbidden {
					t.Fatalf("a %s was not refused (status %d) though the route requires %s",
						lower, refused.Code, want)
				}
			}
		})
	}

	for key := range expected {
		if !seen[key] {
			t.Errorf("expected route %s is not registered; the table and the expectation have diverged", key)
		}
	}
}

// roleRequest issues a request as a principal holding one role. Only the
// authorization outcome is asserted, so no fixture setup is needed: refusal
// happens before the handler runs.
func roleRequest(t *testing.T, principals fakePrincipals, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	server := newHandler(newFakeRepository(), principals, testAuthenticator{}, testLogger(),
		func() time.Time { return testTime })
	return request(t, server, method, path, map[string]any{"name": "x", "fingerprint": "fp-1"})
}

// Revoking a credential stops the tokens minted from it, on the next request.
//
// Review finding 14. ADR-0019 resolves authority per request precisely so that
// revocation is immediate — but that only covered the ROLE. The credential was
// never rechecked, so revoking a leaked secret left every token minted from it
// working for a full TTL, which is the delay that decision exists to avoid.
//
// Asserted through the middleware rather than on the domain method, because the
// hole was that nothing between the token and the handler asked the question.
func TestRevokingACredentialStopsItsTokens(t *testing.T) {
	for _, c := range []struct {
		name     string
		secretID string
		want     int
	}{
		{"the credential that minted the token", testSecretID, http.StatusOK},
		{"a credential the principal no longer holds", "s-revoked", http.StatusNotFound},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The fixture principal holds testSecretID and nothing else, so a
			// token naming anything else is one whose credential is gone.
			server := newHandler(
				newFakeRepository(), fakePrincipals{role: identity.RoleReader},
				secretNamingAuthenticator{secretID: c.secretID},
				testLogger(), func() time.Time { return testTime },
			)
			w := request(t, server, http.MethodGet, testBase+"/buckets", nil)
			if w.Code != c.want {
				t.Fatalf("status = %d, want %d; body %s", w.Code, c.want, w.Body)
			}
		})
	}
}

// secretNamingAuthenticator mints a token naming whichever credential the case
// under test wants.
type secretNamingAuthenticator struct{ secretID string }

func (a secretNamingAuthenticator) Verify(token string) (identity.Verified, error) {
	if token != testToken {
		return identity.Verified{}, identity.ErrInvalid
	}
	return identity.Verified{
		PrincipalID: "p-test",
		Scope: identity.Scope{
			OrganizationID: uuid.MustParse(testOrg),
			ProjectID:      uuid.MustParse(testProject),
		},
		SecretID: a.secretID,
	}, nil
}
