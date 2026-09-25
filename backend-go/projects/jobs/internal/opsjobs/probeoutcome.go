package opsjobs

import (
	"net/http"
)

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
	// ProbeOutcomeCredentialRejected 是相对 Node 探测行为的刻意偏离（用户
	// 2026-09-25 拍板）：上游对探测请求返回 401/403（凭据/授权失效，账号
	// 确定坏）。Node 侧任意 HTTP 状态码都判 framing_complete 并推进恢复，
	// 导致 key 失效账号被自己的 401“治愈”（恢复后真实流量再次熔断，振荡）。
	// 该类别不推进恢复：结算映射（CircuitOutcome）落到 complete_canary/
	// complete_confirmation 的 transport_failure 臂（重新 OPEN + 退避）。
	// 其余状态码（2xx/404/429/5xx）维持 Node 判定不变：429/5xx 说明服务
	// 活着，推进恢复符合分层设计；404 是探测配置问题，不误伤账号。
	ProbeOutcomeCredentialRejected ProbeOutcomeKind = "credential_rejected"
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
		// 401/403 是真实上游 HTTP 响应（framing 完成），不是本地传输失败，
		// 但凭据/授权已确定失效：不得按 framing_complete 推进恢复（Node
		// 行为的刻意偏离，见 ProbeOutcomeCredentialRejected 注释）。
		if *statusCode == http.StatusUnauthorized || *statusCode == http.StatusForbidden {
			return TransportProbeOutcome{Kind: ProbeOutcomeCredentialRejected, StatusCode: statusCode}
		}
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
