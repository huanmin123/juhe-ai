package proberepo

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// randomInt63 提供 [0, 2^63) 的非负随机数（crypto/rand 驱动）。
func randomInt63() (int64, error) {
	var buffer [8]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(buffer[:]) &^ (1 << 63)), nil
}

// 本文件移植 Node gateway/runtime/normal-route-latency-degradation.service.ts
// 的 Redis 降级运行态契约（单实现约定：与 Node 网关读写同一批键，不引入
// 第二套状态形状）：
//   - 键前缀：juhe-ai:{namespace}:state:gateway-normal-route-latency-degradation:
//     （redisNamespacedKey('juhe-ai:state:...') 的等价展开）；
//   - 子键：v1:generation / v1:all-index / v1:probe-index /
//     v1:mutation-lock:{key} / v1:probe-claim:{generation}:{key}；
//   - state JSON 形状与 candidate-match 围栏逐字段一致；
//   - claim/mutation 锁与 CAS Lua 语义与 shared/runtime-state-store.ts 一致。

const (
	speedFirstRedisGroup           = "state:gateway-normal-route-latency-degradation"
	speedFirstStateVersion         = "v1"
	speedFirstGenerationTTL        = 48 * time.Hour
	speedFirstIndexTTL             = 24 * time.Hour
	speedFirstIndexMaxKeys         = 10_000
	speedFirstClaimTTL             = 2 * time.Minute
	speedFirstMutationLockTTL      = 15 * time.Second
	speedFirstLockMaxAttempts      = 50
	speedFirstRecoveryRoundSize    = 2
	speedFirstRecoveryIntervalMS   = 5_000
	speedFirstGenerationCASRetries = 8
)

// SpeedFirstRedisConfig 是速度优先降级运行态的 Redis 连接约定。
type SpeedFirstRedisConfig struct {
	Enabled   bool
	URL       string
	Namespace string
}

var speedFirstNamespacePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// LoadSpeedFirstRedisConfig 读取 JUHE_AI_REDIS_STATE_URL / JUHE_AI_REDIS_NAMESPACE。
func LoadSpeedFirstRedisConfig(getenv func(string) string) (SpeedFirstRedisConfig, error) {
	if getenv == nil {
		return SpeedFirstRedisConfig{}, errors.New("getenv 不能为空")
	}
	config := SpeedFirstRedisConfig{
		URL:       strings.TrimSpace(getenv("JUHE_AI_REDIS_STATE_URL")),
		Namespace: strings.TrimSpace(getenv("JUHE_AI_REDIS_NAMESPACE")),
	}
	config.Enabled = config.URL != ""
	if !config.Enabled {
		return config, nil
	}
	if !speedFirstNamespacePattern.MatchString(config.Namespace) {
		return config, errors.New("启用速度优先恢复探针必须配置合法 JUHE_AI_REDIS_NAMESPACE")
	}
	return config, nil
}

// ValidSpeedFirstNamespace 校验命名空间形状（与 LoadSpeedFirstRedisConfig 一致）。
func ValidSpeedFirstNamespace(namespace string) bool {
	return speedFirstNamespacePattern.MatchString(strings.TrimSpace(namespace))
}

// SpeedFirstStore 是 opsjobs.SpeedFirstClaimStore 的 Redis 实现。
type SpeedFirstStore struct {
	client *redis.Client
	prefix string
	now    func() time.Time
	random func() float64
}

func defaultRandom() float64 {
	value, err := randomInt63()
	if err != nil {
		return 0
	}
	return float64(value) / (1 << 63)
}

// stateKeyFor 等价 accountLatencyStateKey（Node 将 runtimeKey 作为第 5 段）。
func stateKeyFor(scope speedFirstScope, runtimeKey string) string {
	return strings.Join([]string{
		speedFirstStateVersion,
		sanitizeKeyPart(scope.SystemAccountID),
		sanitizeKeyPart(scope.RouteStrategyID),
		sanitizeKeyPart(scope.GroupID),
		sanitizeKeyPart(runtimeKey),
	}, ":")
}

// OpenSpeedFirstStore 建立 Redis 连接。
func OpenSpeedFirstStore(config SpeedFirstRedisConfig, now func() time.Time) (*SpeedFirstStore, error) {
	if !config.Enabled {
		return nil, errors.New("速度优先恢复探针 Redis 未启用")
	}
	options, err := redis.ParseURL(config.URL)
	if err != nil {
		return nil, fmt.Errorf("解析速度优先降级运行态 Redis URL: %w", err)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &SpeedFirstStore{
		client: redis.NewClient(options),
		prefix: "juhe-ai:" + config.Namespace + ":" + speedFirstRedisGroup + ":",
		now:    now,
		random: defaultRandom,
	}, nil
}

// Close 释放 Redis 连接。
func (s *SpeedFirstStore) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

func (s *SpeedFirstStore) redisKey(key string) string { return s.prefix + key }

func (s *SpeedFirstStore) generationKey() string { return speedFirstStateVersion + ":generation" }
func (s *SpeedFirstStore) allIndexKey() string   { return speedFirstStateVersion + ":all-index" }
func (s *SpeedFirstStore) probeIndexKey() string { return speedFirstStateVersion + ":probe-index" }

func (s *SpeedFirstStore) mutationLockKey(stateKey string) string {
	return speedFirstStateVersion + ":mutation-lock:" + stateKey
}

func (s *SpeedFirstStore) probeClaimLockKey(candidate opsjobs.ProbeCandidate) string {
	return speedFirstStateVersion + ":probe-claim:" + candidate.Generation + ":" + candidate.StateKey
}

// ---- Redis 原语（shared/runtime-state-store.ts 的 Lua 契约）----

const speedFirstCompareSetScript = `
local current = redis.call('GET', KEYS[1])
if ARGV[1] == '' then
  if current then
    return 0
  end
elseif current ~= ARGV[1] then
  return 0
end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
return 1
`

const speedFirstCompareDeleteScript = `
local current = redis.call('GET', KEYS[1])
if not current or current ~= ARGV[1] then
  return 0
end
return redis.call('DEL', KEYS[1])
`

const speedFirstReleaseLockScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

const speedFirstRenewLockScript = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
  return 0
end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`

func (s *SpeedFirstStore) compareSetJSON(ctx context.Context, key string, expected, next any, ttl time.Duration) (bool, error) {
	expectedText := ""
	if expected != nil {
		encoded, err := json.Marshal(expected)
		if err != nil {
			return false, err
		}
		expectedText = string(encoded)
	}
	nextText, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	result, err := s.client.Eval(ctx, speedFirstCompareSetScript, []string{s.redisKey(key)},
		expectedText, string(nextText), ttl.Milliseconds()).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (s *SpeedFirstStore) compareDeleteJSON(ctx context.Context, key string, expected any) (bool, error) {
	encoded, err := json.Marshal(expected)
	if err != nil {
		return false, err
	}
	result, err := s.client.Eval(ctx, speedFirstCompareDeleteScript, []string{s.redisKey(key)}, string(encoded)).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (s *SpeedFirstStore) getJSON(ctx context.Context, key string, target any) (bool, error) {
	raw, err := s.client.Get(ctx, s.redisKey(key)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		// Node：损坏数据删除。
		_ = s.client.Del(ctx, s.redisKey(key)).Err()
		return false, nil
	}
	return true, nil
}

func (s *SpeedFirstStore) setJSON(ctx context.Context, key string, value any, ttl time.Duration) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, s.redisKey(key), string(encoded), ttl).Err()
}

func (s *SpeedFirstStore) acquireLock(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	return s.client.SetNX(ctx, s.redisKey(key), token, ttl).Result()
}

func (s *SpeedFirstStore) renewLock(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	result, err := s.client.Eval(ctx, speedFirstRenewLockScript, []string{s.redisKey(key)}, token, ttl.Milliseconds()).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (s *SpeedFirstStore) releaseLock(ctx context.Context, key, token string) error {
	return s.client.Eval(ctx, speedFirstReleaseLockScript, []string{s.redisKey(key)}, token).Err()
}

// ---- generation 事件（{version, publishedAt}，token = JSON[ms, version]）----

type speedFirstGenerationEvent struct {
	Version     string `json:"version"`
	PublishedAt string `json:"publishedAt"`
}

func normalizeGenerationEvent(event speedFirstGenerationEvent) (speedFirstGenerationEvent, error) {
	version := strings.TrimSpace(event.Version)
	if version == "" {
		return speedFirstGenerationEvent{}, errors.New("普通路由速度优先 generation event 缺少 version")
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(event.PublishedAt))
	if err != nil {
		return speedFirstGenerationEvent{}, errors.New("普通路由速度优先 generation event publishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return speedFirstGenerationEvent{Version: version, PublishedAt: parsed.UTC().Format(rfc3339Milli)}, nil
}

func generationToken(event speedFirstGenerationEvent) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(event.PublishedAt))
	if err != nil {
		return "", errors.New("普通路由速度优先 generation event publishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	token, err := json.Marshal([]any{parsed.UnixMilli(), event.Version})
	if err != nil {
		return "", err
	}
	return string(token), nil
}

var speedFirstInitialGenerationEvent = speedFirstGenerationEvent{Version: "initial", PublishedAt: "1970-01-01T00:00:00.000Z"}

// LoadGeneration 等价 loadLatencyStateGeneration（读取或初始化 generation marker）。
func (s *SpeedFirstStore) LoadGeneration(ctx context.Context) (string, error) {
	for attempt := 0; attempt < speedFirstGenerationCASRetries; attempt++ {
		current, err := s.loadGenerationEvent(ctx)
		if err != nil {
			return "", err
		}
		if current != nil {
			return generationToken(*current)
		}
		created, err := s.compareSetJSON(ctx, s.generationKey(), nil, speedFirstInitialGenerationEvent, speedFirstGenerationTTL)
		if err != nil {
			return "", err
		}
		if created {
			return generationToken(speedFirstInitialGenerationEvent)
		}
	}
	return "", errors.New("普通路由速度优先 generation marker CAS 初始化重试耗尽（8 次）")
}

func (s *SpeedFirstStore) loadGenerationEvent(ctx context.Context) (*speedFirstGenerationEvent, error) {
	for attempt := 0; attempt < speedFirstGenerationCASRetries; attempt++ {
		var event speedFirstGenerationEvent
		found, err := s.getJSON(ctx, s.generationKey(), &event)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, nil
		}
		normalized, normalizeErr := normalizeGenerationEvent(event)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		// JSON.stringify 相等性：序列化形状一致即可（Go struct 序列化即 canonical）。
		encodedOriginal, _ := json.Marshal(event)
		encodedNormalized, _ := json.Marshal(normalized)
		if string(encodedOriginal) == string(encodedNormalized) {
			return &normalized, nil
		}
		updated, err := s.compareSetJSON(ctx, s.generationKey(), event, normalized, speedFirstGenerationTTL)
		if err != nil {
			return nil, err
		}
		if updated {
			return &normalized, nil
		}
	}
	return nil, errors.New("普通路由速度优先 generation marker canonical CAS 重试耗尽（8 次）")
}

func (s *SpeedFirstStore) renewGeneration(ctx context.Context, generation string) (bool, error) {
	current, err := s.loadGenerationEvent(ctx)
	if err != nil {
		return false, err
	}
	if current == nil {
		return false, nil
	}
	token, err := generationToken(*current)
	if err != nil {
		return false, err
	}
	if token != generation {
		return false, nil
	}
	return s.compareSetJSON(ctx, s.generationKey(), current, current, speedFirstGenerationTTL)
}

// ---- state JSON（与 Node NormalRouteLatencyState 形状一致）----

type speedFirstScope struct {
	SystemAccountID string `json:"systemAccountId"`
	RouteStrategyID string `json:"routeStrategyId"`
	GroupID         string `json:"groupId"`
}

type speedFirstConfig struct {
	SlowTriggerCount          int   `json:"slowTriggerCount"`
	SlowWindowSeconds         int   `json:"slowWindowSeconds"`
	RecoverySuccessCount      int   `json:"recoverySuccessCount"`
	ProbeIntervalSeconds      int   `json:"probeIntervalSeconds"`
	DegradedTTLSeconds        int   `json:"degradedTtlSeconds"`
	MaxFirstByteRetriesPerReq int   `json:"maxFirstByteRetriesPerRequest"`
	FirstByteDeadlineMS       int64 `json:"firstByteDeadlineMs"`
}

type speedFirstState struct {
	Generation                     string           `json:"generation"`
	AccountID                      string           `json:"accountId"`
	AccountName                    string           `json:"accountName,omitempty"`
	RuntimeKey                     string           `json:"runtimeKey"`
	Scope                          speedFirstScope  `json:"scope"`
	Config                         speedFirstConfig `json:"config"`
	FirstSlowAtMS                  int64            `json:"firstSlowAtMs"`
	LastSlowAtMS                   int64            `json:"lastSlowAtMs"`
	SlowCount                      int              `json:"slowCount"`
	DegradationEventID             string           `json:"degradationEventId,omitempty"`
	DegradedUntilMS                *int64           `json:"degradedUntilMs,omitempty"`
	SuccessCount                   int              `json:"successCount"`
	RecoveryProbeRoundAttemptCount *int             `json:"recoveryProbeRoundAttemptCount,omitempty"`
	RecoveryProbeRoundSuccessCount *int             `json:"recoveryProbeRoundSuccessCount,omitempty"`
	NextProbeAtMS                  *int64           `json:"nextProbeAtMs,omitempty"`
	Reason                         string           `json:"reason"`
}

func (s *speedFirstState) roundAttempts() int {
	if s.RecoveryProbeRoundAttemptCount == nil {
		return 0
	}
	return *s.RecoveryProbeRoundAttemptCount
}

func (s *speedFirstState) roundSuccesses() int {
	if s.RecoveryProbeRoundSuccessCount == nil {
		return 0
	}
	return *s.RecoveryProbeRoundSuccessCount
}

func (s *speedFirstState) candidate() opsjobs.ProbeCandidate {
	candidate := opsjobs.ProbeCandidate{
		StateKey:    stateKeyFor(s.Scope, s.RuntimeKey),
		AccountID:   s.AccountID,
		AccountName: s.AccountName,
		RuntimeKey:  s.RuntimeKey,
		Scope: opsjobs.ProbeScope{
			RouteStrategyID: s.Scope.RouteStrategyID,
			GroupID:         s.Scope.GroupID,
			SystemAccountID: s.Scope.SystemAccountID,
		},
		Generation:           s.Generation,
		DegradationEventID:   s.DegradationEventID,
		RecoverySuccessCount: s.SuccessCount,
		RoundAttemptCount:    s.roundAttempts(),
		RoundSuccessCount:    s.roundSuccesses(),
		Config: opsjobs.ProbeConfig{
			FirstByteDeadlineMS:  s.Config.FirstByteDeadlineMS,
			RecoverySuccessCount: s.Config.RecoverySuccessCount,
		},
	}
	if s.DegradedUntilMS != nil {
		candidate.DegradedUntil = formatMillis(*s.DegradedUntilMS)
	}
	if s.NextProbeAtMS != nil {
		candidate.NextProbeAt = formatMillis(*s.NextProbeAtMS)
	}
	return candidate
}

// stateKeyFor 等价 accountLatencyStateKey。
func sanitizeKeyPart(value string) string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "_"
	}
	var builder strings.Builder
	for _, char := range normalized {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9',
			char == '_' || char == '-' || char == '.' || char == ':':
			builder.WriteRune(char)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

func (s *SpeedFirstStore) loadState(ctx context.Context, key, generation string) (*speedFirstState, error) {
	var state speedFirstState
	found, err := s.getJSON(ctx, key, &state)
	if err != nil || !found {
		return nil, err
	}
	if !state.valid() || state.Generation != generation {
		return nil, nil
	}
	return &state, nil
}

func (st *speedFirstState) valid() bool {
	if st.Generation == "" || st.AccountID == "" || st.RuntimeKey == "" {
		return false
	}
	if st.Scope.SystemAccountID == "" || st.Scope.RouteStrategyID == "" || st.Scope.GroupID == "" {
		return false
	}
	if st.Config.SlowTriggerCount < 1 || st.Config.SlowWindowSeconds < 1 || st.Config.RecoverySuccessCount < 1 ||
		st.Config.ProbeIntervalSeconds < 1 || st.Config.DegradedTTLSeconds < 1 || st.Config.MaxFirstByteRetriesPerReq < 0 {
		return false
	}
	return true
}

// candidateMatchesState 等价 latencyProbeCandidateMatchesState。
func candidateMatchesState(candidate opsjobs.ProbeCandidate, state *speedFirstState) bool {
	degradedUntil := ""
	if state.DegradedUntilMS != nil {
		degradedUntil = formatMillis(*state.DegradedUntilMS)
	}
	nextProbeAt := ""
	if state.NextProbeAtMS != nil {
		nextProbeAt = formatMillis(*state.NextProbeAtMS)
	}
	return candidate.AccountID == state.AccountID &&
		candidate.RuntimeKey == state.RuntimeKey &&
		candidate.Scope.SystemAccountID == state.Scope.SystemAccountID &&
		candidate.Scope.RouteStrategyID == state.Scope.RouteStrategyID &&
		candidate.Scope.GroupID == state.Scope.GroupID &&
		candidate.DegradationEventID == state.DegradationEventID &&
		candidate.RoundAttemptCount == state.roundAttempts() &&
		candidate.RoundSuccessCount == state.roundSuccesses() &&
		candidate.NextProbeAt == nextProbeAt &&
		candidate.DegradedUntil == degradedUntil
}

// writeStateAndIndexes 等价 writeLatencyStateAndIndexesStrictAsync（简化回滚：
// Node 的索引回滚分支在 jobs 只写探针侧状态时保持一致语义——失败即报错）。
func (s *SpeedFirstStore) writeStateAndIndexes(ctx context.Context, key string, state speedFirstState, ttl time.Duration, addProbeIndex bool) error {
	if err := s.setJSON(ctx, key, state, ttl); err != nil {
		return err
	}
	if err := s.addIndexKey(ctx, s.allIndexKey(), key); err != nil {
		return err
	}
	if addProbeIndex {
		return s.addIndexKey(ctx, s.probeIndexKey(), key)
	}
	return nil
}

func (s *SpeedFirstStore) addIndexKey(ctx context.Context, indexKey, key string) error {
	token := randomToken(16)
	locked, err := s.acquireIndexLock(ctx, indexKey, token)
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("普通路由速度优先索引锁获取失败：%s", indexKey)
	}
	defer func() { _ = s.releaseLock(ctx, indexKey, token) }()
	for attempt := 0; attempt < speedFirstGenerationCASRetries; attempt++ {
		var currentIndex struct {
			Keys []string `json:"keys"`
		}
		found, err := s.getJSON(ctx, indexKey, &currentIndex)
		if err != nil {
			return err
		}
		if !found {
			currentIndex.Keys = nil
		}
		exists := false
		for _, existing := range currentIndex.Keys {
			if existing == key {
				exists = true
				break
			}
		}
		next := currentIndex.Keys
		if !exists {
			next = append(next, key)
			if len(next) > speedFirstIndexMaxKeys {
				next = next[len(next)-speedFirstIndexMaxKeys:]
			}
		}
		nextValue := map[string]any{"keys": next}
		var expected any
		if found {
			expected = currentIndex
		}
		updated, err := s.compareSetJSON(ctx, indexKey, expected, nextValue, speedFirstIndexTTL)
		if err != nil {
			return err
		}
		if updated {
			return nil
		}
	}
	return fmt.Errorf("普通路由速度优先索引 CAS 重试耗尽（8 次）：%s", indexKey)
}

func (s *SpeedFirstStore) removeIndexKeys(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	removeSet := map[string]bool{}
	for _, key := range keys {
		removeSet[key] = true
	}
	if err := s.filterIndexKeys(ctx, s.probeIndexKey(), removeSet); err != nil {
		return err
	}
	return s.filterIndexKeys(ctx, s.allIndexKey(), removeSet)
}

func (s *SpeedFirstStore) filterIndexKeys(ctx context.Context, indexKey string, removeSet map[string]bool) error {
	token := randomToken(16)
	locked, err := s.acquireIndexLock(ctx, indexKey, token)
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("普通路由速度优先索引锁获取失败：%s", indexKey)
	}
	defer func() { _ = s.releaseLock(ctx, indexKey, token) }()
	for attempt := 0; attempt < speedFirstGenerationCASRetries; attempt++ {
		var currentIndex struct {
			Keys []string `json:"keys"`
		}
		found, err := s.getJSON(ctx, indexKey, &currentIndex)
		if err != nil {
			return err
		}
		filtered := make([]string, 0, len(currentIndex.Keys))
		for _, existing := range currentIndex.Keys {
			if !removeSet[existing] {
				filtered = append(filtered, existing)
			}
		}
		nextValue := map[string]any{"keys": filtered}
		var expected any
		if found {
			expected = currentIndex
		}
		updated, err := s.compareSetJSON(ctx, indexKey, expected, nextValue, speedFirstIndexTTL)
		if err != nil {
			return err
		}
		if updated {
			return nil
		}
	}
	return fmt.Errorf("普通路由速度优先索引 CAS 重试耗尽（8 次）：%s", indexKey)
}

func (s *SpeedFirstStore) acquireIndexLock(ctx context.Context, lockKey, token string) (bool, error) {
	for attempt := 0; attempt < speedFirstLockMaxAttempts; attempt++ {
		acquired, err := s.acquireLock(ctx, lockKey, token, speedFirstMutationLockTTL)
		if err != nil {
			return false, err
		}
		if acquired {
			return true, nil
		}
		delay := 20 + attempt*5
		if delay > 100 {
			delay = 100
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(time.Duration(delay) * time.Millisecond):
		}
	}
	return false, nil
}

// withMutationLock 等价 withLatencyStateMutationLock：锁内先续 generation，
// 续租失败返回 false（调用方按 Node undefined 分支处理）。
func (s *SpeedFirstStore) withMutationLock(ctx context.Context, stateKey, generation string, operation func() error) (bool, error) {
	token := randomToken(16)
	lockKey := s.mutationLockKey(stateKey)
	var locked bool
	for attempt := 0; attempt < speedFirstLockMaxAttempts; attempt++ {
		acquired, err := s.acquireLock(ctx, lockKey, token, speedFirstMutationLockTTL)
		if err != nil {
			return false, err
		}
		if acquired {
			locked = true
			break
		}
		delay := 20 + attempt*5
		if delay > 100 {
			delay = 100
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(time.Duration(delay) * time.Millisecond):
		}
	}
	if !locked {
		return false, nil
	}
	defer func() { _ = s.releaseLock(ctx, lockKey, token) }()
	renewed, err := s.renewGeneration(ctx, generation)
	if err != nil {
		return false, err
	}
	if !renewed {
		return false, nil
	}
	if err := operation(); err != nil {
		return false, err
	}
	return true, nil
}

// ---- opsjobs.SpeedFirstClaimStore 实现 ----

// AcquireClaim 等价 acquireNormalRouteLatencyProbeClaimAsync。
func (s *SpeedFirstStore) AcquireClaim(ctx context.Context, candidate opsjobs.ProbeCandidate) (*opsjobs.ProbeClaim, error) {
	token := randomToken(16)
	acquired, err := s.acquireLock(ctx, s.probeClaimLockKey(candidate), token, speedFirstClaimTTL)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, nil
	}
	return &opsjobs.ProbeClaim{Token: token, Candidate: candidate}, nil
}

// RenewClaim 等价 renewNormalRouteLatencyProbeClaimAsync。
func (s *SpeedFirstStore) RenewClaim(ctx context.Context, claim opsjobs.ProbeClaim) (bool, error) {
	return s.renewLock(ctx, s.probeClaimLockKey(claim.Candidate), claim.Token, speedFirstClaimTTL)
}

// ReleaseClaim 等价 releaseNormalRouteLatencyProbeClaimAsync。
func (s *SpeedFirstStore) ReleaseClaim(ctx context.Context, claim opsjobs.ProbeClaim) error {
	return s.releaseLock(ctx, s.probeClaimLockKey(claim.Candidate), claim.Token)
}

// Discard 等价 discardNormalRouteLatencyProbeCandidateAsync。
func (s *SpeedFirstStore) Discard(ctx context.Context, candidate opsjobs.ProbeCandidate) error {
	generation := candidate.Generation
	if generation == "" {
		loaded, err := s.LoadGeneration(ctx)
		if err != nil {
			return err
		}
		generation = loaded
	}
	_, err := s.withMutationLock(ctx, candidate.StateKey, generation, func() error {
		current, err := s.loadState(ctx, candidate.StateKey, generation)
		if err != nil || current == nil {
			return err
		}
		if !candidateMatchesState(candidate, current) {
			return nil
		}
		return s.deleteStateAndIndexes(ctx, candidate.StateKey)
	})
	return err
}

// Defer 等价 deferNormalRouteLatencyProbeCandidateAsync（保留降级状态并顺延）。
func (s *SpeedFirstStore) Defer(ctx context.Context, candidate opsjobs.ProbeCandidate) (bool, error) {
	generation := candidate.Generation
	if generation == "" {
		loaded, err := s.LoadGeneration(ctx)
		if err != nil {
			return false, err
		}
		generation = loaded
	}
	applied := false
	_, err := s.withMutationLock(ctx, candidate.StateKey, generation, func() error {
		current, err := s.loadState(ctx, candidate.StateKey, generation)
		if err != nil || current == nil {
			return err
		}
		applied = false
		if !candidateMatchesState(candidate, current) {
			return nil
		}
		now := s.now().UnixMilli()
		if current.DegradedUntilMS == nil || *current.DegradedUntilMS <= now {
			if err := s.deleteStateAndIndexes(ctx, candidate.StateKey); err != nil {
				return err
			}
			return nil
		}
		next := *current
		next.RecoveryProbeRoundAttemptCount = intPtr(0)
		next.RecoveryProbeRoundSuccessCount = intPtr(0)
		nextProbeAt := now + passiveOffsetApply(speedFirstRecoveryIntervalMS, s.random)
		next.NextProbeAtMS = &nextProbeAt
		ttl := time.Duration(max64(1, *next.DegradedUntilMS-now)) * time.Millisecond
		if err := s.writeStateAndIndexes(ctx, candidate.StateKey, next, ttl, true); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

// RecordSuccess 等价 recordNormalRouteRecoveryProbeSuccessAsync（双探针窗口）。
func (s *SpeedFirstStore) RecordSuccess(ctx context.Context, candidate opsjobs.ProbeCandidate, candidateAccountRef opsjobs.ProbeAccountRef, firstByteMS *int64) (opsjobs.SpeedFirstRecoveryResult, error) {
	generation := candidate.Generation
	if generation == "" {
		loaded, err := s.LoadGeneration(ctx)
		if err != nil {
			return opsjobs.SpeedFirstRecoveryResult{}, err
		}
		generation = loaded
	}
	result := opsjobs.SpeedFirstRecoveryResult{}
	_, err := s.withMutationLock(ctx, candidate.StateKey, generation, func() error {
		current, err := s.loadState(ctx, candidate.StateKey, generation)
		if err != nil || current == nil {
			return err
		}
		if !candidateMatchesState(candidate, current) {
			return nil
		}
		if candidate.AccountID != candidateAccountRef.AccountID {
			return nil
		}
		now := s.now().UnixMilli()
		if current.DegradedUntilMS == nil || *current.DegradedUntilMS <= now {
			if err := s.deleteStateAndIndexes(ctx, candidate.StateKey); err != nil {
				return err
			}
			result = opsjobs.SpeedFirstRecoveryResult{Cleared: true, RecoverySuccessCount: 0, RequiredRecoverySuccessCount: speedFirstRecoveryRoundSize}
			return nil
		}
		roundAttempts := current.roundAttempts() + 1
		roundSuccesses := current.roundSuccesses() + 1
		if roundAttempts >= speedFirstRecoveryRoundSize && roundSuccesses == speedFirstRecoveryRoundSize {
			if err := s.deleteStateAndIndexes(ctx, candidate.StateKey); err != nil {
				return err
			}
			result = opsjobs.SpeedFirstRecoveryResult{Cleared: true, RecoverySuccessCount: roundSuccesses, RequiredRecoverySuccessCount: speedFirstRecoveryRoundSize}
			return nil
		}
		next := *current
		if roundAttempts >= speedFirstRecoveryRoundSize {
			next.RecoveryProbeRoundAttemptCount = intPtr(0)
			next.RecoveryProbeRoundSuccessCount = intPtr(0)
		} else {
			next.RecoveryProbeRoundAttemptCount = intPtr(roundAttempts)
			next.RecoveryProbeRoundSuccessCount = intPtr(roundSuccesses)
		}
		nextProbeAt := now + passiveOffsetApply(speedFirstRecoveryIntervalMS, s.random)
		next.NextProbeAtMS = &nextProbeAt
		ttl := time.Duration(max64(1, *current.DegradedUntilMS-now)) * time.Millisecond
		if err := s.writeStateAndIndexes(ctx, candidate.StateKey, next, ttl, true); err != nil {
			return err
		}
		result = opsjobs.SpeedFirstRecoveryResult{Cleared: false, RecoverySuccessCount: next.roundSuccesses(), RequiredRecoverySuccessCount: speedFirstRecoveryRoundSize}
		return nil
	})
	if err != nil {
		return opsjobs.SpeedFirstRecoveryResult{}, err
	}
	return result, nil
}

// RecordFailure 等价 recordNormalRouteProbeFailureAsync（仅 FF 双失败续租）。
func (s *SpeedFirstStore) RecordFailure(ctx context.Context, candidate opsjobs.ProbeCandidate, reason string) error {
	generation := candidate.Generation
	if generation == "" {
		loaded, err := s.LoadGeneration(ctx)
		if err != nil {
			return err
		}
		generation = loaded
	}
	_, err := s.withMutationLock(ctx, candidate.StateKey, generation, func() error {
		current, err := s.loadState(ctx, candidate.StateKey, generation)
		if err != nil || current == nil {
			return err
		}
		if !candidateMatchesState(candidate, current) {
			return nil
		}
		now := s.now().UnixMilli()
		if current.DegradedUntilMS == nil || *current.DegradedUntilMS <= now {
			return s.deleteStateAndIndexes(ctx, candidate.StateKey)
		}
		roundAttempts := current.roundAttempts() + 1
		roundSuccesses := current.roundSuccesses()
		roundComplete := roundAttempts >= speedFirstRecoveryRoundSize
		degradedUntilMS := *current.DegradedUntilMS
		if roundComplete && roundSuccesses == 0 {
			renewed := now + int64(maxInt(60, current.Config.DegradedTTLSeconds))*1000
			if renewed > degradedUntilMS {
				degradedUntilMS = renewed
			}
		}
		nextProbeAt := now + passiveOffsetApply(speedFirstRecoveryIntervalMS, s.random)
		next := *current
		next.LastSlowAtMS = now
		next.SlowCount = maxInt(current.SlowCount, current.Config.SlowTriggerCount)
		next.DegradedUntilMS = &degradedUntilMS
		if roundComplete {
			next.RecoveryProbeRoundAttemptCount = intPtr(0)
			next.RecoveryProbeRoundSuccessCount = intPtr(0)
		} else {
			next.RecoveryProbeRoundAttemptCount = intPtr(roundAttempts)
			next.RecoveryProbeRoundSuccessCount = intPtr(roundSuccesses)
		}
		next.NextProbeAtMS = &nextProbeAt
		next.Reason = reason
		ttl := time.Duration(max64(1, degradedUntilMS-now)) * time.Millisecond
		return s.writeStateAndIndexes(ctx, candidate.StateKey, next, ttl, true)
	})
	return err
}

func (s *SpeedFirstStore) deleteStateAndIndexes(ctx context.Context, key string) error {
	if err := s.client.Del(ctx, s.redisKey(key)).Err(); err != nil {
		return err
	}
	return s.removeIndexKeys(ctx, []string{key})
}

// ---- 候选扫描（listNormalRouteLatencyProbeCandidatesAsync）----

// ListProbeCandidates 扫描 probe-index 中到期且仍降级的候选。
func (s *SpeedFirstStore) ListProbeCandidates(ctx context.Context, limit int) ([]opsjobs.ProbeCandidate, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	keys, err := s.loadIndexKeys(ctx, s.probeIndexKey())
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, nil
	}
	generation, err := s.LoadGeneration(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now().UnixMilli()
	type scored struct {
		candidate     opsjobs.ProbeCandidate
		nextProbeAtMS int64
	}
	var candidates []scored
	for _, key := range keys {
		state, err := s.loadState(ctx, key, generation)
		if err != nil {
			return nil, err
		}
		if state == nil {
			continue
		}
		if state.DegradedUntilMS == nil || *state.DegradedUntilMS <= now {
			continue
		}
		if state.NextProbeAtMS == nil || *state.NextProbeAtMS > now {
			continue
		}
		candidate := state.candidate()
		candidate.StateKey = key
		candidates = append(candidates, scored{candidate: candidate, nextProbeAtMS: *state.NextProbeAtMS})
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].nextProbeAtMS != candidates[right].nextProbeAtMS {
			return candidates[left].nextProbeAtMS < candidates[right].nextProbeAtMS
		}
		return candidates[left].candidate.AccountID < candidates[right].candidate.AccountID
	})
	output := make([]opsjobs.ProbeCandidate, 0, limit)
	for index, item := range candidates {
		if index >= limit {
			break
		}
		output = append(output, item.candidate)
	}
	return output, nil
}

func (s *SpeedFirstStore) loadIndexKeys(ctx context.Context, indexKey string) ([]string, error) {
	var index struct {
		Keys []string `json:"keys"`
	}
	found, err := s.getJSON(ctx, indexKey, &index)
	if err != nil || !found {
		return nil, err
	}
	return index.Keys, nil
}

func (s *SpeedFirstStore) indexLockKey(indexKey string) string { return indexKey + "-lock" }

// ---- 小工具 ----

func intPtr(value int) *int { return &value }

func max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// passiveOffsetApply 对齐 nextRecoveryProbeDelayMs：
// passiveScheduleDelayMs(5000)（对称抖动）。
func passiveOffsetApply(intervalMS int64, random func() float64) int64 {
	windowMS := passiveJitterWindowMS(intervalMS)
	offset := int64(0)
	if windowMS > 0 {
		unit := random()
		if unit < 0 {
			unit = 0
		}
		if unit > 1 {
			unit = 1
		}
		sampled := int64(unit*float64(windowMS*2+1)) - windowMS
		if sampled > windowMS {
			sampled = windowMS
		}
		offset = sampled
		if offset == 0 {
			offset = 1
		}
	}
	delay := intervalMS + offset
	if delay < 1 {
		delay = 1
	}
	return delay
}
