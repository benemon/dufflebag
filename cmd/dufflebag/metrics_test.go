package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type staticOperationResolver struct {
	operation identity.AuditOperation
}

func (r staticOperationResolver) Resolve(*http.Request) audit.Descriptor {
	return audit.Descriptor{Operation: r.operation}
}

func TestHTTPMetricsRecordOperationStatusAndDuration(t *testing.T) {
	tests := []struct {
		name, operation string
	}{
		{name: "matched route", operation: "principal.create"},
		{name: "unmatched route", operation: "unmatched"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := prometheus.NewRegistry()
			metrics := newHTTPMetrics(registry)
			resolvedOperation := identity.AuditOperation(test.operation)
			if test.operation == "unmatched" {
				resolvedOperation = ""
			}
			handler := metrics.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusTeapot)
			}), staticOperationResolver{operation: resolvedOperation})
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/raw/path", nil))

			if got := testutil.ToFloat64(metrics.requests.WithLabelValues(test.operation, "418")); got != 1 {
				t.Fatalf("request counter = %v, want 1", got)
			}
			families, err := registry.Gather()
			if err != nil {
				t.Fatal(err)
			}
			var histogramCount uint64
			for _, family := range families {
				if family.GetName() != "dufflebag_http_request_duration_seconds" {
					continue
				}
				for _, metric := range family.Metric {
					for _, label := range metric.Label {
						if label.GetName() == "operation" && label.GetValue() == test.operation {
							histogramCount = metric.GetHistogram().GetSampleCount()
						}
					}
				}
			}
			if histogramCount != 1 {
				t.Fatalf("duration histogram count = %d, want 1", histogramCount)
			}
		})
	}
}

func TestMetricsScrapeExposesRequestsFromWrappedHandler(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := newHTTPMetrics(registry)
	plane := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	applicationHandler, _ := testComposer(t, plane, plane, plane, plane)
	application := httptest.NewServer(instrumentServingHandler(applicationHandler, metrics))
	defer application.Close()

	response, err := http.Get(application.URL + "/packer/buckets/example")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	t.Setenv("DFBG_METRICS_ADDR", "127.0.0.1:0")
	configured := metricsServerFromEnvironment(registry)
	metricsServer := httptest.NewServer(configured.Handler)
	defer metricsServer.Close()
	scrape, err := http.Get(metricsServer.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(scrape.Body)
	_ = scrape.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	exposition := string(body)
	for _, want := range []string{
		`dufflebag_http_requests_total{code="202",operation="packer.request"} 1`,
		`dufflebag_http_request_duration_seconds_count{operation="packer.request"} 1`,
		`dufflebag_http_requests_in_flight 0`,
	} {
		if !strings.Contains(exposition, want) {
			t.Errorf("metrics scrape missing %q:\n%s", want, exposition)
		}
	}
}

func TestMainMuxDoesNotServeMetrics(t *testing.T) {
	plane := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	handler, _ := testComposer(t, plane, plane, plane, plane)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("main mux GET /metrics = %d, want 404", response.Code)
	}
}

func TestMetricsServerConfiguration(t *testing.T) {
	t.Setenv("DFBG_METRICS_ADDR", "")
	if server := metricsServerFromEnvironment(prometheus.NewRegistry()); server != nil {
		t.Fatalf("unset DFBG_METRICS_ADDR created server %#v", server)
	}

	t.Setenv("DFBG_METRICS_ADDR", "127.0.0.1:9090")
	server := metricsServerFromEnvironment(prometheus.NewRegistry())
	if server == nil || server.Addr != "127.0.0.1:9090" {
		t.Fatalf("configured metrics server = %#v", server)
	}
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("metrics POST = %d, want 405", response.Code)
	}
}
