package opsjobs

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// AI 账户余额自动探测，逐语义对齐 Node
// modules/background/account-balance-auto-detect.service.ts：
//   - 阈值：detectionIntervalMinutes=5、detectionRetryMinutes=5；
//   - builtin 查询 snapshot.status ∈ {fresh, unlimited} 视为命中；
//     unsupported 记为不支持；其余（stale/error 等）与查询错误一律 retry；
//   - 命中后 enable（expectedConfigRevision/expectedNextRefreshAt 围栏）→
//     nextRefreshAt = now + passiveJitter(intervalMinutes*60_000) →
//     replace snapshot if current；
//   - 恢复扫描是持久路径：候选来自 DB 持久化探测意图（首次健康成功写入），
//     worker 被 kill 后重启由本扫描重新认领续跑；
//   - lease_busy 顺延（deferred），stale 表示围栏过期。

const (
	BalanceDetectionIntervalMinutes = 5
	BalanceDetectionRetryMinutes    = 5
	// BalanceAutoDetectionRecoveryBatchSize 对齐 Node 默认
	// JUHE_AI_BACKGROUND_ACCOUNT_BALANCE_AUTO_DETECTION_RECOVERY_BATCH_SIZE=2。
	BalanceAutoDetectionRecoveryBatchSize = 2
)

// BalanceQueryConfig 对齐 AccountBalanceQueryConfig。
type BalanceQueryConfig struct {
	Adapter                 string `json:"adapter"`
	IntervalMinutes         int    `json:"interval_minutes"`
	PreferredBuiltinAdapter string `json:"preferred_builtin_adapter,omitempty"`
}

// BalanceSnapshotStatus 快照状态词表。
type BalanceSnapshotStatus string

const (
	BalanceSnapshotFresh       BalanceSnapshotStatus = "fresh"
	BalanceSnapshotUnlimited   BalanceSnapshotStatus = "unlimited"
	BalanceSnapshotUnsupported BalanceSnapshotStatus = "unsupported"
	BalanceSnapshotStale       BalanceSnapshotStatus = "stale"
	BalanceSnapshotError       BalanceSnapshotStatus = "error"
)

// BalanceSnapshot 是探测结果的窄投影。
type BalanceSnapshot struct {
	Status         BalanceSnapshotStatus `json:"status"`
	Currency       string                `json:"currency,omitempty"`
	DisplayBalance *float64              `json:"display_balance,omitempty"`
	RawStatus      string                `json:"raw_status,omitempty"`
	ErrorMessage   string                `json:"error_message,omitempty"`
}

// BalanceDetectionCandidate 对齐 AccountBalanceDetectionCandidate。
type BalanceDetectionCandidate struct {
	ID              string  `json:"id"`
	SystemAccountID string  `json:"system_account_id"`
	InputVersion    *int64  `json:"input_version,omitempty"`
	ConfigRevision  int64   `json:"config_revision"`
	NextRefreshAt   *string `json:"next_refresh_at,omitempty"`
	ProxyProfileID  string  `json:"proxy_profile_id,omitempty"`
}

// BalanceBuiltinQueryResult 是 builtin 适配器查询结果。
type BalanceBuiltinQueryResult struct {
	Adapter  string          `json:"adapter"`
	Snapshot BalanceSnapshot `json:"snapshot"`
}

// BalanceLeaseRunner 以候选为粒度的互斥租约（Node runWithAccountBalanceLease）。
// acquired=false 表示其他节点在跑。
type BalanceLeaseRunner interface {
	RunWithLease(ctx context.Context, candidate BalanceDetectionCandidate, run func(ctx context.Context) error) (acquired bool, err error)
}

// BalanceDetectionRepo 是探测持久化 port。
type BalanceDetectionRepo interface {
	// ListDueCandidates 读取到期探测意图（持久化恢复路径）。
	ListDueCandidates(ctx context.Context, limit int) ([]BalanceDetectionCandidate, error)
	// CommitDetectionDue 围栏提交探测意图完成/顺延。
	CommitDetectionDue(ctx context.Context, input BalanceCommitDueInput) (bool, error)
	// EnableDetectedQuery 围栏开启探测配置。
	EnableDetectedQuery(ctx context.Context, input BalanceEnableInput) (bool, error)
	// ReplaceSnapshotIfCurrent 围栏写入快照。
	ReplaceSnapshotIfCurrent(ctx context.Context, input BalanceSnapshotInput) (bool, error)
}

// BalanceCommitDueInput 围栏输入。
type BalanceCommitDueInput struct {
	AccountID              string  `json:"account_id"`
	ExpectedConfigRevision int64   `json:"expected_config_revision"`
	ExpectedNextRefreshAt  *string `json:"expected_next_refresh_at,omitempty"`
	NextRefreshAt          *string `json:"next_refresh_at"` // nil = 收口
}

// BalanceEnableInput 开启探测输入。
type BalanceEnableInput struct {
	AccountID              string             `json:"account_id"`
	ExpectedConfigRevision int64              `json:"expected_config_revision"`
	ExpectedNextRefreshAt  *string            `json:"expected_next_refresh_at,omitempty"`
	Config                 BalanceQueryConfig `json:"config"`
	NextRefreshAt          string             `json:"next_refresh_at"`
}

// BalanceSnapshotInput 快照写入围栏输入。
type BalanceSnapshotInput struct {
	AccountID              string               `json:"account_id"`
	SystemAccountID        string               `json:"system_account_id"`
	ExpectedConfigRevision int64                `json:"expected_config_revision"`
	ExpectedConfig         BalanceQueryConfig   `json:"expected_config"`
	Snapshot               BalanceSnapshotWrite `json:"snapshot"`
	NextRefreshAfter       string               `json:"next_refresh_after"`
}

// BalanceSnapshotWrite 是持久化快照。
type BalanceSnapshotWrite struct {
	Status         BalanceSnapshotStatus `json:"status"`
	Currency       string                `json:"currency,omitempty"`
	DisplayBalance *float64              `json:"display_balance,omitempty"`
	RawStatus      string                `json:"raw_status,omitempty"`
	ConfigRevision int64                 `json:"config_revision"`
	LastAttemptAt  string                `json:"last_attempt_at"`
	LastSuccessAt  string                `json:"last_success_at"`
}

// BalanceDetector 抽象 builtin 余额查询（外部上游依赖，mock 闭环）。
type BalanceDetector interface {
	QueryBuiltin(ctx context.Context, candidate BalanceDetectionCandidate, config BalanceQueryConfig) (BalanceBuiltinQueryResult, error)
}

// BalanceAutoDetectionOutcome 与 Node 完全一致。
type BalanceAutoDetectionOutcome string

const (
	BalanceOutcomeEnabled     BalanceAutoDetectionOutcome = "enabled"
	BalanceOutcomeUnsupported BalanceAutoDetectionOutcome = "unsupported"
	BalanceOutcomeRetry       BalanceAutoDetectionOutcome = "retry"
	BalanceOutcomeStale       BalanceAutoDetectionOutcome = "stale"
	BalanceOutcomeLeaseBusy   BalanceAutoDetectionOutcome = "lease_busy"
)

// BalanceAutoDetectionRecoverySummary 计数字段与 Node 一致。
type BalanceAutoDetectionRecoverySummary struct {
	Outcome          string `json:"outcome"` // success | partial
	SelectedCount    int    `json:"selectedCount"`
	EnabledCount     int    `json:"enabledCount"`
	UnsupportedCount int    `json:"unsupportedCount"`
	RetryCount       int    `json:"retryCount"`
	StaleCount       int    `json:"staleCount"`
	DeferredCount    int    `json:"deferredCount"`
}

// BalanceAutoDetectDependencies 注入仓储/租约/探测器与时钟。
type BalanceAutoDetectDependencies struct {
	Repo     BalanceDetectionRepo
	Lease    BalanceLeaseRunner
	Detector BalanceDetector
	NowMS    func() int64
	Random   RandomUnit // 被动抖动随机源；nil = 确定性（0 偏移规范化为 +1ms）
}

type balanceAttemptKind string

const (
	balanceAttemptMatched     balanceAttemptKind = "matched"
	balanceAttemptUnsupported balanceAttemptKind = "unsupported"
	balanceAttemptRetry       balanceAttemptKind = "retry"
)

// DetectAccountBalanceAdapterAttempt 对齐 detectAccountBalanceAdapterAttempt：
// 命中返回 config+snapshot；unsupported / retry / 错误分类与 Node 一致
// （查询错误返回 retry 且保留原始错误供调用方记录）。
func DetectAccountBalanceAdapterAttempt(ctx context.Context, candidate BalanceDetectionCandidate, config BalanceQueryConfig, deps BalanceAutoDetectDependencies) (BalanceBuiltinQueryResult, balanceAttemptKind, error) {
	result, err := deps.Detector.QueryBuiltin(ctx, candidate, config)
	if err != nil {
		return BalanceBuiltinQueryResult{}, balanceAttemptRetry, err
	}
	switch result.Snapshot.Status {
	case BalanceSnapshotFresh, BalanceSnapshotUnlimited:
		if result.Adapter == "" {
			result.Adapter = config.Adapter
		}
		return result, balanceAttemptMatched, nil
	case BalanceSnapshotUnsupported:
		return BalanceBuiltinQueryResult{}, balanceAttemptUnsupported, nil
	default:
		return BalanceBuiltinQueryResult{}, balanceAttemptRetry, nil
	}
}

// AutoDetectAccountBalanceCandidate 对齐 autoDetectAccountBalanceCandidate。
func AutoDetectAccountBalanceCandidate(ctx context.Context, candidate BalanceDetectionCandidate, deps BalanceAutoDetectDependencies) (BalanceAutoDetectionOutcome, error) {
	if err := validateBalanceDeps(deps); err != nil {
		return "", err
	}
	var outcome BalanceAutoDetectionOutcome
	acquired, err := deps.Lease.RunWithLease(ctx, candidate, func(leaseCtx context.Context) error {
		detected, detectErr := autoDetectWithLease(leaseCtx, candidate, deps)
		if detectErr != nil {
			return detectErr
		}
		outcome = detected
		return nil
	})
	if err != nil {
		return "", err
	}
	if !acquired {
		return BalanceOutcomeLeaseBusy, nil
	}
	return outcome, nil
}

func autoDetectWithLease(ctx context.Context, candidate BalanceDetectionCandidate, deps BalanceAutoDetectDependencies) (BalanceAutoDetectionOutcome, error) {
	config := BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: BalanceDetectionIntervalMinutes}
	result, kind, attemptErr := DetectAccountBalanceAdapterAttempt(ctx, candidate, config, deps)
	if attemptErr != nil && kind != balanceAttemptRetry {
		return "", attemptErr
	}
	nowMS := deps.NowMS()
	switch kind {
	case balanceAttemptUnsupported:
		completed, err := completeBalanceDetectionIntent(ctx, candidate, nil, deps)
		if err != nil {
			return "", err
		}
		if completed {
			return BalanceOutcomeUnsupported, nil
		}
		return BalanceOutcomeStale, nil
	case balanceAttemptRetry:
		retryAt := time.UnixMilli(nowMS + PassiveScheduleDelayMS(BalanceDetectionRetryMinutes*60_000, jitterRandom(deps.Random))).UTC().Format(time.RFC3339Nano)
		deferred, err := completeBalanceDetectionIntent(ctx, candidate, &retryAt, deps)
		if err != nil {
			return "", err
		}
		if deferred {
			return BalanceOutcomeRetry, nil
		}
		if candidate.NextRefreshAt != nil {
			return BalanceOutcomeStale, nil
		}
		return BalanceOutcomeRetry, nil
	}

	// matched：intervalMinutes 兜底 Math.max(1, trunc(n)||1)。
	intervalMinutes := BalanceDetectionIntervalMinutes
	if intervalMinutes < 1 {
		intervalMinutes = 1
	}
	intervalMS := int64(intervalMinutes) * 60_000
	nextRefreshAt := time.UnixMilli(nowMS + PassiveScheduleDelayMS(intervalMS, jitterRandom(deps.Random))).UTC().Format(time.RFC3339Nano)
	completedAt := time.UnixMilli(nowMS).UTC().Format(time.RFC3339Nano)
	detectedConfig := BalanceQueryConfig{
		Adapter:                 "builtin",
		IntervalMinutes:         intervalMinutes,
		PreferredBuiltinAdapter: result.Adapter,
	}
	enabled, err := deps.Repo.EnableDetectedQuery(ctx, BalanceEnableInput{
		AccountID:              candidate.ID,
		ExpectedConfigRevision: candidate.ConfigRevision,
		ExpectedNextRefreshAt:  candidate.NextRefreshAt,
		Config:                 detectedConfig,
		NextRefreshAt:          nextRefreshAt,
	})
	if err != nil {
		return "", err
	}
	if !enabled {
		return BalanceOutcomeStale, nil
	}
	written, err := deps.Repo.ReplaceSnapshotIfCurrent(ctx, BalanceSnapshotInput{
		AccountID:              candidate.ID,
		SystemAccountID:        candidate.SystemAccountID,
		ExpectedConfigRevision: candidate.ConfigRevision,
		ExpectedConfig:         detectedConfig,
		NextRefreshAfter:       nextRefreshAt,
		Snapshot: BalanceSnapshotWrite{
			Status:         result.Snapshot.Status,
			Currency:       result.Snapshot.Currency,
			DisplayBalance: result.Snapshot.DisplayBalance,
			RawStatus:      result.Snapshot.RawStatus,
			ConfigRevision: candidate.ConfigRevision,
			LastAttemptAt:  completedAt,
			LastSuccessAt:  completedAt,
		},
	})
	if err != nil {
		return "", err
	}
	if !written {
		return BalanceOutcomeStale, nil
	}
	return BalanceOutcomeEnabled, nil
}

// completeBalanceDetectionIntent 对齐 completeAccountBalanceDetectionIntent：
// nextRefreshAt=nil 表示探测收口（不再保留首次探测意图）。
func completeBalanceDetectionIntent(ctx context.Context, candidate BalanceDetectionCandidate, nextRefreshAt *string, deps BalanceAutoDetectDependencies) (bool, error) {
	if candidate.NextRefreshAt == nil {
		return true, nil
	}
	return deps.Repo.CommitDetectionDue(ctx, BalanceCommitDueInput{
		AccountID:              candidate.ID,
		ExpectedConfigRevision: candidate.ConfigRevision,
		ExpectedNextRefreshAt:  candidate.NextRefreshAt,
		NextRefreshAt:          nextRefreshAt,
	})
}

// RunBalanceAutoDetectionRecovery 对齐 runAccountBalanceAutoDetectionRecovery：
// 有界补偿扫描，逐候选处理并输出计数汇总；日志由组合根输出。
func RunBalanceAutoDetectionRecovery(ctx context.Context, deps BalanceAutoDetectDependencies) (BalanceAutoDetectionRecoverySummary, error) {
	if err := validateBalanceDeps(deps); err != nil {
		return BalanceAutoDetectionRecoverySummary{}, err
	}
	candidates, err := deps.Repo.ListDueCandidates(ctx, BalanceAutoDetectionRecoveryBatchSize)
	if err != nil {
		return BalanceAutoDetectionRecoverySummary{}, fmt.Errorf("读取余额自动探测候选失败: %w", err)
	}
	summary := BalanceAutoDetectionRecoverySummary{SelectedCount: len(candidates)}
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			summary.DeferredCount++
			continue
		}
		outcome, detectErr := AutoDetectAccountBalanceCandidate(ctx, candidate, deps)
		if detectErr != nil {
			return summary, detectErr
		}
		switch outcome {
		case BalanceOutcomeEnabled:
			summary.EnabledCount++
		case BalanceOutcomeUnsupported:
			summary.UnsupportedCount++
		case BalanceOutcomeRetry:
			summary.RetryCount++
		case BalanceOutcomeLeaseBusy:
			summary.DeferredCount++
		default:
			summary.StaleCount++
		}
	}
	if summary.RetryCount > 0 || summary.StaleCount > 0 || summary.DeferredCount > 0 {
		summary.Outcome = "partial"
	} else {
		summary.Outcome = "success"
	}
	return summary, nil
}

func validateBalanceDeps(deps BalanceAutoDetectDependencies) error {
	if deps.Repo == nil || deps.Lease == nil || deps.Detector == nil {
		return errors.New("余额自动探测依赖未初始化")
	}
	if deps.NowMS == nil {
		return errors.New("余额自动探测必须注入 NowMS 时钟")
	}
	return nil
}

func jitterRandom(random RandomUnit) RandomUnit {
	if random != nil {
		return random
	}
	// 确定性退化：取窗口上界（unit=1 → +window），测试可回放。
	return RandomUnit(func() float64 { return 1 })
}
