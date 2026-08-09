package objectstore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewRequiresCompleteConfiguration(t *testing.T) {
	_, err := New(Config{})
	want := "endpoint, region, bucket, access_key, secret_key must be set"
	if err == nil || err.Error() != want {
		t.Fatalf("New empty configuration = %v, want %q", err, want)
	}

	_, err = New(Config{
		Endpoint: "not-a-url", Region: "us-east-1", Bucket: "sboms",
		AccessKey: "access", SecretKey: "secret",
	})
	if err == nil || !strings.Contains(err.Error(), "absolute HTTP(S) URL") {
		t.Fatalf("New invalid endpoint = %v", err)
	}

	_, err = New(Config{
		Endpoint: "ftp://127.0.0.1", Region: "us-east-1", Bucket: "sboms",
		AccessKey: "access", SecretKey: "secret",
	})
	if err == nil || !strings.Contains(err.Error(), "absolute HTTP(S) URL") {
		t.Fatalf("New non-HTTP endpoint = %v", err)
	}
}

func TestCheckBucketRetriesTemporaryFailure(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead || r.URL.Path != "/sboms" {
			t.Fatalf("bucket check = %s %s, want HEAD /sboms", r.Method, r.URL.Path)
		}
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	objects := testStore(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := objects.CheckBucket(ctx); err != nil {
		t.Fatalf("CheckBucket after temporary failures: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("bucket attempts = %d, want 3", got)
	}
}

// The health probe reads what the last operation saw rather than asking the
// store itself, so an operation that fails has to record the failure. A store
// that only ever remembers success would report a healthy bucket forever.
func TestOperationsRecordWhatTheyObserved(t *testing.T) {
	var reachable atomic.Bool
	reachable.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !reachable.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	objects := testStore(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := objects.CheckBucket(ctx); err != nil {
		t.Fatalf("CheckBucket against a healthy bucket: %v", err)
	}
	if !objects.Reachable() {
		t.Fatal("a bucket that answered is reported unreachable")
	}

	reachable.Store(false)
	if err := objects.Put(ctx, "any-key", []byte("payload")); err == nil {
		t.Fatal("put against a failing bucket did not error")
	}
	if objects.Reachable() {
		t.Fatal("a bucket that stopped answering is still reported reachable")
	}

	reachable.Store(true)
	if err := objects.CheckBucket(ctx); err != nil {
		t.Fatalf("CheckBucket after recovery: %v", err)
	}
	if !objects.Reachable() {
		t.Fatal("a recovered bucket is still reported unreachable")
	}
}

// A store can accept a request and fail while streaming the answer. Observing
// only the accepted request would report a healthy store to the probe at the
// moment a download is failing.
func TestGetObservesAFailureThatArrivesMidStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		// Promise more than is delivered, then hang up: the request succeeded,
		// the body did not.
		w.Header().Set("Content-Length", "64")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("short")); err != nil {
			return
		}
		if hijacker, ok := w.(http.Hijacker); ok {
			connection, _, err := hijacker.Hijack()
			if err == nil {
				_ = connection.Close()
			}
		}
	}))
	defer server.Close()

	objects := testStore(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := objects.CheckBucket(ctx); err != nil {
		t.Fatalf("CheckBucket: %v", err)
	}
	if !objects.Reachable() {
		t.Fatal("a bucket that answered is reported unreachable")
	}

	if _, err := objects.Get(ctx, "truncated-key"); err == nil {
		t.Fatal("a truncated body did not error")
	}
	if objects.Reachable() {
		t.Fatal("a download that failed mid-stream still reports the store reachable")
	}
}

func TestCheckBucketHonorsDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(time.Second)
	}))
	defer server.Close()
	objects := testStore(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := objects.CheckBucket(ctx)
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("CheckBucket deadline = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("CheckBucket exceeded deadline by taking %s", elapsed)
	}
}

func TestCheckBucketTreatsMissingBucketAsUnavailable(t *testing.T) {
	var creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			creates.Add(1)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	if err := testStore(t, server.URL).CheckBucket(context.Background()); err == nil {
		t.Fatal("CheckBucket with missing bucket succeeded")
	}
	if creates.Load() != 0 {
		t.Fatalf("missing bucket was created %d times", creates.Load())
	}
}

func testStore(t *testing.T, endpoint string) *Store {
	t.Helper()
	objects, err := New(Config{
		Endpoint: endpoint, Region: "us-east-1", Bucket: "sboms",
		AccessKey: "access", SecretKey: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	return objects
}

func TestKeyIsTenantQualifiedAndContentAddressed(t *testing.T) {
	first := Key(
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		"build", "sbom", []byte("first"),
	)
	second := Key(
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		"build", "sbom", []byte("second"),
	)
	if first == second || !strings.HasPrefix(first,
		"t/11111111111111111111111111111111/22222222222222222222222222222222/s/") {
		t.Fatalf("keys = %q and %q", first, second)
	}
}

// Transcript objects carry the class tag, so one documented bucket lifecycle
// rule can collect strays from the crash windows that leave an object with no
// row (duf-umu7). Plain puts stay untagged: SBOM objects live forever and a
// lifecycle filter must never match them.
func TestPutTranscriptCarriesTheClassTagAndPutDoesNot(t *testing.T) {
	var tagged, untagged atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			if r.URL.Path == "/sboms/tagged" {
				tagged.Store(r.Header.Get("X-Amz-Tagging"))
			}
			if r.URL.Path == "/sboms/untagged" {
				untagged.Store(r.Header.Get("X-Amz-Tagging"))
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	objects := testStore(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := objects.PutTranscript(ctx, "tagged", []byte("payload")); err != nil {
		t.Fatalf("PutTranscript: %v", err)
	}
	if err := objects.Put(ctx, "untagged", []byte("payload")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := tagged.Load(); got != TranscriptClassTag {
		t.Fatalf("transcript put tagging = %v, want %q", got, TranscriptClassTag)
	}
	if got := untagged.Load(); got != "" {
		t.Fatalf("plain put tagging = %v, want none", got)
	}
}
