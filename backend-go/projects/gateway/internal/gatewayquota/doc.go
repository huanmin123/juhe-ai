// Package gatewayquota implements the G07 gateway quota vertical slice,
// ported from backend/src/modules/gateway/quota/:
//
//	api-key-quota.service.ts          -> apikeyquota.go
//	api-key-inflight-quota.service.ts -> inflight.go
//	authorization-quota.service.ts    -> authzquota.go
//	quota-snapshot-cache.service.ts   -> snapshot.go
//	request-quota-checker.ts          -> costs.go (re-export shim in Node)
//	storage/request-quota-limits.ts   -> limits.go
//	storage/usage-stats-helpers.ts    -> statwindow.go (date/week/month keys only)
//	shared/rfc3339.ts                 -> rfc3339.go
//	shared/runtime-state-store.ts     -> runtimestate.go + rediscache.go
//	shared/cache.ts (SharedJsonCache) -> runtimestate.go + rediscache.go
//
// Contract gates preserved verbatim from Node:
//   - allow/deny semantics: `isRequestQuotaExceeded` denies on cost >= limit,
//     while the in-flight projection denies on projected > limit (strictly
//     greater) — both operators are preserved exactly.
//   - 429 message: 额度已用完，请联系管理员提升额度 (API key and authorization
//     quota share the same text).
//   - window/reset semantics: hourly uses the pre-aggregated
//     usage_quota_hourly_windows row (window_hours), daily/weekly/monthly use
//     the timezone-aware stat_date/stat_week(Monday)/stat_month keys of "now",
//     so a reset happens exactly when the zoned key rolls over.
//   - snapshot invalidation: the authorization snapshot keeps a version
//     counter plus publishedAt watermark (monotonic max) so readers reject
//     snapshots generated before the invalidation, mirroring the Node
//     authorization_quota_cache invalidator contract. The composition root
//     wires these methods to internal/inval topics:
//     inval.TopicAuthorizationQuota / inval.TopicAPIKeyQuota, and provides the
//     InvalidationSyncer port backed by inval.Bus.SyncFromShared (Node
//     syncGatewayCacheInvalidationsFromRuntimeState).
//
// Runtime forks preserved as explicit Modes (Node runtimeConfig):
//   - PostgresDatabase: databaseDriver === 'postgres' (SQLite otherwise)
//   - RedisCache: cacheDriver === 'redis' (process-local caches otherwise)
//   - RedisRuntimeState: runtimeStateDriver === 'redis' (memory store otherwise)
//   - ServerRole: processRole === 'server' (local SQLite sync reads refused)
//
// DB-service exact checks (Node db-service-ipc requestDbService) are the
// DBServiceClient port; tests inject mocks. Redis paths use go-redis plus the
// juhe-ai:<namespace>: key convention (redisNamespacedKey) and are tested
// against miniredis.
package gatewayquota
