// Command spec-drift reports when HashiCorp's published API specs appear to have
// changed upstream.
//
// The obvious route is closed: hashicorp/hcp-specs is private, and the public
// docs page embeds a transformed SSR payload with no reachable download endpoint.
// So this uses hcp-sdk-go as a PUBLIC MIRROR — it is go-swagger output from the
// same specs, and when HashiCorp changes a spec they regenerate the SDK.
//
// THE SDK IS THE DETECTOR, NEVER THE SOURCE. Remediation on an alert is to
// re-download the spec from the public Download Spec control and re-vendor it
// with updated provenance and checksum. Copying model definitions out of the SDK
// would smuggle a derived artifact into the position the clean-room position
// reserves for a published spec (spec/vendor/PROVENANCE.md).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// vendored maps each API version we retain to the local model directory built
// from its spec. An empty directory means evidence-only: the spec remains
// checksummed and monitored, but no models are generated or operations served.
var vendored = map[string]string{
	"2023-01-01": "internal/compat/hcp2023/models",
	"2021-04-30": "",
}

// sharedModelOffset is the number of files the SDK does NOT carry per service
// because it factors shared types into cloud-shared/v1/models rather than
// duplicating them.
//
// Validated 2026-07-29: ours 132 files against the SDK's 121 for 2023-01-01, and
// the whole gap is google_rpc_status, google_protobuf_any, pagination_* and
// location_*. Field-level content on those is identical, so the comparison is
// over the intersection by filename and this offset is expected rather than
// drift.
const sharedModelOffset = 11

const remediation = `REMEDIATION — read before acting.
The SDK is the detector, never the source. Do NOT copy model definitions out of
hcp-sdk-go: that would put a derived artifact where the clean-room position
requires a published spec.
Instead: download the spec from the public "Download Spec" control at
https://developer.hashicorp.com/hcp/api-docs/packer, re-vendor it under
spec/vendor/, and update spec/vendor/PROVENANCE.md with the new SHA-256.`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	pinned, err := pinnedSDKVersion()
	if err != nil {
		return err
	}
	latest, err := latestSDKVersion()
	if err != nil {
		return err
	}

	fmt.Printf("hcp-sdk-go pinned=%s latest=%s\n", pinned, latest)

	var findings []string

	// Signal 1, cheapest: a new dated directory means a new API version was
	// published. Checked against the LATEST SDK, since a version added after our
	// pin is exactly what we want to hear about.
	versions, err := publishedVersions(latest)
	if err != nil {
		return err
	}
	// Printed so a clean run is evidence rather than an assertion: a comparison
	// that silently matched nothing would otherwise look identical to one that
	// matched everything and found no drift.
	if len(versions) == 0 {
		return fmt.Errorf("found no API versions in the SDK — the path layout has changed and this check is not comparing anything")
	}
	fmt.Printf("api versions upstream: %s\n", strings.Join(versions, " "))
	for _, version := range versions {
		if _, known := vendored[version]; !known {
			findings = append(findings, fmt.Sprintf(
				"NEW API VERSION: cloud-packer-service/stable/%s exists upstream and is not vendored", version))
		}
	}

	// Signal 2: model divergence for versions we already support.
	for version, local := range vendored {
		if local == "" {
			continue
		}
		drifted, err := modelDrift(latest, version, local)
		if err != nil {
			return err
		}
		findings = append(findings, drifted...)
	}

	// Signal 3, corroboration only: the SDK moving on is not itself drift, but it
	// is the cheap hint that something changed.
	if pinned != latest {
		fmt.Printf("note: hcp-sdk-go has moved from the pinned %s to %s — corroborating signal only\n", pinned, latest)
	}

	if len(findings) == 0 {
		fmt.Println("no drift detected")
		return nil
	}

	sort.Strings(findings)
	fmt.Println()
	for _, finding := range findings {
		fmt.Println("DRIFT:", finding)
	}
	fmt.Println()
	fmt.Println(remediation)
	// Non-zero so a scheduled workflow surfaces this rather than passing quietly.
	os.Exit(1)
	return nil
}

func pinnedSDKVersion() (string, error) {
	// The pin lives in the contract module, which is where the SDK is used.
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}", "github.com/hashicorp/hcp-sdk-go").Output()
	if err == nil {
		if version := strings.TrimSpace(string(out)); version != "" {
			return version, nil
		}
	}
	data, err := os.ReadFile(filepath.Join("contract", "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read contract/go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "github.com/hashicorp/hcp-sdk-go") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return fields[1], nil
			}
		}
	}
	return "", fmt.Errorf("no hcp-sdk-go pin found in contract/go.mod")
}

func latestSDKVersion() (string, error) {
	var info struct{ Version string }
	if err := fetchJSON("https://proxy.golang.org/github.com/hashicorp/hcp-sdk-go/@latest", &info); err != nil {
		return "", err
	}
	if info.Version == "" {
		return "", fmt.Errorf("module proxy returned no version for hcp-sdk-go")
	}
	return info.Version, nil
}

// publishedVersions lists the dated API versions the SDK carries for the packer
// service, read from the module zip's file listing via the proxy.
func publishedVersions(sdkVersion string) ([]string, error) {
	names, err := moduleFileList(sdkVersion)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var versions []string
	const prefix = "clients/cloud-packer-service/stable/"
	for _, name := range names {
		index := strings.Index(name, prefix)
		if index < 0 {
			continue
		}
		rest := name[index+len(prefix):]
		version := strings.SplitN(rest, "/", 2)[0]
		if version != "" && !seen[version] {
			seen[version] = true
			versions = append(versions, version)
		}
	}
	sort.Strings(versions)
	return versions, nil
}

// modelDrift compares the SDK's model filenames for a version against ours.
//
// Only the intersection is compared, because the SDK factors shared types out
// per sharedModelOffset. A model the SDK has and we do not is the signal that
// matters: it means the upstream spec grew something we have not vendored.
func modelDrift(sdkVersion, apiVersion, localDir string) ([]string, error) {
	names, err := moduleFileList(sdkVersion)
	if err != nil {
		return nil, err
	}
	prefix := fmt.Sprintf("clients/cloud-packer-service/stable/%s/models/", apiVersion)

	sdkModels := map[string]bool{}
	for _, name := range names {
		index := strings.Index(name, prefix)
		if index < 0 {
			continue
		}
		base := name[index+len(prefix):]
		if strings.HasSuffix(base, ".go") && !strings.Contains(base, "/") {
			sdkModels[base] = true
		}
	}
	if len(sdkModels) == 0 {
		// Not a finding: an SDK that no longer ships this version is itself worth
		// reporting, but as its own signal rather than as a thousand missing models.
		return []string{fmt.Sprintf(
			"VERSION WITHDRAWN: the SDK at %s carries no models for %s, which we still vendor",
			sdkVersion, apiVersion)}, nil
	}

	entries, err := os.ReadDir(localDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", localDir, err)
	}
	ours := map[string]bool{}
	for _, entry := range entries {
		if name := entry.Name(); strings.HasSuffix(name, ".go") {
			ours[name] = true
		}
	}

	var missing []string
	for name := range sdkModels {
		if !ours[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)

	fmt.Printf("  %s: ours %d models, SDK %d\n", apiVersion, len(ours), len(sdkModels))

	var findings []string
	if len(missing) > 0 {
		findings = append(findings, fmt.Sprintf(
			"MODEL DIVERGENCE in %s: the SDK carries %d model(s) we do not vendor: %s",
			apiVersion, len(missing), summarise(missing)))
	}

	// A gap materially wider than the known shared-type offset means the shape of
	// the comparison has changed and the offset needs revalidating, not silently
	// trusting.
	if gap := len(ours) - len(sdkModels); gap > sharedModelOffset {
		findings = append(findings, fmt.Sprintf(
			"OFFSET CHANGED in %s: ours %d vs SDK %d is a gap of %d, wider than the validated %d — "+
				"revalidate which shared types the SDK factors out before trusting this comparison",
			apiVersion, len(ours), len(sdkModels), gap, sharedModelOffset))
	}
	return findings, nil
}

// summarise lists at most listLimit names, and says how many it left out. A
// structural change upstream produces hundreds of names, and an alert nobody
// reads to the end is an alert that failed.
func summarise(names []string) string {
	if len(names) <= listLimit {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s, and %d more",
		strings.Join(names[:listLimit], ", "), len(names)-listLimit)
}

const listLimit = 10

func moduleFileList(version string) ([]string, error) {
	// The proxy's .info/.mod endpoints do not list files, so read the zip index.
	// Cached per process because both signals need it.
	if cached, ok := fileListCache[version]; ok {
		return cached, nil
	}
	url := fmt.Sprintf("https://proxy.golang.org/github.com/hashicorp/hcp-sdk-go/@v/%s.zip", version)
	response, err := httpGet(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Close() }()

	names, err := zipNames(response)
	if err != nil {
		return nil, err
	}
	fileListCache[version] = names
	return names, nil
}

var fileListCache = map[string][]string{}

func fetchJSON(url string, into any) error {
	body, err := httpGet(url)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	return json.NewDecoder(body).Decode(into)
}

func httpGet(url string) (io.ReadCloser, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	response, err := client.Get(url) //nolint:noctx // a standalone probe with an explicit timeout
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", url, err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("get %s: %s", url, response.Status)
	}
	return response.Body, nil
}
