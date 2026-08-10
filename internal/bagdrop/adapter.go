package bagdrop

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const hcpAPIAudience = "https://api.hashicorp.cloud"

type Adapter interface {
	Resolve(context.Context, Destination) VerificationResult
}

type Registry map[AdapterKind]Adapter

type hcpPackerAdapter struct {
	authBase string
	apiBase  string
	client   *http.Client
}

// NewHCPPackerAdapter takes bases rather than reading configuration so tests
// exercise the exact production request shapes against httptest servers.
func NewHCPPackerAdapter(authBase, apiBase string) Adapter {
	return &hcpPackerAdapter{
		authBase: strings.TrimRight(authBase, "/"),
		apiBase:  strings.TrimRight(apiBase, "/"),
		client:   &http.Client{},
	}
}

func (a *hcpPackerAdapter) Resolve(ctx context.Context, destination Destination) VerificationResult {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	form := url.Values{
		"grant_type": {"client_credentials"},
		"audience":   {hcpAPIAudience},
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, a.authBase+"/oauth2/token", strings.NewReader(form.Encode()),
	)
	if err != nil {
		return failed("prepare token request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(destination.ClientID, destination.ClientSecret)
	response, err := a.client.Do(request)
	if err != nil {
		return classifyTransport(err)
	}
	tokenBody, err := io.ReadAll(io.LimitReader(response.Body, 8<<10))
	_ = response.Body.Close()
	if err != nil {
		return failed("read token response")
	}
	var token struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	_ = json.Unmarshal(tokenBody, &token)
	if response.StatusCode == http.StatusUnauthorized || token.Error == "invalid_client" {
		return VerificationResult{Outcome: OutcomeFailed, Reason: ReasonCredentialRefused}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return failed(fmt.Sprintf("token endpoint returned HTTP %d", response.StatusCode))
	}
	if token.AccessToken == "" {
		return failed("token response omitted access_token")
	}

	path := "/packer/2023-01-01/organizations/" + url.PathEscape(destination.OrganizationID) +
		"/projects/" + url.PathEscape(destination.ProjectID) + "/buckets?pagination.page_size=1"
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, a.apiBase+path, nil)
	if err != nil {
		return failed("prepare destination request")
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	response, err = a.client.Do(request)
	if err != nil {
		return classifyTransport(err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8<<10))
	_ = response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return VerificationResult{Outcome: OutcomeFailed, Reason: ReasonProjectNotFound}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return failed(fmt.Sprintf("destination returned HTTP %d", response.StatusCode))
	}
	return VerificationResult{Outcome: OutcomeResolved}
}

func classifyTransport(err error) VerificationResult {
	var certificateVerification *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &certificateVerification) || errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostname) || errors.As(err, &invalid) {
		return VerificationResult{Outcome: OutcomeFailed, Reason: ReasonTLSFailure}
	}
	return VerificationResult{Outcome: OutcomeFailed, Reason: ReasonUnreachable}
}

func failed(message string) VerificationResult {
	return VerificationResult{Outcome: OutcomeFailed, Message: message}
}
