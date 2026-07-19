import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { runtimeConfig } from '../../config/runtime.js'
import type { RouteStrategySpeedFirstConfig } from '../../domain/types.js'
import { createRuntimeStateStore } from '../../shared/runtime-state-store.js'

const invalidationSource = readFileSync(new URL('../../shared/gateway-cache-invalidation.ts', import.meta.url), 'utf8')
const runtimeCacheSource = readFileSync(new URL('../../modules/gateway/runtime/runtime-cache.service.ts', import.meta.url), 'utf8')
const latencyDegradationSource = readFileSync(new URL('../../modules/gateway/runtime/normal-route-latency-degradation.service.ts', import.meta.url), 'utf8')
const apiKeyQuotaSource = readFileSync(new URL('../../modules/gateway/quota/api-key-quota.service.ts', import.meta.url), 'utf8')
const authorizationQuotaSource = readFileSync(new URL('../../modules/gateway/quota/authorization-quota.service.ts', import.meta.url), 'utf8')
const backgroundIpcSource = readFileSync(new URL('../../modules/background/background-ipc.ts', import.meta.url), 'utf8')
const repositoryLookupsSource = readFileSync(new URL('../../storage/repository-lookups.ts', import.meta.url), 'utf8')

assert.match(invalidationSource, /createRuntimeStateStore\('gateway_cache_invalidation'\)/, '网关缓存失效应使用 runtime state store 承接跨进程版本')
assert.match(invalidationSource, /runtimeConfig\.runtimeStateDriver !== 'redis'/, '非 Redis runtime state 模式不应触发跨进程同步')
assert.match(invalidationSource, /gatewayCacheInvalidationSyncIntervalMs\s*=\s*1000/, '读取侧应有固定节流，避免每个请求都读取 Redis')
assert.doesNotMatch(invalidationSource, /background-ipc|process\.send|gateway_runtime_cache_invalidate/, '网关缓存失效广播不能依赖后台 IPC 作为跨进程事实源')
assert.match(invalidationSource, /publishGatewayCacheInvalidationToRuntimeState\('gateway_runtime_cache'/, '网关运行态失效应发布 runtime state 版本')
assert.match(invalidationSource, /publishGatewayCacheInvalidationToRuntimeState\('authorization_quota_cache'/, '授权额度失效应发布 runtime state 版本')
assert.match(invalidationSource, /publishGatewayCacheInvalidationToRuntimeState\('api_key_quota_cache'/, 'API Key 额度失效应发布 runtime state 版本')
assert.match(invalidationSource, /applyRuntimeStateCacheInvalidation/, '读取侧应把远端版本变化转换为本地 handler 调用')
assert.match(invalidationSource, /source: 'local'/, '网关运行态失效 handler 应保留无事件版本的 local metadata')
assert.match(
  invalidationSource,
  /source: 'runtime_state'[\s\S]*version: string[\s\S]*publishedAt: string/,
  'runtime-state metadata 应携带 version 和 publishedAt'
)
assert.match(
  invalidationSource,
  /GatewayRuntimeCacheInvalidationResult \| Promise<GatewayRuntimeCacheInvalidationResult>/,
  '网关运行态失效 handler 应支持异步清理和 deferred 结果'
)
assert.match(invalidationSource, /handler\(reason, \{ source: 'local' \}\)/, '本进程网关运行态失效应把 local source 传给 handler')
assert.match(
  invalidationSource,
  /source: 'runtime_state',\s*version: state\.version,\s*publishedAt: state\.publishedAt/,
  '跨进程网关运行态失效应透传 runtime-state event marker'
)
assert.match(invalidationSource, /return runGatewayRuntimeCacheInvalidatorsAsync\(/, '跨进程网关运行态失效应等待异步 listener 完成')
assert.match(invalidationSource, /handler\(state\.apiKeyId\)/, 'API Key 额度远端失效应保留定点 apiKeyId')
assertRuntimeStateVersionCommitsAfterSuccessfulApply(invalidationSource)
assertDeferredRuntimeStatePublishBoundary(invalidationSource)
assertNotifyPublishesRuntimeState(invalidationSource, 'notifyGatewayRuntimeCacheInvalidation', 'gateway_runtime_cache')
assertNotifyPublishesRuntimeState(invalidationSource, 'notifyAuthorizationQuotaCacheInvalidation', 'authorization_quota_cache')
assertNotifyPublishesRuntimeState(invalidationSource, 'notifyApiKeyQuotaCacheInvalidation', 'api_key_quota_cache')
assertIpcCacheInvalidationRemainsHotPushOnly(backgroundIpcSource)
assertGatewayRuntimeInvalidationClearsSettingsByReason(runtimeCacheSource)
assertApiKeyLookupLocalCacheInvalidation(invalidationSource, repositoryLookupsSource)
assertRouteStrategyLatencyInvalidationBoundary(invalidationSource, runtimeCacheSource, latencyDegradationSource)

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

await assertGoRouteStrategyInvalidationClearsAllLatencyState()

console.log('gateway-cache-invalidation-runtime-state-regression passed')

async function assertGoRouteStrategyInvalidationClearsAllLatencyState(): Promise<void> {
  runtimeConfig.runtimeStateDriver = 'memory'
  runtimeConfig.databaseDriver = 'postgres'
  runtimeConfig.log.consoleEnabled = false
  runtimeConfig.log.fileEnabled = false
  const invalidationStateStore = createRuntimeStateStore('gateway_cache_invalidation')
  const latencyStateStore = createRuntimeStateStore('gateway-normal-route-latency-degradation')
  const [
    invalidationModule,
    latencyModule,
    _runtimeCacheModule,
    { logger }
  ] = await Promise.all([
    import('../../shared/gateway-cache-invalidation.js'),
    import('../../modules/gateway/runtime/normal-route-latency-degradation.service.js'),
    import('../../modules/gateway/runtime/runtime-cache.service.js'),
    import('../../shared/logger.js')
  ])
  const config: RouteStrategySpeedFirstConfig = {
    firstByteThresholdMs: 30000,
    slowTriggerCount: 2,
    slowWindowSeconds: 120,
    recoverySuccessCount: 3,
    probeIntervalSeconds: 10,
    degradedTtlSeconds: 300,
    maxFirstByteRetriesPerRequest: 2
  }
  const account = { id: 'account_go_route_strategy_invalidation', name: 'Go 策略失效回归账号' }
  const scopes = [
    latencyModule.normalRouteLatencyDegradationScope({
      systemAccountId: 'sys_go_route_strategy_invalidation',
      routeStrategyId: 'route_go_route_strategy_invalidation_a',
      groupId: 'group_go_route_strategy_invalidation_a'
    }),
    latencyModule.normalRouteLatencyDegradationScope({
      systemAccountId: 'sys_go_route_strategy_invalidation',
      routeStrategyId: 'route_go_route_strategy_invalidation_b',
      groupId: 'group_go_route_strategy_invalidation_b'
    })
  ]
  assert(scopes[0] && scopes[1], 'Go 策略失效回归需要两个有效 scope')

  for (const scope of scopes) {
    await latencyModule.recordNormalRouteFirstByteSlowAsync(account, scope, config)
    const result = await latencyModule.recordNormalRouteFirstByteSlowAsync(account, scope, config)
    assert.equal(result?.degraded, true, 'Go 策略失效回归应先写入两个策略的降级状态')
  }

  invalidationModule.notifyGatewayRuntimeCacheInvalidation('route_strategy_updated')
  await delay(0)
  for (const scope of scopes) {
    assert.equal(
      await latencyModule.isNormalRouteAccountLatencyDegradedAsync(account, scope),
      true,
      'Node 本机 route_strategy_updated notify 不能全量清理普通路由速度优先降级状态'
    )
  }
  assert.equal(
    (await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys?.length,
    2,
    'Node 本机 route_strategy_updated notify 不能清空 latency all-index'
  )
  assert.equal(
    (await latencyStateStore.getJson<{ keys?: string[] }>('v1:probe-index'))?.keys?.length,
    2,
    'Node 本机 route_strategy_updated notify 不能清空 latency probe-index'
  )

  runtimeConfig.runtimeStateDriver = 'redis'
  const finalOverwrittenEvent = {
    version: 'go-runtime-version-settings-updated-final-slot',
    reason: 'settings_updated',
    publishedAt: '2026-07-12T01:02:04.567Z'
  }
  let observedRuntimeMetadata:
    | { source: string; version?: string; publishedAt?: string }
    | undefined
  const unregisterRuntimeMetadataObserver =
    invalidationModule.registerGatewayRuntimeCacheInvalidator((reason, metadata) => {
      if (reason === finalOverwrittenEvent.reason && metadata.source === 'runtime_state') {
        observedRuntimeMetadata = metadata
      }
    })
  await invalidationStateStore.setJson('topic:gateway_runtime_cache', {
    version: 'go-runtime-version-route-strategy-updated-overwritten',
    reason: 'route_strategy_updated',
    publishedAt: '2026-07-12T01:02:03.456Z'
  }, 60_000)
  await invalidationStateStore.setJson('topic:gateway_runtime_cache', finalOverwrittenEvent, 60_000)
  try {
    await invalidationModule.syncGatewayCacheInvalidationsFromRuntimeState({ force: true })
  } finally {
    unregisterRuntimeMetadataObserver()
  }

  for (const scope of scopes) {
    assert.equal(
      await latencyModule.isNormalRouteAccountLatencyDegradedAsync(account, scope),
      false,
      'route_strategy_updated 被 settings_updated 覆盖后，最终通用 runtime-state 事件仍应通过 generation bump 失效全部旧状态'
    )
  }
  assert.deepEqual(
    await latencyStateStore.getJson('v1:generation'),
    {
      version: finalOverwrittenEvent.version,
      publishedAt: finalOverwrittenEvent.publishedAt
    },
    '任意 remote gateway runtime event 应写入其 version/publishedAt generation marker'
  )
  assert.deepEqual(
    observedRuntimeMetadata,
    {
      source: 'runtime_state',
      version: finalOverwrittenEvent.version,
      publishedAt: finalOverwrittenEvent.publishedAt
    },
    'runtime-state apply 应把 version/publishedAt 透传给异步 invalidator'
  )
  assert.equal(
    ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? []).length,
    2,
    'generation bump 不应清空旧 all-index'
  )
  assert.equal(
    ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:probe-index'))?.keys ?? []).length,
    2,
    'generation bump 不应清空旧 probe-index'
  )

  await latencyModule.recordNormalRouteFirstByteSlowAsync(account, scopes[0], config)
  await latencyModule.recordNormalRouteFirstByteSlowAsync(account, scopes[0], config)
  await invalidationStateStore.setJson('topic:gateway_runtime_cache', {
    version: 'go-runtime-version-delayed-older',
    reason: 'settings_updated',
    publishedAt: '2026-07-12T01:02:04.000Z'
  }, 60_000)
  await invalidationModule.syncGatewayCacheInvalidationsFromRuntimeState({ force: true })
  assert.equal(
    await latencyModule.isNormalRouteAccountLatencyDegradedAsync(account, scopes[0]),
    true,
    '延迟到达的旧 runtime-state event 不能失效较新 marker 后写入的 state'
  )
  assert.deepEqual(
    await latencyStateStore.getJson('v1:generation'),
    {
      version: finalOverwrittenEvent.version,
      publishedAt: finalOverwrittenEvent.publishedAt
    },
    '延迟旧 runtime-state event 不能回滚 generation marker'
  )
  const newerRuntimeEvent = {
    version: 'go-runtime-version-newer',
    reason: 'settings_updated',
    publishedAt: '2026-07-12T01:02:05.000Z'
  }
  await invalidationStateStore.setJson('topic:gateway_runtime_cache', newerRuntimeEvent, 60_000)
  await invalidationModule.syncGatewayCacheInvalidationsFromRuntimeState({ force: true })
  assert.equal(
    await latencyModule.isNormalRouteAccountLatencyDegradedAsync(account, scopes[0]),
    false,
    '较新 runtime-state event 应失效旧 marker state'
  )

  let deferredOverwriteRuntimeApplyCount = 0
  let deferredRemoteApplyCount = 0
  const unregisterDeferredOverwriteCounter =
    invalidationModule.registerGatewayRuntimeCacheInvalidator((reason, metadata) => {
      if (
        reason === 'runtime_state_deferred_before_local_overwrite'
        && metadata.source === 'runtime_state'
      ) {
        deferredRemoteApplyCount += 1
        return false
      }
      if (
        reason === 'deferred_local_publish_overwrite'
        && metadata.source === 'runtime_state'
      ) {
        deferredOverwriteRuntimeApplyCount += 1
      }
    })
  await invalidationStateStore.setJson('topic:gateway_runtime_cache', {
    version: 'go-runtime-version-handler-deferred',
    reason: 'runtime_state_deferred_before_local_overwrite',
    publishedAt: '2026-07-12T01:01:01.123Z'
  }, 60_000)
  await invalidationModule.syncGatewayCacheInvalidationsFromRuntimeState({ force: true })
  assert.equal(
    deferredRemoteApplyCount,
    1,
    'runtime-state handler 返回 false 时应标记 topic deferred'
  )
  invalidationModule.notifyGatewayRuntimeCacheInvalidation(
    'deferred_local_publish_overwrite'
  )
  let deferredOverwriteState:
    | { version?: string; reason?: string }
    | undefined
  for (let attempt = 0; attempt < 50; attempt += 1) {
    deferredOverwriteState = await invalidationStateStore.getJson<{
      version?: string
      reason?: string
    }>('topic:gateway_runtime_cache')
    if (deferredOverwriteState?.reason === 'deferred_local_publish_overwrite') {
      break
    }
    await delay(10)
  }
  assert.equal(
    deferredOverwriteState?.reason,
    'deferred_local_publish_overwrite',
    'local publish 应覆盖 deferred remote runtime-state 单槽'
  )
  assert(deferredOverwriteState.version, 'local overwrite 应生成当前槽 version')
  await invalidationModule.syncGatewayCacheInvalidationsFromRuntimeState({ force: true })
  assert.equal(
    deferredOverwriteRuntimeApplyCount,
    1,
    'deferred topic 被 local publish 覆盖后仍应 runtime-state apply 当前槽'
  )
  await invalidationModule.syncGatewayCacheInvalidationsFromRuntimeState({ force: true })
  assert.equal(
    deferredOverwriteRuntimeApplyCount,
    1,
    'deferred overwrite 成功后应推进当前槽 version，避免重复全清'
  )
  unregisterDeferredOverwriteCounter()

  const warnings: Array<{ fields?: Record<string, unknown>; message?: unknown }> = []
  const mutableLogger = logger as unknown as { warn: (...args: unknown[]) => unknown }
  const originalWarn = mutableLogger.warn
  mutableLogger.warn = (fields, message) => {
    warnings.push({
      fields: fields && typeof fields === 'object' ? fields as Record<string, unknown> : undefined,
      message
    })
  }
  let awaitedHandlerAttempts = 0
  let failingHandlerAttempts = 0
  let followingHandlerAttempts = 0
  let errorOverwriteRuntimeAttempts = 0
  const unregisterAwaited = invalidationModule.registerGatewayRuntimeCacheInvalidator(async (reason) => {
    if (reason !== 'async_invalidator_runtime_regression') return
    await delay(10)
    awaitedHandlerAttempts += 1
  })
  const unregisterFailing = invalidationModule.registerGatewayRuntimeCacheInvalidator(async (reason) => {
    if (reason === 'async_invalidator_runtime_regression') {
      failingHandlerAttempts += 1
      if (failingHandlerAttempts === 1) {
        throw new Error('async invalidator regression failure')
      }
    }
  })
  const unregisterFollowing = invalidationModule.registerGatewayRuntimeCacheInvalidator((reason, metadata) => {
    if (reason === 'async_invalidator_runtime_regression') {
      followingHandlerAttempts += 1
    }
    if (
      reason === 'async_error_local_publish_overwrite'
      && metadata.source === 'runtime_state'
    ) {
      errorOverwriteRuntimeAttempts += 1
    }
  })
  try {
    await invalidationStateStore.setJson('topic:gateway_runtime_cache', {
      version: 'go-runtime-version-async-invalidator',
      reason: 'async_invalidator_runtime_regression',
      publishedAt: '2026-07-12T01:04:05.678Z'
    }, 60_000)
    await assert.rejects(
      () => invalidationModule.syncGatewayCacheInvalidationsFromRuntimeState({ force: true }),
      'runtime-state handler 首次失败应让本次同步报告失败'
    )
    assert.equal(awaitedHandlerAttempts, 1, '首次失败前异步 handler 应被等待')
    assert.equal(failingHandlerAttempts, 1, '首次同步应执行失败 handler')
    assert.equal(followingHandlerAttempts, 1, '失败后仍应继续执行同组后续 handler')
    invalidationModule.notifyGatewayRuntimeCacheInvalidation(
      'async_error_local_publish_overwrite'
    )
    let errorOverwriteState:
      | { version?: string; reason?: string }
      | undefined
    for (let attempt = 0; attempt < 50; attempt += 1) {
      errorOverwriteState = await invalidationStateStore.getJson<{
        version?: string
        reason?: string
      }>('topic:gateway_runtime_cache')
      if (errorOverwriteState?.reason === 'async_error_local_publish_overwrite') {
        break
      }
      await delay(10)
    }
    assert.equal(
      errorOverwriteState?.reason,
      'async_error_local_publish_overwrite',
      '真实 handler error 后 local publish 应覆盖当前 runtime-state 单槽'
    )
    await invalidationModule.syncGatewayCacheInvalidationsFromRuntimeState({ force: true })
    await invalidationModule.syncGatewayCacheInvalidationsFromRuntimeState({ force: true })
  } finally {
    unregisterAwaited()
    unregisterFailing()
    unregisterFollowing()
  }
  assert.equal(awaitedHandlerAttempts, 1, '单槽被 local publish 覆盖后不应重放已被覆盖的旧 reason')
  assert.equal(failingHandlerAttempts, 1, '单槽被覆盖后不应重放旧失败 handler')
  assert.equal(followingHandlerAttempts, 1, '旧失败 reason 的后续 handler 只应执行一次')
  assert.equal(
    errorOverwriteRuntimeAttempts,
    1,
    '真实 handler error 必须标记 topic deferred，使下一次 sync 处理 local publish 覆盖后的当前槽'
  )
  const failureWarning = warnings.find((warning) => warning.fields?.reason === 'async_invalidator_runtime_regression')
  assert(failureWarning, '异步 invalidator 失败应记录可诊断日志')
  assert.equal(failureWarning.fields?.event, 'gateway_cache_invalidation_failed', '异步失败日志应保留统一事件名')
  assert.equal(failureWarning.fields?.cacheName, 'gateway_runtime_cache', '异步失败日志应包含缓存名称')
  assert.equal(
    (failureWarning.fields?.err as { message?: unknown } | undefined)?.message,
    'async invalidator regression failure',
    '异步失败日志应包含原始错误'
  )

  runtimeConfig.runtimeStateDriver = 'memory'
  let releaseLocalHandler: (() => void) | undefined
  let localHandlerCompleted = false
  let localFollowingHandlerCompleted = false
  const localPending = new Promise<void>((resolve) => {
    releaseLocalHandler = resolve
  })
  const unregisterLocalPending = invalidationModule.registerGatewayRuntimeCacheInvalidator(async (reason) => {
    if (reason !== 'local_async_invalidator_regression') return
    await localPending
    localHandlerCompleted = true
  })
  const unregisterLocalFailing = invalidationModule.registerGatewayRuntimeCacheInvalidator(async (reason) => {
    if (reason === 'local_async_invalidator_regression') {
      throw new Error('local async invalidator regression failure')
    }
  })
  const unregisterLocalFollowing = invalidationModule.registerGatewayRuntimeCacheInvalidator((reason) => {
    if (reason === 'local_async_invalidator_regression') {
      localFollowingHandlerCompleted = true
    }
  })
  try {
    assert.equal(
      invalidationModule.notifyGatewayRuntimeCacheInvalidation('local_async_invalidator_regression'),
      undefined,
      '本地 notify API 应保持同步 void'
    )
    assert.equal(localHandlerCompleted, false, '本地 notify 不应等待异步 invalidator')
    assert.equal(localFollowingHandlerCompleted, true, '本地 notify 应立即投递其他 invalidator')
    releaseLocalHandler?.()
    await localPending
    await delay(0)
    assert.equal(localHandlerCompleted, true, '本地异步 invalidator 应在后台完成')
    const localFailureWarning = warnings.find((warning) => warning.fields?.reason === 'local_async_invalidator_regression')
    assert(localFailureWarning, '本地异步 invalidator 失败应在后台记录可诊断日志')
    assert.equal(localFailureWarning.fields?.event, 'gateway_cache_invalidation_failed', '本地异步失败日志应保留统一事件名')
    assert.equal(localFailureWarning.fields?.cacheName, 'gateway_runtime_cache', '本地异步失败日志应包含缓存名称')
    assert.equal(
      (localFailureWarning.fields?.err as { message?: unknown } | undefined)?.message,
      'local async invalidator regression failure',
      '本地异步失败日志应包含原始错误'
    )
  } finally {
    unregisterLocalPending()
    unregisterLocalFailing()
    unregisterLocalFollowing()
    mutableLogger.warn = originalWarn
  }
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

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

function assertApiKeyLookupLocalCacheInvalidation(invalidationSource: string, lookupSource: string): void {
  assert.match(
    invalidationSource,
    /shouldInvalidateApiKeyLookupCache\(reason\)[\s\S]*clearLocalApiKeyLookupCache\(\)/,
    'gateway runtime listener 应按 API Key mutation reason 清空本进程 lookup LRU'
  )
  const helperBlock = sourceFunctionBlock(invalidationSource, 'function shouldInvalidateApiKeyLookupCache')
  const expectedReasons = ['api_key_created', 'api_key_updated', 'api_key_secret_refreshed', 'api_key_deleted']
  for (const reason of expectedReasons) {
    assert.match(helperBlock, new RegExp(`'${reason}'`), `API Key lookup runtime reason 缺少 ${reason}`)
  }
  const actualReasons = [...helperBlock.matchAll(/'([^']+)'/g)].map((match) => match[1]).sort()
  assert.deepEqual(actualReasons, [...expectedReasons].sort(), '只有 API Key create/update/refresh/delete reason 可以清本进程 lookup LRU')
  const clearBlock = sourceFunctionBlock(lookupSource, 'export function clearLocalApiKeyLookupCache')
  assert.match(clearBlock, /apiKeyLookupCache\.clear\(\)/, 'API Key lookup 本地清理 helper 必须清空本进程 LRU')
  assert.doesNotMatch(clearBlock, /apiKeyLookupSharedCache|clearLookupSharedCache/, 'runtime listener 不应重复清 shared lookup cache')
}

function assertRouteStrategyLatencyInvalidationBoundary(
  invalidationSource: string,
  runtimeCacheSource: string,
  latencyDegradationSource: string
): void {
  assert.doesNotMatch(
    invalidationSource,
    /normal-route-latency-degradation/,
    'shared gateway cache invalidation 不应反向依赖 gateway runtime 业务模块'
  )
  assert.match(
    runtimeCacheSource,
    /clearAllNormalRouteLatencyDegradationAsync/,
    'gateway runtime 稳定入口应注册 route strategy latency 全量清理'
  )
  assert.match(
    runtimeCacheSource,
    /metadata\.source !== 'runtime_state'/,
    'latency 全量清理只能响应 runtime-state 来源'
  )
  assert.doesNotMatch(
    runtimeCacheSource,
    /metadata\.source !== 'runtime_state' \|\| reason !== 'route_strategy_updated'/,
    '覆盖式 gateway runtime topic 不能依赖最终 reason 仍为 route_strategy_updated'
  )
  const clearAllBlock = sourceFunctionBlock(
    latencyDegradationSource,
    'export async function clearAllNormalRouteLatencyDegradationAsync'
  )
  assert.doesNotMatch(
    clearAllBlock,
    /while \(true\)/,
    'latency generation marker CAS 不能无限热循环'
  )
  assert.match(
    latencyDegradationSource,
    /const latencyStateGenerationCasMaxAttempts\s*=\s*\d+/,
    'latency generation marker CAS 应声明小的有界重试上限'
  )
  assert.match(
    clearAllBlock,
    /attempt < latencyStateGenerationCasMaxAttempts/,
    'latency generation marker 更新应使用有界 CAS retry loop'
  )
  assert.match(
    clearAllBlock,
    /compareLatencyGenerationEvents/,
    'latency generation marker 应按 publishedAt 和 version 有序比较'
  )
  assert.match(
    clearAllBlock,
    /latencyStateStore\.compareSetJson\(/,
    'generation marker 必须通过 RuntimeStateStore 原子 CAS 更新'
  )
  assert.match(
    clearAllBlock,
    /return false/,
    'generation marker CAS 重试耗尽应返回 deferred'
  )
  assert.doesNotMatch(
    clearAllBlock,
    /loadLatencyStateIndexKeys|latencyStateAllIndexKey|latencyStateProbeIndexKey/,
    'generation 全量失效不能读取 state/index 计算返回数量'
  )
  assert.doesNotMatch(
    clearAllBlock,
    /latencyStateStore\.(?:delete|incr|setJson)|removeLatencyStateIndexKeys|acquireLock/,
    'generation 全量失效不能删除 state/index、无条件 set 或依赖会过期的 generation lock'
  )
  assert.match(
    latencyDegradationSource,
    /const latencyStateGenerationTtlMs\s*=\s*48 \* 60 \* 60 \* 1000/,
    'generation TTL 应长于 state/index 最大 TTL'
  )
  assert.match(
    latencyDegradationSource,
    /interface NormalRouteLatencyState \{[\s\S]*generation: string/,
    'latency state 必须持久化 generation marker token'
  )
  const loadOrCreateBlock = sourceFunctionBlock(
    latencyDegradationSource,
    'async function loadOrCreateLatencyGenerationEvent'
  )
  assert.doesNotMatch(
    loadOrCreateBlock,
    /while \(true\)/,
    'generation marker 初始化 CAS 不能无限热循环'
  )
  assert.match(
    loadOrCreateBlock,
    /attempt < latencyStateGenerationCasMaxAttempts/,
    'generation marker 初始化应复用有界 CAS retry loop'
  )
  assert.match(
    loadOrCreateBlock,
    /throw new Error\([^)]*CAS[^)]*重试耗尽/,
    'generation marker 初始化 CAS 耗尽应抛出明确错误'
  )
  assert.match(
    runtimeCacheSource,
    /return clearAllNormalRouteLatencyDegradationAsync\(/,
    'runtime-state latency invalidator 必须透传 false deferred 结果'
  )
  const mutateIndexBlock = sourceFunctionBlock(
    latencyDegradationSource,
    'async function mutateLatencyStateIndexKeys'
  )
  assert.match(
    latencyDegradationSource,
    /const latencyStateIndexCasMaxAttempts\s*=\s*\d+/,
    'latency index CAS 应声明小的有界重试上限'
  )
  assert.match(
    mutateIndexBlock,
    /attempt < latencyStateIndexCasMaxAttempts/,
    'latency index RMW 应使用有界 CAS retry loop'
  )
  assert.match(
    mutateIndexBlock,
    /latencyStateStore\.compareSetJson\(/,
    'latency index 最终写入必须以精确旧快照 CAS 作为 correctness fence'
  )
  assert.doesNotMatch(
    mutateIndexBlock,
    /latencyStateStore\.setJson\(/,
    'latency index RMW 禁止用 plain setJson 覆盖并发成员'
  )
  assert.match(
    mutateIndexBlock,
    /throw new Error\([^)]*CAS[^)]*重试耗尽/,
    'latency index CAS 耗尽必须报告失败'
  )
  assert.match(
    latencyDegradationSource,
    /interface NormalRouteLatencyProbeCandidate \{[\s\S]*generation: string/,
    'probe candidate 必须携带 generation marker token，防止旧候选覆盖新代状态'
  )
  assert.doesNotMatch(
    latencyDegradationSource,
    /FullClear|fullClear|Barrier|barrier/,
    'generation 架构不应残留 full-clear lock/barrier 删除路径'
  )
  assert.match(
    latencyDegradationSource,
    /latencyStateMutationLockKey/,
    'route/account/probe 精确清理与 record/rebuild 应复用同一个 state mutation lock key'
  )
  assert.match(
    latencyDegradationSource,
    /state\.generation !== generation \|\| !predicate\(state\)/,
    '精确清理只能删除当前 generation 的目标状态'
  )
  assert.match(
    latencyDegradationSource,
    /latencyStateExactClearConcurrency/,
    'exact clear 应声明有限并发'
  )
  assert.match(
    latencyDegradationSource,
    /clearCurrentGenerationLatencyStateKeyAsync/,
    'exact clear 应逐 key 独立完成 acquire/read-delete-index/release'
  )
  assert.match(
    latencyDegradationSource,
    /const latencyStateMutationLockTtlMs\s*=\s*2 \* latencyStateLockAcquireMaxAttempts \* latencyStateLockAcquireMaxDelayMs[\s\S]*\+ 5000/,
    '所有 mutation lock TTL 应统一覆盖两个全局 index lock 的最坏等待'
  )
  assert.match(
    latencyDegradationSource,
    /renewLatencyStateGenerationAsync/,
    'mutation 在写 state/index 前应通过同值 CAS 条件续期当前 generation marker'
  )
  assert.doesNotMatch(
    latencyDegradationSource,
    /latencyStateExactClearMutationLockTtlMs/,
    'record 与 exact clear 不应再使用不同 mutation lease'
  )
  assert.doesNotMatch(
    latencyDegradationSource,
    /locks\.push\(await acquireLatencyStateMutationLockStrictAsync/,
    'exact clear 不能在等待后续 key 时批量持有前面 key 的 mutation lock'
  )
}

function assertRuntimeStateVersionCommitsAfterSuccessfulApply(source: string): void {
  const block = sourceFunctionBlock(source, 'async function syncGatewayCacheInvalidationsFromRuntimeStateUnsafe')
  const applyIndex = block.indexOf('await applyRuntimeStateCacheInvalidation(topic, state)')
  const commitIndex = block.indexOf('lastSeenGatewayCacheInvalidationVersions.set(topic, state.version)')
  assert(applyIndex >= 0, 'runtime-state 同步应等待 topic invalidation apply')
  assert(commitIndex > applyIndex, 'runtime-state topic version 只能在 apply 全部成功后推进')
}

function assertDeferredRuntimeStatePublishBoundary(source: string): void {
  assert.match(
    source,
    /deferredGatewayCacheInvalidationTopics\.add\(topic\)/,
    'runtime-state apply deferred 时应记录 deferred topic'
  )
  assert.match(
    source,
    /deferredGatewayCacheInvalidationTopics\.delete\(topic\)/,
    'runtime-state apply 成功时应清除 deferred topic'
  )
  const publishBlock = sourceFunctionBlock(
    source,
    'async function publishGatewayCacheInvalidationToRuntimeStateAsync'
  )
  assert.match(
    publishBlock,
    /if \(!deferredGatewayCacheInvalidationTopics\.has\(topic\)\)/,
    'local publish 仅在 topic 非 deferred 时写入 lastSeen'
  )
  const syncBlock = sourceFunctionBlock(
    source,
    'async function syncGatewayCacheInvalidationsFromRuntimeStateUnsafe'
  )
  assert.match(
    syncBlock,
    /catch \(error\) \{\s*deferredGatewayCacheInvalidationTopics\.add\(topic\)\s*throw error/,
    'runtime-state apply 抛错时必须先标记 deferred topic 再向上报告失败'
  )
}

function assertNotifyPublishesRuntimeState(source: string, functionName: string, topic: string): void {
  const block = sourceFunctionBlock(source, `export function ${functionName}`)
  assert.match(block, new RegExp(`publishGatewayCacheInvalidationToRuntimeState\\('${topic}'`), `${functionName} 必须发布 Redis runtime state 版本`)
  if (topic === 'gateway_runtime_cache') {
    assert.match(block, /runGatewayRuntimeCacheInvalidators\(reason\)/, `${functionName} 应保留本进程统一 listener 热清理`)
  } else {
    assert.match(block, /runCacheInvalidators/, `${functionName} 应保留本进程 handler 热清理`)
  }
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
