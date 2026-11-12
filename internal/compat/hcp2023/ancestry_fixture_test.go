package hcp2023

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
)

// The console's Ancestry card fixture is these responses, keyed by the path
// the console requests: the file is the producer's output, not a hand-written
// copy of it.
const ancestryCardFixture = "../../../web/tests/fixtures/ancestry-card.json"

type fixtureResponse struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

func TestAncestryCardWebFixture(t *testing.T) {
	repository := newFakeRepository()
	ids := 0
	nextID := func() registry.ID {
		ids++
		// Fixed ULIDs keep the fixture byte-stable between runs.
		id, err := registry.ParseID(fmt.Sprintf("01K1CJ4X8G%016d", ids))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	version := func(bucket string, sequence int, revocation *registry.Revocation) *registry.Version {
		v, err := registry.RestoreVersion(registry.Version{
			ID: nextID(), BucketName: bucket, Fingerprint: fmt.Sprintf("%s-%d", bucket, sequence),
			TemplateType: registry.TemplateHCL2, CreatedAt: testTime, UpdatedAt: testTime,
		}, true, sequence, revocation)
		if err != nil {
			t.Fatal(err)
		}
		repository.versions[bucket+"/"+v.Fingerprint] = v
		return v
	}
	bucket := func(name string, latest *registry.Version) {
		repository.buckets[name] = &store.Bucket{
			ID: nextID(), Name: name, Labels: map[string]string{}, LatestVersion: latest,
			CreatedAt: testTime, UpdatedAt: testTime,
		}
	}
	channel := func(bucket, name string, target *registry.Version) *store.Channel {
		c := &store.Channel{
			ID: nextID(), BucketName: bucket, Name: name, Managed: name == "latest",
			Version: target, CreatedAt: testTime, UpdatedAt: testTime,
		}
		repository.channels[bucket+"/"+name] = c
		return c
	}
	build := func(child, parent *registry.Version, from *store.Channel) {
		stored := store.StoredBuild{
			Build: registry.Build{
				ID: nextID(), ComponentType: "docker.image", Status: registry.BuildDone, Platform: "docker",
			},
			VersionID: child.ID, ParentVersionID: parent.ID.String(),
			CreatedAt: testTime, UpdatedAt: testTime,
		}
		if from != nil {
			stored.ParentChannelID = from.ID.String()
		}
		key := child.BucketName + "/" + child.Fingerprint
		repository.builds[key] = append(repository.builds[key], stored)
	}

	pastRevocation := testTime.Add(-time.Hour)
	base := map[int]*registry.Version{}
	for sequence := 1; sequence <= 5; sequence++ {
		var revocation *registry.Revocation
		if sequence == 2 {
			revocation = &registry.Revocation{RevokeAt: pastRevocation, Message: "CVE", Author: "ops"}
		}
		base[sequence] = version("base", sequence, revocation)
	}
	bucket("base", base[5])
	channel("base", "latest", base[5])
	production := channel("base", "production", base[4])
	staging := channel("base", "staging", nil)
	baseLatest := repository.channels["base/latest"]

	agent := map[int]*registry.Version{}
	for sequence := 1; sequence <= 5; sequence++ {
		var revocation *registry.Revocation
		if sequence == 4 {
			revocation = &registry.Revocation{
				RevokeAt: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC), Author: "ops",
			}
		}
		agent[sequence] = version("agent", sequence, revocation)
	}
	bucket("agent", agent[5])
	channel("agent", "latest", agent[5])
	channel("agent", "production", agent[5])
	channel("agent", "stable", agent[3])
	build(agent[5], base[4], production)
	build(agent[4], base[4], baseLatest)
	build(agent[3], base[2], production)
	build(agent[2], base[1], production)
	build(agent[1], base[1], production)

	runner := map[int]*registry.Version{}
	runner[1] = version("runner", 1, &registry.Revocation{
		RevokeAt: pastRevocation, Author: "ops",
		InheritedFrom: &registry.RevokedAncestor{
			VersionID: base[2].ID, BucketName: "base", Fingerprint: base[2].Fingerprint, VersionName: "v2",
		},
	})
	runner[2] = version("runner", 2, nil)
	runner[3] = version("runner", 3, nil)
	bucket("runner", runner[3])
	channel("runner", "latest", runner[2])
	build(runner[3], base[4], staging)
	build(runner[2], base[4], nil)
	build(runner[1], base[2], production)

	// A related bucket the console cannot read: its version still names base
	// as a parent, but the bucket itself answers 404.
	retired := version("retired", 1, nil)
	build(retired, base[4], production)

	server := newHandler(repository, testPrincipals(), testAuthenticator{}, testLogger(), func() time.Time { return testTime })
	paths := []string{
		"/buckets/base",
		"/buckets/base/versions/base-4",
		"/buckets/base/versions",
		"/buckets/base/channels",
		"/buckets/agent",
		"/buckets/agent/versions",
		"/buckets/agent/channels",
		"/buckets/runner/versions",
		"/buckets/runner/channels",
		"/buckets/retired/versions",
		"/buckets/retired/channels",
	}
	for name, versions := range map[string]map[int]*registry.Version{"base": base, "agent": agent} {
		for _, v := range versions {
			for _, kind := range []string{"PARENTS", "CHILDREN"} {
				paths = append(paths, fmt.Sprintf("/buckets/%s/ancestry?type=ANCESTRY_TYPE_%s&version_fingerprint=%s",
					name, kind, v.Fingerprint))
			}
		}
	}
	responses := make(map[string]fixtureResponse, len(paths))
	for _, path := range paths {
		response := request(t, server, http.MethodGet, testBase+path, nil)
		responses[path] = fixtureResponse{Status: response.Code, Body: response.Body.Bytes()}
	}
	if responses["/buckets/retired/channels"].Status != http.StatusNotFound {
		t.Fatalf("retired channels status = %d, want 404", responses["/buckets/retired/channels"].Status)
	}

	encoded, err := json.MarshalIndent(responses, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if os.Getenv("DUFFLEBAG_UPDATE_FIXTURES") != "" {
		if err := os.WriteFile(ancestryCardFixture, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fixture, err := os.ReadFile(ancestryCardFixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, fixture) {
		t.Fatalf("web fixture drifted from the handler's responses; regenerate with DUFFLEBAG_UPDATE_FIXTURES=1:\n%s", encoded)
	}
}
