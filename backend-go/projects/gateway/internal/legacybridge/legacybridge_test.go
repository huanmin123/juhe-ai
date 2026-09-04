package legacybridge

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

func TestBridgeProxiesAndRemoves(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__aisys__/api/legacy-route":
			if got := r.Header.Get("X-Juhe-Ai-Served-By"); got != "legacy-bridge" {
				t.Fatalf("bridge marker header missing: %q", got)
			}
			_, _ = io.WriteString(w, `{"data":"legacy"}`)
		default:
			t.Fatalf("unexpected origin request: %s", r.URL.Path)
		}
	}))
	defer origin.Close()

	k := kernel.New(kernel.Options{})
	bridge, err := New(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	bridge.RegisterPrefix("/__aisys__/api")

	k.Register("GET /__aisys__/api/go-route", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kernel.WriteOK(w, map[string]string{"served": "go"}, "")
	}))
	k.RegisterFallback(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bridge.ServeHTTP(w, r)
	}))
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	goResp, err := http.Get(server.URL + "/__aisys__/api/go-route")
	if err != nil {
		t.Fatal(err)
	}
	goBody, _ := io.ReadAll(goResp.Body)
	goResp.Body.Close()
	if goResp.StatusCode != 200 || !strings.Contains(string(goBody), `"served":"go"`) {
		t.Fatalf("go route: %d %s", goResp.StatusCode, goBody)
	}

	legacyResp, err := http.Get(server.URL + "/__aisys__/api/legacy-route")
	if err != nil {
		t.Fatal(err)
	}
	legacyBody, _ := io.ReadAll(legacyResp.Body)
	legacyResp.Body.Close()
	if legacyResp.StatusCode != 200 || !strings.Contains(string(legacyBody), "legacy") {
		t.Fatalf("legacy route: %d %s", legacyResp.StatusCode, legacyBody)
	}

	// Flip: remove the prefix, subsequent requests 404 via catch-all.
	if !bridge.RemovePrefix("/__aisys__/api") {
		t.Fatal("RemovePrefix must report removal")
	}
	flipped, err := http.Get(server.URL + "/__aisys__/api/legacy-route")
	if err != nil {
		t.Fatal(err)
	}
	flippedBody, _ := io.ReadAll(flipped.Body)
	flippedResp := flipped
	flippedResp.Body.Close()
	if flippedResp.StatusCode != 404 {
		t.Fatalf("after flip must 404, got %d body=%s", flippedResp.StatusCode, flippedBody)
	}
	_ = strings.TrimSpace
}
