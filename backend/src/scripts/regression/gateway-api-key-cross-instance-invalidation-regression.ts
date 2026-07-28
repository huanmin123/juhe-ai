import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const repositorySource = readFileSync(new URL('../../storage/gateway-api-key.repository.ts', import.meta.url), 'utf8')
const invalidationSource = readFileSync(new URL('../../shared/gateway-cache-invalidation.ts', import.meta.url), 'utf8')
const dbServiceIpcSource = readFileSync(new URL('../../modules/db-service/db-service-ipc.ts', import.meta.url), 'utf8')
const dbServiceTypesSource = readFileSync(new URL('../../modules/db-service/db-service-types.ts', import.meta.url), 'utf8')
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

const sqliteWorkerBody = functionBody(repositorySource, 'validateGatewayApiKeyWithSqliteReadWorker')
assert.match(sqliteWorkerBody, /gatewayApiKeyValidationAttemptLimit/, 'SQLite read worker 鉴权也必须有失效竞态有界重试')
assertOrdered(sqliteWorkerBody, [
  'const row = await requestSqliteReadWorker',
  'await syncGatewayApiKeyValidationGenerationAfterAsyncRead(generation)',
  'if (!isGatewayApiKeyValidationGenerationCurrent(generation)) continue',
  'expectedGeneration: generation'
], 'SQLite read worker 返回旧读时不得越过失效代际写回缓存')

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

await assertIpcInvalidationAwaitsHandlers()

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
