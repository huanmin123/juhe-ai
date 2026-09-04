package gatewayresponse

// TimeoutProfile 对齐 GatewayTimeoutProfile（policy/timeout-profile.ts）中被
// response 层消费的字段。
type TimeoutProfile struct {
	FirstResponseTimeoutMs          int64
	IdleTimeoutMs                   int64
	UncommittedAttemptMaxLifetimeMs int64
	TimeoutsDisabled                bool
}

// StreamReadPlan 对齐 StreamReadPlan。
type StreamReadPlan struct {
	Phase                   string // 'first_chunk' | 'active_stream'
	TimeoutMs               int64
	RawTimeoutMs            *int64
	SemanticResultTimeoutMs *int64
	StreamLifetimeTimeoutMs *int64
	TimeoutKind             string // 'first_chunk' | 'upstream_activity' | 'semantic_result' | 'stream_lifetime'
	TimeoutMessage          string
	DeadlineExceeded        bool
}

// StreamReadPlanStatus 对齐 buildStreamReadPlan 的 status 入参。
type StreamReadPlanStatus struct {
	WaitingForFirstChunk   bool
	LastUpstreamActivityAt int64
	LastSseEventActivityAt int64 // 0 表示 undefined
	UpstreamChunkReceived  bool
	SemanticResultReceived bool
	PendingProtocolEvent   bool
	ParserSkipped          bool
}

// BuildGatewayStreamReadPlan 对齐 buildGatewayStreamReadPlan：timeoutsDisabled
// 时返回 nil（无 read plan）。
func BuildGatewayStreamReadPlan(profile TimeoutProfile, startedAt int64, status StreamReadPlanStatus, now int64) *StreamReadPlan {
	if profile.TimeoutsDisabled {
		return nil
	}
	return buildStreamReadPlan(profile, startedAt, status, now)
}

func buildStreamReadPlan(profile TimeoutProfile, startedAt int64, status StreamReadPlanStatus, now int64) *StreamReadPlan {
	var streamLifetimeTimeoutMs *int64
	if !status.SemanticResultReceived {
		value := profile.UncommittedAttemptMaxLifetimeMs - (now - startedAt)
		streamLifetimeTimeoutMs = &value
	}

	if !status.WaitingForFirstChunk || status.UpstreamChunkReceived {
		rawTimeoutMs := profile.IdleTimeoutMs - (now - status.LastUpstreamActivityAt)
		semanticResultStartedAt := startedAt
		if status.LastSseEventActivityAt != 0 {
			semanticResultStartedAt = status.LastSseEventActivityAt
		}
		semanticResultTimeoutMs := profile.FirstResponseTimeoutMs - (now - semanticResultStartedAt)
		if !status.SemanticResultReceived && !status.PendingProtocolEvent && !status.ParserSkipped &&
			semanticResultTimeoutMs <= rawTimeoutMs &&
			(streamLifetimeTimeoutMs == nil || semanticResultTimeoutMs <= *streamLifetimeTimeoutMs) {
			return &StreamReadPlan{
				Phase:                   "active_stream",
				TimeoutMs:               semanticResultTimeoutMs,
				RawTimeoutMs:            &rawTimeoutMs,
				SemanticResultTimeoutMs: &semanticResultTimeoutMs,
				StreamLifetimeTimeoutMs: streamLifetimeTimeoutMs,
				TimeoutKind:             "semantic_result",
				TimeoutMessage:          StreamSemanticResultTimeoutMessage(timeoutSeconds(profile.FirstResponseTimeoutMs)),
				DeadlineExceeded:        semanticResultTimeoutMs <= 0,
			}
		}
		semanticResultVisible := !status.SemanticResultReceived && !status.PendingProtocolEvent && !status.ParserSkipped
		if streamLifetimeTimeoutMs != nil && *streamLifetimeTimeoutMs <= rawTimeoutMs {
			var semanticPtr *int64
			if semanticResultVisible {
				value := semanticResultTimeoutMs
				semanticPtr = &value
			}
			return &StreamReadPlan{
				Phase:                   "active_stream",
				TimeoutMs:               *streamLifetimeTimeoutMs,
				RawTimeoutMs:            &rawTimeoutMs,
				SemanticResultTimeoutMs: semanticPtr,
				StreamLifetimeTimeoutMs: streamLifetimeTimeoutMs,
				TimeoutKind:             "stream_lifetime",
				TimeoutMessage:          StreamMaxLifetimeTimeoutMessage(timeoutSeconds(profile.UncommittedAttemptMaxLifetimeMs)),
				DeadlineExceeded:        *streamLifetimeTimeoutMs <= 0,
			}
		}
		// Raw upstream activity remains the hard timeout while a protocol event
		// is incomplete: large or fragmented events can stay valid while bytes
		// continue to arrive.
		var semanticPtr *int64
		if semanticResultVisible {
			value := semanticResultTimeoutMs
			semanticPtr = &value
		}
		return &StreamReadPlan{
			Phase:                   "active_stream",
			TimeoutMs:               rawTimeoutMs,
			RawTimeoutMs:            &rawTimeoutMs,
			SemanticResultTimeoutMs: semanticPtr,
			StreamLifetimeTimeoutMs: streamLifetimeTimeoutMs,
			TimeoutKind:             "upstream_activity",
			TimeoutMessage:          StreamIdleTimeoutMessage(timeoutSeconds(profile.IdleTimeoutMs)),
			DeadlineExceeded:        rawTimeoutMs <= 0,
		}
	}

	firstChunkTimeoutMs := profile.FirstResponseTimeoutMs - (now - startedAt)
	if streamLifetimeTimeoutMs != nil && *streamLifetimeTimeoutMs <= firstChunkTimeoutMs {
		return &StreamReadPlan{
			Phase:                   "first_chunk",
			TimeoutMs:               *streamLifetimeTimeoutMs,
			StreamLifetimeTimeoutMs: streamLifetimeTimeoutMs,
			TimeoutKind:             "stream_lifetime",
			TimeoutMessage:          StreamMaxLifetimeTimeoutMessage(timeoutSeconds(profile.UncommittedAttemptMaxLifetimeMs)),
			DeadlineExceeded:        *streamLifetimeTimeoutMs <= 0,
		}
	}
	return &StreamReadPlan{
		Phase:            "first_chunk",
		TimeoutMs:        firstChunkTimeoutMs,
		TimeoutKind:      "first_chunk",
		TimeoutMessage:   FirstChunkTimeoutMessage(timeoutSeconds(profile.FirstResponseTimeoutMs)),
		DeadlineExceeded: firstChunkTimeoutMs <= 0,
	}
}

func timeoutSeconds(timeoutMs int64) int64 {
	if timeoutMs <= 0 {
		return 1
	}
	seconds := (timeoutMs + 999) / 1000
	if seconds < 1 {
		return 1
	}
	return seconds
}

// FirstChunkTimeoutMessage 对齐 firstChunkTimeoutMessage。
func FirstChunkTimeoutMessage(timeoutSeconds int64) string {
	return "上游流式请求 " + itoa(timeoutSeconds) + "s 内未返回首段数据"
}

// StreamIdleTimeoutMessage 对齐 streamIdleTimeoutMessage。
func StreamIdleTimeoutMessage(timeoutSeconds int64) string {
	return "上游流式响应 " + itoa(timeoutSeconds) + "s 内未返回任何新数据"
}

// StreamSemanticResultTimeoutMessage 对齐 streamSemanticResultTimeoutMessage。
func StreamSemanticResultTimeoutMessage(timeoutSeconds int64) string {
	return "上游流式响应 " + itoa(timeoutSeconds) + "s 内未返回有效输出、失败或终止事件"
}

// StreamMaxLifetimeTimeoutMessage 对齐 streamMaxLifetimeTimeoutMessage。
func StreamMaxLifetimeTimeoutMessage(timeoutSeconds int64) string {
	return "上游流式响应已达到最大存活时间 " + itoa(timeoutSeconds) + "s，已中断当前连接"
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	var digits [20]byte
	position := len(digits)
	unsigned := uint64(value)
	if negative {
		unsigned = uint64(-value)
	}
	for unsigned > 0 {
		position--
		digits[position] = byte('0' + unsigned%10)
		unsigned /= 10
	}
	if negative {
		position--
		digits[position] = '-'
	}
	return string(digits[position:])
}
