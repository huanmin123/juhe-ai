package groups

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// StatsReader is the interface for reading group_account_stats from the stats database.
// Implementations must be safe for concurrent use.
type StatsReader interface {
	ReadGroupAccountStats(ctx context.Context, groupIDs []string) (map[string]AccountStats, error)
}

// GroupAccountStatsDBReader reads group_account_stats from juhe_stats database.
type GroupAccountStatsDBReader struct {
	db *sql.DB
	// pg 决定占位符方言（PG 句柄为 pgx stdlib，? 须重写为 $N，与 ipstats /
	// groups Store.bind 同一惯例）。
	pg bool
}

// NewGroupAccountStatsDBReader creates a stats reader from the stats database.
func NewGroupAccountStatsDBReader(db *sql.DB, postgres bool) *GroupAccountStatsDBReader {
	return &GroupAccountStatsDBReader{db: db, pg: postgres}
}

// table qualifies stats-schema tables for PostgreSQL（生产 PG 无 search_path，
// 裸表名 42P01 → hydrate 整体丢弃 → 列表账户数/并发/状态三列恒 0；与
// apikeys StatsUsageSource.table / gatewayclientip SQLPolicySource.table 同一
// 方言惯例：SQLite 保持裸表名）。
func (r *GroupAccountStatsDBReader) table(name string) string {
	if r.pg {
		return "juhe_stats." + name
	}
	return name
}

// bind rewrites ? placeholders to $N for PostgreSQL.
func (r *GroupAccountStatsDBReader) bind(query string) string {
	if !r.pg {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
			continue
		}
		out.WriteByte(query[i])
	}
	return out.String()
}

// ReadGroupAccountStats reads group_account_stats for the given group IDs from juhe_stats.
// Returns a map keyed by group_id. Missing groups are not included in the result.
//
// 列集与过滤语义对齐 Node group-read-loaders.ts 的 groupAccountStatsSelectColumns
// （10 个真实列，无时效过滤）：group_account_stats 只有这 11 列（含
// system_account_id），此前 SELECT 的 today_usage/usage 两列不存在（恒
// "no such column" → hydrate 整体丢弃），`updated_at > 读时刻` 也恒假，两者
// 均为移植错误。TodayUsage/Usage 在 Node 由 usage-summary hydrate 单独供给
// （group-summary.repository.ts 的 loadGroupUsageSummariesForScopes 后
// merge），Go 侧由 WithUsageSource 端口 + hydrateListUsage /
// hydrateDetailUsage 供给，行存在时先铺 emptyAccountUsageSummary 空形状
// （见 accountStatsFromGroupRow）。
func (r *GroupAccountStatsDBReader) ReadGroupAccountStats(ctx context.Context, groupIDs []string) (map[string]AccountStats, error) {
	if len(groupIDs) == 0 || r.db == nil {
		return map[string]AccountStats{}, nil
	}

	result := make(map[string]AccountStats, len(groupIDs))
	placeholders := make([]string, len(groupIDs))
	args := make([]any, 0, len(groupIDs))
	for i, id := range groupIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}

	query := "SELECT group_id, total, available, active, disabled, error, rate_limited, " +
		"current_concurrency, concurrency_limit " +
		"FROM " + r.table("group_account_stats") + " WHERE group_id IN (" + strings.Join(placeholders, ",") + ")"

	rows, err := r.db.QueryContext(ctx, r.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var row groupAccountStatsRow
		if err := rows.Scan(
			&row.GroupID,
			&row.Total,
			&row.Available,
			&row.Active,
			&row.Disabled,
			&row.Error,
			&row.RateLimited,
			&row.CurrentConcurrency,
			&row.ConcurrencyLimit,
		); err != nil {
			return nil, err
		}
		result[row.GroupID] = accountStatsFromGroupRow(row)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// groupAccountStatsRow mirrors the juhe_stats.group_account_stats projection.
type groupAccountStatsRow struct {
	GroupID            string
	Total              int
	Available          int
	Active             int
	Disabled           int
	Error              int
	RateLimited        int
	CurrentConcurrency int
	ConcurrencyLimit   int
}

// accountStatsFromGroupRow mirrors the Node GroupAccountStats mapper:
// reads the stats row and converts it to the AccountStats projection.
// TodayUsage/Usage 先铺 emptyAccountUsageSummary 的 13 键空形状，再由
// hydrateListUsage / hydrateDetailUsage 覆盖真值（恢复 Node 响应形状：
// group_account_stats 行存在时 two key 也必须非 nil；修复前这里整体替换
// emptyAccountStats，行存在时连零值 key 都缺失，是前端"用量(日)"列恒 0
// 的根因之一）。
func accountStatsFromGroupRow(row groupAccountStatsRow) AccountStats {
	stats := emptyAccountStats()
	stats.Total = row.Total
	stats.Available = row.Available
	stats.Active = row.Active
	stats.Disabled = row.Disabled
	stats.Error = row.Error
	stats.RateLimited = row.RateLimited
	stats.CurrentConcurrency = row.CurrentConcurrency
	stats.ConcurrencyLimit = row.ConcurrencyLimit
	return stats
}
