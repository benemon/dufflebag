//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

var runtimeBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "dufflebag-audit-runtime-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create runtime test directory:", err)
		os.Exit(1)
	}
	runtimeBinary = filepath.Join(dir, "dufflebag")
	build := exec.Command("go", "build", "-o", runtimeBinary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build real dufflebag binary: %v\n%s", err, output)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type runtimeDatabase struct {
	container *tcpostgres.PostgresContainer
	admin     *sql.DB
	appURL    string
	once      sync.Once
}

func newRuntimeDatabase(t *testing.T) *runtimeDatabase {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(
		ctx,
		"postgres:17-alpine",
		tcpostgres.WithDatabase("dufflebag"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start runtime postgres: %v", err)
	}
	adminURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("runtime postgres connection string: %v", err)
	}
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("open runtime postgres: %v", err)
	}
	if err := store.Migrate(admin); err != nil {
		_ = admin.Close()
		_ = container.Terminate(ctx)
		t.Fatalf("migrate runtime postgres: %v", err)
	}
	if _, err := admin.Exec(`
		CREATE ROLE dufflebag_app LOGIN PASSWORD 'app';
		GRANT USAGE ON SCHEMA public TO dufflebag_app;
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dufflebag_app;
	`); err != nil {
		_ = admin.Close()
		_ = container.Terminate(ctx)
		t.Fatalf("create runtime application role: %v", err)
	}
	parsed, err := url.Parse(adminURL)
	if err != nil {
		_ = admin.Close()
		_ = container.Terminate(ctx)
		t.Fatalf("parse runtime postgres URL: %v", err)
	}
	parsed.User = url.UserPassword("dufflebag_app", "app")
	database := &runtimeDatabase{container: container, admin: admin, appURL: parsed.String()}
	t.Cleanup(database.stop)
	return database
}

func (d *runtimeDatabase) stop() {
	d.once.Do(func() {
		_ = d.admin.Close()
		_ = d.container.Terminate(context.Background())
	})
}

func (d *runtimeDatabase) configureTarget(t *testing.T, path string) {
	t.Helper()
	if _, err := d.admin.Exec(`
		INSERT INTO audit_targets (id, slot, path, created_at)
		VALUES ($1, 1, $2, now())
	`, uuid.NewString(), path); err != nil {
		t.Fatalf("configure runtime audit target: %v", err)
	}
}

func TestAuditStartupRealBinaryRefusesConfiguredTargetWithoutHMACKey(t *testing.T) {
	database := newRuntimeDatabase(t)
	database.configureTarget(t, filepath.Join(t.TempDir(), "audit.log"))
	command := runtimeCommand(database.appURL, reserveAddress(t), nil)
	want := "audit initialization: DFBG_AUDIT_HMAC_KEY and DFBG_AUDIT_HMAC_KEY_VERSION are required when audit is configured"
	assertStartupRefusal(t, command, want)
}

func TestUnconfiguredRuntimeStartsAndSBOMUploadReturns503(t *testing.T) {
	database := newRuntimeDatabase(t)
	process := startRuntimeProcess(t, database.appURL)

	initialized := runtimeJSONRequest(t, process.address, http.MethodPost, "/sys/init", nil, "")
	if initialized.StatusCode != http.StatusOK {
		t.Fatalf("initialize unconfigured runtime = %d: %s", initialized.StatusCode, initialized.Body)
	}
	credentials := struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}{}
	if err := json.Unmarshal(initialized.Body, &credentials); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"grant_type": {"client_credentials"}, "audience": {"https://api.hashicorp.cloud"}}
	request, err := http.NewRequest(http.MethodPost, "http://"+process.address+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth(credentials.ClientID, credentials.ClientSecret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var tokenResponse struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&tokenResponse); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || tokenResponse.AccessToken == "" {
		t.Fatalf("token from unconfigured runtime = %d, token=%q", response.StatusCode, tokenResponse.AccessToken)
	}

	organizationResponse := runtimeJSONRequest(t, process.address, http.MethodPost,
		"/api/v1/organizations", map[string]string{"name": "runtime"}, tokenResponse.AccessToken)
	var organization struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(organizationResponse.Body, &organization); err != nil {
		t.Fatal(err)
	}
	projectResponse := runtimeJSONRequest(t, process.address, http.MethodPost,
		"/api/v1/organizations/"+organization.ID+"/projects",
		map[string]string{"name": "runtime"}, tokenResponse.AccessToken)
	var project struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(projectResponse.Body, &project); err != nil {
		t.Fatal(err)
	}
	base := "/packer/2023-01-01/organizations/" + organization.ID + "/projects/" + project.ID
	runtimeJSONRequest(t, process.address, http.MethodPut, base+"/buckets",
		map[string]string{"name": "images"}, tokenResponse.AccessToken)
	runtimeJSONRequest(t, process.address, http.MethodPost, base+"/buckets/images/versions",
		map[string]string{"fingerprint": "fp", "template_type": "HCL2"}, tokenResponse.AccessToken)
	buildResponse := runtimeJSONRequest(t, process.address, http.MethodPost,
		base+"/buckets/images/versions/fp/builds",
		map[string]string{"component_type": "docker"}, tokenResponse.AccessToken)
	var build struct {
		Build struct {
			ID string `json:"id"`
		} `json:"build"`
	}
	if err := json.Unmarshal(buildResponse.Body, &build); err != nil {
		t.Fatal(err)
	}
	upload := runtimeJSONRequest(t, process.address, http.MethodPut,
		base+"/buckets/images/versions/fp/builds/"+build.Build.ID+"/sboms",
		map[string]string{"compressed_sbom": base64.StdEncoding.EncodeToString([]byte("zstd"))},
		tokenResponse.AccessToken)
	if upload.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(upload.Body), `"code":14`) {
		t.Fatalf("unconfigured runtime SBOM upload = %d %s, want 503/code 14", upload.StatusCode, upload.Body)
	}
	process.stop(t)
}

func TestConfiguredRuntimeRefusesUnavailableObjectStorageWithoutLeakingSecret(t *testing.T) {
	database := newRuntimeDatabase(t)
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer s3.Close()
	secret := "runtime-object-storage-secret"
	command := runtimeCommand(database.appURL, reserveAddress(t), map[string]string{
		"DFBG_OBJECT_STORAGE_ENDPOINT":   s3.URL,
		"DFBG_OBJECT_STORAGE_REGION":     "us-east-1",
		"DFBG_OBJECT_STORAGE_BUCKET":     "sboms",
		"DFBG_OBJECT_STORAGE_ACCESS_KEY": "access",
		"DFBG_OBJECT_STORAGE_SECRET_KEY": secret,
	})
	output := assertStartupRefusal(t, command, "object storage availability:")
	if strings.Contains(output, secret) {
		t.Fatalf("startup availability refusal exposed object-storage secret: %s", output)
	}
}

func TestAuditStartupRealBinaryRefusesUnwritableConfiguredTarget(t *testing.T) {
	database := newRuntimeDatabase(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("make target parent world-writable: %v", err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	database.configureTarget(t, filepath.Join(dir, "audit.log"))
	command := runtimeCommand(database.appURL, reserveAddress(t), map[string]string{
		"DFBG_AUDIT_HMAC_KEY": "runtime-audit-key", "DFBG_AUDIT_HMAC_KEY_VERSION": "runtime-v1",
	})
	assertStartupRefusal(t, command, "audit initialization: open configured target", "world-writable")
}

func TestAuditRuntimeRealSIGHUPReopensAfterRename(t *testing.T) {
	database := newRuntimeDatabase(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	rotated := filepath.Join(dir, "audit.log.1")
	database.configureTarget(t, path)
	process := startRuntimeProcess(t, database.appURL)

	postInit(t, process.address, http.StatusOK)
	if err := os.Rename(path, rotated); err != nil {
		t.Fatalf("rename active audit file: %v", err)
	}
	if err := process.command.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatalf("send real SIGHUP: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		info, err := os.Stat(path)
		return err == nil && info.Mode().IsRegular()
	}, "SIGHUP did not create the replacement audit file")
	postInit(t, process.address, http.StatusConflict)
	process.stop(t)

	assertAuditPairFile(t, rotated, http.StatusOK)
	assertAuditPairFile(t, path, http.StatusConflict)
}

func TestAuditRuntimeDatabaseOutageStillWritesCompletePair(t *testing.T) {
	database := newRuntimeDatabase(t)
	path := filepath.Join(t.TempDir(), "audit.log")
	database.configureTarget(t, path)
	process := startRuntimeProcess(t, database.appURL)
	database.stop()

	postInit(t, process.address, http.StatusInternalServerError)
	process.stop(t)
	assertAuditPairFile(t, path, http.StatusInternalServerError)
}

func TestAuditRuntimeDegradedBrokerReturns503WithHealthyDatabase(t *testing.T) {
	database := newRuntimeDatabase(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	rotated := filepath.Join(dir, "audit.log.1")
	database.configureTarget(t, path)
	process := startRuntimeProcess(t, database.appURL)

	if err := os.Rename(path, rotated); err != nil {
		t.Fatalf("rename active audit file: %v", err)
	}
	if err := os.Symlink(rotated, path); err != nil {
		t.Fatalf("replace audit path with symlink: %v", err)
	}
	if err := process.command.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatalf("send degrading SIGHUP: %v", err)
	}
	var health struct {
		Database bool   `json:"database"`
		Audit    string `json:"audit"`
	}
	waitFor(t, 5*time.Second, func() bool {
		response, err := http.Get("http://" + process.address + "/sys/health")
		if err != nil {
			return false
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusServiceUnavailable {
			return false
		}
		return json.NewDecoder(response.Body).Decode(&health) == nil
	}, "real degraded broker did not answer health with 503")
	if !health.Database || health.Audit != "degraded" {
		t.Fatalf("degraded health = database %t, audit %q; want healthy DB and degraded audit", health.Database, health.Audit)
	}
	process.stop(t)
}

type runtimeProcess struct {
	command *exec.Cmd
	address string
	log     *os.File
	stopped bool
}

func startRuntimeProcess(t *testing.T, databaseURL string) *runtimeProcess {
	t.Helper()
	address := reserveAddress(t)
	command := runtimeCommand(databaseURL, address, map[string]string{
		"DFBG_AUDIT_HMAC_KEY": "runtime-audit-key", "DFBG_AUDIT_HMAC_KEY_VERSION": "runtime-v1",
		"DFBG_SHUTDOWN_GRACE_PERIOD": "1s",
	})
	logFile, err := os.Create(filepath.Join(t.TempDir(), "dufflebag.log"))
	if err != nil {
		t.Fatalf("create runtime process log: %v", err)
	}
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start real dufflebag binary: %v", err)
	}
	process := &runtimeProcess{command: command, address: address, log: logFile}
	t.Cleanup(func() {
		if !process.stopped {
			_ = process.command.Process.Kill()
			_ = process.command.Wait()
		}
		_ = process.log.Close()
	})
	waitFor(t, 10*time.Second, func() bool {
		response, err := http.Get("http://" + address + "/sys/health")
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		return true
	}, "real dufflebag binary did not become reachable; "+process.logs())
	return process
}

func (p *runtimeProcess) stop(t *testing.T) {
	t.Helper()
	if p.stopped {
		return
	}
	if err := p.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal real dufflebag binary: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- p.command.Wait() }()
	select {
	case err := <-exited:
		p.stopped = true
		if err != nil {
			t.Fatalf("real dufflebag binary shutdown: %v\n%s", err, p.logs())
		}
	case <-time.After(2 * time.Second):
		_ = p.command.Process.Kill()
		<-exited
		p.stopped = true
		t.Fatalf("real dufflebag binary exceeded its 1s shutdown grace\n%s", p.logs())
	}
}

func (p *runtimeProcess) logs() string {
	_ = p.log.Sync()
	content, _ := os.ReadFile(p.log.Name())
	return string(content)
}

func runtimeCommand(databaseURL, address string, extra map[string]string) *exec.Cmd {
	values := map[string]string{
		"DFBG_DATABASE_URL":      databaseURL,
		"DFBG_HTTP_ADDR":         address,
		"DFBG_TOKEN_SIGNING_KEY": "runtime-token-signing-key-at-least-32-bytes",
	}
	for key, value := range extra {
		values[key] = value
	}
	environment := make([]string, 0, len(os.Environ())+len(values))
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if _, replaced := values[key]; !replaced && key != "DFBG_AUDIT_HMAC_KEY" &&
			key != "DFBG_AUDIT_HMAC_KEY_VERSION" && key != "DFBG_SHUTDOWN_GRACE_PERIOD" &&
			key != "DFBG_BAGDROP_CREDENTIAL_KEY" &&
			!strings.HasPrefix(key, "DFBG_OBJECT_STORAGE_") {
			environment = append(environment, entry)
		}
	}
	for key, value := range values {
		environment = append(environment, key+"="+value)
	}
	command := exec.Command(runtimeBinary)
	command.Env = environment
	return command
}

type runtimeResponse struct {
	StatusCode int
	Body       []byte
}

func runtimeJSONRequest(
	t *testing.T,
	address, method, path string,
	body any,
	token string,
) runtimeResponse {
	t.Helper()
	var encoded io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		encoded = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, "http://"+address+path, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s %s = %d: %s", method, path, response.StatusCode, data)
		}
	}
	return runtimeResponse{StatusCode: response.StatusCode, Body: data}
}

func assertStartupRefusal(t *testing.T, command *exec.Cmd, fragments ...string) string {
	t.Helper()
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatalf("start real binary for refusal: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	select {
	case err := <-exited:
		if err == nil {
			t.Fatalf("real binary served instead of refusing startup: %s", output.String())
		}
	case <-time.After(2 * time.Second):
		_ = command.Process.Kill()
		<-exited
		t.Fatalf("real binary did not refuse startup within 2s: %s", output.String())
	}
	for _, fragment := range fragments {
		if !strings.Contains(output.String(), fragment) {
			t.Fatalf("startup refusal omitted %q: %s", fragment, output.String())
		}
	}
	return output.String()
}

func reserveAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve runtime address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release runtime address: %v", err)
	}
	return address
}

func postInit(t *testing.T, address string, wantStatus int) {
	t.Helper()
	response, err := http.Post("http://"+address+"/sys/init", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /sys/init: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != wantStatus {
		t.Fatalf("POST /sys/init = %d, want %d", response.StatusCode, wantStatus)
	}
}

func assertAuditPairFile(t *testing.T, path string, wantStatus int) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open audit pair %q: %v", path, err)
	}
	defer func() { _ = file.Close() }()
	var records []map[string]any
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("decode audit record %q: %v", scanner.Bytes(), err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan audit pair: %v", err)
	}
	if len(records) != 2 || records[0]["kind"] != "request" || records[1]["kind"] != "response" ||
		records[0]["correlation_id"] != records[1]["correlation_id"] ||
		records[1]["status"] != float64(wantStatus) {
		t.Fatalf("audit pair in %q = %#v, want correlated request/response with status %d", path, records, wantStatus)
	}
}

func waitFor(t *testing.T, within time.Duration, condition func() bool, failure string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal(failure)
}
