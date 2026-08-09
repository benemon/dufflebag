package hcp2023

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/compat/hcp2023/models"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Conformance: every response we emit must validate against the vendored HCP
// specification, not merely against our own expectations.
//
// This complements the behavioural tests and replaces neither. The spec
// describes structure, not semantics: it documents Version.name as "Human-
// readable name of the version" and contains zero occurrences of "v0",
// "Aborted" or "incomplete". A response can therefore be perfectly conformant
// and still abort a real packer build. See spec/vendor/PROVENANCE.md.

const specPath = "../../../spec/vendor/cloud-packer-service/2023-01-01/hcp.swagger.json"

var (
	specOnce sync.Once
	compiler *jsonschema.Compiler
	specErr  error
)

// definitionSchema compiles a validator for one Swagger 2.0 definition.
//
// Swagger 2.0 definitions are a draft-4 subset, so the document is registered
// whole and referenced by pointer — that way intra-document $refs between
// definitions resolve without rewriting anything.
func definitionSchema(t *testing.T, definition string) *jsonschema.Schema {
	t.Helper()

	specOnce.Do(func() {
		raw, err := os.ReadFile(filepath.Clean(specPath))
		if err != nil {
			specErr = err
			return
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			specErr = err
			return
		}
		compiler = jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft4)
		specErr = compiler.AddResource("hcp.json", doc)
	})
	if specErr != nil {
		t.Fatalf("load vendored spec: %v", specErr)
	}

	schema, err := compiler.Compile("hcp.json#/definitions/" + definition)
	if err != nil {
		t.Fatalf("compile definition %q: %v", definition, err)
	}
	return schema
}

// assertConforms validates a recorded response body against a named definition.
func assertConforms(t *testing.T, response *httptest.ResponseRecorder, definition string) {
	t.Helper()

	var body any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v\nbody: %s", err, response.Body.String())
	}
	if err := definitionSchema(t, definition).Validate(body); err != nil {
		t.Fatalf("response does not conform to %s:\n%v\nbody: %s",
			definition, err, response.Body.String())
	}
}

func TestResponsesConformToVendoredSpec(t *testing.T) {
	handler := NewHandlerWithRepository(newFakeRepository(), testPrincipals(), testAuthenticator{}, testLogger())
	const base = testBase

	// Seed enough state that the read responses are not trivially empty.
	request(t, handler, http.MethodPut, base+"/buckets",
		map[string]any{"name": "images", "description": "conformance"})
	request(t, handler, http.MethodPost, base+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp-1", "template_type": "HCL2"})
	seeded := request(t, handler, http.MethodPost, base+"/buckets/images/versions/fp-1/builds",
		map[string]any{"component_type": "docker.example", "packer_run_uuid": "run-1", "status": "BUILD_UNSET"})
	var build models.HashicorpCloudPacker20230101CreateBuildResponse
	decodeResponse(t, seeded, &build)

	// DeleteBucket last: it takes the seeded state with it.
	for _, c := range []struct {
		name       string
		method     string
		path       string
		body       any
		definition string
	}{
		{"GetBucket", http.MethodGet, base + "/buckets/images", nil,
			"hashicorp.cloud.packer_20230101.GetBucketResponse"},
		{"ListBuckets", http.MethodGet, base + "/buckets", nil,
			"hashicorp.cloud.packer_20230101.ListBucketsResponse"},
		{"GetVersion", http.MethodGet, base + "/buckets/images/versions/fp-1", nil,
			"hashicorp.cloud.packer_20230101.GetVersionResponse"},
		{"ListBuilds", http.MethodGet, base + "/buckets/images/versions/fp-1/builds", nil,
			"hashicorp.cloud.packer_20230101.ListBuildsResponse"},
		{"ListChannels", http.MethodGet, base + "/buckets/images/channels", nil,
			"hashicorp.cloud.packer_20230101.ListChannelsResponse"},
		{"UploadSbom", http.MethodPut,
			base + "/buckets/images/versions/fp-1/builds/" + build.Build.ID + "/sboms",
			map[string]any{"compressed_sbom": "enN0ZA==", "format": "CYCLONEDX"},
			"hashicorp.cloud.packer_20230101.UploadSbomResponse"},
		{"GetSbom", http.MethodGet,
			base + "/buckets/images/versions/fp-1/builds/" + build.Build.ID + "/sboms/fp-1",
			nil, "hashicorp.cloud.packer_20230101.GetSbomResponse"},
		// After the reads: the revoked version stays in seeded state until
		// DeleteBucket takes it.
		{"UpdateVersion", http.MethodPatch, base + "/buckets/images/versions/fp-1",
			map[string]any{"revoke_at": testTime.Format(time.RFC3339), "revocation_message": "conformance"},
			"hashicorp.cloud.packer_20230101.UpdateVersionResponse"},
		{"DeleteBucket", http.MethodDelete, base + "/buckets/images", nil,
			"hashicorp.cloud.packer_20230101.DeleteBucketResponse"},
	} {
		t.Run(c.name, func(t *testing.T) {
			response := request(t, handler, c.method, c.path, c.body)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", response.Code, response.Body.String())
			}
			assertConforms(t, response, c.definition)
		})
	}
}

// Error bodies are the highest-risk shape in the whole surface: Packer regex
// matches the error TEXT for a gRPC code, so a malformed or over-populated
// envelope breaks version creation in a way that looks like a server fault.
func TestErrorResponsesConformAndCarryExactlyOneCode(t *testing.T) {
	handler := NewHandlerWithRepository(newFakeRepository(), testPrincipals(), testAuthenticator{}, testLogger())
	const base = testBase

	for _, c := range []struct {
		name     string
		path     string
		wantCode string
	}{
		{"missing bucket answers 5", base + "/buckets/absent", `"code":5`},
		{"missing version answers 10", base + "/buckets/absent/versions/absent", `"code":10`},
		{"unserved path answers 12", base + "/unserved", `"code":12`},
	} {
		t.Run(c.name, func(t *testing.T) {
			response := request(t, handler, http.MethodGet, c.path, nil)
			assertConforms(t, response, "google.rpc.Status")

			body := response.Body.String()
			if !strings.Contains(body, c.wantCode) {
				t.Fatalf("body lacks %s: %s", c.wantCode, body)
			}
			// Packer's regex is [Cc]ode"?:([0-9]+) and takes the FIRST match, so
			// a second code-like field anywhere in the envelope is a live bug.
			if n := strings.Count(body, `"code"`); n != 1 {
				t.Fatalf(`body contains %d "code" fields, want exactly 1: %s`, n, body)
			}
		})
	}
}

// A validator that silently accepts everything is worse than none, because it
// reports green while proving nothing. This asserts the harness rejects.
func TestConformanceHarnessActuallyRejects(t *testing.T) {
	schema := definitionSchema(t, "google.rpc.Status")

	for _, c := range []struct {
		name string
		body string
	}{
		{"wrong type for code", `{"code":"not-a-number","message":"x"}`},
		{"array where object expected", `[]`},
		{"string where object expected", `"nope"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var body any
			if err := json.Unmarshal([]byte(c.body), &body); err != nil {
				t.Fatalf("bad fixture: %v", err)
			}
			if err := schema.Validate(body); err == nil {
				t.Fatalf("validator accepted a non-conforming body: %s", c.body)
			}
		})
	}
}
