package gatewayclientip

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// Reject reasons mirror ClientIpConcurrencyRejectReason.
const (
	RejectLimitReached   = "limit_reached"
	RejectQueueDisabled  = "queue_disabled"
	RejectQueueFull      = "queue_full"
	RejectTimeout        = "timeout"
	RejectAborted        = "aborted"
	OverflowModeQueue    = "queue"
	OverflowModeReject   = "reject"
)

// Constants mirror client-ip-concurrency.service.ts.
const (
	redisClientIPConcurrencyTTL           = 180_000 // ms
	redisClientIPConcurrencyRenewInterval = 30 * time.Second
)

// ClientIPConcurrencyAcquireInput mirrors ClientIpConcurrencyAcquireInput.
// Signal mirrors AbortSignal: canceling the context aborts the wait.
type ClientIPConcurrencyAcquireInput struct {
	SystemAccountID string
	GroupID         string
	APIKeyID        string
	ClientIP        string
	// Policy is the raw scheduling policy map (nil uses defaults).
	Policy map[string]any
	Signal context.Context
}

// ClientIPConcurrencyDecision mirrors the ClientIpConcurrencyDecision union:
// Enabled=false / Acquired=true is the disabled fast path; a rejection fills
// Reason + QueueSize; an acquisition fills Current/Queued/
// QueueSizeBeforeAcquire and carries Release.
type ClientIPConcurrencyDecision struct {
	Enabled                bool
	Acquired               bool
	Reason                 string
	Current                int
	Limit                  int
	WaitedMs               int64
	Queued                 bool
	QueueSizeBeforeAcquire int
	QueueSize              int

	release func()
}

// Release mirrors the Node release() closure; it is idempotent (the release
// closure embeds its own once guard, so the decision stays a plain value).
func (d *ClientIPConcurrencyDecision) Release() {
	if d == nil || d.release == nil {
		return
	}
	d.release()
}

// ClientIPConcurrencySnapshotRow mirrors clientIpConcurrencySnapshot rows.
type ClientIPConcurrencySnapshotRow struct {
	Key       string
	Current   int
	QueueSize int
}

// ClientIPConcurrencyOptions configures the per client-IP concurrency slots.
type ClientIPConcurrencyOptions struct {
	Clock  Clock
	Logger Logger
	// RuntimeStateDriver mirrors runtimeConfig.runtimeStateDriver.
	RuntimeStateDriver string
	// StateRedisURL mirrors runtimeConfig.redis.stateUrl.
	StateRedisURL string
	// PolicyDefaults carries the concurrency.globalMax-derived defaults.
	PolicyDefaults HighConcurrencyPolicyDefaults
	// Scheduler schedules queue timeouts; defaults to time.AfterFunc.
	Scheduler FlushScheduler
	// Sleep mirrors the redis poll-loop delay; tests can shrink it.
	Sleep func(d time.Duration)
}

// ClientIPConcurrency owns the per client-IP concurrency slots and queues.
type ClientIPConcurrency struct {
	clock    Clock
	logger   Logger
	driver   string
	sched    FlushScheduler
	sleep    func(time.Duration)
	defaults HighConcurrencyPolicyDefaults

	redis    *redis.Client
	closeFns []func()

	mu     sync.Mutex
	states map[string]*clientIPConcurrencyState
	nextID int64
}

type clientIPConcurrencyState struct {
	key     string
	limit   int
	current int
	items   []*clientIPConcurrencyQueueItem
}

type clientIPConcurrencyQueueItem struct {
	id           int64
	key          string
	enqueuedAtMs int64
	state        *clientIPConcurrencyState
	limit        int
	signal       context.Context
	cancelTimer  func()
	// completed is guarded by the service mutex; it is the single
	// completion arbiter (Node relies on the single-threaded event loop).
	completed bool
	resolve   chan ClientIPConcurrencyDecision
	// done closes after the decision was delivered so the abort watcher can
	// exit without consuming the waiter's value.
	done chan struct{}
}

// NewClientIPConcurrency builds the slot family.
func NewClientIPConcurrency(opts ClientIPConcurrencyOptions) (*ClientIPConcurrency, error) {
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
	c := &ClientIPConcurrency{
		clock:    clock,
		logger:   opts.Logger,
		driver:   opts.RuntimeStateDriver,
		sched:    sched,
		sleep:    sleep,
		defaults: opts.PolicyDefaults,
		states:   map[string]*clientIPConcurrencyState{},
	}
	if opts.RuntimeStateDriver == RuntimeStateDriverRedis {
		if strings.TrimSpace(opts.StateRedisURL) == "" {
			return nil, errors.New("JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置")
		}
		options, err := redis.ParseURL(opts.StateRedisURL)
		if err != nil {
			return nil, err
		}
		client := redis.NewClient(options)
		c.redis = client
		c.closeFns = append(c.closeFns, func() { _ = client.Close() })
	}
	return c, nil
}

// Close disposes the Redis client when this instance owns one.
func (c *ClientIPConcurrency) Close() {
	for _, closeFn := range c.closeFns {
		closeFn()
	}
	c.closeFns = nil
}

// AcquireHighConcurrencyClientIPSlot mirrors acquireHighConcurrencyClientIpSlot.
func (c *ClientIPConcurrency) AcquireHighConcurrencyClientIPSlot(ctx context.Context, input ClientIPConcurrencyAcquireInput) (ClientIPConcurrencyDecision, error) {
	policy, err := resolveGroupSchedulingPolicy(input.Policy, c.defaults)
	if err != nil {
		return ClientIPConcurrencyDecision{}, err
	}
	limit := policy.ClientIPConcurrencyLimit
	clientIP := strings.TrimSpace(input.ClientIP)
	if clientIP == "" || limit <= 0 {
		return ClientIPConcurrencyDecision{Enabled: false, Acquired: true}, nil
	}
	if input.Signal != nil && input.Signal.Err() != nil {
		return rejectedDecisionConst(RejectAborted, 0, limit, 0, 0), nil
	}
	key := clientIPConcurrencyKey(input.SystemAccountID, input.GroupID, input.APIKeyID, clientIP)
	if c.driver == RuntimeStateDriverRedis {
		return c.acquireRedisClientIPSlot(ctx, input, policy, key, limit)
	}
	return c.acquireLocalClientIPSlot(ctx, input, policy, key, limit), nil
}

// ---------------------------------------------------------------------------
// local (memory) driver
// ---------------------------------------------------------------------------

func (c *ClientIPConcurrency) acquireLocalClientIPSlot(ctx context.Context, input ClientIPConcurrencyAcquireInput, policy GroupSchedulingPolicy, key string, limit int) ClientIPConcurrencyDecision {
	c.mu.Lock()
	state, ok := c.states[key]
	if !ok {
		state = &clientIPConcurrencyState{key: key}
		c.states[key] = state
	}
	state.limit = limit
	if state.current < limit {
		decision := c.acquiredLocalDecisionLocked(state, limit, 0, false, len(state.items))
		c.mu.Unlock()
		return decision
	}
	if policy.ClientIPConcurrencyOverflowMode != OverflowModeQueue {
		decision := rejectedLocalDecisionLocked(RejectLimitReached, state, limit, 0)
		c.mu.Unlock()
		return decision
	}
	maxQueueWaitMs := policy.MaxQueueWaitMs
	if maxQueueWaitMs <= 0 {
		decision := rejectedLocalDecisionLocked(RejectQueueDisabled, state, limit, 0)
		c.mu.Unlock()
		return decision
	}
	queueLimit := int(normalizePositiveInteger(policy.PerAPIKeyQueueLimit, int64(nonZero(c.defaults.PerAPIKeyQueueLimit, 1))))
	if len(state.items) >= queueLimit {
		decision := rejectedLocalDecisionLocked(RejectQueueFull, state, limit, 0)
		c.mu.Unlock()
		return decision
	}
	item := &clientIPConcurrencyQueueItem{
		id:           c.nextID,
		key:          key,
		enqueuedAtMs: c.clock.Now().UnixMilli(),
		state:        state,
		limit:        limit,
		signal:       input.Signal,
		resolve:      make(chan ClientIPConcurrencyDecision, 1),
		done:         make(chan struct{}),
	}
	c.nextID += 1
	state.items = append(state.items, item)
	// Timer is registered under the lock so a racing release wake cannot
	// observe the unset handle.
	item.cancelTimer = c.sched.AfterFunc(time.Duration(maxQueueWaitMs)*time.Millisecond, func() {
		c.completeQueuedItem(item, RejectTimeout)
	})
	c.mu.Unlock()

	if input.Signal != nil {
		go func() {
			select {
			case <-input.Signal.Done():
				c.completeQueuedItem(item, RejectAborted)
			case <-item.done:
			}
		}()
	}
	decision := <-item.resolve
	return decision
}

// completeQueuedItem mirrors completeQueueItem for the timer / abort
// completions: idempotent, removes the item, cancels the timer and delivers
// the decision. The decision reads the pre-removal queue state like the Node
// argument evaluation order.
func (c *ClientIPConcurrency) completeQueuedItem(item *clientIPConcurrencyQueueItem, reason string) {
	c.mu.Lock()
	if item.completed {
		c.mu.Unlock()
		return
	}
	item.completed = true
	decision := c.rejectedQueuedDecisionLocked(reason, item)
	c.removeItemLocked(item)
	c.mu.Unlock()
	c.deliverItem(item, decision)
}

// deliverItem cancels the timer outside the lock and hands the decision to
// the waiting acquirer.
func (c *ClientIPConcurrency) deliverItem(item *clientIPConcurrencyQueueItem, decision ClientIPConcurrencyDecision) {
	if item.cancelTimer != nil {
		item.cancelTimer()
	}
	item.resolve <- decision
	close(item.done)
}

// removeItemLocked drops the item from its state queue and cleans the state
// up when idle (Node cleanupStateIfIdle).
func (c *ClientIPConcurrency) removeItemLocked(item *clientIPConcurrencyQueueItem) {
	state := item.state
	for i, candidate := range state.items {
		if candidate.id == item.id {
			state.items = append(state.items[:i], state.items[i+1:]...)
			break
		}
	}
	c.cleanupStateIfIdleLocked(state)
}

// acquiredLocalDecisionLocked mirrors acquiredDecision (state.current += 1).
func (c *ClientIPConcurrency) acquiredLocalDecisionLocked(state *clientIPConcurrencyState, limit int, waitedMs time.Duration, queued bool, queueSizeBeforeAcquire int) ClientIPConcurrencyDecision {
	state.current += 1
	current := state.current
	var once sync.Once
	return ClientIPConcurrencyDecision{
		Enabled:                true,
		Acquired:               true,
		Current:                current,
		Limit:                  limit,
		WaitedMs:               maxInt64(0, int64(waitedMs/time.Millisecond)),
		Queued:                 queued,
		QueueSizeBeforeAcquire: queueSizeBeforeAcquire,
		release: func() {
			once.Do(func() { c.releaseClientIPSlot(state.key) })
		},
	}
}

// rejectedQueuedDecisionLocked mirrors rejectedDecision for a queued item:
// the queue snapshot includes the item itself (pre-removal evaluation).
func (c *ClientIPConcurrency) rejectedQueuedDecisionLocked(reason string, item *clientIPConcurrencyQueueItem) ClientIPConcurrencyDecision {
	waited := c.clock.Now().UnixMilli() - item.enqueuedAtMs
	return ClientIPConcurrencyDecision{
		Enabled:   true,
		Acquired:  false,
		Reason:    reason,
		Current:   item.state.current,
		Limit:     item.state.limit,
		WaitedMs:  maxInt64(0, waited),
		QueueSize: len(item.state.items),
	}
}

func rejectedLocalDecisionLocked(reason string, state *clientIPConcurrencyState, limit int, waitedMs int64) ClientIPConcurrencyDecision {
	return ClientIPConcurrencyDecision{
		Enabled:   true,
		Acquired:  false,
		Reason:    reason,
		Current:   state.current,
		Limit:     limit,
		WaitedMs:  maxInt64(0, waitedMs),
		QueueSize: len(state.items),
	}
}

func rejectedDecisionConst(reason string, current int, limit int, waitedMs int64, queueSize int) ClientIPConcurrencyDecision {
	return ClientIPConcurrencyDecision{
		Enabled:   true,
		Acquired:  false,
		Reason:    reason,
		Current:   current,
		Limit:     limit,
		WaitedMs:  maxInt64(0, waitedMs),
		QueueSize: queueSize,
	}
}

// releaseClientIPSlot mirrors releaseClientIpSlot +
// wakeQueuedClientIpRequests: aborted head items are drained, the first
// live head is woken with a fresh slot.
func (c *ClientIPConcurrency) releaseClientIPSlot(key string) {
	c.mu.Lock()
	state, ok := c.states[key]
	if !ok {
		c.mu.Unlock()
		return
	}
	state.current = maxInt(0, state.current-1)
	for state.current < state.limit && len(state.items) > 0 {
		head := state.items[0]
		if head.completed {
			state.items = state.items[1:]
			continue
		}
		if head.signal != nil && head.signal.Err() != nil {
			head.completed = true
			decision := ClientIPConcurrencyDecision{
				Enabled:   true,
				Acquired:  false,
				Reason:    RejectAborted,
				Current:   state.current,
				Limit:     state.limit,
				WaitedMs:  maxInt64(0, c.clock.Now().UnixMilli()-head.enqueuedAtMs),
				QueueSize: len(state.items),
			}
			state.items = state.items[1:]
			c.cleanupStateIfIdleLocked(state)
			c.mu.Unlock()
			c.deliverItem(head, decision)
			c.mu.Lock()
			continue
		}
		head.completed = true
		waited := time.Duration(c.clock.Now().UnixMilli()-head.enqueuedAtMs) * time.Millisecond
		decision := c.acquiredLocalDecisionLocked(state, state.limit, waited, true, len(state.items))
		state.items = state.items[1:]
		c.mu.Unlock()
		c.deliverItem(head, decision)
		return
	}
	c.cleanupStateIfIdleLocked(state)
	c.mu.Unlock()
}

func (c *ClientIPConcurrency) cleanupStateIfIdleLocked(state *clientIPConcurrencyState) {
	if state.current <= 0 && len(state.items) == 0 {
		delete(c.states, state.key)
	}
}

// Snapshot mirrors clientIpConcurrencySnapshot.
func (c *ClientIPConcurrency) Snapshot() []ClientIPConcurrencySnapshotRow {
	if c.driver == RuntimeStateDriverRedis {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rows := make([]ClientIPConcurrencySnapshotRow, 0, len(c.states))
	for _, state := range c.states {
		rows = append(rows, ClientIPConcurrencySnapshotRow{
			Key:       state.key,
			Current:   state.current,
			QueueSize: len(state.items),
		})
	}
	return rows
}

// Clear mirrors clearClientIpConcurrency.
func (c *ClientIPConcurrency) Clear() {
	if c.driver == RuntimeStateDriverRedis {
		c.mu.Lock()
		c.states = map[string]*clientIPConcurrencyState{}
		c.mu.Unlock()
		return
	}
	for {
		c.mu.Lock()
		var head *clientIPConcurrencyQueueItem
		for _, state := range c.states {
			if len(state.items) > 0 {
				head = state.items[0]
				break
			}
		}
		if head == nil {
			c.mu.Unlock()
			break
		}
		// Node completes every queued item with rejectedDecision('aborted',
		// state, 1, waited): limit is the literal 1 here.
		head.completed = true
		decision := ClientIPConcurrencyDecision{
			Enabled:   true,
			Acquired:  false,
			Reason:    RejectAborted,
			Current:   head.state.current,
			Limit:     1,
			WaitedMs:  maxInt64(0, c.clock.Now().UnixMilli()-head.enqueuedAtMs),
			QueueSize: len(head.state.items),
		}
		head.state.items = head.state.items[1:]
		c.mu.Unlock()
		c.deliverItem(head, decision)
	}
	c.mu.Lock()
	c.states = map[string]*clientIPConcurrencyState{}
	c.mu.Unlock()
}

// ---------------------------------------------------------------------------
// redis driver
// ---------------------------------------------------------------------------

func (c *ClientIPConcurrency) acquireRedisClientIPSlot(ctx context.Context, input ClientIPConcurrencyAcquireInput, policy GroupSchedulingPolicy, key string, limit int) (ClientIPConcurrencyDecision, error) {
	startedAtMs := c.clock.Now().UnixMilli()
	firstSlotToken := redisClientIPConcurrencySlotToken()
	firstAttempt, err := c.tryAcquireRedisClientIPSlot(ctx, key, limit, true, firstSlotToken)
	if err != nil {
		return ClientIPConcurrencyDecision{}, err
	}
	if firstAttempt.acquired {
		return c.redisAcquiredDecision(key, firstSlotToken, firstAttempt.current, limit, 0, false, 0), nil
	}
	if policy.ClientIPConcurrencyOverflowMode != OverflowModeQueue {
		return rejectedDecisionConst(RejectLimitReached, firstAttempt.current, limit, 0, 0), nil
	}
	maxQueueWaitMs := policy.MaxQueueWaitMs
	if maxQueueWaitMs <= 0 {
		return rejectedDecisionConst(RejectQueueDisabled, firstAttempt.current, limit, 0, 0), nil
	}
	queueLimit := int(normalizePositiveInteger(policy.PerAPIKeyQueueLimit, int64(nonZero(c.defaults.PerAPIKeyQueueLimit, 1))))
	deadlineAtMs := startedAtMs + maxQueueWaitMs
	itemID := fmt.Sprintf("%d:%d:%s", os.Getpid(), startedAtMs, randomHex8())
	queuedSlotToken := redisClientIPConcurrencySlotToken()
	enqueued, err := c.enqueueRedisClientIPQueueItem(ctx, key, itemID, deadlineAtMs, queueLimit)
	if err != nil {
		return ClientIPConcurrencyDecision{}, err
	}
	if enqueued.status != "enqueued" {
		return rejectedDecisionConst(RejectQueueFull, firstAttempt.current, limit, 0, enqueued.queueSize), nil
	}
	current := firstAttempt.current
	for c.clock.Now().UnixMilli() < deadlineAtMs {
		if input.Signal != nil && input.Signal.Err() != nil {
			queueSize, err := c.removeRedisClientIPQueueItem(ctx, key, itemID)
			if err != nil {
				return ClientIPConcurrencyDecision{}, err
			}
			return rejectedDecisionConst(RejectAborted, current, limit, c.clock.Now().UnixMilli()-startedAtMs, queueSize), nil
		}
		position, err := c.redisClientIPQueuePosition(ctx, key, itemID)
		if err != nil {
			return ClientIPConcurrencyDecision{}, err
		}
		if !position.present {
			return rejectedDecisionConst(RejectTimeout, current, limit, c.clock.Now().UnixMilli()-startedAtMs, position.queueSize), nil
		}
		if position.rank == 0 {
			attempt, attemptErr := c.tryAcquireRedisClientIPSlot(ctx, key, limit, false, queuedSlotToken)
			if attemptErr != nil {
				return ClientIPConcurrencyDecision{}, attemptErr
			}
			current = attempt.current
			if attempt.acquired {
				queueSize, removeErr := c.removeRedisClientIPQueueItem(ctx, key, itemID)
				if removeErr != nil {
					c.releaseRedisClientIPSlotWithRetry(context.Background(), key, queuedSlotToken)
					return ClientIPConcurrencyDecision{}, removeErr
				}
				return c.redisAcquiredDecision(key, queuedSlotToken, attempt.current, limit, c.clock.Now().UnixMilli()-startedAtMs, true, queueSize+1), nil
			}
		}
		remaining := deadlineAtMs - c.clock.Now().UnixMilli()
		delayMs := int64(100)
		if remaining < delayMs {
			delayMs = remaining
		}
		if delayMs < 1 {
			delayMs = 1
		}
		c.sleep(time.Duration(delayMs) * time.Millisecond)
	}
	queueSize, err := c.removeRedisClientIPQueueItem(ctx, key, itemID)
	if err != nil {
		return ClientIPConcurrencyDecision{}, err
	}
	return rejectedDecisionConst(RejectTimeout, current, limit, c.clock.Now().UnixMilli()-startedAtMs, queueSize), nil
}

func (c *ClientIPConcurrency) tryAcquireRedisClientIPSlot(ctx context.Context, key string, limit int, requireEmptyQueue bool, slotToken string) (struct {
	acquired bool
	current  int
}, error) {
	result, err := c.redis.Eval(ctx, redisAcquireClientIPConcurrencyScript, []string{
		redisClientIPConcurrencyKey(key),
		redisClientIPQueueKey(key),
	}, strconvItoa(limit), strconvItoa(redisClientIPConcurrencyTTL), requireEmptyQueueBool(requireEmptyQueue), strconvIota64(c.clock.Now().UnixMilli()), slotToken).Result()
	if err != nil {
		return struct{ acquired bool; current int }{}, err
	}
	values := numericRedisArray(result)
	out := struct {
		acquired bool
		current  int
	}{}
	out.acquired = len(values) > 0 && values[0] == 1
	if len(values) > 1 {
		out.current = int(values[1])
	}
	return out, nil
}

func requireEmptyQueueBool(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

// redisAcquiredDecision mirrors redisAcquiredDecision: starts slot renewal.
func (c *ClientIPConcurrency) redisAcquiredDecision(key string, slotToken string, current int, limit int, waitedMs int64, queued bool, queueSizeBeforeAcquire int) ClientIPConcurrencyDecision {
	stopRenewal := c.startRedisClientIPSlotRenewal(key, slotToken)
	var once sync.Once
	return ClientIPConcurrencyDecision{
		Enabled:                true,
		Acquired:               true,
		Current:                current,
		Limit:                  limit,
		WaitedMs:               maxInt64(0, waitedMs),
		Queued:                 queued,
		QueueSizeBeforeAcquire: queueSizeBeforeAcquire,
		release: func() {
			once.Do(func() {
				stopRenewal()
				c.releaseRedisClientIPSlotWithRetry(context.Background(), key, slotToken)
			})
		},
	}
}

// startRedisClientIPSlotRenewal mirrors startRedisClientIpSlotRenewal.
func (c *ClientIPConcurrency) startRedisClientIPSlotRenewal(key string, slotToken string) func() {
	stopped := make(chan struct{})
	var stopOnce sync.Once
	ticker := time.NewTicker(redisClientIPConcurrencyRenewInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), stateOperationTimeout)
				renewed, err := c.renewRedisClientIPSlot(ctx, key, slotToken)
				cancel()
				if err != nil {
					if c.logger != nil {
						c.logger.Warn("redis_client_ip_concurrency_renew_failed", map[string]any{"key": key, "err": err.Error()}, "Redis Client-IP 并发槽续租失败")
					}
					continue
				}
				if !renewed {
					stopOnce.Do(func() { close(stopped) })
				}
			}
		}
	}()
	return func() { stopOnce.Do(func() { close(stopped) }) }
}

func (c *ClientIPConcurrency) renewRedisClientIPSlot(ctx context.Context, key string, slotToken string) (bool, error) {
	result, err := c.redis.Eval(ctx, redisRenewClientIPConcurrencyScript, []string{redisClientIPConcurrencyKey(key)},
		strconvIota64(c.clock.Now().UnixMilli()), strconvItoa(redisClientIPConcurrencyTTL), slotToken).Result()
	if err != nil {
		return false, err
	}
	values := numericRedisArray(result)
	return len(values) > 0 && values[0] == 1, nil
}

// releaseRedisClientIPSlotWithRetry mirrors releaseRedisClientIpSlotWithRetry
// (delays 0/250/1000/5000ms, then one error warn).
func (c *ClientIPConcurrency) releaseRedisClientIPSlotWithRetry(ctx context.Context, key string, slotToken string) {
	delays := []time.Duration{0, 250 * time.Millisecond, 1000 * time.Millisecond, 5000 * time.Millisecond}
	var lastErr error
	for _, delayMs := range delays {
		if delayMs > 0 {
			c.sleep(delayMs)
		}
		runCtx, cancel := context.WithTimeout(ctx, stateOperationTimeout)
		err := c.redis.Eval(runCtx, redisReleaseClientIPConcurrencyScript, []string{redisClientIPConcurrencyKey(key)}, slotToken).Err()
		cancel()
		if err == nil {
			return
		}
		lastErr = err
	}
	if c.logger != nil {
		message := ""
		if lastErr != nil {
			message = lastErr.Error()
		}
		c.logger.Warn("redis_client_ip_concurrency_release_failed", map[string]any{"key": key, "err": message}, "Redis Client-IP 并发槽释放失败")
	}
}

func (c *ClientIPConcurrency) enqueueRedisClientIPQueueItem(ctx context.Context, key string, itemID string, deadlineAtMs int64, queueLimit int) (struct {
	status    string
	queueSize int
}, error) {
	nowMs := c.clock.Now().UnixMilli()
	ttlMs := deadlineAtMs - nowMs + 60_000
	if ttlMs < 1 {
		ttlMs = 1
	}
	result, err := c.redis.Eval(ctx, redisClientIPQueueEnqueueScript, []string{redisClientIPQueueKey(key)},
		itemID, strconvIota64(clampNonNegative(deadlineAtMs)), strconvIota64(clampNonNegative(nowMs)),
		strconvItoa(maxInt(1, queueLimit)), strconvIota64(ttlMs)).Result()
	if err != nil {
		return struct {
			status    string
			queueSize int
		}{}, err
	}
	values := numericRedisArray(result)
	out := struct {
		status    string
		queueSize int
	}{status: "queue_full"}
	if len(values) > 0 && values[0] == 1 {
		out.status = "enqueued"
	}
	if len(values) > 1 {
		out.queueSize = int(values[1])
	}
	return out, nil
}

func (c *ClientIPConcurrency) redisClientIPQueuePosition(ctx context.Context, key string, itemID string) (struct {
	present   bool
	rank      int
	queueSize int
}, error) {
	result, err := c.redis.Eval(ctx, redisClientIPQueuePositionScript, []string{redisClientIPQueueKey(key)},
		itemID, strconvIota64(clampNonNegative(c.clock.Now().UnixMilli()))).Result()
	if err != nil {
		return struct {
			present   bool
			rank      int
			queueSize int
		}{}, err
	}
	values := numericRedisArray(result)
	out := struct {
		present   bool
		rank      int
		queueSize int
	}{rank: -1}
	if len(values) > 0 && values[0] == 1 {
		out.present = true
	}
	if len(values) > 1 {
		out.rank = int(values[1])
	}
	if len(values) > 2 {
		out.queueSize = int(values[2])
	}
	return out, nil
}

func (c *ClientIPConcurrency) removeRedisClientIPQueueItem(ctx context.Context, key string, itemID string) (int, error) {
	result, err := c.redis.Eval(ctx, redisClientIPQueueRemoveScript, []string{redisClientIPQueueKey(key)},
		itemID, strconvIota64(clampNonNegative(c.clock.Now().UnixMilli()))).Result()
	if err != nil {
		return 0, err
	}
	values := numericRedisArray(result)
	if len(values) == 0 {
		return 0, nil
	}
	return int(values[0]), nil
}

// ---------------------------------------------------------------------------
// keys + scripts
// ---------------------------------------------------------------------------

// clientIPConcurrencyKey mirrors clientIpConcurrencyKey.
func clientIPConcurrencyKey(systemAccountID string, groupID string, apiKeyID string, clientIP string) string {
	apiKeyPart := strings.TrimSpace(apiKeyID)
	if apiKeyPart == "" {
		apiKeyPart = "internal"
	}
	return systemAccountID + ":" + groupID + ":" + apiKeyPart + ":" + clientIP
}

// redisClientIPConcurrencyKey mirrors redisClientIpConcurrencyKey. Node
// applies a raw juhe-ai: prefix here (not redisNamespacedKey); ported as-is.
func redisClientIPConcurrencyKey(key string) string {
	return "juhe-ai:client-ip-concurrency:" + base64.RawURLEncoding.EncodeToString([]byte(key))
}

// redisClientIPQueueKey mirrors redisClientIpQueueKey.
func redisClientIPQueueKey(key string) string {
	return "juhe-ai:client-ip-concurrency-queue:" + base64.RawURLEncoding.EncodeToString([]byte(key))
}

// redisClientIPConcurrencySlotToken mirrors redisClientIpConcurrencySlotToken.
func redisClientIPConcurrencySlotToken() string {
	return fmt.Sprintf("%d:%d:%s", os.Getpid(), time.Now().UnixMilli(), randomHex8())
}

func randomHex8() string {
	buffer := make([]byte, 8)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}

func numericRedisArray(value any) []int64 {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	output := make([]int64, 0, len(values))
	for _, item := range values {
		switch number := item.(type) {
		case int64:
			output = append(output, number)
		case int:
			output = append(output, int64(number))
		case string:
			var parsed int64
			_, _ = fmt.Sscanf(number, "%d", &parsed)
			output = append(output, parsed)
		default:
			output = append(output, 0)
		}
	}
	return output
}

func strconvItoa(value int) string { return fmt.Sprintf("%d", value) }
func strconvIota64(value int64) string { return fmt.Sprintf("%d", value) }
func clampNonNegative(value int64) int64 { return maxInt64(0, value) }
func maxInt(a, b int) int { if a > b { return a }; return b }

const redisAcquireClientIPConcurrencyScript = `
local limit = tonumber(ARGV[1])
local slot_ttl_ms = tonumber(ARGV[2])
local require_empty_queue = ARGV[3] == '1'
local now_ms = tonumber(ARGV[4])
local slot_token = ARGV[5]
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
local current = tonumber(redis.call('ZCARD', KEYS[1]) or '0') or 0
if require_empty_queue then
  redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms)
  if redis.call('ZCARD', KEYS[2]) > 0 then
    return {0, current}
  end
end
if current >= limit then
  return {0, current}
end
redis.call('ZADD', KEYS[1], now_ms + slot_ttl_ms, slot_token)
redis.call('PEXPIRE', KEYS[1], slot_ttl_ms)
return {1, current + 1}
`

const redisReleaseClientIPConcurrencyScript = `
local slot_token = ARGV[1]
redis.call('ZREM', KEYS[1], slot_token)
if redis.call('ZCARD', KEYS[1]) == 0 then
  redis.call('DEL', KEYS[1])
end
return 1
`

const redisRenewClientIPConcurrencyScript = `
local now_ms = tonumber(ARGV[1])
local slot_ttl_ms = tonumber(ARGV[2])
local slot_token = ARGV[3]
local current_score = tonumber(redis.call('ZSCORE', KEYS[1], slot_token))
if not current_score then
  return {0}
end
if current_score <= now_ms then
  redis.call('ZREM', KEYS[1], slot_token)
  return {0}
end
redis.call('ZADD', KEYS[1], now_ms + slot_ttl_ms, slot_token)
redis.call('PEXPIRE', KEYS[1], slot_ttl_ms)
return {1}
`

const redisClientIPQueueEnqueueScript = `
local item_id = ARGV[1]
local deadline_at_ms = tonumber(ARGV[2])
local now_ms = tonumber(ARGV[3])
local queue_limit = tonumber(ARGV[4])
local ttl_ms = tonumber(ARGV[5])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
local queue_size = redis.call('ZCARD', KEYS[1])
if queue_size >= queue_limit then
  return {0, queue_size}
end
redis.call('ZADD', KEYS[1], deadline_at_ms, item_id)
redis.call('PEXPIRE', KEYS[1], ttl_ms)
return {1, queue_size + 1}
`

const redisClientIPQueuePositionScript = `
local item_id = ARGV[1]
local now_ms = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
local rank = redis.call('ZRANK', KEYS[1], item_id)
if rank == false then
  return {0, -1, redis.call('ZCARD', KEYS[1])}
end
return {1, rank, redis.call('ZCARD', KEYS[1])}
`

const redisClientIPQueueRemoveScript = `
local item_id = ARGV[1]
local now_ms = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
redis.call('ZREM', KEYS[1], item_id)
return {redis.call('ZCARD', KEYS[1])}
`
