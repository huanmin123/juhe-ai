package main

// v1DispatchLoop 族：/v1 请求的派发循环核心（Node routes.ts second half）——
// dispatch while(true) 循环、调度错误结算与终端渲染、route-action /
// api-key 分组回退切换、请求级耗尽集与 client-IP 并发槽释放收集。
// 自 chain_v1.go 按职责拆出（REFACTOR-0007 文件内拆分）；被移动的函数体
// 逐字节保持。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// ---------------------------------------------------------------------------
// dispatch loop with the api-key group fallback (routes.ts second half)
// ---------------------------------------------------------------------------

// v1DispatchLoop carries the mutable per-request state of the Node dispatch
// loop (routes.ts:537-566 locals): the current preflight context, the
// visited-group markers of resolveRouteAction (routeActionVisitedGroupIds) and
// switchToFallbackGroup (enteredRouteGroupIds), and the fallback hop counter.
type v1DispatchLoop struct {
	c                   *gatewayChain
	req                 *gatewaypreauth.GatewayRequest
	res                 *gatewaypreauth.TrackingWriter
	auditCapture        gatewaypreauth.AuditCaptureContext
	requestSnapshot     gatewaypreauth.UsageRequestSnapshot
	budgets             requestBudgets
	serverRetryBudget   *gatewaypreauth.ServerRetryBudget
	startedAt           int64
	endpoint            string
	traceID             string
	current             *gatewaypreauth.DispatchContext
	actionVisitedGroups map[string]bool
	enteredGroups       map[string]bool
	fallbackSwitches    int
	// exhaustedAccounts is the request-level exhausted set (Node
	// exhaustedAccountIds, routes.ts:568): every non-recoverable failed
	// account of an UpstreamAttemptError enters it, and switchToFallbackGroup
	// hands it to the fallback candidate window as excludedAccountIds
	// (routes.ts:625).
	exhaustedAccounts map[string]struct{}
	// streamRetryExcludedAccounts is the per-group stream server-retry
	// exclusion set (Node streamServerRetryExcludedAccountIds, routes.ts:540):
	// accounts the response layer's RetryUpstream verdict asked to avoid.
	// switchToFallbackGroup resets it on a group switch (routes.ts:652).
	streamRetryExcludedAccounts map[string]struct{}
	// streamServerRetryCount mirrors Node streamServerRetryCount (routes.ts:
	// 541) for the stream_server_retry_dispatch audit metadata.
	streamServerRetryCount int
	// ---- W4-B（BUG-0175 D-114）speed-first per-request 状态
	//（routes.ts:542-546 locals）----
	// speedFirstByteRetryCount 是本请求已执行的速度优先切号次数（上限
	// maxFirstByteRetriesPerRequest）；组切换时清零（routes.ts:656/787）。
	speedFirstByteRetryCount int
	// speedFirstRetryCandidateAccountIds 非空时把候选窗口收窄到保留目标
	//（routes.ts:921-923）。
	speedFirstRetryCandidateAccountIds map[string]struct{}
	// speedFirstCutoverReservation 携带跨次派发的切换预留（目标并发槽）；
	// 每次派发前取走（routes.ts:1103-1104）。
	speedFirstCutoverReservation *gatewayhotquality.SpeedFirstCutoverReservation
	// speedFirstAttachedViews 记录已 attach 到引擎协调器的视图 → 具体预留
	// 映射：cutover 错误把视图带回循环时据此恢复 TakeForAccount 能力。
	speedFirstAttachedViews map[*gatewaydispatch.SpeedFirstCutoverReservationView]*gatewayhotquality.SpeedFirstCutoverReservation
	// speedFirstSlowObservedForAttempt 记录本次尝试的首字慢观察（
	// routes.ts:1109 闭包写入、2395 响应观测读取，避免重复记录）。
	speedFirstSlowObservedForAttempt *gatewayproxyhealth.LatencySlowResult
	// releases 收集每个 DispatchContext 的 client-IP 并发槽释放闭包
	//（D-109；Node attachClientIpSlotRelease 在组切换时重新 attach）。
	releases *clientIPSlotReleaseList
	// waitCommitState / waitHeartbeat 是 D-120 SSE 等待心跳的请求级共享状态：
	// 心跳写出的 transport-commit 标记必须与响应处理看到的是同一个实例。
	waitCommitState *gatewayresponse.DownstreamCommitState
	waitHeartbeat   *gatewayresponse.GatewaySseWaitHeartbeat
}

// v1FallbackSwitch mirrors the switchToFallbackGroup return union
// (routes.ts:570-572).
type v1FallbackSwitch string

const (
	v1FallbackNone      v1FallbackSwitch = "none"
	v1FallbackSwitched  v1FallbackSwitch = "switched"
	v1FallbackCompleted v1FallbackSwitch = "completed"
)

// stopWaitHeartbeat 终止 D-120 等待心跳：请求 handler 返回即下游终态
// （Node 由 res 的 close/error 监听承载），防止心跳 goroutine 泄漏或在
// 响应结束后继续写出。
func (l *v1DispatchLoop) stopWaitHeartbeat() {
	if l != nil && l.waitHeartbeat != nil {
		l.waitHeartbeat.Stop()
	}
}

// run mirrors the Node while(true) dispatch loop: fetch the first available
// upstream for the current group context and hand the response to the
// response layer; classify dispatch errors, switching to the fallback group
// before rendering the terminal exits.
func (l *v1DispatchLoop) run(ctx context.Context) {
	// routes.ts:2643: a leftover cutover reservation releases with the request.
	defer l.releasePendingSpeedFirstReservation()
	for {
		current := l.current
		coordination := &gatewaydispatch.RequestCoordinationContext{
			Scope:                    gatewaydispatch.CoordinationScopeGatewayRequest,
			ServerRetryBudget:        l.serverRetryBudget,
			GatewayRequestWallBudget: l.budgets.wall,
			RouteCoordinationBudget:  l.budgets.coordination,
			RequestAttemptTracker:    l.budgets.tracker,
		}
		// W4-B（BUG-0175）D-114 接线：普通路由首字截止配置与速度优先决策
		// 闭包（Node normalRouteFirstByteConfig / onNormalRouteFirstByteDeadline，
		// routes.ts:1272-1273 的 coordination 注入）。
		coordination.NormalRouteFirstByteConfig = chainFirstByteConfigOf(current.NormalRouteFirstByteConfig)
		coordination.OnNormalRouteFirstByteDeadline = l.onNormalRouteFirstByteDeadline(ctx, current)
		// W4-B（BUG-0175）D-114 接线：取走上次切号留下的并发槽预留
		//（routes.ts:1103-1104 dispatchCutoverReservation）。
		dispatchReservation := l.speedFirstCutoverReservation
		l.speedFirstCutoverReservation = nil
		l.speedFirstSlowObservedForAttempt = nil
		// Node dispatches streamRetryDispatchAccounts(accounts,
		// streamServerRetryExcludedAccountIds) (routes.ts:942): the accounts a
		// previous response-layer RetryUpstream verdict excluded never re-enter
		// the candidate window of the current group.
		dispatchAccounts := streamRetryDispatchAccounts(current.Accounts, l.streamRetryExcludedAccounts)
		// routes.ts:921-923: a speed-first cutover narrows the window to the
		// reserved target first.
		if l.speedFirstRetryCandidateAccountIds != nil {
			narrowed := make([]gatewaydispatch.AccountCandidate, 0, len(l.speedFirstRetryCandidateAccountIds))
			for _, account := range dispatchAccounts {
				if _, reserved := l.speedFirstRetryCandidateAccountIds[account.ID]; reserved {
					narrowed = append(narrowed, account)
				}
			}
			dispatchAccounts = narrowed
		}
		// SwitchTarget（切号冻结目标）：本请求一旦有账户完成上游请求构造，所有
		// 重派窗口（流式重试、速度优先切换、调度错误重派、分组回退）在推进前
		// 按冻结目标后置过滤；首个候选选择（无冻结目标）不受影响。
		dispatchAccounts = l.filterAccountsForFrozenSwitchTarget(ctx, dispatchAccounts)
		dispatched, dispatchErr := l.c.engine.FetchFirstAvailableUpstream(ctx, gatewaydispatch.FetchFirstAvailableUpstreamArgs{
			Req:                             l.req,
			Accounts:                        dispatchAccounts,
			Settings:                        current.ActiveGatewaySettings,
			UsageContext:                    current.UsageContext,
			AuditCapture:                    l.c.engineAuditCapture(l.auditCapture),
			SessionAffinityKey:              current.SessionAffinityKey,
			Signal:                          ctx,
			ClientIPAccountAvoidanceTracker: current.ClientIPAccountAvoidance,
			RequestLane:                     string(current.RequestLane),
			GroupSchedulingPolicy:           current.GroupSchedulingPolicy,
			AccountStateMutationEnabled:     current.UsageContext.TrafficSource == gatewayTrafficSource,
			RequestClientCompatibility:      current.ClientStrategy.RequestClientCompatibility,
			ModelPriority:                   current.ModelPriority,
			AllowPrecheckHalfOpen:           current.PrecheckHalfOpenEligible,
			// F5-2: the codex turn (client source) avoidance filter + last-
			// resort reversal ride the dispatch loop (Node routes.ts:911-917).
			CodexTurnAccountAvoidanceApplied: current.CodexTurnAccountAvoidanceApplied,
			CodexTurnAvoidedAccountIDs:       current.CodexTurnAvoidedAccountIDs,
			RequestCoordination:              coordination,
			WaitForRecoverableFailures:       true,
			// W4-B（BUG-0175）D-114：速度优先切换预留（Node
			// preAcquiredConcurrency 参数）。
			PreAcquiredConcurrency: speedFirstReservationHandleOf(dispatchReservation),
		})
		if dispatchErr == nil {
			// ---- response piping + finalization (response/finalization.ts) ----
			// F13（E2E-FINDING #13 第二层）：账户并发槽随本轮派发迭代释放。
			// keepConcurrencySlot=true 时 dispatch 内 releaseTransientState 是空操作，
			// 槽必须在响应管道结束后交给排队者（Node attachAccountSlotRelease
			// 挂在 res finish/close；Go 等价点是 handleUpstreamResponse 返回）。
			// 嵌套函数 + defer：循环体内不能用函数级 defer（会拖到 run 返回），
			// onceFunc 幂等，panic 与 RetryUpstream 切号都不会漏槽或双释放。
			handling := func() gatewayresponse.UpstreamResponseHandlingResult {
				defer func() {
					if dispatched.ReleaseConcurrency != nil {
						dispatched.ReleaseConcurrency()
					}
				}()
				return l.c.handleUpstreamResponse(l.req, l.res, l.auditCapture, current, dispatched, l.startedAt, current.ActiveGatewaySettings, l.budgets, l.waitCommitState)
			}()
			if handling.FirstByteDeadlineCutover {
				// R5：非流式管线 configured_deadline 首字超时的切号 verdict
				// （Node routes.ts catch 响应段）交给既有 cutover 消费端：
				// 收窄到保留目标重派（false → continue）或耗尽退出（true）。
				// 预留已在响应面 TransferForCutover 转移进 verdict，此处只
				// 消费、不重复转移（Transfer 为 active→transferred once 语义）。
				if l.settleFirstByteDeadlineCutoverVerdict(ctx, dispatched, handling) {
					return
				}
				continue
			}
			if !handling.RetryUpstream {
				// routes.ts:2393-2455: the speed-first response observation
				// (slow/success sampling) runs once the response completed
				// without a server-retry verdict.
				l.observeSpeedFirstResponseOutcome(ctx, current, dispatched, handling)
				// routes.ts:2478-2486: the final protocol oracle confirms the
				// pending sibling Key failures + the winning Key success.
				l.confirmProtocolSuccessSideEffects(ctx, dispatched, handling)
				return
			}
			// D1: the response layer asked for a server-side account switch
			// (Node routes.ts:1899 `if (handledResponse.retryUpstream)`); the
			// loop continues on the remaining candidates or settles the
			// exhausted exit — never an empty 200.
			if l.settleResponseStreamServerRetry(ctx, dispatched, handling) {
				return
			}
			continue
		}
		if l.settleDispatchError(ctx, dispatchErr) {
			return
		}
	}
}

// settleResponseStreamServerRetry mirrors the Node retryUpstream consumption
// (routes.ts:1899-2398) for the reasons the Go response layer produces
// (response_inspection / pre_commit_stream_failure). The deep per-branch
// server-retry loops that stay engine-internal in Go (speed-first cutover,
// codex encrypted-content recovery, account-lock lease carry)
// never reach this method, and the same-account retry reservation
// (routes.ts:2245-2300) is a Node dispatch-loop nicety the Go chain does not
// carry: the verdict rotates to the next account instead. Returns true when
// the request settled (terminal response rendered) and false when the loop
// should re-dispatch on the remaining candidates.
func (l *v1DispatchLoop) settleResponseStreamServerRetry(
	ctx context.Context,
	dispatched gatewaydispatch.UpstreamDispatchResult,
	handling gatewayresponse.UpstreamResponseHandlingResult,
) bool {
	// A RetryUpstream verdict is a pre-commit decision (the response layer only
	// produces it while canRetryUpstream); a writable-ended downstream can no
	// longer take a different account's response.
	if writableEndedOf(l.res) {
		return true
	}
	current := l.current
	accountID := dispatched.Account.ID
	// Node 2301-2307: a policy-requested exclusion puts the current account
	// into the per-group stream-retry excluded set.
	policyRequestedAccountExclusion := handling.ExcludeCurrentAccount
	if policyRequestedAccountExclusion {
		if l.streamRetryExcludedAccounts == nil {
			l.streamRetryExcludedAccounts = map[string]struct{}{}
		}
		l.streamRetryExcludedAccounts[accountID] = struct{}{}
	}
	l.streamServerRetryCount++
	remaining := streamRetryDispatchAccounts(current.Accounts, l.streamRetryExcludedAccounts)
	l.auditCapture.AddGatewayMetadata("stream_server_retry_dispatch", map[string]any{
		"retryReason":                     handling.RetryReason,
		"retryCount":                      l.streamServerRetryCount,
		"candidateCount":                  len(current.Accounts),
		"remainingCandidateCount":         len(remaining),
		"elapsedMs":                       l.c.preauth.NowMs() - l.startedAt,
		"accountId":                       accountID,
		"excludedAccountIds":              stringSetKeys(l.streamRetryExcludedAccounts),
		"excludeCurrentAccount":           handling.ExcludeCurrentAccount,
		"currentRequestAccountExcluded":   policyRequestedAccountExclusion,
		"policyRequestedAccountExclusion": policyRequestedAccountExclusion,
		"errorCode":                       handling.ErrorCode,
	})
	if handling.ResponseInspection != nil {
		l.auditCapture.AddGatewayMetadata("stream_server_retry_policy", map[string]any{
			"policyId":      handling.ResponseInspection.PolicyID,
			"policyName":    handling.ResponseInspection.PolicyName,
			"accountSwitch": handling.ResponseInspection.AccountSwitch,
			"retryEnabled":  handling.ResponseInspection.RetryEnabled,
		})
	}
	// Node 2326-2356: a response-inspection retry that does not change the
	// dispatch (no account exclusion) stops with the exhausted contract.
	if handling.RetryReason == gatewayresponse.StreamServerRetryResponseInspection &&
		handling.ResponseInspection != nil && !policyRequestedAccountExclusion {
		// Node routes.ts:2332: the terminal failure confirms the pending
		// client-IP account failures before the response renders.
		l.confirmClientIPAccountAvoidanceAfterFinalFailure(ctx, current, "response_inspection_no_dispatch_change")
		l.auditCapture.AddGatewayMetadata("response_inspection_server_retry_stopped", map[string]any{
			"reason":        "no_dispatch_change",
			"accountId":     accountID,
			"policyId":      handling.ResponseInspection.PolicyID,
			"policyName":    handling.ResponseInspection.PolicyName,
			"accountSwitch": handling.ResponseInspection.AccountSwitch,
			"retryEnabled":  handling.ResponseInspection.RetryEnabled,
			"errorCode":     handling.ErrorCode,
		})
		l.sendStreamServerRetryExhaustedResponse(streamServerRetryExhaustedInput{
			message:        handling.Message,
			retryReason:    handling.RetryReason,
			errorCode:      handling.ErrorCode,
			decision:       handling.ResponseInspection,
			usageContext:   current.UsageContext,
			clientStrategy: &current.ClientStrategy,
		})
		return true
	}
	if len(remaining) == 0 {
		// Node 2362-2366: the group's candidate window is empty — the excluded
		// accounts join the request-level exhausted set and the fallback group
		// gets its chance before the exhausted exit.
		for accountID := range l.streamRetryExcludedAccounts {
			if l.exhaustedAccounts == nil {
				l.exhaustedAccounts = map[string]struct{}{}
			}
			l.exhaustedAccounts[accountID] = struct{}{}
		}
		fallbackReason := streamServerRetryFallbackReason(handling.RetryReason)
		switch fallback, fallbackErr := l.switchToFallbackGroup(ctx, fallbackReason); {
		case fallbackErr != nil:
			// Node: the switch error propagates to the top-level catch.
			l.renderUnexpectedDispatchFailure(ctx, fallbackErr)
			return true
		case fallback == v1FallbackCompleted:
			return true
		case fallback == v1FallbackSwitched:
			return false
		}
		// Node 2386-2397: no fallback switch → the stream server-retry
		// exhausted contract (503, recordUsage:false), never an empty 200.
		// Node routes.ts:2374: the pending client-IP account failures are
		// confirmed before the exhausted response renders.
		l.confirmClientIPAccountAvoidanceAfterFinalFailure(ctx, current, "stream_server_retry_exhausted")
		l.sendStreamServerRetryExhaustedResponse(streamServerRetryExhaustedInput{
			message:        handling.Message,
			retryReason:    handling.RetryReason,
			errorCode:      handling.ErrorCode,
			decision:       handling.ResponseInspection,
			usageContext:   current.UsageContext,
			clientStrategy: &current.ClientStrategy,
		})
		return true
	}
	// Node 2398: candidates remain — continue the dispatch loop.
	return false
}

// settleDispatchError maps one dispatch-loop error onto the Node error
// handling (routes.ts:1285-1487 catch + the top-level catch 2532-2638). It
// returns true when the request is settled (response written, aborted, or
// terminal exit rendered) and false when the loop should continue on the
// switched fallback group.
func (l *v1DispatchLoop) settleDispatchError(ctx context.Context, dispatchErr error) bool {
	// W4-B（BUG-0175）D-114：速度优先切号错误（routes.ts:1295-1345）。持有
	// 切换预留时收窄到保留目标重派；预留缺席/已锁定走耗尽退出。
	var cutover *gatewaydispatch.NormalRouteFirstByteCutoverError
	if errors.As(dispatchErr, &cutover) {
		if l.settleSpeedFirstCutoverError(ctx, cutover) {
			return true
		}
		return false
	}
	// Node top-level catch: known errors (downstream closed, agent guidance,
	// validation / codex adapter, diagnostic timeout/cancel) render their own
	// contracts before the exhaustion exits.
	if l.c.preauth.HandleGatewayRequestKnownErrorResponse(gatewaypreauth.KnownErrorResponseInput{
		Req:          l.req,
		Res:          l.res,
		AuditCapture: l.auditCapture,
		Err:          dispatchErr,
		Signal:       ctx,
	}) {
		return true
	}
	var aborted *gatewaydispatch.UpstreamRequestAbortedError
	if errors.As(dispatchErr, &aborted) {
		// Downstream closed / request aborted: no response contract.
		return true
	}
	var wall *gatewaydispatch.GatewayRequestWallBudgetExhaustedError
	if errors.As(dispatchErr, &wall) {
		if wall.BudgetKind == gatewaydispatch.WallBudgetKindCoordination {
			// Node routes.ts:1346-1370: the coordination kind hands the
			// request back to the client instead of the wall 503.
			l.auditCapture.AddGatewayMetadata("gateway_request_client_handoff", map[string]any{
				"reason":          "route_coordination_budget_exhausted",
				"wallRemainingMs": wall.WallRemainingMs,
			})
			l.sendStreamServerRetryExhaustedResponse(streamServerRetryExhaustedInput{
				message:        "网关请求协调预算已到，请客户端重试并重新选择可用账户",
				retryReason:    "pre_commit_stream_failure",
				errorCode:      gatewaypreauth.GatewayStreamClientRetryErrorCode,
				usageContext:   l.current.UsageContext,
				clientStrategy: &l.current.ClientStrategy,
			})
			return true
		}
		// The wall kind keeps the fixed 503 wall exit (review ruling V4; the
		// Go engine surfaces the wall error instead of the Node while-loop
		// continue, whose continuation the engine budget loop internalizes).
		message := "网关请求时间预算已用尽，请稍后重试"
		l.c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
			Req:             l.req,
			Res:             l.res,
			AuditCapture:    l.auditCapture,
			UsageContext:    l.current.UsageContext,
			StartedAt:       l.startedAt,
			StatusCode:      http.StatusServiceUnavailable,
			ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf(message, "service_unavailable", "gateway_request_wall_budget_exhausted"),
			Audit: gatewaypreauth.FailureAudit{
				Outcome:      gatewaypreauth.AuditOutcomeGatewayFailed,
				ErrorPhase:   "dispatch",
				ErrorCode:    "gateway_request_wall_budget_exhausted",
				ErrorMessage: message,
			},
			FailureScope: "upstream",
		})
		return true
	}
	var attempt *gatewaydispatch.UpstreamAttemptError
	if errors.As(dispatchErr, &attempt) {
		if !attempt.TerminalUpstreamFailure {
			// Node 1425-1433: only the non-recoverable failed accounts enter
			// the exhausted set; recoverable failures stay retryable.
			l.exhaustDispatchFailedAccounts(attempt)
			// routes.ts:1432-1452: a narrowed speed-first window whose target
			// failed falls back to the full candidate window (failed accounts
			// excluded); an empty window continues to the fallback below.
			if l.speedFirstRetryCandidateAccountIds != nil {
				for _, id := range attempt.FailedAccountIDs {
					if l.streamRetryExcludedAccounts == nil {
						l.streamRetryExcludedAccounts = map[string]struct{}{}
					}
					l.streamRetryExcludedAccounts[id] = struct{}{}
				}
				l.speedFirstRetryCandidateAccountIds = nil
				remainingSpeedFirstAccounts := streamRetryDispatchAccounts(l.current.Accounts, l.streamRetryExcludedAccounts)
				l.auditCapture.AddGatewayMetadata("normal_route_speed_first_reserved_target_exhausted", map[string]any{
					"failedAccountIds":             attempt.FailedAccountIDs,
					"recoverableAccountIds":        attempt.RecoverableAccountIDs,
					"remainingCandidateAccountIds": chainAccountIDsOf(remainingSpeedFirstAccounts),
				})
				if len(remainingSpeedFirstAccounts) > 0 {
					return false
				}
			}
			// Node 1469-1478: try the fallback group before the exhaustion
			// exit. A switched fallback continues the loop; a completed one
			// means the fallback preflight settled the request.
			reason := "upstream_accounts_exhausted"
			if attempt.AgentGuidanceResponse != nil {
				reason = "account_scoped_agent_guidance_exhausted"
			}
			switch fallback, fallbackErr := l.switchToFallbackGroup(ctx, reason); {
			case fallbackErr != nil:
				// Node: the switch error propagates to the top-level catch.
				l.renderUnexpectedDispatchFailure(ctx, fallbackErr)
				return true
			case fallback == v1FallbackCompleted:
				return true
			case fallback == v1FallbackSwitched:
				return false
			}
		}
		l.renderDispatchExhausted(ctx, attempt)
		return true
	}
	// Node top-level catch: an unexpected dispatch error keeps the 503
	// upstream contract — never the orchestrator 500 (V5).
	l.renderUnexpectedDispatchFailure(ctx, dispatchErr)
	return true
}

// renderDispatchExhausted mirrors the Node top-level catch for the
// UpstreamAttemptError branch (routes.ts:2551-2638): the client payload is
// the fixed copy pair (no candidate accounts vs. retryable upstream), the
// detailed last-attempt diagnostics stay on the audit/log surface
// (upstream-dispatch.ts buildUpstreamAttemptFailureMessage,
// dispatch-exhaustion-classifier.ts).
func (l *v1DispatchLoop) renderDispatchExhausted(ctx context.Context, attempt *gatewaydispatch.UpstreamAttemptError) {
	lastAttempt := attempt.LastAttempt
	fields := map[string]any{
		"event":         "gateway_dispatch_exhausted",
		"endpoint":      l.current.UsageContext.Endpoint,
		"apiKeyId":      l.current.UsageContext.APIKeyID,
		"groupId":       l.current.UsageContext.GroupID,
		"trafficSource": l.current.UsageContext.TrafficSource,
	}
	failureReason, upstreamStatus := classifyGatewayDispatchExhaustion(lastAttempt)
	fields["failureReason"] = failureReason
	if upstreamStatus != nil {
		fields["upstreamStatus"] = upstreamStatus
	}
	if lastAttempt != nil {
		fields["lastAttemptAccountId"] = lastAttempt.AccountID
	}
	fields["failedAccountIds"] = attempt.FailedAccountIDs
	l.c.observability.Logger().Warn("gateway_dispatch_exhausted", fields, "网关上游调度已耗尽")

	payloadMessage := "上游暂时不可用，请重试"
	payloadCode := gatewaypreauth.GatewayStreamClientRetryErrorCode
	if lastAttempt == nil {
		// Node: message === '没有可用的上游账户' — the no-candidate attempt
		// error carries no last attempt.
		payloadMessage = "没有可用的上游账户"
		payloadCode = "no_available_upstream_account"
	}
	// Node routes.ts:2619: the gateway failure response confirms the pending
	// client-IP account failures first (the dispatch_exhausted_protocol_retry
	// branch at routes.ts:2576 has no Go rendering — V5 kept the fixed 503
	// upstream contract — so this single confirm covers the terminal exit).
	l.confirmClientIPAccountAvoidanceAfterFinalFailure(ctx, l.current, "gateway_failure_response")
	l.c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:             l.req,
		Res:             l.res,
		AuditCapture:    l.auditCapture,
		UsageContext:    l.current.UsageContext,
		StartedAt:       l.startedAt,
		StatusCode:      http.StatusServiceUnavailable,
		ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf(payloadMessage, "service_unavailable", payloadCode),
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      gatewaypreauth.AuditOutcomeUpstreamFailed,
			ErrorPhase:   "dispatch",
			ErrorCode:    payloadCode,
			ErrorMessage: attempt.Message,
		},
		RecordUsage:  boolPtr(lastAttempt == nil),
		FailureScope: "upstream",
	})
}

// renderUnexpectedDispatchFailure mirrors the Node top-level catch for
// non-attempt errors (routes.ts:2564-2572 + 2616-2638): the 503 upstream
// contract with the fixed copy; the error detail stays on the log/audit
// surface.
func (l *v1DispatchLoop) renderUnexpectedDispatchFailure(ctx context.Context, dispatchErr error) {
	l.c.observability.Logger().Warn("gateway_request_unexpected_error", map[string]any{
		"event":    "gateway_request_unexpected_error",
		"endpoint": l.current.UsageContext.Endpoint,
		"apiKeyId": l.current.UsageContext.APIKeyID,
		"groupId":  l.current.UsageContext.GroupID,
		"error":    dispatchErr.Error(),
	}, "网关请求处理出现未预期异常")
	payload := gatewaypreauth.GatewayErrorPayloadOf("上游暂时不可用，请重试", "service_unavailable", gatewaypreauth.GatewayStreamClientRetryErrorCode)
	// Node routes.ts:2619: the same terminal confirm precedes the gateway
	// failure response.
	l.confirmClientIPAccountAvoidanceAfterFinalFailure(ctx, l.current, "gateway_failure_response")
	l.c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:             l.req,
		Res:             l.res,
		AuditCapture:    l.auditCapture,
		UsageContext:    l.current.UsageContext,
		StartedAt:       l.startedAt,
		StatusCode:      http.StatusServiceUnavailable,
		ResponsePayload: payload,
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      gatewaypreauth.AuditOutcomeUpstreamFailed,
			ErrorPhase:   "dispatch",
			ErrorCode:    gatewaypreauth.GatewayStreamClientRetryErrorCode,
			ErrorMessage: dispatchErr.Error(),
		},
		RecordUsage:  boolPtr(true),
		FailureScope: "upstream",
	})
}

// resolveRouteAction mirrors resolveRouteAction (routes.ts:433-487): walk
// route actions, attempting the api-key group fallback before rendering a
// terminal action. A nil result means the request ended inside the loop
// (terminal action rendered, or the fallback preflight completed/rejected it).
func (l *v1DispatchLoop) resolveRouteAction(ctx context.Context, initial gatewaypreauth.PreflightResult) (*gatewaypreauth.DispatchContext, error) {
	result := initial
	for result.IsRouteAction() {
		action := result.RouteAction
		groupID := action.UsageContext.GroupID
		mayTryFallback := action.Coordination.Outcome != gatewaypreauth.RouteOutcomeClientHandoff &&
			action.InteractionResourceAffinity == nil &&
			!l.actionVisitedGroups[groupID]
		l.actionVisitedGroups[groupID] = true
		if mayTryFallback {
			actionAPIKeyRecord := action.APIKeyRecord
			if action.GroupFallbackAPIKeyRecord != nil {
				actionAPIKeyRecord = action.GroupFallbackAPIKeyRecord
			}
			fallback, err := l.c.preauth.PrepareAPIKeyGroupFallbackDispatchContext(ctx, gatewaypreauth.APIKeyGroupFallbackDispatchInput{
				Req:                        l.req,
				Res:                        l.res,
				AuditCapture:               l.auditCapture,
				Options:                    l.actionFallbackOptions(action),
				StartedAt:                  l.startedAt,
				TraceID:                    l.traceID,
				ClientIP:                   l.req.ClientIP,
				Endpoint:                   l.endpoint,
				RequestSnapshot:            l.requestSnapshot,
				Signal:                     ctx,
				Reason:                     action.Coordination.Reason,
				APIKeyRecord:               actionAPIKeyRecord,
				GroupFallbackAPIKeyRecord:  actionAPIKeyRecord,
				SystemAccountID:            action.UsageContext.SystemAccountID,
				APIKeyID:                   action.UsageContext.APIKeyID,
				GroupID:                    groupID,
				TrafficSource:              action.UsageContext.TrafficSource,
				RequestLane:                action.RequestLane,
				RequestClientCompatibility: action.ClientStrategy.RequestClientCompatibility,
				RoutePlanSnapshot:          action.RoutePlanSnapshot,
			})
			if err != nil {
				return nil, err
			}
			if fallback.Attempted {
				if !isEmptyPreflightResult(fallback.Context) {
					result = fallback.Context
					continue
				}
				// attempted && undefined: the fallback preflight completed or
				// rejected the request (Node 481).
				return nil, nil
			}
		}
		l.finalizeRouteAction(action)
		return nil, nil
	}
	return result.DispatchContext, nil
}

// actionFallbackOptions mirrors the option bag Node passes from the route
// action (routes.ts:449-459): the shared budgets and the per-group runtime
// fields travel into the fallback preflight.
func (l *v1DispatchLoop) actionFallbackOptions(action *gatewaypreauth.RouteAction) *gatewaypreauth.PreflightOptions {
	return &gatewaypreauth.PreflightOptions{
		TrafficSource:              gatewayTrafficSource,
		RequestLane:                action.RequestLane,
		ServerRetryBudget:          action.ServerRetryBudget,
		GatewayRequestWallBudget:   action.GatewayRequestWallBudget,
		RouteCoordinationBudget:    action.RouteCoordinationBudget,
		RequestAttemptTracker:      action.RequestAttemptTracker,
		DownstreamCommitState:      action.DownstreamCommitState,
		NormalRouteFirstByteConfig: action.NormalRouteFirstByteConfig,
	}
}

// exhaustDispatchFailedAccounts adds the attempt's non-recoverable failed
// accounts to the request-level exhausted set (Node routes.ts:1425-1433:
// failedAccountIds minus recoverableAccountIds; recoverable failures keep the
// account retryable).
func (l *v1DispatchLoop) exhaustDispatchFailedAccounts(attempt *gatewaydispatch.UpstreamAttemptError) {
	if len(attempt.FailedAccountIDs) == 0 {
		return
	}
	recoverable := make(map[string]struct{}, len(attempt.RecoverableAccountIDs))
	for _, id := range attempt.RecoverableAccountIDs {
		recoverable[id] = struct{}{}
	}
	if l.exhaustedAccounts == nil {
		l.exhaustedAccounts = make(map[string]struct{})
	}
	for _, id := range attempt.FailedAccountIDs {
		if _, isRecoverable := recoverable[id]; isRecoverable {
			continue
		}
		l.exhaustedAccounts[id] = struct{}{}
	}
}

// switchToFallbackGroup mirrors switchToFallbackGroup (routes.ts:570-662).
func (l *v1DispatchLoop) switchToFallbackGroup(ctx context.Context, reason string) (v1FallbackSwitch, error) {
	current := l.current
	if current.InteractionResourceAffinity != nil {
		return v1FallbackNone, nil
	}
	// Node 576-578 + 624: the agent-guidance reason may elevate to the
	// group-fallback key record; both records default to the current one.
	groupFallbackRecord := current.APIKeyRecord
	if current.GroupFallbackAPIKeyRecord != nil {
		groupFallbackRecord = current.GroupFallbackAPIKeyRecord
	}
	fallbackAPIKeyRecord := current.APIKeyRecord
	if reason == "account_scoped_agent_guidance_exhausted" {
		fallbackAPIKeyRecord = groupFallbackRecord
	}
	groupBindingCount := 0
	if fallbackAPIKeyRecord != nil {
		groupBindingCount = len(fallbackAPIKeyRecord.GroupBindings)
	}
	if groupBindingCount > 0 && l.fallbackSwitches >= groupBindingCount {
		l.auditCapture.AddGatewayMetadata("api_key_group_route_fallback_skipped", map[string]any{
			"reason":              reason,
			"groupBindingCount":   groupBindingCount,
			"fallbackSwitchCount": l.fallbackSwitches,
			"skippedReason":       "fallback_hop_limit",
		})
		return v1FallbackNone, nil
	}
	fallback, err := l.c.preauth.PrepareAPIKeyGroupFallbackDispatchContext(ctx, gatewaypreauth.APIKeyGroupFallbackDispatchInput{
		Req:                        l.req,
		Res:                        l.res,
		AuditCapture:               l.auditCapture,
		Options:                    l.fallbackOptions(current),
		StartedAt:                  l.startedAt,
		TraceID:                    l.traceID,
		ClientIP:                   l.req.ClientIP,
		Endpoint:                   l.endpoint,
		RequestSnapshot:            l.requestSnapshot,
		Signal:                     ctx,
		Reason:                     reason,
		APIKeyRecord:               fallbackAPIKeyRecord,
		GroupFallbackAPIKeyRecord:  groupFallbackRecord,
		SystemAccountID:            current.UsageContext.SystemAccountID,
		APIKeyID:                   current.UsageContext.APIKeyID,
		GroupID:                    current.UsageContext.GroupID,
		TrafficSource:              current.UsageContext.TrafficSource,
		RequestLane:                current.RequestLane,
		RequestClientCompatibility: current.ClientStrategy.RequestClientCompatibility,
		// Node 625: the request-level exhausted set filters every fallback
		// candidate group window.
		ExcludedAccountIDs: l.exhaustedAccounts,
		RoutePlanSnapshot:  current.RoutePlanSnapshot,
	})
	if err != nil {
		return "", err
	}
	if !fallback.Attempted {
		return v1FallbackNone, nil
	}
	// An empty fallback context means the fallback preflight completed the
	// request (Node 630-633).
	if isEmptyPreflightResult(fallback.Context) {
		return v1FallbackCompleted, nil
	}
	next, err := l.resolveRouteAction(ctx, fallback.Context)
	if err != nil {
		return "", err
	}
	if next == nil {
		return v1FallbackCompleted, nil
	}
	l.fallbackSwitches++
	// Node 640-642: a repeated group target stops the switch (the hop still
	// counts).
	if l.enteredGroups[next.UsageContext.GroupID] {
		return v1FallbackNone, nil
	}
	l.enteredGroups[next.UsageContext.GroupID] = true
	// D-109（BUG-0175）：新分组的 DispatchContext 带新的 client-IP 并发槽
	//（Node routes.ts:651 重新 attach release；旧槽在 handler 终态统一释放）。
	l.releases.Add(next.ReleaseClientIPConcurrency)
	// Node 644-651 transfers the client-ip slot and settles the hot-quality
	// reservation; those lifecycle ports stay engine-internal in Go. The
	// per-group retry resets ride on the fresh DispatchContext.
	l.current = next
	// Node 652-657: a switched fallback resets the per-group stream server-
	// retry bookkeeping (streamServerRetryExcludedAccountIds /
	// streamServerRetryCount) and the W4-B speed-first cutover state
	// (routes.ts:654-659).
	l.streamRetryExcludedAccounts = map[string]struct{}{}
	l.streamServerRetryCount = 0
	l.resetSpeedFirstState()
	return v1FallbackSwitched, nil
}

// filterAccountsForFrozenSwitchTarget 在重派前按本请求已冻结的切号目标后置
// 过滤候选窗口：与冻结源同账户的候选（同账户 Key 轮换 / 同账户重试）不是
// 切换点，语义不变。冻结目标不可解析（不变量违规）时 fail-closed——停止跨
// 账户切号，仅保留冻结源账户，并输出一次 switch_target_unresolved 结构化
// 诊断（设计文档 §4/§6）。
func (l *v1DispatchLoop) filterAccountsForFrozenSwitchTarget(ctx context.Context, accounts []gatewaydispatch.AccountCandidate) []gatewaydispatch.AccountCandidate {
	gate := gatewaydispatch.SwitchTargetGateFromContext(ctx)
	if gate == nil || !gate.Frozen() {
		return accounts
	}
	filtered := gate.FilterAccounts(accounts)
	if gate.Unresolved() && gate.MarkUnresolvedDiagnosed() {
		fields := map[string]any{
			"event":    "switch_target_unresolved",
			"endpoint": l.current.UsageContext.Endpoint,
			"apiKeyId": l.current.UsageContext.APIKeyID,
			"groupId":  l.current.UsageContext.GroupID,
			"traceId":  l.traceID,
		}
		if sourceID := gatewaydispatch.SwitchTargetGateSourceOf(gate); sourceID != "" {
			fields["sourceAccountId"] = sourceID
		}
		l.auditCapture.AddGatewayMetadata("switch_target_unresolved", fields)
		l.c.observability.Logger().Warn("switch_target_unresolved", fields, "切号冻结目标不可解析，已停止跨账户切号")
	}
	return filtered
}

// chainAccountIDsOf 投影候选 id 列表（审计元数据用）。
func chainAccountIDsOf(accounts []gatewaydispatch.AccountCandidate) []string {
	out := make([]string, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, account.ID)
	}
	return out
}

// exhaustDispatchFailedAccountID 把单个账号加入请求级耗尽集。
func (l *v1DispatchLoop) exhaustDispatchFailedAccountID(accountID string) {
	if accountID == "" {
		return
	}
	if l.exhaustedAccounts == nil {
		l.exhaustedAccounts = make(map[string]struct{})
	}
	l.exhaustedAccounts[accountID] = struct{}{}
}

// renderDispatchExhaustedWithMessage 渲染速度优先耗尽的固定 503 契约
// （Node UpstreamAttemptError transportFailureKind=timeout 的顶层 catch）。
func (l *v1DispatchLoop) renderDispatchExhaustedWithMessage(ctx context.Context, message, accountID, accountName string) {
	l.c.observability.Logger().Warn("gateway_dispatch_exhausted", map[string]any{
		"event":                  "gateway_dispatch_exhausted",
		"failureReason":          "first_byte_timeout",
		"lastAttemptAccountId":   accountID,
		"lastAttemptAccountName": accountName,
		"endpoint":               l.current.UsageContext.Endpoint,
		"apiKeyId":               l.current.UsageContext.APIKeyID,
		"groupId":                l.current.UsageContext.GroupID,
		"trafficSource":          l.current.UsageContext.TrafficSource,
	}, "网关上游调度已耗尽")
	l.confirmClientIPAccountAvoidanceAfterFinalFailure(ctx, l.current, "gateway_failure_response")
	l.c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:          l.req,
		Res:          l.res,
		AuditCapture: l.auditCapture,
		UsageContext: l.current.UsageContext,
		StartedAt:    l.startedAt,
		StatusCode:   http.StatusServiceUnavailable,
		ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf(
			"上游暂时不可用，请重试", "service_unavailable", gatewaypreauth.GatewayStreamClientRetryErrorCode),
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      gatewaypreauth.AuditOutcomeUpstreamFailed,
			ErrorPhase:   "dispatch",
			ErrorCode:    gatewaypreauth.GatewayStreamClientRetryErrorCode,
			ErrorMessage: message,
		},
		RecordUsage:  boolPtr(false),
		FailureScope: "upstream",
	})
}

// confirmProtocolSuccessSideEffects 镜像 routes.ts:2478-2486 的最终协议
// 成功结算：挂起的同账户 Key 轮转失败确认 + 胜出 Key 的成功记录（D-111）。
// 结算错误不改写已提交的下游响应（Node 顶层 catch 同样只记日志）。
func (l *v1DispatchLoop) confirmProtocolSuccessSideEffects(ctx context.Context, dispatched gatewaydispatch.UpstreamDispatchResult, handling gatewayresponse.UpstreamResponseHandlingResult) {
	if !handling.ProtocolValidatedSuccess {
		return
	}
	if dispatched.ConfirmSameAccountApiKeyFailures != nil {
		if err := dispatched.ConfirmSameAccountApiKeyFailures(); err != nil {
			l.c.observability.Logger().Warn("gateway_account_api_key_rotation_confirm_failed", map[string]any{
				"event":     "gateway_account_api_key_rotation_confirm_failed",
				"accountId": dispatched.Account.ID,
				"error":     err.Error(),
			}, "已确认同账户 API Key 轮转失败结算未完成")
		}
	}
	if dispatched.ConfirmAccountAPIKeySuccess != nil {
		if err := dispatched.ConfirmAccountAPIKeySuccess(); err != nil {
			l.c.observability.Logger().Warn("gateway_account_api_key_success_settlement_failed", map[string]any{
				"event":     "gateway_account_api_key_success_settlement_failed",
				"accountId": dispatched.Account.ID,
				"error":     err.Error(),
			}, "账户 API Key 成功结算未完成")
		}
	}
	// 成功侧结算：把 ENGAGED 锁复位为 LOCKED_IDLE
	// 与 Node routes.ts:2481-2483 completeAccountLockSuccessAsync 行为一致。
	if dispatched.ConfirmAccountLockSuccess != nil {
		if err := dispatched.ConfirmAccountLockSuccess(); err != nil {
			l.c.observability.Logger().Warn("gateway_account_lock_success_settlement_failed", map[string]any{
				"event":     "gateway_account_lock_success_settlement_failed",
				"accountId": dispatched.Account.ID,
				"error":     err.Error(),
			}, "账户锁成功结算未完成")
		}
	}
}

// fallbackOptions mirrors the option bag Node passes from the current
// preflight (routes.ts:597-607).
func (l *v1DispatchLoop) fallbackOptions(current *gatewaypreauth.DispatchContext) *gatewaypreauth.PreflightOptions {
	return &gatewaypreauth.PreflightOptions{
		TrafficSource:              gatewayTrafficSource,
		RequestLane:                current.RequestLane,
		ServerRetryBudget:          current.ServerRetryBudget,
		GatewayRequestWallBudget:   current.GatewayRequestWallBudget,
		RouteCoordinationBudget:    current.RouteCoordinationBudget,
		RequestAttemptTracker:      current.RequestAttemptTracker,
		DownstreamCommitState:      current.DownstreamCommitState,
		NormalRouteFirstByteConfig: current.NormalRouteFirstByteConfig,
	}
}

// confirmClientIPAccountAvoidanceAfterFinalFailure mirrors
// confirmCurrentClientIpAccountAvoidanceAfterFinalFailure (routes.ts:2690-2725):
// once the request failed back to the client, the tracker's pending account
// failures become client-IP avoidance entries immediately instead of waiting
// for the next request's success confirm. The tracker rides the DispatchContext
// (Node preflight.clientIpAccountAvoidanceTracker) and the avoidance service is
// the G05 factory the preauth service was assembled with.
func (l *v1DispatchLoop) confirmClientIPAccountAvoidanceAfterFinalFailure(ctx context.Context, context *gatewaypreauth.DispatchContext, reason string) {
	if gatewayusage.IsAccountDiagnosticTrafficSource(context.UsageContext.TrafficSource) {
		return
	}
	avoidance, _ := l.c.preauth.AccountAvoidance.(*gatewayclientip.Avoidance)
	tracker, _ := context.ClientIPAccountAvoidance.(*gatewayclientip.AvoidanceTracker)
	if avoidance == nil || tracker == nil {
		return
	}
	settings := context.ActiveGatewaySettings
	result, err := avoidance.ConfirmAfterFinalFailureAsync(ctx, tracker, &settings)
	if err != nil {
		// Node: a rejection here would abandon the terminal render and jump to
		// the finally block. Go keeps the terminal render (a failed avoidance
		// confirmation must not swallow the client exit) and logs instead.
		l.c.observability.Logger().Warn("gateway_client_ip_account_avoidance_confirm_failed", map[string]any{
			"event":  "gateway_client_ip_account_avoidance_confirm_failed",
			"reason": reason,
			"error":  err.Error(),
		}, "客户端 IP 级账号回避终态确认失败")
		return
	}
	if len(result.ConfirmedAccountIDs) == 0 {
		return
	}
	l.c.observability.Logger().Warn("gateway_client_ip_account_failure_confirmed_after_final_failure", map[string]any{
		"event":               "gateway_client_ip_account_failure_confirmed_after_final_failure",
		"reason":              reason,
		"confirmedAccountIds": result.ConfirmedAccountIDs,
		"systemAccountId":     context.UsageContext.SystemAccountID,
		"apiKeyId":            context.UsageContext.APIKeyID,
		"groupId":             context.UsageContext.GroupID,
		"clientIp":            context.UsageContext.ClientIP,
	}, "请求失败已返回客户端，客户端 IP 级账号回避状态已立即确认")
	l.auditCapture.AddGatewayMetadata("client_ip_account_avoidance_update", map[string]any{
		"reason":              reason,
		"confirmedAccountIds": result.ConfirmedAccountIDs,
	})
}

// finalizeRouteAction mirrors the Node finalizeRouteAction: the route-action
// failure / client-handoff / blocked / exhausted exits (routes.ts:373-432).
func (l *v1DispatchLoop) finalizeRouteAction(action *gatewaypreauth.RouteAction) {
	res := l.res
	if writableEndedOf(res) {
		return
	}
	if action.Failure != nil {
		failure := action.Failure
		if failure.RetryAfterMs != nil && !res.HeadersSent() {
			retryAfterSeconds := (*failure.RetryAfterMs + 999) / 1000
			if retryAfterSeconds < 1 {
				retryAfterSeconds = 1
			}
			res.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSeconds))
		}
		l.c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
			Req:             l.req,
			Res:             res,
			AuditCapture:    l.auditCapture,
			UsageContext:    action.UsageContext,
			StartedAt:       l.startedAt,
			StatusCode:      failure.StatusCode,
			ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf(failure.Message, failure.ErrorType, failure.ErrorCode),
			Audit: gatewaypreauth.FailureAudit{
				Outcome:      gatewaypreauth.AuditOutcomeGatewayFailed,
				ErrorPhase:   failure.ErrorPhase,
				ErrorCode:    failure.ErrorCode,
				ErrorMessage: failure.Message,
			},
			FailureAttribution: failure.FailureAttribution,
		})
		return
	}
	if action.Coordination.Outcome == gatewaypreauth.RouteOutcomeClientHandoff {
		// Node 398-411: the client-handoff outcome renders the stream server
		// retry exhausted contract instead of the exhausted-accounts copy.
		l.sendStreamServerRetryExhaustedResponse(streamServerRetryExhaustedInput{
			message:        "当前路由暂时无法继续派发，请客户端重试并重新选择可用账户",
			retryReason:    "pre_commit_stream_failure",
			errorCode:      gatewaypreauth.GatewayStreamClientRetryErrorCode,
			usageContext:   action.UsageContext,
			clientStrategy: &action.ClientStrategy,
		})
		return
	}
	temporarilyBlocked := action.Coordination.Outcome == "temporarily_blocked"
	message := "当前路由没有可用的上游账户"
	if temporarilyBlocked {
		message = "当前路由暂时没有可派发账户，请稍后重试"
	}
	l.c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:             l.req,
		Res:             res,
		AuditCapture:    l.auditCapture,
		UsageContext:    action.UsageContext,
		StartedAt:       l.startedAt,
		StatusCode:      http.StatusServiceUnavailable,
		ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf(message, "service_unavailable", "upstream_retryable_error"),
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      gatewaypreauth.AuditOutcomeGatewayFailed,
			ErrorPhase:   "dispatch",
			ErrorCode:    "upstream_retryable_error",
			ErrorMessage: message,
		},
		FailureScope: "upstream",
	})
}

// classifyGatewayDispatchExhaustion mirrors
// response/dispatch-exhaustion-classifier.ts.
func classifyGatewayDispatchExhaustion(lastAttempt *gatewaydispatch.UpstreamAttempt) (string, any) {
	if lastAttempt == nil {
		return "no_available_account", nil
	}
	switch lastAttempt.UpstreamURL {
	case "account:api_key_pool_unavailable":
		return "api_key_pool_unavailable", nil
	case "account:locally_suppressed":
		return "all_accounts_locally_suppressed", nil
	case "concurrency:limit":
		return "account_concurrency_exhausted", nil
	}
	if lastAttempt.HasStatus && lastAttempt.Status > 0 {
		return "upstream_http_error", lastAttempt.Status
	}
	return "upstream_transport_error", nil
}

// isEmptyPreflightResult mirrors the Node undefined member of the
// DispatchContext | RouteAction | undefined union.
func isEmptyPreflightResult(result gatewaypreauth.PreflightResult) bool {
	return result.DispatchContext == nil && result.RouteAction == nil
}

// routeStrategyIDOf mirrors routes.ts:535 preflight.apiKeyRecord?.route_strategy_id.
func routeStrategyIDOf(record *gatewayruntimecache.GatewayAPIKeyRow) string {
	if record == nil {
		return ""
	}
	return record.RouteStrategyID
}

// clientIPSlotReleaseList 收集每个 DispatchContext 的 client-IP 并发槽释放
// 闭包（D-109，BUG-0175；Node routes.ts attachClientIpSlotRelease 的
// res.once('finish'/'close') 语义：每次 preflight / 组切换都会追加一个
// release，响应终态统一触发）。release 本身是 once 幂等的。
type clientIPSlotReleaseList struct {
	mu       sync.Mutex
	releases []func()
}

// Add appends one release closure; nil releases are skipped.
func (l *clientIPSlotReleaseList) Add(release func()) {
	if release == nil {
		return
	}
	l.mu.Lock()
	l.releases = append(l.releases, release)
	l.mu.Unlock()
}

// ReleaseAll runs every collected release once (idempotent per slot).
func (l *clientIPSlotReleaseList) ReleaseAll() {
	l.mu.Lock()
	releases := l.releases
	l.releases = nil
	l.mu.Unlock()
	for _, release := range releases {
		release()
	}
}
