package upstreamcatalog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubDoer struct {
	handler func(r *http.Request) (*http.Response, error)
}

func (s *stubDoer) Do(r *http.Request) (*http.Response, error) {
	return s.handler(r)
}

func jsonResponse(status int, body string) (*http.Response, error) {
	return &http.Response{
		StatusCode: status,
		Body:       ioNopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}, nil
}

type nopCloser struct{ s *strings.Reader }

func ioNopCloser(r *strings.Reader) ioReadCloser { return &nopCloser{s: r} }

type ioReadCloser interface {
	Read(p []byte) (int, error)
	Close() error
}

func (n *nopCloser) Read(p []byte) (int, error) { return n.s.Read(p) }
func (n *nopCloser) Close() error               { return nil }

func TestFetchOpenAIModelsPathAndBearerHeader(t *testing.T) {
	var gotPath, gotAuth, gotAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"},{"id":"gpt-4o"}]}`))
	}))
	defer server.Close()
	doer := &stubDoer{handler: func(r *http.Request) (*http.Response, error) {
		return newServerAwareDoer(server.URL)(r)
	}}
	_ = doer
	ids, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL:      server.URL + "/v1",
		ProtocolCode: "openai",
		Credential:   "sk-test",
		Doer:         server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("path = %s, want /v1/models", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Fatalf("Accept = %q", gotAccept)
	}
	if len(ids) != 2 || ids[0] != "gpt-4o" || ids[1] != "gpt-4o-mini" {
		t.Fatalf("ids = %v (must dedupe and keep order)", ids)
	}
}

// newServerAwareDoer keeps using the test server transport through a plain
// http.Client while letting the handler above record request fields.
func newServerAwareDoer(serverURL string) func(*http.Request) (*http.Response, error) {
	transport := http.DefaultTransport
	return func(r *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(r.URL.String(), serverURL) {
			return jsonResponse(http.StatusBadRequest, `{"error":"unexpected target"}`)
		}
		return transport.RoundTrip(r)
	}
}

func TestFetchAnthropicModelsUsesXAPIKeyHeader(t *testing.T) {
	var gotPath, gotAPIKey, gotVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-4"}]}`))
	}))
	defer server.Close()
	ids, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL:      server.URL,
		ProtocolCode: "anthropic",
		Credential:   "ak-key",
		Doer:         server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("path = %s, want /v1/models", gotPath)
	}
	if gotAPIKey != "ak-key" || gotVersion == "" {
		t.Fatalf("x-api-key=%q anthropic-version=%q", gotAPIKey, gotVersion)
	}
	if len(ids) != 1 || ids[0] != "claude-sonnet-4" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestFetchGeminiModelsUsesV1BetaAndGoogKeyHeader(t *testing.T) {
	var gotPath, gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-2.0-flash"},{"name":"models/gemini-1.5-flash"}]}`))
	}))
	defer server.Close()
	// Host-only base: the /v1beta root must be appended.
	ids, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL:      server.URL,
		ProtocolCode: "gemini",
		Credential:   "g-key",
		Doer:         server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1beta/models" {
		t.Fatalf("path = %s, want /v1beta/models", gotPath)
	}
	if gotKey != "g-key" {
		t.Fatalf("x-goog-api-key = %q", gotKey)
	}
	if len(ids) != 2 || ids[0] != "gemini-2.0-flash" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestFetchGeminiBaseWithVersionRootKeepsSingleRoot(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()
	if _, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL:      server.URL + "/v1beta",
		ProtocolCode: "gemini",
		Credential:   "g-key",
		Doer:         server.Client(),
	}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1beta/models" {
		t.Fatalf("path = %s, want /v1beta/models", gotPath)
	}
}

func TestFetchErrorClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	if _, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL: server.URL, ProtocolCode: "openai", Credential: "sk", Doer: server.Client(),
	}); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("want HTTP status error, got %v", err)
	}
	if _, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL: server.URL, ProtocolCode: "openai", Credential: "",
	}); err == nil || !strings.Contains(err.Error(), "缺少 API Key") {
		t.Fatalf("want missing credential error, got %v", err)
	}
	if _, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL: " ", ProtocolCode: "openai", Credential: "sk",
	}); err == nil || !strings.Contains(err.Error(), "缺少 base_url") {
		t.Fatalf("want missing base_url error, got %v", err)
	}
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer broken.Close()
	if _, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL: broken.URL, ProtocolCode: "openai", Credential: "sk", Doer: broken.Client(),
	}); err == nil || !strings.Contains(err.Error(), "不是有效 JSON") {
		t.Fatalf("want invalid JSON error, got %v", err)
	}
	doerErr := &stubDoer{handler: func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("boom")
	}}
	if _, err := FetchUpstreamModelIDs(context.Background(), FetchOptions{
		BaseURL: "https://example.com", ProtocolCode: "openai", Credential: "sk", Doer: doerErr,
	}); err == nil || !strings.Contains(err.Error(), "上游请求失败") {
		t.Fatalf("want transport error, got %v", err)
	}
}

func TestIntersectModelIDs(t *testing.T) {
	first := []string{"m1", "m2", "m3"}
	second := []string{"m2", "m3", "m4"}
	third := []string{"m3", "m2"}
	got := IntersectModelIDs([][]string{first, second, third})
	if len(got) != 2 || got[0] != "m2" || got[1] != "m3" {
		t.Fatalf("intersect = %v, want [m2 m3]", got)
	}
	if got := IntersectModelIDs(nil); len(got) != 0 {
		t.Fatalf("empty catalogs must intersect to empty, got %v", got)
	}
	if got := IntersectModelIDs([][]string{first}); len(got) != 3 {
		t.Fatalf("single catalog must pass through, got %v", got)
	}
}
