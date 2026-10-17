package gatewayrouting

import (
	"context"
	"encoding/base64"
	"sort"
	"sync"
)

const (
	// apiKeyGroupRouteStateMaxEntries mirrors
	// apiKeyGroupRouteStateMaxEntries: the in-process route state maps are
	// trimmed to this many keys (oldest insertion first).
	apiKeyGroupRouteStateMaxEntries = 10000
)

// routeStateRegistry owns the process-local round-robin / weighted route
// state (Node module-level roundRobinStates / weightedRouteStates Maps).
// Insertion-order trimming mirrors Map iteration order; re-setting an
// existing key keeps its original position, exactly like Map#set.
// roundRobinSeen / weightedSeen mirror the membership of the matching keys
// slice so appendKeyOnce is O(1) instead of a linear scan; both are only
// touched under mu and kept in sync by appendKeyOnce / trimLocked.
type routeStateRegistry struct {
	mu             sync.Mutex
	roundRobin     map[string]int
	roundRobinKeys []string
	roundRobinSeen map[string]struct{}
	weighted       map[string]map[string]int64
	weightedKeys   []string
	weightedSeen   map[string]struct{}
}

func newRouteStateRegistry() *routeStateRegistry {
	return &routeStateRegistry{
		roundRobin:     make(map[string]int),
		roundRobinSeen: make(map[string]struct{}),
		weighted:       make(map[string]map[string]int64),
		weightedSeen:   make(map[string]struct{}),
	}
}

func (r *routeStateRegistry) nextRoundRobinIndex(key string, bindingCount int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.roundRobin[key]
	index := state % bindingCount
	r.roundRobin[key] = (index + 1) % bindingCount
	r.appendKeyOnce(r.roundRobinSeen, &r.roundRobinKeys, key)
	r.trimLocked()
	return index
}

// peekRoundRobinIndex mirrors nextRoundRobinIndex without advancing the
// cursor: 组级会话亲和（策略路由分组会话亲和设计 §4）的黏住命中不得消费
// 轮转状态，这里只读当前 index。
func (r *routeStateRegistry) peekRoundRobinIndex(key string, bindingCount int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.roundRobin[key] % bindingCount
}

// peekWeightedOrder mirrors orderWeightedBindings 的选择判定但不落任何状态：
// 只在锁内读当前 debt 计算本请求的 smooth-weighted 选中项，不记账、不清
// 理、不写回。sticky 命中路径用它生成剩余序列（§4 关键点 3）；剩余绑定按
// 优先序稳定排列（同为同请求内回退序，确定性优先，不镜像 advance 路径的
// debt 排序以避免读后语义混淆）。
func (r *routeStateRegistry) peekWeightedOrder(key string, bindings []GroupBindingRow) []GroupBindingRow {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.weighted[key]
	weights := make([]int64, len(bindings))
	var totalWeight int64
	for i, binding := range bindings {
		weight, err := NormalizeAPIKeyGroupBindingWeight(binding.Weight)
		if err != nil {
			return bindings
		}
		weights[i] = weight
		totalWeight += weight
	}
	selected := bindings[0]
	selectedCurrentWeight := int64(0)
	selectedSet := false
	for i, binding := range bindings {
		current := state[binding.ID] + weights[i]
		if !selectedSet || current > selectedCurrentWeight ||
			(current == selectedCurrentWeight && compareBindingOrderByPriority(binding, selected) < 0) {
			selected = binding
			selectedCurrentWeight = current
			selectedSet = true
		}
	}
	if !selectedSet {
		return bindings
	}
	ordered := make([]GroupBindingRow, 0, len(bindings))
	for _, binding := range bindings {
		if binding.ID == selected.ID {
			continue
		}
		ordered = append(ordered, binding)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return compareBindingOrderByPriority(ordered[i], ordered[j]) < 0
	})
	return append([]GroupBindingRow{selected}, ordered...)
}

func (r *routeStateRegistry) weightedStateStore(key string, state map[string]int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.weighted[key] = state
	r.appendKeyOnce(r.weightedSeen, &r.weightedKeys, key)
	r.trimLocked()
}

func (r *routeStateRegistry) appendKeyOnce(seen map[string]struct{}, keys *[]string, key string) {
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	*keys = append(*keys, key)
}

func (r *routeStateRegistry) trimLocked() {
	for len(r.roundRobin) > apiKeyGroupRouteStateMaxEntries {
		oldest := r.roundRobinKeys[0]
		r.roundRobinKeys = r.roundRobinKeys[1:]
		delete(r.roundRobin, oldest)
		delete(r.roundRobinSeen, oldest)
	}
	for len(r.weighted) > apiKeyGroupRouteStateMaxEntries {
		oldest := r.weightedKeys[0]
		r.weightedKeys = r.weightedKeys[1:]
		delete(r.weighted, oldest)
		delete(r.weightedSeen, oldest)
	}
}

// redisRouteStateTtlMs and redisRouteStateOperationTimeoutMs mirror the Node
// constants; the counter contract lives on RedisRouteCounter.
const (
	RedisRouteStateTtlMs              = int64(30 * 24 * 60 * 60 * 1000)
	RedisRouteStateOperationTimeoutMs = 3000
	RedisRouteStateRoundRobinMode     = "round-robin"
	RedisRouteStateWeightedMode       = "weighted"
	// ErrHighPerformanceDynamicRouteRequiresStateURL mirrors the Node error
	// thrown when the redis driver is selected without JUHE_AI_REDIS_STATE_URL.
	ErrHighPerformanceDynamicRouteRequiresStateURL = "高性能模式动态路由需要 JUHE_AI_REDIS_STATE_URL"
	// ErrRedisRouteCounterInvalidResult mirrors the Node error for a
	// non-numeric counter result.
	ErrRedisRouteCounterInvalidResult = "Redis 动态路由计数器返回值无效"
	// ErrSyncRouteStateForbidden mirrors the Node error thrown when the sync
	// dispatch ordering is used for dynamic modes under the redis driver.
	ErrSyncRouteStateForbidden = "高性能模式动态路由禁止使用本机同步状态，请调用 orderGatewayApiKeyGroupBindingsForDispatchAsync"
)

// RedisRouteCounter is the port toward the Redis dynamic route counter (Node
// nextRedisRouteCounterIndex). modulo <= 0 short-circuits to 0 before the
// counter is contacted.
type RedisRouteCounter interface {
	NextRouteCounterIndex(ctx context.Context, key string, modulo int64) (int64, error)
}

// RedisRouteCounterPeeker 是组级会话黏性 peek 路径的可选端口扩展（策略路由
// 分组会话亲和设计 §4 关键点 3）：选择器在 Redis 驱动下通过类型断言获取纯
// 读能力，未实现的计数器保持既有行为。
type RedisRouteCounterPeeker interface {
	PeekRouteCounterIndex(ctx context.Context, key string, modulo int64) (int64, error)
}

// APIKeyGroupRouteSelector mirrors routing/api-key-group-route-selector
// .service.ts: dispatch-order resolution for an API key's group bindings
// under the normal/round-robin/weighted strategy modes.
type APIKeyGroupRouteSelector struct {
	// RuntimeStateDriver mirrors runtimeConfig.runtimeStateDriver:
	// anything other than "redis" keeps state process-locally.
	RuntimeStateDriver string
	// Redis drives the shared counter when RuntimeStateDriver is "redis".
	Redis RedisRouteCounter
	// RedisStateURL mirrors runtimeConfig.redis.stateUrl; an empty value with
	// the redis driver reproduces the Node missing-URL error.
	RedisStateURL string

	states *routeStateRegistry
}

// NewAPIKeyGroupRouteSelector builds a selector with fresh process-local
// route state (Node module-level state singleton).
func NewAPIKeyGroupRouteSelector(runtimeStateDriver string, redisCounter RedisRouteCounter, redisStateURL string) *APIKeyGroupRouteSelector {
	return &APIKeyGroupRouteSelector{
		RuntimeStateDriver: runtimeStateDriver,
		Redis:              redisCounter,
		RedisStateURL:      redisStateURL,
		states:             newRouteStateRegistry(),
	}
}

// runtimeStateDriverRedis mirrors the Node "redis" literal.
const runtimeStateDriverRedis = "redis"

// GroupStickyApplicable 报告组级会话黏性是否适用于该模式与绑定数
// （策略路由分组会话亲和设计 §6：round_robin / weighted / failover 且活跃
// 绑定 ≥ 2；normal / merge / 单绑定零额外路径）。
func GroupStickyApplicable(mode string, activeBindingCount int) bool {
	if activeBindingCount < 2 {
		return false
	}
	switch mode {
	case RouteStrategyModeRoundRobin, RouteStrategyModeWeighted, RouteStrategyModeFailover:
		return true
	}
	return false
}

// ReorderBindingsForStickyGroup 把黏住分组挪到派发序首位、其余保持既有顺序
// （纯函数，策略路由分组会话亲和设计 §4）；绑定组不在列表中时原样返回
//（视同未命中）。
func ReorderBindingsForStickyGroup(ordered []GroupBindingRow, stickyGroupID string) []GroupBindingRow {
	if stickyGroupID == "" || len(ordered) <= 1 {
		return ordered
	}
	stickyIndex := -1
	for i, binding := range ordered {
		if binding.GroupID == stickyGroupID {
			stickyIndex = i
			break
		}
	}
	if stickyIndex <= 0 {
		return ordered
	}
	result := make([]GroupBindingRow, 0, len(ordered))
	result = append(result, ordered[stickyIndex])
	result = append(result, ordered[:stickyIndex]...)
	result = append(result, ordered[stickyIndex+1:]...)
	return result
}

// PeekAPIKeyGroupBindingsForDispatchAsync mirrors
// OrderAPIKeyGroupBindingsForDispatchAsync 的纯读变体：生成当前模式派发序但
// 不推进任何轮转/加权状态（Redis counter 零调用、本地 debt 零写入，
// §11 状态阻断项）。仅动态模式需要 peek；其余模式返回优先序。
func (s *APIKeyGroupRouteSelector) PeekAPIKeyGroupBindingsForDispatchAsync(ctx context.Context, apiKey *APIKeyRow) ([]GroupBindingRow, error) {
	bindings, err := normalizeGatewayAPIKeyGroupBindings(apiKey.GroupBindings)
	if err != nil {
		return nil, err
	}
	if len(bindings) <= 1 {
		return bindings, nil
	}
	if apiKey.RouteStrategyMode == RouteStrategyModeRoundRobin {
		if s.RuntimeStateDriver == runtimeStateDriverRedis {
			index, err := s.peekRedisRouteCounterIndex(ctx, redisRouteStateKey(apiKey, RedisRouteStateRoundRobinMode), int64(len(bindings)))
			if err != nil {
				return nil, err
			}
			return rotateBindings(bindings, int(index)), nil
		}
		return rotateBindings(bindings, s.states.peekRoundRobinIndex(apiKeyRouteStateKey(apiKey), len(bindings))), nil
	}
	if apiKey.RouteStrategyMode == RouteStrategyModeWeighted {
		if s.RuntimeStateDriver == runtimeStateDriverRedis {
			return s.peekWeightedBindingsWithRedisCounter(ctx, apiKey, bindings)
		}
		return s.states.peekWeightedOrder(apiKeyRouteStateKey(apiKey), bindings), nil
	}
	return bindings, nil
}

// peekWeightedBindingsWithRedisCounter 与 orderWeightedBindingsWithRedisCounter
// 同一累计权重选择，但 GET 当前 counter 而不 INCR（零状态推进）。与 memory
// peek 同一取舍：剩余序列按纯优先序排列（advance 路径为 weight-desc+priority），
// 只影响黏住组失败后的同请求后备组顺序，不影响黏住命中与状态语义。
func (s *APIKeyGroupRouteSelector) peekWeightedBindingsWithRedisCounter(ctx context.Context, apiKey *APIKeyRow, bindings []GroupBindingRow) ([]GroupBindingRow, error) {
	var totalWeight int64
	for _, binding := range bindings {
		weight, err := NormalizeAPIKeyGroupBindingWeight(binding.Weight)
		if err != nil {
			return nil, err
		}
		totalWeight += weight
	}
	selectedWeightIndex, err := s.peekRedisRouteCounterIndex(ctx, redisRouteStateKey(apiKey, RedisRouteStateWeightedMode), totalWeight)
	if err != nil {
		return nil, err
	}
	selected := bindings[0]
	var cursor int64
	for _, binding := range bindings {
		weight, err := NormalizeAPIKeyGroupBindingWeight(binding.Weight)
		if err != nil {
			return nil, err
		}
		cursor += weight
		if selectedWeightIndex < cursor {
			selected = binding
			break
		}
	}
	ordered := make([]GroupBindingRow, 0, len(bindings))
	for _, binding := range bindings {
		if binding.ID == selected.ID {
			continue
		}
		ordered = append(ordered, binding)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return compareBindingOrderByPriority(ordered[i], ordered[j]) < 0
	})
	return append([]GroupBindingRow{selected}, ordered...), nil
}

// OrderAPIKeyGroupBindingsForDispatch mirrors
// orderGatewayApiKeyGroupBindingsForDispatch: the synchronous ordering path.
func (s *APIKeyGroupRouteSelector) OrderAPIKeyGroupBindingsForDispatch(apiKey *APIKeyRow) ([]GroupBindingRow, error) {
	bindings, err := normalizeGatewayAPIKeyGroupBindings(apiKey.GroupBindings)
	if err != nil {
		return nil, err
	}
	if len(bindings) <= 1 {
		return bindings, nil
	}
	if err := assertSyncRouteStateAllowed(s.RuntimeStateDriver, apiKey.RouteStrategyMode); err != nil {
		return nil, err
	}
	if apiKey.RouteStrategyMode == RouteStrategyModeRoundRobin {
		return rotateBindings(bindings, s.states.nextRoundRobinIndex(apiKeyRouteStateKey(apiKey), len(bindings))), nil
	}
	if apiKey.RouteStrategyMode == RouteStrategyModeWeighted {
		return s.orderWeightedBindings(apiKeyRouteStateKey(apiKey), bindings)
	}
	return bindings, nil
}

// OrderAPIKeyGroupBindingsForDispatchAsync mirrors
// orderGatewayApiKeyGroupBindingsForDispatchAsync: under the redis driver
// the dynamic modes share their counter through Redis.
func (s *APIKeyGroupRouteSelector) OrderAPIKeyGroupBindingsForDispatchAsync(ctx context.Context, apiKey *APIKeyRow) ([]GroupBindingRow, error) {
	bindings, err := normalizeGatewayAPIKeyGroupBindings(apiKey.GroupBindings)
	if err != nil {
		return nil, err
	}
	if len(bindings) <= 1 {
		return bindings, nil
	}
	if s.RuntimeStateDriver != runtimeStateDriverRedis {
		return s.OrderAPIKeyGroupBindingsForDispatch(apiKey)
	}
	if apiKey.RouteStrategyMode == RouteStrategyModeRoundRobin {
		index, err := s.nextRedisRouteCounterIndex(ctx, redisRouteStateKey(apiKey, RedisRouteStateRoundRobinMode), int64(len(bindings)))
		if err != nil {
			return nil, err
		}
		return rotateBindings(bindings, int(index)), nil
	}
	if apiKey.RouteStrategyMode == RouteStrategyModeWeighted {
		return s.orderWeightedBindingsWithRedisCounter(ctx, apiKey, bindings)
	}
	return bindings, nil
}

// apiKeyRouteStateKey mirrors apiKeyRouteStateKey: the strategy id wins over
// the key id so strategies share rotation state across key restarts.
func apiKeyRouteStateKey(apiKey *APIKeyRow) string {
	if apiKey.RouteStrategyID != "" {
		return apiKey.RouteStrategyID
	}
	return apiKey.ID
}

// normalizeGatewayAPIKeyGroupBindings mirrors
// normalizeGatewayAPIKeyGroupBindings: active bindings with enabled groups,
// normalized weights, sorted by priority then group id.
func normalizeGatewayAPIKeyGroupBindings(bindings []GroupBindingRow) ([]GroupBindingRow, error) {
	normalized := make([]GroupBindingRow, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Status != RowStatusActive || binding.GroupEnabled == 0 {
			continue
		}
		weight, err := NormalizeAPIKeyGroupBindingWeight(binding.Weight)
		if err != nil {
			return nil, err
		}
		weightCopy := weight
		binding.Weight = &weightCopy
		normalized = append(normalized, binding)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return compareBindingOrderByPriority(normalized[i], normalized[j]) < 0
	})
	return normalized, nil
}

// orderWeightedBindings mirrors orderWeightedBindings: smooth weighted
// selection where each binding accrues its weight and the selected one pays
// the total weight back; the remainder is emitted by current-weight debt.
// Weights are normalized up front (a pure function of binding.Weight), so the
// first failure surfaces at the same binding as the original two-pass loop
// and the route state stays untouched on error; applyWeightedOrder then
// updates the stored per-key state in place under the registry lock instead
// of copying the whole map out and replacing it.
func (s *APIKeyGroupRouteSelector) orderWeightedBindings(key string, bindings []GroupBindingRow) ([]GroupBindingRow, error) {
	weights := make([]int64, len(bindings))
	var totalWeight int64
	for i, binding := range bindings {
		weight, err := NormalizeAPIKeyGroupBindingWeight(binding.Weight)
		if err != nil {
			return nil, err
		}
		weights[i] = weight
		totalWeight += weight
	}
	return s.states.applyWeightedOrder(key, bindings, weights, totalWeight), nil
}

// applyWeightedOrder performs the stateful half of orderWeightedBindings
// while holding the registry lock: the stored per-key state map is cleaned,
// accrued and debited in place, then the remainder is sorted by the
// post-update current weights. Holding the lock across the sort keeps every
// reader of the smooth-weighted current weights on a consistent view — the
// stored map is never observed mid-update. The lock is the registry's single
// mutex and no other lock or callback is taken inside, so no new lock order
// is introduced.
func (r *routeStateRegistry) applyWeightedOrder(key string, bindings []GroupBindingRow, weights []int64, totalWeight int64) []GroupBindingRow {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.weighted[key]
	if state == nil {
		state = make(map[string]int64)
	}
	cleanupWeightedState(state, bindings)
	selected := bindings[0]
	selectedCurrentWeight := int64(0)
	selectedSet := false
	for i, binding := range bindings {
		current := state[binding.ID] + weights[i]
		state[binding.ID] = current
		if !selectedSet || current > selectedCurrentWeight ||
			(current == selectedCurrentWeight && compareBindingOrderByPriority(binding, selected) < 0) {
			selected = binding
			selectedCurrentWeight = current
			selectedSet = true
		}
	}
	if selectedSet {
		state[selected.ID] = state[selected.ID] - totalWeight
	}
	r.weighted[key] = state
	r.appendKeyOnce(r.weightedSeen, &r.weightedKeys, key)
	r.trimLocked()
	selectedIndex := -1
	for i, binding := range bindings {
		if binding.ID == selected.ID && selectedSet {
			selectedIndex = i
			break
		}
	}
	orderedByWeightDebt := make([]GroupBindingRow, 0, len(bindings))
	for _, binding := range bindings {
		if selectedSet && binding.ID == selected.ID {
			continue
		}
		orderedByWeightDebt = append(orderedByWeightDebt, binding)
	}
	sort.SliceStable(orderedByWeightDebt, func(i, j int) bool {
		left := orderedByWeightDebt[i]
		right := orderedByWeightDebt[j]
		currentDelta := state[right.ID] - state[left.ID]
		if currentDelta != 0 {
			return currentDelta > 0
		}
		leftWeight, err := NormalizeAPIKeyGroupBindingWeight(left.Weight)
		if err != nil {
			return false
		}
		rightWeight, err := NormalizeAPIKeyGroupBindingWeight(right.Weight)
		if err != nil {
			return false
		}
		if rightWeight != leftWeight {
			return leftWeight > rightWeight
		}
		return compareBindingOrderByPriority(left, right) < 0
	})
	if selectedIndex < 0 {
		return bindings
	}
	result := make([]GroupBindingRow, 0, len(bindings))
	result = append(result, bindings[selectedIndex])
	result = append(result, orderedByWeightDebt...)
	return result
}

// orderWeightedBindingsWithRedisCounter mirrors
// orderWeightedBindingsWithRedisCounter: a single shared counter cursor picks
// the selected binding by cumulative weight; the remainder sorts by weight
// descending then binding order.
func (s *APIKeyGroupRouteSelector) orderWeightedBindingsWithRedisCounter(ctx context.Context, apiKey *APIKeyRow, bindings []GroupBindingRow) ([]GroupBindingRow, error) {
	var totalWeight int64
	for _, binding := range bindings {
		weight, err := NormalizeAPIKeyGroupBindingWeight(binding.Weight)
		if err != nil {
			return nil, err
		}
		totalWeight += weight
	}
	selectedWeightIndex, err := s.nextRedisRouteCounterIndex(ctx, redisRouteStateKey(apiKey, RedisRouteStateWeightedMode), totalWeight)
	if err != nil {
		return nil, err
	}
	selected := bindings[0]
	var cursor int64
	for _, binding := range bindings {
		weight, err := NormalizeAPIKeyGroupBindingWeight(binding.Weight)
		if err != nil {
			return nil, err
		}
		cursor += weight
		if selectedWeightIndex < cursor {
			selected = binding
			break
		}
	}
	ordered := make([]GroupBindingRow, 0, len(bindings))
	for _, binding := range bindings {
		if binding.ID == selected.ID {
			continue
		}
		ordered = append(ordered, binding)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left := ordered[i]
		right := ordered[j]
		leftWeight, err := NormalizeAPIKeyGroupBindingWeight(left.Weight)
		if err != nil {
			return false
		}
		rightWeight, err := NormalizeAPIKeyGroupBindingWeight(right.Weight)
		if err != nil {
			return false
		}
		if rightWeight != leftWeight {
			return leftWeight > rightWeight
		}
		return compareBindingOrderByPriority(left, right) < 0
	})
	result := make([]GroupBindingRow, 0, len(bindings))
	result = append(result, selected)
	result = append(result, ordered...)
	return result, nil
}

// cleanupWeightedState mirrors cleanupWeightedState: drop weights for
// bindings that are no longer active.
func cleanupWeightedState(state map[string]int64, bindings []GroupBindingRow) {
	active := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		active[binding.ID] = struct{}{}
	}
	for id := range state {
		if _, ok := active[id]; !ok {
			delete(state, id)
		}
	}
}

// rotateBindings mirrors rotateBindings.
func rotateBindings(bindings []GroupBindingRow, startIndex int) []GroupBindingRow {
	normalizedStart := startIndex
	if normalizedStart < 0 {
		normalizedStart = 0
	}
	if normalizedStart > len(bindings)-1 {
		normalizedStart = len(bindings) - 1
	}
	result := make([]GroupBindingRow, 0, len(bindings))
	result = append(result, bindings[normalizedStart:]...)
	result = append(result, bindings[:normalizedStart]...)
	return result
}

// nextRedisRouteCounterIndex mirrors nextRedisRouteCounterIndex: modulo <= 0
// short-circuits, a missing state URL and a non-numeric result fail with the
// original Chinese messages.
func (s *APIKeyGroupRouteSelector) nextRedisRouteCounterIndex(ctx context.Context, key string, modulo int64) (int64, error) {
	if modulo <= 0 {
		return 0, nil
	}
	if s.RedisStateURL == "" {
		return 0, &RouteStateURLError{Message: ErrHighPerformanceDynamicRouteRequiresStateURL}
	}
	if s.Redis == nil {
		return 0, &RouteStateURLError{Message: ErrHighPerformanceDynamicRouteRequiresStateURL}
	}
	index, err := s.Redis.NextRouteCounterIndex(ctx, key, modulo)
	if err != nil {
		return 0, err
	}
	// Node clamps with Math.max(0, Math.trunc(index)).
	if index < 0 {
		return 0, nil
	}
	return index, nil
}

// peekRedisRouteCounterIndex 是 nextRedisRouteCounterIndex 的纯读变体：
// GET 当前 counter（不 INCR、不刷 TTL），组级会话黏性的 peek 路径专用
// （§11 状态阻断项：sticky hit 的 Redis counter 调用次数为 0 是指推进类
// 调用；peek GET 不推进）。缺失键取 0。
func (s *APIKeyGroupRouteSelector) peekRedisRouteCounterIndex(ctx context.Context, key string, modulo int64) (int64, error) {
	if modulo <= 0 {
		return 0, nil
	}
	if s.Redis == nil {
		return 0, &RouteStateURLError{Message: ErrHighPerformanceDynamicRouteRequiresStateURL}
	}
	peeks, ok := s.Redis.(RedisRouteCounterPeeker)
	if !ok {
		return 0, &RouteStateURLError{Message: ErrHighPerformanceDynamicRouteRequiresStateURL}
	}
	index, err := peeks.PeekRouteCounterIndex(ctx, key, modulo)
	if err != nil {
		return 0, err
	}
	if index < 0 {
		return 0, nil
	}
	return index, nil
}

// RouteStateURLError marks the two plain-Error failures of the Node Redis
// route counter path (missing state URL, invalid counter result).
type RouteStateURLError struct{ Message string }

func (e *RouteStateURLError) Error() string { return e.Message }

// redisRouteStateKey mirrors redisRouteStateKey:
// juhe-ai:route-state:api-key-group:<mode>:<base64url(stateKey)>.
func redisRouteStateKey(apiKey *APIKeyRow, mode string) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(apiKeyRouteStateKey(apiKey)))
	return "juhe-ai:route-state:api-key-group:" + mode + ":" + encoded
}

// assertSyncRouteStateAllowed mirrors assertSyncRouteStateAllowed: the
// redis driver forbids process-local dynamic route state.
func assertSyncRouteStateAllowed(runtimeStateDriver, mode string) error {
	if runtimeStateDriver != runtimeStateDriverRedis {
		return nil
	}
	if mode != RouteStrategyModeRoundRobin && mode != RouteStrategyModeWeighted {
		return nil
	}
	return &SyncRouteStateError{Message: ErrSyncRouteStateForbidden}
}

// SyncRouteStateError marks the Node plain-Error thrown for sync dynamic
// route state under the redis driver.
type SyncRouteStateError struct{ Message string }

func (e *SyncRouteStateError) Error() string { return e.Message }
