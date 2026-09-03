// Package rm2019 serves the resource-manager endpoints the Packer CLI uses to
// resolve which organization and project it is working in.
//
// A third wire vocabulary over the same domain (ADR-0002). Unlike the packer
// endpoints these carry NO tenant in the path: the caller is asking which
// tenants it may see at all, so the answer comes from the principal resolved
// from storage for this request. That makes them the one place where scope is
// discovered rather than checked (ADR-0016, ADR-0019).
package rm2019

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/compat/rm2019/models"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/go-openapi/strfmt"
)

const basePath = "/resource-manager/2019-12-10"

// Repository reads the tenancy catalog.
//
// Listing is scoped at this layer rather than filtered by the handler, so a
// caller cannot see tenancies outside its scope by a handler forgetting to
// filter (ADR-0016). The listing takes the whole principal rather than a bare
// scope, because Scope's zero value is platform scope — the most privileged
// input — and a principal cannot be constructed holding it without root
// (duf-ueq).
type Repository interface {
	ListOrganizationsForPrincipal(context.Context, *identity.Principal) ([]store.Organization, error)
	ListProjects(context.Context, string) ([]store.Project, error)
	GetProject(context.Context, string, string) (*store.Project, error)
}

// Authenticator verifies a bearer token and reports who presented it.
type Authenticator interface {
	Verify(token string) (identity.Verified, error)
}

// Principals resolves an authenticated caller's authority, per request rather
// than from the token (ADR-0019).
type Principals interface {
	GetPrincipalByID(ctx context.Context, id string) (*identity.Principal, error)
}

type handler struct {
	repository Repository
	principals Principals
	logger     *slog.Logger
}

type route struct {
	method        string
	path          string
	required      identity.Role
	operation     identity.AuditOperation
	targetType    string
	targetIDParam string
	handle        func(*handler, http.ResponseWriter, *http.Request, *identity.Principal)
}

func routes() []route {
	return []route{
		{method: http.MethodGet, path: "/organizations", required: identity.RoleReader, operation: "organization.list", targetType: "organization_collection", handle: (*handler).listOrganizations},
		{method: http.MethodGet, path: "/projects", required: identity.RoleReader, operation: identity.AuditOperationProjectList, targetType: "project_collection", handle: (*handler).listProjects},
		{method: http.MethodGet, path: "/projects/{id}", required: identity.RoleReader, operation: identity.AuditOperationProjectRead, targetType: "project", targetIDParam: "id", handle: (*handler).getProject},
	}
}

type resolvedHandler struct {
	http.Handler
	descriptors *http.ServeMux
}

type describedRoute struct{ descriptor audit.Descriptor }

func (h describedRoute) ServeHTTP(http.ResponseWriter, *http.Request) {}

func (h *resolvedHandler) Resolve(r *http.Request) audit.Descriptor {
	handler, pattern := h.descriptors.Handler(r)
	if described, ok := handler.(describedRoute); ok {
		descriptor := described.descriptor
		if descriptor.TargetIDParam != "" {
			descriptor.TargetID = audit.PathValue(pattern, r.URL.Path, descriptor.TargetIDParam)
		}
		return descriptor
	}
	return audit.Descriptor{
		RouteID: "request.not_found", Operation: "request.not_found",
		TargetType: "request", HandlerlessReason: "not_found",
	}
}

// NewHandler serves the resource-manager compatibility endpoints.
func NewHandler(
	repository Repository, principals Principals, auth Authenticator, logger *slog.Logger,
) http.Handler {
	h := &handler{repository: repository, principals: principals, logger: logger}
	mux := http.NewServeMux()
	descriptors := http.NewServeMux()
	for _, route := range routes() {
		mux.HandleFunc(
			route.method+" "+basePath+route.path,
			h.authorized(route,
				func(w http.ResponseWriter, r *http.Request, principal *identity.Principal) {
					route.handle(h, w, r, principal)
				}),
		)
		descriptors.Handle(route.method+" "+basePath+route.path, describedRoute{descriptor: audit.Descriptor{
			RouteID: string(route.operation), Operation: route.operation,
			TargetType: route.targetType, TargetIDParam: route.targetIDParam,
		}})
	}
	return &resolvedHandler{Handler: authenticate(auth, mux), descriptors: descriptors}
}

// listOrganizations returns the caller's organization, and only ever that one.
//
// Returning exactly one satisfies a CLI contract for free: loadOrganizationID
// fails with "unexpected number of organizations: expected 1, actual: N" unless
// the list has a single entry. A principal is bound to one organization (ADR-0016).
func (h *handler) listOrganizations(
	w http.ResponseWriter, r *http.Request, principal *identity.Principal,
) {
	// Scoped by the RESOLVED principal, not by the token's claims. The repository
	// decides what a scope may see, so both planes answer the same question the
	// same way.
	organizations, err := h.repository.ListOrganizationsForPrincipal(r.Context(), principal)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			writeJSON(w, http.StatusOK, models.HashicorpCloudResourcemanagerOrganizationListResponse{
				Organizations: []*models.HashicorpCloudResourcemanagerOrganization{},
			})
			return
		}
		writeInternal(w, r, h.logger, err)
		return
	}

	rendered := make([]*models.HashicorpCloudResourcemanagerOrganization, 0, len(organizations))
	for _, organization := range organizations {
		rendered = append(rendered, &models.HashicorpCloudResourcemanagerOrganization{
			ID:        organization.ID,
			Name:      organization.Name,
			CreatedAt: strfmt.DateTime(organization.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, models.HashicorpCloudResourcemanagerOrganizationListResponse{
		Organizations: rendered,
	})
}

// listProjects returns the projects the caller may see, or refuses.
//
// The refusal is load-bearing rather than incidental. Upstream, a project-level
// service principal cannot list an organization's projects, and the CLI turns
// that 403 into "try setting HCP_PROJECT_ID". Answering with the one project
// instead would look more helpful and would make that documented path
// unreachable, so a project-scoped principal is refused here exactly as it is
// upstream (ADR-0016).
func (h *handler) listProjects(
	w http.ResponseWriter, r *http.Request, principal *identity.Principal,
) {
	if !principal.Scope.OrganizationScoped() {
		audit.FromContext(r.Context()).Enrich(audit.Enrichment{
			PrincipalID:    principal.ID,
			PrincipalName:  principal.Name,
			IdentityKind:   identity.IdentityKindServicePrincipal,
			Scope:          identity.AuditScopeProject,
			OrganizationID: principal.Scope.OrganizationID.String(),
			ProjectID:      principal.Scope.ProjectID.String(),
			Reason:         "project_scoped_principal",
		})
		writeError(w, http.StatusForbidden, 7, "the principal may not list projects in this organization")
		return
	}

	// A scope filter naming a different organization is not an error: it is a
	// request for something outside the caller's scope, and the answer to that is
	// nothing rather than a hint that the organization exists.
	if scope := r.URL.Query().Get("scope.id"); scope != "" && scope != principal.Scope.OrganizationID.String() {
		writeJSON(w, http.StatusOK, models.HashicorpCloudResourcemanagerProjectListResponse{
			Projects: []*models.HashicorpCloudResourcemanagerProject{},
		})
		return
	}

	projects, err := h.repository.ListProjects(r.Context(), principal.Scope.OrganizationID.String())
	if err != nil {
		writeInternal(w, r, h.logger, err)
		return
	}

	rendered := make([]*models.HashicorpCloudResourcemanagerProject, 0, len(projects))
	for _, project := range projects {
		// created_at is load-bearing: an unpinned CLI selects the OLDEST project
		// when several exist, so this timestamp decides default tenant selection
		// (ADR-0003).
		rendered = append(rendered, renderProject(project))
	}
	writeJSON(w, http.StatusOK, models.HashicorpCloudResourcemanagerProjectListResponse{
		Projects: rendered,
	})
}

// getProject returns one project only when it belongs to the resolved
// principal's binding. The path carries no organization, so that organization
// comes from principal.Scope in storage, never from token claims (ADR-0019).
// A foreign project is concealed as ordinary not-found, matching the scope
// rule used by the sibling read plane (ADR-0016).
func (h *handler) getProject(
	w http.ResponseWriter, r *http.Request, principal *identity.Principal,
) {
	projectID := r.PathValue("id")
	if principal.Scope.PlatformScoped() ||
		(!principal.Scope.OrganizationScoped() && principal.Scope.ProjectID.String() != projectID) {
		h.auditProjectRefusal(r, principal, projectID, "outside_principal_scope")
		writeError(w, http.StatusNotFound, 5, "project not found")
		return
	}

	project, err := h.repository.GetProject(
		r.Context(), principal.Scope.OrganizationID.String(), projectID,
	)
	if errors.Is(err, registry.ErrNotFound) {
		writeError(w, http.StatusNotFound, 5, "project not found")
		return
	}
	if err != nil {
		writeInternal(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, models.HashicorpCloudResourcemanagerProjectGetResponse{
		Project: renderProject(*project),
	})
}

func renderProject(project store.Project) *models.HashicorpCloudResourcemanagerProject {
	return &models.HashicorpCloudResourcemanagerProject{
		ID:        project.ID,
		Name:      project.Name,
		CreatedAt: strfmt.DateTime(project.CreatedAt),
		Parent: &models.HashicorpCloudResourcemanagerResourceID{
			ID:   project.OrganizationID,
			Type: resourceTypeOrganization(),
		},
	}
}

func (h *handler) auditProjectRefusal(
	r *http.Request, principal *identity.Principal, projectID, reason string,
) {
	audit.FromContext(r.Context()).Enrich(audit.Enrichment{
		PrincipalID:    principal.ID,
		PrincipalName:  principal.Name,
		IdentityKind:   identity.IdentityKindServicePrincipal,
		Scope:          identity.AuditScopeProject,
		OrganizationID: principal.Scope.OrganizationID.String(),
		ProjectID:      projectID,
		Reason:         reason,
	})
}

func resourceTypeOrganization() *models.HashicorpCloudResourcemanagerResourceIDResourceType {
	organization := models.HashicorpCloudResourcemanagerResourceIDResourceTypeORGANIZATION
	return &organization
}

func verifiedFrom(r *http.Request) (identity.Verified, bool) {
	verified, ok := r.Context().Value(verifiedKey{}).(identity.Verified)
	return verified, ok
}

func trimBearer(header string) (string, bool) {
	if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
		return "", false
	}
	token := strings.TrimSpace(header[7:])
	return token, token != ""
}

// authorized resolves the caller and enforces the route table's required role.
// A principal below reader may not enumerate tenancies (ADR-0019).
func (h *handler) authorized(
	route route,
	next func(http.ResponseWriter, *http.Request, *identity.Principal),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		verified, ok := verifiedFrom(r)
		if !ok {
			unauthenticated(w)
			return
		}
		principal, err := h.principals.GetPrincipalByID(r.Context(), verified.PrincipalID)
		// A token whose minting credential has been revoked is refused now, not at
		// expiry (review finding 14).
		revoked := err == nil && !principal.HasActiveSecret(verified.SecretID, time.Now().UTC())
		if err != nil || revoked || !principal.Role.AtLeast(route.required) {
			reason := "insufficient_role"
			switch {
			case err != nil:
				reason = "principal_unresolvable"
			case revoked:
				reason = "credential_revoked"
			}
			event := audit.Enrichment{
				// The token's subject, because the principal may not have resolved.
				PrincipalID:  verified.PrincipalID,
				IdentityKind: identity.IdentityKindServicePrincipal,
				Scope:        identity.AuditScopePlatform,
				Reason:       reason,
			}
			if err == nil {
				event.PrincipalName = principal.Name
			}
			audit.FromContext(r.Context()).Enrich(event)
			writeError(w, http.StatusForbidden, 7, "the principal may not enumerate tenancies")
			return
		}
		scope := identity.AuditScopePlatform
		organizationID, projectID := "", ""
		if !principal.Scope.PlatformScoped() {
			scope = identity.AuditScopeOrganization
			organizationID = principal.Scope.OrganizationID.String()
			if !principal.Scope.OrganizationScoped() {
				scope = identity.AuditScopeProject
				projectID = principal.Scope.ProjectID.String()
			}
		}
		audit.FromContext(r.Context()).Enrich(audit.Enrichment{
			PrincipalID: principal.ID, PrincipalName: principal.Name,
			IdentityKind: identity.IdentityKindServicePrincipal,
			Scope:        scope, OrganizationID: organizationID, ProjectID: projectID,
		})
		next(w, r, principal)
	}
}
