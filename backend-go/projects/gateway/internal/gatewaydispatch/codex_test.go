package gatewaydispatch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// Codex adapter tests: oauth-normalizer + client-headers + builtin-tools +
// usage headers (adapters/gpt-codex/* contract).

func TestNormalizeOpenAIOAuthCodexParsedBodyFullRequest(t *testing.T) {
	parsed := map[string]any{
		"model":        "gpt-5",
		"input":        "hello",
		"temperature":  0.7,
		"top_p":        0.9,
		"store":        true,
		"stream":       false,
		"session_id":   "raw-session",
		"instructions": "base",
		"reasoning":    map[string]any{"effort": "high"},
	}
	inputHeaders := http.Header{}
	inputHeaders.Set("X-Prompt-Cache-Key", "raw-session")
	result, err := NormalizeOpenAIOAuthCodexParsedBody(parsed, OpenAIOAuthCodexNormalizeInput{
		InputHeaders: inputHeaders,
		Account:      OpenAIOAuthCodexAccount{ID: "acc-1", APIKey: "token"},
		Identity:     OpenAIOAuthCodexIdentity{SystemAccountID: "sys-1", GroupID: "g-1"},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !result.Stream {
		t.Fatal("non-compact codex requests must stream")
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(result.Body), &body); err != nil {
		t.Fatalf("body json: %v", err)
	}
	// input string → message array
	input, ok := body["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input = %#v", body["input"])
	}
	// dropped fields
	for _, field := range []string{"temperature", "top_p", "session_id", "conversation"} {
		if _, ok := body[field]; ok {
			t.Fatalf("field %s should be dropped", field)
		}
	}
	// store=false stream=true
	if body["store"] != false || body["stream"] != true {
		t.Fatalf("store/stream = %#v/%#v", body["store"], body["stream"])
	}
	// reasoning.encrypted_content include
	include, ok := body["include"].([]any)
	if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v", body["include"])
	}
	// prompt_cache_key isolated from the raw session id
	cacheKey, _ := body["prompt_cache_key"].(string)
	if cacheKey == "" || cacheKey == "raw-session" {
		t.Fatalf("prompt_cache_key = %q", cacheKey)
	}
	if len(cacheKey) != 32 {
		t.Fatalf("isolated cache key length = %d", len(cacheKey))
	}
}

func TestNormalizeOpenAIOAuthCodexSystemRoleToDeveloper(t *testing.T) {
	parsed := map[string]any{
		"model": "gpt-5",
		"input": []any{
			map[string]any{"type": "message", "role": "system", "content": []any{
				map[string]any{"type": "input_text", "text": "be nice"},
			}},
			map[string]any{"type": "message", "role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "hi"},
			}},
		},
	}
	result, err := NormalizeOpenAIOAuthCodexParsedBody(parsed, OpenAIOAuthCodexNormalizeInput{
		InputHeaders: http.Header{},
		Account:      OpenAIOAuthCodexAccount{APIKey: "token"},
		Identity:     OpenAIOAuthCodexIdentity{SystemAccountID: "sys-1"},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(result.Body), &body); err != nil {
		t.Fatalf("body json: %v", err)
	}
	instructions, _ := body["instructions"].(string)
	if !strings.Contains(instructions, "be nice") {
		t.Fatalf("instructions = %q", instructions)
	}
	items := body["input"].([]any)
	first := items[0].(map[string]any)
	if first["role"] != "developer" {
		t.Fatalf("role = %#v", first["role"])
	}
}

func TestNormalizeOpenAIOAuthCodexCompact(t *testing.T) {
	parsed := map[string]any{
		"model":            "gpt-5",
		"input":            []any{},
		"prompt_cache_key": "cache-1",
		"tools":            []any{},
		"tool_choice":      "auto",
	}
	result, err := NormalizeOpenAIOAuthCodexParsedBody(parsed, OpenAIOAuthCodexNormalizeInput{
		InputHeaders: http.Header{},
		Account:      OpenAIOAuthCodexAccount{APIKey: "token"},
		Identity:     OpenAIOAuthCodexIdentity{SystemAccountID: "sys-1"},
		Compact:      true,
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if result.Stream {
		t.Fatal("compact requests must not stream")
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(result.Body), &body); err != nil {
		t.Fatalf("body json: %v", err)
	}
	for _, field := range []string{"prompt_cache_key", "tools", "tool_choice", "store", "stream"} {
		if _, ok := body[field]; ok {
			t.Fatalf("compact field %s should be dropped", field)
		}
	}
}

func TestNormalizeOpenAIOAuthCodexValidation(t *testing.T) {
	cases := []struct {
		name    string
		parsed  map[string]any
		compact bool
		message string
	}{
		{"missing model", map[string]any{"input": "x"}, false, "请求体中的 model 必须是非空字符串"},
		{"missing input", map[string]any{"model": "gpt-5"}, false, "请求体必须包含 input 字段"},
		{"invalid input type", map[string]any{"model": "gpt-5", "input": 3}, false, "请求体中的 input 必须是字符串或数组"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeOpenAIOAuthCodexParsedBody(tc.parsed, OpenAIOAuthCodexNormalizeInput{
				InputHeaders: http.Header{},
				Account:      OpenAIOAuthCodexAccount{APIKey: "token"},
				Identity:     OpenAIOAuthCodexIdentity{SystemAccountID: "sys-1"},
				Compact:      tc.compact,
			})
			var adapterErr *OpenAIOAuthCodexAdapterError
			if !errorsAs(err, &adapterErr) {
				t.Fatalf("expected adapter error, got %v", err)
			}
			if adapterErr.Message != tc.message {
				t.Fatalf("message = %q, want %q", adapterErr.Message, tc.message)
			}
			if adapterErr.StatusCode != 400 || adapterErr.Type != "invalid_request_error" {
				t.Fatalf("status/type = %d/%s", adapterErr.StatusCode, adapterErr.Type)
			}
		})
	}
}

func TestIsolateOpenAIOAuthCodexSessionIDStable(t *testing.T) {
	identity := OpenAIOAuthCodexIdentity{SystemAccountID: "sys-1", APIKeyID: "key-1"}
	first := IsolateOpenAIOAuthCodexSessionID("session-a", OpenAIOAuthCodexAccount{}, identity)
	second := IsolateOpenAIOAuthCodexSessionID("session-a", OpenAIOAuthCodexAccount{ID: "other-account"}, identity)
	if first != second {
		t.Fatal("isolation must be independent of the upstream account")
	}
	if first == IsolateOpenAIOAuthCodexSessionID("session-b", OpenAIOAuthCodexAccount{}, identity) {
		t.Fatal("different raw ids must isolate differently")
	}
	if IsolateOpenAIOAuthCodexSessionID("   ", OpenAIOAuthCodexAccount{}, identity) != "" {
		t.Fatal("blank ids isolate to empty")
	}
}

func TestNormalizeOpenAICodexBuiltinTools(t *testing.T) {
	body := map[string]any{
		"tools": []any{
			map[string]any{"type": "web_search_preview", "search_context_size": "medium"},
			map[string]any{"type": "function", "name": "f"},
		},
	}
	NormalizeOpenAICodexBuiltinTools(body)
	tools := body["tools"].([]any)
	if tools[0].(map[string]any)["type"] != "web_search" {
		t.Fatalf("tool type = %#v", tools[0].(map[string]any)["type"])
	}
	if tools[1].(map[string]any)["type"] != "function" {
		t.Fatalf("tool type = %#v", tools[1].(map[string]any)["type"])
	}
}

func TestNormalizeOpenAICodexResponsesLiteBody(t *testing.T) {
	body := map[string]any{"model": "gpt-5.6-luna", "reasoning": map[string]any{"effort": "low"}}
	NormalizeOpenAICodexResponsesLiteBody(body, "gpt-5.6-luna", nil)
	if body["parallel_tool_calls"] != false {
		t.Fatalf("parallel_tool_calls = %#v", body["parallel_tool_calls"])
	}
	reasoning := body["reasoning"].(map[string]any)
	if reasoning["context"] != "all_turns" {
		t.Fatalf("reasoning.context = %#v", reasoning["context"])
	}
}

func TestIsOpenAICodexClientHeaders(t *testing.T) {
	cases := []struct {
		userAgent string
		want      bool
	}{
		{"codex_cli_rs", true},
		{"Codex Desktop/0.145.0", true},
		{"codex", true},
		{"codexicious", false},
		{"OpenAI/NodeJS", false},
	}
	for _, tc := range cases {
		headers := http.Header{}
		headers.Set("User-Agent", tc.userAgent)
		if got := IsOpenAICodexClientHeaders(headers); got != tc.want {
			t.Fatalf("ua %q: got %v want %v", tc.userAgent, got, tc.want)
		}
	}
}

func TestNormalizeOpenAIOAuthCodexSessionHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("Session-Id", "s-1")
	headers.Set("Thread-Id", "t-1")
	parsed := map[string]any{"model": "gpt-5", "input": "x"}
	result, err := NormalizeOpenAIOAuthCodexParsedBody(parsed, OpenAIOAuthCodexNormalizeInput{
		InputHeaders: headers,
		Account:      OpenAIOAuthCodexAccount{APIKey: "token"},
		Identity:     OpenAIOAuthCodexIdentity{SystemAccountID: "sys-1"},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if result.Session.SessionID == "" || result.Session.ConversationID == "" || result.Session.PromptCacheKey == "" {
		t.Fatalf("session = %#v", result.Session)
	}
	// header building mirrors buildOpenAIOAuthCodexHeaders
	account := OpenAIOAuthCodexAccount{APIKey: "token", Credentials: map[string]any{}}
	parts, err := BuildOpenAIOAuthCodexRequestParts(newCodexRequest(t, "/v1/responses"), headers, account,
		OpenAIOAuthCodexIdentity{SystemAccountID: "sys-1"}, OpenAIOAuthCodexRequestOptions{})
	if err != nil {
		t.Fatalf("parts: %v", err)
	}
	if parts.Headers.Get("Authorization") != "Bearer token" {
		t.Fatalf("authorization = %q", parts.Headers.Get("Authorization"))
	}
	if parts.Headers.Get("Openai-Beta") != "responses=experimental" {
		t.Fatalf("openai-beta = %q", parts.Headers.Get("Openai-Beta"))
	}
	if parts.Headers.Get("Accept") != "text/event-stream" {
		t.Fatalf("accept = %q", parts.Headers.Get("Accept"))
	}
	if parts.Headers.Get("Session-Id") == "" || parts.Headers.Get("Thread-Id") == "" {
		t.Fatalf("session headers = %q/%q", parts.Headers.Get("Session-Id"), parts.Headers.Get("Thread-Id"))
	}
	if parts.Headers.Get("Originator") != OpenAICodexOriginator {
		t.Fatalf("originator = %q", parts.Headers.Get("Originator"))
	}
	if parts.Headers.Get("X-Codex-Turn-Metadata") == "" {
		t.Fatal("synthetic turn metadata missing")
	}
}

func TestBuildOpenAIOAuthCodexRequestPartsAttestation(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Oai-Attestation", strings.Repeat("a", 33*1024))
	_, err := BuildOpenAIOAuthCodexRequestParts(newCodexRequest(t, "/v1/responses"), headers,
		OpenAIOAuthCodexAccount{APIKey: "token", Credentials: map[string]any{}},
		OpenAIOAuthCodexIdentity{SystemAccountID: "sys-1"}, OpenAIOAuthCodexRequestOptions{})
	var adapterErr *OpenAIOAuthCodexAdapterError
	if !errorsAs(err, &adapterErr) || adapterErr.Code != "invalid_openai_oauth_codex_attestation" {
		t.Fatalf("expected attestation error, got %v", err)
	}
	if adapterErr.Message != "Codex 设备证明 header 无效" {
		t.Fatalf("message = %q", adapterErr.Message)
	}
}

func TestIsOpenAIOAuthCodexCompactRequest(t *testing.T) {
	if !IsOpenAIOAuthCodexCompactRequest(newCodexRequest(t, "/v1/responses/compact")) {
		t.Fatal("/v1/responses/compact must be compact")
	}
	if IsOpenAIOAuthCodexCompactRequest(newCodexRequest(t, "/v1/responses")) {
		t.Fatal("/v1/responses must not be compact")
	}
}

func newCodexRequest(t *testing.T, target string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	raw := httptest.NewRequest(http.MethodPost, target, nil)
	request := gatewaypreauth.NewGatewayRequest(raw)
	body := `{"model":"gpt-5","input":"hello"}`
	request.Body = &gatewaybody.Request{
		RawBody: []byte(body),
		Body:    mustJSONObject(t, body),
		State:   &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusParsed},
	}
	return request
}

func TestParseOpenAICodexUsageHeaders(t *testing.T) {
	t.Run("full snapshot", func(t *testing.T) {
		headers := http.Header{}
		headers.Set("X-Codex-Primary-Used-Percent", "12.5")
		headers.Set("X-Codex-Primary-Reset-After-Seconds", "7200.9")
		headers.Set("X-Codex-Primary-Window-Minutes", "300")
		headers.Set("X-Codex-Secondary-Used-Percent", "80")
		headers.Set("X-Codex-Secondary-Reset-After-Seconds", "500000")
		headers.Set("X-Codex-Secondary-Window-Minutes", "10080")
		snapshot := ParseOpenAICodexUsageHeaders(headers)
		if snapshot == nil {
			t.Fatal("expected snapshot")
		}
		if snapshot.PrimaryUsedPercent == nil || *snapshot.PrimaryUsedPercent != 12.5 {
			t.Fatalf("primary used = %v", snapshot.PrimaryUsedPercent)
		}
		if snapshot.PrimaryResetAfterSeconds == nil || *snapshot.PrimaryResetAfterSeconds != 7200 {
			t.Fatalf("primary reset truncation = %v", snapshot.PrimaryResetAfterSeconds)
		}
		queue := &collectorQueue{}
		if !PersistOpenAICodexUsageHeaders(queue, "acc-1", headers, "gateway") {
			t.Fatal("expected persisted job")
		}
		if len(queue.jobs) != 1 {
			t.Fatalf("jobs = %d", len(queue.jobs))
		}
		job := queue.jobs[0]
		if job.Type != "account_usage_snapshot_upsert" || job.Kind != "openai_codex" || job.AccountID != "acc-1" {
			t.Fatalf("job = %#v", job)
		}
		if job.Snapshot["codex_5h_used_percent"] != 12.5 {
			t.Fatalf("5h payload = %#v", job.Snapshot)
		}
		if job.Snapshot["codex_7d_used_percent"] != float64(80) {
			t.Fatalf("7d payload = %#v", job.Snapshot)
		}
		updatedAt, ok := job.Snapshot["codex_usage_updated_at"].(string)
		if !ok {
			t.Fatalf("updatedAt = %#v, want Node toISOString string", job.Snapshot["codex_usage_updated_at"])
		}
		if _, err := time.Parse("2006-01-02T15:04:05.000Z", updatedAt); err != nil {
			t.Fatalf("updatedAt = %q, want Node toISOString millisecond shape", updatedAt)
		}
		resetAt, ok := job.Snapshot["codex_5h_reset_at"].(string)
		if !ok {
			t.Fatalf("5h resetAt = %#v, want Node toISOString string", job.Snapshot["codex_5h_reset_at"])
		}
		if _, err := time.Parse("2006-01-02T15:04:05.000Z", resetAt); err != nil {
			t.Fatalf("5h resetAt = %q, want Node toISOString millisecond shape", resetAt)
		}
	})
	t.Run("no data", func(t *testing.T) {
		if snapshot := ParseOpenAICodexUsageHeaders(http.Header{}); snapshot != nil {
			t.Fatalf("unexpected snapshot %#v", snapshot)
		}
	})
}

type collectorQueue struct{ jobs []RecordMaintenanceJob }

func (q *collectorQueue) EnqueueRecordMaintenanceJob(job RecordMaintenanceJob) {
	q.jobs = append(q.jobs, job)
}

func TestHeaderPolicyStripping(t *testing.T) {
	headers := http.Header{}
	headers.Set("Originator", "codex")
	headers.Set("X-Codex-Turn-Metadata", "{}")
	headers.Set("Anthropic-Version", "2023-06-01")
	headers.Set("X-Api-Key", "key")
	headers.Set("X-Goog-Api-Key", "gem")
	headers.Set("Keep", "yes")

	StripCodexResponsesScopedHeaders(headers)
	if headers.Get("Originator") != "" || headers.Get("X-Codex-Turn-Metadata") != "" {
		t.Fatal("codex headers should be stripped")
	}
	StripAnthropicMessagesScopedHeaders(headers)
	if headers.Get("Anthropic-Version") != "" || headers.Get("X-Api-Key") != "" {
		t.Fatal("anthropic headers should be stripped")
	}
	if headers.Get("Keep") != "yes" {
		t.Fatal("unrelated headers must survive")
	}
}

func TestCopyOfficialOAuthClientRequestHeaders(t *testing.T) {
	input := http.Header{}
	input.Set("Accept", "application/json")
	input.Set("Session-Id", "s")
	input.Set("X-Codex-Custom", "1")
	input.Set("X-Unknown", "drop")

	copied := CopyOfficialOAuthClientRequestHeaders(input, OAuthHeaderProfileOpenAICodex)
	if copied.Get("Accept") != "application/json" || copied.Get("Session-Id") != "s" || copied.Get("X-Codex-Custom") != "1" {
		t.Fatalf("codex copy = %#v", copied)
	}
	if copied.Get("X-Unknown") != "" {
		t.Fatal("x-unknown must be dropped for codex profile")
	}
}
