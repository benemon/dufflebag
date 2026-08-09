package web

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

const (
	immutableCacheControl = "public, max-age=31536000, immutable"
	indexCacheControl     = "no-store"
)

//go:embed dist
var assets embed.FS

//go:embed dist/not-built.html
var notBuiltIndex []byte

// NewHandler serves the built console and falls back to its entry point for
// extensionless client-side routes.
func NewHandler() http.Handler {
	dist, err := fs.Sub(assets, "dist/build")
	if err != nil {
		dist, err = fs.Sub(assets, "dist")
		if err != nil {
			panic(err)
		}
	}
	return newHandler(dist)
}

type handler struct {
	dist  fs.FS
	files http.Handler
	index []byte
}

func newHandler(dist fs.FS) http.Handler {
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		index = notBuiltIndex
	}
	return &handler{
		dist:  dist,
		files: http.FileServerFS(dist),
		index: index,
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "." || name == "index.html" {
		h.serveIndex(w, r)
		return
	}

	info, err := fs.Stat(h.dist, name)
	if err == nil && !info.IsDir() {
		if isHashedAsset(name) {
			w.Header().Set("Cache-Control", immutableCacheControl)
		}
		h.files.ServeHTTP(w, r)
		return
	}

	if path.Ext(name) != "" {
		http.NotFound(w, r)
		return
	}
	h.serveIndex(w, r)
}

func (h *handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", indexCacheControl)
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(h.index))
}

func isHashedAsset(name string) bool {
	if !strings.HasPrefix(name, "assets/") {
		return false
	}
	stem := strings.TrimSuffix(path.Base(name), path.Ext(name))
	if len(stem) < 10 || stem[len(stem)-9] != '-' {
		return false
	}
	for _, char := range stem[len(stem)-8:] {
		switch {
		case 'a' <= char && char <= 'z',
			'A' <= char && char <= 'Z',
			'0' <= char && char <= '9',
			char == '_',
			char == '-':
		default:
			return false
		}
	}
	return true
}
