package gatewayproxyhealth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Ports runtime/user-request-limit-coordinator.ts: the background single-flight
// Redis sync of dirty user request limit counters.

const (
	userRequestLimitSyncIntervalMs      = int64(1_000)
	userRequestLimitMaxBatchSize        = 1_024
	userRequestLimitErrorLogIntervalMs  = int64(30_000)
	userRequestLimitCapacityLogInterval = int64(30_000)
	userRequestLimitRedisCommandTimeout = int64(3_000)
	userRequestLimitMaxRetryBackoffMs   = int64(30_000)
)

// UserRequestLimitRedisSyncScript mirrors userRequestLimitRedisSyncScript:
// per-instance field merge with monotonic __total and PEXPIRE refresh.
const UserRequestLimitRedisSyncScript = `
local result = {}
local field = ARGV[1]
for index = 1, #KEYS do
  local count = ARGV[index * 2]
  local ttl = tonumber(ARGV[(index * 2) + 1])
  local previous = tonumber(redis.call('HGET', KEYS[index], field)) or 0
  local total = tonumber(redis.call('HGET', KEYS[index], '__total')) or 0
  local delta = tonumber(count) - previous
  if delta > 0 then
    total = redis.call('HINCRBY', KEYS[index], '__total', delta)
    redis.call('HSET', KEYS[index], field, count)
  end
  redis.call('PEXPIRE', KEYS[index], ttl)
  result[index] = total
end
return result
`

// UserRequestLimitRedisClient is the command surface the coordinator needs
// (Node RedisCommandClient.eval). Wrap go-redis Eval(...).Result().
type UserRequestLimitRedisClient interface {
	Eval(ctx context.Context, script string, keys []string, args ...any) (any, error)
}

// UserRequestLimitRedisClientProvider mirrors getRedisClient(stateUrl) plus
// invalidateRedisClient on failure.
type UserRequestLimitRedisClientProvider interface {
	Client(ctx context.Context) (UserRequestLimitRedisClient, error)
	Invalidate(ctx context.Context, client UserRequestLimitRedisClient)
}

// UserRequestLimitCoordinatorLogFunc receives the Node logger payloads.
type UserRequestLimitCoordinatorLogFunc func(fields map[string]any, message string)

// UserRequestLimitCoordinatorOptions carries the driver wiring and test hooks.
type UserRequestLimitCoordinatorOptions struct {
	// RedisEnabled mirrors runtimeConfig.runtimeStateDriver === 'redis'.
	RedisEnabled   bool
	Namespace      string // runtimeConfig.redis.namespace
	ClientProvider UserRequestLimitRedisClientProvider
	Log            UserRequestLimitCoordinatorLogFunc
	// Random feeds the passive-schedule jitter; nil uses the default.
	Random func() float64
	// Sleep overrides both the coordinator tick wait and the 20ms stop-poll
	// delay (tests inject a no-op / manual pump).
	Sleep func(d time.Duration)
	// ServerInstanceID mirrors `${process.pid}-${randomBytes(6).toString('hex')}`.
	ServerInstanceID string
}

// UserRequestLimitCoordinator mirrors the coordinator module.
type UserRequestLimitCoordinator struct {
	counter *UserRequestLimitCounter
	clock   Clock
	opts    UserRequestLimitCoordinatorOptions

	mu                  sync.Mutex
	started             bool
	stopCh              chan struct{}
	loopDone            chan struct{}
	syncInFlight        bool
	consecutiveFailures int
	nextSyncAttemptAtMs int64
	lastErrorLogAtMs    int64
	lastCapacityLogAtMs int64
	lastLoggedEvictions int64

	loopStarted atomic.Bool
}

// NewUserRequestLimitCoordinator builds the coordinator for one counter.
func NewUserRequestLimitCoordinator(counter *UserRequestLimitCounter, clock Clock, opts UserRequestLimitCoordinatorOptions) *UserRequestLimitCoordinator {
	if opts.ServerInstanceID == "" {
		opts.ServerInstanceID = fmt.Sprintf("%d-%s", os.Getpid(), NewRandomHex(6))
	}
	return &UserRequestLimitCoordinator{
		counter: counter,
		clock:   clock,
		opts:    opts,
		stopCh:  make(chan struct{}),
	}
}

// StartCoordinator mirrors startUserRequestLimitCoordinator: idempotent.
func (c *UserRequestLimitCoordinator) StartCoordinator() {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	c.started = true
	c.stopCh = make(chan struct{})
	c.loopDone = make(chan struct{})
	stopCh := c.stopCh
	done := c.loopDone
	c.mu.Unlock()
	go c.loop(stopCh, done)
}

func (c *UserRequestLimitCoordinator) loop(stopCh, done chan struct{}) {
	defer close(done)
	for {
		delayMs := PassiveScheduleDelayMs(userRequestLimitSyncIntervalMs, c.opts.Random)
		timer := time.NewTimer(time.Duration(delayMs) * time.Millisecond)
		select {
		case <-stopCh:
			timer.Stop()
			return
		case <-timer.C:
			c.runScheduledTick()
		}
	}
}

func (c *UserRequestLimitCoordinator) runScheduledTick() {
	c.counter.CleanupExpired(nil, nil)
	c.logCapacityPressure()
	if c.opts.RedisEnabled {
		go func() { _ = c.SynchronizeDirtyCounters(false) }()
	}
}

// coordinatorSleep sleeps through the injected hook (default time.Sleep).
func (c *UserRequestLimitCoordinator) coordinatorSleep(d time.Duration) {
	if c.opts.Sleep != nil {
		c.opts.Sleep(d)
		return
	}
	time.Sleep(d)
}

// StopCoordinator mirrors stopUserRequestLimitCoordinator: stop the loop, wait
// for an in-flight sync, then drain dirty counters via forced syncs. Returns
// true when all dirty state reached Redis (or nothing had to be done).
func (c *UserRequestLimitCoordinator) StopCoordinator(timeoutMs *int64) bool {
	normalizedTimeout := userRequestLimitRedisCommandTimeout
	if timeoutMs != nil && *timeoutMs > 0 {
		normalizedTimeout = *timeoutMs
	}
	c.mu.Lock()
	if c.stopCh != nil {
		select {
		case <-c.stopCh:
		default:
			close(c.stopCh)
		}
	}
	c.started = false
	stopCh := c.stopCh
	loopDone := c.loopDone
	c.mu.Unlock()
	if stopCh != nil && loopDone != nil {
		select {
		case <-loopDone:
		default:
		}
	}

	deadlineAtMs := ClockNowMs(c.clock) + maxInt64(1, normalizedTimeout)
	for c.isSyncInFlight() && ClockNowMs(c.clock) < deadlineAtMs {
		c.coordinatorSleep(20 * time.Millisecond)
	}
	if c.isSyncInFlight() || !c.opts.RedisEnabled {
		return !c.isSyncInFlight()
	}
	c.setNextSyncAttemptAtMs(0)
	for {
		stats := c.counter.Stats()
		if stats.DirtyEntries == 0 || ClockNowMs(c.clock) >= deadlineAtMs {
			if stats.DirtyEntries == 0 {
				return true
			}
			break
		}
		dirtyBefore := stats.DirtyEntries
		if err := c.synchronizeWithTimeout(maxInt64(1, deadlineAtMs-ClockNowMs(c.clock)), "用户请求限制退出同步超时"); err != nil {
			return false
		}
		if c.counter.Stats().DirtyEntries >= dirtyBefore {
			return false
		}
	}
	return c.counter.Stats().DirtyEntries == 0
}

func (c *UserRequestLimitCoordinator) isSyncInFlight() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.syncInFlight
}

func (c *UserRequestLimitCoordinator) setNextSyncAttemptAtMs(value int64) {
	c.mu.Lock()
	c.nextSyncAttemptAtMs = value
	c.mu.Unlock()
}

// SynchronizeDirtyCounters mirrors synchronizeDirtyCounters (single-flight).
func (c *UserRequestLimitCoordinator) SynchronizeDirtyCounters(force bool) error {
	c.mu.Lock()
	if c.syncInFlight {
		c.mu.Unlock()
		return nil
	}
	nowMs := ClockNowMs(c.clock)
	if !force && nowMs < c.nextSyncAttemptAtMs {
		c.mu.Unlock()
		return nil
	}
	if !c.opts.RedisEnabled || c.opts.ClientProvider == nil {
		// Node: missing stateUrl returns silently; the memory counters stay.
		c.mu.Unlock()
		return nil
	}
	batch := c.counter.DirtySnapshot(userRequestLimitMaxBatchSize)
	if len(batch) == 0 {
		c.mu.Unlock()
		return nil
	}
	c.syncInFlight = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.syncInFlight = false
		c.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(userRequestLimitRedisCommandTimeout)*time.Millisecond)
	defer cancel()
	client, err := c.opts.ClientProvider.Client(ctx)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = errors.New("用户请求限制 Redis 连接超时")
		}
		c.handleSyncError(nowMs, client, err)
		return err
	}
	keys := make([]string, 0, len(batch))
	args := make([]any, 0, 1+2*len(batch))
	args = append(args, c.opts.ServerInstanceID)
	for _, entry := range batch {
		keys = append(keys, c.redisKey(entry))
		args = append(args, formatSyncInt64(entry.LocalCount), formatSyncInt64(entry.RedisTTLms))
	}
	raw, err := c.evalWithTimeout(ctx, client, keys, args)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = errors.New("用户请求限制 Redis 命令超时")
		}
		c.handleSyncError(nowMs, client, err)
		return err
	}
	totals, _ := raw.([]any)
	results := make([]UserRequestLimitSyncResult, 0, len(batch))
	for index, entry := range batch {
		var remoteTotal int64
		if index < len(totals) {
			remoteTotal = numericSyncValue(totals[index])
		}
		results = append(results, UserRequestLimitSyncResult{
			EntryKey:       entry.EntryKey,
			SentLocalCount: entry.LocalCount,
			RemoteTotal:    remoteTotal,
		})
	}
	c.counter.ApplySyncResults(results)
	c.mu.Lock()
	c.consecutiveFailures = 0
	c.nextSyncAttemptAtMs = 0
	c.mu.Unlock()
	return nil
}

func (c *UserRequestLimitCoordinator) evalWithTimeout(ctx context.Context, client UserRequestLimitRedisClient, keys []string, args []any) (any, error) {
	type evalResult struct {
		value any
		err   error
	}
	done := make(chan evalResult, 1)
	go func() {
		value, err := client.Eval(ctx, UserRequestLimitRedisSyncScript, keys, args...)
		done <- evalResult{value: value, err: err}
	}()
	select {
	case result := <-done:
		return result.value, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *UserRequestLimitCoordinator) handleSyncError(nowMs int64, client UserRequestLimitRedisClient, syncErr error) {
	c.mu.Lock()
	c.consecutiveFailures++
	backoff := maxInt64(userRequestLimitSyncIntervalMs, 0)
	exponent := c.consecutiveFailures - 1
	if exponent > 5 {
		exponent = 5
	}
	if exponent < 0 {
		exponent = 0
	}
	backoff = userRequestLimitSyncIntervalMs * (1 << uint(exponent))
	if backoff > userRequestLimitMaxRetryBackoffMs {
		backoff = userRequestLimitMaxRetryBackoffMs
	}
	c.nextSyncAttemptAtMs = nowMs + backoff
	retryAfterMs := maxInt64(0, c.nextSyncAttemptAtMs-nowMs)
	shouldLog := nowMs-c.lastErrorLogAtMs >= userRequestLimitErrorLogIntervalMs
	if shouldLog {
		c.lastErrorLogAtMs = nowMs
	}
	consecutiveFailures := c.consecutiveFailures
	c.mu.Unlock()
	if client != nil && c.opts.ClientProvider != nil {
		c.opts.ClientProvider.Invalidate(context.Background(), client)
	}
	if shouldLog && c.opts.Log != nil {
		c.opts.Log(map[string]any{
			"event":               "gateway_user_request_limit_redis_sync_failed",
			"error":               syncErr.Error(),
			"consecutiveFailures": consecutiveFailures,
			"retryAfterMs":        retryAfterMs,
		}, "用户请求限制 Redis 后台同步失败，继续使用本机内存计数")
	}
}

func (c *UserRequestLimitCoordinator) synchronizeWithTimeout(timeoutMs int64, message string) error {
	done := make(chan error, 1)
	go func() { done <- c.SynchronizeDirtyCounters(true) }()
	timer := time.NewTimer(time.Duration(maxInt64(1, timeoutMs)) * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return errors.New(message)
	}
}

func (c *UserRequestLimitCoordinator) logCapacityPressure() {
	stats := c.counter.Stats()
	if int64(stats.CapacityEvictions) <= c.lastLoggedCapacityEvictions() {
		return
	}
	nowMs := ClockNowMs(c.clock)
	c.mu.Lock()
	if nowMs-c.lastCapacityLogAtMs < userRequestLimitCapacityLogInterval {
		c.mu.Unlock()
		return
	}
	c.lastCapacityLogAtMs = nowMs
	c.lastLoggedEvictions = int64(stats.CapacityEvictions)
	c.mu.Unlock()
	if c.opts.Log != nil {
		c.opts.Log(map[string]any{
			"event":             "gateway_user_request_limit_capacity_exhausted",
			"entries":           stats.Entries,
			"dirtyEntries":      stats.DirtyEntries,
			"capacityEvictions": stats.CapacityEvictions,
		}, "用户请求限制本机计数容量已满，已淘汰最旧桶以维持固定内存上限")
	}
}

func (c *UserRequestLimitCoordinator) lastLoggedCapacityEvictions() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastLoggedEvictions
}

// redisKey mirrors redisKey: `<namespace>:gateway:user-request-limit:<window>:<bucket>:<systemAccountId>`.
func (c *UserRequestLimitCoordinator) redisKey(entry UserRequestLimitDirtySnapshot) string {
	return c.opts.Namespace + ":gateway:user-request-limit:" + string(entry.Window) + ":" + entry.Bucket + ":" + entry.SystemAccountID
}

func numericSyncValue(value any) int64 {
	normalized := numericSyncFloat(value)
	if normalized >= 0 {
		return int64(normalized)
	}
	return 0
}

func numericSyncFloat(value any) float64 {
	switch v := value.(type) {
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case float64:
		return v
	case string:
		var parsed float64
		if _, err := fmt.Sscanf(v, "%g", &parsed); err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

func formatSyncInt64(value int64) string {
	return formatInt64Value(value)
}

func formatInt64Value(value int64) string {
	negative := value < 0
	unsigned := value
	if negative {
		unsigned = -value
	}
	if unsigned == 0 {
		return "0"
	}
	digits := make([]byte, 0, 20)
	for unsigned > 0 {
		digits = append(digits, byte('0'+unsigned%10))
		unsigned /= 10
	}
	if negative {
		digits = append(digits, '-')
	}
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return string(digits)
}
