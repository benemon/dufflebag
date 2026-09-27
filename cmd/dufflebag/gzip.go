package main

import (
	"compress/gzip"
	"net/http"
	"strings"
)

type gzipResponseWriter struct {
	http.ResponseWriter
	writer *gzip.Writer
}

func (w gzipResponseWriter) WriteHeader(status int) {
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(status)
}

func (w gzipResponseWriter) Write(body []byte) (int, error) {
	w.Header().Del("Content-Length")
	return w.writer.Write(body)
}

// Flush commits what the handler has written so far, which is how a handler
// commits its headers before the response body is complete.
func (w gzipResponseWriter) Flush() {
	_ = w.writer.Flush()
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func gzipGET(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		// The trailer is written only for a handler that returned: a panic
		// must reach the audit boundary untouched, not be capped as a body.
		finished := false
		defer func() {
			if finished {
				_ = writer.Close()
			}
		}()
		next.ServeHTTP(gzipResponseWriter{ResponseWriter: w, writer: writer}, r)
		finished = true
	})
}

func acceptsGzip(header string) bool {
	for _, encoding := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(encoding, ";", 2)[0]), "gzip") {
			return true
		}
	}
	return false
}
