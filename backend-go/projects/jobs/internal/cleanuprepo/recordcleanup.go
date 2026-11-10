package cleanuprepo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// api-key-record-cleanup.ts / account-record-cleanup.ts 的关联数据清理链移植：
// dataset targets 表、双聚合 global 游标放行门（usage_shard 逐分片游标已废，
// 与 dataretention 半区共用 sqliteAggregationFloorCursorAt）、分批删除与统计
// 扣减结算（CleanupAPIKeyRelatedPostgres / CleanupAccountRelatedPostgres）。

const recordCleanupBatchLimit = 100

// RecordCleanupStore 承载已删除 API Key / AI 账户关联数据清理。
type RecordCleanupStore struct {
	Dataset *DB
	Stats   *DB
	// Business PG 模式下 juhe_business 句柄，供 PG 扣减链授权查找
	// （createPostgresUsageStatsAuthorizationLookup 的
	// resource_authorizations 查询）。
	Business *DB
	Now      func() time.Time
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
