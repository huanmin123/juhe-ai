import assert from 'node:assert/strict'

const postgresURL = requiredEnv('JUHE_AI_OPERATION_LOG_PRODUCER_POSTGRES_URL')
const redisCacheURL = requiredEnv('JUHE_AI_OPERATION_LOG_PRODUCER_REDIS_CACHE_URL')
const redisStateURL = requiredEnv('JUHE_AI_OPERATION_LOG_PRODUCER_REDIS_STATE_URL')
const redisQueueURL = requiredEnv('JUHE_AI_OPERATION_LOG_PRODUCER_REDIS_QUEUE_URL')

process.env.JUHE_AI_RUNTIME_MODE = 'performance'
process.env.JUHE_AI_PERFORMANCE_NODE_ROLE = 'control'
process.env.JUHE_AI_DATABASE_DRIVER = 'postgres'
process.env.JUHE_AI_POSTGRES_URL = postgresURL
process.env.JUHE_AI_POSTGRES_JIT_ENABLED = 'true'
process.env.JUHE_AI_CACHE_DRIVER = 'redis'
process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'redis'
process.env.JUHE_AI_QUEUE_DRIVER = 'redis_stream'
process.env.JUHE_AI_REDIS_CACHE_URL = redisCacheURL
process.env.JUHE_AI_REDIS_STATE_URL = redisStateURL
process.env.JUHE_AI_REDIS_QUEUE_URL = redisQueueURL
process.env.JUHE_AI_REDIS_NAMESPACE = `juhe-ai:f4-producer-regression-${process.pid}`
process.env.JUHE_AI_OPERATION_LOG_INPUT_URL = 'http://127.0.0.1:39991'
process.env.JUHE_AI_OPERATION_LOG_INPUT_SECRET = 'f4-operation-log-performance-producer-regression-secret'
process.env.JUHE_AI_OPERATION_LOG_INPUT_TIMEOUT_MS = '1000'
process.env.JUHE_AI_LOG_FILE_ENABLED = 'false'
process.env.JUHE_AI_LOG_CONSOLE_ENABLED = 'false'
process.env.NODE_ENV = 'test'

const originalFetch = globalThis.fetch
const fetchStarted = deferred<void>()
const releaseFetch = deferred<void>()
const fetchFinished = deferred<void>()
let fetchCalls = 0
let signedRequest = false

globalThis.fetch = (async (_input, init) => {
  fetchCalls += 1
  const headers = new Headers(init?.headers)
  signedRequest = headers.has('x-juhe-ai-signature')
    && headers.has('x-juhe-ai-timestamp')
    && headers.has('x-juhe-ai-nonce')
  fetchStarted.resolve()
  await releaseFetch.promise
  fetchFinished.resolve()
  return new Response(null, { status: 204 })
}) as typeof fetch

try {
  const { logger } = await import('../../shared/logger.js')
  const { closePostgresPool } = await import('../../storage/postgres-client.js')
  const { closeRedisClients } = await import('../../shared/redis-client.js')
  const { getSettingsAsync } = await import('../../storage/settings.repository.js')
  const { runLoggedOperationAsync } = await import('../../modules/operation-logs/operation-log.service.js')
  logger.level = 'silent'

  const settings = await getSettingsAsync()
  assert.equal(typeof settings.operationLogMaxChangesPerRecord, 'number', 'performance settings 必须可通过异步 PostgreSQL/Redis 路径读取')

  let afterCommitRan = false
  const result = await within(
    runLoggedOperationAsync(async () => ({
      result: 'business-committed',
      log: {
        id: 'oplog-performance-producer-regression',
        createdAt: '2026-08-13T00:00:00.000Z',
        actorSystemAccountId: 'sys_admin',
        actorRole: 'admin',
        module: 'settings',
        action: 'update',
        operationKey: 'settings.update',
        resourceType: 'settings',
        summary: 'performance producer regression'
      },
      afterCommit: () => { afterCommitRan = true }
    })),
    1_000,
    '业务操作不应等待被阻塞的 F4 RPC'
  )
  assert.equal(result, 'business-committed')
  assert.equal(afterCommitRan, true, 'afterCommit 必须在业务返回前完成')

  await within(fetchStarted.promise, 10_000, 'performance/PG producer 未经异步 settings 路径发出 F4 RPC')
  assert.equal(fetchCalls, 1, 'F4 producer 只能发送一次 RPC，不得重试或回退')
  assert.equal(signedRequest, true, 'performance/PG producer 必须发出受签名 F4 RPC')
  releaseFetch.resolve()
  await within(fetchFinished.promise, 1_000, 'F4 RPC 释放后未完成')

  await closeRedisClients()
  await closePostgresPool()
  console.log('operation-log-performance-producer-regression passed')
} finally {
  globalThis.fetch = originalFetch
  releaseFetch.resolve()
}

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`${name} is required for the F4 performance producer regression`)
  return value
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => { resolve = resolvePromise })
  return { promise, resolve }
}

async function within<T>(promise: Promise<T>, timeoutMs: number, message: string): Promise<T> {
  let timer: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_resolve, reject) => {
        timer = setTimeout(() => reject(new Error(message)), timeoutMs)
      })
    ])
  } finally {
    if (timer) clearTimeout(timer)
  }
}
