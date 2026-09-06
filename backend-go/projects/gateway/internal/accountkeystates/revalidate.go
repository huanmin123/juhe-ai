package accountkeystates

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// 本文件移植 Node revalidateAccountApiKeyRuntimePoolAsync（runtime-reset 端口
// RevalidateAccountAPIKeyRuntimePool 的域语义）与 markRuntimeStateChanged(Async)
// （group_account_stats_dirty 标记 + 进程内运行态缓存失效通知）。

// Revalidate 理由码（Node AccountApiKeyRuntimeRevalidateResult.reason）。
const (
	ReasonAccountNotFound        = "account_not_found"
	ReasonAccountNotActive       = "account_not_active"
	ReasonAccountUnschedulable   = "account_unschedulable"
	ReasonConfigRevisionConflict = "config_revision_conflict"
	ReasonNotSupported           = "not_supported"
	ReasonNoRevalidatableKey     = "no_revalidatable_key"
)

// RevalidateResult 等价 AccountApiKeyRuntimeRevalidateResult。
type RevalidateResult struct {
	Changed  int
	Eligible bool
	Reason   string
}

// accountRow 是 revalidate 的账户行投影。
type revalidateAccountRow struct {
	providerCode         string
	protocolCode         sql.NullString
	protocolVersion      sql.NullString
	accountType          string
	status               string
	schedulable          int64
	configRevision       int64
	credentialsEncrypted string
}

// RevalidatePool 实现 revalidateAccountApiKeyRuntimePoolAsync：账户必须是
// active + schedulable 的池隔离账户（config_revision 乐观校验），把全部
// 探针可重试的非 active/disabled Key 立即置为到期（error 回落 unverified）；
// 租约未过期的 Key 不动。changed>0 或重试成功时补 runtime-state-changed 标记。
func (s *Store) RevalidatePool(ctx context.Context, accountID string, expectedConfigRevision int64) (RevalidateResult, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || expectedConfigRevision < 1 {
		return RevalidateResult{}, errInvalid("重新验证 API Key 池参数无效")
	}
	now := s.nowISO()
	row, err := s.loadRevalidateAccountRow(ctx, accountID)
	if err != nil {
		return RevalidateResult{}, err
	}
	reason, ok := revalidateGateReason(row, expectedConfigRevision)
	if !ok {
		return RevalidateResult{Reason: reason}, nil
	}
	credentials, err := s.DecryptCredentials(row.credentialsEncrypted)
	if err != nil {
		// Node：解密失败按空凭据处理（isolation 判定 → not_supported）。
		credentials = map[string]any{}
	}
	eligible := s.IsAccountAPIKeyPoolIsolationEnabled(row.providerCode, row.protocolCode.String, row.protocolVersion.String, row.accountType, credentials)
	fingerprints := make([]string, 0)
	for _, entry := range s.AccountAPIKeyEntries(credentials) {
		fingerprints = append(fingerprints, entry.Fingerprint)
	}
	if !eligible || len(fingerprints) < 2 {
		return RevalidateResult{Reason: ReasonNotSupported}, nil
	}
	changed, err := s.execRevalidateUpdate(ctx, now, accountID, fingerprints, expectedConfigRevision)
	if err != nil {
		return RevalidateResult{}, err
	}
	if changed == 0 {
		// Node 的 changed==0 分支：重读账户行重推理由，再判候选后重试一次。
		current, err := s.loadRevalidateGateRow(ctx, accountID)
		if err != nil {
			return RevalidateResult{}, err
		}
		gateReason, ok := revalidateGateReasonFromGate(current, expectedConfigRevision)
		if !ok {
			return RevalidateResult{Reason: gateReason}, nil
		}
		candidate, err := s.hasRevalidatableCandidate(ctx, accountID, fingerprints, now)
		if err != nil {
			return RevalidateResult{}, err
		}
		if !candidate {
			return RevalidateResult{Reason: ReasonNoRevalidatableKey}, nil
		}
		retried, err := s.execRevalidateUpdate(ctx, now, accountID, fingerprints, expectedConfigRevision)
		if err != nil {
			return RevalidateResult{}, err
		}
		if retried <= 0 {
			return RevalidateResult{Reason: ReasonNoRevalidatableKey}, nil
		}
		if err := s.markRuntimeStateChanged(ctx, accountID); err != nil {
			return RevalidateResult{}, err
		}
		return RevalidateResult{Changed: retried, Eligible: true}, nil
	}
	if err := s.markRuntimeStateChanged(ctx, accountID); err != nil {
		return RevalidateResult{}, err
	}
	return RevalidateResult{Changed: changed, Eligible: true}, nil
}

// loadRevalidateAccountRow 等价 revalidate 的首个账户读取（含凭据）。
func (s *Store) loadRevalidateAccountRow(ctx context.Context, accountID string) (*revalidateAccountRow, error) {
	query := s.bind(fmt.Sprintf(`SELECT provider_code, protocol_code, protocol_version, type, status, schedulable, config_revision, credentials_encrypted
      FROM %s WHERE id = ? AND deleted_at IS NULL`, s.businessTable("accounts")))
	var row revalidateAccountRow
	var credentials sql.NullString
	if err := s.db.QueryRowContext(ctx, query, accountID).Scan(
		&row.providerCode, &row.protocolCode, &row.protocolVersion, &row.accountType,
		&row.status, &row.schedulable, &row.configRevision, &credentials); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	row.credentialsEncrypted = credentials.String
	return &row, nil
}

type revalidateGateRow struct {
	status         string
	schedulable    int64
	configRevision int64
}

// loadRevalidateGateRow 等价 changed==0 分支的重读取。
func (s *Store) loadRevalidateGateRow(ctx context.Context, accountID string) (*revalidateGateRow, error) {
	query := s.bind(fmt.Sprintf(`SELECT status, schedulable, config_revision FROM %s WHERE id = ? AND deleted_at IS NULL`, s.businessTable("accounts")))
	var row revalidateGateRow
	if err := s.db.QueryRowContext(ctx, query, accountID).Scan(&row.status, &row.schedulable, &row.configRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// revalidateGateReason 等价首个账户读取的四段门槛（Node 顺序：not_found →
// config_revision_conflict → account_not_active → account_unschedulable）。
func revalidateGateReason(row *revalidateAccountRow, expectedConfigRevision int64) (string, bool) {
	if row == nil {
		return ReasonAccountNotFound, false
	}
	return revalidateGateReasonFromGate(&revalidateGateRow{
		status:         row.status,
		schedulable:    row.schedulable,
		configRevision: row.configRevision,
	}, expectedConfigRevision)
}

func revalidateGateReasonFromGate(row *revalidateGateRow, expectedConfigRevision int64) (string, bool) {
	if row == nil {
		return ReasonAccountNotFound, false
	}
	if row.configRevision != expectedConfigRevision {
		return ReasonConfigRevisionConflict, false
	}
	if row.status != "active" {
		return ReasonAccountNotActive, false
	}
	if row.schedulable != 1 {
		return ReasonAccountUnschedulable, false
	}
	return "", true
}

// revalidateUpdateSQL 渲染重校验 UPDATE（PG 用 AS states 别名，SQLite 裸表，
// 与 Node 两个变体一一对应）。
func (s *Store) revalidateUpdateSQL(fingerprints int) string {
	if s.postgres {
		return s.bind(fmt.Sprintf(`
    UPDATE %s AS states
    SET status = CASE WHEN states.status = 'error' THEN 'unverified' ELSE states.status END,
        recovery_started_at = NULL,
        cooldown_until = NULL,
        next_probe_at = ?, last_attempt_at = ?, updated_at = ?
    WHERE states.account_id = ?
      AND states.key_fingerprint IN (%s)
      AND states.status NOT IN ('active', 'disabled')
      AND (states.probe_claimed_until IS NULL OR states.probe_claimed_until <= ?)
      AND EXISTS (
        SELECT 1 FROM %s accounts
        WHERE accounts.id = states.account_id
          AND accounts.config_revision = ?
          AND accounts.status = 'active'
          AND accounts.schedulable = 1
          AND accounts.deleted_at IS NULL
      )
  `, s.statesTable(), placeholders(fingerprints), s.businessTable("accounts")))
	}
	return s.bind(fmt.Sprintf(`
    UPDATE %s
    SET status = CASE WHEN status = 'error' THEN 'unverified' ELSE status END,
        recovery_started_at = NULL,
        cooldown_until = NULL,
        next_probe_at = ?, last_attempt_at = ?, updated_at = ?
    WHERE account_id = ?
      AND key_fingerprint IN (%s)
      AND status NOT IN ('active', 'disabled')
      AND (probe_claimed_until IS NULL OR probe_claimed_until <= ?)
      AND EXISTS (
        SELECT 1 FROM %s
        WHERE %s.id = %s.account_id
          AND %s.config_revision = ?
          AND %s.status = 'active'
          AND %s.schedulable = 1
          AND %s.deleted_at IS NULL
      )
  `, s.statesTable(), placeholders(fingerprints),
		s.businessTable("accounts"), s.businessTable("accounts"),
		s.statesTable(), s.businessTable("accounts"),
		s.businessTable("accounts"), s.businessTable("accounts"),
		s.businessTable("accounts")))
}

// execRevalidateUpdate 执行重校验 UPDATE 并返回受影响行数。
func (s *Store) execRevalidateUpdate(ctx context.Context, now, accountID string, fingerprints []string, expectedConfigRevision int64) (int, error) {
	query := s.revalidateUpdateSQL(len(fingerprints))
	args := make([]any, 0, 3+len(fingerprints)+1+1)
	args = append(args, s.instantParam(now), s.instantParam(now), now, accountID)
	for _, fingerprint := range fingerprints {
		args = append(args, fingerprint)
	}
	args = append(args, s.instantParam(now), expectedConfigRevision)
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(changed), nil
}

// hasRevalidatableCandidate 等价 changed==0 分支的候选存在性探测。
func (s *Store) hasRevalidatableCandidate(ctx context.Context, accountID string, fingerprints []string, now string) (bool, error) {
	query := s.bind(fmt.Sprintf(`
    SELECT 1 FROM %s
    WHERE account_id = ?
      AND key_fingerprint IN (%s)
      AND status NOT IN ('active', 'disabled')
      AND (probe_claimed_until IS NULL OR probe_claimed_until <= ?)
    LIMIT 1
  `, s.statesTable(), placeholders(len(fingerprints))))
	args := make([]any, 0, 1+len(fingerprints)+1)
	args = append(args, accountID)
	for _, fingerprint := range fingerprints {
		args = append(args, fingerprint)
	}
	args = append(args, s.instantParam(now))
	var one int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// markRuntimeStateChanged 等价 markRuntimeStateChanged(Async)：来源账户 +
// 授权实例同源账户一并标脏，写 group_account_stats_dirty（reason 固定
// account_api_key_runtime），并触发进程内运行态缓存失效通知。
func (s *Store) markRuntimeStateChanged(ctx context.Context, sourceAccountID string) error {
	affected, err := s.accountIdsAffectedBySourceAccount(ctx, sourceAccountID)
	if err != nil {
		return err
	}
	if len(affected) == 0 {
		affected = []string{sourceAccountID}
	}
	if err := s.markGroupAccountStatsDirty(ctx, affected); err != nil {
		return err
	}
	if s.inval != nil {
		s.inval(statsDirtyReason)
	}
	return nil
}

// accountIdsAffectedBySourceAccount 等价 accountIdsAffectedBySourceAccountAsync。
func (s *Store) accountIdsAffectedBySourceAccount(ctx context.Context, sourceAccountID string) ([]string, error) {
	query := s.bind(fmt.Sprintf(`
    SELECT id FROM %s
    WHERE id = ? OR authorization_instance_source_account_id = ?
  `, s.businessTable("accounts")))
	rows, err := s.db.QueryContext(ctx, query, sourceAccountID, sourceAccountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id.String != "" {
			ids = append(ids, id.String)
		}
	}
	return ids, rows.Err()
}

// markGroupAccountStatsDirty 等价 markGroupAccountStatsDirtyByAccountIdsAsync
// （900 分批查 group_accounts DISTINCT group_id，逐 group upsert 脏标记）。
func (s *Store) markGroupAccountStatsDirty(ctx context.Context, accountIds []string) error {
	ids := normalizeAccountIds(accountIds)
	if len(ids) == 0 {
		return nil
	}
	groupIds := []string{}
	seen := map[string]bool{}
	queryTemplate := `
    SELECT DISTINCT group_id FROM %s WHERE account_id IN (%s)
  `
	for _, chunk := range chunkValues(ids, 900) {
		query := s.bind(fmt.Sprintf(queryTemplate, s.businessTable("group_accounts"), placeholders(len(chunk))))
		args := make([]any, len(chunk))
		for index, id := range chunk {
			args[index] = id
		}
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var groupID sql.NullString
			if err := rows.Scan(&groupID); err != nil {
				rows.Close()
				return err
			}
			if groupID.String != "" && !seen[groupID.String] {
				seen[groupID.String] = true
				groupIds = append(groupIds, groupID.String)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	if len(groupIds) == 0 {
		return nil
	}
	updatedAt := s.nowISO()
	upsert := s.bind(fmt.Sprintf(`
    INSERT INTO %s (group_id, reason, updated_at)
    VALUES (?, ?, ?)
    ON CONFLICT (group_id) DO UPDATE SET
      reason = excluded.reason,
      updated_at = excluded.updated_at
  `, s.businessTable("group_account_stats_dirty")))
	for _, groupID := range groupIds {
		if _, err := s.db.ExecContext(ctx, upsert, groupID, statsDirtyReason, updatedAt); err != nil {
			return err
		}
	}
	return nil
}
