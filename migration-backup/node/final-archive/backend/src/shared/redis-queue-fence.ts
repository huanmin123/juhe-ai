import { createDedicatedRedisClient, type RedisCommandClient } from './redis-client.js'
import { redisNamespacedKey } from './redis-namespace.js'

const redisQueueFenceReleaseScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

const redisQueueFenceIdempotentReleaseScript = `
local current = redis.call('GET', KEYS[1])
if not current then
  return 1
end
if current ~= ARGV[1] then
  return 0
end
redis.call('DEL', KEYS[1])
return 1
`

const redisQueueFenceRenewScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`

// A fence must recover after an interrupted maintenance operation.  The drain
// command renews this lease while it owns the queue, rather than relying on a
// permanently blocking key.
export const redisQueueFenceLeaseMs = 120_000

export function redisQueueFenceKey(): string {
  return redisNamespacedKey('juhe-ai:queue:fence')
}

export async function acquireRedisQueueFence(
  redisUrl: string,
  token: string,
  leaseMs = redisQueueFenceLeaseMs
): Promise<boolean> {
  const client = await createDedicatedRedisClient(redisUrl, { disableOfflineQueue: true, connectTimeoutMs: 3000 })
  try {
    return await acquireRedisQueueFenceWithClient(client, token, leaseMs)
  } finally {
    await closeDedicatedClient(client)
  }
}

export async function renewRedisQueueFence(redisUrl: string, token: string, leaseMs = redisQueueFenceLeaseMs): Promise<boolean> {
  const client = await createDedicatedRedisClient(redisUrl, { disableOfflineQueue: true, connectTimeoutMs: 3000 })
  try {
    return await renewRedisQueueFenceWithClient(client, token, leaseMs)
  } finally {
    await closeDedicatedClient(client)
  }
}

export async function releaseRedisQueueFence(redisUrl: string, token: string): Promise<boolean> {
  const client = await createDedicatedRedisClient(redisUrl, { disableOfflineQueue: true, connectTimeoutMs: 3000 })
  try {
    return await releaseRedisQueueFenceWithClient(client, token)
  } finally {
    await closeDedicatedClient(client)
  }
}

export async function releaseRedisQueueFenceIdempotently(redisUrl: string, token: string): Promise<boolean> {
  const client = await createDedicatedRedisClient(redisUrl, { disableOfflineQueue: true, connectTimeoutMs: 3000 })
  try {
    return await releaseRedisQueueFenceIdempotentlyWithClient(client, token)
  } finally {
    await closeDedicatedClient(client)
  }
}

export async function acquireRedisQueueFenceWithClient(
  client: RedisCommandClient,
  token: string,
  leaseMs = redisQueueFenceLeaseMs
): Promise<boolean> {
  assertFenceToken(token)
  const normalizedLeaseMs = normalizeFenceLeaseMs(leaseMs)
  const result = await client.set(redisQueueFenceKey(), token, { NX: true, PX: normalizedLeaseMs })
  return result === 'OK'
}

export async function renewRedisQueueFenceWithClient(
  client: RedisCommandClient,
  token: string,
  leaseMs = redisQueueFenceLeaseMs
): Promise<boolean> {
  assertFenceToken(token)
  const result = await client.eval(redisQueueFenceRenewScript, {
    keys: [redisQueueFenceKey()],
    arguments: [token, String(normalizeFenceLeaseMs(leaseMs))]
  })
  return Number(result ?? 0) === 1
}

export async function releaseRedisQueueFenceWithClient(client: RedisCommandClient, token: string): Promise<boolean> {
  assertFenceToken(token)
  const result = await client.eval(redisQueueFenceReleaseScript, {
    keys: [redisQueueFenceKey()],
    arguments: [token]
  })
  return Number(result ?? 0) === 1
}

export async function releaseRedisQueueFenceIdempotentlyWithClient(client: RedisCommandClient, token: string): Promise<boolean> {
  assertFenceToken(token)
  const result = await client.eval(redisQueueFenceIdempotentReleaseScript, {
    keys: [redisQueueFenceKey()],
    arguments: [token]
  })
  return Number(result ?? 0) === 1
}

function assertFenceToken(token: string): void {
  if (!token.trim()) {
    throw new Error('Redis queue fence token 不能为空')
  }
}

function normalizeFenceLeaseMs(leaseMs: number): number {
  if (!Number.isFinite(leaseMs)) {
    throw new Error('Redis queue fence lease 必须是有限毫秒数')
  }
  return Math.max(1_000, Math.trunc(leaseMs))
}

async function closeDedicatedClient(client: RedisCommandClient): Promise<void> {
  if (typeof client.quit === 'function') {
    await client.quit().catch(() => undefined)
    return
  }
  client.destroy?.()
}
