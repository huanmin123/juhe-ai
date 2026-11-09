package main

// worker_ingestgate.go 组合根的 ingest 排干门控装配（T6a）：
// Node usageStatsAggregationSafety 经 ingest-worker IPC 获取队列快照
// （background_worker_ingest_status_request，按 Go 总设计消灭）；Go 单进程内
// 直接读 usagewriter.Runtime() 构造 ingestgate.Probe，供
// usage-stats-aggregation / client-ip-stats-aggregation / account-quality-refresh
// 三个消费 usage_records 游标的任务前置门控。
//
// 游标安全水位的多源覆盖（统计链路排查 B①）：writer 内存队列只是其中一段；
// gateway spool 交接文件与 writer 溢出 spool 文件由本进程的两个 drain 持有，
// 记录可能滞留超过安全水位（默认 15s）才入表，游标一旦越过其 created_at
// 即永久不聚合。probe 取三源最旧（min）注入 DrainStatus，由 ingestgate 的
// oldestPendingUsageRecordCreatedAt / safeCreatedBeforeForPendingBacklog 统一
// 回退游标。多源读取顺序本身是游标安全约束：必须先读 spool 水位、后读
// writer runtime（DD3-B1，论证见 assembleIngestDrainStatus），反序在 drain
// 末批交错下存在两源双漏窗口、游标永久越过积压记录。gateway 进程内 buffer
// （spool 落盘之前的一段）在本进程边界之外、无法观测；T93-6 起按
// ingestgate.DefaultGatewayBufferMaxAge（5min）作为保守下界压住安全水位
// （ingestDrainSafety 的注入点），迟到入表的记录不再被游标越过。

import (
	"context"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/ingestgate"
	"github.com/huanminabc/juhe-ai/backend-go-platform/usagewriter"
)

// gateFunc 将门控闭包适配为包侧 IngestDrainGate port（accountquality 与
// statsverify 各自定义同形消费端接口，一个适配器同时满足）。
type gateFunc func(ctx context.Context) error

// EnsureUsageRecordsIngested 实现 accountquality.IngestDrainGate /
// statsverify.IngestDrainGate。
func (f gateFunc) EnsureUsageRecordsIngested(ctx context.Context) error { return f(ctx) }

// ingestDrainProbe 把 usagewriter 运行态与两个 spool drain 的待删水位适配
// 为 ingestgate.Probe。writer 未装配时返回 (nil, nil)：等价 Node
// ingest worker 不可达（requestIngestWorkerDrainStatus → undefined），
// 门控按"快照不可用"失败本轮，不静默放行。
func (a *workerAssembly) ingestDrainProbe() ingestgate.Probe {
	return func(ctx context.Context) (*ingestgate.DrainStatus, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		writer := a.writer
		if writer == nil {
			return nil, nil
		}
		return assembleIngestDrainStatus(
			func() string { return a.spoolPendingOldestCreatedAt() },
			func() usagewriter.Runtime { return writer.Runtime() },
		), nil
	}
}

// assembleIngestDrainStatus 把两段积压源读取装配为 DrainStatus。两段读取以
// thunk 注入，读序即游标安全契约：**必须先读 spool 水位（T1）、后读 writer
// runtime（T2）**（统计链路排查 B①/DD3-B1，2026-10-31）。
//
// 论证（记录单向迁移：spool 文件 → writer 内存队列 → usage_records 表，无
// 逆向路径；drain goroutine 与本 probe 并发，两段读取之间存在任意交错）：
// 门控的正确性要求"读取时点尚未入表的积压记录"必被至少一个读点观测到，
// 否则 safeCreatedBefore 回退默认 now-15s、聚合游标越过其 created_at，记录
// 随后入表即永久漏聚合。按水位居前、runtime 居后的读序，该不变式成立：
//
//   - T1 仍滞留在 spool 文件的记录必被水位覆盖（文件只能迁出，不会凭空
//     消失）；T1 后经队列 flush 入表的记录，其 created_at 已把
//     safeCreatedBefore 压到该记录之前（oldest-1ms），游标不越过——入表后
//     由本轮（上界之内）或后续轮次聚合覆盖。
//   - T1 时已在 writer 队列的记录：T2 时要么仍在队列（runtime 的
//     OldestCreatedAt 观测到），要么已 flush 入表（聚合查询直接可见），
//     不存在第三去向。
//   - 反序（先 runtime 后水位）存在双漏窗口：T1 读 runtime 时记录仍在
//     spool 文件（未观测）→ T1~T2 间 drain 完成末批（入队、删文件、水位
//     清空）→ T2 读水位为空（未观测）→ 记录尚未入表而两源皆空，游标以
//     默认 now-15s 推进越过其 created_at，入表后永久漏聚合。
func assembleIngestDrainStatus(readSpoolOldest func() string, readRuntime func() usagewriter.Runtime) *ingestgate.DrainStatus {
	spoolOldest := readSpoolOldest()
	runtime := readRuntime()
	return &ingestgate.DrainStatus{
		// Node ready：ingest worker 就绪；Go 进程内 writer 存在即就绪。
		Ready: true,
		// Node snapshot.usageRecordQueue：writer 内存队列段。
		SnapshotUsageRecordQueueOldestCreatedAt:   runtime.OldestCreatedAt,
		SnapshotUsageRecordQueueFlushFailureCount: runtime.FlushFailureCount,
		// C2-H2：死信/丢弃计数。死信已脱离队列与 spool 水位，必须单独进入
		// 门控；丢弃计数供 /health 观测（不硬阻断，见 ingestgate 字段注释）。
		SnapshotUsageRecordQueueDeadLetterCount: runtime.ActiveDeadLetterCount,
		SnapshotUsageRecordQueueDroppedCount:    runtime.DroppedCount,
		// Node pendingQueues.usageRecords envelope：Go 单进程承载为
		// spool 文件段——gateway 交接 drain 与 writer 溢出回放 drain
		// 各自仍持有（未确认删除）的队头记录 created_at，取最旧。
		PendingUsageRecordsOldestCreatedAt: spoolOldest,
		// Redis Stream 分支随 queueDriver=redis_stream 消灭（Go 直写分片）。
	}
}

// spoolPendingOldestCreatedAt 汇总溢出回放的待删水位；无积压返回空串。
// 时间值为 usagewriter 归一化后的 RFC3339 毫秒 UTC，解析失败按不可知处理
// （返回空串，由门控按默认水位放行）。
func (a *workerAssembly) spoolPendingOldestCreatedAt() string {
	var candidates []string
	if a.usageOverflowReplay != nil {
		if value := a.usageOverflowReplay.OldestPendingCreatedAt(); value != "" {
			candidates = append(candidates, value)
		}
	}
	oldest := ""
	for _, candidate := range candidates {
		if oldest == "" {
			oldest = candidate
			continue
		}
		if spoolWatermarkEarlier(candidate, oldest) {
			oldest = candidate
		}
	}
	return oldest
}

// spoolWatermarkEarlier 比较两个 RFC3339 时间值的先后（a 更早返回 true）。
func spoolWatermarkEarlier(a, b string) bool {
	parsedA, errA := time.Parse(time.RFC3339Nano, a)
	parsedB, errB := time.Parse(time.RFC3339Nano, b)
	if errA != nil {
		return false
	}
	if errB != nil {
		return true
	}
	return parsedA.Before(parsedB)
}

// ingestDrainGate 返回 Node ensureUsageRecordsSafeForStatsAggregation 的
// Go 等价门控闭包（失败本轮，不产出 safeCreatedBefore）。
func (a *workerAssembly) ingestDrainGate() func(ctx context.Context) error {
	return ingestgate.Gate(a.ingestDrainProbe(), time.Now)
}

// ingestDrainSafety 执行门控并返回游标安全截止时间
// （usage-stats-aggregation 专用：Node 把 safety.safeCreatedBefore 传给
// aggregate_usage_stats）。T93-6 的 gateway buffer 滞留估计在此显式注入：
// 默认取 ingestgate.DefaultGatewayBufferMaxAge（5min），需要调整保守下界时
// 只改这一处传入值。
func (a *workerAssembly) ingestDrainSafety(ctx context.Context) (ingestgate.Safety, error) {
	return ingestgate.CheckWithOptions(ctx, a.ingestDrainProbe(), time.Now(), ingestgate.Options{
		GatewayBufferMaxAge: ingestgate.DefaultGatewayBufferMaxAge,
	})
}
