package authz

import (
	"context"
	"time"
)

// T6d 登记缺口的 gateway 侧消费面：jobs oauthrefresh sweep
// （resource-authorization-expiry-sweep，GoWired）把到期的
// resource_authorization_grants 翻转为 expired 并在事务内写健康任务输入
// fanout（account_health_jobs_input_outbox）+ 全量统计脏标记；但 runtime
// 投影（resource_authorizations 行 + effective source 重算）属 gateway authz
// 域，jobs 进程不可 import（jobregistry GoBinding 冻结登记）。本方法按窗口
// 重放 runtime 投影，使任意一侧 sweep 抢到翻转都能收敛投影。
//
// 投影必须走 recomputeGrantRuntimes（发送-领取模型的唯一投影入口）：它按全部
// source 重新裁决，并只在终态时调用 disableRuntimeBindings（分组绑定、实例
// 账户停机）。窗口内重复重放保持幂等。
const (
	// expiryReconcileLookback 是重扫窗口：覆盖 jobs sweep 的 1 分钟调度
	// 节拍、抖动与短暂停机；窗口内的 expired grant 幂等重放。
	expiryReconcileLookback = 10 * time.Minute
	// expiryReconcileBatchLimit 是单次扫描的 grant 上限（防整表重放）。
	expiryReconcileBatchLimit = 100
)

// ReconcileExpiredGrants re-projects the runtime rows of grants the jobs
// sweep already flipped to expired within the lookback window (oldest first,
// batched). Returns the number of grants re-synced. Errors abort the current
// pass; the caller owns retry cadence.
func (s *Store) ReconcileExpiredGrants(ctx context.Context, lookback time.Duration, limit int) (int, error) {
	ctx = ensureCtx(ctx)
	if lookback <= 0 {
		lookback = expiryReconcileLookback
	}
	if limit <= 0 {
		limit = expiryReconcileBatchLimit
	}
	s.expiryReconcileMu.Lock()
	defer s.expiryReconcileMu.Unlock()
	now := s.now()
	nowText := now.UTC().Format("2006-01-02T15:04:05.000Z")
	cutoff := now.Add(-lookback).UTC().Format("2006-01-02T15:04:05.000Z")
	query := `SELECT id, updated_at FROM ` + s.table("resource_authorization_grants") + `
		WHERE status = 'expired'
			AND updated_at >= ?`
	args := []any{cutoff}
	if s.expiryReconcileUpdatedAt != "" {
		query += ` AND (updated_at > ? OR (updated_at = ? AND id > ?))`
		args = append(args, s.expiryReconcileUpdatedAt, s.expiryReconcileUpdatedAt, s.expiryReconcileID)
	}
	query += ` ORDER BY updated_at ASC, id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	lastUpdated, lastID := "", ""
	for rows.Next() {
		var id, updatedAt string
		if err := rows.Scan(&id, &updatedAt); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
		lastUpdated, lastID = updatedAt, id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		// The cursor fell outside the lookback window (or the table drained):
		// restart the cycle on the next call.
		s.expiryReconcileUpdatedAt, s.expiryReconcileID = "", ""
		return 0, nil
	}
	reconciled := 0
	for _, id := range ids {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return reconciled, err
		}
		var status string
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT status FROM `+s.table("resource_authorization_grants")+`
			WHERE id = ?`), id).Scan(&status); err != nil {
			tx.Rollback()
			return reconciled, err
		}
		// 只重放仍处于 expired 的 grant：并发翻转/回补（重新发送）后本方法
		// 不再触碰，交给对应写路径自己的投影链。
		if status != StatusExpired {
			tx.Rollback()
			continue
		}
		if err := s.recomputeGrantRuntimes(ctx, tx, id, "system:expiry", nowText); err != nil {
			tx.Rollback()
			return reconciled, err
		}
		if err := tx.Commit(); err != nil {
			return reconciled, err
		}
		reconciled++
		// Post-commit invalidation fan-out per committed replay: the replay
		// rewrites the runtime authorization rows, so the committed write
		// clears the gateway runtime cache like every other committed
		// authorization write. Best-effort, never blocks or fails the pass.
		s.invalidateAfterBusinessWrite(ctx, invalidationReasonExpired)
	}
	if len(ids) < limit {
		// End of the cycle: the next pass restarts from the oldest row.
		s.expiryReconcileUpdatedAt, s.expiryReconcileID = "", ""
	} else {
		s.expiryReconcileUpdatedAt, s.expiryReconcileID = lastUpdated, lastID
	}
	return reconciled, nil
}
