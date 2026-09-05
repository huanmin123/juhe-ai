package cleanuprepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// api-key-record-cleanup.ts / account-record-cleanup.ts PostgreSQL 高性能模式
// 主流程移植（Node 权威：final-archive/backend/src/storage 两文件的 *Async PG
// 分支）。覆盖：
//
//   - juhe_dataset 清理目标表 PG 语句（upsert / deferred / error / clear / list）
//   - juhe_stats.stats_job_state 全局 floor 游标（usage_stats_aggregation +
//     client_ip_stats_aggregation 双游标齐备才放行）
//   - juhe_usage.usage_records 批次选择 + 批内事务（扣减台账 → 目录收缩 →
//     分区键删除 → shard_deleted 台账标记）
//   - juhe_stats.usage_record_cleanup_deductions 台账（source_shard_key 固定
//     'postgres'，INSERT ... ON CONFLICT 以 EXCLUDED 覆盖归属并保留
//     stats_subtracted_at 单次扣减语义）
//   - scope stats 清理（deletePostgresApiKeyScopeStatsRows /
//     deletePostgresAccountScopeStatsRows）与残余检查
//     （hasPostgresDeletedApiKeyStatsRows / hasPostgresDeletedAccountStatsRows）
//
// 语句文本与参数顺序逐字段对照 Node PG 路径（juhe_stats/juhe_usage/juhe_dataset
// schema 限定、$n 占位、ANY(?::text[]) 数组参数）。

const postgresUsageRecordCleanupDeductionShardKey = "postgres"

// ---- 清理目标表（juhe_dataset）----

func (s *RecordCleanupStore) upsertPostgresAPIKeyCleanupTarget(ctx context.Context, apiKeyID, systemAccountID, updatedAt string) error {
	_, err := execChangedQ(ctx, s.Dataset, s.Dataset.Bind(`
    INSERT INTO juhe_dataset.api_key_record_cleanup_targets (api_key_id, system_account_id, created_at, updated_at)
    VALUES (?, ?, ?, ?)
    ON CONFLICT(api_key_id) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      updated_at = excluded.updated_at
  `), apiKeyID, systemAccountID, updatedAt, updatedAt)
	return err
}

func (s *RecordCleanupStore) markPostgresAPIKeyCleanupTargetDeferred(ctx context.Context, apiKeyID, systemAccountID, blockedReason, updatedAt string) error {
	_, err := execChangedQ(ctx, s.Dataset, s.Dataset.Bind(`
    UPDATE juhe_dataset.api_key_record_cleanup_targets
    SET attempt_count = attempt_count + 1,
        last_attempt_at = ?,
        last_blocked_reason = ?,
        last_error_message = NULL,
        updated_at = ?
    WHERE api_key_id = ? AND system_account_id = ?
  `), updatedAt, blockedReason, updatedAt, apiKeyID, systemAccountID)
	return err
}

func (s *RecordCleanupStore) markPostgresAPIKeyCleanupTargetError(ctx context.Context, apiKeyID, systemAccountID, message, updatedAt string) error {
	_, err := execChangedQ(ctx, s.Dataset, s.Dataset.Bind(`
    UPDATE juhe_dataset.api_key_record_cleanup_targets
    SET attempt_count = attempt_count + 1,
        last_attempt_at = ?,
        last_blocked_reason = NULL,
        last_error_message = ?,
        updated_at = ?
    WHERE api_key_id = ? AND system_account_id = ?
  `), updatedAt, message, updatedAt, apiKeyID, systemAccountID)
	return err
}

func (s *RecordCleanupStore) clearPostgresAPIKeyCleanupTarget(ctx context.Context, apiKeyID, systemAccountID string) error {
	_, err := execChangedQ(ctx, s.Dataset, s.Dataset.Bind(
		`DELETE FROM juhe_dataset.api_key_record_cleanup_targets WHERE api_key_id = ? AND system_account_id = ?`),
		apiKeyID, systemAccountID)
	return err
}

func (s *RecordCleanupStore) listPostgresAPIKeyCleanupTargets(ctx context.Context, limit int) ([]retention.APIKeyCleanupTarget, error) {
	rows, err := queryRows(ctx, s.Dataset, s.Dataset.Bind(`
    SELECT api_key_id, system_account_id
    FROM juhe_dataset.api_key_record_cleanup_targets
    ORDER BY COALESCE(last_attempt_at, created_at) ASC, created_at ASC, api_key_id ASC
    LIMIT ?
  `), batchLimit(limit))
	if err != nil {
		return nil, err
	}
	var targets []retention.APIKeyCleanupTarget
	for _, row := range rows {
		target := retention.APIKeyCleanupTarget{
			APIKeyID:        textOf(row["api_key_id"]),
			SystemAccountID: textOf(row["system_account_id"]),
		}
		if target.APIKeyID != "" && target.SystemAccountID != "" {
			targets = append(targets, target)
		}
	}
	return targets, nil
}

func (s *RecordCleanupStore) upsertPostgresAccountCleanupTarget(ctx context.Context, target retention.ExpiredDeletedAccountTarget, updatedAt string) error {
	_, err := execChangedQ(ctx, s.Dataset, s.Dataset.Bind(`
    INSERT INTO juhe_dataset.account_record_cleanup_targets (
      account_id, system_account_id, related_account_ids_json, authorization_ids_json, team_scope_ids_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(account_id) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      related_account_ids_json = CASE
        WHEN excluded.related_account_ids_json <> '[]' THEN excluded.related_account_ids_json
        ELSE account_record_cleanup_targets.related_account_ids_json
      END,
      authorization_ids_json = CASE
        WHEN excluded.authorization_ids_json <> '[]' THEN excluded.authorization_ids_json
        ELSE account_record_cleanup_targets.authorization_ids_json
      END,
      team_scope_ids_json = CASE
        WHEN excluded.team_scope_ids_json <> '[]' THEN excluded.team_scope_ids_json
        ELSE account_record_cleanup_targets.team_scope_ids_json
      END,
      updated_at = excluded.updated_at
  `),
		target.AccountID, target.SystemAccountID,
		stringArrayJSON(target.RelatedAccountIDs), stringArrayJSON(target.AuthorizationIDs), stringArrayJSON(target.TeamScopeIDs),
		updatedAt, updatedAt)
	return err
}

func (s *RecordCleanupStore) markPostgresAccountCleanupTargetDeferred(ctx context.Context, target retention.ExpiredDeletedAccountTarget, blockedReason, updatedAt string) error {
	_, err := execChangedQ(ctx, s.Dataset, s.Dataset.Bind(`
    UPDATE juhe_dataset.account_record_cleanup_targets
    SET attempt_count = attempt_count + 1,
        last_attempt_at = ?,
        last_blocked_reason = ?,
        last_error_message = NULL,
        updated_at = ?
    WHERE account_id = ? AND system_account_id = ?
  `), updatedAt, blockedReason, updatedAt, target.AccountID, target.SystemAccountID)
	return err
}

func (s *RecordCleanupStore) markPostgresAccountCleanupTargetError(ctx context.Context, target retention.ExpiredDeletedAccountTarget, message, updatedAt string) error {
	_, err := execChangedQ(ctx, s.Dataset, s.Dataset.Bind(`
    UPDATE juhe_dataset.account_record_cleanup_targets
    SET attempt_count = attempt_count + 1,
        last_attempt_at = ?,
        last_blocked_reason = NULL,
        last_error_message = ?,
        updated_at = ?
    WHERE account_id = ? AND system_account_id = ?
  `), updatedAt, message, updatedAt, target.AccountID, target.SystemAccountID)
	return err
}

func (s *RecordCleanupStore) clearPostgresAccountCleanupTarget(ctx context.Context, target retention.ExpiredDeletedAccountTarget) error {
	_, err := execChangedQ(ctx, s.Dataset, s.Dataset.Bind(
		`DELETE FROM juhe_dataset.account_record_cleanup_targets WHERE account_id = ? AND system_account_id = ?`),
		target.AccountID, target.SystemAccountID)
	return err
}

func (s *RecordCleanupStore) listPostgresAccountCleanupTargets(ctx context.Context, limit int) ([]retention.ExpiredDeletedAccountTarget, error) {
	rows, err := queryRows(ctx, s.Dataset, s.Dataset.Bind(`
    SELECT account_id, system_account_id,
      related_account_ids_json, authorization_ids_json, team_scope_ids_json
    FROM juhe_dataset.account_record_cleanup_targets
    ORDER BY COALESCE(last_attempt_at, created_at) ASC, created_at ASC, account_id ASC
    LIMIT ?
  `), batchLimit(limit))
	if err != nil {
		return nil, err
	}
	var targets []retention.ExpiredDeletedAccountTarget
	for _, row := range rows {
		target := retention.ExpiredDeletedAccountTarget{
			AccountID:         textOf(row["account_id"]),
			SystemAccountID:   textOf(row["system_account_id"]),
			RelatedAccountIDs: parseStringArrayJSON(textOf(row["related_account_ids_json"])),
			AuthorizationIDs:  parseStringArrayJSON(textOf(row["authorization_ids_json"])),
			TeamScopeIDs:      parseStringArrayJSON(textOf(row["team_scope_ids_json"])),
		}
		if target.AccountID != "" && target.SystemAccountID != "" {
			targets = append(targets, target)
		}
	}
	return targets, nil
}

// ---- floor 游标与批次选择 ----

// postgresUsageRecordCleanupFloorCursor 照 postgresUsageRecordCleanupFloorCursor：
// 全局聚合双游标（usage_stats_aggregation / client_ip_stats_aggregation）齐备
// 时取最早的 cursor，否则返回 nil（本批不清理）。
func (s *RecordCleanupStore) postgresUsageRecordCleanupFloorCursor(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (*cleanupCursor, error) {
	rows, err := queryRows(ctx, queryer, s.Stats.Bind(`
    SELECT job_name, cursor_created_at, cursor_id
    FROM juhe_stats.stats_job_state
    WHERE scope_type = 'global'
      AND scope_id = ''
      AND job_name = ANY(?::text[])
      AND cursor_created_at IS NOT NULL
      AND cursor_id IS NOT NULL
    ORDER BY cursor_created_at ASC, cursor_id ASC
  `), usageRecordCleanupRequiredCursorJobNames)
	if err != nil {
		return nil, err
	}
	jobNames := map[string]bool{}
	var first *cleanupCursor
	for _, row := range rows {
		if name := strings.TrimSpace(textOf(row["job_name"])); name != "" {
			jobNames[name] = true
		}
		if first == nil {
			createdAt := strings.TrimSpace(textOf(row["cursor_created_at"]))
			id := strings.TrimSpace(textOf(row["cursor_id"]))
			if createdAt != "" && id != "" {
				first = &cleanupCursor{CreatedAt: createdAt, ID: id}
			}
		}
	}
	for _, jobName := range usageRecordCleanupRequiredCursorJobNames {
		if !jobNames[jobName] {
			return nil, nil
		}
	}
	return first, nil
}

// selectPostgresAPIKeyUsageRows 照 deletePostgresApiKeyUsageDataBatch 的批次
// 查询（usageStatsRecordSelectColumns 全列，游标内正序）。
func (s *RecordCleanupStore) selectPostgresAPIKeyUsageRows(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, apiKeyID, systemAccountID string, cursor cleanupCursor, limit int) ([]statsagg.UsageStatsRecordRow, error) {
	return queryUsageStatsRows(ctx, queryer, s.Stats.Bind(fmt.Sprintf(`
    SELECT %s
    FROM juhe_usage.usage_records
    WHERE api_key_id = ?
      AND system_account_id = ?
      AND (created_at < ? OR (created_at = ? AND id <= ?))
    ORDER BY created_at ASC, id ASC
    LIMIT ?
  `, usageStatsRecordSelectColumns)), apiKeyID, systemAccountID,
		cursor.CreatedAt, cursor.CreatedAt, cursor.ID, batchLimit(limit))
}

// selectPostgresAccountUsageRows 照 deletePostgresAccountUsageDataBatch 的批次
// 查询（account_id / account_authorization_id 双 ANY 过滤）。
func (s *RecordCleanupStore) selectPostgresAccountUsageRows(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, target retention.ExpiredDeletedAccountTarget, cursor cleanupCursor, limit int) ([]statsagg.UsageStatsRecordRow, error) {
	accountIDs := uniqueNonEmpty(append([]string{target.AccountID}, target.RelatedAccountIDs...))
	authorizationIDs := uniqueNonEmpty(target.AuthorizationIDs)
	return queryUsageStatsRows(ctx, queryer, s.Stats.Bind(fmt.Sprintf(`
    SELECT %s
    FROM juhe_usage.usage_records
    WHERE (account_id = ANY(?::text[]) OR account_authorization_id = ANY(?::text[]))
      AND (created_at < ? OR (created_at = ? AND id <= ?))
    ORDER BY created_at ASC, id ASC
    LIMIT ?
  `, usageStatsRecordSelectColumns)), accountIDs, authorizationIDs,
		cursor.CreatedAt, cursor.CreatedAt, cursor.ID, batchLimit(limit))
}

// ---- 扣减台账（juhe_stats.usage_record_cleanup_deductions）----

// postgresRecordJSONOf 照 Node JSON.stringify(row)：使用行的 IPC JSON 形状
// （不含 source_shard_key；PG 单库行的分片键固定由台账列承载）。
func postgresRecordJSONOf(row statsagg.UsageStatsRecordRow) (string, error) {
	payload := statsaggRowToMap(row, "")
	delete(payload, "source_shard_key")
	data, err := jsonMarshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// subtractPostgresAPIKeyUsageRowsOnce 照 subtractPostgresApiKeyUsageRowsOnce。
func (s *RecordCleanupStore) subtractPostgresAPIKeyUsageRowsOnce(ctx context.Context, tx *sql.Tx, rows []statsagg.UsageStatsRecordRow, apiKeyID, systemAccountID, updatedAt string) error {
	var rowsToSubtract []statsagg.UsageStatsRecordRow
	for _, row := range rows {
		recordJSON, err := postgresRecordJSONOf(row)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(`
      INSERT INTO juhe_stats.usage_record_cleanup_deductions (
        usage_id, api_key_id, account_id, system_account_id, source_shard_key, record_json,
        stats_subtracted_at, shard_deleted_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)
      ON CONFLICT(usage_id, source_shard_key) DO UPDATE SET
        api_key_id = EXCLUDED.api_key_id,
        account_id = COALESCE(usage_record_cleanup_deductions.account_id, EXCLUDED.account_id),
        system_account_id = EXCLUDED.system_account_id,
        record_json = EXCLUDED.record_json,
        updated_at = EXCLUDED.updated_at
    `), row.ID, apiKeyID, optionalTextPtr(row.AccountID), systemAccountID,
			postgresUsageRecordCleanupDeductionShardKey, recordJSON, updatedAt, updatedAt); err != nil {
			return err
		}
		subtracted, err := s.postgresDeductionStatsSubtractedAt(ctx, tx, row.ID)
		if err != nil {
			return err
		}
		if !subtracted {
			rowsToSubtract = append(rowsToSubtract, row)
		}
	}
	if len(rowsToSubtract) == 0 {
		return nil
	}
	if err := s.subtractPostgresUsageStatsRows(ctx, tx, rowsToSubtract, updatedAt, nil); err != nil {
		return err
	}
	usageIDs := make([]string, 0, len(rowsToSubtract))
	for _, row := range rowsToSubtract {
		usageIDs = append(usageIDs, row.ID)
	}
	return s.markPostgresUsageCleanupRowsSubtracted(ctx, tx, usageIDs, updatedAt)
}

// subtractPostgresAccountUsageRowsOnce 照 subtractPostgresAccountUsageRowsOnce
// （account 变体：api_key_id COALESCE 保留、account_id/system_account_id 以
// EXCLUDED 覆盖，行缺省回落目标归属）。
func (s *RecordCleanupStore) subtractPostgresAccountUsageRowsOnce(ctx context.Context, tx *sql.Tx, rows []statsagg.UsageStatsRecordRow, target retention.ExpiredDeletedAccountTarget, updatedAt string) error {
	var rowsToSubtract []statsagg.UsageStatsRecordRow
	for _, row := range rows {
		recordJSON, err := postgresRecordJSONOf(row)
		if err != nil {
			return err
		}
		apiKeyID := ""
		if row.APIKeyID != nil {
			apiKeyID = *row.APIKeyID
		}
		accountID := target.AccountID
		if row.AccountID != nil {
			accountID = *row.AccountID
		}
		systemAccountID := target.SystemAccountID
		if row.SystemAccountID != "" {
			systemAccountID = row.SystemAccountID
		}
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(`
      INSERT INTO juhe_stats.usage_record_cleanup_deductions (
        usage_id, api_key_id, account_id, system_account_id, source_shard_key, record_json,
        stats_subtracted_at, shard_deleted_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)
      ON CONFLICT(usage_id, source_shard_key) DO UPDATE SET
        api_key_id = COALESCE(usage_record_cleanup_deductions.api_key_id, EXCLUDED.api_key_id),
        account_id = EXCLUDED.account_id,
        system_account_id = EXCLUDED.system_account_id,
        record_json = EXCLUDED.record_json,
        updated_at = EXCLUDED.updated_at
    `), row.ID, apiKeyID, accountID, systemAccountID,
			postgresUsageRecordCleanupDeductionShardKey, recordJSON, updatedAt, updatedAt); err != nil {
			return err
		}
		subtracted, err := s.postgresDeductionStatsSubtractedAt(ctx, tx, row.ID)
		if err != nil {
			return err
		}
		if !subtracted {
			rowsToSubtract = append(rowsToSubtract, row)
		}
	}
	if len(rowsToSubtract) == 0 {
		return nil
	}
	if err := s.subtractPostgresUsageStatsRows(ctx, tx, rowsToSubtract, updatedAt, nil); err != nil {
		return err
	}
	usageIDs := make([]string, 0, len(rowsToSubtract))
	for _, row := range rowsToSubtract {
		usageIDs = append(usageIDs, row.ID)
	}
	return s.markPostgresUsageCleanupRowsSubtracted(ctx, tx, usageIDs, updatedAt)
}

// postgresDeductionStatsSubtractedAt 照 Node client.one ... FOR UPDATE 查询：
// 行存在且 stats_subtracted_at 非空返回 true。
func (s *RecordCleanupStore) postgresDeductionStatsSubtractedAt(ctx context.Context, tx *sql.Tx, usageID string) (bool, error) {
	var statsSubtractedAt sql.NullString
	err := tx.QueryRowContext(ctx, s.Stats.Bind(`
      SELECT stats_subtracted_at
      FROM juhe_stats.usage_record_cleanup_deductions
      WHERE usage_id = ? AND source_shard_key = ?
      LIMIT 1
      FOR UPDATE
	`), usageID, postgresUsageRecordCleanupDeductionShardKey).Scan(&statsSubtractedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return statsSubtractedAt.Valid && statsSubtractedAt.String != "", nil
}

func (s *RecordCleanupStore) markPostgresUsageCleanupRowsSubtracted(ctx context.Context, tx *sql.Tx, usageIDs []string, updatedAt string) error {
	if len(usageIDs) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, s.Stats.Bind(`
    UPDATE juhe_stats.usage_record_cleanup_deductions
    SET stats_subtracted_at = COALESCE(stats_subtracted_at, ?),
        updated_at = ?
    WHERE usage_id = ANY(?::text[])
      AND source_shard_key = ?
  `), updatedAt, updatedAt, usageIDs, postgresUsageRecordCleanupDeductionShardKey)
	return err
}

func (s *RecordCleanupStore) markPostgresUsageCleanupRowsDeleted(ctx context.Context, tx *sql.Tx, usageIDs []string, updatedAt string) error {
	if len(usageIDs) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, s.Stats.Bind(`
    UPDATE juhe_stats.usage_record_cleanup_deductions
    SET shard_deleted_at = COALESCE(shard_deleted_at, ?),
        updated_at = ?
    WHERE usage_id = ANY(?::text[])
      AND source_shard_key = ?
  `), updatedAt, updatedAt, usageIDs, postgresUsageRecordCleanupDeductionShardKey)
	return err
}

// ---- 批次删除 ----

// deletePostgresAPIKeyUsageDataBatch 照 deletePostgresApiKeyUsageDataBatch
// （调用方已开启事务）。
func (s *RecordCleanupStore) deletePostgresAPIKeyUsageDataBatch(ctx context.Context, tx *sql.Tx, apiKeyID, systemAccountID string, limit int, updatedAt string) (int64, error) {
	cursor, err := s.postgresUsageRecordCleanupFloorCursor(ctx, tx)
	if err != nil {
		return 0, err
	}
	if cursor == nil {
		return 0, nil
	}
	rows, err := s.selectPostgresAPIKeyUsageRows(ctx, tx, apiKeyID, systemAccountID, *cursor, limit)
	if err != nil {
		return 0, err
	}
	usageIDs := make([]string, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		normalized := strings.TrimSpace(row.ID)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		usageIDs = append(usageIDs, normalized)
	}
	if len(usageIDs) == 0 {
		return 0, nil
	}
	if err := s.subtractPostgresAPIKeyUsageRowsOnce(ctx, tx, rows, apiKeyID, systemAccountID, updatedAt); err != nil {
		return 0, err
	}
	if err := deletePostgresUsageRecordCatalogRowsByUsageIds(ctx, &UsageRecordsStore{Catalog: s.UsageCatalog, Stats: s.Stats}, tx, usageIDs); err != nil {
		return 0, err
	}
	deletedRows, err := s.deletePostgresUsageRecordsByPartitionKeys(ctx, tx, rows)
	if err != nil {
		return 0, err
	}
	if err := s.markPostgresUsageCleanupRowsDeleted(ctx, tx, usageIDs, updatedAt); err != nil {
		return 0, err
	}
	return deletedRows, nil
}

// deletePostgresAccountUsageDataBatch 照 deletePostgresAccountUsageDataBatch。
func (s *RecordCleanupStore) deletePostgresAccountUsageDataBatch(ctx context.Context, tx *sql.Tx, target retention.ExpiredDeletedAccountTarget, limit int, updatedAt string) (int64, error) {
	cursor, err := s.postgresUsageRecordCleanupFloorCursor(ctx, tx)
	if err != nil {
		return 0, err
	}
	if cursor == nil {
		return 0, nil
	}
	accountIDs := uniqueNonEmpty(append([]string{target.AccountID}, target.RelatedAccountIDs...))
	authorizationIDs := uniqueNonEmpty(target.AuthorizationIDs)
	if len(accountIDs) == 0 && len(authorizationIDs) == 0 {
		return 0, nil
	}
	rows, err := s.selectPostgresAccountUsageRows(ctx, tx, target, *cursor, limit)
	if err != nil {
		return 0, err
	}
	usageIDs := make([]string, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		normalized := strings.TrimSpace(row.ID)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		usageIDs = append(usageIDs, normalized)
	}
	if len(usageIDs) == 0 {
		return 0, nil
	}
	if err := s.subtractPostgresAccountUsageRowsOnce(ctx, tx, rows, target, updatedAt); err != nil {
		return 0, err
	}
	if err := deletePostgresUsageRecordCatalogRowsByUsageIds(ctx, &UsageRecordsStore{Catalog: s.UsageCatalog, Stats: s.Stats}, tx, usageIDs); err != nil {
		return 0, err
	}
	deletedRows, err := s.deletePostgresUsageRecordsByPartitionKeys(ctx, tx, rows)
	if err != nil {
		return 0, err
	}
	if err := s.markPostgresUsageCleanupRowsDeleted(ctx, tx, usageIDs, updatedAt); err != nil {
		return 0, err
	}
	return deletedRows, nil
}

// queryUsageStatsRows 按列序扫描 usage_records 全列行。
func queryUsageStatsRows(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args ...any) ([]statsagg.UsageStatsRecordRow, error) {
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanUsageStatsRecordRows(rows)
}

// deletePostgresUsageRecordsByPartitionKeys 照 deletePostgresUsageRecordsByPartitionKeys：
// 按 (created_at, id) 复合键删除 usage_records。
func (s *RecordCleanupStore) deletePostgresUsageRecordsByPartitionKeys(ctx context.Context, tx *sql.Tx, rows []statsagg.UsageStatsRecordRow) (int64, error) {
	type partitionKey struct{ createdAt, id string }
	var keys []partitionKey
	for _, row := range rows {
		createdAt := strings.TrimSpace(row.CreatedAt)
		id := strings.TrimSpace(row.ID)
		if createdAt == "" || id == "" {
			continue
		}
		keys = append(keys, partitionKey{createdAt, id})
	}
	if len(keys) == 0 {
		return 0, nil
	}
	placeholders := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys)*2)
	for _, key := range keys {
		placeholders = append(placeholders, "(?, ?)")
		args = append(args, key.createdAt, key.id)
	}
	result, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(
		`DELETE FROM juhe_usage.usage_records
    WHERE (created_at, id) IN (%s)`, strings.Join(placeholders, ", "))), args...)
	if err != nil {
		return 0, err
	}
	return changes(result)
}

// ---- 存在性检查与 final stats ----

func (s *RecordCleanupStore) hasPostgresAPIKeyUsageRecords(ctx context.Context, apiKeyID, systemAccountID string) (bool, error) {
	rows, err := queryRows(ctx, s.Stats, s.Stats.Bind(`
    SELECT 1 AS found
    FROM juhe_usage.usage_records
    WHERE api_key_id = ?
      AND system_account_id = ?
    LIMIT 1
  `), apiKeyID, systemAccountID)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

func (s *RecordCleanupStore) hasPostgresAccountUsageRecords(ctx context.Context, target retention.ExpiredDeletedAccountTarget) (bool, error) {
	accountIDs := uniqueNonEmpty(append([]string{target.AccountID}, target.RelatedAccountIDs...))
	authorizationIDs := uniqueNonEmpty(target.AuthorizationIDs)
	if len(accountIDs) == 0 && len(authorizationIDs) == 0 {
		return false, nil
	}
	rows, err := queryRows(ctx, s.Stats, s.Stats.Bind(`
    SELECT 1 AS found
    FROM juhe_usage.usage_records
    WHERE account_id = ANY(?::text[])
      OR account_authorization_id = ANY(?::text[])
    LIMIT 1
  `), accountIDs, authorizationIDs)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

func postgresRowsExist(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args ...any) (bool, error) {
	rows, err := queryRows(ctx, queryer, query, args...)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

func (s *RecordCleanupStore) hasPostgresAPIKeyStatsRows(ctx context.Context, apiKeyID, systemAccountID string) (bool, error) {
	for _, tableName := range apiKeyScopeStatsTables {
		exists, err := postgresRowsExist(ctx, s.Stats, s.Stats.Bind(fmt.Sprintf(`
    SELECT 1
    FROM juhe_stats.%s
    WHERE system_account_id = ?
      AND scope_type = 'api_key'
      AND scope_id = ?
    LIMIT 1
  `, tableName)), systemAccountID, apiKeyID)
		if err != nil || exists {
			return exists, err
		}
	}
	return postgresRowsExist(ctx, s.Stats, s.Stats.Bind(`
    SELECT 1
    FROM juhe_stats.usage_record_cleanup_deductions
    WHERE api_key_id = ?
      AND system_account_id = ?
    LIMIT 1
  `), apiKeyID, systemAccountID)
}

func (s *RecordCleanupStore) hasPostgresAccountStatsRows(ctx context.Context, target retention.ExpiredDeletedAccountTarget) (bool, error) {
	accountIDs := uniqueNonEmpty(append([]string{target.AccountID}, target.RelatedAccountIDs...))
	authorizationIDs := uniqueNonEmpty(target.AuthorizationIDs)
	teamScopeIDs := uniqueNonEmpty(target.TeamScopeIDs)
	for _, tableName := range accountScopeStatsTables {
		if len(accountIDs) > 0 {
			exists, err := postgresRowsExist(ctx, s.Stats, s.Stats.Bind(fmt.Sprintf(`
      SELECT 1
      FROM juhe_stats.%s
      WHERE scope_type IN ('account', 'caller_account')
        AND scope_id = ANY(?::text[])
      LIMIT 1
    `, tableName)), accountIDs)
			if err != nil || exists {
				return exists, err
			}
		}
		if len(authorizationIDs) > 0 {
			exists, err := postgresRowsExist(ctx, s.Stats, s.Stats.Bind(fmt.Sprintf(`
      SELECT 1
      FROM juhe_stats.%s
      WHERE scope_type = 'account_authorization'
        AND scope_id = ANY(?::text[])
      LIMIT 1
    `, tableName)), authorizationIDs)
			if err != nil || exists {
				return exists, err
			}
		}
		if len(teamScopeIDs) > 0 {
			exists, err := postgresRowsExist(ctx, s.Stats, s.Stats.Bind(fmt.Sprintf(`
      SELECT 1
      FROM juhe_stats.%s
      WHERE scope_type = 'account_authorization_team'
        AND scope_id = ANY(?::text[])
      LIMIT 1
    `, tableName)), teamScopeIDs)
			if err != nil || exists {
				return exists, err
			}
		}
	}
	if len(accountIDs) > 0 {
		return postgresRowsExist(ctx, s.Stats, s.Stats.Bind(`
    SELECT 1
    FROM juhe_stats.usage_record_cleanup_deductions
    WHERE account_id = ANY(?::text[])
    LIMIT 1
  `), accountIDs)
	}
	return false, nil
}

// cleanupPostgresAPIKeyFinalStats 照 cleanupDeletedApiKeyFinalStatsAsync。
func (s *RecordCleanupStore) cleanupPostgresAPIKeyFinalStats(ctx context.Context, apiKeyID, systemAccountID string) error {
	tx, err := s.Stats.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.deletePostgresAPIKeyScopeStatsRows(ctx, tx, apiKeyID, systemAccountID); err != nil {
		return err
	}
	if err := s.deletePostgresAPIKeyUsageCleanupDeductions(ctx, tx, apiKeyID, systemAccountID); err != nil {
		return err
	}
	return tx.Commit()
}

// deletePostgresAPIKeyScopeStatsRows 照 deletePostgresApiKeyScopeStatsRows。
func (s *RecordCleanupStore) deletePostgresAPIKeyScopeStatsRows(ctx context.Context, tx *sql.Tx, apiKeyID, systemAccountID string) error {
	for _, tableName := range apiKeyScopeStatsTables {
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(
			`DELETE FROM juhe_stats.%s WHERE system_account_id = ? AND scope_type = 'api_key' AND scope_id = ?`, tableName)),
			systemAccountID, apiKeyID); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, s.Stats.Bind(
		`DELETE FROM juhe_stats.stats_job_state WHERE scope_type = 'api_key' AND scope_id = ?`), apiKeyID)
	return err
}

func (s *RecordCleanupStore) deletePostgresAPIKeyUsageCleanupDeductions(ctx context.Context, tx *sql.Tx, apiKeyID, systemAccountID string) error {
	_, err := tx.ExecContext(ctx, s.Stats.Bind(
		`DELETE FROM juhe_stats.usage_record_cleanup_deductions WHERE api_key_id = ? AND system_account_id = ?`),
		apiKeyID, systemAccountID)
	return err
}

// cleanupPostgresAccountFinalStats 照 cleanupDeletedAccountFinalStatsAsync。
func (s *RecordCleanupStore) cleanupPostgresAccountFinalStats(ctx context.Context, target retention.ExpiredDeletedAccountTarget) error {
	tx, err := s.Stats.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.deletePostgresAccountScopeStatsRows(ctx, tx, target, target.AuthorizationIDs, target.TeamScopeIDs); err != nil {
		return err
	}
	if err := s.deletePostgresAccountUsageCleanupDeductions(ctx, tx, target); err != nil {
		return err
	}
	return tx.Commit()
}

// deletePostgresAccountScopeStatsRows 照 deletePostgresAccountScopeStatsRows。
func (s *RecordCleanupStore) deletePostgresAccountScopeStatsRows(ctx context.Context, tx *sql.Tx, target retention.ExpiredDeletedAccountTarget, authorizationIDs, teamScopeIDs []string) error {
	accountIDs := uniqueNonEmpty(append([]string{target.AccountID}, target.RelatedAccountIDs...))
	normalizedAuthorizationIDs := uniqueNonEmpty(authorizationIDs)
	normalizedTeamScopeIDs := uniqueNonEmpty(teamScopeIDs)
	for _, tableName := range accountScopeStatsTables {
		if len(accountIDs) > 0 {
			if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(
				`DELETE FROM juhe_stats.%s WHERE scope_type IN ('account', 'caller_account') AND scope_id = ANY(?::text[])`, tableName)),
				accountIDs); err != nil {
				return err
			}
			for _, accountID := range accountIDs {
				if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(
					`DELETE FROM juhe_stats.%s WHERE scope_type = 'account_authorization_team' AND scope_id LIKE ? ESCAPE '\\'`, tableName)),
					escapeLikePrefix(accountID)+":%"); err != nil {
					return err
				}
			}
		}
		for _, chunk := range chunkValues(normalizedAuthorizationIDs, 900) {
			if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(
				`DELETE FROM juhe_stats.%s WHERE scope_type = 'account_authorization' AND scope_id = ANY(?::text[])`, tableName)),
				chunk); err != nil {
				return err
			}
		}
		for _, chunk := range chunkValues(normalizedTeamScopeIDs, 900) {
			if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(
				`DELETE FROM juhe_stats.%s WHERE scope_type = 'account_authorization_team' AND scope_id = ANY(?::text[])`, tableName)),
				chunk); err != nil {
				return err
			}
		}
	}
	if len(accountIDs) > 0 {
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(
			`DELETE FROM juhe_stats.stats_job_state WHERE scope_type IN ('account', 'caller_account') AND scope_id = ANY(?::text[])`),
			accountIDs); err != nil {
			return err
		}
		for _, accountID := range accountIDs {
			if _, err := tx.ExecContext(ctx, s.Stats.Bind(
				`DELETE FROM juhe_stats.stats_job_state WHERE scope_type = 'account_authorization_team' AND scope_id LIKE ? ESCAPE '\\'`),
				escapeLikePrefix(accountID)+":%"); err != nil {
				return err
			}
		}
		for _, statement := range []struct {
			table string
		}{
			{"account_quality_scores"},
			{"account_quality_dirty_accounts"},
			{"account_quality_minute_stats"},
			{"account_health_hourly"},
			{"account_usage_snapshots"},
		} {
			if _, err := tx.ExecContext(ctx, s.Stats.Bind(
				`DELETE FROM juhe_stats.`+statement.table+` WHERE account_id = ANY(?::text[])`), accountIDs); err != nil {
				return err
			}
		}
		if err := s.deletePostgresAccountAuthorizationReportRows(ctx, tx, accountIDs); err != nil {
			return err
		}
	}
	for _, chunk := range chunkValues(normalizedAuthorizationIDs, 900) {
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(
			`DELETE FROM juhe_stats.stats_job_state WHERE scope_type = 'account_authorization' AND scope_id = ANY(?::text[])`), chunk); err != nil {
			return err
		}
	}
	for _, chunk := range chunkValues(normalizedTeamScopeIDs, 900) {
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(
			`DELETE FROM juhe_stats.stats_job_state WHERE scope_type = 'account_authorization_team' AND scope_id = ANY(?::text[])`), chunk); err != nil {
			return err
		}
	}
	return nil
}

// deletePostgresAccountAuthorizationReportRows 照
// deletePostgresAccountAuthorizationReportRows（四张授权日报表）。
func (s *RecordCleanupStore) deletePostgresAccountAuthorizationReportRows(ctx context.Context, tx *sql.Tx, accountIDs []string) error {
	for _, tableName := range accountAuthorizationReportTables {
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(
			`DELETE FROM juhe_stats.%s WHERE resource_filter_type = 'account' AND resource_filter_id = ANY(?::text[])`, tableName)),
			accountIDs); err != nil {
			return err
		}
	}
	return nil
}

func (s *RecordCleanupStore) deletePostgresAccountUsageCleanupDeductions(ctx context.Context, tx *sql.Tx, target retention.ExpiredDeletedAccountTarget) error {
	accountIDs := uniqueNonEmpty(append([]string{target.AccountID}, target.RelatedAccountIDs...))
	if len(accountIDs) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, s.Stats.Bind(
		`DELETE FROM juhe_stats.usage_record_cleanup_deductions WHERE account_id = ANY(?::text[])`), accountIDs)
	return err
}

// ---- 对外入口（组合根接线）----

// runPostgresBatchInTx 在独立事务内执行批次删除（Node client.transaction 等价）。
func (s *RecordCleanupStore) runPostgresBatchInTx(ctx context.Context, run func(tx *sql.Tx) (int64, error)) (int64, error) {
	tx, err := s.Stats.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	deletedRows, err := run(tx)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deletedRows, nil
}

// CleanupAPIKeyRelatedPostgres 照 cleanupDeletedApiKeyRelatedRecordDataCorePostgresAsync。
func (s *RecordCleanupStore) CleanupAPIKeyRelatedPostgres(ctx context.Context, apiKeyID, systemAccountID string) (retention.RelatedCleanupResult, error) {
	updatedAt := s.nowIso()
	if err := s.upsertPostgresAPIKeyCleanupTarget(ctx, apiKeyID, systemAccountID, updatedAt); err != nil {
		return retention.RelatedCleanupResult{}, err
	}
	deletedRows, err := s.runPostgresBatchInTx(ctx, func(tx *sql.Tx) (int64, error) {
		return s.deletePostgresAPIKeyUsageDataBatch(ctx, tx, apiKeyID, systemAccountID, recordCleanupBatchLimit, updatedAt)
	})
	if err != nil {
		_ = s.markPostgresAPIKeyCleanupTargetError(ctx, apiKeyID, systemAccountID, err.Error(), s.nowIso())
		return retention.RelatedCleanupResult{}, err
	}
	hasUsageMore, err := s.hasPostgresAPIKeyUsageRecords(ctx, apiKeyID, systemAccountID)
	if err != nil {
		_ = s.markPostgresAPIKeyCleanupTargetError(ctx, apiKeyID, systemAccountID, err.Error(), s.nowIso())
		return retention.RelatedCleanupResult{}, err
	}
	hasMore := hasUsageMore
	if !hasMore {
		if err := s.cleanupPostgresAPIKeyFinalStats(ctx, apiKeyID, systemAccountID); err != nil {
			_ = s.markPostgresAPIKeyCleanupTargetError(ctx, apiKeyID, systemAccountID, err.Error(), s.nowIso())
			return retention.RelatedCleanupResult{}, err
		}
	}
	statsRows, err := s.hasPostgresAPIKeyStatsRows(ctx, apiKeyID, systemAccountID)
	if err != nil {
		_ = s.markPostgresAPIKeyCleanupTargetError(ctx, apiKeyID, systemAccountID, err.Error(), s.nowIso())
		return retention.RelatedCleanupResult{}, err
	}
	hasMore = hasMore || statsRows
	result := retention.RelatedCleanupResult{DeletedRows: deletedRows, HasMore: hasMore}
	if hasMore {
		result.BlockedReason = cleanupPendingReason(hasUsageMore, false)
	}
	if result.HasMore || result.BlockedReason != "" {
		reason := result.BlockedReason
		if reason == "" {
			reason = "等待高性能模式后续批次清理"
		}
		if err := s.markPostgresAPIKeyCleanupTargetDeferred(ctx, apiKeyID, systemAccountID, reason, updatedAt); err != nil {
			return result, err
		}
	} else if err := s.clearPostgresAPIKeyCleanupTarget(ctx, apiKeyID, systemAccountID); err != nil {
		return result, err
	}
	return result, nil
}

// CleanupPendingAPIKeyTargetsPostgres 照 cleanupPendingDeletedApiKeyRecordTargetsAsync
// 的 PG 分支（statsWriter 不参与，Node 同语义）。
func (s *RecordCleanupStore) CleanupPendingAPIKeyTargetsPostgres(ctx context.Context, limit int) (retention.PendingCleanupSummary, error) {
	targets, err := s.listPostgresAPIKeyCleanupTargets(ctx, batchLimit(limit))
	if err != nil {
		return retention.PendingCleanupSummary{}, err
	}
	summary := retention.PendingCleanupSummary{}
	for _, target := range targets {
		summary.Attempted++
		result, err := s.CleanupAPIKeyRelatedPostgres(ctx, target.APIKeyID, target.SystemAccountID)
		if err != nil {
			summary.Failed++
			if markErr := s.markPostgresAPIKeyCleanupTargetError(ctx, target.APIKeyID, target.SystemAccountID, err.Error(), s.nowIso()); markErr != nil {
				return summary, markErr
			}
			continue
		}
		summary.DeletedRows += result.DeletedRows
		if result.HasMore || result.BlockedReason != "" {
			summary.Deferred++
		} else {
			summary.Completed++
		}
	}
	return summary, nil
}

// CleanupAccountRelatedPostgres 照 cleanupDeletedAccountRelatedRecordDataCorePostgresAsync。
func (s *RecordCleanupStore) CleanupAccountRelatedPostgres(ctx context.Context, target retention.ExpiredDeletedAccountTarget) (retention.RelatedCleanupResult, error) {
	updatedAt := s.nowIso()
	if err := s.upsertPostgresAccountCleanupTarget(ctx, target, updatedAt); err != nil {
		return retention.RelatedCleanupResult{}, err
	}
	deletedRows, err := s.runPostgresBatchInTx(ctx, func(tx *sql.Tx) (int64, error) {
		return s.deletePostgresAccountUsageDataBatch(ctx, tx, target, recordCleanupBatchLimit, updatedAt)
	})
	if err != nil {
		_ = s.markPostgresAccountCleanupTargetError(ctx, target, err.Error(), s.nowIso())
		return retention.RelatedCleanupResult{}, err
	}
	hasUsageMore, err := s.hasPostgresAccountUsageRecords(ctx, target)
	if err != nil {
		_ = s.markPostgresAccountCleanupTargetError(ctx, target, err.Error(), s.nowIso())
		return retention.RelatedCleanupResult{}, err
	}
	hasMore := hasUsageMore
	if !hasMore {
		if err := s.cleanupPostgresAccountFinalStats(ctx, target); err != nil {
			_ = s.markPostgresAccountCleanupTargetError(ctx, target, err.Error(), s.nowIso())
			return retention.RelatedCleanupResult{}, err
		}
	}
	statsRows, err := s.hasPostgresAccountStatsRows(ctx, target)
	if err != nil {
		_ = s.markPostgresAccountCleanupTargetError(ctx, target, err.Error(), s.nowIso())
		return retention.RelatedCleanupResult{}, err
	}
	hasMore = hasMore || statsRows
	result := retention.RelatedCleanupResult{DeletedRows: deletedRows, HasMore: hasMore}
	if hasMore {
		result.BlockedReason = cleanupPendingReason(hasUsageMore, false)
	}
	if result.HasMore || result.BlockedReason != "" {
		reason := result.BlockedReason
		if reason == "" {
			reason = "等待高性能模式后续批次清理"
		}
		if err := s.markPostgresAccountCleanupTargetDeferred(ctx, target, reason, updatedAt); err != nil {
			return result, err
		}
	} else if err := s.clearPostgresAccountCleanupTarget(ctx, target); err != nil {
		return result, err
	}
	return result, nil
}

// CleanupPendingAccountTargetsPostgres 照 cleanupPendingDeletedAccountRecordTargetsAsync
// 的 PG 分支。
func (s *RecordCleanupStore) CleanupPendingAccountTargetsPostgres(ctx context.Context, limit int) (retention.PendingCleanupSummary, error) {
	targets, err := s.listPostgresAccountCleanupTargets(ctx, batchLimit(limit))
	if err != nil {
		return retention.PendingCleanupSummary{}, err
	}
	summary := retention.PendingCleanupSummary{}
	for _, target := range targets {
		summary.Attempted++
		result, err := s.CleanupAccountRelatedPostgres(ctx, target)
		if err != nil {
			summary.Failed++
			if markErr := s.markPostgresAccountCleanupTargetError(ctx, target, err.Error(), s.nowIso()); markErr != nil {
				return summary, markErr
			}
			continue
		}
		summary.DeletedRows += result.DeletedRows
		if result.HasMore || result.BlockedReason != "" {
			summary.Deferred++
		} else {
			summary.Completed++
		}
	}
	return summary, nil
}
