package runtimelogretention

import (
	"context"
	"fmt"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	DefaultRetentionDays              = 14
	MinRetentionDays                  = 1
	MaxRetentionDays                  = 90
	DefaultBatchSize                  = 1000
	MaxBatchSize                      = 1000
	DefaultMaxBatches                 = 20
	MaxMaxBatches                     = 100
	DefaultBatchPause                 = 25 * time.Millisecond
	CleanupPhaseRuntimeLogs           = "runtime_logs"
	CleanupPhaseRuntimeLogFileCursors = "runtime_log_file_cursors"
	CleanupPhaseComplete              = "complete"
)

const runtimeLogRetentionISOLayout = "2006-01-02T15:04:05.000Z"

type Service struct {
	store      port.RuntimeLogRetentionCleaner
	batchPause time.Duration
	sleep      func(context.Context, time.Duration) error
}

type ServiceOptions struct {
	Store      port.RuntimeLogRetentionCleaner
	BatchPause time.Duration
	Sleep      func(context.Context, time.Duration) error
}

type CleanupInput struct {
	IndexEnabled  bool
	Now           time.Time
	RetentionDays int
	BatchSize     int
	MaxBatches    int
}

type CleanupResult struct {
	IndexEnabled                bool
	Phase                       string
	Partial                     bool
	RetentionDays               int
	BatchSize                   int
	MaxBatches                  int
	CutoffISO                   string
	RuntimeLogs                 int64
	RuntimeLogBatches           int
	RuntimeLogFileCursors       int64
	RuntimeLogFileCursorBatches int
}

func NewService(store port.RuntimeLogRetentionCleaner) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	batchPause := opts.BatchPause
	if batchPause == 0 {
		batchPause = DefaultBatchPause
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	return &Service{store: opts.Store, batchPause: batchPause, sleep: sleep}
}

func (s *Service) Cleanup(ctx context.Context, input CleanupInput) (CleanupResult, error) {
	if !input.IndexEnabled {
		return CleanupResult{IndexEnabled: false}, nil
	}
	if s.store == nil {
		return CleanupResult{}, fmt.Errorf("运行日志索引保留清理存储不能为空")
	}
	if err := ctx.Err(); err != nil {
		return CleanupResult{}, err
	}

	retentionDays, err := s.retentionDays(ctx, input.RetentionDays)
	if err != nil {
		return CleanupResult{}, err
	}
	batchSize, err := normalizeBatchSize(input.BatchSize)
	if err != nil {
		return CleanupResult{}, err
	}
	maxBatches, err := normalizeMaxBatches(input.MaxBatches)
	if err != nil {
		return CleanupResult{}, err
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	cutoffISO := now.UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour).Truncate(time.Millisecond).Format(runtimeLogRetentionISOLayout)
	result := CleanupResult{
		IndexEnabled:  true,
		Phase:         CleanupPhaseRuntimeLogs,
		RetentionDays: retentionDays,
		BatchSize:     batchSize,
		MaxBatches:    maxBatches,
		CutoffISO:     cutoffISO,
	}

	result.RuntimeLogs, result.RuntimeLogBatches, err = s.cleanupInBatches(ctx, batchSize, maxBatches, func(ctx context.Context) (int64, error) {
		return s.store.CleanupRuntimeLogIndexBefore(ctx, port.RuntimeLogRetentionCleanupInput{CutoffISO: cutoffISO, Limit: batchSize})
	})
	if err != nil {
		result.Partial = cleanupResultHasProgress(result)
		return result, err
	}
	result.Phase = CleanupPhaseRuntimeLogFileCursors
	result.RuntimeLogFileCursors, result.RuntimeLogFileCursorBatches, err = s.cleanupInBatches(ctx, batchSize, maxBatches, func(ctx context.Context) (int64, error) {
		return s.store.CleanupCompletedRuntimeLogFileCursorsBefore(ctx, port.RuntimeLogRetentionCleanupInput{CutoffISO: cutoffISO, Limit: batchSize})
	})
	if err != nil {
		result.Partial = cleanupResultHasProgress(result)
		return result, err
	}
	result.Phase = CleanupPhaseComplete
	return result, nil
}

func cleanupResultHasProgress(result CleanupResult) bool {
	return result.RuntimeLogs > 0 || result.RuntimeLogFileCursors > 0
}

func (s *Service) retentionDays(ctx context.Context, override int) (int, error) {
	if override != 0 {
		if override < MinRetentionDays || override > MaxRetentionDays {
			return 0, fmt.Errorf("runtimeLogIndexRetentionDays 必须在 %d 到 %d 之间", MinRetentionDays, MaxRetentionDays)
		}
		return override, nil
	}
	days, found, err := s.store.GetRuntimeLogIndexRetentionDays(ctx)
	if err != nil {
		return 0, err
	}
	if !found || days == 0 {
		return DefaultRetentionDays, nil
	}
	return min(MaxRetentionDays, max(MinRetentionDays, days)), nil
}

func (s *Service) cleanupInBatches(
	ctx context.Context,
	batchSize int,
	maxBatches int,
	cleanup func(context.Context) (int64, error),
) (int64, int, error) {
	var total int64
	batches := 0
	for attempt := 0; attempt < maxBatches; attempt++ {
		if err := ctx.Err(); err != nil {
			return total, batches, err
		}
		deleted, err := cleanup(ctx)
		if err != nil {
			return total, batches, err
		}
		if deleted <= 0 {
			break
		}
		total += deleted
		batches++
		if deleted < int64(batchSize) {
			break
		}
		if attempt < maxBatches-1 && s.batchPause > 0 {
			if err := s.sleep(ctx, s.batchPause); err != nil {
				return total, batches, err
			}
		}
	}
	return total, batches, nil
}

func normalizeBatchSize(value int) (int, error) {
	if value == 0 {
		return DefaultBatchSize, nil
	}
	if value < 0 || value > MaxBatchSize {
		return 0, fmt.Errorf("运行日志索引保留清理单批数量必须在 1 到 %d 之间", MaxBatchSize)
	}
	return value, nil
}

func normalizeMaxBatches(value int) (int, error) {
	if value == 0 {
		return DefaultMaxBatches, nil
	}
	if value < 0 || value > MaxMaxBatches {
		return 0, fmt.Errorf("运行日志索引保留清理单轮批数必须在 1 到 %d 之间", MaxMaxBatches)
	}
	return value, nil
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
