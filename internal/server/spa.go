package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// spaHandler serves static assets and falls back to index.html for client-side routing.
type spaHandler struct {
	staticDir string
	indexFile string
}

func newSPAHandler(staticDir string) http.Handler {
	return &spaHandler{staticDir: staticDir, indexFile: "index.html"}
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Prevent directory traversal.
	cleanPath := filepath.Clean(r.URL.Path)
	if cleanPath == "/" {
		cleanPath = "/index.html"
	}
	servingIndex := cleanPath == "/index.html"
	// Strip leading slash for filesystem lookup.
	target := filepath.Join(h.staticDir, strings.TrimPrefix(cleanPath, "/"))

	if fileExists(target) {
		if servingIndex {
			w.Header().Set("Cache-Control", "no-store")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeFile(w, r, target)
		return
	}

	indexPath := filepath.Join(h.staticDir, h.indexFile)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, indexPath)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
