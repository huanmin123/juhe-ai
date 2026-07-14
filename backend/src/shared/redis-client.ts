export interface RedisCommandClient {
  isOpen?: boolean
  isReady?: boolean
  connect(): Promise<unknown>
  get(key: string): Promise<string | null>
  set(key: string, value: string, options?: Record<string, unknown>): Promise<string | null>
  del(key: string): Promise<number>
  eval(script: string, options: { keys: string[]; arguments: string[] }): Promise<unknown>
  sendCommand(command: string[]): Promise<unknown>
  quit?(): Promise<unknown>
  destroy?(): void
  on(event: string, listener: (...args: unknown[]) => void): unknown
}

export interface DedicatedRedisClientOptions {
  disableOfflineQueue?: boolean
  commandsQueueMaxLength?: number
  connectTimeoutMs?: number
}

const redisClients = new Map<string, Promise<RedisCommandClient>>()
const redisClientConnectTimeoutMs = 10000
const redisClientCloseTimeoutMs = 2000

export function getRedisClient(url: string): Promise<RedisCommandClient> {
  const normalizedUrl = normalizeRedisUrl(url)
  const existing = redisClients.get(normalizedUrl)
  if (existing) return existing
  const clientPromise = createRedisClient(normalizedUrl).catch((error) => {
    redisClients.delete(normalizedUrl)
    throw error
  })
  clientPromise.catch(() => undefined)
  redisClients.set(normalizedUrl, clientPromise)
  return clientPromise
}

export function hasRedisClient(url: string): boolean {
  return redisClients.has(normalizeRedisUrl(url))
}

export function createDedicatedRedisClient(url: string, options: DedicatedRedisClientOptions = {}): Promise<RedisCommandClient> {
  return createRedisClient(normalizeRedisUrl(url), options)
}

export async function closeRedisClients(): Promise<void> {
  const clientPromises = Array.from(redisClients.values())
  redisClients.clear()
  const settledClients = await Promise.allSettled(
    clientPromises.map((clientPromise) =>
      withTimeout(clientPromise, redisClientCloseTimeoutMs, 'Redis client close wait timeout')
    )
  )
  for (const settledClient of settledClients) {
    if (settledClient.status !== 'fulfilled') continue
    await closeRedisClient(settledClient.value)
  }
}

async function closeRedisClient(client: RedisCommandClient): Promise<void> {
  if (!client.quit) {
    client.destroy?.()
    return
  }
  const timedOut = Symbol('redis-close-timeout')
  const result = await Promise.race([
    client.quit().then(() => undefined, () => undefined),
    timeoutResult(timedOut, redisClientCloseTimeoutMs)
  ])
  if (result === timedOut) {
    client.destroy?.()
  }
}

async function createRedisClient(url: string, options: DedicatedRedisClientOptions = {}): Promise<RedisCommandClient> {
  const { createClient } = await import('redis')
  const connectTimeoutMs = normalizedPositiveInteger(options.connectTimeoutMs, redisClientConnectTimeoutMs)
  const commandsQueueMaxLength = options.commandsQueueMaxLength === undefined
    ? undefined
    : normalizedPositiveInteger(options.commandsQueueMaxLength, 1)
  const client = createClient({
    url,
    ...(options.disableOfflineQueue === true ? { disableOfflineQueue: true } : {}),
    ...(commandsQueueMaxLength === undefined ? {} : { commandsQueueMaxLength }),
    socket: {
      connectTimeout: connectTimeoutMs,
      reconnectStrategy: (retries) => Math.min(5000, 250 + retries * 250)
    }
  }) as unknown as RedisCommandClient
  client.on('error', () => {
    // node-redis emits connection errors; command promises still reject for callers.
  })
  try {
    await withTimeout(client.connect(), connectTimeoutMs, 'Redis connect timeout')
  } catch (error) {
    client.destroy?.()
    throw error
  }
  return client
}

function normalizedPositiveInteger(value: number | undefined, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value)
    ? Math.max(1, Math.trunc(value))
    : fallback
}

async function withTimeout<T>(promise: Promise<T>, timeoutMs: number, message: string): Promise<T> {
  let timeout: ReturnType<typeof setTimeout> | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_resolve, reject) => {
        timeout = setTimeout(() => reject(new Error(message)), timeoutMs)
      })
    ])
  } finally {
    if (timeout) {
      clearTimeout(timeout)
    }
    promise.catch(() => undefined)
  }
}

function timeoutResult<T>(value: T, timeoutMs: number): Promise<T> {
  return new Promise((resolve) => {
    const timeout = setTimeout(() => {
      resolve(value)
    }, timeoutMs)
    timeout.unref?.()
  })
}

function normalizeRedisUrl(url: string): string {
  const trimmed = url.trim()
  if (!trimmed) {
    throw new Error('Redis URL 不能为空')
  }
  return trimmed
}
