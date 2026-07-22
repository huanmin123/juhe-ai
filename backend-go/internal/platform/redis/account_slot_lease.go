package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

var ErrAccountSlotTokenCollision = errors.New("Redis 账号并发槽 token 冲突")

type AccountSlotLane string

const (
	AccountSlotLaneText  AccountSlotLane = "text"
	AccountSlotLaneImage AccountSlotLane = "image"
)

type AccountSlotAcquireInput struct {
	AccountID  string
	Lane       AccountSlotLane
	TotalLimit int
	LaneLimit  int
	TTL        time.Duration
	Token      string
}

type AccountSlotLease struct {
	AccountID string
	Lane      AccountSlotLane
	Token     string
	ExpiresAt time.Time
}

type AccountSlotAcquireResult struct {
	Acquired    bool
	Current     int
	LaneCurrent int
	Lease       AccountSlotLease
}

type AccountSlotRefreshResult struct {
	Refreshed bool
	Lease     AccountSlotLease
}

type AccountSlotLeaseStore struct {
	rootNamespace string
	acquire       func(context.Context, []string, ...interface{}) ([]interface{}, error)
	refresh       func(context.Context, []string, ...interface{}) ([]interface{}, error)
	release       func(context.Context, []string, ...interface{}) (int64, error)
}

type accountSlotAcquireReply struct {
	status      int64
	current     int64
	laneCurrent int64
	expiresAtMS int64
}

const accountSlotAcquireLua = `
local total_limit = tonumber(ARGV[1])
local lane_limit = tonumber(ARGV[2])
local slot_ttl_ms = tonumber(ARGV[3])
local slot_token = ARGV[4]
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local started_at_ms = now_ms
local expires_at_ms = now_ms + slot_ttl_ms

local function hdel_expired(metadata_key, expired)
  local index = 1
  while index <= #expired do
    local last = math.min(index + 199, #expired)
    redis.call('HDEL', metadata_key, unpack(expired, index, last))
    index = last + 1
  end
end

local function latest_expiry_ms(key)
  local latest = redis.call('ZREVRANGE', key, 0, 0, 'WITHSCORES')
  if #latest < 2 then
    return now_ms
  end
  return tonumber(latest[2]) or now_ms
end

local function expire_at_latest_slot(key, expiry_ms)
  if redis.call('EXISTS', key) ~= 0 then
    redis.call('PEXPIRE', key, math.max(1, expiry_ms - now_ms))
  end
end

local expired = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now_ms)
if #expired > 0 then
  hdel_expired(KEYS[5], expired)
end
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', now_ms)

local current = tonumber(redis.call('ZCARD', KEYS[1]) or '0') or 0
local lane_current = tonumber(redis.call('ZCARD', KEYS[4]) or '0') or 0
local total_token_expiry = redis.call('ZSCORE', KEYS[1], slot_token)
local selected_lane_token_expiry = redis.call('ZSCORE', KEYS[4], slot_token)
local other_lane_key = KEYS[2]
if KEYS[4] == KEYS[2] then
  other_lane_key = KEYS[3]
end
local other_lane_token_expiry = redis.call('ZSCORE', other_lane_key, slot_token)
if total_token_expiry ~= false and selected_lane_token_expiry ~= false and other_lane_token_expiry == false then
  local total_latest_expiry_ms = latest_expiry_ms(KEYS[1])
  local lane_latest_expiry_ms = latest_expiry_ms(KEYS[4])
  expire_at_latest_slot(KEYS[1], total_latest_expiry_ms)
  expire_at_latest_slot(KEYS[4], lane_latest_expiry_ms)
  expire_at_latest_slot(KEYS[5], total_latest_expiry_ms)
  return {1, current, lane_current, tonumber(total_token_expiry)}
end
if total_token_expiry ~= false
  or selected_lane_token_expiry ~= false
  or other_lane_token_expiry ~= false
  or redis.call('HEXISTS', KEYS[5], slot_token) == 1 then
  return {2, current, lane_current, 0}
end
if current >= total_limit or lane_current >= lane_limit then
  return {0, current, lane_current, 0}
end

redis.call('ZADD', KEYS[1], expires_at_ms, slot_token)
redis.call('ZADD', KEYS[4], expires_at_ms, slot_token)
redis.call('HSET', KEYS[5], slot_token, cjson.encode({startedAtMs = started_at_ms}))

local total_latest_expiry_ms = latest_expiry_ms(KEYS[1])
local lane_latest_expiry_ms = latest_expiry_ms(KEYS[4])
expire_at_latest_slot(KEYS[1], total_latest_expiry_ms)
expire_at_latest_slot(KEYS[4], lane_latest_expiry_ms)
expire_at_latest_slot(KEYS[5], total_latest_expiry_ms)
return {1, current + 1, lane_current + 1, expires_at_ms}
`

const accountSlotRefreshLua = `
local slot_token = ARGV[1]
local slot_ttl_ms = tonumber(ARGV[2])
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local expires_at_ms = now_ms + slot_ttl_ms

local function latest_expiry_ms(key)
  local latest = redis.call('ZREVRANGE', key, 0, 0, 'WITHSCORES')
  if #latest < 2 then
    return now_ms
  end
  return tonumber(latest[2]) or now_ms
end

local function expire_at_latest_slot(key, expiry_ms)
  if redis.call('EXISTS', key) ~= 0 then
    redis.call('PEXPIRE', key, math.max(1, expiry_ms - now_ms))
  end
end

local total_expiry_raw = redis.call('ZSCORE', KEYS[1], slot_token)
if total_expiry_raw == false then
  return {0, 0}
end
local total_expiry_ms = tonumber(total_expiry_raw)
if total_expiry_ms == nil or total_expiry_ms <= now_ms then
  redis.call('ZREM', KEYS[1], slot_token)
  redis.call('ZREM', KEYS[2], slot_token)
  redis.call('ZREM', KEYS[3], slot_token)
  redis.call('HDEL', KEYS[5], slot_token)
  return {0, 0}
end

local selected_lane_expiry_ms = redis.call('ZSCORE', KEYS[4], slot_token)
local other_lane_key = KEYS[2]
if KEYS[4] == KEYS[2] then
  other_lane_key = KEYS[3]
end
local other_lane_expiry_ms = redis.call('ZSCORE', other_lane_key, slot_token)
if selected_lane_expiry_ms == false or other_lane_expiry_ms ~= false then
  return {0, 0}
end

redis.call('ZADD', KEYS[1], expires_at_ms, slot_token)
redis.call('ZADD', KEYS[4], expires_at_ms, slot_token)
local total_latest_expiry_ms = latest_expiry_ms(KEYS[1])
local lane_latest_expiry_ms = latest_expiry_ms(KEYS[4])
expire_at_latest_slot(KEYS[1], total_latest_expiry_ms)
expire_at_latest_slot(KEYS[4], lane_latest_expiry_ms)
expire_at_latest_slot(KEYS[5], total_latest_expiry_ms)
return {1, expires_at_ms}
`

const accountSlotReleaseLua = `
local slot_token = ARGV[1]
local removed = redis.call('ZREM', KEYS[1], slot_token)
redis.call('ZREM', KEYS[2], slot_token)
redis.call('ZREM', KEYS[3], slot_token)
redis.call('HDEL', KEYS[4], slot_token)
if redis.call('ZCARD', KEYS[1]) == 0 then redis.call('DEL', KEYS[1]) end
if redis.call('ZCARD', KEYS[2]) == 0 then redis.call('DEL', KEYS[2]) end
if redis.call('ZCARD', KEYS[3]) == 0 then redis.call('DEL', KEYS[3]) end
if redis.call('HLEN', KEYS[4]) == 0 then redis.call('DEL', KEYS[4]) end
return removed
`

var accountSlotAcquireScript = goredis.NewScript(accountSlotAcquireLua)
var accountSlotRefreshScript = goredis.NewScript(accountSlotRefreshLua)
var accountSlotReleaseScript = goredis.NewScript(accountSlotReleaseLua)

func NewAccountSlotLeaseStore(client *Client, rootNamespace string) (*AccountSlotLeaseStore, error) {
	if client == nil || client.client == nil {
		return nil, fmt.Errorf("Redis state client 未初始化")
	}
	namespace, err := normalizeAccountConcurrencyNamespace(rootNamespace)
	if err != nil {
		return nil, err
	}
	return &AccountSlotLeaseStore{
		rootNamespace: namespace,
		acquire: func(ctx context.Context, keys []string, args ...interface{}) ([]interface{}, error) {
			return accountSlotAcquireScript.Run(ctx, client.client, keys, args...).Slice()
		},
		refresh: func(ctx context.Context, keys []string, args ...interface{}) ([]interface{}, error) {
			return accountSlotRefreshScript.Run(ctx, client.client, keys, args...).Slice()
		},
		release: func(ctx context.Context, keys []string, args ...interface{}) (int64, error) {
			return accountSlotReleaseScript.Run(ctx, client.client, keys, args...).Int64()
		},
	}, nil
}

func NewAccountSlotToken() string {
	return "go|" + uuid.NewString()
}

func (s *AccountSlotLeaseStore) Acquire(ctx context.Context, input AccountSlotAcquireInput) (AccountSlotAcquireResult, error) {
	if s == nil || s.acquire == nil {
		return AccountSlotAcquireResult{}, fmt.Errorf("Redis 账号并发槽 store 未初始化")
	}
	if err := contextError(ctx); err != nil {
		return AccountSlotAcquireResult{}, err
	}
	accountID, err := validateAccountSlotID(input.AccountID)
	if err != nil {
		return AccountSlotAcquireResult{}, err
	}
	if err := validateAccountSlotLane(input.Lane); err != nil {
		return AccountSlotAcquireResult{}, err
	}
	if input.TotalLimit <= 0 {
		return AccountSlotAcquireResult{}, fmt.Errorf("账号总并发上限必须为正整数")
	}
	if input.LaneLimit <= 0 || input.LaneLimit > input.TotalLimit {
		return AccountSlotAcquireResult{}, fmt.Errorf("lane 并发上限必须在 1 到账号总并发上限之间")
	}
	ttlMS, err := accountSlotTTL(input.TTL)
	if err != nil {
		return AccountSlotAcquireResult{}, err
	}
	token := strings.TrimSpace(input.Token)
	if err := validateAccountSlotToken(token); err != nil {
		return AccountSlotAcquireResult{}, err
	}
	values, err := s.acquire(
		ctx,
		s.accountSlotKeys(accountID, input.Lane, true),
		strconv.Itoa(input.TotalLimit),
		strconv.Itoa(input.LaneLimit),
		strconv.FormatInt(ttlMS, 10),
		token,
	)
	if err != nil {
		return AccountSlotAcquireResult{}, fmt.Errorf("获取 Redis 账号并发槽: %w", err)
	}
	reply, err := parseAccountSlotAcquireResult(values)
	if err != nil {
		return AccountSlotAcquireResult{}, err
	}
	result := AccountSlotAcquireResult{Current: int(reply.current), LaneCurrent: int(reply.laneCurrent)}
	switch reply.status {
	case 0:
		return result, nil
	case 2:
		return AccountSlotAcquireResult{}, ErrAccountSlotTokenCollision
	case 1:
		if reply.expiresAtMS <= 0 {
			return AccountSlotAcquireResult{}, fmt.Errorf("Redis 账号并发槽返回了无效到期时间")
		}
		result.Acquired = true
		result.Lease = AccountSlotLease{
			AccountID: accountID,
			Lane:      input.Lane,
			Token:     token,
			ExpiresAt: time.UnixMilli(reply.expiresAtMS).UTC(),
		}
		return result, nil
	default:
		return AccountSlotAcquireResult{}, fmt.Errorf("Redis 账号并发槽返回了未知状态 %d", reply.status)
	}
}

func (s *AccountSlotLeaseStore) Refresh(ctx context.Context, lease AccountSlotLease, ttl time.Duration) (AccountSlotRefreshResult, error) {
	if s == nil || s.refresh == nil {
		return AccountSlotRefreshResult{}, fmt.Errorf("Redis 账号并发槽 store 未初始化")
	}
	if err := contextError(ctx); err != nil {
		return AccountSlotRefreshResult{}, err
	}
	lease, err := validateAccountSlotLease(lease)
	if err != nil {
		return AccountSlotRefreshResult{}, err
	}
	ttlMS, err := accountSlotTTL(ttl)
	if err != nil {
		return AccountSlotRefreshResult{}, err
	}
	refreshed, err := s.refresh(
		ctx,
		s.accountSlotKeys(lease.AccountID, lease.Lane, true),
		lease.Token,
		strconv.FormatInt(ttlMS, 10),
	)
	if err != nil {
		return AccountSlotRefreshResult{}, fmt.Errorf("续租 Redis 账号并发槽: %w", err)
	}
	status, expiresAtMS, err := parseAccountSlotRefreshResult(refreshed)
	if err != nil {
		return AccountSlotRefreshResult{}, err
	}
	if status == 0 {
		return AccountSlotRefreshResult{}, nil
	}
	lease.ExpiresAt = time.UnixMilli(expiresAtMS).UTC()
	return AccountSlotRefreshResult{Refreshed: true, Lease: lease}, nil
}

func (s *AccountSlotLeaseStore) Release(ctx context.Context, lease AccountSlotLease) (bool, error) {
	if s == nil || s.release == nil {
		return false, fmt.Errorf("Redis 账号并发槽 store 未初始化")
	}
	if err := contextError(ctx); err != nil {
		return false, err
	}
	lease, err := validateAccountSlotLease(lease)
	if err != nil {
		return false, err
	}
	released, err := s.release(ctx, s.accountSlotKeys(lease.AccountID, lease.Lane, false), lease.Token)
	if err != nil {
		return false, fmt.Errorf("释放 Redis 账号并发槽: %w", err)
	}
	if released != 0 && released != 1 {
		return false, fmt.Errorf("Redis 账号并发槽释放返回了未知状态 %d", released)
	}
	return released == 1, nil
}

func (s *AccountSlotLeaseStore) accountSlotKeys(accountID string, lane AccountSlotLane, includeSelectedLane bool) []string {
	prefix := "juhe-ai:" + s.rootNamespace + ":account-concurrency-v2:" + accountID + ":"
	keys := []string{prefix + "total", prefix + "text", prefix + "image"}
	if includeSelectedLane {
		keys = append(keys, prefix+string(lane))
	}
	return append(keys, prefix+"metadata")
}

func validateAccountSlotLease(lease AccountSlotLease) (AccountSlotLease, error) {
	accountID, err := validateAccountSlotID(lease.AccountID)
	if err != nil {
		return AccountSlotLease{}, err
	}
	if err := validateAccountSlotLane(lease.Lane); err != nil {
		return AccountSlotLease{}, err
	}
	token := strings.TrimSpace(lease.Token)
	if err := validateAccountSlotToken(token); err != nil {
		return AccountSlotLease{}, err
	}
	lease.AccountID = accountID
	lease.Token = token
	return lease, nil
}

func validateAccountSlotID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("账号 ID 不能为空")
	}
	if len(value) > 128 {
		return "", fmt.Errorf("账号 ID 长度不能超过 128")
	}
	if strings.Contains(value, ":") || strings.ContainsAny(value, "\r\n\t") {
		return "", fmt.Errorf("账号 ID 包含 Redis key 不允许的字符")
	}
	return value, nil
}

func validateAccountSlotLane(lane AccountSlotLane) error {
	if lane != AccountSlotLaneText && lane != AccountSlotLaneImage {
		return fmt.Errorf("账号并发 lane 必须为 text 或 image")
	}
	return nil
}

func validateAccountSlotToken(token string) error {
	if token == "" {
		return fmt.Errorf("Redis 账号并发槽 token 不能为空")
	}
	if len(token) > 256 {
		return fmt.Errorf("Redis 账号并发槽 token 长度不能超过 256")
	}
	if strings.ContainsAny(token, "\r\n\x00") {
		return fmt.Errorf("Redis 账号并发槽 token 包含不允许的字符")
	}
	return nil
}

func accountSlotTTL(ttl time.Duration) (int64, error) {
	ttlMS := ttl.Milliseconds()
	if ttl <= 0 || ttlMS <= 0 {
		return 0, fmt.Errorf("Redis 账号并发槽 TTL 必须至少为 1ms")
	}
	return ttlMS, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("context 不能为空")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func parseAccountSlotAcquireResult(values []interface{}) (accountSlotAcquireReply, error) {
	if len(values) != 4 {
		return accountSlotAcquireReply{}, fmt.Errorf("Redis 账号并发槽返回值长度异常: %d", len(values))
	}
	parsed := make([]int64, 4)
	for index, value := range values {
		item, err := redisInt64(value)
		if err != nil {
			return accountSlotAcquireReply{}, fmt.Errorf("解析 Redis 账号并发槽返回值[%d]: %w", index, err)
		}
		parsed[index] = item
	}
	if parsed[0] < 0 || parsed[0] > 2 {
		return accountSlotAcquireReply{}, fmt.Errorf("Redis 账号并发槽返回了未知状态 %d", parsed[0])
	}
	if parsed[1] < 0 || parsed[2] < 0 || parsed[3] < 0 {
		return accountSlotAcquireReply{}, fmt.Errorf("Redis 账号并发槽返回了负数")
	}
	return accountSlotAcquireReply{
		status: parsed[0], current: parsed[1], laneCurrent: parsed[2], expiresAtMS: parsed[3],
	}, nil
}

func parseAccountSlotRefreshResult(values []interface{}) (int64, int64, error) {
	if len(values) != 2 {
		return 0, 0, fmt.Errorf("Redis 账号并发槽续租返回值长度异常: %d", len(values))
	}
	status, err := redisInt64(values[0])
	if err != nil {
		return 0, 0, fmt.Errorf("解析 Redis 账号并发槽续租状态: %w", err)
	}
	expiresAtMS, err := redisInt64(values[1])
	if err != nil {
		return 0, 0, fmt.Errorf("解析 Redis 账号并发槽续租到期时间: %w", err)
	}
	if status != 0 && status != 1 {
		return 0, 0, fmt.Errorf("Redis 账号并发槽续租返回了未知状态 %d", status)
	}
	if expiresAtMS < 0 || (status == 1 && expiresAtMS == 0) {
		return 0, 0, fmt.Errorf("Redis 账号并发槽续租返回了无效到期时间")
	}
	return status, expiresAtMS, nil
}
