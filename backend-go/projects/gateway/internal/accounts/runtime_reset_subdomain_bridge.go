package accounts

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountsreset"
)

// REFACTOR-0005 阶段 B 桥接层：运行时重置子域（accountsreset）从根包拆出后，
// 门面在这里集中保留类型别名、Store 方法转发与自由函数转发，保证根包内既有
// 引用（runtime_reset_routes.go HTTP 面、w10a/w13a/w13g/w2 各波测试）与外部
// 消费文件（cmd/compose_accounts_reset.go、accountkeystates e2e）零改动。
//
// 降级留根（阶段 A 先例）：runtime_reset_routes.go 的 mount 函数依赖 kernel/
// authsys 与门面路由 helper，整体留在门面；Store.runtimeEffects 注入字段与
// SetRuntimeResetEffects 同名转发留在门面。
//
// 转发每次以 Store 的活字段构造子域 Service（无缓存指针），因此
// "clone := *store; clone.db = ..." 的故障注入克隆语义保持不变。

// ---- 门面别名（子域导出类型） ----

type (
	// RuntimeResetEffects is the narrow cross-package port into the gateway
	// runtime packages.
	RuntimeResetEffects = accountsreset.RuntimeResetEffects
	// RuntimeAvailabilityClearInput mirrors AccountRuntimeAvailabilityClearTarget.
	RuntimeAvailabilityClearInput = accountsreset.RuntimeAvailabilityClearInput
	// RuntimeAuthorizedBinding mirrors the authorizedBinding triple.
	RuntimeAuthorizedBinding = accountsreset.RuntimeAuthorizedBinding
	// RuntimeAvailabilityClearResult mirrors the clear outcome tuple.
	RuntimeAvailabilityClearResult = accountsreset.RuntimeAvailabilityClearResult
	// AccountAPIKeyRuntimeRevalidation mirrors AccountApiKeyRuntimeRevalidateResult.
	AccountAPIKeyRuntimeRevalidation = accountsreset.AccountAPIKeyRuntimeRevalidation
	// AccountAPIKeyTransientSelectionState mirrors the consumed projection of
	// AccountApiKeyRuntimeSelectionState.
	AccountAPIKeyTransientSelectionState = accountsreset.AccountAPIKeyTransientSelectionState
	// AuthorizationQuotaCheckInput mirrors the Node authorizationQuotaExceeded
	// inputs.
	AuthorizationQuotaCheckInput = accountsreset.AuthorizationQuotaCheckInput
	// RuntimeResetResult mirrors AccountRuntimeResetResult.
	RuntimeResetResult = accountsreset.RuntimeResetResult
	// RuntimeResetOutcome mirrors AccountRuntimeResetOutcome.
	RuntimeResetOutcome = accountsreset.RuntimeResetOutcome
	// RuntimeResetLog mirrors the log half of AccountRuntimeResetOutcome.
	RuntimeResetLog = accountsreset.RuntimeResetLog
)

// 根包历史私有名 → 子域类型别名（测试字面量沿用旧名，字段已随子域导出）.
type (
	resetSummary                         = accountsreset.ResetSummary
	patchFailureStateInput               = accountsreset.PatchFailureStateInput
	patchFailureStateResult              = accountsreset.PatchFailureStateResult
	patchFailureStateRow                 = accountsreset.PatchFailureStateRow
	updateAuthorizedBindingDispatchInput = accountsreset.UpdateAuthorizedBindingDispatchInput
	authorizedDispatchResetResult        = accountsreset.AuthorizedDispatchResetResult
	authorizedDispatchResetRow           = accountsreset.AuthorizedDispatchResetRow
	authorizedDispatchResetBinding       = accountsreset.AuthorizedDispatchResetBinding
	resetDispatchFenceResult             = accountsreset.ResetDispatchFenceResult
	resetAPIKeyEntry                     = accountsreset.ResetAPIKeyEntry
)

// ---- StoreBase 适配 + 子域 Service 构造（活字段，无缓存） ----
// storeBaseAdapter 复用 test_subdomain_bridge.go 的实现。

// resetService builds the subdomain service over this store's live fields.
func (s *Store) resetService() *accountsreset.Service {
	return accountsreset.New(storeBaseAdapter{s}, accountsreset.Deps{
		AdvanceBatchDispatchRevision: s.advanceBatchDispatchRevision,
		CircuitEventID:               batchCircuitEventID,
		OperationMode:                operationMode,
	}, s.runtimeEffects)
}

// ---- RuntimeResetEffects 注入口（门面保留同名转发，组合根不变） ----

// SetRuntimeResetEffects wires the runtime port (composition-root handover;
// nil keeps the reset self-contained).
func (s *Store) SetRuntimeResetEffects(effects RuntimeResetEffects) {
	s.runtimeEffects = effects
}

// runtimeResetEffectsOrNil returns the wired port or nil.
func (s *Store) runtimeResetEffectsOrNil() RuntimeResetEffects {
	if s.runtimeEffects == nil {
		slog.Debug("runtime-reset effects port not wired; runtime surfaces stay untouched")
	}
	return s.runtimeEffects
}

// ---- Store 方法转发（运行时重置子域） ----

func (s *Store) ResetAccountRuntimeState(ctx context.Context, accountID string, expectedConfigRevision int64, access AccessScope) (*RuntimeResetOutcome, error) {
	return s.resetService().ResetAccountRuntimeState(ctx, accountID, expectedConfigRevision, access)
}

func (s *Store) patchAccountFailureStateForReset(ctx context.Context, in patchFailureStateInput) (*patchFailureStateResult, error) {
	return s.resetService().PatchAccountFailureStateForReset(ctx, in)
}

func (s *Store) unchangedFailureStateResult(tx *sql.Tx, row patchFailureStateRow) (*patchFailureStateResult, error) {
	return s.resetService().UnchangedFailureStateResult(tx, row)
}

func (s *Store) updateAuthorizedBindingDispatchForReset(ctx context.Context, in updateAuthorizedBindingDispatchInput) (*authorizedDispatchResetResult, error) {
	return s.resetService().UpdateAuthorizedBindingDispatchForReset(ctx, in)
}

func (s *Store) advanceResetDispatchRevision(ctx context.Context, accountID, transitionID string, nowMS int64) (resetDispatchFenceResult, error) {
	return s.resetService().AdvanceResetDispatchRevision(ctx, accountID, transitionID, nowMS)
}

func (s *Store) clearResetAPIKeyTransientStates(ctx context.Context, effects RuntimeResetEffects, accountID string, credentials Credentials) (int, error) {
	return s.resetService().ClearResetAPIKeyTransientStates(ctx, effects, accountID, credentials)
}

func (s *Store) findResetSummary(ctx context.Context, accountID string, access AccessScope) (*resetSummary, error) {
	return s.resetService().FindResetSummary(ctx, accountID, access)
}

func (s *Store) resetEffectiveAvailability(ctx context.Context, account *resetSummary, now time.Time) bool {
	return s.resetService().ResetEffectiveAvailability(ctx, account, now)
}

func (s *Store) authorizedResetUnavailableMessage(ctx context.Context, row authorizedDispatchResetRow, binding authorizedDispatchResetBinding, access AccessScope) (string, error) {
	return s.resetService().AuthorizedResetUnavailableMessage(ctx, row, binding, access)
}

func (s *Store) resetAuthorizationQuotaExceeded(ctx context.Context, row authorizedDispatchResetRow, access AccessScope) bool {
	return s.resetService().ResetAuthorizationQuotaExceeded(ctx, row, access)
}

// ---- 自由函数转发（重置域纯函数，根包测试沿用旧名） ----

func healthCheckReasonValue(required bool) string {
	return accountsreset.HealthCheckReasonValue(required)
}

func unchangedAuthorizedResetResult(row authorizedDispatchResetRow, binding authorizedDispatchResetBinding) *authorizedDispatchResetResult {
	return accountsreset.UnchangedAuthorizedResetResult(row, binding)
}

func clearAuthorizedFailureStateColumnsForReset(sets map[string]any, row authorizedDispatchResetRow) bool {
	return accountsreset.ClearAuthorizedFailureStateColumnsForReset(sets, row)
}

func fingerprintAccountAPIKey(secret, key string) string {
	return accountsreset.FingerprintAccountAPIKey(secret, key)
}

func accountAPIKeyEntries(secret string, credentials Credentials) []resetAPIKeyEntry {
	return accountsreset.AccountAPIKeyEntries(secret, credentials)
}

func trimSpaces(value string) string {
	return accountsreset.TrimSpaces(value)
}

func isFutureTimestamp(value string, now time.Time) bool {
	return accountsreset.IsFutureTimestamp(value, now)
}

func isResourceAuthorizationExpired(expiresAt string, now time.Time) bool {
	return accountsreset.IsResourceAuthorizationExpired(expiresAt, now)
}

func bindingIsAuthorizationUnavailable(summary *resetSummary) bool {
	return accountsreset.BindingIsAuthorizationUnavailable(summary)
}
