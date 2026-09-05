package accountquality

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// CooldownQueueName 与 Node createRetryQueue name 一致。
const CooldownQueueName = "account-api-key-cooldown-retest"

// 可恢复探针资格（Node storage/account-status.ts）：
// isAccountStatusEligibleForRecoveryProbe = active || rate_limited || temporary_unavailable。
func isAccountStatusEligibleForRecoveryProbe(status string) bool {
	return status == AccountStatusActive || status == AccountStatusRateLimited || status == AccountStatusTemporaryUnavail
}

// QuotaRetestDecision 等价 Node AccountApiKeyQuotaRetestDecision。
type QuotaRetestDecision struct {
	QuotaFailure         bool
	StatusCode           int
	ErrorCode            string
	Message              string
	PreviousRecoveryMode QuotaRecoveryMode
	HasPreviousMode      bool
	RecoveryMode         QuotaRecoveryMode
	HasRecoveryMode      bool
	RecoveryHint         *QuotaRecoveryHint
	TimedOut             bool
	CooldownUntil        string
}

// ResolveQuotaRetestDecision 是 resolveAccountApiKeyQuotaRetestDecision 的移植。
// evidence 携带上游尝试证据；observedAt 为响应观测时间（Node：额度 hint 与
// 恢复窗口都以响应观测时刻定义，而非探针发起时刻）。
func ResolveQuotaRetestDecision(result ProbeResult, evidence ProbeEvidence, candidate *OpenAIAccountCandidate, previousErrCode, recoveryStartedAt, recoverySeed string, observedAt time.Time) QuotaRetestDecision {
	bodyText := result.ResponseBodyText
	errorCode := result.ErrorCode
	if errorCode == "" && bodyText != "" {
		errorCode = parseUpstreamErrorCode(bodyText)
	}
	message := result.Message
	if bodyText != "" {
		if parsed := parseUpstreamMessage(bodyText); parsed != "" {
			message = parsed
		}
	}
	var searchable []string
	for _, value := range []string{message, bodyText, result.Message} {
		if strings.TrimSpace(value) != "" {
			searchable = append(searchable, value)
		}
	}
	searchableText := strings.Join(searchable, "\n")
	// Node：statusCode = upstreamAttempt?.status ?? result.statusCode ?? 0。
	statusCode := 0
	if evidence.HasRealUpstreamAttempt {
		statusCode = evidence.UpstreamStatus
	} else if result.StatusCode != nil {
		statusCode = *result.StatusCode
	}
	quotaFailure := SystemInsufficientQuotaRuleMatches(statusCode, errorCode, "", searchableText)
	var recoveryHint *QuotaRecoveryHint
	if quotaFailure {
		recoveryHint = ExtractQuotaRecoveryHint(bodyText, result.ResponseHeaders, observedAt)
	}
	previousRecoveryMode, hasPrevious := APIKeyQuotaRecoveryModeFromErrorCode(previousErrCode)
	var recoveryMode QuotaRecoveryMode
	hasRecovery := false
	if quotaFailure {
		if recoveryHint != nil {
			recoveryMode = recoveryHint.Mode
		} else {
			recoveryMode = QuotaRecoveryGeneric
		}
		hasRecovery = true
	}
	timedOut := hasRecovery && recoveryMode == QuotaRecoveryGeneric && APIKeyQuotaObservationExceeded(recoveryStartedAt, observedAt)

	decision := QuotaRetestDecision{
		QuotaFailure: quotaFailure,
		StatusCode:   statusCode,
		Message:      message,
		TimedOut:     timedOut,
	}
	if errorCode != "" {
		decision.ErrorCode = errorCode
	}
	if hasPrevious {
		decision.PreviousRecoveryMode = previousRecoveryMode
		decision.HasPreviousMode = true
	}
	if hasRecovery {
		decision.RecoveryMode = recoveryMode
		decision.HasRecoveryMode = true
	}
	if recoveryHint != nil {
		decision.RecoveryHint = recoveryHint
	}
	if !timedOut && hasRecovery {
		if recoveryHint != nil {
			decision.CooldownUntil = recoveryHint.CooldownUntil
		} else {
			var policy *QuotaRecoveryPolicy
			if candidate != nil && len(candidate.QuotaRecoveryPolicy) > 0 {
				normalized, err := NormalizeQuotaRecoveryPolicy(candidate.QuotaRecoveryPolicy)
				if err == nil {
					policy = &normalized
				}
			}
			seed := recoverySeed
			if seed == "" {
				seed = "api-key:default"
			}
			decision.CooldownUntil = GenericAPIKeyQuotaCooldownUntil(policy, seed, observedAt)
		}
	}
	return decision
}

// parseUpstreamErrorCode 是 parseAccountTestUpstreamErrorCode 的窄移植：
// 递归查找 JSON 中首个 error.code / code 字段（openai 报文契约）。
func parseUpstreamErrorCode(bodyText string) string {
	value := parseJSONValue(bodyText)
	if code := findErrorCode(value); code != "" {
		return code
	}
	return ""
}

func findErrorCode(value any) string {
	obj, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	if errObj, present := obj["error"]; present {
		if found := findErrorCode(errObj); found != "" {
			return found
		}
	}
	if code, present := obj["code"]; present {
		if text, ok := code.(string); ok && strings.TrimSpace(text) != "" {
			return text
		}
	}
	for _, child := range obj {
		if found := findErrorCode(child); found != "" {
			return found
		}
	}
	return ""
}

// parseUpstreamMessage 是 parseAccountTestUpstreamMessage(rawFallback: true) 的
// 窄移植：JSON error.message 优先，回落 bodyText 前 240 字符。
func parseUpstreamMessage(bodyText string) string {
	value := parseJSONValue(bodyText)
	if message := findErrorMessage(value, 0); message != "" {
		return message
	}
	runes := []rune(bodyText)
	if len(runes) > 240 {
		runes = runes[:240]
	}
	return string(runes)
}

func findErrorMessage(value any, depth int) string {
	if depth > 8 {
		return ""
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	if errObj, present := obj["error"]; present {
		if found := findErrorMessage(errObj, depth+1); found != "" {
			return found
		}
	}
	if message, present := obj["message"]; present {
		if text, ok := message.(string); ok && strings.TrimSpace(text) != "" {
			return text
		}
	}
	for _, child := range obj {
		if found := findErrorMessage(child, depth+1); found != "" {
			return found
		}
	}
	return ""
}

// CooldownRetestRunner 承载 account-api-key-cooldown-retest scheduled 扫描
// 与同名异步队列。
type CooldownRetestRunner struct {
	clock       Clock
	logger      Logger
	reader      AccountReader
	prober      Prober
	candidates  CooldownCandidateSource
	mutation    CooldownMutation
	queue       *RetryQueue[CooldownQueueItem]
	settings    SettingsNumber
	batchSize   int
	concurrency QueueConcurrency
}

// CooldownQueueItem 等价 Node AccountApiKeyCooldownRetestQueueItem
// （candidate + maxRecoveryHours）。
type CooldownQueueItem struct {
	Candidate        CooldownProbeCandidate
	MaxRecoveryHours int
}

// CooldownDeps 组装 runner。
type CooldownDeps struct {
	Clock       Clock
	Logger      Logger
	Reader      AccountReader
	Prober      Prober
	Candidates  CooldownCandidateSource
	Mutation    CooldownMutation
	Settings    SettingsNumber
	Concurrency QueueConcurrency
	// BatchSize 为空取默认 10。
	BatchSize int
	// QueueConcurrency 固定并发；Node 使用 globalSharedQueueConcurrency。
	QueueWorkers int
}

// NewCooldownRetestRunner 构建 runner，挂接零重试队列
// （Node: sequenceRetryPolicy('account_api_key_cooldown_retest_revival', [], 0)）。
func NewCooldownRetestRunner(deps CooldownDeps) *CooldownRetestRunner {
	clock := deps.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	logger := deps.Logger
	if logger == nil {
		logger = NopLogger{}
	}
	batch := deps.BatchSize
	if batch == 0 {
		batch = DefaultCooldownRetestBatchSize
	}
	runner := &CooldownRetestRunner{
		clock:       clock,
		logger:      logger,
		reader:      deps.Reader,
		prober:      deps.Prober,
		candidates:  deps.Candidates,
		mutation:    deps.Mutation,
		settings:    deps.Settings,
		batchSize:   batch,
		concurrency: deps.Concurrency,
	}
	runner.queue = NewRetryQueue[CooldownQueueItem](
		CooldownQueueName,
		deps.QueueWorkers,
		clock,
		logger,
		func(ctx context.Context, run QueueRunContext, item CooldownQueueItem) (bool, error) {
			return runner.runQueueItem(ctx, run, item)
		},
		func(event RetryQueueEvent[CooldownQueueItem]) {
			logger.Warn("background_account_api_key_cooldown_retest_retry_exhausted", map[string]any{
				"accountId":      event.Item.Candidate.AccountID,
				"accountName":    event.Item.Candidate.AccountName,
				"keyFingerprint": event.Item.Candidate.KeyFingerprint,
				"attemptCount":   event.AttemptIndex + 1,
			}, "账户内 API Key 复测重试已用尽，本轮保留冷却状态等待下个周期")
		},
	)
	return runner
}

// Enqueue 等价 enqueueAccountApiKeyCooldownRetest（key = accountId:keyFingerprint）。
func (r *CooldownRetestRunner) Enqueue(candidate CooldownProbeCandidate, maxRecoveryHours int) bool {
	return r.queue.Enqueue(candidate.AccountID+":"+candidate.KeyFingerprint, CooldownQueueItem{
		Candidate:        candidate,
		MaxRecoveryHours: maxRecoveryHours,
	})
}

// Snapshot 队列快照。
func (r *CooldownRetestRunner) Snapshot() RetryQueueSnapshot { return r.queue.Snapshot() }

// StopAndDrain 停机排空（Node stopAccountApiKeyCooldownRetestQueue）。
func (r *CooldownRetestRunner) StopAndDrain(timeout time.Duration) (bool, int) {
	return r.queue.StopAndDrain(timeout)
}

// Scan 是 runAccountApiKeyCooldownRetest 的移植：按剩余队列槽位圈定候选
// 并入队；无槽位直接返回。
func (r *CooldownRetestRunner) Scan(ctx context.Context) error {
	maxRecoveryHours := r.settings("cooldownAccountRetestMaxBackoffHours", CooldownRetestMaxBackoffMinHours, CooldownRetestMaxBackoffMaxHours)
	queueBeforeScan := r.queue.Snapshot()
	queueConcurrency := defaultConcurrency(r.concurrency)
	availableQueueSlots := queueConcurrency - queueBeforeScan.RunningCount - queueBeforeScan.PendingCount
	if availableQueueSlots < 0 {
		availableQueueSlots = 0
	}
	if availableQueueSlots == 0 {
		return nil
	}
	limit := r.batchSize
	if availableQueueSlots < limit {
		limit = availableQueueSlots
	}
	candidates, err := r.candidates.ListDueForProbe(ctx, limit)
	if err != nil {
		return err
	}
	startedAt := r.clock.Now()
	enqueuedCount := 0
	skippedQueuedCount := 0
	for _, candidate := range candidates {
		if r.Enqueue(candidate, maxRecoveryHours) {
			enqueuedCount++
		} else {
			skippedQueuedCount++
		}
	}
	if len(candidates) > 0 {
		queue := r.queue.Snapshot()
		fields := map[string]any{
			"candidateCount":         len(candidates),
			"enqueuedCount":          enqueuedCount,
			"skippedQueuedCount":     skippedQueuedCount,
			"retryQueueConcurrency":  queueConcurrency,
			"retryQueuePendingCount": queue.PendingCount,
			"retryQueueRunningCount": queue.RunningCount,
			"elapsedMs":              r.clock.Now().Sub(startedAt).Milliseconds(),
		}
		if queue.NextRunAt != "" {
			fields["retryQueueNextRunAt"] = queue.NextRunAt
		}
		r.logger.Info("background_account_api_key_cooldown_retest_completed", fields, "账户内 API Key 复测候选已加入异步队列")
	}
	return nil
}

// runQueueItem 是 runAccountApiKeyCooldownRetestQueueItem 的移植。
func (r *CooldownRetestRunner) runQueueItem(ctx context.Context, runCtx QueueRunContext, item CooldownQueueItem) (bool, error) {
	candidate := item.Candidate
	account, err := r.reader.FindAccountForTest(ctx, candidate.AccountID)
	if err != nil {
		return false, err
	}
	if account == nil || account.Type != "api_key" || !isAccountStatusEligibleForRecoveryProbe(account.Status) || !account.Schedulable || account.BoundGroupID == "" {
		r.logger.Debug("background_account_api_key_cooldown_retest_discarded", map[string]any{
			"accountId":      candidate.AccountID,
			"accountName":    candidate.AccountName,
			"keyFingerprint": candidate.KeyFingerprint,
			"attemptIndex":   runCtx.AttemptIndex,
			"accountStatus":  accountStatusOrEmpty(account),
			"boundGroupId":   accountBoundGroupOrEmpty(account),
		}, "账户内 API Key 复测任务已失效，跳过队列项")
		return true, nil
	}

	systemAccountID := account.OwnerSystemAccountID
	if systemAccountID == "" {
		systemAccountID = account.SystemAccountID
	}
	if systemAccountID == "" {
		return true, nil
	}
	candidateAccount, err := r.reader.FindAccountForGroup(ctx, account.BoundGroupID, account.ID, systemAccountID)
	if err != nil {
		return false, err
	}
	if candidateAccount == nil || candidateAccount.Type != "api_key" {
		return true, nil
	}
	keyStillPresent, err := r.reader.HasAPIKeyEntry(ctx, candidateAccount, candidate.KeyFingerprint, candidate.APIKey)
	if err != nil {
		return false, err
	}
	if !keyStillPresent {
		r.logger.Debug("background_account_api_key_cooldown_retest_stale_credential_discarded", map[string]any{
			"accountId":      account.ID,
			"accountName":    account.Name,
			"keyFingerprint": candidate.KeyFingerprint,
			"attemptIndex":   0,
		}, "账户内 API Key 复测凭据已轮换，已丢弃旧队列项")
		return true, nil
	}

	observation, probeErr := r.prober.Probe(ctx, ProbeRequest{
		AccountID:           account.ID,
		SystemAccountID:     systemAccountID,
		GroupID:             account.BoundGroupID,
		TrafficSource:       "cooldown_retest",
		Full:                false,
		FixedAPIKey:         candidate.APIKey,
		FixedKeyFingerprint: candidate.KeyFingerprint,
		FixedKeyIndex:       candidate.KeyIndex,
	})
	if probeErr != nil {
		r.logger.Warn("background_account_api_key_cooldown_retest_attempt_failed", map[string]any{
			"accountId":      account.ID,
			"accountName":    account.Name,
			"keyFingerprint": candidate.KeyFingerprint,
			"failedKeyCount": 1,
		}, "账户内 API Key 复测调用异常，已保留 Key 状态")
		return false, fmt.Errorf("账户 %s 的 API Key 复测存在调用异常: %w", account.ID, probeErr)
	}
	if observation == nil {
		r.logger.Warn("background_account_api_key_cooldown_retest_missing_diagnostic_result", map[string]any{
			"accountId":      account.ID,
			"accountName":    account.Name,
			"keyFingerprint": candidate.KeyFingerprint,
		}, "账户内 API Key 复测没有返回诊断结果，已保留 Key 状态")
		return true, nil
	}

	result := observation.Result
	probeOutcome := AutomaticProbeOutcome(result, observation.Evidence)
	// 响应观测时刻（Node：额度 hint 与恢复窗口由此时刻定义）。
	responseObservedAt := r.clock.Now()
	responseObservedAtIso := FormatMillis(responseObservedAt)
	decision := ResolveQuotaRetestDecision(result, observation.Evidence, candidateAccount, candidate.LastErrorCode, candidate.RecoveryStartedAt,
		account.ID+":"+strings.TrimSpace(nonEmpty(candidate.KeyFingerprint, "account")), responseObservedAt)
	quotaFailure := decision.QuotaFailure
	quotaStatusCode := decision.StatusCode
	upstreamMessage := ""
	if quotaFailure {
		upstreamMessage = decision.Message
	}
	previousQuotaRecoveryMode := decision.PreviousRecoveryMode
	quotaRecoveryMode := decision.RecoveryMode
	expected := KeyMutationExpected{
		Status:                candidate.Status,
		NextProbeAt:           candidate.NextProbeAt,
		StateUpdatedAt:        candidate.StateUpdatedAt,
		ProbeClaimToken:       candidate.ProbeClaimToken,
		AccountConfigRevision: candidate.AccountConfigRevision,
	}

	if probeOutcome == OutcomeCompleteSuccess {
		restored, err := r.mutation.RecordKeySuccess(ctx, KeySuccessInput{
			AccountID:      account.ID,
			KeyFingerprint: candidate.KeyFingerprint,
			KeyIndex:       candidate.KeyIndex,
			TrafficSource:  "cooldown_retest",
			ProbeOutcome:   "complete_success",
			ObservedAt:     responseObservedAtIso,
			Expected:       expected,
		})
		if err != nil {
			return false, err
		}
		r.logger.Info("background_account_api_key_cooldown_retest_restored", map[string]any{
			"accountId":      account.ID,
			"accountName":    account.Name,
			"keyFingerprint": candidate.KeyFingerprint,
			"attemptIndex":   runCtx.AttemptIndex,
			"retryNumber":    runCtx.RetryNumber,
			"statusCode":     statusCodeOrNil(result.StatusCode),
			"durationMs":     result.DurationMs,
			"restored":       restored.Changed,
		}, "账户内 API Key 复测通过，Key 已恢复可调度")
		return true, nil
	}

	if quotaFailure && decision.HasRecoveryMode {
		timedOut := decision.TimedOut
		status := AccountStatusRateLimited
		errorCode := QuotaRecoveryErrorCode(quotaRecoveryMode)
		cooldownUntil := decision.CooldownUntil
		if timedOut {
			status = AccountStatusError
			errorCode = QuotaRecoveryTimeoutErrorCode
			cooldownUntil = ""
		}
		failure, err := r.mutation.RecordKeyFailure(ctx, KeyFailureInput{
			AccountID:         account.ID,
			KeyFingerprint:    candidate.KeyFingerprint,
			KeyIndex:          candidate.KeyIndex,
			TrafficSource:     "cooldown_retest",
			ProbeOutcome:      string(probeOutcome),
			QuotaRecoveryMode: string(quotaRecoveryMode),
			Status:            status,
			StatusCode:        quotaStatusCode,
			ErrorCode:         errorCode,
			ErrorMessage:      firstNonEmpty(upstreamMessage, result.Message),
			CooldownUntil:     cooldownUntil,
			TraceID:           result.TraceID,
			ObservedAt:        responseObservedAtIso,
			Expected:          expected,
		})
		if err != nil {
			return false, err
		}
		event := "background_account_api_key_quota_retest_failed"
		message := "API Key 额度仍不足，已按通用恢复间隔等待下次复测"
		if timedOut {
			event = "background_account_api_key_quota_recovery_timeout"
			message = "API Key 额度连续确认失败已达到 30 天，进入人工恢复的异常状态"
		} else if quotaRecoveryMode == QuotaRecoveryExplicitReset {
			message = "API Key 额度仍不足，已严格按上游恢复时间等待下次复测"
		} else if previousQuotaRecoveryMode == QuotaRecoveryExplicitReset {
			message = "API Key 当前未提供恢复时间，已切换通用 30 天额度观察窗口"
		}
		r.logger.Info(event, map[string]any{
			"accountId":         account.ID,
			"accountName":       account.Name,
			"keyFingerprint":    candidate.KeyFingerprint,
			"quotaRecoveryMode": string(quotaRecoveryMode),
			"status":            status,
			"statusCode":        quotaStatusCode,
			"errorCode":         result.ErrorCode,
			"probeOutcome":      string(probeOutcome),
			"durationMs":        result.DurationMs,
			"changed":           failure.Changed,
		}, message)
		return true, nil
	}

	if probeOutcome != OutcomeUpstreamFailure {
		delaySeconds := CooldownDefaultDeferSeconds
		if decision.HasRecoveryMode {
			delaySeconds = quotaRecoveryDelaySeconds(candidateAccount, candidate.KeyFingerprint, candidate.RecoveryStartedAt, responseObservedAt)
		}
		deferred, err := r.mutation.DeferKeyProbe(ctx, KeyDeferInput{
			AccountID:                account.ID,
			KeyFingerprint:           candidate.KeyFingerprint,
			KeyIndex:                 candidate.KeyIndex,
			TrafficSource:            "cooldown_retest",
			ProbeOutcome:             string(probeOutcome),
			QuotaRecoveryMode:        optionalMode(decision.RecoveryMode, decision.HasRecoveryMode),
			DelaySeconds:             delaySeconds,
			BreakQuotaRecoveryWindow: decision.HasPreviousMode,
			ObservedAt:               responseObservedAtIso,
			Expected:                 expected,
		})
		if err != nil {
			return false, err
		}
		r.logger.Warn("background_account_api_key_cooldown_retest_task_failed", map[string]any{
			"accountId":      account.ID,
			"accountName":    account.Name,
			"keyFingerprint": candidate.KeyFingerprint,
			"attemptIndex":   runCtx.AttemptIndex,
			"retryNumber":    runCtx.RetryNumber,
			"probeOutcome":   string(probeOutcome),
			"durationMs":     result.DurationMs,
			"deferred":       deferred.Changed,
			"message":        result.Message,
		}, "账户内 API Key 复测未形成传输失败证据，已保留 Key 状态")
		return true, nil
	}

	if decision.HasRecoveryMode {
		deferred, err := r.mutation.DeferKeyProbe(ctx, KeyDeferInput{
			AccountID:                account.ID,
			KeyFingerprint:           candidate.KeyFingerprint,
			KeyIndex:                 candidate.KeyIndex,
			TrafficSource:            "cooldown_retest",
			ProbeOutcome:             string(probeOutcome),
			QuotaRecoveryMode:        string(quotaRecoveryMode),
			DelaySeconds:             quotaRecoveryDelaySeconds(candidateAccount, candidate.KeyFingerprint, candidate.RecoveryStartedAt, responseObservedAt),
			BreakQuotaRecoveryWindow: decision.HasPreviousMode,
			ObservedAt:               responseObservedAtIso,
			Expected:                 expected,
		})
		if err != nil {
			return false, err
		}
		r.logger.Warn("background_account_api_key_quota_retest_transport_deferred", map[string]any{
			"accountId":      account.ID,
			"accountName":    account.Name,
			"keyFingerprint": candidate.KeyFingerprint,
			"probeOutcome":   string(probeOutcome),
			"deferred":       deferred.Changed,
		}, "API Key 额度复测未形成有效额度结论，按通用间隔顺延且不累计 30 天确认失败")
		return true, nil
	}

	failure, err := r.mutation.RecordKeyFailure(ctx, KeyFailureInput{
		AccountID:                account.ID,
		TrafficSource:            "cooldown_retest",
		ProbeOutcome:             "upstream_failure",
		Status:                   AccountStatusTemporaryUnavail,
		StatusCode:               quotaStatusCode,
		ErrorCode:                result.ErrorCode,
		ErrorMessage:             firstNonEmpty(upstreamMessage, result.Message),
		TraceID:                  result.TraceID,
		BreakQuotaRecoveryWindow: decision.HasPreviousMode,
		ObservedAt:               responseObservedAtIso,
		Expected:                 expected,
	})
	if err != nil {
		return false, err
	}
	r.logger.Debug("background_account_api_key_cooldown_retest_failed", map[string]any{
		"accountId":      account.ID,
		"accountName":    account.Name,
		"keyFingerprint": candidate.KeyFingerprint,
		"attemptIndex":   0,
		"retryNumber":    1,
		"statusCode":     statusCodeOrNil(result.StatusCode),
		"errorCode":      result.ErrorCode,
		"probeOutcome":   string(probeOutcome),
		"durationMs":     result.DurationMs,
		"changed":        failure.Changed,
		"message":        result.Message,
	}, "账户内 API Key 复测未通过，已按 Key 运行态退避等待下次复测")
	return true, nil
}

// quotaRecoveryDelaySeconds 是 quotaRecoveryDelaySeconds 的移植：
// max(60, ceil((genericUntil - now)/1000))。
func quotaRecoveryDelaySeconds(candidate *OpenAIAccountCandidate, keyFingerprint, recoveryStartedAt string, now time.Time) int {
	var policy *QuotaRecoveryPolicy
	if candidate != nil && len(candidate.QuotaRecoveryPolicy) > 0 {
		if normalized, err := NormalizeQuotaRecoveryPolicy(candidate.QuotaRecoveryPolicy); err == nil {
			policy = &normalized
		}
	}
	seed := candidateAccountSeed(candidate, keyFingerprint)
	untilText := GenericAPIKeyQuotaCooldownUntil(policy, seed, now)
	until, err := time.Parse(time.RFC3339, untilText)
	if err != nil {
		return CooldownQuotaMinDeferSeconds
	}
	seconds := int64(math.Ceil(until.Sub(now).Seconds()))
	if seconds < CooldownQuotaMinDeferSeconds {
		seconds = CooldownQuotaMinDeferSeconds
	}
	return int(seconds)
}

func candidateAccountSeed(candidate *OpenAIAccountCandidate, keyFingerprint string) string {
	id := ""
	if candidate != nil {
		id = candidate.ID
	}
	return id + ":" + strings.TrimSpace(nonEmpty(keyFingerprint, "account"))
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// optionalMode 等价 Node 的条件展开：`...(quotaRecoveryMode ? {...} : {})`。
func optionalMode(mode QuotaRecoveryMode, ok bool) string {
	if !ok {
		return ""
	}
	return string(mode)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
