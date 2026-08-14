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
		{Association{State: AssociationActive, LastSyncError: &failure}, SyncError},
		{Association{State: AssociationActive, LastSyncedAt: &now, LastSyncError: &failure}, SyncError},
		{Association{State: AssociationPendingRemoval, LastSyncedAt: &now, LastSyncError: &failure}, SyncRemoving},
	} {
		if got := test.association.SyncStatus(); got != test.want {
			t.Errorf("SyncStatus(%#v) = %q, want %q", test.association, got, test.want)
		}
	}
}

func TestOrderAssociationsByAncestry(t *testing.T) {
	parent := &BucketSnapshot{
		Name: "parent",
		Versions: []VersionSnapshot{{Builds: []BuildSnapshot{{
			Artifacts: []ArtifactSnapshot{{ExternalIdentifier: "parent-artifact"}},
		}}}},
	}
	child := &BucketSnapshot{
		Name: "child",
		Versions: []VersionSnapshot{{Builds: []BuildSnapshot{{
			SourceExternalIdentifier: "parent-artifact",
		}}}},
	}
	cycleA := &BucketSnapshot{
		Name: "cycle-a",
		Versions: []VersionSnapshot{{Builds: []BuildSnapshot{{
			SourceExternalIdentifier: "artifact-b",
			Artifacts:                []ArtifactSnapshot{{ExternalIdentifier: "artifact-a"}},
		}}}},
	}
	cycleB := &BucketSnapshot{
		Name: "cycle-b",
		Versions: []VersionSnapshot{{Builds: []BuildSnapshot{{
			SourceExternalIdentifier: "artifact-a",
			Artifacts:                []ArtifactSnapshot{{ExternalIdentifier: "artifact-b"}},
		}}}},
	}

	for _, test := range []struct {
		name         string
		associations []Association
		snapshots    map[string]*BucketSnapshot
		want         string
	}{
		{
			name:         "parent after child moves before child",
			associations: []Association{testAssociation("child"), testAssociation("parent")},
			snapshots:    map[string]*BucketSnapshot{"child": child, "parent": parent},
			want:         "parent,child",
		},
		{
			name: "unrelated buckets retain input order",
			associations: []Association{
				testAssociation("second"), testAssociation("first"), testAssociation("third"),
			},
			snapshots: map[string]*BucketSnapshot{},
			want:      "second,first,third",
		},
		{
			name:         "cycle retains input order",
			associations: []Association{testAssociation("cycle-b"), testAssociation("cycle-a")},
			snapshots: map[string]*BucketSnapshot{
				"cycle-a": cycleA,
				"cycle-b": cycleB,
			},
			want: "cycle-b,cycle-a",
		},
		{
			name: "unassociated parent exerts no edge",
			associations: []Association{
				testAssociation("child"), testAssociation("unrelated"),
			},
			snapshots: map[string]*BucketSnapshot{
				"parent": parent,
				"child":  child,
			},
			want: "child,unrelated",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ordered := orderAssociationsByAncestry(test.associations, test.snapshots)
			names := make([]string, 0, len(ordered))
			for _, association := range ordered {
				names = append(names, association.BucketName)
			}
			if got := strings.Join(names, ","); got != test.want {
				t.Fatalf("order = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReconcileOrdersParentBeforeChildForBuildCorrelation(t *testing.T) {
	reconciler, repository, run, _ := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("a-child"), testAssociation("z-parent")}
	repository.snapshots["a-child"] = &BucketSnapshot{
		Name: "a-child",
		Versions: []VersionSnapshot{{Fingerprint: "child-fp", Builds: []BuildSnapshot{{
			ID:                       "child-build",
			ComponentType:            "child",
			PackerRunUUID:            "child-run",
			SourceExternalIdentifier: "parent-artifact",
		}}}},
	}
	repository.snapshots["z-parent"] = &BucketSnapshot{
		Name: "z-parent",
		Versions: []VersionSnapshot{{Fingerprint: "parent-fp", Builds: []BuildSnapshot{{
			ID:            "parent-build",
			ComponentType: "parent",
			PackerRunUUID: "parent-run",
			Artifacts: []ArtifactSnapshot{{
				ExternalIdentifier: "parent-artifact",
			}},
		}}}},
	}

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	if !run.correlatedAtCreate["remote-child"] {
		t.Fatalf("child build was not correlated at create time: %#v", run.correlatedAtCreate)
	}
	parentCreate, childCreate := -1, -1
	for i, event := range repository.events {
		switch event {
		case "create-build:parent":
			parentCreate = i
		case "create-build:child":
			childCreate = i
		}
	}
	if parentCreate == -1 || childCreate == -1 || parentCreate >= childCreate {
		t.Fatalf("build creation order = %v", repository.events)
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
		"create-version:fp-1", "get-version:fp-1", "list-builds:fp-1", "create-build:amazon-ebs",
		"update-build:amazon-ebs", "list-sboms:remote-amazon-ebs", "list-channels:images",
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

func TestReconcileStandardConvergenceParityHCPAndDufflebag(t *testing.T) {
	type outcome struct {
		events []string
		calls  []string
	}
	run := func(t *testing.T, kind AdapterKind) outcome {
		t.Helper()
		versionExists := false
		var calls []string
		handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			calls = append(calls, request.Method+" "+request.URL.Path)
			path := request.URL.Path
			switch {
			case path == "/oauth2/token":
				// Fixture source: internal/compat/hcpauth/handler.go tokenResponse.
				_, _ = w.Write([]byte(tokenSuccessFixture))
			case request.Method == http.MethodGet && strings.HasSuffix(path, "/buckets/images"):
				// Fixture source: internal/compat/hcp2023 writeBucketNotFound.
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":5,"message":"bucket not found","details":[]}`))
			case request.Method == http.MethodPut && strings.HasSuffix(path, "/buckets"):
				_, _ = w.Write([]byte(`{"bucket":{"name":"images"}}`))
			case request.Method == http.MethodGet && strings.HasSuffix(path, "/versions/fp-1"):
				if !versionExists {
					// Fixture source: internal/compat/hcp2023 writeVersionNotFound.
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"code":10,"message":"version not found","details":[]}`))
					return
				}
				_, _ = w.Write([]byte(`{"version":{"fingerprint":"fp-1"}}`))
			case request.Method == http.MethodPost && strings.HasSuffix(path, "/versions"):
				versionExists = true
				_, _ = w.Write([]byte(`{"version":{"fingerprint":"fp-1"}}`))
			case request.Method == http.MethodGet && strings.HasSuffix(path, "/versions/fp-1/builds"):
				// Fixture source: vendored PackerService_ListBuilds response.
				_, _ = w.Write([]byte(`{"builds":[],"pagination":{}}`))
			case request.Method == http.MethodPost && strings.HasSuffix(path, "/versions/fp-1/builds"):
				_, _ = w.Write([]byte(`{"build":{"id":"remote-build","component_type":"amazon-ebs","status":"BUILD_PENDING"}}`))
			case request.Method == http.MethodPatch && strings.HasSuffix(path, "/builds/remote-build"):
				_, _ = w.Write([]byte(`{"build":{"id":"remote-build","status":"BUILD_DONE"}}`))
			case request.Method == http.MethodGet && strings.HasSuffix(path, "/builds/remote-build/sboms"):
				// Fixture source: vendored PackerService_ListSboms response.
				_, _ = w.Write([]byte(`{"sboms":[],"pagination":{}}`))
			case request.Method == http.MethodGet && strings.HasSuffix(path, "/buckets/images/channels"):
				// Fixture source: vendored PackerService_ListChannels response.
				_, _ = w.Write([]byte(`{"channels":[],"pagination":{}}`))
			default:
				t.Errorf("unexpected destination request %s %s", request.Method, path)
				w.WriteHeader(http.StatusNotFound)
			}
		})

		reconciler, repository, _, _ := newTestReconciler(t, "secret")
		var server *httptest.Server
		if kind == AdapterDufflebag {
			var caChain string
			server, caChain = newGeneratedCATLSServer(t, handler)
			repository.record.Adapter = AdapterDufflebag
			repository.record.HCPPacker = HCPPackerConfig{}
			repository.record.Dufflebag = DufflebagConfig{
				Endpoint: server.URL, CAChain: caChain,
				OrganizationID: "hcp-org", ProjectID: "hcp-project", ClientID: "client",
			}
			reconciler.adapters = Registry{AdapterDufflebag: NewDufflebagAdapterFactory()}
		} else {
			server = httptest.NewServer(handler)
			reconciler.adapters = Registry{AdapterHCPPacker: NewHCPPackerAdapter(server.URL, server.URL)}
		}
		defer server.Close()
		repository.associations = []Association{testAssociation("images")}
		repository.snapshots["images"] = testSnapshot("images")
		if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
			t.Fatal(err)
		}
		return outcome{events: append([]string(nil), repository.events...), calls: calls}
	}

	hcp := run(t, AdapterHCPPacker)
	dufflebag := run(t, AdapterDufflebag)
	if strings.Join(dufflebag.events, ",") != strings.Join(hcp.events, ",") ||
		strings.Join(dufflebag.calls, ",") != strings.Join(hcp.calls, ",") {
		t.Fatalf("adapter semantics diverged:\nHCP events=%v calls=%v\ndufflebag events=%v calls=%v",
			hcp.events, hcp.calls, dufflebag.events, dufflebag.calls)
	}
}

func TestReconcileMirrorSemanticsAgainstHCP2023FakeDestination(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Fixture source: internal/compat/hcpauth/handler.go tokenResponse.
		_, _ = w.Write([]byte(tokenSuccessFixture))
	}))
	defer auth.Close()
	var bucketExists, versionExists, buildExists, buildRunning, buildDone, sbomPresent bool
	var remoteChannelDrift, remoteVersionDrift, remoteBuildDrift bool
	foreignBucket := true
	var productionExists bool
	productionFingerprint := ""
	latestMutations := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		path := request.URL.Path
		if strings.Contains(path, "/buckets/foreign") {
			foreignBucket = false
			t.Errorf("foreign unassociated bucket was touched: %s %s", request.Method, path)
		}
		switch {
		case request.Method == http.MethodGet && strings.HasSuffix(path, "/buckets/images"):
			if !bucketExists {
				// Fixture source: internal/compat/hcp2023 writeBucketNotFound.
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":5,"message":"bucket not found","details":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"bucket":{"description":"images description"}}`))
		case request.Method == http.MethodGet && strings.HasSuffix(path, "/buckets/images/versions"):
			if versionExists {
				drift := ""
				if remoteVersionDrift {
					drift = `,{"fingerprint":"fp-remote"}`
				}
				_, _ = w.Write([]byte(`{"versions":[{"fingerprint":"fp-1"}` + drift + `],"pagination":{}}`))
			} else {
				_, _ = w.Write([]byte(`{"versions":[],"pagination":{}}`))
			}
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
			if buildRunning {
				status = "BUILD_RUNNING"
			} else if buildDone {
				status = "BUILD_DONE"
			}
			drift := ""
			if remoteBuildDrift {
				drift = `,{"id":"remote-build-drift","component_type":"googlecompute","status":"BUILD_DONE"}`
			}
			_, _ = w.Write([]byte(`{"builds":[{"id":"remote-build","component_type":"amazon-ebs","status":"` + status + `"}` + drift + `]}`))
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
			if body.Status == "BUILD_RUNNING" {
				if len(body.Artifacts) != 0 || body.Metadata != nil {
					t.Errorf("running build update was not minimal: %#v", body)
				}
				buildRunning, buildDone = true, false
				_, _ = w.Write([]byte(`{"build":{"id":"remote-build","component_type":"amazon-ebs","status":"BUILD_RUNNING"}}`))
				return
			}
			if body.Status != "BUILD_DONE" || len(body.Artifacts) != 1 ||
				body.Artifacts[0].ExternalIdentifier != "ami-1" || body.Artifacts[0].Region != "eu-west-2" ||
				body.Metadata["packer"] == nil {
				t.Errorf("build update = %#v", body)
			}
			buildRunning, buildDone = false, true
			_, _ = w.Write([]byte(`{"build":{"id":"remote-build","component_type":"amazon-ebs","status":"BUILD_DONE"}}`))
		case request.Method == http.MethodGet && strings.HasSuffix(path, "/builds/remote-build/sboms"):
			// Fixture source: vendored PackerService_ListSboms response.
			if sbomPresent {
				_, _ = w.Write([]byte(`{"sboms":[{"name":"manifest","format":"CYCLONEDX"}],"pagination":{}}`))
				return
			}
			_, _ = w.Write([]byte(`{"sboms":[],"pagination":{}}`))
		case request.Method == http.MethodPut && strings.HasSuffix(path, "/builds/remote-build/sboms"):
			if !buildRunning {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":3,"message":"This build's status isn't Running, so sboms can not be uploaded","details":[]}`))
				return
			}
			sbomPresent = true
			_, _ = w.Write([]byte(`{"sbom":{"name":"manifest","format":"CYCLONEDX"}}`))
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
			drift := ""
			if remoteChannelDrift {
				drift = `,{"name":"remote-only","managed":false,"version":null}`
			}
			_, _ = w.Write([]byte(`{"channels":[{"name":"latest","managed":true,"version":{"fingerprint":"fp-1"}}` + production + drift + `]}`))
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
		case request.Method == http.MethodDelete && strings.HasSuffix(path, "/channels/remote-only"):
			remoteChannelDrift = false
			// Fixture source: the vendored DeleteChannelResponse is an empty object.
			_, _ = w.Write([]byte(`{}`))
		case request.Method == http.MethodDelete && strings.HasSuffix(path, "/versions/fp-remote"):
			remoteVersionDrift = false
			// Fixture source: the vendored DeleteVersionResponse is an empty object.
			_, _ = w.Write([]byte(`{}`))
		case request.Method == http.MethodDelete && strings.HasSuffix(path, "/builds/remote-build-drift"):
			remoteBuildDrift = false
			// Fixture source: the vendored DeleteBuildResponse is an empty object.
			_, _ = w.Write([]byte(`{}`))
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
	repository.snapshots["images"].Versions[0].Builds[0].Sboms = []SbomSnapshot{{
		Name: "manifest", Format: "CYCLONEDX", Document: []byte(`{"bomFormat":"CycloneDX"}`),
	}}
	repository.snapshots["images"].Channels = []ChannelSnapshot{{
		Name: "production", AssignedVersionFingerprint: fingerprintPointer("fp-1"),
	}}
	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	if !bucketExists || !versionExists || !buildDone || !sbomPresent || !productionExists ||
		productionFingerprint != "fp-1" || latestMutations != 0 || repository.successes["images"] != 1 {
		t.Fatalf("destination state bucket=%v version=%v build_done=%v sbom=%v channel=%v/%q latest_mutations=%d successes=%v",
			bucketExists, versionExists, buildDone, sbomPresent, productionExists, productionFingerprint, latestMutations, repository.successes)
	}
	remoteChannelDrift, remoteVersionDrift, remoteBuildDrift = true, true, true
	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	if remoteChannelDrift || remoteVersionDrift || remoteBuildDrift || !foreignBucket ||
		latestMutations != 0 || repository.successes["images"] != 2 {
		t.Fatalf("destructive convergence channel=%v version=%v build=%v foreign=%v latest_mutations=%d successes=%v",
			remoteChannelDrift, remoteVersionDrift, remoteBuildDrift, foreignBucket, latestMutations, repository.successes)
	}
}

func fingerprintPointer(value string) *string { return &value }

func TestReconcilePartiallyExistingPushesOnlyBuilds(t *testing.T) {
	reconciler, repository, run, writer := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images")}
	repository.snapshots["images"] = testSnapshot("images")
	run.buckets["images"] = RemoteBucket{Description: "images description"}
	run.versions["fp-1"] = RemoteVersion{Fingerprint: "fp-1"}

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

func TestReconcileBuildWithSbomsUsesRunningUploadWindowAndEndsDone(t *testing.T) {
	reconciler, repository, run, writer := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images")}
	snapshot := testSnapshot("images")
	snapshot.Versions[0].Builds[0].Sboms = []SbomSnapshot{{
		Name: "manifest", Format: "CYCLONEDX", Document: []byte(`{"bomFormat":"CycloneDX"}`),
	}}
	repository.snapshots["images"] = snapshot
	seedDeletionInvariants(run, "images")
	run.buckets["images"] = RemoteBucket{
		Description: "images description", Versions: []RemoteVersion{{Fingerprint: "fp-1"}},
	}
	run.versions["fp-1"] = RemoteVersion{Fingerprint: "fp-1"}

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{
		"create-build:amazon-ebs",
		"list-sboms:remote-amazon-ebs",
		"update-build-running:remote-amazon-ebs",
		"upload-sbom:manifest",
		"update-build:amazon-ebs",
	}
	position := -1
	for _, want := range wantOrder {
		found := false
		for i := position + 1; i < len(repository.events); i++ {
			if repository.events[i] == want {
				position = i
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("event %q missing in order from %v", want, repository.events)
		}
	}
	build := findRemoteBuild(run.builds["fp-1"], "amazon-ebs")
	if build == nil || build.Status != "BUILD_DONE" ||
		len(run.sboms["images/fp-1/remote-amazon-ebs"]) != 1 {
		t.Fatalf("destination build=%#v sboms=%#v", build, run.sboms)
	}
	assertAuditMutation(t, writer, "bagdrop.sync.sbom.upload", "")
	assertDeletionInvariants(t, run, "images")
}

func TestReconcileVersionWithMultipleBuildsCreatesAllBeforeCompletingAny(t *testing.T) {
	reconciler, repository, run, _ := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images")}
	snapshot := testSnapshot("images")
	snapshot.Versions[0].Builds[0].Sboms = []SbomSnapshot{{
		Name: "manifest", Format: "CYCLONEDX", Document: []byte(`{"bomFormat":"CycloneDX"}`),
	}}
	snapshot.Versions[0].Builds = append(snapshot.Versions[0].Builds, BuildSnapshot{
		ID: "build-web", ComponentType: "docker.web", PackerRunUUID: "run-uuid-web",
		Platform: "docker", Artifacts: []ArtifactSnapshot{{ExternalIdentifier: "web:latest"}},
	})
	repository.snapshots["images"] = snapshot

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	for _, componentType := range []string{"amazon-ebs", "docker.web"} {
		build := findRemoteBuild(run.builds["fp-1"], componentType)
		if build == nil || build.Status != "BUILD_DONE" {
			t.Fatalf("destination build %q = %#v", componentType, build)
		}
	}
	if len(run.uploadedSboms) != 1 || run.uploadedSboms[0] != "images/fp-1/remote-amazon-ebs/manifest" {
		t.Fatalf("uploaded SBOMs = %#v", run.uploadedSboms)
	}
	createPositions := make(map[string]int)
	firstTerminalUpdate := len(repository.events)
	for i, event := range repository.events {
		if strings.HasPrefix(event, "create-build:") {
			createPositions[event] = i
		}
		if strings.HasPrefix(event, "update-build:") && firstTerminalUpdate == len(repository.events) {
			firstTerminalUpdate = i
		}
	}
	if createPositions["create-build:amazon-ebs"] >= firstTerminalUpdate ||
		createPositions["create-build:docker.web"] >= firstTerminalUpdate ||
		len(createPositions) != 2 || firstTerminalUpdate == len(repository.events) {
		t.Fatalf("build creation and terminal update order = %v", repository.events)
	}
}

func TestReconcileSbomPresenceDiffAndScopeInvariants(t *testing.T) {
	for _, test := range []struct {
		name          string
		remotePresent bool
		wantUploads   int
	}{
		{name: "absent uploads", wantUploads: 1},
		{name: "present makes no call", remotePresent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reconciler, repository, run, writer := newTestReconciler(t, "secret")
			repository.associations = []Association{testAssociation("images")}
			snapshot := testSnapshot("images")
			snapshot.Versions[0].Builds[0].Sboms = []SbomSnapshot{{
				Name: "manifest", Format: "CYCLONEDX", Document: []byte(`{"bomFormat":"CycloneDX"}`),
			}}
			repository.snapshots["images"] = snapshot
			seedDeletionInvariants(run, "images")
			run.buckets["images"] = RemoteBucket{
				Description: "images description", Versions: []RemoteVersion{{Fingerprint: "fp-1"}},
			}
			run.versions["fp-1"] = RemoteVersion{Fingerprint: "fp-1"}
			run.builds["fp-1"] = []RemoteBuild{{
				ID: "remote-amazon-ebs", ComponentType: "amazon-ebs", Status: "BUILD_RUNNING",
			}}
			key := "images/fp-1/remote-amazon-ebs"
			if test.remotePresent {
				run.sboms[key] = []RemoteSbom{{Name: "manifest", Format: "CYCLONEDX"}}
			}

			if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
				t.Fatal(err)
			}
			if len(run.uploadedSboms) != test.wantUploads {
				t.Fatalf("uploaded SBOMs = %v, want %d", run.uploadedSboms, test.wantUploads)
			}
			for _, uploaded := range run.uploadedSboms {
				if !strings.HasPrefix(uploaded, "images/") {
					t.Fatalf("foreign unassociated bucket received SBOM: %v", run.uploadedSboms)
				}
			}
			assertDeletionInvariants(t, run, "images")
			if test.wantUploads == 1 {
				assertAuditMutation(t, writer, "bagdrop.sync.sbom.upload", "")
			}
			if repository.successes["images"] != 1 || repository.failures["images"] != "" {
				t.Fatalf("presence convergence successes=%v failures=%v", repository.successes, repository.failures)
			}
		})
	}
}

func TestReconcileOversizedSbomIsSkippedSurfacedAndNonfatal(t *testing.T) {
	reconciler, repository, run, writer := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images"), testAssociation("healthy")}
	snapshot := testSnapshot("images")
	snapshot.Channels = []ChannelSnapshot{{Name: "production"}}
	snapshot.Versions[0].Builds[0].Sboms = []SbomSnapshot{{
		Name: "oversized", Format: "SPDX", Document: []byte(`{"spdxVersion":"SPDX-2.3"}`),
	}}
	repository.snapshots["images"] = snapshot
	repository.snapshots["healthy"] = &BucketSnapshot{Name: "healthy"}
	seedDeletionInvariants(run, "images")
	run.buckets["images"] = RemoteBucket{
		Description: "images description", Versions: []RemoteVersion{{Fingerprint: "fp-1"}},
	}
	run.versions["fp-1"] = RemoteVersion{Fingerprint: "fp-1"}
	run.builds["fp-1"] = []RemoteBuild{{
		ID: "remote-amazon-ebs", ComponentType: "amazon-ebs", Status: "BUILD_RUNNING",
	}}
	run.uploadSbomErrors["images/fp-1/remote-amazon-ebs/oversized"] = &AdapterError{
		StatusCode: http.StatusGatewayTimeout,
	}

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatalf("size refusal was fatal: %v", err)
	}
	if len(run.uploadedSboms) != 0 || !strings.Contains(repository.failures["images"], "size refusal: HTTP 504") {
		t.Fatalf("size refusal result uploads=%v failures=%v", run.uploadedSboms, repository.failures)
	}
	if repository.successes["images"] != 0 || repository.successes["healthy"] != 1 ||
		!run.createdBuckets["healthy"] || len(run.createdChannels) != 1 || run.createdChannels[0] != "production" {
		t.Fatalf("other work did not converge: successes=%v buckets=%v channels=%v",
			repository.successes, run.createdBuckets, run.createdChannels)
	}
	uploadCalls := 0
	for _, event := range repository.events {
		if event == "upload-sbom:oversized" {
			uploadCalls++
		}
	}
	build := findRemoteBuild(run.builds["fp-1"], "amazon-ebs")
	if uploadCalls != 1 || build == nil || build.Status != "BUILD_DONE" {
		t.Fatalf("size-refused build=%#v upload_calls=%d events=%v", build, uploadCalls, repository.events)
	}
	assertDeletionInvariants(t, run, "images")
	assertAuditMutation(t, writer, "bagdrop.sync.sbom.upload", "")
}

func TestReconcileLateSbomAgainstCompletedBuildIsSurfacedPermanentDrift(t *testing.T) {
	reconciler, repository, run, _ := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images"), testAssociation("healthy")}
	snapshot := testSnapshot("images")
	snapshot.Versions[0].Builds[0].Sboms = []SbomSnapshot{{Name: "late", Format: "SPDX", Document: []byte("document")}}
	repository.snapshots["images"] = snapshot
	repository.snapshots["healthy"] = &BucketSnapshot{Name: "healthy"}
	seedDeletionInvariants(run, "images")
	run.buckets["images"] = RemoteBucket{
		Description: "images description", Versions: []RemoteVersion{{Fingerprint: "fp-1"}},
	}
	run.versions["fp-1"] = RemoteVersion{Fingerprint: "fp-1"}
	run.builds["fp-1"] = []RemoteBuild{{
		ID: "remote-amazon-ebs", ComponentType: "amazon-ebs", Status: "BUILD_DONE",
	}}

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatalf("completed-build drift was fatal: %v", err)
	}
	if len(run.uploadedSboms) != 0 ||
		!strings.Contains(repository.failures["images"], "SBOM fp-1/amazon-ebs/late cannot be uploaded to a completed destination build") {
		t.Fatalf("late SBOM result uploads=%v failures=%v", run.uploadedSboms, repository.failures)
	}
	if repository.successes["healthy"] != 1 || !run.createdBuckets["healthy"] {
		t.Fatalf("next association did not converge: successes=%v buckets=%v", repository.successes, run.createdBuckets)
	}
	assertDeletionInvariants(t, run, "images")
}

func TestFakeDestinationRefusesSbomOutsideRunningWindow(t *testing.T) {
	_, _, run, _ := newTestReconciler(t, "secret")
	run.builds["fp-1"] = []RemoteBuild{{
		ID: "remote-amazon-ebs", ComponentType: "amazon-ebs", Status: "BUILD_DONE",
	}}
	err := run.UploadSbom(context.Background(), "images", "fp-1", "remote-amazon-ebs", SbomSnapshot{Name: "manifest"})
	var adapterErr *AdapterError
	if !errors.As(err, &adapterErr) || adapterErr.StatusCode != http.StatusBadRequest || adapterErr.Code != 3 ||
		adapterErr.Summary != "This build's status isn't Running, so sboms can not be uploaded" {
		t.Fatalf("UploadSbom refusal = %#v", err)
	}
}

func TestReconcileRemoteOnlySbomIsSurfacedWithoutDeletion(t *testing.T) {
	reconciler, repository, run, _ := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images")}
	repository.snapshots["images"] = testSnapshot("images")
	seedDeletionInvariants(run, "images")
	run.buckets["images"] = RemoteBucket{
		Description: "images description", Versions: []RemoteVersion{{Fingerprint: "fp-1"}},
	}
	run.versions["fp-1"] = RemoteVersion{Fingerprint: "fp-1"}
	run.builds["fp-1"] = []RemoteBuild{{
		ID: "remote-amazon-ebs", ComponentType: "amazon-ebs", Status: "BUILD_DONE",
	}}
	run.sboms["images/fp-1/remote-amazon-ebs"] = []RemoteSbom{{Name: "remote-only", Format: "SPDX"}}

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatalf("non-removable drift was fatal: %v", err)
	}
	if !strings.Contains(repository.failures["images"], "has no local source and cannot be deleted") {
		t.Fatalf("remote-only drift not surfaced: %v", repository.failures)
	}
	assertDeletionInvariants(t, run, "images")
}

func TestReconcileSbomUploadIsAuditFailClosed(t *testing.T) {
	reconciler, repository, run, writer := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images")}
	snapshot := testSnapshot("images")
	snapshot.Versions[0].Builds[0].Sboms = []SbomSnapshot{{Name: "manifest", Format: "SPDX", Document: []byte("document")}}
	repository.snapshots["images"] = snapshot
	run.buckets["images"] = RemoteBucket{
		Description: "images description", Versions: []RemoteVersion{{Fingerprint: "fp-1"}},
	}
	run.versions["fp-1"] = RemoteVersion{Fingerprint: "fp-1"}
	run.builds["fp-1"] = []RemoteBuild{{ID: "remote-amazon-ebs", ComponentType: "amazon-ebs", Status: "BUILD_RUNNING"}}
	run.channels["images"] = map[string]RemoteChannel{"latest": {Name: "latest", Managed: true}}
	writer.failAt = 1

	err := reconciler.ReconcileProject(context.Background(), repository.project)
	if !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("error = %v, want audit unavailable", err)
	}
	if len(run.uploadedSboms) != 0 {
		t.Fatalf("SBOM uploaded without request audit: %v", run.uploadedSboms)
	}
}

func TestReconcileVersionRevocationDirectionAndInvariants(t *testing.T) {
	revokeAt := time.Date(2026, 8, 11, 12, 0, 0, 123000000, time.UTC)
	for _, test := range []struct {
		name        string
		localAt     *time.Time
		remoteAt    *time.Time
		wantRevoke  bool
		wantRestore bool
		operation   string
	}{
		{name: "local revoked revokes remote", localAt: &revokeAt, wantRevoke: true, operation: "bagdrop.sync.version.revoke"},
		{name: "local active restores remote", remoteAt: &revokeAt, wantRestore: true, operation: "bagdrop.sync.version.restore"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reconciler, repository, run, writer := newTestReconciler(t, "secret")
			repository.associations = []Association{testAssociation("images")}
			repository.snapshots["images"] = &BucketSnapshot{
				Name: "images", Versions: []VersionSnapshot{{
					Fingerprint: "fp-1", RevokeAt: test.localAt, RevocationMessage: "superseded",
				}},
			}
			seedDeletionInvariants(run, "images")
			run.buckets["images"] = RemoteBucket{Versions: []RemoteVersion{{Fingerprint: "fp-1"}}}
			run.versions["fp-1"] = RemoteVersion{Fingerprint: "fp-1", RevokeAt: test.remoteAt}

			if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
				t.Fatal(err)
			}
			if (len(run.revokedVersions) == 1) != test.wantRevoke ||
				(len(run.restoredVersions) == 1) != test.wantRestore {
				t.Fatalf("wrong direction: revoked=%v restored=%v", run.revokedVersions, run.restoredVersions)
			}
			if len(run.revokedVersions) != 0 && run.revokedVersions[0] != "images/fp-1" {
				t.Fatalf("foreign version revoked: %v", run.revokedVersions)
			}
			if len(run.restoredVersions) != 0 && run.restoredVersions[0] != "images/fp-1" {
				t.Fatalf("foreign version restored: %v", run.restoredVersions)
			}
			assertDeletionInvariants(t, run, "images")
			detail := ""
			if test.wantRevoke {
				detail = "revoke_at " + revokeAt.Format(time.RFC3339Nano)
			}
			assertAuditMutation(t, writer, test.operation, detail)
		})
	}
}

func TestReconcileBuildsBeforeVersionRevocation(t *testing.T) {
	revokeAt := time.Date(2026, 8, 11, 12, 0, 0, 123000000, time.UTC)
	reconciler, repository, _, _ := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images")}
	repository.snapshots["images"] = &BucketSnapshot{
		Name: "images",
		Versions: []VersionSnapshot{{
			Fingerprint: "fp-revoked",
			RevokeAt:    &revokeAt,
			Builds: []BuildSnapshot{{
				ID: "build-local", ComponentType: "amazon-ebs", PackerRunUUID: "run-uuid",
			}},
		}},
	}

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	createBuild, updateBuild, revokeVersion := -1, -1, -1
	for i, event := range repository.events {
		switch event {
		case "create-build:amazon-ebs":
			createBuild = i
		case "update-build:amazon-ebs":
			updateBuild = i
		case "revoke-version:fp-revoked":
			revokeVersion = i
		}
	}
	if createBuild == -1 || updateBuild == -1 || revokeVersion == -1 ||
		createBuild >= revokeVersion || updateBuild >= revokeVersion {
		t.Fatalf("build/revocation event order = %v", repository.events)
	}
}

func TestReconcileVersionRevocationMutationsAreAuditFailClosed(t *testing.T) {
	revokeAt := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		localAt  *time.Time
		remoteAt *time.Time
	}{
		{name: "revoke", localAt: &revokeAt},
		{name: "restore", remoteAt: &revokeAt},
	} {
		t.Run(test.name, func(t *testing.T) {
			reconciler, repository, run, writer := newTestReconciler(t, "secret")
			repository.associations = []Association{testAssociation("images")}
			repository.snapshots["images"] = &BucketSnapshot{
				Name: "images", Versions: []VersionSnapshot{{Fingerprint: "fp-1", RevokeAt: test.localAt}},
			}
			run.buckets["images"] = RemoteBucket{Versions: []RemoteVersion{{Fingerprint: "fp-1"}}}
			run.versions["fp-1"] = RemoteVersion{Fingerprint: "fp-1", RevokeAt: test.remoteAt}
			run.channels["images"] = map[string]RemoteChannel{"latest": {Name: "latest", Managed: true}}
			writer.failAt = 1

			err := reconciler.ReconcileProject(context.Background(), repository.project)
			if !errors.Is(err, ErrAuditUnavailable) {
				t.Fatalf("error = %v, want audit unavailable", err)
			}
			if len(run.revokedVersions) != 0 || len(run.restoredVersions) != 0 {
				t.Fatalf("revocation mutation ran without request audit: revoked=%v restored=%v",
					run.revokedVersions, run.restoredVersions)
			}
		})
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
			run.versions["fp-1"] = RemoteVersion{Fingerprint: "fp-1"}
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

// A locally restricted channel must arrive restricted, and restriction drift
// converges — the flag was silently dropped once (duf-cmh7).
func TestReconcileConvergesChannelRestriction(t *testing.T) {
	t.Run("created restricted", func(t *testing.T) {
		reconciler, repository, run, _ := newTestReconciler(t, "secret")
		repository.associations = []Association{testAssociation("images")}
		repository.snapshots["images"] = &BucketSnapshot{
			Name:     "images",
			Channels: []ChannelSnapshot{{Name: "production", Restricted: true}},
		}
		run.buckets["images"] = RemoteBucket{}
		run.channels["images"] = map[string]RemoteChannel{}
		if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
			t.Fatal(err)
		}
		if !run.channels["images"]["production"].Restricted {
			t.Fatalf("created channel is not restricted: %#v", run.channels["images"]["production"])
		}
	})
	t.Run("drifted restriction converges", func(t *testing.T) {
		reconciler, repository, run, _ := newTestReconciler(t, "secret")
		repository.associations = []Association{testAssociation("images")}
		repository.snapshots["images"] = &BucketSnapshot{
			Name:     "images",
			Channels: []ChannelSnapshot{{Name: "production", Restricted: true}},
		}
		run.buckets["images"] = RemoteBucket{}
		run.channels["images"] = map[string]RemoteChannel{
			"production": {Name: "production", Restricted: false},
		}
		if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
			t.Fatal(err)
		}
		if !run.channels["images"]["production"].Restricted {
			t.Fatalf("restriction drift did not converge: %#v", run.channels["images"]["production"])
		}
		joined := strings.Join(*run.events, ",")
		if !strings.Contains(joined, "update-channel-restriction:production:true") {
			t.Fatalf("no restriction update event: %s", joined)
		}
	})
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
				run.versions["fp-1"] = RemoteVersion{Fingerprint: "fp-1"}
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
				run.versions["fp-1"] = RemoteVersion{Fingerprint: "fp-1"}
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
	run.versions["fp-1"] = RemoteVersion{Fingerprint: "fp-1"}
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

func TestReconcileRemovesRemoteChannelDriftWithoutTouchingManagedLatestOrForeignBucket(t *testing.T) {
	reconciler, repository, run, writer := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images")}
	repository.snapshots["images"] = &BucketSnapshot{Name: "images"}
	seedDeletionInvariants(run, "images")
	run.channels["images"]["remote-only"] = RemoteChannel{Name: "remote-only"}

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	if len(run.deletedChannels) != 1 || run.deletedChannels[0] != "remote-only" {
		t.Fatalf("deleted channels = %v", run.deletedChannels)
	}
	assertDeletionInvariants(t, run, "images")
	assertAuditMutation(t, writer, "bagdrop.sync.channel.delete", "drift")
}

func TestReconcileRemovesRemoteVersionDriftWithoutTouchingForeignBucket(t *testing.T) {
	reconciler, repository, run, writer := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images")}
	repository.snapshots["images"] = &BucketSnapshot{
		Name: "images", Versions: []VersionSnapshot{{Fingerprint: "fp-local"}},
	}
	seedDeletionInvariants(run, "images")
	run.buckets["images"] = RemoteBucket{Versions: []RemoteVersion{
		{Fingerprint: "fp-local"}, {Fingerprint: "fp-remote"},
	}}
	run.versions["fp-local"] = RemoteVersion{Fingerprint: "fp-local"}

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	if len(run.deletedVersions) != 1 || run.deletedVersions[0] != "fp-remote" {
		t.Fatalf("deleted versions = %v", run.deletedVersions)
	}
	assertDeletionInvariants(t, run, "images")
	assertAuditMutation(t, writer, "bagdrop.sync.version.delete", "drift")
}

func TestReconcileRemovesRemoteBuildDriftWhenVendoredDeleteBuildExists(t *testing.T) {
	reconciler, repository, run, writer := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images")}
	repository.snapshots["images"] = testSnapshot("images")
	seedDeletionInvariants(run, "images")
	run.buckets["images"] = RemoteBucket{Description: "images description", Versions: []RemoteVersion{{Fingerprint: "fp-1"}}}
	run.versions["fp-1"] = RemoteVersion{Fingerprint: "fp-1"}
	run.builds["fp-1"] = []RemoteBuild{
		{ID: "local-build", ComponentType: "amazon-ebs", Status: "BUILD_DONE"},
		{ID: "remote-build", ComponentType: "googlecompute", Status: "BUILD_DONE"},
	}

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	if len(run.deletedBuilds) != 1 || run.deletedBuilds[0] != "fp-1/remote-build" {
		t.Fatalf("deleted builds = %v", run.deletedBuilds)
	}
	assertDeletionInvariants(t, run, "images")
	assertAuditMutation(t, writer, "bagdrop.sync.build.delete", "drift")
}

func TestReconcileDeletesChannelsThenVersionsThenSurvivingVersionBuilds(t *testing.T) {
	reconciler, repository, run, _ := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images")}
	repository.snapshots["images"] = &BucketSnapshot{
		Name: "images", Versions: []VersionSnapshot{{Fingerprint: "fp-local"}},
	}
	seedDeletionInvariants(run, "images")
	run.channels["images"]["remote-only"] = RemoteChannel{Name: "remote-only"}
	run.buckets["images"] = RemoteBucket{Versions: []RemoteVersion{
		{Fingerprint: "fp-local"}, {Fingerprint: "fp-remote"},
	}}
	run.versions["fp-local"] = RemoteVersion{Fingerprint: "fp-local"}
	run.builds["fp-local"] = []RemoteBuild{{ID: "remote-build", ComponentType: "amazon-ebs"}}

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(repository.events, ",")
	channelAt := strings.Index(joined, "delete-channel:remote-only")
	versionAt := strings.Index(joined, "delete-version:fp-remote")
	buildAt := strings.Index(joined, "delete-build:remote-build")
	if channelAt < 0 || versionAt <= channelAt || buildAt <= versionAt {
		t.Fatalf("delete order = %v", repository.events)
	}
	assertDeletionInvariants(t, run, "images")
}

func TestReconcilePropagatesLocalBucketDeleteAndConsumesAssociation(t *testing.T) {
	reconciler, repository, run, writer := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images")}
	repository.snapshots["images"] = nil
	seedDeletionInvariants(run, "images")

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	if len(run.deletedBuckets) != 1 || run.deletedBuckets[0] != "images" || len(repository.associations) != 0 {
		t.Fatalf("delete result buckets=%v associations=%#v", run.deletedBuckets, repository.associations)
	}
	assertForeignBucketUntouched(t, run)
	if len(run.deletedChannels) != 0 {
		t.Fatalf("managed latest received leaf delete: %v", run.deletedChannels)
	}
	assertAuditMutation(t, writer, "bagdrop.sync.bucket.delete", "local_delete")
}

func TestReconcileConsumesTombstoneAfterRemoteDelete(t *testing.T) {
	reconciler, repository, run, writer := newTestReconciler(t, "secret")
	tombstone := testAssociation("images")
	tombstone.State = AssociationPendingRemoval
	repository.associations = []Association{tombstone}
	seedDeletionInvariants(run, "images")

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	if len(run.deletedBuckets) != 1 || len(repository.associations) != 0 {
		t.Fatalf("delete result buckets=%v associations=%#v", run.deletedBuckets, repository.associations)
	}
	assertForeignBucketUntouched(t, run)
	if len(run.deletedChannels) != 0 {
		t.Fatalf("managed latest received leaf delete: %v", run.deletedChannels)
	}
	assertAuditMutation(t, writer, "bagdrop.sync.bucket.delete", "unassociate")
}

func TestReconcileFailedRemoteDeleteRetainsTombstoneAndConvergesLater(t *testing.T) {
	reconciler, repository, run, _ := newTestReconciler(t, "secret")
	tombstone := testAssociation("images")
	tombstone.State = AssociationPendingRemoval
	repository.associations = []Association{tombstone}
	seedDeletionInvariants(run, "images")
	run.deleteBucketErrors["images"] = &AdapterError{StatusCode: http.StatusInternalServerError, Code: 13, Summary: "refused"}

	if err := reconciler.ReconcileProject(context.Background(), repository.project); err == nil {
		t.Fatal("reconcile succeeded despite refused remote delete")
	}
	if len(repository.associations) != 1 || repository.associations[0].State != AssociationPendingRemoval ||
		!strings.Contains(repository.failures["images"], "HTTP 500 code 13: refused") {
		t.Fatalf("lost cleanup intent: associations=%#v failures=%v", repository.associations, repository.failures)
	}
	assertDeletionInvariants(t, run, "images")

	delete(run.deleteBucketErrors, "images")
	if err := reconciler.ReconcileProject(context.Background(), repository.project); err != nil {
		t.Fatal(err)
	}
	if len(repository.associations) != 0 || len(run.deletedBuckets) != 1 {
		t.Fatalf("retry did not converge: associations=%#v deletes=%v", repository.associations, run.deletedBuckets)
	}
	assertForeignBucketUntouched(t, run)
}

func TestReconcileDeleteRequestAuditFailurePreventsRemoteMutation(t *testing.T) {
	reconciler, repository, run, writer := newTestReconciler(t, "secret")
	repository.associations = []Association{testAssociation("images")}
	repository.snapshots["images"] = nil
	seedDeletionInvariants(run, "images")
	writer.failAt = 1

	err := reconciler.ReconcileProject(context.Background(), repository.project)
	if !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("error = %v, want audit unavailable", err)
	}
	if len(run.deletedBuckets) != 0 || len(repository.associations) != 1 {
		t.Fatalf("delete ran without request audit: deletes=%v associations=%#v",
			run.deletedBuckets, repository.associations)
	}
	assertDeletionInvariants(t, run, "images")
}

func seedDeletionInvariants(run *testReconcileRun, associated string) {
	run.buckets[associated] = RemoteBucket{}
	run.buckets["foreign"] = RemoteBucket{Description: "must survive"}
	run.channels[associated] = map[string]RemoteChannel{
		"latest": {Name: "latest", Managed: true},
	}
	run.channels["foreign"] = map[string]RemoteChannel{
		"latest":       {Name: "latest", Managed: true},
		"foreign-only": {Name: "foreign-only"},
	}
}

func assertDeletionInvariants(t *testing.T, run *testReconcileRun, associated string) {
	t.Helper()
	assertForeignBucketUntouched(t, run)
	latest, exists := run.channels[associated]["latest"]
	if !exists || !latest.Managed {
		t.Fatalf("managed latest was touched: %#v", run.channels[associated])
	}
}

func assertForeignBucketUntouched(t *testing.T, run *testReconcileRun) {
	t.Helper()
	if _, exists := run.buckets["foreign"]; !exists {
		t.Fatal("foreign unassociated bucket was deleted")
	}
	if len(run.channels["foreign"]) != 2 {
		t.Fatalf("foreign bucket contents were touched: %#v", run.channels["foreign"])
	}
}

func assertAuditMutation(t *testing.T, writer *testAuditWriter, operation, detail string) {
	t.Helper()
	found := 0
	for _, encoded := range writer.records {
		var event map[string]any
		if err := json.Unmarshal(encoded, &event); err != nil {
			t.Fatal(err)
		}
		if event["operation"] == operation {
			found++
			if detail != "" && event["detail"] != detail {
				t.Fatalf("%s detail = %#v, want %q", operation, event["detail"], detail)
			}
		}
	}
	if found != 2 {
		t.Fatalf("%s audit records = %d, want request and response", operation, found)
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
		events: &repository.events, buckets: make(map[string]RemoteBucket), versions: make(map[string]RemoteVersion),
		builds: make(map[string][]RemoteBuild), readFailures: make(map[string]error), createdBuckets: make(map[string]bool),
		channels: make(map[string]map[string]RemoteChannel), deleteBucketErrors: make(map[string]error),
		sboms: make(map[string][]RemoteSbom), uploadSbomErrors: make(map[string]error),
		completeVersions:   make(map[string]bool),
		artifacts:          make(map[string]map[string][]ArtifactSnapshot),
		correlatedAtCreate: make(map[string]bool),
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
func (r *testReconcileRepository) DeleteBagDropAssociation(_ context.Context, _, _, name string) error {
	for i := range r.associations {
		if r.associations[i].BucketName == name {
			r.associations = append(r.associations[:i], r.associations[i+1:]...)
			r.events = append(r.events, "delete-association:"+name)
			return nil
		}
	}
	return errors.New("association not found")
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
	versions           map[string]RemoteVersion
	builds             map[string][]RemoteBuild
	sboms              map[string][]RemoteSbom
	uploadSbomErrors   map[string]error
	uploadedSboms      []string
	revokedVersions    []string
	restoredVersions   []string
	readFailures       map[string]error
	createdBuckets     map[string]bool
	createBucketError  error
	updatedArtifacts   []ArtifactSnapshot
	channels           map[string]map[string]RemoteChannel
	createdChannels    []string
	updatedChannels    []string
	updateChannelError error
	deletedBuckets     []string
	deleteBucketErrors map[string]error
	deletedVersions    []string
	deletedBuilds      []string
	deletedChannels    []string
	completeVersions   map[string]bool
	artifacts          map[string]map[string][]ArtifactSnapshot
	correlatedAtCreate map[string]bool
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
func (r *testReconcileRun) DeleteBucket(_ context.Context, name string) error {
	*r.events = append(*r.events, "delete-bucket:"+name)
	if err := r.deleteBucketErrors[name]; err != nil {
		return err
	}
	delete(r.buckets, name)
	delete(r.channels, name)
	delete(r.artifacts, name)
	r.deletedBuckets = append(r.deletedBuckets, name)
	return nil
}
func (r *testReconcileRun) GetVersion(_ context.Context, _, fingerprint string) (*RemoteVersion, bool, error) {
	*r.events = append(*r.events, "get-version:"+fingerprint)
	version, exists := r.versions[fingerprint]
	return &version, exists, nil
}
func (r *testReconcileRun) CreateVersion(_ context.Context, _ string, version VersionSnapshot) error {
	*r.events = append(*r.events, "create-version:"+version.Fingerprint)
	r.versions[version.Fingerprint] = RemoteVersion{Fingerprint: version.Fingerprint}
	return nil
}
func (r *testReconcileRun) RevokeVersion(
	_ context.Context, bucket, fingerprint string, revokeAt time.Time, message string,
) error {
	*r.events = append(*r.events, "revoke-version:"+fingerprint)
	version := r.versions[fingerprint]
	version.RevokeAt = &revokeAt
	version.RevocationMessage = message
	r.versions[fingerprint] = version
	r.revokedVersions = append(r.revokedVersions, bucket+"/"+fingerprint)
	return nil
}
func (r *testReconcileRun) RestoreVersion(_ context.Context, bucket, fingerprint string) error {
	*r.events = append(*r.events, "restore-version:"+fingerprint)
	version := r.versions[fingerprint]
	version.RevokeAt = nil
	version.RevocationMessage = ""
	r.versions[fingerprint] = version
	r.restoredVersions = append(r.restoredVersions, bucket+"/"+fingerprint)
	return nil
}
func (r *testReconcileRun) DeleteVersion(_ context.Context, bucket, fingerprint string) error {
	*r.events = append(*r.events, "delete-version:"+fingerprint)
	for _, build := range r.builds[fingerprint] {
		delete(r.artifacts[bucket], build.ID)
	}
	delete(r.versions, fingerprint)
	delete(r.builds, fingerprint)
	delete(r.completeVersions, fingerprint)
	r.deletedVersions = append(r.deletedVersions, fingerprint)
	return nil
}
func (r *testReconcileRun) ListBuilds(_ context.Context, _, fingerprint string) ([]RemoteBuild, error) {
	*r.events = append(*r.events, "list-builds:"+fingerprint)
	return append([]RemoteBuild(nil), r.builds[fingerprint]...), nil
}
func (r *testReconcileRun) CreateBuild(_ context.Context, _, fingerprint string, build BuildSnapshot) (string, error) {
	*r.events = append(*r.events, "create-build:"+build.ComponentType)
	if r.completeVersions[fingerprint] {
		return "", &AdapterError{
			StatusCode: http.StatusBadRequest,
			Code:       9,
			Summary:    "This version is complete. If you wish to add a new build a new version must be created by changing the build fingerprint.",
		}
	}
	remote := RemoteBuild{ID: "remote-" + build.ComponentType, ComponentType: build.ComponentType, Status: "BUILD_PENDING"}
	r.correlatedAtCreate[remote.ID] = false
	for _, builds := range r.artifacts {
		for _, artifacts := range builds {
			for _, artifact := range artifacts {
				if artifact.ExternalIdentifier == build.SourceExternalIdentifier {
					r.correlatedAtCreate[remote.ID] = true
				}
			}
		}
	}
	r.builds[fingerprint] = append(r.builds[fingerprint], remote)
	return remote.ID, nil
}
func (r *testReconcileRun) UpdateBuildRunning(_ context.Context, _, fingerprint, id string) error {
	*r.events = append(*r.events, "update-build-running:"+id)
	for i := range r.builds[fingerprint] {
		if r.builds[fingerprint][i].ID == id {
			r.builds[fingerprint][i].Status = "BUILD_RUNNING"
		}
	}
	r.completeVersionIfAllBuildsDone(fingerprint)
	return nil
}
func (r *testReconcileRun) UpdateBuild(_ context.Context, bucket, fingerprint, id string, build BuildSnapshot) error {
	*r.events = append(*r.events, "update-build:"+build.ComponentType)
	for i := range r.builds[fingerprint] {
		if r.builds[fingerprint][i].ID == id {
			r.builds[fingerprint][i].Status = "BUILD_DONE"
		}
	}
	r.completeVersionIfAllBuildsDone(fingerprint)
	r.updatedArtifacts = append([]ArtifactSnapshot(nil), build.Artifacts...)
	if r.artifacts[bucket] == nil {
		r.artifacts[bucket] = make(map[string][]ArtifactSnapshot)
	}
	r.artifacts[bucket][id] = append([]ArtifactSnapshot(nil), build.Artifacts...)
	return nil
}

func (r *testReconcileRun) completeVersionIfAllBuildsDone(fingerprint string) {
	builds := r.builds[fingerprint]
	if len(builds) == 0 {
		return
	}
	for _, build := range builds {
		if build.Status != "BUILD_DONE" {
			return
		}
	}
	r.completeVersions[fingerprint] = true
}
func (r *testReconcileRun) DeleteBuild(_ context.Context, bucket, fingerprint, id string) error {
	*r.events = append(*r.events, "delete-build:"+id)
	builds := r.builds[fingerprint]
	for i := range builds {
		if builds[i].ID == id {
			r.builds[fingerprint] = append(builds[:i], builds[i+1:]...)
			break
		}
	}
	delete(r.artifacts[bucket], id)
	r.deletedBuilds = append(r.deletedBuilds, fingerprint+"/"+id)
	return nil
}
func (r *testReconcileRun) ListSboms(_ context.Context, bucket, fingerprint, buildID string) ([]RemoteSbom, error) {
	*r.events = append(*r.events, "list-sboms:"+buildID)
	key := bucket + "/" + fingerprint + "/" + buildID
	return append([]RemoteSbom(nil), r.sboms[key]...), nil
}
func (r *testReconcileRun) UploadSbom(
	_ context.Context, bucket, fingerprint, buildID string, sbom SbomSnapshot,
) error {
	key := bucket + "/" + fingerprint + "/" + buildID
	*r.events = append(*r.events, "upload-sbom:"+sbom.Name)
	var build *RemoteBuild
	for i := range r.builds[fingerprint] {
		if r.builds[fingerprint][i].ID == buildID {
			build = &r.builds[fingerprint][i]
			break
		}
	}
	if build == nil || build.Status != "BUILD_RUNNING" {
		return &AdapterError{
			StatusCode: http.StatusBadRequest,
			Code:       3,
			Summary:    "This build's status isn't Running, so sboms can not be uploaded",
		}
	}
	if err := r.uploadSbomErrors[key+"/"+sbom.Name]; err != nil {
		return err
	}
	r.sboms[key] = append(r.sboms[key], RemoteSbom{Name: sbom.Name, Format: sbom.Format})
	r.uploadedSboms = append(r.uploadedSboms, key+"/"+sbom.Name)
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
func (r *testReconcileRun) CreateChannel(_ context.Context, bucket string, channel ChannelSnapshot) error {
	*r.events = append(*r.events, "create-channel:"+channel.Name)
	if r.channels[bucket] == nil {
		r.channels[bucket] = make(map[string]RemoteChannel)
	}
	r.channels[bucket][channel.Name] = RemoteChannel{Name: channel.Name, Restricted: channel.Restricted}
	r.createdChannels = append(r.createdChannels, channel.Name)
	return nil
}

func (r *testReconcileRun) UpdateChannelRestriction(_ context.Context, bucket, name string, restricted bool) error {
	detail := "false"
	if restricted {
		detail = "true"
	}
	*r.events = append(*r.events, "update-channel-restriction:"+name+":"+detail)
	channel := r.channels[bucket][name]
	channel.Restricted = restricted
	r.channels[bucket][name] = channel
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
func (r *testReconcileRun) DeleteChannel(_ context.Context, bucket, name string) error {
	*r.events = append(*r.events, "delete-channel:"+name)
	channel := r.channels[bucket][name]
	if channel.Managed {
		return &AdapterError{StatusCode: http.StatusBadRequest, Code: 3, Summary: "managed latest cannot be deleted"}
	}
	delete(r.channels[bucket], name)
	r.deletedChannels = append(r.deletedChannels, name)
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
