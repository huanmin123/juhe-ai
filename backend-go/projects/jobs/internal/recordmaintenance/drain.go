// Drainer 把 record_maintenance_jobs 交接表接到 retention.RunOnce 执行面。
// 节拍与结算对照 Node record-maintenance-queue.service.ts 本地队列 flush：
//   - flush 轮询间隔 recordMaintenanceFlushIntervalMs=100ms；
//   - 批次 recordMaintenanceBatchSize（JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_BATCH_SIZE，
//     默认 10），每个 tick 内整表排空（与 jobs 组合根既有 flushLoop 的
//     takeBatch 循环同形；Node 侧 flush 后 0 延迟重排，等效持续排空）；
//   - 失败结算按 T207 C1 毒丸修复替换 Node 的 head-of-line 停止分支
//     （fixedRetryPolicy('record_maintenance_queue_flush', 1000) 固定 1s 后
//     重试、失败行保留队头）：确定性失败（retention 层 normalize/校验拒绝
//     类别）置 status='failed' 隔离出队不再重试；瞬时失败按 attempt_count
//     指数退避（60s/300s/900s/3600s 封顶）保留 pending，FIFO 语义保持但
//     失败不再阻塞后续行；
//   - 停机 flushRecordMaintenanceQueueForShutdown：有界批次
//     （JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_SHUTDOWN_FLUSH_MAX_BATCHES，
//     默认 1）；剩余行持久化待下次启动。
package recordmaintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// drain 节拍与结算常量（Node record-maintenance-queue.service.ts 同值）。
const (
	// DefaultFlushIntervalMs 是 Node recordMaintenanceFlushIntervalMs。
	DefaultFlushIntervalMs = 100
	// DefaultBatchSize 是 Node recordMaintenanceBatchSize 默认值。
	DefaultBatchSize = 10
	// DefaultRetryDelay 是 Node fixedRetryPolicy 固定重试延迟（drain 循环级
	// 退避；行级失败退避见 queue.go retryBackoffDelaysMs）。
	DefaultRetryDelay = time.Second
	// DefaultShutdownFlushMaxBatches 是 Node
	// recordMaintenanceShutdownFlushMaxBatches 默认值。
	DefaultShutdownFlushMaxBatches = 1

	// runRoundTimeout 是单轮 drain 的执行上限（对照组合根 flushLoop 每批
	// 60s 的既有约定）。
	runRoundTimeout = 60 * time.Second
	// shutdownRoundTimeout 是停机排空每批上限（对照组合根 queue.drainShutdown
	// 每批 30s 的既有约定）。
	shutdownRoundTimeout = 30 * time.Second
)

// Runner 是 record-maintenance 执行面（retention.RecordMaintenanceRunner
// 的 RunOnce 签名；接口化供测试 Mock）。
type Runner interface {
	RunOnce(ctx context.Context, job retention.RecordMaintenanceJob) (map[string]any, error)
}

// SnapshotUpsertBatchRunner 是 Runner 的可选批量扩展：把连续
// account_usage_snapshot_upsert 任务段合并为一次 stats-writer 往返
// （Node record-maintenance-queue.service.ts:846-869 的 collect/process
// 合并语义；D5）。retention.RecordMaintenanceRunner 已实现；未实现该接口的
// Runner（测试 Mock）回落逐行执行。
type SnapshotUpsertBatchRunner interface {
	RunAccountUsageSnapshotUpserts(ctx context.Context, jobs []retention.RecordMaintenanceJob) (map[string]any, error)
}

// Drainer 轮询交接表并执行任务。
type Drainer struct {
	Store  *Store
	Runner Runner
	Logger *slog.Logger

	// BatchSize / FlushInterval / RetryDelay 为零值时取 Node 默认。
	BatchSize     int
	FlushInterval time.Duration
	RetryDelay    time.Duration
}

func (d *Drainer) batchSize() int {
	if d.BatchSize > 0 {
		return d.BatchSize
	}
	return DefaultBatchSize
}

func (d *Drainer) flushInterval() time.Duration {
	if d.FlushInterval > 0 {
		return d.FlushInterval
	}
	return DefaultFlushIntervalMs * time.Millisecond
}

func (d *Drainer) retryDelay() time.Duration {
	if d.RetryDelay > 0 {
		return d.RetryDelay
	}
	return DefaultRetryDelay
}

func (d *Drainer) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

// DrainOnce 排空当前到期任务：逐行 RunOnce（连续 account_usage_snapshot_upsert
// 段合并为一次批量执行）→ 成功按 id 删行；行级失败按确定性/瞬时分类结算后
// 继续处理后续行（T207 C1：FIFO 语义保持但失败不阻塞）。Dequeue 只回到期行，
// 被退避的行本轮不再出现、failed 行永不再出现，循环自然终止。返回本轮成功
// 执行并删除的行数。结算记账失败（退避/置终态写不进库）返回错误中止本轮：
// 行保持到期，若继续循环会同一行无限重试（活锁），交由调用方按退避重开。
func (d *Drainer) DrainOnce(ctx context.Context) (int, error) {
	processed := 0
	for {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		jobs, err := d.Store.Dequeue(ctx, d.batchSize())
		if err != nil {
			return processed, err
		}
		if len(jobs) == 0 {
			return processed, nil
		}
		settled, err := d.runBatch(ctx, jobs)
		processed += settled
		if err != nil {
			return processed, err
		}
	}
}

// runBatch 执行一个批次的任务，对照 Node flush 循环（record-maintenance-queue
// .service.ts:291-324）：遇到 account_usage_snapshot_upsert 时收集从当前下标
// 开始的连续任务段，一次批量执行、成功后整段删行（Node
// removeRecordMaintenanceJobsFromHead(snapshotJobs.length)）。T207 C1：毒丸行
// 在执行前预判隔离（确定性拒绝类别直接置 failed，不进执行器）；执行/删行失败
// 按瞬时退避结算后继续处理后续行，不再以 head-of-line 停止本轮。结算记账
// 失败上抛中止本轮。返回已执行并删除的行数。
func (d *Drainer) runBatch(ctx context.Context, jobs []retention.RecordMaintenanceJob) (int, error) {
	processed := 0
	for index := 0; index < len(jobs); {
		job := jobs[index]
		if job.Type == retention.JobTypeAccountUsageSnapshotUpsert {
			segment := collectAccountUsageSnapshotJobs(jobs, index)
			// 段内毒丸先行隔离（执行器 normalize fail-fast，任一毒丸行会让整
			// 段不执行；置 failed 后剩余合法行照常成段执行）。
			run, err := d.rejectSnapshotPoison(ctx, segment)
			if err != nil {
				return processed, err
			}
			if len(run) > 0 {
				if runErr := d.runSnapshotUpsertRun(ctx, run); runErr != nil {
					// 段首行按瞬时退避重试（Node 段首重试语义）；段内其余行
					// 保持 pending——快照行各自独立 upsert，后续行提前组成新
					// 段执行无害。
					if settleErr := d.settleTransientFailure(ctx, run[0], runErr); settleErr != nil {
						return processed, settleErr
					}
				} else {
					for _, item := range run {
						if err := d.Store.Delete(ctx, item.ID); err != nil {
							// 执行成功但删行失败：行保留会被重复执行（幂等），
							// 按瞬时失败退避。
							if settleErr := d.settleTransientFailure(ctx, item, err); settleErr != nil {
								return processed, settleErr
							}
							continue
						}
						processed++
					}
				}
			}
			index += len(segment)
			continue
		}
		if rejection := d.deterministicRejection(job); rejection != nil {
			if markErr := d.Store.MarkFailed(ctx, job.ID); markErr != nil {
				// 置终态失败按瞬时退避兜底；记账仍失败则中止本轮（行保持
				// 到期，交由调用方退避重开）。
				if settleErr := d.settleTransientFailure(ctx, job, fmt.Errorf("标记 failed 失败: %v（原始确定性拒绝: %w）", markErr, rejection)); settleErr != nil {
					return processed, settleErr
				}
			} else {
				d.logPoison(job, rejection)
			}
			index++
			continue
		}
		if _, err := d.Runner.RunOnce(ctx, job); err != nil {
			// 执行器层面的运行期失败（DB/上游/统计写入）按瞬时退避重试；
			// 确定性拒绝已在上方预判，不会进入执行器。
			if settleErr := d.settleTransientFailure(ctx, job, err); settleErr != nil {
				return processed, settleErr
			}
			index++
			continue
		}
		if err := d.Store.Delete(ctx, job.ID); err != nil {
			// 执行成功但删行失败：行保留会被重复执行（幂等），按瞬时失败退避。
			if settleErr := d.settleTransientFailure(ctx, job, err); settleErr != nil {
				return processed, settleErr
			}
			index++
			continue
		}
		processed++
		index++
	}
	return processed, nil
}

// deterministicRejection 判定 job 是否属于 retention 层的确定性拒绝类别
//（T207 C1）：normalize 失败（未知 type、非法参数——与执行器 RunOnce 首步
// 同一函数，重放必然重演）或快照行未知 kind（白名单与执行器校验同一集合，
// 见 retention.IsValidAccountUsageSnapshotKind）。返回非 nil 错误即确定性。
// 执行器层面的运行期失败（DB/上游/统计写入）不在此类别，走退避重试。
func (d *Drainer) deterministicRejection(job retention.RecordMaintenanceJob) error {
	if _, err := retention.NormalizeRecordMaintenanceJob(job, time.Now()); err != nil {
		return err
	}
	if job.Type == retention.JobTypeAccountUsageSnapshotUpsert && !retention.IsValidAccountUsageSnapshotKind(job.Kind) {
		return fmt.Errorf("未知账号用量快照 kind：%s", job.Kind)
	}
	return nil
}

// rejectSnapshotPoison 把任务段内的确定性拒绝行置 failed 隔离并返回剩余合法
// 行（T207 C1）。置终态失败按瞬时退避兜底，记账失败上抛（中止本轮）。
func (d *Drainer) rejectSnapshotPoison(ctx context.Context, run []retention.RecordMaintenanceJob) ([]retention.RecordMaintenanceJob, error) {
	remaining := make([]retention.RecordMaintenanceJob, 0, len(run))
	for _, item := range run {
		rejection := d.deterministicRejection(item)
		if rejection == nil {
			remaining = append(remaining, item)
			continue
		}
		if markErr := d.Store.MarkFailed(ctx, item.ID); markErr != nil {
			if settleErr := d.settleTransientFailure(ctx, item, fmt.Errorf("标记 failed 失败: %v（原始确定性拒绝: %w）", markErr, rejection)); settleErr != nil {
				return nil, settleErr
			}
			continue
		}
		d.logPoison(item, rejection)
	}
	return remaining, nil
}

// settleTransientFailure 结算瞬时失败（执行失败、删行失败等）：按指数退避
// 保留 pending，行稍后重入（重复执行幂等）。退避记账失败时返回错误（调用方
// 中止本轮）：行保持到期，继续循环会对同一行无限重试。
func (d *Drainer) settleTransientFailure(ctx context.Context, job retention.RecordMaintenanceJob, err error) error {
	if backoffErr := d.Store.RetryWithBackoff(ctx, job.ID); backoffErr != nil {
		d.logger().Error("数据维护任务退避记账失败，本轮中止等待重开",
			"event", "record_maintenance_queue_settle_failed",
			"jobId", job.ID, "error", backoffErr)
		return fmt.Errorf("数据维护任务 %s 退避记账失败: %w", job.ID, backoffErr)
	}
	d.logFlushFailure(job, err)
	return nil
}

// DrainShutdown 停机排空：最多 maxBatches 个批次；失败行按确定性/瞬时分类
// 结算（同 DrainOnce），剩余行持久化待下次启动消费。返回成功执行的行数。
func (d *Drainer) DrainShutdown(maxBatches int) int {
	if maxBatches < 1 {
		maxBatches = DefaultShutdownFlushMaxBatches
	}
	processed := 0
	for batch := 0; batch < maxBatches; batch++ {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownRoundTimeout)
		jobs, err := d.Store.Dequeue(ctx, d.batchSize())
		if err != nil {
			d.logger().Warn("停机数据维护队列读取失败",
				"event", "record_maintenance_queue_shutdown_dequeue_failed", "error", err)
			cancel()
			return processed
		}
		if len(jobs) == 0 {
			cancel()
			return processed
		}
		settled, runErr := d.runBatch(ctx, jobs)
		processed += settled
		if runErr != nil {
			cancel()
			return processed
		}
		cancel()
	}
	return processed
}

// Run 是 drain 循环（组合根 supervisor 关闭面）：100ms 节拍排空；Dequeue/记
// 账层错误按固定 1s 退避后再进入下一轮（行级失败由结算路径消化，不再触发
// 循环退避）；stop 关闭后退出（停机排空由组合根在 closer 中另行调用
// DrainShutdown）。
func (d *Drainer) Run(stop <-chan struct{}) {
	ticker := time.NewTicker(d.flushInterval())
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), runRoundTimeout)
			_, err := d.DrainOnce(ctx)
			cancel()
			if err != nil {
				timer := time.NewTimer(d.retryDelay())
				select {
				case <-stop:
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}
	}
}

// collectAccountUsageSnapshotJobs mirrors collectAccountUsageSnapshotJobs
// （record-maintenance-queue.service.ts:846-857）：从 startIndex 开始的连续
// account_usage_snapshot_upsert 任务段。
func collectAccountUsageSnapshotJobs(jobs []retention.RecordMaintenanceJob, startIndex int) []retention.RecordMaintenanceJob {
	run := make([]retention.RecordMaintenanceJob, 0, len(jobs)-startIndex)
	for index := startIndex; index < len(jobs); index++ {
		if jobs[index].Type != retention.JobTypeAccountUsageSnapshotUpsert {
			break
		}
		run = append(run, jobs[index])
	}
	return run
}

// runSnapshotUpsertRun 执行一个快照任务段：Runner 实现批量扩展时一次往返
// 写入整段；否则逐行执行保持既有 Mock 行为。
func (d *Drainer) runSnapshotUpsertRun(ctx context.Context, run []retention.RecordMaintenanceJob) error {
	if batchRunner, ok := d.Runner.(SnapshotUpsertBatchRunner); ok {
		_, err := batchRunner.RunAccountUsageSnapshotUpserts(ctx, run)
		return err
	}
	for _, job := range run {
		if _, err := d.Runner.RunOnce(ctx, job); err != nil {
			return err
		}
	}
	return nil
}

// logFlushFailure 记录瞬时失败（行已按指数退避保留 pending）。
func (d *Drainer) logFlushFailure(job retention.RecordMaintenanceJob, err error) {
	d.logger().Error("数据维护队列执行失败，已按退避保留任务等待重试",
		"event", "record_maintenance_queue_flush_failed",
		"jobType", job.Type, "jobId", job.ID, "error", err)
}

// logPoison 记录确定性失败（行已置 failed 终态隔离出队），携带原始载荷摘要
// 供排查投递端缺陷（快照载荷为用量响应头计数字段，不含凭据）。
func (d *Drainer) logPoison(job retention.RecordMaintenanceJob, rejection error) {
	d.logger().Error("数据维护任务确定性失败，已置 failed 隔离出队不再重试",
		"event", "record_maintenance_queue_job_poison",
		"jobType", job.Type, "jobId", job.ID,
		"kind", job.Kind, "accountId", job.AccountID,
		"payloadSummary", snapshotPayloadSummary(job),
		"error", rejection)
}

// snapshotPayloadSummary 返回快照载荷的截断 JSON 摘要（清理行无载荷时为空）。
func snapshotPayloadSummary(job retention.RecordMaintenanceJob) string {
	if job.Snapshot == nil {
		return ""
	}
	encoded, err := json.Marshal(job.Snapshot)
	if err != nil {
		return fmt.Sprintf("不可序列化: %v", err)
	}
	return truncateText(string(encoded), 200)
}
