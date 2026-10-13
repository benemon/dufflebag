package pluginread

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/domain/plugin"
	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

type countedDownload struct {
	tenant   store.OrganizationTenant
	pluginID uuid.UUID
	version  string
	platform string
	day      time.Time
}

type fakeRepository struct {
	versions  map[string][]string
	files     map[string]store.ServedPluginFile
	objects   map[string]string
	openError error
	counts    []countedDownload
}

func (f *fakeRepository) ServedPluginVersions(_ context.Context, organization, name string) ([]string, error) {
	versions, ok := f.versions[organization+"/"+name]
	if !ok {
		return nil, registry.ErrNotFound
	}
	return versions, nil
}

func (f *fakeRepository) ServedPluginFile(
	_ context.Context, organization, name, version string, kind store.PluginFileKind, filename string,
) (store.ServedPluginFile, error) {
	file, ok := f.files[strings.Join([]string{organization, name, version, filename}, "/")]
	if !ok {
		return store.ServedPluginFile{}, registry.ErrNotFound
	}
	return file, nil
}

func (f *fakeRepository) OpenPluginObject(_ context.Context, key string) (io.ReadCloser, error) {
	if f.openError != nil {
		return nil, f.openError
	}
	return io.NopCloser(strings.NewReader(f.objects[key])), nil
}

func (f *fakeRepository) CountPluginDownload(
	_ context.Context, tenant store.OrganizationTenant, pluginID uuid.UUID, version, platform string, day time.Time,
) error {
	f.counts = append(f.counts, countedDownload{tenant: tenant, pluginID: pluginID, version: version, platform: platform, day: day})
	return nil
}

func amazonRepository() *fakeRepository {
	zip := "packer-plugin-amazon_1.8.2_linux_amd64.zip"
	organizationID := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	pluginID := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	served := func(file store.ServedPluginFile) store.ServedPluginFile {
		file.OrganizationID, file.PluginID = organizationID, pluginID
		return file
	}
	return &fakeRepository{
		versions: map[string][]string{"acme/amazon": {"1.8.2", "1.8.1"}},
		files: map[string]store.ServedPluginFile{
			"acme/amazon/1.8.2/" + plugin.SumsName("amazon", "1.8.2"):     served(store.ServedPluginFile{ObjectKey: "sums", Size: 4}),
			"acme/amazon/1.8.2/" + plugin.ManifestName("amazon", "1.8.2"): served(store.ServedPluginFile{Content: plugin.RenderManifest("5.0")}),
			"acme/amazon/1.8.2/" + zip:                                    served(store.ServedPluginFile{ObjectKey: "zip", Size: 3}),
		},
		objects: map[string]string{"sums": "sums", "zip": "zip"},
	}
}

type auditTrail struct{ records []map[string]any }

func (a *auditTrail) Write(encoded []byte) error {
	var record map[string]any
	if err := json.Unmarshal(encoded, &record); err != nil {
		return err
	}
	a.records = append(a.records, record)
	return nil
}

type resolver struct{}

func (resolver) Resolve(*http.Request) audit.Descriptor {
	return audit.Descriptor{RouteID: "root.plugins", Operation: "plugin.read", TargetType: "plugin_file"}
}

func get(t *testing.T, repository Repository, method, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	trail := &auditTrail{}
	handler := audit.NewHTTPHandler(trail, resolver{}, NewHandler(repository, slog.Default()),
		func() (string, []byte) { return "1", []byte("0123456789abcdef0123456789abcdef") })
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
	if len(trail.records) != 2 {
		t.Fatalf("audit records = %d, want request and response", len(trail.records))
	}
	return response, trail.records[1]
}

func TestServesTheFourGetterRequests(t *testing.T) {
	repository := amazonRepository()
	index, record := get(t, repository, http.MethodGet, "/plugins/acme/packer-plugin-amazon/index.json")
	var parsed struct {
		Versions map[string]struct{} `json:"versions"`
	}
	if index.Code != http.StatusOK || json.Unmarshal(index.Body.Bytes(), &parsed) != nil || len(parsed.Versions) != 2 {
		t.Fatalf("index = %d %s", index.Code, index.Body.String())
	}
	if record["reason"] != "served" || record["target_id"] != "acme/packer-plugin-amazon/index.json" {
		t.Fatalf("index audit = %#v", record)
	}
	for path, want := range map[string]string{
		"/plugins/acme/packer-plugin-amazon/1.8.2/packer-plugin-amazon_1.8.2_SHA256SUMS":      "sums",
		"/plugins/acme/packer-plugin-amazon/1.8.2/packer-plugin-amazon_1.8.2_linux_amd64.zip": "zip",
		"/plugins/acme/packer-plugin-amazon/1.8.2/packer-plugin-amazon_1.8.2_manifest.json":   string(plugin.RenderManifest("5.0")),
	} {
		response, record := get(t, repository, http.MethodGet, path)
		if response.Code != http.StatusOK || response.Body.String() != want ||
			response.Header().Get("Content-Length") != strconv.Itoa(len(want)) || record["reason"] != "served" {
			t.Fatalf("%s = %d %q length %s, audit %#v", path, response.Code, response.Body.String(), response.Header().Get("Content-Length"), record)
		}
	}
}

func TestEverythingUnservedAnswersTheSame404(t *testing.T) {
	repository := amazonRepository()
	reference, _ := get(t, repository, http.MethodGet, "/plugins/acme/packer-plugin-unknown/index.json")
	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "/plugins/other/packer-plugin-amazon/index.json"},
		{http.MethodGet, "/plugins/acme/packer-plugin-amazon/9.9.9/packer-plugin-amazon_9.9.9_SHA256SUMS"},
		{http.MethodGet, "/plugins/acme/packer-plugin-amazon/1.8.2/packer-plugin-amazon_1.8.2_SHA256SUMS.sig"},
		{http.MethodGet, "/plugins/acme/packer-plugin-amazon/1.8.2/packer-plugin-amazon_1.8.2_plan9_amd64.zip"},
		{http.MethodGet, "/plugins/acme/packer-plugin-amazon/1.8.2/README.md"},
		{http.MethodGet, "/plugins/acme/amazon/index.json"},
		{http.MethodGet, "/plugins/acme"},
		{http.MethodGet, "/plugins/"},
		{http.MethodGet, "/plugins/acme/packer-plugin-amazon/1.8.2/extra/packer-plugin-amazon_1.8.2_SHA256SUMS"},
		{http.MethodPost, "/plugins/acme/packer-plugin-amazon/index.json"},
		{http.MethodHead, "/plugins/acme/packer-plugin-amazon/index.json"},
	} {
		response, record := get(t, repository, request.method, request.path)
		if response.Code != http.StatusNotFound || response.Body.String() != reference.Body.String() || record["reason"] != "not_found" {
			t.Fatalf("%s %s = %d %q, audit %#v; want the uniform 404", request.method, request.path, response.Code, response.Body.String(), record)
		}
	}
}

func TestStorageFailureIsNotDisguisedAsAbsence(t *testing.T) {
	repository := amazonRepository()
	repository.openError = errors.New("object storage is unavailable")
	response, record := get(t, repository, http.MethodGet, "/plugins/acme/packer-plugin-amazon/1.8.2/packer-plugin-amazon_1.8.2_linux_amd64.zip")
	if response.Code != http.StatusServiceUnavailable || record["reason"] != "unavailable" {
		t.Fatalf("storage failure = %d, audit %#v", response.Code, record)
	}
}

func TestAServedZipCountsOnceWithTheRightPluginVersionAndPlatform(t *testing.T) {
	repository := amazonRepository()
	response, _ := get(t, repository, http.MethodGet, "/plugins/acme/packer-plugin-amazon/1.8.2/packer-plugin-amazon_1.8.2_linux_amd64.zip")
	if response.Code != http.StatusOK || len(repository.counts) != 1 {
		t.Fatalf("served zip status = %d, counts = %d; want 200 and one count", response.Code, len(repository.counts))
	}
	count := repository.counts[0]
	wantPlugin := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	if count.tenant.OrganizationID != uuid.MustParse("00000000-0000-4000-8000-000000000001") ||
		count.pluginID != wantPlugin || count.version != "1.8.2" || count.platform != "linux_amd64" ||
		count.day.Location() != time.UTC || count.day.Hour() != 0 || count.day.Minute() != 0 || count.day.Second() != 0 {
		t.Fatalf("count = %+v, want the served plugin, version, platform and UTC day", count)
	}
}

func TestIndexSumsSignatureAndManifestServesCountNothing(t *testing.T) {
	repository := amazonRepository()
	reference := repository.files["acme/amazon/1.8.2/"+plugin.SumsName("amazon", "1.8.2")]
	reference.ObjectKey, reference.Size = "signature", 9
	repository.files["acme/amazon/1.8.2/"+plugin.SignatureName("amazon", "1.8.2")] = reference
	repository.objects["signature"] = "signature"
	paths := []string{
		"/plugins/acme/packer-plugin-amazon/index.json",
		"/plugins/acme/packer-plugin-amazon/1.8.2/" + plugin.SumsName("amazon", "1.8.2"),
		"/plugins/acme/packer-plugin-amazon/1.8.2/" + plugin.SignatureName("amazon", "1.8.2"),
		"/plugins/acme/packer-plugin-amazon/1.8.2/" + plugin.ManifestName("amazon", "1.8.2"),
	}
	for _, path := range paths {
		response, _ := get(t, repository, http.MethodGet, path)
		if response.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", path, response.Code)
		}
	}
	if len(repository.counts) != 0 {
		t.Fatalf("non-zip serves recorded %d downloads, want none", len(repository.counts))
	}
}

func TestAnInterruptedStreamCountsNothing(t *testing.T) {
	repository := amazonRepository()
	key := "acme/amazon/1.8.2/packer-plugin-amazon_1.8.2_linux_amd64.zip"
	file := repository.files[key]
	file.Size++
	repository.files[key] = file
	_, record := get(t, repository, http.MethodGet, "/plugins/acme/packer-plugin-amazon/1.8.2/packer-plugin-amazon_1.8.2_linux_amd64.zip")
	if record["reason"] != "stream_interrupted" || len(repository.counts) != 0 {
		t.Fatalf("interrupted audit = %#v, counts = %d; want interrupted and no count", record, len(repository.counts))
	}
}

func TestA404CountsNothing(t *testing.T) {
	repository := amazonRepository()
	response, _ := get(t, repository, http.MethodGet, "/plugins/acme/packer-plugin-amazon/1.8.2/packer-plugin-amazon_1.8.2_plan9_amd64.zip")
	if response.Code != http.StatusNotFound || len(repository.counts) != 0 {
		t.Fatalf("missing zip status = %d, counts = %d; want 404 and no count", response.Code, len(repository.counts))
	}
}
