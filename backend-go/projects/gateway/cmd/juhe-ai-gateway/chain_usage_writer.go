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

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-platform/usagepricing"
	"github.com/huanminabc/juhe-ai/backend-go-platform/usagewriter"
)

const (
	// gatewayUsageWriterShardCount / MaxWriteAttempts 与 jobs 原装配同值。
	gatewayUsageWriterShardCount  = 16
	gatewayUsageWriterMaxAttempts = 120
)

// usageWriterDeliverer 把 gatewayusage 记录转换为 usagewriter 输入并直投 PG。
type usageWriterDeliverer struct {
	writer *usagewriter.Writer
}

// convertUsageRecordInput 把 gatewayusage 记录转换为 usagewriter 输入。两端
// 字段契约逐字段一致（shared/platform/usagewriter/doc.go 的 G20 契约），用
// JSON 桥避免手写 50+ 字段拷贝漂移。
func convertUsageRecordInput(input gatewayusage.UsageRecordInput) (usagewriter.UsageRecordInput, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return usagewriter.UsageRecordInput{}, err
	}
	var converted usagewriter.UsageRecordInput
	if err := json.Unmarshal(payload, &converted); err != nil {
		return usagewriter.UsageRecordInput{}, err
	}
	// JSON 桥会把 any 类型的 pricingSnapshot 解成 map；writer 的冻结/落库路径
	// 只识别 *CostBreakdown，原样保留 map 会让已冻结快照退化为 fallback。
	// 这里按同名字段重新解码为 typed CostBreakdown；形状不匹配时保留原值
	// （与旧 spool 路径行为一致，不丢记录）。
	if converted.PricingSnapshot != nil {
		raw, err := json.Marshal(converted.PricingSnapshot)
		if err == nil {
			var breakdown usagewriter.CostBreakdown
			if err := json.Unmarshal(raw, &breakdown); err == nil {
				converted.PricingSnapshot = &breakdown
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
		writer.Close(context.Background())
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
