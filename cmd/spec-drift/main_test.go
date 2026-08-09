// Fixture provenance: testdata/sdk-v0.174.0-files.txt was captured 2026-08-06
// from the module-cache copy of the hcp-sdk-go v0.174.0 zip pinned by contract/go.mod.
// Command:
// unzip -Z1 "$(go env GOMODCACHE)/cache/download/github.com/hashicorp/hcp-sdk-go/@v/v0.174.0.zip" | rg '^github\.com/hashicorp/hcp-sdk-go@v0\.174\.0/(clients/cloud-packer-service/stable/(2021-04-30|2023-01-01)/models/[^/]+\.go|\.changelog/128\.txt|go\.mod)$'
// The filter retains every top-level Go model for both stable dated Packer APIs,
// plus the zip's .changelog/128.txt and go.mod as unrelated layout sentinels.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const fixtureSDKVersion = "fixture"

func TestPublishedVersionsFromPinnedSDK(t *testing.T) {
	seedFileList(t, pinnedSDKFileList(t))

	got, err := publishedVersions(fixtureSDKVersion)
	if err != nil {
		t.Fatalf("publishedVersions: %v", err)
	}
	want := []string{"2021-04-30", "2023-01-01"}
	if !slices.Equal(got, want) {
		t.Fatalf("published versions = %v, want %v", got, want)
	}
}

func TestPublishedVersionsSeesNothingWhenLayoutChanges(t *testing.T) {
	seedFileList(t, []string{
		"github.com/hashicorp/hcp-sdk-go@v0.174.0/clients/cloud-packer-service/v2/2023-01-01/models/example.go",
		"github.com/hashicorp/hcp-sdk-go@v0.174.0/go.mod",
	})

	got, err := publishedVersions(fixtureSDKVersion)
	if err != nil {
		t.Fatalf("publishedVersions: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("published versions = %v, want none after layout change", got)
	}
}

func TestModelDriftCleanAgainstVendoredTree(t *testing.T) {
	seedFileList(t, pinnedSDKFileList(t))

	findings, err := modelDrift(
		fixtureSDKVersion,
		"2023-01-01",
		filepath.Join("..", "..", "internal", "compat", "hcp2023", "models"),
	)
	if err != nil {
		t.Fatalf("modelDrift: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("modelDrift findings = %v, want none", findings)
	}
}

func TestModelDriftNamesTheMissingModel(t *testing.T) {
	names := append([]string(nil), pinnedSDKFileList(t)...)
	const missing = "hashicorp_cloud_packer20230101_invented_model.go"
	names = append(names,
		"github.com/hashicorp/hcp-sdk-go@v0.174.0/clients/cloud-packer-service/stable/2023-01-01/models/"+missing)
	seedFileList(t, names)

	findings, err := modelDrift(
		fixtureSDKVersion,
		"2023-01-01",
		filepath.Join("..", "..", "internal", "compat", "hcp2023", "models"),
	)
	if err != nil {
		t.Fatalf("modelDrift: %v", err)
	}
	if len(findings) != 1 ||
		!strings.Contains(findings[0], "MODEL DIVERGENCE") ||
		!strings.Contains(findings[0], missing) {
		t.Fatalf("modelDrift findings = %v, want one MODEL DIVERGENCE naming %s", findings, missing)
	}
}

// The offset is a MEASUREMENT, not a preference: on 2026-07-29 our 2023-01-01
// tree carried 132 models against the SDK's 121, the whole gap being shared
// types the SDK factors into cloud-shared/v1/models. The synthetic tests below
// exercise the tolerate/flag branches, but they scale with the constant and so
// cannot notice it being edited. This one re-derives the gap from the pinned
// SDK listing and the vendored tree, so changing the constant without
// revalidating the measurement fails here.
func TestSharedTypeOffsetStillMatchesTheVendoredTree(t *testing.T) {
	sdkModels := 0
	const prefix = "clients/cloud-packer-service/stable/2023-01-01/models/"
	for _, name := range pinnedSDKFileList(t) {
		if index := strings.Index(name, prefix); index >= 0 &&
			!strings.Contains(name[index+len(prefix):], "/") {
			sdkModels++
		}
	}
	entries, err := os.ReadDir(filepath.Join("..", "..", "internal", "compat", "hcp2023", "models"))
	if err != nil {
		t.Fatalf("read vendored models: %v", err)
	}
	ours := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".go") {
			ours++
		}
	}
	const validated = 11
	if got := ours - sdkModels; got != validated {
		t.Fatalf("measured offset = %d (ours %d, SDK %d), want the validated %d — revalidate which shared types the SDK factors out",
			got, ours, sdkModels, validated)
	}
	if sharedModelOffset != validated {
		t.Fatalf("sharedModelOffset = %d, but the measured gap is %d", sharedModelOffset, validated)
	}
}

func TestModelDriftToleratesTheSharedTypeOffset(t *testing.T) {
	seedFileList(t, []string{
		"github.com/hashicorp/hcp-sdk-go@v0.174.0/clients/cloud-packer-service/stable/test-version/models/sdk_model.go",
	})
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "sdk_model.go"), nil, 0o600); err != nil {
		t.Fatalf("write SDK model: %v", err)
	}
	for i := range sharedModelOffset {
		name := filepath.Join(localDir, fmt.Sprintf("shared_%02d.go", i))
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatalf("write shared model: %v", err)
		}
	}

	findings, err := modelDrift(fixtureSDKVersion, "test-version", localDir)
	if err != nil {
		t.Fatalf("modelDrift: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("modelDrift findings = %v, want none at offset %d", findings, sharedModelOffset)
	}
}

func TestModelDriftFlagsAWiderOffset(t *testing.T) {
	seedFileList(t, []string{
		"github.com/hashicorp/hcp-sdk-go@v0.174.0/clients/cloud-packer-service/stable/test-version/models/sdk_model.go",
	})
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "sdk_model.go"), nil, 0o600); err != nil {
		t.Fatalf("write SDK model: %v", err)
	}
	for i := range sharedModelOffset + 1 {
		name := filepath.Join(localDir, fmt.Sprintf("shared_%02d.go", i))
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatalf("write shared model: %v", err)
		}
	}

	findings, err := modelDrift(fixtureSDKVersion, "test-version", localDir)
	if err != nil {
		t.Fatalf("modelDrift: %v", err)
	}
	if len(findings) != 1 || !strings.Contains(findings[0], "OFFSET CHANGED") {
		t.Fatalf("modelDrift findings = %v, want one OFFSET CHANGED finding", findings)
	}
}

func TestModelDriftReportsAWithdrawnVersion(t *testing.T) {
	seedFileList(t, []string{
		"github.com/hashicorp/hcp-sdk-go@v0.174.0/clients/cloud-packer-service/stable/another-version/models/example.go",
	})

	findings, err := modelDrift(fixtureSDKVersion, "withdrawn-version", t.TempDir())
	if err != nil {
		t.Fatalf("modelDrift: %v", err)
	}
	if len(findings) != 1 ||
		!strings.Contains(findings[0], "VERSION WITHDRAWN") ||
		strings.Contains(findings[0], "MODEL DIVERGENCE") {
		t.Fatalf("modelDrift findings = %v, want one VERSION WITHDRAWN finding", findings)
	}
}

func TestSummariseCapsTheList(t *testing.T) {
	names := make([]string, listLimit+3)
	for i := range names {
		names[i] = fmt.Sprintf("model_%02d.go", i)
	}

	want := strings.Join(names[:listLimit], ", ") + ", and 3 more"
	if got := summarise(names); got != want {
		t.Fatalf("summarise = %q, want %q", got, want)
	}
}

func seedFileList(t *testing.T, names []string) {
	t.Helper()
	previous := fileListCache
	fileListCache = map[string][]string{fixtureSDKVersion: names}
	t.Cleanup(func() {
		fileListCache = previous
	})
}

func pinnedSDKFileList(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sdk-v0.174.0-files.txt"))
	if err != nil {
		t.Fatalf("read pinned SDK file list: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}
