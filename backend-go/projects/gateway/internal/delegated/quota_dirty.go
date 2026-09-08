package delegated

// W2-A 待办2（BUG-0175 战役）：delegated api-key 状态补丁重建配额小时窗绑定
// 后的脏范围打脏。
//
// 归档 authority: request-quota-hourly-windows.repository.ts
// syncApiKeyRequestQuotaHourlyWindowScopeBindingAsync :79-106 —— PG 分支在
// 绑定 upsert 之后调用 markPostgresRequestQuotaHourlyWindowDirtyScopes
// （:384-405，:105 调用点），jobs 侧增量消费
// （internal/statsagg claimQuotaHourlyWindowDirtyScopes）随即重算该 scope 的
// usage_quota_hourly_windows；SQLite 分支不打脏（SQLite 调度走全量重建，
// 脏范围表无消费者）。
//
// 失败契约：打脏是提交后的尾随效果，失败不回滚主写，落 warn 告警。

import (
	"context"
	"database/sql"
	"log/slog"
)

// statsTable qualifies a juhe_stats table (PostgreSQL schema-qualified, bare
// on SQLite), matching the authz/apikeys stats table helpers.
func (d *Deps) statsTable(name string) string {
	if d.PGDialect {
		return "juhe_stats." + name
	}
	return name
}

// markQuotaHourlyWindowDirtyScope mirrors
// markPostgresRequestQuotaHourlyWindowDirtyScopes (:384-405) for one api_key
// scope: generation-counting upsert into the stats dirty scope table.
// Callable on either dialect (SQLite tests ATTACH juhe_stats); production
// callers gate on d.PGDialect via markQuotaHourlyWindowDirtyScopeAfterCommit.
func (d *Deps) markQuotaHourlyWindowDirtyScope(ctx context.Context, q sqlDB, systemAccountID, apiKeyID, timestamp string) error {
	query := `INSERT INTO ` + d.statsTable("usage_quota_hourly_window_dirty_scopes") + ` (
		system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
	) VALUES (?, 'api_key', ?, 1, ?, ?)
	ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
		generation = ` + d.statsTable("usage_quota_hourly_window_dirty_scopes") + `.generation + 1,
		updated_at = EXCLUDED.updated_at`
	_, err := q.ExecContext(ctx, d.bind(query), systemAccountID, apiKeyID, timestamp, timestamp)
	return err
}

// sqlDB is the minimal database handle the trailing mark needs (the patch
// transaction has already committed by the time this runs).
type sqlDB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// markQuotaHourlyWindowDirtyScopeAfterCommit runs the trailing dirty mark after
// the owning patch transaction committed (PostgreSQL only, mirroring the
// archived PG-only :105 step). Failures never fail the committed patch; they
// surface as a warn log and the scope converges on the next scheduler expiry
// sweep instead.
func (d *Deps) markQuotaHourlyWindowDirtyScopeAfterCommit(ctx context.Context, systemAccountID, apiKeyID, timestamp string) {
	if !d.PGDialect {
		return
	}
	if err := d.markQuotaHourlyWindowDirtyScope(ctx, d.DB, systemAccountID, apiKeyID, timestamp); err != nil {
		slog.Warn("配额小时窗绑定打脏失败，等待调度轮 expiry 区间推进收敛",
			"event", "delegated_api_key_quota_hourly_window_dirty_mark_failed",
			"apiKeyId", apiKeyID, "error", err.Error())
	}
}
