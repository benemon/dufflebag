package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGETCompressionNegotiation(t *testing.T) {
	payload := bytes.Repeat([]byte(`{"packages":[]}`), 64)
	server := gzipGET(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))

	identity := httptest.NewRecorder()
	server.ServeHTTP(identity, httptest.NewRequest(http.MethodGet, "/api/v1/instance", nil))
	if identity.Header().Get("Content-Encoding") != "" || !bytes.Equal(identity.Body.Bytes(), payload) {
		t.Fatalf("identity response = %q %q", identity.Header().Get("Content-Encoding"), identity.Body.Bytes()[:20])
	}
	if !strings.Contains(identity.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("identity response Vary = %q", identity.Header().Get("Vary"))
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	compressed := httptest.NewRecorder()
	server.ServeHTTP(compressed, req)
	if compressed.Header().Get("Content-Encoding") != "gzip" ||
		!strings.Contains(compressed.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("gzip headers = %#v", compressed.Header())
	}
	reader, err := gzip.NewReader(compressed.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("gzip body differs from identity: %q", body[:20])
	}

	post := httptest.NewRecorder()
	postReq := httptest.NewRequest(http.MethodPost, "/api/v1/instance", nil)
	postReq.Header.Set("Accept-Encoding", "gzip")
	server.ServeHTTP(post, postReq)
	if post.Header().Get("Content-Encoding") != "" {
		t.Fatalf("POST response compressed: %#v", post.Header())
	}
}
