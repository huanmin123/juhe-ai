package gatewayhotquality

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Memory hot quality store mirroring
// backend/src/modules/gateway/runtime/hot-quality-memory-store.ts.
// Node relies on the single-threaded event loop; the Go port guards all state
// with a mutex while keeping the mutation order identical.

// MemoryHotQualityStoreOptions mirrors MemoryHotQualityStoreOptions; nil
// numeric pointers fall back to the Node defaults, a nil Now falls back to
// the wall clock.
type MemoryHotQualityStoreOptions struct {
	KeyCapacity     *int
	AttemptCapacity *int
	KeyTtlMs        *int64
	TerminalTtlMs   *int64
	Now             func() int64
}

type memoryHotQualityEntry struct {
	scopeKey    string
	scope       HotQualityScope
	buckets     [HotQualityMinuteBucketCount]*HotQualityBucketState
	expiresAtMs int64
}

type memoryAttemptIdentity struct {
	attemptId         string
	requestedScopeKey string
	effectiveScopeKey string
	effectiveScope    HotQualityScope
	expiresAtMs       int64
	terminal          *HotQualityTerminalRecord
}

// MemoryHotQualityStore mirrors MemoryHotQualityStore.
type MemoryHotQualityStore struct {
	mu                          sync.Mutex
	entries                     map[string]*memoryHotQualityEntry
	attempts                    map[string]*memoryAttemptIdentity
	terminalOutcomeAttempts     map[string]string
	keyCapacity                 int
	attemptCapacity             int
	keyTtlMs                    int64
	terminalTtlMs               int64
	now                         func() int64
	keyCreationRefusals         int64
	highCardinalityDegradations int64
	attemptCapacityRefusals     int64
	terminalQualityKeyMisses    int64
	nextCleanupAtMs             int64
}

// NewMemoryHotQualityStore mirrors the MemoryHotQualityStore constructor.
func NewMemoryHotQualityStore(options MemoryHotQualityStoreOptions) (*MemoryHotQualityStore, error) {
	keyCapacity := 10_000
	if options.KeyCapacity != nil {
		keyCapacity = *options.KeyCapacity
	}
	attemptCapacity := 100_000
	if options.AttemptCapacity != nil {
		attemptCapacity = *options.AttemptCapacity
	}
	keyTtlMs := HotQualityKeyTTLMS
	if options.KeyTtlMs != nil {
		keyTtlMs = *options.KeyTtlMs
	}
	terminalTtlMs := HotQualityTerminalTTLMS
	if options.TerminalTtlMs != nil {
		terminalTtlMs = *options.TerminalTtlMs
	}
	normalizedKeyCapacity, err := positiveIntegerInt(keyCapacity, "keyCapacity")
	if err != nil {
		return nil, err
	}
	normalizedAttemptCapacity, err := positiveIntegerInt(attemptCapacity, "attemptCapacity")
	if err != nil {
		return nil, err
	}
	normalizedKeyTtl, err := positiveIntegerInt64(keyTtlMs, "keyTtlMs")
	if err != nil {
		return nil, err
	}
	normalizedTerminalTtl, err := positiveIntegerInt64(terminalTtlMs, "terminalTtlMs")
	if err != nil {
		return nil, err
	}
	if normalizedTerminalTtl < HotQualityTerminalTTLMS {
		return nil, fmt.Errorf("terminalTtlMs 不得少于 %dms", HotQualityTerminalTTLMS)
	}
	now := options.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &MemoryHotQualityStore{
		entries:                 make(map[string]*memoryHotQualityEntry),
		attempts:                make(map[string]*memoryAttemptIdentity),
		terminalOutcomeAttempts: make(map[string]string),
		keyCapacity:             normalizedKeyCapacity,
		attemptCapacity:         normalizedAttemptCapacity,
		keyTtlMs:                normalizedKeyTtl,
		terminalTtlMs:           normalizedTerminalTtl,
		now:                     now,
	}, nil
}

// RecordAttempt mirrors recordAttempt.
func (store *MemoryHotQualityStore) RecordAttempt(ctx context.Context, input HotQualityRecordAttemptInput) (*HotQualityAttemptMutationResult, error) {
	now, err := normalizedNowMs(derefOrDefault(input.NowMs, store.now))
	if err != nil {
		return nil, err
	}
	attemptId, err := boundedIdentity(input.AttemptID, "attemptId")
	if err != nil {
		return nil, err
	}
	requestedScope, err := NormalizeHotQualityScope(input.Scope)
	if err != nil {
		return nil, err
	}
	requestedScopeKey, err := HotQualityScopeKey(requestedScope)
	if err != nil {
		return nil, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	store.cleanup(now)

	if existingAttempt := store.freshAttempt(attemptId, now); existingAttempt != nil {
		status := AttemptMutationIdempotent
		if existingAttempt.requestedScopeKey != requestedScopeKey {
			status = AttemptMutationAttemptConflict
		}
		return &HotQualityAttemptMutationResult{
			Status:         status,
			RequestedScope: requestedScope,
			EffectiveScope: CloneHotQualityScope(existingAttempt.effectiveScope),
		}, nil
	}
	if len(store.attempts) >= store.attemptCapacity {
		store.cleanup(now, true)
	}
	if len(store.attempts) >= store.attemptCapacity {
		store.attemptCapacityRefusals = incrementInt64(store.attemptCapacityRefusals)
		return &HotQualityAttemptMutationResult{
			Status:         AttemptMutationAttemptCapacityExhausted,
			RequestedScope: requestedScope,
			EffectiveScope: requestedScope,
		}, nil
	}

	resolved, err := store.resolveAttemptEntry(requestedScope, requestedScopeKey, now)
	if err != nil {
		return nil, err
	}
	if resolved == nil {
		store.keyCreationRefusals = incrementInt64(store.keyCreationRefusals)
		return &HotQualityAttemptMutationResult{
			Status:         AttemptMutationKeyCapacityExhausted,
			RequestedScope: requestedScope,
			EffectiveScope: requestedScope,
		}, nil
	}

	store.attempts[attemptId] = &memoryAttemptIdentity{
		attemptId:         attemptId,
		requestedScopeKey: requestedScopeKey,
		effectiveScopeKey: resolved.entry.scopeKey,
		effectiveScope:    CloneHotQualityScope(resolved.entry.scope),
		expiresAtMs:       expirationAt(now, store.terminalTtlMs),
	}
	bucket := currentMemoryBucket(resolved.entry, now)
	bucket.Attempts = incrementInt64(bucket.Attempts)
	resolved.entry.expiresAtMs = expirationAt(now, store.keyTtlMs)
	if resolved.degraded {
		store.highCardinalityDegradations = incrementInt64(store.highCardinalityDegradations)
	}
	status := AttemptMutationApplied
	if resolved.degraded {
		status = AttemptMutationDegradedToProtocol
	}
	return &HotQualityAttemptMutationResult{
		Status:         status,
		RequestedScope: requestedScope,
		EffectiveScope: CloneHotQualityScope(resolved.entry.scope),
	}, nil
}

// RecordTerminal mirrors recordTerminal.
func (store *MemoryHotQualityStore) RecordTerminal(ctx context.Context, input HotQualityRecordTerminalInput) (*HotQualityTerminalMutationResult, error) {
	now, err := normalizedNowMs(derefOrDefault(input.NowMs, store.now))
	if err != nil {
		return nil, err
	}
	attemptId, err := boundedIdentity(input.AttemptID, "attemptId")
	if err != nil {
		return nil, err
	}
	requestedScopeKey, err := HotQualityScopeKey(input.Scope)
	if err != nil {
		return nil, err
	}
	terminalOutcomeId, err := boundedIdentity(input.TerminalOutcomeID, "terminalOutcomeId")
	if err != nil {
		return nil, err
	}
	if err := assertOutcomeClass(input.OutcomeClass); err != nil {
		return nil, err
	}
	if err := assertFailureScope(input.FailureScope); err != nil {
		return nil, err
	}
	if err := assertTerminalSource(input.Source); err != nil {
		return nil, err
	}
	var firstByteMs *int64
	if input.FirstByteMs != nil {
		normalized, err := NormalizedFirstByteMs(*input.FirstByteMs)
		if err != nil {
			return nil, err
		}
		firstByteMs = &normalized
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	store.cleanup(now)
	attempt := store.freshAttempt(attemptId, now)
	if attempt == nil {
		return &HotQualityTerminalMutationResult{Status: TerminalMutationAttemptNotFound}, nil
	}
	if attempt.requestedScopeKey != requestedScopeKey {
		effectiveScope := CloneHotQualityScope(attempt.effectiveScope)
		return &HotQualityTerminalMutationResult{
			Status:         TerminalMutationAttemptConflict,
			EffectiveScope: &effectiveScope,
		}, nil
	}

	if attempt.terminal != nil {
		status := TerminalMutationTerminalConflict
		if sameTerminal(*attempt.terminal, terminalOutcomeId, input.OutcomeClass, input.FailureScope, input.Source) {
			status = TerminalMutationIdempotent
		}
		terminal := cloneTerminal(attempt.terminal)
		effectiveScope := CloneHotQualityScope(attempt.effectiveScope)
		return &HotQualityTerminalMutationResult{
			Status:         status,
			Terminal:       terminal,
			EffectiveScope: &effectiveScope,
		}, nil
	}
	terminalOwner, hasOwner := store.terminalOutcomeAttempts[terminalOutcomeId]
	if hasOwner && store.freshAttempt(terminalOwner, now) == nil {
		delete(store.terminalOutcomeAttempts, terminalOutcomeId)
		hasOwner = false
	}
	if hasOwner && terminalOwner != attemptId {
		effectiveScope := CloneHotQualityScope(attempt.effectiveScope)
		return &HotQualityTerminalMutationResult{
			Status:         TerminalMutationTerminalOutcomeConflict,
			EffectiveScope: &effectiveScope,
		}, nil
	}

	entry := store.entryForTerminal(attempt, now)
	if entry == nil {
		store.terminalQualityKeyMisses = incrementInt64(store.terminalQualityKeyMisses)
		effectiveScope := CloneHotQualityScope(attempt.effectiveScope)
		return &HotQualityTerminalMutationResult{
			Status:         TerminalMutationQualityKeyUnavailable,
			EffectiveScope: &effectiveScope,
		}, nil
	}

	terminal := &HotQualityTerminalRecord{
		TerminalOutcomeID: terminalOutcomeId,
		OutcomeClass:      input.OutcomeClass,
		FailureScope:      input.FailureScope,
		Source:            input.Source,
		CreatedAtMs:       now,
	}
	attempt.terminal = terminal
	attempt.expiresAtMs = expirationAt(now, store.terminalTtlMs)
	store.terminalOutcomeAttempts[terminalOutcomeId] = attemptId

	bucket := currentMemoryBucket(entry, now)
	applyTerminalToBucket(bucket, input.OutcomeClass, firstByteMs, now)
	entry.expiresAtMs = expirationAt(now, store.keyTtlMs)
	clonedTerminal := cloneTerminal(terminal)
	effectiveScope := CloneHotQualityScope(attempt.effectiveScope)
	return &HotQualityTerminalMutationResult{
		Status:         TerminalMutationApplied,
		Terminal:       clonedTerminal,
		EffectiveScope: &effectiveScope,
	}, nil
}

// Get mirrors get.
func (store *MemoryHotQualityStore) Get(ctx context.Context, scope HotQualityScope, nowMs *int64) (*HotQualitySnapshot, error) {
	now, err := normalizedNowMs(derefOrDefault(nowMs, store.now))
	if err != nil {
		return nil, err
	}
	scopeKey, err := HotQualityScopeKey(scope)
	if err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.cleanup(now)
	entry := store.freshEntry(scopeKey, now)
	if entry == nil {
		return nil, nil
	}
	return memoryEntrySnapshot(entry, now), nil
}

// GetTerminal mirrors getTerminal.
func (store *MemoryHotQualityStore) GetTerminal(ctx context.Context, attemptID string, nowMs *int64) (*HotQualityTerminalRecord, error) {
	now, err := normalizedNowMs(derefOrDefault(nowMs, store.now))
	if err != nil {
		return nil, err
	}
	attemptId, err := boundedIdentity(attemptID, "attemptId")
	if err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.cleanup(now)
	attempt := store.freshAttempt(attemptId, now)
	if attempt == nil || attempt.terminal == nil {
		return nil, nil
	}
	return cloneTerminal(attempt.terminal), nil
}

// Stats mirrors stats (forced cleanup included).
func (store *MemoryHotQualityStore) Stats(ctx context.Context, nowMs *int64) (*HotQualityStoreStats, error) {
	now, err := normalizedNowMs(derefOrDefault(nowMs, store.now))
	if err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.cleanup(now, true)
	return &HotQualityStoreStats{
		KeyCount:                    int64(len(store.entries)),
		AttemptIdentityCount:        int64(len(store.attempts)),
		TerminalIdentityCount:       int64(len(store.terminalOutcomeAttempts)),
		KeyCreationRefusals:         store.keyCreationRefusals,
		HighCardinalityDegradations: store.highCardinalityDegradations,
		AttemptCapacityRefusals:     store.attemptCapacityRefusals,
		TerminalQualityKeyMisses:    store.terminalQualityKeyMisses,
	}, nil
}

type memoryResolvedEntry struct {
	entry    *memoryHotQualityEntry
	degraded bool
}

func (store *MemoryHotQualityStore) resolveAttemptEntry(requestedScope HotQualityScope, requestedScopeKey string, now int64) (*memoryResolvedEntry, error) {
	if existing := store.freshEntry(requestedScopeKey, now); existing != nil {
		return &memoryResolvedEntry{entry: existing, degraded: false}, nil
	}
	if len(store.entries) >= store.keyCapacity {
		store.cleanup(now, true)
	}
	if len(store.entries) < store.keyCapacity {
		return &memoryResolvedEntry{entry: store.createEntry(requestedScope, requestedScopeKey, now), degraded: false}, nil
	}
	fallbackScope, err := ProtocolHotQualityScope(requestedScope)
	if err != nil {
		return nil, err
	}
	fallbackScopeKey, err := HotQualityScopeKey(fallbackScope)
	if err != nil {
		return nil, err
	}
	if fallback := store.freshEntry(fallbackScopeKey, now); fallback != nil {
		return &memoryResolvedEntry{entry: fallback, degraded: true}, nil
	}
	return nil, nil
}

func (store *MemoryHotQualityStore) entryForTerminal(attempt *memoryAttemptIdentity, now int64) *memoryHotQualityEntry {
	if existing := store.freshEntry(attempt.effectiveScopeKey, now); existing != nil {
		return existing
	}
	if len(store.entries) >= store.keyCapacity {
		store.cleanup(now, true)
	}
	if len(store.entries) >= store.keyCapacity {
		return nil
	}
	return store.createEntry(attempt.effectiveScope, attempt.effectiveScopeKey, now)
}

func (store *MemoryHotQualityStore) createEntry(scope HotQualityScope, scopeKey string, now int64) *memoryHotQualityEntry {
	entry := &memoryHotQualityEntry{
		scopeKey:    scopeKey,
		scope:       CloneHotQualityScope(scope),
		expiresAtMs: expirationAt(now, store.keyTtlMs),
	}
	store.entries[scopeKey] = entry
	return entry
}

func (store *MemoryHotQualityStore) freshEntry(scopeKey string, now int64) *memoryHotQualityEntry {
	entry, ok := store.entries[scopeKey]
	if !ok || entry.expiresAtMs > now {
		return entry
	}
	delete(store.entries, scopeKey)
	return nil
}

func (store *MemoryHotQualityStore) freshAttempt(attemptId string, now int64) *memoryAttemptIdentity {
	attempt, ok := store.attempts[attemptId]
	if !ok || attempt.expiresAtMs > now {
		return attempt
	}
	store.deleteAttempt(attemptId, attempt)
	return nil
}

func (store *MemoryHotQualityStore) deleteAttempt(attemptId string, attempt *memoryAttemptIdentity) {
	delete(store.attempts, attemptId)
	if attempt.terminal != nil {
		delete(store.terminalOutcomeAttempts, attempt.terminal.TerminalOutcomeID)
	}
}

func (store *MemoryHotQualityStore) cleanup(now int64, force ...bool) {
	if len(force) == 0 || !force[0] {
		if now < store.nextCleanupAtMs {
			return
		}
	}
	for scopeKey, entry := range store.entries {
		if entry.expiresAtMs <= now {
			delete(store.entries, scopeKey)
		}
	}
	for attemptId, attempt := range store.attempts {
		if attempt.expiresAtMs > now {
			continue
		}
		store.deleteAttempt(attemptId, attempt)
	}
	store.nextCleanupAtMs = expirationAt(now, 60_000)
}

func currentMemoryBucket(entry *memoryHotQualityEntry, now int64) *HotQualityBucketState {
	minute := now / 60_000
	index := int(minute % HotQualityMinuteBucketCount)
	minuteStartedAtMs := minute * 60_000
	bucket := entry.buckets[index]
	if bucket == nil || bucket.MinuteStartedAtMs != minuteStartedAtMs {
		bucket = &HotQualityBucketState{MinuteStartedAtMs: minuteStartedAtMs}
		entry.buckets[index] = bucket
	}
	return bucket
}

// applyTerminalToBucket mirrors the private applyTerminal helper.
func applyTerminalToBucket(bucket *HotQualityBucketState, outcomeClass string, firstByteMs *int64, now int64) {
	switch outcomeClass {
	case TerminalOutcomeCompletedResponse:
		bucket.CompletedResponses = incrementInt64(bucket.CompletedResponses)
		bucket.LastCompletedAtMs = maximumInt64(bucket.LastCompletedAtMs, now)
	case TerminalOutcomeUpstreamResponseFailure:
		bucket.UpstreamResponseFailures = incrementInt64(bucket.UpstreamResponseFailures)
	case TerminalOutcomeExplicitPolicyFailure:
		bucket.ExplicitPolicyFailures = incrementInt64(bucket.ExplicitPolicyFailures)
		bucket.LastFailureAtMs = maximumInt64(bucket.LastFailureAtMs, now)
	case TerminalOutcomeTransportFailure:
		bucket.LocalTransportFailures = incrementInt64(bucket.LocalTransportFailures)
		bucket.LastFailureAtMs = maximumInt64(bucket.LastFailureAtMs, now)
	case TerminalOutcomeTimeout:
		bucket.LocalTransportFailures = incrementInt64(bucket.LocalTransportFailures)
		bucket.Timeouts = incrementInt64(bucket.Timeouts)
		bucket.LastFailureAtMs = maximumInt64(bucket.LastFailureAtMs, now)
	case TerminalOutcomeReadInterruption:
		bucket.LocalTransportFailures = incrementInt64(bucket.LocalTransportFailures)
		bucket.ReadInterruptions = incrementInt64(bucket.ReadInterruptions)
		bucket.LastFailureAtMs = maximumInt64(bucket.LastFailureAtMs, now)
	case TerminalOutcomeIncompleteResponse:
		bucket.LocalTransportFailures = incrementInt64(bucket.LocalTransportFailures)
		bucket.IncompleteResponses = incrementInt64(bucket.IncompleteResponses)
		bucket.LastFailureAtMs = maximumInt64(bucket.LastFailureAtMs, now)
	case TerminalOutcomeUnknown:
		bucket.UnknownOutcomes = incrementInt64(bucket.UnknownOutcomes)
	case TerminalOutcomeClientCancellation:
		bucket.ClientCancellations = incrementInt64(bucket.ClientCancellations)
	}
	if firstByteMs == nil ||
		outcomeClass == TerminalOutcomeUpstreamResponseFailure ||
		outcomeClass == TerminalOutcomeUnknown ||
		outcomeClass == TerminalOutcomeClientCancellation {
		return
	}
	sample := *firstByteMs
	bucket.FirstByteSampleCount = incrementInt64(bucket.FirstByteSampleCount)
	bucket.FirstByteSumMs = addInt64(bucket.FirstByteSumMs, sample)
	histogramIndex := FirstByteHistogramBucket(sample)
	bucket.FirstByteHistogram[histogramIndex] = incrementInt64(bucket.FirstByteHistogram[histogramIndex])
}

func memoryEntrySnapshot(entry *memoryHotQualityEntry, now int64) *HotQualitySnapshot {
	buckets := make([]HotQualityBucketState, 0, HotQualityMinuteBucketCount)
	for _, bucket := range entry.buckets {
		if bucket != nil {
			buckets = append(buckets, *bucket)
		}
	}
	return CreateHotQualitySnapshot(HotQualitySnapshotState{
		ScopeKey:    entry.scopeKey,
		Scope:       entry.scope,
		Buckets:     buckets,
		ExpiresAtMs: entry.expiresAtMs,
	}, now)
}

func sameTerminal(terminal HotQualityTerminalRecord, terminalOutcomeId string, outcomeClass string, failureScope string, source string) bool {
	return terminal.TerminalOutcomeID == terminalOutcomeId &&
		terminal.OutcomeClass == outcomeClass &&
		terminal.FailureScope == failureScope &&
		terminal.Source == source
}

func assertOutcomeClass(value string) error {
	switch value {
	case TerminalOutcomeCompletedResponse,
		TerminalOutcomeUpstreamResponseFailure,
		TerminalOutcomeExplicitPolicyFailure,
		TerminalOutcomeTransportFailure,
		TerminalOutcomeTimeout,
		TerminalOutcomeReadInterruption,
		TerminalOutcomeIncompleteResponse,
		TerminalOutcomeUnknown,
		TerminalOutcomeClientCancellation:
		return nil
	}
	return errors.New("热质量 outcomeClass 非法")
}

func assertFailureScope(value string) error {
	switch value {
	case FailureScopeNone, FailureScopeKey, FailureScopeProtocolModel, FailureScopeAccount, FailureScopeUpstreamBucket:
		return nil
	}
	return errors.New("热质量 failureScope 非法")
}

func assertTerminalSource(value string) error {
	switch value {
	case TerminalSourceGatewayTransport, TerminalSourceUpstreamResponse, TerminalSourceExplicitPolicy, TerminalSourceRequestLifecycle:
		return nil
	}
	return errors.New("热质量 terminal source 非法")
}

func boundedIdentity(value string, name string) (string, error) {
	normalized := trimSpace(value)
	if normalized == "" || len(normalized) > 256 {
		return "", fmt.Errorf("%s 必须是 1 到 256 字符", name)
	}
	return normalized, nil
}

func normalizedNowMs(value int64) (int64, error) {
	if value < 0 || value > maxSafeInteger {
		return 0, errors.New("nowMs 必须是非负安全整数")
	}
	return value, nil
}

func incrementInt64(value int64) int64 {
	if value >= maxSafeInteger {
		return maxSafeInteger
	}
	return value + 1
}

func expirationAt(now int64, ttlMs int64) int64 {
	expires := now + ttlMs
	if expires > maxSafeInteger {
		return maxSafeInteger
	}
	return expires
}

func maximumInt64(left *int64, right int64) *int64 {
	if left == nil {
		value := right
		return &value
	}
	if *left < right {
		value := right
		return &value
	}
	return left
}

func cloneTerminal(terminal *HotQualityTerminalRecord) *HotQualityTerminalRecord {
	cloned := *terminal
	return &cloned
}

func derefOrDefault(value *int64, fallback func() int64) int64 {
	if value != nil {
		return *value
	}
	return fallback()
}

// trimSpace mirrors the JS String.prototype.trim.
func trimSpace(value string) string {
	return strings.TrimSpace(value)
}
