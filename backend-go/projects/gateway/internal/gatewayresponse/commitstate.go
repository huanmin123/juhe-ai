package gatewayresponse

// DownstreamConnectionClosedMessage 对齐 client-abort.ts 的
// downstreamConnectionClosedMessage。
const DownstreamConnectionClosedMessage = "下游连接关闭"

// DownstreamCommitState 对齐 GatewayDownstreamCommitState：下游提交状态机，
// 决定失败时能否换号重试（canRetryUpstream）以及终态事件是否允许补发。
type DownstreamCommitState struct {
	TransportCommitted                bool
	SemanticCommitted                 bool
	SuccessfulProtocolTerminalReceived bool
	DownstreamBytesWritten            int64
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
