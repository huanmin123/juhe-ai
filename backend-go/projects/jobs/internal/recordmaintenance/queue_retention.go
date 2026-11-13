package recordmaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// record_maintenance_jobs 交接表的保留清理（T207 C1）。
//
// 此前该表全仓无删除路径：消费侧「失败保留等待重试」叠加毒丸行后，任一
// 确定性失败行及其后全部行永久滞留（T207 C1 队头阻塞的另一半）。毒丸隔离
// （status='failed'）解决阻塞，但行仍在表内；本清理按行龄滚动删除
//（含 pending / failed），保证生产者停止、队列排空后表最终归零。挂接点选
// 组合根 data-retention-cleanup 节拍（worker_retention.go），与
// background_task_runs / account_health_outcomes 等同拍滚动清理同型。
//
// created_at 是规范化 UTC ISO 文本（字典序即时间序），cutoff 用同格式文本
// 直接比较。批删走 ctid 子查询（CleanupOutcomesBefore 同型）：单语句锁面压
// 在 limit 行内，子查询命中 created_at 序的前端行。

// CleanupJobsBefore 删除 created_at 严格早于 cutoff 的行（含 pending/failed），
// 最旧优先，单批最多 limit 行，返回删除行数。
func CleanupJobsBefore(ctx context.Context, db *sql.DB, cutoff string, limit int) (int64, error) {
	if db == nil {
		return 0, errors.New("db 不能为空")
	}
	if limit <= 0 {
		return 0, errors.New("limit 必须为正数")
	}
	tag, err := db.ExecContext(ctx, `DELETE FROM `+PGSchema+"."+TableName+`
WHERE ctid IN (
	SELECT ctid FROM `+PGSchema+"."+TableName+`
	WHERE created_at < $1
	ORDER BY created_at ASC, ctid ASC
	LIMIT $2
)`, cutoff, limit)
	if err != nil {
		return 0, fmt.Errorf("清理 record_maintenance_jobs 失败: %w", err)
	}
	deleted, err := tag.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取 record_maintenance_jobs 清理行数失败: %w", err)
	}
	return deleted, nil
}
