package main

// accounts RuntimeResetEffects composition bridge — the production assembly
// of the runtime-reset port (internal/accounts/runtime_reset_effects.go),
// following the migration wave's runtime-reset port assembly table:
//
//	ClearAccountRuntimeAvailability      → gatewayaccounteffects runtimekeys
//	                                       (GatewayAccountRuntimeClearTarget.ClearKeys)
//	                                       + the K5 runtime-cache invalidation
//	                                       (Node clearGatewayAccountRuntimeAvailabilityAsync)
//	ClearNormalRouteLatencyDegradation   → gatewayproxyhealth.LatencyDegradationService.
//	                                       ClearNormalRouteLatencyDegradationForAccount
//	RevalidateAccountAPIKeyRuntimePool   → accountkeystates.Store.RevalidatePool
//	                                       (Node revalidateAccountApiKeyRuntimePoolAsync,
//	                                       account_api_key_runtime_states; jobs 侧
//	                                       proberepo 同表同键，两侧读写互通)
//	LoadAPIKeyTransientStates            → gatewayaccounteffects
//	                                       AccountAPIKeyFailureGuard.
//	                                       LoadTransientStatesForDispatch
//	                                       (RedisAccountApiKeyTransientStateStore.LoadMany
//	                                       in the redis driver, process-local otherwise)
//	ClearAPIKeyFailureGuard              → gatewayaccounteffects
//	                                       AccountAPIKeyFailureGuard.ClearFailureGuard
//	ClearAPIKeyTransientFailure          → gatewayaccounteffects
//	                                       AccountAPIKeyFailureGuard.ClearTransientFailure
//	                                       (generation CAS)
//	DispatchAccountHealthCheck           → the request-failure health outbox
//	                                       writer (chain_request_failure_health.go):
//	                                       one account_health_probe_request_outbox
//	                                       row per dispatch, written in-process
//	                                       (jobs J1 Runner drains; Node
//	                                       dispatchAccountHealthCheck,
//	                                       internal-api service); fire-and-forget
//	                                       — a rejected dispatch logs and the
//	                                       reset continues
//	APIKeyPoolAllUnavailable             → accountkeystates.Store.AllUnavailable
//	                                       (Node loadAccountApiKeyRuntimeSummariesByAccountIdsAsync
//	                                       的 allUnavailable 投影；非池账户按部分可用
//	                                       处理，与原 nil port 行为一致)
//
// The bridge is wired through accounts.Store.SetRuntimeResetEffects after the
// chain runtime services compose; the chain is always assembled since the
// 2026-09-21 JUHE_AI_GATEWAY_CHAIN_ENABLED switch removal, so the port is
// never nil in production.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accountkeystates"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// newAccountsRuntimeResetBridge builds the RuntimeResetEffects port over the
// composed runtime collaborators. Every collaborator is already non-nil (the
// chain runtime construction fail-fasts otherwise); the accountkeystates store
// fails fast on a missing business handle / runtime secret. healthDispatch is
// the in-process probe-request outbox writer (an inert nil-DB writer keeps
// the dispatch on its logged skip contract).
func newAccountsRuntimeResetBridge(composed *composition, services *chainRuntimeServices, secret string, healthDispatch *chainProbeRequestOutboxWriter) (*accountsRuntimeResetBridge, error) {
	keyStates, err := accountkeystates.NewStore(accountkeystates.Config{
		DB:     composed.db,
		Secret: secret,
		Now:    time.Now,
		InvalidateRuntimeCache: func(reason string) {
			if composed.Bus != nil {
				composed.Bus.Invalidate(inval.TopicGatewayRuntime, reason)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	return &accountsRuntimeResetBridge{
		bus:       composed.Bus,
		db:        composed.db,
		latency:   services.LatencyDegradation,
		guard:     services.AccountAPIKeyGuard,
		keyStates: keyStates,
		health:    healthDispatch,
	}, nil
}

// accountsRuntimeResetBridge implements accounts.RuntimeResetEffects.
type accountsRuntimeResetBridge struct {
	bus       *inval.Bus
	db        *sql.DB
	latency   *gatewayproxyhealth.LatencyDegradationService
	guard     *gatewayaccounteffects.AccountAPIKeyFailureGuard
	keyStates *accountkeystates.Store
	health    *chainProbeRequestOutboxWriter

	// transientGenerations remembers the opaque Redis CAS generation strings
	// reported by LoadAPIKeyTransientStates. The accounts port models the
	// generation as int64 (HasGeneration gates the tombstone attempt), while
	// the shared Redis contract carries an opaque string; the bridge resolves
	// the authoritative string at clear time, and a missing entry (expired
	// between load and clear) skips the CAS instead of writing a stale tombstone.
	mu                   sync.Mutex
	transientGenerations map[string]string
}

// ClearAccountRuntimeAvailability mirrors clearGatewayAccountRuntimeAvailabilityAsync
// for the keys the runtime derives. Residual: the Go gateway keeps no
// per-account availability overlay yet (the dispatch suppression / proxy-health
// collaborators are explicitly degraded, chain_dispatch.go), so there is no
// per-key state to probe or delete; the reset drops the shared runtime-cache
// projection through the K5 bus (the same channel every management write uses)
// and reports cleared for every derived runtime key. Once the availability
// overlay lands, this adapter is the single place to add the per-key
// probe/generation-clear loop.
func (b *accountsRuntimeResetBridge) ClearAccountRuntimeAvailability(_ context.Context, input accounts.RuntimeAvailabilityClearInput) (accounts.RuntimeAvailabilityClearResult, error) {
	target := gatewayaccounteffects.GatewayAccountRuntimeClearTarget{
		AccountID:                         input.AccountID,
		PreserveConfiguredPolicyAvoidance: input.PreserveConfiguredPolicyAvoidance,
	}
	if input.AuthorizedBinding != nil {
		target.AuthorizedBinding = &gatewayaccounteffects.AuthorizedBinding{
			SystemAccountID:        input.AuthorizedBinding.SystemAccountID,
			GroupID:                input.AuthorizedBinding.GroupID,
			AccountAuthorizationID: input.AuthorizedBinding.AccountAuthorizationID,
		}
	}
	if !input.IncludeBaseAccountKey {
		include := false
		target.IncludeBaseAccountKey = &include
	}
	keys := target.ClearKeys()
	if len(keys) == 0 {
		return accounts.RuntimeAvailabilityClearResult{}, nil
	}
	if b.bus != nil {
		b.bus.Invalidate(inval.TopicGatewayRuntime, "account_runtime_reset:"+input.AccountID)
	}
	slog.Info("已手动清理账号网关运行态避让", "event", "gateway_account_runtime_availability_cleared", "runtimeKeys", keys)
	return accounts.RuntimeAvailabilityClearResult{Cleared: true}, nil
}

// ClearNormalRouteLatencyDegradation bridges the latency degradation clear.
func (b *accountsRuntimeResetBridge) ClearNormalRouteLatencyDegradation(ctx context.Context, systemAccountID, accountID string) (int64, error) {
	if b.latency == nil {
		return 0, nil
	}
	return b.latency.ClearNormalRouteLatencyDegradationForAccount(ctx, gatewayproxyhealth.ClearNormalRouteLatencyDegradationForAccountInput{
		SystemAccountID: systemAccountID,
		AccountID:       accountID,
	})
}

// RevalidateAccountAPIKeyRuntimePool bridges the api key runtime pool
// revalidation (accountkeystates.Store.RevalidatePool,
// revalidateAccountApiKeyRuntimePoolAsync)：eligible/changed/reason 直接投影，
// 参数非法以错误上抛（Node 抛出同步异常）。
func (b *accountsRuntimeResetBridge) RevalidateAccountAPIKeyRuntimePool(ctx context.Context, accountID string, expectedConfigRevision int64) (accounts.AccountAPIKeyRuntimeRevalidation, error) {
	result, err := b.keyStates.RevalidatePool(ctx, accountID, expectedConfigRevision)
	if err != nil {
		return accounts.AccountAPIKeyRuntimeRevalidation{}, err
	}
	return accounts.AccountAPIKeyRuntimeRevalidation{
		Eligible: result.Eligible,
		Changed:  result.Changed,
		Reason:   result.Reason,
	}, nil
}

// LoadAPIKeyTransientStates bridges the guard dispatch projection. The
// generation strings are remembered for the CAS tombstone below; the int64
// carrier on the accounts port stays zero (only HasGeneration crosses).
func (b *accountsRuntimeResetBridge) LoadAPIKeyTransientStates(ctx context.Context, accountID string, keyFingerprints []string) ([]accounts.AccountAPIKeyTransientSelectionState, error) {
	states, err := b.guard.LoadTransientStatesForDispatch(ctx, accountID, keyFingerprints)
	if err != nil {
		return nil, err
	}
	out := make([]accounts.AccountAPIKeyTransientSelectionState, 0, len(states))
	for _, state := range states {
		item := accounts.AccountAPIKeyTransientSelectionState{KeyFingerprint: state.KeyFingerprint}
		if state.TransientGeneration != nil && strings.TrimSpace(*state.TransientGeneration) != "" {
			b.rememberTransientGeneration(accountID, state.KeyFingerprint, strings.TrimSpace(*state.TransientGeneration))
			item.HasGeneration = true
		}
		out = append(out, item)
	}
	return out, nil
}

// ClearAPIKeyFailureGuard bridges the process-local suppression clear.
func (b *accountsRuntimeResetBridge) ClearAPIKeyFailureGuard(accountID, keyFingerprint string, _ *int64) bool {
	return b.guard.ClearFailureGuard(b.guardAccount(accountID, keyFingerprint, ""))
}

// ClearAPIKeyTransientFailure bridges the Redis tombstone CAS. A missing
// remembered generation (record expired between load and clear) reports
// not-cleared instead of attempting a stale CAS.
func (b *accountsRuntimeResetBridge) ClearAPIKeyTransientFailure(ctx context.Context, accountID, keyFingerprint string, transientGeneration *int64) (bool, error) {
	if transientGeneration == nil {
		return false, nil
	}
	generation := b.rememberedTransientGeneration(accountID, keyFingerprint)
	if generation == "" {
		return false, nil
	}
	return b.guard.ClearTransientFailure(ctx, b.guardAccount(accountID, keyFingerprint, generation))
}

// DispatchAccountHealthCheck bridges the reset/activation health-check
// dispatch over the in-process probe-request outbox (chain_request_failure_health.go
// → account_health_probe_request_outbox; jobs J1 Runner drains pending rows).
// Fire-and-forget: a rejected dispatch (inert writer, insert failure) logs a
// warning and the reset continues.
func (b *accountsRuntimeResetBridge) DispatchAccountHealthCheck(accountID, reason string) {
	// Node dispatchAccountHealthCheck starts publication asynchronously and
	// returns to the account write/reset path immediately. Keep that boundary:
	// the outbox insert must not add latency to a committed request.
	go func() {
		defer safego.Recover("juheai.compose_accounts_reset.health_dispatch")
		outcome := b.health.EnqueueProbeRequest(context.Background(), accountID, reason, "", nil)
		if outcome.Outcome == gatewaycodex.HealthDispatchRejected {
			slog.Warn("runtime-reset 健康检查派发未受理",
				"event", "account_health_check_dispatch_rejected",
				"accountId", accountID, "reason", reason, "decisionCode", outcome.DecisionCode)
			return
		}
		slog.Info("runtime-reset 健康检查派发已受理",
			"event", "account_health_check_dispatched",
			"accountId", accountID, "reason", reason, "targetRole", outcome.TargetRole)
	}()
}

// chainKeyModelHealthDispatcher adapts the runtime-reset bridge onto the
// gatewayaccounteffects attempt dispatcher port (GatewayKeyModelAttempt.
// SetDispatcher 的生产接线，chain_wiring_w2c.go chainKeyModelAdmission 在每个
// admitted attempt 出口挂载)。The attempt 的 fence 引用是 key-model 前台状态
// store 的栅栏句柄；Node dispatchAccountHealthCheck(accountId, reason) 无
// fence 参数，outbox 派发也不消费它，因此在本边界丢弃（fire-and-forget 语义
// 由上方 DispatchAccountHealthCheck 保持）。bridge.health（probe-request
// outbox writer）在 compose.go chain 启用块内先于本适配器构造，nil-db writer
// 是 inert（dispatch 显式 input_unavailable），无空指针路径。bridge 字段持有
// RuntimeResetEffects 接口（newAccountsRuntimeResetBridge 的返回形态）。
type chainKeyModelHealthDispatcher struct {
	bridge accounts.RuntimeResetEffects
}

func (d chainKeyModelHealthDispatcher) DispatchAccountHealthCheck(accountID string, reason string, _ *gatewayaccounteffects.KeyModelFenceReference) {
	d.bridge.DispatchAccountHealthCheck(accountID, reason)
}

var _ gatewayaccounteffects.AccountHealthCheckDispatcher = chainKeyModelHealthDispatcher{}

// APIKeyPoolAllUnavailable bridges the pool availability projection
// (accountkeystates.Store.AllUnavailable)：非池账户 / 无运行态账户返回 false
// （部分可用），与 Node summaries 缺省投影一致。
func (b *accountsRuntimeResetBridge) APIKeyPoolAllUnavailable(ctx context.Context, accountID string) (bool, error) {
	return b.keyStates.AllUnavailable(ctx, accountID)
}

// ClearDisabledBackgroundProbeRuntime implements accounts.BackgroundProbeRuntimeCleanup
// over the same runtime collaborators the reset endpoint uses（后台自动探测总
// 开关禁用臂，复审 M1）：circuit 运行态由禁用事务内的 dispatch fence 事件经
// jobs 控制面投影关闭（不在本端口），这里清理其余网关面——latency_degraded
// 速度优先运行态、探针派生 Key 运行态池（按禁用后的新 config_revision 重验
// 证）与网关运行时缓存投影失效。任一面失败即整体报错，由账户侧按 best-effort
// 留痕（禁用已提交，清理可经运行时重置端点重放）。
func (b *accountsRuntimeResetBridge) ClearDisabledBackgroundProbeRuntime(ctx context.Context, input accounts.BackgroundProbeRuntimeCleanupInput) (int64, error) {
	cleared := int64(0)
	count, err := b.ClearNormalRouteLatencyDegradation(ctx, input.SystemAccountID, input.AccountID)
	if err != nil {
		return cleared, err
	}
	cleared += count
	if _, err := b.RevalidateAccountAPIKeyRuntimePool(ctx, input.AccountID, input.ExpectedConfigRevision); err != nil {
		return cleared, err
	}
	if _, err := b.ClearAccountRuntimeAvailability(ctx, accounts.RuntimeAvailabilityClearInput{
		AccountID:             input.AccountID,
		IncludeBaseAccountKey: true,
	}); err != nil {
		return cleared, err
	}
	return cleared, nil
}

// ---- helpers ----

// guardAccount projects (accountID, fingerprint, generation) onto the
// OpenAIAccountSecret shape the guard target resolution consumes
// (accountApiKeyRuntimeTarget).
func (b *accountsRuntimeResetBridge) guardAccount(accountID, keyFingerprint, transientGeneration string) gatewayruntimecache.OpenAIAccountSecret {
	fingerprint := keyFingerprint
	account := gatewayruntimecache.OpenAIAccountSecret{ID: accountID, SelectedAPIKeyFingerprint: &fingerprint}
	if transientGeneration != "" {
		generation := transientGeneration
		account.SelectedAPIKeyTransientGeneration = &generation
	}
	return account
}

func (b *accountsRuntimeResetBridge) rememberTransientGeneration(accountID, keyFingerprint, generation string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.transientGenerations == nil {
		b.transientGenerations = map[string]string{}
	}
	b.transientGenerations[accountID+"\x00"+keyFingerprint] = generation
}

func (b *accountsRuntimeResetBridge) rememberedTransientGeneration(accountID, keyFingerprint string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.transientGenerations[accountID+"\x00"+keyFingerprint]
}

// table qualifies business tables the way the other composition adapters do:
// PostgreSQL reaches juhe_business through schema qualification on the shared
// pool.
func (b *accountsRuntimeResetBridge) table(name string) string {
	return "juhe_business." + name
}

func (b *accountsRuntimeResetBridge) bind(query string) string {
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + fmt.Sprint(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}
