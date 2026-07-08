package redis

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

var ErrNotFound = errors.New("redis key not found")

var incrWithTTLScript = goredis.NewScript(`
local value = redis.call("INCR", KEYS[1])
if value == 1 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return value
`)

var fixedWindowRateLimitScript = goredis.NewScript(`
local retry_after_ms = 0
for i = 1, #KEYS do
  local limit = tonumber(ARGV[(i - 1) * 2 + 1])
  local window_ms = tonumber(ARGV[(i - 1) * 2 + 2])
  if limit ~= nil and window_ms ~= nil and limit > 0 then
    local current = tonumber(redis.call("GET", KEYS[i]) or "0")
    if current >= limit then
      local ttl = redis.call("PTTL", KEYS[i])
      if ttl < 0 then
        ttl = window_ms
        redis.call("PEXPIRE", KEYS[i], ttl)
      end
      if ttl <= 0 then
        ttl = 1
      end
      if ttl > retry_after_ms then
        retry_after_ms = ttl
      end
    end
  end
end

if retry_after_ms > 0 then
  return {0, math.ceil(retry_after_ms / 1000)}
end

for i = 1, #KEYS do
  local limit = tonumber(ARGV[(i - 1) * 2 + 1])
  local window_ms = tonumber(ARGV[(i - 1) * 2 + 2])
  if limit ~= nil and window_ms ~= nil and limit > 0 then
    local value = redis.call("INCR", KEYS[i])
    if value == 1 or redis.call("PTTL", KEYS[i]) < 0 then
      redis.call("PEXPIRE", KEYS[i], window_ms)
    end
  end
end

return {1, 0}
`)

var penaltyWindowRateLimitScript = goredis.NewScript(`
local now_ms = tonumber(ARGV[1])
local rule_count = tonumber(ARGV[2])
local counts = {}
local penalty_values = {}
local window_started_values = {}
local ttl_values = {}
local blocked_index = 0
local blocked_retry_ms = 0

for index = 1, rule_count do
  local offset = 3 + (index - 1) * 5
  local window_ms = tonumber(ARGV[offset])
  local window_started_at = tonumber(ARGV[offset + 1])
  local max_requests = tonumber(ARGV[offset + 2])
  local max_penalty_ms = tonumber(ARGV[offset + 3])
  local ttl_ms = tonumber(ARGV[offset + 4])
  local values = redis.call('HMGET', KEYS[index], 'windowStartedAt', 'count', 'penaltyMs', 'blockedUntilMs')
  local stored_window_started_at = tonumber(values[1])
  local count = 0
  if stored_window_started_at == window_started_at then
    count = tonumber(values[2]) or 0
  end
  local penalty_ms = tonumber(values[3]) or 0
  local blocked_until_ms = tonumber(values[4]) or 0
  counts[index] = count
  penalty_values[index] = penalty_ms
  window_started_values[index] = window_started_at
  ttl_values[index] = ttl_ms

  if blocked_until_ms > now_ms or count >= max_requests then
    local next_penalty_ms = penalty_ms > 0 and penalty_ms * 2 or window_ms
    if next_penalty_ms > max_penalty_ms then
      next_penalty_ms = max_penalty_ms
    end
    blocked_until_ms = now_ms + next_penalty_ms
    redis.call(
      'HSET',
      KEYS[index],
      'windowStartedAt', tostring(window_started_at),
      'count', tostring(count),
      'penaltyMs', tostring(next_penalty_ms),
      'blockedUntilMs', tostring(blocked_until_ms)
    )
    redis.call('PEXPIRE', KEYS[index], ttl_ms)
    if blocked_index == 0 then
      blocked_index = index
      blocked_retry_ms = blocked_until_ms - now_ms
    end
  end
end

if blocked_index > 0 then
  return {0, blocked_retry_ms, blocked_index}
end

for index = 1, rule_count do
  redis.call(
    'HSET',
    KEYS[index],
    'windowStartedAt', tostring(window_started_values[index]),
    'count', tostring(counts[index] + 1),
    'penaltyMs', tostring(penalty_values[index]),
    'blockedUntilMs', '0'
  )
  redis.call('PEXPIRE', KEYS[index], ttl_values[index])
end
return {1, 0, 0}
`)

type Client struct {
	client    *goredis.Client
	namespace string
}

type SetItem struct {
	Key   string
	Value []byte
	TTL   time.Duration
}

type FixedWindowLimit struct {
	Key    string
	Limit  int
	Window time.Duration
}

type FixedWindowDecision struct {
	Allowed           bool
	RetryAfterSeconds int
}

type PenaltyWindowLimit struct {
	StoreName    string
	ScopeKey     string
	Window       time.Duration
	Limit        int
	MaxPenalty   time.Duration
	MaxIdle      time.Duration
	Now          time.Time
	WindowNumber int
}

type PenaltyWindowDecision struct {
	Allowed            bool
	RetryAfterSeconds  int
	BlockedWindowIndex int
}

func NewClient(rawURL string, namespace string) (*Client, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("redis url is required")
	}
	namespace = strings.Trim(namespace, ":")
	if namespace == "" {
		return nil, fmt.Errorf("redis namespace is required")
	}

	opts, err := goredis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}

	return &Client{
		client:    goredis.NewClient(opts),
		namespace: namespace,
	}, nil
}

func (c *Client) Close() error {
	return c.client.Close()
}

func (c *Client) Ping(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

func (c *Client) Key(parts ...string) string {
	clean := make([]string, 0, len(parts)+1)
	clean = append(clean, c.namespace)
	for _, part := range parts {
		part = strings.Trim(part, ":")
		if part != "" {
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, ":")
}

func (c *Client) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := validateKeyAndTTL(key, ttl); err != nil {
		return err
	}
	return c.client.Set(ctx, c.Key(key), value, ttl).Err()
}

func (c *Client) SetPersistent(ctx context.Context, key string, value []byte) error {
	if err := validateKey(key); err != nil {
		return err
	}
	return c.client.Set(ctx, c.Key(key), value, 0).Err()
}

func (c *Client) SetMany(ctx context.Context, items []SetItem) error {
	pipe := c.client.Pipeline()
	for _, item := range items {
		if err := validateKeyAndTTL(item.Key, item.TTL); err != nil {
			return err
		}
		pipe.Set(ctx, c.Key(item.Key), item.Value, item.TTL)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	value, err := c.client.Get(ctx, c.Key(key)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return value, nil
}

func (c *Client) GetDelete(ctx context.Context, key string) ([]byte, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	value, err := c.client.GetDel(ctx, c.Key(key)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return value, nil
}

func (c *Client) Incr(ctx context.Context, key string) (int64, error) {
	return c.IncrWithTTL(ctx, key, 24*time.Hour)
}

func (c *Client) IncrWithTTL(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	if err := validateKeyAndTTL(key, ttl); err != nil {
		return 0, err
	}
	result, err := incrWithTTLScript.Run(ctx, c.client, []string{c.Key(key)}, strconv.FormatInt(ttl.Milliseconds(), 10)).Int64()
	if err != nil {
		return 0, err
	}
	return result, nil
}

func (c *Client) AllowFixedWindow(ctx context.Context, limits []FixedWindowLimit) (FixedWindowDecision, error) {
	keys, args, err := c.fixedWindowScriptArgs(limits)
	if err != nil {
		return FixedWindowDecision{}, err
	}
	if len(keys) == 0 {
		return FixedWindowDecision{Allowed: true}, nil
	}

	values, err := fixedWindowRateLimitScript.Run(ctx, c.client, keys, args...).Slice()
	if err != nil {
		return FixedWindowDecision{}, err
	}
	if len(values) != 2 {
		return FixedWindowDecision{}, fmt.Errorf("unexpected fixed-window redis result length: %d", len(values))
	}

	allowedValue, err := redisInt64(values[0])
	if err != nil {
		return FixedWindowDecision{}, fmt.Errorf("parse fixed-window allowed value: %w", err)
	}
	retryAfter, err := redisInt64(values[1])
	if err != nil {
		return FixedWindowDecision{}, fmt.Errorf("parse fixed-window retry-after value: %w", err)
	}

	return FixedWindowDecision{
		Allowed:           allowedValue == 1,
		RetryAfterSeconds: max(0, int(retryAfter)),
	}, nil
}

func (c *Client) AllowPenaltyWindow(ctx context.Context, limits []PenaltyWindowLimit) (PenaltyWindowDecision, error) {
	keys, args, err := c.penaltyWindowScriptArgs(limits)
	if err != nil {
		return PenaltyWindowDecision{}, err
	}
	if len(keys) == 0 {
		return PenaltyWindowDecision{Allowed: true}, nil
	}

	values, err := penaltyWindowRateLimitScript.Run(ctx, c.client, keys, args...).Slice()
	if err != nil {
		return PenaltyWindowDecision{}, err
	}
	if len(values) != 3 {
		return PenaltyWindowDecision{}, fmt.Errorf("unexpected penalty-window redis result length: %d", len(values))
	}

	allowedValue, err := redisInt64(values[0])
	if err != nil {
		return PenaltyWindowDecision{}, fmt.Errorf("parse penalty-window allowed value: %w", err)
	}
	retryAfterMs, err := redisInt64(values[1])
	if err != nil {
		return PenaltyWindowDecision{}, fmt.Errorf("parse penalty-window retry-after value: %w", err)
	}
	blockedIndex, err := redisInt64(values[2])
	if err != nil {
		return PenaltyWindowDecision{}, fmt.Errorf("parse penalty-window blocked index value: %w", err)
	}

	return PenaltyWindowDecision{
		Allowed:            allowedValue == 1,
		RetryAfterSeconds:  max(0, int(math.Ceil(float64(retryAfterMs)/1000))),
		BlockedWindowIndex: max(0, int(blockedIndex)),
	}, nil
}

func (c *Client) fixedWindowScriptArgs(limits []FixedWindowLimit) ([]string, []interface{}, error) {
	keys := make([]string, 0, len(limits))
	args := make([]interface{}, 0, len(limits)*2)
	for _, item := range limits {
		if item.Limit <= 0 {
			continue
		}
		if err := validateKeyAndTTL(item.Key, item.Window); err != nil {
			return nil, nil, err
		}
		keys = append(keys, c.Key(item.Key))
		args = append(args,
			strconv.Itoa(item.Limit),
			strconv.FormatInt(item.Window.Milliseconds(), 10),
		)
	}
	return keys, args, nil
}

func (c *Client) penaltyWindowScriptArgs(limits []PenaltyWindowLimit) ([]string, []interface{}, error) {
	active := make([]PenaltyWindowLimit, 0, len(limits))
	for _, item := range limits {
		if item.Limit <= 0 || item.Window <= 0 {
			continue
		}
		if strings.TrimSpace(item.StoreName) == "" {
			return nil, nil, fmt.Errorf("penalty-window store name is required")
		}
		if strings.TrimSpace(item.ScopeKey) == "" {
			return nil, nil, fmt.Errorf("penalty-window scope key is required")
		}
		active = append(active, item)
	}
	if len(active) == 0 {
		return nil, nil, nil
	}

	now := active[0].Now
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()
	keys := make([]string, 0, len(active))
	args := make([]interface{}, 0, 2+len(active)*5)
	args = append(args, strconv.FormatInt(nowMs, 10), strconv.Itoa(len(active)))
	for _, item := range active {
		windowMs := item.Window.Milliseconds()
		if windowMs <= 0 {
			return nil, nil, fmt.Errorf("penalty-window window must be positive")
		}
		windowStartedAt := (nowMs / windowMs) * windowMs
		maxPenalty := item.MaxPenalty
		if maxPenalty <= 0 {
			maxPenalty = 15 * time.Minute
		}
		if maxPenalty < item.Window {
			maxPenalty = item.Window
		}
		maxIdle := item.MaxIdle
		if maxIdle <= 0 {
			maxIdle = 24 * time.Hour
		}
		ttl := maxDuration(maxIdle, maxPenalty, item.Window)

		keys = append(keys, c.Key(
			"rate-limit",
			"penalty",
			keyHash(item.StoreName),
			keyHash(item.ScopeKey),
			strconv.FormatInt(int64(item.Window/time.Second), 10),
			strconv.Itoa(item.Limit),
		))
		args = append(args,
			strconv.FormatInt(windowMs, 10),
			strconv.FormatInt(windowStartedAt, 10),
			strconv.Itoa(item.Limit),
			strconv.FormatInt(maxPenalty.Milliseconds(), 10),
			strconv.FormatInt(ttl.Milliseconds(), 10),
		)
	}
	return keys, args, nil
}

func redisInt64(value interface{}) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected redis integer type %T", value)
	}
}

func validateKey(key string) error {
	if strings.Trim(key, ":") == "" {
		return fmt.Errorf("redis key is required")
	}
	return nil
}

func validateKeyAndTTL(key string, ttl time.Duration) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if ttl <= 0 {
		return fmt.Errorf("redis ttl must be positive")
	}
	return nil
}

func keyHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func maxDuration(values ...time.Duration) time.Duration {
	var out time.Duration
	for _, value := range values {
		if value > out {
			out = value
		}
	}
	return out
}
