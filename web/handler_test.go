package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandlerServesClientRoute(t *testing.T) {
	t.Parallel()

	h := testHandler(t)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/buckets", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if body := response.Body.String(); !strings.Contains(body, "console entry point") {
		t.Fatalf("body = %q, want index.html", body)
	}
}

func TestHandlerServesNotBuiltPageWithoutIndex(t *testing.T) {
	t.Parallel()

	h := newHandler(fstest.MapFS{})
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if body := response.Body.String(); !strings.Contains(body, "The web console was not built") {
		t.Fatalf("body = %q, want not-built page", body)
	}
}

func TestHandlerReturnsNotFoundForMissingAsset(t *testing.T) {
	t.Parallel()

	h := testHandler(t)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/nope.js", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	if body := response.Body.String(); strings.Contains(body, "console entry point") {
		t.Fatalf("missing asset served index.html: %q", body)
	}
}

func TestHandlerDoesNotCacheIndex(t *testing.T) {
	t.Parallel()

	h := testHandler(t)
	for _, requestPath := range []string{"/", "/principals"} {
		t.Run(requestPath, func(t *testing.T) {
			response := httptest.NewRecorder()
			h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, requestPath, nil))

			if got := response.Header().Get("Cache-Control"); got != indexCacheControl {
				t.Fatalf("Cache-Control = %q, want %q", got, indexCacheControl)
			}
		})
	}
}

func TestHandlerCachesHashedAssets(t *testing.T) {
	t.Parallel()

	h := testHandler(t)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/app-Ab12_cd3.js", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Cache-Control"); got != immutableCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, immutableCacheControl)
	}
}

func testHandler(t *testing.T) http.Handler {
	t.Helper()

	dist := fstest.MapFS{
		"index.html": {
			Data: []byte("<!doctype html><title>console entry point</title>"),
		},
		"assets/app-Ab12_cd3.js": {
			Data: []byte("console.log('dufflebag')"),
		},
	}
	return newHandler(dist)
}
