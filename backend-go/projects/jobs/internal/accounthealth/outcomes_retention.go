package accounthealth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// outcomes 表保留清理（W40）：juhe_jobs.account_health_outcomes 是只增探针
// 遥测（读面最大 31 天窗，超窗行无任何代码路径消费；生产备份亦把该表列为
// 排除项），按 observed_at 滚动窗口批量删除最旧行收敛表体积。清理 SQL 归本
// 包实现（表归属 juhe_jobs schema）；保留窗口天数、批次与节奏由调用方承担
// （组合根 data-retention-cleanup 节拍，与 taskruns retention 同型）。

// CleanupOutcomesBefore 删除 observed_at 严格早于 cutoff 的 outcome 行，最旧
// 优先，单批最多 limit 行，返回删除行数。ctid 批删把单轮事务锁面压在
// limit 行内；子查询走 idx_account_health_outcomes_observed 索引（DESC 索引
// 反向扫描即 ASC 序）。
func CleanupOutcomesBefore(ctx context.Context, db *sql.DB, cutoff time.Time, limit int) (int64, error) {
	if db == nil {
		return 0, errors.New("db 不能为空")
	}
	if limit <= 0 {
		return 0, errors.New("limit 必须为正数")
	}
	tag, err := db.ExecContext(ctx, `
		DELETE FROM juhe_jobs.account_health_outcomes
		WHERE ctid IN (
			SELECT ctid FROM juhe_jobs.account_health_outcomes
			WHERE observed_at < $1
			ORDER BY observed_at ASC, ctid ASC
			LIMIT $2
		)`, cutoff.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("清理 account_health_outcomes 失败: %w", err)
	}
	deleted, err := tag.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取 account_health_outcomes 清理行数失败: %w", err)
	}
	return deleted, nil
}
