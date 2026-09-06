// Package ingestgate 移植 Node background-jobs.ts 的 ingest 排干门控
// （usageStatsAggregationSafety / ensureUsageRecordsSafeForStatsAggregation，
// background-jobs.ts:630-661）：在统计聚合 / 账户质量刷新消费 usage_records
// 游标前，确认 ingest 队列快照可用且没有会把统计游标推过排队记录的积压。
//
// Node 契约（background-jobs.ts:631-661 逐条对应）：
//   - requestIngestWorkerDrainStatus(6000) 不可用（undefined / !ready /
//     无 snapshot）→ 抛错跳过本轮统计；
//   - flushFailureCount > 0 且仍有待处理记录 → 抛错等待写入队列恢复；
//   - safeCreatedBefore = 默认 now-15s（usageStatsCursorSafetyDelaySeconds，
//     usage-stats.repository.ts:146）；存在待处理积压时取
//     min(默认, 最旧排队记录 - 1ms)。
//
// Go 形态差异（登记，不静默）：Node 的 ingest-worker 是独立进程、状态经 IPC
// 获取（background_worker_ingest_status_request，按 Go 总设计消灭）；Go 单进程
// 内由宿主直接读 usagewriter.Runtime() 注入 Probe。pendingQueues envelope 与
// Redis Stream 分支（queueDriver=redis_stream）随 IPC/Stream 路径一起消灭，
// DrainStatus 保留对应字段以便宿主在多源积压时聚合。
package ingestgate

import (
	"context"
	"fmt"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// UsageStatsCursorSafetyDelaySeconds mirrors usageStatsCursorSafetyDelaySeconds
// (usage-stats.repository.ts:146).
const UsageStatsCursorSafetyDelaySeconds = 15

// DrainStatus 携带宿主探测到的 ingest 排干状态，字段对照
// BackgroundWorkerIngestDrainStatus 中门控读取的子集。
type DrainStatus struct {
	// Ready mirrors status.ready（ingest worker 就绪；Go 单进程 writer 存在即就绪）。
	Ready bool
	// SnapshotUsageRecordQueueOldestCreatedAt mirrors
	// status.snapshot.usageRecordQueue.oldestCreatedAt。
	SnapshotUsageRecordQueueOldestCreatedAt string
	// SnapshotUsageRecordQueueFlushFailureCount mirrors
	// status.snapshot.usageRecordQueue.flushFailureCount（缺失按 0）。
	SnapshotUsageRecordQueueFlushFailureCount int
	// PendingUsageRecordsOldestCreatedAt mirrors
	// status.pendingQueues.usageRecords.oldestCreatedAt（Go 单进程无 IPC
	// envelope，通常为空）。
	PendingUsageRecordsOldestCreatedAt string
	// RedisStreamOldestCreatedAt mirrors
	// getUsageRecordRedisStreamOldestCreatedAt()（queueDriver != redis_stream
	// 时为空；Go Stream 分派已按总设计消灭）。
	RedisStreamOldestCreatedAt string
}

// Probe mirrors requestIngestWorkerDrainStatus(6000)：返回 (nil, nil) 表示
// 快照不可用（Node undefined），非 nil error 同样按不可用处理（Node 仅在
// 超时/IPC 断裂时 resolve undefined，Go 侧宿主错误等价跳过本轮）。
type Probe func(ctx context.Context) (*DrainStatus, error)

// Safety mirrors UsageStatsAggregationSafety（background-jobs.ts:85-88）。
type Safety struct {
	// SafeCreatedBefore 是本轮统计聚合的游标安全截止时间（RFC3339 毫秒）。
	SafeCreatedBefore string
}

// Check mirrors usageStatsAggregationSafety（background-jobs.ts:630-661）：
// 门控失败返回错误（调用方使本轮任务失败即 Node 的"跳过本轮统计聚合"），
// 成功返回游标安全截止时间。
func Check(ctx context.Context, probe Probe, now time.Time) (Safety, error) {
	if probe == nil {
		return Safety{}, fmt.Errorf("ingest-worker 使用记录队列快照不可用，本轮跳过统计聚合，避免统计游标越过排队记录")
	}
	status, err := probe(ctx)
	if err != nil || status == nil || !status.Ready {
		return Safety{}, fmt.Errorf("ingest-worker 使用记录队列快照不可用，本轮跳过统计聚合，避免统计游标越过排队记录")
	}
	flushFailureCount := status.SnapshotUsageRecordQueueFlushFailureCount
	defaultSafeCreatedBefore := statsagg.FormatRFC3339Millis(now.Add(-UsageStatsCursorSafetyDelaySeconds * time.Second))
	oldestPendingCreatedAt, err := oldestPendingUsageRecordCreatedAt(status)
	if err != nil {
		return Safety{}, err
	}
	if flushFailureCount > 0 && oldestPendingCreatedAt != "" {
		return Safety{}, fmt.Errorf("使用记录 ingest 队列已有 %d 次写入失败且仍有待处理记录，本轮跳过统计聚合，等待写入队列恢复", flushFailureCount)
	}
	safeCreatedBefore, err := safeCreatedBeforeForPendingBacklog(defaultSafeCreatedBefore, oldestPendingCreatedAt)
	if err != nil {
		return Safety{}, err
	}
	return Safety{SafeCreatedBefore: safeCreatedBefore}, nil
}

// Gate 把 Check 包装成 Node ensureUsageRecordsSafeForStatsAggregation 的
// () => Promise<void> 形态：门控只负责失败本轮，safeCreatedBefore 由需要
// 的调用方（usage-stats-aggregation）单独取用。
func Gate(probe Probe, now func() time.Time) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		_, err := Check(ctx, probe, now())
		return err
	}
}

// oldestPendingUsageRecordCreatedAt mirrors oldestPendingUsageRecordCreatedAt +
// oldestRedisStreamUsageRecordCreatedAtForStatsAggregation：三个来源取最旧
// （pendingQueues → snapshot → redis stream），空值跳过，非法时间戳报错。
func oldestPendingUsageRecordCreatedAt(status *DrainStatus) (string, error) {
	oldest, err := oldestIso(status.PendingUsageRecordsOldestCreatedAt, status.SnapshotUsageRecordQueueOldestCreatedAt)
	if err != nil {
		return "", err
	}
	return oldestIso(oldest, status.RedisStreamOldestCreatedAt)
}

// oldestIso mirrors oldestIso（background-jobs.ts:658-667）：规范化后按毫秒
// 比较取较早者；任一输入非法时报 Node 同款错误。
func oldestIso(left, right string) (string, error) {
	normalizedLeft, err := normalizeIsoTime(left)
	if err != nil {
		return "", err
	}
	normalizedRight, err := normalizeIsoTime(right)
	if err != nil {
		return "", err
	}
	if normalizedLeft == "" {
		return normalizedRight, nil
	}
	if normalizedRight == "" {
		return normalizedLeft, nil
	}
	leftMs, ok := statsagg.RFC3339Milliseconds(normalizedLeft)
	if !ok {
		return "", fmt.Errorf("使用记录 oldestCreatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	rightMs, ok := statsagg.RFC3339Milliseconds(normalizedRight)
	if !ok {
		return "", fmt.Errorf("使用记录 oldestCreatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	if leftMs <= rightMs {
		return normalizedLeft, nil
	}
	return normalizedRight, nil
}

// safeCreatedBeforeForPendingBacklog mirrors
// usageStatsSafeCreatedBeforeForPendingBacklog（background-jobs.ts:669-684）：
// 无积压用默认；积压早于等于默认时回退到最旧记录前 1ms（Node
// Math.max(0, oldest - 1)）。defaultSafeCreatedBefore 由 Check 内部生成、
// 必为合法 RFC3339 毫秒；oldest 非法时按 Node 同款校验报错由调用方处理。
func safeCreatedBeforeForPendingBacklog(defaultSafeCreatedBefore, oldestPendingCreatedAt string) (string, error) {
	normalizedDefault, err := statsagg.RequiredRFC3339Instant(defaultSafeCreatedBefore, "统计安全截止时间")
	if err != nil {
		return "", err
	}
	normalizedOldest, err := normalizeIsoTime(oldestPendingCreatedAt)
	if err != nil {
		return "", err
	}
	if normalizedOldest == "" {
		return normalizedDefault, nil
	}
	oldestMs, ok := statsagg.RFC3339Milliseconds(normalizedOldest)
	if !ok {
		return "", fmt.Errorf("统计安全截止时间必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	defaultMs, ok := statsagg.RFC3339Milliseconds(normalizedDefault)
	if !ok {
		return "", fmt.Errorf("统计安全截止时间必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	if oldestMs > defaultMs {
		return normalizedDefault, nil
	}
	backlogMs := oldestMs - 1
	if backlogMs < 0 {
		backlogMs = 0
	}
	return statsagg.FormatRFC3339Millis(time.UnixMilli(backlogMs).UTC()), nil
}

// normalizeIsoTime mirrors normalizeIsoTime（background-jobs.ts:686-690）：
// 空值透传为空，其余经 requiredRfc3339Instant 规范化。
func normalizeIsoTime(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return statsagg.RequiredRFC3339Instant(value, "使用记录 createdAt")
}
