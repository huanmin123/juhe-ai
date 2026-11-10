package gatewaydispatch

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// Attempt-loop helpers of the dispatch engine (dispatch/upstream-dispatch.ts
// private helpers + the concurrency acquire path).

// attemptStop mirrors the Node `stop`/`break` control of the URL loop.
type attemptStop int

const (
	attemptStopNone attemptStop = iota
	attemptStopAccount
	attemptStopRotation
	// attemptStopLocked 表示账号处于锁死窗口（ENGAGED 且未到死亡窗口）：
	// 终止本账号的 attempt 循环并让候选循环整体停在锁死账号上，不再尝试
	// 其他候选（设计 §3.4 锁死门）。
	attemptStopLocked
)

// halfOpenLeaseUnclaimed mirrors halfOpenLease?.generation === undefined
// (an absent lease never claims a generation).
func halfOpenLeaseUnclaimed(lease HalfOpenLease) bool {
	if lease == nil {
		return true
	}
	return lease.Generation() == nil
}

func onceFunc(fn func()) func() {
	var once sync.Once
	return func() {
		once.Do(fn)
	}
}

// takeReservedSlot mirrors preAcquiredConcurrency?.takeForAccount(account).
func (e *Engine) takeReservedSlot(handle *SpeedFirstCutoverReservationHandle, account AccountCandidate) *ConcurrencySlot {
	if handle == nil || handle.TakeForAccount == nil {
		return nil
	}
	if slot, ok := handle.TakeForAccount(account); ok {
		return &slot
	}
	return nil
}

// Account capacity failure kinds (AI 账户每分钟请求上限设计 §5.1)：并发耗尽与
// RPM 耗尽同属本地容量不可用，Kind 区分终态与等待语义。
const (
	AccountCapacityFailureKindConcurrency = "concurrency"
	AccountCapacityFailureKindRPM         = "rpm"
)

// accountCapacityFailureKind 归属一次并发占位拒绝（设计 §5.1）：并发（总量与
// lane）仍有余量而 RPM 窗口计数已满 → rpm；否则归并发（两约束同时满时并发
// 优先，等待语义按“槽可能随时释放”处理）。
func accountCapacityFailureKind(slot ConcurrencySlot) string {
	if slot.RPMLimit > 0 && slot.RPMCurrent >= slot.RPMLimit &&
		(slot.Limit <= 0 || slot.Current < slot.Limit) &&
		(slot.LaneLimit <= 0 || slot.LaneCurrent < slot.LaneLimit) {
		return AccountCapacityFailureKindRPM
	}
	return AccountCapacityFailureKindConcurrency
}

// acquireAccountConcurrencyWithShortRetry mirrors
// acquireAccountConcurrencyWithShortRetry（upstream-dispatch.ts:2231-2266）。
// 返回 (slot, waitedMs, remainingWaitBudgetMs, err)：remainingWaitBudgetMs 是
// 扣除本次短等并经 serverRetryBudget 收窄后的请求级预算余额——调用方把它回写
// 共享的 concurrencyRetryWaitBudgetMs（Node :793/:866 的预算 carry，BUG-0247
// 项 2 恢复请求级递减语义；错误路径预算无意义，恒 0）。
// AI 账户每分钟请求上限设计 §5.1 修正：RPM 窗口计数不随并发释放下降，纯 RPM
// 拒绝（并发仍有余量）不做并发短等——首拒直接返回（预算原样保留），短等循环
// 内重取后仍纯 RPM 立即结束（保留已等待时间与预算扣减，随外层候选切换）；
// 并发拒绝与「并发 + RPM 双满」保持既有短等行为。
func (e *Engine) acquireAccountConcurrencyWithShortRetry(
	ctx context.Context,
	signal context.Context,
	accountID string,
	concurrencyLimit int,
	rpmLimit int,
	waitBudgetMs int64,
	lane gatewayproto.RequestLane,
	policy *gatewayruntimecache.GroupSchedulingPolicy,
	serverRetryBudget *gatewaypreauth.ServerRetryBudget,
	fixedPollIntervalMs int64,
) (ConcurrencySlot, int64, int64, error) {
	remainingWaitBudgetMs := maxInt64(0, waitBudgetMs)
	acquireOptions := e.accountConcurrencyLaneAcquireOptions(concurrencyLimit, rpmLimit, lane, policy)
	slot, err := e.Concurrency.TryAcquireAsync(ctx, accountID, concurrencyLimit, acquireOptions)
	if err != nil {
		return slot, 0, 0, err
	}
	waitedMs := int64(0)
	retryCount := int64(0)
	if !slot.Acquired && accountCapacityFailureKind(slot) == AccountCapacityFailureKindRPM {
		if fixedPollIntervalMs <= 0 {
			return slot, waitedMs, remainingWaitBudgetMs, nil
		}
		resetWaitMs := slot.RPMResetAtMs - gatewayupstream.NowMs()
		if slot.RPMResetAtMs <= 0 || resetWaitMs <= 0 || resetWaitMs > remainingWaitBudgetMs {
			return slot, waitedMs, remainingWaitBudgetMs, nil
		}
	}
	if !slot.Acquired && remainingWaitBudgetMs > 0 {
		serverRetryBudget.BeginNoAvailableWait(nil)
		if budgetRemaining := serverRetryBudget.RemainingMs(nil); budgetRemaining < remainingWaitBudgetMs {
			remainingWaitBudgetMs = budgetRemaining
		}
		for !slot.Acquired && remainingWaitBudgetMs > 0 {
			delayMs := e.nextConcurrencyRetryDelayMs(retryCount+1, remainingWaitBudgetMs)
			if fixedPollIntervalMs > 0 {
				// owner 路径固定 200ms 轮询，配合共享 1-2s 预算形成 5-10 次探测。
				delayMs = fixedPollIntervalMs
			}
			currentDelayMs := minInt64(delayMs, remainingWaitBudgetMs)
			if waitErr := waitForDelayMs(signal, currentDelayMs); waitErr != nil {
				serverRetryBudget.PauseNoAvailableWait(nil)
				return slot, waitedMs, 0, &UpstreamRequestAbortedError{Message: "请求已取消"}
			}
			waitedMs += currentDelayMs
			remainingWaitBudgetMs -= currentDelayMs
			retryCount++
			slot, err = e.Concurrency.TryAcquireAsync(ctx, accountID, concurrencyLimit, acquireOptions)
			if err != nil {
				serverRetryBudget.PauseNoAvailableWait(nil)
				return slot, waitedMs, 0, err
			}
			if !slot.Acquired && accountCapacityFailureKind(slot) == AccountCapacityFailureKindRPM {
				if fixedPollIntervalMs <= 0 {
					break
				}
				resetWaitMs := slot.RPMResetAtMs - gatewayupstream.NowMs()
				if slot.RPMResetAtMs <= 0 || resetWaitMs <= 0 || resetWaitMs > remainingWaitBudgetMs {
					break
				}
			}
		}
		serverRetryBudget.PauseNoAvailableWait(nil)
	}
	return slot, waitedMs, remainingWaitBudgetMs, nil
}

// SessionAffinityOwnerWaitBudget 是链面初始化请求级 owner 等待额度的公开
// 入口：链 loop 跨 attempt 复用同一指针，避免每个 attempt 重新获得完整 2s。
func SessionAffinityOwnerWaitBudget(configBudgetMs int64, waitPlanned bool, waitRemainingMs int64) int64 {
	return sessionAffinityOwnerWaitBudgetMs(configBudgetMs, waitPlanned, waitRemainingMs)
}

// sessionAffinityOwnerWaitBudgetMs 计算 owner 本次可用的等待额度：
//   - 配置 <=0 表示禁用 owner 等待；
//   - 名额规划已消耗等待时，用准备期剩余额度收紧；
//   - 其余情况使用配置额度（默认 2000ms）。
func sessionAffinityOwnerWaitBudgetMs(configBudgetMs int64, waitPlanned bool, waitRemainingMs int64) int64 {
	if configBudgetMs <= 0 {
		return 0
	}
	budget := configBudgetMs
	if waitPlanned && waitRemainingMs < budget {
		budget = waitRemainingMs
	}
	if budget < 0 {
		return 0
	}
	return budget
}

// consumeSessionAffinityOwnerWaitBudget 从请求级 owner 等待额度扣除本次实际
// 等待，保证 key 轮换重取/多次获取不会重新获得完整预算。
func consumeSessionAffinityOwnerWaitBudget(budget *int64, waitedMs int64) {
	if budget == nil {
		return
	}
	*budget = maxInt64(0, *budget-waitedMs)
}

// remainingAfterAffinityOwnerWait 把 owner 的独立小预算消耗记回请求级总预算：
// requestBudgetMs 是请求开始时的总预算，waitedMs 是 owner 实际短等时长。结果
// 再受 ServerRetryBudget 的零可派发等待余额封顶。
func remainingAfterAffinityOwnerWait(requestBudgetMs int64, waitedMs int64, serverRetryBudget *gatewaypreauth.ServerRetryBudget) int64 {
	remaining := requestBudgetMs - waitedMs
	if remaining < 0 {
		remaining = 0
	}
	if serverRetryBudget != nil {
		if serverRemaining := serverRetryBudget.RemainingMs(nil); serverRemaining < remaining {
			remaining = serverRemaining
		}
	}
	if remaining < 0 {
		remaining = 0
	}
	return remaining
}

func (e *Engine) nextConcurrencyRetryDelayMs(attempt int64, remainingWaitBudgetMs int64) int64 { // exponentialRetryPolicy('gateway_account_concurrency_short_wait',
	// initialDelayMs, maxDelayMs): delay = min(max, initial * 2^(attempt-1)).
	delay := e.Config.AccountConcurrencyRetryInitialDelayMs
	for i := int64(1); i < attempt; i++ {
		delay *= 2
		if delay >= e.Config.AccountConcurrencyRetryMaxDelayMs {
			delay = e.Config.AccountConcurrencyRetryMaxDelayMs
			break
		}
	}
	delay = minInt64(delay, e.Config.AccountConcurrencyRetryMaxDelayMs)
	return minInt64(delay, remainingWaitBudgetMs)
}

func (e *Engine) accountConcurrencyLaneAcquireOptions(concurrencyLimit int, rpmLimit int, lane gatewayproto.RequestLane, policy *gatewayruntimecache.GroupSchedulingPolicy) AccountConcurrencyAcquireOptions {
	if lane != gatewayproto.LaneImage {
		return AccountConcurrencyAcquireOptions{Lane: "text", RPMLimit: rpmLimit}
	}
	laneLimit := gatewayhotqualityEffectiveImageLaneConcurrencyLimit(concurrencyLimit, policy)
	return AccountConcurrencyAcquireOptions{Lane: "image", LaneLimit: &laneLimit, RPMLimit: rpmLimit}
}

// recordConfirmedSameAccountApiKeyFailures mirrors
// recordConfirmedSameAccountApiKeyFailures.
func (e *Engine) recordConfirmedSameAccountApiKeyFailures(
	ctx context.Context,
	failures []PendingAccountApiKeyFailure,
	successAccount AccountCandidate,
	usageContext *gatewaypreauth.GatewayFailureUsageContext,
) error {
	if len(failures) == 0 || successAccount.SelectedAPIKeyFingerprint == nil || e.APIKeyEffects == nil {
		return nil
	}
	successSourceID := accountRuntimeSourceId(successAccount)
	for _, failure := range failures {
		if accountRuntimeSourceId(failure.Account) != successSourceID {
			continue
		}
		if failure.Account.SelectedAPIKeyFingerprint == nil ||
			*failure.Account.SelectedAPIKeyFingerprint == *successAccount.SelectedAPIKeyFingerprint {
			continue
		}
		if err := e.APIKeyEffects.RecordFailure(ctx, failure.Account, RecordAPIKeyFailureInput{
			Status:           failure.Status,
			StatusCode:       failure.StatusCode,
			ErrorCode:        failure.ErrorCode,
			ErrorMessage:     failure.ErrorMessage,
			MutationContext:  failure.MutationContext,
			ObservationEpoch: failure.ObservationEpoch,
			TraceID:          usageContext.TraceID,
			CooldownUntil:    failure.CooldownUntil,
			TrafficSource:    usageContext.TrafficSource,
			ClientIP:         usageContext.ClientIP,
			APIKeyID:         usageContext.APIKeyID,
			Source:           "same_account_api_key_rotation_confirmed",
		}); err != nil {
			return err
		}
	}
	return nil
}

// recordAccountAPIKeySuccess mirrors recordGatewayAccountApiKeySuccess 的
// 触发面（D-111，BUG-0175）：上游请求成功后对最终选中的 Key 记录成功观察——
// guard 复位 + Redis 瞬态避让清理 + 合并持久成功写（持久写的授权由
// gatewayaccounteffects 的 mutation authority 面裁定：网关普通流量不持持久
// 写权，仅自动探针的 complete_success 授权持久成功）。效果链未装配
// （APIKeyEffects == nil）或无选中 Key 时保持中性跳过。
func (e *Engine) recordAccountAPIKeySuccess(
	ctx context.Context,
	successAccount AccountCandidate,
	usageContext *gatewaypreauth.GatewayFailureUsageContext,
) error {
	if e.APIKeyEffects == nil || successAccount.SelectedAPIKeyFingerprint == nil {
		return nil
	}
	return e.APIKeyEffects.RecordSuccess(ctx, successAccount, RecordAPIKeySuccessInput{
		Source:        "upstream_dispatch_success",
		TrafficSource: usageContext.TrafficSource,
	})
}

func accountRuntimeSourceId(account AccountCandidate) string {
	if account.CredentialSourceAccountID != nil && *account.CredentialSourceAccountID != "" {
		return *account.CredentialSourceAccountID
	}
	return account.ID
}

// accountConcurrencyLimitMessage mirrors accountConcurrencyLimitMessage. RPM
// 拒绝（并发仍有余量、窗口计数已满）按设计 §5.1 输出频次文案并给出窗口重置
// 倒计时；其余保持既有并发文案。
func accountConcurrencyLimitMessage(slot ConcurrencySlot, waitedMs int64) string {
	suffix := ""
	if waitedMs > 0 {
		suffix = "（短等 " + int64ToString(waitedMs) + "ms 后仍未释放）"
	}
	if slot.Lane == "image" && slot.LaneCurrent >= slot.LaneLimit && slot.Current < slot.Limit {
		return "账户图像通道并发已达到上限 " + int64ToString(int64(slot.LaneCurrent)) + "/" + int64ToString(int64(slot.LaneLimit)) +
			"，已为文本通道保留并发槽" + suffix
	}
	if accountCapacityFailureKind(slot) == AccountCapacityFailureKindRPM {
		resetInSeconds := maxInt64(1, (slot.RPMResetAtMs-gatewayupstream.NowMs()+999)/1000)
		// RPM 没有可释放的并发槽：不追加“短等…仍未释放”后缀，避免把频次
		// 耗尽误述为等待并发释放。
		return "账户每分钟请求数已达到上限 " + int64ToString(int64(slot.RPMCurrent)) + "/" + int64ToString(int64(slot.RPMLimit)) +
			"（约 " + int64ToString(resetInSeconds) + " 秒后重置）"
	}
	return "账户并发已达到上限 " + int64ToString(int64(slot.Current)) + "/" + int64ToString(int64(slot.Limit)) + suffix
}

func locallySuppressedAttempt(account AccountCandidate, nextRetryAfterMs *int64) *UpstreamAttempt {
	suffix := ""
	if nextRetryAfterMs != nil {
		seconds := (maxInt64(1, *nextRetryAfterMs) + 999) / 1000
		suffix = "，预计 " + int64ToString(seconds) + " 秒后释放"
	}
	return &UpstreamAttempt{
		AccountID:   account.ID,
		AccountName: account.Name,
		UpstreamURL: "account:locally_suppressed",
		Message:     "账号处于本地短期屏蔽" + suffix,
	}
}

func attemptWithIdentity(account AccountCandidate, upstreamURL, message string) *UpstreamAttempt {
	return &UpstreamAttempt{
		AccountID:                 account.ID,
		AccountName:               account.Name,
		ProviderCode:              account.VendorCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
		UpstreamURL:               upstreamURL,
		Message:                   message,
	}
}

func accountApiKeyPoolUnavailableAttempt(account AccountCandidate) *UpstreamAttempt {
	return attemptWithIdentity(account, "account:api_key_pool_unavailable", "账户 API Key 池暂无可用 Key")
}

func accountApiKeyRetryBudgetExhaustedAttempt(account AccountCandidate, message string) *UpstreamAttempt {
	return attemptWithIdentity(account, "account:api_key_request_retry_budget_exhausted", message)
}

func accountCircuitBlockedAttempt(account AccountCandidate, phase string) *UpstreamAttempt {
	return attemptWithIdentity(account, "account:circuit_blocked", "账户短电路处于 "+phase)
}

// accountCapacityLimitAttempt 构造本地容量拒绝 attempt（设计 §5.1）：并发拒绝
// 落 upstreamURL="concurrency:limit"，RPM 拒绝落 "rpm:limit"。
func accountCapacityLimitAttempt(account AccountCandidate, upstreamURL string, message string) *UpstreamAttempt {
	return attemptWithIdentity(account, upstreamURL, message)
}

// consumeAccountRPM 为同一并发槽内的后续真实上游尝试消费一次 RPM（T161）。
// 首次占位的计数由 TryAcquire 原子完成，调用方用 rpmAttemptCovered 跳过消费；
// 存储未实现消费端口时 fail-closed，避免 RPM 计数静默少计。
func (e *Engine) consumeAccountRPM(ctx context.Context, accountID string, rpmLimit int) (bool, int, int64, error) {
	if rpmLimit <= 0 {
		return true, 0, 0, nil
	}
	consumer, ok := e.Concurrency.(AccountRPMConsumer)
	if !ok {
		return false, 0, 0, errors.New("账户 RPM 消费端口未装配")
	}
	return consumer.ConsumeRPM(ctx, accountID, rpmLimit)
}

// accountCapacityLimitRejection 把一次并发占位拒绝投影为 attempt + 容量失败
// 记录（AI 账户每分钟请求上限设计 §5.1）：UpstreamURL 与 Kind 按归属分流
// （concurrency:limit / rpm:limit），RPM 携带 RetryAfterMs = max(1,
// RPMResetAtMs - now)；命中 RPM 拒绝同步计一次 Prometheus 频次限制计数。
func accountCapacityLimitRejection(account AccountCandidate, slot ConcurrencySlot, waitedMs int64) (*UpstreamAttempt, AccountCapacityLimitFailure) {
	kind := accountCapacityFailureKind(slot)
	message := accountConcurrencyLimitMessage(slot, waitedMs)
	upstreamURL := capacityLimitUpstreamURLConcurrency
	failure := AccountCapacityLimitFailure{account: account, message: message, kind: kind}
	if kind == AccountCapacityFailureKindRPM {
		upstreamURL = capacityLimitUpstreamURLRPM
		failure.retryAfterMs = maxInt64(1, slot.RPMResetAtMs-gatewayupstream.NowMs())
		gatewayusage.RecordGatewayAccountRPMLimitedMetric()
	}
	attempt := accountCapacityLimitAttempt(account, upstreamURL, message)
	attempt.RetryAfterMs = failure.retryAfterMs
	return attempt, failure
}

func accountScopedGuidanceAttempt(account AccountCandidate, guidance *gatewaypreauth.GatewayAgentGuidanceResponse) *UpstreamAttempt {
	return attemptWithIdentity(account, "gateway:agent_guidance", guidance.Message)
}

func requestDeduplicatedAttempt(account AccountCandidate, reason string) *UpstreamAttempt {
	return attemptWithIdentity(account, "account:request_deduplicated", "请求内候选已尝试："+reason)
}

func keyModelUnavailableAttempt(account AccountCandidate, reason string) *UpstreamAttempt {
	return attemptWithIdentity(account, "account:key_model_unavailable", "精确 Key-model 候选暂不可选："+reason)
}

// shouldRetryAnotherAccountApiKey mirrors shouldRetryAnotherAccountApiKey.
func (e *Engine) shouldRetryAnotherAccountApiKey(
	account AccountCandidate,
	keyScopedFailure bool,
	accountApiKeyAttemptCount int,
	requestApiKeyAttemptCount int,
	auditCapture AuditCapture,
) bool {
	if !keyScopedFailure || account.SelectedAPIKeyFingerprint == nil {
		return false
	}
	if account.APIKeyRuntimeStateDisabled {
		return false
	}
	if len(account.APIKeys) <= accountApiKeyAttemptCount {
		auditCapture.AddGatewayMetadata("account_api_key_request_pool_exhausted", map[string]any{
			"accountId":                 account.ID,
			"accountName":               account.Name,
			"accountApiKeyAttemptCount": accountApiKeyAttemptCount,
			"requestApiKeyAttemptCount": requestApiKeyAttemptCount,
			"configuredKeyCount":        len(account.APIKeys),
			"poolExhausted":             true,
		})
		return false
	}
	if !e.accountApiKeyRequestRetryBudgetAvailable(account, accountApiKeyAttemptCount, requestApiKeyAttemptCount, auditCapture) {
		return false
	}
	auditCapture.AddGatewayMetadata("account_api_key_request_failover_scheduled", map[string]any{
		"accountId":                 account.ID,
		"accountName":               account.Name,
		"selectedApiKeyIndex":       derefIntPtr(account.SelectedAPIKeyIndex),
		"accountApiKeyAttemptCount": accountApiKeyAttemptCount,
		"requestApiKeyAttemptCount": requestApiKeyAttemptCount,
		"requestAttemptSafetyLimit": e.Config.AccountApiKeyRequestAttemptSafetyLimit,
	})
	return true
}

// shouldTryAnotherAccountApiKeyForRequest mirrors
// shouldTryAnotherAccountApiKeyForRequest.
func (e *Engine) shouldTryAnotherAccountApiKeyForRequest(
	account AccountCandidate,
	accountApiKeyAttemptCount int,
	requestApiKeyAttemptCount int,
) bool {
	if account.SelectedAPIKeyFingerprint == nil {
		return false
	}
	if len(account.APIKeys) <= accountApiKeyAttemptCount {
		return false
	}
	return requestApiKeyAttemptCount < e.Config.AccountApiKeyRequestAttemptSafetyLimit
}

func (e *Engine) accountApiKeyRequestRetryBudgetAvailable(
	account AccountCandidate,
	accountApiKeyAttemptCount int,
	requestApiKeyAttemptCount int,
	auditCapture AuditCapture,
) bool {
	remainingConfiguredKeyCount := maxInt64(0, int64(len(account.APIKeys)-accountApiKeyAttemptCount))
	if requestApiKeyAttemptCount >= e.Config.AccountApiKeyRequestAttemptSafetyLimit {
		auditCapture.AddGatewayMetadata("account_api_key_request_retry_budget_exhausted", map[string]any{
			"accountId":                   account.ID,
			"accountName":                 account.Name,
			"accountApiKeyAttemptCount":   accountApiKeyAttemptCount,
			"requestApiKeyAttemptCount":   requestApiKeyAttemptCount,
			"remainingConfiguredKeyCount": remainingConfiguredKeyCount,
			"reason":                      "request_safety_limit",
			"requestAttemptSafetyLimit":   e.Config.AccountApiKeyRequestAttemptSafetyLimit,
			"poolExhausted":               false,
		})
		return false
	}
	return true
}

// assertGatewayRequestWallBudgetAvailableForAttempt mirrors
// assertGatewayRequestWallBudgetAvailableForAttempt. minimumMeaningfulAttemptMs
// 是新尝试启动门槛（调度慢失败直切与墙钟收尾修复设计 §4 C1，取
// settings.GatewayAttemptStartMinWindowMs）：剩余预算扣渲染保留后不足以支撑
// 一次有意义尝试时不启动，走既有 wall 终态路径主动收尾；0 = 旧行为。
func (e *Engine) assertGatewayRequestWallBudgetAvailableForAttempt(
	wallBudget *gatewayrouting.GatewayRequestWallBudget,
	tracker *gatewayrouting.GatewayRequestAttemptTracker,
	auditCapture AuditCapture,
	finalResponseReserveMs int64,
	minimumMeaningfulAttemptMs int64,
) error {
	handoff, err := wallBudget.HandoffRequired(gatewayrouting.GatewayRequestWallBudgetDecision{
		FinalResponseReserveMs:     &finalResponseReserveMs,
		MinimumMeaningfulAttemptMs: &minimumMeaningfulAttemptMs,
	})
	if err != nil {
		return err
	}
	if !handoff {
		return nil
	}
	auditCapture.AddGatewayMetadata("gateway_upstream_attempt_blocked_wall_budget", map[string]any{
		"reason":                     "gateway_request_wall_budget_exhausted",
		"wallRemainingMs":            wallBudget.RemainingMs(gatewayupstream.NowMs()),
		"finalResponseReserveMs":     finalResponseReserveMs,
		"minimumMeaningfulAttemptMs": minimumMeaningfulAttemptMs,
		"attempts":                   tracker.Snapshot().AttemptedAccountRuntimeKeys,
	})
	return &GatewayRequestWallBudgetExhaustedError{
		WallRemainingMs:            wallBudget.RemainingMs(gatewayupstream.NowMs()),
		MinimumMeaningfulAttemptMs: minimumMeaningfulAttemptMs,
	}
}

// shouldRetainTransportFailureForRecovery mirrors
// shouldRetainTransportFailureForRecovery.
func shouldRetainTransportFailureForRecovery(upstreamURL string, signal context.Context) bool {
	return (signal == nil || signal.Err() == nil) &&
		(strings.HasPrefix(upstreamURL, "http://") || strings.HasPrefix(upstreamURL, "https://"))
}

// isLocalRequestFailure mirrors the localRequestFailure union check.
func isLocalRequestFailure(err error) bool {
	var guidanceErr *gatewaypreauth.GatewayAgentGuidanceResponse
	var localErr *gatewaypreauth.GatewayLocalProtocolResponse
	var validationErr *gatewaypreauth.GatewayRequestValidationError
	var adapterErr UpstreamAdapterContractError
	return errors.As(err, &guidanceErr) || errors.As(err, &localErr) ||
		errors.As(err, &validationErr) || errors.As(err, &adapterErr)
}

func firstByteTimeoutSourceOf(err error) FirstByteTimeoutSource {
	var timeoutErr *GatewayFirstByteTimeoutError
	if errors.As(err, &timeoutErr) {
		return timeoutErr.Source
	}
	return ""
}

func lastStatusOf(attempt *UpstreamAttempt) int {
	if attempt == nil {
		return 0
	}
	return attempt.Status
}

func lastMessageOf(attempt *UpstreamAttempt) string {
	if attempt == nil {
		return ""
	}
	return attempt.Message
}

func deadlineMsPtr(deadline *gatewayrouting.NormalRouteAttemptFirstByteDeadline) *int64 {
	if deadline == nil {
		return nil
	}
	value := deadline.EffectiveDeadlineMs
	return &value
}

func derefIntPtr(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func boolPtr(value bool) *bool { return &value }
