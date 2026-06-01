import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-client-ip-account-avoidance-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'client-ip-account-avoidance.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'client-ip-account-avoidance-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'
const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

const [
  { openAIGatewayRouter },
  { captureGatewayRawBody },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  settingsRepository,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue,
  clientIpAvoidance
] = await Promise.all([
  import('../../modules/gateway/openai-gateway.routes.js'),
  import('../../modules/gateway/openai-gateway-request-body-middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/gateway-runtime-cache.service.js'),
  import('../../modules/gateway/gateway-account-side-effects.service.js'),
  import('../../modules/gateway/usage-record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/gateway/openai-gateway-client-ip-account-avoidance.service.js')
])

interface SeededGateway {
  apiKey: string
  groupId: string
  firstAccountId: string
  secondAccountId: string
  firstUpstreamKey: string
  secondUpstreamKey: string
}

interface MockRequestRecord {
  accountKey: string
  scenario: string
}

interface MockUpstreamState {
  requests: MockRequestRecord[]
}

async function main(): Promise<void> {
  let gatewayServer: http.Server | undefined
  let upstreamServer: http.Server | undefined
  const upstreamState: MockUpstreamState = { requests: [] }

  try {
    assertSourceAvoidsPendingFailureArrayRebuilds()
    clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
    settingsRepository.updateSettings({
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0,
      defaultTemporaryUnschedulableMinutes: 5
    })
    gatewayCache.clearGatewayRuntimeCache()

    upstreamServer = createMockOpenAIUpstream(upstreamState)
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`
    const seeded = seedTwoAccountGateway(upstreamBaseUrl)

    gatewayServer = createGatewayServer()
    await listen(gatewayServer)
    const baseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

    await assertClientIpAvoidsFailedAccountAfterSwitch(baseUrl, seeded, upstreamState)
    assertServiceBypassesWhenAllCandidatesAvoided()
    assertServiceSharesAvoidanceAcrossGroupsForSameApiKey()
    assertPendingFailureTrackerIsBoundedAndTransferSafe()

    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    assertAccountsStillActive(seeded)
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 0, '测试清理后不应残留进程级本地账号屏蔽')

    console.log('IP 级账号回避回归通过：同 IP 在前序账号失败且后续账号成功后短期避让失败账号，不影响其他 IP，账号不冷却')
  } finally {
    clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    auditLogQueue.flushAllAuditLogQueue()
    await closeServer(gatewayServer)
    await closeServer(upstreamServer)
    try {
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

function assertSourceAvoidsPendingFailureArrayRebuilds(): void {
  const routesSource = readFileSync(new URL('../../modules/gateway/openai-gateway.routes.ts', import.meta.url), 'utf8')
  assert(!routesSource.includes('[...currentPreflight.clientIpAccountAvoidanceTracker.pendingFailures]'), 'fallback 切组不能复制待确认账号失败数组')
  assert(!routesSource.includes('pendingFailures.unshift(...'), 'fallback 切组不能通过 unshift 搬移待确认账号失败数组')
  assert(routesSource.includes('transferClientIpAccountPendingFailures('), 'fallback 切组应使用有界转移函数传递待确认账号失败')

  const serviceSource = readFileSync(new URL('../../modules/gateway/openai-gateway-client-ip-account-avoidance.service.ts', import.meta.url), 'utf8')
  assert(serviceSource.includes('pendingFailureIndexByAccountId'), '待确认账号失败应维护按账号去重索引')
  assert(serviceSource.includes('clientIpAccountAvoidanceMaxPendingFailures = 256'), '待确认账号失败应有固定上限')
}

async function assertClientIpAvoidsFailedAccountAfterSwitch(
  baseUrl: string,
  seeded: SeededGateway,
  upstreamState: MockUpstreamState
): Promise<void> {
  const ipA = '198.51.100.10'
  const ipB = '198.51.100.20'

  const firstText = await requestChatCompletion(baseUrl, seeded.apiKey, ipA, 'ip-a-prime')
  assert.match(firstText, /ok from second/, `IP A 首次请求应通过切号由第二账号救成功：${firstText}`)
  assert.equal(hitCount(upstreamState, seeded.firstUpstreamKey, 'ip-a-prime'), 1, 'IP A 首次请求应先命中第一账号')
  assert.equal(hitCount(upstreamState, seeded.secondUpstreamKey, 'ip-a-prime'), 1, 'IP A 首次请求应切到第二账号成功')

  const snapshotAfterPrime = clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest()
  assert(snapshotAfterPrime.some((entry) => entry.accountId === seeded.firstAccountId && entry.clientIp === ipA), 'IP A 首次切号成功后应记录第一账号短期回避')
  assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 0, '未命中账号错误策略的上游失败不应同步进入进程级本地屏蔽')
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()

  const followupText = await requestChatCompletion(baseUrl, seeded.apiKey, ipA, 'ip-a-followup')
  assert.match(followupText, /ok from second/, `IP A 后续请求应优先避开第一账号并命中第二账号：${followupText}`)
  assert.equal(hitCount(upstreamState, seeded.firstUpstreamKey, 'ip-a-followup'), 0, 'IP A 后续请求不应继续命中已回避的第一账号')
  assert.equal(hitCount(upstreamState, seeded.secondUpstreamKey, 'ip-a-followup'), 1, 'IP A 后续请求应命中第二账号')

  const ipBText = await requestChatCompletion(baseUrl, seeded.apiKey, ipB, 'ip-b-control')
  assert.match(ipBText, /ok from first/, `IP B 不应继承 IP A 的回避状态，应仍命中第一账号：${ipBText}`)
  assert.equal(hitCount(upstreamState, seeded.firstUpstreamKey, 'ip-b-control'), 1, 'IP B 控制请求应命中第一账号')
  assert.equal(hitCount(upstreamState, seeded.secondUpstreamKey, 'ip-b-control'), 0, 'IP B 控制请求不应被迫切到第二账号')
}

function assertServiceBypassesWhenAllCandidatesAvoided(): void {
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
  const scope = {
    systemAccountId: 'sys_bypass',
    groupId: 'grp_bypass',
    apiKeyId: 'key_bypass',
    clientIp: '203.0.113.9'
  }
  const tracker = clientIpAvoidance.createClientIpAccountAvoidanceTracker(scope)
  const accounts = [
    createTestAccount('avoid-a'),
    createTestAccount('avoid-b')
  ]
  for (const account of accounts) {
    clientIpAvoidance.rememberClientIpAccountPendingFailure(tracker, account, {
      errorPhase: 'upstream_request',
      errorMessage: '测试回避旁路'
    })
  }
  clientIpAvoidance.confirmClientIpAccountAvoidanceAfterSuccess(tracker, 'external-success')
  const ordered = clientIpAvoidance.orderOpenAIAccountsByClientIpAccountAvoidance(accounts, scope)
  assert.equal(ordered.applied, false, '全部候选都被回避时不应应用过滤排序')
  assert.equal(ordered.bypassedAllAvoided, true, '全部候选都被回避时应标记旁路')
  assert.deepEqual(ordered.accounts.map((account) => account.id), accounts.map((account) => account.id), '全部回避旁路时应保留原候选顺序')
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
}

function assertServiceSharesAvoidanceAcrossGroupsForSameApiKey(): void {
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
  const scopeGroupA = {
    systemAccountId: 'sys_cross_group',
    groupId: 'grp_a',
    apiKeyId: 'key_cross_group',
    clientIp: '203.0.113.19'
  }
  const scopeGroupB = {
    ...scopeGroupA,
    groupId: 'grp_b'
  }
  const scopeOtherApiKey = {
    ...scopeGroupA,
    apiKeyId: 'key_other'
  }
  const accounts = [
    createTestAccount('avoid-cross-group-a'),
    createTestAccount('avoid-cross-group-b')
  ]
  const tracker = clientIpAvoidance.createClientIpAccountAvoidanceTracker(scopeGroupA)
  clientIpAvoidance.rememberClientIpAccountPendingFailure(tracker, accounts[0], {
    errorPhase: 'upstream_response',
    statusCode: 502,
    errorCode: 'mock_cross_group'
  })
  clientIpAvoidance.confirmClientIpAccountAvoidanceAfterSuccess(tracker, accounts[1].id)

  const sameApiKeyDifferentGroupOrder = clientIpAvoidance.orderOpenAIAccountsByClientIpAccountAvoidance(accounts, scopeGroupB)
  assert.equal(sameApiKeyDifferentGroupOrder.applied, true, '同一 API Key 下不同分组应共享来源账号回避状态')
  assert.deepEqual(sameApiKeyDifferentGroupOrder.accounts.map((account) => account.id), [accounts[1].id, accounts[0].id])

  const otherApiKeyOrder = clientIpAvoidance.orderOpenAIAccountsByClientIpAccountAvoidance(accounts, scopeOtherApiKey)
  assert.equal(otherApiKeyOrder.applied, false, '不同 API Key 不应共享来源账号回避状态')
  assert.deepEqual(otherApiKeyOrder.accounts.map((account) => account.id), accounts.map((account) => account.id))
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
}

function assertPendingFailureTrackerIsBoundedAndTransferSafe(): void {
  const scope = {
    systemAccountId: 'sys_pending_boundary',
    groupId: 'grp_pending_boundary',
    apiKeyId: 'key_pending_boundary',
    clientIp: '203.0.113.29'
  }
  const tracker = clientIpAvoidance.createClientIpAccountAvoidanceTracker(scope)
  const repeated = createTestAccount('repeat-pending')
  clientIpAvoidance.rememberClientIpAccountPendingFailure(tracker, repeated, {
    errorPhase: 'upstream_response',
    statusCode: 502,
    errorCode: 'stale'
  })
  clientIpAvoidance.rememberClientIpAccountPendingFailure(tracker, repeated, {
    errorPhase: 'upstream_response',
    statusCode: 503,
    errorCode: 'latest'
  })
  assert.equal(tracker.pendingFailures.length, 1, '同一请求内同账号失败应按账号去重')
  assert.equal(tracker.pendingFailures[0]?.statusCode, 503, '重复账号失败应保留最新失败信息')
  assert.equal(tracker.pendingFailureIndexByAccountId.size, 1, '待确认失败索引应同步去重')

  for (let index = 0; index < 300; index += 1) {
    clientIpAvoidance.rememberClientIpAccountPendingFailure(tracker, createTestAccount(`overflow-${index}`), {
      errorPhase: 'upstream_request',
      errorMessage: `overflow-${index}`
    })
  }
  assert.equal(tracker.pendingFailures.length, 256, '单请求待确认账号失败数量应固定封顶')
  assert.equal(tracker.pendingFailureIndexByAccountId.size, 256, '待确认失败索引大小应跟随固定上限')
  assert(!tracker.pendingFailureIndexByAccountId.has('overflow-299'), '超过上限的待确认失败不应继续扩容')

  const source = clientIpAvoidance.createClientIpAccountAvoidanceTracker(scope)
  const target = clientIpAvoidance.createClientIpAccountAvoidanceTracker(scope)
  const shared = createTestAccount('transfer-shared')
  clientIpAvoidance.rememberClientIpAccountPendingFailure(target, shared, {
    errorPhase: 'upstream_response',
    statusCode: 500,
    errorCode: 'target-old'
  })
  clientIpAvoidance.rememberClientIpAccountPendingFailure(source, shared, {
    errorPhase: 'upstream_response',
    statusCode: 502,
    errorCode: 'source-new'
  })
  clientIpAvoidance.rememberClientIpAccountPendingFailure(source, createTestAccount('transfer-a'), {
    errorPhase: 'upstream_request',
    errorMessage: 'transfer-a'
  })
  clientIpAvoidance.rememberClientIpAccountPendingFailure(source, createTestAccount('transfer-b'), {
    errorPhase: 'upstream_request',
    errorMessage: 'transfer-b'
  })
  clientIpAvoidance.transferClientIpAccountPendingFailures(source, target)
  assert.equal(source.pendingFailures.length, 0, '转移后来源 tracker 应清空待确认失败')
  assert.equal(source.pendingFailureIndexByAccountId.size, 0, '转移后来源 tracker 索引应同步清空')
  assert.equal(target.pendingFailures.length, 3, '转移到目标 tracker 时应去重追加而不是数组头插')
  const sharedIndex = target.pendingFailureIndexByAccountId.get(shared.id)
  assert.notEqual(sharedIndex, undefined, '目标 tracker 应保留共享账号索引')
  assert.equal(target.pendingFailures[sharedIndex ?? -1]?.errorCode, 'source-new', '转移时同账号失败应更新为来源最新信息')
}

function seedTwoAccountGateway(upstreamBaseUrl: string): SeededGateway {
  const group = repositories.createGroup({
    name: 'IP 级账号回避回归分组',
    providerCode: 'openai',
    enabled: true
  }, access)
  const firstUpstreamKey = 'sk-client-ip-avoidance-first'
  const secondUpstreamKey = 'sk-client-ip-avoidance-second'
  const firstAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '01-IP回避首选账号',
    type: 'api_key',
    credentials: {
      api_key: firstUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  const secondAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '02-IP回避备用账号',
    type: 'api_key',
    credentials: {
      api_key: secondUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 10
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: 'IP 级账号回避回归 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '临时 API Key 未返回明文密钥')
  gatewayCache.clearGatewayRuntimeCache()
  return {
    apiKey: apiKey.key,
    groupId: group.id,
    firstAccountId: firstAccount.id,
    secondAccountId: secondAccount.id,
    firstUpstreamKey,
    secondUpstreamKey
  }
}

function createGatewayServer(): http.Server {
  const app = express()
  app.use(requestContextMiddleware)
  app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
  return http.createServer(app)
}

function createMockOpenAIUpstream(state: MockUpstreamState): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(Buffer.from(chunk)))
    req.on('end', () => {
      const accountKey = bearerToken(req.headers.authorization)
      const body = parseJsonObject(Buffer.concat(chunks).toString('utf8'))
      const scenario = requestScenario(body)
      state.requests.push({ accountKey, scenario })

      if (scenario === 'ip-a-prime' && accountKey === 'sk-client-ip-avoidance-first') {
        res.writeHead(502, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { message: 'temporary upstream failure for IP A', type: 'mock_error', code: 'mock_unconfirmed_failure' } }))
        return
      }

      const from = accountKey === 'sk-client-ip-avoidance-first' ? 'first' : 'second'
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({
        id: `chatcmpl-client-ip-${from}`,
        object: 'chat.completion',
        choices: [
          {
            index: 0,
            message: { role: 'assistant', content: `ok from ${from}` },
            finish_reason: 'stop'
          }
        ],
        usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
      }))
    })
  })
}

async function requestChatCompletion(baseUrl: string, apiKey: string, clientIp: string, scenario: string): Promise<string> {
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      'x-forwarded-for': clientIp
    },
    body: JSON.stringify({
      model: 'gpt-4o-mini',
      messages: [{ role: 'user', content: scenario }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `请求 ${scenario} 应成功，实际 HTTP ${response.status}: ${text}`)
  return text
}

function assertAccountsStillActive(seeded: SeededGateway): void {
  const accounts = repositories.listAccounts(access)
  for (const accountId of [seeded.firstAccountId, seeded.secondAccountId]) {
    const account = accounts.find((item) => item.id === accountId)
    assert(account, `账号 ${accountId} 不存在`)
    assert.equal(account.status, 'active', `账号 ${account.name} 不应被冷却或停用`)
    assert.equal(account.schedulable, true, `账号 ${account.name} 不应变为不可调度`)
    assert.equal(account.cooldownUntil, undefined, `账号 ${account.name} 不应写入冷却时间`)
    assert.equal(account.lastErrorMessage, undefined, `账号 ${account.name} 不应写入最近错误`)
  }
}

function hitCount(state: MockUpstreamState, accountKey: string, scenario: string): number {
  return state.requests.filter((request) => request.accountKey === accountKey && request.scenario === scenario).length
}

function bearerToken(value: unknown): string {
  const text = Array.isArray(value) ? value[0] : String(value ?? '')
  return text.replace(/^Bearer\s+/i, '')
}

function requestScenario(body: Record<string, unknown>): string {
  const messages = Array.isArray(body.messages) ? body.messages : []
  const firstMessage = messages.find((item): item is Record<string, unknown> => typeof item === 'object' && item !== null && !Array.isArray(item))
  return typeof firstMessage?.content === 'string' ? firstMessage.content : 'unknown'
}

function parseJsonObject(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function createTestAccount(id: string): Parameters<typeof clientIpAvoidance.orderOpenAIAccountsByClientIpAccountAvoidance>[0][number] {
  return {
    id,
    providerCode: 'openai',
    systemAccountId: 'sys_bypass',
    accountOwnerSystemAccountId: 'sys_bypass',
    groupOwnerSystemAccountId: 'sys_bypass',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: id,
    type: 'api_key',
    status: 'active',
    credentials: {},
    apiKey: '',
    baseUrl: 'http://127.0.0.1/v1',
    proxyProfileId: undefined,
    concurrencyLimit: 1,
    errorPolicyId: undefined,
    cooldownUntil: undefined,
    lastErrorMessage: undefined,
    streamFailureCount: 0,
    streamFailureWindowStartedAt: undefined,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    supportedModels: []
  }
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
  if (!address || typeof address === 'string') {
    throw new Error('服务地址不可用')
  }
  return address.port
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

await main()
