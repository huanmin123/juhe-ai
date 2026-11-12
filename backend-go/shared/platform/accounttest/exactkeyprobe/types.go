// 本文件是 jobs internal/accounthealth/types.go 的成对子集下沉（成对关系见
// probe.go 包注释）：仅承载探针执行器闭包所需的 Input 快照与探针结果类型。
// jobs 留守侧的 J1 输出 schema（Projection/Outcome/CurrentState/ProbeRequest、
// SourceFence/KeyModelFence 与 OutcomeStale 等）不在此处，仍归 jobs 拥有；
// 两侧同名字段/取值必须保持逐字一致，修改时必须两侧同步。
package exactkeyprobe

import "time"

const (
	OutcomeSuccess = "complete_success"
	// OutcomeHTTPFailure 表示真实上游 HTTP 交换已完成但未取得协议成功证据
	// （非 2xx、2xx-invalid-body、协议错误对象等）。对恢复决策它就是一次失败，
	// 不再称"中性"；与 transport 失败走同一快速复测道，但不触发只对
	// transport 证据生效的 10 分钟有界终态。
	OutcomeHTTPFailure = "upstream_http_failure"
	// OutcomeUpstreamFailed 仅限 transport incomplete（超时 / 连接 / 读取中断 /
	// framing 未完成），此时 StatusCode == 0。
	OutcomeUpstreamFailed = "upstream_failure"
	OutcomeTaskFailed     = "probe_task_failure"
)

type CredentialEnvelope struct {
	Kind       string `json:"kind"`
	Ciphertext string `json:"ciphertext"`
}

type APIKeyInput struct {
	Index       int                `json:"index"`
	Fingerprint string             `json:"fingerprint"`
	Credential  CredentialEnvelope `json:"credential"`
}

// Input is the immutable, signed snapshot consumed by jobs. It intentionally
// contains encrypted credential material only; the decrypted values never
// leave the direct-probe call.
type Input struct {
	AccountID           string              `json:"account_id"`
	InputVersion        int64               `json:"input_version"`
	ConfigRevision      int64               `json:"config_revision"`
	DispatchRevision    int64               `json:"dispatch_revision"`
	Provider            string              `json:"provider"`
	ProtocolProfileID   string              `json:"provider_protocol_profile_id,omitempty"`
	ProtocolCode        string              `json:"protocol_code,omitempty"`
	ProtocolVersion     string              `json:"protocol_version,omitempty"`
	Type                string              `json:"type"`
	ClientCompatibility string              `json:"client_compatibility,omitempty"`
	EndpointMode        string              `json:"endpoint_mode"`
	HealthModel         string              `json:"health_model"`
	BaseURL             string              `json:"base_url"`
	KeySetFingerprint   string              `json:"key_set_fingerprint,omitempty"`
	APIKeys             []APIKeyInput       `json:"api_keys,omitempty"`
	OAuthAccess         *CredentialEnvelope `json:"oauth_access,omitempty"`
	// SpeechToken 是火山 openspeech 语音域的 speech_token（仅 J1 媒体连通性
	// 使用，不进入 Input JSON、不落库、不写日志）。
	SpeechToken          string              `json:"-"`
	OAuthExpiresAt       *time.Time          `json:"oauth_expires_at,omitempty"`
	OAuthAccountID       string              `json:"oauth_account_id,omitempty"`
	OAuthQuotaProjectID  string              `json:"oauth_quota_project_id,omitempty"`
	OAuthType            string              `json:"oauth_type,omitempty"`
	OAuthProjectID       string              `json:"oauth_project_id,omitempty"`
	Proxy                *CredentialEnvelope `json:"proxy,omitempty"`
	IssuedAt             time.Time           `json:"issued_at"`
	ExpiresAt            time.Time           `json:"expires_at"`
	TLSPolicyVersion     string              `json:"tls_policy_version"`
	AllowInsecureBaseURL bool                `json:"allow_insecure_base_url"`
	Eligibility          Eligibility         `json:"eligibility"`
	Cooldown             *CooldownFence      `json:"cooldown_fence,omitempty"`
	Schedule             Schedule            `json:"schedule"`
}

// Eligibility is a Node business-state snapshot, not a task decision.  Go
// refuses to probe as soon as this immutable snapshot is expired or declares
// the account unavailable; it never opens the Node business SQLite file.
type Eligibility struct {
	AccountStatus                              string     `json:"account_status"`
	Schedulable                                bool       `json:"schedulable"`
	BoundGroup                                 bool       `json:"bound_group"`
	AuthorizationEligible                      bool       `json:"authorization_eligible"`
	SourceConfigRevision                       *int64     `json:"source_config_revision,omitempty"`
	CooldownUntil                              *time.Time `json:"cooldown_until,omitempty"`
	TemporaryUnavailableContinuousProbeEnabled *bool      `json:"temporary_unavailable_continuous_probe_enabled,omitempty"`
	// ExpeditedRecovery 是按账户显式标记的恢复道加速档（"特供"，账户属性而非
	// 状态，与 TemporaryUnavailableContinuousProbeEnabled 同类的事实布尔）。
	// jobs 冷却复测据此收紧节奏：失败慢速道基准 15s、跳过 1 小时长期降频道、
	// 绕开有界 10 分钟 error 终态（7 天观察超时保留）；真实上游非成功结果
	// （HTTP 失败与 transport 失败）都走失败快速道，不读取
	// Schedule.CooldownNeutralMaxMS（该字段现在只约束本地任务故障
	// probe_task_failure 的顺延上限，特供账户由 reader 覆盖为 60s）。
	ExpeditedRecovery bool `json:"expedited_recovery,omitempty"`
}

// Schedule is frozen with each input version.  Changing a policy requires a
// new input version, which makes pending work visibly stale instead of
// applying a new policy halfway through an old account configuration.
type Schedule struct {
	HealthIntervalMS int64 `json:"health_interval_ms"`
	HealthJitterMS   int64 `json:"health_jitter_ms"`
	FailureThreshold int   `json:"failure_threshold"`
	FailureRetryMS   int64 `json:"failure_retry_ms"`
	// CooldownNeutralBaseMS / CooldownNeutralMaxMS 自 2026-11-03 冷却复测二元化
	// 后只约束本地任务故障（probe_task_failure）的顺延基数与上限，不再影响
	// 真实上游非成功结果（HTTP 失败与 transport 失败同走失败快速道）。
	CooldownNeutralBaseMS    int64 `json:"cooldown_neutral_base_ms"`
	CooldownNeutralMaxMS     int64 `json:"cooldown_neutral_max_ms"`
	CooldownFailureBackoffMS int64 `json:"cooldown_failure_backoff_ms"`
	MaxPauseMinutes          int   `json:"max_pause_minutes,omitempty"`
	MaxRecoveryHours         int   `json:"max_recovery_hours,omitempty"`
}

type CooldownFence struct {
	ObservationStartedAt time.Time `json:"observation_started_at"`
	Generation           string    `json:"generation"`
	SourceConfigRevision *int64    `json:"source_config_revision,omitempty"`
}

type ProbeResult struct {
	Outcome      string
	StatusCode   int
	ErrorCode    string
	ErrorMessage string
}
