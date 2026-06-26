export interface RedisCommandClient {
  isOpen?: boolean
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

const redisClients = new Map<string, Promise<RedisCommandClient>>()

export function getRedisClient(url: string): Promise<RedisCommandClient> {
  const normalizedUrl = normalizeRedisUrl(url)
  const existing = redisClients.get(normalizedUrl)
  if (existing) return existing
  const clientPromise = createRedisClient(normalizedUrl).catch((error) => {
    redisClients.delete(normalizedUrl)
    throw error
  })
  redisClients.set(normalizedUrl, clientPromise)
  return clientPromise
}

export function createDedicatedRedisClient(url: string): Promise<RedisCommandClient> {
  return createRedisClient(normalizeRedisUrl(url))
}

async function createRedisClient(url: string): Promise<RedisCommandClient> {
  const { createClient } = await import('redis')
  const client = createClient({
    url,
    socket: {
      reconnectStrategy: (retries) => Math.min(5000, 250 + retries * 250)
    }
  }) as unknown as RedisCommandClient
  client.on('error', () => {
    // node-redis emits connection errors; command promises still reject for callers.
  })
  await client.connect()
  return client
}

function normalizeRedisUrl(url: string): string {
  const trimmed = url.trim()
  if (!trimmed) {
    throw new Error('Redis URL 不能为空')
  }
  return trimmed
}
