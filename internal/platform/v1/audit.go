package v1

import (
	"net/http"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
)

type operationDescriptor struct {
	method string
	path   string
	audit.Descriptor
}

var sessionRenewDescriptor = audit.Descriptor{
	Operation:  identity.AuditOperationSessionRenew,
	TargetType: "session",
	TargetID:   SessionPath,
}

// operationDescriptors is keyed by the operation ID emitted by the generated
// strict middleware. The HTTP seam resolves from the method/path fields before
// authentication; the operation ID is only an independent test cross-check.
var operationDescriptors = map[string]operationDescriptor{
	"Initialize":               {method: http.MethodPost, path: "/sys/init", Descriptor: audit.Descriptor{Operation: identity.AuditOperationInstanceInitialize, TargetType: "instance", TargetID: "singleton"}},
	"Recover":                  {method: http.MethodPost, path: "/sys/recovery", Descriptor: audit.Descriptor{Operation: identity.AuditOperationInstanceRecover, TargetType: "instance", TargetID: "singleton"}},
	"Health":                   {method: http.MethodGet, path: "/sys/health", Descriptor: audit.Descriptor{Operation: "health.read", TargetType: "instance", TargetID: "singleton"}},
	"CreateSession":            {method: http.MethodPost, path: "/sys/session", Descriptor: audit.Descriptor{Operation: identity.AuditOperationSessionCreate, TargetType: "session", TargetID: SessionPath}},
	"ReadSession":              {method: http.MethodGet, path: "/sys/session", Descriptor: audit.Descriptor{Operation: "session.read", TargetType: "session", TargetID: SessionPath}},
	"DeleteSession":            {method: http.MethodDelete, path: "/sys/session", Descriptor: audit.Descriptor{Operation: identity.AuditOperationSessionDelete, TargetType: "session", TargetID: SessionPath}},
	"GetInstance":              {method: http.MethodGet, path: "/api/v1/instance", Descriptor: audit.Descriptor{Operation: "instance.read", TargetType: "instance", TargetID: "singleton"}},
	"GetSelf":                  {method: http.MethodGet, path: "/api/v1/self", Descriptor: audit.Descriptor{Operation: "principal.self_read", TargetType: "principal"}},
	"ListAuditTargets":         {method: http.MethodGet, path: "/api/v1/audit/targets", Descriptor: audit.Descriptor{Operation: "audit_target.list", TargetType: "audit_target_collection", SystemWhenDisabled: true}},
	"CreateAuditTarget":        {method: http.MethodPost, path: "/api/v1/audit/targets", Descriptor: audit.Descriptor{Operation: "audit_target.create", TargetType: "audit_target", SystemWhenDisabled: true}},
	"DeleteAuditTarget":        {method: http.MethodDelete, path: "/api/v1/audit/targets/{targetId}", Descriptor: audit.Descriptor{Operation: "audit_target.delete", TargetType: "audit_target", TargetIDParam: "targetId", SystemWhenDisabled: true}},
	"GetEncryption":            {method: http.MethodGet, path: "/api/v1/encryption", Descriptor: audit.Descriptor{Operation: "encryption.read", TargetType: "keyring", SystemWhenDisabled: true}},
	"RewrapEncryption":         {method: http.MethodPost, path: "/api/v1/encryption/rewrap", Descriptor: audit.Descriptor{Operation: "encryption.rewrap", TargetType: "keyring", SystemWhenDisabled: true}},
	"RotateEncryption":         {method: http.MethodPost, path: "/api/v1/encryption/rotate", Descriptor: audit.Descriptor{Operation: "encryption.rotate", TargetType: "keyring", SystemWhenDisabled: true}},
	"GetScannerHealth":         {method: http.MethodGet, path: "/api/v1/scanner/health", Descriptor: audit.Descriptor{Operation: "scanner.health_read", TargetType: "scanner", TargetID: "singleton"}},
	"RescanBuild":              {method: http.MethodPost, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/builds/{buildId}/rescan", Descriptor: audit.Descriptor{Operation: "scan.request", TargetType: "build", TargetIDParam: "buildId"}},
	"ListOrganizations":        {method: http.MethodGet, path: "/api/v1/organizations", Descriptor: audit.Descriptor{Operation: "organization.list", TargetType: "organization_collection"}},
	"CreateOrganization":       {method: http.MethodPost, path: "/api/v1/organizations", Descriptor: audit.Descriptor{Operation: "organization.create", TargetType: "organization"}},
	"GetOrganization":          {method: http.MethodGet, path: "/api/v1/organizations/{organizationId}", Descriptor: audit.Descriptor{Operation: "organization.read", TargetType: "organization", TargetIDParam: "organizationId"}},
	"DeleteOrganization":       {method: http.MethodDelete, path: "/api/v1/organizations/{organizationId}", Descriptor: audit.Descriptor{Operation: "organization.delete", TargetType: "organization", TargetIDParam: "organizationId"}},
	"ListProjects":             {method: http.MethodGet, path: "/api/v1/organizations/{organizationId}/projects", Descriptor: audit.Descriptor{Operation: identity.AuditOperationProjectList, TargetType: "project_collection"}},
	"CreateProject":            {method: http.MethodPost, path: "/api/v1/organizations/{organizationId}/projects", Descriptor: audit.Descriptor{Operation: "project.create", TargetType: "project"}},
	"GetProject":               {method: http.MethodGet, path: "/api/v1/organizations/{organizationId}/projects/{projectId}", Descriptor: audit.Descriptor{Operation: identity.AuditOperationProjectRead, TargetType: "project", TargetIDParam: "projectId"}},
	"DeleteProject":            {method: http.MethodDelete, path: "/api/v1/organizations/{organizationId}/projects/{projectId}", Descriptor: audit.Descriptor{Operation: "project.delete", TargetType: "project", TargetIDParam: "projectId"}},
	"ListPrincipals":           {method: http.MethodGet, path: "/api/v1/principals", Descriptor: audit.Descriptor{Operation: identity.AuditOperationPrincipalList, TargetType: "principal_collection"}},
	"CreatePrincipal":          {method: http.MethodPost, path: "/api/v1/principals", Descriptor: audit.Descriptor{Operation: identity.AuditOperationPrincipalCreate, TargetType: "principal"}},
	"GetPrincipal":             {method: http.MethodGet, path: "/api/v1/principals/{principalId}", Descriptor: audit.Descriptor{Operation: identity.AuditOperationPrincipalRead, TargetType: "principal", TargetIDParam: "principalId"}},
	"DeletePrincipal":          {method: http.MethodDelete, path: "/api/v1/principals/{principalId}", Descriptor: audit.Descriptor{Operation: identity.AuditOperationPrincipalDelete, TargetType: "principal", TargetIDParam: "principalId"}},
	"CreatePrincipalSecret":    {method: http.MethodPost, path: "/api/v1/principals/{principalId}/secrets", Descriptor: audit.Descriptor{Operation: identity.AuditOperationSecretIssue, TargetType: "secret"}},
	"RevokePrincipalSecret":    {method: http.MethodDelete, path: "/api/v1/principals/{principalId}/secrets/{secretId}", Descriptor: audit.Descriptor{Operation: identity.AuditOperationSecretRevoke, TargetType: "secret", TargetIDParam: "secretId"}},
	"ListPins":                 {method: http.MethodGet, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/pins", Descriptor: audit.Descriptor{Operation: "pin.list", TargetType: "pin_collection"}},
	"SetPin":                   {method: http.MethodPut, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/pins/{bucketName}", Descriptor: audit.Descriptor{Operation: "pin.set", TargetType: "pin", TargetIDParam: "bucketName"}},
	"DeletePin":                {method: http.MethodDelete, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/pins/{bucketName}", Descriptor: audit.Descriptor{Operation: "pin.delete", TargetType: "pin", TargetIDParam: "bucketName"}},
	"GetBagDropConfig":         {method: http.MethodGet, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop", Descriptor: audit.Descriptor{Operation: "bagdrop.config.read", TargetType: "bagdrop_config"}},
	"PutBagDropConfig":         {method: http.MethodPut, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop", Descriptor: audit.Descriptor{Operation: "bagdrop.config.write", TargetType: "bagdrop_config"}},
	"DeleteBagDropConfig":      {method: http.MethodDelete, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop", Descriptor: audit.Descriptor{Operation: "bagdrop.config.delete", TargetType: "bagdrop_config"}},
	"VerifyBagDrop":            {method: http.MethodPost, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop/verify", Descriptor: audit.Descriptor{Operation: "bagdrop.verify", TargetType: "bagdrop_config"}},
	"EnableBagDrop":            {method: http.MethodPost, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop/enable", Descriptor: audit.Descriptor{Operation: "bagdrop.enable", TargetType: "bagdrop_config"}},
	"DisableBagDrop":           {method: http.MethodPost, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop/disable", Descriptor: audit.Descriptor{Operation: "bagdrop.disable", TargetType: "bagdrop_config"}},
	"ListBagDropAssociations":  {method: http.MethodGet, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop/buckets", Descriptor: audit.Descriptor{Operation: "bagdrop.association.list", TargetType: "bagdrop_association_collection"}},
	"SetBagDropAssociation":    {method: http.MethodPut, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop/buckets/{bucketName}", Descriptor: audit.Descriptor{Operation: "bagdrop.association.set", TargetType: "bagdrop_association", TargetIDParam: "bucketName"}},
	"DeleteBagDropAssociation": {method: http.MethodDelete, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop/buckets/{bucketName}", Descriptor: audit.Descriptor{Operation: "bagdrop.association.delete", TargetType: "bagdrop_association", TargetIDParam: "bucketName"}},
	"GetBagDropStatus":         {method: http.MethodGet, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop/status", Descriptor: audit.Descriptor{Operation: "bagdrop.status.read", TargetType: "bagdrop_status"}},
	"ReconcileBagDrop":         {method: http.MethodPost, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/bagdrop/reconcile", Descriptor: audit.Descriptor{Operation: "bagdrop.reconcile", TargetType: "bagdrop_config"}},
	"ListWebhooks":             {method: http.MethodGet, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/webhooks", Descriptor: audit.Descriptor{Operation: "webhook.list", TargetType: "webhook_collection"}},
	"CreateWebhook":            {method: http.MethodPost, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/webhooks", Descriptor: audit.Descriptor{Operation: "webhook.create", TargetType: "webhook"}},
	"GetWebhook":               {method: http.MethodGet, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/webhooks/{webhookId}", Descriptor: audit.Descriptor{Operation: "webhook.read", TargetType: "webhook", TargetIDParam: "webhookId"}},
	"UpdateWebhook":            {method: http.MethodPatch, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/webhooks/{webhookId}", Descriptor: audit.Descriptor{Operation: "webhook.update", TargetType: "webhook", TargetIDParam: "webhookId"}},
	"DeleteWebhook":            {method: http.MethodDelete, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/webhooks/{webhookId}", Descriptor: audit.Descriptor{Operation: "webhook.delete", TargetType: "webhook", TargetIDParam: "webhookId"}},
	"VerifyWebhook":            {method: http.MethodPost, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/webhooks/{webhookId}/verify", Descriptor: audit.Descriptor{Operation: "webhook.verify", TargetType: "webhook", TargetIDParam: "webhookId"}},
	"ListWebhookDeliveries":    {method: http.MethodGet, path: "/api/v1/organizations/{organizationId}/projects/{projectId}/webhooks/{webhookId}/deliveries", Descriptor: audit.Descriptor{Operation: "webhook.delivery.list", TargetType: "webhook", TargetIDParam: "webhookId"}},
}

type resolvedHandler struct {
	http.Handler
	descriptors *http.ServeMux
	paths       *http.ServeMux
}

type describedOperation struct{ descriptor audit.Descriptor }

func (h describedOperation) ServeHTTP(http.ResponseWriter, *http.Request) {}

func withDescriptors(next http.Handler) *resolvedHandler {
	descriptors := http.NewServeMux()
	paths := http.NewServeMux()
	seenPaths := map[string]bool{}
	for operationID, described := range operationDescriptors {
		descriptor := described.Descriptor
		descriptor.RouteID = operationID
		descriptor.OperationID = operationID
		descriptors.Handle(described.method+" "+described.path, describedOperation{descriptor: descriptor})
		if !seenPaths[described.path] {
			paths.Handle(described.path, describedOperation{})
			seenPaths[described.path] = true
		}
	}
	return &resolvedHandler{Handler: next, descriptors: descriptors, paths: paths}
}

func (h *resolvedHandler) Resolve(r *http.Request) audit.Descriptor {
	handler, pattern := h.descriptors.Handler(r)
	if described, ok := handler.(describedOperation); ok {
		descriptor := described.descriptor
		if descriptor.TargetIDParam != "" {
			descriptor.TargetID = audit.PathValue(pattern, r.URL.Path, descriptor.TargetIDParam)
		}
		return descriptor
	}
	reason := "not_found"
	operation := identity.AuditOperation("request.not_found")
	if _, pattern := h.paths.Handler(r); pattern != "" {
		reason = "method_not_allowed"
		operation = "request.method_not_allowed"
	}
	return audit.Descriptor{
		RouteID: string(operation), Operation: operation, TargetType: "request",
		HandlerlessReason: reason,
	}
}
