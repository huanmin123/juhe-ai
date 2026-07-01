import assert from 'node:assert/strict'
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

interface CacheHit {
  file: string
  line: number
  variableName: string
}

const scriptDir = dirname(fileURLToPath(import.meta.url))
const backendRoot = resolve(scriptDir, '../../..')
const srcRoot = resolve(backendRoot, 'src')

const localOnlyAppCaches = new Map<string, Set<string>>([
  ['modules/gateway/client-profiles/codex-turn-retry.service.ts', new Set(['codexTurnRetryCache'])],
  ['modules/gateway/hybrid/affinity.service.ts', new Set(['hybridRouteAffinityBindings'])],
  ['modules/gateway/runtime/client-ip-account-avoidance.service.ts', new Set(['clientIpAccountAvoidanceCache'])],
  ['modules/gateway/runtime/client-ip-error-circuit.service.ts', new Set(['preAuthCache', 'clientIpErrorCircuitCache'])],
  ['modules/gateway/runtime/proxy-health.service.ts', new Set(['upstreamBucketFailureCache'])],
  ['modules/gateway/runtime/runtime-cache.service.ts', new Set(['gatewayRuntimeCache', 'openAIAccountsCache'])],
  ['modules/gateway/runtime/session-affinity.service.ts', new Set(['sessionAffinityCache', 'trafficMigrationPreferenceCache'])],
  ['modules/gateway/upstream/request.ts', new Set(['proxyAgents'])],
  ['modules/openai-oauth/openai-oauth-access-token-refresh.service.ts', new Set(['recentRefreshByAccountId'])]
])

const queueContracts = [
  {
    file: 'modules/gateway/usage/record-queue.service.ts',
    functionName: 'shouldEnqueueUsageRecordToRedisStream',
    startConsumer: 'startUsageRecordRedisStreamConsumer'
  },
  {
    file: 'modules/audit-logs/audit-log-queue.service.ts',
    functionName: 'shouldEnqueueAuditLogToRedisStream',
    startConsumer: 'startAuditLogRedisStreamConsumer'
  },
  {
    file: 'modules/operation-logs/operation-log-queue.service.ts',
    functionName: 'shouldEnqueueOperationLogToRedisStream',
    startConsumer: 'startOperationLogRedisStreamConsumer'
  },
  {
    file: 'modules/public-api-logs/public-api-log-queue.service.ts',
    functionName: 'shouldEnqueuePublicApiLogToRedisStream',
    startConsumer: 'startPublicApiLogRedisStreamConsumer'
  },
  {
    file: 'modules/record-maintenance/record-maintenance-queue.service.ts',
    functionName: 'shouldEnqueueRecordMaintenanceJobToRedisStream',
    startConsumer: 'startRecordMaintenanceRedisStreamConsumer'
  },
  {
    file: 'modules/runtime-logs/runtime-log-index-queue.service.ts',
    functionName: 'shouldEnqueueRuntimeLogToRedisStream',
    startConsumer: 'startRuntimeLogRedisStreamConsumer'
  }
]

const runtimeSource = source('config/runtime.ts')
assert.match(runtimeSource, /configuredRuntimeMode === 'performance'\s*\?\s*'redis'\s*:\s*'memory'/, 'performance 模式默认 cache driver 必须是 redis')
assert.match(runtimeSource, /configuredRuntimeMode === 'performance'\s*\?\s*'redis'\s*:\s*'memory'/, 'performance 模式默认 runtime state driver 必须是 redis')
assert.match(runtimeSource, /configuredRuntimeMode === 'performance'\s*\?\s*'redis_stream'\s*:\s*'memory'/, 'performance 模式默认 queue driver 必须是 redis_stream')
assert.match(runtimeSource, /JUHE_AI_RUNTIME_MODE=performance 时 JUHE_AI_CACHE_DRIVER 必须为 redis/, 'performance 模式必须拒绝 memory cache driver')
assert.match(runtimeSource, /JUHE_AI_RUNTIME_MODE=performance 时 JUHE_AI_RUNTIME_STATE_DRIVER 必须为 redis/, 'performance 模式必须拒绝 memory runtime state driver')
assert.match(runtimeSource, /JUHE_AI_RUNTIME_MODE=performance 时 JUHE_AI_QUEUE_DRIVER 必须为 redis_stream/, 'performance 模式必须拒绝 memory queue driver')
const cacheSource = source('shared/cache.ts')
assert.match(cacheSource, /export function canUseProcessLocalAppCacheAsFactSource\(\): boolean \{[\s\S]*runtimeConfig\.cacheDriver !== 'redis'/, 'performance 模式不得把进程内 AppCache 当跨进程事实源')
assert.match(cacheSource, /export function createSharedJsonCache[\s\S]*new DriverSharedJsonCache/, 'SharedJsonCache 必须通过 driver wrapper 按运行模式选择底座')
assert.match(cacheSource, /private cache\(\): SharedJsonCache<V> \{[\s\S]*runtimeConfig\.cacheDriver !== 'redis'[\s\S]*return this\.memoryCache[\s\S]*new RedisSharedJsonCache/, 'SharedJsonCache 在 Redis cache driver 下必须直接使用 Redis')
assert.match(cacheSource, /class RedisSharedJsonCache[\s\S]*runtimeConfig\.redis\.cacheUrl/, 'Redis shared cache 必须使用 JUHE_AI_REDIS_CACHE_URL')
assert.match(cacheSource, /throw new Error\('JUHE_AI_REDIS_CACHE_URL 在 Redis cache driver 下必须配置'\)/, 'Redis shared cache 缺少 URL 时必须 fail-fast')

const runtimeStateSource = source('shared/runtime-state-store.ts')
assert.match(runtimeStateSource, /export function createRuntimeStateStore[\s\S]*runtimeConfig\.runtimeStateDriver === 'redis'[\s\S]*new RedisRuntimeStateStore/, 'RuntimeStateStore 在 Redis state driver 下必须直接使用 Redis')
assert.match(runtimeStateSource, /class RedisRuntimeStateStore[\s\S]*runtimeConfig\.redis\.stateUrl/, 'Redis runtime state 必须使用 JUHE_AI_REDIS_STATE_URL')
assert.match(runtimeStateSource, /throw new Error\('JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置'\)/, 'Redis runtime state 缺少 URL 时必须 fail-fast')
assert.match(runtimeStateSource, /eval\(incrWithMaxScript/, '运行态计数必须使用 Redis Lua 保证跨进程原子性')
assert.match(runtimeStateSource, /eval\(releaseLockScript/, '运行态锁释放必须使用 Redis Lua 校验 token')

const redisStreamQueueSource = source('shared/redis-stream-queue.ts')
assert.match(redisStreamQueueSource, /redisUrl/, 'Redis Stream 队列必须显式持有 Redis queue URL')
assert.match(redisStreamQueueSource, /this\.redisUrl = options\.redisUrl \?\? requiredRedisQueueUrl\(\)/, 'Redis Stream 队列必须从 JUHE_AI_REDIS_QUEUE_URL 解析默认连接')
assert.match(redisStreamQueueSource, /createDedicatedRedisClient\(this\.redisUrl\)/, 'Redis Stream consumer 必须通过专用 Redis queue URL 建立连接')

const backgroundIpcSource = source('modules/background/background-ipc.ts')
assert.match(backgroundIpcSource, /function isRedisStreamManagedIngestQueueMessage[\s\S]*background_worker_usage_records[\s\S]*background_worker_runtime_log_line/, 'Redis Stream 管理的记录类消息必须集中识别')
assert.match(backgroundIpcSource, /function queueWorkerMessage[\s\S]*runtimeConfig\.queueDriver === 'redis_stream'[\s\S]*isRedisStreamManagedIngestQueueMessage/, 'redis_stream driver 下禁止记录类消息进入后台 IPC 本地队列')
assert.match(backgroundIpcSource, /function requeueIngestWorkerMessageFirst[\s\S]*runtimeConfig\.queueDriver === 'redis_stream'[\s\S]*isRedisStreamManagedIngestQueueMessage/, 'redis_stream driver 下发送失败不能把记录类消息 requeue 回本地 IPC 队列')
assert.match(backgroundIpcSource, /function sendBackgroundWorkerMessageToParent[\s\S]*runtimeConfig\.queueDriver === 'redis_stream'[\s\S]*isRedisStreamManagedIngestQueueMessage/, 'redis_stream driver 下非 ingest worker 不能把记录类消息发回父进程 IPC')

const dbServiceIpcSource = source('modules/db-service/db-service-ipc.ts')
assert.match(dbServiceIpcSource, /function rejectRedisStreamLocalQueueForward[\s\S]*runtimeConfig\.queueDriver !== 'redis_stream'[^]*return false[\s\S]*db_service_redis_stream_local_queue_forward_rejected/, 'redis_stream driver 下 DB service 父消息不能转发记录类 IPC 本地队列')
for (const functionName of [
  'forwardUsageRecordsToWorker',
  'forwardAuditLogsToWorker',
  'forwardOperationLogsToWorker',
  'forwardPublicApiLogsToWorker',
  'forwardRuntimeLogLineToWorker',
  'forwardRecordMaintenanceJobsToWorker'
]) {
  assert.match(
    functionBody(dbServiceIpcSource, functionName),
    /rejectRedisStreamLocalQueueForward/,
    `${functionName} 必须在 redis_stream driver 下拒绝本地 IPC 转发`
  )
}

const accountConcurrencySource = source('shared/account-concurrency.ts')
assert.match(accountConcurrencySource, /runtimeConfig\.runtimeStateDriver === 'memory'[\s\S]*tryAcquireAccountConcurrency\(accountId, concurrencyLimit, options\)/, '账号并发 memory 分支只能作为 standalone 本地实现')
assert.match(accountConcurrencySource, /acquireRedisAccountConcurrency\(accountId, limit, lane, laneLimit, redisToken\)/, '账号并发在 Redis runtime state driver 下必须使用 Redis 原子占用')
assert.match(accountConcurrencySource, /redisStateClient\(\)/, '账号并发读取在 Redis runtime state driver 下必须读取 Redis state')
assert.match(accountConcurrencySource, /account-concurrency-v2/, 'Redis 账号并发必须使用带槽位租约的 v2 key，避免旧数字计数脏占用')
assert.match(accountConcurrencySource, /redisAccountConcurrencySlotLeaseTtlMs\s*=\s*90_000/, 'Redis 账号并发槽必须使用短租约，释放失败或进程退出后应及时过期')
assert.match(accountConcurrencySource, /function ensureRedisAccountConcurrencySlotRefresh\(\)/, 'Redis 账号并发活跃槽必须由当前进程定期续租')
assert.match(accountConcurrencySource, /ZREMRANGEBYSCORE/, 'Redis 账号并发读取和占用前必须清理已过租约的槽位')
assert.doesNotMatch(accountConcurrencySource, /redis\.call\('INCR'/, 'Redis 账号并发不能回退简单 INCR 计数，否则进程崩溃会残留脏并发')
assert.doesNotMatch(accountConcurrencySource, /redis\.call\('DECR'/, 'Redis 账号并发不能回退简单 DECR 释放，否则无法按请求槽位精确归还')

assertRuntimeStateStoreCallsites()
assertQueueContracts()
assertAppCacheClassification()

console.log('performance-redis-boundary-regression passed')

function assertRuntimeStateStoreCallsites(): void {
  const files = listSourceFiles(srcRoot)
  const hits: Array<{ file: string; line: number; name: string }> = []
  for (const filePath of files) {
    const relativePath = slash(relative(srcRoot, filePath))
    if (relativePath.startsWith('scripts/') || relativePath === 'shared/runtime-state-store.ts') continue
    const lines = readFileSync(filePath, 'utf8').split(/\r?\n/)
    for (let index = 0; index < lines.length; index += 1) {
      const match = lines[index].match(/createRuntimeStateStore\(['"]([^'"]+)['"]\)/)
      if (!match) continue
      hits.push({ file: relativePath, line: index + 1, name: match[1] })
    }
  }
  assert.deepEqual(
    hits.map((hit) => `${hit.file}:${hit.name}`).sort(),
    [
      'modules/auth/captcha.service.ts:auth_captcha',
      'modules/auth/login-guard.service.ts:auth_login_guard',
      'shared/gateway-cache-invalidation.ts:gateway_cache_invalidation'
    ].sort(),
    `新增 RuntimeStateStore 调用点必须确认 performance 模式走 Redis，不得新增进程内跨进程事实源：${JSON.stringify(hits, null, 2)}`
  )
}

function assertQueueContracts(): void {
  const workerSource = source('worker.ts')
  for (const contract of queueContracts) {
    const content = source(contract.file)
    assert.match(content, new RegExp(`function ${contract.functionName}`), `${contract.file} 必须声明 Redis Stream 入队判定函数`)
    assert.match(content, /runtimeConfig\.queueDriver === 'redis_stream'/, `${contract.file} 必须在 redis_stream driver 下写 Redis Stream`)
    assert.match(content, new RegExp(contract.startConsumer), `${contract.file} 必须暴露 Redis Stream consumer`)
    assert.match(content, /queue\.ack\(/, `${contract.file} 必须只在成功消费后 ack Redis Stream 消息`)
    assert.match(content, /catch\(scheduleProcessFatalError\)/, `${contract.file} 同步入口 Redis Stream 入队失败必须进入受控 fail-fast，不能退化为未处理 Promise`)
    assert.doesNotMatch(content, /AfterRedisStreamFailure/, `${contract.file} Redis Stream 入队失败后不能回退到进程内队列作为事实源`)
    assert.doesNotMatch(content, /shouldEnqueue\w+ToRedisStream[\s\S]*&&\s*!is\w+IngestWorker/, `${contract.file} redis_stream driver 下不能因为当前是 ingest-worker 而绕过 Redis Stream`)
    assert.match(content, /Redis Stream queue driver 下禁止写入/, `${contract.file} redis_stream driver 下必须禁止写入本地队列`)
    assert.match(workerSource, new RegExp(`${contract.startConsumer}\\(\\)`), `ingest-worker 必须启动 ${contract.startConsumer}`)
  }
  assert.match(workerSource, /assertLocalQueueIpcAllowed\(message\)/, 'worker 收到本地队列 IPC 消息前必须检查 redis_stream driver 边界')
  assert.match(workerSource, /Redis Stream queue driver 下禁止消费后台 IPC 本地队列消息/, 'redis_stream driver 下 worker 不能消费 IPC 本地队列消息')
}

function assertAppCacheClassification(): void {
  const files = listSourceFiles(srcRoot)
  const appCacheHits = collectCacheHits(files, /const\s+(\w+)\s*=\s*createAppCache/g)
    .filter((hit) => !hit.file.startsWith('scripts/') && hit.file !== 'shared/cache.ts')
  const sharedCacheFiles = new Set(
    collectCacheHits(files, /const\s+(\w+)\s*=\s*createSharedJsonCache/g)
      .filter((hit) => !hit.file.startsWith('scripts/') && hit.file !== 'shared/cache.ts')
      .map((hit) => hit.file)
  )
  const unclassified = appCacheHits.filter((hit) => {
    if (sharedCacheFiles.has(hit.file)) return false
    return !(localOnlyAppCaches.get(hit.file)?.has(hit.variableName))
  })
  assert.deepEqual(
    unclassified,
    [],
    '新增 createAppCache 调用必须说明边界：要么同文件有 Redis SharedJsonCache 作为跨进程事实源，要么加入本脚本 localOnlyAppCaches 并证明只是进程内易失优化'
  )
}

function collectCacheHits(files: string[], pattern: RegExp): CacheHit[] {
  const hits: CacheHit[] = []
  for (const filePath of files) {
    const file = slash(relative(srcRoot, filePath))
    const lines = readFileSync(filePath, 'utf8').split(/\r?\n/)
    for (let index = 0; index < lines.length; index += 1) {
      pattern.lastIndex = 0
      const match = pattern.exec(lines[index])
      if (!match) continue
      hits.push({ file, line: index + 1, variableName: match[1] })
    }
  }
  return hits
}

function listSourceFiles(root: string): string[] {
  const output: string[] = []
  const walk = (directory: string): void => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const fullPath = resolve(directory, entry.name)
      if (entry.isDirectory()) {
        if (entry.name === 'dist' || entry.name === 'node_modules') continue
        walk(fullPath)
        continue
      }
      if (entry.isFile() && entry.name.endsWith('.ts')) output.push(fullPath)
    }
  }
  walk(root)
  return output.sort()
}

function source(path: string): string {
  return readFileSync(resolve(srcRoot, path), 'utf8')
}

function functionBody(sourceText: string, functionName: string): string {
  const start = sourceText.indexOf(`function ${functionName}`)
  assert(start >= 0, `缺少函数 ${functionName}`)
  const parametersStart = sourceText.indexOf('(', start)
  assert(parametersStart >= 0, `函数 ${functionName} 缺少参数列表`)
  let parameterDepth = 0
  let parametersEnd = -1
  for (let index = parametersStart; index < sourceText.length; index += 1) {
    const char = sourceText[index]
    if (char === '(') parameterDepth += 1
    if (char === ')') {
      parameterDepth -= 1
      if (parameterDepth === 0) {
        parametersEnd = index
        break
      }
    }
  }
  assert(parametersEnd >= 0, `函数 ${functionName} 参数列表未闭合`)
  const openBrace = sourceText.indexOf('{', parametersEnd)
  assert(openBrace >= 0, `函数 ${functionName} 缺少函数体`)
  let depth = 0
  for (let index = openBrace; index < sourceText.length; index += 1) {
    const char = sourceText[index]
    if (char === '{') depth += 1
    if (char === '}') {
      depth -= 1
      if (depth === 0) {
        return sourceText.slice(openBrace, index + 1)
      }
    }
  }
  throw new Error(`函数 ${functionName} 函数体解析失败`)
}

function slash(path: string): string {
  return path.replace(/\\/g, '/')
}
