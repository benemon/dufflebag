package hcp2023

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/compat/hcp2023/models"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/scan"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

const (
	testOrg     = "3f2b1c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	testProject = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	testBase    = "/packer/2023-01-01/organizations/" + testOrg + "/projects/" + testProject
)

var testTime = time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

func TestBucketEndpoints(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	response := request(t, server, http.MethodGet, testBase+"/buckets/images", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing bucket HTTP status = %d, want 404", response.Code)
	}
	if body := response.Body.String(); !strings.Contains(body, `"code":5`) {
		t.Fatalf("missing bucket body = %s, want code 5", body)
	} else if !strings.Contains(body,
		`"message":"Error: The bucket with identifier images does not exist."`) {
		t.Fatalf("missing bucket prose diverges from probe 02: %s", body)
	}

	response = request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{
		"name": "images", "description": "base images", "labels": map[string]string{"team": "platform"},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("CreateBucket status = %d: %s", response.Code, response.Body)
	}
	var created models.HashicorpCloudPacker20230101CreateBucketResponse
	decodeResponse(t, response, &created)
	if created.Bucket == nil || created.Bucket.Name != "images" ||
		created.Bucket.Description != "base images" ||
		created.Bucket.Labels["team"] != "platform" {
		t.Fatalf("CreateBucket response = %#v", created.Bucket)
	}
	response = request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), `"code":6`) ||
		!strings.Contains(response.Body.String(),
			`"message":"Error: The bucket with identifier images already exists."`) {
		t.Fatalf("duplicate CreateBucket diverges from probe 21: %d %s", response.Code, response.Body)
	}

	response = request(t, server, http.MethodGet, testBase+"/buckets/images", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GetBucket status = %d: %s", response.Code, response.Body)
	}
	var fetched models.HashicorpCloudPacker20230101GetBucketResponse
	decodeResponse(t, response, &fetched)
	if fetched.Bucket == nil || fetched.Bucket.ID != created.Bucket.ID {
		t.Fatalf("GetBucket response = %#v, want id %s", fetched.Bucket, created.Bucket.ID)
	}

	response = request(t, server, http.MethodPatch, testBase+"/buckets/images", map[string]any{
		"description": "updated", "labels": map[string]string{"team": "runtime"},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("UpdateBucket status = %d: %s", response.Code, response.Body)
	}
	var updated models.HashicorpCloudPacker20230101UpdateBucketResponse
	decodeResponse(t, response, &updated)
	if updated.Bucket == nil || updated.Bucket.Description != "updated" ||
		updated.Bucket.Labels["team"] != "runtime" {
		t.Fatalf("UpdateBucket response = %#v", updated.Bucket)
	}
}

func TestGetVersionNotFoundUsesAbortedStatus(t *testing.T) {
	server := newHandler(newFakeRepository(), testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	trail := &auditTrail{}
	server = audit.NewHTTPHandler(trail, server.(audit.Resolver), server, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))
	response := request(
		t,
		server,
		http.MethodGet,
		testBase+"/buckets/images/versions/unknown",
		nil,
	)
	body := response.Body.String()
	if response.Code == http.StatusNotFound {
		t.Fatalf("missing version used HTTP 404: %s", body)
	}
	if !strings.Contains(body, `"code":10`) {
		t.Fatalf("missing version body = %s, want code 10", body)
	}
	if strings.Contains(body, `"code":5`) {
		t.Fatalf("missing version body contains bucket not-found code: %s", body)
	}
	if strings.Count(body, `"code"`) != 1 {
		t.Fatalf("error envelope has another code field: %s", body)
	}
	if !strings.Contains(body,
		`"message":"Version with fingerprint unknown not found"`) {
		t.Fatalf("missing version prose diverges from probe 07: %s", body)
	}
	responses := trail.responses(t)
	assertAuditFields(t, responses[0], map[string]any{
		"operation": "version.read", "target_type": "version", "target_id": "unknown",
		"outcome": "refused", "reason": "version_not_found",
	})
}

func TestVersionNameReflectsCompletion(t *testing.T) {
	incomplete, err := registry.NewVersion(
		registry.NewID(testTime), "images", "incomplete", registry.TemplateHCL2, testTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	wireIncomplete, err := renderVersion(store.ParseTenant(testOrg, testProject), incomplete, nil, testTime)
	if err != nil {
		t.Fatal(err)
	}
	if got := wireIncomplete.Name; got != "v0" {
		t.Fatalf("incomplete version name = %q, want v0", got)
	}

	complete, err := registry.RestoreVersion(registry.Version{
		ID: registry.NewID(testTime.Add(time.Second)), BucketName: "images",
		Fingerprint: "complete", TemplateType: registry.TemplateHCL2,
		CreatedAt: testTime, UpdatedAt: testTime,
	}, true, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	wireComplete, err := renderVersion(store.ParseTenant(testOrg, testProject), complete, nil, testTime)
	if err != nil {
		t.Fatal(err)
	}
	if got := wireComplete.Name; got != "v7" {
		t.Fatalf("complete version name = %q, want v7", got)
	}
}

func TestGetVersionSerializesCompletionNames(t *testing.T) {
	repository := newFakeRepository()
	incomplete, err := registry.NewVersion(
		registry.NewID(testTime), "images", "incomplete", registry.TemplateHCL2, testTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	complete, err := registry.RestoreVersion(registry.Version{
		ID: registry.NewID(testTime.Add(time.Second)), BucketName: "images",
		Fingerprint: "complete", TemplateType: registry.TemplateHCL2,
		CreatedAt: testTime, UpdatedAt: testTime,
	}, true, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	repository.versions["images/incomplete"] = incomplete
	repository.versions["images/complete"] = complete
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	for fingerprint, wantName := range map[string]string{
		"incomplete": "v0",
		"complete":   "v3",
	} {
		response := request(
			t,
			server,
			http.MethodGet,
			testBase+"/buckets/images/versions/"+fingerprint,
			nil,
		)
		if response.Code != http.StatusOK {
			t.Fatalf("GetVersion(%s) status = %d: %s", fingerprint, response.Code, response.Body)
		}
		var body models.HashicorpCloudPacker20230101GetVersionResponse
		decodeResponse(t, response, &body)
		if body.Version == nil || body.Version.Name != wantName {
			t.Fatalf("GetVersion(%s) name = %#v, want %q", fingerprint, body.Version, wantName)
		}
	}
}

func TestCreateVersionRejectsUnsetTemplateType(t *testing.T) {
	for _, body := range []map[string]any{
		{"fingerprint": "fp"},
		{"fingerprint": "fp", "template_type": "TEMPLATE_TYPE_UNSET"},
	} {
		repository := newFakeRepository()
		server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
		response := request(
			t,
			server,
			http.MethodPost,
			testBase+"/buckets/images/versions",
			body,
		)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("CreateVersion unset template status = %d: %s", response.Code, response.Body)
		}
		if len(repository.versions) != 0 {
			t.Fatal("unset template type reached repository")
		}
	}
}

func TestCreateVersionRejectsTemplateTypeChange(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	path := testBase + "/buckets/images/versions"
	response := request(t, server, http.MethodPost, path, map[string]any{
		"fingerprint": "fp", "template_type": "HCL2",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("first CreateVersion status = %d: %s", response.Code, response.Body)
	}
	response = request(t, server, http.MethodPost, path, map[string]any{
		"fingerprint": "fp", "template_type": "JSON",
	})
	// Code 9 pairs with HTTP 400 on live HCP, not 409 (dossier §5.1; duf-xwx).
	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), `"code":9`) {
		t.Fatalf("template type change status/body = %d %s", response.Code, response.Body)
	}
}

func TestCreateVersionProjectsAuthenticatedPrincipalID(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	response := request(t, server, http.MethodPost, testBase+"/buckets/images/versions", map[string]any{
		"fingerprint": "fp", "template_type": "HCL2",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("CreateVersion status = %d: %s", response.Code, response.Body)
	}
	var created models.HashicorpCloudPacker20230101CreateVersionResponse
	decodeResponse(t, response, &created)
	if created.Version == nil || created.Version.AuthorID != "p-test" {
		t.Fatalf("CreateVersion author_id = %#v, want authenticated principal p-test", created.Version)
	}
	if stored := repository.versions["images/fp"]; stored == nil || stored.AuthorID != "p-test" {
		t.Fatalf("stored version author_id = %#v, want p-test", stored)
	}
}

func TestBuildMetadataSurvivesUpdateAndRegistryReads(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	request(t, server, http.MethodPost, testBase+"/buckets/images/versions", map[string]any{
		"fingerprint": "fp", "template_type": "HCL2",
	})
	created := request(t, server, http.MethodPost,
		testBase+"/buckets/images/versions/fp/builds",
		map[string]any{"component_type": "docker"})
	var build models.HashicorpCloudPacker20230101CreateBuildResponse
	decodeResponse(t, created, &build)

	packerMetadata := map[string]any{
		"options": map[string]any{"path": "template.pkr.hcl", "debug": false},
		"os":      map[string]any{"type": "darwin", "arch": "arm64", "version": "15.6"},
		"plugins": []any{map[string]any{"name": "docker", "version": "1.1.4"}},
	}
	updated := request(t, server, http.MethodPatch,
		testBase+"/buckets/images/versions/fp/builds/"+build.Build.ID,
		map[string]any{"metadata": map[string]any{"packer": packerMetadata}},
	)
	if updated.Code != http.StatusOK {
		t.Fatalf("UpdateBuild status = %d: %s", updated.Code, updated.Body)
	}

	for name, response := range map[string]*httptest.ResponseRecorder{
		"ListBuilds": request(t, server, http.MethodGet,
			testBase+"/buckets/images/versions/fp/builds", nil),
		"GetVersion": request(t, server, http.MethodGet,
			testBase+"/buckets/images/versions/fp", nil),
	} {
		var got *models.HashicorpCloudPacker20230101Build
		switch name {
		case "ListBuilds":
			var body models.HashicorpCloudPacker20230101ListBuildsResponse
			decodeResponse(t, response, &body)
			got = body.Builds[0]
		default:
			var body models.HashicorpCloudPacker20230101GetVersionResponse
			decodeResponse(t, response, &body)
			got = body.Version.Builds[0]
		}
		if got.Metadata == nil || !reflect.DeepEqual(got.Metadata.Packer, packerMetadata) {
			t.Fatalf("%s build metadata = %#v, want %#v", name, got.Metadata, packerMetadata)
		}
	}
}

func TestVersionRelationshipsAreProjected(t *testing.T) {
	repository := newFakeRepository()
	parent, err := registry.NewVersion(
		registry.NewID(testTime), "base", "base-fp", registry.TemplateHCL2, testTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	parent.HasDescendants = true
	child, err := registry.NewVersion(
		registry.NewID(testTime.Add(time.Second)), "derived", "derived-fp", registry.TemplateHCL2, testTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	child.Parents = &registry.VersionParents{Status: registry.AncestryOutOfDate}
	repository.versions["base/base-fp"] = parent
	repository.versions["derived/derived-fp"] = child
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	parentResponse := request(t, server, http.MethodGet,
		testBase+"/buckets/base/versions/base-fp", nil)
	var parentBody models.HashicorpCloudPacker20230101GetVersionResponse
	decodeResponse(t, parentResponse, &parentBody)
	if parentBody.Version == nil || !parentBody.Version.HasDescendants {
		t.Fatalf("parent has_descendants = %#v, want true", parentBody.Version)
	}

	childResponse := request(t, server, http.MethodGet,
		testBase+"/buckets/derived/versions/derived-fp", nil)
	var childBody models.HashicorpCloudPacker20230101GetVersionResponse
	decodeResponse(t, childResponse, &childBody)
	wantHref := testBase + "/buckets/derived/ancestry?type=ANCESTRY_TYPE_PARENTS&version_fingerprint=derived-fp"
	if childBody.Version == nil || childBody.Version.Parents == nil ||
		childBody.Version.Parents.Status == nil ||
		*childBody.Version.Parents.Status != models.HashicorpCloudPacker20230101AncestryStatusOUTOFDATE ||
		childBody.Version.Parents.Href != wantHref {
		t.Fatalf("child parents = %#v, want OUT_OF_DATE at %s", childBody.Version, wantHref)
	}
}

func TestBucketRelationshipsAreProjected(t *testing.T) {
	version, err := registry.RestoreVersion(registry.Version{
		ID:           registry.NewID(testTime),
		BucketName:   "derived",
		Fingerprint:  "derived-fp",
		TemplateType: registry.TemplateHCL2,
		Parents:      &registry.VersionParents{Status: registry.AncestryOutOfDate},
		CreatedAt:    testTime,
		UpdatedAt:    testTime,
	}, true, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	childrenStatus := registry.AncestryUpToDate
	wire, err := renderBucket(store.ParseTenant(testOrg, testProject), &store.Bucket{
		ID: registry.NewID(testTime), Name: "derived", Labels: map[string]string{},
		LatestVersion: version, ChildrenStatus: &childrenStatus,
		CreatedAt: testTime, UpdatedAt: testTime,
	}, testTime)
	if err != nil {
		t.Fatal(err)
	}
	parentsHref := testBase + "/buckets/derived/ancestry?type=ANCESTRY_TYPE_PARENTS"
	if wire.Parents == nil || wire.Parents.Status == nil ||
		*wire.Parents.Status != models.HashicorpCloudPacker20230101AncestryStatusOUTOFDATE ||
		wire.Parents.Href != parentsHref {
		t.Fatalf("bucket parents = %#v, want OUT_OF_DATE at %s", wire.Parents, parentsHref)
	}
	childrenHref := testBase + "/buckets/derived/ancestry?type=ANCESTRY_TYPE_CHILDREN"
	if wire.Children == nil || wire.Children.Status == nil ||
		*wire.Children.Status != models.HashicorpCloudPacker20230101AncestryStatusUPTODATE ||
		wire.Children.Href != childrenHref {
		t.Fatalf("bucket children = %#v, want UP_TO_DATE at %s", wire.Children, childrenHref)
	}
}

func TestBucketWithoutAncestryOmitsRelationships(t *testing.T) {
	version, err := registry.RestoreVersion(registry.Version{
		ID:           registry.NewID(testTime),
		BucketName:   "unrelated",
		Fingerprint:  "unrelated-fp",
		TemplateType: registry.TemplateHCL2,
		CreatedAt:    testTime,
		UpdatedAt:    testTime,
	}, true, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := renderBucket(store.ParseTenant(testOrg, testProject), &store.Bucket{
		ID: registry.NewID(testTime), Name: "unrelated", Labels: map[string]string{},
		LatestVersion: version, CreatedAt: testTime, UpdatedAt: testTime,
	}, testTime)
	if err != nil {
		t.Fatal(err)
	}
	if wire.Parents != nil || wire.Children != nil {
		t.Fatalf("bucket without ancestry = parents %#v, children %#v; want both absent",
			wire.Parents, wire.Children)
	}
}

func TestBuildEndpointsAreIdempotentAndHeartbeatSafe(t *testing.T) {
	repository := newFakeRepository()
	version, err := registry.NewVersion(
		registry.NewID(testTime), "images", "fp", registry.TemplateHCL2, testTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	repository.versions["images/fp"] = version
	now := testTime
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time {
		now = now.Add(time.Second)
		return now
	})
	buildsPath := testBase + "/buckets/images/versions/fp/builds"
	createBody := map[string]any{
		"component_type":  "docker",
		"packer_run_uuid": "run-1",
		"status":          "BUILD_UNSET",
		"artifacts":       []any{},
	}
	first := request(t, server, http.MethodPost, buildsPath, createBody)
	if first.Code != http.StatusOK {
		t.Fatalf("CreateBuild status = %d: %s", first.Code, first.Body)
	}
	var firstBody models.HashicorpCloudPacker20230101CreateBuildResponse
	decodeResponse(t, first, &firstBody)

	second := request(t, server, http.MethodPost, buildsPath, createBody)
	if second.Code != http.StatusOK {
		t.Fatalf("idempotent CreateBuild status = %d: %s", second.Code, second.Body)
	}
	var secondBody models.HashicorpCloudPacker20230101CreateBuildResponse
	decodeResponse(t, second, &secondBody)
	if firstBody.Build == nil || secondBody.Build == nil || firstBody.Build.ID != secondBody.Build.ID {
		t.Fatalf("idempotent CreateBuild responses = %#v %#v", firstBody.Build, secondBody.Build)
	}

	listed := request(t, server, http.MethodGet, buildsPath, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("ListBuilds status = %d: %s", listed.Code, listed.Body)
	}
	var listedBody models.HashicorpCloudPacker20230101ListBuildsResponse
	decodeResponse(t, listed, &listedBody)
	if len(listedBody.Builds) != 1 {
		t.Fatalf("ListBuilds count = %d, want 1", len(listedBody.Builds))
	}

	updatePath := buildsPath + "/" + firstBody.Build.ID
	// Packer's terminal UpdateBuild supplies these fields after correlating the
	// HCL data-source outputs (dossier §5.7). The real-client oracle is
	// make test-packer; this assertion keeps the ingestion seam covered in CI.
	updateBody := map[string]any{
		"status":            "BUILD_RUNNING",
		"artifacts":         []any{},
		"parent_version_id": "01PARENTVERSION",
		"parent_channel_id": "01PARENTCHANNEL",
	}
	running := request(t, server, http.MethodPatch, updatePath, updateBody)
	if running.Code != http.StatusOK {
		t.Fatalf("UpdateBuild status = %d: %s", running.Code, running.Body)
	}
	var runningBody models.HashicorpCloudPacker20230101UpdateBuildResponse
	decodeResponse(t, running, &runningBody)

	heartbeat := request(t, server, http.MethodPatch, updatePath, updateBody)
	if heartbeat.Code != http.StatusOK {
		t.Fatalf("heartbeat UpdateBuild status = %d: %s", heartbeat.Code, heartbeat.Body)
	}
	var heartbeatBody models.HashicorpCloudPacker20230101UpdateBuildResponse
	decodeResponse(t, heartbeat, &heartbeatBody)
	if !time.Time(heartbeatBody.Build.UpdatedAt).Equal(time.Time(runningBody.Build.UpdatedAt)) {
		t.Fatalf(
			"heartbeat changed updated_at from %v to %v",
			runningBody.Build.UpdatedAt,
			heartbeatBody.Build.UpdatedAt,
		)
	}
	persisted, err := repository.GetBuild(
		context.Background(), store.Tenant{}, "images", "fp", firstBody.Build.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ParentVersionID != "01PARENTVERSION" || persisted.ParentChannelID != "01PARENTCHANNEL" {
		t.Fatalf(
			"UpdateBuild parent ids = %q %q, want producer fields persisted",
			persisted.ParentVersionID, persisted.ParentChannelID,
		)
	}

	emptyParentNoop := request(t, server, http.MethodPatch, updatePath, map[string]any{
		"status": "BUILD_RUNNING", "artifacts": []any{},
		"parent_version_id": "", "parent_channel_id": "",
	})
	if emptyParentNoop.Code != http.StatusOK {
		t.Fatalf("empty parent no-op status = %d: %s", emptyParentNoop.Code, emptyParentNoop.Body)
	}
	for _, tc := range []struct {
		name    string
		field   string
		value   string
		message string
	}{
		{
			name: "parent version", field: "parent_version_id", value: "01DIFFERENTVERSION",
			message: "You cannot override a build's Parent Version ID if it has already been set.",
		},
		{
			name: "parent channel", field: "parent_channel_id", value: "01DIFFERENTCHANNEL",
			message: "You cannot override a build's Parent Channel ID if it has already been set.",
		},
	} {
		refused := request(t, server, http.MethodPatch, updatePath, map[string]any{
			"status": "BUILD_RUNNING", "artifacts": []any{}, tc.field: tc.value,
		})
		if refused.Code != http.StatusBadRequest ||
			!strings.Contains(refused.Body.String(), `"code":6`) ||
			!strings.Contains(refused.Body.String(), tc.message) {
			t.Fatalf("mutable %s refusal = %d %s", tc.name, refused.Code, refused.Body)
		}
	}
	persisted, err = repository.GetBuild(
		context.Background(), store.Tenant{}, "images", "fp", firstBody.Build.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ParentVersionID != "01PARENTVERSION" || persisted.ParentChannelID != "01PARENTCHANNEL" {
		t.Fatalf("refused parent mutation changed stored ids to %q %q",
			persisted.ParentVersionID, persisted.ParentChannelID)
	}
}

func TestChannelAndBucketListEndpoints(t *testing.T) {
	repository := newFakeRepository()
	for i, name := range []string{"images", "base"} {
		at := testTime.Add(time.Duration(i) * time.Second)
		repository.buckets[name] = &store.Bucket{
			ID: registry.NewID(at), Name: name, Labels: map[string]string{},
			CreatedAt: at, UpdatedAt: at,
		}
	}
	complete, err := registry.RestoreVersion(registry.Version{
		ID:           registry.NewID(testTime.Add(2 * time.Second)),
		BucketName:   "images",
		Fingerprint:  "complete",
		TemplateType: registry.TemplateHCL2,
		CreatedAt:    testTime,
		UpdatedAt:    testTime,
	}, true, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	incomplete, err := registry.NewVersion(
		registry.NewID(testTime.Add(3*time.Second)),
		"images",
		"incomplete",
		registry.TemplateHCL2,
		testTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	repository.versions["images/complete"] = complete
	repository.versions["images/incomplete"] = incomplete
	latestBuild := store.StoredBuild{
		Build: registry.Build{
			ID: registry.NewID(testTime.Add(4 * time.Second)), ComponentType: "docker",
			Status: registry.BuildDone, Platform: "docker", MetadataSeen: true,
		},
		VersionID: complete.ID,
		Labels:    map[string]string{},
		Metadata:  json.RawMessage(`{"packer":{}}`),
		CreatedAt: testTime,
		UpdatedAt: testTime,
	}
	repository.buckets["images"].LatestVersion = complete
	repository.buckets["images"].LatestVersionBuilds = []store.StoredBuild{latestBuild}
	repository.buckets["images"].Platforms = []string{"docker"}
	now := testTime.Add(4 * time.Second)
	server := newHandler(repository, fakePrincipals{role: identity.RoleMaintainer}, testAuthenticator{}, testLogger(), func() time.Time {
		now = now.Add(time.Second)
		return now
	})

	response := request(t, server, http.MethodGet, testBase+"/buckets", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("ListBuckets status = %d: %s", response.Code, response.Body)
	}
	var buckets models.HashicorpCloudPacker20230101ListBucketsResponse
	decodeResponse(t, response, &buckets)
	if len(buckets.Buckets) != 2 ||
		buckets.Buckets[0].Name != "base" ||
		buckets.Buckets[1].Name != "images" {
		t.Fatalf("ListBuckets response = %#v", buckets.Buckets)
	}
	if buckets.Buckets[1].LatestVersion == nil ||
		buckets.Buckets[1].LatestVersion.Fingerprint != complete.Fingerprint ||
		len(buckets.Buckets[1].LatestVersion.Builds) != 1 ||
		!reflect.DeepEqual(buckets.Buckets[1].Platforms, []string{"docker"}) {
		t.Fatalf("ListBuckets latest version/platforms = %#v", buckets.Buckets[1])
	}

	channelsPath := testBase + "/buckets/images/channels"
	response = request(t, server, http.MethodGet, channelsPath+"/missing", nil)
	if response.Code != http.StatusNotFound ||
		!strings.Contains(response.Body.String(), `"code":5`) ||
		strings.Count(response.Body.String(), `"code"`) != 1 {
		t.Fatalf("missing GetChannel status/body = %d %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(),
		`"message":"Error: The channel with identifier missing does not exist."`) {
		t.Fatalf("missing channel prose diverges from probe 08: %s", response.Body)
	}

	response = request(t, server, http.MethodPost, channelsPath, map[string]any{
		"name": "bad", "version_fingerprint": "incomplete",
	})
	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), `"code":9`) {
		t.Fatalf("CreateChannel incomplete status/body = %d %s", response.Code, response.Body)
	}
	if _, ok := repository.channels["images/bad"]; ok {
		t.Fatal("incomplete initial version created a channel")
	}

	response = request(t, server, http.MethodPost, channelsPath, map[string]any{
		"name": "initial", "version_fingerprint": "complete",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("CreateChannel with version status = %d: %s", response.Code, response.Body)
	}
	response = request(t, server, http.MethodPost, channelsPath, map[string]any{"name": "initial"})
	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), `"code":6`) ||
		!strings.Contains(response.Body.String(),
			`"message":"Error: The channel with identifier initial already exists."`) {
		t.Fatalf("duplicate CreateChannel diverges from probe 38: %d %s", response.Code, response.Body)
	}

	for _, body := range []map[string]any{
		{"name": "staging", "restricted": true},
		{"name": "production"},
	} {
		response = request(t, server, http.MethodPost, channelsPath, body)
		if response.Code != http.StatusOK {
			t.Fatalf("CreateChannel(%v) status = %d: %s", body, response.Code, response.Body)
		}
	}

	response = request(t, server, http.MethodGet, channelsPath, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("ListChannels status = %d: %s", response.Code, response.Body)
	}
	var channels models.HashicorpCloudPacker20230101ListChannelsResponse
	decodeResponse(t, response, &channels)
	if len(channels.Channels) != 3 {
		t.Fatalf("ListChannels count = %d, want 3", len(channels.Channels))
	}
	if channels.Channels[2].Name != "staging" {
		t.Fatalf("ListChannels ordering = %#v", channels.Channels)
	}

	response = request(t, server, http.MethodPatch, channelsPath+"/staging", map[string]any{
		"update_mask": "versionFingerprint", "version_fingerprint": "complete",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("UpdateChannel assignment status = %d: %s", response.Code, response.Body)
	}
	var updated models.HashicorpCloudPacker20230101UpdateChannelResponse
	decodeResponse(t, response, &updated)
	if updated.Channel == nil || updated.Channel.Version == nil ||
		updated.Channel.Version.Fingerprint != "complete" ||
		updated.Channel.Version.Name != "v1" ||
		updated.Channel.AuthorID != "p-test" {
		t.Fatalf("UpdateChannel response = %#v", updated.Channel)
	}

	response = request(t, server, http.MethodPatch, channelsPath+"/staging", map[string]any{
		"restricted": false, "update_mask": "restricted",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("UpdateChannel restriction status = %d: %s", response.Code, response.Body)
	}
	updated = models.HashicorpCloudPacker20230101UpdateChannelResponse{}
	decodeResponse(t, response, &updated)
	if updated.Channel.Restricted {
		t.Fatalf("UpdateChannel restriction response = %#v", updated.Channel)
	}

	response = request(t, server, http.MethodPatch, channelsPath+"/staging", map[string]any{
		"update_mask": "versionFingerprint", "version_fingerprint": "incomplete",
	})
	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), `"code":9`) {
		t.Fatalf("UpdateChannel incomplete status/body = %d %s", response.Code, response.Body)
	}

	response = request(t, server, http.MethodGet, channelsPath+"/staging", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GetChannel status = %d: %s", response.Code, response.Body)
	}
	var fetched models.HashicorpCloudPacker20230101GetChannelResponse
	decodeResponse(t, response, &fetched)
	if fetched.Channel == nil || fetched.Channel.Version == nil ||
		fetched.Channel.Version.Fingerprint != "complete" {
		t.Fatalf("GetChannel response = %#v", fetched.Channel)
	}

	response = request(t, server, http.MethodPost, channelsPath+"/assign", map[string]any{
		"source_channel": "staging", "target_channel": "production",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("AssignChannelVersion status = %d: %s", response.Code, response.Body)
	}
	var assigned models.HashicorpCloudPacker20230101AssignChannelVersionResponse
	decodeResponse(t, response, &assigned)
	if assigned.Fingerprint != "complete" ||
		assigned.TargetChannel == nil ||
		assigned.TargetChannel.Version == nil ||
		assigned.TargetChannel.Version.Name != "v1" {
		t.Fatalf("AssignChannelVersion response = %#v", assigned)
	}

	response = request(t, server, http.MethodGet, channelsPath+"/staging/history", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("ListChannelAssignmentHistory status = %d: %s", response.Code, response.Body)
	}
	var history models.HashicorpCloudPacker20230101ListChannelAssignmentHistoryResponse
	decodeResponse(t, response, &history)
	if history.Count != 1 || len(history.History) != 1 ||
		history.History[0].Version.Fingerprint != "complete" ||
		history.History[0].AuthorID != "p-test" {
		t.Fatalf("ListChannelAssignmentHistory response = %#v", history)
	}

	production := repository.channels["images/production"]
	response = request(t, server, http.MethodDelete, channelsPath+"/production", nil)
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "{}" {
		t.Fatalf("DeleteChannel status/body = %d %s", response.Code, response.Body)
	}
	if len(repository.channelHistory[production.ID]) != 1 {
		t.Fatal("DeleteChannel removed assignment history")
	}
	response = request(t, server, http.MethodGet, channelsPath+"/production", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("deleted GetChannel status = %d, want 404", response.Code)
	}
}

// Whatever no route serves must still answer a google.rpc.Status body: Packer
// regex-matches the error TEXT for a code, so http.ServeMux's text/plain "404
// page not found" (or 405) turns "unimplemented" into "undiagnosable" (review
// finding 7). Code 12 Unimplemented — the plane refuses what it does not serve.
func TestUnmatchedPathsAnswerAStatusBody(t *testing.T) {
	for _, c := range []struct {
		name   string
		method string
		path   string
	}{
		{"an unserved operation", http.MethodDelete, testBase + "/registry"},
		{"an unknown path", http.MethodGet, testBase + "/nonsense"},
		{"a method mismatch", http.MethodPost, testBase + "/registry"},
		{"the unimplemented 2021-04-30 tree", http.MethodGet,
			"/packer/2021-04-30/organizations/" + testOrg + "/projects/" + testProject + "/images"},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := newHandler(newFakeRepository(), testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
			trail := &auditTrail{}
			server = audit.NewHTTPHandler(trail, server.(audit.Resolver), server, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))
			response := request(t, server, c.method, c.path, nil)
			if response.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want 501; body %s", response.Code, response.Body)
			}
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			body := response.Body.String()
			if !strings.Contains(body, `"code":12`) {
				t.Fatalf("body lacks code 12: %s", body)
			}
			if strings.Count(body, `"code"`) != 1 {
				t.Fatalf("error envelope has another code field: %s", body)
			}
			responses := trail.responses(t)
			assertAuditFields(t, responses[0], map[string]any{
				"operation": "request.unimplemented", "target_type": "request",
				"outcome": "refused", "reason": "unimplemented",
			}, "organization_id", "project_id", "target_id")
		})
	}
}

func request(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, body any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), body); err != nil {
		t.Fatalf("decode response %s: %v", response.Body, err)
	}
}

type fakeRepository struct {
	// Injected failures, so error handling can be asserted rather than assumed.
	listBucketsErr  error
	createBucketErr error
	uploadSbomErr   error
	downloadSbomErr error
	assignCalls     int
	lastRevocation  store.RevocationRequest

	buckets        map[string]*store.Bucket
	channels       map[string]*store.Channel
	channelHistory map[registry.ID][]store.ChannelAssignment
	versions       map[string]*registry.Version
	builds         map[string][]store.StoredBuild
	sboms          map[string]store.Sbom
	packages       []store.ReportedPackage
	unparseable    []string
	scanStates     map[string]*store.BuildScanState
	scanRuns       map[string]*store.ScanRun
	scanFindings   map[string][]store.StoredFinding
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{
		buckets:        make(map[string]*store.Bucket),
		channels:       make(map[string]*store.Channel),
		channelHistory: make(map[registry.ID][]store.ChannelAssignment),
		versions:       make(map[string]*registry.Version),
		builds:         make(map[string][]store.StoredBuild),
		sboms:          make(map[string]store.Sbom),
		scanStates:     make(map[string]*store.BuildScanState),
		scanRuns:       make(map[string]*store.ScanRun),
		scanFindings:   make(map[string][]store.StoredFinding),
	}
}

func (r *fakeRepository) CreateBucket(
	_ context.Context,
	_ store.Tenant,
	bucket store.Bucket,
) (*store.Bucket, error) {
	if r.createBucketErr != nil {
		return nil, r.createBucketErr
	}
	if _, exists := r.buckets[bucket.Name]; exists {
		return nil, registry.ErrConflict
	}
	bucket.UpdatedAt = bucket.CreatedAt
	r.buckets[bucket.Name] = &bucket
	// Mirrors the real store: a bucket is born with its managed "latest"
	// channel in the same transaction (Appendix A probes 04-06).
	r.channels[bucket.Name+"/latest"] = &store.Channel{
		ID: registry.NewID(bucket.CreatedAt), BucketName: bucket.Name, Name: "latest",
		Restricted: true, Managed: true,
		CreatedAt: bucket.CreatedAt, UpdatedAt: bucket.CreatedAt,
	}
	return &bucket, nil
}

func (r *fakeRepository) GetBucket(
	_ context.Context,
	_ store.Tenant,
	name string,
) (*store.Bucket, error) {
	bucket, ok := r.buckets[name]
	if !ok {
		return nil, registry.ErrNotFound
	}
	return bucket, nil
}

func (r *fakeRepository) GetBucketWithLatestVersion(
	ctx context.Context,
	tenant store.Tenant,
	name string,
) (*store.Bucket, error) {
	return r.GetBucket(ctx, tenant, name)
}

func (r *fakeRepository) ListBuckets(
	_ context.Context,
	_ store.Tenant,
) ([]store.Bucket, error) {
	if r.listBucketsErr != nil {
		return nil, r.listBucketsErr
	}
	names := make([]string, 0, len(r.buckets))
	for name := range r.buckets {
		names = append(names, name)
	}
	sort.Strings(names)
	buckets := make([]store.Bucket, 0, len(names))
	for _, name := range names {
		buckets = append(buckets, *r.buckets[name])
	}
	return buckets, nil
}

func (r *fakeRepository) UpdateBucket(
	_ context.Context,
	_ store.Tenant,
	name, description string,
	labels map[string]string,
	at time.Time,
) (*store.Bucket, error) {
	bucket, ok := r.buckets[name]
	if !ok {
		return nil, registry.ErrNotFound
	}
	bucket.Description = description
	bucket.Labels = labels
	bucket.UpdatedAt = at
	return bucket, nil
}

func (r *fakeRepository) DeleteBucket(
	_ context.Context,
	_ store.Tenant,
	name string,
) error {
	if _, ok := r.buckets[name]; !ok {
		return registry.ErrNotFound
	}
	delete(r.buckets, name)
	for key := range r.versions {
		if strings.HasPrefix(key, name+"/") {
			delete(r.versions, key)
			delete(r.builds, key)
		}
	}
	for key := range r.channels {
		if strings.HasPrefix(key, name+"/") {
			delete(r.channels, key)
		}
	}
	for key := range r.sboms {
		if strings.HasPrefix(key, name+"/") {
			delete(r.sboms, key)
		}
	}
	return nil
}

func (r *fakeRepository) UploadSbom(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint, buildID string,
	sbom store.Sbom,
) (*store.Sbom, error) {
	if r.uploadSbomErr != nil {
		return nil, r.uploadSbomErr
	}
	build, err := r.GetBuild(context.Background(), store.Tenant{}, bucket, fingerprint, buildID)
	if err != nil {
		return nil, err
	}
	sbom.BuildID = build.ID
	key := bucket + "/" + fingerprint + "/" + buildID + "/" + sbom.Name
	// A replace keeps the original identity, as the store's upsert does.
	if existing, ok := r.sboms[key]; ok {
		sbom.ID, sbom.CreatedAt = existing.ID, existing.CreatedAt
	}
	r.sboms[key] = sbom
	return &sbom, nil
}

func (r *fakeRepository) ListSboms(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint, buildID string,
) ([]store.Sbom, error) {
	if _, err := r.GetBuild(context.Background(), store.Tenant{}, bucket, fingerprint, buildID); err != nil {
		return nil, err
	}
	prefix := bucket + "/" + fingerprint + "/" + buildID + "/"
	names := make([]string, 0)
	byName := make(map[string]store.Sbom)
	for key, sbom := range r.sboms {
		if strings.HasPrefix(key, prefix) {
			names = append(names, sbom.Name)
			byName[sbom.Name] = sbom
		}
	}
	sort.Strings(names)
	sboms := make([]store.Sbom, 0, len(names))
	for _, name := range names {
		sboms = append(sboms, byName[name])
	}
	return sboms, nil
}

func (r *fakeRepository) GetSbom(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint, buildID, name string,
) (*store.Sbom, error) {
	if _, err := r.GetBuild(context.Background(), store.Tenant{}, bucket, fingerprint, buildID); err != nil {
		return nil, err
	}
	sbom, ok := r.sboms[bucket+"/"+fingerprint+"/"+buildID+"/"+name]
	if !ok {
		return nil, registry.ErrNotFound
	}
	return &sbom, nil
}

func (r *fakeRepository) DownloadSbom(
	ctx context.Context,
	tenant store.Tenant,
	bucket, fingerprint, buildID, name string,
) ([]byte, error) {
	if r.downloadSbomErr != nil {
		return nil, r.downloadSbomErr
	}
	sbom, err := r.GetSbom(ctx, tenant, bucket, fingerprint, buildID, name)
	if err != nil {
		return nil, err
	}
	return sbom.CompressedData, nil
}

func (r *fakeRepository) ListBuildPackages(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint, buildID string,
) ([]store.ReportedPackage, []string, error) {
	if _, err := r.GetBuild(context.Background(), store.Tenant{}, bucket, fingerprint, buildID); err != nil {
		return nil, nil, err
	}
	return append([]store.ReportedPackage(nil), r.packages...), append([]string(nil), r.unparseable...), nil
}

func (r *fakeRepository) GetBuildScanState(
	_ context.Context,
	_ store.Tenant,
	buildID string,
) (*store.BuildScanState, error) {
	return r.scanStates[buildID], nil
}

func (r *fakeRepository) GetScanRun(
	_ context.Context,
	_ store.Tenant,
	runID string,
) (*store.ScanRun, error) {
	run, ok := r.scanRuns[runID]
	if !ok {
		return nil, registry.ErrNotFound
	}
	return run, nil
}

func (r *fakeRepository) ListScanFindings(
	_ context.Context,
	_ store.Tenant,
	runID string,
) ([]store.StoredFinding, error) {
	return append([]store.StoredFinding(nil), r.scanFindings[runID]...), nil
}

func (r *fakeRepository) CreateChannel(
	_ context.Context,
	_ store.Tenant,
	channel store.Channel,
	versionFingerprint, authorID string,
) (*store.Channel, error) {
	if _, ok := r.buckets[channel.BucketName]; !ok {
		return nil, registry.ErrNotFound
	}
	key := channel.BucketName + "/" + channel.Name
	if _, exists := r.channels[key]; exists {
		return nil, store.ErrChannelExists
	}
	channel.UpdatedAt = channel.CreatedAt
	r.channels[key] = &channel
	if versionFingerprint != "" {
		if err := r.assignChannel(&channel, versionFingerprint, authorID, channel.CreatedAt); err != nil {
			delete(r.channels, key)
			return nil, err
		}
	}
	return &channel, nil
}

func (r *fakeRepository) GetChannel(
	_ context.Context,
	_ store.Tenant,
	bucketName, channelName string,
) (*store.Channel, error) {
	channel, ok := r.channels[bucketName+"/"+channelName]
	if !ok {
		return nil, registry.ErrNotFound
	}
	return channel, nil
}

func (r *fakeRepository) ListChannels(
	_ context.Context,
	_ store.Tenant,
	bucketName string,
) ([]store.Channel, error) {
	if _, ok := r.buckets[bucketName]; !ok {
		return nil, registry.ErrNotFound
	}
	names := make([]string, 0)
	for key, channel := range r.channels {
		if strings.HasPrefix(key, bucketName+"/") {
			names = append(names, channel.Name)
		}
	}
	sort.Strings(names)
	channels := make([]store.Channel, 0, len(names))
	for _, name := range names {
		channels = append(channels, *r.channels[bucketName+"/"+name])
	}
	return channels, nil
}

func (r *fakeRepository) UpdateChannel(
	_ context.Context,
	_ store.Tenant,
	bucketName, channelName string,
	updateRestricted, restricted bool,
	updateVersion bool, versionFingerprint, authorID string,
	at time.Time,
) (*store.Channel, error) {
	channel, err := r.GetChannel(context.Background(), store.Tenant{}, bucketName, channelName)
	if err != nil {
		return nil, err
	}
	if updateRestricted {
		channel.Restricted = restricted
		channel.UpdatedAt = at
	}
	if updateVersion && versionFingerprint != "" {
		if err := r.assignChannel(channel, versionFingerprint, authorID, at); err != nil {
			return nil, err
		}
	} else if updateVersion {
		channel.Version = nil
		channel.UpdatedAt = at
	}
	return channel, nil
}

func (r *fakeRepository) AssignChannelVersion(
	_ context.Context,
	_ store.Tenant,
	bucketName, sourceName, targetName, authorID string,
	at time.Time,
) (*store.Channel, *store.Channel, error) {
	r.assignCalls++
	source, err := r.GetChannel(context.Background(), store.Tenant{}, bucketName, sourceName)
	if err != nil {
		return nil, nil, err
	}
	target, err := r.GetChannel(context.Background(), store.Tenant{}, bucketName, targetName)
	if err != nil {
		return nil, nil, err
	}
	if source.Version == nil {
		return nil, nil, registry.ErrConflict
	}
	if err := r.assignChannel(target, source.Version.Fingerprint, authorID, at); err != nil {
		return nil, nil, err
	}
	return source, target, nil
}

func (r *fakeRepository) DeleteChannel(
	_ context.Context,
	_ store.Tenant,
	bucketName, channelName string,
) error {
	key := bucketName + "/" + channelName
	if _, ok := r.channels[key]; !ok {
		return registry.ErrNotFound
	}
	delete(r.channels, key)
	return nil
}

func (r *fakeRepository) ListChannelAssignmentHistory(
	_ context.Context,
	_ store.Tenant,
	bucketName, channelName string,
) ([]store.ChannelAssignment, error) {
	channel, err := r.GetChannel(context.Background(), store.Tenant{}, bucketName, channelName)
	if err != nil {
		return nil, err
	}
	history := r.channelHistory[channel.ID]
	result := make([]store.ChannelAssignment, len(history))
	for i := range history {
		result[len(history)-1-i] = history[i]
	}
	return result, nil
}

func (r *fakeRepository) ListBucketAncestry(
	_ context.Context,
	_ store.Tenant,
	bucketName, ancestryType, channelName, versionFingerprint string,
) ([]store.BucketAncestry, error) {
	if _, ok := r.buckets[bucketName]; !ok {
		return nil, registry.ErrNotFound
	}
	latestFingerprint := versionFingerprint
	if latestFingerprint == "" {
		latestSequence := 0
		for _, version := range r.versions {
			if version.BucketName != bucketName {
				continue
			}
			if sequence, complete := version.Sequence(); complete && sequence > latestSequence {
				latestSequence, latestFingerprint = sequence, version.Fingerprint
			}
		}
	}
	relations := make([]store.BucketAncestry, 0)
	for _, builds := range r.builds {
		for i := range builds {
			build := &builds[i]
			if build.ParentVersionID == "" {
				continue
			}
			parent := r.versionByID(build.ParentVersionID)
			child := r.versionByID(build.VersionID.String())
			if parent == nil || child == nil {
				continue
			}
			channel := r.channelByID(build.ParentChannelID)
			isParent := ancestryType != string(models.HashicorpCloudPacker20230101BucketAncestryTypeANCESTRYTYPECHILDREN) &&
				child.BucketName == bucketName && child.Fingerprint == latestFingerprint
			isChild := ancestryType != string(models.HashicorpCloudPacker20230101BucketAncestryTypeANCESTRYTYPEPARENTS) &&
				parent.BucketName == bucketName &&
				(channelName == "" || channel != nil && channel.Name == channelName)
			if !isParent && !isChild {
				continue
			}
			relation := store.BucketAncestry{
				Parent: ancestryVersion(parent),
				Child:  ancestryVersion(child),
			}
			if channel != nil {
				relation.ParentChannelName = channel.Name
				if channel.Version != nil {
					current := ancestryVersion(channel.Version)
					relation.ParentChannelVersion = &current
				}
			}
			relations = append(relations, relation)
		}
	}
	return relations, nil
}

func (r *fakeRepository) versionByID(id string) *registry.Version {
	for _, version := range r.versions {
		if version.ID.String() == id {
			return version
		}
	}
	return nil
}

func (r *fakeRepository) channelByID(id string) *store.Channel {
	for _, channel := range r.channels {
		if channel.ID.String() == id {
			return channel
		}
	}
	return nil
}

func ancestryVersion(version *registry.Version) store.AncestryVersion {
	sequence, _ := version.Sequence()
	return store.AncestryVersion{
		ID: version.ID, BucketName: version.BucketName,
		Fingerprint: version.Fingerprint, Sequence: sequence,
	}
}

func (r *fakeRepository) assignChannel(
	channel *store.Channel,
	versionFingerprint, authorID string,
	at time.Time,
) error {
	version, err := r.GetVersion(
		context.Background(), store.Tenant{}, channel.BucketName, versionFingerprint,
	)
	if err != nil {
		return err
	}
	if err := version.AssignableToChannel(); err != nil {
		return err
	}
	channel.Version = version
	channel.AssignmentAuthorID = authorID
	channel.UpdatedAt = at
	r.channelHistory[channel.ID] = append(r.channelHistory[channel.ID], store.ChannelAssignment{
		ID:         registry.NewID(at),
		ChannelID:  channel.ID,
		Version:    version,
		AuthorID:   authorID,
		AssignedAt: at,
	})
	return nil
}

func (r *fakeRepository) CreateVersion(
	_ context.Context,
	_ store.Tenant,
	version *registry.Version,
) (*registry.Version, error) {
	key := version.BucketName + "/" + version.Fingerprint
	if existing, ok := r.versions[key]; ok {
		if err := existing.EnsureTemplateType(version.TemplateType); err != nil {
			return nil, err
		}
		return existing, nil
	}
	r.versions[key] = version
	return version, nil
}

func (r *fakeRepository) GetVersion(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint string,
) (*registry.Version, error) {
	version, ok := r.versions[bucket+"/"+fingerprint]
	if !ok {
		return nil, registry.ErrNotFound
	}
	return version, nil
}

func (r *fakeRepository) DeleteVersion(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint string,
	_ time.Time,
) error {
	key := bucket + "/" + fingerprint
	version, ok := r.versions[key]
	if !ok {
		return registry.ErrNotFound
	}
	var channels []string
	for _, channel := range r.channels {
		if channel.BucketName == bucket && !channel.Managed && channel.Version == version {
			channels = append(channels, channel.Name)
		}
	}
	if len(channels) != 0 {
		sort.Strings(channels)
		return &store.VersionAssignedError{Channels: channels}
	}
	delete(r.versions, key)
	delete(r.builds, key)
	for _, channel := range r.channels {
		if channel.Version == version {
			channel.Version = nil
		}
	}
	return nil
}

func TestNeverRevokedVersionRendersNullRevokeAtLikeLiveContract(t *testing.T) {
	version, err := registry.RestoreVersion(registry.Version{
		ID:           registry.NewID(testTime),
		BucketName:   "images",
		Fingerprint:  "fp-1",
		TemplateType: registry.TemplateHCL2,
		CreatedAt:    testTime,
		UpdatedAt:    testTime,
	}, true, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := renderVersion(store.ParseTenant(testOrg, testProject), version, nil, testTime)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	// Fixture source: Appendix A.7 verbatim capture and the S3a live proof.
	if !bytes.Contains(encoded, []byte(`"revoke_at":null`)) ||
		bytes.Contains(encoded, []byte(`"revoke_at":"0001-01-01T00:00:00.000Z"`)) {
		t.Fatalf("never-revoked version revoke_at = %s, want byte-faithful null", encoded)
	}
}

func TestRestoredVersionRendersLikeNeverRevokedVersion(t *testing.T) {
	version, err := registry.RestoreVersion(registry.Version{
		ID:           registry.NewID(testTime),
		BucketName:   "images",
		Fingerprint:  "fp-restored",
		TemplateType: registry.TemplateHCL2,
		CreatedAt:    testTime,
		UpdatedAt:    testTime,
	}, true, 1, &registry.Revocation{
		RevokeAt: testTime.Add(time.Hour), Message: "scheduled", Author: "ops",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := version.Restore(testTime.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	wire, err := renderVersion(store.ParseTenant(testOrg, testProject), version, nil, testTime)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"revoke_at":null`)) ||
		bytes.Contains(encoded, []byte(`"revocation_message"`)) ||
		bytes.Contains(encoded, []byte(`"revocation_author"`)) ||
		bytes.Contains(encoded, []byte(`"revocation_type"`)) ||
		bytes.Contains(encoded, []byte(`"revocation_inherited_from"`)) {
		t.Fatalf("restored version = %s, want the never-revoked revocation shape", encoded)
	}
	if wire.Status == nil || *wire.Status != models.HashicorpCloudPacker20230101VersionStatusVERSIONACTIVE {
		t.Fatalf("restored status = %v, want VERSION_ACTIVE", wire.Status)
	}
}

func TestVersionRevocationRendersStatusAndFields(t *testing.T) {
	tenant := store.ParseTenant(testOrg, testProject)
	base := registry.Version{
		ID: registry.NewID(testTime), BucketName: "images", Fingerprint: "fp-1",
		TemplateType: registry.TemplateHCL2,
	}

	manual, err := registry.RestoreVersion(base, true, 3, &registry.Revocation{
		RevokeAt: testTime.Add(-time.Hour), Message: "CVE-2026-0001", Author: "ops",
	})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := renderVersion(tenant, manual, nil, testTime)
	if err != nil {
		t.Fatal(err)
	}
	if *wire.Status != models.HashicorpCloudPacker20230101VersionStatusVERSIONREVOKED {
		t.Fatalf("status = %s; an effective revocation must render VERSION_REVOKED", *wire.Status)
	}
	if wire.Name != "v3" {
		t.Fatalf("name = %s; revocation must not disturb the completion name", wire.Name)
	}
	if wire.RevocationMessage != "CVE-2026-0001" || wire.RevocationAuthor != "ops" {
		t.Fatalf("message/author = %q/%q; want the recorded values", wire.RevocationMessage, wire.RevocationAuthor)
	}
	if *wire.RevocationType != models.HashicorpCloudPacker20230101RevocationTypeMANUAL {
		t.Fatalf("revocation_type = %s; a direct revocation is MANUAL", *wire.RevocationType)
	}
	if wire.RevocationInheritedFrom != nil {
		t.Fatal("a manual revocation names no ancestor")
	}
	if wire.RevokeAt == nil || time.Time(*wire.RevokeAt).IsZero() {
		t.Fatal("revoke_at must be rendered")
	}

	// A future effect time is scheduled, not revoked — Packer's data sources
	// refuse only VERSION_REVOKED, so a scheduled version stays consumable.
	scheduled, err := registry.RestoreVersion(base, true, 3, &registry.Revocation{
		RevokeAt: testTime.Add(time.Hour), Author: "ops",
	})
	if err != nil {
		t.Fatal(err)
	}
	wire, err = renderVersion(tenant, scheduled, nil, testTime)
	if err != nil {
		t.Fatal(err)
	}
	if *wire.Status != models.HashicorpCloudPacker20230101VersionStatusVERSIONREVOCATIONSCHEDULED {
		t.Fatalf("status = %s; a future revoke_at must render VERSION_REVOCATION_SCHEDULED", *wire.Status)
	}

	ancestorID := registry.NewID(testTime)
	inherited, err := registry.RestoreVersion(base, true, 3, &registry.Revocation{
		RevokeAt: testTime.Add(-time.Hour), Message: "CVE-2026-0001", Author: "ops",
		InheritedFrom: &registry.RevokedAncestor{
			VersionID: ancestorID, BucketName: "base-images", Fingerprint: "fp-base", VersionName: "v7",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wire, err = renderVersion(tenant, inherited, nil, testTime)
	if err != nil {
		t.Fatal(err)
	}
	if *wire.RevocationType != models.HashicorpCloudPacker20230101RevocationTypeINHERITED {
		t.Fatalf("revocation_type = %s; an ancestor means INHERITED", *wire.RevocationType)
	}
	ancestor := wire.RevocationInheritedFrom
	if ancestor == nil || ancestor.BucketName != "base-images" || ancestor.VersionFingerprint != "fp-base" ||
		ancestor.VersionName != "v7" || ancestor.VersionID != ancestorID.String() {
		t.Fatalf("revocation_inherited_from = %+v; want the ancestor's identity", ancestor)
	}
	wantHref := testBase + "/buckets/base-images/versions/fp-base"
	if ancestor.Href != wantHref {
		t.Fatalf("ancestor href = %s; want %s", ancestor.Href, wantHref)
	}
}

func TestUpdateVersionRevokesThroughTheWire(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	request(t, server, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp-1", "template_type": "HCL2"})

	// Unsupported or mutually exclusive body fields are refused loudly.
	for name, body := range map[string]map[string]any{
		"complete":              {"complete": true},
		"restore and revoke_at": {"restore": true, "revoke_at": testTime.Format(time.RFC3339)},
		"restore and revoke_in": {"restore": true, "revoke_in": "1h"},
		"both revoke fields":    {"revoke_at": testTime.Format(time.RFC3339), "revoke_in": "1h"},
		"neither revoke field":  {},
		"unparseable revoke_in": {"revoke_in": "5w"},
	} {
		response := request(t, server, http.MethodPatch, testBase+"/buckets/images/versions/fp-1", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400; body %s", name, response.Code, response.Body.String())
		}
		if !strings.Contains(response.Body.String(), `"code":3`) {
			t.Fatalf("%s: body %s; want code 3", name, response.Body.String())
		}
	}

	response := request(t, server, http.MethodPatch, testBase+"/buckets/images/versions/fp-1",
		map[string]any{"restore": true})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("restore active: status = %d, want 400; body %s", response.Code, response.Body.String())
	}
	var refusal models.GoogleRPCStatus
	decodeResponse(t, response, &refusal)
	wantRestoreRefusal := "Restoring does not apply. This version is valid and it is not scheduled to be revoked. "
	if refusal.Code != 9 || refusal.Message != wantRestoreRefusal {
		t.Fatalf("restore active = code %d message %q; want code 9 message %q",
			refusal.Code, refusal.Message, wantRestoreRefusal)
	}

	request(t, server, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp-opt-out", "template_type": "HCL2"})
	response = request(t, server, http.MethodPatch, testBase+"/buckets/images/versions/fp-opt-out",
		map[string]any{"disable_rollback_channels": true, "revoke_in": "0s"})
	if response.Code != http.StatusOK {
		t.Fatalf("disable rollback: status = %d; body %s", response.Code, response.Body.String())
	}
	if !repository.lastRevocation.DisableRollbackChannels {
		t.Fatal("disable_rollback_channels was not threaded to the repository")
	}

	// revoke_in schedules relative to the server clock.
	response = request(t, server, http.MethodPatch, testBase+"/buckets/images/versions/fp-1",
		map[string]any{"revoke_in": "1d2h", "revocation_message": "rotate the base image"})
	if response.Code != http.StatusOK {
		t.Fatalf("revoke: status = %d; body %s", response.Code, response.Body.String())
	}
	var updated models.HashicorpCloudPacker20230101UpdateVersionResponse
	decodeResponse(t, response, &updated)
	if *updated.Version.Status != models.HashicorpCloudPacker20230101VersionStatusVERSIONREVOCATIONSCHEDULED {
		t.Fatalf("status = %s; want scheduled", *updated.Version.Status)
	}
	if updated.Version.RevokeAt == nil {
		t.Fatal("revoke_at was not rendered")
	}
	if got := time.Time(*updated.Version.RevokeAt); !got.Equal(testTime.Add(26 * time.Hour)) {
		t.Fatalf("revoke_at = %v; want now+1d2h", got)
	}
	// The author is the principal's customer-chosen name, not its ID.
	if updated.Version.RevocationAuthor != "test" {
		t.Fatalf("revocation_author = %q; want the principal name", updated.Version.RevocationAuthor)
	}
	if updated.Version.RevocationMessage != "rotate the base image" {
		t.Fatalf("revocation_message = %q", updated.Version.RevocationMessage)
	}

	// Re-revoking is a conflict: 400 with code 9, like the other state conflicts.
	response = request(t, server, http.MethodPatch, testBase+"/buckets/images/versions/fp-1",
		map[string]any{"revoke_in": "0s"})
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":9`) {
		t.Fatalf("re-revoke: status %d body %s; want 400 code 9", response.Code, response.Body.String())
	}

	response = request(t, server, http.MethodPatch, testBase+"/buckets/images/versions/fp-1",
		map[string]any{"restore": true})
	if response.Code != http.StatusOK {
		t.Fatalf("restore revoked: status = %d; body %s", response.Code, response.Body.String())
	}
	updated = models.HashicorpCloudPacker20230101UpdateVersionResponse{}
	decodeResponse(t, response, &updated)
	if updated.Version.Status == nil ||
		*updated.Version.Status != models.HashicorpCloudPacker20230101VersionStatusVERSIONRUNNING {
		t.Fatalf("restored status = %v; want running for an incomplete version", updated.Version.Status)
	}
	if updated.Version.RevokeAt != nil || updated.Version.RevocationMessage != "" ||
		updated.Version.RevocationAuthor != "" || updated.Version.RevocationType != nil ||
		updated.Version.RevocationInheritedFrom != nil {
		t.Fatalf("restored revocation fields = %#v; want cleared", updated.Version)
	}

	// The version-not-found shape matches GetVersion exactly — 409 with code 10,
	// the Aborted quirk packer's regex reads.
	response = request(t, server, http.MethodPatch, testBase+"/buckets/images/versions/fp-unknown",
		map[string]any{"revoke_in": "0s"})
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":10`) {
		t.Fatalf("unknown fingerprint: status %d body %s; want 409 code 10", response.Code, response.Body.String())
	}
}

func TestUpdateVersionRestoreResponses(t *testing.T) {
	inherited := registry.Revocation{
		RevokeAt: testTime.Add(time.Hour), Author: "ops",
		InheritedFrom: &registry.RevokedAncestor{
			VersionID: registry.NewID(testTime), BucketName: "base",
			Fingerprint: "base-fp", VersionName: "v1",
		},
	}
	cases := []struct {
		name        string
		revocation  *registry.Revocation
		wantStatus  int
		wantMessage string
	}{
		{
			name: "active", wantStatus: http.StatusBadRequest,
			wantMessage: "Restoring does not apply. This version is valid and it is not scheduled to be revoked. ",
		},
		{name: "manually revoked", revocation: &registry.Revocation{
			RevokeAt: testTime.Add(time.Hour), Author: "ops",
		}, wantStatus: http.StatusOK},
		{
			name: "inherited revocation", revocation: &inherited, wantStatus: http.StatusBadRequest,
			wantMessage: "Directly restoring this version does not apply. The revocation status is inherited from an ancestor version. To restore this version, the revoked ancestor should be restored.",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repository := newFakeRepository()
			server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
			request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
			request(t, server, http.MethodPost, testBase+"/buckets/images/versions",
				map[string]any{"fingerprint": "fp-1", "template_type": "HCL2"})
			version := repository.versions["images/fp-1"]
			if c.revocation != nil {
				if err := version.Revoke(*c.revocation, testTime); err != nil {
					t.Fatal(err)
				}
			}

			response := request(t, server, http.MethodPatch, testBase+"/buckets/images/versions/fp-1",
				map[string]any{"restore": true})
			if response.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", response.Code, c.wantStatus, response.Body)
			}
			if c.wantStatus == http.StatusOK {
				if version.Revocation() != nil {
					t.Fatalf("manual revocation = %+v; want cleared", version.Revocation())
				}
				return
			}
			var refusal models.GoogleRPCStatus
			decodeResponse(t, response, &refusal)
			if refusal.Code != 9 || refusal.Message != c.wantMessage {
				t.Fatalf("refusal = code %d message %q; want code 9 message %q",
					refusal.Code, refusal.Message, c.wantMessage)
			}
		})
	}
}

func TestUpdateVersionRestoreAuditsRefusalAndSuccess(t *testing.T) {
	repository := newFakeRepository()
	seed := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	request(t, seed, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	request(t, seed, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp-1", "template_type": "HCL2"})

	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	trail := &auditTrail{}
	server = audit.NewHTTPHandler(trail, server.(audit.Resolver), server,
		audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))
	response := request(t, server, http.MethodPatch, testBase+"/buckets/images/versions/fp-1",
		map[string]any{"restore": true})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("restore active status = %d, want 400", response.Code)
	}
	request(t, seed, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp-inherited", "template_type": "HCL2"})
	inherited := repository.versions["images/fp-inherited"]
	if err := inherited.Revoke(registry.Revocation{
		RevokeAt: testTime.Add(time.Hour), Author: "ops",
		InheritedFrom: &registry.RevokedAncestor{
			VersionID: registry.NewID(testTime), BucketName: "base",
			Fingerprint: "base-fp", VersionName: "v1",
		},
	}, testTime); err != nil {
		t.Fatal(err)
	}
	response = request(t, server, http.MethodPatch, testBase+"/buckets/images/versions/fp-inherited",
		map[string]any{"restore": true})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("restore inherited status = %d, want 400", response.Code)
	}
	version := repository.versions["images/fp-1"]
	if err := version.Revoke(registry.Revocation{
		RevokeAt: testTime.Add(time.Hour), Author: "ops",
	}, testTime); err != nil {
		t.Fatal(err)
	}
	response = request(t, server, http.MethodPatch, testBase+"/buckets/images/versions/fp-1",
		map[string]any{"restore": true})
	if response.Code != http.StatusOK {
		t.Fatalf("restore revoked status = %d, want 200; body %s", response.Code, response.Body)
	}

	responses := trail.responses(t)
	if len(responses) != 3 || len(trail.records) != 6 {
		t.Fatalf("audit records = %#v; want request/response pairs for two refusals and success", trail.records)
	}
	assertAuditFields(t, responses[0], map[string]any{
		"operation": "version.update", "target_type": "version", "target_id": "fp-1",
		"outcome": "refused", "reason": "version_not_revoked",
	})
	assertAuditFields(t, responses[1], map[string]any{
		"operation": "version.update", "target_type": "version", "target_id": "fp-inherited",
		"outcome": "refused", "reason": "version_revocation_inherited",
	})
	assertAuditFields(t, responses[2], map[string]any{
		"operation": "version.update", "target_type": "version", "target_id": "fp-1",
		"outcome": "success",
	}, "reason")
}

func TestParseRevokeIn(t *testing.T) {
	for value, want := range map[string]time.Duration{
		"0s":    0,
		"30d":   30 * 24 * time.Hour,
		"2h45m": 2*time.Hour + 45*time.Minute,
		"1d2h":  26 * time.Hour,
	} {
		got, err := parseRevokeIn(value)
		if err != nil || got != want {
			t.Fatalf("parseRevokeIn(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	// The two giant day counts would wrap the day-to-hours multiplication into
	// a small or negative duration if accepted (found in review).
	for _, value := range []string{
		"", "5w", "-1h", "1.5h", "h", "10", "1h ", "∞",
		"768614336404564651d", "9223372036854775807d", "99999999999999d",
	} {
		if _, err := parseRevokeIn(value); err == nil {
			t.Fatalf("parseRevokeIn(%q) must be rejected", value)
		}
	}
}

func (r *fakeRepository) RevokeVersion(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint string,
	req store.RevocationRequest,
	_ func(*registry.Version) string,
	at time.Time,
) (*registry.Version, error) {
	version, ok := r.versions[bucket+"/"+fingerprint]
	if !ok {
		return nil, registry.ErrNotFound
	}
	r.lastRevocation = req
	// The fake models no ancestry graph; inheritance is the store's concern
	// and is proven by the integration suite.
	if err := version.Revoke(registry.Revocation{
		RevokeAt: req.RevokeAt, Message: req.Message, Author: req.Author,
	}, at); err != nil {
		return nil, err
	}
	return version, nil
}

func (r *fakeRepository) RestoreRevokedVersion(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint string,
	at time.Time,
) (*registry.Version, error) {
	version, ok := r.versions[bucket+"/"+fingerprint]
	if !ok {
		return nil, registry.ErrNotFound
	}
	if err := version.Restore(at); err != nil {
		return nil, err
	}
	return version, nil
}

func (r *fakeRepository) ListVersions(
	_ context.Context,
	_ store.Tenant,
	bucket string,
) ([]*registry.Version, error) {
	// Sorted so the fake matches the repository's contract: the handler's output
	// order is part of what the tests assert.
	var versions []*registry.Version
	for key, version := range r.versions {
		if strings.HasPrefix(key, bucket+"/") {
			versions = append(versions, version)
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].Fingerprint > versions[j].Fingerprint
	})
	return versions, nil
}

func (r *fakeRepository) CreateBuild(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint string,
	templateType registry.TemplateType,
	build store.StoredBuild,
	_ func(*registry.Version) string,
) (*store.StoredBuild, error) {
	version, err := r.GetVersion(context.Background(), store.Tenant{}, bucket, fingerprint)
	if err != nil {
		return nil, err
	}
	if err := version.EnsureTemplateType(templateType); err != nil {
		return nil, err
	}
	key := bucket + "/" + fingerprint
	for i := range r.builds[key] {
		if r.builds[key][i].ComponentType == build.ComponentType {
			return &r.builds[key][i], nil
		}
	}
	build.VersionID = version.ID
	build.UpdatedAt = build.CreatedAt
	r.builds[key] = append(r.builds[key], build)
	return &r.builds[key][len(r.builds[key])-1], nil
}

func (r *fakeRepository) ListBuilds(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint string,
) ([]store.StoredBuild, error) {
	if _, err := r.GetVersion(context.Background(), store.Tenant{}, bucket, fingerprint); err != nil {
		return nil, err
	}
	return r.builds[bucket+"/"+fingerprint], nil
}

func (r *fakeRepository) GetBuild(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint, buildID string,
) (*store.StoredBuild, error) {
	for i := range r.builds[bucket+"/"+fingerprint] {
		if r.builds[bucket+"/"+fingerprint][i].ID.String() == buildID {
			build := r.builds[bucket+"/"+fingerprint][i]
			return &build, nil
		}
	}
	return nil, registry.ErrNotFound
}

func (r *fakeRepository) UpdateBuild(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint string,
	build store.StoredBuild,
	_ func(*registry.Version) string,
	at time.Time,
) (*store.StoredBuild, error) {
	key := bucket + "/" + fingerprint
	for i := range r.builds[key] {
		if r.builds[key][i].ID == build.ID {
			if r.builds[key][i].Build != build.Build ||
				r.builds[key][i].PackerRunUUID != build.PackerRunUUID ||
				!mapsEqual(r.builds[key][i].Labels, build.Labels) ||
				!bytes.Equal(r.builds[key][i].Metadata, build.Metadata) ||
				r.builds[key][i].SourceExternalIdentifier != build.SourceExternalIdentifier ||
				r.builds[key][i].ParentVersionID != build.ParentVersionID ||
				r.builds[key][i].ParentChannelID != build.ParentChannelID {
				build.UpdatedAt = at
			}
			r.builds[key][i] = build
			return &r.builds[key][i], nil
		}
	}
	return nil, registry.ErrNotFound
}

func (r *fakeRepository) DeleteBuild(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint, buildID string,
) error {
	key := bucket + "/" + fingerprint
	for i := range r.builds[key] {
		if r.builds[key][i].ID.String() == buildID {
			r.builds[key] = append(r.builds[key][:i], r.builds[key][i+1:]...)
			return nil
		}
	}
	return registry.ErrNotFound
}
func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

// The provider's destroy path for hcp_packer_bucket calls DeleteBucket directly
// on the generated client, and tolerates exactly one error: a 404 removes the
// resource from state, anything else fails the destroy (review finding 6).
func TestDeleteBucketRemovesTheAggregate(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	response := request(t, server, http.MethodDelete, testBase+"/buckets/images", nil)
	if response.Code != http.StatusNotFound ||
		!strings.Contains(response.Body.String(), `"code":5`) ||
		strings.Count(response.Body.String(), `"code"`) != 1 {
		t.Fatalf("missing DeleteBucket status/body = %d %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(),
		`"message":"Error: The bucket with identifier images does not exist."`) {
		t.Fatalf("missing DeleteBucket prose diverges from probe 03: %s", response.Body)
	}

	request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	request(t, server, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp", "template_type": "HCL2"})
	request(t, server, http.MethodPost, testBase+"/buckets/images/versions/fp/builds",
		map[string]any{"component_type": "docker"})
	request(t, server, http.MethodPost, testBase+"/buckets/images/channels",
		map[string]any{"name": "production"})

	response = request(t, server, http.MethodDelete, testBase+"/buckets/images", nil)
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "{}" {
		t.Fatalf("DeleteBucket status/body = %d %s", response.Code, response.Body)
	}
	response = request(t, server, http.MethodGet, testBase+"/buckets/images", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("deleted GetBucket status = %d, want 404", response.Code)
	}
	if len(repository.versions) != 0 || len(repository.builds) != 0 || len(repository.channels) != 0 {
		t.Fatalf("bucket deletion left contents: %#v %#v %#v",
			repository.versions, repository.builds, repository.channels)
	}
}

func TestDeleteVersionRefusalsAndSuccess(t *testing.T) {
	repository := newFakeRepository()
	version, err := registry.NewVersion(
		registry.NewID(testTime), "images", "assigned", registry.TemplateHCL2, testTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	repository.versions["images/assigned"] = version
	for i, name := range []string{"zeta", "alpha"} {
		repository.channels["images/"+name] = &store.Channel{
			ID:         registry.NewID(testTime.Add(time.Duration(i+1) * time.Second)),
			BucketName: "images", Name: name, Version: version,
		}
	}
	repository.channels["images/latest"] = &store.Channel{
		ID: registry.NewID(testTime.Add(3 * time.Second)), BucketName: "images",
		Name: "latest", Managed: true, Version: version,
	}
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	trail := &auditTrail{}
	server = audit.NewHTTPHandler(trail, server.(audit.Resolver), server,
		audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))

	response := request(t, server, http.MethodDelete,
		testBase+"/buckets/images/versions/assigned", nil)
	if response.Code != http.StatusBadRequest || response.Body.String() !=
		"{\"code\":9,\"details\":[],\"message\":\"Version is assigned by channels: alpha, zeta. Please, remove the channels assignment before deleting the version.\"}\n" {
		t.Fatalf("assigned DeleteVersion = %d %s", response.Code, response.Body)
	}
	assertAuditFields(t, trail.responses(t)[0], map[string]any{
		"operation": "version.delete", "target_type": "version", "target_id": "assigned",
		"outcome": "refused", "reason": "version_assigned",
	})

	delete(repository.channels, "images/alpha")
	delete(repository.channels, "images/zeta")
	response = request(t, server, http.MethodDelete,
		testBase+"/buckets/images/versions/assigned", nil)
	if response.Code != http.StatusOK || response.Body.String() != "{}\n" {
		t.Fatalf("successful DeleteVersion = %d %s, want 200 {}", response.Code, response.Body)
	}
	response = request(t, server, http.MethodDelete,
		testBase+"/buckets/images/versions/assigned", nil)
	if response.Code != http.StatusNotFound || response.Body.String() !=
		"{\"code\":5,\"details\":[],\"message\":\"Error: The version with identifier assigned does not exist.\"}\n" {
		t.Fatalf("missing DeleteVersion = %d %s", response.Code, response.Body)
	}
	responses := trail.responses(t)
	assertAuditFields(t, responses[len(responses)-1], map[string]any{
		"operation": "version.delete", "target_type": "version", "target_id": "assigned",
		"outcome": "refused", "reason": "version_not_found",
	})
}

func TestDeleteBuildMissingAndSuccess(t *testing.T) {
	repository := newFakeRepository()
	version, err := registry.NewVersion(
		registry.NewID(testTime), "images", "fp", registry.TemplateHCL2, testTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	repository.versions["images/fp"] = version
	buildID := registry.NewID(testTime.Add(time.Second))
	repository.builds["images/fp"] = []store.StoredBuild{{Build: registry.Build{ID: buildID}}}
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	trail := &auditTrail{}
	server = audit.NewHTTPHandler(trail, server.(audit.Resolver), server,
		audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))

	path := testBase + "/buckets/images/versions/fp/builds/" + buildID.String()
	response := request(t, server, http.MethodDelete, path, nil)
	if response.Code != http.StatusOK || response.Body.String() != "{}\n" {
		t.Fatalf("successful DeleteBuild = %d %s, want 200 {}", response.Code, response.Body)
	}
	response = request(t, server, http.MethodDelete, path, nil)
	want := "{\"code\":5,\"details\":[],\"message\":\"The build with identifier " + buildID.String() + " does not exist.\"}\n"
	if response.Code != http.StatusNotFound || response.Body.String() != want {
		t.Fatalf("missing DeleteBuild = %d %s", response.Code, response.Body)
	}
	responses := trail.responses(t)
	assertAuditFields(t, responses[len(responses)-1], map[string]any{
		"operation": "build.delete", "target_type": "build", "target_id": buildID.String(),
		"outcome": "refused", "reason": "build_not_found",
	})
}

func TestUploadSbomStoresAndNamesTheDocument(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	response := request(t, server, http.MethodPut,
		testBase+"/buckets/images/versions/fp/builds/absent/sboms",
		map[string]any{"name": "my-sbom"})
	if response.Code != http.StatusNotFound ||
		!strings.Contains(response.Body.String(), `"code":5`) ||
		strings.Count(response.Body.String(), `"code"`) != 1 {
		t.Fatalf("missing-build UploadSbom status/body = %d %s", response.Code, response.Body)
	}

	request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	request(t, server, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp", "template_type": "HCL2"})
	created := request(t, server, http.MethodPost, testBase+"/buckets/images/versions/fp/builds",
		map[string]any{"component_type": "docker", "status": "BUILD_RUNNING"})
	var build models.HashicorpCloudPacker20230101CreateBuildResponse
	decodeResponse(t, created, &build)
	sbomsPath := testBase + "/buckets/images/versions/fp/builds/" + build.Build.ID + "/sboms"

	compressed := base64.StdEncoding.EncodeToString([]byte("zstd-bytes"))
	response = request(t, server, http.MethodPut, sbomsPath, map[string]any{
		"compressed_sbom": compressed, "format": "CYCLONEDX", "name": "my-sbom",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("UploadSbom status = %d: %s", response.Code, response.Body)
	}
	var uploaded models.HashicorpCloudPacker20230101UploadSbomResponse
	decodeResponse(t, response, &uploaded)
	if uploaded.Sbom == nil || uploaded.Sbom.Name != "my-sbom" || uploaded.Sbom.ID == "" ||
		uploaded.Sbom.Format == nil || *uploaded.Sbom.Format != models.HashicorpCloudPacker20230101SbomFormatCYCLONEDX {
		t.Fatalf("UploadSbom response = %#v", uploaded.Sbom)
	}
	stored := repository.sboms["images/fp/"+build.Build.ID+"/my-sbom"]
	if string(stored.CompressedData) != "zstd-bytes" {
		t.Fatalf("stored sbom bytes = %q, want the zstd payload as sent", stored.CompressedData)
	}

	// A replace, not a conflict: a re-run build re-uploads under the same name,
	// and any error fails the whole build (dossier §5.6).
	response = request(t, server, http.MethodPut, sbomsPath, map[string]any{
		"compressed_sbom": compressed, "format": "CYCLONEDX", "name": "my-sbom",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("repeat UploadSbom status = %d: %s", response.Code, response.Body)
	}
	var repeated models.HashicorpCloudPacker20230101UploadSbomResponse
	decodeResponse(t, response, &repeated)
	if repeated.Sbom == nil || repeated.Sbom.ID != uploaded.Sbom.ID {
		t.Fatalf("repeat UploadSbom id = %#v, want %s", repeated.Sbom, uploaded.Sbom.ID)
	}

	// An omitted name defaults to the build fingerprint — the server's job, per
	// the provisioner's documentation (dossier §5.6).
	response = request(t, server, http.MethodPut, sbomsPath, map[string]any{
		"compressed_sbom": compressed, "format": "SPDX",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("unnamed UploadSbom status = %d: %s", response.Code, response.Body)
	}
	var unnamed models.HashicorpCloudPacker20230101UploadSbomResponse
	decodeResponse(t, response, &unnamed)
	if unnamed.Sbom == nil || unnamed.Sbom.Name != "fp" {
		t.Fatalf("unnamed UploadSbom = %#v, want name %q", unnamed.Sbom, "fp")
	}
}

func TestUploadSbomRefusesNonRunningBuildLikeLiveContract(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	request(t, server, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp", "template_type": "HCL2"})
	created := request(t, server, http.MethodPost, testBase+"/buckets/images/versions/fp/builds",
		map[string]any{"component_type": "docker", "status": "BUILD_DONE"})
	var build models.HashicorpCloudPacker20230101CreateBuildResponse
	decodeResponse(t, created, &build)

	response := request(t, server, http.MethodPut,
		testBase+"/buckets/images/versions/fp/builds/"+build.Build.ID+"/sboms",
		map[string]any{"compressed_sbom": base64.StdEncoding.EncodeToString([]byte("zstd"))})
	// Fixture source: Appendix A.11, captured by live conformance and S3d.
	wantBody := "{\"code\":3,\"message\":\"This build's status isn't Running, so sboms can not be uploaded\",\"details\":[]}\n"
	if response.Code != http.StatusBadRequest || response.Body.String() != wantBody {
		t.Fatalf("non-running UploadSbom = %d %q, want 400 %q", response.Code, response.Body.String(), wantBody)
	}
	if len(repository.sboms) != 0 {
		t.Fatalf("non-running UploadSbom stored documents: %#v", repository.sboms)
	}
}

func TestUploadSbomReturns503WhenObjectStorageIsUnconfigured(t *testing.T) {
	repository := newFakeRepository()
	seed := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	request(t, seed, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	request(t, seed, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp", "template_type": "HCL2"})
	created := request(t, seed, http.MethodPost, testBase+"/buckets/images/versions/fp/builds",
		map[string]any{"component_type": "docker", "status": "BUILD_RUNNING"})
	var build models.HashicorpCloudPacker20230101CreateBuildResponse
	decodeResponse(t, created, &build)

	repository.uploadSbomErr = store.ErrObjectStorageNotConfigured
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	trail := &auditTrail{}
	server = audit.NewHTTPHandler(trail, server.(audit.Resolver), server, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))
	response := request(t, server, http.MethodPut,
		testBase+"/buckets/images/versions/fp/builds/"+build.Build.ID+"/sboms",
		map[string]any{"compressed_sbom": base64.StdEncoding.EncodeToString([]byte("zstd"))})
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), `"code":14`) ||
		strings.Count(response.Body.String(), `"code"`) != 1 {
		t.Fatalf("unconfigured UploadSbom = %d %s, want 503/code 14", response.Code, response.Body)
	}
	assertAuditFields(t, trail.responses(t)[0], map[string]any{
		"outcome": "failure", "reason": "object_storage_unconfigured",
	})
}

func TestUploadSbomReturns503WhenObjectStorageIsUnavailable(t *testing.T) {
	repository := newFakeRepository()
	seed := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	request(t, seed, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	request(t, seed, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp", "template_type": "HCL2"})
	created := request(t, seed, http.MethodPost, testBase+"/buckets/images/versions/fp/builds",
		map[string]any{"component_type": "docker", "status": "BUILD_RUNNING"})
	var build models.HashicorpCloudPacker20230101CreateBuildResponse
	decodeResponse(t, created, &build)

	repository.uploadSbomErr = store.ErrObjectStorageUnavailable
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	trail := &auditTrail{}
	server = audit.NewHTTPHandler(trail, server.(audit.Resolver), server, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))
	response := request(t, server, http.MethodPut,
		testBase+"/buckets/images/versions/fp/builds/"+build.Build.ID+"/sboms",
		map[string]any{"compressed_sbom": base64.StdEncoding.EncodeToString([]byte("zstd"))})
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), `"code":14`) ||
		strings.Count(response.Body.String(), `"code"`) != 1 {
		t.Fatalf("unavailable UploadSbom = %d %s, want 503/code 14", response.Code, response.Body)
	}
	assertAuditFields(t, trail.responses(t)[0], map[string]any{
		"outcome": "failure", "reason": "object_storage_unavailable",
	})
}

// The record exists; its bytes do not answer. Deployment doc's claim that an
// absent object store yields 503, proven on the download side too (a store
// removed after upload previously fell through to 500).
func TestDownloadSbomReturns503WhenObjectStorageIsUnavailable(t *testing.T) {
	repository := newFakeRepository()
	seed := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	request(t, seed, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	request(t, seed, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp", "template_type": "HCL2"})
	created := request(t, seed, http.MethodPost, testBase+"/buckets/images/versions/fp/builds",
		map[string]any{"component_type": "docker", "status": "BUILD_RUNNING"})
	var build models.HashicorpCloudPacker20230101CreateBuildResponse
	decodeResponse(t, created, &build)
	request(t, seed, http.MethodPut,
		testBase+"/buckets/images/versions/fp/builds/"+build.Build.ID+"/sboms",
		map[string]any{"compressed_sbom": base64.StdEncoding.EncodeToString([]byte("zstd"))})

	repository.downloadSbomErr = store.ErrObjectStorageUnavailable
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	trail := &auditTrail{}
	server = audit.NewHTTPHandler(trail, server.(audit.Resolver), server, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))
	response := request(t, server, http.MethodGet,
		testBase+"/buckets/images/versions/fp/builds/"+build.Build.ID+"/sboms/fp/download", nil)
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), `"code":14`) ||
		strings.Count(response.Body.String(), `"code"`) != 1 {
		t.Fatalf("unavailable DownloadSbom = %d %s, want 503/code 14", response.Code, response.Body)
	}
	assertAuditFields(t, trail.responses(t)[0], map[string]any{
		"outcome": "failure", "reason": "object_storage_unavailable",
	})
}

func TestListSbomsAndBuildPackagesUsePublishedReadShape(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	request(t, server, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp", "template_type": "HCL2"})
	created := request(t, server, http.MethodPost, testBase+"/buckets/images/versions/fp/builds",
		map[string]any{"component_type": "docker"})
	var build models.HashicorpCloudPacker20230101CreateBuildResponse
	decodeResponse(t, created, &build)
	buildID, err := registry.ParseID(build.Build.ID)
	if err != nil {
		t.Fatal(err)
	}
	sbomA := store.Sbom{ID: registry.NewID(testTime.Add(time.Second)), BuildID: buildID, Name: "base", Format: "SPDX"}
	sbomB := store.Sbom{ID: registry.NewID(testTime.Add(2 * time.Second)), BuildID: buildID, Name: "application", Format: "CYCLONEDX"}
	repository.sboms["images/fp/"+build.Build.ID+"/base"] = sbomA
	repository.sboms["images/fp/"+build.Build.ID+"/application"] = sbomB
	repository.packages = []store.ReportedPackage{
		{Name: "openssl", Version: "3.0.11", Purl: "pkg:rpm/openssl@3.0.11", Sboms: []store.Sbom{sbomA, sbomB}},
		{Name: "zlib", Version: "1.3", Purl: "pkg:apk/zlib@1.3", Sboms: []store.Sbom{sbomA}},
	}
	base := testBase + "/buckets/images/versions/fp/builds/" + build.Build.ID

	response := request(t, server, http.MethodGet, base+"/sboms", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("ListSboms status/body = %d %s", response.Code, response.Body)
	}
	var listed models.HashicorpCloudPacker20230101ListSbomsResponse
	decodeResponse(t, response, &listed)
	if len(listed.Sboms) != 2 || listed.Sboms[0].Name != "application" || listed.Sboms[1].Name != "base" {
		t.Fatalf("ListSboms = %#v", listed.Sboms)
	}

	response = request(t, server, http.MethodGet, base+"/packages?pagination.page_size=1", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("ListBuildPackages status/body = %d %s", response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), "vuln_details") {
		t.Fatalf("unscanned packages claimed vulnerability detail: %s", response.Body)
	}
	var first listBuildPackagesResponse
	decodeResponse(t, response, &first)
	if len(first.Packages) != 1 || first.Packages[0].Name != "openssl" ||
		first.Packages[0].Purl != "pkg:rpm/openssl@3.0.11" || len(first.Packages[0].Sboms) != 2 ||
		first.Pagination == nil || first.Pagination.NextPageToken == "" {
		t.Fatalf("first package page = %#v", first)
	}
	response = request(t, server, http.MethodGet,
		base+"/packages?pagination.page_size=1&pagination.next_page_token="+first.Pagination.NextPageToken, nil)
	var second listBuildPackagesResponse
	decodeResponse(t, response, &second)
	if response.Code != http.StatusOK || len(second.Packages) != 1 || second.Packages[0].Name != "zlib" ||
		second.Pagination == nil || second.Pagination.PreviousPageToken == "" {
		t.Fatalf("second package page = %#v; status/body %d %s", second, response.Code, response.Body)
	}

	response = request(t, server, http.MethodGet,
		base+"/packages?package_name_starts_with=open&package_version=3.0.11", nil)
	var filtered listBuildPackagesResponse
	decodeResponse(t, response, &filtered)
	if response.Code != http.StatusOK || len(filtered.Packages) != 1 || filtered.Packages[0].Name != "openssl" {
		t.Fatalf("filtered packages = %#v; status/body %d %s", filtered, response.Code, response.Body)
	}
}

func TestUnconfiguredScannerOmitsVulnerabilityDataAndHeaders(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	request(t, server, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp", "template_type": "HCL2"})
	created := request(t, server, http.MethodPost, testBase+"/buckets/images/versions/fp/builds",
		map[string]any{"component_type": "docker"})
	var build models.HashicorpCloudPacker20230101CreateBuildResponse
	decodeResponse(t, created, &build)
	repository.packages = []store.ReportedPackage{{
		Name: "openssl", Version: "3.0.0", Purl: "pkg:apk/alpine/openssl@3.0.0",
	}}

	packagesPath := testBase + "/buckets/images/versions/fp/builds/" + build.Build.ID + "/packages"
	response := request(t, server, http.MethodGet, packagesPath, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("unconfigured ListBuildPackages = %d %s", response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), "vuln_details") {
		t.Fatalf("unconfigured ListBuildPackages asserted a scan result: %s", response.Body)
	}
	for _, suffix := range []string{
		"Adapter", "Engine", "Database-Revision", "Observed-At",
		"Submitted", "Invalid", "Unversioned", "Unsupported",
	} {
		name := http.CanonicalHeaderKey(scanHeaderPrefix + suffix)
		if values, present := response.Header()[name]; present {
			t.Errorf("unconfigured %s header = %q, want absent", name, values)
		}
	}

	for _, path := range []string{
		testBase + "/buckets/images/packages/vulnerability-summary",
		testBase + "/buckets/images/packages/with-vulnerabilities",
		testBase + "/buckets/images/vulnerabilities",
	} {
		response := request(t, server, http.MethodGet, path, nil)
		if response.Code != http.StatusNotFound ||
			!strings.Contains(response.Body.String(), `"code":5`) {
			t.Errorf("unconfigured GET %s = %d %s, want 404/code 5", path, response.Code, response.Body)
		}
	}
}

func TestFailedLatestAttemptDoesNotBlankCurrentBuildFindings(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	request(t, server, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp", "template_type": "HCL2"})
	created := request(t, server, http.MethodPost, testBase+"/buckets/images/versions/fp/builds",
		map[string]any{"component_type": "docker"})
	var build models.HashicorpCloudPacker20230101CreateBuildResponse
	decodeResponse(t, created, &build)
	repository.packages = []store.ReportedPackage{{
		Name: "openssl", Version: "3.0.0", Purl: "pkg:apk/alpine/openssl@3.0.0",
	}}
	repository.scanStates[build.Build.ID] = &store.BuildScanState{
		BuildID: build.Build.ID, CurrentFindingsRunID: "run-current", LatestAttemptRunID: "run-failed",
	}
	repository.scanRuns["run-current"] = &store.ScanRun{
		ID: "run-current", BuildID: build.Build.ID, Status: store.ScanRunSucceeded,
		Adapter: "osv", Engine: "stub", DatabaseRevision: "db-1", ObservedAt: testTime,
		Coverage: scan.Coverage{Submitted: 1},
	}
	repository.scanRuns["run-failed"] = &store.ScanRun{
		ID: "run-failed", BuildID: build.Build.ID, Status: store.ScanRunFailed,
		Adapter: "failed-adapter", ObservedAt: testTime.Add(time.Hour),
	}
	repository.scanFindings["run-current"] = []store.StoredFinding{{
		Finding: scan.Finding{
			Package: scan.Package{Name: "openssl", Version: "3.0.0", Purl: "pkg:apk/alpine/openssl@3.0.0"},
			ID:      "CVE-2026-0001", Summary: "current finding", Severity: scan.SeverityHigh,
			Severities: []scan.SeverityValue{{Type: "label", Value: "HIGH"}},
		},
		FirstSeenAt: testTime.Add(-24 * time.Hour),
	}}

	path := testBase + "/buckets/images/versions/fp/builds/" + build.Build.ID + "/packages"
	response := request(t, server, http.MethodGet, path, nil)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"identifier":"CVE-2026-0001"`) {
		t.Fatalf("ListBuildPackages after failed attempt = %d %s", response.Code, response.Body)
	}
	if got := response.Header().Get(scanHeaderPrefix + "Adapter"); got != "osv" {
		t.Fatalf("scan attribution adapter = %q, want current run osv", got)
	}
}

func TestGetSbomReturnsDufflebagProxyAndDownloadServesTheDocument(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	request(t, server, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp", "template_type": "HCL2"})
	created := request(t, server, http.MethodPost, testBase+"/buckets/images/versions/fp/builds",
		map[string]any{"component_type": "docker"})
	var build models.HashicorpCloudPacker20230101CreateBuildResponse
	decodeResponse(t, created, &build)
	base := testBase + "/buckets/images/versions/fp/builds/" + build.Build.ID + "/sboms"
	payload := []byte("opaque zstd bytes")
	repository.sboms["images/fp/"+build.Build.ID+"/manifest"] = store.Sbom{
		ID: registry.NewID(testTime), Name: "manifest", CompressedData: payload,
	}

	missing := request(t, server, http.MethodGet, base+"/absent", nil)
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), `"code":5`) {
		t.Fatalf("missing GetSbom status/body = %d %s", missing.Code, missing.Body)
	}
	response := request(t, server, http.MethodGet, base+"/manifest", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GetSbom status/body = %d %s", response.Code, response.Body)
	}
	var got models.HashicorpCloudPacker20230101GetSbomResponse
	decodeResponse(t, response, &got)
	if got.DownloadURL != "http://example.com"+base+"/manifest/download" {
		t.Fatalf("download_url = %q", got.DownloadURL)
	}

	// The observed wire (live HCP, 2026-08-08): the download is the JSON
	// document as "<name>.json". The store serves the document; the fake
	// stores plain bytes, so the body passes through unchanged here and the
	// decompression itself is pinned by the real-binary e2e lane.
	download := request(t, server, http.MethodGet, base+"/manifest/download", nil)
	if download.Code != http.StatusOK || !bytes.Equal(download.Body.Bytes(), payload) ||
		download.Header().Get("Content-Type") != "application/json" ||
		download.Header().Get("Content-Disposition") != `attachment; filename=manifest.json` {
		t.Fatalf("download status/headers/body = %d %#v %q",
			download.Code, download.Header(), download.Body.Bytes())
	}
}

func TestListBuildPackagesRefusesUnparseableWithoutCallingItEmpty(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	request(t, server, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp", "template_type": "HCL2"})
	created := request(t, server, http.MethodPost, testBase+"/buckets/images/versions/fp/builds",
		map[string]any{"component_type": "docker"})
	var build models.HashicorpCloudPacker20230101CreateBuildResponse
	decodeResponse(t, created, &build)
	path := testBase + "/buckets/images/versions/fp/builds/" + build.Build.ID + "/packages"

	repository.unparseable = []string{"broken-client-report"}
	response := request(t, server, http.MethodGet, path, nil)
	if response.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(response.Body.String(), `"code":9`) ||
		!strings.Contains(response.Body.String(), `broken-client-report`) {
		t.Fatalf("unparseable packages status/body = %d %s", response.Code, response.Body)
	}

	repository.unparseable = nil
	response = request(t, server, http.MethodGet, path, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"packages":[]`) {
		t.Fatalf("genuinely empty packages status/body = %d %s", response.Code, response.Body)
	}
}

func TestSBOMReadsRefuseMissingBuildAndInvalidPagination(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	missingBase := testBase + "/buckets/images/versions/fp/builds/absent"
	for _, path := range []string{missingBase + "/sboms", missingBase + "/packages"} {
		response := request(t, server, http.MethodGet, path, nil)
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":5`) {
			t.Fatalf("missing build %s status/body = %d %s", path, response.Code, response.Body)
		}
	}

	request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	request(t, server, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp", "template_type": "HCL2"})
	created := request(t, server, http.MethodPost, testBase+"/buckets/images/versions/fp/builds",
		map[string]any{"component_type": "docker"})
	var build models.HashicorpCloudPacker20230101CreateBuildResponse
	decodeResponse(t, created, &build)
	response := request(t, server, http.MethodGet,
		testBase+"/buckets/images/versions/fp/builds/"+build.Build.ID+
			"/packages?pagination.next_page_token=MA&pagination.previous_page_token=MA", nil)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":3`) ||
		!strings.Contains(response.Body.String(), "cannot both be set") {
		t.Fatalf("invalid package pagination status/body = %d %s", response.Code, response.Body)
	}
}

// The provider's destroy path, and version_fingerprint = "none", both send an
// UpdateChannel whose mask names versionFingerprint with an EMPTY fingerprint.
// Real HCP treats that as "clear the assignment"; answering 400 broke every
// terraform destroy of an hcp_packer_channel_assignment (duf-8em).
func TestChannelUnassignment(t *testing.T) {
	repository := newFakeRepository()
	repository.buckets["images"] = &store.Bucket{
		ID: registry.NewID(testTime), Name: "images", Labels: map[string]string{},
		CreatedAt: testTime, UpdatedAt: testTime,
	}
	complete, err := registry.RestoreVersion(registry.Version{
		ID:           registry.NewID(testTime.Add(time.Second)),
		BucketName:   "images",
		Fingerprint:  "complete",
		TemplateType: registry.TemplateHCL2,
		CreatedAt:    testTime,
		UpdatedAt:    testTime,
	}, true, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	repository.versions["images/complete"] = complete
	now := testTime.Add(2 * time.Second)
	server := newHandler(repository, fakePrincipals{role: identity.RoleMaintainer}, testAuthenticator{}, testLogger(), func() time.Time {
		now = now.Add(time.Second)
		return now
	})

	channelsPath := testBase + "/buckets/images/channels"
	response := request(t, server, http.MethodPost, channelsPath, map[string]any{
		"name": "production", "version_fingerprint": "complete",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("CreateChannel status = %d: %s", response.Code, response.Body)
	}

	// The destroy shape: mask names the field, fingerprint absent (the SDK
	// omits empty strings when marshalling).
	response = request(t, server, http.MethodPatch, channelsPath+"/production", map[string]any{
		"update_mask": "versionFingerprint",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("unassign status = %d: %s", response.Code, response.Body)
	}
	var updated models.HashicorpCloudPacker20230101UpdateChannelResponse
	decodeResponse(t, response, &updated)
	if updated.Channel == nil || updated.Channel.Version != nil {
		t.Fatalf("unassigned channel = %#v", updated.Channel)
	}
	response = request(t, server, http.MethodGet, channelsPath+"/production", nil)
	var fetched models.HashicorpCloudPacker20230101GetChannelResponse
	decodeResponse(t, response, &fetched)
	if fetched.Channel == nil || fetched.Channel.Version != nil {
		t.Fatalf("unassignment did not persist: %#v", fetched.Channel)
	}

	// The same with the empty string sent explicitly.
	response = request(t, server, http.MethodPatch, channelsPath+"/production", map[string]any{
		"update_mask": "versionFingerprint", "version_fingerprint": "complete",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("reassign status = %d: %s", response.Code, response.Body)
	}
	response = request(t, server, http.MethodPatch, channelsPath+"/production", map[string]any{
		"update_mask": "versionFingerprint", "version_fingerprint": "",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("explicit-empty unassign status = %d: %s", response.Code, response.Body)
	}
	updated = models.HashicorpCloudPacker20230101UpdateChannelResponse{}
	decodeResponse(t, response, &updated)
	if updated.Channel == nil || updated.Channel.Version != nil {
		t.Fatalf("explicit-empty unassigned channel = %#v", updated.Channel)
	}

	// An empty fingerprint WITHOUT the versionFingerprint mask still means "do
	// not touch": only masked-and-empty clears.
	response = request(t, server, http.MethodPatch, channelsPath+"/production", map[string]any{
		"update_mask": "versionFingerprint", "version_fingerprint": "complete",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("reassign status = %d: %s", response.Code, response.Body)
	}
	response = request(t, server, http.MethodPatch, channelsPath+"/production", map[string]any{
		"update_mask": "restricted", "restricted": true,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("restrict status = %d: %s", response.Code, response.Body)
	}
	updated = models.HashicorpCloudPacker20230101UpdateChannelResponse{}
	decodeResponse(t, response, &updated)
	if updated.Channel == nil || updated.Channel.Version == nil ||
		updated.Channel.Version.Fingerprint != "complete" {
		t.Fatalf("unmasked update cleared the assignment: %#v", updated.Channel)
	}
}

// Every bucket carries a managed "latest" channel from creation, and clients
// cannot mutate it. Shapes are the live captures: auto-create with
// managed/restricted/author (Appendix A probes 04-06), update refused 400
// code 9, delete refused 400 code 3 — the 9-vs-3 asymmetry is the capture's,
// verbatim (probes 19 and 17).
func TestManagedLatestChannel(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	response := request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	if response.Code != http.StatusOK {
		t.Fatalf("CreateBucket status = %d: %s", response.Code, response.Body)
	}

	response = request(t, server, http.MethodGet, testBase+"/buckets/images/channels", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("ListChannels status = %d: %s", response.Code, response.Body)
	}
	var listed models.HashicorpCloudPacker20230101ListChannelsResponse
	decodeResponse(t, response, &listed)
	if len(listed.Channels) != 1 {
		t.Fatalf("fresh bucket channels = %#v, want exactly latest", listed.Channels)
	}
	latest := listed.Channels[0]
	// author_id is "Dufflebag", not the captured "HCP Packer": the deliberate
	// trademark deviation (dossier §7 note). Flags stay exactly as captured.
	if latest.Name != "latest" || !latest.Managed || !latest.Restricted ||
		latest.AuthorID != "Dufflebag" || latest.Version != nil {
		t.Fatalf("managed latest = %#v, want managed restricted Dufflebag-authored unassigned", latest)
	}

	response = request(t, server, http.MethodGet, testBase+"/buckets/images/channels/latest", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GetChannel latest status = %d: %s", response.Code, response.Body)
	}
	var fetched models.HashicorpCloudPacker20230101GetChannelResponse
	decodeResponse(t, response, &fetched)
	if fetched.Channel == nil || !fetched.Channel.Managed {
		t.Fatalf("GetChannel latest = %#v", fetched.Channel)
	}

	// Probe 19: a well-formed update — valid mask, existing complete version —
	// is still refused because the channel is managed.
	complete, err := registry.RestoreVersion(registry.Version{
		ID: registry.NewID(testTime.Add(time.Second)), BucketName: "images",
		Fingerprint: "complete", TemplateType: registry.TemplateHCL2,
		CreatedAt: testTime, UpdatedAt: testTime,
	}, true, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	repository.versions["images/complete"] = complete
	response = request(t, server, http.MethodPatch, testBase+"/buckets/images/channels/latest", map[string]any{
		"update_mask": "versionFingerprint", "version_fingerprint": "complete",
	})
	body := response.Body.String()
	if response.Code != http.StatusBadRequest || !strings.Contains(body, `"code":9`) {
		t.Fatalf("UpdateChannel latest status/body = %d %s, want 400 code 9", response.Code, body)
	}
	if !strings.Contains(body,
		`Can't update channel assignment on channel \"latest\". This channel is managed by Dufflebag`) {
		t.Fatalf("UpdateChannel latest message diverges from the probe-19 structure: %s", body)
	}
	if strings.Count(body, `"code"`) != 1 {
		t.Fatalf("update refusal has another code field: %s", body)
	}

	// Probe 17: deletion is refused with code 3, not 9, and its own message.
	response = request(t, server, http.MethodDelete, testBase+"/buckets/images/channels/latest", nil)
	body = response.Body.String()
	if response.Code != http.StatusBadRequest || !strings.Contains(body, `"code":3`) {
		t.Fatalf("DeleteChannel latest status/body = %d %s, want 400 code 3", response.Code, body)
	}
	if !strings.Contains(body, "Can't delete managed channel latest, it's controlled by Dufflebag") {
		t.Fatalf("DeleteChannel latest message diverges from the probe-17 structure: %s", body)
	}
	if strings.Count(body, `"code"`) != 1 {
		t.Fatalf("delete refusal has another code field: %s", body)
	}
	if _, ok := repository.channels["images/latest"]; !ok {
		t.Fatal("refused delete still removed the managed channel")
	}
}

// Probe 40 settles the distinct /channels/assign behaviour. The fake does not
// enforce managed-channel immutability, deliberately: this proves the handler
// guard fires before the repository is called. The Postgres test proves the
// independent repository guard.
func TestAssignChannelVersionRefusesManagedTargetBeforeRepository(t *testing.T) {
	repository := newFakeRepository()
	complete, err := registry.RestoreVersion(registry.Version{
		ID: registry.NewID(testTime), BucketName: "images", Fingerprint: "complete",
		TemplateType: registry.TemplateHCL2, CreatedAt: testTime, UpdatedAt: testTime,
	}, true, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	repository.buckets["images"] = &store.Bucket{
		ID: registry.NewID(testTime), Name: "images", CreatedAt: testTime, UpdatedAt: testTime,
	}
	repository.versions["images/complete"] = complete
	repository.channels["images/production"] = &store.Channel{
		ID: registry.NewID(testTime), BucketName: "images", Name: "production",
		Version: complete, CreatedAt: testTime, UpdatedAt: testTime,
	}
	repository.channels["images/latest"] = &store.Channel{
		ID: registry.NewID(testTime), BucketName: "images", Name: "latest",
		Managed: true, Restricted: true, CreatedAt: testTime, UpdatedAt: testTime,
	}
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	response := request(t, server, http.MethodPost,
		testBase+"/buckets/images/channels/assign",
		map[string]any{"source_channel": "production", "target_channel": "latest"})
	body := response.Body.String()
	if response.Code != http.StatusBadRequest || !strings.Contains(body, `"code":9`) ||
		!strings.Contains(body, `"message":"Cannot assign to managed channel 'latest'"`) {
		t.Fatalf("managed assign diverges from probe 40: %d %s", response.Code, body)
	}
	if repository.assignCalls != 0 {
		t.Fatalf("managed assign reached repository %d times", repository.assignCalls)
	}
}

// UpdateChannel requires update_mask: the one place live HCP is strict where
// the spec is silent, and the only observed error with non-empty details.
// Message and google.rpc.BadRequest detail are Appendix A probe 15, verbatim.
func TestUpdateChannelRequiresUpdateMask(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	response := request(t, server, http.MethodPost, testBase+"/buckets/images/channels",
		map[string]any{"name": "production"})
	if response.Code != http.StatusOK {
		t.Fatalf("CreateChannel status = %d: %s", response.Code, response.Body)
	}

	response = request(t, server, http.MethodPatch, testBase+"/buckets/images/channels/production",
		map[string]any{"version_fingerprint": "anything"})
	body := response.Body.String()
	if response.Code != http.StatusBadRequest || !strings.Contains(body, `"code":3`) {
		t.Fatalf("maskless UpdateChannel status/body = %d %s, want 400 code 3", response.Code, body)
	}
	if !strings.Contains(body, "body: (update_mask: field mask: must be set.).") {
		t.Fatalf("maskless UpdateChannel message diverges from probe 15: %s", body)
	}
	for _, fragment := range []string{
		`"@type":"type.googleapis.com/google.rpc.BadRequest"`,
		`"field_violations":[{"field":"body.update_mask","description":"field mask: must be set","reason":"","localized_message":null}]`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("maskless UpdateChannel details lack %s: %s", fragment, body)
		}
	}
	// Packer's regex takes the FIRST code-like field; the detail must not add one.
	if strings.Count(body, `"code"`) != 1 {
		t.Fatalf("maskless refusal has another code field: %s", body)
	}
	assertConforms(t, response, "google.rpc.Status")
}

// Unknown request body fields are silently ignored, as live HCP ignores them:
// UpdateBucket with a bogus field applies the known fields (Appendix A probe
// 20), and an UpdateBuild wrapped whole in an unknown envelope is a 200 no-op
// (probe 11). DisallowUnknownFields would turn both into 400s live never sends
// (dossier §4a; duf-7cy).
func TestUnknownRequestFieldsAreIgnored(t *testing.T) {
	repository := newFakeRepository()
	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })

	request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{"name": "images"})
	response := request(t, server, http.MethodPatch, testBase+"/buckets/images", map[string]any{
		"description":           "probe description updated",
		"dufflebag_bogus_field": "ignored?",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("UpdateBucket with unknown field status = %d: %s", response.Code, response.Body)
	}
	var updated models.HashicorpCloudPacker20230101UpdateBucketResponse
	decodeResponse(t, response, &updated)
	if updated.Bucket == nil || updated.Bucket.Description != "probe description updated" {
		t.Fatalf("known fields not applied alongside unknown one: %#v", updated.Bucket)
	}

	request(t, server, http.MethodPost, testBase+"/buckets/images/versions",
		map[string]any{"fingerprint": "fp", "template_type": "HCL2"})
	created := request(t, server, http.MethodPost, testBase+"/buckets/images/versions/fp/builds",
		map[string]any{"component_type": "docker", "status": "BUILD_RUNNING"})
	var build models.HashicorpCloudPacker20230101CreateBuildResponse
	decodeResponse(t, created, &build)

	// Probe 11: the real fields nested under an unknown wrapper reach no known
	// field, so the request succeeds and changes nothing.
	response = request(t, server, http.MethodPatch,
		testBase+"/buckets/images/versions/fp/builds/"+build.Build.ID,
		map[string]any{"updates": map[string]any{"status": "BUILD_DONE"}})
	if response.Code != http.StatusOK {
		t.Fatalf("enveloped UpdateBuild status = %d: %s", response.Code, response.Body)
	}
	var noop models.HashicorpCloudPacker20230101UpdateBuildResponse
	decodeResponse(t, response, &noop)
	if noop.Build == nil || noop.Build.Status == nil ||
		*noop.Build.Status != models.HashicorpCloudPacker20230101BuildStatusBUILDRUNNING {
		t.Fatalf("enveloped UpdateBuild was not a no-op: %#v", noop.Build)
	}
}

func TestOversizedRequestBodyMatchesLiveHCPRefusal(t *testing.T) {
	server := newHandlerWithMaxBody(
		newFakeRepository(), testPrincipals(), testAuthenticator{}, testLogger(),
		func() time.Time { return testTime }, 64,
	)
	trail := &auditTrail{}
	server = audit.NewHTTPHandler(trail, server.(audit.Resolver), server, audit.StaticHMACKey("test-v1", []byte("test-audit-hmac-key")))
	response := request(t, server, http.MethodPut, testBase+"/buckets", map[string]any{
		"name":        "images",
		"description": strings.Repeat("x", 128),
	})
	if response.Code != http.StatusGatewayTimeout || response.Body.String() != "Gateway Timeout" {
		t.Fatalf("oversized request status/body = %d %q, want 504 %q",
			response.Code, response.Body.String(), "Gateway Timeout")
	}
	responses := trail.responses(t)
	assertAuditFields(t, responses[0], map[string]any{
		"operation": "bucket.create", "target_type": "bucket",
		"outcome": "refused", "reason": "body_too_large",
	}, "target_id")
}
