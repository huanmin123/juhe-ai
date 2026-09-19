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
// RuntimeStateDriver 字段。sideeffects.go 的 SideEffectsService、sideeffectqueue.go
// 的 epoch registry/queue、sideeffectpolicy.go 全部、accounteffects.go 的
// AccountEffects（ApplyAccountErrorHandling/MarkTemporaryUnavailable/ClearStreamFailure/
// HandleStreamFailure）是 Node 未接线机制的移植残留，生产零调用（Node 归档中
// recordGatewayAccountFailureForPrecheck 等同样零生产调用，终态归档已删除
// local-suppression-preflight）。整簇删除因 8 个测试文件与活符号混测而暂缓，
// 待专项清理计划按文件拆解。
package gatewayaccounteffects
