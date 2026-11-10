package gatewaydispatch

import (
	"context"
	"errors"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// handleUpstreamAttemptResponse + handleUpstreamAttemptError: the response-ok
// / failed-response / error branches of the attempt loop
// (dispatch/upstream-dispatch.ts).

// newConfirmSessionApiKeyFingerprintFunc 构造账户内 Key 会话黏性的最终成功写回
// 闭包：把本尝试选中的上游 Key 指纹写回现存同 owner 会话绑定（服务端语义只
// 更新、不创建、不改 owner）。无会话键、无选中指纹（非池隔离开启路径不导出
// 指纹）或端口未装配时返回 nil，链面 nil 检查自然跳过。捕获 WithoutCancel
// 上下文：写回发生在响应完成之后，请求 ctx 可能已被取消。
func newConfirmSessionApiKeyFingerprintFunc(port SessionAffinityPort, ctx context.Context, sessionAffinityKey string, account AccountCandidate) func() {
	if port == nil || sessionAffinityKey == "" || account.SelectedAPIKeyFingerprint == nil {
		return nil
	}
	fingerprint := trimString(*account.SelectedAPIKeyFingerprint)
	if fingerprint == "" {
		return nil
	}
	writeCtx := context.WithoutCancel(ctx)
	accountID := account.ID
	return func() {
		port.RememberApiKeyFingerprintAsync(writeCtx, sessionAffinityKey, accountID, fingerprint)
	}
}

// newConfirmGroupAffinityFunc 构造组级会话黏性的最终成功写回闭包（策略路由分
// 组会话亲和设计 §5）：把最终成功派发账户所属分组写回/创建组绑定。无组亲和键、
// 无落组或端口未装配时返回 nil。捕获 WithoutCancel 上下文，与 Key 指纹写回同
// 一请求生命周期语义。
func newConfirmGroupAffinityFunc(port GroupAffinityPort, ctx context.Context, groupAffinityKey, routeStrategyID, groupID string) func() {
	if port == nil || groupAffinityKey == "" || groupID == "" {
		return nil
	}
	writeCtx := context.WithoutCancel(ctx)
	return func() {
		port.RememberGroupAsync(writeCtx, groupAffinityKey, routeStrategyID, groupID)
	}
}

// newConfirmSessionAffinityRebindFunc 构造账户亲和同层 latch 的最终成功结算
// 闭包：同 owner 刷新 30 分钟 lease；owner 变化时先记跨账户切换，容量拒绝 +
// 同层且端口实现 RebindAsync 时 CAS 重绑到成功账户（跨层/非容量变化只记指标，
// 不改绑）。端口未实现可选 RebindAsync 面（测试 fake/降级）时仅保留跨账户计数
// 句柄；无会话键/owner/成功账户时返回 nil，链面 nil 检查自然跳过。
func newConfirmSessionAffinityRebindFunc(port SessionAffinityPort, ctx context.Context, sessionAffinityKey, expectedOwner, newOwner string, expectedOwnerUnavailable, expectedOwnerSoftBusy, slotFallback, sameLayer bool, scope AffinityScope, auditCapture AuditCapture) func() {
	if port == nil || sessionAffinityKey == "" || expectedOwner == "" || newOwner == "" {
		return nil
	}
	rebind, canRebind := port.(SessionAffinityRebindPort)
	writeCtx := context.WithoutCancel(ctx)
	if expectedOwner != newOwner {
		eligible := (expectedOwnerUnavailable || expectedOwnerSoftBusy || slotFallback) && (sameLayer || slotFallback) && canRebind
		reason := "capacity"
		switch {
		case slotFallback:
			reason = "slot_fallback"
		case !expectedOwnerUnavailable && expectedOwnerSoftBusy:
			reason = "soft_threshold"
		}
		return func() {
			gatewayusage.RecordGatewaySessionAffinityCrossAccountSwitchMetric()
			if !eligible {
				return
			}
			applied := rebind.RebindAsync(writeCtx, sessionAffinityKey, expectedOwner, newOwner, scope)
			gatewayusage.RecordGatewaySessionAffinityRebindMetric(reason, applied)
			if auditCapture.Nil() {
				return
			}
			auditCapture.AddGatewayMetadata("session_affinity_rebind", map[string]any{
				"fromAccountId": expectedOwner,
				"toAccountId":   newOwner,
				"reason":        reason,
				"sameLayer":     sameLayer,
				"slotFallback":  slotFallback,
				"applied":       applied,
			})
		}
	}
	if !canRebind {
		return nil
	}
	return func() {
		applied := rebind.RebindAsync(writeCtx, sessionAffinityKey, expectedOwner, newOwner, scope)
		gatewayusage.RecordGatewaySessionAffinityRebindMetric("success", applied)
		if auditCapture.Nil() {
			return
		}
		auditCapture.AddGatewayMetadata("session_affinity_rebind", map[string]any{
			"fromAccountId": expectedOwner,
			"toAccountId":   newOwner,
			"reason":        "success",
			"sameLayer":     sameLayer,
			"slotFallback":  false,
			"applied":       applied,
		})
	}
}

// newConfirmSessionAffinitySlotFunc 构造最终成功后的名额结算闭包：把成功
// 账户写入/刷新名额表，owner 变化时释放旧名额；名额满时 LRU 驱逐最久空闲
// 会话。端口未实现（memory 降级/测试）返回 nil。
func newConfirmSessionAffinitySlotFunc(port SessionAffinityPort, ctx context.Context, sessionAffinityKey, expectedOwner, newOwner string, concurrencyLimit, rpmLimit int, slotFallback bool, auditCapture AuditCapture) func() {
	slotPort, ok := port.(SessionAffinitySlotPort)
	if !ok || sessionAffinityKey == "" || newOwner == "" {
		return nil
	}
	writeCtx := context.WithoutCancel(ctx)
	cap := gatewaysession.SessionSlotCap(concurrencyLimit, rpmLimit)
	return func() {
		applied := slotPort.ConfirmSessionSlot(writeCtx, sessionAffinityKey, expectedOwner, newOwner, cap)
		// 只在实际成功账户不同于原 owner 且名额结算应用成功时计名额回退，
		// 与 rebind reason=slot_fallback 的 applied 口径对齐。
		if applied && slotFallback && expectedOwner != "" && expectedOwner != newOwner {
			gatewayusage.RecordGatewaySessionAffinitySlotFallbackMetric()
		}
		if auditCapture.Nil() {
			return
		}
		auditCapture.AddGatewayMetadata("session_affinity_slot_confirm", map[string]any{
			"fromAccountId": expectedOwner,
			"toAccountId":   newOwner,
			"cap":           cap,
			"slotFallback":  slotFallback,
			"applied":       applied,
		})
	}
}

// sessionAffinityOwnerWasCapacityRejected 报告 owner 是否在本请求被本地容量
// 拒绝（当前整池轮次的失败集合；owner 在成功轮次仍被排到前面时即可命中）。
func sessionAffinityOwnerWasCapacityRejected(failures []AccountCapacityLimitFailure, ownerID string) bool {
	if ownerID == "" {
		return false
	}
	for _, failure := range failures {
		if failure.account.ID == ownerID {
			return true
		}
	}
	return false
}

// sessionAffinitySameLayer 用四元调度层比较 owner 与最终成功账户；owner 不在
// 本请求候选窗口（硬过滤/已删除）时返回 false，P0 不重绑。
func sessionAffinitySameLayer(accounts []AccountCandidate, ownerID string, success AccountCandidate, priority *gatewayrouting.GatewayAccountModelPriority) bool {
	if ownerID == "" || ownerID == success.ID {
		return true
	}
	for _, account := range accounts {
		if account.ID != ownerID {
			continue
		}
		return dispatchPriorityTierOf(account, priority) == dispatchPriorityTierOf(success, priority)
	}
	return false
}

// rememberUpstreamAttemptFailure 结算现场构造来源避让 AccountFailure（调度慢
// 失败直切与墙钟收尾修复设计 §3 B1）：回调未注入（测试/内部派发）不记账；
// 状态码取自结算后的 lastAttempt（传输失败无状态码保持 nil），消息优先取
// lastAttempt.Message，缺失回落调用方给定的原始错误文案。
func rememberUpstreamAttemptFailure(args *FetchFirstAvailableUpstreamArgs, account AccountCandidate, upstreamURL, errorPhase string, lastAttempt *UpstreamAttempt, fallbackMessage string) {
	if args == nil || args.RememberUpstreamAttemptFailure == nil {
		return
	}
	failure := gatewayclientip.AccountFailure{
		AccountName:  account.Name,
		ErrorPhase:   errorPhase,
		ErrorMessage: fallbackMessage,
		Endpoint:     upstreamURL,
	}
	if lastAttempt != nil {
		if lastAttempt.HasStatus && lastAttempt.Status > 0 {
			status := int64(lastAttempt.Status)
			failure.StatusCode = &status
		}
		failure.ErrorCode = lastAttempt.ErrorCode
		if lastAttempt.Message != "" {
			failure.ErrorMessage = lastAttempt.Message
		}
	}
	args.RememberUpstreamAttemptFailure(account.ID, failure)
}

// handleUpstreamAttemptResponse mirrors the `if (response.ok) {...}` block
// plus the failed-response tail of the attempt loop.
func (e *Engine) handleUpstreamAttemptResponse(ctx context.Context, c upstreamAttemptResponseContext) (responseKind, responseStopKind, error) {
	in := c.loop.in
	usageContext := c.loop.usageContext
	auditCapture := c.loop.auditCapture
	response := c.response

	*in.lastAttempt = &UpstreamAttempt{
		AccountID:                 c.account.ID,
		AccountName:               c.account.Name,
		ProviderCode:              c.account.VendorCode,
		ProviderProtocolProfileID: c.account.ProviderProtocolProfileID,
		ProtocolCode:              c.account.ProtocolCode,
		ProtocolVersion:           c.account.ProtocolVersion,
		UpstreamURL:               c.upstreamURL,
		Status:                    response.Status(),
		HasStatus:                 true,
	}

	if response.OK() {
		e.Affinity.RememberAsync(ctx, in.args.SessionAffinityKey, c.account.ID, AffinityScope{
			SystemAccountID: usageContext.SystemAccountID,
			APIKeyID:        usageContext.APIKeyID,
			GroupID:         usageContext.GroupID,
		})
		*c.loop.keepConcurrencySlotRef = true
		// R2 前移收回（BUG-0267 post-verdict 结算块）：confirmation 不再在拿到
		// 2xx 头部时前置 ReportFramingComplete——Node 在 body 处理完成后按实际
		// 结局分类结算（routes.ts:1865-1869），"2xx 但 body 中途死亡"被前置
		// 结算记成治愈证据（framing_complete → RECOVERING、失败证据清零）是
		// 方向性错误。结算移交链面 post-verdict 块（Node :1865-1877），句柄经
		// UpstreamDispatchResult 带出；R2 的租约悬挂担忧由链面响应轮 defer 的
		// 兜底结算覆盖（settleTransferredUpstreamAttemptsSafely，对齐 Node
		// :2509-2521）。失败/错误分支的前置结算保持不变（各分支的
		// ReportUnknown / ReportTransportFailure 见下文与 handleUpstreamAttemptError）。
		var responsePrecommitDeadlineAtMs *int64
		if in.requestLane != "image" && !in.coordination.GatewayRequestWallBudget.Unbounded {
			value := in.coordination.GatewayRequestWallBudget.DeadlineAtMs - gatewayrouting.DefaultGatewayFinalResponseReserveMs
			responsePrecommitDeadlineAtMs = &value
		}
		accountLockLeaseRelease := in.createAccountLockLeaseRelease(false)
		confirmFailures := append([]PendingAccountApiKeyFailure{}, *c.loop.pendingApiKeyFailuresRef...)
		affinityOwnerID := in.args.SessionAffinityOwnerAccountID
		var affinityFailures []AccountCapacityLimitFailure
		if in.capacityLimitFailures != nil {
			affinityFailures = *in.capacityLimitFailures
		}
		affinityOwnerCapacityRejected := in.args.SessionAffinityOwnerBusy ||
			sessionAffinityOwnerWasCapacityRejected(affinityFailures, affinityOwnerID)
		affinitySameLayer := sessionAffinitySameLayer(in.args.Accounts, affinityOwnerID, c.account, in.args.ModelPriority)
		affinityScope := AffinityScope{
			SystemAccountID: usageContext.SystemAccountID,
			APIKeyID:        usageContext.APIKeyID,
			GroupID:         usageContext.GroupID,
		}
		*c.loop.resultRef = &UpstreamDispatchResult{
			Account:              c.account,
			Response:             response,
			RequestBody:          c.loop.body,
			UpstreamURL:          c.upstreamURL,
			AuditAttemptID:       c.auditAttemptID,
			AttemptStartedAt:     c.attemptStartedAt,
			EffectiveServiceTier: c.loop.effectiveServiceTier,
			// BUG-0343：生效档位/思考级别随结果带出，链面成功终局回填 usage
			// 上下文（引擎只更新自己的值拷贝，链面读的是原始副本）。
			EffectiveReasoningEffort: c.loop.effectiveReasoningEffort,
			TimeoutProfile:           in.timeoutProfile,
			// BUG-0267：circuit/keyModel 句柄带出——链面 post-verdict 结算块按
			// body 实际结局分类结算（Node routes.ts:1865-1877）。key-model 准入
			// 让位路径（dispatchsingle.go 准入互斥）已把 circuit 句柄置 nil 并在
			// 引擎内 ReportUnknown，链面 nil 跳过语义自洽。
			AccountCircuitAttempt: in.accountCircuitAttempt,
			KeyModelAttempt:       c.keyModelAttempt,
			// 观测面带出本尝试的真实索引（与失败派发器输入同值域：
			// auditAttemptIndex 在每次真实尝试前自增，是该尝试的 1-based 序数）。
			AttemptIndex:       *c.attemptIndex,
			AuditAttemptIndex:  *in.auditAttemptIndex,
			ReleaseConcurrency: onceFunc(c.loop.concurrencySlot.Release),
			MarkFirstOutput:    c.markFirstOutput,
			ConfirmSameAccountApiKeyFailures: func() error {
				return e.recordConfirmedSameAccountApiKeyFailures(ctx, confirmFailures, c.account, usageContext)
			},
			// D-111（BUG-0175）：成功侧结算随协议成功在链上消费（Node
			// recordGatewayAccountApiKeySuccess 的成功触发点；nil 端口时为
			// 中性 no-op）。
			ConfirmAccountAPIKeySuccess: func() error {
				return e.recordAccountAPIKeySuccess(ctx, c.account, usageContext)
			},
			// 账户内 Key 会话亲和（设计 §5/§6）：闭包携带本尝试选中的上游 Key
			// 指纹，链面仅在最终协议成功结算消费一次；2xx 响应头阶段不执行，
			// 写回失败只告警、不改写已完成响应。
			ConfirmSessionApiKeyFingerprint: newConfirmSessionApiKeyFingerprintFunc(e.Affinity, ctx, in.args.SessionAffinityKey, c.account),
			// 组级会话黏性（策略路由分组会话亲和设计 §5）：闭包携带本尝试落组
			// 的组绑定写回，链面仅在最终协议成功结算消费一次（组 → 账户 → Key
			// 顺序由链面结算块保证）；写回失败只告警，不改写已完成响应。
			ConfirmGroupAffinity: newConfirmGroupAffinityFunc(e.GroupAffinity, ctx, in.args.GroupAffinityKey, in.args.RouteStrategyID, usageContext.GroupID),
			// 账户亲和同层 latch（同层单向粘性设计 §5.5）：同 owner 刷新 lease；
			// owner 变化且本请求容量拒绝 + 同层时 CAS 重绑到成功账户。链面仅在
			// 最终协议成功结算消费一次（组 → 账户 → Key 顺序中的“账户”层）。
			ConfirmSessionAffinityRebind:         newConfirmSessionAffinityRebindFunc(e.Affinity, ctx, in.args.SessionAffinityKey, affinityOwnerID, c.account.ID, affinityOwnerCapacityRejected, in.args.SessionAffinityOwnerSoftBusy, in.args.SessionAffinitySlotFallback, affinitySameLayer, affinityScope, in.args.AuditCapture),
			ConfirmSessionAffinitySlot:           newConfirmSessionAffinitySlotFunc(e.Affinity, ctx, in.args.SessionAffinityKey, affinityOwnerID, c.account.ID, accountHardConcurrencyLimit(c.account), c.account.RPMLimit, in.args.SessionAffinitySlotFallback, in.args.AuditCapture),
			SessionAffinityOwnerAccountID:        affinityOwnerID,
			SessionAffinityOwnerCapacityRejected: affinityOwnerCapacityRejected,
			SessionAffinitySameLayer:             affinitySameLayer,
			// 成功侧结算：ENGAGED 锁复位（Node 完全成功后调用
			// completeAccountLockSuccessAsync，routes.ts:2481-2483）。
			ConfirmAccountLockSuccess: func() error {
				if in.accountLockTrafficEnabled && e.Locks != nil {
					var obs *AccountLockObservation
					if in.activeAccountLockObservation != nil {
						obs = *in.activeAccountLockObservation
					}
					return e.Locks.CompleteSuccessAsync(ctx, c.account.ID, "", obs)
				}
				return nil
			},
			ConfirmHalfOpenSuccess: func() bool {
				if !in.automaticAccountStateMutationAllowed {
					return false
				}
				return completeHalfOpenLeaseSuccess(ctx, c.loop.halfOpenLease)
			},
			ReleaseHalfOpenLease: func() bool {
				return releaseHalfOpenLease(ctx, c.loop.halfOpenLease)
			},
			HotQualityAttempt:             c.hotQualityAttempt,
			NormalRouteFirstByteDeadline:  c.normalRouteFirstByteDeadline,
			ResponsePrecommitDeadlineAtMs: responsePrecommitDeadlineAtMs,
			OnFirstByteDeadline:           c.onFirstByteDeadline,
			FirstByteDeadlineCoordinator:  c.firstByteDeadlineCoordinator,
			AccountLockObservation:        *in.activeAccountLockObservation,
			AccountLockRetryLease:         *in.activeAccountLockRetryLease,
			ReleaseAccountLockRetryLease:  accountLockLeaseRelease,
			UpstreamResponseModelSlot:     c.upstreamResponseModelSlot,
		}
		return responseKindSelected, responseStopNone, nil
	}

	// Failed HTTP response path.
	if c.firstByteDeadlineCoordinator != nil {
		c.firstByteDeadlineCoordinator.Supersede()
	}
	failedResponseResult, handleErr := e.FailureDispatcher.HandleFailedUpstreamResponse(ctx, FailedUpstreamResponseInput{
		Req:                                  in.args.Req,
		RequestLane:                          in.requestLane,
		UsageContext:                         *usageContext,
		AuditCapture:                         auditCapture,
		AuditAttemptID:                       c.auditAttemptID,
		Account:                              c.account,
		UpstreamURL:                          c.upstreamURL,
		Response:                             response,
		RequestBody:                          c.loop.body,
		Settings:                             in.settings,
		AttemptStartedAt:                     c.attemptStartedAt,
		AttemptIndex:                         *c.attemptIndex,
		AuditAttemptIndex:                    *in.auditAttemptIndex,
		SessionAffinityKey:                   in.args.SessionAffinityKey,
		LastAttempt:                          *in.lastAttempt,
		RequestClientCompatibility:           in.args.RequestClientCompatibility,
		ClientIPAccountAvoidance:             in.args.ClientIPAccountAvoidanceTracker,
		AccountStateMutationEnabled:          in.args.AccountStateMutationEnabled,
		AutomaticAccountStateMutationEnabled: in.automaticAccountStateMutationAllowed,
		DeferAutomaticSameAccountKeyRotation: !*c.firstByteDeadlineTriggeredRef &&
			halfOpenLeaseUnclaimed(c.loop.halfOpenLease) &&
			isTransientSameAccountHttpStatus(response.Status()),
	})
	if handleErr != nil {
		auditCapture.CompleteAttempt(c.auditAttemptID, CompleteAttemptInput{
			Success: false, ErrorPhase: "upstream_request", ErrorMessage: handleErr.Error(),
		})
		return responseKindContinue, responseStopNone, handleErr
	}
	// B1 记账（调度慢失败直切与墙钟收尾修复设计 §3）：上游归因响应失败在
	// 结算点记入来源避让 PendingFailures，透传与跳过两臂同为上游归因失败。
	rememberUpstreamAttemptFailure(in.args, c.account, c.upstreamURL, "upstream_response", failedResponseResult.LastAttempt, "")
	explicitPolicyFailure := failedResponseResult.Action != FailedResponseActionReturnResponse &&
		failedResponseResult.FailureKind == FailureKindExplicitPolicy
	if failedResponseResult.Action != FailedResponseActionReturnResponse {
		outcomeClass := HotQualityOutcomeUpstreamResponseFailure
		failureScope := "none"
		source := "upstream_response"
		if explicitPolicyFailure {
			outcomeClass = HotQualityOutcomeExplicitPolicyFailure
			failureScope = "account"
			source = "explicit_policy"
		}
		c.hotQualityAttempt.RecordTerminal(ctx, HotQualityTerminal{
			OutcomeClass: outcomeClass, FailureScope: failureScope, Source: source,
		})
	}
	if failedResponseResult.Action == FailedResponseActionReturnResponse {
		*c.loop.keepConcurrencySlotRef = true
		// R2 修复（透传侧结算）：失败响应直接透传给客户端的分支（真实触发面：
		// 账号诊断流量 chain_ports.go:305 与非网关流量 :314 的 ReturnResponse
		// 配置）原先不结算 confirmation，租约悬挂至 30s leaseUntil 过期。该
		// 分支只可能在非 2xx 失败响应时到达（成功早已走 response.OK() 分支），
		// 故与失败分支同构调 ReportUnknown：立即释放租约、保持 SUSPECT、不新
		// 增失败证据；observer 尝试为 no-op。nil 句柄自然跳过。
		if in.accountCircuitAttempt != nil {
			_, _ = in.accountCircuitAttempt.ReportUnknown(ctx)
		}
		// BUG-0267（ReturnResponse 臂 keyModel 结算）：keyModel 原结算位于
		// 本分支 return 之后的公共路径，对透传路径不可达，准入许可永不释放
		// （settle 前 renewal 持续续租）。与上方 circuit 同构就地
		// ReportUnknown（Node :1876 reportUpstreamNotComplete 同分支语义）；
		// settle-once 幂等，公共路径对已结算句柄不再重复结算。
		if c.keyModelAttempt != nil {
			_ = c.keyModelAttempt.ReportUnknown(ctx)
		}
		var responsePrecommitDeadlineAtMs *int64
		if in.requestLane != "image" && !in.coordination.GatewayRequestWallBudget.Unbounded {
			value := in.coordination.GatewayRequestWallBudget.DeadlineAtMs - gatewayrouting.DefaultGatewayFinalResponseReserveMs
			responsePrecommitDeadlineAtMs = &value
		}
		accountLockLeaseRelease := in.createAccountLockLeaseRelease(false)
		*c.loop.resultRef = &UpstreamDispatchResult{
			Account:              c.account,
			Response:             failedResponseResult.Response,
			RequestBody:          c.loop.body,
			UpstreamURL:          c.upstreamURL,
			AuditAttemptID:       c.auditAttemptID,
			AttemptStartedAt:     c.attemptStartedAt,
			EffectiveServiceTier: c.loop.effectiveServiceTier,
			// BUG-0343：同成功臂，生效思考级别随结果带出供链面回填。
			EffectiveReasoningEffort:         c.loop.effectiveReasoningEffort,
			TimeoutProfile:                   in.timeoutProfile,
			AttemptIndex:                     *c.attemptIndex,
			AuditAttemptIndex:                *in.auditAttemptIndex,
			UpstreamStageRecorded:            failedResponseResult.UpstreamStageRecorded,
			ReleaseConcurrency:               onceFunc(c.loop.concurrencySlot.Release),
			MarkFirstOutput:                  c.markFirstOutput,
			ConfirmSameAccountApiKeyFailures: func() error { return nil },
			// BUG-0247 项 1：ReturnResponse 分支补齐成功侧结算回调，与
			// response.OK() 分支同构（Node 的调用点是 routes.ts:2481-2483
			// protocolValidatedSuccess 时 completeAccountLockSuccessAsync；Go
			// 以结果字段承载，chain 面 confirmProtocolSuccessSideEffects 消费）。
			// 本分支只承载非 2xx 透传（协议验证成功在链上恒为 false，回调
			// 当面不会被触发），字段补齐是结构对齐：透传响应若未来通过协议
			// 校验，锁复位随结果可达，无需再造分支。
			ConfirmAccountLockSuccess: func() error {
				if in.accountLockTrafficEnabled && e.Locks != nil {
					var obs *AccountLockObservation
					if in.activeAccountLockObservation != nil {
						obs = *in.activeAccountLockObservation
					}
					return e.Locks.CompleteSuccessAsync(ctx, c.account.ID, "", obs)
				}
				return nil
			},
			ConfirmHalfOpenSuccess: func() bool { return false },
			ReleaseHalfOpenLease: func() bool {
				return releaseHalfOpenLease(ctx, c.loop.halfOpenLease)
			},
			HotQualityAttempt:             c.hotQualityAttempt,
			NormalRouteFirstByteDeadline:  c.normalRouteFirstByteDeadline,
			ResponsePrecommitDeadlineAtMs: responsePrecommitDeadlineAtMs,
			OnFirstByteDeadline:           c.onFirstByteDeadline,
			AccountLockObservation:        *in.activeAccountLockObservation,
			AccountLockRetryLease:         *in.activeAccountLockRetryLease,
			ReleaseAccountLockRetryLease:  accountLockLeaseRelease,
			UpstreamResponseModelSlot:     c.upstreamResponseModelSlot,
		}
		return responseKindSelected, responseStopNone, nil
	}
	if c.keyModelAttempt != nil {
		_ = c.keyModelAttempt.ReportUnknown(ctx)
	}
	// T65-①（网关上游 401 自动刷新）：失败派发器完成一次 OAuth 单飞刷新并把
	// 重新水合的候选带回时，同账户以新凭据重试一次。复用
	// retry_with_compatibility_recovery 的引擎处理形态（改写循环态 + 新语义
	// 重试 ID 注册 → attemptIndex++; continue），same-account retry 预算
	//（reserveSameAccountRetry / TryReserveSameAccountRetry）语义不变；候选
	// 缺失或请求件重建失败时不吞证据，落回既有候选拒绝面（下方 skip 路径）。
	if failedResponseResult.Action == FailedResponseActionRetryRefreshedSameAccount && failedResponseResult.RefreshedAccount != nil {
		rebuilt, partsErr := e.BuildPreparedUpstreamRequestParts(ctx, in.args.Req, *failedResponseResult.RefreshedAccount, *usageContext, in.args.RequestClientCompatibility)
		if partsErr == nil {
			c.loop.account = *failedResponseResult.RefreshedAccount
			c.loop.headers = rebuilt.Headers
			c.loop.body = rebuilt.Body
			in.semanticRetryID = "oauth-refresh:" + uuid4String()
			return responseKindContinue, responseStopSemanticRetry, nil
		}
		auditCapture.AddGatewayMetadata("oauth_refreshed_retry_rebuild_failed", map[string]any{
			"accountId": failedResponseResult.RefreshedAccount.ID,
			"reason":    partsErr.Error(),
		})
	}
	if failedResponseResult.Action == FailedResponseActionRetryWithCompatibilityRecovery {
		c.loop.body = failedResponseResult.Recovery.Body
		// Node keeps the recovery semantic-retry id in the local
		// `activeSemanticRetryId` and the next attempt registration consumes
		// it (upstream-dispatch.ts:1488/1257); the per-dispatch input field is
		// that local, not the shared coordination context.
		in.semanticRetryID = failedResponseResult.Recovery.SemanticRetryID
		return responseKindContinue, responseStopSemanticRetry, nil
	}
	// R2 修复（失败侧结算）：返回 429/500 的失败响应不再走
	// ReportFramingComplete（原调用把 SUSPECT 推进 RECOVERING、失败证据清零，
	// observer 尝试还会被失败响应直接关成 CLOSED——失败上游反向治愈熔断），
	// 改为 ReportUnknown：租约立即释放、保持 SUSPECT、不新增失败证据、退避
	// 后允许下一次 confirmation 认领（memory 与 redis/Lua 行为一致）。
	if in.accountCircuitAttempt != nil {
		_, _ = in.accountCircuitAttempt.ReportUnknown(ctx)
	}
	*in.lastAttempt = failedResponseResult.LastAttempt
	// 锁死窗口推进（设计 §3.3/§3.4）：完整非 2xx 不是有效成功，必须作为真实
	// 失败写入锁死运行态并把 LOCKED_IDLE 推进到 ENGAGED（已有 ENGAGED 窗口时
	// 幂等），然后按死期结算。只有 complete_success 能清窗；Key 轮换 / 同账号
	// 有界重试等后续分支照常执行，锁死门只否决跨账号切换。
	//
	// M1（复审）：锁死记账是运行态观测，best-effort 失败只落审计，不得否决
	// 既有 Key 轮换 / 同账号重试 / 锁死判断。
	// M2（复审）：显式策略失败已由失败派发器把账户置为不可调度，不再写锁死
	// 运行态，也不参与末尾锁死门（按原机制换号/结束）。
	if in.accountLockTrafficEnabled && e.Locks != nil && !explicitPolicyFailure {
		if err := e.Locks.RecordFailureAsync(ctx, c.account.ID, "upstream_http_response", *in.activeAccountLockObservation); err != nil {
			auditCapture.AddGatewayMetadata("account_lock_failure_record_error", map[string]any{
				"accountId": c.account.ID,
				"phase":     "record_failure",
				"error":     err.Error(),
			})
		}
		if err := e.Locks.SettleDeadlineAsync(ctx, c.account.ID, gatewayupstream.NowMs(), *in.activeAccountLockObservation); err != nil {
			auditCapture.AddGatewayMetadata("account_lock_failure_record_error", map[string]any{
				"accountId": c.account.ID,
				"phase":     "settle_deadline",
				"error":     err.Error(),
			})
		}
	}
	in.failedAccountIDs[c.account.ID] = struct{}{}
	delete(in.recoverableFailedAccountIDs, c.account.ID)
	delete(in.cycleRecoverableAccountIDs, c.account.ID)

	// API-key rotation decision (Node
	// shouldTryAnotherAccountApiKeyForRequest / shouldRetryAnotherAccountApiKey).
	tryNextKey := false
	if failedResponseResult.TryNextApiKeyForRequest {
		tryNextKey = e.shouldTryAnotherAccountApiKeyForRequest(c.account, *c.loop.accountApiKeyAttemptCount, *in.requestApiKeyAttemptCount)
	} else {
		tryNextKey = e.shouldRetryAnotherAccountApiKey(c.account, failedResponseResult.KeyScopedFailure, *c.loop.accountApiKeyAttemptCount, *in.requestApiKeyAttemptCount, auditCapture)
	}
	if !*c.firstByteDeadlineTriggeredRef && halfOpenLeaseUnclaimed(c.loop.halfOpenLease) && tryNextKey {
		if failedResponseResult.PendingApiKeyFailure != nil {
			*c.loop.pendingApiKeyFailuresRef = append(*c.loop.pendingApiKeyFailuresRef, *failedResponseResult.PendingApiKeyFailure)
		}
		*c.loop.retryAccountApiKeyRef = true
		return responseKindContinue, responseStopRetryKey, nil
	}
	if in.requestLane == "text" && !*c.firstByteDeadlineTriggeredRef &&
		halfOpenLeaseUnclaimed(c.loop.halfOpenLease) &&
		failedResponseResult.Action == FailedResponseActionSkipAccount &&
		failedResponseResult.FailureKind != FailureKindExplicitPolicy &&
		isTransientSameAccountHttpStatus(lastStatusOf(failedResponseResult.LastAttempt)) {
		// 慢失败直切（调度慢失败直切与墙钟收尾修复设计 §2）：耗时达到
		// SameAccountRetryFastFailMaxMs 的失败不再原地重试，直接轮换下一候选；
		// 同账户重试预算上限 min(2, temporaryUnschedulableRetryAttempts) 不变。
		if elapsedMs := gatewayupstream.NowMs() - c.attemptStartedAt; elapsedMs >= in.settings.SameAccountRetryFastFailMaxMs {
			auditCapture.AddGatewayMetadata("same_account_retry_skipped_slow_failure", map[string]any{
				"accountId":   c.account.ID,
				"elapsedMs":   elapsedMs,
				"thresholdMs": in.settings.SameAccountRetryFastFailMaxMs,
				"retryReason": "upstream_http_response",
			})
		} else {
			sameAccountRetryID, retryErr := in.reserveSameAccountRetry(c.dispatchAttemptIdentity, "upstream_http_response", c.account.ID)
			if retryErr != nil {
				return responseKindContinue, responseStopNone, retryErr
			}
			if sameAccountRetryID != "" {
				*in.activeSameAccountRetryID = sameAccountRetryID
				return responseKindContinue, responseStopRetrySameAccount, nil
			}
		}
	}
	// M2（复审）：显式策略失败已把账户置为不可调度，末尾不再走锁死门，直接
	// 按原有跳过出口换号/结束。
	if in.accountLockTrafficEnabled && e.Locks != nil && !explicitPolicyFailure {
		lockState, stateErr := e.Locks.FindStateAsync(ctx, c.account.ID)
		if stateErr != nil {
			// 锁死门 fail-closed 收窄（M4 复审）：只有本请求已观察到该账户的锁行
			// 时才按锁死有效处理；从未观察到锁行的账户没有被锁死证据，按既有
			// 跳过出口 fail-open，避免锁运行态读取抖动把请求钉在未锁账户上。
			auditCapture.AddGatewayMetadata("account_lock_state_read_failed", map[string]any{
				"accountId": c.account.ID,
				"reason":    "lock_state_read_failed",
				"error":     stateErr.Error(),
			})
			if accountLockObservationPresent(in.activeAccountLockObservation) {
				return responseKindContinue, responseStopAccountLocked, nil
			}
			return responseKindContinue, responseStopSkipAccount, nil
		}
		if lockState != nil && accountLockBlocksCrossAccount(*lockState) {
			return responseKindContinue, responseStopAccountLocked, nil
		}
	}
	return responseKindContinue, responseStopSkipAccount, nil
}

// accountLockObservationPresent 报告本请求是否已观察到该账户的锁行。锁状态
// 读取失败时只有已观察到锁行的请求才 fail-closed（M4 复审收窄），未观察到
// 锁行的请求按既有跳过出口 fail-open。
func accountLockObservationPresent(observation **AccountLockObservation) bool {
	return observation != nil && *observation != nil
}

// handleUpstreamAttemptError mirrors the `catch (error) {...}` block of the
// attempt loop.
func (e *Engine) handleUpstreamAttemptError(ctx context.Context, c upstreamAttemptErrorContext) (errorKind, errorStop, error) {
	in := c.loop.in
	usageContext := c.loop.usageContext
	auditCapture := c.loop.auditCapture
	signal := c.loop.signal
	err := c.attemptFailure

	var wallErr *GatewayRequestWallBudgetExhaustedError
	if errorsAs(err, &wallErr) {
		return errorKindRethrow, errorStop{kind: errorStopNone, rethrown: err}, nil
	}
	configuredFirstByteDeadline := IsGatewayFirstByteTimeoutError(err) &&
		firstByteTimeoutSourceOf(err) == FirstByteTimeoutSourceConfiguredDeadline
	neutralFirstByteDeadline := configuredFirstByteDeadline &&
		c.normalRouteFirstByteDeadline != nil &&
		(c.normalRouteFirstByteDeadline.LimitingFactor == gatewayrouting.FirstByteLimitingFactorConfigured ||
			c.normalRouteFirstByteDeadline.LimitingFactor == gatewayrouting.FirstByteLimitingFactorWallPrecommit)
	configuredDeadlineCutover := neutralFirstByteDeadline &&
		c.normalRouteFirstByteDeadline.LimitingFactor == gatewayrouting.FirstByteLimitingFactorConfigured
	if !configuredDeadlineCutover && c.firstByteDeadlineCoordinator != nil {
		c.firstByteDeadlineCoordinator.Supersede()
	}
	localRequestFailure := isLocalRequestFailure(err)
	primaryStartedTransportFailure := IsPrimaryStartedGatewayTransportError(err)
	provenBodyTransportFailure := IsProvenUpstreamBodyTransportError(err)
	provenStartedTransportFailure := primaryStartedTransportFailure || provenBodyTransportFailure
	// Dial-phase failures never reached the upstream (gateway-side DNS /
	// egress outage): every candidate-exclusion branch below keeps its
	// behavior, but the account circuit is not fed — a local network outage
	// would otherwise record every account as a proven upstream transport
	// failure.
	dialPhaseTransportFailure := IsDialPhaseStartedTransportError(err)
	if signal.Err() != nil || *c.firstByteDeadlineTriggeredRef || localRequestFailure || !provenStartedTransportFailure {
		if c.keyModelAttempt != nil {
			_ = c.keyModelAttempt.ReportUnknown(ctx)
		}
	}
	retryAnotherAccountApiKey := false
	{
		failoverAllowed := e.FailureDispatcher.IsOpaqueUpstreamFailoverAllowed(in.args.Req) ||
			(in.requestLane == "text" && in.accountCircuitAttempt != nil && in.accountCircuitAttempt.IsConfirmation())
		retryAnotherAccountApiKey = failoverAllowed && !localRequestFailure &&
			provenStartedTransportFailure && !neutralFirstByteDeadline &&
			signal.Err() == nil && !*c.firstByteDeadlineTriggeredRef &&
			halfOpenLeaseUnclaimed(c.loop.halfOpenLease) &&
			e.shouldTryAnotherAccountApiKeyForRequest(c.account, *c.loop.accountApiKeyAttemptCount, *in.requestApiKeyAttemptCount)
	}
	deferredConfirmationFailure := false
	if retryAnotherAccountApiKey && in.accountCircuitAttempt != nil {
		deferredConfirmationFailure = in.accountCircuitAttempt.DeferConfirmationTransportFailureForKeyRotation()
	}
	confirmedTransportQuality := in.accountCircuitAttempt != nil && in.accountCircuitAttempt.IsConfirmation() &&
		!localRequestFailure && provenStartedTransportFailure && !neutralFirstByteDeadline && !deferredConfirmationFailure
	{
		terminal := HotQualityTerminal{OutcomeClass: HotQualityOutcomeUnknown, FailureScope: "none", Source: "request_lifecycle"}
		if confirmedTransportQuality {
			if signal.Err() != nil {
				terminal = HotQualityTerminal{OutcomeClass: HotQualityOutcomeClientCancellation, Source: "request_lifecycle"}
			} else {
				failure := circuitTransportFailure(err, lastMessageOf(*in.lastAttempt))
				if failure.kind == TransportFailureKindTimeout {
					terminal = HotQualityTerminal{OutcomeClass: HotQualityOutcomeTimeout, FailureScope: "protocol_model", Source: "gateway_transport"}
				} else {
					terminal = HotQualityTerminal{OutcomeClass: HotQualityOutcomeTransportFailure, FailureScope: "protocol_model", Source: "gateway_transport"}
				}
			}
		}
		c.hotQualityAttempt.RecordTerminal(ctx, terminal)
	}
	if signal.Err() != nil {
		_ = e.Affinity.ForgetAsync(ctx, in.args.SessionAffinityKey, c.account.ID)
	}
	var guidanceErr *gatewaypreauth.GatewayAgentGuidanceResponse
	var localErr *gatewaypreauth.GatewayLocalProtocolResponse
	var validationErr *gatewaypreauth.GatewayRequestValidationError
	var adapterErr UpstreamAdapterContractError
	if errorsAs(err, &guidanceErr) && guidanceErr.IsAccountScoped() {
		auditCapture.CompleteAttempt(c.auditAttemptID, CompleteAttemptInput{
			Success: false, ErrorPhase: "request_validation", ErrorMessage: err.Error(),
		})
		*in.lastAttempt = accountScopedGuidanceAttempt(c.account, guidanceErr)
		*in.agentGuidanceResponse = guidanceErr
		in.failedAccountIDs[c.account.ID] = struct{}{}
		return errorKindHandled, errorStop{kind: errorStopSkipAccount}, nil
	}
	if errorsAs(err, &guidanceErr) || errorsAs(err, &localErr) || errorsAs(err, &validationErr) || errorsAs(err, &adapterErr) {
		auditCapture.CompleteAttempt(c.auditAttemptID, CompleteAttemptInput{
			Success: false, ErrorPhase: "request_validation", ErrorMessage: err.Error(),
		})
		return errorKindRethrow, errorStop{kind: errorStopNone, rethrown: err}, nil
	}
	if neutralFirstByteDeadline && c.normalRouteFirstByteDeadline != nil {
		message := err.Error()
		auditCapture.CompleteAttempt(c.auditAttemptID, CompleteAttemptInput{
			Success: false, ErrorPhase: "upstream_request",
			ErrorCode: "normal_route_first_byte_timeout", ErrorMessage: message,
		})
		if e.Usage != nil {
			_ = e.Usage.RecordFailedUpstreamAttempt(ctx, in.args.Req, *usageContext, c.account, FailedAttemptRecord{
				UpstreamURL:                c.upstreamURL,
				StartedAt:                  c.attemptStartedAt,
				ErrorMessage:               message,
				FailureAttribution:         "gateway_policy",
				InterpretUpstreamSemantics: boolPtr(false),
			})
		}
		_ = e.Affinity.ForgetAsync(ctx, in.args.SessionAffinityKey, c.account.ID)
		if in.accountCircuitAttempt != nil {
			_, _ = in.accountCircuitAttempt.ReportUnknown(ctx)
		}
		if c.normalRouteFirstByteDeadline.LimitingFactor == gatewayrouting.FirstByteLimitingFactorWallPrecommit {
			if c.firstByteDeadlineCoordinator != nil {
				c.firstByteDeadlineCoordinator.Supersede()
			}
			return errorKindRethrow, errorStop{kind: errorStopNone, rethrown: &GatewayRequestWallBudgetExhaustedError{
				WallRemainingMs: in.coordination.GatewayRequestWallBudget.RemainingMs(gatewayupstream.NowMs()),
				BudgetKind:      WallBudgetKindWall,
			}}, nil
		}
		var cutoverReservation *SpeedFirstCutoverReservationView
		if c.firstByteDeadlineCoordinator != nil {
			cutoverReservation = c.firstByteDeadlineCoordinator.TransferForCutover()
		}
		return errorKindRethrow, errorStop{kind: errorStopNone, rethrown: &NormalRouteFirstByteCutoverError{
			AccountID:          c.account.ID,
			AccountName:        c.account.Name,
			Deadline:           *c.normalRouteFirstByteDeadline,
			Message:            message,
			CutoverReservation: cutoverReservation,
		}}, nil
	}
	if signal.Err() != nil && shouldRecordAbortedUpstreamAttempt(err) {
		if _, handleErr := e.FailureDispatcher.HandleUpstreamRequestError(ctx, UpstreamRequestErrorInput{
			Req:                         in.args.Req,
			RequestLane:                 in.requestLane,
			UsageContext:                *usageContext,
			AuditCapture:                auditCapture,
			AuditAttemptID:              c.auditAttemptID,
			Account:                     c.account,
			UpstreamURL:                 c.upstreamURL,
			AttemptStartedAt:            c.attemptStartedAt,
			AttemptIndex:                c.attemptIndex,
			AuditAttemptIndex:           *in.auditAttemptIndex,
			Settings:                    in.settings,
			SessionAffinityKey:          in.args.SessionAffinityKey,
			LastAttempt:                 *in.lastAttempt,
			FailedProxyDispatchKeys:     in.failedProxyDispatchKeys,
			Error:                       err,
			ClientIPAccountAvoidance:    in.args.ClientIPAccountAvoidanceTracker,
			AccountStateMutationEnabled: in.automaticAccountStateMutationAllowed,
		}); handleErr != nil {
			return errorKindHandled, errorStop{}, handleErr
		}
		return errorKindRethrow, errorStop{kind: errorStopNone, rethrown: err}, nil
	}
	if !provenStartedTransportFailure {
		auditCapture.CompleteAttempt(c.auditAttemptID, CompleteAttemptInput{
			Success: false, ErrorPhase: "gateway_local_dispatch",
			ErrorCode: "unproven_upstream_transport_failure", ErrorMessage: err.Error(),
		})
		auditCapture.AddGatewayMetadata("gateway_unproven_upstream_transport_failure", map[string]any{
			"accountId":      c.account.ID,
			"keyFingerprint": derefStringPtr(c.account.SelectedAPIKeyFingerprint),
			"requestLane":    in.requestLane,
			"endpoint":       usageContext.Endpoint,
		})
		// 原 unsafe URL 的账户临时不可用标记分支（engine.AccountState.
		// MarkTemporaryUnavailableWithCacheInvalidation）已删：生产组合根按
		// 设计不装配该端口（普通请求不写 precheck/运行态，Node 侧本就未
		// 接线），分支受 nil 守卫从未执行。
		if in.accountCircuitAttempt != nil {
			_, _ = in.accountCircuitAttempt.ReportUnknown(ctx)
		}
		return errorKindRethrow, errorStop{kind: errorStopNone, rethrown: err}, nil
	}
	requestErrorResult, handleErr := e.FailureDispatcher.HandleUpstreamRequestError(ctx, UpstreamRequestErrorInput{
		Req:                         in.args.Req,
		RequestLane:                 in.requestLane,
		UsageContext:                *usageContext,
		AuditCapture:                auditCapture,
		AuditAttemptID:              c.auditAttemptID,
		Account:                     c.account,
		UpstreamURL:                 c.upstreamURL,
		AttemptStartedAt:            c.attemptStartedAt,
		AttemptIndex:                c.attemptIndex,
		AuditAttemptIndex:           *in.auditAttemptIndex,
		Settings:                    in.settings,
		SessionAffinityKey:          in.args.SessionAffinityKey,
		LastAttempt:                 *in.lastAttempt,
		FailedProxyDispatchKeys:     in.failedProxyDispatchKeys,
		Error:                       err,
		ClientIPAccountAvoidance:    in.args.ClientIPAccountAvoidanceTracker,
		AccountStateMutationEnabled: in.automaticAccountStateMutationAllowed,
	})
	if handleErr != nil {
		return errorKindHandled, errorStop{}, handleErr
	}
	if in.accountLockTrafficEnabled {
		_ = e.Locks.RecordFailureAsync(ctx, c.account.ID, "upstream_transport_failure", *in.activeAccountLockObservation)
		_ = e.Locks.SettleDeadlineAsync(ctx, c.account.ID, gatewayupstream.NowMs(), *in.activeAccountLockObservation)
	}
	if requestErrorResult.LastAttempt != nil {
		*in.lastAttempt = requestErrorResult.LastAttempt
	}
	// B1 记账（调度慢失败直切与墙钟收尾修复设计 §3）：上游归因传输失败在
	// 结算点记入来源避让 PendingFailures；downstream_closed 分支（上方中止臂）
	// 无上游失败不记。
	rememberUpstreamAttemptFailure(in.args, c.account, c.upstreamURL, "upstream_request", *in.lastAttempt, err.Error())
	if requestErrorResult.Action == FailedResponseActionSkipAccount && !retryAnotherAccountApiKey {
		if *in.lastAttempt != nil {
			failure := circuitTransportFailure(err, (*in.lastAttempt).Message)
			if failure.kind == TransportFailureKindTimeout {
				(*in.lastAttempt).TransportFailureKind = TransportFailureKindTimeout
			} else {
				(*in.lastAttempt).TransportFailureKind = TransportFailureKindConnection
			}
		}
		if in.accountCircuitAttempt != nil && signal.Err() == nil && !deferredConfirmationFailure && !dialPhaseTransportFailure {
			failure := circuitTransportFailure(err, lastMessageOf(*in.lastAttempt))
			_, _ = in.accountCircuitAttempt.ReportTransportFailure(ctx, gatewaycircuit.TransportFailure{Kind: failure.kind, Reason: failure.reason})
		}
		if in.requestLane == "text" && provenStartedTransportFailure && !neutralFirstByteDeadline &&
			!*c.firstByteDeadlineTriggeredRef && halfOpenLeaseUnclaimed(c.loop.halfOpenLease) {
			// 慢失败直切（调度慢失败直切与墙钟收尾修复设计 §2）：与响应失败
			// 预留点同门槛，慢传输失败直接轮换下一候选。
			if elapsedMs := gatewayupstream.NowMs() - c.attemptStartedAt; elapsedMs >= in.settings.SameAccountRetryFastFailMaxMs {
				auditCapture.AddGatewayMetadata("same_account_retry_skipped_slow_failure", map[string]any{
					"accountId":   c.account.ID,
					"elapsedMs":   elapsedMs,
					"thresholdMs": in.settings.SameAccountRetryFastFailMaxMs,
					"retryReason": "upstream_transport_failure",
				})
			} else {
				sameAccountRetryID, retryErr := in.reserveSameAccountRetry(c.dispatchAttemptIdentity, "upstream_transport_failure", c.account.ID)
				if retryErr != nil {
					return errorKindHandled, errorStop{}, retryErr
				}
				if sameAccountRetryID != "" {
					if c.keyModelAttempt != nil {
						_ = c.keyModelAttempt.ReportUnknown(ctx)
					}
					*in.activeSameAccountRetryID = sameAccountRetryID
					return errorKindHandled, errorStop{kind: errorStopRetrySameAccount}, nil
				}
			}
		}
		in.failedAccountIDs[c.account.ID] = struct{}{}
		// B1（复审）：Key-model attempt 必须在本跳过/锁死终态统一结算一次。
		// 锁死门两个 return 此前跳过 ReportUpstreamNotComplete，传输失败锁死
		// 会泄漏前台许可与续租；结算幂等，故前置到锁死门之前覆盖所有出口。
		if c.keyModelAttempt != nil {
			_ = c.keyModelAttempt.ReportUpstreamNotComplete(ctx)
		}
		if in.accountLockTrafficEnabled && e.Locks != nil {
			lockState, stateErr := e.Locks.FindStateAsync(ctx, c.account.ID)
			if stateErr != nil {
				// 锁死门 fail-closed 收窄（M4 复审）：只有本请求已观察到该账户的
				// 锁行时才按锁死有效处理；未观察到锁行按既有跳过出口 fail-open。
				auditCapture.AddGatewayMetadata("account_lock_state_read_failed", map[string]any{
					"accountId": c.account.ID,
					"reason":    "lock_state_read_failed",
					"error":     stateErr.Error(),
				})
				if accountLockObservationPresent(in.activeAccountLockObservation) {
					return errorKindHandled, errorStop{kind: errorStopAccountLocked}, nil
				}
				return errorKindHandled, errorStop{kind: errorStopSkipAccount}, nil
			}
			if lockState != nil && accountLockBlocksCrossAccount(*lockState) {
				return errorKindHandled, errorStop{kind: errorStopAccountLocked}, nil
			}
		}
		return errorKindHandled, errorStop{kind: errorStopSkipAccount}, nil
	}
	if !retryAnotherAccountApiKey {
		in.failedAccountIDs[c.account.ID] = struct{}{}
	}
	if in.accountCircuitAttempt != nil && signal.Err() == nil && !deferredConfirmationFailure && !dialPhaseTransportFailure {
		failure := circuitTransportFailure(err, lastMessageOf(*in.lastAttempt))
		_, _ = in.accountCircuitAttempt.ReportTransportFailure(ctx, gatewaycircuit.TransportFailure{Kind: failure.kind, Reason: failure.reason})
	}
	if retryAnotherAccountApiKey {
		if c.keyModelAttempt != nil {
			_ = c.keyModelAttempt.ReportUpstreamNotComplete(ctx)
		}
		if shouldRetainTransportFailureForRecovery(c.upstreamURL, signal) &&
			c.account.SelectedAPIKeyFingerprint != nil &&
			!c.account.APIKeyRuntimeStateDisabled &&
			len(c.account.APIKeys) > *c.loop.accountApiKeyAttemptCount &&
			*in.requestApiKeyAttemptCount < e.Config.AccountApiKeyRequestAttemptSafetyLimit &&
			e.APIKeyEffects != nil {
			observationEpoch := e.APIKeyEffects.CaptureFailureObservation(c.account)
			*c.loop.pendingApiKeyFailuresRef = append(*c.loop.pendingApiKeyFailuresRef, PendingAccountApiKeyFailure{
				Account:          c.account,
				Status:           "temporary_unavailable",
				ObservationEpoch: observationEpoch,
				ErrorMessage:     err.Error(),
			})
		}
		*c.loop.retryAccountApiKeyRef = true
		return errorKindHandled, errorStop{kind: errorStopRetryKey}, nil
	}
	return errorKindHandled, errorStop{kind: errorStopSkipAccount}, nil
}

func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}
