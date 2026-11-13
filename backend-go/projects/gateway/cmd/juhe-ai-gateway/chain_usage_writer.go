package main

// gateway → PG 用量直投（T188，2026-11-09 用户裁决）：删除 gateway 文件
// spool 与 jobs usagespooldrain，gateway 用共享 usagewriter 有界异步直写
// usage_records。定价冻结复用 shared usagepricing 目录适配器，与 jobs 原
// 路径同一语义。2026-11-13 BUG-0363 终审 M1（用户授权接线）：writer 装配
// 溢出 spool——内部队列满溢时记录落盘待 jobs Drainer 回放，不再终态丢弃。

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"path/filepath"
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
	// usageOverflowSpoolDirName 是 writer 溢出 spool 目录名，与 jobs 侧
	// worker_assembly.go 同名同根（DATA_DIR 派生）。
	usageOverflowSpoolDirName = "usage-record-overflow-spool"
	// usageOverflowSpoolInstanceDirectory 是 gateway 溢出文件的实例子目录名
	// （文件名仍为 `<UnixMilli>-<pid>-<uuid>.json` 同型；jobs 侧 Drainer 扫
	// 描根目录下全部实例子目录，名字只需各进程稳定不混淆）。
	usageOverflowSpoolInstanceDirectory = "gateway"
)

// gatewayUsageOverflowSpoolDirectory 派生 writer 溢出 spool 根目录：DATA_DIR
// 下固定名，与 jobs 侧 workerUsageOverflowSpoolDirectory 同根同名。单机
// compose（docker/single-server）gateway 与 jobs 的 data/ 是同一宿主目录，
// 回放闭环成立。
func gatewayUsageOverflowSpoolDirectory(dataDir string) string {
	return filepath.Join(dataDir, usageOverflowSpoolDirName)
}

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
// 供定价目录读取，dataDir 是 DATA_DIR 解析结果（溢出 spool 根目录派生）。
// 调用方负责把返回的 close 注册到 shutdown 链。
func newUsageWriterDeliverer(db, businessDB *sql.DB, dataDir string) (usageRecordDeliverer, func()) {
	if db == nil {
		return nil, func() {}
	}
	store := usagewriter.NewPostgresShardStore(usagewriter.PostgresShardStoreConfig{
		DB:         db,
		ShardCount: gatewayUsageWriterShardCount,
		BusinessDB: businessDB,
	})
	// 溢出 spool（BUG-0363 终审 M1，2026-11-13 用户授权接线）：writer 内部
	// 队列满溢时记录原子落盘 <DATA_DIR>/usage-record-overflow-spool/gateway/
	// 而非终态丢弃；溢出记录在 Enqueue 内已 NormalizeUsageRecordInput（稳定
	// id/createdAt），jobs usagespooldrain.Drainer 扫描同一 DATA_DIR 根下全
	// 部实例子目录（含 gateway/）幂等回放 EnqueueDurable（ON CONFLICT DO
	// NOTHING），格式与 jobs 侧同构。
	//   - 单机 compose：gateway 与 jobs 的 data/ 是同一宿主目录
	//     （./data/app/data，compose.yml 两服务同挂载），回放闭环。
	//   - 多节点/不同源形态（如 docker/cluster 各节点本地盘）：gateway 溢出
	//     文件滞留本节点不丢（temp+rename 原子落盘、文件名含 pid/uuid 不互
	//     覆），但 jobs 扫描不到；可手工把 gateway/ 子目录复制到 jobs 可见的
	//     同根 usage-record-overflow-spool/ 下等待回放，或按文件内记录直接
	//     手工入库——文件持久在盘，无自动清理，不处理只占磁盘。
	// Persist 失败不丢记录：writer 按 T207-C2 语义强制回队延迟重试；目录
	// 恒非空（DATA_DIR 派生），恒接线，无 env/开关。
	overflowSpool := usagewriter.NewFileOverflowSpool(gatewayUsageOverflowSpoolDirectory(dataDir))
	overflowSpool.InstanceDirectory = usageOverflowSpoolInstanceDirectory
	writer := usagewriter.NewWriter(usagewriter.Config{
		ShardCount:       gatewayUsageWriterShardCount,
		FreezePricing:    true,
		MaxWriteAttempts: gatewayUsageWriterMaxAttempts,
	}, store, nil,
		usagewriter.WithLogger(usageWriterLogger{inner: slog.Default()}),
		usagewriter.WithCatalog(usagepricing.NewCatalog(businessDB)),
		usagewriter.WithOverflowSpool(overflowSpool))
	writer.Start()
	return usageWriterDeliverer{writer: writer}, func() {
		// 停机排空必须有界：DB 黑洞时 writer 内部的 flush goroutine 可能仍
		// 卡在一次查询上，Close 只等到超时即返回，进程随后退出（compose
		// stop_grace_period 30s 内完成）。不使用 Background 无限等待。
		// 停机收尾与 jobs 侧对齐：FileOverflowSpool 无状态（每次 Persist 原
		// 子 temp+rename，无需 flush/close）；writer.Close 的有界排空只覆盖
		// 内存队列，已落盘文件由 jobs Drainer 在其运行节拍内回放，gateway
		// 无本地消费面（与 jobs「溢出文件留待下次启动消费」同语义）。
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
