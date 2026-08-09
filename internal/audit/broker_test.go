package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type scriptedSink struct {
	mu          sync.Mutex
	results     []error
	writes      [][]byte
	measurement SinkMeasurement
	measureErr  error
}

func (s *scriptedSink) Write(record []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, append([]byte(nil), record...))
	if len(s.results) == 0 {
		return nil
	}
	err := s.results[0]
	s.results = s.results[1:]
	return err
}

func (s *scriptedSink) Reopen() error { return nil }

func (s *scriptedSink) Measure() (SinkMeasurement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.measurement, s.measureErr
}

func (s *scriptedSink) Close(context.Context) error { return nil }

func (s *scriptedSink) writeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.writes)
}

func TestBrokerWritesEverySinkAndSucceedsWithOneHealthy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	healthy, err := NewFileSink(path, nil)
	if err != nil {
		t.Fatalf("new healthy file sink: %v", err)
	}
	failing := &scriptedSink{results: []error{errors.New("disk full")}}
	var systemLog bytes.Buffer
	broker, err := NewBroker(
		slog.New(slog.NewJSONHandler(&systemLog, nil)),
		Target{ID: "healthy", Sink: healthy},
		Target{ID: "failing", Sink: failing},
	)
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}

	record := []byte("record\n")
	if err := broker.Write(record); err != nil {
		t.Fatalf("one of two sinks accepted the record: %v", err)
	}
	if failing.writeCount() != 1 {
		t.Fatalf("failing sink writes = %d, want 1; broker stopped after the healthy sink", failing.writeCount())
	}
	if broker.Degraded() {
		t.Fatal("broker is degraded while one sink is healthy")
	}
	health := broker.Health()
	if len(health) != 2 || health[0].Status != SinkStatusHealthy || health[1].Status != SinkStatusFailing {
		t.Fatalf("health = %+v, want healthy then failing", health)
	}
	if err := healthy.Close(context.Background()); err != nil {
		t.Fatalf("close healthy sink: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read healthy file: %v", err)
	}
	if !bytes.Equal(got, record) {
		t.Fatalf("healthy sink record = %q, want %q", got, record)
	}
}

func TestBrokerWarnsAndCountsEveryFailureThenRecoversOnWrite(t *testing.T) {
	failure := errors.New("device unavailable")
	sink := &scriptedSink{results: []error{failure, failure, nil}}
	first := time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	times := []time.Time{first, second}
	nowCalls := 0
	now := func() time.Time {
		at := times[nowCalls]
		nowCalls++
		return at
	}
	var systemLog bytes.Buffer
	broker, err := newBroker(
		slog.New(slog.NewJSONHandler(&systemLog, nil)),
		now,
		Target{ID: "file-1", Sink: sink},
	)
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if err := broker.Write([]byte("record")); !errors.Is(err, ErrNoHealthySink) {
			t.Fatalf("failure %d = %v, want ErrNoHealthySink", attempt, err)
		}
	}
	if !broker.Degraded() {
		t.Fatal("broker with no healthy sink is not degraded")
	}
	health := broker.Health()
	if len(health) != 1 || health[0].Status != SinkStatusFailing || health[0].ConsecutiveFailures != 2 || health[0].CumulativeFailures != 2 {
		t.Fatalf("health after two failures = %+v", health)
	}
	if !health[0].Since.Equal(first) {
		t.Fatalf("failure since = %v, want first failure time %v", health[0].Since, first)
	}
	if !health[0].LastFailureAt.Equal(second) {
		t.Fatalf("last failure at = %v, want latest failure time %v", health[0].LastFailureAt, second)
	}
	if nowCalls != 2 {
		t.Fatalf("clock read %d times, want once per failure", nowCalls)
	}

	lines := strings.Split(strings.TrimSpace(systemLog.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("system WARN records = %d, want one for each failure: %s", len(lines), systemLog.String())
	}
	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode WARN %d: %v", i+1, err)
		}
		if record["level"] != "WARN" || record["msg"] != "audit sink write failed" || record["sink_id"] != "file-1" {
			t.Fatalf("WARN %d = %#v", i+1, record)
		}
		if record["consecutive_failures"] != float64(i+1) {
			t.Fatalf("WARN %d count = %#v, want %d", i+1, record["consecutive_failures"], i+1)
		}
	}

	if err := broker.Write([]byte("recovery record")); err != nil {
		t.Fatalf("ordinary write did not retry and recover sink: %v", err)
	}
	if sink.writeCount() != 3 {
		t.Fatalf("sink writes = %d, want every ordinary write attempted", sink.writeCount())
	}
	health = broker.Health()
	if health[0].Status != SinkStatusHealthy || health[0].ConsecutiveFailures != 0 || !health[0].Since.IsZero() {
		t.Fatalf("current health after recovery = %+v", health[0])
	}
	if health[0].CumulativeFailures != 2 {
		t.Fatalf("cumulative failures after recovery = %d, want 2", health[0].CumulativeFailures)
	}
	if !health[0].LastFailureAt.Equal(second) {
		t.Fatalf("last failure at after recovery = %v, want %v", health[0].LastFailureAt, second)
	}
	if broker.Degraded() {
		t.Fatal("broker remained degraded after an ordinary write recovered the sink")
	}
}

func TestBrokerDefaultsNilLoggerOnSinkFailure(t *testing.T) {
	broker, err := NewBroker(
		nil,
		Target{ID: "failing", Sink: &scriptedSink{results: []error{errors.New("disk full")}}},
	)
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	if err := broker.Write([]byte("record")); !errors.Is(err, ErrNoHealthySink) {
		t.Fatalf("failing write with default logger = %v, want ErrNoHealthySink", err)
	}
}

func TestBrokerRejectsFourthSink(t *testing.T) {
	targets := []Target{
		{ID: "one", Sink: &scriptedSink{}},
		{ID: "two", Sink: &scriptedSink{}},
		{ID: "three", Sink: &scriptedSink{}},
		{ID: "four", Sink: &scriptedSink{}},
	}
	if _, err := NewBroker(slog.Default(), targets...); !errors.Is(err, ErrTooManySinks) {
		t.Fatalf("four sinks = %v, want ErrTooManySinks", err)
	}
}

func TestBrokerWithNoSinksIsDisabledRatherThanDegraded(t *testing.T) {
	broker, err := NewBroker(slog.Default())
	if err != nil {
		t.Fatalf("new disabled broker: %v", err)
	}
	if err := broker.Write([]byte("ignored while disabled")); err != nil {
		t.Fatalf("disabled broker write: %v", err)
	}
	if broker.Degraded() {
		t.Fatal("zero configured sinks is disabled, not degraded")
	}
	if health := broker.Health(); len(health) != 0 {
		t.Fatalf("disabled broker health = %+v, want no targets", health)
	}
}

func TestBrokerHealthDistinguishesEmptyFileFromUnavailableMeasurement(t *testing.T) {
	empty := &scriptedSink{measurement: SinkMeasurement{FilesystemFreeBytes: 4096}}
	unavailable := &scriptedSink{measureErr: errors.New("descriptor stat failed")}
	broker, err := NewBroker(
		slog.Default(),
		Target{ID: "empty", Sink: empty},
		Target{ID: "unavailable", Sink: unavailable},
	)
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}

	health := broker.Health()
	if health[0].Measurement == nil || health[0].Measurement.CurrentFileSizeBytes != 0 ||
		health[0].Measurement.FilesystemFreeBytes != 4096 {
		t.Fatalf("empty target measurement = %#v, want measured zero-byte current file", health[0].Measurement)
	}
	if health[1].Measurement != nil {
		t.Fatalf("failed target measurement = %#v, want absent", health[1].Measurement)
	}
	if health[1].Status != SinkStatusHealthy {
		t.Fatalf("measurement failure changed write health to %q", health[1].Status)
	}
}

func TestBrokerSuccessfulReopenRecordsWhenCurrentFileCanReset(t *testing.T) {
	reopenedAt := time.Date(2026, 8, 4, 12, 30, 0, 0, time.UTC)
	sink := &scriptedSink{measurement: SinkMeasurement{CurrentFileSizeBytes: 8192}}
	broker, err := newBroker(
		slog.Default(), func() time.Time { return reopenedAt }, Target{ID: "rotated", Sink: sink},
	)
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	if err := broker.Reopen(); err != nil {
		t.Fatalf("reopen broker: %v", err)
	}
	sink.mu.Lock()
	sink.measurement.CurrentFileSizeBytes = 0
	sink.mu.Unlock()

	health := broker.Health()
	if len(health) != 1 || !health[0].LastReopenedAt.Equal(reopenedAt) {
		t.Fatalf("last reopened at = %v, want %v", health[0].LastReopenedAt, reopenedAt)
	}
	if health[0].Measurement == nil || health[0].Measurement.CurrentFileSizeBytes != 0 {
		t.Fatalf("post-reopen current file measurement = %#v, want measured zero", health[0].Measurement)
	}
	if health[0].Status != SinkStatusHealthy || health[0].CumulativeFailures != 0 {
		t.Fatalf("normal post-reopen size reset changed write health: %+v", health[0])
	}
}

type blockingCloseSink struct {
	writeStarted chan struct{}
	releaseWrite chan struct{}
	closed       chan struct{}
	startOnce    sync.Once
	closeOnce    sync.Once
}

func (s *blockingCloseSink) Write([]byte) error {
	s.startOnce.Do(func() { close(s.writeStarted) })
	<-s.releaseWrite
	return nil
}

func (s *blockingCloseSink) Reopen() error { return nil }

func (s *blockingCloseSink) Measure() (SinkMeasurement, error) {
	return SinkMeasurement{}, nil
}

func (s *blockingCloseSink) Close(context.Context) error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func TestBrokerRemoveWaitsForInFlightWriteBeforeClose(t *testing.T) {
	sink := &blockingCloseSink{
		writeStarted: make(chan struct{}),
		releaseWrite: make(chan struct{}),
		closed:       make(chan struct{}),
	}
	broker, err := NewBroker(slog.Default(), Target{ID: "removed", Sink: sink})
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	writeDone := make(chan error, 1)
	go func() { writeDone <- broker.Write([]byte("in flight")) }()
	<-sink.writeStarted

	removeDone := make(chan error, 1)
	go func() { removeDone <- broker.Remove("removed") }()
	select {
	case <-sink.closed:
		t.Fatal("removed target closed while an in-flight write still held it")
	case <-time.After(50 * time.Millisecond):
	}

	close(sink.releaseWrite)
	if err := <-writeDone; err != nil {
		t.Fatalf("in-flight write: %v", err)
	}
	if err := <-removeDone; err != nil {
		t.Fatalf("remove target: %v", err)
	}
	select {
	case <-sink.closed:
	default:
		t.Fatal("removed target did not close after its in-flight write drained")
	}
}
