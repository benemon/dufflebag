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
		"/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop/buckets:",
		"/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop/buckets/{bucketName}:",
		"/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop/status:",
		"/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop/reconcile:",
		"operationId: getBagDropConfig",
		"operationId: deleteBagDropConfig", "operationId: verifyBagDrop",
		"operationId: enableBagDrop", "operationId: disableBagDrop",
		"operationId: listBagDropAssociations", "operationId: setBagDropAssociation",
		"operationId: deleteBagDropAssociation", "operationId: getBagDropStatus",
		"operationId: reconcileBagDrop",
		"enum: [hcp-packer, dufflebag]", "enum: [resolved, failed]",
		"enum: [active, pending_removal]", "enum: [pending, synced, error, removing]",
		"enum: [credential_refused, project_not_found, unreachable, tls_failure]",
	} {
		if !strings.Contains(spec, required) {
			t.Errorf("platform spec lacks %q", required)
		}
	}
	if strings.Contains(spec, "operationId: putBagDropConfig") {
		t.Error("platform spec still exposes putBagDropConfig")
	}
	enableStart := strings.Index(spec, "operationId: enableBagDrop")
	enableEnd := strings.Index(spec[enableStart:], "/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop/disable:")
	enable := spec[enableStart : enableStart+enableEnd]
	if !strings.Contains(enable, "requestBody:") || !strings.Contains(enable, "required: false") ||
		!strings.Contains(enable, `$ref: "#/components/schemas/BagDropConfigWrite"`) {
		t.Error("enableBagDrop lacks the optional BagDropConfigWrite request body")
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
	for _, readType := range []string{
		"BagDropConfig", "BagDropHCPPacker", "BagDropLastVerification",
		"BagDropAssociation", "BagDropStatus",
	} {
		for _, secretField := range []string{"ClientSecret", "SealedSecret"} {
			if structFields[readType][secretField] {
				t.Fatalf("generated read type %s exposes %s", readType, secretField)
			}
		}
	}
	for _, field := range []string{"LastAttemptAt", "LastSyncError"} {
		if !structFields["BagDropAssociation"][field] {
			t.Errorf("generated BagDropAssociation lacks %s", field)
		}
	}
	for _, response := range []string{
		"GetBagDropConfig200JSONResponse", "EnableBagDrop200JSONResponse",
		"DeleteBagDropConfig409JSONResponse", "VerifyBagDrop200JSONResponse",
		"EnableBagDrop409JSONResponse", "DisableBagDrop200JSONResponse",
		"ListBagDropAssociations200JSONResponse", "SetBagDropAssociation200JSONResponse",
		"DeleteBagDropAssociation204Response", "GetBagDropStatus200JSONResponse",
		"ReconcileBagDrop202JSONResponse", "ReconcileBagDrop503JSONResponse",
	} {
		if !strings.Contains(generated, "type "+response) {
			t.Errorf("generated contract lacks %s", response)
		}
	}
}
