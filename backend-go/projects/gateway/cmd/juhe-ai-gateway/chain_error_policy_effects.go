package main

// 显式账户错误策略的状态写侧窄口与生产桥 —— Node
// applyExplicitAccountErrorPolicyDecision（policy/account-error-policy.service.ts）
// 落到 markAccountCooldown / markAccountDisabledByFailure /
// markAuthorizedAccountBinding{CooldownByContext,DisabledByFailure}
// （storage/account-runtime-mutation.repository.ts）与 keyScoped 分支
// recordGatewayAccountApiKeyFailure（runtime/account-api-key-effects.service.ts）
// 的移植。决策本身见 chain_error_policy.go；本文件只负责「决策 → 持久状态」。
//
// 装配：compose.go 在链条运行服务就绪后构造 bridge（与
// compose_accounts_reset.go 的 RuntimeResetEffects 同点同模式），经
// chainRuntimeDeps.AccountErrorPolicyEffects 注入 chainFailureDispatcher；
// 端口为 nil 时派发器保留决策事实（failureKind / 换 Key / audit 归因），
// 状态变更跳过并在首次使用时记录一条显式降级日志。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accountkeystates"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// ---------------------------------------------------------------------------
// 窄口
// ---------------------------------------------------------------------------

// chainErrorPolicyFailureInput 镜像 applyAccountErrorHandling 失败输入中被
// 写侧消费的投影。
type chainErrorPolicyFailureInput struct {
	StatusCode    int
	HasStatusCode bool
	// BodyText / ErrorMessage 参与归因文案（Node bodyText ?? errorMessage）。
	BodyText     string
	ErrorMessage string
	// UpstreamErrorSummary / UpstreamErrorSummaryResolved 镜像同名字段：
	// resolved 时直接采用摘要，否则由失败体重解析。
	UpstreamErrorSummary         string
	UpstreamErrorSummaryResolved bool
	TraceID                      string
	// AttemptStartedAtMs 镜像 attemptStartedAt（Key 级记录的 observedAt）。
	AttemptStartedAtMs int64
}

// chainAccountErrorPolicyEffects 是失败派发器的状态写侧窄口：显式策略决策
// 的 cooldown/disable 状态变更与 keyScoped 系统 quota 的 Key 级失败记录。
type chainAccountErrorPolicyEffects interface {
	// ApplyAccountErrorPolicyDecision 镜像 applyExplicitAccountErrorPolicyDecision：
	// 返回 changed 与写入后的账户状态（changed=false 表示守卫竞争未写入）。
	ApplyAccountErrorPolicyDecision(ctx context.Context, account gatewaydispatch.AccountCandidate, decision accountErrorPolicyDecision, input chainErrorPolicyFailureInput) (bool, string, error)
	// RecordKeyScopedQuotaFailure 镜像 keyScoped 分支的
	// recordGatewayAccountApiKeyFailure（status rate_limited + quota 恢复码）。
	RecordKeyScopedQuotaFailure(ctx context.Context, account gatewaydispatch.AccountCandidate, decision accountErrorPolicyDecision, input chainErrorPolicyFailureInput) error
}

// explicitAccountErrorPolicyReason 镜像 explicitAccountErrorPolicyReason。
func explicitAccountErrorPolicyReason(account gatewaydispatch.AccountCandidate, input chainErrorPolicyFailureInput, decision accountErrorPolicyDecision) string {
	bodyText := input.BodyText
	if bodyText == "" {
		bodyText = input.ErrorMessage
	}
	summary := input.UpstreamErrorSummary
	if !input.UpstreamErrorSummaryResolved {
		summary = firstNonEmptyString(input.UpstreamErrorSummary, summaryFromFailureBody(input, account))
	}
	var failure string
	if !input.HasStatusCode {
		failure = fmt.Sprintf("上游请求异常：%s", sanitizeDiagnosticText(firstNonEmptyString(input.ErrorMessage, bodyText, "请求失败")))
	} else {
		base := fmt.Sprintf("上游调用失败：HTTP %d", input.StatusCode)
		if summary != "" {
			failure = base + "；" + summary
		} else {
			failure = base
		}
	}
	policyLabel := "账户错误策略"
	if decision.RuleSource == "system" {
		policyLabel = "系统继承错误策略"
	}
	ruleName := decision.RuleName
	if ruleName == "" {
		ruleName = "未命名规则"
	}
	reason := fmt.Sprintf("%s「%s」命中；%s", policyLabel, ruleName, failure)
	if len(reason) > 1000 {
		reason = truncateUTF8(reason, 1000)
	}
	return reason
}

// summaryFromFailureBody 镜像 accountErrorPolicyUpstreamSummary 的兜底：从
// 失败体文本重解析协议错误载荷摘要（未解析摘要时）。当前链只挂 OpenAI 协议
// 族，解析投影与 Node registry 的 openai 驱动一致。
func summaryFromFailureBody(input chainErrorPolicyFailureInput, _ gatewaydispatch.AccountCandidate) string {
	if input.BodyText == "" {
		return ""
	}
	payload := gatewayopenai.ParseErrorPayload(input.BodyText, nil)
	return accountErrorPayloadSummary(payload)
}

// firstNonEmptyString 与 chain_compose.go 重复声明（错误策略接线波与失败
// 派发接线波的并行产物），删除后共用 compose 版本（语义一致：跳过空白串，
// 返回第一个非空值）。

// truncateUTF8 按 rune 截断（Node String.prototype.slice 的近似投影：
// 按码元截断；文案场景 rune 截断即可保证合法 UTF-8）。
func truncateUTF8(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// explicitPolicyFailureCode 镜像 applyExplicitAccountErrorPolicyDecision 的
// failureCode 选择。
func explicitPolicyFailureCode(decision accountErrorPolicyDecision) string {
	if decision.RuleSource == "system" {
		if decision.QuotaRecoveryMode == string(quotaRecoveryModeExplicitReset) {
			return systemQuotaExplicitResetCooldownCode
		}
		return systemQuotaGenericCooldownCode
	}
	return explicitAccountErrorPolicyCooldownCode
}

// authorizedBindingTargetOf 镜像 authorizedAccountBindingRuntimeTarget。
type authorizedBindingTarget struct {
	AccountID             string
	SystemAccountID       string
	GroupID               string
	AccountAuthorizationID string
}

func authorizedBindingTargetOf(account gatewaydispatch.AccountCandidate) *authorizedBindingTarget {
	if account.AccountAccessType != "account_authorized" {
		return nil
	}
	if account.BindingSystemAccountID == nil || account.BoundGroupID == nil || account.AccountAuthorizationID == nil {
		return nil
	}
	if *account.BindingSystemAccountID == "" || *account.BoundGroupID == "" || *account.AccountAuthorizationID == "" {
		return nil
	}
	return &authorizedBindingTarget{
		AccountID:             account.ID,
		SystemAccountID:       *account.BindingSystemAccountID,
		GroupID:               *account.BoundGroupID,
		AccountAuthorizationID: *account.AccountAuthorizationID,
	}
}

// ---------------------------------------------------------------------------
// 生产桥
// ---------------------------------------------------------------------------

// newChainErrorPolicyEffectsBridge 构造状态写侧桥（compose.go 装配点）：
// 业务库句柄 + 方言 + K5 运行态失效总线 + accountkeystates（Key 级记录与
// 池隔离判定）。任一必要协作者缺失时返回错误（组合根 fail-fast）。
func newChainErrorPolicyEffectsBridge(composed *composition, secret string) (chainAccountErrorPolicyEffects, *chainErrorPolicyService, error) {
	if composed.db == nil {
		return nil, nil, fmt.Errorf("账户错误策略桥缺少业务库句柄")
	}
	if strings.TrimSpace(secret) == "" {
		return nil, nil, fmt.Errorf("账户错误策略桥缺少 JUHE_AI_SECRET")
	}
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
		return nil, nil, err
	}
	pool := func(account gatewaydispatch.AccountCandidate) bool {
		credentials := account.Credentials
		if len(account.APIKeys) > 0 {
			merged := make(map[string]any, len(credentials)+1)
			for key, value := range credentials {
				merged[key] = value
			}
			merged["api_keys"] = account.APIKeys
			credentials = merged
		}
		return keyStates.IsAccountAPIKeyPoolIsolationEnabled(account.ProviderCode, account.ProtocolCode, account.ProtocolVersion, account.Type, credentials) &&
			account.SelectedAPIKeyFingerprint != nil
	}
	bridge := &chainErrorPolicyEffectsBridge{
		db:        composed.db,
		pg:        composed.pgDialect,
		bus:       composed.Bus,
		keyStates: keyStates,
		now:       time.Now,
	}
	return bridge, newChainErrorPolicyService(chainErrorPolicyDeps{Now: time.Now, PoolIsolationEnabled: pool}), nil
}

// chainErrorPolicyEffectsBridge 实现 chainAccountErrorPolicyEffects。
type chainErrorPolicyEffectsBridge struct {
	db        *sql.DB
	pg        bool
	bus       *inval.Bus
	keyStates *accountkeystates.Store
	now       func() time.Time
}

// ApplyAccountErrorPolicyDecision 镜像 applyExplicitAccountErrorPolicyDecision。
func (b *chainErrorPolicyEffectsBridge) ApplyAccountErrorPolicyDecision(ctx context.Context, account gatewaydispatch.AccountCandidate, decision accountErrorPolicyDecision, input chainErrorPolicyFailureInput) (bool, string, error) {
	if decision.Action == decisionActionRetryNext {
		return false, account.Status, nil
	}
	// Node 观察围栏的 observedAt 是决策时刻（applyAccountErrorHandlingWithCacheInvalidation
	// 入队时捕获的 new Date()，account-effects.ts:36）；Go 同步桥在同一时点捕获。
	observedAt := b.now()
	guard := runtimeFailureObservationGuardOf(account, observedAt)
	reason := explicitAccountErrorPolicyReason(account, input, decision)
	failureCode := explicitPolicyFailureCode(decision)
	binding := authorizedBindingTargetOf(account)
	if decision.Action == decisionActionDisable {
		changed, err := b.markDisabledByFailure(ctx, account, reason, binding, guard)
		if err != nil {
			return false, "", err
		}
		if changed {
			return true, "error", nil
		}
		return false, account.Status, nil
	}
	changed, err := b.markCooldown(ctx, account, decision, reason, failureCode, input.TraceID, binding, guard)
	if err != nil {
		return false, "", err
	}
	if changed {
		return true, decision.CooldownStatus, nil
	}
	return false, account.Status, nil
}

// RecordKeyScopedQuotaFailure 镜像 handleFailedUpstreamResponse 的 keyScoped
// 分支（failure-dispatch.ts:348-367）：status rate_limited、错误码取 quota
// 恢复模式码、cooldown_until 用决策值、authority/source 固定
// system_quota_policy。
func (b *chainErrorPolicyEffectsBridge) RecordKeyScopedQuotaFailure(ctx context.Context, account gatewaydispatch.AccountCandidate, decision accountErrorPolicyDecision, input chainErrorPolicyFailureInput) error {
	if account.SelectedAPIKeyFingerprint == nil || account.APIKeyRuntimeStateDisabled {
		return nil
	}
	mode := decision.QuotaRecoveryMode
	if mode == "" {
		mode = string(quotaRecoveryModeGeneric)
	}
	observedAt := b.now().UTC().Format(rfc3339MillisUTC)
	if input.AttemptStartedAtMs > 0 {
		observedAt = time.UnixMilli(input.AttemptStartedAtMs).UTC().Format(rfc3339MillisUTC)
	}
	_, err := b.keyStates.RecordFailure(ctx, accountkeystates.FailureInput{
		Account:          accountKeyTargetOf(account),
		Status:           "rate_limited",
		StatusCode:       input.StatusCode,
		ErrorCode:        quotaRecoveryErrorCode(mode),
		ErrorMessage:     input.UpstreamErrorSummary,
		TraceID:          input.TraceID,
		CooldownUntil:    decision.CooldownUntil,
		QuotaRecoveryMode: mode,
		ObservedAt:       observedAt,
		Expected:         accountkeystates.ExpectedProbeState{AccountConfigRevision: account.ConfigRevision},
	})
	return err
}

// accountKeyTargetOf 把候选账户投影为 Key 级运行态写目标。
func accountKeyTargetOf(account gatewaydispatch.AccountCandidate) accountkeystates.TargetInput {
	target := accountkeystates.TargetInput{
		AccountID:                 account.ID,
		SystemAccountID:           account.SystemAccountID,
		OwnerSystemAccountID:      account.AccountOwnerSystemAccountID,
		CredentialSourceAccountID: stringValueOf(account.CredentialSourceAccountID),
		SelectedAPIKeyFingerprint: stringValueOf(account.SelectedAPIKeyFingerprint),
		ProviderCode:              account.ProviderCode,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
		AccountType:               account.Type,
		APIKey:                    account.APIKey,
		APIKeys:                   account.APIKeys,
		Credentials:               account.Credentials,
		RuntimeStateDisabled:      account.APIKeyRuntimeStateDisabled,
	}
	if account.SelectedAPIKeyIndex != nil {
		target.SelectedAPIKeyIndex = *account.SelectedAPIKeyIndex
		target.HasSelectedAPIKeyIndex = true
	}
	return target
}

func stringValueOf(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// ---------------------------------------------------------------------------
// accounts 运行态写侧（account-runtime-mutation.repository.ts 投影）
// ---------------------------------------------------------------------------

// runtimeFailureObservationGuard 镜像 AccountRuntimeFailureObservationGuard
// （repository.ts:955-958）：决策时刻的陈旧观察围栏输入。
type runtimeFailureObservationGuard struct {
	ExpectedDispatchRevision int64
	ObservedAt               string
}

// runtimeFailureObservationGuardOf 镜像 accountRuntimeFailureObservationGuard
// （account-error-policy.service.ts:607-615）：dispatch revision 取决策时刻的
// 候选快照（Gateway candidate 的 dispatchRevision），observedAt 取决策时刻；
// 任一缺失（revision < 1）时不设围栏，updated_at 回落普通 nowISO 写入。
func runtimeFailureObservationGuardOf(account gatewaydispatch.AccountCandidate, observedAt time.Time) *runtimeFailureObservationGuard {
	if account.DispatchRevision == nil || *account.DispatchRevision < 1 {
		return nil
	}
	return &runtimeFailureObservationGuard{
		ExpectedDispatchRevision: *account.DispatchRevision,
		ObservedAt:               observedAt.UTC().Format(rfc3339MillisUTC),
	}
}

// runtimeFailureObservationGuardSQL 镜像
// accountRuntimeFailureObservationGuardSql（repository.ts:2296-2303）：行上的
// dispatch_revision 必须仍等于决策快照，且决策之后没有健康成功（
// last_health_success_at）或其他写者（updated_at）。
func runtimeFailureObservationGuardSQL(guard *runtimeFailureObservationGuard) string {
	if guard == nil {
		return ""
	}
	return `
		  AND dispatch_revision = ?
		  AND (last_health_success_at IS NULL OR last_health_success_at < ?)
		  AND (updated_at IS NULL OR updated_at <= ?)`
}

// runtimeFailureObservationGuardParams 镜像
// accountRuntimeFailureObservationGuardParams（repository.ts:2305-2312，
// revision 下限截断为 1）。
func runtimeFailureObservationGuardParams(guard *runtimeFailureObservationGuard) []any {
	if guard == nil {
		return nil
	}
	revision := guard.ExpectedDispatchRevision
	if revision < 1 {
		revision = 1
	}
	return []any{revision, guard.ObservedAt, guard.ObservedAt}
}

// runtimeFailureUpdatedAtSQL 镜像 accountRuntimeFailureUpdatedAtSql
// （repository.ts:2314-2318）：有围栏时较新值保留，无围栏时普通赋值。
func runtimeFailureUpdatedAtSQL(guard *runtimeFailureObservationGuard) string {
	if guard == nil {
		return "?"
	}
	return "CASE WHEN updated_at IS NULL OR updated_at < ? THEN ? ELSE updated_at END"
}

// runtimeFailureUpdatedAtParams 镜像 accountRuntimeFailureUpdatedAtParams
// （repository.ts:2320-2325）。
func runtimeFailureUpdatedAtParams(guard *runtimeFailureObservationGuard, fallback string) []any {
	if guard == nil {
		return []any{fallback}
	}
	return []any{guard.ObservedAt, guard.ObservedAt}
}

// hardUnavailable 状态判定（Node isHardUnavailableAccountStatus）。
func isHardUnavailableAccountStatus(status string) bool {
	return status == "disabled" || status == "pending_test" || status == "error" || status == "quality_isolated"
}

// accountRuntimeRow 镜像 findAccountSummaryAsync 的写侧守卫投影。
type accountRuntimeRow struct {
	Status          string
	ConfigRevision  int64
	AccountExpiresAt sql.NullString
}

func (b *chainErrorPolicyEffectsBridge) loadAccountRuntimeRow(ctx context.Context, accountID string) (*accountRuntimeRow, error) {
	row := b.db.QueryRowContext(ctx, b.bind(`SELECT status, COALESCE(config_revision, 1), account_expires_at
		FROM `+b.table("accounts")+` WHERE id = ? AND deleted_at IS NULL`), accountID)
	current := &accountRuntimeRow{}
	if err := row.Scan(&current.Status, &current.ConfigRevision, &current.AccountExpiresAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return current, nil
}

// isAccountExpired 镜像 isAccountExpired。
func isAccountExpired(accountExpiresAt sql.NullString, now time.Time) bool {
	if !accountExpiresAt.Valid || strings.TrimSpace(accountExpiresAt.String) == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, accountExpiresAt.String)
	if err != nil {
		return false
	}
	return !parsed.After(now)
}

// markCooldown 镜像 markAccountCooldownAsync / markAuthorizedAccountBindingCooldownByContextAsync
// 的持久语义：硬不可用短路、套餐过期自动停用分支、状态 + config_revision
// 守卫、运行态失败观察围栏（runtimeFailureObservationGuard）、系统 quota
// 通用码的冷却优先级围栏、流失败计数复位、重测代次刷新。
//
// #7 裁决（省略登记，不静默）：Node 在冷却落库事务内追加
// enqueueRuntimeAccountHealthInputAsync（repository.ts:2584/2662，J1 快照
// 输入 outbox + 授权源 fanout）。Go 拓扑中 account_health_jobs_input_outbox
// 只有写方（jobs oauthrefresh fanout / 账户删除清理），全 Go 侧没有消费者；
// J1 输入由 jobs accounthealth 的 PG direct input reader 按冻结业务事实
// （冷却列）自主调度发现。此处再写 outbox 会产出无消费者的死表面，
// 故省略该入队；冷却后快速探活的时延差由 direct input 调度周期吸收。
func (b *chainErrorPolicyEffectsBridge) markCooldown(
	ctx context.Context,
	account gatewaydispatch.AccountCandidate,
	decision accountErrorPolicyDecision,
	reason, failureCode, traceID string,
	binding *authorizedBindingTarget,
	guard *runtimeFailureObservationGuard,
) (bool, error) {
	current, err := b.loadAccountRuntimeRow(ctx, account.ID)
	if err != nil || current == nil {
		return false, err
	}
	if isHardUnavailableAccountStatus(current.Status) {
		return false, nil
	}
	now := b.now()
	nowISO := now.UTC().Format(rfc3339MillisUTC)
	if isAccountExpired(current.AccountExpiresAt, now) {
		// Node 过期分支：直接停用并固定 account_expired 错误码（updated_at
		// 保持普通 nowIso 写入，观察围栏只在 WHERE 生效，对齐
		// repository.ts:2565 的普通赋值）。
		query := `UPDATE ` + b.table("accounts") + `
			SET status = 'disabled',
			    schedulable = 0,
			    cooldown_until = NULL,
			    last_error_code = 'account_expired',
			    last_error_message = ?,
			    last_error_trace_id = NULL,
			    cooldown_retest_failure_count = 0,
			    cooldown_retest_observation_started_at = NULL,
			    cooldown_retest_last_at = NULL,
			    cooldown_retest_last_status_code = NULL,
			    stream_failure_count = 0,
			    stream_failure_window_started_at = NULL,
			    updated_at = ?
			WHERE id = ? AND deleted_at IS NULL AND status = ? AND COALESCE(config_revision, 1) = ?` + b.bindingGuardSQL(binding) + runtimeFailureObservationGuardSQL(guard)
		params := []any{"账户套餐已过期，已自动停用", nowISO, account.ID, current.Status, current.ConfigRevision}
		params = append(params, bindingGuardParams(binding)...)
		params = append(params, runtimeFailureObservationGuardParams(guard)...)
		result, err := b.db.ExecContext(ctx, b.bind(query), params...)
		if err != nil {
			return false, err
		}
		changed := rowCountOf(result) > 0
		if changed {
			b.invalidateRuntime("account_runtime_expired")
		}
		return changed, nil
	}

	cooldownStatus := cooldownStatusTemporaryUnavailable
	if decision.CooldownStatus == cooldownStatusRateLimited {
		cooldownStatus = cooldownStatusRateLimited
	}
	observationStartedAt := nowISO
	var cooldownUntil string
	if cooldownStatus == cooldownStatusTemporaryUnavailable {
		// Node temporaryUnavailableRuntimeState：初始退避 3 秒。
		cooldownUntil = now.Add(3 * time.Second).UTC().Format(rfc3339MillisUTC)
	} else {
		cooldownUntil = decision.CooldownUntil
		if cooldownUntil == "" {
			// Node 兜底链末端：now + (defaultCooldownMinutes ?? 1) 分钟。
			cooldownUntil = now.Add(1 * time.Minute).UTC().Format(rfc3339MillisUTC)
		}
	}
	errorCode := sql.NullString{}
	if trimmed := strings.TrimSpace(failureCode); trimmed != "" {
		if len(trimmed) > 120 {
			trimmed = trimmed[:120]
		}
		errorCode = sql.NullString{String: trimmed, Valid: true}
	}
	errorMessage := sql.NullString{}
	if reason != "" {
		errorMessage = sql.NullString{String: reason, Valid: true}
	}
	traceIDValue := sql.NullString{}
	if strings.TrimSpace(traceID) != "" {
		traceIDValue = sql.NullString{String: traceID, Valid: true}
	}
	generation := "cooldown:" + newUUIDv4()

	// Node systemQuotaCooldownPriorityParams 的参考时间取
	// runtimeFailureGuard?.observedAt ?? cooldownNow（repository.ts:2614-2617）。
	priorityReferenceAt := nowISO
	if guard != nil {
		priorityReferenceAt = guard.ObservedAt
	}
	fencing := b.systemQuotaCooldownPrioritySQL(failureCode, priorityReferenceAt)
	query := `UPDATE ` + b.table("accounts") + `
		SET status = ?,
		    schedulable = 1,
		    cooldown_until = ?,
		    last_error_code = ?,
		    last_error_message = ?,
		    last_error_trace_id = ?,
		    cooldown_retest_failure_count = 0,
		    cooldown_retest_observation_started_at = ?,
		    cooldown_retest_generation = ?,
		    cooldown_retest_last_at = NULL,
		    cooldown_retest_last_status_code = NULL,
		    stream_failure_count = 0,
		    stream_failure_window_started_at = NULL,
		    updated_at = ` + runtimeFailureUpdatedAtSQL(guard) + `
		WHERE id = ?
		  AND deleted_at IS NULL
		  AND status = ?
		  AND COALESCE(config_revision, 1) = ?` + fencing + runtimeFailureObservationGuardSQL(guard) + b.bindingGuardSQL(binding)
	params := []any{
		cooldownStatus, cooldownUntil, errorCode, errorMessage, traceIDValue,
		observationStartedAt, generation,
	}
	params = append(params, runtimeFailureUpdatedAtParams(guard, nowISO)...)
	params = append(params, account.ID, current.Status, current.ConfigRevision)
	params = append(params, systemQuotaCooldownPriorityParams(failureCode, priorityReferenceAt)...)
	params = append(params, runtimeFailureObservationGuardParams(guard)...)
	params = append(params, bindingGuardParams(binding)...)
	result, err := b.db.ExecContext(ctx, b.bind(query), params...)
	if err != nil {
		return false, err
	}
	changed := rowCountOf(result) > 0
	if changed {
		b.invalidateRuntime("account_runtime_cooldown")
	}
	return changed, nil
}

// markDisabledByFailure 镜像 markAccountDisabledByFailureAsync（plain：
// markAccountException 'upstream_failure' 语义：error/disabled 短路 + 状态与
// config_revision 围栏 + 运行态失败观察围栏）与
// markAuthorizedAccountBindingDisabledByFailureAsync（绑定变体：无预读，SQL
// 以 status <> 'disabled' 守卫 + 运行态失败观察围栏）。两者都写
// status='error' + schedulable=0。
func (b *chainErrorPolicyEffectsBridge) markDisabledByFailure(
	ctx context.Context,
	account gatewaydispatch.AccountCandidate,
	reason string,
	binding *authorizedBindingTarget,
	guard *runtimeFailureObservationGuard,
) (bool, error) {
	nowISO := b.now().UTC().Format(rfc3339MillisUTC)
	errorMessage := sql.NullString{}
	if reason != "" {
		errorMessage = sql.NullString{String: reason, Valid: true}
	}
	var query string
	var params []any
	if binding != nil {
		query = `UPDATE ` + b.table("accounts") + `
			SET status = 'error',
			    schedulable = 0,
			    cooldown_until = NULL,
			    last_error_code = 'upstream_failure',
			    last_error_message = ?,
			    last_error_trace_id = NULL,
			    cooldown_retest_failure_count = 0,
			    cooldown_retest_observation_started_at = NULL,
			    cooldown_retest_last_at = NULL,
			    cooldown_retest_last_status_code = NULL,
			    stream_failure_count = 0,
			    stream_failure_window_started_at = NULL,
			    updated_at = ` + runtimeFailureUpdatedAtSQL(guard) + `
			WHERE id = ?
			  AND system_account_id = ?
			  AND authorization_instance_authorization_id = ?
			  AND deleted_at IS NULL
			  AND status <> 'disabled'` + runtimeFailureObservationGuardSQL(guard) + b.bindingExistsSQL(binding)
		params = append(params, errorMessage)
		params = append(params, runtimeFailureUpdatedAtParams(guard, nowISO)...)
		params = append(params,
			binding.AccountID, binding.SystemAccountID, binding.AccountAuthorizationID)
		params = append(params, runtimeFailureObservationGuardParams(guard)...)
		params = append(params, bindingExistsParams(binding)...)
	} else {
		current, err := b.loadAccountRuntimeRow(ctx, account.ID)
		if err != nil || current == nil {
			return false, err
		}
		// Node markAccountExceptionAsync：error 短路，preserveDisabled 使
		// disabled 也短路。
		if current.Status == "error" || current.Status == "disabled" {
			return false, nil
		}
		query = `UPDATE ` + b.table("accounts") + `
			SET status = 'error',
			    schedulable = 0,
			    cooldown_until = NULL,
			    last_error_code = 'upstream_failure',
			    last_error_message = ?,
			    last_error_trace_id = NULL,
			    cooldown_retest_failure_count = 0,
			    cooldown_retest_observation_started_at = NULL,
			    cooldown_retest_last_at = NULL,
			    cooldown_retest_last_status_code = NULL,
			    stream_failure_count = 0,
			    stream_failure_window_started_at = NULL,
			    updated_at = ` + runtimeFailureUpdatedAtSQL(guard) + `
			WHERE id = ?
			  AND deleted_at IS NULL
			  AND status = ?
			  AND COALESCE(config_revision, 1) = ?` + runtimeFailureObservationGuardSQL(guard)
		params = append(params, errorMessage)
		params = append(params, runtimeFailureUpdatedAtParams(guard, nowISO)...)
		params = append(params, account.ID, current.Status, current.ConfigRevision)
		params = append(params, runtimeFailureObservationGuardParams(guard)...)
	}
	result, err := b.db.ExecContext(ctx, b.bind(query), params...)
	if err != nil {
		return false, err
	}
	changed := rowCountOf(result) > 0
	if changed {
		b.invalidateRuntime("account_exception")
	}
	return changed, nil
}

// systemQuotaCooldownPrioritySQL 镜像 systemQuotaCooldownPrioritySql：系统
// quota 通用码不得覆盖已存在的显式重置 / 账户策略 / 遗留策略前缀冷却。
func (b *chainErrorPolicyEffectsBridge) systemQuotaCooldownPrioritySQL(failureCode, referenceAt string) string {
	if failureCode != systemQuotaGenericCooldownCode {
		return ""
	}
	futureCooldown := `julianday(cooldown_until) > julianday(?)`
	if b.pg {
		futureCooldown = `cooldown_until::timestamptz > ?::timestamptz`
	}
	return `
		  AND NOT (
		    (
		      last_error_code IN (?, ?, ?)
		      OR (
		        last_error_code IS NULL
		        AND COALESCE(last_error_message, '') LIKE ?
		      )
		    )
		    AND cooldown_until IS NOT NULL
		    AND ` + futureCooldown + `
		  )`
}

// systemQuotaCooldownPriorityParams 镜像 systemQuotaCooldownPriorityParams。
func systemQuotaCooldownPriorityParams(failureCode, referenceAt string) []any {
	if failureCode != systemQuotaGenericCooldownCode {
		return nil
	}
	return []any{
		systemQuotaExplicitResetCooldownCode,
		systemQuotaGenericCooldownCode,
		explicitAccountErrorPolicyCooldownCode,
		legacyExplicitPolicyMessagePrefix + "%",
		referenceAt,
	}
}

// bindingGuardSQL / bindingExistsSQL：授权绑定变体的额外守卫（Node
// 授权绑定 UPDATE 的 WHERE 前缀与 EXISTS group_accounts 子查询）。
func (b *chainErrorPolicyEffectsBridge) bindingGuardSQL(binding *authorizedBindingTarget) string {
	if binding == nil {
		return ""
	}
	return `
		  AND system_account_id = ?
		  AND authorization_instance_authorization_id = ?` + b.bindingExistsSQL(binding)
}

func (b *chainErrorPolicyEffectsBridge) bindingExistsSQL(binding *authorizedBindingTarget) string {
	if binding == nil {
		return ""
	}
	return `
		  AND EXISTS (
		    SELECT 1
		    FROM ` + b.table("group_accounts") + ` group_accounts
		    WHERE group_accounts.account_id = accounts.id
		      AND group_accounts.system_account_id = ?
		      AND group_accounts.group_id = ?
		      AND group_accounts.enabled = 1
		      AND group_accounts.account_authorization_id = ?
		  )`
}

func bindingGuardParams(binding *authorizedBindingTarget) []any {
	if binding == nil {
		return nil
	}
	return append([]any{binding.SystemAccountID, binding.AccountAuthorizationID}, bindingExistsParams(binding)...)
}

func bindingExistsParams(binding *authorizedBindingTarget) []any {
	if binding == nil {
		return nil
	}
	return []any{binding.SystemAccountID, binding.GroupID, binding.AccountAuthorizationID}
}

// invalidateRuntime 镜像 invalidateGatewayRuntimeAfterBusinessWrite：K5
// 总线丢弃共享运行态投影（派发侧下次读取重建）。
func (b *chainErrorPolicyEffectsBridge) invalidateRuntime(reason string) {
	if b.bus != nil {
		b.bus.Invalidate(inval.TopicGatewayRuntime, reason)
	}
}

func (b *chainErrorPolicyEffectsBridge) table(name string) string {
	if b.pg {
		return "juhe_business." + name
	}
	return name
}

func (b *chainErrorPolicyEffectsBridge) bind(query string) string {
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

func rowCountOf(result sql.Result) int64 {
	if result == nil {
		return 0
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0
	}
	return affected
}

// newUUIDv4 生成 UUIDv4 形状的随机串（Node randomUUID 的最小投影）。
func newUUIDv4() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("cooldown-%d", time.Now().UnixNano())
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

// degradedChainErrorPolicyEffects 是端口缺位的显式降级实现：决策事实保留，
// 状态变更跳过（首次使用记录一条降级日志，可观察、不静默）。
type degradedChainErrorPolicyEffects struct {
	once sync.Once
}

func (d *degradedChainErrorPolicyEffects) degrade() {
	d.once.Do(func() {
		slog.Warn("网关链端口显式降级", "port", "chainAccountErrorPolicyEffects", "effect", "错误策略状态变更跳过（cooldown/disable 不落库）")
	})
}

func (d *degradedChainErrorPolicyEffects) ApplyAccountErrorPolicyDecision(_ context.Context, account gatewaydispatch.AccountCandidate, _ accountErrorPolicyDecision, _ chainErrorPolicyFailureInput) (bool, string, error) {
	d.degrade()
	return false, account.Status, nil
}

func (d *degradedChainErrorPolicyEffects) RecordKeyScopedQuotaFailure(_ context.Context, _ gatewaydispatch.AccountCandidate, _ accountErrorPolicyDecision, _ chainErrorPolicyFailureInput) error {
	d.degrade()
	return nil
}
