// Package helpweb — spa.go ports the management SPA static hosting from
// Node server.ts:272-306. When the frontend dist directory exists (checked
// via index.html), the /__aisys__/ prefix serves the SPA: express.static
// over the dist root, asset immutable caching, no-cache for index/brand/
// build-info, and a SPA catch-all that serves index.html for any
// /__aisys__/* path not matched by the API routes.
package helpweb

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// systemPrefix mirrors the Node systemPrefix (the management SPA mount).
const systemPrefix = "/__aisys__"

// systemAPIPrefix mirrors the Node systemApiPrefix (the API sub-prefix that
// the SPA catch-all must NOT intercept).
const systemAPIPrefix = "/__aisys__/api"

// MountSPA registers the management SPA static hosting at /__aisys__/ when
// the frontend dist directory carries an index.html. When the dist is absent
// the surface is disabled (the kernel fallback answers 404, exactly like a
// Node build without frontend/dist).
//
// The handler serves existing files from the dist root (express.static
// semantics: immutable cache for hashed assets, no-cache for index.html /
// brand-icon.svg / build-info.json) and falls back to index.html for any
// /__aisys__/* path not matching a real file — EXCEPT /__aisys__/api/*
// and /__aisys__/health which fall through to the API routes.
func MountSPA(k Registrar, distPath string) {
	if distPath == "" {
		return
	}
	indexPath := filepath.Join(distPath, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		return
	}
	spa := &spaHandler{distPath: distPath, indexPath: indexPath}
	// The bare /__aisys__ 302s to /__aisys__/ (Node redirect at :274-281).
	k.Register(systemPrefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, systemPrefix+"/", http.StatusFound)
	}))
	// Static + SPA catch-all for the subtree.
	k.Register(systemPrefix+"/", spa)
}

// spaHandler serves the management SPA from the dist directory.
type spaHandler struct {
	distPath  string
	indexPath string
}

// noCacheBasenames mirrors the Node setHeaders basename gate (server.ts:288-294).
var noCacheBasenames = map[string]bool{
	"index.html":      true,
	"brand-icon.svg":  true,
	"build-info.json": true,
}

// ServeHTTP implements http.Handler. API and health paths fall through to the
// kernel (next(w, r) equivalent: the mux continues to more specific routes).
func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	// Node server.ts:298 — /health and /api/* fall through to the API routes.
	if r.URL.Path == systemPrefix+"/health" ||
		r.URL.Path == systemAPIPrefix ||
		strings.HasPrefix(r.URL.Path, systemAPIPrefix+"/") {
		http.NotFound(w, r)
		return
	}
	// Strip the /__aisys__/ prefix to get the dist-relative path.
	relative := strings.TrimPrefix(r.URL.Path, systemPrefix+"/")
	if relative == "" {
		relative = "index.html"
	}
	clean := path.Clean("/" + relative)
	if strings.Contains(clean, "..") {
		http.NotFound(w, r)
		return
	}
	target := filepath.Join(h.distPath, filepath.FromSlash(clean))
	info, err := os.Stat(target)
	if err == nil && info.IsDir() {
		// Directory: resolve index.html.
		index := filepath.Join(target, "index.html")
		if indexInfo, indexErr := os.Stat(index); indexErr == nil && !indexInfo.IsDir() {
			h.serveFile(w, r, index)
			return
		}
		h.serveIndex(w, r)
		return
	}
	if err != nil {
		// SPA catch-all: unknown paths serve index.html (server.ts:297-305).
		h.serveIndex(w, r)
		return
	}
	h.serveFile(w, r, target)
}

// serveFile streams one dist file with the express.static setHeaders
// contract (server.ts:283-295).
func (h *spaHandler) serveFile(w http.ResponseWriter, r *http.Request, target string) {
	assetsPath := filepath.Join(h.distPath, "assets")
	if strings.HasPrefix(target, assetsPath+string(filepath.Separator)) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else if noCacheBasenames[filepath.Base(target)] {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFile(w, r, target)
}

// serveIndex serves the SPA entry point with no-cache.
func (h *spaHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, h.indexPath)
}
