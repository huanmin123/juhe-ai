package cleanuprepo

import "context"

// ClientIPRetentionStore 是 IP 维度统计两张只增表的保留清理。
//
// 背景（2026-11-09 全量清理核对）：`client_ip_stats_daily`（每 IP 每日一行）
// 与 `client_ip_policy_hits`（每策略命中每日一行）此前只出现在
// nonBusinessStatsCleanupTables 清单里，而该清单的唯一执行入口
// non_business_data_cleanup 的 gateway 生产者已随表监控页删除（提交
// f1991b708），jobs 侧只 drain 存量行，现行无任何任务会再投递它。两张表
// 因此只增不减。两张表都是纯统计/审计派生数据（读面最大 31 天，超窗行无
// 消费方），按 35 天滚动窗口清理即可；窗口天数、批次与节奏由调用方（jobs
// 组合根 data-retention-cleanup）承担。
//
// `client_ip_registry` 刻意不在此清理：它是 IP 管理列表的成员全集，且网关
// 封禁执行独立读 `client_ip_policies`，删除 registry 行会让仍生效的策略从
// 管理页消失但网关继续拦截。详见 IP统计与封禁设计「数据保留」。
type ClientIPRetentionStore struct {
	DB *DB
}

// schema 返回 PG 方言的 schema 限定（SQLite 为空，走裸表名）。
func (s *ClientIPRetentionStore) schema() string {
	if s.DB.Postgres {
		return "juhe_stats"
	}
	return ""
}

// CleanupDailyStatsBefore 删除 stat_date 严格早于 cutoffDate 的 IP 日统计行，
// 最旧优先，单批最多 limit 行，返回删除行数。stat_date 是业务时区的
// `YYYY-MM-DD` 键（与统计聚合写入格式一致）。
func (s *ClientIPRetentionStore) CleanupDailyStatsBefore(ctx context.Context, cutoffDate string, limit int) (int64, error) {
	return deleteRowsBefore(ctx, s.DB, s.schema(), "client_ip_stats_daily", "stat_date", cutoffDate, limit)
}

// CleanupPolicyHitsBefore 删除 stat_date 严格早于 cutoffDate 的 IP 策略命中
// 汇总行，最旧优先，单批最多 limit 行，返回删除行数。
func (s *ClientIPRetentionStore) CleanupPolicyHitsBefore(ctx context.Context, cutoffDate string, limit int) (int64, error) {
	return deleteRowsBefore(ctx, s.DB, s.schema(), "client_ip_policy_hits", "stat_date", cutoffDate, limit)
}
