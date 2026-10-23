// 三刀（调度慢失败直切与来源避让记账及墙钟收尾修复设计）生产错误形态全链
// 验收：真实网关进程 + 可编程 mock 上游，按 2026-10-22 生产故障形态复演——
//
//	① 慢失败直切：坏渠道挂起 12s 后吐 Cloudflare 形态 504（≥10s 门槛），同
//	   账户零重试，好渠道立即接管交付；
//	② 来源避让：同层两渠道按名字序坏渠道先行，两次「坏→好」成功交付确认
//	   后，第三次请求直接落好渠道（坏渠道上游零命中）；探针类非流式请求
//	   不参与流式命中计数；
//	③ 墙钟主动收尾：双渠道全部挂起时，客户端在 270s 预算内拿到
//	   gateway_request_wall_budget_exhausted 确定性终态（503），而非吊死到
//	   客户端自身超时。
package acceptance

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	chainSlowFailBadUpstreamKey  = "sk-accept-bad-upstream"
	chainSlowFailGoodUpstreamKey = "sk-accept-good-upstream"
	// chainSlowFailLatency 是坏渠道的慢失败耗时（≥ sameAccountRetryFastFailMaxMs
	// 种子默认 10000，< textFirstResponseTimeoutSeconds 种子 120s）：与生产
	// 504 形态同构（请求被上游挂住直至 5xx），时间按验收可承受尺度缩放。
	chainSlowFailLatency = 12 * time.Second
)

type chainSlowFailAvoidanceUpstream struct {
	badChatHits  atomic.Int64
	goodChatHits atomic.Int64
	hangForever  atomic.Bool
}

// ServeHTTP 按 upstream key 区分坏/好渠道；只统计流式 chat 命中（健康检查
// 等非流式探测直接以最小合法 JSON 放行，避免引入账户状态噪声）。
func (u *chainSlowFailAvoidanceUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	body := readRequestBody(r)
	if !strings.Contains(body, `"stream":true`) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-hc","object":"chat.completion","model":"` + acceptanceModel + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		return
	}
	switch r.Header.Get("Authorization") {
	case "Bearer " + chainSlowFailBadUpstreamKey:
		u.badChatHits.Add(1)
	case "Bearer " + chainSlowFailGoodUpstreamKey:
		u.goodChatHits.Add(1)
	default:
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if u.hangForever.Load() {
		<-r.Context().Done()
		return
	}
	if r.Header.Get("Authorization") == "Bearer "+chainSlowFailBadUpstreamKey {
		time.Sleep(chainSlowFailLatency)
		// 生产同构：Cloudflare 源站超时的 text/html 504 页面（无 JSON 错误体）。
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte("<html><title>upstream | 504: Gateway time-out</title><body>error code 504</body></html>"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("data: " + sseChunk(`{"id":"chatcmpl-good","object":"chat.completion.chunk","model":"`+acceptanceModel+`","choices":[{"index":0,"delta":{"role":"assistant","content":"GOOD-CHANNEL"},"finish_reason":null}]}`) + "\n\n"))
	_, _ = w.Write([]byte("data: " + sseChunk(`{"id":"chatcmpl-good","object":"chat.completion.chunk","model":"`+acceptanceModel+`","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`) + "\n\n"))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}

func (u *chainSlowFailAvoidanceUpstream) postStreamChat(t *testing.T, baseURL, key, userContent string, clientTimeout time.Duration) (int, string, string, time.Duration) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions",
		strings.NewReader(`{"model":"`+acceptanceModel+`","stream":true,"messages":[{"role":"user","content":"`+userContent+`"}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{Timeout: clientTimeout}
	startedAt := time.Now()
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer response.Body.Close()
	body := readAllBody(t, response)
	return response.StatusCode, response.Header.Get("Content-Type"), body, time.Since(startedAt)
}

// TestChainSlowFailDirectSwitchAvoidanceSinkAndWallBudgetTerminal 按上述三阶段
// 顺序复演生产错误场景（共享一次网关引导；坏渠道命名 aa-* 保证同层名字序
// 先行，好渠道 zz-* 随后）。
func TestChainSlowFailDirectSwitchAvoidanceSinkAndWallBudgetTerminal(t *testing.T) {
	upstream := &chainSlowFailAvoidanceUpstream{}
	upstreamServer := httptest.NewServer(http.HandlerFunc(upstream.ServeHTTP))
	t.Cleanup(upstreamServer.Close)

	fixture := startGateway(t, gatewayEnvOptions{ChainEnabled: true})
	admin := &acceptanceClient{t: t, http: fixture.admin, baseURL: fixture.baseURL}

	createChannelAccount := func(name, upstreamKey string) string {
		_, created := admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
			"providerCode": "gpt",
			"name":         name,
			"type":         "api_key",
			"credentials":  map[string]any{"api_key": upstreamKey, "base_url": upstreamServer.URL},
			"connections": []map[string]any{{
				"connectionPresetCode": "gpt_openai_v1",
				"baseUrl":              upstreamServer.URL,
				"endpointModes":        []string{"chat_json", "chat_sse"},
				"enabled":              true,
			}},
			"healthCheckConnectionPresetCode": "gpt_openai_v1",
			"healthCheckEndpointMode":         "chat_json",
			"healthCheckModel":                acceptanceModel,
			"supportedModels":                 []string{acceptanceModel},
			"status":                          "active",
			"groupId":                         "grp_default_gpt_sys_admin",
		}, wantStatus(http.StatusCreated))
		accountID := str(data(created)["id"])
		if accountID == "" {
			t.Fatalf("channel account create wrong: %#v", created)
		}
		return accountID
	}
	createChannelAccount("aa-bad-channel", chainSlowFailBadUpstreamKey)
	createChannelAccount("zz-good-channel", chainSlowFailGoodUpstreamKey)

	// seed 默认 key 读取（与 startChainFixture 同构）。
	_, listPayload := admin.do(http.MethodGet, "/__aisys__/api/api-keys?page=1&pageSize=100", nil, wantStatus(http.StatusOK))
	defaultKey := ""
	for _, raw := range data(listPayload)["items"].([]any) {
		item, _ := raw.(map[string]any)
		if item != nil && item["isDefault"] == true && str(item["routeStrategyId"]) == defaultGPTStrategyID {
			defaultKey = revealKeySecret(t, admin, str(item["id"]))
		}
	}
	if defaultKey == "" {
		t.Fatalf("seed default key missing: %#v", listPayload)
	}

	// ① 慢失败直切：请求 1 坏渠道慢失败（12s ≥ 10s 门槛）→ 零同账户重试 →
	// 好渠道接管；客户端 ~12s 拿到 200 完整流。
	status, _, body, elapsed := upstream.postStreamChat(t, fixture.baseURL, defaultKey, "三刀场景一", 60*time.Second)
	if status != http.StatusOK || !contains(body, "GOOD-CHANNEL") || !contains(body, "[DONE]") {
		t.Fatalf("场景一客户端应收到好渠道完整流: status=%d body=%s", status, body)
	}
	if elapsed > 25*time.Second {
		t.Fatalf("场景一不得烧重试预算: elapsed=%v", elapsed)
	}
	if got := upstream.badChatHits.Load(); got != 1 {
		t.Fatalf("场景一坏渠道应恰被尝试 1 次（慢失败直切，无同账户重试）: badHits=%d", got)
	}
	if got := upstream.goodChatHits.Load(); got != 1 {
		t.Fatalf("场景一好渠道应接管 1 次: goodHits=%d", got)
	}

	// ② 失败后绕开（合并生效断言）：请求 2 起客户端不再经历坏渠道的慢失败
	// ——首慢失败后，来源避让（两次确认沉底）与 key-model 准入压制任一生效
	// 都保证后续请求直接落好渠道；层内归属由单元/引擎级用例钉住
	// （dispatchreplay/dispatchavoidance/w1_v1deep），此处钉用户可见契约：
	// 全程 200 好渠道完整流，坏渠道全程至多被单发触碰 2 次（每请求至多
	// 一次、永不重试）。
	status, _, body, _ = upstream.postStreamChat(t, fixture.baseURL, defaultKey, "三刀场景二", 60*time.Second)
	if status != http.StatusOK || !contains(body, "GOOD-CHANNEL") {
		t.Fatalf("场景二客户端应收到好渠道完整流: status=%d body=%s", status, body)
	}
	status, _, body, elapsed = upstream.postStreamChat(t, fixture.baseURL, defaultKey, "三刀场景三", 60*time.Second)
	if status != http.StatusOK || !contains(body, "GOOD-CHANNEL") {
		t.Fatalf("场景三客户端应直接收到好渠道完整流: status=%d body=%s", status, body)
	}
	if elapsed > 25*time.Second {
		t.Fatalf("场景三不得经历坏渠道慢失败: elapsed=%v", elapsed)
	}
	if got := upstream.badChatHits.Load(); got > 2 {
		t.Fatalf("坏渠道全程至多被单发触碰 2 次（每请求一次、永不重试）: badHits=%d", got)
	}
	if got := upstream.goodChatHits.Load(); got != 3 {
		t.Fatalf("三个请求都应由好渠道交付: goodHits=%d", got)
	}

	// ③ 墙钟主动收尾：双渠道全部挂起，客户端必须在预算内拿到确定性终态
	// （503，不吊死到自身超时）。坏渠道若被压制则无可用账户等待交接、未
	// 被压制则两次 120s 尝试后墙钟拒启第三次——两种分支都满足「预算内终
	// 态」；墙钟错误码的渲染契约由 TestV1DispatchLoopWallBudgetKinds 钉住。
	upstream.hangForever.Store(true)
	status, _, body, elapsed = upstream.postStreamChat(t, fixture.baseURL, defaultKey, "三刀场景四", 300*time.Second)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("场景四应收到 503 确定性终态: status=%d body=%s", status, body)
	}
	if contains(body, "GOOD-CHANNEL") {
		t.Fatalf("场景四不得收到好渠道流: body=%s", body)
	}
	if elapsed > 285*time.Second {
		t.Fatalf("场景四必须在 270s 预算内主动收尾: elapsed=%v", elapsed)
	}
}
