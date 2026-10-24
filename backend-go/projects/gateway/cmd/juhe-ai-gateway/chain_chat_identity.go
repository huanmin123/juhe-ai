package main

// AI 问答内部身份通道（AI 问答免 Key 直调内部通道计划 §3.1，W-A 批）。
//
// 通道形态：chat.GenerationExecutor 的生产实现 chatGatewayExecutor 在派发前
// 经 contextWithChatInternalIdentity 携带会话归属系统账户，Dispatch 驱动的
// /v1 请求在 handleOpenAIGatewayRequest 把该身份落到
// GatewayRequest.InternalOwnerSystemAccountID 字段——preauth 据此跳过 API Key
// 鉴权，SuppressUsage 随请求置位，最终在持久化闸口
// （FinalizationDispatch.DispatchUsageRecord 入口，两条投递臂都覆盖）丢弃全部
// 使用日志。
//
// 安全边界：context 值只由进程内 chatGatewayExecutor.Dispatch 注入（key 为本
// 包未导出结构体类型）；net/http 服务端构造的外部请求 context 不可能携带该
// 未导出 key，因此不存在外部伪造通道。该身份最终只以
// GatewayRequest.InternalOwnerSystemAccountID 字段形式进入鉴权链，不经任何
// header / query / body 承载。

import "context"

// chatInternalIdentityContextKey 是内部身份的 context key。未导出结构体类型
// 保证包外无法构造同名 key，外部 HTTP 请求无法注入内部身份（与
// chatDispatchTargetContextKey 同构）。
type chatInternalIdentityContextKey struct{}

// chatInternalIdentity 是一次内部通道请求的归属身份：仅携带归属系统账户 ID。
type chatInternalIdentity struct {
	OwnerSystemAccountID string
}

// contextWithChatInternalIdentity 把内部身份挂到 context 上（进程内执行器
// Dispatch 是唯一生产注入点）。
func contextWithChatInternalIdentity(ctx context.Context, identity chatInternalIdentity) context.Context {
	return context.WithValue(ctx, chatInternalIdentityContextKey{}, identity)
}

// chatInternalIdentityFromContext 读取内部身份；ctx 为 nil、未携带（或 key 上
// 类型不符）、OwnerSystemAccountID 为空串时返回 ok=false（外部请求同一路径）。
func chatInternalIdentityFromContext(ctx context.Context) (chatInternalIdentity, bool) {
	if ctx == nil {
		return chatInternalIdentity{}, false
	}
	identity, ok := ctx.Value(chatInternalIdentityContextKey{}).(chatInternalIdentity)
	if !ok || identity.OwnerSystemAccountID == "" {
		return chatInternalIdentity{}, false
	}
	return identity, true
}
