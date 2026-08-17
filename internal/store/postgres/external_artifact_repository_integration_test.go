//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

func TestSearchExternalArtifactsFiltersRevocationAndTenant(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	tenantA := store.ParseTenant(orgA, projectA)
	tenantB := store.ParseTenant(orgB, projectB)
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	bucket, err := repository.CreateBucket(ctx, tenantA, store.Bucket{
		ID: registry.NewID(at), Name: "artifact-search", Labels: map[string]string{"team": "platform"},
		CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	type seeded struct {
		fingerprint, platform, region string
		version                       *registry.Version
		build                         *store.StoredBuild
	}
	seeds := []seeded{
		{fingerprint: "older", platform: "aws", region: "eu-west-1"},
		{fingerprint: "newer", platform: "gcp", region: "europe-west1"},
	}
	for i := range seeds {
		createdAt := at.Add(time.Duration(i+1) * time.Hour)
		version, err := registry.NewVersion(
			registry.NewID(createdAt), bucket.Name, seeds[i].fingerprint,
			registry.TemplateHCL2, createdAt,
		)
		if err != nil {
			t.Fatalf("NewVersion %s: %v", seeds[i].fingerprint, err)
		}
		version.AuthorID = "p-builder"
		if _, err := repository.CreateVersion(ctx, tenantA, version); err != nil {
			t.Fatalf("CreateVersion %s: %v", seeds[i].fingerprint, err)
		}
		build, err := repository.CreateBuild(
			ctx, tenantA, bucket.Name, version.Fingerprint, registry.TemplateHCL2,
			store.StoredBuild{
				Build: registry.Build{
					ID: registry.NewID(createdAt.Add(time.Minute)), ComponentType: "image",
					Status: registry.BuildRunning, Platform: seeds[i].platform,
				},
				PackerRunUUID: "run-" + seeds[i].fingerprint,
				Labels:        map[string]string{"arch": "amd64"},
				Artifacts: []store.Artifact{{
					ID:                 registry.NewID(createdAt.Add(2 * time.Minute)),
					ExternalIdentifier: "shared-digest", Region: seeds[i].region,
					CreatedAt: createdAt.Add(2 * time.Minute),
				}},
				CreatedAt: createdAt.Add(time.Minute),
			},
			testVersionName,
		)
		if err != nil {
			t.Fatalf("CreateBuild %s: %v", seeds[i].fingerprint, err)
		}
		build.Status = registry.BuildDone
		build.MetadataSeen = true
		build.Metadata = []byte(`{}`)
		build, err = repository.UpdateBuild(
			ctx, tenantA, bucket.Name, version.Fingerprint, *build,
			testVersionName, createdAt.Add(3*time.Minute),
		)
		if err != nil {
			t.Fatalf("UpdateBuild %s: %v", seeds[i].fingerprint, err)
		}
		seeds[i].version, err = repository.GetVersion(ctx, tenantA, bucket.Name, version.Fingerprint)
		if err != nil {
			t.Fatalf("GetVersion %s: %v", seeds[i].fingerprint, err)
		}
		seeds[i].build = build
	}

	revokedAt := at.Add(-time.Hour)
	if _, err := repository.RevokeVersion(ctx, tenantA, bucket.Name, "newer", store.RevocationRequest{
		RevokeAt: revokedAt, Message: "retired", Author: "security", DisableRollbackChannels: true,
	}, testVersionName, at); err != nil {
		t.Fatalf("RevokeVersion: %v", err)
	}

	matches, err := repository.SearchExternalArtifacts(ctx, tenantA, "shared-digest", "", "")
	if err != nil {
		t.Fatalf("SearchExternalArtifacts: %v", err)
	}
	if len(matches) != 2 || matches[0].Version.Fingerprint != "newer" ||
		matches[0].Version.Revocation() == nil || matches[1].Version.Fingerprint != "older" {
		t.Fatalf("unfiltered matches = %#v, want newest revoked then older", matches)
	}
	if matches[0].Bucket.Name != bucket.Name || matches[0].Bucket.Labels["team"] != "platform" ||
		matches[0].Build.ID != seeds[1].build.ID || len(matches[0].Build.Artifacts) != 1 {
		t.Fatalf("restored aggregate = %#v", matches[0])
	}

	filtered, err := repository.SearchExternalArtifacts(ctx, tenantA, "shared-digest", "aws", "")
	if err != nil || len(filtered) != 1 || filtered[0].Version.Fingerprint != "older" {
		t.Fatalf("platform include = %#v, %v", filtered, err)
	}
	filtered, err = repository.SearchExternalArtifacts(ctx, tenantA, "shared-digest", "azure", "")
	if err != nil || len(filtered) != 0 {
		t.Fatalf("platform exclude = %#v, %v", filtered, err)
	}
	filtered, err = repository.SearchExternalArtifacts(ctx, tenantA, "shared-digest", "", "europe-west1")
	if err != nil || len(filtered) != 1 || filtered[0].Version.Fingerprint != "newer" {
		t.Fatalf("region include = %#v, %v", filtered, err)
	}
	filtered, err = repository.SearchExternalArtifacts(ctx, tenantA, "shared-digest", "", "us-east-1")
	if err != nil || len(filtered) != 0 {
		t.Fatalf("region exclude = %#v, %v", filtered, err)
	}

	isolated, err := repository.SearchExternalArtifacts(ctx, tenantB, "shared-digest", "", "")
	if err != nil || len(isolated) != 0 {
		t.Fatalf("tenant B search of tenant A digest = %#v, %v; want empty", isolated, err)
	}
}
