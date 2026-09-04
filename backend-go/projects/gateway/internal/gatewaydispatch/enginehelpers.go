package gatewaydispatch

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Attempt-loop helpers of the dispatch engine (dispatch/upstream-dispatch.ts
// private helpers + the concurrency acquire path).

// attemptStop mirrors the Node `stop`/`break` control of the URL loop.
type attemptStop int

const (
	attemptStopNone attemptStop = iota
	attemptStopAccount
	attemptStopRotation
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

// remainingConcurrencyWaitBudget mirrors the budget carry after each acquire.
func (e *Engine) remainingConcurrencyWaitBudget(current int64) int64 {
	return current
}

// acquireAccountConcurrencyWithShortRetry mirrors
// acquireAccountConcurrencyWithShortRetry.
func (e *Engine) acquireAccountConcurrencyWithShortRetry(
	ctx context.Context,
	signal context.Context,
	accountID string,
	concurrencyLimit int,
	waitBudgetMs int64,
	lane gatewayproto.RequestLane,
	policy *gatewayruntimecache.GroupSchedulingPolicy,
	serverRetryBudget *gatewaypreauth.ServerRetryBudget,
) (ConcurrencySlot, int64, error) {
	remainingWaitBudgetMs := maxInt64(0, waitBudgetMs)
	acquireOptions := e.accountConcurrencyLaneAcquireOptions(concurrencyLimit, lane, policy)
	slot, err := e.Concurrency.TryAcquireAsync(ctx, accountID, concurrencyLimit, acquireOptions)
	if err != nil {
		return slot, 0, err
	}
	waitedMs := int64(0)
	retryCount := int64(0)
	if !slot.Acquired && remainingWaitBudgetMs > 0 {
		serverRetryBudget.BeginNoAvailableWait(nil)
		if budgetRemaining := serverRetryBudget.RemainingMs(nil); budgetRemaining < remainingWaitBudgetMs {
			remainingWaitBudgetMs = budgetRemaining
		}
		for !slot.Acquired && remainingWaitBudgetMs > 0 {
			delayMs := e.nextConcurrencyRetryDelayMs(retryCount+1, remainingWaitBudgetMs)
			currentDelayMs := minInt64(delayMs, remainingWaitBudgetMs)
			if waitErr := waitForDelayMs(signal, currentDelayMs); waitErr != nil {
				serverRetryBudget.PauseNoAvailableWait(nil)
				return slot, waitedMs, &UpstreamRequestAbortedError{Message: "请求已取消"}
			}
			waitedMs += currentDelayMs
			remainingWaitBudgetMs -= currentDelayMs
			retryCount++
			slot, err = e.Concurrency.TryAcquireAsync(ctx, accountID, concurrencyLimit, acquireOptions)
			if err != nil {
				serverRetryBudget.PauseNoAvailableWait(nil)
				return slot, waitedMs, err
			}
		}
		serverRetryBudget.PauseNoAvailableWait(nil)
	}
	return slot, waitedMs, nil
}

func (e *Engine) nextConcurrencyRetryDelayMs(attempt int64, remainingWaitBudgetMs int64) int64 {
	// exponentialRetryPolicy('gateway_account_concurrency_short_wait',
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

func (e *Engine) accountConcurrencyLaneAcquireOptions(concurrencyLimit int, lane gatewayproto.RequestLane, policy *gatewayruntimecache.GroupSchedulingPolicy) AccountConcurrencyAcquireOptions {
	if lane != gatewayproto.LaneImage {
		return AccountConcurrencyAcquireOptions{Lane: "text"}
	}
	laneLimit := gatewayhotqualityEffectiveImageLaneConcurrencyLimit(concurrencyLimit, policy)
	return AccountConcurrencyAcquireOptions{Lane: "image", LaneLimit: &laneLimit}
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

func accountRuntimeSourceId(account AccountCandidate) string {
	if account.CredentialSourceAccountID != nil && *account.CredentialSourceAccountID != "" {
		return *account.CredentialSourceAccountID
	}
	return account.ID
}

// accountConcurrencyLimitMessage mirrors accountConcurrencyLimitMessage.
func accountConcurrencyLimitMessage(slot ConcurrencySlot, waitedMs int64) string {
	suffix := ""
	if waitedMs > 0 {
		suffix = "（短等 " + int64ToString(waitedMs) + "ms 后仍未释放）"
	}
	if slot.Lane == "image" && slot.LaneCurrent >= slot.LaneLimit && slot.Current < slot.Limit {
		return "账户图像通道并发已达到上限 " + int64ToString(int64(slot.LaneCurrent)) + "/" + int64ToString(int64(slot.LaneLimit)) +
			"，已为文本通道保留并发槽" + suffix
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
		ProviderCode:              account.ProviderCode,
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

func accountCapacityLimitAttempt(account AccountCandidate, message string) *UpstreamAttempt {
	return attemptWithIdentity(account, "concurrency:limit", message)
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
// assertGatewayRequestWallBudgetAvailableForAttempt.
func (e *Engine) assertGatewayRequestWallBudgetAvailableForAttempt(
	wallBudget *gatewayrouting.GatewayRequestWallBudget,
	tracker *gatewayrouting.GatewayRequestAttemptTracker,
	auditCapture AuditCapture,
	finalResponseReserveMs int64,
) error {
	handoff, err := wallBudget.HandoffRequired(gatewayrouting.GatewayRequestWallBudgetDecision{
		FinalResponseReserveMs: &finalResponseReserveMs,
	})
	if err != nil {
		return err
	}
	if !handoff {
		return nil
	}
	auditCapture.AddGatewayMetadata("gateway_upstream_attempt_blocked_wall_budget", map[string]any{
		"reason":                 "gateway_request_wall_budget_exhausted",
		"wallRemainingMs":        wallBudget.RemainingMs(NowMs()),
		"finalResponseReserveMs": finalResponseReserveMs,
		"attempts":               tracker.Snapshot().AttemptedAccountRuntimeKeys,
	})
	return &GatewayRequestWallBudgetExhaustedError{WallRemainingMs: wallBudget.RemainingMs(NowMs())}
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
	var adapterErr *OpenAIOAuthCodexAdapterError
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
