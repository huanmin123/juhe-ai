package gatewayresponse

// 响应侧占位符还原（APIKey 安全策略与出站内容脱敏设计 §7，批C）的管道级集成
// 测试：HandleStreamUpstreamResponse / HandleNonStreamUpstreamResponse 实际装
// 配还原器后的客户端可见行为与审计两态（§8：upstream_response = 上游原文、
// gateway_response = 还原后字节）。
// 链级（preauth → dispatch → response 全链）形态见 cmd/juhe-ai-gateway
// chain_security_policy_response_test.go（镜像批B2 TestChainSecurityPolicy*
// 测试文件族）。

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysecpol"
)

const securityPolicyPhonePlaceholderRulesJSON = `{"rules":[{"id":"r1","type":"phone","action":"placeholder"}]}`

const securityPolicySSEDeltaFrame = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"CONTENT\"}}]}\n\n"
const securityPolicySSEDoneFrame = "data: [DONE]\n\n"

// securityPolicyPhoneMapping 用与出站脱敏点同源的 SanitizeBody 构造映射表：
// [[PHONE_1]] → 13812345678（string 叶子）。
func securityPolicyPhoneMapping(t *testing.T) *gatewaysecpol.Mapping {
	t.Helper()
	policy, err := gatewaysecpol.Compile(securityPolicyPhonePlaceholderRulesJSON)
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	result := gatewaysecpol.SanitizeBody(policy, []byte(`{"messages":[{"role":"user","content":"打13812345678求助"}]}`), gatewaysecpol.FamilyOpenAIChat)
	if result.Mapping == nil || result.Mapping.Empty() {
		t.Fatalf("映射表应非空: %+v", result)
	}
	return result.Mapping
}

// securityPolicyNumberLeafMapping 用 tool_calls arguments 内层 JSON number 叶
// 子构造映射表：[[PHONE_1]] → 13812345678（isNumber=true，§7.1 类型保持）。
func securityPolicyNumberLeafMapping(t *testing.T) *gatewaysecpol.Mapping {
	t.Helper()
	policy, err := gatewaysecpol.Compile(securityPolicyPhonePlaceholderRulesJSON)
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	result := gatewaysecpol.SanitizeBody(policy,
		[]byte(`{"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"f","arguments":"{\"phone\":13812345678}"}}]}]}`),
		gatewaysecpol.FamilyOpenAIChat)
	if result.Mapping == nil || result.Mapping.Empty() {
		t.Fatalf("映射表应非空: %+v", result)
	}
	return result.Mapping
}

func attachSecurityPolicyMapping(input HandleUpstreamResponseInput, mapping *gatewaysecpol.Mapping) HandleUpstreamResponseInput {
	input.Req.SecurityPolicy = &gatewaypreauth.SecurityPolicyRequestState{PolicyID: "secpol-c", Mapping: mapping}
	return input
}

func securityPolicyStreamDeps() *FinalizationDeps {
	return &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}
}

// assertUpstreamAuditKeepsPlaceholder 断言 attempt 审计（upstream_response 态，
// finalize.go CompleteAttempt 的 ResponseBody = AuditUpstreamBody）只含上游原
// 文、不含还原后真实值（§8 两态硬约束）。
func assertUpstreamAuditKeepsPlaceholder(t *testing.T, audit *mockAuditCapture) {
	t.Helper()
	if len(audit.completed) == 0 {
		t.Fatalf("attempt 审计未完成")
	}
	for _, attempt := range audit.completed {
		body := string(attempt.ResponseBody)
		if !strings.Contains(body, "[[PHONE_1]]") {
			t.Fatalf("upstream_response 审计必须保留上游原文占位符: %q", body)
		}
		if strings.Contains(body, "13812345678") {
			t.Fatalf("upstream_response 审计不得包含还原后真实值: %q", body)
		}
	}
}

// TestSecurityPolicyStreamRestoresPlaceholderSingleChunk 验收 a/c：单 chunk 上
// 游流含占位符 → 客户端收到还原后真实值；字节级还原不破坏 SSE 帧分隔（整流
// 逐字节等于原帧替换形态）；upstream_response 审计保留原文。
func TestSecurityPolicyStreamRestoresPlaceholderSingleChunk(t *testing.T) {
	frame := strings.Replace(securityPolicySSEDeltaFrame, "CONTENT", "来电 [[PHONE_1]] 已登记", 1)
	upstream := frame + securityPolicySSEDoneFrame
	want := strings.Replace(upstream, "[[PHONE_1]]", "13812345678", 1)

	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(upstream)), 200, map[string]string{"Content-Type": "text/event-stream"})
	input.Deps = securityPolicyStreamDeps()
	audit := newMockAuditCapture()
	input.AuditCapture = audit
	input = attachSecurityPolicyMapping(input, securityPolicyPhoneMapping(t))

	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.AlreadyFinalized {
		t.Fatalf("成功流不应走失败终态: %+v", result)
	}
	if got := recorder.Body.String(); got != want {
		t.Fatalf("客户端流应逐字节等于还原后形态：\nwant=%q\ngot =%q", want, got)
	}
	if !strings.Contains(recorder.Body.String(), "13812345678") {
		t.Fatalf("客户端应收到还原后真实值: %q", recorder.Body.String())
	}
	assertUpstreamAuditKeepsPlaceholder(t, audit)
}

// TestSecurityPolicyStreamRestoresPlaceholderSplitAcrossChunks 验收 b（跨
// chunk 拆分）：占位符拆在两个上游分片（悬挂缓冲跨 chunk 拼合），客户端收到
// 完整还原值且 SSE 帧分隔完好。
func TestSecurityPolicyStreamRestoresPlaceholderSplitAcrossChunks(t *testing.T) {
	part1 := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"来电 [[PH"
	part2 := "ONE_1]] 已登记\"}}]}\n\n"
	upstream := part1 + part2 + securityPolicySSEDoneFrame
	want := strings.Replace(upstream, "[[PHONE_1]]", "13812345678", 1)

	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(part1), []byte(part2), []byte(securityPolicySSEDoneFrame)), 200, map[string]string{"Content-Type": "text/event-stream"})
	input.Deps = securityPolicyStreamDeps()
	audit := newMockAuditCapture()
	input.AuditCapture = audit
	input = attachSecurityPolicyMapping(input, securityPolicyPhoneMapping(t))

	if _, err := HandleStreamUpstreamResponse(input); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := recorder.Body.String(); got != want {
		t.Fatalf("跨 chunk 拆分应还原为完整值且帧分隔完好：\nwant=%q\ngot =%q", want, got)
	}
	assertUpstreamAuditKeepsPlaceholder(t, audit)
}

// TestSecurityPolicyStreamRestoresPlaceholderSplitAcrossSseEvents T174：占位符
// 两半分属两个 SSE 事件时，事件级缓存扣住前一事件，在下一事件补齐后把前半截
// 替换为真实值、删除后半截占位符；客户端按事件顺序拼接后得到完整真实值，帧
// 结构保持合法。
func TestSecurityPolicyStreamRestoresPlaceholderSplitAcrossSseEvents(t *testing.T) {
	event1 := strings.Replace(securityPolicySSEDeltaFrame, "CONTENT", "来电 [[PH", 1)
	event2 := strings.Replace(securityPolicySSEDeltaFrame, "CONTENT", "ONE_1]] 已登记", 1)
	event1Want := strings.Replace(event1, "[[PH", "13812345678", 1)
	event2Want := strings.Replace(event2, "ONE_1]]", "", 1)
	want := event1Want + event2Want + securityPolicySSEDoneFrame

	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(event1), []byte(event2), []byte(securityPolicySSEDoneFrame)), 200, map[string]string{"Content-Type": "text/event-stream"})
	input.Deps = securityPolicyStreamDeps()
	input = attachSecurityPolicyMapping(input, securityPolicyPhoneMapping(t))

	if _, err := HandleStreamUpstreamResponse(input); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := recorder.Body.String(); got != want {
		t.Fatalf("跨 SSE 事件拆分应还原为完整值且帧结构合法：\nwant=%q\ngot =%q", want, got)
	}
	if strings.Contains(recorder.Body.String(), "[[PH") || strings.Contains(recorder.Body.String(), "ONE_1]]") {
		t.Fatalf("客户端流不得残留占位符残片: %q", recorder.Body.String())
	}
}

// TestSecurityPolicyStreamRestoresPlaceholderSplitAcrossThreeSseEvents：占位符
// 可被上游拆成多于两个事件；事件级缓存按 token 剩余片段逐段续接，仍应在写出
// 前还原，且不缓存整条响应。
func TestSecurityPolicyStreamRestoresPlaceholderSplitAcrossThreeSseEvents(t *testing.T) {
	event1 := strings.Replace(securityPolicySSEDeltaFrame, "CONTENT", "来电 [[PH", 1)
	event2 := strings.Replace(securityPolicySSEDeltaFrame, "CONTENT", "ONE", 1)
	event3 := strings.Replace(securityPolicySSEDeltaFrame, "CONTENT", "_1]] 已登记", 1)
	event1Want := strings.Replace(event1, "[[PH", "13812345678", 1)
	event2Want := strings.Replace(event2, `"content":"ONE"`, `"content":""`, 1)
	event3Want := strings.Replace(event3, "_1]]", "", 1)
	want := event1Want + event2Want + event3Want + securityPolicySSEDoneFrame

	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(event1), []byte(event2), []byte(event3), []byte(securityPolicySSEDoneFrame)), 200, map[string]string{"Content-Type": "text/event-stream"})
	input.Deps = securityPolicyStreamDeps()
	input = attachSecurityPolicyMapping(input, securityPolicyPhoneMapping(t))

	if _, err := HandleStreamUpstreamResponse(input); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := recorder.Body.String(); got != want {
		t.Fatalf("三事件拆分应还原为完整值：\nwant=%q\ngot =%q", want, got)
	}
}

// TestSecurityPolicyStreamSplitAcrossSseEventsWithoutCompletionPassthrough：
// 下一事件没有补齐 token 时，扣住事件必须原样放行（保持旧透传边界，不丢帧）。
func TestSecurityPolicyStreamSplitAcrossSseEventsWithoutCompletionPassthrough(t *testing.T) {
	event1 := strings.Replace(securityPolicySSEDeltaFrame, "CONTENT", "来电 [[PH", 1)
	event2 := strings.Replace(securityPolicySSEDeltaFrame, "CONTENT", "已登记", 1)
	upstream := event1 + event2 + securityPolicySSEDoneFrame

	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(event1), []byte(event2), []byte(securityPolicySSEDoneFrame)), 200, map[string]string{"Content-Type": "text/event-stream"})
	input.Deps = securityPolicyStreamDeps()
	input = attachSecurityPolicyMapping(input, securityPolicyPhoneMapping(t))

	if _, err := HandleStreamUpstreamResponse(input); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := recorder.Body.String(); got != upstream {
		t.Fatalf("未补齐时应逐字节透传：\nwant=%q\ngot =%q", upstream, got)
	}
}

// TestSecurityPolicyStreamNumberLeafTypePreservation 验收 d：工具参数
// arguments 独占叶子占位符（转义形态 \"TOKEN\"，SSE 真实字节形态）→ 客户端
// 收到裸数字字面量，arguments 解析后为合法 JSON。
func TestSecurityPolicyStreamNumberLeafTypePreservation(t *testing.T) {
	frame := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"phone\\\":\\\"[[PHONE_1]]\\\"}\"}}]}}]}\n\n"
	upstream := frame + securityPolicySSEDoneFrame

	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(upstream)), 200, map[string]string{"Content-Type": "text/event-stream"})
	input.Deps = securityPolicyStreamDeps()
	input = attachSecurityPolicyMapping(input, securityPolicyNumberLeafMapping(t))

	if _, err := HandleStreamUpstreamResponse(input); err != nil {
		t.Fatalf("err = %v", err)
	}
	got := recorder.Body.String()
	// SSE 帧内 arguments 是外层 JSON 字符串：还原后的裸数字字面量在内层 JSON
	// 中不带引号（外层字节为 \"phone\":13812345678），客户端解析外层帧后
	// arguments 即为 {"phone":13812345678}（JSON 可解析）。
	if !strings.Contains(got, `\"phone\":13812345678`) {
		t.Fatalf("独占叶子应还原为裸数字字面量: %q", got)
	}
	if strings.Contains(got, "[[PHONE_1]]") {
		t.Fatalf("客户端流不得残留占位符: %q", got)
	}
	if strings.Contains(got, `\"phone\":\"13812345678\"`) {
		t.Fatalf("类型保持失败：数字叶子被还原为字符串: %q", got)
	}
}

// TestSecurityPolicyMaskOnlyLeavesStreamByteIdentical 验收 e：mask-only 策略
// （Mapping 为 nil）不挂还原器——上游响应逐字节透传，占位符字面量原样到达客
// 户端。
func TestSecurityPolicyMaskOnlyLeavesStreamByteIdentical(t *testing.T) {
	upstream := securityPolicySSEDeltaFrame + securityPolicySSEDoneFrame
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(upstream)), 200, map[string]string{"Content-Type": "text/event-stream"})
	input.Deps = securityPolicyStreamDeps()
	input.Req.SecurityPolicy = &gatewaypreauth.SecurityPolicyRequestState{PolicyID: "secpol-mask", Family: "openai_chat"}

	if _, err := HandleStreamUpstreamResponse(input); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := recorder.Body.String(); got != upstream {
		t.Fatalf("mask-only 应逐字节透传：\nwant=%q\ngot =%q", upstream, got)
	}
}

// TestSecurityPolicyUnboundStreamByteIdentical 验收 f：未绑定 Key（无执行事
// 实）响应逐字节一致。
func TestSecurityPolicyUnboundStreamByteIdentical(t *testing.T) {
	upstream := securityPolicySSEDeltaFrame + securityPolicySSEDoneFrame
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(upstream)), 200, map[string]string{"Content-Type": "text/event-stream"})
	input.Deps = securityPolicyStreamDeps()

	if _, err := HandleStreamUpstreamResponse(input); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := recorder.Body.String(); got != upstream {
		t.Fatalf("未绑定 Key 应逐字节一致：\nwant=%q\ngot =%q", upstream, got)
	}
}

// TestSecurityPolicyNonStreamRestoresBodyAndAudit 验收 g（非流式 a/c）：完整
// 缓冲 JSON 成功响应发送前还原；审计两态（§8）——CompleteAttempt 的
// ResponseBody（upstream_response 态）消费还原前上游原文，Finalize 的
// ResponseBody（gateway_response 态）与客户端所见同源消费还原后字节。
func TestSecurityPolicyNonStreamRestoresBodyAndAudit(t *testing.T) {
	body := `{"id":"x","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"号码[[PHONE_1]]已记录"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	want := strings.Replace(body, "[[PHONE_1]]", "13812345678", 1)

	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(body)), 200, map[string]string{"Content-Type": "application/json"})
	input.Deps = securityPolicyStreamDeps()
	audit := newMockAuditCapture()
	input.AuditCapture = audit
	input = attachSecurityPolicyMapping(input, securityPolicyPhoneMapping(t))

	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.AlreadyFinalized {
		t.Fatalf("成功响应不应走失败终态: %+v", result)
	}
	if got := recorder.Body.String(); got != want {
		t.Fatalf("非流式响应应发送还原后字节：\nwant=%s\ngot =%s", want, got)
	}
	// Finalize 属 chain 层在 Handle 返回后的终态调用，包内显式补调以断言
	// gateway_response 态（cmd 链级测试 TestChainSecurityPolicyRestoresNonStreamBodyFullChain
	// 另有全链覆盖）。
	FinalizeHandledUpstreamResponse(input, result)
	if len(audit.completed) == 0 {
		t.Fatalf("attempt 审计未完成")
	}
	last := audit.completed[len(audit.completed)-1]
	if string(last.ResponseBody) != body {
		t.Fatalf("upstream_response 审计应记录上游原文：\nwant=%s\ngot =%s", body, string(last.ResponseBody))
	}
	if len(audit.finalized) == 0 {
		t.Fatalf("gateway_response 审计未完成")
	}
	fin := audit.finalized[len(audit.finalized)-1]
	if fin.ResponseBody != want {
		t.Fatalf("gateway_response 审计应记录还原后字节：\nwant=%s\ngot =%s", want, fin.ResponseBody)
	}
}

// TestSecurityPolicyNonStreamErrorBodyNotRestored 设计 §7.1：上游错误体不还
// 原、原样透传。
func TestSecurityPolicyNonStreamErrorBodyNotRestored(t *testing.T) {
	body := `{"error":{"message":"quota [[PHONE_1]] exceeded","type":"invalid_request_error","code":"rate_limit_exceeded"}}`

	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(body)), 429, map[string]string{"Content-Type": "application/json"})
	input.Deps = securityPolicyStreamDeps()
	input = attachSecurityPolicyMapping(input, securityPolicyPhoneMapping(t))

	if _, err := HandleNonStreamUpstreamResponse(input); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := recorder.Body.String(); got != body {
		t.Fatalf("错误体必须原样透传不还原：\nwant=%s\ngot =%s", body, got)
	}
}
