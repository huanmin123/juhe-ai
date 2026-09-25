package cleanuprepo

import "context"

// TaskRunsRetentionStore 是 jobs 运行历史与共享租约两张表的保留清理。
//
// 背景（jobs 框架域排查缺陷1）：PG 模式下所有 GoWired 任务每轮经
// taskruns.RunWithTaskRun 写一行 background_task_runs（queued→running→终态），
// 1s 投影任务即可日增 8 万+ 行，此前这两张表在全仓没有任何删除路径，表无限
// 膨胀。两表没有业务 created_at 聚合游标依赖，不需要挂 usage records 清理链
// 那样的双聚合游标放行门，直接按时间窗滚动保留：
//   - background_task_runs 按 created_at 滚动保留（保留天数由组合根常量定，
//     取 30 天）；
//   - background_job_leases 按 lease_until 滚动删除过期行（scheduled 租约
//     释放只把 lease_until 置为当下留作 fencing 审计，窗口内审计行保留，
//     窗口外的行删除；临时任务租约按 run_id 逐行增长，同样靠本窗口收敛）。
//
// 表归属与 taskruns.Store 的 runsTable/leasesTable 一致：PG schema
// juhe_stats；SQLite 模式在独立的 task-runs 库（裸表名），由组合根以
// TaskRunsSQLitePath 句柄接入本 store，不走 stats 库句柄（SQLite standalone
// 的 stats 库没有这两张表，故不能并入 nonBusinessStatsCleanupTables 清单）。
type TaskRunsRetentionStore struct {
	DB *DB
}

// schema 返回 PG 方言的 schema 限定（SQLite 为空，走裸表名）。
func (s *TaskRunsRetentionStore) schema() string {
	if s.DB.Postgres {
		return "juhe_stats"
	}
	return ""
}

// CleanupRunsBefore 删除 created_at 严格早于 cutoff 的运行历史行，最旧优先，
// 最多 limit 行，返回删除行数。
func (s *TaskRunsRetentionStore) CleanupRunsBefore(ctx context.Context, cutoffCreatedAt string, limit int) (int64, error) {
	return deleteRowsBefore(ctx, s.DB, s.schema(), "background_task_runs", "created_at", cutoffCreatedAt, limit)
}

// CleanupExpiredLeasesBefore 删除 lease_until 严格早于 expiredBefore 的租约行
// （即已过期超过保留窗口的行），最旧优先，最多 limit 行，返回删除行数。
func (s *TaskRunsRetentionStore) CleanupExpiredLeasesBefore(ctx context.Context, expiredBefore string, limit int) (int64, error) {
	return deleteRowsBefore(ctx, s.DB, s.schema(), "background_job_leases", "lease_until", expiredBefore, limit)
}
