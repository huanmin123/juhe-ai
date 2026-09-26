package accounthealth

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// J1 直读输入基线播种（BUG-0194）。
//
// 直读候选 SQL（direct_input_reader.go:420 / direct_input_reader_sqlite.go:740）
// 对 account_health_jobs_input_versions 做 INNER JOIN；该表只在事件驱动路径上
// 被惰性创建（gateway 账户写入/授权扇出、jobs OAuth 刷新扇出），没有任何基线
// 播种方。全新部署或整库迁移后的账户从不经过这些事件路径，versions 表恒空，
// INNER JOIN 把候选筛空，J1 永远无探针可发，account_health_hourly 无源——
// AI 健康监控页恒为空。SQLite standalone 有 EnsureSQLiteDirectInputLayout 冷
// 启动兜底建表，但同样不播种行；PG 形态连建表兜底都没有。
//
// 本文件在 J1 启动路径上幂等播种基线行（current_version=1，与事件路径首次
// 预留的版本一致）， WHERE 静态条件镜像直读候选 SQL 的账户白名单（provider
// code + type + 未删除）；其余资格条件（状态/授权/到期/冷却）由读取器运行时
// 过滤，这里不重复——为暂不符合条件的账户预置版本行无害且免二次迁移。
// 白名单与 Node 账户白名单同源（account-health-jobs-input-authorization-
// fanout.repository.ts:59-60），与两方言读取器内联字面量保持一致，漂移由
// direct_input 契约测试与本次基线测试共同锚定。

// directInputBaselineProviderCodes mirrors direct_input_reader.go:451。
var directInputBaselineProviderCodes = [8]string{"gpt", "openai", "xai", "anthropic", "deepseek", "glm", "gemini", "hybrid"}

// directInputBaselineAccountTypes mirrors direct_input_reader.go:452。
var directInputBaselineAccountTypes = [3]string{"api_key", "oauth", "google_oauth"}

// EnsurePostgresDirectInputBaseline 幂等播种 PG 直读输入基线，返回新插入行数。
// 在 jobs 启动的 postgres 输入分支、直读 reader CheckContract 通过后调用；
// 失败按启动契约 fail-fast。
func EnsurePostgresDirectInputBaseline(ctx context.Context, db *sql.DB) (int64, error) {
	return seedDirectInputBaseline(ctx, db, "juhe_business.", "$1")
}

// seedDirectInputBaseline 是两方言共用的 INSERT...SELECT...ON CONFLICT DO
// NOTHING。qualifier 取 "juhe_business."（PG）或 ""（SQLite）；nowPlaceholder
// 取 "$1"（pgx）或 "?"（sqlite）。基线版本固定为 1：与事件路径首尝预留的
// current_version 相同，后续事件扇出在其上 +1，版本语义不受影响。
func seedDirectInputBaseline(ctx context.Context, db *sql.DB, qualifier, nowPlaceholder string) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("J1 直读基线播种缺少数据库句柄")
	}
	query := `INSERT INTO ` + qualifier + `account_health_jobs_input_versions (account_id, current_version, reserved_at)
SELECT a.id, 1, ` + nowPlaceholder + `
FROM ` + qualifier + `accounts a
WHERE a.deleted_at IS NULL
  AND a.provider_code IN ('` + joinBaselineList(directInputBaselineProviderCodes[:]) + `')
  AND a.type IN ('` + joinBaselineList(directInputBaselineAccountTypes[:]) + `')
ON CONFLICT (account_id) DO NOTHING`
	result, err := db.ExecContext(ctx, query, sqliteDirectTimestamp(time.Now()))
	if err != nil {
		return 0, fmt.Errorf("播种 J1 直读输入基线失败: %w", err)
	}
	seeded, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取 J1 直读输入基线播种行数失败: %w", err)
	}
	return seeded, nil
}

func joinBaselineList(values []string) string {
	joined := values[0]
	for _, value := range values[1:] {
		joined += "', '" + value
	}
	return joined
}
