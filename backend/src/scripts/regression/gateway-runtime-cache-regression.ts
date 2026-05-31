import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

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
  gatewayCache
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/db-service/db-service-handlers.js'),
  import('../../modules/db-service/db-service-ipc.js'),
  import('../../modules/gateway/gateway-runtime-cache.service.js')
])

class FakeDbServiceChild extends EventEmitter {
  readonly pid = 424242
  readonly connected = true
  sentOperationCount = 0

  send(message: unknown, callback?: (error?: Error | null) => void): boolean {
    void this.handleMessage(message, callback)
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

  const database = databaseModule.getDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  let groupOwnerLookupCount = 0
  database.prepare = ((sql: string) => {
    const normalizedSql = sql.replace(/\s+/g, ' ')
    if (/\bFROM\s+groups\b/i.test(normalizedSql) && /\bWHERE\s+id\s*=\s*\?/i.test(normalizedSql)) {
      groupOwnerLookupCount += 1
    }
    return originalPrepare(sql)
  }) as typeof database.prepare
  let first: Awaited<ReturnType<typeof gatewayCache.readCachedGatewayRuntimeAsync>>
  try {
    first = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  } finally {
    database.prepare = originalPrepare
  }
  assert(first.apiKey?.id === apiKey.id, '首次读取应返回 API Key 运行配置')
  assert.equal(first.accounts.length, 2, '首次读取应返回同一分组内 OAuth/API Key 混合候选账号')
  assert.deepEqual(sortedAccountTypes(first.accounts), ['api_key', 'oauth'], '运行配置缓存不应按上游账号类型拆分候选账号')
  assert.equal(fakeChild.sentOperationCount, 1, '首次读取应请求 DB service')
  assert.equal(groupOwnerLookupCount, 1, 'read_gateway_runtime 应复用已解析的 groupAccess，避免账号选择阶段重复查询分组归属')

  const second = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert(second.apiKey?.id === apiKey.id, '第二次读取应仍返回 API Key 运行配置')
  assert.equal(second.accounts.length, 2, '第二次读取应从同一本地运行配置缓存返回 OAuth/API Key 混合候选账号')
  assert.deepEqual(sortedAccountTypes(second.accounts), ['api_key', 'oauth'], 'server 本地运行配置缓存命中后仍不应按账号类型拆分')
  assert.equal(fakeChild.sentOperationCount, 1, '第二次读取应命中网关 server 本地运行配置缓存')

  const updatedCredentials = {
    api_key: 'sk-runtime-cache-account-updated',
    base_url: 'http://127.0.0.1:9/v1'
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

  const invalidFirst = await gatewayCache.readCachedGatewayRuntimeAsync('sk-runtime-cache-invalid')
  const invalidSecond = await gatewayCache.readCachedGatewayRuntimeAsync('sk-runtime-cache-invalid')
  assert.equal(invalidFirst.apiKey, undefined, '无效 API Key 首次读取不应返回运行配置')
  assert.equal(invalidSecond.apiKey, undefined, '无效 API Key 缓存命中后仍不应返回运行配置')
  assert.equal(fakeChild.sentOperationCount, 6, '同一无效 API Key 短期重复认证失败应命中负缓存，避免重复请求 DB service')

  const scheduleActiveAt = Date.parse('2026-06-01T00:00:30.000Z')
  const scheduledFirst = await withMockedNow(scheduleActiveAt, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.scheduledKey))
  assert.equal(scheduledFirst.apiKey?.availability_schedule_active, 1, '计划允许时段内应返回可用 API Key')
  assert.equal(scheduledFirst.accounts.length, 2, '计划允许时段内应返回候选账号')
  assert.equal(fakeChild.sentOperationCount, 7, '首次读取计划 API Key 应请求 DB service')
  const scheduledSecond = await withMockedNow(scheduleActiveAt + 10_000, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.scheduledKey))
  assert.equal(scheduledSecond.apiKey?.availability_schedule_active, 1, '计划边界前应继续命中可用缓存')
  assert.equal(fakeChild.sentOperationCount, 7, '计划边界前重复读取应命中缓存')
  const scheduledAfterBoundary = await withMockedNow(Date.parse('2026-06-01T00:01:01.000Z'), () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.scheduledKey))
  assert.equal(scheduledAfterBoundary.apiKey?.availability_schedule_active, 0, '计划边界后应重新计算为停用')
  assert.equal(scheduledAfterBoundary.accounts.length, 0, '计划停用后不应返回候选账号')
  assert.equal(fakeChild.sentOperationCount, 8, '即使缓存被高频命中，计划边界后也应重新请求 DB service')

  const accountScheduledFirst = await withMockedNow(scheduleActiveAt, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.accountScheduledKey))
  assert.equal(accountScheduledFirst.apiKey?.availability_schedule_json, null, '账户计划用例不应依赖 API Key 自身计划')
  assert.equal(accountScheduledFirst.hasAccountAvailabilitySchedule, true, '运行配置应标记分组内存在账户自动启停计划')
  assert.equal(accountScheduledFirst.accounts.length, 1, '账户计划允许时段内应返回候选账号')
  assert.equal(fakeChild.sentOperationCount, 9, '首次读取账户计划 API Key 应请求 DB service')
  const accountScheduledSecond = await withMockedNow(scheduleActiveAt + 10_000, () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.accountScheduledKey))
  assert.equal(accountScheduledSecond.accounts.length, 1, '账户计划边界前应继续命中可用缓存')
  assert.equal(fakeChild.sentOperationCount, 9, '账户计划边界前重复读取应命中缓存')
  const accountScheduledAfterBoundary = await withMockedNow(Date.parse('2026-06-01T00:01:01.000Z'), () => gatewayCache.readCachedGatewayRuntimeAsync(apiKey.accountScheduledKey))
  assert.equal(accountScheduledAfterBoundary.accounts.length, 0, '账户计划停用后不应返回候选账号')
  assert.equal(fakeChild.sentOperationCount, 10, '只有账户计划存在时，计划边界后也应重新请求 DB service')

  console.log('网关运行配置缓存回归通过：server 按需缓存本地 API Key、分组和 OAuth/API Key 混合候选账号，清缓存后重新加载，对重复无效 Key 做短期负缓存，并在 API Key 与账户计划边界后重新计算运行态')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedGatewayRuntime(): { apiKeyAccountId: string; oauthAccountId: string; id: string; key: string; scheduledKey: string; accountScheduledKey: string } {
  const group = repositories.createGroup({
    name: '运行配置缓存混合账号分组',
    providerCode: 'openai',
    enabled: true
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const apiKeyAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '运行配置缓存 API Key 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-runtime-cache-account',
      base_url: 'http://127.0.0.1:9/v1'
    },
    groupId: group.id,
    status: 'active',
    concurrencyLimit: 20,
    schedulable: true
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const oauthAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '运行配置缓存 OAuth 账号',
    type: 'oauth',
    credentials: {
      refresh_token: 'refresh-runtime-cache-oauth',
      access_token: 'access-runtime-cache-oauth',
      expires_at: new Date(Date.now() + 3600_000).toISOString(),
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    status: 'active',
    concurrencyLimit: 20,
    schedulable: true
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const apiKey = repositories.createApiKeyRecord({
    name: '运行配置缓存 API Key',
    groupId: group.id
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const scheduledApiKey = repositories.createApiKeyRecord({
    name: '运行配置缓存计划 API Key',
    groupId: group.id,
    availabilitySchedule: {
      enabled: true,
      timezone: 'UTC',
      mode: 'allow_windows',
      windows: [
        { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '00:00', end: '00:01' }
      ]
    }
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const accountScheduledGroup = repositories.createGroup({
    name: '运行配置缓存账户计划分组',
    providerCode: 'openai',
    enabled: true
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  repositories.createAccount({
    providerCode: 'openai',
    name: '运行配置缓存账户计划账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-runtime-cache-account-scheduled',
      base_url: 'http://127.0.0.1:9/v1'
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
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const accountScheduledApiKey = repositories.createApiKeyRecord({
    name: '运行配置缓存账户计划 API Key',
    groupId: accountScheduledGroup.id
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  return {
    apiKeyAccountId: apiKeyAccount.id,
    oauthAccountId: oauthAccount.id,
    id: apiKey.id,
    key: apiKey.key,
    scheduledKey: scheduledApiKey.key,
    accountScheduledKey: accountScheduledApiKey.key
  }
}

function sortedAccountTypes(accounts: Array<{ type: string }>): string[] {
  return accounts.map((account) => account.type).sort()
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
  await runWithDbServiceParentMessageBridge(fakeChild, async () => {
    runtimeConfig.processRole = 'db-service'
    gatewayCache.clearGatewayRuntimeCache()
    await delay(10)
  })
}

async function runWithDbServiceParentMessageBridge<T>(fakeChild: FakeDbServiceChild, operation: () => Promise<T> | T): Promise<T> {
  const previousProcessRole = runtimeConfig.processRole
  const previousSend = process.send
  try {
    ;(process as typeof process & { send?: (message: unknown) => boolean }).send = (message: unknown) => {
      queueMicrotask(() => {
        const parentProcessRole = runtimeConfig.processRole
        runtimeConfig.processRole = 'server'
        try {
          fakeChild.emit('message', message)
        } finally {
          runtimeConfig.processRole = parentProcessRole
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
