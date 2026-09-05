package cleanuprepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// api-key-record-cleanup.ts / account-record-cleanup.ts 的关联数据清理链移植：
// dataset targets 表、分片游标覆盖检查、分批删除、统计扣减结算
// （CleanupDeletedApiKeyRecordStats / CleanupDeletedAccountRecordStats）与
// 派生窗口刷新挂钩。

const (
	recordCleanupBatchLimit = 100
	recordCleanupShardLimit = 16
)

// DerivedWindowRefresher 照 refreshUsageQuotaHourlyWindowsCache +
// refreshUsageRankSnapshots（SQLite 派生窗口重算；由组合根接入 statsagg）。
type DerivedWindowRefresher interface {
	RefreshQuotaHourlyWindows(ctx context.Context) error
	RefreshRankSnapshots(ctx context.Context) error
}

// RecordCleanupStore 承载已删除 API Key / AI 账户关联数据清理。
type RecordCleanupStore struct {
	Dataset      *DB
	Stats        *DB
	UsageCatalog *DB
	Shards       *ShardStore
	// Business PG 模式下 juhe_business 句柄，供 PG 扣减链授权查找
	// （createPostgresUsageStatsAuthorizationLookup 的
	// resource_authorizations 查询）；SQLite 分片路径不使用。
	Business *DB
	// DerivedWindows SQLite 派生窗口刷新；nil 时由组合根登记为跳过（Go 的
	// 调度式窗口刷新 jobs 已覆盖同一收敛语义）。
	DerivedWindows DerivedWindowRefresher
	// OnDerivedWindowsSkipped 在 DerivedWindows 为 nil 时上报（不静默）。
	OnDerivedWindowsSkipped func(reason string)
	Now                     func() time.Time
	// Timezone 提供业务统计时区（时间桶键计算）。
	Timezone func(ctx context.Context) (*time.Location, error)
	// CacheReadCostEstimator 注入 Node estimateProviderCacheReadCostUsd
	// （PG 扣减链 applyPostgresEstimatedCacheReadCost 的估算回填；
	// nil 时与 Node 估算返回 undefined 同语义：不回填）。
	CacheReadCostEstimator statsagg.CacheReadCostEstimator
}

func (s *RecordCleanupStore) nowIso() string {
	if s.Now != nil {
		return ISOOf(s.Now())
	}
	return ISOOf(time.Now())
}

func sqliteBusyBlockedReason(domain string) string {
	return "SQLite 正在执行其他写入，已保留已删除 " + domain + " 关联数据清理目标，等待后台重试"
}

func cleanupPendingReason(hasMoreCoveredRows, hasUncoveredRows bool) string {
	if hasMoreCoveredRows {
		return "仍有已被统计安全游标覆盖的使用记录待后续批次清理，已保留待后台重试"
	}
	if hasUncoveredRows {
		return "仍有使用记录尚未被对应分片统计安全游标覆盖，已保留待后台重试清理"
	}
	return "仍有使用记录尚未被统计安全游标覆盖，已保留待后台重试清理"
}

// ---- targets 表 ----

func (s *RecordCleanupStore) upsertAPIKeyTarget(ctx context.Context, apiKeyID, systemAccountID, updatedAt string) error {
	_, err := execChangedQ(ctx, s.Dataset, s.Dataset.Bind(fmt.Sprintf(`
    INSERT INTO api_key_record_cleanup_targets (api_key_id, system_account_id, created_at, updated_at)
    VALUES (?, ?, ?, ?)
    ON CONFLICT(api_key_id) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      updated_at = excluded.updated_at
	`)), apiKeyID, systemAccountID, updatedAt, updatedAt)
	return err
}

func (s *RecordCleanupStore) markAPIKeyTarget(ctx context.Context, apiKeyID, systemAccountID, blockedReason, errorMessage, updatedAt string) error {
	query := s.Dataset.Bind(fmt.Sprintf(`
    UPDATE api_key_record_cleanup_targets
    SET attempt_count = attempt_count + 1,
        last_attempt_at = ?,
        last_blocked_reason = %s,
        last_error_message = %s,
        updated_at = ?
    WHERE api_key_id = ? AND system_account_id = ?
	`, placeholderNull(blockedReason), placeholderNull(errorMessage)))
	args := []any{updatedAt}
	if blockedReason != "" {
		args = append(args, blockedReason)
	}
	if errorMessage != "" {
		args = append(args, errorMessage)
	}
	args = append(args, updatedAt, apiKeyID, systemAccountID)
	_, err := execChangedQ(ctx, s.Dataset, query, args...)
	return err
}

func placeholderNull(value string) string {
	if value == "" {
		return "NULL"
	}
	return "?"
}

func (s *RecordCleanupStore) clearAPIKeyTarget(ctx context.Context, apiKeyID, systemAccountID string) error {
	_, err := execChangedQ(ctx, s.Dataset,
		`DELETE FROM api_key_record_cleanup_targets WHERE api_key_id = ? AND system_account_id = ?`, apiKeyID, systemAccountID)
	return err
}

func (s *RecordCleanupStore) listAPIKeyTargets(ctx context.Context, limit int) ([]retention.APIKeyCleanupTarget, error) {
	rows, err := queryRows(ctx, s.Dataset, s.Dataset.Bind(fmt.Sprintf(`
    SELECT api_key_id, system_account_id
    FROM api_key_record_cleanup_targets
    ORDER BY COALESCE(last_attempt_at, created_at) ASC, created_at ASC, api_key_id ASC
    LIMIT ?
	`)), batchLimit(limit))
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

func (s *RecordCleanupStore) upsertAccountTarget(ctx context.Context, target retention.ExpiredDeletedAccountTarget, updatedAt string) error {
	_, err := execChangedQ(ctx, s.Dataset, s.Dataset.Bind(fmt.Sprintf(`
    INSERT INTO account_record_cleanup_targets (
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
	`)),
		target.AccountID, target.SystemAccountID,
		stringArrayJSON(target.RelatedAccountIDs), stringArrayJSON(target.AuthorizationIDs), stringArrayJSON(target.TeamScopeIDs),
		updatedAt, updatedAt)
	return err
}

func (s *RecordCleanupStore) markAccountTarget(ctx context.Context, target retention.ExpiredDeletedAccountTarget, blockedReason, errorMessage, updatedAt string) error {
	query := s.Dataset.Bind(fmt.Sprintf(`
    UPDATE account_record_cleanup_targets
    SET attempt_count = attempt_count + 1,
        last_attempt_at = ?,
        last_blocked_reason = %s,
        last_error_message = %s,
        updated_at = ?
    WHERE account_id = ? AND system_account_id = ?
	`, placeholderNull(blockedReason), placeholderNull(errorMessage)))
	args := []any{updatedAt}
	if blockedReason != "" {
		args = append(args, blockedReason)
	}
	if errorMessage != "" {
		args = append(args, errorMessage)
	}
	args = append(args, updatedAt, target.AccountID, target.SystemAccountID)
	_, err := execChangedQ(ctx, s.Dataset, query, args...)
	return err
}

func (s *RecordCleanupStore) clearAccountTarget(ctx context.Context, target retention.ExpiredDeletedAccountTarget) error {
	_, err := execChangedQ(ctx, s.Dataset,
		`DELETE FROM account_record_cleanup_targets WHERE account_id = ? AND system_account_id = ?`, target.AccountID, target.SystemAccountID)
	return err
}

func (s *RecordCleanupStore) listAccountTargets(ctx context.Context, limit int) ([]retention.ExpiredDeletedAccountTarget, error) {
	rows, err := queryRows(ctx, s.Dataset, s.Dataset.Bind(fmt.Sprintf(`
    SELECT account_id, system_account_id, related_account_ids_json, authorization_ids_json, team_scope_ids_json
    FROM account_record_cleanup_targets
    ORDER BY COALESCE(last_attempt_at, created_at) ASC, created_at ASC, account_id ASC
    LIMIT ?
	`)), batchLimit(limit))
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

func stringArrayJSON(values []string) string {
	normalized, _ := json.Marshal(uniqueNonEmpty(values))
	return string(normalized)
}

func parseStringArrayJSON(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var parsed []string
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return nil
	}
	return uniqueNonEmpty(parsed)
}

// ---- 分片游标 ----

func (s *RecordCleanupStore) shardCursor(ctx context.Context, shardKey string) (*cleanupCursor, error) {
	rows, err := queryRows(ctx, s.Stats, s.Stats.Bind(fmt.Sprintf(`
    SELECT job_name, cursor_created_at, cursor_id
    FROM stats_job_state
    WHERE scope_type = 'usage_shard'
      AND scope_id = ?
      AND job_name IN (%s)
      AND cursor_created_at IS NOT NULL
      AND cursor_id IS NOT NULL
    ORDER BY cursor_created_at ASC, cursor_id ASC
	`, s.Stats.BindIn(len(usageRecordCleanupRequiredCursorJobNames)))),
		append([]any{shardKey}, stringSliceToAny(usageRecordCleanupRequiredCursorJobNames)...)...)
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

// ---- 分片使用记录选择（SQLite）----

type shardUsageRow struct {
	statsagg.UsageStatsRecordRow
	Location       ShardLocation
	SourceShardKey string
}

func (s *RecordCleanupStore) selectAPIKeyUsageRows(ctx context.Context, apiKeyID, systemAccountID string, limit int) ([]shardUsageRow, bool, bool, error) {
	window, err := ListLocationsForApiKey(ctx, s.UsageCatalog, apiKeyID, systemAccountID, recordCleanupShardLimit)
	if err != nil {
		return nil, false, false, err
	}
	return s.selectUsageRows(ctx, window, limit, "api_key_id = ? AND system_account_id = ?", apiKeyID, systemAccountID)
}

func (s *RecordCleanupStore) selectAccountUsageRows(ctx context.Context, accountID string, limit int) ([]shardUsageRow, bool, bool, error) {
	window, err := ListLocationsForAccount(ctx, s.UsageCatalog, accountID, recordCleanupShardLimit)
	if err != nil {
		return nil, false, false, err
	}
	return s.selectUsageRows(ctx, window, limit, "account_id = ?", accountID)
}

func (s *RecordCleanupStore) selectUsageRows(ctx context.Context, window ShardLocationWindow, limit int, scopeColumn string, scopeArgs ...string) ([]shardUsageRow, bool, bool, error) {
	batchLimitValue := batchLimit(limit)
	queryLimitValue := batchLimitValue + 1
	var rows []shardUsageRow
	hasUncoveredRows := false
	for _, location := range window.Locations {
		shardDB, err := s.Shards.Open(location.FilePath)
		if err != nil {
			return nil, false, false, err
		}
		cursor, err := s.shardCursor(ctx, location.ShardKey)
		if err != nil {
			return nil, false, false, err
		}
		if cursor == nil {
			hasUncoveredRows = true
			continue
		}
		uncoveredQuery := fmt.Sprintf(`
      SELECT id FROM usage_records
      WHERE %s
        AND (created_at > ? OR (created_at = ? AND id > ?))
      LIMIT 1
		`, scopeColumn)
		uncovered, err := queryOne(ctx, shardDB, uncoveredQuery,
			append(stringSliceToAny(scopeArgs), cursor.CreatedAt, cursor.CreatedAt, cursor.ID)...)
		if err != nil {
			return nil, false, false, err
		}
		if uncovered != nil && textOf((*uncovered)["id"]) != "" {
			hasUncoveredRows = true
		}
		rowsQuery := fmt.Sprintf(`
      SELECT %s
      FROM usage_records
      WHERE %s
        AND (created_at < ? OR (created_at = ? AND id <= ?))
      ORDER BY created_at ASC, id ASC
      LIMIT ?
		`, usageStatsRecordSelectColumns, scopeColumn)
		records, err := s.scanUsageRows(ctx, shardDB, rowsQuery,
			append(stringSliceToAny(scopeArgs), cursor.CreatedAt, cursor.CreatedAt, cursor.ID, queryLimitValue)...)
		if err != nil {
			return nil, false, false, err
		}
		for _, record := range records {
			rows = append(rows, shardUsageRow{UsageStatsRecordRow: record, Location: location, SourceShardKey: location.ShardKey})
		}
	}
	sortShardUsageRows(rows)
	hasMoreCoveredRows := len(rows) > batchLimitValue || window.HasMore
	if len(rows) > queryLimitValue {
		rows = rows[:queryLimitValue]
	}
	return rows, hasMoreCoveredRows, hasUncoveredRows, nil
}

func sortShardUsageRows(rows []shardUsageRow) {
	// Node: sort by created_at localeCompare, then id localeCompare。
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			left, right := rows[j-1], rows[j]
			if left.CreatedAt < right.CreatedAt || (left.CreatedAt == right.CreatedAt && left.ID <= right.ID) {
				break
			}
			rows[j-1], rows[j] = rows[j], rows[j-1]
		}
	}
}

func (s *RecordCleanupStore) scanUsageRows(ctx context.Context, db *sql.DB, query string, args ...any) ([]statsagg.UsageStatsRecordRow, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsageStatsRecordRows(rows)
}

// hasUsageRecords SQLite：scope catalog 是否仍有分片。
func (s *RecordCleanupStore) hasAPIKeyUsageRecords(ctx context.Context, apiKeyID, systemAccountID string) (bool, error) {
	window, err := ListLocationsForApiKey(ctx, s.UsageCatalog, apiKeyID, systemAccountID, 1)
	if err != nil {
		return false, err
	}
	return len(window.Locations) > 0, nil
}

func (s *RecordCleanupStore) hasAccountUsageRecords(ctx context.Context, accountIDs []string) (bool, error) {
	for _, accountID := range uniqueNonEmpty(accountIDs) {
		window, err := ListLocationsForAccount(ctx, s.UsageCatalog, accountID, 1)
		if err != nil {
			return false, err
		}
		if len(window.Locations) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// deleteUsageRows 照 deleteApiKeyUsageRows / deleteAccountUsageRows（SQLite）。
func (s *RecordCleanupStore) deleteUsageRows(ctx context.Context, rows []shardUsageRow, scopeColumn string, scopeArgs ...string) (int64, error) {
	var deletedRows int64
	rowsByShard := map[string][]shardUsageRow{}
	var shardOrder []string
	for _, row := range rows {
		if _, ok := rowsByShard[row.Location.ShardKey]; !ok {
			shardOrder = append(shardOrder, row.Location.ShardKey)
		}
		rowsByShard[row.Location.ShardKey] = append(rowsByShard[row.Location.ShardKey], row)
	}
	var ids []string
	for _, shardKey := range shardOrder {
		shardRows := rowsByShard[shardKey]
		db, err := s.Shards.Open(shardRows[0].Location.FilePath)
		if err != nil {
			return deletedRows, err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return deletedRows, err
		}
		statement, err := tx.PrepareContext(ctx, fmt.Sprintf(
			`DELETE FROM usage_records WHERE id = ? AND %s`, scopeColumn))
		if err != nil {
			_ = tx.Rollback()
			return deletedRows, err
		}
		for _, row := range shardRows {
			result, err := statement.ExecContext(ctx, append([]any{row.ID}, stringSliceToAny(scopeArgs)...)...)
			if err != nil {
				statement.Close()
				_ = tx.Rollback()
				return deletedRows, err
			}
			affected, err := changes(result)
			if err != nil {
				statement.Close()
				_ = tx.Rollback()
				return deletedRows, err
			}
			deletedRows += affected
			ids = append(ids, row.ID)
		}
		statement.Close()
		if err := tx.Commit(); err != nil {
			return deletedRows, err
		}
	}
	if _, err := s.Shards.DeleteShardEntries(ctx, s.UsageCatalog, ids); err != nil {
		return deletedRows, err
	}
	return deletedRows, nil
}

// CleanupPendingAPIKeyTargets 照 cleanupPendingDeletedApiKeyRecordTargetsAsync
// （SQLite statsWriter 路径；PG 分支见 CleanupAPIKeyRelatedPostgres）。
func (s *RecordCleanupStore) CleanupPendingAPIKeyTargets(ctx context.Context, limit int, statsWriter retention.StatsWriter) (retention.PendingCleanupSummary, error) {
	targets, err := s.listAPIKeyTargets(ctx, batchLimit(limit))
	if err != nil {
		return retention.PendingCleanupSummary{}, err
	}
	return s.runAPIKeyTargets(ctx, targets, statsWriter)
}

func (s *RecordCleanupStore) runAPIKeyTargets(ctx context.Context, targets []retention.APIKeyCleanupTarget, statsWriter retention.StatsWriter) (retention.PendingCleanupSummary, error) {
	summary := retention.PendingCleanupSummary{}
	for _, target := range targets {
		summary.Attempted++
		result, err := s.cleanupAPIKeyRelated(ctx, target.APIKeyID, target.SystemAccountID, statsWriter)
		if err != nil {
			summary.Failed++
			if markErr := s.markAPIKeyTarget(ctx, target.APIKeyID, target.SystemAccountID, "", err.Error(), s.nowIso()); markErr != nil {
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

// CleanupPendingAccountTargets 照 cleanupPendingDeletedAccountRecordTargetsAsync。
func (s *RecordCleanupStore) CleanupPendingAccountTargets(ctx context.Context, limit int, statsWriter retention.StatsWriter) (retention.PendingCleanupSummary, error) {
	targets, err := s.listAccountTargets(ctx, batchLimit(limit))
	if err != nil {
		return retention.PendingCleanupSummary{}, err
	}
	summary := retention.PendingCleanupSummary{}
	for _, target := range targets {
		summary.Attempted++
		result, err := s.cleanupAccountRelated(ctx, target, statsWriter)
		if err != nil {
			summary.Failed++
			if markErr := s.markAccountTarget(ctx, target, "", err.Error(), s.nowIso()); markErr != nil {
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

// RelatedCleanupResult 保持 retention.RelatedCleanupResult。
func (s *RecordCleanupStore) toRelated(result retention.RelatedCleanupResult) retention.RelatedCleanupResult {
	return result
}

// cleanupAPIKeyRelated 照 cleanupDeletedApiKeyRelatedRecordDataCoreAsync（SQLite
// statsWriter 路径）。
func (s *RecordCleanupStore) cleanupAPIKeyRelated(ctx context.Context, apiKeyID, systemAccountID string, statsWriter retention.StatsWriter) (retention.RelatedCleanupResult, error) {
	updatedAt := s.nowIso()
	if err := s.upsertAPIKeyTarget(ctx, apiKeyID, systemAccountID, updatedAt); err != nil {
		return retention.RelatedCleanupResult{}, err
	}
	usageRows, hasMoreCoveredRows, hasUncoveredRows, blockedReason, err := s.selectAPIKeyUsageRowsGuarded(ctx, apiKeyID, systemAccountID, recordCleanupBatchLimit)
	if err != nil {
		_ = s.markAPIKeyTarget(ctx, apiKeyID, systemAccountID, "", err.Error(), s.nowIso())
		return retention.RelatedCleanupResult{}, err
	}
	rowsToDelete := usageRows
	if len(rowsToDelete) > recordCleanupBatchLimit {
		rowsToDelete = rowsToDelete[:recordCleanupBatchLimit]
	}
	var deletedUsageRows int64
	if blockedReason == "" && len(rowsToDelete) > 0 {
		if statsWriter == nil {
			err = fmt.Errorf("retention stats writer 未初始化")
		} else {
			err = statsWriter.CleanupDeletedApiKeyRecordStats(ctx, retention.DeletedApiKeyRecordStatsCleanupInput{
				Target:    retention.APIKeyCleanupTarget{APIKeyID: apiKeyID, SystemAccountID: systemAccountID},
				Rows:      shardUsageRowsToMaps(rowsToDelete),
				UpdatedAt: updatedAt,
			})
		}
		if err != nil {
			_ = s.markAPIKeyTarget(ctx, apiKeyID, systemAccountID, "", err.Error(), s.nowIso())
			return retention.RelatedCleanupResult{}, err
		}
	}
	if blockedReason == "" {
		deletedUsageRows, err = s.deleteUsageRows(ctx, rowsToDelete, "api_key_id = ? AND system_account_id = ?", apiKeyID, systemAccountID)
		if err != nil {
			_ = s.markAPIKeyTarget(ctx, apiKeyID, systemAccountID, "", err.Error(), s.nowIso())
			return retention.RelatedCleanupResult{}, err
		}
	}
	if blockedReason == "" && len(rowsToDelete) > 0 && statsWriter != nil {
		if err := statsWriter.CleanupDeletedApiKeyRecordStats(ctx, retention.DeletedApiKeyRecordStatsCleanupInput{
			Target:       retention.APIKeyCleanupTarget{APIKeyID: apiKeyID, SystemAccountID: systemAccountID},
			Rows:         shardUsageRowsToMaps(rowsToDelete),
			UpdatedAt:    updatedAt,
			ShardDeleted: true,
		}); err != nil {
			_ = s.markAPIKeyTarget(ctx, apiKeyID, systemAccountID, "", err.Error(), s.nowIso())
			return retention.RelatedCleanupResult{}, err
		}
	}
	hasUsageMore := true
	if blockedReason == "" {
		hasUsageMore, err = s.hasAPIKeyUsageRecords(ctx, apiKeyID, systemAccountID)
		if err != nil {
			_ = s.markAPIKeyTarget(ctx, apiKeyID, systemAccountID, "", err.Error(), s.nowIso())
			return retention.RelatedCleanupResult{}, err
		}
	}
	hasMore := hasUsageMore || blockedReason != ""
	if blockedReason == "" && !hasMore {
		if statsWriter == nil {
			err = fmt.Errorf("retention stats writer 未初始化")
		} else {
			err = statsWriter.CleanupDeletedApiKeyRecordStats(ctx, retention.DeletedApiKeyRecordStatsCleanupInput{
				Target:       retention.APIKeyCleanupTarget{APIKeyID: apiKeyID, SystemAccountID: systemAccountID},
				Rows:         nil,
				UpdatedAt:    updatedAt,
				ShardDeleted: true,
			})
		}
		if err != nil {
			_ = s.markAPIKeyTarget(ctx, apiKeyID, systemAccountID, "", err.Error(), s.nowIso())
			return retention.RelatedCleanupResult{}, err
		}
		hasMore = false
	}
	result := retention.RelatedCleanupResult{DeletedRows: deletedUsageRows, HasMore: hasMore}
	if blockedReason != "" {
		result.BlockedReason = blockedReason
	} else if hasMore {
		result.BlockedReason = cleanupPendingReason(hasMoreCoveredRows, hasUncoveredRows)
	}
	if result.HasMore || result.BlockedReason != "" {
		reason := result.BlockedReason
		if reason == "" {
			reason = "等待统计安全游标追平"
		}
		if err := s.markAPIKeyTarget(ctx, apiKeyID, systemAccountID, reason, "", updatedAt); err != nil {
			return result, err
		}
	} else if err := s.clearAPIKeyTarget(ctx, apiKeyID, systemAccountID); err != nil {
		return result, err
	}
	return s.toRelated(result), nil
}

// selectAPIKeyUsageRowsGuarded 包装分片选择：分片句柄错误保持透传。
func (s *RecordCleanupStore) selectAPIKeyUsageRowsGuarded(ctx context.Context, apiKeyID, systemAccountID string, limit int) ([]shardUsageRow, bool, bool, string, error) {
	rows, hasMore, hasUncovered, err := s.selectAPIKeyUsageRows(ctx, apiKeyID, systemAccountID, limit)
	if err != nil {
		return nil, false, false, "", err
	}
	return rows, hasMore, hasUncovered, "", nil
}

// cleanupAccountRelated 照 cleanupDeletedAccountRelatedRecordDataCoreAsync（SQLite）。
func (s *RecordCleanupStore) cleanupAccountRelated(ctx context.Context, target retention.ExpiredDeletedAccountTarget, statsWriter retention.StatsWriter) (retention.RelatedCleanupResult, error) {
	updatedAt := s.nowIso()
	if err := s.upsertAccountTarget(ctx, target, updatedAt); err != nil {
		return retention.RelatedCleanupResult{}, err
	}
	accountIDs := uniqueNonEmpty(append([]string{target.AccountID}, target.RelatedAccountIDs...))
	var allRows []shardUsageRow
	hasMoreCoveredRows := false
	hasUncoveredRows := false
	for _, accountID := range accountIDs {
		rows, hasMore, hasUncovered, err := s.selectAccountUsageRows(ctx, accountID, recordCleanupBatchLimit)
		if err != nil {
			_ = s.markAccountTarget(ctx, target, "", err.Error(), s.nowIso())
			return retention.RelatedCleanupResult{}, err
		}
		hasMoreCoveredRows = hasMoreCoveredRows || hasMore
		hasUncoveredRows = hasUncoveredRows || hasUncovered
		allRows = append(allRows, rows...)
	}
	sortShardUsageRows(allRows)
	rowsToDelete := allRows
	if len(rowsToDelete) > recordCleanupBatchLimit {
		rowsToDelete = rowsToDelete[:recordCleanupBatchLimit]
	}
	var deletedUsageRows int64
	if len(rowsToDelete) > 0 && statsWriter != nil {
		if err := statsWriter.CleanupDeletedAccountRecordStats(ctx, retention.DeletedAccountRecordStatsCleanupInput{
			Target:    target,
			Rows:      shardUsageRowsToMaps(rowsToDelete),
			UpdatedAt: updatedAt,
		}); err != nil {
			_ = s.markAccountTarget(ctx, target, "", err.Error(), s.nowIso())
			return retention.RelatedCleanupResult{}, err
		}
	}
	for _, row := range rowsToDelete {
		deleted, err := s.deleteUsageRows(ctx, []shardUsageRow{row}, "account_id = ?", textOfAccountID(row))
		if err != nil {
			_ = s.markAccountTarget(ctx, target, "", err.Error(), s.nowIso())
			return retention.RelatedCleanupResult{}, err
		}
		deletedUsageRows += deleted
	}
	if len(rowsToDelete) > 0 && statsWriter != nil {
		if err := statsWriter.CleanupDeletedAccountRecordStats(ctx, retention.DeletedAccountRecordStatsCleanupInput{
			Target:       target,
			Rows:         shardUsageRowsToMaps(rowsToDelete),
			UpdatedAt:    updatedAt,
			ShardDeleted: true,
		}); err != nil {
			_ = s.markAccountTarget(ctx, target, "", err.Error(), s.nowIso())
			return retention.RelatedCleanupResult{}, err
		}
	}
	hasUsageMore, err := s.hasAccountUsageRecords(ctx, accountIDs)
	if err != nil {
		_ = s.markAccountTarget(ctx, target, "", err.Error(), s.nowIso())
		return retention.RelatedCleanupResult{}, err
	}
	hasMore := hasUsageMore
	if !hasMore {
		if statsWriter == nil {
			err = fmt.Errorf("retention stats writer 未初始化")
		} else {
			err = statsWriter.CleanupDeletedAccountRecordStats(ctx, retention.DeletedAccountRecordStatsCleanupInput{
				Target:       target,
				Rows:         nil,
				UpdatedAt:    updatedAt,
				ShardDeleted: true,
			})
		}
		if err != nil {
			_ = s.markAccountTarget(ctx, target, "", err.Error(), s.nowIso())
			return retention.RelatedCleanupResult{}, err
		}
	}
	result := retention.RelatedCleanupResult{DeletedRows: deletedUsageRows, HasMore: hasMore}
	if hasMore {
		result.BlockedReason = cleanupPendingReason(hasMoreCoveredRows, hasUncoveredRows)
	}
	if result.HasMore || result.BlockedReason != "" {
		reason := result.BlockedReason
		if reason == "" {
			reason = "等待统计安全游标追平"
		}
		if err := s.markAccountTarget(ctx, target, reason, "", updatedAt); err != nil {
			return result, err
		}
	} else if err := s.clearAccountTarget(ctx, target); err != nil {
		return result, err
	}
	return result, nil
}

func textOfAccountID(row shardUsageRow) string {
	if row.AccountID != nil {
		return *row.AccountID
	}
	return ""
}

// ---- 相关记录存在性检查（SQLite / PG）----

func (s *RecordCleanupStore) hasRelatedRecordDataSQLite(ctx context.Context, target *cleanupTarget) (bool, error) {
	accountIDs := uniqueNonEmpty(append([]string{target.AccountID}, target.RelatedAccountIDs...))
	// target 表自检
	if len(target.AccountID) > 0 {
		rows, err := queryRows(ctx, s.Dataset, `SELECT account_id FROM account_record_cleanup_targets WHERE account_id = ? AND system_account_id = ? LIMIT 1`, target.AccountID, target.SystemAccountID)
		if err != nil {
			return false, err
		}
		if len(rows) > 0 {
			return true, nil
		}
	}
	exists, err := s.hasAccountUsageRecords(ctx, accountIDs)
	if err != nil || exists {
		return exists, err
	}
	return s.hasDeletedAccountStatsRowsSQLite(ctx, target, accountIDs)
}

var accountScopeStatsTables = []string{
	"usage_stats_totals", "usage_stats_minute", "usage_stats_hourly", "usage_stats_daily",
	"usage_stats_weekly", "usage_stats_monthly", "usage_latency_minute", "usage_latency_hourly",
	"usage_latency_daily", "usage_latency_weekly", "usage_latency_monthly", "usage_rank_snapshots",
	"usage_quota_hourly_windows", "usage_scope_range_windows",
}

var accountAuthorizationReportTables = []string{
	"authorization_team_usage_summary_daily", "authorization_team_usage_range_windows",
	"authorization_user_usage_summary_daily", "authorization_user_usage_range_windows",
}

func escapeLikePrefix(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return replacer.Replace(value)
}

func (s *RecordCleanupStore) hasDeletedAccountStatsRowsSQLite(ctx context.Context, target *cleanupTarget, accountIDs []string) (bool, error) {
	authorizationIDs := uniqueNonEmpty(target.AuthorizationIDs)
	teamScopeIDs := uniqueNonEmpty(target.TeamScopeIDs)
	rowExists := func(table, condition string, args ...any) (bool, error) {
		rows, err := queryRows(ctx, s.Stats, fmt.Sprintf(`SELECT 1 AS found FROM %s WHERE %s LIMIT 1`, table, condition), args...)
		if err != nil {
			return false, err
		}
		return len(rows) > 0, nil
	}
	for _, accountID := range accountIDs {
		for _, tableName := range accountScopeStatsTables {
			if ok, err := rowExists(tableName, "scope_type IN ('account', 'caller_account') AND scope_id = ?", accountID); err != nil || ok {
				return ok, err
			}
			if ok, err := rowExists(tableName, "scope_type = 'account_authorization_team' AND scope_id LIKE ? ESCAPE '\\'", escapeLikePrefix(accountID)+":%"); err != nil || ok {
				return ok, err
			}
		}
		for _, condition := range [][2]string{
			{"stats_job_state", "scope_type IN ('account', 'caller_account') AND scope_id = ?"},
			{"stats_job_state", "scope_type = 'account_authorization_team' AND scope_id LIKE ? ESCAPE '\\'"},
			{"account_quality_scores", "account_id = ?"},
			{"account_quality_dirty_accounts", "account_id = ?"},
			{"account_quality_minute_stats", "account_id = ?"},
			{"account_health_hourly", "account_id = ?"},
			{"account_usage_snapshots", "account_id = ?"},
			{"usage_record_cleanup_deductions", "account_id = ?"},
		} {
			if ok, err := rowExists(condition[0], condition[1], accountID); err != nil || ok {
				return ok, err
			}
		}
		for _, tableName := range accountAuthorizationReportTables {
			if ok, err := rowExists(tableName, "resource_filter_type = 'account' AND resource_filter_id = ?", accountID); err != nil || ok {
				return ok, err
			}
		}
	}
	for _, chunk := range chunkValues(authorizationIDs, 400) {
		if len(chunk) == 0 {
			continue
		}
		condition := fmt.Sprintf("scope_type = 'account_authorization' AND scope_id IN (%s)", placeholderList(len(chunk)))
		for _, tableName := range accountScopeStatsTables {
			if ok, err := rowExists(tableName, condition, stringSliceToAny(chunk)...); err != nil || ok {
				return ok, err
			}
		}
		if ok, err := rowExists("stats_job_state", condition, stringSliceToAny(chunk)...); err != nil || ok {
			return ok, err
		}
	}
	for _, chunk := range chunkValues(teamScopeIDs, 400) {
		if len(chunk) == 0 {
			continue
		}
		condition := fmt.Sprintf("scope_type = 'account_authorization_team' AND scope_id IN (%s)", placeholderList(len(chunk)))
		for _, tableName := range accountScopeStatsTables {
			if ok, err := rowExists(tableName, condition, stringSliceToAny(chunk)...); err != nil || ok {
				return ok, err
			}
		}
		if ok, err := rowExists("stats_job_state", condition, stringSliceToAny(chunk)...); err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

// hasRelatedRecordDataPostgres 照 hasDeletedAccountRelatedRecordDataAsync。
func (s *DeletedAccountStore) hasRelatedRecordDataPostgres(ctx context.Context, target *cleanupTarget) (bool, error) {
	accountIDs := uniqueNonEmpty(target.AccountIDs)
	authorizationIDs := uniqueNonEmpty(target.AuthorizationIDs)
	teamScopeIDs := uniqueNonEmpty(target.TeamScopeIDs)
	exists := func(query string, args ...any) (bool, error) {
		rows, err := queryRows(ctx, s.Dataset, query, args...)
		if err != nil {
			if s.Dataset != nil && strings.Contains(err.Error(), "relation") {
				return false, err
			}
			return false, err
		}
		return len(rows) > 0, nil
	}
	if ok, err := exists(`SELECT 1 FROM juhe_dataset.account_record_cleanup_targets WHERE account_id = ANY($1::text[]) LIMIT 1`, accountIDs); err != nil || ok {
		return ok, err
	}
	if ok, err := exists(`SELECT 1 FROM juhe_usage.usage_records WHERE account_id = ANY($1::text[]) LIMIT 1`, accountIDs); err != nil || ok {
		return ok, err
	}
	if len(authorizationIDs) > 0 {
		if ok, err := exists(`SELECT 1 FROM juhe_usage.usage_records WHERE account_authorization_id = ANY($1::text[]) LIMIT 1`, authorizationIDs); err != nil || ok {
			return ok, err
		}
	}
	if len(teamScopeIDs) > 0 {
		if ok, err := exists(`SELECT 1 FROM juhe_usage.usage_records WHERE group_authorization_id = ANY($1::text[]) LIMIT 1`, teamScopeIDs); err != nil || ok {
			return ok, err
		}
	}
	if ok, err := exists(`SELECT 1 FROM juhe_dataset.audit_logs WHERE account_id = ANY($1::text[]) LIMIT 1`, accountIDs); err != nil || ok {
		return ok, err
	}
	if ok, err := exists(`SELECT 1 FROM juhe_stats.account_quality_scores WHERE account_id = ANY($1::text[]) LIMIT 1`, accountIDs); err != nil || ok {
		return ok, err
	}
	if ok, err := exists(`SELECT 1 FROM juhe_stats.account_usage_snapshots WHERE account_id = ANY($1::text[]) LIMIT 1`, accountIDs); err != nil || ok {
		return ok, err
	}
	scopeIDsByType := []struct {
		scopeType string
		ids       []string
	}{
		{"account", accountIDs},
		{"caller_account", accountIDs},
		{"account_authorization", authorizationIDs},
		{"account_authorization_team", teamScopeIDs},
	}
	statsTables := []string{
		"usage_stats_totals", "usage_stats_minute", "usage_stats_hourly", "usage_stats_daily",
		"usage_stats_weekly", "usage_stats_monthly", "usage_latency_minute", "usage_latency_hourly",
		"usage_latency_daily", "usage_latency_weekly", "usage_latency_monthly",
		"usage_quota_hourly_windows", "usage_scope_range_windows",
	}
	for _, scope := range scopeIDsByType {
		scopeIDs := uniqueNonEmpty(scope.ids)
		if len(scopeIDs) == 0 {
			continue
		}
		for _, tableName := range statsTables {
			ok, err := exists(fmt.Sprintf(
				`SELECT 1 FROM juhe_stats.%s WHERE system_account_id = $1 AND scope_type = $2 AND scope_id = ANY($3::text[]) LIMIT 1`, tableName),
				target.SystemAccountID, scope.scopeType, scopeIDs)
			if err != nil || ok {
				return ok, err
			}
		}
		ok, err := exists(fmt.Sprintf(
			`SELECT 1 FROM juhe_stats.usage_rank_snapshots WHERE system_account_id = $1 AND scope_type = $2 AND scope_id = ANY($3::text[]) LIMIT 1`),
			target.SystemAccountID, scope.scopeType, scopeIDs)
		if err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

// CleanupAPIKeyRelatedSQLite 是 retention.RecordMaintenanceExecutor 的
// SQLite 关联清理入口（组合根 runner 调用）。
func (s *RecordCleanupStore) CleanupAPIKeyRelatedSQLite(ctx context.Context, apiKeyID, systemAccountID string, statsWriter retention.StatsWriter) (retention.RelatedCleanupResult, error) {
	result, err := s.cleanupAPIKeyRelated(ctx, apiKeyID, systemAccountID, statsWriter)
	if err != nil {
		return retention.RelatedCleanupResult{}, err
	}
	return result, nil
}

// CleanupAccountRelatedSQLite 是 account 变体入口。
func (s *RecordCleanupStore) CleanupAccountRelatedSQLite(ctx context.Context, target retention.ExpiredDeletedAccountTarget, statsWriter retention.StatsWriter) (retention.RelatedCleanupResult, error) {
	result, err := s.cleanupAccountRelated(ctx, target, statsWriter)
	if err != nil {
		return retention.RelatedCleanupResult{}, err
	}
	return result, nil
}
