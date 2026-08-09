package keyring

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"sync"
	"time"
)

const (
	heartbeatInterval = 5 * time.Minute
	heartbeatJitter   = 30 * time.Second
)

var (
	ErrKeyService       = errors.New("key service failure")
	ErrRotationConflict = errors.New("keyring rotation conflict")
)

// Store is the persistence needed by runtime keyring operations.
type Store interface {
	ListKeyringEntries(context.Context) ([]Entry, error)
	CreateKeyringEntries(context.Context, []Entry, time.Time) (bool, error)
	RewrapKeyringEntries(context.Context, []Entry, time.Time) error
}

// Service owns runtime keyring maintenance and its remembered key-service
// health. Startup seeds healthy because Load has just unwrapped every entry.
type Service struct {
	provider Provider
	store    Store
	ring     *Keyring
	logger   *slog.Logger
	now      func() time.Time

	opMu                sync.Mutex
	mu                  sync.RWMutex
	state               string
	since               time.Time
	consecutiveFailures int
}

func NewService(provider Provider, store Store, ring *Keyring, logger *slog.Logger) *Service {
	return &Service{
		provider: provider, store: store, ring: ring, logger: logger,
		now: time.Now, state: "ok",
	}
}

func (s *Service) State() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *Service) Entries(ctx context.Context) ([]Entry, error) {
	return s.store.ListKeyringEntries(ctx)
}

// Run probes after each fixed heartbeat interval with uniform jitter.
func (s *Service) Run(ctx context.Context) {
	for {
		delay := heartbeatInterval - heartbeatJitter + time.Duration(mathrand.Int64N(int64(2*heartbeatJitter)+1))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			_ = s.Probe(ctx)
		}
	}
}

// Probe performs the same real unwrap required during startup, then adopts
// any versions created by another replica. Database errors do not alter this
// key-service-only state.
func (s *Service) Probe(ctx context.Context) error {
	entries, err := s.store.ListKeyringEntries(ctx)
	if err != nil {
		s.logger.Warn("encryption heartbeat could not list keyring", "error", err)
		return err
	}
	if len(entries) == 0 {
		err := errors.New("keyring is empty")
		s.failed(err)
		return err
	}
	first, err := s.provider.Unwrap(ctx, entries[0].Wrapped, entries[0].KEKRef)
	if err != nil {
		s.failed(err)
		return err
	}
	if len(first) != keyBytes {
		err := fmt.Errorf("keyring: %s v%d is %d bytes, want %d", entries[0].Purpose, entries[0].Version, len(first), keyBytes)
		s.failed(err)
		return err
	}

	missing := make(map[string]map[uint32][]byte)
	for i, entry := range entries {
		if s.ring.has(entry.Purpose, entry.Version) {
			continue
		}
		key := first
		if i != 0 {
			key, err = s.provider.Unwrap(ctx, entry.Wrapped, entry.KEKRef)
			if err != nil {
				s.failed(err)
				return err
			}
		}
		if len(key) != keyBytes {
			err := fmt.Errorf("keyring: %s v%d is %d bytes, want %d", entry.Purpose, entry.Version, len(key), keyBytes)
			s.failed(err)
			return err
		}
		if missing[entry.Purpose] == nil {
			missing[entry.Purpose] = make(map[uint32][]byte)
		}
		missing[entry.Purpose][entry.Version] = key
	}
	s.ring.adopt(missing)
	s.succeeded()
	return nil
}

func (s *Service) failed(err error) {
	s.mu.Lock()
	if s.state == "ok" {
		s.state = "degraded"
		s.since = s.now()
		s.consecutiveFailures = 0
	}
	s.consecutiveFailures++
	s.mu.Unlock()
	s.logger.Warn("encryption heartbeat failed", "error", err)
}

func (s *Service) succeeded() {
	s.mu.Lock()
	s.state = "ok"
	s.since = time.Time{}
	s.consecutiveFailures = 0
	s.mu.Unlock()
}

func (s *Service) Rewrap(ctx context.Context) ([]Entry, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	defer func() { _ = s.Probe(ctx) }()

	entries, err := s.store.ListKeyringEntries(ctx)
	if err != nil {
		return nil, err
	}
	rewrapped := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		var wrapped []byte
		var kekRef string
		if native, ok := s.provider.(Rewrapper); ok {
			wrapped, kekRef, err = native.Rewrap(ctx, entry.Wrapped, entry.KEKRef)
		} else {
			var plaintext []byte
			plaintext, err = s.provider.Unwrap(ctx, entry.Wrapped, entry.KEKRef)
			if err == nil {
				wrapped, kekRef, err = s.provider.Wrap(ctx, plaintext)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%w: rewrap %s v%d: %v", ErrKeyService, entry.Purpose, entry.Version, err)
		}
		entry.Wrapped = wrapped
		entry.KEKRef = kekRef
		rewrapped = append(rewrapped, entry)
	}
	at := s.now().UTC()
	if err := s.store.RewrapKeyringEntries(ctx, rewrapped, at); err != nil {
		return nil, err
	}
	for i := range rewrapped {
		rewrapped[i].WrappedAt = at
	}
	return rewrapped, nil
}

func (s *Service) Rotate(ctx context.Context) ([]Entry, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	defer func() { _ = s.Probe(ctx) }()

	entries, err := s.store.ListKeyringEntries(ctx)
	if err != nil {
		return nil, err
	}
	versions := make(map[string]uint32, len(purposes))
	for _, entry := range entries {
		if entry.Version > versions[entry.Purpose] {
			versions[entry.Purpose] = entry.Version
		}
	}
	keys := make(map[string]map[uint32][]byte, len(purposes))
	fresh := make([]Entry, 0, len(purposes))
	for _, purpose := range purposes {
		version := versions[purpose] + 1
		key := make([]byte, keyBytes)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("generate %s key: %w", purpose, err)
		}
		wrapped, kekRef, err := s.provider.Wrap(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("%w: wrap %s v%d: %v", ErrKeyService, purpose, version, err)
		}
		keys[purpose] = map[uint32][]byte{version: key}
		fresh = append(fresh, Entry{Purpose: purpose, Version: version, Wrapped: wrapped, KEKRef: kekRef})
	}
	at := s.now().UTC()
	stored, err := s.store.CreateKeyringEntries(ctx, fresh, at)
	if err != nil {
		return nil, err
	}
	if !stored {
		return nil, ErrRotationConflict
	}
	for i := range fresh {
		fresh[i].WrappedAt = at
	}
	s.ring.adopt(keys)
	return append(entries, fresh...), nil
}
