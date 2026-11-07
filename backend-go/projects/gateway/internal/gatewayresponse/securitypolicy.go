package gatewayresponse

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysecpol"
)

// 响应侧占位符还原挂载面（APIKey 安全策略与出站内容脱敏设计 §7，批C）。
//
// 挂载点经既有 TransformUpstreamChunk / FlushTransformedUpstreamChunks 管道通
// 道（stream.go 选项 + pipe.go:229-231 / pipefinal.go eofFlushPhase 既有装配
// 位，生产此前未装配）：
//   - TransformUpstreamChunk 位于 upstreamCapture.Push（upstream_response 审计
//     原文，§8）之后、写下游之前——还原后字节经 writeDownstreamChunk 写出并被
//     captureDownstreamChunk 捕获进 gateway_response 审计，四态如实记录；
//   - Flush 在上游 EOF 段冲刷悬挂缓冲残留（§7.2 第 4 条），冲刷字节走与主循
//     环完全相同的 interceptor → 预提交 → writeDownstreamChunk 路径（含审计
//     捕获），不新增直写点。
//
// 已知边界（设计 §7.1/§7.3，一期接受）：字节级还原器不区分事件类型，上游错
// 误帧中若出现映射表内 token 同样会被替换（上游未见过真实值，错误帧含映射
// token 的概率仅限模型回显占位符）；终态前的失败/拦截路径不冲刷悬挂缓冲
// （≤ maxHold 字节随失败流终止丢弃）。

// securityPolicyPlaceholderMapping 取请求的 placeholder 映射表（响应侧还原消
// 费）。仅当请求绑定安全策略且发生过 placeholder 命中时非 nil（出站脱敏点构
// 造，见 gatewaypreauth.SecurityPolicyRequestState.Mapping：纯 mask、未命中与
// 未绑定 Key 恒 nil）——还原器不装配，零响应侧成本（§7.1）。
func securityPolicyPlaceholderMapping(req *gatewaypreauth.GatewayRequest) *gatewaysecpol.Mapping {
	if req == nil || req.SecurityPolicy == nil || req.SecurityPolicy.Mapping == nil || req.SecurityPolicy.Mapping.Empty() {
		return nil
	}
	return req.SecurityPolicy.Mapping
}
