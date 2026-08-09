package v1

import (
	"context"
	"errors"

	"github.com/benemon/dufflebag/internal/domain/identity"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// Scanner is the scanner service as this plane needs it: remembered health,
// and a rescan request that enters the ordinary queue. A nil Scanner is a
// deployment with no adapter configured.
type Scanner interface {
	Health() store.ScannerHealth
	ManualRescan(ctx context.Context, tenant store.Tenant, buildID string) error
}

// scannerStates reports the coarse state /sys/health carries and the
// authenticated instance's configuration. The instance may name the adapter,
// but the endpoint remains root-only detail on /api/v1/scanner/health.
func (s *server) scannerStates() (HealthScanner, InstanceScanner) {
	if s.scanner == nil {
		return HealthScannerDisabled, InstanceScanner{Configured: false}
	}
	health := s.scanner.Health()
	adapter := health.Adapter
	return HealthScanner(health.State), InstanceScanner{
		Configured: true,
		Adapter:    &adapter,
	}
}

func (s *server) GetScannerHealth(
	ctx context.Context,
	_ GetScannerHealthRequestObject,
) (GetScannerHealthResponseObject, error) {
	if _, refused := authorizePlatform(ctx, identity.RoleRoot); refused != permitted {
		return newRefusal(refused), nil
	}
	if s.scanner == nil {
		return GetScannerHealth200JSONResponse{
			State: ScannerHealthStateDisabled,
		}, nil
	}
	health := s.scanner.Health()
	response := GetScannerHealth200JSONResponse{
		State:    ScannerHealthState(health.State),
		Adapter:  health.Adapter,
		Endpoint: health.Endpoint,
	}
	if !health.LastObservedAt.IsZero() {
		observed := health.LastObservedAt
		response.LastObservedAt = &observed
	}
	if health.Detail != "" {
		detail := health.Detail
		response.Detail = &detail
	}
	if !health.AuditCircuitOpenUntil.IsZero() {
		until := health.AuditCircuitOpenUntil
		response.AuditCircuitOpenUntil = &until
	}
	return response, nil
}

func (s *server) RescanBuild(
	ctx context.Context,
	request RescanBuildRequestObject,
) (RescanBuildResponseObject, error) {
	audited := s.beginLifecycleAudit()
	defer func() { audited.log(ctx) }()

	caller, refused := authorizeTenancy(ctx, identity.RoleMaintainer, request.OrganizationId.String(), request.ProjectId.String())
	if refused != permitted {
		audited.refused(refused.reason())
		return newRefusal(refused), nil
	}
	audited.actor(caller)

	if s.scanner == nil {
		audited.failed("scanner_unconfigured")
		return RescanBuild409JSONResponse{Message: "scanning is not configured on this deployment"}, nil
	}

	tenant := store.ParseTenant(request.OrganizationId.String(), request.ProjectId.String())
	err := s.scanner.ManualRescan(ctx, tenant, request.BuildId)
	switch {
	case errors.Is(err, store.ErrScanIneligible):
		// Absent and ineligible answer alike: which builds a channel selects
		// is information (ADR-0017).
		audited.refused("not_found")
		return RescanBuild404JSONResponse{Message: "no such build"}, nil
	case err != nil:
		s.logger.Error("manual rescan could not be queued", "error", err)
		audited.failed("queue_failed")
		return nil, err
	}
	audited.succeeded(request.BuildId, "queued")
	return RescanBuild202Response{}, nil
}
