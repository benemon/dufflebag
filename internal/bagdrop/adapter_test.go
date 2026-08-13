package bagdrop

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Source: internal/compat/hcpauth/handler.go tokenResponse and docs/compatibility.md §3.
const tokenSuccessFixture = `{"access_token":"fixture-token","token_type":"Bearer","expires_in":3600}`

// Source: internal/compat/hcpauth/handler.go writeInvalidClient and docs/compatibility.md §3.
const invalidClientFixture = `{"error":"invalid_client","error_description":"client authentication failed"}`

// Source: internal/compat/hcp2023/handler.go ListBuckets response producer.
const bucketsFixture = `{"buckets":[],"pagination":{}}`

// Source: internal/compat/hcp2023/handler.go writeRPCError and
// docs/compatibility.md §5.1 AlreadyExists handling.
const alreadyExistsFixture = `{"code":6,"message":"already exists","details":[]}`

func TestDufflebagSuppliedCAChainTrustsTokenAndRead(t *testing.T) {
	var calls []string
	server, caChain := newGeneratedCATLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls = append(calls, request.Method+" "+request.URL.RequestURI())
		switch request.URL.Path {
		case "/oauth2/token":
			// Fixture source: internal/compat/hcpauth/handler.go tokenResponse.
			_, _ = w.Write([]byte(tokenSuccessFixture))
		case "/packer/2023-01-01/organizations/hcp-org/projects/hcp-project/buckets":
			// Fixture source: internal/compat/hcp2023/handler.go ListBuckets response producer.
			_, _ = w.Write([]byte(bucketsFixture))
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	adapter, err := NewDufflebagAdapter(server.URL, caChain)
	if err != nil {
		t.Fatal(err)
	}
	result := adapter.Resolve(context.Background(), adapterDestination())
	if result.Outcome != OutcomeResolved {
		t.Fatalf("Resolve = %#v", result)
	}
	want := []string{
		"POST /oauth2/token",
		"GET /packer/2023-01-01/organizations/hcp-org/projects/hcp-project/buckets?pagination.page_size=1",
	}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestDufflebagUntrustedServerClassifiesTLSFailure(t *testing.T) {
	server, _ := newGeneratedCATLSServer(t, http.NotFoundHandler())
	defer server.Close()
	adapter, err := NewDufflebagAdapter(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	result := adapter.Resolve(context.Background(), adapterDestination())
	if result.Outcome != OutcomeFailed || result.Reason != ReasonTLSFailure {
		t.Fatalf("Resolve = %#v, want tls_failure", result)
	}
}

func TestDufflebagWrongCAChainClassifiesTLSFailure(t *testing.T) {
	server, _ := newGeneratedCATLSServer(t, http.NotFoundHandler())
	defer server.Close()
	wrongCA, wrongChain := newGeneratedCATLSServer(t, http.NotFoundHandler())
	defer wrongCA.Close()
	adapter, err := NewDufflebagAdapter(server.URL, wrongChain)
	if err != nil {
		t.Fatal(err)
	}
	result := adapter.Resolve(context.Background(), adapterDestination())
	if result.Outcome != OutcomeFailed || result.Reason != ReasonTLSFailure {
		t.Fatalf("Resolve = %#v, want tls_failure", result)
	}
}

func newGeneratedCATLSServer(t *testing.T, handler http.Handler) (*httptest.Server, string) {
	t.Helper()
	// Fixture source: generated here with crypto/x509. Both the throwaway CA
	// and its localhost leaf exist only for this test process.
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "dufflebag test CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{
		Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey,
	}}}
	server.StartTLS()
	return server, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
}

func TestHCPPackerResolveUsesCompatibilityRequestShapes(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/oauth2/token" {
			t.Errorf("token request = %s %s", r.Method, r.URL.String())
		}
		clientID, secret, ok := r.BasicAuth()
		if !ok || clientID != "client-id" || secret != "client-secret" {
			t.Errorf("Basic credentials = %q/%q/%v", clientID, secret, ok)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
		}
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("audience") != hcpAPIAudience {
			t.Errorf("token form = %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tokenSuccessFixture))
	}))
	defer auth.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/packer/2023-01-01/organizations/hcp-org/projects/hcp-project/buckets"
		if r.Method != http.MethodGet || r.URL.Path != wantPath ||
			r.URL.Query().Get("pagination.page_size") != "1" {
			t.Errorf("bucket request = %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Errorf("bucket Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bucketsFixture))
	}))
	defer api.Close()

	result := NewHCPPackerAdapter(auth.URL, api.URL).Resolve(context.Background(), adapterDestination())
	if result.Outcome != OutcomeResolved || result.Reason != "" || result.Message != "" {
		t.Fatalf("Resolve = %#v", result)
	}
}

func TestHCPPackerResolveClassification(t *testing.T) {
	for _, test := range []struct {
		name        string
		tokenStatus int
		tokenBody   string
		apiStatus   int
		wantReason  VerificationReason
		wantMessage bool
	}{
		{"token 401", http.StatusUnauthorized, invalidClientFixture, 0, ReasonCredentialRefused, false},
		{"invalid_client body", http.StatusBadRequest, invalidClientFixture, 0, ReasonCredentialRefused, false},
		{"bucket 401", http.StatusOK, tokenSuccessFixture, http.StatusUnauthorized, ReasonProjectNotFound, false},
		{"bucket 403", http.StatusOK, tokenSuccessFixture, http.StatusForbidden, ReasonProjectNotFound, false},
		{"bucket 404", http.StatusOK, tokenSuccessFixture, http.StatusNotFound, ReasonProjectNotFound, false},
		{"token other", http.StatusInternalServerError, `{}`, 0, "", true},
		{"bucket other", http.StatusOK, tokenSuccessFixture, http.StatusInternalServerError, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.tokenStatus)
				_, _ = w.Write([]byte(test.tokenBody))
			}))
			defer auth.Close()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.apiStatus)
				_, _ = w.Write([]byte(bucketsFixture))
			}))
			defer api.Close()
			result := NewHCPPackerAdapter(auth.URL, api.URL).Resolve(context.Background(), adapterDestination())
			if result.Outcome != OutcomeFailed || result.Reason != test.wantReason ||
				(result.Message != "") != test.wantMessage {
				t.Fatalf("Resolve = %#v", result)
			}
		})
	}
}

func TestHCPPackerResolveClassifiesTLSFailure(t *testing.T) {
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer tlsServer.Close()
	result := NewHCPPackerAdapter(tlsServer.URL, "http://unused.invalid").Resolve(
		context.Background(), adapterDestination(),
	)
	if result.Outcome != OutcomeFailed || result.Reason != ReasonTLSFailure {
		t.Fatalf("Resolve = %#v", result)
	}
}

func TestHCPPackerResolveClassifiesTimeoutAsUnreachable(t *testing.T) {
	release := make(chan struct{})
	auth := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer auth.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result := NewHCPPackerAdapter(auth.URL, "http://unused.invalid").Resolve(ctx, adapterDestination())
	close(release)
	if result.Outcome != OutcomeFailed || result.Reason != ReasonUnreachable {
		t.Fatalf("Resolve = %#v", result)
	}
}

func TestHCPPackerResolveClassifiesConnectionRefusedAsUnreachable(t *testing.T) {
	listener := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address, err := url.Parse(listener.URL)
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	result := NewHCPPackerAdapter(address.String(), "http://unused.invalid").Resolve(
		context.Background(), adapterDestination(),
	)
	if result.Outcome != OutcomeFailed || result.Reason != ReasonUnreachable {
		t.Fatalf("Resolve = %#v", result)
	}
}

func TestHCPPackerReconcileRefreshesTokenOn401Once(t *testing.T) {
	grants := 0
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		grants++
		_, _ = w.Write([]byte(`{"access_token":"token-` + strconv.Itoa(grants) + `"}`))
	}))
	defer auth.Close()
	requests := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			if r.Header.Get("Authorization") != "Bearer token-1" {
				t.Errorf("first Authorization = %q", r.Header.Get("Authorization"))
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer token-2" {
			t.Errorf("refreshed Authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"bucket":{"description":"fixture"}}`))
	}))
	defer api.Close()

	run, err := NewHCPPackerAdapter(auth.URL, api.URL).BeginReconcile(context.Background(), adapterDestination())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, err := run.GetBucket(context.Background(), "images"); err != nil || !exists {
		t.Fatalf("GetBucket exists=%v, err=%v", exists, err)
	}
	if grants != 2 || requests != 3 {
		t.Fatalf("grants=%d requests=%d, want initial+refresh and bucket+version reads", grants, requests)
	}
}

func TestHCPPackerGetBucketInventoriesAllRemoteVersions(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tokenSuccessFixture))
	}))
	defer auth.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/buckets/images"):
			_, _ = w.Write([]byte(`{"bucket":{"description":"fixture"}}`))
		case strings.HasSuffix(r.URL.Path, "/buckets/images/versions") && r.URL.Query().Get("pagination.next_page_token") == "":
			_, _ = w.Write([]byte(`{"versions":[{"fingerprint":"fp-2"}],"pagination":{"next_page_token":"next"}}`))
		case strings.HasSuffix(r.URL.Path, "/buckets/images/versions") && r.URL.Query().Get("pagination.next_page_token") == "next":
			_, _ = w.Write([]byte(`{"versions":[{"fingerprint":"fp-1"}],"pagination":{}}`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
	}))
	defer api.Close()
	run, err := NewHCPPackerAdapter(auth.URL, api.URL).BeginReconcile(context.Background(), adapterDestination())
	if err != nil {
		t.Fatal(err)
	}
	bucket, exists, err := run.GetBucket(context.Background(), "images")
	if err != nil || !exists || len(bucket.Versions) != 2 ||
		bucket.Versions[0].Fingerprint != "fp-2" || bucket.Versions[1].Fingerprint != "fp-1" {
		t.Fatalf("GetBucket = %#v, %v, %v", bucket, exists, err)
	}
}

func TestHCPPackerListBuildsInventoriesAllRemoteBuilds(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tokenSuccessFixture))
	}))
	defer auth.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pagination.next_page_token") == "" {
			_, _ = w.Write([]byte(`{"builds":[{"id":"build-2","component_type":"googlecompute"}],"pagination":{"next_page_token":"next"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"builds":[{"id":"build-1","component_type":"amazon-ebs"}],"pagination":{}}`))
	}))
	defer api.Close()
	run, err := NewHCPPackerAdapter(auth.URL, api.URL).BeginReconcile(context.Background(), adapterDestination())
	if err != nil {
		t.Fatal(err)
	}
	builds, err := run.ListBuilds(context.Background(), "images", "fp-1")
	if err != nil || len(builds) != 2 || builds[0].ID != "build-2" || builds[1].ID != "build-1" {
		t.Fatalf("ListBuilds = %#v, %v", builds, err)
	}
}

func TestHCPPackerVersionRevocationRequestsUseVendoredShape(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tokenSuccessFixture))
	}))
	defer auth.Close()
	revokeAt := time.Date(2026, 8, 11, 12, 0, 0, 123000000, time.UTC)
	var bodies []map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		wantPath := "/packer/2023-01-01/organizations/hcp-org/projects/hcp-project/buckets/images/versions/fp-1"
		if request.URL.Path != wantPath {
			t.Errorf("version path = %s", request.URL.Path)
		}
		if request.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"version":{"fingerprint":"fp-1","revoke_at":"2026-08-11T12:00:00.123Z","revocation_message":"superseded"}}`))
			return
		}
		if request.Method != http.MethodPatch {
			t.Errorf("version method = %s", request.Method)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		_, _ = w.Write([]byte(`{"version":{"fingerprint":"fp-1"}}`))
	}))
	defer api.Close()
	run, err := NewHCPPackerAdapter(auth.URL, api.URL).BeginReconcile(context.Background(), adapterDestination())
	if err != nil {
		t.Fatal(err)
	}
	remote, exists, err := run.GetVersion(context.Background(), "images", "fp-1")
	if err != nil || !exists || remote.RevokeAt == nil || remote.RevocationMessage != "superseded" {
		t.Fatalf("GetVersion = %#v, %v, %v", remote, exists, err)
	}
	if err := run.RevokeVersion(context.Background(), "images", "fp-1", revokeAt, "superseded"); err != nil {
		t.Fatal(err)
	}
	if err := run.RestoreVersion(context.Background(), "images", "fp-1"); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0]["revoke_at"] != revokeAt.Format(time.RFC3339Nano) ||
		bodies[0]["revocation_message"] != "superseded" || bodies[0]["skip_descendants_revocation"] != true {
		t.Fatalf("revoke body = %#v", bodies)
	}
	if len(bodies[1]) != 1 || bodies[1]["restore"] != true {
		t.Fatalf("restore body = %#v", bodies[1])
	}
}

func TestHCPPackerSbomPresenceAndUploadUseVendoredShape(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tokenSuccessFixture))
	}))
	defer auth.Close()
	document := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6"}`)
	uploads := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		wantPath := "/packer/2023-01-01/organizations/hcp-org/projects/hcp-project/buckets/images/versions/fp-1/builds/build-1/sboms"
		if request.URL.Path != wantPath {
			t.Errorf("SBOM path = %s", request.URL.Path)
		}
		switch request.Method {
		case http.MethodGet:
			if request.URL.Query().Get("pagination.next_page_token") == "" {
				_, _ = w.Write([]byte(`{"sboms":[{"name":"first","format":"SPDX"}],"pagination":{"next_page_token":"next"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"sboms":[{"name":"second","format":"CYCLONEDX"}],"pagination":{}}`))
		case http.MethodPut:
			uploads++
			var body struct {
				CompressedSbom []byte `json:"compressed_sbom"`
				Format         string `json:"format"`
				Name           string `json:"name"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			reader, err := zstd.NewReader(bytes.NewReader(body.CompressedSbom))
			if err != nil {
				t.Fatal(err)
			}
			opened, err := io.ReadAll(reader)
			reader.Close()
			if err != nil || !bytes.Equal(opened, document) || body.Format != "CYCLONEDX" || body.Name != "manifest" {
				t.Errorf("upload body format=%q name=%q document=%q err=%v", body.Format, body.Name, opened, err)
			}
			_, _ = w.Write([]byte(`{"sbom":{"name":"manifest","format":"CYCLONEDX"}}`))
		default:
			t.Errorf("SBOM method = %s", request.Method)
		}
	}))
	defer api.Close()
	run, err := NewHCPPackerAdapter(auth.URL, api.URL).BeginReconcile(context.Background(), adapterDestination())
	if err != nil {
		t.Fatal(err)
	}
	sboms, err := run.ListSboms(context.Background(), "images", "fp-1", "build-1")
	if err != nil || len(sboms) != 2 || sboms[0].Name != "first" || sboms[1].Name != "second" {
		t.Fatalf("ListSboms = %#v, %v", sboms, err)
	}
	if err := run.UploadSbom(context.Background(), "images", "fp-1", "build-1", SbomSnapshot{
		Name: "manifest", Format: "CYCLONEDX", Document: document,
	}); err != nil {
		t.Fatal(err)
	}
	if uploads != 1 {
		t.Fatalf("uploads = %d", uploads)
	}
}

func TestHCPPackerUpdateBuildRunningUsesMinimalBody(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tokenSuccessFixture))
	}))
	defer auth.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		wantPath := "/packer/2023-01-01/organizations/hcp-org/projects/hcp-project/buckets/images/versions/fp-1/builds/build-1"
		if request.Method != http.MethodPatch || request.URL.Path != wantPath {
			t.Errorf("running update = %s %s", request.Method, request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 1 || body["status"] != "BUILD_RUNNING" {
			t.Errorf("running body = %#v", body)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer api.Close()
	run, err := NewHCPPackerAdapter(auth.URL, api.URL).BeginReconcile(context.Background(), adapterDestination())
	if err != nil {
		t.Fatal(err)
	}
	if err := run.UpdateBuildRunning(context.Background(), "images", "fp-1", "build-1"); err != nil {
		t.Fatal(err)
	}
}

func TestHCPPackerSbomSizeRefusalClassificationIncludesBare504(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "A.8 bare gateway timeout", status: http.StatusGatewayTimeout},
		{name: "payload too large", status: http.StatusRequestEntityTooLarge},
		{name: "size-shaped application refusal", status: http.StatusBadRequest, body: `{"code":3,"message":"compressed SBOM size exceeds maximum limit"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tokenSuccessFixture))
			}))
			defer auth.Close()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer api.Close()
			run, err := NewHCPPackerAdapter(auth.URL, api.URL).BeginReconcile(context.Background(), adapterDestination())
			if err != nil {
				t.Fatal(err)
			}
			err = run.UploadSbom(context.Background(), "images", "fp-1", "build-1", SbomSnapshot{
				Name: "oversized", Format: "SPDX", Document: []byte("document"),
			})
			if !sbomSizeRefusal(err) {
				t.Fatalf("size refusal = %v", err)
			}
		})
	}
}

func TestHCPPackerDelete404Code5IsIdempotentSuccess(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tokenSuccessFixture))
	}))
	defer auth.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":5,"message":"already absent","details":[]}`))
	}))
	defer api.Close()
	run, err := NewHCPPackerAdapter(auth.URL, api.URL).BeginReconcile(context.Background(), adapterDestination())
	if err != nil {
		t.Fatal(err)
	}
	for name, deleteTarget := range map[string]func() error{
		"bucket":  func() error { return run.DeleteBucket(context.Background(), "images") },
		"version": func() error { return run.DeleteVersion(context.Background(), "images", "fp-1") },
		"build":   func() error { return run.DeleteBuild(context.Background(), "images", "fp-1", "build-1") },
		"channel": func() error { return run.DeleteChannel(context.Background(), "images", "production") },
	} {
		t.Run(name, func(t *testing.T) {
			if err := deleteTarget(); err != nil {
				t.Fatalf("404/code-5 = %v", err)
			}
		})
	}
}

func TestHCPPackerDeletesUseVendoredPathsAndDoNotTolerateConflict(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tokenSuccessFixture))
	}))
	defer auth.Close()
	var paths []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/channels/refused") {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(alreadyExistsFixture))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer api.Close()
	run, err := NewHCPPackerAdapter(auth.URL, api.URL).BeginReconcile(context.Background(), adapterDestination())
	if err != nil {
		t.Fatal(err)
	}
	if err := run.DeleteChannel(context.Background(), "images", "production"); err != nil {
		t.Fatal(err)
	}
	if err := run.DeleteVersion(context.Background(), "images", "fp-1"); err != nil {
		t.Fatal(err)
	}
	if err := run.DeleteBuild(context.Background(), "images", "fp-1", "build-1"); err != nil {
		t.Fatal(err)
	}
	if err := run.DeleteBucket(context.Background(), "images"); err != nil {
		t.Fatal(err)
	}
	err = run.DeleteChannel(context.Background(), "images", "refused")
	if !remoteError(err, http.StatusConflict, 6) {
		t.Fatalf("delete 409/code-6 = %v, want failure", err)
	}
	want := []string{
		"DELETE /packer/2023-01-01/organizations/hcp-org/projects/hcp-project/buckets/images/channels/production",
		"DELETE /packer/2023-01-01/organizations/hcp-org/projects/hcp-project/buckets/images/versions/fp-1",
		"DELETE /packer/2023-01-01/organizations/hcp-org/projects/hcp-project/buckets/images/versions/fp-1/builds/build-1",
		"DELETE /packer/2023-01-01/organizations/hcp-org/projects/hcp-project/buckets/images",
		"DELETE /packer/2023-01-01/organizations/hcp-org/projects/hcp-project/buckets/images/channels/refused",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("delete paths = %v, want %v", paths, want)
	}
}

func TestHCPPackerReconcileToleratesAlreadyExistsCreates(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tokenSuccessFixture))
	}))
	defer auth.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(alreadyExistsFixture))
	}))
	defer api.Close()
	run, err := NewHCPPackerAdapter(auth.URL, api.URL).BeginReconcile(context.Background(), adapterDestination())
	if err != nil {
		t.Fatal(err)
	}
	if err := run.CreateVersion(context.Background(), "images", VersionSnapshot{
		Fingerprint: "fp-1", TemplateType: "HCL2",
	}); err != nil {
		t.Fatalf("CreateVersion 409/code-6 = %v", err)
	}
	if _, err := run.CreateBuild(context.Background(), "images", "fp-1", BuildSnapshot{
		ComponentType: "amazon-ebs", PackerRunUUID: "run-uuid",
	}); err != nil {
		t.Fatalf("CreateBuild 409/code-6 = %v", err)
	}
	if err := run.CreateChannel(context.Background(), "images", "production"); err != nil {
		t.Fatalf("CreateChannel 409/code-6 = %v", err)
	}
}

func TestHCPPackerUpdateChannelAssignmentRequiresCapturedMask(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tokenSuccessFixture))
	}))
	defer auth.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body struct {
			VersionFingerprint string `json:"version_fingerprint"`
			UpdateMask         string `json:"update_mask"`
		}
		if request.Method != http.MethodPatch ||
			request.URL.Path != "/packer/2023-01-01/organizations/hcp-org/projects/hcp-project/buckets/images/channels/production" {
			t.Errorf("channel update request = %s %s", request.Method, request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.UpdateMask == "" {
			// Fixture source: internal/compat/hcp2023 writeUpdateMaskRequired and
			// docs/compatibility.md probe 15.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":3,"message":"body: (update_mask: field mask: must be set.).","details":[{"@type":"type.googleapis.com/google.rpc.BadRequest","field_violations":[{"field":"body.update_mask","description":"field mask: must be set","reason":"","localized_message":null}]}]}`))
			return
		}
		if body.UpdateMask != "versionFingerprint" || body.VersionFingerprint != "fp-1" {
			t.Errorf("channel update body = %#v", body)
		}
		_, _ = w.Write([]byte(`{"channel":{"name":"production","version":{"fingerprint":"fp-1"}}}`))
	}))
	defer api.Close()
	run, err := NewHCPPackerAdapter(auth.URL, api.URL).BeginReconcile(context.Background(), adapterDestination())
	if err != nil {
		t.Fatal(err)
	}
	if err := run.UpdateChannelAssignment(context.Background(), "images", "production", fingerprintPointer("fp-1")); err != nil {
		t.Fatalf("UpdateChannelAssignment = %v", err)
	}
}

func TestHCPPackerUpdateChannelAssignmentClearsWithMaskedEmptyFingerprint(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tokenSuccessFixture))
	}))
	defer auth.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["update_mask"] != "versionFingerprint" || body["version_fingerprint"] != "" {
			t.Errorf("clear body = %#v", body)
		}
		_, _ = w.Write([]byte(`{"channel":{"name":"production","version":null}}`))
	}))
	defer api.Close()
	run, err := NewHCPPackerAdapter(auth.URL, api.URL).BeginReconcile(context.Background(), adapterDestination())
	if err != nil {
		t.Fatal(err)
	}
	if err := run.UpdateChannelAssignment(context.Background(), "images", "production", nil); err != nil {
		t.Fatalf("clear UpdateChannelAssignment = %v", err)
	}
}

func TestHCPPackerUpdateChannelDoesNotTolerate409Code6(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tokenSuccessFixture))
	}))
	defer auth.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(alreadyExistsFixture))
	}))
	defer api.Close()
	run, err := NewHCPPackerAdapter(auth.URL, api.URL).BeginReconcile(context.Background(), adapterDestination())
	if err != nil {
		t.Fatal(err)
	}
	err = run.UpdateChannelAssignment(context.Background(), "images", "production", fingerprintPointer("fp-1"))
	if !remoteError(err, http.StatusConflict, 6) {
		t.Fatalf("UpdateChannelAssignment 409/code-6 = %v, want failure", err)
	}
}

func adapterDestination() Destination {
	return Destination{
		HCPPackerConfig: HCPPackerConfig{
			OrganizationID: "hcp-org", ProjectID: "hcp-project", ClientID: "client-id",
		},
		ClientSecret: "client-secret",
	}
}

// Older dufflebag destinations rendered a never-revoked version's revoke_at as
// the zero time. Rendering is now fixed at the source, but the adapter keeps
// normalising old destinations so the engine never attempts a restore against
// a version that was never revoked.
func TestGetVersionNormalisesZeroRevokeAtToNotRevoked(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tokenSuccessFixture))
	}))
	defer auth.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Fixture source: a dufflebag compat-plane GetVersion rendering,
		// captured during the duf-bq2w live proof (zero-time revoke_at).
		_, _ = w.Write([]byte(`{"version":{"fingerprint":"fp-1","status":"VERSION_ACTIVE","revoke_at":"0001-01-01T00:00:00.000Z"}}`))
	}))
	defer api.Close()
	run, err := NewHCPPackerAdapter(auth.URL, api.URL).BeginReconcile(context.Background(), adapterDestination())
	if err != nil {
		t.Fatal(err)
	}
	version, exists, err := run.GetVersion(context.Background(), "images", "fp-1")
	if err != nil || !exists {
		t.Fatalf("GetVersion = %v exists=%v", err, exists)
	}
	if version.RevokeAt != nil {
		t.Fatalf("zero-time revoke_at must normalise to not-revoked, got %v", version.RevokeAt)
	}
}

// The build source identifier must travel on the terminal update only: live
// HCP refuses CreateBuild carrying source_external_identifier without a
// parent_version_id (400/code-3, observed 2026-08-13 against the real
// service), while accepting it alone on the terminal update — the sequence
// Packer itself performs (compatibility.md §5.7).
func TestHCPPackerSourceIdentifierTravelsOnTerminalUpdateOnly(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tokenSuccessFixture))
	}))
	defer auth.Close()
	bodies := make(map[string]map[string]any)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies[request.Method+" "+request.URL.Path] = body
		if request.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"build":{"id":"build-1","component_type":"docker.example","status":"BUILD_PENDING"}}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer api.Close()
	run, err := NewHCPPackerAdapter(auth.URL, api.URL).BeginReconcile(context.Background(), adapterDestination())
	if err != nil {
		t.Fatal(err)
	}
	build := BuildSnapshot{
		ComponentType: "docker.example", PackerRunUUID: "run-1", Platform: "docker",
		Labels:                   map[string]string{},
		SourceExternalIdentifier: "sha256:0d4ca43cdc07f2c4d260ea3a4143c8d8fecc35e0e1a02cee7a10ae263e78fdce",
	}
	buildsPath := "/packer/2023-01-01/organizations/hcp-org/projects/hcp-project/buckets/images/versions/fp-1/builds"
	if _, err := run.CreateBuild(context.Background(), "images", "fp-1", build); err != nil {
		t.Fatal(err)
	}
	created := bodies["POST "+buildsPath]
	if created == nil {
		t.Fatal("CreateBuild request was not observed")
	}
	if _, present := created["source_external_identifier"]; present {
		t.Errorf("CreateBuild body carries source_external_identifier: %#v", created)
	}
	if err := run.UpdateBuild(context.Background(), "images", "fp-1", "build-1", build); err != nil {
		t.Fatal(err)
	}
	updated := bodies["PATCH "+buildsPath+"/build-1"]
	if updated == nil {
		t.Fatal("UpdateBuild request was not observed")
	}
	if updated["source_external_identifier"] != build.SourceExternalIdentifier {
		t.Errorf("terminal update body = %#v, want source_external_identifier %q", updated, build.SourceExternalIdentifier)
	}
	sourceless := build
	sourceless.SourceExternalIdentifier = ""
	if err := run.UpdateBuild(context.Background(), "images", "fp-1", "build-2", sourceless); err != nil {
		t.Fatal(err)
	}
	bare := bodies["PATCH "+buildsPath+"/build-2"]
	if bare == nil {
		t.Fatal("sourceless UpdateBuild request was not observed")
	}
	if _, present := bare["source_external_identifier"]; present {
		t.Errorf("sourceless terminal update carries an empty source_external_identifier: %#v", bare)
	}
}
