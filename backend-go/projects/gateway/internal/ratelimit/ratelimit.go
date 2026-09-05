// Package ratelimit mirrors the Node system API two-layer limiter
// (system-api-rate-limit.middleware.ts): per-IP minute+burst buckets and
// per-user minute buckets with settings-driven limits, allowlist bypass,
// health-path bypass and the exact 429 contract.
package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

type MethodClass string

const (
	MethodClassRead  MethodClass = "read"
	MethodClassWrite MethodClass = "write"
)

const (
	minuteWindowMs    = 60 * 1000
	burstWindowMs     = 10 * 1000
	cleanupIntervalMs = 60 * 1000
	maxEntriesPerSan  = 20_000
)

// Settings mirrors SystemApiRateLimitSettings.
type Settings struct {
	IPReadPerMinute    int
	IPReadBurstPer10s  int
	IPWritePerMinute   int
	IPWriteBurstPer10s int
	UserReadPerMinute  int
	UserWritePerMinute int
}

// SettingsProvider loads current settings; errors become 500 (Node
// respondRateLimitFailure).
type SettingsProvider func(ctx context.Context) (Settings, error)

// AllowlistFunc reports whether the client IP is allowlisted (Node
// inspectClientIpPolicy). Wired to the client-ip policy slice later.
type AllowlistFunc func(ctx context.Context, clientIP string) bool

// Store is the fixed-window counter backend (memory or redis).
type Store interface {
	// Check inspects all buckets atomically and commits when all allow.
	Check(ctx context.Context, nowMs int64, buckets []BucketInput) (allowed bool, retryAfter int, bucketName string, limit int)
}

type BucketInput struct {
	StoreName string
	WindowMs  int64
	Limit     int
	Key       string
}

// MemoryStore mirrors the Node in-memory fixed window with cleanup + trim.
type MemoryStore struct {
	mu      sync.Mutex
	entries map[string]memoryEntry
	next    int64
	now     func() time.Time
}

type memoryEntry struct {
	count     int
	resetAtMs int64
}

func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{entries: map[string]memoryEntry{}, now: now}
}

func (s *MemoryStore) Check(ctx context.Context, nowMs int64, buckets []BucketInput) (bool, int, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup(nowMs)

	type pending struct {
		key     string
		count   int
		resetAt int64
	}
	pendings := make([]pending, 0, len(buckets))
	for _, bucket := range buckets {
		if bucket.Limit <= 0 {
			continue
		}
		current, exists := s.entries[bucket.Key]
		var count int
		var resetAt int64
		if exists && current.resetAtMs > nowMs {
			count = current.count
			resetAt = current.resetAtMs
		} else {
			resetAt = nowMs + bucket.WindowMs
		}
		if count >= bucket.Limit {
			retry := int(math.Ceil(float64(resetAt-nowMs) / 1000))
			if retry < 1 {
				retry = 1
			}
			return false, retry, bucket.StoreName, bucket.Limit
		}
		pendings = append(pendings, pending{bucket.Key, count + 1, resetAt})
	}
	for _, p := range pendings {
		s.entries[p.key] = memoryEntry{count: p.count, resetAtMs: p.resetAt}
		s.trim(nowMs)
	}
	return true, 0, "", 0
}

func (s *MemoryStore) cleanup(nowMs int64) {
	if s.next > nowMs && len(s.entries) <= maxEntriesPerSan {
		return
	}
	for key, entry := range s.entries {
		if entry.resetAtMs <= nowMs {
			delete(s.entries, key)
		}
	}
	s.next = nowMs + cleanupIntervalMs
	s.trim(nowMs)
}

func (s *MemoryStore) trim(nowMs int64) {
	if len(s.entries) <= maxEntriesPerSan {
		return
	}
	target := maxEntriesPerSan * 9 / 10
	for key, entry := range s.entries {
		if entry.resetAtMs <= nowMs || len(s.entries) > target {
			delete(s.entries, key)
		}
		if len(s.entries) <= target {
			break
		}
	}
}

// Limiter composes settings + allowlist + stores, mirroring the two exported
// Node middlewares.
type Limiter struct {
	Settings  SettingsProvider
	Allowlist AllowlistFunc
	Store     Store
}

// IPRateLimitMiddleware mirrors app.use(systemApiPrefix, systemApiIpRateLimit)
// for the composition root: the per-IP minute+burst buckets with the health
// path bypass and the exact 429 contract.
func (l *Limiter) IPRateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l.ipRateLimit(w, r) {
			next.ServeHTTP(w, r)
		}
	})
}

// AuthenticatedRateLimit mirrors systemApiAuthenticatedRateLimit for one
// already-authenticated system account (requireAuth -> user rate limit order).
// It returns false after writing the 429 contract.
func (l *Limiter) AuthenticatedRateLimit(w http.ResponseWriter, r *http.Request, systemAccountID string) bool {
	if r.URL.Path == "/health" || strings.HasSuffix(r.URL.Path, "/__aisys__/api/health") {
		return true
	}
	return l.authenticatedRateLimit(w, r, systemAccountID)
}

func (l *Limiter) ipRateLimit(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/health" || strings.HasSuffix(r.URL.Path, "/__aisys__/api/health") {
		return true
	}
	settings, ok := l.load(w, r)
	if !ok {
		return false
	}
	if l.allowlisted(r) {
		return true
	}
	class := methodClassFor(r)
	ip := clientIPKey(r)
	key := ip + ":" + string(class)
	limits := [2]int{settings.IPReadPerMinute, settings.IPReadBurstPer10s}
	if class == MethodClassWrite {
		limits[0] = settings.IPWritePerMinute
		limits[1] = settings.IPWriteBurstPer10s
	}
	return l.check(w, r, []BucketInput{
		{StoreName: "system_api_ip_minute", WindowMs: minuteWindowMs, Limit: limits[0], Key: key},
		{StoreName: "system_api_ip_burst", WindowMs: burstWindowMs, Limit: limits[1], Key: key},
	}, "ip", class)
}

func (l *Limiter) authenticatedRateLimit(w http.ResponseWriter, r *http.Request, systemAccountID string) bool {
	settings, ok := l.load(w, r)
	if !ok {
		return false
	}
	if l.allowlisted(r) {
		return true
	}
	class := methodClassFor(r)
	limit := settings.UserReadPerMinute
	if class == MethodClassWrite {
		limit = settings.UserWritePerMinute
	}
	buckets := []BucketInput{{
		StoreName: "system_api_user_minute", WindowMs: minuteWindowMs,
		Limit: limit, Key: systemAccountID + ":" + string(class),
	}}
	return l.check(w, r, buckets, "user", class)
}

func (l *Limiter) load(w http.ResponseWriter, r *http.Request) (Settings, bool) {
	settings, err := l.Settings(r.Context())
	if err != nil {
		http.Error(w, `{"message":"服务器内部错误"}`, 500)
		return Settings{}, false
	}
	return settings, true
}

func (l *Limiter) allowlisted(r *http.Request) bool {
	if l.Allowlist == nil {
		return false
	}
	return l.Allowlist(r.Context(), clientIPKey(r))
}

func (l *Limiter) check(w http.ResponseWriter, r *http.Request, buckets []BucketInput, scope string, class MethodClass) bool {
	allowed, retryAfter, _, _ := l.Store.Check(r.Context(), time.Now().UnixMilli(), buckets)
	if !allowed {
		if retryAfter < 1 {
			retryAfter = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		kernel.WriteError(w, http.StatusTooManyRequests, "请求过于频繁，请稍后重试")
		return false
	}
	return true
}

func methodClassFor(r *http.Request) MethodClass {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return MethodClassRead
	default:
		return MethodClassWrite
	}
}

func clientIPKey(r *http.Request) string {
	if ip := kernel.Context(r).ClientIP; ip != "" {
		return ip
	}
	return "unknown"
}

func integerSetting(value json.Number, key string) (int, error) {
	parsed, err := strconv.Atoi(value.String())
	if err != nil {
		return 0, &SettingError{Key: key}
	}
	if parsed < 0 || parsed > 1_000_000 {
		return 0, &SettingError{Key: key, OutOfRange: true}
	}
	return parsed, nil
}

type SettingError struct {
	Key        string
	OutOfRange bool
}

func (e *SettingError) Error() string {
	if e.OutOfRange {
		return e.Key + " 必须在 0 到 1000000 之间"
	}
	return e.Key + " 必须是整数"
}

// RedisStore implements Store with the Node fixed-window Lua script
// (multi-bucket atomic check-and-commit).
type RedisStore struct {
	Client redisEval
	Now    func() time.Time
}

type redisEval interface {
	Eval(ctx context.Context, script string, keys []string, args ...any) (any, error)
}

const redisFixedWindowScript = `
local now_ms = tonumber(ARGV[1])
local bucket_count = tonumber(ARGV[2])
local pending_counts = {}
local pending_resets = {}
for index = 1, bucket_count do
  local offset = 3 + (index - 1) * 3
  local store_name = ARGV[offset]
  local window_ms = tonumber(ARGV[offset + 1])
  local limit = tonumber(ARGV[offset + 2])
  if limit > 0 then
    local raw = redis.call('GET', KEYS[index])
    local count = 0
    local reset_at_ms = now_ms + window_ms
    if raw then
      local separator = string.find(raw, ':')
      if separator then
        count = tonumber(string.sub(raw, 1, separator - 1)) or 0
        reset_at_ms = tonumber(string.sub(raw, separator + 1)) or reset_at_ms
      end
    end
    if reset_at_ms <= now_ms then
      count = 0
      reset_at_ms = now_ms + window_ms
    end
    if count >= limit then
      return {0, math.max(1, math.ceil((reset_at_ms - now_ms) / 1000)), store_name, limit}
    end
    pending_counts[index] = count + 1
    pending_resets[index] = reset_at_ms
  end
end
for index = 1, bucket_count do
  local offset = 3 + (index - 1) * 3
  local limit = tonumber(ARGV[offset + 2])
  if limit > 0 then
    local reset_at_ms = pending_resets[index]
    local ttl_ms = math.max(1, reset_at_ms - now_ms)
    redis.call('SET', KEYS[index], tostring(pending_counts[index]) .. ':' .. tostring(reset_at_ms), 'PX', ttl_ms)
  end
end
return {1, 0, '', 0}
`

func (s *RedisStore) Check(ctx context.Context, nowMs int64, buckets []BucketInput) (bool, int, string, int) {
	if len(buckets) == 0 {
		return true, 0, "", 0
	}
	keys := make([]string, len(buckets))
	args := []any{nowMs, len(buckets)}
	for i, bucket := range buckets {
		keys[i] = redisFixedWindowKey(bucket.StoreName, bucket.Key)
		args = append(args, bucket.StoreName, bucket.WindowMs, bucket.Limit)
	}
	result, err := s.Client.Eval(ctx, redisFixedWindowScript, keys, args...)
	if err != nil {
		return false, 1, "redis_error", 0
	}
	values, ok := result.([]any)
	if !ok {
		return false, 1, "redis_error", 0
	}
	if numeric(values[0]) == 1 {
		return true, 0, "", 0
	}
	retry := int(numeric(values[1]))
	if retry < 1 {
		retry = 1
	}
	name, _ := values[2].(string)
	return false, retry, name, int(numeric(values[3]))
}

func redisFixedWindowKey(storeName, key string) string {
	return "juhe-ai:rate-limit:fixed:" + redisKeyHash(storeName) + ":" + redisKeyHash(key)
}

func redisKeyHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func numeric(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	default:
		return 0
	}
}
