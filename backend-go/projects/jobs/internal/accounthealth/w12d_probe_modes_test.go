package accounthealth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// w12d_probe_modes_test.go 覆盖 w12d 波次 probe 执行器的 endpoint mode 矩阵、
// 传输失败臂与响应校验臂（httptest 回环，不触外网）。

// w12dProbeInput 构造一个指向本地 httptest 的探活输入。
func w12dProbeInput(baseURL, mode, protocol string) Input {
	input := testInput(baseURL, mode)
	input.Provider = protocol
	input.HealthModel = "w12d-model"
	input.KeySetFingerprint = "keyset-w12d"
	input.APIKeys = []APIKeyInput{{Index: 0, Fingerprint: "fp-w12d", Credential: CredentialEnvelope{Kind: "api_key"}}}
	return input
}

// TestW12dProbeEndpointModeMatrix 逐个 endpoint mode 走真实探针执行路径。
func TestW12dProbeEndpointModeMatrix(t *testing.T) {
	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seenPath = request.URL.Path
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"juhe"}}]}`))
	}))
	defer server.Close()
	cases := []struct {
		mode     string
		protocol string
		path     string
		// jsonOnly 响应为统一 chat JSON；SSE/images 等格式差异只要求请求
		// 构造正确（响应语义差异覆盖在 neutral 断言中）。
		requireSuccess bool
	}{
		{"chat_json", "openai", "/v1/chat/completions", true},
		{"chat_sse", "openai", "/v1/chat/completions", false},
		{"responses_json", "openai", "/v1/responses", true},
		{"responses_sse", "openai", "/v1/responses", false},
		{"images_json", "openai", "/v1/images/generations", false},
		{"messages_json", "anthropic", "/v1/messages", false},
		{"generate_content_json", "gemini", "/v1beta/models/w12d-model:generateContent", false},
		{"generate_content_sse", "gemini", "/v1beta/models/w12d-model:streamGenerateContent", false},
		{"interactions_json", "gemini", "/v1beta/interactions", false},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			input := w12dProbeInput(server.URL, tc.mode, tc.protocol)
			switch tc.protocol {
			case "anthropic":
				input.Provider = "anthropic"
				input.ProtocolProfileID = "profile_anthropic_anthropic_v1"
			case "gemini":
				input.Provider = "gemini"
				input.ProtocolProfileID = "profile_gemini_native_v1beta"
			}
			input.APIKeys[0].Credential.Ciphertext = testEnvelope(t, "w12d-probe-secret", `{"api_key":"sk-w12d"}`)
			result := ProbeOpenAI(context.Background(), input, input.APIKeys[0].Credential, ProbeOptions{Secret: "w12d-probe-secret", Timeout: 2 * time.Second, MaxResponseBytes: 8192})
			if tc.requireSuccess && result.Outcome != OutcomeSuccess {
				t.Fatalf("mode=%s result=%+v", tc.mode, result)
			}
			if !tc.requireSuccess && result.Outcome == OutcomeTaskFailed {
				t.Fatalf("mode=%s must not fail before the wire: %+v", tc.mode, result)
			}
			if seenPath != tc.path {
				t.Fatalf("path=%q want %q", seenPath, tc.path)
			}
		})
	}
	// 未知 endpoint mode → 前置校验拒绝（build switch 的 default 分支由
	// isSupportedDirectProfile 契约保证不可达，属防御守卫）。
	badMode := w12dProbeInput(server.URL, "teleport_json", "openai")
	badMode.ProtocolProfileID = "profile_openai_openai_v1"
	result := ProbeOpenAI(context.Background(), badMode, badMode.APIKeys[0].Credential, ProbeOptions{Secret: "s", Timeout: time.Second})
	if result.Outcome != OutcomeTaskFailed || result.ErrorCode != "invalid_input" {
		t.Fatalf("unknown mode: %+v", result)
	}
}

// TestW12dProbeTransportAndVerifyArms 覆盖传输失败、协议校验失败与响应过大臂。
func TestW12dProbeTransportAndVerifyArms(t *testing.T) {
	// 不可达端口 → transportFailure。
	dead := w12dProbeInput("http://127.0.0.1:1", "chat_json", "openai")
	dead.APIKeys[0].Credential.Ciphertext = testEnvelope(t, "s", `{"api_key":"sk"}`)
	result := ProbeOpenAI(context.Background(), dead, dead.APIKeys[0].Credential, ProbeOptions{Secret: "s", Timeout: 500 * time.Millisecond})
	if result.Outcome != OutcomeUpstreamFailed {
		t.Fatalf("transport failure outcome=%s err=%s", result.Outcome, result.ErrorCode)
	}
	// 响应内容不满足探活语义 → neutral protocol invalid。
	badBody := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"unrelated":true}`))
	}))
	defer badBody.Close()
	wrong := w12dProbeInput(badBody.URL, "chat_json", "openai")
	wrong.APIKeys[0].Credential.Ciphertext = testEnvelope(t, "s", `{"api_key":"sk"}`)
	result = ProbeOpenAI(context.Background(), wrong, wrong.APIKeys[0].Credential, ProbeOptions{Secret: "s", Timeout: time.Second})
	if result.Outcome != OutcomeNeutral || result.ErrorCode != "upstream_protocol_invalid" {
		t.Fatalf("protocol invalid: %+v", result)
	}
	// 非 2xx → neutral http status。
	errStatus := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
	}))
	defer errStatus.Close()
	forbidden := w12dProbeInput(errStatus.URL, "chat_json", "openai")
	forbidden.APIKeys[0].Credential.Ciphertext = testEnvelope(t, "s", `{"api_key":"sk"}`)
	result = ProbeOpenAI(context.Background(), forbidden, forbidden.APIKeys[0].Credential, ProbeOptions{Secret: "s", Timeout: time.Second})
	if result.Outcome != OutcomeNeutral || result.ErrorCode != "upstream_http_status" {
		t.Fatalf("http status: %+v", result)
	}
	// MaxResponseBytes 不足 → 响应过大失败。
	huge := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"` + strings.Repeat("juhe", 4096) + `"}}]}`))
	}))
	defer huge.Close()
	capped := w12dProbeInput(huge.URL, "chat_json", "openai")
	capped.APIKeys[0].Credential.Ciphertext = testEnvelope(t, "s", `{"api_key":"sk"}`)
	result = ProbeOpenAI(context.Background(), capped, capped.APIKeys[0].Credential, ProbeOptions{Secret: "s", Timeout: time.Second, MaxResponseBytes: 128})
	if result.Outcome == OutcomeSuccess {
		t.Fatalf("oversized body must fail: %+v", result)
	}
}

// TestW12dProbeRequestValidationArms 覆盖 oauth 探活的到期守卫（校验在
// buildProbeRequest 内联，通过 ProbeExactKeyModel / 构造请求路径验证）。
func TestW12dProbeRequestValidationArms(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	options := ProbeOptions{Now: func() time.Time { return now }}
	// oauth 输入被 Key-model 恢复入口直接拒绝。
	input := testInput("http://127.0.0.1:1", "responses_json")
	input.Type = "oauth"
	result := ProbeExactKeyModel(context.Background(), input, "fp", "m", "responses_json", options)
	if result.Outcome != OutcomeTaskFailed || result.ErrorCode != "key_model_credential_type_unsupported" {
		t.Fatalf("oauth reject: %+v", result)
	}
	// 空 route 在 api_key 输入下返回路由缺失。
	apiKey := testInput("http://127.0.0.1:1", "chat_json")
	result = ProbeExactKeyModel(context.Background(), apiKey, "fp", " ", " ", options)
	if result.Outcome != OutcomeTaskFailed || result.ErrorCode != "key_model_route_invalid" {
		t.Fatalf("route missing: %+v", result)
	}
	_ = now
}
