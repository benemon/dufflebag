package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

const maxSinks = 3

var (
	ErrNoHealthySink = errors.New("no audit sink accepted the record")
	ErrTooManySinks  = errors.New("at most three audit sinks are allowed")
	ErrSinkExists    = errors.New("audit sink already exists")
	ErrSinkNotFound  = errors.New("audit sink not found")
)

type SinkStatus string

const (
	SinkStatusHealthy SinkStatus = "healthy"
	SinkStatusFailing SinkStatus = "failing"
)

// Target gives health and warnings the stable identity used by configuration.
type Target struct {
	ID   string
	Sink Sink
}

type SinkHealth struct {
	ID                  string
	Status              SinkStatus
	Since               time.Time
	ConsecutiveFailures int
	CumulativeFailures  int
	LastFailureAt       time.Time
	LastReopenedAt      time.Time
	Measurement         *SinkMeasurement
}

type brokerTarget struct {
	target  Target
	health  SinkHealth
	refs    int
	removed bool
	drained chan struct{}
}

type Broker struct {
	logger  *slog.Logger
	now     func() time.Time
	targets []*brokerTarget
	mu      sync.Mutex
}

func NewBroker(logger *slog.Logger, targets ...Target) (*Broker, error) {
	return newBroker(logger, time.Now, targets...)
}

func newBroker(logger *slog.Logger, now func() time.Time, targets ...Target) (*Broker, error) {
	if len(targets) > maxSinks {
		return nil, ErrTooManySinks
	}
	if logger == nil {
		logger = slog.Default()
	}
	broker := &Broker{
		logger:  logger,
		now:     now,
		targets: make([]*brokerTarget, 0, len(targets)),
	}
	for _, target := range targets {
		broker.targets = append(broker.targets, &brokerTarget{
			target:  target,
			health:  SinkHealth{ID: target.ID, Status: SinkStatusHealthy},
			drained: make(chan struct{}),
		})
	}
	return broker, nil
}

func (b *Broker) Write(record []byte) error {
	targets := b.acquireTargets()
	var failures []error
	for _, target := range targets {
		if err := target.target.Sink.Write(record); err != nil {
			failures = append(failures, fmt.Errorf("sink %q: %w", target.target.ID, err))
			health := b.failed(target)
			b.logger.Warn(
				"audit sink write failed",
				"sink_id", health.ID,
				"error", err,
				"since", health.Since,
				"consecutive_failures", health.ConsecutiveFailures,
			)
			b.releaseTarget(target)
			continue
		}
		b.succeeded(target)
		b.releaseTarget(target)
	}
	// The explicit zero-failure case keeps a broker with no configured sinks
	// disabled rather than treating it as degraded; configured sinks are
	// covered by the second clause.
	if len(failures) == 0 || len(failures) < len(targets) {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrNoHealthySink, errors.Join(failures...))
}

// Reopen rotates every configured file target in the same order as writes.
// A failed reopen leaves FileSink's previous safe descriptor in place, but the
// target stays visibly failing until a later write proves it healthy again.
func (b *Broker) Reopen() error {
	targets := b.acquireTargets()
	var failures []error
	for _, target := range targets {
		if err := target.target.Sink.Reopen(); err != nil {
			failures = append(failures, fmt.Errorf("sink %q: %w", target.target.ID, err))
			health := b.failed(target)
			b.logger.Warn(
				"audit sink reopen failed",
				"sink_id", health.ID,
				"error", err,
				"since", health.Since,
				"consecutive_failures", health.ConsecutiveFailures,
			)
			b.releaseTarget(target)
			continue
		}
		b.reopened(target)
		b.releaseTarget(target)
	}
	if len(failures) == 0 {
		return nil
	}
	return errors.Join(failures...)
}

// Close gives every configured sink the same process-owned shutdown deadline.
func (b *Broker) Close(ctx context.Context) error {
	b.mu.Lock()
	targets := append([]*brokerTarget(nil), b.targets...)
	b.mu.Unlock()
	var failures []error
	for _, target := range targets {
		if err := target.target.Sink.Close(ctx); err != nil {
			failures = append(failures, fmt.Errorf("sink %q: %w", target.target.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (b *Broker) acquireTargets() []*brokerTarget {
	b.mu.Lock()
	defer b.mu.Unlock()
	targets := append([]*brokerTarget(nil), b.targets...)
	for _, target := range targets {
		target.refs++
	}
	return targets
}

func (b *Broker) releaseTarget(target *brokerTarget) {
	b.mu.Lock()
	defer b.mu.Unlock()
	target.refs--
	if target.removed && target.refs == 0 {
		close(target.drained)
	}
}

func (b *Broker) failed(target *brokerTarget) SinkHealth {
	b.mu.Lock()
	defer b.mu.Unlock()
	health := &target.health
	failedAt := b.now()
	if health.Status == SinkStatusHealthy {
		health.Status = SinkStatusFailing
		health.Since = failedAt
		health.ConsecutiveFailures = 0
	}
	health.ConsecutiveFailures++
	health.CumulativeFailures++
	health.LastFailureAt = failedAt
	return *health
}

func (b *Broker) succeeded(target *brokerTarget) {
	b.mu.Lock()
	defer b.mu.Unlock()
	health := &target.health
	health.Status = SinkStatusHealthy
	health.Since = time.Time{}
	health.ConsecutiveFailures = 0
}

func (b *Broker) reopened(target *brokerTarget) {
	b.mu.Lock()
	defer b.mu.Unlock()
	health := &target.health
	health.Status = SinkStatusHealthy
	health.Since = time.Time{}
	health.ConsecutiveFailures = 0
	health.LastReopenedAt = b.now()
}

func (b *Broker) Health() []SinkHealth {
	targets := b.acquireTargets()
	health := make([]SinkHealth, 0, len(targets))
	for _, target := range targets {
		measurement, err := target.target.Sink.Measure()
		b.mu.Lock()
		current := target.health
		b.mu.Unlock()
		if err == nil {
			current.Measurement = &measurement
		}
		health = append(health, current)
		b.releaseTarget(target)
	}
	return health
}

func (b *Broker) Degraded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.targets) == 0 {
		return false
	}
	for i := range b.targets {
		if b.targets[i].health.Status == SinkStatusHealthy {
			return false
		}
	}
	return true
}

// Enabled reports whether records currently have a configured file target.
func (b *Broker) Enabled() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.targets) != 0
}

// Add activates a validated target. Configuration persistence owns the
// three-target policy; this repeats the bound so an impossible active set
// cannot be constructed through the runtime surface.
func (b *Broker) Add(target Target) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.targets) == maxSinks {
		return ErrTooManySinks
	}
	for _, existing := range b.targets {
		if existing.target.ID == target.ID {
			return ErrSinkExists
		}
	}
	b.targets = append(b.targets, &brokerTarget{
		target:  target,
		health:  SinkHealth{ID: target.ID, Status: SinkStatusHealthy},
		drained: make(chan struct{}),
	})
	return nil
}

// Remove detaches a target from new writes, waits for writes that already
// acquired it, then closes the underlying handle.
func (b *Broker) Remove(id string) error {
	b.mu.Lock()
	var removed *brokerTarget
	for i, target := range b.targets {
		if target.target.ID == id {
			removed = target
			b.targets = append(b.targets[:i:i], b.targets[i+1:]...)
			break
		}
	}
	if removed == nil {
		b.mu.Unlock()
		return ErrSinkNotFound
	}
	removed.removed = true
	if removed.refs == 0 {
		close(removed.drained)
	}
	b.mu.Unlock()

	<-removed.drained
	return removed.target.Sink.Close(context.Background())
}

// WriteSystem writes the same audit fields to the process log when a config
// request is enabling the first target. The standard logger adds its ordinary
// envelope; sink=system distinguishes this accepted pre-activation trail.
func (b *Broker) WriteSystem(record []byte) error {
	var fields map[string]any
	if err := json.Unmarshal(record, &fields); err != nil {
		return fmt.Errorf("decode system audit record: %w", err)
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	attrs := make([]slog.Attr, 0, len(keys)+1)
	for _, key := range keys {
		attrs = append(attrs, slog.Any(key, fields[key]))
	}
	attrs = append(attrs, slog.String("sink", "system"))
	b.logger.LogAttrs(context.Background(), slog.LevelInfo, "audit event", attrs...)
	return nil
}
