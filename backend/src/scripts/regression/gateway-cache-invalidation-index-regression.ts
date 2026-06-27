import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import type { DatabaseSync } from 'node:sqlite'
import { fileURLToPath } from 'node:url'
import { join, resolve } from 'node:path'

const backendSrcRoot = fileURLToPath(new URL('../..', import.meta.url))

const gatewayApiKeyRepositorySource = readSource('storage/gateway-api-key.repository.ts')
const apiKeyQuotaServiceSource = readSource('modules/gateway/quota/api-key-quota.service.ts')
const authorizationQuotaServiceSource = readSource('modules/gateway/quota/authorization-quota.service.ts')
const gatewayRuntimeCacheSource = readSource('modules/gateway/runtime/runtime-cache.service.ts')
const modelCatalogSource = readSource('modules/model-pricing/model-catalog.service.ts')
const appCacheSource = readSource('shared/cache.ts')

assertFunctionDoesNotScanCacheEntries(gatewayApiKeyRepositorySource, 'invalidateGatewayApiKeyCacheById')
assertFunctionUsesReverseIndex(gatewayApiKeyRepositorySource, 'invalidateGatewayApiKeyCacheById', 'gatewayApiKeyCacheKeysById')
assert(gatewayApiKeyRepositorySource.includes('dispose: (entry, keyHash)'), '网关 API Key 校验缓存应在 LRU 逐出时同步反向索引')
assert(gatewayApiKeyRepositorySource.includes('createSharedJsonCache<GatewayApiKeyCacheEntry>'), '网关 API Key 校验缓存应声明 Redis JSON 共享缓存')
assertFunctionIncludes(gatewayApiKeyRepositorySource, 'validateGatewayApiKeyAsync', 'syncGatewayCacheInvalidationsFromRuntimeState()', '网关 API Key 异步校验应先同步 Redis runtime state 失效版本')
assertFunctionIncludes(gatewayApiKeyRepositorySource, 'validateGatewayApiKeyAsync', 'getGatewayApiKeySharedCacheEntry(keyHash)', '网关 API Key 异步校验应读取 Redis 共享缓存')
assertFunctionIncludes(gatewayApiKeyRepositorySource, 'validateGatewayApiKeyAsync', 'setGatewayApiKeyCacheEntryAsync(keyHash', '网关 API Key 异步校验 DB 命中后应写 Redis 共享缓存')
assertFunctionIncludes(gatewayApiKeyRepositorySource, 'clearGatewayApiKeyValidationCache', 'clearGatewayApiKeySharedCache()', '网关 API Key 校验全量失效应清理 Redis 共享缓存命名空间')
assertFunctionIncludes(gatewayApiKeyRepositorySource, 'invalidateGatewayApiKeyCacheById', 'clearGatewayApiKeySharedCache()', '网关 API Key 校验定点失效应清理 Redis 共享缓存命名空间')

assertFunctionDoesNotScanCacheEntries(apiKeyQuotaServiceSource, 'invalidateApiKeyQuotaCacheById')
assertFunctionUsesReverseIndex(apiKeyQuotaServiceSource, 'invalidateApiKeyQuotaCacheById', 'apiKeyQuotaCacheKeysById')
assert(apiKeyQuotaServiceSource.includes('dispose: (_entry, cacheKey)'), 'API Key 额度缓存应在 LRU 逐出时同步反向索引')
assert(apiKeyQuotaServiceSource.includes('createSharedJsonCache<ApiKeyQuotaCacheEntry>'), 'API Key 额度缓存应声明 Redis JSON 共享缓存')
assertFunctionIncludes(apiKeyQuotaServiceSource, 'checkGatewayApiKeyQuotaAsync', 'getApiKeyQuotaSharedCacheEntry(cacheKey)', 'API Key 额度异步判定应先读取 Redis 共享缓存')
assertFunctionIncludes(apiKeyQuotaServiceSource, 'setApiKeyQuotaCacheEntryAsync', 'setApiKeyQuotaSharedCacheEntry(cacheKey, entry)', 'API Key 额度异步写入应同步写 Redis 共享缓存')
assertFunctionIncludes(apiKeyQuotaServiceSource, 'clearApiKeyQuotaCache', 'clearApiKeyQuotaSharedCache()', 'API Key 额度全量失效应清理 Redis 共享缓存命名空间')

assert(authorizationQuotaServiceSource.includes('createSharedJsonCache<AuthorizationQuotaCacheEntry>'), '授权额度缓存应声明 Redis JSON 共享缓存')
assertFunctionIncludes(authorizationQuotaServiceSource, 'checkGatewayAuthorizationQuotaAsync', 'getAuthorizationQuotaSharedCacheEntry(cacheKey)', '授权额度异步判定应先读取 Redis 共享缓存')
assertFunctionIncludes(authorizationQuotaServiceSource, 'checkGatewayAuthorizationQuotaBatchAsync', 'getAuthorizationQuotaSharedCacheEntry', '授权额度批量判定应读取 Redis 共享缓存')
assertFunctionIncludes(authorizationQuotaServiceSource, 'setAuthorizationQuotaCacheEntryAsync', 'setAuthorizationQuotaSharedCacheEntry(cacheKey, entry)', '授权额度异步写入应同步写 Redis 共享缓存')
assertFunctionIncludes(authorizationQuotaServiceSource, 'clearAuthorizationQuotaCache', 'clearAuthorizationQuotaSharedCache()', '授权额度全量失效应清理 Redis 共享缓存命名空间')

assert(gatewayRuntimeCacheSource.includes('createSharedJsonCache<ProviderModelCatalogItem[]>'), '网关模型目录缓存应声明 Redis JSON 共享缓存')
assert(gatewayRuntimeCacheSource.includes('createSharedJsonCache<ResponseInspectionPolicyCacheEntry>'), '网关响应检查策略缓存应声明 Redis JSON 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'listCachedProviderModelCatalogAsync', 'getProviderModelCatalogSharedCacheEntry(cacheKey)', '网关模型目录异步读取应先读取 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'listCachedProviderModelCatalogAsync', 'setProviderModelCatalogCacheEntryAsync(cacheKey', '网关模型目录 DB 命中后应写 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'listCachedActiveResponseInspectionPoliciesAsync', 'getResponseInspectionPolicySharedCacheEntry(cacheKey)', '网关响应检查策略异步读取应先读取 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'listCachedActiveResponseInspectionPoliciesAsync', 'setResponseInspectionPolicyCacheEntryAsync(cacheKey', '网关响应检查策略 DB 命中后应写 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'clearGatewayRuntimeCacheLocal', 'clearProviderModelCatalogSharedCache()', '网关运行态全量失效应清理模型目录 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'clearGatewayRuntimeCacheLocal', 'clearResponseInspectionPolicySharedCache()', '网关运行态全量失效应清理响应检查策略 Redis 共享缓存')

assert(modelCatalogSource.includes('createSharedJsonCache<ProviderModelCatalogItem[]>'), '模型目录服务缓存应声明 Redis JSON 共享缓存')
assertFunctionIncludes(modelCatalogSource, 'listProviderModelCatalogAsync', 'getProviderModelCatalogSharedCacheEntry(cacheKey)', '模型目录服务异步读取应先读取 Redis 共享缓存')
assertFunctionIncludes(modelCatalogSource, 'listProviderModelCatalogAsync', 'setProviderModelCatalogCacheEntryAsync(cacheKey', '模型目录服务 DB 命中后应写 Redis 共享缓存')
assertFunctionIncludes(modelCatalogSource, 'clearProviderModelCatalogCaches', 'clearProviderModelCatalogSharedCache()', '模型目录服务失效应清理 Redis 共享缓存命名空间')
assert(modelCatalogSource.includes('registerGatewayRuntimeCacheInvalidator(clearProviderModelCatalogCaches)'), '模型目录服务应复用网关运行态失效事件清理本地和 Redis shared cache')

assert(!/store\.clear\(\)/.test(appCacheSource), '通用缓存 clear 不应线性清空 LRU 条目')
assert(appCacheSource.includes('store = createStore(options)'), '通用缓存 clear 应替换底层 LRU 实例以保持常量时间')

await assertGatewayCacheInvalidationBehavior()

console.log('网关缓存定点失效回归通过：API Key 校验和额度缓存按反向索引删除，网关 / 管理端模型目录和响应检查策略包含 Redis shared cache 路径，行为级定点失效正确，通用缓存清理不再扫描全部缓存条目')

function readSource(relativePath: string): string {
  return readFileSync(resolve(backendSrcRoot, relativePath), 'utf8')
}

function assertFunctionDoesNotScanCacheEntries(source: string, functionName: string): void {
  const body = functionBody(source, functionName)
  assert(!/\.entries\(\)/.test(body), `${functionName} 不应遍历缓存 entries 做定点失效`)
}

function assertFunctionUsesReverseIndex(source: string, functionName: string, indexName: string): void {
  const body = functionBody(source, functionName)
  assert(body.includes(`${indexName}.get(id)`), `${functionName} 应通过 ${indexName} 按 API Key ID 定位缓存键`)
}

function assertFunctionIncludes(source: string, functionName: string, pattern: string, message: string): void {
  const body = functionBody(source, functionName)
  assert(body.includes(pattern), message)
}

function functionBody(source: string, functionName: string): string {
  const start = source.indexOf(`function ${functionName}`)
  assert(start >= 0, `缺少函数 ${functionName}`)
  let openBrace = -1
  let parenDepth = 0
  for (let index = start; index < source.length; index += 1) {
    const char = source[index]
    if (char === '(') parenDepth += 1
    if (char === ')') parenDepth = Math.max(0, parenDepth - 1)
    if (char === '{' && parenDepth === 0) {
      openBrace = index
      break
    }
  }
  assert(openBrace >= 0, `函数 ${functionName} 缺少函数体`)
  let depth = 0
  for (let index = openBrace; index < source.length; index += 1) {
    const char = source[index]
    if (char === '{') depth += 1
    if (char === '}') {
      depth -= 1
      if (depth === 0) {
        return source.slice(openBrace, index + 1)
      }
    }
  }
  throw new Error(`函数 ${functionName} 函数体未闭合`)
}

async function assertGatewayCacheInvalidationBehavior(): Promise<void> {
  const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-cache-invalidation-index-${Date.now()}-${Math.random().toString(16).slice(2)}`)
  mkdirSync(tempRoot, { recursive: true })
  const { runtimeConfig } = await import('../../config/runtime.js')
  runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
  runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
  runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
  runtimeConfig.secret = 'gateway-cache-invalidation-index-secret'
  runtimeConfig.log.consoleEnabled = false
  runtimeConfig.log.fileEnabled = false
  runtimeConfig.processRole = 'worker'

  const [
    { logger },
    databaseModule,
    repositories,
    gatewayApiKeyRepository,
    apiKeyQuotaService
  ] = await Promise.all([
    import('../../shared/logger.js'),
    import('../../storage/database.js'),
    import('../../storage/repositories.js'),
    import('../../storage/gateway-api-key.repository.js'),
    import('../../modules/gateway/quota/api-key-quota.service.js')
  ])
  logger.level = 'silent'

  try {
    const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
    const group = repositories.createGroup({
      name: '缓存定点失效分组',
      providerCode: 'gpt',
      enabled: true
    }, access)
    const first = repositories.createApiKeyRecord({
      name: '缓存定点失效 Key A',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }]
    }, access)
    const second = repositories.createApiKeyRecord({
      name: '缓存定点失效 Key B',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }]
    }, access)

    assert.equal(gatewayApiKeyRepository.validateGatewayApiKey(first.key)?.id, first.id, 'API Key A 首次校验应写入缓存')
    assert.equal(gatewayApiKeyRepository.validateGatewayApiKey(second.key)?.id, second.id, 'API Key B 首次校验应写入缓存')
    repositories.updateApiKey(first.id, { status: 'disabled' }, access)

    const database = databaseModule.getBusinessDatabase()
    const originalPrepare = database.prepare.bind(database) as typeof database.prepare
    let validationSelects = 0
    database.prepare = ((sql: string) => {
      const normalizedSql = sql.replace(/\s+/g, ' ')
      if (/\bFROM\s+api_keys\b/i.test(normalizedSql) && /\bapi_keys\.key_hash\s*=\s*\?/i.test(normalizedSql)) {
        validationSelects += 1
      }
      return originalPrepare(sql)
    }) as typeof database.prepare
    try {
      assert.equal(gatewayApiKeyRepository.validateGatewayApiKey(first.key), undefined, '按 ID 失效后，已停用 API Key A 不能继续命中旧缓存')
      assert.equal(gatewayApiKeyRepository.validateGatewayApiKey(second.key)?.id, second.id, '按 ID 失效不能误删 API Key B 的缓存')
    } finally {
      database.prepare = originalPrepare
    }
    assert.equal(validationSelects, 1, 'API Key A 定点失效后只应重新查询目标 Key，API Key B 应继续命中缓存')

    const quotaFirst = repositories.createApiKeyRecord({
      name: '额度缓存定点失效 Key A',
      quotaLimits: { total: { enabled: true, limit: 1 } },
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }]
    }, access)
    const quotaSecond = repositories.createApiKeyRecord({
      name: '额度缓存定点失效 Key B',
      quotaLimits: { total: { enabled: true, limit: 1 } },
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }]
    }, access)
    const quotaFirstRow = gatewayApiKeyRepository.validateGatewayApiKey(quotaFirst.key)
    const quotaSecondRow = gatewayApiKeyRepository.validateGatewayApiKey(quotaSecond.key)
    assert(quotaFirstRow, '额度 Key A 应可校验')
    assert(quotaSecondRow, '额度 Key B 应可校验')

    const quotaNow = new Date('2026-06-01T00:00:00.000Z')
    assert.equal(apiKeyQuotaService.checkGatewayApiKeyQuota(quotaFirstRow, quotaNow).allowed, true, '额度 Key A 初始应允许')
    assert.equal(apiKeyQuotaService.checkGatewayApiKeyQuota(quotaSecondRow, quotaNow).allowed, true, '额度 Key B 初始应允许')
    writeApiKeyTotalCost(databaseModule.getStatsDatabase(), quotaFirst.id, 2)
    writeApiKeyTotalCost(databaseModule.getStatsDatabase(), quotaSecond.id, 2)
    apiKeyQuotaService.invalidateApiKeyQuotaCacheById(quotaFirst.id)
    assert.equal(apiKeyQuotaService.checkGatewayApiKeyQuota(quotaFirstRow, quotaNow).allowed, false, '额度 Key A 定点失效后应重新读取统计并拒绝')
    assert.equal(apiKeyQuotaService.checkGatewayApiKeyQuota(quotaSecondRow, quotaNow).allowed, true, '额度 Key A 定点失效不能误删额度 Key B 的缓存')
  } finally {
    try {
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

function writeApiKeyTotalCost(database: DatabaseSync, apiKeyId: string, totalCostUsd: number): void {
  database
    .prepare(`
      INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, total_cost_usd, updated_at)
      VALUES ('sys_admin', 'api_key', ?, ?, '2026-06-01T00:00:00.000Z')
      ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
        total_cost_usd = excluded.total_cost_usd,
        updated_at = excluded.updated_at
    `)
    .run(apiKeyId, totalCostUsd)
}
