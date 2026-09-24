package gatewaydispatch

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// prepareOpenAIGatewayDispatchAccounts, migrated from dispatch/preparation.ts.
// Every stage hook, log event and Chinese message mirrors the Node
// implementation; the collaborator services arrive through the ports.

// Preparation outcome union (Node outcome tags + 'ready').
const (
	PreparationOutcomeReady     = "ready"
	PreparationOutcomeFallback  = "fallback"
	PreparationOutcomeCompleted = "completed"
)

// PreparationResult mirrors DispatchPreparationResult.
type PreparationResult struct {
	// Outcome is 'ready' | 'fallback' | 'completed'.
	Outcome string
	// ready variant
	Accounts                                 []AccountCandidate
	ReleaseClientIPConcurrency               func()
	NormalRouteLatencyDegradationApplied     bool
	CodexTurnAccountAvoidanceApplied         bool
	CodexTurnAvoidedAccountIDs               []string
	PrecheckHalfOpenEligible                 bool
	HotQualityExplorationReservation         *HotQualityReservation
	SettleHotQualityExplorationAfterDispatch func(ctx context.Context, outcome string) error
	// W1b 决策摘要带出：窗口级授权配额批查否决的账户 ID 与 lane 容量快照
	// 中繁忙的候选 ID（busy 候选仍留在窗口内，仅排序降位）。仅 ready 出口
	// 携带；fallback/completed 出口不汇总（原因已在既有审计标签与终局
	// 响应上）。
	QuotaDeniedAccountIDs  []string
	CapacityBusyAccountIDs []string
	// fallback variant
	Reason  string
	Context any
}

// localSuppressionBypassResult mirrors localSuppressionBypassResult.
func localSuppressionBypassResult(accounts []AccountCandidate) SuppressionFilterResult {
	return SuppressionFilterResult{
		Accounts:               accounts,
		SuppressedCount:        0,
		AllSuppressed:          false,
		SuppressedAccountIDs:   []string{},
		AcquiredHalfOpenLeases: []HalfOpenLease{},
	}
}

// ---------------------------------------------------------------------------
// W1b：网关调度决策摘要（candidate-window explainability）
//
// 目标是把一次候选准备窗口的既有决策数据（总量/入选/首选账户、跳过明细、
// busy 候选、被抑制/降级/回避 ID）汇成一个可序列化结构，经审计 metadata
// 标签 gateway_dispatch_candidates 与进程级观察槽（组合根装配的
// gateway_dispatch_decision slog 日志）带出。只读既有决策数据，不改变任何
// 过滤/排序行为；纯内存切片组装，O(n)，无锁无 IO；引擎包保持零 slog 依赖。
// ---------------------------------------------------------------------------

// 摘要跳过原因稳定常量（准备阶段可判定的粒度；模型过滤三分支细分原因在
// candfilters 的 ModelSkipReason* 常量与 account_model_filter 审计标签上）。
const (
	// DispatchSkipReasonModelUnsupported：模型秩判定为 unsupported 的被跳
	// 账户（从未进入候选准备窗口）。
	DispatchSkipReasonModelUnsupported = "model_unsupported"
	// DispatchSkipReasonQuotaDenied：窗口级授权配额批查否决的候选。
	DispatchSkipReasonQuotaDenied = "quota_denied"
)

// dispatchDecisionSummaryListCap 是摘要各列表字段的截断上限：大分组不得
// 爆炸审计记录与决策日志；被截断时对应 Truncated 字段置位。
const dispatchDecisionSummaryListCap = 20

// DispatchDecisionSkip 是摘要里的一条跳过明细（与 candfilters.AccountSkip
// 同形；JSON 键名即日志/审计形状）。
type DispatchDecisionSkip struct {
	AccountID string `json:"id"`
	Reason    string `json:"reason"`
}

// DispatchDecisionSummary 汇总一次候选准备窗口的调度决策。JSON 形状即
// gateway_dispatch_candidates 审计标签值与 gateway_dispatch_decision 日志
// 的摘要形状；空列表/零值字段经 omitempty 保持紧凑。
type DispatchDecisionSummary struct {
	// CandidateTotal 是进入候选准备的窗口总量。能力/模型过滤发生在窗口
	// 之前：其逐账户明细现随 PreFilterSkipped/PreFilterSkippedCount 进本
	// 摘要（既有审计标签 account_request_capability_filter /
	// account_model_filter 保持不变）；模型过滤被跳账户同时以
	// reason=model_unsupported 汇入本摘要 skipped。
	CandidateTotal        int                    `json:"candidateTotal"`
	EligibleCount         int                    `json:"eligibleCount"`
	SelectedAccountID     string                 `json:"selectedAccountId,omitempty"`
	ModelRankAvailable    bool                   `json:"modelRankAvailable"`
	PreFilterSkippedCount int                    `json:"preFilterSkippedCount,omitempty"`
	PreFilterSkipped      []DispatchDecisionSkip `json:"preFilterSkipped,omitempty"`
	PreFilterSkippedTrunc bool                   `json:"preFilterSkippedTruncated,omitempty"`
	Skipped               []DispatchDecisionSkip `json:"skipped,omitempty"`
	SkippedTruncated      bool                   `json:"skippedTruncated,omitempty"`
	Busy                  []string               `json:"busy,omitempty"`
	BusyTruncated         bool                   `json:"busyTruncated,omitempty"`
	Suppressed            []string               `json:"suppressedAccountIds,omitempty"`
	SuppressedTruncated   bool                   `json:"suppressedTruncated,omitempty"`
	Degraded              []string               `json:"degradedAccountIds,omitempty"`
	DegradedTruncated     bool                   `json:"degradedTruncated,omitempty"`
	Avoided               []string               `json:"avoidedAccountIds,omitempty"`
	AvoidedTruncated      bool                   `json:"avoidedTruncated,omitempty"`
}

// DispatchDecisionSummaryInput 是汇总组装输入：全部为既有决策数据的只读
// 投影（与 PrepareOpenAIGatewayDispatchAccounts 内现成变量一一对应）。
type DispatchDecisionSummaryInput struct {
	CandidateTotal int
	// Eligible 是准备完成的可派发账户（含 busy 降位候选）；首选账户取
	// 首位（session affinity claim 之后的最终次序）。
	Eligible      []AccountCandidate
	ModelPriority *gatewayrouting.GatewayAccountModelPriority
	// PreFilterSkipped 是窗口之前能力/模型过滤的逐账户跳过明细（W1b 续：
	// 使决策日志自含"为什么不在窗口里"的完整答案）。
	PreFilterSkipped []AccountSkip
	QuotaDenied      []string
	Busy             []string
	Suppressed       []string
	Degraded         []string
	Avoided          []string
}

// BuildDispatchDecisionSummary 把候选窗口既有决策数据汇成可序列化摘要。
// skipped 顺序：配额否决在前、模型秩 unsupported 在后，各自按账户 ID 稳定
// 排序——截断窗口对同一输入确定可回放（Mock/回归可回放约束）。
func BuildDispatchDecisionSummary(input DispatchDecisionSummaryInput) DispatchDecisionSummary {
	summary := DispatchDecisionSummary{
		CandidateTotal:     input.CandidateTotal,
		EligibleCount:      len(input.Eligible),
		ModelRankAvailable: input.ModelPriority != nil,
	}
	if len(input.Eligible) > 0 {
		summary.SelectedAccountID = input.Eligible[0].ID
	}
	// W1b 续：预过滤层（能力/模型）跳过明细保持输入顺序（能力在前、模型
	// 在后；candfilters 的产出顺序即过滤执行顺序），截断保护同其余列表。
	if len(input.PreFilterSkipped) > 0 {
		preFilterSkipped := make([]DispatchDecisionSkip, 0, len(input.PreFilterSkipped))
		for _, skip := range input.PreFilterSkipped {
			preFilterSkipped = append(preFilterSkipped, DispatchDecisionSkip{AccountID: skip.AccountID, Reason: skip.Reason})
		}
		summary.PreFilterSkippedCount = len(preFilterSkipped)
		summary.PreFilterSkipped, summary.PreFilterSkippedTrunc = cappedSlice(preFilterSkipped)
	}
	quotaDenied := sortedStringsCopy(input.QuotaDenied)
	modelSkipped := modelRankSkippedIDs(input.ModelPriority)
	skipped := make([]DispatchDecisionSkip, 0, len(quotaDenied)+len(modelSkipped))
	for _, accountID := range quotaDenied {
		skipped = append(skipped, DispatchDecisionSkip{AccountID: accountID, Reason: DispatchSkipReasonQuotaDenied})
	}
	for _, accountID := range modelSkipped {
		skipped = append(skipped, DispatchDecisionSkip{AccountID: accountID, Reason: DispatchSkipReasonModelUnsupported})
	}
	summary.Skipped, summary.SkippedTruncated = cappedSlice(skipped)
	summary.Busy, summary.BusyTruncated = cappedCopy(input.Busy)
	summary.Suppressed, summary.SuppressedTruncated = cappedSortedUniqueIDs(input.Suppressed)
	summary.Degraded, summary.DegradedTruncated = cappedSortedUniqueIDs(input.Degraded)
	summary.Avoided, summary.AvoidedTruncated = cappedSortedUniqueIDs(input.Avoided)
	return summary
}

// AuditMetadata 把摘要投影为 AddGatewayMetadata 的 map 值形状（JSON 往返，
// 保证与决策日志序列化形状一致）。结构只含 string/int/bool/切片，
// json.Marshal 不可能失败；错误分支仅兜底空 map，不影响审计主流程。
func (s DispatchDecisionSummary) AuditMetadata() map[string]any {
	encoded, _ := json.Marshal(s)
	metadata := map[string]any{}
	_ = json.Unmarshal(encoded, &metadata)
	return metadata
}

// modelRankSkippedIDs 投影模型秩为 unsupported 的账户 ID（稳定排序）。
func modelRankSkippedIDs(priority *gatewayrouting.GatewayAccountModelPriority) []string {
	if priority == nil || len(priority.RankByAccountID) == 0 {
		return nil
	}
	ids := make([]string, 0, len(priority.RankByAccountID))
	for accountID, rank := range priority.RankByAccountID {
		if rank == ModelPriorityRankUnsupported {
			ids = append(ids, accountID)
		}
	}
	sort.Strings(ids)
	return ids
}

func sortedStringsCopy(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

// cappedSortedUniqueIDs 去重、稳定排序并截断保护。
func cappedSortedUniqueIDs(ids []string) ([]string, bool) {
	seen := make(map[string]struct{}, len(ids))
	unique := make([]string, 0, len(ids))
	for _, accountID := range ids {
		if accountID == "" {
			continue
		}
		if _, dup := seen[accountID]; dup {
			continue
		}
		seen[accountID] = struct{}{}
		unique = append(unique, accountID)
	}
	sort.Strings(unique)
	return cappedCopy(unique)
}

// cappedCopy 截断保护：最多 dispatchDecisionSummaryListCap 条 + truncated
// 标记；保持给定顺序（busy 的输入顺序即并发快照扫描顺序，确定可回放）。
func cappedCopy(ids []string) ([]string, bool) {
	if len(ids) == 0 {
		return nil, false
	}
	if len(ids) > dispatchDecisionSummaryListCap {
		return append([]string(nil), ids[:dispatchDecisionSummaryListCap]...), true
	}
	return append([]string(nil), ids...), false
}

func cappedSlice[T any](items []T) ([]T, bool) {
	if len(items) == 0 {
		return nil, false
	}
	if len(items) > dispatchDecisionSummaryListCap {
		return items[:dispatchDecisionSummaryListCap], true
	}
	return items, false
}

// DispatchDecisionEvent 是候选准备决策事件的链侧投影：引擎在候选准备完成
// （ready 出口）时发出，组合根据此输出一条 gateway_dispatch_decision
// slog 结构化日志（每请求含每次重派至多一条）。
type DispatchDecisionEvent struct {
	TraceID         string
	SystemAccountID string
	APIKeyID        string
	GroupID         string
	TrafficSource   string
	DurationMs      int64
	Summary         DispatchDecisionSummary
}

// notifyDispatchDecisionObserver 是进程级决策观察槽（与
// notifyOneRecoverableUnavailableRuntimeWaiter 同模式）：默认 no-op；
// 组合根经 SetDispatchDecisionObserver 装配 slog 投影。panic-safe 契约由
// 观察者实现方承担（cmd 侧 adapter 整体 recover，观测故障绝不影响主链路）。
var notifyDispatchDecisionObserver = func(DispatchDecisionEvent) {}

// SetDispatchDecisionObserver 装配候选准备决策观察者（组合根启动期调用；
// nil 显式降级为 no-op）。
func SetDispatchDecisionObserver(observer func(DispatchDecisionEvent)) {
	if observer == nil {
		observer = func(DispatchDecisionEvent) {}
	}
	notifyDispatchDecisionObserver = observer
}

// releaseHalfOpenLease mirrors releaseHalfOpenLease.
func releaseHalfOpenLease(ctx context.Context, lease HalfOpenLease) bool {
	if lease == nil {
		return false
	}
	released, err := lease.Release()
	if released && err == nil {
		notifyOneRecoverableUnavailableRuntimeWaiter(lease.RuntimeKey())
	}
	return released
}

// completeHalfOpenLeaseSuccess mirrors completeHalfOpenLeaseSuccess.
func completeHalfOpenLeaseSuccess(ctx context.Context, lease HalfOpenLease) bool {
	if lease == nil {
		return false
	}
	completed, err := lease.CompleteSuccess()
	if completed && err == nil {
		notifyOneRecoverableUnavailableRuntimeWaiter(lease.RuntimeKey())
	}
	return completed
}

// notifyOneRecoverableUnavailableRuntimeWaiter is the waiter wake hook (Node
// runtime/recoverable-unavailable-wait.ts); the concrete wake surface is
// wired by G20, nil = no-op.
var notifyOneRecoverableUnavailableRuntimeWaiter = func(runtimeKey string) {}

// SetRecoverableUnavailableRuntimeWaiterNotifier wires the half-open lease
// wake hook (D-134, BUG-0175): releasing or completing a half-open lease
// wakes the recoverable waiters parked on that runtime key. The composition
// root binds it to the shared wait coordinator.
func SetRecoverableUnavailableRuntimeWaiterNotifier(notify func(runtimeKey string)) {
	if notify == nil {
		notify = func(string) {}
	}
	notifyOneRecoverableUnavailableRuntimeWaiter = notify
}

// requestRouteFallback mirrors requestRouteFallback with the account-lock
// guard from preparation.ts.
func (p *CandidatePipeline) requestRouteFallback(
	ctx context.Context,
	input gatewaypreauth.DispatchPreparationInput,
	candidateAccounts []AccountCandidate,
	reason string,
) (attempted bool, contextValue any, err error) {
	if reason != "authorization_quota_exceeded" && p.engine.Locks != nil {
		ids := make([]string, 0, len(candidateAccounts))
		for _, account := range candidateAccounts {
			ids = append(ids, account.ID)
		}
		states, stateErr := p.engine.Locks.ListStatesAsync(ctx, ids)
		if stateErr != nil {
			return false, nil, stateErr
		}
		for _, state := range states {
			if accountLockBlocksCrossAccount(state) {
				return false, nil, nil
			}
		}
	}
	fallback, err := input.RouteCoordinator.RequestFallback(ctx, reason)
	if err != nil {
		return false, nil, err
	}
	return fallback.Attempted, fallback.Context, nil
}

// PrepareOpenAIGatewayDispatchAccounts mirrors
// prepareOpenAIGatewayDispatchAccounts.
func (p *CandidatePipeline) PrepareOpenAIGatewayDispatchAccounts(ctx context.Context, input gatewaypreauth.DispatchPreparationInput) (PreparationResult, error) {
	e := p.engine
	// W1b：决策摘要的 stage 时长起点（gateway_dispatch_decision.durationMs）。
	prepareStartedAtMs := gatewayupstream.NowMs()
	dispatchOrderingOptions := AffinityOrderingOptions{
		GroupType:        groupTypeOf(input.GroupAccess),
		SchedulingPolicy: input.GroupAccess.SchedulingPolicy,
		ModelPriority:    input.ModelPriority,
		TrafficMigrationScope: &AffinityScope{
			SystemAccountID: input.SystemAccountID,
			APIKeyID:        input.APIKeyID,
			GroupID:         input.GroupID,
		},
	}

	orderedCandidateAccounts, err := e.Affinity.OrderAsync(ctx, input.CandidateAccounts, input.SessionAffinityKey, dispatchOrderingOptions)
	if err != nil {
		return PreparationResult{}, err
	}

	bypassLocalSuppression := input.IgnoreAccountRuntimeSuppression || isAccountProbeTrafficSource(input.UsageContext.TrafficSource)
	var initialLocalSuppressionFilter SuppressionFilterResult
	if bypassLocalSuppression {
		initialLocalSuppressionFilter = localSuppressionBypassResult(orderedCandidateAccounts)
	} else {
		initialLocalSuppressionFilter, err = e.Suppression.FilterAsync(ctx, orderedCandidateAccounts, SuppressionFilterOptions{})
		if err != nil {
			return PreparationResult{}, err
		}
	}

	precheckHalfOpenEligible := false
	if initialLocalSuppressionFilter.AllSuppressed {
		fallbackAttempted, fallbackContext, err := p.requestRouteFallback(ctx, input, orderedCandidateAccounts, "local_account_suppressed")
		if err != nil {
			return PreparationResult{}, err
		}
		if fallbackAttempted {
			input.AuditCapture.AddGatewayMetadata("local_account_suppression", map[string]any{
				"suppressedCount":      initialLocalSuppressionFilter.SuppressedCount,
				"suppressedAccountIds": initialLocalSuppressionFilter.SuppressedAccountIDs,
				"allSuppressed":        true,
				"nextRetryAfterMs":     initialLocalSuppressionFilter.NextRetryAfterMs,
				"fallbackAttempted":    true,
			})
			return PreparationResult{Outcome: PreparationOutcomeFallback, Reason: "local_account_suppressed", Context: fallbackContext}, nil
		}
		precheckHalfOpenEligible = initialLocalSuppressionFilter.PrecheckSuppressedAccountIDs != nil &&
			len(initialLocalSuppressionFilter.PrecheckSuppressedAccountIDs) == len(orderedCandidateAccounts) &&
			len(initialLocalSuppressionFilter.ConfiguredPolicySuppressedAccountIDs) == 0
	}

	var localSuppressionFilter *SuppressionFilterResult
	if bypassLocalSuppression {
		bypassed := localSuppressionBypassResult(orderedCandidateAccounts)
		localSuppressionFilter = &bypassed
	} else if precheckHalfOpenEligible {
		bypassed := localSuppressionBypassResult(orderedCandidateAccounts)
		localSuppressionFilter = &bypassed
	} else {
		resolved, completed, err := e.Suppression.ResolveLocalSuppressionFilter(ctx, LocalSuppressionPreflightInput{
			Req:                      input.Req,
			UsageContext:             input.UsageContext,
			AuditCapture:             p.engine.auditCaptureOf(input.AuditCapture),
			StartedAt:                input.StartedAt,
			Accounts:                 orderedCandidateAccounts,
			SystemAccountID:          input.SystemAccountID,
			APIKeyID:                 input.APIKeyID,
			GroupID:                  input.GroupID,
			ServerRetryBudget:        input.ServerRetryBudget,
			RouteCoordinationBudget:  input.RouteCoordinationBudget,
			GatewayRequestWallBudget: input.GatewayRequestWallBudget,
			RouteCoordinator:         input.RouteCoordinator,
			Signal:                   input.Signal,
		})
		if err != nil {
			return PreparationResult{}, err
		}
		if completed {
			return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
		}
		localSuppressionFilter = resolved
	}
	if localSuppressionFilter == nil {
		return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
	}

	modelRankByAccountID := modelPriorityRankMap(input.ModelPriority)
	runtimeDegradationOrder := e.Degradation.OrderGatewayAccountsByRuntimeDegradation(localSuppressionFilter.Accounts, modelRankByAccountID)
	if runtimeDegradationOrder.Applied || runtimeDegradationOrder.BypassedAllDegraded {
		input.AuditCapture.AddGatewayMetadata("runtime_account_degradation", map[string]any{
			"applied":             runtimeDegradationOrder.Applied,
			"degradedCount":       runtimeDegradationOrder.DegradedCount,
			"degradedAccountIds":  runtimeDegradationOrder.DegradedAccountIDs,
			"bypassedAllDegraded": runtimeDegradationOrder.BypassedAllDegraded,
		})
	}
	if runtimeDegradationOrder.BypassedAllDegraded {
		fallbackAttempted, fallbackContext, err := p.requestRouteFallback(ctx, input, orderedCandidateAccounts, "runtime_degraded")
		if err != nil {
			return PreparationResult{}, err
		}
		if fallbackAttempted {
			return PreparationResult{Outcome: PreparationOutcomeFallback, Reason: "runtime_degraded", Context: fallbackContext}, nil
		}
	}

	latencyDegradationOrder, err := e.Latency.OrderAsync(ctx, runtimeDegradationOrder.Accounts, &LatencyScopeInput{
		SystemAccountID: input.SystemAccountID,
		RouteStrategyID: input.RouteStrategyID,
		GroupID:         input.GroupID,
	}, input.NormalRouteSpeedFirstConfig, input.ModelPriority)
	if err != nil {
		return PreparationResult{}, err
	}
	if latencyDegradationOrder.Applied || latencyDegradationOrder.BypassedAllDegraded {
		input.AuditCapture.AddGatewayMetadata("normal_route_latency_degradation", map[string]any{
			"applied":             latencyDegradationOrder.Applied,
			"degradedAccountIds":  latencyDegradationOrder.DegradedAccountIDs,
			"bypassedAllDegraded": latencyDegradationOrder.BypassedAllDegraded,
		})
	}

	proxyHealthOrder, err := e.ProxyHealth.OrderAsync(ctx, latencyDegradationOrder.Accounts, input.ModelPriority)
	if err != nil {
		return PreparationResult{}, err
	}
	if proxyHealthOrder.Applied || proxyHealthOrder.BypassedAllAvoided {
		input.AuditCapture.AddGatewayMetadata("upstream_bucket_health_avoidance", map[string]any{
			"applied":            proxyHealthOrder.Applied,
			"avoidedBucketKeys":  proxyHealthOrder.AvoidedBucketKeys,
			"avoidedProxyKeys":   proxyHealthOrder.AvoidedProxyKeys,
			"avoidedAccountIds":  proxyHealthOrder.AvoidedAccountIDs,
			"halfOpenBucketKeys": proxyHealthOrder.HalfOpenBucketKeys,
			"halfOpenAccountIds": proxyHealthOrder.HalfOpenAccountIDs,
			"bypassedAllAvoided": proxyHealthOrder.BypassedAllAvoided,
		})
	}

	clientIpAccountAvoidance, err := e.ClientIPAvoidance.OrderAsync(ctx, proxyHealthOrder.Accounts, ClientIPAvoidanceScope{
		SystemAccountID: input.SystemAccountID,
		APIKeyID:        input.APIKeyID,
		GroupID:         input.GroupID,
		ClientIP:        input.ClientIP,
	}, input.ModelPriority)
	if err != nil {
		return PreparationResult{}, err
	}
	if clientIpAccountAvoidance.Applied || clientIpAccountAvoidance.BypassedAllAvoided {
		input.AuditCapture.AddGatewayMetadata("client_ip_account_avoidance", map[string]any{
			"applied":            clientIpAccountAvoidance.Applied,
			"avoidedAccountIds":  clientIpAccountAvoidance.AvoidedAccountIDs,
			"bypassedAllAvoided": clientIpAccountAvoidance.BypassedAllAvoided,
		})
	}

	clientSourceAvoidance, err := e.ClientSourceAvoidance.OrderAsync(ctx, clientIpAccountAvoidance.Accounts, input.ClientStrategy, input.ModelPriority)
	if err != nil {
		return PreparationResult{}, err
	}
	if clientSourceAvoidance.Applied || clientSourceAvoidance.BypassedAllAvoided {
		input.AuditCapture.AddGatewayMetadata("client_source_account_avoidance", map[string]any{
			"applied":            clientSourceAvoidance.Applied,
			"failureCount":       clientSourceAvoidance.FailureCount,
			"avoidedAccountIds":  clientSourceAvoidance.AvoidedAccountIDs,
			"bypassedAllAvoided": clientSourceAvoidance.BypassedAllAvoided,
		})
	}

	latencyDegradedAccountIDs := make(map[string]struct{}, len(latencyDegradationOrder.DegradedAccountIDs))
	for _, id := range latencyDegradationOrder.DegradedAccountIDs {
		latencyDegradedAccountIDs[id] = struct{}{}
	}
	readyPreparation, err := p.prepareQuotaAndCapacityReadyAccounts(ctx, quotaCapacityInput{
		input:                        input,
		accounts:                     clientSourceAvoidance.Accounts,
		dispatchOrderingOptions:      dispatchOrderingOptions,
		latencyDegradedAccountIDs:    latencyDegradedAccountIDs,
		hotQualityMode:               hotQualityModeFor(input.NormalRouteSpeedFirstConfig),
		eligibleFirstPrimaryDispatch: input.UsageContext.TrafficSource == "gateway",
	})
	if err != nil {
		return PreparationResult{}, err
	}
	if readyPreparation.Outcome != PreparationOutcomeReady {
		return readyPreparation, nil
	}
	readyAccounts := readyPreparation.Accounts

	// Session affinity claim (Node: claimOpenAIAccountForSessionAsync).
	{
		accountsBeforeSessionAffinityClaim := readyAccounts
		proposedAccountID := ""
		if len(readyAccounts) > 0 {
			proposedAccountID = readyAccounts[0].ID
		}
		claimedAccountID := ""
		if proposedAccountID != "" {
			claimedAccountID, _ = e.Affinity.ClaimAsync(ctx, input.SessionAffinityKey, proposedAccountID, AffinityScope{
				SystemAccountID: input.SystemAccountID,
				APIKeyID:        input.APIKeyID,
				GroupID:         input.GroupID,
			})
		}
		containsClaimed := false
		for _, account := range readyAccounts {
			if account.ID == claimedAccountID {
				containsClaimed = true
				break
			}
		}
		if claimedAccountID != "" && claimedAccountID != proposedAccountID && containsClaimed {
			readyAccounts, err = e.Affinity.OrderAsync(ctx, readyAccounts, input.SessionAffinityKey, dispatchOrderingOptions)
			if err != nil {
				readyPreparation.ReleaseClientIPConcurrency()
				if readyPreparation.SettleHotQualityExplorationAfterDispatch != nil {
					_ = readyPreparation.SettleHotQualityExplorationAfterDispatch(ctx, "not_dispatched")
				}
				return PreparationResult{}, err
			}
			_ = accountsBeforeSessionAffinityClaim
		}
		if input.SessionAffinityKey != "" && proposedAccountID != "" {
			winnerAvailable := false
			if claimedAccountID != "" {
				for _, account := range readyAccounts {
					if account.ID == claimedAccountID {
						winnerAvailable = true
						break
					}
				}
			}
			applied := len(readyAccounts) > 0 && readyAccounts[0].ID == claimedAccountID
			input.AuditCapture.AddGatewayMetadata("session_affinity_claim", map[string]any{
				"proposedAccountId": proposedAccountID,
				"claimedAccountId":  claimedAccountID,
				"winnerAvailable":   winnerAvailable,
				"applied":           applied,
			})
		}
	}

	readyPreparation.Accounts = readyAccounts
	readyPreparation.PrecheckHalfOpenEligible = precheckHalfOpenEligible
	readyPreparation.NormalRouteLatencyDegradationApplied = latencyDegradationOrder.Applied
	readyPreparation.CodexTurnAccountAvoidanceApplied = clientSourceAvoidance.ThresholdReached
	readyPreparation.CodexTurnAvoidedAccountIDs = clientSourceAvoidance.AvoidedAccountIDs

	// W1b：候选准备完成——把窗口既有决策数据汇成可序列化摘要。写入审计
	// metadata 标签（门控沿用现有审计开关，不绕过），并经进程级观察槽发出
	//（组合根装配 gateway_dispatch_decision slog 日志；每请求含每次重派
	// 至多一条）。fallback/completed 出口不在此汇总：原因已在既有审计标签
	//（local_account_suppression 等）与终局响应上。
	decisionSummary := BuildDispatchDecisionSummary(DispatchDecisionSummaryInput{
		CandidateTotal: len(input.CandidateAccounts),
		Eligible:       readyAccounts,
		ModelPriority:  input.ModelPriority,
		// W1b 续：预过滤跳过明细从端口类型投影回引擎侧同形结构。
		PreFilterSkipped: accountSkipsOfPreFilter(input.PreFilterSkipped),
		QuotaDenied:      readyPreparation.QuotaDeniedAccountIDs,
		Busy:             readyPreparation.CapacityBusyAccountIDs,
		Suppressed:       localSuppressionFilter.SuppressedAccountIDs,
		Degraded: append(append([]string{}, runtimeDegradationOrder.DegradedAccountIDs...),
			latencyDegradationOrder.DegradedAccountIDs...),
		Avoided: append(append(append([]string{},
			proxyHealthOrder.AvoidedAccountIDs...),
			clientIpAccountAvoidance.AvoidedAccountIDs...),
			clientSourceAvoidance.AvoidedAccountIDs...),
	})
	input.AuditCapture.AddGatewayMetadata("gateway_dispatch_candidates", decisionSummary.AuditMetadata())
	notifyDispatchDecisionObserver(DispatchDecisionEvent{
		TraceID:         input.UsageContext.TraceID,
		SystemAccountID: input.SystemAccountID,
		APIKeyID:        input.APIKeyID,
		GroupID:         input.GroupID,
		TrafficSource:   input.UsageContext.TrafficSource,
		DurationMs:      gatewayupstream.NowMs() - prepareStartedAtMs,
		Summary:         decisionSummary,
	})
	return readyPreparation, nil
}

// quotaCapacityInput mirrors prepareQuotaAndCapacityReadyAccounts' input.
type quotaCapacityInput struct {
	input                        gatewaypreauth.DispatchPreparationInput
	accounts                     []AccountCandidate
	dispatchOrderingOptions      AffinityOrderingOptions
	latencyDegradedAccountIDs    map[string]struct{}
	hotQualityMode               string
	eligibleFirstPrimaryDispatch bool
}

// accountSkipsOfPreFilter 把端口层的预过滤跳过明细投影回引擎侧同形结构
// （gatewaypreauth.AccountSkipDetail 与 AccountSkip 的 id/reason 键一致）。
func accountSkipsOfPreFilter(details []gatewaypreauth.AccountSkipDetail) []AccountSkip {
	if len(details) == 0 {
		return nil
	}
	out := make([]AccountSkip, 0, len(details))
	for _, detail := range details {
		out = append(out, AccountSkip{AccountID: detail.AccountID, Reason: detail.Reason})
	}
	return out
}

// hotQualityModeFor mirrors hotQualityModeFor.
func hotQualityModeFor(config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig) string {
	if config != nil {
		return HotQualityModeSpeedFirst
	}
	return HotQualityModeCostFirst
}

func groupTypeOf(groupAccess gatewayruntimecache.GroupUsageAccessMetadata) string {
	if groupAccess.GroupType == nil {
		return ""
	}
	return *groupAccess.GroupType
}

func isAccountProbeTrafficSource(trafficSource string) bool {
	// usage/traffic-source.ts isAccountProbeTrafficSource.
	return trafficSource == "probe" || trafficSource == "health_check"
}

// prepareQuotaAndCapacityReadyAccounts mirrors
// prepareQuotaAndCapacityReadyAccounts.
func (p *CandidatePipeline) prepareQuotaAndCapacityReadyAccounts(ctx context.Context, input quotaCapacityInput) (PreparationResult, error) {
	e := p.engine
	req := input.input
	var authorizationQuotaDeniedAccountCount int
	var quotaDeniedAccountIDs []string
	var capacityBusyAccountIDs []string
	accounts := []AccountCandidate{}
	var hotQualityExplorationReservation *HotQualityReservation
	var settleHotQualityExplorationAfterDispatch func(ctx context.Context, outcome string) error
	var err error

	// T4/B14（合并路由设计）：merge 上下文跳过窗口级配额批查——此处用窗口
	// 组 GroupAccess 对整池 CheckBatchAsync，窗口组为带组级配额的授权组时会
	// 否决整池（首组配额耗尽 + 次组充足也 429）。merge 的权威门是解析期逐
	// 片段批查（组合根 chain_routing.go resolveMergeSelectedRoute），此门有
	// 意跳过；全池实际耗尽由下方 no_available_upstream_account 终局承接。
	if req.SkipGroupQuotaWindowCheck {
		accounts = input.accounts
	} else {
		accountQuotaDecisions, batchErr := e.Quota.CheckBatchAsync(ctx, req.GroupAccess, input.accounts)
		if batchErr != nil {
			return PreparationResult{}, batchErr
		}
		for _, account := range input.accounts {
			decision, ok := accountQuotaDecisions[account.ID]
			if ok && !decision.Allowed {
				authorizationQuotaDeniedAccountCount++
				// W1b：被否决账户 ID 随 ready 出口汇入决策摘要。
				quotaDeniedAccountIDs = append(quotaDeniedAccountIDs, account.ID)
				continue
			}
			accounts = append(accounts, account)
		}
	}

	// capacity.account_snapshot: high-concurrency groups refresh the
	// concurrency snapshot.
	if input.dispatchOrderingOptions.GroupType == gatewayruntimecacheGroupTypeHighConcurrency {
		accounts, err = RefreshGatewayAccountCurrentConcurrencyAsync(ctx, e.Concurrency, accounts)
		if err != nil {
			return PreparationResult{}, err
		}
	}

	if len(accounts) == 0 {
		if authorizationQuotaDeniedAccountCount > 0 {
			fallbackAttempted, fallbackContext, err := p.requestRouteFallback(ctx, req, accounts, "authorization_quota_exceeded")
			if err != nil {
				return PreparationResult{}, err
			}
			if fallbackAttempted {
				return PreparationResult{Outcome: PreparationOutcomeFallback, Reason: "authorization_quota_exceeded", Context: fallbackContext}, nil
			}
			if err := req.RouteCoordinator.CompleteFailure(ctx, gatewayrouting.GatewayRouteFinalFailure{
				StatusCode: 429,
				Message:    gatewayquotaAuthorizationQuotaExceededMessage,
				ErrorType:  "rate_limit_exceeded",
				ErrorCode:  "rate_limit_exceeded",
				ErrorPhase: "quota",
			}); err != nil {
				return PreparationResult{}, err
			}
			return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
		}
		if err := req.RouteCoordinator.CompleteFailure(ctx, gatewayrouting.GatewayRouteFinalFailure{
			StatusCode: 503,
			Message:    "没有可用的上游账户",
			ErrorType:  "service_unavailable",
			ErrorCode:  "no_available_upstream_account",
			ErrorPhase: "dispatch",
		}); err != nil {
			return PreparationResult{}, err
		}
		return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
	}

	busyOptions := HighConcurrencyBusyOptions{AffinityOrderingOptions: input.dispatchOrderingOptions, RequestLane: req.RequestLane}

	highConcurrencyBusy, err := e.Affinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, busyOptions)
	if err != nil {
		return PreparationResult{}, err
	}
	if highConcurrencyBusy {
		refreshed, err := RefreshGatewayAccountCurrentConcurrencyAsync(ctx, e.Concurrency, accounts)
		if err != nil {
			return PreparationResult{}, err
		}
		accounts, err = e.Affinity.OrderAsync(ctx, refreshed, req.SessionAffinityKey, input.dispatchOrderingOptions)
		if err != nil {
			return PreparationResult{}, err
		}
	}

	applyHotQualityOrder := func() error {
		hotQualityOrder, err := e.HotQuality.OrderAsync(ctx, HotQualityOrderInput{
			Accounts:                     accounts,
			ModelPriority:                req.ModelPriority,
			Mode:                         input.hotQualityMode,
			SystemAccountID:              req.SystemAccountID,
			RouteStrategyID:              req.RouteStrategyID,
			GroupID:                      req.GroupID,
			RequestLane:                  req.RequestLane,
			Model:                        requestModelOrEmpty(req.Req),
			RequestID:                    req.UsageContext.TraceID,
			LatencyDegradedAccountIDs:    input.latencyDegradedAccountIDs,
			EligibleFirstPrimaryDispatch: input.eligibleFirstPrimaryDispatch,
		})
		if err != nil {
			return err
		}
		accounts = hotQualityOrder.Accounts
		if input.hotQualityMode == HotQualityModeSpeedFirst && len(input.latencyDegradedAccountIDs) > 0 {
			healthy := make([]AccountCandidate, 0, len(accounts))
			degraded := make([]AccountCandidate, 0, len(accounts))
			for _, account := range accounts {
				if _, isDegraded := input.latencyDegradedAccountIDs[account.ID]; isDegraded {
					degraded = append(degraded, account)
				} else {
					healthy = append(healthy, account)
				}
			}
			accounts = append(append([]AccountCandidate{}, healthy...), degraded...)
		}
		hotQualityExplorationReservation = hotQualityOrder.ExplorationReservation
		settleHotQualityExplorationAfterDispatch = hotQualityOrder.SettleExplorationAfterDispatch
		if hotQualityOrder.DispatchIntent == "same_tier_exploration" || len(hotQualityOrder.QualityReorderedTierKeys) > 0 {
			req.AuditCapture.AddGatewayMetadata("hot_quality_candidate_selection", map[string]any{
				"dispatchIntent":                 hotQualityOrder.DispatchIntent,
				"selectedAccountId":              hotQualityOrder.SelectedAccountID,
				"explorationStatus":              hotQualityOrder.ExplorationStatus,
				"qualityReorderedTierKeys":       hotQualityOrder.QualityReorderedTierKeys,
				"latencyDegradedOverrideApplied": hotQualityOrder.LatencyDegradedOverrideApplied,
			})
		}
		return nil
	}

	if input.dispatchOrderingOptions.GroupType == gatewayruntimecacheGroupTypeHighConcurrency {
		if err := applyHotQualityOrder(); err != nil {
			return PreparationResult{}, err
		}
	}

	highConcurrencyBusy, err = e.Affinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, busyOptions)
	if err != nil {
		return PreparationResult{}, err
	}
	if highConcurrencyBusy {
		fallbackAttempted, fallbackContext, err := p.requestRouteFallback(ctx, req, accounts, "high_concurrency_group_busy")
		if err != nil {
			return PreparationResult{}, err
		}
		if fallbackAttempted {
			return PreparationResult{Outcome: PreparationOutcomeFallback, Reason: "high_concurrency_group_busy", Context: fallbackContext}, nil
		}
	}

	if input.dispatchOrderingOptions.GroupType != gatewayruntimecacheGroupTypeHighConcurrency {
		capacityBusy, err := AreGatewayAccountsCapacityBusyForLaneAsync(ctx, e.Concurrency, accounts, gatewayprotoLane(req.RequestLane), req.GroupAccess.SchedulingPolicy)
		if err != nil {
			return PreparationResult{}, err
		}
		if capacityBusy {
			fallbackAttempted, fallbackContext, err := p.requestRouteFallback(ctx, req, accounts, "group_capacity_busy")
			if err != nil {
				return PreparationResult{}, err
			}
			if fallbackAttempted {
				return PreparationResult{Outcome: PreparationOutcomeFallback, Reason: "group_capacity_busy", Context: fallbackContext}, nil
			}
		}
		// W1b：lane 容量排序同时带出排序前快照中的 busy 候选 ID（busy 候选
		// 仍留在窗口内，仅排序降位）。
		accounts, capacityBusyAccountIDs, err = OrderGatewayAccountsByLaneCapacityAvailabilityWithBusyAsync(ctx, e.Concurrency, accounts, gatewayprotoLane(req.RequestLane), req.GroupAccess.SchedulingPolicy, req.ModelPriority)
		if err != nil {
			return PreparationResult{}, err
		}
		if err := applyHotQualityOrder(); err != nil {
			return PreparationResult{}, err
		}
		// Keep busy candidates in the dispatch context (Node parity comment:
		// the upstream dispatcher owns bounded capacity waiting).
	}

	var releaseClientIPConcurrency func() = noopRelease
	var releaseOnce sync.Once
	releaseClientIPConcurrencyOnce := func() {
		releaseOnce.Do(func() {
			releaseClientIPConcurrency()
		})
	}

	fail := func(err error) (PreparationResult, error) {
		releaseClientIPConcurrencyOnce()
		return PreparationResult{}, err
	}

	if input.dispatchOrderingOptions.GroupType == gatewayruntimecacheGroupTypeHighConcurrency {
		clientIpConcurrency, err := e.ClientIPConcurrency.Acquire(ctx, ClientIPConcurrencyInput{
			SystemAccountID: req.SystemAccountID,
			GroupID:         req.GroupID,
			APIKeyID:        req.APIKeyID,
			ClientIP:        req.ClientIP,
			Policy:          req.GroupAccess.SchedulingPolicy,
			Signal:          req.Signal,
		})
		if err != nil {
			return PreparationResult{}, err
		}
		if clientIpConcurrency.Acquired && clientIpConcurrency.Release != nil {
			releaseClientIPConcurrency = clientIpConcurrency.Release
		}
		if clientIpConcurrency.Enabled {
			req.AuditCapture.AddGatewayMetadata("high_concurrency_client_ip", clientIpConcurrencyAuditMetadata(clientIpConcurrency))
		}
		if !clientIpConcurrency.Acquired {
			if signalAborted(req.Signal) || resWritableEnded(ctx) {
				return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
			}
			if err := req.RouteCoordinator.CompleteFailure(ctx, gatewayrouting.GatewayRouteFinalFailure{
				StatusCode:         429,
				Message:            clientIpConcurrencyFailureMessage(clientIpConcurrency),
				ErrorType:          "rate_limit_exceeded",
				ErrorCode:          "rate_limit_exceeded",
				ErrorPhase:         "dispatch",
				FailureAttribution: "gateway_capacity",
			}); err != nil {
				return PreparationResult{}, err
			}
			return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
		}
		if signalAborted(req.Signal) || resWritableEnded(ctx) {
			releaseClientIPConcurrencyOnce()
			return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
		}
	}

	highConcurrencyBusy, err = e.Affinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, busyOptions)
	if err != nil {
		return fail(err)
	}
	if highConcurrencyBusy {
		queueWaitStartedAtMs := gatewayupstream.NowMs()
		req.ServerRetryBudget.BeginNoAvailableWait(&queueWaitStartedAtMs)
		queueWait, waitErr := func() (QueueWaitResult, error) {
			defer req.ServerRetryBudget.PauseNoAvailableWait(&queueWaitStartedAtMs)
			serverRetryRemainingMs := req.ServerRetryBudget.RemainingMs(&queueWaitStartedAtMs)
			availableDecisionMs, err := req.GatewayRequestWallBudget.AvailableDecisionMs(gatewayrouting.GatewayRequestWallBudgetDecision{NowMs: &queueWaitStartedAtMs})
			if err != nil {
				return QueueWaitResult{}, err
			}
			maxWaitMs := serverRetryRemainingMs
			if req.RequestLane != "image" && availableDecisionMs < maxWaitMs {
				maxWaitMs = availableDecisionMs
			}
			return e.HighConcurrencyQueue.WaitForCapacity(ctx, HighConcurrencyWaitInput{
				SystemAccountID:          req.SystemAccountID,
				GroupID:                  req.GroupID,
				APIKeyID:                 req.APIKeyID,
				AccountIDs:               gatewaySessionConcurrencyIDs(accounts),
				AccountConcurrencyLimits: GatewayAccountConcurrencyLimitsByAccountID(accounts),
				Lane:                     req.RequestLane,
				Policy:                   req.GroupAccess.SchedulingPolicy,
				MaxWaitMs:                maxWaitMs,
			})
		}()
		if waitErr != nil {
			return fail(waitErr)
		}
		req.AuditCapture.AddGatewayMetadata("high_concurrency_group_queue", map[string]any{
			"ready":     queueWait.Ready,
			"reason":    queueWait.Reason,
			"waitedMs":  queueWait.WaitedMs,
			"queueSize": queueWait.QueueSize,
			"lane":      req.RequestLane,
		})
		if signalAborted(req.Signal) || resWritableEnded(ctx) {
			releaseClientIPConcurrencyOnce()
			return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
		}
		refreshed, err := RefreshGatewayAccountCurrentConcurrencyAsync(ctx, e.Concurrency, accounts)
		if err != nil {
			return fail(err)
		}
		accounts, err = e.Affinity.OrderAsync(ctx, refreshed, req.SessionAffinityKey, input.dispatchOrderingOptions)
		if err != nil {
			return fail(err)
		}
	}

	highConcurrencyBusy, err = e.Affinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, busyOptions)
	if err != nil {
		return fail(err)
	}
	if highConcurrencyBusy {
		fallbackAttempted, fallbackContext, err := p.requestRouteFallback(ctx, req, accounts, "high_concurrency_group_busy")
		if err != nil {
			return fail(err)
		}
		if fallbackAttempted {
			releaseClientIPConcurrencyOnce()
			return PreparationResult{Outcome: PreparationOutcomeFallback, Reason: "high_concurrency_group_busy", Context: fallbackContext}, nil
		}
		releaseClientIPConcurrencyOnce()
		if err := req.RouteCoordinator.CompleteFailure(ctx, gatewayrouting.GatewayRouteFinalFailure{
			StatusCode:         429,
			Message:            "分组繁忙，请稍后重试",
			ErrorType:          "rate_limit_exceeded",
			ErrorCode:          "rate_limit_exceeded",
			ErrorPhase:         "dispatch",
			FailureAttribution: "gateway_capacity",
		}); err != nil {
			return PreparationResult{}, err
		}
		return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
	}

	return PreparationResult{
		Outcome:                                  PreparationOutcomeReady,
		Accounts:                                 accounts,
		ReleaseClientIPConcurrency:               releaseClientIPConcurrencyOnce,
		HotQualityExplorationReservation:         hotQualityExplorationReservation,
		SettleHotQualityExplorationAfterDispatch: settleHotQualityExplorationAfterDispatch,
		QuotaDeniedAccountIDs:                    quotaDeniedAccountIDs,
		CapacityBusyAccountIDs:                   capacityBusyAccountIDs,
	}, nil
}

func clientIpConcurrencyAuditMetadata(decision ClientIPConcurrencyDecision) map[string]any {
	if !decision.Enabled {
		return map[string]any{"enabled": false}
	}
	if decision.Acquired {
		return map[string]any{
			"enabled":                true,
			"acquired":               true,
			"current":                decision.Current,
			"limit":                  decision.Limit,
			"waitedMs":               decision.WaitedMs,
			"queued":                 decision.Queued,
			"queueSizeBeforeAcquire": decision.QueueSizeBeforeAcquire,
		}
	}
	return map[string]any{
		"enabled":   true,
		"acquired":  false,
		"reason":    decision.Reason,
		"current":   decision.Current,
		"limit":     decision.Limit,
		"waitedMs":  decision.WaitedMs,
		"queueSize": decision.QueueSize,
	}
}

func clientIpConcurrencyFailureMessage(decision ClientIPConcurrencyDecision) string {
	if decision.Enabled && !decision.Acquired && decision.Reason == "timeout" {
		return "当前 IP 并发排队等待超时，请稍后重试"
	}
	return "当前 IP 并发已达到分组限制，请稍后重试"
}

func noopRelease() {}

func signalAborted(signal context.Context) bool {
	return signal != nil && signal.Err() != nil
}

// resWritableEnded mirrors input.res.writableEnded: the Go response writer
// closure is injected on the engine (G20 wires the downstream writer state).
var resWritableEnded = func(ctx context.Context) bool { return false }

func requestModelOrEmpty(req *gatewaypreauth.GatewayRequest) string {
	if model, ok := gatewaypreauth.RequestModel(req); ok {
		return model
	}
	return ""
}

// gatewayruntimecacheGroupTypeHighConcurrency mirrors groupType value.
const gatewayruntimecacheGroupTypeHighConcurrency = "high_concurrency"

// gatewayquotaAuthorizationQuotaExceededMessage mirrors
// AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE.
const gatewayquotaAuthorizationQuotaExceededMessage = "额度已用完，请联系管理员提升额度"
