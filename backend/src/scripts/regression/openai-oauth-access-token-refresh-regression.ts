import { mkdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import type { OpenAIOAuthRefreshFailureRedisClientForTest } from '../../modules/openai-oauth/openai-oauth-access-token-refresh.service.js'

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
  oauthRefreshService,
  dbServiceHandlers,
  sqliteReadWorkerPool
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/openai-oauth/openai-oauth-access-token-refresh.service.js'),
  import('../../modules/db-service/db-service-handlers.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const refreshedByToken = new Set<string>()
let observedClientAbortSignal: AbortSignal | undefined
let oauthGroupId = ''

async function main(): Promise<void> {
  try {
    const group = repositories.createGroup({
      name: 'OAuth 后台保活回归分组',
      providerCode: GPT_VENDOR_CODE
    }, access)
    oauthGroupId = group.id
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId, signal }) => {
      observedClientAbortSignal = signal
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
      supportedModels: ['gpt-5.5'],
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
      supportedModels: ['gpt-5.5'],
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

    const manualRefreshAccount = repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '客户端断开后仍应完成刷新的账户',
      type: 'oauth',
      credentials: oauthCredentials('manual-client-abort-token', new Date(Date.now() - 60_000).toISOString()),
      supportedModels: ['gpt-5.5'],
      status: 'active',
      schedulable: true,
      groupId: oauthGroupId
    }, access)
    const clientAbortController = new AbortController()
    clientAbortController.abort()
    observedClientAbortSignal = undefined
    await oauthRefreshService.refreshOpenAIOAuthAccountAccessToken(manualRefreshAccount, {
      force: true,
      persistMode: 'sync',
      signal: clientAbortController.signal
    })
    assert(observedClientAbortSignal === undefined, '客户端断开信号不得取消已开始的 OAuth token 刷新')

    const explicitCooldownAccounts = [
      {
        account: createOAuthAccount('用户显式临时不可调用刷新账户', 'temporary_unavailable', false, 'explicit-temporary-token', {
          expiresAt: new Date(Date.now() - 60_000).toISOString()
        }),
        status: 'temporary_unavailable',
        errorCode: 'user_explicit_temporary_unavailable'
      },
      {
        account: createOAuthAccount('用户显式限流刷新账户', 'rate_limited', false, 'explicit-rate-limited-token', {
          expiresAt: new Date(Date.now() - 60_000).toISOString()
        }),
        status: 'rate_limited',
        errorCode: 'user_explicit_rate_limited'
      }
    ] as const
    for (const { account: cooldownAccount, status, errorCode } of explicitCooldownAccounts) {
      databaseModule.getBusinessDatabase().prepare(`
        UPDATE accounts
        SET status = ?,
            schedulable = 0,
            last_error_code = ?,
            last_error_message = '用户显式设置的状态'
        WHERE id = ?
      `).run(status, errorCode, cooldownAccount.id)
      const current = repositories.findAccountForTest(cooldownAccount.id, access)
      assert(current, `${cooldownAccount.name} 不存在`)
      const refreshedCooldownAccount = await oauthRefreshService.refreshOpenAIOAuthAccountAccessToken(current, {
        force: true,
        persistMode: 'sync'
      })
      assert(refreshedCooldownAccount.status === status, `${cooldownAccount.name} 刷新成功不得清理用户显式状态`)
      assertAccountState(cooldownAccount.id, status, false, errorCode, '用户显式设置')
    }

    for (const scenario of [
      { status: 'temporary_unavailable', schedulable: false, errorCode: 'explicit_race_temporary', cooldown: true },
      { status: 'rate_limited', schedulable: false, errorCode: 'explicit_race_rate_limited', cooldown: true },
      { status: 'disabled', schedulable: false, errorCode: 'explicit_race_disabled', cooldown: false },
      { status: 'error', schedulable: false, errorCode: 'explicit_race_error', cooldown: false }
    ] as const) {
      const raceAccount = createOAuthAccount(
        `OAuth 窄写回并发状态保护 ${scenario.status}`,
        'active',
        true,
        `narrow-race-${scenario.status}`,
        { accessToken: undefined, expiresAt: new Date(Date.now() - 60_000).toISOString() }
      )
      const entered = deferred<void>()
      const release = deferred<void>()
      oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => {
        entered.resolve()
        await release.promise
        return {
          accessToken: `access-after-${scenario.status}`,
          refreshToken,
          expiresIn: 3600,
          expiresAt: new Date(Date.now() + 3600_000).toISOString(),
          clientId: clientId ?? 'test-client'
        }
      })
      const refreshPromise = oauthRefreshService.refreshOpenAIOAuthAccountAccessToken(
        repositories.findAccountForTest(raceAccount.id, access)!,
        { force: true, persistMode: 'sync' }
      )
      await entered.promise
      databaseModule.getBusinessDatabase().prepare(`
        UPDATE accounts
        SET status = ?,
            schedulable = ?,
            cooldown_until = ?,
            last_error_code = ?,
            last_error_message = '用户在 OAuth token exchange 期间写入的显式状态'
        WHERE id = ?
      `).run(
        scenario.status,
        scenario.schedulable ? 1 : 0,
        scenario.cooldown ? new Date(Date.now() + 3600_000).toISOString() : null,
        scenario.errorCode,
        raceAccount.id
      )
      release.resolve()
      await refreshPromise
      assertAccountState(raceAccount.id, scenario.status, scenario.schedulable, scenario.errorCode, 'token exchange 期间')
      assert(
        repositories.findAccountForTest(raceAccount.id, access)?.credentials.access_token === `access-after-${scenario.status}`,
        `${scenario.status} 并发状态应与 OAuth 凭据窄写回相互独立`
      )
    }

    const reauthorizationRaceAccount = createOAuthAccount(
      'OAuth 旧刷新成功不得覆盖新重新授权',
      'active',
      true,
      'reauthorization-race-old-refresh',
      { accessToken: undefined, expiresAt: new Date(Date.now() - 60_000).toISOString() }
    )
    const reauthorizationEntered = deferred<void>()
    const reauthorizationRelease = deferred<void>()
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => {
      reauthorizationEntered.resolve()
      await reauthorizationRelease.promise
      return {
        accessToken: 'stale-refresh-access-token',
        refreshToken,
        expiresIn: 3600,
        expiresAt: new Date(Date.now() + 3600_000).toISOString(),
        clientId: clientId ?? 'test-client'
      }
    })
    const staleRefreshPromise = oauthRefreshService.refreshOpenAIOAuthAccountAccessToken(
      repositories.findAccountForTest(reauthorizationRaceAccount.id, access)!,
      { force: true, persistMode: 'sync' }
    )
    await reauthorizationEntered.promise
    const latestBeforeReauthorization = repositories.findAccountForTest(reauthorizationRaceAccount.id, access)!
    repositories.updateAccount(reauthorizationRaceAccount.id, {
      credentials: {
        ...latestBeforeReauthorization.credentials,
        refresh_token: 'reauthorization-race-new-refresh',
        access_token: 'reauthorization-race-new-access',
        expires_at: new Date(Date.now() + 7200_000).toISOString()
      }
    }, access)
    reauthorizationRelease.resolve()
    const reauthorizationWinner = await staleRefreshPromise
    assert(reauthorizationWinner.credentials.refresh_token === 'reauthorization-race-new-refresh', '旧 refresh 成功不得覆盖新重新授权 Refresh Token')
    assert(reauthorizationWinner.credentials.access_token === 'reauthorization-race-new-access', '旧 refresh 成功不得覆盖新重新授权 Access Token')

    const raceWinnerAccount = createOAuthAccount('并发刷新胜者结果复用账户', 'active', true, 'race-winner-old-token', {
      accessToken: undefined,
      expiresAt: new Date(Date.now() - 60_000).toISOString()
    })
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async () => {
      const latest = repositories.findAccountForTest(raceWinnerAccount.id, access)
      assert(latest, '并发刷新胜者结果复用账户不存在')
      repositories.updateAccount(raceWinnerAccount.id, {
        credentials: {
          ...latest.credentials,
          access_token: 'access-written-by-race-winner',
          refresh_token: 'refresh-written-by-race-winner',
          expires_at: new Date(Date.now() + 3600_000).toISOString()
        }
      }, access)
      throw Object.assign(new Error('opaque concurrent transport failure'), { code: 'ECONNRESET' })
    })
    const recoveredRaceWinner = await oauthRefreshService.refreshOpenAIOAuthAccountAccessToken(
      repositories.findAccountForTest(raceWinnerAccount.id, access)!,
      { force: true, persistMode: 'sync' }
    )
    assert(recoveredRaceWinner.credentials.access_token === 'access-written-by-race-winner', '任意刷新错误后应按最新可用凭据恢复，不得依赖上游错误文案')

    const raceRetryAccount = createOAuthAccount('并发 Refresh Token 轮换重试账户', 'active', true, 'race-retry-old-token', {
      accessToken: undefined,
      expiresAt: new Date(Date.now() - 60_000).toISOString()
    })
    const raceRetryTokens: string[] = []
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => {
      raceRetryTokens.push(refreshToken)
      if (refreshToken === 'race-retry-old-token') {
        const latest = repositories.findAccountForTest(raceRetryAccount.id, access)
        assert(latest, '并发 Refresh Token 轮换重试账户不存在')
        repositories.updateAccount(raceRetryAccount.id, {
          credentials: {
            ...latest.credentials,
            refresh_token: 'race-retry-latest-token'
          }
        }, access)
        throw new Error('opaque provider failure without recognized code or message')
      }
      return {
        accessToken: 'access-after-race-retry',
        refreshToken: refreshToken,
        expiresIn: 3600,
        expiresAt: new Date(Date.now() + 3600_000).toISOString(),
        clientId: clientId ?? 'test-client'
      }
    })
    const recoveredRaceRetry = await oauthRefreshService.refreshOpenAIOAuthAccountAccessToken(
      repositories.findAccountForTest(raceRetryAccount.id, access)!,
      { force: true, persistMode: 'sync' }
    )
    assert(raceRetryTokens.join(',') === 'race-retry-old-token,race-retry-latest-token', `并发轮换后应只用最新 Refresh Token 重试一次，实际 ${raceRetryTokens.join(',')}`)
    assert(recoveredRaceRetry.credentials.access_token === 'access-after-race-retry', '最新 Refresh Token 重试成功后应写回 Access Token')

    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId, signal }) => {
      observedClientAbortSignal = signal
      refreshedByToken.add(refreshToken)
      return {
        accessToken: `access-refreshed-${refreshToken}`,
        refreshToken: `refresh-refreshed-${refreshToken}`,
        expiresIn: 3600,
        expiresAt: new Date(Date.now() + 3600_000).toISOString(),
        clientId: clientId ?? 'test-client'
      }
    })

    for (const account of dueAccounts) {
      const latest = repositories.findAccountForTest(account.id, access)
      assert(latest, `账户 ${account.name} 不存在`)
      assert(latest.status === account.status, `后台刷新不应改变 ${account.name} 状态：${latest.status}`)
      assert(latest.schedulable === account.schedulable, `后台刷新不应改变 ${account.name} 调度标记`)
      assert(latest.credentials.access_token === `access-refreshed-${account.originalRefreshToken}`, `${account.name} access_token 未刷新`)
      assert(latest.credentials.refresh_token === `refresh-refreshed-${account.originalRefreshToken}`, `${account.name} refresh_token 未刷新`)
    }

    const upstreamFailureCases = [
      ['HTTP 401', () => new Error('OpenAI OAuth 令牌请求失败：HTTP 401，invalid_grant')],
      ['HTTP 403', () => new Error('OpenAI OAuth 令牌请求失败：HTTP 403，account_disabled')],
      ['HTTP 429', () => new Error('OpenAI OAuth 令牌请求失败：HTTP 429，rate_limit_exceeded')],
      ['HTTP 500', () => new Error('OpenAI OAuth 令牌请求失败：HTTP 500，internal_error')],
      ['HTTP 503', () => new Error('OpenAI OAuth 令牌请求失败：HTTP 503，service_unavailable')],
      ['错误正文', () => new Error('OpenAI OAuth 令牌请求失败：HTTP 418，Authorization: Bearer oauth-refresh-bearer-token sk-oauth-refresh-secret-token refresh_token=oauth-refresh-token-secret client_secret=oauth-refresh-client-secret proxy=https://oauth-refresh-proxy-user:oauth-refresh-proxy-password@example.com')],
      ['坏 JSON', () => new SyntaxError('OpenAI OAuth 令牌响应不是有效 JSON')],
      ['缺少 access_token', () => new Error('OpenAI OAuth 令牌响应缺少访问令牌')],
      ['缺少 expires_in', () => new Error('OpenAI OAuth 令牌响应的 expires_in 必须是有限正数')],
      ['网络 transport', () => Object.assign(new Error('socket hang up'), { code: 'ECONNRESET' })],
      ['网络 timeout', () => Object.assign(new Error('OpenAI OAuth 令牌请求超时'), { code: 'ETIMEDOUT' })]
    ] as const
    const upstreamFailureAccounts = upstreamFailureCases.map(([label], index) => createOAuthAccount(
      `不可信上游失败-${label}`,
      'active',
      true,
      `upstream-failure-${index}`,
      { accessToken: undefined, expiresAt: new Date(Date.now() - 60_000).toISOString() }
    ))
    const upstreamFailureByRefreshToken = new Map(upstreamFailureAccounts.map((account, index) => [
      account.originalRefreshToken,
      upstreamFailureCases[index][1]
    ]))
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken }) => {
      const errorFactory = upstreamFailureByRefreshToken.get(refreshToken)
      if (!errorFactory) throw new Error('测试未配置的 Refresh Token')
      throw errorFactory()
    })

    for (let attempt = 1; attempt <= 3; attempt += 1) {
      const failedResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
        leadSeconds: 300,
        batchSize: 200,
        retryBackoffSeconds: 0,
        accountIds: upstreamFailureAccounts.map((account) => account.id),
        persistMode: 'sync'
      })
      assert(failedResult.failed === upstreamFailureAccounts.length, `第 ${attempt} 次应记录全部不可信上游刷新失败，实际 ${failedResult.failed}`)
      assert(failedResult.exceptioned === 0, `第 ${attempt} 次不可信上游失败不得标记账户异常，实际 ${failedResult.exceptioned}`)
      assert(failedResult.cooldowned === 0, `第 ${attempt} 次不可信上游失败不得改成临时不可调用，实际 ${failedResult.cooldowned}`)
      for (const account of upstreamFailureAccounts) {
        assertAccountState(account.id, 'active', true)
      }
    }

    const backoffAccount = createOAuthAccount('不可信上游失败有界退避', 'active', true, 'upstream-backoff-token', {
      accessToken: undefined,
      expiresAt: new Date(Date.now() - 60_000).toISOString()
    })
    let backoffRefreshCalls = 0
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async () => {
      backoffRefreshCalls += 1
      throw new Error('OpenAI OAuth 令牌请求失败：HTTP 503')
    })
    const firstBackoffResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 20,
      retryBackoffSeconds: 60,
      accountIds: [backoffAccount.id],
      persistMode: 'sync'
    })
    const secondBackoffResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 20,
      retryBackoffSeconds: 60,
      accountIds: [backoffAccount.id],
      persistMode: 'sync'
    })
    assert(firstBackoffResult.failed === 1, '首次不可信上游失败应记录一次诊断失败')
    assert(secondBackoffResult.skippedBackoff === 1, '退避窗口内不得再次热打 OAuth token endpoint')
    assert(backoffRefreshCalls === 1, `退避窗口内实际请求了 ${backoffRefreshCalls} 次上游`)
    assertAccountState(backoffAccount.id, 'active', true)

    const missingRefreshTokenAccount = repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '缺少 Refresh Token 的本地配置错误账户',
      type: 'oauth',
      credentials: {
        access_token: 'access-without-refresh-token',
        expires_at: new Date(Date.now() - 60_000).toISOString(),
        client_id: 'test-client',
        base_url: 'https://api.openai.com/v1'
      },
      supportedModels: ['gpt-5.5'],
      status: 'active',
      schedulable: true,
      groupId: oauthGroupId
    }, access)
    await assertRejects(
      () => oauthRefreshService.refreshOpenAIOAuthAccountAccessToken(missingRefreshTokenAccount, {
        force: true,
        persistMode: 'sync'
      }),
      (error) => oauthRefreshService.isOpenAIOAuthRefreshLocalConfigurationError(error),
      '本地缺少 Refresh Token 必须产生可独立识别的本地配置错误'
    )

    databaseModule.getBusinessDatabase().prepare(`
      UPDATE accounts
      SET status = 'active', schedulable = 1
      WHERE id = ?
    `).run(missingRefreshTokenAccount.id)
    let missingRefreshTokenCalls = 0
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async () => {
      missingRefreshTokenCalls += 1
      throw new Error('缺少 Refresh Token 不应到达上游 token endpoint')
    })
    for (let attempt = 1; attempt <= 3; attempt += 1) {
      const result = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
        leadSeconds: 300,
        batchSize: 20,
        retryBackoffSeconds: 0,
        accountIds: [missingRefreshTokenAccount.id],
        persistMode: 'sync'
      })
      assert(result.scanned === 1, `过期且缺少 Refresh Token 应进入后台候选，实际 ${result.scanned}`)
      assert(result.failed === 1, `缺少 Refresh Token 本地确认第 ${attempt} 次应记录失败`)
      assert(result.exceptioned === (attempt === 3 ? 1 : 0), `缺少 Refresh Token 第 ${attempt} 次异常标记不正确`)
    }
    assert(missingRefreshTokenCalls === 0, '缺少 Refresh Token 不应调用上游 token endpoint')
    assertAccountState(
      missingRefreshTokenAccount.id,
      'error',
      false,
      oauthRefreshService.OPENAI_OAUTH_TOKEN_REFRESH_LOCAL_CONFIGURATION_ERROR_CODE,
      '本地配置错误'
    )

    const disabledProxy = repositories.createProxy({
      name: 'OAuth 刷新已停用代理',
      type: 'http',
      host: '127.0.0.1',
      port: 19081,
      enabled: true
    }, access)
    const corruptCredentialProxy = repositories.createProxy({
      name: 'OAuth 刷新损坏凭据代理',
      type: 'http',
      host: '127.0.0.1',
      port: 19082,
      username: 'oauth-proxy-user',
      password: 'oauth-proxy-password',
      enabled: true
    }, access)
    const proxyConfigurationAccounts = [
      createOAuthAccount('已停用代理本地配置账户', 'active', true, 'disabled-proxy-refresh-token', {
        accessToken: undefined,
        expiresAt: new Date(Date.now() - 60_000).toISOString()
      }),
      createOAuthAccount('损坏代理凭据本地配置账户', 'active', true, 'corrupt-proxy-refresh-token', {
        accessToken: undefined,
        expiresAt: new Date(Date.now() - 60_000).toISOString()
      })
    ]
    repositories.updateAccount(proxyConfigurationAccounts[0].id, { proxyProfileId: disabledProxy.id }, access)
    repositories.updateAccount(proxyConfigurationAccounts[1].id, { proxyProfileId: corruptCredentialProxy.id }, access)
    databaseModule.getBusinessDatabase().prepare("UPDATE accounts SET status = 'active', schedulable = 1 WHERE id IN (?, ?)")
      .run(proxyConfigurationAccounts[0].id, proxyConfigurationAccounts[1].id)
    databaseModule.getBusinessDatabase().prepare('UPDATE proxy_profiles SET enabled = 0 WHERE id = ?').run(disabledProxy.id)
    databaseModule.getBusinessDatabase().prepare("UPDATE proxy_profiles SET password_encrypted = 'invalid-proxy-ciphertext' WHERE id = ?").run(corruptCredentialProxy.id)
    let proxyConfigurationRefreshCalls = 0
    const proxyConfigurationMarkAttempts: Array<{ accountId: string; expectedConfigRevision?: number; expectedStatus?: string }> = []
    const proxyConfigurationReadRevisions: Array<{ accountId: string; configRevision?: number }> = []
    const proxyConfigurationRequesterErrors: string[] = []
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async () => {
      proxyConfigurationRefreshCalls += 1
      throw new Error('代理本地配置错误不应调用上游 token endpoint')
    })
    runtimeConfig.processRole = 'db-service'
    oauthRefreshService.setOpenAIOAuthDbServiceRequesterForTest(async (operation) => {
      if (operation.type === 'mark_openai_oauth_local_configuration_exception') {
        proxyConfigurationMarkAttempts.push({
          accountId: operation.accountId,
          expectedConfigRevision: operation.expectedConfigRevision,
          expectedStatus: operation.expectedStatus
        })
      }
      try {
        const result = await dbServiceHandlers.handleDbServiceOperation(operation)
        if (operation.type === 'find_openai_oauth_account_for_refresh') {
          const refreshAccount = result as { configRevision?: number } | undefined
          proxyConfigurationReadRevisions.push({ accountId: operation.accountId, configRevision: refreshAccount?.configRevision })
        }
        return result
      } catch (error) {
        proxyConfigurationRequesterErrors.push(error instanceof Error ? error.message : String(error))
        throw error
      }
    })
    for (let attempt = 1; attempt <= 3; attempt += 1) {
      const result = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
        leadSeconds: 300,
        batchSize: 20,
        retryBackoffSeconds: 0,
        accountIds: proxyConfigurationAccounts.map((account) => account.id),
        persistMode: 'db-service'
      })
      assert(result.failed === 2, `DB service 代理配置第 ${attempt} 次应逐账户失败，实际 ${result.failed}`)
      assert(result.exceptioned === (attempt === 3 ? 2 : 0), `DB service 代理配置第 ${attempt} 次异常标记不正确：${JSON.stringify(result)} marks=${JSON.stringify(proxyConfigurationMarkAttempts)} reads=${JSON.stringify(proxyConfigurationReadRevisions)} errors=${JSON.stringify(proxyConfigurationRequesterErrors)}`)
    }
    oauthRefreshService.setOpenAIOAuthDbServiceRequesterForTest()
    runtimeConfig.processRole = 'worker'
    await sqliteReadWorkerPool.closeSqliteReadWorkerPool()
    assert(proxyConfigurationRefreshCalls === 0, 'DB service 代理不可用/凭据损坏必须在上游请求前截断')
    for (const account of proxyConfigurationAccounts) {
      assertAccountState(
        account.id,
        'error',
        false,
        oauthRefreshService.OPENAI_OAUTH_TOKEN_REFRESH_LOCAL_CONFIGURATION_ERROR_CODE,
        '本地配置错误'
      )
    }

    const corruptCiphertextAccount = createOAuthAccount('单行损坏 OAuth 密文账户', 'active', true, 'corrupt-ciphertext-token', {
      accessToken: undefined,
      expiresAt: new Date(Date.now() - 60_000).toISOString()
    })
    const decryptableSiblingAccounts = [
      createOAuthAccount('损坏密文同批健康账户 A', 'active', true, 'decryptable-sibling-a', {
        accessToken: undefined,
        expiresAt: new Date(Date.now() - 60_000).toISOString()
      }),
      createOAuthAccount('损坏密文同批健康账户 B', 'active', true, 'decryptable-sibling-b', {
        accessToken: undefined,
        expiresAt: new Date(Date.now() - 60_000).toISOString()
      })
    ]
    databaseModule.getBusinessDatabase().prepare("UPDATE accounts SET credentials_encrypted = 'invalid-oauth-ciphertext' WHERE id = ?").run(corruptCiphertextAccount.id)
    const ciphertextSiblingRefreshTokens: string[] = []
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => {
      ciphertextSiblingRefreshTokens.push(refreshToken)
      return {
        accessToken: `access-after-ciphertext-isolation-${refreshToken}`,
        refreshToken,
        expiresIn: 3600,
        expiresAt: new Date(Date.now() + 3600_000).toISOString(),
        clientId: clientId ?? 'test-client'
      }
    })
    const ciphertextFirstResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 20,
      retryBackoffSeconds: 0,
      accountIds: [corruptCiphertextAccount.id, ...decryptableSiblingAccounts.map((account) => account.id)],
      persistMode: 'sync'
    })
    assert(ciphertextFirstResult.refreshed === 2, `单行坏密文不得中断同批健康账户，实际刷新 ${ciphertextFirstResult.refreshed}`)
    assert(ciphertextFirstResult.failed === 1, `单行坏密文只应影响自身，实际失败 ${ciphertextFirstResult.failed}`)
    assert(
      ciphertextSiblingRefreshTokens.slice().sort().join(',') === ['decryptable-sibling-a', 'decryptable-sibling-b'].sort().join(','),
      `单行坏密文同批健康账户未全部刷新：${ciphertextSiblingRefreshTokens.join(',')}`
    )
    for (let attempt = 2; attempt <= 3; attempt += 1) {
      const result = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
        leadSeconds: 300,
        batchSize: 20,
        retryBackoffSeconds: 0,
        accountIds: [corruptCiphertextAccount.id],
        persistMode: 'sync'
      })
      assert(result.failed === 1, `单行坏密文第 ${attempt} 次应继续独立确认`)
      assert(result.exceptioned === (attempt === 3 ? 1 : 0), `单行坏密文第 ${attempt} 次异常标记不正确`)
    }
    const corruptCiphertextState = databaseModule.getBusinessDatabase().prepare(`
      SELECT status, schedulable, last_error_code
      FROM accounts
      WHERE id = ?
    `).get(corruptCiphertextAccount.id) as { status: string; schedulable: number; last_error_code: string | null }
    assert(corruptCiphertextState.status === 'error', '已证明 keyring 健康后，单行坏密文应有界收敛为本地配置异常')
    assert(corruptCiphertextState.schedulable === 0, '单行坏密文收敛后不得继续调度')
    assert(
      corruptCiphertextState.last_error_code === oauthRefreshService.OPENAI_OAUTH_TOKEN_REFRESH_LOCAL_CONFIGURATION_ERROR_CODE,
      '单行坏密文应使用 OAuth 本地配置错误码'
    )

    const keyringOutageAccount = createOAuthAccount('全批 keyring 不可验证不得误杀账户', 'active', true, 'keyring-outage-token', {
      accessToken: undefined,
      expiresAt: new Date(Date.now() - 60_000).toISOString()
    })
    databaseModule.getBusinessDatabase().prepare("UPDATE accounts SET credentials_encrypted = 'invalid-keyring-outage-ciphertext' WHERE id = ?").run(keyringOutageAccount.id)
    const otherOAuthExpiryRows = databaseModule.getBusinessDatabase().prepare(`
      SELECT id, oauth_access_token_expires_at
      FROM accounts
      WHERE type = 'oauth' AND id <> ?
    `).all(keyringOutageAccount.id) as Array<{ id: string; oauth_access_token_expires_at: string | null }>
    const futureExpiry = new Date(Date.now() + 24 * 60 * 60_000).toISOString()
    const setExpiry = databaseModule.getBusinessDatabase().prepare('UPDATE accounts SET oauth_access_token_expires_at = ? WHERE id = ?')
    for (const row of otherOAuthExpiryRows) setExpiry.run(futureExpiry, row.id)
    for (let attempt = 1; attempt <= 3; attempt += 1) {
      const result = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
        leadSeconds: 300,
        batchSize: 20,
        retryBackoffSeconds: 0,
        accountIds: [keyringOutageAccount.id],
        persistMode: 'sync'
      })
      assert(result.failed === 1, `全批 keyring 不可验证第 ${attempt} 次应只记运行时诊断`)
      assert(result.exceptioned === 0, `全批 keyring 不可验证第 ${attempt} 次不得标记账户异常`)
    }
    const keyringOutageState = databaseModule.getBusinessDatabase().prepare(`
      SELECT status, schedulable, last_error_code
      FROM accounts
      WHERE id = ?
    `).get(keyringOutageAccount.id) as { status: string; schedulable: number; last_error_code: string | null }
    assert(keyringOutageState.status === 'active' && keyringOutageState.schedulable === 1, '全批无法解密时必须保持账户状态中性')
    assert(keyringOutageState.last_error_code === null, '全批无法解密时不得写账户错误码')
    for (const row of otherOAuthExpiryRows) setExpiry.run(row.oauth_access_token_expires_at, row.id)

    const localConfigurationAccount = createOAuthAccount(
      '连续本地配置错误账户',
      'active',
      true,
      'local-configuration-failure-token',
      { accessToken: undefined, expiresAt: new Date(Date.now() - 60_000).toISOString() }
    )
    const localFailureSequence = [
      'local_configuration',
      'local_configuration',
      'untrusted_upstream',
      'local_configuration',
      'local_configuration',
      'local_configuration'
    ] as const
    let localFailureAttempt = 0
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async () => {
      const failureKind = localFailureSequence[localFailureAttempt]
      localFailureAttempt += 1
      if (failureKind === 'local_configuration') {
        throw new oauthRefreshService.OpenAIOAuthRefreshLocalConfigurationError('本地 OAuth 代理配置无法解析')
      }
      throw new Error('OpenAI OAuth 令牌请求失败：HTTP 401，invalid_grant')
    })
    for (let attempt = 1; attempt <= localFailureSequence.length; attempt += 1) {
      const failedResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
        leadSeconds: 300,
        batchSize: 20,
        retryBackoffSeconds: 0,
        accountIds: [localConfigurationAccount.id],
        persistMode: 'sync'
      })
      assert(failedResult.failed === 1, `本地配置错误序列第 ${attempt} 次应执行一次刷新`)
      assert(
        failedResult.exceptioned === (attempt === localFailureSequence.length ? 1 : 0),
        `本地配置错误序列第 ${attempt} 次异常标记数量不正确：${failedResult.exceptioned}`
      )
      assertAccountState(
        localConfigurationAccount.id,
        attempt === localFailureSequence.length ? 'error' : 'active',
        attempt !== localFailureSequence.length,
        attempt === localFailureSequence.length
          ? oauthRefreshService.OPENAI_OAUTH_TOKEN_REFRESH_LOCAL_CONFIGURATION_ERROR_CODE
          : undefined,
        attempt === localFailureSequence.length ? '本地配置错误' : undefined
      )
    }
    const stoppedLocalConfigurationResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 20,
      retryBackoffSeconds: 0,
      accountIds: [localConfigurationAccount.id],
      persistMode: 'sync'
    })
    assert(stoppedLocalConfigurationResult.scanned === 0, '已确认的本地配置异常应停止后台热循环，等待人工修正')

    const delayedLocalFailureAccount = createOAuthAccount(
      '迟到本地配置失败不得覆盖显式冷却',
      'active',
      true,
      'delayed-local-failure-token',
      { accessToken: undefined, expiresAt: new Date(Date.now() - 60_000).toISOString() }
    )
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async () => {
      throw new oauthRefreshService.OpenAIOAuthRefreshLocalConfigurationError('本地 OAuth 配置无法装配')
    })
    for (let attempt = 1; attempt <= 2; attempt += 1) {
      const result = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
        leadSeconds: 300,
        batchSize: 20,
        retryBackoffSeconds: 0,
        accountIds: [delayedLocalFailureAccount.id],
        persistMode: 'sync'
      })
      assert(result.exceptioned === 0, `迟到失败场景第 ${attempt} 次不得提前标记异常`)
    }
    let delayedMarkInjected = false
    oauthRefreshService.setOpenAIOAuthDbServiceRequesterForTest(async (operation) => {
      if (operation.type !== 'mark_openai_oauth_local_configuration_exception') {
        throw new Error(`迟到本地失败测试未处理 DB service operation: ${operation.type}`)
      }
      databaseModule.getBusinessDatabase().prepare(`
        UPDATE accounts
        SET status = 'temporary_unavailable',
            schedulable = 0,
            cooldown_until = ?,
            last_error_code = 'explicit_account_error_policy_cooldown',
            last_error_message = '用户在第三次本地失败落库前写入显式冷却',
            cooldown_retest_observation_started_at = ?
        WHERE id = ?
      `).run(
        new Date(Date.now() + 3600_000).toISOString(),
        new Date().toISOString(),
        delayedLocalFailureAccount.id
      )
      delayedMarkInjected = true
      const updated = repositories.markOpenAIOAuthLocalConfigurationExceptionIfCurrent(operation)
      return { updated } as never
    })
    const delayedThirdFailure = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 20,
      retryBackoffSeconds: 0,
      accountIds: [delayedLocalFailureAccount.id],
      persistMode: 'sync'
    })
    oauthRefreshService.setOpenAIOAuthDbServiceRequesterForTest()
    assert(delayedMarkInjected, '第三次本地配置失败必须进入受 fencing 保护的异常标记入口')
    assert(delayedThirdFailure.exceptioned === 0, '迟到第三次本地配置失败不得覆盖新显式冷却')
    assertAccountState(
      delayedLocalFailureAccount.id,
      'temporary_unavailable',
      false,
      'explicit_account_error_policy_cooldown',
      '第三次本地失败落库前'
    )

    const regeneratedLocalFailureAccount = createOAuthAccount(
      'OAuth 本地失败计数按配置代次隔离',
      'active',
      true,
      'regenerated-local-failure-token',
      { accessToken: undefined, expiresAt: new Date(Date.now() - 60_000).toISOString() }
    )
    for (let attempt = 1; attempt <= 2; attempt += 1) {
      await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
        leadSeconds: 300,
        batchSize: 20,
        retryBackoffSeconds: 0,
        accountIds: [regeneratedLocalFailureAccount.id],
        persistMode: 'sync'
      })
    }
    repositories.updateAccount(regeneratedLocalFailureAccount.id, { priority: 7 }, access)
    const firstFailureInNewGeneration = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 20,
      retryBackoffSeconds: 0,
      accountIds: [regeneratedLocalFailureAccount.id],
      persistMode: 'sync'
    })
    assert(firstFailureInNewGeneration.exceptioned === 0, '配置代次变化后旧的两次本地失败不得与新失败合并成异常')
    assertAccountState(regeneratedLocalFailureAccount.id, 'active', true)

    const fakeFailureRedis = new FakeOpenAIOAuthRefreshFailureRedis()
    oauthRefreshService.setOpenAIOAuthRefreshFailureRedisClientForTest(fakeFailureRedis)
    const lateSuccessAccount = createOAuthAccount(
      'OAuth Redis 迟到成功不得删新代次',
      'active',
      true,
      'redis-late-success-token',
      { accessToken: undefined, expiresAt: new Date(Date.now() - 60_000).toISOString() }
    )
    const lateSuccessRevision = repositories.findAccountForTest(lateSuccessAccount.id, access)?.configRevision ?? 1
    fakeFailureRedis.seedOnFirstGet = refreshFailureRedisPayload({
      count: 1,
      localConfigurationCount: 1,
      backoffUntil: 0,
      configRevision: lateSuccessRevision,
      mutationId: 'observed-old-revision-state'
    })
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => {
      fakeFailureRedis.setLastObservedKey(refreshFailureRedisPayload({
        count: 2,
        localConfigurationCount: 2,
        backoffUntil: Date.now() + 60_000,
        configRevision: lateSuccessRevision + 1,
        mutationId: 'new-revision-written-before-success-clear'
      }))
      return {
        accessToken: 'redis-late-success-access',
        refreshToken,
        expiresIn: 3600,
        expiresAt: new Date(Date.now() + 3600_000).toISOString(),
        clientId: clientId ?? 'test-client'
      }
    })
    const lateSuccessResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 20,
      retryBackoffSeconds: 0,
      accountIds: [lateSuccessAccount.id],
      persistMode: 'sync'
    })
    assert(lateSuccessResult.refreshed === 1, 'Redis 迟到成功场景凭据刷新应成功')
    assert(
      fakeFailureRedis.lastObservedState()?.configRevision === lateSuccessRevision + 1,
      'R1 成功的 compare-delete 不得删除 R2 新失败状态'
    )

    const staleFailureAccount = createOAuthAccount(
      'OAuth Redis 旧代次迟到失败不得覆盖新代次',
      'active',
      true,
      'redis-stale-failure-token',
      { accessToken: undefined, expiresAt: new Date(Date.now() - 60_000).toISOString() }
    )
    const staleFailureRevision = repositories.findAccountForTest(staleFailureAccount.id, access)?.configRevision ?? 1
    fakeFailureRedis.seedOnFirstGet = undefined
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async () => {
      fakeFailureRedis.setLastObservedKey(refreshFailureRedisPayload({
        count: 3,
        localConfigurationCount: 3,
        backoffUntil: Date.now() + 60_000,
        configRevision: staleFailureRevision + 1,
        mutationId: 'new-revision-before-stale-failure'
      }))
      throw new oauthRefreshService.OpenAIOAuthRefreshLocalConfigurationError('R1 迟到本地配置失败')
    })
    const staleFailureResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 20,
      retryBackoffSeconds: 0,
      accountIds: [staleFailureAccount.id],
      persistMode: 'sync'
    })
    assert(staleFailureResult.failed === 1, 'Redis 旧代次迟到失败应保留请求级诊断')
    assert(staleFailureResult.exceptioned === 0, 'Redis 旧代次迟到失败不得借用 R2 计数标记账户异常')
    assert(
      fakeFailureRedis.lastObservedState()?.configRevision === staleFailureRevision + 1,
      'R1 迟到失败不得覆盖 R2 状态'
    )
    assertAccountState(staleFailureAccount.id, 'active', true)
    oauthRefreshService.setOpenAIOAuthRefreshFailureRedisClientForTest()

    const legacyFailureAccount = createOAuthAccount(
      '历史 OAuth 刷新误判账户',
      'active',
      true,
      'legacy-refresh-failure-token',
      { accessToken: undefined, expiresAt: new Date(Date.now() - 60_000).toISOString() }
    )
    repositories.markAccountException(
      legacyFailureAccount.id,
      oauthRefreshService.OPENAI_OAUTH_TOKEN_REFRESH_FAILED_ERROR_CODE,
      '历史版本根据上游错误写入的 OAuth 刷新异常'
    )
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => ({
      accessToken: `access-recovered-${refreshToken}`,
      refreshToken: `refresh-recovered-${refreshToken}`,
      expiresIn: 3600,
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      clientId: clientId ?? 'test-client'
    }))
    const legacyRecoveryResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 20,
      retryBackoffSeconds: 0,
      accountIds: [legacyFailureAccount.id],
      persistMode: 'sync'
    })
    assert(legacyRecoveryResult.refreshed === 1, '历史 OAuth 刷新误判账户应重新参与刷新')
    assertAccountState(legacyFailureAccount.id, 'pending_test', false)

    const syncProvenanceGuardAccount = createOAuthAccount(
      '同步 OAuth 恢复 provenance 守卫账户',
      'active',
      true,
      'sync-provenance-guard-token',
      { accessToken: undefined, expiresAt: new Date(Date.now() - 60_000).toISOString() }
    )
    repositories.markAccountException(
      syncProvenanceGuardAccount.id,
      oauthRefreshService.OPENAI_OAUTH_TOKEN_REFRESH_FAILED_ERROR_CODE,
      '历史 OAuth 刷新异常'
    )
    databaseModule.getBusinessDatabase().prepare(`
      UPDATE accounts
      SET status = 'error',
          schedulable = 0,
          last_error_code = 'user_explicit_non_oauth_error',
          last_error_message = '用户在刷新期间写入的显式异常'
      WHERE id = ?
    `).run(syncProvenanceGuardAccount.id)
    const syncProvenanceGuardResult = repositories.clearAccountFailureStateResult(
      syncProvenanceGuardAccount.id,
      access,
      { expectedLastErrorCodes: oauthRefreshService.openAIOAuthRefreshManagedErrorCodes }
    )
    assert(syncProvenanceGuardResult.changed === false, 'SQLite OAuth provenance 不匹配时清理必须 no-op')
    assertAccountState(syncProvenanceGuardAccount.id, 'error', false, 'user_explicit_non_oauth_error', '用户在刷新期间')

    const dbServiceRaceAccount = createOAuthAccount(
      'DB service OAuth 恢复竞态账户',
      'active',
      true,
      'db-service-race-token',
      { accessToken: undefined, expiresAt: new Date(Date.now() - 60_000).toISOString() }
    )
    repositories.markAccountException(
      dbServiceRaceAccount.id,
      oauthRefreshService.OPENAI_OAUTH_TOKEN_REFRESH_FAILED_ERROR_CODE,
      '历史 OAuth 刷新异常'
    )
    let dbServiceRaceInjected = false
    let dbServiceRaceError = ''
    oauthRefreshService.setOpenAIOAuthDbServiceRequesterForTest(async (operation) => {
      if (operation.type === 'find_openai_oauth_account_for_refresh') {
        return repositories.findAccountForTest(operation.accountId, access) as never
      }
      if (operation.type === 'update_openai_oauth_credentials') {
        return {
          updated: Boolean(repositories.updateOpenAIOAuthCredentialsIfCurrent(
            operation.accountId,
            operation.credentials,
            operation.expectedConfigRevision,
            access
          ))
        } as never
      }
      if (operation.type === 'clear_account_failure_state' && operation.accountId === dbServiceRaceAccount.id) {
        databaseModule.getBusinessDatabase().prepare(`
          UPDATE accounts
          SET status = 'error',
              schedulable = 0,
              last_error_code = 'user_explicit_race_error',
              last_error_message = '用户在 OAuth 刷新成功后写入的显式异常'
          WHERE id = ?
        `).run(dbServiceRaceAccount.id)
        dbServiceRaceInjected = true
      }
      try {
        if (operation.type === 'clear_account_failure_state') {
          const cleared = repositories.clearAccountFailureStateResult(operation.accountId, access, {
            allowPendingTestRestore: operation.allowPendingTestRestore,
            allowErrorRestore: operation.allowErrorRestore,
            expectedLastErrorCodes: operation.expectedLastErrorCodes
          })
          return { changed: cleared.changed, accountStatus: cleared.account?.status } as never
        }
        throw new Error(`测试未处理 DB service operation: ${operation.type}`)
      } catch (error) {
        dbServiceRaceError = error instanceof Error ? error.message : String(error)
        throw error
      }
    })
    const dbServiceRaceResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 20,
      retryBackoffSeconds: 0,
      accountIds: [dbServiceRaceAccount.id],
      persistMode: 'db-service'
    })
    oauthRefreshService.setOpenAIOAuthDbServiceRequesterForTest()
    assert(dbServiceRaceResult.refreshed === 1, `DB service 竞态账户凭据刷新本身应成功：${JSON.stringify(dbServiceRaceResult)}，错误：${dbServiceRaceError}`)
    assert(dbServiceRaceInjected, 'DB service 回归必须在 OAuth clear 前注入用户显式错误')
    assertAccountState(dbServiceRaceAccount.id, 'error', false, 'user_explicit_race_error', '用户在 OAuth 刷新成功后')

    const boundedAccounts = Array.from({ length: 8 }, (_, index) => createOAuthAccount(
      `OAuth 有界并发账户 ${index}`,
      'active',
      true,
      `bounded-refresh-${index}`,
      { expiresAt: `2000-01-01T00:00:${String(index).padStart(2, '0')}.000Z` }
    ))
    let activeRefreshes = 0
    let maxActiveRefreshes = 0
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => {
      activeRefreshes += 1
      maxActiveRefreshes = Math.max(maxActiveRefreshes, activeRefreshes)
      await delay(150)
      activeRefreshes -= 1
      return successfulTokenInfo(refreshToken, clientId)
    })
    const boundedResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 8,
      retryBackoffSeconds: 0,
      accountIds: boundedAccounts.map((account) => account.id),
      persistMode: 'sync',
      startAdmissionBudgetMs: 100
    })
    assert(maxActiveRefreshes === 4, `OAuth 后台刷新并发应限制为 4，实际 ${maxActiveRefreshes}，结果 ${JSON.stringify(boundedResult)}`)
    assert(boundedResult.started === 4, `100ms 启动预算内应只启动首批 4 个账户，实际 ${boundedResult.started}`)
    assert(boundedResult.refreshed === 4, `已启动的 4 个 token rotation 应等待写回完成，实际 ${boundedResult.refreshed}`)
    assert(boundedResult.deferredBudget === 4, `预算到期后应延后剩余 4 个账户，实际 ${boundedResult.deferredBudget}`)
    assert(boundedResult.failed === 0, '启动预算到期不应记为刷新失败')

    const abortAccounts = Array.from({ length: 8 }, (_, index) => createOAuthAccount(
      `OAuth 取消 admission 账户 ${index}`,
      'active',
      true,
      `abort-admission-${index}`,
      { expiresAt: `2000-01-01T00:00:${String(index).padStart(2, '0')}.000Z` }
    ))
    const firstAbortWaveEntered = deferred<void>()
    const releaseAbortWave = deferred<void>()
    const abortController = new AbortController()
    let abortWaveStarted = 0
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => {
      abortWaveStarted += 1
      if (abortWaveStarted === 4) firstAbortWaveEntered.resolve()
      await releaseAbortWave.promise
      return successfulTokenInfo(refreshToken, clientId)
    })
    const abortedBatchPromise = oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 8,
      retryBackoffSeconds: 0,
      accountIds: abortAccounts.map((account) => account.id),
      persistMode: 'sync',
      startAdmissionBudgetMs: 5_000,
      signal: abortController.signal
    })
    await firstAbortWaveEntered.promise
    abortController.abort(new Error('scheduler stopping'))
    releaseAbortWave.resolve()
    const abortedBatchResult = await abortedBatchPromise
    assert(abortedBatchResult.started === 4, `父任务取消后不得启动第二批 OAuth rotation，实际 ${abortedBatchResult.started}`)
    assert(abortedBatchResult.refreshed === 4, `取消前已开始的 OAuth rotation 必须完成并写回，实际 ${abortedBatchResult.refreshed}`)
    assert(abortedBatchResult.deferredBudget === 4, `父任务取消后剩余 OAuth 候选必须延期，实际 ${abortedBatchResult.deferredBudget}`)
    assert(abortedBatchResult.failed === 0, '父任务取消不得把未启动 OAuth 候选记为刷新失败')

    const lockedAccount = createOAuthAccount(
      'OAuth 后台非阻塞锁账户',
      'active',
      true,
      'background-lock-skip',
      { expiresAt: '2000-01-01T00:01:00.000Z' }
    )
    const lockedCurrent = repositories.findAccountForTest(lockedAccount.id, access)
    assert(lockedCurrent, 'OAuth 后台非阻塞锁账户不存在')
    const lockEntered = deferred<void>()
    const lockRelease = deferred<void>()
    let lockedRefreshCalls = 0
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => {
      lockedRefreshCalls += 1
      if (lockedRefreshCalls === 1) {
        lockEntered.resolve()
        await lockRelease.promise
      }
      return successfulTokenInfo(refreshToken, clientId)
    })
    const firstManualRefresh = oauthRefreshService.refreshOpenAIOAuthAccountAccessToken(lockedCurrent, {
      force: true,
      persistMode: 'sync'
    })
    await lockEntered.promise
    let secondManualSettled = false
    const secondManualRefresh = oauthRefreshService.refreshOpenAIOAuthAccountAccessToken(lockedCurrent, {
      force: true,
      persistMode: 'sync'
    }).finally(() => {
      secondManualSettled = true
    })
    await delay(5)
    assert(!secondManualSettled, '手动刷新必须等待同账户在途 token rotation，不得改成非阻塞跳过')
    const lockedBackgroundResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 20,
      retryBackoffSeconds: 0,
      accountIds: [lockedAccount.id],
      persistMode: 'sync'
    })
    assert(lockedBackgroundResult.started === 0, '后台锁未取得时不得计为已启动')
    assert(lockedBackgroundResult.skippedLocked === 1, '后台刷新遇到在途同账户任务应立即跳过锁')
    assert(lockedBackgroundResult.failed === 0, '后台锁占用不得污染刷新失败统计')
    lockRelease.resolve()
    await Promise.all([firstManualRefresh, secondManualRefresh])

    const defaultBatchAccounts = Array.from({ length: 21 }, (_, index) => createOAuthAccount(
      `OAuth 默认批次账户 ${index}`,
      'active',
      true,
      `default-batch-${index}`,
      { expiresAt: `2000-01-01T00:02:${String(index).padStart(2, '0')}.000Z` }
    ))
    const boundedFailureRedis = new BoundedReadOpenAIOAuthRefreshFailureRedis()
    oauthRefreshService.setOpenAIOAuthRefreshFailureRedisClientForTest(boundedFailureRedis)
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => successfulTokenInfo(refreshToken, clientId))
    const defaultBatchResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      retryBackoffSeconds: 0,
      accountIds: defaultBatchAccounts.map((account) => account.id),
      persistMode: 'sync'
    })
    oauthRefreshService.setOpenAIOAuthRefreshFailureRedisClientForTest()
    assert(defaultBatchResult.started === 20, `OAuth 默认 batch 应为 20，实际 ${defaultBatchResult.started}`)
    assert(defaultBatchResult.refreshed === 20, `OAuth 默认 batch 应完成 20 个账户，实际 ${defaultBatchResult.refreshed}`)
    assert(boundedFailureRedis.getCalls === 20, `无退避命中时只应读取入选 20 个 Redis 状态，实际 ${boundedFailureRedis.getCalls}`)
    assert(boundedFailureRedis.maxActiveGets <= 4, `Redis 退避读取并发应不超过 4，实际 ${boundedFailureRedis.maxActiveGets}`)

    console.log('OpenAI OAuth Access Token 后台保活回归通过：默认 batch 20、并发 4、启动预算延后、后台非阻塞锁、Redis 退避读取有界')
  } finally {
    oauthRefreshService.setOpenAIOAuthTokenRefresherForTest()
    oauthRefreshService.setOpenAIOAuthDbServiceRequesterForTest()
    oauthRefreshService.setOpenAIOAuthRefreshFailureRedisClientForTest()
    runtimeConfig.processRole = 'worker'
    await sqliteReadWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
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
    supportedModels: ['gpt-5.5'],
    status,
    schedulable,
    groupId: oauthGroupId
  }, access)
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET status = ?, schedulable = ?
    WHERE id = ?
  `).run(status, schedulable ? 1 : 0, account.id)
  return {
    id: account.id,
    name,
    status,
    schedulable,
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

async function assertRejects(
  task: () => Promise<unknown>,
  predicate: (error: unknown) => boolean,
  message: string
): Promise<void> {
  try {
    await task()
  } catch (error) {
    assert(predicate(error), `${message}，实际错误：${error instanceof Error ? error.name : typeof error}`)
    return
  }
  throw new Error(`${message}，实际未抛错`)
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
        AND oauth_refresh_token_present IN (0, 1)
        AND (status <> 'error' OR last_error_code IS NULL OR last_error_code <> ?)
        AND (oauth_access_token_expires_at IS NULL OR oauth_access_token_expires_at <= ?)
      ORDER BY oauth_access_token_expires_at IS NOT NULL ASC, oauth_access_token_expires_at ASC, updated_at ASC, id ASC
      LIMIT ?
    `)
    .all(GPT_VENDOR_CODE, oauthRefreshService.OPENAI_OAUTH_TOKEN_REFRESH_LOCAL_CONFIGURATION_ERROR_CODE, new Date(Date.now() + 300_000).toISOString(), 20)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
  assert(details.includes('idx_accounts_openai_oauth_refresh_due'), `OAuth 刷新候选查询应使用索引，实际计划：${details}`)
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

function deferred<T>() {
  let resolvePromise!: (value: T | PromiseLike<T>) => void
  let rejectPromise!: (reason?: unknown) => void
  const promise = new Promise<T>((resolve, reject) => {
    resolvePromise = resolve
    rejectPromise = reject
  })
  return { promise, resolve: resolvePromise, reject: rejectPromise }
}

function successfulTokenInfo(refreshToken: string, clientId?: string) {
  return {
    accessToken: `access-refreshed-${refreshToken}`,
    refreshToken: `refresh-refreshed-${refreshToken}`,
    expiresIn: 3600,
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    clientId: clientId ?? 'test-client'
  }
}

function delay(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}

class BoundedReadOpenAIOAuthRefreshFailureRedis implements OpenAIOAuthRefreshFailureRedisClientForTest {
  getCalls = 0
  activeGets = 0
  maxActiveGets = 0

  async get(): Promise<string | null> {
    this.getCalls += 1
    this.activeGets += 1
    this.maxActiveGets = Math.max(this.maxActiveGets, this.activeGets)
    await delay(1)
    this.activeGets -= 1
    return null
  }

  async eval(): Promise<unknown> {
    throw new Error('无退避状态的成功刷新不应执行 Redis failure-state eval')
  }
}

type RefreshFailureRedisPayload = {
  count: number
  localConfigurationCount: number
  backoffUntil: number
  configRevision: number
  mutationId: string
}

function refreshFailureRedisPayload(value: RefreshFailureRedisPayload): string {
  return JSON.stringify(value)
}

class FakeOpenAIOAuthRefreshFailureRedis implements OpenAIOAuthRefreshFailureRedisClientForTest {
  readonly values = new Map<string, string>()
  seedOnFirstGet?: string
  private lastKey?: string

  async get(key: string): Promise<string | null> {
    this.lastKey = key
    if (!this.values.has(key) && this.seedOnFirstGet !== undefined) {
      this.values.set(key, this.seedOnFirstGet)
      this.seedOnFirstGet = undefined
    }
    return this.values.get(key) ?? null
  }

  async eval(script: string, options: { keys: string[]; arguments: string[] }): Promise<unknown> {
    const key = options.keys[0]
    this.lastKey = key
    if (script.includes("raw == ARGV[1]")) {
      const raw = this.values.get(key)
      if (!raw || raw !== options.arguments[0]) return 0
      const expectedRevision = Number(options.arguments[1])
      const parsed = parseRefreshFailureRedisPayload(raw)
      if (expectedRevision > 0 && parsed && parsed.configRevision !== expectedRevision) return 0
      this.values.delete(key)
      return 1
    }

    const incomingBackoffUntil = Math.max(0, Math.trunc(Number(options.arguments[0]) || 0))
    const incomingLocalConfiguration = Number(options.arguments[2]) === 1
    const incomingRevision = Math.max(1, Math.trunc(Number(options.arguments[3]) || 1))
    const incomingMutationId = options.arguments[4]
    const raw = this.values.get(key)
    const stored = raw ? parseRefreshFailureRedisPayload(raw) : undefined
    if (stored && stored.configRevision > incomingRevision) {
      return [
        stored.count,
        stored.backoffUntil,
        stored.localConfigurationCount,
        stored.configRevision,
        0,
        stored.mutationId,
        raw
      ]
    }
    const sameRevision = stored?.configRevision === incomingRevision
    const next: RefreshFailureRedisPayload = {
      count: (sameRevision ? stored?.count ?? 0 : 0) + 1,
      localConfigurationCount: incomingLocalConfiguration
        ? (sameRevision ? stored?.localConfigurationCount ?? 0 : 0) + 1
        : 0,
      backoffUntil: Math.max(sameRevision ? stored?.backoffUntil ?? 0 : 0, incomingBackoffUntil),
      configRevision: incomingRevision,
      mutationId: incomingMutationId
    }
    const nextRaw = refreshFailureRedisPayload(next)
    this.values.set(key, nextRaw)
    return [next.count, next.backoffUntil, next.localConfigurationCount, next.configRevision, 1, next.mutationId, nextRaw]
  }

  setLastObservedKey(raw: string): void {
    assert(this.lastKey, 'Fake Redis 尚未观测 OAuth refresh failure key')
    this.values.set(this.lastKey, raw)
  }

  lastObservedState(): RefreshFailureRedisPayload | undefined {
    if (!this.lastKey) return undefined
    const raw = this.values.get(this.lastKey)
    return raw ? parseRefreshFailureRedisPayload(raw) : undefined
  }
}

function parseRefreshFailureRedisPayload(raw: string): RefreshFailureRedisPayload | undefined {
  try {
    const parsed = JSON.parse(raw) as Partial<RefreshFailureRedisPayload>
    if (
      typeof parsed.count !== 'number'
      || typeof parsed.localConfigurationCount !== 'number'
      || typeof parsed.backoffUntil !== 'number'
      || typeof parsed.configRevision !== 'number'
      || typeof parsed.mutationId !== 'string'
    ) return undefined
    return parsed as RefreshFailureRedisPayload
  } catch {
    return undefined
  }
}

main().catch((error) => {
  console.error('\nOpenAI OAuth Access Token 后台保活回归失败')
  console.error(error instanceof Error ? error.stack ?? error.message : error)
  process.exitCode = 1
})
