package hcp2023

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// One of Packer's four resolution paths errors unless a non-nil registry comes
// back, so the shape matters as much as the status (ADR-0003).
func TestGetRegistryReturnsACoherentDocument(t *testing.T) {
	server := newHandler(newFakeRepository(), testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	response := request(t, server, http.MethodGet, testBase+"/registry", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", response.Code, response.Body)
	}

	var body struct {
		Registry *struct {
			ID       string `json:"id"`
			Location *struct {
				OrganizationID string `json:"organization_id"`
				ProjectID      string `json:"project_id"`
			} `json:"location"`
			CreatedAt string `json:"created_at"`
			Config    *struct {
				Activated   bool   `json:"activated"`
				FeatureTier string `json:"feature_tier"`
			} `json:"config"`
		} `json:"registry"`
	}
	decodeResponse(t, response, &body)

	if body.Registry == nil {
		t.Fatal("registry is null — the CLI treats this as a fatal error")
	}
	if body.Registry.Location == nil {
		t.Fatal("registry carries no location")
	}
	if body.Registry.Location.OrganizationID != testOrg || body.Registry.Location.ProjectID != testProject {
		t.Fatalf("location = %+v, want the requested tenant", body.Registry.Location)
	}
	if body.Registry.Config == nil || !body.Registry.Config.Activated {
		t.Fatalf("registry is not activated: %+v", body.Registry.Config)
	}
	if body.Registry.Config.FeatureTier == "" || body.Registry.Config.FeatureTier == "UNSET" {
		t.Fatalf("feature tier = %q, which gates features off", body.Registry.Config.FeatureTier)
	}
	if body.Registry.CreatedAt == "" {
		t.Fatal("registry has no created_at")
	}
}

// A registry belongs to the caller's own tenant, never to the one named in a
// path they are not entitled to.
func TestGetRegistryIsScopedToTheCaller(t *testing.T) {
	server := newHandler(newFakeRepository(), testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	response := request(t, server, http.MethodGet,
		"/packer/2023-01-01/organizations/"+otherOrg+"/projects/"+testProject+"/registry", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body %s", response.Code, response.Body)
	}
}

// Packer calls this from FetchEnforcedBlocks early in every build.
func TestEnforcedBlocksByBucketReturnsAnEmptyList(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	if response := request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{
		"name": "images",
	}); response.Code != http.StatusOK {
		t.Fatalf("create bucket: %d %s", response.Code, response.Body)
	}

	response := request(t, server, http.MethodGet, testBase+"/enforced_blocks/bucket/images", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", response.Code, response.Body)
	}

	// The field has no omitempty, so a nil slice marshals to null and a client
	// ranging over it without a nil check fails on a response meaning "nothing
	// to enforce". Assert the JSON, not just the decoded value.
	var raw map[string]json.RawMessage
	decodeResponse(t, response, &raw)
	detail, present := raw["enforced_block_detail"]
	if !present {
		t.Fatalf("no enforced_block_detail field: %s", response.Body)
	}
	if string(detail) != "[]" {
		t.Fatalf("enforced_block_detail = %s, want []", detail)
	}
}

func TestEnforcedBlocksForAnUnknownBucketIsNotFound(t *testing.T) {
	server := newHandler(newFakeRepository(), testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	response := request(t, server, http.MethodGet, testBase+"/enforced_blocks/bucket/absent", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — an unknown bucket must not report an empty block list", response.Code)
	}
}

func TestListVersionsReturnsTheBucketsVersions(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	if response := request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{
		"name": "images",
	}); response.Code != http.StatusOK {
		t.Fatalf("create bucket: %d %s", response.Code, response.Body)
	}
	for _, fingerprint := range []string{"fp-1", "fp-2"} {
		if response := request(t, server, http.MethodPost, testBase+"/buckets/images/versions", map[string]any{
			"fingerprint": fingerprint, "template_type": "HCL2",
		}); response.Code != http.StatusOK {
			t.Fatalf("create version %s: %d %s", fingerprint, response.Code, response.Body)
		}
	}

	response := request(t, server, http.MethodGet, testBase+"/buckets/images/versions", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", response.Code, response.Body)
	}
	var body struct {
		Versions []struct {
			Fingerprint string `json:"fingerprint"`
			Name        string `json:"name"`
		} `json:"versions"`
	}
	decodeResponse(t, response, &body)
	if len(body.Versions) != 2 {
		t.Fatalf("%d versions, want 2: %s", len(body.Versions), response.Body)
	}
}

// An empty bucket and a missing bucket are different claims, and only one of
// them means the name is wrong.
func TestListVersionsDistinguishesEmptyFromMissing(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	if response := request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{
		"name": "empty",
	}); response.Code != http.StatusOK {
		t.Fatalf("create bucket: %d %s", response.Code, response.Body)
	}

	empty := request(t, server, http.MethodGet, testBase+"/buckets/empty/versions", nil)
	if empty.Code != http.StatusOK {
		t.Fatalf("empty bucket status = %d, want 200", empty.Code)
	}
	var body map[string]json.RawMessage
	decodeResponse(t, empty, &body)
	if string(body["versions"]) != "[]" {
		t.Fatalf("versions = %s, want [] — nil marshals to null and breaks clients that range over it", body["versions"])
	}

	missing := request(t, server, http.MethodGet, testBase+"/buckets/absent/versions", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing bucket status = %d, want 404", missing.Code)
	}
}
