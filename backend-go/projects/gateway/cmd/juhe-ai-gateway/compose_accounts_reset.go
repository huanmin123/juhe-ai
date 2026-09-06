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
//	DispatchAccountHealthCheck           → REGISTERED NIL: the Go gateway has no
//	                                       health-check dispatcher yet (Node
//	                                       dispatchAccountHealthCheck, internal-api
//	                                       service); the dispatch is logged and skipped
//	AuthorizationQuotaExceeded           → gatewayquota StatsStore.LoadCostsBatch +
//	                                       IsRequestQuotaExceeded over the
//	                                       authorization + team-grant limits
//	                                       (Node loadAuthorizationQuotaExceededByAuthorizationIdAsync)
//	APIKeyPoolAllUnavailable             → accountkeystates.Store.AllUnavailable
//	                                       (Node loadAccountApiKeyRuntimeSummariesByAccountIdsAsync
//	                                       的 allUnavailable 投影；非池账户按部分可用
//	                                       处理，与原 nil port 行为一致)
//
// The bridge is wired through accounts.Store.SetRuntimeResetEffects after the
// chain runtime services compose; with JUHE_AI_GATEWAY_CHAIN_ENABLED off the
// port stays nil and the endpoint keeps its self-contained (degraded) test
// contract.

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
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// newAccountsRuntimeResetBridge builds the RuntimeResetEffects port over the
// composed runtime collaborators. Every collaborator is already non-nil (the
// chain runtime construction fail-fasts otherwise); the accountkeystates store
// fails fast on a missing business handle / runtime secret.
func newAccountsRuntimeResetBridge(composed *composition, settingValue SettingValueFunc, services *chainRuntimeServices, secret string) (accounts.RuntimeResetEffects, error) {
	keyStates, err := accountkeystates.NewStore(accountkeystates.Config{
		DB:       composed.db,
		Postgres: composed.pgDialect,
		Secret:   secret,
		Now:      time.Now,
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
		pg:        composed.pgDialect,
		settings:  settingValue,
		latency:   services.LatencyDegradation,
		guard:     services.AccountAPIKeyGuard,
		stats:     services.QuotaStats,
		keyStates: keyStates,
		now:       time.Now,
	}, nil
}

// accountsRuntimeResetBridge implements accounts.RuntimeResetEffects.
type accountsRuntimeResetBridge struct {
	bus       *inval.Bus
	db        *sql.DB
	pg        bool
	settings  SettingValueFunc
	latency   *gatewayproxyhealth.LatencyDegradationService
	guard     *gatewayaccounteffects.AccountAPIKeyFailureGuard
	stats     *gatewayquota.StatsStore
	keyStates *accountkeystates.Store
	now       func() time.Time

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

// DispatchAccountHealthCheck is a REGISTERED NIL port entry: the Go gateway
// owns no health-check dispatcher yet (Node dispatchAccountHealthCheck,
// internal-api service); the reset continues and the skip is observable in the
// logs. 账户 API Key 运行池域（account_api_key_runtime_states）已由
// accountkeystates 落地；本项剩余缺口仅是健康检查派发器本身。
func (b *accountsRuntimeResetBridge) DispatchAccountHealthCheck(accountID, reason string) {
	slogOnceWarn("accounts.RuntimeResetEffects.DispatchAccountHealthCheck", "健康检查派发未装配，跳过后台复检派发")
	slog.Info("runtime-reset 健康检查派发未装配", "accountId", accountID, "reason", reason)
}

// AuthorizationQuotaExceeded bridges the quota gate: the authorization-scoped
// limits plus the effective team grant, costed through the shared
// gatewayquota StatsStore (Node loadAuthorizationQuotaExceededByAuthorizationIdAsync:
// any exceeded check marks the authorization exceeded).
func (b *accountsRuntimeResetBridge) AuthorizationQuotaExceeded(ctx context.Context, input accounts.AuthorizationQuotaCheckInput) (bool, error) {
	authorizationID := strings.TrimSpace(input.AuthorizationID)
	granteeID := strings.TrimSpace(input.GranteeSystemAccountID)
	if authorizationID == "" || granteeID == "" {
		return false, nil
	}
	location, err := settingsTimezoneProvider{read: b.settings}.StatsTimezone(ctx)
	if err != nil {
		return false, err
	}
	now := b.now()

	type quotaCheck struct {
		input  gatewayquota.CostInput
		limits gatewayquota.RequestQuotaLimits
	}
	var checks []quotaCheck

	appendCheck := func(limits gatewayquota.RequestQuotaLimits, scopeType, scopeID string) {
		hourly, hasHourly := 0, false
		if limits.Hourly != nil {
			hourly, hasHourly = limits.Hourly.Hours, true
		}
		checks = append(checks, quotaCheck{
			limits: limits,
			input: gatewayquota.CostInput{
				SystemAccountID:   granteeID,
				ScopeType:         scopeType,
				ScopeID:           scopeID,
				Now:               now,
				HourlyWindowHours: hourly,
				HasHourlyWindow:   hasHourly,
			},
		})
	}

	// 1. direct authorization limits (resource_authorizations.limits_json).
	authorizationLimits, err := b.authorizationLimitsJSON(ctx, authorizationID)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(authorizationLimits) != "" {
		limits, parseErr := gatewayquota.ParseRequestQuotaLimitsJSON(authorizationLimits)
		if parseErr != nil {
			return false, parseErr
		}
		if gatewayquota.HasEnabledRequestQuotaLimit(limits) {
			appendCheck(limits, gatewayquota.ScopeTypeAccountAuthorization, authorizationID)
		}
	}

	// 2. the effective team grant limits (resource_authorization_grants joined
	// through the authorization's effective source team).
	teamID := strings.TrimSpace(input.EffectiveSourceTeamID)
	if teamID != "" {
		teamLimits, err := b.teamGrantLimitsJSON(ctx, authorizationID, now)
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(teamLimits) != "" {
			limits, parseErr := gatewayquota.ParseRequestQuotaLimitsJSON(teamLimits)
			if parseErr != nil {
				return false, parseErr
			}
			if gatewayquota.HasEnabledRequestQuotaLimit(limits) {
				// The team bucket is keyed per authorized instance
				// (<instanceId>:<teamId>); every instance sharing the
				// authorization participates (Node keys the exceeded map by
				// authorization id, so any exceeded instance marks the whole
				// authorization).
				instanceIDs, err := b.authorizationInstanceIDs(ctx, authorizationID)
				if err != nil {
					return false, err
				}
				for _, instanceID := range instanceIDs {
					appendCheck(limits, gatewayquota.ScopeTypeAccountAuthorizationTeam, instanceID+":"+teamID)
				}
			}
		}
	}

	if len(checks) == 0 {
		return false, nil
	}
	inputs := make([]gatewayquota.CostInput, 0, len(checks))
	for _, check := range checks {
		inputs = append(inputs, check.input)
	}
	costsByKey, err := b.stats.LoadCostsBatch(ctx, inputs, location)
	if err != nil {
		return false, err
	}
	for _, check := range checks {
		costs, ok := costsByKey[gatewayquota.CostKey(check.input, location)]
		if ok && gatewayquota.IsRequestQuotaExceeded(check.limits, costs) {
			return true, nil
		}
	}
	return false, nil
}

// APIKeyPoolAllUnavailable bridges the pool availability projection
// (accountkeystates.Store.AllUnavailable)：非池账户 / 无运行态账户返回 false
// （部分可用），与 Node summaries 缺省投影一致。
func (b *accountsRuntimeResetBridge) APIKeyPoolAllUnavailable(ctx context.Context, accountID string) (bool, error) {
	return b.keyStates.AllUnavailable(ctx, accountID)
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
// pool, SQLite uses the unqualified tables.
func (b *accountsRuntimeResetBridge) table(name string) string {
	if b.pg {
		return "juhe_business." + name
	}
	return name
}

func (b *accountsRuntimeResetBridge) bind(query string) string {
	if !b.pg {
		return query
	}
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

// authorizationLimitsJSON reads resource_authorizations.limits_json.
func (b *accountsRuntimeResetBridge) authorizationLimitsJSON(ctx context.Context, authorizationID string) (string, error) {
	var limits sql.NullString
	err := b.db.QueryRowContext(ctx, b.bind(`SELECT limits_json FROM `+b.table("resource_authorizations")+` WHERE id = ?`), authorizationID).Scan(&limits)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return limits.String, nil
}

// teamGrantLimitsJSON mirrors loadTeamAuthorizationGrantLimitJsonByAuthorizationIdAsync
// for a single authorization: the active team grant over the authorization's
// effective source team.
func (b *accountsRuntimeResetBridge) teamGrantLimitsJSON(ctx context.Context, authorizationID string, now time.Time) (string, error) {
	nowISO := now.UTC().Format("2006-01-02T15:04:05.000") + "Z"
	var limits sql.NullString
	err := b.db.QueryRowContext(ctx, b.bind(`SELECT grant_rows.limits_json
		FROM `+b.table("resource_authorizations")+` ra
		INNER JOIN `+b.table("resource_authorization_grants")+` grant_rows
			ON grant_rows.resource_type = ra.resource_type
			AND grant_rows.resource_id = ra.resource_id
			AND grant_rows.grantee_type = 'team'
			AND grant_rows.grantee_team_id = ra.effective_source_team_id
			AND grant_rows.status = 'active'
			AND (grant_rows.expires_at IS NULL OR grant_rows.expires_at > ?)
		WHERE ra.status = 'active'
			AND (ra.expires_at IS NULL OR ra.expires_at > ?)
			AND ra.effective_source_team_id IS NOT NULL
			AND ra.id = ?
		LIMIT 1`), nowISO, nowISO, authorizationID).Scan(&limits)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return limits.String, nil
}

// authorizationInstanceIDs lists the authorized instance accounts sharing the
// authorization (the team quota bucket key).
func (b *accountsRuntimeResetBridge) authorizationInstanceIDs(ctx context.Context, authorizationID string) ([]string, error) {
	rows, err := b.db.QueryContext(ctx, b.bind(`SELECT id FROM `+b.table("accounts")+`
		WHERE authorization_instance_authorization_id = ?
			AND deleted_at IS NULL`), authorizationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
