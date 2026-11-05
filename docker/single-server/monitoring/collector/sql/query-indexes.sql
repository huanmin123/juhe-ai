-- 监控查询依赖的索引（应用库 owner 执行一次；不在 monitoring-role.sql 内）
--
-- 为什么单独放：索引建在 juhe_stats.background_task_runs 上，需要表 owner 权限，
-- 监控只读角色 juhe_monitor 无权创建。缺失时 queries.yaml 的 juhe_app_job_stall
-- 查询会退化为全表扫描（历史事故 BUG-0313：2GB/181 万行，每 60s 扫一次，把磁盘打满）。
--
-- 执行（应用机）：
--   docker exec -i juhe-ai-postgres psql -U juhe_ai -d juhe_ai < sql/query-indexes.sql
--
-- 幂等：IF NOT EXISTS；已存在时 no-op（生产已有同名索引时不会被覆盖）。
-- 大表上建索引会持写锁，请在低峰执行。

-- 任务停摆查询的访问路径：按 job_name 定位，取 finished_at 最大的一行。
-- 部分索引排除未完成运行（finished_at 为空串），索引更小。
CREATE INDEX IF NOT EXISTS idx_bg_task_runs_job_finished
  ON juhe_stats.background_task_runs (job_name, finished_at DESC)
  WHERE finished_at <> '';

-- 验证（应看到 Index Scan，而不是 Seq Scan）
-- EXPLAIN (ANALYZE, BUFFERS)
-- SELECT nullif(finished_at,'')::timestamptz
-- FROM juhe_stats.background_task_runs
-- WHERE job_name = '<任一任务名>' AND finished_at <> ''
-- ORDER BY finished_at DESC LIMIT 1;
