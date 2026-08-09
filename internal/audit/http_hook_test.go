package audit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAfterResponseHookRunsExactlyOnce(t *testing.T) {
	handle := &Handle{correlationID: "hook-test"}
	invocations := 0
	if !handle.AfterResponse(func() { invocations++ }) {
		t.Fatal("composed handle refused an after-response hook")
	}

	handle.runAfterResponse()
	handle.runAfterResponse()
	if invocations != 1 {
		t.Fatalf("activation hook invocations = %d, want exactly 1", invocations)
	}
}

type rotatingKeyWriter struct {
	records []map[string]any
}

func (w *rotatingKeyWriter) Write(encoded []byte) error {
	var record map[string]any
	if err := json.Unmarshal(encoded, &record); err != nil {
		return err
	}
	w.records = append(w.records, record)
	return nil
}

type testResolver struct{}

func (testResolver) Resolve(*http.Request) Descriptor {
	return Descriptor{RouteID: "test", Operation: "test.read", TargetType: "test"}
}

func TestHTTPHandlerReadsHMACKeyForEachEntryWrite(t *testing.T) {
	writer := &rotatingKeyWriter{}
	version := "keyring-v1"
	handler := NewHTTPHandler(writer, testResolver{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		version = "keyring-v2"
		w.WriteHeader(http.StatusNoContent)
	}), func() (string, []byte) {
		return version, []byte("01234567890123456789012345678901")
	})
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if len(writer.records) != 2 {
		t.Fatalf("audit records = %d, want 2", len(writer.records))
	}
	if writer.records[0]["hmac_key_version"] != "keyring-v1" ||
		writer.records[1]["hmac_key_version"] != "keyring-v2" {
		t.Fatalf("HMAC versions = %v, %v", writer.records[0]["hmac_key_version"], writer.records[1]["hmac_key_version"])
	}
}
