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
	"strconv"
	"strings"
	"time"
)

const hcpAPIAudience = "https://api.hashicorp.cloud"

const adapterCallTimeout = 10 * time.Second

type Adapter interface {
	Resolve(context.Context, Destination) VerificationResult
	BeginReconcile(context.Context, Destination) (ReconcileRun, error)
}

type ReconcileRun interface {
	GetBucket(context.Context, string) (*RemoteBucket, bool, error)
	CreateBucket(context.Context, BucketSnapshot) error
	UpdateBucket(context.Context, BucketSnapshot) error
	GetVersion(context.Context, string, string) (bool, error)
	CreateVersion(context.Context, string, VersionSnapshot) error
	ListBuilds(context.Context, string, string) ([]RemoteBuild, error)
	CreateBuild(context.Context, string, string, BuildSnapshot) (string, error)
	UpdateBuild(context.Context, string, string, string, BuildSnapshot) error
}

type Registry map[AdapterKind]Adapter

type hcpPackerAdapter struct {
	authBase string
	apiBase  string
	client   *http.Client
}

type hcpReconcileRun struct {
	adapter     *hcpPackerAdapter
	destination Destination
	token       string
	refreshed   bool
}

type AdapterError struct {
	StatusCode int
	Code       int
	Summary    string
	RetryAfter time.Duration
}

func (e *AdapterError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("HTTP %d code %d: %s", e.StatusCode, e.Code, e.Summary)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Summary)
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
	token, result := a.token(ctx, destination)
	if result.Outcome != OutcomeResolved {
		return result
	}

	path := "/packer/2023-01-01/organizations/" + url.PathEscape(destination.OrganizationID) +
		"/projects/" + url.PathEscape(destination.ProjectID) + "/buckets?pagination.page_size=1"
	callCtx, cancel := context.WithTimeout(ctx, adapterCallTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, http.MethodGet, a.apiBase+path, nil)
	if err != nil {
		return failed("prepare destination request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := a.client.Do(request)
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

func (a *hcpPackerAdapter) BeginReconcile(ctx context.Context, destination Destination) (ReconcileRun, error) {
	token, result := a.token(ctx, destination)
	if result.Outcome != OutcomeResolved {
		return nil, verificationError(result)
	}
	return &hcpReconcileRun{adapter: a, destination: destination, token: token}, nil
}

func (a *hcpPackerAdapter) token(ctx context.Context, destination Destination) (string, VerificationResult) {
	callCtx, cancel := context.WithTimeout(ctx, adapterCallTimeout)
	defer cancel()
	form := url.Values{"grant_type": {"client_credentials"}, "audience": {hcpAPIAudience}}
	request, err := http.NewRequestWithContext(
		callCtx, http.MethodPost, a.authBase+"/oauth2/token", strings.NewReader(form.Encode()),
	)
	if err != nil {
		return "", failed("prepare token request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(destination.ClientID, destination.ClientSecret)
	response, err := a.client.Do(request)
	if err != nil {
		return "", classifyTransport(err)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<10))
	_ = response.Body.Close()
	if err != nil {
		return "", failed("read token response")
	}
	var token struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	_ = json.Unmarshal(body, &token)
	if response.StatusCode == http.StatusUnauthorized || token.Error == "invalid_client" {
		return "", VerificationResult{Outcome: OutcomeFailed, Reason: ReasonCredentialRefused}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", failed(fmt.Sprintf("token endpoint returned HTTP %d", response.StatusCode))
	}
	if token.AccessToken == "" {
		return "", failed("token response omitted access_token")
	}
	return token.AccessToken, VerificationResult{Outcome: OutcomeResolved}
}

func (r *hcpReconcileRun) basePath() string {
	return "/packer/2023-01-01/organizations/" + url.PathEscape(r.destination.OrganizationID) +
		"/projects/" + url.PathEscape(r.destination.ProjectID)
}

func (r *hcpReconcileRun) GetBucket(ctx context.Context, name string) (*RemoteBucket, bool, error) {
	var response struct {
		Bucket *RemoteBucket `json:"bucket"`
	}
	err := r.do(ctx, http.MethodGet, r.basePath()+"/buckets/"+url.PathEscape(name), nil, &response)
	if remoteError(err, http.StatusNotFound, 5) {
		return nil, false, nil
	}
	return response.Bucket, err == nil, err
}

func (r *hcpReconcileRun) CreateBucket(ctx context.Context, bucket BucketSnapshot) error {
	return r.do(ctx, http.MethodPut, r.basePath()+"/buckets", map[string]any{
		"name": bucket.Name, "description": bucket.Description,
	}, nil)
}

func (r *hcpReconcileRun) UpdateBucket(ctx context.Context, bucket BucketSnapshot) error {
	return r.do(ctx, http.MethodPatch, r.basePath()+"/buckets/"+url.PathEscape(bucket.Name), map[string]any{
		"description": bucket.Description,
	}, nil)
}

func (r *hcpReconcileRun) GetVersion(ctx context.Context, bucket, fingerprint string) (bool, error) {
	err := r.do(ctx, http.MethodGet, r.versionPath(bucket, fingerprint), nil, nil)
	if remoteError(err, http.StatusConflict, 10) {
		return false, nil
	}
	return err == nil, err
}

func (r *hcpReconcileRun) CreateVersion(ctx context.Context, bucket string, version VersionSnapshot) error {
	err := r.do(ctx, http.MethodPost, r.basePath()+"/buckets/"+url.PathEscape(bucket)+"/versions", map[string]any{
		"fingerprint": version.Fingerprint, "template_type": version.TemplateType,
	}, nil)
	if remoteError(err, http.StatusConflict, 6) {
		return nil
	}
	return err
}

func (r *hcpReconcileRun) ListBuilds(ctx context.Context, bucket, fingerprint string) ([]RemoteBuild, error) {
	var response struct {
		Builds []RemoteBuild `json:"builds"`
	}
	err := r.do(ctx, http.MethodGet, r.versionPath(bucket, fingerprint)+"/builds", nil, &response)
	return response.Builds, err
}

func (r *hcpReconcileRun) CreateBuild(
	ctx context.Context, bucket, fingerprint string, build BuildSnapshot,
) (string, error) {
	status := "BUILD_PENDING"
	var response struct {
		Build *RemoteBuild `json:"build"`
	}
	err := r.do(ctx, http.MethodPost, r.versionPath(bucket, fingerprint)+"/builds", map[string]any{
		"component_type": build.ComponentType, "status": status,
		"packer_run_uuid": build.PackerRunUUID, "platform": build.Platform,
		"labels": build.Labels, "source_external_identifier": build.SourceExternalIdentifier,
	}, &response)
	if remoteError(err, http.StatusConflict, 6) {
		return "", nil
	}
	if err != nil || response.Build == nil {
		return "", err
	}
	return response.Build.ID, nil
}

func (r *hcpReconcileRun) UpdateBuild(
	ctx context.Context, bucket, fingerprint, buildID string, build BuildSnapshot,
) error {
	artifacts := make([]map[string]string, 0, len(build.Artifacts))
	for _, artifact := range build.Artifacts {
		artifacts = append(artifacts, map[string]string{
			"external_identifier": artifact.ExternalIdentifier, "region": artifact.Region,
		})
	}
	// Only completion state is sent: platform, packer_run_uuid, labels and
	// source_external_identifier were set at CreateBuild, and live HCP refuses
	// re-setting platform on update with 409/code-6 ("You cannot override a
	// build's Platform if it has already been set" — observed 2026-08-10,
	// recorded in the probe evidence). The terminal update carries exactly
	// what the probed Packer flow carries: status, artifacts, metadata.
	body := map[string]any{
		"status": "BUILD_DONE", "artifacts": artifacts,
	}
	if len(build.Metadata) != 0 {
		body["metadata"] = json.RawMessage(build.Metadata)
	}
	return r.do(ctx, http.MethodPatch, r.versionPath(bucket, fingerprint)+"/builds/"+url.PathEscape(buildID), body, nil)
}

func (r *hcpReconcileRun) versionPath(bucket, fingerprint string) string {
	return r.basePath() + "/buckets/" + url.PathEscape(bucket) + "/versions/" + url.PathEscape(fingerprint)
}

func (r *hcpReconcileRun) do(ctx context.Context, method, path string, body, output any) error {
	for attempt := 0; attempt < 2; attempt++ {
		var encoded io.Reader
		if body != nil {
			payload, err := json.Marshal(body)
			if err != nil {
				return fmt.Errorf("encode destination request: %w", err)
			}
			encoded = strings.NewReader(string(payload))
		}
		callCtx, cancel := context.WithTimeout(ctx, adapterCallTimeout)
		request, err := http.NewRequestWithContext(callCtx, method, r.adapter.apiBase+path, encoded)
		if err != nil {
			cancel()
			return fmt.Errorf("prepare destination request: %w", err)
		}
		request.Header.Set("Authorization", "Bearer "+r.token)
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := r.adapter.client.Do(request)
		if err != nil {
			cancel()
			return fmt.Errorf("destination request: %w", err)
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
		cancel()
		if readErr != nil {
			return fmt.Errorf("read destination response: %w", readErr)
		}
		if response.StatusCode == http.StatusUnauthorized && !r.refreshed {
			r.refreshed = true
			token, result := r.adapter.token(ctx, r.destination)
			if result.Outcome != OutcomeResolved {
				return verificationError(result)
			}
			r.token = token
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return responseError(response, responseBody)
		}
		if output != nil && len(responseBody) != 0 {
			if err := json.Unmarshal(responseBody, output); err != nil {
				return fmt.Errorf("decode destination response: %w", err)
			}
		}
		return nil
	}
	return errors.New("destination remained unauthorized after token refresh")
}

func verificationError(result VerificationResult) error {
	if result.Message != "" {
		return errors.New(result.Message)
	}
	if result.Reason != "" {
		return fmt.Errorf("destination %s", result.Reason)
	}
	return errors.New("destination authentication failed")
}

func responseError(response *http.Response, body []byte) error {
	var rpc struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &rpc)
	summary := strings.TrimSpace(rpc.Message)
	if summary == "" {
		summary = strings.TrimSpace(string(body))
	}
	if len(summary) > 512 {
		summary = summary[:512]
	}
	retryAfter := time.Duration(0)
	if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 {
		retryAfter = time.Duration(seconds) * time.Second
	}
	return &AdapterError{StatusCode: response.StatusCode, Code: rpc.Code, Summary: summary, RetryAfter: retryAfter}
}

func remoteError(err error, status, code int) bool {
	var destinationError *AdapterError
	return errors.As(err, &destinationError) && destinationError.StatusCode == status && destinationError.Code == code
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
