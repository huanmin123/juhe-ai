package main

// 文件 spool 测试替身（2026-11-09 T188）。生产用量记录已由网关直投 PG：
// usage-record-spool 目录、JUHE_AI_USAGE_SPOOL_* 容量 env 与 gateway→jobs
// 交接全部删除。本文件保留原 gatewayusage.UsageRecordSpool 的构造 helper，
// 仅供组合/适配层测试作为可读的持久投递面替身使用。

import (
	"log/slog"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// usageSpoolCapacity 汇集历史 runtimeConfig.usageSpool 的四个容量旋钮；
// <=0 的字段回落原默认值（测试替身保持与旧装配同容量语义）。
type usageSpoolCapacity struct {
	MaxItems         int
	MaxBytes         int
	ReplayBatchSize  int
	ReplayIntervalMs int
}

func newUsageSpool(directory string, clock gatewaypreauth.Clock, logger *slog.Logger, capacity usageSpoolCapacity) *gatewayusage.UsageRecordSpool {
	if directory == "" {
		return nil
	}
	if capacity.MaxItems <= 0 {
		capacity.MaxItems = 250_000
	}
	if capacity.MaxBytes <= 0 {
		capacity.MaxBytes = 4_096 * 1024 * 1024
	}
	if capacity.ReplayBatchSize <= 0 {
		capacity.ReplayBatchSize = 500
	}
	if capacity.ReplayIntervalMs <= 0 {
		capacity.ReplayIntervalMs = 1_000
	}
	return gatewayusage.NewUsageRecordSpool(gatewayusage.SpoolConfig{
		Directory:        directory,
		InstanceID:       "gateway-chain",
		MaxItems:         capacity.MaxItems,
		MaxBytes:         capacity.MaxBytes,
		ReplayBatchSize:  capacity.ReplayBatchSize,
		ReplayIntervalMs: capacity.ReplayIntervalMs,
		Enabled:          true,
	}, clock, slogLogger{inner: logger})
}

// spoolOverflow 是 gatewayusage.DispatchOverflowSpool 的测试替身。
type spoolOverflow struct {
	spool *gatewayusage.UsageRecordSpool
}

func (o spoolOverflow) PersistOverflow(ctx gatewayusage.Ctx, input gatewayusage.UsageRecordInput) error {
	if o.spool == nil {
		return nil
	}
	return o.spool.Persist(ctx, input)
}
