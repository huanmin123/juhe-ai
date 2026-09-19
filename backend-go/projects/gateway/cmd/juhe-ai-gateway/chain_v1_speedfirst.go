package main

// W4-B（BUG-0175 D-114）/ R5 speed-first 切换族：普通路由首字截止的决策
// 闭包、切换预留（cutover reservation）的携带/释放/恢复投影，以及响应观测
// 与并发槽获取桥。自 chain_v1.go 按职责拆出（REFACTOR-0007 文件内拆分）；
// 被移动的函数体逐字节保持。

import (
	"context"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
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
	})
}

// settleSpeedFirstCutoverError 镜像 routes.ts:1295-1345 的
// NormalRouteFirstByteCutoverError 分支。返回 true = 请求已结算（终端退出或
// 锁定重派已在引擎预算内完成——Go 侧引擎把同账户重派内化，锁定臂直接按
// 耗尽退出渲染）；false = 循环继续。
func (l *v1DispatchLoop) settleSpeedFirstCutoverError(ctx context.Context, cutover *gatewaydispatch.NormalRouteFirstByteCutoverError) bool {
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
	l.auditCapture.AddGatewayMetadata("normal_route_speed_first_retry_dispatch", map[string]any{
		"accountId":               cutover.AccountID,
		"responseHeadersReceived": false,
		"limitingFactor":          cutover.Deadline.LimitingFactor,
		"retryCount":              l.speedFirstByteRetryCount,
		"maxRetries":              chainSpeedFirstMaxRetriesOf(current),
		"retryAllowed":            retryAllowed,
		"retryBlockedReason":      map[bool]string{true: "", false: "cutover_not_confirmed"}[retryAllowed],
		"targetAccountId":         targetAccountID,
	})
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

// observeSpeedFirstResponseOutcome 镜像 routes.ts:2393-2455 的响应观测：
// 首字耗时超阈值补记慢采样（同尝试去重），达标则记成功恢复采样。
func (l *v1DispatchLoop) observeSpeedFirstResponseOutcome(
	ctx context.Context,
	current *gatewaypreauth.DispatchContext,
	dispatched gatewaydispatch.UpstreamDispatchResult,
	handling gatewayresponse.UpstreamResponseHandlingResult,
) {
	config := current.NormalRouteSpeedFirstConfig
	if config == nil || handling.FirstTokenMs == nil {
		return
	}
	decisions := l.speedFirstDecisionsOf()
	scope := l.speedFirstLatencyScopeOf(current)
	if decisions == nil || scope == nil {
		return
	}
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
			"firstTokenMs":                 *handling.FirstTokenMs,
			"thresholdMs":                  thresholdMs,
			"cleared":                      recoveryResult.Cleared,
			"recoverySuccessCount":         recoveryResult.RecoverySuccessCount,
			"requiredRecoverySuccessCount": recoveryResult.RequiredRecoverySuccessCount,
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
