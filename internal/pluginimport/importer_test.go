package pluginimport

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

type fakeRepository struct {
	existing  []string
	published []store.PluginVersionInput
	err       error
}

func (f *fakeRepository) PublishPluginVersion(_ context.Context, _ store.OrganizationTenant, input store.PluginVersionInput) error {
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, input)
	return nil
}

func (f *fakeRepository) ListPluginVersions(context.Context, store.OrganizationTenant, string) (store.PluginSource, []store.PluginVersionSummary, error) {
	if len(f.existing) == 0 {
		return store.PluginSource{}, nil, registry.ErrNotFound
	}
	var versions []store.PluginVersionSummary
	for _, version := range f.existing {
		versions = append(versions, store.PluginVersionSummary{Version: version})
	}
	return store.PluginSource{Kind: "releases-hashicorp"}, versions, nil
}

// upstreamRelease stands in for releases.hashicorp.com with one goreleaser-
// shaped release of packer-plugin-probe 1.0.0, signed by signer.
func upstreamRelease(t *testing.T, signer *openpgp.Entity, tamper bool) *httptest.Server {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, _ := writer.Create("packer-plugin-probe_v1.0.0_x5.0_linux_amd64")
	_, _ = entry.Write([]byte("binary"))
	_ = writer.Close()
	zipName := "packer-plugin-probe_v1.0.0_x5.0_linux_amd64.zip"
	sum := sha256.Sum256(archive.Bytes())
	sums := fmt.Sprintf("%s  %s\n%s  packer-plugin-probe_v1.0.0_x5.0_darwin_arm64.zip\n", hex.EncodeToString(sum[:]), zipName, strings.Repeat("0", 64))
	var signature bytes.Buffer
	if err := openpgp.DetachSign(&signature, signer, strings.NewReader(sums), nil); err != nil {
		t.Fatal(err)
	}
	if tamper {
		sums = strings.Replace(sums, "darwin_arm64", "darwin_amd64", 1)
	}
	signatureName := fmt.Sprintf("packer-plugin-probe_1.0.0_SHA256SUMS.%08X.sig", uint32(signer.PrimaryKey.KeyId))
	index, _ := json.Marshal(map[string]any{"versions": map[string]any{"1.0.0": map[string]any{
		"shasums":            "packer-plugin-probe_1.0.0_SHA256SUMS",
		"shasums_signatures": []string{signatureName, "packer-plugin-probe_1.0.0_SHA256SUMS.sig"},
		"builds": []map[string]string{
			{"os": "linux", "arch": "amd64", "filename": zipName},
			{"os": "darwin", "arch": "arm64", "filename": "packer-plugin-probe_v1.0.0_x5.0_darwin_arm64.zip"},
		},
	}}})
	files := map[string][]byte{
		"/packer-plugin-probe/index.json":                                 index,
		"/packer-plugin-probe/1.0.0/packer-plugin-probe_1.0.0_SHA256SUMS": []byte(sums),
		"/packer-plugin-probe/1.0.0/" + signatureName:                     signature.Bytes(),
		"/packer-plugin-probe/1.0.0/" + zipName:                           archive.Bytes(),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	return server
}

func signer(t *testing.T) *openpgp.Entity {
	t.Helper()
	entity, err := openpgp.NewEntity("release signer", "", "signer@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	return entity
}

func TestImportVersionVerifiesThenPublishesTheSelectedPlatforms(t *testing.T) {
	key := signer(t)
	server := upstreamRelease(t, key, false)
	repository := &fakeRepository{}
	importer := NewImporter(NewUpstream(server.Client(), server.URL, server.URL), openpgp.EntityList{key}, repository)

	outcome := importer.ImportVersion(context.Background(), store.OrganizationTenant{}, "packer-plugin-probe", "1.0.0",
		[]string{"linux_amd64", "windows_amd64"})
	if outcome.Outcome != OutcomeImported {
		t.Fatalf("outcome = %+v", outcome)
	}
	if len(outcome.Platforms) != 2 || outcome.Platforms[0] != (PlatformOutcome{Platform: "linux_amd64", Outcome: OutcomeImported}) ||
		outcome.Platforms[1].Outcome != OutcomeFailed || outcome.Platforms[1].Error != "not published upstream" {
		t.Fatalf("platforms = %+v", outcome.Platforms)
	}
	if len(repository.published) != 1 {
		t.Fatalf("published %d versions", len(repository.published))
	}
	input := repository.published[0]
	if input.Source != (store.PluginSource{Kind: "releases-hashicorp", Repository: "packer-plugin-probe"}) ||
		input.Signature == nil || len(input.Zips) != 1 || len(input.Listed) != 2 {
		t.Fatalf("input = %+v", input)
	}
	stored, _ := io.ReadAll(input.Sums.Body)
	if !strings.Contains(string(stored), "darwin_arm64") {
		t.Fatalf("stored SHA256SUMS is not the upstream bytes: %q", stored)
	}
}

func TestImportVersionRefusesWhatTheKeyDidNotSign(t *testing.T) {
	key := signer(t)
	for name, tc := range map[string]struct {
		server  *httptest.Server
		keyring openpgp.EntityList
		want    string
	}{
		"tampered SHA256SUMS":   {upstreamRelease(t, key, true), openpgp.EntityList{key}, "does not verify"},
		"signed by another key": {upstreamRelease(t, signer(t), false), openpgp.EntityList{key}, "no signature by the pinned HashiCorp key"},
	} {
		t.Run(name, func(t *testing.T) {
			repository := &fakeRepository{}
			importer := NewImporter(NewUpstream(tc.server.Client(), tc.server.URL, tc.server.URL), tc.keyring, repository)
			outcome := importer.ImportVersion(context.Background(), store.OrganizationTenant{}, "packer-plugin-probe", "1.0.0", []string{"linux_amd64"})
			if outcome.Outcome != OutcomeFailed || !strings.Contains(outcome.Error, tc.want) || len(repository.published) != 0 {
				t.Fatalf("outcome = %+v, published = %d; want a failure containing %q and nothing stored", outcome, len(repository.published), tc.want)
			}
		})
	}
}

func TestImportVersionSkipsAMirroredVersionWithoutFetching(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests++ }))
	t.Cleanup(server.Close)
	importer := NewImporter(NewUpstream(server.Client(), server.URL, server.URL), openpgp.EntityList{signer(t)}, &fakeRepository{existing: []string{"1.0.0"}})
	outcome := importer.ImportVersion(context.Background(), store.OrganizationTenant{}, "packer-plugin-probe", "1.0.0", []string{"linux_amd64"})
	if outcome.Outcome != OutcomeAlreadyMirrored || requests != 0 {
		t.Fatalf("outcome = %+v after %d upstream requests", outcome, requests)
	}
}
