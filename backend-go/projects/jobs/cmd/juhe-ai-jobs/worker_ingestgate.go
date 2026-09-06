package main

// worker_ingestgate.go 组合根的 ingest 排干门控装配（T6a）：
// Node usageStatsAggregationSafety 经 ingest-worker IPC 获取队列快照
// （background_worker_ingest_status_request，按 Go 总设计消灭）；Go 单进程内
// 直接读 usagewriter.Runtime() 构造 ingestgate.Probe，供
// usage-stats-aggregation / client-ip-stats-aggregation / account-quality-refresh
// 三个消费 usage_records 游标的任务前置门控。

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

// ingestDrainProbe 把 usagewriter 运行态适配为 ingestgate.Probe。
// writer 未装配（UsageWriterEnabled=false）时返回 (nil, nil)：等价 Node
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
			// Node pendingQueues.usageRecords envelope 随 IPC 路径消灭，
			// 单进程只有 snapshot 一份队列状态。
			SnapshotUsageRecordQueueOldestCreatedAt:   runtime.OldestCreatedAt,
			SnapshotUsageRecordQueueFlushFailureCount: runtime.FlushFailureCount,
			// Redis Stream 分支随 queueDriver=redis_stream 消灭（Go 直写分片）。
		}, nil
	}
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
