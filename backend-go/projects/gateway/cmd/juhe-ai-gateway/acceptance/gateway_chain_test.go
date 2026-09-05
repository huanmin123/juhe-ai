// X05 场景 5：网关链。seed 默认 key → /v1/chat/completions 对 mock upstream
// （httptest 进程内上游）→ 200 非流式 + SSE 流式透传 + usage 落 spool；
// 401 invalid_api_key / 404 非 协议路径 / 429 用户限流契约；/v1/models 端点。
package acceptance

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	acceptanceModel      = "gpt-5.6-sol"
	upstreamAccountKey   = "sk-upstream-account-key"
	defaultGPTStrategyID = "route_strategy_default_gpt_sys_admin"
)

type chainFixture struct {
	fixture      *gatewayFixture
	admin        *acceptanceClient
	upstream     *httptest.Server
	upstreamHits *atomic.Int64
	defaultKey   string
	chatKey      string
}

// startChainFixture 组装链路验收公共环境：mock 上游 + chain 网关 + 通过管理
// 面创建指向 mock 上游的 AI 账户 + 读取 seed 默认 key / seed chat key 明文。
// account_model_mappings 读取按真实 DDL（复合主键，无 id 列）的 Node 排序
// 查询；fresh 种子库上 runtime resolution 必须直接可用。
func startChainFixture(t *testing.T) *chainFixture {
	t.Helper()

	var upstreamHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+upstreamAccountKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"bad upstream key"}}`))
			return
		}
		body := readRequestBody(r)
		if strings.Contains(body, `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: " + sseChunk(`{"id":"chatcmpl-acc-1","object":"chat.completion.chunk","model":"` + acceptanceModel + `","choices":[{"index":0,"delta":{"role":"assistant","content":"验收"},"finish_reason":null}]}`) + "\n\n"))
			_, _ = w.Write([]byte("data: " + sseChunk(`{"id":"chatcmpl-acc-1","object":"chat.completion.chunk","model":"` + acceptanceModel + `","choices":[{"index":0,"delta":{"content":"直通"},"finish_reason":null}]}`) + "\n\n"))
			_, _ = w.Write([]byte("data: " + sseChunk(`{"id":"chatcmpl-acc-1","object":"chat.completion.chunk","model":"` + acceptanceModel + `","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`) + "\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-acc","object":"chat.completion","model":"` + acceptanceModel + `","choices":[{"index":0,"message":{"role":"assistant","content":"验收直通内容"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}))
	t.Cleanup(upstream.Close)

	fixture := startGateway(t, gatewayEnvOptions{ChainEnabled: true})
	admin := &acceptanceClient{t: t, http: fixture.admin, baseURL: fixture.baseURL}

	// 通过管理面创建指向 mock 上游的账户（凭据由服务端密封），并绑定
	// seed 默认 GPT 分组（pgSeedGroups：grp_default_gpt_sys_admin）。
	_, accountCreated := admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "gpt",
		"providerProtocolProfileId": "profile_gpt_openai_v1",
		"name":                      "链路验收账户",
		"type":                      "api_key",
		"credentials":               map[string]any{"api_key": upstreamAccountKey, "base_url": upstream.URL},
		"supportedModels":           []string{acceptanceModel},
		"status":                    "active",
		"groupId":                   "grp_default_gpt_sys_admin",
	}, wantStatus(http.StatusCreated))
	accountID := str(data(accountCreated)["id"])
	if accountID == "" {
		t.Fatalf("chain account create wrong: %#v", accountCreated)
	}
	// go-only 拓扑没有 Node worker 执行初始探活（pending_test 由探活流转
	// 为 active），因此创建时直接声明 status=active 保证可调度；探活链路
	// 由 J3b/J1 切片自身的测试覆盖。
	_, accountDetail := admin.do(http.MethodGet, "/__aisys__/api/accounts/"+accountID, nil, wantStatus(http.StatusOK))
	if str(data(accountDetail)["status"]) != "active" {
		t.Fatalf("chain account not active: %#v", data(accountDetail)["status"])
	}

	// seed 默认 key（maintenance seed 的 is_default key，绑定 seed 默认
	// 策略 route_strategy_default_gpt_sys_admin）与 seed chat key
	// （purpose=chat「AI 对话 API Key」）。
	_, listPayload := admin.do(http.MethodGet, "/__aisys__/api/api-keys?page=1&pageSize=100", nil, wantStatus(http.StatusOK))
	listData := data(listPayload)
	items, _ := listData["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("seed api keys empty: %#v", listPayload)
	}
	defaultKeyID, chatKeyID := "", ""
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		if str(item["routeStrategyId"]) == defaultGPTStrategyID && item["isDefault"] == true {
			defaultKeyID = str(item["id"])
		}
		if str(item["purpose"]) == "chat" {
			chatKeyID = str(item["id"])
		}
	}
	if defaultKeyID == "" || chatKeyID == "" {
		t.Fatalf("seed keys missing (default/chat): %#v", listPayload)
	}
	chain := &chainFixture{
		fixture: fixture, admin: admin, upstream: upstream, upstreamHits: &upstreamHits,
		defaultKey: revealKeySecret(t, admin, defaultKeyID),
		chatKey:    revealKeySecret(t, admin, chatKeyID),
	}
	return chain
}

func revealKeySecret(t *testing.T, admin *acceptanceClient, keyID string) string {
	t.Helper()
	_, secret := admin.do(http.MethodGet, "/__aisys__/api/api-keys/"+keyID+"/secret", nil, wantStatus(http.StatusOK))
	plaintext := str(data(secret)["key"])
	if plaintext == "" {
		t.Fatalf("api-key secret reveal empty: %#v", secret)
	}
	return plaintext
}

func TestAcceptanceGatewayChain(t *testing.T) {
	chain := startChainFixture(t)
	base := chain.fixture.baseURL

	chat := func(authKey string, body string) (int, map[string]any, string) {
		request, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", bytes.NewReader([]byte(body)))
		request.Header.Set("Content-Type", "application/json")
		if authKey != "" {
			request.Header.Set("Authorization", "Bearer "+authKey)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("POST /v1/chat/completions: %v", err)
		}
		defer response.Body.Close()
		raw := readAllBody(t, response)
		payload := map[string]any{}
		_ = jsonUnmarshal(raw, &payload)
		return response.StatusCode, payload, raw
	}

	// 401 契约：未知 key → OpenAI 错误信封 + 逐字节消息「API Key 无效」
	// （gatewaypreauth preauth.go:170 SendGatewayJSONError 401，
	// type=invalid_request_error；对齐 Node request/preauth.ts 未知 key 分支；
	// 「已存在但不可用」key 的 401 另有 invalid_api_key +
	// 「API Key 不可用或已过期」契约，见 authorizationpreflight.go）。
	status, _, raw := chat("sk-definitely-wrong", fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}]}`, acceptanceModel))
	if status != http.StatusUnauthorized {
		t.Fatalf("invalid key status=%d body=%s", status, raw)
	}
	if !strings.Contains(raw, "\"message\":\"API Key 无效\"") || !strings.Contains(raw, "invalid_request_error") {
		t.Fatalf("invalid key contract wrong: %s", raw)
	}

	// 200 非流式：seed 默认 key → mock 上游 → 透传（choices 消息逐字节等于
	// 上游 content「验收直通内容」）。
	var completionRaw string
	deadline := time.Now().Add(20 * time.Second)
	for {
		var completionPayload map[string]any
		status, completionPayload, raw = chat(chain.defaultKey, fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"你好"}]}`, acceptanceModel))
		if status == http.StatusOK {
			choices, _ := completionPayload["choices"].([]any)
			if len(choices) == 0 {
				t.Fatalf("completion missing choices: %s", raw)
			}
			completionRaw = raw
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("chain completion not 200 in time: status=%d body=%s", status, raw)
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !strings.Contains(completionRaw, "验收直通内容") {
		t.Fatalf("upstream content missing: %s", completionRaw)
	}
	if chain.upstreamHits.Load() < 1 {
		t.Fatalf("mock upstream not hit: %d", chain.upstreamHits.Load())
	}

	// SSE 流式透传：chunk 内容 + [DONE] 终帧 + text/event-stream。
	sseRequest, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions",
		bytes.NewReader([]byte(fmt.Sprintf(`{"model":"%s","stream":true,"messages":[{"role":"user","content":"流式"}]}`, acceptanceModel))))
	sseRequest.Header.Set("Content-Type", "application/json")
	sseRequest.Header.Set("Authorization", "Bearer "+chain.defaultKey)
	sseResponse, err := http.DefaultClient.Do(sseRequest)
	if err != nil {
		t.Fatalf("POST stream: %v", err)
	}
	defer sseResponse.Body.Close()
	if sseResponse.StatusCode != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", sseResponse.StatusCode, readAllBody(t, sseResponse))
	}
	if ct := sseResponse.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("stream content-type=%q", ct)
	}
	var sseBuilder strings.Builder
	scanner := bufio.NewScanner(sseResponse.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		sseBuilder.WriteString(scanner.Text())
		sseBuilder.WriteString("\n")
	}
	sseBody := sseBuilder.String()
	if !strings.Contains(sseBody, `"content":"验收"`) || !strings.Contains(sseBody, `"content":"直通"`) {
		t.Fatalf("sse chunks missing upstream deltas: %s", sseBody)
	}
	if !strings.Contains(sseBody, "[DONE]") {
		t.Fatalf("sse missing [DONE]: %s", sseBody)
	}

	// usage 落 spool：异步持久化桥（chain_usage.go spooledUsageRecorder）
	// 最终把记录写入 JUHE_AI_USAGE_SPOOL_DIRECTORY。
	spoolHasEntries := false
	spoolDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(spoolDeadline) {
		entries, err := os.ReadDir(chain.fixture.spoolDir)
		if err == nil && len(entries) > 0 {
			spoolHasEntries = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !spoolHasEntries {
		t.Fatalf("usage spool empty after completion; dir=%s", chain.fixture.spoolDir)
	}

	// /v1/models：认证后模型列表（协议端点）。
	modelsRequest, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	modelsRequest.Header.Set("Authorization", "Bearer "+chain.defaultKey)
	modelsResponse, err := http.DefaultClient.Do(modelsRequest)
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer modelsResponse.Body.Close()
	modelsRaw := readAllBody(t, modelsResponse)
	if modelsResponse.StatusCode != http.StatusOK {
		t.Fatalf("models status=%d body=%s", modelsResponse.StatusCode, modelsRaw)
	}
	if !strings.Contains(modelsRaw, acceptanceModel) {
		t.Fatalf("models missing %s: %s", acceptanceModel, modelsRaw)
	}

	// 404 契约：非协议 /v1 路径（chain_v1.go：`{"message":"资源不存在"}`，
	// 对齐 Node server.ts rejectUnrecognizedGatewayProtocolRequest）。
	notFoundRequest, _ := http.NewRequest(http.MethodGet, base+"/v1/definitely-not-a-protocol-path", nil)
	notFoundResponse, err := http.DefaultClient.Do(notFoundRequest)
	if err != nil {
		t.Fatalf("GET non-protocol /v1: %v", err)
	}
	defer notFoundResponse.Body.Close()
	notFoundRaw := readAllBody(t, notFoundResponse)
	if notFoundResponse.StatusCode != http.StatusNotFound || strings.TrimSpace(notFoundRaw) != `{"message":"资源不存在"}` {
		t.Fatalf("non-protocol /v1 contract wrong: status=%d body=%s", notFoundResponse.StatusCode, notFoundRaw)
	}

	// 429 契约：用户限流（settings.gatewayUserRequestLimitPerMinute=1 →
	// 第二次请求 429；preauth.go「你的每分钟请求数已达到 1 次，请联系管理员
	// 提升额度。」；对齐 Node request/preauth.ts 用户限流消息）。
	chain.admin.do(http.MethodPatch, "/__aisys__/api/settings",
		map[string]any{"gatewayUserRequestLimitPerMinute": 1}, wantStatus(http.StatusOK))
	defer func() {
		chain.admin.do(http.MethodPatch, "/__aisys__/api/settings",
			map[string]any{"gatewayUserRequestLimitPerMinute": 0})
	}()
	sawLimited := false
	limitDeadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(limitDeadline) {
		status, _, raw = chat(chain.defaultKey, fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"限流"}]}`, acceptanceModel))
		if status == http.StatusTooManyRequests {
			if !strings.Contains(raw, "请求数已达到 1 次") || !strings.Contains(raw, "请联系管理员提升额度。") {
				t.Fatalf("429 contract wrong: %s", raw)
			}
			sawLimited = true
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	if !sawLimited {
		t.Fatalf("user request limit never produced 429")
	}
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func sseChunk(json string) string { return json }

func readRequestBody(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	var builder strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			builder.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return builder.String()
}

func readAllBody(t *testing.T, response *http.Response) string {
	t.Helper()
	if response.Body == nil {
		return ""
	}
	var builder strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := response.Body.Read(buf)
		if n > 0 {
			builder.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return builder.String()
}

func jsonUnmarshal(raw string, target any) error {
	return json.Unmarshal([]byte(raw), target)
}
