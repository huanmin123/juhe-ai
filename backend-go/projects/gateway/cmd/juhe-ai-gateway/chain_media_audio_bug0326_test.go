package main

// BUG-0326 语音上游 4xx 业务拒绝分级 chain 级测试（问题档案
// docs/bug/问题-0326-语音上游4xx业务拒绝被渲染为503可重试.md）：音频同步
// lane 的上游非 2xx 按状态码分级——
//  1. 确定性业务拒绝 4xx（401/403/407/408/429 保留集之外）→ 失败派发器短路
//     透传（ReturnResponse）：客户端拿到上游真实状态码与错误信封、不换账户
//     重试（attemptCount=1），不再被派发耗尽渲染伪装成 503 upstream_retryable_error；
//  2. 5xx → 维持既有候选切换（换账户重建成功），不透传；
//  3. 401/403 → 维持既有失败派发语义（不透传给客户端，单账户耗尽仍 503）；
//  4. 429 → 维持受理前限流换账户重试（mock 契约 §3.2.4 既有钉面）。
//
// 状态分级落在失败派发器（chainMediaSyncAudioRejectionShortCircuit，
// chain_ports.go），对 LaneAudio 全部同步音频出站面生效（/v1/audio/speech
// 的火山 create /api/v3/tts/create 与流式 unidirectional 两出站面、openai 族
// 直连透传、gemini/minimax TTS 同属本 lane）；本文件用火山 create 面钉行，
// 分级判定与出站面无关。账户状态语义零改动的对照面（任意状态码互换不改变
// generic 请求的内部状态动作）由 AI账户错误语义与状态变更边界 §6 既有门槛
// 覆盖，此处只钉客户端可见分级与候选行为。
import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// seedExtraVolcengineSpeechAccount 向既有 volcengine 语音分组
// （group_volcengine_speech，由 seedMediaVolcengineSpeechAccount 创建）追加
// 一个同组候选账户，返回其 speech_token（上游请求按 X-Api-Key 归属账户）。
func seedExtraVolcengineSpeechAccount(t *testing.T, fixture *chainFixture, id, speechToken string, priority int) string {
	t.Helper()
	credentials := map[string]any{
		"api_key":                  "ark-" + id,
		"base_url":                 "https://ark.invalid",
		"supported_endpoint_modes": []string{"audio_speech"},
		"speech_token":             speechToken,
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(fixtureBind(query), args...); err != nil {
			t.Fatalf("seed extra volcengine speech row %s: %v: %v", id, query, err)
		}
	}
	seed(`INSERT INTO accounts (id, system_account_id, vendor_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at) VALUES (?, ?, 'volcengine', 'profile_volcengine_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, ?, ?, NULL)`,
		id, fixture.systemAccount, id, priority, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_volcengine_speech', ?, ?, 1, '2026-10-04T00:00:00.000Z')`, fixture.systemAccount, id)
	// B4 第一批单连接事实源：BaseURL 与 mode 集改连接行（语音出站面 URL 由
	// chainVolcengineSpeechUpstreamEnv seam 决定，连接行只承载候选过滤 mode）。
	chainSeedAccountConnection(t, fixture.db, id, "https://ark.invalid", "audio_speech")
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at)
		VALUES (?, 'doubao-tts', 'mdl_volc_doubao_tts', '2026-10-04T00:00:00.000Z')`, id)
	return speechToken
}

// newScenarioSwitchingMock 把 mock 引擎包一层按请求序数改写场景头的上游：
// 第 forceFirstRequests 个请求（1-based）注入 forceScenario，其余清除场景头
// （火山 create 面缺省 OK）。返回外层 server 供账户 base_url/seam 指向。
func newScenarioSwitchingMock(t *testing.T, inner *platformmock.Server, forceScenario platformmock.Scenario, forceFirstRequests int64) *httptest.Server {
	t.Helper()
	var seen atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen.Add(1) <= forceFirstRequests {
			r.Header.Set("X-Mock-Scenario", string(forceScenario))
		} else {
			r.Header.Del("X-Mock-Scenario")
		}
		inner.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

// TestChainBug0326VolcengineSpeechUpstream400PassthroughNoSwitch 钉住档案验收
// 主行：音频同步 lane 上游 400 业务拒绝 → 客户端拿到上游真实状态码与错误
// 信封（非 503 upstream_retryable_error）、不换账户重试（第二候选零出站，
// attemptCount=1）。透传行照落 usage 失败事实（success=false + 真实状态码）。
func TestChainBug0326VolcengineSpeechUpstream400PassthroughNoSwitch(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	t.Setenv(chainVolcengineSpeechUpstreamEnv, mock.URL)
	// 第一候选 priority 0（seedMediaVolcengineSpeechAccount 固定），第二候选
	// priority 10——若 400 仍进候选切换，第二账户必被命中，断言可证伪。
	seedMediaVolcengineSpeechAccount(t, fixture, "acc_volc_400_a", mock.URL, "ark-400-a", true)
	seedExtraVolcengineSpeechAccount(t, fixture, "acc_volc_400_b", "volc-tts-token-b", 10)

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/audio/speech", "sk-chain-volcengine-speech",
		`{"model":"doubao-tts","input":"今天天气怎么样","voice":"BV700_streaming"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioStatus400)})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("上游 400 speech status=%d, want 400（透传上游状态，非 503 upstream_retryable_error）body=%s", response.StatusCode, payload)
	}
	// 上游错误信封原样透传（mock status_400 信封：invalid_parameter /
	// Invalid parameters——message 含上游业务语义，非网关固定文案）。
	if !strings.Contains(string(payload), "Invalid parameters") || !strings.Contains(string(payload), "invalid_parameter") {
		t.Fatalf("透传体缺少上游错误信封语义: %s", payload)
	}
	if strings.Contains(string(payload), "upstream_retryable_error") {
		t.Fatalf("透传体不得再携带网关可重试错误码: %s", payload)
	}

	// attemptCount=1：上游恰好一次出站、命中第一候选（第二候选零出站）。
	requests := mock.Requests()
	if len(requests) != 1 {
		t.Fatalf("上游出站请求数 = %d, want 1（确定性业务拒绝不换账户重试）: %+v", len(requests), requests)
	}
	if requests[0].Path != "/api/v3/tts/create" || requests[0].APIKeyHeader != "volc-tts-token-a" {
		t.Fatalf("出站请求形态/归属错误: %+v", requests[0])
	}

	// usage 失败事实照落且携带真实上游状态码（口径对齐：透传的完整非 2xx 以
	// 失败行结束，不伪装成功、不改写为可重试错误）。
	waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"endpoint":"POST /v1/audio/speech"`) &&
			strings.Contains(text, `"success":false`) &&
			strings.Contains(text, `"statusCode":400`)
	}, "BUG-0326 透传失败 usage 行")
}

// TestChainBug0326VolcengineSpeech5xxKeepsCandidateSwitch 钉住 5xx 维持既有
// 重试语义：第一候选上游 500 → 排除换第二候选重建成功（客户端 200 音频），
// 不透传 500。
func TestChainBug0326VolcengineSpeech5xxKeepsCandidateSwitch(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	upstream := newScenarioSwitchingMock(t, mock, platformmock.ScenarioStatus500, 1)
	t.Setenv(chainVolcengineSpeechUpstreamEnv, upstream.URL)
	seedMediaVolcengineSpeechAccount(t, fixture, "acc_volc_5xx_a", upstream.URL, "ark-5xx-a", true)
	seedExtraVolcengineSpeechAccount(t, fixture, "acc_volc_5xx_b", "volc-tts-token-b", 10)

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/audio/speech", "sk-chain-volcengine-speech",
		`{"model":"doubao-tts","input":"你好火山","voice":"BV700_streaming"}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("5xx 换账户后 speech status=%d, want 200 body=%s", response.StatusCode, payload)
	}
	if len(payload) < 2 || payload[0] != 0xFF || payload[1] != 0xFB {
		t.Fatalf("响应不是解码后的 mp3 载荷: % x", payload[:min(2, len(payload))])
	}
	// 两候选各恰好一次出站：第一候选 500（token-a）→ 第二候选成功（token-b）。
	requests := mock.Requests()
	if len(requests) != 2 {
		t.Fatalf("上游出站请求数 = %d, want 2（5xx 维持候选切换）: %+v", len(requests), requests)
	}
	if requests[0].APIKeyHeader != "volc-tts-token-a" || requests[1].APIKeyHeader != "volc-tts-token-b" {
		t.Fatalf("5xx 候选顺序错误: %q -> %q", requests[0].APIKeyHeader, requests[1].APIKeyHeader)
	}
}

// TestChainBug0326VolcengineSpeechCredentialRejectionKeepsDispatchPath 钉住
// 401/403 维持既有失败派发语义：不透传给客户端（区别于 400 业务拒绝），
// 单候选耗尽仍渲染 503 upstream_retryable_error 网关契约，且不换账户原地
// 重放（媒体域无同账户自动轮换，恰好一次出站）。
func TestChainBug0326VolcengineSpeechCredentialRejectionKeepsDispatchPath(t *testing.T) {
	for name, scenario := range map[string]platformmock.Scenario{
		"401": platformmock.ScenarioStatus401,
		"403": platformmock.ScenarioStatus403,
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newChainFixture(t)
			mock := platformmock.New()
			defer mock.Close()
			upstream := newScenarioSwitchingMock(t, mock, scenario, 10_000)
			t.Setenv(chainVolcengineSpeechUpstreamEnv, upstream.URL)
			seedMediaVolcengineSpeechAccount(t, fixture, "acc_volc_cred_"+name, upstream.URL, "ark-cred-a", true)

			chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
			server := httptest.NewServer(chain)
			defer server.Close()

			response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/audio/speech", "sk-chain-volcengine-speech",
				`{"model":"doubao-tts","input":"你好火山","voice":"BV700_streaming"}`, nil)
			if response.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("凭据域 %s speech status=%d, want 503（维持派发耗尽契约，不透传上游状态）body=%s", name, response.StatusCode, payload)
			}
			if !strings.Contains(string(payload), "upstream_retryable_error") {
				t.Fatalf("凭据域 %s 耗尽契约缺少 upstream_retryable_error: %s", name, payload)
			}
			requests := mock.Requests()
			if len(requests) != 1 {
				t.Fatalf("凭据域 %s 上游出站请求数 = %d, want 1（无同账户原地重放）: %+v", name, len(requests), requests)
			}
		})
	}
}

// TestChainBug0326VolcengineSpeech429KeepsCandidateSwitch 钉住 429 归属（档案
// 分级的实施缺口决议）：受理前限流维持既有换账户重试语义（mock 契约
// §3.2.4），不进 400 类透传。
func TestChainBug0326VolcengineSpeech429KeepsCandidateSwitch(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	upstream := newScenarioSwitchingMock(t, mock, platformmock.ScenarioStatus429, 1)
	t.Setenv(chainVolcengineSpeechUpstreamEnv, upstream.URL)
	seedMediaVolcengineSpeechAccount(t, fixture, "acc_volc_429_a", upstream.URL, "ark-429-a", true)
	seedExtraVolcengineSpeechAccount(t, fixture, "acc_volc_429_b", "volc-tts-token-b", 10)

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/audio/speech", "sk-chain-volcengine-speech",
		`{"model":"doubao-tts","input":"你好火山","voice":"BV700_streaming"}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("429 换账户后 speech status=%d, want 200 body=%s", response.StatusCode, payload)
	}
	requests := mock.Requests()
	if len(requests) != 2 || requests[0].APIKeyHeader != "volc-tts-token-a" || requests[1].APIKeyHeader != "volc-tts-token-b" {
		t.Fatalf("429 候选切换形态错误: n=%d %+v", len(requests), requests)
	}
}

// TestChainAudioSyncBusinessRejectionStatusClassification 钉住状态分级词表与
// lane 门：保留集（401/403/407/408/429）与非 4xx 不透传，其余 4xx 透传；
// 短路门只对音频同步 lane 生效。
func TestChainAudioSyncBusinessRejectionStatusClassification(t *testing.T) {
	statusCases := []struct {
		status int
		want   bool
	}{
		{http.StatusOK, false},
		{http.StatusMovedPermanently, false},
		{399, false},
		{http.StatusBadRequest, true},
		{http.StatusUnauthorized, false},
		{http.StatusPaymentRequired, true},
		{http.StatusForbidden, false},
		{http.StatusNotFound, true},
		{http.StatusProxyAuthRequired, false},
		{http.StatusRequestTimeout, false},
		{http.StatusConflict, true},
		{http.StatusRequestEntityTooLarge, true},
		{http.StatusUnsupportedMediaType, true},
		{http.StatusUnprocessableEntity, true},
		{http.StatusTooManyRequests, false},
		{http.StatusRequestURITooLong, true},
		{499, true},
		{http.StatusInternalServerError, false},
		{http.StatusBadGateway, false},
		{http.StatusServiceUnavailable, false},
		{0, false},
	}
	for _, statusCase := range statusCases {
		if got := chainAudioSyncBusinessRejectionStatus(statusCase.status); got != statusCase.want {
			t.Fatalf("chainAudioSyncBusinessRejectionStatus(%d) = %v, want %v", statusCase.status, got, statusCase.want)
		}
	}
	laneCases := []struct {
		lane string
		want bool
	}{
		{string(gatewayproto.LaneAudio), true},
		{string(gatewayproto.LaneVideo), false},
		{string(gatewayproto.LaneText), false},
		{string(gatewayproto.LaneImage), false},
		{"", false},
	}
	for _, laneCase := range laneCases {
		if got := chainMediaSyncAudioRejectionShortCircuit(laneCase.lane, http.StatusBadRequest); got != laneCase.want {
			t.Fatalf("chainMediaSyncAudioRejectionShortCircuit(%q, 400) = %v, want %v", laneCase.lane, got, laneCase.want)
		}
	}
}
