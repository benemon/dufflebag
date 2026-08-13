package webhook

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEnvelopeRendering(t *testing.T) {
	envelope := Envelope{
		EventID: "01K00000000000000000000000", OccurredAt: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC),
		OrganizationID: "11111111-1111-1111-1111-111111111111",
		ProjectID:      "22222222-2222-2222-2222-222222222222", Operation: OperationBucketCreated,
		Target: Target{Type: "bucket", Bucket: "base"}, Actor: Actor{PrincipalID: "principal", Name: "builder"},
		Payload: json.RawMessage(`{"bucket":{"name":"base"}}`),
	}
	got, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"event_id":"01K00000000000000000000000","occurred_at":"2026-08-13T12:00:00Z","organization_id":"11111111-1111-1111-1111-111111111111","project_id":"22222222-2222-2222-2222-222222222222","operation":"bucket.created","target":{"type":"bucket","bucket":"base"},"actor":{"principal_id":"principal","name":"builder"},"payload":{"bucket":{"name":"base"}}}`
	if string(got) != want {
		t.Fatalf("envelope = %s\nwant     = %s", got, want)
	}
}
