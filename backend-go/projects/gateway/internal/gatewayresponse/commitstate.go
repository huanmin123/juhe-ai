package gatewayresponse

// DownstreamConnectionClosedMessage 对齐 client-abort.ts 的
// downstreamConnectionClosedMessage。
const DownstreamConnectionClosedMessage = "下游连接关闭"

// ClientCanceledErrorCode 是客户端断开后 drain 收口写入 usage 的稳定错误码
// （2026-11-12 计费漏记修复）：区分于上游流错误，失败行仍带实际/估算 usage。
const ClientCanceledErrorCode = "client_canceled"

// UpstreamBodyInterruptedErrorCode 是非流式上游正文中途中断的稳定错误码
// （2026-11-12 补行修复）：此前该形态在链面兜底不写 usage。
const UpstreamBodyInterruptedErrorCode = "upstream_body_interrupted"

// FailureAttributionDownstreamClosed 与 gatewayusage.FailureAttributionDownstreamClosed
// 同值：响应层不依赖 usage 包，字符串在此单点定义并由 chain 原样透传。
const FailureAttributionDownstreamClosed = "downstream_closed"

// ClientGoneDrainBudgetMs 是客户端断开后继续读取上游的默认预算：预算内读到
// 协议终态按实际 usage 结算；预算耗尽按已观测内容估算后收口。预算存在的目的
// 是避免上游长流无限占用账号并发槽（上游请求不再随客户端取消）。
const ClientGoneDrainBudgetMs int64 = 30_000

// DownstreamCommitState 对齐 GatewayDownstreamCommitState：下游提交状态机，
// 决定失败时能否换号重试（canRetryUpstream）以及终态事件是否允许补发。
type DownstreamCommitState struct {
	TransportCommitted                 bool
	SemanticCommitted                  bool
	SuccessfulProtocolTerminalReceived bool
	DownstreamBytesWritten             int64
}

// MarkTransportCommitted 对齐 markTransportCommitted。
func (s *DownstreamCommitState) MarkTransportCommitted(bytesWritten int64) {
	s.TransportCommitted = true
	s.DownstreamBytesWritten += normalizedBytes(bytesWritten)
}

// MarkSemanticCommitted 对齐 markSemanticCommitted。
func (s *DownstreamCommitState) MarkSemanticCommitted(bytesWritten int64) {
	s.TransportCommitted = true
	s.SemanticCommitted = true
	s.DownstreamBytesWritten += normalizedBytes(bytesWritten)
}

// MarkSuccessfulProtocolTerminalReceived 对齐 markSuccessfulProtocolTerminalReceived。
func (s *DownstreamCommitState) MarkSuccessfulProtocolTerminalReceived() {
	s.SuccessfulProtocolTerminalReceived = true
}

// CanRetryUpstream 对齐 canRetryUpstream。
func (s *DownstreamCommitState) CanRetryUpstream() bool { return !s.SemanticCommitted }

func normalizedBytes(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
