package scan

import (
	"embed"
	"io"
	"net/http"
	"sync"
)

//go:embed testdata/osv/*.json
var osvStubFixtureFiles embed.FS

// OSVStubRoute identifies one request the recorded OSV stub recognises. POST
// bodies are matched exactly so an adapter wire-shape change fails loudly.
type OSVStubRoute struct {
	Method string
	Path   string
	Body   string
}

// OSVStubRequest is one request observed by OSVStub.
type OSVStubRequest struct {
	Method string
	Path   string
	Body   []byte
	Status int
}

// OSVStub serves the recorded api.osv.dev captures without decoding or
// re-encoding their response bodies.
type OSVStub struct {
	mu        sync.Mutex
	responses map[OSVStubRoute][]byte
	requests  []OSVStubRequest
}

var osvStubRecordedRoutes = map[OSVStubRoute]string{
	{Method: http.MethodPost, Path: "/v1/querybatch", Body: `{"queries":[{"package":{"name":"busybox","ecosystem":"Alpine:v3.20"},"version":"1.36.1-r0"}]}`}:            "querybatch-alpine-vulnerable.json",
	{Method: http.MethodPost, Path: "/v1/querybatch", Body: `{"queries":[{"package":{"name":"busybox","ecosystem":"Alpine:v3.20"},"version":"1.36.1-r31"}]}`}:           "querybatch-alpine-patched.json",
	{Method: http.MethodPost, Path: "/v1/querybatch", Body: `{"queries":[{"package":{"name":"glibc","ecosystem":"Debian:12"},"version":"2.36-9+deb12u1"}]}`}:            "querybatch-debian-vulnerable.json",
	{Method: http.MethodPost, Path: "/v1/querybatch", Body: `{"queries":[{"package":{"name":"glibc","ecosystem":"Debian:12"},"version":"2.36-9+deb12u3"}]}`}:            "querybatch-debian-patched.json",
	{Method: http.MethodPost, Path: "/v1/querybatch", Body: `{"queries":[{"package":{"name":"openssl","ecosystem":"Red Hat"},"version":"1:1.1.1k-7.el8"}]}`}:            "querybatch-redhat-vulnerable.json",
	{Method: http.MethodPost, Path: "/v1/querybatch", Body: `{"queries":[{"package":{"name":"openssl","ecosystem":"Red Hat"},"version":"1:1.1.1k-12.el8_9"}]}`}:         "querybatch-redhat-patched.json",
	{Method: http.MethodPost, Path: "/v1/querybatch", Body: `{"queries":[{"package":{"name":"github.com/go-jose/go-jose/v4","ecosystem":"Go"},"version":"v4.1.1"}]}`}:   "querybatch-go-vulnerable.json",
	{Method: http.MethodGet, Path: "/v1/vulns/ALPINE-CVE-2022-48174"}:                                                                                                   "detail-ALPINE-CVE-2022-48174.json",
	{Method: http.MethodGet, Path: "/v1/vulns/DEBIAN-CVE-2023-4527"}:                                                                                                    "detail-DEBIAN-CVE-2023-4527.json",
	{Method: http.MethodGet, Path: "/v1/vulns/GHSA-78h2-9frx-2jm8"}:                                                                                                     "detail-GHSA-78h2-9frx-2jm8.json",
	{Method: http.MethodGet, Path: "/v1/vulns/GO-2026-4945"}:                                                                                                            "detail-GO-2026-4945.json",
	{Method: http.MethodGet, Path: "/v1/vulns/RHBA-2025:6314"}:                                                                                                          "detail-RHBA-2025-6314.json",
	{Method: http.MethodGet, Path: "/v1/vulns/RHSA-2023:7877"}:                                                                                                          "detail-RHSA-2023-7877.json",
	{Method: http.MethodPost, Path: "/v1/query", Body: `{"package":{"name":"openssl","ecosystem":"Red Hat:enterprise_linux:8::baseos"},"version":"1:1.1.1k-7.el8"}`}:    "query-redhat-baseos-vulnerable.json",
	{Method: http.MethodPost, Path: "/v1/query", Body: `{"package":{"name":"openssl","ecosystem":"Red Hat:enterprise_linux:8::baseos"},"version":"1:1.1.1k-12.el8_9"}`}: "query-redhat-baseos-patched.json",
}

// RecordedOSVStubFixtures returns an independent copy of the recorded response
// set. Callers may mutate the copy to exercise failure modes.
func RecordedOSVStubFixtures() map[OSVStubRoute][]byte {
	responses := make(map[OSVStubRoute][]byte, len(osvStubRecordedRoutes))
	for route, name := range osvStubRecordedRoutes {
		body, err := osvStubFixtureFiles.ReadFile("testdata/osv/" + name)
		if err != nil {
			panic(err)
		}
		responses[route] = append([]byte(nil), body...)
	}
	return responses
}

// NewOSVStub constructs a handler from responses. A nil response map selects
// the complete recorded fixture set.
func NewOSVStub(responses map[OSVStubRoute][]byte) *OSVStub {
	if responses == nil {
		responses = RecordedOSVStubFixtures()
	}
	owned := make(map[OSVStubRoute][]byte, len(responses))
	for route, body := range responses {
		owned[route] = append([]byte(nil), body...)
	}
	return &OSVStub{responses: owned}
}

// Requests returns a snapshot of every request received by the stub.
func (s *OSVStub) Requests() []OSVStubRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	requests := make([]OSVStubRequest, len(s.requests))
	for i, request := range s.requests {
		requests[i] = request
		requests[i].Body = append([]byte(nil), request.Body...)
	}
	return requests
}

func (s *OSVStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body []byte
	var err error
	if r.Method == http.MethodPost {
		body, err = io.ReadAll(r.Body)
	}
	route := OSVStubRoute{Method: r.Method, Path: r.URL.Path, Body: string(body)}

	s.mu.Lock()
	response, recognised := s.responses[route]
	status := http.StatusOK
	if err != nil {
		status = http.StatusBadRequest
	} else if !recognised {
		status = http.StatusNotFound
	}
	s.requests = append(s.requests, OSVStubRequest{
		Method: r.Method, Path: r.URL.Path, Body: append([]byte(nil), body...), Status: status,
	})
	s.mu.Unlock()

	if err != nil {
		http.Error(w, "osv stub: reading request body: "+err.Error(), status)
		return
	}
	if !recognised {
		http.Error(w, "osv stub: unrecognised "+r.Method+" "+r.URL.Path, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(response)
}
