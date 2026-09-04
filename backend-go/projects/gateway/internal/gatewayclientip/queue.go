package gatewayclientip

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// Reject reasons mirror HighConcurrencyQueueRejectReason.
const (
	QueueRejectQueueDisabled    = "queue_disabled"
	QueueRejectQueueFull        = "queue_full"
	QueueRejectAPIKeyQueueFull  = "api_key_queue_full"
	QueueRejectTimeout          = "timeout"
	QueueRejectAborted          = "aborted"
)

// highConcurrencyStateStoreKeyPrefix is the Redis key family Node namespaces
// under juhe-ai:state:high-concurrency-queue:.
const highConcurrencyQueueKeyFamily = "juhe-ai:state:high-concurrency-queue:"

// HighConcurrencyQueueWaitInput mirrors HighConcurrencyQueueWaitInput.
type HighConcurrencyQueueWaitInput struct {
	SystemAccountID           string
	GroupID                   string
	APIKeyID                  string
	AccountIDs                []string
	AccountConcurrencyLimits  map[string]int
	Lane                      string
	Policy                    map[string]any
	MaxWaitMs                 *int64
	Signal                    context.Context
}

// HighConcurrencyQueueWaitResult mirrors the HighConcurrencyQueueWaitResult
// union flattened: Ready=true fills WaitedMs/QueueSizeBeforeWake;
// Ready=false fills Reason/WaitedMs/QueueSize/PerAPIKeyQueueSize.
type HighConcurrencyQueueWaitResult struct {
	Ready               bool
	WaitedMs            int64
	QueueSizeBeforeWake int
	Reason              string
	QueueSize           int
	PerAPIKeyQueueSize  int
}

// HighConcurrencyQueueSnapshotRow mirrors highConcurrencyGroupQueueSnapshot.
type HighConcurrencyQueueSnapshotRow struct {
	GroupKey           string
	Lane               string
	QueueSize          int
	PerAPIKeyQueueSize map[string]int
}

// HighConcurrencyQueueOptions configures the group queue.
type HighConcurrencyQueueOptions struct {
	Clock  Clock
	Logger Logger
	// RuntimeStateDriver mirrors runtimeConfig.runtimeStateDriver.
	RuntimeStateDriver string
	// StateRedisURL mirrors runtimeConfig.redis.stateUrl.
	StateRedisURL string
	// RedisNamespace mirrors runtimeConfig.redis.namespace.
	RedisNamespace string
	// PolicyDefaults carries the concurrency.globalMax-derived defaults.
	PolicyDefaults HighConcurrencyPolicyDefaults
	// Concurrency is the account live-concurrency seam (G10 ConcurrencySource
	// plus the lane read and release subscription).
	Concurrency AccountConcurrencySource
	// Scheduler schedules queue timeouts; defaults to time.AfterFunc.
	Scheduler FlushScheduler
	// Sleep mirrors the redis poll-loop delay.
	Sleep func(time.Duration)
}

// HighConcurrencyGroupQueue owns the per-group wait queues.
type HighConcurrencyGroupQueue struct {
	clock        Clock
	logger       Logger
	driver       string
	sched        FlushScheduler
	sleep        func(time.Duration)
	defaults     HighConcurrencyPolicyDefaults
	concurrency  AccountConcurrencySource
	unsubscribe  func()

	redis     *redis.Client
	closeFns  []func()
	namespace string

	mu     sync.Mutex
	queues map[string]*highConcurrencyQueueState
	nextID int64
	// index mirrors queueItemsByAccountLane: "lane:accountId" → items.
	index map[string]map[*highConcurrencyQueueItem]bool
}

type highConcurrencyQueueState struct {
	groupKey       string
	lane           string
	items          []*highConcurrencyQueueItem
	perAPIKeyCount map[string]int
}

type highConcurrencyQueueItem struct {
	id                 int64
	groupKey           string
	lane               string
	apiKeyKey          string
	accountIDs         map[string]bool
	accountCapacities  map[string]highConcurrencyAccountCapacity
	enqueuedAtMs       int64
	deadlineAtMs       int64
	cancelTimer        func()
	signal             context.Context
	completed          bool
	resolve            chan HighConcurrencyQueueWaitResult
	done               chan struct{}
}

type highConcurrencyAccountCapacity struct {
	hardLimit      int
	imageLaneLimit int
}

// NewHighConcurrencyGroupQueue builds the queue and subscribes the account
// release wake-up (Node's module-level subscribeAccountConcurrencyRelease).
func NewHighConcurrencyGroupQueue(opts HighConcurrencyQueueOptions) (*HighConcurrencyGroupQueue, error) {
	if opts.Concurrency == nil {
		return nil, errors.New("gatewayclientip HighConcurrencyGroupQueue 需要 AccountConcurrencySource")
	}
	clock := opts.Clock
	if clock == nil {
		clock = systemClock()
	}
	sched := opts.Scheduler
	if sched == nil {
		sched = timerFlushScheduler{}
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	q := &HighConcurrencyGroupQueue{
		clock:       clock,
		logger:      opts.Logger,
		driver:      opts.RuntimeStateDriver,
		sched:       sched,
		sleep:       sleep,
		defaults:    opts.PolicyDefaults,
		concurrency: opts.Concurrency,
		namespace:   opts.RedisNamespace,
		queues:      map[string]*highConcurrencyQueueState{},
		index:       map[string]map[*highConcurrencyQueueItem]bool{},
	}
	q.unsubscribe = opts.Concurrency.SubscribeAccountConcurrencyRelease(func(event AccountConcurrencyReleaseEvent) {
		q.wakeQueuesForReleasedAccount(event.AccountID, event.Lane)
	})
	if opts.RuntimeStateDriver == RuntimeStateDriverRedis {
		if strings.TrimSpace(opts.StateRedisURL) == "" {
			q.unsubscribe()
			return nil, errors.New("JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置")
		}
		options, err := redis.ParseURL(opts.StateRedisURL)
		if err != nil {
			q.unsubscribe()
			return nil, err
		}
		client := redis.NewClient(options)
		q.redis = client
		q.closeFns = append(q.closeFns, func() { _ = client.Close() })
	}
	return q, nil
}

// Close unsubscribes the release listener and disposes owned resources.
func (q *HighConcurrencyGroupQueue) Close() {
	if q.unsubscribe != nil {
		q.unsubscribe()
		q.unsubscribe = nil
	}
	for _, closeFn := range q.closeFns {
		closeFn()
	}
	q.closeFns = nil
}

// WaitForHighConcurrencyGroupCapacity mirrors waitForHighConcurrencyGroupCapacity.
func (q *HighConcurrencyGroupQueue) WaitForHighConcurrencyGroupCapacity(ctx context.Context, input HighConcurrencyQueueWaitInput) (HighConcurrencyQueueWaitResult, error) {
	policy, err := resolveGroupSchedulingPolicy(input.Policy, q.defaults)
	if err != nil {
		return HighConcurrencyQueueWaitResult{}, err
	}
	configuredMaxQueueWaitMs := policy.MaxQueueWaitMs
	maxQueueWaitMs := configuredMaxQueueWaitMs
	if input.MaxWaitMs != nil {
		maxQueueWaitMs = configuredMaxQueueWaitMs
		if requested := normalizeNonNegativeInteger(*input.MaxWaitMs, 0); requested < maxQueueWaitMs {
			maxQueueWaitMs = requested
		}
	}
	maxQueueSize := policy.MaxQueueSize
	perAPIKeyQueueLimit := policy.PerAPIKeyQueueLimit
	lane := AccountConcurrencyLaneText
	if input.Lane == AccountConcurrencyLaneImage {
		lane = AccountConcurrencyLaneImage
	}
	if q.driver == RuntimeStateDriverRedis {
		return q.waitForRedisHighConcurrencyGroupCapacity(ctx, input, policy, maxQueueWaitMs, maxQueueSize, perAPIKeyQueueLimit, lane)
	}
	return q.waitForLocalHighConcurrencyGroupCapacity(ctx, input, policy, maxQueueWaitMs, maxQueueSize, perAPIKeyQueueLimit, lane), nil
}

// ---------------------------------------------------------------------------
// local (memory) driver
// ---------------------------------------------------------------------------

func (q *HighConcurrencyGroupQueue) waitForLocalHighConcurrencyGroupCapacity(
	ctx context.Context,
	input HighConcurrencyQueueWaitInput,
	policy GroupSchedulingPolicy,
	maxQueueWaitMs int64,
	maxQueueSize int,
	perAPIKeyQueueLimit int,
	lane string,
) HighConcurrencyQueueWaitResult {
	groupKey := highConcurrencyGroupQueueKey(input.SystemAccountID, input.GroupID, lane)
	apiKeyKey := apiKeyQueueKey(input.APIKeyID)
	accountCapacities := buildAccountCapacities(input.AccountIDs, input.AccountConcurrencyLimits, policy)

	q.mu.Lock()
	state, ok := q.queues[groupKey]
	if !ok {
		state = &highConcurrencyQueueState{groupKey: groupKey, lane: lane, perAPIKeyCount: map[string]int{}}
	}
	perAPIKeyQueueSize := state.perAPIKeyCount[apiKeyKey]
	if input.Signal != nil && input.Signal.Err() != nil {
		q.mu.Unlock()
		return rejectedQueueWait(QueueRejectAborted, 0, len(state.items), perAPIKeyQueueSize)
	}
	if len(state.items) == 0 && q.hasImmediateAccountCapacity(input.AccountIDs, accountCapacities, lane) {
		if ok {
			// The existing state stays registered exactly as found.
		}
		q.mu.Unlock()
		return HighConcurrencyQueueWaitResult{Ready: true}
	}
	if maxQueueWaitMs <= 0 {
		q.mu.Unlock()
		return rejectedQueueWait(QueueRejectQueueDisabled, 0, len(state.items), perAPIKeyQueueSize)
	}
	if len(state.items) >= maxQueueSize {
		q.mu.Unlock()
		return rejectedQueueWait(QueueRejectQueueFull, 0, len(state.items), perAPIKeyQueueSize)
	}
	if perAPIKeyQueueSize >= perAPIKeyQueueLimit {
		q.mu.Unlock()
		return rejectedQueueWait(QueueRejectAPIKeyQueueFull, 0, len(state.items), perAPIKeyQueueSize)
	}
	q.queues[groupKey] = state
	item := &highConcurrencyQueueItem{
		id:                q.nextID,
		groupKey:          groupKey,
		lane:              lane,
		apiKeyKey:         apiKeyKey,
		accountIDs:        uniqueAccountIDSet(input.AccountIDs),
		accountCapacities: accountCapacities,
		enqueuedAtMs:      q.clock.Now().UnixMilli(),
		deadlineAtMs:      q.clock.Now().UnixMilli() + maxQueueWaitMs,
		signal:            input.Signal,
		resolve:           make(chan HighConcurrencyQueueWaitResult, 1),
		done:              make(chan struct{}),
	}
	q.nextID += 1
	state.items = append(state.items, item)
	q.indexQueueItem(item)
	state.perAPIKeyCount[apiKeyKey] = perAPIKeyQueueSize + 1
	// Timer is registered under the lock so a racing release wake cannot
	// observe the unset handle.
	item.cancelTimer = q.sched.AfterFunc(time.Duration(maxQueueWaitMs)*time.Millisecond, func() {
		q.completeGroupQueueItem(item, rejectedQueueWait(QueueRejectTimeout,
			q.clock.Now().UnixMilli()-item.enqueuedAtMs,
			q.queueSizeOf(item.groupKey),
			q.perAPIKeyQueueSizeOf(item.groupKey, item.apiKeyKey)))
	})
	q.mu.Unlock()

	if input.Signal != nil {
		go func() {
			select {
			case <-input.Signal.Done():
				q.completeGroupQueueItem(item, rejectedQueueWait(QueueRejectAborted,
					q.clock.Now().UnixMilli()-item.enqueuedAtMs,
					q.queueSizeOf(item.groupKey),
					q.perAPIKeyQueueSizeOf(item.groupKey, item.apiKeyKey)))
			case <-item.done:
			}
		}()
	}
	result := <-item.resolve
	return result
}

// completeGroupQueueItem mirrors completeQueueItem: idempotent removal,
// timer cancel, per-api-key accounting, idle-state eviction and delivery.
func (q *HighConcurrencyGroupQueue) completeGroupQueueItem(item *highConcurrencyQueueItem, result HighConcurrencyQueueWaitResult) {
	q.mu.Lock()
	if item.completed {
		q.mu.Unlock()
		return
	}
	item.completed = true
	state := q.queues[item.groupKey]
	q.unindexQueueItem(item)
	if state != nil {
		for i, candidate := range state.items {
			if candidate.id == item.id {
				state.items = append(state.items[:i], state.items[i+1:]...)
				next := state.perAPIKeyCount[item.apiKeyKey] - 1
				if next <= 0 {
					delete(state.perAPIKeyCount, item.apiKeyKey)
				} else {
					state.perAPIKeyCount[item.apiKeyKey] = next
				}
				break
			}
		}
		if len(state.items) == 0 {
			delete(q.queues, item.groupKey)
		}
	}
	q.mu.Unlock()
	if item.cancelTimer != nil {
		item.cancelTimer()
	}
	item.resolve <- result
	close(item.done)
}

func (q *HighConcurrencyGroupQueue) queueSizeOf(groupKey string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if state, ok := q.queues[groupKey]; ok {
		return len(state.items)
	}
	return 0
}

func (q *HighConcurrencyGroupQueue) perAPIKeyQueueSizeOf(groupKey string, apiKeyKey string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if state, ok := q.queues[groupKey]; ok {
		return state.perAPIKeyCount[apiKeyKey]
	}
	return 0
}

// wakeQueuesForReleasedAccount mirrors wakeQueuesForReleasedAccount.
func (q *HighConcurrencyGroupQueue) wakeQueuesForReleasedAccount(accountID string, releasedLane string) {
	fallbackLane := AccountConcurrencyLaneImage
	if releasedLane == AccountConcurrencyLaneImage {
		fallbackLane = AccountConcurrencyLaneText
	}
	candidate := q.findQueueWakeCandidate(accountID, releasedLane)
	if candidate == nil {
		candidate = q.findQueueWakeCandidate(accountID, fallbackLane)
	}
	if candidate == nil {
		return
	}
	// Node evaluates queueSizeBeforeWake before the removal, so the waking
	// item counts itself.
	result := HighConcurrencyQueueWaitResult{
		Ready:               true,
		WaitedMs:            maxInt64(0, q.clock.Now().UnixMilli()-candidate.item.enqueuedAtMs),
		QueueSizeBeforeWake: q.queueSizeOf(candidate.item.groupKey),
	}
	q.completeGroupQueueItem(candidate.item, result)
}

type queueWakeCandidate struct {
	state *highConcurrencyQueueState
	item  *highConcurrencyQueueItem
}

// findQueueWakeCandidate mirrors findQueueWakeCandidate.
func (q *HighConcurrencyGroupQueue) findQueueWakeCandidate(accountID string, lane string) *queueWakeCandidate {
	q.mu.Lock()
	candidates := q.index[accountLaneIndexKey(accountID, lane)]
	if candidates == nil {
		q.mu.Unlock()
		return nil
	}
	// Insertion-order iteration over the Node Set: walk the state queues in
	// item id order (ids grow monotonically per queue).
	for _, state := range q.queues {
		if state.lane != lane {
			continue
		}
		for _, item := range state.items {
			if !candidates[item] {
				continue
			}
			if !item.accountIDs[accountID] {
				continue
			}
			if q.queueItemCanAcquireAfterRelease(item, accountID) {
				q.mu.Unlock()
				return &queueWakeCandidate{state: state, item: item}
			}
		}
	}
	q.mu.Unlock()
	return nil
}

// queueItemCanAcquireAfterRelease mirrors queueItemCanAcquireAfterRelease.
func (q *HighConcurrencyGroupQueue) queueItemCanAcquireAfterRelease(item *highConcurrencyQueueItem, accountID string) bool {
	capacity, ok := item.accountCapacities[accountID]
	if !ok {
		return true
	}
	if q.concurrency.CurrentAccountConcurrency(accountID, "") >= capacity.hardLimit {
		return false
	}
	if item.lane != AccountConcurrencyLaneImage {
		return true
	}
	return q.concurrency.CurrentAccountConcurrency(accountID, AccountConcurrencyLaneImage) < capacity.imageLaneLimit
}

// hasImmediateAccountCapacity mirrors hasImmediateAccountCapacity.
func (q *HighConcurrencyGroupQueue) hasImmediateAccountCapacity(accountIDs []string, capacities map[string]highConcurrencyAccountCapacity, lane string) bool {
	seen := map[string]bool{}
	for _, accountID := range accountIDs {
		if accountID == "" || seen[accountID] {
			continue
		}
		seen[accountID] = true
		capacity, ok := capacities[accountID]
		if !ok {
			return true
		}
		if q.concurrency.CurrentAccountConcurrency(accountID, "") >= capacity.hardLimit {
			continue
		}
		if lane != AccountConcurrencyLaneImage || q.concurrency.CurrentAccountConcurrency(accountID, AccountConcurrencyLaneImage) < capacity.imageLaneLimit {
			return true
		}
	}
	return false
}

func (q *HighConcurrencyGroupQueue) indexQueueItem(item *highConcurrencyQueueItem) {
	for accountID := range item.accountIDs {
		key := accountLaneIndexKey(accountID, item.lane)
		items := q.index[key]
		if items == nil {
			items = map[*highConcurrencyQueueItem]bool{}
			q.index[key] = items
		}
		items[item] = true
	}
}

func (q *HighConcurrencyGroupQueue) unindexQueueItem(item *highConcurrencyQueueItem) {
	for accountID := range item.accountIDs {
		key := accountLaneIndexKey(accountID, item.lane)
		items := q.index[key]
		if items == nil {
			continue
		}
		delete(items, item)
		if len(items) == 0 {
			delete(q.index, key)
		}
	}
}

// Snapshot mirrors highConcurrencyGroupQueueSnapshot.
func (q *HighConcurrencyGroupQueue) Snapshot() []HighConcurrencyQueueSnapshotRow {
	if q.driver == RuntimeStateDriverRedis {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	rows := make([]HighConcurrencyQueueSnapshotRow, 0, len(q.queues))
	for _, state := range q.queues {
		perAPIKey := make(map[string]int, len(state.perAPIKeyCount))
		for key, value := range state.perAPIKeyCount {
			perAPIKey[key] = value
		}
		rows = append(rows, HighConcurrencyQueueSnapshotRow{
			GroupKey:           state.groupKey,
			Lane:               state.lane,
			QueueSize:          len(state.items),
			PerAPIKeyQueueSize: perAPIKey,
		})
	}
	return rows
}

// Clear mirrors clearHighConcurrencyGroupQueues.
func (q *HighConcurrencyGroupQueue) Clear() {
	if q.driver == RuntimeStateDriverRedis {
		q.mu.Lock()
		q.queues = map[string]*highConcurrencyQueueState{}
		q.index = map[string]map[*highConcurrencyQueueItem]bool{}
		q.mu.Unlock()
		return
	}
	for {
		q.mu.Lock()
		var head *highConcurrencyQueueItem
		for _, state := range q.queues {
			if len(state.items) > 0 {
				head = state.items[0]
				break
			}
		}
		if head == nil {
			q.mu.Unlock()
			break
		}
		// Node: rejectedQueueWait('aborted', Date.now() - enqueuedAtMs,
		// state.items.length, perApiKeyCount) evaluated pre-removal.
		result := rejectedQueueWait(QueueRejectAborted,
			q.clock.Now().UnixMilli()-head.enqueuedAtMs,
			q.queueSizeLocked(head.groupKey),
			q.perAPIKeyQueueSizeLocked(head.groupKey, head.apiKeyKey))
		q.mu.Unlock()
		q.completeGroupQueueItem(head, result)
	}
	q.mu.Lock()
	q.queues = map[string]*highConcurrencyQueueState{}
	q.index = map[string]map[*highConcurrencyQueueItem]bool{}
	q.mu.Unlock()
}

func (q *HighConcurrencyGroupQueue) queueSizeLocked(groupKey string) int {
	if state, ok := q.queues[groupKey]; ok {
		return len(state.items)
	}
	return 0
}

func (q *HighConcurrencyGroupQueue) perAPIKeyQueueSizeLocked(groupKey string, apiKeyKey string) int {
	if state, ok := q.queues[groupKey]; ok {
		return state.perAPIKeyCount[apiKeyKey]
	}
	return 0
}

// ---------------------------------------------------------------------------
// redis driver
// ---------------------------------------------------------------------------

func (q *HighConcurrencyGroupQueue) waitForRedisHighConcurrencyGroupCapacity(
	ctx context.Context,
	input HighConcurrencyQueueWaitInput,
	policy GroupSchedulingPolicy,
	maxQueueWaitMs int64,
	maxQueueSize int,
	perAPIKeyQueueLimit int,
	lane string,
) (HighConcurrencyQueueWaitResult, error) {
	startedAtMs := q.clock.Now().UnixMilli()
	groupKey := highConcurrencyGroupQueueKey(input.SystemAccountID, input.GroupID, lane)
	apiKeyKey := apiKeyQueueKey(input.APIKeyID)
	capacities := buildAccountCapacities(input.AccountIDs, input.AccountConcurrencyLimits, policy)
	if input.Signal != nil && input.Signal.Err() != nil {
		return rejectedQueueWait(QueueRejectAborted, 0, 0, 0), nil
	}
	initialSizes, err := q.redisHighConcurrencyQueueSizes(ctx, groupKey, apiKeyKey)
	if err != nil {
		return HighConcurrencyQueueWaitResult{}, err
	}
	immediate, err := q.hasImmediateAccountCapacityAsync(ctx, input.AccountIDs, capacities, lane)
	if err != nil {
		return HighConcurrencyQueueWaitResult{}, err
	}
	if initialSizes.queueSize == 0 && immediate {
		return HighConcurrencyQueueWaitResult{Ready: true}, nil
	}
	if maxQueueWaitMs <= 0 {
		return rejectedQueueWait(QueueRejectQueueDisabled, 0, initialSizes.queueSize, initialSizes.perAPIKeyQueueSize), nil
	}
	deadlineAtMs := startedAtMs + maxQueueWaitMs
	itemID := fmt.Sprintf("%d:%d:%s", os.Getpid(), startedAtMs, randomHex8())
	enqueued, err := q.enqueueRedisHighConcurrencyQueueItem(ctx, groupKey, apiKeyKey, itemID, deadlineAtMs, maxQueueSize, perAPIKeyQueueLimit)
	if err != nil {
		return HighConcurrencyQueueWaitResult{}, err
	}
	switch enqueued.status {
	case "queue_full":
		return rejectedQueueWait(QueueRejectQueueFull, 0, enqueued.queueSize, enqueued.perAPIKeyQueueSize), nil
	case "api_key_queue_full":
		return rejectedQueueWait(QueueRejectAPIKeyQueueFull, 0, enqueued.queueSize, enqueued.perAPIKeyQueueSize), nil
	}
	for q.clock.Now().UnixMilli() < deadlineAtMs {
		if input.Signal != nil && input.Signal.Err() != nil {
			sizes, removeErr := q.removeRedisHighConcurrencyQueueItem(ctx, groupKey, apiKeyKey, itemID)
			if removeErr != nil {
				return HighConcurrencyQueueWaitResult{}, removeErr
			}
			return rejectedQueueWait(QueueRejectAborted, q.clock.Now().UnixMilli()-startedAtMs, sizes.queueSize, sizes.perAPIKeyQueueSize), nil
		}
		position, posErr := q.redisHighConcurrencyQueuePosition(ctx, groupKey, apiKeyKey, itemID)
		if posErr != nil {
			return HighConcurrencyQueueWaitResult{}, posErr
		}
		if !position.present {
			return rejectedQueueWait(QueueRejectTimeout, q.clock.Now().UnixMilli()-startedAtMs, position.queueSize, position.perAPIKeyQueueSize), nil
		}
		if position.rank == 0 {
			immediate, immErr := q.hasImmediateAccountCapacityAsync(ctx, input.AccountIDs, capacities, lane)
			if immErr != nil {
				return HighConcurrencyQueueWaitResult{}, immErr
			}
			if immediate {
				sizes, removeErr := q.removeRedisHighConcurrencyQueueItem(ctx, groupKey, apiKeyKey, itemID)
				if removeErr != nil {
					return HighConcurrencyQueueWaitResult{}, removeErr
				}
				return HighConcurrencyQueueWaitResult{
					Ready:               true,
					WaitedMs:            q.clock.Now().UnixMilli() - startedAtMs,
					QueueSizeBeforeWake: maxInt(1, sizes.queueSize+1),
				}, nil
			}
		}
		remaining := deadlineAtMs - q.clock.Now().UnixMilli()
		delayMs := int64(100)
		if remaining < delayMs {
			delayMs = remaining
		}
		if delayMs < 1 {
			delayMs = 1
		}
		q.sleep(time.Duration(delayMs) * time.Millisecond)
	}
	sizes, err := q.removeRedisHighConcurrencyQueueItem(ctx, groupKey, apiKeyKey, itemID)
	if err != nil {
		return HighConcurrencyQueueWaitResult{}, err
	}
	return rejectedQueueWait(QueueRejectTimeout, q.clock.Now().UnixMilli()-startedAtMs, sizes.queueSize, sizes.perAPIKeyQueueSize), nil
}

// hasImmediateAccountCapacityAsync mirrors hasImmediateAccountCapacityAsync.
func (q *HighConcurrencyGroupQueue) hasImmediateAccountCapacityAsync(ctx context.Context, accountIDs []string, capacities map[string]highConcurrencyAccountCapacity, lane string) (bool, error) {
	unique := uniqueAccountIDs(accountIDs)
	currentConcurrency, err := q.concurrency.LoadAccountCurrentConcurrencyByID(ctx, unique)
	if err != nil {
		return false, err
	}
	var imageLaneConcurrency map[string]int
	if lane == AccountConcurrencyLaneImage {
		imageLaneConcurrency, err = q.concurrency.LoadAccountCurrentConcurrencyByLane(ctx, unique, AccountConcurrencyLaneImage)
		if err != nil {
			return false, err
		}
	}
	for _, accountID := range unique {
		capacity, ok := capacities[accountID]
		if !ok {
			return true, nil
		}
		if currentConcurrency[accountID] >= capacity.hardLimit {
			continue
		}
		if lane != AccountConcurrencyLaneImage || imageLaneConcurrency[accountID] < capacity.imageLaneLimit {
			return true, nil
		}
	}
	return false, nil
}

type redisQueueSizes struct {
	queueSize          int
	perAPIKeyQueueSize int
}

func (q *HighConcurrencyGroupQueue) enqueueRedisHighConcurrencyQueueItem(ctx context.Context, groupKey string, apiKeyKey string, itemID string, deadlineAtMs int64, maxQueueSize int, perAPIKeyQueueLimit int) (struct {
	status            string
	queueSize         int
	perAPIKeyQueueSize int
}, error) {
	nowMs := q.clock.Now().UnixMilli()
	ttlMs := deadlineAtMs - nowMs + 60_000
	if ttlMs < 1 {
		ttlMs = 1
	}
	result, err := q.redis.Eval(ctx, redisHighConcurrencyQueueEnqueueScript, []string{
		redisHighConcurrencyGroupQueueKey(q.redisNamespace(), groupKey),
		redisHighConcurrencyAPIKeyQueueKey(q.redisNamespace(), groupKey, apiKeyKey),
	}, itemID, strconvIota64(clampNonNegative(deadlineAtMs)), strconvIota64(clampNonNegative(nowMs)),
		strconvItoa(maxInt(1, maxQueueSize)), strconvItoa(maxInt(1, perAPIKeyQueueLimit)), strconvIota64(ttlMs)).Result()
	if err != nil {
		return struct {
			status             string
			queueSize          int
			perAPIKeyQueueSize int
		}{}, err
	}
	values := numericRedisArray(result)
	out := struct {
		status             string
		queueSize          int
		perAPIKeyQueueSize int
	}{status: "queue_full"}
	if len(values) > 0 {
		switch values[0] {
		case 1:
			out.status = "enqueued"
		case 2:
			out.status = "api_key_queue_full"
		}
	}
	if len(values) > 1 {
		out.queueSize = int(values[1])
	}
	if len(values) > 2 {
		out.perAPIKeyQueueSize = int(values[2])
	}
	return out, nil
}

func (q *HighConcurrencyGroupQueue) redisHighConcurrencyQueuePosition(ctx context.Context, groupKey string, apiKeyKey string, itemID string) (redisQueueSizesAndRank, error) {
	result, err := q.redis.Eval(ctx, redisHighConcurrencyQueuePositionScript, []string{
		redisHighConcurrencyGroupQueueKey(q.redisNamespace(), groupKey),
		redisHighConcurrencyAPIKeyQueueKey(q.redisNamespace(), groupKey, apiKeyKey),
	}, itemID, strconvIota64(clampNonNegative(q.clock.Now().UnixMilli()))).Result()
	if err != nil {
		return redisQueueSizesAndRank{}, err
	}
	values := numericRedisArray(result)
	out := redisQueueSizesAndRank{rank: -1}
	if len(values) > 0 && values[0] == 1 {
		out.present = true
	}
	if len(values) > 1 {
		out.rank = int(values[1])
	}
	if len(values) > 2 {
		out.queueSize = int(values[2])
	}
	if len(values) > 3 {
		out.perAPIKeyQueueSize = int(values[3])
	}
	return out, nil
}

type redisQueueSizesAndRank struct {
	present            bool
	rank               int
	queueSize          int
	perAPIKeyQueueSize int
}

func (q *HighConcurrencyGroupQueue) redisHighConcurrencyQueueSizes(ctx context.Context, groupKey string, apiKeyKey string) (redisQueueSizes, error) {
	result, err := q.redis.Eval(ctx, redisHighConcurrencyQueueSizesScript, []string{
		redisHighConcurrencyGroupQueueKey(q.redisNamespace(), groupKey),
		redisHighConcurrencyAPIKeyQueueKey(q.redisNamespace(), groupKey, apiKeyKey),
	}, strconvIota64(clampNonNegative(q.clock.Now().UnixMilli()))).Result()
	if err != nil {
		return redisQueueSizes{}, err
	}
	values := numericRedisArray(result)
	out := redisQueueSizes{}
	if len(values) > 0 {
		out.queueSize = int(values[0])
	}
	if len(values) > 1 {
		out.perAPIKeyQueueSize = int(values[1])
	}
	return out, nil
}

func (q *HighConcurrencyGroupQueue) removeRedisHighConcurrencyQueueItem(ctx context.Context, groupKey string, apiKeyKey string, itemID string) (redisQueueSizes, error) {
	result, err := q.redis.Eval(ctx, redisHighConcurrencyQueueRemoveScript, []string{
		redisHighConcurrencyGroupQueueKey(q.redisNamespace(), groupKey),
		redisHighConcurrencyAPIKeyQueueKey(q.redisNamespace(), groupKey, apiKeyKey),
	}, itemID, strconvIota64(clampNonNegative(q.clock.Now().UnixMilli()))).Result()
	if err != nil {
		return redisQueueSizes{}, err
	}
	values := numericRedisArray(result)
	out := redisQueueSizes{}
	if len(values) > 0 {
		out.queueSize = int(values[0])
	}
	if len(values) > 1 {
		out.perAPIKeyQueueSize = int(values[1])
	}
	return out, nil
}

// redisNamespace returns the configured Redis namespace segment.
func (q *HighConcurrencyGroupQueue) redisNamespace() string {
	return q.namespace
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// highConcurrencyGroupQueueKey mirrors highConcurrencyGroupQueueKey.
func highConcurrencyGroupQueueKey(systemAccountID string, groupID string, lane string) string {
	return systemAccountID + ":" + groupID + ":" + lane
}

// apiKeyQueueKey mirrors input.apiKeyId?.trim() || 'internal'.
func apiKeyQueueKey(apiKeyID string) string {
	trimmed := strings.TrimSpace(apiKeyID)
	if trimmed == "" {
		return "internal"
	}
	return trimmed
}

// accountLaneIndexKey mirrors accountLaneIndexKey.
func accountLaneIndexKey(accountID string, lane string) string {
	return lane + ":" + accountID
}

// uniqueAccountIDs mirrors uniqueAccountIds.
func uniqueAccountIDs(accountIDs []string) []string {
	seen := map[string]bool{}
	output := make([]string, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		trimmed := strings.TrimSpace(accountID)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		output = append(output, trimmed)
	}
	return output
}

func uniqueAccountIDSet(accountIDs []string) map[string]bool {
	set := map[string]bool{}
	for _, accountID := range accountIDs {
		if accountID == "" {
			continue
		}
		set[accountID] = true
	}
	return set
}

// buildAccountCapacities mirrors buildAccountCapacities.
func buildAccountCapacities(accountIDs []string, accountConcurrencyLimits map[string]int, policy GroupSchedulingPolicy) map[string]highConcurrencyAccountCapacity {
	capacities := map[string]highConcurrencyAccountCapacity{}
	seen := map[string]bool{}
	for _, accountID := range accountIDs {
		if accountID == "" || seen[accountID] {
			continue
		}
		seen[accountID] = true
		hardLimit := int(normalizePositiveInteger(accountConcurrencyLimits[accountID], 1))
		capacities[accountID] = highConcurrencyAccountCapacity{
			hardLimit:      hardLimit,
			imageLaneLimit: EffectiveImageLaneConcurrencyLimit(hardLimit, policy),
		}
	}
	return capacities
}

// rejectedQueueWait mirrors rejectedQueueWait.
func rejectedQueueWait(reason string, waitedMs int64, queueSize int, perAPIKeyQueueSize int) HighConcurrencyQueueWaitResult {
	return HighConcurrencyQueueWaitResult{
		Ready:              false,
		Reason:             reason,
		WaitedMs:           maxInt64(0, waitedMs),
		QueueSize:          queueSize,
		PerAPIKeyQueueSize: perAPIKeyQueueSize,
	}
}

// redisHighConcurrencyGroupQueueKey mirrors redisHighConcurrencyGroupQueueKey:
// redisNamespacedKey(`juhe-ai:state:high-concurrency-queue:<sanitized>`).
func redisHighConcurrencyGroupQueueKey(namespace string, groupKey string) string {
	return namespacedStateKey(namespace, highConcurrencyQueueKeyFamily+sanitizeRedisKeyPart(groupKey))
}

// redisHighConcurrencyAPIKeyQueueKey mirrors redisHighConcurrencyApiKeyQueueKey.
func redisHighConcurrencyAPIKeyQueueKey(namespace string, groupKey string, apiKeyKey string) string {
	return namespacedStateKey(namespace, highConcurrencyQueueKeyFamily+sanitizeRedisKeyPart(groupKey)+":api-key:"+sanitizeRedisKeyPart(apiKeyKey))
}

// namespacedStateKey applies redisNamespacedKey with the "juhe-ai:" root.
func namespacedStateKey(namespace string, key string) string {
	normalized := strings.TrimSpace(key)
	if normalized == "" {
		return ""
	}
	sanitizedNamespace, err := sanitizeRedisNamespacePart(namespace)
	if err != nil {
		return normalized
	}
	prefix := "juhe-ai:" + sanitizedNamespace + ":"
	if strings.HasPrefix(normalized, prefix) {
		return normalized
	}
	if strings.HasPrefix(normalized, "juhe-ai:") {
		return prefix + normalized[len("juhe-ai:"):]
	}
	return prefix + normalized
}

// The redis Lua scripts mirror high-concurrency-queue.service.ts verbatim.
const redisHighConcurrencyQueueEnqueueScript = `
local item_id = ARGV[1]
local deadline_at_ms = tonumber(ARGV[2])
local now_ms = tonumber(ARGV[3])
local max_queue_size = tonumber(ARGV[4])
local per_api_key_queue_limit = tonumber(ARGV[5])
local ttl_ms = tonumber(ARGV[6])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms)
local queue_size = redis.call('ZCARD', KEYS[1])
local per_api_key_queue_size = redis.call('ZCARD', KEYS[2])
if queue_size >= max_queue_size then
  return {0, queue_size, per_api_key_queue_size}
end
if per_api_key_queue_size >= per_api_key_queue_limit then
  return {2, queue_size, per_api_key_queue_size}
end
redis.call('ZADD', KEYS[1], deadline_at_ms, item_id)
redis.call('ZADD', KEYS[2], deadline_at_ms, item_id)
redis.call('PEXPIRE', KEYS[1], ttl_ms)
redis.call('PEXPIRE', KEYS[2], ttl_ms)
return {1, queue_size + 1, per_api_key_queue_size + 1}
`

const redisHighConcurrencyQueuePositionScript = `
local item_id = ARGV[1]
local now_ms = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms)
local rank = redis.call('ZRANK', KEYS[1], item_id)
if rank == false then
  return {0, -1, redis.call('ZCARD', KEYS[1]), redis.call('ZCARD', KEYS[2])}
end
return {1, rank, redis.call('ZCARD', KEYS[1]), redis.call('ZCARD', KEYS[2])}
`

const redisHighConcurrencyQueueSizesScript = `
local now_ms = tonumber(ARGV[1])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms)
return {redis.call('ZCARD', KEYS[1]), redis.call('ZCARD', KEYS[2])}
`

const redisHighConcurrencyQueueRemoveScript = `
local item_id = ARGV[1]
local now_ms = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms)
redis.call('ZREM', KEYS[1], item_id)
redis.call('ZREM', KEYS[2], item_id)
return {redis.call('ZCARD', KEYS[1]), redis.call('ZCARD', KEYS[2])}
`
