import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { DEFAULT_GPT_GROUP } from '../../storage/schema-defaults.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-runtime-cache-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'gateway-runtime-cache.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-runtime-cache-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  dbServiceHandlers,
  dbServiceIpc,
  gatewayCache,
  readWorkerPool
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/db-service/db-service-handlers.js'),
  import('../../modules/db-service/db-service-ipc.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

class FakeDbServiceChild extends EventEmitter {
  readonly pid = 424242
  readonly connected = true
  sentOperationCount = 0
  private operationQueue = Promise.resolve()

  send(message: unknown, callback?: (error?: Error | null) => void): boolean {
    const operation = this.operationQueue.then(() => this.handleMessage(message, callback))
    this.operationQueue = operation.catch(() => undefined)
    return true
  }

  private async handleMessage(message: unknown, callback?: (error?: Error | null) => void): Promise<void> {
    if (!isDbServiceRequest(message)) {
      callback?.()
      return
    }
    this.sentOperationCount += 1
    const previousProcessRole = runtimeConfig.processRole
    try {
      runtimeConfig.processRole = 'db-service'
      const result = await dbServiceHandlers.handleDbServiceOperation(message.operation)
      queueMicrotask(() => {
        this.emit('message', {
          type: 'db_service_response',
          requestId: message.requestId,
          ok: true,
          result
        })
      })
      callback?.()
    } catch (error) {
      queueMicrotask(() => {
        this.emit('message', {
          type: 'db_service_response',
          requestId: message.requestId,
          ok: false,
          errorMessage: error instanceof Error ? error.message : String(error)
        })
      })
      callback?.()
    } finally {
      runtimeConfig.processRole = previousProcessRole
    }
  }
}

try {
  assertReadGatewayRuntimeDefersPolicyLists()
  assertGatewayRuntimeCacheUsesStaleWhileRevalidate()
  const apiKey = seedGatewayRuntime()
  const fakeChild = new FakeDbServiceChild()
  runtimeConfig.processRole = 'server'
  dbServiceIpc.attachDbServiceProcess(fakeChild as never)
  fakeChild.emit('message', {
    type: 'db_service_ready',
    pid: fakeChild.pid,
    httpHost: '127.0.0.1',
    httpPort: 1
  })

  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  let groupOwnerLookupCount = 0
  database.prepare = ((sql: string) => {
    const normalizedSql = sql.replace(/\s+/g, ' ')
    if (/\bFROM\s+groups\b/i.test(normalizedSql) && /\bWHERE\s+id\s*=\s*\?/i.test(normalizedSql)) {
      groupOwnerLookupCount += 1
    }
    return originalPrepare(sql)
  }) as typeof database.prepare
  const runtimeReadWorkerJobsBefore = readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs
  let first: Awaited<ReturnType<typeof gatewayCache.readCachedGatewayRuntimeAsync>>
  try {
    first = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  } finally {
    database.prepare = originalPrepare
  }
  assert(first.apiKey?.id === apiKey.id, '首次读取应返回 API Key 运行配置')
  assert.equal(first.accounts.length, 2, '首次读取应返回同一分组内 OAuth/API Key 混合候选账号')
  assert.deepEqual(sortedAccountTypes(first.accounts), ['api_key', 'oauth'], '运行配置缓存不应按上游账号类型拆分候选账号')
  assertRuntimeCredentialsAreSlim(first.accounts, apiKey)
  assert.equal(fakeChild.sentOperationCount, 1, '首次读取应请求 DB service')
  assert.equal(groupOwnerLookupCount, 0, 'read_gateway_runtime 冷加载不应在 DB service 主线程同步查询分组归属')
  assert(
    readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs >= runtimeReadWorkerJobsBefore + 1,
    'read_gateway_runtime 冷加载应进入 SQLite read worker'
  )

  const second = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert(second.apiKey?.id === apiKey.id, '第二次读取应仍返回 API Key 运行配置')
  assert.equal(second.accounts.length, 2, '第二次读取应从同一本地运行配置缓存返回 OAuth/API Key 混合候选账号')
  assert.deepEqual(sortedAccountTypes(second.accounts), ['api_key', 'oauth'], 'server 本地运行配置缓存命中后仍不应按账号类型拆分')
  assert.equal(fakeChild.sentOperationCount, 1, '第二次读取应命中网关 server 本地运行配置缓存')

  const updatedCredentials = {
    api_key: 'sk-runtime-cache-account-updated',
    base_url: 'https://api.openai.com/v1'
  }
  const updateResult = await runWithDbServiceParentMessageBridge(fakeChild, () => dbServiceIpc.requestDbService({
    type: 'update_openai_oauth_credentials',
    accountId: apiKey.apiKeyAccountId,
    credentials: updatedCredentials
  }))
  assert.equal(updateResult.updated, true, 'DB service 直写账号凭据应成功')
  await delay(10)
  const reloadedAfterDbServiceStorageWrite = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert.equal(accountById(reloadedAfterDbServiceStorageWrite.accounts, apiKey.apiKeyAccountId)?.apiKey, updatedCredentials.api_key, 'DB service 仓储写入后应清掉 server 本地运行配置缓存')
  assert.equal(accountById(reloadedAfterDbServiceStorageWrite.accounts, apiKey.oauthAccountId)?.apiKey, 'access-runtime-cache-oauth', 'API Key 账号刷新不应影响同一缓存边界内的 OAuth 候选账号')
  assert.equal(fakeChild.sentOperationCount, 3, 'DB service 仓储写入触发失效后应重新请求 DB service')

  await simulateDbServiceRuntimeCacheInvalidation(fakeChild)
  const reloadedAfterDbServiceInvalidation = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert(reloadedAfterDbServiceInvalidation.apiKey?.id === apiKey.id, 'DB service 触发失效后读取应返回 API Key 运行配置')
  assert.equal(fakeChild.sentOperationCount, 4, 'DB service 触发失效后应清掉 server 本地运行配置缓存')

  gatewayCache.clearGatewayRuntimeCacheLocal()
  const third = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert(third.apiKey?.id === apiKey.id, '清缓存后读取应返回 API Key 运行配置')
  assert.equal(fakeChild.sentOperationCount, 5, '清缓存后应重新请求 DB service')

  const invalidOperationCount = fakeChild.sentOperationCount
  const invalidFirst = await gatewayCache.readCachedGatewayRuntimeAsync('sk-runtime-cache-invalid')
  const invalidSecond = await gatewayCache.readCachedGatewayRuntimeAsync('sk-runtime-cache-invalid')
  assert.equal(invalidFirst.apiKey, undefined, '无效 API Key 首次读取不应返回运行配置')
  assert.equal(invalidSecond.apiKey, undefined, '无效 API Key 缓存命中后仍不应返回运行配置')
  assert.equal(fakeChild.sentOperationCount, invalidOperationCount + 1, '同一无效 API Key 短期重复认证失败应命中负缓存，避免重复请求 DB service')

  const scheduleActiveAt = Date.parse('2099-06-01T00:00:30.000Z')
  await syncApiKeyScheduleStatusAt(scheduleActiveAt)
  const scheduledOperationCount = fakeChild.sentOperationCount
  const scheduledFirst = await withMockedNow(scheduleActiveAt, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.scheduledKey))
  assert.equal(scheduledFirst.apiKey?.status, 'active', '计划允许时段内 API Key 状态应启用')
  assert.equal(scheduledFirst.accounts.length, 2, '计划允许时段内应返回候选账号')
  assert.equal(fakeChild.sentOperationCount, scheduledOperationCount + 1, '首次读取计划 API Key 应请求 DB service')
  const scheduledSecond = await withMockedNow(scheduleActiveAt + 10_000, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.scheduledKey))
  assert.equal(scheduledSecond.apiKey?.status, 'active', '计划边界前应继续命中可用缓存')
  assert.equal(fakeChild.sentOperationCount, scheduledOperationCount + 1, '计划边界前重复读取应命中缓存')
  database.prepare("UPDATE api_keys SET status = 'disabled' WHERE id = ?").run(apiKey.scheduledKeyId)
  clearGatewayCachesForRegression()
  await syncApiKeyScheduleStatusAt(scheduleActiveAt + 40_000)
  const scheduledAfterManualDisable = await withMockedNow(scheduleActiveAt + 40_000, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.scheduledKey))
  assert.equal(scheduledAfterManualDisable.apiKey, undefined, '开始边界执行过后，窗口中间手动停用不应被计划再次启用')
  assert.equal(fakeChild.sentOperationCount, scheduledOperationCount + 2, '窗口中间手动停用后读取应重新请求 DB service')
  await syncApiKeyScheduleStatusAt(Date.parse('2099-06-01T00:01:01.000Z'))
  const scheduledAfterBoundary = await withMockedNow(Date.parse('2099-06-01T00:01:01.000Z'), () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.scheduledKey))
  assert.equal(scheduledAfterBoundary.apiKey, undefined, '计划边界后同步任务写入停用状态，网关不应返回运行配置')
  assert.equal(scheduledAfterBoundary.accounts.length, 0, '时段外后不应返回候选账号')
  assert.equal(fakeChild.sentOperationCount, scheduledOperationCount + 3, '计划边界后同步任务清缓存，下一次读取应重新请求 DB service')
  database.prepare("UPDATE api_keys SET status = 'active' WHERE id = ?").run(apiKey.scheduledKeyId)
  clearGatewayCachesForRegression()
  await syncApiKeyScheduleStatusAt(Date.parse('2099-06-01T00:01:30.000Z'))
  const scheduledAfterManualEnable = await withMockedNow(Date.parse('2099-06-01T00:01:30.000Z'), () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.scheduledKey))
  assert.equal(scheduledAfterManualEnable.apiKey?.status, 'active', '结束边界执行过后，时段外手动启用应保留到下一次计划边界')
  assert.equal(scheduledAfterManualEnable.accounts.length, 2, '结束边界之后时段外手动启用应返回候选账号')
  assert.equal(fakeChild.sentOperationCount, scheduledOperationCount + 4, '结束边界之后时段外手动启用读取应重新请求 DB service')
  const groupAccountsAfterInactiveKeySchedule = await withMockedNow(Date.parse('2099-06-01T00:01:01.000Z'), () => gatewayCache.listCachedOpenAIAccountsForGroupAsync(apiKey.accountGroupId, 'sys_admin'))
  assert.equal(groupAccountsAfterInactiveKeySchedule.length, 2, 'API Key 时段外不应污染同分组账户候选缓存')

  const disabledScheduledOperationCount = fakeChild.sentOperationCount
  database.prepare("UPDATE api_keys SET status = 'disabled' WHERE id = ?").run(apiKey.disabledScheduledKeyId)
  const disabledScheduleActiveAt = Date.parse('2099-06-01T00:02:30.000Z')
  const disabledScheduleInactiveAt = Date.parse('2099-06-01T00:03:01.000Z')
  await syncApiKeyScheduleStatusAt(disabledScheduleActiveAt)
  const disabledScheduledFirst = await withMockedNow(disabledScheduleActiveAt, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.disabledScheduledKey))
  assert.equal(disabledScheduledFirst.apiKey?.status, 'active', '计划窗口开始边界应启用此前停用的 API Key')
  assert.equal(disabledScheduledFirst.accounts.length, 2, '计划窗口内启用的 API Key 应返回候选账号')
  assert.equal(fakeChild.sentOperationCount, disabledScheduledOperationCount + 1, '首次读取手动停用计划 API Key 应请求 DB service')
  const disabledScheduledSecond = await withMockedNow(disabledScheduleActiveAt + 5_000, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.disabledScheduledKey))
  assert.equal(disabledScheduledSecond.apiKey?.status, 'active', '计划启用后边界前应继续命中可用缓存')
  assert.equal(fakeChild.sentOperationCount, disabledScheduledOperationCount + 1, '计划启用后短期重复读取应命中缓存')
  await syncApiKeyScheduleStatusAt(disabledScheduleInactiveAt)
  const disabledScheduledAfterBoundary = await withMockedNow(disabledScheduleInactiveAt, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.disabledScheduledKey))
  assert.equal(disabledScheduledAfterBoundary.apiKey, undefined, '计划结束边界后 API Key 应不可用')
  assert.equal(disabledScheduledAfterBoundary.accounts.length, 0, '计划结束边界后不应返回候选账号')
  assert.equal(fakeChild.sentOperationCount, disabledScheduledOperationCount + 2, 'API Key 跨计划边界后应重新请求 DB service')

  gatewayCache.clearGatewayRuntimeCacheLocal()
  const unscheduledGroupListOperationCount = fakeChild.sentOperationCount
  const unscheduledGroupListFirst = await withMockedNow(scheduleActiveAt, () => gatewayCache.listCachedOpenAIAccountsForGroupAsync(apiKey.accountGroupId, 'sys_admin'))
  assert.equal(unscheduledGroupListFirst.length, 2, '无账户计划分组首次读取应返回候选账号')
  assert.equal(fakeChild.sentOperationCount, unscheduledGroupListOperationCount + 1, '无账户计划分组首次读取应请求 DB service')
  const unscheduledGroupListAfterMinute = await withMockedNow(Date.parse('2099-06-01T00:01:01.000Z'), () => gatewayCache.listCachedOpenAIAccountsForGroupAsync(apiKey.accountGroupId, 'sys_admin'))
  assert.equal(unscheduledGroupListAfterMinute.length, 2, '无账户计划分组跨分钟后仍应命中普通账号候选缓存')
  assert.equal(fakeChild.sentOperationCount, unscheduledGroupListOperationCount + 1, '无账户计划分组不应被计划分钟边界强制重新请求 DB service')

  await syncAccountScheduleStatusAt(scheduleActiveAt)
  const accountScheduleOperationCount = fakeChild.sentOperationCount
  assert.equal(runtimeConfig.processRole, 'server', '账户计划缓存用例前 processRole 应恢复为 server')
  const accountScheduledFirst = await withMockedNow(scheduleActiveAt, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.accountScheduledKey))
  assert(accountScheduledFirst.apiKey?.id, '账户计划用例不应依赖 API Key 自身计划')
  assert.equal(accountScheduledFirst.accounts.length, 1, '账户计划允许时段内应返回候选账号')
  assert.equal(fakeChild.sentOperationCount, accountScheduleOperationCount + 1, '首次读取账户计划 API Key 应请求 DB service')
  const accountScheduledSecond = await withMockedNow(scheduleActiveAt + 10_000, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.accountScheduledKey))
  assert.equal(accountScheduledSecond.accounts.length, 1, '后台同步前应继续命中可用缓存')
  assert.equal(fakeChild.sentOperationCount, accountScheduleOperationCount + 1, '后台同步前重复读取应命中缓存')
  await syncAccountScheduleStatusAt(Date.parse('2099-06-01T00:01:01.000Z'))
  const accountScheduledAfterBoundary = await withMockedNow(Date.parse('2099-06-01T00:01:01.000Z'), () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.accountScheduledKey))
  assert.equal(accountScheduledAfterBoundary.accounts.length, 0, '账户时段外后不应返回候选账号')
  assert.equal(fakeChild.sentOperationCount, accountScheduleOperationCount + 2, '账户计划后台同步清缓存后应重新请求 DB service')

  await syncAccountScheduleStatusAt(scheduleActiveAt)
  const multiGroupAccountScheduleOperationCount = fakeChild.sentOperationCount
  const multiGroupAccountScheduledFirst = await withMockedNow(scheduleActiveAt, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.multiGroupAccountScheduledKey))
  assert.equal(multiGroupAccountScheduledFirst.accounts.length, 0, '多分组全部因账户时段外时应返回空候选')
  assert.equal(fakeChild.sentOperationCount, multiGroupAccountScheduleOperationCount + 1, '首次读取多分组账户计划 API Key 应请求 DB service')
  const multiGroupAccountScheduledSecond = await withMockedNow(scheduleActiveAt + 10_000, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.multiGroupAccountScheduledKey))
  assert.equal(multiGroupAccountScheduledSecond.accounts.length, 0, '多分组账户计划后台同步前应继续命中空候选缓存')
  assert.equal(fakeChild.sentOperationCount, multiGroupAccountScheduleOperationCount + 1, '多分组账户计划后台同步前重复读取应命中缓存')
  await syncAccountScheduleStatusAt(Date.parse('2099-06-01T00:04:01.000Z'))
  const multiGroupAccountScheduledAfterBoundary = await withMockedNow(Date.parse('2099-06-01T00:04:01.000Z'), () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.multiGroupAccountScheduledKey))
  assert.equal(multiGroupAccountScheduledAfterBoundary.accounts.length, 1, '多分组账户计划进入允许时段后应重新返回候选账号')
  assert.equal(fakeChild.sentOperationCount, multiGroupAccountScheduleOperationCount + 2, '多分组账户计划后台同步后应重新请求 DB service，不能继续命中空运行配置')

  const expiringOperationCount = fakeChild.sentOperationCount
  const expiringFirst = await withMockedNow(scheduleActiveAt, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.expiringKey))
  assert.equal(expiringFirst.apiKey?.id, apiKey.expiringKeyId, 'API Key 过期前应返回运行配置')
  assert.equal(fakeChild.sentOperationCount, expiringOperationCount + 1, '首次读取临期 API Key 应请求 DB service')
  const expiringSecond = await withMockedNow(scheduleActiveAt + 10_000, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.expiringKey))
  assert.equal(expiringSecond.apiKey?.id, apiKey.expiringKeyId, 'API Key 过期前重复读取应命中运行配置缓存')
  assert.equal(fakeChild.sentOperationCount, expiringOperationCount + 1, 'API Key 过期前重复读取应命中缓存')
  const expiringAfterBoundary = await withMockedNow(Date.parse('2099-06-01T00:01:01.000Z'), () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.expiringKey))
  assert.equal(expiringAfterBoundary.apiKey, undefined, 'API Key 过期后不应被高频缓存命中续命')
  assert.equal(fakeChild.sentOperationCount, expiringOperationCount + 2, 'API Key 过期后应重新请求 DB service')
  await delay(10)
  runtimeConfig.processRole = 'server'

  const accountExpiringExpiresAtMs = Date.parse(apiKey.accountExpiringExpiresAt)
  const accountExpiringBeforeBoundary = accountExpiringExpiresAtMs - 20_000
  const accountExpiringAfterBoundary = accountExpiringExpiresAtMs + 1_000
  const accountExpiringOperationCount = fakeChild.sentOperationCount
  assert.equal(runtimeConfig.processRole, 'server', '账户到期缓存用例前 processRole 应恢复为 server')
  const accountExpiringFirst = await withMockedNow(accountExpiringBeforeBoundary, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.accountExpiringKey))
  assert.equal(accountExpiringFirst.accounts.length, 1, '账户到期前应返回候选账号')
  assert.equal(accountExpiringFirst.accounts[0]?.accountExpiresAt, apiKey.accountExpiringExpiresAt, '运行态候选应携带账户到期时间用于缓存边界')
  assert.equal(fakeChild.sentOperationCount, accountExpiringOperationCount + 1, '首次读取临期账户 API Key 应请求 DB service')
  const accountExpiringSecond = await withMockedNow(accountExpiringBeforeBoundary + 10_000, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.accountExpiringKey))
  assert.equal(accountExpiringSecond.accounts.length, 1, '账户到期前重复读取应命中运行配置缓存')
  assert.equal(fakeChild.sentOperationCount, accountExpiringOperationCount + 1, '账户到期前重复读取应命中缓存')
  const accountExpiringAfterBoundaryResult = await withMockedNow(accountExpiringAfterBoundary, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.accountExpiringKey))
  assert.equal(accountExpiringAfterBoundaryResult.accounts.length, 0, '账户到期后不应被运行配置缓存续命')
  assert.equal(fakeChild.sentOperationCount, accountExpiringOperationCount + 2, '账户到期后应重新请求 DB service')

  gatewayCache.clearGatewayRuntimeCacheLocal()
  const authorizationExpiresAt = new Date(Date.now() + 300).toISOString()
  repositories.updateResourceAuthorization(apiKey.authorizedGroupAuthorizationId, { expiresAt: authorizationExpiresAt }, { systemAccountId: 'sys_admin', role: 'admin' })
  const authorizedGroupAccessFirst = await gatewayCache.resolveCachedGroupUsageAccessMetadataAsync(apiKey.authorizedGroupId, apiKey.authorizedGranteeId)
  assert.equal(authorizedGroupAccessFirst?.groupAuthorizationExpiresAt, authorizationExpiresAt, '授权分组过期前应返回分组授权元数据')
  const authorizedAccountsFirst = await gatewayCache.listCachedOpenAIAccountsForGroupAsync(apiKey.authorizedGroupId, apiKey.authorizedGranteeId)
  assert.equal(authorizedAccountsFirst.length, 1, '授权分组过期前应返回授权账号候选')
  assert.equal(authorizedAccountsFirst[0]?.groupAuthorizationExpiresAt, authorizationExpiresAt, '授权账号候选应携带分组授权到期时间')
  const authorizationLoadedOperationCount = fakeChild.sentOperationCount
  const authorizedGroupAccessSecond = await gatewayCache.resolveCachedGroupUsageAccessMetadataAsync(apiKey.authorizedGroupId, apiKey.authorizedGranteeId)
  assert.equal(authorizedGroupAccessSecond?.groupAuthorizationId, authorizedGroupAccessFirst?.groupAuthorizationId, '授权过期边界前应命中分组授权元数据缓存')
  const authorizedAccountsSecond = await gatewayCache.listCachedOpenAIAccountsForGroupAsync(apiKey.authorizedGroupId, apiKey.authorizedGranteeId)
  assert.equal(authorizedAccountsSecond.length, 1, '授权过期边界前应命中授权账号候选缓存')
  assert.equal(fakeChild.sentOperationCount, authorizationLoadedOperationCount, '授权过期边界前分组元数据和账号候选重复读取应命中缓存')
  await delay(380)
  const authorizedGroupAccessAfterBoundary = await gatewayCache.resolveCachedGroupUsageAccessMetadataAsync(apiKey.authorizedGroupId, apiKey.authorizedGranteeId)
  assert.equal(authorizedGroupAccessAfterBoundary, undefined, '授权过期后分组元数据旁路缓存不应继续命中过期授权')
  const authorizedAccountsAfterBoundary = await gatewayCache.listCachedOpenAIAccountsForGroupAsync(apiKey.authorizedGroupId, apiKey.authorizedGranteeId)
  assert.equal(authorizedAccountsAfterBoundary.length, 0, '授权过期后账号候选旁路缓存不应继续返回过期授权账号')

  gatewayCache.clearGatewayRuntimeCacheLocal()
  const coldConcurrentOperationCount = fakeChild.sentOperationCount
  const concurrentRuntimeReads = await Promise.all([
    gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key),
    gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key),
    gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  ])
  assert(concurrentRuntimeReads.every((runtime) => runtime.apiKey?.id === apiKey.id), '冷缓存并发读取应全部返回同一个 API Key 运行配置')
  assert.equal(fakeChild.sentOperationCount, coldConcurrentOperationCount + 1, '同一 API Key 冷缓存并发读取应合并为一次 DB service 请求')

  console.log('网关运行配置缓存回归通过：server 按需缓存本地 API Key、分组和 OAuth/API Key 混合候选账号，运行态软过期后请求继续使用内存快照并后台刷新，清缓存后重新加载，对重复无效 Key 做短期负缓存，无计划 API Key 停用不污染分组账号缓存，API Key 和账户时间计划都由后台同步任务维护单一状态，无账户计划分组不被分钟边界误伤，并在 API Key、账户计划同步、API Key 过期、账户到期和授权过期边界后重新计算运行态，同 Key 冷缓存并发读取只请求一次 DB service')
} finally {
  await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedGatewayRuntime(): {
  apiKeyAccountId: string
  oauthAccountId: string
  accountGroupId: string
  id: string
  key: string
  scheduledKeyId: string
  scheduledKey: string
  disabledScheduledKeyId: string
  disabledScheduledKey: string
  accountScheduledKey: string
  multiGroupAccountScheduledKey: string
  accountExpiringKey: string
  accountExpiringExpiresAt: string
  expiringKeyId: string
  expiringKey: string
  authorizedGroupId: string
  authorizedGranteeId: string
  authorizedGroupAuthorizationId: string
} {
  const group = repositories.createGroup({
    name: '运行配置缓存混合账号分组',
    providerCode: DEFAULT_GPT_GROUP.providerCode,
    enabled: true
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const apiKeyAccount = repositories.createAccount({
    providerCode: DEFAULT_GPT_GROUP.providerCode,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '运行配置缓存 API Key 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-runtime-cache-account',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    status: 'active',
    concurrencyLimit: 20,
    schedulable: true
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const oauthAccount = repositories.createAccount({
    providerCode: DEFAULT_GPT_GROUP.providerCode,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '运行配置缓存 OAuth 账号',
    type: 'oauth',
    credentials: {
      refresh_token: 'refresh-runtime-cache-oauth',
      access_token: 'access-runtime-cache-oauth',
      expires_at: '2100-01-01T00:00:00.000Z',
      account_id: 'acct_runtime_cache_oauth',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    status: 'active',
    concurrencyLimit: 20,
    schedulable: true
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '运行配置缓存 API Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const scheduledApiKey = withMockedNowSync(Date.parse('2099-05-31T23:59:30.000Z'), () => createApiKeyRecordWithRouteStrategy(repositories, {
    name: '运行配置缓存计划 API Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    availabilitySchedule: {
      enabled: true,
      timezone: 'UTC',
      mode: 'allow_windows',
      windows: [
        { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '00:00', end: '00:01' }
      ]
    }
  }, { systemAccountId: 'sys_admin', role: 'admin' }))
  const disabledScheduledApiKey = withMockedNowSync(Date.parse('2099-06-01T00:01:30.000Z'), () => createApiKeyRecordWithRouteStrategy(repositories, {
    name: '运行配置缓存手动停用计划 API Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'disabled',
    availabilitySchedule: {
      enabled: true,
      timezone: 'UTC',
      mode: 'allow_windows',
      windows: [
        { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '00:02', end: '00:03' }
      ]
    }
  }, { systemAccountId: 'sys_admin', role: 'admin' }))
  const expiringApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '运行配置缓存临期 API Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    expiresAt: '2099-06-01T00:01:00.000Z'
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const authorizedGrantee = repositories.createSystemAccount({
    username: 'gateway_runtime_cache_auth_grantee',
    displayName: '运行配置缓存授权被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const authorizedGroup = repositories.createGroup({
    name: '运行配置缓存临期授权分组',
    providerCode: DEFAULT_GPT_GROUP.providerCode,
    enabled: true
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  repositories.createAccount({
    providerCode: DEFAULT_GPT_GROUP.providerCode,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '运行配置缓存临期授权账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-runtime-cache-authorized-group',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: authorizedGroup.id,
    status: 'active',
    concurrencyLimit: 20,
    schedulable: true
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const authorizedGroupAuthorization = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: authorizedGroup.id,
    granteeType: 'system_account',
    granteeId: authorizedGrantee.id,
    expiresAt: '2100-01-01T00:01:00.000Z'
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const accountScheduledGroup = repositories.createGroup({
    name: '运行配置缓存账户计划分组',
    providerCode: DEFAULT_GPT_GROUP.providerCode,
    enabled: true
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  withMockedNowSync(Date.parse('2099-05-31T23:59:30.000Z'), () => repositories.createAccount({
    providerCode: DEFAULT_GPT_GROUP.providerCode,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '运行配置缓存账户计划账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-runtime-cache-account-scheduled',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: accountScheduledGroup.id,
    status: 'active',
    concurrencyLimit: 20,
    schedulable: true,
    availabilitySchedule: {
      enabled: true,
      timezone: 'UTC',
      mode: 'allow_windows',
      windows: [
        { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '00:00', end: '00:01' }
      ]
    }
  }, { systemAccountId: 'sys_admin', role: 'admin' }))
  const accountScheduledApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '运行配置缓存账户计划 API Key',
    groupBindings: [{ groupId: accountScheduledGroup.id, priority: 1, status: 'active' }],
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const accountExpiringGroup = repositories.createGroup({
    name: '运行配置缓存账户到期分组',
    providerCode: DEFAULT_GPT_GROUP.providerCode,
    enabled: true
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const accountExpiringExpiresAt = new Date(Date.now() + 30_000).toISOString()
  repositories.createAccount({
    providerCode: DEFAULT_GPT_GROUP.providerCode,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '运行配置缓存临期账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-runtime-cache-account-expiring',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: accountExpiringGroup.id,
    status: 'active',
    concurrencyLimit: 20,
    schedulable: true,
    accountExpiresAt: accountExpiringExpiresAt
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const accountExpiringApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '运行配置缓存账户到期 API Key',
    groupBindings: [{ groupId: accountExpiringGroup.id, priority: 1, status: 'active' }],
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const multiGroupAccountScheduledPrimaryGroup = repositories.createGroup({
    name: '运行配置缓存多分组账户计划主分组',
    providerCode: DEFAULT_GPT_GROUP.providerCode,
    enabled: true
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const multiGroupAccountScheduledFallbackGroup = repositories.createGroup({
    name: '运行配置缓存多分组账户计划备用分组',
    providerCode: DEFAULT_GPT_GROUP.providerCode,
    enabled: true
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  for (const [index, groupId] of [multiGroupAccountScheduledPrimaryGroup.id, multiGroupAccountScheduledFallbackGroup.id].entries()) {
    withMockedNowSync(Date.parse('2099-06-01T00:03:30.000Z'), () => repositories.createAccount({
      providerCode: DEFAULT_GPT_GROUP.providerCode,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `运行配置缓存多分组账户计划账号 ${index + 1}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-runtime-cache-multi-account-scheduled-${index + 1}`,
        base_url: 'https://api.openai.com/v1'
      },
      groupId,
      status: 'active',
      concurrencyLimit: 20,
      schedulable: true,
      availabilitySchedule: {
        enabled: true,
        timezone: 'UTC',
        mode: 'allow_windows',
        windows: [
          { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '00:04', end: '00:05' }
        ]
      }
    }, { systemAccountId: 'sys_admin', role: 'admin' }))
  }
  const multiGroupAccountScheduledApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '运行配置缓存多分组账户计划 API Key',
    groupRouteStrategy: 'priority_failover',
    groupBindings: [
      { groupId: multiGroupAccountScheduledPrimaryGroup.id, priority: 1, status: 'active' },
      { groupId: multiGroupAccountScheduledFallbackGroup.id, priority: 2, status: 'active' }
    ]
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  return {
    apiKeyAccountId: apiKeyAccount.id,
    oauthAccountId: oauthAccount.id,
    accountGroupId: group.id,
    id: apiKey.id,
    key: apiKey.key,
    scheduledKeyId: scheduledApiKey.id,
    scheduledKey: scheduledApiKey.key,
    disabledScheduledKeyId: disabledScheduledApiKey.id,
    disabledScheduledKey: disabledScheduledApiKey.key,
    accountScheduledKey: accountScheduledApiKey.key,
    multiGroupAccountScheduledKey: multiGroupAccountScheduledApiKey.key,
    accountExpiringKey: accountExpiringApiKey.key,
    accountExpiringExpiresAt,
    expiringKeyId: expiringApiKey.id,
    expiringKey: expiringApiKey.key,
    authorizedGroupId: authorizedGroup.id,
    authorizedGranteeId: authorizedGrantee.id,
    authorizedGroupAuthorizationId: authorizedGroupAuthorization.id
  }
}

function sortedAccountTypes(accounts: Array<{ type: string }>): string[] {
  return accounts.map((account) => account.type).sort()
}

function assertRuntimeCredentialsAreSlim(
  accounts: Array<{ id: string; apiKey: string; credentials: Record<string, unknown> }>,
  seed: { apiKeyAccountId: string; oauthAccountId: string }
): void {
  const apiKeyAccount = accountById(accounts, seed.apiKeyAccountId)
  const oauthAccount = accountById(accounts, seed.oauthAccountId)
  assert(apiKeyAccount, '运行配置应包含 API Key 账号')
  assert(oauthAccount, '运行配置应包含 OAuth 账号')
  assert.equal(apiKeyAccount.apiKey, 'sk-runtime-cache-account', '运行时账号顶层仍应保留转发所需 API Key')
  assert.equal(oauthAccount.apiKey, 'access-runtime-cache-oauth', '运行时 OAuth 账号顶层仍应保留转发所需 Access Token')
  assert.equal(oauthAccount.credentials.account_id, 'acct_runtime_cache_oauth', '运行时 OAuth 账号应保留 Codex 必需 account_id')
  for (const [account, label] of [[apiKeyAccount, 'API Key'], [oauthAccount, 'OAuth']] as const) {
    for (const field of ['api_key', 'access_token', 'refresh_token', 'base_url', 'client_id', 'expires_at']) {
      assert.equal(Object.prototype.hasOwnProperty.call(account.credentials, field), false, `运行时 ${label} credentials 不应携带完整 ${field}`)
    }
  }
}

function assertReadGatewayRuntimeDefersPolicyLists(): void {
  const handlersSource = readFileSync(new URL('../../modules/db-service/db-service-handlers.ts', import.meta.url), 'utf8')
  const readRuntimeBody = sourceFunctionBlock(handlersSource, 'function readGatewayRuntime')
  const validateIndex = readRuntimeBody.indexOf('validateGatewayApiKey')
  const responseInspectionPolicyIndex = readRuntimeBody.indexOf('listActiveResponseInspectionPoliciesForAccounts')
  assert(validateIndex >= 0, 'read_gateway_runtime 应先验证 API Key')
  assert(responseInspectionPolicyIndex > validateIndex, 'read_gateway_runtime 不能在验证 API Key 前加载全量响应检查策略')
  assert(!readRuntimeBody.includes('listActiveClientIpPolicies'), 'read_gateway_runtime 不能携带全量 active IP 封禁策略')
}

function assertGatewayRuntimeCacheUsesStaleWhileRevalidate(): void {
  const source = readFileSync(new URL('../../modules/gateway/runtime/runtime-cache.service.ts', import.meta.url), 'utf8')
  assert.match(source, /export const gatewayRuntimeDbServiceTimeoutMs = 10_000/, '网关运行态冷缓存 DB service 读取应使用独立超时，避免默认 5s 过早 503')
  assert.match(source, /type:\s*'read_gateway_runtime'[\s\S]*timeoutMs:\s*gatewayRuntimeDbServiceTimeoutMs/, 'read_gateway_runtime 请求必须传入独立 DB service timeout')
  assert.match(source, /gatewayRuntimeRetainTtlMs\s*=\s*10\s*\*\s*60_000/, '网关运行态缓存应使用长保留窗口，避免软过期后请求链路硬 miss 等 DB')
  assert.match(source, /refreshGatewayRuntimeInBackground\(apiKey,\s*cacheKey\)/, '网关运行态软过期应触发后台刷新')
  assert.match(source, /sanitizedGatewayRuntimeForDispatch\(cached\.runtime\)/, '软过期运行态返回前必须按当前时间过滤过期 API Key、授权和账号')
  assert.match(sourceFunctionBlock(source, 'export async function readCachedGatewayRuntimeAsync'), /isGatewayRuntimeCacheEntryFresh\(cached\)[\s\S]*const runtime = sanitizedGatewayRuntimeForDispatch\(cached\.runtime\)/, '新鲜命中的运行态返回前也必须按当前时间过滤过期 API Key、授权和账号')
  assert.match(source, /groupUsageAccessRetainTtlMs\s*=\s*10\s*\*\s*60_000/, '分组访问缓存应软过期保留，动态路由不能在 TTL 边界硬 miss 等 DB')
  assert.match(source, /openAIAccountsRetainTtlMs\s*=\s*10\s*\*\s*60_000/, '候选账号缓存应软过期保留，动态路由不能在 TTL 边界硬 miss 等 DB')
  assert.match(source, /refreshOpenAIAccountsForGroupInBackground/, '候选账号缓存软过期应后台刷新')
  assert.match(source, /function isOpenAIAccountRuntimeUsableAt/, '软过期账号快照必须在内存中判断到期边界')
}

function sourceFunctionBlock(source: string, marker: string): string {
  const start = source.indexOf(marker)
  assert(start >= 0, `未找到源码片段：${marker}`)
  const nextFunction = source.indexOf('\nfunction ', start + marker.length)
  return source.slice(start, nextFunction === -1 ? undefined : nextFunction)
}

function accountById<T extends { id: string }>(accounts: T[], accountId: string): T | undefined {
  return accounts.find((account) => account.id === accountId)
}

function isDbServiceRequest(value: unknown): value is { type: 'db_service_request'; requestId: string; operation: Parameters<typeof dbServiceHandlers.handleDbServiceOperation>[0] } {
  return typeof value === 'object'
    && value !== null
    && !Array.isArray(value)
    && (value as Record<string, unknown>).type === 'db_service_request'
    && typeof (value as Record<string, unknown>).requestId === 'string'
    && typeof (value as Record<string, unknown>).operation === 'object'
    && (value as Record<string, unknown>).operation !== null
}

async function simulateDbServiceRuntimeCacheInvalidation(fakeChild: FakeDbServiceChild): Promise<void> {
  const previousProcessRole = runtimeConfig.processRole
  try {
    await runWithDbServiceParentMessageBridge(fakeChild, async () => {
      runtimeConfig.processRole = 'db-service'
      gatewayCache.clearGatewayRuntimeCache()
      await delay(10)
    })
  } finally {
    runtimeConfig.processRole = previousProcessRole
  }
}

async function runWithDbServiceParentMessageBridge<T>(fakeChild: FakeDbServiceChild, operation: () => Promise<T> | T): Promise<T> {
  const previousProcessRole = runtimeConfig.processRole
  const previousSend = process.send
  try {
    ;(process as typeof process & { send?: (message: unknown) => boolean }).send = (message: unknown) => {
      queueMicrotask(() => {
        runtimeConfig.processRole = 'server'
        try {
          fakeChild.emit('message', message)
        } finally {
          runtimeConfig.processRole = previousProcessRole
        }
      })
      return true
    }
    return await operation()
  } finally {
    runtimeConfig.processRole = previousProcessRole
    ;(process as typeof process & { send?: (message: unknown) => boolean }).send = previousSend
  }
}

async function syncApiKeyScheduleStatusAt(nowMs: number): Promise<void> {
  await runScheduleSyncAsDbService(() => repositories.syncApiKeyAvailabilityScheduleStatuses(new Date(nowMs)))
}

async function syncAccountScheduleStatusAt(nowMs: number): Promise<void> {
  await runScheduleSyncAsDbService(() => repositories.syncAccountAvailabilityScheduleStatuses(new Date(nowMs)))
}

async function runScheduleSyncAsDbService(operation: () => void): Promise<void> {
  const previousProcessRole = runtimeConfig.processRole
  try {
    runtimeConfig.processRole = 'db-service'
    operation()
  } finally {
    runtimeConfig.processRole = previousProcessRole
  }
  await settleGatewayRuntimeInvalidationEffectsForRegression()
  clearGatewayCachesForRegression()
  await settleGatewayRuntimeInvalidationEffectsForRegression()
}

function clearGatewayCachesForRegression(): void {
  runtimeConfig.processRole = 'server'
  repositories.clearGatewayApiKeyValidationCache()
  gatewayCache.clearGatewayRuntimeCacheLocal()
}

async function settleGatewayRuntimeInvalidationEffectsForRegression(): Promise<void> {
  for (let index = 0; index < 5; index += 1) {
    await delay(0)
  }
}

function withMockedNowSync<T>(nowMs: number, operation: () => T): T {
  const OriginalDate = Date
  const MockedDate = class extends OriginalDate {
    constructor(value?: string | number | Date, month?: number, date?: number, hours?: number, minutes?: number, seconds?: number, ms?: number) {
      if (arguments.length === 0) {
        super(nowMs)
        return
      }
      if (arguments.length === 1) {
        super(value as string | number | Date)
        return
      }
      super(value as number, month as number, date, hours, minutes, seconds, ms)
    }

    static now(): number {
      return nowMs
    }
  }
  Object.defineProperty(globalThis, 'Date', {
    configurable: true,
    writable: true,
    value: MockedDate
  })
  try {
    return operation()
  } finally {
    Object.defineProperty(globalThis, 'Date', {
      configurable: true,
      writable: true,
      value: OriginalDate
    })
  }
}

async function withMockedNow<T>(nowMs: number, operation: () => Promise<T> | T): Promise<T> {
  const OriginalDate = Date
  const MockedDate = class extends OriginalDate {
    constructor(value?: string | number | Date, month?: number, date?: number, hours?: number, minutes?: number, seconds?: number, ms?: number) {
      if (arguments.length === 0) {
        super(nowMs)
        return
      }
      if (arguments.length === 1) {
        super(value as string | number | Date)
        return
      }
      super(value as number, month as number, date, hours, minutes, seconds, ms)
    }

    static now(): number {
      return nowMs
    }
  }
  Object.defineProperty(globalThis, 'Date', {
    configurable: true,
    writable: true,
    value: MockedDate
  })
  try {
    return await operation()
  } finally {
    Object.defineProperty(globalThis, 'Date', {
      configurable: true,
      writable: true,
      value: OriginalDate
    })
  }
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
