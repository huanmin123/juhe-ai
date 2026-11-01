package gatewayresponse

// T86（深挖复审遗留项）非流式结算面钉住测试：跨协议桥接非流式响应的 usage
// 快照消费入口（HandleUpstreamResponseInput.BridgeUsage 的非流式对称面）。
//   - 桥内快照在场时是本次 attempt 的权威值，优先于结算 driver 对转换后客户端
//     协议字节的抽取（driver 按账户协议解析取不到 usage）；
//   - ok=false（本次 attempt 非桥接 / 上游未发 usage）保持既有 driver 抽取与
//     估算兜底路径；
//   - Responses 根节点失败终态（responsesFailedTerminal）不消费快照——流式
//     DD2-B1/B2 Completed 门（失败终态不消费半程快照）的非流式对称位。

import (
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

func ddT86IntPtr(value int) *int { return &value }

func ddT86BridgeSnapshot() gatewayproto.ParsedUsage {
	return gatewayproto.ParsedUsage{
		InputTokens:     ddT86IntPtr(100),
		OutputTokens:    ddT86IntPtr(50),
		CacheReadTokens: ddT86IntPtr(30),
	}
}

func ddT86BridgeSupplier() func() (gatewayproto.ParsedUsage, bool) {
	return func() (gatewayproto.ParsedUsage, bool) { return ddT86BridgeSnapshot(), true }
}

// TestDDT86NonStreamBridgeUsageSnapshotConsumed 钉住消费主路径：客户端协议形
// JSON（结算 driver 按账户协议解析取不到 usage）+ 桥内快照在场 → 结算 usage
// 等于快照（input/output/cache 三族），观察到的上游模型照常回填。
func TestDDT86NonStreamBridgeUsageSnapshotConsumed(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`
	input, _ := newInputFixture(NewSliceUpstreamBody([]byte(body)), 200, map[string]string{"Content-Type": "application/json"})
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}
	input.UpstreamResponse.UpstreamResponseModel = "up-model-x"
	input.BridgeUsage = ddT86BridgeSupplier()
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if gatewayproto.Token(result.Usage.InputTokens) != 100 || gatewayproto.Token(result.Usage.OutputTokens) != 50 ||
		gatewayproto.Token(result.Usage.CacheReadTokens) != 30 {
		t.Fatalf("usage = %+v, want 快照 100/50/30", result.Usage)
	}
	if result.Usage.UpstreamResponseModel != "up-model-x" {
		t.Fatalf("UpstreamResponseModel = %q, want up-model-x", result.Usage.UpstreamResponseModel)
	}
}

// TestDDT86NonStreamBridgeUsageAbsentKeepsDriverPath 钉住 ok=false 语义：本次
// attempt 非桥接 / 上游未发 usage 时保持既有 driver 抽取路径（无 usage 的正文
// → 空 usage，估算兜底在记账面照常触发，不因供给口存在而改变）。
func TestDDT86NonStreamBridgeUsageAbsentKeepsDriverPath(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`
	input, _ := newInputFixture(NewSliceUpstreamBody([]byte(body)), 200, map[string]string{"Content-Type": "application/json"})
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}
	input.BridgeUsage = func() (gatewayproto.ParsedUsage, bool) { return gatewayproto.ParsedUsage{}, false }
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Usage.InputTokens != nil || result.Usage.OutputTokens != nil {
		t.Fatalf("ok=false 时不得产生 usage: %+v", result.Usage)
	}
	// 未接线（nil supplier，既有非桥接路径）行为一致。
	input2, _ := newInputFixture(NewSliceUpstreamBody([]byte(body)), 200, map[string]string{"Content-Type": "application/json"})
	input2.Deps = input.Deps
	result2, err := HandleNonStreamUpstreamResponse(input2)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result2.Usage.InputTokens != nil || result2.Usage.OutputTokens != nil {
		t.Fatalf("未接线时不得产生 usage: %+v", result2.Usage)
	}
}

// TestDDT86NonStreamBridgeUsageBlockedOnResponsesFailedTerminal 钉住失败终态
// 不落快照记账：Responses 根节点 status=failed 的 2xx 非流式响应按协议失败终
// 局收尾（协议校验在 usage 结算臂之前拦截），桥内快照不得进入该终局的记账
// usage——失败终态记账不按半程 usage 口径（流式 DD2-B1/B2 Completed 门的非
// 流式对称位；结算臂内的 !responsesFailedTerminal 门是同哲学的对称守卫，当前
// 面内根 failed 均被协议校验先行拦截，门为拦截顺序变化时的承载位）。
func TestDDT86NonStreamBridgeUsageBlockedOnResponsesFailedTerminal(t *testing.T) {
	body := `{"object":"response","id":"r1","status":"failed","output":[],"error":{"code":"x","message":"resp failed"}}`
	input, _ := newInputFixture(NewSliceUpstreamBody([]byte(body)), 200, map[string]string{"Content-Type": "application/json"})
	input.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/responses", nil))
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}
	input.BridgeUsage = ddT86BridgeSupplier()
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.AlreadyFinalized || result.ErrorCode != "upstream_protocol_failure" {
		t.Fatalf("result = %+v, want 失败终态裁决（AlreadyFinalized + upstream_protocol_failure）", result)
	}
	if result.Usage.InputTokens != nil || result.Usage.OutputTokens != nil || result.Usage.CacheReadTokens != nil {
		t.Fatalf("失败终态不得携带桥内快照 usage: %+v", result.Usage)
	}
}
