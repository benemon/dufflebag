package bagdrop

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// Source: internal/compat/hcpauth/handler.go tokenResponse and docs/compatibility.md §3.
const tokenSuccessFixture = `{"access_token":"fixture-token","token_type":"Bearer","expires_in":3600}`

// Source: internal/compat/hcpauth/handler.go writeInvalidClient and docs/compatibility.md §3.
const invalidClientFixture = `{"error":"invalid_client","error_description":"client authentication failed"}`

// Source: internal/compat/hcp2023/handler.go ListBuckets response producer.
const bucketsFixture = `{"buckets":[],"pagination":{}}`

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

func adapterDestination() Destination {
	return Destination{
		HCPPackerConfig: HCPPackerConfig{
			OrganizationID: "hcp-org", ProjectID: "hcp-project", ClientID: "client-id",
		},
		ClientSecret: "client-secret",
	}
}
