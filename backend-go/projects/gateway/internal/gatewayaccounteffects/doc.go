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
package gatewayaccounteffects
