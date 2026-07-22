package managementpublicapilogs

import (
	"context"
	"fmt"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	DefaultPublicAPILogRetentionDays     = 30
	MinPublicAPILogRetentionDays         = 1
	MaxPublicAPILogRetentionDays         = 365
	DefaultPublicAPILogCleanupBatchSize  = 1000
	DefaultPublicAPILogCleanupMaxBatches = 20
	DefaultPublicAPILogCleanupBatchPause = 25 * time.Millisecond
	MaxPublicAPILogCleanupBatchSize      = 1000
	MaxPublicAPILogCleanupMaxBatches     = 100
	RetentionCleanupPhasePublicAPILogs   = "public_api_logs"
	RetentionCleanupPhaseComplete        = "complete"
)

type RetentionCleanupService struct {
	store      port.PublicAPILogRetentionCleaner
	batchPause time.Duration
	sleep      func(context.Context, time.Duration) error
}

type RetentionCleanupServiceOptions struct {
	Store      port.PublicAPILogRetentionCleaner
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
	Phase           string
	Partial         bool
	RetentionDays   int
	BatchSize       int
	MaxBatches      int
	CutoffCreatedAt time.Time
}

func NewRetentionCleanupService(store port.PublicAPILogRetentionCleaner) *RetentionCleanupService {
	return NewRetentionCleanupServiceWithOptions(RetentionCleanupServiceOptions{Store: store})
}

func NewRetentionCleanupServiceWithOptions(opts RetentionCleanupServiceOptions) *RetentionCleanupService {
	batchPause := opts.BatchPause
	if batchPause == 0 {
		batchPause = DefaultPublicAPILogCleanupBatchPause
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
		return RetentionCleanupResult{}, fmt.Errorf("公开接口日志保留清理存储不能为空")
	}
	if err := ctx.Err(); err != nil {
		return RetentionCleanupResult{}, err
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
		Phase:           RetentionCleanupPhasePublicAPILogs,
		RetentionDays:   retentionDays,
		BatchSize:       batchSize,
		MaxBatches:      maxBatches,
		CutoffCreatedAt: now.Add(-time.Duration(retentionDays) * 24 * time.Hour),
	}
	for result.Batches < maxBatches {
		if err := ctx.Err(); err != nil {
			result.Partial = result.Deleted > 0
			return result, err
		}
		deleted, err := s.store.CleanupPublicAPILogsBefore(ctx, port.PublicAPILogCleanupInput{
			CutoffCreatedAt: result.CutoffCreatedAt,
			Limit:           batchSize,
		})
		if err != nil {
			result.Partial = result.Deleted > 0
			return result, err
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
				result.Partial = result.Deleted > 0
				return result, err
			}
		}
	}
	result.Phase = RetentionCleanupPhaseComplete
	return result, nil
}

func (s *RetentionCleanupService) retentionDays(ctx context.Context, override int) (int, error) {
	if override != 0 {
		return validatePublicAPILogRetentionDays(override)
	}
	value, found, err := s.store.GetPublicAPILogRetentionDays(ctx)
	if err != nil {
		return 0, err
	}
	if !found || value == 0 {
		value = DefaultPublicAPILogRetentionDays
	}
	return validatePublicAPILogRetentionDays(value)
}

func validatePublicAPILogRetentionDays(value int) (int, error) {
	if value < MinPublicAPILogRetentionDays || value > MaxPublicAPILogRetentionDays {
		return 0, fmt.Errorf("publicApiLogRetentionDays 必须在 %d 到 %d 之间", MinPublicAPILogRetentionDays, MaxPublicAPILogRetentionDays)
	}
	return value, nil
}

func normalizeCleanupBatchSize(value int) (int, error) {
	if value == 0 {
		return DefaultPublicAPILogCleanupBatchSize, nil
	}
	if value < 0 || value > MaxPublicAPILogCleanupBatchSize {
		return 0, fmt.Errorf("公开接口日志保留清理单批数量必须在 1 到 %d 之间", MaxPublicAPILogCleanupBatchSize)
	}
	return value, nil
}

func normalizeCleanupMaxBatches(value int) (int, error) {
	if value == 0 {
		return DefaultPublicAPILogCleanupMaxBatches, nil
	}
	if value < 0 || value > MaxPublicAPILogCleanupMaxBatches {
		return 0, fmt.Errorf("公开接口日志保留清理单轮批数必须在 1 到 %d 之间", MaxPublicAPILogCleanupMaxBatches)
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
