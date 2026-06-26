import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const invalidationSource = readFileSync(new URL('../../shared/gateway-cache-invalidation.ts', import.meta.url), 'utf8')
const runtimeCacheSource = readFileSync(new URL('../../modules/gateway/runtime/runtime-cache.service.ts', import.meta.url), 'utf8')
const apiKeyQuotaSource = readFileSync(new URL('../../modules/gateway/quota/api-key-quota.service.ts', import.meta.url), 'utf8')
const authorizationQuotaSource = readFileSync(new URL('../../modules/gateway/quota/authorization-quota.service.ts', import.meta.url), 'utf8')

assert.match(invalidationSource, /createRuntimeStateStore\('gateway_cache_invalidation'\)/, '网关缓存失效应使用 runtime state store 承接跨进程版本')
assert.match(invalidationSource, /runtimeConfig\.runtimeStateDriver !== 'redis'/, '非 Redis runtime state 模式不应触发跨进程同步')
assert.match(invalidationSource, /gatewayCacheInvalidationSyncIntervalMs\s*=\s*1000/, '读取侧应有固定节流，避免每个请求都读取 Redis')
assert.match(invalidationSource, /publishGatewayCacheInvalidationToRuntimeState\('gateway_runtime_cache'/, '网关运行态失效应发布 runtime state 版本')
assert.match(invalidationSource, /publishGatewayCacheInvalidationToRuntimeState\('authorization_quota_cache'/, '授权额度失效应发布 runtime state 版本')
assert.match(invalidationSource, /publishGatewayCacheInvalidationToRuntimeState\('api_key_quota_cache'/, 'API Key 额度失效应发布 runtime state 版本')
assert.match(invalidationSource, /applyRuntimeStateCacheInvalidation/, '读取侧应把远端版本变化转换为本地 handler 调用')
assert.match(invalidationSource, /handler\(state\.apiKeyId\)/, 'API Key 额度远端失效应保留定点 apiKeyId')

for (const functionName of [
  'readCachedGatewaySettingsAsync',
  'resolveCachedGroupUsageAccessMetadataAsync',
  'listCachedOpenAIAccountsForGroupAsync',
  'listCachedProviderModelCatalogAsync',
  'resolveCachedProviderModelRouteAsync',
  'listCachedActiveResponseInspectionPoliciesAsync',
  'readCachedGatewayRuntimeAsync'
]) {
  assertFunctionCallsRuntimeStateSync(runtimeCacheSource, functionName)
}

assertFunctionCallsRuntimeStateSync(apiKeyQuotaSource, 'checkGatewayApiKeyQuotaAsync')
assertFunctionCallsRuntimeStateSync(authorizationQuotaSource, 'checkGatewayAuthorizationQuotaAsync')
assertFunctionCallsRuntimeStateSync(authorizationQuotaSource, 'checkGatewayAuthorizationQuotaBatchAsync')

console.log('gateway-cache-invalidation-runtime-state-regression passed')

function assertFunctionCallsRuntimeStateSync(source: string, functionName: string): void {
  const start = source.indexOf(functionName)
  assert(start >= 0, `未找到函数 ${functionName}`)
  const nextExport = source.indexOf('\nexport ', start + functionName.length)
  const nextFunction = source.indexOf('\nfunction ', start + functionName.length)
  const candidates = [nextExport, nextFunction].filter((index) => index > start)
  const end = candidates.length ? Math.min(...candidates) : source.length
  const block = source.slice(start, end)
  assert.match(block, /await syncGatewayCacheInvalidationsFromRuntimeState\(\)/, `${functionName} 应先同步 runtime state 缓存失效版本`)
}
