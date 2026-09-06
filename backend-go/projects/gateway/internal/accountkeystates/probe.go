package accountkeystates

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"
)

// 本文件移植 Node storage/account-api-key-runtime-state.repository.ts 的
// 探针写面：
//   - listAccountApiKeyRuntimeStatesDueForProbe(Async) +
//     claimAccountApiKeyRuntimeProbeCandidates(Async)  → ClaimDueForProbe；
//   - recordAccountApiKeyRuntimeFailure(Async)        → RecordFailure；
//   - recordAccountApiKeyRuntimeSuccess(Async)        → RecordSuccess；
//   - deferAccountApiKeyRuntimeProbe(Async)           → DeferProbe；
//   - accountApiKeyRuntimeTarget                      → ResolveTarget。
//
// 与 jobs 侧 backend-go-jobs/internal/proberepo/mutation.go 同表同键、SQL
// 同源（同一批 CAS 围栏）；jobs 承担后台 claim/探针写，本包承担网关侧被动
// 失败/成功登记与 defer，读写互通不冲突。
//
// 方言取材：PostgreSQL 分支取 Node *Async 变体（UPDATE ... AS current_state
// 别名 + ::timestamptz cast），SQLite 分支取同步变体（julianday 比较）。
// 已知偏差（与迁移报告同步披露）：Node PG 失败写入把 next_probe_at 的背期
// 调度放进 SQL（statement_timestamp()+random() 原子计算）；这里与 jobs
// proberepo 一致改为应用侧按注入时钟计算后绑定参数，避免同一张表出现两种
// Go 调度行为；config fence 的 FOR UPDATE 不移植（autocommit 写路径）。

// Target 等价 accountApiKeyRuntimeTarget 的解析结果。
type Target struct {
	SystemAccountID string
	AccountID       string
	KeyFingerprint  string
	KeyIndex        int
}

// TargetInput 是 ResolveTarget 的输入（Node OpenAIAccountSecret 的被消费
// 字段投影；全部来自调用方已加载的账户视图，本解析不读库——与 Node
// accountApiKeyRuntimeTarget 一致）。
type TargetInput struct {
	AccountID                 string
	SystemAccountID           string
	OwnerSystemAccountID      string // accountOwnerSystemAccountId
	CredentialSourceAccountID string // credentialSourceAccountId
	SelectedAPIKeyFingerprint string
	SelectedAPIKeyIndex       int
	HasSelectedAPIKeyIndex    bool
	ProviderCode              string
	ProtocolCode              string
	ProtocolVersion           string
	AccountType               string
	APIKey                    string
	APIKeys                   []string
	Credentials               map[string]any
	// RuntimeStateDisabled 对应 apiKeyRuntimeStateDisabled（置位时无目标）。
	RuntimeStateDisabled bool
}

// ResolveTarget 等价 accountApiKeyRuntimeTarget：合并凭据（api_key 覆写 +
// api_keys 池），要求池隔离开启且指纹仍在池内；失败返回 nil（调用方映射
// not_api_key_pool_account）。
func (s *Store) ResolveTarget(input TargetInput) *Target {
	if input.RuntimeStateDisabled {
		return nil
	}
	keyFingerprint := strings.TrimSpace(input.SelectedAPIKeyFingerprint)
	if keyFingerprint == "" {
		return nil
	}
	credentials := map[string]any{}
	for key, value := range input.Credentials {
		credentials[key] = value
	}
	credentials["api_key"] = input.APIKey
	if len(input.APIKeys) > 0 {
		list := make([]any, len(input.APIKeys))
		for index, key := range input.APIKeys {
			list[index] = key
		}
		credentials["api_keys"] = list
	}
	if !s.IsAccountAPIKeyPoolIsolationEnabled(input.ProviderCode, input.ProtocolCode, input.ProtocolVersion, input.AccountType, credentials) {
		return nil
	}
	found := false
	for _, entry := range s.AccountAPIKeyEntries(credentials) {
		if entry.Fingerprint == keyFingerprint {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	accountID := input.CredentialSourceAccountID
	if accountID == "" {
		accountID = input.AccountID
	}
	systemAccountID := input.OwnerSystemAccountID
	if systemAccountID == "" {
		systemAccountID = input.SystemAccountID
	}
	if accountID == "" || systemAccountID == "" {
		return nil
	}
	keyIndex := 0
	if input.HasSelectedAPIKeyIndex {
		keyIndex = input.SelectedAPIKeyIndex
	}
	return &Target{
		SystemAccountID: systemAccountID,
		AccountID:       accountID,
		KeyFingerprint:  keyFingerprint,
		KeyIndex:        keyIndex,
	}
}

// WriteResult 等价 AccountApiKeyRuntimeWriteResult。
type WriteResult struct {
	Changed       bool
	SkippedReason string
}

// ExpectedProbeState 对应 expectedProbeStateFence 输入（乐观围栏）。
// AccountConfigRevision 为 nil 表示未提供；非 nil 且 < 1 判为非法。
type ExpectedProbeState struct {
	Status                string
	NextProbeAt           string
	StateUpdatedAt        string
	ProbeClaimToken       string
	AccountConfigRevision *int64
}

type probeFence struct {
	provided      bool
	sql           string
	params        []any
	invalidReason string
}

// buildProbeFence 等价 expectedProbeStateFence(input, columnPrefix)。
func buildProbeFence(expected ExpectedProbeState, columnPrefix string) probeFence {
	fence := probeFence{}
	if expected.Status != "" {
		fence.provided = true
		fence.sql += "\n        AND " + columnPrefix + "status = ?"
		fence.params = append(fence.params, expected.Status)
	}
	if expected.NextProbeAt != "" {
		normalized, err := canonicalInstant(expected.NextProbeAt)
		if err != nil {
			return probeFence{provided: true, invalidReason: "invalid_expected_probe_at"}
		}
		fence.provided = true
		fence.sql += "\n        AND " + columnPrefix + "next_probe_at = ?"
		fence.params = append(fence.params, normalized)
	}
	if expected.StateUpdatedAt != "" {
		normalized, err := canonicalInstant(expected.StateUpdatedAt)
		if err != nil {
			return probeFence{provided: true, invalidReason: "invalid_expected_state_updated_at"}
		}
		fence.provided = true
		fence.sql += "\n        AND " + columnPrefix + "updated_at = ?"
		fence.params = append(fence.params, normalized)
	}
	if expected.ProbeClaimToken != "" {
		token := strings.TrimSpace(expected.ProbeClaimToken)
		if token == "" {
			return probeFence{provided: true, invalidReason: "invalid_expected_probe_claim_token"}
		}
		fence.provided = true
		fence.sql += "\n        AND " + columnPrefix + "probe_claim_token = ?"
		fence.params = append(fence.params, token)
	}
	if expected.AccountConfigRevision != nil && *expected.AccountConfigRevision < 1 {
		return probeFence{provided: true, invalidReason: "invalid_expected_account_config_revision"}
	}
	if expected.AccountConfigRevision != nil {
		fence.provided = true
	}
	return fence
}

// configRevisionFence 等价 expectedAccountConfigRevisionFence（不带 Node PG
// 分支的 FOR UPDATE，见文件头偏差披露）。
func (s *Store) configRevisionFence(expected ExpectedProbeState, stateAccountIDColumn, columnPrefix string) (string, []any) {
	if expected.AccountConfigRevision == nil {
		return "", nil
	}
	sqlText := fmt.Sprintf(`
        AND EXISTS (
          SELECT 1
          FROM %s probe_account
          WHERE probe_account.id = %s
            AND probe_account.deleted_at IS NULL
            AND %sprobe_account.config_revision = ?
        )`, s.businessTable("accounts"), stateAccountIDColumn, columnPrefix)
	return sqlText, []any{*expected.AccountConfigRevision}
}

// ---- claim ----

// ProbeCandidate 等价 AccountApiKeyRuntimeProbeCandidate。
type ProbeCandidate struct {
	AccountID             string
	AccountName           string
	KeyFingerprint        string
	KeyIndex              int
	APIKey                string
	Status                string
	NextProbeAt           string
	StateUpdatedAt        string
	AccountConfigRevision int64
	ProbeClaimToken       string
	ProbeClaimedUntil     string
	RecoveryStartedAt     string
	LastErrorCode         string
}

// ClaimDueForProbe 实现 listAccountApiKeyRuntimeStatesDueForProbeAsync：扫描
// 到期候选（限 probeCandidateScanLimit），逐条 CAS 抢占租约后返回
// （limit 归一到 1..100）。
func (s *Store) ClaimDueForProbe(ctx context.Context, limit int) ([]ProbeCandidate, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	now := s.nowISO()
	query := s.bind(fmt.Sprintf(`
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
      AND accounts.status IN (%s)
      AND accounts.schedulable = 1
      AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
    ORDER BY states.next_probe_at ASC, states.updated_at ASC, states.account_id ASC, states.key_index ASC
    LIMIT ?
  `, s.statesTable(), s.businessTable("accounts"), probeParentStatusesSQL))
	rows, err := s.db.QueryContext(ctx, query, s.instantParam(now), s.instantParam(now), s.instantParam(now), probeCandidateScanLimit)
	if err != nil {
		return nil, err
	}
	var candidates []ProbeCandidate
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
			rows.Close()
			return nil, err
		}
		credentials, err := s.DecryptCredentials(credentialsEncrypted.String)
		if err != nil {
			// Node：解密失败按候选缺失处理。
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
		candidates = append(candidates, ProbeCandidate{
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
		rows.Close()
		return nil, err
	}
	rows.Close()
	return s.claimCandidates(ctx, candidates, limit, now)
}

// claimCandidates 等价 claimAccountApiKeyRuntimeProbeCandidates(Async)：逐条
// CAS 写入 claim token/租约，竞争失败的条目丢弃。
func (s *Store) claimCandidates(ctx context.Context, candidates []ProbeCandidate, limit int, now string) ([]ProbeCandidate, error) {
	claimedUntilTime, err := time.Parse(time.RFC3339Nano, now)
	if err != nil {
		return nil, err
	}
	claimedUntil := claimedUntilTime.Add(probeClaimLeaseSeconds).UTC().Format(rfc3339Milli)
	claimed := make([]ProbeCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if len(claimed) >= limit {
			break
		}
		token := "account_api_key_probe_claim_" + randomToken(12)
		query := s.bind(fmt.Sprintf(`
      UPDATE %s
      SET probe_claim_token = ?, probe_claimed_until = ?
      WHERE account_id = ?
        AND key_fingerprint = ?
        AND status = ?
        AND next_probe_at = ?
        AND (probe_claimed_until IS NULL OR probe_claimed_until <= ?)
    `, s.statesTable()))
		result, err := s.db.ExecContext(ctx, query,
			token, s.instantParam(claimedUntil),
			candidate.AccountID, candidate.KeyFingerprint, candidate.Status,
			s.instantParam(candidate.NextProbeAt), s.instantParam(now))
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

// ---- 写语句方言外壳 ----

// writePrefix 返回 PG 写语句的列前缀（Node expectedProbeStateFence 的
// 'current_state.'）。
func (s *Store) writePrefix() string {
	if s.postgres {
		return "current_state."
	}
	return ""
}

// writeColumn 返回写语句里的列引用（PG 带 current_state 别名）。
func (s *Store) writeColumn(column string) string { return s.writePrefix() + column }

// updateTarget 返回 UPDATE 目标（PG 用 AS current_state 别名，SQLite 裸表）。
func (s *Store) updateTarget() string {
	if s.postgres {
		return s.statesTable() + " AS current_state"
	}
	return s.statesTable()
}

// genericGuard 渲染失败写入的配额泛化守卫（沿写语句列前缀）。
func (s *Store) genericGuard() string {
	if s.postgres {
		return `
        AND NOT (
          ? = 'generic'
          AND current_state.last_error_code IN (?, ?)
          AND current_state.cooldown_until IS NOT NULL
          AND current_state.cooldown_until::timestamptz > ?::timestamptz
        )`
	}
	return `
        AND NOT (
          ? = 'generic'
          AND last_error_code IN (?, ?)
          AND cooldown_until IS NOT NULL
          AND julianday(cooldown_until) > julianday(?)
        )`
}

func genericGuardParams(mode, observedAt string, bind func(string) any) []any {
	return []any{mode, QuotaRecoveryExplicitErrorCode, QuotaRecoveryGenericErrorCode, bind(observedAt)}
}

// ---- record failure ----

// FailureInput 等价 AccountApiKeyRuntimeFailureInput（account 换成显式
// TargetInput 投影；StatusCode 0 表示未提供）。
type FailureInput struct {
	Account                  TargetInput
	Status                   string // 'rate_limited' | 'error'，其他回落 temporary_unavailable
	StatusCode               int
	ErrorCode                string
	ErrorMessage             string
	TraceID                  string
	CooldownUntil            string
	QuotaRecoveryMode        string // '' | 'generic' | 'explicit_reset'
	BreakQuotaRecoveryWindow bool
	ObservedAt               string
	Expected                 ExpectedProbeState
}

// RecordFailure 实现 recordAccountApiKeyRuntimeFailure(Async)：existing 读后
// 按 fence/配额守卫做 CAS 写入；带 fence 两方言都走 fenced UPDATE，无 fence
// 时 SQLite 按 existing 分支 UPDATE/INSERT、PG 走原子 upsert。
func (s *Store) RecordFailure(ctx context.Context, input FailureInput) (WriteResult, error) {
	target := s.ResolveTarget(input.Account)
	if target == nil {
		return WriteResult{SkippedReason: "not_api_key_pool_account"}, nil
	}
	expectedFence := buildProbeFence(input.Expected, s.writePrefix())
	if expectedFence.invalidReason != "" {
		return WriteResult{SkippedReason: expectedFence.invalidReason}, nil
	}
	existing, err := s.loadRuntimeRow(ctx, target.AccountID, target.KeyFingerprint)
	if err != nil {
		return WriteResult{}, err
	}
	if existing != nil && existing.status == "disabled" {
		return WriteResult{SkippedReason: "key_disabled"}, nil
	}
	if expectedFence.provided && existing == nil {
		return WriteResult{SkippedReason: "stale_probe_state"}, nil
	}
	now := s.nowISO()
	observedAt := normalizeObservedAt(input.ObservedAt, now)
	previousBackoff := 0
	if existing != nil {
		previousBackoff = existing.backoffSeconds()
	}
	nextBackoff := nextProbeBackoffSeconds(previousBackoff)
	status := normalizeFailureStatus(input.Status)
	parsedCooldownUntil := ""
	if input.CooldownUntil != "" {
		if _, err := canonicalInstant(input.CooldownUntil); err != nil {
			return WriteResult{}, errInvalid("cooldownUntil必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
		parsedCooldownUntil = input.CooldownUntil
	}
	nextProbeAt := passiveProbeRetryAt(nextBackoff, s.now)
	if parsedCooldownUntil != "" && status == "rate_limited" {
		nextProbeAt = passiveProbeNotBeforeAt(parsedCooldownUntil, s.now)
	}
	persistedCooldownUntil := parsedCooldownUntil
	if persistedCooldownUntil == "" {
		persistedCooldownUntil = nextProbeAt
	}
	errorCode := input.ErrorCode
	if errorCode == "" {
		switch input.QuotaRecoveryMode {
		case "explicit_reset":
			errorCode = QuotaRecoveryExplicitErrorCode
		case "generic":
			errorCode = QuotaRecoveryGenericErrorCode
		default:
			if input.StatusCode != 0 {
				errorCode = fmt.Sprintf("http_%d", input.StatusCode)
			}
		}
	}
	defaultMessage := "上游请求失败"
	if input.StatusCode != 0 {
		defaultMessage = fmt.Sprintf("上游返回 HTTP %d", input.StatusCode)
	}
	errorMessage := sanitizeRuntimeMessage(firstNonEmptyStr(input.ErrorMessage, defaultMessage))
	recoveryStartedAt := quotaRecoveryStartedAt(input.QuotaRecoveryMode, existing, observedAt, input.BreakQuotaRecoveryWindow)

	// recovery SQL 分支（Node recoveryStartedAtSql）。
	recoverySQL := "COALESCE(" + s.writeColumn("recovery_started_at") + ", ?)"
	recoveryParams := []any{now}
	if input.BreakQuotaRecoveryWindow || input.QuotaRecoveryMode != "" {
		recoverySQL = "?"
		recoveryParams = []any{recoveryStartedAt}
	}
	configFenceSQL, configFenceParams := s.configRevisionFence(input.Expected, s.writeColumn("account_id"), s.writePrefix())

	fencedUpdate := func() (WriteResult, error) {
		query := s.bind(fmt.Sprintf(`
      UPDATE %s
      SET system_account_id = ?, key_index = ?, status = ?,
          failure_count = failure_count + 1, consecutive_failures = consecutive_failures + 1,
          cooldown_until = ?, next_probe_at = ?, probe_backoff_seconds = ?,
          recovery_started_at = %s,
          last_attempt_at = ?, last_failure_at = ?, last_error_code = ?, last_error_message = ?, last_trace_id = ?,
          probe_claim_token = NULL, probe_claimed_until = NULL,
          updated_at = ?
      WHERE %s = ? AND %s = ?
        AND %s
        AND (%s IS NULL OR %s < ?)
        %s
        %s
        %s
    `, s.updateTarget(), recoverySQL,
			s.writeColumn("account_id"), s.writeColumn("key_fingerprint"),
			s.writeColumn("status"),
			s.writeColumn("last_attempt_at"), s.writeColumn("last_attempt_at"),
			s.genericGuard(), expectedFence.sql, configFenceSQL))
		params := []any{
			target.SystemAccountID, target.KeyIndex, status,
			s.instantParam(persistedCooldownUntil), s.instantParam(nextProbeAt), nextBackoff,
		}
		params = append(params, recoveryParams...)
		params = append(params,
			s.instantParam(observedAt), s.instantParam(observedAt), argOrNull(errorCode), errorMessage, normalizeTraceID(input.TraceID), now,
			target.AccountID, target.KeyFingerprint, s.instantParam(observedAt))
		params = append(params, genericGuardParams(input.QuotaRecoveryMode, observedAt, s.instantParam)...)
		params = append(params, expectedFence.params...)
		params = append(params, configFenceParams...)
		result, err := s.db.ExecContext(ctx, query, params...)
		if err != nil {
			return WriteResult{}, err
		}
		return s.finishMutation(ctx, result, target.AccountID, true)
	}

	if expectedFence.provided {
		return fencedUpdate()
	}
	if s.postgres {
		// PG 无 fence：原子 upsert（Node async 变体）。
		query := s.bind(fmt.Sprintf(`
    INSERT INTO %s AS current_state (
      id, system_account_id, account_id, key_fingerprint, key_index,
      status, failure_count, consecutive_failures, success_count,
      cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
      last_attempt_at, last_failure_at, last_error_code, last_error_message,
      last_trace_id,
      created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, 1, 1, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT (account_id, key_fingerprint) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      key_index = excluded.key_index,
      status = excluded.status,
      failure_count = current_state.failure_count + 1,
      consecutive_failures = current_state.consecutive_failures + 1,
      cooldown_until = excluded.cooldown_until,
      next_probe_at = excluded.next_probe_at,
      probe_backoff_seconds = excluded.probe_backoff_seconds,
      recovery_started_at = CASE
        WHEN %s THEN excluded.recovery_started_at
        ELSE COALESCE(current_state.recovery_started_at, excluded.recovery_started_at)
      END,
      last_attempt_at = excluded.last_attempt_at,
      last_failure_at = excluded.last_failure_at,
      last_error_code = excluded.last_error_code,
      last_error_message = excluded.last_error_message,
      last_trace_id = excluded.last_trace_id,
      probe_claim_token = NULL,
      probe_claimed_until = NULL,
      updated_at = excluded.updated_at
    WHERE current_state.status <> 'disabled'
      AND (current_state.last_attempt_at IS NULL OR current_state.last_attempt_at < excluded.last_attempt_at)
      AND NOT (
        ? = 'generic'
        AND current_state.last_error_code IN (?, ?)
        AND current_state.cooldown_until IS NOT NULL
        AND current_state.cooldown_until::timestamptz > excluded.last_attempt_at::timestamptz
      )
  `, s.statesTable(), boolSQL(input.BreakQuotaRecoveryWindow || input.QuotaRecoveryMode != "")))
		result, err := s.db.ExecContext(ctx, query,
			newStateID(), target.SystemAccountID, target.AccountID, target.KeyFingerprint, target.KeyIndex,
			status, s.instantParam(persistedCooldownUntil), s.instantParam(nextProbeAt), nextBackoff, recoveryStartedAt,
			s.instantParam(observedAt), s.instantParam(observedAt), argOrNull(errorCode), errorMessage, normalizeTraceID(input.TraceID), now, now,
			input.QuotaRecoveryMode, QuotaRecoveryExplicitErrorCode, QuotaRecoveryGenericErrorCode)
		if err != nil {
			return WriteResult{}, err
		}
		return s.finishMutation(ctx, result, target.AccountID, false)
	}
	if existing != nil {
		// SQLite 无 fence 且已有行：不带 fence 的守卫 UPDATE（Node 同步变体）。
		query := s.bind(fmt.Sprintf(`
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
        AND (last_attempt_at IS NULL OR last_attempt_at < ?)
        %s
    `, s.statesTable(), recoverySQL, s.genericGuard()))
		params := []any{
			target.SystemAccountID, target.KeyIndex, status,
			s.instantParam(persistedCooldownUntil), s.instantParam(nextProbeAt), nextBackoff,
		}
		params = append(params, recoveryParams...)
		params = append(params,
			s.instantParam(observedAt), s.instantParam(observedAt), argOrNull(errorCode), errorMessage, normalizeTraceID(input.TraceID), now,
			target.AccountID, target.KeyFingerprint, s.instantParam(observedAt))
		params = append(params, genericGuardParams(input.QuotaRecoveryMode, observedAt, s.instantParam)...)
		result, err := s.db.ExecContext(ctx, query, params...)
		if err != nil {
			return WriteResult{}, err
		}
		return s.finishMutation(ctx, result, target.AccountID, false)
	}
	// SQLite 无 fence 且无行：普通 INSERT（Node 同步变体）。
	query := s.bind(fmt.Sprintf(`
      INSERT INTO %s (
        id, system_account_id, account_id, key_fingerprint, key_index,
        status, failure_count, consecutive_failures, success_count,
        cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
        last_attempt_at, last_failure_at, last_error_code, last_error_message,
        last_trace_id,
        created_at, updated_at
      )
      VALUES (?, ?, ?, ?, ?, ?, 1, 1, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, s.statesTable()))
	result, err := s.db.ExecContext(ctx, query,
		newStateID(), target.SystemAccountID, target.AccountID, target.KeyFingerprint, target.KeyIndex,
		status, s.instantParam(persistedCooldownUntil), s.instantParam(nextProbeAt), nextBackoff, recoveryStartedAt,
		s.instantParam(observedAt), s.instantParam(observedAt), argOrNull(errorCode), errorMessage, normalizeTraceID(input.TraceID), now, now)
	if err != nil {
		return WriteResult{}, err
	}
	return s.finishMutation(ctx, result, target.AccountID, false)
}

// ---- record success ----

// SuccessInput 等价 AccountApiKeyRuntimeSuccessInput。
type SuccessInput struct {
	ObservedAt string
	Expected   ExpectedProbeState
}

// RecordSuccess 实现 recordAccountApiKeyRuntimeSuccess(Async)。
func (s *Store) RecordSuccess(ctx context.Context, account TargetInput, input SuccessInput) (WriteResult, error) {
	if input.Expected.Status == "error" {
		return WriteResult{SkippedReason: "manual_restore_required"}, nil
	}
	target := s.ResolveTarget(account)
	if target == nil {
		return WriteResult{SkippedReason: "not_api_key_pool_account"}, nil
	}
	expectedFence := buildProbeFence(input.Expected, s.writePrefix())
	if expectedFence.invalidReason != "" {
		return WriteResult{SkippedReason: expectedFence.invalidReason}, nil
	}
	now := s.nowISO()
	observedAt := normalizeObservedAt(input.ObservedAt, now)
	configFenceSQL, configFenceParams := s.configRevisionFence(input.Expected, s.writeColumn("account_id"), s.writePrefix())

	if expectedFence.provided {
		query := s.bind(fmt.Sprintf(`
      UPDATE %s
      SET system_account_id = ?, key_index = ?, status = 'active',
          consecutive_failures = 0, success_count = success_count + 1,
          cooldown_until = NULL, next_probe_at = NULL, probe_backoff_seconds = 0,
          recovery_started_at = NULL,
          last_attempt_at = ?, last_success_at = ?,
          last_error_code = NULL, last_error_message = NULL, last_trace_id = NULL,
          probe_claim_token = NULL, probe_claimed_until = NULL,
          updated_at = ?
      WHERE %s = ? AND %s = ?
        AND %s
        AND (%s IS NULL OR %s <= ?)
        %s
        %s
    `, s.updateTarget(),
			s.writeColumn("account_id"), s.writeColumn("key_fingerprint"),
			s.writeColumn("status"),
			s.writeColumn("last_attempt_at"), s.writeColumn("last_attempt_at"),
			expectedFence.sql, configFenceSQL))
		params := []any{
			target.SystemAccountID, target.KeyIndex, s.instantParam(observedAt), s.instantParam(observedAt), now,
			target.AccountID, target.KeyFingerprint, s.instantParam(observedAt),
		}
		params = append(params, expectedFence.params...)
		params = append(params, configFenceParams...)
		result, err := s.db.ExecContext(ctx, query, params...)
		if err != nil {
			return WriteResult{}, err
		}
		return s.finishMutation(ctx, result, target.AccountID, expectedFence.provided)
	}
	if s.postgres {
		query := s.bind(fmt.Sprintf(`
    INSERT INTO %s AS current_state (
      id, system_account_id, account_id, key_fingerprint, key_index,
      status, failure_count, consecutive_failures, success_count,
      cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
      last_attempt_at, last_success_at, last_error_code, last_error_message,
      last_trace_id,
      created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, 'active', 0, 0, 1, NULL, NULL, 0, NULL, ?, ?, NULL, NULL, NULL, ?, ?)
    ON CONFLICT (account_id, key_fingerprint) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      key_index = excluded.key_index,
      status = 'active',
      consecutive_failures = 0,
      success_count = current_state.success_count + 1,
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
    WHERE current_state.status NOT IN ('disabled', 'error')
      AND (current_state.last_attempt_at IS NULL OR current_state.last_attempt_at <= excluded.last_attempt_at)
  `, s.statesTable()))
		result, err := s.db.ExecContext(ctx, query,
			newStateID(), target.SystemAccountID, target.AccountID, target.KeyFingerprint, target.KeyIndex,
			s.instantParam(observedAt), s.instantParam(observedAt), now, now)
		if err != nil {
			return WriteResult{}, err
		}
		return s.finishMutation(ctx, result, target.AccountID, false)
	}
	query := s.bind(fmt.Sprintf(`
    INSERT INTO %s (
      id, system_account_id, account_id, key_fingerprint, key_index,
      status, failure_count, consecutive_failures, success_count,
      cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
      last_attempt_at, last_success_at, last_error_code, last_error_message,
      last_trace_id,
      created_at, updated_at
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
  `, s.statesTable()))
	result, err := s.db.ExecContext(ctx, query,
		newStateID(), target.SystemAccountID, target.AccountID, target.KeyFingerprint, target.KeyIndex,
		s.instantParam(observedAt), s.instantParam(observedAt), now, now)
	if err != nil {
		return WriteResult{}, err
	}
	return s.finishMutation(ctx, result, target.AccountID, false)
}

// ---- defer probe ----

// DeferInput 等价 AccountApiKeyRuntimeProbeDeferInput。
type DeferInput struct {
	ExpectedNextProbeAt      string // 必填（Node missing/invalid 二分）
	DelaySeconds             int
	ObservedAt               string
	BreakQuotaRecoveryWindow bool
	Expected                 ExpectedProbeState
}

// DeferProbe 实现 deferAccountApiKeyRuntimeProbe(Async)。Node 在变体间共享
// 同一 WHERE（PG 带 current_state 别名）；不触发 runtime-state-changed 标记。
func (s *Store) DeferProbe(ctx context.Context, account TargetInput, input DeferInput) (WriteResult, error) {
	target := s.ResolveTarget(account)
	if target == nil {
		return WriteResult{SkippedReason: "not_api_key_pool_account"}, nil
	}
	if strings.TrimSpace(input.ExpectedNextProbeAt) == "" {
		return WriteResult{SkippedReason: "missing_expected_probe_at"}, nil
	}
	fenceInput := input.Expected
	fenceInput.NextProbeAt = input.ExpectedNextProbeAt
	expectedFence := buildProbeFence(fenceInput, s.writePrefix())
	if expectedFence.invalidReason != "" {
		return WriteResult{SkippedReason: expectedFence.invalidReason}, nil
	}
	now := s.nowISO()
	observedAt := normalizeObservedAt(input.ObservedAt, now)
	nextProbeAt := passiveProbeRetryAt(normalizeProbeDeferSeconds(input.DelaySeconds), s.now)
	recoverySQL := s.writeColumn("recovery_started_at")
	if input.BreakQuotaRecoveryWindow {
		recoverySQL = "NULL"
	}
	configFenceSQL, configFenceParams := s.configRevisionFence(input.Expected, s.writeColumn("account_id"), s.writePrefix())
	query := s.bind(fmt.Sprintf(`
    UPDATE %s
    SET next_probe_at = ?,
        last_attempt_at = ?,
        probe_claim_token = NULL,
        probe_claimed_until = NULL,
        recovery_started_at = CASE WHEN %s THEN NULL ELSE %s END,
        updated_at = ?
    WHERE %s = ? AND %s = ?
      AND (%s IS NULL OR %s <= ?)
      %s
      %s
  `, s.updateTarget(), boolSQL(input.BreakQuotaRecoveryWindow), recoverySQL,
		s.writeColumn("account_id"), s.writeColumn("key_fingerprint"),
		s.writeColumn("last_attempt_at"), s.writeColumn("last_attempt_at"),
		expectedFence.sql, configFenceSQL))
	params := []any{s.instantParam(nextProbeAt), s.instantParam(observedAt), now,
		target.AccountID, target.KeyFingerprint, s.instantParam(observedAt)}
	params = append(params, expectedFence.params...)
	params = append(params, configFenceParams...)
	result, err := s.db.ExecContext(ctx, query, params...)
	if err != nil {
		return WriteResult{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return WriteResult{}, err
	}
	if changed > 0 {
		return WriteResult{Changed: true}, nil
	}
	return WriteResult{SkippedReason: "stale_probe_state"}, nil
}

// ---- 共享辅助 ----

// finishMutation 收尾：changed 时补 runtime-state-changed 标记；未 changed 且
// 带围栏时报告 stale_probe_state（Node rowsToResult 形状）。
func (s *Store) finishMutation(ctx context.Context, result sql.Result, sourceAccountID string, fenceProvided bool) (WriteResult, error) {
	changed, err := result.RowsAffected()
	if err != nil {
		return WriteResult{}, err
	}
	if changed > 0 {
		if err := s.markRuntimeStateChanged(ctx, sourceAccountID); err != nil {
			return WriteResult{}, err
		}
		return WriteResult{Changed: true}, nil
	}
	if fenceProvided {
		return WriteResult{SkippedReason: "stale_probe_state"}, nil
	}
	return WriteResult{}, nil
}

type runtimeRow struct {
	status            string
	recoveryStartedAt string
	lastErrorCode     string
	backoff           int
}

func (r *runtimeRow) backoffSeconds() int { return r.backoff }

// loadRuntimeRow 等价 record* 共用的 existing 读取。
func (s *Store) loadRuntimeRow(ctx context.Context, accountID, keyFingerprint string) (*runtimeRow, error) {
	query := s.bind(fmt.Sprintf(`
    SELECT status, recovery_started_at, last_error_code, probe_backoff_seconds
    FROM %s WHERE account_id = ? AND key_fingerprint = ? LIMIT 1
  `, s.statesTable()))
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
		backoff:           int(probeBackoff.Int64),
	}, nil
}

// newStateID 等价 newId('account_api_key_runtime_state')。
func newStateID() string {
	return "account_api_key_runtime_state_" + randomToken(16)
}

func randomToken(length int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buffer := make([]byte, length)
	for index := range buffer {
		buffer[index] = alphabet[rand.Intn(len(alphabet))]
	}
	return string(buffer)
}

// normalizeObservedAt 等价 normalizeObservedAt：取观察时间与当前时间的较早值。
func normalizeObservedAt(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
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

// normalizeFailureStatus 等价 normalizeFailureStatus。
func normalizeFailureStatus(status string) string {
	if status == "rate_limited" || status == "error" {
		return status
	}
	return "temporary_unavailable"
}

// nextProbeBackoffSeconds 等价 nextProbeBackoffSeconds。
func nextProbeBackoffSeconds(previous int) int {
	if previous > 0 {
		return int(math.Min(maxProbeBackoffSeconds, float64(previous*2)))
	}
	return initialProbeBackoffSeconds
}

// normalizeProbeDeferSeconds 等价 normalizeProbeDeferSeconds。
func normalizeProbeDeferSeconds(value int) int {
	if value < initialProbeBackoffSeconds {
		return initialProbeBackoffSeconds
	}
	if value > maxProbeBackoffSeconds {
		return maxProbeBackoffSeconds
	}
	return value
}

// passiveJitterWindowMS 等价 passiveScheduleJitterWindowMs（shared/
// passive-schedule-jitter.ts；与 jobs proberepo 同源）。
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

// passiveJitterOffsetMS 采样对称抖动偏移（零偏移取 1，与 jobs proberepo
// passiveScheduleDelayMs 的 offset 采样同源）。
func passiveJitterOffsetMS(windowMS int64) int64 {
	if windowMS <= 0 {
		return 0
	}
	unit := rand.Float64()
	offset := int64(math.Min(float64(windowMS), math.Floor(unit*float64(windowMS*2+1))-float64(windowMS)))
	if offset == 0 {
		offset = 1
	}
	return offset
}

// passiveProbeRetryAt 等价 passiveProbeRetryAt。
func passiveProbeRetryAt(delaySeconds int, now func() time.Time) string {
	intervalMS := int64(normalizeProbeDeferSeconds(delaySeconds)) * 1000
	windowMS := passiveJitterWindowMS(intervalMS)
	delay := intervalMS + passiveJitterOffsetMS(windowMS)
	if delay < 1 {
		delay = 1
	}
	return now().Add(time.Duration(delay) * time.Millisecond).UTC().Format(rfc3339Milli)
}

// passiveProbeNotBeforeAt 等价 passiveProbeNotBeforeAt：上游 deadline 已过则
// 立即到期；未过则在 deadline 上叠加对称抖动。
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
	offset := passiveJitterOffsetMS(windowMS)
	if offset < 0 {
		offset = -offset
	}
	return formatMillis(nowMS + intervalMS + offset)
}

// quotaRecoveryStartedAt 等价 quotaRecoveryStartedAt（existing 可为 nil，
// 对应 Node input.existing?. 的可选链）。
func quotaRecoveryStartedAt(mode string, existing *runtimeRow, observedAt string, breakWindow bool) any {
	if breakWindow {
		return nil
	}
	if mode == "explicit_reset" {
		return nil
	}
	if mode == "generic" {
		previousErrorCode := ""
		previousStatus := ""
		previousRecoveryStartedAt := ""
		if existing != nil {
			previousErrorCode = existing.lastErrorCode
			previousStatus = existing.status
			previousRecoveryStartedAt = existing.recoveryStartedAt
		}
		previousMode := quotaModeFromErrorCode(previousErrorCode)
		if previousMode == "generic" && (previousStatus == "rate_limited" || previousStatus == "error") {
			if previousRecoveryStartedAt != "" {
				return previousRecoveryStartedAt
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

// quotaModeFromErrorCode 等价 apiKeyQuotaRecoveryModeFromErrorCode。
func quotaModeFromErrorCode(errorCode string) string {
	switch errorCode {
	case QuotaRecoveryExplicitErrorCode:
		return "explicit_reset"
	case QuotaRecoveryGenericErrorCode:
		return "generic"
	default:
		return ""
	}
}

// sanitizeRuntimeMessage 等价 sanitizeRuntimeErrorMessage（空白折叠 + 1000 截断）。
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

// normalizeTraceID 等价 normalizeRuntimeTraceId（NULL 语义：空串绑定为 nil）。
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

// boolSQL 渲染 SQL 布尔字面量。
func boolSQL(value bool) string {
	if value {
		return "TRUE"
	}
	return "FALSE"
}
