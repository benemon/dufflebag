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

func TestPluginDownloadsAreNotRecompressed(t *testing.T) {
	zip := bytes.Repeat([]byte("PK"), 512)
	server := gzipGET(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		_, _ = w.Write(zip)
	}))
	request := httptest.NewRequest(http.MethodGet, "/plugins/acme/packer-plugin-amazon/1.8.2/packer-plugin-amazon_1.8.2_linux_amd64.zip", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Header().Get("Content-Encoding") != "" || response.Header().Get("Content-Length") != "1024" ||
		!bytes.Equal(response.Body.Bytes(), zip) {
		t.Fatalf("plugin download headers = %#v; want identity encoding with its Content-Length", response.Header())
	}
}
