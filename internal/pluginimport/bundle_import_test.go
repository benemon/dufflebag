package pluginimport

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/benemon/dufflebag/internal/domain/plugin"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

type fakeBundleImports struct {
	claim     *store.ClaimedPluginBundleImport
	objects   map[string][]byte
	held      map[string]string
	publishes []store.PluginVersionInput
	states    []string
	outcomes  []BundleVersionOutcome
	origin    store.PluginBundleOrigin
	problem   string
	deleted   []string
}

func (f *fakeBundleImports) ClaimPluginBundleImport(context.Context, time.Duration) (*store.ClaimedPluginBundleImport, error) {
	claim := f.claim
	f.claim = nil
	return claim, nil
}

func (f *fakeBundleImports) RecordPluginBundleImport(_ context.Context, _ store.OrganizationTenant, state string, origin store.PluginBundleOrigin, outcomes any, problem string) error {
	f.states = append(f.states, state)
	f.outcomes = append([]BundleVersionOutcome{}, outcomes.([]BundleVersionOutcome)...)
	f.origin, f.problem = origin, problem
	return nil
}

func (f *fakeBundleImports) OpenPluginObject(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := f.objects[key]
	if !ok {
		return nil, errors.New("object storage: no such object")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeBundleImports) DeletePluginBundleObject(_ context.Context, _ store.OrganizationTenant, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *fakeBundleImports) PluginVersionSumsDigest(_ context.Context, _ store.OrganizationTenant, name, version string) (string, error) {
	return f.held[name+" "+version], nil
}

func (f *fakeBundleImports) PublishPluginVersion(_ context.Context, _ store.OrganizationTenant, input store.PluginVersionInput) error {
	f.publishes = append(f.publishes, input)
	return nil
}

// bundledRelease is one version's release file set as the exporter writes it.
type bundledRelease struct {
	zipName, zip, sums, sig string
}

// probeRelease stores the binary uncompressed so a same-length edit of its
// content leaves the zip's size unchanged.
func probeRelease(name, version string) bundledRelease {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, _ := writer.CreateHeader(&zip.FileHeader{Name: "packer-plugin-" + name + "_v" + version + "_x5.0_linux_amd64", Method: zip.Store})
	_, _ = entry.Write([]byte("binary " + version))
	_ = writer.Close()
	zipName := "packer-plugin-" + name + "_v" + version + "_x5.0_linux_amd64.zip"
	return bundledRelease{zipName: zipName, zip: archive.String(), sums: digestOf(archive.String()) + "  " + zipName + "\n"}
}

type bundledPlugin struct {
	name, version, originKind, originRepository string
	files                                       bundledRelease
}

// bundleOf builds a manifest and the archive content keyed by manifest object key.
func bundleOf(plugins ...bundledPlugin) (plugin.BundleManifest, map[string]string) {
	manifest := plugin.BundleManifest{
		Format: 1, ExportedAt: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC),
		Instance: plugin.BundleInstance{Host: "connected.example.com", Organization: "acme"}, DufflebagVersion: "v0.5.0-rc5",
	}
	content := map[string]string{}
	for _, bundled := range plugins {
		prefix := "packer-plugin-" + bundled.name + "_v" + bundled.version + "_"
		version := plugin.BundleVersion{Version: bundled.version, Platforms: []string{"linux_amd64"}}
		add := func(name, data string) {
			key := bundled.name + "/" + bundled.version + "/" + name
			content[key] = data
			version.Files = append(version.Files, plugin.BundleFile{Name: name, SHA256: digestOf(data), Size: int64(len(data)), ObjectKey: key})
		}
		add(prefix+"SHA256SUMS", bundled.files.sums)
		if bundled.files.sig != "" {
			add(prefix+"SHA256SUMS.sig", bundled.files.sig)
		}
		if bundled.files.zip != "" {
			add(bundled.files.zipName, bundled.files.zip)
		}
		index := -1
		for i := range manifest.Plugins {
			if manifest.Plugins[i].Name == bundled.name {
				index = i
			}
		}
		if index < 0 {
			manifest.Plugins = append(manifest.Plugins, plugin.BundlePlugin{Name: bundled.name, Origin: plugin.BundleOrigin{Kind: bundled.originKind, Repository: bundled.originRepository}})
			index = len(manifest.Plugins) - 1
		}
		manifest.Plugins[index].Versions = append(manifest.Plugins[index].Versions, version)
	}
	return manifest, content
}

func archiveOf(t *testing.T, manifest plugin.BundleManifest, content map[string]string) []byte {
	t.Helper()
	var archive bytes.Buffer
	if err := plugin.WriteBundle(&archive, manifest, func(file plugin.BundleFile) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(content[file.ObjectKey])), nil
	}); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func mustKeyring(t *testing.T) openpgp.EntityList {
	t.Helper()
	ring, err := HashiCorpKeyring()
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func importBundle(t *testing.T, archive []byte, fake *fakeBundleImports) *fakeBundleImports {
	t.Helper()
	fake.claim = &store.ClaimedPluginBundleImport{
		Tenant: store.OrganizationTenant{OrganizationID: uuid.New()}, Upload: store.PluginBundleUpload{ObjectKey: "upload", Filename: "bundle.tar.gz"},
	}
	fake.objects = map[string][]byte{"upload": archive}
	importer := NewBundleImporter(fake, mustKeyring(t), nil, time.Second, slog.Default())
	// An unreadable bundle is recorded as failed and also returned, for the log.
	if worked, err := importer.RunOnce(context.Background()); !worked || (err != nil && fake.problem == "") {
		t.Fatalf("run = %v %v", worked, err)
	}
	return fake
}

func TestBundleImportPublishesEachVersionAsAnUploadAndSkipsHeldOnes(t *testing.T) {
	held := probeRelease("probe", "1.0.0")
	manifest, content := bundleOf(
		bundledPlugin{name: "other", version: "2.0.0", originKind: "github", originRepository: "acme/packer-plugin-other", files: probeRelease("other", "2.0.0")},
		bundledPlugin{name: "probe", version: "1.1.0", originKind: "upload", files: probeRelease("probe", "1.1.0")},
		bundledPlugin{name: "probe", version: "1.0.0", originKind: "upload", files: held},
	)
	fake := importBundle(t, archiveOf(t, manifest, content), &fakeBundleImports{held: map[string]string{"probe 1.0.0": digestOf(held.sums)}})
	if len(fake.publishes) != 2 || fake.publishes[0].Name != "other" || fake.publishes[1].Name != "probe" || fake.publishes[1].Version != "1.1.0" ||
		fake.publishes[1].Source.Kind != "upload" || len(fake.publishes[1].Zips) != 1 || fake.publishes[1].Zips[0].OS != "linux" || fake.publishes[1].Signature != nil {
		t.Fatalf("publishes = %+v", fake.publishes)
	}
	if len(fake.outcomes) != 3 || fake.outcomes[0].Outcome != BundleOutcomeImported || fake.outcomes[0].OriginRepository != "acme/packer-plugin-other" ||
		fake.outcomes[1].Outcome != BundleOutcomeImported || fake.outcomes[2].Outcome != BundleOutcomeSkipped {
		t.Fatalf("outcomes = %+v", fake.outcomes)
	}
	if fake.states[len(fake.states)-1] != StateSucceeded || fake.origin.Organization != "acme" || fake.origin.Host != "connected.example.com" ||
		fake.origin.PluginCount != 2 || fake.origin.VersionCount != 3 || fake.origin.ExportedAt == nil || fake.origin.Version != "v0.5.0-rc5" {
		t.Fatalf("states = %v origin = %+v", fake.states, fake.origin)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != "upload" {
		t.Fatalf("deleted = %v, want the uploaded bundle removed after the import", fake.deleted)
	}
}

func TestBundleImportReportsAHeldVersionWithDifferentChecksumsAndNeverOverwrites(t *testing.T) {
	manifest, content := bundleOf(bundledPlugin{name: "probe", version: "1.0.0", originKind: "upload", files: probeRelease("probe", "1.0.0")})
	fake := importBundle(t, archiveOf(t, manifest, content), &fakeBundleImports{held: map[string]string{"probe 1.0.0": digestOf("other sums")}})
	if len(fake.publishes) != 0 || len(fake.outcomes) != 1 || fake.outcomes[0].Outcome != BundleOutcomeMismatch ||
		!strings.Contains(fake.outcomes[0].Error, "present with different checksums") || fake.states[len(fake.states)-1] != StateFailed {
		t.Fatalf("publishes = %d outcomes = %+v states = %v", len(fake.publishes), fake.outcomes, fake.states)
	}
}

func TestBundleImportVerifiesHashiCorpProvenanceOfflineAndRefusesWithoutIt(t *testing.T) {
	sums, _ := os.ReadFile("testdata/packer-plugin-amazon_1.8.2_SHA256SUMS")
	signature, _ := os.ReadFile("testdata/packer-plugin-amazon_1.8.2_SHA256SUMS.72D7468F.sig")
	amazon := bundledPlugin{name: "amazon", version: "1.8.2", originKind: "releases-hashicorp", originRepository: "packer-plugin-amazon", files: bundledRelease{sums: string(sums), sig: string(signature)}}
	// A verified signature lets the version through to the held check, where it is skipped.
	manifest, content := bundleOf(amazon)
	fake := importBundle(t, archiveOf(t, manifest, content), &fakeBundleImports{held: map[string]string{"amazon 1.8.2": digestOf(string(sums))}})
	if len(fake.outcomes) != 1 || fake.outcomes[0].Outcome != BundleOutcomeSkipped {
		t.Fatalf("signed outcomes = %+v, want the version past the signature check", fake.outcomes)
	}
	unsigned := amazon
	unsigned.files.sig = ""
	manifest, content = bundleOf(unsigned)
	fake = importBundle(t, archiveOf(t, manifest, content), &fakeBundleImports{})
	if len(fake.outcomes) != 1 || fake.outcomes[0].Outcome != BundleOutcomeRefused || !strings.Contains(fake.outcomes[0].Error, "carries no signature") {
		t.Fatalf("unsigned outcomes = %+v", fake.outcomes)
	}
	tampered := amazon
	tampered.files.sums = strings.Replace(tampered.files.sums, "0", "1", 1)
	manifest, content = bundleOf(tampered)
	fake = importBundle(t, archiveOf(t, manifest, content), &fakeBundleImports{})
	if len(fake.outcomes) != 1 || fake.outcomes[0].Outcome != BundleOutcomeRefused || !strings.Contains(fake.outcomes[0].Error, "does not verify") {
		t.Fatalf("tampered outcomes = %+v", fake.outcomes)
	}
	// A GitHub origin carries no key to check: sums without a signature import.
	manifest, content = bundleOf(bundledPlugin{name: "probe", version: "1.0.0", originKind: "github", originRepository: "acme/packer-plugin-probe", files: probeRelease("probe", "1.0.0")})
	fake = importBundle(t, archiveOf(t, manifest, content), &fakeBundleImports{})
	if len(fake.outcomes) != 1 || fake.outcomes[0].Outcome != BundleOutcomeImported {
		t.Fatalf("github outcomes = %+v", fake.outcomes)
	}
}

func TestBundleImportRefusesAFileAlteredAfterExportAndImportsTheRest(t *testing.T) {
	first := probeRelease("probe", "1.1.0")
	manifest, content := bundleOf(
		bundledPlugin{name: "probe", version: "1.1.0", originKind: "upload", files: first},
		bundledPlugin{name: "probe", version: "1.0.0", originKind: "upload", files: probeRelease("probe", "1.0.0")},
	)
	// The manifest keeps the genuine digest; the archived zip differs by one byte.
	content["probe/1.1.0/"+first.zipName] = strings.Replace(first.zip, "binary 1.1.0", "binary 1.1.x", 1)
	fake := importBundle(t, archiveOf(t, manifest, content), &fakeBundleImports{})
	if len(fake.outcomes) != 2 || fake.outcomes[0].Outcome != BundleOutcomeRefused || !strings.Contains(fake.outcomes[0].Error, first.zipName) ||
		!strings.Contains(fake.outcomes[0].Error, "does not match the manifest") || fake.outcomes[1].Outcome != BundleOutcomeImported {
		t.Fatalf("outcomes = %+v", fake.outcomes)
	}
	if len(fake.publishes) != 1 || fake.publishes[0].Version != "1.0.0" || fake.states[len(fake.states)-1] != StatePartiallySucceeded {
		t.Fatalf("publishes = %+v states = %v", fake.publishes, fake.states)
	}
}

func TestBundleImportRefusesABundleItCannotRead(t *testing.T) {
	var misordered bytes.Buffer
	gz := gzip.NewWriter(&misordered)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "plugins/probe/1.0.0/x.zip", Mode: 0o644, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_ = gz.Close()
	manifest, content := bundleOf()
	manifest.Format = 2
	for name, archive := range map[string][]byte{
		"not gzip":        []byte("plain text"),
		"manifest second": misordered.Bytes(),
		"format 2":        archiveOf(t, manifest, content),
	} {
		fake := importBundle(t, archive, &fakeBundleImports{})
		if fake.states[len(fake.states)-1] != StateFailed || fake.problem == "" || len(fake.outcomes) != 0 || len(fake.deleted) != 1 {
			t.Fatalf("%s: states = %v problem = %q outcomes = %+v deleted = %v", name, fake.states, fake.problem, fake.outcomes, fake.deleted)
		}
	}
}
