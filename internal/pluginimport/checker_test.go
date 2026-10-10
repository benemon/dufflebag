package pluginimport

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	store "github.com/benemon/dufflebag/internal/store/postgres"
)

type checkResult struct{ latest, tag, err string }

type fakeChecks struct {
	due      []store.ClaimedPluginUpdateCheck
	github   []bool
	recorded []checkResult
}

func (f *fakeChecks) ClaimPluginUpdateCheck(_ context.Context, _ time.Duration, includeGitHub bool) (*store.ClaimedPluginUpdateCheck, error) {
	f.github = append(f.github, includeGitHub)
	for i, claimed := range f.due {
		if includeGitHub || claimed.Source.Kind != "github" {
			f.due = append(f.due[:i], f.due[i+1:]...)
			return &claimed, nil
		}
	}
	return nil, nil
}

func (f *fakeChecks) RecordPluginUpdateCheck(_ context.Context, _ store.OrganizationTenant, _ uuid.UUID, latest, tag, checkErr string) error {
	f.recorded = append(f.recorded, checkResult{latest, tag, checkErr})
	return nil
}

// releasesAPI serves the captured first page of packer-plugin-amazon's
// releases (testdata, trimmed to the fields a check reads), from the entry
// at *from, so a test can advance upstream by moving it.
func releasesAPI(t *testing.T, from *int, edit func([]map[string]any)) *httptest.Server {
	t.Helper()
	captured, err := os.ReadFile("testdata/releases-api-packer-plugin-amazon-page1.json")
	if err != nil {
		t.Fatal(err)
	}
	var page []map[string]any
	if err := json.Unmarshal(captured, &page); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(page)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/releases/packer-plugin-amazon" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(page[*from:])
	}))
	t.Cleanup(server.Close)
	return server
}

func amazonCheck() store.ClaimedPluginUpdateCheck {
	return store.ClaimedPluginUpdateCheck{ID: uuid.New(), Name: "amazon", Source: store.PluginSource{Kind: "releases-hashicorp", Repository: "packer-plugin-amazon"}}
}

func TestCheckerRecordsTheNewestStableReleaseAsUpstreamAdvances(t *testing.T) {
	from := 1
	server := releasesAPI(t, &from, nil)
	checks := &fakeChecks{due: []store.ClaimedPluginUpdateCheck{amazonCheck()}}
	checker := NewChecker(checks, NewUpstream(server.Client(), server.URL, server.URL), nil, time.Hour, slog.Default())
	if worked, err := checker.RunOnce(context.Background()); !worked || err != nil {
		t.Fatalf("first check: worked=%v err=%v", worked, err)
	}
	from = 0
	checks.due = []store.ClaimedPluginUpdateCheck{amazonCheck()}
	if worked, err := checker.RunOnce(context.Background()); !worked || err != nil {
		t.Fatalf("second check: worked=%v err=%v", worked, err)
	}
	want := []checkResult{{"1.8.2", "1.8.2", ""}, {"1.8.3", "1.8.3", ""}}
	if len(checks.recorded) != 2 || checks.recorded[0] != want[0] || checks.recorded[1] != want[1] {
		t.Fatalf("recorded = %+v, want %+v", checks.recorded, want)
	}
	if worked, _ := checker.RunOnce(context.Background()); worked {
		t.Fatal("a check ran with nothing due")
	}
}

func TestCheckerSkipsPrereleasesAndRecordsFailuresQuietly(t *testing.T) {
	from := 0
	server := releasesAPI(t, &from, func(page []map[string]any) { page[0]["is_prerelease"] = true })
	checks := &fakeChecks{due: []store.ClaimedPluginUpdateCheck{amazonCheck()}}
	checker := NewChecker(checks, NewUpstream(server.Client(), server.URL, server.URL), nil, time.Hour, slog.Default())
	_, _ = checker.RunOnce(context.Background())
	if checks.recorded[0] != (checkResult{"1.8.2", "1.8.2", ""}) {
		t.Fatalf("recorded = %+v, want the newest stable release", checks.recorded[0])
	}

	server.Close()
	checks.due = []store.ClaimedPluginUpdateCheck{amazonCheck()}
	if worked, err := checker.RunOnce(context.Background()); !worked || err != nil {
		t.Fatalf("an unreachable upstream must be a recorded result, not a checker error: worked=%v err=%v", worked, err)
	}
	if failed := checks.recorded[1]; failed.latest != "" || failed.err == "" {
		t.Fatalf("unreachable upstream recorded %+v, want an error and no version", failed)
	}
}

func TestCheckerPacesGitHub(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/repos/limited/packer-plugin-git/releases/latest" {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v0.6.4","prerelease":false,"assets":[]}`))
	}))
	t.Cleanup(server.Close)
	github := func(repository string) store.ClaimedPluginUpdateCheck {
		return store.ClaimedPluginUpdateCheck{ID: uuid.New(), Name: "git", Source: store.PluginSource{Kind: "github", Repository: repository}}
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	checks := &fakeChecks{due: []store.ClaimedPluginUpdateCheck{github("ethanmdavidson/packer-plugin-git"), github("other/packer-plugin-git")}}
	checker := NewChecker(checks, nil, NewGitHub(server.Client(), server.URL), time.Hour, slog.Default())
	checker.now = func() time.Time { return now }

	_, _ = checker.RunOnce(context.Background())
	if checks.recorded[0] != (checkResult{"0.6.4", "v0.6.4", ""}) {
		t.Fatalf("recorded = %+v", checks.recorded[0])
	}
	if worked, _ := checker.RunOnce(context.Background()); worked || checks.github[1] {
		t.Fatal("a second GitHub check ran inside the pacing gap")
	}
	now = now.Add(githubCheckGap)
	if worked, _ := checker.RunOnce(context.Background()); !worked {
		t.Fatal("the paced GitHub check did not run once the gap passed")
	}

	checks.due = []store.ClaimedPluginUpdateCheck{github("limited/packer-plugin-git"), github("ethanmdavidson/packer-plugin-git")}
	now = now.Add(githubCheckGap)
	_, _ = checker.RunOnce(context.Background())
	now = now.Add(30 * time.Minute)
	if worked, _ := checker.RunOnce(context.Background()); worked {
		t.Fatal("a GitHub check ran before the rate limit could have reset")
	}
	if calls != 3 {
		t.Fatalf("GitHub was called %d times, want 3", calls)
	}
}
