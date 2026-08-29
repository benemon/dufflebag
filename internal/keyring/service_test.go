package keyring

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type serviceStore struct {
	mu           sync.Mutex
	entries      []Entry
	listErr      error
	createResult bool
	rewrapWrites int
}

func (s *serviceStore) ListKeyringEntries(context.Context) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	return cloneEntries(s.entries), nil
}

func (s *serviceStore) CreateKeyringEntries(_ context.Context, entries []Entry, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.createResult {
		return false, nil
	}
	for _, entry := range entries {
		entry.WrappedAt = at
		s.entries = append(s.entries, entry)
	}
	return true, nil
}

func (s *serviceStore) RewrapKeyringEntries(_ context.Context, entries []Entry, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rewrapWrites++
	for i := range entries {
		entries[i].WrappedAt = at
	}
	s.entries = cloneEntries(entries)
	return nil
}

func cloneEntries(entries []Entry) []Entry {
	cloned := make([]Entry, len(entries))
	for i, entry := range entries {
		cloned[i] = entry
		cloned[i].Wrapped = bytes.Clone(entry.Wrapped)
	}
	return cloned
}

type serviceProvider struct {
	mu       sync.Mutex
	fail     bool
	failCall int
	calls    int
	wraps    int
	unwraps  int
}

func (p *serviceProvider) failed() bool {
	p.calls++
	return p.fail || (p.failCall != 0 && p.calls >= p.failCall)
}

func (p *serviceProvider) Wrap(_ context.Context, plaintext []byte) ([]byte, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wraps++
	if p.failed() {
		return nil, "", errors.New("key service unreachable")
	}
	wrapped := bytes.Clone(plaintext)
	for i := range wrapped {
		wrapped[i] ^= 0x5a
	}
	return wrapped, "kek-v2", nil
}

func (p *serviceProvider) Unwrap(_ context.Context, blob []byte, _ string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unwraps++
	if p.failed() {
		return nil, errors.New("key service unreachable")
	}
	plaintext := bytes.Clone(blob)
	for i := range plaintext {
		plaintext[i] ^= 0x5a
	}
	return plaintext, nil
}

type nativeServiceProvider struct {
	*serviceProvider
	nativeCalls int
}

type versionedServiceProvider struct {
	*serviceProvider
	latest    string
	latestErr error
}

func (p *versionedServiceProvider) LatestKEK(context.Context) (string, error) {
	return p.latest, p.latestErr
}

func (p *nativeServiceProvider) Rewrap(_ context.Context, blob []byte, _ string) ([]byte, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nativeCalls++
	if p.failed() {
		return nil, "", errors.New("key service unreachable")
	}
	return bytes.Clone(blob), "kek-native", nil
}

func serviceFixture(t *testing.T) (*Service, *serviceStore, *serviceProvider) {
	t.Helper()
	provider := &serviceProvider{}
	ring, entries, err := Generate(context.Background(), provider)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	provider.calls = 0
	provider.wraps = 0
	store := &serviceStore{entries: entries, createResult: true}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewService(provider, store, ring, logger), store, provider
}

func TestServiceProbeTracksOnlyKeyServiceFailures(t *testing.T) {
	service, store, provider := serviceFixture(t)
	edge := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return edge }
	provider.fail = true
	if err := service.Probe(context.Background()); err == nil {
		t.Fatal("broken provider probe succeeded")
	}
	if got := service.State(); got != "degraded" {
		t.Fatalf("state after provider failure = %q, want degraded", got)
	}
	if !service.since.Equal(edge) || service.consecutiveFailures != 1 {
		t.Fatalf("failure edge = %v/%d, want %v/1", service.since, service.consecutiveFailures, edge)
	}
	store.listErr = errors.New("database unavailable")
	if err := service.Probe(context.Background()); err == nil {
		t.Fatal("database listing failure succeeded")
	}
	if got := service.State(); got != "degraded" {
		t.Fatalf("database error changed degraded encryption state to %q", got)
	}
	store.listErr = nil
	provider.fail = false
	if err := service.Probe(context.Background()); err != nil {
		t.Fatalf("restored provider probe: %v", err)
	}
	if got := service.State(); got != "ok" {
		t.Fatalf("state after recovery = %q, want ok", got)
	}
	if !service.since.IsZero() || service.consecutiveFailures != 0 {
		t.Fatalf("recovered health retained edge = %v/%d", service.since, service.consecutiveFailures)
	}
	store.listErr = errors.New("database unavailable")
	if err := service.Probe(context.Background()); err == nil {
		t.Fatal("database listing failure succeeded")
	}
	if got := service.State(); got != "ok" {
		t.Fatalf("database error changed encryption state to %q", got)
	}
}

func TestServiceProbeStoresLatestKEK(t *testing.T) {
	service, _, provider := serviceFixture(t)
	service.provider = &versionedServiceProvider{serviceProvider: provider, latest: "v6"}
	if err := service.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got := service.LatestKEK(); got != "v6" {
		t.Fatalf("LatestKEK = %q, want v6", got)
	}
}

func TestServiceProbeLatestKEKErrorClearsValueWithoutDegrading(t *testing.T) {
	service, _, provider := serviceFixture(t)
	versioned := &versionedServiceProvider{serviceProvider: provider, latest: "v6"}
	service.provider = versioned
	if err := service.Probe(context.Background()); err != nil {
		t.Fatalf("initial Probe: %v", err)
	}
	versioned.latestErr = errors.New("read capability denied")
	if err := service.Probe(context.Background()); err != nil {
		t.Fatalf("Probe with Versioner error: %v", err)
	}
	if got := service.LatestKEK(); got != "" {
		t.Fatalf("LatestKEK after Versioner error = %q, want unknown", got)
	}
	if got := service.State(); got != "ok" {
		t.Fatalf("state after Versioner error = %q, want ok", got)
	}
}

func TestServiceRefreshLatestKEKReadsLiveAndStores(t *testing.T) {
	service, _, provider := serviceFixture(t)
	versioned := &versionedServiceProvider{serviceProvider: provider, latest: "v6"}
	service.provider = versioned
	if got := service.RefreshLatestKEK(context.Background()); got != "v6" {
		t.Fatalf("RefreshLatestKEK = %q, want v6", got)
	}
	versioned.latest = "v7"
	if got := service.RefreshLatestKEK(context.Background()); got != "v7" {
		t.Fatalf("RefreshLatestKEK after rotation = %q, want v7", got)
	}
	if got := service.LatestKEK(); got != "v7" {
		t.Fatalf("LatestKEK after refresh = %q, want v7", got)
	}
	versioned.latestErr = errors.New("read capability denied")
	if got := service.RefreshLatestKEK(context.Background()); got != "" {
		t.Fatalf("RefreshLatestKEK under error = %q, want unknown", got)
	}
	if got := service.State(); got != "ok" {
		t.Fatalf("state after refresh error = %q, want ok", got)
	}
}

func TestServiceProbeWithoutVersionerLeavesLatestKEKUnknown(t *testing.T) {
	service, _, _ := serviceFixture(t)
	if err := service.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got := service.LatestKEK(); got != "" {
		t.Fatalf("LatestKEK = %q, want unknown", got)
	}
}

func TestServiceRewrapWritesNothingAfterPartialProviderFailure(t *testing.T) {
	service, store, provider := serviceFixture(t)
	before := cloneEntries(store.entries)
	provider.failCall = 3
	if _, err := service.Rewrap(context.Background()); err == nil {
		t.Fatal("rewrap with third provider call failing succeeded")
	}
	if store.rewrapWrites != 0 {
		t.Fatalf("rewrap writes = %d, want zero", store.rewrapWrites)
	}
	if !entriesEqual(store.entries, before) {
		t.Fatal("stored rows changed after partial provider failure")
	}
}

func entriesEqual(a, b []Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Purpose != b[i].Purpose || a[i].Version != b[i].Version ||
			a[i].KEKRef != b[i].KEKRef || !a[i].WrappedAt.Equal(b[i].WrappedAt) ||
			!bytes.Equal(a[i].Wrapped, b[i].Wrapped) {
			return false
		}
	}
	return true
}

func TestServiceRewrapUsesNativeCapabilityAndFallback(t *testing.T) {
	t.Run("native", func(t *testing.T) {
		service, store, base := serviceFixture(t)
		native := &nativeServiceProvider{serviceProvider: base}
		service.provider = native
		if _, err := service.Rewrap(context.Background()); err != nil {
			t.Fatalf("Rewrap: %v", err)
		}
		if native.nativeCalls != len(store.entries) || base.wraps != 0 {
			t.Fatalf("native calls/wraps = %d/%d, want %d/0", native.nativeCalls, base.wraps, len(store.entries))
		}
	})
	t.Run("fallback", func(t *testing.T) {
		service, store, provider := serviceFixture(t)
		if _, err := service.Rewrap(context.Background()); err != nil {
			t.Fatalf("Rewrap: %v", err)
		}
		if provider.wraps != len(store.entries) {
			t.Fatalf("fallback wraps = %d, want %d", provider.wraps, len(store.entries))
		}
	})
}

func TestServiceRotateConflict(t *testing.T) {
	service, store, _ := serviceFixture(t)
	store.createResult = false
	if _, err := service.Rotate(context.Background()); !errors.Is(err, ErrRotationConflict) {
		t.Fatalf("Rotate error = %v, want ErrRotationConflict", err)
	}
}

func TestRotationPreservesOldEnvelopesAndMACs(t *testing.T) {
	service, _, _ := serviceFixture(t)
	aad := []byte("tenant|row")
	oldEnvelope, err := service.ring.Encrypt([]byte("old payload"), aad)
	if err != nil {
		t.Fatal(err)
	}
	oldMAC := service.ring.MAC([]byte("old row"))
	if _, err := service.Rotate(context.Background()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	newEnvelope, err := service.ring.Encrypt([]byte("new payload"), aad)
	if err != nil {
		t.Fatal(err)
	}
	newMAC := service.ring.MAC([]byte("new row"))
	if got := binary.BigEndian.Uint32(newEnvelope[1:5]); got != 2 {
		t.Fatalf("new envelope version = %d, want 2", got)
	}
	if got := binary.BigEndian.Uint32(newMAC[1:5]); got != 2 {
		t.Fatalf("new MAC version = %d, want 2", got)
	}
	if _, err := service.ring.Decrypt(oldEnvelope, aad); err != nil {
		t.Fatalf("old envelope after rotation: %v", err)
	}
	if err := service.ring.VerifyMAC(oldMAC, []byte("old row")); err != nil {
		t.Fatalf("old MAC after rotation: %v", err)
	}
}

func TestProbeAdoptsPeerRotation(t *testing.T) {
	service, store, provider := serviceFixture(t)
	loaded, err := Load(context.Background(), provider, store.entries)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	service.ring = loaded
	key := bytes.Repeat([]byte{0x33}, keyBytes)
	wrapped, kekRef, err := provider.Wrap(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	store.entries = append(store.entries, Entry{Purpose: PurposePayload, Version: 2, Wrapped: wrapped, KEKRef: kekRef})
	if err := service.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	envelope, err := service.ring.Encrypt([]byte("peer version"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.BigEndian.Uint32(envelope[1:5]); got != 2 {
		t.Fatalf("active payload version = %d, want peer version 2", got)
	}
}

func TestTokenSigningKeysNewestFirstAndCopied(t *testing.T) {
	keys := map[string]map[uint32][]byte{
		PurposePayload:   {1: bytes.Repeat([]byte{1}, keyBytes)},
		PurposeIntegrity: {1: bytes.Repeat([]byte{2}, keyBytes)},
		PurposeTokenSigning: {
			1: bytes.Repeat([]byte{3}, keyBytes),
			3: bytes.Repeat([]byte{5}, keyBytes),
			2: bytes.Repeat([]byte{4}, keyBytes),
		},
		PurposeAuditHMAC: {1: bytes.Repeat([]byte{6}, keyBytes)},
	}
	ring, err := New(keys)
	if err != nil {
		t.Fatal(err)
	}
	got := ring.TokenSigningKeys()
	if got[0][0] != 5 || got[1][0] != 4 || got[2][0] != 3 {
		t.Fatalf("key order = %d, %d, %d; want 5, 4, 3", got[0][0], got[1][0], got[2][0])
	}
	got[0][0] = 0
	if ring.TokenSigningKeys()[0][0] != 5 {
		t.Fatal("TokenSigningKeys exposed keyring memory")
	}
}

func TestConcurrentEncryptAndRotate(t *testing.T) {
	service, _, _ := serviceFixture(t)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				if _, err := service.ring.Encrypt([]byte("payload"), []byte("aad")); err != nil {
					t.Errorf("Encrypt: %v", err)
				}
			}
		}()
	}
	if _, err := service.Rotate(context.Background()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	workers.Wait()
}
