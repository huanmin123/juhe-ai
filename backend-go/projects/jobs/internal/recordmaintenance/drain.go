// Drainer 把 record_maintenance_jobs 交接表接到 retention.RunOnce 执行面。
// 节拍与结算对照 Node record-maintenance-queue.service.ts 本地队列 flush：
//   - flush 轮询间隔 recordMaintenanceFlushIntervalMs=100ms；
//   - 批次 recordMaintenanceBatchSize（JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_BATCH_SIZE，
//     默认 10），每个 tick 内整表排空（与 jobs 组合根既有 flushLoop 的
//     takeBatch 循环同形；Node 侧 flush 后 0 延迟重排，等效持续排空）；
//   - 失败退避 fixedRetryPolicy('record_maintenance_queue_flush', 1000)
//     固定 1s 后重试，失败行保留队头（head-of-line，Node 同语义）；
//   - 停机 flushRecordMaintenanceQueueForShutdown：有界批次
//     （JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_SHUTDOWN_FLUSH_MAX_BATCHES，
//     默认 1）+ retryOnFailure=false（失败即停，剩余行持久化待下次启动）。
package recordmaintenance

import (
	"context"
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
	// DefaultRetryDelay 是 Node fixedRetryPolicy 固定重试延迟。
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

// DrainOnce 排空当前表内任务：逐行 RunOnce → 成功按 id 删行；任一行失败
// 记录 record_maintenance_queue_flush_failed 并中止本轮（该行与后续行保留，
// 由调用方按固定退避重试）。返回本轮成功执行并删除的行数。
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
		for _, job := range jobs {
			if _, err := d.Runner.RunOnce(ctx, job); err != nil {
				d.logFlushFailure(job, err)
				return processed, err
			}
			if err := d.Store.Delete(ctx, job.ID); err != nil {
				// 执行成功但删行失败：行保留会被重复执行（幂等），按失败
				// 退避重试。
				d.logFlushFailure(job, err)
				return processed, err
			}
			processed++
		}
	}
}

// DrainShutdown 停机排空：最多 maxBatches 个批次、retryOnFailure=false
// （失败即停，剩余行持久化待下次启动消费）。返回成功执行的行数。
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
		for _, job := range jobs {
			if _, err := d.Runner.RunOnce(ctx, job); err != nil {
				d.logFlushFailure(job, err)
				cancel()
				return processed
			}
			if err := d.Store.Delete(ctx, job.ID); err != nil {
				d.logFlushFailure(job, err)
				cancel()
				return processed
			}
			processed++
		}
		cancel()
	}
	return processed
}

// Run 是 drain 循环（组合根 supervisor 关闭面）：100ms 节拍排空；失败按
// 固定 1s 退避后再进入下一轮；stop 关闭后退出（停机排空由组合根在 closer
// 中另行调用 DrainShutdown）。
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

func (d *Drainer) logFlushFailure(job retention.RecordMaintenanceJob, err error) {
	d.logger().Error("数据维护队列执行失败，已保留任务等待重试",
		"event", "record_maintenance_queue_flush_failed",
		"jobType", job.Type, "jobId", job.ID, "error", err)
}
