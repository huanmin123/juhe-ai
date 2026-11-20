package mockupstream

// 2026-11-20 火山 openspeech 同步 STT（recognize/flash）mock 面测试：
// 成功载荷（duration 毫秒 + text/utterances）、头驱动拒绝形态（缺 key /
// 资源未授权 / 缺请求 ID / 缺 sequence）、请求体校验（base64 与 format）、
// URL 输入形态，以及白名单/媒体路径识别。

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
)

const (
	volcengineASRSyncTestAPIKey    = "mock-volc-sync-key"
	volcengineASRSyncTestRequestID = "volcsync-test-0001"
)

// doVolcengineASRSync posts one flash-recognition request with the explicit
// header quartet. Empty header values omit that header so the rejection
// shapes stay reachable.
func doVolcengineASRSync(t *testing.T, m *Server, apiKey, resourceID, requestID, sequence, scenario string, body []byte) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, m.Server.URL+volcengineASRSyncPath, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if apiKey != "" {
		req.Header.Set("X-Api-Key", apiKey)
	}
	if resourceID != "" {
		req.Header.Set("X-Api-Resource-Id", resourceID)
	}
	if requestID != "" {
		req.Header.Set("X-Api-Request-Id", requestID)
	}
	if sequence != "" {
		req.Header.Set("X-Api-Sequence", sequence)
	}
	if scenario != "" {
		req.Header.Set("X-Mock-Scenario", scenario)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw
}

// volcengineASRSyncBodyWithData builds the gateway-shaped request: base64
// audio.data plus audio.format and request.model_name.
func volcengineASRSyncBodyWithData(t *testing.T, data string, format string) []byte {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString([]byte(data))
	return []byte(fmt.Sprintf(`{"user":{"uid":"juhe-ai-gateway"},"audio":{"data":%q,"format":%q},"request":{"model_name":"bigmodel"}}`, encoded, format))
}

// TestMediaVolcengineASRSyncOK pins the one-shot success shape and the
// recorded outbound request (path + header quartet + base64 body).
func TestMediaVolcengineASRSyncOK(t *testing.T) {
	m := New()
	defer m.Close()

	status, headers, raw := doVolcengineASRSync(t, m,
		volcengineASRSyncTestAPIKey, volcengineASRResourceID, volcengineASRSyncTestRequestID, "-1",
		string(ScenarioMediaVolcengineASRSyncOK), volcengineASRSyncBodyWithData(t, "mock-mp3-bytes", "mp3"))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, raw)
	}
	if got := headers.Get("X-Api-Status-Code"); got != volcengineASRStatusOK {
		t.Fatalf("X-Api-Status-Code = %q, want %q", got, volcengineASRStatusOK)
	}
	if got := headers.Get("X-Api-Message"); got != "OK" {
		t.Fatalf("X-Api-Message = %q, want OK", got)
	}
	if got := headers.Get("X-Api-Request-Id"); got != volcengineASRSyncTestRequestID {
		t.Fatalf("X-Api-Request-Id echo = %q, want %q", got, volcengineASRSyncTestRequestID)
	}

	var payload struct {
		AudioInfo struct {
			Duration int `json:"duration"`
		} `json:"audio_info"`
		Result struct {
			Text       string `json:"text"`
			Utterances []struct {
				StartTime int    `json:"start_time"`
				EndTime   int    `json:"end_time"`
				Text      string `json:"text"`
			} `json:"utterances"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload: %v (%s)", err, raw)
	}
	if payload.AudioInfo.Duration != 3996 {
		t.Fatalf("audio_info.duration = %d, want 3996 (毫秒)", payload.AudioInfo.Duration)
	}
	if payload.Result.Text != "Hello from the mock sync transcription." {
		t.Fatalf("result.text = %q", payload.Result.Text)
	}
	if len(payload.Result.Utterances) != 1 ||
		payload.Result.Utterances[0].StartTime != 450 ||
		payload.Result.Utterances[0].EndTime != 3570 {
		t.Fatalf("utterances = %+v, want one 450→3570 segment", payload.Result.Utterances)
	}

	requests := m.Requests()
	if len(requests) != 1 {
		t.Fatalf("recorded requests = %d, want 1", len(requests))
	}
	recorded := requests[0]
	if recorded.Method != http.MethodPost || recorded.Path != volcengineASRSyncPath {
		t.Fatalf("recorded path = %s %s, want POST %s", recorded.Method, recorded.Path, volcengineASRSyncPath)
	}
	if recorded.APIKeyHeader != volcengineASRSyncTestAPIKey ||
		recorded.ResourceIDHeader != volcengineASRResourceID ||
		recorded.RequestIDHeader != volcengineASRSyncTestRequestID {
		t.Fatalf("recorded headers = key %q resource %q request %q",
			recorded.APIKeyHeader, recorded.ResourceIDHeader, recorded.RequestIDHeader)
	}
	for _, want := range []string{`"data":`, `"format":"mp3"`, `"model_name":"bigmodel"`} {
		if !bytes.Contains([]byte(recorded.Body), []byte(want)) {
			t.Fatalf("recorded body missing %s: %s", want, recorded.Body)
		}
	}
}

// TestMediaVolcengineASRSyncURLInput covers the real upstream's second
// accepted input form: audio.url instead of audio.data.
func TestMediaVolcengineASRSyncURLInput(t *testing.T) {
	m := New()
	defer m.Close()

	body := []byte(`{"audio":{"url":"https://mock.invalid/audio/sync.mp3","format":"mp3"},"request":{"model_name":"bigmodel"}}`)
	status, headers, raw := doVolcengineASRSync(t, m,
		volcengineASRSyncTestAPIKey, volcengineASRResourceID, volcengineASRSyncTestRequestID, "-1", "", body)
	if status != http.StatusOK || headers.Get("X-Api-Status-Code") != volcengineASRStatusOK {
		t.Fatalf("url input status=%d header=%q body=%s", status, headers.Get("X-Api-Status-Code"), raw)
	}
	if !bytes.Contains(raw, []byte(`"duration":3996`)) {
		t.Fatalf("url input payload = %s, want duration 3996", raw)
	}
}

// TestMediaVolcengineASRSyncRejections pins every header/body rejection
// shape the gateway adapter distinguishes.
func TestMediaVolcengineASRSyncRejections(t *testing.T) {
	validData := volcengineASRSyncBodyWithData(t, "mock-mp3-bytes", "mp3")
	cases := []struct {
		name       string
		apiKey     string
		resourceID string
		requestID  string
		sequence   string
		body       []byte
		wantHTTP   int
		wantStatus string
		wantBody   string
	}{
		{
			name:       "missing api key",
			resourceID: volcengineASRResourceID,
			requestID:  volcengineASRSyncTestRequestID,
			sequence:   "-1",
			body:       validData,
			wantHTTP:   http.StatusUnauthorized,
			wantStatus: volcengineASRStatusNoAuth,
			wantBody:   `"code":45000010`,
		},
		{
			name:       "resource not granted",
			apiKey:     volcengineASRSyncTestAPIKey,
			resourceID: "volc.seedasr.other",
			requestID:  volcengineASRSyncTestRequestID,
			sequence:   "-1",
			body:       validData,
			wantHTTP:   http.StatusOK,
			wantStatus: volcengineASRStatusNotGranted,
			wantBody:   `"code":45000030`,
		},
		{
			name:       "missing request id",
			apiKey:     volcengineASRSyncTestAPIKey,
			resourceID: volcengineASRResourceID,
			sequence:   "-1",
			body:       validData,
			wantHTTP:   http.StatusOK,
			wantStatus: volcengineASRStatusBadParam,
			wantBody:   `X-Api-Request-Id is required`,
		},
		{
			name:       "missing sequence",
			apiKey:     volcengineASRSyncTestAPIKey,
			resourceID: volcengineASRResourceID,
			requestID:  volcengineASRSyncTestRequestID,
			body:       validData,
			wantHTTP:   http.StatusOK,
			wantStatus: volcengineASRStatusBadParam,
			wantBody:   `X-Api-Sequence must be -1`,
		},
		{
			name:       "missing audio",
			apiKey:     volcengineASRSyncTestAPIKey,
			resourceID: volcengineASRResourceID,
			requestID:  volcengineASRSyncTestRequestID,
			sequence:   "-1",
			body:       []byte(`{"audio":{},"request":{"model_name":"bigmodel"}}`),
			wantHTTP:   http.StatusOK,
			wantStatus: volcengineASRStatusBadParam,
			wantBody:   `audio.data or audio.url is required`,
		},
		{
			name:       "invalid base64",
			apiKey:     volcengineASRSyncTestAPIKey,
			resourceID: volcengineASRResourceID,
			requestID:  volcengineASRSyncTestRequestID,
			sequence:   "-1",
			body:       []byte(`{"audio":{"data":"not-base64!","format":"mp3"}}`),
			wantHTTP:   http.StatusOK,
			wantStatus: volcengineASRStatusBadParam,
			wantBody:   `audio.data must be base64`,
		},
		{
			name:       "missing format",
			apiKey:     volcengineASRSyncTestAPIKey,
			resourceID: volcengineASRResourceID,
			requestID:  volcengineASRSyncTestRequestID,
			sequence:   "-1",
			body:       []byte(`{"audio":{"data":"bW9jaw=="}}`),
			wantHTTP:   http.StatusOK,
			wantStatus: volcengineASRStatusBadParam,
			wantBody:   `audio.format is required with audio.data`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New()
			defer m.Close()
			status, headers, raw := doVolcengineASRSync(t, m,
				tc.apiKey, tc.resourceID, tc.requestID, tc.sequence, "", tc.body)
			if status != tc.wantHTTP {
				t.Fatalf("status = %d, want %d: %s", status, tc.wantHTTP, raw)
			}
			if got := headers.Get("X-Api-Status-Code"); got != tc.wantStatus {
				t.Fatalf("X-Api-Status-Code = %q, want %q: %s", got, tc.wantStatus, raw)
			}
			if !bytes.Contains(raw, []byte(tc.wantBody)) {
				t.Fatalf("body = %s, want %q", raw, tc.wantBody)
			}
		})
	}
}

// TestMediaVolcengineASRSyncWhitelisted pins the path into the media
// whitelist so the engine routes it to serveMedia instead of 404.
func TestMediaVolcengineASRSyncWhitelisted(t *testing.T) {
	if !acceptsMediaEndpoint(http.MethodPost, volcengineASRSyncPath) {
		t.Fatal("acceptsMediaEndpoint rejected the sync ASR path")
	}
	if !isMediaPath(volcengineASRSyncPath) {
		t.Fatal("isMediaPath rejected the sync ASR path")
	}
}
