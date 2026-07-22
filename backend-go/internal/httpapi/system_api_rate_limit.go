package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycache"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	systemAPISettingsCacheTTL = 60 * time.Second
	rateLimitMinuteWindow     = time.Minute
	rateLimitBurstWindow      = 10 * time.Second

	systemAPIIPMinuteStoreName   = "system_api_ip_minute"
	systemAPIIPBurstStoreName    = "system_api_ip_burst"
	systemAPIUserMinuteStoreName = "system_api_user_minute"

	systemAPIClientIPAllowlistCacheTTL        = 30 * time.Second
	systemAPIClientIPAllowlistCacheMaxEntries = 5000
	systemAPIClientIPAllowlistCacheTrimTarget = 4500
)

type systemAPIIPRateLimitMiddleware struct {
	reader        port.SystemAPIRateLimitReader
	limiter       SystemAPIIPRateLimiter
	clientIPs     clientIPResolver
	allowlist     *systemAPIClientIPAllowlistInspector
	logger        *slog.Logger
	settingsCache SystemAPIRateLimitSettingsCache
}

type systemAPIAuthenticatedRateLimitMiddleware struct {
	reader        port.SystemAPIRateLimitReader
	limiter       SystemAPIAuthenticatedRateLimiter
	clientIPs     clientIPResolver
	allowlist     *systemAPIClientIPAllowlistInspector
	logger        *slog.Logger
	settingsCache SystemAPIRateLimitSettingsCache
}

type systemAPIMethodClass string

const (
	systemAPIMethodRead  systemAPIMethodClass = "read"
	systemAPIMethodWrite systemAPIMethodClass = "write"
)

type systemAPIRouteKey struct {
	method string
	path   string
}

var systemAPIReadRouteOverrides = map[systemAPIRouteKey]struct{}{
	{method: http.MethodPost, path: "/__aisys__/api/accounts/import/preview"}:    {},
	{method: http.MethodPost, path: "/__aisys__/api/my-accounts/import/preview"}: {},
}

type systemAPIRateLimitSettingsCache struct {
	mu            sync.Mutex
	settings      port.SystemAPIRateLimitSettings
	expiresAt     time.Time
	versionReader SystemAPIRateLimitSettingsVersionReader
	version       string
	versionLoaded bool
	generation    uint64
}

type systemAPIClientIPAllowlistInspector struct {
	reader        port.SystemAPIClientIPAllowlistReader
	versionReader SystemAPIClientIPAllowlistVersionReader
	mu            sync.Mutex
	version       string
	versionLoaded bool
	entries       map[string]systemAPIClientIPAllowlistCacheEntry
}

type systemAPIClientIPAllowlistCacheEntry struct {
	allowlisted bool
	expiresAt   time.Time
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

type SystemAPIClientIPAllowlistVersionReader interface {
	SystemAPIClientIPAllowlistVersion(ctx context.Context) (string, error)
}

type SystemAPIRateLimitSettingsVersionReader interface {
	SystemAPIRateLimitSettingsVersion(ctx context.Context) (string, error)
}

type SystemAPIRateLimitSettingsCache interface {
	current(
		ctx context.Context,
		reader port.SystemAPIRateLimitReader,
		now time.Time,
	) (port.SystemAPIRateLimitSettings, error)
	ClearSystemAPIRateLimitSettingsCache()
}

type SystemAPIClientIPAllowlistVersionStore interface {
	GetRaw(ctx context.Context, key string) ([]byte, error)
}

type SystemAPIRateLimitSettingsVersionStore interface {
	GetRaw(ctx context.Context, key string) ([]byte, error)
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

type redisNamedFixedWindowClient interface {
	AllowNamedFixedWindowRaw(context.Context, time.Time, []redisplatform.NamedFixedWindowLimit) (redisplatform.NamedFixedWindowDecision, error)
}

type redisSystemAPIIPRateLimiter struct {
	client    redisNamedFixedWindowClient
	namespace string
}

type redisSystemAPIAuthenticatedRateLimiter struct {
	client    redisNamedFixedWindowClient
	namespace string
}

type redisSystemAPIClientIPAllowlistVersionReader struct {
	client SystemAPIClientIPAllowlistVersionStore
	key    string
}

type redisSystemAPIRateLimitSettingsVersionReader struct {
	client SystemAPIRateLimitSettingsVersionStore
	key    string
}

type systemAPIRateLimitSettingsContextKey struct{}

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
	allowlist *systemAPIClientIPAllowlistInspector,
	logger *slog.Logger,
	settingsCache SystemAPIRateLimitSettingsCache,
) func(http.Handler) http.Handler {
	if limiter == nil {
		panic("SystemAPIIPRateLimiter is required when SystemAPIRateLimitReader is configured")
	}
	if settingsCache == nil {
		settingsCache = NewSystemAPIRateLimitSettingsCache(nil)
	}
	middleware := &systemAPIIPRateLimitMiddleware{
		reader:        reader,
		limiter:       limiter,
		clientIPs:     clientIPs,
		allowlist:     allowlist,
		logger:        logger,
		settingsCache: settingsCache,
	}
	return middleware.handle
}

func newSystemAPIAuthenticatedRateLimitMiddleware(
	reader port.SystemAPIRateLimitReader,
	limiter SystemAPIAuthenticatedRateLimiter,
	clientIPs clientIPResolver,
	allowlist *systemAPIClientIPAllowlistInspector,
	logger *slog.Logger,
	settingsCache SystemAPIRateLimitSettingsCache,
) func(http.Handler) http.Handler {
	if limiter == nil {
		panic("SystemAPIAuthenticatedRateLimiter is required when authenticated management rate limiting is configured")
	}
	if settingsCache == nil {
		settingsCache = NewSystemAPIRateLimitSettingsCache(nil)
	}
	middleware := &systemAPIAuthenticatedRateLimitMiddleware{
		reader:        reader,
		limiter:       limiter,
		clientIPs:     clientIPs,
		allowlist:     allowlist,
		logger:        logger,
		settingsCache: settingsCache,
	}
	return middleware.handle
}

func NewSystemAPIRateLimitSettingsCache(
	versionReader SystemAPIRateLimitSettingsVersionReader,
) SystemAPIRateLimitSettingsCache {
	return &systemAPIRateLimitSettingsCache{versionReader: versionReader}
}

func newSystemAPIClientIPAllowlistInspector(
	reader port.SystemAPIClientIPAllowlistReader,
	versionReader SystemAPIClientIPAllowlistVersionReader,
) *systemAPIClientIPAllowlistInspector {
	if reader == nil {
		return nil
	}
	return &systemAPIClientIPAllowlistInspector{
		reader:        reader,
		versionReader: versionReader,
		entries:       map[string]systemAPIClientIPAllowlistCacheEntry{},
	}
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

func NewRedisSystemAPIIPRateLimiter(client redisNamedFixedWindowClient, namespace string) SystemAPIIPRateLimiter {
	if client == nil {
		return nil
	}
	return &redisSystemAPIIPRateLimiter{client: client, namespace: namespace}
}

func NewRedisSystemAPIAuthenticatedRateLimiter(client redisNamedFixedWindowClient, namespace string) SystemAPIAuthenticatedRateLimiter {
	if client == nil {
		return nil
	}
	return &redisSystemAPIAuthenticatedRateLimiter{client: client, namespace: namespace}
}

func NewRedisSystemAPIClientIPAllowlistVersionReader(
	client SystemAPIClientIPAllowlistVersionStore,
	namespace string,
) (SystemAPIClientIPAllowlistVersionReader, error) {
	if client == nil {
		return nil, nil
	}
	key, err := gatewaycache.SharedCacheVersionKey(namespace, "gateway:client-ip-policy-by-ip")
	if err != nil {
		return nil, err
	}
	return &redisSystemAPIClientIPAllowlistVersionReader{
		client: client,
		key:    key,
	}, nil
}

func NewRedisSystemAPIRateLimitSettingsVersionReader(
	client SystemAPIRateLimitSettingsVersionStore,
	namespace string,
) (SystemAPIRateLimitSettingsVersionReader, error) {
	if client == nil {
		return nil, nil
	}
	key, err := gatewaycache.SharedCacheVersionKey(namespace, gatewaycache.SystemSettingsCacheName)
	if err != nil {
		return nil, err
	}
	return &redisSystemAPIRateLimitSettingsVersionReader{
		client: client,
		key:    key,
	}, nil
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
		r = r.WithContext(context.WithValue(
			r.Context(),
			systemAPIRateLimitSettingsContextKey{},
			settings,
		))

		methodClass := systemAPIRouteClassFor(r)
		clientIP := m.clientIPs.FromRequest(r)
		if systemAPIClientIPRateLimitAllowlisted(r, clientIP, m.allowlist, m.logger) {
			next.ServeHTTP(w, r)
			return
		}
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
		methodClass := systemAPIRouteClassFor(r)
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			next.ServeHTTP(w, r)
			return
		}

		settings, ok := systemAPIRateLimitSettingsFromContext(r.Context())
		if !ok {
			var err error
			settings, err = m.settingsCache.current(r.Context(), m.reader, time.Now())
			if err != nil {
				if m.logger != nil {
					m.logger.Error("系统 API 认证用户限流设置读取失败",
						slog.String("path", r.URL.Path),
						slog.String("method_class", string(methodClass)),
						slog.String("request_id", requestIDFromContext(r.Context())),
						slog.Any("error", err),
					)
				}
				writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
		}

		clientIP := m.clientIPs.FromRequest(r)
		if systemAPIClientIPRateLimitAllowlisted(r, clientIP, m.allowlist, m.logger) {
			next.ServeHTTP(w, r)
			return
		}
		decision, err := m.limiter.AllowSystemAPIAuthenticated(
			r.Context(),
			systemAPIAuthenticatedRateLimitKey(authContext.SystemAccountID, methodClass),
			systemAPIAuthenticatedRateLimitFor(settings, methodClass),
		)
		if err != nil {
			if m.logger != nil {
				m.logger.Error("系统 API 认证用户限流失败",
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

func (i *systemAPIClientIPAllowlistInspector) allowlisted(
	ctx context.Context,
	clientIP string,
	now time.Time,
) (bool, error) {
	ipHash, ok := systemAPIClientIPPolicyHash(clientIP)
	if !ok {
		return false, nil
	}
	if err := i.refreshVersion(ctx); err != nil {
		return false, err
	}

	i.mu.Lock()
	if entry, exists := i.entries[ipHash]; exists && now.Before(entry.expiresAt) {
		i.mu.Unlock()
		return entry.allowlisted, nil
	}
	delete(i.entries, ipHash)
	i.mu.Unlock()

	policy, allowlisted, err := i.reader.FindSystemAPIClientIPAllowlistPolicy(ctx, ipHash, now)
	if err != nil {
		return false, err
	}

	expiresAt := now.Add(systemAPIClientIPAllowlistCacheTTL)
	if allowlisted && policy.ExpiresAt != nil && policy.ExpiresAt.Before(expiresAt) {
		expiresAt = *policy.ExpiresAt
	}
	i.mu.Lock()
	i.entries[ipHash] = systemAPIClientIPAllowlistCacheEntry{
		allowlisted: allowlisted,
		expiresAt:   expiresAt,
	}
	i.trimLocked(now)
	i.mu.Unlock()
	return allowlisted, nil
}

func (i *systemAPIClientIPAllowlistInspector) refreshVersion(ctx context.Context) error {
	if i.versionReader == nil {
		return nil
	}
	version, err := i.versionReader.SystemAPIClientIPAllowlistVersion(ctx)
	if err != nil {
		return err
	}
	i.mu.Lock()
	if !i.versionLoaded {
		i.version = version
		i.versionLoaded = true
	} else if version != i.version {
		i.version = version
		i.entries = map[string]systemAPIClientIPAllowlistCacheEntry{}
	}
	i.mu.Unlock()
	return nil
}

func (i *systemAPIClientIPAllowlistInspector) trimLocked(now time.Time) {
	if len(i.entries) <= systemAPIClientIPAllowlistCacheMaxEntries {
		return
	}
	for key, entry := range i.entries {
		if !entry.expiresAt.After(now) || len(i.entries) > systemAPIClientIPAllowlistCacheTrimTarget {
			delete(i.entries, key)
		}
		if len(i.entries) <= systemAPIClientIPAllowlistCacheTrimTarget {
			break
		}
	}
}

func systemAPIClientIPRateLimitAllowlisted(
	r *http.Request,
	clientIP string,
	inspector *systemAPIClientIPAllowlistInspector,
	logger *slog.Logger,
) bool {
	if inspector == nil {
		return false
	}
	allowlisted, err := inspector.allowlisted(r.Context(), clientIP, time.Now())
	if err == nil {
		return allowlisted
	}
	if logger != nil {
		logger.Warn("后台系统 API 白名单检查失败，本次请求继续执行限流",
			slog.String("path", r.URL.Path),
			slog.String("method", r.Method),
			slog.String("client_ip", clientIP),
			slog.String("request_id", requestIDFromContext(r.Context())),
			slog.Any("error", err),
		)
	}
	return false
}

func systemAPIClientIPPolicyHash(clientIP string) (string, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(clientIP))
	if err != nil || !addr.Is4() {
		return "", false
	}
	sum := sha256.Sum256([]byte("client-ip:" + addr.String()))
	return hex.EncodeToString(sum[:]), true
}

func (r *redisSystemAPIClientIPAllowlistVersionReader) SystemAPIClientIPAllowlistVersion(
	ctx context.Context,
) (string, error) {
	raw, err := r.client.GetRaw(ctx, r.key)
	if errors.Is(err, redisplatform.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func (r *redisSystemAPIRateLimitSettingsVersionReader) SystemAPIRateLimitSettingsVersion(
	ctx context.Context,
) (string, error) {
	raw, err := r.client.GetRaw(ctx, r.key)
	if errors.Is(err, redisplatform.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func (c *systemAPIRateLimitSettingsCache) current(
	ctx context.Context,
	reader port.SystemAPIRateLimitReader,
	now time.Time,
) (port.SystemAPIRateLimitSettings, error) {
	version, err := c.readVersion(ctx)
	if err != nil {
		return port.SystemAPIRateLimitSettings{}, err
	}

	for {
		c.mu.Lock()
		c.applyVersionLocked(version)
		if now.Before(c.expiresAt) {
			settings := c.settings
			c.mu.Unlock()
			return settings, nil
		}
		generation := c.generation
		c.mu.Unlock()

		settings, err := reader.SystemAPIRateLimitSettings(ctx)
		if err != nil {
			return port.SystemAPIRateLimitSettings{}, err
		}

		if c.versionReader != nil {
			latestVersion, err := c.readVersion(ctx)
			if err != nil {
				return port.SystemAPIRateLimitSettings{}, err
			}
			if latestVersion != version {
				version = latestVersion
				continue
			}
		}

		c.mu.Lock()
		if c.generation != generation {
			c.mu.Unlock()
			if c.versionReader != nil {
				version, err = c.readVersion(ctx)
				if err != nil {
					return port.SystemAPIRateLimitSettings{}, err
				}
			}
			continue
		}
		c.settings = settings
		c.expiresAt = now.Add(systemAPISettingsCacheTTL)
		c.mu.Unlock()
		return settings, nil
	}
}

func (c *systemAPIRateLimitSettingsCache) ClearSystemAPIRateLimitSettingsCache() {
	c.mu.Lock()
	c.clearSnapshotLocked()
	c.mu.Unlock()
}

func (c *systemAPIRateLimitSettingsCache) readVersion(ctx context.Context) (string, error) {
	if c.versionReader == nil {
		return "", nil
	}
	return c.versionReader.SystemAPIRateLimitSettingsVersion(ctx)
}

func (c *systemAPIRateLimitSettingsCache) applyVersionLocked(version string) {
	if c.versionReader == nil {
		return
	}
	if !c.versionLoaded {
		c.version = version
		c.versionLoaded = true
		return
	}
	if version == c.version {
		return
	}
	c.clearSnapshotLocked()
	c.version = version
}

func (c *systemAPIRateLimitSettingsCache) clearSnapshotLocked() {
	c.settings = port.SystemAPIRateLimitSettings{}
	c.expiresAt = time.Time{}
	c.generation++
}

func systemAPIRateLimitSettingsFromContext(
	ctx context.Context,
) (port.SystemAPIRateLimitSettings, bool) {
	settings, ok := ctx.Value(systemAPIRateLimitSettingsContextKey{}).(port.SystemAPIRateLimitSettings)
	return settings, ok
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
	minuteKey, err := systemAPIRedisFixedWindowKey(l.namespace, systemAPIIPMinuteStoreName, key)
	if err != nil {
		return SystemAPIRateLimitDecision{}, err
	}
	burstKey, err := systemAPIRedisFixedWindowKey(l.namespace, systemAPIIPBurstStoreName, key)
	if err != nil {
		return SystemAPIRateLimitDecision{}, err
	}
	decision, err := l.client.AllowNamedFixedWindowRaw(ctx, time.Now(), []redisplatform.NamedFixedWindowLimit{
		{RawKey: minuteKey, StoreName: systemAPIIPMinuteStoreName, Limit: settings.PerMinute, Window: rateLimitMinuteWindow},
		{RawKey: burstKey, StoreName: systemAPIIPBurstStoreName, Limit: settings.BurstPer10Seconds, Window: rateLimitBurstWindow},
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
	minuteKey, err := systemAPIRedisFixedWindowKey(l.namespace, systemAPIUserMinuteStoreName, key)
	if err != nil {
		return SystemAPIRateLimitDecision{}, err
	}
	decision, err := l.client.AllowNamedFixedWindowRaw(ctx, time.Now(), []redisplatform.NamedFixedWindowLimit{
		{RawKey: minuteKey, StoreName: systemAPIUserMinuteStoreName, Limit: limit, Window: rateLimitMinuteWindow},
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
	return path != "/__aisys__/api/health" && path != "/__aisys__/api/readyz"
}

func systemAPIRouteClassFor(r *http.Request) systemAPIMethodClass {
	method := strings.ToUpper(strings.TrimSpace(r.Method))
	methodClass := systemAPIMethodClassFor(method)
	if methodClass == systemAPIMethodRead {
		return systemAPIMethodRead
	}
	path := strings.TrimRight(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}
	if _, ok := systemAPIReadRouteOverrides[systemAPIRouteKey{method: method, path: path}]; ok {
		return systemAPIMethodRead
	}
	return systemAPIMethodWrite
}

func systemAPIMethodClassFor(method string) systemAPIMethodClass {
	if isReadMethod(strings.ToUpper(strings.TrimSpace(method))) {
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
	return systemAPIRedisKeyHash(identity + ":" + string(methodClass))
}

func systemAPIRedisFixedWindowKey(namespace string, storeName string, identityHash string) (string, error) {
	return gatewaycache.RedisNamespacedKey(
		namespace,
		"juhe-ai:rate-limit:fixed:"+systemAPIRedisKeyHash(storeName)+":"+identityHash,
	)
}

func systemAPIRedisKeyHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func writeSystemAPIRateLimitResponse(w http.ResponseWriter, decision SystemAPIRateLimitDecision) {
	w.Header().Set("Retry-After", intString(max(1, decision.RetryAfterSeconds)))
	writeMessageError(w, http.StatusTooManyRequests, "请求过于频繁，请稍后重试")
}

func intString(value int) string {
	return strconv.Itoa(value)
}
