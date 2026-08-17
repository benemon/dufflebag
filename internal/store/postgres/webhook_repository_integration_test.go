//go:build integration

package postgres_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/credseal"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/benemon/dufflebag/internal/webhook"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const integrationWebhookKey = "0123456789abcdef0123456789abcdef"

type receivedWebhook struct {
	headers http.Header
	body    []byte
}

type refusingWebhookDialer struct {
	dials *atomic.Int32
}

func (d refusingWebhookDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	d.dials.Add(1)
	return nil, errors.New("test dial refused")
}

func TestWebhookVerificationDispatchSignatureAndActivationGating(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	var mu sync.Mutex
	var received []receivedWebhook
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		mu.Lock()
		received = append(received, receivedWebhook{headers: request.Header.Clone(), body: body})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	sealer := credseal.New(nil, integrationWebhookKey)
	service := webhook.NewService(repository, sealer, webhook.NewHTTPClient(true, nil, nil))
	record, err := service.Create(context.Background(), orgA, projectA, webhook.Create{
		Name: "receiver", URL: receiver.URL, Secret: "delivery-secret",
		Events: []string{webhook.OperationBucketCreated},
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.State != webhook.StateActive {
		t.Fatalf("state = %s, want active", record.State)
	}

	at := time.Now().UTC()
	ctx := identity.WithActor(context.Background(), "principal-1", "Builder One")
	if _, err := repository.CreateBucket(ctx, store.ParseTenant(orgA, projectA), store.Bucket{
		ID: registry.NewID(at), Name: "webhook-bucket", Description: "fixture", Labels: map[string]string{}, CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := webhook.NewDispatcher(
		repository, sealer, webhook.NewHTTPClient(true, nil, nil), time.Second, time.Millisecond,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.DispatchProject(context.Background(), webhook.Project{OrganizationID: orgA, ProjectID: projectA}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatalf("receiver requests = %d, want handshake + delivery", len(received))
	}
	delivery := received[1]
	if delivery.headers.Get(webhook.EventHeader) != webhook.OperationBucketCreated ||
		delivery.headers.Get(webhook.DeliveryHeader) == "" {
		t.Fatalf("delivery headers = %#v", delivery.headers)
	}
	mac := hmac.New(sha512.New, []byte("delivery-secret"))
	_, _ = mac.Write(delivery.body)
	wantSignature := "sha512=" + hex.EncodeToString(mac.Sum(nil))
	if got := delivery.headers.Get(webhook.SignatureHeader); got != wantSignature {
		t.Fatalf("signature = %q, want %q", got, wantSignature)
	}
	var envelope webhook.Envelope
	if err := json.Unmarshal(delivery.body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Operation != webhook.OperationBucketCreated || envelope.Target.Bucket != "webhook-bucket" ||
		envelope.Actor.PrincipalID != "principal-1" || envelope.Actor.Name != "Builder One" {
		t.Fatalf("envelope = %#v", envelope)
	}
}

func TestWebhookPendingReceivesNothing(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer receiver.Close()
	service := webhook.NewService(repository, credseal.New(nil, integrationWebhookKey), webhook.NewHTTPClient(true, nil, nil))
	record, err := service.Create(context.Background(), orgA, projectA, webhook.Create{
		Name: "pending", URL: receiver.URL, Events: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.State != webhook.StatePending {
		t.Fatalf("state = %s, want pending", record.State)
	}
	at := time.Now().UTC()
	if _, err := repository.CreateBucket(context.Background(), store.ParseTenant(orgA, projectA), store.Bucket{
		ID: registry.NewID(at), Name: "pending-bucket", Labels: map[string]string{}, CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	tx, err := store.BeginTenant(context.Background(), db, orgA, projectA, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var events, deliveries int
	if err := tx.QueryRow(`SELECT count(*) FROM webhook_outbox WHERE operation = 'bucket.created'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`SELECT count(*) FROM webhook_deliveries WHERE operation = 'bucket.created'`).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if events != 0 || deliveries != 0 {
		t.Fatalf("pending webhook received event: outbox=%d deliveries=%d", events, deliveries)
	}
}

func TestWebhookSSRFMetadataAddressRefusalIsRecordedWithoutDial(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	var dials atomic.Int32
	client := webhook.NewHTTPClient(false, nil, refusingWebhookDialer{dials: &dials})
	service := webhook.NewService(repository, credseal.New(nil, integrationWebhookKey), client)
	record, err := service.Create(context.Background(), orgA, projectA, webhook.Create{
		Name: "metadata", URL: "http://169.254.169.254/latest", Events: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.State != webhook.StatePending || record.LastVerificationError == nil ||
		!strings.Contains(*record.LastVerificationError, "private or local") {
		t.Fatalf("record = %#v", record)
	}
	deliveries, err := service.Deliveries(context.Background(), orgA, projectA, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || deliveries[0].Status != webhook.DeliveryRefused {
		t.Fatalf("deliveries = %#v", deliveries)
	}
	if got := dials.Load(); got != 0 {
		t.Fatalf("metadata address reached dialer %d times", got)
	}
}

func TestWebhookDispatcherBoundsRetriesAtFiveAttempts(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	var fail atomic.Bool
	var requests atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if fail.Load() {
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	sealer := credseal.New(nil, integrationWebhookKey)
	service := webhook.NewService(repository, sealer, webhook.NewHTTPClient(true, nil, nil))
	record, err := service.Create(context.Background(), orgA, projectA, webhook.Create{
		Name: "retry", URL: receiver.URL, Events: []string{webhook.OperationBucketCreated},
	})
	if err != nil || record.State != webhook.StateActive {
		t.Fatalf("create = %#v, %v", record, err)
	}
	fail.Store(true)
	at := time.Now().UTC()
	if _, err := repository.CreateBucket(context.Background(), store.ParseTenant(orgA, projectA), store.Bucket{
		ID: registry.NewID(at), Name: "retry-bucket", Labels: map[string]string{}, CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	// The retry base must exceed a dispatch pass's own latency: DispatchProject
	// drains DUE work in a loop, so a backoff smaller than the loop's round-trip
	// makes a rescheduled delivery due again within the same pass.
	const retryBase = 300 * time.Millisecond
	dispatcher, err := webhook.NewDispatcher(repository, sealer, webhook.NewHTTPClient(true, nil, nil), time.Second, retryBase, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	project := webhook.Project{OrganizationID: orgA, ProjectID: projectA}
	if err := dispatcher.DispatchProject(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests after first attempt = %d, want handshake + one", got)
	}
	if err := dispatcher.DispatchProject(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("backoff allowed an early retry: requests = %d", got)
	}
	for attempt := 1; attempt < webhook.MaxAttempts; attempt++ {
		time.Sleep(retryBase<<(attempt-1) + 100*time.Millisecond)
		if err := dispatcher.DispatchProject(context.Background(), project); err != nil {
			t.Fatal(err)
		}
	}
	deliveries, err := service.Deliveries(context.Background(), orgA, projectA, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	var eventDelivery *webhook.Delivery
	for i := range deliveries {
		if deliveries[i].Operation == webhook.OperationBucketCreated {
			eventDelivery = &deliveries[i]
		}
	}
	if eventDelivery == nil || eventDelivery.Status != webhook.DeliveryFailed || eventDelivery.AttemptCount != webhook.MaxAttempts {
		t.Fatalf("event delivery = %#v", eventDelivery)
	}
	if err := dispatcher.DispatchProject(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != int32(webhook.MaxAttempts+1) {
		t.Fatalf("requests after terminal dispatch = %d, want handshake + %d attempts", got, webhook.MaxAttempts)
	}
}

func TestWebhookDeliveryRingPrunesBeyondOneHundred(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer receiver.Close()
	service := webhook.NewService(repository, credseal.New(nil, integrationWebhookKey), webhook.NewHTTPClient(true, nil, nil))
	record, err := service.Create(context.Background(), orgA, projectA, webhook.Create{
		Name: "ring", URL: receiver.URL, Events: []string{webhook.OperationBucketCreated},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 105 {
		at := time.Now().UTC()
		if _, err := repository.CreateBucket(context.Background(), store.ParseTenant(orgA, projectA), store.Bucket{
			ID: registry.NewID(at), Name: fmt.Sprintf("ring-%03d", i), Labels: map[string]string{}, CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	deliveries, err := service.Deliveries(context.Background(), orgA, projectA, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 100 {
		t.Fatalf("delivery ring length = %d, want 100", len(deliveries))
	}
}

func TestWebhookOutboxLostEventTransactionality(t *testing.T) {
	db, appURL, cleanup := openTestDatabase(t)
	defer cleanup()
	repository := store.NewRepository(db)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer receiver.Close()
	service := webhook.NewService(repository, credseal.New(nil, integrationWebhookKey), webhook.NewHTTPClient(true, nil, nil))
	if _, err := service.Create(context.Background(), orgA, projectA, webhook.Create{Name: "rollback", URL: receiver.URL, Events: []string{}}); err != nil {
		t.Fatal(err)
	}
	adminURL, err := url.Parse(appURL)
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
		CREATE FUNCTION reject_webhook_outbox() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced outbox rollback'; END $$;
		CREATE TRIGGER reject_webhook_outbox BEFORE INSERT ON webhook_outbox
		FOR EACH ROW EXECUTE FUNCTION reject_webhook_outbox();
	`); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	_, err = repository.CreateBucket(context.Background(), store.ParseTenant(orgA, projectA), store.Bucket{
		ID: registry.NewID(at), Name: "rolled-back-bucket", Labels: map[string]string{}, CreatedAt: at,
	})
	if err == nil || !strings.Contains(err.Error(), "forced outbox rollback") {
		t.Fatalf("CreateBucket error = %v, want forced rollback", err)
	}
	tx, err := store.BeginTenant(context.Background(), db, orgA, projectA, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var buckets, events int
	if err := tx.QueryRow(`SELECT count(*) FROM buckets WHERE name = 'rolled-back-bucket'`).Scan(&buckets); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`SELECT count(*) FROM webhook_outbox WHERE operation = 'bucket.created'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if buckets != 0 || events != 0 {
		t.Fatalf("forced rollback left bucket=%d event=%d", buckets, events)
	}
}
