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
// 回退游标。gateway 进程内 buffer（spool 落盘之前的一段）在本进程边界之外、
// 无法观测，与 Node IPC 边界同款，为已登记的残余窗口。

import (
	"context"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/ingestgate"
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
		runtime := writer.Runtime()
		return &ingestgate.DrainStatus{
			// Node ready：ingest worker 就绪；Go 进程内 writer 存在即就绪。
			Ready: true,
			// Node snapshot.usageRecordQueue：writer 内存队列段。
			SnapshotUsageRecordQueueOldestCreatedAt:   runtime.OldestCreatedAt,
			SnapshotUsageRecordQueueFlushFailureCount: runtime.FlushFailureCount,
			// Node pendingQueues.usageRecords envelope：Go 单进程承载为
			// spool 文件段——gateway 交接 drain 与 writer 溢出回放 drain
			// 各自仍持有（未确认删除）的队头记录 created_at，取最旧。
			PendingUsageRecordsOldestCreatedAt: a.spoolPendingOldestCreatedAt(),
			// Redis Stream 分支随 queueDriver=redis_stream 消灭（Go 直写分片）。
		}, nil
	}
}

// spoolPendingOldestCreatedAt 汇总两个 drain 的待删水位，返回最旧一条；
// 两者皆无积压返回空串。时间值为 usagewriter 归一化后的 RFC3339 毫秒
// UTC，解析失败按不可知处理（返回另一值或空串，由门控按默认水位放行）。
func (a *workerAssembly) spoolPendingOldestCreatedAt() string {
	var candidates []string
	if a.usageSpoolDrain != nil {
		if value := a.usageSpoolDrain.OldestPendingCreatedAt(); value != "" {
			candidates = append(candidates, value)
		}
	}
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
// aggregate_usage_stats）。
func (a *workerAssembly) ingestDrainSafety(ctx context.Context) (ingestgate.Safety, error) {
	return ingestgate.Check(ctx, a.ingestDrainProbe(), time.Now())
}
