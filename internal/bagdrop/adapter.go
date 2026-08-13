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

	"github.com/klauspost/compress/zstd"
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
	DeleteBucket(context.Context, string) error
	GetVersion(context.Context, string, string) (*RemoteVersion, bool, error)
	CreateVersion(context.Context, string, VersionSnapshot) error
	RevokeVersion(context.Context, string, string, time.Time, string) error
	RestoreVersion(context.Context, string, string) error
	DeleteVersion(context.Context, string, string) error
	ListBuilds(context.Context, string, string) ([]RemoteBuild, error)
	CreateBuild(context.Context, string, string, BuildSnapshot) (string, error)
	UpdateBuildRunning(context.Context, string, string, string) error
	UpdateBuild(context.Context, string, string, string, BuildSnapshot) error
	DeleteBuild(context.Context, string, string, string) error
	ListSboms(context.Context, string, string, string) ([]RemoteSbom, error)
	UploadSbom(context.Context, string, string, string, SbomSnapshot) error
	ListChannels(context.Context, string) ([]RemoteChannel, error)
	CreateChannel(context.Context, string, string) error
	UpdateChannelAssignment(context.Context, string, string, *string) error
	DeleteChannel(context.Context, string, string) error
}

type Registry map[AdapterKind]Adapter

type destinationAdapter struct {
	authBase string
	apiBase  string
	client   *http.Client
}

type destinationReconcileRun struct {
	adapter     *destinationAdapter
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
	return newDestinationAdapter(authBase, apiBase, &http.Client{})
}

// NewDufflebagAdapter uses one dufflebag listener for both the token grant and
// Packer-compatible API. caChainPEM augments, rather than replaces, system
// trust roots.
func NewDufflebagAdapter(endpoint, caChainPEM string) (Adapter, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("dufflebag endpoint must be a valid https URL")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system CA pool: %w", err)
	}
	if roots == nil {
		roots = x509.NewCertPool()
	}
	if strings.TrimSpace(caChainPEM) != "" && !roots.AppendCertsFromPEM([]byte(caChainPEM)) {
		return nil, errors.New("dufflebag ca_chain contains no parseable PEM certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots}
	return newDestinationAdapter(endpoint, endpoint, &http.Client{Transport: transport}), nil
}

func newDestinationAdapter(authBase, apiBase string, client *http.Client) Adapter {
	return &destinationAdapter{
		authBase: strings.TrimRight(authBase, "/"),
		apiBase:  strings.TrimRight(apiBase, "/"),
		client:   client,
	}
}

// NewDufflebagAdapterFactory is the registry entry used by the process. It
// binds endpoint and trust configuration from each destination at use time.
func NewDufflebagAdapterFactory() Adapter { return dufflebagAdapterFactory{} }

type dufflebagAdapterFactory struct{}

func (dufflebagAdapterFactory) Resolve(ctx context.Context, destination Destination) VerificationResult {
	adapter, normalized, err := configuredDufflebagAdapter(destination)
	if err != nil {
		return failed(err.Error())
	}
	return adapter.Resolve(ctx, normalized)
}

func (dufflebagAdapterFactory) BeginReconcile(
	ctx context.Context, destination Destination,
) (ReconcileRun, error) {
	adapter, normalized, err := configuredDufflebagAdapter(destination)
	if err != nil {
		return nil, err
	}
	return adapter.BeginReconcile(ctx, normalized)
}

func configuredDufflebagAdapter(destination Destination) (Adapter, Destination, error) {
	config := destination.Dufflebag
	adapter, err := NewDufflebagAdapter(config.Endpoint, config.CAChain)
	if err != nil {
		return nil, Destination{}, err
	}
	return adapter, Destination{
		HCPPackerConfig: HCPPackerConfig{
			OrganizationID: config.OrganizationID,
			ProjectID:      config.ProjectID,
			ClientID:       config.ClientID,
		},
		ClientSecret: destination.ClientSecret,
	}, nil
}

func (a *destinationAdapter) Resolve(ctx context.Context, destination Destination) VerificationResult {
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

func (a *destinationAdapter) BeginReconcile(ctx context.Context, destination Destination) (ReconcileRun, error) {
	token, result := a.token(ctx, destination)
	if result.Outcome != OutcomeResolved {
		return nil, verificationError(result)
	}
	return &destinationReconcileRun{adapter: a, destination: destination, token: token}, nil
}

func (a *destinationAdapter) token(ctx context.Context, destination Destination) (string, VerificationResult) {
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

func (r *destinationReconcileRun) basePath() string {
	return "/packer/2023-01-01/organizations/" + url.PathEscape(r.destination.OrganizationID) +
		"/projects/" + url.PathEscape(r.destination.ProjectID)
}

func (r *destinationReconcileRun) GetBucket(ctx context.Context, name string) (*RemoteBucket, bool, error) {
	var response struct {
		Bucket *RemoteBucket `json:"bucket"`
	}
	err := r.do(ctx, http.MethodGet, r.basePath()+"/buckets/"+url.PathEscape(name), nil, &response)
	if remoteError(err, http.StatusNotFound, 5) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if response.Bucket == nil {
		return nil, false, errors.New("destination bucket response omitted bucket")
	}
	versions, err := r.listVersions(ctx, name)
	if err != nil {
		return nil, false, err
	}
	response.Bucket.Versions = versions
	return response.Bucket, true, nil
}

func (r *destinationReconcileRun) CreateBucket(ctx context.Context, bucket BucketSnapshot) error {
	return r.do(ctx, http.MethodPut, r.basePath()+"/buckets", map[string]any{
		"name": bucket.Name, "description": bucket.Description,
	}, nil)
}

func (r *destinationReconcileRun) UpdateBucket(ctx context.Context, bucket BucketSnapshot) error {
	return r.do(ctx, http.MethodPatch, r.basePath()+"/buckets/"+url.PathEscape(bucket.Name), map[string]any{
		"description": bucket.Description,
	}, nil)
}

func (r *destinationReconcileRun) DeleteBucket(ctx context.Context, bucket string) error {
	return r.delete(ctx, r.basePath()+"/buckets/"+url.PathEscape(bucket))
}

func (r *destinationReconcileRun) GetVersion(
	ctx context.Context, bucket, fingerprint string,
) (*RemoteVersion, bool, error) {
	var response struct {
		Version *RemoteVersion `json:"version"`
	}
	err := r.do(ctx, http.MethodGet, r.versionPath(bucket, fingerprint), nil, &response)
	if remoteError(err, http.StatusConflict, 10) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if response.Version == nil {
		return nil, false, errors.New("destination version response omitted version")
	}
	// A zero revoke_at is not a revocation. Live HCP renders null for a
	// never-revoked version, but a dufflebag destination currently renders the
	// zero time (duf-mhaw); treating it as revoked made the engine attempt a
	// restore against nothing. Zero time is not-revoked on any destination.
	if response.Version.RevokeAt != nil && response.Version.RevokeAt.IsZero() {
		response.Version.RevokeAt = nil
	}
	return response.Version, true, nil
}

func (r *destinationReconcileRun) CreateVersion(ctx context.Context, bucket string, version VersionSnapshot) error {
	err := r.do(ctx, http.MethodPost, r.basePath()+"/buckets/"+url.PathEscape(bucket)+"/versions", map[string]any{
		"fingerprint": version.Fingerprint, "template_type": version.TemplateType,
	}, nil)
	if remoteError(err, http.StatusConflict, 6) {
		return nil
	}
	return err
}

func (r *destinationReconcileRun) RevokeVersion(
	ctx context.Context, bucket, fingerprint string, revokeAt time.Time, message string,
) error {
	return r.do(ctx, http.MethodPatch, r.versionPath(bucket, fingerprint), map[string]any{
		"revoke_at": revokeAt.UTC().Format(time.RFC3339Nano), "revocation_message": message,
		"skip_descendants_revocation": true,
	}, nil)
}

func (r *destinationReconcileRun) RestoreVersion(ctx context.Context, bucket, fingerprint string) error {
	return r.do(ctx, http.MethodPatch, r.versionPath(bucket, fingerprint), map[string]any{
		"restore": true,
	}, nil)
}

func (r *destinationReconcileRun) DeleteVersion(ctx context.Context, bucket, fingerprint string) error {
	return r.delete(ctx, r.versionPath(bucket, fingerprint))
}

func (r *destinationReconcileRun) listVersions(ctx context.Context, bucket string) ([]RemoteVersion, error) {
	path := r.basePath() + "/buckets/" + url.PathEscape(bucket) + "/versions"
	var versions []RemoteVersion
	pageToken := ""
	for {
		var response struct {
			Versions   []RemoteVersion `json:"versions"`
			Pagination struct {
				NextPageToken string `json:"next_page_token"`
			} `json:"pagination"`
		}
		pagePath := path
		if pageToken != "" {
			pagePath += "?pagination.next_page_token=" + url.QueryEscape(pageToken)
		}
		if err := r.do(ctx, http.MethodGet, pagePath, nil, &response); err != nil {
			return nil, err
		}
		versions = append(versions, response.Versions...)
		if response.Pagination.NextPageToken == "" {
			return versions, nil
		}
		pageToken = response.Pagination.NextPageToken
	}
}

func (r *destinationReconcileRun) ListBuilds(ctx context.Context, bucket, fingerprint string) ([]RemoteBuild, error) {
	path := r.versionPath(bucket, fingerprint) + "/builds"
	var builds []RemoteBuild
	pageToken := ""
	for {
		var response struct {
			Builds     []RemoteBuild `json:"builds"`
			Pagination struct {
				NextPageToken string `json:"next_page_token"`
			} `json:"pagination"`
		}
		pagePath := path
		if pageToken != "" {
			pagePath += "?pagination.next_page_token=" + url.QueryEscape(pageToken)
		}
		if err := r.do(ctx, http.MethodGet, pagePath, nil, &response); err != nil {
			return nil, err
		}
		builds = append(builds, response.Builds...)
		if response.Pagination.NextPageToken == "" {
			return builds, nil
		}
		pageToken = response.Pagination.NextPageToken
	}
}

func (r *destinationReconcileRun) CreateBuild(
	ctx context.Context, bucket, fingerprint string, build BuildSnapshot,
) (string, error) {
	status := "BUILD_PENDING"
	var response struct {
		Build *RemoteBuild `json:"build"`
	}
	// source_external_identifier is deliberately absent here: live HCP refuses
	// CreateBuild carrying it without a parent_version_id (400/code-3, observed
	// 2026-08-13), and the mirror correlates by names and fingerprints, never
	// destination ids. Packer's own flow reports the source identifier on the
	// terminal update only (compatibility.md §5.7), where HCP accepts it alone —
	// UpdateBuild below carries it.
	err := r.do(ctx, http.MethodPost, r.versionPath(bucket, fingerprint)+"/builds", map[string]any{
		"component_type": build.ComponentType, "status": status,
		"packer_run_uuid": build.PackerRunUUID, "platform": build.Platform,
		"labels": build.Labels,
	}, &response)
	if remoteError(err, http.StatusConflict, 6) {
		return "", nil
	}
	if err != nil || response.Build == nil {
		return "", err
	}
	return response.Build.ID, nil
}

func (r *destinationReconcileRun) UpdateBuildRunning(
	ctx context.Context, bucket, fingerprint, buildID string,
) error {
	// Live HCP accepts SBOM uploads only while the build is BUILD_RUNNING.
	// Keep this transition minimal so completion-only fields are not sent early.
	return r.do(ctx, http.MethodPatch,
		r.versionPath(bucket, fingerprint)+"/builds/"+url.PathEscape(buildID),
		map[string]any{"status": "BUILD_RUNNING"}, nil)
}

func (r *destinationReconcileRun) UpdateBuild(
	ctx context.Context, bucket, fingerprint, buildID string, build BuildSnapshot,
) error {
	artifacts := make([]map[string]string, 0, len(build.Artifacts))
	for _, artifact := range build.Artifacts {
		artifacts = append(artifacts, map[string]string{
			"external_identifier": artifact.ExternalIdentifier, "region": artifact.Region,
		})
	}
	// Completion state plus the source identifier: platform, packer_run_uuid and
	// labels were set at CreateBuild, and live HCP refuses re-setting platform on
	// update with 409/code-6 ("You cannot override a build's Platform if it has
	// already been set" — observed 2026-08-10, recorded in the probe evidence).
	// source_external_identifier travels HERE, not at create, because live HCP
	// refuses the create-time combination without a parent_version_id (400/code-3,
	// observed 2026-08-13) while accepting it alone on the terminal update — the
	// exact sequence Packer itself performs (compatibility.md §5.7). The reconciler
	// only calls this while the destination build is not BUILD_DONE, so the field
	// is never re-set on an already-completed build.
	body := map[string]any{
		"status": "BUILD_DONE", "artifacts": artifacts,
	}
	if build.SourceExternalIdentifier != "" {
		body["source_external_identifier"] = build.SourceExternalIdentifier
	}
	if len(build.Metadata) != 0 {
		body["metadata"] = json.RawMessage(build.Metadata)
	}
	return r.do(ctx, http.MethodPatch, r.versionPath(bucket, fingerprint)+"/builds/"+url.PathEscape(buildID), body, nil)
}

func (r *destinationReconcileRun) DeleteBuild(ctx context.Context, bucket, fingerprint, buildID string) error {
	return r.delete(ctx, r.versionPath(bucket, fingerprint)+"/builds/"+url.PathEscape(buildID))
}

func (r *destinationReconcileRun) ListSboms(
	ctx context.Context, bucket, fingerprint, buildID string,
) ([]RemoteSbom, error) {
	path := r.versionPath(bucket, fingerprint) + "/builds/" + url.PathEscape(buildID) + "/sboms"
	var sboms []RemoteSbom
	pageToken := ""
	for {
		var response struct {
			Sboms      []RemoteSbom `json:"sboms"`
			Pagination struct {
				NextPageToken string `json:"next_page_token"`
			} `json:"pagination"`
		}
		pagePath := path
		if pageToken != "" {
			pagePath += "?pagination.next_page_token=" + url.QueryEscape(pageToken)
		}
		if err := r.do(ctx, http.MethodGet, pagePath, nil, &response); err != nil {
			return nil, err
		}
		sboms = append(sboms, response.Sboms...)
		if response.Pagination.NextPageToken == "" {
			return sboms, nil
		}
		pageToken = response.Pagination.NextPageToken
	}
}

func (r *destinationReconcileRun) UploadSbom(
	ctx context.Context, bucket, fingerprint, buildID string, sbom SbomSnapshot,
) error {
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		return fmt.Errorf("create SBOM compressor: %w", err)
	}
	compressed := encoder.EncodeAll(sbom.Document, nil)
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("close SBOM compressor: %w", err)
	}
	return r.do(ctx, http.MethodPut,
		r.versionPath(bucket, fingerprint)+"/builds/"+url.PathEscape(buildID)+"/sboms",
		map[string]any{"compressed_sbom": compressed, "format": sbom.Format, "name": sbom.Name}, nil)
}

func (r *destinationReconcileRun) ListChannels(ctx context.Context, bucket string) ([]RemoteChannel, error) {
	var response struct {
		Channels []struct {
			Name    string `json:"name"`
			Managed bool   `json:"managed"`
			Version *struct {
				Fingerprint string `json:"fingerprint"`
			} `json:"version"`
		} `json:"channels"`
	}
	err := r.do(ctx, http.MethodGet, r.channelPath(bucket), nil, &response)
	channels := make([]RemoteChannel, 0, len(response.Channels))
	for _, channel := range response.Channels {
		remote := RemoteChannel{Name: channel.Name, Managed: channel.Managed}
		if channel.Version != nil {
			fingerprint := channel.Version.Fingerprint
			remote.AssignedVersionFingerprint = &fingerprint
		}
		channels = append(channels, remote)
	}
	return channels, err
}

func (r *destinationReconcileRun) CreateChannel(ctx context.Context, bucket, name string) error {
	err := r.do(ctx, http.MethodPost, r.channelPath(bucket), map[string]any{"name": name}, nil)
	if remoteError(err, http.StatusConflict, 6) {
		return nil
	}
	return err
}

func (r *destinationReconcileRun) UpdateChannelAssignment(
	ctx context.Context, bucket, name string, fingerprint *string,
) error {
	value := ""
	if fingerprint != nil {
		value = *fingerprint
	}
	return r.do(ctx, http.MethodPatch, r.channelPath(bucket)+"/"+url.PathEscape(name), map[string]any{
		"version_fingerprint": value,
		"update_mask":         "versionFingerprint",
	}, nil)
}

func (r *destinationReconcileRun) DeleteChannel(ctx context.Context, bucket, name string) error {
	return r.delete(ctx, r.channelPath(bucket)+"/"+url.PathEscape(name))
}

func (r *destinationReconcileRun) delete(ctx context.Context, path string) error {
	err := r.do(ctx, http.MethodDelete, path, nil, nil)
	if remoteError(err, http.StatusNotFound, 5) {
		return nil
	}
	return err
}

func (r *destinationReconcileRun) channelPath(bucket string) string {
	return r.basePath() + "/buckets/" + url.PathEscape(bucket) + "/channels"
}

func (r *destinationReconcileRun) versionPath(bucket, fingerprint string) string {
	return r.basePath() + "/buckets/" + url.PathEscape(bucket) + "/versions/" + url.PathEscape(fingerprint)
}

func (r *destinationReconcileRun) do(ctx context.Context, method, path string, body, output any) error {
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

func sbomSizeRefusal(err error) bool {
	var destinationError *AdapterError
	if !errors.As(err, &destinationError) {
		return false
	}
	if destinationError.StatusCode == http.StatusRequestEntityTooLarge ||
		destinationError.StatusCode == http.StatusGatewayTimeout {
		return true
	}
	summary := strings.ToLower(destinationError.Summary)
	sizeWord := strings.Contains(summary, "size") || strings.Contains(summary, "large") ||
		strings.Contains(summary, "payload") || strings.Contains(summary, "entity")
	limitWord := strings.Contains(summary, "limit") || strings.Contains(summary, "maximum") ||
		strings.Contains(summary, "exceed") || strings.Contains(summary, "too large")
	return sizeWord && limitWord
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
