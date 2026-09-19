package accountsreset

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

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
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

// Deps bundles the cross-domain function ports the reset flow needs into the
// facade (each adapter closes over the live Store, so composition-root tests
// that clone a Store and swap fields keep observing the clone).
type Deps struct {
	// AdvanceBatchDispatchRevision mirrors Store.advanceBatchDispatchRevision
	// (batch_effects.go): the dispatch-revision family advance inside the
	// caller's transaction.
	AdvanceBatchDispatchRevision func(ctx context.Context, q accountscore.Queryer, accountID, transitionID string, nowMS int64) error
	// CircuitEventID mirrors batchCircuitEventID: the outbox event id slot
	// (random 128 bits, hex-encoded like the control-plane store token).
	CircuitEventID func() (string, error)
	// OperationMode mirrors operationMode (routes.go): the audit log mode for
	// the access scope.
	OperationMode func(access accountscore.AccessScope) string
}

// Service carries the runtime-reset subdomain state: the injected Store
// capability port, the cross-domain function ports and the wired
// RuntimeResetEffects port (构造函数接管注入口，门面保留 SetRuntimeResetEffects
// 同名转发).
type Service struct {
	store accountscore.StoreBase
	deps  Deps
	// effects is the runtime-reset port. Nil until wired through the facade
	// Store field; a nil port keeps the endpoint self-contained (tests) and
	// reports the runtime surfaces unchanged/zero.
	effects RuntimeResetEffects
}

// New builds the subdomain service over the injected Store capability port.
func New(store accountscore.StoreBase, deps Deps, effects RuntimeResetEffects) *Service {
	return &Service{store: store, deps: deps, effects: effects}
}

// forUpdate renders the SELECT ... FOR UPDATE suffix (PostgreSQL only).
func (s *Service) forUpdate() string {
	return accountscore.SQLForUpdate(s.store.PG())
}

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
	Changes                       []accountscore.PatchChange
	ViewerSystemAccountID         string
}

// ResetSummary is the focused findAccountSummaryAsync projection the reset
// flow reads: the persistent fields plus the authorized-branch joins.
type ResetSummary struct {
	ID                                 string
	ConfigRevision                     int64
	DispatchRevision                   sql.NullInt64
	Name                               string
	AccountType                        string
	Credentials                        accountscore.Credentials
	Status                             string
	Schedulable                        bool
	ProviderCode                       string
	ProviderProtocolProfileID          string
	ProtocolCode                       string
	ProtocolVersion                    string
	ClientCompatibility                string
	SystemAccountID                    string
	AccessType                         string // 'owner' | 'authorized'
	AccountExpiresAt                   sql.NullString
	CooldownUntil                      sql.NullString
	LastErrorCode                      sql.NullString
	LastErrorMessage                   sql.NullString
	LastErrorTraceID                   sql.NullString
	LastHealthCheckAt                  sql.NullString
	LastHealthCheckErrorCode           sql.NullString
	LastHealthCheckErrorMessage        sql.NullString
	CooldownRetestFailureCount         int
	CooldownRetestObservationStartedAt sql.NullString
	CooldownRetestGeneration           sql.NullString
	CooldownRetestLastAt               sql.NullString
	HealthCheckFailureCount            int
	HealthCheckFailureStartedAt        sql.NullString
	StreamFailureCount                 int
	StreamFailureWindowStartedAt       sql.NullString
	// authorized-instance columns (owner rows leave them NULL).
	AuthorizationID              sql.NullString // accounts.authorization_instance_authorization_id
	AuthorizationStatus          sql.NullString
	AuthorizationExpiresAt       sql.NullString
	AuthorizationEffectiveTeamID sql.NullString
	AuthorizationQuotaLimited    bool
	SourceAccountID              sql.NullString
	SourceID                     sql.NullString
	SourceStatus                 sql.NullString
	SourceSchedulable            sql.NullInt64
	SourceExpiresAt              sql.NullString
	SourceLastErrorCode          sql.NullString
	SourceLastErrorMessage       sql.NullString
	SourceCooldownUntil          sql.NullString
	// bound group (latest enabled group_accounts row).
	BindingSystemAccountID     sql.NullString
	BoundGroupID               sql.NullString
	BoundGroupAuthorizationID  sql.NullString
	AuthorizationQuotaExceeded bool
}

// ResetAccountRuntimeState mirrors resetAccountRuntimeStateAsync. A nil
// summary means the account does not exist in scope (route renders 404).
func (s *Service) ResetAccountRuntimeState(ctx context.Context, accountID string, expectedConfigRevision int64, access accountscore.AccessScope) (*RuntimeResetOutcome, error) {
	ctx = accountscore.EnsureCtx(ctx)
	before, err := s.FindResetSummary(ctx, accountID, access)
	if err != nil {
		return nil, err
	}
	if before == nil {
		return nil, nil
	}
	if before.ConfigRevision != expectedConfigRevision {
		return nil, &accountscore.RevisionConflictError{Message: accountscore.RevisionConflictMessage}
	}

	id := before.ID
	configRevision := before.ConfigRevision
	changedFields := []string{}
	name := before.Name
	ownerSystemAccountID := before.SystemAccountID
	status := before.Status
	schedulable := before.Schedulable
	var authorizedBinding *RuntimeAuthorizedBinding
	healthCheckRequired := false
	healthCheckReason := ""
	failed := []string{}
	apiKeyRuntimeRevalidated := 0
	apiKeyTransientCleared := 0
	var dispatchRevision *int64
	dispatchFenceAdvanced := false
	preserveConfiguredPolicyAvoidance := IsExplicitAccountErrorPolicyCooldown(before.LastErrorCode.String, before.LastErrorMessage.String)
	cleared := map[string]bool{}
	skipped := map[string]bool{}
	addCleared := func(key string) { cleared[key] = true }
	addSkipped := func(key string) { skipped[key] = true }

	// Lock incidents are a hard boundary: the reset never turns a lock-policy
	// outage back into a dispatchable account.
	lockState, err := s.findAccountLockStateRow(ctx, before.ID)
	if err != nil {
		return nil, errors.New("账户锁死状态读取失败，请稍后重试")
	}
	lockBlocksPersistentReset := lockState.enabled &&
		(lockState.lockState == "ENGAGED" || lockState.lockState == "DEAD_CONFIRMED")
	if lockBlocksPersistentReset {
		addSkipped("lock_state")
	}

	now := s.store.Now()
	if before.AccessType == "authorized" {
		manualUnschedulable := before.Status == "active" && !before.Schedulable
		if before.AuthorizationID.Valid && before.BoundGroupID.Valid && before.AuthorizationID.String != "" {
			authorizedBinding = &RuntimeAuthorizedBinding{
				SystemAccountID:        before.SystemAccountID,
				GroupID:                before.BoundGroupID.String,
				AccountAuthorizationID: before.AuthorizationID.String,
			}
		}
		sourceExplicitPolicyCooldown := IsExplicitAccountErrorPolicyCooldown(before.SourceLastErrorCode.String, before.SourceLastErrorMessage.String)
		preserveConfiguredPolicyAvoidance = preserveConfiguredPolicyAvoidance || sourceExplicitPolicyCooldown
		sourceBlocked := !before.SourceAccountID.Valid || before.SourceAccountID.String == "" ||
			(before.AuthorizationStatus.Valid && before.AuthorizationStatus.String != "active") ||
			IsResourceAuthorizationExpired(before.AuthorizationExpiresAt.String, now) ||
			BindingIsAuthorizationUnavailable(before) ||
			(before.SourceAccountID.Valid && before.SourceAccountID.String != "" && !before.SourceStatus.Valid) ||
			(before.SourceStatus.Valid && before.SourceStatus.String != "active") ||
			(before.SourceSchedulable.Valid && before.SourceSchedulable.Int64 == 0) ||
			accountscore.IsAccountExpired(before.SourceExpiresAt.String, now) ||
			before.SourceLastErrorCode.String == "account_expired" ||
			accountscore.IsAccountExpired(before.AccountExpiresAt.String, now) ||
			before.LastErrorCode.String == "account_expired" ||
			before.Status == "quality_isolated" ||
			sourceExplicitPolicyCooldown ||
			IsFutureTimestamp(before.SourceCooldownUntil.String, now) ||
			before.AuthorizationQuotaExceeded ||
			IsExplicitAccountErrorPolicyCooldown(before.LastErrorCode.String, before.LastErrorMessage.String) ||
			lockBlocksPersistentReset ||
			manualUnschedulable
		if before.Status == "pending_test" {
			addSkipped("pending_test")
		}
		if before.Status == "error" {
			addSkipped("health_check_gate")
		}
		if before.Status == "disabled" {
			addSkipped("disabled")
		}
		if before.Status == "quality_isolated" {
			addSkipped("quality_isolated")
		}
		if accountscore.IsAccountExpired(before.AccountExpiresAt.String, now) || before.LastErrorCode.String == "account_expired" {
			addSkipped("expired")
		}
		if manualUnschedulable {
			addSkipped("manual_unschedulable")
		}
		if before.AuthorizationQuotaExceeded {
			addSkipped("authorization_quota")
		}
		if sourceBlocked && !before.AuthorizationQuotaExceeded {
			addSkipped("authorization_source_blocked")
		}
		if sourceExplicitPolicyCooldown || IsExplicitAccountErrorPolicyCooldown(before.LastErrorCode.String, before.LastErrorMessage.String) {
			addSkipped("explicit_policy_cooldown")
		}
		if before.Status != "pending_test" && before.Status != "error" && before.Status != "disabled" &&
			!sourceBlocked && !manualUnschedulable {
			patched, err := s.UpdateAuthorizedBindingDispatchForReset(ctx, UpdateAuthorizedBindingDispatchInput{
				AccountID:              before.ID,
				ExpectedConfigRevision: expectedConfigRevision,
				Access:                 access,
			})
			if err != nil {
				return nil, err
			}
			if patched == nil {
				return nil, nil
			}
			id = patched.ID
			configRevision = patched.ConfigRevision
			changedFields = patched.ChangedFields
			name = patched.Name
			ownerSystemAccountID = patched.OwnerSystemAccountID
			if patched.PatchStatus != nil {
				status = *patched.PatchStatus
			} else {
				status = before.Status
			}
			if patched.PatchSchedulable != nil {
				schedulable = *patched.PatchSchedulable
			} else {
				schedulable = before.Schedulable
			}
			authorizedBinding = patched.AuthorizedBinding
			if len(patched.ChangedFields) > 0 {
				addCleared("account_persistent")
			}
			// The failure-state transaction advances the circuit fence only
			// when it restores the account directly to active; the manual
			// fence below still runs otherwise.
			dispatchFenceAdvanced = len(patched.ChangedFields) > 0 && patched.RuntimeRestoreRequired && status == "active"
			if dispatchFenceAdvanced {
				addCleared("dispatch_revision")
			}
		}
	} else {
		manualUnschedulable := before.Status == "active" && !before.Schedulable
		pendingHealthCheckWithoutFailure := before.Status == "pending_test" &&
			!(before.LastHealthCheckAt.Valid && (before.LastHealthCheckErrorCode.Valid || before.LastHealthCheckErrorMessage.Valid))
		skipPersistentClear := pendingHealthCheckWithoutFailure ||
			before.Status == "disabled" ||
			manualUnschedulable ||
			before.LastErrorCode.String == "account_expired" ||
			accountscore.IsAccountExpired(before.AccountExpiresAt.String, now) ||
			before.Status == "quality_isolated" ||
			!ResetHasPersistentFailureState(before) ||
			IsExplicitAccountErrorPolicyCooldown(before.LastErrorCode.String, before.LastErrorMessage.String) ||
			lockBlocksPersistentReset
		if before.Status == "pending_test" {
			addSkipped("pending_test")
		}
		if before.Status == "disabled" {
			addSkipped("disabled")
		}
		if before.Status == "quality_isolated" {
			addSkipped("quality_isolated")
		}
		if before.LastErrorCode.String == "account_expired" || accountscore.IsAccountExpired(before.AccountExpiresAt.String, now) {
			addSkipped("expired")
		}
		if manualUnschedulable {
			addSkipped("manual_unschedulable")
		}
		if IsExplicitAccountErrorPolicyCooldown(before.LastErrorCode.String, before.LastErrorMessage.String) {
			addSkipped("explicit_policy_cooldown")
		}
		if !skipPersistentClear {
			patched, err := s.PatchAccountFailureStateForReset(ctx, PatchFailureStateInput{
				AccountID:              before.ID,
				ExpectedConfigRevision: expectedConfigRevision,
				Access:                 access,
				Now:                    now,
			})
			if err != nil {
				return nil, err
			}
			if patched == nil {
				return nil, nil
			}
			id = patched.ID
			configRevision = patched.ConfigRevision
			changedFields = patched.ChangedFields
			name = patched.Name
			ownerSystemAccountID = patched.OwnerSystemAccountID
			status = patched.Status
			healthCheckRequired = patched.HealthCheckRequired
			healthCheckReason = patched.HealthCheckReason
			if len(patched.ChangedFields) > 0 {
				addCleared("account_persistent")
			}
			dispatchFenceAdvanced = len(patched.ChangedFields) > 0 && patched.RuntimeRestoreRequired && status == "active"
			if dispatchFenceAdvanced {
				addCleared("dispatch_revision")
			}
		}
	}

	// Gateway runtime availability clear (port).
	effects := s.effectsOrNil()
	runtimeClearCleared := false
	{
		if effects != nil {
			clearResult, err := effects.ClearAccountRuntimeAvailability(ctx, RuntimeAvailabilityClearInput{
				AccountID:                         id,
				AuthorizedBinding:                 authorizedBinding,
				IncludeBaseAccountKey:             before.AccessType != "authorized",
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

	systemAccountID := before.SystemAccountID
	latencyDegradationCleared := int64(0)
	if effects != nil && systemAccountID != "" {
		clearedCount, err := effects.ClearNormalRouteLatencyDegradation(ctx, systemAccountID, before.ID)
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
		fenced, err := s.AdvanceResetDispatchRevision(ctx, id, transitionID, now.UnixMilli())
		if err != nil {
			failed = append(failed, "dispatch_revision")
		} else {
			revision := fenced.DispatchRevision
			dispatchRevision = &revision
			dispatchFenceAdvanced = fenced.Status == "applied" || fenced.Status == "idempotent"
			if dispatchFenceAdvanced {
				addCleared("dispatch_revision")
			}
		}
	}

	if healthCheckRequired && healthCheckReason != "" && effects != nil {
		effects.DispatchAccountHealthCheck(id, healthCheckReason)
	}

	// Current summary refresh.
	current, err := s.FindResetSummary(ctx, id, access)
	if err != nil {
		return nil, err
	}
	if current != nil {
		configRevision = current.ConfigRevision
		if current.DispatchRevision.Valid {
			revision := current.DispatchRevision.Int64
			dispatchRevision = &revision
		}
		status = current.Status
		schedulable = current.Schedulable
	}
	if before.AccessType != "authorized" && before.AccountType == "api_key" {
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
	if before.AccessType != "authorized" && before.AccountType == "api_key" {
		if effects != nil {
			clearedCount, err := s.ClearResetAPIKeyTransientStates(ctx, effects, id, before.Credentials)
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

	final, err := s.FindResetSummary(ctx, id, access)
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
		finalAvailable = s.ResetEffectiveAvailability(ctx, final, s.store.Now())
		finalSchedulable = final.Schedulable
		configRevision = final.ConfigRevision
		if final.DispatchRevision.Valid {
			revision := final.DispatchRevision.Int64
			dispatchRevision = &revision
		}
		status = final.Status
		schedulable = final.Schedulable
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
			Mode:                          s.deps.OperationMode(access),
			Module:                        "accounts",
			Action:                        runtimeResetAction,
			OperationKey:                  "accounts." + runtimeResetAction,
			ResourceType:                  "account",
			ResourceID:                    id,
			ResourceName:                  name,
			Summary:                       "清理 AI 账户运行状态：" + name,
			Changes: []accountscore.PatchChange{
				{Field: "runtimeState", Before: before.Status, After: status},
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
	accountscore.SortStrings(out)
	return out
}

// ---- persistent failure-state patch (owner branch) ----

type PatchFailureStateInput struct {
	AccountID              string
	ExpectedConfigRevision int64
	Access                 accountscore.AccessScope
	Now                    time.Time
}

type PatchFailureStateResult struct {
	ID                     string
	ConfigRevision         int64
	ChangedFields          []string
	Name                   string
	OwnerSystemAccountID   string
	Status                 string
	HealthCheckRequired    bool
	HealthCheckReason      string
	RuntimeRestoreRequired bool
}

// PatchFailureStateRow is the locked projection the owner failure-state patch
// reads (the clearFailureState column set of
// account-management-patch.repository.ts patchAccountFailureStateInTransaction).
type PatchFailureStateRow struct {
	ID                                   string
	ConfigRevision                       int64
	SystemAccountID                      string
	Name                                 string
	Status                               string
	Schedulable                          int
	AccountExpiresAt                     sql.NullString
	CooldownUntil                        sql.NullString
	LastErrorCode                        sql.NullString
	LastErrorMessage                     sql.NullString
	LastErrorTraceID                     sql.NullString
	LastHealthCheckAt                    sql.NullString
	NextHealthCheckAt                    sql.NullString
	LastHealthSuccessAt                  sql.NullString
	HealthCheckFailureCount              sql.NullInt64
	HealthCheckFailureStartedAt          sql.NullString
	LastHealthCheckStatusCode            sql.NullInt64
	LastHealthCheckErrorCode             sql.NullString
	LastHealthCheckErrorMessage          sql.NullString
	LastHealthCheckTraceID               sql.NullString
	CooldownRetestFailureCount           sql.NullInt64
	CooldownRetestObservationStartedAt   sql.NullString
	CooldownRetestGeneration             sql.NullString
	CooldownRetestLastAt                 sql.NullString
	cooldownRetestLastStatusCode         sql.NullInt64
	StreamFailureCount                   sql.NullInt64
	StreamFailureWindowStartedAt         sql.NullString
	AuthorizationInstanceAuthorizationID sql.NullString
}

// PatchAccountFailureStateForReset mirrors
// patchAccountFailureStateInTransaction restricted to the
// {expectedConfigRevision, clearFailureState, runtimeResetRequireUnlocked}
// command the reset sends. Returns (nil, nil) when the row is out of scope.
func (s *Service) PatchAccountFailureStateForReset(ctx context.Context, in PatchFailureStateInput) (*PatchFailureStateResult, error) {
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	scoped := in.Access.ManageableID()
	scopeClause := ""
	args := []any{in.AccountID}
	if scoped != "" {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row PatchFailureStateRow
	err = tx.QueryRowContext(ctx, s.store.Bind(`SELECT accounts.id, accounts.config_revision,
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
		FROM `+s.store.Table("accounts")+` accounts
		WHERE accounts.id = ?
			AND accounts.deleted_at IS NULL`+scopeClause+`
		LIMIT 1`+s.forUpdate()), args...).Scan(
		&row.ID, &row.ConfigRevision, &row.SystemAccountID, &row.Name, &row.Status,
		&row.Schedulable, &row.AccountExpiresAt, &row.CooldownUntil, &row.LastErrorCode,
		&row.LastErrorMessage, &row.LastErrorTraceID,
		&row.LastHealthCheckAt, &row.NextHealthCheckAt, &row.LastHealthSuccessAt,
		&row.HealthCheckFailureCount, &row.HealthCheckFailureStartedAt,
		&row.LastHealthCheckStatusCode, &row.LastHealthCheckErrorCode,
		&row.LastHealthCheckErrorMessage, &row.LastHealthCheckTraceID,
		&row.CooldownRetestFailureCount, &row.CooldownRetestObservationStartedAt,
		&row.CooldownRetestGeneration, &row.CooldownRetestLastAt,
		&row.cooldownRetestLastStatusCode, &row.StreamFailureCount,
		&row.StreamFailureWindowStartedAt, &row.AuthorizationInstanceAuthorizationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.AuthorizationInstanceAuthorizationID.Valid && row.AuthorizationInstanceAuthorizationID.String != "" {
		// Authorized instances go through the authorized dispatch write.
		return nil, nil
	}

	// runtimeResetRequireUnlocked: a live lock incident blocks the reset.
	lock, err := s.findAccountLockStateRowTx(ctx, tx, row.ID)
	if err != nil {
		return nil, err
	}
	if lock.enabled && (lock.lockState == "ENGAGED" || lock.lockState == "DEAD_CONFIRMED") {
		return s.UnchangedFailureStateResult(tx, row)
	}
	if row.Status == "pending_test" &&
		!(row.LastHealthCheckAt.Valid && (row.LastHealthCheckErrorCode.Valid || row.LastHealthCheckErrorMessage.Valid)) {
		return nil, errors.New("账户正在等待首次后台健康检查，无需重新检查")
	}
	expiredByPackage := accountscore.IsAccountExpired(row.AccountExpiresAt.String, in.Now)
	if row.Status == "disabled" && !expiredByPackage {
		return s.UnchangedFailureStateResult(tx, row)
	}

	sets := map[string]any{}
	nextStatus := "active"
	if expiredByPackage {
		nextStatus = "disabled"
	} else if row.Status == "pending_test" || row.Status == "error" {
		nextStatus = "pending_test"
	}
	if row.Status != nextStatus {
		sets["status"] = nextStatus
	}
	nextSchedulable := nextStatus == "active"
	currentSchedulable := row.Schedulable == 1
	if currentSchedulable != nextSchedulable {
		sets["schedulable"] = accountscore.BoolInt(nextSchedulable)
	}
	if row.CooldownUntil.Valid {
		sets["cooldown_until"] = nil
	}
	nextErrorCode := any(nil)
	if expiredByPackage {
		nextErrorCode = "account_expired"
	}
	if row.LastErrorCode.Valid || nextErrorCode != nil {
		if row.LastErrorCode.String != nextErrorCode {
			sets["last_error_code"] = nextErrorCode
		}
	}
	nextErrorMessage := any(nil)
	if expiredByPackage {
		nextErrorMessage = "账户套餐已过期，已自动停用"
	} else if nextStatus == "pending_test" {
		nextErrorMessage = "账户已重置，等待后台健康检查"
	}
	if row.LastErrorMessage.Valid && row.LastErrorMessage.String != nextErrorMessage {
		sets["last_error_message"] = nextErrorMessage
	} else if !row.LastErrorMessage.Valid && nextErrorMessage != nil {
		sets["last_error_message"] = nextErrorMessage
	}
	if row.LastErrorTraceID.Valid {
		sets["last_error_trace_id"] = nil
	}
	if row.CooldownRetestFailureCount.Int64 != 0 {
		sets["cooldown_retest_failure_count"] = 0
	}
	if row.CooldownRetestObservationStartedAt.Valid {
		sets["cooldown_retest_observation_started_at"] = nil
	}
	if row.CooldownRetestGeneration.Valid {
		sets["cooldown_retest_generation"] = nil
	}
	if row.CooldownRetestLastAt.Valid {
		sets["cooldown_retest_last_at"] = nil
	}
	if row.cooldownRetestLastStatusCode.Valid {
		sets["cooldown_retest_last_status_code"] = nil
	}
	if row.StreamFailureCount.Int64 != 0 {
		sets["stream_failure_count"] = 0
	}
	if row.StreamFailureWindowStartedAt.Valid {
		sets["stream_failure_window_started_at"] = nil
	}
	if nextStatus == "pending_test" {
		if row.LastHealthCheckAt.Valid {
			sets["last_health_check_at"] = nil
		}
		if row.NextHealthCheckAt.Valid {
			sets["next_health_check_at"] = nil
		}
		if row.LastHealthSuccessAt.Valid {
			sets["last_health_success_at"] = nil
		}
		if row.HealthCheckFailureCount.Int64 != 0 {
			sets["health_check_failure_count"] = 0
		}
		if row.HealthCheckFailureStartedAt.Valid {
			sets["health_check_failure_started_at"] = nil
		}
		if row.LastHealthCheckStatusCode.Valid {
			sets["last_health_check_status_code"] = nil
		}
		if row.LastHealthCheckErrorCode.Valid {
			sets["last_health_check_error_code"] = nil
		}
		if row.LastHealthCheckErrorMessage.Valid {
			sets["last_health_check_error_message"] = nil
		}
		if row.LastHealthCheckTraceID.Valid {
			sets["last_health_check_trace_id"] = nil
		}
	}
	if len(sets) == 0 {
		return s.UnchangedFailureStateResult(tx, row)
	}
	assignments := []string{}
	setArgs := []any{}
	for column, value := range sets {
		assignments = append(assignments, column+" = ?")
		setArgs = append(setArgs, value)
	}
	assignments = append(assignments, "config_revision = config_revision + 1", "updated_at = ?")
	setArgs = append(setArgs, accountscore.IsoMillis(in.Now))
	updateArgs := append(append([]any{}, setArgs...), row.ID, row.ConfigRevision)
	exec, err := tx.ExecContext(ctx, s.store.Bind(`UPDATE `+s.store.Table("accounts")+` SET
		`+joinStrings(assignments, ", ")+`
		WHERE id = ? AND config_revision = ? AND deleted_at IS NULL`), updateArgs...)
	if err != nil {
		return nil, err
	}
	if affected, _ := exec.RowsAffected(); affected != 1 {
		return nil, &accountscore.RevisionConflictError{Message: accountscore.RevisionConflictMessage}
	}
	restoredForDispatch := nextStatus == "active"
	if restoredForDispatch {
		if err := s.deps.AdvanceBatchDispatchRevision(ctx, tx, row.ID, newResetDispatchTransitionID(), in.Now.UnixMilli()); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &PatchFailureStateResult{
		ID:                     row.ID,
		ConfigRevision:         row.ConfigRevision + 1,
		ChangedFields:          []string{"clearFailureState"},
		Name:                   row.Name,
		OwnerSystemAccountID:   row.SystemAccountID,
		Status:                 nextStatus,
		HealthCheckRequired:    nextStatus == "pending_test",
		HealthCheckReason:      HealthCheckReasonValue(nextStatus == "pending_test"),
		RuntimeRestoreRequired: true,
	}, nil
}

func HealthCheckReasonValue(required bool) string {
	if required {
		return "activation"
	}
	return ""
}

// UnchangedFailureStateResult mirrors unchangedPatchResult: the account stays
// as-is and the reset continues with the runtime-only surfaces.
func (s *Service) UnchangedFailureStateResult(tx *sql.Tx, row PatchFailureStateRow) (*PatchFailureStateResult, error) {
	_ = tx
	return &PatchFailureStateResult{
		ID:                   row.ID,
		ConfigRevision:       row.ConfigRevision,
		ChangedFields:        []string{},
		Name:                 row.Name,
		OwnerSystemAccountID: row.SystemAccountID,
		Status:               row.Status,
	}, nil
}

// ---- authorized dispatch clear ----

type UpdateAuthorizedBindingDispatchInput struct {
	AccountID              string
	ExpectedConfigRevision int64
	Access                 accountscore.AccessScope
}

type AuthorizedDispatchResetResult struct {
	ID                     string
	ConfigRevision         int64
	ChangedFields          []string
	Name                   string
	OwnerSystemAccountID   string
	PatchStatus            *string
	PatchSchedulable       *bool
	RuntimeRestoreRequired bool
	AuthorizedBinding      *RuntimeAuthorizedBinding
}

type AuthorizedDispatchResetRow struct {
	ID                                   string
	ConfigRevision                       int64
	SystemAccountID                      string
	Name                                 string
	Status                               string
	Schedulable                          int
	AccountExpiresAt                     sql.NullString
	CooldownUntil                        sql.NullString
	LastErrorCode                        sql.NullString
	LastErrorMessage                     sql.NullString
	LastErrorTraceID                     sql.NullString
	CooldownRetestFailureCount           sql.NullInt64
	CooldownRetestObservationStartedAt   sql.NullString
	CooldownRetestGeneration             sql.NullString
	CooldownRetestLastAt                 sql.NullString
	cooldownRetestLastStatusCode         sql.NullInt64
	StreamFailureCount                   sql.NullInt64
	StreamFailureWindowStartedAt         sql.NullString
	AuthorizationInstanceSourceAccountID sql.NullString
	AuthorizationInstanceAuthorizationID sql.NullString
	AuthorizationStatus                  sql.NullString
	AuthorizationExpiresAt               sql.NullString
	AuthorizationLimitsJSON              sql.NullString
	AuthorizationEffectiveTeamID         sql.NullString
	SourceID                             sql.NullString
	SourceStatus                         sql.NullString
	SourceSchedulable                    sql.NullInt64
	SourceExpiresAt                      sql.NullString
	SourceCooldownUntil                  sql.NullString
	SourceLastErrorCode                  sql.NullString
	SourceLastErrorMessage               sql.NullString
}

type AuthorizedDispatchResetBinding struct {
	GroupID                string
	AccountAuthorizationID string
	LocalPriority          sql.NullInt64
	LocalSuperPriority     sql.NullInt64
	LocalFallback          sql.NullInt64
}

// UpdateAuthorizedBindingDispatchForReset mirrors
// updateAuthorizedAccountBindingDispatchAsync restricted to the
// clearFailureState command with runtimeResetRequireUnlocked. Returns
// (nil, nil) when the instance is out of scope or its relation is gone.
func (s *Service) UpdateAuthorizedBindingDispatchForReset(ctx context.Context, in UpdateAuthorizedBindingDispatchInput) (*AuthorizedDispatchResetResult, error) {
	if in.ExpectedConfigRevision < 1 {
		return nil, &accountscore.ValidationError{Message: "账户配置版本无效"}
	}
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	scoped := in.Access.ManageableID()
	if scoped == "" && !in.Access.CanAccessAll() {
		return nil, nil
	}
	scopeClause := ""
	args := []any{in.AccountID}
	if scoped != "" {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row AuthorizedDispatchResetRow
	err = tx.QueryRowContext(ctx, s.store.Bind(`SELECT
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
		FROM `+s.store.Table("accounts")+` accounts
		INNER JOIN `+s.store.Table("resource_authorizations")+` authorizations
			ON authorizations.id = accounts.authorization_instance_authorization_id
			AND authorizations.resource_type = 'account'
			AND authorizations.resource_id = accounts.authorization_instance_source_account_id
			AND authorizations.grantee_system_account_id = accounts.system_account_id
		LEFT JOIN `+s.store.Table("accounts")+` source_accounts
			ON source_accounts.id = accounts.authorization_instance_source_account_id
			AND source_accounts.deleted_at IS NULL
		WHERE accounts.id = ?`+scopeClause+`
			AND accounts.authorization_instance_authorization_id IS NOT NULL
			AND accounts.deleted_at IS NULL
		LIMIT 1`+s.forUpdate()), args...).Scan(
		&row.ID, &row.ConfigRevision, &row.SystemAccountID, &row.Name,
		&row.Status, &row.Schedulable, &row.AccountExpiresAt, &row.CooldownUntil,
		&row.LastErrorCode, &row.LastErrorMessage, &row.LastErrorTraceID,
		&row.CooldownRetestFailureCount, &row.CooldownRetestObservationStartedAt,
		&row.CooldownRetestGeneration, &row.CooldownRetestLastAt,
		&row.cooldownRetestLastStatusCode, &row.StreamFailureCount,
		&row.StreamFailureWindowStartedAt, &row.AuthorizationInstanceSourceAccountID,
		&row.AuthorizationInstanceAuthorizationID,
		&row.AuthorizationStatus, &row.AuthorizationExpiresAt,
		&row.AuthorizationLimitsJSON, &row.AuthorizationEffectiveTeamID,
		&row.SourceID, &row.SourceStatus, &row.SourceSchedulable,
		&row.SourceExpiresAt, &row.SourceCooldownUntil,
		&row.SourceLastErrorCode, &row.SourceLastErrorMessage)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !row.AuthorizationInstanceSourceAccountID.Valid || row.AuthorizationInstanceSourceAccountID.String == "" ||
		!row.AuthorizationInstanceAuthorizationID.Valid || row.AuthorizationInstanceAuthorizationID.String == "" {
		return nil, nil
	}
	// Latest enabled binding.
	var binding AuthorizedDispatchResetBinding
	err = tx.QueryRowContext(ctx, s.store.Bind(`SELECT group_id, account_authorization_id,
			local_priority, local_super_priority_enabled, local_fallback_enabled
		FROM `+s.store.Table("group_accounts")+`
		WHERE account_id = ?
			AND system_account_id = ?
			AND account_authorization_id = ?
			AND enabled = 1
		ORDER BY updated_at DESC, group_id ASC
		LIMIT 1`+s.forUpdate()), row.ID, row.SystemAccountID, row.AuthorizationInstanceAuthorizationID.String).
		Scan(&binding.GroupID, &binding.AccountAuthorizationID, &binding.LocalPriority,
			&binding.LocalSuperPriority, &binding.LocalFallback)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if row.ConfigRevision != in.ExpectedConfigRevision {
		return nil, &accountscore.RevisionConflictError{Message: accountscore.RevisionConflictMessage}
	}
	// runtimeResetRequireUnlocked.
	lock, err := s.findAccountLockStateRowTx(ctx, tx, row.ID)
	if err != nil {
		return nil, err
	}
	if lock.enabled && (lock.lockState == "ENGAGED" || lock.lockState == "DEAD_CONFIRMED") {
		return UnchangedAuthorizedResetResult(row, binding), nil
	}
	if row.AuthorizationStatus.Valid && (row.AuthorizationStatus.String == "revoked" || row.AuthorizationStatus.String == "returned") {
		return nil, nil
	}

	// clearFailureState implies a pending_test refusal first (Node
	// patchAuthorizedAccountDispatchInTransaction guard).
	if row.Status == "pending_test" {
		return nil, errors.New("待检查账户需等待后台健康检查通过后才能参与调度")
	}
	// allowLocalRecovery=true: only the authorization/source availability
	// gates run; the local failure states are exactly what the reset clears.
	if message, err := s.AuthorizedResetUnavailableMessage(ctx, row, binding, in.Access); err != nil {
		return nil, err
	} else if message != "" {
		return nil, errors.New(message)
	}

	currentSchedulable := row.Schedulable == 1
	nextStatus := "active"
	nextSchedulable := true
	sets := map[string]any{}
	if row.Status != nextStatus {
		sets["status"] = nextStatus
	}
	if currentSchedulable != nextSchedulable {
		sets["schedulable"] = accountscore.BoolInt(nextSchedulable)
	}
	failureStateChanged := ClearAuthorizedFailureStateColumnsForReset(sets, row)
	if len(sets) == 0 {
		return UnchangedAuthorizedResetResult(row, binding), nil
	}
	nowISO := accountscore.IsoMillis(s.store.Now())
	assignments := []string{}
	setArgs := []any{}
	for column, value := range sets {
		assignments = append(assignments, column+" = ?")
		setArgs = append(setArgs, value)
	}
	assignments = append(assignments, "config_revision = config_revision + 1", "updated_at = ?")
	setArgs = append(setArgs, nowISO)
	updateArgs := append(append([]any{}, setArgs...), row.ID, row.SystemAccountID, row.AuthorizationInstanceAuthorizationID.String, row.ConfigRevision)
	exec, err := tx.ExecContext(ctx, s.store.Bind(`UPDATE `+s.store.Table("accounts")+` SET
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
		return nil, &accountscore.RevisionConflictError{Message: accountscore.RevisionConflictMessage}
	}
	restoredForDispatch := nextStatus == "active" && (!currentSchedulable || row.Status != "active" || failureStateChanged)
	if restoredForDispatch {
		if err := s.deps.AdvanceBatchDispatchRevision(ctx, tx, row.ID, newResetDispatchTransitionID(), s.store.Now().UnixMilli()); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	changedFields := []string{"clearFailureState"}
	accountscore.SortStrings(changedFields)
	patchStatus := nextStatus
	patchSchedulable := nextSchedulable
	return &AuthorizedDispatchResetResult{
		ID:                     row.ID,
		ConfigRevision:         row.ConfigRevision + 1,
		ChangedFields:          changedFields,
		Name:                   row.Name,
		OwnerSystemAccountID:   row.SystemAccountID,
		PatchStatus:            &patchStatus,
		PatchSchedulable:       &patchSchedulable,
		RuntimeRestoreRequired: true,
		AuthorizedBinding: &RuntimeAuthorizedBinding{
			SystemAccountID:        row.SystemAccountID,
			GroupID:                binding.GroupID,
			AccountAuthorizationID: binding.AccountAuthorizationID,
		},
	}, nil
}

func UnchangedAuthorizedResetResult(row AuthorizedDispatchResetRow, binding AuthorizedDispatchResetBinding) *AuthorizedDispatchResetResult {
	return &AuthorizedDispatchResetResult{
		ID:                   row.ID,
		ConfigRevision:       row.ConfigRevision,
		ChangedFields:        []string{},
		Name:                 row.Name,
		OwnerSystemAccountID: row.SystemAccountID,
		AuthorizedBinding: &RuntimeAuthorizedBinding{
			SystemAccountID:        row.SystemAccountID,
			GroupID:                binding.GroupID,
			AccountAuthorizationID: binding.AccountAuthorizationID,
		},
	}
}

// ClearAuthorizedFailureStateColumnsForReset mirrors
// clearAuthorizedFailureStateColumns: only changed columns are written and the
// boolean reports whether any failure column actually moved.
func ClearAuthorizedFailureStateColumnsForReset(sets map[string]any, row AuthorizedDispatchResetRow) bool {
	before := len(sets)
	if row.CooldownUntil.Valid {
		sets["cooldown_until"] = nil
	}
	if row.LastErrorCode.Valid {
		sets["last_error_code"] = nil
	}
	if row.LastErrorMessage.Valid {
		sets["last_error_message"] = nil
	}
	if row.LastErrorTraceID.Valid {
		sets["last_error_trace_id"] = nil
	}
	if row.CooldownRetestFailureCount.Int64 != 0 {
		sets["cooldown_retest_failure_count"] = 0
	}
	if row.CooldownRetestObservationStartedAt.Valid {
		sets["cooldown_retest_observation_started_at"] = nil
	}
	if row.CooldownRetestGeneration.Valid {
		sets["cooldown_retest_generation"] = nil
	}
	if row.CooldownRetestLastAt.Valid {
		sets["cooldown_retest_last_at"] = nil
	}
	if row.cooldownRetestLastStatusCode.Valid {
		sets["cooldown_retest_last_status_code"] = nil
	}
	if row.StreamFailureCount.Int64 != 0 {
		sets["stream_failure_count"] = 0
	}
	if row.StreamFailureWindowStartedAt.Valid {
		sets["stream_failure_window_started_at"] = nil
	}
	return len(sets) > before
}

// AuthorizedResetUnavailableMessage mirrors authorizedDispatchUnavailableMessage
// with allowLocalRecovery=true (the clearFailureState path). The request-quota
// gate goes through the RuntimeResetEffects port.
func (s *Service) AuthorizedResetUnavailableMessage(ctx context.Context, row AuthorizedDispatchResetRow, binding AuthorizedDispatchResetBinding, access accountscore.AccessScope) (string, error) {
	now := s.store.Now()
	nowMillis := now.UnixMilli()
	if row.AuthorizationStatus.Valid && (row.AuthorizationStatus.String == "expired" ||
		(!row.AuthorizationExpiresAt.Valid && row.AuthorizationStatus.String != "expired" && IsResourceAuthorizationExpired(row.AuthorizationExpiresAt.String, now))) {
		return "授权已到期，当前账户不能调用", nil
	}
	if IsResourceAuthorizationExpired(row.AuthorizationExpiresAt.String, now) {
		return "授权已到期，当前账户不能调用", nil
	}
	if row.AuthorizationStatus.Valid && row.AuthorizationStatus.String == "paused" {
		return "授权已暂停，当前账户不能调用", nil
	}
	if row.AuthorizationStatus.Valid && (row.AuthorizationStatus.String == "revoked" || row.AuthorizationStatus.String == "returned") {
		return "授权关系已失效，当前账户不能调用", nil
	}
	if s.ResetAuthorizationQuotaExceeded(ctx, row, access) {
		return "授权额度已用完，当前账户不能调用", nil
	}
	if !row.SourceID.Valid || row.SourceID.String == "" || !row.SourceStatus.Valid || row.SourceStatus.String == "" {
		return "授权方原账户不存在或已删除，当前账户不能调用", nil
	}
	if row.SourceLastErrorCode.String == "account_expired" || accountscore.IsAccountExpired(row.SourceExpiresAt.String, now) {
		return "授权方原账户已到期，当前账户不能调用", nil
	}
	switch row.SourceStatus.String {
	case "disabled":
		return "授权方原账户已停用，当前账户不能调用", nil
	case "pending_test":
		return "授权方原账户尚未通过后台健康检查，当前账户不能调用", nil
	case "error":
		if row.SourceLastErrorMessage.Valid && row.SourceLastErrorMessage.String != "" {
			return row.SourceLastErrorMessage.String, nil
		}
		return "授权方原账户处于异常状态，当前账户不能调用", nil
	case "rate_limited":
		if row.SourceLastErrorMessage.Valid && row.SourceLastErrorMessage.String != "" {
			return row.SourceLastErrorMessage.String, nil
		}
		return "授权方原账户限流中，当前账户不能调用", nil
	case "temporary_unavailable":
		if row.SourceLastErrorMessage.Valid && row.SourceLastErrorMessage.String != "" {
			return row.SourceLastErrorMessage.String, nil
		}
		return "授权方原账户临时不可调用，当前账户不能调用", nil
	case "quality_isolated":
		return "授权方原账户因模型质量不达标已隔离，恢复前不能调用", nil
	}
	if IsFutureTimestamp(row.SourceCooldownUntil.String, now) {
		return "授权方原账户正在冷却，恢复前当前账户不能调用", nil
	}
	if row.SourceSchedulable.Valid && row.SourceSchedulable.Int64 == 0 {
		return "授权方原账户已关闭调度，当前账户不能调用", nil
	}
	if row.LastErrorCode.String == "account_expired" || accountscore.IsAccountExpired(row.AccountExpiresAt.String, now) {
		return "授权账户已到期，当前不可用", nil
	}
	if binding.GroupID == "" {
		return "授权账户需要先绑定到你的分组", nil
	}
	_ = nowMillis
	return "", nil
}

// resetSummaryQuotaExceeded resolves the authorization-quota gate for the
// authorized summary projection (Node loadAuthorizedAccountSummaryContextAsync
// authorizationQuotaExceededByAuthorization).
func (s *Service) resetSummaryQuotaExceeded(ctx context.Context, summary *ResetSummary) bool {
	effects := s.effectsOrNil()
	if effects == nil {
		return false
	}
	exceeded, err := effects.AuthorizationQuotaExceeded(ctx, AuthorizationQuotaCheckInput{
		AuthorizationID:        summary.AuthorizationID.String,
		GranteeSystemAccountID: summary.SystemAccountID,
		EffectiveSourceTeamID:  summary.AuthorizationEffectiveTeamID.String,
	})
	if err != nil {
		slog.Warn("runtime-reset 授权额度读取失败，按未超额处理",
			"event", "account_runtime_reset_authorization_quota_read_failed",
			"accountId", summary.ID, "error", err)
		return false
	}
	return exceeded
}

// ResetAuthorizationQuotaExceeded resolves the quota gate through the port.
func (s *Service) ResetAuthorizationQuotaExceeded(ctx context.Context, row AuthorizedDispatchResetRow, access accountscore.AccessScope) bool {
	effects := s.effectsOrNil()
	if effects == nil {
		return false
	}
	exceeded, err := effects.AuthorizationQuotaExceeded(ctx, AuthorizationQuotaCheckInput{
		AuthorizationID:        row.AuthorizationInstanceAuthorizationID.String,
		GranteeSystemAccountID: row.SystemAccountID,
		EffectiveSourceTeamID:  row.AuthorizationEffectiveTeamID.String,
	})
	if err != nil {
		slog.Warn("runtime-reset 授权额度读取失败，按未超额处理",
			"event", "account_runtime_reset_authorization_quota_read_failed",
			"accountId", row.ID, "error", err)
		return false
	}
	return exceeded
}

// ---- dispatch revision fence (standalone variant) ----

type ResetDispatchFenceResult struct {
	Status           string
	DispatchRevision int64
}

// AdvanceResetDispatchRevision mirrors
// advanceAccountCircuitDispatchRevision (standalone): one transaction around
// the in-transaction fence, dedupe replays stay idempotent.
func (s *Service) AdvanceResetDispatchRevision(ctx context.Context, accountID, transitionID string, nowMS int64) (ResetDispatchFenceResult, error) {
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return ResetDispatchFenceResult{}, err
	}
	defer tx.Rollback()
	// The shared in-transaction fence reports replays through a nil error;
	// distinguish by re-reading the current dispatch revision for idempotent
	// replays.
	// 缺陷现象：重放同一 transitionID 时 SELECT 按 dedupe_key 查不到既有事件，
	// 随后 INSERT 撞 UNIQUE(projection_key, dedupe_key) 报错，无法幂等返回。
	// 根因：SELECT 的 projection_key 硬编码 "account_circuit_dispatch"，与
	// INSERT 使用的 circuitProjectionKey()（circuit_control_plane.ProjectionKey
	// = "account_circuit_runtime_v1"）不一致；控制面 store.go 亦校验
	// projection_key 必须等于 ProjectionKey。最小修复：SELECT 改用同一常量。
	dedupeKey := "dispatch:" + transitionID
	var replayRevision int64
	var replayEventType, replayAccountID, replayRuntimeKey string
	err = tx.QueryRowContext(ctx, s.store.Bind(`SELECT event_type, account_id, account_runtime_key, dispatch_revision
		FROM `+s.store.Table("account_circuit_outbox")+`
		WHERE projection_key = ? AND dedupe_key = ?`), circuitProjectionKey(), dedupeKey).
		Scan(&replayEventType, &replayAccountID, &replayRuntimeKey, &replayRevision)
	if err == nil {
		if replayEventType != "dispatch_revision_changed" || replayAccountID != accountID || replayRuntimeKey != accountID {
			return ResetDispatchFenceResult{}, errors.New("账户 circuit outbox dedupe key 与既有事件身份冲突")
		}
		if err := tx.Commit(); err != nil {
			return ResetDispatchFenceResult{}, err
		}
		return ResetDispatchFenceResult{Status: "idempotent", DispatchRevision: replayRevision}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ResetDispatchFenceResult{}, err
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, s.store.Bind(`SELECT dispatch_revision FROM `+s.store.Table("accounts")+`
		WHERE id = ?`+s.forUpdate()), accountID).Scan(&revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ResetDispatchFenceResult{}, fmt.Errorf("AI 账户不存在：%s", accountID)
		}
		return ResetDispatchFenceResult{}, err
	}
	exec, err := tx.ExecContext(ctx, s.store.Bind(`UPDATE `+s.store.Table("accounts")+`
		SET dispatch_revision = dispatch_revision + 1
		WHERE id = ? AND dispatch_revision = ?`), accountID, revision)
	if err != nil {
		return ResetDispatchFenceResult{}, err
	}
	if affected, _ := exec.RowsAffected(); affected != 1 {
		return ResetDispatchFenceResult{}, errors.New("账户 dispatch revision 推进冲突：" + accountID)
	}
	revision++
	eventID, err := s.deps.CircuitEventID()
	if err != nil {
		return ResetDispatchFenceResult{}, err
	}
	if _, err := tx.ExecContext(ctx, s.store.Bind(`INSERT INTO `+s.store.Table("account_circuit_outbox")+`
		(event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
		 circuit_scope_key, incident_id, transition_id, dispatch_revision, generation,
		 ledger_revision, status, available_at_ms, attempt_count, created_at_ms, updated_at_ms)
		VALUES (?, ?, ?, 'dispatch_revision_changed', ?, ?, NULL, NULL, ?, ?, NULL, NULL, 'pending', ?, 0, ?, ?)`),
		eventID, circuitProjectionKey(), dedupeKey, accountID, accountID,
		transitionID, revision, nowMS, nowMS, nowMS); err != nil {
		return ResetDispatchFenceResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ResetDispatchFenceResult{}, err
	}
	return ResetDispatchFenceResult{Status: "applied", DispatchRevision: revision}, nil
}

// ---- shared helpers ----

type resetLockStateRow struct {
	enabled   bool
	lockState string
}

func (s *Service) findAccountLockStateRow(ctx context.Context, accountID string) (resetLockStateRow, error) {
	return s.findAccountLockStateRowTx(ctx, s.store.DB(), accountID)
}

func (s *Service) findAccountLockStateRowTx(ctx context.Context, q accountscore.Queryer, accountID string) (resetLockStateRow, error) {
	var row resetLockStateRow
	var enabled int64
	var lockState sql.NullString
	err := q.QueryRowContext(ctx, s.store.Bind(`SELECT enabled, lock_state FROM `+s.store.Table("account_lock_states")+`
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
	return "dispatch_" + accountscore.Itoa64(time.Now().UnixMilli()) + "_" + hex.EncodeToString(buf)[:8]
}

// circuitProjectionKey mirrors accountCircuitProjectionKey (the shared
// business/circuit_control_plane constant the batch fence also writes).
func circuitProjectionKey() string { return circuitcontrolplane.ProjectionKey }

// FingerprintAccountAPIKey mirrors fingerprintAccountApiKey: HMAC-SHA256 over
// the runtime secret, hex-encoded.
func FingerprintAccountAPIKey(secret, key string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

type ResetAPIKeyEntry struct {
	Fingerprint string
}

// AccountAPIKeyEntries mirrors accountApiKeyEntries (the fingerprint subset
// the transient clear needs).
func AccountAPIKeyEntries(secret string, credentials accountscore.Credentials) []ResetAPIKeyEntry {
	trimKey := func(value any) (string, bool) {
		text, ok := value.(string)
		if !ok {
			return "", false
		}
		key := TrimSpaces(text)
		return key, key != ""
	}
	var rawKeys []any
	if list, ok := credentials["api_keys"].([]any); ok && len(list) > 0 {
		rawKeys = list
	} else if single, present := credentials["api_key"]; present {
		rawKeys = []any{single}
	}
	entries := []ResetAPIKeyEntry{}
	seen := map[string]bool{}
	for _, value := range rawKeys {
		key, ok := trimKey(value)
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, ResetAPIKeyEntry{Fingerprint: FingerprintAccountAPIKey(secret, key)})
	}
	return entries
}

func TrimSpaces(value string) string {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\t' || value[start] == '\n' || value[start] == '\r') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t' || value[end-1] == '\n' || value[end-1] == '\r') {
		end--
	}
	return value[start:end]
}

// ClearResetAPIKeyTransientStates mirrors the api key transient-clear block.
func (s *Service) ClearResetAPIKeyTransientStates(ctx context.Context, effects RuntimeResetEffects, accountID string, credentials accountscore.Credentials) (int, error) {
	entries := AccountAPIKeyEntries(s.store.Secret(), credentials)
	fingerprints := make([]string, 0, len(entries))
	for _, entry := range entries {
		fingerprints = append(fingerprints, entry.Fingerprint)
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
		state, ok := stateByFingerprint[entry.Fingerprint]
		var generation *int64
		if ok && state.HasGeneration {
			generation = &state.TransientGeneration
		}
		didClear := effects.ClearAPIKeyFailureGuard(accountID, entry.Fingerprint, generation)
		// A Redis transient record may already have expired from the dispatch
		// projection; the generation is still the authoritative CAS fence and
		// is tombstoned when it exists.
		if generation != nil {
			if transientCleared, err := effects.ClearAPIKeyTransientFailure(ctx, accountID, entry.Fingerprint, generation); err != nil {
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

// FindResetSummary mirrors findAccountSummaryAsync for the reset projection:
// owner rows through the owner scope, authorized instances through the
// authorization join.
func (s *Service) FindResetSummary(ctx context.Context, accountID string, access accountscore.AccessScope) (*ResetSummary, error) {
	id := TrimSpaces(accountID)
	if id == "" {
		return nil, nil
	}
	scoped := access.ManageableID()
	if scoped == "" && !access.CanAccessAll() {
		return nil, nil
	}
	summary, err := s.findOwnerResetSummary(ctx, id, scoped)
	if err != nil {
		return nil, err
	}
	if summary != nil {
		return summary, nil
	}
	if scoped == "" && !access.CanAccessAll() {
		return nil, nil
	}
	return s.findAuthorizedResetSummary(ctx, id, scoped)
}

func (s *Service) findOwnerResetSummary(ctx context.Context, id, scoped string) (*ResetSummary, error) {
	scopeClause := ""
	args := []any{id}
	if scoped != "" {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row ResetSummary
	var schedulable int64
	var credentialsEncrypted sql.NullString
	err := s.store.DB().QueryRowContext(ctx, s.store.Bind(`SELECT accounts.id, accounts.config_revision, accounts.dispatch_revision,
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
		FROM `+s.store.Table("accounts")+` accounts
		WHERE accounts.id = ?
			AND accounts.deleted_at IS NULL
			AND accounts.authorization_instance_authorization_id IS NULL`+scopeClause+`
		LIMIT 1`), args...).Scan(
		&row.ID, &row.ConfigRevision, &row.DispatchRevision,
		&row.Name, &row.AccountType, &row.Status, &schedulable,
		&row.ProviderCode, &row.ProviderProtocolProfileID, &row.ProtocolCode,
		&row.ProtocolVersion, &row.ClientCompatibility, &row.SystemAccountID,
		&credentialsEncrypted, &row.AccountExpiresAt, &row.CooldownUntil,
		&row.LastErrorCode, &row.LastErrorMessage, &row.LastErrorTraceID,
		&row.LastHealthCheckAt, &row.LastHealthCheckErrorCode,
		&row.LastHealthCheckErrorMessage, &row.CooldownRetestFailureCount,
		&row.CooldownRetestObservationStartedAt, &row.CooldownRetestGeneration,
		&row.CooldownRetestLastAt, &row.HealthCheckFailureCount,
		&row.HealthCheckFailureStartedAt, &row.StreamFailureCount,
		&row.StreamFailureWindowStartedAt,
		&row.AuthorizationID,
		&row.SourceAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.Schedulable = schedulable == 1
	row.AccessType = "owner"
	row.Credentials = accountscore.Credentials{}
	if TrimSpaces(credentialsEncrypted.String) != "" {
		if err := accountscore.DecryptJSON(s.store.Secret(), credentialsEncrypted.String, &row.Credentials); err != nil {
			row.Credentials = accountscore.Credentials{}
		}
	}
	return &row, nil
}

func (s *Service) findAuthorizedResetSummary(ctx context.Context, id, scoped string) (*ResetSummary, error) {
	scopeClause := ""
	args := []any{id}
	if scoped != "" {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row ResetSummary
	var schedulable int64
	var credentialsEncrypted sql.NullString
	err := s.store.DB().QueryRowContext(ctx, s.store.Bind(`SELECT accounts.id, accounts.config_revision, accounts.dispatch_revision,
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
		FROM `+s.store.Table("accounts")+` accounts
		INNER JOIN `+s.store.Table("resource_authorizations")+` authorizations
			ON authorizations.id = accounts.authorization_instance_authorization_id
		LEFT JOIN `+s.store.Table("accounts")+` source_accounts
			ON source_accounts.id = accounts.authorization_instance_source_account_id
			AND source_accounts.deleted_at IS NULL
		LEFT JOIN `+s.store.Table("group_accounts")+` group_bindings
			ON group_bindings.account_id = accounts.id
			AND group_bindings.system_account_id = accounts.system_account_id
			AND group_bindings.enabled = 1
		WHERE accounts.id = ?`+scopeClause+`
			AND accounts.deleted_at IS NULL
			AND accounts.authorization_instance_authorization_id IS NOT NULL
			AND authorizations.status IN ('active', 'paused', 'expired')
		ORDER BY group_bindings.updated_at DESC
		LIMIT 1`), args...).Scan(
		&row.ID, &row.ConfigRevision, &row.DispatchRevision,
		&row.Name, &row.AccountType, &row.Status, &schedulable,
		&row.ProviderCode, &row.ProviderProtocolProfileID, &row.ProtocolCode,
		&row.ProtocolVersion, &row.ClientCompatibility, &row.SystemAccountID,
		&credentialsEncrypted, &row.AccountExpiresAt, &row.CooldownUntil,
		&row.LastErrorCode, &row.LastErrorMessage, &row.LastErrorTraceID,
		&row.LastHealthCheckAt, &row.LastHealthCheckErrorCode,
		&row.LastHealthCheckErrorMessage, &row.CooldownRetestFailureCount,
		&row.CooldownRetestObservationStartedAt, &row.CooldownRetestGeneration,
		&row.CooldownRetestLastAt, &row.HealthCheckFailureCount,
		&row.HealthCheckFailureStartedAt, &row.StreamFailureCount,
		&row.StreamFailureWindowStartedAt,
		&row.AuthorizationID,
		&row.SourceAccountID,
		&row.AuthorizationStatus,
		&row.AuthorizationExpiresAt,
		&row.AuthorizationEffectiveTeamID,
		&row.SourceID,
		&row.SourceStatus,
		&row.SourceSchedulable,
		&row.SourceExpiresAt,
		&row.SourceLastErrorCode,
		&row.SourceLastErrorMessage,
		&row.SourceCooldownUntil,
		&row.BindingSystemAccountID,
		&row.BoundGroupID,
		&row.BoundGroupAuthorizationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.Schedulable = schedulable == 1
	row.AccessType = "authorized"
	row.Credentials = accountscore.Credentials{}
	if TrimSpaces(credentialsEncrypted.String) != "" {
		if err := accountscore.DecryptJSON(s.store.Secret(), credentialsEncrypted.String, &row.Credentials); err != nil {
			row.Credentials = accountscore.Credentials{}
		}
	}
	row.AuthorizationQuotaExceeded = s.resetSummaryQuotaExceeded(ctx, &row)
	return &row, nil
}

// BindingIsAuthorizationUnavailable mirrors groupBindStatus ===
// 'authorization_unavailable': the binding row pins a different authorization
// id than the instance stamp.
func BindingIsAuthorizationUnavailable(summary *ResetSummary) bool {
	if !summary.BoundGroupID.Valid || summary.BoundGroupID.String == "" {
		return false
	}
	if !summary.BoundGroupAuthorizationID.Valid || summary.BoundGroupAuthorizationID.String == "" {
		return false
	}
	if !summary.AuthorizationID.Valid {
		return true
	}
	return summary.BoundGroupAuthorizationID.String != summary.AuthorizationID.String
}

// ResetEffectiveAvailability mirrors accountEffectiveAvailability for the
// reset response (apiKeyPoolAvailability goes through the port; the
// runtimeAvailability overlay is never hydrated by findAccountSummaryAsync in
// Node either).
func (s *Service) ResetEffectiveAvailability(ctx context.Context, account *ResetSummary, now time.Time) bool {
	if account.AccessType == "authorized" {
		// authorizedBindingAvailability.
		if !account.BoundGroupID.Valid || account.BoundGroupID.String == "" {
			return false
		}
		if BindingIsAuthorizationUnavailable(account) {
			return false
		}
		// authorizationAvailability.
		if account.AuthorizationStatus.Valid {
			switch account.AuthorizationStatus.String {
			case "expired":
				return false
			case "paused":
				return false
			case "revoked", "returned":
				return false
			}
		}
		if IsResourceAuthorizationExpired(account.AuthorizationExpiresAt.String, now) {
			return false
		}
		if account.AuthorizationQuotaExceeded {
			return false
		}
		// sourceAccountAvailability.
		if !account.SourceAccountID.Valid || account.SourceAccountID.String == "" ||
			!account.SourceStatus.Valid || account.SourceStatus.String == "" {
			return false
		}
		if account.SourceLastErrorCode.String == "account_expired" || accountscore.IsAccountExpired(account.SourceExpiresAt.String, now) {
			return false
		}
		switch account.SourceStatus.String {
		case "disabled", "pending_test", "error", "rate_limited", "temporary_unavailable", "quality_isolated":
			return false
		}
		if IsFutureTimestamp(account.SourceCooldownUntil.String, now) {
			return false
		}
		if account.SourceSchedulable.Valid && account.SourceSchedulable.Int64 == 0 {
			return false
		}
	}
	// instanceAccountAvailability.
	if account.LastErrorCode.String == "account_expired" || accountscore.IsAccountExpired(account.AccountExpiresAt.String, now) {
		return false
	}
	switch account.Status {
	case "disabled", "pending_test", "error", "rate_limited", "temporary_unavailable", "quality_isolated":
		return false
	}
	if IsFutureTimestamp(account.CooldownUntil.String, now) {
		return false
	}
	if !account.Schedulable {
		return false
	}
	// apiKeyPoolAvailability via the port (allUnavailable).
	if account.AccessType != "authorized" && account.AccountType == "api_key" {
		if effects := s.effectsOrNil(); effects != nil {
			if allUnavailable, err := effects.APIKeyPoolAllUnavailable(ctx, account.ID); err == nil && allUnavailable {
				return false
			}
		}
	}
	return true
}

// IsFutureTimestamp mirrors IsFutureTimestamp.
func IsFutureTimestamp(value string, now time.Time) bool {
	if TrimSpaces(value) == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.UnixMilli() > now.UnixMilli()
}

// IsResourceAuthorizationExpired mirrors IsResourceAuthorizationExpired.
func IsResourceAuthorizationExpired(expiresAt string, now time.Time) bool {
	if TrimSpaces(expiresAt) == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return false
	}
	return parsed.UnixMilli() <= now.UnixMilli()
}

// IsExplicitAccountErrorPolicyCooldown mirrors
// IsExplicitAccountErrorPolicyCooldown (account-runtime-provenance.ts).
func IsExplicitAccountErrorPolicyCooldown(errorCode, errorMessage string) bool {
	if errorCode == "explicit_account_error_policy_cooldown" || errorCode == "system_quota_explicit_reset" {
		return true
	}
	if TrimSpaces(errorCode) != "" {
		return false
	}
	const legacyPrefix = "账户错误策略「"
	const systemQuotaPrefix = "系统继承错误策略「"
	return startsWith(errorMessage, legacyPrefix) || startsWith(errorMessage, systemQuotaPrefix)
}

func startsWith(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}

// ResetHasPersistentFailureState mirrors hasPersistentFailureState.
func ResetHasPersistentFailureState(account *ResetSummary) bool {
	return account.Status == "error" ||
		account.Status == "rate_limited" ||
		account.Status == "temporary_unavailable" ||
		account.CooldownUntil.Valid ||
		account.LastErrorCode.Valid ||
		account.LastErrorMessage.Valid ||
		account.LastErrorTraceID.Valid ||
		account.CooldownRetestFailureCount > 0 ||
		account.CooldownRetestObservationStartedAt.Valid ||
		account.CooldownRetestGeneration.Valid ||
		account.CooldownRetestLastAt.Valid ||
		account.HealthCheckFailureCount > 0 ||
		account.HealthCheckFailureStartedAt.Valid ||
		account.LastHealthCheckErrorCode.Valid ||
		account.LastHealthCheckErrorMessage.Valid ||
		account.StreamFailureCount > 0 ||
		account.StreamFailureWindowStartedAt.Valid
}

func joinStrings(values []string, separator string) string {
	return strings.Join(values, separator)
}
