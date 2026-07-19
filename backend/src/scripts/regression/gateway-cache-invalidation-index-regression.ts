import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, readdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import type { DatabaseSync } from 'node:sqlite'
import { fileURLToPath } from 'node:url'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
const backendSrcRoot = fileURLToPath(new URL('../..', import.meta.url))

const gatewayApiKeyRepositorySource = readSource('storage/gateway-api-key.repository.ts')
const apiKeyQuotaServiceSource = readSource('modules/gateway/quota/api-key-quota.service.ts')
const authorizationQuotaServiceSource = readSource('modules/gateway/quota/authorization-quota.service.ts')
const gatewayRuntimeCacheSource = readSource('modules/gateway/runtime/runtime-cache.service.ts')
const clientIpPolicyCacheSource = readSource('modules/gateway/runtime/client-ip-policy-cache.service.ts')
const hybridScoringSource = readSource('modules/gateway/hybrid/scoring.service.ts')
const modelCatalogSource = readSource('modules/model-pricing/model-catalog.service.ts')
const groupReadLoadersSource = readSource('storage/group-read-loaders.ts')
const authorizationReadLoadersSource = readSource('storage/authorization-read-loaders.ts')
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

assert(gatewayRuntimeCacheSource.includes('createSharedJsonCache<GatewaySettings>'), '网关设置缓存应声明 Redis JSON 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'readCachedGatewaySettingsAsync', 'getGatewaySettingsSharedCacheEntry()', '网关设置异步读取应先读取 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'readCachedGatewaySettingsAsync', 'setGatewaySettingsCacheEntryAsync(value)', '网关设置 DB service 命中后应写 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'clearGatewayRuntimeCacheLocal', 'clearGatewaySettingsSharedCache()', '网关运行态全量失效应清理网关设置 Redis 共享缓存')

assert(gatewayRuntimeCacheSource.includes('createSharedJsonCache<GroupUsageAccessCacheEntry>'), '网关分组访问缓存应声明 Redis JSON 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'resolveCachedGroupUsageAccessMetadataAsync', 'getGroupUsageAccessSharedCacheEntry(cacheKey)', '网关分组访问异步读取应先读取 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'resolveCachedGroupUsageAccessMetadataAsync', 'loadGroupUsageAccessMetadataAndPopulateCache(groupId, systemAccountId, cacheKey)', '网关分组访问缓存 miss 后应进入统一加载并写缓存 helper')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'loadGroupUsageAccessMetadataAndPopulateCache', 'setGroupUsageAccessCacheEntryAsync(cacheKey', '网关分组访问 DB 命中后应写 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'refreshGroupUsageAccessMetadataInBackground', 'setGroupUsageAccessSharedCacheEntry(cacheKey, entry)', '网关分组访问后台刷新应同步写 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'clearGatewayRuntimeCacheLocal', 'clearGroupUsageAccessSharedCache()', '网关运行态全量失效应清理分组访问 Redis 共享缓存')

assert(gatewayRuntimeCacheSource.includes('createSharedJsonCache<ProviderModelCatalogItem[]>'), '网关模型目录缓存应声明 Redis JSON 共享缓存')
assert(gatewayRuntimeCacheSource.includes('createSharedJsonCache<ResponseInspectionPolicyCacheEntry>'), '网关响应检查策略缓存应声明 Redis JSON 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'listCachedProviderModelCatalogAsync', 'getProviderModelCatalogSharedCacheEntry(cacheKey)', '网关模型目录异步读取应先读取 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'listCachedProviderModelCatalogAsync', 'setProviderModelCatalogCacheEntryAsync(cacheKey', '网关模型目录 DB 命中后应写 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'listCachedActiveResponseInspectionPoliciesAsync', 'getResponseInspectionPolicySharedCacheEntry(cacheKey)', '网关响应检查策略异步读取应先读取 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'listCachedActiveResponseInspectionPoliciesAsync', 'loadActiveResponseInspectionPoliciesAndPopulateCache(input, cacheKey)', '网关响应检查策略缓存 miss 后应进入统一加载并写缓存 helper')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'loadActiveResponseInspectionPoliciesAndPopulateCache', 'setResponseInspectionPolicyCacheEntryAsync(cacheKey', '网关响应检查策略 DB 命中后应写 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'clearGatewayRuntimeCacheLocal', 'clearProviderModelCatalogSharedCache()', '网关运行态全量失效应清理模型目录 Redis 共享缓存')
assert(gatewayRuntimeCacheSource.includes('createSharedJsonCache<ProviderModelRouteIndexSharedCacheEntry>'), '网关模型路由索引应声明 Redis JSON 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'resolveCachedProviderModelRouteAsync', 'getProviderModelRouteIndexSharedCacheEntry(cacheKey)', '网关模型路由索引本地 miss 后应读取 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'resolveCachedProviderModelRouteAsync', 'setProviderModelRouteIndexSharedCacheEntry(cacheKey, cached)', '网关模型路由索引构建后应写 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'clearGatewayRuntimeCacheLocal', 'clearProviderModelRouteIndexSharedCache()', '网关运行态全量失效应清理模型路由索引 Redis 共享缓存')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'providerModelRouteIndexCacheEntryToShared', 'entries: [...entry.index.entries()]', '网关模型路由索引写入 Redis 前应把 Map 转成 JSON 数组')
assertFunctionIncludes(gatewayRuntimeCacheSource, 'clearGatewayRuntimeCacheLocal', 'clearResponseInspectionPolicySharedCache()', '网关运行态全量失效应清理响应检查策略 Redis 共享缓存')

assert(clientIpPolicyCacheSource.includes('createSharedJsonCache<ClientIpPolicyByIpCacheEntry>'), '客户端 IP 封禁策略应声明单 IP Redis JSON 共享缓存')
assertFunctionIncludes(clientIpPolicyCacheSource, 'reloadClientIpPolicyCacheLocal', 'clearPolicyByIpSharedCache()', '客户端 IP 封禁策略重载应清理单 IP Redis 共享缓存')
assertFunctionIncludes(clientIpPolicyCacheSource, 'reloadClientIpPolicyCacheLocal', 'bypassSharedCache', '客户端 IP 封禁策略失效重载必须支持绕过旧 Redis shared cache')
assert.match(functionBody(clientIpPolicyCacheSource, 'reloadClientIpPolicyCacheLocal'), /runtimeConfig\.cacheDriver === 'redis'[\s\S]*clearPolicyByIpSharedCache\(\)[\s\S]*return[\s\S]*const sharedSnapshot/, '客户端 IP 封禁策略 Redis 重载不能回源全量策略快照')
assertFunctionIncludes(clientIpPolicyCacheSource, 'replaceClientIpPolicyCacheLocal', '高性能模式禁止同步写入 Client-IP 策略 Redis shared cache', '客户端 IP 封禁策略同步替换在高性能模式下必须拒绝写 Redis shared cache')
assertFunctionIncludes(clientIpPolicyCacheSource, 'loadClientIpPolicyByHashFromDatabase', "type: 'find_active_client_ip_policy_by_hash'", '客户端 IP 封禁策略 Redis miss 后应按 ip_hash 索引回源')
assertFunctionIncludes(clientIpPolicyCacheSource, 'replaceClientIpPolicySharedSnapshotAsync', 'setClientIpPolicyByIpSharedCacheEntry', '显式客户端 IP 封禁策略快照应写入单 IP Redis 共享缓存')
assertFunctionIncludes(clientIpPolicyCacheSource, 'clearClientIpPolicyCacheLocal', 'clearActivePolicySnapshotSharedCache()', '客户端 IP 封禁策略本地清理应清理 Redis 共享缓存')
assertFunctionIncludes(clientIpPolicyCacheSource, 'clearClientIpPolicyCacheLocal', 'clearPolicyByIpSharedCache()', '客户端 IP 封禁策略本地清理应清理单 IP Redis 共享缓存')
assertFunctionIncludes(clientIpPolicyCacheSource, 'inspectClientIpPolicy', 'loadClientIpPolicyByHashFromSharedCacheOrDatabase', '客户端 IP 封禁策略高性能请求路径应按单 IP hash 读取 Redis shared cache 或索引回源')
assertFunctionIncludes(clientIpPolicyCacheSource, 'inspectClientIpPolicy', 'activePolicySnapshot.get', '客户端 IP 封禁策略单机请求路径仍应读取 server 本地快照')
assertFunctionExcludes(clientIpPolicyCacheSource, 'inspectClientIpPolicy', 'loadClientIpPolicySnapshotFromSharedCacheOrDatabase', '客户端 IP 封禁策略高性能请求路径不能加载全量 active 策略快照')

assert(hybridScoringSource.includes('createSharedJsonCache<HybridScoringCacheEntry>'), '混合路由评分结果缓存应声明 Redis JSON 共享缓存')
assertFunctionIncludes(hybridScoringSource, 'scoreHybridGatewayRequest', 'getHybridScoringSharedCacheEntry(cacheKey)', '混合路由评分本地缓存 miss 后应读取 Redis 共享缓存')
assertFunctionIncludes(hybridScoringSource, 'rememberHybridScoringCacheResult', 'setHybridScoringSharedCacheEntry(key', '混合路由评分成功后应写 Redis 共享缓存')
assertFunctionIncludes(hybridScoringSource, 'clearHybridScoringCacheForTest', 'clearHybridScoringSharedCache()', '混合路由评分测试清理应支持清理 Redis 共享缓存')
assertFunctionIncludes(hybridScoringSource, 'getHybridScoringSharedCacheEntry', "runtimeConfig.cacheDriver !== 'redis'", '混合路由评分 Redis shared cache 只应在 Redis cache driver 下读取')

assert(modelCatalogSource.includes('createSharedJsonCache<ProviderModelCatalogItem[]>'), '模型目录服务缓存应声明 Redis JSON 共享缓存')
assertFunctionIncludes(modelCatalogSource, 'listProviderModelCatalogAsync', 'getProviderModelCatalogSharedCacheEntry(cacheKey)', '模型目录服务异步读取应先读取 Redis 共享缓存')
assertFunctionIncludes(modelCatalogSource, 'listProviderModelCatalogAsync', 'setProviderModelCatalogCacheEntryAsync(cacheKey', '模型目录服务 DB 命中后应写 Redis 共享缓存')
assertFunctionIncludes(modelCatalogSource, 'clearProviderModelCatalogCaches', 'clearProviderModelCatalogSharedCacheAsync()', '模型目录服务失效应清理 Redis 共享缓存命名空间')
assert(modelCatalogSource.includes('shouldInvalidateProviderModelCatalog(reason)'), '模型目录服务只应响应模型目录变更事件')

assert(groupReadLoadersSource.includes('createSharedJsonCache<string[]>'), '分组账号 ID lookup 应声明 Redis JSON 共享缓存')
assertFunctionIncludes(groupReadLoadersSource, 'loadGroupAccountIdsByGroupIdsAsync', 'getGroupAccountIdsSharedCacheEntry(id)', '分组账号 ID async lookup 应先读取 Redis 共享缓存')
assertFunctionIncludes(groupReadLoadersSource, 'loadGroupAccountIdsByGroupIdsAsync', 'setGroupAccountIdsSharedCacheEntryAsync(id, accountIds)', '分组账号 ID async lookup DB 命中后应写 Redis 共享缓存')
assertFunctionIncludes(groupReadLoadersSource, 'invalidateGroupAccountIdsCache', 'deleteGroupAccountIdsSharedCacheEntry(id)', '分组账号 ID 定点失效应清理 Redis 共享缓存')
assertFunctionIncludes(groupReadLoadersSource, 'invalidateGroupAccountIdsCache', 'clearGroupAccountIdsSharedCache()', '分组账号 ID 全量失效应清理 Redis 共享缓存命名空间')

assert(authorizationReadLoadersSource.includes('createSharedJsonCache<ResourceAuthorizationStats>'), '授权统计 lookup 应声明 Redis JSON 共享缓存')
assert(authorizationReadLoadersSource.includes('createSharedJsonCache<ResourceAuthorizationSourceSummary[]>'), '授权来源 lookup 应声明 Redis JSON 共享缓存')
assertFunctionIncludes(authorizationReadLoadersSource, 'loadResourceAuthorizationStatsByResourceIdsAsync', 'getAuthorizationStatsSharedCacheEntry(cacheKey)', '授权统计 async lookup 应先读取 Redis 共享缓存')
assertFunctionIncludes(authorizationReadLoadersSource, 'loadResourceAuthorizationStatsByResourceIdsAsync', 'setAuthorizationStatsSharedCacheEntryAsync(cacheKey, stats)', '授权统计 async lookup DB 命中后应写 Redis 共享缓存')
assertFunctionIncludes(authorizationReadLoadersSource, 'loadResourceAuthorizationSourcesByAuthorizationIdsAsync', 'getAuthorizationSourcesSharedCacheEntry(id)', '授权来源 async lookup 应先读取 Redis 共享缓存')
assertFunctionIncludes(authorizationReadLoadersSource, 'loadResourceAuthorizationSourcesByAuthorizationIdsAsync', 'setAuthorizationSourcesSharedCacheEntryAsync(id, sources)', '授权来源 async lookup DB 命中后应写 Redis 共享缓存')
assertFunctionIncludes(authorizationReadLoadersSource, 'clearResourceAuthorizationLookupCaches', 'clearAuthorizationStatsSharedCache()', '授权 lookup 全量清理应清理统计 Redis 共享缓存')
assertFunctionIncludes(authorizationReadLoadersSource, 'clearResourceAuthorizationLookupCaches', 'clearAuthorizationSourcesSharedCache()', '授权 lookup 全量清理应清理来源 Redis 共享缓存')

assert(!/store\.clear\(\)/.test(appCacheSource), '通用缓存 clear 不应线性清空 LRU 条目')
assert(appCacheSource.includes('store = createStore(options)'), '通用缓存 clear 应替换底层 LRU 实例以保持常量时间')

await assertGatewayCacheInvalidationBehavior()

console.log('网关缓存定点失效回归通过：API Key 校验和额度缓存按反向索引删除，网关设置、分组访问、网关 / 管理端模型目录、模型路由索引、响应检查策略、客户端 IP 封禁策略单 IP 条目和混合路由评分结果包含 Redis shared cache 路径，行为级定点失效正确，通用缓存清理不再扫描全部缓存条目')

function readSource(relativePath: string): string {
  return readFileSync(resolve(backendSrcRoot, relativePath), 'utf8')
}

interface CacheDeclaration {
  file: string
  name: string
}

const localOnlyAppCacheReasons = new Map<string, string>([
  ['gateway:client-ip-policy-by-ip', 'per-IP derived lookup over the Redis-backed policy entries'],
  ['gateway:runtime', 'large gateway runtime snapshot invalidated by runtime-state versioning'],
  ['gateway:openai-accounts', 'contains upstream account secrets and must stay process-local'],
  ['gateway:openai-session-affinity', 'session affinity uses local reverse indexes for migration'],
  ['gateway:openai-traffic-migration-preference', 'short-lived local preference coupled to session affinity migration'],
  ['openai-oauth:recent-refresh', 'deduplicates sensitive OAuth refresh records inside one process']
])

assertProductionCacheClassification()

function assertProductionCacheClassification(): void {
  const appCaches = collectCacheDeclarations('createAppCache')
  const sharedCacheNames = new Set(collectCacheDeclarations('createSharedJsonCache').map((cache) => cache.name))
  const appCacheNames = new Set(appCaches.map((cache) => cache.name))
  const unclassifiedLocalCaches = appCaches
    .filter((cache) => !sharedCacheNames.has(cache.name) && !localOnlyAppCacheReasons.has(cache.name))
    .map((cache) => `${cache.name} (${cache.file})`)
  assert.deepEqual(
    unclassifiedLocalCaches,
    [],
    '新增生产 LRU cache 必须接入 Redis shared cache，或在 localOnlyAppCacheReasons 中登记本地保留原因'
  )
  const staleLocalClassifications = [...localOnlyAppCacheReasons.keys()]
    .filter((name) => !appCacheNames.has(name))
  assert.deepEqual(
    staleLocalClassifications,
    [],
    'localOnlyAppCacheReasons 中存在已经不存在的缓存分类，请同步清理'
  )
}

function collectCacheDeclarations(factoryName: 'createAppCache' | 'createSharedJsonCache'): CacheDeclaration[] {
  const result: CacheDeclaration[] = []
  for (const file of productionSourceFiles(backendSrcRoot)) {
    const source = readFileSync(file, 'utf8')
    result.push(...cacheDeclarationsFromSource(file, source, factoryName))
  }
  return result
}

function cacheDeclarationsFromSource(
  file: string,
  source: string,
  factoryName: 'createAppCache' | 'createSharedJsonCache'
): CacheDeclaration[] {
  const declarations: CacheDeclaration[] = []
  const pattern = new RegExp(`${factoryName}<[^]*?>\\s*\\(\\s*\\{[^]*?name:\\s*'([^']+)'`, 'g')
  let match: RegExpExecArray | null
  while ((match = pattern.exec(source)) !== null) {
    declarations.push({
      file: file.replace(/\\/g, '/').replace(backendSrcRoot.replace(/\\/g, '/'), 'backend/src'),
      name: match[1]
    })
  }
  return declarations
}

function productionSourceFiles(directory: string): string[] {
  const result: string[] = []
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const fullPath = join(directory, entry.name)
    if (entry.isDirectory()) {
      if (entry.name === 'scripts') continue
      result.push(...productionSourceFiles(fullPath))
      continue
    }
    if (entry.isFile() && entry.name.endsWith('.ts')) {
      result.push(fullPath)
    }
  }
  return result
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

function assertFunctionExcludes(source: string, functionName: string, pattern: string, message: string): void {
  const body = functionBody(source, functionName)
  assert(!body.includes(pattern), message)
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
    const first = createApiKeyRecordWithRouteStrategy(repositories, {
      name: '缓存定点失效 Key A',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }]
    }, access)
    const second = createApiKeyRecordWithRouteStrategy(repositories, {
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

    const quotaFirst = createApiKeyRecordWithRouteStrategy(repositories, {
      name: '额度缓存定点失效 Key A',
      quotaLimits: { total: { enabled: true, limit: 1 } },
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }]
    }, access)
    const quotaSecond = createApiKeyRecordWithRouteStrategy(repositories, {
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
