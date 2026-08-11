//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/bagdrop"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	platform "github.com/benemon/dufflebag/internal/platform/v1"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

func TestBagDropRepositoryLifecycle(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	ctx := context.Background()
	createdAt := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	record := &bagdrop.Record{
		OrganizationID: orgA, ProjectID: projectA, Adapter: bagdrop.AdapterHCPPacker,
		HCPPacker: bagdrop.HCPPackerConfig{
			OrganizationID: "hcp-org", ProjectID: "hcp-project", ClientID: "client",
		},
		SealedSecret: []byte("sealed-credential"), CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	stored, err := repository.PutBagDropConfig(ctx, record)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if stored.Enabled || stored.HCPPacker != record.HCPPacker || string(stored.SealedSecret) != "sealed-credential" {
		t.Fatalf("stored = %#v", stored)
	}
	failed := bagdrop.VerificationResult{
		Outcome: bagdrop.OutcomeFailed, Reason: bagdrop.ReasonProjectNotFound,
	}
	verifiedAt := createdAt.Add(time.Minute)
	stored, err = repository.RecordBagDropVerification(ctx, orgA, projectA, failed, verifiedAt)
	if err != nil || stored.LastVerification == nil ||
		stored.LastVerification.Reason != bagdrop.ReasonProjectNotFound ||
		!stored.LastVerification.VerifiedAt.Equal(verifiedAt) {
		t.Fatalf("record verification = %#v, %v", stored, err)
	}
	resolved := bagdrop.VerificationResult{Outcome: bagdrop.OutcomeResolved}
	stored, err = repository.SetBagDropEnabled(ctx, orgA, projectA, true, &resolved, verifiedAt.Add(time.Minute))
	if err != nil || !stored.Enabled || stored.LastVerification.Outcome != bagdrop.OutcomeResolved {
		t.Fatalf("enable = %#v, %v", stored, err)
	}
	stored, err = repository.SetBagDropEnabled(ctx, orgA, projectA, false, nil, verifiedAt.Add(2*time.Minute))
	if err != nil || stored.Enabled {
		t.Fatalf("disable = %#v, %v", stored, err)
	}
	if err := repository.DeleteBagDropConfig(ctx, orgA, projectA); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repository.GetBagDropConfig(ctx, orgA, projectA); !errors.Is(err, bagdrop.ErrNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
}

func TestBagDropRepositoryRLSConcealsOtherProject(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	record := &bagdrop.Record{
		OrganizationID: orgA, ProjectID: projectA, Adapter: bagdrop.AdapterHCPPacker,
		HCPPacker: bagdrop.HCPPackerConfig{
			OrganizationID: "hcp-org", ProjectID: "hcp-project", ClientID: "client",
		},
		SealedSecret: []byte("sealed"), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if _, err := repository.PutBagDropConfig(context.Background(), record); err != nil {
		t.Fatalf("put tenant A: %v", err)
	}
	if _, err := repository.GetBagDropConfig(context.Background(), orgB, projectB); !errors.Is(err, bagdrop.ErrNotFound) {
		t.Fatalf("tenant B read tenant A = %v", err)
	}
}

func TestBagDropServiceAgainstPostgres(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	adapter := &integrationBagDropAdapter{}
	service := bagdrop.NewService(
		repository,
		bagdrop.NewCredentialSealer(nil, "0123456789abcdef0123456789abcdef"),
		bagdrop.Registry{bagdrop.AdapterHCPPacker: adapter},
	)
	secret := "postgres-destination-secret"
	config, verification, err := service.Put(context.Background(), orgA, projectA, bagdrop.Write{
		Adapter: bagdrop.AdapterHCPPacker,
		HCPPacker: bagdrop.HCPPackerConfig{
			OrganizationID: "hcp-org", ProjectID: "hcp-project", ClientID: "client",
		},
		ClientSecret: &secret,
	})
	if err != nil || verification != nil || !config.SecretSet || config.Enabled {
		t.Fatalf("put = %#v, %#v, %v", config, verification, err)
	}
	result, err := service.Verify(context.Background(), orgA, projectA)
	if err != nil || result.Outcome != bagdrop.OutcomeResolved || adapter.secret != secret {
		t.Fatalf("verify = %#v, %v, adapter secret %q", result, err, adapter.secret)
	}
	config, verification, err = service.Enable(context.Background(), orgA, projectA)
	if err != nil || verification != nil || !config.Enabled || config.LastVerification == nil {
		t.Fatalf("enable = %#v, %#v, %v", config, verification, err)
	}
	if err := service.Delete(context.Background(), orgA, projectA); !errors.Is(err, bagdrop.ErrEnabled) {
		t.Fatalf("delete enabled = %v", err)
	}
	config, err = service.Disable(context.Background(), orgA, projectA)
	if err != nil || config.Enabled {
		t.Fatalf("disable = %#v, %v", config, err)
	}
	if err := service.Delete(context.Background(), orgA, projectA); err != nil {
		t.Fatalf("delete disabled: %v", err)
	}
}

func TestBagDropAssociationRepositoryLifecycleAndBucketIndependence(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	ctx := context.Background()
	createdAt := time.Date(2026, 8, 10, 13, 0, 0, 0, time.UTC)
	config := integrationBagDropRecord(createdAt)
	if _, err := repository.PutBagDropConfig(ctx, config); err != nil {
		t.Fatalf("put config: %v", err)
	}
	insertPinBucket(t, repository, "images")
	association := bagdrop.Association{
		OrganizationID: orgA, ProjectID: projectA, BucketName: "images",
		State: bagdrop.AssociationActive, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	stored, err := repository.PutBagDropAssociation(ctx, association)
	if err != nil || stored.State != bagdrop.AssociationActive {
		t.Fatalf("put association = %#v, %v", stored, err)
	}
	idempotent := association
	idempotent.UpdatedAt = createdAt.Add(time.Minute)
	stored, err = repository.PutBagDropAssociation(ctx, idempotent)
	if err != nil || !stored.UpdatedAt.Equal(createdAt) {
		t.Fatalf("idempotent association = %#v, %v", stored, err)
	}
	if err := repository.DeleteBucket(ctx, store.ParseTenant(orgA, projectA), "images"); err != nil {
		t.Fatalf("delete local bucket: %v", err)
	}
	listed, err := repository.ListBagDropAssociations(ctx, orgA, projectA)
	if err != nil || len(listed) != 1 || listed[0].BucketName != "images" {
		t.Fatalf("association after bucket delete = %#v, %v", listed, err)
	}
	outcome, err := repository.RemoveBagDropAssociation(
		ctx, orgA, projectA, "images", createdAt.Add(2*time.Minute),
	)
	if err != nil || outcome != bagdrop.RemovedClean {
		t.Fatalf("remove clean = %q, %v", outcome, err)
	}
	listed, err = repository.ListBagDropAssociations(ctx, orgA, projectA)
	if err != nil || len(listed) != 0 {
		t.Fatalf("after clean remove = %#v, %v", listed, err)
	}
}

func TestBagDropAttemptedAssociationTombstoneAndDeleteGuard(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	ctx := context.Background()
	createdAt := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	if _, err := repository.PutBagDropConfig(ctx, integrationBagDropRecord(createdAt)); err != nil {
		t.Fatalf("put config: %v", err)
	}
	attemptedAt := createdAt.Add(time.Minute)
	if _, err := repository.PutBagDropAssociation(ctx, bagdrop.Association{
		OrganizationID: orgA, ProjectID: projectA, BucketName: "images",
		State: bagdrop.AssociationActive, FirstAttemptedAt: &attemptedAt,
		CreatedAt: createdAt, UpdatedAt: attemptedAt,
	}); err != nil {
		t.Fatalf("seed attempted association through repository: %v", err)
	}
	outcome, err := repository.RemoveBagDropAssociation(
		ctx, orgA, projectA, "images", attemptedAt.Add(time.Minute),
	)
	if err != nil || outcome != bagdrop.RemovalPending {
		t.Fatalf("remove attempted = %q, %v", outcome, err)
	}
	listed, err := repository.ListBagDropAssociations(ctx, orgA, projectA)
	if err != nil || len(listed) != 1 || listed[0].State != bagdrop.AssociationPendingRemoval {
		t.Fatalf("tombstone = %#v, %v", listed, err)
	}
	retriedAt := attemptedAt.Add(2 * time.Minute)
	if err := repository.MarkBagDropAssociationAttempt(ctx, orgA, projectA, "images", retriedAt); err != nil {
		t.Fatalf("mark tombstone retry: %v", err)
	}
	if err := repository.RecordBagDropAssociationFailure(
		ctx, orgA, projectA, "images", "HTTP 500: delete refused", retriedAt,
	); err != nil {
		t.Fatalf("record tombstone failure: %v", err)
	}
	listed, err = repository.ListBagDropAssociations(ctx, orgA, projectA)
	if err != nil || len(listed) != 1 || listed[0].LastSyncError == nil ||
		*listed[0].LastSyncError != "HTTP 500: delete refused" {
		t.Fatalf("failed tombstone retry = %#v, %v", listed, err)
	}
	service := bagdrop.NewService(repository, bagdrop.NewCredentialSealer(nil, ""), nil)
	if err := service.Delete(ctx, orgA, projectA); !errors.Is(err, bagdrop.ErrCleanupPending) {
		t.Fatalf("delete with tombstone = %v", err)
	}
}

func TestBagDropConsumedTombstoneUnblocksConfigDelete(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	broker, err := audit.NewBroker(logger)
	if err != nil {
		t.Fatal(err)
	}
	// Fixture source: internal/compat/hcpauth/handler.go tokenResponse and
	// docs/compatibility.md section 3.
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"fixture-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer auth.Close()
	remoteImages, foreignBucket := true, true
	managedLatestDelete := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/buckets"):
			// Fixture source: internal/compat/hcp2023 ListBuckets response.
			_, _ = w.Write([]byte(`{"buckets":[],"pagination":{}}`))
		case request.Method == http.MethodDelete && strings.HasSuffix(request.URL.Path, "/buckets/images"):
			remoteImages = false
			// Fixture source: the vendored DeleteBucketResponse is an empty object.
			_, _ = w.Write([]byte(`{}`))
		case request.Method == http.MethodDelete && strings.Contains(request.URL.Path, "/channels/latest"):
			managedLatestDelete = true
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":3,"message":"managed channel","details":[]}`))
		default:
			t.Fatalf("unexpected fake destination request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer api.Close()

	sealer := bagdrop.NewCredentialSealer(nil, "0123456789abcdef0123456789abcdef")
	adapters := bagdrop.Registry{
		bagdrop.AdapterHCPPacker: bagdrop.NewHCPPackerAdapter(auth.URL, api.URL),
	}
	service := bagdrop.NewService(repository, sealer, adapters)
	secret := "destination-secret"
	if _, _, err := service.Put(ctx, orgA, projectA, bagdrop.Write{
		Adapter: bagdrop.AdapterHCPPacker,
		HCPPacker: bagdrop.HCPPackerConfig{
			OrganizationID: "hcp-org", ProjectID: "hcp-project", ClientID: "client",
		},
		ClientSecret: &secret,
	}); err != nil {
		t.Fatalf("put config: %v", err)
	}
	if _, _, err := service.Enable(ctx, orgA, projectA); err != nil {
		t.Fatalf("enable config: %v", err)
	}
	insertPinBucket(t, repository, "images")
	if _, err := service.Associate(ctx, orgA, projectA, "images"); err != nil {
		t.Fatalf("associate: %v", err)
	}
	attemptedAt := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	if err := repository.MarkBagDropAssociationAttempt(ctx, orgA, projectA, "images", attemptedAt); err != nil {
		t.Fatalf("mark sync attempt: %v", err)
	}
	if outcome, err := service.Unassociate(ctx, orgA, projectA, "images"); err != nil || outcome != bagdrop.RemovalPending {
		t.Fatalf("unassociate = %q, %v", outcome, err)
	}
	if err := service.Delete(ctx, orgA, projectA); !errors.Is(err, bagdrop.ErrEnabled) {
		t.Fatalf("enabled delete guard = %v", err)
	}
	reconciler, err := bagdrop.NewReconciler(repository, sealer, adapters, broker, time.Hour, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.ReconcileProject(ctx, bagdrop.Project{OrganizationID: orgA, ProjectID: projectA}); err != nil {
		t.Fatalf("consume tombstone: %v", err)
	}
	associations, err := service.ListAssociations(ctx, orgA, projectA)
	if err != nil || len(associations) != 0 {
		t.Fatalf("associations after reconcile = %#v, %v", associations, err)
	}
	if remoteImages || !foreignBucket || managedLatestDelete {
		t.Fatalf("destination invariants images=%v foreign=%v managed_latest_delete=%v",
			remoteImages, foreignBucket, managedLatestDelete)
	}
	if _, err := service.Disable(ctx, orgA, projectA); err != nil {
		t.Fatalf("disable config: %v", err)
	}
	if err := service.Delete(ctx, orgA, projectA); err != nil {
		t.Fatalf("delete config after consumed tombstone: %v", err)
	}
	if _, err := service.Get(ctx, orgA, projectA); !errors.Is(err, bagdrop.ErrNotFound) {
		t.Fatalf("get deleted config = %v", err)
	}
}

func TestBagDropAssociationReconcileStatusLifecycle(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	ctx := context.Background()
	createdAt := time.Date(2026, 8, 10, 14, 30, 0, 0, time.UTC)
	if _, err := repository.PutBagDropConfig(ctx, integrationBagDropRecord(createdAt)); err != nil {
		t.Fatalf("put config: %v", err)
	}
	if _, err := repository.PutBagDropAssociation(ctx, bagdrop.Association{
		OrganizationID: orgA, ProjectID: projectA, BucketName: "images",
		State: bagdrop.AssociationActive, CreatedAt: createdAt, UpdatedAt: createdAt,
	}); err != nil {
		t.Fatalf("put association: %v", err)
	}
	attemptedAt := createdAt.Add(time.Minute)
	if err := repository.MarkBagDropAssociationAttempt(ctx, orgA, projectA, "images", attemptedAt); err != nil {
		t.Fatalf("mark attempt: %v", err)
	}
	if err := repository.RecordBagDropAssociationFailure(
		ctx, orgA, projectA, "images", "HTTP 500: destination failed", attemptedAt,
	); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	listed, err := repository.ListBagDropAssociations(ctx, orgA, projectA)
	if err != nil || len(listed) != 1 || listed[0].FirstAttemptedAt == nil ||
		listed[0].LastAttemptAt == nil || listed[0].LastSyncError == nil ||
		listed[0].SyncStatus() != bagdrop.SyncPending {
		t.Fatalf("failed status = %#v, %v", listed, err)
	}
	syncedAt := attemptedAt.Add(time.Minute)
	if err := repository.RecordBagDropAssociationSuccess(ctx, orgA, projectA, "images", syncedAt); err != nil {
		t.Fatalf("record success: %v", err)
	}
	listed, err = repository.ListBagDropAssociations(ctx, orgA, projectA)
	if err != nil || len(listed) != 1 || listed[0].LastSyncedAt == nil ||
		listed[0].LastSyncError != nil || listed[0].SyncStatus() != bagdrop.SyncSynced {
		t.Fatalf("successful status = %#v, %v", listed, err)
	}
}

func TestBagDropSnapshotExcludesManagedLatestTripwire(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	ctx := context.Background()
	tenant := store.ParseTenant(orgA, projectA)
	at := time.Date(2026, 8, 10, 15, 30, 0, 0, time.UTC)
	bucket, err := repository.CreateBucket(ctx, tenant, store.Bucket{
		ID: registry.NewID(at), Name: "images", Labels: map[string]string{}, CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	version, err := registry.NewVersion(
		registry.NewID(at.Add(time.Second)), bucket.Name, "fp-1", registry.TemplateHCL2, at.Add(time.Second),
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
	completed := *build
	completed.Status = registry.BuildDone
	completed.MetadataSeen = true
	if _, err := repository.UpdateBuild(
		ctx, tenant, bucket.Name, version.Fingerprint, completed, at.Add(3*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateChannel(ctx, tenant, store.Channel{
		ID: registry.NewID(at.Add(4 * time.Second)), BucketName: bucket.Name,
		Name: "production", CreatedAt: at.Add(4 * time.Second),
	}, version.Fingerprint, "publisher"); err != nil {
		t.Fatal(err)
	}
	revokeAt := at.Add(5 * time.Second)
	if _, err := repository.RevokeVersion(ctx, tenant, bucket.Name, version.Fingerprint,
		store.RevocationRequest{RevokeAt: revokeAt, Message: "superseded", Author: "publisher", SkipDescendants: true},
		func(*registry.Version) string { return "v1" }, revokeAt,
	); err != nil {
		t.Fatal(err)
	}

	snapshot, err := repository.GetBagDropBucketSnapshot(ctx, orgA, projectA, bucket.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Versions) != 1 || snapshot.Versions[0].Fingerprint != version.Fingerprint ||
		snapshot.Versions[0].RevokeAt == nil || !snapshot.Versions[0].RevokeAt.Equal(revokeAt) ||
		snapshot.Versions[0].RevocationMessage != "superseded" {
		t.Fatalf("completed versions snapshot = %#v", snapshot.Versions)
	}
	if len(snapshot.Channels) != 1 || snapshot.Channels[0].Name != "production" ||
		snapshot.Channels[0].AssignedVersionFingerprint == nil ||
		*snapshot.Channels[0].AssignedVersionFingerprint != version.Fingerprint {
		t.Fatalf("ordinary channel snapshot = %#v; managed latest must be absent", snapshot.Channels)
	}
}

func TestBagDropReconcileTriggerIntegration(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	broker, err := audit.NewBroker(logger)
	if err != nil {
		t.Fatal(err)
	}
	sealer := bagdrop.NewCredentialSealer(nil, "0123456789abcdef0123456789abcdef")
	adapter := &integrationBagDropAdapter{}
	adapters := bagdrop.Registry{bagdrop.AdapterHCPPacker: adapter}
	service := bagdrop.NewService(repository, sealer, adapters)
	reconciler, err := bagdrop.NewReconciler(repository, sealer, adapters, broker, time.Hour, logger)
	if err != nil {
		t.Fatal(err)
	}
	bagDropRuntime := &bagdrop.Runtime{Service: service, Reconciler: reconciler}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		reconciler.Run(ctx)
	}()
	<-reconciler.Started()
	defer func() {
		cancel()
		<-done
	}()

	principal, err := identity.NewPrincipal(
		"maintainer", "maintainer", "maintainer-client",
		identity.Scope{OrganizationID: uuid.MustParse(orgA), ProjectID: uuid.MustParse(projectA)},
		identity.RoleMaintainer, time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	secretID := "maintainer-secret"
	if _, err := principal.IssueSecret(secretID, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	handler := platform.NewHandler(
		repository, repository, auditAPIAuthenticator{principalID: principal.ID, secretID: secretID},
		auditAPIPrincipals{principal: principal}, logger, repository, broker,
		nil, nil, bagDropRuntime, platform.BuildInfo{},
	)
	call := func(project string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost,
			"/api/v1/organizations/"+orgA+"/projects/"+project+"/bagdrop/reconcile", nil)
		request.Header.Set("Authorization", "Bearer integration-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := call(projectA); response.Code != http.StatusAccepted {
		t.Fatalf("running trigger = %d: %s", response.Code, response.Body)
	}
	if response := call(projectB); response.Code != http.StatusNotFound {
		t.Fatalf("foreign trigger = %d: %s", response.Code, response.Body)
	}

	unavailable := platform.NewHandler(
		repository, repository, auditAPIAuthenticator{principalID: principal.ID, secretID: secretID},
		auditAPIPrincipals{principal: principal}, logger, repository, broker,
		nil, nil, service, platform.BuildInfo{},
	)
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/organizations/"+orgA+"/projects/"+projectA+"/bagdrop/reconcile", nil)
	request.Header.Set("Authorization", "Bearer integration-token")
	response := httptest.NewRecorder()
	unavailable.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("absent reconciler = %d: %s", response.Code, response.Body)
	}
}

func TestBagDropCleanAssociationsCascadeWithConfig(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	ctx := context.Background()
	createdAt := time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC)
	if _, err := repository.PutBagDropConfig(ctx, integrationBagDropRecord(createdAt)); err != nil {
		t.Fatalf("put config: %v", err)
	}
	if _, err := repository.PutBagDropAssociation(ctx, bagdrop.Association{
		OrganizationID: orgA, ProjectID: projectA, BucketName: "images",
		State: bagdrop.AssociationActive, CreatedAt: createdAt, UpdatedAt: createdAt,
	}); err != nil {
		t.Fatalf("put clean association: %v", err)
	}
	service := bagdrop.NewService(repository, bagdrop.NewCredentialSealer(nil, ""), nil)
	if err := service.Delete(ctx, orgA, projectA); err != nil {
		t.Fatalf("delete clean config: %v", err)
	}
	listed, err := repository.ListBagDropAssociations(ctx, orgA, projectA)
	if err != nil || len(listed) != 0 {
		t.Fatalf("associations after cascade = %#v, %v", listed, err)
	}
}

func integrationBagDropRecord(at time.Time) *bagdrop.Record {
	return &bagdrop.Record{
		OrganizationID: orgA, ProjectID: projectA, Adapter: bagdrop.AdapterHCPPacker,
		HCPPacker: bagdrop.HCPPackerConfig{
			OrganizationID: "hcp-org", ProjectID: "hcp-project", ClientID: "client",
		},
		SealedSecret: []byte("opaque-envelope"), CreatedAt: at, UpdatedAt: at,
	}
}

type integrationBagDropAdapter struct{ secret string }

func (a *integrationBagDropAdapter) Resolve(
	_ context.Context, destination bagdrop.Destination,
) bagdrop.VerificationResult {
	a.secret = destination.ClientSecret
	return bagdrop.VerificationResult{Outcome: bagdrop.OutcomeResolved}
}

func (*integrationBagDropAdapter) BeginReconcile(
	context.Context, bagdrop.Destination,
) (bagdrop.ReconcileRun, error) {
	panic("BeginReconcile is not used by configuration service integration tests")
}
