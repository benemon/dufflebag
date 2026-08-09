package v1

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/google/uuid"
)

func (s *server) ListAuditTargets(
	ctx context.Context,
	_ ListAuditTargetsRequestObject,
) (ListAuditTargetsResponseObject, error) {
	if _, refused := authorizePlatform(ctx, identity.RoleRoot); refused != permitted {
		return newRefusal(refused), nil
	}
	targets, err := s.auditTargets.ListAuditTargets(ctx)
	if err != nil {
		return nil, err
	}
	healthByID := make(map[string]audit.SinkHealth, len(targets))
	for _, health := range s.auditBroker.Health() {
		healthByID[health.ID] = health
	}
	response := ListAuditTargets200JSONResponse{Targets: make([]AuditTarget, 0, len(targets))}
	for _, target := range targets {
		health, active := healthByID[target.ID]
		if !active {
			// A peer may have persisted a target this process has not loaded yet.
			// Status is write health, not convergence state: it cannot accept this
			// process's writes, so "healthy" would be false.
			health = audit.SinkHealth{ID: target.ID, Status: audit.SinkStatusFailing}
		}
		rendered, err := renderAuditTarget(target, health)
		if err != nil {
			return nil, err
		}
		response.Targets = append(response.Targets, rendered)
	}
	return response, nil
}

func (s *server) CreateAuditTarget(
	ctx context.Context,
	request CreateAuditTargetRequestObject,
) (CreateAuditTargetResponseObject, error) {
	if _, refused := authorizePlatform(ctx, identity.RoleRoot); refused != permitted {
		return newRefusal(refused), nil
	}
	if request.Body == nil || request.Body.Path == "" {
		audit.FromContext(ctx).Enrich(audit.Enrichment{Reason: "invalid_audit_target"})
		return CreateAuditTarget400JSONResponse{Message: "audit target path is required"}, nil
	}

	sink, err := s.openAuditSink(request.Body.Path)
	if err != nil {
		audit.FromContext(ctx).Enrich(audit.Enrichment{Reason: "invalid_audit_target"})
		return CreateAuditTarget400JSONResponse{
			Message: "audit target path was refused",
			Reason:  auditTargetOpenReason(err),
		}, nil
	}

	s.configMu.Lock()
	unlockOnReturn := true
	defer func() {
		if unlockOnReturn {
			s.configMu.Unlock()
		}
	}()

	target, err := s.auditTargets.CreateAuditTarget(
		ctx, uuid.NewString(), request.Body.Path, s.now().UTC(),
	)
	if err != nil {
		_ = sink.Close(context.Background())
		if errors.Is(err, registry.ErrConflict) {
			audit.FromContext(ctx).Enrich(audit.Enrichment{Reason: "audit_target_limit"})
			return CreateAuditTarget409JSONResponse{Message: "three audit targets are already configured"}, nil
		}
		return nil, err
	}

	activate := func() {
		defer s.configMu.Unlock()
		if err := s.auditBroker.Add(audit.Target{ID: target.ID, Sink: sink}); err != nil {
			_ = sink.Close(context.Background())
			s.logger.Error("activate audit target", "target_id", target.ID, "error", err)
		}
	}
	unlockOnReturn = false
	if !audit.FromContext(ctx).AfterResponse(activate) {
		activate()
	}

	rendered, err := renderAuditTarget(target, audit.SinkHealth{
		ID: target.ID, Status: audit.SinkStatusHealthy,
	})
	if err != nil {
		return nil, err
	}
	return CreateAuditTarget201JSONResponse(rendered), nil
}

func auditTargetOpenReason(err error) AuditTargetOpenErrorReason {
	switch {
	case errors.Is(err, audit.ErrNotRegularFile):
		return NotARegularFile
	case errors.Is(err, os.ErrPermission):
		return PermissionDenied
	case errors.Is(err, syscall.ELOOP):
		return SymlinkRefused
	case errors.Is(err, audit.ErrWorldWritableParent):
		return WorldWritableParent
	default:
		return PathUnavailable
	}
}

func (s *server) DeleteAuditTarget(
	ctx context.Context,
	request DeleteAuditTargetRequestObject,
) (DeleteAuditTargetResponseObject, error) {
	if _, refused := authorizePlatform(ctx, identity.RoleRoot); refused != permitted {
		return newRefusal(refused), nil
	}

	s.configMu.Lock()
	unlockOnReturn := true
	defer func() {
		if unlockOnReturn {
			s.configMu.Unlock()
		}
	}()

	id := request.TargetId.String()
	if err := s.auditTargets.DeleteAuditTarget(ctx, id); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			audit.FromContext(ctx).Enrich(audit.Enrichment{Reason: "audit_target_not_found"})
			return DeleteAuditTarget404JSONResponse{
				NotFoundJSONResponse: NotFoundJSONResponse{Message: "audit target not found"},
			}, nil
		}
		return nil, err
	}

	activate := func() {
		defer s.configMu.Unlock()
		if err := s.auditBroker.Remove(id); err != nil {
			s.logger.Error("deactivate audit target", "target_id", id, "error", err)
		}
	}
	unlockOnReturn = false
	if !audit.FromContext(ctx).AfterResponse(activate) {
		activate()
	}
	return DeleteAuditTarget204Response{}, nil
}

func renderAuditTarget(target identity.AuditTarget, health audit.SinkHealth) (AuditTarget, error) {
	id, err := uuid.Parse(target.ID)
	if err != nil {
		return AuditTarget{}, err
	}
	var since, lastFailureAt, lastReopenedAt *time.Time
	if !health.Since.IsZero() {
		value := health.Since
		since = &value
	}
	if !health.LastFailureAt.IsZero() {
		value := health.LastFailureAt
		lastFailureAt = &value
	}
	if !health.LastReopenedAt.IsZero() {
		value := health.LastReopenedAt
		lastReopenedAt = &value
	}
	measurement := AuditTargetMeasurement{}
	if health.Measurement == nil {
		if err := measurement.FromAuditTargetMeasurementUnavailable(
			AuditTargetMeasurementUnavailable{State: Unavailable},
		); err != nil {
			return AuditTarget{}, err
		}
	} else if err := measurement.FromAuditTargetMeasurementAvailable(
		AuditTargetMeasurementAvailable{
			State: Available, CurrentFileSizeBytes: health.Measurement.CurrentFileSizeBytes,
			FilesystemFreeBytes: health.Measurement.FilesystemFreeBytes,
		},
	); err != nil {
		return AuditTarget{}, err
	}
	return AuditTarget{
		Id: id, Path: target.Path, CreatedAt: target.CreatedAt,
		Status: AuditTargetStatus(health.Status), Since: since,
		ConsecutiveFailures: health.ConsecutiveFailures,
		CumulativeFailures:  health.CumulativeFailures,
		LastFailureAt:       lastFailureAt, LastReopenedAt: lastReopenedAt,
		Measurement: measurement,
	}, nil
}
