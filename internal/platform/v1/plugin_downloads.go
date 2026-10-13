package v1

import (
	"context"
	"errors"

	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

func (s *server) GetPluginDownloads(
	ctx context.Context, request GetPluginDownloadsRequestObject,
) (GetPluginDownloadsResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()
	organizationID := request.OrganizationId.String()
	refused, err := s.admitPluginRead(ctx, audited, request.OrganizationId)
	if err != nil {
		return nil, err
	}
	if refused != permitted {
		return newRefusal(refused), nil
	}
	downloads, err := s.repository.PluginDownloads(
		ctx, store.ParseOrganizationTenant(organizationID), request.PluginName, s.now().UTC(),
	)
	if errors.Is(err, registry.ErrNotFound) {
		audited.failed("not_found")
		return GetPluginDownloads404JSONResponse{NotFoundJSONResponse: NotFoundJSONResponse{Message: "plugin not found"}}, nil
	}
	if errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		audited.failed("not_enabled")
		return GetPluginDownloads409JSONResponse{Message: "plugin registry is not enabled"}, nil
	}
	if err != nil {
		audited.failed("storage_failed")
		return nil, err
	}
	audited.succeeded(request.PluginName, "")
	response := GetPluginDownloads200JSONResponse{
		Today:       openapi_types.Date{Time: downloads.Today},
		AllVersions: renderPluginDownloadWindows(downloads.AllVersions),
		Versions:    make([]PluginVersionDownloads, 0, len(downloads.Versions)),
	}
	for _, version := range downloads.Versions {
		windows := renderPluginDownloadWindows(version.PluginDownloadWindows)
		response.Versions = append(response.Versions, PluginVersionDownloads{
			Version: version.Version, State: PluginVersionDownloadsState(version.State),
			Last7Days: windows.Last7Days, Last30Days: windows.Last30Days,
			Last365Days: windows.Last365Days, AllTime: windows.AllTime,
		})
	}
	return response, nil
}

func renderPluginDownloadWindows(windows store.PluginDownloadWindows) PluginDownloadWindows {
	return PluginDownloadWindows{
		Last7Days: windows.Last7Days, Last30Days: windows.Last30Days,
		Last365Days: windows.Last365Days, AllTime: windows.AllTime,
	}
}
