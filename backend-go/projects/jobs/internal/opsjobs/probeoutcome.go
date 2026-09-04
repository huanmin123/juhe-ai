package opsjobs

// TransportProbeOutcome 逐字段对齐 Node modules/accounts/automatic-account-probe-outcome.ts
// 的 TransportProbeOutcome。三态:
//   - framing_complete: HTTP framing 完成；StatusCode 必有；
//     SemanticSuccess=false 表示语义探针失败（invalid_probe_output）。
//   - transport_incomplete: 传输未完成，FailureKind 为 timeout/connection/read。
//   - unknown: 无法判定，FailureKind 为 canceled/task_failure。
type TransportProbeOutcome struct {
	Kind            ProbeOutcomeKind `json:"kind"`
	StatusCode      *int             `json:"status_code,omitempty"`
	FailureKind     ProbeFailureKind `json:"failure_kind,omitempty"`
	SemanticSuccess *bool            `json:"semantic_success,omitempty"`
}

type ProbeOutcomeKind string

const (
	ProbeOutcomeFramingComplete     ProbeOutcomeKind = "framing_complete"
	ProbeOutcomeTransportIncomplete ProbeOutcomeKind = "transport_incomplete"
	ProbeOutcomeUnknown             ProbeOutcomeKind = "unknown"
)

type ProbeFailureKind string

const (
	ProbeFailureTimeout     ProbeFailureKind = "timeout"
	ProbeFailureConnection  ProbeFailureKind = "connection"
	ProbeFailureRead        ProbeFailureKind = "read"
	ProbeFailureCanceled    ProbeFailureKind = "canceled"
	ProbeFailureTaskFailure ProbeFailureKind = "task_failure"
)

// ProbeResultSnapshot 是 speed-first/circuit 探针对账户测试结果的窄投影。
type ProbeResultSnapshot struct {
	Success      bool   `json:"success"`
	StatusCode   *int   `json:"status_code,omitempty"`
	FirstTokenMS *int64 `json:"first_token_ms,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	Message      string `json:"message,omitempty"`
}

// UpstreamAttemptSnapshot 是上游尝试的窄投影，对齐 Node UpstreamAttempt 的
// transportFailureKind 判定字段。
type UpstreamAttemptSnapshot struct {
	Status               *int   `json:"status,omitempty"`
	TransportFailureKind string `json:"transport_failure_kind,omitempty"` // timeout | read_incomplete | connection
	IsReal               bool   `json:"is_real"`
	IsCompletedReal      bool   `json:"is_completed_real"`
}

// TransportProbeOutcomeFromResult 对齐 Node transportProbeOutcomeFromAccountTestResult。
func TransportProbeOutcomeFromResult(result ProbeResultSnapshot, upstreamAttempt *UpstreamAttemptSnapshot, canceled, timedOut bool, diagnosticTimeoutExhausted *bool) TransportProbeOutcome {
	if canceled {
		return TransportProbeOutcome{Kind: ProbeOutcomeUnknown, FailureKind: ProbeFailureCanceled}
	}

	var real *UpstreamAttemptSnapshot
	if upstreamAttempt != nil && upstreamAttempt.IsReal {
		real = upstreamAttempt
	}
	var statusCode *int
	if real != nil && real.IsCompletedReal {
		statusCode = real.Status
	}
	exhaustedTimeout := true
	if diagnosticTimeoutExhausted != nil {
		exhaustedTimeout = *diagnosticTimeoutExhausted
	}
	localFailureKind := transportProbeLocalFailureKind(real, statusCode, timedOut, exhaustedTimeout && real != nil)

	if localFailureKind != "" {
		outcome := TransportProbeOutcome{Kind: ProbeOutcomeTransportIncomplete, FailureKind: localFailureKind}
		if statusCode != nil {
			outcome.StatusCode = statusCode
		}
		return outcome
	}
	if statusCode != nil {
		outcome := TransportProbeOutcome{Kind: ProbeOutcomeFramingComplete, StatusCode: statusCode}
		if result.ErrorCode == "invalid_probe_output" {
			success := false
			outcome.SemanticSuccess = &success
		}
		return outcome
	}
	if timedOut && diagnosticTimeoutExhausted != nil && !*diagnosticTimeoutExhausted && localFailureKind == "" {
		return TransportProbeOutcome{Kind: ProbeOutcomeUnknown, FailureKind: ProbeFailureTaskFailure}
	}
	if real != nil {
		return TransportProbeOutcome{Kind: ProbeOutcomeTransportIncomplete, FailureKind: ProbeFailureConnection}
	}
	return TransportProbeOutcome{Kind: ProbeOutcomeUnknown, FailureKind: ProbeFailureTaskFailure}
}

func transportProbeLocalFailureKind(upstream *UpstreamAttemptSnapshot, statusCode *int, timedOut, diagnosticTimeoutExhausted bool) ProbeFailureKind {
	// 诊断 deadline 只有在当前探针阶段所有层级都发起了真实 HTTPS 请求并超时后，
	// 才能作为上游超时证据。
	if upstream != nil {
		switch upstream.TransportFailureKind {
		case "timeout":
			return ProbeFailureTimeout
		case "read_incomplete":
			return ProbeFailureRead
		case "connection":
			return ProbeFailureConnection
		}
	}
	if statusCode != nil {
		return ""
	}
	if timedOut {
		if diagnosticTimeoutExhausted {
			return ProbeFailureTimeout
		}
		return ""
	}
	if upstream != nil {
		return ProbeFailureConnection
	}
	return ""
}

// TransportProbeMeetsFirstByteTarget 对齐 Node transportProbeMeetsFirstByteTarget。
func TransportProbeMeetsFirstByteTarget(result ProbeResultSnapshot, outcome TransportProbeOutcome, firstByteThresholdMS int64) bool {
	return result.Success &&
		outcome.Kind == ProbeOutcomeFramingComplete &&
		result.FirstTokenMS != nil &&
		*result.FirstTokenMS <= firstByteThresholdMS
}
