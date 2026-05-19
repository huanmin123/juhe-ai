import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-runtime-cache-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'gateway-runtime-cache.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'gateway-runtime-cache-records.sqlite3')
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
    if (/\bSELECT\s+system_account_id\s+FROM\s+groups\s+WHERE\s+id\s*=\s*\?/i.test(sql.replace(/\s+/g, ' '))) {
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
  assert.equal(first.accounts.length, 1, '首次读取应返回候选账号')
  assert.equal(fakeChild.sentOperationCount, 1, '首次读取应请求 DB service')
  assert.equal(groupOwnerLookupCount, 1, 'read_gateway_runtime 应复用已解析的 groupAccess，避免账号选择阶段重复查询分组归属')

  const second = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert(second.apiKey?.id === apiKey.id, '第二次读取应仍返回 API Key 运行配置')
  assert.equal(fakeChild.sentOperationCount, 1, '第二次读取应命中网关 server 本地运行配置缓存')

  const updatedCredentials = {
    api_key: 'sk-runtime-cache-account-updated',
    base_url: 'http://127.0.0.1:9/v1'
  }
  const updateResult = await runWithDbServiceParentMessageBridge(fakeChild, () => dbServiceIpc.requestDbService({
    type: 'update_openai_oauth_credentials',
    accountId: apiKey.accountId,
    credentials: updatedCredentials
  }))
  assert.equal(updateResult.updated, true, 'DB service 直写账号凭据应成功')
  await delay(10)
  const reloadedAfterDbServiceStorageWrite = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert.equal(reloadedAfterDbServiceStorageWrite.accounts[0]?.apiKey, updatedCredentials.api_key, 'DB service 仓储写入后应清掉 server 本地运行配置缓存')
  assert.equal(fakeChild.sentOperationCount, 3, 'DB service 仓储写入触发失效后应重新请求 DB service')

  await simulateDbServiceRuntimeCacheInvalidation(fakeChild)
  const reloadedAfterDbServiceInvalidation = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert(reloadedAfterDbServiceInvalidation.apiKey?.id === apiKey.id, 'DB service 触发失效后读取应返回 API Key 运行配置')
  assert.equal(fakeChild.sentOperationCount, 4, 'DB service 触发失效后应清掉 server 本地运行配置缓存')

  gatewayCache.clearGatewayRuntimeCacheLocal()
  const third = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert(third.apiKey?.id === apiKey.id, '清缓存后读取应返回 API Key 运行配置')
  assert.equal(fakeChild.sentOperationCount, 5, '清缓存后应重新请求 DB service')

  console.log('网关运行配置缓存回归通过：server 按需缓存 API Key、分组和候选账号，清缓存后重新加载')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedGatewayRuntime(): { accountId: string; id: string; key: string } {
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '运行配置缓存账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-runtime-cache-account',
      base_url: 'http://127.0.0.1:9/v1'
    },
    status: 'active',
    concurrencyLimit: 20,
    schedulable: true
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  const apiKey = repositories.createApiKeyRecord({
    name: '运行配置缓存 API Key',
    groupId: account.boundGroupId
  }, { systemAccountId: 'sys_admin', role: 'admin' })
  return { accountId: account.id, id: apiKey.id, key: apiKey.key }
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

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
