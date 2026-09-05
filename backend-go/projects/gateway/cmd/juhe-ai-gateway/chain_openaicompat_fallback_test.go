package main

// chainCompatDispatcher fallback tests (chain_openaicompat.go K4): a
// family-prefix path without a registered route keeps the Node 404 JSON
// contract instead of the net/http text/plain `404 page not found` body
// (express routers fall through to the 404 JSON for unknown sub-paths and
// unmatched methods alike).

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newChainCompatDispatcherForTest() *chainCompatDispatcher {
	compat := &chainCompatMux{mux: http.NewServeMux()}
	compat.Register("GET /v1/files", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	compat.Register("GET /v1/vector_stores/{vectorStoreId}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	return &chainCompatDispatcher{mux: compat}
}

func TestChainCompatDispatcherUncoveredCombosRenderJSON404(t *testing.T) {
	server := httptest.NewServer(newChainCompatDispatcherForTest())
	defer server.Close()
	client := &http.Client{}

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"unknown deep sub-path", http.MethodGet, "/v1/files/a/b"},
		{"unknown single segment", http.MethodGet, "/v1/vector_stores/a/b/c"},
		{"unmatched method on exact route", http.MethodPost, "/v1/files"},
		{"unmatched method on parameter route", http.MethodPost, "/v1/vector_stores/vs_1"},
		{"unsupported method", http.MethodPatch, "/v1/files"},
		{"non-family path", http.MethodGet, "/v1/definitely-not-a-family"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request, err := http.NewRequest(testCase.method, server.URL+testCase.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatalf("do request: %v", err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("%s %s status=%d body=%s", testCase.method, testCase.path, response.StatusCode, string(body))
			}
			if body := strings.TrimSpace(string(body)); body != `{"message":"资源不存在"}` {
				t.Fatalf("%s %s body=%q want the Node 404 JSON", testCase.method, testCase.path, body)
			}
			if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
				t.Fatalf("%s %s content-type=%q", testCase.method, testCase.path, contentType)
			}
		})
	}
}

func TestChainCompatDispatcherMatchedRouteStillAnswers(t *testing.T) {
	server := httptest.NewServer(newChainCompatDispatcherForTest())
	defer server.Close()
	response, err := http.Get(server.URL + "/v1/files")
	if err != nil {
		t.Fatalf("GET /v1/files: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", response.StatusCode)
	}
}
