package accountquality

import (
	"context"
	"errors"
)

// RefreshRunner 承载 scheduled 任务 account-quality-refresh
// （Node runAccountQualityRefresh：统计刷新 + 失败前置确认候选入队）。
type RefreshRunner struct {
	store         *StatsStore
	settings      SettingsNumber
	logger        Logger
	caches        RuntimeCacheInvalidator
	precheck      *PrecheckRunner
	precheckBatch int
	offset        int
	concurrency   QueueConcurrency
	ingest        IngestDrainGate
}

// RefreshDeps 组装 runner。
type RefreshDeps struct {
	Store       *StatsStore
	Settings    SettingsNumber
	Logger      Logger
	Caches      RuntimeCacheInvalidator
	Precheck    *PrecheckRunner
	Concurrency QueueConcurrency
	// PrecheckBatchSize 为空取默认 10（Node runtimeConfig 默认）。
	PrecheckBatchSize int
	// IngestGate 对应 Node deps.ensureUsageRecordsIngestedBeforeStatsAggregation
	// （account-probe-jobs.ts:46，必填依赖）：统计刷新前排干门控，未排干时
	// 返回错误跳过本轮，避免统计游标越过排队记录。nil 在 Run 时报错
	// （Node 依赖缺失等价任务失败，不静默降级）。组合根用 internal/ingestgate
	// 构造；测试注入桩。
	IngestGate IngestDrainGate
}

// NewRefreshRunner 构建 runner。offset 状态在进程内保持（与 Node 模块级
// accountQualityFailurePrecheckOffset 一致）。
func NewRefreshRunner(deps RefreshDeps) *RefreshRunner {
	logger := deps.Logger
	if logger == nil {
		logger = NopLogger{}
	}
	batch := deps.PrecheckBatchSize
	if batch == 0 {
		batch = DefaultPrecheckBatchSize
	}
	return &RefreshRunner{
		store:         deps.Store,
		settings:      deps.Settings,
		logger:        logger,
		caches:        deps.Caches,
		precheck:      deps.Precheck,
		precheckBatch: batch,
		concurrency:   deps.Concurrency,
		ingest:        deps.IngestGate,
	}
}

// Run 是 runAccountQualityRefresh 的移植：
// 0) ingest 排干门控（Node ensureUsageRecordsIngestedBeforeStatsAggregation）
// 1) 读取 accountQualityWindowMinutes
// 2) 刷新质量统计并按 offset 拉取失败候选
// 3) offset 回绕规则：候选数 < 批大小时归零，否则前移
// 4) 候选逐个入队（去重跳过）
// 5) 有刷新/删除/候选时清网关运行时缓存并打点 info 日志
func (r *RefreshRunner) Run(ctx context.Context) error {
	if r.ingest == nil {
		return errors.New("account-quality-refresh 未注入 ingest 排干门控（Node ensureUsageRecordsIngestedBeforeStatsAggregation 必填依赖）")
	}
	if err := r.ingest.EnsureUsageRecordsIngested(ctx); err != nil {
		return err
	}
	windowMinutes := bounded(r.settings("accountQualityWindowMinutes", AccountQualityWindowMinMinutes, AccountQualityWindowMaxMinutes), AccountQualityWindowMinMinutes, AccountQualityWindowMaxMinutes)
	result, err := r.store.RefreshFromUsage(ctx, RefreshInput{
		WindowMinutes: windowMinutes,
		DirtyLimit:    DirtyAccountBatchLimit,
	})
	if err != nil {
		r.logger.Error("background_account_quality_refresh_failed", map[string]any{
			"error": err.Error(),
		}, "账户质量缓存刷新失败")
		return err
	}
	candidates, err := r.store.ListFailurePrecheckCandidates(ctx, bounded(r.precheckBatch, 1, PrecheckCandidateLimitMax), bounded(r.offset, 0, PrecheckOffsetMax))
	if err != nil {
		r.logger.Error("background_account_quality_refresh_failed", map[string]any{
			"error": err.Error(),
		}, "账户质量缓存刷新失败")
		return err
	}
	// Node：failureCandidates.length < batchSize ? 0 : offset + length
	if len(candidates) < r.precheckBatch {
		r.offset = 0
	} else {
		r.offset += len(candidates)
	}

	queueConcurrency := defaultConcurrency(r.concurrency)
	enqueuedCount := 0
	skippedQueuedCount := 0
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.precheck.Enqueue(candidate) {
			enqueuedCount++
		} else {
			skippedQueuedCount++
		}
	}
	if result.Refreshed > 0 || result.Removed > 0 || len(candidates) > 0 {
		if r.caches != nil {
			r.caches.ClearGatewayRuntimeCache(ctx)
		}
		queue := r.precheck.Snapshot()
		fields := map[string]any{
			"realtimeRefreshed":                 result.Refreshed,
			"realtimeRemoved":                   result.Removed,
			"failureCandidateCount":             len(candidates),
			"failurePrecheckEnqueuedCount":      enqueuedCount,
			"failurePrecheckSkippedQueuedCount": skippedQueuedCount,
			"failurePrecheckQueueConcurrency":   queueConcurrency,
			"failurePrecheckQueuePendingCount":  queue.PendingCount,
			"failurePrecheckQueueRunningCount":  queue.RunningCount,
		}
		if queue.NextRunAt != "" {
			fields["failurePrecheckQueueNextRunAt"] = queue.NextRunAt
		}
		r.logger.Info("background_account_quality_refresh_completed", fields, "账户质量缓存刷新完成")
	}
	return nil
}

func bounded(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func defaultConcurrency(fn QueueConcurrency) int {
	if fn == nil {
		return 0
	}
	return fn()
}
