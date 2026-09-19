package main

// 切号冻结捕获（SwitchTarget）：本请求第一个账户候选完成 mapping 解析与
// 上游请求构造、尚未首次真实发送前，冻结该账户的有效上游目标并存入请求级
// 载体（每请求一次，不随后续尝试重置）。契约：
// docs/functions/切号时有效上游目标与上下文迁移设计.md §3.1。
//
// 冻结输入与构造同源：ClientRequestedModel / ClientSourceEndpointFamily 记录
// 构造侧 mapping 解析实际使用的值（requestMappingSourceFamilyOf 词表），消费
// 点过滤输入一律取自冻结快照，保证同一请求内冻结与过滤用同一族词表。

import (
	"context"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// BuildGatewayUpstreamRequestParts 构造上游请求部件，并在构造成功完成后
// 冻结本请求的 SwitchTarget（设计文档 §3.1：冻结发生在"能完整描述实际上游
// 请求"之后；构造前的失败——URL 构建错误、候选门拒绝——不触发冻结）。
// 唯一允许冻结的调用方是主请求账户派发（engine dispatchSingleAccount）；内部
// 合成 / 辅助 / 打分类派发必须经 WithoutSwitchTargetCapture 剥离载体。
func (d *chainProviderDriver) BuildGatewayUpstreamRequestParts(
	ctx context.Context,
	req *gatewaypreauth.GatewayRequest,
	account gatewaydispatch.AccountCandidate,
	identity gatewaydispatch.UsageIdentity,
	requestClientCompatibility string,
) (gatewaydispatch.PreparedRequestParts, error) {
	parts, err := d.buildGatewayUpstreamRequestParts(ctx, req, account, identity, requestClientCompatibility)
	if err == nil {
		d.freezeSwitchTargetForRequest(ctx, req, account, requestClientCompatibility)
	}
	return parts, err
}

// freezeSwitchTargetForRequest 把刚完成的构造结果冻结进请求级载体；ctx 未注入
// 载体或已被剥离（探针、混合打分等内部辅助派发）时不参与。
func (d *chainProviderDriver) freezeSwitchTargetForRequest(
	ctx context.Context,
	req *gatewaypreauth.GatewayRequest,
	account gatewaydispatch.AccountCandidate,
	requestClientCompatibility string,
) {
	capture := gatewaydispatch.SwitchTargetCaptureFromContext(ctx)
	if capture == nil || req == nil {
		return
	}
	requestedModel := ""
	if model, ok := gatewaypreauth.RequestModel(req); ok {
		requestedModel = strings.TrimSpace(model)
	}
	capture.Freeze(gatewaydispatch.SwitchTargetFreezeInput{
		SourceAccountID: account.ID,
		Target:          d.switchTargetForRequest(req, account, requestClientCompatibility),
		// 与构造侧 d.resolveAccountModelMapping 完全同源的 LHS 依据。
		ClientRequestedModel:       requestedModel,
		ClientSourceEndpointFamily: requestMappingSourceFamilyOf(req),
	})
}

// switchTargetForRequest 依据刚完成的构造步骤推导冻结目标：有 mapping 时取
// RHS（mapping.UpstreamModel / mapping.UpstreamEndpointFamily）与该 (family,
// 流式) 对应的精确 mode；无 mapping（原生）时取 (客户端模型, 构造侧客户端
// 协议族, 客户端精确 mode)。协议族一律用构造侧 requestMappingSourceFamilyOf
// 词表（与 mapping 解析同源；恒非空，countTokens 等未闸门形态不再产生空
// family 误诊）。精确 mode 复用 requiredSupportedEndpointMode（覆盖 D-99
// codex_responses 强制 SSE 与跨协议桥的 mode 重映射），缺省回落请求形状 mode。
func (d *chainProviderDriver) switchTargetForRequest(
	req *gatewaypreauth.GatewayRequest,
	account gatewaydispatch.AccountCandidate,
	requestClientCompatibility string,
) *gatewaydispatch.SwitchTarget {
	model := ""
	if requested, ok := gatewaypreauth.RequestModel(req); ok {
		model = strings.TrimSpace(requested)
	}
	family := requestMappingSourceFamilyOf(req)
	mode := gatewaypreauth.RequestSupportedEndpointMode(req)
	if requiredMode, required := d.requiredSupportedEndpointMode(req, account, requestClientCompatibility); required && requiredMode != "" {
		mode = requiredMode
	}
	if mapping := d.resolveAccountModelMapping(account, req, requestClientCompatibility); mapping != nil {
		model = strings.TrimSpace(mapping.UpstreamModel)
		family = mapping.UpstreamEndpointFamily
	}
	return &gatewaydispatch.SwitchTarget{
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		UpstreamModel:             model,
		UpstreamEndpointFamily:    family,
		UpstreamEndpointMode:      mode,
	}
}
