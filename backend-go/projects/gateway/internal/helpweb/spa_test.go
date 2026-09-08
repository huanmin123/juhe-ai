package helpweb

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMountSPA(t *testing.T) {
	dist := t.TempDir()
	// Create a minimal SPA layout.
	writeFile := func(rel, content string) {
		p := filepath.Join(dist, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
	writeFile("index.html", "<html><body>SPA</body></html>")
	writeFile("assets/app.js", "console.log(1)")
	writeFile("assets/app.css", "body{}")
	writeFile("brand-icon.svg", "<svg/>")
	writeFile("build-info.json", "{}")

	reg := &testRegistrar{}
	MountSPA(reg, dist)

	if len(reg.handlers) == 0 {
		t.Fatal("SPA not mounted when index.html exists")
	}
	handler := reg.handlers[systemPrefix+"/"]
	if handler == nil {
		t.Fatal("subtree handler missing")
	}

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
		wantCache  string
	}{
		{
			name:       "root serves index",
			path:       systemPrefix + "/",
			wantStatus: 200,
			wantBody:   "SPA",
			wantCache:  "no-cache",
		},
		{
			name:       "asset gets immutable cache",
			path:       systemPrefix + "/assets/app.js",
			wantStatus: 200,
			wantBody:   "console.log",
			wantCache:  "public, max-age=31536000, immutable",
		},
		{
			name:       "brand icon gets no-cache",
			path:       systemPrefix + "/brand-icon.svg",
			wantStatus: 200,
			wantBody:   "<svg/>",
			wantCache:  "no-cache",
		},
		{
			name:       "SPA fallback serves index for unknown path",
			path:       systemPrefix + "/groups/page/1",
			wantStatus: 200,
			wantBody:   "SPA",
			wantCache:  "no-cache",
		},
		{
			name:       "API path falls through with 404",
			path:       systemAPIPrefix + "/groups",
			wantStatus: 404,
		},
		{
			name:       "health path falls through with 404",
			path:       systemPrefix + "/health",
			wantStatus: 404,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantBody != "" && !contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body missing %q: %s", tc.wantBody, rec.Body.String())
			}
			if tc.wantCache != "" && rec.Header().Get("Cache-Control") != tc.wantCache {
				t.Errorf("Cache-Control = %q, want %q", rec.Header().Get("Cache-Control"), tc.wantCache)
			}
		})
	}
}

func TestMountSPADisabledWithoutDist(t *testing.T) {
	reg := &testRegistrar{}
	MountSPA(reg, t.TempDir()) // empty dir, no index.html
	if len(reg.handlers) != 0 {
		t.Fatal("SPA should not mount without index.html")
	}
	MountSPA(reg, "")
	if len(reg.handlers) != 0 {
		t.Fatal("SPA should not mount with empty dist path")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

type testRegistrar struct {
	handlers map[string]http.Handler
}

func (r *testRegistrar) Register(pattern string, handler http.Handler) {
	if r.handlers == nil {
		r.handlers = make(map[string]http.Handler)
	}
	r.handlers[pattern] = handler
}
