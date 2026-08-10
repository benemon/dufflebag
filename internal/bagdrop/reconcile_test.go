package bagdrop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBackoffDelay(t *testing.T) {
	interval := 5 * time.Minute
	for _, test := range []struct {
		failures int
		want     time.Duration
	}{
		{1, 10 * time.Minute}, {2, 20 * time.Minute}, {3, 40 * time.Minute},
		{4, time.Hour}, {20, time.Hour},
	} {
		if got := backoffDelay(interval, test.failures); got != test.want {
			t.Errorf("backoffDelay(%s, %d) = %s, want %s", interval, test.failures, got, test.want)
		}
	}
}

func TestAssociationSyncStatus(t *testing.T) {
	now := time.Now()
	failure := "failed"
	for _, test := range []struct {
		association Association
		want        SyncStatus
	}{
		{Association{State: AssociationActive}, SyncPending},
		{Association{State: AssociationActive, LastSyncedAt: &now}, SyncSynced},
		{Association{State: AssociationActive, LastSyncedAt: &now, LastSyncError: &failure}, SyncPending},
		{Association{State: AssociationPendingRemoval, LastSyncedAt: &now}, SyncRemoving},
	} {
		if got := test.association.SyncStatus(); got != test.want {
			t.Errorf("SyncStatus(%#v) = %q, want %q", test.association, got, test.want)
		}
	}
}

func TestReconcileEmptyDestinationConverges(t *testing.T) {
	reconciler, repository, run, writer := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images")}
	repository.snapshots["images"] = testSnapshot("images")

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	if repository.successes["images"] == 0 || repository.failures["images"] != "" {
		t.Fatalf("association status: successes=%v failures=%v", repository.successes, repository.failures)
	}
	want := []string{
		"mark:images", "get-bucket:images", "create-bucket:images", "get-version:fp-1",
		"create-version:fp-1", "list-builds:fp-1", "create-build:amazon-ebs", "update-build:amazon-ebs",
		"list-channels:images",
	}
	if strings.Join(repository.events, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", repository.events, want)
	}
	if len(run.updatedArtifacts) != 1 || run.updatedArtifacts[0].ExternalIdentifier != "ami-1" {
		t.Fatalf("updated artifacts = %#v", run.updatedArtifacts)
	}
	if len(writer.records) != 8 {
		t.Fatalf("audit records = %d, want four mutation pairs", len(writer.records))
	}
}

func TestReconcileMirrorsVersionsBeforeChannelAssignmentsAgainstHCP2023FakeDestination(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Fixture source: internal/compat/hcpauth/handler.go tokenResponse.
		_, _ = w.Write([]byte(tokenSuccessFixture))
	}))
	defer auth.Close()
	var bucketExists, versionExists, buildExists, buildDone bool
	var productionExists bool
	productionFingerprint := ""
	latestMutations := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		path := request.URL.Path
		switch {
		case request.Method == http.MethodGet && strings.HasSuffix(path, "/buckets/images"):
			if !bucketExists {
				// Fixture source: internal/compat/hcp2023 writeBucketNotFound.
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":5,"message":"bucket not found","details":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"bucket":{"description":"images description"}}`))
		case request.Method == http.MethodPut && strings.HasSuffix(path, "/buckets"):
			bucketExists = true
			_, _ = w.Write([]byte(`{"bucket":{"name":"images","description":"images description"}}`))
		case request.Method == http.MethodGet && strings.HasSuffix(path, "/versions/fp-1"):
			if !versionExists {
				// Fixture source: internal/compat/hcp2023 writeVersionNotFound.
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"code":10,"message":"version not found","details":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"version":{"fingerprint":"fp-1","status":"VERSION_ACTIVE"}}`))
		case request.Method == http.MethodPost && strings.HasSuffix(path, "/versions"):
			versionExists = true
			_, _ = w.Write([]byte(`{"version":{"fingerprint":"fp-1","status":"VERSION_RUNNING"}}`))
		case request.Method == http.MethodGet && strings.HasSuffix(path, "/versions/fp-1/builds"):
			if !buildExists {
				_, _ = w.Write([]byte(`{"builds":[]}`))
				return
			}
			status := "BUILD_PENDING"
			if buildDone {
				status = "BUILD_DONE"
			}
			_, _ = w.Write([]byte(`{"builds":[{"id":"remote-build","component_type":"amazon-ebs","status":"` + status + `"}]}`))
		case request.Method == http.MethodPost && strings.HasSuffix(path, "/versions/fp-1/builds"):
			var body struct {
				ComponentType string `json:"component_type"`
				PackerRunUUID string `json:"packer_run_uuid"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode build create: %v", err)
			}
			if body.ComponentType != "amazon-ebs" || body.PackerRunUUID != "run-uuid" {
				t.Errorf("build create = %#v", body)
			}
			buildExists = true
			_, _ = w.Write([]byte(`{"build":{"id":"remote-build","component_type":"amazon-ebs","status":"BUILD_PENDING"}}`))
		case request.Method == http.MethodPatch && strings.HasSuffix(path, "/builds/remote-build"):
			var body struct {
				Status    string `json:"status"`
				Artifacts []struct {
					ExternalIdentifier string `json:"external_identifier"`
					Region             string `json:"region"`
				} `json:"artifacts"`
				Metadata map[string]any `json:"metadata"`
				// Live HCP refuses re-setting create-time fields on update:
				// "You cannot override a build's Platform if it has already
				// been set" (409/code-6, observed 2026-08-10). The fake
				// carries that contract so the adapter cannot regress.
				Platform      *string `json:"platform"`
				PackerRunUUID *string `json:"packer_run_uuid"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode build update: %v", err)
			}
			if body.Platform != nil {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"code":6, "message":"You cannot override a build's Platform if it has already been set.", "details":[]}`))
				return
			}
			if body.PackerRunUUID != nil {
				t.Errorf("build update resent create-time field packer_run_uuid")
			}
			if body.Status != "BUILD_DONE" || len(body.Artifacts) != 1 ||
				body.Artifacts[0].ExternalIdentifier != "ami-1" || body.Artifacts[0].Region != "eu-west-2" ||
				body.Metadata["packer"] == nil {
				t.Errorf("build update = %#v", body)
			}
			buildDone = true
			_, _ = w.Write([]byte(`{"build":{"id":"remote-build","component_type":"amazon-ebs","status":"BUILD_DONE"}}`))
		case request.Method == http.MethodGet && strings.HasSuffix(path, "/buckets/images/channels"):
			// Fixture source: internal/compat/hcp2023 renderChannel and
			// docs/compatibility.md managed-latest capture (Appendix A.2).
			production := ""
			if productionExists {
				version := "null"
				if productionFingerprint != "" {
					version = `{"fingerprint":"` + productionFingerprint + `"}`
				}
				production = `,{"name":"production","managed":false,"version":` + version + `}`
			}
			_, _ = w.Write([]byte(`{"channels":[{"name":"latest","managed":true,"version":{"fingerprint":"fp-1"}}` + production + `]}`))
		case request.Method == http.MethodPost && strings.HasSuffix(path, "/buckets/images/channels"):
			var body struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode channel create: %v", err)
			}
			if body.Name != "production" {
				t.Errorf("channel create = %#v", body)
			}
			if productionExists {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(alreadyExistsFixture))
				return
			}
			productionExists = true
			_, _ = w.Write([]byte(`{"channel":{"name":"production","managed":false,"version":null}}`))
		case request.Method == http.MethodPatch && strings.HasSuffix(path, "/channels/latest"):
			latestMutations++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":9,"message":"Can't update channel assignment on channel \"latest\". This channel is managed by HCP Packer","details":[]}`))
		case request.Method == http.MethodDelete && strings.HasSuffix(path, "/channels/latest"):
			latestMutations++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":3,"message":"Can't delete managed channel latest, it's controlled by HCP Packer","details":[]}`))
		case request.Method == http.MethodPatch && strings.HasSuffix(path, "/channels/production"):
			var body struct {
				VersionFingerprint string `json:"version_fingerprint"`
				UpdateMask         string `json:"update_mask"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode channel update: %v", err)
			}
			if body.UpdateMask == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":3,"message":"body: (update_mask: field mask: must be set.).","details":[{"@type":"type.googleapis.com/google.rpc.BadRequest","field_violations":[{"field":"body.update_mask","description":"field mask: must be set","reason":"","localized_message":null}]}]}`))
				return
			}
			if body.UpdateMask != "versionFingerprint" {
				t.Errorf("channel update mask = %q", body.UpdateMask)
			}
			if body.VersionFingerprint != "" && !versionExists {
				// Fixture source: internal/compat/hcp2023 writeVersionNotFound
				// and docs/compatibility.md version identity code 10.
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"code":10,"message":"version not found","details":[]}`))
				return
			}
			productionFingerprint = body.VersionFingerprint
			_, _ = w.Write([]byte(`{"channel":{"name":"production","managed":false,"version":{"fingerprint":"` + productionFingerprint + `"}}}`))
		default:
			t.Errorf("unexpected destination request %s %s", request.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()

	reconciler, repository, _, _ := newTestReconciler(t, "secret")
	reconciler.adapters[AdapterHCPPacker] = NewHCPPackerAdapter(auth.URL, api.URL)
	repository.associations = []Association{testAssociation("images")}
	repository.snapshots["images"] = testSnapshot("images")
	repository.snapshots["images"].Channels = []ChannelSnapshot{{
		Name: "production", AssignedVersionFingerprint: fingerprintPointer("fp-1"),
	}}
	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	if !bucketExists || !versionExists || !buildDone || !productionExists ||
		productionFingerprint != "fp-1" || latestMutations != 0 || repository.successes["images"] != 1 {
		t.Fatalf("destination state bucket=%v version=%v build_done=%v channel=%v/%q latest_mutations=%d successes=%v",
			bucketExists, versionExists, buildDone, productionExists, productionFingerprint, latestMutations, repository.successes)
	}
}

func fingerprintPointer(value string) *string { return &value }

func TestReconcilePartiallyExistingPushesOnlyBuilds(t *testing.T) {
	reconciler, repository, run, writer := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images")}
	repository.snapshots["images"] = testSnapshot("images")
	run.buckets["images"] = RemoteBucket{Description: "images description"}
	run.versions["fp-1"] = true

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(repository.events, ",")
	if strings.Contains(joined, "create-bucket") || strings.Contains(joined, "create-version") {
		t.Fatalf("existing bucket/version were mutated: %v", repository.events)
	}
	if len(writer.records) != 4 {
		t.Fatalf("audit records = %d, want build create/update pairs", len(writer.records))
	}
}

func TestReconcileConvergesOrdinaryChannelPointers(t *testing.T) {
	for _, test := range []struct {
		name          string
		remote        *string
		local         *string
		remoteExists  bool
		wantCreate    bool
		wantUpdate    string
		wantAuditPair int
	}{
		{name: "absent created and assigned", local: fingerprintPointer("fp-1"), wantCreate: true, wantUpdate: "production:fp-1", wantAuditPair: 4},
		{name: "unassigned remote assigned locally", remoteExists: true, local: fingerprintPointer("fp-1"), wantUpdate: "production:fp-1", wantAuditPair: 2},
		{name: "drift overwritten", remoteExists: true, remote: fingerprintPointer("fp-old"), local: fingerprintPointer("fp-1"), wantUpdate: "production:fp-1", wantAuditPair: 2},
		{name: "remote assignment cleared", remoteExists: true, remote: fingerprintPointer("fp-old"), wantUpdate: "production:clear", wantAuditPair: 2},
		{name: "equal pointer untouched", remoteExists: true, remote: fingerprintPointer("fp-1"), local: fingerprintPointer("fp-1")},
	} {
		t.Run(test.name, func(t *testing.T) {
			reconciler, repository, run, writer := newTestReconciler(t, "secret")
			repository.associations = []Association{testAssociation("images")}
			repository.snapshots["images"] = &BucketSnapshot{
				Name:     "images",
				Versions: []VersionSnapshot{{Fingerprint: "fp-1", TemplateType: "HCL2"}},
				Channels: []ChannelSnapshot{{Name: "production", AssignedVersionFingerprint: test.local}},
			}
			run.buckets["images"] = RemoteBucket{}
			run.versions["fp-1"] = true
			run.channels["images"] = map[string]RemoteChannel{
				"latest": {Name: "latest", Managed: true, AssignedVersionFingerprint: fingerprintPointer("fp-1")},
			}
			if test.remoteExists {
				run.channels["images"]["production"] = RemoteChannel{
					Name: "production", AssignedVersionFingerprint: test.remote,
				}
			}

			if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
				t.Fatal(err)
			}
			if gotCreate := len(run.createdChannels) != 0; gotCreate != test.wantCreate {
				t.Fatalf("created channels = %v, want create %v", run.createdChannels, test.wantCreate)
			}
			if got := strings.Join(run.updatedChannels, ","); got != test.wantUpdate {
				t.Fatalf("updated channels = %q, want %q", got, test.wantUpdate)
			}
			if strings.Contains(strings.Join(run.updatedChannels, ","), "latest") {
				t.Fatalf("managed latest was mutated: %v", run.updatedChannels)
			}
			if len(writer.records) != test.wantAuditPair {
				t.Fatalf("audit records = %d, want %d", len(writer.records), test.wantAuditPair)
			}
			if repository.successes["images"] != 1 {
				t.Fatalf("association was not synced: %v", repository.successes)
			}
		})
	}
}

func TestReconcileChannelMutationsAreAuditFailClosed(t *testing.T) {
	for _, test := range []struct {
		name         string
		remoteExists bool
		local        *string
	}{
		{name: "create", remoteExists: false},
		{name: "update", remoteExists: true, local: fingerprintPointer("fp-1")},
	} {
		t.Run(test.name, func(t *testing.T) {
			reconciler, repository, run, writer := newTestReconciler(t, "secret")
			repository.associations = []Association{testAssociation("images")}
			repository.snapshots["images"] = &BucketSnapshot{
				Name: "images", Channels: []ChannelSnapshot{{Name: "production", AssignedVersionFingerprint: test.local}},
			}
			if test.local != nil {
				repository.snapshots["images"].Versions = []VersionSnapshot{{Fingerprint: "fp-1"}}
				run.versions["fp-1"] = true
			}
			run.buckets["images"] = RemoteBucket{}
			run.channels["images"] = map[string]RemoteChannel{"latest": {Name: "latest", Managed: true}}
			if test.remoteExists {
				run.channels["images"]["production"] = RemoteChannel{Name: "production"}
			}
			writer.failAt = 1

			err := reconciler.ReconcileProject(context.Background(), repository.project)
			if !errors.Is(err, ErrAuditUnavailable) {
				t.Fatalf("error = %v, want audit unavailable", err)
			}
			if len(run.createdChannels) != 0 || len(run.updatedChannels) != 0 {
				t.Fatalf("channel mutation ran without request audit: creates=%v updates=%v",
					run.createdChannels, run.updatedChannels)
			}
		})
	}
}

func TestReconcileChannelUpdateAuditDistinguishesAssignAndClear(t *testing.T) {
	for _, test := range []struct {
		name   string
		local  *string
		remote *string
		detail string
	}{
		{name: "assign", local: fingerprintPointer("fp-1"), detail: "assign version fingerprint fp-1"},
		{name: "clear", remote: fingerprintPointer("fp-old"), detail: "clear assignment"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reconciler, repository, run, writer := newTestReconciler(t, "secret")
			repository.associations = []Association{testAssociation("images")}
			repository.snapshots["images"] = &BucketSnapshot{
				Name: "images", Channels: []ChannelSnapshot{{Name: "production", AssignedVersionFingerprint: test.local}},
			}
			if test.local != nil {
				repository.snapshots["images"].Versions = []VersionSnapshot{{Fingerprint: "fp-1"}}
				run.versions["fp-1"] = true
			}
			run.buckets["images"] = RemoteBucket{}
			run.channels["images"] = map[string]RemoteChannel{
				"production": {Name: "production", AssignedVersionFingerprint: test.remote},
			}
			if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
				t.Fatal(err)
			}
			for _, encoded := range writer.records {
				var event map[string]any
				if err := json.Unmarshal(encoded, &event); err != nil {
					t.Fatal(err)
				}
				if event["operation"] != "bagdrop.sync.channel.update" || event["detail"] != test.detail {
					t.Fatalf("channel update audit = %#v", event)
				}
			}
		})
	}
}

func TestReconcileUnknownAssignmentTargetIsAssociationFailure(t *testing.T) {
	reconciler, repository, run, _ := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images")}
	repository.snapshots["images"] = &BucketSnapshot{
		Name:     "images",
		Versions: []VersionSnapshot{{Fingerprint: "fp-1"}},
		Channels: []ChannelSnapshot{{
			Name: "production", AssignedVersionFingerprint: fingerprintPointer("fp-1"),
		}},
	}
	run.buckets["images"] = RemoteBucket{}
	run.versions["fp-1"] = true
	run.channels["images"] = map[string]RemoteChannel{
		"production": {Name: "production"},
	}
	run.updateChannelError = &AdapterError{
		StatusCode: http.StatusConflict, Code: 10, Summary: "version not found",
	}

	err := reconciler.ReconcileProject(context.Background(), repository.project)
	if err == nil || repository.successes["images"] != 0 ||
		!strings.Contains(repository.failures["images"], "HTTP 409 code 10: version not found") {
		t.Fatalf("unknown target result error=%v successes=%v failures=%v",
			err, repository.successes, repository.failures)
	}
}

func TestReconcileContinuesPastAssociationFailure(t *testing.T) {
	reconciler, repository, run, _ := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("broken"), testAssociation("healthy")}
	repository.snapshots["broken"] = &BucketSnapshot{Name: "broken"}
	repository.snapshots["healthy"] = &BucketSnapshot{Name: "healthy"}
	run.readFailures["broken"] = errors.New("destination down")

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err == nil {
		t.Fatal("reconcile succeeded despite destination failure")
	}
	if repository.failures["broken"] == "" || repository.successes["healthy"] != 1 || !run.createdBuckets["healthy"] {
		t.Fatalf("failure did not isolate: failures=%v successes=%v created=%v", repository.failures, repository.successes, run.createdBuckets)
	}
}

func TestReconcileAuditFailurePausesRun(t *testing.T) {
	reconciler, repository, run, writer := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("first"), testAssociation("second")}
	repository.snapshots["first"] = &BucketSnapshot{Name: "first"}
	repository.snapshots["second"] = &BucketSnapshot{Name: "second"}
	writer.failAt = 2 // request accepted, remote mutation succeeds, response audit fails.

	err := reconciler.ReconcileProject(context.Background(), repository.project)
	if !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("error = %v, want audit unavailable", err)
	}
	if !run.createdBuckets["first"] || run.createdBuckets["second"] {
		t.Fatalf("audit failure did not pause run: %v", run.createdBuckets)
	}
	if repository.failures["first"] != "audit unavailable; Bag Drop sync paused" {
		t.Fatalf("last_sync_error = %q", repository.failures["first"])
	}
}

func TestReconcileRequestAuditFailurePreventsMutation(t *testing.T) {
	reconciler, repository, run, writer := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("first")}
	repository.snapshots["first"] = &BucketSnapshot{Name: "first"}
	writer.failAt = 1 // the request event itself fails: nothing may execute unaudited.

	err := reconciler.ReconcileProject(context.Background(), repository.project)
	if !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("error = %v, want audit unavailable", err)
	}
	if len(run.createdBuckets) != 0 {
		t.Fatalf("mutation executed without a written request event: %v", run.createdBuckets)
	}
}

func TestReconcilePersistsFirstAttemptBeforeOutboundCall(t *testing.T) {
	reconciler, repository, _, _ := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images")}
	repository.snapshots["images"] = &BucketSnapshot{Name: "images"}

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	if len(repository.events) < 2 || repository.events[0] != "mark:images" || repository.events[1] != "get-bucket:images" {
		t.Fatalf("events = %v; first attempt must precede outbound calls", repository.events)
	}
}

func TestReconcileSecretNeverReachesStatusOrAudit(t *testing.T) {
	const secret = "SENTINEL-BAGDROP-CLIENT-SECRET"
	reconciler, repository, run, writer := newTestReconciler(t, secret)
	repository.associations = []Association{testAssociation("images")}
	repository.snapshots["images"] = &BucketSnapshot{Name: "images"}
	run.createBucketError = &AdapterError{StatusCode: 500, Summary: "echoed request credential " + secret}

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err == nil {
		t.Fatal("reconcile succeeded despite forced failure")
	}
	if strings.Contains(repository.failures["images"], secret) {
		t.Fatalf("last_sync_error contains secret: %q", repository.failures["images"])
	}
	for _, record := range writer.records {
		if strings.Contains(string(record), secret) {
			t.Fatalf("audit contains secret: %s", record)
		}
	}
}

func TestReconcileSkipsRunningVersionAndDeletedBucket(t *testing.T) {
	reconciler, repository, _, writer := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("running"), testAssociation("deleted")}
	// Repository projections omit running versions. A nil snapshot is the
	// locally-deleted association case and must remain wholly untouched.
	repository.snapshots["running"] = &BucketSnapshot{Name: "running"}
	repository.snapshots["deleted"] = nil

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	if repository.marks["deleted"] != 0 || repository.successes["deleted"] != 0 || repository.failures["deleted"] != "" {
		t.Fatalf("deleted association was touched: marks=%v successes=%v failures=%v", repository.marks, repository.successes, repository.failures)
	}
	if len(writer.records) != 2 {
		t.Fatalf("running version caused unexpected mutations: %d audit records", len(writer.records))
	}
}

func newTestReconciler(t *testing.T, secret string) (*Reconciler, *testReconcileRepository, *testReconcileRun, *testAuditWriter) {
	t.Helper()
	sealer := NewCredentialSealer(nil, testKey)
	sealed, err := sealer.Seal(testOrganization, testProject, secret)
	if err != nil {
		t.Fatal(err)
	}
	repository := &testReconcileRepository{
		project: Project{OrganizationID: testOrganization, ProjectID: testProject},
		record: &Record{
			OrganizationID: testOrganization, ProjectID: testProject, Adapter: AdapterHCPPacker,
			HCPPacker:    HCPPackerConfig{OrganizationID: "hcp-org", ProjectID: "hcp-project", ClientID: "client"},
			SealedSecret: sealed, Enabled: true,
		},
		snapshots: make(map[string]*BucketSnapshot), marks: make(map[string]int),
		successes: make(map[string]int), failures: make(map[string]string),
	}
	run := &testReconcileRun{
		events: &repository.events, buckets: make(map[string]RemoteBucket), versions: make(map[string]bool),
		builds: make(map[string][]RemoteBuild), readFailures: make(map[string]error), createdBuckets: make(map[string]bool),
		channels: make(map[string]map[string]RemoteChannel),
	}
	writer := &testAuditWriter{}
	reconciler, err := NewReconciler(repository, sealer, Registry{
		AdapterHCPPacker: &testReconcileAdapter{run: run},
	}, writer, 5*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	return reconciler, repository, run, writer
}

func testAssociation(name string) Association {
	return Association{OrganizationID: testOrganization, ProjectID: testProject, BucketName: name, State: AssociationActive}
}

func testSnapshot(name string) *BucketSnapshot {
	return &BucketSnapshot{
		Name: name, Description: name + " description",
		Versions: []VersionSnapshot{{
			Fingerprint: "fp-1", TemplateType: "HCL2",
			Builds: []BuildSnapshot{{
				ID: "build-local", ComponentType: "amazon-ebs", PackerRunUUID: "run-uuid",
				Platform: "aws", Labels: map[string]string{"purpose": "test"},
				Metadata:  json.RawMessage(`{"packer":{"version":"1.16.0"}}`),
				Artifacts: []ArtifactSnapshot{{ExternalIdentifier: "ami-1", Region: "eu-west-2"}},
			}},
		}},
	}
}

type testReconcileRepository struct {
	project      Project
	record       *Record
	associations []Association
	snapshots    map[string]*BucketSnapshot
	marks        map[string]int
	successes    map[string]int
	failures     map[string]string
	events       []string
}

func (r *testReconcileRepository) ListBagDropProjects(context.Context) ([]Project, error) {
	return []Project{r.project}, nil
}
func (r *testReconcileRepository) GetBagDropConfig(context.Context, string, string) (*Record, error) {
	return r.record, nil
}
func (r *testReconcileRepository) ListBagDropAssociations(context.Context, string, string) ([]Association, error) {
	return append([]Association(nil), r.associations...), nil
}
func (r *testReconcileRepository) GetBagDropBucketSnapshot(_ context.Context, _, _, name string) (*BucketSnapshot, error) {
	return r.snapshots[name], nil
}
func (r *testReconcileRepository) MarkBagDropAssociationAttempt(_ context.Context, _, _, name string, _ time.Time) error {
	r.marks[name]++
	r.events = append(r.events, "mark:"+name)
	return nil
}
func (r *testReconcileRepository) RecordBagDropAssociationSuccess(_ context.Context, _, _, name string, _ time.Time) error {
	r.successes[name]++
	return nil
}
func (r *testReconcileRepository) RecordBagDropAssociationFailure(_ context.Context, _, _, name, summary string, _ time.Time) error {
	r.failures[name] = summary
	return nil
}

type testReconcileAdapter struct{ run ReconcileRun }

func (*testReconcileAdapter) Resolve(context.Context, Destination) VerificationResult {
	return VerificationResult{Outcome: OutcomeResolved}
}
func (a *testReconcileAdapter) BeginReconcile(context.Context, Destination) (ReconcileRun, error) {
	return a.run, nil
}

type testReconcileRun struct {
	events             *[]string
	buckets            map[string]RemoteBucket
	versions           map[string]bool
	builds             map[string][]RemoteBuild
	readFailures       map[string]error
	createdBuckets     map[string]bool
	createBucketError  error
	updatedArtifacts   []ArtifactSnapshot
	channels           map[string]map[string]RemoteChannel
	createdChannels    []string
	updatedChannels    []string
	updateChannelError error
}

func (r *testReconcileRun) GetBucket(_ context.Context, name string) (*RemoteBucket, bool, error) {
	*r.events = append(*r.events, "get-bucket:"+name)
	if err := r.readFailures[name]; err != nil {
		return nil, false, err
	}
	bucket, ok := r.buckets[name]
	return &bucket, ok, nil
}
func (r *testReconcileRun) CreateBucket(_ context.Context, bucket BucketSnapshot) error {
	*r.events = append(*r.events, "create-bucket:"+bucket.Name)
	if r.createBucketError != nil {
		return r.createBucketError
	}
	r.createdBuckets[bucket.Name] = true
	r.buckets[bucket.Name] = RemoteBucket{Description: bucket.Description}
	return nil
}
func (r *testReconcileRun) UpdateBucket(_ context.Context, bucket BucketSnapshot) error {
	*r.events = append(*r.events, "update-bucket:"+bucket.Name)
	r.buckets[bucket.Name] = RemoteBucket{Description: bucket.Description}
	return nil
}
func (r *testReconcileRun) GetVersion(_ context.Context, _, fingerprint string) (bool, error) {
	*r.events = append(*r.events, "get-version:"+fingerprint)
	return r.versions[fingerprint], nil
}
func (r *testReconcileRun) CreateVersion(_ context.Context, _ string, version VersionSnapshot) error {
	*r.events = append(*r.events, "create-version:"+version.Fingerprint)
	r.versions[version.Fingerprint] = true
	return nil
}
func (r *testReconcileRun) ListBuilds(_ context.Context, _, fingerprint string) ([]RemoteBuild, error) {
	*r.events = append(*r.events, "list-builds:"+fingerprint)
	return append([]RemoteBuild(nil), r.builds[fingerprint]...), nil
}
func (r *testReconcileRun) CreateBuild(_ context.Context, _, fingerprint string, build BuildSnapshot) (string, error) {
	*r.events = append(*r.events, "create-build:"+build.ComponentType)
	remote := RemoteBuild{ID: "remote-" + build.ComponentType, ComponentType: build.ComponentType, Status: "BUILD_PENDING"}
	r.builds[fingerprint] = append(r.builds[fingerprint], remote)
	return remote.ID, nil
}
func (r *testReconcileRun) UpdateBuild(_ context.Context, _, fingerprint, id string, build BuildSnapshot) error {
	*r.events = append(*r.events, "update-build:"+build.ComponentType)
	for i := range r.builds[fingerprint] {
		if r.builds[fingerprint][i].ID == id {
			r.builds[fingerprint][i].Status = "BUILD_DONE"
		}
	}
	r.updatedArtifacts = append([]ArtifactSnapshot(nil), build.Artifacts...)
	return nil
}
func (r *testReconcileRun) ListChannels(_ context.Context, bucket string) ([]RemoteChannel, error) {
	*r.events = append(*r.events, "list-channels:"+bucket)
	channels := r.channels[bucket]
	listed := make([]RemoteChannel, 0, len(channels))
	for _, channel := range channels {
		listed = append(listed, channel)
	}
	return listed, nil
}
func (r *testReconcileRun) CreateChannel(_ context.Context, bucket, name string) error {
	*r.events = append(*r.events, "create-channel:"+name)
	if r.channels[bucket] == nil {
		r.channels[bucket] = make(map[string]RemoteChannel)
	}
	r.channels[bucket][name] = RemoteChannel{Name: name}
	r.createdChannels = append(r.createdChannels, name)
	return nil
}
func (r *testReconcileRun) UpdateChannelAssignment(
	_ context.Context, bucket, name string, fingerprint *string,
) error {
	*r.events = append(*r.events, "update-channel:"+name)
	if r.updateChannelError != nil {
		return r.updateChannelError
	}
	channel := r.channels[bucket][name]
	channel.AssignedVersionFingerprint = fingerprint
	r.channels[bucket][name] = channel
	detail := "clear"
	if fingerprint != nil {
		detail = *fingerprint
	}
	r.updatedChannels = append(r.updatedChannels, name+":"+detail)
	return nil
}

type testAuditWriter struct {
	records [][]byte
	writes  int
	failAt  int
}

func (w *testAuditWriter) Write(record []byte) error {
	w.writes++
	if w.failAt != 0 && w.writes == w.failAt {
		return errors.New("audit sink down")
	}
	w.records = append(w.records, append([]byte(nil), record...))
	return nil
}
