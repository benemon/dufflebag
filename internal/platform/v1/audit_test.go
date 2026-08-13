package v1

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/benemon/dufflebag/internal/audit"
)

func TestPlatformDescriptorKeysEqualGeneratedOperationSet(t *testing.T) {
	type semantic struct{ operation, targetType, targetParam string }
	want := map[string]semantic{
		"Initialize":               {"instance.initialize", "instance", ""},
		"Recover":                  {"instance.recover", "instance", ""},
		"Health":                   {"health.read", "instance", ""},
		"CreateSession":            {"session.create", "session", ""},
		"ReadSession":              {"session.read", "session", ""},
		"DeleteSession":            {"session.delete", "session", ""},
		"GetInstance":              {"instance.read", "instance", ""},
		"GetSelf":                  {"principal.self_read", "principal", ""},
		"ListAuditTargets":         {"audit_target.list", "audit_target_collection", ""},
		"CreateAuditTarget":        {"audit_target.create", "audit_target", ""},
		"DeleteAuditTarget":        {"audit_target.delete", "audit_target", "targetId"},
		"GetEncryption":            {"encryption.read", "keyring", ""},
		"RewrapEncryption":         {"encryption.rewrap", "keyring", ""},
		"RotateEncryption":         {"encryption.rotate", "keyring", ""},
		"ListOrganizations":        {"organization.list", "organization_collection", ""},
		"CreateOrganization":       {"organization.create", "organization", ""},
		"GetOrganization":          {"organization.read", "organization", "organizationId"},
		"DeleteOrganization":       {"organization.delete", "organization", "organizationId"},
		"ListProjects":             {"project.list", "project_collection", ""},
		"CreateProject":            {"project.create", "project", ""},
		"GetProject":               {"project.read", "project", "projectId"},
		"DeleteProject":            {"project.delete", "project", "projectId"},
		"ListPrincipals":           {"principal.list", "principal_collection", ""},
		"CreatePrincipal":          {"principal.create", "principal", ""},
		"GetPrincipal":             {"principal.read", "principal", "principalId"},
		"DeletePrincipal":          {"principal.delete", "principal", "principalId"},
		"CreatePrincipalSecret":    {"secret.issue", "secret", ""},
		"RevokePrincipalSecret":    {"secret.revoke", "secret", "secretId"},
		"GetScannerHealth":         {"scanner.health_read", "scanner", ""},
		"RescanBuild":              {"scan.request", "build", "buildId"},
		"ListPins":                 {"pin.list", "pin_collection", ""},
		"SetPin":                   {"pin.set", "pin", "bucketName"},
		"DeletePin":                {"pin.delete", "pin", "bucketName"},
		"GetBagDropConfig":         {"bagdrop.config.read", "bagdrop_config", ""},
		"DeleteBagDropConfig":      {"bagdrop.config.delete", "bagdrop_config", ""},
		"VerifyBagDrop":            {"bagdrop.verify", "bagdrop_config", ""},
		"EnableBagDrop":            {"bagdrop.enable", "bagdrop_config", ""},
		"DisableBagDrop":           {"bagdrop.disable", "bagdrop_config", ""},
		"ListBagDropAssociations":  {"bagdrop.association.list", "bagdrop_association_collection", ""},
		"SetBagDropAssociation":    {"bagdrop.association.set", "bagdrop_association", "bucketName"},
		"DeleteBagDropAssociation": {"bagdrop.association.delete", "bagdrop_association", "bucketName"},
		"GetBagDropStatus":         {"bagdrop.status.read", "bagdrop_status", ""},
		"ReconcileBagDrop":         {"bagdrop.reconcile", "bagdrop_config", ""},
		"ListWebhooks":             {"webhook.list", "webhook_collection", ""},
		"CreateWebhook":            {"webhook.create", "webhook", ""},
		"GetWebhook":               {"webhook.read", "webhook", "webhookId"},
		"UpdateWebhook":            {"webhook.update", "webhook", "webhookId"},
		"DeleteWebhook":            {"webhook.delete", "webhook", "webhookId"},
		"VerifyWebhook":            {"webhook.verify", "webhook", "webhookId"},
		"ListWebhookDeliveries":    {"webhook.delivery.list", "webhook", "webhookId"},
	}
	generated := generatedOperationIDs(t)
	described := make([]string, 0, len(operationDescriptors))
	for operationID, descriptor := range operationDescriptors {
		described = append(described, operationID)
		got := semantic{string(descriptor.Operation), descriptor.TargetType, descriptor.TargetIDParam}
		if got != want[operationID] {
			t.Errorf("descriptor %s semantic = %#v, want %#v", operationID, got, want[operationID])
		}
	}
	slices.Sort(generated)
	slices.Sort(described)
	if !slices.Equal(described, generated) {
		t.Fatalf("platform descriptor keys = %v, generated operation set = %v", described, generated)
	}
}

func TestPlatformDescriptorRoutesCrossCheckStrictOperationIDs(t *testing.T) {
	for expectedOperationID, descriptor := range operationDescriptors {
		t.Run(expectedOperationID, func(t *testing.T) {
			gotOperationID := ""
			strict := NewStrictHandler(&server{}, []StrictMiddlewareFunc{
				func(_ StrictHandlerFunc, operationID string) StrictHandlerFunc {
					return func(context.Context, http.ResponseWriter, *http.Request, any) (any, error) {
						gotOperationID = operationID
						return nil, nil
					}
				},
			})
			path := strings.NewReplacer(
				"{organizationId}", "11111111-2222-4333-8444-555555555555",
				"{projectId}", "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d",
				"{principalId}", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
				"{secretId}", "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff",
				"{bucketName}", "images",
				"{targetId}", "cccccccc-dddd-4eee-8fff-000000000000",
				"{webhookId}", "dddddddd-eeee-4fff-8000-111111111111",
			).Replace(descriptor.path)
			request := httptest.NewRequest(descriptor.method, path, strings.NewReader("{}"))
			request.Header.Set("Content-Type", "application/json")
			Handler(strict).ServeHTTP(httptest.NewRecorder(), request)
			if gotOperationID != expectedOperationID {
				t.Fatalf("descriptor %s %s reached strict operation %q, want %q", descriptor.method, descriptor.path, gotOperationID, expectedOperationID)
			}
		})
	}
}

func TestMalformedPlatformBodyUsesPreAuthDescriptor(t *testing.T) {
	handler := platformServer(testRoles{role: "root"})
	resolver := handler.(audit.Resolver)
	descriptor := resolver.Resolve(httptest.NewRequest(http.MethodPost, "/api/v1/organizations", nil))
	if descriptor.Operation != "organization.create" || descriptor.TargetType != "organization" {
		t.Fatalf("malformed request pre-auth descriptor = %#v", descriptor)
	}
}

func TestPlatformMethodMismatchStillHasATruthfulDescriptor(t *testing.T) {
	handler := platformServer(testRoles{role: "root"})
	descriptor := handler.(audit.Resolver).Resolve(
		httptest.NewRequest(http.MethodPatch, "/api/v1/organizations", nil),
	)
	if descriptor.Operation != "request.method_not_allowed" ||
		descriptor.HandlerlessReason != "method_not_allowed" || descriptor.TargetType != "request" {
		t.Fatalf("method-mismatch descriptor = %#v", descriptor)
	}
}

func generatedOperationIDs(t *testing.T) []string {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate audit test source")
	}
	generatedFile := filepath.Join(filepath.Dir(testFile), "api.gen.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), generatedFile, nil, 0)
	if err != nil {
		t.Fatalf("parse generated API: %v", err)
	}
	seen := map[string]struct{}{}
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		name, ok := call.Fun.(*ast.Ident)
		if !ok || name.Name != "middleware" {
			return true
		}
		literal, ok := call.Args[1].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		operationID, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Fatalf("unquote generated operation ID %s: %v", literal.Value, err)
		}
		seen[operationID] = struct{}{}
		return true
	})
	operations := make([]string, 0, len(seen))
	for operationID := range seen {
		operations = append(operations, operationID)
	}
	if len(operations) == 0 {
		t.Fatal("derived zero operation IDs from generated strict middleware")
	}
	return operations
}
