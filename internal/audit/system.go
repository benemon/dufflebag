package audit

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/google/uuid"
)

// SystemEvent is the semantic context shared by a system request/response
// pair. It uses the same record fields and schema version as the HTTP seam.
type SystemEvent struct {
	Operation                 identity.AuditOperation
	TargetType                string
	TargetID                  string
	Detail                    string
	Scope                     identity.AuditScope
	OrganizationID            string
	ProjectID                 string
	DestinationOrganizationID string
	DestinationProjectID      string
}

// SystemEmitter writes audit pairs for work outside an HTTP request.
type SystemEmitter struct {
	writer      Writer
	now         func() time.Time
	principalID string
	path        string
	routeID     string
}

func NewSystemEmitter(writer Writer) *SystemEmitter {
	return newSystemEmitter(writer, identity.SystemScannerPrincipalID, "/scanner", "system.scanner")
}

func NewBagDropEmitter(writer Writer) *SystemEmitter {
	return newSystemEmitter(writer, identity.SystemBagDropPrincipalID, "/bagdrop/reconcile", "system.bagdrop")
}

func newSystemEmitter(writer Writer, principalID, path, routeID string) *SystemEmitter {
	return &SystemEmitter{writer: writer, now: time.Now, principalID: principalID, path: path, routeID: routeID}
}

// Request writes the fail-closed half of a pair and returns its correlation.
func (e *SystemEmitter) Request(event SystemEvent) (string, error) {
	correlationID := uuid.NewString()
	err := e.write(requestRecord{
		SchemaVersion: 2, Kind: eventKindRequest, CorrelationID: correlationID,
		OccurredAt: e.now().UTC(), Method: "SYSTEM", Path: e.path,
		IdentityKind: identity.IdentityKindSystem, Operation: event.Operation,
		TargetType: event.TargetType, TargetID: event.TargetID, Detail: event.Detail,
		PrincipalID: e.principalID, PrincipalName: e.principalID,
		Scope: event.Scope, OrganizationID: event.OrganizationID, ProjectID: event.ProjectID,
		DestinationOrganizationID: event.DestinationOrganizationID,
		DestinationProjectID:      event.DestinationProjectID,
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
		OccurredAt: e.now().UTC(), RouteID: e.routeID, Status: status,
		Outcome: outcome, Reason: reason, Operation: event.Operation,
		TargetType: event.TargetType, TargetID: event.TargetID, Detail: event.Detail,
		PrincipalID: e.principalID, PrincipalName: e.principalID,
		IdentityKind: identity.IdentityKindSystem, Scope: event.Scope,
		OrganizationID: event.OrganizationID, ProjectID: event.ProjectID,
		DestinationOrganizationID: event.DestinationOrganizationID,
		DestinationProjectID:      event.DestinationProjectID,
	})
}

func (e *SystemEmitter) write(record any) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return e.writer.Write(append(encoded, '\n'))
}
