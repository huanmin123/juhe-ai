package main

// W4-B（BUG-0175 D-114）/ R5 speed-first 切换族：普通路由首字截止的决策
// 闭包、切换预留（cutover reservation）的携带/释放/恢复投影，以及响应观测
// 与并发槽获取桥。自 chain_v1.go 按职责拆出（REFACTOR-0007 文件内拆分）；
// 被移动的函数体逐字节保持。

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// releasePendingSpeedFirstReservation 释放请求结束时仍未消费的切号预留。
func (l *v1DispatchLoop) releasePendingSpeedFirstReservation() {
	if l.speedFirstCutoverReservation != nil {
		l.speedFirstCutoverReservation.Release()
		l.speedFirstCutoverReservation = nil
	}
}

// resetSpeedFirstState mirrors the Node group-switch resets (routes.ts:
// 656-659 / 787-790): retry count, candidate narrowing and the pending
// cutover reservation (released).
func (l *v1DispatchLoop) resetSpeedFirstState() {
	l.speedFirstByteRetryCount = 0
	l.speedFirstRetryCandidateAccountIds = nil
	if l.speedFirstCutoverReservation != nil {
		l.speedFirstCutoverReservation.Release()
		l.speedFirstCutoverReservation = nil
	}
	l.speedFirstSlowObservedForAttempt = nil
	// 与 transport timer goroutine 并发（major-2 修复）：清零持观察互斥锁。
	l.speedFirstTotalTimeObservationMu.Lock()
	l.speedFirstTotalTimeSlowObservedForAttempt = nil
	l.speedFirstTotalTimeCutoverSignal = nil
	l.speedFirstTotalTimeObservationMu.Unlock()
}

// settleFirstByteDeadlineCutoverVerdict 把响应面的非流式首字截止切号
// verdict（R5）映射为 NormalRouteFirstByteCutoverError 交给既有
// settleSpeedFirstCutoverError 消费（锁定臂/预留携带收窄重派/无预留耗尽
// 退出臂与审计保持原样）。返回 true = 请求已结算；false = 收窄后继续派发。
func (l *v1DispatchLoop) settleFirstByteDeadlineCutoverVerdict(
	ctx context.Context,
	dispatched gatewaydispatch.UpstreamDispatchResult,
	handling gatewayresponse.UpstreamResponseHandlingResult,
) bool {
	deadline := dispatched.NormalRouteFirstByteDeadline
	if deadline == nil {
		// 响应面仅在尝试级截止存在时产出 cutover verdict；此臂为恒不可达
		// 守卫：保持耗尽契约渲染，避免空 200。
		l.renderDispatchExhaustedWithMessage(ctx, handling.Message, dispatched.Account.ID, dispatched.Account.Name)
		return true
	}
	return l.settleSpeedFirstCutoverError(ctx, &gatewaydispatch.NormalRouteFirstByteCutoverError{
		AccountID:          dispatched.Account.ID,
		AccountName:        dispatched.Account.Name,
		Deadline:           *deadline,
		Message:            handling.Message,
		CutoverReservation: handling.CutoverReservationView,
	}, gatewayproxyhealth.LatencyDimensionFirstByte, 0)
}

// settleSpeedFirstCutoverError 镜像 routes.ts:1295-1345 的
// NormalRouteFirstByteCutoverError 分支。返回 true = 请求已结算（终端退出或
// 锁定重派已在引擎预算内完成——Go 侧引擎把同账户重派内化，锁定臂直接按
// 耗尽退出渲染）；false = 循环继续。dimension/elapsedMs 是审计维度标记
// （设计 6.7：首字切号 first_byte / 总时间切号 total_time；总时间另带
// elapsedMs，首字路径传 0 不带出）。
func (l *v1DispatchLoop) settleSpeedFirstCutoverError(ctx context.Context, cutover *gatewaydispatch.NormalRouteFirstByteCutoverError, dimension string, elapsedMs int64) bool {
	current := l.current
	// routes.ts:1297-1317: a cross-account lock denies the cutover — release
	// the reservation, un-exclude the slow account and settle the request
	// through the exhaustion contract (the Node same-account retry reservation
	// is a dispatch-loop nicety the Go chain does not carry, see the D1 note
	// on settleResponseStreamServerRetry).
	if current.UsageContext.TrafficSource == gatewayTrafficSource && l.c.engine.Locks != nil {
		lockState, err := l.c.engine.Locks.FindStateAsync(ctx, cutover.AccountID)
		if err == nil && lockState != nil && lockState.BlocksCrossAccount {
			if view, ok := cutover.CutoverReservation.(*gatewaydispatch.SpeedFirstCutoverReservationView); ok && view != nil {
				l.releaseCutoverReservation(view)
			}
			l.speedFirstRetryCandidateAccountIds = nil
			delete(l.streamRetryExcludedAccounts, cutover.AccountID)
			l.exhaustDispatchFailedAccountID(cutover.AccountID)
			l.renderDispatchExhaustedWithMessage(ctx, cutover.Message, cutover.AccountID, cutover.AccountName)
			return true
		}
	}
	reservation, _ := cutover.CutoverReservation.(*gatewaydispatch.SpeedFirstCutoverReservationView)
	targetAccountID := ""
	if reservation != nil {
		targetAccountID = reservation.TargetAccountIDValue
	}
	if l.streamRetryExcludedAccounts == nil {
		l.streamRetryExcludedAccounts = map[string]struct{}{}
	}
	l.streamRetryExcludedAccounts[cutover.AccountID] = struct{}{}
	l.speedFirstByteRetryCount++
	retryAllowed := targetAccountID != ""
	cutoverAudit := map[string]any{
		"accountId":               cutover.AccountID,
		"responseHeadersReceived": false,
		"limitingFactor":          cutover.Deadline.LimitingFactor,
		"dimension":               dimension,
		"retryCount":              l.speedFirstByteRetryCount,
		"maxRetries":              chainSpeedFirstMaxRetriesOf(current),
		"retryAllowed":            retryAllowed,
		"retryBlockedReason":      map[bool]string{true: "", false: "cutover_not_confirmed"}[retryAllowed],
		"targetAccountId":         targetAccountID,
	}
	if elapsedMs > 0 {
		cutoverAudit["elapsedMs"] = elapsedMs
	}
	l.auditCapture.AddGatewayMetadata("normal_route_speed_first_retry_dispatch", cutoverAudit)
	if reservation != nil && targetAccountID != "" {
		// routes.ts:1328-1332: carry the reservation into the next dispatch
		// and narrow the window to the reserved target.
		l.speedFirstCutoverReservation = l.attachedConcreteReservationOf(reservation)
		l.speedFirstRetryCandidateAccountIds = map[string]struct{}{targetAccountID: {}}
		return false
	}
	if reservation != nil {
		l.releaseCutoverReservation(reservation)
	}
	// routes.ts:1334-1345: no reservation — the slow account is exhausted and
	// the request settles through the fallback/exhaustion contract.
	l.exhaustDispatchFailedAccountID(cutover.AccountID)
	switch fallback, fallbackErr := l.switchToFallbackGroup(ctx, "normal_route_speed_first_exhausted"); {
	case fallbackErr != nil:
		l.renderUnexpectedDispatchFailure(ctx, fallbackErr)
		return true
	case fallback == v1FallbackCompleted:
		return true
	case fallback == v1FallbackSwitched:
		return false
	}
	l.renderDispatchExhaustedWithMessage(ctx, cutover.Message, cutover.AccountID, cutover.AccountName)
	return true
}

// releaseCutoverReservation 释放引擎预留视图并清空待携带状态。
func (l *v1DispatchLoop) releaseCutoverReservation(reservation *gatewaydispatch.SpeedFirstCutoverReservationView) {
	if reservation != nil && reservation.ReleaseFunc != nil {
		reservation.ReleaseFunc()
	}
	l.speedFirstCutoverReservation = nil
}

// recordAttachedSpeedFirstReservation 记录 attach 成功的视图 → 具体预留映射，
// 供 cutover 错误把预留接回循环携带状态。
func (l *v1DispatchLoop) recordAttachedSpeedFirstReservation(view *gatewaydispatch.SpeedFirstCutoverReservationView, reservation *gatewayhotquality.SpeedFirstCutoverReservation) {
	if view == nil || reservation == nil {
		return
	}
	if l.speedFirstAttachedViews == nil {
		l.speedFirstAttachedViews = map[*gatewaydispatch.SpeedFirstCutoverReservationView]*gatewayhotquality.SpeedFirstCutoverReservation{}
	}
	l.speedFirstAttachedViews[view] = reservation
}

// attachedConcreteReservationOf 已由 speedFirstAttachedViews 映射实现：视图 →
// 具体预留恢复 TakeForAccount 能力。
func (l *v1DispatchLoop) attachedConcreteReservationOf(view *gatewaydispatch.SpeedFirstCutoverReservationView) *gatewayhotquality.SpeedFirstCutoverReservation {
	if view == nil {
		return nil
	}
	concrete := l.speedFirstAttachedViews[view]
	delete(l.speedFirstAttachedViews, view)
	if concrete != nil {
		return concrete
	}
	// 未知视图（不应发生）：至少保留一次确定性释放，不残留并发槽。
	if view.ReleaseFunc != nil {
		view.ReleaseFunc()
	}
	return nil
}

// chainSpeedFirstMaxRetriesOf 取单请求切号上限（缺配置 = 0，禁止切号）。
func chainSpeedFirstMaxRetriesOf(current *gatewaypreauth.DispatchContext) int64 {
	config := chainSpeedFirstRuntimeConfigOf(current.NormalRouteSpeedFirstConfig)
	if config == nil {
		return 0
	}
	return config.MaxFirstByteRetriesPerRequest
}

// ---------------------------------------------------------------------------
// W4-B（BUG-0175 D-114）speed-first 首字截止决策闭包
// ---------------------------------------------------------------------------

// chainFirstByteConfigOf 把 preauth 的首字截止配置投影为 routing 侧类型
// （两包同形状；DispatchContext 载 preauth 投影，coordination 消费 routing）。
func chainFirstByteConfigOf(config *gatewaypreauth.NormalRouteFirstByteRuntimeConfig) *gatewayrouting.NormalRouteFirstByteRuntimeConfig {
	if config == nil {
		return nil
	}
	return &gatewayrouting.NormalRouteFirstByteRuntimeConfig{
		SchedulingPreference: config.SchedulingPreference,
		FirstByteDeadlineMs:  derefInt64Ptr2(config.FirstByteDeadlineMs),
	}
}

// speedFirstDecisionsOf 从时延降级端口上取速度优先决策面；组合根未装配
// （组合测试的 degradedLatency）时返回 nil——决策闭包保持 Node
// runtime 缺席的 continue 语义。
func (l *v1DispatchLoop) speedFirstDecisionsOf() chainSpeedFirstDecisions {
	if decisions, ok := l.c.engine.Latency.(chainSpeedFirstDecisions); ok {
		return decisions
	}
	return nil
}

// speedFirstLatencyScopeOf 镜像 normalRouteLatencyDegradationScope
// （routes.ts:1106-1110）：systemAccountId + apiKey 的 routeStrategyId + groupId。
func (l *v1DispatchLoop) speedFirstLatencyScopeOf(current *gatewaypreauth.DispatchContext) *gatewaydispatch.LatencyScopeInput {
	routeStrategyID := ""
	if current.APIKeyRecord != nil {
		routeStrategyID = current.APIKeyRecord.RouteStrategyID
	}
	scope := gatewayproxyhealth.NormalRouteLatencyDegradationScope(
		current.UsageContext.SystemAccountID, routeStrategyID, current.UsageContext.GroupID)
	if scope == nil {
		return nil
	}
	return &gatewaydispatch.LatencyScopeInput{
		SystemAccountID: scope.SystemAccountID,
		RouteStrategyID: scope.RouteStrategyID,
		GroupID:         scope.GroupID,
	}
}

// speedFirstDeadlineAction mirrors the deadline closure signature the engine
// coordination context consumes.
type speedFirstDeadlineAction = gatewaydispatch.FirstByteDeadlineAction

// onNormalRouteFirstByteDeadline 镜像 routes.ts:1111-1250 的
// onNormalRouteFirstByteDeadline：limiting factor 门、锁检查、慢采样、
// 剩余候选评估、切换预留、审计元数据。decisionErr 兜底与 Node catch 一致：
// 释放预留、warn + 审计、继续当前上游。
func (l *v1DispatchLoop) onNormalRouteFirstByteDeadline(
	ctx context.Context,
	current *gatewaypreauth.DispatchContext,
) func(gatewaydispatch.FirstByteDeadlineDecisionInput, gatewaydispatch.AccountCandidate, gatewayrouting.NormalRouteAttemptFirstByteDeadline, *gatewaydispatch.NormalRouteFirstByteAttemptCoordinator) speedFirstDeadlineAction {
	return func(_ gatewaydispatch.FirstByteDeadlineDecisionInput, account gatewaydispatch.AccountCandidate, deadline gatewayrouting.NormalRouteAttemptFirstByteDeadline, coordinator *gatewaydispatch.NormalRouteFirstByteAttemptCoordinator) speedFirstDeadlineAction {
		switch deadline.LimitingFactor {
		case gatewayrouting.FirstByteLimitingFactorLaneTimeout,
			gatewayrouting.FirstByteLimitingFactorUncommittedAttempt:
			return gatewaydispatch.FirstByteDeadlineActionContinue
		case gatewayrouting.FirstByteLimitingFactorWallPrecommit:
			return gatewaydispatch.FirstByteDeadlineActionAbort
		}
		config := current.NormalRouteSpeedFirstConfig
		if config == nil {
			return gatewaydispatch.FirstByteDeadlineActionContinue
		}
		decisions := l.speedFirstDecisionsOf()
		scope := l.speedFirstLatencyScopeOf(current)
		if decisions == nil || scope == nil {
			return gatewaydispatch.FirstByteDeadlineActionContinue
		}
		action, decisionErr := l.speedFirstDeadlineDecision(ctx, current, account, deadline, coordinator, decisions, scope, config)
		if decisionErr != nil {
			coordinator.ReleaseReservation()
			l.c.observability.Logger().Warn("normal_route_speed_first_local_decision_failed", map[string]any{
				"event":           "normal_route_speed_first_local_decision_failed",
				"stage":           "first_byte_cutover",
				"accountId":       account.ID,
				"routeStrategyId": scope.RouteStrategyID,
				"groupId":         scope.GroupID,
				"error":           decisionErr.Error(),
			}, "普通路由速度优先本地决策失败，继续当前上游")
			l.auditCapture.AddGatewayMetadata("normal_route_speed_first_local_decision_failed", map[string]any{
				"stage":     "first_byte_cutover",
				"accountId": account.ID,
			})
			return gatewaydispatch.FirstByteDeadlineActionContinue
		}
		return action
	}
}

// speedFirstDeadlineDecision 镜像 Node 决策闭包主体（routes.ts:1119-1240）。
func (l *v1DispatchLoop) speedFirstDeadlineDecision(
	ctx context.Context,
	current *gatewaypreauth.DispatchContext,
	account gatewaydispatch.AccountCandidate,
	deadline gatewayrouting.NormalRouteAttemptFirstByteDeadline,
	coordinator *gatewaydispatch.NormalRouteFirstByteAttemptCoordinator,
	decisions chainSpeedFirstDecisions,
	scope *gatewaydispatch.LatencyScopeInput,
	config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig,
) (speedFirstDeadlineAction, error) {
	// routes.ts:1124-1139: a cross-account lock blocks the cutover.
	if current.UsageContext.TrafficSource == gatewayTrafficSource && l.c.engine.Locks != nil {
		lockState, err := l.c.engine.Locks.FindStateAsync(ctx, account.ID)
		if err != nil {
			return "", err
		}
		if lockState != nil && lockState.BlocksCrossAccount {
			l.auditCapture.AddGatewayMetadata("account_lock_speed_first_cutover_denied", map[string]any{
				"accountId": account.ID,
			})
			return gatewaydispatch.FirstByteDeadlineActionContinue, nil
		}
	}
	alreadyDegraded, err := decisions.IsAccountLatencyDegradedAsync(ctx, account, scope)
	if err != nil {
		return "", err
	}
	slowResult, err := decisions.RecordFirstByteSlowAsync(ctx, account, scope, config,
		"普通路由速度优先首字观察阈值 "+fmt.Sprintf("%d", deadline.EffectiveDeadlineMs)+"ms 已到达")
	if err != nil {
		return "", err
	}
	l.speedFirstSlowObservedForAttempt = slowResult
	nextExcluded := make(map[string]struct{}, len(l.streamRetryExcludedAccounts)+1)
	for id := range l.streamRetryExcludedAccounts {
		nextExcluded[id] = struct{}{}
	}
	nextExcluded[account.ID] = struct{}{}
	remainingAccounts, err := l.speedFirstRouteEligibleDispatchAccounts(ctx, current, nextExcluded, scope, decisions)
	if err != nil {
		return "", err
	}
	remainingCandidateCount := len(remainingAccounts)
	typedConfig := chainSpeedFirstRuntimeConfigOf(config)
	maxRetries := int64(0)
	if typedConfig != nil {
		maxRetries = typedConfig.MaxFirstByteRetriesPerRequest
	}
	degradedForCutover := alreadyDegraded || (slowResult != nil && slowResult.Degraded)
	preconditionsMet := degradedForCutover &&
		int64(l.speedFirstByteRetryCount) < maxRetries &&
		remainingCandidateCount > 0
	var reservation *gatewayhotquality.SpeedFirstCutoverReservation
	if preconditionsMet {
		reservation, err = gatewayhotquality.ReserveSpeedFirstCutoverTarget(ctx, gatewayhotquality.SpeedFirstCutoverReservationInput{
			SystemAccountID:       scope.SystemAccountID,
			RouteStrategyID:       scope.RouteStrategyID,
			GroupID:               scope.GroupID,
			SlowAccountID:         chainConcurrencyAccountIDOf(account),
			Targets:               chainCutoverTargetsOf(remainingAccounts),
			Lane:                  string(current.RequestLane),
			GroupSchedulingPolicy: chainSchedulingPolicyValueOf(current.GroupSchedulingPolicy),
			SlotAcquirer:          l.speedFirstSlotAcquirer(current),
		})
		if err != nil {
			return "", err
		}
	}
	cutoverAllowed := false
	if reservation != nil {
		view := speedFirstReservationViewOf(reservation)
		if coordinator.AttachReservation(view) {
			cutoverAllowed = true
			l.recordAttachedSpeedFirstReservation(view, reservation)
		}
	}
	remainingIDs := make([]string, 0, remainingCandidateCount)
	for _, candidate := range remainingAccounts {
		remainingIDs = append(remainingIDs, candidate.ID)
	}
	retryBlockedReason := ""
	if !cutoverAllowed {
		switch {
		case remainingCandidateCount <= 0:
			retryBlockedReason = "no_remaining_candidate"
		case !degradedForCutover:
			retryBlockedReason = "slow_observation_not_degraded"
		case !preconditionsMet:
			retryBlockedReason = "max_retry_exceeded"
		default:
			retryBlockedReason = "target_slot_or_cutover_budget_unavailable"
		}
	}
	slowCount := int64(0)
	var degradedUntil, nextProbeAt *string
	degraded := false
	if slowResult != nil {
		slowCount = slowResult.SlowCount
		degraded = slowResult.Degraded
		degradedUntil = slowResult.DegradedUntil
		nextProbeAt = slowResult.NextProbeAt
	}
	thresholdMs := int64(0)
	if config.FirstByteDeadlineMs != nil {
		thresholdMs = *config.FirstByteDeadlineMs
	}
	l.auditCapture.AddGatewayMetadata("normal_route_speed_first_slow_observed", map[string]any{
		"accountId":                    account.ID,
		"accountName":                  account.Name,
		"dimension":                    gatewayproxyhealth.LatencyDimensionFirstByte,
		"thresholdMs":                  thresholdMs,
		"observedAt":                   "first_byte_deadline",
		"alreadyDegraded":              alreadyDegraded,
		"slowCount":                    slowCount,
		"degraded":                     degraded,
		"degradedUntil":                degradedUntil,
		"nextProbeAt":                  nextProbeAt,
		"cutoverAllowed":               cutoverAllowed,
		"retryBlockedReason":           retryBlockedReason,
		"retryCount":                   l.speedFirstByteRetryCount,
		"maxRetries":                   maxRetries,
		"remainingCandidateCount":      remainingCandidateCount,
		"remainingCandidateAccountIds": remainingIDs,
	})
	if cutoverAllowed {
		return gatewaydispatch.FirstByteDeadlineActionAbort, nil
	}
	if reservation != nil {
		// The reservation lost the attach race; release it deterministically.
		reservation.Release()
	}
	return gatewaydispatch.FirstByteDeadlineActionContinue, nil
}

// speedFirstRouteEligibleDispatchAccounts 镜像 routes.ts:2866-2892：候选窗口
// 减去排除集、减去已尝试账户、减去已降级账户。
func (l *v1DispatchLoop) speedFirstRouteEligibleDispatchAccounts(
	ctx context.Context,
	current *gatewaypreauth.DispatchContext,
	excludedAccountIds map[string]struct{},
	scope *gatewaydispatch.LatencyScopeInput,
	decisions chainSpeedFirstDecisions,
) ([]gatewaydispatch.AccountCandidate, error) {
	remaining := streamRetryDispatchAccounts(current.Accounts, excludedAccountIds)
	if l.budgets.tracker != nil {
		eligible := make([]gatewaydispatch.AccountCandidate, 0, len(remaining))
		for _, account := range remaining {
			registration, err := l.budgets.tracker.CanAttemptAccount(gatewayrouting.CanAttemptAccountInput{
				AccountRuntimeKey:     chainRuntimeKeyOfCandidate(account),
				PhysicalCredentialKey: chainConcurrencyAccountIDOf(account),
			})
			if err != nil || !registration.Allowed {
				continue
			}
			eligible = append(eligible, account)
		}
		remaining = eligible
	}
	if scope == nil || len(remaining) == 0 {
		return remaining, nil
	}
	output := make([]gatewaydispatch.AccountCandidate, 0, len(remaining))
	for _, account := range remaining {
		degraded, err := decisions.IsAccountLatencyDegradedAsync(ctx, account, scope)
		if err != nil {
			return nil, err
		}
		if !degraded {
			output = append(output, account)
		}
	}
	return output, nil
}

// chainSchedulingPolicyValueOf 解引用调度策略指针（nil = 空 map 语义，
// gatewayhotquality 内部按缺省处理）。
func chainSchedulingPolicyValueOf(policy *gatewayruntimecache.GroupSchedulingPolicy) gatewayruntimecache.GroupSchedulingPolicy {
	if policy == nil {
		return nil
	}
	return *policy
}

// chainRuntimeKeyOfCandidate 复用引擎的运行态键投影（owner 账户即 ID，
// 授权账户带绑定上下文）。
func chainRuntimeKeyOfCandidate(account gatewaydispatch.AccountCandidate) string {
	if account.AccountAccessType == "account_authorized" &&
		account.BindingSystemAccountID != nil && account.BoundGroupID != nil && account.AccountAuthorizationID != nil &&
		*account.BindingSystemAccountID != "" && *account.BoundGroupID != "" && *account.AccountAuthorizationID != "" {
		return account.ID + ":authorized:" + *account.BindingSystemAccountID + ":" + *account.BoundGroupID + ":" + *account.AccountAuthorizationID
	}
	return account.ID
}

// chainConcurrencyAccountIDOf 镜像 gatewayAccountConcurrencyAccountId：
// 凭据源账户优先（规范化去空白）。
func chainConcurrencyAccountIDOf(account gatewaydispatch.AccountCandidate) string {
	if account.CredentialSourceAccountID != nil {
		normalized := strings.TrimSpace(*account.CredentialSourceAccountID)
		if normalized != "" {
			return normalized
		}
	}
	return account.ID
}

// chainCutoverTargetsOf 投影切号目标（Node targets 数组）。
func chainCutoverTargetsOf(accounts []gatewaydispatch.AccountCandidate) []gatewayhotquality.GatewayAccountConcurrencyLimitIdentity {
	out := make([]gatewayhotquality.GatewayAccountConcurrencyLimitIdentity, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, gatewayhotquality.GatewayAccountConcurrencyLimitIdentity{
			ID:                        account.ID,
			CredentialSourceAccountID: chainConcurrencyAccountIDOf(account),
			ConcurrencyLimit:          account.ConcurrencyLimit,
		})
	}
	return out
}

// speedFirstSlotAcquirer 把引擎的账户并发存储桥成切号预留的槽获取器
// （Node tryAcquireAccountConcurrencyAsync 共享实现）。
func (l *v1DispatchLoop) speedFirstSlotAcquirer(current *gatewaypreauth.DispatchContext) gatewayhotquality.SpeedFirstCutoverSlotAcquirer {
	return func(ctx context.Context, accountID string, concurrencyLimit int, request gatewayhotquality.AccountConcurrencyAcquireRequest) (gatewayhotquality.AccountConcurrencySlot, bool, error) {
		if l.c.engine.Concurrency == nil {
			return gatewayhotquality.AccountConcurrencySlot{}, false, nil
		}
		options := gatewaydispatch.AccountConcurrencyAcquireOptions{Lane: request.Lane}
		if request.Lane == gatewayhotquality.AccountConcurrencyLaneImage {
			imageLimit := gatewayhotquality.EffectiveImageLaneConcurrencyLimit(concurrencyLimit, chainSchedulingPolicyValueOf(current.GroupSchedulingPolicy))
			options.LaneLimit = &imageLimit
		}
		slot, err := l.c.engine.Concurrency.TryAcquireAsync(ctx, accountID, concurrencyLimit, options)
		if err != nil {
			return gatewayhotquality.AccountConcurrencySlot{}, false, err
		}
		if !slot.Acquired {
			return gatewayhotquality.AccountConcurrencySlot{}, false, nil
		}
		release := slot.Release
		return gatewayhotquality.AccountConcurrencySlot{
			Key:     accountID + ":" + request.Lane,
			Lane:    request.Lane,
			Release: release,
		}, true, nil
	}
}

// speedFirstReservationViewOf 把热质量预留投影为引擎协调器的预留视图。
func speedFirstReservationViewOf(reservation *gatewayhotquality.SpeedFirstCutoverReservation) *gatewaydispatch.SpeedFirstCutoverReservationView {
	if reservation == nil {
		return nil
	}
	return &gatewaydispatch.SpeedFirstCutoverReservationView{
		TargetAccountIDValue: reservation.TargetAccountID(),
		ReleaseFunc:          reservation.Release,
	}
}

// speedFirstReservationHandleOf 把预留包装成引擎的预占并发句柄
// （Node preAcquiredConcurrency）。
func speedFirstReservationHandleOf(reservation *gatewayhotquality.SpeedFirstCutoverReservation) *gatewaydispatch.SpeedFirstCutoverReservationHandle {
	if reservation == nil {
		return nil
	}
	return &gatewaydispatch.SpeedFirstCutoverReservationHandle{
		TakeForAccount: func(account gatewaydispatch.AccountCandidate) (gatewaydispatch.ConcurrencySlot, bool) {
			slot, ok := reservation.TakeForAccount(gatewayhotquality.GatewayAccountConcurrencyLimitIdentity{
				ID:                        account.ID,
				CredentialSourceAccountID: chainConcurrencyAccountIDOf(account),
				ConcurrencyLimit:          account.ConcurrencyLimit,
			})
			if !ok {
				return gatewaydispatch.ConcurrencySlot{}, false
			}
			release := slot.Release
			return gatewaydispatch.ConcurrencySlot{Acquired: true, Release: release}, true
		},
	}
}

// ---------------------------------------------------------------------------
// 总时间兜底截止（设计 6.3-6.7）：决策闭包、切号消费与完成观测
// ---------------------------------------------------------------------------

// speedFirstTotalTimeCutoverSignal 是总时间决策闭包确认切号后的载荷快照：
// transport timer goroutine 写入、settleDispatchError 主循环读取（transport
// 侧 NormalRouteTotalTimeTimeoutError 只带阈值与 elapsed，账户信息留在决策
// 闭包可见的 loop 槽）。
type speedFirstTotalTimeCutoverSignal struct {
	accountID   string
	accountName string
	thresholdMs int64
	elapsedMs   int64
}

// speedFirstCompactionGateOf 镜像 dispatch 侧压缩豁免门
// （upstreamdispatch.go compactionTimeoutsDisabled）：wall budget 无界
// （codex 压缩请求的 TimeoutPolicy 源头）或请求体压缩识别。loop 层与引擎的
// coordination.TimeoutPolicy 同源（newRequestCoordination 以 wall.Unbounded
// 导出该策略），压缩判定由此保持两侧一致。
func (l *v1DispatchLoop) speedFirstCompactionGateOf(current *gatewaypreauth.DispatchContext) bool {
	if l.budgets.wall != nil && l.budgets.wall.Unbounded {
		return true
	}
	return gatewaydispatch.CodexCompactionExpectedForRequest(l.req)
}

// speedFirstDownstreamCommittedOf 判断当前请求是否已向下游写出可见内容
// （设计 6.6 总时间切号安全条件：SemanticCommitted = false 且
// DownstreamBytesWritten = 0 才可切号；已写出的请求对总时间截止只是软观察）。
func (l *v1DispatchLoop) speedFirstDownstreamCommittedOf() bool {
	return l.waitCommitState != nil &&
		(l.waitCommitState.SemanticCommitted || l.waitCommitState.DownstreamBytesWritten > 0)
}

// speedFirstTotalTimeThresholdMsOf 在 chain 层复算该请求的总时间档位阈值：
// 与 dispatch 侧 attempt 装配共用 ResolveNormalRouteTotalTimeDeadline 纯
// 函数（同一样本只用一把尺——timer、软观察与完成补记同档）。ok=false =
// 该请求未装配总时间维度（无速度优先配置 / 非文本 lane / 阈值非法）。
func (l *v1DispatchLoop) speedFirstTotalTimeThresholdMsOf(current *gatewaypreauth.DispatchContext) (int64, bool) {
	config := current.NormalRouteSpeedFirstConfig
	if config == nil {
		return 0, false
	}
	if !gatewayrouting.NormalRouteFirstByteDeadlineAppliesToLane(current.RequestLane) {
		return 0, false
	}
	deadline, ok := gatewaydispatch.ResolveNormalRouteTotalTimeDeadline(gatewaydispatch.NormalRouteTotalTimeDeadlineInput{
		Config:                     config,
		CompactionTimeoutsDisabled: l.speedFirstCompactionGateOf(current),
		EstimatedInputTokens:       estimateChainRequestInputTokens(l.req),
		AttemptStartedAtMs:         l.c.preauth.NowMs(),
	})
	if !ok {
		return 0, false
	}
	return deadline.ThresholdMs, true
}

// estimateChainRequestInputTokens 投影请求体估算输入（与 dispatch 侧
// estimateNormalRouteRequestInputTokens 同源估算；nil body 安全）。
func estimateChainRequestInputTokens(req *gatewaypreauth.GatewayRequest) int {
	if req == nil || req.Body == nil {
		return 0
	}
	tokens, _ := gatewayopenai.EstimateRequestInputTokens(req.Body.Body, req.Body.RawBody)
	return tokens
}

// onNormalRouteTotalTimeDeadline 构造总时间截止到期决策闭包（镜像
// onNormalRouteFirstByteDeadline 的接线形态）：锁检查与候选评估失败一律
// continue 当前上游（Node 决策 catch 语义），确认切号返回 abort 由
// transport 销毁请求进入 NormalRouteTotalTimeTimeoutError 通路。
func (l *v1DispatchLoop) onNormalRouteTotalTimeDeadline(
	ctx context.Context,
	current *gatewaypreauth.DispatchContext,
) func(gatewaydispatch.TotalTimeDeadlineDecisionInput, gatewaydispatch.AccountCandidate, int64) gatewaydispatch.FirstByteDeadlineAction {
	return func(input gatewaydispatch.TotalTimeDeadlineDecisionInput, account gatewaydispatch.AccountCandidate, thresholdMs int64) gatewaydispatch.FirstByteDeadlineAction {
		config := current.NormalRouteSpeedFirstConfig
		if config == nil {
			return gatewaydispatch.FirstByteDeadlineActionContinue
		}
		decisions := l.speedFirstDecisionsOf()
		scope := l.speedFirstLatencyScopeOf(current)
		if decisions == nil || scope == nil {
			return gatewaydispatch.FirstByteDeadlineActionContinue
		}
		action, decisionErr := l.speedFirstTotalTimeDeadlineDecision(ctx, current, account, thresholdMs, input.ElapsedMs, decisions, scope, config)
		if decisionErr != nil {
			// 决策失败兜底（镜像首字 ReleaseReservation）：载荷槽与预留只在
			// 确认切号后写入，err 分支至多释放本决策已写对的组合。
			l.speedFirstTotalTimeObservationMu.Lock()
			signal := l.speedFirstTotalTimeCutoverSignal
			l.speedFirstTotalTimeObservationMu.Unlock()
			if signal != nil {
				if reservation := l.speedFirstCutoverReservation; reservation != nil {
					reservation.Release()
					l.speedFirstCutoverReservation = nil
				}
				l.speedFirstTotalTimeCutoverSignal = nil
			}
			l.c.observability.Logger().Warn("normal_route_speed_first_local_decision_failed", map[string]any{
				"event":           "normal_route_speed_first_local_decision_failed",
				"stage":           "total_time_cutover",
				"accountId":       account.ID,
				"routeStrategyId": scope.RouteStrategyID,
				"groupId":         scope.GroupID,
				"error":           decisionErr.Error(),
			}, "普通路由速度优先本地决策失败，继续当前上游")
			l.auditCapture.AddGatewayMetadata("normal_route_speed_first_local_decision_failed", map[string]any{
				"stage":     "total_time_cutover",
				"accountId": account.ID,
			})
			return gatewaydispatch.FirstByteDeadlineActionContinue
		}
		return action
	}
}

// speedFirstTotalTimeDeadlineDecision 镜像 speedFirstDeadlineDecision 的总
// 时间维度主体（设计 6.3/6.6）：锁阻断 → 降级查询 → 记慢样本 → 写出检查
// （已写出不切号，软观察）→ 剩余候选 → 预占 → 审计 → Abort/Continue。
// 总时间预留不经引擎 coordinator（请求头阶段无原始字节竞争，attach 竞争臂
// 不存在）：预占成功即确认切号，视图经 settleTotalTimeCutoverError 接回
// loop 携带槽收窄重派。
func (l *v1DispatchLoop) speedFirstTotalTimeDeadlineDecision(
	ctx context.Context,
	current *gatewaypreauth.DispatchContext,
	account gatewaydispatch.AccountCandidate,
	thresholdMs int64,
	elapsedMs int64,
	decisions chainSpeedFirstDecisions,
	scope *gatewaydispatch.LatencyScopeInput,
	config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig,
) (speedFirstDeadlineAction, error) {
	// routes.ts:1124-1139 同型：a cross-account lock blocks the cutover.
	if current.UsageContext.TrafficSource == gatewayTrafficSource && l.c.engine.Locks != nil {
		lockState, err := l.c.engine.Locks.FindStateAsync(ctx, account.ID)
		if err != nil {
			return "", err
		}
		if lockState != nil && lockState.BlocksCrossAccount {
			l.auditCapture.AddGatewayMetadata("account_lock_speed_first_cutover_denied", map[string]any{
				"accountId": account.ID,
				"dimension": gatewayproxyhealth.LatencyDimensionTotalTime,
			})
			return gatewaydispatch.FirstByteDeadlineActionContinue, nil
		}
	}
	alreadyDegraded, err := decisions.IsAccountLatencyDegradedAsync(ctx, account, scope)
	if err != nil {
		return "", err
	}
	slowResult, slowErr := l.recordTotalTimeSlowSample(ctx, decisions, scope, config, account,
		"普通路由速度优先总时间观察阈值 "+fmt.Sprintf("%d", thresholdMs)+"ms 已到达")
	if slowErr != nil {
		// 镜像首字决策：慢采样失败向上抛，由决策闭包兜底释放并 continue。
		return "", slowErr
	}
	// 写出检查（设计 6.6 总时间档与首字档的关键差异）：已向下游写出可见内容
	// 的请求不切号——只记慢样本继续等待当前上游（软观察）。
	if l.speedFirstDownstreamCommittedOf() {
		l.auditCapture.AddGatewayMetadata("normal_route_speed_first_total_time_cutover_blocked", map[string]any{
			"accountId":          account.ID,
			"dimension":          gatewayproxyhealth.LatencyDimensionTotalTime,
			"retryBlockedReason": "downstream_committed",
			"elapsedMs":          elapsedMs,
			"thresholdMs":        thresholdMs,
		})
		return gatewaydispatch.FirstByteDeadlineActionContinue, nil
	}
	nextExcluded := make(map[string]struct{}, len(l.streamRetryExcludedAccounts)+1)
	for id := range l.streamRetryExcludedAccounts {
		nextExcluded[id] = struct{}{}
	}
	nextExcluded[account.ID] = struct{}{}
	remainingAccounts, err := l.speedFirstRouteEligibleDispatchAccounts(ctx, current, nextExcluded, scope, decisions)
	if err != nil {
		return "", err
	}
	remainingCandidateCount := len(remainingAccounts)
	typedConfig := chainSpeedFirstRuntimeConfigOf(config)
	maxRetries := int64(0)
	if typedConfig != nil {
		maxRetries = typedConfig.MaxFirstByteRetriesPerRequest
	}
	degradedForCutover := alreadyDegraded || (slowResult != nil && slowResult.Degraded)
	preconditionsMet := degradedForCutover &&
		int64(l.speedFirstByteRetryCount) < maxRetries &&
		remainingCandidateCount > 0
	var reservation *gatewayhotquality.SpeedFirstCutoverReservation
	if preconditionsMet {
		reservation, err = gatewayhotquality.ReserveSpeedFirstCutoverTarget(ctx, gatewayhotquality.SpeedFirstCutoverReservationInput{
			SystemAccountID:       scope.SystemAccountID,
			RouteStrategyID:       scope.RouteStrategyID,
			GroupID:               scope.GroupID,
			SlowAccountID:         chainConcurrencyAccountIDOf(account),
			Targets:               chainCutoverTargetsOf(remainingAccounts),
			Lane:                  string(current.RequestLane),
			GroupSchedulingPolicy: chainSchedulingPolicyValueOf(current.GroupSchedulingPolicy),
			SlotAcquirer:          l.speedFirstSlotAcquirer(current),
		})
		if err != nil {
			return "", err
		}
	}
	cutoverAllowed := false
	if reservation != nil {
		// 预占成功即确认切号：预留写入 loop 携带槽，载荷快照供
		// settleTotalTimeCutoverError 还原账户与 elapsedMs。决策回调跑在
		// transport timer goroutine 上，与轮级清零并发（major-2 修复）：
		// 写入持观察互斥锁，与清零/读取点同锁建立 happens-before。
		cutoverAllowed = true
		l.speedFirstCutoverReservation = reservation
		l.speedFirstTotalTimeObservationMu.Lock()
		l.speedFirstTotalTimeCutoverSignal = &speedFirstTotalTimeCutoverSignal{
			accountID:   account.ID,
			accountName: account.Name,
			thresholdMs: thresholdMs,
			elapsedMs:   elapsedMs,
		}
		l.speedFirstTotalTimeObservationMu.Unlock()
	}
	remainingIDs := make([]string, 0, remainingCandidateCount)
	for _, candidate := range remainingAccounts {
		remainingIDs = append(remainingIDs, candidate.ID)
	}
	retryBlockedReason := ""
	if !cutoverAllowed {
		switch {
		case remainingCandidateCount <= 0:
			retryBlockedReason = "no_remaining_candidate"
		case !degradedForCutover:
			retryBlockedReason = "slow_observation_not_degraded"
		case !preconditionsMet:
			retryBlockedReason = "max_retry_exceeded"
		default:
			retryBlockedReason = "target_slot_or_cutover_budget_unavailable"
		}
	}
	slowCount := int64(0)
	degraded := false
	var degradedUntil, nextProbeAt *string
	if slowResult != nil {
		slowCount = slowResult.SlowCount
		degraded = slowResult.Degraded
		degradedUntil = slowResult.DegradedUntil
		nextProbeAt = slowResult.NextProbeAt
	}
	l.auditCapture.AddGatewayMetadata("normal_route_speed_first_slow_observed", map[string]any{
		"accountId":                    account.ID,
		"accountName":                  account.Name,
		"dimension":                    gatewayproxyhealth.LatencyDimensionTotalTime,
		"thresholdMs":                  thresholdMs,
		"elapsedMs":                    elapsedMs,
		"observedAt":                   "total_time_deadline",
		"alreadyDegraded":              alreadyDegraded,
		"slowCount":                    slowCount,
		"degraded":                     degraded,
		"degradedUntil":                degradedUntil,
		"nextProbeAt":                  nextProbeAt,
		"cutoverAllowed":               cutoverAllowed,
		"retryBlockedReason":           retryBlockedReason,
		"retryCount":                   l.speedFirstByteRetryCount,
		"maxRetries":                   maxRetries,
		"remainingCandidateCount":      remainingCandidateCount,
		"remainingCandidateAccountIds": remainingIDs,
	})
	if cutoverAllowed {
		return gatewaydispatch.FirstByteDeadlineActionAbort, nil
	}
	if reservation != nil {
		// 未确认切号（attach 前置条件失败）的预留确定性释放。
		reservation.Release()
	}
	return gatewaydispatch.FirstByteDeadlineActionContinue, nil
}

// settleTotalTimeCutoverError 消费总时间截止切号（设计 6.6）：复用
// settleSpeedFirstCutoverError 消费链（锁定臂 / 预留携带收窄重派 / 无预留
// 耗尽退出），载荷槽还原账户与 elapsedMs，审计带 dimension=total_time。
func (l *v1DispatchLoop) settleTotalTimeCutoverError(ctx context.Context, totalTimeout *gatewaydispatch.NormalRouteTotalTimeTimeoutError) bool {
	l.speedFirstTotalTimeObservationMu.Lock()
	signal := l.speedFirstTotalTimeCutoverSignal
	l.speedFirstTotalTimeObservationMu.Unlock()
	if signal == nil {
		// 决策未确认切号的 abort（RunTotalTimeDeadlineHandler panic 路径）：
		// 无载荷槽即无重派目标，按耗尽契约渲染，避免空 200。
		l.auditCapture.AddGatewayMetadata("normal_route_speed_first_total_time_decision_failed", map[string]any{
			"accountId":   "",
			"dimension":   gatewayproxyhealth.LatencyDimensionTotalTime,
			"thresholdMs": totalTimeout.TimeoutMs,
		})
		l.renderDispatchExhaustedWithMessage(ctx, totalTimeout.Message, "", "")
		return true
	}
	cutover := &gatewaydispatch.NormalRouteFirstByteCutoverError{
		AccountID:   signal.accountID,
		AccountName: signal.accountName,
		Deadline: gatewayrouting.NormalRouteAttemptFirstByteDeadline{
			ConfiguredDeadlineMs: signal.thresholdMs,
			EffectiveDeadlineMs:  signal.thresholdMs,
			LimitingFactor:       gatewayrouting.FirstByteLimitingFactorConfigured,
		},
		Message: totalTimeout.Message,
	}
	if reservation := l.speedFirstCutoverReservation; reservation != nil {
		view := speedFirstReservationViewOf(reservation)
		l.recordAttachedSpeedFirstReservation(view, reservation)
		cutover.CutoverReservation = view
		l.speedFirstCutoverReservation = nil
	}
	return l.settleSpeedFirstCutoverError(ctx, cutover, gatewayproxyhealth.LatencyDimensionTotalTime, signal.elapsedMs)
}

// armTotalTimeDeadlineObserver 挂响应轮总时间软观察 timer（设计 6.3 采样点
// 1 的 body 阶段承接）：transport 总时间 timer 在响应头到达后由
// responseReceived 守卫失效，本 timer 补上响应处理期的到期观察——只记慢
// 样本（每 attempt 去重一次），不中断当前响应轮；完成时采样点 2/3 由
// observeSpeedFirstResponseOutcome 承接。返回 stop（幂等）。
func (l *v1DispatchLoop) armTotalTimeDeadlineObserver(ctx context.Context, current *gatewaypreauth.DispatchContext, dispatched gatewaydispatch.UpstreamDispatchResult, finished *atomic.Int32) func() {
	thresholdMs, ok := l.speedFirstTotalTimeThresholdMsOf(current)
	if !ok {
		return func() {}
	}
	remainingMs := thresholdMs - (l.c.preauth.NowMs() - dispatched.AttemptStartedAt)
	if remainingMs < 1 {
		remainingMs = 1
	}
	timer := time.AfterFunc(time.Duration(remainingMs)*time.Millisecond, func() {
		if finished.Load() != 0 {
			return
		}
		l.observeTotalTimeDeadlineSample(ctx, current, dispatched, thresholdMs)
	})
	return func() { timer.Stop() }
}

// observeTotalTimeDeadlineSample 是响应轮软观察 timer 的到期执行体：响应
// 未完成（finished=0）时记一次总时间慢样本（去重锁内，每 attempt 一次），
// 不切号。
func (l *v1DispatchLoop) observeTotalTimeDeadlineSample(ctx context.Context, current *gatewaypreauth.DispatchContext, dispatched gatewaydispatch.UpstreamDispatchResult, thresholdMs int64) {
	config := current.NormalRouteSpeedFirstConfig
	if config == nil {
		return
	}
	decisions := l.speedFirstDecisionsOf()
	scope := l.speedFirstLatencyScopeOf(current)
	if decisions == nil || scope == nil {
		return
	}
	elapsedMs := l.c.preauth.NowMs() - dispatched.AttemptStartedAt
	slowResult, slowErr := l.recordTotalTimeSlowSample(ctx, decisions, scope, config, dispatched.Account,
		"普通路由速度优先总时间观察阈值 "+fmt.Sprintf("%d", thresholdMs)+"ms 已到达")
	if slowErr != nil {
		l.warnSpeedFirstDecisionFailure(dispatched.Account, scope, "total_time_observation", slowErr)
		return
	}
	if slowResult == nil {
		return
	}
	l.auditCapture.AddGatewayMetadata("normal_route_speed_first_slow_observed", map[string]any{
		"accountId":     dispatched.Account.ID,
		"dimension":     gatewayproxyhealth.LatencyDimensionTotalTime,
		"thresholdMs":   thresholdMs,
		"elapsedMs":     elapsedMs,
		"observedAt":    "total_time_deadline",
		"slowCount":     slowResult.SlowCount,
		"degraded":      slowResult.Degraded,
		"degradedUntil": slowResult.DegradedUntil,
		"nextProbeAt":   slowResult.NextProbeAt,
	})
}

// recordTotalTimeSlowSample 在总时间维度去重锁内记录慢样本（设计 6.3：每
// attempt 每维度恰好一次；三个写入点——transport 决策闭包、响应轮软观察
// timer、完成观测补记——并发可达，双记即双慢样本）。已有标记返回
// (nil, nil)；采样失败返回错误（决策闭包透传，观测调用方告警吞并）。
func (l *v1DispatchLoop) recordTotalTimeSlowSample(ctx context.Context, decisions chainSpeedFirstDecisions, scope *gatewaydispatch.LatencyScopeInput, config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, account gatewaydispatch.AccountCandidate, reason string) (*gatewayproxyhealth.LatencySlowResult, error) {
	l.speedFirstTotalTimeObservationMu.Lock()
	defer l.speedFirstTotalTimeObservationMu.Unlock()
	if l.speedFirstTotalTimeSlowObservedForAttempt != nil {
		return nil, nil
	}
	slowResult, err := decisions.RecordTotalTimeSlowAsync(ctx, account, scope, config, reason)
	if err != nil {
		return nil, err
	}
	l.speedFirstTotalTimeSlowObservedForAttempt = slowResult
	return slowResult, nil
}

// observeSpeedFirstResponseOutcome 镜像 routes.ts:2393-2455 的响应观测：
// 首字耗时超阈值补记慢采样（同尝试去重），达标则记成功恢复采样；总时间
// 分支按 attempt 选定档位补记慢采样或达标恢复采样（设计 6.3 采样点 2/3）。
func (l *v1DispatchLoop) observeSpeedFirstResponseOutcome(
	ctx context.Context,
	current *gatewaypreauth.DispatchContext,
	dispatched gatewaydispatch.UpstreamDispatchResult,
	handling gatewayresponse.UpstreamResponseHandlingResult,
) {
	config := current.NormalRouteSpeedFirstConfig
	if config == nil {
		return
	}
	decisions := l.speedFirstDecisionsOf()
	scope := l.speedFirstLatencyScopeOf(current)
	if decisions == nil || scope == nil {
		return
	}
	// 压缩守卫（设计 6.3）：压缩请求首字维度维持既有豁免——不补记首字慢
	// 样本、不记首字恢复样本（成功压缩首输出可达 125 秒以上，属正常形态，
	// 不得进入首字慢样本通道）。
	if !l.speedFirstCompactionGateOf(current) && handling.FirstTokenMs != nil {
		l.observeSpeedFirstFirstByteOutcome(ctx, dispatched, handling, decisions, scope, config)
	}
	// 总时间维度照常生效（含压缩请求）：timer 未到点时由完成观测补记慢
	// 样本（采样点 2），elapsed 未超阈值记达标恢复样本（采样点 3）。
	l.observeSpeedFirstTotalTimeOutcome(ctx, current, dispatched, decisions, scope, config)
}

// observeSpeedFirstFirstByteOutcome 承接首字维度的完成观测（原
// observeSpeedFirstResponseOutcome 首字主体）。
func (l *v1DispatchLoop) observeSpeedFirstFirstByteOutcome(
	ctx context.Context,
	dispatched gatewaydispatch.UpstreamDispatchResult,
	handling gatewayresponse.UpstreamResponseHandlingResult,
	decisions chainSpeedFirstDecisions,
	scope *gatewaydispatch.LatencyScopeInput,
	config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig,
) {
	thresholdMs := int64(0)
	if config.FirstByteDeadlineMs != nil {
		thresholdMs = *config.FirstByteDeadlineMs
	}
	if *handling.FirstTokenMs > thresholdMs {
		if l.speedFirstSlowObservedForAttempt != nil {
			return
		}
		slowResult, err := decisions.RecordFirstByteSlowAsync(ctx, dispatched.Account, scope, config,
			"普通路由速度优先首字耗时 "+fmt.Sprintf("%d", *handling.FirstTokenMs)+"ms 超过阈值 "+fmt.Sprintf("%d", thresholdMs)+"ms")
		if err != nil {
			l.warnSpeedFirstDecisionFailure(dispatched.Account, scope, "response_observation", err)
			return
		}
		var slowCount int64
		degraded := false
		var degradedUntil, nextProbeAt *string
		if slowResult != nil {
			slowCount = slowResult.SlowCount
			degraded = slowResult.Degraded
			degradedUntil = slowResult.DegradedUntil
			nextProbeAt = slowResult.NextProbeAt
		}
		l.auditCapture.AddGatewayMetadata("normal_route_speed_first_slow_observed", map[string]any{
			"accountId":     dispatched.Account.ID,
			"dimension":     gatewayproxyhealth.LatencyDimensionFirstByte,
			"firstTokenMs":  *handling.FirstTokenMs,
			"thresholdMs":   thresholdMs,
			"observedAt":    "response_completed",
			"slowCount":     slowCount,
			"degraded":      degraded,
			"degradedUntil": degradedUntil,
			"nextProbeAt":   nextProbeAt,
		})
		return
	}
	recoveryResult, err := decisions.RecordFirstByteSuccessAsync(ctx, dispatched.Account, scope, config, *handling.FirstTokenMs)
	if err != nil {
		l.warnSpeedFirstDecisionFailure(dispatched.Account, scope, "response_observation", err)
		return
	}
	if recoveryResult != nil {
		l.auditCapture.AddGatewayMetadata("normal_route_speed_first_recovery_observed", map[string]any{
			"accountId":                    dispatched.Account.ID,
			"dimension":                    gatewayproxyhealth.LatencyDimensionFirstByte,
			"firstTokenMs":                 *handling.FirstTokenMs,
			"thresholdMs":                  thresholdMs,
			"cleared":                      recoveryResult.Cleared,
			"recoverySuccessCount":         recoveryResult.RecoverySuccessCount,
			"requiredRecoverySuccessCount": recoveryResult.RequiredRecoverySuccessCount,
		})
	}
}

// observeSpeedFirstTotalTimeOutcome 承接总时间维度的完成观测（设计 6.3
// 采样点 2/3）：该请求装配过总时间维度（与 dispatch 侧同一选档函数复算）
// 才观测；elapsed 超阈值且本 attempt 未记过 → 补记慢样本；未超阈值 → 记
// 达标（恢复）样本，effectiveDeadlineMs 用选定档阈值。timer 已记样本的
// attempt（回调路径已写去重标记）不再补记。
func (l *v1DispatchLoop) observeSpeedFirstTotalTimeOutcome(
	ctx context.Context,
	current *gatewaypreauth.DispatchContext,
	dispatched gatewaydispatch.UpstreamDispatchResult,
	decisions chainSpeedFirstDecisions,
	scope *gatewaydispatch.LatencyScopeInput,
	config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig,
) {
	thresholdMs, ok := l.speedFirstTotalTimeThresholdMsOf(current)
	if !ok {
		return
	}
	// 完成观测点即当下（handling 无终态时刻字段；AttemptStartedAt 与
	// dispatch 侧锚点同源）。
	elapsedMs := l.c.preauth.NowMs() - dispatched.AttemptStartedAt
	if elapsedMs > thresholdMs {
		reason := "普通路由速度优先总时间耗时 " + fmt.Sprintf("%d", elapsedMs) + "ms 超过阈值 " + fmt.Sprintf("%d", thresholdMs) + "ms"
		slowResult, slowErr := l.recordTotalTimeSlowSample(ctx, decisions, scope, config, dispatched.Account, reason)
		if slowErr != nil {
			l.warnSpeedFirstDecisionFailure(dispatched.Account, scope, "total_time_response_observation", slowErr)
			return
		}
		if slowResult == nil {
			return
		}
		l.auditCapture.AddGatewayMetadata("normal_route_speed_first_slow_observed", map[string]any{
			"accountId":     dispatched.Account.ID,
			"dimension":     gatewayproxyhealth.LatencyDimensionTotalTime,
			"thresholdMs":   thresholdMs,
			"elapsedMs":     elapsedMs,
			"observedAt":    "response_completed",
			"slowCount":     slowResult.SlowCount,
			"degraded":      slowResult.Degraded,
			"degradedUntil": slowResult.DegradedUntil,
			"nextProbeAt":   slowResult.NextProbeAt,
		})
		return
	}
	successResult, err := decisions.RecordTotalTimeSuccessAsync(ctx, dispatched.Account, scope, config, thresholdMs, elapsedMs)
	if err != nil {
		l.warnSpeedFirstDecisionFailure(dispatched.Account, scope, "total_time_response_observation", err)
		return
	}
	if successResult != nil {
		l.auditCapture.AddGatewayMetadata("normal_route_speed_first_recovery_observed", map[string]any{
			"accountId":                    dispatched.Account.ID,
			"dimension":                    gatewayproxyhealth.LatencyDimensionTotalTime,
			"elapsedMs":                    elapsedMs,
			"thresholdMs":                  thresholdMs,
			"cleared":                      successResult.Cleared,
			"recoverySuccessCount":         successResult.RecoverySuccessCount,
			"requiredRecoverySuccessCount": successResult.RequiredRecoverySuccessCount,
		})
	}
}

func (l *v1DispatchLoop) warnSpeedFirstDecisionFailure(account gatewaydispatch.AccountCandidate, scope *gatewaydispatch.LatencyScopeInput, stage string, err error) {
	l.c.observability.Logger().Warn("normal_route_speed_first_local_decision_failed", map[string]any{
		"event":           "normal_route_speed_first_local_decision_failed",
		"stage":           stage,
		"accountId":       account.ID,
		"routeStrategyId": scope.RouteStrategyID,
		"groupId":         scope.GroupID,
		"error":           err.Error(),
	}, "普通路由速度优先响应观测失败，保留已完成上游响应")
	l.auditCapture.AddGatewayMetadata("normal_route_speed_first_local_decision_failed", map[string]any{
		"stage":     stage,
		"accountId": account.ID,
	})
}
