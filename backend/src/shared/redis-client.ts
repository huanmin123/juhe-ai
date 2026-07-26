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

export interface RedisOperationDeadlineOptions {
  timeoutMs?: number
  deadlineAtMs?: number
  signal?: AbortSignal
  operationName: string
}

export class RedisOperationDeadlineError extends Error {
  constructor(operationName: string) {
    super(`${operationName}超时`)
    this.name = 'RedisOperationDeadlineError'
  }
}

const redisClients = new Map<string, Promise<RedisCommandClient>>()
const redisClientConnectTimeoutMs = 10000
const redisClientCloseTimeoutMs = 2000

export async function getRedisClient(url: string): Promise<RedisCommandClient> {
  const normalizedUrl = normalizeRedisUrl(url)
  const existing = redisClients.get(normalizedUrl)
  if (existing) {
    const client = await existing
    if (client.isOpen !== false && client.isReady !== false) return client
    await invalidateRedisClient(normalizedUrl, client)
    const replacement = redisClients.get(normalizedUrl)
    if (replacement) return await replacement
  }
  const clientPromise = createRedisClient(normalizedUrl).catch((error) => {
    if (redisClients.get(normalizedUrl) === clientPromise) {
      redisClients.delete(normalizedUrl)
    }
    throw error
  })
  clientPromise.catch(() => undefined)
  redisClients.set(normalizedUrl, clientPromise)
  return await clientPromise
}

export async function runRedisOperationWithDeadline<T>(
  url: string,
  options: RedisOperationDeadlineOptions,
  operation: (client: RedisCommandClient) => Promise<T>
): Promise<T> {
  const normalizedUrl = normalizeRedisUrl(url)
  const deadlineAtMs = normalizedRedisOperationDeadlineAt(options)
  let client: RedisCommandClient | undefined
  try {
    client = await awaitRedisOperationStep(
      getRedisClient(normalizedUrl),
      deadlineAtMs,
      options,
      `${options.operationName}连接`
    )
    return await awaitRedisOperationStep(
      operation(client),
      deadlineAtMs,
      options,
      options.operationName
    )
  } catch (error) {
    if (options.signal?.aborted || error instanceof RedisOperationDeadlineError || isRecoverableRedisClientError(error)) {
      client?.destroy?.()
      void invalidateRedisClient(normalizedUrl, client).catch(() => undefined)
    }
    throw error
  }
}

export function hasRedisClient(url: string): boolean {
  return redisClients.has(normalizeRedisUrl(url))
}

export function createDedicatedRedisClient(url: string, options: DedicatedRedisClientOptions = {}): Promise<RedisCommandClient> {
  return createRedisClient(normalizeRedisUrl(url), options)
}

export async function invalidateRedisClient(url: string, expectedClient?: RedisCommandClient): Promise<boolean> {
  const normalizedUrl = normalizeRedisUrl(url)
  const clientPromise = redisClients.get(normalizedUrl)
  if (!clientPromise) return false
  let client: RedisCommandClient
  try {
    client = await withTimeout(clientPromise, redisClientCloseTimeoutMs, 'Redis client invalidation wait timeout')
  } catch {
    if (redisClients.get(normalizedUrl) === clientPromise) {
      redisClients.delete(normalizedUrl)
    }
    return true
  }
  if (expectedClient && client !== expectedClient) return false
  if (redisClients.get(normalizedUrl) !== clientPromise) return false
  redisClients.delete(normalizedUrl)
  client.destroy?.()
  return true
}

export function isRecoverableRedisClientError(error: unknown): boolean {
  const record = typeof error === 'object' && error !== null
    ? error as Record<string, unknown>
    : undefined
  const name = error instanceof Error ? error.name : String(record?.name ?? '')
  const message = error instanceof Error ? error.message : String(error)
  const code = String(record?.code ?? '')
  if (/timeout/i.test(name) || /timeout|timed out/i.test(message)) return true
  if (/^(?:ECONNRESET|ECONNREFUSED|EPIPE|ETIMEDOUT|ENOTCONN|NR_CLOSED)$/i.test(code)) return true
  return /socket closed|client is closed|client is offline|connection (?:closed|lost)|disconnect/i.test(message)
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
    // Redis 承载缓存、运行态和队列协调。断线重连期间保留命令会绕过调用方的有界
    // 重试，并可能在故障期间无限增长；需要重试的调用方必须自行负责。
    disableOfflineQueue: options.disableOfflineQueue ?? true,
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

function normalizedRedisOperationDeadlineAt(options: RedisOperationDeadlineOptions): number {
  const configuredDeadline = Number(options.deadlineAtMs)
  if (Number.isFinite(configuredDeadline) && configuredDeadline > 0) return Math.trunc(configuredDeadline)
  return Date.now() + normalizedPositiveInteger(options.timeoutMs, 3_000)
}

async function awaitRedisOperationStep<T>(
  promise: Promise<T>,
  deadlineAtMs: number,
  options: RedisOperationDeadlineOptions,
  operationName: string
): Promise<T> {
  options.signal?.throwIfAborted()
  const remainingMs = Math.max(0, deadlineAtMs - Date.now())
  if (remainingMs === 0) throw new RedisOperationDeadlineError(operationName)
  let timer: ReturnType<typeof setTimeout> | undefined
  let abortListener: (() => void) | undefined
  const deadline = new Promise<T>((_resolve, reject) => {
    timer = setTimeout(() => reject(new RedisOperationDeadlineError(operationName)), remainingMs)
    timer.unref?.()
    if (options.signal) {
      abortListener = () => reject(options.signal?.reason ?? new Error(`${operationName}已取消`))
      options.signal.addEventListener('abort', abortListener, { once: true })
    }
  })
  try {
    return await Promise.race([promise, deadline])
  } finally {
    if (timer) clearTimeout(timer)
    if (abortListener) options.signal?.removeEventListener('abort', abortListener)
    promise.catch(() => undefined)
  }
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
