import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { OPENAI_COMPATIBLE_PROVIDER_CODE } from '../../domain/provider-protocol.js'
import { clearAccountConcurrency, tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-quality-gateway-status-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-quality-gateway-status-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { captureGatewayRawBody },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue,
  usageStatsRepository,
  accountQualityRepository,
  accountQualityFailurePrecheckService,
  cooldownRetestService
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../modules/gateway/request/body-middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/account-quality.repository.js'),
  import('../../modules/background/account-quality-failure-precheck.service.js'),
  import('../../modules/background/cooldown-account-retest.service.js')
])

interface MockUpstreamState {
  mode: 'failure' | 'success' | 'slow_headers'
  hits: number
}

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  let upstreamServer: http.Server | undefined
  let gatewayServer: http.Server | undefined
  const upstreamState: MockUpstreamState = { mode: 'failure', hits: 0 }

  try {
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
    updateSystemSettingsForTest({
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0,
      defaultTemporaryUnschedulableMinutes: 5
    })
    gatewayCache.clearGatewayRuntimeCache()
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()

    upstreamServer = createMockOpenAIUpstream(upstreamState)
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`

    const group = repositories.createGroup({
      name: '状态质量 mock AI 分组',
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      enabled: true
    }, access)
    const account = repositories.createAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      name: '状态质量 mock AI 账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-quality-gateway-status-upstream',
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true
    }, access)
    const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: '状态质量 mock AI Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, '回归 API Key 未返回明文密钥')

    gatewayServer = http.createServer(app)
    await listen(gatewayServer)
    const baseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

    for (let index = 0; index < 5; index += 1) {
      const response = await requestChatCompletion(baseUrl, apiKey.key, `mock upstream 504 ${index}`)
      assert.equal(response.status, 503, `单账号上游 504 用尽候选后应返回网关 503，实际 HTTP ${response.status}: ${response.text}`)
      assert.match(response.text, /没有可用的上游账户/, `网关失败响应应说明没有可用上游账户：${response.text}`)
      accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    }
    assert.equal(upstreamState.hits, 5, 'mock 上游应收到 5 次真实失败请求')

    upstreamState.mode = 'success'
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    const successResponse = await requestChatCompletion(baseUrl, apiKey.key, 'mock upstream recovery')
    assert.equal(successResponse.status, 200, `mock 上游恢复后网关请求应成功，实际 HTTP ${successResponse.status}: ${successResponse.text}`)
    assert.match(successResponse.text, /mock quality recovery ok/, '成功响应应来自 mock 上游')
    assert.equal(upstreamState.hits, 6, 'mock 上游应收到 5 次失败和 1 次成功请求')

    usageRecordQueue.flushAllUsageRecordQueue()
    assert.equal(usageStatsRepository.aggregateUsageStatsBatch(100, usageStatsSafeCreatedBeforeForTest()), 6, '真实网关使用记录应进入统计聚合')
    const qualityResult = accountQualityRepository.refreshAccountQualityFromUsage(10)
    assert.equal(qualityResult.refreshed, 1, '账号质量刷新应处理 mock AI 命中的账户')

    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    const listed = repositories.listAccountsPage(access, { keyword: account.name, page: 1, pageSize: 10 }).items.find((item) => item.id === account.id)
    assert(listed, '账户列表应返回 mock AI 账户')
    assert.equal(listed.status, 'active', '频繁 504 后恢复成功不应把账户持久状态写死')
    assert.equal(listed.effectiveAvailability.status, 'available', '质量反馈不应改变账户筛选可用性')
    assert.equal(listed.qualityRecentRequestCount, 6, '账户列表应返回真实聚合的近窗口请求数')
    assert.equal(listed.qualityRecentErrorCount, 5, '账户列表应返回真实聚合的近窗口失败数')
    assert.equal(Math.round((listed.qualityRecentSuccessRate ?? 0) * 100), 17, '账户列表应返回真实聚合的近窗口成功率')
    assert.match(listed.qualityLastErrorMessage ?? '', /mock upstream 504/, '账户列表应返回 mock 上游最后失败原因')

    const capacityGroup = repositories.createGroup({
      name: '状态质量本地容量失败 mock AI 分组',
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      enabled: true
    }, access)
    const capacityAccount = repositories.createAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      name: '状态质量本地容量失败 mock AI 账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-quality-local-capacity',
        base_url: upstreamBaseUrl
      },
      groupId: capacityGroup.id,
      status: 'active',
      schedulable: true,
      concurrencyLimit: 1
    }, access)
    const capacityApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: '状态质量本地容量失败 mock AI Key',
      groupBindings: [{ groupId: capacityGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(capacityApiKey.key, '本地容量失败回归 API Key 未返回明文密钥')

    const heldCapacitySlot = tryAcquireAccountConcurrency(capacityAccount.id, capacityAccount.concurrencyLimit)
    assert.equal(heldCapacitySlot.acquired, true, '本地容量失败回归前应成功占用账号并发槽')
    const capacityHitsBefore = upstreamState.hits
    try {
      for (let index = 0; index < 5; index += 1) {
        const response = await requestChatCompletion(baseUrl, capacityApiKey.key, `local capacity saturated ${index}`)
        assert.equal(response.status, 503, `账号并发满应由网关返回 503，实际 HTTP ${response.status}: ${response.text}`)
        assert.match(response.text, /并发已达到上限|没有可用的上游账户/, `账号并发满响应应保留容量失败原因：${response.text}`)
        accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
      }
    } finally {
      heldCapacitySlot.release()
      clearAccountConcurrency()
    }
    assert.equal(upstreamState.hits, capacityHitsBefore, '账号并发满属于本地容量失败，不应命中 mock 上游')

    usageRecordQueue.flushAllUsageRecordQueue()
    const capacityUsageRecords = repositories.listUsageRecords(access, { page: 1, pageSize: 50, result: 'failed' }).items
      .filter((record) => record.accountId === capacityAccount.id)
    assert.equal(capacityUsageRecords.length, 5, '账号并发满应保留 5 条失败使用记录')
    assert.equal(capacityUsageRecords.every((record) => record.failureAttribution === 'gateway_capacity'), true, '账号并发满使用记录必须归因为本地容量失败')
    assert.equal(usageStatsRepository.aggregateUsageStatsBatch(100, usageStatsSafeCreatedBeforeForTest()), 5, '账号并发满使用记录应进入通用统计聚合')
    accountQualityRepository.refreshAccountQualityFromUsage(10)
    const capacityQualityStats = databaseModule.getStatsDatabase()
      .prepare('SELECT SUM(request_count) AS request_count, SUM(error_count) AS error_count FROM account_quality_minute_stats WHERE account_id = ?')
      .get(capacityAccount.id) as { request_count?: number | null; error_count?: number | null } | undefined
    assert.equal(Number(capacityQualityStats?.request_count ?? 0), 0, '账号并发满不应写入账号质量分钟请求样本')
    assert.equal(Number(capacityQualityStats?.error_count ?? 0), 0, '账号并发满不应写入账号质量分钟失败样本')
    const capacityCandidate = accountQualityRepository
      .listAccountQualityFailurePrecheckCandidates(10)
      .find((candidate) => candidate.accountId === capacityAccount.id)
    assert.equal(capacityCandidate, undefined, '账号并发满不应进入近期质量频繁失败后台确认候选')
    const capacityListed = repositories.listAccountsPage(access, { keyword: capacityAccount.name, page: 1, pageSize: 10 }).items.find((item) => item.id === capacityAccount.id)
    assert(capacityListed, '账户列表应返回本地容量失败账号')
    assert.equal(capacityListed.qualityRecentRequestCount, undefined, '账号并发满不应在账户列表展示为账号近期质量请求')
    assert.equal(capacityListed.qualityRecentErrorCount, undefined, '账号并发满不应在账户列表展示为账号近期质量失败')
    assert.equal(capacityListed.qualityLastErrorMessage, undefined, '账号并发满不应在账户列表展示为账号近期质量最后原因')

    const abortGroup = repositories.createGroup({
      name: '状态质量客户端断开 mock AI 分组',
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      enabled: true
    }, access)
    const abortAccount = repositories.createAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      name: '状态质量客户端断开 mock AI 账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-quality-client-abort',
        base_url: upstreamBaseUrl
      },
      groupId: abortGroup.id,
      status: 'active',
      schedulable: true
    }, access)
    const abortApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: '状态质量客户端断开 mock AI Key',
      groupBindings: [{ groupId: abortGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(abortApiKey.key, '客户端断开回归 API Key 未返回明文密钥')

    upstreamState.mode = 'slow_headers'
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    const abortHitsBefore = upstreamState.hits
    await requestAndAbortAfterUpstreamHit(baseUrl, abortApiKey.key, 'client abort after upstream started', () => upstreamState.hits > abortHitsBefore)
    assert.equal(upstreamState.hits, abortHitsBefore + 1, '客户端断开前应已经命中 mock 上游一次')

    const abortUsageRecords = await waitForUsageRecords(abortAccount.id, 1)
    assert.equal(abortUsageRecords[0]?.failureAttribution, 'client_lifecycle', '客户端断开后的使用记录必须归因为客户端生命周期')
    assert.match(abortUsageRecords[0]?.errorMessage ?? '', /下游连接提前关闭/, '客户端断开后的使用记录应保留下游断开原因')
    assert.equal(usageStatsRepository.aggregateUsageStatsBatch(100, usageStatsSafeCreatedBeforeForTest()), 1, '客户端断开使用记录应进入通用统计聚合')
    accountQualityRepository.refreshAccountQualityFromUsage(10)
    const abortQualityStats = databaseModule.getStatsDatabase()
      .prepare('SELECT SUM(request_count) AS request_count, SUM(error_count) AS error_count FROM account_quality_minute_stats WHERE account_id = ?')
      .get(abortAccount.id) as { request_count?: number | null; error_count?: number | null } | undefined
    assert.equal(Number(abortQualityStats?.request_count ?? 0), 0, '客户端断开不应写入账号质量分钟请求样本')
    assert.equal(Number(abortQualityStats?.error_count ?? 0), 0, '客户端断开不应写入账号质量分钟失败样本')
    assert.equal(
      accountQualityRepository.listAccountQualityFailurePrecheckCandidates(10).some((candidate) => candidate.accountId === abortAccount.id),
      false,
      '客户端断开不应进入近期质量频繁失败后台确认候选'
    )

    upstreamState.mode = 'failure'
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    const failureGroup = repositories.createGroup({
      name: '状态质量确认失败 mock AI 分组',
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      enabled: true
    }, access)
    const failureAccount = repositories.createAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      name: '状态质量确认失败 mock AI 账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-quality-precheck-failure',
        base_url: upstreamBaseUrl
      },
      groupId: failureGroup.id,
      status: 'active',
      schedulable: true
    }, access)
    const failureApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: '状态质量确认失败 mock AI Key',
      groupBindings: [{ groupId: failureGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(failureApiKey.key, '频繁失败确认回归 API Key 未返回明文密钥')

    const failureHitsBefore = upstreamState.hits
    for (let index = 0; index < 5; index += 1) {
      const response = await requestChatCompletion(baseUrl, failureApiKey.key, `mock precheck failure ${index}`)
      assert.equal(response.status, 503, `频繁失败确认前置请求应由 mock 上游失败触发网关 503，实际 HTTP ${response.status}: ${response.text}`)
      accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    }
    assert.equal(upstreamState.hits - failureHitsBefore, 5, '频繁失败确认账号应收到 5 次真实 mock 上游失败请求')

    usageRecordQueue.flushAllUsageRecordQueue()
    assert.equal(usageStatsRepository.aggregateUsageStatsBatch(100, usageStatsSafeCreatedBeforeForTest()), 5, '频繁失败确认账号的真实失败记录应进入统计聚合')
    accountQualityRepository.refreshAccountQualityFromUsage(10)
    const failureCandidate = accountQualityRepository
      .listAccountQualityFailurePrecheckCandidates(10)
      .find((candidate) => candidate.accountId === failureAccount.id)
    assert(failureCandidate, '真实 mock AI 频繁失败账号应进入质量失败确认候选')
    assert.equal(accountQualityFailurePrecheckService.enqueueAccountQualityFailurePrecheck(failureCandidate), true, '频繁失败候选应能进入后台确认队列')

    const temporaryUnavailable = await waitForAccountStatus(failureAccount.id, 'temporary_unavailable')
    assert(temporaryUnavailable, '后台确认失败后账号应升级为临时不可调用')
    assert.equal(temporaryUnavailable.effectiveAvailability.status, 'instance_temporary_unavailable', '临时不可调用应改变实际可用性')
    assert.match(temporaryUnavailable.lastErrorMessage ?? '', /近期质量频繁失败/, '临时不可调用原因应说明来自近期质量频繁失败确认')

    upstreamState.mode = 'success'
    databaseModule.getBusinessDatabase()
      .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
      .run(new Date(Date.now() - 1000).toISOString(), failureAccount.id)
    const dueForRecovery = repositories.findAccountSummary(failureAccount.id, access)
    assert(dueForRecovery, '冷却复测前应能读取临时不可调用账号')
    assert.equal(cooldownRetestService.enqueueCooldownAccountRetest(dueForRecovery, {
      maxPauseMinutes: 10,
      maxRecoveryHours: 1,
      longTermIntervalHours: 24
    }), true, '临时不可调用账号应能进入冷却复测队列')
    const recovered = await waitForAccountStatus(failureAccount.id, 'active')
    assert(recovered, 'mock 上游恢复后冷却复测应把账号恢复正常')
    assert.equal(recovered.effectiveAvailability.status, 'available', '冷却复测恢复后账号应重新可用')
    assert.equal(recovered.cooldownUntil, undefined, '冷却复测恢复后应清理冷却时间')
    assert.equal(recovered.lastErrorMessage, undefined, '冷却复测恢复后应清理错误原因')

    console.log('账号质量状态 mock AI 回归通过：真实网关失败进入质量标签，后台确认失败升级临时不可调用，mock 上游恢复后冷却复测恢复 active')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    clearAccountConcurrency()
    auditLogQueue.flushAllAuditLogQueue()
    await closeServer(gatewayServer)
    await closeServer(upstreamServer)
  }
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function createMockOpenAIUpstream(state: MockUpstreamState): http.Server {
  return http.createServer((_req, res) => {
    state.hits += 1
    if (state.mode === 'slow_headers') {
      const timer = setTimeout(() => {
        if (res.destroyed || res.writableEnded) return
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({
          id: 'chatcmpl-account-quality-client-abort',
          object: 'chat.completion',
          choices: [
            {
              index: 0,
              message: { role: 'assistant', content: 'mock delayed ok' },
              finish_reason: 'stop'
            }
          ],
          usage: {
            input_tokens: 3,
            output_tokens: 4,
            total_tokens: 7
          }
        }))
      }, 5_000)
      res.once('close', () => clearTimeout(timer))
      return
    }
    if (state.mode === 'failure') {
      res.writeHead(504, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({
        error: {
          message: `mock upstream 504 failure ${state.hits}`,
          code: 'mock_upstream_504',
          type: 'upstream_timeout'
        }
      }))
      return
    }
    res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
    res.end(JSON.stringify({
      id: 'chatcmpl-account-quality-gateway-status',
      object: 'chat.completion',
      choices: [
        {
          index: 0,
          message: { role: 'assistant', content: 'mock quality recovery ok' },
          finish_reason: 'stop'
        }
      ],
      usage: {
        input_tokens: 3,
        output_tokens: 4,
        total_tokens: 7
      }
    }))
  })
}

async function requestChatCompletion(baseUrl: string, apiKey: string, content: string): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content }],
      stream: false
    })
  })
  return {
    status: response.status,
    text: await response.text()
  }
}

async function requestAndAbortAfterUpstreamHit(
  baseUrl: string,
  apiKey: string,
  content: string,
  upstreamHit: () => boolean
): Promise<void> {
  const controller = new AbortController()
  const request = fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content }],
      stream: false
    }),
    signal: controller.signal
  })
  await waitForCondition(upstreamHit, 2_000, 'mock 上游未在客户端断开前收到请求')
  controller.abort()
  await assert.rejects(request, /aborted|AbortError|This operation was aborted/i, '客户端 abort 后 fetch 应被取消')
  await new Promise((resolvePromise) => setTimeout(resolvePromise, 100))
}

async function waitForUsageRecords(accountId: string, expectedCount: number, timeoutMs = 3_000): Promise<ReturnType<typeof repositories.listUsageRecords>['items']> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    usageRecordQueue.flushAllUsageRecordQueue()
    const records = repositories.listUsageRecords(access, { page: 1, pageSize: 100, result: 'failed' }).items
      .filter((record) => record.accountId === accountId)
    if (records.length >= expectedCount) {
      return records
    }
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 50))
  }
  usageRecordQueue.flushAllUsageRecordQueue()
  return repositories.listUsageRecords(access, { page: 1, pageSize: 100, result: 'failed' }).items
    .filter((record) => record.accountId === accountId)
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(typeof address === 'object' && address !== null, 'server 未监听端口')
  return address.port
}

function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server || !server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.close((error) => {
      if (error) rejectPromise(error)
      else resolvePromise()
    })
  })
}

function usageStatsSafeCreatedBeforeForTest(): string {
  return new Date(Date.now() + 1000).toISOString()
}

async function waitForCondition(predicate: () => boolean, timeoutMs: number, timeoutMessage: string): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (predicate()) return
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 20))
  }
  assert.fail(timeoutMessage)
}

async function waitForAccountStatus(accountId: string, status: string, timeoutMs = 10_000): Promise<ReturnType<typeof repositories.findAccountSummary> | undefined> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const account = repositories.findAccountSummary(accountId, access)
    if (account?.status === status) {
      return account
    }
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 50))
  }
  return undefined
}

function updateSystemSettingsForTest(settings: Record<string, unknown>): void {
  const now = new Date().toISOString()
  const statement = databaseModule.getBusinessDatabase().prepare(`
    INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
    VALUES ('sys_admin', ?, ?, ?)
    ON CONFLICT(system_account_id, key) DO UPDATE SET
      value_json = excluded.value_json,
      updated_at = excluded.updated_at
  `)
  for (const [key, value] of Object.entries(settings)) {
    statement.run(key, JSON.stringify(value), now)
  }
}
