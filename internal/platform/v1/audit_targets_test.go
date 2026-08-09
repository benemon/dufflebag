package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/google/uuid"
)

type fakeAuditTargetRepository struct {
	mu           sync.Mutex
	targets      []identity.AuditTarget
	createCalls  int
	deleteCalls  int
	createCalled chan int
}

func (r *fakeAuditTargetRepository) ListAuditTargets(context.Context) ([]identity.AuditTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]identity.AuditTarget(nil), r.targets...), nil
}

func (r *fakeAuditTargetRepository) CreateAuditTarget(
	_ context.Context, id, path string, createdAt time.Time,
) (identity.AuditTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createCalls++
	if r.createCalled != nil {
		r.createCalled <- r.createCalls
	}
	if len(r.targets) == 3 {
		return identity.AuditTarget{}, registry.ErrConflict
	}
	target := identity.AuditTarget{ID: id, Path: path, CreatedAt: createdAt}
	r.targets = append(r.targets, target)
	return target, nil
}

func (r *fakeAuditTargetRepository) DeleteAuditTarget(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleteCalls++
	for i, target := range r.targets {
		if target.ID == id {
			r.targets = append(r.targets[:i:i], r.targets[i+1:]...)
			return nil
		}
	}
	return registry.ErrNotFound
}

func (r *fakeAuditTargetRepository) calls() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.createCalls, r.deleteCalls
}

type spyAuditBroker struct {
	mu          sync.Mutex
	addCalls    int
	removeCalls int
	panicAdd    bool
	targets     []audit.Target
	health      []audit.SinkHealth
}

func (b *spyAuditBroker) Add(target audit.Target) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.addCalls++
	if b.panicAdd {
		panic("activation panic")
	}
	b.targets = append(b.targets, target)
	return nil
}

func (b *spyAuditBroker) Remove(string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removeCalls++
	return nil
}

func (b *spyAuditBroker) Health() []audit.SinkHealth {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]audit.SinkHealth(nil), b.health...)
}

func (b *spyAuditBroker) counts() (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.addCalls, b.removeCalls
}

func (b *spyAuditBroker) closeTargets() {
	b.mu.Lock()
	targets := append([]audit.Target(nil), b.targets...)
	b.mu.Unlock()
	for _, target := range targets {
		_ = target.Sink.Close(context.Background())
	}
}

func newAuditConfigHandler(
	t *testing.T, role identity.Role, targets AuditTargetRepository, broker AuditTargetBroker,
) http.Handler {
	t.Helper()
	scope := identity.Scope{}
	if role != identity.RoleRoot {
		scope.OrganizationID = uuid.MustParse(testOrgID)
	}
	return newHandlerWithAudit(
		&fakeTenancyRepository{}, &fakeInstanceRepository{}, testAuth{},
		testRoles{role: role, scope: scope}, testLogger(), targets, broker,
		func() time.Time { return initTestTime },
	)
}

func withAuditWriter(t *testing.T, handler http.Handler, writer audit.Writer) http.Handler {
	t.Helper()
	resolver, ok := handler.(audit.Resolver)
	if !ok {
		t.Fatalf("platform handler %T has no audit resolver", handler)
	}
	return audit.NewHTTPHandler(writer, resolver, handler, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))
}

func decodeAuditLines(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode audit record %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func TestAuditTargetConfigurationRequiresRootAndAuditsRefusal(t *testing.T) {
	repository := &fakeAuditTargetRepository{}
	broker := &spyAuditBroker{}
	handler := newAuditConfigHandler(t, identity.RoleMaintainer, repository, broker)
	trail := &platformAuditTrail{}
	handler = withAuditWriter(t, handler, trail)
	server := httptest.NewServer(handler)
	defer server.Close()
	client, err := NewClientWithResponses(server.URL, WithRequestEditorFn(
		func(_ context.Context, request *http.Request) error {
			request.Header.Set("Authorization", "Bearer "+testToken)
			return nil
		},
	))
	if err != nil {
		t.Fatalf("new generated platform client: %v", err)
	}
	targetID := uuid.MustParse("11111111-2222-4333-8444-555555555555")
	requests := []struct {
		name string
		call func() (int, error)
	}{
		{"list", func() (int, error) {
			response, err := client.ListAuditTargetsWithResponse(context.Background())
			if err != nil {
				return 0, err
			}
			return response.StatusCode(), nil
		}},
		{"create", func() (int, error) {
			response, err := client.CreateAuditTargetWithResponse(
				context.Background(), AuditTargetCreate{Path: filepath.Join(t.TempDir(), "audit.log")},
			)
			if err != nil {
				return 0, err
			}
			return response.StatusCode(), nil
		}},
		{"delete", func() (int, error) {
			response, err := client.DeleteAuditTargetWithResponse(context.Background(), targetID)
			if err != nil {
				return 0, err
			}
			return response.StatusCode(), nil
		}},
	}
	for _, request := range requests {
		status, err := request.call()
		if err != nil {
			t.Fatalf("generated client %s: %v", request.name, err)
		}
		if status != http.StatusForbidden {
			t.Fatalf("non-root generated client %s = %d, want 403", request.name, status)
		}
		record := trail.response(t)
		if record["principal_id"] != testPrincID || record["identity_kind"] != "service_principal" ||
			record["scope"] != "platform" || record["outcome"] != "refused" || record["reason"] != "role_refused" {
			t.Fatalf("non-root config refusal audit = %#v", record)
		}
	}
	if creates, deletes := repository.calls(); creates != 0 || deletes != 0 {
		t.Fatalf("non-root config reached persistence: creates=%d deletes=%d", creates, deletes)
	}
}

func TestAuditTargetOpenFailuresReturnSafeReasonCategories(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want AuditTargetOpenErrorReason
	}{
		{"not a regular file", audit.ErrNotRegularFile, NotARegularFile},
		{"permission denied", &os.PathError{Op: "open", Path: "/secret", Err: os.ErrPermission}, PermissionDenied},
		{"symlink refused", &os.PathError{Op: "open", Path: "/link", Err: syscall.ELOOP}, SymlinkRefused},
		{"world-writable parent", audit.ErrWorldWritableParent, WorldWritableParent},
		{"unavailable path", &os.PathError{Op: "stat", Path: "/missing", Err: os.ErrNotExist}, PathUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := auditTargetOpenReason(test.err); got != test.want {
				t.Fatalf("reason = %q, want %q", got, test.want)
			}
		})
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "target.log")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatalf("create symlink target: %v", err)
	}
	link := filepath.Join(dir, "audit.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create audit symlink: %v", err)
	}
	handler := newAuditConfigHandler(
		t, identity.RoleRoot, &fakeAuditTargetRepository{}, &spyAuditBroker{},
	)
	response := call(t, handler, http.MethodPost, "/api/v1/audit/targets", map[string]string{"path": link}, testToken)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("symlink target = %d, want 400; body %s", response.Code, response.Body)
	}
	var refusal AuditTargetOpenError
	if err := json.Unmarshal(response.Body.Bytes(), &refusal); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if refusal.Reason != SymlinkRefused || refusal.Message != "audit target path was refused" {
		t.Fatalf("symlink refusal = %#v", refusal)
	}
	if strings.Contains(response.Body.String(), link) {
		t.Fatalf("symlink refusal leaked its path: %s", response.Body)
	}
}

func TestDeleteAuditTargetActivatesAfterItsResponseEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	sink, err := audit.NewFileSink(path, testLogger())
	if err != nil {
		t.Fatalf("open audit file: %v", err)
	}
	targetID := "11111111-2222-4333-8444-555555555555"
	broker, err := audit.NewBroker(testLogger(), audit.Target{ID: targetID, Sink: sink})
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	repository := &fakeAuditTargetRepository{targets: []identity.AuditTarget{{
		ID: targetID, Path: path, CreatedAt: initTestTime,
	}}}
	handler := newAuditConfigHandler(t, identity.RoleRoot, repository, broker)
	handler = withAuditWriter(t, handler, broker)

	response := call(t, handler, http.MethodDelete, "/api/v1/audit/targets/"+targetID, nil, testToken)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete target = %d, want 204; body %s", response.Code, response.Body)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read disabled target: %v", err)
	}
	records := decodeAuditLines(t, raw)
	if len(records) != 2 || records[0]["kind"] != "request" || records[1]["kind"] != "response" {
		t.Fatalf("disabled target records = %#v, want the triggering request pair", records)
	}
	if records[1]["operation"] != "audit_target.delete" || records[1]["target_id"] != targetID {
		t.Fatalf("disabled target response record = %#v", records[1])
	}
}

type responseFailWriter struct {
	mu            sync.Mutex
	failResponse  bool
	panicResponse bool
}

func (w *responseFailWriter) Write(record []byte) error {
	var decoded map[string]any
	_ = json.Unmarshal(record, &decoded)
	w.mu.Lock()
	defer w.mu.Unlock()
	if decoded["kind"] == "response" {
		if w.panicResponse {
			panic("response audit panic")
		}
		if w.failResponse {
			w.failResponse = false
			return errors.New("forced response audit failure")
		}
	}
	return nil
}

func TestResponseWriteFailureStillActivatesExactlyOnceAndReleasesConfigLock(t *testing.T) {
	repository := &fakeAuditTargetRepository{}
	broker := &spyAuditBroker{}
	defer broker.closeTargets()
	writer := &responseFailWriter{failResponse: true}
	handler := withAuditWriter(t, newAuditConfigHandler(t, identity.RoleRoot, repository, broker), writer)

	first := call(t, handler, http.MethodPost, "/api/v1/audit/targets",
		map[string]string{"path": filepath.Join(t.TempDir(), "first.log")}, testToken)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create with failed response audit = %d, want 201; body %s", first.Code, first.Body)
	}
	if adds, _ := broker.counts(); adds != 1 {
		t.Fatalf("activation calls after failed response write = %d, want exactly 1", adds)
	}

	second := call(t, handler, http.MethodPost, "/api/v1/audit/targets",
		map[string]string{"path": filepath.Join(t.TempDir(), "second.log")}, testToken)
	if second.Code != http.StatusCreated {
		t.Fatalf("later config request after failed response audit = %d, want 201; body %s", second.Code, second.Body)
	}
	if adds, _ := broker.counts(); adds != 2 {
		t.Fatalf("activation calls after two creates = %d, want 2", adds)
	}
}

type configBarrierWriter struct {
	mu             sync.Mutex
	requests       int
	blockFirstOnce sync.Once
	firstResponse  chan struct{}
	secondRequest  chan struct{}
	release        chan struct{}
}

func (w *configBarrierWriter) Write(record []byte) error {
	var decoded map[string]any
	_ = json.Unmarshal(record, &decoded)
	if decoded["kind"] == "request" {
		w.mu.Lock()
		w.requests++
		if w.requests == 2 {
			close(w.secondRequest)
		}
		w.mu.Unlock()
		return nil
	}
	w.blockFirstOnce.Do(func() {
		close(w.firstResponse)
		<-w.release
	})
	return nil
}

func TestConfigMutexSerializesPersistThroughPostResponseActivation(t *testing.T) {
	repository := &fakeAuditTargetRepository{createCalled: make(chan int, 2)}
	broker := &spyAuditBroker{}
	defer broker.closeTargets()
	writer := &configBarrierWriter{
		firstResponse: make(chan struct{}), secondRequest: make(chan struct{}), release: make(chan struct{}),
	}
	handler := withAuditWriter(t, newAuditConfigHandler(t, identity.RoleRoot, repository, broker), writer)

	responses := make(chan *httptest.ResponseRecorder, 2)
	go func() {
		responses <- call(t, handler, http.MethodPost, "/api/v1/audit/targets",
			map[string]string{"path": filepath.Join(t.TempDir(), "first.log")}, testToken)
	}()
	<-writer.firstResponse
	if call := <-repository.createCalled; call != 1 {
		t.Fatalf("first persistence call = %d, want 1", call)
	}
	go func() {
		responses <- call(t, handler, http.MethodPost, "/api/v1/audit/targets",
			map[string]string{"path": filepath.Join(t.TempDir(), "second.log")}, testToken)
	}()
	<-writer.secondRequest
	select {
	case call := <-repository.createCalled:
		t.Fatalf("second config operation persisted before first activation: create call %d", call)
	case <-time.After(50 * time.Millisecond):
	}
	close(writer.release)
	for range 2 {
		if response := <-responses; response.Code != http.StatusCreated {
			t.Fatalf("serialized create = %d, want 201; body %s", response.Code, response.Body)
		}
	}
	if creates, _ := repository.calls(); creates != 2 {
		t.Fatalf("serialized create calls = %d, want 2 after activation", creates)
	}
}

func TestConfigMutexUnlockSurvivesResponseOrActivationPanic(t *testing.T) {
	for _, test := range []struct {
		name            string
		panicResponse   bool
		panicActivation bool
	}{
		{name: "response write", panicResponse: true},
		{name: "activation", panicActivation: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeAuditTargetRepository{}
			broker := &spyAuditBroker{panicAdd: test.panicActivation}
			defer broker.closeTargets()
			writer := &responseFailWriter{panicResponse: test.panicResponse}
			handler := withAuditWriter(t, newAuditConfigHandler(t, identity.RoleRoot, repository, broker), writer)

			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("forced panic did not reach the seam caller")
					}
				}()
				call(t, handler, http.MethodPost, "/api/v1/audit/targets",
					map[string]string{"path": filepath.Join(t.TempDir(), "panic.log")}, testToken)
			}()

			writer.mu.Lock()
			writer.panicResponse = false
			writer.mu.Unlock()
			broker.mu.Lock()
			broker.panicAdd = false
			broker.mu.Unlock()

			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- call(t, handler, http.MethodPost, "/api/v1/audit/targets",
					map[string]string{"path": filepath.Join(t.TempDir(), "after.log")}, testToken)
			}()
			select {
			case response := <-done:
				if response.Code != http.StatusCreated {
					t.Fatalf("config after panic = %d, want 201; body %s", response.Code, response.Body)
				}
			case <-time.After(time.Second):
				t.Fatal("config mutex remained locked after panic")
			}
		})
	}
}

func TestFirstEnableUsesSystemLogAndStartsFileOnNextRequest(t *testing.T) {
	var systemLog bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&systemLog, nil))
	broker, err := audit.NewBroker(logger)
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	repository := &fakeAuditTargetRepository{}
	handler := newHandlerWithAudit(
		&fakeTenancyRepository{}, &fakeInstanceRepository{}, testAuth{},
		testRoles{role: identity.RoleRoot}, logger, repository, broker,
		func() time.Time { return initTestTime },
	)
	handler = withAuditWriter(t, handler, broker)
	path := filepath.Join(t.TempDir(), "first.log")

	created := call(t, handler, http.MethodPost, "/api/v1/audit/targets", map[string]string{"path": path}, testToken)
	if created.Code != http.StatusCreated {
		t.Fatalf("first enable = %d, want 201; body %s", created.Code, created.Body)
	}
	logs := decodeAuditLines(t, systemLog.Bytes())
	if len(logs) != 2 || logs[0]["sink"] != "system" || logs[1]["sink"] != "system" {
		t.Fatalf("first-enable system records = %#v, want request and response marked sink=system", logs)
	}
	if raw, err := os.ReadFile(path); err != nil || len(raw) != 0 {
		t.Fatalf("new target contains retrospective first-enable records: %q, err=%v", raw, err)
	}

	listed := call(t, handler, http.MethodGet, "/api/v1/audit/targets", nil, testToken)
	if listed.Code != http.StatusOK {
		t.Fatalf("request after first enable = %d, want 200; body %s", listed.Code, listed.Body)
	}
	records, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read first target: %v", err)
	}
	if got := decodeAuditLines(t, records); len(got) != 2 || got[0]["kind"] != "request" || got[1]["kind"] != "response" {
		t.Fatalf("first target's next request = %#v, want one pair", got)
	}
	targets, _ := repository.ListAuditTargets(context.Background())
	if err := broker.Remove(targets[0].ID); err != nil {
		t.Fatalf("close first target: %v", err)
	}
}

func TestAuditTargetListCarriesHealthMeasurementAndCreateReturnsConflictAtThree(t *testing.T) {
	firstFailure := initTestTime.Add(time.Minute)
	lastFailure := firstFailure.Add(time.Hour)
	lastReopened := lastFailure.Add(time.Hour)
	targetID := "11111111-2222-4333-8444-555555555555"
	repository := &fakeAuditTargetRepository{targets: []identity.AuditTarget{
		{ID: targetID, Path: "/var/log/audit.log", CreatedAt: initTestTime},
		{ID: "22222222-3333-4444-8555-666666666666", Path: "/var/log/two.log", CreatedAt: initTestTime},
		{ID: "33333333-4444-4555-8666-777777777777", Path: "/var/log/three.log", CreatedAt: initTestTime},
	}}
	broker := &spyAuditBroker{health: []audit.SinkHealth{{
		ID: targetID, Status: audit.SinkStatusFailing, Since: firstFailure,
		ConsecutiveFailures: 2, CumulativeFailures: 7, LastFailureAt: lastFailure,
		LastReopenedAt: lastReopened,
		Measurement: &audit.SinkMeasurement{
			CurrentFileSizeBytes: 0, FilesystemFreeBytes: 16 * 1024 * 1024,
		},
	}}}
	handler := newAuditConfigHandler(t, identity.RoleRoot, repository, broker)

	var listed ListAuditTargets200JSONResponse
	requestJSON(t, handler, http.MethodGet, "/api/v1/audit/targets", nil, http.StatusOK, &listed)
	got := listed.Targets[0]
	if got.Status != Failing || got.Since == nil || !got.Since.Equal(firstFailure) ||
		got.ConsecutiveFailures != 2 || got.CumulativeFailures != 7 ||
		got.LastFailureAt == nil || !got.LastFailureAt.Equal(lastFailure) ||
		got.LastReopenedAt == nil || !got.LastReopenedAt.Equal(lastReopened) {
		t.Fatalf("audit target health = %#v, want failure and reopen history", got)
	}
	measurement, err := got.Measurement.AsAuditTargetMeasurementAvailable()
	if err != nil || measurement.State != Available || measurement.CurrentFileSizeBytes != 0 ||
		measurement.FilesystemFreeBytes != 16*1024*1024 {
		t.Fatalf("empty current file measurement = %#v, %v", measurement, err)
	}

	response := call(t, handler, http.MethodPost, "/api/v1/audit/targets",
		map[string]string{"path": filepath.Join(t.TempDir(), "fourth.log")}, testToken)
	if response.Code != http.StatusConflict {
		t.Fatalf("fourth target through API = %d, want 409; body %s", response.Code, response.Body)
	}
}

func TestAuditTargetPresentOnlyOnPeerReportsFailingHere(t *testing.T) {
	targetID := "11111111-2222-4333-8444-555555555555"
	repository := &fakeAuditTargetRepository{targets: []identity.AuditTarget{{
		ID: targetID, Path: "/var/log/peer-added.log", CreatedAt: initTestTime,
	}}}
	handler := newAuditConfigHandler(t, identity.RoleRoot, repository, &spyAuditBroker{})

	var listed ListAuditTargets200JSONResponse
	requestJSON(t, handler, http.MethodGet, "/api/v1/audit/targets", nil, http.StatusOK, &listed)
	if len(listed.Targets) != 1 {
		t.Fatalf("targets = %d, want peer-only target", len(listed.Targets))
	}
	got := listed.Targets[0]
	if got.Status != Failing || got.ConsecutiveFailures != 0 || got.CumulativeFailures != 0 ||
		got.Since != nil || got.LastFailureAt != nil || got.LastReopenedAt != nil {
		t.Fatalf("peer-only target health = %#v, want failing with no local failure history", got)
	}
	if state, err := got.Measurement.Discriminator(); err != nil || state != string(Unavailable) {
		t.Fatalf("peer-only target measurement state = %q, %v; want unavailable", state, err)
	}
}
