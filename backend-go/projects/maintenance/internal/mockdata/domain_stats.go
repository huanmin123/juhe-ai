package mockdata

import "context"

// seedStatsRaw 是统计域：写 stats 库的明细与原始样本（后台任务运行、系统指标
// 样本、事件循环样本、IP 统计输入、账户用量快照、统计扣减），并调用既有聚合器
// 重建派生聚合表。
//
// 为什么不直接往派生聚合表塞行：派生族（usage_stats_*、usage_model_*、
// usage_error_*、usage_latency_*、usage_rank_snapshots、*_windows、
// account_quality_*、account_health_hourly、group_account_stats、client_ip_*、
// authorization_*_usage_*、system_metrics_*、process_event_loop_*、
// go_runtime_metrics_*）的口径必须与线上读路径一致，只能由聚合器从明细重建；
// autofill.go 已把这些表排除在自动补全之外。
//
// 实施要点（给域实现者）：重建聚合使用 jobs 侧已经存在的聚合器语义（handler /
// repository port），本域只负责按 Options.Days 造出足够的明细输入。
// TODO(domain): 由域实现替换
func seedStatsRaw(ctx context.Context, e *env) (DomainResult, error) {
	return DomainResult{Name: DomainStats, Counts: map[string]int{}}, nil
}
