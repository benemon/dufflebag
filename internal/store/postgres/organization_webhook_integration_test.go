//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/credseal"
	"github.com/benemon/dufflebag/internal/pluginimport"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/benemon/dufflebag/internal/webhook"
)

// A stand-in releases API whose newest release a test advances, delivering to
// organization webhooks through the real checker, outbox and dispatcher.
func TestPluginUpdateAvailableIsDeliveredOncePerNewVersion(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	_, objects := openTestObjectStore(t)
	ctx := context.Background()
	repository := store.NewRepositoryWithObjectStore(db, objects)
	tenant := store.ParseOrganizationTenant(orgA)
	if _, err := repository.EnablePluginRegistry(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishPluginVersion(ctx, tenant, pluginVersion("1.0.0", store.PluginSource{Kind: "releases-hashicorp", Repository: "packer-plugin-probe"})); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetPluginUpdateCheck(ctx, tenant, "probe", true); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	newest := "1.0.0"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode([]map[string]any{{"version": newest, "is_prerelease": false, "timestamp_created": time.Now()}})
	}))
	defer upstream.Close()
	var deliveries [][]byte
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get(webhook.EventHeader) == webhook.OperationPluginUpdateAvailable {
			mu.Lock()
			deliveries = append(deliveries, body)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	sealer := credseal.New(nil, integrationWebhookKey)
	client := webhook.NewHTTPClient(true, nil, nil)
	service := webhook.NewService(repository, sealer, client)
	if _, err := service.Create(ctx, orgA, "", webhook.Create{
		Name: "registry", URL: receiver.URL, Secret: "org-secret", Events: []string{webhook.OperationVersionCreated},
	}); !errors.Is(err, webhook.ErrInvalid) {
		t.Fatalf("an organization webhook subscribed to a project event: %v", err)
	}
	if _, err := service.Create(ctx, orgA, projectA, webhook.Create{
		Name: "project", URL: receiver.URL, Events: []string{webhook.OperationPluginUpdateAvailable},
	}); !errors.Is(err, webhook.ErrInvalid) {
		t.Fatalf("a project webhook subscribed to a plugin event: %v", err)
	}
	record, err := service.Create(ctx, orgA, "", webhook.Create{
		Name: "registry", URL: receiver.URL, Secret: "org-secret", Events: []string{webhook.OperationPluginUpdateAvailable},
	})
	if err != nil || record.State != webhook.StateActive || record.ProjectID != "" {
		t.Fatalf("organization webhook = %+v, %v", record, err)
	}
	if _, err := service.Create(ctx, orgB, "", webhook.Create{Name: "other", URL: receiver.URL}); err != nil {
		t.Fatal(err)
	}
	if listed, _ := service.List(ctx, orgB, ""); len(listed) != 1 {
		t.Fatalf("organization B sees %d webhooks, want only its own", len(listed))
	}

	checker := pluginimport.NewChecker(repository, pluginimport.NewUpstream(upstream.Client(), upstream.URL, upstream.URL), nil, time.Nanosecond, slog.Default())
	dispatcher, err := webhook.NewDispatcher(repository, sealer, client, time.Second, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		if worked, err := checker.RunOnce(ctx); !worked || err != nil {
			t.Fatalf("check: worked=%v err=%v", worked, err)
		}
		for _, organization := range []string{orgA, orgB} {
			if err := dispatcher.DispatchProject(ctx, webhook.Project{OrganizationID: organization}); err != nil {
				t.Fatal(err)
			}
		}
	}
	check()
	check()
	mu.Lock()
	newest = "1.1.0"
	mu.Unlock()
	check()
	check()

	mu.Lock()
	defer mu.Unlock()
	if len(deliveries) != 1 {
		t.Fatalf("plugin.update_available deliveries = %d, want exactly one for 1.1.0", len(deliveries))
	}
	var envelope struct {
		webhook.Envelope
		ProjectID *string `json:"project_id"`
		Payload   struct {
			Plugin       string `json:"plugin"`
			Latest       string `json:"latest"`
			NewestStored string `json:"newest_stored"`
			Source       struct{ Kind, Repository string }
		} `json:"payload"`
	}
	if err := json.Unmarshal(deliveries[0], &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.OrganizationID != orgA || envelope.ProjectID != nil || envelope.Target.Type != "plugin" || envelope.Target.Name != "probe" ||
		envelope.Payload.Latest != "1.1.0" || envelope.Payload.NewestStored != "1.0.0" || envelope.Payload.Source.Repository != "packer-plugin-probe" {
		t.Fatalf("delivery = %s", deliveries[0])
	}
	history, err := service.Deliveries(ctx, orgA, "", record.ID)
	if err != nil || len(history) != 2 || history[0].Status != webhook.DeliveryDelivered {
		t.Fatalf("delivery history = %+v, %v; want the event and the handshake", history, err)
	}
}
