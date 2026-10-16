package gatewaysession

import (
	"context"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// Session affinity service core: key resolution, claim / remember / forget /
// migrate, and the traffic migration preference. Migrated from
// runtime/session-affinity.service.ts (git HEAD). Node keeps process-global
// singletons plus module-level config; the Go service carries the same state
// per instance with one state mutex (Node relies on the single-threaded event
// loop).
const (
	sessionAffinityTtlMs            = int64(60 * 60 * 1000)
	trafficMigrationPreferenceTtlMs = sessionAffinityTtlMs

	redisSessionAffinityIndexTtlPaddingMs = int64(60_000)

	// redisMissingBindingExpectedValue mirrors redisMissingBindingExpectedValue.
	redisMissingBindingExpectedValue = ""

	// Cache sizing mirrors the Node createAppCache options.
	sessionAffinityCacheMaxEntries            = 5000
	trafficMigrationPreferenceCacheMaxEntries = 1000
)

// CacheDriver mirrors runtimeConfig.cacheDriver.
type CacheDriver string

const (
	CacheDriverMemory CacheDriver = "memory"
	CacheDriverRedis  CacheDriver = "redis"
)

// RuntimeStateDriver mirrors runtimeConfig.runtimeStateDriver.
type RuntimeStateDriver string

const (
	RuntimeStateDriverMemory RuntimeStateDriver = "memory"
	RuntimeStateDriverRedis  RuntimeStateDriver = "redis"
)

// OpenAIGatewaySessionAffinityScope mirrors OpenAIGatewaySessionAffinityScope.
type OpenAIGatewaySessionAffinityScope struct {
	SystemAccountID string
	APIKeyID        string
	GroupID         string
}

// SessionBinding mirrors the internal SessionBinding. UpstreamApiKeyFingerprint
// 是账户内 Key 会话黏性的可选扩展（上游 Key 明文的 HMAC-SHA256 指纹，64 位
// 小写 hex）；仅最终协议成功后经 RememberOpenAIApiKeyFingerprintAsync 写入，
// 不参与亲和索引键，也不随账户迁移保留（迁移写回为字段级重建）。
type SessionBinding struct {
	AccountID                 string
	Scope                     *OpenAIGatewaySessionAffinityScope
	TrafficMigrationPreferred bool
	UpstreamApiKeyFingerprint string
}

// validUpstreamApiKeyFingerprint 报告值是否为合法指纹：恰好 64 个 [0-9a-f]
// 字符（与 apiKeyFingerprint 的 hex.EncodeToString 输出同形）。绑定 JSON 解析
// 与 Key 指纹写回入口共用该判据。
func validUpstreamApiKeyFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ValidUpstreamApiKeyFingerprint 是 validUpstreamApiKeyFingerprint 的导出镜像：
// cmd 侧 localSessionAffinity 的 Key 写回入口与测试共用同一指纹格式判据。
func ValidUpstreamApiKeyFingerprint(value string) bool {
	return validUpstreamApiKeyFingerprint(value)
}

// redisSessionBindingRecord mirrors the internal RedisSessionBindingRecord.
type redisSessionBindingRecord struct {
	binding  SessionBinding
	rawValue string
}

// TrafficMigrationPreference mirrors the internal TrafficMigrationPreference.
type TrafficMigrationPreference struct {
	SourceAccountID string
	TargetAccountID string
}

// TrafficMigrationPreferenceWriteOptions mirrors
// TrafficMigrationPreferenceWriteOptions.
type TrafficMigrationPreferenceWriteOptions struct {
	ThrowOnRedisError bool
}

// MigrationOptions mirrors the migrate options.
type MigrationOptions struct {
	PreferMigratedSessions bool
}

// MigrationResult mirrors { migratedSessionCount }.
type MigrationResult struct {
	MigratedSessionCount int
}

// AffinityLogger ports the shared logger warn surface with the Node field
// shape (errorLogFields(error, fields)).
type AffinityLogger interface {
	Warn(fields map[string]any, message string)
}

// AffinityConfig mirrors the runtimeConfig fields the Node module reads plus
// the injectable seams (clock, redis override, logger, concurrency source).
type AffinityConfig struct {
	CacheDriver        CacheDriver
	RuntimeStateDriver RuntimeStateDriver
	// Secret mirrors runtimeConfig.secret (the deriveGatewaySessionAffinityKey
	// fallback). Must be non-empty, like the Node runtime default.
	Secret         string
	RedisCacheURL  string
	RedisNamespace string
	// ConcurrencyGlobalMax mirrors runtimeConfig.concurrency.globalMax
	// (default 5000) used by the scheduling policy defaults.
	ConcurrencyGlobalMax int64
	// Clock injects time; defaults to time.Now.
	Clock func() time.Time
	// Redis overrides the lazy go-redis dial
	// (setOpenAISessionAffinityRedisClientForTest).
	Redis RedisClient
	// Logger defaults to a no-op.
	Logger AffinityLogger
	// Concurrency is required by the high-concurrency ordering / busy checks.
	Concurrency ConcurrencySource
}

// AffinityService carries the session affinity state.
type AffinityService struct {
	cfg                AffinityConfig
	schedulingDefaults SchedulingDefaults
	clock              func() time.Time
	logger             AffinityLogger
	redis              RedisClient

	mu                             sync.Mutex
	sessionAffinityCache           *ttlCache[*SessionBinding]
	bindingByKey                   map[string]*SessionBinding
	keysByAccountID                map[string]map[string]struct{}
	keysByAccountSystemScope       map[string]map[string]struct{}
	keysByAccountSystemAPIKeyScope map[string]map[string]struct{}
	trafficMigrationPreference     *ttlCache[TrafficMigrationPreference]
}

// NewAffinityService builds the service. The secret guard mirrors Node's
// versionedHmac('Gateway session identity HMAC secret must not be empty').
func NewAffinityService(cfg AffinityConfig) (*AffinityService, error) {
	if err := validateIdentitySecret(cfg.Secret); err != nil {
		return nil, err
	}
	globalMax := cfg.ConcurrencyGlobalMax
	if globalMax == 0 {
		globalMax = 5000
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	logger := cfg.Logger
	if logger == nil {
		logger = noopAffinityLogger{}
	}
	redisDriver := cfg.Redis
	if redisDriver == nil && cfg.RedisCacheURL != "" {
		redisDriver = newLazyRedisClient(cfg.RedisCacheURL)
	}
	s := &AffinityService{
		cfg:                            cfg,
		schedulingDefaults:             SchedulingDefaults{GlobalMax: globalMax},
		clock:                          clock,
		logger:                         logger,
		redis:                          redisDriver,
		bindingByKey:                   make(map[string]*SessionBinding),
		keysByAccountID:                make(map[string]map[string]struct{}),
		keysByAccountSystemScope:       make(map[string]map[string]struct{}),
		keysByAccountSystemAPIKeyScope: make(map[string]map[string]struct{}),
	}
	s.sessionAffinityCache = newTTLCache[*SessionBinding](sessionAffinityCacheMaxEntries, time.Duration(sessionAffinityTtlMs)*time.Millisecond, true)
	s.sessionAffinityCache.readable = func() bool { return s.cfg.CacheDriver != CacheDriverRedis }
	s.sessionAffinityCache.onReset = s.clearSessionAffinityIndexesLocked
	s.sessionAffinityCache.dispose = s.removeSessionAffinityIndexLocked
	s.sessionAffinityCache.now = s.clock
	s.trafficMigrationPreference = newTTLCache[TrafficMigrationPreference](trafficMigrationPreferenceCacheMaxEntries, time.Duration(trafficMigrationPreferenceTtlMs)*time.Millisecond, false)
	s.trafficMigrationPreference.readable = func() bool { return s.cfg.CacheDriver != CacheDriverRedis }
	s.trafficMigrationPreference.now = s.clock
	return s, nil
}

type noopAffinityLogger struct{}

func (noopAffinityLogger) Warn(fields map[string]any, message string) {}

func (s *AffinityService) warn(event string, err error, fields map[string]any, message string) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["event"] = event
	if err != nil {
		fields["error"] = err.Error()
	}
	s.logger.Warn(fields, message)
}

// shouldUseRedisSessionAffinity mirrors shouldUseRedisSessionAffinity.
func (s *AffinityService) shouldUseRedisSessionAffinity() bool {
	return s.cfg.CacheDriver == CacheDriverRedis
}

// UsesRedisSessionAffinity 是 shouldUseRedisSessionAffinity 的导出只读镜像：
// 组合根据此分叉 dispatch 侧会话亲和端口（Redis cacheDriver → 与 preauth
// 共用本服务；memory → 进程内 localSessionAffinity 显式降级）。
func (s *AffinityService) UsesRedisSessionAffinity() bool {
	return s.shouldUseRedisSessionAffinity()
}

// canUseProcessLocalSessionAffinity mirrors canUseProcessLocalSessionAffinity.
func (s *AffinityService) canUseProcessLocalSessionAffinity() bool {
	if s.cfg.CacheDriver != CacheDriverRedis {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearSessionAffinityIndexesLocked()
	return false
}

// clearSessionAffinityIndexesLocked mirrors clearSessionAffinityIndexes.
// Caller must hold s.mu.
func (s *AffinityService) clearSessionAffinityIndexesLocked() {
	s.bindingByKey = make(map[string]*SessionBinding)
	s.keysByAccountID = make(map[string]map[string]struct{})
	s.keysByAccountSystemScope = make(map[string]map[string]struct{})
	s.keysByAccountSystemAPIKeyScope = make(map[string]map[string]struct{})
}

// ResolveOpenAIGatewaySessionAffinityKey mirrors
// resolveOpenAIGatewaySessionAffinityKey: false when the identity carries no
// conversation key.
func (s *AffinityService) ResolveOpenAIGatewaySessionAffinityKey(conversationKey string, input GatewaySessionAffinityKeyScope) (string, bool) {
	if conversationKey == "" {
		return "", false
	}
	key, err := s.DeriveAffinityKey(conversationKey, input)
	if err != nil {
		// Unreachable after construction-time secret validation; mirrors the
		// Node versionedHmac throw surface.
		return "", false
	}
	return key, true
}

// DeriveAffinityKey mirrors deriveGatewaySessionAffinityKey with the service
// secret as the fallback.
func (s *AffinityService) DeriveAffinityKey(conversationKey string, input GatewaySessionAffinityKeyScope) (string, error) {
	if input.HMACSecret == "" {
		input.HMACSecret = s.cfg.Secret
	}
	return DeriveGatewaySessionAffinityKeyFromConversationKey(conversationKey, input)
}

// ResolveOpenAIGatewaySessionAffinityKeyFromClientSource mirrors
// resolveOpenAIGatewaySessionAffinityKeyFromClientSource: the client-source
// affinity key is trimmed; an absent / blank key never creates affinity.
func (s *AffinityService) ResolveOpenAIGatewaySessionAffinityKeyFromClientSource(affinityKey string, input GatewaySessionAffinityKeyScope) (string, bool) {
	trimmed := jsTrimString(affinityKey)
	if trimmed == "" {
		return "", false
	}
	key, err := s.DeriveAffinityKey(trimmed, input)
	if err != nil {
		return "", false
	}
	return key, true
}

// RememberOpenAIAccountForSession mirrors rememberOpenAIAccountForSession:
// fire-and-forget under the Redis driver.
func (s *AffinityService) RememberOpenAIAccountForSession(sessionAffinityKey string, accountID string, scope *OpenAIGatewaySessionAffinityScope) {
	if sessionAffinityKey == "" {
		return
	}
	if s.shouldUseRedisSessionAffinity() {
		go func() {
			defer safego.Recover("gatewaysession.affinity.remember")
			_, _ = s.ClaimOpenAIAccountForSessionAsync(context.Background(), sessionAffinityKey, accountID, scope)
		}()
		return
	}
	if !s.canUseProcessLocalSessionAffinity() {
		return
	}
	s.RememberOpenAIAccountForSessionLocal(sessionAffinityKey, accountID, scope)
}

// RememberOpenAIAccountForSessionAsync mirrors rememberOpenAIAccountForSessionAsync.
func (s *AffinityService) RememberOpenAIAccountForSessionAsync(ctx context.Context, sessionAffinityKey string, accountID string, scope *OpenAIGatewaySessionAffinityScope) {
	_, _ = s.ClaimOpenAIAccountForSessionAsync(ctx, sessionAffinityKey, accountID, scope)
}

// StickyOpenAIApiKeyFingerprintAsync 返回会话绑定中属于 accountID 的上游 Key
// 指纹（账户内 Key 会话黏性读）。绑定缺失、过期、属于其他账户或读取失败均
// 返回空串——降级为无黏性；读取失败按既有亲和读取失败路径告警。读取不刷新
// 绑定 TTL（刷新责任归排序读 / claim / 成功写回）。
func (s *AffinityService) StickyOpenAIApiKeyFingerprintAsync(ctx context.Context, sessionAffinityKey string, accountID string) string {
	if sessionAffinityKey == "" || accountID == "" {
		return ""
	}
	if !s.shouldUseRedisSessionAffinity() {
		if !s.canUseProcessLocalSessionAffinity() {
			return ""
		}
		return s.stickyOpenAIApiKeyFingerprintLocal(sessionAffinityKey, accountID)
	}
	record, err := s.getRedisSessionAffinityRecord(ctx, sessionAffinityKey, false)
	if err != nil {
		s.warn("redis_openai_session_affinity_read_failed", err, nil, "Redis 会话亲和绑定读取失败，已跳过本次 Key 黏性读取")
		return ""
	}
	if record == nil || record.binding.AccountID != accountID {
		return ""
	}
	return record.binding.UpstreamApiKeyFingerprint
}

// stickyOpenAIApiKeyFingerprintLocal 是黏性读的进程内路径。Caller 无需持锁。
func (s *AffinityService) stickyOpenAIApiKeyFingerprintLocal(sessionAffinityKey string, accountID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding := s.sessionAffinityCacheGetLocked(sessionAffinityKey)
	if binding == nil || binding.AccountID != accountID {
		return ""
	}
	return binding.UpstreamApiKeyFingerprint
}

// RememberOpenAIApiKeyFingerprintAsync 把最终协议成功使用的上游 Key 指纹写回
// 现存同 owner 绑定（成功者赢）：不创建绑定、不改账户 owner；记录缺失、过期、
// owner 不匹配或指纹格式非法时跳过。同指纹重复写回等价 TTL 刷新。
func (s *AffinityService) RememberOpenAIApiKeyFingerprintAsync(ctx context.Context, sessionAffinityKey string, accountID string, fingerprint string) {
	if sessionAffinityKey == "" || accountID == "" || !validUpstreamApiKeyFingerprint(fingerprint) {
		return
	}
	if !s.shouldUseRedisSessionAffinity() {
		if !s.canUseProcessLocalSessionAffinity() {
			return
		}
		s.rememberOpenAIApiKeyFingerprintLocal(sessionAffinityKey, accountID, fingerprint)
		return
	}
	s.rememberOpenAIApiKeyFingerprintRedis(ctx, sessionAffinityKey, accountID, fingerprint)
}

// rememberOpenAIApiKeyFingerprintLocal 是 Key 指纹写回的进程内路径：互斥锁内
// 校验现存未过期同 owner 记录后原位更新；TTLCache.Set 重置过期时间即刷新 TTL。
func (s *AffinityService) rememberOpenAIApiKeyFingerprintLocal(sessionAffinityKey string, accountID string, fingerprint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding := s.sessionAffinityCacheGetLocked(sessionAffinityKey)
	if binding == nil || binding.AccountID != accountID {
		return
	}
	updated := *binding
	updated.UpstreamApiKeyFingerprint = fingerprint
	s.setSessionAffinityBindingLocked(sessionAffinityKey, updated)
}

// rememberOpenAIApiKeyFingerprintRedis 是 Key 指纹写回的 Redis 路径：最多两轮
// GET + CAS（expected=该轮 GET 的 rawValue）；每轮 GET 后记录缺失或 owner 不
// 匹配即停止，不创建绑定；第二轮仍冲突则告警放弃。set 脚本同时维护索引与 TTL。
func (s *AffinityService) rememberOpenAIApiKeyFingerprintRedis(ctx context.Context, sessionAffinityKey string, accountID string, fingerprint string) {
	for attempt := 0; attempt < 2; attempt++ {
		record, err := s.getRedisSessionAffinityRecord(ctx, sessionAffinityKey, false)
		if err != nil {
			s.warn("redis_openai_session_affinity_api_key_write_failed", err, map[string]any{
				"accountId": accountID,
			}, "Redis 会话 Key 指纹写回失败，已跳过本次写回")
			return
		}
		if record == nil || record.binding.AccountID != accountID {
			return
		}
		updated := record.binding
		updated.UpstreamApiKeyFingerprint = fingerprint
		written, err := s.setRedisSessionAffinityBinding(ctx, sessionAffinityKey, updated, record)
		if err != nil {
			s.warn("redis_openai_session_affinity_api_key_write_failed", err, map[string]any{
				"accountId": accountID,
			}, "Redis 会话 Key 指纹写回失败，已跳过本次写回")
			return
		}
		if written {
			return
		}
	}
	s.warn("redis_openai_session_affinity_api_key_cas_conflict", nil, map[string]any{
		"accountId": accountID,
	}, "Redis 会话 Key 指纹写回连续 CAS 冲突，已跳过本次写回")
}

// ClaimOpenAIAccountForSessionAsync mirrors claimOpenAIAccountForSessionAsync:
// first binder wins; re-claiming the bound account refreshes the binding TTL.
// Redis failures warn and yield ("", false), exactly like the Node catch.
func (s *AffinityService) ClaimOpenAIAccountForSessionAsync(ctx context.Context, sessionAffinityKey string, accountID string, scope *OpenAIGatewaySessionAffinityScope) (string, bool) {
	if sessionAffinityKey == "" {
		return "", false
	}
	if !s.shouldUseRedisSessionAffinity() {
		if !s.canUseProcessLocalSessionAffinity() {
			return "", false
		}
		return s.claimOpenAIAccountForSessionLocal(sessionAffinityKey, accountID, scope), true
	}
	owner, err := s.claimRedis(ctx, sessionAffinityKey, accountID, scope)
	if err != nil {
		s.warn("redis_openai_session_affinity_remember_failed", err, map[string]any{
			"accountId": accountID,
		}, "Redis 会话亲和绑定写入失败，已跳过本次亲和记录")
		return "", false
	}
	return owner, owner != ""
}

// claimRedis mirrors the Node try{} block: read previous, up to two
// compare-and-set attempts.
func (s *AffinityService) claimRedis(ctx context.Context, sessionAffinityKey string, accountID string, scope *OpenAIGatewaySessionAffinityScope) (string, error) {
	previous, err := s.getRedisSessionAffinityRecord(ctx, sessionAffinityKey, false)
	if err != nil {
		return "", err
	}
	for attempt := 0; attempt < 2; attempt++ {
		if previous != nil {
			if previous.binding.AccountID == accountID {
				client, err := s.redisSessionAffinityClient(ctx)
				if err != nil {
					return "", err
				}
				if _, err := s.refreshRedisSessionAffinityBinding(ctx, client, sessionAffinityKey, previous); err != nil {
					return "", err
				}
			}
			return previous.binding.AccountID, nil
		}
		written, err := s.setRedisSessionAffinityBinding(ctx, sessionAffinityKey, SessionBinding{
			AccountID: accountID,
			Scope:     scope,
		}, previous)
		if err != nil {
			return "", err
		}
		if written {
			return accountID, nil
		}
		previous, err = s.getRedisSessionAffinityRecord(ctx, sessionAffinityKey, false)
		if err != nil {
			return "", err
		}
	}
	return "", nil
}

// RememberOpenAIAccountForSessionLocal mirrors rememberOpenAIAccountForSessionLocal.
func (s *AffinityService) RememberOpenAIAccountForSessionLocal(sessionAffinityKey string, accountID string, scope *OpenAIGatewaySessionAffinityScope) {
	s.claimOpenAIAccountForSessionLocal(sessionAffinityKey, accountID, scope)
}

// claimOpenAIAccountForSessionLocal mirrors claimOpenAIAccountForSessionLocal.
func (s *AffinityService) claimOpenAIAccountForSessionLocal(sessionAffinityKey string, accountID string, scope *OpenAIGatewaySessionAffinityScope) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous := s.sessionAffinityCacheGetLocked(sessionAffinityKey); previous != nil {
		return previous.AccountID
	}
	s.setSessionAffinityBindingLocked(sessionAffinityKey, SessionBinding{AccountID: accountID, Scope: scope})
	return accountID
}

// RememberOpenAIAccountTrafficMigrationPreference mirrors
// rememberOpenAIAccountTrafficMigrationPreference (fire-and-forget under
// Redis).
func (s *AffinityService) RememberOpenAIAccountTrafficMigrationPreference(sourceAccountID string, targetAccountID string, scope *OpenAIGatewaySessionAffinityScope) {
	source, sourceOK := stringValue(sourceAccountID)
	target, targetOK := stringValue(targetAccountID)
	key, keyOK := trafficMigrationPreferenceScopeKey(scope)
	if !sourceOK || !targetOK || !keyOK || source == target {
		return
	}
	if s.shouldUseRedisSessionAffinity() {
		go func() {
			defer safego.Recover("gatewaysession.affinity.traffic_migration")
			_ = s.RememberOpenAIAccountTrafficMigrationPreferenceAsync(context.Background(), source, target, scope, TrafficMigrationPreferenceWriteOptions{})
		}()
		return
	}
	if !s.canUseProcessLocalSessionAffinity() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trafficMigrationPreference.Set(key, TrafficMigrationPreference{
		SourceAccountID: source,
		TargetAccountID: target,
	})
}

// RememberOpenAIAccountTrafficMigrationPreferenceAsync mirrors the async
// variant; with ThrowOnRedisError the write error is returned instead of only
// warned.
func (s *AffinityService) RememberOpenAIAccountTrafficMigrationPreferenceAsync(ctx context.Context, sourceAccountID string, targetAccountID string, scope *OpenAIGatewaySessionAffinityScope, options TrafficMigrationPreferenceWriteOptions) error {
	source, sourceOK := stringValue(sourceAccountID)
	target, targetOK := stringValue(targetAccountID)
	key, keyOK := trafficMigrationPreferenceScopeKey(scope)
	if !sourceOK || !targetOK || !keyOK || source == target {
		return nil
	}
	if !s.shouldUseRedisSessionAffinity() {
		s.RememberOpenAIAccountTrafficMigrationPreference(source, target, scope)
		return nil
	}
	if err := s.setRedisTrafficMigrationPreference(ctx, key, TrafficMigrationPreference{
		SourceAccountID: source,
		TargetAccountID: target,
	}); err != nil {
		s.warn("redis_openai_traffic_migration_preference_write_failed", err, map[string]any{
			"sourceAccountId": source,
			"targetAccountId": target,
		}, "Redis 流量迁移偏向写入失败，已跳过本次偏向记录")
		if options.ThrowOnRedisError {
			return err
		}
	}
	return nil
}

// ForgetOpenAIAccountForSession mirrors forgetOpenAIAccountForSession: under
// the Redis driver the sync entry point is refused with a warn.
func (s *AffinityService) ForgetOpenAIAccountForSession(sessionAffinityKey string, accountID string) {
	if sessionAffinityKey == "" {
		return
	}
	if s.shouldUseRedisSessionAffinity() {
		s.logger.Warn(map[string]any{
			"event":     "redis_openai_session_affinity_sync_forget_ignored",
			"accountId": accountID,
		}, "Redis cache driver 下必须使用异步会话亲和清理入口")
		return
	}
	if !s.canUseProcessLocalSessionAffinity() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	binding := s.sessionAffinityCacheGetLocked(sessionAffinityKey)
	if binding == nil {
		return
	}
	if accountID != "" && binding.AccountID != accountID {
		return
	}
	s.sessionAffinityCache.Delete(sessionAffinityKey)
}

// ForgetOpenAIAccountForSessionAsync mirrors forgetOpenAIAccountForSessionAsync;
// Redis failures warn and return nil (the Node catch swallows).
func (s *AffinityService) ForgetOpenAIAccountForSessionAsync(ctx context.Context, sessionAffinityKey string, accountID string) error {
	if sessionAffinityKey == "" {
		return nil
	}
	if !s.shouldUseRedisSessionAffinity() {
		s.ForgetOpenAIAccountForSession(sessionAffinityKey, accountID)
		return nil
	}
	if err := s.forgetRedis(ctx, sessionAffinityKey, accountID); err != nil {
		s.warn("redis_openai_session_affinity_forget_failed", err, map[string]any{
			"accountId": accountID,
		}, "Redis 会话亲和绑定清理失败，已跳过本次清理")
	}
	return nil
}

func (s *AffinityService) forgetRedis(ctx context.Context, sessionAffinityKey string, accountID string) error {
	record, err := s.getRedisSessionAffinityRecord(ctx, sessionAffinityKey, false)
	if err != nil {
		return err
	}
	if record == nil {
		return nil
	}
	if accountID != "" && record.binding.AccountID != accountID {
		return nil
	}
	client, err := s.redisSessionAffinityClient(ctx)
	if err != nil {
		return err
	}
	_, err = s.deleteRedisSessionAffinityBinding(ctx, client, sessionAffinityKey, record)
	return err
}

// MigrateOpenAIAccountSessionAffinity mirrors migrateOpenAIAccountSessionAffinity.
func (s *AffinityService) MigrateOpenAIAccountSessionAffinity(sourceAccountID string, targetAccountID string, scope *OpenAIGatewaySessionAffinityScope, options MigrationOptions) MigrationResult {
	if !s.canUseProcessLocalSessionAffinity() {
		return MigrationResult{}
	}
	migratedSessionCount := 0
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range s.sessionAffinityMigrationCandidateKeysLocked(sourceAccountID, scope) {
		binding := s.sessionAffinityCacheGetLocked(key)
		if binding == nil {
			continue
		}
		if binding.AccountID != sourceAccountID {
			continue
		}
		if scope != nil && !sessionBindingMatchesScope(binding, scope) {
			continue
		}
		s.setSessionAffinityBindingLocked(key, SessionBinding{
			AccountID:                 targetAccountID,
			Scope:                     binding.Scope,
			TrafficMigrationPreferred: options.PreferMigratedSessions,
		})
		migratedSessionCount++
	}
	return MigrationResult{MigratedSessionCount: migratedSessionCount}
}

// MigrateOpenAIAccountSessionAffinityAsync mirrors
// migrateOpenAIAccountSessionAffinityAsync: Redis failures warn and rethrow.
func (s *AffinityService) MigrateOpenAIAccountSessionAffinityAsync(ctx context.Context, sourceAccountID string, targetAccountID string, scope *OpenAIGatewaySessionAffinityScope, options MigrationOptions) (MigrationResult, error) {
	if !s.shouldUseRedisSessionAffinity() {
		return s.MigrateOpenAIAccountSessionAffinity(sourceAccountID, targetAccountID, scope, options), nil
	}
	result, err := s.migrateRedisOpenAIAccountSessionAffinity(ctx, sourceAccountID, targetAccountID, scope, options)
	if err != nil {
		s.warn("redis_openai_session_affinity_migration_failed", err, map[string]any{
			"sourceAccountId": sourceAccountID,
			"targetAccountId": targetAccountID,
		}, "Redis 会话亲和迁移失败")
		return MigrationResult{}, err
	}
	return result, nil
}

func (s *AffinityService) migrateRedisOpenAIAccountSessionAffinity(ctx context.Context, sourceAccountID string, targetAccountID string, scope *OpenAIGatewaySessionAffinityScope, options MigrationOptions) (MigrationResult, error) {
	source, sourceOK := stringValue(sourceAccountID)
	target, targetOK := stringValue(targetAccountID)
	if !sourceOK || !targetOK || source == target {
		return MigrationResult{}, nil
	}
	migratedSessionCount := 0
	candidateKeys, err := s.redisSessionAffinityMigrationCandidateKeys(ctx, source, scope)
	if err != nil {
		return MigrationResult{}, err
	}
	for _, key := range candidateKeys {
		record, err := s.getRedisSessionAffinityRecord(ctx, key, false)
		if err != nil {
			return MigrationResult{}, err
		}
		if record == nil {
			continue
		}
		if record.binding.AccountID != source {
			continue
		}
		if scope != nil && !sessionBindingMatchesScope(&record.binding, scope) {
			continue
		}
		migrated, err := s.setRedisSessionAffinityBinding(ctx, key, SessionBinding{
			AccountID:                 target,
			Scope:                     record.binding.Scope,
			TrafficMigrationPreferred: options.PreferMigratedSessions,
		}, record)
		if err != nil {
			return MigrationResult{}, err
		}
		if migrated {
			migratedSessionCount++
		}
	}
	return MigrationResult{MigratedSessionCount: migratedSessionCount}, nil
}

// setSessionAffinityBindingLocked mirrors setSessionAffinityBinding.
// Caller must hold s.mu.
func (s *AffinityService) setSessionAffinityBindingLocked(key string, binding SessionBinding) {
	if !s.canUseProcessLocalLocked() {
		return
	}
	if previous := s.bindingByKey[key]; previous != nil {
		s.removeSessionAffinityIndexLocked(key, previous)
	}
	stored := &binding
	s.sessionAffinityCache.Set(key, stored)
	s.addSessionAffinityIndexLocked(key, stored)
}

// canUseProcessLocalLocked is canUseProcessLocalSessionAffinity for callers
// already holding s.mu.
func (s *AffinityService) canUseProcessLocalLocked() bool {
	if s.cfg.CacheDriver != CacheDriverRedis {
		return true
	}
	s.clearSessionAffinityIndexesLocked()
	return false
}

// addSessionAffinityIndexLocked mirrors addSessionAffinityIndex.
func (s *AffinityService) addSessionAffinityIndexLocked(key string, binding *SessionBinding) {
	s.bindingByKey[key] = binding
	addSetValue(s.keysByAccountID, binding.AccountID, key)
	if binding.Scope != nil && binding.Scope.SystemAccountID != "" {
		addSetValue(s.keysByAccountSystemScope, accountSystemScopeIndexKey(binding.AccountID, binding.Scope.SystemAccountID), key)
		if binding.Scope.APIKeyID != "" {
			addSetValue(s.keysByAccountSystemAPIKeyScope, accountSystemAPIKeyScopeIndexKey(binding.AccountID, binding.Scope.SystemAccountID, binding.Scope.APIKeyID), key)
		}
	}
}

// removeSessionAffinityIndexLocked mirrors removeSessionAffinityIndex.
func (s *AffinityService) removeSessionAffinityIndexLocked(key string, binding *SessionBinding) {
	if s.bindingByKey[key] != binding {
		return
	}
	delete(s.bindingByKey, key)
	deleteSetValue(s.keysByAccountID, binding.AccountID, key)
	if binding.Scope != nil && binding.Scope.SystemAccountID != "" {
		deleteSetValue(s.keysByAccountSystemScope, accountSystemScopeIndexKey(binding.AccountID, binding.Scope.SystemAccountID), key)
		if binding.Scope.APIKeyID != "" {
			deleteSetValue(s.keysByAccountSystemAPIKeyScope, accountSystemAPIKeyScopeIndexKey(binding.AccountID, binding.Scope.SystemAccountID, binding.Scope.APIKeyID), key)
		}
	}
}

// sessionAffinityMigrationCandidateKeysLocked mirrors
// sessionAffinityMigrationCandidateKeys. Caller must hold s.mu.
func (s *AffinityService) sessionAffinityMigrationCandidateKeysLocked(sourceAccountID string, scope *OpenAIGatewaySessionAffinityScope) []string {
	if !s.canUseProcessLocalLocked() {
		return nil
	}
	if scope != nil && scope.SystemAccountID != "" && scope.APIKeyID != "" {
		return stringSetValues(s.keysByAccountSystemAPIKeyScope[accountSystemAPIKeyScopeIndexKey(sourceAccountID, scope.SystemAccountID, scope.APIKeyID)])
	}
	if scope != nil && scope.SystemAccountID != "" {
		return stringSetValues(s.keysByAccountSystemScope[accountSystemScopeIndexKey(sourceAccountID, scope.SystemAccountID)])
	}
	return stringSetValues(s.keysByAccountID[sourceAccountID])
}

// sessionAffinityCacheGetLocked reads the binding cache with the Node get
// semantics (TTL purge + dispose + age refresh). Caller must hold s.mu.
func (s *AffinityService) sessionAffinityCacheGetLocked(key string) *SessionBinding {
	binding, _ := s.sessionAffinityCache.Get(key)
	return binding
}

func sessionBindingMatchesScope(binding *SessionBinding, scope *OpenAIGatewaySessionAffinityScope) bool {
	if binding.Scope == nil {
		return false
	}
	if scope.SystemAccountID != "" && binding.Scope.SystemAccountID != scope.SystemAccountID {
		return false
	}
	if scope.APIKeyID != "" && binding.Scope.APIKeyID != scope.APIKeyID {
		return false
	}
	return true
}

func stringValue(value string) (string, bool) {
	trimmed := jsTrimString(value)
	return trimmed, trimmed != ""
}

func addSetValue(index map[string]map[string]struct{}, key string, value string) {
	values := index[key]
	if values == nil {
		values = make(map[string]struct{})
		index[key] = values
	}
	values[value] = struct{}{}
}

func deleteSetValue(index map[string]map[string]struct{}, key string, value string) {
	values := index[key]
	if values == nil {
		return
	}
	delete(values, value)
	if len(values) == 0 {
		delete(index, key)
	}
}

func stringSetValues(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	return out
}

func accountSystemScopeIndexKey(accountID string, systemAccountID string) string {
	return accountID + ":" + systemAccountID
}

func accountSystemAPIKeyScopeIndexKey(accountID string, systemAccountID string, apiKeyID string) string {
	return accountID + ":" + systemAccountID + ":" + apiKeyID
}

// trafficMigrationPreferenceScopeKey mirrors trafficMigrationPreferenceScopeKey
// (the write key: per-api-key scope when an api key is present, group-wide
// wildcard scope otherwise).
func trafficMigrationPreferenceScopeKey(scope *OpenAIGatewaySessionAffinityScope) (string, bool) {
	keys := trafficMigrationPreferenceScopeKeys(scope)
	if len(keys) == 0 {
		return "", false
	}
	return keys[0], true
}

// trafficMigrationPreferenceScopeKeys mirrors trafficMigrationPreferenceScopeKeys
// (the read order: api-key scope first, then the group-wide scope).
func trafficMigrationPreferenceScopeKeys(scope *OpenAIGatewaySessionAffinityScope) []string {
	systemAccountID, systemOK := stringValue(scopeSystemAccountID(scope))
	groupID, groupOK := stringValue(scopeGroupID(scope))
	if !systemOK || !groupOK {
		return nil
	}
	apiKeyID, apiKeyOK := stringValue(scopeAPIKeyID(scope))
	if apiKeyOK {
		return []string{
			systemAccountID + ":" + apiKeyID + ":" + groupID,
			trafficMigrationGroupPreferenceScopeKey(systemAccountID, groupID),
		}
	}
	return []string{trafficMigrationGroupPreferenceScopeKey(systemAccountID, groupID)}
}

func trafficMigrationGroupPreferenceScopeKey(systemAccountID string, groupID string) string {
	return systemAccountID + ":*:" + groupID
}

func scopeSystemAccountID(scope *OpenAIGatewaySessionAffinityScope) string {
	if scope == nil {
		return ""
	}
	return scope.SystemAccountID
}

func scopeAPIKeyID(scope *OpenAIGatewaySessionAffinityScope) string {
	if scope == nil {
		return ""
	}
	return scope.APIKeyID
}

func scopeGroupID(scope *OpenAIGatewaySessionAffinityScope) string {
	if scope == nil {
		return ""
	}
	return scope.GroupID
}
