package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
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

type systemAPIIPRateLimitMiddleware struct {
	reader        port.SystemAPIRateLimitReader
	limiter       SystemAPIIPRateLimiter
	clientIPs     clientIPResolver
	logger        *slog.Logger
	settingsCache systemAPIRateLimitSettingsCache
}

type systemAPIAuthenticatedRateLimitMiddleware struct {
	reader        port.SystemAPIRateLimitReader
	limiter       SystemAPIAuthenticatedRateLimiter
	methodClass   systemAPIMethodClass
	logger        *slog.Logger
	settingsCache systemAPIRateLimitSettingsCache
}

type systemAPIMethodClass string

const (
	systemAPIMethodRead  systemAPIMethodClass = "read"
	systemAPIMethodWrite systemAPIMethodClass = "write"
)

type systemAPIRateLimitSettingsCache struct {
	mu        sync.Mutex
	settings  port.SystemAPIRateLimitSettings
	expiresAt time.Time
}

type SystemAPIIPRateLimitSettings struct {
	PerMinute         int
	BurstPer10Seconds int
}

type SystemAPIIPRateLimiter interface {
	AllowSystemAPIIP(ctx context.Context, key string, settings SystemAPIIPRateLimitSettings) (SystemAPIRateLimitDecision, error)
}

type SystemAPIAuthenticatedRateLimiter interface {
	AllowSystemAPIAuthenticated(ctx context.Context, key string, limit int) (SystemAPIRateLimitDecision, error)
}

type SystemAPIRateLimitDecision struct {
	Allowed           bool
	RetryAfterSeconds int
}

type inMemorySystemAPIIPRateLimiter struct {
	minuteLimiter fixedWindowRateLimiter
	burstLimiter  fixedWindowRateLimiter
}

type inMemorySystemAPIAuthenticatedRateLimiter struct {
	minuteLimiter fixedWindowRateLimiter
}

type redisFixedWindowClient interface {
	AllowFixedWindow(context.Context, []redisplatform.FixedWindowLimit) (redisplatform.FixedWindowDecision, error)
}

type redisSystemAPIIPRateLimiter struct {
	client redisFixedWindowClient
}

type redisSystemAPIAuthenticatedRateLimiter struct {
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

func newSystemAPIIPRateLimitMiddleware(
	reader port.SystemAPIRateLimitReader,
	limiter SystemAPIIPRateLimiter,
	clientIPs clientIPResolver,
	logger *slog.Logger,
) func(http.Handler) http.Handler {
	if limiter == nil {
		panic("SystemAPIIPRateLimiter is required when SystemAPIRateLimitReader is configured")
	}
	middleware := &systemAPIIPRateLimitMiddleware{
		reader:    reader,
		limiter:   limiter,
		clientIPs: clientIPs,
		logger:    logger,
	}
	return middleware.handle
}

func newSystemAPIAuthenticatedRateLimitMiddleware(
	reader port.SystemAPIRateLimitReader,
	limiter SystemAPIAuthenticatedRateLimiter,
	methodClass systemAPIMethodClass,
	logger *slog.Logger,
) func(http.Handler) http.Handler {
	if limiter == nil {
		panic("SystemAPIAuthenticatedRateLimiter is required when authenticated management rate limiting is configured")
	}
	middleware := &systemAPIAuthenticatedRateLimitMiddleware{
		reader:      reader,
		limiter:     limiter,
		methodClass: methodClass,
		logger:      logger,
	}
	return middleware.handle
}

func NewInMemorySystemAPIIPRateLimiter() SystemAPIIPRateLimiter {
	return &inMemorySystemAPIIPRateLimiter{
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

func NewInMemorySystemAPIAuthenticatedRateLimiter() SystemAPIAuthenticatedRateLimiter {
	return &inMemorySystemAPIAuthenticatedRateLimiter{
		minuteLimiter: fixedWindowRateLimiter{
			window:  rateLimitMinuteWindow,
			entries: map[string]rateLimitEntry{},
		},
	}
}

func NewRedisSystemAPIIPRateLimiter(client redisFixedWindowClient) SystemAPIIPRateLimiter {
	if client == nil {
		return nil
	}
	return &redisSystemAPIIPRateLimiter{client: client}
}

func NewRedisSystemAPIAuthenticatedRateLimiter(client redisFixedWindowClient) SystemAPIAuthenticatedRateLimiter {
	if client == nil {
		return nil
	}
	return &redisSystemAPIAuthenticatedRateLimiter{client: client}
}

func (m *systemAPIIPRateLimitMiddleware) handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !shouldApplySystemAPIIPRateLimit(r) {
			next.ServeHTTP(w, r)
			return
		}

		settings, err := m.settingsCache.current(r.Context(), m.reader, time.Now())
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

		methodClass := systemAPIMethodClassFor(r.Method)
		clientIP := m.clientIPs.FromRequest(r)
		decision, err := m.limiter.AllowSystemAPIIP(
			r.Context(),
			systemAPIIPRateLimitKey(clientIP, methodClass),
			systemAPIIPRateLimitSettingsFor(settings, methodClass),
		)
		if err != nil {
			if m.logger != nil {
				m.logger.Error("系统 API IP 限流失败",
					slog.String("path", r.URL.Path),
					slog.String("method_class", string(methodClass)),
					slog.String("request_id", requestIDFromContext(r.Context())),
					slog.Any("error", err),
				)
			}
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !decision.Allowed {
			writeSystemAPIRateLimitResponse(w, decision)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (m *systemAPIAuthenticatedRateLimitMiddleware) handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			next.ServeHTTP(w, r)
			return
		}

		settings, err := m.settingsCache.current(r.Context(), m.reader, time.Now())
		if err != nil {
			if m.logger != nil {
				m.logger.Error("系统 API 认证用户限流设置读取失败",
					slog.String("path", r.URL.Path),
					slog.String("method_class", string(m.methodClass)),
					slog.String("request_id", requestIDFromContext(r.Context())),
					slog.Any("error", err),
				)
			}
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		decision, err := m.limiter.AllowSystemAPIAuthenticated(
			r.Context(),
			systemAPIAuthenticatedRateLimitKey(authContext.SystemAccountID, m.methodClass),
			systemAPIAuthenticatedRateLimitFor(settings, m.methodClass),
		)
		if err != nil {
			if m.logger != nil {
				m.logger.Error("系统 API 认证用户限流失败",
					slog.String("path", r.URL.Path),
					slog.String("method_class", string(m.methodClass)),
					slog.String("request_id", requestIDFromContext(r.Context())),
					slog.Any("error", err),
				)
			}
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !decision.Allowed {
			writeSystemAPIRateLimitResponse(w, decision)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (c *systemAPIRateLimitSettingsCache) current(
	ctx context.Context,
	reader port.SystemAPIRateLimitReader,
	now time.Time,
) (port.SystemAPIRateLimitSettings, error) {
	c.mu.Lock()
	if now.Before(c.expiresAt) {
		settings := c.settings
		c.mu.Unlock()
		return settings, nil
	}
	c.mu.Unlock()

	settings, err := reader.SystemAPIRateLimitSettings(ctx)
	if err != nil {
		return port.SystemAPIRateLimitSettings{}, err
	}

	c.mu.Lock()
	c.settings = settings
	c.expiresAt = now.Add(systemAPISettingsCacheTTL)
	c.mu.Unlock()
	return settings, nil
}

func (l *inMemorySystemAPIIPRateLimiter) AllowSystemAPIIP(
	_ context.Context,
	key string,
	settings SystemAPIIPRateLimitSettings,
) (SystemAPIRateLimitDecision, error) {
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
	return SystemAPIRateLimitDecision{Allowed: true}, nil
}

func (l *inMemorySystemAPIAuthenticatedRateLimiter) AllowSystemAPIAuthenticated(
	_ context.Context,
	key string,
	limit int,
) (SystemAPIRateLimitDecision, error) {
	return l.minuteLimiter.Allow(key+":minute", limit, time.Now()), nil
}

func (l *redisSystemAPIIPRateLimiter) AllowSystemAPIIP(
	ctx context.Context,
	key string,
	settings SystemAPIIPRateLimitSettings,
) (SystemAPIRateLimitDecision, error) {
	decision, err := l.client.AllowFixedWindow(ctx, []redisplatform.FixedWindowLimit{
		{Key: "system-api:ip:" + key + ":minute", Limit: settings.PerMinute, Window: rateLimitMinuteWindow},
		{Key: "system-api:ip:" + key + ":burst", Limit: settings.BurstPer10Seconds, Window: rateLimitBurstWindow},
	})
	if err != nil {
		return SystemAPIRateLimitDecision{}, err
	}
	return SystemAPIRateLimitDecision{
		Allowed:           decision.Allowed,
		RetryAfterSeconds: decision.RetryAfterSeconds,
	}, nil
}

func (l *redisSystemAPIAuthenticatedRateLimiter) AllowSystemAPIAuthenticated(
	ctx context.Context,
	key string,
	limit int,
) (SystemAPIRateLimitDecision, error) {
	decision, err := l.client.AllowFixedWindow(ctx, []redisplatform.FixedWindowLimit{
		{Key: "system-api:user:" + key + ":minute", Limit: limit, Window: rateLimitMinuteWindow},
	})
	if err != nil {
		return SystemAPIRateLimitDecision{}, err
	}
	return SystemAPIRateLimitDecision{
		Allowed:           decision.Allowed,
		RetryAfterSeconds: decision.RetryAfterSeconds,
	}, nil
}

func (l *fixedWindowRateLimiter) Allow(key string, limit int, now time.Time) SystemAPIRateLimitDecision {
	l.mu.Lock()
	defer l.mu.Unlock()

	decision := l.decisionLocked(key, limit, now)
	if !decision.Allowed {
		return decision
	}
	l.incrementLocked(key, limit, now)
	return SystemAPIRateLimitDecision{Allowed: true}
}

func (l *fixedWindowRateLimiter) decisionLocked(key string, limit int, now time.Time) SystemAPIRateLimitDecision {
	if limit <= 0 {
		return SystemAPIRateLimitDecision{Allowed: true}
	}
	entry := l.currentEntryLocked(key, now)
	if entry.count >= limit {
		return SystemAPIRateLimitDecision{
			Allowed:           false,
			RetryAfterSeconds: max(1, int(math.Ceil(entry.resetAt.Sub(now).Seconds()))),
		}
	}
	return SystemAPIRateLimitDecision{Allowed: true}
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

func shouldApplySystemAPIIPRateLimit(r *http.Request) bool {
	path := strings.TrimRight(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}
	return path != "/__aisys__/api/health"
}

func systemAPIMethodClassFor(method string) systemAPIMethodClass {
	if isReadMethod(method) {
		return systemAPIMethodRead
	}
	return systemAPIMethodWrite
}

func systemAPIIPRateLimitSettingsFor(settings port.SystemAPIRateLimitSettings, methodClass systemAPIMethodClass) SystemAPIIPRateLimitSettings {
	if methodClass == systemAPIMethodRead {
		return SystemAPIIPRateLimitSettings{
			PerMinute:         settings.IPReadPerMinute,
			BurstPer10Seconds: settings.IPReadBurstPer10Seconds,
		}
	}
	return SystemAPIIPRateLimitSettings{
		PerMinute:         settings.IPWritePerMinute,
		BurstPer10Seconds: settings.IPWriteBurstPer10Seconds,
	}
}

func systemAPIAuthenticatedRateLimitFor(settings port.SystemAPIRateLimitSettings, methodClass systemAPIMethodClass) int {
	if methodClass == systemAPIMethodRead {
		return settings.UserReadPerMinute
	}
	return settings.UserWritePerMinute
}

func systemAPIIPRateLimitKey(clientIP string, methodClass systemAPIMethodClass) string {
	return systemAPIRateLimitKey(clientIP, methodClass)
}

func systemAPIAuthenticatedRateLimitKey(systemAccountID string, methodClass systemAPIMethodClass) string {
	return systemAPIRateLimitKey(systemAccountID, methodClass)
}

func systemAPIRateLimitKey(identity string, methodClass systemAPIMethodClass) string {
	sum := sha256.Sum256([]byte(identity + "\x00" + string(methodClass)))
	return hex.EncodeToString(sum[:])
}

func writeSystemAPIRateLimitResponse(w http.ResponseWriter, decision SystemAPIRateLimitDecision) {
	w.Header().Set("Retry-After", intString(max(1, decision.RetryAfterSeconds)))
	writeMessageError(w, http.StatusTooManyRequests, "请求过于频繁，请稍后重试")
}

func intString(value int) string {
	return strconv.Itoa(value)
}
