import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '../../domain/provider-protocol.js'
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
  oauthRefreshService,
  accountPreparation,
  readWorkerPool
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/db-service/db-service-handlers.js'),
  import('../../modules/db-service/db-service-ipc.js'),
  import('../../modules/openai-oauth/openai-oauth-access-token-refresh.service.js'),
  import('../../modules/gateway/dispatch/account-preparation.js'),
  import('../../storage/sqlite-read-worker-pool.js')
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
  const group = repositories.createGroup({
    name: '请求链路 OAuth 热路径刷新分组',
    providerCode: GPT_VENDOR_CODE
  }, access)
  const account = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
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
    schedulable: true,
    groupId: group.id
  }, access)
  const accountRefreshTarget = {
    ...account,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION
  }

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
    oauthRefreshService.refreshOpenAIOAuthAccountAccessToken(accountRefreshTarget, { force: false, persistMode: 'db-service' }),
    oauthRefreshService.refreshOpenAIOAuthAccountAccessToken(accountRefreshTarget, { force: false, persistMode: 'db-service' }),
    oauthRefreshService.refreshOpenAIOAuthAccountAccessToken(accountRefreshTarget, { force: false, persistMode: 'db-service' })
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
    providerCode: GPT_VENDOR_CODE,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    type: 'oauth',
    credentials: refreshed[0].credentials
  }, { force: true, persistMode: 'db-service' })
  assert.equal(refreshCallCount, 2, '强制刷新应绕过最近刷新缓存')
  assert.equal(fakeChild.count('find_openai_oauth_account_for_refresh'), 2, '强制刷新仍应重新读取最新 OAuth 凭据')

  const nearExpiryAccount = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '请求链路 OAuth 临近过期预热账号',
    type: 'oauth',
    credentials: {
      access_token: 'access-near-stale',
      refresh_token: 'refresh-near-hot-path',
      expires_at: new Date(Date.now() + 30_000).toISOString(),
      client_id: 'client-near-hot-path',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    schedulable: true,
    groupId: group.id
  }, access)
  activateAccountForGatewayFixture(nearExpiryAccount.id)
  const nearRuntimeAccount = runtimeAccount(group.id, nearExpiryAccount.id)
  refreshCallCount = 0
  const beforeNearFindCount = fakeChild.count('find_openai_oauth_account_for_refresh')
  const beforeNearUpdateCount = fakeChild.count('update_openai_oauth_credentials')
  let releaseNearRefresh: (() => void) | undefined
  oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => {
    refreshCallCount += 1
    await new Promise<void>((resolve) => {
      releaseNearRefresh = resolve
    })
    return {
      accessToken: `access-preheated-${refreshCallCount}`,
      refreshToken,
      expiresIn: 3600,
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      clientId: clientId ?? 'client-near-hot-path'
    }
  })

  const nearPrepared = await accountPreparation.prepareUpstreamAccount(nearRuntimeAccount)
  assert.equal(nearPrepared.apiKey, 'access-near-stale', '临近过期但尚未到硬阈值的 OAuth token 不应阻塞当前请求')
  await waitUntil(() => refreshCallCount === 1, '临近过期 OAuth token 应触发一次后台预热')
  const nearPreparedAgain = await accountPreparation.prepareUpstreamAccount(nearRuntimeAccount)
  assert.equal(nearPreparedAgain.apiKey, 'access-near-stale', '预热进行中时当前请求仍应继续使用原 token')
  assert.equal(refreshCallCount, 1, '同一账号后台预热进行中时不应重复排队刷新')
  releaseNearRefresh?.()
  await waitUntil(
    () => fakeChild.count('update_openai_oauth_credentials') === beforeNearUpdateCount + 1,
    '临近过期 OAuth token 后台预热应最终写回新凭据'
  )
  assert.equal(
    fakeChild.count('find_openai_oauth_account_for_refresh'),
    beforeNearFindCount + 1,
    '临近过期 OAuth token 多个热路径请求只应触发一次 DB service 重读'
  )

  const expiredAccount = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '请求链路 OAuth 已过期阻塞刷新账号',
    type: 'oauth',
    credentials: {
      access_token: 'access-expired-stale',
      refresh_token: 'refresh-expired-hot-path',
      expires_at: new Date(Date.now() - 1_000).toISOString(),
      client_id: 'client-expired-hot-path',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    schedulable: true,
    groupId: group.id
  }, access)
  activateAccountForGatewayFixture(expiredAccount.id)
  const expiredRuntimeAccount = runtimeAccount(group.id, expiredAccount.id)
  refreshCallCount = 0
  oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => {
    refreshCallCount += 1
    await delay(20)
    return {
      accessToken: `access-expired-refreshed-${refreshCallCount}`,
      refreshToken,
      expiresIn: 3600,
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      clientId: clientId ?? 'client-expired-hot-path'
    }
  })
  const expiredPrepared = await accountPreparation.prepareUpstreamAccount(expiredRuntimeAccount)
  assert.equal(refreshCallCount, 1, '已过期 OAuth token 必须在当前请求内完成刷新')
  assert.equal(expiredPrepared.apiKey, 'access-expired-refreshed-1', '已过期 OAuth token 不应继续使用旧 access_token')

  console.log('OpenAI OAuth 请求链路懒刷新缓存回归通过：同账号并发临期刷新只读写一次 DB service，临近过期 token 改为后台预热，已过期 token 仍阻塞刷新')
} finally {
  oauthRefreshService.setOpenAIOAuthTokenRefresherForTest()
  oauthRefreshService.clearOpenAIOAuthRecentRefreshCache()
  await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  try {
    rmSync(tempRoot, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 })
  } catch (error) {
    const code = (error as NodeJS.ErrnoException).code
    if (code !== 'EBUSY' && code !== 'EPERM') throw error
  }
}

function runtimeAccount(groupId: string, accountId: string): ReturnType<typeof repositories.listOpenAIAccountsForGroup>[number] {
  const account = repositories.listOpenAIAccountsForGroup(groupId, 'sys_admin')
    .find((item) => item.id === accountId)
  assert(account, '应能读取已可调度 OAuth 账号运行时快照')
  return account
}

function activateAccountForGatewayFixture(accountId: string): void {
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET status = 'active', schedulable = 1
    WHERE id = ?
  `).run(accountId)
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

async function waitUntil(predicate: () => boolean, message: string, timeoutMs = 1000): Promise<void> {
  const startedAt = Date.now()
  while (!predicate()) {
    if (Date.now() - startedAt > timeoutMs) {
      throw new Error(message)
    }
    await delay(5)
  }
}
