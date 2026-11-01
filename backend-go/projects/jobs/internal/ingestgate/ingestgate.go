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
// T93-6 扩展（本地裁决）：gateway 进程内 buffer（spool 落盘之前）在 jobs 边界
// 之外不可观测，记录可能在该段滞留后迟到入表；默认安全截止时间因此再与
// now-DefaultGatewayBufferMaxAge（5min）取较早者，使游标不越过任何可能仍在
// buffer 的记录。代价是常规聚合延迟从 15s 放宽到 5min（保守下界），换来迟到
// 记录不再永久漏聚合；statsverify 另按 usage_records 重算最近窗口做对账。
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

// DefaultGatewayBufferMaxAge 是 gateway 进程内用量记录 buffer 的最长滞留估计
// （T93-6 / C2-H1）：记录在 gateway → spool 文件 → writer 队列 → usage_records
// 的单向迁移中，进程内 buffer 是 spool 落盘之前唯一不可观测的一段（jobs 与
// gateway 分进程）。任何 created_at 晚于 now-DefaultGatewayBufferMaxAge 的记录
// 都可能仍在 buffer 里，游标越过它就会在记录迟到入表后永久漏聚合；因此安全
// 截止时间被压到 now-5min，而不是外部可观测积压水位。
//
// 5 分钟是保守下界（gateway 实际 flush 远快于此），对应网关侧 spool 写入失败
// 重试与进程内批量刷盘的常见滞留上限。可经 Options.GatewayBufferMaxAge 覆盖
// （组合根注入点，见 jobs cmd 的 ingestgate 装配）；未放进 system_settings 是
// 因为它不是业务调参项，而是跨进程边界的保守安全余量，不应随运营改值放宽。
const DefaultGatewayBufferMaxAge = 5 * time.Minute

// Options 是可配置的安全水位参数（零值即默认）。
type Options struct {
	// GatewayBufferMaxAge 覆盖 gateway 进程内 buffer 的最大滞留估计；<=0 时
	// 取 DefaultGatewayBufferMaxAge。
	GatewayBufferMaxAge time.Duration
}

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
	// SnapshotUsageRecordQueueDeadLetterCount 是 Go writer 的死信终态计数
	// （C2-H2）：重试耗尽的批次已从队列移除、记录不再出现在任何水位里，
	// 游标若继续推进会永久越过这些丢失记录。>0 时 Check 失败本轮（可观测
	// 的降级），而不是静默推进统计游标；writer 恢复/重启后计数归零即解除。
	SnapshotUsageRecordQueueDeadLetterCount int
	// SnapshotUsageRecordQueueDroppedCount 是 writer 运行态的累计丢弃计数
	// （溢出落盘失败 / oversize / 死信合计，C2-H2 观测面）。只进载荷与日志，
	// 不参与 Check 的硬门控：oversize 等终态丢弃不可恢复，按累计值硬阻断
	// 会让统计聚合永久停摆；死信由 DeadLetterCount 单独阻断。
	SnapshotUsageRecordQueueDroppedCount int
	// PendingUsageRecordsOldestCreatedAt mirrors
	// status.pendingQueues.usageRecords.oldestCreatedAt。Go 单进程承载为
	// spool 文件段：宿主注入 drain（gateway 交接表 / writer 溢出 spool）仍
	// 持有的未确认文件队头记录 created_at，覆盖"记录滞留超过安全水位后才
	// 入表"的积压——该段此前不进水位，游标会越过其 created_at 造成永久
	// 不聚合。无积压时为空。
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
// 成功返回游标安全截止时间。零值 Options 即生产默认（gateway buffer 5min）。
func Check(ctx context.Context, probe Probe, now time.Time) (Safety, error) {
	return CheckWithOptions(ctx, probe, now, Options{})
}

// CheckWithOptions 是 Check 的可配置形态（T93-6a）。
func CheckWithOptions(ctx context.Context, probe Probe, now time.Time, options Options) (Safety, error) {
	if probe == nil {
		return Safety{}, fmt.Errorf("ingest-worker 使用记录队列快照不可用，本轮跳过统计聚合，避免统计游标越过排队记录")
	}
	status, err := probe(ctx)
	if err != nil || status == nil || !status.Ready {
		return Safety{}, fmt.Errorf("ingest-worker 使用记录队列快照不可用，本轮跳过统计聚合，避免统计游标越过排队记录")
	}
	flushFailureCount := status.SnapshotUsageRecordQueueFlushFailureCount
	defaultSafeCreatedBefore := conservativeSafeCreatedBefore(now, options.GatewayBufferMaxAge)
	oldestPendingCreatedAt, err := oldestPendingUsageRecordCreatedAt(status)
	if err != nil {
		return Safety{}, err
	}
	if flushFailureCount > 0 && oldestPendingCreatedAt != "" {
		return Safety{}, fmt.Errorf("使用记录 ingest 队列已有 %d 次写入失败且仍有待处理记录，本轮跳过统计聚合，等待写入队列恢复", flushFailureCount)
	}
	// C2-H2：死信批已从 writer 队列移除，记录不会再出现在任何积压水位中；
	// 若继续按 safeCreatedBefore 推进游标，这些记录即使日后补写也永久不会
	// 被聚合。死信 >0 时失败本轮（可观测降级），writer 恢复后计数归零即
	// 自动解除。
	if deadLetterCount := status.SnapshotUsageRecordQueueDeadLetterCount; deadLetterCount > 0 {
		return Safety{}, fmt.Errorf("使用记录 ingest 队列已有 %d 条死信记录（重试耗尽仍未落库），本轮跳过统计聚合，避免统计游标越过丢失记录", deadLetterCount)
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

// conservativeSafeCreatedBefore 返回默认安全截止时间：Node 的 now-15s 与
// gateway 进程内 buffer 的 now-GatewayBufferMaxAge 取较早者。缓冲段不可观测，
// 只能按最大滞留做保守压后；两者都是规范 UTC 毫秒串，逐字节比较即时间比较。
func conservativeSafeCreatedBefore(now time.Time, gatewayBufferMaxAge time.Duration) string {
	if gatewayBufferMaxAge <= 0 {
		gatewayBufferMaxAge = DefaultGatewayBufferMaxAge
	}
	defaultSafeCreatedBefore := statsagg.FormatRFC3339Millis(now.Add(-UsageStatsCursorSafetyDelaySeconds * time.Second))
	bufferSafeCreatedBefore := statsagg.FormatRFC3339Millis(now.Add(-gatewayBufferMaxAge))
	if bufferSafeCreatedBefore < defaultSafeCreatedBefore {
		return bufferSafeCreatedBefore
	}
	return defaultSafeCreatedBefore
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
	// 说明：两侧均经 normalizeIsoTime（RequiredRFC3339Instant）规范化，输出
	// 必可再解析，原 RFC3339Milliseconds !ok 错误臂为死代码，按覆盖率清理授权删除。
	leftMs, _ := statsagg.RFC3339Milliseconds(normalizedLeft)
	rightMs, _ := statsagg.RFC3339Milliseconds(normalizedRight)
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
	// 说明：oldest 经 normalizeIsoTime、default 经 RequiredRFC3339Instant 规范
	// 后必可再解析，原 RFC3339Milliseconds !ok 错误臂为死代码，按覆盖率清理授权删除。
	oldestMs, _ := statsagg.RFC3339Milliseconds(normalizedOldest)
	defaultMs, _ := statsagg.RFC3339Milliseconds(normalizedDefault)
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
