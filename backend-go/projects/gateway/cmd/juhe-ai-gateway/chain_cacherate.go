package main

// 期二（缓存率感知调度与用量缓存率展示设计 8.1）：缓存率快照加载器的组合
// 根装配。加载器读统计库句柄（usage_stats_hourly 的 global/account 行），
// 时区复用统计侧同源的 usageStatsTimezone 解析器（statreads.
// NewSystemSettingsTimezoneSource，业务库 system_settings），刷新失败保留
// 旧快照并限频 WARN——本文件只做参数装配与启动投递，快照语义全部在
// internal/gatewaycacherate 契约内。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycacherate"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/statreads"
)

// newChainCacheRateSnapshotSource 构造缓存率快照加载器（设计 8.1 端口
// CacheRateSnapshotSource 的 adapter）：
//   - statsDB：统计库句柄（PG 共享业务池 + juhe_stats. 限定 / SQLite 独立
//     stats 文件句柄，与 accounts.NewStatsUsageSource 同一装配口径）；
//   - businessDB：业务库句柄，承载 usageStatsTimezone 系统设置读；
//   - 失败语义：刷新失败保留旧快照 + 每分钟至多一条 WARN（loader 内实现），
//     构造期仅校验句柄非空，缺句柄属于组合根装配缺陷（fail-fast）。
func newChainCacheRateSnapshotSource(statsDB *sql.DB, businessDB *sql.DB, postgres bool) (*gatewaycacherate.SnapshotSource, error) {
	if statsDB == nil {
		return nil, errors.New("缓存率快照加载器要求统计库句柄")
	}
	if businessDB == nil {
		return nil, fmt.Errorf("缓存率快照加载器要求业务库句柄（usageStatsTimezone 源）")
	}
	return gatewaycacherate.NewSnapshotSource(
		statsDB,
		postgres,
		statreads.NewSystemSettingsTimezoneSource(businessDB, postgres),
		time.Now,
		func(message string) {
			slog.Default().Warn("缓存率快照刷新失败，保留旧快照（缓存率维度超龄后转中性）",
				"event", "gateway_cache_rate_snapshot_refresh_failed", "error", message)
		},
	)
}

// startChainCacheRateWarmup 在组合根后台序列投递首次加载（设计 8.1 Warmup；
// 失败仅日志——Snapshot 的 TTL 调度会在首个请求窗口重试）。返回取消函数：
// 与 startUpstreamClientVersionAutoRefresh 同风格登记进 shutdowns，进程停
// 机时 goroutine 随 ctx 退出。
func startChainCacheRateWarmup(source *gatewaycacherate.SnapshotSource) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		source.Warmup(ctx)
	}()
	return cancel
}
