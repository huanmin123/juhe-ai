package gatewayresponse

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// UpstreamResponseHandlingResult 对齐 response-handling-result.ts 的三态 union：
// Go 用扁平结构 + 标志位表达。

// UpstreamResponseHandlingResult 的三态判定：
//   - AlreadyFinalized=true → 终态已写入下游；
//   - RetryUpstream=true → 交由上层换号/换 Key 重试；
//   - 其余 → 正常完成，携带 usage / response 事实。
type UpstreamResponseHandlingResult struct {
	AlreadyFinalized bool
	// 已终态分支
	ErrorCode           string
	TransportFailure    *StreamTransportFailure
	GatewayLocalFailure bool

	// 重试分支
	RetryUpstream               bool
	RetryReason                 string // StreamServerRetryReason
	SameAccountRetryEligible    bool
	ResponseInspection          *ResponseInspectionDecision
	ExcludeCurrentAccount       bool
	Message                     string
	UncommittedResponseBody     []byte
	CompatibilityRecoverySignal string

	// FirstByteDeadlineCutover 标记非流式首字截止竞速的 configured_deadline
	// abort（速度优先切号，R5）：响应面不渲染固定 503，由 chain dispatch
	// loop 按 NormalRouteFirstByteCutoverError 契约消费——收窄到保留目标
	// 重派或耗尽退出。Node 对照：routes.ts catch 响应段的 deadline 分支凭
	// loop 作用域 reservation continue 下一候选；Go 的 engine attempt loop
	// 只覆盖 fetch 阶段，响应段由 chain 层 settleSpeedFirstCutoverError
	// 消费。
	FirstByteDeadlineCutover bool
	// CutoverReservationView 携带响应面从尝试协调器 TransferForCutover()
	// 转移出的切换预留视图（*gatewaydispatch.SpeedFirstCutoverReservationView）。
	// nil = 决策未产生预留（settle 走耗尽退出臂）。chain 侧只消费、不再转移。
	CutoverReservationView any

	// 完成分支
	Usage                      gatewayproto.ParsedUsage
	FirstTokenMs               *int64
	ResponseBodyText           string
	ResponseResourceId         string
	BodyOmission               *StreamBodyOmissionSummary
	ProtocolValidatedSuccess   bool
	PassthroughUpstreamFailure bool
	ErrorPayload               gatewayproto.ErrorPayload
	// ErrorPayloadExtra 保留额外键（Node 的 Record<string, unknown>）。
	ErrorPayloadExtra map[string]any
}
