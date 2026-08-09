package audit

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/benemon/dufflebag/internal/domain/identity"
)

type systemTestWriter struct {
	records [][]byte
	err     error
}

func (w *systemTestWriter) Write(record []byte) error {
	if w.err != nil {
		return w.err
	}
	w.records = append(w.records, append([]byte(nil), record...))
	return nil
}

func TestSystemEmitterUsesHTTPAuditSchema(t *testing.T) {
	writer := &systemTestWriter{}
	emitter := NewSystemEmitter(writer)
	event := SystemEvent{
		Operation: identity.AuditOperationScanExecute, TargetType: "build", TargetID: "build-1",
		Scope: identity.AuditScopeProject, OrganizationID: "org-1", ProjectID: "project-1",
	}
	correlationID, err := emitter.Request(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := emitter.Response(correlationID, event, identity.AuditOutcomeSuccess, ""); err != nil {
		t.Fatal(err)
	}
	if len(writer.records) != 2 {
		t.Fatalf("records = %d, want request and response", len(writer.records))
	}
	for i, encoded := range writer.records {
		var record map[string]any
		if err := json.Unmarshal(encoded, &record); err != nil {
			t.Fatal(err)
		}
		if record["schema_version"] != float64(2) || record["principal_id"] != "system:scanner" ||
			record["identity_kind"] != "system" || record["scope"] != "project" ||
			record["organization_id"] != "org-1" || record["project_id"] != "project-1" ||
			record["operation"] != "scan.execute" || record["correlation_id"] != correlationID {
			t.Fatalf("record %d = %#v", i, record)
		}
	}
}

func TestSystemEmitterRequestFailureIsReturned(t *testing.T) {
	want := errors.New("sink down")
	emitter := NewSystemEmitter(&systemTestWriter{err: want})
	if _, err := emitter.Request(SystemEvent{}); !errors.Is(err, want) {
		t.Fatalf("request error = %v", err)
	}
}
