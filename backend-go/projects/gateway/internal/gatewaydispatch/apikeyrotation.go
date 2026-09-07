package gatewaydispatch

import (
	"context"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 账户 API Key 轮转策略族，移植 Node
// storage/account-api-key-rotation.ts（归档 :87-115 选择、:177-181 策略、
// :183-224 rr/weighted 轮转、:239-252 states 过滤、:254-267 continuation、
// :269-299 Redis 计数器）。B-3（BUG-0174）：此前 Go 恒选「第一把可用 Key」，
// 无轮转语义。

// api_key_strategy 凭据值（归档 AccountApiKeyStrategy，:15、:177-181）。
const (
	accountAPIKeyStrategyFailover           = "failover"
	accountAPIKeyStrategyWeightedRoundRobin = "weighted_round_robin"
	accountAPIKeyStrategyRoundRobin         = "round_robin"
)

// Redis 轮转计数器键内的 strategy 段（归档 :271、:297-299：键命名为
// juhe-ai:route-state:account-api-key:<strategy>:<base64url(accountId)>）。
const (
	apiKeyRotationScopeRoundRobin = "round-robin"
	apiKeyRotationScopeWeighted   = "weighted"
)

// APIKeyRotationCounter 是 Key 轮转计数器的最小依赖端口，等价 Node
// nextRedisAccountApiKeyRotationIndex（归档 :269-295）的消费面：在键
// juhe-ai:route-state:account-api-key:<strategy>:<base64url(accountID)> 上
// INCR + PEXPIRE 30d（redisAccountApiKeyRotationTtlMs = 30*24*60*60*1000），
// 返回 (value - 1) % modulo（取值 [0, modulo)）。strategy 取
// apiKeyRotationScope* 键段常量。
//
// 落地方式（BUG-0174 波1）：gatewaydispatch 现有依赖里没有可注入的 Redis
// 客户端（Engine 端口均为业务接口），因此本波不自行连 Redis——组合根下一波
// 用 shared Redis 客户端按上述键命名/TTL 实现 Redis 版计数器并注入
// Engine.KeyRotation；Redis 不可用或未注入时回落包级进程内计数器
// defaultAPIKeyRotationCounter（inProcessAPIKeyRotationCounter），单进程内
// 语义与 Redis Lua（INCR 后 (value-1)%modulo）一致，仅不跨进程共享。
type APIKeyRotationCounter interface {
	NextIndex(ctx context.Context, accountID string, strategy string, modulo int) (int, error)
}

// inProcessAPIKeyRotationCounter 是进程内计数器回退实现：键域为
// (accountID, strategy)，模回环语义对齐归档 Redis Lua。
type inProcessAPIKeyRotationCounter struct {
	mu       sync.Mutex
	counters map[string]int64
}

func newInProcessAPIKeyRotationCounter() *inProcessAPIKeyRotationCounter {
	return &inProcessAPIKeyRotationCounter{counters: map[string]int64{}}
}

func (c *inProcessAPIKeyRotationCounter) NextIndex(_ context.Context, accountID, strategy string, modulo int) (int, error) {
	if modulo <= 0 {
		return 0, nil
	}
	key := accountID + "\x00" + strategy
	c.mu.Lock()
	defer c.mu.Unlock()
	value := c.counters[key] + 1
	c.counters[key] = value
	return int((value - 1) % int64(modulo)), nil
}

// defaultAPIKeyRotationCounter 是 Engine.KeyRotation 未接线时的包级进程内
// 回退（组合根 Redis 计数器落地前的默认轮转语义）。
var defaultAPIKeyRotationCounter APIKeyRotationCounter = newInProcessAPIKeyRotationCounter()

// accountApiKeyStrategy mirrors accountApiKeyStrategy（归档 :177-181）：
// failover / weighted_round_robin 显式声明，其余（含缺省）按 round_robin。
func accountApiKeyStrategy(credentials map[string]any) string {
	switch credentials["api_key_strategy"] {
	case accountAPIKeyStrategyFailover:
		return accountAPIKeyStrategyFailover
	case accountAPIKeyStrategyWeightedRoundRobin:
		return accountAPIKeyStrategyWeightedRoundRobin
	default:
		return accountAPIKeyStrategyRoundRobin
	}
}

// selectAccountRuntimeApiKeyEntry mirrors selectAccountRuntimeApiKeyEntry
// （归档 :59-85；async 版 :87-115 在 Redis 驱动下的差异只在计数器后端，
// Go 侧统一走 counter 端口）。流程：
//  1. states 过滤（归档 :239-252，status !== 'active' 全剔除，unverified 不
//     例外）：Go 水合层把 status != 'active' 折叠为
//     AccountAPIKeyRuntimeSelectionState.Disabled
//     （cmd/juhe-ai-gateway/chain_accounts.go
//     loadAPIKeyRuntimeStatesByAccountIds），故跳过 Disabled 即等价；
//  2. per-request 排除（excludeFingerprints）；
//  3. 单候选直选；continuation（continueAfterFingerprint）优先于策略；
//  4. 策略：failover 恒首把 / weighted 按权重 / round_robin 计数轮转。
//
// 与归档的唯一行为偏差（保留既有 Go 行为，任务指示保留）：冷却中的候选
// （Disabled=false 但 CooldownUntil 未过期，水合快照滞后的窗口）后置为
// recovery，仅当无健康候选时兜底返回；Node 侧同一窗口由 status 本身覆盖。
func selectAccountRuntimeApiKeyEntry(ctx context.Context, input apiKeySelectionInput) (*selectedApiKeyEntry, error) {
	if len(input.entries) == 0 {
		return nil, nil
	}
	excluded := input.ExcludeFingerprints
	if excluded == nil {
		excluded = map[string]struct{}{}
	}
	stateByFingerprint := make(map[string]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState, len(input.RuntimeStates))
	for _, state := range input.RuntimeStates {
		stateByFingerprint[state.Fingerprint] = state
	}

	// 1. states 过滤：status !== 'active'（Disabled）剔除（归档 :239-252）。
	available := make([]apiKeyEntry, 0, len(input.entries))
	for _, entry := range input.entries {
		if state, hasState := stateByFingerprint[entry.fingerprint]; hasState && state.Disabled {
			continue
		}
		available = append(available, entry)
	}

	// 2. per-request 排除；冷却中的候选后置（Go 兜底，见函数注释）。
	candidates := make([]apiKeyEntry, 0, len(available))
	var recovery []apiKeyEntry
	for _, entry := range available {
		if _, isExcluded := excluded[entry.fingerprint]; isExcluded {
			continue
		}
		if state, hasState := stateByFingerprint[entry.fingerprint]; hasState && state.CooldownUntil != nil &&
			cooldownUntilActive(*state.CooldownUntil) {
			recovery = append(recovery, entry)
			continue
		}
		candidates = append(candidates, entry)
	}

	if len(candidates) == 0 {
		if len(recovery) == 0 {
			return nil, nil
		}
		selected := recovery[0]
		return &selectedApiKeyEntry{key: selected.key, fingerprint: selected.fingerprint, index: selected.index}, nil
	}
	// 3. 单候选直选（归档 :74）。
	if len(candidates) == 1 {
		selected := candidates[0]
		return &selectedApiKeyEntry{key: selected.key, fingerprint: selected.fingerprint, index: selected.index}, nil
	}
	// continuation 优先于策略（归档 :75-78、:103-106、:254-267）。
	if input.ContinueAfterFingerprint != "" {
		return selectedOf(*selectNextAPIKeyAfterFingerprint(input.entries, candidates, input.ContinueAfterFingerprint)), nil
	}
	// 4. 策略选择（归档 :79-84、:110-114）。
	switch accountApiKeyStrategy(input.credentials) {
	case accountAPIKeyStrategyFailover:
		return selectedOf(candidates[0]), nil
	case accountAPIKeyStrategyWeightedRoundRobin:
		return selectWeightedAPIKeyEntry(ctx, input.counter, input.AccountID, candidates)
	default:
		return selectRoundRobinAPIKeyEntry(ctx, input.counter, input.AccountID, candidates)
	}
}

// selectNextAPIKeyAfterFingerprint mirrors selectNextApiKeyAfterFingerprint
// （归档 :254-267）：在全量 pool 顺序中从 previous 指纹的下一位开始环回，
// 返回第一个命中候选集的条目；previous 不在 pool 中时回退 candidates[0]。
func selectNextAPIKeyAfterFingerprint(pool, candidates []apiKeyEntry, previousFingerprint string) *apiKeyEntry {
	previousIndex := -1
	for index := range pool {
		if pool[index].fingerprint == previousFingerprint {
			previousIndex = index
			break
		}
	}
	if previousIndex < 0 {
		return &candidates[0]
	}
	candidateSet := make(map[string]struct{}, len(candidates))
	for _, entry := range candidates {
		candidateSet[entry.fingerprint] = struct{}{}
	}
	for offset := 1; offset <= len(pool); offset++ {
		entry := &pool[(previousIndex+offset)%len(pool)]
		if _, ok := candidateSet[entry.fingerprint]; ok {
			return entry
		}
	}
	return &candidates[0]
}

// selectRoundRobinAPIKeyEntry 等价 selectRoundRobinApiKeyWithRedisCounter
// （归档 :190-192）：计数器索引对候选数取模轮转。
func selectRoundRobinAPIKeyEntry(ctx context.Context, counter APIKeyRotationCounter, accountID string, entries []apiKeyEntry) (*selectedApiKeyEntry, error) {
	index, err := counter.NextIndex(ctx, accountID, apiKeyRotationScopeRoundRobin, len(entries))
	if err != nil {
		return nil, err
	}
	index %= len(entries)
	return selectedOf(entries[index]), nil
}

// selectWeightedAPIKeyEntry 等价 selectWeightedApiKeyWithRedisCounter
// （归档 :213-224）：计数器索引对总权重取模，再按权重游标落位。
func selectWeightedAPIKeyEntry(ctx context.Context, counter APIKeyRotationCounter, accountID string, entries []apiKeyEntry) (*selectedApiKeyEntry, error) {
	totalWeight := 0
	for _, entry := range entries {
		totalWeight += entry.weight
	}
	index, err := counter.NextIndex(ctx, accountID, apiKeyRotationScopeWeighted, totalWeight)
	if err != nil {
		return nil, err
	}
	cursor := 0
	for i := range entries {
		cursor += entries[i].weight
		if index < cursor {
			return selectedOf(entries[i]), nil
		}
	}
	return selectedOf(entries[0]), nil
}

func selectedOf(entry apiKeyEntry) *selectedApiKeyEntry {
	return &selectedApiKeyEntry{key: entry.key, fingerprint: entry.fingerprint, index: entry.index}
}
