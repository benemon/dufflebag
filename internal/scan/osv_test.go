package scan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testClock returns a deterministic clock advancing one second per call.
func testClock() func() time.Time {
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	n := 0
	return func() time.Time {
		n++
		return base.Add(time.Duration(n) * time.Second)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "osv", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

// rawResults builds a querybatch results array from literal JSON entries.
func rawResults(entries ...string) []json.RawMessage {
	out := make([]json.RawMessage, len(entries))
	for i, e := range entries {
		out[i] = json.RawMessage(e)
	}
	return out
}

// batchBody synthesizes a querybatch response body from literal result
// entries.
func batchBody(t *testing.T, entries ...string) []byte {
	t.Helper()
	b, err := json.Marshal(osvBatchResponse{Results: rawResults(entries...)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// singleVulnBatch derives a one-candidate batch response by mutating a live
// capture down to the named advisory — the minimal synthesis the fixture
// provenance rules allow.
func singleVulnBatch(t *testing.T, capture, keep string) []byte {
	t.Helper()
	var decoded struct {
		Results []osvBatchResult `json:"results"`
	}
	if err := json.Unmarshal(readFixture(t, capture), &decoded); err != nil {
		t.Fatalf("decoding %s: %v", capture, err)
	}
	if len(decoded.Results) != 1 {
		t.Fatalf("%s carries %d results, expected 1", capture, len(decoded.Results))
	}
	var kept []osvRef
	for _, v := range decoded.Results[0].Vulns {
		if v.ID == keep {
			kept = append(kept, v)
		}
	}
	if len(kept) != 1 {
		t.Fatalf("%s does not contain %s", capture, keep)
	}
	entry, err := json.Marshal(osvBatchResult{Vulns: kept})
	if err != nil {
		t.Fatal(err)
	}
	return batchBody(t, string(entry))
}

// stubOSV serves canned bodies and records every request for assertions.
type stubOSV struct {
	t  *testing.T
	mu sync.Mutex
	// batches are served in call order; a call beyond the list fails the test.
	batches []stubResponse
	// details maps advisory id to its response; status 0 means 200.
	details map[string]stubResponse
	// queries maps "ecosystem|name|version" to a phase-2 response.
	queries map[string]stubResponse
	delay   time.Duration

	batchBodies [][]byte
	queryKeys   []string
}

type stubResponse struct {
	status int
	body   []byte
}

func fixtureResponse(body []byte) stubResponse { return stubResponse{body: body} }

func (s *stubOSV) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.delay > 0 {
			time.Sleep(s.delay)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/querybatch":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				s.t.Errorf("reading querybatch body: %v", err)
			}
			s.batchBodies = append(s.batchBodies, body)
			if len(s.batchBodies) > len(s.batches) {
				s.t.Errorf("unexpected querybatch call %d", len(s.batchBodies))
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			s.write(w, s.batches[len(s.batchBodies)-1])
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/vulns/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/vulns/")
			resp, ok := s.details[id]
			if !ok {
				s.t.Errorf("unexpected detail fetch %q", id)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			s.write(w, resp)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/query":
			var q osvQuery
			if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
				s.t.Errorf("decoding /v1/query body: %v", err)
			}
			key := q.Package.Ecosystem + "|" + q.Package.Name + "|" + q.Version
			s.queryKeys = append(s.queryKeys, key)
			resp, ok := s.queries[key]
			if !ok {
				s.t.Errorf("unexpected confirmation query %q", key)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			s.write(w, resp)
		default:
			s.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func (s *stubOSV) write(w http.ResponseWriter, resp stubResponse) {
	if resp.status != 0 {
		w.WriteHeader(resp.status)
	}
	if len(resp.body) > 0 {
		if _, err := w.Write(resp.body); err != nil {
			s.t.Errorf("writing response: %v", err)
		}
	}
}

func scanOne(t *testing.T, stub *stubOSV, packages ...Package) (Result, error) {
	t.Helper()
	srv := stub.server()
	defer srv.Close()
	o := NewOSV(srv.URL, srv.Client(), testClock())
	return o.Scan(context.Background(), Inventory{Packages: packages})
}

const alpineVulnerablePurl = "pkg:apk/alpine/busybox@1.36.1-r0?arch=aarch64&distro=alpine-3.20.10"

func TestScanAlpineVulnerableControl(t *testing.T) {
	stub := &stubOSV{
		t:       t,
		batches: []stubResponse{fixtureResponse(singleVulnBatch(t, "querybatch-alpine-vulnerable.json", "ALPINE-CVE-2022-48174"))},
		details: map[string]stubResponse{
			"ALPINE-CVE-2022-48174": fixtureResponse(readFixture(t, "detail-ALPINE-CVE-2022-48174.json")),
		},
	}
	res, err := scanOne(t, stub, Package{
		SBOMID:  "sbom-1",
		Name:    "decoy-name",
		Version: "9.9.9-decoy",
		Purl:    alpineVulnerablePurl,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Coverage.Submitted != 1 {
		t.Fatalf("coverage = %+v, want 1 submitted", res.Coverage)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(res.Findings))
	}
	f := res.Findings[0]
	if f.ID != "ALPINE-CVE-2022-48174" {
		t.Errorf("finding id = %q", f.ID)
	}
	if len(f.FixedVersions) != 1 || f.FixedVersions[0] != "1.36.1-r2" {
		t.Errorf("fixed versions = %v, want [1.36.1-r2] (the Alpine:v3.20 stream's fix)", f.FixedVersions)
	}
	if f.Severity != SeverityCritical {
		t.Errorf("derived severity = %q, want critical (CVSS 9.8)", f.Severity)
	}
	if len(f.Severities) == 0 || f.Severities[0].Source != "osv" || !strings.HasPrefix(f.Severities[0].Value, "CVSS:3.1/") {
		t.Errorf("verbatim severities = %+v, want the provider CVSS vector first", f.Severities)
	}
	if f.Package.Name != "decoy-name" || f.Package.SBOMID != "sbom-1" {
		t.Errorf("finding package identity = %+v, want the full inventory identity", f.Package)
	}
	// Only purl-derived data may reach the wire: the inventory name and
	// version must not appear in any request body.
	for _, body := range stub.batchBodies {
		if strings.Contains(string(body), "decoy") {
			t.Errorf("request body leaked inventory data: %s", body)
		}
		if !strings.Contains(string(body), "Alpine:v3.20") || !strings.Contains(string(body), "busybox") {
			t.Errorf("request body missing purl-derived query: %s", body)
		}
	}
	if res.Transcript.Digest() == "" || len(res.Transcript.Records) != 2 {
		t.Errorf("transcript = %d records, want batch + detail", len(res.Transcript.Records))
	}
}

func TestScanAlpinePatchedControl(t *testing.T) {
	stub := &stubOSV{
		t:       t,
		batches: []stubResponse{fixtureResponse(readFixture(t, "querybatch-alpine-patched.json"))},
		details: map[string]stubResponse{},
	}
	res, err := scanOne(t, stub, Package{SBOMID: "sbom-1", Name: "busybox", Version: "1.36.1-r31",
		Purl: "pkg:apk/alpine/busybox@1.36.1-r31?arch=aarch64&distro=alpine-3.20.10"})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Findings) != 0 || res.Coverage.Submitted != 1 {
		t.Fatalf("patched control: findings=%d coverage=%+v, want zero findings from one submitted query", len(res.Findings), res.Coverage)
	}
}

func TestScanUnsupportedDistinguishableFromZeroFindings(t *testing.T) {
	stub := &stubOSV{
		t:       t,
		batches: []stubResponse{fixtureResponse(readFixture(t, "querybatch-alpine-patched.json"))},
		details: map[string]stubResponse{},
	}
	res, err := scanOne(t, stub,
		Package{SBOMID: "s", Name: "openssl", Version: "1.0.2k", Purl: "pkg:rpm/amzn/openssl@1.0.2k-24.amzn2.0.7"},
		Package{SBOMID: "s", Name: "busybox", Version: "1.36.1-r31", Purl: "pkg:apk/alpine/busybox@1.36.1-r31?distro=alpine-3.20.10"},
	)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Coverage.Unsupported != 1 || res.Coverage.Submitted != 1 {
		t.Fatalf("coverage = %+v, want unsupported=1 submitted=1", res.Coverage)
	}
	var batch struct {
		Queries []osvQuery `json:"queries"`
	}
	if err := json.Unmarshal(stub.batchBodies[0], &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Queries) != 1 {
		t.Fatalf("wire carried %d queries, want 1: the unsupported purl must never be submitted", len(batch.Queries))
	}
}

func TestScanDebianControlKeepsUrgencyVerbatim(t *testing.T) {
	stub := &stubOSV{
		t:       t,
		batches: []stubResponse{fixtureResponse(singleVulnBatch(t, "querybatch-debian-vulnerable.json", "DEBIAN-CVE-2023-4527"))},
		details: map[string]stubResponse{
			"DEBIAN-CVE-2023-4527": fixtureResponse(readFixture(t, "detail-DEBIAN-CVE-2023-4527.json")),
		},
	}
	res, err := scanOne(t, stub, Package{SBOMID: "s", Name: "libc6", Version: "2.36-9+deb12u1",
		Purl: "pkg:deb/debian/libc6@2.36-9+deb12u1?arch=arm64&distro=debian-12.15&upstream=glibc"})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(res.Findings))
	}
	f := res.Findings[0]
	if len(f.FixedVersions) != 1 || f.FixedVersions[0] != "2.36-9+deb12u3" {
		t.Errorf("fixed = %v, want [2.36-9+deb12u3]", f.FixedVersions)
	}
	var sawURgency, sawCVSS bool
	for _, sv := range f.Severities {
		if sv.Source == "osv:ecosystem_specific" && sv.Type == "urgency" && sv.Value == "not yet assigned" {
			sawURgency = true
		}
		if sv.Source == "osv" && strings.HasPrefix(sv.Value, "CVSS:3.1/") {
			sawCVSS = true
		}
	}
	if !sawURgency || !sawCVSS {
		t.Errorf("severities = %+v, want verbatim urgency and CVSS vector", f.Severities)
	}
	if f.Severity != SeverityMedium {
		t.Errorf("derived severity = %q, want medium (CVSS 6.5; urgency 'not yet assigned' contributes nothing)", f.Severity)
	}
}

const redhatVulnerablePurl = "pkg:rpm/redhat/openssl@1.1.1k-7.el8?arch=x86_64&distro=rhel-8.5&epoch=1&upstream=openssl-1.1.1k-7.el8.src.rpm"

func TestScanRedHatTwoPhaseConfirmed(t *testing.T) {
	batch := batchBody(t, `{"vulns":[{"id":"RHSA-2023:7877"}]}`)
	stub := &stubOSV{
		t:       t,
		batches: []stubResponse{fixtureResponse(batch)},
		details: map[string]stubResponse{
			"RHSA-2023:7877": fixtureResponse(readFixture(t, "detail-RHSA-2023-7877.json")),
		},
		queries: map[string]stubResponse{
			"Red Hat:enterprise_linux:8::baseos|openssl|1:1.1.1k-7.el8": fixtureResponse(readFixture(t, "query-redhat-baseos-vulnerable.json")),
		},
	}
	res, err := scanOne(t, stub, Package{SBOMID: "s", Name: "openssl", Version: "1.1.1k-7.el8", Purl: redhatVulnerablePurl})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d, want 1 (stream-confirmed)", len(res.Findings))
	}
	f := res.Findings[0]
	if f.ID != "RHSA-2023:7877" {
		t.Errorf("finding id = %q", f.ID)
	}
	if len(f.FixedVersions) != 1 || f.FixedVersions[0] != "1:1.1.1k-12.el8_9" {
		t.Errorf("fixed = %v, want [1:1.1.1k-12.el8_9]", f.FixedVersions)
	}
	if len(stub.queryKeys) == 0 {
		t.Error("no confirmation query was issued")
	}
	// Transcript: batch chunk, detail, then the confirmation response.
	if len(res.Transcript.Records) != 3 {
		t.Errorf("transcript = %d records, want 3", len(res.Transcript.Records))
	}
}

func TestScanRedHatCrossMajorCandidateDropped(t *testing.T) {
	batch := batchBody(t, `{"vulns":[{"id":"RHBA-2025:6314"}]}`)
	stub := &stubOSV{
		t:       t,
		batches: []stubResponse{fixtureResponse(batch)},
		details: map[string]stubResponse{
			"RHBA-2025:6314": fixtureResponse(readFixture(t, "detail-RHBA-2025-6314.json")),
		},
		queries: map[string]stubResponse{},
	}
	res, err := scanOne(t, stub, Package{SBOMID: "s", Name: "openssl", Version: "3.2.2-16.el9",
		Purl: "pkg:rpm/redhat/openssl@3.2.2-16.el9?arch=x86_64&distro=rhel-9.8&epoch=1"})
	if err != nil {
		t.Fatalf("cross-stream noise must not error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("findings = %d, want 0: the RHBA-2025:6314 record only names enterprise_linux 10 streams", len(res.Findings))
	}
	if len(stub.queryKeys) != 0 {
		t.Errorf("confirmation queries issued for a record with no matching major: %v", stub.queryKeys)
	}
	if res.Coverage.Submitted != 1 {
		t.Errorf("coverage = %+v", res.Coverage)
	}
}

func TestScanChunksAtOneThousandQueries(t *testing.T) {
	emptyResults := func(n int) stubResponse {
		entries := make([]string, n)
		for i := range entries {
			entries[i] = "{}"
		}
		return fixtureResponse(batchBody(t, entries...))
	}
	stub := &stubOSV{
		t:       t,
		batches: []stubResponse{emptyResults(1000), emptyResults(1)},
		details: map[string]stubResponse{},
	}
	packages := make([]Package, 1001)
	for i := range packages {
		packages[i] = Package{SBOMID: "s", Name: fmt.Sprintf("p%d", i), Version: "1.0.0",
			Purl: fmt.Sprintf("pkg:npm/p%d@1.0.0", i)}
	}
	res, err := scanOne(t, stub, packages...)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(stub.batchBodies) != 2 {
		t.Fatalf("querybatch calls = %d, want 2", len(stub.batchBodies))
	}
	for i, want := range []int{1000, 1} {
		var batch struct {
			Queries []osvQuery `json:"queries"`
		}
		if err := json.Unmarshal(stub.batchBodies[i], &batch); err != nil {
			t.Fatal(err)
		}
		if len(batch.Queries) != want {
			t.Errorf("chunk %d carried %d queries, want %d", i, len(batch.Queries), want)
		}
	}
	if res.Coverage.Submitted != 1001 {
		t.Errorf("submitted = %d", res.Coverage.Submitted)
	}
}

func TestScanFailsClosed(t *testing.T) {
	alpinePkg := Package{SBOMID: "s", Name: "busybox", Version: "1.36.1-r0", Purl: alpineVulnerablePurl}
	singleBatch := func(id string) []byte {
		return batchBody(t, fmt.Sprintf(`{"vulns":[{"id":%q}]}`, id))
	}
	mismatched := func() []byte {
		var record map[string]any
		if err := json.Unmarshal(readFixture(t, "detail-ALPINE-CVE-2022-48174.json"), &record); err != nil {
			t.Fatal(err)
		}
		record["id"] = "SOMETHING-ELSE"
		b, _ := json.Marshal(record)
		return b
	}

	cases := []struct {
		name        string
		stub        *stubOSV
		wantRecords int
	}{
		{
			name: "cardinality mismatch",
			stub: &stubOSV{t: t,
				batches: []stubResponse{fixtureResponse([]byte(`{"results":[]}`))},
				details: map[string]stubResponse{}},
			wantRecords: 1,
		},
		{
			name: "next_page_token",
			stub: &stubOSV{t: t,
				batches: []stubResponse{fixtureResponse([]byte(`{"results":[{"vulns":[],"next_page_token":"x"}]}`))},
				details: map[string]stubResponse{}},
			wantRecords: 1,
		},
		{
			// A failed request's body was still received: the transcript
			// must retain the provider's error payload.
			name: "querybatch failure",
			stub: &stubOSV{t: t,
				batches: []stubResponse{{status: http.StatusInternalServerError, body: []byte(`{"code":13,"message":"boom"}`)}},
				details: map[string]stubResponse{}},
			wantRecords: 1,
		},
		{
			name: "null batch result",
			stub: &stubOSV{t: t,
				batches: []stubResponse{fixtureResponse([]byte(`{"results":[null]}`))},
				details: map[string]stubResponse{}},
			wantRecords: 1,
		},
		{
			name: "detail fetch failure",
			stub: &stubOSV{t: t,
				batches: []stubResponse{fixtureResponse(singleBatch("ALPINE-CVE-2022-48174"))},
				details: map[string]stubResponse{"ALPINE-CVE-2022-48174": {status: http.StatusInternalServerError, body: []byte(`{"code":13}`)}}},
			wantRecords: 2,
		},
		{
			name: "null detail body",
			stub: &stubOSV{t: t,
				batches: []stubResponse{fixtureResponse(singleBatch("ALPINE-CVE-2022-48174"))},
				details: map[string]stubResponse{"ALPINE-CVE-2022-48174": fixtureResponse([]byte(`null`))}},
			wantRecords: 2,
		},
		{
			name: "unmappable detail identity",
			stub: &stubOSV{t: t,
				batches: []stubResponse{fixtureResponse(singleBatch("ALPINE-CVE-2022-48174"))},
				details: map[string]stubResponse{"ALPINE-CVE-2022-48174": fixtureResponse(mismatched())}},
			wantRecords: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := scanOne(t, tc.stub, alpinePkg)
			if err == nil {
				t.Fatal("scan succeeded, want failure")
			}
			if res.Findings != nil {
				t.Errorf("findings = %v, want none on error", res.Findings)
			}
			if len(res.Transcript.Records) != tc.wantRecords {
				t.Errorf("transcript = %d records, want %d retained", len(res.Transcript.Records), tc.wantRecords)
			}
			if res.Attribution.Adapter != "osv" || res.Attribution.DatabaseRevision != "unreported" {
				t.Errorf("attribution = %+v", res.Attribution)
			}
		})
	}
}

// TestScanRedHatPatchedControlDropsUnconfirmed replays the captured patched
// phase-2 response: the stream-scoped query answers without RHSA-2023:7877,
// so a phase-1 candidate for the patched version must not become a finding —
// this is what proves confirmation checks id membership rather than
// accepting every same-major candidate.
func TestScanRedHatPatchedControlDropsUnconfirmed(t *testing.T) {
	batch := batchBody(t, `{"vulns":[{"id":"RHSA-2023:7877"}]}`)
	stub := &stubOSV{
		t:       t,
		batches: []stubResponse{fixtureResponse(batch)},
		details: map[string]stubResponse{
			"RHSA-2023:7877": fixtureResponse(readFixture(t, "detail-RHSA-2023-7877.json")),
		},
		queries: map[string]stubResponse{
			"Red Hat:enterprise_linux:8::baseos|openssl|1:1.1.1k-12.el8_9": fixtureResponse(readFixture(t, "query-redhat-baseos-patched.json")),
		},
	}
	res, err := scanOne(t, stub, Package{SBOMID: "s", Name: "openssl", Version: "1.1.1k-12.el8_9",
		Purl: "pkg:rpm/redhat/openssl@1.1.1k-12.el8_9?arch=x86_64&distro=rhel-8.9&epoch=1"})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("findings = %v, want none: the stream-scoped response does not confirm RHSA-2023:7877 at the patched version", res.Findings)
	}
	if len(stub.queryKeys) != 1 {
		t.Errorf("confirmation queries = %v, want exactly the patched-version stream query", stub.queryKeys)
	}
}

func TestScanNonRedHatUnmappableAffectedFailsRun(t *testing.T) {
	batch := batchBody(t, `{"vulns":[{"id":"SYNTH-OTHER"}]}`)
	stub := &stubOSV{
		t:       t,
		batches: []stubResponse{fixtureResponse(batch)},
		details: map[string]stubResponse{
			// Synthesized: a record whose id matches but whose affected
			// entries name a different package entirely.
			"SYNTH-OTHER": fixtureResponse(synthDetail("SYNTH-OTHER", "some-other-package")),
		},
	}
	res, err := scanOne(t, stub, Package{SBOMID: "s", Name: "foo", Version: "1.0.0", Purl: "pkg:npm/foo@1.0.0"})
	if err == nil {
		t.Fatal("scan succeeded, want unmappable-identity failure")
	}
	if res.Findings != nil {
		t.Error("findings survived an unmappable advisory")
	}
}

func TestScanOversizeResponseFailsRun(t *testing.T) {
	old := osvMaxResponseBytes
	osvMaxResponseBytes = 64
	t.Cleanup(func() { osvMaxResponseBytes = old })
	big := append([]byte(`{"results":[{}]}         `), bytes.Repeat([]byte(" "), 128)...)
	stub := &stubOSV{
		t:       t,
		batches: []stubResponse{fixtureResponse(big)},
		details: map[string]stubResponse{},
	}
	res, err := scanOne(t, stub, Package{SBOMID: "s", Name: "busybox", Version: "1.36.1-r31",
		Purl: "pkg:apk/alpine/busybox@1.36.1-r31?distro=alpine-3.20.10"})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want oversize failure", err)
	}
	if res.Findings != nil {
		t.Error("findings survived an oversize response")
	}
}

func TestScanRedHatConfirmationFailureFailsRun(t *testing.T) {
	batch := batchBody(t, `{"vulns":[{"id":"RHSA-2023:7877"}]}`)
	stub := &stubOSV{
		t:       t,
		batches: []stubResponse{fixtureResponse(batch)},
		details: map[string]stubResponse{
			"RHSA-2023:7877": fixtureResponse(readFixture(t, "detail-RHSA-2023-7877.json")),
		},
		queries: map[string]stubResponse{
			"Red Hat:enterprise_linux:8::baseos|openssl|1:1.1.1k-7.el8": {status: http.StatusInternalServerError},
		},
	}
	res, err := scanOne(t, stub, Package{SBOMID: "s", Name: "openssl", Version: "1.1.1k-7.el8", Purl: redhatVulnerablePurl})
	if err == nil {
		t.Fatal("scan succeeded, want confirmation failure to fail the run")
	}
	if res.Findings != nil {
		t.Error("findings survived a failed run")
	}
	if len(res.Transcript.Records) != 2 {
		t.Errorf("transcript = %d records, want batch + detail retained", len(res.Transcript.Records))
	}
}

func TestScanTimeoutFailsRun(t *testing.T) {
	stub := &stubOSV{
		t:       t,
		batches: []stubResponse{fixtureResponse(readFixture(t, "querybatch-alpine-patched.json"))},
		details: map[string]stubResponse{},
		delay:   200 * time.Millisecond,
	}
	srv := stub.server()
	defer srv.Close()
	o := NewOSV(srv.URL, srv.Client(), testClock())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	res, err := o.Scan(ctx, Inventory{Packages: []Package{{SBOMID: "s", Name: "busybox", Version: "1.36.1-r31",
		Purl: "pkg:apk/alpine/busybox@1.36.1-r31?distro=alpine-3.20.10"}}})
	if err == nil {
		t.Fatal("scan succeeded, want timeout failure")
	}
	if res.Findings != nil {
		t.Error("findings survived a timed-out run")
	}
}

// synthDetail builds a synthesized npm advisory record; synthesized because
// the determinism test needs a fan of concurrent detail fetches, which no
// single live capture provides.
func synthDetail(id, name string) []byte {
	record := map[string]any{
		"id":      id,
		"summary": "synthesized for transcript determinism",
		"affected": []map[string]any{{
			"package": map[string]any{"ecosystem": "npm", "name": name},
			"ranges": []map[string]any{{
				"type":   "SEMVER",
				"events": []map[string]any{{"introduced": "0"}, {"fixed": "2.0.0"}},
			}},
		}},
	}
	b, _ := json.Marshal(record)
	return b
}

func TestTranscriptDeterministicUnderConcurrency(t *testing.T) {
	const packages = 6
	build := func() *stubOSV {
		entries := make([]string, packages)
		details := map[string]stubResponse{}
		for i := 0; i < packages; i++ {
			id := fmt.Sprintf("SYNTH-%d", i)
			entries[i] = fmt.Sprintf(`{"vulns":[{"id":%q}]}`, id)
			details[id] = fixtureResponse(synthDetail(id, fmt.Sprintf("pkg%d", i)))
		}
		return &stubOSV{t: t, batches: []stubResponse{fixtureResponse(batchBody(t, entries...))}, details: details}
	}
	inventory := make([]Package, packages)
	for i := range inventory {
		inventory[i] = Package{SBOMID: "s", Name: fmt.Sprintf("pkg%d", i), Version: "1.0.0",
			Purl: fmt.Sprintf("pkg:npm/pkg%d@1.0.0", i)}
	}

	digests := map[string]bool{}
	var firstRecords [][]byte
	for run := 0; run < 5; run++ {
		res, err := scanOne(t, build(), inventory...)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		digests[res.Transcript.Digest()] = true
		if run == 0 {
			firstRecords = res.Transcript.Records
		}
	}
	if len(digests) != 1 {
		t.Fatalf("digests diverged across identical runs: %v", digests)
	}
	// The canonical order is structural, not accidental: chunk first, then
	// details sorted by advisory id.
	if len(firstRecords) != 1+packages {
		t.Fatalf("transcript = %d records", len(firstRecords))
	}
	for i := 0; i < packages; i++ {
		var record struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(firstRecords[1+i], &record); err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("SYNTH-%d", i); record.ID != want {
			t.Errorf("transcript record %d = %s, want %s (details ordered by id)", 1+i, record.ID, want)
		}
	}
}

func TestTranscriptEncodingAndDigest(t *testing.T) {
	tr := Transcript{Records: [][]byte{[]byte("a"), []byte("bb")}}
	var want []byte
	for _, r := range [][]byte{[]byte("a"), []byte("bb")} {
		var prefix [8]byte
		binary.BigEndian.PutUint64(prefix[:], uint64(len(r)))
		want = append(want, prefix[:]...)
		want = append(want, r...)
	}
	if got := tr.Encode(); string(got) != string(want) {
		t.Fatalf("encode = %x, want %x", got, want)
	}
	sum := sha256.Sum256(want)
	if tr.Digest() != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest mismatch")
	}
	reordered := Transcript{Records: [][]byte{[]byte("bb"), []byte("a")}}
	if reordered.Digest() == tr.Digest() {
		t.Fatal("reordered records produced an identical digest")
	}
}

func TestProbe(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		stub := &stubOSV{t: t, batches: []stubResponse{fixtureResponse([]byte(`{"results":[]}`))}}
		srv := stub.server()
		defer srv.Close()
		o := NewOSV(srv.URL, srv.Client(), testClock())
		h, err := o.Probe(context.Background())
		if err != nil || !h.OK {
			t.Fatalf("probe = %+v, %v", h, err)
		}
		if h.Latency != time.Second {
			t.Errorf("latency = %v, want the injected clock's 1s step", h.Latency)
		}
		if h.ObservedAt.IsZero() {
			t.Error("observed_at unset")
		}
	})
	t.Run("provider failure", func(t *testing.T) {
		stub := &stubOSV{t: t, batches: []stubResponse{{status: http.StatusInternalServerError}}}
		srv := stub.server()
		defer srv.Close()
		o := NewOSV(srv.URL, srv.Client(), testClock())
		h, err := o.Probe(context.Background())
		if err == nil || h.OK {
			t.Fatalf("probe = %+v, want failure", h)
		}
		if h.Detail == "" {
			t.Error("failure carries no detail")
		}
	})
	t.Run("timeout", func(t *testing.T) {
		stub := &stubOSV{t: t, batches: []stubResponse{fixtureResponse([]byte(`{"results":[]}`))}, delay: 200 * time.Millisecond}
		srv := stub.server()
		defer srv.Close()
		o := NewOSV(srv.URL, srv.Client(), testClock())
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		h, err := o.Probe(ctx)
		if err == nil || h.OK {
			t.Fatalf("probe = %+v, want timeout failure", h)
		}
	})
}

func TestOSVStubServesRecordedBodiesByteIdentically(t *testing.T) {
	stub := NewOSVStub(nil)
	server := httptest.NewServer(stub)
	defer server.Close()

	for route, name := range osvStubRecordedRoutes {
		route, name := route, name
		t.Run(name, func(t *testing.T) {
			request, err := http.NewRequest(route.Method, server.URL+route.Path, strings.NewReader(route.Body))
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			got, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, body = %q", response.StatusCode, got)
			}
			want := readFixture(t, name)
			if !bytes.Equal(got, want) {
				t.Fatalf("served response differs from %s", name)
			}
		})
	}
}

func TestOSVStubRejectsAndLogsUnrecognisedRequest(t *testing.T) {
	stub := NewOSVStub(nil)
	server := httptest.NewServer(stub)
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/v1/not-real", "application/json", strings.NewReader(`{"inventory":"must not look clean"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode == http.StatusOK || !strings.Contains(string(body), "unrecognised POST /v1/not-real") {
		t.Fatalf("unknown request response = %d %q, want a loud non-200", response.StatusCode, body)
	}
	requests := stub.Requests()
	if len(requests) != 1 || requests[0].Method != http.MethodPost || requests[0].Path != "/v1/not-real" ||
		string(requests[0].Body) != `{"inventory":"must not look clean"}` || requests[0].Status == http.StatusOK {
		t.Fatalf("request log = %+v, want the rejected request and body", requests)
	}
}

func TestOSVStubStandaloneContainer(t *testing.T) {
	base := os.Getenv("OSV_STUB_ENDPOINT")
	if base == "" {
		t.Skip("OSV_STUB_ENDPOINT is set by make test-scanner")
	}

	var response *http.Response
	var err error
	for range 50 {
		response, err = http.Get(base + "/v1/vulns/GO-2026-4945")
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("standalone stub did not become reachable: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !bytes.Equal(body, readFixture(t, "detail-GO-2026-4945.json")) {
		t.Fatalf("standalone response = %d (%d bytes), want byte-identical GO detail", response.StatusCode, len(body))
	}
}

const osvStubGoldenTranscriptDigest = "90f561dad681cf9d5470da65ab0cc2d556934e95ae2fe9d6acebc6a347ae484c"

// To regenerate the golden deliberately, run this test after reviewing the
// fixture change and copy the reported "got" digest into the constant above.
func TestOSVStubGoldenTranscript(t *testing.T) {
	responses := canonicalOSVStubResponses(t)
	transcript, _, err := runCanonicalOSVStubInteraction(responses)
	if err != nil {
		t.Fatal(err)
	}
	if got := transcript.Digest(); got != osvStubGoldenTranscriptDigest {
		t.Fatalf("golden transcript digest = %s, want %s", got, osvStubGoldenTranscriptDigest)
	}

}

func TestOSVStubRequestLogWireHygiene(t *testing.T) {
	responses := canonicalOSVStubResponses(t)
	_, requests, err := runCanonicalOSVStubInteraction(responses)
	assertCanonicalOSVStubRequests(t, requests)
	if err != nil {
		t.Fatal(err)
	}
}

func TestOSVStubGoldenDetectsMissingResponse(t *testing.T) {
	responses := canonicalOSVStubResponses(t)
	delete(responses, OSVStubRoute{Method: http.MethodGet, Path: "/v1/vulns/RHSA-2023:7877"})
	if err := canonicalOSVStubGoldenError(responses); err == nil {
		t.Fatal("golden assertion passed after deleting the Red Hat detail response")
	}
}

func TestOSVStubGoldenDetectsReorderedResponses(t *testing.T) {
	responses := canonicalOSVStubResponses(t)
	ghsa := OSVStubRoute{Method: http.MethodGet, Path: "/v1/vulns/GHSA-78h2-9frx-2jm8"}
	goAdvisory := OSVStubRoute{Method: http.MethodGet, Path: "/v1/vulns/GO-2026-4945"}
	responses[ghsa], responses[goAdvisory] = responses[goAdvisory], responses[ghsa]
	if err := canonicalOSVStubGoldenError(responses); err == nil {
		t.Fatal("golden assertion passed after swapping two detail responses")
	}
}

func TestOSVStubGoldenDetectsOneByteChange(t *testing.T) {
	responses := canonicalOSVStubResponses(t)
	route := OSVStubRoute{Method: http.MethodGet, Path: "/v1/vulns/GO-2026-4945"}
	body := append([]byte(nil), responses[route]...)
	index := bytes.Index(body, []byte("2026-"))
	if index < 0 {
		t.Fatal("GO detail fixture has no 2026 timestamp to mutate")
	}
	body[index+3] = '5'
	responses[route] = body
	if err := canonicalOSVStubGoldenError(responses); err == nil {
		t.Fatal("golden assertion passed after altering one response byte")
	}
}

func canonicalOSVStubResponses(t *testing.T) map[OSVStubRoute][]byte {
	t.Helper()
	responses := RecordedOSVStubFixtures()
	alpine := OSVStubRoute{Method: http.MethodPost, Path: "/v1/querybatch", Body: `{"queries":[{"package":{"name":"busybox","ecosystem":"Alpine:v3.20"},"version":"1.36.1-r0"}]}`}
	redhat := OSVStubRoute{Method: http.MethodPost, Path: "/v1/querybatch", Body: `{"queries":[{"package":{"name":"openssl","ecosystem":"Red Hat"},"version":"1:1.1.1k-7.el8"}]}`}
	responses[alpine] = singleVulnBatch(t, "querybatch-alpine-vulnerable.json", "ALPINE-CVE-2022-48174")
	responses[redhat] = singleVulnBatch(t, "querybatch-redhat-vulnerable.json", "RHSA-2023:7877")
	return responses
}

func runCanonicalOSVStubInteraction(responses map[OSVStubRoute][]byte) (Transcript, []OSVStubRequest, error) {
	stub := NewOSVStub(responses)
	server := httptest.NewServer(stub)
	defer server.Close()
	adapter := NewOSV(server.URL, server.Client(), testClock())

	inventories := []Inventory{
		{Packages: []Package{{
			SBOMID: "sbom-go", Name: "inventory-go-name", Version: "inventory-go-version",
			Purl: "pkg:golang/github.com/go-jose/go-jose/v4@v4.1.1",
		}}},
		{Packages: []Package{{
			SBOMID: "sbom-redhat", Name: "inventory-redhat-name", Version: "inventory-redhat-version",
			Purl: redhatVulnerablePurl,
		}}},
		{Packages: []Package{{
			SBOMID: "sbom-patched", Name: "inventory-patched-name", Version: "inventory-patched-version",
			Purl: "pkg:apk/alpine/busybox@1.36.1-r31?arch=aarch64&distro=alpine-3.20.10",
		}}},
	}

	var transcript Transcript
	for _, inventory := range inventories {
		result, err := adapter.Scan(context.Background(), inventory)
		transcript.Records = append(transcript.Records, result.Transcript.Records...)
		if err != nil {
			return transcript, stub.Requests(), err
		}
	}
	return transcript, stub.Requests(), nil
}

func canonicalOSVStubGoldenError(responses map[OSVStubRoute][]byte) error {
	transcript, _, err := runCanonicalOSVStubInteraction(responses)
	if err != nil {
		return err
	}
	if got := transcript.Digest(); got != osvStubGoldenTranscriptDigest {
		return fmt.Errorf("golden transcript digest = %s, want %s", got, osvStubGoldenTranscriptDigest)
	}
	return nil
}

func assertCanonicalOSVStubRequests(t *testing.T, requests []OSVStubRequest) {
	t.Helper()
	if len(requests) != 7 {
		t.Errorf("stub request log has %d entries, want all 7 scanner requests", len(requests))
	}
	expected := map[OSVStubRoute]bool{
		{Method: http.MethodPost, Path: "/v1/querybatch", Body: `{"queries":[{"package":{"name":"github.com/go-jose/go-jose/v4","ecosystem":"Go"},"version":"v4.1.1"}]}`}: true,
		{Method: http.MethodGet, Path: "/v1/vulns/GHSA-78h2-9frx-2jm8"}: true,
		{Method: http.MethodGet, Path: "/v1/vulns/GO-2026-4945"}:        true,
		{Method: http.MethodPost, Path: "/v1/querybatch", Body: `{"queries":[{"package":{"name":"openssl","ecosystem":"Red Hat"},"version":"1:1.1.1k-7.el8"}]}`}: true,
		{Method: http.MethodGet, Path: "/v1/vulns/RHSA-2023:7877"}: true,
		{Method: http.MethodPost, Path: "/v1/query", Body: `{"package":{"name":"openssl","ecosystem":"Red Hat:enterprise_linux:8::baseos"},"version":"1:1.1.1k-7.el8"}`}: true,
		{Method: http.MethodPost, Path: "/v1/querybatch", Body: `{"queries":[{"package":{"name":"busybox","ecosystem":"Alpine:v3.20"},"version":"1.36.1-r31"}]}`}:        true,
	}
	for _, request := range requests {
		if request.Status != http.StatusOK {
			t.Errorf("scanner request failed at stub: %+v", request)
		}
		if strings.Contains(string(request.Body), "inventory-") {
			t.Errorf("request body leaked inventory name or version: %s", request.Body)
		}
		route := OSVStubRoute{Method: request.Method, Path: request.Path, Body: string(request.Body)}
		if !expected[route] {
			t.Errorf("unexpected scanner request reached stub: %+v", request)
		}
		delete(expected, route)
	}
	if len(expected) != 0 {
		t.Errorf("scanner requests missing from stub log: %+v", expected)
	}
}

// TestScanUbuntuLTSAffectedEntry pins a shape found only by running the demo:
// OSV accepts "Ubuntu:20.04" as a QUERY ecosystem but keys the advisory's
// affected entries "Ubuntu:20.04:LTS". Strict equality rejected the finding
// and, because an unmappable identity fails the whole run, produced no
// findings at all rather than one wrong one.
func TestScanUbuntuLTSAffectedEntry(t *testing.T) {
	stub := &stubOSV{
		t:       t,
		batches: []stubResponse{fixtureResponse(batchBody(t, `{"vulns":[{"id":"UBUNTU-CVE-2026-42250"}]}`))},
		details: map[string]stubResponse{
			"UBUNTU-CVE-2026-42250": fixtureResponse(readFixture(t, "detail-UBUNTU-CVE-2026-42250.json")),
		},
	}
	res, err := scanOne(t, stub, Package{
		SBOMID: "s", Name: "bzip2", Version: "1.0.8-2",
		Purl: "pkg:deb/ubuntu/bzip2@1.0.8-2?arch=arm64&distro=ubuntu-20.04",
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d, want the LTS-keyed advisory to map back", len(res.Findings))
	}
	if res.Findings[0].ID != "UBUNTU-CVE-2026-42250" {
		t.Errorf("finding = %q", res.Findings[0].ID)
	}
}

// A different Ubuntu product must NOT be mistaken for the plain release: Pro
// carries its own support window and its own fixed versions.
func TestUbuntuProIsNotThePlainRelease(t *testing.T) {
	if matchesEcosystem("Ubuntu:Pro:20.04:LTS", "Ubuntu:20.04") {
		t.Fatal("Ubuntu Pro matched the plain release")
	}
	if !matchesEcosystem("Ubuntu:20.04:LTS", "Ubuntu:20.04") {
		t.Fatal("the LTS suffix was not tolerated")
	}
	if !matchesEcosystem("Ubuntu:25.10", "Ubuntu:25.10") {
		t.Fatal("a non-LTS release did not match itself")
	}
}

// TestScanUbuntuProOnlyFixReportsNoFix pins the second shape the demo found:
// OSV matched our Ubuntu:20.04 query, but the advisory's only 20.04 entry is
// Ubuntu:Pro:20.04:LTS. The package IS affected; the fix simply lives behind
// extended support. Reporting Pro's fixed version would advertise an upgrade
// standard support cannot install, and failing the run would lose every other
// finding in the pass.
func TestScanUbuntuProOnlyFixReportsNoFix(t *testing.T) {
	stub := &stubOSV{
		t:       t,
		batches: []stubResponse{fixtureResponse(batchBody(t, `{"vulns":[{"id":"UBUNTU-CVE-2025-14104"}]}`))},
		details: map[string]stubResponse{
			"UBUNTU-CVE-2025-14104": fixtureResponse(readFixture(t, "detail-UBUNTU-CVE-2025-14104.json")),
		},
	}
	res, err := scanOne(t, stub, Package{
		SBOMID: "s", Name: "util-linux", Version: "2.34-0.1ubuntu9.6",
		Purl: "pkg:deb/ubuntu/util-linux@2.34-0.1ubuntu9.6?arch=arm64&distro=ubuntu-20.04",
	})
	if err != nil {
		t.Fatalf("a Pro-only fix must not fail the run: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d, want the advisory reported as affected", len(res.Findings))
	}
	if len(res.Findings[0].FixedVersions) != 0 {
		t.Fatalf("fixed versions = %v, want none: the fix exists only under Ubuntu Pro",
			res.Findings[0].FixedVersions)
	}
}

// The unmappable-identity guard must still fire when the record names a
// genuinely different package.
func TestScanUnrelatedAdvisoryStillFailsClosed(t *testing.T) {
	stub := &stubOSV{
		t:       t,
		batches: []stubResponse{fixtureResponse(batchBody(t, `{"vulns":[{"id":"UBUNTU-CVE-2025-14104"}]}`))},
		details: map[string]stubResponse{
			"UBUNTU-CVE-2025-14104": fixtureResponse(readFixture(t, "detail-UBUNTU-CVE-2025-14104.json")),
		},
	}
	_, err := scanOne(t, stub, Package{
		SBOMID: "s", Name: "something-else", Version: "1.0",
		Purl: "pkg:deb/ubuntu/something-else@1.0?arch=arm64&distro=ubuntu-20.04",
	})
	if err == nil {
		t.Fatal("an advisory naming a different package was accepted")
	}
}

// TestFixedVersionIgnoresGitCommits pins a shape the demo surfaced: PYSEC and
// GHSA records carry a GIT range whose fixed event is a commit hash, beside
// the ECOSYSTEM range holding the released version. Projecting both put a
// forty-character SHA in the console's "Fixed in" column, which nobody can act
// on when choosing an upgrade.
func TestFixedVersionIgnoresGitCommits(t *testing.T) {
	stub := &stubOSV{
		t:       t,
		batches: []stubResponse{fixtureResponse(batchBody(t, `{"vulns":[{"id":"PYSEC-2024-60"}]}`))},
		details: map[string]stubResponse{
			"PYSEC-2024-60": fixtureResponse(readFixture(t, "detail-PYSEC-2024-60.json")),
		},
	}
	res, err := scanOne(t, stub, Package{
		SBOMID: "s", Name: "idna", Version: "2.5", Purl: "pkg:pypi/idna@2.5",
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d", len(res.Findings))
	}
	fixed := res.Findings[0].FixedVersions
	if len(fixed) != 1 || fixed[0] != "3.7" {
		t.Fatalf("fixed versions = %v, want only the released version 3.7", fixed)
	}
}
