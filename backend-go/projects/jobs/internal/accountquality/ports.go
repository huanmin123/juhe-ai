package accountquality

import (
	"context"
	"time"
)

// Clock 注入时间源（硬门禁：时间可 Mock）。
type Clock interface {
	Now() time.Time
}

// SystemClock 真实时间。
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

// SettingsNumber 读取系统设置（Node settingsNumber(key, min, max) 语义：
// 越界回落边界值）。由宿主进程注入；测试用 map 实现。
type SettingsNumber func(key string, min, max int) int

// 默认间隔常量（Node scheduler 注册处的取值范围）。
const (
	AccountQualityRefreshIntervalMinSeconds = 60
	AccountQualityRefreshIntervalMaxSeconds = 3600
	AccountQualityWindowMinMinutes          = 1
	AccountQualityWindowMaxMinutes          = 60
	CooldownRetestIntervalMinSeconds        = 1
	CooldownRetestIntervalMaxSeconds        = 3600
	CooldownRetestMaxBackoffMinHours        = 1
	CooldownRetestMaxBackoffMaxHours        = 24 * 30
)

// 批大小默认值（Node runtimeConfig.background 默认）。
const (
	DefaultPrecheckBatchSize       = 10
	DefaultCooldownRetestBatchSize = 10
)

// QueueConcurrency 返回全局共享队列并发（Node globalSharedQueueConcurrency）。
type QueueConcurrency func() int

// Prober 是与 gateway 的窄衔接 port：执行一次账户诊断探针。
// Go jobs 不直接实现 Node 诊断栈（testOpenAIAccountDiagnosticAttempt 属于
// gateway/账户域），由宿主注入实现；测试用 mock 闭环。
type Prober interface {
	// Probe 执行一次有界诊断。实现负责超时与传输层证据收集；
	// 返回 (nil, nil) 表示没有产生诊断结果（等价 Node missing diagnostic result）。
	Probe(ctx context.Context, req ProbeRequest) (*ProbeObservation, error)
}

// ProbeRequest 描述一次探针的目标账户与约束。
type ProbeRequest struct {
	// AccountID 业务账户 ID。
	AccountID string
	// SystemAccountID 归属系统账户。
	SystemAccountID string
	// GroupID 绑定分组。
	GroupID string
	// TrafficSource 取值 runtime_recovery_probe（precheck）| cooldown_retest。
	TrafficSource string
	// Full 为 true 时 precheck 使用 full 诊断；cooldown-retest 使用 limited。
	Full bool
	// FixedAPIKey/FixedKeyFingerprint/FixedKeyIndex 钉住复测的具体 Key。
	FixedAPIKey         string
	FixedKeyFingerprint string
	FixedKeyIndex       int
}

// AccountForTest 是 loadAccountForTest 的最小视图
// （等价 Node find_account_for_test 返回的被消费字段）。
type AccountForTest struct {
	ID                   string
	Name                 string
	Type                 string // 'api_key' | ...
	Status               string
	Schedulable          bool
	BoundGroupID         string
	OwnerSystemAccountID string
	SystemAccountID      string
	ProtocolCode         string
	AccountExpiresAt     string
	EffectiveAvailable   bool
	HasEffectiveAvail    bool
	QuotaRecoveryPolicy  map[string]any
}

// AccountReader 是 find_account_for_test / find_openai_account_for_group 的
// 窄 port（Node 经 DB-service IPC；Go jobs 由宿主注入直查或网关读取器）。
type AccountReader interface {
	FindAccountForTest(ctx context.Context, accountID string) (*AccountForTest, error)
	// FindAccountForGroup 返回可执行候选（includeUnavailable/ignoreAvailability=true）。
	FindAccountForGroup(ctx context.Context, groupID, accountID, systemAccountID string) (*OpenAIAccountCandidate, error)
	// HasAPIKeyEntry 校验 keyFingerprint+apiKey 是否仍在当前凭据池
	// （等价 accountApiKeyEntries(...).find(...)）。
	HasAPIKeyEntry(ctx context.Context, candidate *OpenAIAccountCandidate, fingerprint, apiKey string) (bool, error)
}

// OpenAIAccountCandidate 是 find_openai_account_for_group 的最小视图。
type OpenAIAccountCandidate struct {
	ID                  string
	Name                string
	Type                string
	Status              string
	DispatchRevision    int64
	HasDispatchRevision bool
	QuotaRecoveryPolicy map[string]any
}

// PrecheckMutationResult 等价 DB-service mark_account_precheck_temporary_unavailable
// 的返回：updated 或 skippedReason。
type PrecheckMutationResult struct {
	Updated       bool
	SkippedReason string
}

// KeyMutationResult 等价 record_account_api_key_success/failure 与
// defer_account_api_key_probe 的返回（changed）。
type KeyMutationResult struct {
	Changed bool
}

// PrecheckMutation 是 mark_account_precheck_temporary_unavailable 的窄 port。
// 实现方必须按 Node precheckTemporaryUnavailableSkipReason 的 fence 校验顺序
// 返回 skippedReason（invalid_precheck_fence / account_missing / hard_unavailable /
// stale_dispatch_revision / stale_account_status / invalid_runtime_state /
// newer_health_success / stale_account_updated）。
type PrecheckMutation interface {
	MarkPrecheckTemporaryUnavailable(ctx context.Context, input PrecheckMutationInput) (PrecheckMutationResult, error)
}

// PrecheckMutationInput 携带 fence 与理由文本（reason 由本包按 Node 模板生成）。
type PrecheckMutationInput struct {
	AccountID                string
	Reason                   string
	PrecheckStartedAt        string
	ExpectedDispatchRevision int64
	ExpectedStatus           string
}

// CooldownCandidateSource 是 list_account_api_key_runtime_states_due_for_probe
// 的窄 port。
type CooldownCandidateSource interface {
	ListDueForProbe(ctx context.Context, limit int) ([]CooldownProbeCandidate, error)
}

// CooldownMutation 覆盖三个 Key 运行态写入 port：
// record_account_api_key_success / record_account_api_key_failure /
// defer_account_api_key_probe。
type CooldownMutation interface {
	RecordKeySuccess(ctx context.Context, input KeySuccessInput) (KeyMutationResult, error)
	RecordKeyFailure(ctx context.Context, input KeyFailureInput) (KeyMutationResult, error)
	DeferKeyProbe(ctx context.Context, input KeyDeferInput) (KeyMutationResult, error)
}

// KeyMutationExpected 是三个写入共享的 CAS fence 字段。
type KeyMutationExpected struct {
	Status                string
	NextProbeAt           string
	StateUpdatedAt        string
	ProbeClaimToken       string
	AccountConfigRevision int64
}

// KeySuccessInput 对应 type: 'record_account_api_key_success'。
type KeySuccessInput struct {
	AccountID     string
	TrafficSource string
	ProbeOutcome  string
	ObservedAt    string
	Expected      KeyMutationExpected
}

// KeyFailureInput 对应 type: 'record_account_api_key_failure'。
type KeyFailureInput struct {
	AccountID                string
	TrafficSource            string
	ProbeOutcome             string
	QuotaRecoveryMode        string
	Status                   string
	StatusCode               int
	ErrorCode                string
	ErrorMessage             string
	CooldownUntil            string
	TraceID                  string
	BreakQuotaRecoveryWindow bool
	ObservedAt               string
	Expected                 KeyMutationExpected
}

// KeyDeferInput 对应 type: 'defer_account_api_key_probe'。
type KeyDeferInput struct {
	AccountID                string
	TrafficSource            string
	ProbeOutcome             string
	QuotaRecoveryMode        string
	DelaySeconds             int
	BreakQuotaRecoveryWindow bool
	ObservedAt               string
	Expected                 KeyMutationExpected
}

// RuntimeCacheInvalidator 对应 clearGatewayRuntimeCache()：质量缓存刷新后
// 通知 gateway 失效运行时缓存。实现方决定通道（进程内钩子/消息）。
type RuntimeCacheInvalidator interface {
	ClearGatewayRuntimeCache(ctx context.Context)
}

// Logger 接收结构化字段事件；字段键与 Node 日志事件一致。
type Logger interface {
	Debug(event string, fields map[string]any, message string)
	Info(event string, fields map[string]any, message string)
	Warn(event string, fields map[string]any, message string)
	Error(event string, fields map[string]any, message string)
}

// NopLogger 丢弃全部日志（测试可选）。
type NopLogger struct{}

func (NopLogger) Debug(string, map[string]any, string) {}
func (NopLogger) Info(string, map[string]any, string)  {}
func (NopLogger) Warn(string, map[string]any, string)  {}
func (NopLogger) Error(string, map[string]any, string) {}
