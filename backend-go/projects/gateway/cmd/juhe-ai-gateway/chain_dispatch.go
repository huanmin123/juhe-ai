package main

// G20 phase-2 composition-root adapters for the gatewaydispatch candidate
// pipeline collaborators. The ordering/runtime-state ports (latency
// degradation, upstream proxy health, hot quality, client-source avoidance)
// have no Go runtime store yet — Node behaves identically when the
// corresponding runtime feature is absent (passthrough ordering, no
// avoidance), so the adapters implement exactly that semantics and log one
// line on first use. The client-IP avoidance and concurrency adapters wrap
// the existing gatewayclientip services (G13); the dispatch quota adapter
// bridges the G07 authorization quota service onto the dispatch port.

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accountkeystates"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// degradedPortWarns dedupes the once-per-port degradation logs.
var degradedPortWarns sync.Map

// slogOnceWarn logs a port degradation once per process.
func slogOnceWarn(port, effect string) {
	if _, loaded := degradedPortWarns.LoadOrStore(port, struct{}{}); loaded {
		return
	}
	slog.Warn("网关链端口显式降级", "port", port, "effect", effect)
}

// degradedLatency implements gatewaydispatch.LatencyDegradationPort with the
// Node "latency degradation runtime absent" semantics: accounts keep the
// scheduling order.
type degradedLatency struct {
	once sync.Once
}

func (d *degradedLatency) OrderAsync(_ context.Context, accounts []gatewaydispatch.AccountCandidate, _ *gatewaydispatch.LatencyScopeInput, _ *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, _ *gatewaydispatch.ModelPriority) (gatewaydispatch.LatencyDegradationOrder, error) {
	d.once.Do(func() {
		slogOnceWarn("gatewaydispatch.LatencyDegradationPort", "时延降级排序保持不变")
	})
	return gatewaydispatch.LatencyDegradationOrder{Accounts: accounts}, nil
}

// degradedProxyHealth implements gatewaydispatch.ProxyHealthPort with the
// "upstream bucket health runtime absent" semantics.
type degradedProxyHealth struct {
	once sync.Once
}

func (d *degradedProxyHealth) OrderAsync(_ context.Context, accounts []gatewaydispatch.AccountCandidate, _ *gatewaydispatch.ModelPriority) (gatewaydispatch.ProxyHealthOrder, error) {
	d.once.Do(func() {
		slogOnceWarn("gatewaydispatch.ProxyHealthPort", "上游桶健康排序保持不变")
	})
	return gatewaydispatch.ProxyHealthOrder{Accounts: accounts}, nil
}

func (d *degradedProxyHealth) RecordFailureAsync(context.Context, gatewaydispatch.AccountCandidate, string) error {
	return nil
}

// degradedHotQuality implements gatewaydispatch.HotQualityPort with the
// "hot quality runtime absent" semantics: no re-ranking, no exploration
// reservation.
type degradedHotQuality struct {
	once sync.Once
}

func (d *degradedHotQuality) OrderAsync(_ context.Context, input gatewaydispatch.HotQualityOrderInput) (gatewaydispatch.HotQualityOrder, error) {
	d.once.Do(func() {
		slogOnceWarn("gatewaydispatch.HotQualityPort", "热度质量排序保持不变")
	})
	return gatewaydispatch.HotQualityOrder{Accounts: input.Accounts}, nil
}

// degradedClientSourceAvoidance implements
// gatewaydispatch.ClientSourceAvoidancePort (client-profiles
// client-source-avoidance.service.ts is a later slice; absent state means no
// avoidance re-rank).
type degradedClientSourceAvoidance struct {
	once sync.Once
}

func (d *degradedClientSourceAvoidance) OrderAsync(_ context.Context, accounts []gatewaydispatch.AccountCandidate, _ gatewaypreauth.ClientStrategyContext, _ *gatewaydispatch.ModelPriority) (gatewaydispatch.AvoidanceOrder, error) {
	d.once.Do(func() {
		slogOnceWarn("gatewaydispatch.ClientSourceAvoidancePort", "客户端来源规避保持直通")
	})
	return gatewaydispatch.AvoidanceOrder{Accounts: accounts}, nil
}

// chainClientSourceAvoidance adapts the G18 gatewaycodex turn-retry service
// onto the dispatch ClientSourceAvoidancePort (client-profiles
// client-source-avoidance.service.ts orderOpenAIAccountsByClientSourceAvoidanceAsync:
// failure-scoped accounts reorder behind fresh ones inside their dispatch
// priority tiers; the frozen preauth strategy carries the source state key in
// its Opaque G18 context).
type chainClientSourceAvoidance struct {
	turnRetry *gatewaycodex.TurnRetryService
}

func (a *chainClientSourceAvoidance) OrderAsync(ctx context.Context, accounts []gatewaydispatch.AccountCandidate, clientStrategy gatewaypreauth.ClientStrategyContext, modelPriority *gatewaydispatch.ModelPriority) (gatewaydispatch.AvoidanceOrder, error) {
	codexStrategy, _ := clientStrategy.Opaque.(gatewaycodex.OpenAIGatewayClientStrategyContext)
	result, err := a.turnRetry.OrderOpenAIAccountsByClientSourceAvoidanceAsync(ctx, accounts, codexStrategy, modelPriority)
	if err != nil {
		return gatewaydispatch.AvoidanceOrder{}, err
	}
	return gatewaydispatch.AvoidanceOrder{
		Accounts:           result.Accounts,
		Applied:            result.Applied,
		AvoidedAccountIDs:  result.AvoidedAccountIDs,
		BypassedAllAvoided: result.BypassedAllAvoided,
		FailureCount:       result.FailureCount,
		ThresholdReached:   result.ThresholdReached,
	}, nil
}

// chainClientIPAvoidance adapts the G13 gatewayclientip.Avoidance onto the
// dispatch ClientIPAvoidancePort (account identity projection: the avoidance
// state keys on the account id inside the client-IP scope).
type chainClientIPAvoidance struct {
	avoidance *gatewayclientip.Avoidance
}

func newChainClientIPAvoidance(avoidance *gatewayclientip.Avoidance) *chainClientIPAvoidance {
	return &chainClientIPAvoidance{avoidance: avoidance}
}

func (a *chainClientIPAvoidance) OrderAsync(ctx context.Context, accounts []gatewaydispatch.AccountCandidate, scope gatewaydispatch.ClientIPAvoidanceScope, modelPriority *gatewaydispatch.ModelPriority) (gatewaydispatch.AvoidanceOrder, error) {
	if a.avoidance == nil || len(accounts) == 0 {
		return gatewaydispatch.AvoidanceOrder{Accounts: accounts}, nil
	}
	projected := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(accounts))
	for _, account := range accounts {
		projected = append(projected, gatewayruntimecache.OpenAIAccountSecret{ID: account.ID})
	}
	result, err := a.avoidance.OrderAccountsByClientIPAccountAvoidanceAsync(ctx, projected, gatewayclientip.AvoidanceScopeInput{
		SystemAccountID: scope.SystemAccountID,
		APIKeyID:        scope.APIKeyID,
		GroupID:         scope.GroupID,
		ClientIP:        scope.ClientIP,
	}, modelPriority)
	if err != nil {
		return gatewaydispatch.AvoidanceOrder{}, err
	}
	byID := make(map[string]gatewaydispatch.AccountCandidate, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	ordered := make([]gatewaydispatch.AccountCandidate, 0, len(result.Accounts))
	for _, account := range result.Accounts {
		if candidate, ok := byID[account.ID]; ok {
			ordered = append(ordered, candidate)
		}
	}
	return gatewaydispatch.AvoidanceOrder{
		Accounts:           ordered,
		Applied:            result.Applied,
		AvoidedAccountIDs:  result.AvoidedAccountIDs,
		BypassedAllAvoided: result.BypassedAllAvoided,
	}, nil
}

// chainDispatchQuota adapts the G07 authorization quota service onto the
// dispatch AuthorizationQuotaChecker port.
type chainDispatchQuota struct {
	quota *gatewayquota.AuthorizationQuotaService
}

func newChainDispatchQuota(quota *gatewayquota.AuthorizationQuotaService) *chainDispatchQuota {
	return &chainDispatchQuota{quota: quota}
}

func (q *chainDispatchQuota) CheckBatchAsync(ctx context.Context, groupAccess gatewayruntimecache.GroupUsageAccessMetadata, accounts []gatewaydispatch.AccountCandidate) (map[string]gatewaydispatch.QuotaDecision, error) {
	entries := make([]gatewayquota.AccountAuthorizationSummary, 0, len(accounts))
	for _, account := range accounts {
		entries = append(entries, gatewayquota.AccountAuthorizationSummary{
			ID:                               account.ID,
			AccountAuthorizationID:           deref(account.AccountAuthorizationID),
			AccountAuthorizationQuotaLimited: account.AccountAuthorizationQuotaLimited != nil && *account.AccountAuthorizationQuotaLimited,
		})
	}
	decisions, err := q.quota.CheckAuthorizationQuotaBatchAsync(ctx, gatewayquota.GroupAccessMetadata{
		GroupAuthorizationID:           deref(groupAccess.GroupAuthorizationID),
		GroupAuthorizationQuotaLimited: groupAccess.GroupAuthorizationQuotaLimited != nil && *groupAccess.GroupAuthorizationQuotaLimited,
	}, entries)
	if err != nil {
		return nil, err
	}
	out := make(map[string]gatewaydispatch.QuotaDecision, len(decisions))
	for accountID, decision := range decisions {
		out[accountID] = gatewaydispatch.QuotaDecision{Allowed: decision.Allowed}
	}
	return out, nil
}

// chainConcurrencyStore adapts the process-local account concurrency tracker
// onto the dispatch AccountConcurrencyStore port (Node shared/account-concurrency
// memory driver: state stays process-local, acquire/release per lane).
type chainConcurrencyStore struct {
	tracker *gatewayclientip.MemoryAccountConcurrency
}

func newChainConcurrencyStore(tracker *gatewayclientip.MemoryAccountConcurrency) *chainConcurrencyStore {
	return &chainConcurrencyStore{tracker: tracker}
}

func (s *chainConcurrencyStore) LoadCurrentAsync(ctx context.Context, accountIDs []string) (map[string]int, error) {
	return s.tracker.LoadAccountCurrentConcurrencyByID(ctx, accountIDs)
}

func (s *chainConcurrencyStore) LoadCurrentByLaneAsync(ctx context.Context, accountIDs []string, lane string) (map[string]int, error) {
	return s.tracker.LoadAccountCurrentConcurrencyByLane(ctx, accountIDs, lane)
}

// TryAcquireAsync acquires one concurrency slot when the account is below
// its (lane-scoped) limit; the returned release puts the slot back.
func (s *chainConcurrencyStore) TryAcquireAsync(_ context.Context, accountID string, concurrencyLimit int, options gatewaydispatch.AccountConcurrencyAcquireOptions) (gatewaydispatch.ConcurrencySlot, error) {
	current := s.tracker.CurrentAccountConcurrency(accountID, options.Lane)
	laneLimit := concurrencyLimit
	if options.LaneLimit != nil {
		laneLimit = *options.LaneLimit
	}
	slot := gatewaydispatch.ConcurrencySlot{
		Current:     current,
		Limit:       concurrencyLimit,
		Lane:        options.Lane,
		LaneCurrent: current,
		LaneLimit:   laneLimit,
	}
	if concurrencyLimit > 0 && current >= laneLimit {
		return slot, nil
	}
	s.tracker.Acquire(accountID, options.Lane)
	slot.Acquired = true
	slot.Current = current + 1
	slot.LaneCurrent = current + 1
	accountIDCopy := accountID
	laneCopy := options.Lane
	slot.Release = func() { s.tracker.Release(accountIDCopy, laneCopy) }
	return slot, nil
}

// chainRuntimeCachePort adapts the G10 runtime cache onto the dispatch
// RuntimeCachePort. LoadApiKeyTransientStatesForDispatch degrades to an
// empty state set: the api-key rotation transient store is owned by the
// keymodel runtime slice (Redis); until it mounts, rotation falls back to
// the credentials carried on the account secret (TAKEOVER POINT).
type chainRuntimeCachePort struct {
	cache *gatewayruntimecache.Service
	guard *gatewayaccounteffects.AccountAPIKeyFailureGuard
	once  sync.Once
}

func newChainRuntimeCachePort(cache *gatewayruntimecache.Service) *chainRuntimeCachePort {
	return &chainRuntimeCachePort{cache: cache}
}

func (p *chainRuntimeCachePort) ListCachedOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, options gatewaydispatch.CachedAccountsOptions) ([]gatewaydispatch.AccountCandidate, error) {
	return p.cache.ListCachedOpenAIAccountsForGroupAsync(ctx, groupID, systemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{
		RequestedModel:          options.RequestedModel,
		RequestedEndpointFamily: options.RequestedEndpointFamily,
	})
}

func (p *chainRuntimeCachePort) ResolveCachedGroupUsageAccessMetadataAsync(ctx context.Context, groupID, systemAccountID string) (gatewayruntimecache.GroupUsageAccessMetadata, bool, error) {
	meta, err := p.cache.ResolveCachedGroupUsageAccessMetadataAsync(ctx, groupID, systemAccountID)
	if err != nil {
		return gatewayruntimecache.GroupUsageAccessMetadata{}, false, err
	}
	if meta == nil {
		return gatewayruntimecache.GroupUsageAccessMetadata{}, false, nil
	}
	return *meta, true, nil
}

func (p *chainRuntimeCachePort) LoadApiKeyTransientStatesForDispatch(ctx context.Context, accountID string, fingerprints []string) ([]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState, error) {
	// W4-B（BUG-0175，D-111）：瞬态加载走 account-api-key failure guard 的
	// dispatch 读面（Node loadGatewayAccountApiKeyTransientStatesForDispatch）：
	// redis 驱动读 gateway-account-api-key-transient-avoidance 键空间，memory
	// 驱动回落进程内屏蔽表。守卫未装配（组合测试）保持此前的空集降级。
	if p.guard == nil {
		p.once.Do(func() {
			slogOnceWarn("gatewaydispatch.RuntimeCachePort.LoadApiKeyTransientStates", "API Key 轮换暂态为空")
		})
		return nil, nil
	}
	states, err := p.guard.LoadTransientStatesForDispatch(ctx, accountID, fingerprints)
	if err != nil {
		return nil, err
	}
	out := make([]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState, 0, len(states))
	for _, state := range states {
		selection := gatewayruntimecache.AccountAPIKeyRuntimeSelectionState{
			Fingerprint: state.KeyFingerprint,
			// Go 水合层把 status != 'active' 折叠为 Disabled（见
			// gatewaydispatch/apikeyrotation.go selectAccountRuntimeApiKeyEntry）。
			Disabled: state.Status != "" && state.Status != "active",
		}
		if state.TransientGeneration != nil {
			generation := *state.TransientGeneration
			selection.Generation = &generation
		}
		if state.NextProbeAt != nil {
			nextProbeAt := *state.NextProbeAt
			selection.CooldownUntil = &nextProbeAt
		}
		out = append(out, selection)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// W4-B（BUG-0175）D-111：账户 API Key 运行态效果链生产装配
// ---------------------------------------------------------------------------

// chainAccountAPIKeyWriter 把 accountkeystates.Store 桥到
// gatewayaccounteffects.AccountAPIKeyWriter（Node db-service
// record_account_api_key_failure / record_account_api_key_success 操作；Go 去
// 跨进程，同一进程直写业务库）。
type chainAccountAPIKeyWriter struct {
	keyStates *accountkeystates.Store
}

func (w *chainAccountAPIKeyWriter) RecordFailure(ctx context.Context, write gatewayaccounteffects.AccountAPIKeyFailureWrite) (gatewayaccounteffects.APIKeyWriteResult, error) {
	result, err := w.keyStates.RecordFailure(ctx, accountkeystates.FailureInput{
		Account:                 chainKeyStateTargetOf(write.Account),
		Status:                  string(write.Input.Status),
		StatusCode:              int(derefInt64Ptr2(write.Input.StatusCode)),
		ErrorCode:               derefStringValue(write.Input.ErrorCode),
		ErrorMessage:            derefStringValue(write.Input.ErrorMessage),
		TraceID:                 derefStringValue(write.Input.TraceID),
		CooldownUntil:           derefStringValue(write.Input.CooldownUntil),
		QuotaRecoveryMode:       string(write.Input.QuotaRecoveryMode),
		ObservedAt:              write.Input.ObservedAt,
		Expected:                accountkeystates.ExpectedProbeState{AccountConfigRevision: write.Input.ExpectedAccountConfigRevision},
	})
	return gatewayaccounteffects.APIKeyWriteResult{Changed: result.Changed, SkippedReason: nilStringPtr(result.SkippedReason)}, err
}

func (w *chainAccountAPIKeyWriter) RecordSuccess(ctx context.Context, write gatewayaccounteffects.AccountAPIKeySuccessWrite) (gatewayaccounteffects.APIKeyWriteResult, error) {
	result, err := w.keyStates.RecordSuccess(ctx, chainKeyStateTargetOf(write.Account), accountkeystates.SuccessInput{
		ObservedAt: write.ObservedAt,
		Expected: accountkeystates.ExpectedProbeState{
			AccountConfigRevision: write.ExpectedAccountConfigRevision,
		},
	})
	return gatewayaccounteffects.APIKeyWriteResult{Changed: result.Changed, SkippedReason: nilStringPtr(result.SkippedReason)}, err
}

// chainKeyStateTargetOf 复用 chain_error_policy_effects.go accountKeyTargetOf
// 的账户投影（Key 级运行态写目标）。
func chainKeyStateTargetOf(account gatewayruntimecache.OpenAIAccountSecret) accountkeystates.TargetInput {
	return accountKeyTargetOf(account)
}

// chainAPIKeyEffectsPort 把 gatewayaccounteffects.AccountAPIKeyEffects 桥到
// gatewaydispatch.APIKeyEffectsPort（Node
// runtime/account-api-key-effects.service.ts 的 dispatch 消费面）。
type chainAPIKeyEffectsPort struct {
	effects *gatewayaccounteffects.AccountAPIKeyEffects
	guard   *gatewayaccounteffects.AccountAPIKeyFailureGuard
}

func (p chainAPIKeyEffectsPort) CaptureFailureObservation(account gatewaydispatch.AccountCandidate) string {
	epoch := p.guard.CaptureFailureObservation(account)
	if epoch == nil {
		return ""
	}
	return strconv.FormatInt(*epoch, 10)
}

func (p chainAPIKeyEffectsPort) RecordFailure(ctx context.Context, account gatewaydispatch.AccountCandidate, input gatewaydispatch.RecordAPIKeyFailureInput) error {
	// Node 实现从不向请求路径上抛写失败（全部 warn 吞掉），这里同样恒 nil。
	var statusCode *int64
	if input.StatusCode != 0 {
		value := int64(input.StatusCode)
		statusCode = &value
	}
	p.effects.RecordFailure(ctx, account, gatewayaccounteffects.RecordFailureInput{
		Status:           gatewayaccounteffects.AccountApiKeyFailureStatus(input.Status),
		StatusCode:       statusCode,
		ErrorCode:        nilStringFrom(input.ErrorCode),
		ErrorMessage:     nilStringFrom(input.ErrorMessage),
		TraceID:          nilStringFrom(input.TraceID),
		CooldownUntil:    input.CooldownUntil,
		QuotaRecoveryMode: gatewayaccounteffects.QuotaRecoveryMode(chainQuotaRecoveryModeOf(input.MutationContext)),
		TrafficSource:    input.TrafficSource,
		MutationContext:  chainMutationContextOf(input.MutationContext),
		ClientIP:         input.ClientIP,
		APIKeyID:         input.APIKeyID,
		ObservationEpoch: chainObservationEpochPtrOf(input.ObservationEpoch),
		Source:           input.Source,
	})
	return nil
}

func (p chainAPIKeyEffectsPort) RecordSuccess(ctx context.Context, account gatewaydispatch.AccountCandidate, input gatewaydispatch.RecordAPIKeySuccessInput) error {
	p.effects.RecordSuccess(ctx, account, gatewayaccounteffects.RecordSuccessInput{
		Source:          input.Source,
		TrafficSource:   input.TrafficSource,
		MutationContext: chainMutationContextOf(input.MutationContext),
	})
	return nil
}

// chainMutationContextOf 把 dispatch 侧的 opaque mutation context map 归一为
// gatewayaccounteffects 的授权上下文（Node 直接透传对象；跨包投影走 JSON
// 往返，字段名大小写不敏感匹配 authority/trafficSource/probeOutcome/
// quotaRecoveryMode）。
func chainMutationContextOf(raw map[string]any) *gatewayaccounteffects.AccountApiKeyPersistentMutationContext {
	if len(raw) == 0 {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var context gatewayaccounteffects.AccountApiKeyPersistentMutationContext
	if err := json.Unmarshal(encoded, &context); err != nil {
		return nil
	}
	return &context
}

func chainQuotaRecoveryModeOf(raw map[string]any) string {
	context := chainMutationContextOf(raw)
	if context == nil {
		return ""
	}
	return string(context.QuotaRecoveryMode)
}

func chainObservationEpochPtrOf(raw string) *int64 {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return nil
	}
	value, err := strconv.ParseInt(normalized, 10, 64)
	if err != nil {
		return nil
	}
	return &value
}

func nilStringFrom(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func nilStringPtr(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func derefStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func derefInt64Ptr2(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

// ---------------------------------------------------------------------------
// W4-B（BUG-0175）D-132：配置策略账号避让（写侧 + 派发候选过滤装饰器 +
// 响应层副作用面）
// ---------------------------------------------------------------------------

// chainConfiguredPolicyAvoidanceSuppression 是 SuppressionPort 装饰器：先跑
// configured-policy-avoidance 过滤（Node filterGatewayAccountRuntimeSuppressions
// 的第一步 filterConfiguredPolicyAvoidances），幸存者再交给内层本地屏蔽过滤。
type chainConfiguredPolicyAvoidanceSuppression struct {
	inner     gatewaydispatch.SuppressionPort
	avoidance *gatewayaccounteffects.ConfiguredPolicyAvoidanceService
}

// chainAvoidanceAccountsOf 把派发候选投影为避让服务的运行态键载体。
func chainAvoidanceAccountsOf(accounts []gatewaydispatch.AccountCandidate) []gatewayaccounteffects.SuppressibleGatewayAccount {
	out := make([]gatewayaccounteffects.SuppressibleGatewayAccount, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, gatewayaccounteffects.SuppressibleGatewayAccount{
			ID:                     account.ID,
			AccountAccessType:      account.AccountAccessType,
			BindingSystemAccountID: derefStringPtr(account.BindingSystemAccountID),
			BoundGroupID:           derefStringPtr(account.BoundGroupID),
			AccountAuthorizationID: derefStringPtr(account.AccountAuthorizationID),
		})
	}
	return out
}

// filterConfiguredPolicyAvoidances 镜像 Node 同名函数：读避让状态、拆分
// 可见/被避让候选、最早 untilMs 作为 nextRetryAfterMs。
func (s *chainConfiguredPolicyAvoidanceSuppression) filterConfiguredPolicyAvoidances(ctx context.Context, accounts []gatewaydispatch.AccountCandidate) (gatewaydispatch.SuppressionFilterResult, error) {
	runtimeKeys := make([]string, len(accounts))
	projections := chainAvoidanceAccountsOf(accounts)
	for index, account := range accounts {
		key, err := gatewayaccounteffects.GatewayAccountRuntimeKey(projections[index])
		if err != nil {
			key = account.ID
		}
		runtimeKeys[index] = key
	}
	states, err := s.avoidance.LoadConfiguredPolicyAvoidanceStates(ctx, runtimeKeys)
	if err != nil {
		return gatewaydispatch.SuppressionFilterResult{}, err
	}
	now := gatewaydispatch.NowMs()
	visible := make([]gatewaydispatch.AccountCandidate, 0, len(accounts))
	suppressedAccountIDs := make([]string, 0)
	var nextRetryAtMs *int64
	for index, state := range states {
		if state == nil || state.UntilMs <= now {
			if state == nil || state.UntilMs <= now {
				visible = append(visible, accounts[index])
			}
			continue
		}
		suppressedAccountIDs = append(suppressedAccountIDs, accounts[index].ID)
		retryAtMs := state.UntilMs
		if retryAtMs <= now {
			retryAtMs = now + 250
		}
		if nextRetryAtMs == nil || retryAtMs < *nextRetryAtMs {
			value := retryAtMs
			nextRetryAtMs = &value
		}
	}
	allSuppressed := len(visible) == 0 && len(accounts) > 0
	result := gatewaydispatch.SuppressionFilterResult{
		Accounts:                             visible,
		SuppressedCount:                      len(suppressedAccountIDs),
		AllSuppressed:                        allSuppressed,
		SuppressedAccountIDs:                 suppressedAccountIDs,
		ConfiguredPolicySuppressedAccountIDs: suppressedAccountIDs,
	}
	if nextRetryAtMs != nil {
		retryAfter := *nextRetryAtMs - now
		if retryAfter < 0 {
			retryAfter = 0
		}
		result.NextRetryAfterMs = &retryAfter
	}
	return result, nil
}

func (s *chainConfiguredPolicyAvoidanceSuppression) FilterAsync(ctx context.Context, accounts []gatewaydispatch.AccountCandidate, options gatewaydispatch.SuppressionFilterOptions) (gatewaydispatch.SuppressionFilterResult, error) {
	configured, err := s.filterConfiguredPolicyAvoidances(ctx, accounts)
	if err != nil {
		return gatewaydispatch.SuppressionFilterResult{}, err
	}
	if configured.AllSuppressed {
		return configured, nil
	}
	inner, err := s.inner.FilterAsync(ctx, configured.Accounts, options)
	if err != nil {
		return gatewaydispatch.SuppressionFilterResult{}, err
	}
	return chainMergeConfiguredPolicyFilterResult(configured, inner), nil
}

// chainMergeConfiguredPolicyFilterResult 镜像 Node 合并语义：计数/账号列表
// 拼接、allSuppressed 以幸存面判定、最早重试时间。
func chainMergeConfiguredPolicyFilterResult(configured gatewaydispatch.SuppressionFilterResult, inner gatewaydispatch.SuppressionFilterResult) gatewaydispatch.SuppressionFilterResult {
	merged := inner
	merged.SuppressedCount = configured.SuppressedCount + inner.SuppressedCount
	merged.AllSuppressed = len(inner.Accounts) == 0 && configured.SuppressedCount+inner.SuppressedCount > 0
	merged.SuppressedAccountIDs = append(append([]string(nil), configured.SuppressedAccountIDs...), inner.SuppressedAccountIDs...)
	merged.ConfiguredPolicySuppressedAccountIDs = configured.ConfiguredPolicySuppressedAccountIDs
	if configured.NextRetryAfterMs != nil && (inner.NextRetryAfterMs == nil || *configured.NextRetryAfterMs < *inner.NextRetryAfterMs) {
		merged.NextRetryAfterMs = configured.NextRetryAfterMs
	}
	return merged
}

// ResolveLocalSuppressionFilter 镜像 Node resolveLocalSuppressionFilter 前置
// configured-policy 过滤：全部被避让时直接进 503 completeFailure 契约；
// 否则幸存者走内层的恢复等待窗。
func (s *chainConfiguredPolicyAvoidanceSuppression) ResolveLocalSuppressionFilter(ctx context.Context, input gatewaydispatch.LocalSuppressionPreflightInput) (*gatewaydispatch.SuppressionFilterResult, bool, error) {
	configured, err := s.filterConfiguredPolicyAvoidances(ctx, input.Accounts)
	if err != nil {
		return nil, false, err
	}
	if configured.AllSuppressed {
		input.AuditCapture.AddGatewayMetadata("local_account_suppression", map[string]any{
			"suppressedCount":                       configured.SuppressedCount,
			"suppressedAccountIds":                  configured.SuppressedAccountIDs,
			"allSuppressed":                         true,
			"nextRetryAfterMs":                      configured.NextRetryAfterMs,
			"configuredPolicySuppressedAccountIds":  configured.ConfiguredPolicySuppressedAccountIDs,
		})
		failure := gatewaycircuit.LocalSuppressionExhaustedFailureResponse(configured.NextRetryAfterMs)
		if err := input.RouteCoordinator.CompleteFailure(ctx, gatewayrouting.GatewayRouteFinalFailure{
			StatusCode:         failure.StatusCode,
			Message:            failure.Message,
			ErrorType:          failure.ErrorType,
			ErrorCode:          failure.ErrorCode,
			ErrorPhase:         failure.ErrorPhase,
			RetryAfterMs:       failure.RetryAfterMs,
			FailureAttribution: "gateway_capacity",
		}); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	input.Accounts = configured.Accounts
	result, completed, err := s.inner.ResolveLocalSuppressionFilter(ctx, input)
	if err != nil || completed || result == nil {
		return result, completed, err
	}
	merged := chainMergeConfiguredPolicyFilterResult(configured, *result)
	return &merged, completed, nil
}

// chainResponseAccountEffects 实现 gatewayresponse.AccountFailureEffects
// （D-132）：响应检查策略的运行态副作用写侧——avoid_account_ttl /
// runtime_avoidance 写配置策略避让，avoid_upstream_bucket_ttl 写上游桶避让
// （Node applyResponseInspectionPolicyRuntimeSideEffects +
// applyResponseInspectionObservationDecisions）。
type chainResponseAccountEffects struct {
	avoidance  *gatewayaccounteffects.ConfiguredPolicyAvoidanceService
	proxyHealth *gatewayproxyhealth.ProxyHealthService
	cache      *gatewayruntimecache.Service
	affinity   gatewaydispatch.SessionAffinityPort
}

func (e *chainResponseAccountEffects) HandleStreamFailure(account gatewayresponse.AccountView, message string, errorCode string, context gatewayresponse.StreamFailureContext, shouldMutateAccount bool) error {
	// Node handleStreamFailure：流式框架/协议观察是请求内的，共享状态只由
	// 传输电路或显式用户策略授权——gatewayaccounteffects 的移植保持 no-op。
	return nil
}

func (e *chainResponseAccountEffects) ForgetSessionAffinity(sessionAffinityKey string, accountID string) {
	if e.affinity != nil {
		_ = e.affinity.ForgetAsync(context.Background(), sessionAffinityKey, accountID)
	}
}

func (e *chainResponseAccountEffects) DispatchRequestFailureAccountHealthCheck(trafficSource string, accountID string) bool {
	// request-failure 健康检查派发由失败派发链（chain_request_failure_health）
	// 在引擎内完成；响应层的 per-request 去重派发保持缺席语义（与既有基线
	// 一致，探活由 jobs accounthealth 的 direct input 调度兜底）。
	return false
}

func (e *chainResponseAccountEffects) ApplyInspectionPolicySideEffects(decision *gatewayresponse.ResponseInspectionDecision, account gatewayresponse.AccountView, accountStateMutationEnabled bool) error {
	if !accountStateMutationEnabled || decision == nil ||
		decision.Reason != "configured_response_policy" || decision.Action == "dry_run" {
		return nil
	}
	secret, ok := chainAccountSecretOfView(account)
	if !ok {
		return nil
	}
	settings, err := e.cache.ReadCachedGatewaySettings(context.Background())
	if err != nil {
		return err
	}
	reason := "响应检查策略命中：" + firstNonEmptyString(decision.PolicyName, decision.PolicyID, decision.MatchedValue, "未命名策略")
	if decision.AccountState == "runtime_avoidance" || decision.AccountSwitch == "avoid_account_ttl" {
		seconds := settings.DefaultTemporaryUnschedulableMinutes * 60
		if err := e.avoidance.SuppressGatewayAccountLocallyForSeconds(
			context.Background(),
			gatewayaccounteffects.SuppressibleGatewayAccount{
				ID:                     secret.ID,
				AccountAccessType:      secret.AccountAccessType,
				BindingSystemAccountID: derefStringPtr(secret.BindingSystemAccountID),
				BoundGroupID:           derefStringPtr(secret.BoundGroupID),
				AccountAuthorizationID: derefStringPtr(secret.AccountAuthorizationID),
			},
			&seconds,
			reason,
		); err != nil {
			return err
		}
	}
	if decision.AccountSwitch == "avoid_upstream_bucket_ttl" {
		ttlSeconds := settings.DefaultTemporaryUnschedulableMinutes * 60
		if ttlSeconds < 1 {
			ttlSeconds = 1
		}
		if _, err := e.proxyHealth.SuppressGatewayUpstreamBucketForSecondsAsync(
			context.Background(), secret, ttlSeconds, reason, gatewayproxyhealth.FailureRecordOptions{},
		); err != nil {
			return err
		}
	}
	return nil
}

// chainAccountSecretOfView 还原响应层账户视图背后的完整密钥投影（响应层
// 只见 AccountView 窄口；Go 链只挂 OpenAIAccountView 一种实现）。
func chainAccountSecretOfView(account gatewayresponse.AccountView) (gatewayruntimecache.OpenAIAccountSecret, bool) {
	if view, ok := account.(gatewayresponse.OpenAIAccountView); ok {
		return view.Account, true
	}
	return gatewayruntimecache.OpenAIAccountSecret{ID: account.GetID()}, false
}

// ---------------------------------------------------------------------------
// W4-B（BUG-0175）D-114：speed-first 时延降级排序端口 + 决策面
// ---------------------------------------------------------------------------

// chainLatencyDegradationPort 把 gatewayproxyhealth.LatencyDegradationService
// 桥到 dispatch 的 LatencyDegradationPort（Node
// orderGatewayAccountsByNormalRouteLatencyDegradationAsync），并实现
// chainSpeedFirstDecisions 决策面（Node normalRouteSpeedFirstDecisionOperations
// 的 store 函数族）。
type chainLatencyDegradationPort struct {
	service *gatewayproxyhealth.LatencyDegradationService
}

func (p chainLatencyDegradationPort) OrderAsync(ctx context.Context, accounts []gatewaydispatch.AccountCandidate, scope *gatewaydispatch.LatencyScopeInput, config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, modelPriority *gatewaydispatch.ModelPriority) (gatewaydispatch.LatencyDegradationOrder, error) {
	if p.service == nil || scope == nil || config == nil || len(accounts) == 0 {
		return gatewaydispatch.LatencyDegradationOrder{Accounts: accounts}, nil
	}
	typedConfig := chainSpeedFirstRuntimeConfigOf(config)
	if typedConfig == nil {
		return gatewaydispatch.LatencyDegradationOrder{Accounts: accounts}, nil
	}
	result, err := gatewayproxyhealth.OrderGatewayAccountsByNormalRouteLatencyDegradation(
		ctx, p.service, accounts, chainLatencyAccountOf, &gatewayproxyhealth.LatencyDegradationScope{
			SystemAccountID: scope.SystemAccountID,
			RouteStrategyID: scope.RouteStrategyID,
			GroupID:         scope.GroupID,
		}, typedConfig, nil,
	)
	if err != nil {
		return gatewaydispatch.LatencyDegradationOrder{}, err
	}
	return gatewaydispatch.LatencyDegradationOrder{
		Accounts:             result.Accounts,
		Applied:              result.Applied,
		DegradedAccountIDs:   result.DegradedAccountIDs,
		BypassedAllDegraded:  result.BypassedAllDegraded,
	}, nil
}

func (p chainLatencyDegradationPort) IsAccountLatencyDegradedAsync(ctx context.Context, account gatewaydispatch.AccountCandidate, scope *gatewaydispatch.LatencyScopeInput) (bool, error) {
	if p.service == nil || scope == nil {
		return false, nil
	}
	return p.service.IsNormalRouteAccountLatencyDegraded(ctx, chainLatencyAccountOf(account), &gatewayproxyhealth.LatencyDegradationScope{
		SystemAccountID: scope.SystemAccountID,
		RouteStrategyID: scope.RouteStrategyID,
		GroupID:         scope.GroupID,
	})
}

func (p chainLatencyDegradationPort) RecordFirstByteSlowAsync(ctx context.Context, account gatewaydispatch.AccountCandidate, scope *gatewaydispatch.LatencyScopeInput, config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, reason string) (*gatewayproxyhealth.LatencySlowResult, error) {
	if p.service == nil || scope == nil {
		return nil, nil
	}
	return p.service.RecordNormalRouteFirstByteSlow(ctx, chainLatencyAccountOf(account), &gatewayproxyhealth.LatencyDegradationScope{
		SystemAccountID: scope.SystemAccountID,
		RouteStrategyID: scope.RouteStrategyID,
		GroupID:         scope.GroupID,
	}, chainSpeedFirstRuntimeConfigOf(config), reason)
}

func (p chainLatencyDegradationPort) RecordFirstByteSuccessAsync(ctx context.Context, account gatewaydispatch.AccountCandidate, scope *gatewaydispatch.LatencyScopeInput, config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, firstByteMs int64) (*gatewayproxyhealth.LatencySuccessResult, error) {
	if p.service == nil || scope == nil {
		return nil, nil
	}
	return p.service.RecordNormalRouteFirstByteSuccess(ctx, chainLatencyAccountOf(account), &gatewayproxyhealth.LatencyDegradationScope{
		SystemAccountID: scope.SystemAccountID,
		RouteStrategyID: scope.RouteStrategyID,
		GroupID:         scope.GroupID,
	}, chainSpeedFirstRuntimeConfigOf(config), &firstByteMs)
}

// chainSpeedFirstDecisions 镜像 Node normalRouteSpeedFirstDecisionOperations
// 的 store 函数面；组合根在链条侧以接口断言消费（决策闭包）。
type chainSpeedFirstDecisions interface {
	IsAccountLatencyDegradedAsync(ctx context.Context, account gatewaydispatch.AccountCandidate, scope *gatewaydispatch.LatencyScopeInput) (bool, error)
	RecordFirstByteSlowAsync(ctx context.Context, account gatewaydispatch.AccountCandidate, scope *gatewaydispatch.LatencyScopeInput, config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, reason string) (*gatewayproxyhealth.LatencySlowResult, error)
	RecordFirstByteSuccessAsync(ctx context.Context, account gatewaydispatch.AccountCandidate, scope *gatewaydispatch.LatencyScopeInput, config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, firstByteMs int64) (*gatewayproxyhealth.LatencySuccessResult, error)
}

// chainLatencyAccountOf 把派发候选投影为时延降级服务的账户载体。
func chainLatencyAccountOf(account gatewaydispatch.AccountCandidate) gatewayproxyhealth.SuppressibleGatewayAccount {
	return gatewayproxyhealth.SuppressibleGatewayAccount{
		ID:                     account.ID,
		AccountAccessType:      account.AccountAccessType,
		BindingSystemAccountID: derefStringPtr(account.BindingSystemAccountID),
		BoundGroupID:           derefStringPtr(account.BoundGroupID),
		AccountAuthorizationID: derefStringPtr(account.AccountAuthorizationID),
		Name:                   account.Name,
	}
}

// chainSpeedFirstRuntimeConfigOf 解码速度优先运行态配置（preflight 的 opaque
// Raw 载荷 → 时延降级服务的类型化配置）。firstByteDeadlineMs 取 preflight 已
// 解出的指针，六个旋钮读 Raw["speedFirstConfig"]（写侧已按 routestrategies
// 归一化，缺省回落同一组默认值：3/120/3/30/300/2）。deadline 缺失即配置无效，
// 返回 nil（排序直通、观测跳过——Node !normalRouteSpeedFirstConfig 分支）。
func chainSpeedFirstRuntimeConfigOf(config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig) *gatewayproxyhealth.SpeedFirstRuntimeConfig {
	if config == nil || config.FirstByteDeadlineMs == nil || *config.FirstByteDeadlineMs <= 0 {
		return nil
	}
	typed := &gatewayproxyhealth.SpeedFirstRuntimeConfig{
		FirstByteDeadlineMs:           *config.FirstByteDeadlineMs,
		SlowTriggerCount:              3,
		SlowWindowSeconds:             120,
		RecoverySuccessCount:          3,
		ProbeIntervalSeconds:          30,
		DegradedTTLSeconds:            300,
		MaxFirstByteRetriesPerRequest: 2,
	}
	speedFirst, ok := config.Raw["speedFirstConfig"].(map[string]any)
	if !ok {
		return typed
	}
	if value := chainConfigIntOf(speedFirst["slowTriggerCount"]); value > 0 {
		typed.SlowTriggerCount = value
	}
	if value := chainConfigIntOf(speedFirst["slowWindowSeconds"]); value > 0 {
		typed.SlowWindowSeconds = value
	}
	if value := chainConfigIntOf(speedFirst["recoverySuccessCount"]); value > 0 {
		typed.RecoverySuccessCount = value
	}
	if value := chainConfigIntOf(speedFirst["probeIntervalSeconds"]); value > 0 {
		typed.ProbeIntervalSeconds = value
	}
	if value := chainConfigIntOf(speedFirst["degradedTtlSeconds"]); value > 0 {
		typed.DegradedTTLSeconds = value
	}
	if value := chainConfigIntOf(speedFirst["maxFirstByteRetriesPerRequest"]); value > 0 {
		typed.MaxFirstByteRetriesPerRequest = value
	}
	return typed
}

func chainConfigIntOf(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	}
	return 0
}

// ---------------------------------------------------------------------------
// W4-B（BUG-0175）D-132：list-availability 投影脏标记
// ---------------------------------------------------------------------------

// newChainListAvailabilityDirtyMarker 镜像 markAccountListRuntimeProjectionDirty
// （account-side-effects.service.ts:101-118）：postgres 驱动且投影开关任一
// 开启时，把来源账号家族（本账号 + 授权实例）写进
// account_list_availability_dirty（Node markAccountListAvailabilityDirtyFamily
// 的 sourceAccountIds 分支；逐账号 upsert 与 jobs circuitstore markDirtySQL
// 同表同列，jobs 是唯一消费者）。开关全关或非 postgres 返回 nil（Node 的
// 直接 return 分支），写入面缺席。
func newChainListAvailabilityDirtyMarker(composed *composition, getenv func(string) string) gatewayaccounteffects.ListAvailabilityDirtyMarker {
	if composed == nil || composed.db == nil || !composed.pgDialect {
		return nil
	}
	projectionEnabled := envBoolOf(getenv("JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED"))
	projectionReadEnabled := envBoolOf(getenv("JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_READ_ENABLED"))
	if !projectionEnabled && !projectionReadEnabled {
		return nil
	}
	return func(ctx context.Context, sourceAccountID string, reason string, availableAtMs int64) error {
		accountID := strings.TrimSpace(sourceAccountID)
		if accountID == "" || len(accountID) > 256 {
			return nil
		}
		nowMs := time.Now().UnixMilli()
		tx, err := composed.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		// 家族展开：id IN (source) OR authorization_instance_source_account_id
		// IN (source)（repository.ts:665-681）。
		rows, err := tx.QueryContext(ctx, `SELECT id FROM juhe_business.accounts
			WHERE deleted_at IS NULL AND (id = ? OR authorization_instance_source_account_id = ?)
			ORDER BY id ASC`, accountID, accountID)
		if err != nil {
			return err
		}
		var familyIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			familyIDs = append(familyIDs, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, id := range familyIDs {
			if err := chainMarkListAvailabilityDirtyRow(ctx, tx, id, reason, availableAtMs, nowMs); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
}

// chainMarkListAvailabilityDirtyRow 与 jobs circuitstore markDirtySQL 同构
// （同表 account_list_availability_dirty、同列、同 upsert 语义；PG 方言）。
func chainMarkListAvailabilityDirtyRow(ctx context.Context, tx *sql.Tx, accountID, reason string, availableAtMs, nowMs int64) error {
	_, err := tx.ExecContext(ctx, `
  INSERT INTO juhe_business.account_list_availability_dirty (
    account_id, viewer_system_account_id, generation, applied_generation, reason, available_at_ms,
    claim_token, claimed_by, claim_until_ms, attempt_count,
    created_at_ms, updated_at_ms
  ) SELECT accounts.id, accounts.system_account_id, COALESCE((
    SELECT MAX(source_generation)
    FROM juhe_business.account_list_availability_projections
    WHERE account_id = $1
  ), 0) + 1, 0, $2, $3, NULL, NULL, NULL, 0, $4, $5
  FROM juhe_business.accounts accounts
  WHERE accounts.id = $6 AND accounts.deleted_at IS NULL
  ON CONFLICT(account_id) DO UPDATE SET
    viewer_system_account_id = excluded.viewer_system_account_id,
    generation = account_list_availability_dirty.generation + 1,
    reason = excluded.reason,
    available_at_ms = CASE
      WHEN account_list_availability_dirty.available_at_ms < excluded.available_at_ms THEN account_list_availability_dirty.available_at_ms
      ELSE excluded.available_at_ms
    END,
    claim_token = NULL,
    claimed_by = NULL,
    claim_until_ms = NULL,
    updated_at_ms = excluded.updated_at_ms`,
		accountID, reason, availableAtMs, nowMs, nowMs, accountID)
	return err
}

func envBoolOf(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// localSessionAffinity (chain_ports.go) satisfies SessionAffinityPort; the
// per-request ClientIPAccountAvoidance tracker factory rides on the same G13
// avoidance service (chainRuntimeDeps.Avoidance).
var _ = gatewayrouting.GatewayAccountModelPriority{}
