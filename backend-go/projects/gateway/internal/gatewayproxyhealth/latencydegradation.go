package gatewayproxyhealth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// Ports runtime/normal-route-latency-degradation.service.ts: normal-route
// speed-first latency degradation (slow sampling, bounded degradation TTL,
// two-probe recovery rounds, generation-scoped indexes and claims).

// LatencyDegradationScope mirrors NormalRouteLatencyDegradationScope.
// PreferenceID / ConfigFingerprint 是调度偏好运行态身份（速度运行态 v2 命名
// 空间）：两者均空 = 历史策略路径（v1 键族，键与 state JSON 与现状逐字节
// 一致）；PreferenceID 非空 = v2 键族（state 键多出 preferenceID 与
// configFingerprint 两段）。字段随 Scope 一起写进 state JSON，omitempty 保证
// v1 state 的载荷形状不变。
type LatencyDegradationScope struct {
	SystemAccountID   string `json:"systemAccountId"`
	RouteStrategyID   string `json:"routeStrategyId"`
	GroupID           string `json:"groupId"`
	PreferenceID      string `json:"preferenceId,omitempty"`
	ConfigFingerprint string `json:"configFingerprint,omitempty"`
}

// SpeedFirstRuntimeConfig mirrors NormalRouteSpeedFirstRuntimeConfig
// (RouteStrategySpeedFirstConfig + firstByteDeadlineMs). The JSON tags match
// the Node state payload exactly.
type SpeedFirstRuntimeConfig struct {
	FirstByteDeadlineMs           int64 `json:"firstByteDeadlineMs"`
	SlowTriggerCount              int64 `json:"slowTriggerCount"`
	SlowWindowSeconds             int64 `json:"slowWindowSeconds"`
	RecoverySuccessCount          int64 `json:"recoverySuccessCount"`
	ProbeIntervalSeconds          int64 `json:"probeIntervalSeconds"`
	DegradedTTLSeconds            int64 `json:"degradedTtlSeconds"`
	MaxFirstByteRetriesPerRequest int64 `json:"maxFirstByteRetriesPerRequest"`
	// 总时间兜底截止（设计 6.2）：不纳入 isRouteStrategySpeedFirstConfig 的
	// 七字段存量校验；存量状态解码时缺省零值视为"未设置"，回落策略当前配置。
	TotalTimeDeadlineMs           int64 `json:"totalTimeDeadlineMs"`
	CompactionTotalTimeDeadlineMs int64 `json:"compactionTotalTimeDeadlineMs"`
}

// LatencyDegradationOrderResult mirrors
// NormalRouteLatencyDegradationOrderResult; the generic accounts are the
// caller's original elements.
type LatencyDegradationOrderResult[T any] struct {
	Accounts            []T
	Applied             bool
	DegradedAccountIDs  []string
	BypassedAllDegraded bool
}

// LatencySlowResult mirrors NormalRouteLatencySlowResult.
type LatencySlowResult struct {
	AccountID            string
	SlowCount            int64
	Degraded             bool
	DegradedUntil        *string
	RecoverySuccessCount int64
	NextProbeAt          *string
}

// LatencySuccessResult mirrors NormalRouteLatencySuccessResult.
type LatencySuccessResult struct {
	AccountID                    string
	Cleared                      bool
	RecoverySuccessCount         int64
	RequiredRecoverySuccessCount int64
}

// LatencyProbeCandidate mirrors NormalRouteLatencyProbeCandidate.
type LatencyProbeCandidate struct {
	StateKey                       string
	Generation                     string
	AccountID                      string
	AccountName                    *string
	RuntimeKey                     string
	Scope                          LatencyDegradationScope
	Config                         SpeedFirstRuntimeConfig
	DegradationEventID             *string
	DegradedUntil                  string
	NextProbeAt                    string
	RecoverySuccessCount           int64
	RecoveryProbeRoundAttemptCount int64
	RecoveryProbeRoundSuccessCount int64
	// Dimension 是归一后的降级维度（first_byte | total_time）；候选选择已把
	// total_time 维度排除在外，透出该字段供调用方兜底核对。
	Dimension string
}

// LatencyProbeClaim mirrors NormalRouteLatencyProbeClaim.
type LatencyProbeClaim struct {
	StateKey   string
	Generation string
	LockKey    string
	Token      string
}

// DegradedRuntimeItem mirrors NormalRouteLatencyDegradedRuntimeItem: the
// admin-facing runtime item without internal coordination fields.
type DegradedRuntimeItem struct {
	AccountID                      string
	AccountName                    *string
	ScopeRouteStrategyID           string
	ScopeGroupID                   string
	SlowCount                      int64
	SlowTriggerCount               int64
	SlowWindowSeconds              int64
	DegradedUntil                  string
	NextProbeAt                    *string
	RecoverySuccessCount           int64
	RequiredRecoverySuccessCount   int64
	RecoveryProbeRoundAttemptCount int64
	RecoveryProbeRoundSuccessCount int64
	Reason                         string
	// Dimension 是归一后的降级维度（first_byte | total_time），供运行态展示
	// 区分触发通道（设计 6.4）。
	Dimension string `json:"dimension"`
	// Source 标识状态所在命名空间（strategy=策略段 v1 / preference=偏好段 v2）；
	// PreferenceID 仅 Source=preference 时非空。两代条目并存不去重。
	Source       string
	PreferenceID string
}

// latencyRuntimeSourceOf 把 Scope 的偏好身份映射为查询条目的来源标识。
func latencyRuntimeSourceOf(scope LatencyDegradationScope) string {
	if scope.PreferenceID != "" {
		return DegradedRuntimeSourcePreference
	}
	return DegradedRuntimeSourceStrategy
}

// LatencyGenerationEvent mirrors NormalRouteLatencyGenerationEvent.
type LatencyGenerationEvent struct {
	Version     string `json:"version"`
	PublishedAt string `json:"publishedAt"`
}

// 慢样本维度（设计 6.4 / 6.5）：首字通道与总时间通道共享一次降级，恢复入口
// 按触发维度双向过滤。存量状态没有 dimension 字段，空串兼容读作 first_byte。
const (
	LatencyDimensionFirstByte = "first_byte"
	LatencyDimensionTotalTime = "total_time"
)

// latencyState mirrors NormalRouteLatencyState with Node JSON field names.
type latencyState struct {
	Generation                     string                  `json:"generation"`
	AccountID                      string                  `json:"accountId"`
	AccountName                    *string                 `json:"accountName,omitempty"`
	RuntimeKey                     string                  `json:"runtimeKey"`
	Scope                          LatencyDegradationScope `json:"scope"`
	Config                         SpeedFirstRuntimeConfig `json:"config"`
	FirstSlowAtMs                  int64                   `json:"firstSlowAtMs"`
	LastSlowAtMs                   int64                   `json:"lastSlowAtMs"`
	SlowCount                      int64                   `json:"slowCount"`
	DegradationEventID             *string                 `json:"degradationEventId,omitempty"`
	DegradedUntilMs                *int64                  `json:"degradedUntilMs,omitempty"`
	SuccessCount                   int64                   `json:"successCount"`
	RecoveryProbeRoundAttemptCount *int64                  `json:"recoveryProbeRoundAttemptCount,omitempty"`
	RecoveryProbeRoundSuccessCount *int64                  `json:"recoveryProbeRoundSuccessCount,omitempty"`
	NextProbeAtMs                  *int64                  `json:"nextProbeAtMs,omitempty"`
	Reason                         string                  `json:"reason"`
	// 总时间通道计数（设计 6.4）：与首字通道各自独立滑窗计数。
	TotalTimeSlowCount     int64 `json:"totalTimeSlowCount"`
	FirstTotalTimeSlowAtMs int64 `json:"firstTotalTimeSlowAtMs"`
	LastTotalTimeSlowAtMs  int64 `json:"lastTotalTimeSlowAtMs"`
	// Dimension 是最近一次触发降级的维度；空串=存量首字状态，兼容读作
	// first_byte（latencyDimensionOf）。
	Dimension string `json:"dimension,omitempty"`
}

// latencyDimensionOf 归一状态的降级维度：只有显式 total_time 读作
// total_time，其余（含存量空串）读作 first_byte。
func latencyDimensionOf(state latencyState) string {
	if state.Dimension == LatencyDimensionTotalTime {
		return LatencyDimensionTotalTime
	}
	return LatencyDimensionFirstByte
}

func (s *latencyState) clone() latencyState {
	copy := *s
	return copy
}

type latencyStateLock struct {
	key     string
	lockKey string
	token   string
}

// LatencyDegradationLogFunc receives the Node logger payloads.
type LatencyDegradationLogFunc func(fields map[string]any, message string)

// LatencyDegradationOptions carries the fixed Node constants plus the two
// environment-derived values (concurrency.globalMax drives the exact-clear
// worker pool).
type LatencyDegradationOptions struct {
	BackgroundClearConcurrency int // runtimeConfig.concurrency.globalMax (5000)
	// LockRetryDelay overrides the lock-acquire backoff sleep
	// (min(100ms, 20+5*attempt)); tests inject a no-op.
	LockRetryDelay func(attempt int)
	// Random overrides the passive-schedule jitter random source.
	Random func() float64
	// BackgroundProbeDisabled 判定账户是否处于手动模式（后台自动探测总开关
	// background_probe_enabled=0，总开关设计 §5）：手动账户的降级仍可由真实
	// 流量建立（排序保留、TTL 到期自然恢复），但不写 nextProbeAt / probe-index，
	// 后台恢复探针因此无候选。nil 或查询失败视为开启（fail-open，与 flag=1
	// 默认路径一致）。生产由组合根注入主键单列 SELECT。
	BackgroundProbeDisabled func(ctx context.Context, account SuppressibleGatewayAccount) (bool, error)
}

const (
	// v1（历史策略路径，完整保留）与 v2（调度偏好路径）两个键族版本段。
	// v1 派生键常量继续存在并被测试引用，是"历史键零字节漂移"的锚点。
	latencyStateVersion                = "v1"
	latencyStateVersionV2              = "v2"
	latencyStateGenerationKey          = latencyStateVersion + ":generation"
	latencyStateAllIndexKey            = latencyStateVersion + ":all-index"
	latencyStateProbeIndexKey          = latencyStateVersion + ":probe-index"
	latencyStateAllIndexLockKey        = latencyStateVersion + ":all-index-lock"
	latencyStateProbeIndexLockKey      = latencyStateVersion + ":probe-index-lock"
	latencyStateIndexMaxKeys           = 10_000
	latencyStateIndexTTLMs             = int64(24 * 60 * 60 * 1000)
	latencyStateGenerationTTLMs        = int64(48 * 60 * 60 * 1000)
	latencyStateLockAcquireMaxAttempts = 50
	latencyStateLockAcquireMaxDelayMs  = 100
	latencyStateMutationLockTTLMs      = int64(2*latencyStateLockAcquireMaxAttempts*latencyStateLockAcquireMaxDelayMs) + 5000
	latencyStateIndexLockTTLMs         = latencyStateMutationLockTTLMs
	latencyStateGenerationCASMax       = 8
	latencyStateIndexCASMax            = 8
	normalRouteRecoveryProbeRoundSize  = int64(2)
	normalRouteRecoveryProbeIntervalMs = int64(5_000)
	latencyProbeClaimTTLMs             = int64(2 * 60 * 1000)
	// NormalRouteLatencyProbeClaimRenewIntervalMs mirrors the exported Node
	// constant (30s probe claim renewal cadence).
	NormalRouteLatencyProbeClaimRenewIntervalMs = int64(30_000)
)

// 降级运行态条目的来源命名空间（查询侧透出字段；与键族版本一一对应）。
const (
	DegradedRuntimeSourceStrategy   = "strategy"
	DegradedRuntimeSourcePreference = "preference"
)

// latencyStateKeyspace 收敛同一版本段下全部键的构造：state、generation
// marker、all/probe 索引、索引锁、mutation lock 与 probe claim。v1 输出与
// 既有 v1 常量拼接逐字节一致；v2 按同一形状以版本段 v2 同构构造（契约：
// 派生键按 v2 版本段同构）。
type latencyStateKeyspace struct{ version string }

var (
	latencyStateKeyspaceV1 = latencyStateKeyspace{version: latencyStateVersion}
	latencyStateKeyspaceV2 = latencyStateKeyspace{version: latencyStateVersionV2}
)

// latencyStateKeyspaces 按"先 v1 后 v2"的稳定顺序枚举两代键族（扫描与清理
// 的遍历顺序确定，错误聚合与输出排序不随 map 迭代抖动）。
func latencyStateKeyspaces() []latencyStateKeyspace {
	return []latencyStateKeyspace{latencyStateKeyspaceV1, latencyStateKeyspaceV2}
}

// latencyStateKeyspaceFor 以 Scope 的偏好身份选键族：PreferenceID 非空 → v2，
// 否则 v1（历史路径）。
func latencyStateKeyspaceFor(scope LatencyDegradationScope) latencyStateKeyspace {
	if strings.TrimSpace(scope.PreferenceID) != "" {
		return latencyStateKeyspaceV2
	}
	return latencyStateKeyspaceV1
}

// latencyStateKeyspaceOfStateKey 从 state 键的版本段反推键族（mutation lock、
// probe claim、索引维护等以 stateKey 为唯一入参的路径用）。
func latencyStateKeyspaceOfStateKey(stateKey string) latencyStateKeyspace {
	version := stateKey
	if boundary := strings.IndexByte(stateKey, ':'); boundary >= 0 {
		version = stateKey[:boundary]
	}
	return latencyStateKeyspace{version: version}
}

func (k latencyStateKeyspace) generationKey() string     { return k.version + ":generation" }
func (k latencyStateKeyspace) allIndexKey() string       { return k.version + ":all-index" }
func (k latencyStateKeyspace) probeIndexKey() string     { return k.version + ":probe-index" }
func (k latencyStateKeyspace) allIndexLockKey() string   { return k.version + ":all-index-lock" }
func (k latencyStateKeyspace) probeIndexLockKey() string { return k.version + ":probe-index-lock" }

func (k latencyStateKeyspace) mutationLockKey(stateKey string) string {
	return k.version + ":mutation-lock:" + stateKey
}

func (k latencyStateKeyspace) probeClaimLockKey(generation, stateKey string) string {
	return k.version + ":probe-claim:" + generation + ":" + stateKey
}

// latencyStateMutationLockKey 是 v1 键族的 mutation-lock 键（历史形状锚点，
// 测试直接引用；v2 由 keyspace 方法同构构造）。
func latencyStateMutationLockKey(stateKey string) string {
	return latencyStateKeyspaceV1.mutationLockKey(stateKey)
}

// stateKey 构造 state 键：v1 保持五段历史形状；v2 在 routeStrategyID 之后插入
// preferenceID 与 configFingerprint 两段。sanitizeKeyPart 规则两代一致。
func (k latencyStateKeyspace) stateKey(scope LatencyDegradationScope, runtimeKey string) string {
	if k.version == latencyStateVersionV2 {
		return strings.Join([]string{
			k.version,
			sanitizeKeyPart(scope.SystemAccountID),
			sanitizeKeyPart(scope.RouteStrategyID),
			sanitizeKeyPart(scope.PreferenceID),
			sanitizeKeyPart(scope.ConfigFingerprint),
			sanitizeKeyPart(scope.GroupID),
			sanitizeKeyPart(runtimeKey),
		}, ":")
	}
	return strings.Join([]string{
		k.version,
		sanitizeKeyPart(scope.SystemAccountID),
		sanitizeKeyPart(scope.RouteStrategyID),
		sanitizeKeyPart(scope.GroupID),
		sanitizeKeyPart(runtimeKey),
	}, ":")
}

var latencyStateInitialGenerationEvent = LatencyGenerationEvent{
	Version:     "initial",
	PublishedAt: "1970-01-01T00:00:00.000Z",
}

// LatencyDegradationService is the normal-route latency degradation service.
// store mirrors createRuntimeStateStore('gateway-normal-route-latency-degradation').
type LatencyDegradationService struct {
	store  RuntimeStateStore
	clock  Clock
	opts   LatencyDegradationOptions
	random func() float64

	clearSlots chan struct{}
}

// NewLatencyDegradationService builds the service.
func NewLatencyDegradationService(store RuntimeStateStore, clock Clock, opts LatencyDegradationOptions) *LatencyDegradationService {
	if opts.BackgroundClearConcurrency <= 0 {
		opts.BackgroundClearConcurrency = 5_000
	}
	random := opts.Random
	return &LatencyDegradationService{
		store:      store,
		clock:      clock,
		opts:       opts,
		random:     random,
		clearSlots: make(chan struct{}, opts.BackgroundClearConcurrency),
	}
}

func (s *LatencyDegradationService) nowMs() int64 { return ClockNowMs(s.clock) }

func (s *LatencyDegradationService) jitterRandom() func() float64 {
	if s.random != nil {
		return s.random
	}
	return nil
}

func (s *LatencyDegradationService) lockRetryDelay(attempt int) {
	if s.opts.LockRetryDelay != nil {
		s.opts.LockRetryDelay(attempt)
		return
	}
	delay := time.Duration(minInt(latencyStateLockAcquireMaxDelayMs, 20+attempt*5)) * time.Millisecond
	time.Sleep(delay)
}

// ---------------------------------------------------------------------------
// Public API.

// NormalRouteLatencyDegradationScope mirrors normalRouteLatencyDegradationScope.
func NormalRouteLatencyDegradationScope(systemAccountID, routeStrategyID, groupID string) *LatencyDegradationScope {
	systemAccountID = strings.TrimSpace(systemAccountID)
	routeStrategyID = strings.TrimSpace(routeStrategyID)
	groupID = strings.TrimSpace(groupID)
	if systemAccountID == "" || routeStrategyID == "" || groupID == "" {
		return nil
	}
	return &LatencyDegradationScope{SystemAccountID: systemAccountID, RouteStrategyID: routeStrategyID, GroupID: groupID}
}

// OrderGatewayAccountsByNormalRouteLatencyDegradation mirrors the generic
// ordering entry point; accountOf projects each element onto
// SuppressibleGatewayAccount, and elements keep their identity on output.
func OrderGatewayAccountsByNormalRouteLatencyDegradation[T any](
	ctx context.Context,
	s *LatencyDegradationService,
	accounts []T,
	accountOf func(T) SuppressibleGatewayAccount,
	scope *LatencyDegradationScope,
	config *SpeedFirstRuntimeConfig,
	_ *gatewayrouting.GatewayAccountModelPriority,
) (LatencyDegradationOrderResult[T], error) {
	if scope == nil || config == nil || len(accounts) == 0 {
		return LatencyDegradationOrderResult[T]{Accounts: accounts, DegradedAccountIDs: []string{}}, nil
	}

	generation, err := s.loadLatencyStateGeneration(ctx, latencyStateKeyspaceFor(*scope))
	if err != nil {
		return LatencyDegradationOrderResult[T]{}, err
	}
	now := s.nowMs()
	type accountState struct {
		account T
		state   *latencyState
	}
	// One batched read replaces the per-account GetJSON round trips. The store
	// drivers return raws positionally aligned with keys (missing keys stay
	// nil), so per-account semantics are identical to loadLatencyState.
	keys := make([]string, len(accounts))
	for i, account := range accounts {
		projected := accountOf(account)
		keys[i] = accountLatencyStateKey(resolvedLatencyScopeForAccount(*scope, projected), projected)
	}
	rawStates, err := s.store.GetJSONMany(ctx, keys)
	if err != nil {
		return LatencyDegradationOrderResult[T]{}, err
	}
	states := make([]accountState, 0, len(accounts))
	for i, account := range accounts {
		states = append(states, accountState{account: account, state: latencyStateFromRaw(rawStates[i], generation)})
	}
	var normalAccounts, degradedAccounts []T
	degradedAccountIDs := make([]string, 0)
	for _, item := range states {
		if item.state != nil && item.state.DegradedUntilMs != nil && *item.state.DegradedUntilMs > now {
			degradedAccounts = append(degradedAccounts, item.account)
			degradedAccountIDs = append(degradedAccountIDs, accountOf(item.account).ID)
		} else {
			normalAccounts = append(normalAccounts, item.account)
		}
	}

	if len(degradedAccounts) == 0 {
		return LatencyDegradationOrderResult[T]{Accounts: accounts, Applied: false, DegradedAccountIDs: []string{}, BypassedAllDegraded: false}, nil
	}

	if len(normalAccounts) == 0 {
		return LatencyDegradationOrderResult[T]{Accounts: accounts, Applied: false, DegradedAccountIDs: degradedAccountIDs, BypassedAllDegraded: true}, nil
	}

	ordered := append(append([]T(nil), normalAccounts...), degradedAccounts...)
	return LatencyDegradationOrderResult[T]{
		Accounts:            ordered,
		Applied:             true,
		DegradedAccountIDs:  degradedAccountIDs,
		BypassedAllDegraded: false,
	}, nil
}

// RecordNormalRouteFirstByteSlow mirrors recordNormalRouteFirstByteSlowAsync.
// An empty reason falls back to the Node default.
func (s *LatencyDegradationService) RecordNormalRouteFirstByteSlow(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	scope *LatencyDegradationScope,
	config *SpeedFirstRuntimeConfig,
	reason string,
) (*LatencySlowResult, error) {
	return s.recordNormalRouteSlow(ctx, account, scope, config, reason, LatencyDimensionFirstByte, "普通路由速度优先首字等待超时")
}

// RecordNormalRouteTotalTimeSlow 是总时间维度的慢样本入口（设计 6.4）：滑窗、
// 触发、TTL、事件 ID 与探针调度与首字维度完全同构，仅计数通道不同；空
// reason 回落到总时间默认文案。
func (s *LatencyDegradationService) RecordNormalRouteTotalTimeSlow(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	scope *LatencyDegradationScope,
	config *SpeedFirstRuntimeConfig,
	reason string,
) (*LatencySlowResult, error) {
	return s.recordNormalRouteSlow(ctx, account, scope, config, reason, LatencyDimensionTotalTime, "普通路由速度优先总时间等待超时")
}

func (s *LatencyDegradationService) recordNormalRouteSlow(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	scope *LatencyDegradationScope,
	config *SpeedFirstRuntimeConfig,
	reason string,
	dimension string,
	defaultReason string,
) (*LatencySlowResult, error) {
	if scope == nil || config == nil {
		return nil, nil
	}
	if reason == "" {
		reason = defaultReason
	}
	// 键与存储 Scope 必须同源（设计 3.6 / B25）：state.Scope 落盘解析后的
	// scope，jobs 探针按存储 Scope 重建键、按 Scope.GroupID 找凭据。
	resolvedScope := resolvedLatencyScopeForAccount(*scope, account)
	key, err := accountLatencyStateKeyChecked(resolvedScope, account)
	if err != nil {
		return nil, err
	}
	// 手动模式在锁外解析一次（总开关设计 §5）：降级可建立，探针调度面不写。
	backgroundProbeDisabled := s.backgroundProbeDisabled(ctx, account)
	generation, err := s.loadLatencyStateGeneration(ctx, latencyStateKeyspaceFor(resolvedScope))
	if err != nil {
		return nil, err
	}
	var result *LatencySlowResult
	ok, err := s.withLatencyStateMutationLock(ctx, key, generation, func() (bool, error) {
		value, err := s.recordNormalRouteSlowLocked(ctx, account, resolvedScope, *config, reason, key, generation, dimension, backgroundProbeDisabled)
		if err != nil {
			return false, err
		}
		result = value
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return result, nil
}

// backgroundProbeDisabled 解析手动模式开关位；nil 或查询失败 fail-open
// （false = 允许探测调度写入，与 flag=1 默认路径逐字节一致）。
func (s *LatencyDegradationService) backgroundProbeDisabled(ctx context.Context, account SuppressibleGatewayAccount) bool {
	if s.opts.BackgroundProbeDisabled == nil {
		return false
	}
	disabled, err := s.opts.BackgroundProbeDisabled(ctx, account)
	if err != nil {
		return false
	}
	return disabled
}

func (s *LatencyDegradationService) recordNormalRouteSlowLocked(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	scope LatencyDegradationScope,
	config SpeedFirstRuntimeConfig,
	reason string,
	key string,
	generation string,
	dimension string,
	backgroundProbeDisabled bool,
) (*LatencySlowResult, error) {
	now := s.nowMs()
	current, err := s.loadLatencyState(ctx, key, generation)
	if err != nil {
		return nil, err
	}
	slowWindowMs := maxInt64(60, config.SlowWindowSeconds) * 1000
	// 慢样本按维度选通道（设计 6.4）：窗口判定与计数取所选通道的
	// First*SlowAtMs / *SlowCount，两个通道互不干扰。
	slowCount := int64(1)
	withinWindow := false
	if current != nil {
		var channelFirstSlowAtMs int64
		if dimension == LatencyDimensionTotalTime {
			channelFirstSlowAtMs = current.FirstTotalTimeSlowAtMs
		} else {
			channelFirstSlowAtMs = current.FirstSlowAtMs
		}
		if now-channelFirstSlowAtMs <= slowWindowMs {
			withinWindow = true
			if dimension == LatencyDimensionTotalTime {
				slowCount = current.TotalTimeSlowCount + 1
			} else {
				slowCount = current.SlowCount + 1
			}
		}
	}
	currentStillDegraded := current != nil && current.DegradedUntilMs != nil && *current.DegradedUntilMs > now
	triggeredDegraded := slowCount >= config.SlowTriggerCount
	degraded := triggeredDegraded || currentStillDegraded
	// A degradation event has one bounded TTL. Repeated slow samples while it
	// is already degraded must not keep moving the recovery deadline forward.
	var degradedUntilMs *int64
	if triggeredDegraded {
		if currentStillDegraded {
			degradedUntilMs = current.DegradedUntilMs
		} else {
			degradedUntilMs = int64Ptr(now + maxInt64(60, config.DegradedTTLSeconds)*1000)
		}
	} else if current != nil {
		degradedUntilMs = current.DegradedUntilMs
	}
	var degradationEventID *string
	if degraded {
		if currentStillDegraded && current.DegradationEventID != nil {
			degradationEventID = current.DegradationEventID
		} else {
			degradationEventID = stringPtr(NewUUID())
		}
	}
	var nextProbeAtMs *int64
	// 手动模式不写探针调度（总开关设计 §5）：不新建 nextProbeAt，也不沿写
	// 既有值；降级排序与 TTL 恢复语义不变。
	if !backgroundProbeDisabled {
		if triggeredDegraded {
			nextProbeAtMs = int64Ptr(now + s.nextRecoveryProbeDelayMs())
		} else if current != nil {
			nextProbeAtMs = current.NextProbeAtMs
		}
	}
	runtimeKey, err := GatewayAccountRuntimeKey(account)
	if err != nil {
		return nil, err
	}
	state := latencyState{
		Generation:                     generation,
		AccountID:                      account.ID,
		AccountName:                    optionalAccountName(account),
		RuntimeKey:                     runtimeKey,
		Scope:                          scope,
		Config:                         config,
		DegradationEventID:             degradationEventID,
		DegradedUntilMs:                degradedUntilMs,
		SuccessCount:                   0,
		RecoveryProbeRoundAttemptCount: int64Ptr(0),
		RecoveryProbeRoundSuccessCount: int64Ptr(0),
		NextProbeAtMs:                  nextProbeAtMs,
		Reason:                         reason,
	}
	// Dimension 只在触发降级时更新为最近触发维度（两个维度都慢的号按最新
	// 触发维度走恢复路径）；观察期样本沿用既有值，新状态保持空串。
	if triggeredDegraded {
		state.Dimension = dimension
	} else if current != nil {
		state.Dimension = current.Dimension
	}
	if dimension == LatencyDimensionTotalTime {
		state.TotalTimeSlowCount = slowCount
		state.FirstTotalTimeSlowAtMs = now
		state.LastTotalTimeSlowAtMs = now
		if current != nil {
			state.SlowCount = current.SlowCount
			state.FirstSlowAtMs = current.FirstSlowAtMs
			state.LastSlowAtMs = current.LastSlowAtMs
			if withinWindow {
				state.FirstTotalTimeSlowAtMs = current.FirstTotalTimeSlowAtMs
			}
		}
	} else {
		state.SlowCount = slowCount
		state.FirstSlowAtMs = now
		state.LastSlowAtMs = now
		if current != nil {
			state.TotalTimeSlowCount = current.TotalTimeSlowCount
			state.FirstTotalTimeSlowAtMs = current.FirstTotalTimeSlowAtMs
			state.LastTotalTimeSlowAtMs = current.LastTotalTimeSlowAtMs
			if withinWindow {
				state.FirstSlowAtMs = current.FirstSlowAtMs
			}
		}
	}
	var ttlMs int64
	if degraded {
		if currentStillDegraded {
			ttlMs = latencyStateRemainingTTLMs(degradedUntilMs, now)
		} else {
			ttlMs = latencyStateTTLMs(config, true)
		}
	} else {
		ttlMs = latencyStateTTLMs(config, false)
	}
	var previous *latencyState
	if current != nil {
		previousCopy := *current
		previous = &previousCopy
	}
	// 手动模式不进 probe-index（恢复探针无候选）；all-index 仍维护，降级排序
	// 读取按直键不受影响。
	if err := s.writeLatencyStateAndIndexesStrict(ctx, key, previous, state, ttlMs, degraded && !backgroundProbeDisabled); err != nil {
		return nil, err
	}
	return &LatencySlowResult{
		AccountID:            account.ID,
		SlowCount:            slowCount,
		Degraded:             degraded,
		DegradedUntil:        isoPtr(degradedUntilMs),
		RecoverySuccessCount: 0,
		NextProbeAt:          isoPtr(nextProbeAtMs),
	}, nil
}

// RecordNormalRouteFirstByteSuccess mirrors recordNormalRouteFirstByteSuccessAsync.
func (s *LatencyDegradationService) RecordNormalRouteFirstByteSuccess(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	scope *LatencyDegradationScope,
	config *SpeedFirstRuntimeConfig,
	firstByteMs *int64,
) (*LatencySuccessResult, error) {
	if scope == nil || config == nil || firstByteMs == nil || *firstByteMs > config.FirstByteDeadlineMs {
		return nil, nil
	}
	return s.recordNormalRouteSuccess(ctx, account, scope, config, LatencyDimensionFirstByte, false, nil)
}

// RecordNormalRouteTotalTimeSuccess 是总时间维度的达标（恢复）样本入口
// （设计 6.5）：effectiveDeadlineMs 由调用方按 attempt 装配时选定的档位传入
// （普通档或压缩档），这里只做门槛与维度双向过滤后的恢复累计；total_time
// 维度不参与探针恢复，仅累计真实流量达标样本。
func (s *LatencyDegradationService) RecordNormalRouteTotalTimeSuccess(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	scope *LatencyDegradationScope,
	config *SpeedFirstRuntimeConfig,
	effectiveDeadlineMs int64,
	elapsedMs int64,
) (*LatencySuccessResult, error) {
	if scope == nil || config == nil || elapsedMs > effectiveDeadlineMs {
		return nil, nil
	}
	return s.recordNormalRouteSuccess(ctx, account, scope, config, LatencyDimensionTotalTime, false, nil)
}

func (s *LatencyDegradationService) recordNormalRouteSuccess(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	scope *LatencyDegradationScope,
	config *SpeedFirstRuntimeConfig,
	dimension string,
	clearOnSuccess bool,
	candidate *LatencyProbeCandidate,
) (*LatencySuccessResult, error) {
	// 读键与写慢样本同源解析（设计 3.6 / B25）：恢复观察只 clone 既有
	// state（Scope 不变），键必须落在同一存储 Scope 的组分量上。
	key, err := accountLatencyStateKeyChecked(resolvedLatencyScopeForAccount(*scope, account), account)
	if err != nil {
		return nil, err
	}
	// 手动模式在锁外解析一次（总开关设计 §5）：真实流量达标恢复保留，
	// 探针调度面（nextProbeAt）不写。
	backgroundProbeDisabled := s.backgroundProbeDisabled(ctx, account)
	generation, err := s.loadLatencyStateGeneration(ctx, latencyStateKeyspaceFor(*scope))
	if err != nil {
		return nil, err
	}
	var result *LatencySuccessResult
	ok, err := s.withLatencyStateMutationLock(ctx, key, generation, func() (bool, error) {
		value, err := s.recordNormalRouteSuccessLocked(ctx, account, *config, key, generation, dimension, clearOnSuccess, candidate, backgroundProbeDisabled)
		if err != nil {
			return false, err
		}
		result = value
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return result, nil
}

// RecordNormalRouteRecoveryProbeSuccess mirrors
// recordNormalRouteRecoveryProbeSuccessAsync: two short-spaced background
// health checks restore priority without waiting for user traffic.
func (s *LatencyDegradationService) RecordNormalRouteRecoveryProbeSuccess(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	candidate LatencyProbeCandidate,
	firstByteMs *int64,
) (*LatencySuccessResult, error) {
	if firstByteMs == nil || *firstByteMs > candidate.Config.FirstByteDeadlineMs {
		return nil, nil
	}
	runtimeKey, err := GatewayAccountRuntimeKey(account)
	if err != nil {
		return nil, err
	}
	if account.ID != candidate.AccountID || runtimeKey != candidate.RuntimeKey {
		return nil, nil
	}
	var result *LatencySuccessResult
	ok, err := s.withLatencyStateMutationLock(ctx, candidate.StateKey, candidate.Generation, func() (bool, error) {
		value, err := s.recordNormalRouteSuccessLocked(ctx, account, candidate.Config, candidate.StateKey, candidate.Generation, LatencyDimensionFirstByte, true, &candidate, false)
		if err != nil {
			return false, err
		}
		result = value
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return result, nil
}

func (s *LatencyDegradationService) recordNormalRouteSuccessLocked(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	config SpeedFirstRuntimeConfig,
	key string,
	generation string,
	dimension string,
	clearOnSuccess bool,
	candidate *LatencyProbeCandidate,
	backgroundProbeDisabled bool,
) (*LatencySuccessResult, error) {
	current, err := s.loadLatencyState(ctx, key, generation)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, nil
	}
	if candidate != nil && !latencyProbeCandidateMatchesState(*candidate, *current) {
		return nil, nil
	}
	now := s.nowMs()
	if current.DegradedUntilMs != nil && *current.DegradedUntilMs <= now {
		// 过期降级清理与维度无关（设计 6.5）：任一维度的达标样本都先兜底清理
		// 已过期降级，避免残留状态依赖 Redis TTL 才消失。
		if err := s.deleteLatencyStateAndIndexesStrict(ctx, key); err != nil {
			return nil, err
		}
		return &LatencySuccessResult{
			AccountID:                    account.ID,
			Cleared:                      true,
			RecoverySuccessCount:         0,
			RequiredRecoverySuccessCount: config.RecoverySuccessCount,
		}, nil
	}
	if current.DegradedUntilMs == nil {
		// 观察期（未降级）达标样本只清自己维度的慢样本计数（设计 6.4 双通道）：
		// 首字快但总时长慢的请求上，一个维度的达标与另一维度的慢样本并存是
		// 常态——达标样本顺手删除整个 state 会把另一维度正在累计的观察期计数
		// 一起清零，该维度永远攒不满 slowTriggerCount。两通道计数都归零才删除
		// state（单通道场景行为与既有语义一致：达标清观察期）。
		next := current.clone()
		if dimension == LatencyDimensionFirstByte {
			next.SlowCount = 0
			next.FirstSlowAtMs = 0
			next.LastSlowAtMs = 0
		} else {
			next.TotalTimeSlowCount = 0
			next.FirstTotalTimeSlowAtMs = 0
			next.LastTotalTimeSlowAtMs = 0
		}
		if next.SlowCount == 0 && next.TotalTimeSlowCount == 0 {
			if err := s.deleteLatencyStateAndIndexesStrict(ctx, key); err != nil {
				return nil, err
			}
			return &LatencySuccessResult{
				AccountID:                    account.ID,
				Cleared:                      true,
				RecoverySuccessCount:         0,
				RequiredRecoverySuccessCount: config.RecoverySuccessCount,
			}, nil
		}
		remainingWindowMs := maxInt64(60, config.SlowWindowSeconds) * 1000
		if err := s.store.SetJSON(ctx, key, next, remainingWindowMs); err != nil {
			return nil, err
		}
		return &LatencySuccessResult{
			AccountID:                    account.ID,
			Cleared:                      false,
			RecoverySuccessCount:         0,
			RequiredRecoverySuccessCount: config.RecoverySuccessCount,
		}, nil
	}
	// 维度双向过滤（设计 6.5）：达标样本只推进与降级维度同向的恢复；另一
	// 维度的达标样本是中性样本，不推进 SuccessCount、不动探针轮次、不改
	// NextProbeAtMs。
	if latencyDimensionOf(*current) != dimension {
		return nil, nil
	}
	successCount := current.SuccessCount
	if !clearOnSuccess {
		successCount = current.SuccessCount + 1
	}
	if clearOnSuccess {
		recoveryProbeRoundAttemptCount := recoveryProbeRoundAttempts(*current) + 1
		recoveryProbeRoundSuccessCount := recoveryProbeRoundSuccesses(*current) + 1
		if recoveryProbeRoundAttemptCount >= normalRouteRecoveryProbeRoundSize &&
			recoveryProbeRoundSuccessCount == normalRouteRecoveryProbeRoundSize {
			if err := s.deleteLatencyStateAndIndexesStrict(ctx, key); err != nil {
				return nil, err
			}
			return &LatencySuccessResult{
				AccountID:                    account.ID,
				Cleared:                      true,
				RecoverySuccessCount:         recoveryProbeRoundSuccessCount,
				RequiredRecoverySuccessCount: normalRouteRecoveryProbeRoundSize,
			}, nil
		}
		next := current.clone()
		if recoveryProbeRoundAttemptCount >= normalRouteRecoveryProbeRoundSize {
			next.RecoveryProbeRoundAttemptCount = int64Ptr(0)
			next.RecoveryProbeRoundSuccessCount = int64Ptr(0)
		} else {
			next.RecoveryProbeRoundAttemptCount = int64Ptr(recoveryProbeRoundAttemptCount)
			next.RecoveryProbeRoundSuccessCount = int64Ptr(recoveryProbeRoundSuccessCount)
		}
		// A user request and a background health probe are separate recovery
		// mechanisms. Only the latter contributes to this two-probe window.
		next.SuccessCount = current.SuccessCount
		// 手动模式不写探针调度（总开关设计 §5）；该臂为恢复探针驱动，手动账户
		// 不会到达，守卫只为开关位语义完整。
		if !backgroundProbeDisabled {
			next.NextProbeAtMs = int64Ptr(now + s.nextRecoveryProbeDelayMs())
		}
		if err := s.store.SetJSON(ctx, key, next, latencyStateRemainingTTLMs(current.DegradedUntilMs, now)); err != nil {
			return nil, err
		}
		return &LatencySuccessResult{
			AccountID:                    account.ID,
			Cleared:                      false,
			RecoverySuccessCount:         recoveryProbeRoundSuccessCount,
			RequiredRecoverySuccessCount: normalRouteRecoveryProbeRoundSize,
		}, nil
	}
	requiredRecoverySuccessCount := config.RecoverySuccessCount
	recovered := successCount >= config.RecoverySuccessCount
	if recovered {
		if err := s.deleteLatencyStateAndIndexesStrict(ctx, key); err != nil {
			return nil, err
		}
		return &LatencySuccessResult{
			AccountID:                    account.ID,
			Cleared:                      true,
			RecoverySuccessCount:         successCount,
			RequiredRecoverySuccessCount: requiredRecoverySuccessCount,
		}, nil
	}
	// 真实流量达标推进恢复计数（总开关设计 §5：降级按 TTL 或真实流量恢复）；
	// 手动模式只跳过探针调度写入，恢复语义不变。
	var nextProbeAtMs *int64
	if !backgroundProbeDisabled {
		if recoveryProbeRoundAttempts(*current) > 0 {
			nextProbeAtMs = current.NextProbeAtMs
		} else {
			nextProbeAtMs = int64Ptr(now + s.nextProbeDelayMs(config))
		}
	}
	next := current.clone()
	next.SuccessCount = successCount
	next.NextProbeAtMs = nextProbeAtMs
	if err := s.store.SetJSON(ctx, key, next, latencyStateRemainingTTLMs(current.DegradedUntilMs, now)); err != nil {
		return nil, err
	}
	return &LatencySuccessResult{
		AccountID:                    account.ID,
		Cleared:                      false,
		RecoverySuccessCount:         successCount,
		RequiredRecoverySuccessCount: requiredRecoverySuccessCount,
	}, nil
}

// IsNormalRouteAccountLatencyDegraded mirrors isNormalRouteAccountLatencyDegradedAsync.
func (s *LatencyDegradationService) IsNormalRouteAccountLatencyDegraded(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	scope *LatencyDegradationScope,
) (bool, error) {
	if scope == nil {
		return false, nil
	}
	generation, err := s.loadLatencyStateGeneration(ctx, latencyStateKeyspaceFor(*scope))
	if err != nil {
		return false, err
	}
	state, err := s.loadLatencyState(ctx, accountLatencyStateKey(resolvedLatencyScopeForAccount(*scope, account), account), generation)
	if err != nil {
		return false, err
	}
	return state != nil && state.DegradedUntilMs != nil && *state.DegradedUntilMs > s.nowMs(), nil
}

// ListNormalRouteLatencyProbeCandidates mirrors listNormalRouteLatencyProbeCandidatesAsync.
func (s *LatencyDegradationService) ListNormalRouteLatencyProbeCandidates(
	ctx context.Context,
	limit *int,
	now *int64,
) ([]LatencyProbeCandidate, error) {
	normalizedLimit := 20
	if limit != nil {
		normalizedLimit = normalizePositiveInteger(limit, 20, 1, 100)
	}
	nowMs := s.nowMs()
	if now != nil {
		nowMs = *now
	}
	type candidateWithOrder struct {
		candidate     LatencyProbeCandidate
		nextProbeAtMs int64
	}
	var candidates []candidateWithOrder
	// 双代扫描：v1（策略段）与 v2（偏好段）各自持有 probe-index 与 generation
	// marker，候选合并后按同一排序规则统一截断。
	for _, keyspace := range latencyStateKeyspaces() {
		keys, err := s.loadLatencyStateIndexKeys(ctx, keyspace.probeIndexKey())
		if err != nil {
			return nil, err
		}
		if len(keys) == 0 {
			continue
		}
		generation, err := s.loadLatencyStateGeneration(ctx, keyspace)
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			state, err := s.loadLatencyState(ctx, key, generation)
			if err != nil {
				return nil, err
			}
			if state == nil || state.DegradedUntilMs == nil || *state.DegradedUntilMs <= nowMs {
				continue
			}
			// total_time 维度不参与探针恢复（设计 6.5）：探针请求规模小，测不出
			// 容量型慢，不探、不清理、不续租，只认真实流量达标或 TTL 过期。
			if latencyDimensionOf(*state) == LatencyDimensionTotalTime {
				continue
			}
			if state.NextProbeAtMs == nil || *state.NextProbeAtMs > nowMs {
				continue
			}
			candidate, ok := probeCandidateFromState(key, *state)
			if ok {
				candidates = append(candidates, candidateWithOrder{candidate: candidate, nextProbeAtMs: *state.NextProbeAtMs})
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.nextProbeAtMs != right.nextProbeAtMs {
			return left.nextProbeAtMs < right.nextProbeAtMs
		}
		return strings.Compare(left.candidate.AccountID, right.candidate.AccountID) < 0
	})
	if len(candidates) > normalizedLimit {
		candidates = candidates[:normalizedLimit]
	}
	output := make([]LatencyProbeCandidate, 0, len(candidates))
	for _, item := range candidates {
		output = append(output, item.candidate)
	}
	return output, nil
}

// ListNormalRouteLatencyDegradedRuntimeInput mirrors the runtime query input.
type ListNormalRouteLatencyDegradedRuntimeInput struct {
	SystemAccountID  *string
	RouteStrategyIDs []string
	Now              *int64
}

// ListNormalRouteLatencyDegradedRuntime mirrors
// listNormalRouteLatencyDegradedRuntimeAsync: reads strictly from the probe
// index so observation-period or expired states are never surfaced.
func (s *LatencyDegradationService) ListNormalRouteLatencyDegradedRuntime(
	ctx context.Context,
	input ListNormalRouteLatencyDegradedRuntimeInput,
) ([]DegradedRuntimeItem, error) {
	var systemAccountID string
	if input.SystemAccountID != nil {
		systemAccountID = strings.TrimSpace(*input.SystemAccountID)
	}
	routeStrategyIDs := normalizedRouteStrategyIDs(input.RouteStrategyIDs)
	if len(routeStrategyIDs) == 0 {
		return []DegradedRuntimeItem{}, nil
	}
	if len(routeStrategyIDs) > 50 {
		return nil, errors.New("普通路由速度优先运行态查询最多支持 50 个策略路由")
	}

	nowMs := s.nowMs()
	if input.Now != nil {
		nowMs = *input.Now
	}
	routeStrategyIDSet := make(map[string]struct{}, len(routeStrategyIDs))
	for _, id := range routeStrategyIDs {
		routeStrategyIDSet[id] = struct{}{}
	}
	items := make([]DegradedRuntimeItem, 0)
	// 双代扫描：v1（策略段）与 v2（偏好段）各自持有 probe-index 与 generation
	// marker；条目合并透出，同账号两代并存不去重（来源字段区分命名空间）。
	for _, keyspace := range latencyStateKeyspaces() {
		keys, err := s.loadLatencyStateIndexKeys(ctx, keyspace.probeIndexKey())
		if err != nil {
			return nil, err
		}
		if len(keys) == 0 {
			continue
		}
		generation, err := s.loadLatencyStateGeneration(ctx, keyspace)
		if err != nil {
			return nil, err
		}
		rawStates, err := s.store.GetJSONMany(ctx, keys)
		if err != nil {
			return nil, err
		}
		for _, raw := range rawStates {
			if raw == nil {
				continue
			}
			state, ok := decodeLatencyState(raw)
			if !ok {
				continue
			}
			if !isCurrentLatencyState(state, generation) {
				continue
			}
			if state.DegradedUntilMs == nil || *state.DegradedUntilMs <= nowMs {
				continue
			}
			if systemAccountID != "" && state.Scope.SystemAccountID != systemAccountID {
				continue
			}
			if _, matched := routeStrategyIDSet[state.Scope.RouteStrategyID]; !matched {
				continue
			}
			item := DegradedRuntimeItem{
				AccountID:                      state.AccountID,
				AccountName:                    state.AccountName,
				ScopeRouteStrategyID:           state.Scope.RouteStrategyID,
				ScopeGroupID:                   state.Scope.GroupID,
				SlowCount:                      state.SlowCount,
				SlowTriggerCount:               state.Config.SlowTriggerCount,
				SlowWindowSeconds:              state.Config.SlowWindowSeconds,
				DegradedUntil:                  ISOStringMs(*state.DegradedUntilMs),
				NextProbeAt:                    isoPtr(state.NextProbeAtMs),
				RecoverySuccessCount:           state.SuccessCount,
				RequiredRecoverySuccessCount:   state.Config.RecoverySuccessCount,
				RecoveryProbeRoundAttemptCount: recoveryProbeRoundAttempts(state),
				RecoveryProbeRoundSuccessCount: recoveryProbeRoundSuccesses(state),
				Reason:                         state.Reason,
				Dimension:                      latencyDimensionOf(state),
				Source:                         latencyRuntimeSourceOf(state.Scope),
				PreferenceID:                   state.Scope.PreferenceID,
			}
			items = append(items, item)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if c := strings.Compare(left.DegradedUntil, right.DegradedUntil); c != 0 {
			return c < 0
		}
		if c := strings.Compare(left.AccountID, right.AccountID); c != 0 {
			return c < 0
		}
		if c := strings.Compare(left.ScopeRouteStrategyID, right.ScopeRouteStrategyID); c != 0 {
			return c < 0
		}
		return strings.Compare(left.ScopeGroupID, right.ScopeGroupID) < 0
	})
	return items, nil
}

// ---------------------------------------------------------------------------
// Probe claims.

// AcquireNormalRouteLatencyProbeClaim mirrors acquireNormalRouteLatencyProbeClaimAsync.
func (s *LatencyDegradationService) AcquireNormalRouteLatencyProbeClaim(
	ctx context.Context,
	candidate LatencyProbeCandidate,
) (*LatencyProbeClaim, error) {
	token := NewUUID()
	lockKey := normalRouteLatencyProbeClaimLockKey(candidate)
	acquired, err := s.store.AcquireLock(ctx, lockKey, latencyProbeClaimTTLMs, token)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, nil
	}
	return &LatencyProbeClaim{
		StateKey:   candidate.StateKey,
		Generation: candidate.Generation,
		LockKey:    lockKey,
		Token:      token,
	}, nil
}

// RenewNormalRouteLatencyProbeClaim mirrors renewNormalRouteLatencyProbeClaimAsync.
func (s *LatencyDegradationService) RenewNormalRouteLatencyProbeClaim(ctx context.Context, claim LatencyProbeClaim) (bool, error) {
	return s.store.RenewLock(ctx, claim.LockKey, latencyProbeClaimTTLMs, claim.Token)
}

// ReleaseNormalRouteLatencyProbeClaim mirrors releaseNormalRouteLatencyProbeClaimAsync.
func (s *LatencyDegradationService) ReleaseNormalRouteLatencyProbeClaim(ctx context.Context, claim LatencyProbeClaim) error {
	return s.store.ReleaseLock(ctx, claim.LockKey, claim.Token)
}

// RecordNormalRouteProbeFailure mirrors recordNormalRouteProbeFailureAsync.
func (s *LatencyDegradationService) RecordNormalRouteProbeFailure(
	ctx context.Context,
	candidate LatencyProbeCandidate,
	reason string,
) (*LatencySlowResult, error) {
	if reason == "" {
		reason = "普通路由速度优先恢复探针未达标"
	}
	var result *LatencySlowResult
	ok, err := s.withLatencyStateMutationLock(ctx, candidate.StateKey, candidate.Generation, func() (bool, error) {
		value, err := s.recordNormalRouteProbeFailureLocked(ctx, candidate, reason)
		if err != nil {
			return false, err
		}
		result = value
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return result, nil
}

func (s *LatencyDegradationService) recordNormalRouteProbeFailureLocked(
	ctx context.Context,
	candidate LatencyProbeCandidate,
	reason string,
) (*LatencySlowResult, error) {
	current, err := s.loadLatencyState(ctx, candidate.StateKey, candidate.Generation)
	if err != nil {
		return nil, err
	}
	now := s.nowMs()
	if current == nil {
		return nil, nil
	}
	if !latencyProbeCandidateMatchesState(candidate, *current) {
		return nil, nil
	}
	if current.DegradedUntilMs == nil || *current.DegradedUntilMs <= now {
		if err := s.deleteLatencyStateAndIndexesStrict(ctx, candidate.StateKey); err != nil {
			return nil, err
		}
		return nil, nil
	}
	config := current.Config
	recoveryProbeRoundAttemptCount := recoveryProbeRoundAttempts(*current) + 1
	recoveryProbeRoundSuccessCount := recoveryProbeRoundSuccesses(*current)
	recoveryProbeRoundComplete := recoveryProbeRoundAttemptCount >= normalRouteRecoveryProbeRoundSize
	// Only FF renews the lease. A mixed pair is deliberately discarded, so it
	// cannot be combined with a later result to manufacture a double failure.
	degradedUntilMs := current.DegradedUntilMs
	if recoveryProbeRoundComplete && recoveryProbeRoundSuccessCount == 0 {
		degradedUntilMs = int64Ptr(maxInt64(*current.DegradedUntilMs, now+maxInt64(60, config.DegradedTTLSeconds)*1000))
	}
	nextProbeAtMs := int64Ptr(now + s.nextRecoveryProbeDelayMs())
	slowCount := maxInt64(current.SlowCount, config.SlowTriggerCount)
	state := current.clone()
	state.Config = config
	state.LastSlowAtMs = now
	state.SlowCount = slowCount
	state.DegradedUntilMs = degradedUntilMs
	// Background probe outcomes must not alter the three-request user-traffic
	// debounce. They have an independent, exactly-two-attempt round.
	state.SuccessCount = current.SuccessCount
	if recoveryProbeRoundComplete {
		state.RecoveryProbeRoundAttemptCount = int64Ptr(0)
		state.RecoveryProbeRoundSuccessCount = int64Ptr(0)
	} else {
		state.RecoveryProbeRoundAttemptCount = int64Ptr(recoveryProbeRoundAttemptCount)
		state.RecoveryProbeRoundSuccessCount = int64Ptr(recoveryProbeRoundSuccessCount)
	}
	state.NextProbeAtMs = nextProbeAtMs
	state.Reason = reason
	if err := s.writeLatencyStateAndIndexesStrict(ctx, candidate.StateKey, current, state, latencyStateRemainingTTLMs(degradedUntilMs, now), true); err != nil {
		return nil, err
	}
	return &LatencySlowResult{
		AccountID:            current.AccountID,
		SlowCount:            slowCount,
		Degraded:             true,
		DegradedUntil:        stringPtr(ISOStringMs(*degradedUntilMs)),
		RecoverySuccessCount: current.SuccessCount,
		NextProbeAt:          stringPtr(ISOStringMs(*nextProbeAtMs)),
	}, nil
}

// DeferNormalRouteLatencyProbeCandidate mirrors deferNormalRouteLatencyProbeCandidateAsync.
func (s *LatencyDegradationService) DeferNormalRouteLatencyProbeCandidate(
	ctx context.Context,
	candidate LatencyProbeCandidate,
) (bool, error) {
	deferred := false
	ok, err := s.withLatencyStateMutationLock(ctx, candidate.StateKey, candidate.Generation, func() (bool, error) {
		current, err := s.loadLatencyState(ctx, candidate.StateKey, candidate.Generation)
		if err != nil {
			return false, err
		}
		now := s.nowMs()
		if current == nil {
			return true, nil
		}
		if !latencyProbeCandidateMatchesState(candidate, *current) {
			return true, nil
		}
		if current.DegradedUntilMs == nil || *current.DegradedUntilMs <= now {
			if err := s.deleteLatencyStateAndIndexesStrict(ctx, candidate.StateKey); err != nil {
				return false, err
			}
			return true, nil
		}
		config := current.Config
		state := current.clone()
		state.Config = config
		state.RecoveryProbeRoundAttemptCount = int64Ptr(0)
		state.RecoveryProbeRoundSuccessCount = int64Ptr(0)
		state.NextProbeAtMs = int64Ptr(now + s.nextRecoveryProbeDelayMs())
		if err := s.writeLatencyStateAndIndexesStrict(ctx, candidate.StateKey, current, state, latencyStateRemainingTTLMs(state.DegradedUntilMs, now), true); err != nil {
			return false, err
		}
		deferred = true
		return true, nil
	})
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return deferred, nil
}

// DiscardNormalRouteLatencyProbeCandidate mirrors discardNormalRouteLatencyProbeCandidateAsync.
func (s *LatencyDegradationService) DiscardNormalRouteLatencyProbeCandidate(
	ctx context.Context,
	candidate LatencyProbeCandidate,
) error {
	_, err := s.withLatencyStateMutationLock(ctx, candidate.StateKey, candidate.Generation, func() (bool, error) {
		current, err := s.loadLatencyState(ctx, candidate.StateKey, candidate.Generation)
		if err != nil {
			return false, err
		}
		if current == nil {
			return true, nil
		}
		if !latencyProbeCandidateMatchesState(candidate, *current) {
			return true, nil
		}
		if err := s.deleteLatencyStateAndIndexesStrict(ctx, candidate.StateKey); err != nil {
			return false, err
		}
		return true, nil
	})
	return err
}

// ---------------------------------------------------------------------------
// Clears.

// ClearNormalRouteLatencyDegradationForRouteStrategy mirrors
// clearNormalRouteLatencyDegradationForRouteStrategyAsync. 按策略段清理必须
// 同时覆盖 v1 与 v2 两代中该策略段的状态（契约：清理谓词跨代）。
func (s *LatencyDegradationService) ClearNormalRouteLatencyDegradationForRouteStrategy(
	ctx context.Context,
	routeStrategyID string,
) (int64, error) {
	normalizedRouteStrategyID := strings.TrimSpace(routeStrategyID)
	if normalizedRouteStrategyID == "" {
		return 0, nil
	}
	return s.clearAcrossLatencyKeyspaces(ctx, func(state latencyState) bool {
		return state.Scope.RouteStrategyID == normalizedRouteStrategyID
	})
}

// clearAcrossLatencyKeyspaces 对 v1/v2 两代键族分别按当前代际执行谓词清理，
// 返回两代合计清理数；任一代失败聚合报错（部分成功的结果不静默丢弃）。
func (s *LatencyDegradationService) clearAcrossLatencyKeyspaces(
	ctx context.Context,
	predicate func(latencyState) bool,
) (int64, error) {
	var totalCleared int64
	var errs []error
	for _, keyspace := range latencyStateKeyspaces() {
		keys, err := s.loadLatencyStateIndexKeys(ctx, keyspace.allIndexKey())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(keys) == 0 {
			continue
		}
		generation, err := s.loadLatencyStateGeneration(ctx, keyspace)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		cleared, err := s.clearCurrentGenerationLatencyStateKeys(ctx, keys, keyspace, generation, predicate)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		totalCleared += cleared
	}
	if len(errs) > 0 {
		return totalCleared, errors.Join(errs...)
	}
	return totalCleared, nil
}

// ClearAllNormalRouteLatencyDegradation mirrors clearAllNormalRouteLatencyDegradationAsync.
// The bool result is false when the generation CAS attempts are exhausted
// (Node returns false without throwing). The generation bump applies to both
// keyspace generations (v1/v2): a global invalidation must expire states in
// both namespaces.
func (s *LatencyDegradationService) ClearAllNormalRouteLatencyDegradation(
	ctx context.Context,
	event LatencyGenerationEvent,
) (bool, error) {
	normalizedEvent, err := normalizeLatencyGenerationEvent(event)
	if err != nil {
		return false, err
	}
	for _, keyspace := range latencyStateKeyspaces() {
		applied, err := s.bumpLatencyGenerationEventForKeyspace(ctx, keyspace, normalizedEvent)
		if err != nil {
			return false, err
		}
		if !applied {
			return false, nil
		}
	}
	return true, nil
}

// bumpLatencyGenerationEventForKeyspace 对单代键族执行 generation 事件的
// compare-and-set 推进（原 ClearAll 主体按 keyspace 提取）。
func (s *LatencyDegradationService) bumpLatencyGenerationEventForKeyspace(
	ctx context.Context,
	keyspace latencyStateKeyspace,
	normalizedEvent LatencyGenerationEvent,
) (bool, error) {
	for attempt := 0; attempt < latencyStateGenerationCASMax; attempt++ {
		current, currentRaw, err := s.loadLatencyGenerationEventRaw(ctx, keyspace)
		if err != nil {
			return false, err
		}
		if current != nil && compareLatencyGenerationEvents(normalizedEvent, *current) <= 0 {
			applied, err := s.store.CompareSetJSON(ctx, keyspace.generationKey(), currentRaw, current, latencyStateGenerationTTLMs)
			if err != nil {
				return false, err
			}
			if applied {
				return true, nil
			}
			continue
		}
		applied, err := s.store.CompareSetJSON(ctx, keyspace.generationKey(), currentRaw, normalizedEvent, latencyStateGenerationTTLMs)
		if err != nil {
			return false, err
		}
		if applied {
			return true, nil
		}
	}
	return false, nil
}

// ClearNormalRouteLatencyDegradationForAccountBindingInput mirrors the
// account-binding clear input.
type ClearNormalRouteLatencyDegradationForAccountBindingInput struct {
	SystemAccountID string
	AccountID       string
	GroupIDs        []*string
}

// ClearNormalRouteLatencyDegradationForAccountBinding mirrors
// clearNormalRouteLatencyDegradationForAccountBindingAsync.
func (s *LatencyDegradationService) ClearNormalRouteLatencyDegradationForAccountBinding(
	ctx context.Context,
	input ClearNormalRouteLatencyDegradationForAccountBindingInput,
) (int64, error) {
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	accountID := strings.TrimSpace(input.AccountID)
	groupIDs := make(map[string]struct{})
	for _, groupID := range input.GroupIDs {
		if groupID == nil {
			continue
		}
		trimmed := strings.TrimSpace(*groupID)
		if trimmed == "" {
			continue
		}
		groupIDs[trimmed] = struct{}{}
	}
	if systemAccountID == "" || accountID == "" || len(groupIDs) == 0 {
		return 0, nil
	}
	// 账户绑定级清理同样跨 v1/v2 两代（同账号在偏好路径的状态一并清除）。
	return s.clearAcrossLatencyKeyspaces(ctx, func(state latencyState) bool {
		if _, matched := groupIDs[state.Scope.GroupID]; !matched {
			return false
		}
		return state.Scope.SystemAccountID == systemAccountID &&
			(state.AccountID == accountID || RuntimeAccountIDFromKey(state.RuntimeKey) == accountID)
	})
}

// ClearNormalRouteLatencyDegradationForAccountInput mirrors the account clear
// input.
type ClearNormalRouteLatencyDegradationForAccountInput struct {
	SystemAccountID string
	AccountID       string
}

// ClearNormalRouteLatencyDegradationForAccount mirrors
// clearNormalRouteLatencyDegradationForAccountAsync: account-level manual
// cleanup must not depend on the currently listed binding group, otherwise
// other groups keep scheduling against latency_degraded.
func (s *LatencyDegradationService) ClearNormalRouteLatencyDegradationForAccount(
	ctx context.Context,
	input ClearNormalRouteLatencyDegradationForAccountInput,
) (int64, error) {
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	accountID := strings.TrimSpace(input.AccountID)
	if systemAccountID == "" || accountID == "" {
		return 0, nil
	}
	return s.clearAcrossLatencyKeyspaces(ctx, func(state latencyState) bool {
		return state.Scope.SystemAccountID == systemAccountID &&
			(state.AccountID == accountID || RuntimeAccountIDFromKey(state.RuntimeKey) == accountID)
	})
}

// ---------------------------------------------------------------------------
// Generation handling.

// loadLatencyStateGeneration 读取（或初始化）指定键族的 generation token；
// v1/v2 两代各自持有一个 generation marker。
func (s *LatencyDegradationService) loadLatencyStateGeneration(ctx context.Context, keyspace latencyStateKeyspace) (string, error) {
	event, err := s.loadOrCreateLatencyGenerationEvent(ctx, keyspace)
	if err != nil {
		return "", err
	}
	return latencyGenerationToken(&event), nil
}

// loadLatencyGenerationEventRaw returns the normalized event together with
// the raw bytes it was read from, so CAS callers resend the exact stored
// payload as the expected value (single read, no re-read race).
func (s *LatencyDegradationService) loadLatencyGenerationEventRaw(ctx context.Context, keyspace latencyStateKeyspace) (*LatencyGenerationEvent, json.RawMessage, error) {
	for attempt := 0; attempt < latencyStateGenerationCASMax; attempt++ {
		raw, err := s.store.GetJSON(ctx, keyspace.generationKey())
		if err != nil {
			return nil, nil, err
		}
		if raw == nil {
			return nil, nil, nil
		}
		var event LatencyGenerationEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, nil, errors.New("普通路由速度优先 runtime-state generation event publishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
		if !isNormalRouteLatencyGenerationEvent(event) {
			return nil, nil, errors.New("普通路由速度优先 runtime-state generation event publishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
		normalizedEvent, err := normalizeLatencyGenerationEvent(event)
		if err != nil {
			return nil, nil, err
		}
		if rawJSONEqual(raw, mustMarshalJSON(normalizedEvent)) {
			return &normalizedEvent, raw, nil
		}
		applied, err := s.store.CompareSetJSON(ctx, keyspace.generationKey(), raw, normalizedEvent, latencyStateGenerationTTLMs)
		if err != nil {
			return nil, nil, err
		}
		if applied {
			return &normalizedEvent, mustMarshalJSON(normalizedEvent), nil
		}
	}
	return nil, nil, fmt.Errorf("普通路由速度优先 generation marker canonical CAS 重试耗尽（%d 次）", latencyStateGenerationCASMax)
}

func (s *LatencyDegradationService) loadLatencyGenerationEvent(ctx context.Context, keyspace latencyStateKeyspace) (*LatencyGenerationEvent, error) {
	event, _, err := s.loadLatencyGenerationEventRaw(ctx, keyspace)
	return event, err
}

func (s *LatencyDegradationService) loadOrCreateLatencyGenerationEvent(ctx context.Context, keyspace latencyStateKeyspace) (LatencyGenerationEvent, error) {
	for attempt := 0; attempt < latencyStateGenerationCASMax; attempt++ {
		current, err := s.loadLatencyGenerationEvent(ctx, keyspace)
		if err != nil {
			return LatencyGenerationEvent{}, err
		}
		if current != nil {
			return *current, nil
		}
		applied, err := s.store.CompareSetJSON(ctx, keyspace.generationKey(), nil, latencyStateInitialGenerationEvent, latencyStateGenerationTTLMs)
		if err != nil {
			return LatencyGenerationEvent{}, err
		}
		if applied {
			return latencyStateInitialGenerationEvent, nil
		}
	}
	return LatencyGenerationEvent{}, fmt.Errorf("普通路由速度优先 generation marker CAS 初始化重试耗尽（%d 次）", latencyStateGenerationCASMax)
}

func (s *LatencyDegradationService) renewLatencyStateGeneration(ctx context.Context, keyspace latencyStateKeyspace, generation string) (bool, error) {
	current, currentRaw, err := s.loadLatencyGenerationEventRaw(ctx, keyspace)
	if err != nil {
		return false, err
	}
	if current == nil || latencyGenerationToken(current) != generation {
		return false, nil
	}
	return s.store.CompareSetJSON(ctx, keyspace.generationKey(), currentRaw, current, latencyStateGenerationTTLMs)
}

func normalizeLatencyGenerationEvent(event LatencyGenerationEvent) (LatencyGenerationEvent, error) {
	version := strings.TrimSpace(event.Version)
	if version == "" {
		return LatencyGenerationEvent{}, errors.New("普通路由速度优先 generation event 缺少 version")
	}
	publishedAt, ok := CanonicalizeRfc3339Instant(event.PublishedAt)
	if !ok {
		return LatencyGenerationEvent{}, errors.New("普通路由速度优先 generation event publishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return LatencyGenerationEvent{Version: version, PublishedAt: publishedAt}, nil
}

func compareLatencyGenerationEvents(left, right LatencyGenerationEvent) int {
	leftPublishedAtMs, leftOK := Rfc3339InstantMilliseconds(left.PublishedAt)
	rightPublishedAtMs, rightOK := Rfc3339InstantMilliseconds(right.PublishedAt)
	if !leftOK || !rightOK {
		panic("普通路由速度优先 generation event publishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	if difference := leftPublishedAtMs - rightPublishedAtMs; difference != 0 {
		if difference < 0 {
			return -1
		}
		return 1
	}
	if left.Version == right.Version {
		return 0
	}
	if left.Version > right.Version {
		return 1
	}
	return -1
}

func latencyGenerationToken(event *LatencyGenerationEvent) string {
	if event == nil {
		return "initial"
	}
	publishedAtMs, ok := Rfc3339InstantMilliseconds(event.PublishedAt)
	if !ok {
		panic("普通路由速度优先 generation event publishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return string(mustMarshalJSON([]any{publishedAtMs, event.Version}))
}

func isNormalRouteLatencyGenerationEvent(value LatencyGenerationEvent) bool {
	if strings.TrimSpace(value.Version) == "" {
		return false
	}
	_, ok := Rfc3339InstantMilliseconds(value.PublishedAt)
	return ok
}

// ---------------------------------------------------------------------------
// State load/validate.

func (s *LatencyDegradationService) loadLatencyState(ctx context.Context, key string, generation string) (*latencyState, error) {
	raw, err := s.store.GetJSON(ctx, key)
	if err != nil {
		return nil, err
	}
	return latencyStateFromRaw(raw, generation), nil
}

// latencyStateFromRaw applies the shared "no state" fallbacks: missing raw,
// undecodable/invalid payload and stale generation all read as no state.
func latencyStateFromRaw(raw json.RawMessage, generation string) *latencyState {
	if raw == nil {
		return nil
	}
	state, ok := decodeLatencyState(raw)
	if !ok {
		return nil
	}
	if !isCurrentLatencyState(state, generation) {
		return nil
	}
	return &state
}

func isCurrentLatencyState(state latencyState, generation string) bool {
	return state.Generation == generation
}

func decodeLatencyState(raw json.RawMessage) (latencyState, bool) {
	var state latencyState
	if err := json.Unmarshal(raw, &state); err != nil {
		return latencyState{}, false
	}
	if !isNormalRouteLatencyState(state) {
		return latencyState{}, false
	}
	return state, true
}

func isNormalRouteLatencyState(state latencyState) bool {
	if state.Generation == "" || state.AccountID == "" || state.RuntimeKey == "" {
		return false
	}
	if !isNormalRouteLatencyDegradationScope(state.Scope) {
		return false
	}
	if !isRouteStrategySpeedFirstConfig(state.Config) {
		return false
	}
	if state.RecoveryProbeRoundAttemptCount != nil && *state.RecoveryProbeRoundAttemptCount < 0 {
		return false
	}
	if state.RecoveryProbeRoundSuccessCount != nil && *state.RecoveryProbeRoundSuccessCount < 0 {
		return false
	}
	return true
}

func isNormalRouteLatencyDegradationScope(scope LatencyDegradationScope) bool {
	return strings.TrimSpace(scope.SystemAccountID) != "" &&
		strings.TrimSpace(scope.RouteStrategyID) != "" &&
		strings.TrimSpace(scope.GroupID) != ""
}

func isRouteStrategySpeedFirstConfig(config SpeedFirstRuntimeConfig) bool {
	return isFinitePositiveInt(config.FirstByteDeadlineMs) &&
		isFinitePositiveInt(config.SlowTriggerCount) &&
		isFinitePositiveInt(config.SlowWindowSeconds) &&
		isFinitePositiveInt(config.RecoverySuccessCount) &&
		isFinitePositiveInt(config.ProbeIntervalSeconds) &&
		isFinitePositiveInt(config.DegradedTTLSeconds) &&
		isFinitePositiveInt(config.MaxFirstByteRetriesPerRequest)
}

func isFinitePositiveInt(value int64) bool { return value > 0 }

// resolvedLatencyScopeForAccount resolves the scope group component per
// account at the service boundary (设计 3.6 / B25): merge-route accounts are
// keyed and stored under their bound group (账号组 ≠ 窗口组), while accounts
// without a bound group fall back to the inbound window group so legacy keys
// and stored scopes stay byte-identical. Callers must build the state key and
// latencyState.Scope from this same resolved value — jobs recovery probes
// rebuild the key from the stored Scope and look up credentials by
// Scope.GroupID, so the two must never diverge.
func resolvedLatencyScopeForAccount(scope LatencyDegradationScope, account SuppressibleGatewayAccount) LatencyDegradationScope {
	boundGroupID := strings.TrimSpace(account.BoundGroupID)
	if boundGroupID == "" {
		return scope
	}
	resolved := scope
	resolved.GroupID = boundGroupID
	return resolved
}

func accountLatencyStateKey(scope LatencyDegradationScope, account SuppressibleGatewayAccount) string {
	runtimeKey, err := GatewayAccountRuntimeKey(account)
	if err != nil {
		runtimeKey = account.ID
	}
	return latencyStateKeyspaceFor(scope).stateKey(scope, runtimeKey)
}

func accountLatencyStateKeyChecked(scope LatencyDegradationScope, account SuppressibleGatewayAccount) (string, error) {
	runtimeKey, err := GatewayAccountRuntimeKey(account)
	if err != nil {
		return "", err
	}
	return latencyStateKeyspaceFor(scope).stateKey(scope, runtimeKey), nil
}

func sanitizeKeyPart(value string) string {
	normalized := strings.TrimSpace(value)
	var builder strings.Builder
	for _, r := range normalized {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == ':' || r == '_' || r == '-' {
			builder.WriteRune(r)
			continue
		}
		builder.WriteByte('_')
	}
	if builder.Len() == 0 {
		return "_"
	}
	return builder.String()
}

func probeCandidateFromState(key string, state latencyState) (LatencyProbeCandidate, bool) {
	if state.DegradedUntilMs == nil || state.NextProbeAtMs == nil {
		return LatencyProbeCandidate{}, false
	}
	return LatencyProbeCandidate{
		StateKey:                       key,
		Generation:                     state.Generation,
		AccountID:                      state.AccountID,
		AccountName:                    state.AccountName,
		RuntimeKey:                     state.RuntimeKey,
		Scope:                          state.Scope,
		Config:                         state.Config,
		DegradationEventID:             state.DegradationEventID,
		DegradedUntil:                  ISOStringMs(*state.DegradedUntilMs),
		NextProbeAt:                    ISOStringMs(*state.NextProbeAtMs),
		RecoverySuccessCount:           state.SuccessCount,
		RecoveryProbeRoundAttemptCount: recoveryProbeRoundAttempts(state),
		RecoveryProbeRoundSuccessCount: recoveryProbeRoundSuccesses(state),
		Dimension:                      latencyDimensionOf(state),
	}, true
}

func latencyProbeCandidateMatchesState(candidate LatencyProbeCandidate, state latencyState) bool {
	var candidateNextProbeAt *string
	if state.NextProbeAtMs != nil {
		candidateNextProbeAt = stringPtr(ISOStringMs(*state.NextProbeAtMs))
	}
	var candidateDegradedUntil *string
	if state.DegradedUntilMs != nil {
		candidateDegradedUntil = stringPtr(ISOStringMs(*state.DegradedUntilMs))
	}
	nextProbeAt := candidate.NextProbeAt
	degradedUntil := candidate.DegradedUntil
	return candidate.AccountID == state.AccountID &&
		candidate.RuntimeKey == state.RuntimeKey &&
		candidate.Scope.SystemAccountID == state.Scope.SystemAccountID &&
		candidate.Scope.RouteStrategyID == state.Scope.RouteStrategyID &&
		candidate.Scope.GroupID == state.Scope.GroupID &&
		// 调度偏好身份（v2 命名空间）参与围栏：迟到探针既不能以旧偏好身份更新
		// 新键，也不能以新身份覆盖旧键（v1 两侧均为空串，比较行为不变）。
		candidate.Scope.PreferenceID == state.Scope.PreferenceID &&
		candidate.Scope.ConfigFingerprint == state.Scope.ConfigFingerprint &&
		stringPtrEqual(candidate.DegradationEventID, state.DegradationEventID) &&
		candidate.RecoveryProbeRoundAttemptCount == recoveryProbeRoundAttempts(state) &&
		candidate.RecoveryProbeRoundSuccessCount == recoveryProbeRoundSuccesses(state) &&
		stringPtrEqual(&nextProbeAt, candidateNextProbeAt) &&
		stringPtrEqual(&degradedUntil, candidateDegradedUntil)
}

func stringPtr(value string) *string { return &value }

func stringPtrEqual(left, right *string) bool {
	if left == nil && right == nil {
		return true
	}
	if left == nil || right == nil {
		return false
	}
	return *left == *right
}

func optionalAccountName(account SuppressibleGatewayAccount) *string {
	if trimmed := strings.TrimSpace(account.Name); trimmed != "" {
		return &trimmed
	}
	return nil
}

func isoPtr(ms *int64) *string {
	if ms == nil {
		return nil
	}
	return stringPtr(ISOStringMs(*ms))
}

func recoveryProbeRoundAttempts(state latencyState) int64 {
	if state.RecoveryProbeRoundAttemptCount == nil {
		return 0
	}
	return *state.RecoveryProbeRoundAttemptCount
}

func recoveryProbeRoundSuccesses(state latencyState) int64 {
	if state.RecoveryProbeRoundSuccessCount == nil {
		return 0
	}
	return *state.RecoveryProbeRoundSuccessCount
}

func latencyStateTTLMs(config SpeedFirstRuntimeConfig, degraded bool) int64 {
	seconds := config.SlowWindowSeconds
	if degraded {
		seconds = config.DegradedTTLSeconds
	}
	return maxInt64(1, seconds) * 1000
}

func latencyStateRemainingTTLMs(degradedUntilMs *int64, now int64) int64 {
	if degradedUntilMs == nil {
		return 1
	}
	return maxInt64(1, *degradedUntilMs-now)
}

func (s *LatencyDegradationService) nextProbeDelayMs(config SpeedFirstRuntimeConfig) int64 {
	baseMs := maxInt64(10, config.ProbeIntervalSeconds) * 1000
	return PassiveScheduleDelayMs(baseMs, s.jitterRandom())
}

func (s *LatencyDegradationService) nextRecoveryProbeDelayMs() int64 {
	return PassiveScheduleDelayMs(normalRouteRecoveryProbeIntervalMs, s.jitterRandom())
}

// ---------------------------------------------------------------------------
// Index maintenance.

func (s *LatencyDegradationService) loadLatencyStateIndexKeys(ctx context.Context, indexKey string) ([]string, error) {
	snapshot, err := s.loadLatencyStateIndexSnapshot(ctx, indexKey)
	if err != nil {
		return nil, err
	}
	return snapshot.keys, nil
}

type latencyIndexSnapshot struct {
	value json.RawMessage
	keys  []string
}

func (s *LatencyDegradationService) loadLatencyStateIndexSnapshot(ctx context.Context, indexKey string) (latencyIndexSnapshot, error) {
	value, err := s.store.GetJSON(ctx, indexKey)
	if err != nil {
		return latencyIndexSnapshot{}, err
	}
	var keys []string
	if value != nil {
		var index struct {
			Keys []string `json:"keys"`
		}
		if err := json.Unmarshal(value, &index); err == nil {
			keys = index.Keys
		}
	}
	return latencyIndexSnapshot{value: value, keys: normalizeLatencyStateIndexKeys(keys)}, nil
}

func (s *LatencyDegradationService) addLatencyStateAllIndexKey(ctx context.Context, key string) error {
	keyspace := latencyStateKeyspaceOfStateKey(key)
	return s.addLatencyStateIndexKey(ctx, keyspace.allIndexKey(), keyspace.allIndexLockKey(), key)
}

func (s *LatencyDegradationService) addLatencyStateProbeIndexKey(ctx context.Context, key string) error {
	keyspace := latencyStateKeyspaceOfStateKey(key)
	return s.addLatencyStateIndexKey(ctx, keyspace.probeIndexKey(), keyspace.probeIndexLockKey(), key)
}

func (s *LatencyDegradationService) addLatencyStateIndexKey(ctx context.Context, indexKey, lockKey, key string) error {
	return s.mutateLatencyStateIndexKeys(ctx, indexKey, lockKey, func(keys []string) []string {
		for _, existing := range keys {
			if existing == key {
				return keys
			}
		}
		keys = append(keys, key)
		if len(keys) > latencyStateIndexMaxKeys {
			keys = keys[len(keys)-latencyStateIndexMaxKeys:]
		}
		return keys
	})
}

// removeLatencyStateIndexKeysStrict 从两代索引中移除目标键。待移除键集可能
// 横跨 v1/v2：按版本段分组后各代只在本代索引上过滤（无该代键时跳过，避免
// 无谓的索引锁竞争），删除顺序保持先 probe 后 all。
func (s *LatencyDegradationService) removeLatencyStateIndexKeysStrict(ctx context.Context, keysToRemove []string) error {
	if len(keysToRemove) == 0 {
		return nil
	}
	removeSet := make(map[string]struct{}, len(keysToRemove))
	versions := make(map[string]struct{}, 2)
	for _, key := range keysToRemove {
		removeSet[key] = struct{}{}
		versions[latencyStateKeyspaceOfStateKey(key).version] = struct{}{}
	}
	filter := func(keys []string) []string { return filterKeys(keys, removeSet) }
	var errs []error
	for _, keyspace := range latencyStateKeyspaces() {
		if _, present := versions[keyspace.version]; !present {
			continue
		}
		if err := s.mutateLatencyStateIndexKeys(ctx, keyspace.probeIndexKey(), keyspace.probeIndexLockKey(), filter); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := s.mutateLatencyStateIndexKeys(ctx, keyspace.allIndexKey(), keyspace.allIndexLockKey(), filter); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func filterKeys(keys []string, removeSet map[string]struct{}) []string {
	output := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, removed := removeSet[key]; removed {
			continue
		}
		output = append(output, key)
	}
	return output
}

func (s *LatencyDegradationService) mutateLatencyStateIndexKeys(
	ctx context.Context,
	indexKey, lockKey string,
	mutator func(keys []string) []string,
) error {
	token := NewUUID()
	locked, err := s.acquireLatencyStateIndexLock(ctx, lockKey, token)
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("普通路由速度优先索引锁获取失败：%s", indexKey)
	}
	defer func() { _ = s.store.ReleaseLock(ctx, lockKey, token) }()
	for attempt := 0; attempt < latencyStateIndexCASMax; attempt++ {
		current, err := s.loadLatencyStateIndexSnapshot(ctx, indexKey)
		if err != nil {
			return err
		}
		next := map[string]any{"keys": normalizeLatencyStateIndexKeys(mutator(current.keys))}
		applied, err := s.store.CompareSetJSON(ctx, indexKey, current.value, next, latencyStateIndexTTLMs)
		if err != nil {
			return err
		}
		if applied {
			return nil
		}
	}
	return fmt.Errorf("普通路由速度优先索引 CAS 重试耗尽（%d 次）：%s", latencyStateIndexCASMax, indexKey)
}

// writeLatencyStateAndIndexesStrict mirrors writeLatencyStateAndIndexesStrictAsync
// including the rollback path with its aggregate error.
func (s *LatencyDegradationService) writeLatencyStateAndIndexesStrict(
	ctx context.Context,
	key string,
	previous *latencyState,
	state latencyState,
	ttlMs int64,
	addProbeIndex bool,
) error {
	if err := s.store.SetJSON(ctx, key, state, ttlMs); err != nil {
		return err
	}
	allIndexApplied := false
	probeIndexApplied := false
	var writeErr error
	if err := s.addLatencyStateAllIndexKey(ctx, key); err != nil {
		writeErr = err
	} else {
		allIndexApplied = true
		if addProbeIndex {
			if err := s.addLatencyStateProbeIndexKey(ctx, key); err != nil {
				writeErr = err
			} else {
				probeIndexApplied = true
			}
		}
	}
	if writeErr == nil {
		return nil
	}
	var rollbackErrors []error
	stateRolledBack := false
	var rollbackErr error
	if previous != nil {
		previousConfigTTL := latencyStateTTLMs(previous.Config, previous.DegradedUntilMs != nil)
		stateRolledBack, rollbackErr = s.store.CompareSetJSON(ctx, key, mustMarshalJSON(state), *previous, previousConfigTTL)
	} else {
		stateRolledBack, rollbackErr = s.store.CompareDeleteJSON(ctx, key, mustMarshalJSON(state))
	}
	if rollbackErr != nil {
		rollbackErrors = append(rollbackErrors, rollbackErr)
	}
	if !stateRolledBack && rollbackErr == nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("普通路由速度优先 state rollback CAS 失败：%s", key))
	}
	if stateRolledBack && previous == nil && (allIndexApplied || probeIndexApplied) {
		if err := s.removeLatencyStateIndexKeysStrict(ctx, []string{key}); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	if len(rollbackErrors) > 0 {
		return fmt.Errorf("普通路由速度优先 state/index 写入失败且回滚存在 %d 个错误: %w", len(rollbackErrors), errors.Join(rollbackErrors...))
	}
	return writeErr
}

func (s *LatencyDegradationService) withLatencyStateMutationLock(
	ctx context.Context,
	key string,
	generation string,
	operation func() (bool, error),
) (bool, error) {
	keyspace := latencyStateKeyspaceOfStateKey(key)
	lock, ok, err := s.acquireLatencyStateMutationLock(ctx, keyspace, key, latencyStateMutationLockTTLMs)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	defer func() { _ = s.store.ReleaseLock(ctx, lock.lockKey, lock.token) }()
	renewed, err := s.renewLatencyStateGeneration(ctx, keyspace, generation)
	if err != nil {
		return false, err
	}
	if !renewed {
		return false, nil
	}
	return operation()
}

func (s *LatencyDegradationService) clearCurrentGenerationLatencyStateKeys(
	ctx context.Context,
	keys []string,
	keyspace latencyStateKeyspace,
	generation string,
	predicate func(latencyState) bool,
) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	var nextIndex atomic.Int64
	var totalCleared atomic.Int64
	workers := minInt(s.opts.BackgroundClearConcurrency, len(keys))
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer safego.Recover("gatewayproxyhealth.latencydegradation.background_clear_worker")
			defer wg.Done()
			for {
				index := nextIndex.Add(1) - 1
				if index >= int64(len(keys)) {
					return
				}
				key := keys[index]
				cleared, err := s.runWithBackgroundSlot(ctx, func() (int64, error) {
					return s.clearCurrentGenerationLatencyStateKey(ctx, key, keyspace, generation, predicate)
				})
				if err != nil {
					errCh <- err
					return
				}
				totalCleared.Add(cleared)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return 0, fmt.Errorf("普通路由速度优先逐 key 精确清理存在 %d 个失败: %w", len(errs), errors.Join(errs...))
	}
	return totalCleared.Load(), nil
}

func (s *LatencyDegradationService) runWithBackgroundSlot(ctx context.Context, task func() (int64, error)) (int64, error) {
	select {
	case s.clearSlots <- struct{}{}:
		defer func() { <-s.clearSlots }()
		return task()
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (s *LatencyDegradationService) clearCurrentGenerationLatencyStateKey(
	ctx context.Context,
	key string,
	keyspace latencyStateKeyspace,
	generation string,
	predicate func(latencyState) bool,
) (int64, error) {
	lock, ok, err := s.acquireLatencyStateMutationLock(ctx, keyspace, key, latencyStateMutationLockTTLMs)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("普通路由速度优先状态 mutation lock 获取失败：%s", key)
	}
	defer func() { _ = s.store.ReleaseLock(ctx, lock.lockKey, lock.token) }()
	renewed, err := s.renewLatencyStateGeneration(ctx, keyspace, generation)
	if err != nil {
		return 0, err
	}
	if !renewed {
		return 0, nil
	}
	raw, err := s.store.GetJSON(ctx, key)
	if err != nil {
		return 0, err
	}
	if raw == nil {
		if err := s.removeLatencyStateIndexKeysStrict(ctx, []string{key}); err != nil {
			return 0, err
		}
		return 0, nil
	}
	state, valid := decodeLatencyState(raw)
	if !valid {
		return 0, nil
	}
	if state.Generation != generation || !predicate(state) {
		return 0, nil
	}
	if err := s.deleteLatencyStateAndIndexesStrict(ctx, key); err != nil {
		return 0, err
	}
	return 1, nil
}

func (s *LatencyDegradationService) deleteLatencyStateAndIndexesStrict(ctx context.Context, key string) error {
	if err := s.store.Delete(ctx, key); err != nil {
		return err
	}
	return s.removeLatencyStateIndexKeysStrict(ctx, []string{key})
}

func (s *LatencyDegradationService) acquireLatencyStateMutationLock(ctx context.Context, keyspace latencyStateKeyspace, key string, ttlMs int64) (*latencyStateLock, bool, error) {
	lockKey := keyspace.mutationLockKey(key)
	token := NewUUID()
	locked, err := s.acquireLatencyStateLock(ctx, lockKey, token, ttlMs)
	if err != nil {
		return nil, false, err
	}
	if !locked {
		return nil, false, nil
	}
	return &latencyStateLock{key: key, lockKey: lockKey, token: token}, true, nil
}

func normalRouteLatencyProbeClaimLockKey(candidate LatencyProbeCandidate) string {
	return latencyStateKeyspaceOfStateKey(candidate.StateKey).
		probeClaimLockKey(candidate.Generation, candidate.StateKey)
}

func (s *LatencyDegradationService) acquireLatencyStateIndexLock(ctx context.Context, lockKey, token string) (bool, error) {
	return s.acquireLatencyStateLock(ctx, lockKey, token, latencyStateIndexLockTTLMs)
}

func (s *LatencyDegradationService) acquireLatencyStateLock(ctx context.Context, lockKey, token string, ttlMs int64) (bool, error) {
	for attempt := 0; attempt < latencyStateLockAcquireMaxAttempts; attempt++ {
		locked, err := s.store.AcquireLock(ctx, lockKey, ttlMs, token)
		if err != nil {
			return false, err
		}
		if locked {
			return true, nil
		}
		s.lockRetryDelay(attempt)
	}
	return false, nil
}

func normalizeLatencyStateIndexKeys(value []string) []string {
	if value == nil {
		return []string{}
	}
	seen := make(map[string]struct{}, len(value))
	keys := make([]string, 0, len(value))
	for _, item := range value {
		key := strings.TrimSpace(item)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	if len(keys) > latencyStateIndexMaxKeys {
		keys = keys[len(keys)-latencyStateIndexMaxKeys:]
	}
	return keys
}

func normalizedRouteStrategyIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	ids := make([]string, 0, len(values))
	for _, value := range values {
		id := strings.TrimSpace(value)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func mustMarshalJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("null")
	}
	return encoded
}
