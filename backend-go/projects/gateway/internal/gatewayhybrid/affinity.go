package gatewayhybrid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// Hybrid route affinity, mirroring backend/src/modules/gateway/hybrid/
// affinity.service.ts: per-session stickiness for the previously dispatched
// level route, downgrade confirmation and level-delta hysteresis.

// HybridRouteAffinityMaxEntries mirrors hybridRouteAffinityMaxEntries.
const HybridRouteAffinityMaxEntries = 10_000

// HybridRouteAffinityMaxTTL mirrors hybridRouteAffinityMaxTtlMs (24h).
const HybridRouteAffinityMaxTTL = int64(24 * 60 * 60 * 1000)

// HybridRouteAffinityBinding mirrors HybridRouteAffinityBinding.
type HybridRouteAffinityBinding struct {
	Route     routestrategies.HybridLevelRoute `json:"route"`
	LastLevel int                              `json:"lastLevel"`
	LowCount  int                              `json:"lowCount"`
}

// HybridRouteAffinityDecision mirrors HybridRouteAffinityDecision; LowCount
// stays nil when Node leaves it undefined.
type HybridRouteAffinityDecision struct {
	Route         routestrategies.HybridLevelRoute
	Applied       bool
	Reason        string
	PreviousModel string
	LowCount      *int
}

func decisionLowCount(value int) *int { return &value }

// AffinityService implements the memory and Redis runtime-state drivers.
// A nil StateStore selects the memory driver
// (runtimeConfig.runtimeStateDriver !== 'redis').
type AffinityService struct {
	clock      Clock
	identity   SessionIdentityPort
	stateStore RuntimeStateStore

	memory hybridAffinityMemoryStore
}

// NewAffinityService builds the service. stateStore nil → memory driver.
func NewAffinityService(clock Clock, identity SessionIdentityPort, stateStore RuntimeStateStore) *AffinityService {
	if clock == nil {
		clock = time.Now
	}
	return &AffinityService{clock: clock, identity: identity, stateStore: stateStore}
}

// hybridAffinityMemoryStore mirrors hybridRouteAffinityMemoryBindings: a Map
// with insertion-order eviction; re-setting an existing key keeps position.
type hybridAffinityMemoryStore struct {
	mu      sync.Mutex
	entries map[string]hybridAffinityMemoryEntry
	order   []string
}

type hybridAffinityMemoryEntry struct {
	value     HybridRouteAffinityBinding
	expiresAt time.Time
}

func (store *hybridAffinityMemoryStore) get(key string, now time.Time) (HybridRouteAffinityBinding, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, exists := store.entries[key]
	if !exists {
		return HybridRouteAffinityBinding{}, false
	}
	if !entry.expiresAt.After(now) {
		store.removeLocked(key)
		return HybridRouteAffinityBinding{}, false
	}
	return entry.value, true
}

func (store *hybridAffinityMemoryStore) set(key string, value HybridRouteAffinityBinding, expiresAt time.Time, now time.Time) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.entries == nil {
		store.entries = map[string]hybridAffinityMemoryEntry{}
	}
	if _, exists := store.entries[key]; !exists {
		store.order = append(store.order, key)
	}
	store.entries[key] = hybridAffinityMemoryEntry{value: value, expiresAt: expiresAt}
	for len(store.entries) > HybridRouteAffinityMaxEntries && len(store.order) > 0 {
		store.removeLocked(store.order[0])
	}
	_ = now
}

func (store *hybridAffinityMemoryStore) removeLocked(key string) {
	delete(store.entries, key)
	for index, candidate := range store.order {
		if candidate == key {
			store.order = append(store.order[:index], store.order[index+1:]...)
			return
		}
	}
}

func (store *hybridAffinityMemoryStore) clear() {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.entries = map[string]hybridAffinityMemoryEntry{}
	store.order = nil
}

// ClearForTest mirrors clearHybridRouteAffinityForTest.
func (service *AffinityService) ClearForTest() {
	service.memory.clear()
}

// Apply mirrors applyHybridRouteAffinity (memory driver path).
func (service *AffinityService) Apply(input AffinityInput) HybridRouteAffinityDecision {
	sessionKey := service.affinityKey(input)
	if sessionKey == "" || !input.Config.CacheAffinityEnabled || input.Config.AffinityTTLSeconds <= 0 {
		return HybridRouteAffinityDecision{Route: input.Route, Applied: false}
	}
	previous, ok := service.memory.get(sessionKey, service.clock())
	var decision HybridRouteAffinityDecision
	if ok {
		decision = ApplyAffinityDecision(ApplyAffinityDecisionInput{
			Previous: previous,
			Level:    input.Level,
			Route:    input.Route,
			Config:   input.Config,
		})
	} else {
		decision = HybridRouteAffinityDecision{Route: input.Route, Applied: false}
	}
	service.remember(sessionKey, HybridRouteAffinityBinding{
		Route:     decision.Route,
		LastLevel: input.Level,
		LowCount:  lowCountOrZero(decision),
	}, input.Config.AffinityTTLSeconds)
	return decision
}

// ApplyAsync mirrors applyHybridRouteAffinityAsync: Redis runtime-state
// driver when a state store is wired, memory otherwise.
func (service *AffinityService) ApplyAsync(ctx context.Context, input AffinityInput) (HybridRouteAffinityDecision, error) {
	if service.stateStore == nil {
		return service.Apply(input), nil
	}
	sessionKey := service.affinityKey(input)
	if sessionKey == "" || !input.Config.CacheAffinityEnabled || input.Config.AffinityTTLSeconds <= 0 {
		return HybridRouteAffinityDecision{Route: input.Route, Applied: false}, nil
	}
	var previous HybridRouteAffinityBinding
	found, err := service.stateStore.GetJSON(ctx, hybridRouteAffinityStateKey(sessionKey), &previous)
	if err != nil {
		return HybridRouteAffinityDecision{}, err
	}
	var decision HybridRouteAffinityDecision
	if found {
		decision = ApplyAffinityDecision(ApplyAffinityDecisionInput{
			Previous: previous,
			Level:    input.Level,
			Route:    input.Route,
			Config:   input.Config,
		})
	} else {
		decision = HybridRouteAffinityDecision{Route: input.Route, Applied: false}
	}
	if err := service.rememberAsync(ctx, sessionKey, HybridRouteAffinityBinding{
		Route:     decision.Route,
		LastLevel: input.Level,
		LowCount:  lowCountOrZero(decision),
	}, input.Config.AffinityTTLSeconds); err != nil {
		return HybridRouteAffinityDecision{}, err
	}
	return decision, nil
}

func lowCountOrZero(decision HybridRouteAffinityDecision) int {
	if decision.LowCount == nil {
		return 0
	}
	return *decision.LowCount
}

// AffinityInput mirrors the applyHybridRouteAffinity input.
type AffinityInput struct {
	View            *GatewayRequestView
	SystemAccountID string
	APIKeyID        string
	Config          *routestrategies.HybridRoutingConfig
	Level           int
	Route           routestrategies.HybridLevelRoute
}

// ApplyAffinityDecisionInput mirrors applyAffinityDecision input.
type ApplyAffinityDecisionInput struct {
	Previous HybridRouteAffinityBinding
	Level    int
	Route    routestrategies.HybridLevelRoute
	Config   *routestrategies.HybridRoutingConfig
}

// ApplyAffinityDecision mirrors applyAffinityDecision: same-model keeps the
// scoring route, downward moves need downgradeConsecutiveLowCount consecutive
// low scores, other switches need |level delta| >= switchMinLevelDelta.
func ApplyAffinityDecision(input ApplyAffinityDecisionInput) HybridRouteAffinityDecision {
	if input.Previous.Route.TargetModel == input.Route.TargetModel {
		return HybridRouteAffinityDecision{Route: input.Route, Applied: false, LowCount: decisionLowCount(0)}
	}
	if input.Route.MaxLevel < input.Previous.Route.MinLevel {
		lowCount := input.Previous.LowCount + 1
		if lowCount < input.Config.DowngradeConsecutiveLowCount {
			return HybridRouteAffinityDecision{
				Route:         input.Previous.Route,
				Applied:       true,
				Reason:        "downgrade_requires_consecutive_low_scores",
				PreviousModel: input.Previous.Route.TargetModel,
				LowCount:      decisionLowCount(lowCount),
			}
		}
		return HybridRouteAffinityDecision{Route: input.Route, Applied: false, LowCount: decisionLowCount(0)}
	}
	levelDelta := input.Level - input.Previous.LastLevel
	if levelDelta < 0 {
		levelDelta = -levelDelta
	}
	if levelDelta < input.Config.SwitchMinLevelDelta {
		return HybridRouteAffinityDecision{
			Route:         input.Previous.Route,
			Applied:       true,
			Reason:        "level_delta_below_threshold",
			PreviousModel: input.Previous.Route.TargetModel,
			LowCount:      decisionLowCount(input.Previous.LowCount),
		}
	}
	return HybridRouteAffinityDecision{Route: input.Route, Applied: false, LowCount: decisionLowCount(0)}
}

func (service *AffinityService) remember(key string, binding HybridRouteAffinityBinding, ttlSeconds int) {
	ttlMs := hybridAffinityTTLMs(ttlSeconds)
	expiresAt := service.clock().Add(time.Duration(hybridMaxInt64(1, ttlMs)) * time.Millisecond)
	service.memory.set(key, binding, expiresAt, service.clock())
}

func (service *AffinityService) rememberAsync(ctx context.Context, key string, binding HybridRouteAffinityBinding, ttlSeconds int) error {
	ttlMs := hybridAffinityTTLMs(ttlSeconds)
	return service.stateStore.SetJSON(ctx, hybridRouteAffinityStateKey(key), binding, hybridMaxInt64(1, ttlMs))
}

// hybridAffinityTTLMs mirrors rememberHybridRouteAffinity's TTL clamp.
func hybridAffinityTTLMs(ttlSeconds int) int64 {
	truncated := int64(ttlSeconds)
	if float64(truncated) != math.Trunc(float64(truncated)) {
		truncated = int64(math.Trunc(float64(truncated)))
	}
	scaled := hybridMaxInt64(1, truncated) * 1000
	return hybridMinInt64(HybridRouteAffinityMaxTTL, scaled)
}

func hybridMaxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func hybridMinInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func hybridRouteAffinityStateKey(key string) string {
	return "session:" + key
}

// affinityKey mirrors hybridRouteAffinityKey: undefined without identity,
// otherwise deriveGatewaySessionAffinityKey(identity, {systemAccountId,
// apiKeyId, routeStrategyId: 'hybrid_smart', groupId: hybridRoutePoolScope}).
func (service *AffinityService) affinityKey(input AffinityInput) string {
	if service.identity == nil || input.View == nil || input.View.ConversationKey == "" {
		return ""
	}
	return service.identity.HybridRouteAffinityKey(input.View, AffinityKeyScope{
		SystemAccountID: input.SystemAccountID,
		APIKeyID:        input.APIKeyID,
		RouteStrategyID: "hybrid_smart",
		GroupID:         HybridRoutePoolScope(input.Config),
	})
}

// HybridRoutePoolScope mirrors hybridRoutePoolScope: sha256 fingerprint over
// {scoringGroupId, levelRoutes[]} rendered like JSON.stringify.
func HybridRoutePoolScope(config *routestrategies.HybridRoutingConfig) string {
	payload := NewOrderedJSON()
	if config.ScoringGroupID != nil {
		payload.Set("scoringGroupId", *config.ScoringGroupID)
	} else {
		payload.Set("scoringGroupId", nil)
	}
	routes := make([]any, 0, len(config.LevelRoutes))
	for _, route := range config.LevelRoutes {
		entry := NewOrderedJSON()
		entry.Set("minLevel", route.MinLevel)
		entry.Set("maxLevel", route.MaxLevel)
		entry.Set("targetModel", route.TargetModel)
		entry.Set("enabled", route.Enabled)
		routes = append(routes, entry)
	}
	payload.Set("levelRoutes", routes)
	digest := sha256.Sum256([]byte(NodeJSONStringify(payload)))
	return "hybrid-route-pool:" + hex.EncodeToString(digest[:])
}
