package rm2019

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const specPath = "../../../spec/vendor/cloud-resource-manager/2019-12-10/hcp.swagger.json"

var (
	specOnce sync.Once
	compiler *jsonschema.Compiler
	specErr  error
)

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
		specErr = compiler.AddResource("rm2019.json", doc)
	})
	if specErr != nil {
		t.Fatalf("load vendored spec: %v", specErr)
	}
	schema, err := compiler.Compile("rm2019.json#/definitions/" + definition)
	if err != nil {
		t.Fatalf("compile definition %q: %v", definition, err)
	}
	return schema
}

func assertConforms(t *testing.T, response *httptest.ResponseRecorder, definition string) {
	t.Helper()
	var body any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v; body %s", err, response.Body)
	}
	if err := definitionSchema(t, definition).Validate(body); err != nil {
		t.Fatalf("response does not conform to %s: %v; body %s", definition, err, response.Body)
	}
}

func TestProjectGetResponseConformsToVendoredSpec(t *testing.T) {
	response := get(t, newServer(orgScope()), basePath+"/projects/"+projectID, token)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", response.Code, response.Body)
	}
	assertConforms(t, response, "hashicorp.cloud.resourcemanager.ProjectGetResponse")
}

func TestProjectGetErrorConformsToVendoredSpec(t *testing.T) {
	response := get(t, newServer(projectScope()), basePath+"/projects/"+olderID, token)
	assertConforms(t, response, "google.rpc.Status")
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 3 || body["code"] != float64(5) {
		t.Fatalf("error body = %#v, want exactly status fields with code 5", body)
	}
}

func TestResourceManagerConformanceHarnessActuallyRejects(t *testing.T) {
	var body any
	if err := json.Unmarshal([]byte(`{"project":{"parent":"not-an-object"}}`), &body); err != nil {
		t.Fatal(err)
	}
	if err := definitionSchema(t, "hashicorp.cloud.resourcemanager.ProjectGetResponse").Validate(body); err == nil {
		t.Fatal("validator accepted a non-conforming project parent")
	}
}
