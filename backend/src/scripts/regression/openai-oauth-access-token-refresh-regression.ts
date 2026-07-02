import { mkdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
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
let oauthGroupId = ''

async function main(): Promise<void> {
  try {
    const group = repositories.createGroup({
      name: 'OAuth 后台保活回归分组',
      providerCode: GPT_VENDOR_CODE
    }, access)
    oauthGroupId = group.id
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
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: 'API Key 不应参与 OAuth 刷新',
      type: 'api_key',
      credentials: { api_key: 'sk-not-oauth', base_url: 'https://api.openai.com/v1' },
      status: 'active',
      schedulable: true,
      groupId: oauthGroupId
    }, access)
    const freshOAuthAccount = repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '未到期 OAuth 账户',
      type: 'oauth',
      credentials: oauthCredentials('fresh-token', new Date(Date.now() + 3600_000).toISOString()),
      status: 'active',
      schedulable: true,
      groupId: oauthGroupId
    }, access)

    const result = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 20,
      retryBackoffSeconds: 60,
      persistMode: 'sync'
    })

    assert(result.scanned === 4, `应只扫描即将到期的 OAuth 刷新候选，实际 ${result.scanned}`)
    assert(result.due === 4, `应刷新 4 个快过期 OAuth 账户，实际 ${result.due}`)
    assert(result.refreshed === 4, `应成功刷新 4 个账户，实际 ${result.refreshed}`)
    assert(result.failed === 0, `不应有刷新失败，实际 ${result.failed}`)
    assert(result.exceptioned === 0, `成功刷新不应标记异常，实际 ${result.exceptioned}`)
    assert(result.cooldowned === 0, `后台保活成功不应改变账号冷却状态，实际 ${result.cooldowned}`)
    assertOAuthRefreshDuePlanUsesIndex()

    assert(!refreshedByToken.has('fresh-token'), '未到期 OAuth 账户不应刷新')
    assert(!refreshedByToken.has('sk-not-oauth'), 'API Key 账户不应参与 OAuth 刷新')
    assert(repositories.listAccounts(access).some((account) => account.id === apiKeyAccount.id), 'API Key 对照账户应仍存在')
    assert(repositories.listAccounts(access).some((account) => account.id === freshOAuthAccount.id), '未到期 OAuth 对照账户应仍存在')

    for (const account of dueAccounts) {
      const latest = repositories.findAccountForTest(account.id, access)
      assert(latest, `账户 ${account.name} 不存在`)
      assert(latest.status === account.status, `后台刷新不应改变 ${account.name} 状态：${latest.status}`)
      assert(latest.schedulable === account.schedulable, `后台刷新不应改变 ${account.name} 调度标记`)
      assert(latest.credentials.access_token === `access-refreshed-${account.originalRefreshToken}`, `${account.name} access_token 未刷新`)
      assert(latest.credentials.refresh_token === `refresh-refreshed-${account.originalRefreshToken}`, `${account.name} refresh_token 未刷新`)
    }

    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async () => {
      throw new Error('模拟刷新失败 Authorization: Bearer oauth-refresh-bearer-token sk-oauth-refresh-secret-token refresh_token=oauth-refresh-token-secret client_secret=oauth-refresh-client-secret proxy=https://oauth-refresh-proxy-user:oauth-refresh-proxy-password@example.com')
    })

    const failedActive = createOAuthAccount('连续刷新失败账户', 'active', true, 'active-fail-token', {
      accessToken: undefined,
      expiresAt: new Date(Date.now() - 60_000).toISOString()
    })
    const failedDisabled = createOAuthAccount('停用账户连续刷新失败', 'disabled', false, 'disabled-fail-token', {
      accessToken: undefined,
      expiresAt: new Date(Date.now() - 60_000).toISOString()
    })
    const stoppedRefresh = createOAuthAccount('OAuth 刷新异常账户', 'active', true, 'stopped-refresh-token', {
      accessToken: undefined,
      expiresAt: new Date(Date.now() - 60_000).toISOString()
    })
    repositories.markAccountException(
      stoppedRefresh.id,
      'oauth_token_refresh_failed',
      'OpenAI OAuth 访问令牌连续 432 次刷新失败：refresh_token_reused'
    )

    for (let attempt = 1; attempt <= 3; attempt += 1) {
      const failedResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
        leadSeconds: 300,
        batchSize: 20,
        retryBackoffSeconds: 0,
        persistMode: 'sync'
      })
      assert(failedResult.failed === 2, `第 ${attempt} 次应记录 2 个刷新失败，实际 ${failedResult.failed}`)
      assert(failedResult.cooldowned === 0, `刷新失败不应改成临时不可调用，实际 ${failedResult.cooldowned}`)
      assert(failedResult.exceptioned === (attempt === 3 ? 1 : 0), `第 ${attempt} 次异常标记数量不正确：${failedResult.exceptioned}`)
      assertAccountState(
        failedActive.id,
        attempt === 3 ? 'error' : 'active',
        attempt !== 3,
        attempt === 3 ? 'oauth_token_refresh_failed' : undefined,
        attempt === 3 ? '已停止自动刷新' : undefined
      )
      assertAccountState(failedDisabled.id, 'disabled', false)
    }
    assertAccountState(stoppedRefresh.id, 'error', false, 'oauth_token_refresh_failed', '连续 432 次')
    assertAccountLastErrorMessageIncludes(stoppedRefresh.id, '连续 432 次')
    assertAccountLastErrorMessageDoesNotInclude(stoppedRefresh.id, '已停止自动刷新')
    assertAccountLastErrorMessageDoesNotInclude(failedActive.id, 'oauth-refresh-bearer-token')
    assertAccountLastErrorMessageDoesNotInclude(failedActive.id, 'sk-oauth-refresh-secret-token')
    assertAccountLastErrorMessageDoesNotInclude(failedActive.id, 'oauth-refresh-token-secret')
    assertAccountLastErrorMessageDoesNotInclude(failedActive.id, 'oauth-refresh-client-secret')
    assertAccountLastErrorMessageDoesNotInclude(failedActive.id, 'oauth-refresh-proxy-user')
    assertAccountLastErrorMessageDoesNotInclude(failedActive.id, 'oauth-refresh-proxy-password')

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
      retryBackoffSeconds: 0,
      persistMode: 'sync'
    })
    assert(refreshedAfterException.scanned === 1, `OAuth 刷新失败异常账户不应再被扫描，实际扫描 ${refreshedAfterException.scanned}`)
    assert(refreshedAfterException.due === 1, `OAuth 刷新失败异常账户不应再进入待刷新列表，实际待刷新 ${refreshedAfterException.due}`)
    assert(refreshedAfterException.refreshed === 1, `仅停用对照账户应继续后台保活刷新，实际刷新 ${refreshedAfterException.refreshed}`)
    assertAccountState(failedActive.id, 'error', false, 'oauth_token_refresh_failed', '已停止自动刷新')
    assertAccountState(failedDisabled.id, 'disabled', false)

    console.log('OpenAI OAuth Access Token 后台保活回归通过：连续失败 3 次后标记 OAuth 刷新异常并停止后台自动刷新，手动停用不被后台覆盖')
  } finally {
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest()
    try {
      databaseModule.getBusinessDatabase().close()
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
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name,
    type: 'oauth',
    credentials: oauthCredentials(refreshToken, overrides.expiresAt ?? new Date(Date.now() + 60_000).toISOString(), overrides.accessToken),
    status,
    schedulable,
    groupId: oauthGroupId
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

function assertAccountState(accountId: string, status: string, schedulable: boolean, lastErrorCode?: string, lastErrorMessageIncludes?: string): void {
  const latest = repositories.findAccountForTest(accountId, access)
  assert(latest, '账户不存在')
  assert(latest.status === status, `账户状态被错误改变：${latest.status}`)
  assert(latest.schedulable === schedulable, `账户调度标记被错误改变：${latest.schedulable}`)
  assert(latest.lastErrorCode === lastErrorCode, `账户异常类型不正确：${latest.lastErrorCode}`)
  if (lastErrorMessageIncludes) {
    assert(
      typeof latest.lastErrorMessage === 'string' && latest.lastErrorMessage.includes(lastErrorMessageIncludes),
      `账户异常信息缺少说明：${latest.lastErrorMessage ?? ''}`
    )
  }
}

function assertAccountLastErrorMessageIncludes(accountId: string, expected: string): void {
  const latest = repositories.findAccountForTest(accountId, access)
  assert(latest, '账户不存在')
  assert(
    typeof latest.lastErrorMessage === 'string' && latest.lastErrorMessage.includes(expected),
    `账户异常信息缺少 ${expected}：${latest.lastErrorMessage ?? ''}`
  )
}

function assertAccountLastErrorMessageDoesNotInclude(accountId: string, unexpected: string): void {
  const latest = repositories.findAccountForTest(accountId, access)
  assert(latest, '账户不存在')
  assert(
    typeof latest.lastErrorMessage !== 'string' || !latest.lastErrorMessage.includes(unexpected),
    `账户异常信息不应被历史兼容归一化为 ${unexpected}：${latest.lastErrorMessage ?? ''}`
  )
}

function assertOAuthRefreshDuePlanUsesIndex(): void {
  const details = databaseModule.getBusinessDatabase()
    .prepare(`
      EXPLAIN QUERY PLAN
      SELECT id
      FROM accounts
      WHERE authorization_instance_authorization_id IS NULL
        AND provider_code = ?
        AND type = 'oauth'
        AND oauth_refresh_token_present = 1
        AND (status <> 'error' OR last_error_code IS NULL OR last_error_code <> ?)
        AND (oauth_access_token_expires_at IS NULL OR oauth_access_token_expires_at <= ?)
      ORDER BY oauth_access_token_expires_at IS NOT NULL ASC, oauth_access_token_expires_at ASC, updated_at ASC, id ASC
      LIMIT ?
    `)
    .all(GPT_VENDOR_CODE, 'oauth_token_refresh_failed', new Date(Date.now() + 300_000).toISOString(), 20)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
  assert(details.includes('idx_accounts_openai_oauth_refresh_due'), `OAuth 刷新候选查询应使用索引，实际计划：${details}`)
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
