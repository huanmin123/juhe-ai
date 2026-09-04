// Package accountquality 迁移 J3c 账户质量任务族：
//   - account-quality-refresh（stats-worker，统计 writer：EWMA/成功率/质量分刷新与清理）
//   - account-quality-failure-precheck-queue（ops-worker，账户可用性状态 writer）
//   - account-api-key-cooldown-retest / -queue（ops-worker，账户内 API Key 复测）
//
// 触发条件、批大小、fence/CAS、失败终态与中文文案逐字段对齐 Node
// backend/src/modules/background/{account-probe-jobs,account-quality-failure-precheck.service,
// account-api-key-cooldown-retest.service}.ts 与 backend/src/storage/account-quality.repository.ts。
// 与 gateway 的衔接只经窄 port（Prober/AccountReader/Mutator 等），测试以 mock 闭环。
package accountquality

import "time"

// QualityState 与 Node AccountQualityState 一致。
type QualityState string

const (
	QualityFresh   QualityState = "fresh"
	QualityStale   QualityState = "stale"
	QualityFailed  QualityState = "failed"
	QualityUnknown QualityState = "unknown"
)

// 刷新与清理常量（Node account-quality.repository.ts 顶部常量）。
const (
	UnknownQualityScore            = 1_000_000
	FailurePenaltyMs               = 60_000
	StalePenaltyMs                 = 5_000
	UnknownStatePenaltyMs          = 10_000
	AgePenaltyCapMs                = 10_000
	QualityCleanupBatchLimit       = 1000
	QualityLookupChunkSize         = 500
	DirtyAccountBatchLimit         = 500
	PrecheckMinRequests            = 5
	PrecheckMinErrors              = 2
	PrecheckFrequentErrors         = 5
	PrecheckMaxSuccessRate         = 0.5
	PrecheckCandidateLimitMax      = 100
	PrecheckOffsetMax              = 1_000_000
	RecentPrecheckRetention        = 30 * time.Minute
	APIKeyQuotaObservationTimeout  = 30 * 24 * time.Hour
	CooldownDefaultDeferSeconds    = 60
	CooldownQuotaMinDeferSeconds   = 60
	AccountStatusTemporaryUnavail  = "temporary_unavailable"
	AccountStatusRateLimited       = "rate_limited"
	AccountStatusError             = "error"
	AccountStatusActive            = "active"
	QuotaRecoveryTimeoutErrorCode  = "api_key_quota_recovery_timeout"
	QuotaRecoveryGenericErrorCode  = "api_key_quota_insufficient"
	QuotaRecoveryExplicitErrorCode = "api_key_quota_insufficient_reset"
	APIKeyGenericQuotaInterval     = time.Hour
)

// FailurePrecheckCandidate 等价 Node AccountQualityFailurePrecheckCandidate。
type FailurePrecheckCandidate struct {
	AccountID          string
	SystemAccountID    string
	ProviderCode       string
	RecentRequestCount int
	RecentSuccessCount int
	RecentErrorCount   int
	SuccessRate        *float64
	LastErrorAt        string
	LastErrorMessage   string
	UpdatedAt          string
}

// QualityRow 是 account_quality_scores 行（等价 Node AccountQualityRow）。
type QualityRow struct {
	AccountID                   string
	SystemAccountID             string
	ProviderCode                string
	QualityScore                int64
	QualityState                QualityState
	RecentRequestCount          int64
	RecentSuccessCount          int64
	RecentErrorCount            int64
	RecentFirstTokenSampleCount int64
	RecentAvgFirstTokenMs       *int64
	EwmaFirstTokenMs            *int64
	SuccessRate                 *float64
	WindowStartedAt             string
	WindowEndedAt               string
	LastSampleAt                *string
	LastSuccessAt               *string
	LastErrorAt                 *string
	LastErrorMessage            *string
	UpdatedAt                   string
}

// RefreshResult 等价 Node AccountQualityRealtimeRefreshResult。
type RefreshResult struct {
	Refreshed       int
	Removed         int64
	WindowStartedAt string
	WindowEndedAt   string
}

// CooldownProbeCandidate 等价 Node AccountApiKeyRuntimeProbeCandidate。
type CooldownProbeCandidate struct {
	AccountID             string
	AccountName           string
	KeyFingerprint        string
	KeyIndex              int
	APIKey                string
	Status                string
	NextProbeAt           string
	StateUpdatedAt        string
	AccountConfigRevision int64
	ProbeClaimToken       string
	ProbeClaimedUntil     string
	RecoveryStartedAt     string
	LastErrorCode         string
}

// ProbeEvidence 是 Prober 观测到的传输层证据，用于可复现地分类探针结果
// （等价 Node automaticAccountProbeOutcome 的 evidence 入参）。
type ProbeEvidence struct {
	// HasRealUpstreamAttempt 表示发生过真实上游 HTTP(S) 尝试。
	HasRealUpstreamAttempt bool
	// UpstreamCompleted 表示真实上游尝试完成了 HTTP framing。
	UpstreamCompleted bool
	// UpstreamStatus 为已完成的真实上游尝试的 HTTP 状态。
	UpstreamStatus int
	// TransportFailureKind 取值 "" | "timeout" | "connection" | "read_incomplete"。
	TransportFailureKind string
	// Canceled / TimedOut / DiagnosticTimeoutExhausted 与 Node 语义一致。
	Canceled                   bool
	TimedOut                   bool
	DiagnosticTimeoutExhausted bool
}

// ProbeResult 是探针诊断结果（等价 Node AccountTestResult 的被消费字段）。
type ProbeResult struct {
	Success bool
	// StatusCode 为 nil 表示未取得 HTTP 状态（Node 的 undefined）。
	StatusCode *int
	ErrorCode  string
	Message    string
	DurationMs int64
	TraceID    string
	// ProtocolCode 决定配额决策的报文协议："openai" | "anthropic" | "gemini"。
	ProtocolCode string
	// ResponseBodyText / ResponseHeaders 提供额度恢复 hint 解析输入。
	ResponseBodyText string
	ResponseHeaders  map[string]string
}

// ProbeObservation 是 Prober 的完整观测。
type ProbeObservation struct {
	Result   ProbeResult
	Evidence ProbeEvidence
}

// ProbeOutcome 与 Node AutomaticAccountProbeOutcome 一致（stale 不由本分类产生）。
type ProbeOutcome string

const (
	OutcomeCompleteSuccess        ProbeOutcome = "complete_success"
	OutcomeFramingCompleteNeutral ProbeOutcome = "framing_complete_neutral"
	OutcomeUpstreamFailure        ProbeOutcome = "upstream_failure"
	OutcomeProbeTaskFailure       ProbeOutcome = "probe_task_failure"
)

// TransportFailureKind 常量（等价 Node upstream attempt transportFailureKind）。
const (
	TransportFailureTimeout    = "timeout"
	TransportFailureConnection = "connection"
	TransportFailureRead       = "read_incomplete"
)

// AutomaticProbeOutcome 是 automaticAccountProbeOutcome 的逐分支移植。
// 说明：Node evidence.diagnosticTimeoutExhausted 允许 undefined（按 true 处理），
// 但两个队列的实际调用方总是传显式布尔值，Go 端以 bool 直传，语义一致。
func AutomaticProbeOutcome(result ProbeResult, evidence ProbeEvidence) ProbeOutcome {
	// transportProbeOutcomeFromAccountTestResult：
	if evidence.Canceled {
		return OutcomeProbeTaskFailure // unknown/canceled
	}
	statusCodeSet := evidence.HasRealUpstreamAttempt && evidence.UpstreamCompleted
	if local := transportProbeLocalFailureKind(evidence, statusCodeSet); local != "" {
		return OutcomeUpstreamFailure // transport_incomplete
	}
	if statusCodeSet {
		if result.Success {
			return OutcomeCompleteSuccess // framing_complete
		}
		return OutcomeFramingCompleteNeutral // framing_complete（语义失败）
	}
	if evidence.TimedOut && !evidence.DiagnosticTimeoutExhausted {
		return OutcomeProbeTaskFailure // unknown/task_failure
	}
	if evidence.HasRealUpstreamAttempt {
		return OutcomeUpstreamFailure // transport_incomplete/connection
	}
	return OutcomeProbeTaskFailure // unknown/task_failure
}

func transportProbeLocalFailureKind(evidence ProbeEvidence, statusCodeSet bool) string {
	switch evidence.TransportFailureKind {
	case TransportFailureTimeout:
		return "timeout"
	case TransportFailureRead:
		return "read"
	case TransportFailureConnection:
		return "connection"
	}
	if statusCodeSet {
		return ""
	}
	diagnosticTimeoutExhausted := evidence.DiagnosticTimeoutExhausted && evidence.HasRealUpstreamAttempt
	if evidence.TimedOut {
		if diagnosticTimeoutExhausted {
			return "timeout"
		}
		return ""
	}
	if evidence.HasRealUpstreamAttempt {
		return "connection"
	}
	return ""
}

// AvailabilityProbeFailed 是 automaticAccountAvailabilityProbeFailed 的移植：
// 只有 upstream_failure / framing_complete_neutral 形成有效上游可用性失败结论。
func AvailabilityProbeFailed(outcome ProbeOutcome) bool {
	return outcome == OutcomeUpstreamFailure || outcome == OutcomeFramingCompleteNeutral
}
