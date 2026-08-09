//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	"github.com/benemon/dufflebag/internal/keyring"
	"github.com/benemon/dufflebag/internal/store/objectstore"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

// integrationProvider stands in for the key service in store tests: the
// wrap/unwrap seam is proven against real Vault in the runtime lane; here the
// subject is what the repository does with the unwrapped keys.
type integrationProvider struct{}

func (integrationProvider) Wrap(_ context.Context, plaintext []byte) ([]byte, string, error) {
	blob := make([]byte, len(plaintext))
	for i, b := range plaintext {
		blob[i] = b ^ 0x5a
	}
	return blob, "test-kek", nil
}

func (integrationProvider) Unwrap(_ context.Context, blob []byte, _ string) ([]byte, error) {
	plaintext := make([]byte, len(blob))
	for i, b := range blob {
		plaintext[i] = b ^ 0x5a
	}
	return plaintext, nil
}

func testRing(t *testing.T) *keyring.Keyring {
	t.Helper()
	ring, _, err := keyring.Generate(context.Background(), integrationProvider{})
	if err != nil {
		t.Fatalf("Generate keyring: %v", err)
	}
	return ring
}

// seedEncryptedBuild creates bucket -> version -> running build under the
// tenant, returning the bucket name, fingerprint, and build.
func seedEncryptedBuild(
	t *testing.T, repository *store.Repository, tenant store.Tenant, suffix string,
) (string, string, *store.StoredBuild) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	bucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "sealed-" + suffix, CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	version, err := registry.NewVersion(
		registry.NewID(at.Add(time.Second)), bucket.Name, "fp-"+suffix,
		registry.TemplateHCL2, at.Add(time.Second),
	)
	if err != nil {
		t.Fatalf("NewVersion: %v", err)
	}
	if _, err := repository.CreateVersion(ctx, tenant, version); err != nil {
		t.Fatalf("CreateVersion: %v", err)
	}
	build, err := repository.CreateBuild(ctx, tenant, bucket.Name, version.Fingerprint,
		registry.TemplateHCL2, store.StoredBuild{
			Build: registry.Build{
				ID: registry.NewID(at.Add(2 * time.Second)), ComponentType: "docker",
				Status: registry.BuildRunning, Platform: "docker",
			},
			Labels: map[string]string{}, CreatedAt: at.Add(2 * time.Second),
		})
	if err != nil {
		t.Fatalf("CreateBuild: %v", err)
	}
	return bucket.Name, version.Fingerprint, build
}

const metadataCanary = "canary-metadata-plaintext-value"

// rawMetadata reads the stored column bytes exactly as a dump would see them,
// through a tenant-scoped session because the test role is RLS-bound.
func rawMetadata(t *testing.T, db *sql.DB, organizationID, projectID, buildID string) []byte {
	t.Helper()
	tx, err := store.BeginTenant(context.Background(), db, organizationID, projectID)
	if err != nil {
		t.Fatalf("begin raw read: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	var stored []byte
	if err := tx.QueryRow(`SELECT metadata FROM builds WHERE id = $1`, buildID).Scan(&stored); err != nil {
		t.Fatalf("read raw metadata: %v", err)
	}
	return stored
}

func TestEncryptedBuildMetadataRoundTripsAndStoresNoPlaintext(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	repository.SetKeyring(testRing(t))

	bucketName, fingerprint, build := seedEncryptedBuild(t, repository, tenant, "meta")
	build.Metadata = []byte(`{"cpu":"amd64","note":"` + metadataCanary + `"}`)
	build.MetadataSeen = true
	updated, err := repository.UpdateBuild(ctx, tenant, bucketName, fingerprint, *build,
		build.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("UpdateBuild: %v", err)
	}
	if !strings.Contains(string(updated.Metadata), metadataCanary) {
		t.Fatalf("metadata did not round trip: %s", updated.Metadata)
	}
	read, err := repository.GetBuild(ctx, tenant, bucketName, fingerprint, build.ID.String())
	if err != nil {
		t.Fatalf("GetBuild: %v", err)
	}
	if !strings.Contains(string(read.Metadata), metadataCanary) {
		t.Fatalf("metadata read = %s", read.Metadata)
	}

	// The dump-inspection assertion (ADR-0024's headline goal): the stored
	// column carries a sealed envelope, and nowhere in it do the plaintext
	// bytes appear.
	stored := rawMetadata(t, db, orgA, projectA, build.ID.String())
	if !keyring.Sealed(stored) {
		t.Fatalf("stored metadata is not sealed: %q", stored)
	}
	if bytes.Contains(stored, []byte(metadataCanary)) || bytes.Contains(stored, []byte("amd64")) {
		t.Fatalf("stored metadata contains plaintext: %q", stored)
	}
}

// The tenancy axis of the acceptance criteria: a valid ciphertext moved to
// another tenant's row must fail to decrypt, not open under the new identity.
func TestSealedMetadataMovedAcrossTenantsFailsToRead(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	repository := store.NewRepository(db)
	repository.SetKeyring(testRing(t))
	tenantA := store.ParseTenant(orgA, projectA)
	tenantB := store.ParseTenant(orgB, projectB)

	bucketA, fingerprintA, buildA := seedEncryptedBuild(t, repository, tenantA, "a")
	buildA.Metadata = []byte(`{"secret":"` + metadataCanary + `"}`)
	buildA.MetadataSeen = true
	if _, err := repository.UpdateBuild(ctx, tenantA, bucketA, fingerprintA, *buildA,
		buildA.CreatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("UpdateBuild A: %v", err)
	}
	bucketB, fingerprintB, buildB := seedEncryptedBuild(t, repository, tenantB, "b")

	// The relocation a database writer could perform: tenant A's envelope
	// lands on tenant B's row.
	sealed := rawMetadata(t, db, orgA, projectA, buildA.ID.String())
	tx, err := store.BeginTenant(ctx, db, orgB, projectB)
	if err != nil {
		t.Fatalf("begin tenant B session: %v", err)
	}
	if _, err := tx.Exec(`UPDATE builds SET metadata = $1, metadata_seen = true WHERE id = $2`,
		sealed, buildB.ID.String()); err != nil {
		t.Fatalf("relocate ciphertext: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit relocation: %v", err)
	}

	// Two guards refuse this independently: the row MAC (the metadata digest
	// no longer matches) and, were the MAC somehow recomputed, the AAD-bound
	// decryption. Either refusal proves the relocation cannot be served.
	if read, err := repository.GetBuild(ctx, tenantB, bucketB, fingerprintB, buildB.ID.String()); err == nil {
		t.Fatalf("relocated ciphertext was served: %s", read.Metadata)
	} else if !strings.Contains(err.Error(), "decryption failed") &&
		!strings.Contains(err.Error(), "integrity verification failed") {
		t.Fatalf("relocated ciphertext error = %v, want a decryption or integrity refusal", err)
	}

	// The same envelope still reads fine where it belongs.
	if _, err := repository.GetBuild(ctx, tenantA, bucketA, fingerprintA, buildA.ID.String()); err != nil {
		t.Fatalf("original row read: %v", err)
	}
}

// An unencrypted repository (no keyring) keeps its plaintext behaviour: the
// two postures share one schema and one code path.
func TestUnencryptedMetadataStaysPlaintext(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)

	bucketName, fingerprint, build := seedEncryptedBuild(t, repository, tenant, "plain")
	build.Metadata = []byte(`{"note":"` + metadataCanary + `"}`)
	build.MetadataSeen = true
	if _, err := repository.UpdateBuild(ctx, tenant, bucketName, fingerprint, *build,
		build.CreatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("UpdateBuild: %v", err)
	}
	stored := rawMetadata(t, db, orgA, projectA, build.ID.String())
	if !bytes.Contains(stored, []byte(metadataCanary)) {
		t.Fatalf("unencrypted deployment stored something other than plaintext: %q", stored)
	}
}

// SBOM bytes are sealed client-side before PutObject: a dump of the bucket is
// not a disclosure regardless of the bucket's own configuration, and the
// download proxy returns the plaintext (ADR-0024).
func TestEncryptedSbomBytesAreSealedInObjectStorage(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	_, objects := openTestObjectStore(t)
	repository := store.NewRepositoryWithObjectStore(db, objects)
	repository.SetKeyring(testRing(t))

	bucketName, fingerprint, build := seedEncryptedBuild(t, repository, tenant, "sbom")
	documentSource := `{
		"bomFormat":"CycloneDX","specVersion":"1.6","components":[
			{"bom-ref":"canary","name":"canary-sealed-package","version":"1.0.0"}
		]}`
	document := compressIntegrationSBOM(t, documentSource)
	uploaded, err := repository.UploadSbom(ctx, tenant, bucketName, fingerprint,
		build.ID.String(), store.Sbom{
			ID: registry.NewID(build.CreatedAt.Add(time.Second)), Name: "sbom",
			Format: "CYCLONEDX", CompressedData: document, CreatedAt: build.CreatedAt,
		})
	if err != nil {
		t.Fatalf("UploadSbom: %v", err)
	}
	// Parsing happened on the plaintext, before sealing.
	if uploaded.ParseStatus != "parsed" {
		t.Fatalf("parse status = %q, want parsed", uploaded.ParseStatus)
	}
	if !bytes.Equal(uploaded.CompressedData, document) {
		t.Fatal("upload response does not carry the caller's bytes")
	}

	// The object at rest is a sealed envelope, not the client's bytes.
	key := objectstore.Key(orgA, projectA, build.ID.String(), "sbom", document)
	raw, err := objects.Get(ctx, key)
	if err != nil {
		t.Fatalf("raw object read: %v", err)
	}
	if !keyring.Sealed(raw) {
		t.Fatalf("stored object is not sealed: %q", raw[:16])
	}
	if bytes.Equal(raw, document) || bytes.Contains(raw, document) {
		t.Fatal("stored object contains the plaintext bytes")
	}

	// The proxy path returns the DOCUMENT the client compressed — unsealed
	// and unwrapped (live-HCP probe, 2026-08-08; see duf-cse).
	downloaded, err := repository.DownloadSbom(ctx, tenant, bucketName, fingerprint,
		build.ID.String(), "sbom")
	if err != nil {
		t.Fatalf("DownloadSbom: %v", err)
	}
	if !bytes.Equal(downloaded, []byte(documentSource)) {
		t.Fatal("download proxy did not return the document")
	}
}

// The whole encrypted provenance chain in one flow — build completion writes
// version, build, and auto-assignment MACs — followed by the rule-2 mutation:
// an altered provenance row must fail its read.
func TestEncryptedProvenanceRowsRoundTripAndDetectAlteration(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	repository := store.NewRepository(db)
	repository.SetKeyring(testRing(t))

	bucketName, fingerprint, build := seedEncryptedBuild(t, repository, tenant, "prov")
	build.Status = registry.BuildDone
	build.Metadata = []byte(`{"provenance":"sealed"}`)
	build.MetadataSeen = true
	build.Artifacts = []store.Artifact{{
		ID:                 registry.NewID(build.CreatedAt.Add(time.Second)),
		ExternalIdentifier: "sha256:sealed-artifact", Region: "lab",
		CreatedAt: build.CreatedAt.Add(time.Second),
	}}
	// Completion drives the whole chain: build MAC, artifact MAC, version
	// completion MAC, and the managed-latest auto-assignment MAC.
	if _, err := repository.UpdateBuild(ctx, tenant, bucketName, fingerprint, *build,
		build.CreatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("UpdateBuild to done: %v", err)
	}

	version, err := repository.GetVersion(ctx, tenant, bucketName, fingerprint)
	if err != nil {
		t.Fatalf("GetVersion after completion: %v", err)
	}
	if !version.Complete() {
		t.Fatal("version did not complete")
	}
	history, err := repository.ListChannelAssignmentHistory(ctx, tenant, bucketName, "latest")
	if err != nil {
		t.Fatalf("ListChannelAssignmentHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("assignment history rows = %d, want the auto-assignment", len(history))
	}

	// Revocation re-seals the row: the write succeeds and reads still verify.
	if _, err := repository.RevokeVersion(ctx, tenant, bucketName, fingerprint,
		store.RevocationRequest{
			RevokeAt: build.CreatedAt.Add(2 * time.Minute), Message: "sealed", Author: "ops",
		},
		func(*registry.Version) string { return "v1" },
		build.CreatedAt.Add(2*time.Minute),
	); err != nil {
		t.Fatalf("RevokeVersion under encryption: %v", err)
	}
	revoked, err := repository.GetVersion(ctx, tenant, bucketName, fingerprint)
	if err != nil {
		t.Fatalf("GetVersion after revocation: %v", err)
	}
	if revoked.Revocation() == nil {
		t.Fatal("revocation did not persist")
	}
	// The channel read is the path packer's data sources consume, and its
	// SELECT carries its own column list — it must verify the re-sealed row
	// and surface the revocation (found in review: the columns were missing,
	// so encrypted channel reads of a revoked version failed verification).
	latestChannel, err := repository.GetChannel(ctx, tenant, bucketName, "latest")
	if err != nil {
		t.Fatalf("GetChannel latest after revocation: %v", err)
	}
	if latestChannel.Version == nil || latestChannel.Version.Revocation() == nil {
		t.Fatal("the latest channel's nested version lost its revocation")
	}

	// Mutation: forge the revocation author. The read must refuse, not serve it.
	forge, err := store.BeginTenant(ctx, db, orgA, projectA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := forge.Exec(`UPDATE versions SET revocation_author = 'forged-revoker' WHERE fingerprint = $1`,
		fingerprint); err != nil {
		t.Fatalf("alter revocation author: %v", err)
	}
	if err := forge.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetVersion(ctx, tenant, bucketName, fingerprint); err == nil {
		t.Fatal("forged revocation author was served")
	} else if !strings.Contains(err.Error(), "integrity verification failed") {
		t.Fatalf("forged revocation error = %v, want integrity failure", err)
	}

	// Mutation: alter the version's author — a provenance lie — directly in
	// the database. The read must refuse, not serve it.
	tx, err := store.BeginTenant(ctx, db, orgA, projectA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE versions SET author_id = 'forged-author' WHERE fingerprint = $1`,
		fingerprint); err != nil {
		t.Fatalf("alter version row: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetVersion(ctx, tenant, bucketName, fingerprint); err == nil {
		t.Fatal("altered version row was served")
	} else if !strings.Contains(err.Error(), "integrity verification failed") {
		t.Fatalf("altered version error = %v, want integrity failure", err)
	}
}

// Fixture hash captured from the break-glass runbook command (see
// TestBreakGlassRunbookMintsARootThatSignsIn in cmd/dufflebag); the plaintext
// is "break-glass-test-secret".
const handInsertedHash = "$argon2id$v=19$m=65536,t=2,p=1$YnJlYWtnbGFzc3NhbHQwMQ$6JE7aLqQDbhrwraovLkpDLrVDJoIUJetdNPFriO1lCg"

// The write-side counterpart of ADR-0018 (ADR-0024): on an encrypted
// deployment a hand-inserted root fails to load at authentication, so
// database write access is not administration. The same rows load fine on an
// unencrypted repository — the floor deployment.md states honestly.
func TestHandInsertedPrincipalFailsToLoadOnEncryptedDeployments(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()

	clientID := "hand-inserted-root"
	if _, err := db.Exec(`
		INSERT INTO principals (id, name, client_id, organization_id, project_id, role, created_at)
		VALUES ('p-hand', 'psql administrator', $1, NULL, NULL, 'root', now())
	`, clientID); err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO principal_secrets (id, principal_id, encoded_hash, created_at)
		VALUES ('s-hand', 'p-hand', $1, now())
	`, handInsertedHash); err != nil {
		t.Fatalf("insert secret: %v", err)
	}

	unencrypted := store.NewRepository(db)
	if _, err := unencrypted.GetPrincipalByClientID(ctx, clientID); err != nil {
		t.Fatalf("unencrypted floor: hand-inserted principal should load, got %v", err)
	}

	encrypted := store.NewRepository(db)
	encrypted.SetKeyring(testRing(t))
	if _, err := encrypted.GetPrincipalByClientID(ctx, clientID); !errors.Is(err, identity.ErrIntegrity) {
		t.Fatalf("encrypted deployment loaded a hand-inserted principal: %v", err)
	}
}

// The acceptance's named mutation: a sabotaged MAC key makes authentication
// fail. A principal written under one keyring must refuse to load under
// another — which is also what proves verification actually consults the key.
func TestSabotagedMACKeyFailsAuthentication(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	repository := store.NewRepository(db)
	repository.SetKeyring(testRing(t))

	principal, err := identity.NewPrincipal(
		"p-sealed", "sealed principal", "sealed-client",
		identity.Scope{}, identity.RoleRoot, time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := principal.IssueSecret("s-sealed", nil, principal.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreatePrincipal(ctx, principal); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := repository.GetPrincipalByClientID(ctx, "sealed-client"); err != nil {
		t.Fatalf("load under the writing keyring: %v", err)
	}

	// SABOTAGE: a different integrity key. Every identity row now fails.
	repository.SetKeyring(testRing(t))
	if _, err := repository.GetPrincipalByClientID(ctx, "sealed-client"); !errors.Is(err, identity.ErrIntegrity) {
		t.Fatalf("sabotaged MAC key still authenticated: %v", err)
	}
}

// duf-2rw: expiry is authority — stretching it with database access must fail
// verification, exactly like forging a hash.
func TestStretchedSecretExpiryFailsIntegrity(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()

	repository := store.NewRepository(db)
	repository.SetKeyring(testRing(t))
	at := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	principal, err := identity.NewPrincipal(
		"principal-stretch", "stretch", "client-stretch",
		identity.Scope{
			OrganizationID: uuid.MustParse(orgA),
			ProjectID:      uuid.MustParse(projectA),
		},
		identity.RoleBuilder, at,
	)
	if err != nil {
		t.Fatal(err)
	}
	expiry := at.Add(time.Hour)
	if _, err := principal.IssueSecret("secret-stretch", &expiry, at); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreatePrincipal(ctx, principal); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := repository.GetPrincipalByClientID(ctx, "client-stretch"); err != nil {
		t.Fatalf("sealed principal must load before tampering: %v", err)
	}

	if _, err := db.Exec(
		`UPDATE principal_secrets SET expires_at = $1 WHERE id = 'secret-stretch'`,
		at.Add(1000*time.Hour),
	); err != nil {
		t.Fatalf("stretch expiry: %v", err)
	}
	if _, err := repository.GetPrincipalByClientID(ctx, "client-stretch"); !errors.Is(err, identity.ErrIntegrity) {
		t.Fatalf("stretched expiry loaded anyway: %v", err)
	}
}
