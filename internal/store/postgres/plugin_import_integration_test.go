//go:build integration

package postgres_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/pluginimport"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// signedRelease serves packer-plugin-probe 1.0.0 for linux_amd64 and
// linux_arm64 the way releases.hashicorp.com lays a version out, signed by
// signer; tamper alters the sums after signing.
func signedRelease(t *testing.T, signer *openpgp.Entity, tamper bool) *httptest.Server {
	t.Helper()
	files := map[string][]byte{}
	var sums string
	var builds []map[string]string
	for _, arch := range []string{"amd64", "arm64"} {
		var archive bytes.Buffer
		writer := zip.NewWriter(&archive)
		entry, _ := writer.Create("packer-plugin-probe_v1.0.0_x5.0_linux_" + arch)
		_, _ = entry.Write([]byte("binary " + arch))
		_ = writer.Close()
		zipName := "packer-plugin-probe_1.0.0_linux_" + arch + ".zip"
		sum := sha256.Sum256(archive.Bytes())
		sums += fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), zipName)
		builds = append(builds, map[string]string{"os": "linux", "arch": arch, "filename": zipName})
		files["/packer-plugin-probe/1.0.0/"+zipName] = archive.Bytes()
	}
	var signature bytes.Buffer
	if err := openpgp.DetachSign(&signature, signer, strings.NewReader(sums), nil); err != nil {
		t.Fatal(err)
	}
	if tamper {
		sums = strings.Replace(sums, "linux_amd64", "linux_386", 1)
	}
	signatureName := fmt.Sprintf("packer-plugin-probe_1.0.0_SHA256SUMS.%08X.sig", uint32(signer.PrimaryKey.KeyId))
	index, _ := json.Marshal(map[string]any{"versions": map[string]any{"1.0.0": map[string]any{
		"shasums": "packer-plugin-probe_1.0.0_SHA256SUMS", "shasums_signatures": []string{signatureName},
		"builds": builds,
	}}})
	files["/packer-plugin-probe/index.json"] = index
	files["/packer-plugin-probe/1.0.0/packer-plugin-probe_1.0.0_SHA256SUMS"] = []byte(sums)
	files["/packer-plugin-probe/1.0.0/"+signatureName] = signature.Bytes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if data, ok := files[r.URL.Path]; ok {
			_, _ = w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestPluginImportWorkerMirrorsASignedRelease(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	ctx := context.Background()
	repository := store.NewRepositoryWithObjectStore(db, objects)
	tenant := store.ParseOrganizationTenant(orgA)
	if _, err := repository.EnablePluginRegistry(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ExposePluginRegistry(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	signer, err := openpgp.NewEntity("release signer", "", "signer@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	run := func(tamper bool) store.PluginImport {
		t.Helper()
		server := signedRelease(t, signer, tamper)
		importer := pluginimport.NewImporter(pluginimport.NewUpstream(server.Client(), server.URL, server.URL), nil, openpgp.EntityList{signer}, repository)
		id, err := repository.CreatePluginImport(ctx, tenant, store.PluginImportRequest{
			SourceKind: "releases-hashicorp", Product: "packer-plugin-probe", Versions: []string{"1.0.0"}, Platforms: []string{"linux_amd64"},
		})
		if err != nil {
			t.Fatal(err)
		}
		worked, err := pluginimport.NewWorker(repository, importer, time.Second, slog.Default()).RunOnce(ctx)
		if err != nil || !worked {
			t.Fatalf("worker claimed nothing: %v", err)
		}
		job, err := repository.GetPluginImport(ctx, tenant, id)
		if err != nil {
			t.Fatal(err)
		}
		return job
	}

	tampered := run(true)
	if tampered.State != "failed" || !strings.Contains(string(tampered.Outcomes), "does not verify") || tampered.FinishedAt == nil {
		t.Fatalf("tampered import = %s %s", tampered.State, tampered.Outcomes)
	}
	if _, _, err := repository.ListPluginVersions(ctx, tenant, "probe"); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("a tampered release left a plugin behind: %v", err)
	}

	job := run(false)
	var outcomes []pluginimport.VersionOutcome
	if job.State != "succeeded" || json.Unmarshal(job.Outcomes, &outcomes) != nil || len(outcomes) != 1 || outcomes[0].Outcome != pluginimport.OutcomeImported {
		t.Fatalf("import = %s %s", job.State, job.Outcomes)
	}
	source, versions, err := repository.ListPluginVersions(ctx, tenant, "probe")
	if err != nil || source != (store.PluginSource{Kind: "releases-hashicorp", Repository: "packer-plugin-probe"}) || len(versions) != 1 {
		t.Fatalf("mirrored plugin = %+v %+v, %v", source, versions, err)
	}
	var organization string
	if err := db.QueryRowContext(ctx, `SELECT name FROM organizations WHERE id = $1`, orgA).Scan(&organization); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ServedPluginFile(ctx, organization, "probe", "1.0.0", store.ServedSignature, ""); err != nil {
		t.Fatalf("the verified signature is not served: %v", err)
	}
	if worked, err := pluginimport.NewWorker(repository, nil, time.Second, slog.Default()).RunOnce(ctx); worked || err != nil {
		t.Fatalf("a finished job was claimed again: worked=%v err=%v", worked, err)
	}

	// A sync adds linux_arm64, admitted against the stored SHA256SUMS, then
	// revokes the version; both apply in one job, in order.
	server := signedRelease(t, signer, false)
	importer := pluginimport.NewImporter(pluginimport.NewUpstream(server.Client(), server.URL, server.URL), nil, openpgp.EntityList{signer}, repository)
	id, err := repository.CreatePluginImport(ctx, tenant, store.PluginImportRequest{
		SourceKind: "releases-hashicorp", Product: "probe", Changes: []store.PluginChange{
			{Version: "1.0.0", Action: "add", Platforms: []string{"linux_arm64"}},
			{Version: "1.0.0", Action: "revoke"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := pluginimport.NewWorker(repository, importer, time.Second, slog.Default()).RunOnce(ctx); !worked || err != nil {
		t.Fatalf("worker claimed no sync: %v", err)
	}
	synced, err := repository.GetPluginImport(ctx, tenant, id)
	if err != nil || synced.State != "succeeded" || len(synced.Request.Changes) != 2 || synced.Request.Changes[0].Platforms[0] != "linux_arm64" {
		t.Fatalf("sync = %+v, %v", synced, err)
	}
	stored, err := repository.GetStoredPluginVersion(ctx, tenant, "probe", "1.0.0")
	if err != nil || len(stored.Stored) != 2 {
		t.Fatalf("stored after sync = %+v, %v", stored, err)
	}
	if _, err := repository.ServedPluginFile(ctx, organization, "probe", "1.0.0", store.ServedZip, "packer-plugin-probe_1.0.0_linux_arm64.zip"); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("a revoked version's zip was served: %v", err)
	}
	if err := repository.SetPluginVersionRevoked(ctx, tenant, "probe", "1.0.0", false); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ServedPluginFile(ctx, organization, "probe", "1.0.0", store.ServedZip, "packer-plugin-probe_1.0.0_linux_arm64.zip"); err != nil {
		t.Fatalf("the added linux_arm64 zip is not served: %v", err)
	}
}
