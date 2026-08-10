package contract_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestBagDropPlatformContractIsGeneratedAndSecretIsWriteOnly(t *testing.T) {
	specBytes, err := os.ReadFile("../spec/platform/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(specBytes)
	for _, required := range []string{
		"/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop:",
		"/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop/verify:",
		"/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop/enable:",
		"/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop/disable:",
		"operationId: getBagDropConfig", "operationId: putBagDropConfig",
		"operationId: deleteBagDropConfig", "operationId: verifyBagDrop",
		"operationId: enableBagDrop", "operationId: disableBagDrop",
		"enum: [hcp-packer]", "enum: [resolved, failed]",
		"enum: [credential_refused, project_not_found, unreachable, tls_failure]",
	} {
		if !strings.Contains(spec, required) {
			t.Errorf("platform spec lacks %q", required)
		}
	}

	generatedBytes, err := os.ReadFile("../internal/platform/v1/api.gen.go")
	if err != nil {
		t.Fatal(err)
	}
	generated := string(generatedBytes)
	parsed, err := parser.ParseFile(token.NewFileSet(), "api.gen.go", generatedBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	structFields := map[string]map[string]bool{}
	ast.Inspect(parsed, func(node ast.Node) bool {
		typeSpec, ok := node.(*ast.TypeSpec)
		if !ok {
			return true
		}
		structure, ok := typeSpec.Type.(*ast.StructType)
		if !ok {
			return true
		}
		fields := map[string]bool{}
		for _, field := range structure.Fields.List {
			for _, name := range field.Names {
				fields[name.Name] = true
			}
		}
		structFields[typeSpec.Name.Name] = fields
		return true
	})
	if !structFields["BagDropHCPPackerWrite"]["ClientSecret"] {
		t.Fatal("generated write shape has no ClientSecret")
	}
	for _, readType := range []string{"BagDropConfig", "BagDropHCPPacker", "BagDropLastVerification"} {
		if structFields[readType]["ClientSecret"] {
			t.Fatalf("generated read type %s exposes ClientSecret", readType)
		}
	}
	for _, response := range []string{
		"GetBagDropConfig200JSONResponse", "PutBagDropConfig200JSONResponse",
		"DeleteBagDropConfig409JSONResponse", "VerifyBagDrop200JSONResponse",
		"EnableBagDrop409JSONResponse", "DisableBagDrop200JSONResponse",
	} {
		if !strings.Contains(generated, "type "+response) {
			t.Errorf("generated contract lacks %s", response)
		}
	}
}
