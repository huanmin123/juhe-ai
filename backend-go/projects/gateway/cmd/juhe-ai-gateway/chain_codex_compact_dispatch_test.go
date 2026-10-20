package main

// T5（BUG-0274 残余）回归：compact 合成摘要 dispatcher 的生产适配器
//（chainCompactSummaryDispatcher）以真实组合链引擎直接驱动——账户
// BaseURL 指向 httptest 服务器（OpenAIAccountSecret 是纯数据），覆盖三条
// 路径：
//
//	1. 2xx + choices.content —— 成功交换（BodyText / UpstreamOK / 状态码 /
//	   终态与释放闭包可调）；
//	2. 非 2xx / 连接失败 —— 引擎错误原样上抛（消费者 502 收口）；
//	3. 2xx 但无 choices —— 适配器照常返回交换（空摘要由消费者收 502 的
//	   口径边界）。
//
// 组合级装配断言（dispatcher 已晚绑定回填）在
// TestComposeSystemAPIWiresCodexBridgeState 与
// TestW1CodexBridgeComposeWiresRealPreflight。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// compactDispatchAdapter 组装一条真实组合链（chainSmokeDeps 全端口真实装配，
// 含 Transport / 失败派发链），返回绑定该链引擎的合成摘要 dispatcher。适配器
// 不依赖 codex 桥装配（wired=false），晚绑定回填由组合级测试断言。
func compactDispatchAdapter(t *testing.T) *chainCompactSummaryDispatcher {
	t.Helper()
	deps := w1CodexBridgeChainDeps(t, false)
	chain, shutdown, assembleErr := composeGatewayChain(deps)
	if assembleErr != nil {
		t.Fatalf("composeGatewayChain: %v", assembleErr)
	}
	t.Cleanup(shutdown)
	return &chainCompactSummaryDispatcher{engine: chain.engine, auditOf: chain.engineAuditCapture}
}

// compactDispatchInput 构造透传字段齐备的合成摘要派发输入（coordination /
// usage / audit 与生产 preflight 投影同源同型）。
func compactDispatchInput(t *testing.T, accounts []gatewayruntimecache.OpenAIAccountSecret) gatewaycodex.CompactSummaryDispatchInput {
	t.Helper()
	body := map[string]any{
		"model":  "chat-model",
		"stream": false,
		"messages": []any{
			map[string]any{"role": "system", "content": "压缩指令"},
			map[string]any{"role": "user", "content": "上下文"},
		},
	}
	raw := gatewaybody.SerializeGatewayJSONObject(body).Raw
	sourceReq, _ := w1CodexBridgeRequest(http.MethodPost, "/v1/responses", map[string]any{
		"model": "gpt-5",
		"input": "hi",
	})
	synthetic := gatewaycodex.BuildSyntheticChatCompletionsRequest(sourceReq, body)
	wallBudgetMs := int64(60_000)
	nowMs := func() int64 { return time.Now().UnixMilli() }
	wall, err := gatewayrouting.NewGatewayRequestWallBudget(gatewayrouting.GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: nowMs(),
		BudgetMs:            &wallBudgetMs,
		Now:                 nowMs,
	}, nil)
	if err != nil {
		t.Fatalf("create wall budget: %v", err)
	}
	tracker, err := gatewayrouting.NewGatewayRequestAttemptTracker(nil)
	if err != nil {
		t.Fatalf("create attempt tracker: %v", err)
	}
	return gatewaycodex.CompactSummaryDispatchInput{
		Body:     body,
		RawBody:  raw,
		Accounts: accounts,
		Settings: gatewayruntimecache.GatewaySettings{
			TextFirstResponseTimeoutSeconds:          30,
			TextStreamIdleTimeoutSeconds:             300,
			TextUncommittedAttemptMaxLifetimeSeconds: 60,
			NoAvailableAccountWaitTimeoutSeconds:     10,
		},
		Signal:           context.Background(),
		StartedAt:        time.Now().UnixMilli(),
		SyntheticRequest: synthetic,
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{
			TraceID:       "trace-compact-dispatch",
			TrafficSource: gatewayTrafficSource,
		},
		AuditCapture: &chainTestAuditCapture{},
		RequestLane:  "text",
		RequestCoordination: gatewaypreauth.CodexRequestCoordination{
			Scope:                    "gateway_request",
			ServerRetryBudget:        gatewaypreauth.NewServerRetryBudget(0, gatewaypreauth.SystemClock{}),
			GatewayRequestWallBudget: wall,
			RequestAttemptTracker:    tracker,
		},
	}
}

// compactDispatchAccount 构造 api_key openai 账户（BaseURL 指向测试上游）。
func compactDispatchAccount(baseURL string) gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID:              "acc-compact-dispatch",
		ProtocolCode:    "openai",
		ProtocolVersion: "v1",
		Type:            "api_key",
		BaseURL:         baseURL,
		APIKey:          "sk-compact-dispatch-test",
	}
}

// chatCompletionPayload 是上游摘要模型的最小 chat completions 响应。
const chatCompletionPayload = `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"压缩后的摘要"}}],"usage":{"total_tokens":3}}`

// compactSummaryUpstream 起一个假 chat completions 上游，记录收到的请求形态
// （方法 / 路径 / 凭据头 / 模型）。
type compactSummaryUpstream struct {
	server     *httptest.Server
	gotMethod  string
	gotPath    string
	gotAuth    string
	gotModel   string
	reqCount   int
	payload    string
	statusCode int
}

func newCompactSummaryUpstream(t *testing.T, statusCode int, payload string) *compactSummaryUpstream {
	t.Helper()
	upstream := &compactSummaryUpstream{payload: payload, statusCode: statusCode}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.reqCount++
		upstream.gotMethod = r.Method
		upstream.gotPath = r.URL.Path
		upstream.gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(raw, &parsed)
		if model, ok := parsed["model"].(string); ok {
			upstream.gotModel = model
		}
		// 首字耗时按整毫秒取整（NowMs），本机 httptest 常在派发同一毫秒内
		// 返回，使 FirstByteMs 合法地记为 0。成功率用例断言正样本，这里给
		// 上游加固定延迟，消除测量精度抖动。
		time.Sleep(2 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(upstream.statusCode)
		_, _ = io.WriteString(w, upstream.payload)
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

// TestCompactSummaryDispatchUpstreamSuccess：2xx + choices.content 成功路径。
func TestCompactSummaryDispatchUpstreamSuccess(t *testing.T) {
	upstream := newCompactSummaryUpstream(t, http.StatusOK, chatCompletionPayload)
	dispatcher := compactDispatchAdapter(t)
	input := compactDispatchInput(t, []gatewayruntimecache.OpenAIAccountSecret{compactDispatchAccount(upstream.server.URL)})
	exchange, dispatchErr := dispatcher.DispatchCompactSummary(context.Background(), input)
	if dispatchErr != nil {
		t.Fatalf("DispatchCompactSummary: %v", dispatchErr)
	}
	if exchange == nil {
		t.Fatal("成功路径必须返回交换")
	}
	if upstream.reqCount != 1 {
		t.Fatalf("上游请求数 = %d，want 1", upstream.reqCount)
	}
	// 上游收到的是合成 chat completions 请求：路径、凭据头与摘要模型。
	if upstream.gotMethod != http.MethodPost || !strings.HasSuffix(upstream.gotPath, "/v1/chat/completions") {
		t.Fatalf("上游请求形态 = %s %s，want POST */v1/chat/completions", upstream.gotMethod, upstream.gotPath)
	}
	if upstream.gotAuth != "Bearer sk-compact-dispatch-test" {
		t.Fatalf("上游凭据头 = %q，want Bearer sk-compact-dispatch-test", upstream.gotAuth)
	}
	if upstream.gotModel != "chat-model" {
		t.Fatalf("上游请求模型 = %q，want chat-model", upstream.gotModel)
	}
	if exchange.Account.ID != "acc-compact-dispatch" {
		t.Fatalf("exchange.Account.ID = %q", exchange.Account.ID)
	}
	if !exchange.UpstreamOK || exchange.UpstreamStatus != http.StatusOK {
		t.Fatalf("UpstreamOK=%v UpstreamStatus=%d，want true/200", exchange.UpstreamOK, exchange.UpstreamStatus)
	}
	summary := gatewaycodex.ExtractChatCompletionSummary(exchange.BodyText)
	if summary != "压缩后的摘要" {
		t.Fatalf("BodyText 摘要 = %q（BodyText=%q）", summary, exchange.BodyText)
	}
	if exchange.Truncated {
		t.Fatal("短响应不得截断")
	}
	if exchange.FirstByteMs <= 0 {
		t.Fatalf("FirstByteMs = %d，want 正样本", exchange.FirstByteMs)
	}
	// 终态与释放闭包可调：真实引擎句柄（组合测试为 no-op 生命周期）不得 panic。
	exchange.RecordTerminal(gatewaycodex.CompactQualityTerminal{
		OutcomeClass: "completed_response",
		FailureScope: "none",
		Source:       "gateway_transport",
		FirstByteMs:  exchange.FirstByteMs,
	})
	exchange.ReleaseConcurrency()
}

// TestCompactSummaryDispatchUpstreamFailurePropagates：非 2xx 与连接失败两条
// 引擎错误路径都必须原样上抛（exchange 为 nil，消费者 502 收口）。
func TestCompactSummaryDispatchUpstreamFailurePropagates(t *testing.T) {
	dispatcher := compactDispatchAdapter(t)

	t.Run("non_2xx_exhausts_candidates", func(t *testing.T) {
		upstream := newCompactSummaryUpstream(t, http.StatusInternalServerError, `{"error":"upstream down"}`)
		input := compactDispatchInput(t, []gatewayruntimecache.OpenAIAccountSecret{compactDispatchAccount(upstream.server.URL)})
		exchange, dispatchErr := dispatcher.DispatchCompactSummary(context.Background(), input)
		if dispatchErr == nil {
			t.Fatalf("全部候选 500 必须上抛引擎错误，got exchange=%+v", exchange)
		}
		if exchange != nil {
			t.Fatalf("引擎错误路径不得返回交换，got %+v", exchange)
		}
	})

	t.Run("connection_refused_propagates", func(t *testing.T) {
		dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		deadURL := dead.URL
		dead.Close()
		input := compactDispatchInput(t, []gatewayruntimecache.OpenAIAccountSecret{compactDispatchAccount(deadURL)})
		exchange, dispatchErr := dispatcher.DispatchCompactSummary(context.Background(), input)
		if dispatchErr == nil {
			t.Fatalf("连接失败必须上抛引擎错误，got exchange=%+v", exchange)
		}
		if exchange != nil {
			t.Fatalf("引擎错误路径不得返回交换，got %+v", exchange)
		}
	})
}

// TestCompactSummaryDispatchEmptyChoicesPassesThrough：2xx 但无 choices——
// 适配器照常返回成功交换（UpstreamOK=true、BodyText 为上游原文），空摘要的
// 502 收口归消费者（gatewaycodex 侧契约），适配器不得代为报错。
func TestCompactSummaryDispatchEmptyChoicesPassesThrough(t *testing.T) {
	upstream := newCompactSummaryUpstream(t, http.StatusOK, `{"id":"c1","object":"chat.completion","choices":[]}`)
	dispatcher := compactDispatchAdapter(t)
	input := compactDispatchInput(t, []gatewayruntimecache.OpenAIAccountSecret{compactDispatchAccount(upstream.server.URL)})
	exchange, dispatchErr := dispatcher.DispatchCompactSummary(context.Background(), input)
	if dispatchErr != nil {
		t.Fatalf("空 choices 不得在适配器报错（消费者收口边界）: %v", dispatchErr)
	}
	if exchange == nil || !exchange.UpstreamOK || exchange.UpstreamStatus != http.StatusOK {
		t.Fatalf("exchange=%+v，want UpstreamOK/200 交换", exchange)
	}
	if gatewaycodex.ExtractChatCompletionSummary(exchange.BodyText) != "" {
		t.Fatalf("空 choices 必须解析为空摘要，BodyText=%q", exchange.BodyText)
	}
	exchange.ReleaseConcurrency()
}
