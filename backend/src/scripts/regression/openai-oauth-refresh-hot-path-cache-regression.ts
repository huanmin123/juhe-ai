import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-oauth-refresh-hot-path-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'oauth-refresh-hot-path.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'oauth-refresh-hot-path-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  dbServiceHandlers,
  dbServiceIpc,
  oauthRefreshService
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/db-service/db-service-handlers.js'),
  import('../../modules/db-service/db-service-ipc.js'),
  import('../../modules/openai-oauth/openai-oauth-access-token-refresh.service.js')
])

class FakeDbServiceChild extends EventEmitter {
  readonly pid = 525252
  readonly connected = true
  readonly operationCounts = new Map<string, number>()

  send(message: unknown, callback?: (error?: Error | null) => void): boolean {
    void this.handleMessage(message, callback)
    return true
  }

  private async handleMessage(message: unknown, callback?: (error?: Error | null) => void): Promise<void> {
    if (!isDbServiceRequest(message)) {
      callback?.()
      return
    }
    const operationType = message.operation.type
    this.operationCounts.set(operationType, (this.operationCounts.get(operationType) ?? 0) + 1)
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

  count(operationType: string): number {
    return this.operationCounts.get(operationType) ?? 0
  }
}

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '请求链路 OAuth 临期刷新账号',
    type: 'oauth',
    credentials: {
      access_token: 'access-stale',
      refresh_token: 'refresh-hot-path',
      expires_at: new Date(Date.now() + 5_000).toISOString(),
      client_id: 'client-hot-path',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    schedulable: true
  }, access)

  let refreshCallCount = 0
  oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => {
    refreshCallCount += 1
    await delay(25)
    return {
      accessToken: `access-refreshed-${refreshCallCount}`,
      refreshToken,
      expiresIn: 3600,
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      clientId: clientId ?? 'client-hot-path'
    }
  })

  const fakeChild = new FakeDbServiceChild()
  runtimeConfig.processRole = 'server'
  dbServiceIpc.attachDbServiceProcess(fakeChild as never)
  fakeChild.emit('message', {
    type: 'db_service_ready',
    pid: fakeChild.pid,
    httpHost: '127.0.0.1',
    httpPort: 1
  })

  const refreshed = await Promise.all([
    oauthRefreshService.refreshOpenAIOAuthAccountAccessToken(account, { force: false, persistMode: 'db-service' }),
    oauthRefreshService.refreshOpenAIOAuthAccountAccessToken(account, { force: false, persistMode: 'db-service' }),
    oauthRefreshService.refreshOpenAIOAuthAccountAccessToken(account, { force: false, persistMode: 'db-service' })
  ])

  assert.equal(refreshCallCount, 1, '同一账号并发懒刷新应只请求一次上游 token endpoint')
  assert.equal(fakeChild.count('find_openai_oauth_account_for_refresh'), 1, '同一账号并发懒刷新应只通过 DB service 重读一次账户')
  assert.equal(fakeChild.count('update_openai_oauth_credentials'), 1, '同一账号并发懒刷新应只写回一次新凭据')
  assert.deepEqual(
    refreshed.map((item) => item.credentials.access_token),
    ['access-refreshed-1', 'access-refreshed-1', 'access-refreshed-1'],
    '排队请求应复用首个请求刚刷出的 Access Token'
  )

  await oauthRefreshService.refreshOpenAIOAuthAccountAccessToken({
    id: account.id,
    providerCode: 'openai',
    type: 'oauth',
    credentials: refreshed[0].credentials
  }, { force: true, persistMode: 'db-service' })
  assert.equal(refreshCallCount, 2, '强制刷新应绕过最近刷新缓存')
  assert.equal(fakeChild.count('find_openai_oauth_account_for_refresh'), 2, '强制刷新仍应重新读取最新 OAuth 凭据')

  console.log('OpenAI OAuth 请求链路懒刷新缓存回归通过：同账号并发临期刷新只读写一次 DB service，强制刷新不复用缓存')
} finally {
  oauthRefreshService.setOpenAIOAuthTokenRefresherForTest()
  oauthRefreshService.clearOpenAIOAuthRecentRefreshCache()
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
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

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
