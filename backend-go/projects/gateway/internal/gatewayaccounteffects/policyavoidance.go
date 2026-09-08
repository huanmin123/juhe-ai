package gatewayaccounteffects

// 配置策略账号避让（D-132，BUG-0175）：runtime/account-side-effects.service.ts
// 的 configuredPolicyAvoidanceStore（createRuntimeStateStore
// 'gateway-configured-account-policy-avoidance'）写读面移植。
//
// 写侧 suppressGatewayAccountLocallyForSeconds 是响应检查策略
// avoid_account_ttl / runtime_avoidance 决策的跨请求 TTL 落地：键空间
// `gateway-configured-account-policy-avoidance` 此前只有 jobs 的只读消费者
// （listavailability_runtime.go），网关侧无写入者，避让收敛为请求内排除。
// 读侧 loadConfiguredPolicyAvoidanceStates + filterConfiguredPolicyAvoidances
// 供派发候选过滤（chain_dispatch.go 的 SuppressionPort 装饰器）消费。
//
// 顺序契约（Node 注释）：列表投影脏标记必须先于 Redis 写入——投影未追上前
// 不得把过期/新写的运行态键当作健康账号呈现。

import (
	"context"
	"encoding/json"
	"sync"
)

// 配置键空间名与进程内缓存常量（runtime.ts:778-780 默认值）。
const (
	ConfiguredPolicyAvoidanceStoreName = "gateway-configured-account-policy-avoidance"

	configuredPolicyAvoidanceCacheTtlMs         = int64(1_000)
	configuredPolicyAvoidanceNegativeCacheTtlMs = int64(500)
	configuredPolicyAvoidanceCacheMaxEntries    = 5_000
	// automaticProbeDueRetryDelayMs（JUHE_AI_GATEWAY_AUTOMATIC_PROBE_DUE_RETRY_DELAY_MS
	// 默认 250）：避让已过期但投影未追上时的最短重试延迟。
	distributedRecoveryProbeDueRetryDelayMs = int64(250)
)

// ConfiguredPolicyAvoidanceState mirrors ConfiguredPolicyAvoidanceState；JSON
// tag 是与 Node / jobs 共享的 Redis wire contract。
type ConfiguredPolicyAvoidanceState struct {
	RuntimeKey  string `json:"runtimeKey"`
	AccountID   string `json:"accountId"`
	Reason      string `json:"reason"`
	StartedAtMs int64  `json:"startedAtMs"`
	UntilMs     int64  `json:"untilMs"`
}

// PolicyAvoidanceStateStore 是 createRuntimeStateStore 的最小读出面。生产装
// 配为 proxyhealth/quota 同族的 Redis runtime-state store（或 memory 驱动的
// 进程内 store）；测试用 map 实现（可 Mock、可回放）。
type PolicyAvoidanceStateStore interface {
	GetJSON(ctx context.Context, key string) (json.RawMessage, error)
	SetJSON(ctx context.Context, key string, value any, ttlMs int64) error
}

// ListAvailabilityDirtyMarker 镜像 markAccountListRuntimeProjectionDirty 的
// 落库面（account_list_availability_dirty 家族入队）；nil = 投影维护关闭
// （Node databaseDriver !== 'postgres' / 投影开关关闭分支）。
type ListAvailabilityDirtyMarker func(ctx context.Context, sourceAccountID string, reason string, availableAtMs int64) error

// ConfiguredPolicyAvoidanceService carries the avoidance collaborators.
type ConfiguredPolicyAvoidanceService struct {
	store      PolicyAvoidanceStateStore
	dirty      ListAvailabilityDirtyMarker
	invalidate func() // clearGatewayRuntimeCache
	clock      Clock

	mu    sync.Mutex
	cache map[string]configuredPolicyAvoidanceCacheEntry
}

type configuredPolicyAvoidanceCacheEntry struct {
	state       *ConfiguredPolicyAvoidanceState
	expiresAtMs int64
}

// NewConfiguredPolicyAvoidanceService builds the service; invalidate mirrors
// clearGatewayRuntimeCache. A nil store degrades both faces to the in-process
// cache only (Node memory-driver semantics without the persistence half).
func NewConfiguredPolicyAvoidanceService(store PolicyAvoidanceStateStore, dirty ListAvailabilityDirtyMarker, invalidate func(), clock Clock) *ConfiguredPolicyAvoidanceService {
	if clock == nil {
		clock = SystemClock{}
	}
	return &ConfiguredPolicyAvoidanceService{
		store:      store,
		dirty:      dirty,
		invalidate: invalidate,
		clock:      clock,
		cache:      map[string]configuredPolicyAvoidanceCacheEntry{},
	}
}

// SuppressGatewayAccountLocallyForSeconds mirrors
// suppressGatewayAccountLocallyForSeconds：TTL 秒数截断到 >=1（undefined/NaN
// 回落 60 秒），先脏标记列表投影，再写 runtime-state（带 TTL），最后进程内
// 缓存 + 运行态缓存失效。错误向上传播（Node await 语义），由响应层调用方
// 决定是否吞掉。
func (s *ConfiguredPolicyAvoidanceService) SuppressGatewayAccountLocallyForSeconds(
	ctx context.Context,
	account SuppressibleGatewayAccount,
	seconds *int64,
	reason string,
) error {
	value := normalizePolicyAvoidanceSeconds(seconds)
	ttlMs := value * 1000
	runtimeKey, err := GatewayAccountRuntimeKey(account)
	if err != nil {
		return err
	}
	now := NowMs(s.clock)
	state := ConfiguredPolicyAvoidanceState{
		RuntimeKey:  runtimeKey,
		AccountID:   GatewayAccountID(account),
		Reason:      reason,
		StartedAtMs: now,
		UntilMs:     now + ttlMs,
	}
	// Node markAccountListRuntimeProjectionDirty(runtimeKey)：先脏标记再写
	// Redis（脏标记失败会让本次避让不可见 → 与 Node 一样向上传播）。
	if s.dirty != nil {
		if err := s.dirty(ctx, RuntimeAccountIDFromKey(runtimeKey), "runtime_availability_changed", now); err != nil {
			return err
		}
	}
	if s.store != nil {
		if err := s.store.SetJSON(ctx, runtimeKey, state, ttlMs); err != nil {
			return err
		}
	}
	s.rememberConfiguredPolicyAvoidanceState(runtimeKey, &state, now)
	if s.invalidate != nil {
		s.invalidate()
	}
	return nil
}

// normalizePolicyAvoidanceSeconds 镜像 typeof seconds === 'number' &&
// Number.isFinite ? max(1, trunc(seconds)) : 60。nil 即 Node undefined（回落
// 60 秒）；已定义的数字一律夹到 >= 1（0/负数 → 1）。
func normalizePolicyAvoidanceSeconds(seconds *int64) int64 {
	if seconds == nil {
		return 60
	}
	if *seconds < 1 {
		return 1
	}
	return *seconds
}

// LoadConfiguredPolicyAvoidanceStates mirrors loadConfiguredPolicyAvoidanceStates:
// 进程内正/负缓存优先，miss 批量读 store（nil store 只剩缓存面）。
func (s *ConfiguredPolicyAvoidanceService) LoadConfiguredPolicyAvoidanceStates(ctx context.Context, runtimeKeys []string) ([]*ConfiguredPolicyAvoidanceState, error) {
	now := NowMs(s.clock)
	states := make([]*ConfiguredPolicyAvoidanceState, len(runtimeKeys))
	var missedIndexes []int
	var missedRuntimeKeys []string
	for index, runtimeKey := range runtimeKeys {
		if state, hit := s.cachedConfiguredPolicyAvoidanceState(runtimeKey, now); hit {
			states[index] = state
			continue
		}
		missedIndexes = append(missedIndexes, index)
		missedRuntimeKeys = append(missedRuntimeKeys, runtimeKey)
	}
	if len(missedRuntimeKeys) == 0 || s.store == nil {
		return states, nil
	}
	for missIndex, runtimeKey := range missedRuntimeKeys {
		raw, err := s.store.GetJSON(ctx, runtimeKey)
		if err != nil {
			return nil, err
		}
		var state *ConfiguredPolicyAvoidanceState
		if len(raw) > 0 {
			decoded := decodeConfiguredPolicyAvoidanceState(raw)
			if decoded != nil {
				state = decoded
			}
		}
		states[missedIndexes[missIndex]] = state
		s.rememberConfiguredPolicyAvoidanceState(runtimeKey, state, now)
	}
	return states, nil
}

// decodeConfiguredPolicyAvoidanceState mirrors the Node getJsonMany 泛型解析
// 的形状校验面：runtimeKey/accountId 必填、until/started 必须为正整数。
func decodeConfiguredPolicyAvoidanceState(raw json.RawMessage) *ConfiguredPolicyAvoidanceState {
	var state ConfiguredPolicyAvoidanceState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil
	}
	if state.RuntimeKey == "" || state.AccountID == "" ||
		state.StartedAtMs < 0 || state.UntilMs <= 0 || state.UntilMs < state.StartedAtMs {
		return nil
	}
	return &state
}

func (s *ConfiguredPolicyAvoidanceService) cachedConfiguredPolicyAvoidanceState(runtimeKey string, now int64) (*ConfiguredPolicyAvoidanceState, bool) {
	s.mu.Lock()
	entry, ok := s.cache[runtimeKey]
	if !ok {
		s.mu.Unlock()
		return nil, false
	}
	if entry.expiresAtMs <= now {
		delete(s.cache, runtimeKey)
		s.mu.Unlock()
		return nil, false
	}
	s.mu.Unlock()
	return entry.state, true
}

// rememberConfiguredPolicyAvoidanceState mirrors the positive/negative cache
// write with the untilMs clamp and the bounded-size eviction.
func (s *ConfiguredPolicyAvoidanceService) rememberConfiguredPolicyAvoidanceState(runtimeKey string, state *ConfiguredPolicyAvoidanceState, now int64) {
	cacheTtlMs := configuredPolicyAvoidanceNegativeCacheTtlMs
	if state != nil {
		cacheTtlMs = configuredPolicyAvoidanceCacheTtlMs
	}
	expiresAtMs := now + cacheTtlMs
	if state != nil && state.UntilMs < expiresAtMs {
		expiresAtMs = state.UntilMs
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[runtimeKey] = configuredPolicyAvoidanceCacheEntry{state: state, expiresAtMs: expiresAtMs}
	if len(s.cache) <= configuredPolicyAvoidanceCacheMaxEntries {
		return
	}
	for key, entry := range s.cache {
		if entry.expiresAtMs <= now || len(s.cache) > configuredPolicyAvoidanceCacheMaxEntries {
			delete(s.cache, key)
		}
		if len(s.cache) <= configuredPolicyAvoidanceCacheMaxEntries {
			break
		}
	}
}
