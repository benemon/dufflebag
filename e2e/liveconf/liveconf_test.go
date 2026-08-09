//go:build liveconf

package liveconf

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

const invertedBurden = "drift from documented expectation — default assumption: our documentation is wrong; re-verify before changing the suite"

var ulidPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

type liveClient struct {
	httpClient *http.Client
	apiBase    string
	token      string
}

type response struct {
	status int
	body   []byte
}

type rpcStatus struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Details []json.RawMessage `json:"details"`
}

type version struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type channel struct {
	Name       string   `json:"name"`
	Managed    bool     `json:"managed"`
	Restricted bool     `json:"restricted"`
	Version    *version `json:"version"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
}

func TestLiveHCPConformance(t *testing.T) {
	clientID := os.Getenv("HCP_CLIENT_ID")
	clientSecret := os.Getenv("HCP_CLIENT_SECRET")
	organizationID := os.Getenv("HCP_ORGANIZATION_ID")
	projectID := os.Getenv("HCP_PROJECT_ID")
	if clientID == "" || clientSecret == "" {
		t.Fatal("HCP_CLIENT_ID and HCP_CLIENT_SECRET must be set; credential values are never logged")
	}
	httpClient := &http.Client{Timeout: 45 * time.Second}
	token := issueToken(t, httpClient, clientID, clientSecret)
	client := &liveClient{
		httpClient: httpClient,
		apiBase:    addressURL(os.Getenv("HCP_API_ADDRESS"), "api.cloud.hashicorp.com"),
		token:      token,
	}
	if organizationID == "" || projectID == "" {
		// Discover the way Packer does when the ids are not pinned (dossier
		// §2): exactly one organization, oldest project. This also makes the
		// resource-manager reads part of what the suite conforms against.
		organizationID, projectID = discoverLocation(t, client)
	}

	randomSuffix := shortRandomHex(t)
	bucketName := "dufflebag-liveconf-" + randomSuffix
	missingBucketName := "dufflebag-missing-" + randomSuffix
	fingerprint := "dufflebag-liveconf-version"
	base := client.apiBase + "/packer/2023-01-01/organizations/" + url.PathEscape(organizationID) +
		"/projects/" + url.PathEscape(projectID)
	bucketsPath := base + "/buckets"
	bucketPath := bucketsPath + "/" + url.PathEscape(bucketName)
	versionPath := bucketPath + "/versions/" + url.PathEscape(fingerprint)

	// Register cleanup before the first create attempt. A failed response can be
	// ambiguous about whether HCP committed the bucket, so cleanup always tries
	// DeleteBucket and independently verifies the documented GetBucket miss.
	t.Cleanup(func() {
		cleanupBucket(t, client, bucketPath, bucketName)
	})

	var bucketCreated bool
	var buildID string
	var lifecycleComplete bool
	var timestamps []string
	// One real component: live HCP refuses an SBOM containing no packages
	// (400/code 3 "sbom contains no packages" — observed on this suite's
	// second run; folded into duf-4h25, dufflebag does not enforce it).
	sbomDocument := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5","version":1,"components":[{"type":"library","name":"liveconf-demo","version":"1.0.0","purl":"pkg:golang/example.com/liveconf@v1.0.0"}]}`)

	t.Run("§6-§7_CreateBucket_managed_latest", func(t *testing.T) {
		created := request(t, client, http.MethodPut, bucketsPath, map[string]any{
			"name":        bucketName,
			"description": "dufflebag live conformance",
		})
		requireStatus(t, created, http.StatusOK, "CreateBucket")
		var createdBody struct {
			Bucket struct {
				Name      string `json:"name"`
				CreatedAt string `json:"created_at"`
				UpdatedAt string `json:"updated_at"`
			} `json:"bucket"`
		}
		decode(t, created.body, &createdBody, "CreateBucket response")
		if createdBody.Bucket.Name != bucketName {
			driftf(t, "CreateBucket name = %q, want %q", createdBody.Bucket.Name, bucketName)
		}
		bucketCreated = true
		timestamps = append(timestamps, createdBody.Bucket.CreatedAt, createdBody.Bucket.UpdatedAt)

		latestResponse := request(t, client, http.MethodGet, bucketPath+"/channels/latest", nil)
		requireStatus(t, latestResponse, http.StatusOK, "GetChannel latest immediately after CreateBucket")
		var latest struct {
			Channel *channel `json:"channel"`
		}
		decode(t, latestResponse.body, &latest, "GetChannel latest response")
		if latest.Channel == nil {
			driftf(t, "GetChannel latest returned a null channel")
		}
		if latest.Channel.Name != "latest" || !latest.Channel.Managed || !latest.Channel.Restricted || latest.Channel.Version != nil {
			driftf(t, "fresh latest channel = name %q managed %t restricted %t version-present %t; want latest/true/true/null",
				latest.Channel.Name, latest.Channel.Managed, latest.Channel.Restricted, latest.Channel.Version != nil)
		}
		var latestEnvelope struct {
			Channel map[string]json.RawMessage `json:"channel"`
		}
		decode(t, latestResponse.body, &latestEnvelope, "GetChannel latest raw response")
		if raw, exists := latestEnvelope.Channel["version"]; !exists || !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			driftf(t, "fresh latest channel version field = %s, want explicit null", raw)
		}
		timestamps = append(timestamps, latest.Channel.CreatedAt, latest.Channel.UpdatedAt)
	})

	t.Run("§5.1_fresh_bucket_misses", func(t *testing.T) {
		requireDependency(t, bucketCreated, "CreateBucket")

		unknownVersion := request(t, client, http.MethodGet, versionPath, nil)
		assertRPCStatus(t, unknownVersion, http.StatusConflict, 10, "not found", true, "GetVersion unknown fingerprint")

		unknownBucketPath := bucketsPath + "/" + url.PathEscape(missingBucketName)
		unknownBucket := request(t, client, http.MethodGet, unknownBucketPath, nil)
		assertRPCStatus(t, unknownBucket, http.StatusNotFound, 5, "", true, "GetBucket unknown bucket")
	})

	// The lifecycle is deliberately before channel mutation checks. That gives
	// UpdateChannel a real fingerprint and lets later SBOM checks reuse the one
	// version/build without manufacturing parallel state.
	t.Run("§6_version_lifecycle", func(t *testing.T) {
		requireDependency(t, bucketCreated, "CreateBucket")

		createdVersion := request(t, client, http.MethodPost, bucketPath+"/versions", map[string]any{
			"fingerprint":   fingerprint,
			"template_type": "HCL2",
		})
		requireStatus(t, createdVersion, http.StatusOK, "CreateVersion")
		var versionBody struct {
			Version *version `json:"version"`
		}
		decode(t, createdVersion.body, &versionBody, "CreateVersion response")
		if versionBody.Version == nil || versionBody.Version.Name != "v0" || versionBody.Version.Fingerprint != fingerprint {
			driftf(t, "CreateVersion returned version %#v; want fingerprint %q named v0", versionBody.Version, fingerprint)
		}
		timestamps = append(timestamps, versionBody.Version.CreatedAt, versionBody.Version.UpdatedAt)

		createdBuild := request(t, client, http.MethodPost, versionPath+"/builds", map[string]any{
			"component_type":  "amazon-ebs.liveconf",
			"packer_run_uuid": "00000000-0000-4000-8000-000000000001",
			"status":          "BUILD_UNSET",
			"artifacts":       []any{},
		})
		requireStatus(t, createdBuild, http.StatusOK, "CreateBuild")
		var buildBody struct {
			Build struct {
				ID        string `json:"id"`
				CreatedAt string `json:"created_at"`
				UpdatedAt string `json:"updated_at"`
			} `json:"build"`
		}
		decode(t, createdBuild.body, &buildBody, "CreateBuild response")
		if buildBody.Build.ID == "" {
			driftf(t, "CreateBuild returned an empty build id")
		}
		buildID = buildBody.Build.ID
		timestamps = append(timestamps, buildBody.Build.CreatedAt, buildBody.Build.UpdatedAt)

		// The documented wire order (§6) uploads the SBOM while the build RUNS,
		// step 7 before the terminal update — and live HCP enforces it: this
		// suite's first run uploaded after BUILD_DONE and was refused 400/code 3
		// "This build's status isn't Running" (pinned in §5.6; dufflebag-side
		// divergence filed as duf-4h25).
		running := request(t, client, http.MethodPatch, versionPath+"/builds/"+url.PathEscape(buildID), map[string]any{
			"status":          "BUILD_RUNNING",
			"packer_run_uuid": "00000000-0000-4000-8000-000000000001",
		})
		requireStatus(t, running, http.StatusOK, "UpdateBuild BUILD_RUNNING")

		var compressed bytes.Buffer
		writer, err := zstd.NewWriter(&compressed)
		if err != nil {
			driftf(t, "construct zstd writer: %v", err)
		}
		if _, err := writer.Write(sbomDocument); err != nil {
			driftf(t, "compress CycloneDX document: %v", err)
		}
		if err := writer.Close(); err != nil {
			driftf(t, "finish CycloneDX compression: %v", err)
		}
		uploaded := request(t, client, http.MethodPut, versionPath+"/builds/"+url.PathEscape(buildID)+"/sboms", map[string]any{
			"compressed_sbom": compressed.Bytes(),
			"format":          "CYCLONEDX",
			"name":            "",
		})
		requireStatus(t, uploaded, http.StatusOK, "UploadSbom with empty name while running")

		completedBuild := request(t, client, http.MethodPatch, versionPath+"/builds/"+url.PathEscape(buildID), map[string]any{
			"status":          "BUILD_DONE",
			"platform":        "aws",
			"packer_run_uuid": "00000000-0000-4000-8000-000000000001",
			"artifacts": []map[string]string{{
				"external_identifier": "ami-dufflebag-liveconf",
				"region":              "eu-west-1",
			}},
			"metadata": map[string]any{},
		})
		requireStatus(t, completedBuild, http.StatusOK, "UpdateBuild BUILD_DONE")

		gotVersion := request(t, client, http.MethodGet, versionPath, nil)
		requireStatus(t, gotVersion, http.StatusOK, "GetVersion after BUILD_DONE")
		decode(t, gotVersion.body, &versionBody, "completed GetVersion response")
		if versionBody.Version == nil || versionBody.Version.Name != "v1" || versionBody.Version.Fingerprint != fingerprint {
			driftf(t, "completed version = %#v; want fingerprint %q named v1", versionBody.Version, fingerprint)
		}
		timestamps = append(timestamps, versionBody.Version.CreatedAt, versionBody.Version.UpdatedAt)

		latestResponse := request(t, client, http.MethodGet, bucketPath+"/channels/latest", nil)
		requireStatus(t, latestResponse, http.StatusOK, "GetChannel latest after completion")
		var latest struct {
			Channel *channel `json:"channel"`
		}
		decode(t, latestResponse.body, &latest, "completed latest response")
		if latest.Channel == nil || latest.Channel.Version == nil || latest.Channel.Version.Fingerprint != fingerprint {
			driftf(t, "latest channel did not carry completed fingerprint %q: %#v", fingerprint, latest.Channel)
		}
		timestamps = append(timestamps, latest.Channel.CreatedAt, latest.Channel.UpdatedAt)

		historyResponse := request(t, client, http.MethodGet, bucketPath+"/channels/latest/history", nil)
		requireStatus(t, historyResponse, http.StatusOK, "ListChannelAssignmentHistory latest")
		var history struct {
			History []struct {
				AssignedAt string `json:"assigned_at"`
			} `json:"history"`
		}
		decode(t, historyResponse.body, &history, "latest assignment history response")
		if len(history.History) == 0 {
			driftf(t, "latest assignment history contained no rows after version completion")
		}
		for _, row := range history.History {
			timestamps = append(timestamps, row.AssignedAt)
		}
		lifecycleComplete = true
	})

	t.Run("§7_managed_latest_refusals", func(t *testing.T) {
		requireDependency(t, lifecycleComplete, "version lifecycle")

		updated := request(t, client, http.MethodPatch, bucketPath+"/channels/latest", map[string]any{
			"version_fingerprint": fingerprint,
			"update_mask":         "versionFingerprint",
		})
		assertRPCStatus(t, updated, http.StatusBadRequest, 9, "", true, "UpdateChannel latest")

		deleted := request(t, client, http.MethodDelete, bucketPath+"/channels/latest", nil)
		assertRPCStatus(t, deleted, http.StatusBadRequest, 3, "", true, "DeleteChannel latest")

		assigned := request(t, client, http.MethodPost, bucketPath+"/channels/assign", map[string]any{
			"source_channel": "latest",
			"target_channel": "latest",
		})
		assertRPCStatus(t, assigned, http.StatusBadRequest, 9, "", true, "AssignChannelVersion target latest")
	})

	t.Run("§4a_request_validation_and_permissiveness", func(t *testing.T) {
		requireDependency(t, lifecycleComplete, "version lifecycle")

		updatedBucket := request(t, client, http.MethodPatch, bucketPath, map[string]any{
			"description":                "unknown field accepted",
			"dufflebag_liveconf_unknown": true,
		})
		requireStatus(t, updatedBucket, http.StatusOK, "UpdateBucket with unknown top-level field")

		channelBody := map[string]any{
			"name":                "liveconf-user",
			"version_fingerprint": fingerprint,
			"restricted":          false,
		}
		createdChannel := request(t, client, http.MethodPost, bucketPath+"/channels", channelBody)
		requireStatus(t, createdChannel, http.StatusOK, "CreateChannel first call")
		duplicateChannel := request(t, client, http.MethodPost, bucketPath+"/channels", channelBody)
		assertRPCStatus(t, duplicateChannel, http.StatusConflict, 6, "", true, "CreateChannel duplicate")

		missingMask := request(t, client, http.MethodPatch, bucketPath+"/channels/liveconf-user", map[string]any{
			"version_fingerprint": fingerprint,
		})
		requireStatus(t, missingMask, http.StatusBadRequest, "UpdateChannel without update_mask")
		var status rpcStatus
		decode(t, missingMask.body, &status, "UpdateChannel missing-mask error")
		if status.Code != 3 {
			driftf(t, "UpdateChannel without update_mask code = %d, want 3", status.Code)
		}
		var hasBadRequestDetail bool
		for _, detail := range status.Details {
			text := string(detail)
			if strings.Contains(text, "google.rpc.BadRequest") && strings.Contains(text, "update_mask") {
				hasBadRequestDetail = true
			}
		}
		if !hasBadRequestDetail {
			driftf(t, "UpdateChannel without update_mask details did not contain a google.rpc.BadRequest naming update_mask: %s", missingMask.body)
		}
	})

	t.Run("§5.6_SBOM_serving", func(t *testing.T) {
		requireDependency(t, lifecycleComplete && buildID != "", "completed version and build")

		sbomsPath := versionPath + "/builds/" + url.PathEscape(buildID) + "/sboms"

		// Observed live on this suite's first run, now pinned: an upload
		// against a build that is no longer running is refused. dufflebag
		// diverges today (no status guard) — duf-4h25.
		var lateCompressed bytes.Buffer
		lateWriter, err := zstd.NewWriter(&lateCompressed)
		if err != nil {
			driftf(t, "construct zstd writer: %v", err)
		}
		if _, err := lateWriter.Write(sbomDocument); err != nil {
			driftf(t, "compress CycloneDX document: %v", err)
		}
		if err := lateWriter.Close(); err != nil {
			driftf(t, "finish CycloneDX compression: %v", err)
		}
		late := request(t, client, http.MethodPut, sbomsPath, map[string]any{
			"compressed_sbom": lateCompressed.Bytes(),
			"format":          "CYCLONEDX",
			"name":            "",
		})
		assertRPCStatus(t, late, http.StatusBadRequest, 3, "isn't Running", true, "UploadSbom after BUILD_DONE")

		listed := request(t, client, http.MethodGet, sbomsPath, nil)
		requireStatus(t, listed, http.StatusOK, "ListSboms")
		var listBody struct {
			Sboms []struct {
				Name string `json:"name"`
			} `json:"sboms"`
		}
		decode(t, listed.body, &listBody, "ListSboms response")
		if len(listBody.Sboms) != 1 {
			driftf(t, "ListSboms returned %d documents, want exactly one", len(listBody.Sboms))
		}
		sbomName := listBody.Sboms[0].Name
		if !ulidPattern.MatchString(sbomName) {
			driftf(t, "unnamed SBOM stored name = %q, want 26-character Crockford-base32 ULID", sbomName)
		}
		if sbomName == fingerprint {
			driftf(t, "unnamed SBOM stored name equals fingerprint %q; want a newly minted ULID", fingerprint)
		}

		got := request(t, client, http.MethodGet, sbomsPath+"/"+url.PathEscape(sbomName), nil)
		requireStatus(t, got, http.StatusOK, "GetSbom")
		var fields map[string]json.RawMessage
		decode(t, got.body, &fields, "GetSbom response")
		if len(fields) != 1 || fields["download_url"] == nil {
			driftf(t, "GetSbom fields = %v, want exactly download_url", mapKeys(fields))
		}
		var downloadURL string
		if err := json.Unmarshal(fields["download_url"], &downloadURL); err != nil || downloadURL == "" {
			driftf(t, "GetSbom download_url was not a non-empty string")
		}

		download := fetch(t, client.httpClient, downloadURL)
		requireStatus(t, download, http.StatusOK, "fetch GetSbom download_url")
		if !bytes.Equal(download.body, sbomDocument) {
			driftf(t, "downloaded SBOM = %q, want decompressed CycloneDX JSON %q", download.body, sbomDocument)
		}
	})

	t.Run("§9a_timestamp_parsing", func(t *testing.T) {
		requireDependency(t, lifecycleComplete, "version lifecycle")
		if len(timestamps) == 0 {
			driftf(t, "no wire timestamps were collected")
		}
		for _, timestamp := range timestamps {
			if timestamp == "" {
				driftf(t, "a documented date-time field was empty")
			}
			if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
				driftf(t, "timestamp %q did not parse as RFC3339 with arbitrary fractional precision: %v", timestamp, err)
			}
		}
	})
}

func issueToken(t *testing.T, httpClient *http.Client, clientID, clientSecret string) string {
	t.Helper()
	authBase := addressURL(os.Getenv("HCP_AUTH_URL"), "auth.idp.hashicorp.com")
	form := url.Values{
		"grant_type": {"client_credentials"},
		"audience":   {"https://api.hashicorp.cloud"},
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, authBase+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("construct token request without logging credentials: %v", err)
	}
	req.SetBasicAuth(clientID, clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("token request failed; credential values are redacted: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("read token response; credential values are redacted: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token endpoint answered HTTP %d; response body withheld because it may contain credential material", resp.StatusCode)
	}
	var tokenBody struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tokenBody); err != nil || tokenBody.AccessToken == "" {
		t.Fatal("token endpoint returned no usable access_token; response body withheld because it contains credential material")
	}
	return tokenBody.AccessToken
}

func addressURL(value, defaultHost string) string {
	if value == "" {
		value = defaultHost
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	return strings.TrimRight(value, "/")
}

func shortRandomHex(t *testing.T) string {
	t.Helper()
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		t.Fatalf("generate disposable bucket suffix: %v", err)
	}
	return hex.EncodeToString(random)
}

func request(t *testing.T, client *liveClient, method, endpoint string, body any) response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			driftf(t, "encode %s request body: %v", method, err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, endpoint, reader)
	if err != nil {
		driftf(t, "construct %s request: %v", method, err)
	}
	req.Header.Set("Authorization", "Bearer "+client.token)
	req.Header.Set("User-Agent", "dufflebag-liveconf")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return do(t, client.httpClient, req)
}

func fetch(t *testing.T, httpClient *http.Client, endpoint string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		driftf(t, "construct SBOM download request: %v", err)
	}
	return do(t, httpClient, req)
}

func do(t *testing.T, httpClient *http.Client, req *http.Request) response {
	t.Helper()
	safeEndpoint := req.URL.Scheme + "://" + req.URL.Host + req.URL.EscapedPath()
	resp, err := httpClient.Do(req)
	if err != nil {
		driftf(t, "%s %s failed: %v", req.Method, safeEndpoint, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		driftf(t, "read %s %s response: %v", req.Method, safeEndpoint, err)
	}
	return response{status: resp.StatusCode, body: body}
}

func requireStatus(t *testing.T, got response, want int, operation string) {
	t.Helper()
	if got.status != want {
		driftf(t, "%s answered HTTP %d with body %s, want HTTP %d", operation, got.status, got.body, want)
	}
}

func assertRPCStatus(t *testing.T, got response, wantHTTP, wantCode int, messageSubstring string, emptyDetails bool, operation string) {
	t.Helper()
	requireStatus(t, got, wantHTTP, operation)
	var status rpcStatus
	decode(t, got.body, &status, operation+" error")
	if status.Code != wantCode {
		driftf(t, "%s body code = %d, want %d; body %s", operation, status.Code, wantCode, got.body)
	}
	if messageSubstring != "" && !strings.Contains(strings.ToLower(status.Message), strings.ToLower(messageSubstring)) {
		driftf(t, "%s message = %q, want it to contain %q", operation, status.Message, messageSubstring)
	}
	if emptyDetails {
		var envelope map[string]json.RawMessage
		decode(t, got.body, &envelope, operation+" error envelope")
		var details []json.RawMessage
		raw, exists := envelope["details"]
		if !exists || json.Unmarshal(raw, &details) != nil || len(details) != 0 {
			driftf(t, "%s details = %s, want []", operation, raw)
		}
	}
}

func decode(t *testing.T, body []byte, target any, operation string) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		driftf(t, "decode %s body %s: %v", operation, body, err)
	}
}

func requireDependency(t *testing.T, ready bool, dependency string) {
	t.Helper()
	if !ready {
		t.Skipf("dependency %s failed; skipped to avoid cascading drift reports", dependency)
	}
}

func driftf(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Fatalf("%s: %s", invertedBurden, fmt.Sprintf(format, args...))
}

func mapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func cleanupBucket(t *testing.T, client *liveClient, bucketPath, bucketName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	deleteResponse, deleteErr := cleanupRequest(ctx, client, http.MethodDelete, bucketPath)
	getResponse, getErr := cleanupRequest(ctx, client, http.MethodGet, bucketPath)
	if deleteErr != nil || getErr != nil {
		t.Errorf("%s: cleanup failed; bucket %q may be left behind and must be removed by a human: DeleteBucket error %v; GetBucket error %v",
			invertedBurden, bucketName, deleteErr, getErr)
		return
	}
	if deleteResponse.status != http.StatusOK && deleteResponse.status != http.StatusNotFound {
		t.Errorf("%s: cleanup failed; bucket %q may be left behind and must be removed by a human: DeleteBucket answered HTTP %d with body %s",
			invertedBurden, bucketName, deleteResponse.status, deleteResponse.body)
		return
	}
	var miss rpcStatus
	var envelope map[string]json.RawMessage
	statusErr := json.Unmarshal(getResponse.body, &miss)
	envelopeErr := json.Unmarshal(getResponse.body, &envelope)
	var details []json.RawMessage
	detailsRaw, hasDetails := envelope["details"]
	detailsErr := json.Unmarshal(detailsRaw, &details)
	if getResponse.status != http.StatusNotFound || statusErr != nil || miss.Code != 5 ||
		envelopeErr != nil || !hasDetails || detailsErr != nil || len(details) != 0 {
		t.Errorf("%s: cleanup failed; leftover bucket %q must be removed by a human: verification GetBucket answered HTTP %d with body %s",
			invertedBurden, bucketName, getResponse.status, getResponse.body)
	}
}

func cleanupRequest(ctx context.Context, client *liveClient, method, endpoint string) (response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Authorization", "Bearer "+client.token)
	req.Header.Set("User-Agent", "dufflebag-liveconf")
	resp, err := client.httpClient.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return response{}, err
	}
	return response{status: resp.StatusCode, body: body}, nil
}

// discoverLocation resolves the organization and project the way an unpinned
// Packer does (dossier §2): list organizations expecting exactly one, then
// take the OLDEST project — created_at ordering is load-bearing. Failing
// either expectation names the count so a multi-org account knows to pin.
func discoverLocation(t *testing.T, client *liveClient) (string, string) {
	t.Helper()
	orgs := request(t, client, http.MethodGet,
		client.apiBase+"/resource-manager/2019-12-10/organizations", nil)
	requireStatus(t, orgs, http.StatusOK, "OrganizationService_List")
	var orgList struct {
		Organizations []struct {
			ID string `json:"id"`
		} `json:"organizations"`
	}
	decode(t, orgs.body, &orgList, "OrganizationService_List")
	if len(orgList.Organizations) != 1 {
		t.Fatalf("discovery expects exactly one organization, found %d — pin HCP_ORGANIZATION_ID/HCP_PROJECT_ID",
			len(orgList.Organizations))
	}
	organizationID := orgList.Organizations[0].ID

	projects := request(t, client, http.MethodGet,
		client.apiBase+"/resource-manager/2019-12-10/projects?scope.type=ORGANIZATION&scope.id="+
			url.QueryEscape(organizationID), nil)
	requireStatus(t, projects, http.StatusOK, "ProjectService_List")
	var projectList struct {
		Projects []struct {
			ID        string    `json:"id"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"projects"`
	}
	decode(t, projects.body, &projectList, "ProjectService_List")
	if len(projectList.Projects) == 0 {
		t.Fatal("discovery found no projects in the organization")
	}
	oldest := projectList.Projects[0]
	for _, project := range projectList.Projects[1:] {
		if project.CreatedAt.Before(oldest.CreatedAt) {
			oldest = project
		}
	}
	return organizationID, oldest.ID
}
