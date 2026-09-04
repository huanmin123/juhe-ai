// Package gatewayrouting ports the Node gateway normal-route resolution
// vertical to Go. It covers, file by file:
//
//	routing/api-key-group-route-selector.service.ts -> routeselector.go
//	routing/model-target-group-selector.ts          -> targetgroupselector.go
//	routing/normal-model-route.service.ts           -> normalroute.go
//	routing/normal-route-first-byte-deadline.ts     -> firstbytedeadline.go
//	routing/route-coordination.ts                   -> routecoordination.go
//	policy/timeout-profile.ts                       -> timeoutprofile.go
//	policy/speed-first-lane.ts                      -> firstbytedeadline.go
//	domain/api-key-routing.ts                       -> bindings.go
//
// routing/hot-quality-candidate-selection.ts is owned by the G09 work
// package and is intentionally absent; none of the files above import it.
//
// Cross-package reads that belong to other slices are consumed through port
// interfaces defined here and injected by the caller:
//
//   - RuntimeCacheReader  (Node runtime-cache.service.ts reads: group usage
//     access metadata, accounts for group, provider model route resolution;
//     the concrete cache is the concurrent gatewayruntimecache work package)
//   - AccountCapabilityFilter (Node dispatch/account-capability-filter.ts /
//     providers drivers registry capability probe)
//   - RedisRouteCounter (Redis-backed dynamic route counter used when the
//     runtime state driver is redis; a go-redis implementation ships in
//     rediscounter.go)
//   - RoutingObserver (Node observability/routing-observability.service.ts
//     observeGatewayRouting budget events)
//
// Model filtering (Node dispatch/model-filter.ts) is pure once account model
// mappings are available, so it is implemented here directly on top of the
// existing internal/gatewayopenai.ResolveAccountModelMapping without
// duplicating the mapping resolution rules.
//
// Behavioural parity contract: group selection results, model mapping
// targets, fallback ordering, first-byte deadline clipping, and the Chinese
// error strings are byte-identical to the Node implementation. JS optional
// numbers become *int64, and JS RangeError/TypeError normalization failures
// become *RangeError/*TypeError values carrying the original message text.
package gatewayrouting
