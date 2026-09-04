package gatewayhybrid

import (
	"math"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// Level-route helpers mirroring backend/src/domain/api-key-hybrid-routing.ts
// (the routing-side functions; config normalization lives in
// internal/routestrategies).

// DefaultHybridScoringFallbackMaxLevel mirrors DEFAULT_HYBRID_SCORING_FALLBACK_MAX_LEVEL.
const DefaultHybridScoringFallbackMaxLevel = 5

// ClampHybridLevel mirrors clampHybridLevel: non-finite falls back to the
// default fallback max level (5), otherwise round + clamp 1..10.
func ClampHybridLevel(value float64) int {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return DefaultHybridScoringFallbackMaxLevel
	}
	return clampLevelInt(int(math.Round(value)))
}

// clampLevelFromAny mirrors clampHybridLevel(value) where value went through
// Number() first: JS undefined coerces to NaN (fallback 5), null to 0
// (clamped to 1).
func clampLevelFromAny(value any) int {
	if IsUndefined(value) {
		return DefaultHybridScoringFallbackMaxLevel
	}
	number, ok := NodeNumber(value)
	if !ok {
		return DefaultHybridScoringFallbackMaxLevel
	}
	return ClampHybridLevel(number)
}

func clampLevelInt(value int) int {
	if value > 10 {
		return 10
	}
	if value < 1 {
		return 1
	}
	return value
}

// TargetHybridLevelRouteForLevel mirrors targetHybridLevelRouteForLevel: the
// first enabled route covering the clamped level.
func TargetHybridLevelRouteForLevel(config *routestrategies.HybridRoutingConfig, level int) *routestrategies.HybridLevelRoute {
	normalized := clampLevelInt(level)
	for index := range config.LevelRoutes {
		route := &config.LevelRoutes[index]
		if route.Enabled && route.MinLevel <= normalized && route.MaxLevel >= normalized {
			return route
		}
	}
	return nil
}

// HigherHybridLevelRoutes mirrors higherHybridLevelRoutes: enabled routes
// starting above the current route's max, sorted by (minLevel, maxLevel).
func HigherHybridLevelRoutes(config *routestrategies.HybridRoutingConfig, route *routestrategies.HybridLevelRoute) []routestrategies.HybridLevelRoute {
	higher := make([]routestrategies.HybridLevelRoute, 0, len(config.LevelRoutes))
	for _, item := range config.LevelRoutes {
		if item.Enabled && item.MinLevel > route.MaxLevel {
			higher = append(higher, item)
		}
	}
	sortHybridLevelRoutes(higher)
	return higher
}

// HybridScoringFallbackRoutes mirrors hybridScoringFallbackRoutes
// (routing.service.ts): enabled routes up to scoringFallbackMaxLevel, sorted
// by (minLevel, maxLevel).
func HybridScoringFallbackRoutes(config *routestrategies.HybridRoutingConfig) []routestrategies.HybridLevelRoute {
	routes := make([]routestrategies.HybridLevelRoute, 0, len(config.LevelRoutes))
	for _, item := range config.LevelRoutes {
		if item.Enabled && item.MinLevel <= config.ScoringFallbackMaxLevel {
			routes = append(routes, item)
		}
	}
	sortHybridLevelRoutes(routes)
	return routes
}

func sortHybridLevelRoutes(routes []routestrategies.HybridLevelRoute) {
	// JS sort is stable; (minLevel, maxLevel) is a full key here anyway.
	for i := 1; i < len(routes); i++ {
		for j := i; j > 0; j-- {
			left, right := routes[j-1], routes[j]
			if left.MinLevel < right.MinLevel || (left.MinLevel == right.MinLevel && left.MaxLevel <= right.MaxLevel) {
				break
			}
			routes[j-1], routes[j] = routes[j], routes[j-1]
		}
	}
}
