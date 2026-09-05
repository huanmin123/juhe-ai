// Package cleanuprepo 移植 Node 保留清理家族的底层仓储（backend/src/storage
// 只读对照），为 jobs 组合根的四个 retention/cleanup 任务提供数据库实现：
//
//   - data-retention.repository.ts（public_api_logs / usage records /
//     stats+metrics 保留 / 非业务数据硬清理 / system_sessions）
//   - codex-context-state.repository.ts 的清理部分（过期状态批清理与
//     storage cleanup queue 结算）
//   - chat.repository.ts cleanupChatRetention（分区/轮次/资产/检查点链）
//   - account-delete-cleanup.repository.ts（逻辑删除账户物理清理）
//   - api-key-record-cleanup.ts / account-record-cleanup.ts（已删除
//     API Key / AI 账户关联数据清理与统计扣减）
//
// 双模方言差异照 Node 两侧实现：SQLite 走 node:sqlite 同构的 rowid/LIMIT
// 语句，PostgreSQL 走 schema 限定（juhe_business/juhe_dataset/juhe_stats/
// juhe_usage/juhe_chat/juhe_codex_context）与 ctid/LIMIT 语句。删除条件的
// 时间边界（含否）与 Node 逐字节一致。
package cleanuprepo

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// cryptoRead 读取随机字节（crypto/rand 的可注入封装）。
func cryptoRead(buffer []byte) (int, error) {
	return rand.Read(buffer)
}

// jsonMarshal 序列化（Node JSON.stringify 等价；map 键序不影响存储语义）。
func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}

// apiKeyScopeStatsTables 照 api-key-record-cleanup.ts apiKeyScopeStatsTables。
var apiKeyScopeStatsTables = []string{
	"usage_stats_totals",
	"usage_stats_minute",
	"usage_stats_hourly",
	"usage_stats_daily",
	"usage_stats_weekly",
	"usage_stats_monthly",
	"usage_latency_minute",
	"usage_latency_hourly",
	"usage_latency_daily",
	"usage_latency_weekly",
	"usage_latency_monthly",
	"usage_rank_snapshots",
	"usage_quota_hourly_windows",
	"usage_scope_range_windows",
}

// DB 是双模库句柄：sqlite 直开单库；postgres 经 pgpool 共享池，表名带
// schema 限定。
type DB struct {
	*sql.DB
	Postgres bool
}

// Table 限定表名（PG schema 限定，SQLite 裸表名）。
func (d *DB) Table(schema, name string) string {
	if d.Postgres {
		return schema + "." + name
	}
	return name
}

// Bind 把 `?` 占位符改写为 PG `$n`。
func (d *DB) Bind(query string) string {
	if !d.Postgres {
		return query
	}
	var out strings.Builder
	index := 0
	for _, ch := range query {
		if ch == '?' {
			index++
			out.WriteString(fmt.Sprintf("$%d", index))
			continue
		}
		out.WriteRune(ch)
	}
	return out.String()
}

// BindIn 生成 `?, ?, ...` 占位符串（经 Bind 改写后为 `$n` 序列）。
func (d *DB) BindIn(count int) string {
	return d.Bind(strings.TrimSuffix(strings.Repeat("?,", count), ","))
}

// chunkValues 照 Node chunkValues：按 900 一批切分 IN 列表。
func chunkValues(values []string, size int) [][]string {
	if size <= 0 {
		size = 900
	}
	chunks := make([][]string, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		end := start + size
		if end > len(values) {
			end = len(values)
		}
		chunks = append(chunks, values[start:end])
	}
	return chunks
}

// uniqueNonEmpty 照 Node uniqueNonEmpty：trim + 去空 + 去重（保序）。
func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]bool, len(values))
	output := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.TrimSpace(value)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		output = append(output, normalized)
	}
	return output
}

// positiveLimit 照 Node positiveLimit：非法输入回落 10000。
func positiveLimit(value int) int {
	if value > 0 {
		return value
	}
	return 10000
}

// batchLimit 照 Node Math.max(1, Math.trunc(limit))。
func batchLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	return limit
}

// changes 从 ExecContext 结果取受影响行数。
func changes(result sql.Result) (int64, error) {
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return affected, nil
}

// execChanged 执行一条语句并返回受影响行数。
func execChanged(ctx context.Context, db *DB, query string, args ...any) (int64, error) {
	result, err := db.ExecContext(ctx, db.Bind(query), args...)
	if err != nil {
		return 0, err
	}
	return changes(result)
}

// nowISO 返回 Node nowIso 等价的 UTC 毫秒 ISO 串。
func nowISO(now func() time.Time) string {
	if now == nil {
		return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	}
	return now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// parseInstant 解析 RFC3339（必须带 Z 或数值 offset）。
func parseInstant(value string) (time.Time, bool) {
	parsed, err := time.Parse("2006-01-02T15:04:05.999999999Z07:00", strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
