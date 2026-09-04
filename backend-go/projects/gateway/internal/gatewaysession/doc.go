// Package gatewaysession migrates the Node gateway session identity and
// session affinity services (work package G14):
//
//   - session-identity/canonicalizer.ts — versioned HMAC identity keys;
//   - session-identity/types.ts — identity candidate / conflict contract;
//   - session-identity/request-utils.ts — normalized request path + headers;
//   - session-identity/resolvers.ts — codex / claude-code header resolvers;
//   - session-identity/registry.ts — resolver registry;
//   - session-identity/service.ts — resolveGatewaySessionIdentity;
//   - runtime/session-affinity.service.ts — affinity binding store (process
//     local + Redis Lua), traffic migration preference and account ordering.
//
// Source of truth is `git show HEAD:backend/src/modules/gateway/...` (pristine
// Node). The affinity store keeps the Node dual-driver split: with
// CacheDriverRedis every binding lives in Redis (three Lua scripts guard
// compare-and-set / delete / refresh), with CacheDriverMemory the same
// semantics run against process-local TTL caches and indexes.
package gatewaysession
