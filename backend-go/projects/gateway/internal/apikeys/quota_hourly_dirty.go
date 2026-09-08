package apikeys

// W2-A 待办1（BUG-0175 战役）：api_key 配额小时窗绑定重建后的脏范围打脏。
//
// 归档 authority: request-quota-hourly-windows.repository.ts
// syncApiKeyRequestQuotaHourlyWindowScopeBindingAsync :79-106 —— PG 分支在
// 绑定 upsert 之后调用 markPostgresRequestQuotaHourlyWindowDirtyScopes
// （:384-405，:105 调用点），让 jobs 侧增量消费
// （jobs internal/statsagg claimQuotaHourlyWindowDirtyScopes）立即重算该
// scope 的 usage_quota_hourly_windows；SQLite 分支（:53-77）不打脏——SQLite
// 调度走全量重建（refreshUsageQuotaHourlyWindowsCache），脏范围表在 SQLite
// 无消费者，写脏只会积累垃圾行。
//
// 失败契约：打脏是提交后的尾随效果，失败不回滚主写，落 warn 告警
// （与既有失效面同一位置：主事务提交成功后才执行）。

import (
	"context"
	"log/slog"
)

// statsTable qualifies a juhe_stats table (PostgreSQL schema-qualified, bare
// on SQLite), matching the authz/accounts stats table helpers.
func (s *Store) statsTable(name string) string {
	if s.pg {
		return "juhe_stats." + name
	}
	return name
}

// markQuotaHourlyWindowDirtyScope mirrors
// markPostgresRequestQuotaHourlyWindowDirtyScopes (:384-405) for one api_key
// scope: generation-counting upsert into the stats dirty scope table. Callable
// on either dialect (SQLite tests ATTACH juhe_stats); production callers gate
// on s.pg via markQuotaHourlyWindowDirtyScopeAfterCommit.
func (s *Store) markQuotaHourlyWindowDirtyScope(ctx context.Context, q queryer, ownerID, apiKeyID, timestamp string) error {
	query := `INSERT INTO ` + s.statsTable("usage_quota_hourly_window_dirty_scopes") + ` (
		system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
	) VALUES (?, 'api_key', ?, 1, ?, ?)
	ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
		generation = ` + s.statsTable("usage_quota_hourly_window_dirty_scopes") + `.generation + 1,
		updated_at = EXCLUDED.updated_at`
	_, err := q.ExecContext(ctx, s.bind(query), ownerID, apiKeyID, timestamp, timestamp)
	return err
}

// markQuotaHourlyWindowDirtyScopeAfterCommit runs the trailing dirty mark after
// the owning mutation transaction committed (PostgreSQL only, mirroring the
// archived PG-only :105 step). Failures never fail the committed mutation;
// they surface as a warn log and the scope converges on the next scheduler
// expiry sweep instead.
func (s *Store) markQuotaHourlyWindowDirtyScopeAfterCommit(ctx context.Context, ownerID, apiKeyID, timestamp string) {
	if !s.pg {
		return
	}
	if err := s.markQuotaHourlyWindowDirtyScope(ctx, s.db, ownerID, apiKeyID, timestamp); err != nil {
		slog.Warn("配额小时窗绑定打脏失败，等待调度轮 expiry 区间推进收敛",
			"event", "api_key_quota_hourly_window_dirty_mark_failed",
			"apiKeyId", apiKeyID, "error", err.Error())
	}
}
