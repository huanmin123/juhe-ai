package authz

import (
	"context"
	"database/sql"
	"time"
)

// T6d 登记缺口的 gateway 侧最小消费面：jobs oauthrefresh sweep
// （resource-authorization-expiry-sweep，GoWired）把到期的
// resource_authorization_grants 翻转为 expired 并在事务内写健康任务输入
// fanout（account_health_jobs_input_outbox）+ 全量统计脏标记；但 runtime
// 投影（resource_authorizations 行 + effective source 重算）与 quota 窗口
// scope bindings 属 gateway authz sync 域，jobs 进程不可 import
// （jobregistry GoBinding 冻结登记）。本方法按窗口重放这两段下游同步，使
// 任意一侧 sweep 抢到翻转都能收敛投影。
//
// 语义对照 Node expireDueResourceAuthorizationsAsync 的 per-grant 下游
// （write.repository.ts:953-962 → syncResourceAuthorizationGrantRuntimeAsync
// :995-1002）：grant 已是 expired 时 syncUserGrantRuntime/syncTeamGrantRuntime
// 的 expired 分支是显式写回（幂等），refreshEffectiveSource 以
// preserveExpired 默认值保持 expired 终态；随后
// syncGrantQuotaScopeBindings 按 grant 重挂绑定（expired 投影不再渲染
// hourly 窗口绑定，等价删除）。健康任务输入 fanout 不在本方法内重放——
// jobs sweep 已在翻转事务内写入，重复入队只会空耗 J1 input version。
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
	now := s.now()
	nowText := now.UTC().Format("2006-01-02T15:04:05.000Z")
	cutoff := now.Add(-lookback).UTC().Format("2006-01-02T15:04:05.000Z")
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_grants")+`
		WHERE status = 'expired'
			AND updated_at >= ?
		ORDER BY updated_at ASC, id ASC
		LIMIT ?`), cutoff, limit)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
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
		if err := s.syncGrantQuotaScopeBindings(ctx, tx, &next, nowText); err != nil {
			tx.Rollback()
			return reconciled, err
		}
		if err := tx.Commit(); err != nil {
			return reconciled, err
		}
		reconciled++
	}
	return reconciled, nil
}
