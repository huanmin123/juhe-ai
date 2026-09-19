// Package gatewayaccounteffects is the Go port of the Node gateway account
// key-pool and side-effect slice (work package G12):
//
//   - backend/src/modules/gateway/runtime/account-runtime-keys.ts
//   - backend/src/modules/gateway/runtime/account-side-effect-queue.ts
//   - backend/src/modules/gateway/runtime/account-side-effect-policy.ts
//   - backend/src/modules/gateway/runtime/account-side-effects.service.ts
//     (queue/enqueue/drain/lifecycle half; failure-storm bookkeeping)
//   - backend/src/modules/gateway/runtime/account-effects.ts
//   - backend/src/modules/gateway/runtime/account-api-key-mutation-authority.ts
//   - backend/src/modules/gateway/runtime/account-api-key-failure-guard.service.ts
//   - backend/src/modules/gateway/runtime/account-api-key-transient-redis-store.ts
//   - backend/src/modules/gateway/runtime/account-api-key-effects.service.ts
//   - backend/src/modules/gateway/runtime/key-model-runtime.ts
//   - backend/src/modules/gateway/runtime/key-model-redis-store.ts
//   - backend/src/modules/gateway/runtime/key-model-memory-recovery.ts
//   - backend/src/modules/gateway/runtime/key-model-attempt.ts
//   - backend/src/modules/gateway/runtime/key-model-capability.ts (pure halves)
//
// Cross-process key_model state shares one Redis key family with
// backend-go/projects/jobs/internal/keymodelrecovery (J-E slice):
// juhe-ai:<namespace>:gateway-account-circuit-key-model:{state,due,closed,...}
// with the identical canonical hash and state JSON contract. The account
// runtime suppression store itself belongs to G11 and is consumed here only
// through narrow ports.
//
// All time flows through Clock and all delayed work flows through Scheduler
// so the queue, guard, lease and recovery windows are deterministic under
// test; every user-visible Chinese message mirrors the Node service byte for
// byte.
//
// 生产接线边界（PLAN-20260918T142845703Z W6 / PLAN-20260919T000723744Z）:
// 生产组合根只使用本包的 apikey*（AccountAPIKeyEffects/AccountAPIKeyFailureGuard/
// transient store）、keymodel*、policyavoidance 家族与 SideEffectsConfig 中的
// RuntimeStateDriver 字段。Node account-side-effects 半区（SideEffectsService、
// epoch registry/queue、policy 谓词、AccountEffects 门面）在 Go 生产链路从未
// 接线（Node 归档中 recordGatewayAccountFailureForPrecheck 等同样零生产调用，
// 终态归档已删除 local-suppression-preflight），已于 2026-09-19 随死代码清理
// 提交 412f679a4 整簇删除；本包现仅保留上述活家族。
package gatewayaccounteffects
