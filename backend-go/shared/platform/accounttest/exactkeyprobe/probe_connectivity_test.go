package exactkeyprobe

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestProbeMediaConnectivityUsesModelsGET(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	result := probeMediaConnectivity(context.Background(), server.Client(), baseURL, Input{Provider: "gpt", EndpointMode: "images_json"}, "sk-test", ProbeOptions{Timeout: 2 * time.Second})
	if result.Outcome != OutcomeSuccess || result.StatusCode != http.StatusOK {
		t.Fatalf("result = %+v, want success 200", result)
	}
	if method != http.MethodGet || path != "/models" {
		t.Fatalf("request = %s %s, want GET /models", method, path)
	}
}

func TestProbeMediaConnectivityClassifiesAuthAndMissingEndpoint(t *testing.T) {
	cases := []struct {
		status      int
		wantCode    string
		wantOutcome string
	}{
		{http.StatusUnauthorized, "upstream_http_status", OutcomeHTTPFailure},
		{http.StatusForbidden, "upstream_http_status", OutcomeHTTPFailure},
		{http.StatusNotFound, "upstream_endpoint_unsupported", OutcomeHTTPFailure},
		{http.StatusInternalServerError, "upstream_http_status", OutcomeHTTPFailure},
	}
	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		baseURL, _ := url.Parse(server.URL)
		result := probeMediaConnectivity(context.Background(), server.Client(), baseURL, Input{Provider: "gpt", EndpointMode: "images_json"}, "sk-test", ProbeOptions{Timeout: 2 * time.Second})
		server.Close()
		if result.Outcome != tc.wantOutcome || result.ErrorCode != tc.wantCode {
			t.Fatalf("status %d result = %+v, want outcome=%s code=%s", tc.status, result, tc.wantOutcome, tc.wantCode)
		}
	}
}

func TestProbeMediaConnectivityImageUsesProviderTaskQuery(t *testing.T) {
	cases := []struct {
		provider string
		wantPath string
	}{
		{"volcengine", "/contents/generations/tasks/" + mediaConnectivityProbeTaskID},
		{"qwen", "/api/v1/tasks/" + mediaConnectivityProbeTaskID},
	}
	for _, tc := range cases {
		var method, path string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			method, path = r.Method, r.URL.Path
			w.WriteHeader(http.StatusNotFound)
		}))
		baseURL, _ := url.Parse(server.URL)
		result := probeMediaConnectivity(context.Background(), server.Client(), baseURL, Input{Provider: tc.provider, EndpointMode: "images_json"}, "sk-test", ProbeOptions{Timeout: 2 * time.Second})
		server.Close()
		if result.Outcome != OutcomeSuccess || result.StatusCode != http.StatusNotFound {
			t.Fatalf("%s result = %+v, want success 404", tc.provider, result)
		}
		if method != http.MethodGet || path != tc.wantPath {
			t.Fatalf("%s request = %s %s, want GET %s", tc.provider, method, path, tc.wantPath)
		}
	}
}

func TestProbeMediaConnectivityVolcengine403MissingResourceIDIsReachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":45000030,"message":"resource_id is required"}`))
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	result := probeMediaConnectivity(context.Background(), server.Client(), baseURL, Input{Provider: "volcengine", EndpointMode: "video_create", HealthModel: "video-model"}, "sk-test", ProbeOptions{Timeout: 2 * time.Second})
	if result.Outcome != OutcomeSuccess || result.StatusCode != http.StatusForbidden {
		t.Fatalf("result = %+v, want success 403 for missing resource_id", result)
	}
}

func TestProbeMediaConnectivityAudioJobQueriesPlaceholderTask(t *testing.T) {
	cases := []struct {
		provider string
		wantPath string
	}{
		{"gpt", "/audio/jobs/" + mediaConnectivityProbeTaskID},
		{"qwen", "/api/v1/tasks/" + mediaConnectivityProbeTaskID},
	}
	for _, tc := range cases {
		var method, path, body string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			method, path = r.Method, r.URL.Path
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
			w.WriteHeader(http.StatusNotFound)
		}))
		baseURL, _ := url.Parse(server.URL)
		result := probeMediaConnectivity(context.Background(), server.Client(), baseURL, Input{Provider: tc.provider, EndpointMode: "audio_job_get"}, "sk-test", ProbeOptions{Timeout: 2 * time.Second})
		server.Close()
		if result.Outcome != OutcomeSuccess || result.StatusCode != http.StatusNotFound {
			t.Fatalf("%s result = %+v, want success 404", tc.provider, result)
		}
		if method != http.MethodGet || path != tc.wantPath || body != "" {
			t.Fatalf("%s request = %s %s body=%q, want GET %s with empty body", tc.provider, method, path, body, tc.wantPath)
		}
	}
}

func TestProbeMediaConnectivityVideoQueriesPlaceholderTask(t *testing.T) {
	cases := []struct {
		provider  string
		model     string
		wantPath  string
		wantQuery string
	}{
		{"gpt", "", "/videos/" + mediaConnectivityProbeTaskID, ""},
		{"qwen", "", "/api/v1/tasks/" + mediaConnectivityProbeTaskID, ""},
		{"volcengine", "", "/contents/generations/tasks/" + mediaConnectivityProbeTaskID, ""},
		{"minimax", "", "/query/video_generation", "task_id=" + mediaConnectivityProbeTaskID},
		{"gemini", "gemini-video", "/models/gemini-video/operations/" + mediaConnectivityProbeTaskID, ""},
	}
	for _, tc := range cases {
		var method, path, rawQuery string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			method, path, rawQuery = r.Method, r.URL.Path, r.URL.RawQuery
			w.WriteHeader(http.StatusNotFound)
		}))
		baseURL, _ := url.Parse(server.URL)
		result := probeMediaConnectivity(context.Background(), server.Client(), baseURL, Input{Provider: tc.provider, EndpointMode: "video_create", HealthModel: tc.model}, "sk-test", ProbeOptions{Timeout: 2 * time.Second})
		server.Close()
		if result.Outcome != OutcomeSuccess || result.StatusCode != http.StatusNotFound {
			t.Fatalf("%s result = %+v, want success 404", tc.provider, result)
		}
		if method != http.MethodGet || path != tc.wantPath || rawQuery != tc.wantQuery {
			t.Fatalf("%s request = %s %s?%s, want GET %s?%s", tc.provider, method, path, rawQuery, tc.wantPath, tc.wantQuery)
		}
	}
}

func TestProbeMediaConnectivityAudioPostsEmptyBody(t *testing.T) {
	var method, path, authorization, body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		authorization = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	result := probeMediaConnectivity(context.Background(), server.Client(), baseURL, Input{Provider: "gpt", EndpointMode: "audio_speech", HealthModel: "tts-1"}, "sk-test", ProbeOptions{Timeout: 2 * time.Second})
	if result.Outcome != OutcomeSuccess || result.StatusCode != http.StatusBadRequest {
		t.Fatalf("result = %+v, want success 400", result)
	}
	if method != http.MethodPost || path != "/audio/speech" {
		t.Fatalf("request = %s %s, want POST /audio/speech", method, path)
	}
	if authorization != "Bearer sk-test" {
		t.Fatalf("authorization = %q, want Bearer sk-test", authorization)
	}
	if strings.TrimSpace(body) != `{"model":"tts-1"}` || strings.Contains(body, `"text"`) {
		t.Fatalf("body = %s, want model only without text", body)
	}
}

func TestProbeMediaConnectivityGeminiAudioUsesAPIKeyHeader(t *testing.T) {
	var method, path, apiKeyHeader, authorization, body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		apiKeyHeader = r.Header.Get("x-goog-api-key")
		authorization = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	result := probeMediaConnectivity(context.Background(), server.Client(), baseURL, Input{Provider: "gemini", Type: "api_key", EndpointMode: "audio_speech", HealthModel: "gemini-tts"}, "gemini-key", ProbeOptions{Timeout: 2 * time.Second})
	if result.Outcome != OutcomeSuccess || result.StatusCode != http.StatusBadRequest {
		t.Fatalf("result = %+v, want success 400", result)
	}
	if method != http.MethodPost || path != "/models/gemini-tts:generateContent" {
		t.Fatalf("request = %s %s, want POST /models/gemini-tts:generateContent", method, path)
	}
	if apiKeyHeader != "gemini-key" || authorization != "" {
		t.Fatalf("auth headers api-key=%q authorization=%q, want x-goog-api-key only", apiKeyHeader, authorization)
	}
	if !strings.Contains(body, `"responseModalities":["AUDIO"]`) || !strings.Contains(body, `"text":""`) {
		t.Fatalf("body = %s, want empty text + AUDIO modality", body)
	}
}

func TestMediaSpeechRequestVolcengineOpenspeechUsesSpeechToken(t *testing.T) {
	baseURL, _ := url.Parse("https://openspeech.bytedance.com")
	path, body, headers, err := mediaSpeechRequest(Input{Provider: "volcengine", HealthModel: "seed-tts", SpeechToken: "speech-token"}, baseURL)
	if err != nil {
		t.Fatalf("mediaSpeechRequest error = %v", err)
	}
	if path != "/api/v3/tts/create" {
		t.Fatalf("path = %s, want /api/v3/tts/create", path)
	}
	if headers["X-Api-Key"] != "speech-token" {
		t.Fatalf("headers = %+v, want X-Api-Key speech-token", headers)
	}
	if strings.TrimSpace(string(body)) != `{"model":"seed-tts"}` {
		t.Fatalf("body = %s, want model only", body)
	}
}

func TestMediaSpeechRequestVolcengineOpenspeechRequiresSpeechToken(t *testing.T) {
	baseURL, _ := url.Parse("https://openspeech.bytedance.com")
	if _, _, _, err := mediaSpeechRequest(Input{Provider: "volcengine", HealthModel: "seed-tts"}, baseURL); err == nil {
		t.Fatal("mediaSpeechRequest error = nil, want missing speech_token error")
	}
}
