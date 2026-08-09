package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/compat/hcpauth"
	"github.com/benemon/dufflebag/internal/domain/identity"
	platform "github.com/benemon/dufflebag/internal/platform/v1"
)

const testAuditHMACKeyVersion = "test-v1"

var testAuditHMACKey = []byte("audit-hmac-key-distinct-from-signing-key")

var objectStorageEnvironment = []string{
	"DFBG_OBJECT_STORAGE_ENDPOINT",
	"DFBG_OBJECT_STORAGE_REGION",
	"DFBG_OBJECT_STORAGE_BUCKET",
	"DFBG_OBJECT_STORAGE_ACCESS_KEY",
	"DFBG_OBJECT_STORAGE_SECRET_KEY",
}

func TestTrustedProxiesConfiguration(t *testing.T) {
	t.Run("unset is empty", func(t *testing.T) {
		t.Setenv("DFBG_TRUSTED_PROXIES", "")
		proxies, err := trustedProxiesFromEnvironment()
		if err != nil || len(proxies) != 0 {
			t.Fatalf("unset trusted proxies = %v, %v; want empty", proxies, err)
		}
	})
	t.Run("CIDRs and bare addresses are accepted", func(t *testing.T) {
		t.Setenv("DFBG_TRUSTED_PROXIES", "10.0.0.0/8, 192.0.2.10, 2001:db8::1")
		proxies, err := trustedProxiesFromEnvironment()
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"10.0.0.0/8", "192.0.2.10/32", "2001:db8::1/128"}
		if len(proxies) != len(want) {
			t.Fatalf("trusted proxies = %v, want %v", proxies, want)
		}
		for i := range want {
			if got := proxies[i].String(); got != want[i] {
				t.Fatalf("trusted proxy %d = %q, want %q", i, got, want[i])
			}
		}
	})
	t.Run("unparseable entry refuses startup configuration", func(t *testing.T) {
		t.Setenv("DFBG_TRUSTED_PROXIES", "10.0.0.0/8, not-an-address")
		_, err := trustedProxiesFromEnvironment()
		if err == nil || !strings.Contains(err.Error(), "DFBG_TRUSTED_PROXIES") ||
			!strings.Contains(err.Error(), "not-an-address") {
			t.Fatalf("error = %v, want the variable and invalid entry", err)
		}
	})
}

func TestObjectStorageConfigurationRequiresOperatorEnvironment(t *testing.T) {
	for _, name := range objectStorageEnvironment {
		t.Setenv(name, "")
	}
	objects, err := objectStorageFromEnvironment(context.Background())
	if err != nil || objects != nil {
		t.Fatalf("unconfigured object storage = %v, %v; want nil, nil", objects, err)
	}

	t.Setenv("DFBG_OBJECT_STORAGE_ENDPOINT", "http://127.0.0.1:9000")
	if _, err := objectStorageFromEnvironment(context.Background()); err == nil ||
		!strings.HasPrefix(err.Error(), "object storage configuration:") {
		t.Fatalf("partial operator configuration = %v, want configuration error", err)
	}

	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead || r.URL.Path != "/sboms" {
			t.Fatalf("startup check = %s %s, want HEAD /sboms", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer s3.Close()
	t.Setenv("DFBG_OBJECT_STORAGE_ENDPOINT", s3.URL)
	t.Setenv("DFBG_OBJECT_STORAGE_REGION", "us-east-1")
	t.Setenv("DFBG_OBJECT_STORAGE_BUCKET", "sboms")
	t.Setenv("DFBG_OBJECT_STORAGE_ACCESS_KEY", "access")
	t.Setenv("DFBG_OBJECT_STORAGE_SECRET_KEY", "secret")
	objects, err = objectStorageFromEnvironment(context.Background())
	if err != nil || objects == nil {
		t.Fatalf("complete operator configuration = %v, %v; want a store", objects, err)
	}
}

func TestObjectStorageConfigurationNeverEchoesSecret(t *testing.T) {
	for _, name := range objectStorageEnvironment {
		t.Setenv(name, "")
	}
	secret := "never-return-this-secret"
	t.Setenv("DFBG_OBJECT_STORAGE_ENDPOINT", "not-a-url")
	t.Setenv("DFBG_OBJECT_STORAGE_REGION", "us-east-1")
	t.Setenv("DFBG_OBJECT_STORAGE_BUCKET", "sboms")
	t.Setenv("DFBG_OBJECT_STORAGE_ACCESS_KEY", "access")
	t.Setenv("DFBG_OBJECT_STORAGE_SECRET_KEY", secret)
	_, err := objectStorageFromEnvironment(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "object storage configuration:") {
		t.Fatalf("malformed settings = %v, want configuration error", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("configuration error exposed secret: %v", err)
	}

	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer s3.Close()
	t.Setenv("DFBG_OBJECT_STORAGE_ENDPOINT", s3.URL)
	_, err = objectStorageFromEnvironment(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "object storage availability:") {
		t.Fatalf("rejected credentials = %v, want availability error", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("availability error exposed secret: %v", err)
	}
}

func TestObjectStorageStartupTimeoutRemainsFiveSeconds(t *testing.T) {
	if objectStorageStartupTimeout != 5*time.Second {
		t.Fatalf("object storage startup timeout = %s, want 5s", objectStorageStartupTimeout)
	}
}

func TestScannerConfiguration(t *testing.T) {
	clearScannerEnvironment := func(t *testing.T) {
		t.Helper()
		for _, entry := range os.Environ() {
			name, value, _ := strings.Cut(entry, "=")
			if !strings.HasPrefix(name, "DFBG_SCANNER_") {
				continue
			}
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Setenv(name, value) })
		}
	}

	t.Run("setting without adapter is rejected", func(t *testing.T) {
		clearScannerEnvironment(t)
		t.Setenv("DFBG_SCANNER_PASS_TIMEOUT", "1m")
		_, err := scannerConfigurationFromEnvironment()
		if err == nil || !strings.Contains(err.Error(), "DFBG_SCANNER_PASS_TIMEOUT") {
			t.Fatalf("error = %v, want offending variable", err)
		}
	})
	t.Run("unknown adapter is rejected", func(t *testing.T) {
		clearScannerEnvironment(t)
		t.Setenv("DFBG_SCANNER_ADAPTER", "other")
		if _, err := scannerConfigurationFromEnvironment(); err == nil {
			t.Fatal("unknown adapter was accepted")
		}
	})
	t.Run("malformed duration is rejected", func(t *testing.T) {
		clearScannerEnvironment(t)
		t.Setenv("DFBG_SCANNER_ADAPTER", "osv")
		t.Setenv("DFBG_SCANNER_PASS_TIMEOUT", "eventually")
		if _, err := scannerConfigurationFromEnvironment(); err == nil {
			t.Fatal("malformed duration was accepted")
		}
	})
	t.Run("zero concurrency is rejected", func(t *testing.T) {
		clearScannerEnvironment(t)
		t.Setenv("DFBG_SCANNER_ADAPTER", "osv")
		t.Setenv("DFBG_SCANNER_WORKERS", "0")
		if _, err := scannerConfigurationFromEnvironment(); err == nil {
			t.Fatal("zero concurrency was accepted")
		}
	})
	t.Run("enabled scanner requires object storage", func(t *testing.T) {
		if err := validateScannerObjectStorage(&scannerRuntimeConfig{}, nil); err == nil {
			t.Fatal("scanner without object storage was accepted")
		}
	})
	t.Run("defaults match the operator contract", func(t *testing.T) {
		clearScannerEnvironment(t)
		t.Setenv("DFBG_SCANNER_ADAPTER", "osv")
		config, err := scannerConfigurationFromEnvironment()
		if err != nil {
			t.Fatal(err)
		}
		if config.endpoint != "https://api.osv.dev" || config.format != "purl" ||
			config.requestTimeout != 30*time.Second || config.passTimeout != 15*time.Minute ||
			config.runRetention != 2160*time.Hour || config.workers != 2 ||
			config.interval != 24*time.Hour {
			t.Fatalf("defaults = %#v", config)
		}
	})
	t.Run("interval without an adapter is rejected", func(t *testing.T) {
		clearScannerEnvironment(t)
		t.Setenv("DFBG_SCANNER_INTERVAL", "6h")
		_, err := scannerConfigurationFromEnvironment()
		if err == nil || !strings.Contains(err.Error(), "DFBG_SCANNER_INTERVAL") {
			t.Fatalf("err = %v, want the offending variable named", err)
		}
	})
	t.Run("a malformed interval fails startup", func(t *testing.T) {
		clearScannerEnvironment(t)
		t.Setenv("DFBG_SCANNER_ADAPTER", "osv")
		t.Setenv("DFBG_SCANNER_INTERVAL", "occasionally")
		if _, err := scannerConfigurationFromEnvironment(); err == nil {
			t.Fatal("a malformed interval was accepted")
		}
	})
	t.Run("a configured interval is honoured", func(t *testing.T) {
		clearScannerEnvironment(t)
		t.Setenv("DFBG_SCANNER_ADAPTER", "osv")
		t.Setenv("DFBG_SCANNER_INTERVAL", "6h")
		config, err := scannerConfigurationFromEnvironment()
		if err != nil {
			t.Fatal(err)
		}
		if config.interval != 6*time.Hour {
			t.Fatalf("interval = %s, want 6h", config.interval)
		}
	})
}

func TestMountedAPIVersionsComeFromRootRouteTable(t *testing.T) {
	want := []string{"/packer/2023-01-01", "/resource-manager/2019-12-10", "/api/v1"}
	got := mountedAPIVersions(rootRoutes(nil, nil, nil, nil))
	if !slices.Equal(got, want) {
		t.Fatalf("mounted API versions = %v, want %v", got, want)
	}
	if normalizedBuildValue("", "dev") != "dev" ||
		normalizedBuildValue("1.2.3", "dev") != "1.2.3" {
		t.Fatal("build value fallback did not preserve a supplied value")
	}
}

func TestServeMuxDoesNotShadowAPIPlanes(t *testing.T) {
	t.Parallel()

	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	handler, _ := testComposer(t, api, api, api, api)

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{name: "token", path: "/oauth2/token", wantStatus: http.StatusTeapot},
		{name: "token subtree", path: "/oauth2/token/missing", wantStatus: http.StatusNotFound},
		{name: "packer subtree", path: "/packer/missing", wantStatus: http.StatusTeapot},
		{name: "init", path: "/sys/init", wantStatus: http.StatusTeapot},
		{name: "init subtree", path: "/sys/init/missing", wantStatus: http.StatusNotFound},
		// The session route must reach the platform plane, not the SPA fallback —
		// index.html served here reads as "no session" and every reload signs
		// the operator out again (the smoke failure that added this line).
		{name: "session", path: "/sys/session", wantStatus: http.StatusTeapot},
		{name: "session subtree", path: "/sys/session/missing", wantStatus: http.StatusNotFound},
		// The platform plane is mounted now (duf-puc). It reaches the same handler
		// as /sys/init, which is why both answer with the sentinel: authorization
		// happens inside that handler, not by the mux withholding the route.
		{name: "platform plane", path: "/api/v1/principals", wantStatus: http.StatusTeapot},
		// /api/v1 without a trailing slash is not a route on that plane and must
		// not fall through to the console, where an extensionless path would be
		// served index.html.
		{name: "platform plane, no subtree", path: "/api/v1", wantStatus: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if strings.Contains(response.Body.String(), "<html") {
				t.Fatalf("%s was shadowed by the SPA fallback", test.path)
			}
		})
	}
}

func TestRemovedLegacyPathFallsThroughToPackerFallback(t *testing.T) {
	t.Parallel()

	packer := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"code":12,"message":"unimplemented","details":[]}`))
	})
	notPacker := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("removed path escaped the /packer/ fallback")
		w.WriteHeader(http.StatusInternalServerError)
	})
	handler, _ := testComposer(t, notPacker, packer, notPacker, notPacker)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/packer/2021-04-30/organizations/org/projects/project/images/images/channels/latest",
		nil,
	))

	if response.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body %s", response.Code, response.Body)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var status struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Details []any  `json:"details"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatalf("parse status body: %v; body %s", err, response.Body)
	}
	if status.Code != 12 || status.Message != "unimplemented" ||
		status.Details == nil || len(status.Details) != 0 {
		t.Fatalf("status body = %#v, want code 12, message unimplemented, and details []", status)
	}
}

type memorySink struct {
	mu      sync.Mutex
	records [][]byte
	results []error
	writes  int
}

func (s *memorySink) Write(record []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	if len(s.results) >= s.writes && s.results[s.writes-1] != nil {
		return s.results[s.writes-1]
	}
	s.records = append(s.records, append([]byte(nil), record...))
	return nil
}

func (s *memorySink) Reopen() error { return nil }
func (s *memorySink) Measure() (audit.SinkMeasurement, error) {
	return audit.SinkMeasurement{}, nil
}
func (s *memorySink) Close(context.Context) error { return nil }

func (s *memorySink) decoded(t *testing.T) []map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	records := make([]map[string]any, 0, len(s.records))
	for _, encoded := range s.records {
		var record map[string]any
		if err := json.Unmarshal(encoded, &record); err != nil {
			t.Fatalf("decode audit record %q: %v", encoded, err)
		}
		records = append(records, record)
	}
	return records
}

func (s *memorySink) reset() {
	s.mu.Lock()
	s.records = nil
	s.mu.Unlock()
}

func testComposer(
	t *testing.T, token, packer, resourceManager, platformPlane http.Handler,
) (*composedHandler, *memorySink) {
	t.Helper()
	sink := &memorySink{}
	broker, err := audit.NewBroker(slog.Default(), audit.Target{ID: "test", Sink: sink})
	if err != nil {
		t.Fatalf("new audit broker: %v", err)
	}
	return composeHandler(
		broker, audit.StaticHMACKey(testAuditHMACKeyVersion, testAuditHMACKey),
		testTokenPlane{Handler: token}, packer, resourceManager, platformPlane,
	), sink
}

type testTokenPlane struct {
	http.Handler
	admission func(http.Handler) http.Handler
}

type resolvingPlane struct {
	http.Handler
	descriptor audit.Descriptor
}

func (p resolvingPlane) Resolve(*http.Request) audit.Descriptor { return p.descriptor }

func (h testTokenPlane) Admit(next http.Handler, _ ...string) http.Handler {
	if h.admission != nil {
		return h.admission(next)
	}
	return next
}

func TestComposerAuditsEveryNonExemptRootRouteClass(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler, sink := testComposer(t, api, api, api, api)

	tests := []struct {
		name, method, path, routeID string
	}{
		{name: "token", method: http.MethodPost, path: hcpauth.TokenPath, routeID: rootRouteToken},
		{name: "packer", method: http.MethodGet, path: "/packer/route", routeID: rootRoutePacker},
		{name: "resource manager", method: http.MethodGet, path: "/resource-manager/route", routeID: rootRouteResourceManager},
		{name: "init", method: http.MethodPost, path: "/sys/init", routeID: rootRouteInit},
		{name: "session", method: http.MethodGet, path: platform.SessionPath, routeID: rootRouteSession},
		{name: "platform", method: http.MethodGet, path: "/api/v1/route", routeID: rootRoutePlatform},
		{name: "closed subtree", method: http.MethodGet, path: "/api/v1", routeID: rootRouteNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sink.reset()
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))

			records := sink.decoded(t)
			if len(records) != 2 {
				t.Fatalf("audit records = %d, want request and response", len(records))
			}
			if records[0]["kind"] != "request" || records[1]["kind"] != "response" {
				t.Fatalf("event kinds = %v, %v; want request then response", records[0]["kind"], records[1]["kind"])
			}
			if records[1]["route_id"] != test.routeID {
				t.Fatalf("resolved route = %v, want %s", records[1]["route_id"], test.routeID)
			}
			if records[0]["correlation_id"] != records[1]["correlation_id"] {
				t.Fatalf("pair correlations differ: %v != %v", records[0]["correlation_id"], records[1]["correlation_id"])
			}
		})
	}
}

func TestComposerUsesPlaneDescriptorBeforeHandlerRuns(t *testing.T) {
	malformed := resolvingPlane{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
		}),
		descriptor: audit.Descriptor{
			Operation: "principal.create", TargetType: "principal", OperationID: "CreatePrincipal",
		},
	}
	other := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	handler, sink := testComposer(t, other, other, other, malformed)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(
		http.MethodPost, "/api/v1/principals", strings.NewReader("{"),
	))
	records := sink.decoded(t)
	if records[1]["operation"] != "principal.create" || records[1]["target_type"] != "principal" || records[1]["outcome"] != "refused" {
		t.Fatalf("pre-handler plane descriptor did not reach response audit: %#v", records[1])
	}
}

func TestRootRouteTableMatchesIndependentRegistrationInventory(t *testing.T) {
	plane := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	routes := rootRoutes(plane, plane, plane, plane)
	type semantic struct{ routeID, operation, targetType string }
	want := map[string]semantic{
		"/oauth2/token":      {rootRouteToken, "token.issue", "access_token"},
		"/oauth2/token/":     {rootRouteNotFound, "request.not_found", "request"},
		"/packer/":           {rootRoutePacker, "packer.request", "request"},
		"/resource-manager/": {rootRouteResourceManager, "resource_manager.request", "request"},
		"/sys/init":          {rootRouteInit, "instance.initialize", "instance"},
		"/sys/recovery":      {rootRouteRecovery, "instance.recover", "instance"},
		"/sys/health":        {rootRouteHealth, "health.read", "instance"},
		"/sys/session":       {rootRouteSession, "session.request", "session"},
		"/api/v1/":           {rootRoutePlatform, "platform.request", "request"},
		"/sys/init/":         {rootRouteNotFound, "request.not_found", "request"},
		"/sys/recovery/":     {rootRouteNotFound, "request.not_found", "request"},
		"/sys/session/":      {rootRouteNotFound, "request.not_found", "request"},
		"/api/v1":            {rootRouteNotFound, "request.not_found", "request"},
		"/metrics":           {rootRouteNotFound, "request.not_found", "request"},
		"/":                  {rootRouteConsole, "console.serve", "console"},
	}
	seen := make(map[string]bool, len(routes))
	for _, route := range routes {
		seen[route.pattern] = true
		expected, ok := want[route.pattern]
		if !ok {
			t.Errorf("registered root route %s has no independent descriptor entry", route.pattern)
			continue
		}
		got := semantic{route.descriptor.RouteID, string(route.descriptor.Operation), route.descriptor.TargetType}
		if got != expected {
			t.Errorf("root route %s descriptor = %#v, want %#v", route.pattern, got, expected)
		}
	}
	for pattern := range want {
		if !seen[pattern] {
			t.Errorf("independently described root route %s is not registered", pattern)
		}
	}
}

func TestComposerExemptsOnlyResolvedHealthAndConsoleRoutes(t *testing.T) {
	got := make([]string, 0, len(auditExemptRouteIDs))
	for routeID := range auditExemptRouteIDs {
		got = append(got, routeID)
	}
	slices.Sort(got)
	if want := []string{rootRouteConsole, rootRouteHealth}; !slices.Equal(got, want) {
		t.Fatalf("audit exemption set = %v, want %v", got, want)
	}

	platformPlane := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	notPlatform := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("health or init escaped the platform plane")
	})
	handler, sink := testComposer(t, notPlatform, notPlatform, notPlatform, platformPlane)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, platform.StatusPath, nil))
	if records := sink.decoded(t); len(records) != 0 {
		t.Fatalf("health wrote %d audit records, want none", len(records))
	}

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/sys/init", nil))
	if records := sink.decoded(t); len(records) != 2 {
		t.Fatalf("init wrote %d audit records, want a pair", len(records))
	}
}

func TestComposerServesStaticConsoleWhenAuditBrokerIsDegraded(t *testing.T) {
	sink := &memorySink{results: []error{
		errors.New("disk full"), errors.New("still full"), errors.New("still full"),
	}}
	broker, err := audit.NewBroker(slog.Default(), audit.Target{ID: "failed", Sink: sink})
	if err != nil {
		t.Fatalf("new audit broker: %v", err)
	}
	if err := broker.Write([]byte("prove the sink is unavailable")); !errors.Is(err, audit.ErrNoHealthySink) {
		t.Fatalf("degrading write = %v, want ErrNoHealthySink", err)
	}
	if !broker.Degraded() {
		t.Fatal("broker did not enter degraded state after its only sink failed")
	}
	writesBefore := sink.writes
	other := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("console request reached an API plane")
	})
	handler := composeHandler(
		broker, audit.StaticHMACKey(testAuditHMACKeyVersion, testAuditHMACKey),
		testTokenPlane{Handler: other}, other, other, other,
	)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("degraded broker console status = %d, want 200; body %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), "<!doctype html>") {
		t.Fatalf("degraded broker console did not serve the embedded SPA: %q", response.Body)
	}
	if sink.writes != writesBefore {
		t.Fatalf("static console changed audit writes from %d to %d", writesBefore, sink.writes)
	}
}

func TestComposerKeepsAdmissionOutsideAudit(t *testing.T) {
	sink := &memorySink{}
	broker, err := audit.NewBroker(slog.Default(), audit.Target{ID: "test", Sink: sink})
	if err != nil {
		t.Fatalf("new audit broker: %v", err)
	}
	called := false
	plane := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	refuse := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
	}
	handler := composeHandler(broker, audit.StaticHMACKey(testAuditHMACKeyVersion, testAuditHMACKey), testTokenPlane{Handler: plane, admission: refuse}, plane, plane, plane)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, hcpauth.TokenPath, nil))
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want admission refusal 429", response.Code)
	}
	if called {
		t.Fatal("admission refusal reached a plane")
	}
	if records := sink.decoded(t); len(records) != 0 {
		t.Fatalf("admission refusal wrote %d audit records, want none", len(records))
	}
}

func TestProductionTokenAdmissionDoesNotAuditAThrottledRequest(t *testing.T) {
	token := hcpauth.NewHandler(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sink := &memorySink{}
	broker, err := audit.NewBroker(slog.Default(), audit.Target{ID: "test", Sink: sink})
	if err != nil {
		t.Fatalf("new audit broker: %v", err)
	}
	other := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("token request reached another plane")
	})
	handler := composeHandler(broker, audit.StaticHMACKey(testAuditHMACKeyVersion, testAuditHMACKey), token, other, other, other)

	for attempt := 1; attempt <= 1000; attempt++ {
		before := len(sink.decoded(t))
		request := httptest.NewRequest(http.MethodPost, hcpauth.TokenPath, strings.NewReader("grant_type=password"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.RemoteAddr = "192.0.2.10:1234"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusTooManyRequests {
			continue
		}
		if after := len(sink.decoded(t)); after != before {
			t.Fatalf("throttled request changed audit record count from %d to %d", before, after)
		}
		return
	}
	t.Fatal("production token admission did not throttle 1000 unpaced requests")
}

func TestComposerFailsClosedBeforeCallingMux(t *testing.T) {
	sink := &memorySink{results: []error{errors.New("disk full"), errors.New("still full")}}
	broker, err := audit.NewBroker(slog.Default(), audit.Target{ID: "failed", Sink: sink})
	if err != nil {
		t.Fatalf("new audit broker: %v", err)
	}
	called := false
	plane := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	handler := composeHandler(broker, audit.StaticHMACKey(testAuditHMACKeyVersion, testAuditHMACKey), testTokenPlane{Handler: plane}, plane, plane, plane)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/packer/route", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if called {
		t.Fatal("mux was called after the request audit write failed")
	}
}

func TestComposerCannotFailClosedAfterResponseBytes(t *testing.T) {
	sink := &memorySink{results: []error{nil, errors.New("disk filled after request record")}}
	broker, err := audit.NewBroker(slog.Default(), audit.Target{ID: "late-failure", Sink: sink})
	if err != nil {
		t.Fatalf("new audit broker: %v", err)
	}
	plane := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("committed"))
	})
	handler := composeHandler(broker, audit.StaticHMACKey(testAuditHMACKeyVersion, testAuditHMACKey), testTokenPlane{Handler: plane}, plane, plane, plane)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/packer/route", nil))
	if response.Code != http.StatusAccepted || response.Body.String() != "committed" {
		t.Fatalf("late audit failure changed committed response to %d %q", response.Code, response.Body.String())
	}
}

func TestComposerWithDisabledBrokerServesNormally(t *testing.T) {
	broker, err := audit.NewBroker(slog.Default())
	if err != nil {
		t.Fatalf("new disabled audit broker: %v", err)
	}
	plane := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := composeHandler(broker, audit.StaticHMACKey(testAuditHMACKeyVersion, testAuditHMACKey), testTokenPlane{Handler: plane}, plane, plane, plane)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/packer/route", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("disabled audit changed status to %d, want 204", response.Code)
	}
}

func TestComposerRecordsAuditUnavailableIfTheResponseRetryRecovers(t *testing.T) {
	sink := &memorySink{results: []error{errors.New("transient failure"), nil}}
	broker, err := audit.NewBroker(slog.Default(), audit.Target{ID: "recovering", Sink: sink})
	if err != nil {
		t.Fatalf("new audit broker: %v", err)
	}
	plane := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request reached the mux after its audit write failed")
	})
	handler := composeHandler(broker, audit.StaticHMACKey(testAuditHMACKeyVersion, testAuditHMACKey), testTokenPlane{Handler: plane}, plane, plane, plane)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/packer/route", nil))
	records := sink.decoded(t)
	if len(records) != 1 || records[0]["kind"] != "response" || records[0]["reason"] != "audit_unavailable" {
		t.Fatalf("recovered records = %#v, want an audit_unavailable response", records)
	}
}

func TestComposerDerivesOutcomeFromActualStatus(t *testing.T) {
	statuses := []struct {
		status  int
		outcome string
	}{
		{http.StatusNoContent, "success"},
		{http.StatusTemporaryRedirect, "success"},
		{http.StatusConflict, "refused"},
		{http.StatusServiceUnavailable, "failure"},
	}
	for _, test := range statuses {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			plane := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(test.status) })
			handler, sink := testComposer(t, plane, plane, plane, plane)
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/packer/route", nil))
			records := sink.decoded(t)
			if got := records[1]["outcome"]; got != test.outcome {
				t.Fatalf("status %d outcome = %v, want %s", test.status, got, test.outcome)
			}
		})
	}
}

func TestHealthLookalikesFollowTheirActualMuxOutcome(t *testing.T) {
	platformPlane := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	other := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	handler, sink := testComposer(t, other, other, other, platformPlane)

	tests := []struct {
		name       string
		request    *http.Request
		wantStatus int
		wantRoute  string
		audited    bool
	}{
		{name: "exact", request: httptest.NewRequest(http.MethodGet, "/sys/health", nil), wantStatus: http.StatusNoContent},
		{name: "trailing slash", request: httptest.NewRequest(http.MethodGet, "/sys/health/", nil), wantStatus: http.StatusOK, wantRoute: rootRouteConsole},
		{name: "repeated slash", request: httptest.NewRequest(http.MethodGet, "/sys//health", nil), wantStatus: http.StatusTemporaryRedirect, wantRoute: rootRouteRedirect, audited: true},
		{name: "dot traversal", request: httptest.NewRequest(http.MethodGet, "/sys/../sys/health", nil), wantStatus: http.StatusTemporaryRedirect, wantRoute: rootRouteRedirect, audited: true},
		{name: "encoded path", request: httptest.NewRequest(http.MethodGet, "/sys/%68ealth", nil), wantStatus: http.StatusNoContent},
		{name: "raw path", request: rawHealthRequest(), wantStatus: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sink.reset()
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, test.request)
			if response.Code != test.wantStatus {
				t.Fatalf("actual mux status = %d, want %d", response.Code, test.wantStatus)
			}
			records := sink.decoded(t)
			if !test.audited {
				if len(records) != 0 {
					t.Fatalf("request resolved to health but wrote %d records", len(records))
				}
				return
			}
			if len(records) != 2 || records[1]["route_id"] != test.wantRoute {
				t.Fatalf("actual resolved audit route = %#v, want %s pair", records, test.wantRoute)
			}
		})
	}
}

func rawHealthRequest() *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/sys/health", nil)
	request.URL.RawPath = "/sys/%68ealth"
	request.RequestURI = request.URL.RawPath
	return request
}

func TestRealServerDeliversOptionsAsteriskToComposer(t *testing.T) {
	plane := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler, sink := testComposer(t, plane, plane, plane, plane)
	address := startRealServer(t, handler)

	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("dial real server: %v", err)
	}
	defer func() { _ = connection.Close() }()
	if _, err := io.WriteString(connection, "OPTIONS * HTTP/1.1\r\nHost: "+address+"\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write OPTIONS *: %v", err)
	}
	response, err := io.ReadAll(connection)
	if err != nil {
		t.Fatalf("read OPTIONS *: %v", err)
	}
	if !bytes.HasPrefix(response, []byte("HTTP/1.1 400 Bad Request\r\n")) {
		t.Fatalf("OPTIONS * response = %q, want handler-level 400", response)
	}
	records := sink.decoded(t)
	if len(records) != 2 || records[1]["route_id"] != rootRouteUnhandled || records[1]["outcome"] != "refused" {
		t.Fatalf("OPTIONS * audit = %#v, want resolved refused pair", records)
	}
}

func TestRealServerRecordsBothPanicBoundaries(t *testing.T) {
	packer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/after") {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("partial"))
		}
		panic("route panic")
	})
	other := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler, sink := testComposer(t, other, packer, other, other)
	address := startRealServer(t, handler)

	response, err := http.Get("http://" + address + "/packer/before")
	if err != nil {
		t.Fatalf("pre-write panic request: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatalf("read pre-write panic response: %v", err)
	}
	if response.StatusCode != http.StatusInternalServerError || string(body) != "Internal Server Error\n" {
		t.Fatalf("pre-write panic = %d %q, want complete 500", response.StatusCode, body)
	}
	records := sink.decoded(t)
	if len(records) != 2 || records[1]["status"] != float64(500) || records[1]["reason"] != "panic" {
		t.Fatalf("pre-write panic audit = %#v", records)
	}

	sink.reset()
	response, err = http.Get("http://" + address + "/packer/after")
	if err != nil {
		t.Fatalf("post-commit panic did not return its committed headers: %v", err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted || string(body) != "partial" {
		t.Fatalf("post-commit panic = %d %q, want committed 202 partial", response.StatusCode, body)
	}
	if readErr == nil {
		t.Fatal("post-commit panic completed the response stream instead of aborting it")
	}
	records = sink.decoded(t)
	if len(records) != 2 || records[1]["status"] != float64(202) || records[1]["bytes"] != float64(len("partial")) || records[1]["reason"] != "panic_after_write" {
		t.Fatalf("post-commit panic audit = %#v", records)
	}
}

func startRealServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := newHTTPServer(listener.Addr().String(), handler)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve: %v", err)
		}
	})
	return listener.Addr().String()
}

type readerOnly struct{ io.Reader }

func TestComposerObserverPreservesSupportedWriterCapabilities(t *testing.T) {
	underlying := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			t.Fatal("observer has no Unwrap")
		}
		if unwrapper.Unwrap() == nil {
			t.Fatal("observer unwraps to nil")
		}
		readerFrom, ok := w.(io.ReaderFrom)
		if !ok {
			t.Fatal("observer has no ReadFrom")
		}
		if _, err := readerFrom.ReadFrom(readerOnly{strings.NewReader("streamed")}); err != nil {
			t.Fatalf("ReadFrom: %v", err)
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("observer has no Flush")
		}
		flusher.Flush()
	})
	handler, sink := testComposer(t, underlying, underlying, underlying, underlying)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/packer/stream", nil))
	if response.Body.String() != "streamed" {
		t.Fatalf("streamed body = %q", response.Body.String())
	}
	records := sink.decoded(t)
	if records[1]["bytes"] != float64(len("streamed")) {
		t.Fatalf("observed bytes = %v, want %d", records[1]["bytes"], len("streamed"))
	}
}

func TestComposerDoesNotExposeUnsupportedWriterCapabilities(t *testing.T) {
	plane := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Hijacker); ok {
			t.Fatal("audit observer unexpectedly exposes Hijack")
		}
		if _, ok := w.(http.Pusher); ok {
			t.Fatal("audit observer unexpectedly exposes HTTP/2 Push")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler, _ := testComposer(t, plane, plane, plane, plane)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/packer/route", nil))
}

func TestRegisteredRoutesDoNotCallUnsupportedWriterCapabilities(t *testing.T) {
	roots := []string{"../../internal/compat", "../../internal/platform", "../../web"}
	fileSet := token.NewFileSet()
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fileSet, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if ok && (selector.Sel.Name == "Hijack" || selector.Sel.Name == "Push") {
					t.Errorf("registered route source %s calls unsupported ResponseWriter.%s", path, selector.Sel.Name)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("inspect registered route source %s: %v", root, err)
		}
	}
}

func TestComposerCorrelationIsTheOneReturnedByAHandler(t *testing.T) {
	plane := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, audit.CorrelationID(r.Context()))
	})
	handler, sink := testComposer(t, plane, plane, plane, plane)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/packer/failure", nil))
	records := sink.decoded(t)
	if got, audited := response.Body.String(), records[1]["correlation_id"]; got != audited {
		t.Fatalf("response correlation = %q, audited correlation = %v", got, audited)
	}
}

func TestResponseReasonsCoverHandlerlessPaths(t *testing.T) {
	methodMux := http.NewServeMux()
	methodMux.HandleFunc("GET /api/v1/only", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	methodMux.HandleFunc("GET /api/v1/handled-not-found", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	other := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler, sink := testComposer(t, other, other, other, methodMux)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1", nil))
	records := sink.decoded(t)
	if records[1]["reason"] != "not_found" {
		t.Fatalf("404 reason = %v, want not_found", records[1]["reason"])
	}

	sink.reset()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/only", nil))
	records = sink.decoded(t)
	if records[1]["reason"] != "method_not_allowed" {
		t.Fatalf("405 reason = %v, want method_not_allowed", records[1]["reason"])
	}

	sink.reset()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/handled-not-found", nil))
	records = sink.decoded(t)
	if _, exists := records[1]["reason"]; exists {
		t.Fatalf("handler-produced 404 received seam-owned reason: %#v", records[1])
	}
}

func TestAuditRequestCarriesOnlyPreAuthenticationFacts(t *testing.T) {
	plane := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler, sink := testComposer(t, plane, plane, plane, plane)
	request := httptest.NewRequest(http.MethodPatch, "/packer/facts", nil)
	request.RemoteAddr = "192.0.2.4:8123"
	request.Header.Set("X-Forwarded-For", "client supplied, verbatim")
	request.Header.Set("User-Agent", "audit-test/1.0")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	record := sink.decoded(t)[0]
	want := map[string]any{
		"schema_version": float64(2), "kind": "request", "method": http.MethodPatch,
		"path": "/packer/facts", "remote_addr": "192.0.2.4:8123",
		"forwarded_for": "client supplied, verbatim", "user_agent": "audit-test/1.0",
		"identity_kind": "anonymous",
	}
	for field, value := range want {
		if record[field] != value {
			t.Fatalf("request %s = %v, want %v", field, record[field], value)
		}
	}
	for _, semantic := range []string{"principal_id", "scope", "organization_id", "project_id", "operation", "target_type", "target_id", "outcome", "reason", "status", "bytes"} {
		if _, exists := record[semantic]; exists {
			t.Fatalf("pre-auth request invented semantic field %q: %#v", semantic, record)
		}
	}
}

func TestAuditHMACsAuthorizationWithItsOwnVersionedKey(t *testing.T) {
	plane := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler, sink := testComposer(t, plane, plane, plane, plane)
	const authorization = "Bearer live-credential-material"
	request := httptest.NewRequest(http.MethodGet, "/packer/facts", nil)
	request.Header.Set("Authorization", authorization)
	handler.ServeHTTP(httptest.NewRecorder(), request)

	records := sink.decoded(t)
	if len(records) != 2 {
		t.Fatalf("records = %d, want pair", len(records))
	}
	for _, record := range records {
		if record["hmac_key_version"] != testAuditHMACKeyVersion {
			t.Fatalf("hmac_key_version = %v; record %#v", record["hmac_key_version"], record)
		}
	}
	want := hmacHex(testAuditHMACKey, authorization)
	if records[0]["authorization_hmac"] != want {
		t.Fatalf("authorization_hmac = %v, want %s", records[0]["authorization_hmac"], want)
	}
	if strings.Contains(string(sink.records[0]), authorization) || strings.Contains(string(sink.records[1]), authorization) {
		t.Fatalf("raw authorization reached audit pair: %q", sink.records)
	}
	for _, absent := range []string{"authorization_hmac", "client_secret_hmac", "access_token_hmac", "bootstrap_secret_hmac"} {
		if _, ok := records[1][absent]; ok {
			t.Fatalf("response invented %s: %#v", absent, records[1])
		}
	}
	signingKey := []byte("token-signing-key-that-is-deliberately-distinct")
	if want == hmacHex(signingKey, authorization) {
		t.Fatal("audit correlation was derived with the token signing key")
	}
}

func TestAuditHMACConfigurationIsSeparateAndOptionalUntilActivation(t *testing.T) {
	t.Setenv("DFBG_TOKEN_SIGNING_KEY", "token-signing-key-that-is-deliberately-distinct")
	t.Setenv("DFBG_AUDIT_HMAC_KEY", "audit-key-from-its-own-environment-variable")
	t.Setenv("DFBG_AUDIT_HMAC_KEY_VERSION", "2026-08")
	version, key := auditHMACConfiguration()
	if version != "2026-08" || string(key) != "audit-key-from-its-own-environment-variable" {
		t.Fatalf("audit HMAC config = %q/%q", version, key)
	}
	if string(key) == os.Getenv("DFBG_TOKEN_SIGNING_KEY") {
		t.Fatal("audit HMAC key reused DFBG_TOKEN_SIGNING_KEY")
	}

	t.Setenv("DFBG_AUDIT_HMAC_KEY_VERSION", "")
	if version, key := auditHMACConfiguration(); version != "" || key != nil {
		t.Fatalf("partial optional HMAC config activated as %q/%q", version, key)
	}
}

type stubAuditTargetLoader struct {
	targets []identity.AuditTarget
	err     error
}

func (s stubAuditTargetLoader) ListAuditTargets(context.Context) ([]identity.AuditTarget, error) {
	return append([]identity.AuditTarget(nil), s.targets...), s.err
}

func TestAuditInitializationLoadsConfiguredTargetsIntoBroker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	broker, err := initializeAudit(
		context.Background(),
		stubAuditTargetLoader{targets: []identity.AuditTarget{{ID: "target-1", Path: path}}},
		slog.Default(), "test-v1", []byte("audit-hmac-key"),
	)
	if err != nil {
		t.Fatalf("initialize configured audit: %v", err)
	}
	if !broker.Enabled() || len(broker.Health()) != 1 || broker.Health()[0].ID != "target-1" {
		t.Fatalf("startup broker health = %+v, want configured target", broker.Health())
	}
	if err := broker.Write([]byte("startup-loaded\n")); err != nil {
		t.Fatalf("write through startup-loaded broker: %v", err)
	}
	if err := broker.Close(context.Background()); err != nil {
		t.Fatalf("close startup broker: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "startup-loaded\n" {
		t.Fatalf("startup-loaded target = %q, err=%v", got, err)
	}
}

func TestAuditInitializationRefusesConfiguredTargetWithoutHMACKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	broker, err := initializeAudit(
		context.Background(),
		stubAuditTargetLoader{targets: []identity.AuditTarget{{ID: "target-1", Path: path}}},
		slog.Default(), "", nil,
	)
	if broker != nil || err == nil || err.Error() != "DFBG_AUDIT_HMAC_KEY and DFBG_AUDIT_HMAC_KEY_VERSION are required when audit is configured" {
		t.Fatalf("configured keyless audit = broker %v, error %v", broker, err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("keyless initialization opened target before refusing: %v", statErr)
	}
}

func TestAuditInitializationRefusesUnwritableConfiguredTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("make configured target parent unsafe: %v", err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	broker, err := initializeAudit(
		context.Background(),
		stubAuditTargetLoader{targets: []identity.AuditTarget{{
			ID: "target-1", Path: filepath.Join(dir, "audit.log"),
		}}},
		slog.Default(), "test-v1", []byte("audit-hmac-key"),
	)
	if broker != nil || err == nil || !strings.Contains(err.Error(), "world-writable") {
		t.Fatalf("unsafe configured target = broker %v, error %v", broker, err)
	}
}

func TestAuditInitializationRefusesUnreadableDatabase(t *testing.T) {
	want := errors.New("database offline")
	broker, err := initializeAudit(
		context.Background(), stubAuditTargetLoader{err: want}, slog.Default(), "", nil,
	)
	if broker != nil || !errors.Is(err, want) {
		t.Fatalf("unreadable audit configuration = broker %v, error %v", broker, err)
	}
}

func TestShutdownGracePeriodIsProcessConfiguration(t *testing.T) {
	t.Setenv("DFBG_SHUTDOWN_GRACE_PERIOD", "750ms")
	if got, err := configuredShutdownGracePeriod(); err != nil || got != 750*time.Millisecond {
		t.Fatalf("configured shutdown grace = %v, %v", got, err)
	}
	t.Setenv("DFBG_SHUTDOWN_GRACE_PERIOD", "0s")
	if _, err := configuredShutdownGracePeriod(); err == nil {
		t.Fatal("zero shutdown grace period was accepted")
	}
}

type deadlineSink struct {
	deadline chan time.Time
}

func (s *deadlineSink) Write([]byte) error { return nil }
func (s *deadlineSink) Reopen() error      { return nil }
func (s *deadlineSink) Measure() (audit.SinkMeasurement, error) {
	return audit.SinkMeasurement{}, nil
}
func (s *deadlineSink) Close(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("audit close received no deadline")
	}
	s.deadline <- deadline
	return nil
}

func TestShutdownSharesOneDeadlineWithHTTPAndAudit(t *testing.T) {
	sink := &deadlineSink{deadline: make(chan time.Time, 1)}
	broker, err := audit.NewBroker(slog.Default(), audit.Target{ID: "deadline", Sink: sink})
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := newHTTPServer(listener.Addr().String(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()

	deadline := time.Now().Add(time.Second).Round(0)
	metricsListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("metrics listen: %v", err)
	}
	metricsServer := newHTTPServer(metricsListener.Addr().String(), http.NotFoundHandler())
	metricsServed := make(chan error, 1)
	go func() { metricsServed <- metricsServer.Serve(metricsListener) }()

	if err := shutdown(server, metricsServer, broker, deadline); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if got := <-sink.deadline; !got.Equal(deadline) {
		t.Fatalf("audit deadline = %v, want HTTP deadline %v", got, deadline)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve after shutdown = %v, want http.ErrServerClosed", err)
	}
	if err := <-metricsServed; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("metrics serve after shutdown = %v, want http.ErrServerClosed", err)
	}
}

func hmacHex(key []byte, value string) string {
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write([]byte(value))
	return hex.EncodeToString(digest.Sum(nil))
}
