package cleanuprepo

import (
	"context"
	"errors"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// 端口实现断言：组合根注入点与 retention 阶段签名保持同形。
var _ retention.AuditShellReaper = (*AuditShellStore)(nil)

// AuditShellStore 是 W13 兜底清扫的存储实现：删除陈旧 in_progress 元数据
// 空壳。空壳是「in_progress 信封写入成功、终态写入与撤回双双失败」的残留
// （2026-10-29 生产取证：与 PG 慢语句峰同窗，峰值 20 条/小时），无载荷、
// 无尝试记录，业务结果在 usage_records 完整可查。
type AuditShellStore struct {
	DB *DB
}

// ReapStaleInProgress 删除 created_at 早于 cutoff 的空壳行，最旧优先，最多
// limit 行，返回删除行数。两个状态位构成守卫：终态行与有载荷行永不命中。
// 存储形态已收敛为 PostgreSQL（SQLite 形态 2026-10-25 退役），非 PG 句柄
// 直接报错而不是静默跳过。
func (s *AuditShellStore) ReapStaleInProgress(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if s == nil || s.DB == nil {
		return 0, errors.New("audit shell reaper requires a cleanup database handle")
	}
	if !s.DB.Postgres {
		return 0, errors.New("audit shell reaper requires PostgreSQL")
	}
	return execChanged(ctx, s.DB, `
      DELETE FROM juhe_dataset.audit_logs
      WHERE ctid IN (
        SELECT ctid FROM juhe_dataset.audit_logs
        WHERE lifecycle_status='in_progress' AND capture_status='metadata_only'
          AND (created_at)::timestamptz < ?::timestamptz
        ORDER BY created_at ASC, ctid ASC
        LIMIT ?
      )
	`, cutoff.UTC().Format(time.RFC3339Nano), batchLimit(limit))
}
