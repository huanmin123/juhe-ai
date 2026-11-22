package main

// 2026-11-20 火山 openspeech 同步 STT（recognize/flash）链级用例：对外
// `/v1/audio/transcriptions` multipart → 网关 adapter 改写成 base64 JSON →
// mockupstream 头驱动一次性识别 → OpenAI 形态归一 + 秒级计量/成本落 usage
// spool。与 BUG-0370 的真实上游验证同构，锁定本地可回归的完整协议面。

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// seedMediaVolcengineSyncASRAccount 种 volcengine 分组 + 同步转写账户 +
// 独立路由策略与 API Key。凭据带 speech_token 与显式
// `audio_transcription_json`（opt-in）；模型行同样只声明该同步模式。
func seedMediaVolcengineSyncASRAccount(t *testing.T, fixture *chainFixture, id, apiKey, speechToken string) string {
	t.Helper()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(fixtureBind(query), args...); err != nil {
			t.Fatalf("seed volcengine sync asr row: %v: %v", query, err)
		}
	}
	keySecret := "sk-chain-volc-sync-asr"
	credentials := map[string]any{
		"api_key":      apiKey,
		"base_url":     "https://ark.cn-beijing.volces.com",
		"speech_token": speechToken,
		"supported_endpoint_modes": []string{
			"audio_transcription_json",
		},
	}
	seed(`INSERT INTO groups (id, system_account_id, enabled, group_type) VALUES ('group_volc_sync_asr', ?, 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (id, system_account_id, vendor_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, schedulable, concurrency_limit, priority, capability_domain, credentials_encrypted, deleted_at) VALUES (?, ?, 'volcengine', 'profile_volcengine_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, 0, 'audio', ?, NULL)`,
		id, fixture.systemAccount, id, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_volc_sync_asr', ?, ?, 1, '2026-11-20T00:00:00.000Z')`, fixture.systemAccount, id)
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, media_pricing_json, created_at, updated_at)
		VALUES ('mdl_volc_seedasr_sync', 'volcengine', 'volc.seedasr.auc', 'volc.seedasr.auc', 'global', 'active', '["audio_transcription_json"]', '{"version":1,"unit":"second","baseUsd":3.174603174603175e-05,"baseTier":"","tierMultipliers":{}}', '2026-11-20T00:00:00.000Z', '2026-11-20T00:00:00.000Z') ON CONFLICT (id) DO NOTHING`)
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at)
		VALUES (?, 'volc.seedasr.auc', 'mdl_volc_seedasr_sync', '2026-11-20T00:00:00.000Z')`, id)
	chainSeedAccountConnection(t, fixture.db, id, "https://ark.cn-beijing.volces.com", "audio_transcription_json")
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_volc_sync_asr', ?, 'volc同步转写', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_volc_sync_asr', 'rs_volc_sync_asr', ?, 'group_volc_sync_asr', 0, 1, 'active', '2026-11-20T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, scheduling_preference_id, created_at)
		VALUES ('key_volc_sync_asr', ?, 'rs_volc_sync_asr', 'volc同步转写Key', ?, 'active', 'schpref-builtin-chat-cost-first', '2026-11-20T00:00:00.000Z')`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(keySecret))
	return keySecret
}

// TestChainMediaVolcengineASRSyncTranscription 覆盖同步转写全链：multipart
// 文件 → base64 `audio.data` + 三头 + speech_token 认证 → mock 一次性
// `20000000` + duration 3996ms → 对外 `{"text","duration"}` → usage spool
// `audioInputSeconds=3.996` + 目录秒价成本、无 usageMissing。
func TestChainMediaVolcengineASRSyncTranscription(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	t.Setenv(chainVolcengineSpeechUpstreamEnv, mock.URL)
	apiKey := seedMediaVolcengineSyncASRAccount(t, fixture, "acc_volc_sync_asr", "sk-volc-sync-asr-a", "ark-test-sync-speech-token")
	// 同步转写分支按派发候选的 SupportedEndpointModes 判定 openspeech 域根
	// 改写；种子连接行在 fixture 建缓存之后落库，重建缓存装载新 mode 集。
	fixture.cache = w1FreshCacheFor(t, fixture)

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()
	defer mock.Close()

	const audioBytes = "mock-mp3-audio-bytes"
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "volc.seedasr.auc"); err != nil {
		t.Fatalf("write model field: %v", err)
	}
	if err := writer.WriteField("response_format", "json"); err != nil {
		t.Fatalf("write response_format field: %v", err)
	}
	filePart, err := writer.CreateFormFile("file", "sample.mp3")
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := filePart.Write([]byte(audioBytes)); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/audio/transcriptions", &body)
	if err != nil {
		t.Fatalf("build transcription request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-Mock-Scenario", string(platformmock.ScenarioMediaVolcengineASRSyncOK))
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("do transcription request: %v", err)
	}
	payloadRaw, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("transcription status=%d body=%s", response.StatusCode, payloadRaw)
	}
	if got := response.Header.Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	var payload struct {
		Text     string  `json:"text"`
		Duration float64 `json:"duration"`
	}
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		t.Fatalf("decode transcription response: %v (%s)", err, payloadRaw)
	}
	if payload.Text != "Hello from the mock sync transcription." {
		t.Fatalf("text = %q", payload.Text)
	}
	if payload.Duration != 3.996 {
		t.Fatalf("duration = %v, want 3.996（duration 毫秒 ÷1000）", payload.Duration)
	}

	// 出站断言：固定语音域路径 + speech_token 作 X-Api-Key + 三头；不得
	// 携带 Authorization；报文是 base64 `audio.data` + format + model_name。
	requests := mock.Requests()
	if len(requests) != 1 {
		t.Fatalf("出站请求计数 = %d, want 1: %+v", len(requests), requests)
	}
	outbound := requests[0]
	if outbound.Method != http.MethodPost || outbound.Path != "/api/v3/auc/bigmodel/recognize/flash" {
		t.Fatalf("出站形态 = %s %s, want POST recognize/flash", outbound.Method, outbound.Path)
	}
	if outbound.APIKeyHeader != "ark-test-sync-speech-token" {
		t.Fatalf("X-Api-Key = %q, want speech_token 凭据", outbound.APIKeyHeader)
	}
	if outbound.AuthHeader != "" {
		t.Fatalf("同步转写出站不得携带 Authorization: %q", outbound.AuthHeader)
	}
	if outbound.ResourceIDHeader != "volc.seedasr.auc" {
		t.Fatalf("X-Api-Resource-Id = %q, want volc.seedasr.auc", outbound.ResourceIDHeader)
	}
	if !strings.HasPrefix(outbound.RequestIDHeader, "volcsync-") {
		t.Fatalf("X-Api-Request-Id = %q, want volcsync-*", outbound.RequestIDHeader)
	}
	wantBase64 := base64.StdEncoding.EncodeToString([]byte(audioBytes))
	for _, want := range []string{
		`"data":"` + wantBase64 + `"`,
		`"format":"mp3"`,
		`"model_name":"bigmodel"`,
	} {
		if !strings.Contains(outbound.Body, want) {
			t.Fatalf("出站报文缺少 %s: %s", want, outbound.Body)
		}
	}

	// usage：上游 duration 3.996s 落 audioInputSeconds，按目录秒价
	// $3.174603174603175e-05/s 估算成本；有计量不得 usageMissing。
	record := waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"audioInputSeconds":3.996`) &&
			strings.Contains(text, `"endpoint":"POST /v1/audio/transcriptions"`)
	}, "volcengine sync asr 终态计量记录")
	if strings.Contains(record, `"usageMissing":true`) {
		t.Fatalf("有上游 duration 不得标记 usageMissing: %s", record)
	}
	var usageRecord map[string]any
	if err := json.Unmarshal([]byte(record), &usageRecord); err != nil {
		t.Fatalf("decode spool record: %v (%s)", err, record)
	}
	cost, ok := usageRecord["costUsd"].(float64)
	if !ok {
		t.Fatalf("spool 缺少 costUsd: %s", record)
	}
	wantCost := 3.996 * 3.174603174603175e-05
	// 成本在 usage 记录中按 fixedNumber 10 位小数舍入（与真实上游样本
	// cost_usd=0.0001268571 一致）。
	if math.Abs(cost-wantCost) > 1e-10 {
		t.Fatalf("costUsd = %v, want ≈%v（3.996s × 目录秒价，10 位小数舍入）", cost, wantCost)
	}
	if got := usageRecord["quantitySource"]; got != "upstream" {
		t.Fatalf("quantitySource = %v, want upstream", got)
	}
}
