package pluginimport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/plugin"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

type fakeBundleExports struct {
	claim      *store.ClaimedPluginBundle
	plugins    []store.ExportablePlugin
	objects    map[string]string
	archive    []byte
	operations []string
	recorded   *store.PluginBundleResult
	recordErr  string
	putErr     error
}

func (f *fakeBundleExports) ClaimPluginBundle(context.Context, time.Duration) (*store.ClaimedPluginBundle, error) {
	claim := f.claim
	f.claim = nil
	return claim, nil
}

func (f *fakeBundleExports) ExportablePluginVersions(context.Context, store.OrganizationTenant) ([]store.ExportablePlugin, error) {
	return f.plugins, nil
}

func (f *fakeBundleExports) OpenPluginObject(_ context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.objects[key])), nil
}

func (f *fakeBundleExports) PutPluginBundle(_ context.Context, _ store.OrganizationTenant, _ string, body io.ReadSeeker, _ int64) error {
	f.operations = append(f.operations, "put")
	if f.putErr != nil {
		return f.putErr
	}
	archive, err := io.ReadAll(body)
	f.archive = archive
	return err
}

func (f *fakeBundleExports) DeletePluginBundleObject(_ context.Context, _ store.OrganizationTenant, _ string) error {
	f.operations = append(f.operations, "delete")
	return nil
}

func (f *fakeBundleExports) RecordPluginBundle(_ context.Context, _ store.OrganizationTenant, result *store.PluginBundleResult, runErr string) error {
	f.operations = append(f.operations, "record")
	f.recorded, f.recordErr = result, runErr
	return nil
}

func TestExporterBuildsManifestFirstAndHashesCompressedArchive(t *testing.T) {
	tenant := store.OrganizationTenant{OrganizationID: uuid.New()}
	sums, zip := "checksums", "plugin zip"
	fake := &fakeBundleExports{
		claim: &store.ClaimedPluginBundle{
			Tenant: tenant, OrganizationName: "acme", RequestedHost: "dufflebag.example.com", PreviousObjectKey: "previous",
		},
		objects: map[string]string{"sums": sums, "zip": zip},
		plugins: []store.ExportablePlugin{{
			Name: "probe", Source: store.PluginSource{Kind: "upload"},
			Versions: []store.ExportablePluginVersion{{
				Version: "1.1.0", Sums: store.ExportablePluginFile{Size: int64(len(sums)), ObjectKey: "sums"},
				Zips: []store.ExportablePluginFile{{
					Filename: "packer-plugin-probe_v1.1.0_x5.0_linux_amd64.zip", OS: "linux", Arch: "amd64",
					SHA256: digestOf(zip), Size: int64(len(zip)), ObjectKey: "zip",
				}},
			}},
		}},
	}
	exporter := NewExporter(fake, nil, time.Second, slog.Default())
	exporter.DufflebagVersion = "v0.5.0-rc5"
	exporter.now = func() time.Time { return time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC) }
	worked, err := exporter.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("run = %v %v", worked, err)
	}
	if strings.Join(fake.operations, ",") != "put,record,delete" {
		t.Fatalf("operations = %v, want store and record before deleting previous", fake.operations)
	}
	if fake.recorded == nil || fake.recorded.SHA256 != digestOfBytes(fake.archive) || !strings.Contains(fake.recorded.Filename, fake.recorded.SHA256[:12]) {
		t.Fatalf("recorded = %+v, archive digest %s", fake.recorded, digestOfBytes(fake.archive))
	}
	if fake.recorded.PluginCount != 1 || fake.recorded.VersionCount != 1 {
		t.Fatalf("recorded counts = %+v, want 1 plugin, 1 version", fake.recorded)
	}
	gz, err := gzip.NewReader(bytes.NewReader(fake.archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	entries := []string{
		"manifest.json",
		"plugins/probe/1.1.0/packer-plugin-probe_v1.1.0_SHA256SUMS",
		"plugins/probe/1.1.0/packer-plugin-probe_v1.1.0_x5.0_linux_amd64.zip",
	}
	var manifest plugin.BundleManifest
	for i, want := range entries {
		header, err := tr.Next()
		if err != nil || header.Name != want {
			t.Fatalf("entry = %v %v, want %q", header, err, want)
		}
		if i == 0 {
			if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
				t.Fatal(err)
			}
			continue
		}
		_, _ = io.Copy(io.Discard, tr)
	}
	if manifest.Format != 1 || manifest.DufflebagVersion != "v0.5.0-rc5" || manifest.Instance.Host != "dufflebag.example.com" || manifest.Instance.Organization != "acme" {
		t.Fatalf("manifest = %+v", manifest)
	}
	file := manifest.Plugins[0].Versions[0].Files[0]
	if file.Name != "packer-plugin-probe_v1.1.0_SHA256SUMS" || file.SHA256 != digestOf(sums) || file.Size != int64(len(sums)) {
		t.Fatalf("metadata file = %+v, want the SHA256SUMS hashed while it was read", file)
	}
	if manifest.Plugins[0].Origin.Kind != "upload" || manifest.Plugins[0].Versions[0].Platforms[0] != "linux_amd64" {
		t.Fatalf("plugin = %+v", manifest.Plugins[0])
	}
}

func TestExporterFailedStoreKeepsPreviousBundle(t *testing.T) {
	tenant := store.OrganizationTenant{OrganizationID: uuid.New()}
	fake := &fakeBundleExports{
		claim:   &store.ClaimedPluginBundle{Tenant: tenant, OrganizationName: "acme", PreviousObjectKey: "previous"},
		objects: map[string]string{}, putErr: errors.New("store unavailable"),
	}
	exporter := NewExporter(fake, nil, time.Second, slog.Default())
	worked, err := exporter.RunOnce(context.Background())
	if !worked || err == nil || fake.recorded != nil || fake.recordErr != "store unavailable" {
		t.Fatalf("run = %v %v, recorded = %+v %q", worked, err, fake.recorded, fake.recordErr)
	}
	if strings.Join(fake.operations, ",") != "put,record" {
		t.Fatalf("operations = %v, previous object must not be deleted", fake.operations)
	}
}

func digestOf(value string) string { return digestOfBytes([]byte(value)) }

func digestOfBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
