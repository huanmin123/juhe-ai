package proberepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountquality"
)

// 本文件移植：
//   - mark_account_precheck_temporary_unavailable（db-service-handlers 的
//     precheckTemporaryUnavailableSkipReason 顺序 + markAccountTemporaryUnavailable
//     的 dispatch_revision/status 围栏写入）；
//   - account_api_key_runtime_states 的到期候选 claim
//     （listAccountApiKeyRuntimeStatesDueForProbe + claimAccountApiKeyRuntimeProbeCandidates）；
//   - recordAccountApiKeyRuntimeSuccess / recordAccountApiKeyRuntimeFailure /
//     deferAccountApiKeyRuntimeProbe（fence + config_revision CAS）。

// precheck 与候选扫描常量（Node account-api-key-runtime-state.repository.ts）。
const (
	initialProbeBackoffSeconds = 3
	maxProbeBackoffSeconds     = 60 * 60
	// probeClaimLeaseSeconds 对齐 Node probeClaimLeaseSeconds = 600。
	probeClaimLeaseSeconds = 10 * time.Minute
	// probeCandidateScanLimit 对齐 Node
	// runtimeConfig.background.accountApiKeyProbeCandidateScanLimit（默认 10_000）。
	probeCandidateScanLimit = 10_000
)

var probeCandidateStatuses = []string{"unverified", "temporary_unavailable", "rate_limited"}

// MarkPrecheckTemporaryUnavailable 实现 accountquality.PrecheckMutation。
func (s *Store) MarkPrecheckTemporaryUnavailable(ctx context.Context, input accountquality.PrecheckMutationInput) (accountquality.PrecheckMutationResult, error) {
	startedAtMS := int64(0)
	if text := strings.TrimSpace(input.PrecheckStartedAt); text != "" {
		parsed, err := instantMS(text)
		if err != nil {
			return skipped("invalid_precheck_fence"), nil
		}
		startedAtMS = parsed
	} else {
		return skipped("invalid_precheck_fence"), nil
	}
	if input.ExpectedDispatchRevision < 1 {
		return skipped("invalid_precheck_fence"), nil
	}
	state, err := s.loadPrecheckState(ctx, input.AccountID)
	if err != nil {
		return accountquality.PrecheckMutationResult{}, err
	}
	if state == nil {
		return skipped("account_missing"), nil
	}
	if state.status == "disabled" || state.status == "error" {
		return skipped("hard_unavailable"), nil
	}
	if state.dispatchRevision != input.ExpectedDispatchRevision {
		return skipped("stale_dispatch_revision"), nil
	}
	if state.status != input.ExpectedStatus {
		return skipped("stale_account_status"), nil
	}
	if state.lastHealthSuccessAt != "" {
		healthMS, err := instantMS(state.lastHealthSuccessAt)
		if err != nil {
			return skipped("invalid_runtime_state"), nil
		}
		if healthMS >= startedAtMS {
			return skipped("newer_health_success"), nil
		}
	}
	if state.updatedAt != "" {
		updatedMS, err := instantMS(state.updatedAt)
		if err != nil {
			return skipped("invalid_runtime_state"), nil
		}
		if updatedMS > startedAtMS && state.updatedAt != state.lastUsedAt {
			return skipped("stale_account_updated"), nil
		}
	}
	query := fmt.Sprintf(`
    UPDATE %s
    SET status = 'temporary_unavailable',
        last_error_code = 'precheck_temporary_unavailable',
        last_error_message = ?,
        cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = NULL,
        cooldown_retest_generation = NULL,
        cooldown_retest_last_at = NULL,
        cooldown_retest_last_status_code = NULL,
        stream_failure_count = 0,
        stream_failure_window_started_at = NULL,
        dispatch_revision = dispatch_revision + 1,
        updated_at = ?
    WHERE id = ?
      AND deleted_at IS NULL
      AND dispatch_revision = ?
      AND status = ?
  `, s.table("accounts"))
	result, err := s.db.ExecContext(ctx, query,
		input.Reason, s.timeParam(s.now()),
		input.AccountID, input.ExpectedDispatchRevision, input.ExpectedStatus)
	if err != nil {
		return accountquality.PrecheckMutationResult{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return accountquality.PrecheckMutationResult{}, err
	}
	if changed > 0 {
		return accountquality.PrecheckMutationResult{Updated: true}, nil
	}
	return skipped("stale_dispatch_revision"), nil
}

type precheckState struct {
	status              string
	dispatchRevision    int64
	lastHealthSuccessAt string
	updatedAt           string
	lastUsedAt          string
}

func (s *Store) loadPrecheckState(ctx context.Context, accountID string) (*precheckState, error) {
	query := fmt.Sprintf(`
    SELECT status, dispatch_revision, last_health_success_at, updated_at, last_used_at
    FROM %s WHERE id = ? AND deleted_at IS NULL LIMIT 1
  `, s.table("accounts"))
	var (
		status              sql.NullString
		dispatchRevision    sql.NullInt64
		lastHealthSuccessAt sql.NullString
		updatedAt           sql.NullString
		lastUsedAt          sql.NullString
	)
	if err := s.db.QueryRowContext(ctx, query, accountID).
		Scan(&status, &dispatchRevision, &lastHealthSuccessAt, &updatedAt, &lastUsedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &precheckState{
		status:              status.String,
		dispatchRevision:    dispatchRevision.Int64,
		lastHealthSuccessAt: lastHealthSuccessAt.String,
		updatedAt:           updatedAt.String,
		lastUsedAt:          lastUsedAt.String,
	}, nil
}

func skipped(reason string) accountquality.PrecheckMutationResult {
	return accountquality.PrecheckMutationResult{Updated: false, SkippedReason: reason}
}

// ListDueForProbe 实现 accountquality.CooldownCandidateSource。
func (s *Store) ListDueForProbe(ctx context.Context, limit int) ([]accountquality.CooldownProbeCandidate, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	now := s.now().UTC().Format(rfc3339Milli)
	query := fmt.Sprintf(`
    SELECT states.account_id, states.key_fingerprint, states.key_index, states.status, states.next_probe_at,
      states.updated_at, states.recovery_started_at, states.last_error_code,
      accounts.name AS account_name, accounts.provider_code, accounts.protocol_code, accounts.protocol_version,
      accounts.type, accounts.credentials_encrypted, accounts.config_revision,
      states.probe_claim_token, states.probe_claimed_until
    FROM %s states
    JOIN %s accounts ON accounts.id = states.account_id
    WHERE states.status IN ('unverified', 'temporary_unavailable', 'rate_limited')
      AND states.next_probe_at IS NOT NULL
      AND states.next_probe_at <= ?
      AND (states.probe_claimed_until IS NULL OR states.probe_claimed_until <= ?)
      AND accounts.deleted_at IS NULL
      AND accounts.status IN ('active', 'rate_limited', 'temporary_unavailable')
      AND accounts.schedulable = 1
      AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
    ORDER BY states.next_probe_at ASC, states.updated_at ASC, states.account_id ASC, states.key_index ASC
    LIMIT ?
  `, s.table("account_api_key_runtime_states"), s.table("accounts"))
	rows, err := s.db.QueryContext(ctx, query, s.instantParam(now), s.instantParam(now), s.instantParam(now), probeCandidateScanLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var scanned []accountquality.CooldownProbeCandidate
	for rows.Next() {
		var (
			accountID, keyFingerprint, status, nextProbeAt, stateUpdatedAt sql.NullString
			keyIndex                                                       sql.NullInt64
			recoveryStartedAt, lastErrorCode                               sql.NullString
			accountName, providerCode, protocolCode, protocolVersion       sql.NullString
			accountType, credentialsEncrypted                              sql.NullString
			configRevision                                                 sql.NullInt64
			probeClaimToken, probeClaimedUntil                             sql.NullString
		)
		if err := rows.Scan(&accountID, &keyFingerprint, &keyIndex, &status, &nextProbeAt,
			&stateUpdatedAt, &recoveryStartedAt, &lastErrorCode,
			&accountName, &providerCode, &protocolCode, &protocolVersion,
			&accountType, &credentialsEncrypted, &configRevision,
			&probeClaimToken, &probeClaimedUntil); err != nil {
			return nil, err
		}
		credentials, err := s.DecryptCredentials(credentialsEncrypted.String)
		if err != nil {
			continue
		}
		if !s.IsAccountAPIKeyPoolIsolationEnabled(providerCode.String, protocolCode.String, protocolVersion.String, accountType.String, credentials) {
			continue
		}
		var matched *KeyEntry
		entries := s.AccountAPIKeyEntries(credentials)
		for index := range entries {
			if entries[index].Fingerprint == keyFingerprint.String {
				matched = &entries[index]
				break
			}
		}
		if matched == nil {
			continue
		}
		if !configRevision.Valid || configRevision.Int64 < 1 {
			continue
		}
		keyIndexValue := matched.Index
		if keyIndex.Valid {
			keyIndexValue = int(keyIndex.Int64)
		}
		scanned = append(scanned, accountquality.CooldownProbeCandidate{
			AccountID:             accountID.String,
			AccountName:           accountName.String,
			KeyFingerprint:        keyFingerprint.String,
			KeyIndex:              keyIndexValue,
			APIKey:                matched.Key,
			Status:                status.String,
			NextProbeAt:           nextProbeAt.String,
			StateUpdatedAt:        stateUpdatedAt.String,
			AccountConfigRevision: configRevision.Int64,
			ProbeClaimToken:       probeClaimToken.String,
			ProbeClaimedUntil:     probeClaimedUntil.String,
			RecoveryStartedAt:     recoveryStartedAt.String,
			LastErrorCode:         lastErrorCode.String,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.claimProbeCandidates(ctx, scanned, limit, now)
}

func nowText(value string) string { return value }

// claimProbeCandidates 等价 claimAccountApiKeyRuntimeProbeCandidates：逐条 CAS
// 写入 claim token/租约，竞争失败的条目丢弃。
func (s *Store) claimProbeCandidates(ctx context.Context, candidates []accountquality.CooldownProbeCandidate, limit int, now string) ([]accountquality.CooldownProbeCandidate, error) {
	claimedUntilTime, err := time.Parse(time.RFC3339Nano, now)
	if err != nil {
		return nil, err
	}
	claimedUntil := claimedUntilTime.Add(probeClaimLeaseSeconds).UTC().Format(rfc3339Milli)
	claimed := make([]accountquality.CooldownProbeCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if len(claimed) >= limit {
			break
		}
		token := "account_api_key_probe_claim_" + randomToken(12)
		query := fmt.Sprintf(`
      UPDATE %s
      SET probe_claim_token = ?, probe_claimed_until = ?
      WHERE account_id = ?
        AND key_fingerprint = ?
        AND status = ?
        AND next_probe_at = ?
        AND (probe_claimed_until IS NULL OR probe_claimed_until <= ?)
    `, s.table("account_api_key_runtime_states"))
		result, err := s.db.ExecContext(ctx, query,
			token, claimedUntil,
			candidate.AccountID, candidate.KeyFingerprint, candidate.Status, candidate.NextProbeAt, now)
		if err != nil {
			return nil, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			continue
		}
		candidate.ProbeClaimToken = token
		candidate.ProbeClaimedUntil = claimedUntil
		claimed = append(claimed, candidate)
	}
	return claimed, nil
}

func randomToken(length int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buffer := make([]byte, length)
	for index := range buffer {
		buffer[index] = alphabet[rand.Intn(len(alphabet))]
	}
	return string(buffer)
}

// RecordKeySuccess 实现 accountquality.CooldownMutation（record_account_api_key_success）。
func (s *Store) RecordKeySuccess(ctx context.Context, input accountquality.KeySuccessInput) (accountquality.KeyMutationResult, error) {
	if input.Expected.Status == "error" {
		return changedFalse("manual_restore_required"), nil
	}
	target, fence, err := s.resolveTarget(ctx, input.AccountID, input.KeyFingerprint, input.KeyIndex, input.Expected)
	if err != nil {
		return accountquality.KeyMutationResult{}, err
	}
	if target == nil {
		return changedFalse("not_api_key_pool_account"), nil
	}
	if fence.invalidReason != "" {
		return changedFalse(fence.invalidReason), nil
	}
	now := s.now().UTC().Format(rfc3339Milli)
	observedAt := normalizeObservedAt(input.ObservedAt, now)
	if fence.provided {
		query := fmt.Sprintf(`
      UPDATE %s
      SET system_account_id = ?, key_index = ?, status = 'active',
          consecutive_failures = 0, success_count = success_count + 1,
          cooldown_until = NULL, next_probe_at = NULL, probe_backoff_seconds = 0,
          recovery_started_at = NULL,
          last_attempt_at = ?, last_success_at = ?,
          last_error_code = NULL, last_error_message = NULL, last_trace_id = NULL,
          probe_claim_token = NULL, probe_claimed_until = NULL,
          updated_at = ?
      WHERE account_id = ? AND key_fingerprint = ?
        AND status NOT IN ('disabled', 'error')
        AND (last_attempt_at IS NULL OR last_attempt_at <= ?)%s%s
    `, s.table("account_api_key_runtime_states"), fence.sql, s.configFenceSQL(input.Expected.AccountConfigRevision))
		params := []any{
			target.systemAccountID, target.keyIndex, observedAt, observedAt, now,
			target.accountID, target.keyFingerprint, observedAt,
		}
		params = append(params, fence.params...)
		params = append(params, s.configFenceParams(input.Expected.AccountConfigRevision)...)
		result, err := s.db.ExecContext(ctx, query, params...)
		if err != nil {
			return accountquality.KeyMutationResult{}, err
		}
		return rowsToResult(result, fence.provided)
	}
	query := fmt.Sprintf(`
    INSERT INTO %s (
      id, system_account_id, account_id, key_fingerprint, key_index,
      status, failure_count, consecutive_failures, success_count,
      cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
      last_attempt_at, last_success_at, last_error_code, last_error_message,
      last_trace_id, created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, 'active', 0, 0, 1, NULL, NULL, 0, NULL, ?, ?, NULL, NULL, NULL, ?, ?)
    ON CONFLICT(account_id, key_fingerprint) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      key_index = excluded.key_index,
      status = 'active',
      consecutive_failures = 0,
      success_count = account_api_key_runtime_states.success_count + 1,
      cooldown_until = NULL,
      next_probe_at = NULL,
      probe_backoff_seconds = 0,
      recovery_started_at = NULL,
      last_attempt_at = excluded.last_attempt_at,
      last_success_at = excluded.last_success_at,
      last_error_code = NULL,
      last_error_message = NULL,
      last_trace_id = NULL,
      probe_claim_token = NULL,
      probe_claimed_until = NULL,
      updated_at = excluded.updated_at
    WHERE account_api_key_runtime_states.status NOT IN ('disabled', 'error')
      AND (account_api_key_runtime_states.last_attempt_at IS NULL OR account_api_key_runtime_states.last_attempt_at <= excluded.last_attempt_at)
  `, s.table("account_api_key_runtime_states"))
	result, err := s.db.ExecContext(ctx, query,
		"account_api_key_runtime_state_"+randomToken(16), target.systemAccountID, target.accountID, target.keyFingerprint, target.keyIndex,
		observedAt, observedAt, now, now)
	if err != nil {
		return accountquality.KeyMutationResult{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return accountquality.KeyMutationResult{}, err
	}
	return accountquality.KeyMutationResult{Changed: changed > 0}, nil
}

// RecordKeyFailure 实现 accountquality.CooldownMutation（record_account_api_key_failure）。
func (s *Store) RecordKeyFailure(ctx context.Context, input accountquality.KeyFailureInput) (accountquality.KeyMutationResult, error) {
	target, fence, err := s.resolveTarget(ctx, input.AccountID, input.KeyFingerprint, input.KeyIndex, input.Expected)
	if err != nil {
		return accountquality.KeyMutationResult{}, err
	}
	if target == nil {
		return changedFalse("not_api_key_pool_account"), nil
	}
	if fence.invalidReason != "" {
		return changedFalse(fence.invalidReason), nil
	}
	existing, err := s.loadRuntimeRow(ctx, target.accountID, target.keyFingerprint)
	if err != nil {
		return accountquality.KeyMutationResult{}, err
	}
	if existing != nil && existing.status == "disabled" {
		return changedFalse("key_disabled"), nil
	}
	if fence.provided && existing == nil {
		return changedFalse("stale_probe_state"), nil
	}
	now := s.now().UTC().Format(rfc3339Milli)
	observedAt := normalizeObservedAt(input.ObservedAt, now)
	nextBackoff := nextProbeBackoffSeconds(existing.backoffSeconds())
	status := normalizeFailureStatus(input.Status)
	cooldownUntil := strings.TrimSpace(input.CooldownUntil)
	nextProbeAt := passiveProbeRetryAt(nextBackoff, s.now)
	if cooldownUntil != "" && status == "rate_limited" {
		nextProbeAt = passiveProbeNotBeforeAt(cooldownUntil, s.now)
	}
	persistedCooldownUntil := cooldownUntil
	if persistedCooldownUntil == "" {
		persistedCooldownUntil = nextProbeAt
	}
	errorCode := input.ErrorCode
	if errorCode == "" {
		switch input.QuotaRecoveryMode {
		case "explicit_reset":
			errorCode = accountquality.QuotaRecoveryExplicitErrorCode
		case "generic":
			errorCode = accountquality.QuotaRecoveryGenericErrorCode
		default:
			errorCode = fmt.Sprintf("http_%d", input.StatusCode)
		}
	}
	errorMessage := sanitizeRuntimeMessage(firstNonEmptyStr(input.ErrorMessage, fmt.Sprintf("上游返回 HTTP %d", input.StatusCode)))
	recoveryStartedAt := quotaRecoveryStartedAt(input.QuotaRecoveryMode, existing, observedAt, input.BreakQuotaRecoveryWindow)

	genericGuard := `
      AND NOT (
        ? = 'generic'
        AND last_error_code IN (?, ?)
        AND cooldown_until IS NOT NULL
        AND cooldown_until > ?
      )`
	genericParams := []any{input.QuotaRecoveryMode, accountquality.QuotaRecoveryExplicitErrorCode, accountquality.QuotaRecoveryGenericErrorCode, observedAt}
	if s.postgres {
		genericGuard = `
      AND NOT (
        ? = 'generic'
        AND last_error_code IN (?, ?)
        AND cooldown_until IS NOT NULL
        AND cooldown_until::timestamptz > ?::timestamptz
      )`
	}
	recoverySQL := "COALESCE(recovery_started_at, ?)"
	recoveryParams := []any{now}
	switch {
	case input.BreakQuotaRecoveryWindow:
		recoverySQL = "?"
		recoveryParams = []any{recoveryStartedAt}
	case input.QuotaRecoveryMode != "":
		recoverySQL = "?"
		recoveryParams = []any{recoveryStartedAt}
	}

	if existing != nil {
		query := fmt.Sprintf(`
      UPDATE %s
      SET system_account_id = ?, key_index = ?, status = ?,
          failure_count = failure_count + 1, consecutive_failures = consecutive_failures + 1,
          cooldown_until = ?, next_probe_at = ?, probe_backoff_seconds = ?,
          recovery_started_at = %s,
          last_attempt_at = ?, last_failure_at = ?, last_error_code = ?, last_error_message = ?, last_trace_id = ?,
          probe_claim_token = NULL, probe_claimed_until = NULL,
          updated_at = ?
      WHERE account_id = ? AND key_fingerprint = ?
        AND status <> 'disabled'
        AND (last_attempt_at IS NULL OR last_attempt_at < ?)%s%s%s
    `, s.table("account_api_key_runtime_states"), recoverySQL, genericGuard, fence.sql, s.configFenceSQL(input.Expected.AccountConfigRevision))
		params := []any{
			target.systemAccountID, target.keyIndex, status,
			persistedCooldownUntil, nextProbeAt, nextBackoff,
		}
		params = append(params, recoveryParams...)
		params = append(params,
			observedAt, observedAt, errorCode, errorMessage, normalizeTraceID(input.TraceID), now,
			target.accountID, target.keyFingerprint, observedAt)
		params = append(params, genericParams...)
		params = append(params, fence.params...)
		params = append(params, s.configFenceParams(input.Expected.AccountConfigRevision)...)
		result, err := s.db.ExecContext(ctx, query, params...)
		if err != nil {
			return accountquality.KeyMutationResult{}, err
		}
		return rowsToResult(result, true)
	}
	query := fmt.Sprintf(`
    INSERT INTO %s (
      id, system_account_id, account_id, key_fingerprint, key_index,
      status, failure_count, consecutive_failures, success_count,
      cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
      last_attempt_at, last_failure_at, last_error_code, last_error_message,
      last_trace_id, created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, 1, 1, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `, s.table("account_api_key_runtime_states"))
	result, err := s.db.ExecContext(ctx, query,
		"account_api_key_runtime_state_"+randomToken(16), target.systemAccountID, target.accountID, target.keyFingerprint, target.keyIndex,
		status, persistedCooldownUntil, nextProbeAt, nextBackoff, recoveryStartedAt,
		observedAt, observedAt, errorCode, errorMessage, normalizeTraceID(input.TraceID), now, now)
	if err != nil {
		return accountquality.KeyMutationResult{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return accountquality.KeyMutationResult{}, err
	}
	return accountquality.KeyMutationResult{Changed: changed > 0}, nil
}

// DeferKeyProbe 实现 accountquality.CooldownMutation（defer_account_api_key_probe）。
func (s *Store) DeferKeyProbe(ctx context.Context, input accountquality.KeyDeferInput) (accountquality.KeyMutationResult, error) {
	target, fence, err := s.resolveTarget(ctx, input.AccountID, input.KeyFingerprint, input.KeyIndex, input.Expected)
	if err != nil {
		return accountquality.KeyMutationResult{}, err
	}
	if target == nil {
		return changedFalse("not_api_key_pool_account"), nil
	}
	if strings.TrimSpace(input.Expected.NextProbeAt) == "" {
		return changedFalse("missing_expected_probe_at"), nil
	}
	if fence.invalidReason != "" {
		return changedFalse(fence.invalidReason), nil
	}
	now := s.now().UTC().Format(rfc3339Milli)
	observedAt := normalizeObservedAt(input.ObservedAt, now)
	nextProbeAt := passiveProbeRetryAt(normalizeProbeDeferSeconds(input.DelaySeconds), s.now)
	recoverySQL := "recovery_started_at"
	if input.BreakQuotaRecoveryWindow {
		recoverySQL = "NULL"
	}
	query := fmt.Sprintf(`
    UPDATE %s
    SET next_probe_at = ?, last_attempt_at = ?,
        probe_claim_token = NULL, probe_claimed_until = NULL,
        recovery_started_at = %s,
        updated_at = ?
    WHERE account_id = ? AND key_fingerprint = ?
      AND (last_attempt_at IS NULL OR last_attempt_at <= ?)%s%s
  `, s.table("account_api_key_runtime_states"), recoverySQL, fence.sql, s.configFenceSQL(input.Expected.AccountConfigRevision))
	params := []any{nextProbeAt, observedAt, now, target.accountID, target.keyFingerprint, observedAt}
	params = append(params, fence.params...)
	params = append(params, s.configFenceParams(input.Expected.AccountConfigRevision)...)
	result, err := s.db.ExecContext(ctx, query, params...)
	if err != nil {
		return accountquality.KeyMutationResult{}, err
	}
	return rowsToResult(result, true)
}

// ---- 共享辅助 ----

type runtimeTarget struct {
	systemAccountID string
	accountID       string
	keyFingerprint  string
	keyIndex        int
}

type probeFence struct {
	provided      bool
	sql           string
	params        []any
	invalidReason string
}

// resolveTarget 等价 accountApiKeyRuntimeTarget + expectedProbeStateFence 的
// 输入校验：账户必须是池隔离 api_key 账户，且指纹仍在当前凭据池内
// （Node 经 account.selectedApiKeyFingerprint 传入，Go 显式传参）。
func (s *Store) resolveTarget(ctx context.Context, accountID, keyFingerprint string, keyIndex int, expected accountquality.KeyMutationExpected) (*runtimeTarget, probeFence, error) {
	fence := probeFence{}
	if expected.Status != "" {
		fence.provided = true
		fence.sql += "\n      AND status = ?"
		fence.params = append(fence.params, expected.Status)
	}
	if expected.NextProbeAt != "" {
		normalized, err := canonicalInstant(expected.NextProbeAt)
		if err != nil {
			fence.provided = true
			fence.invalidReason = "invalid_expected_probe_at"
			return nil, fence, nil
		}
		fence.provided = true
		fence.sql += "\n      AND next_probe_at = ?"
		fence.params = append(fence.params, normalized)
	}
	if expected.StateUpdatedAt != "" {
		normalized, err := canonicalInstant(expected.StateUpdatedAt)
		if err != nil {
			fence.provided = true
			fence.invalidReason = "invalid_expected_state_updated_at"
			return nil, fence, nil
		}
		fence.provided = true
		fence.sql += "\n      AND updated_at = ?"
		fence.params = append(fence.params, normalized)
	}
	if expected.ProbeClaimToken != "" {
		fence.provided = true
		fence.sql += "\n      AND probe_claim_token = ?"
		fence.params = append(fence.params, expected.ProbeClaimToken)
	}
	if expected.AccountConfigRevision < 0 || (expected.AccountConfigRevision > 0 && expected.AccountConfigRevision < 1) {
		fence.provided = true
		fence.invalidReason = "invalid_expected_account_config_revision"
		return nil, fence, nil
	}
	if expected.AccountConfigRevision >= 1 {
		fence.provided = true
	}
	trimmedFingerprint := strings.TrimSpace(keyFingerprint)
	if trimmedFingerprint == "" {
		return nil, fence, nil
	}
	row, err := s.LoadAccountForGroupFull(ctx, accountID)
	if err != nil {
		return nil, fence, err
	}
	if row == nil || row.Type != "api_key" || len(row.APIKeyEntries) < 2 {
		return nil, fence, nil
	}
	foundIndex := -1
	for _, entry := range row.APIKeyEntries {
		if entry.Fingerprint == trimmedFingerprint {
			foundIndex = entry.Index
			break
		}
	}
	if foundIndex < 0 {
		return nil, fence, nil
	}
	resolvedIndex := foundIndex
	if keyIndex >= 0 && row.Type == "api_key" {
		// Node：Number.isInteger(selectedApiKeyIndex) ? selectedApiKeyIndex : 0。
		resolvedIndex = keyIndex
	}
	return &runtimeTarget{
		systemAccountID: row.AccountOwnerSystemAccountID,
		accountID:       row.ID,
		keyFingerprint:  trimmedFingerprint,
		keyIndex:        resolvedIndex,
	}, fence, nil
}

func (s *Store) configFenceSQL(configRevision int64) string {
	if configRevision < 1 {
		return ""
	}
	return fmt.Sprintf(`
      AND EXISTS (
        SELECT 1
        FROM %s probe_account
        WHERE probe_account.id = account_id
          AND probe_account.deleted_at IS NULL
          AND probe_account.config_revision = ?
      )`, s.table("accounts"))
}

func (s *Store) configFenceParams(configRevision int64) []any {
	if configRevision < 1 {
		return nil
	}
	return []any{configRevision}
}

type runtimeRow struct {
	status            string
	recoveryStartedAt string
	lastErrorCode     string
	backoffSecondsVal int
}

func (r *runtimeRow) backoffSeconds() int { return r.backoffSecondsVal }

func (s *Store) loadRuntimeRow(ctx context.Context, accountID, keyFingerprint string) (*runtimeRow, error) {
	query := fmt.Sprintf(`
    SELECT status, recovery_started_at, last_error_code, probe_backoff_seconds
    FROM %s WHERE account_id = ? AND key_fingerprint = ? LIMIT 1
  `, s.table("account_api_key_runtime_states"))
	var (
		status            sql.NullString
		recoveryStartedAt sql.NullString
		lastErrorCode     sql.NullString
		probeBackoff      sql.NullInt64
	)
	if err := s.db.QueryRowContext(ctx, query, accountID, keyFingerprint).
		Scan(&status, &recoveryStartedAt, &lastErrorCode, &probeBackoff); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &runtimeRow{
		status:            status.String,
		recoveryStartedAt: recoveryStartedAt.String,
		lastErrorCode:     lastErrorCode.String,
		backoffSecondsVal: int(probeBackoff.Int64),
	}, nil
}

func rowsToResult(result sql.Result, fenceProvided bool) (accountquality.KeyMutationResult, error) {
	changed, err := result.RowsAffected()
	if err != nil {
		return accountquality.KeyMutationResult{}, err
	}
	if changed > 0 {
		return accountquality.KeyMutationResult{Changed: true}, nil
	}
	if fenceProvided {
		return changedFalse("stale_probe_state"), nil
	}
	return accountquality.KeyMutationResult{Changed: false}, nil
}

func changedFalse(reason string) accountquality.KeyMutationResult {
	return accountquality.KeyMutationResult{Changed: false}
}

func normalizeObservedAt(value, fallback string) string {
	observedMS, err := instantMS(value)
	if err != nil {
		return fallback
	}
	fallbackMS, err := instantMS(fallback)
	if err != nil {
		return fallback
	}
	if observedMS < fallbackMS {
		return formatMillis(observedMS)
	}
	return fallback
}

func formatMillis(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(rfc3339Milli)
}

func canonicalInstant(value string) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	return parsed.UTC().Format(rfc3339Milli), nil
}

func normalizeFailureStatus(status string) string {
	if status == "rate_limited" || status == "error" {
		return status
	}
	return "temporary_unavailable"
}

func nextProbeBackoffSeconds(previous int) int {
	if previous > 0 {
		return int(math.Min(maxProbeBackoffSeconds, float64(previous*2)))
	}
	return initialProbeBackoffSeconds
}

func normalizeProbeDeferSeconds(value int) int {
	if value < initialProbeBackoffSeconds {
		return initialProbeBackoffSeconds
	}
	if value > maxProbeBackoffSeconds {
		return maxProbeBackoffSeconds
	}
	return value
}

// passiveJitterWindowMS 等价 passiveScheduleJitterWindowMs。
func passiveJitterWindowMS(intervalMS int64) int64 {
	if intervalMS < 1 {
		intervalMS = 1
	}
	var windowMS int64
	switch {
	case intervalMS < 60_000:
		half := intervalMS / 2
		if half < 30_000 {
			windowMS = half
		} else {
			windowMS = 30_000
		}
	case intervalMS < 60*60_000:
		windowMS = 30_000
	case intervalMS < 24*60*60_000:
		windowMS = 30 * 60_000
	case intervalMS < 7*24*60*60_000:
		windowMS = 60 * 60_000
	default:
		windowMS = 8 * 60 * 60_000
	}
	half := intervalMS / 2
	if windowMS > half {
		windowMS = half
	}
	if windowMS < 0 {
		windowMS = 0
	}
	return windowMS
}

// passiveScheduleDelayMS 等价 passiveScheduleDelayMs（对称抖动，零偏移取 1）。
func passiveScheduleDelayMS(intervalMS int64) int64 {
	windowMS := passiveJitterWindowMS(intervalMS)
	offset := int64(0)
	if windowMS > 0 {
		unit := rand.Float64()
		offset = int64(math.Min(float64(windowMS), math.Floor(unit*float64(windowMS*2+1))-float64(windowMS)))
		if offset == 0 {
			offset = 1
		}
	}
	delay := intervalMS + offset
	if delay < 1 {
		delay = 1
	}
	return delay
}

// passiveProbeRetryAt 等价 passiveProbeRetryAt。
func passiveProbeRetryAt(delaySeconds int, now func() time.Time) string {
	intervalMS := int64(normalizeProbeDeferSeconds(delaySeconds)) * 1000
	return now().Add(time.Duration(passiveScheduleDelayMS(intervalMS)) * time.Millisecond).UTC().Format(rfc3339Milli)
}

// passiveProbeNotBeforeAt 等价 passiveProbeNotBeforeAt。
func passiveProbeNotBeforeAt(deadlineAt string, now func() time.Time) string {
	nowTime := now()
	deadlineMS, err := instantMS(deadlineAt)
	if err != nil {
		return deadlineAt
	}
	nowMS := nowTime.UnixMilli()
	if deadlineMS <= nowMS {
		return formatMillis(deadlineMS)
	}
	intervalMS := deadlineMS - nowMS
	windowMS := passiveJitterWindowMS(intervalMS)
	offset := int64(0)
	if windowMS > 0 {
		unit := rand.Float64()
		offset = int64(math.Min(float64(windowMS), math.Floor(unit*float64(windowMS*2+1))-float64(windowMS)))
		if offset == 0 {
			offset = 1
		}
	}
	absOffset := offset
	if absOffset < 0 {
		absOffset = -absOffset
	}
	return formatMillis(nowMS + intervalMS + absOffset)
}

func quotaRecoveryStartedAt(mode string, existing *runtimeRow, observedAt string, breakWindow bool) any {
	if breakWindow {
		return nil
	}
	if mode == "explicit_reset" {
		return nil
	}
	if mode == "generic" {
		previousMode := quotaModeFromErrorCode(existing.lastErrorCode)
		if previousMode == "generic" && (existing.status == "rate_limited" || existing.status == "error") {
			if existing.recoveryStartedAt != "" {
				return existing.recoveryStartedAt
			}
			return observedAt
		}
		return observedAt
	}
	if existing != nil && existing.recoveryStartedAt != "" {
		return existing.recoveryStartedAt
	}
	return observedAt
}

func quotaModeFromErrorCode(errorCode string) string {
	switch errorCode {
	case accountquality.QuotaRecoveryExplicitErrorCode:
		return "explicit_reset"
	case accountquality.QuotaRecoveryGenericErrorCode:
		return "generic"
	default:
		return ""
	}
}

func sanitizeRuntimeMessage(value string) string {
	collapsed := strings.Join(strings.Fields(value), " ")
	collapsed = strings.TrimSpace(collapsed)
	if len(collapsed) > 1000 {
		runes := []rune(collapsed)
		collapsed = string(runes[:1000])
	}
	if collapsed == "" {
		return "上游请求失败"
	}
	return collapsed
}

func normalizeTraceID(value string) any {
	text := strings.TrimSpace(value)
	if text == "" {
		return nil
	}
	if len(text) > 200 {
		return text[:200]
	}
	return text
}

func firstNonEmptyStr(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
