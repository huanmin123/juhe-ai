import { mkdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-oauth-token-refresh-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'oauth-token-refresh.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'oauth-token-refresh-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  oauthRefreshService
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/openai-oauth/openai-oauth-access-token-refresh.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const refreshedByToken = new Set<string>()

async function main(): Promise<void> {
  try {
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => {
      refreshedByToken.add(refreshToken)
      return {
        accessToken: `access-refreshed-${refreshToken}`,
        refreshToken: `refresh-refreshed-${refreshToken}`,
        expiresIn: 3600,
        expiresAt: new Date(Date.now() + 3600_000).toISOString(),
        clientId: clientId ?? 'test-client'
      }
    })

    const dueAccounts = [
      createOAuthAccount('正常调度账户', 'active', true, 'active-token'),
      createOAuthAccount('停用账户', 'disabled', false, 'disabled-token'),
      createOAuthAccount('临时不可调用账户', 'temporary_unavailable', true, 'temporary-token'),
      createOAuthAccount('错误账户', 'error', false, 'error-token')
    ]
    const apiKeyAccount = repositories.createAccount({
      providerCode: 'openai',
      name: 'API Key 不应参与 OAuth 刷新',
      type: 'api_key',
      credentials: { api_key: 'sk-not-oauth', base_url: 'https://api.openai.com/v1' },
      status: 'active',
      schedulable: true
    }, access)
    const freshOAuthAccount = repositories.createAccount({
      providerCode: 'openai',
      name: '未到期 OAuth 账户',
      type: 'oauth',
      credentials: oauthCredentials('fresh-token', new Date(Date.now() + 3600_000).toISOString()),
      status: 'active',
      schedulable: true
    }, access)

    const result = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 20,
      retryBackoffSeconds: 60
    })

    assert(result.scanned === 5, `应扫描全部未删除 OAuth 账户，实际 ${result.scanned}`)
    assert(result.due === 4, `应刷新 4 个快过期 OAuth 账户，实际 ${result.due}`)
    assert(result.refreshed === 4, `应成功刷新 4 个账户，实际 ${result.refreshed}`)
    assert(result.failed === 0, `不应有刷新失败，实际 ${result.failed}`)
    assert(result.exceptioned === 0, `成功刷新不应标记异常，实际 ${result.exceptioned}`)
    assert(result.cooldowned === 0, `后台保活成功不应改变账号冷却状态，实际 ${result.cooldowned}`)

    assert(!refreshedByToken.has('fresh-token'), '未到期 OAuth 账户不应刷新')
    assert(!refreshedByToken.has('sk-not-oauth'), 'API Key 账户不应参与 OAuth 刷新')
    assert(repositories.listAccounts().some((account) => account.id === apiKeyAccount.id), 'API Key 对照账户应仍存在')
    assert(repositories.listAccounts().some((account) => account.id === freshOAuthAccount.id), '未到期 OAuth 对照账户应仍存在')

    for (const account of dueAccounts) {
      const latest = repositories.findAccountForTest(account.id, access)
      assert(latest, `账户 ${account.name} 不存在`)
      assert(latest.status === account.status, `后台刷新不应改变 ${account.name} 状态：${latest.status}`)
      assert(latest.schedulable === account.schedulable, `后台刷新不应改变 ${account.name} 调度标记`)
      assert(latest.credentials.access_token === `access-refreshed-${account.originalRefreshToken}`, `${account.name} access_token 未刷新`)
      assert(latest.credentials.refresh_token === `refresh-refreshed-${account.originalRefreshToken}`, `${account.name} refresh_token 未刷新`)
    }

    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async () => {
      throw new Error('模拟刷新失败')
    })

    const failedActive = createOAuthAccount('连续刷新失败账户', 'active', true, 'active-fail-token', {
      accessToken: undefined,
      expiresAt: new Date(Date.now() - 60_000).toISOString()
    })
    const failedDisabled = createOAuthAccount('停用账户连续刷新失败', 'disabled', false, 'disabled-fail-token', {
      accessToken: undefined,
      expiresAt: new Date(Date.now() - 60_000).toISOString()
    })

    for (let attempt = 1; attempt <= 3; attempt += 1) {
      const failedResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
        leadSeconds: 300,
        batchSize: 20,
        retryBackoffSeconds: 0
      })
      assert(failedResult.failed === 2, `第 ${attempt} 次应记录 2 个刷新失败，实际 ${failedResult.failed}`)
      assert(failedResult.cooldowned === 0, `刷新失败不应改成临时不可调用，实际 ${failedResult.cooldowned}`)
      assert(failedResult.exceptioned === (attempt === 3 ? 2 : 0), `第 ${attempt} 次异常标记数量不正确：${failedResult.exceptioned}`)
      assertAccountState(failedActive.id, attempt === 3 ? 'error' : 'active', attempt !== 3, attempt === 3 ? 'oauth_token_refresh_failed' : undefined)
      assertAccountState(failedDisabled.id, attempt === 3 ? 'error' : 'disabled', false, attempt === 3 ? 'oauth_token_refresh_failed' : undefined)
    }

    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => ({
      accessToken: `access-recovered-${refreshToken}`,
      refreshToken: `refresh-recovered-${refreshToken}`,
      expiresIn: 3600,
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      clientId: clientId ?? 'test-client'
    }))
    const refreshedAfterException = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 20,
      retryBackoffSeconds: 0
    })
    assert(refreshedAfterException.refreshed === 2, `异常账户仍应后台保活刷新，实际刷新 ${refreshedAfterException.refreshed}`)
    assertAccountState(failedActive.id, 'error', false, 'oauth_token_refresh_failed')
    const restored = repositories.clearAccountFailureState(failedActive.id, access)
    assert(restored?.status === 'active', `恢复异常后应回到正常状态，实际 ${restored?.status}`)
    assert(restored?.lastErrorCode === undefined, '恢复异常后应清理异常类型')
    assert(restored?.lastErrorMessage === undefined, '恢复异常后应清理异常信息')

    console.log('OpenAI OAuth Access Token 后台保活回归通过：未删除 OAuth 账户不受调度状态影响，连续失败 3 次会标记异常并保留恢复入口')
  } finally {
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest()
    try {
      databaseModule.getDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

function createOAuthAccount(
  name: string,
  status: 'active' | 'disabled' | 'error' | 'rate_limited' | 'temporary_unavailable',
  schedulable: boolean,
  refreshToken: string,
  overrides: { accessToken?: string; expiresAt?: string } = {}
): { id: string; name: string; status: string; schedulable: boolean; originalRefreshToken: string } {
  const account = repositories.createAccount({
    providerCode: 'openai',
    name,
    type: 'oauth',
    credentials: oauthCredentials(refreshToken, overrides.expiresAt ?? new Date(Date.now() + 60_000).toISOString(), overrides.accessToken),
    status,
    schedulable
  }, access)
  return {
    id: account.id,
    name,
    status: account.status,
    schedulable: account.schedulable,
    originalRefreshToken: refreshToken
  }
}

function oauthCredentials(refreshToken: string, expiresAt: string, accessToken = `access-${refreshToken}`): Record<string, unknown> {
  const credentials: Record<string, unknown> = {
    refresh_token: refreshToken,
    expires_at: expiresAt,
    client_id: 'test-client',
    base_url: 'https://api.openai.com/v1'
  }
  if (accessToken) {
    credentials.access_token = accessToken
  }
  return credentials
}

function assertAccountState(accountId: string, status: string, schedulable: boolean, lastErrorCode?: string): void {
  const latest = repositories.findAccountForTest(accountId, access)
  assert(latest, '账户不存在')
  assert(latest.status === status, `账户状态被错误改变：${latest.status}`)
  assert(latest.schedulable === schedulable, `账户调度标记被错误改变：${latest.schedulable}`)
  assert(latest.lastErrorCode === lastErrorCode, `账户异常类型不正确：${latest.lastErrorCode}`)
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

main().catch((error) => {
  console.error('\nOpenAI OAuth Access Token 后台保活回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
