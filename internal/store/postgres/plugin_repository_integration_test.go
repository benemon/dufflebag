//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/objectstore"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

func pluginBlob(filename string, data []byte) store.PluginBlob {
	sum := sha256.Sum256(data)
	return store.PluginBlob{
		Filename: filename, SHA256: hex.EncodeToString(sum[:]),
		Body: bytes.NewReader(data), Size: int64(len(data)),
	}
}

func pluginVersion(version string, source store.PluginSource) store.PluginVersionInput {
	sums := pluginBlob("SHA256SUMS", []byte("sums for "+version+"\n"))
	zip := pluginBlob("packer-plugin-probe_v"+version+"_x5.0_linux_amd64.zip", []byte("zip for "+version))
	return store.PluginVersionInput{
		Name: "probe", Version: version, Source: source,
		Listed: []string{"linux_amd64", "darwin_arm64"},
		Sums:   sums,
		Zips:   []store.PluginZip{{PluginBlob: zip, OS: "linux", Arch: "amd64"}},
	}
}

func TestPluginVersionPublishListAndDisable(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	ctx := context.Background()
	repository := store.NewRepositoryWithObjectStore(db, objects)
	tenant := store.ParseOrganizationTenant(orgA)
	upload := store.PluginSource{Kind: "upload"}

	if err := repository.PublishPluginVersion(ctx, tenant, pluginVersion("1.0.0", upload)); !errors.Is(err, store.ErrPluginRegistryNotEnabled) {
		t.Fatalf("publish without a registry = %v, want ErrPluginRegistryNotEnabled", err)
	}
	if _, err := repository.EnablePluginRegistry(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	first := pluginVersion("1.0.0", upload)
	if err := repository.PublishPluginVersion(ctx, tenant, first); err != nil {
		t.Fatalf("publish: %v", err)
	}
	stored, err := objects.Get(ctx, objectstore.PluginKey(orgA, "probe", "1.0.0", "SHA256SUMS", first.Sums.SHA256))
	if err != nil || string(stored) != "sums for 1.0.0\n" {
		t.Fatalf("stored SHA256SUMS = %q, %v; want the uploaded bytes verbatim", stored, err)
	}
	if err := repository.PublishPluginVersion(ctx, tenant, pluginVersion("1.0.0", upload)); !errors.Is(err, store.ErrPluginVersionExists) {
		t.Fatalf("republish = %v, want ErrPluginVersionExists", err)
	}
	var held store.HeldSourceError
	if err := repository.PublishPluginVersion(ctx, tenant, pluginVersion("1.1.0", store.PluginSource{Kind: "github", Repository: "someone/packer-plugin-probe"})); !errors.As(err, &held) || held.Source != upload {
		t.Fatalf("publish from another source = %v, want HeldSourceError naming the upload source", err)
	}
	if err := repository.PublishPluginVersion(ctx, tenant, pluginVersion("1.1.0", upload)); err != nil {
		t.Fatalf("second version: %v", err)
	}

	plugins, err := repository.ListPlugins(ctx, tenant)
	if err != nil || len(plugins) != 1 || plugins[0].Name != "probe" || len(plugins[0].PublishedVersions) != 2 {
		t.Fatalf("plugins = %+v, %v", plugins, err)
	}
	source, versions, err := repository.ListPluginVersions(ctx, tenant, "probe")
	if err != nil || source != upload || len(versions) != 2 {
		t.Fatalf("versions = %+v %+v, %v", source, versions, err)
	}
	for _, v := range versions {
		if len(v.Listed) != 2 || len(v.Stored) != 1 || v.Stored[0] != "linux_amd64" {
			t.Fatalf("%s coverage = %+v; want two listed platforms and linux_amd64 stored", v.Version, v)
		}
	}
	if _, _, err := repository.ListPluginVersions(ctx, store.ParseOrganizationTenant(orgB), "probe"); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("another organization reading probe = %v, want ErrNotFound", err)
	}

	if err := repository.DisablePluginRegistry(ctx, tenant); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := objects.Get(ctx, objectstore.PluginKey(orgA, "probe", "1.0.0", "SHA256SUMS", first.Sums.SHA256)); err == nil {
		t.Fatal("disable left the version's blobs in the object store")
	}
	if _, err := repository.EnablePluginRegistry(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	plugins, err = repository.ListPlugins(ctx, tenant)
	if err != nil || len(plugins) != 0 {
		t.Fatalf("re-enabled registry plugins = %+v, %v; want empty", plugins, err)
	}
}

func TestPluginVersionWritesNoRowWhenItsBytesCannotBeStored(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	unreachable, err := objectstore.New(objectstore.Config{
		Endpoint: "http://127.0.0.1:1", Region: "us-east-1", Bucket: "plugins", AccessKey: "a", SecretKey: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := store.NewRepositoryWithObjectStore(db, unreachable)
	tenant := store.ParseOrganizationTenant(orgA)
	if _, err := repository.EnablePluginRegistry(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishPluginVersion(ctx, tenant, pluginVersion("1.0.0", store.PluginSource{Kind: "upload"})); !errors.Is(err, store.ErrObjectStorageUnavailable) {
		t.Fatalf("publish with unreachable storage = %v, want ErrObjectStorageUnavailable", err)
	}
	plugins, err := repository.ListPlugins(ctx, tenant)
	if err != nil || len(plugins) != 0 {
		t.Fatalf("plugins after a failed blob write = %+v, %v; want none", plugins, err)
	}
}

func TestServedPluginFilesFollowExposureAndRevocation(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	ctx := context.Background()
	repository := store.NewRepositoryWithObjectStore(db, objects)
	tenant := store.ParseOrganizationTenant(orgA)
	if _, err := repository.EnablePluginRegistry(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	version := pluginVersion("1.0.0", store.PluginSource{Kind: "upload"})
	version.Protocol = "5.0"
	if err := repository.PublishPluginVersion(ctx, tenant, version); err != nil {
		t.Fatal(err)
	}
	var organization string
	if err := db.QueryRowContext(ctx, `SELECT name FROM organizations WHERE id = $1`, orgA).Scan(&organization); err != nil {
		t.Fatal(err)
	}
	zip := version.Zips[0].Filename

	if _, err := repository.ServedPluginVersions(ctx, organization, "probe"); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("unexposed index = %v, want ErrNotFound", err)
	}
	if _, err := repository.ServedPluginFile(ctx, organization, "probe", "1.0.0", store.ServedZip, zip); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("unexposed zip = %v, want ErrNotFound", err)
	}
	if _, err := repository.ExposePluginRegistry(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	versions, err := repository.ServedPluginVersions(ctx, organization, "probe")
	if err != nil || len(versions) != 1 || versions[0] != "1.0.0" {
		t.Fatalf("exposed index = %v, %v", versions, err)
	}
	served, err := repository.ServedPluginFile(ctx, organization, "probe", "1.0.0", store.ServedZip, zip)
	if err != nil {
		t.Fatal(err)
	}
	body, err := repository.OpenPluginObject(ctx, served.ObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	streamed, _ := io.ReadAll(body)
	_ = body.Close()
	if string(streamed) != "zip for 1.0.0" || served.Size != int64(len(streamed)) {
		t.Fatalf("streamed zip = %q size %d", streamed, served.Size)
	}
	manifest, err := repository.ServedPluginFile(ctx, organization, "probe", "1.0.0", store.ServedManifest, "")
	if err != nil || !strings.Contains(string(manifest.Content), `"protocol_version": "5.0"`) {
		t.Fatalf("rendered manifest = %q, %v", manifest.Content, err)
	}
	for name, read := range map[string]func() error{
		"absent signature": func() error {
			_, err := repository.ServedPluginFile(ctx, organization, "probe", "1.0.0", store.ServedSignature, "")
			return err
		},
		"zip not uploaded": func() error {
			_, err := repository.ServedPluginFile(ctx, organization, "probe", "1.0.0", store.ServedZip, "packer-plugin-probe_v1.0.0_x5.0_darwin_arm64.zip")
			return err
		},
		"unknown organization": func() error {
			_, err := repository.ServedPluginVersions(ctx, "nobody", "probe")
			return err
		},
	} {
		if err := read(); !errors.Is(err, registry.ErrNotFound) {
			t.Fatalf("%s = %v, want ErrNotFound", name, err)
		}
	}

	revoke, err := store.BeginOrganizationTenant(ctx, db, orgA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := revoke.ExecContext(ctx, `UPDATE plugin_versions SET revoked = true`); err != nil {
		t.Fatal(err)
	}
	if err := revoke.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ServedPluginVersions(ctx, organization, "probe"); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("index with every version revoked = %v, want ErrNotFound", err)
	}
	if _, err := repository.ServedPluginFile(ctx, organization, "probe", "1.0.0", store.ServedSums, ""); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("revoked sums = %v, want ErrNotFound", err)
	}
}
