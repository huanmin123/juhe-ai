package accountquality

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

// PrecheckQueueName 与 Node createRetryQueue name 一致。
const PrecheckQueueName = "account-quality-failure-precheck"

// PrecheckRunner 承载 account-quality-failure-precheck-queue：
// 近期失败账户的 API Key 池诊断确认；成功保留状态，确认失败才按
// expectedDispatchRevision CAS 标记 temporary unavailable。
type PrecheckRunner struct {
	clock     Clock
	logger    Logger
	reader    AccountReader
	prober    Prober
	mutation  PrecheckMutation
	queue     *RetryQueue[FailurePrecheckCandidate]
	queueName string

	recentMu        sync.Mutex
	recent          map[string]time.Time
	recentRetention time.Duration
}

// PrecheckDeps 组装 runner。
type PrecheckDeps struct {
	Clock       Clock
	Logger      Logger
	Reader      AccountReader
	Prober      Prober
	Mutation    PrecheckMutation
	Concurrency int
}

// NewPrecheckRunner 构建 runner 并挂接零重试队列
// （Node: sequenceRetryPolicy('account_quality_failure_precheck', [], 0)）。
func NewPrecheckRunner(deps PrecheckDeps) *PrecheckRunner {
	clock := deps.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	logger := deps.Logger
	if logger == nil {
		logger = NopLogger{}
	}
	runner := &PrecheckRunner{
		clock:           clock,
		logger:          logger,
		reader:          deps.Reader,
		prober:          deps.Prober,
		mutation:        deps.Mutation,
		recent:          map[string]time.Time{},
		recentRetention: RecentPrecheckRetention,
	}
	runner.queue = NewRetryQueue[FailurePrecheckCandidate](
		PrecheckQueueName,
		deps.Concurrency,
		clock,
		logger,
		func(ctx context.Context, run QueueRunContext, item FailurePrecheckCandidate) (bool, error) {
			return runner.runQueueItem(ctx, run, item)
		},
		func(event RetryQueueEvent[FailurePrecheckCandidate]) {
			logger.Warn("background_account_quality_failure_precheck_exhausted", map[string]any{
				"accountId":          event.Item.AccountID,
				"recentRequestCount": event.Item.RecentRequestCount,
				"recentErrorCount":   event.Item.RecentErrorCount,
				"attemptCount":       event.AttemptIndex + 1,
			}, "账户质量失败确认任务已用尽，本轮跳过")
		},
	)
	return runner
}

// Enqueue 等价 enqueueAccountQualityFailurePrecheck：30 分钟内已确认过的账户
// 直接跳过（返回 false）；否则以 accountId 为 key 入队。
func (r *PrecheckRunner) Enqueue(candidate FailurePrecheckCandidate) bool {
	r.cleanupRecent()
	if r.wasRecentlyPrechecked(candidate.AccountID) {
		return false
	}
	return r.queue.Enqueue(candidate.AccountID, candidate)
}

// Snapshot 暴露队列快照（供 refresh 日志字段）。
func (r *PrecheckRunner) Snapshot() RetryQueueSnapshot { return r.queue.Snapshot() }

// StopAndDrain 停机排空。
func (r *PrecheckRunner) StopAndDrain(timeout time.Duration) (bool, int) {
	return r.queue.StopAndDrain(timeout)
}

func (r *PrecheckRunner) wasRecentlyPrechecked(accountID string) bool {
	r.recentMu.Lock()
	defer r.recentMu.Unlock()
	checkedAt, ok := r.recent[accountID]
	if !ok {
		return false
	}
	return r.clock.Now().Sub(checkedAt) < r.recentRetention
}


func (r *PrecheckRunner) rememberPrechecked(accountID string) {
	r.recentMu.Lock()
	r.recent[accountID] = r.clock.Now()
	r.recentMu.Unlock()
}

func (r *PrecheckRunner) cleanupRecent() {
	cutoff := r.clock.Now().Add(-r.recentRetention)
	r.recentMu.Lock()
	for accountID, checkedAt := range r.recent {
		if checkedAt.Before(cutoff) {
			delete(r.recent, accountID)
		}
	}
	r.recentMu.Unlock()
}

// runQueueItem 是 runAccountQualityFailurePrecheckQueueItem 的移植。
// 返回 false（或 error）时零重试队列直接 onExhausted。
func (r *PrecheckRunner) runQueueItem(ctx context.Context, runCtx QueueRunContext, item FailurePrecheckCandidate) (bool, error) {
	account, err := r.reader.FindAccountForTest(ctx, item.AccountID)
	if err != nil {
		return false, err
	}
	if !isPrecheckEligible(account, r.clock.Now()) {
		r.rememberPrechecked(item.AccountID)
		r.logger.Debug("background_account_quality_failure_precheck_discarded", map[string]any{
			"accountId":                   item.AccountID,
			"accountStatus":               accountStatusOrEmpty(account),
			"schedulable":                 account != nil && account.Schedulable,
			"boundGroupId":                accountBoundGroupOrEmpty(account),
			"effectiveAvailabilityStatus": "",
		}, "账户质量失败确认任务已失效，跳过")
		return true, nil
	}

	candidateAccount, err := r.reader.FindAccountForGroup(ctx, account.BoundGroupID, account.ID, item.SystemAccountID)
	if err != nil {
		return false, err
	}
	expectedDispatchRevision := int64(0)
	hasDispatchRevision := false
	if candidateAccount != nil && candidateAccount.HasDispatchRevision {
		expectedDispatchRevision = candidateAccount.DispatchRevision
		hasDispatchRevision = true
	}
	if candidateAccount == nil || candidateAccount.Status != AccountStatusActive || !hasDispatchRevision || expectedDispatchRevision < 1 {
		r.rememberPrechecked(item.AccountID)
		fields := map[string]any{
			"accountId":   account.ID,
			"accountName": account.Name,
		}
		if hasDispatchRevision {
			fields["dispatchRevision"] = expectedDispatchRevision
		}
		r.logger.Warn("background_account_quality_failure_precheck_discarded", fields, "账户质量失败确认缺少当前调度代次，已跳过")
		return true, nil
	}

	precheckStartedAt := FormatMillis(r.clock.Now())
	observation, probeErr := r.prober.Probe(ctx, ProbeRequest{
		AccountID:       account.ID,
		SystemAccountID: item.SystemAccountID,
		GroupID:         account.BoundGroupID,
		TrafficSource:   "runtime_recovery_probe",
		Full:            true,
	})
	if probeErr != nil {
		// Node：API Key 池存在调用异常 → 抛 AggregateError → 队列 onExhausted。
		r.logger.Warn("background_account_quality_failure_precheck_api_key_pool_attempt_failed", map[string]any{
			"accountId":      account.ID,
			"accountName":    account.Name,
			"failedKeyCount": 1,
		}, "账户质量失败确认的 Key 池探针存在调用异常")
		return false, fmt.Errorf("账户 %s 的质量确认 API Key 池存在调用异常: %w", account.ID, probeErr)
	}

	r.rememberPrechecked(item.AccountID)
	result := observation.Result
	probeOutcome := AutomaticProbeOutcome(result, observation.Evidence)

	switch {
	case probeOutcome == OutcomeCompleteSuccess:
		r.logger.Info("background_account_quality_failure_precheck_recovered", map[string]any{
			"accountId":          account.ID,
			"accountName":        account.Name,
			"statusCode":         statusCodeOrNil(result.StatusCode),
			"durationMs":         result.DurationMs,
			"recentRequestCount": item.RecentRequestCount,
			"recentErrorCount":   item.RecentErrorCount,
			"attemptIndex":       runCtx.AttemptIndex,
			"retryNumber":        runCtx.RetryNumber,
		}, "账户近期频繁失败但后台确认通过，保留正常状态")
		return true, nil
	case !AvailabilityProbeFailed(probeOutcome):
		r.logger.Warn("background_account_quality_failure_precheck_ineligible_failure_discarded", map[string]any{
			"accountId":          account.ID,
			"accountName":        account.Name,
			"statusCode":         statusCodeOrNil(result.StatusCode),
			"errorCode":          result.ErrorCode,
			"durationMs":         result.DurationMs,
			"message":            result.Message,
			"recentRequestCount": item.RecentRequestCount,
			"recentErrorCount":   item.RecentErrorCount,
		}, "账户近期频繁失败但后台探针未形成有效上游可用性结论，已跳过状态写入")
		return true, nil
	}

	mutation, err := r.mutation.MarkPrecheckTemporaryUnavailable(ctx, PrecheckMutationInput{
		AccountID:                candidateAccount.ID,
		Reason:                   precheckReason(item, result),
		PrecheckStartedAt:        precheckStartedAt,
		ExpectedDispatchRevision: expectedDispatchRevision,
		ExpectedStatus:           AccountStatusActive,
	})
	if err != nil {
		return false, err
	}
	r.logger.Warn("background_account_quality_failure_precheck_marked", map[string]any{
		"accountId":          account.ID,
		"accountName":        account.Name,
		"statusCode":         statusCodeOrNil(result.StatusCode),
		"errorCode":          result.ErrorCode,
		"durationMs":         result.DurationMs,
		"recentRequestCount": item.RecentRequestCount,
		"recentErrorCount":   item.RecentErrorCount,
		"updated":            mutation.Updated,
		"skippedReason":      mutation.SkippedReason,
	}, "账户近期频繁失败且后台确认未通过，已尝试标记为临时不可调用")
	return true, nil
}

// isPrecheckEligible 是 isAccountQualityFailurePrecheckEligible 的移植。
func isPrecheckEligible(account *AccountForTest, now time.Time) bool {
	if account == nil {
		return false
	}
	if account.Status != AccountStatusActive || !account.Schedulable || account.BoundGroupID == "" {
		return false
	}
	if account.AccountExpiresAt != "" {
		expiresAtMs, err := ParseMillisField(account.AccountExpiresAt, "accountExpiresAt")
		if err != nil {
			panic(err)
		}
		if !expiresAtMs.After(now) {
			return false
		}
	}
	if account.HasEffectiveAvail && !account.EffectiveAvailable {
		return false
	}
	return true
}

func accountStatusOrEmpty(account *AccountForTest) string {
	if account == nil {
		return ""
	}
	return account.Status
}

func accountBoundGroupOrEmpty(account *AccountForTest) string {
	if account == nil {
		return ""
	}
	return account.BoundGroupID
}

func statusCodeOrNil(statusCode *int) any {
	if statusCode == nil {
		return nil
	}
	return *statusCode
}

// precheckReason 是 accountQualityFailurePrecheckReason 的逐字段移植，
// 分隔符与 1000 字符截断保持一致。
func precheckReason(item FailurePrecheckCandidate, result ProbeResult) string {
	parts := []string{
		"近期质量频繁失败，后台确认失败后标记为临时不可调用",
		fmt.Sprintf("近窗口 %d 次请求失败 %d 次", item.RecentRequestCount, item.RecentErrorCount),
	}
	if item.SuccessRate != nil {
		parts = append(parts, fmt.Sprintf("成功率 %d%%", int64(math.Round(*item.SuccessRate*100))))
	}
	if item.LastErrorAt != "" {
		parts = append(parts, fmt.Sprintf("最后业务失败 %s", item.LastErrorAt))
	}
	if result.StatusCode != nil {
		parts = append(parts, fmt.Sprintf("确认 HTTP %d", int64(math.Trunc(float64(*result.StatusCode)))))
	}
	if result.ErrorCode != "" {
		parts = append(parts, result.ErrorCode)
	}
	if result.Message != "" {
		parts = append(parts, result.Message)
	} else if item.LastErrorMessage != "" {
		parts = append(parts, item.LastErrorMessage)
	}
	joined := strings.Join(parts, "；")
	if len(joined) > 1000 {
		// Node slice(0, 1000) 按 UTF-16 码元截断；Go 按字节截断在多字节
		// 字符下不等价，这里按 rune 截断并保持不超过 1000 个码元语义。
		runes := []rune(joined)
		if len(runes) > 1000 {
			joined = string(runes[:1000])
		}
	}
	return joined
}
