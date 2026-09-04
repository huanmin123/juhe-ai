package gatewayproxyhealth

import (
	"container/list"
	"context"
	"encoding/json"
	"sort"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Ports the recording half of runtime/proxy-health.service.ts.

// suppressMutation is the void mutation result used by the suppress path.
type suppressMutation struct{}

type keyFailureOutcome struct {
	bucketKey            string
	suspected            bool
	distinctAccountCount int64
	opened               bool
	halfOpenProbeFailed  bool
	avoidUntilMs         *int64
}

// RecordGatewayUpstreamBucketFailure mirrors recordGatewayUpstreamBucketFailure.
func (s *ProxyHealthService) RecordGatewayUpstreamBucketFailure(
	account gatewayruntimecache.OpenAIAccountSecret,
	reason string,
	options FailureRecordOptions,
) GatewayProxyFailureDecision {
	bucketKeys := GatewayUpstreamBucketKeys(account, options.BucketScope)
	if len(bucketKeys) == 0 {
		return GatewayProxyFailureDecision{Recorded: false}
	}
	decisions := make([]int64, 0, len(bucketKeys))
	suspectedKeys := make([]string, 0, len(bucketKeys))
	for _, key := range bucketKeys {
		decision := s.recordGatewayUpstreamBucketFailureKey(account, key, reason)
		decisions = append(decisions, decision.distinctAccountCount)
		if decision.suspected {
			suspectedKeys = append(suspectedKeys, decision.bucketKey)
		}
	}
	return failureDecisionFromKeys(bucketKeys, suspectedKeys, decisions)
}

// RecordGatewayUpstreamBucketFailureAsync mirrors recordGatewayUpstreamBucketFailureAsync.
func (s *ProxyHealthService) RecordGatewayUpstreamBucketFailureAsync(
	ctx context.Context,
	account gatewayruntimecache.OpenAIAccountSecret,
	reason string,
	options FailureRecordOptions,
) (GatewayProxyFailureDecision, error) {
	if !s.shouldUseRedis() {
		return s.RecordGatewayUpstreamBucketFailure(account, reason, options), nil
	}
	bucketKeys := GatewayUpstreamBucketKeys(account, options.BucketScope)
	if len(bucketKeys) == 0 {
		return GatewayProxyFailureDecision{Recorded: false}, nil
	}
	decisions := make([]int64, 0, len(bucketKeys))
	suspectedKeys := make([]string, 0, len(bucketKeys))
	for _, key := range bucketKeys {
		outcome, err := s.recordGatewayUpstreamBucketFailureKeyAsync(ctx, account, key, reason)
		if err != nil {
			return GatewayProxyFailureDecision{}, err
		}
		decisions = append(decisions, outcome.distinctAccountCount)
		if outcome.suspected {
			suspectedKeys = append(suspectedKeys, outcome.bucketKey)
		}
	}
	return failureDecisionFromKeys(bucketKeys, suspectedKeys, decisions), nil
}

// RecordGatewayProxyFailure mirrors recordGatewayProxyFailure (scope defaults
// to 'proxy').
func (s *ProxyHealthService) RecordGatewayProxyFailure(
	account gatewayruntimecache.OpenAIAccountSecret,
	reason string,
	options FailureRecordOptions,
) GatewayProxyFailureDecision {
	return s.RecordGatewayUpstreamBucketFailure(account, reason, FailureRecordOptions{BucketScope: options.scopeOrProxy()})
}

// RecordGatewayProxyFailureAsync mirrors recordGatewayProxyFailureAsync.
func (s *ProxyHealthService) RecordGatewayProxyFailureAsync(
	ctx context.Context,
	account gatewayruntimecache.OpenAIAccountSecret,
	reason string,
	options FailureRecordOptions,
) (GatewayProxyFailureDecision, error) {
	return s.RecordGatewayUpstreamBucketFailureAsync(ctx, account, reason, FailureRecordOptions{BucketScope: options.scopeOrProxy()})
}

func (o FailureRecordOptions) scopeOrProxy() GatewayUpstreamBucketScope {
	if o.BucketScope == "" {
		return BucketScopeProxy
	}
	return o.BucketScope
}

func failureDecisionFromKeys(bucketKeys []string, suspectedKeys []string, distinctCounts []int64) GatewayProxyFailureDecision {
	var proxyKeyPtr *string
	for _, key := range bucketKeys {
		if isProxyBucketKey(key) {
			proxyKey := bucketKeyForLog(key)
			proxyKeyPtr = &proxyKey
			break
		}
	}
	distinct := int64(0)
	for _, count := range distinctCounts {
		if count > distinct {
			distinct = count
		}
	}
	return GatewayProxyFailureDecision{
		Recorded:             true,
		ProxyKey:             proxyKeyPtr,
		BucketKeys:           bucketKeysForLog(bucketKeys),
		SuspectedBucketKeys:  bucketKeysForLog(suspectedKeys),
		Suspected:            len(suspectedKeys) > 0,
		DistinctAccountCount: &distinct,
	}
}

// recordGatewayUpstreamBucketFailureKey mirrors the memory-driver key path.
func (s *ProxyHealthService) recordGatewayUpstreamBucketFailureKey(
	account gatewayruntimecache.OpenAIAccountSecret,
	key string,
	reason string,
) keyFailureOutcome {
	now := s.nowMs()
	current, currentOK := s.getMemoryEntry(key)
	var currentPtr *upstreamBucketFailureEntry
	if currentOK {
		currentCopy := current
		currentPtr = &currentCopy
	}
	accountSamples := pruneAccountSamples(
		append(append([]AccountSample(nil), current.AccountSamples...),
			AccountSample{AccountID: gatewayFailureEvidenceAccountID(account), FailedAtMs: now}),
		now, s.opts.FailureWindowMs, s.opts.MaxAccountSamples)
	distinctAccountCount := distinctSampleCount(accountSamples)
	halfOpenProbeFailed := isHalfOpenProbeForAccount(currentPtr, account.ID, now)
	suspected := halfOpenProbeFailed || distinctAccountCount >= int64(s.opts.DistinctAccountThreshold)
	entry := upstreamBucketFailureEntry{
		Key:             key,
		Reason:          reason,
		AccountSamples:  accountSamples,
		FailureCount:    current.FailureCount + 1,
		FirstFailedAtMs: firstFailedAtOrDefault(currentPtr, now),
		LastFailedAtMs:  now,
	}
	if suspected {
		entry.AvoidUntilMs = int64Ptr(maxInt64(avoidUntilOrDefault(currentPtr, 0), now+s.opts.AvoidTTLms))
	} else if currentPtr != nil {
		entry.AvoidUntilMs = currentPtr.AvoidUntilMs
	}
	s.setMemoryEntry(key, entry, s.opts.AvoidTTLms+s.opts.FailureWindowMs)
	if suspected && (currentPtr == nil || currentPtr.AvoidUntilMs == nil || *currentPtr.AvoidUntilMs <= now) {
		avoidUntil := now
		if entry.AvoidUntilMs != nil {
			avoidUntil = *entry.AvoidUntilMs
		}
		s.logWarn(map[string]any{
			"event":                "gateway_upstream_failure_bucket_opened",
			"bucketKey":            bucketKeyForLog(key),
			"bucketType":           upstreamBucketType(key),
			"accountId":            account.ID,
			"distinctAccountCount": distinctAccountCount,
			"reason":               reason,
			"halfOpenProbeFailed":  halfOpenProbeFailed,
			"avoidUntil":           ISOStringMs(avoidUntil),
		}, "同上游桶多个账号短窗失败，网关已进入上游桶运行态避让")
	}
	return keyFailureOutcome{
		bucketKey:            key,
		suspected:            suspected,
		distinctAccountCount: distinctAccountCount,
		halfOpenProbeFailed:  halfOpenProbeFailed,
		avoidUntilMs:         entry.AvoidUntilMs,
	}
}

// recordGatewayUpstreamBucketFailureKeyAsync mirrors the Redis-driver key path.
func (s *ProxyHealthService) recordGatewayUpstreamBucketFailureKeyAsync(
	ctx context.Context,
	account gatewayruntimecache.OpenAIAccountSecret,
	key string,
	reason string,
) (keyFailureOutcome, error) {
	now := s.nowMs()
	observation := s.nextObservation(now)
	_, _, outcome, err := mutateRedisBucketFailureEntry(ctx, s, key, func(current *upstreamBucketFailureEntry) (upstreamBucketFailureEntry, int64, keyFailureOutcome, error) {
		accountSamples := pruneAccountSamples(
			append(append([]AccountSample(nil), currentEntrySamples(current)...),
				AccountSample{AccountID: gatewayFailureEvidenceAccountID(account), FailedAtMs: now}),
			now, s.opts.FailureWindowMs, s.opts.MaxAccountSamples)
		distinctAccountCount := distinctSampleCount(accountSamples)
		halfOpenProbeFailed := isHalfOpenProbeForAccount(current, account.ID, now)
		suspected := halfOpenProbeFailed || distinctAccountCount >= int64(s.opts.DistinctAccountThreshold)
		latestFailure := latestGatewayUpstreamBucketFailureObservation(current, observation)
		entry := upstreamBucketFailureEntry{
			Key:                   key,
			Reason:                reason,
			AccountSamples:        accountSamples,
			FailureCount:          entryFailureCount(current),
			FirstFailedAtMs:       firstFailedAtOrDefault(current, now),
			LastFailedAtMs:        latestFailure.observedAtMs,
			LastFailureGeneration: latestFailure.generation,
		}
		if suspected {
			entry.AvoidUntilMs = int64Ptr(maxInt64(avoidUntilOrDefault(current, 0), now+s.opts.AvoidTTLms))
		} else if current != nil {
			entry.AvoidUntilMs = current.AvoidUntilMs
		}
		ttlMs := s.redisBucketFailureEntryTTLMs(&entry, now, s.opts.AvoidTTLms+s.opts.FailureWindowMs)
		opened := suspected && (current == nil || current.AvoidUntilMs == nil || *current.AvoidUntilMs <= now)
		return entry, ttlMs, keyFailureOutcome{
			bucketKey:            key,
			suspected:            suspected,
			distinctAccountCount: distinctAccountCount,
			opened:               opened,
			halfOpenProbeFailed:  halfOpenProbeFailed,
			avoidUntilMs:         entry.AvoidUntilMs,
		}, nil
	})
	if err != nil {
		return keyFailureOutcome{}, err
	}
	if outcome.opened {
		avoidUntil := now
		if outcome.avoidUntilMs != nil {
			avoidUntil = *outcome.avoidUntilMs
		}
		s.logWarn(map[string]any{
			"event":                "gateway_upstream_failure_bucket_opened",
			"bucketKey":            bucketKeyForLog(key),
			"bucketType":           upstreamBucketType(key),
			"accountId":            account.ID,
			"distinctAccountCount": outcome.distinctAccountCount,
			"reason":               reason,
			"halfOpenProbeFailed":  outcome.halfOpenProbeFailed,
			"avoidUntil":           ISOStringMs(avoidUntil),
		}, "同上游桶多个账号短窗失败，网关已进入上游桶运行态避让")
	}
	return outcome, nil
}

// mutateRedisBucketFailureEntry mirrors mutateRedisBucketFailureEntry with the
// CAS retry loop. mutation receives the current entry (nil when absent) and
// returns the next entry, its TTL and the caller result.
func mutateRedisBucketFailureEntry[TResult any](
	ctx context.Context,
	s *ProxyHealthService,
	key string,
	mutation func(current *upstreamBucketFailureEntry) (entry upstreamBucketFailureEntry, ttlMs int64, result TResult, err error),
) (*upstreamBucketFailureEntry, upstreamBucketFailureEntry, TResult, error) {
	var zeroResult TResult
	current, err := s.getRedisBucketFailureEntry(ctx, key)
	if err != nil {
		return nil, upstreamBucketFailureEntry{}, zeroResult, err
	}
	for attempt := 0; attempt < s.opts.CASMaxAttempts; attempt++ {
		var currentPtr *upstreamBucketFailureEntry
		if current != nil {
			currentPtr = &current.entry
		}
		nextEntry, ttlMs, result, err := mutation(currentPtr)
		if err != nil {
			return nil, upstreamBucketFailureEntry{}, zeroResult, err
		}
		var expected json.RawMessage
		if current != nil {
			expected = current.raw
		}
		applied, err := s.stateStore.CompareSetJSON(ctx, redisBucketStateKey(key), expected, nextEntry, ttlMs)
		if err != nil {
			return nil, upstreamBucketFailureEntry{}, zeroResult, err
		}
		if applied {
			return currentPtr, nextEntry, result, nil
		}
		current, err = s.getRedisBucketFailureEntry(ctx, key)
		if err != nil {
			return nil, upstreamBucketFailureEntry{}, zeroResult, err
		}
	}
	return nil, upstreamBucketFailureEntry{}, zeroResult, redisBucketCASExhaustedError(key, "mutation", s.opts.CASMaxAttempts)
}

// SuppressGatewayUpstreamBucketLocallyForSeconds mirrors
// suppressGatewayUpstreamBucketLocallyForSeconds (memory driver).
func (s *ProxyHealthService) SuppressGatewayUpstreamBucketLocallyForSeconds(
	account gatewayruntimecache.OpenAIAccountSecret,
	ttlSeconds int64,
	reason string,
	options FailureRecordOptions,
) GatewayProxyFailureDecision {
	bucketKeys := GatewayUpstreamBucketKeys(account, options.BucketScope)
	if len(bucketKeys) == 0 {
		return GatewayProxyFailureDecision{Recorded: false}
	}
	now := s.nowMs()
	normalizedTtlSeconds := maxInt64(1, ttlSeconds)
	ttlMs := normalizedTtlSeconds * 1000
	avoidUntilMs := now + ttlMs
	effectiveAvoidUntilMs := avoidUntilMs
	for _, key := range bucketKeys {
		current, currentOK := s.getMemoryEntry(key)
		var currentPtr *upstreamBucketFailureEntry
		if currentOK {
			currentCopy := current
			currentPtr = &currentCopy
		}
		accountSamples := pruneAccountSamples(
			append(append([]AccountSample(nil), current.AccountSamples...),
				AccountSample{AccountID: gatewayFailureEvidenceAccountID(account), FailedAtMs: now}),
			now, s.opts.FailureWindowMs, s.opts.MaxAccountSamples)
		entry := upstreamBucketFailureEntry{
			Key:             key,
			Reason:          reason,
			AccountSamples:  accountSamples,
			FailureCount:    current.FailureCount + 1,
			FirstFailedAtMs: firstFailedAtOrDefault(currentPtr, now),
			LastFailedAtMs:  now,
			AvoidUntilMs:    int64Ptr(maxInt64(avoidUntilOrDefault(currentPtr, 0), avoidUntilMs)),
		}
		effectiveAvoidUntilMs = maxInt64(effectiveAvoidUntilMs, *entry.AvoidUntilMs)
		s.setMemoryEntry(key, entry, ttlMs+s.opts.FailureWindowMs)
	}
	proxyKeyPtr := findProxyKeyForLog(bucketKeys)
	safeBucketKeys := bucketKeysForLog(bucketKeys)
	s.logWarn(map[string]any{
		"event":      "gateway_upstream_bucket_locally_suppressed",
		"bucketKeys": safeBucketKeys,
		"proxyKey":   proxyKeyPtr,
		"accountId":  account.ID,
		"ttlSeconds": normalizedTtlSeconds,
		"avoidUntil": ISOStringMs(effectiveAvoidUntilMs),
		"reason":     reason,
	}, "网关按策略短期避让上游桶")
	return GatewayProxyFailureDecision{
		Recorded:             true,
		ProxyKey:             proxyKeyPtr,
		BucketKeys:           safeBucketKeys,
		SuspectedBucketKeys:  safeBucketKeys,
		Suspected:            true,
		DistinctAccountCount: int64Ptr(1),
	}
}

// SuppressGatewayUpstreamBucketForSecondsAsync mirrors
// suppressGatewayUpstreamBucketForSecondsAsync (Redis driver).
func (s *ProxyHealthService) SuppressGatewayUpstreamBucketForSecondsAsync(
	ctx context.Context,
	account gatewayruntimecache.OpenAIAccountSecret,
	ttlSeconds int64,
	reason string,
	options FailureRecordOptions,
) (GatewayProxyFailureDecision, error) {
	if !s.shouldUseRedis() {
		return s.SuppressGatewayUpstreamBucketLocallyForSeconds(account, ttlSeconds, reason, options), nil
	}
	bucketKeys := GatewayUpstreamBucketKeys(account, options.BucketScope)
	if len(bucketKeys) == 0 {
		return GatewayProxyFailureDecision{Recorded: false}, nil
	}
	now := s.nowMs()
	observation := s.nextObservation(now)
	normalizedTtlSeconds := maxInt64(1, ttlSeconds)
	ttlMs := normalizedTtlSeconds * 1000
	avoidUntilMs := now + ttlMs
	effectiveAvoidUntilMs := avoidUntilMs
	for _, key := range bucketKeys {
		_, entry, _, err := mutateRedisBucketFailureEntry(ctx, s, key, func(current *upstreamBucketFailureEntry) (upstreamBucketFailureEntry, int64, suppressMutation, error) {
			accountSamples := pruneAccountSamples(
				append(append([]AccountSample(nil), currentEntrySamples(current)...),
					AccountSample{AccountID: gatewayFailureEvidenceAccountID(account), FailedAtMs: now}),
				now, s.opts.FailureWindowMs, s.opts.MaxAccountSamples)
			latestFailure := latestGatewayUpstreamBucketFailureObservation(current, observation)
			entry := upstreamBucketFailureEntry{
				Key:                   key,
				Reason:                reason,
				AccountSamples:        accountSamples,
				FailureCount:          entryFailureCount(current),
				FirstFailedAtMs:       firstFailedAtOrDefault(current, now),
				LastFailedAtMs:        latestFailure.observedAtMs,
				LastFailureGeneration: latestFailure.generation,
				AvoidUntilMs:          int64Ptr(maxInt64(avoidUntilOrDefault(current, 0), avoidUntilMs)),
			}
			ttl := s.redisBucketFailureEntryTTLMs(&entry, now, ttlMs+s.opts.FailureWindowMs)
			return entry, ttl, suppressMutation{}, nil
		})
		if err != nil {
			return GatewayProxyFailureDecision{}, err
		}
		if entry.AvoidUntilMs != nil {
			effectiveAvoidUntilMs = maxInt64(effectiveAvoidUntilMs, *entry.AvoidUntilMs)
		}
	}
	proxyKeyPtr := findProxyKeyForLog(bucketKeys)
	safeBucketKeys := bucketKeysForLog(bucketKeys)
	s.logWarn(map[string]any{
		"event":      "gateway_upstream_bucket_locally_suppressed",
		"bucketKeys": safeBucketKeys,
		"proxyKey":   proxyKeyPtr,
		"accountId":  account.ID,
		"ttlSeconds": normalizedTtlSeconds,
		"avoidUntil": ISOStringMs(effectiveAvoidUntilMs),
		"reason":     reason,
	}, "网关按策略短期避让上游桶")
	return GatewayProxyFailureDecision{
		Recorded:             true,
		ProxyKey:             proxyKeyPtr,
		BucketKeys:           safeBucketKeys,
		SuspectedBucketKeys:  safeBucketKeys,
		Suspected:            true,
		DistinctAccountCount: int64Ptr(1),
	}, nil
}

func findProxyKeyForLog(bucketKeys []string) *string {
	for _, key := range bucketKeys {
		if isProxyBucketKey(key) {
			proxyKey := bucketKeyForLog(key)
			return &proxyKey
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Success cleanup.

// RecordGatewayUpstreamBucketSuccess mirrors recordGatewayUpstreamBucketSuccess.
func (s *ProxyHealthService) RecordGatewayUpstreamBucketSuccess(
	account gatewayruntimecache.OpenAIAccountSecret,
	options FailureRecordOptions,
) bool {
	existed := false
	for _, key := range GatewayUpstreamBucketKeys(account, options.BucketScope) {
		if _, ok := s.getMemoryEntry(key); ok {
			existed = true
		}
		s.mu.Lock()
		s.deleteMemoryEntryLocked(key)
		s.mu.Unlock()
	}
	return existed
}

// RecordGatewayUpstreamBucketSuccessAsync mirrors the Redis variant.
func (s *ProxyHealthService) RecordGatewayUpstreamBucketSuccessAsync(
	ctx context.Context,
	account gatewayruntimecache.OpenAIAccountSecret,
	options FailureRecordOptions,
) (bool, error) {
	if !s.shouldUseRedis() {
		return s.RecordGatewayUpstreamBucketSuccess(account, options), nil
	}
	successObservation := s.nextObservation(s.nowMs())
	cleared := false
	for _, key := range GatewayUpstreamBucketKeys(account, options.BucketScope) {
		keyCleared, err := s.clearRedisBucketAfterSuccessObservation(ctx, key, successObservation)
		if err != nil {
			return false, err
		}
		cleared = keyCleared || cleared
	}
	return cleared, nil
}

// RecordGatewayProxySuccess mirrors recordGatewayProxySuccess.
func (s *ProxyHealthService) RecordGatewayProxySuccess(account gatewayruntimecache.OpenAIAccountSecret) bool {
	return s.RecordGatewayUpstreamBucketSuccess(account, FailureRecordOptions{BucketScope: BucketScopeProxy})
}

// RecordGatewayProxySuccessAsync mirrors recordGatewayProxySuccessAsync.
func (s *ProxyHealthService) RecordGatewayProxySuccessAsync(ctx context.Context, account gatewayruntimecache.OpenAIAccountSecret) (bool, error) {
	return s.RecordGatewayUpstreamBucketSuccessAsync(ctx, account, FailureRecordOptions{BucketScope: BucketScopeProxy})
}

func (s *ProxyHealthService) clearRedisBucketAfterSuccessObservation(
	ctx context.Context,
	key string,
	successObservation upstreamBucketMutationObservation,
) (bool, error) {
	current, err := s.getRedisBucketFailureEntry(ctx, key)
	if err != nil {
		return false, err
	}
	if current == nil || gatewayUpstreamBucketFailureOccurredAfterObservation(current.entry, successObservation) {
		return false, nil
	}
	observedFailureEvidence := current.entry
	for attempt := 0; attempt < s.opts.CASMaxAttempts; attempt++ {
		applied, err := s.stateStore.CompareDeleteJSON(ctx, redisBucketStateKey(key), current.raw)
		if err != nil {
			return false, err
		}
		if applied {
			return true, nil
		}
		current, err = s.getRedisBucketFailureEntry(ctx, key)
		if err != nil {
			return false, err
		}
		if current == nil {
			return false, nil
		}
		if gatewayUpstreamBucketFailureOccurredAfterObservation(current.entry, successObservation) ||
			!sameGatewayUpstreamBucketFailureEvidence(observedFailureEvidence, current.entry) {
			return false, nil
		}
	}
	return false, redisBucketCASExhaustedError(key, "success_cleanup", s.opts.CASMaxAttempts)
}

// ---------------------------------------------------------------------------
// Evidence helpers.

func currentEntrySamples(current *upstreamBucketFailureEntry) []AccountSample {
	if current == nil {
		return nil
	}
	return current.AccountSamples
}

func entryFailureCount(current *upstreamBucketFailureEntry) int64 {
	if current == nil {
		return 0
	}
	return current.FailureCount
}

func firstFailedAtOrDefault(current *upstreamBucketFailureEntry, now int64) int64 {
	if current != nil {
		return current.FirstFailedAtMs
	}
	return now
}

func avoidUntilOrDefault(current *upstreamBucketFailureEntry, fallback int64) int64 {
	if current != nil && current.AvoidUntilMs != nil {
		return *current.AvoidUntilMs
	}
	return fallback
}

func distinctSampleCount(samples []AccountSample) int64 {
	seen := make(map[string]struct{}, len(samples))
	for _, sample := range samples {
		seen[sample.AccountID] = struct{}{}
	}
	return int64(len(seen))
}

// isHalfOpenProbeForAccount mirrors isHalfOpenProbeForAccount.
func isHalfOpenProbeForAccount(entry *upstreamBucketFailureEntry, accountID string, now int64) bool {
	return entry != nil &&
		entry.HalfOpenAccountID != nil && *entry.HalfOpenAccountID == accountID &&
		entry.HalfOpenUntilMs != nil && *entry.HalfOpenUntilMs > now &&
		entry.AvoidUntilMs != nil && *entry.AvoidUntilMs <= now
}

// latestGatewayUpstreamBucketFailureObservation mirrors the same-named helper.
func latestGatewayUpstreamBucketFailureObservation(
	current *upstreamBucketFailureEntry,
	incoming upstreamBucketMutationObservation,
) upstreamBucketMutationObservation {
	if current == nil || current.LastFailedAtMs < incoming.observedAtMs {
		return incoming
	}
	if current.LastFailedAtMs > incoming.observedAtMs {
		return upstreamBucketMutationObservation{
			observedAtMs: current.LastFailedAtMs,
			generation:   validGatewayUpstreamBucketMutationGeneration(current.LastFailureGeneration),
		}
	}
	currentGeneration := validGatewayUpstreamBucketMutationGeneration(current.LastFailureGeneration)
	if currentGeneration != nil &&
		incoming.generation != nil &&
		currentGeneration.InstanceID == incoming.generation.InstanceID &&
		currentGeneration.Sequence > incoming.generation.Sequence {
		return upstreamBucketMutationObservation{observedAtMs: current.LastFailedAtMs, generation: currentGeneration}
	}
	return incoming
}

func gatewayUpstreamBucketFailureOccurredAfterObservation(
	entry upstreamBucketFailureEntry,
	observation upstreamBucketMutationObservation,
) bool {
	if entry.LastFailedAtMs > observation.observedAtMs {
		return true
	}
	if entry.LastFailedAtMs < observation.observedAtMs {
		return false
	}
	failureGeneration := validGatewayUpstreamBucketMutationGeneration(entry.LastFailureGeneration)
	if failureGeneration == nil || observation.generation == nil || failureGeneration.InstanceID != observation.generation.InstanceID {
		return true
	}
	return failureGeneration.Sequence > observation.generation.Sequence
}

func validGatewayUpstreamBucketMutationGeneration(value *upstreamBucketMutationGeneration) *upstreamBucketMutationGeneration {
	if value == nil || value.InstanceID == "" || value.Sequence <= 0 {
		return nil
	}
	return value
}

func sameGatewayUpstreamBucketFailureEvidence(left, right upstreamBucketFailureEntry) bool {
	return left.Key == right.Key &&
		left.Reason == right.Reason &&
		left.FailureCount == right.FailureCount &&
		left.FirstFailedAtMs == right.FirstFailedAtMs &&
		left.LastFailedAtMs == right.LastFailedAtMs &&
		avoidUntilEqual(left.AvoidUntilMs, right.AvoidUntilMs) &&
		sameGatewayUpstreamBucketMutationGeneration(left.LastFailureGeneration, right.LastFailureGeneration) &&
		sameGatewayUpstreamBucketAccountSamples(left.AccountSamples, right.AccountSamples)
}

func avoidUntilEqual(left, right *int64) bool {
	if left == nil && right == nil {
		return true
	}
	if left == nil || right == nil {
		return false
	}
	return *left == *right
}

func sameGatewayUpstreamBucketMutationGeneration(left, right *upstreamBucketMutationGeneration) bool {
	if left == nil && right == nil {
		return true
	}
	normalizedLeft := validGatewayUpstreamBucketMutationGeneration(left)
	normalizedRight := validGatewayUpstreamBucketMutationGeneration(right)
	return normalizedLeft != nil && normalizedRight != nil &&
		normalizedLeft.InstanceID == normalizedRight.InstanceID &&
		normalizedLeft.Sequence == normalizedRight.Sequence
}

func sameGatewayUpstreamBucketAccountSamples(left, right []AccountSample) bool {
	if len(left) != len(right) {
		return false
	}
	for index, sample := range left {
		if right[index].AccountID != sample.AccountID || right[index].FailedAtMs != sample.FailedAtMs {
			return false
		}
	}
	return true
}

// pruneAccountSamples mirrors pruneAccountSamples: window-filter, keep the
// latest sample per account (insertion order of first occurrence, later
// timestamps win), stable sort by time, keep the last maxSamples entries.
func pruneAccountSamples(samples []AccountSample, now int64, windowMs int64, maxSamples int) []AccountSample {
	type sampleEntry struct {
		value AccountSample
		order int
	}
	latestByAccountID := map[string]*sampleEntry{}
	ordered := make([]*sampleEntry, 0, len(samples))
	for _, sample := range samples {
		if now-sample.FailedAtMs > windowMs {
			continue
		}
		if previous, ok := latestByAccountID[sample.AccountID]; ok {
			if sample.FailedAtMs >= previous.value.FailedAtMs {
				previous.value = sample
			}
			continue
		}
		insert := &sampleEntry{value: sample, order: len(ordered)}
		latestByAccountID[sample.AccountID] = insert
		ordered = append(ordered, insert)
	}
	values := make([]AccountSample, 0, len(ordered))
	for _, item := range ordered {
		values = append(values, item.value)
	}
	sort.SliceStable(values, func(i, j int) bool {
		return values[i].FailedAtMs < values[j].FailedAtMs
	})
	if len(values) > maxSamples {
		values = values[len(values)-maxSamples:]
	}
	return values
}

// ClearForTest mirrors clearGatewayProxyHealthForTest (memory entries only;
// the clock is owned by the caller's Clock injection).
func (s *ProxyHealthService) ClearForTest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = map[string]*memoryBucketEntry{}
	s.order.Init()
	s.elementOf = map[string]*list.Element{}
}
