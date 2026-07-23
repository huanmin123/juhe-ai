import assert from 'node:assert/strict'

import { MemorySameTierExplorationStore } from '../../modules/gateway/runtime/same-tier-exploration-memory-store.js'
import {
  SAME_TIER_EXPLORATION_IDENTITY_CAPACITY,
  SAME_TIER_EXPLORATION_TARGET_COOLDOWN_MS
} from '../../modules/gateway/runtime/same-tier-exploration-store.js'

let nowMs = 100_000
const store = new MemorySameTierExplorationStore({ now: () => nowMs, stateTtlMs: 5 * 60_000, poolCapacity: 8 })
const poolKey = 'pool-a'

await Promise.all(Array.from({ length: 20 }, (_, index) => store.accrue({
  poolKey,
  accrualToken: `eligible-${index}`,
  eligible: true
})))
assert.equal((await store.get({ poolKey })).credit, 1, '20 个合格首派发必须原子累积为 1 credit')

await Promise.all(Array.from({ length: 20 }, () => store.accrue({
  poolKey,
  accrualToken: 'duplicate-accrual-token',
  eligible: true
})))
assert.equal((await store.get({ poolKey })).credit, 1, '重复 accrual token 不得重复补充 credit')

const reservations = await Promise.all([
  store.reserve({ poolKey, reservationId: 'reservation-a', accountRuntimeKey: 'target-a', leaseUntilMs: nowMs + 1_000 }),
  store.reserve({ poolKey, reservationId: 'reservation-b', accountRuntimeKey: 'target-b', leaseUntilMs: nowMs + 1_000 })
])
assert.equal(reservations.filter((item) => item.status === 'reserved').length, 1, '同一 peer-pool 只能有一个在途 reservation')
assert.equal(reservations.filter((item) => item.status === 'pool_busy').length, 1)
const winner = reservations.find((item) => item.status === 'reserved')!.reservation!

const restored = await store.settle({
  poolKey,
  reservationId: winner.reservationId,
  accountRuntimeKey: winner.accountRuntimeKey,
  outcome: 'not_dispatched'
})
assert.equal(restored.status, 'applied')
assert.equal(restored.state.credit, 1, '未进入真实上游必须归还完整 credit')
assert.equal(restored.state.cursor, 0, '未派发不得推进公平 cursor')
assert.equal(restored.state.cooldownUntilMsByRuntimeKey['target-a'], undefined, '未派发不得伪造目标探索冷却')
assert.equal((await store.settle({
  poolKey,
  reservationId: winner.reservationId,
  accountRuntimeKey: winner.accountRuntimeKey,
  outcome: 'not_dispatched'
})).status, 'idempotent')

const dispatchedReservation = await store.reserve({
  poolKey,
  reservationId: 'reservation-dispatched',
  accountRuntimeKey: 'target-a',
  leaseUntilMs: nowMs + 1_000
})
assert.equal(dispatchedReservation.status, 'reserved', '归还 reservation 后目标必须可立即重新选择')
const dispatched = await store.settle({
  poolKey,
  reservationId: 'reservation-dispatched',
  accountRuntimeKey: 'target-a',
  outcome: 'dispatched'
})
assert.equal(dispatched.state.credit, 0)
assert.equal(dispatched.state.cursor, 1)
assert.equal(dispatched.state.cooldownUntilMsByRuntimeKey['target-a'], nowMs + SAME_TIER_EXPLORATION_TARGET_COOLDOWN_MS)
assert.equal((await store.settle({
  poolKey,
  reservationId: 'reservation-dispatched',
  accountRuntimeKey: 'target-a',
  outcome: 'dispatched'
})).status, 'idempotent', '重复成功回调不得再次扣 credit 或推进 cursor')

await Promise.all(Array.from({ length: 20 }, (_, index) => store.accrue({
  poolKey,
  accrualToken: `second-wave-${index}`,
  eligible: true
})))
assert.equal((await store.reserve({
  poolKey,
  reservationId: 'cooldown-blocked',
  accountRuntimeKey: 'target-a',
  leaseUntilMs: nowMs + 1_000
})).status, 'target_cooldown')
nowMs += SAME_TIER_EXPLORATION_TARGET_COOLDOWN_MS
assert.equal((await store.reserve({
  poolKey,
  reservationId: 'cooldown-expired',
  accountRuntimeKey: 'target-a',
  leaseUntilMs: nowMs + 1_000
})).status, 'reserved', '60 秒边界到达后目标必须恢复可探索')

nowMs += 1_001
assert.equal((await store.reserve({
  poolKey,
  reservationId: 'lease-takeover',
  accountRuntimeKey: 'target-a',
  leaseUntilMs: nowMs + 1_000
})).status, 'reserved', '过期 reservation 必须允许新 owner 接管')
nowMs += 1_001
assert.equal((await store.reserve({
  poolKey,
  reservationId: 'lease-takeover',
  accountRuntimeKey: 'target-b',
  leaseUntilMs: nowMs + 1_000
})).status, 'reservation_conflict', '过期 reservation ID 不得被新 owner 复用')
assert.equal((await store.settle({
  poolKey,
  reservationId: 'lease-takeover',
  accountRuntimeKey: 'target-a',
  outcome: 'dispatched'
})).status, 'idempotent', '迟到的旧 owner settlement 不得改变新状态')
await assert.rejects(
  store.reserve({ poolKey, reservationId: 'expired-at-create', accountRuntimeKey: 'target-b', leaseUntilMs: nowMs }),
  /leaseUntilMs/
)
await assert.rejects(
  store.reserve({ poolKey, reservationId: 'lease-beyond-ttl', accountRuntimeKey: 'target-b', leaseUntilMs: nowMs + 5 * 60_000 + 1 }),
  /pool TTL/
)

const identityStore = new MemorySameTierExplorationStore({ now: () => nowMs, poolCapacity: 2 })
for (let index = 0; index <= SAME_TIER_EXPLORATION_IDENTITY_CAPACITY; index += 1) {
  await identityStore.accrue({ poolKey: 'identity-pool', accrualToken: `identity-${index}`, eligible: true })
}
assert.equal(
  (await identityStore.get({ poolKey: 'identity-pool' })).accruedTokens.length,
  SAME_TIER_EXPLORATION_IDENTITY_CAPACITY,
  'accrual identity 集合必须有硬上限'
)
await identityStore.settle({
  poolKey: 'identity-pool',
  reservationId: (await identityStore.reserve({
    poolKey: 'identity-pool',
    reservationId: 'identity-spend',
    accountRuntimeKey: 'identity-target',
    leaseUntilMs: nowMs + 1_000
  })).reservation!.reservationId,
  accountRuntimeKey: 'identity-target',
  outcome: 'dispatched'
})
await identityStore.accrue({ poolKey: 'identity-pool', accrualToken: 'overflow-identity', eligible: true })
await identityStore.accrue({ poolKey: 'identity-pool', accrualToken: 'overflow-identity', eligible: true })
assert.equal((await identityStore.get({ poolKey: 'identity-pool' })).credit, 0, 'identity 容量耗尽后不得应用无法幂等追踪的 accrual')

const capacityStore = new MemorySameTierExplorationStore({ now: () => nowMs, poolCapacity: 1, stateTtlMs: 100 })
await Promise.all(Array.from({ length: 20 }, (_, index) => capacityStore.accrue({
  poolKey: 'active-pool',
  accrualToken: `active-${index}`,
  eligible: true
})))
assert.equal((await capacityStore.reserve({
  poolKey: 'active-pool',
  reservationId: 'active-reservation',
  accountRuntimeKey: 'active-target',
  leaseUntilMs: nowMs + 50
})).status, 'reserved')
assert.equal((await capacityStore.accrue({
  poolKey: 'overflow-pool',
  accrualToken: 'overflow-token',
  eligible: true
})).credit, 0, '容量满且只有活动 pool 时必须拒绝新增状态而非无界增长')
assert.equal((await capacityStore.get({ poolKey: 'active-pool' })).reservations.length, 1, '容量保护不得淘汰活动 reservation')

nowMs += 101
await capacityStore.accrue({ poolKey: 'replacement-pool', accrualToken: 'replacement-token', eligible: true })
assert.equal((await capacityStore.get({ poolKey: 'replacement-pool' })).credit, 0.05, '活动 lease 过期后可以回收旧 pool 容量')
nowMs += 101
assert.equal((await capacityStore.get({ poolKey: 'replacement-pool' })).credit, 0, 'pool TTL 到期必须清空 credit 和 identity')

console.log('same-tier exploration memory store regression passed')
