package gatewaycallertier

import "context"

// SignalRecorder 是请求链接线点依赖的最小透传面（调用方分级调度设计 §3/§5.4
// 适配边界）：接线点所在包（cmd 链编排 / gatewaypreauth / gatewaybody）不
// 理解分级语义，只把「已认证 API Key ID + 本地高置信事实」透传给影子记录
// 器，调用一律 best-effort——方法不向请求路径传播错误（设计 §6.2，分级是
// 辅助调度不是事实门槛）。
//
// *Recorder 是唯一生产实现；nil 接口值 = 未装配（组合测试），接线点静默
// 跳过（对齐网关链可选端口的既有缺席语义）。
type SignalRecorder interface {
	// RecordRequest 计入请求分母（认证成功后每请求恰一次）。
	RecordRequest(ctx context.Context, apiKeyID string)
	// RecordSignal 计入一次高置信本地信号。
	RecordSignal(ctx context.Context, apiKeyID string, kind SignalKind)
}

// *Recorder 恒满足接线面（编译期锁定）。
var _ SignalRecorder = (*Recorder)(nil)
