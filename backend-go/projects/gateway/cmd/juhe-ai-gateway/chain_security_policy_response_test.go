package main

// API Key 安全策略响应侧占位符还原的链级测试（安全策略设计 §7/§8，批C；镜像
// 批B2 chain_security_policy_test.go 文件族）：
//  - 绑定 placeholder 策略 + mock 上游流式返回含 [[PHONE_1]]：客户端收到还原
//    后真实值（单 chunk 与跨 chunk 拆分两形态）；
//  - 审计两态（§8）：upstream_response payload 含占位符原文、gateway_response
//    payload 含真实值（capturingAuditDispatcher 直收）；
//  - 类型保持：工具参数 arguments 独占叶子占位符还原为裸数字字面量（JSON 可
//    解析）；
//  - mask-only 策略与未绑定 Key：响应逐字节一致（还原器未挂载）；
//  - 非流式：完整缓冲 JSON 发送前整体还原，错误体不还原。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// seedSecurityPolicyRulesWorld 在链级夹具上种一条自定义规则集策略并可选绑定。
func seedSecurityPolicyRulesWorld(t *testing.T, fixture *chainFixture, bindKey bool, rulesJSON string) {
	t.Helper()
	now := "2026-09-04T00:00:00.000Z"
	if _, err := fixture.db.Exec(fixtureBind(`INSERT INTO security_policies (id, system_account_id, name, rules_json, built_in, created_at, updated_at)
		VALUES ('secpol_chain_phone', ?, '链级手机号占位', ?, 0, ?, ?)`),
		fixture.systemAccount, rulesJSON, now, now); err != nil {
		t.Fatalf("seed security policy: %v", err)
	}
	if bindKey {
		if _, err := fixture.db.Exec(fixtureBind(`UPDATE api_keys SET security_policy_id = 'secpol_chain_phone' WHERE id = 'key_1'`)); err != nil {
			t.Fatalf("bind security policy: %v", err)
		}
	}
}

// securityPolicySSEUpstream 是流式 mock 上游：按 parts 逐段写出并 flush（每段
// 一个网络分片），捕获上游收到的请求体。
func securityPolicySSEUpstream(t *testing.T, parts ...string) (*httptest.Server, func() string) {
	t.Helper()
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured = raw
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, part := range parts {
			_, _ = w.Write([]byte(part))
			flusher.Flush()
		}
	}))
	t.Cleanup(server.Close)
	return server, func() string { return string(captured) }
}

// securityPolicyServeChat 发一次 /v1/chat/completions（body 由调用方给，可含
// "stream":true）。
func securityPolicyServeChat(t *testing.T, chain http.Handler, fixture *chainFixture, body string) (int, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixture.apiKeySecret)
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

func securityPolicyStreamRequestBody(content string) string {
	return `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"` + content + `"}]}`
}

const securityPolicySSEDoneFrame = "data: [DONE]\n\n"

func securityPolicySSEDelta(content string) string {
	return "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + content + "\"}}]}\n\n"
}

// waitForSecurityPolicyAuditLog 轮询审计派发快照直到出现带 attempt 的日志。
func waitForSecurityPolicyAuditLog(t *testing.T, dispatcher *capturingAuditDispatcher) *gatewayusage.AuditLogInput {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		logs := dispatcher.snapshot()
		for index := range logs {
			if len(logs[index].Attempts) > 0 {
				return &logs[index]
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("审计日志未派发")
	return nil
}

func securityPolicyAuditPayload(log *gatewayusage.AuditLogInput, partType gatewayusage.AuditPayloadPartType) string {
	for _, payload := range log.Payloads {
		if payload.PartType == partType {
			return string(payload.Body)
		}
	}
	return ""
}

// TestChainSecurityPolicyRestoresStreamPlaceholderSingleChunk 验收 a：绑定
// placeholder 策略 + mock 上游单 chunk 流含 [[PHONE_1]] → 客户端收到还原后真
// 实值。
func TestChainSecurityPolicyRestoresStreamPlaceholderSingleChunk(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	seedSecurityPolicyChainWorld(t, fixture, true)
	frame := strings.Replace(securityPolicySSEDelta("CONTENT"), "CONTENT", "来电 [[PHONE_1]] 已登记", 1)
	upstream, _ := securityPolicySSEUpstream(t, frame+securityPolicySSEDoneFrame)
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.URL)
	chain, _ := composeChatDispatchChain(t, fixture)

	status, body := securityPolicyServeChat(t, chain, fixture, securityPolicyStreamRequestBody("打13812345678求助"))
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(body, "来电 13812345678 已登记") {
		t.Fatalf("客户端应收到还原后真实值：\n%s", body)
	}
	if strings.Contains(body, "[[PHONE_1]]") {
		t.Fatalf("客户端流不得残留占位符：\n%s", body)
	}
	// SSE 帧分隔完好：还原后帧仍是可解析的 data 行 + 终态帧。
	if !strings.Contains(body, securityPolicySSEDoneFrame) {
		t.Fatalf("终态帧应原样到达：\n%s", body)
	}
}

// TestChainSecurityPolicyRestoresStreamPlaceholderSplitAcrossChunks 验收 b：
// 占位符拆在两个上游分片（[[PH | ONE_1]]，悬挂缓冲跨 chunk 拼合），客户端收
// 到完整还原值且 SSE 帧分隔完好。
func TestChainSecurityPolicyRestoresStreamPlaceholderSplitAcrossChunks(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	seedSecurityPolicyChainWorld(t, fixture, true)
	part1 := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"来电 [[PH"
	part2 := "ONE_1]] 已登记\"}}]}\n\n"
	upstream, _ := securityPolicySSEUpstream(t, part1, part2, securityPolicySSEDoneFrame)
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.URL)
	chain, _ := composeChatDispatchChain(t, fixture)

	status, body := securityPolicyServeChat(t, chain, fixture, securityPolicyStreamRequestBody("打13812345678求助"))
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(body, "来电 13812345678 已登记") {
		t.Fatalf("跨 chunk 拆分应还原为完整值：\n%s", body)
	}
	if strings.Contains(body, "[[PH") {
		t.Fatalf("客户端流不得残留半截占位符：\n%s", body)
	}
	if !strings.Contains(body, securityPolicySSEDoneFrame) {
		t.Fatalf("终态帧应原样到达：\n%s", body)
	}
}

// TestChainSecurityPolicyStreamAuditTwoStates 验收 c（设计 §8 审计四态的响应
// 侧两态）：upstream_response payload 含占位符原文不含真实值；gateway_response
// payload 含真实值不含占位符。
func TestChainSecurityPolicyStreamAuditTwoStates(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	seedSecurityPolicyChainWorld(t, fixture, true)
	frame := strings.Replace(securityPolicySSEDelta("CONTENT"), "CONTENT", "来电 [[PHONE_1]] 已登记", 1)
	upstream, _ := securityPolicySSEUpstream(t, frame+securityPolicySSEDoneFrame)
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.URL)

	dispatcher := &capturingAuditDispatcher{}
	chain, _, shutdown := composeChainWithSpool(t, fixture, func(deps *chainRuntimeDeps) {
		deps.AuditLogEnabled = func() bool { return true }
		deps.AuditLogSuccessSampleRate = 1
		deps.AuditUsageDispatch = dispatcher
	})
	defer shutdown()

	status, body := securityPolicyServeChat(t, chain, fixture, securityPolicyStreamRequestBody("打13812345678求助"))
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(body, "13812345678") {
		t.Fatalf("客户端应收到还原后真实值：\n%s", body)
	}
	log := waitForSecurityPolicyAuditLog(t, dispatcher)
	upstreamBody := securityPolicyAuditPayload(log, gatewayusage.AuditPartUpstreamResponse)
	if upstreamBody == "" {
		t.Fatalf("upstream_response payload 缺失: %+v", log.Payloads)
	}
	if !strings.Contains(upstreamBody, "[[PHONE_1]]") {
		t.Fatalf("upstream_response 应保留上游原文占位符：\n%s", upstreamBody)
	}
	if strings.Contains(upstreamBody, "13812345678") {
		t.Fatalf("upstream_response 不得包含还原后真实值：\n%s", upstreamBody)
	}
	gatewayBody := securityPolicyAuditPayload(log, gatewayusage.AuditPartGatewayResponse)
	if gatewayBody == "" {
		t.Fatalf("gateway_response payload 缺失: %+v", log.Payloads)
	}
	if !strings.Contains(gatewayBody, "13812345678") {
		t.Fatalf("gateway_response 应记录还原后真实值：\n%s", gatewayBody)
	}
	if strings.Contains(gatewayBody, "[[PHONE_1]]") {
		t.Fatalf("gateway_response 不得保留占位符：\n%s", gatewayBody)
	}
}

// TestChainSecurityPolicyRestoresNumberLeafTypeFullChain 验收 d：工具参数
// arguments 独占叶子占位符（转义形态）→ 客户端收到裸数字字面量（JSON 可解
// 析）。
func TestChainSecurityPolicyRestoresNumberLeafTypeFullChain(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	seedSecurityPolicyChainWorld(t, fixture, true)
	frame := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"phone\\\":\\\"[[PHONE_1]]\\\"}\"}}]}}]}\n\n"
	upstream, _ := securityPolicySSEUpstream(t, frame+securityPolicySSEDoneFrame)
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.URL)
	chain, _ := composeChatDispatchChain(t, fixture)

	// 请求侧经 assistant 历史 tool_calls 的 number 叶子产生 number 类型映射。
	requestBody := `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"帮我查一下"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"phone\":13812345678}"}}]}]}`
	status, body := securityPolicyServeChat(t, chain, fixture, requestBody)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(body, `\"phone\":13812345678`) {
		t.Fatalf("独占叶子应还原为裸数字字面量：\n%s", body)
	}
	if strings.Contains(body, `\"phone\":\"13812345678\"`) {
		t.Fatalf("类型保持失败：数字叶子被还原为字符串：\n%s", body)
	}
	if strings.Contains(body, "[[PHONE_1]]") {
		t.Fatalf("客户端流不得残留占位符：\n%s", body)
	}
}

// TestChainSecurityPolicyMaskOnlyPassesStreamThrough 验收 e：mask-only 策略
// （无 placeholder 规则 → 无映射表）不挂还原器——上游响应逐字节透传。
func TestChainSecurityPolicyMaskOnlyPassesStreamThrough(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	seedSecurityPolicyRulesWorld(t, fixture, true, `{"rules":[{"id":"r1","type":"phone","action":"mask"}]}`)
	frame := strings.Replace(securityPolicySSEDelta("CONTENT"), "CONTENT", "来电 [[PHONE_1]] 已登记", 1)
	streamBody := frame + securityPolicySSEDoneFrame
	upstream, _ := securityPolicySSEUpstream(t, streamBody)
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.URL)
	chain, _ := composeChatDispatchChain(t, fixture)

	status, body := securityPolicyServeChat(t, chain, fixture, securityPolicyStreamRequestBody("打13812345678求助"))
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if body != streamBody {
		t.Fatalf("mask-only 应逐字节透传：\nwant=%q\ngot =%q", streamBody, body)
	}
}

// TestChainSecurityPolicyUnboundStreamByteIdentical 验收 f：未绑定 Key 响应逐
// 字节一致（还原器未挂载）。
func TestChainSecurityPolicyUnboundStreamByteIdentical(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	seedSecurityPolicyChainWorld(t, fixture, false)
	frame := strings.Replace(securityPolicySSEDelta("CONTENT"), "CONTENT", "来电 [[PHONE_1]] 已登记", 1)
	streamBody := frame + securityPolicySSEDoneFrame
	upstream, _ := securityPolicySSEUpstream(t, streamBody)
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.URL)
	chain, _ := composeChatDispatchChain(t, fixture)

	status, body := securityPolicyServeChat(t, chain, fixture, securityPolicyStreamRequestBody("打13812345678求助"))
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if body != streamBody {
		t.Fatalf("未绑定 Key 应逐字节一致：\nwant=%q\ngot =%q", streamBody, body)
	}
}

// TestChainSecurityPolicyRestoresNonStreamBodyFullChain 验收 g：非流式完整缓冲
// JSON 发送前还原（客户端与 gateway_response 审计两态同源）。
func TestChainSecurityPolicyRestoresNonStreamBodyFullChain(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	seedSecurityPolicyChainWorld(t, fixture, true)
	responseBody := `{"id":"chatcmpl-secpol","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"号码[[PHONE_1]]已记录"},"finish_reason":"stop"}]}`
	upstream, _ := securityPolicyBodyCaptureUpstream(t, responseBody)
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.URL)

	dispatcher := &capturingAuditDispatcher{}
	chain, _, shutdown := composeChainWithSpool(t, fixture, func(deps *chainRuntimeDeps) {
		deps.AuditLogEnabled = func() bool { return true }
		deps.AuditLogSuccessSampleRate = 1
		deps.AuditUsageDispatch = dispatcher
	})
	defer shutdown()

	status, body := securityPolicyServeChat(t, chain, fixture, `{"model":"gpt-test","messages":[{"role":"user","content":"打13812345678求助"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	want := strings.Replace(responseBody, "[[PHONE_1]]", "13812345678", 1)
	if body != want {
		t.Fatalf("非流式响应应发送还原后字节：\nwant=%s\ngot =%s", want, body)
	}
	log := waitForSecurityPolicyAuditLog(t, dispatcher)
	gatewayBody := securityPolicyAuditPayload(log, gatewayusage.AuditPartGatewayResponse)
	if gatewayBody == "" {
		t.Fatalf("gateway_response payload 缺失: %+v", log.Payloads)
	}
	if !strings.Contains(gatewayBody, "13812345678") || strings.Contains(gatewayBody, "[[PHONE_1]]") {
		t.Fatalf("gateway_response 应记录还原后内容：\n%s", gatewayBody)
	}
	upstreamBody := securityPolicyAuditPayload(log, gatewayusage.AuditPartUpstreamResponse)
	if upstreamBody != "" {
		if !strings.Contains(upstreamBody, "[[PHONE_1]]") {
			t.Fatalf("upstream_response 应保留上游原文占位符：\n%s", upstreamBody)
		}
		if strings.Contains(upstreamBody, "13812345678") {
			t.Fatalf("upstream_response 不得包含真实值：\n%s", upstreamBody)
		}
	}
}
