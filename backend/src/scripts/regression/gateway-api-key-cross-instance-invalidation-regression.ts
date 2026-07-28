import assert from 'node:assert/strict'
import { fork } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

if (process.env.JUHE_GATEWAY_API_KEY_INVALIDATION_CHILD === '1') {
  await runDbServiceInvalidationChild()
  process.exit(0)
}

const repositorySource = readFileSync(new URL('../../storage/gateway-api-key.repository.ts', import.meta.url), 'utf8')
const invalidationSource = readFileSync(new URL('../../shared/gateway-cache-invalidation.ts', import.meta.url), 'utf8')
const dbServiceIpcSource = readFileSync(new URL('../../modules/db-service/db-service-ipc.ts', import.meta.url), 'utf8')
const dbServiceTypesSource = readFileSync(new URL('../../modules/db-service/db-service-types.ts', import.meta.url), 'utf8')
const runtimeCacheSource = readFileSync(new URL('../../modules/gateway/runtime/runtime-cache.service.ts', import.meta.url), 'utf8')
const preAuthSource = readFileSync(new URL('../../modules/gateway/request/pre-auth.ts', import.meta.url), 'utf8')
const body = functionBody(repositorySource, 'validateGatewayApiKeyAsync')
const synchronization = body.indexOf("await syncGatewayCacheInvalidationsFromRuntimeState({ force: true })")
const processCacheRead = body.indexOf('gatewayApiKeyProcessCache.get(keyHash)')

assert(synchronization >= 0, '跨实例 API Key 鉴权必须强制同步 Redis runtime state 失效版本')
assert(processCacheRead >= 0, 'API Key 异步鉴权应保留进程内热缓存')
assert(synchronization < processCacheRead, '必须先同步失效版本，再读取进程内 API Key 热缓存')
assert.doesNotMatch(
  body,
  /void syncGatewayCacheInvalidationsFromRuntimeState\(\)\.catch\(\(\) => undefined\)/,
  'API Key 鉴权不得在已命中本地缓存后异步同步失效版本'
)
assert(body.includes('for (let attempt = 0; attempt < gatewayApiKeyValidationAttemptLimit; attempt += 1)'), 'API Key 异步鉴权应为失效竞态提供有界重试')
assertOrdered(body, [
  'const sharedCached = await getGatewayApiKeySharedCacheEntry(keyHash)',
  'await syncGatewayApiKeyValidationGenerationAfterAsyncRead(generation)',
  'if (!isGatewayApiKeyValidationGenerationCurrent(generation)) continue',
  'gatewayApiKeyProcessCache.set(keyHash'
], '共享缓存读取后必须确认失效代际，才能回填进程内缓存')
assertOrdered(body, [
  'row.group_bindings = await loadActiveGatewayApiKeyGroupBindingsAsync',
  'await syncGatewayApiKeyValidationGenerationAfterAsyncRead(generation)',
  'if (!isGatewayApiKeyValidationGenerationCurrent(generation)) continue',
  'expectedGeneration: generation'
], '数据库异步读取后必须确认失效代际，并把同一代际传到缓存写入围栏')
assertOrdered(body, [
  'for (let attempt = 0; attempt < gatewayApiKeyValidationAttemptLimit; attempt += 1)',
  'return await loadGatewayApiKeyForValidationAuthoritativelyAsync(keyHash)'
], 'PostgreSQL 连续代际冲突耗尽后必须执行不经过缓存的权威读取，不能把有效 Key 误判为 401')
const authoritativePostgresBody = functionBody(repositorySource, 'loadGatewayApiKeyForValidationAuthoritativelyAsync')
assert.match(authoritativePostgresBody, /loadGatewayApiKeyBaseRowAsync\(keyHash, client\)/, 'PostgreSQL 权威兜底必须直接回源 API Key 行')
assert.doesNotMatch(authoritativePostgresBody, /gatewayApiKey(?:Process|Shared)?Cache|setGatewayApiKeyCacheEntry/, 'PostgreSQL 权威兜底不得读取或回填失效竞态中的缓存')

const sqliteWorkerBody = functionBody(repositorySource, 'validateGatewayApiKeyWithSqliteReadWorker')
assert.match(sqliteWorkerBody, /gatewayApiKeyValidationAttemptLimit/, 'SQLite read worker 鉴权也必须有失效竞态有界重试')
assertOrdered(sqliteWorkerBody, [
  'const row = await requestSqliteReadWorker',
  'await syncGatewayApiKeyValidationGenerationAfterAsyncRead(generation)',
  'if (!isGatewayApiKeyValidationGenerationCurrent(generation)) continue',
  'expectedGeneration: generation'
], 'SQLite read worker 返回旧读时不得越过失效代际写回缓存')
assertOrdered(sqliteWorkerBody, [
  'for (let attempt = 0; attempt < gatewayApiKeyValidationAttemptLimit; attempt += 1)',
  'return await loadGatewayApiKeyForValidationWithSqliteReadWorkerAuthoritativelyAsync(key)'
], 'SQLite 连续代际冲突耗尽后必须执行 read worker 权威读取，不能把有效 Key 误判为 401')
const authoritativeSqliteBody = functionBody(repositorySource, 'loadGatewayApiKeyForValidationWithSqliteReadWorkerAuthoritativelyAsync')
assert.match(authoritativeSqliteBody, /requestSqliteReadWorker\(/, 'SQLite 权威兜底必须继续使用 query-only read worker')
assert.doesNotMatch(authoritativeSqliteBody, /gatewayApiKey(?:Process|Shared)?Cache|setGatewayApiKeyCacheEntry/, 'SQLite 权威兜底不得读取或回填失效竞态中的缓存')

const setCacheBody = functionBody(repositorySource, 'setGatewayApiKeyCacheEntryAsync')
assertOrdered(setCacheBody, [
  '!isGatewayApiKeyValidationGenerationCurrent(options.expectedGeneration)',
  'await setGatewayApiKeySharedCacheEntry(keyHash, entry, options)',
  'await syncGatewayApiKeyValidationGenerationAfterAsyncRead(options.expectedGeneration)',
  'await clearGatewayApiKeySharedCacheAsync()',
  'gatewayApiKeyProcessCache.set(keyHash'
], 'API Key 缓存写入必须在 Redis 写前后检查代际，失效竞态时清除旧共享写入且禁止本地回填')
assert.match(functionBody(repositorySource, 'clearGatewayApiKeyValidationCache'), /advanceGatewayApiKeyValidationCacheGeneration()/, '全量失效必须推进 API Key 校验代际')
assert.match(functionBody(repositorySource, 'invalidateGatewayApiKeyCacheByIdAsync'), /advanceGatewayApiKeyValidationCacheGeneration()/, '定点异步失效必须推进 API Key 校验代际')
assert.match(functionBody(repositorySource, 'prewarmGatewayApiKeyValidationCacheAsync'), /expectedGeneration: generation/, '启动预热也必须遵守同一失效代际围栏')

const notifyBody = functionBody(invalidationSource, 'notifyGatewayApiKeyValidationCacheInvalidationAsync')
assert.match(notifyBody, /runtimeConfig.runtimeStateDriver !== 'redis'/, 'memory runtime-state 必须进入 server IPC 热推送分支')
assert.match(notifyBody, /runtimeConfig.processRole === 'db-service'/, 'server IPC 热推送应由执行管理写的 DB service 发起')
assert(notifyBody.includes('await gatewayApiKeyValidationServerInvalidator(apiKeyId, keyHashes)'), '管理写返回前必须等待 server API Key 缓存失效确认')
assert.match(invalidationSource, /applyGatewayApiKeyValidationCacheInvalidationFromIpcAsync/, 'server 必须有专用 API Key validation cache IPC 应用入口')
assert(dbServiceIpcSource.includes('registerGatewayApiKeyValidationServerInvalidator(requestServerGatewayApiKeyCacheInvalidationAsync)'), 'DB service IPC 必须注册 memory runtime-state publisher')
assert.match(dbServiceIpcSource, /db_service_gateway_api_key_cache_invalidation_request/, 'DB service 必须发送专用 API Key 缓存失效请求')
assert.match(dbServiceIpcSource, /db_service_gateway_api_key_cache_invalidation_response/, 'server 必须回传 API Key 缓存失效确认')
assert(dbServiceIpcSource.includes('await applyGatewayApiKeyValidationCacheInvalidationFromIpcAsync(apiKeyId, keyHashes)'), 'server 回执前必须完成本地 API Key 校验和运行时缓存清理')
assert.match(dbServiceTypesSource, /type: 'db_service_gateway_api_key_cache_invalidation_request'/, 'IPC 类型必须声明 API Key 缓存失效请求')
assert.match(dbServiceTypesSource, /type: 'db_service_gateway_api_key_cache_invalidation_response'/, 'IPC 类型必须声明 API Key 缓存失效回执')

const runtimeReadBody = functionBody(runtimeCacheSource, 'readCachedGatewayRuntimeAsync')
const runtimeSynchronization = runtimeReadBody.indexOf("await syncGatewayCacheInvalidationsFromRuntimeState({ force: true })")
const runtimeProcessCacheRead = runtimeReadBody.indexOf('gatewayRuntimeCache.get(cacheKey)')
assert(runtimeSynchronization >= 0, '普通网关鉴权也必须强制同步 Redis API Key 失效版本')
assert(runtimeProcessCacheRead > runtimeSynchronization, '普通网关鉴权必须先同步失效版本，再信任 runtime 快照')
assert.doesNotMatch(runtimeReadBody, /syncGatewayCacheInvalidationsBestEffort/, '普通网关鉴权不得吞掉 Redis 同步失败并继续放行旧 runtime')
assert.match(functionBody(preAuthSource, 'resolveGatewayRuntimeAsync'), /await readCachedGatewayRuntimeAsync\(gatewayApiKey\)/, '普通 pre-auth 必须走受严格失效同步保护的 runtime 读取')
assert.doesNotMatch(runtimeCacheSource, /gatewayRuntimeKeyGenerationCache/, '运行时旧读围栏不得依赖可淘汰的 per-key LRU marker')
assert.match(
  functionBody(runtimeCacheSource, 'invalidateGatewayRuntimeCacheByApiKeyId'),
  /gatewayApiKeyRuntimeCacheGeneration \+= 1[\s\S]*pendingGatewayRuntimeLoads\.delete\(cacheKey\)/,
  '定点 API Key 失效必须先推进不可淘汰 epoch，再删除目标 pending load'
)

await assertIpcInvalidationAwaitsHandlers()
await assertMemoryRuntimeStateCrossProcessInvalidation()

console.log('跨实例 API Key 失效回归通过：Redis 读取前确认版本、memory DB service 等待 server 回执，异步旧读受代际围栏保护')

async function assertIpcInvalidationAwaitsHandlers(): Promise<void> {
  const invalidation = await import('../../shared/gateway-cache-invalidation.js')
  let release: (() => void) | undefined
  let completed = false
  const pending = new Promise<void>((resolve) => {
    release = resolve
  })
  const unregister = invalidation.registerGatewayApiKeyValidationCacheInvalidator(async (apiKeyId, metadata) => {
    if (apiKeyId !== 'key_ipc_regression') return
    assert.deepEqual(metadata, {
      source: 'local',
      keyHashes: ['hash-before', 'hash-after']
    }, 'IPC 应保留 API Key ID 和新旧 key hash')
    await pending
    completed = true
  })
  try {
    const applying = invalidation.applyGatewayApiKeyValidationCacheInvalidationFromIpcAsync(
      'key_ipc_regression',
      ['hash-before', 'hash-after']
    )
    await Promise.resolve()
    assert.equal(completed, false, 'server IPC 回执不能早于异步 invalidator 完成')
    release?.()
    await applying
    assert.equal(completed, true, 'server IPC 应等待异步 invalidator 完成')
  } finally {
    unregister()
  }
}

async function assertMemoryRuntimeStateCrossProcessInvalidation(): Promise<void> {
  const { runtimeConfig } = await import('../../config/runtime.js')
  const invalidation = await import('../../shared/gateway-cache-invalidation.js')
  const dbServiceIpc = await import('../../modules/db-service/db-service-ipc.js')
  runtimeConfig.processRole = 'server'
  runtimeConfig.runtimeStateDriver = 'memory'

  let observedMetadata: unknown
  let childOutput = ''
  const unregister = invalidation.registerGatewayApiKeyValidationCacheInvalidator(async (apiKeyId, metadata) => {
    if (apiKeyId !== 'key_cross_process_regression') return
    await new Promise<void>((resolve) => setTimeout(resolve, 25))
    observedMetadata = metadata
  })
  const child = fork(fileURLToPath(import.meta.url), [], {
    execArgv: process.execArgv,
    env: {
      ...process.env,
      JUHE_GATEWAY_API_KEY_INVALIDATION_CHILD: '1',
      JUHE_AI_RUNTIME_MODE: 'standalone',
      JUHE_AI_PROCESS_ROLE: 'db-service',
      JUHE_AI_DATABASE_DRIVER: 'sqlite',
      JUHE_AI_CACHE_DRIVER: 'memory',
      JUHE_AI_RUNTIME_STATE_DRIVER: 'memory'
    },
    stdio: ['ignore', 'pipe', 'pipe', 'ipc']
  })
  let listenerReadyResolve: (() => void) | undefined
  const listenerReady = new Promise<void>((resolve) => { listenerReadyResolve = resolve })
  const collectChildOutput = (chunk: unknown): void => {
    childOutput += String(chunk)
    if (childOutput.includes('gateway api key invalidation child listener ready')) listenerReadyResolve?.()
  }
  child.stdout?.on('data', collectChildOutput)
  child.stderr?.on('data', collectChildOutput)
  try {
    dbServiceIpc.attachDbServiceProcess(child)
    let readyResolve: (() => void) | undefined
    let completedResolve: (() => void) | undefined
    const ready = new Promise<void>((resolve) => { readyResolve = resolve })
    const completed = new Promise<void>((resolve) => { completedResolve = resolve })
    const failed = new Promise<never>((_resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error(`跨进程 API Key 失效回归超时：${childOutput}`)), 10_000)
      child.on('message', (message: unknown) => {
        if (!isRecord(message)) return
        if (message.type === 'gateway_api_key_invalidation_regression_ready') readyResolve?.()
        if (message.type === 'gateway_api_key_invalidation_regression_completed') {
          clearTimeout(timeout)
          completedResolve?.()
        }
      })
      child.once('exit', (code) => {
        if (code === 0 && observedMetadata) return
        clearTimeout(timeout)
        reject(new Error(`跨进程 API Key 失效子进程异常退出 ${code ?? 'unknown'}：${childOutput}`))
      })
    })
    await Promise.race([listenerReady, failed])
    child.send({ type: 'gateway_api_key_invalidation_regression_bootstrap' })
    await Promise.race([ready, failed])
    child.send({ type: 'gateway_api_key_invalidation_regression_start' })
    await Promise.race([completed, failed])
    assert.deepEqual(observedMetadata, {
      source: 'local',
      keyHashes: ['hash-before-cross-process', 'hash-after-cross-process']
    }, 'memory runtime-state 下 server 应在 DB service 收到回执前执行专用失效 handler')
  } finally {
    unregister()
    if (!child.killed && child.exitCode === null) child.kill()
  }
}

async function runDbServiceInvalidationChild(): Promise<void> {
  const bootstrap = waitForParentMessage('gateway_api_key_invalidation_regression_bootstrap', '等待跨进程 API Key 失效回归 bootstrap 超时')
  process.stderr.write('gateway api key invalidation child listener ready\n')
  await bootstrap
  const { runtimeConfig } = await import('../../config/runtime.js')
  runtimeConfig.processRole = 'db-service'
  runtimeConfig.runtimeStateDriver = 'memory'
  const dbServiceIpc = await import('../../modules/db-service/db-service-ipc.js')
  process.on('message', dbServiceIpc.handleDbServiceParentRuntimeMessage)
  const invalidation = await import('../../shared/gateway-cache-invalidation.js')
  await sendChildMessageAsync({ type: 'gateway_api_key_invalidation_regression_ready' })
  await waitForParentMessage('gateway_api_key_invalidation_regression_start', '等待跨进程 API Key 失效回归启动超时')
  await invalidation.notifyGatewayApiKeyValidationCacheInvalidationAsync(
    'key_cross_process_regression',
    'api_key_updated',
    ['hash-before-cross-process', 'hash-after-cross-process']
  )
  await sendChildMessageAsync({ type: 'gateway_api_key_invalidation_regression_completed' })
}

async function waitForParentMessage(type: string, timeoutMessage: string): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error(timeoutMessage)), 5_000)
    const listener = (message: unknown): void => {
      if (!isRecord(message) || message.type !== type) return
      clearTimeout(timeout)
      process.off('message', listener)
      resolve()
    }
    process.on('message', listener)
  })
}

async function sendChildMessageAsync(message: Record<string, unknown>): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    if (!process.send) return reject(new Error('跨进程 API Key 失效回归缺少父进程 IPC'))
    process.send(message, (error) => {
      if (error) reject(error)
      else resolve()
    })
  })
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function assertOrdered(sourceText: string, markers: string[], message: string): void {
  let previous = -1
  for (const marker of markers) {
    const index = sourceText.indexOf(marker, previous + 1)
    assert(index >= 0, `${message}：缺少 ${marker}`)
    assert(index > previous, `${message}：顺序错误 ${marker}`)
    previous = index
  }
}

function functionBody(sourceText: string, functionName: string): string {
  const start = sourceText.indexOf(`function ${functionName}`)
  assert(start >= 0, `缺少函数 ${functionName}`)
  let openBrace = -1
  let parenthesisDepth = 0
  for (let index = start; index < sourceText.length; index += 1) {
    const char = sourceText[index]
    if (char === '(') parenthesisDepth += 1
    if (char === ')') parenthesisDepth = Math.max(0, parenthesisDepth - 1)
    if (char === '{' && parenthesisDepth === 0) {
      openBrace = index
      break
    }
  }
  assert(openBrace >= 0, `${functionName} 缺少函数体`)
  let depth = 0
  for (let index = openBrace; index < sourceText.length; index += 1) {
    if (sourceText[index] === '{') depth += 1
    if (sourceText[index] === '}') {
      depth -= 1
      if (depth === 0) return sourceText.slice(openBrace, index + 1)
    }
  }
  throw new Error(`${functionName} 函数体未闭合`)
}
