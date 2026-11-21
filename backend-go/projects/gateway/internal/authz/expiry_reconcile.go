package authz

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// T6d 登记缺口的 gateway 侧消费面：jobs oauthrefresh sweep
// （resource-authorization-expiry-sweep，GoWired）把到期的
// resource_authorization_grants 翻转为 expired 并在事务内写健康任务输入
// fanout（account_health_jobs_input_outbox）+ 全量统计脏标记；但 runtime
// 投影（resource_authorizations 行 + effective source 重算）、接收方通知与
// quota 窗口 scope bindings 属 gateway authz 域，jobs 进程不可 import
// （jobregistry GoBinding 冻结登记）。本方法按窗口重放这些下游同步，使
// 任意一侧 sweep 抢到翻转都能收敛投影。
//
// 投影必须走 recomputeGrantRuntimes（接收方同意模型的唯一投影入口），不能
// 复用 legacy syncUserGrantRuntime/syncTeamGrantRuntime：后者按 grant 直接
// 把 runtime 写成 expired，会误伤同一 runtime 上仍有其它 active source 的
// 多来源场景；recomputeRuntime 按全部 source 重新裁决，并只在终态时调用
// disableRuntimeBindings（分组绑定、授权分组开关、实例账户停机）。通知与
// expireGrant 保持同一 dedupe key（expiry_cycle 参与），这样 extend 后再次
// 到期仍会生成新通知，而窗口内重复重放保持幂等。健康任务输入 fanout 不在
// 本方法内重放——jobs sweep 已在翻转事务内写入，重复入队只会空耗 J1 input
// version。
const (
	// expiryReconcileLookback 是重扫窗口：覆盖 jobs sweep 的 1 分钟调度
	// 节拍、抖动与短暂停机；窗口内的 expired grant 幂等重放。
	expiryReconcileLookback = 10 * time.Minute
	// expiryReconcileBatchLimit 是单次扫描的 grant 上限（防整表重放）。
	expiryReconcileBatchLimit = 100
)

// ReconcileExpiredGrants re-projects the runtime rows and quota scope
// bindings of grants the jobs sweep already flipped to expired within the
// lookback window (oldest first, batched). Returns the number of grants
// re-synced. Errors abort the current pass; the caller owns retry cadence.
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
		grant, err := s.GetGrantForMutation(ctx, tx, id)
		if err != nil {
			tx.Rollback()
			return reconciled, err
		}
		// 只重放仍处于 expired 的 grant：并发翻转/回补（重新激活）后本方法
		// 不再触碰，交给对应写路径自己的 sync 链。
		if grant == nil || grant.Status != StatusExpired {
			tx.Rollback()
			continue
		}
		// Node :958-962：sync actor = revoked_by ?? created_by。
		actor := ""
		if grant.RevokedBy.Valid && grant.RevokedBy.String != "" {
			actor = grant.RevokedBy.String
		} else if grant.CreatedBy.Valid {
			actor = grant.CreatedBy.String
		}
		next := *grant
		if !(grant.RevokedAt.Valid && grant.RevokedAt.String != "") {
			// jobs sweep 已盖 revoked_at；缺失时补当前时刻（幂等重放不改历史）。
			next.RevokedAt = sql.NullString{String: nowText, Valid: true}
		}
		next.UpdatedAt = nowText
		var expiryCycle int
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT COALESCE(expiry_cycle, 0) FROM `+s.table("resource_authorization_grants")+`
			WHERE id = ?`), grant.ID).Scan(&expiryCycle); err != nil {
			tx.Rollback()
			return reconciled, err
		}
		// 新模型 grant 至少有一条 grant_id 关联 source；有则按该 grant 关联的
		// 全部 source 重算 runtime，多来源场景下其它 active source 仍可保持
		// runtime active。零关联 source 是迁移前的 legacy grant（其 source 行
		// 没有 grant_id，或尚未生成 source），仍走 legacy 单 grant 投影以保留
		// revoked_at 写回与 failpoint 语义；迁移完成后该分支自然消失。
		var linkedSources int
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("resource_authorization_sources")+`
			WHERE grant_id = ?`), grant.ID).Scan(&linkedSources); err != nil {
			tx.Rollback()
			return reconciled, err
		}
		if linkedSources == 0 {
			if grant.GranteeType == "system_account" {
				if err := s.syncUserGrantRuntime(ctx, tx, &next, actor, nowText); err != nil {
					tx.Rollback()
					return reconciled, err
				}
			} else {
				if err := s.syncTeamGrantRuntime(ctx, tx, &next, actor, nowText); err != nil {
					tx.Rollback()
					return reconciled, err
				}
			}
		} else if err := s.recomputeGrantRuntimes(ctx, tx, grant.ID, actor, nowText); err != nil {
			tx.Rollback()
			return reconciled, err
		}
		for _, target := range s.grantRuntimeGrantees(ctx, tx, grant.ID) {
			if err := s.insertNotification(ctx, tx, target, "offer_expired", grant.ID, "", nil,
				fmt.Sprintf("expired:%s:%d:%s", grant.ID, expiryCycle, target), nowText); err != nil {
				tx.Rollback()
				return reconciled, err
			}
		}
		if err := s.syncGrantQuotaScopeBindings(ctx, tx, &next, nowText); err != nil {
			tx.Rollback()
			return reconciled, err
		}
		if err := tx.Commit(); err != nil {
			return reconciled, err
		}
		reconciled++
		// Post-commit invalidation fan-out per committed replay (网关模型列表
		// 账户并集设计 6.3 coverage): the replay rewrites the runtime
		// authorization rows, so the committed write clears the gateway runtime
		// cache like every other committed authorization write. Best-effort,
		// never blocks or fails the reconcile pass.
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
