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
	RetryUpstream             bool
	RetryReason               string // StreamServerRetryReason
	SameAccountRetryEligible  bool
	ResponseInspection        *ResponseInspectionDecision
	ExcludeCurrentAccount     bool
	Message                   string
	UncommittedResponseBody   []byte
	CompatibilityRecoverySignal string

	// 完成分支
	Usage                     gatewayproto.ParsedUsage
	FirstTokenMs              *int64
	ResponseBodyText          string
	ResponseResourceId        string
	BodyOmission              *StreamBodyOmissionSummary
	ProtocolValidatedSuccess  bool
	PassthroughUpstreamFailure bool
	ErrorPayload              gatewayproto.ErrorPayload
	// ErrorPayloadExtra 保留额外键（Node 的 Record<string, unknown>）。
	ErrorPayloadExtra map[string]any
}
