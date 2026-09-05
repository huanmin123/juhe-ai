package accountprobe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountquality"
)

// fakeSource 是 CandidateSource 的 Mock：按 ProbeRequest 返回预置视图。
type fakeSource struct {
	view *View
	err  error
	last accountquality.ProbeRequest
}

func (f *fakeSource) LoadProbeView(_ context.Context, req accountquality.ProbeRequest) (*View, error) {
	f.last = req
	if f.err != nil {
		return nil, f.err
	}
	return f.view, nil
}

func probeView(baseURL string) *View {
	return &View{
		AccountID:               "acc-1",
		AccountName:             "账户一",
		Type:                    "api_key",
		Status:                  "active",
		ProviderCode:            "openai",
		ProtocolCode:            "openai",
		HealthCheckModel:        "gpt-test",
		HealthCheckEndpointMode: string(ModeChatJSON),
		SupportedModels:         []string{"gpt-test"},
		BaseURL:                 baseURL,
		Credentials:             map[string]any{},
		SelectedAPIKey:          "sk-test",
		APIKeyEntries:           []KeyEntry{{Key: "sk-test", Fingerprint: "fp-default", Index: 0}},
		NormalizeEndpointModes: map[EndpointMode]bool{
			ModeChatJSON: true, ModeChatSSE: true, ModeResponsesJSON: true, ModeResponsesSSE: true,
		},
	}
}

func newTestService(t *testing.T, source CandidateSource) *Service {
	t.Helper()
	service, err := NewService(Options{Source: source, Secret: "test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// TestProbeChatJSONSuccess 验证 OpenAI chat_json 完整成功路径：
// finish_reason + 输出包含预期令牌 → success；证据 framing_complete。
func TestProbeChatJSONSuccess(t *testing.T) {
	var seenPath, seenAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenAuth = r.Header.Get("authorization")
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("请求体必须是 JSON: %v", err)
		}
		if payload["stream"] != false {
			t.Errorf("chat_json 不得流式: %v", payload["stream"])
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	source := &fakeSource{view: probeView(server.URL)}
	service := newTestService(t, source)
	observation, err := service.Probe(context.Background(), accountquality.ProbeRequest{
		AccountID: "acc-1", GroupID: "group-1", SystemAccountID: "sys-1", TrafficSource: "runtime_recovery_probe", Full: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if seenPath != "/v1/chat/completions" {
		t.Fatalf("upstream path=%q", seenPath)
	}
	if seenAuth != "Bearer sk-test" {
		t.Fatalf("authorization=%q", seenAuth)
	}
	if !observation.Result.Success {
		t.Fatalf("success=%v message=%q", observation.Result.Success, observation.Result.Message)
	}
	if observation.Result.StatusCode == nil || *observation.Result.StatusCode != 200 {
		t.Fatalf("statusCode=%v", observation.Result.StatusCode)
	}
	if outcome := accountquality.AutomaticProbeOutcome(observation.Result, observation.Evidence); outcome != accountquality.OutcomeCompleteSuccess {
		t.Fatalf("outcome=%s", outcome)
	}
}

// TestProbeLimitedQuotaFailure 验证 limited（cooldown-retest）路径的额度失败
// 脱敏：message=上游额度不足，保留 retry-after 头与完整响应体文本。
func TestProbeLimitedQuotaFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.Header().Set("retry-after", "120")
		w.Header().Set("x-request-id", "should-not-leak")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"insufficient_quota","message":"You exceeded your current quota, please check your plan and billing details."}}`))
	}))
	defer server.Close()

	view := probeView(server.URL)
	view.FixedKey = &KeyEntry{Key: "sk-fixed", Fingerprint: "fp-1", Index: 1}
	source := &fakeSource{view: view}
	service := newTestService(t, source)
	observation, err := service.Probe(context.Background(), accountquality.ProbeRequest{
		AccountID: "acc-1", GroupID: "group-1", SystemAccountID: "sys-1",
		TrafficSource: "cooldown_retest", Full: false,
		FixedAPIKey: "sk-fixed", FixedKeyFingerprint: "fp-1", FixedKeyIndex: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Result.Success {
		t.Fatal("额度失败不得 success")
	}
	if observation.Result.Message != "上游额度不足" {
		t.Fatalf("limited message=%q", observation.Result.Message)
	}
	if observation.Result.ResponseHeaders["retry-after"] != "120" {
		t.Fatalf("quota headers=%v", observation.Result.ResponseHeaders)
	}
	if _, leak := observation.Result.ResponseHeaders["x-request-id"]; leak {
		t.Fatalf("limited 不得保留非白名单头: %v", observation.Result.ResponseHeaders)
	}
	if !strings.Contains(observation.Result.ResponseBodyText, "insufficient_quota") {
		t.Fatalf("responseBodyText 必须保留完整上游文本: %q", observation.Result.ResponseBodyText)
	}
}

// TestProbeConnectionFailure 验证连接失败分类：transport_incomplete/connection
// → upstream_failure（连接类失败不重试升级）。
func TestProbeConnectionFailure(t *testing.T) {
	source := &fakeSource{view: probeView("http://127.0.0.1:1")}
	service := newTestService(t, source)
	observation, err := service.Probe(context.Background(), accountquality.ProbeRequest{
		AccountID: "acc-1", GroupID: "group-1", SystemAccountID: "sys-1", TrafficSource: "runtime_recovery_probe", Full: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Result.Success {
		t.Fatal("连接失败不得 success")
	}
	if observation.Evidence.TransportFailureKind != accountquality.TransportFailureConnection {
		t.Fatalf("failure kind=%q", observation.Evidence.TransportFailureKind)
	}
	if outcome := accountquality.AutomaticProbeOutcome(observation.Result, observation.Evidence); outcome != accountquality.OutcomeUpstreamFailure {
		t.Fatalf("outcome=%s", outcome)
	}
}

// TestProbeAnthropicMessages 验证 Anthropic 协议请求构造与成功证据。
func TestProbeAnthropicMessages(t *testing.T) {
	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"juhe"}],"stop_reason":"end_turn"}`))
	}))
	defer server.Close()

	view := probeView(server.URL)
	view.ProtocolCode = "anthropic"
	view.HealthCheckEndpointMode = string(ModeMessagesJSON)
	view.NormalizeEndpointModes = map[EndpointMode]bool{ModeMessagesJSON: true, ModeMessagesSSE: true}
	source := &fakeSource{view: view}
	service := newTestService(t, source)
	observation, err := service.Probe(context.Background(), accountquality.ProbeRequest{
		AccountID: "acc-1", GroupID: "group-1", SystemAccountID: "sys-1", Full: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if seenPath != "/v1/messages" {
		t.Fatalf("anthropic path=%q", seenPath)
	}
	if !observation.Result.Success {
		t.Fatalf("anthropic success=%v message=%q", observation.Result.Success, observation.Result.Message)
	}
	if observation.Result.ProtocolCode != string(ProtocolAnthropic) {
		t.Fatalf("protocol=%q", observation.Result.ProtocolCode)
	}
}

// TestProbeGeminiGenerateContent 验证 Gemini 协议 URL 拼接与成功证据。
func TestProbeGeminiGenerateContent(t *testing.T) {
	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"juhe"}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()

	view := probeView(server.URL)
	view.ProtocolCode = "gemini"
	view.HealthCheckEndpointMode = string(ModeGenerateContentJSON)
	view.NormalizeEndpointModes = map[EndpointMode]bool{ModeGenerateContentJSON: true, ModeGenerateContentSSE: true}
	source := &fakeSource{view: view}
	service := newTestService(t, source)
	observation, err := service.Probe(context.Background(), accountquality.ProbeRequest{
		AccountID: "acc-1", GroupID: "group-1", SystemAccountID: "sys-1", Full: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if seenPath != "/v1beta/models/gpt-test:generateContent" {
		t.Fatalf("gemini path=%q", seenPath)
	}
	if !observation.Result.Success {
		t.Fatalf("gemini success=%v message=%q", observation.Result.Success, observation.Result.Message)
	}
}

// TestProbeChatSSEStreaming 验证流式 chat_sse 的完成证据解析与首字计时。
func TestProbeChatSSEStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"juhe\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	view := probeView(server.URL)
	view.HealthCheckEndpointMode = string(ModeChatSSE)
	source := &fakeSource{view: view}
	service := newTestService(t, source)
	observation, err := service.Probe(context.Background(), accountquality.ProbeRequest{
		AccountID: "acc-1", GroupID: "group-1", SystemAccountID: "sys-1", Full: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Result.Success {
		t.Fatalf("chat_sse success=%v message=%q", observation.Result.Success, observation.Result.Message)
	}
	if observation.Result.FirstTokenMS < 0 {
		t.Fatalf("firstTokenMs=%d", observation.Result.FirstTokenMS)
	}
}

// TestProbeHTTP2xxWithoutProtocolEvidence 验证 2xx 但缺协议完成证据的语义失败
// （framing_complete_neutral：不得写入失败状态）。
func TestProbeHTTP2xxWithoutProtocolEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"juhe"}}]}`))
	}))
	defer server.Close()

	source := &fakeSource{view: probeView(server.URL)}
	service := newTestService(t, source)
	observation, err := service.Probe(context.Background(), accountquality.ProbeRequest{
		AccountID: "acc-1", GroupID: "group-1", SystemAccountID: "sys-1", Full: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Result.Success {
		t.Fatal("2xx 缺完成证据不得 success")
	}
	if observation.Result.ErrorCode != "invalid_protocol_success_response" {
		t.Fatalf("errorCode=%q", observation.Result.ErrorCode)
	}
	if outcome := accountquality.AutomaticProbeOutcome(observation.Result, observation.Evidence); outcome != accountquality.OutcomeFramingCompleteNeutral {
		t.Fatalf("outcome=%s", outcome)
	}
}

// TestProbeMissingCandidate 验证候选缺失时返回配置错误（等价 Node
// AccountTestConfigurationError → 队列 onExhausted 路径）。
func TestProbeMissingCandidate(t *testing.T) {
	source := &fakeSource{view: nil}
	service := newTestService(t, source)
	if _, err := service.Probe(context.Background(), accountquality.ProbeRequest{AccountID: "missing", Full: true}); err == nil {
		t.Fatal("候选缺失必须返回错误")
	}
}

// TestFingerprintStableAndDistinct 锁定 HMAC-SHA256 指纹的形状与稳定性
// （跨语言向量由 oauthrefresh crypto_test 覆盖同一信封算法域）。
func TestFingerprintStableAndDistinct(t *testing.T) {
	source := &fakeSource{}
	service := newTestService(t, source)
	first := service.FingerprintAPIKey("sk-abc")
	if len(first) != 64 {
		t.Fatalf("fingerprint 长度=%d", len(first))
	}
	if first != service.FingerprintAPIKey("sk-abc") {
		t.Fatal("指纹必须稳定")
	}
	if first == service.FingerprintAPIKey("sk-other") {
		t.Fatal("不同 Key 指纹必须不同")
	}
}
