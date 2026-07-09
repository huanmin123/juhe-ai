package managementoperationlogs

import (
	"context"
	"fmt"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	DefaultOperationLogRetentionDays     = 365
	MinOperationLogRetentionDays         = 1
	MaxOperationLogRetentionDays         = 3650
	DefaultOperationLogCleanupBatchSize  = 1000
	DefaultOperationLogCleanupMaxBatches = 20
	DefaultOperationLogCleanupBatchPause = 25 * time.Millisecond
	MaxOperationLogCleanupBatchSize      = 1000
	MaxOperationLogCleanupMaxBatches     = 100
)

type RetentionCleanupService struct {
	store      port.OperationLogRetentionCleaner
	batchPause time.Duration
	sleep      func(context.Context, time.Duration) error
}

type RetentionCleanupServiceOptions struct {
	Store      port.OperationLogRetentionCleaner
	BatchPause time.Duration
	Sleep      func(context.Context, time.Duration) error
}

type RetentionCleanupInput struct {
	Now           time.Time
	RetentionDays int
	BatchSize     int
	MaxBatches    int
}

type RetentionCleanupResult struct {
	Deleted         int64
	Batches         int
	RetentionDays   int
	BatchSize       int
	MaxBatches      int
	CutoffCreatedAt time.Time
}

func NewRetentionCleanupService(store port.OperationLogRetentionCleaner) *RetentionCleanupService {
	return NewRetentionCleanupServiceWithOptions(RetentionCleanupServiceOptions{Store: store})
}

func NewRetentionCleanupServiceWithOptions(opts RetentionCleanupServiceOptions) *RetentionCleanupService {
	batchPause := opts.BatchPause
	if batchPause == 0 {
		batchPause = DefaultOperationLogCleanupBatchPause
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	return &RetentionCleanupService{
		store:      opts.Store,
		batchPause: batchPause,
		sleep:      sleep,
	}
}

func (s *RetentionCleanupService) Cleanup(ctx context.Context, input RetentionCleanupInput) (RetentionCleanupResult, error) {
	if s.store == nil {
		return RetentionCleanupResult{}, fmt.Errorf("操作日志保留清理存储不能为空")
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()

	retentionDays, err := s.retentionDays(ctx, input.RetentionDays)
	if err != nil {
		return RetentionCleanupResult{}, err
	}
	batchSize, err := normalizeCleanupBatchSize(input.BatchSize)
	if err != nil {
		return RetentionCleanupResult{}, err
	}
	maxBatches, err := normalizeCleanupMaxBatches(input.MaxBatches)
	if err != nil {
		return RetentionCleanupResult{}, err
	}

	result := RetentionCleanupResult{
		RetentionDays:   retentionDays,
		BatchSize:       batchSize,
		MaxBatches:      maxBatches,
		CutoffCreatedAt: now.Add(-time.Duration(retentionDays) * 24 * time.Hour),
	}
	for result.Batches < maxBatches {
		deleted, err := s.store.CleanupOperationLogsBefore(ctx, port.OperationLogCleanupInput{
			CutoffCreatedAt: result.CutoffCreatedAt,
			Limit:           batchSize,
		})
		if err != nil {
			return RetentionCleanupResult{}, err
		}
		if deleted <= 0 {
			break
		}
		result.Deleted += deleted
		result.Batches++
		if deleted < int64(batchSize) {
			break
		}
		if result.Batches < maxBatches && s.batchPause > 0 {
			if err := s.sleep(ctx, s.batchPause); err != nil {
				return RetentionCleanupResult{}, err
			}
		}
	}
	return result, nil
}

func (s *RetentionCleanupService) retentionDays(ctx context.Context, override int) (int, error) {
	if override != 0 {
		return validateOperationLogRetentionDays(override)
	}
	value, found, err := s.store.GetOperationLogRetentionDays(ctx)
	if err != nil {
		return 0, err
	}
	if !found || value == 0 {
		value = DefaultOperationLogRetentionDays
	}
	return validateOperationLogRetentionDays(value)
}

func validateOperationLogRetentionDays(value int) (int, error) {
	if value < MinOperationLogRetentionDays || value > MaxOperationLogRetentionDays {
		return 0, fmt.Errorf("operationLogRetentionDays 必须在 %d 到 %d 之间", MinOperationLogRetentionDays, MaxOperationLogRetentionDays)
	}
	return value, nil
}

func normalizeCleanupBatchSize(value int) (int, error) {
	if value == 0 {
		return DefaultOperationLogCleanupBatchSize, nil
	}
	if value < 0 || value > MaxOperationLogCleanupBatchSize {
		return 0, fmt.Errorf("操作日志保留清理单批数量必须在 1 到 %d 之间", MaxOperationLogCleanupBatchSize)
	}
	return value, nil
}

func normalizeCleanupMaxBatches(value int) (int, error) {
	if value == 0 {
		return DefaultOperationLogCleanupMaxBatches, nil
	}
	if value < 0 || value > MaxOperationLogCleanupMaxBatches {
		return 0, fmt.Errorf("操作日志保留清理单轮批数必须在 1 到 %d 之间", MaxOperationLogCleanupMaxBatches)
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
