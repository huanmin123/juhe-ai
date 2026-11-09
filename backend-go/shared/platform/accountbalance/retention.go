package accountbalance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// outcomes 表保留清理：juhe_jobs.account_balance_outcomes 是只增余额刷新遥测
// （每次余额刷新经 AppendOutcome 写一行，读面只按 request_id / outcome_id 消
// 费窗口内行，超窗行无任何代码路径消费），按 observed_at 滚动窗口批量删除最
// 旧行收敛表体积。清理 SQL 归本包实现（表归属 juhe_jobs schema，DDL 与幂等写
// 入见 store.go）；保留窗口天数、批次与节奏由调用方承担（jobs 组合根
// data-retention-cleanup 节拍，窗口 35 天，与 accounthealth outcomes
// retention 同型）。

// CleanupOutcomesBefore 删除 observed_at 严格早于 cutoff 的余额 outcome 行，
// 最旧优先，单批最多 limit 行，返回删除行数。ctid 批删把单轮事务锁面压在
// limit 行内；子查询按 observed_at ASC, ctid ASC 定序，等值 observed_at 时顺
// 序确定，重入安全。
func CleanupOutcomesBefore(ctx context.Context, db *sql.DB, cutoff time.Time, limit int) (int64, error) {
	if db == nil {
		return 0, errors.New("db 不能为空")
	}
	if limit <= 0 {
		return 0, errors.New("limit 必须为正数")
	}
	tag, err := db.ExecContext(ctx, `
		DELETE FROM juhe_jobs.account_balance_outcomes
		WHERE ctid IN (
			SELECT ctid FROM juhe_jobs.account_balance_outcomes
			WHERE observed_at < $1
			ORDER BY observed_at ASC, ctid ASC
			LIMIT $2
		)`, cutoff.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("清理 account_balance_outcomes 失败: %w", err)
	}
	deleted, err := tag.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取 account_balance_outcomes 清理行数失败: %w", err)
	}
	return deleted, nil
}
