import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const invalidationSource = readFileSync(new URL('../../shared/gateway-cache-invalidation.ts', import.meta.url), 'utf8')
const runtimeCacheSource = readFileSync(new URL('../../modules/gateway/runtime/runtime-cache.service.ts', import.meta.url), 'utf8')
const apiKeyQuotaSource = readFileSync(new URL('../../modules/gateway/quota/api-key-quota.service.ts', import.meta.url), 'utf8')
const authorizationQuotaSource = readFileSync(new URL('../../modules/gateway/quota/authorization-quota.service.ts', import.meta.url), 'utf8')
const backgroundIpcSource = readFileSync(new URL('../../modules/background/background-ipc.ts', import.meta.url), 'utf8')

assert.match(invalidationSource, /createRuntimeStateStore\('gateway_cache_invalidation'\)/, '网关缓存失效应使用 runtime state store 承接跨进程版本')
assert.match(invalidationSource, /runtimeConfig\.runtimeStateDriver !== 'redis'/, '非 Redis runtime state 模式不应触发跨进程同步')
assert.match(invalidationSource, /gatewayCacheInvalidationSyncIntervalMs\s*=\s*1000/, '读取侧应有固定节流，避免每个请求都读取 Redis')
assert.doesNotMatch(invalidationSource, /background-ipc|process\.send|gateway_runtime_cache_invalidate/, '网关缓存失效广播不能依赖后台 IPC 作为跨进程事实源')
assert.match(invalidationSource, /publishGatewayCacheInvalidationToRuntimeState\('gateway_runtime_cache'/, '网关运行态失效应发布 runtime state 版本')
assert.match(invalidationSource, /publishGatewayCacheInvalidationToRuntimeState\('authorization_quota_cache'/, '授权额度失效应发布 runtime state 版本')
assert.match(invalidationSource, /publishGatewayCacheInvalidationToRuntimeState\('api_key_quota_cache'/, 'API Key 额度失效应发布 runtime state 版本')
assert.match(invalidationSource, /applyRuntimeStateCacheInvalidation/, '读取侧应把远端版本变化转换为本地 handler 调用')
assert.match(invalidationSource, /type GatewayRuntimeCacheInvalidationHandler = \(reason: string\) => void/, '网关运行态失效 handler 应保留 reason，避免账户更新误清系统设置缓存')
assert.match(invalidationSource, /handler\(reason\)/, '本进程网关运行态失效应把 reason 传给 handler')
assert.match(invalidationSource, /handler\(state\.reason\)/, '跨进程网关运行态失效应把远端 reason 传给 handler')
assert.match(invalidationSource, /handler\(state\.apiKeyId\)/, 'API Key 额度远端失效应保留定点 apiKeyId')
assertNotifyPublishesRuntimeState(invalidationSource, 'notifyGatewayRuntimeCacheInvalidation', 'gateway_runtime_cache')
assertNotifyPublishesRuntimeState(invalidationSource, 'notifyAuthorizationQuotaCacheInvalidation', 'authorization_quota_cache')
assertNotifyPublishesRuntimeState(invalidationSource, 'notifyApiKeyQuotaCacheInvalidation', 'api_key_quota_cache')
assertIpcCacheInvalidationRemainsHotPushOnly(backgroundIpcSource)
assertGatewayRuntimeInvalidationClearsSettingsByReason(runtimeCacheSource)

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

function assertGatewayRuntimeInvalidationClearsSettingsByReason(source: string): void {
  const clearCacheBlock = sourceFunctionBlock(source, 'export function clearGatewayRuntimeCache')
  assert.match(clearCacheBlock, /shouldClearSettingsCacheForGatewayInvalidation\(reason\)/, '网关运行态失效必须按 reason 决定是否清系统设置缓存')
  const clearLocalBlock = sourceFunctionBlock(source, 'export function clearGatewayRuntimeCacheLocal')
  assert.match(clearLocalBlock, /if \(options\.clearSettings \?\? true\)/, '本地网关缓存清理默认仍应清系统设置缓存，避免手动清理漏失效')
  const reasonBlock = sourceFunctionBlock(source, 'function shouldClearSettingsCacheForGatewayInvalidation')
  assert.match(reasonBlock, /!reason \|\| reason === 'settings_updated'/, '只有无 reason 的兼容清理和 settings_updated 才能清系统设置缓存')
}

function assertNotifyPublishesRuntimeState(source: string, functionName: string, topic: string): void {
  const block = sourceFunctionBlock(source, `export function ${functionName}`)
  assert.match(block, new RegExp(`publishGatewayCacheInvalidationToRuntimeState\\('${topic}'`), `${functionName} 必须发布 Redis runtime state 版本`)
  assert.match(block, /runCacheInvalidators/, `${functionName} 应保留本进程 handler 热清理`)
  assert.doesNotMatch(block, /process\.send|sendBackgroundWorkerMessage|gateway_runtime_cache_invalidate/, `${functionName} 不能回退成 IPC-only 缓存失效广播`)
}

function assertIpcCacheInvalidationRemainsHotPushOnly(source: string): void {
  const block = sourceCaseBlock(source, "case 'gateway_runtime_cache_invalidate'")
  assert.match(block, /clearServerGatewayRuntimeCache/, '后台 IPC 缓存失效消息只能触发 server 本地热清理')
  assert.doesNotMatch(block, /publishGatewayCacheInvalidationToRuntimeState|createRuntimeStateStore/, '后台 IPC 不能成为网关缓存失效 runtime state 发布源')
}

function sourceCaseBlock(source: string, marker: string): string {
  const start = source.indexOf(marker)
  assert(start >= 0, `未找到源码片段：${marker}`)
  const nextCase = source.indexOf('\n    case ', start + marker.length)
  const nextDefault = source.indexOf('\n    default:', start + marker.length)
  const candidates = [nextCase, nextDefault].filter((index) => index > start)
  const end = candidates.length ? Math.min(...candidates) : source.length
  return source.slice(start, end)
}

function sourceFunctionBlock(source: string, marker: string): string {
  const start = source.indexOf(marker)
  assert(start >= 0, `未找到源码片段：${marker}`)
  let searchFrom = source.indexOf('):', start)
  if (searchFrom < 0) searchFrom = source.indexOf(') {', start)
  assert(searchFrom >= 0, `源码片段缺少函数签名结束：${marker}`)
  const bodyStart = source.indexOf('{', searchFrom)
  assert(bodyStart >= 0, `源码片段缺少函数体：${marker}`)
  let depth = 0
  for (let index = bodyStart; index < source.length; index += 1) {
    const char = source[index]
    if (char === '{') depth += 1
    if (char === '}') {
      depth -= 1
      if (depth === 0) {
        return source.slice(start, index + 1)
      }
    }
  }
  throw new Error(`源码片段函数体未闭合：${marker}`)
}
