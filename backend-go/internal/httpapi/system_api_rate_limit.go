package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	systemAPISettingsCacheTTL = 60 * time.Second
	rateLimitMinuteWindow     = time.Minute
	rateLimitBurstWindow      = 10 * time.Second
)

type systemAPIRateLimitMiddleware struct {
	reader        port.SystemAPIIPRateLimitReader
	limiter       SystemAPIIPReadRateLimiter
	clientIPs     clientIPResolver
	logger        *slog.Logger
	settingsCache systemAPIRateLimitSettingsCache
}

type systemAPIRateLimitSettingsCache struct {
	mu        sync.Mutex
	settings  port.SystemAPIIPReadRateLimitSettings
	expiresAt time.Time
}

type SystemAPIIPReadRateLimiter interface {
	AllowSystemAPIIPRead(ctx context.Context, key string, settings port.SystemAPIIPReadRateLimitSettings) (SystemAPIIPReadRateLimitDecision, error)
}

type SystemAPIIPReadRateLimitDecision struct {
	Allowed           bool
	RetryAfterSeconds int
}

type inMemorySystemAPIIPReadRateLimiter struct {
	minuteLimiter fixedWindowRateLimiter
	burstLimiter  fixedWindowRateLimiter
}

type redisFixedWindowClient interface {
	AllowFixedWindow(context.Context, []redisplatform.FixedWindowLimit) (redisplatform.FixedWindowDecision, error)
}

type redisSystemAPIIPReadRateLimiter struct {
	client redisFixedWindowClient
}

type fixedWindowRateLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	entries map[string]rateLimitEntry
}

type rateLimitEntry struct {
	count   int
	resetAt time.Time
}

func newSystemAPIIPReadRateLimitMiddleware(
	reader port.SystemAPIIPRateLimitReader,
	limiter SystemAPIIPReadRateLimiter,
	clientIPs clientIPResolver,
	logger *slog.Logger,
) func(http.Handler) http.Handler {
	if limiter == nil {
		panic("SystemAPIIPReadRateLimiter is required when SystemAPIIPRateLimitReader is configured")
	}
	middleware := &systemAPIRateLimitMiddleware{
		reader:    reader,
		limiter:   limiter,
		clientIPs: clientIPs,
		logger:    logger,
	}
	return middleware.handle
}

func NewInMemorySystemAPIIPReadRateLimiter() SystemAPIIPReadRateLimiter {
	return &inMemorySystemAPIIPReadRateLimiter{
		minuteLimiter: fixedWindowRateLimiter{
			window:  rateLimitMinuteWindow,
			entries: map[string]rateLimitEntry{},
		},
		burstLimiter: fixedWindowRateLimiter{
			window:  rateLimitBurstWindow,
			entries: map[string]rateLimitEntry{},
		},
	}
}

func NewRedisSystemAPIIPReadRateLimiter(client redisFixedWindowClient) SystemAPIIPReadRateLimiter {
	if client == nil {
		return nil
	}
	return &redisSystemAPIIPReadRateLimiter{client: client}
}

func (m *systemAPIRateLimitMiddleware) handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__aisys__/api/health" || !isReadMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		settings, err := m.currentSettings(r.Context(), time.Now())
		if err != nil {
			if m.logger != nil {
				m.logger.Error("系统 API 限流设置读取失败",
					slog.String("path", r.URL.Path),
					slog.String("request_id", requestIDFromContext(r.Context())),
					slog.Any("error", err),
				)
			}
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		clientIP := m.clientIPs.FromRequest(r)
		decision, err := m.limiter.AllowSystemAPIIPRead(
			r.Context(),
			systemAPIIPReadRateLimitKey(clientIP),
			settings,
		)
		if err != nil {
			if m.logger != nil {
				m.logger.Error("系统 API IP 读限流失败",
					slog.String("path", r.URL.Path),
					slog.String("request_id", requestIDFromContext(r.Context())),
					slog.Any("error", err),
				)
			}
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !decision.Allowed {
			w.Header().Set("Retry-After", intString(decision.RetryAfterSeconds))
			writeMessageError(w, http.StatusTooManyRequests, "请求过于频繁，请稍后重试")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (m *systemAPIRateLimitMiddleware) currentSettings(ctx context.Context, now time.Time) (port.SystemAPIIPReadRateLimitSettings, error) {
	m.settingsCache.mu.Lock()
	if now.Before(m.settingsCache.expiresAt) {
		settings := m.settingsCache.settings
		m.settingsCache.mu.Unlock()
		return settings, nil
	}
	m.settingsCache.mu.Unlock()

	settings, err := m.reader.SystemAPIIPReadRateLimitSettings(ctx)
	if err != nil {
		return port.SystemAPIIPReadRateLimitSettings{}, err
	}

	m.settingsCache.mu.Lock()
	m.settingsCache.settings = settings
	m.settingsCache.expiresAt = now.Add(systemAPISettingsCacheTTL)
	m.settingsCache.mu.Unlock()
	return settings, nil
}

func (l *inMemorySystemAPIIPReadRateLimiter) AllowSystemAPIIPRead(
	_ context.Context,
	key string,
	settings port.SystemAPIIPReadRateLimitSettings,
) (SystemAPIIPReadRateLimitDecision, error) {
	now := time.Now()
	minuteKey := key + ":minute"
	burstKey := key + ":burst"

	l.minuteLimiter.mu.Lock()
	defer l.minuteLimiter.mu.Unlock()
	l.burstLimiter.mu.Lock()
	defer l.burstLimiter.mu.Unlock()

	if decision := l.minuteLimiter.decisionLocked(minuteKey, settings.PerMinute, now); !decision.Allowed {
		return decision, nil
	}
	if decision := l.burstLimiter.decisionLocked(burstKey, settings.BurstPer10Seconds, now); !decision.Allowed {
		return decision, nil
	}
	l.minuteLimiter.incrementLocked(minuteKey, settings.PerMinute, now)
	l.burstLimiter.incrementLocked(burstKey, settings.BurstPer10Seconds, now)
	return SystemAPIIPReadRateLimitDecision{Allowed: true}, nil
}

func (l *redisSystemAPIIPReadRateLimiter) AllowSystemAPIIPRead(
	ctx context.Context,
	key string,
	settings port.SystemAPIIPReadRateLimitSettings,
) (SystemAPIIPReadRateLimitDecision, error) {
	decision, err := l.client.AllowFixedWindow(ctx, []redisplatform.FixedWindowLimit{
		{Key: "system-api:ip-read:" + key + ":minute", Limit: settings.PerMinute, Window: rateLimitMinuteWindow},
		{Key: "system-api:ip-read:" + key + ":burst", Limit: settings.BurstPer10Seconds, Window: rateLimitBurstWindow},
	})
	if err != nil {
		return SystemAPIIPReadRateLimitDecision{}, err
	}
	return SystemAPIIPReadRateLimitDecision{
		Allowed:           decision.Allowed,
		RetryAfterSeconds: decision.RetryAfterSeconds,
	}, nil
}

func (l *fixedWindowRateLimiter) Allow(key string, limit int, now time.Time) SystemAPIIPReadRateLimitDecision {
	l.mu.Lock()
	defer l.mu.Unlock()

	decision := l.decisionLocked(key, limit, now)
	if !decision.Allowed {
		return decision
	}
	l.incrementLocked(key, limit, now)
	return SystemAPIIPReadRateLimitDecision{Allowed: true}
}

func (l *fixedWindowRateLimiter) decisionLocked(key string, limit int, now time.Time) SystemAPIIPReadRateLimitDecision {
	if limit <= 0 {
		return SystemAPIIPReadRateLimitDecision{Allowed: true}
	}
	entry := l.currentEntryLocked(key, now)
	if entry.count >= limit {
		return SystemAPIIPReadRateLimitDecision{
			Allowed:           false,
			RetryAfterSeconds: max(1, int(math.Ceil(entry.resetAt.Sub(now).Seconds()))),
		}
	}
	return SystemAPIIPReadRateLimitDecision{Allowed: true}
}

func (l *fixedWindowRateLimiter) incrementLocked(key string, limit int, now time.Time) {
	if limit <= 0 {
		return
	}
	entry := l.currentEntryLocked(key, now)
	entry.count++
	l.entries[key] = entry
	l.pruneLocked(now)
}

func (l *fixedWindowRateLimiter) currentEntryLocked(key string, now time.Time) rateLimitEntry {
	if l.entries == nil {
		l.entries = map[string]rateLimitEntry{}
	}
	entry := l.entries[key]
	if entry.resetAt.IsZero() || !entry.resetAt.After(now) {
		entry = rateLimitEntry{
			count:   0,
			resetAt: now.Add(l.window),
		}
	}
	return entry
}

func (l *fixedWindowRateLimiter) pruneLocked(now time.Time) {
	if len(l.entries) <= 20_000 {
		return
	}
	for key, entry := range l.entries {
		if !entry.resetAt.After(now) || len(l.entries) > 18_000 {
			delete(l.entries, key)
		}
		if len(l.entries) <= 18_000 {
			break
		}
	}
}

func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func systemAPIIPReadRateLimitKey(clientIP string) string {
	sum := sha256.Sum256([]byte(clientIP + "\x00read"))
	return hex.EncodeToString(sum[:])
}

func intString(value int) string {
	return strconv.Itoa(value)
}
