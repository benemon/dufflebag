package main

import (
	"context"
	"net/http"
	"os"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type httpMetrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inFlight prometheus.Gauge
}

func newHTTPMetrics(registerer prometheus.Registerer) *httpMetrics {
	metrics := &httpMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dufflebag_http_requests_total",
			Help: "Total number of HTTP requests handled.",
		}, []string{"operation", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "dufflebag_http_request_duration_seconds",
			Help: "Duration of HTTP requests in seconds.",
		}, []string{"operation"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dufflebag_http_requests_in_flight",
			Help: "Number of HTTP requests currently being handled.",
		}),
	}
	registerer.MustRegister(metrics.requests, metrics.duration, metrics.inFlight)
	return metrics
}

type metricsOperationContextKey struct{}

func instrumentServingHandler(application *composedHandler, metrics *httpMetrics) http.Handler {
	return metrics.Wrap(application, application)
}

func (m *httpMetrics) Wrap(next http.Handler, resolver audit.Resolver) http.Handler {
	operationLabel := promhttp.WithLabelFromCtx("operation", func(ctx context.Context) string {
		return ctx.Value(metricsOperationContextKey{}).(string)
	})
	instrumented := promhttp.InstrumentHandlerInFlight(
		m.inFlight,
		promhttp.InstrumentHandlerDuration(
			m.duration,
			promhttp.InstrumentHandlerCounter(m.requests, next, operationLabel),
			operationLabel,
		),
	)
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		operation := string(resolver.Resolve(request).Operation)
		if operation == "" {
			operation = "unmatched"
		}
		ctx := context.WithValue(request.Context(), metricsOperationContextKey{}, operation)
		instrumented.ServeHTTP(w, request.WithContext(ctx))
	})
}

func metricsServerFromEnvironment(gatherer prometheus.Gatherer) *http.Server {
	address := os.Getenv("DFBG_METRICS_ADDR")
	if address == "" {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
	return newHTTPServer(address, mux)
}
