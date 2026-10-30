package cleanuprepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// api-key-record-cleanup.ts / account-record-cleanup.ts 的关联数据清理链移植：
// dataset targets 表、双聚合 global 游标放行门（usage_shard 逐分片游标已废，
// 与 dataretention 半区共用 sqliteAggregationFloorCursorAt）、分批删除、统计
// 扣减结算（CleanupDeletedApiKeyRecordStats / CleanupDeletedAccountRecordStats）
// 与派生窗口刷新挂钩。

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

// logger 返回清理链日志出口。RecordCleanupStore 当前无 Logger 注入字段
// （组合根未提供），按任务约定调用时取 slog.Default()、不缓存；后续需要
// 定向注入时再收敛到结构体字段。
func (s *RecordCleanupStore) logger() *slog.Logger {
	return slog.Default()
}

func cleanupPendingReason(hasMoreCoveredRows, hasUncoveredRows bool) string {
	if hasMoreCoveredRows {
		return "仍有已被统计安全游标覆盖的使用记录待后续批次清理，已保留待后台重试"
	}
	if hasUncoveredRows {
		return "仍有使用记录尚未被统计聚合安全游标覆盖，已保留待后台重试清理"
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

// ---- 统计安全游标 ----

func (s *RecordCleanupStore) scanUsageRows(ctx context.Context, db *sql.DB, query string, args ...any) ([]statsagg.UsageStatsRecordRow, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsageStatsRecordRows(rows)
}

func (s *RecordCleanupStore) toRelated(result retention.RelatedCleanupResult) retention.RelatedCleanupResult {
	return result
}

var accountScopeStatsTables = []string{
	"usage_stats_totals", "usage_stats_minute", "usage_stats_hourly", "usage_stats_daily",
	"usage_stats_weekly", "usage_stats_monthly", "usage_latency_minute", "usage_latency_hourly",
	"usage_latency_daily", "usage_latency_weekly", "usage_latency_monthly", "usage_rank_snapshots",
	"usage_quota_hourly_windows", "usage_scope_range_windows",
}

var accountAuthorizationReportTables = []string{
	"authorization_team_usage_summary_daily",
	"authorization_user_usage_summary_daily",
}

func escapeLikePrefix(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return replacer.Replace(value)
}

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
