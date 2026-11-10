package gatewayclientip

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayscheduling"
)

// Reject reasons mirror ClientIpConcurrencyRejectReason. The queue overflow
// mode was retired with high_concurrency groups; limit reached is always an
// immediate rejection.
const (
	RejectLimitReached = "limit_reached"
	RejectAborted      = "aborted"
)

const (
	redisClientIPConcurrencyTTL           = 180_000 // ms
	redisClientIPConcurrencyRenewInterval = 30 * time.Second
)

// ClientIPConcurrencyAcquireInput mirrors the ordinary-group IP gate input.
type ClientIPConcurrencyAcquireInput struct {
	SystemAccountID string
	GroupID         string
	APIKeyID        string
	ClientIP        string
	// Policy is the two-key ordinary-group policy (nil means disabled).
	Policy *gatewayscheduling.Policy
	Signal context.Context
}

// ClientIPConcurrencyDecision mirrors ClientIpConcurrencyDecision. Queue
// fields are retained as zero values for response-shape compatibility.
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

func (d *ClientIPConcurrencyDecision) Release() {
	if d == nil || d.release == nil {
		return
	}
	d.release()
}

type ClientIPConcurrencySnapshotRow struct {
	Key       string
	Current   int
	QueueSize int
}

type ClientIPConcurrencyOptions struct {
	Clock  Clock
	Logger Logger
	// RuntimeStateDriver mirrors runtimeConfig.runtimeStateDriver.
	RuntimeStateDriver string
	// StateRedisURL mirrors runtimeConfig.redis.stateUrl.
	StateRedisURL string
	// RedisNamespace is the domain-qualified key namespace.
	RedisNamespace string
	// Scheduler / Sleep are retained for constructor compatibility; the
	// queue implementation is gone, so they are unused.
	Scheduler FlushScheduler
	Sleep     func(d time.Duration)
}

type ClientIPConcurrency struct {
	clock  Clock
	logger Logger
	driver string

	redis     redis.UniversalClient
	closeFns  []func()
	namespace string

	mu     sync.Mutex
	states map[string]*clientIPConcurrencyState
}

type clientIPConcurrencyState struct {
	key     string
	limit   int
	current int
}

func NewClientIPConcurrency(opts ClientIPConcurrencyOptions) (*ClientIPConcurrency, error) {
	clock := opts.Clock
	if clock == nil {
		clock = systemClock()
	}
	c := &ClientIPConcurrency{
		clock:  clock,
		logger: opts.Logger,
		driver: opts.RuntimeStateDriver,
		states: map[string]*clientIPConcurrencyState{},
	}
	if opts.RuntimeStateDriver == RuntimeStateDriverRedis {
		if strings.TrimSpace(opts.StateRedisURL) == "" {
			return nil, errors.New("JUHE_AI_REDIS_URL 在 Redis runtime state driver 下必须配置")
		}
		client, err := rediscfg.NewUniversalClient(rediscfg.Config{URL: opts.StateRedisURL})
		if err != nil {
			return nil, err
		}
		c.redis = client
		c.namespace = opts.RedisNamespace
		c.closeFns = append(c.closeFns, func() { _ = client.Close() })
	}
	return c, nil
}

func (c *ClientIPConcurrency) Close() {
	for _, closeFn := range c.closeFns {
		closeFn()
	}
	c.closeFns = nil
}

// AcquireClientIPSlot acquires one per-client-IP slot; limit 0
// or blank IP is the disabled fast path.
func (c *ClientIPConcurrency) AcquireClientIPSlot(ctx context.Context, input ClientIPConcurrencyAcquireInput) (ClientIPConcurrencyDecision, error) {
	limit := 0
	if input.Policy != nil {
		limit = input.Policy.ClientIPConcurrencyLimit
	}
	clientIP := strings.TrimSpace(input.ClientIP)
	if clientIP == "" || limit <= 0 {
		return ClientIPConcurrencyDecision{Enabled: false, Acquired: true}, nil
	}
	if input.Signal != nil && input.Signal.Err() != nil {
		return rejectedDecisionConst(RejectAborted, 0, limit), nil
	}
	key := clientIPConcurrencyKey(input.SystemAccountID, input.GroupID, input.APIKeyID, clientIP)
	if c.driver == RuntimeStateDriverRedis {
		return c.acquireRedisClientIPSlot(ctx, key, limit)
	}
	return c.acquireLocalClientIPSlot(key, limit), nil
}

func (c *ClientIPConcurrency) acquireLocalClientIPSlot(key string, limit int) ClientIPConcurrencyDecision {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.states[key]
	if !ok {
		state = &clientIPConcurrencyState{key: key}
		c.states[key] = state
	}
	state.limit = limit
	if state.current >= limit {
		return rejectedDecisionConst(RejectLimitReached, state.current, limit)
	}
	state.current++
	current := state.current
	var once sync.Once
	return ClientIPConcurrencyDecision{
		Enabled:  true,
		Acquired: true,
		Current:  current,
		Limit:    limit,
		release: func() {
			once.Do(func() { c.releaseLocalClientIPSlot(key) })
		},
	}
}

func (c *ClientIPConcurrency) releaseLocalClientIPSlot(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.states[key]
	if !ok {
		return
	}
	if state.current > 0 {
		state.current--
	}
	if state.current <= 0 {
		delete(c.states, key)
	}
}

func (c *ClientIPConcurrency) acquireRedisClientIPSlot(ctx context.Context, key string, limit int) (ClientIPConcurrencyDecision, error) {
	slotToken := redisClientIPConcurrencySlotToken()
	attempt, err := c.tryAcquireRedisClientIPSlot(ctx, key, limit, slotToken)
	if err != nil {
		return ClientIPConcurrencyDecision{}, err
	}
	if !attempt.acquired {
		return rejectedDecisionConst(RejectLimitReached, attempt.current, limit), nil
	}
	stopRenewal := c.startRedisClientIPSlotRenewal(key, slotToken)
	var once sync.Once
	return ClientIPConcurrencyDecision{
		Enabled:  true,
		Acquired: true,
		Current:  attempt.current,
		Limit:    limit,
		release: func() {
			once.Do(func() {
				stopRenewal()
				c.releaseRedisClientIPSlotWithRetry(context.Background(), key, slotToken)
			})
		},
	}, nil
}

func (c *ClientIPConcurrency) tryAcquireRedisClientIPSlot(ctx context.Context, key string, limit int, slotToken string) (struct {
	acquired bool
	current  int
}, error) {
	result, err := c.redis.Eval(ctx, redisAcquireClientIPConcurrencyScript, []string{
		redisClientIPConcurrencyKeyNS(c.namespace, key),
	}, strconvItoa(limit), strconvItoa(redisClientIPConcurrencyTTL), strconvIota64(c.clock.Now().UnixMilli()), slotToken).Result()
	if err != nil {
		return struct {
			acquired bool
			current  int
		}{}, err
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

func (c *ClientIPConcurrency) startRedisClientIPSlotRenewal(key string, slotToken string) func() {
	stopped := make(chan struct{})
	var stopOnce sync.Once
	ticker := time.NewTicker(redisClientIPConcurrencyRenewInterval)
	go func() {
		defer safego.Recover("gatewayclientip.concurrency.slot_renewal")
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
	result, err := c.redis.Eval(ctx, redisRenewClientIPConcurrencyScript, []string{redisClientIPConcurrencyKeyNS(c.namespace, key)},
		strconvIota64(c.clock.Now().UnixMilli()), strconvItoa(redisClientIPConcurrencyTTL), slotToken).Result()
	if err != nil {
		return false, err
	}
	values := numericRedisArray(result)
	return len(values) > 0 && values[0] == 1, nil
}

func (c *ClientIPConcurrency) releaseRedisClientIPSlotWithRetry(ctx context.Context, key string, slotToken string) {
	delays := []time.Duration{0, 250 * time.Millisecond, 1000 * time.Millisecond, 5000 * time.Millisecond}
	var lastErr error
	for _, delay := range delays {
		if delay > 0 {
			time.Sleep(delay)
		}
		runCtx, cancel := context.WithTimeout(ctx, stateOperationTimeout)
		err := c.redis.Eval(runCtx, redisReleaseClientIPConcurrencyScript, []string{redisClientIPConcurrencyKeyNS(c.namespace, key)}, slotToken).Err()
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

func (c *ClientIPConcurrency) Snapshot() []ClientIPConcurrencySnapshotRow {
	if c.driver == RuntimeStateDriverRedis {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rows := make([]ClientIPConcurrencySnapshotRow, 0, len(c.states))
	for _, state := range c.states {
		rows = append(rows, ClientIPConcurrencySnapshotRow{Key: state.key, Current: state.current})
	}
	return rows
}

func (c *ClientIPConcurrency) Clear() {
	c.mu.Lock()
	c.states = map[string]*clientIPConcurrencyState{}
	c.mu.Unlock()
}

func rejectedDecisionConst(reason string, current int, limit int) ClientIPConcurrencyDecision {
	return ClientIPConcurrencyDecision{
		Enabled:  true,
		Acquired: false,
		Reason:   reason,
		Current:  current,
		Limit:    limit,
	}
}

func clientIPConcurrencyKey(systemAccountID string, groupID string, apiKeyID string, clientIP string) string {
	apiKey := strings.TrimSpace(apiKeyID)
	if apiKey == "" {
		apiKey = "internal"
	}
	return systemAccountID + ":" + groupID + ":" + apiKey + ":" + clientIP
}

func redisClientIPConcurrencyKey(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

func redisClientIPConcurrencyKeyNS(namespace, key string) string {
	return namespace + ":client-ip-concurrency:" + redisClientIPConcurrencyKey(key)
}

func redisClientIPConcurrencySlotToken() string {
	return fmt.Sprintf("%d:%s", time.Now().UnixNano(), randomHex8())
}

func randomHex8() string {
	buffer := make([]byte, 4)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

func numericRedisArray(value any) []int64 {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]int64, 0, len(items))
	for _, item := range items {
		switch typed := item.(type) {
		case int64:
			out = append(out, typed)
		case int:
			out = append(out, int64(typed))
		case string:
			var parsed int64
			if _, err := fmt.Sscan(typed, &parsed); err == nil {
				out = append(out, parsed)
			}
		default:
			out = append(out, 0)
		}
	}
	return out
}

func strconvItoa(value int) string     { return fmt.Sprintf("%d", value) }
func strconvIota64(value int64) string { return fmt.Sprintf("%d", value) }

const redisAcquireClientIPConcurrencyScript = `
local limit = tonumber(ARGV[1])
local slot_ttl_ms = tonumber(ARGV[2])
local now_ms = tonumber(ARGV[3])
local slot_token = ARGV[4]
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
local current = tonumber(redis.call('ZCARD', KEYS[1]) or '0') or 0
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
