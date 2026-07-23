import assert from 'node:assert/strict'

import { closeRedisClients, getRedisClient } from '../../shared/redis-client.js'
import {
  RedisSameTierExplorationStore,
  redisSameTierExplorationStoreKeys
} from '../../modules/gateway/runtime/same-tier-exploration-redis-store.js'

const redisUrl = process.env.JUHE_AI_TEST_REDIS_URL?.trim()
if (!redisUrl) {
  console.log('same-tier exploration Redis smoke skipped: JUHE_AI_TEST_REDIS_URL 未配置')
  process.exit(0)
}

const name = `regression-${process.pid}-${Date.now()}`
const capacityName = `${name}-capacity`
const ttlName = `${name}-ttl`
const redis = await getRedisClient(redisUrl)
await clearStore(redis, redisSameTierExplorationStoreKeys(name).prefix)
await clearStore(redis, redisSameTierExplorationStoreKeys(capacityName).prefix)
await clearStore(redis, redisSameTierExplorationStoreKeys(ttlName).prefix)

try {
  let nowMs = 200_000
  const first = new RedisSameTierExplorationStore({ redisUrl, name, poolCapacity: 4, now: () => nowMs })
  const second = new RedisSameTierExplorationStore({ redisUrl, name, poolCapacity: 4, now: () => nowMs })
  const poolKey = 'shared-pool'

  await Promise.all(Array.from({ length: 20 }, (_, index) => (index % 2 === 0 ? first : second).accrue({
    poolKey,
    accrualToken: `eligible-${index}`,
    eligible: true
  })))
  assert.equal((await second.get({ poolKey })).credit, 1, '双 adapter 必须共享原子 credit')
  await Promise.all(Array.from({ length: 20 }, (_, index) => (index % 2 === 0 ? first : second).accrue({
    poolKey,
    accrualToken: 'duplicate-token',
    eligible: true
  })))
  assert.equal((await first.get({ poolKey })).credit, 1, '跨实例重复 token 不得重复累积')

  const [reserveA, reserveB] = await Promise.all([
    first.reserve({ poolKey, reservationId: 'reservation-a', accountRuntimeKey: 'target-a', leaseUntilMs: nowMs + 1_000 }),
    second.reserve({ poolKey, reservationId: 'reservation-b', accountRuntimeKey: 'target-b', leaseUntilMs: nowMs + 1_000 })
  ])
  assert.equal([reserveA, reserveB].filter((item) => item.status === 'reserved').length, 1, '跨实例 peer-pool reservation 必须单飞')
  const winner = [reserveA, reserveB].find((item) => item.status === 'reserved')!.reservation!
  const restored = await second.settle({
    poolKey,
    reservationId: winner.reservationId,
    accountRuntimeKey: winner.accountRuntimeKey,
    outcome: 'not_dispatched'
  })
  assert.equal(restored.state.credit, 1)
  assert.equal(restored.state.cursor, 0)
  assert.equal(restored.state.cooldownUntilMsByRuntimeKey['target-a'], undefined)

  assert.equal((await first.reserve({
    poolKey,
    reservationId: 'real-dispatch',
    accountRuntimeKey: 'target-a',
    leaseUntilMs: nowMs + 1_000
  })).status, 'reserved')
  const dispatched = await second.settle({
    poolKey,
    reservationId: 'real-dispatch',
    accountRuntimeKey: 'target-a',
    outcome: 'dispatched'
  })
  assert.equal(dispatched.state.credit, 0)
  assert.equal(dispatched.state.cursor, 1)
  assert.equal(dispatched.state.cooldownUntilMsByRuntimeKey['target-a'], nowMs + 60_000)
  assert.equal((await first.settle({
    poolKey,
    reservationId: 'real-dispatch',
    accountRuntimeKey: 'target-a',
    outcome: 'dispatched'
  })).status, 'idempotent')

  await Promise.all(Array.from({ length: 20 }, (_, index) => first.accrue({
    poolKey,
    accrualToken: `refill-${index}`,
    eligible: true
  })))
  assert.equal((await second.reserve({
    poolKey,
    reservationId: 'cooldown-blocked',
    accountRuntimeKey: 'target-a',
    leaseUntilMs: nowMs + 1_000
  })).status, 'target_cooldown')
  assert.equal((await second.reserve({
    poolKey,
    reservationId: 'expiring-lease',
    accountRuntimeKey: 'target-b',
    leaseUntilMs: nowMs + 1_000
  })).status, 'reserved')
  nowMs += 1_001
  assert.equal((await first.reserve({
    poolKey,
    reservationId: 'lease-takeover',
    accountRuntimeKey: 'target-b',
    leaseUntilMs: nowMs + 1_000
  })).status, 'reserved', 'Lua 必须清除过期 lease 并允许另一个实例接管')
  nowMs += 1_001
  assert.equal((await second.reserve({
    poolKey,
    reservationId: 'lease-takeover',
    accountRuntimeKey: 'target-a',
    leaseUntilMs: nowMs + 1_000
  })).status, 'reservation_conflict', '过期 reservation ID 不得被跨实例旧 owner 复用')

  const capacity = new RedisSameTierExplorationStore({ redisUrl, name: capacityName, poolCapacity: 1, now: () => nowMs })
  await capacity.accrue({ poolKey: 'capacity-a', accrualToken: 'a', eligible: true })
  assert.equal((await capacity.accrue({ poolKey: 'capacity-b', accrualToken: 'b', eligible: true })).credit, 0)
  assert.equal(
    Number(await redis.sendCommand(['ZCARD', redisSameTierExplorationStoreKeys(capacityName).registry])),
    1,
    'Redis registry 不得超过 pool 容量'
  )

  const ttl = new RedisSameTierExplorationStore({ redisUrl, name: ttlName, poolCapacity: 1, stateTtlMs: 30 })
  await ttl.accrue({ poolKey: 'ttl-pool', accrualToken: 'ttl', eligible: true })
  await new Promise((resolve) => setTimeout(resolve, 80))
  const ttlPoolKeys = await scanKeys(redis, `${redisSameTierExplorationStoreKeys(ttlName).prefix}:pool:*`)
  assert.equal(ttlPoolKeys.length, 0, 'peer-pool 状态必须使用原生 Redis TTL 到期')

  console.log('same-tier exploration Redis smoke passed')
} finally {
  try {
    await clearStore(redis, redisSameTierExplorationStoreKeys(name).prefix)
    await clearStore(redis, redisSameTierExplorationStoreKeys(capacityName).prefix)
    await clearStore(redis, redisSameTierExplorationStoreKeys(ttlName).prefix)
  } finally {
    await closeRedisClients()
  }
}

async function clearStore(client: Awaited<ReturnType<typeof getRedisClient>>, prefix: string): Promise<void> {
  const keys = await scanKeys(client, `${prefix}:*`)
  if (keys.length > 0) await client.sendCommand(['DEL', ...keys])
}

async function scanKeys(client: Awaited<ReturnType<typeof getRedisClient>>, pattern: string): Promise<string[]> {
  let cursor = '0'
  const keys: string[] = []
  do {
    const raw = await client.sendCommand(['SCAN', cursor, 'MATCH', pattern, 'COUNT', '200'])
    const [nextCursor, values] = raw as [string | number, unknown[]]
    cursor = String(nextCursor)
    keys.push(...values.map((value) => Buffer.isBuffer(value) ? value.toString('utf8') : String(value)))
  } while (cursor !== '0')
  return keys
}
