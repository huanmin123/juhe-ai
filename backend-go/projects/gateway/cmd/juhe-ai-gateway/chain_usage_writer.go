package main

// gateway → PG 用量直投（T188，2026-11-09 用户裁决）：删除 gateway 文件
// spool 与 jobs usagespooldrain，gateway 用共享 usagewriter 有界异步直写
// usage_records。定价冻结复用 shared usagepricing 目录适配器，与 jobs 原
// 路径同一语义。

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-platform/usagepricing"
	"github.com/huanminabc/juhe-ai/backend-go-platform/usagewriter"
)

const (
	// gatewayUsageWriterShardCount / MaxWriteAttempts 与 jobs 原装配同值。
	gatewayUsageWriterShardCount  = 16
	gatewayUsageWriterMaxAttempts = 120
	// gatewayUsageWriterCloseTimeout 是进程停机时等待 writer 排空的上限；与
	// recorder 排空（4s）合计 <=9s，整体 shutdown 保持在 compose
	// stop_grace_period=30s 内。
	gatewayUsageWriterCloseTimeout = 5 * time.Second
)

// usageWriterDeliverer 把 gatewayusage 记录转换为 usagewriter 输入并直投 PG。
type usageWriterDeliverer struct {
	writer *usagewriter.Writer
}

// convertUsageRecordInput 把 gatewayusage 记录交给 usagewriter。2026-11-09
// T188 起两处是同一类型别名（gatewayusage.UsageRecordInput =
// usagewriter.UsageRecordInput），常规路径零拷贝；只有已冻结的
// pricingSnapshot 需要从 any 收紧为 typed CostBreakdown（writer 落库路径只
// 识别 *CostBreakdown）。形状不匹配时保留原值，不丢记录。
func convertUsageRecordInput(input gatewayusage.UsageRecordInput) (usagewriter.UsageRecordInput, error) {
	converted := input
	if converted.PricingSnapshot != nil {
		if _, ok := converted.PricingSnapshot.(*usagewriter.CostBreakdown); !ok {
			raw, err := json.Marshal(converted.PricingSnapshot)
			if err == nil {
				var breakdown usagewriter.CostBreakdown
				if err := json.Unmarshal(raw, &breakdown); err == nil {
					converted.PricingSnapshot = &breakdown
				}
			}
		}
	}
	return converted, nil
}

// Persist 实现 usageRecordDeliverer。
func (d usageWriterDeliverer) Persist(ctx gatewayusage.Ctx, input gatewayusage.UsageRecordInput) error {
	converted, err := convertUsageRecordInput(input)
	if err != nil {
		return err
	}
	return d.writer.Enqueue(ctx, converted)
}

// newUsageWriterDeliverer 构造生产直投器；db 为 usage 库句柄，businessDB
// 供定价目录读取。调用方负责把返回的 close 注册到 shutdown 链。
func newUsageWriterDeliverer(db, businessDB *sql.DB) (usageRecordDeliverer, func()) {
	if db == nil {
		return nil, func() {}
	}
	store := usagewriter.NewPostgresShardStore(usagewriter.PostgresShardStoreConfig{
		DB:         db,
		ShardCount: gatewayUsageWriterShardCount,
		BusinessDB: businessDB,
	})
	writer := usagewriter.NewWriter(usagewriter.Config{
		ShardCount:       gatewayUsageWriterShardCount,
		FreezePricing:    true,
		MaxWriteAttempts: gatewayUsageWriterMaxAttempts,
	}, store, nil,
		usagewriter.WithLogger(usageWriterLogger{inner: slog.Default()}),
		usagewriter.WithCatalog(usagepricing.NewCatalog(businessDB)))
	writer.Start()
	return usageWriterDeliverer{writer: writer}, func() {
		// 停机排空必须有界：DB 黑洞时 writer 内部的 flush goroutine 可能仍
		// 卡在一次查询上，Close 只等到超时即返回，进程随后退出（compose
		// stop_grace_period 30s 内完成）。不使用 Background 无限等待。
		closeCtx, cancel := context.WithTimeout(context.Background(), gatewayUsageWriterCloseTimeout)
		defer cancel()
		writer.Close(closeCtx)
	}
}

type usageWriterLogger struct {
	inner *slog.Logger
}

func (l usageWriterLogger) Warn(msg string, fields map[string]any) {
	l.inner.Warn(msg, fieldsArgs(fields)...)
}

func (l usageWriterLogger) Error(msg string, fields map[string]any) {
	l.inner.Error(msg, fieldsArgs(fields)...)
}
