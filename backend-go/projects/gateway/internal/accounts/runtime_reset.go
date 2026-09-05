package accounts

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	circuitcontrolplane "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_control_plane"
)

// Runtime-reset endpoint service (维护者 6f9739e96): the port of
// backend/src/modules/accounts/account-runtime-reset.service.ts plus the
// focused subset of account-summary.repository.ts
// (findAccountSummaryAsync), account-management-patch.repository.ts
// (patchAccountFailureStateInTransaction) and
// account-authorized-dispatch.repository.ts
// (updateAuthorizedAccountBindingDispatchAsync restricted to the
// clearFailureState command) that the flow needs. Runtime surfaces (gateway
// runtime keys, latency degradation, api key pool/transient state, health
// check dispatch, authorization quota) go through the narrow
// RuntimeResetEffects port; every persistent account write stays in this
// package. The dispatch revision fence reuses the in-package control-plane
// SQL (batch_effects.go advanceBatchDispatchRevision).

const runtimeResetAction = "runtime_reset"

// RuntimeResetResult mirrors AccountRuntimeResetResult.
type RuntimeResetResult struct {
	ID                        string   `json:"id"`
	ConfigRevision            int64    `json:"configRevision"`
	DispatchRevision          *int64   `json:"dispatchRevision,omitempty"`
	Changed                   bool     `json:"changed"`
	Status                    string   `json:"status"`
	Schedulable               bool     `json:"schedulable"`
	DispatchEligible          bool     `json:"dispatchEligible"`
	GatewayRuntime            string   `json:"gatewayRuntime"`
	LatencyDegradationCleared int64    `json:"latencyDegradationCleared"`
	APIKeyRuntimeRevalidated  int      `json:"apiKeyRuntimeRevalidated"`
	APIKeyTransientCleared    int      `json:"apiKeyTransientCleared"`
	Cleared                   []string `json:"cleared"`
	Skipped                   []string `json:"skipped"`
	Failed                    []string `json:"failed"`
}

// RuntimeResetOutcome mirrors AccountRuntimeResetOutcome: the response payload
// plus the operation-log entry fields.
type RuntimeResetOutcome struct {
	Result RuntimeResetResult `json:"result"`
	Log    RuntimeResetLog    `json:"-"`
}

// RuntimeResetLog mirrors the log half of AccountRuntimeResetOutcome.
type RuntimeResetLog struct {
	OperationScopeSystemAccountID string
	Mode                          string
	Module                        string
	Action                        string
	OperationKey                  string
	ResourceType                  string
	ResourceID                    string
	ResourceName                  string
	Summary                       string
	Changes                       []PatchChange
	ViewerSystemAccountID         string
}

// resetSummary is the focused findAccountSummaryAsync projection the reset
// flow reads: the persistent fields plus the authorized-branch joins.
type resetSummary struct {
	id                                 string
	configRevision                     int64
	dispatchRevision                   sql.NullInt64
	name                               string
	accountType                        string
	credentials                        Credentials
	status                             string
	schedulable                        bool
	providerCode                       string
	providerProtocolProfileID          string
	protocolCode                       string
	protocolVersion                    string
	clientCompatibility                string
	systemAccountID                    string
	accessType                         string // 'owner' | 'authorized'
	accountExpiresAt                   sql.NullString
	cooldownUntil                      sql.NullString
	lastErrorCode                      sql.NullString
	lastErrorMessage                   sql.NullString
	lastErrorTraceID                   sql.NullString
	lastHealthCheckAt                  sql.NullString
	lastHealthCheckErrorCode           sql.NullString
	lastHealthCheckErrorMessage        sql.NullString
	cooldownRetestFailureCount         int
	cooldownRetestObservationStartedAt sql.NullString
	cooldownRetestGeneration           sql.NullString
	cooldownRetestLastAt               sql.NullString
	healthCheckFailureCount            int
	healthCheckFailureStartedAt        sql.NullString
	streamFailureCount                 int
	streamFailureWindowStartedAt       sql.NullString
	// authorized-instance columns (owner rows leave them NULL).
	authorizationID              sql.NullString // accounts.authorization_instance_authorization_id
	authorizationStatus          sql.NullString
	authorizationExpiresAt       sql.NullString
	authorizationEffectiveTeamID sql.NullString
	authorizationQuotaLimited    bool
	sourceAccountID              sql.NullString
	sourceID                     sql.NullString
	sourceStatus                 sql.NullString
	sourceSchedulable            sql.NullInt64
	sourceExpiresAt              sql.NullString
	sourceLastErrorCode          sql.NullString
	sourceLastErrorMessage       sql.NullString
	sourceCooldownUntil          sql.NullString
	// bound group (latest enabled group_accounts row).
	bindingSystemAccountID     sql.NullString
	boundGroupID               sql.NullString
	boundGroupAuthorizationID  sql.NullString
	authorizationQuotaExceeded bool
}

// ResetAccountRuntimeState mirrors resetAccountRuntimeStateAsync. A nil
// summary means the account does not exist in scope (route renders 404).
func (s *Store) ResetAccountRuntimeState(ctx context.Context, accountID string, expectedConfigRevision int64, access AccessScope) (*RuntimeResetOutcome, error) {
	ctx = ensureCtx(ctx)
	before, err := s.findResetSummary(ctx, accountID, access)
	if err != nil {
		return nil, err
	}
	if before == nil {
		return nil, nil
	}
	if before.configRevision != expectedConfigRevision {
		return nil, &RevisionConflictError{Message: RevisionConflictMessage}
	}

	id := before.id
	configRevision := before.configRevision
	changedFields := []string{}
	name := before.name
	ownerSystemAccountID := before.systemAccountID
	status := before.status
	schedulable := before.schedulable
	var authorizedBinding *RuntimeAuthorizedBinding
	healthCheckRequired := false
	healthCheckReason := ""
	failed := []string{}
	apiKeyRuntimeRevalidated := 0
	apiKeyTransientCleared := 0
	var dispatchRevision *int64
	dispatchFenceAdvanced := false
	preserveConfiguredPolicyAvoidance := isExplicitAccountErrorPolicyCooldown(before.lastErrorCode.String, before.lastErrorMessage.String)
	cleared := map[string]bool{}
	skipped := map[string]bool{}
	addCleared := func(key string) { cleared[key] = true }
	addSkipped := func(key string) { skipped[key] = true }

	// Lock incidents are a hard boundary: the reset never turns a lock-policy
	// outage back into a dispatchable account.
	lockState, err := s.findAccountLockStateRow(ctx, before.id)
	if err != nil {
		return nil, errors.New("账户锁死状态读取失败，请稍后重试")
	}
	lockBlocksPersistentReset := lockState.enabled &&
		(lockState.lockState == "ENGAGED" || lockState.lockState == "DEAD_CONFIRMED")
	if lockBlocksPersistentReset {
		addSkipped("lock_state")
	}

	now := s.now()
	if before.accessType == "authorized" {
		manualUnschedulable := before.status == "active" && !before.schedulable
		if before.authorizationID.Valid && before.boundGroupID.Valid && before.authorizationID.String != "" {
			authorizedBinding = &RuntimeAuthorizedBinding{
				SystemAccountID:        before.systemAccountID,
				GroupID:                before.boundGroupID.String,
				AccountAuthorizationID: before.authorizationID.String,
			}
		}
		sourceExplicitPolicyCooldown := isExplicitAccountErrorPolicyCooldown(before.sourceLastErrorCode.String, before.sourceLastErrorMessage.String)
		preserveConfiguredPolicyAvoidance = preserveConfiguredPolicyAvoidance || sourceExplicitPolicyCooldown
		sourceBlocked := !before.sourceAccountID.Valid || before.sourceAccountID.String == "" ||
			(before.authorizationStatus.Valid && before.authorizationStatus.String != "active") ||
			isResourceAuthorizationExpired(before.authorizationExpiresAt.String, now) ||
			bindingIsAuthorizationUnavailable(before) ||
			(before.sourceAccountID.Valid && before.sourceAccountID.String != "" && !before.sourceStatus.Valid) ||
			(before.sourceStatus.Valid && before.sourceStatus.String != "active") ||
			(before.sourceSchedulable.Valid && before.sourceSchedulable.Int64 == 0) ||
			isAccountExpired(before.sourceExpiresAt.String, now) ||
			before.sourceLastErrorCode.String == "account_expired" ||
			isAccountExpired(before.accountExpiresAt.String, now) ||
			before.lastErrorCode.String == "account_expired" ||
			before.status == "quality_isolated" ||
			sourceExplicitPolicyCooldown ||
			isFutureTimestamp(before.sourceCooldownUntil.String, now) ||
			before.authorizationQuotaExceeded ||
			isExplicitAccountErrorPolicyCooldown(before.lastErrorCode.String, before.lastErrorMessage.String) ||
			lockBlocksPersistentReset ||
			manualUnschedulable
		if before.status == "pending_test" {
			addSkipped("pending_test")
		}
		if before.status == "error" {
			addSkipped("health_check_gate")
		}
		if before.status == "disabled" {
			addSkipped("disabled")
		}
		if before.status == "quality_isolated" {
			addSkipped("quality_isolated")
		}
		if isAccountExpired(before.accountExpiresAt.String, now) || before.lastErrorCode.String == "account_expired" {
			addSkipped("expired")
		}
		if manualUnschedulable {
			addSkipped("manual_unschedulable")
		}
		if before.authorizationQuotaExceeded {
			addSkipped("authorization_quota")
		}
		if sourceBlocked && !before.authorizationQuotaExceeded {
			addSkipped("authorization_source_blocked")
		}
		if sourceExplicitPolicyCooldown || isExplicitAccountErrorPolicyCooldown(before.lastErrorCode.String, before.lastErrorMessage.String) {
			addSkipped("explicit_policy_cooldown")
		}
		if before.status != "pending_test" && before.status != "error" && before.status != "disabled" &&
			!sourceBlocked && !manualUnschedulable {
			patched, err := s.updateAuthorizedBindingDispatchForReset(ctx, updateAuthorizedBindingDispatchInput{
				accountID:              before.id,
				expectedConfigRevision: expectedConfigRevision,
				access:                 access,
			})
			if err != nil {
				return nil, err
			}
			if patched == nil {
				return nil, nil
			}
			id = patched.id
			configRevision = patched.configRevision
			changedFields = patched.changedFields
			name = patched.name
			ownerSystemAccountID = patched.ownerSystemAccountID
			if patched.patchStatus != nil {
				status = *patched.patchStatus
			} else {
				status = before.status
			}
			if patched.patchSchedulable != nil {
				schedulable = *patched.patchSchedulable
			} else {
				schedulable = before.schedulable
			}
			authorizedBinding = patched.authorizedBinding
			if len(patched.changedFields) > 0 {
				addCleared("account_persistent")
			}
			// The failure-state transaction advances the circuit fence only
			// when it restores the account directly to active; the manual
			// fence below still runs otherwise.
			dispatchFenceAdvanced = len(patched.changedFields) > 0 && patched.runtimeRestoreRequired && status == "active"
			if dispatchFenceAdvanced {
				addCleared("dispatch_revision")
			}
		}
	} else {
		manualUnschedulable := before.status == "active" && !before.schedulable
		pendingHealthCheckWithoutFailure := before.status == "pending_test" &&
			!(before.lastHealthCheckAt.Valid && (before.lastHealthCheckErrorCode.Valid || before.lastHealthCheckErrorMessage.Valid))
		skipPersistentClear := pendingHealthCheckWithoutFailure ||
			before.status == "disabled" ||
			manualUnschedulable ||
			before.lastErrorCode.String == "account_expired" ||
			isAccountExpired(before.accountExpiresAt.String, now) ||
			before.status == "quality_isolated" ||
			!resetHasPersistentFailureState(before) ||
			isExplicitAccountErrorPolicyCooldown(before.lastErrorCode.String, before.lastErrorMessage.String) ||
			lockBlocksPersistentReset
		if before.status == "pending_test" {
			addSkipped("pending_test")
		}
		if before.status == "disabled" {
			addSkipped("disabled")
		}
		if before.status == "quality_isolated" {
			addSkipped("quality_isolated")
		}
		if before.lastErrorCode.String == "account_expired" || isAccountExpired(before.accountExpiresAt.String, now) {
			addSkipped("expired")
		}
		if manualUnschedulable {
			addSkipped("manual_unschedulable")
		}
		if isExplicitAccountErrorPolicyCooldown(before.lastErrorCode.String, before.lastErrorMessage.String) {
			addSkipped("explicit_policy_cooldown")
		}
		if !skipPersistentClear {
			patched, err := s.patchAccountFailureStateForReset(ctx, patchFailureStateInput{
				accountID:              before.id,
				expectedConfigRevision: expectedConfigRevision,
				access:                 access,
				now:                    now,
			})
			if err != nil {
				return nil, err
			}
			if patched == nil {
				return nil, nil
			}
			id = patched.id
			configRevision = patched.configRevision
			changedFields = patched.changedFields
			name = patched.name
			ownerSystemAccountID = patched.ownerSystemAccountID
			status = patched.status
			healthCheckRequired = patched.healthCheckRequired
			healthCheckReason = patched.healthCheckReason
			if len(patched.changedFields) > 0 {
				addCleared("account_persistent")
			}
			dispatchFenceAdvanced = len(patched.changedFields) > 0 && patched.runtimeRestoreRequired && status == "active"
			if dispatchFenceAdvanced {
				addCleared("dispatch_revision")
			}
		}
	}

	// Gateway runtime availability clear (port).
	effects := s.runtimeResetEffectsOrNil()
	runtimeClearCleared := false
	{
		if effects != nil {
			clearResult, err := effects.ClearAccountRuntimeAvailability(ctx, RuntimeAvailabilityClearInput{
				AccountID:                         id,
				AuthorizedBinding:                 authorizedBinding,
				IncludeBaseAccountKey:             before.accessType != "authorized",
				PreserveConfiguredPolicyAvoidance: preserveConfiguredPolicyAvoidance,
			})
			if err != nil || len(clearResult.FailedKeys) > 0 {
				failed = append(failed, "gateway_runtime")
			}
			runtimeClearCleared = err == nil && clearResult.Cleared
			if runtimeClearCleared {
				addCleared("gateway_runtime")
			}
		} else {
			failed = append(failed, "gateway_runtime")
		}
	}

	systemAccountID := before.systemAccountID
	latencyDegradationCleared := int64(0)
	if effects != nil && systemAccountID != "" {
		clearedCount, err := effects.ClearNormalRouteLatencyDegradation(ctx, systemAccountID, before.id)
		if err != nil {
			failed = append(failed, "speed_first_latency")
		} else {
			latencyDegradationCleared = clearedCount
			if latencyDegradationCleared > 0 {
				addCleared("speed_first_latency")
			}
		}
	}

	// Runtime-only resets may not touch any persistent column; advance the
	// circuit dispatch fence when runtime state was actually cleared.
	if !dispatchFenceAdvanced && (len(changedFields) > 0 || runtimeClearCleared || latencyDegradationCleared > 0) {
		transitionID := newResetDispatchTransitionID()
		fenced, err := s.advanceResetDispatchRevision(ctx, id, transitionID, now.UnixMilli())
		if err != nil {
			failed = append(failed, "dispatch_revision")
		} else {
			revision := fenced.dispatchRevision
			dispatchRevision = &revision
			dispatchFenceAdvanced = fenced.status == "applied" || fenced.status == "idempotent"
			if dispatchFenceAdvanced {
				addCleared("dispatch_revision")
			}
		}
	}

	if healthCheckRequired && healthCheckReason != "" && effects != nil {
		effects.DispatchAccountHealthCheck(id, healthCheckReason)
	}

	// Current summary refresh.
	current, err := s.findResetSummary(ctx, id, access)
	if err != nil {
		return nil, err
	}
	if current != nil {
		configRevision = current.configRevision
		if current.dispatchRevision.Valid {
			revision := current.dispatchRevision.Int64
			dispatchRevision = &revision
		}
		status = current.status
		schedulable = current.schedulable
	}
	if before.accessType != "authorized" && before.accountType == "api_key" {
		if effects != nil {
			revalidated, err := effects.RevalidateAccountAPIKeyRuntimePool(ctx, id, configRevision)
			if err != nil {
				failed = append(failed, "api_key_runtime")
			} else if revalidated.Eligible {
				apiKeyRuntimeRevalidated = revalidated.Changed
				if apiKeyRuntimeRevalidated > 0 {
					addCleared("api_key_runtime")
				}
			}
		}
	}
	if before.accessType != "authorized" && before.accountType == "api_key" {
		if effects != nil {
			clearedCount, err := s.clearResetAPIKeyTransientStates(ctx, effects, id, before.credentials)
			if err != nil {
				failed = append(failed, "api_key_transient")
			} else {
				apiKeyTransientCleared = clearedCount
				if apiKeyTransientCleared > 0 {
					addCleared("api_key_transient")
				}
			}
		}
	}

	final, err := s.findResetSummary(ctx, id, access)
	if err != nil {
		return nil, err
	}
	finalLockState, err := s.findAccountLockStateRow(ctx, id)
	finalLockStateReadFailed := err != nil
	if finalLockStateReadFailed {
		failed = append(failed, "lock_state")
	}
	finalLockBlocked := finalLockStateReadFailed ||
		(finalLockState.enabled && (finalLockState.lockState == "ENGAGED" || finalLockState.lockState == "DEAD_CONFIRMED"))
	if finalLockBlocked {
		addSkipped("lock_state")
	}
	finalAvailable := false
	finalSchedulable := false
	if final != nil {
		finalAvailable = s.resetEffectiveAvailability(ctx, final, s.now())
		finalSchedulable = final.schedulable
		configRevision = final.configRevision
		if final.dispatchRevision.Valid {
			revision := final.dispatchRevision.Int64
			dispatchRevision = &revision
		}
		status = final.status
		schedulable = final.schedulable
	}
	dispatchEligible := !finalLockBlocked && finalAvailable && finalSchedulable

	changed := len(changedFields) > 0 || runtimeClearCleared || latencyDegradationCleared > 0 ||
		apiKeyRuntimeRevalidated > 0 || apiKeyTransientCleared > 0 || dispatchFenceAdvanced

	result := RuntimeResetResult{
		ID:                        id,
		ConfigRevision:            configRevision,
		DispatchRevision:          dispatchRevision,
		Changed:                   changed,
		Status:                    status,
		Schedulable:               schedulable,
		DispatchEligible:          dispatchEligible,
		GatewayRuntime:            "unavailable",
		LatencyDegradationCleared: latencyDegradationCleared,
		APIKeyRuntimeRevalidated:  apiKeyRuntimeRevalidated,
		APIKeyTransientCleared:    apiKeyTransientCleared,
		Cleared:                   sortedKeySet(cleared),
		Skipped:                   sortedKeySet(skipped),
		Failed:                    failed,
	}
	if effects != nil {
		if runtimeClearCleared {
			result.GatewayRuntime = "cleared"
		} else {
			result.GatewayRuntime = "unchanged"
		}
	}
	return &RuntimeResetOutcome{
		Result: result,
		Log: RuntimeResetLog{
			OperationScopeSystemAccountID: ownerSystemAccountID,
			Mode:                          operationMode(access),
			Module:                        "accounts",
			Action:                        runtimeResetAction,
			OperationKey:                  "accounts." + runtimeResetAction,
			ResourceType:                  "account",
			ResourceID:                    id,
			ResourceName:                  name,
			Summary:                       "清理 AI 账户运行状态：" + name,
			Changes: []PatchChange{
				{Field: "runtimeState", Before: before.status, After: status},
			},
			ViewerSystemAccountID: ownerSystemAccountID,
		},
	}, nil
}

func sortedKeySet(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sortStrings(out)
	return out
}

// ---- persistent failure-state patch (owner branch) ----

type patchFailureStateInput struct {
	accountID              string
	expectedConfigRevision int64
	access                 AccessScope
	now                    time.Time
}

type patchFailureStateResult struct {
	id                     string
	configRevision         int64
	changedFields          []string
	name                   string
	ownerSystemAccountID   string
	status                 string
	healthCheckRequired    bool
	healthCheckReason      string
	runtimeRestoreRequired bool
}

// patchFailureStateRow is the locked projection the owner failure-state patch
// reads (the clearFailureState column set of
// account-management-patch.repository.ts patchAccountFailureStateInTransaction).
type patchFailureStateRow struct {
	id                                   string
	configRevision                       int64
	systemAccountID                      string
	name                                 string
	status                               string
	schedulable                          int
	accountExpiresAt                     sql.NullString
	cooldownUntil                        sql.NullString
	lastErrorCode                        sql.NullString
	lastErrorMessage                     sql.NullString
	lastErrorTraceID                     sql.NullString
	lastHealthCheckAt                    sql.NullString
	nextHealthCheckAt                    sql.NullString
	lastHealthSuccessAt                  sql.NullString
	healthCheckFailureCount              sql.NullInt64
	healthCheckFailureStartedAt          sql.NullString
	lastHealthCheckStatusCode            sql.NullInt64
	lastHealthCheckErrorCode             sql.NullString
	lastHealthCheckErrorMessage          sql.NullString
	lastHealthCheckTraceID               sql.NullString
	cooldownRetestFailureCount           sql.NullInt64
	cooldownRetestObservationStartedAt   sql.NullString
	cooldownRetestGeneration             sql.NullString
	cooldownRetestLastAt                 sql.NullString
	cooldownRetestLastStatusCode         sql.NullInt64
	streamFailureCount                   sql.NullInt64
	streamFailureWindowStartedAt         sql.NullString
	authorizationInstanceAuthorizationID sql.NullString
}

// patchAccountFailureStateForReset mirrors
// patchAccountFailureStateInTransaction restricted to the
// {expectedConfigRevision, clearFailureState, runtimeResetRequireUnlocked}
// command the reset sends. Returns (nil, nil) when the row is out of scope.
func (s *Store) patchAccountFailureStateForReset(ctx context.Context, in patchFailureStateInput) (*patchFailureStateResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	scoped := in.access.manageableID()
	scopeClause := ""
	args := []any{in.accountID}
	if scoped != "" {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row patchFailureStateRow
	err = tx.QueryRowContext(ctx, s.bind(`SELECT accounts.id, accounts.config_revision,
			accounts.system_account_id, accounts.name, accounts.status, accounts.schedulable,
			accounts.account_expires_at, accounts.cooldown_until, accounts.last_error_code,
			accounts.last_error_message, accounts.last_error_trace_id,
			accounts.last_health_check_at, accounts.next_health_check_at, accounts.last_health_success_at,
			accounts.health_check_failure_count, accounts.health_check_failure_started_at,
			accounts.last_health_check_status_code, accounts.last_health_check_error_code,
			accounts.last_health_check_error_message, accounts.last_health_check_trace_id,
			accounts.cooldown_retest_failure_count, accounts.cooldown_retest_observation_started_at,
			accounts.cooldown_retest_generation, accounts.cooldown_retest_last_at,
			accounts.cooldown_retest_last_status_code, accounts.stream_failure_count,
			accounts.stream_failure_window_started_at, accounts.authorization_instance_authorization_id
		FROM `+s.table("accounts")+` accounts
		WHERE accounts.id = ?
			AND accounts.deleted_at IS NULL`+scopeClause+`
		LIMIT 1`+s.forUpdate()), args...).Scan(
		&row.id, &row.configRevision, &row.systemAccountID, &row.name, &row.status,
		&row.schedulable, &row.accountExpiresAt, &row.cooldownUntil, &row.lastErrorCode,
		&row.lastErrorMessage, &row.lastErrorTraceID,
		&row.lastHealthCheckAt, &row.nextHealthCheckAt, &row.lastHealthSuccessAt,
		&row.healthCheckFailureCount, &row.healthCheckFailureStartedAt,
		&row.lastHealthCheckStatusCode, &row.lastHealthCheckErrorCode,
		&row.lastHealthCheckErrorMessage, &row.lastHealthCheckTraceID,
		&row.cooldownRetestFailureCount, &row.cooldownRetestObservationStartedAt,
		&row.cooldownRetestGeneration, &row.cooldownRetestLastAt,
		&row.cooldownRetestLastStatusCode, &row.streamFailureCount,
		&row.streamFailureWindowStartedAt, &row.authorizationInstanceAuthorizationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.authorizationInstanceAuthorizationID.Valid && row.authorizationInstanceAuthorizationID.String != "" {
		// Authorized instances go through the authorized dispatch write.
		return nil, nil
	}

	// runtimeResetRequireUnlocked: a live lock incident blocks the reset.
	lock, err := s.findAccountLockStateRowTx(ctx, tx, row.id)
	if err != nil {
		return nil, err
	}
	if lock.enabled && (lock.lockState == "ENGAGED" || lock.lockState == "DEAD_CONFIRMED") {
		return s.unchangedFailureStateResult(tx, row)
	}
	if row.status == "pending_test" &&
		!(row.lastHealthCheckAt.Valid && (row.lastHealthCheckErrorCode.Valid || row.lastHealthCheckErrorMessage.Valid)) {
		return nil, errors.New("账户正在等待首次后台健康检查，无需重新检查")
	}
	expiredByPackage := isAccountExpired(row.accountExpiresAt.String, in.now)
	if row.status == "disabled" && !expiredByPackage {
		return s.unchangedFailureStateResult(tx, row)
	}

	sets := map[string]any{}
	nextStatus := "active"
	if expiredByPackage {
		nextStatus = "disabled"
	} else if row.status == "pending_test" || row.status == "error" {
		nextStatus = "pending_test"
	}
	if row.status != nextStatus {
		sets["status"] = nextStatus
	}
	nextSchedulable := nextStatus == "active"
	currentSchedulable := row.schedulable == 1
	if currentSchedulable != nextSchedulable {
		sets["schedulable"] = boolInt(nextSchedulable)
	}
	if row.cooldownUntil.Valid {
		sets["cooldown_until"] = nil
	}
	nextErrorCode := any(nil)
	if expiredByPackage {
		nextErrorCode = "account_expired"
	}
	if row.lastErrorCode.Valid || nextErrorCode != nil {
		if row.lastErrorCode.String != nextErrorCode {
			sets["last_error_code"] = nextErrorCode
		}
	}
	nextErrorMessage := any(nil)
	if expiredByPackage {
		nextErrorMessage = "账户套餐已过期，已自动停用"
	} else if nextStatus == "pending_test" {
		nextErrorMessage = "账户已重置，等待后台健康检查"
	}
	if row.lastErrorMessage.Valid && row.lastErrorMessage.String != nextErrorMessage {
		sets["last_error_message"] = nextErrorMessage
	} else if !row.lastErrorMessage.Valid && nextErrorMessage != nil {
		sets["last_error_message"] = nextErrorMessage
	}
	if row.lastErrorTraceID.Valid {
		sets["last_error_trace_id"] = nil
	}
	if row.cooldownRetestFailureCount.Int64 != 0 {
		sets["cooldown_retest_failure_count"] = 0
	}
	if row.cooldownRetestObservationStartedAt.Valid {
		sets["cooldown_retest_observation_started_at"] = nil
	}
	if row.cooldownRetestGeneration.Valid {
		sets["cooldown_retest_generation"] = nil
	}
	if row.cooldownRetestLastAt.Valid {
		sets["cooldown_retest_last_at"] = nil
	}
	if row.cooldownRetestLastStatusCode.Valid {
		sets["cooldown_retest_last_status_code"] = nil
	}
	if row.streamFailureCount.Int64 != 0 {
		sets["stream_failure_count"] = 0
	}
	if row.streamFailureWindowStartedAt.Valid {
		sets["stream_failure_window_started_at"] = nil
	}
	if nextStatus == "pending_test" {
		if row.lastHealthCheckAt.Valid {
			sets["last_health_check_at"] = nil
		}
		if row.nextHealthCheckAt.Valid {
			sets["next_health_check_at"] = nil
		}
		if row.lastHealthSuccessAt.Valid {
			sets["last_health_success_at"] = nil
		}
		if row.healthCheckFailureCount.Int64 != 0 {
			sets["health_check_failure_count"] = 0
		}
		if row.healthCheckFailureStartedAt.Valid {
			sets["health_check_failure_started_at"] = nil
		}
		if row.lastHealthCheckStatusCode.Valid {
			sets["last_health_check_status_code"] = nil
		}
		if row.lastHealthCheckErrorCode.Valid {
			sets["last_health_check_error_code"] = nil
		}
		if row.lastHealthCheckErrorMessage.Valid {
			sets["last_health_check_error_message"] = nil
		}
		if row.lastHealthCheckTraceID.Valid {
			sets["last_health_check_trace_id"] = nil
		}
	}
	if len(sets) == 0 {
		return s.unchangedFailureStateResult(tx, row)
	}
	assignments := []string{}
	setArgs := []any{}
	for column, value := range sets {
		assignments = append(assignments, column+" = ?")
		setArgs = append(setArgs, value)
	}
	assignments = append(assignments, "config_revision = config_revision + 1", "updated_at = ?")
	setArgs = append(setArgs, isoMillis(in.now))
	updateArgs := append(append([]any{}, setArgs...), row.id, row.configRevision)
	exec, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+` SET
		`+joinStrings(assignments, ", ")+`
		WHERE id = ? AND config_revision = ? AND deleted_at IS NULL`), updateArgs...)
	if err != nil {
		return nil, err
	}
	if affected, _ := exec.RowsAffected(); affected != 1 {
		return nil, &RevisionConflictError{Message: RevisionConflictMessage}
	}
	restoredForDispatch := nextStatus == "active"
	if restoredForDispatch {
		if err := s.advanceBatchDispatchRevision(ctx, tx, row.id, newResetDispatchTransitionID(), in.now.UnixMilli()); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &patchFailureStateResult{
		id:                     row.id,
		configRevision:         row.configRevision + 1,
		changedFields:          []string{"clearFailureState"},
		name:                   row.name,
		ownerSystemAccountID:   row.systemAccountID,
		status:                 nextStatus,
		healthCheckRequired:    nextStatus == "pending_test",
		healthCheckReason:      healthCheckReasonValue(nextStatus == "pending_test"),
		runtimeRestoreRequired: true,
	}, nil
}

func healthCheckReasonValue(required bool) string {
	if required {
		return "activation"
	}
	return ""
}

// unchangedFailureStateResult mirrors unchangedPatchResult: the account stays
// as-is and the reset continues with the runtime-only surfaces.
func (s *Store) unchangedFailureStateResult(tx *sql.Tx, row patchFailureStateRow) (*patchFailureStateResult, error) {
	_ = tx
	return &patchFailureStateResult{
		id:                   row.id,
		configRevision:       row.configRevision,
		changedFields:        []string{},
		name:                 row.name,
		ownerSystemAccountID: row.systemAccountID,
		status:               row.status,
	}, nil
}

// ---- authorized dispatch clear ----

type updateAuthorizedBindingDispatchInput struct {
	accountID              string
	expectedConfigRevision int64
	access                 AccessScope
}

type authorizedDispatchResetResult struct {
	id                     string
	configRevision         int64
	changedFields          []string
	name                   string
	ownerSystemAccountID   string
	patchStatus            *string
	patchSchedulable       *bool
	runtimeRestoreRequired bool
	authorizedBinding      *RuntimeAuthorizedBinding
}

type authorizedDispatchResetRow struct {
	id                                   string
	configRevision                       int64
	systemAccountID                      string
	name                                 string
	status                               string
	schedulable                          int
	accountExpiresAt                     sql.NullString
	cooldownUntil                        sql.NullString
	lastErrorCode                        sql.NullString
	lastErrorMessage                     sql.NullString
	lastErrorTraceID                     sql.NullString
	cooldownRetestFailureCount           sql.NullInt64
	cooldownRetestObservationStartedAt   sql.NullString
	cooldownRetestGeneration             sql.NullString
	cooldownRetestLastAt                 sql.NullString
	cooldownRetestLastStatusCode         sql.NullInt64
	streamFailureCount                   sql.NullInt64
	streamFailureWindowStartedAt         sql.NullString
	authorizationInstanceSourceAccountID sql.NullString
	authorizationInstanceAuthorizationID sql.NullString
	authorizationStatus                  sql.NullString
	authorizationExpiresAt               sql.NullString
	authorizationLimitsJSON              sql.NullString
	authorizationEffectiveTeamID         sql.NullString
	sourceID                             sql.NullString
	sourceStatus                         sql.NullString
	sourceSchedulable                    sql.NullInt64
	sourceExpiresAt                      sql.NullString
	sourceCooldownUntil                  sql.NullString
	sourceLastErrorCode                  sql.NullString
	sourceLastErrorMessage               sql.NullString
}

type authorizedDispatchResetBinding struct {
	groupID                string
	accountAuthorizationID string
	localPriority          sql.NullInt64
	localSuperPriority     sql.NullInt64
	localFallback          sql.NullInt64
}

// updateAuthorizedBindingDispatchForReset mirrors
// updateAuthorizedAccountBindingDispatchAsync restricted to the
// clearFailureState command with runtimeResetRequireUnlocked. Returns
// (nil, nil) when the instance is out of scope or its relation is gone.
func (s *Store) updateAuthorizedBindingDispatchForReset(ctx context.Context, in updateAuthorizedBindingDispatchInput) (*authorizedDispatchResetResult, error) {
	if in.expectedConfigRevision < 1 {
		return nil, &ValidationError{Message: "账户配置版本无效"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	scoped := in.access.manageableID()
	if scoped == "" && !in.access.canAccessAll() {
		return nil, nil
	}
	scopeClause := ""
	args := []any{in.accountID}
	if scoped != "" {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row authorizedDispatchResetRow
	err = tx.QueryRowContext(ctx, s.bind(`SELECT
			accounts.id, accounts.config_revision, accounts.system_account_id, accounts.name,
			accounts.status, accounts.schedulable, accounts.account_expires_at, accounts.cooldown_until,
			accounts.last_error_code, accounts.last_error_message, accounts.last_error_trace_id,
			accounts.cooldown_retest_failure_count, accounts.cooldown_retest_observation_started_at,
			accounts.cooldown_retest_generation, accounts.cooldown_retest_last_at,
			accounts.cooldown_retest_last_status_code, accounts.stream_failure_count,
			accounts.stream_failure_window_started_at, accounts.authorization_instance_source_account_id,
			accounts.authorization_instance_authorization_id,
			authorizations.status AS authorization_status,
			authorizations.expires_at AS authorization_expires_at,
			authorizations.limits_json AS authorization_limits_json,
			authorizations.effective_source_team_id AS authorization_effective_source_team_id,
			source_accounts.id AS source_id,
			source_accounts.status AS source_status,
			source_accounts.schedulable AS source_schedulable,
			source_accounts.account_expires_at AS source_account_expires_at,
			source_accounts.cooldown_until AS source_cooldown_until,
			source_accounts.last_error_code AS source_last_error_code,
			source_accounts.last_error_message AS source_last_error_message
		FROM `+s.table("accounts")+` accounts
		INNER JOIN `+s.table("resource_authorizations")+` authorizations
			ON authorizations.id = accounts.authorization_instance_authorization_id
			AND authorizations.resource_type = 'account'
			AND authorizations.resource_id = accounts.authorization_instance_source_account_id
			AND authorizations.grantee_system_account_id = accounts.system_account_id
		LEFT JOIN `+s.table("accounts")+` source_accounts
			ON source_accounts.id = accounts.authorization_instance_source_account_id
			AND source_accounts.deleted_at IS NULL
		WHERE accounts.id = ?`+scopeClause+`
			AND accounts.authorization_instance_authorization_id IS NOT NULL
			AND accounts.deleted_at IS NULL
		LIMIT 1`+s.forUpdate()), args...).Scan(
		&row.id, &row.configRevision, &row.systemAccountID, &row.name,
		&row.status, &row.schedulable, &row.accountExpiresAt, &row.cooldownUntil,
		&row.lastErrorCode, &row.lastErrorMessage, &row.lastErrorTraceID,
		&row.cooldownRetestFailureCount, &row.cooldownRetestObservationStartedAt,
		&row.cooldownRetestGeneration, &row.cooldownRetestLastAt,
		&row.cooldownRetestLastStatusCode, &row.streamFailureCount,
		&row.streamFailureWindowStartedAt, &row.authorizationInstanceSourceAccountID,
		&row.authorizationInstanceAuthorizationID,
		&row.authorizationStatus, &row.authorizationExpiresAt,
		&row.authorizationLimitsJSON, &row.authorizationEffectiveTeamID,
		&row.sourceID, &row.sourceStatus, &row.sourceSchedulable,
		&row.sourceExpiresAt, &row.sourceCooldownUntil,
		&row.sourceLastErrorCode, &row.sourceLastErrorMessage)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !row.authorizationInstanceSourceAccountID.Valid || row.authorizationInstanceSourceAccountID.String == "" ||
		!row.authorizationInstanceAuthorizationID.Valid || row.authorizationInstanceAuthorizationID.String == "" {
		return nil, nil
	}
	// Latest enabled binding.
	var binding authorizedDispatchResetBinding
	err = tx.QueryRowContext(ctx, s.bind(`SELECT group_id, account_authorization_id,
			local_priority, local_super_priority_enabled, local_fallback_enabled
		FROM `+s.table("group_accounts")+`
		WHERE account_id = ?
			AND system_account_id = ?
			AND account_authorization_id = ?
			AND enabled = 1
		ORDER BY updated_at DESC, group_id ASC
		LIMIT 1`+s.forUpdate()), row.id, row.systemAccountID, row.authorizationInstanceAuthorizationID.String).
		Scan(&binding.groupID, &binding.accountAuthorizationID, &binding.localPriority,
			&binding.localSuperPriority, &binding.localFallback)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if row.configRevision != in.expectedConfigRevision {
		return nil, &RevisionConflictError{Message: RevisionConflictMessage}
	}
	// runtimeResetRequireUnlocked.
	lock, err := s.findAccountLockStateRowTx(ctx, tx, row.id)
	if err != nil {
		return nil, err
	}
	if lock.enabled && (lock.lockState == "ENGAGED" || lock.lockState == "DEAD_CONFIRMED") {
		return unchangedAuthorizedResetResult(row, binding), nil
	}
	if row.authorizationStatus.Valid && (row.authorizationStatus.String == "revoked" || row.authorizationStatus.String == "returned") {
		return nil, nil
	}

	// clearFailureState implies a pending_test refusal first (Node
	// patchAuthorizedAccountDispatchInTransaction guard).
	if row.status == "pending_test" {
		return nil, errors.New("待检查账户需等待后台健康检查通过后才能参与调度")
	}
	// allowLocalRecovery=true: only the authorization/source availability
	// gates run; the local failure states are exactly what the reset clears.
	if message, err := s.authorizedResetUnavailableMessage(ctx, row, binding, in.access); err != nil {
		return nil, err
	} else if message != "" {
		return nil, errors.New(message)
	}

	currentSchedulable := row.schedulable == 1
	nextStatus := "active"
	nextSchedulable := true
	sets := map[string]any{}
	if row.status != nextStatus {
		sets["status"] = nextStatus
	}
	if currentSchedulable != nextSchedulable {
		sets["schedulable"] = boolInt(nextSchedulable)
	}
	failureStateChanged := clearAuthorizedFailureStateColumnsForReset(sets, row)
	if len(sets) == 0 {
		return unchangedAuthorizedResetResult(row, binding), nil
	}
	nowISO := isoMillis(s.now())
	assignments := []string{}
	setArgs := []any{}
	for column, value := range sets {
		assignments = append(assignments, column+" = ?")
		setArgs = append(setArgs, value)
	}
	assignments = append(assignments, "config_revision = config_revision + 1", "updated_at = ?")
	setArgs = append(setArgs, nowISO)
	updateArgs := append(append([]any{}, setArgs...), row.id, row.systemAccountID, row.authorizationInstanceAuthorizationID.String, row.configRevision)
	exec, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+` SET
		`+joinStrings(assignments, ", ")+`
		WHERE id = ?
			AND system_account_id = ?
			AND authorization_instance_authorization_id = ?
			AND config_revision = ?
			AND deleted_at IS NULL`), updateArgs...)
	if err != nil {
		return nil, err
	}
	if affected, _ := exec.RowsAffected(); affected != 1 {
		return nil, &RevisionConflictError{Message: RevisionConflictMessage}
	}
	restoredForDispatch := nextStatus == "active" && (!currentSchedulable || row.status != "active" || failureStateChanged)
	if restoredForDispatch {
		if err := s.advanceBatchDispatchRevision(ctx, tx, row.id, newResetDispatchTransitionID(), s.now().UnixMilli()); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	changedFields := []string{"clearFailureState"}
	sortStrings(changedFields)
	patchStatus := nextStatus
	patchSchedulable := nextSchedulable
	return &authorizedDispatchResetResult{
		id:                     row.id,
		configRevision:         row.configRevision + 1,
		changedFields:          changedFields,
		name:                   row.name,
		ownerSystemAccountID:   row.systemAccountID,
		patchStatus:            &patchStatus,
		patchSchedulable:       &patchSchedulable,
		runtimeRestoreRequired: true,
		authorizedBinding: &RuntimeAuthorizedBinding{
			SystemAccountID:        row.systemAccountID,
			GroupID:                binding.groupID,
			AccountAuthorizationID: binding.accountAuthorizationID,
		},
	}, nil
}

func unchangedAuthorizedResetResult(row authorizedDispatchResetRow, binding authorizedDispatchResetBinding) *authorizedDispatchResetResult {
	return &authorizedDispatchResetResult{
		id:                   row.id,
		configRevision:       row.configRevision,
		changedFields:        []string{},
		name:                 row.name,
		ownerSystemAccountID: row.systemAccountID,
		authorizedBinding: &RuntimeAuthorizedBinding{
			SystemAccountID:        row.systemAccountID,
			GroupID:                binding.groupID,
			AccountAuthorizationID: binding.accountAuthorizationID,
		},
	}
}

// clearAuthorizedFailureStateColumnsForReset mirrors
// clearAuthorizedFailureStateColumns: only changed columns are written and the
// boolean reports whether any failure column actually moved.
func clearAuthorizedFailureStateColumnsForReset(sets map[string]any, row authorizedDispatchResetRow) bool {
	before := len(sets)
	if row.cooldownUntil.Valid {
		sets["cooldown_until"] = nil
	}
	if row.lastErrorCode.Valid {
		sets["last_error_code"] = nil
	}
	if row.lastErrorMessage.Valid {
		sets["last_error_message"] = nil
	}
	if row.lastErrorTraceID.Valid {
		sets["last_error_trace_id"] = nil
	}
	if row.cooldownRetestFailureCount.Int64 != 0 {
		sets["cooldown_retest_failure_count"] = 0
	}
	if row.cooldownRetestObservationStartedAt.Valid {
		sets["cooldown_retest_observation_started_at"] = nil
	}
	if row.cooldownRetestGeneration.Valid {
		sets["cooldown_retest_generation"] = nil
	}
	if row.cooldownRetestLastAt.Valid {
		sets["cooldown_retest_last_at"] = nil
	}
	if row.cooldownRetestLastStatusCode.Valid {
		sets["cooldown_retest_last_status_code"] = nil
	}
	if row.streamFailureCount.Int64 != 0 {
		sets["stream_failure_count"] = 0
	}
	if row.streamFailureWindowStartedAt.Valid {
		sets["stream_failure_window_started_at"] = nil
	}
	return len(sets) > before
}

// authorizedResetUnavailableMessage mirrors authorizedDispatchUnavailableMessage
// with allowLocalRecovery=true (the clearFailureState path). The request-quota
// gate goes through the RuntimeResetEffects port.
func (s *Store) authorizedResetUnavailableMessage(ctx context.Context, row authorizedDispatchResetRow, binding authorizedDispatchResetBinding, access AccessScope) (string, error) {
	now := s.now()
	nowMillis := now.UnixMilli()
	if row.authorizationStatus.Valid && (row.authorizationStatus.String == "expired" ||
		(!row.authorizationExpiresAt.Valid && row.authorizationStatus.String != "expired" && isResourceAuthorizationExpired(row.authorizationExpiresAt.String, now))) {
		return "授权已到期，当前账户不能调用", nil
	}
	if isResourceAuthorizationExpired(row.authorizationExpiresAt.String, now) {
		return "授权已到期，当前账户不能调用", nil
	}
	if row.authorizationStatus.Valid && row.authorizationStatus.String == "paused" {
		return "授权已暂停，当前账户不能调用", nil
	}
	if row.authorizationStatus.Valid && (row.authorizationStatus.String == "revoked" || row.authorizationStatus.String == "returned") {
		return "授权关系已失效，当前账户不能调用", nil
	}
	if s.resetAuthorizationQuotaExceeded(ctx, row, access) {
		return "授权额度已用完，当前账户不能调用", nil
	}
	if !row.sourceID.Valid || row.sourceID.String == "" || !row.sourceStatus.Valid || row.sourceStatus.String == "" {
		return "授权方原账户不存在或已删除，当前账户不能调用", nil
	}
	if row.sourceLastErrorCode.String == "account_expired" || isAccountExpired(row.sourceExpiresAt.String, now) {
		return "授权方原账户已到期，当前账户不能调用", nil
	}
	switch row.sourceStatus.String {
	case "disabled":
		return "授权方原账户已停用，当前账户不能调用", nil
	case "pending_test":
		return "授权方原账户尚未通过后台健康检查，当前账户不能调用", nil
	case "error":
		if row.sourceLastErrorMessage.Valid && row.sourceLastErrorMessage.String != "" {
			return row.sourceLastErrorMessage.String, nil
		}
		return "授权方原账户处于异常状态，当前账户不能调用", nil
	case "rate_limited":
		if row.sourceLastErrorMessage.Valid && row.sourceLastErrorMessage.String != "" {
			return row.sourceLastErrorMessage.String, nil
		}
		return "授权方原账户限流中，当前账户不能调用", nil
	case "temporary_unavailable":
		if row.sourceLastErrorMessage.Valid && row.sourceLastErrorMessage.String != "" {
			return row.sourceLastErrorMessage.String, nil
		}
		return "授权方原账户临时不可调用，当前账户不能调用", nil
	case "quality_isolated":
		return "授权方原账户因模型质量不达标已隔离，恢复前不能调用", nil
	}
	if isFutureTimestamp(row.sourceCooldownUntil.String, now) {
		return "授权方原账户正在冷却，恢复前当前账户不能调用", nil
	}
	if row.sourceSchedulable.Valid && row.sourceSchedulable.Int64 == 0 {
		return "授权方原账户已关闭调度，当前账户不能调用", nil
	}
	if row.lastErrorCode.String == "account_expired" || isAccountExpired(row.accountExpiresAt.String, now) {
		return "授权账户已到期，当前不可用", nil
	}
	if binding.groupID == "" {
		return "授权账户需要先绑定到你的分组", nil
	}
	_ = nowMillis
	return "", nil
}

// resetSummaryQuotaExceeded resolves the authorization-quota gate for the
// authorized summary projection (Node loadAuthorizedAccountSummaryContextAsync
// authorizationQuotaExceededByAuthorization).
func (s *Store) resetSummaryQuotaExceeded(ctx context.Context, summary *resetSummary) bool {
	effects := s.runtimeResetEffectsOrNil()
	if effects == nil {
		return false
	}
	exceeded, err := effects.AuthorizationQuotaExceeded(ctx, AuthorizationQuotaCheckInput{
		AuthorizationID:        summary.authorizationID.String,
		GranteeSystemAccountID: summary.systemAccountID,
		EffectiveSourceTeamID:  summary.authorizationEffectiveTeamID.String,
	})
	if err != nil {
		slog.Warn("runtime-reset 授权额度读取失败，按未超额处理",
			"event", "account_runtime_reset_authorization_quota_read_failed",
			"accountId", summary.id, "error", err)
		return false
	}
	return exceeded
}

// resetAuthorizationQuotaExceeded resolves the quota gate through the port.
func (s *Store) resetAuthorizationQuotaExceeded(ctx context.Context, row authorizedDispatchResetRow, access AccessScope) bool {
	effects := s.runtimeResetEffectsOrNil()
	if effects == nil {
		return false
	}
	exceeded, err := effects.AuthorizationQuotaExceeded(ctx, AuthorizationQuotaCheckInput{
		AuthorizationID:        row.authorizationInstanceAuthorizationID.String,
		GranteeSystemAccountID: row.systemAccountID,
		EffectiveSourceTeamID:  row.authorizationEffectiveTeamID.String,
	})
	if err != nil {
		slog.Warn("runtime-reset 授权额度读取失败，按未超额处理",
			"event", "account_runtime_reset_authorization_quota_read_failed",
			"accountId", row.id, "error", err)
		return false
	}
	return exceeded
}

// ---- dispatch revision fence (standalone variant) ----

type resetDispatchFenceResult struct {
	status           string
	dispatchRevision int64
}

// advanceResetDispatchRevision mirrors
// advanceAccountCircuitDispatchRevision (standalone): one transaction around
// the in-transaction fence, dedupe replays stay idempotent.
func (s *Store) advanceResetDispatchRevision(ctx context.Context, accountID, transitionID string, nowMS int64) (resetDispatchFenceResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return resetDispatchFenceResult{}, err
	}
	defer tx.Rollback()
	// The shared in-transaction fence reports replays through a nil error;
	// distinguish by re-reading the current dispatch revision for idempotent
	// replays.
	dedupeKey := "dispatch:" + transitionID
	var replayRevision int64
	var replayEventType, replayAccountID, replayRuntimeKey string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT event_type, account_id, account_runtime_key, dispatch_revision
		FROM `+s.table("account_circuit_outbox")+`
		WHERE projection_key = ? AND dedupe_key = ?`), "account_circuit_dispatch", dedupeKey).
		Scan(&replayEventType, &replayAccountID, &replayRuntimeKey, &replayRevision)
	if err == nil {
		if replayEventType != "dispatch_revision_changed" || replayAccountID != accountID || replayRuntimeKey != accountID {
			return resetDispatchFenceResult{}, errors.New("账户 circuit outbox dedupe key 与既有事件身份冲突")
		}
		if err := tx.Commit(); err != nil {
			return resetDispatchFenceResult{}, err
		}
		return resetDispatchFenceResult{status: "idempotent", dispatchRevision: replayRevision}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return resetDispatchFenceResult{}, err
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT dispatch_revision FROM `+s.table("accounts")+`
		WHERE id = ?`+s.forUpdate()), accountID).Scan(&revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return resetDispatchFenceResult{}, fmt.Errorf("AI 账户不存在：%s", accountID)
		}
		return resetDispatchFenceResult{}, err
	}
	exec, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET dispatch_revision = dispatch_revision + 1
		WHERE id = ? AND dispatch_revision = ?`), accountID, revision)
	if err != nil {
		return resetDispatchFenceResult{}, err
	}
	if affected, _ := exec.RowsAffected(); affected != 1 {
		return resetDispatchFenceResult{}, errors.New("账户 dispatch revision 推进冲突：" + accountID)
	}
	revision++
	eventID, err := batchCircuitEventID()
	if err != nil {
		return resetDispatchFenceResult{}, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_circuit_outbox")+`
		(event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
		 circuit_scope_key, incident_id, transition_id, dispatch_revision, generation,
		 ledger_revision, status, available_at_ms, attempt_count, created_at_ms, updated_at_ms)
		VALUES (?, ?, ?, 'dispatch_revision_changed', ?, ?, NULL, NULL, ?, ?, NULL, NULL, 'pending', ?, 0, ?, ?)`),
		eventID, circuitProjectionKey(), dedupeKey, accountID, accountID,
		transitionID, revision, nowMS, nowMS, nowMS); err != nil {
		return resetDispatchFenceResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return resetDispatchFenceResult{}, err
	}
	return resetDispatchFenceResult{status: "applied", dispatchRevision: revision}, nil
}

// ---- shared helpers ----

type resetLockStateRow struct {
	enabled   bool
	lockState string
}

func (s *Store) findAccountLockStateRow(ctx context.Context, accountID string) (resetLockStateRow, error) {
	return s.findAccountLockStateRowTx(ctx, s.db, accountID)
}

func (s *Store) findAccountLockStateRowTx(ctx context.Context, q queryer, accountID string) (resetLockStateRow, error) {
	var row resetLockStateRow
	var enabled int64
	var lockState sql.NullString
	err := q.QueryRowContext(ctx, s.bind(`SELECT enabled, lock_state FROM `+s.table("account_lock_states")+`
		WHERE account_id = ?`), accountID).Scan(&enabled, &lockState)
	if errors.Is(err, sql.ErrNoRows) {
		return resetLockStateRow{enabled: false, lockState: "UNLOCKED"}, nil
	}
	if err != nil {
		return row, err
	}
	row.enabled = enabled == 1
	row.lockState = lockState.String
	return row, nil
}

// newResetDispatchTransitionID mirrors newId('dispatch').
func newResetDispatchTransitionID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return "dispatch_" + itoa64(time.Now().UnixMilli()) + "_" + hex.EncodeToString(buf)[:8]
}

// circuitProjectionKey mirrors accountCircuitProjectionKey (the shared
// business/circuit_control_plane constant the batch fence also writes).
func circuitProjectionKey() string { return circuitcontrolplane.ProjectionKey }

// fingerprintAccountAPIKey mirrors fingerprintAccountApiKey: HMAC-SHA256 over
// the runtime secret, hex-encoded.
func fingerprintAccountAPIKey(secret, key string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

type resetAPIKeyEntry struct {
	fingerprint string
}

// accountAPIKeyEntries mirrors accountApiKeyEntries (the fingerprint subset
// the transient clear needs).
func accountAPIKeyEntries(secret string, credentials Credentials) []resetAPIKeyEntry {
	trimKey := func(value any) (string, bool) {
		text, ok := value.(string)
		if !ok {
			return "", false
		}
		key := trimSpaces(text)
		return key, key != ""
	}
	var rawKeys []any
	if list, ok := credentials["api_keys"].([]any); ok && len(list) > 0 {
		rawKeys = list
	} else if single, present := credentials["api_key"]; present {
		rawKeys = []any{single}
	}
	entries := []resetAPIKeyEntry{}
	seen := map[string]bool{}
	for _, value := range rawKeys {
		key, ok := trimKey(value)
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, resetAPIKeyEntry{fingerprint: fingerprintAccountAPIKey(secret, key)})
	}
	return entries
}

func trimSpaces(value string) string {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\t' || value[start] == '\n' || value[start] == '\r') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t' || value[end-1] == '\n' || value[end-1] == '\r') {
		end--
	}
	return value[start:end]
}

// clearResetAPIKeyTransientStates mirrors the api key transient-clear block.
func (s *Store) clearResetAPIKeyTransientStates(ctx context.Context, effects RuntimeResetEffects, accountID string, credentials Credentials) (int, error) {
	entries := accountAPIKeyEntries(s.secret, credentials)
	fingerprints := make([]string, 0, len(entries))
	for _, entry := range entries {
		fingerprints = append(fingerprints, entry.fingerprint)
	}
	states, err := effects.LoadAPIKeyTransientStates(ctx, accountID, fingerprints)
	if err != nil {
		return 0, err
	}
	stateByFingerprint := map[string]AccountAPIKeyTransientSelectionState{}
	for _, state := range states {
		stateByFingerprint[state.KeyFingerprint] = state
	}
	cleared := 0
	for _, entry := range entries {
		state, ok := stateByFingerprint[entry.fingerprint]
		var generation *int64
		if ok && state.HasGeneration {
			generation = &state.TransientGeneration
		}
		didClear := effects.ClearAPIKeyFailureGuard(accountID, entry.fingerprint, generation)
		// A Redis transient record may already have expired from the dispatch
		// projection; the generation is still the authoritative CAS fence and
		// is tombstoned when it exists.
		if generation != nil {
			if transientCleared, err := effects.ClearAPIKeyTransientFailure(ctx, accountID, entry.fingerprint, generation); err != nil {
				return cleared, err
			} else if transientCleared {
				didClear = true
			}
		}
		if didClear {
			cleared++
		}
	}
	return cleared, nil
}

// ---- summary read + availability ----

// findResetSummary mirrors findAccountSummaryAsync for the reset projection:
// owner rows through the owner scope, authorized instances through the
// authorization join.
func (s *Store) findResetSummary(ctx context.Context, accountID string, access AccessScope) (*resetSummary, error) {
	id := trimSpaces(accountID)
	if id == "" {
		return nil, nil
	}
	scoped := access.manageableID()
	if scoped == "" && !access.canAccessAll() {
		return nil, nil
	}
	summary, err := s.findOwnerResetSummary(ctx, id, scoped)
	if err != nil {
		return nil, err
	}
	if summary != nil {
		return summary, nil
	}
	if scoped == "" && !access.canAccessAll() {
		return nil, nil
	}
	return s.findAuthorizedResetSummary(ctx, id, scoped)
}

func (s *Store) findOwnerResetSummary(ctx context.Context, id, scoped string) (*resetSummary, error) {
	scopeClause := ""
	args := []any{id}
	if scoped != "" {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row resetSummary
	var schedulable int64
	var credentialsEncrypted sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT accounts.id, accounts.config_revision, accounts.dispatch_revision,
			accounts.name, accounts.type, accounts.status, accounts.schedulable,
			accounts.provider_code, accounts.provider_protocol_profile_id, accounts.protocol_code,
			accounts.protocol_version, accounts.client_compatibility, accounts.system_account_id,
			accounts.credentials_encrypted, accounts.account_expires_at, accounts.cooldown_until,
			accounts.last_error_code, accounts.last_error_message, accounts.last_error_trace_id,
			accounts.last_health_check_at, accounts.last_health_check_error_code,
			accounts.last_health_check_error_message, accounts.cooldown_retest_failure_count,
			accounts.cooldown_retest_observation_started_at, accounts.cooldown_retest_generation,
			accounts.cooldown_retest_last_at, accounts.health_check_failure_count,
			accounts.health_check_failure_started_at, accounts.stream_failure_count,
			accounts.stream_failure_window_started_at,
			accounts.authorization_instance_authorization_id,
			accounts.authorization_instance_source_account_id
		FROM `+s.table("accounts")+` accounts
		WHERE accounts.id = ?
			AND accounts.deleted_at IS NULL
			AND accounts.authorization_instance_authorization_id IS NULL`+scopeClause+`
		LIMIT 1`), args...).Scan(
		&row.id, &row.configRevision, &row.dispatchRevision,
		&row.name, &row.accountType, &row.status, &schedulable,
		&row.providerCode, &row.providerProtocolProfileID, &row.protocolCode,
		&row.protocolVersion, &row.clientCompatibility, &row.systemAccountID,
		&credentialsEncrypted, &row.accountExpiresAt, &row.cooldownUntil,
		&row.lastErrorCode, &row.lastErrorMessage, &row.lastErrorTraceID,
		&row.lastHealthCheckAt, &row.lastHealthCheckErrorCode,
		&row.lastHealthCheckErrorMessage, &row.cooldownRetestFailureCount,
		&row.cooldownRetestObservationStartedAt, &row.cooldownRetestGeneration,
		&row.cooldownRetestLastAt, &row.healthCheckFailureCount,
		&row.healthCheckFailureStartedAt, &row.streamFailureCount,
		&row.streamFailureWindowStartedAt,
		&row.authorizationID,
		&row.sourceAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.schedulable = schedulable == 1
	row.accessType = "owner"
	row.credentials = Credentials{}
	if trimSpaces(credentialsEncrypted.String) != "" {
		if err := DecryptJSON(s.secret, credentialsEncrypted.String, &row.credentials); err != nil {
			row.credentials = Credentials{}
		}
	}
	return &row, nil
}

func (s *Store) findAuthorizedResetSummary(ctx context.Context, id, scoped string) (*resetSummary, error) {
	scopeClause := ""
	args := []any{id}
	if scoped != "" {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row resetSummary
	var schedulable int64
	var credentialsEncrypted sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT accounts.id, accounts.config_revision, accounts.dispatch_revision,
			accounts.name, accounts.type, accounts.status, accounts.schedulable,
			accounts.provider_code, accounts.provider_protocol_profile_id, accounts.protocol_code,
			accounts.protocol_version, accounts.client_compatibility, accounts.system_account_id,
			accounts.credentials_encrypted, accounts.account_expires_at, accounts.cooldown_until,
			accounts.last_error_code, accounts.last_error_message, accounts.last_error_trace_id,
			accounts.last_health_check_at, accounts.last_health_check_error_code,
			accounts.last_health_check_error_message, accounts.cooldown_retest_failure_count,
			accounts.cooldown_retest_observation_started_at, accounts.cooldown_retest_generation,
			accounts.cooldown_retest_last_at, accounts.health_check_failure_count,
			accounts.health_check_failure_started_at, accounts.stream_failure_count,
			accounts.stream_failure_window_started_at,
			accounts.authorization_instance_authorization_id,
			accounts.authorization_instance_source_account_id,
			authorizations.status AS authorization_status,
			authorizations.expires_at AS authorization_expires_at,
			authorizations.effective_source_team_id AS authorization_effective_source_team_id,
			source_accounts.id AS source_id,
			source_accounts.status AS source_status,
			source_accounts.schedulable AS source_schedulable,
			source_accounts.account_expires_at AS source_account_expires_at,
			source_accounts.last_error_code AS source_last_error_code,
			source_accounts.last_error_message AS source_last_error_message,
			source_accounts.cooldown_until AS source_cooldown_until,
			group_bindings.system_account_id AS binding_system_account_id,
			group_bindings.group_id AS bound_group_id,
			group_bindings.account_authorization_id AS bound_group_account_authorization_id
		FROM `+s.table("accounts")+` accounts
		INNER JOIN `+s.table("resource_authorizations")+` authorizations
			ON authorizations.id = accounts.authorization_instance_authorization_id
		LEFT JOIN `+s.table("accounts")+` source_accounts
			ON source_accounts.id = accounts.authorization_instance_source_account_id
			AND source_accounts.deleted_at IS NULL
		LEFT JOIN `+s.table("group_accounts")+` group_bindings
			ON group_bindings.account_id = accounts.id
			AND group_bindings.system_account_id = accounts.system_account_id
			AND group_bindings.enabled = 1
		WHERE accounts.id = ?`+scopeClause+`
			AND accounts.deleted_at IS NULL
			AND accounts.authorization_instance_authorization_id IS NOT NULL
			AND authorizations.status IN ('active', 'paused', 'expired')
		ORDER BY group_bindings.updated_at DESC
		LIMIT 1`), args...).Scan(
		&row.id, &row.configRevision, &row.dispatchRevision,
		&row.name, &row.accountType, &row.status, &schedulable,
		&row.providerCode, &row.providerProtocolProfileID, &row.protocolCode,
		&row.protocolVersion, &row.clientCompatibility, &row.systemAccountID,
		&credentialsEncrypted, &row.accountExpiresAt, &row.cooldownUntil,
		&row.lastErrorCode, &row.lastErrorMessage, &row.lastErrorTraceID,
		&row.lastHealthCheckAt, &row.lastHealthCheckErrorCode,
		&row.lastHealthCheckErrorMessage, &row.cooldownRetestFailureCount,
		&row.cooldownRetestObservationStartedAt, &row.cooldownRetestGeneration,
		&row.cooldownRetestLastAt, &row.healthCheckFailureCount,
		&row.healthCheckFailureStartedAt, &row.streamFailureCount,
		&row.streamFailureWindowStartedAt,
		&row.authorizationID,
		&row.sourceAccountID,
		&row.authorizationStatus,
		&row.authorizationExpiresAt,
		&row.authorizationEffectiveTeamID,
		&row.sourceID,
		&row.sourceStatus,
		&row.sourceSchedulable,
		&row.sourceExpiresAt,
		&row.sourceLastErrorCode,
		&row.sourceLastErrorMessage,
		&row.sourceCooldownUntil,
		&row.bindingSystemAccountID,
		&row.boundGroupID,
		&row.boundGroupAuthorizationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.schedulable = schedulable == 1
	row.accessType = "authorized"
	row.credentials = Credentials{}
	if trimSpaces(credentialsEncrypted.String) != "" {
		if err := DecryptJSON(s.secret, credentialsEncrypted.String, &row.credentials); err != nil {
			row.credentials = Credentials{}
		}
	}
	row.authorizationQuotaExceeded = s.resetSummaryQuotaExceeded(ctx, &row)
	return &row, nil
}

// bindingIsAuthorizationUnavailable mirrors groupBindStatus ===
// 'authorization_unavailable': the binding row pins a different authorization
// id than the instance stamp.
func bindingIsAuthorizationUnavailable(summary *resetSummary) bool {
	if !summary.boundGroupID.Valid || summary.boundGroupID.String == "" {
		return false
	}
	if !summary.boundGroupAuthorizationID.Valid || summary.boundGroupAuthorizationID.String == "" {
		return false
	}
	if !summary.authorizationID.Valid {
		return true
	}
	return summary.boundGroupAuthorizationID.String != summary.authorizationID.String
}

// resetEffectiveAvailability mirrors accountEffectiveAvailability for the
// reset response (apiKeyPoolAvailability goes through the port; the
// runtimeAvailability overlay is never hydrated by findAccountSummaryAsync in
// Node either).
func (s *Store) resetEffectiveAvailability(ctx context.Context, account *resetSummary, now time.Time) bool {
	if account.accessType == "authorized" {
		// authorizedBindingAvailability.
		if !account.boundGroupID.Valid || account.boundGroupID.String == "" {
			return false
		}
		if bindingIsAuthorizationUnavailable(account) {
			return false
		}
		// authorizationAvailability.
		if account.authorizationStatus.Valid {
			switch account.authorizationStatus.String {
			case "expired":
				return false
			case "paused":
				return false
			case "revoked", "returned":
				return false
			}
		}
		if isResourceAuthorizationExpired(account.authorizationExpiresAt.String, now) {
			return false
		}
		if account.authorizationQuotaExceeded {
			return false
		}
		// sourceAccountAvailability.
		if !account.sourceAccountID.Valid || account.sourceAccountID.String == "" ||
			!account.sourceStatus.Valid || account.sourceStatus.String == "" {
			return false
		}
		if account.sourceLastErrorCode.String == "account_expired" || isAccountExpired(account.sourceExpiresAt.String, now) {
			return false
		}
		switch account.sourceStatus.String {
		case "disabled", "pending_test", "error", "rate_limited", "temporary_unavailable", "quality_isolated":
			return false
		}
		if isFutureTimestamp(account.sourceCooldownUntil.String, now) {
			return false
		}
		if account.sourceSchedulable.Valid && account.sourceSchedulable.Int64 == 0 {
			return false
		}
	}
	// instanceAccountAvailability.
	if account.lastErrorCode.String == "account_expired" || isAccountExpired(account.accountExpiresAt.String, now) {
		return false
	}
	switch account.status {
	case "disabled", "pending_test", "error", "rate_limited", "temporary_unavailable", "quality_isolated":
		return false
	}
	if isFutureTimestamp(account.cooldownUntil.String, now) {
		return false
	}
	if !account.schedulable {
		return false
	}
	// apiKeyPoolAvailability via the port (allUnavailable).
	if account.accessType != "authorized" && account.accountType == "api_key" {
		if effects := s.runtimeResetEffectsOrNil(); effects != nil {
			if allUnavailable, err := effects.APIKeyPoolAllUnavailable(ctx, account.id); err == nil && allUnavailable {
				return false
			}
		}
	}
	return true
}

// isFutureTimestamp mirrors isFutureTimestamp.
func isFutureTimestamp(value string, now time.Time) bool {
	if trimSpaces(value) == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.UnixMilli() > now.UnixMilli()
}

// isResourceAuthorizationExpired mirrors isResourceAuthorizationExpired.
func isResourceAuthorizationExpired(expiresAt string, now time.Time) bool {
	if trimSpaces(expiresAt) == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return false
	}
	return parsed.UnixMilli() <= now.UnixMilli()
}

// isExplicitAccountErrorPolicyCooldown mirrors
// isExplicitAccountErrorPolicyCooldown (account-runtime-provenance.ts).
func isExplicitAccountErrorPolicyCooldown(errorCode, errorMessage string) bool {
	if errorCode == "explicit_account_error_policy_cooldown" || errorCode == "system_quota_explicit_reset" {
		return true
	}
	if trimSpaces(errorCode) != "" {
		return false
	}
	const legacyPrefix = "账户错误策略「"
	const systemQuotaPrefix = "系统继承错误策略「"
	return startsWith(errorMessage, legacyPrefix) || startsWith(errorMessage, systemQuotaPrefix)
}

func startsWith(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}

// resetHasPersistentFailureState mirrors hasPersistentFailureState.
func resetHasPersistentFailureState(account *resetSummary) bool {
	return account.status == "error" ||
		account.status == "rate_limited" ||
		account.status == "temporary_unavailable" ||
		account.cooldownUntil.Valid ||
		account.lastErrorCode.Valid ||
		account.lastErrorMessage.Valid ||
		account.lastErrorTraceID.Valid ||
		account.cooldownRetestFailureCount > 0 ||
		account.cooldownRetestObservationStartedAt.Valid ||
		account.cooldownRetestGeneration.Valid ||
		account.cooldownRetestLastAt.Valid ||
		account.healthCheckFailureCount > 0 ||
		account.healthCheckFailureStartedAt.Valid ||
		account.lastHealthCheckErrorCode.Valid ||
		account.lastHealthCheckErrorMessage.Valid ||
		account.streamFailureCount > 0 ||
		account.streamFailureWindowStartedAt.Valid
}

func joinStrings(values []string, separator string) string {
	return strings.Join(values, separator)
}
