//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/store/objectstore"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/golang-migrate/migrate/v4"
	migratepostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
)

func TestRepositoryRoundTripsRegistryAggregate(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	createdAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

	bucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID:          registry.NewID(createdAt),
		Name:        "base-images",
		Description: "base images",
		Labels:      map[string]string{"team": "platform"},
		CreatedAt:   createdAt,
	})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	gotBucket, err := repository.GetBucket(ctx, tenant, bucket.Name)
	if err != nil {
		t.Fatalf("GetBucket: %v", err)
	}
	if gotBucket.ID != bucket.ID || gotBucket.Description != bucket.Description ||
		gotBucket.Labels["team"] != "platform" {
		t.Fatalf("bucket round trip = %#v, want %#v", gotBucket, bucket)
	}

	version, err := registry.NewVersion(
		registry.NewID(createdAt.Add(time.Second)),
		bucket.Name,
		"fingerprint-1",
		registry.TemplateHCL2,
		createdAt.Add(time.Second),
	)
	if err != nil {
		t.Fatalf("NewVersion: %v", err)
	}
	version.AuthorID = "p-builder"
	createdVersion, err := repository.CreateVersion(ctx, tenant, version)
	if err != nil {
		t.Fatalf("CreateVersion: %v", err)
	}
	gotVersion, err := repository.GetVersion(ctx, tenant, bucket.Name, version.Fingerprint)
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if gotVersion.ID != createdVersion.ID || gotVersion.Complete() || gotVersion.AuthorID != "p-builder" {
		t.Fatalf("version round trip = %#v", gotVersion)
	}

	buildAt := createdAt.Add(2 * time.Second)
	build, err := repository.CreateBuild(
		ctx,
		tenant,
		bucket.Name,
		version.Fingerprint,
		registry.TemplateHCL2,
		store.StoredBuild{
			Build: registry.Build{
				ID:            registry.NewID(buildAt),
				ComponentType: "amazon-ebs",
				Status:        registry.BuildRunning,
				Platform:      "aws",
			},
			PackerRunUUID: "run-1",
			Labels:        map[string]string{"region": "eu-west-2"},
			Artifacts: []store.Artifact{{
				ID:                 registry.NewID(buildAt.Add(time.Millisecond)),
				ExternalIdentifier: "ami-123",
				Region:             "eu-west-2",
				CreatedAt:          buildAt,
			}, {
				ID:                 registry.NewID(buildAt.Add(2 * time.Millisecond)),
				ExternalIdentifier: "ami-456",
				Region:             "eu-west-1",
				CreatedAt:          buildAt.Add(2 * time.Millisecond),
			}},
			CreatedAt: buildAt,
		},
	)
	if err != nil {
		t.Fatalf("CreateBuild: %v", err)
	}
	newerBuild, err := repository.CreateBuild(
		ctx,
		tenant,
		bucket.Name,
		version.Fingerprint,
		registry.TemplateHCL2,
		store.StoredBuild{
			Build: registry.Build{
				ID: registry.NewID(buildAt.Add(3 * time.Second)), ComponentType: "docker",
				Status: registry.BuildRunning, Platform: "docker",
			},
			PackerRunUUID: "run-2", Labels: map[string]string{}, CreatedAt: buildAt.Add(3 * time.Second),
		},
	)
	if err != nil {
		t.Fatalf("CreateBuild newer: %v", err)
	}
	builds, err := repository.ListBuilds(ctx, tenant, bucket.Name, version.Fingerprint)
	if err != nil {
		t.Fatalf("ListBuilds: %v", err)
	}
	if len(builds) != 2 || builds[0].ID != newerBuild.ID || builds[1].ID != build.ID ||
		len(builds[1].Artifacts) != 2 ||
		builds[1].Artifacts[0].ExternalIdentifier != "ami-456" ||
		builds[1].Artifacts[1].ExternalIdentifier != "ami-123" {
		t.Fatalf("build aggregate round trip = %#v", builds)
	}
}

func TestRepositoryPersistsAndProjectsBuildAncestry(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	parentBucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "base", Labels: map[string]string{}, CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	parentVersion, err := registry.RestoreVersion(registry.Version{
		ID: registry.NewID(at.Add(time.Second)), BucketName: parentBucket.Name,
		Fingerprint: "base-fingerprint", TemplateType: registry.TemplateHCL2,
		AuthorID:  "p-parent",
		CreatedAt: at.Add(time.Second), UpdatedAt: at.Add(time.Second),
	}, true, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateVersion(ctx, tenant, parentVersion); err != nil {
		t.Fatal(err)
	}
	parentChannel, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(2 * time.Second)), BucketName: parentBucket.Name,
		Name: "production", CreatedAt: at.Add(2 * time.Second),
	}, parentVersion.Fingerprint, "p-test")
	if err != nil {
		t.Fatal(err)
	}

	childBucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at.Add(3 * time.Second)), Name: "derived",
		Labels: map[string]string{}, CreatedAt: at.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	childVersion, err := registry.NewVersion(
		registry.NewID(at.Add(4*time.Second)), childBucket.Name, "derived-fingerprint",
		registry.TemplateHCL2, at.Add(4*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	childVersion.AuthorID = "p-child"
	if _, err := repository.CreateVersion(ctx, tenant, childVersion); err != nil {
		t.Fatal(err)
	}
	childBuild, err := repository.CreateBuild(
		ctx, tenant, childBucket.Name, childVersion.Fingerprint, registry.TemplateHCL2,
		store.StoredBuild{
			Build: registry.Build{
				ID: registry.NewID(at.Add(5 * time.Second)), ComponentType: "docker",
				Status: registry.BuildRunning, Platform: "docker",
			},
			Labels: map[string]string{}, CreatedAt: at.Add(5 * time.Second),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	childBuild.Status = registry.BuildDone
	childBuild.MetadataSeen = true
	childBuild.Metadata = json.RawMessage(`{"packer":{"os":{"arch":"arm64","type":"darwin"}}}`)
	childBuild.ParentVersionID = parentVersion.ID.String()
	childBuild.ParentChannelID = parentChannel.ID.String()
	updated, err := repository.UpdateBuild(
		ctx, tenant, childBucket.Name, childVersion.Fingerprint, *childBuild, at.Add(6*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ParentVersionID != parentVersion.ID.String() ||
		updated.ParentChannelID != parentChannel.ID.String() {
		t.Fatalf("updated build parent ids = %q %q", updated.ParentVersionID, updated.ParentChannelID)
	}
	fetched, err := repository.GetBuild(
		ctx, tenant, childBucket.Name, childVersion.Fingerprint, childBuild.ID.String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.ParentVersionID != parentVersion.ID.String() ||
		fetched.ParentChannelID != parentChannel.ID.String() {
		t.Fatalf("persisted build parent ids = %q %q", fetched.ParentVersionID, fetched.ParentChannelID)
	}
	var metadata map[string]any
	if err := json.Unmarshal(fetched.Metadata, &metadata); err != nil {
		t.Fatalf("persisted build metadata: %v", err)
	}
	packer, ok := metadata["packer"].(map[string]any)
	if !ok || packer["os"] == nil {
		t.Fatalf("persisted build metadata = %#v", metadata)
	}

	projectedParent, err := repository.GetVersion(
		ctx, tenant, parentBucket.Name, parentVersion.Fingerprint,
	)
	if err != nil {
		t.Fatal(err)
	}
	projectedChild, err := repository.GetVersion(
		ctx, tenant, childBucket.Name, childVersion.Fingerprint,
	)
	if err != nil {
		t.Fatal(err)
	}
	if projectedParent.AuthorID != "p-parent" || !projectedParent.HasDescendants {
		t.Fatalf("parent version projection = %#v", projectedParent)
	}
	if projectedChild.AuthorID != "p-child" || projectedChild.Parents == nil ||
		projectedChild.Parents.Status != registry.AncestryUpToDate {
		t.Fatalf("child version projection = %#v", projectedChild)
	}

	children, err := repository.ListBucketAncestry(
		ctx, tenant, parentBucket.Name, "ANCESTRY_TYPE_CHILDREN", "production", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].Parent.ID != parentVersion.ID ||
		children[0].ParentChannelName != "production" ||
		children[0].ParentChannelVersion == nil ||
		children[0].ParentChannelVersion.ID != parentVersion.ID ||
		children[0].Child.ID != childVersion.ID {
		t.Fatalf("children ancestry = %#v", children)
	}
	parents, err := repository.ListBucketAncestry(
		ctx, tenant, childBucket.Name, "ANCESTRY_TYPE_PARENTS", "", childVersion.Fingerprint,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(parents) != 1 || parents[0].Parent.ID != parentVersion.ID ||
		parents[0].Child.ID != childVersion.ID {
		t.Fatalf("parents ancestry = %#v", parents)
	}

	parentVersion2, err := registry.RestoreVersion(registry.Version{
		ID: registry.NewID(at.Add(7 * time.Second)), BucketName: parentBucket.Name,
		Fingerprint: "base-fingerprint-2", TemplateType: registry.TemplateHCL2,
		CreatedAt: at.Add(7 * time.Second), UpdatedAt: at.Add(7 * time.Second),
	}, true, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateVersion(ctx, tenant, parentVersion2); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateChannel(
		ctx, tenant, parentBucket.Name, parentChannel.Name, false, false,
		true, parentVersion2.Fingerprint, "p-test", at.Add(8*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	childVersion2, err := registry.RestoreVersion(registry.Version{
		ID: registry.NewID(at.Add(9 * time.Second)), BucketName: childBucket.Name,
		Fingerprint: "derived-fingerprint-2", TemplateType: registry.TemplateHCL2,
		CreatedAt: at.Add(9 * time.Second), UpdatedAt: at.Add(9 * time.Second),
	}, true, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateVersion(ctx, tenant, childVersion2); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateBuild(
		ctx, tenant, childBucket.Name, childVersion2.Fingerprint, registry.TemplateHCL2,
		store.StoredBuild{
			Build: registry.Build{
				ID: registry.NewID(at.Add(10 * time.Second)), ComponentType: "docker",
				Status: registry.BuildDone, Platform: "docker", MetadataSeen: true,
			},
			ParentVersionID: parentVersion2.ID.String(),
			ParentChannelID: parentChannel.ID.String(),
			Labels:          map[string]string{},
			CreatedAt:       at.Add(10 * time.Second),
		},
	); err != nil {
		t.Fatal(err)
	}
	stableChannel, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(10*time.Second + time.Millisecond)), BucketName: parentBucket.Name,
		Name: "stable", CreatedAt: at.Add(10*time.Second + time.Millisecond),
	}, parentVersion2.Fingerprint, "p-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateBuild(
		ctx, tenant, childBucket.Name, childVersion2.Fingerprint, registry.TemplateHCL2,
		store.StoredBuild{
			Build: registry.Build{
				ID:            registry.NewID(at.Add(10*time.Second + 2*time.Millisecond)),
				ComponentType: "docker-copy", Status: registry.BuildDone,
				Platform: "docker", MetadataSeen: true,
			},
			ParentVersionID: parentVersion2.ID.String(),
			ParentChannelID: stableChannel.ID.String(),
			Labels:          map[string]string{},
			CreatedAt:       at.Add(10*time.Second + 2*time.Millisecond),
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at.Add(11 * time.Second)), Name: "empty",
		Labels: map[string]string{}, CreatedAt: at.Add(11 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	buckets, err := repository.ListBuckets(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 3 {
		t.Fatalf("ListBuckets returned %d buckets, want 3", len(buckets))
	}
	byName := make(map[string]*store.Bucket, len(buckets))
	for i := range buckets {
		byName[buckets[i].Name] = &buckets[i]
	}
	projectedBase := byName[parentBucket.Name]
	if projectedBase.LatestVersion == nil ||
		projectedBase.LatestVersion.Fingerprint != parentVersion2.Fingerprint ||
		!projectedBase.LatestVersion.HasDescendants ||
		projectedBase.LatestVersion.Parents != nil || projectedBase.ChildrenStatus == nil ||
		*projectedBase.ChildrenStatus != registry.AncestryUpToDate {
		t.Fatalf("current base bucket ancestry = %#v, want only UP_TO_DATE children", projectedBase)
	}
	projectedDerived := byName[childBucket.Name]
	if projectedDerived.LatestVersion == nil ||
		projectedDerived.LatestVersion.Fingerprint != childVersion2.Fingerprint ||
		projectedDerived.LatestVersion.Parents == nil ||
		projectedDerived.LatestVersion.Parents.Status != registry.AncestryUpToDate ||
		projectedDerived.ChildrenStatus != nil {
		t.Fatalf("current derived bucket ancestry = %#v, want only UP_TO_DATE parents", projectedDerived)
	}
	projectedEmpty := byName["empty"]
	if projectedEmpty.LatestVersion != nil || projectedEmpty.ChildrenStatus != nil {
		t.Fatalf("empty bucket ancestry = %#v, want absent", projectedEmpty)
	}

	if _, err := repository.UpdateChannel(
		ctx, tenant, parentBucket.Name, parentChannel.Name, false, false,
		true, parentVersion.Fingerprint, "p-test", at.Add(12*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	staleBuckets, err := repository.ListBuckets(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	staleByName := make(map[string]*store.Bucket, len(staleBuckets))
	for i := range staleBuckets {
		staleByName[staleBuckets[i].Name] = &staleBuckets[i]
	}
	if got := staleByName[parentBucket.Name].ChildrenStatus; got == nil || *got != registry.AncestryOutOfDate {
		t.Fatalf("mixed current child statuses = %v, want OUT_OF_DATE", got)
	}
	if got := staleByName[childBucket.Name].LatestVersion.Parents; got == nil || got.Status != registry.AncestryOutOfDate {
		t.Fatalf("mixed current parent statuses = %#v, want OUT_OF_DATE", got)
	}

	if _, err := repository.UpdateChannel(
		ctx, tenant, parentBucket.Name, parentChannel.Name, false, false,
		true, "", "p-test", at.Add(13*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	undeterminedBuckets, err := repository.ListBuckets(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	undeterminedByName := make(map[string]*store.Bucket, len(undeterminedBuckets))
	for i := range undeterminedBuckets {
		undeterminedByName[undeterminedBuckets[i].Name] = &undeterminedBuckets[i]
	}
	if got := undeterminedByName[parentBucket.Name].ChildrenStatus; got == nil || *got != registry.AncestryUndetermined {
		t.Fatalf("current child with an unassigned channel = %v, want UNDETERMINED", got)
	}
	if got := undeterminedByName[childBucket.Name].LatestVersion.Parents; got == nil || got.Status != registry.AncestryUndetermined {
		t.Fatalf("current parent with an unassigned channel = %#v, want UNDETERMINED", got)
	}
}

func TestRepositoryCreateSequenceIsIdempotent(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	bucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "images", Labels: map[string]string{}, CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := registry.RestoreVersion(registry.Version{
		ID:           registry.NewID(at.Add(time.Second)),
		BucketName:   bucket.Name,
		Fingerprint:  "same-fingerprint",
		TemplateType: registry.TemplateHCL2,
		CreatedAt:    at.Add(time.Second),
		UpdatedAt:    at.Add(time.Second),
	}, true, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	gotFirst, err := repository.CreateVersion(ctx, tenant, first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateBuild(
		ctx, tenant, bucket.Name, first.Fingerprint, registry.TemplateJSON,
		store.StoredBuild{
			Build: registry.Build{
				ID: registry.NewID(at.Add(3 * time.Second)), ComponentType: "mismatched", Status: registry.BuildPending,
			},
			Labels: map[string]string{}, CreatedAt: at,
		},
	); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("CreateBuild template mismatch = %v, want ErrConflict", err)
	}

	firstBuild, err := repository.CreateBuild(
		ctx, tenant, bucket.Name, first.Fingerprint, registry.TemplateHCL2,
		store.StoredBuild{
			Build: registry.Build{
				ID: registry.NewID(at.Add(3 * time.Second)), ComponentType: "docker", Status: registry.BuildPending,
			},
			Labels: map[string]string{}, CreatedAt: at,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	second, err := registry.RestoreVersion(registry.Version{
		ID:           registry.NewID(at.Add(4 * time.Second)),
		BucketName:   bucket.Name,
		Fingerprint:  first.Fingerprint,
		TemplateType: registry.TemplateHCL2,
		CreatedAt:    at.Add(time.Minute),
		UpdatedAt:    at.Add(time.Minute),
	}, true, 99, nil)
	if err != nil {
		t.Fatal(err)
	}
	gotSecond, err := repository.CreateVersion(ctx, tenant, second)
	if err != nil {
		t.Fatalf("replayed CreateVersion: %v", err)
	}
	secondBuild, err := repository.CreateBuild(
		ctx, tenant, bucket.Name, first.Fingerprint, registry.TemplateHCL2,
		store.StoredBuild{
			Build: registry.Build{
				ID: registry.NewID(at.Add(5 * time.Second)), ComponentType: "docker", Status: registry.BuildPending,
			},
			Labels: map[string]string{}, CreatedAt: at.Add(time.Minute),
		},
	)
	if err != nil {
		t.Fatalf("replayed CreateBuild: %v", err)
	}
	if gotSecond.ID != gotFirst.ID {
		t.Fatalf("CreateVersion returned new id %s, want existing %s", gotSecond.ID, gotFirst.ID)
	}
	if !gotSecond.CreatedAt.Equal(gotFirst.CreatedAt) {
		t.Fatalf("CreateVersion changed created_at from %v to %v", gotFirst.CreatedAt, gotSecond.CreatedAt)
	}
	firstSequence, firstComplete := gotFirst.Sequence()
	secondSequence, secondComplete := gotSecond.Sequence()
	if !firstComplete || !secondComplete || secondSequence != firstSequence {
		t.Fatalf(
			"CreateVersion changed sequence from %d,%v to %d,%v",
			firstSequence,
			firstComplete,
			secondSequence,
			secondComplete,
		)
	}
	if secondBuild.ID != firstBuild.ID {
		t.Fatalf("CreateBuild returned new id %s, want existing %s", secondBuild.ID, firstBuild.ID)
	}
	if !secondBuild.CreatedAt.Equal(firstBuild.CreatedAt) {
		t.Fatalf("CreateBuild returned a replacement: first=%#v second=%#v", firstBuild, secondBuild)
	}
	versions, err := repository.ListVersions(ctx, tenant, bucket.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 {
		t.Fatalf("versions after replay = %d, want 1", len(versions))
	}
	builds, err := repository.ListBuilds(ctx, tenant, bucket.Name, first.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if len(builds) != 1 {
		t.Fatalf("builds after replay = %d, want 1", len(builds))
	}
}

func TestRepositoryConcurrentCreateSequenceIsIdempotent(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	bucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "concurrent-images", Labels: map[string]string{}, CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 12
	components := []string{"amazon-ebs", "docker", "googlecompute"}
	versionStart := make(chan struct{})
	buildStarts := make([]chan struct{}, len(components))
	for i := range buildStarts {
		buildStarts[i] = make(chan struct{})
	}
	lookupResults := make(chan error, workers)
	type versionResult struct {
		worker  int
		version *registry.Version
		err     error
	}
	versionResults := make(chan versionResult, workers)
	type buildResult struct {
		worker    int
		component string
		build     *store.StoredBuild
		err       error
	}
	buildResults := make([]chan buildResult, len(components))
	for i := range buildResults {
		buildResults[i] = make(chan buildResult, workers)
	}

	for worker := range workers {
		go func() {
			_, err := repository.GetVersion(ctx, tenant, bucket.Name, "concurrent-fingerprint")
			if errors.Is(err, registry.ErrNotFound) {
				err = nil
			}
			lookupResults <- err
			<-versionStart

			workerAt := at.Add(time.Duration(worker+1) * time.Second)
			version, versionErr := registry.NewVersion(
				registry.NewID(workerAt),
				bucket.Name,
				"concurrent-fingerprint",
				registry.TemplateHCL2,
				workerAt,
			)
			if versionErr == nil {
				version, versionErr = repository.CreateVersion(ctx, tenant, version)
			}
			versionResults <- versionResult{worker: worker, version: version, err: versionErr}

			for componentIndex, component := range components {
				<-buildStarts[componentIndex]
				build, buildErr := repository.CreateBuild(
					ctx,
					tenant,
					bucket.Name,
					"concurrent-fingerprint",
					registry.TemplateHCL2,
					store.StoredBuild{
						Build: registry.Build{
							ID:            registry.NewID(workerAt.Add(time.Duration(componentIndex+1) * time.Minute)),
							ComponentType: component,
							Status:        registry.BuildPending,
						},
						Labels:    map[string]string{},
						CreatedAt: workerAt,
					},
				)
				buildResults[componentIndex] <- buildResult{
					worker: worker, component: component, build: build, err: buildErr,
				}
			}
		}()
	}

	var failures []string
	for range workers {
		if err := <-lookupResults; err != nil {
			failures = append(failures, "GetVersion: "+err.Error())
		}
	}
	close(versionStart)

	var versionID registry.ID
	for range workers {
		result := <-versionResults
		if result.err != nil {
			failures = append(failures, "CreateVersion: "+result.err.Error())
			continue
		}
		if result.version == nil {
			failures = append(failures, "CreateVersion returned a nil version")
			continue
		}
		if versionID == "" {
			versionID = result.version.ID
		} else if result.version.ID != versionID {
			failures = append(failures, "CreateVersion returned different identifiers")
		}
	}

	buildIDs := make(map[string]registry.ID, len(components))
	for componentIndex, component := range components {
		close(buildStarts[componentIndex])
		for range workers {
			result := <-buildResults[componentIndex]
			if result.err != nil {
				failures = append(failures, "CreateBuild "+result.component+": "+result.err.Error())
				continue
			}
			if result.build == nil {
				failures = append(failures, "CreateBuild "+result.component+" returned a nil build")
				continue
			}
			if buildIDs[component] == "" {
				buildIDs[component] = result.build.ID
			} else if result.build.ID != buildIDs[component] {
				failures = append(failures, "CreateBuild "+component+" returned different identifiers")
			}
		}
	}
	for _, failure := range failures {
		t.Error(failure)
	}

	versions, err := repository.ListVersions(ctx, tenant, bucket.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 {
		t.Fatalf("versions after concurrent creates = %d, want 1", len(versions))
	}
	builds, err := repository.ListBuilds(ctx, tenant, bucket.Name, "concurrent-fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	if len(builds) != len(components) {
		t.Fatalf("builds after concurrent creates = %d, want %d", len(builds), len(components))
	}
	for _, build := range builds {
		if build.ID != buildIDs[build.ComponentType] {
			t.Errorf("persisted %s build id = %s, returned %s", build.ComponentType, build.ID, buildIDs[build.ComponentType])
		}
	}
}

func TestRepositoryRejectsPersistedInvalidVersionInvariant(t *testing.T) {
	db, databaseURL, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()

	adminURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminURL.User = url.UserPassword("postgres", "postgres")
	admin, err := sql.Open("pgx", adminURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()

	if _, err := admin.ExecContext(ctx, "ALTER TABLE versions DROP CONSTRAINT versions_check"); err != nil {
		t.Fatalf("drop persisted invariant for corruption test: %v", err)
	}
	at := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	bucketID := registry.NewID(at)
	versionID := registry.NewID(at.Add(time.Second))
	if _, err := admin.ExecContext(ctx, `
		INSERT INTO buckets
			(organization_id, project_id, id, name, created_at, updated_at)
		VALUES ($1, $2, $3, 'corrupt', $4, $4)
	`, orgA, projectA, bucketID.String(), at); err != nil {
		t.Fatalf("insert corrupt-test bucket: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `
		INSERT INTO versions
			(organization_id, project_id, id, bucket_id, fingerprint, template_type,
			 complete, sequence, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'bad', 'HCL2', false, 7, $5, $5)
	`, orgA, projectA, versionID.String(), bucketID.String(), at); err != nil {
		t.Fatalf("insert corrupted version: %v", err)
	}

	_, err = store.NewRepository(db).GetVersion(
		ctx,
		store.ParseTenant(orgA, projectA),
		"corrupt",
		"bad",
	)
	if !errors.Is(err, registry.ErrInvalid) {
		t.Fatalf("GetVersion should reject RestoreVersion invariant violation, got %v", err)
	}
}

func TestRepositoryUpdateBuildHeartbeatIsNotAStateTransition(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

	bucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "heartbeats", Labels: map[string]string{}, CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	version, err := registry.NewVersion(
		registry.NewID(at.Add(time.Second)), bucket.Name, "fp", registry.TemplateHCL2, at,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateVersion(ctx, tenant, version); err != nil {
		t.Fatal(err)
	}
	build, err := repository.CreateBuild(
		ctx, tenant, bucket.Name, version.Fingerprint, registry.TemplateHCL2,
		store.StoredBuild{
			Build: registry.Build{
				ID: registry.NewID(at.Add(2 * time.Second)), ComponentType: "docker", Status: registry.BuildRunning,
			},
			Labels: map[string]string{}, CreatedAt: at.Add(2 * time.Second),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	heartbeatAt := at.Add(time.Hour)
	afterHeartbeat, err := repository.UpdateBuild(
		ctx, tenant, bucket.Name, version.Fingerprint, *build, heartbeatAt,
	)
	if err != nil {
		t.Fatalf("heartbeat UpdateBuild: %v", err)
	}
	if !afterHeartbeat.UpdatedAt.Equal(build.UpdatedAt) {
		t.Fatalf("heartbeat changed updated_at from %v to %v", build.UpdatedAt, afterHeartbeat.UpdatedAt)
	}
	gotVersion, err := repository.GetVersion(ctx, tenant, bucket.Name, version.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if gotVersion.Complete() {
		t.Fatal("running heartbeat completed the version")
	}

	completedBuild := *afterHeartbeat
	completedBuild.Status = registry.BuildDone
	completedBuild.MetadataSeen = true
	completedAt := heartbeatAt.Add(time.Minute)
	afterCompletion, err := repository.UpdateBuild(
		ctx, tenant, bucket.Name, version.Fingerprint, completedBuild, completedAt,
	)
	if err != nil {
		t.Fatalf("meaningful UpdateBuild: %v", err)
	}
	if !afterCompletion.UpdatedAt.Equal(completedAt) {
		t.Fatalf("meaningful update kept updated_at %v, want %v", afterCompletion.UpdatedAt, completedAt)
	}
	gotVersion, err = repository.GetVersion(ctx, tenant, bucket.Name, version.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	sequence, complete := gotVersion.Sequence()
	if !complete || sequence != 1 {
		t.Fatalf("completed version sequence = %d,%v, want 1,true", sequence, complete)
	}
}

func TestRepositoryChannelLifecycleAndHistory(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	bucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "images", Labels: map[string]string{}, CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID:   registry.NewID(at.Add(time.Millisecond)),
		Name: "base", Labels: map[string]string{}, CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	buckets, err := repository.ListBuckets(ctx, tenant)
	if err != nil {
		t.Fatalf("ListBuckets: %v", err)
	}
	if len(buckets) != 2 || buckets[0].Name != "base" || buckets[1].Name != "images" {
		t.Fatalf("ListBuckets = %#v", buckets)
	}

	incomplete, err := registry.NewVersion(
		registry.NewID(at.Add(time.Second)),
		bucket.Name,
		"incomplete",
		registry.TemplateHCL2,
		at,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateVersion(ctx, tenant, incomplete); err != nil {
		t.Fatal(err)
	}
	complete, err := registry.RestoreVersion(registry.Version{
		ID:           registry.NewID(at.Add(2 * time.Second)),
		BucketName:   bucket.Name,
		Fingerprint:  "complete",
		TemplateType: registry.TemplateHCL2,
		CreatedAt:    at,
		UpdatedAt:    at,
	}, true, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateVersion(ctx, tenant, complete); err != nil {
		t.Fatal(err)
	}

	if _, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(3 * time.Second)), BucketName: bucket.Name,
		Name: "bad", CreatedAt: at.Add(3 * time.Second),
	}, incomplete.Fingerprint, "p-test"); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("CreateChannel incomplete version = %v, want ErrConflict", err)
	}
	if _, err := repository.GetChannel(ctx, tenant, bucket.Name, "bad"); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("failed CreateChannel persisted channel: %v", err)
	}

	initial, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(4 * time.Second)), BucketName: bucket.Name,
		Name: "initial", Restricted: true, CreatedAt: at.Add(4 * time.Second),
	}, complete.Fingerprint, "p-test")
	if err != nil {
		t.Fatalf("CreateChannel with version: %v", err)
	}
	if initial.Version == nil || initial.Version.Fingerprint != complete.Fingerprint {
		t.Fatalf("CreateChannel version = %#v", initial.Version)
	}

	staging, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(5 * time.Second)), BucketName: bucket.Name,
		Name: "staging", CreatedAt: at.Add(5 * time.Second),
	}, "", "p-test")
	if err != nil {
		t.Fatal(err)
	}
	production, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(6 * time.Second)), BucketName: bucket.Name,
		Name: "production", CreatedAt: at.Add(6 * time.Second),
	}, "", "p-test")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(6 * time.Second)), BucketName: bucket.Name,
		Name: "legacy", CreatedAt: at.Add(6 * time.Second),
	}, "", "p-test")
	if err != nil {
		t.Fatal(err)
	}
	target, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(6 * time.Second)), BucketName: bucket.Name,
		Name: "target", CreatedAt: at.Add(6 * time.Second),
	}, "", "p-test")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.BeginTenant(ctx, db, tenant.OrganizationID.String(), tenant.ProjectID.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO channel_assignments (
			organization_id, project_id, id, channel_id, version_id, assigned_at
		) VALUES ($1, $2, $3, $4, $5, $6)
	`, tenant.OrganizationID, tenant.ProjectID, registry.NewID(at).String(),
		legacy.ID.String(), incomplete.ID.String(), at); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert legacy incomplete assignment: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AssignChannelVersion(
		ctx, tenant, bucket.Name, legacy.Name, target.Name, "p-test", at.Add(7*time.Second),
	); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("AssignChannelVersion incomplete source = %v, want ErrConflict", err)
	}
	targetHistory, err := repository.ListChannelAssignmentHistory(ctx, tenant, bucket.Name, target.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(targetHistory) != 0 {
		t.Fatalf("rejected copied assignment history = %#v, want empty", targetHistory)
	}
	if _, err := repository.UpdateChannel(
		ctx, tenant, bucket.Name, staging.Name, false, false,
		true, incomplete.Fingerprint, "p-test", at.Add(7*time.Second),
	); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("UpdateChannel incomplete version = %v, want ErrConflict", err)
	}
	history, err := repository.ListChannelAssignmentHistory(ctx, tenant, bucket.Name, staging.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Fatalf("rejected assignment history = %#v, want empty", history)
	}

	staging, err = repository.UpdateChannel(
		ctx, tenant, bucket.Name, staging.Name, true, true,
		true, complete.Fingerprint, "p-test", at.Add(8*time.Second),
	)
	if err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if !staging.Restricted || staging.Version == nil ||
		staging.Version.Fingerprint != complete.Fingerprint {
		t.Fatalf("updated channel = %#v", staging)
	}
	got, err := repository.GetChannel(ctx, tenant, bucket.Name, staging.Name)
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if got.Version == nil || got.Version.ID != complete.ID {
		t.Fatalf("GetChannel version = %#v", got.Version)
	}
	history, err = repository.ListChannelAssignmentHistory(ctx, tenant, bucket.Name, staging.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Version.ID != complete.ID {
		t.Fatalf("staging history = %#v", history)
	}
	if history[0].AuthorID != "p-test" {
		t.Fatalf("staging history author = %q, want manual caller p-test", history[0].AuthorID)
	}

	if _, _, err := repository.AssignChannelVersion(
		ctx, tenant, bucket.Name, staging.Name, "latest", "p-test", at.Add(9*time.Second),
	); !errors.Is(err, store.ErrManagedChannel) {
		t.Fatalf("AssignChannelVersion managed target = %v, want ErrManagedChannel", err)
	}
	latestHistory, err := repository.ListChannelAssignmentHistory(ctx, tenant, bucket.Name, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if len(latestHistory) != 0 {
		t.Fatalf("refused managed assignment wrote history = %#v", latestHistory)
	}

	source, target, err := repository.AssignChannelVersion(
		ctx, tenant, bucket.Name, staging.Name, production.Name, "p-test", at.Add(10*time.Second),
	)
	if err != nil {
		t.Fatalf("AssignChannelVersion: %v", err)
	}
	if source.Version == nil || target.Version == nil || source.Version.ID != target.Version.ID {
		t.Fatalf("assigned channels = %#v %#v", source, target)
	}
	channels, err := repository.ListChannels(ctx, tenant, bucket.Name)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	// "latest" is the managed channel CreateBucket brought into existence
	// (duf-08q); the five user channels list around it.
	if len(channels) != 6 ||
		channels[0].Name != "initial" ||
		channels[1].Name != "latest" ||
		channels[2].Name != "legacy" ||
		channels[3].Name != "production" ||
		channels[4].Name != "staging" ||
		channels[5].Name != "target" {
		t.Fatalf("ListChannels = %#v", channels)
	}
	if !channels[1].Managed || channels[0].Managed {
		t.Fatalf("managed flags = %#v, want only latest managed", channels)
	}
	if channels[3].AssignmentAuthorID != "p-test" {
		t.Fatalf("production assignment author = %q, want p-test", channels[3].AssignmentAuthorID)
	}

	if err := repository.DeleteChannel(ctx, tenant, bucket.Name, production.Name); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}
	if _, err := repository.GetChannel(
		ctx, tenant, bucket.Name, production.Name,
	); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("GetChannel after delete = %v, want ErrNotFound", err)
	}
	tx, err = store.BeginTenant(ctx, db, tenant.OrganizationID.String(), tenant.ProjectID.String())
	if err != nil {
		t.Fatal(err)
	}
	var assignmentCount int
	if err := tx.QueryRowContext(
		ctx,
		"SELECT count(*) FROM channel_assignments WHERE channel_id = $1",
		production.ID.String(),
	).Scan(&assignmentCount); err != nil {
		_ = tx.Rollback()
		t.Fatalf("count retained channel history: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if assignmentCount != 1 {
		t.Fatalf("history rows after DeleteChannel = %d, want 1", assignmentCount)
	}
}

func TestRepositoryBucketsIncludeLatestVersionAndPlatforms(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	bucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "images", Labels: map[string]string{}, CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if _, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at.Add(time.Millisecond)), Name: "empty",
		Labels: map[string]string{}, CreatedAt: at,
	}); err != nil {
		t.Fatalf("CreateBucket empty: %v", err)
	}

	type buildSeed struct {
		component, platform string
		artifact            bool
	}
	versions := []struct {
		fingerprint string
		sequence    int
		builds      []buildSeed
	}{
		{fingerprint: "old", sequence: 1, builds: []buildSeed{{component: "amazon-ebs", platform: "aws"}}},
		{fingerprint: "current", sequence: 2, builds: []buildSeed{
			{component: "docker", platform: "docker", artifact: true},
			{component: "docker-copy", platform: "docker"},
			{component: "googlecompute", platform: "gcp"},
		}},
	}
	for versionIndex, seed := range versions {
		versionAt := at.Add(time.Duration(versionIndex+1) * time.Second)
		version, err := registry.RestoreVersion(registry.Version{
			ID: registry.NewID(versionAt), BucketName: bucket.Name,
			Fingerprint: seed.fingerprint, TemplateType: registry.TemplateHCL2,
			AuthorID: "p-test", CreatedAt: versionAt, UpdatedAt: versionAt,
		}, true, seed.sequence, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.CreateVersion(ctx, tenant, version); err != nil {
			t.Fatalf("CreateVersion %s: %v", seed.fingerprint, err)
		}
		for buildIndex, buildSeed := range seed.builds {
			buildAt := versionAt.Add(time.Duration(buildIndex+1) * time.Millisecond)
			build := store.StoredBuild{
				Build: registry.Build{
					ID: registry.NewID(buildAt), ComponentType: buildSeed.component,
					Status: registry.BuildDone, Platform: buildSeed.platform, MetadataSeen: true,
				},
				Labels: map[string]string{}, Metadata: json.RawMessage(`{"packer":{}}`),
				CreatedAt: buildAt,
			}
			if buildSeed.artifact {
				build.Artifacts = []store.Artifact{{
					ID:                 registry.NewID(buildAt.Add(time.Microsecond)),
					ExternalIdentifier: "docker-image-old", Region: "local", CreatedAt: buildAt,
				}, {
					ID:                 registry.NewID(buildAt.Add(2 * time.Microsecond)),
					ExternalIdentifier: "docker-image-new", Region: "local", CreatedAt: buildAt,
				}}
			}
			if _, err := repository.CreateBuild(
				ctx, tenant, bucket.Name, version.Fingerprint, registry.TemplateHCL2, build,
			); err != nil {
				t.Fatalf("CreateBuild %s/%s: %v", seed.fingerprint, buildSeed.component, err)
			}
		}
	}

	listed, err := repository.ListBuckets(ctx, tenant)
	if err != nil {
		t.Fatalf("ListBuckets: %v", err)
	}
	if len(listed) != 2 || listed[0].Name != "empty" || listed[1].Name != "images" {
		t.Fatalf("ListBuckets = %#v", listed)
	}
	fetched, err := repository.GetBucketWithLatestVersion(ctx, tenant, bucket.Name)
	if err != nil {
		t.Fatalf("GetBucketWithLatestVersion: %v", err)
	}
	for _, got := range []*store.Bucket{&listed[1], fetched} {
		if got.LatestVersion == nil || got.LatestVersion.Fingerprint != "current" {
			t.Fatalf("%s latest version = %#v, want current", got.Name, got.LatestVersion)
		}
		if sequence, complete := got.LatestVersion.Sequence(); !complete || sequence != 2 {
			t.Fatalf("%s latest version sequence = %d,%v, want 2,true", got.Name, sequence, complete)
		}
		if !reflect.DeepEqual(got.Platforms, []string{"docker", "gcp"}) {
			t.Fatalf("%s platforms = %v, want [docker gcp] from current version only", got.Name, got.Platforms)
		}
		if len(got.LatestVersionBuilds) != 3 || len(got.LatestVersion.Builds) != 3 {
			t.Fatalf("%s latest version builds = %d/%d, want 3/3",
				got.Name, len(got.LatestVersionBuilds), len(got.LatestVersion.Builds))
		}
		components := make([]string, 0, len(got.LatestVersionBuilds))
		for _, build := range got.LatestVersionBuilds {
			components = append(components, build.ComponentType)
		}
		if !reflect.DeepEqual(components, []string{"googlecompute", "docker-copy", "docker"}) {
			t.Fatalf("%s latest version builds = %v, want newest first", got.Name, components)
		}
		artifactCount := 0
		for i := range got.LatestVersionBuilds {
			artifactCount += len(got.LatestVersionBuilds[i].Artifacts)
		}
		if artifactCount != 2 {
			t.Fatalf("%s latest version artifacts = %d, want 2", got.Name, artifactCount)
		}
		if artifacts := got.LatestVersionBuilds[2].Artifacts; len(artifacts) != 2 ||
			artifacts[0].ExternalIdentifier != "docker-image-new" ||
			artifacts[1].ExternalIdentifier != "docker-image-old" {
			t.Fatalf("%s latest docker artifacts = %#v, want newest first", got.Name, artifacts)
		}
	}
}

// CreateBucket brings the managed "latest" channel into existence in the same
// transaction: a fresh bucket is never channel-less (dossier §7, Appendix A
// probes 04-06; duf-08q).
func TestCreateBucketCreatesManagedLatest(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	bucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "fresh", Labels: map[string]string{}, CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	latest, err := repository.GetChannel(ctx, tenant, bucket.Name, "latest")
	if err != nil {
		t.Fatalf("GetChannel latest on fresh bucket: %v", err)
	}
	if !latest.Managed || !latest.Restricted || latest.Version != nil {
		t.Fatalf("managed latest = %#v, want managed restricted unassigned", latest)
	}
	if !latest.CreatedAt.Equal(at) {
		t.Fatalf("latest created_at = %v, want the bucket's instant %v", latest.CreatedAt, at)
	}
	channels, err := repository.ListChannels(ctx, tenant, bucket.Name)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(channels) != 1 || channels[0].Name != "latest" {
		t.Fatalf("fresh bucket channels = %#v, want exactly latest", channels)
	}
}

// The terminal UpdateBuild that completes a version assigns it to "latest" in
// the same transaction, with no client call: the probe observed the channel's
// updated_at land on the completing UpdateBuild's own instant (Appendix A
// probes 13-14, 25; duf-08q).
func TestVersionCompletionAssignsManagedLatest(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	bucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "auto", Labels: map[string]string{}, CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	completeOne := func(fingerprint string, buildAt, completeAt time.Time) {
		t.Helper()
		version, err := registry.NewVersion(
			registry.NewID(buildAt), bucket.Name, fingerprint, registry.TemplateHCL2, buildAt,
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.CreateVersion(ctx, tenant, version); err != nil {
			t.Fatal(err)
		}
		build, err := repository.CreateBuild(
			ctx, tenant, bucket.Name, fingerprint, registry.TemplateHCL2,
			store.StoredBuild{
				Build: registry.Build{
					ID: registry.NewID(buildAt), ComponentType: "docker", Status: registry.BuildRunning,
				},
				Labels: map[string]string{}, CreatedAt: buildAt,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		completed := *build
		completed.Status = registry.BuildDone
		completed.MetadataSeen = true
		if _, err := repository.UpdateBuild(
			ctx, tenant, bucket.Name, fingerprint, completed, completeAt,
		); err != nil {
			t.Fatalf("completing UpdateBuild: %v", err)
		}
	}

	firstCompleteAt := at.Add(time.Hour)
	completeOne("fp-1", at.Add(time.Second), firstCompleteAt)

	latest, err := repository.GetChannel(ctx, tenant, bucket.Name, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Version == nil || latest.Version.Fingerprint != "fp-1" {
		t.Fatalf("latest after completion = %#v, want fp-1 assigned", latest.Version)
	}
	if sequence, complete := latest.Version.Sequence(); !complete || sequence != 1 {
		t.Fatalf("latest carries sequence %d,%v, want 1,true", sequence, complete)
	}
	// Same transaction, same instant: the assignment carries the completing
	// UpdateBuild's timestamp, not a later one.
	if !latest.UpdatedAt.Equal(firstCompleteAt) {
		t.Fatalf("latest updated_at = %v, want the completion instant %v",
			latest.UpdatedAt, firstCompleteAt)
	}
	history, err := repository.ListChannelAssignmentHistory(ctx, tenant, bucket.Name, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || !history[0].AssignedAt.Equal(firstCompleteAt) {
		t.Fatalf("latest history = %#v, want one entry at %v", history, firstCompleteAt)
	}
	if history[0].AuthorID != "Dufflebag" {
		t.Fatalf("latest history author = %q, want service author Dufflebag", history[0].AuthorID)
	}

	// A later completion moves latest forward and appends to the history.
	secondCompleteAt := at.Add(2 * time.Hour)
	completeOne("fp-2", at.Add(90*time.Minute), secondCompleteAt)
	latest, err = repository.GetChannel(ctx, tenant, bucket.Name, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Version == nil || latest.Version.Fingerprint != "fp-2" {
		t.Fatalf("latest after second completion = %#v, want fp-2", latest.Version)
	}
	history, err = repository.ListChannelAssignmentHistory(ctx, tenant, bucket.Name, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Version.Fingerprint != "fp-2" {
		t.Fatalf("latest history after second completion = %#v", history)
	}

	// A heartbeat after completion must not re-assign: the completion branch
	// only runs on the incomplete->complete transition.
	builds, err := repository.ListBuilds(ctx, tenant, bucket.Name, "fp-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateBuild(
		ctx, tenant, bucket.Name, "fp-2", builds[0], at.Add(3*time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	history, err = repository.ListChannelAssignmentHistory(ctx, tenant, bucket.Name, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("post-completion heartbeat grew history to %d rows", len(history))
	}
}

func TestConcurrentVersionCompletionsAllocateDistinctSequences(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	bucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "concurrent-sequences", Labels: map[string]string{}, CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	for round := range 10 {
		fingerprints := []string{fmt.Sprintf("round-%d-a", round), fmt.Sprintf("round-%d-b", round)}
		builds := make([]*store.StoredBuild, len(fingerprints))
		for i, fingerprint := range fingerprints {
			createdAt := at.Add(time.Duration(round*2+i+1) * time.Second)
			version, err := registry.NewVersion(
				registry.NewID(createdAt), bucket.Name, fingerprint, registry.TemplateHCL2, createdAt,
			)
			if err != nil {
				t.Fatalf("round %d NewVersion: %v", round, err)
			}
			if _, err := repository.CreateVersion(ctx, tenant, version); err != nil {
				t.Fatalf("round %d CreateVersion: %v", round, err)
			}
			builds[i], err = repository.CreateBuild(
				ctx, tenant, bucket.Name, fingerprint, registry.TemplateHCL2,
				store.StoredBuild{
					Build: registry.Build{
						ID:            registry.NewID(createdAt.Add(time.Millisecond)),
						ComponentType: "docker", Status: registry.BuildRunning,
					},
					Labels: map[string]string{}, CreatedAt: createdAt,
				},
			)
			if err != nil {
				t.Fatalf("round %d CreateBuild: %v", round, err)
			}
			builds[i].Status = registry.BuildDone
			builds[i].MetadataSeen = true
		}

		start := make(chan struct{})
		errs := make(chan error, len(builds))
		var wg sync.WaitGroup
		for i := range builds {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				_, err := repository.UpdateBuild(
					ctx, tenant, bucket.Name, fingerprints[i], *builds[i], at.Add(24*time.Hour),
				)
				errs <- err
			}(i)
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d concurrent UpdateBuild: %v", round, err)
			}
		}

		sequences := make([]int, len(fingerprints))
		for i, fingerprint := range fingerprints {
			version, err := repository.GetVersion(ctx, tenant, bucket.Name, fingerprint)
			if err != nil {
				t.Fatalf("round %d GetVersion: %v", round, err)
			}
			var complete bool
			sequences[i], complete = version.Sequence()
			if !complete {
				t.Fatalf("round %d version %s stayed incomplete", round, fingerprint)
			}
		}
		if sequences[0] == sequences[1] {
			t.Fatalf("round %d allocated duplicate sequence %d", round, sequences[0])
		}
	}
}

// The uniqueness that guards completion is migration 000001's
// versions_bucket_sequence, scoped (organization_id, project_id, bucket_id,
// sequence): duplicates are refused WITHIN a tenant, while two tenants that
// happen to hold the same bucket id each keep their own sequence space. A bare
// (bucket_id, sequence) index was proposed under duf-bd2 and rejected in
// review for coupling tenants — this test pins both halves of that decision.
func TestVersionSequenceIsUniquePerBucketWithinATenant(t *testing.T) {
	_, databaseURL, cleanup := openTestDatabase(t)
	defer cleanup()

	adminURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminURL.User = url.UserPassword("postgres", "postgres")
	admin, err := sql.Open("pgx", adminURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()

	if _, err := admin.Exec(`
		INSERT INTO buckets (organization_id, project_id, id, name, created_at, updated_at) VALUES
			($1, $2, 'shared-bucket-id', 'shared-a', now(), now()),
			($3, $4, 'shared-bucket-id', 'shared-b', now(), now())
	`, orgA, projectA, orgB, projectB); err != nil {
		t.Fatalf("insert same bucket id in two tenants: %v", err)
	}
	if _, err := admin.Exec(`
		INSERT INTO versions (
			organization_id, project_id, id, bucket_id, fingerprint,
			template_type, complete, sequence, created_at, updated_at
		) VALUES ($1, $2, 'version-a', 'shared-bucket-id', 'fingerprint-a', 'HCL2', true, 1, now(), now())
	`, orgA, projectA); err != nil {
		t.Fatalf("insert first sequence: %v", err)
	}
	if _, err := admin.Exec(`
		INSERT INTO versions (
			organization_id, project_id, id, bucket_id, fingerprint,
			template_type, complete, sequence, created_at, updated_at
		) VALUES ($1, $2, 'version-a2', 'shared-bucket-id', 'fingerprint-a2', 'HCL2', true, 1, now(), now())
	`, orgA, projectA); err == nil {
		t.Fatal("duplicate (bucket_id, sequence) insert succeeded within one tenant")
	}
	if _, err := admin.Exec(`
		INSERT INTO versions (
			organization_id, project_id, id, bucket_id, fingerprint,
			template_type, complete, sequence, created_at, updated_at
		) VALUES ($1, $2, 'version-b', 'shared-bucket-id', 'fingerprint-b', 'HCL2', true, 1, now(), now())
	`, orgB, projectB); err != nil {
		t.Fatalf("another tenant's identical (bucket_id, sequence) must not be refused: %v", err)
	}
}

// Migration 000008 backfills the managed "latest" for buckets that predate it,
// and converges a hand-created "latest" onto the managed shape instead of
// colliding with it (duf-08q). The dance: step the schema back to 000007,
// write what an old deployment would hold, step forward, and read the result
// through the RLS-bound repository — which also proves the backfilled ids are
// ULIDs the restore path accepts.
func TestMigrationBackfillsManagedLatest(t *testing.T) {
	db, databaseURL, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()

	adminURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminURL.User = url.UserPassword("postgres", "postgres")
	admin, err := sql.Open("pgx", adminURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()

	driver, err := migratepostgres.WithInstance(admin, &migratepostgres.Config{})
	if err != nil {
		t.Fatal(err)
	}
	migrator, err := migrate.NewWithDatabaseInstance("file://migrations", "postgres", driver)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.Migrate(7); err != nil {
		t.Fatalf("step back to the pre-managed schema: %v", err)
	}

	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	bareBucket := registry.NewID(at)
	latestBucket := registry.NewID(at.Add(time.Millisecond))
	handMadeLatest := registry.NewID(at.Add(2 * time.Millisecond))
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO buckets (organization_id, project_id, id, name, created_at, updated_at)
			VALUES ($1,$2,$3,'legacy-bare',$4,$4)`,
			[]any{orgA, projectA, bareBucket.String(), at}},
		{`INSERT INTO buckets (organization_id, project_id, id, name, created_at, updated_at)
			VALUES ($1,$2,$3,'legacy-latest',$4,$4)`,
			[]any{orgA, projectA, latestBucket.String(), at}},
		{`INSERT INTO channels (organization_id, project_id, id, bucket_id, name, restricted, created_at, updated_at)
			VALUES ($1,$2,$3,$4,'latest',false,$5,$5)`,
			[]any{orgA, projectA, handMadeLatest.String(), latestBucket.String(), at}},
	} {
		if _, err := admin.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("write pre-migration state: %v", err)
		}
	}

	if err := migrator.Up(); err != nil {
		t.Fatalf("apply the managed-latest migration over existing data: %v", err)
	}

	repository := store.NewRepository(db)
	tenant := store.ParseTenant(orgA, projectA)
	backfilled, err := repository.GetChannel(ctx, tenant, "legacy-bare", "latest")
	if err != nil {
		t.Fatalf("GetChannel backfilled latest: %v", err)
	}
	if !backfilled.Managed || !backfilled.Restricted || backfilled.Version != nil {
		t.Fatalf("backfilled latest = %#v, want managed restricted unassigned", backfilled)
	}
	converted, err := repository.GetChannel(ctx, tenant, "legacy-latest", "latest")
	if err != nil {
		t.Fatalf("GetChannel converted latest: %v", err)
	}
	if !converted.Managed || !converted.Restricted {
		t.Fatalf("hand-made latest = %#v, want converged onto the managed shape", converted)
	}
	if converted.ID != handMadeLatest {
		t.Fatalf("conversion re-minted the channel: id %s, want %s", converted.ID, handMadeLatest)
	}
}

// Migration 000009 cannot recover actors that were never stored. It backfills
// the explicit unknown empty string, keeps the old writer compatible through a
// default, and restores FORCE RLS after crossing tenants for the backfill.
// Removing the UPDATE makes SET NOT NULL fail while applying the migration, so
// this test is also the named backfill mutation gate.
func TestMigrationBackfillsChannelAssignmentAuthor(t *testing.T) {
	_, databaseURL, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()

	adminURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminURL.User = url.UserPassword("postgres", "postgres")
	admin, err := sql.Open("pgx", adminURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()

	driver, err := migratepostgres.WithInstance(admin, &migratepostgres.Config{})
	if err != nil {
		t.Fatal(err)
	}
	migrator, err := migrate.NewWithDatabaseInstance("file://migrations", "postgres", driver)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.Migrate(8); err != nil {
		t.Fatalf("step back to pre-author schema: %v", err)
	}

	at := time.Date(2026, 8, 1, 7, 0, 0, 0, time.UTC)
	bucketID := registry.NewID(at)
	versionID := registry.NewID(at.Add(time.Millisecond))
	channelID := registry.NewID(at.Add(2 * time.Millisecond))
	assignmentID := registry.NewID(at.Add(3 * time.Millisecond))
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO buckets (organization_id, project_id, id, name, created_at, updated_at)
			VALUES ($1,$2,$3,'legacy-author',$4,$4)`, []any{orgA, projectA, bucketID.String(), at}},
		{`INSERT INTO versions (organization_id, project_id, id, bucket_id, fingerprint,
			template_type, complete, sequence, created_at, updated_at)
			VALUES ($1,$2,$3,$4,'legacy-fp','HCL2',true,1,$5,$5)`,
			[]any{orgA, projectA, versionID.String(), bucketID.String(), at}},
		{`INSERT INTO channels (organization_id, project_id, id, bucket_id, name,
			restricted, managed, created_at, updated_at)
			VALUES ($1,$2,$3,$4,'production',false,false,$5,$5)`,
			[]any{orgA, projectA, channelID.String(), bucketID.String(), at}},
		{`INSERT INTO channel_assignments
			(organization_id, project_id, id, channel_id, version_id, assigned_at)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			[]any{orgA, projectA, assignmentID.String(), channelID.String(), versionID.String(), at}},
	} {
		if _, err := admin.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("write pre-author state: %v", err)
		}
	}

	if err := migrator.Up(); err != nil {
		t.Fatalf("apply channel-author migration over existing data: %v", err)
	}
	var authorID string
	if err := admin.QueryRowContext(ctx,
		`SELECT author_id FROM channel_assignments WHERE id = $1`, assignmentID.String(),
	).Scan(&authorID); err != nil {
		t.Fatal(err)
	}
	if authorID != "" {
		t.Fatalf("legacy assignment author = %q, want explicit unknown", authorID)
	}
	var rowSecurity, forceRowSecurity bool
	if err := admin.QueryRowContext(ctx, `
		SELECT relrowsecurity, relforcerowsecurity
		FROM pg_class WHERE oid = 'channel_assignments'::regclass
	`).Scan(&rowSecurity, &forceRowSecurity); err != nil {
		t.Fatal(err)
	}
	if !rowSecurity || !forceRowSecurity {
		t.Fatalf("channel_assignments RLS posture = enabled:%v forced:%v", rowSecurity, forceRowSecurity)
	}
	var policyCount int
	if err := admin.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_policies
		WHERE schemaname = current_schema() AND tablename = 'channel_assignments'
	`).Scan(&policyCount); err != nil {
		t.Fatal(err)
	}
	if policyCount != 1 {
		t.Fatalf("channel_assignments policy count = %d, want 1", policyCount)
	}
}

func TestDeleteProjectRefusesAProjectScopedPrincipal(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	principal, err := identity.NewPrincipal(
		"project-principal", "project principal", "project-client",
		identity.Scope{OrganizationID: uuid.MustParse(orgA), ProjectID: uuid.MustParse(projectA)},
		identity.RoleBuilder, time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	if err := repository.CreatePrincipal(ctx, principal); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if err := repository.DeleteProject(ctx, orgA, projectA); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("DeleteProject with scoped principal = %v, want ErrConflict", err)
	}
	if err := repository.DeletePrincipal(ctx, principal.ID); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}
	if err := repository.DeleteProject(ctx, orgA, projectA); err != nil {
		t.Fatalf("DeleteProject after deleting principal: %v", err)
	}
}

func TestDeleteOrganizationRefusesAnOrganizationScopedPrincipal(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	organization := store.Organization{
		ID: "00000000-0000-4000-8000-000000000090", Name: "principal-only", CreatedAt: time.Now().UTC(),
	}
	if _, err := repository.CreateOrganization(ctx, organization); err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	principal, err := identity.NewPrincipal(
		"organization-principal", "organization principal", "organization-client",
		identity.Scope{OrganizationID: uuid.MustParse(organization.ID)},
		identity.RoleMaintainer, time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	if err := repository.CreatePrincipal(ctx, principal); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if err := repository.DeleteOrganization(ctx, organization.ID); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("DeleteOrganization with scoped principal = %v, want ErrConflict", err)
	}
}

func TestPrincipalScopeForeignKeysAndPlatformRootShape(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	if _, err := db.Exec(`
		INSERT INTO principals (
			id, name, client_id, organization_id, project_id, role, created_at
		) VALUES ('cross-tenant', 'cross tenant', 'cross-tenant-client', $1, $2, 'builder', now())
	`, orgA, projectB); err == nil {
		t.Fatal("principal naming another organization's project was inserted")
	}
	if _, err := db.Exec(`
		INSERT INTO principals (
			id, name, client_id, organization_id, project_id, role, created_at
		) VALUES ('break-glass-root', 'break glass root', 'break-glass-client', NULL, NULL, 'root', now())
	`); err != nil {
		t.Fatalf("insert platform-scoped NULL/NULL root: %v", err)
	}
	var organizationID, projectID sql.NullString
	if err := db.QueryRow(`
		SELECT organization_id::text, project_id::text
		FROM principals WHERE id = 'break-glass-root'
	`).Scan(&organizationID, &projectID); err != nil {
		t.Fatalf("read platform root: %v", err)
	}
	if organizationID.Valid || projectID.Valid {
		t.Fatalf("platform root scope = organization %v, project %v; want NULL/NULL", organizationID, projectID)
	}
}

func TestTenancyRepositoryLifecycle(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	repository := store.NewRepository(db)
	createdAt := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	organization := store.Organization{
		ID:        "00000000-0000-4000-8000-000000000003",
		Name:      "lifecycle",
		CreatedAt: createdAt,
	}

	createdOrganization, err := repository.CreateOrganization(ctx, organization)
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	if *createdOrganization != organization {
		t.Fatalf("created organization = %#v, want %#v", createdOrganization, organization)
	}
	// Platform scope, because this test is about the lifecycle rather than
	// visibility; filtering has its own tests.
	organizations, err := repository.ListOrganizationsForPrincipal(
		ctx, listingCaller(t, identity.Scope{}, identity.RoleRoot),
	)
	if err != nil {
		t.Fatalf("ListOrganizationsForPrincipal: %v", err)
	}
	var listedOrganization *store.Organization
	for i := range organizations {
		if organizations[i].ID == organization.ID {
			listedOrganization = &organizations[i]
			break
		}
	}
	if listedOrganization == nil || *listedOrganization != organization {
		t.Fatalf("ListOrganizations did not contain %#v: %#v", organization, organizations)
	}
	gotOrganization, err := repository.GetOrganization(ctx, organization.ID)
	if err != nil {
		t.Fatalf("GetOrganization: %v", err)
	}
	if *gotOrganization != organization {
		t.Fatalf("organization round trip = %#v, want %#v", gotOrganization, organization)
	}
	if _, err := repository.CreateOrganization(ctx, store.Organization{
		ID:        "00000000-0000-4000-8000-000000000004",
		Name:      organization.Name,
		CreatedAt: createdAt,
	}); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("duplicate CreateOrganization = %v, want ErrConflict", err)
	}

	newer := store.Project{
		ID:             "00000000-0000-4000-8000-000000000202",
		OrganizationID: organization.ID,
		Name:           "newer",
		CreatedAt:      createdAt.Add(time.Hour),
	}
	oldest := store.Project{
		ID:             "00000000-0000-4000-8000-000000000201",
		OrganizationID: organization.ID,
		Name:           "oldest",
		CreatedAt:      createdAt,
	}
	if _, err := repository.CreateProject(ctx, newer); err != nil {
		t.Fatalf("CreateProject newer: %v", err)
	}
	if _, err := repository.CreateProject(ctx, oldest); err != nil {
		t.Fatalf("CreateProject oldest: %v", err)
	}
	projects, err := repository.ListProjects(ctx, organization.ID)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 2 || projects[0] != oldest || projects[1] != newer {
		t.Fatalf("ListProjects = %#v, want oldest project first", projects)
	}
	gotProject, err := repository.GetProject(ctx, organization.ID, oldest.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if *gotProject != oldest {
		t.Fatalf("project round trip = %#v, want %#v", gotProject, oldest)
	}
	if _, err := repository.GetProject(ctx, orgB, oldest.ID); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("cross-organization GetProject = %v, want ErrNotFound", err)
	}
	if _, err := repository.CreateProject(ctx, store.Project{
		ID:             "00000000-0000-4000-8000-000000000203",
		OrganizationID: organization.ID,
		Name:           oldest.Name,
		CreatedAt:      createdAt,
	}); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("duplicate CreateProject = %v, want ErrConflict", err)
	}
	if _, err := repository.CreateProject(ctx, store.Project{
		ID:             "00000000-0000-4000-8000-000000000204",
		OrganizationID: "00000000-0000-4000-8000-000000000099",
		Name:           "orphan",
		CreatedAt:      createdAt,
	}); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("orphan CreateProject = %v, want ErrConflict", err)
	}
	if err := repository.DeleteOrganization(ctx, organization.ID); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("DeleteOrganization with projects = %v, want ErrConflict", err)
	}

	tenant := store.ParseTenant(organization.ID, oldest.ID)
	bucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID:        registry.NewID(createdAt),
		Name:      "images",
		Labels:    map[string]string{},
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if err := repository.DeleteProject(ctx, organization.ID, oldest.ID); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("DeleteProject with bucket = %v, want ErrConflict", err)
	}
	tx, err := store.BeginTenant(ctx, db, organization.ID, oldest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM buckets WHERE id = $1", bucket.ID.String()); err != nil {
		_ = tx.Rollback()
		t.Fatalf("delete bucket fixture: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for _, project := range []store.Project{oldest, newer} {
		if err := repository.DeleteProject(ctx, organization.ID, project.ID); err != nil {
			t.Fatalf("DeleteProject %s: %v", project.Name, err)
		}
	}
	if err := repository.DeleteProject(ctx, organization.ID, oldest.ID); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("repeat DeleteProject = %v, want ErrNotFound", err)
	}
	if err := repository.DeleteOrganization(ctx, organization.ID); err != nil {
		t.Fatalf("DeleteOrganization: %v", err)
	}
	if _, err := repository.GetOrganization(ctx, organization.ID); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("GetOrganization after delete = %v, want ErrNotFound", err)
	}
	if err := repository.DeleteOrganization(ctx, organization.ID); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("repeat DeleteOrganization = %v, want ErrNotFound", err)
	}
}

// Packer upserts its bucket at the start of every build and tolerates only
// AlreadyExists, so a duplicate must surface as ErrConflict rather than a
// failure. The fake repository always modelled this; the real store did not,
// which no suite noticed (duf-ano).
func TestCreateBucketRefusesADuplicateName(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	if _, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "images", Labels: map[string]string{}, CreatedAt: at,
	}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	_, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID:   registry.NewID(at.Add(time.Millisecond)),
		Name: "images", Labels: map[string]string{}, CreatedAt: at,
	})
	if !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("duplicate CreateBucket = %v, want ErrConflict", err)
	}

	// The name is unique per tenancy, not per instance: another tenant reusing
	// it is not a conflict.
	if _, err := repository.CreateBucket(ctx, store.ParseTenant(orgB, projectB), store.Bucket{
		ID: registry.NewID(at.Add(2 * time.Millisecond)), Name: "images",
		Labels: map[string]string{}, CreatedAt: at,
	}); err != nil {
		t.Fatalf("CreateBucket in another tenancy: %v", err)
	}
}

func TestUpdateBucketRoundTripsAndStaysTenantScoped(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)

	if _, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "images", Description: "base images",
		Labels: map[string]string{"team": "platform"}, CreatedAt: at,
	}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	later := at.Add(time.Minute)
	updated, err := repository.UpdateBucket(ctx, tenant, "images",
		"revised base images", map[string]string{"team": "platform", "tier": "base"}, later)
	if err != nil {
		t.Fatalf("UpdateBucket: %v", err)
	}
	if updated.Description != "revised base images" || updated.Labels["tier"] != "base" ||
		!updated.UpdatedAt.Equal(later) {
		t.Fatalf("UpdateBucket returned %#v", updated)
	}

	// The change must survive a fresh read, not merely be echoed back.
	reread, err := repository.GetBucket(ctx, tenant, "images")
	if err != nil {
		t.Fatalf("GetBucket after update: %v", err)
	}
	if reread.Description != "revised base images" || reread.Labels["tier"] != "base" {
		t.Fatalf("GetBucket after update = %#v", reread)
	}

	if _, err := repository.UpdateBucket(ctx, tenant, "absent",
		"no such bucket", map[string]string{}, later); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("UpdateBucket on absent bucket = %v, want ErrNotFound", err)
	}

	// Tenancy axis: another tenant addressing the same name reaches nothing —
	// not-found, and the owning tenant's row is untouched.
	if _, err := repository.UpdateBucket(ctx, store.ParseTenant(orgB, projectB), "images",
		"cross-tenant write", map[string]string{}, later); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("cross-tenant UpdateBucket = %v, want ErrNotFound", err)
	}
	intact, err := repository.GetBucket(ctx, tenant, "images")
	if err != nil || intact.Description != "revised base images" {
		t.Fatalf("bucket after cross-tenant attempt = %#v, %v", intact, err)
	}
}

func TestCreateChannelRefusesADuplicateName(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	bucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "images", Labels: map[string]string{}, CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if _, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at), BucketName: bucket.Name, Name: "production", CreatedAt: at,
	}, "", "p-test"); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	_, err = repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(time.Millisecond)), BucketName: bucket.Name,
		Name: "production", CreatedAt: at,
	}, "", "p-test")
	if !errors.Is(err, store.ErrChannelExists) {
		t.Fatalf("duplicate CreateChannel = %v, want ErrChannelExists", err)
	}
}

// Clearing an assignment appends a row with no version to the append-only
// history rather than deleting anything: the latest row having no version is
// what "unassigned" looks like (duf-8em).
func TestUpdateChannelClearsTheAssignment(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 7, 31, 13, 0, 0, 0, time.UTC)

	bucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "images", Labels: map[string]string{}, CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	complete, err := registry.RestoreVersion(registry.Version{
		ID:           registry.NewID(at.Add(time.Second)),
		BucketName:   bucket.Name,
		Fingerprint:  "complete",
		TemplateType: registry.TemplateHCL2,
		CreatedAt:    at,
		UpdatedAt:    at,
	}, true, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateVersion(ctx, tenant, complete); err != nil {
		t.Fatal(err)
	}
	channel, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(2 * time.Second)), BucketName: bucket.Name,
		Name: "production", CreatedAt: at.Add(2 * time.Second),
	}, complete.Fingerprint, "p-test")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if channel.Version == nil {
		t.Fatalf("channel created unassigned: %#v", channel)
	}

	cleared, err := repository.UpdateChannel(
		ctx, tenant, bucket.Name, channel.Name, false, false,
		true, "", "p-test", at.Add(3*time.Second),
	)
	if err != nil {
		t.Fatalf("UpdateChannel clear: %v", err)
	}
	if cleared.Version != nil {
		t.Fatalf("cleared channel still assigned: %#v", cleared.Version)
	}
	fetched, err := repository.GetChannel(ctx, tenant, bucket.Name, channel.Name)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Version != nil {
		t.Fatalf("clear did not persist: %#v", fetched.Version)
	}

	// The assignment that led here is still on record.
	history, err := repository.ListChannelAssignmentHistory(ctx, tenant, bucket.Name, channel.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Version == nil ||
		history[0].Version.Fingerprint != complete.Fingerprint {
		t.Fatalf("history after clear = %#v, want the original assignment", history)
	}

	// Clearing an already-unassigned channel is a no-op, not another history row.
	if _, err := repository.UpdateChannel(
		ctx, tenant, bucket.Name, channel.Name, false, false,
		true, "", "p-test", at.Add(4*time.Second),
	); err != nil {
		t.Fatalf("repeat clear: %v", err)
	}
	tx, err := store.BeginTenant(ctx, db, tenant.OrganizationID.String(), tenant.ProjectID.String())
	if err != nil {
		t.Fatal(err)
	}
	var assignments int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM channel_assignments WHERE channel_id = $1
	`, channel.ID.String()).Scan(&assignments); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if assignments != 2 {
		t.Fatalf("assignment rows = %d, want 2 (one assign, one clear)", assignments)
	}

	// A cleared channel can be assigned again.
	reassigned, err := repository.UpdateChannel(
		ctx, tenant, bucket.Name, channel.Name, false, false,
		true, complete.Fingerprint, "p-test", at.Add(5*time.Second),
	)
	if err != nil {
		t.Fatalf("reassign after clear: %v", err)
	}
	if reassigned.Version == nil || reassigned.Version.Fingerprint != complete.Fingerprint {
		t.Fatalf("reassigned channel = %#v", reassigned.Version)
	}
}

// DeleteBucket takes the whole aggregate with it — versions, builds, artifacts,
// channels and sboms — which requires the append-only trigger to let the
// cascade through channel_assignments (migration 000006). The trigger keeps its
// teeth for everything else: live history is still immutable.
func TestDeleteBucketRemovesTheAggregate(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	_, objects := openTestObjectStore(t)
	repository := store.NewRepositoryWithObjectStore(db, objects)
	at := time.Date(2026, 7, 31, 15, 0, 0, 0, time.UTC)

	bucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "images", Labels: map[string]string{}, CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	complete, err := registry.RestoreVersion(registry.Version{
		ID:           registry.NewID(at.Add(time.Second)),
		BucketName:   bucket.Name,
		Fingerprint:  "complete",
		TemplateType: registry.TemplateHCL2,
		CreatedAt:    at,
		UpdatedAt:    at,
	}, true, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateVersion(ctx, tenant, complete); err != nil {
		t.Fatal(err)
	}
	build, err := repository.CreateBuild(ctx, tenant, bucket.Name, complete.Fingerprint,
		registry.TemplateHCL2, store.StoredBuild{
			Build: registry.Build{
				ID: registry.NewID(at.Add(2 * time.Second)), ComponentType: "amazon-ebs",
				Status: registry.BuildRunning, Platform: "aws",
			},
			Artifacts: []store.Artifact{{
				ID: registry.NewID(at.Add(2 * time.Second)), ExternalIdentifier: "ami-1",
				Region: "eu-west-2", CreatedAt: at,
			}},
			CreatedAt: at,
		})
	if err != nil {
		t.Fatalf("CreateBuild: %v", err)
	}
	if _, err := repository.UploadSbom(ctx, tenant, bucket.Name, complete.Fingerprint,
		build.ID.String(), store.Sbom{
			ID: registry.NewID(at.Add(3 * time.Second)), Name: "sbom",
			Format: "CYCLONEDX", CompressedData: []byte("zstd"), CreatedAt: at,
		}); err != nil {
		t.Fatalf("UploadSbom: %v", err)
	}
	channel, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(4 * time.Second)), BucketName: bucket.Name,
		Name: "production", CreatedAt: at.Add(4 * time.Second),
	}, complete.Fingerprint, "p-test")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	// A cleared assignment too, so a marker row with no version exists.
	if _, err := repository.UpdateChannel(
		ctx, tenant, bucket.Name, channel.Name, false, false, true, "", "p-test", at.Add(5*time.Second),
	); err != nil {
		t.Fatalf("UpdateChannel clear: %v", err)
	}

	// The trigger still rejects tampering with live history before the bucket
	// goes: migration 000006 narrowed the invariant, it did not drop it.
	tx, err := store.BeginTenant(ctx, db, tenant.OrganizationID.String(), tenant.ProjectID.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM channel_assignments WHERE channel_id = $1
	`, channel.ID.String()); err == nil || !strings.Contains(err.Error(), "append-only") {
		_ = tx.Rollback()
		t.Fatalf("deleting live history = %v, want the append-only rejection", err)
	}
	_ = tx.Rollback()

	if err := repository.DeleteBucket(ctx, tenant, bucket.Name); err != nil {
		t.Fatalf("DeleteBucket: %v", err)
	}
	if _, err := repository.GetBucket(ctx, tenant, bucket.Name); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("GetBucket after delete = %v, want ErrNotFound", err)
	}
	if err := repository.DeleteBucket(ctx, tenant, bucket.Name); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("repeat DeleteBucket = %v, want ErrNotFound", err)
	}

	tx, err = store.BeginTenant(ctx, db, tenant.OrganizationID.String(), tenant.ProjectID.String())
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, table := range []string{"versions", "builds", "artifacts", "channels", "sboms", "sbom_packages"} {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			_ = tx.Rollback()
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = count
	}
	var withVersion, markers int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE version_id IS NOT NULL),
		       count(*) FILTER (WHERE version_id IS NULL)
		FROM channel_assignments WHERE channel_id = $1
	`, channel.ID.String()).Scan(&withVersion, &markers); err != nil {
		_ = tx.Rollback()
		t.Fatalf("count assignments: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for table, count := range counts {
		if count != 0 {
			t.Errorf("%s rows after DeleteBucket = %d, want 0", table, count)
		}
	}
	// Version-bearing history followed its versions out by cascade; the
	// unassignment marker outlives what it describes, like history for a
	// deleted channel.
	if withVersion != 0 || markers != 1 {
		t.Fatalf("assignment rows after DeleteBucket = %d with version, %d markers; want 0 and 1",
			withVersion, markers)
	}
}

// UploadSbom is a PUT: a re-run build re-uploads under the same name, so the
// same name replaces content while keeping its identity (dossier §5.6).
func TestUploadSbomStoresAndReplaces(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()

	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	_, objects := openTestObjectStore(t)
	repository := store.NewRepositoryWithObjectStore(db, objects)
	at := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)

	bucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "images", Labels: map[string]string{}, CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	version, err := registry.NewVersion(
		registry.NewID(at.Add(time.Second)), bucket.Name, "fp", registry.TemplateHCL2, at,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateVersion(ctx, tenant, version); err != nil {
		t.Fatal(err)
	}
	build, err := repository.CreateBuild(ctx, tenant, bucket.Name, version.Fingerprint,
		registry.TemplateHCL2, store.StoredBuild{
			Build: registry.Build{
				ID: registry.NewID(at.Add(2 * time.Second)), ComponentType: "docker",
				Status: registry.BuildRunning, Platform: "docker",
			},
			CreatedAt: at,
		})
	if err != nil {
		t.Fatalf("CreateBuild: %v", err)
	}

	orphanData := []byte("zstd")
	if _, err := repository.UploadSbom(ctx, tenant, bucket.Name, version.Fingerprint,
		"absent", store.Sbom{
			ID: registry.NewID(at.Add(3 * time.Second)), Name: "sbom",
			Format: "CYCLONEDX", CompressedData: orphanData, CreatedAt: at,
		}); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("UploadSbom to a missing build = %v, want ErrNotFound", err)
	}
	orphanKey := objectstore.Key(orgA, projectA, "absent", "sbom", orphanData)
	if got, err := objects.Get(ctx, orphanKey); err != nil || !bytes.Equal(got, orphanData) {
		t.Fatalf("object-first orphan = %q, %v", got, err)
	}

	cycloneDXSource := `{
		"bomFormat":"CycloneDX","specVersion":"1.6","components":[
			{"bom-ref":"root","name":"application","components":[
				{"bom-ref":"openssl-npm","name":"openssl","version":"3.0.11",
				 "purl":"pkg:npm/openssl@3.0.11","licenses":[{"license":{"id":"MIT"}}]}
			]}
		]}`
	cycloneDX := compressIntegrationSBOM(t, cycloneDXSource)
	first, err := repository.UploadSbom(ctx, tenant, bucket.Name, version.Fingerprint,
		build.ID.String(), store.Sbom{
			ID: registry.NewID(at.Add(3 * time.Second)), Name: "sbom",
			Format: "CYCLONEDX", CompressedData: cycloneDX, CreatedAt: at,
		})
	if err != nil {
		t.Fatalf("UploadSbom: %v", err)
	}
	if first.Name != "sbom" || first.Format != "CYCLONEDX" ||
		!bytes.Equal(first.CompressedData, cycloneDX) || first.BuildID != build.ID ||
		first.ParseStatus != "parsed" {
		t.Fatalf("stored sbom = %#v", first)
	}
	{
		tx, err := store.BeginTenant(ctx, db, orgA, projectA)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		var licenses, paths, objectKey string
		if err := tx.QueryRowContext(ctx, `
			SELECT packages.licenses::text, packages.component_paths::text,
			       sboms.object_key
			FROM sbom_packages AS packages
			JOIN sboms ON sboms.id = packages.sbom_id
			WHERE packages.sbom_id = $1 AND packages.name = 'openssl'
		`, first.ID.String()).Scan(&licenses, &paths, &objectKey); err != nil {
			t.Fatal(err)
		}
		if licenses != `["MIT"]` || paths != `[["root", "openssl-npm"]]` || objectKey == "" {
			t.Fatalf("CycloneDX row = licenses %s, paths %s, key %q", licenses, paths, objectKey)
		}
	}
	// The download is the DOCUMENT, not the zstd envelope it travels in
	// (live-HCP probe, 2026-08-08; see duf-cse).
	if downloaded, err := repository.DownloadSbom(
		ctx, tenant, bucket.Name, version.Fingerprint, build.ID.String(), "sbom",
	); err != nil || !bytes.Equal(downloaded, []byte(cycloneDXSource)) {
		t.Fatalf("download first SBOM = %x, %v", downloaded, err)
	}

	spdxSource := `{
		"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","packages":[
			{"name":"image-root","SPDXID":"SPDXRef-Root"},
			{"name":"openssl","SPDXID":"SPDXRef-OpenSSL","versionInfo":"3.0.11",
			 "licenseDeclared":"Apache-2.0","externalRefs":[
				{"referenceType":"purl","referenceLocator":"pkg:rpm/openssl@3.0.11"}
			 ]}
		],"relationships":[
			{"spdxElementId":"SPDXRef-DOCUMENT","relatedSpdxElement":"SPDXRef-Root","relationshipType":"DESCRIBES"}
		]}`
	spdx := compressIntegrationSBOM(t, spdxSource)
	second, err := repository.UploadSbom(ctx, tenant, bucket.Name, version.Fingerprint,
		build.ID.String(), store.Sbom{
			ID: registry.NewID(at.Add(4 * time.Second)), Name: "sbom",
			Format: "SPDX", CompressedData: spdx, CreatedAt: at.Add(time.Second),
		})
	if err != nil {
		t.Fatalf("repeat UploadSbom: %v", err)
	}
	if second.ID != first.ID || second.Format != "SPDX" ||
		!bytes.Equal(second.CompressedData, spdx) || second.ParseStatus != "parsed" {
		t.Fatalf("replaced sbom = %#v, want the original identity with new content", second)
	}
	if downloaded, err := repository.DownloadSbom(
		ctx, tenant, bucket.Name, version.Fingerprint, build.ID.String(), "sbom",
	); err != nil || !bytes.Equal(downloaded, []byte(spdxSource)) {
		t.Fatalf("download replacement SBOM = %x, %v", downloaded, err)
	}
	if _, err := repository.UploadSbom(ctx, tenant, bucket.Name, version.Fingerprint,
		build.ID.String(), store.Sbom{
			ID: registry.NewID(at.Add(5 * time.Second)), Name: "second-sbom",
			Format: "SPDX", CompressedData: spdx, CreatedAt: at.Add(2 * time.Second),
		}); err != nil {
		t.Fatalf("upload second SBOM: %v", err)
	}

	packages, unparseable, err := repository.ListBuildPackages(
		ctx, tenant, bucket.Name, version.Fingerprint, build.ID.String())
	if err != nil || len(unparseable) != 0 || len(packages) != 1 {
		t.Fatalf("ListBuildPackages = %#v, unparseable %#v, %v", packages, unparseable, err)
	}
	if packages[0].Name != "openssl" || packages[0].Purl != "pkg:rpm/openssl@3.0.11" ||
		len(packages[0].Sboms) != 2 {
		t.Fatalf("reported package aggregation = %#v", packages)
	}

	broken, err := repository.UploadSbom(ctx, tenant, bucket.Name, version.Fingerprint,
		build.ID.String(), store.Sbom{
			ID: registry.NewID(at.Add(7 * time.Second)), Name: "broken",
			Format: "SPDX", CompressedData: []byte("client bytes that are not zstd"), CreatedAt: at,
		})
	if err != nil || broken.ParseStatus != "unparseable" || broken.ParseError == "" {
		t.Fatalf("unparseable upload = %#v, %v", broken, err)
	}
	if _, unparseable, err := repository.ListBuildPackages(
		ctx, tenant, bucket.Name, version.Fingerprint, build.ID.String()); err != nil ||
		len(unparseable) != 1 || unparseable[0] != "broken" {
		t.Fatalf("unparseable read state = %#v, %v", unparseable, err)
	}

	tx, err := store.BeginTenant(ctx, db, orgA, projectA)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var licenses string
	if err := tx.QueryRowContext(ctx, `
		SELECT licenses::text FROM sbom_packages
		WHERE sbom_id = $1 AND name = 'openssl'
	`, first.ID.String()).Scan(&licenses); err != nil {
		t.Fatal(err)
	}
	if licenses != `["Apache-2.0"]` {
		t.Fatalf("reported licences = %s", licenses)
	}
}

func compressIntegrationSBOM(t *testing.T, document string) []byte {
	t.Helper()
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := encoder.Close(); err != nil {
			t.Errorf("close zstd encoder: %v", err)
		}
	}()
	return encoder.EncodeAll([]byte(document), nil)
}

func TestRevokeVersionRollsBackUserAndManagedChannels(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC)

	if _, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "rollback", Labels: map[string]string{}, CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	completeVersionForRollback(t, repository, ctx, tenant, "rollback", "fp-one",
		at.Add(time.Minute), at.Add(2*time.Minute))
	completeVersionForRollback(t, repository, ctx, tenant, "rollback", "fp-two",
		at.Add(3*time.Minute), at.Add(4*time.Minute))
	if _, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(5 * time.Minute)), BucketName: "rollback",
		Name: "production", CreatedAt: at.Add(5 * time.Minute),
	}, "fp-one", "publisher"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateChannel(
		ctx, tenant, "rollback", "production", false, false, true,
		"fp-two", "publisher", at.Add(6*time.Minute),
	); err != nil {
		t.Fatal(err)
	}

	revokeAt := at.Add(7 * time.Minute)
	if _, err := repository.RevokeVersion(ctx, tenant, "rollback", "fp-two",
		store.RevocationRequest{RevokeAt: revokeAt, Author: "ops"},
		func(*registry.Version) string { return "v2" }, revokeAt); err != nil {
		t.Fatal(err)
	}
	for _, channelName := range []string{"production", "latest"} {
		channel, err := repository.GetChannel(ctx, tenant, "rollback", channelName)
		if err != nil {
			t.Fatal(err)
		}
		if channel.Version == nil || channel.Version.Fingerprint != "fp-one" {
			t.Fatalf("%s current version = %#v, want fp-one", channelName, channel.Version)
		}
		history, err := repository.ListChannelAssignmentHistory(ctx, tenant, "rollback", channelName)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) != 3 || history[0].Version.Fingerprint != "fp-one" ||
			history[0].AuthorID != "Dufflebag" || !history[0].AssignedAt.Equal(revokeAt) {
			t.Fatalf("%s rollback history = %#v", channelName, history)
		}
	}
}

func TestRevokeVersionRollbackWalksNewestValidHistory(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)

	if _, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "multi-step", Labels: map[string]string{}, CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	for i, fingerprint := range []string{"fp-one", "fp-two", "fp-three"} {
		createdAt := at.Add(time.Duration(i*2+1) * time.Minute)
		completeVersionForRollback(t, repository, ctx, tenant, "multi-step", fingerprint,
			createdAt, createdAt.Add(time.Minute))
	}
	if _, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(8 * time.Minute)), BucketName: "multi-step",
		Name: "production", CreatedAt: at.Add(8 * time.Minute),
	}, "fp-one", "publisher"); err != nil {
		t.Fatal(err)
	}
	for i, fingerprint := range []string{"fp-two", "fp-three"} {
		if _, err := repository.UpdateChannel(
			ctx, tenant, "multi-step", "production", false, false, true,
			fingerprint, "publisher", at.Add(time.Duration(i+9)*time.Minute),
		); err != nil {
			t.Fatal(err)
		}
	}

	firstRevokeAt := at.Add(11 * time.Minute)
	if _, err := repository.RevokeVersion(ctx, tenant, "multi-step", "fp-three",
		store.RevocationRequest{RevokeAt: firstRevokeAt, Author: "ops"},
		func(*registry.Version) string { return "v3" }, firstRevokeAt); err != nil {
		t.Fatal(err)
	}
	production, err := repository.GetChannel(ctx, tenant, "multi-step", "production")
	if err != nil {
		t.Fatal(err)
	}
	if production.Version == nil || production.Version.Fingerprint != "fp-two" {
		t.Fatalf("first rollback = %#v, want fp-two", production.Version)
	}

	secondRevokeAt := at.Add(12 * time.Minute)
	if _, err := repository.RevokeVersion(ctx, tenant, "multi-step", "fp-two",
		store.RevocationRequest{RevokeAt: secondRevokeAt, Author: "ops"},
		func(*registry.Version) string { return "v2" }, secondRevokeAt); err != nil {
		t.Fatal(err)
	}
	production, err = repository.GetChannel(ctx, tenant, "multi-step", "production")
	if err != nil {
		t.Fatal(err)
	}
	if production.Version == nil || production.Version.Fingerprint != "fp-one" {
		t.Fatalf("second rollback = %#v, want fp-one after skipping revoked history", production.Version)
	}
}

func TestRevokeVersionCanDisableChannelRollback(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	if _, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "opt-out", Labels: map[string]string{}, CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	completeVersionForRollback(t, repository, ctx, tenant, "opt-out", "fp-one",
		at.Add(time.Minute), at.Add(2*time.Minute))
	completeVersionForRollback(t, repository, ctx, tenant, "opt-out", "fp-two",
		at.Add(3*time.Minute), at.Add(4*time.Minute))
	if _, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(5 * time.Minute)), BucketName: "opt-out",
		Name: "production", CreatedAt: at.Add(5 * time.Minute),
	}, "fp-one", "publisher"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateChannel(
		ctx, tenant, "opt-out", "production", false, false, true,
		"fp-two", "publisher", at.Add(6*time.Minute),
	); err != nil {
		t.Fatal(err)
	}

	revokeAt := at.Add(7 * time.Minute)
	if _, err := repository.RevokeVersion(ctx, tenant, "opt-out", "fp-two",
		store.RevocationRequest{
			RevokeAt: revokeAt, Author: "ops", DisableRollbackChannels: true,
		}, func(*registry.Version) string { return "v2" }, revokeAt); err != nil {
		t.Fatal(err)
	}
	for _, channelName := range []string{"production", "latest"} {
		channel, err := repository.GetChannel(ctx, tenant, "opt-out", channelName)
		if err != nil {
			t.Fatal(err)
		}
		if channel.Version == nil || channel.Version.Fingerprint != "fp-two" ||
			channel.Version.Revocation() == nil {
			t.Fatalf("%s opt-out current version = %#v, want revoked fp-two", channelName, channel.Version)
		}
		history, err := repository.ListChannelAssignmentHistory(ctx, tenant, "opt-out", channelName)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) != 2 {
			t.Fatalf("%s opt-out history grew to %d rows", channelName, len(history))
		}
	}
}

func TestRevokeVersionLeavesChannelWithoutValidHistoryAssigned(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	if _, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "rollback-floor", Labels: map[string]string{}, CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	completeVersionForRollback(t, repository, ctx, tenant, "rollback-floor", "only-fp",
		at.Add(time.Minute), at.Add(2*time.Minute))
	if _, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(3 * time.Minute)), BucketName: "rollback-floor",
		Name: "production", CreatedAt: at.Add(3 * time.Minute),
	}, "only-fp", "publisher"); err != nil {
		t.Fatal(err)
	}

	revokeAt := at.Add(4 * time.Minute)
	if _, err := repository.RevokeVersion(ctx, tenant, "rollback-floor", "only-fp",
		store.RevocationRequest{RevokeAt: revokeAt, Author: "ops"},
		func(*registry.Version) string { return "v1" }, revokeAt); err != nil {
		t.Fatal(err)
	}
	for _, channelName := range []string{"production", "latest"} {
		channel, err := repository.GetChannel(ctx, tenant, "rollback-floor", channelName)
		if err != nil {
			t.Fatal(err)
		}
		if channel.Version == nil || channel.Version.Fingerprint != "only-fp" ||
			channel.Version.Revocation() == nil {
			t.Fatalf("%s floor assignment = %#v, want revoked only-fp", channelName, channel.Version)
		}
		history, err := repository.ListChannelAssignmentHistory(ctx, tenant, "rollback-floor", channelName)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) != 1 {
			t.Fatalf("%s floor history grew to %d rows", channelName, len(history))
		}
	}
}

func TestRevokeVersionRollsBackChannelFromInheritedDescendant(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 9, 13, 0, 0, 0, time.UTC)

	for i, bucketName := range []string{"rollback-parent", "rollback-child"} {
		when := at.Add(time.Duration(i) * time.Minute)
		if _, err := repository.CreateBucket(ctx, tenant, store.Bucket{
			ID: registry.NewID(when), Name: bucketName, Labels: map[string]string{}, CreatedAt: when,
		}); err != nil {
			t.Fatal(err)
		}
	}
	parent := completeVersionForRollback(t, repository, ctx, tenant, "rollback-parent", "parent-fp",
		at.Add(2*time.Minute), at.Add(3*time.Minute))
	completeVersionForRollback(t, repository, ctx, tenant, "rollback-child", "child-old-fp",
		at.Add(4*time.Minute), at.Add(5*time.Minute))

	childCreatedAt := at.Add(6 * time.Minute)
	childVersion, err := registry.NewVersion(
		registry.NewID(childCreatedAt), "rollback-child", "child-new-fp",
		registry.TemplateHCL2, childCreatedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateVersion(ctx, tenant, childVersion); err != nil {
		t.Fatal(err)
	}
	childBuild, err := repository.CreateBuild(
		ctx, tenant, "rollback-child", "child-new-fp", registry.TemplateHCL2,
		store.StoredBuild{
			Build: registry.Build{
				ID: registry.NewID(childCreatedAt.Add(time.Millisecond)), ComponentType: "docker",
				Status: registry.BuildRunning,
			},
			Labels: map[string]string{}, ParentVersionID: parent.ID.String(), CreatedAt: childCreatedAt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	childBuild.Status = registry.BuildDone
	childBuild.MetadataSeen = true
	if _, err := repository.UpdateBuild(
		ctx, tenant, "rollback-child", "child-new-fp", *childBuild, at.Add(7*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(8 * time.Minute)), BucketName: "rollback-child",
		Name: "production", CreatedAt: at.Add(8 * time.Minute),
	}, "child-old-fp", "publisher"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateChannel(
		ctx, tenant, "rollback-child", "production", false, false, true,
		"child-new-fp", "publisher", at.Add(9*time.Minute),
	); err != nil {
		t.Fatal(err)
	}

	revokeAt := at.Add(10 * time.Minute)
	if _, err := repository.RevokeVersion(ctx, tenant, "rollback-parent", "parent-fp",
		store.RevocationRequest{RevokeAt: revokeAt, Author: "ops"},
		func(*registry.Version) string { return "v1" }, revokeAt); err != nil {
		t.Fatal(err)
	}
	child, err := repository.GetVersion(ctx, tenant, "rollback-child", "child-new-fp")
	if err != nil {
		t.Fatal(err)
	}
	if revocation := child.Revocation(); revocation == nil || revocation.InheritedFrom == nil {
		t.Fatalf("child revocation = %+v, want inherited", revocation)
	}
	production, err := repository.GetChannel(ctx, tenant, "rollback-child", "production")
	if err != nil {
		t.Fatal(err)
	}
	if production.Version == nil || production.Version.Fingerprint != "child-old-fp" {
		t.Fatalf("descendant rollback = %#v, want child-old-fp", production.Version)
	}
}

func completeVersionForRollback(
	t *testing.T,
	repository *store.Repository,
	ctx context.Context,
	tenant store.Tenant,
	bucketName, fingerprint string,
	createdAt, completeAt time.Time,
) *registry.Version {
	t.Helper()
	version, err := registry.NewVersion(
		registry.NewID(createdAt), bucketName, fingerprint, registry.TemplateHCL2, createdAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateVersion(ctx, tenant, version); err != nil {
		t.Fatal(err)
	}
	build, err := repository.CreateBuild(
		ctx, tenant, bucketName, fingerprint, registry.TemplateHCL2,
		store.StoredBuild{
			Build: registry.Build{
				ID: registry.NewID(createdAt.Add(time.Millisecond)), ComponentType: "docker",
				Status: registry.BuildRunning,
			},
			Labels: map[string]string{}, CreatedAt: createdAt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	build.Status = registry.BuildDone
	build.MetadataSeen = true
	if _, err := repository.UpdateBuild(
		ctx, tenant, bucketName, fingerprint, *build, completeAt,
	); err != nil {
		t.Fatal(err)
	}
	completed, err := repository.GetVersion(ctx, tenant, bucketName, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	return completed
}

func TestRevokeVersionInheritsDownAncestryTransitively(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	wireName := func(*registry.Version) string { return "v1" }

	// base -> derived -> leaf, plus flagged as a second child of derived; the
	// ancestry edges live on the child builds, exactly as Packer records them.
	parents := map[string]string{"derived": "base", "leaf": "derived", "flagged": "derived"}
	versions := make(map[string]*registry.Version)
	for i, name := range []string{"base", "derived", "leaf", "flagged"} {
		when := at.Add(time.Duration(i) * time.Minute)
		if _, err := repository.CreateBucket(ctx, tenant, store.Bucket{
			ID: registry.NewID(when), Name: name, Labels: map[string]string{}, CreatedAt: when,
		}); err != nil {
			t.Fatal(err)
		}
		version, err := registry.RestoreVersion(registry.Version{
			ID: registry.NewID(when.Add(time.Second)), BucketName: name,
			Fingerprint: name + "-fp", TemplateType: registry.TemplateHCL2,
			CreatedAt: when, UpdatedAt: when,
		}, true, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.CreateVersion(ctx, tenant, version); err != nil {
			t.Fatal(err)
		}
		versions[name] = version
		if parent, ok := parents[name]; ok {
			if _, err := repository.CreateBuild(ctx, tenant, name, version.Fingerprint,
				registry.TemplateHCL2, store.StoredBuild{
					Build: registry.Build{
						ID: registry.NewID(when.Add(2 * time.Second)), ComponentType: "docker",
						Status: registry.BuildRunning, Platform: "docker",
					},
					Labels:          map[string]string{},
					ParentVersionID: versions[parent].ID.String(),
					CreatedAt:       when.Add(2 * time.Second),
				}); err != nil {
				t.Fatal(err)
			}
		}
	}

	// A channel pointing at the version being revoked: the read packer's data
	// sources perform must carry the revocation afterwards.
	if _, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(5 * time.Minute)), BucketName: "base",
		Name: "production", CreatedAt: at.Add(5 * time.Minute),
	}, "base-fp", "p-test"); err != nil {
		t.Fatal(err)
	}

	// flagged is already revoked manually; inheritance must not overwrite it.
	if _, err := repository.RevokeVersion(ctx, tenant, "flagged", "flagged-fp",
		store.RevocationRequest{
			RevokeAt: at.Add(10 * time.Minute), Message: "manual flag", Author: "security-team",
		}, wireName, at.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}

	// Revoking the root reaches the whole subtree in one call.
	effectAt := at.Add(11 * time.Minute)
	revoked, err := repository.RevokeVersion(ctx, tenant, "base", "base-fp",
		store.RevocationRequest{
			RevokeAt: effectAt, Message: "CVE-2026-0002", Author: "ops",
		}, wireName, effectAt)
	if err != nil {
		t.Fatal(err)
	}
	if rev := revoked.Revocation(); rev == nil || rev.InheritedFrom != nil ||
		rev.Author != "ops" || !rev.RevokeAt.Equal(effectAt) {
		t.Fatalf("base revocation = %+v; want a manual revocation by ops", rev)
	}

	for _, name := range []string{"derived", "leaf"} {
		version, err := repository.GetVersion(ctx, tenant, name, name+"-fp")
		if err != nil {
			t.Fatal(err)
		}
		rev := version.Revocation()
		if rev == nil || rev.InheritedFrom == nil {
			t.Fatalf("%s: revocation = %+v; want inherited", name, rev)
		}
		if rev.InheritedFrom.VersionID != versions["base"].ID ||
			rev.InheritedFrom.BucketName != "base" ||
			rev.InheritedFrom.Fingerprint != "base-fp" ||
			rev.InheritedFrom.VersionName != "v1" {
			t.Fatalf("%s: inherited from %+v; want the revoked ancestor's identity", name, rev.InheritedFrom)
		}
		if !rev.RevokeAt.Equal(effectAt) || rev.Message != "CVE-2026-0002" || rev.Author != "ops" {
			t.Fatalf("%s: inherited revocation = %+v; want the ancestor's effect", name, rev)
		}
	}

	// The channel's nested version carries the revocation on every channel
	// read path (found in review: the channel SELECTs had their own column
	// lists and silently restored revoked versions as active).
	production, err := repository.GetChannel(ctx, tenant, "base", "production")
	if err != nil {
		t.Fatal(err)
	}
	if production.Version == nil || production.Version.Revocation() == nil {
		t.Fatal("the production channel's nested version lost its revocation")
	}
	history, err := repository.ListChannelAssignmentHistory(ctx, tenant, "base", "production")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) == 0 || history[0].Version == nil || history[0].Version.Revocation() == nil {
		t.Fatal("the assignment history's nested version lost its revocation")
	}

	// The pre-revoked descendant keeps its own manual record.
	flagged, err := repository.GetVersion(ctx, tenant, "flagged", "flagged-fp")
	if err != nil {
		t.Fatal(err)
	}
	if rev := flagged.Revocation(); rev == nil || rev.InheritedFrom != nil ||
		rev.Author != "security-team" || rev.Message != "manual flag" {
		t.Fatalf("flagged revocation = %+v; a manual record must survive inheritance", rev)
	}

	// Re-revoking is a conflict, not an overwrite.
	if _, err := repository.RevokeVersion(ctx, tenant, "base", "base-fp",
		store.RevocationRequest{RevokeAt: effectAt, Author: "ops"},
		wireName, effectAt); !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("re-revoke = %v; want ErrConflict", err)
	}
}

func TestRevokeVersionSkipsDescendantsWhenAsked(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

	for i, name := range []string{"solo", "solo-child"} {
		when := at.Add(time.Duration(i) * time.Minute)
		if _, err := repository.CreateBucket(ctx, tenant, store.Bucket{
			ID: registry.NewID(when), Name: name, Labels: map[string]string{}, CreatedAt: when,
		}); err != nil {
			t.Fatal(err)
		}
		version, err := registry.NewVersion(
			registry.NewID(when.Add(time.Second)), name, name+"-fp", registry.TemplateHCL2, when,
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.CreateVersion(ctx, tenant, version); err != nil {
			t.Fatal(err)
		}
	}
	parent, err := repository.GetVersion(ctx, tenant, "solo", "solo-fp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateBuild(ctx, tenant, "solo-child", "solo-child-fp",
		registry.TemplateHCL2, store.StoredBuild{
			Build: registry.Build{
				ID: registry.NewID(at.Add(2 * time.Minute)), ComponentType: "docker",
				Status: registry.BuildRunning, Platform: "docker",
			},
			Labels:          map[string]string{},
			ParentVersionID: parent.ID.String(),
			CreatedAt:       at.Add(2 * time.Minute),
		}); err != nil {
		t.Fatal(err)
	}

	if _, err := repository.RevokeVersion(ctx, tenant, "solo", "solo-fp",
		store.RevocationRequest{
			RevokeAt: at.Add(3 * time.Minute), Author: "ops", SkipDescendants: true,
		},
		func(*registry.Version) string { return "v0" }, at.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	child, err := repository.GetVersion(ctx, tenant, "solo-child", "solo-child-fp")
	if err != nil {
		t.Fatal(err)
	}
	if child.Revocation() != nil {
		t.Fatalf("child revocation = %+v; skip_descendants_revocation must leave it untouched", child.Revocation())
	}
}

func seedRestoreVersionGraph(
	t *testing.T,
	repository *store.Repository,
	tenant store.Tenant,
	at time.Time,
	parents map[string][]string,
) map[string]*registry.Version {
	t.Helper()
	versions := make(map[string]*registry.Version)
	names := make([]string, 0, len(parents))
	for name := range parents {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		when := at.Add(time.Duration(i) * time.Minute)
		if _, err := repository.CreateBucket(context.Background(), tenant, store.Bucket{
			ID: registry.NewID(when), Name: name, Labels: map[string]string{}, CreatedAt: when,
		}); err != nil {
			t.Fatal(err)
		}
		version, err := registry.RestoreVersion(registry.Version{
			ID: registry.NewID(when.Add(time.Second)), BucketName: name,
			Fingerprint: name + "-fp", TemplateType: registry.TemplateHCL2,
			CreatedAt: when, UpdatedAt: when,
		}, true, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.CreateVersion(context.Background(), tenant, version); err != nil {
			t.Fatal(err)
		}
		versions[name] = version
	}
	for _, name := range names {
		for i, parent := range parents[name] {
			when := at.Add(time.Duration(len(names)+i) * time.Minute)
			if _, err := repository.CreateBuild(context.Background(), tenant, name, name+"-fp",
				registry.TemplateHCL2, store.StoredBuild{
					Build: registry.Build{
						ID: registry.NewID(when), ComponentType: fmt.Sprintf("docker-%d", i),
						Status: registry.BuildRunning, Platform: "docker",
					},
					Labels:          map[string]string{},
					ParentVersionID: versions[parent].ID.String(),
					CreatedAt:       when,
				}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return versions
}

func TestRestoreRevokedVersionClearsMatchingInheritedDescendantsAndAllowsRerevoke(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	seedRestoreVersionGraph(t, repository, tenant, at, map[string][]string{
		"base": nil, "derived": {"base"}, "leaf": {"derived"},
	})
	wireName := func(*registry.Version) string { return "v1" }
	if _, err := repository.RevokeVersion(ctx, tenant, "base", "base-fp",
		store.RevocationRequest{RevokeAt: at.Add(time.Hour), Author: "ops"},
		wireName, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RestoreRevokedVersion(ctx, tenant, "base", "base-fp", at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"base", "derived", "leaf"} {
		version, err := repository.GetVersion(ctx, tenant, name, name+"-fp")
		if err != nil {
			t.Fatal(err)
		}
		if version.Revocation() != nil {
			t.Fatalf("%s revocation = %+v; want cleared", name, version.Revocation())
		}
	}
	if _, err := repository.RevokeVersion(ctx, tenant, "base", "base-fp",
		store.RevocationRequest{RevokeAt: at.Add(3 * time.Hour), Author: "security"},
		wireName, at.Add(3*time.Hour)); err != nil {
		t.Fatalf("revoke after restore: %v", err)
	}
}

func TestRestoreRevokedVersionLeavesManualDescendantRevoked(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	seedRestoreVersionGraph(t, repository, tenant, at, map[string][]string{
		"base": nil, "child": {"base"},
	})
	wireName := func(*registry.Version) string { return "v1" }
	if _, err := repository.RevokeVersion(ctx, tenant, "child", "child-fp",
		store.RevocationRequest{RevokeAt: at.Add(time.Hour), Message: "manual", Author: "security"},
		wireName, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RevokeVersion(ctx, tenant, "base", "base-fp",
		store.RevocationRequest{RevokeAt: at.Add(2 * time.Hour), Author: "ops"},
		wireName, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RestoreRevokedVersion(ctx, tenant, "base", "base-fp", at.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	child, err := repository.GetVersion(ctx, tenant, "child", "child-fp")
	if err != nil {
		t.Fatal(err)
	}
	if rev := child.Revocation(); rev == nil || rev.InheritedFrom != nil ||
		rev.Author != "security" || rev.Message != "manual" {
		t.Fatalf("child revocation = %+v; manual revocation must stand", rev)
	}
}

func TestRestoreRevokedVersionLeavesDescendantInheritedFromDifferentAncestor(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	at := time.Date(2026, 8, 11, 11, 0, 0, 0, time.UTC)
	versions := seedRestoreVersionGraph(t, repository, tenant, at, map[string][]string{
		"base": nil, "other": nil, "child": {"base", "other"},
	})
	wireName := func(*registry.Version) string { return "v1" }
	if _, err := repository.RevokeVersion(ctx, tenant, "other", "other-fp",
		store.RevocationRequest{RevokeAt: at.Add(time.Hour), Author: "other-owner"},
		wireName, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RevokeVersion(ctx, tenant, "base", "base-fp",
		store.RevocationRequest{RevokeAt: at.Add(2 * time.Hour), Author: "ops"},
		wireName, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RestoreRevokedVersion(ctx, tenant, "base", "base-fp", at.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	child, err := repository.GetVersion(ctx, tenant, "child", "child-fp")
	if err != nil {
		t.Fatal(err)
	}
	if rev := child.Revocation(); rev == nil || rev.InheritedFrom == nil ||
		rev.InheritedFrom.VersionID != versions["other"].ID || rev.Author != "other-owner" {
		t.Fatalf("child revocation = %+v; different ancestor's inherited revocation must stand", rev)
	}
}
