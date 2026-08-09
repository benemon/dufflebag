package audit

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/google/uuid"
)

// SystemEvent is the semantic context shared by a scanner request/response
// pair. It uses the same record fields and schema version as the HTTP seam.
type SystemEvent struct {
	Operation      identity.AuditOperation
	TargetType     string
	TargetID       string
	Scope          identity.AuditScope
	OrganizationID string
	ProjectID      string
}

// SystemEmitter writes audit pairs for scanner work outside an HTTP request.
type SystemEmitter struct {
	writer Writer
	now    func() time.Time
}

func NewSystemEmitter(writer Writer) *SystemEmitter {
	return &SystemEmitter{writer: writer, now: time.Now}
}

// Request writes the fail-closed half of a pair and returns its correlation.
func (e *SystemEmitter) Request(event SystemEvent) (string, error) {
	correlationID := uuid.NewString()
	err := e.write(requestRecord{
		SchemaVersion: 2, Kind: eventKindRequest, CorrelationID: correlationID,
		OccurredAt: e.now().UTC(), Method: "SYSTEM", Path: "/scanner",
		IdentityKind: identity.IdentityKindSystem, Operation: event.Operation,
		TargetType: event.TargetType, TargetID: event.TargetID,
		PrincipalID: identity.SystemScannerPrincipalID, PrincipalName: identity.SystemScannerPrincipalID,
		Scope: event.Scope, OrganizationID: event.OrganizationID, ProjectID: event.ProjectID,
	})
	return correlationID, err
}

// Response writes the observed half after the scanner mutation has settled.
func (e *SystemEmitter) Response(
	correlationID string, event SystemEvent, outcome identity.AuditOutcome, reason string,
) error {
	status := http.StatusOK
	if outcome != identity.AuditOutcomeSuccess {
		status = http.StatusInternalServerError
	}
	return e.write(responseRecord{
		SchemaVersion: 2, Kind: eventKindResponse, CorrelationID: correlationID,
		OccurredAt: e.now().UTC(), RouteID: "system.scanner", Status: status,
		Outcome: outcome, Reason: reason, Operation: event.Operation,
		TargetType: event.TargetType, TargetID: event.TargetID,
		PrincipalID: identity.SystemScannerPrincipalID, PrincipalName: identity.SystemScannerPrincipalID,
		IdentityKind: identity.IdentityKindSystem, Scope: event.Scope,
		OrganizationID: event.OrganizationID, ProjectID: event.ProjectID,
	})
}

func (e *SystemEmitter) write(record any) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return e.writer.Write(append(encoded, '\n'))
}
