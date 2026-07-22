package apikeyrecordcleanup

import (
	"context"
	"fmt"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	DefaultTargetLimit = 25
	MaxTargetLimit     = port.MaxAPIKeyRecordCleanupTargetLimit

	UsageCleanupContractUnavailableBlockedReason = "Go 当前 schema 尚未提供 usage_records、usage_record_cleanup_deductions 与完整派生统计清理契约；本轮未删除关联记录，已保留目标等待后续重试"
	UsageCleanupContractUnavailableConcern       = "当前 Go migrations 无法完整执行 usage 统计扣减与派生记录清理；run-once 仅安全 claim 并标记 blocked，不删除 usage、audit 或统计记录"
)

type Service struct {
	store port.APIKeyRecordCleanupRunner
}

type RunOnceInput struct {
	Now   time.Time
	Limit int
}

type RunOnceResult struct {
	Attempted   int64
	Completed   int64
	Deferred    int64
	DeletedRows int64
	Concern     string
}

func NewService(store port.APIKeyRecordCleanupRunner) *Service {
	return &Service{store: store}
}

func (s *Service) RunOnce(ctx context.Context, input RunOnceInput) (RunOnceResult, error) {
	if s == nil || s.store == nil {
		return RunOnceResult{}, fmt.Errorf("API Key 记录清理存储不能为空")
	}
	limit, err := normalizeTargetLimit(input.Limit)
	if err != nil {
		return RunOnceResult{}, err
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}

	result, err := s.store.RunAPIKeyRecordCleanupOnce(ctx, port.APIKeyRecordCleanupRunInput{
		Limit:         limit,
		AttemptedAt:   now.UTC(),
		BlockedReason: UsageCleanupContractUnavailableBlockedReason,
	})
	if err != nil {
		return RunOnceResult{}, fmt.Errorf("执行 API Key 记录清理 run-once: %w", err)
	}
	return RunOnceResult{
		Attempted: result.Attempted,
		Deferred:  result.Deferred,
		Concern:   UsageCleanupContractUnavailableConcern,
	}, nil
}

func normalizeTargetLimit(value int) (int, error) {
	if value == 0 {
		return DefaultTargetLimit, nil
	}
	if value < 0 || value > MaxTargetLimit {
		return 0, fmt.Errorf("API Key 记录清理目标数量必须在 1 到 %d 之间", MaxTargetLimit)
	}
	return value, nil
}
