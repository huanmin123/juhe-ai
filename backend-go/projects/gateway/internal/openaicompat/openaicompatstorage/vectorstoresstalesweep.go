package openaicompatstorage

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// staleVectorStoreIndexingSweepThreshold 是遗留 in_progress 索引绑定的收尾
// 判定阈值（DD6-S8）：IndexAsync 生产为 nil 时索引在进程内 goroutine 执行，
// 进程重启后闭包永久丢失，绑定停留 in_progress 且无任何进程内写点能收尾。
// 阈值依据：索引任务是本地文件读取 + 切块（BuildVectorStoreChunks，无外部
// IO），正常完成在秒级；updated_at 即"本代绑定的最后写点"（添加/重加 upsert
// 落定，任务随后启动），10 分钟约为正常预算的数量级余量。误收的代价是合法
// 长任务的完成回调因 updated_at 已被清扫推进而被代次守卫（DD6-S7）拒绝，
// 绑定终态为 failed——不会静默污染数据，重新添加文件即可重建绑定。
const staleVectorStoreIndexingSweepThreshold = 10 * time.Minute

// sweepStaleVectorStoreFileIndexing 是启动期的一次性幂等收尾（DD6-S8）：把
// 超过 staleVectorStoreIndexingSweepThreshold 仍未收尾的 in_progress 绑定
// 标记为 failed 并推进 updated_at。条件含 deleted_at IS NULL——已删行保持
// 删除形态（status 停留 in_progress），与 DD2-C7 守卫契约互不波及；刚写入
// 的正常绑定 updated_at 新鲜，不受影响，重复执行自然空转。时间比较两侧显式
// ::timestamptz cast（列是 text ISO 文本，避免字典序在零分数秒边界误序，
// 同 modelcheckowner stale_runs 口径）。返回收尾行数。
func (s *Store) sweepStaleVectorStoreFileIndexing(ctx context.Context) (int64, error) {
	ctx = ensureCtx(ctx)
	moment := s.now()
	stamped := isoMillis(moment)
	cutoff := isoMillis(moment.Add(-staleVectorStoreIndexingSweepThreshold))
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("openai_compatible_vector_store_files")+`
		SET status = ?, updated_at = ?
		WHERE status = ?
			AND deleted_at IS NULL
			AND updated_at::timestamptz < ?::timestamptz`),
		VectorStoreFileStatusFailed, stamped, VectorStoreFileStatusInProgress, cutoff)
	if err != nil {
		return 0, fmt.Errorf("sweep stale openai compatible vector store file indexing bindings: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read stale openai compatible vector store file indexing sweep result: %w", err)
	}
	if changed > 0 {
		slog.Info("openai compatible vector store 遗留 in_progress 索引绑定已收尾为 failed",
			"count", changed, "cutoff", cutoff)
	}
	return changed, nil
}
