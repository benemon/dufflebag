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
	"github.com/google/uuid"
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
		}, store.PluginImportOrigin{CreatedBy: "alice", Kind: "import"})
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
	if job.Origin != (store.PluginImportOrigin{CreatedBy: "alice", Kind: "import", BatchIndex: 1, BatchSize: 1}) || job.QueuedAhead != 0 {
		t.Fatalf("origin = %+v, %d ahead", job.Origin, job.QueuedAhead)
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
	}, store.PluginImportOrigin{CreatedBy: "alice", Kind: "plugin"})
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

	// A queued job counts the organization's unfinished jobs ahead of it.
	batch := store.PluginImportRequest{SourceKind: "releases-hashicorp", Product: "packer-plugin-probe", Versions: []string{"1.0.0"}, Platforms: []string{"linux_amd64"}}
	var queued []uuid.UUID
	for position := range 2 {
		id, err := repository.CreatePluginImport(ctx, tenant, batch, store.PluginImportOrigin{CreatedBy: "alice", Kind: "catalogue", BatchIndex: position + 1, BatchSize: 2})
		if err != nil {
			t.Fatal(err)
		}
		queued = append(queued, id)
	}
	for position, id := range queued {
		job, err := repository.GetPluginImport(ctx, tenant, id)
		if err != nil || job.QueuedAhead != position || job.Origin.BatchIndex != position+1 || job.Origin.BatchSize != 2 {
			t.Fatalf("job %d = %+v ahead %d, %v", position, job.Origin, job.QueuedAhead, err)
		}
	}
	if worked, err := pluginimport.NewWorker(repository, importer, time.Second, slog.Default()).RunOnce(ctx); !worked || err != nil {
		t.Fatalf("worker claimed nothing: %v", err)
	}
	if last, err := repository.GetPluginImport(ctx, tenant, queued[1]); err != nil || last.QueuedAhead != 0 {
		t.Fatalf("after the first finished, the second has %d ahead, %v", last.QueuedAhead, err)
	}
}

func TestPluginImportsListAndStartedAt(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseOrganizationTenant(orgA)
	repository := store.NewRepository(db)
	if _, err := repository.EnablePluginRegistry(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	queue := func(product string) uuid.UUID {
		t.Helper()
		id, err := repository.CreatePluginImport(ctx, tenant, store.PluginImportRequest{
			SourceKind: "releases-hashicorp", Product: product, Versions: []string{"1.0.0"}, Platforms: []string{"linux_amd64"},
		}, store.PluginImportOrigin{CreatedBy: "alice", Kind: "import"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	probe, other, again := queue("packer-plugin-probe"), queue("packer-plugin-other"), queue("packer-plugin-probe")
	all, total, err := repository.ListPluginImports(ctx, tenant, store.PluginImportFilter{Limit: 10})
	if err != nil || total != 3 || len(all) != 3 || all[0].ID != again || all[1].ID != other || all[2].ID != probe || all[0].Plugin != "probe" {
		t.Fatalf("list = %v total %d %+v, want newest first", err, total, all)
	}
	if all[2].QueuedAhead != 0 || all[0].QueuedAhead != 2 {
		t.Fatalf("queued ahead = %d, %d", all[2].QueuedAhead, all[0].QueuedAhead)
	}
	byPlugin, total, err := repository.ListPluginImports(ctx, tenant, store.PluginImportFilter{Plugins: []string{"probe"}, Limit: 10})
	if err != nil || total != 2 || len(byPlugin) != 2 {
		t.Fatalf("by plugin = %v total %d, %d rows", err, total, len(byPlugin))
	}
	page, total, err := repository.ListPluginImports(ctx, tenant, store.PluginImportFilter{Limit: 2, Offset: 2})
	if err != nil || total != 3 || len(page) != 1 || page[0].ID != probe {
		t.Fatalf("last page = %v total %d %+v", err, total, page)
	}
	past, total, err := repository.ListPluginImports(ctx, tenant, store.PluginImportFilter{Limit: 2, Offset: 5})
	if err != nil || total != 3 || len(past) != 0 {
		t.Fatalf("page past the end = %v total %d, %d rows; the total must survive an empty page", err, total, len(past))
	}

	claimed, err := repository.ClaimPluginImport(ctx, time.Minute)
	if err != nil || claimed == nil || claimed.ID != probe {
		t.Fatalf("claim = %+v %v, want the oldest job", claimed, err)
	}
	running, err := repository.GetPluginImport(ctx, tenant, probe)
	if err != nil || running.State != "running" || running.StartedAt == nil {
		t.Fatalf("claimed job = %+v %v, want running with started_at", running, err)
	}
	if err := repository.RecordPluginImport(ctx, tenant, probe, "succeeded", []string{}); err != nil {
		t.Fatal(err)
	}
	finished, err := repository.GetPluginImport(ctx, tenant, probe)
	if err != nil || finished.StartedAt == nil || !finished.StartedAt.Equal(*running.StartedAt) || finished.FinishedAt == nil {
		t.Fatalf("finished job = %+v %v, want started_at kept", finished, err)
	}
	unfinished, total, err := repository.ListPluginImports(ctx, tenant, store.PluginImportFilter{States: []string{"queued", "running"}, Limit: 10})
	if err != nil || total != 2 || len(unfinished) != 2 {
		t.Fatalf("unfinished = %v total %d, %d rows", err, total, len(unfinished))
	}
}

func TestPluginImportsClearOnlyWhenNotRunning(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseOrganizationTenant(orgA)
	repository := store.NewRepository(db)
	if _, err := repository.EnablePluginRegistry(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	queue := func(product string) uuid.UUID {
		t.Helper()
		id, err := repository.CreatePluginImport(ctx, tenant, store.PluginImportRequest{
			SourceKind: "releases-hashicorp", Product: product, Versions: []string{"1.0.0"}, Platforms: []string{"linux_amd64"},
		}, store.PluginImportOrigin{CreatedBy: "alice", Kind: "import"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first, second := queue("packer-plugin-probe"), queue("packer-plugin-other")
	if claimed, err := repository.ClaimPluginImport(ctx, time.Minute); err != nil || claimed == nil || claimed.ID != first {
		t.Fatalf("claim = %+v %v", claimed, err)
	}
	if err := repository.DeletePluginImport(ctx, tenant, first); !errors.Is(err, store.ErrPluginImportRunning) {
		t.Fatalf("clearing a running job = %v, want ErrPluginImportRunning", err)
	}
	if err := repository.DeletePluginImport(ctx, tenant, second); err != nil {
		t.Fatalf("clearing a queued job = %v", err)
	}
	if _, err := repository.GetPluginImport(ctx, tenant, second); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("cleared job still reads: %v", err)
	}
	if err := repository.DeletePluginImport(ctx, tenant, second); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("clearing twice = %v, want ErrNotFound", err)
	}
	if err := repository.RecordPluginImport(ctx, tenant, first, "failed", []string{}); err != nil {
		t.Fatal(err)
	}
	if err := repository.DeletePluginImport(ctx, tenant, first); err != nil {
		t.Fatalf("clearing a finished job = %v", err)
	}
	if _, total, err := repository.ListPluginImports(ctx, tenant, store.PluginImportFilter{Limit: 10}); err != nil || total != 0 {
		t.Fatalf("after clearing = %d jobs, %v", total, err)
	}
}

func TestPluginPruneClaimsOnceAndClearsOnlyOldFinishedJobs(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseOrganizationTenant(orgA)
	repository := store.NewRepository(db)
	if _, err := repository.EnablePluginRegistry(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	queue := func(product string) uuid.UUID {
		t.Helper()
		id, err := repository.CreatePluginImport(ctx, tenant, store.PluginImportRequest{
			SourceKind: "releases-hashicorp", Product: product, Versions: []string{"1.0.0"}, Platforms: []string{"linux_amd64"},
		}, store.PluginImportOrigin{CreatedBy: "alice", Kind: "import"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	old, recent, queued := queue("packer-plugin-old"), queue("packer-plugin-recent"), queue("packer-plugin-queued")
	for _, id := range []uuid.UUID{old, recent} {
		if claimed, err := repository.ClaimPluginImport(ctx, time.Minute); err != nil || claimed == nil || claimed.ID != id {
			t.Fatalf("claim = %+v %v", claimed, err)
		}
		if err := repository.RecordPluginImport(ctx, tenant, id, "succeeded", []string{}); err != nil {
			t.Fatal(err)
		}
	}
	// Row-level security hides the rows from a bare connection; age the job as the tenant.
	aged, err := store.BeginOrganizationTenant(ctx, db, orgA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aged.ExecContext(ctx, `UPDATE plugin_imports SET finished_at = now() - interval '40 days' WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}
	if err := aged.Commit(); err != nil {
		t.Fatal(err)
	}
	before, err := repository.GetPluginPrune(ctx, tenant)
	if err != nil || before.Enabled || before.AgeDays != 30 || before.Schedule != "0 3 * * *" || before.WouldClear != 1 || before.Unfinished != 1 || before.Kept != 3 || before.OldestFinishedAt == nil {
		t.Fatalf("defaults = %+v %v; want off, 30 days, daily 03:00, one job to clear", before, err)
	}
	// Off: nothing is claimed. On with a run due now: exactly one claim across two callers.
	if claimed, err := repository.ClaimPluginPrune(ctx); err != nil || claimed != nil {
		t.Fatalf("claim while off = %+v %v", claimed, err)
	}
	due := time.Now().Add(-time.Minute)
	if err := repository.SetPluginPrune(ctx, tenant, true, 30, "0 3 * * *", &due); err != nil {
		t.Fatal(err)
	}
	first, err := repository.ClaimPluginPrune(ctx)
	if err != nil || first == nil || first.AgeDays != 30 || first.Schedule != "0 3 * * *" {
		t.Fatalf("first claim = %+v %v", first, err)
	}
	if second, err := repository.ClaimPluginPrune(ctx); err != nil || second != nil {
		t.Fatalf("second claim = %+v %v; the run is due once", second, err)
	}
	cleared, err := repository.PrunePluginImports(ctx, tenant, time.Now().AddDate(0, 0, -30))
	if err != nil || cleared != 1 {
		t.Fatalf("pruned %d %v, want the one old finished job", cleared, err)
	}
	next := time.Now().Add(24 * time.Hour)
	if err := repository.RecordPluginPrune(ctx, tenant, cleared, "", &next); err != nil {
		t.Fatal(err)
	}
	after, err := repository.GetPluginPrune(ctx, tenant)
	if err != nil || after.LastCleared != 1 || after.LastRunAt == nil || after.NextAt == nil || after.Kept != 2 || after.WouldClear != 0 {
		t.Fatalf("after = %+v %v", after, err)
	}
	for _, id := range []uuid.UUID{recent, queued} {
		if _, err := repository.GetPluginImport(ctx, tenant, id); err != nil {
			t.Fatalf("job %s was pruned: %v", id, err)
		}
	}
	if claimed, err := repository.ClaimPluginPrune(ctx); err != nil || claimed != nil {
		t.Fatalf("claim before the next run = %+v %v", claimed, err)
	}
}
