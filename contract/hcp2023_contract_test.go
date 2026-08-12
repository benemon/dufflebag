package contract_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/compat/hcp2023"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/scan"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	httptransport "github.com/go-openapi/runtime/client"
	"github.com/go-openapi/strfmt"
	"github.com/google/uuid"
	cloudclient "github.com/hashicorp/hcp-sdk-go/clients/cloud-packer-service/stable/2023-01-01/client"
	"github.com/hashicorp/hcp-sdk-go/clients/cloud-packer-service/stable/2023-01-01/client/packer_service"
	sdkmodels "github.com/hashicorp/hcp-sdk-go/clients/cloud-packer-service/stable/2023-01-01/models"
)

const (
	contractOrg     = "3f2b1c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	contractProject = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	contractToken   = "contract-token"
)

// contractAuth attaches the bearer token the way the SDK does in anger, so the
// generated client exercises the real authenticated path.
var contractAuth = httptransport.BearerToken(contractToken)

// contractPrincipals resolves the caller's authority. Publisher, because the
// contract test drives the whole generated client including channel assignment.
type contractPrincipals struct{}

func (contractPrincipals) GetPrincipalByID(_ context.Context, id string) (*identity.Principal, error) {
	return identity.RestorePrincipal(
		id, "contract", "client",
		identity.Scope{
			OrganizationID: uuid.MustParse(contractOrg),
			ProjectID:      uuid.MustParse(contractProject),
		},
		identity.RolePublisher, time.Now(), contractSecrets(),
	)
}

type contractAuthenticator struct{}

func (contractAuthenticator) Verify(token string) (identity.Verified, error) {
	if token != contractToken {
		return identity.Verified{}, identity.ErrInvalid
	}
	return identity.Verified{
		PrincipalID: "p-contract",
		Scope: identity.Scope{
			OrganizationID: uuid.MustParse(contractOrg),
			ProjectID:      uuid.MustParse(contractProject),
		},
		SecretID: contractSecretID,
	}, nil
}

func TestGeneratedClientDrivesRunningServer(t *testing.T) {
	repository := newContractRepository()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(hcp2023.NewHandlerWithRepository(repository, contractPrincipals{}, contractAuthenticator{}, logger))
	defer server.Close()

	client := cloudclient.NewHTTPClientWithConfig(
		nil,
		cloudclient.DefaultTransportConfig().
			WithHost(strings.TrimPrefix(server.URL, "http://")).
			WithSchemes([]string{"http"}),
	).PackerService

	_, err := client.PackerServiceGetBucket(
		packer_service.NewPackerServiceGetBucketParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images"),
		contractAuth,
	)
	if err == nil || !strings.Contains(err.Error(), `"code":5`) {
		t.Fatalf("generated GetBucket missing error = %v, want code 5", err)
	}

	// GetRegistry sits on the CLI's org-unset/project-set resolution path, where
	// a nil registry is fatal upstream (ADR-0003). No lane's client reaches it in
	// anger, so the generated client is its wire oracle (duf-egk2.3).
	gotRegistry, err := client.PackerServiceGetRegistry(
		packer_service.NewPackerServiceGetRegistryParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject),
		contractAuth,
	)
	if err != nil {
		t.Fatalf("generated GetRegistry: %v", err)
	}
	registryPayload := gotRegistry.Payload.Registry
	if registryPayload == nil || registryPayload.ID != contractProject {
		t.Fatalf("generated GetRegistry payload = %#v, want registry ID %q",
			registryPayload, contractProject)
	}
	if registryPayload.Location == nil ||
		registryPayload.Location.OrganizationID != contractOrg ||
		registryPayload.Location.ProjectID != contractProject {
		t.Fatalf("generated GetRegistry location = %#v", registryPayload.Location)
	}
	if registryPayload.Config == nil || !registryPayload.Config.Activated {
		t.Fatalf("generated GetRegistry config = %#v, want activated", registryPayload.Config)
	}

	createdBucket, err := client.PackerServiceCreateBucket(
		packer_service.NewPackerServiceCreateBucketParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBody(&sdkmodels.HashicorpCloudPacker20230101CreateBucketBody{
				Name: "images", Description: "base images",
				Labels: map[string]string{"team": "platform"},
			}),
		contractAuth,
	)
	if err != nil {
		t.Fatalf("generated CreateBucket: %v", err)
	}
	if createdBucket.Payload.Bucket.Name != "images" {
		t.Fatalf("generated CreateBucket payload = %#v", createdBucket.Payload.Bucket)
	}
	// The provider's bucket Read stores resource_name in Terraform state, so an
	// empty value is perpetual state drift rather than a hard failure.
	wantResourceName := "packer/project/" + contractProject + "/bucket/images"
	if createdBucket.Payload.Bucket.ResourceName != wantResourceName {
		t.Fatalf("generated CreateBucket resource_name = %q, want %q",
			createdBucket.Payload.Bucket.ResourceName, wantResourceName)
	}

	// A fresh bucket lists no versions, and the answer is an empty list rather
	// than null — a client ranging over it must not need a nil check.
	emptyVersions, err := client.PackerServiceListVersions(
		packer_service.NewPackerServiceListVersionsParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images"),
		contractAuth,
	)
	if err != nil || emptyVersions.Payload.Versions == nil || len(emptyVersions.Payload.Versions) != 0 {
		t.Fatalf("generated ListVersions on fresh bucket = %#v, %v (want empty, non-nil)",
			emptyVersions, err)
	}

	// UpdateBucket fires from Packer's upsert only when stored metadata
	// mismatches the template's — a path no e2e lane varies, so the wire shape
	// is proven here (duf-egk2.3). The update must round-trip through a
	// subsequent GET, not merely echo.
	if _, err := client.PackerServiceUpdateBucket(
		packer_service.NewPackerServiceUpdateBucketParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("absent").
			WithBody(&sdkmodels.HashicorpCloudPacker20230101UpdateBucketBody{
				Description: "no such bucket",
			}),
		contractAuth,
	); err == nil || !strings.Contains(err.Error(), `"code":5`) {
		t.Fatalf("generated UpdateBucket missing error = %v, want code 5", err)
	}
	updatedBucket, err := client.PackerServiceUpdateBucket(
		packer_service.NewPackerServiceUpdateBucketParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithBody(&sdkmodels.HashicorpCloudPacker20230101UpdateBucketBody{
				Description: "revised base images",
				Labels:      map[string]string{"team": "platform", "tier": "base"},
			}),
		contractAuth,
	)
	if err != nil {
		t.Fatalf("generated UpdateBucket: %v", err)
	}
	if updatedBucket.Payload.Bucket.Description != "revised base images" ||
		updatedBucket.Payload.Bucket.Labels["tier"] != "base" {
		t.Fatalf("generated UpdateBucket payload = %#v", updatedBucket.Payload.Bucket)
	}
	rereadBucket, err := client.PackerServiceGetBucket(
		packer_service.NewPackerServiceGetBucketParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images"),
		contractAuth,
	)
	if err != nil || rereadBucket.Payload.Bucket.Description != "revised base images" ||
		rereadBucket.Payload.Bucket.Labels["tier"] != "base" {
		t.Fatalf("generated GetBucket after update = %#v, %v", rereadBucket, err)
	}

	// A fresh bucket is never channel-less: CreateBucket brought the managed
	// "latest" into existence in the same instant, visible to the LIST the
	// provider filters client-side and to GetChannel directly (Appendix A
	// probes 04-06). managed and restricted are the flags the provider's
	// channel resource branches on (resource_packer_channel.go); author_id is
	// "Dufflebag" — a deliberate, verified-inert deviation from the captured
	// "HCP Packer" (dossier §7 deviation note).
	freshChannels, err := client.PackerServiceListChannels(
		packer_service.NewPackerServiceListChannelsParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images"),
		contractAuth,
	)
	if err != nil || len(freshChannels.Payload.Channels) != 1 {
		t.Fatalf("generated ListChannels on fresh bucket = %#v, %v; want exactly latest",
			freshChannels, err)
	}
	managedLatest := freshChannels.Payload.Channels[0]
	if managedLatest.Name != "latest" || !managedLatest.Managed || !managedLatest.Restricted ||
		managedLatest.AuthorID != "Dufflebag" || managedLatest.Version != nil {
		t.Fatalf("generated managed latest = %#v, want managed restricted Dufflebag-authored unassigned",
			managedLatest)
	}
	if _, err := client.PackerServiceGetChannel(
		packer_service.NewPackerServiceGetChannelParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithChannelName("latest"),
		contractAuth,
	); err != nil {
		t.Fatalf("generated GetChannel latest on fresh bucket: %v", err)
	}

	// terraform-provider-hcp's resource_packer_channel Create adopts a managed
	// channel only when the generated client exposes AlreadyExists/code 6. Probe
	// 38 settles both the HTTP 409 carrier and the code. The follow-up GET and
	// LIST are the provider's adoption reads, not assumptions about server state.
	_, err = client.PackerServiceCreateChannel(
		packer_service.NewPackerServiceCreateChannelParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithBody(&sdkmodels.HashicorpCloudPacker20230101CreateChannelBody{Name: "latest"}),
		contractAuth,
	)
	var duplicateChannel *packer_service.PackerServiceCreateChannelDefault
	if !errors.As(err, &duplicateChannel) || !duplicateChannel.IsCode(http.StatusConflict) ||
		duplicateChannel.Payload.Code != 6 {
		t.Fatalf("generated duplicate CreateChannel latest = %v, want typed 409/code 6", err)
	}
	adoptedLatest, err := client.PackerServiceGetChannel(
		packer_service.NewPackerServiceGetChannelParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithChannelName("latest"),
		contractAuth,
	)
	if err != nil || adoptedLatest.Payload.Channel == nil || !adoptedLatest.Payload.Channel.Managed {
		t.Fatalf("generated adoption GetChannel latest = %#v, %v", adoptedLatest, err)
	}
	adoptionList, err := client.PackerServiceListChannels(
		packer_service.NewPackerServiceListChannelsParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images"),
		contractAuth,
	)
	if err != nil || len(adoptionList.Payload.Channels) != 1 ||
		adoptionList.Payload.Channels[0].Name != "latest" {
		t.Fatalf("generated adoption ListChannels = %#v, %v", adoptionList, err)
	}

	_, err = client.PackerServiceGetVersion(
		packer_service.NewPackerServiceGetVersionParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithFingerprint("fingerprint"),
		contractAuth,
	)
	if err == nil || !strings.Contains(err.Error(), `"code":10`) {
		t.Fatalf("generated GetVersion missing error = %v, want code 10", err)
	}

	templateType := sdkmodels.HashicorpCloudPacker20230101TemplateTypeHCL2
	createVersionParams := func() *packer_service.PackerServiceCreateVersionParams {
		return packer_service.NewPackerServiceCreateVersionParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithBody(&sdkmodels.HashicorpCloudPacker20230101CreateVersionBody{
				Fingerprint: "fingerprint", TemplateType: &templateType,
			})
	}
	createdVersion, err := client.PackerServiceCreateVersion(createVersionParams(), contractAuth)
	if err != nil {
		t.Fatalf("generated CreateVersion: %v", err)
	}
	repeatedVersion, err := client.PackerServiceCreateVersion(createVersionParams(), contractAuth)
	if err != nil {
		t.Fatalf("generated idempotent CreateVersion: %v", err)
	}
	if createdVersion.Payload.Version.Name != "v0" ||
		createdVersion.Payload.Version.AuthorID != "p-contract" ||
		repeatedVersion.Payload.Version.ID != createdVersion.Payload.Version.ID {
		t.Fatalf(
			"generated CreateVersion payloads = %#v %#v",
			createdVersion.Payload.Version,
			repeatedVersion.Payload.Version,
		)
	}

	buildStatus := sdkmodels.HashicorpCloudPacker20230101BuildStatusBUILDUNSET
	createBuildParams := func() *packer_service.PackerServiceCreateBuildParams {
		return packer_service.NewPackerServiceCreateBuildParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithFingerprint("fingerprint").
			WithBody(&sdkmodels.HashicorpCloudPacker20230101CreateBuildBody{
				ComponentType: "docker",
				PackerRunUUID: "run-1",
				Status:        &buildStatus,
				Artifacts:     []*sdkmodels.HashicorpCloudPacker20230101ArtifactCreateBody{},
			})
	}
	createdBuild, err := client.PackerServiceCreateBuild(createBuildParams(), contractAuth)
	if err != nil {
		t.Fatalf("generated CreateBuild: %v", err)
	}
	repeatedBuild, err := client.PackerServiceCreateBuild(createBuildParams(), contractAuth)
	if err != nil {
		t.Fatalf("generated idempotent CreateBuild: %v", err)
	}
	if repeatedBuild.Payload.Build.ID != createdBuild.Payload.Build.ID {
		t.Fatalf(
			"generated idempotent CreateBuild id = %s, want %s",
			repeatedBuild.Payload.Build.ID,
			createdBuild.Payload.Build.ID,
		)
	}

	listed, err := client.PackerServiceListBuilds(
		packer_service.NewPackerServiceListBuildsParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithFingerprint("fingerprint"),
		contractAuth,
	)
	if err != nil || len(listed.Payload.Builds) != 1 {
		t.Fatalf("generated ListBuilds = %#v, %v", listed, err)
	}

	running := sdkmodels.HashicorpCloudPacker20230101BuildStatusBUILDRUNNING
	updateParams := func() *packer_service.PackerServiceUpdateBuildParams {
		return packer_service.NewPackerServiceUpdateBuildParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithFingerprint("fingerprint").
			WithBuildID(createdBuild.Payload.Build.ID).
			WithBody(&sdkmodels.HashicorpCloudPacker20230101UpdateBuildBody{
				Status: &running, Artifacts: []*sdkmodels.HashicorpCloudPacker20230101ArtifactCreateBody{},
			})
	}
	firstUpdate, err := client.PackerServiceUpdateBuild(updateParams(), contractAuth)
	if err != nil {
		t.Fatalf("generated UpdateBuild: %v", err)
	}
	heartbeat, err := client.PackerServiceUpdateBuild(updateParams(), contractAuth)
	if err != nil {
		t.Fatalf("generated heartbeat UpdateBuild: %v", err)
	}
	if !time.Time(heartbeat.Payload.Build.UpdatedAt).Equal(time.Time(firstUpdate.Payload.Build.UpdatedAt)) {
		t.Fatalf(
			"heartbeat changed updated_at from %v to %v",
			firstUpdate.Payload.Build.UpdatedAt,
			heartbeat.Payload.Build.UpdatedAt,
		)
	}

	// Packer uploads SBOMs while the build is running, before doCompleteBuild's
	// terminal heartbeat. Any error fails the whole build, the response payload
	// is ignored, and an omitted name defaults to the build fingerprint
	// (dossier §5.6).
	sbomFormat := sdkmodels.HashicorpCloudPacker20230101SbomFormatCYCLONEDX
	uploadSbomParams := func(name string) *packer_service.PackerServiceUploadSbomParams {
		return packer_service.NewPackerServiceUploadSbomParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithFingerprint("fingerprint").
			WithBuildID(createdBuild.Payload.Build.ID).
			WithBody(&sdkmodels.HashicorpCloudPacker20230101UploadSbomBody{
				// Probe 66 accepted 1,049,625 compressed bytes. This 768 KiB
				// fixture exercises the generated client's base64 expansion while
				// staying below that observed working size.
				CompressedSbom: bytes.Repeat([]byte{'z'}, 768<<10),
				Format:         &sbomFormat,
				Name:           name,
			})
	}
	namedSbom, err := client.PackerServiceUploadSbom(uploadSbomParams("my-sbom"), contractAuth)
	if err != nil {
		t.Fatalf("generated UploadSbom: %v", err)
	}
	if namedSbom.Payload.Sbom == nil || namedSbom.Payload.Sbom.Name != "my-sbom" {
		t.Fatalf("generated UploadSbom payload = %#v", namedSbom.Payload.Sbom)
	}
	unnamedSbom, err := client.PackerServiceUploadSbom(uploadSbomParams(""), contractAuth)
	if err != nil {
		t.Fatalf("generated unnamed UploadSbom: %v", err)
	}
	if unnamedSbom.Payload.Sbom == nil || unnamedSbom.Payload.Sbom.Name != "fingerprint" {
		t.Fatalf("generated unnamed UploadSbom = %#v, want the fingerprint as name",
			unnamedSbom.Payload.Sbom)
	}

	// doCompleteBuild's final PATCH: BUILD_DONE with artifacts and metadata.
	// Completion is what makes the version assignable to a channel, and the
	// name leaving "v0" is how Packer sees completion (dossier §5.2).
	doneStatus := sdkmodels.HashicorpCloudPacker20230101BuildStatusBUILDDONE
	packerMetadata := map[string]any{
		"options": map[string]any{"path": "template.pkr.hcl", "debug": false},
		"os":      map[string]any{"type": "linux", "arch": "amd64", "version": "contract"},
		"plugins": []any{map[string]any{"name": "docker", "version": "1.1.4"}},
	}
	if _, err := client.PackerServiceUpdateBuild(
		updateParams().WithBody(&sdkmodels.HashicorpCloudPacker20230101UpdateBuildBody{
			Status: &doneStatus,
			Artifacts: []*sdkmodels.HashicorpCloudPacker20230101ArtifactCreateBody{
				{ExternalIdentifier: "ami-1", Region: "us-east-1"},
			},
			Metadata: &sdkmodels.HashicorpCloudPacker20230101BuildMetadata{Packer: packerMetadata},
		}),
		contractAuth,
	); err != nil {
		t.Fatalf("generated completing UpdateBuild: %v", err)
	}
	completedVersion, err := client.PackerServiceGetVersion(
		packer_service.NewPackerServiceGetVersionParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithFingerprint("fingerprint"),
		contractAuth,
	)
	if err != nil || completedVersion.Payload.Version.Name != "v1" {
		t.Fatalf("generated completed GetVersion = %#v, %v; want name v1",
			completedVersion, err)
	}
	// The console browses buckets through ListVersions (ADR-0006); the completed
	// version must appear there under its completion name, not the v0 sentinel.
	listedVersions, err := client.PackerServiceListVersions(
		packer_service.NewPackerServiceListVersionsParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images"),
		contractAuth,
	)
	if err != nil || len(listedVersions.Payload.Versions) != 1 {
		t.Fatalf("generated ListVersions after completion = %#v, %v (want exactly 1)",
			listedVersions, err)
	}
	if got := listedVersions.Payload.Versions[0]; got.Name != "v1" || got.Fingerprint != "fingerprint" {
		t.Fatalf("generated ListVersions entry = name %q fingerprint %q, want v1/fingerprint",
			got.Name, got.Fingerprint)
	}
	completedBuild := completedVersion.Payload.Version.Builds[0]
	gotPackerMetadata, ok := completedBuild.Metadata.Packer.(map[string]any)
	if !ok || gotPackerMetadata["options"] == nil || gotPackerMetadata["os"] == nil ||
		gotPackerMetadata["plugins"] == nil {
		t.Fatalf("generated completed GetVersion metadata = %#v", completedBuild.Metadata)
	}

	// Completion assigned the version to "latest" with NO UpdateChannel call in
	// this test — exactly what the probe observed live (Appendix A probes
	// 13-14), and what makes `channel_name = "latest"` consumption work.
	autoAssigned, err := client.PackerServiceGetChannel(
		packer_service.NewPackerServiceGetChannelParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithChannelName("latest"),
		contractAuth,
	)
	if err != nil || autoAssigned.Payload.Channel == nil ||
		autoAssigned.Payload.Channel.Version == nil ||
		autoAssigned.Payload.Channel.Version.Fingerprint != "fingerprint" ||
		autoAssigned.Payload.Channel.Version.Name != "v1" {
		t.Fatalf("generated GetChannel latest after completion = %#v, %v; want v1 auto-assigned",
			autoAssigned, err)
	}
	latestHistory, err := client.PackerServiceListChannelAssignmentHistory(
		packer_service.NewPackerServiceListChannelAssignmentHistoryParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithChannelName("latest"),
		contractAuth,
	)
	if err != nil || len(latestHistory.Payload.History) != 1 ||
		latestHistory.Payload.History[0].AuthorID != "Dufflebag" {
		t.Fatalf("generated latest history = %#v, %v; want one Dufflebag-authored entry",
			latestHistory, err)
	}

	// Clients cannot mutate the managed channel. Both refusals are HTTP 400
	// with the captured codes — 9 for the assignment update, 3 for the delete;
	// the asymmetry is live's (Appendix A probes 19 and 17). Message prose
	// says "Dufflebag" where live says "HCP Packer" (verified inert: Packer's
	// errCodeRegex reads only the code; the provider branches on the managed
	// flag).
	managedMutation, err := client.PackerServiceUpdateChannel(
		packer_service.NewPackerServiceUpdateChannelParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithChannelName("latest").
			WithBody(&sdkmodels.HashicorpCloudPacker20230101UpdateChannelBody{
				UpdateMask:         "versionFingerprint",
				VersionFingerprint: "fingerprint",
			}),
		contractAuth,
	)
	if managedMutation != nil || err == nil {
		t.Fatalf("generated UpdateChannel latest succeeded: %#v", managedMutation)
	}
	var updateRefusal *packer_service.PackerServiceUpdateChannelDefault
	if !errors.As(err, &updateRefusal) || !updateRefusal.IsCode(http.StatusBadRequest) ||
		updateRefusal.Payload.Code != 9 {
		t.Fatalf("generated UpdateChannel latest = %v, want a parsed 400 code 9", err)
	}
	if !strings.Contains(err.Error(), `"code":9`) ||
		!strings.Contains(err.Error(), "This channel is managed by Dufflebag") {
		t.Fatalf("generated UpdateChannel latest error text = %v", err)
	}
	_, err = client.PackerServiceDeleteChannel(
		packer_service.NewPackerServiceDeleteChannelParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithChannelName("latest"),
		contractAuth,
	)
	var deleteRefusal *packer_service.PackerServiceDeleteChannelDefault
	if !errors.As(err, &deleteRefusal) || !deleteRefusal.IsCode(http.StatusBadRequest) ||
		deleteRefusal.Payload.Code != 3 {
		t.Fatalf("generated DeleteChannel latest = %v, want a parsed 400 code 3", err)
	}
	if !strings.Contains(err.Error(), "Can't delete managed channel latest, it's controlled by Dufflebag") {
		t.Fatalf("generated DeleteChannel latest error text = %v", err)
	}

	listedSboms, err := client.PackerServiceListSboms(
		packer_service.NewPackerServiceListSbomsParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithFingerprint("fingerprint").
			WithBuildID(createdBuild.Payload.Build.ID),
		contractAuth,
	)
	if err != nil || len(listedSboms.Payload.Sboms) != 2 {
		t.Fatalf("generated ListSboms = %#v, %v", listedSboms, err)
	}
	// GetSbom answers a download URL pointing at the sibling /download route —
	// the console's SBOM link follows it verbatim, so the path shape is the
	// contract (duf-egk2.3). The unnamed upload is fetched under the defaulted
	// name to prove the fingerprint-as-name rule survives the read path.
	getSbomParams := func(name string) *packer_service.PackerServiceGetSbomParams {
		return packer_service.NewPackerServiceGetSbomParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithFingerprint("fingerprint").
			WithBuildID(createdBuild.Payload.Build.ID).
			WithSbomName(name)
	}
	gotSbom, err := client.PackerServiceGetSbom(getSbomParams("fingerprint"), contractAuth)
	if err != nil {
		t.Fatalf("generated GetSbom: %v", err)
	}
	wantDownloadSuffix := "/builds/" + createdBuild.Payload.Build.ID + "/sboms/fingerprint/download"
	if !strings.HasSuffix(gotSbom.Payload.DownloadURL, wantDownloadSuffix) {
		t.Fatalf("generated GetSbom download_url = %q, want suffix %q",
			gotSbom.Payload.DownloadURL, wantDownloadSuffix)
	}
	if _, err := client.PackerServiceGetSbom(getSbomParams("absent"), contractAuth); err == nil ||
		!strings.Contains(err.Error(), `"code":5`) {
		t.Fatalf("generated GetSbom missing error = %v, want code 5", err)
	}
	repository.packages = []store.ReportedPackage{{
		Name: "openssl", Version: "3.0.11", Purl: "pkg:rpm/openssl@3.0.11",
		Sboms: []store.Sbom{
			repository.sboms["images/fingerprint/"+createdBuild.Payload.Build.ID+"/my-sbom"],
			repository.sboms["images/fingerprint/"+createdBuild.Payload.Build.ID+"/fingerprint"],
		},
	}}
	listedPackages, err := client.PackerServiceListBuildPackages(
		packer_service.NewPackerServiceListBuildPackagesParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithFingerprint("fingerprint").
			WithBuildID(createdBuild.Payload.Build.ID),
		contractAuth,
	)
	if err != nil || len(listedPackages.Payload.Packages) != 1 ||
		listedPackages.Payload.Packages[0].Purl != "pkg:rpm/openssl@3.0.11" ||
		len(listedPackages.Payload.Packages[0].Sboms) != 2 ||
		listedPackages.Payload.Pagination == nil {
		t.Fatalf("generated ListBuildPackages = %#v, %v", listedPackages, err)
	}

	if _, err := client.PackerServiceCreateChannel(
		packer_service.NewPackerServiceCreateChannelParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithBody(&sdkmodels.HashicorpCloudPacker20230101CreateChannelBody{
				Name: "production",
			}),
		contractAuth,
	); err != nil {
		t.Fatalf("generated CreateChannel: %v", err)
	}

	// The provider's assignment path: mask naming versionFingerprint with the
	// fingerprint set.
	updateChannelParams := func(body *sdkmodels.HashicorpCloudPacker20230101UpdateChannelBody) *packer_service.PackerServiceUpdateChannelParams {
		return packer_service.NewPackerServiceUpdateChannelParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithChannelName("production").
			WithBody(body)
	}
	assigned, err := client.PackerServiceUpdateChannel(
		updateChannelParams(&sdkmodels.HashicorpCloudPacker20230101UpdateChannelBody{
			UpdateMask:         "versionFingerprint",
			VersionFingerprint: "fingerprint",
		}),
		contractAuth,
	)
	if err != nil {
		t.Fatalf("generated assign UpdateChannel: %v", err)
	}
	if assigned.Payload.Channel == nil || assigned.Payload.Channel.Version == nil ||
		assigned.Payload.Channel.Version.Fingerprint != "fingerprint" {
		t.Fatalf("generated assign payload = %#v", assigned.Payload.Channel)
	}
	manualHistory, err := client.PackerServiceListChannelAssignmentHistory(
		packer_service.NewPackerServiceListChannelAssignmentHistoryParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithChannelName("production"),
		contractAuth,
	)
	if err != nil || len(manualHistory.Payload.History) != 1 ||
		manualHistory.Payload.History[0].AuthorID != "p-contract" {
		t.Fatalf("generated manual history = %#v, %v; want caller-authored row",
			manualHistory, err)
	}

	_, err = client.PackerServiceAssignChannelVersion(
		packer_service.NewPackerServiceAssignChannelVersionParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithBody(&sdkmodels.HashicorpCloudPacker20230101AssignChannelVersionBody{
				SourceChannel: "production", TargetChannel: "latest",
			}),
		contractAuth,
	)
	var managedAssign *packer_service.PackerServiceAssignChannelVersionDefault
	if !errors.As(err, &managedAssign) || !managedAssign.IsCode(http.StatusBadRequest) ||
		managedAssign.Payload.Code != 9 ||
		managedAssign.Payload.Message != "Cannot assign to managed channel 'latest'" {
		t.Fatalf("generated assign into managed latest = %v, want typed probe-40 refusal", err)
	}

	// The provider reads channels by LISTING and filtering client-side
	// (GetPackerChannelByNameFromList, ADR-0013), so the listing must carry the
	// assignment too.
	listedChannels, err := client.PackerServiceListChannels(
		packer_service.NewPackerServiceListChannelsParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images"),
		contractAuth,
	)
	if err != nil || len(listedChannels.Payload.Channels) != 2 ||
		listedChannels.Payload.Channels[0].Name != "latest" ||
		listedChannels.Payload.Channels[1].Name != "production" ||
		listedChannels.Payload.Channels[1].Version == nil {
		t.Fatalf("generated ListChannels = %#v, %v", listedChannels, err)
	}

	// UpdateChannel without update_mask is refused — the one strictness live
	// HCP has where the spec is silent (Appendix A probe 15; duf-7cy keeps it
	// while dropping DisallowUnknownFields).
	_, err = client.PackerServiceUpdateChannel(
		updateChannelParams(&sdkmodels.HashicorpCloudPacker20230101UpdateChannelBody{
			VersionFingerprint: "fingerprint",
		}),
		contractAuth,
	)
	var masklessRefusal *packer_service.PackerServiceUpdateChannelDefault
	if !errors.As(err, &masklessRefusal) || !masklessRefusal.IsCode(http.StatusBadRequest) ||
		masklessRefusal.Payload.Code != 3 {
		t.Fatalf("generated maskless UpdateChannel = %v, want a parsed 400 code 3", err)
	}
	if !strings.Contains(err.Error(), "update_mask: field mask: must be set") {
		t.Fatalf("generated maskless UpdateChannel error text = %v", err)
	}

	// The provider's destroy path: UpdateChannel whose mask names
	// versionFingerprint with the fingerprint omitted means "clear the
	// assignment". This answered 400 code 3, failing every terraform destroy of
	// an hcp_packer_channel_assignment (duf-8em). The SDK marshals both fields
	// into the body with omitempty, so the wire shape is the mask alone.
	unassigned, err := client.PackerServiceUpdateChannel(
		updateChannelParams(&sdkmodels.HashicorpCloudPacker20230101UpdateChannelBody{
			UpdateMask: "versionFingerprint",
		}),
		contractAuth,
	)
	if err != nil {
		t.Fatalf("generated unassign UpdateChannel: %v", err)
	}
	if unassigned.Payload.Channel == nil || unassigned.Payload.Channel.Version != nil {
		t.Fatalf("generated unassign payload = %#v", unassigned.Payload.Channel)
	}
	cleared, err := client.PackerServiceGetChannel(
		packer_service.NewPackerServiceGetChannelParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithChannelName("production"),
		contractAuth,
	)
	if err != nil || cleared.Payload.Channel == nil || cleared.Payload.Channel.Version != nil {
		t.Fatalf("generated GetChannel after unassign = %#v, %v; want no version",
			cleared, err)
	}

	reassigned, err := client.PackerServiceUpdateChannel(
		updateChannelParams(&sdkmodels.HashicorpCloudPacker20230101UpdateChannelBody{
			UpdateMask:         "versionFingerprint",
			VersionFingerprint: "fingerprint",
		}),
		contractAuth,
	)
	if err != nil || reassigned.Payload.Channel == nil || reassigned.Payload.Channel.Version == nil {
		t.Fatalf("generated reassign UpdateChannel = %#v, %v", reassigned, err)
	}

	pageSize := int64(1)
	historyPageOne, err := client.PackerServiceListChannelAssignmentHistory(
		packer_service.NewPackerServiceListChannelAssignmentHistoryParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithChannelName("production").
			WithPaginationPageSize(&pageSize),
		contractAuth,
	)
	if err != nil || historyPageOne.Payload.Count != 2 ||
		len(historyPageOne.Payload.History) != 1 ||
		historyPageOne.Payload.Pagination == nil ||
		historyPageOne.Payload.Pagination.NextPageToken == "" {
		t.Fatalf("generated paged history first page = %#v, %v", historyPageOne, err)
	}
	historyPageTwo, err := client.PackerServiceListChannelAssignmentHistory(
		packer_service.NewPackerServiceListChannelAssignmentHistoryParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithChannelName("production").
			WithPaginationPageSize(&pageSize).
			WithPaginationNextPageToken(&historyPageOne.Payload.Pagination.NextPageToken),
		contractAuth,
	)
	if err != nil || len(historyPageTwo.Payload.History) != 1 ||
		historyPageTwo.Payload.Pagination == nil ||
		historyPageTwo.Payload.Pagination.PreviousPageToken == "" {
		t.Fatalf("generated paged history second page = %#v, %v", historyPageTwo, err)
	}
	historyBack, err := client.PackerServiceListChannelAssignmentHistory(
		packer_service.NewPackerServiceListChannelAssignmentHistoryParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithChannelName("production").
			WithPaginationPageSize(&pageSize).
			WithPaginationPreviousPageToken(&historyPageTwo.Payload.Pagination.PreviousPageToken),
		contractAuth,
	)
	if err != nil || len(historyBack.Payload.History) != 1 ||
		historyBack.Payload.History[0].AssignedAt.String() != historyPageOne.Payload.History[0].AssignedAt.String() {
		t.Fatalf("generated paged history previous page = %#v, %v", historyBack, err)
	}

	if _, err := client.PackerServiceCreateBucket(
		packer_service.NewPackerServiceCreateBucketParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBody(&sdkmodels.HashicorpCloudPacker20230101CreateBucketBody{Name: "derived"}),
		contractAuth,
	); err != nil {
		t.Fatalf("generated CreateBucket derived: %v", err)
	}
	for index, fingerprint := range []string{"derived-1", "derived-2"} {
		if _, err := client.PackerServiceCreateVersion(
			packer_service.NewPackerServiceCreateVersionParams().
				WithLocationOrganizationID(contractOrg).
				WithLocationProjectID(contractProject).
				WithBucketName("derived").
				WithBody(&sdkmodels.HashicorpCloudPacker20230101CreateVersionBody{
					Fingerprint: fingerprint, TemplateType: &templateType,
				}),
			contractAuth,
		); err != nil {
			t.Fatalf("generated CreateVersion %s: %v", fingerprint, err)
		}
		childBuild, err := client.PackerServiceCreateBuild(
			packer_service.NewPackerServiceCreateBuildParams().
				WithLocationOrganizationID(contractOrg).
				WithLocationProjectID(contractProject).
				WithBucketName("derived").
				WithFingerprint(fingerprint).
				WithBody(&sdkmodels.HashicorpCloudPacker20230101CreateBuildBody{
					ComponentType: "docker", PackerRunUUID: fmt.Sprintf("child-run-%d", index),
					Status: &buildStatus, Artifacts: []*sdkmodels.HashicorpCloudPacker20230101ArtifactCreateBody{},
				}),
			contractAuth,
		)
		if err != nil {
			t.Fatalf("generated CreateBuild %s: %v", fingerprint, err)
		}
		if _, err := client.PackerServiceUpdateBuild(
			packer_service.NewPackerServiceUpdateBuildParams().
				WithLocationOrganizationID(contractOrg).
				WithLocationProjectID(contractProject).
				WithBucketName("derived").
				WithFingerprint(fingerprint).
				WithBuildID(childBuild.Payload.Build.ID).
				WithBody(&sdkmodels.HashicorpCloudPacker20230101UpdateBuildBody{
					Status: &doneStatus, Metadata: &sdkmodels.HashicorpCloudPacker20230101BuildMetadata{},
					Artifacts:       []*sdkmodels.HashicorpCloudPacker20230101ArtifactCreateBody{},
					ParentVersionID: completedVersion.Payload.Version.ID,
					ParentChannelID: reassigned.Payload.Channel.ID,
				}),
			contractAuth,
		); err != nil {
			t.Fatalf("generated completing child UpdateBuild %s: %v", fingerprint, err)
		}
	}

	projectedParent, err := client.PackerServiceGetVersion(
		packer_service.NewPackerServiceGetVersionParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithFingerprint("fingerprint"),
		contractAuth,
	)
	if err != nil || !projectedParent.Payload.Version.HasDescendants {
		t.Fatalf("generated parent version projection = %#v, %v", projectedParent, err)
	}
	projectedChild, err := client.PackerServiceGetVersion(
		packer_service.NewPackerServiceGetVersionParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("derived").
			WithFingerprint("derived-1"),
		contractAuth,
	)
	wantParentsHref := "/packer/2023-01-01/organizations/" + contractOrg +
		"/projects/" + contractProject +
		"/buckets/derived/ancestry?type=ANCESTRY_TYPE_PARENTS&version_fingerprint=derived-1"
	if err != nil || projectedChild.Payload.Version.Parents == nil ||
		projectedChild.Payload.Version.Parents.Status == nil ||
		*projectedChild.Payload.Version.Parents.Status != sdkmodels.HashicorpCloudPacker20230101AncestryStatusUPTODATE ||
		projectedChild.Payload.Version.Parents.Href != wantParentsHref {
		t.Fatalf("generated child version projection = %#v, %v", projectedChild, err)
	}

	childrenType := string(sdkmodels.HashicorpCloudPacker20230101BucketAncestryTypeANCESTRYTYPECHILDREN)
	productionChannel := "production"
	ancestryPageOne, err := client.PackerServiceListBucketAncestry(
		packer_service.NewPackerServiceListBucketAncestryParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithType(&childrenType).
			WithChannelName(&productionChannel).
			WithPaginationPageSize(&pageSize),
		contractAuth,
	)
	if err != nil || ancestryPageOne.Payload.TotalCount != 2 ||
		len(ancestryPageOne.Payload.Relations) != 1 ||
		ancestryPageOne.Payload.Pagination == nil ||
		ancestryPageOne.Payload.Pagination.NextPageToken == "" {
		t.Fatalf("generated ancestry first page = %#v, %v", ancestryPageOne, err)
	}
	firstRelation := ancestryPageOne.Payload.Relations[0]
	if firstRelation.Parent == nil || firstRelation.Parent.BucketName != "images" ||
		firstRelation.Parent.ChannelName != "production" ||
		firstRelation.Child == nil || firstRelation.Child.BucketName != "derived" ||
		firstRelation.Status == nil ||
		*firstRelation.Status != sdkmodels.HashicorpCloudPacker20230101AncestryStatusUPTODATE {
		t.Fatalf("generated ancestry relation = %#v", firstRelation)
	}
	ancestryPageTwo, err := client.PackerServiceListBucketAncestry(
		packer_service.NewPackerServiceListBucketAncestryParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithType(&childrenType).
			WithChannelName(&productionChannel).
			WithPaginationPageSize(&pageSize).
			WithPaginationNextPageToken(&ancestryPageOne.Payload.Pagination.NextPageToken),
		contractAuth,
	)
	if err != nil || len(ancestryPageTwo.Payload.Relations) != 1 ||
		ancestryPageTwo.Payload.Pagination == nil ||
		ancestryPageTwo.Payload.Pagination.PreviousPageToken == "" {
		t.Fatalf("generated ancestry second page = %#v, %v", ancestryPageTwo, err)
	}
	ancestryBack, err := client.PackerServiceListBucketAncestry(
		packer_service.NewPackerServiceListBucketAncestryParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithType(&childrenType).
			WithChannelName(&productionChannel).
			WithPaginationPageSize(&pageSize).
			WithPaginationPreviousPageToken(&ancestryPageTwo.Payload.Pagination.PreviousPageToken),
		contractAuth,
	)
	if err != nil || len(ancestryBack.Payload.Relations) != 1 ||
		ancestryBack.Payload.Relations[0].Child.VersionFingerprint !=
			firstRelation.Child.VersionFingerprint {
		t.Fatalf("generated ancestry previous page = %#v, %v", ancestryBack, err)
	}

	parentsType := string(sdkmodels.HashicorpCloudPacker20230101BucketAncestryTypeANCESTRYTYPEPARENTS)
	childFingerprint := "derived-1"
	parents, err := client.PackerServiceListBucketAncestry(
		packer_service.NewPackerServiceListBucketAncestryParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("derived").
			WithType(&parentsType).
			WithVersionFingerprint(&childFingerprint),
		contractAuth,
	)
	if err != nil || len(parents.Payload.Relations) != 1 ||
		parents.Payload.Relations[0].Child.VersionFingerprint != childFingerprint {
		t.Fatalf("generated ancestry parent filter = %#v, %v", parents, err)
	}

	listedBuckets, err := client.PackerServiceListBuckets(
		packer_service.NewPackerServiceListBucketsParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject),
		contractAuth,
	)
	if err != nil || len(listedBuckets.Payload.Buckets) != 2 ||
		listedBuckets.Payload.Buckets[0].Name != "derived" ||
		listedBuckets.Payload.Buckets[1].Name != "images" {
		t.Fatalf("generated ListBuckets = %#v, %v", listedBuckets, err)
	}

	// Version revocation through the generated client (duf-3bno). The write is
	// HCP-console-plane surface no supported client drives; the READ side is
	// the compatibility contract — packer's data sources refuse a channel
	// whose nested version carries VERSION_REVOKED (hcp-packer-version
	// data.go:129).
	updateVersionParams := func(body *sdkmodels.HashicorpCloudPacker20230101UpdateVersionBody) *packer_service.PackerServiceUpdateVersionParams {
		return packer_service.NewPackerServiceUpdateVersionParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithFingerprint("fingerprint").
			WithBody(body)
	}
	// Restore on a valid version is a state refusal, with the live message
	// byte-for-byte including its trailing space.
	_, err = client.PackerServiceUpdateVersion(
		updateVersionParams(&sdkmodels.HashicorpCloudPacker20230101UpdateVersionBody{Restore: true}),
		contractAuth,
	)
	var updateVersionRefusal *packer_service.PackerServiceUpdateVersionDefault
	if !errors.As(err, &updateVersionRefusal) || !updateVersionRefusal.IsCode(http.StatusBadRequest) ||
		updateVersionRefusal.Payload.Code != 9 ||
		updateVersionRefusal.Payload.Message !=
			"Restoring does not apply. This version is valid and it is not scheduled to be revoked. " {
		t.Fatalf("generated UpdateVersion restore active = %#v, %v; want the exact parsed 400 code 9",
			updateVersionRefusal, err)
	}

	// Restore cannot be combined with either scheduling form.
	_, err = client.PackerServiceUpdateVersion(
		updateVersionParams(&sdkmodels.HashicorpCloudPacker20230101UpdateVersionBody{
			Restore: true, RevokeAt: strfmt.DateTime(time.Now().UTC()),
		}),
		contractAuth,
	)
	if !errors.As(err, &updateVersionRefusal) || !updateVersionRefusal.IsCode(http.StatusBadRequest) ||
		updateVersionRefusal.Payload.Code != 3 {
		t.Fatalf("generated UpdateVersion restore+revoke_at = %v, want a parsed 400 code 3", err)
	}

	revokedResponse, err := client.PackerServiceUpdateVersion(
		updateVersionParams(&sdkmodels.HashicorpCloudPacker20230101UpdateVersionBody{
			RevokeAt:          strfmt.DateTime(time.Now().UTC()),
			RevocationMessage: "contract revocation",
		}),
		contractAuth,
	)
	if err != nil {
		t.Fatalf("generated UpdateVersion revoke: %v", err)
	}
	revokedVersion := revokedResponse.Payload.Version
	if revokedVersion.Status == nil ||
		*revokedVersion.Status != sdkmodels.HashicorpCloudPacker20230101VersionStatusVERSIONREVOKED {
		t.Fatalf("generated revoked status = %#v, want VERSION_REVOKED", revokedVersion.Status)
	}
	if revokedVersion.RevocationType == nil ||
		*revokedVersion.RevocationType != sdkmodels.HashicorpCloudPacker20230101RevocationTypeMANUAL ||
		revokedVersion.RevocationAuthor != "contract" ||
		revokedVersion.RevocationMessage != "contract revocation" {
		t.Fatalf("generated revoked fields = %#v", revokedVersion)
	}

	// The read packer's data source performs: the managed latest channel's
	// nested version now carries VERSION_REVOKED.
	latestRead, err := client.PackerServiceGetChannel(
		packer_service.NewPackerServiceGetChannelParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithChannelName("latest"),
		contractAuth,
	)
	if err != nil || latestRead.Payload.Channel == nil || latestRead.Payload.Channel.Version == nil ||
		latestRead.Payload.Channel.Version.Status == nil ||
		*latestRead.Payload.Channel.Version.Status != sdkmodels.HashicorpCloudPacker20230101VersionStatusVERSIONREVOKED {
		t.Fatalf("generated GetChannel latest after revoke = %#v, %v; want a REVOKED nested version",
			latestRead, err)
	}

	// Descendants inherit transitively, naming the revoked ancestor.
	inheritedRead, err := client.PackerServiceGetVersion(
		packer_service.NewPackerServiceGetVersionParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("derived").
			WithFingerprint("derived-1"),
		contractAuth,
	)
	if err != nil {
		t.Fatalf("generated GetVersion derived-1 after revoke: %v", err)
	}
	inherited := inheritedRead.Payload.Version
	if inherited.RevocationType == nil ||
		*inherited.RevocationType != sdkmodels.HashicorpCloudPacker20230101RevocationTypeINHERITED ||
		inherited.RevocationInheritedFrom == nil ||
		inherited.RevocationInheritedFrom.BucketName != "images" ||
		inherited.RevocationInheritedFrom.VersionFingerprint != "fingerprint" {
		t.Fatalf("generated inherited revocation = %#v", inherited)
	}

	// A descendant cannot restore a revocation inherited from its ancestor.
	updateVersionRefusal = nil
	_, err = client.PackerServiceUpdateVersion(
		packer_service.NewPackerServiceUpdateVersionParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("derived").
			WithFingerprint("derived-1").
			WithBody(&sdkmodels.HashicorpCloudPacker20230101UpdateVersionBody{Restore: true}),
		contractAuth,
	)
	if !errors.As(err, &updateVersionRefusal) || !updateVersionRefusal.IsCode(http.StatusBadRequest) ||
		updateVersionRefusal.Payload.Code != 9 ||
		updateVersionRefusal.Payload.Message !=
			"Directly restoring this version does not apply. The revocation status is inherited from an ancestor version. To restore this version, the revoked ancestor should be restored." {
		t.Fatalf("generated UpdateVersion restore inherited = %#v, %v; want the exact parsed 400 code 9",
			updateVersionRefusal, err)
	}

	// Re-revoking is a state conflict: 400 with code 9, like the managed
	// channel refusals.
	_, err = client.PackerServiceUpdateVersion(
		updateVersionParams(&sdkmodels.HashicorpCloudPacker20230101UpdateVersionBody{RevokeIn: "0s"}),
		contractAuth,
	)
	if !errors.As(err, &updateVersionRefusal) || !updateVersionRefusal.IsCode(http.StatusBadRequest) ||
		updateVersionRefusal.Payload.Code != 9 {
		t.Fatalf("generated re-revoke = %v, want a parsed 400 code 9", err)
	}

	restoredResponse, err := client.PackerServiceUpdateVersion(
		updateVersionParams(&sdkmodels.HashicorpCloudPacker20230101UpdateVersionBody{Restore: true}),
		contractAuth,
	)
	if err != nil {
		t.Fatalf("generated UpdateVersion restore: %v", err)
	}
	restoredVersion := restoredResponse.Payload.Version
	if restoredVersion.Status == nil ||
		*restoredVersion.Status != sdkmodels.HashicorpCloudPacker20230101VersionStatusVERSIONACTIVE ||
		!time.Time(restoredVersion.RevokeAt).IsZero() || restoredVersion.RevocationMessage != "" ||
		restoredVersion.RevocationAuthor != "" || restoredVersion.RevocationType != nil ||
		restoredVersion.RevocationInheritedFrom != nil {
		t.Fatalf("generated restored fields = %#v, want active with revocation cleared", restoredVersion)
	}

	restoredRead, err := client.PackerServiceGetVersion(
		packer_service.NewPackerServiceGetVersionParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithFingerprint("fingerprint"),
		contractAuth,
	)
	if err != nil || restoredRead.Payload.Version.Status == nil ||
		*restoredRead.Payload.Version.Status != sdkmodels.HashicorpCloudPacker20230101VersionStatusVERSIONACTIVE ||
		!time.Time(restoredRead.Payload.Version.RevokeAt).IsZero() {
		t.Fatalf("generated GetVersion after restore = %#v, %v; want ACTIVE and cleared",
			restoredRead, err)
	}
	latestRead, err = client.PackerServiceGetChannel(
		packer_service.NewPackerServiceGetChannelParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithChannelName("latest"),
		contractAuth,
	)
	if err != nil || latestRead.Payload.Channel == nil || latestRead.Payload.Channel.Version == nil ||
		latestRead.Payload.Channel.Version.Status == nil ||
		*latestRead.Payload.Channel.Version.Status != sdkmodels.HashicorpCloudPacker20230101VersionStatusVERSIONACTIVE {
		t.Fatalf("generated GetChannel latest after restore = %#v, %v; want an ACTIVE nested version",
			latestRead, err)
	}
	restoredInherited, err := client.PackerServiceGetVersion(
		packer_service.NewPackerServiceGetVersionParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("derived").
			WithFingerprint("derived-1"),
		contractAuth,
	)
	if err != nil || restoredInherited.Payload.Version.RevocationType != nil ||
		restoredInherited.Payload.Version.RevocationInheritedFrom != nil {
		t.Fatalf("generated descendant after restore = %#v, %v; want inherited revocation cleared",
			restoredInherited, err)
	}

	// Packer sends explicit parent IDs on the completing UpdateBuild. If the
	// parent is already revoked, that SDK-driven child flow inherits immediately
	// rather than waiting for another revoke operation (live probe A.14).
	if _, err := client.PackerServiceUpdateVersion(
		updateVersionParams(&sdkmodels.HashicorpCloudPacker20230101UpdateVersionBody{
			RevokeAt: strfmt.DateTime(time.Now().UTC()), RevocationMessage: "record-time contract",
		}),
		contractAuth,
	); err != nil {
		t.Fatalf("generated parent revoke before child flow: %v", err)
	}
	if _, err := client.PackerServiceCreateVersion(
		packer_service.NewPackerServiceCreateVersionParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("derived").
			WithBody(&sdkmodels.HashicorpCloudPacker20230101CreateVersionBody{
				Fingerprint: "derived-record-time", TemplateType: &templateType,
			}),
		contractAuth,
	); err != nil {
		t.Fatalf("generated CreateVersion record-time child: %v", err)
	}
	recordTimeBuild, err := client.PackerServiceCreateBuild(
		packer_service.NewPackerServiceCreateBuildParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("derived").
			WithFingerprint("derived-record-time").
			WithBody(&sdkmodels.HashicorpCloudPacker20230101CreateBuildBody{
				ComponentType: "docker", PackerRunUUID: "record-time-child",
				Status: &buildStatus, Artifacts: []*sdkmodels.HashicorpCloudPacker20230101ArtifactCreateBody{},
			}),
		contractAuth,
	)
	if err != nil {
		t.Fatalf("generated CreateBuild record-time child: %v", err)
	}
	if _, err := client.PackerServiceUpdateBuild(
		packer_service.NewPackerServiceUpdateBuildParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("derived").
			WithFingerprint("derived-record-time").
			WithBuildID(recordTimeBuild.Payload.Build.ID).
			WithBody(&sdkmodels.HashicorpCloudPacker20230101UpdateBuildBody{
				Status: &doneStatus, Metadata: &sdkmodels.HashicorpCloudPacker20230101BuildMetadata{},
				Artifacts:       []*sdkmodels.HashicorpCloudPacker20230101ArtifactCreateBody{},
				ParentVersionID: completedVersion.Payload.Version.ID,
				ParentChannelID: reassigned.Payload.Channel.ID,
			}),
		contractAuth,
	); err != nil {
		t.Fatalf("generated completing record-time child UpdateBuild: %v", err)
	}
	recordTimeChild, err := client.PackerServiceGetVersion(
		packer_service.NewPackerServiceGetVersionParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("derived").
			WithFingerprint("derived-record-time"),
		contractAuth,
	)
	if err != nil || recordTimeChild.Payload.Version.RevocationType == nil ||
		*recordTimeChild.Payload.Version.RevocationType != sdkmodels.HashicorpCloudPacker20230101RevocationTypeINHERITED ||
		recordTimeChild.Payload.Version.Status == nil ||
		*recordTimeChild.Payload.Version.Status != sdkmodels.HashicorpCloudPacker20230101VersionStatusVERSIONREVOKED ||
		recordTimeChild.Payload.Version.RevocationInheritedFrom == nil ||
		recordTimeChild.Payload.Version.RevocationInheritedFrom.VersionID != completedVersion.Payload.Version.ID {
		t.Fatalf("generated record-time inherited child = %#v, %v", recordTimeChild, err)
	}

	// The provider's channel destroy calls DeleteChannel directly.
	if _, err := client.PackerServiceDeleteChannel(
		packer_service.NewPackerServiceDeleteChannelParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithChannelName("production"),
		contractAuth,
	); err != nil {
		t.Fatalf("generated DeleteChannel: %v", err)
	}
	_, err = client.PackerServiceGetChannel(
		packer_service.NewPackerServiceGetChannelParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithChannelName("production"),
		contractAuth,
	)
	if err == nil || !strings.Contains(err.Error(), `"code":5`) {
		t.Fatalf("generated GetChannel after delete = %v, want code 5", err)
	}

	// The provider's bucket destroy calls DeleteBucket directly and tolerates
	// exactly one error: an HTTP 404 removes the resource from state
	// (resource_packer_bucket.go, dossier §9).
	deleteBucketParams := func() *packer_service.PackerServiceDeleteBucketParams {
		return packer_service.NewPackerServiceDeleteBucketParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images")
	}
	if _, err := client.PackerServiceDeleteBucket(deleteBucketParams(), contractAuth); err != nil {
		t.Fatalf("generated DeleteBucket: %v", err)
	}
	_, err = client.PackerServiceGetBucket(
		packer_service.NewPackerServiceGetBucketParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images"),
		contractAuth,
	)
	if err == nil || !strings.Contains(err.Error(), `"code":5`) {
		t.Fatalf("generated GetBucket after delete = %v, want code 5", err)
	}
	_, err = client.PackerServiceDeleteBucket(deleteBucketParams(), contractAuth)
	var deleteErr *packer_service.PackerServiceDeleteBucketDefault
	if !errors.As(err, &deleteErr) || !deleteErr.IsCode(http.StatusNotFound) {
		t.Fatalf("generated repeat DeleteBucket = %v, want the HTTP 404 the provider tolerates", err)
	}
}

func TestGeneratedClientReadsStoredVulnerabilities(t *testing.T) {
	repository := newContractRepository()
	at := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC)
	repository.buckets["images"] = &store.Bucket{
		ID: registry.NewID(at), Name: "images", CreatedAt: at, UpdatedAt: at,
	}
	versionOne, err := registry.NewVersion(
		registry.NewID(at.Add(time.Second)), "images", "fp-1", registry.TemplateHCL2, at,
	)
	if err != nil {
		t.Fatal(err)
	}
	versionTwo, err := registry.NewVersion(
		registry.NewID(at.Add(2*time.Second)), "images", "fp-2", registry.TemplateHCL2, at,
	)
	if err != nil {
		t.Fatal(err)
	}
	repository.versions["images/fp-1"] = versionOne
	repository.versions["images/fp-2"] = versionTwo
	buildOne := store.StoredBuild{
		Build:     registry.Build{ID: registry.NewID(at.Add(3 * time.Second)), ComponentType: "amazon-ebs"},
		VersionID: versionOne.ID, CreatedAt: at, UpdatedAt: at,
	}
	buildTwo := store.StoredBuild{
		Build:     registry.Build{ID: registry.NewID(at.Add(4 * time.Second)), ComponentType: "docker"},
		VersionID: versionTwo.ID, CreatedAt: at, UpdatedAt: at,
	}
	repository.builds["images/fp-1"] = []store.StoredBuild{buildOne}
	repository.builds["images/fp-2"] = []store.StoredBuild{buildTwo}
	stable := store.Channel{
		ID: registry.NewID(at.Add(5 * time.Second)), BucketName: "images", Name: "stable",
		Version: versionOne, CreatedAt: at, UpdatedAt: at,
	}
	latest := store.Channel{
		ID: registry.NewID(at.Add(6 * time.Second)), BucketName: "images", Name: "latest",
		Version: versionTwo, CreatedAt: at, UpdatedAt: at,
	}
	repository.channels["images/stable"] = &stable
	repository.channels["images/latest"] = &latest
	repository.packages = []store.ReportedPackage{{
		Name: "openssl", Version: "1.0", Purl: "pkg:apk/alpine/openssl@1.0",
	}}

	observedOne := at.Add(10 * time.Minute)
	observedTwo := at.Add(20 * time.Minute)
	repository.scanStates[buildOne.ID.String()] = &store.BuildScanState{
		BuildID: buildOne.ID.String(), CurrentFindingsRunID: "run-current-1", LatestAttemptRunID: "run-failed-1",
	}
	repository.scanStates[buildTwo.ID.String()] = &store.BuildScanState{
		BuildID: buildTwo.ID.String(), CurrentFindingsRunID: "run-current-2", LatestAttemptRunID: "run-current-2",
	}
	repository.scanRuns["run-current-1"] = &store.ScanRun{
		ID: "run-current-1", BuildID: buildOne.ID.String(), Status: store.ScanRunSucceeded,
		Adapter: "osv", Engine: "stub", DatabaseRevision: "2026-08-07", ObservedAt: observedOne,
		Coverage: scan.Coverage{Submitted: 2},
	}
	repository.scanRuns["run-current-2"] = &store.ScanRun{
		ID: "run-current-2", BuildID: buildTwo.ID.String(), Status: store.ScanRunSucceeded,
		Adapter: "osv", Engine: "stub", DatabaseRevision: "2026-08-07", ObservedAt: observedTwo,
		Coverage: scan.Coverage{Submitted: 1},
	}
	repository.scanRuns["run-failed-1"] = &store.ScanRun{
		ID: "run-failed-1", BuildID: buildOne.ID.String(), Status: store.ScanRunFailed,
		Adapter: "failed", ObservedAt: observedTwo.Add(time.Hour),
	}
	firstSeen := at.Add(-48 * time.Hour)
	published := at.Add(-30 * 24 * time.Hour)
	withdrawn := at.Add(-24 * time.Hour)
	packageOne := scan.Package{SBOMID: "sbom-a", Name: "openssl", Version: "1.0", Purl: "pkg:apk/alpine/openssl@1.0"}
	cve := store.StoredFinding{Finding: scan.Finding{
		Package: packageOne, ID: "CVE-2026-0001", Summary: "openssl overflow",
		Aliases: []string{"CVE-ALIAS-2", "CVE-ALIAS-1"}, Related: []string{"REL-2", "REL-1"},
		Published: published, Withdrawn: withdrawn, FixedVersions: []string{"1.2", "1.1"},
		Severities: []scan.SeverityValue{
			{Source: "osv:database_specific", Type: "label", Value: "LOW"},
			{Source: "osv", Type: "CVSS_V3", Value: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:L"},
		},
		Severity: scan.SeverityHigh,
	}, FirstSeenAt: firstSeen}
	duplicateCVE := cve
	duplicateCVE.Package.SBOMID = "sbom-b"
	critical := store.StoredFinding{Finding: scan.Finding{
		Package: packageOne, ID: "GHSA-critical", Summary: "critical advisory",
		Severities: []scan.SeverityValue{{Source: "osv:database_specific", Type: "label", Value: "CRITICAL"}},
		Severity:   scan.SeverityCritical,
	}, FirstSeenAt: at.Add(-24 * time.Hour)}
	repository.scanFindings["run-current-1"] = []store.StoredFinding{duplicateCVE, critical, cve, cve}
	buildTwoCVE := cve
	buildTwoCVE.Package.SBOMID = "sbom-c"
	buildTwoCVE.FirstSeenAt = at.Add(-24 * time.Hour)
	repository.scanFindings["run-current-2"] = []store.StoredFinding{buildTwoCVE, buildTwoCVE}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(hcp2023.NewHandlerWithRepository(repository, contractPrincipals{}, contractAuthenticator{}, logger))
	defer server.Close()
	client := cloudclient.NewHTTPClientWithConfig(
		nil,
		cloudclient.DefaultTransportConfig().
			WithHost(strings.TrimPrefix(server.URL, "http://")).
			WithSchemes([]string{"http"}),
	).PackerService

	buildPackages, err := client.PackerServiceListBuildPackages(
		packer_service.NewPackerServiceListBuildPackagesParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithFingerprint("fp-1").
			WithBuildID(buildOne.ID.String()),
		contractAuth,
	)
	if err != nil || len(buildPackages.Payload.Packages) != 1 ||
		len(buildPackages.Payload.Packages[0].VulnDetails) != 1 {
		t.Fatalf("generated ListBuildPackages vulnerabilities = %#v, %v", buildPackages, err)
	}
	mapped := buildPackages.Payload.Packages[0].VulnDetails[0].Vulnerabilities[0]
	wantVector := "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:L"
	if mapped.Identifier != "CVE-2026-0001" || mapped.Description != "openssl overflow" ||
		mapped.Criticality != "high" || mapped.Severity != wantVector ||
		mapped.RefersTo != "CVE-ALIAS-1,CVE-ALIAS-2" || mapped.Related != "REL-1,REL-2" ||
		mapped.FixedVersion != "1.1,1.2" || !time.Time(mapped.FirstSeenAt).Equal(firstSeen) ||
		!time.Time(mapped.PublishedAt).Equal(published) || !time.Time(mapped.WithdrawnAt).Equal(withdrawn) {
		t.Fatalf("generated finding mapping = %#v", mapped)
	}

	pageSize := int64(1)
	packagePageOne, err := client.PackerServiceListBucketPackagesWithVulnerabilities(
		packer_service.NewPackerServiceListBucketPackagesWithVulnerabilitiesParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithPaginationPageSize(&pageSize),
		contractAuth,
	)
	if err != nil || len(packagePageOne.Payload.Packages) != 1 ||
		packagePageOne.Payload.Pagination == nil || packagePageOne.Payload.Pagination.NextPageToken == "" {
		t.Fatalf("generated vulnerable packages first page = %#v, %v", packagePageOne, err)
	}
	firstPackage := packagePageOne.Payload.Packages[0]
	if firstPackage.BuildID != buildOne.ID.String() || firstPackage.BuildName != "amazon-ebs" ||
		firstPackage.ChannelID != stable.ID.String() || firstPackage.ChannelName != "stable" ||
		len(firstPackage.VulnDetails) != 1 {
		t.Fatalf("generated vulnerable package row = %#v", firstPackage)
	}
	packagePageTwo, err := client.PackerServiceListBucketPackagesWithVulnerabilities(
		packer_service.NewPackerServiceListBucketPackagesWithVulnerabilitiesParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithPaginationPageSize(&pageSize).
			WithPaginationNextPageToken(&packagePageOne.Payload.Pagination.NextPageToken),
		contractAuth,
	)
	if err != nil || len(packagePageTwo.Payload.Packages) != 1 ||
		packagePageTwo.Payload.Packages[0].BuildID != buildTwo.ID.String() ||
		packagePageTwo.Payload.Packages[0].ChannelName != "latest" ||
		packagePageTwo.Payload.Pagination.PreviousPageToken == "" {
		t.Fatalf("generated vulnerable packages second page = %#v, %v", packagePageTwo, err)
	}

	summary, err := client.PackerServiceListBucketPackagesVulnerabilitySummary(
		packer_service.NewPackerServiceListBucketPackagesVulnerabilitySummaryParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images"),
		contractAuth,
	)
	if err != nil || len(summary.Payload.PackagesByCriticality) != 2 ||
		len(summary.Payload.ChannelsByCriticality) != 3 || len(summary.Payload.TotalByCriticality) != 2 {
		t.Fatalf("generated vulnerability summary = %#v, %v", summary, err)
	}
	if summary.Payload.PackagesByCriticality[0].Criticality != "critical" ||
		summary.Payload.PackagesByCriticality[0].VulnerabilityCount != "1" ||
		summary.Payload.PackagesByCriticality[1].Criticality != "high" ||
		summary.Payload.PackagesByCriticality[1].VulnerabilityCount != "2" {
		t.Fatalf("generated package criticality counts = %#v", summary.Payload.PackagesByCriticality)
	}
	channels := summary.Payload.ChannelsByCriticality
	if channels[0].ChannelName != "latest" || channels[0].Criticality != "high" ||
		channels[0].VulnerabilityCount != "1" ||
		channels[1].ChannelName != "stable" || channels[1].Criticality != "critical" ||
		channels[1].VulnerabilityCount != "1" ||
		channels[2].ChannelName != "stable" || channels[2].Criticality != "high" ||
		channels[2].VulnerabilityCount != "1" {
		t.Fatalf("generated channel criticality counts = %#v", channels)
	}
	if got := summary.Payload.TotalByCriticality; got[0].Criticality != "critical" ||
		got[0].VulnerabilityCount != "1" || got[1].Criticality != "high" ||
		got[1].VulnerabilityCount != "2" {
		t.Fatalf("generated total criticality counts = %#v", got)
	}

	vulnerabilityPageOne, err := client.PackerServiceListBucketVulnerabilities(
		packer_service.NewPackerServiceListBucketVulnerabilitiesParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithPaginationPageSize(&pageSize),
		contractAuth,
	)
	if err != nil || len(vulnerabilityPageOne.Payload.Vulnerabilities) != 1 ||
		vulnerabilityPageOne.Payload.Pagination.NextPageToken == "" {
		t.Fatalf("generated vulnerabilities first page = %#v, %v", vulnerabilityPageOne, err)
	}
	cveImpact := vulnerabilityPageOne.Payload.Vulnerabilities[0]
	if cveImpact.Vulnerability.Identifier != "CVE-2026-0001" ||
		len(cveImpact.ImpactedPackages) != 1 || len(cveImpact.ImpactedBuilds) != 2 ||
		len(cveImpact.ImpactedChannels) != 2 || cveImpact.ImpactedChannels[0].Name != "latest" ||
		cveImpact.ImpactedChannels[1].Name != "stable" {
		t.Fatalf("generated CVE impacts = %#v", cveImpact)
	}
	vulnerabilityPageTwo, err := client.PackerServiceListBucketVulnerabilities(
		packer_service.NewPackerServiceListBucketVulnerabilitiesParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithPaginationPageSize(&pageSize).
			WithPaginationNextPageToken(&vulnerabilityPageOne.Payload.Pagination.NextPageToken),
		contractAuth,
	)
	if err != nil || len(vulnerabilityPageTwo.Payload.Vulnerabilities) != 1 ||
		vulnerabilityPageTwo.Payload.Vulnerabilities[0].Vulnerability.Identifier != "GHSA-critical" ||
		vulnerabilityPageTwo.Payload.Pagination.PreviousPageToken == "" {
		t.Fatalf("generated vulnerabilities second page = %#v, %v", vulnerabilityPageTwo, err)
	}
}

// Whatever this plane does not serve must still answer a google.rpc.Status
// body the generated client can parse and Packer's errCodeRegex can match —
// http.ServeMux's text/plain 404 satisfies neither (review finding 7).
func TestUnservedOperationsAnswerAParsableStatus(t *testing.T) {
	repository := newContractRepository()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(hcp2023.NewHandlerWithRepository(repository, contractPrincipals{}, contractAuthenticator{}, logger))
	defer server.Close()

	client := cloudclient.NewHTTPClientWithConfig(
		nil,
		cloudclient.DefaultTransportConfig().
			WithHost(strings.TrimPrefix(server.URL, "http://")).
			WithSchemes([]string{"http"}),
	).PackerService

	_, err := client.PackerServiceDeleteVersion(
		packer_service.NewPackerServiceDeleteVersionParams().
			WithLocationOrganizationID(contractOrg).
			WithLocationProjectID(contractProject).
			WithBucketName("images").
			WithFingerprint("fingerprint"),
		contractAuth,
	)
	// The typed default proves the client parsed a JSON status body; the
	// string form is what Packer's regex sees.
	var unserved *packer_service.PackerServiceDeleteVersionDefault
	if !errors.As(err, &unserved) || !unserved.IsCode(http.StatusNotImplemented) {
		t.Fatalf("generated DeleteVersion = %v, want a parsed 501 status", err)
	}
	if unserved.Payload.Code != 12 {
		t.Fatalf("generated DeleteVersion code = %d, want 12", unserved.Payload.Code)
	}
	if !strings.Contains(err.Error(), `"code":12`) {
		t.Fatalf("generated DeleteVersion error text = %v, want a regex-matchable code 12", err)
	}
}

type contractRepository struct {
	buckets        map[string]*store.Bucket
	channels       map[string]*store.Channel
	channelHistory map[registry.ID][]store.ChannelAssignment
	versions       map[string]*registry.Version
	builds         map[string][]store.StoredBuild
	sboms          map[string]store.Sbom
	packages       []store.ReportedPackage
	scanStates     map[string]*store.BuildScanState
	scanRuns       map[string]*store.ScanRun
	scanFindings   map[string][]store.StoredFinding
}

func newContractRepository() *contractRepository {
	return &contractRepository{
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

func (r *contractRepository) DeleteBucket(
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

func (r *contractRepository) UploadSbom(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint, buildID string,
	sbom store.Sbom,
) (*store.Sbom, error) {
	build, err := r.GetBuild(context.Background(), store.Tenant{}, bucket, fingerprint, buildID)
	if err != nil {
		return nil, err
	}
	sbom.BuildID = build.ID
	key := bucket + "/" + fingerprint + "/" + buildID + "/" + sbom.Name
	if existing, ok := r.sboms[key]; ok {
		sbom.ID, sbom.CreatedAt = existing.ID, existing.CreatedAt
	}
	r.sboms[key] = sbom
	return &sbom, nil
}

func (r *contractRepository) ListSboms(
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

func (r *contractRepository) GetSbom(
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

func (r *contractRepository) DownloadSbom(
	ctx context.Context,
	tenant store.Tenant,
	bucket, fingerprint, buildID, name string,
) ([]byte, error) {
	sbom, err := r.GetSbom(ctx, tenant, bucket, fingerprint, buildID, name)
	if err != nil {
		return nil, err
	}
	return sbom.CompressedData, nil
}

func (r *contractRepository) ListBuildPackages(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint, buildID string,
) ([]store.ReportedPackage, []string, error) {
	if _, err := r.GetBuild(context.Background(), store.Tenant{}, bucket, fingerprint, buildID); err != nil {
		return nil, nil, err
	}
	return append([]store.ReportedPackage(nil), r.packages...), nil, nil
}

func (r *contractRepository) GetBuildScanState(
	_ context.Context,
	_ store.Tenant,
	buildID string,
) (*store.BuildScanState, error) {
	return r.scanStates[buildID], nil
}

func (r *contractRepository) GetScanRun(
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

func (r *contractRepository) ListScanFindings(
	_ context.Context,
	_ store.Tenant,
	runID string,
) ([]store.StoredFinding, error) {
	return append([]store.StoredFinding(nil), r.scanFindings[runID]...), nil
}

func (r *contractRepository) CreateBucket(
	_ context.Context,
	_ store.Tenant,
	bucket store.Bucket,
) (*store.Bucket, error) {
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

func (r *contractRepository) GetBucket(
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

func (r *contractRepository) GetBucketWithLatestVersion(
	ctx context.Context,
	tenant store.Tenant,
	name string,
) (*store.Bucket, error) {
	return r.GetBucket(ctx, tenant, name)
}

func (r *contractRepository) ListBuckets(
	_ context.Context,
	_ store.Tenant,
) ([]store.Bucket, error) {
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

func (r *contractRepository) UpdateBucket(
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
	bucket.Description, bucket.Labels, bucket.UpdatedAt = description, labels, at
	return bucket, nil
}

func (r *contractRepository) CreateChannel(
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

func (r *contractRepository) GetChannel(
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

func (r *contractRepository) ListChannels(
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

func (r *contractRepository) UpdateChannel(
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

func (r *contractRepository) AssignChannelVersion(
	_ context.Context,
	_ store.Tenant,
	bucketName, sourceName, targetName, authorID string,
	at time.Time,
) (*store.Channel, *store.Channel, error) {
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

func (r *contractRepository) DeleteChannel(
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

func (r *contractRepository) ListChannelAssignmentHistory(
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

func (r *contractRepository) ListBucketAncestry(
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
			isParent := ancestryType != string(sdkmodels.HashicorpCloudPacker20230101BucketAncestryTypeANCESTRYTYPECHILDREN) &&
				child.BucketName == bucketName && child.Fingerprint == latestFingerprint
			isChild := ancestryType != string(sdkmodels.HashicorpCloudPacker20230101BucketAncestryTypeANCESTRYTYPEPARENTS) &&
				parent.BucketName == bucketName &&
				(channelName == "" || channel != nil && channel.Name == channelName)
			if !isParent && !isChild {
				continue
			}
			relation := store.BucketAncestry{
				Parent: contractAncestryVersion(parent),
				Child:  contractAncestryVersion(child),
			}
			if channel != nil {
				relation.ParentChannelName = channel.Name
				if channel.Version != nil {
					current := contractAncestryVersion(channel.Version)
					relation.ParentChannelVersion = &current
				}
			}
			relations = append(relations, relation)
		}
	}
	// Map iteration order is random per call, but pagination tokens only mean
	// something over a total order. Mirror the real store's ORDER BY
	// (ancestry_repository.go): child bucket, child sequence DESC, child id,
	// parent bucket, parent sequence DESC, parent id, channel name, channel
	// version id — with absent values ordering last, as SQL NULLs do under
	// ascending order.
	sort.Slice(relations, func(i, j int) bool {
		a, b := relations[i], relations[j]
		if a.Child.BucketName != b.Child.BucketName {
			return a.Child.BucketName < b.Child.BucketName
		}
		if a.Child.Sequence != b.Child.Sequence {
			return a.Child.Sequence > b.Child.Sequence
		}
		if a.Child.ID != b.Child.ID {
			return a.Child.ID.String() < b.Child.ID.String()
		}
		if a.Parent.BucketName != b.Parent.BucketName {
			return a.Parent.BucketName < b.Parent.BucketName
		}
		if a.Parent.Sequence != b.Parent.Sequence {
			return a.Parent.Sequence > b.Parent.Sequence
		}
		if a.Parent.ID != b.Parent.ID {
			return a.Parent.ID.String() < b.Parent.ID.String()
		}
		if a.ParentChannelName != b.ParentChannelName {
			if a.ParentChannelName == "" || b.ParentChannelName == "" {
				return b.ParentChannelName == ""
			}
			return a.ParentChannelName < b.ParentChannelName
		}
		aVersion, bVersion := "", ""
		if a.ParentChannelVersion != nil {
			aVersion = a.ParentChannelVersion.ID.String()
		}
		if b.ParentChannelVersion != nil {
			bVersion = b.ParentChannelVersion.ID.String()
		}
		if aVersion != bVersion {
			if aVersion == "" || bVersion == "" {
				return bVersion == ""
			}
			return aVersion < bVersion
		}
		return false
	})
	return relations, nil
}

func (r *contractRepository) versionByID(id string) *registry.Version {
	for _, version := range r.versions {
		if version.ID.String() == id {
			return version
		}
	}
	return nil
}

func (r *contractRepository) channelByID(id string) *store.Channel {
	for _, channel := range r.channels {
		if channel.ID.String() == id {
			return channel
		}
	}
	return nil
}

func contractAncestryVersion(version *registry.Version) store.AncestryVersion {
	sequence, _ := version.Sequence()
	return store.AncestryVersion{
		ID: version.ID, BucketName: version.BucketName,
		Fingerprint: version.Fingerprint, Sequence: sequence,
	}
}

func (r *contractRepository) assignChannel(
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
	channel.UpdatedAt = at
	r.channelHistory[channel.ID] = append(r.channelHistory[channel.ID], store.ChannelAssignment{
		ID: registry.NewID(at), ChannelID: channel.ID, Version: version,
		AuthorID: authorID, AssignedAt: at,
	})
	return nil
}

func (r *contractRepository) CreateVersion(
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

func (r *contractRepository) GetVersion(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint string,
) (*registry.Version, error) {
	version, ok := r.versions[bucket+"/"+fingerprint]
	if !ok {
		return nil, registry.ErrNotFound
	}
	r.projectVersionRelationships(version)
	return version, nil
}

func (r *contractRepository) projectVersionRelationships(version *registry.Version) {
	version.HasDescendants = false
	version.Parents = nil
	for _, builds := range r.builds {
		for i := range builds {
			build := &builds[i]
			if build.ParentVersionID == version.ID.String() {
				version.HasDescendants = true
			}
			if build.VersionID != version.ID || build.ParentVersionID == "" {
				continue
			}
			status := registry.AncestryUndetermined
			if channel := r.channelByID(build.ParentChannelID); channel != nil && channel.Version != nil {
				status = registry.AncestryOutOfDate
				if channel.Version.ID.String() == build.ParentVersionID {
					status = registry.AncestryUpToDate
				}
			}
			if version.Parents == nil || status == registry.AncestryOutOfDate ||
				(status == registry.AncestryUndetermined && version.Parents.Status == registry.AncestryUpToDate) {
				version.Parents = &registry.VersionParents{Status: status}
			}
		}
	}
}

func (r *contractRepository) RevokeVersion(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint string,
	req store.RevocationRequest,
	versionName func(*registry.Version) string,
	at time.Time,
) (*registry.Version, error) {
	version, ok := r.versions[bucket+"/"+fingerprint]
	if !ok {
		return nil, registry.ErrNotFound
	}
	if err := version.Revoke(registry.Revocation{
		RevokeAt: req.RevokeAt, Message: req.Message, Author: req.Author,
	}, at); err != nil {
		return nil, err
	}
	if req.SkipDescendants {
		return version, nil
	}
	// Transitive walk over the build edges, mirroring the store's recursive
	// query; an already-revoked descendant keeps its own record.
	ancestor := registry.RevokedAncestor{
		VersionID: version.ID, BucketName: bucket, Fingerprint: fingerprint,
		VersionName: versionName(version),
	}
	frontier := []string{version.ID.String()}
	for len(frontier) > 0 {
		parentID := frontier[0]
		frontier = frontier[1:]
		for key, builds := range r.builds {
			descendant := r.versions[key]
			if descendant == nil || descendant.Revocation() != nil {
				continue
			}
			for i := range builds {
				if builds[i].ParentVersionID != parentID {
					continue
				}
				if err := descendant.Revoke(registry.Revocation{
					RevokeAt: req.RevokeAt, Message: req.Message, Author: req.Author,
					InheritedFrom: &ancestor,
				}, at); err != nil {
					return nil, err
				}
				frontier = append(frontier, descendant.ID.String())
				break
			}
		}
	}
	return version, nil
}

func (r *contractRepository) RestoreRevokedVersion(
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
	for _, descendant := range r.versions {
		revocation := descendant.Revocation()
		if revocation == nil || revocation.InheritedFrom == nil ||
			revocation.InheritedFrom.VersionID != version.ID {
			continue
		}
		sequence, _ := descendant.Sequence()
		state := *descendant
		state.UpdatedAt = at
		restored, err := registry.RestoreVersion(state, descendant.Complete(), sequence, nil)
		if err != nil {
			return nil, err
		}
		*descendant = *restored
	}
	return version, nil
}

func (r *contractRepository) ListVersions(
	_ context.Context,
	_ store.Tenant,
	bucket string,
) ([]*registry.Version, error) {
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

func (r *contractRepository) CreateBuild(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint string,
	templateType registry.TemplateType,
	build store.StoredBuild,
	versionName func(*registry.Version) string,
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
	build.VersionID, build.UpdatedAt = version.ID, build.CreatedAt
	r.builds[key] = append(r.builds[key], build)
	r.inheritParentRevocation(version, build.ParentVersionID, versionName, build.CreatedAt)
	return &r.builds[key][len(r.builds[key])-1], nil
}

func (r *contractRepository) ListBuilds(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint string,
) ([]store.StoredBuild, error) {
	if _, err := r.GetVersion(context.Background(), store.Tenant{}, bucket, fingerprint); err != nil {
		return nil, err
	}
	return r.builds[bucket+"/"+fingerprint], nil
}

func (r *contractRepository) GetBuild(
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

func (r *contractRepository) UpdateBuild(
	_ context.Context,
	_ store.Tenant,
	bucket, fingerprint string,
	build store.StoredBuild,
	versionName func(*registry.Version) string,
	at time.Time,
) (*store.StoredBuild, error) {
	key := bucket + "/" + fingerprint
	for i := range r.builds[key] {
		old := r.builds[key][i]
		if old.ID != build.ID {
			continue
		}
		if old.Build != build.Build ||
			old.PackerRunUUID != build.PackerRunUUID ||
			!bytes.Equal(old.Metadata, build.Metadata) ||
			old.SourceExternalIdentifier != build.SourceExternalIdentifier ||
			old.ParentVersionID != build.ParentVersionID ||
			old.ParentChannelID != build.ParentChannelID {
			build.UpdatedAt = at
		}
		r.builds[key][i] = build
		r.inheritParentRevocation(r.versions[key], build.ParentVersionID, versionName, at)
		r.completeVersion(key, bucket, at)
		return &r.builds[key][i], nil
	}
	return nil, errors.New("build missing")
}

func (r *contractRepository) inheritParentRevocation(
	child *registry.Version,
	parentVersionID string,
	versionName func(*registry.Version) string,
	at time.Time,
) {
	if parentVersionID == "" || parentVersionID == child.ID.String() || child.Revocation() != nil {
		return
	}
	for _, parent := range r.versions {
		if parent.ID.String() != parentVersionID || parent.Revocation() == nil {
			continue
		}
		revocation := parent.Revocation()
		if err := child.Revoke(registry.Revocation{
			RevokeAt: revocation.RevokeAt, Message: revocation.Message, Author: revocation.Author,
			InheritedFrom: &registry.RevokedAncestor{
				VersionID: parent.ID, BucketName: parent.BucketName, Fingerprint: parent.Fingerprint,
				VersionName: versionName(parent),
			},
		}, at); err != nil {
			panic("contract fake record-time inheritance: " + err.Error())
		}
		return
	}
}

// completeVersion mirrors the real store: a version whose builds have all
// succeeded and reported metadata is completed at UpdateBuild, taking the next
// per-bucket sequence. Without this the fake could never produce an assignable
// version and the channel-assignment contract would be untestable.
func (r *contractRepository) completeVersion(key, bucket string, at time.Time) {
	version := r.versions[key]
	domainBuilds := make([]registry.Build, len(r.builds[key]))
	for i := range r.builds[key] {
		domainBuilds[i] = r.builds[key][i].Build
	}
	version.Builds = domainBuilds
	if version.Complete() || !version.ReadyToComplete() {
		return
	}
	sequence := 1
	for versionKey, other := range r.versions {
		if !strings.HasPrefix(versionKey, bucket+"/") {
			continue
		}
		if existing, ok := other.Sequence(); ok && existing >= sequence {
			sequence = existing + 1
		}
	}
	if err := version.MarkComplete(sequence, at); err != nil {
		panic("contract fake completion: " + err.Error())
	}
	if revocation := version.Revocation(); revocation != nil && !revocation.RevokeAt.After(at) {
		return
	}
	// Mirrors the real store: completion assigns the version to the bucket's
	// managed "latest" channel with no client call, in the same instant
	// (Appendix A probes 13-14).
	if latest, ok := r.channels[bucket+"/latest"]; ok && latest.Managed {
		latest.Version = version
		latest.UpdatedAt = at
		r.channelHistory[latest.ID] = append(r.channelHistory[latest.ID], store.ChannelAssignment{
			ID: registry.NewID(at), ChannelID: latest.ID, Version: version,
			AuthorID: "Dufflebag", AssignedAt: at,
		})
	}
}

// A token names the credential it was minted from, and the principal must still
// hold it or the request is refused as revoked (review finding 14).
const contractSecretID = "s-contract"

func contractSecrets() []identity.Secret {
	secret, err := identity.RestoreSecret(
		contractSecretID,
		"$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHQ$aGFzaGhhc2g",
		time.Unix(0, 0).UTC(), nil, nil,
	)
	if err != nil {
		panic("contract secret: " + err.Error())
	}
	return []identity.Secret{secret}
}
