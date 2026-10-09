package v1

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/pluginimport"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// PluginCatalogue is HashiCorp's release service, as the import screen reads it.
type PluginCatalogue interface {
	Plugins(ctx context.Context) ([]string, error)
	Versions(ctx context.Context, product, after string) ([]pluginimport.UpstreamVersion, string, error)
}

var platformPattern = regexp.MustCompile(`^[a-z0-9]+_[a-z0-9]+$`)

func validPlatforms(platforms []string) bool {
	seen := map[string]bool{}
	for _, platform := range platforms {
		if !platformPattern.MatchString(platform) || seen[platform] {
			return false
		}
		seen[platform] = true
	}
	return len(platforms) > 0 && len(platforms) <= 32
}

const upstreamUnavailable = "releases.hashicorp.com could not be reached"

func (s *server) GetPluginDefaultPlatforms(
	ctx context.Context, request GetPluginDefaultPlatformsRequestObject,
) (GetPluginDefaultPlatformsResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	refused, err := s.admitPluginRead(ctx, audited, request.OrganizationId)
	if err != nil {
		return nil, err
	}
	if refused != permitted {
		return newRefusal(refused), nil
	}
	platforms, err := s.repository.PluginRegistryDefaultPlatforms(ctx, store.ParseOrganizationTenant(request.OrganizationId.String()))
	if errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		audited.failed("not_enabled")
		return GetPluginDefaultPlatforms409JSONResponse{Message: "plugin registry is not enabled"}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(request.OrganizationId.String(), "")
	return GetPluginDefaultPlatforms200JSONResponse{Platforms: platforms}, nil
}

func (s *server) SetPluginDefaultPlatforms(
	ctx context.Context, request SetPluginDefaultPlatformsRequestObject,
) (SetPluginDefaultPlatformsResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID := request.OrganizationId.String()
	caller, refused, err := s.admitPluginRegistryMutation(ctx, identity.RoleMaintainer, organizationID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	if request.Body == nil || !validPlatforms(request.Body.Platforms) {
		audited.failed("invalid_request")
		return badRequestResponse{message: "platforms must be 1 to 32 distinct OS_ARCH values, for example linux_amd64"}, nil
	}
	platforms, err := s.repository.SetPluginRegistryDefaultPlatforms(ctx, store.ParseOrganizationTenant(organizationID), request.Body.Platforms)
	if errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		audited.failed("not_enabled")
		return SetPluginDefaultPlatforms409JSONResponse{Message: "plugin registry is not enabled"}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, "updated")
	return SetPluginDefaultPlatforms200JSONResponse{Platforms: platforms}, nil
}

func (s *server) ListHashicorpPlugins(
	ctx context.Context, request ListHashicorpPluginsRequestObject,
) (ListHashicorpPluginsResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID := request.OrganizationId.String()
	caller, refused, err := s.admitPluginRegistryMutation(ctx, identity.RolePublisher, organizationID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	products, err := s.catalogue.Plugins(ctx)
	if err != nil {
		audited.failed("upstream_unavailable")
		return ListHashicorpPlugins502JSONResponse{Message: upstreamUnavailable}, nil
	}
	held, err := s.repository.ListPlugins(ctx, store.ParseOrganizationTenant(organizationID))
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	byName := map[string]store.PluginSummary{}
	for _, summary := range held {
		byName[summary.Name] = summary
	}
	response := ListHashicorpPlugins200JSONResponse{Plugins: make([]HashicorpPlugin, 0, len(products))}
	for _, product := range products {
		rendered := HashicorpPlugin{Product: product, Name: strings.TrimPrefix(product, "packer-plugin-")}
		if summary, ok := byName[rendered.Name]; ok {
			if summary.Source == (store.PluginSource{Kind: "releases-hashicorp", Repository: product}) {
				rendered.MirroredVersions = len(summary.PublishedVersions)
			} else {
				source := renderPluginSource(summary.Source)
				rendered.HeldBy = &source
			}
		}
		response.Plugins = append(response.Plugins, rendered)
	}
	audited.succeeded(organizationID, "")
	return response, nil
}

func (s *server) ListHashicorpPluginVersions(
	ctx context.Context, request ListHashicorpPluginVersionsRequestObject,
) (ListHashicorpPluginVersionsResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID := request.OrganizationId.String()
	caller, refused, err := s.admitPluginRegistryMutation(ctx, identity.RolePublisher, organizationID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	after := ""
	if request.Params.After != nil {
		after = *request.Params.After
	}
	versions, next, err := s.catalogue.Versions(ctx, request.Product, after)
	if errors.Is(err, pluginimport.ErrUpstreamNotFound) {
		audited.failed("not_found")
		return ListHashicorpPluginVersions404JSONResponse{NotFoundJSONResponse: NotFoundJSONResponse{Message: "plugin not found on releases.hashicorp.com"}}, nil
	}
	if err != nil {
		audited.failed("upstream_unavailable")
		return ListHashicorpPluginVersions502JSONResponse{Message: upstreamUnavailable}, nil
	}
	_, mirroredVersions, err := s.repository.ListPluginVersions(ctx, store.ParseOrganizationTenant(organizationID), strings.TrimPrefix(request.Product, "packer-plugin-"))
	if err != nil && !errors.Is(err, registry.ErrNotFound) {
		audited.failed("storage_failed")
		return nil, err
	}
	mirrored := map[string]bool{}
	for _, version := range mirroredVersions {
		mirrored[version.Version] = true
	}
	response := ListHashicorpPluginVersions200JSONResponse{Versions: make([]HashicorpPluginVersion, 0, len(versions))}
	for _, version := range versions {
		rendered := HashicorpPluginVersion{
			Version: version.Version, CreatedAt: version.Created, Prerelease: version.Prerelease,
			Platforms: version.Platforms, Mirrored: mirrored[version.Version],
		}
		if version.State != "" {
			rendered.State = &version.State
		}
		if version.Changelog != "" {
			rendered.Changelog = &version.Changelog
		}
		response.Versions = append(response.Versions, rendered)
	}
	if next != "" {
		response.Next = &next
	}
	audited.succeeded(organizationID, "")
	return response, nil
}

func (s *server) CreatePluginImport(
	ctx context.Context, request CreatePluginImportRequestObject,
) (CreatePluginImportResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID := request.OrganizationId.String()
	caller, refused, err := s.admitPluginRegistryMutation(ctx, identity.RolePublisher, organizationID)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)
	body := request.Body
	if body == nil || !pluginimport.ValidProduct(body.Product) || len(body.Versions) == 0 || len(body.Versions) > 50 || !validPlatforms(body.Platforms) {
		audited.failed("invalid_request")
		return badRequestResponse{message: "an import needs a packer-plugin product, 1 to 50 versions and 1 to 32 distinct OS_ARCH platforms"}, nil
	}
	tenant := store.ParseOrganizationTenant(organizationID)
	id, err := s.repository.CreatePluginImport(ctx, tenant, store.PluginImportRequest{
		SourceKind: string(body.Source), Product: body.Product, Versions: body.Versions, Platforms: body.Platforms,
	})
	if errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		audited.failed("not_enabled")
		return CreatePluginImport409JSONResponse{Message: "plugin registry is not enabled"}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	job, err := s.repository.GetPluginImport(ctx, tenant, id)
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(organizationID, "queued")
	rendered, err := renderPluginImport(job)
	if err != nil {
		return nil, err
	}
	return CreatePluginImport202JSONResponse(rendered), nil
}

func (s *server) GetPluginImport(
	ctx context.Context, request GetPluginImportRequestObject,
) (GetPluginImportResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	refused, err := s.admitPluginRead(ctx, audited, request.OrganizationId)
	if err != nil {
		return nil, err
	}
	if refused != permitted {
		return newRefusal(refused), nil
	}
	job, err := s.repository.GetPluginImport(ctx, store.ParseOrganizationTenant(request.OrganizationId.String()), request.ImportId)
	if errors.Is(err, registry.ErrNotFound) {
		audited.failed("not_found")
		return GetPluginImport404JSONResponse{NotFoundJSONResponse: NotFoundJSONResponse{Message: "import not found"}}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(request.OrganizationId.String(), "")
	rendered, err := renderPluginImport(job)
	if err != nil {
		return nil, err
	}
	return GetPluginImport200JSONResponse(rendered), nil
}

func renderPluginImport(job store.PluginImport) (PluginImport, error) {
	rendered := PluginImport{
		Id: job.ID, Source: job.Request.SourceKind, Product: job.Request.Product,
		Versions: job.Request.Versions, Platforms: job.Request.Platforms,
		State: PluginImportState(job.State), CreatedAt: job.CreatedAt, FinishedAt: job.FinishedAt,
		Outcomes: []PluginImportVersionOutcome{},
	}
	if err := json.Unmarshal(job.Outcomes, &rendered.Outcomes); err != nil {
		return PluginImport{}, err
	}
	return rendered, nil
}
