// Package gatewayhybrid is the G09 slice of the W4-W5 gateway chain: the
// hybrid smart routing (混合智能路由) core.
//
// It mirrors backend/src/modules/gateway/hybrid plus the assigned pure
// selection module backend/src/modules/gateway/routing/hot-quality-candidate-selection.ts:
//
//   - scoring.service.ts        → scoring.go (difficulty scoring request,
//     context sanitization, LRU + Redis shared cache, response parsing),
//   - affinity.service.ts       → affinity.go (session stickiness: memory map
//     plus Redis runtime-state dual driver),
//   - quality-inspection.service.ts → quality.go (trigger rules, quality
//     scoring response parsing, action resolution),
//   - quality-repair.service.ts → repair.go (repair instruction construction
//     and chat/responses body mutation),
//   - routing.service.ts        → routing.go (route resolution: level route
//     selection, scoring fallback, affinity stickiness, upgrade candidates),
//   - auxiliary-dispatch.service.ts → ports.go (the upstream auxiliary
//     dispatch is a port; the pure response/usage parsing helpers live in
//     auxiliary.go),
//   - hot-quality-candidate-selection.ts → hotquality.go (tier ordering and
//     same-tier exploration, fully deterministic),
//   - domain/api-key-hybrid-routing.ts → config.go (level-route helpers).
//
// Hybrid config normalization is owned by internal/routestrategies
// (HybridRoutingConfig / HybridLevelRoute / HybridQualityInspection); this
// package only imports it. External capabilities (auxiliary upstream
// dispatch, target group selection, session identity, usage recording,
// request body mutation, diagnostics) are ports defined here and mocked in
// tests; Redis adapters follow the juhe-ai:<namespace> key convention.
//
// Every time source is an injected clock and the algorithms are
// deterministic (Node has no randomness in this slice; same-tier
// exploration fairness uses a persisted cursor), so tests replay exactly.
package gatewayhybrid
