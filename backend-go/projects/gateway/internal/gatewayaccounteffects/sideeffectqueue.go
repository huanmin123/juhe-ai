package gatewayaccounteffects

import (
	"container/list"
	"errors"
	"math"
	"regexp"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// rfc3339InstantPattern mirrors shared/rfc3339.ts: the offset is required, a
// bare date-time never falls back to the local timezone.
var rfc3339InstantPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$`)

// parseRfc3339Instant mirrors parseRfc3339Instant.
func parseRfc3339Instant(value any) (int64, string, bool) {
	text, ok := value.(string)
	if !ok {
		return 0, "", false
	}
	text = trimSpace(text)
	if !rfc3339InstantPattern.MatchString(text) {
		return 0, "", false
	}
	parsed, err := timeParseRFC3339(text)
	if err != nil {
		return 0, "", false
	}
	return parsed.UnixMilli(), canonicalRFC3339(parsed), true
}

// requiredRfc3339Instant mirrors requiredRfc3339Instant with the exact error
// message: `${label}必须是带 Z 或数值 offset 的 RFC3339 时间`.
func requiredRfc3339Instant(value any, label string) (int64, string, error) {
	ms, canonical, ok := parseRfc3339Instant(value)
	if !ok {
		return 0, "", errors.New(label + "必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return ms, canonical, nil
}

// safeIntegerMax mirrors Number.MAX_SAFE_INTEGER.
const safeIntegerMax int64 = 9007199254740991

func isSafeInteger(value int64) bool {
	return value >= -safeIntegerMax && value <= safeIntegerMax
}

// normalizedDispatchRevision mirrors normalizedDispatchRevision: only safe
// positive integers survive.
func normalizedDispatchRevision(value *int64) *int64 {
	if value == nil || !isSafeInteger(*value) || *value <= 0 {
		return nil
	}
	copied := *value
	return &copied
}

// AccountSideEffectEpoch mirrors AccountSideEffectEpoch.
type AccountSideEffectEpoch struct {
	RuntimeKey       string `json:"runtimeKey"`
	Sequence         int64  `json:"sequence"`
	ObservedAt       string `json:"observedAt"`
	Success          bool   `json:"success"`
	DispatchRevision *int64 `json:"dispatchRevision,omitempty"`
}

// equalDispatchRevision mirrors the Node `===` comparison on optional
// numbers: both undefined or both defined with the same value.
func equalDispatchRevision(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// epochIdentity keys the retained-epoch set. Node relies on object identity;
// between clear() calls a (runtimeKey, sequence) pair identifies exactly one
// epoch instance, so value identity is equivalent here.
type epochIdentity struct {
	runtimeKey string
	sequence   int64
}

// AccountSideEffectEpochDecision mirrors AccountSideEffectEpochDecision.
type AccountSideEffectEpochDecision struct {
	Accepted bool
	Epoch    AccountSideEffectEpoch
}

// EpochObservation mirrors the observe() observation argument.
type EpochObservation struct {
	ObservedAt       string
	Success          bool
	DispatchRevision *int64
	Retain           bool
}

// AccountSideEffectEpochRegistry mirrors AccountSideEffectEpochRegistry.
type AccountSideEffectEpochRegistry struct {
	capacity int

	current *list.List               // back = most recently observed
	byKey   map[string]*list.Element // current epochs

	retained              map[epochIdentity]struct{}
	retainedCountByKey    map[string]int
}

// NewAccountSideEffectEpochRegistry mirrors the constructor; capacity 0 uses
// the Node default 20000.
func NewAccountSideEffectEpochRegistry(capacity int) (*AccountSideEffectEpochRegistry, error) {
	if capacity == 0 {
		capacity = 20_000
	}
	if capacity < 1 {
		return nil, errors.New("account side effect epoch capacity must be a positive integer")
	}
	return &AccountSideEffectEpochRegistry{
		capacity:           capacity,
		current:            list.New(),
		byKey:              map[string]*list.Element{},
		retained:           map[epochIdentity]struct{}{},
		retainedCountByKey: map[string]int{},
	}, nil
}

type registryEntry struct {
	key   string
	epoch AccountSideEffectEpoch
}

// Observe mirrors AccountSideEffectEpochRegistry.observe.
func (r *AccountSideEffectEpochRegistry) Observe(runtimeKey string, observation EpochObservation) (AccountSideEffectEpochDecision, error) {
	normalizedRuntimeKey := trimSpace(runtimeKey)
	if normalizedRuntimeKey == "" {
		return AccountSideEffectEpochDecision{}, errors.New("account side effect runtimeKey is required")
	}
	observedAtMs, observedAt, err := requiredRfc3339Instant(observation.ObservedAt, "account side effect observedAt")
	if err != nil {
		return AccountSideEffectEpochDecision{}, err
	}
	_ = observedAtMs
	dispatchRevision := normalizedDispatchRevision(observation.DispatchRevision)

	var current AccountSideEffectEpoch
	currentObservedAtMs := int64(math.MinInt64)
	if element, ok := r.byKey[normalizedRuntimeKey]; ok {
		current = element.Value.(*registryEntry).epoch
		parsed, _, ok := parseRfc3339Instant(current.ObservedAt)
		if !ok {
			return AccountSideEffectEpochDecision{}, errors.New("account side effect current observedAt must be a RFC3339 instant")
		}
		currentObservedAtMs = parsed
	}
	staleByRevision := current.DispatchRevision != nil &&
		(dispatchRevision == nil || *dispatchRevision < *current.DispatchRevision)
	newerRevision := dispatchRevision != nil &&
		(current.DispatchRevision == nil || *dispatchRevision > *current.DispatchRevision)
	staleByTime := !newerRevision && observedAtMs < currentObservedAtMs
	staleEqualTimestampFailure := !newerRevision &&
		observedAtMs == currentObservedAtMs &&
		current.Success &&
		!observation.Success
	if staleByRevision || staleByTime || staleEqualTimestampFailure {
		epoch := AccountSideEffectEpoch{
			RuntimeKey: normalizedRuntimeKey,
			Sequence:   current.Sequence,
			ObservedAt: observedAt,
			Success:    observation.Success,
		}
		if dispatchRevision != nil {
			epoch.DispatchRevision = dispatchRevision
		}
		return AccountSideEffectEpochDecision{Accepted: false, Epoch: epoch}, nil
	}
	epoch := AccountSideEffectEpoch{
		RuntimeKey:       normalizedRuntimeKey,
		Sequence:         current.Sequence + 1,
		ObservedAt:       observedAt,
		Success:          observation.Success,
		DispatchRevision: dispatchRevision,
	}
	r.upsert(normalizedRuntimeKey, epoch)
	if observation.Retain {
		r.retain(epoch)
	}
	r.trimToCapacity(normalizedRuntimeKey)
	return AccountSideEffectEpochDecision{Accepted: true, Epoch: epoch}, nil
}

func (r *AccountSideEffectEpochRegistry) upsert(runtimeKey string, epoch AccountSideEffectEpoch) {
	if element, ok := r.byKey[runtimeKey]; ok {
		r.current.Remove(element)
	}
	element := r.current.PushBack(&registryEntry{key: runtimeKey, epoch: epoch})
	r.byKey[runtimeKey] = element
}

// IsCurrent mirrors AccountSideEffectEpochRegistry.isCurrent.
func (r *AccountSideEffectEpochRegistry) IsCurrent(epoch AccountSideEffectEpoch) bool {
	element, ok := r.byKey[epoch.RuntimeKey]
	if !ok {
		return false
	}
	current := element.Value.(*registryEntry).epoch
	return current.Sequence == epoch.Sequence &&
		current.ObservedAt == epoch.ObservedAt &&
		current.Success == epoch.Success &&
		equalDispatchRevision(current.DispatchRevision, epoch.DispatchRevision)
}

// Release mirrors AccountSideEffectEpochRegistry.release.
func (r *AccountSideEffectEpochRegistry) Release(epoch AccountSideEffectEpoch) {
	identity := epochIdentity{runtimeKey: epoch.RuntimeKey, sequence: epoch.Sequence}
	if _, ok := r.retained[identity]; !ok {
		return
	}
	delete(r.retained, identity)
	retainedCount := r.retainedCountByKey[epoch.RuntimeKey]
	if retainedCount <= 1 {
		delete(r.retainedCountByKey, epoch.RuntimeKey)
	} else {
		r.retainedCountByKey[epoch.RuntimeKey] = retainedCount - 1
	}
	r.trimToCapacity("")
}

// Clear mirrors AccountSideEffectEpochRegistry.clear.
func (r *AccountSideEffectEpochRegistry) Clear() {
	r.current.Init()
	r.byKey = map[string]*list.Element{}
	r.retained = map[epochIdentity]struct{}{}
	r.retainedCountByKey = map[string]int{}
}

// Size exposes the live epoch count (Node currentByRuntimeKey.size).
func (r *AccountSideEffectEpochRegistry) Size() int {
	return r.current.Len()
}

func (r *AccountSideEffectEpochRegistry) retain(epoch AccountSideEffectEpoch) {
	r.retained[epochIdentity{runtimeKey: epoch.RuntimeKey, sequence: epoch.Sequence}] = struct{}{}
	r.retainedCountByKey[epoch.RuntimeKey]++
}

// trimToCapacity mirrors trimToCapacity: scan from the LRU front, evict the
// first non-retained non-preserved entry, then move the retained prefix to
// the LRU back so sustained pressure does not rescan the same segment.
func (r *AccountSideEffectEpochRegistry) trimToCapacity(preserveRuntimeKey string) {
	for r.current.Len() > r.capacity {
		evicted := false
		type retainedEntry struct {
			key   string
			epoch AccountSideEffectEpoch
		}
		retainedPrefix := make([]retainedEntry, 0, 4)
		for element := r.current.Front(); element != nil; element = element.Next() {
			entry := element.Value.(*registryEntry)
			if entry.key == preserveRuntimeKey || r.retainedCountByKey[entry.key] > 0 {
				retainedPrefix = append(retainedPrefix, retainedEntry{key: entry.key, epoch: entry.epoch})
				continue
			}
			r.current.Remove(element)
			delete(r.byKey, entry.key)
			evicted = true
			break
		}
		// 活跃项移到 LRU 末尾，持续容量压力不必反复扫描同一段 retained 前缀。
		for _, item := range retainedPrefix {
			element, ok := r.byKey[item.key]
			if !ok {
				continue
			}
			entry, ok := element.Value.(*registryEntry)
			if !ok || entry.epoch.Sequence != item.epoch.Sequence {
				continue
			}
			r.current.Remove(element)
			moved := r.current.PushBack(entry)
			r.byKey[item.key] = moved
		}
		if !evicted {
			break
		}
	}
}

// AccountErrorHandlingInput mirrors the apply_account_error_handling input.
type AccountErrorHandlingInput struct {
	Success                       bool
	StatusCode                    *int64
	Headers                       map[string][]string
	BodyText                      *string
	ErrorMessage                  *string
	UpstreamErrorSummary          *string
	UpstreamErrorSummaryResolved  *bool
	TraceID                       *string
	ObservedAt                    string
	DispatchRevision              *int64
	TrafficSource                 string
	// PolicyDecision mirrors AccountErrorPolicyDecision; only its presence is
	// consumed by this slice, so it stays opaque.
	PolicyDecision any
}

// AccountSideEffectOperation mirrors AccountSideEffectOperation: the single
// account error handling operation type. The account mirrors the Node
// OpenAIAccountSecret projection carried on the operation.
type AccountSideEffectOperation struct {
	Type    string // 'apply_account_error_handling'
	Account gatewayruntimecache.OpenAIAccountSecret
	Input   AccountErrorHandlingInput
}

// SuppressibleFromSecret adapts a runtime-cache secret to the runtime-key
// surface. The Node UpstreamAccount accessType union collapses onto
// accountAccessType in the cache projection.
func SuppressibleFromSecret(secret gatewayruntimecache.OpenAIAccountSecret) SuppressibleGatewayAccount {
	return SuppressibleGatewayAccount{
		ID:                        secret.ID,
		AccountAccessType:         secret.AccountAccessType,
		BindingSystemAccountID:    derefStringPtr(secret.BindingSystemAccountID),
		BoundGroupID:              derefStringPtr(secret.BoundGroupID),
		AccountAuthorizationID:    derefStringPtr(secret.AccountAuthorizationID),
		CredentialSourceAccountID: derefStringPtr(secret.CredentialSourceAccountID),
	}
}

// GatewayAccountRuntimeKeyForSecret derives the runtime key from a secret.
func GatewayAccountRuntimeKeyForSecret(secret gatewayruntimecache.OpenAIAccountSecret) (string, error) {
	return GatewayAccountRuntimeKey(SuppressibleFromSecret(secret))
}

func derefStringPtr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// AccountSideEffectOperationType is the only queued operation type.
const AccountSideEffectOperationType = "apply_account_error_handling"

// QueuedAccountSideEffect mirrors QueuedAccountSideEffect.
type QueuedAccountSideEffect struct {
	Operation      AccountSideEffectOperation
	Epoch          AccountSideEffectEpoch
	Attempts       int
	EnqueuedAtMs   int64
	NextAttemptAtMs int64
	ExpiresAtMs    int64
}

// compareSideEffectQueueItems mirrors compareSideEffectQueueItems.
func compareSideEffectQueueItems(left, right *QueuedAccountSideEffect) int {
	if left.NextAttemptAtMs != right.NextAttemptAtMs {
		if left.NextAttemptAtMs < right.NextAttemptAtMs {
			return -1
		}
		return 1
	}
	if left.EnqueuedAtMs != right.EnqueuedAtMs {
		if left.EnqueuedAtMs < right.EnqueuedAtMs {
			return -1
		}
		return 1
	}
	return 0
}

// compareSideEffectFailureAge mirrors compareSideEffectFailureAge.
func compareSideEffectFailureAge(left, right *QueuedAccountSideEffect) int {
	if left.EnqueuedAtMs != right.EnqueuedAtMs {
		if left.EnqueuedAtMs < right.EnqueuedAtMs {
			return -1
		}
		return 1
	}
	if left.NextAttemptAtMs != right.NextAttemptAtMs {
		if left.NextAttemptAtMs < right.NextAttemptAtMs {
			return -1
		}
		return 1
	}
	return 0
}

// AccountSideEffectQueue mirrors AccountSideEffectQueue: a binary heap keyed
// by (nextAttemptAtMs, enqueuedAtMs) with per-runtime-key and per-item
// indexes plus a secondary failure-age heap.
type AccountSideEffectQueue struct {
	items                []*QueuedAccountSideEffect
	indexByItem          map[*QueuedAccountSideEffect]int
	itemsByRuntimeKey    map[string]map[*QueuedAccountSideEffect]struct{}
	failuresByAge        []*QueuedAccountSideEffect
	failureAgeIndexByItem map[*QueuedAccountSideEffect]int
	failureCount         int
}

// NewAccountSideEffectQueue builds an empty queue.
func NewAccountSideEffectQueue() *AccountSideEffectQueue {
	return &AccountSideEffectQueue{
		indexByItem:           map[*QueuedAccountSideEffect]int{},
		itemsByRuntimeKey:     map[string]map[*QueuedAccountSideEffect]struct{}{},
		failureAgeIndexByItem: map[*QueuedAccountSideEffect]int{},
	}
}

// Len mirrors the length getter.
func (q *AccountSideEffectQueue) Len() int { return len(q.items) }

// HasFailures mirrors the hasFailures getter.
func (q *AccountSideEffectQueue) HasFailures() bool { return q.failureCount > 0 }

// Push mirrors push.
func (q *AccountSideEffectQueue) Push(item *QueuedAccountSideEffect) {
	q.items = append(q.items, item)
	q.registerItem(item, len(q.items)-1)
	q.heapifyUp(len(q.items) - 1)
}

// Peek mirrors peek.
func (q *AccountSideEffectQueue) Peek() *QueuedAccountSideEffect {
	if len(q.items) == 0 {
		return nil
	}
	return q.items[0]
}

// Pop mirrors pop.
func (q *AccountSideEffectQueue) Pop() *QueuedAccountSideEffect {
	return q.removeAt(0)
}

// Clear mirrors clear.
func (q *AccountSideEffectQueue) Clear() {
	q.items = q.items[:0]
	q.indexByItem = map[*QueuedAccountSideEffect]int{}
	q.itemsByRuntimeKey = map[string]map[*QueuedAccountSideEffect]struct{}{}
	q.failuresByAge = q.failuresByAge[:0]
	q.failureAgeIndexByItem = map[*QueuedAccountSideEffect]int{}
	q.failureCount = 0
}

// FindIndex mirrors findIndex.
func (q *AccountSideEffectQueue) FindIndex(predicate func(*QueuedAccountSideEffect) bool) int {
	for index, item := range q.items {
		if predicate(item) {
			return index
		}
	}
	return -1
}

// FindIndexByRuntimeKey mirrors findIndexByRuntimeKey: insertion order of the
// runtime-key set is the Node Map insertion order; the first-inserted item
// wins. Go maps have no order, so the earliest queue position among the
// runtime key's items is returned instead — the only observable difference is
// which of several queued items for one key is replaced first, and Node's own
// contract only guarantees "some item of that key" (all carry the same
// runtime key).
func (q *AccountSideEffectQueue) FindIndexByRuntimeKey(runtimeKey string) int {
	items := q.itemsByRuntimeKey[runtimeKey]
	if len(items) == 0 {
		return -1
	}
	best := -1
	for item := range items {
		index, ok := q.indexByItem[item]
		if !ok {
			continue
		}
		if best < 0 || index < best {
			best = index
		}
	}
	return best
}

// HasRuntimeKey mirrors hasRuntimeKey.
func (q *AccountSideEffectQueue) HasRuntimeKey(runtimeKey string) bool {
	return len(q.itemsByRuntimeKey[runtimeKey]) > 0
}

// ReplaceAt mirrors replaceAt.
func (q *AccountSideEffectQueue) ReplaceAt(index int, item *QueuedAccountSideEffect) *QueuedAccountSideEffect {
	if index < 0 || index >= len(q.items) {
		return nil
	}
	replaced := q.items[index]
	q.unregisterItem(replaced)
	q.items[index] = item
	q.registerItem(item, index)
	q.rebalanceAt(index)
	return replaced
}

// RemoveWhere mirrors removeWhere.
func (q *AccountSideEffectQueue) RemoveWhere(predicate func(*QueuedAccountSideEffect) bool) int {
	return len(q.RemoveWhereItems(predicate))
}

// RemoveWhereItems mirrors removeWhereItems.
func (q *AccountSideEffectQueue) RemoveWhereItems(predicate func(*QueuedAccountSideEffect) bool) []*QueuedAccountSideEffect {
	removed := []*QueuedAccountSideEffect{}
	kept := q.items[:0]
	for _, item := range q.items {
		if predicate(item) {
			removed = append(removed, item)
			continue
		}
		kept = append(kept, item)
	}
	if len(removed) == 0 {
		return removed
	}
	q.items = kept
	q.rebuildIndexes()
	q.heapify()
	return removed
}

// RemoveRuntimeKey mirrors removeRuntimeKey.
func (q *AccountSideEffectQueue) RemoveRuntimeKey(runtimeKey string) []*QueuedAccountSideEffect {
	matching := make([]*QueuedAccountSideEffect, 0, len(q.itemsByRuntimeKey[runtimeKey]))
	for item := range q.itemsByRuntimeKey[runtimeKey] {
		matching = append(matching, item)
	}
	removed := []*QueuedAccountSideEffect{}
	for _, item := range matching {
		index, ok := q.indexByItem[item]
		if !ok {
			continue
		}
		if removedItem := q.removeAt(index); removedItem != nil {
			removed = append(removed, removedItem)
		}
	}
	return removed
}

// RemoveOldestWhere mirrors removeOldestWhere.
func (q *AccountSideEffectQueue) RemoveOldestWhere(predicate func(*QueuedAccountSideEffect) bool) *QueuedAccountSideEffect {
	oldestIndex := -1
	for index, item := range q.items {
		if !predicate(item) {
			continue
		}
		if oldestIndex < 0 || item.EnqueuedAtMs < q.items[oldestIndex].EnqueuedAtMs {
			oldestIndex = index
		}
	}
	if oldestIndex < 0 {
		return nil
	}
	return q.removeAt(oldestIndex)
}

// RemoveOldestFailure mirrors removeOldestFailure.
func (q *AccountSideEffectQueue) RemoveOldestFailure() *QueuedAccountSideEffect {
	if len(q.failuresByAge) == 0 {
		return nil
	}
	oldestFailure := q.failuresByAge[0]
	index, ok := q.indexByItem[oldestFailure]
	if !ok {
		return nil
	}
	return q.removeAt(index)
}

func (q *AccountSideEffectQueue) heapify() {
	for index := len(q.items)/2 - 1; index >= 0; index-- {
		q.heapifyDown(index)
	}
}

func (q *AccountSideEffectQueue) heapifyUp(startIndex int) int {
	index := startIndex
	for index > 0 {
		parentIndex := (index - 1) / 2
		if compareSideEffectQueueItems(q.items[parentIndex], q.items[index]) <= 0 {
			return index
		}
		q.swap(index, parentIndex)
		index = parentIndex
	}
	return index
}

func (q *AccountSideEffectQueue) heapifyDown(startIndex int) {
	index := startIndex
	for {
		leftIndex := index*2 + 1
		rightIndex := leftIndex + 1
		smallestIndex := index
		if leftIndex < len(q.items) && compareSideEffectQueueItems(q.items[leftIndex], q.items[smallestIndex]) < 0 {
			smallestIndex = leftIndex
		}
		if rightIndex < len(q.items) && compareSideEffectQueueItems(q.items[rightIndex], q.items[smallestIndex]) < 0 {
			smallestIndex = rightIndex
		}
		if smallestIndex == index {
			return
		}
		q.swap(index, smallestIndex)
		index = smallestIndex
	}
}

func (q *AccountSideEffectQueue) swap(leftIndex, rightIndex int) {
	left := q.items[leftIndex]
	right := q.items[rightIndex]
	q.items[leftIndex] = right
	q.items[rightIndex] = left
	q.indexByItem[right] = leftIndex
	q.indexByItem[left] = rightIndex
}

func (q *AccountSideEffectQueue) rebalanceAt(index int) {
	indexAfterHeapifyUp := q.heapifyUp(index)
	q.heapifyDown(indexAfterHeapifyUp)
}

func (q *AccountSideEffectQueue) removeAt(index int) *QueuedAccountSideEffect {
	if index < 0 || index >= len(q.items) {
		return nil
	}
	removed := q.items[index]
	last := q.items[len(q.items)-1]
	q.items = q.items[:len(q.items)-1]
	q.unregisterItem(removed)
	if len(q.items) > 0 && index < len(q.items) {
		q.items[index] = last
		q.indexByItem[last] = index
		q.rebalanceAt(index)
	}
	return removed
}

func (q *AccountSideEffectQueue) registerItem(item *QueuedAccountSideEffect, index int) {
	q.indexByItem[item] = index
	runtimeItems := q.itemsByRuntimeKey[item.Epoch.RuntimeKey]
	if runtimeItems == nil {
		runtimeItems = map[*QueuedAccountSideEffect]struct{}{}
		q.itemsByRuntimeKey[item.Epoch.RuntimeKey] = runtimeItems
	}
	runtimeItems[item] = struct{}{}
	if !item.Operation.Input.Success {
		q.failureCount++
		q.pushFailureByAge(item)
	}
}

func (q *AccountSideEffectQueue) unregisterItem(item *QueuedAccountSideEffect) {
	delete(q.indexByItem, item)
	runtimeItems := q.itemsByRuntimeKey[item.Epoch.RuntimeKey]
	if runtimeItems != nil {
		delete(runtimeItems, item)
		if len(runtimeItems) == 0 {
			delete(q.itemsByRuntimeKey, item.Epoch.RuntimeKey)
		}
	}
	if !item.Operation.Input.Success {
		q.failureCount--
		q.removeFailureByAge(item)
	}
}

func (q *AccountSideEffectQueue) rebuildIndexes() {
	q.indexByItem = map[*QueuedAccountSideEffect]int{}
	q.itemsByRuntimeKey = map[string]map[*QueuedAccountSideEffect]struct{}{}
	q.failuresByAge = q.failuresByAge[:0]
	q.failureAgeIndexByItem = map[*QueuedAccountSideEffect]int{}
	q.failureCount = 0
	for index, item := range q.items {
		q.registerItem(item, index)
	}
}

func (q *AccountSideEffectQueue) pushFailureByAge(item *QueuedAccountSideEffect) {
	q.failuresByAge = append(q.failuresByAge, item)
	index := len(q.failuresByAge) - 1
	q.failureAgeIndexByItem[item] = index
	for index > 0 {
		parentIndex := (index - 1) / 2
		if compareSideEffectFailureAge(q.failuresByAge[parentIndex], q.failuresByAge[index]) <= 0 {
			break
		}
		q.swapFailureAge(index, parentIndex)
		index = parentIndex
	}
}

func (q *AccountSideEffectQueue) removeFailureByAge(item *QueuedAccountSideEffect) {
	index, ok := q.failureAgeIndexByItem[item]
	if !ok {
		return
	}
	last := q.failuresByAge[len(q.failuresByAge)-1]
	q.failuresByAge = q.failuresByAge[:len(q.failuresByAge)-1]
	delete(q.failureAgeIndexByItem, item)
	if len(q.failuresByAge) == 0 || index >= len(q.failuresByAge) {
		return
	}
	q.failuresByAge[index] = last
	q.failureAgeIndexByItem[last] = index
	q.rebalanceFailureAgeAt(index)
}

func (q *AccountSideEffectQueue) rebalanceFailureAgeAt(startIndex int) {
	index := startIndex
	for index > 0 {
		parentIndex := (index - 1) / 2
		if compareSideEffectFailureAge(q.failuresByAge[parentIndex], q.failuresByAge[index]) <= 0 {
			break
		}
		q.swapFailureAge(index, parentIndex)
		index = parentIndex
	}
	for {
		leftIndex := index*2 + 1
		rightIndex := leftIndex + 1
		smallestIndex := index
		if leftIndex < len(q.failuresByAge) && compareSideEffectFailureAge(q.failuresByAge[leftIndex], q.failuresByAge[smallestIndex]) < 0 {
			smallestIndex = leftIndex
		}
		if rightIndex < len(q.failuresByAge) && compareSideEffectFailureAge(q.failuresByAge[rightIndex], q.failuresByAge[smallestIndex]) < 0 {
			smallestIndex = rightIndex
		}
		if smallestIndex == index {
			break
		}
		q.swapFailureAge(index, smallestIndex)
		index = smallestIndex
	}
}

func (q *AccountSideEffectQueue) swapFailureAge(leftIndex, rightIndex int) {
	left := q.failuresByAge[leftIndex]
	right := q.failuresByAge[rightIndex]
	q.failuresByAge[leftIndex] = right
	q.failuresByAge[rightIndex] = left
	q.failureAgeIndexByItem[right] = leftIndex
	q.failureAgeIndexByItem[left] = rightIndex
}
