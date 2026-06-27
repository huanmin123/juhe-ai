import { strict as assert } from 'node:assert'
import { createHash } from 'node:crypto'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  OPENAI_PROTOCOL_CODE
} from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { clearAccountConcurrency, tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'
import type { UsageRecordSummary } from '../../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-api-key-group-route-capability-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'api-key-group-route-capability-secret'
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
  settingsRepository,
  dbServiceHandlers,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue,
  authorizationQuotaService,
  usageStatsRepository,
  usageRecordShards,
  responseInspectionPolicyRepository,
  upstreamModule,
  clientIpAccountAvoidance
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../modules/gateway/request/body-middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/db-service/db-service-handlers.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/gateway/quota/authorization-quota.service.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/usage-record-shards.js'),
  import('../../storage/response-inspection-policy.repository.js'),
  import('../../modules/gateway/upstream/request.js'),
  import('../../modules/gateway/runtime/client-ip-account-avoidance.service.js')
])

usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)

interface MockUpstreamRequest {
  path: string
  accountKey: string
  model?: string
}

type SimulatedGroupRouteStrategy = 'priority_failover' | 'round_robin' | 'weighted_round_robin'

const upstreamRequests: MockUpstreamRequest[] = []
const failingUpstreamKeys = new Set<string>()
const releaseLocalSuppressionsBeforeRespondingKeys = new Set<string>()
let gatewayServer: http.Server | undefined
let upstreamServer: http.Server | undefined

try {
  clearAccountConcurrency()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  clientIpAccountAvoidance.clearClientIpAccountAvoidanceForTest()
  failingUpstreamKeys.clear()
  releaseLocalSuppressionsBeforeRespondingKeys.clear()
  settingsRepository.updateSettings({ temporaryUnschedulableRetryAttempts: 0 })
  upstreamServer = createMockOpenAIUpstream()
  await listen(upstreamServer)
  const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`

  gatewayServer = createGatewayServer()
  await listen(gatewayServer)
  const gatewayBaseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

  await assertCapabilityFallback(gatewayBaseUrl, upstreamBaseUrl)
  await assertCapabilityThenBusyMultiHopFallback(gatewayBaseUrl, upstreamBaseUrl)
  await assertModelFallback(gatewayBaseUrl, upstreamBaseUrl)
  await assertModelSpecificAccountPreferred(gatewayBaseUrl, upstreamBaseUrl)
  await assertHighConcurrencyModelSpecificAccountPreferred(gatewayBaseUrl, upstreamBaseUrl)
  await assertPersonalQualityTieBreakWithMockAI(gatewayBaseUrl, upstreamBaseUrl)
  await assertHighConcurrencyQualityTieBreakWithMockAI(gatewayBaseUrl, upstreamBaseUrl)
  await assertNormalApiKeyCrossProviderModelRoute(gatewayBaseUrl, upstreamBaseUrl)
  await assertNormalApiKeyUnknownModelReturnsLocalError(gatewayBaseUrl, upstreamBaseUrl)
  await assertHighConcurrencyBusyFallback(gatewayBaseUrl, upstreamBaseUrl)
  await assertPersonalConcurrencyBusyFallback(gatewayBaseUrl, upstreamBaseUrl)
  await assertLocalSuppressionFallback(gatewayBaseUrl, upstreamBaseUrl)
  await assertAuthorizationQuotaFallback(gatewayBaseUrl, upstreamBaseUrl)
  await assertResponseInspectionFallbackToNextGroup(gatewayBaseUrl, upstreamBaseUrl)
  await assertCrossGroupFallbackAfterUpstreamAccountsExhausted(gatewayBaseUrl, upstreamBaseUrl)
  await assertAllRouteStrategiesFallbackAfterUpstreamAccountsExhausted(gatewayBaseUrl, upstreamBaseUrl)
  await assertKeyRedistributionWrapsToRecoveredPrimaryAccount(gatewayBaseUrl, upstreamBaseUrl)

  console.log('API Key 分组请求级路由回归通过：主号池路径能力、模型不匹配、同组显式模型命中账户优先、高并发号池显式模型命中账户优先、普通/高并发同级多账号按历史质量分选择、普通 Key 跨供应商模型路由、授权额度耗尽、分组容量硬满或本地短期屏蔽时，会在派发前切到可承接的后备分组；响应检查未写下游且当前号池耗尽时可切后备分组；真实上游失败耗尽当前号池账号且未写下游时，会回到 API Key 分组候选序列继续尝试；主备、轮询、权重三种策略下账号耗尽 fallback 均按候选顺序工作；A -> B -> C 后 A 出现未失败可用账号时可回到 A 承接')
} finally {
  clearAccountConcurrency()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  clientIpAccountAvoidance.clearClientIpAccountAvoidanceForTest()
  failingUpstreamKeys.clear()
  releaseLocalSuppressionsBeforeRespondingKeys.clear()
  usageRecordQueue.flushAllUsageRecordQueue()
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  auditLogQueue.flushAllAuditLogQueue()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  upstreamModule.closeGatewayUpstreamAgentsForTest()
  await closeServer(gatewayServer)
  await closeServer(upstreamServer)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertResponseInspectionFallbackToNextGroup(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'route_response_inspection_fallback_owner',
    displayName: '响应检查切后备用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const primaryGroup = repositories.createGroup({
    name: '响应检查主号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const fallbackGroup = repositories.createGroup({
    name: '响应检查后备号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const primaryUpstreamKey = 'sk-route-response-inspection-primary'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '响应检查主号池账号',
    type: 'api_key',
    credentials: {
      api_key: primaryUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: primaryGroup.id,
    status: 'active',
    schedulable: true,
  }, access)
  const fallbackUpstreamKey = 'sk-route-response-inspection-fallback'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '响应检查后备账号',
    type: 'api_key',
    credentials: {
      api_key: fallbackUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: fallbackGroup.id,
    status: 'active',
    schedulable: true,
  }, access)
  responseInspectionPolicyRepository.createResponseInspectionPolicy({
    name: '回归：未写下游污染流切后备',
    enabled: true,
    priority: 20,
    scopeType: 'provider',
    protocolCode: OPENAI_PROTOCOL_CODE,
    providerCode: 'gpt',
    match: { outputTextIncludes: ['route-stream-pollution'] },
    action: 'retry_next_account'
  })
  const apiKey = repositories.createApiKeyRecord({
    name: '响应检查切后备 API Key',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: fallbackGroup.id, priority: 2, status: 'active' }
    ]
  }, access)
  gatewayCache.clearGatewayRuntimeCache()

  const beforeCount = upstreamRequests.length
  const traceId = 'trace-route-response-inspection-fallback'
  const response = await requestResponseStream(gatewayBaseUrl, apiKey.key, traceId)
  assert.equal(response.status, 200, `响应检查当前号池耗尽后应切后备并成功，实际 ${response.status}: ${response.text}`)
  assert(response.text.includes('route stream ok'), `后备流式响应应返回成功内容：${response.text}`)
  assert(!response.text.includes('route-stream-pollution'), `写下游前被拦截的主号池污染事件不应泄露给客户端：${response.text}`)
  const newRequests = upstreamRequests.slice(beforeCount)
  assert.equal(newRequests.length, 2, '响应检查切后备应先命中主号池，再命中后备号池')
  assert.equal(newRequests[0]?.accountKey, primaryUpstreamKey, '响应检查切后备应先尝试主号池账号')
  assert.equal(newRequests[1]?.accountKey, fallbackUpstreamKey, '响应检查切后备应在主号池耗尽后命中后备账号')

  usageRecordQueue.flushAllUsageRecordQueue()
  auditLogQueue.flushAllAuditLogQueue()
  const usageRecords = usageRecordsByTraceId(traceId)
  assert(usageRecords.some((record) => record.groupId === primaryGroup.id && record.success === false), '主号池响应检查失败应记录失败尝试并归属主分组')
  assert(usageRecords.some((record) => record.groupId === fallbackGroup.id && record.success === true), '后备流式成功应记录成功尝试并归属后备分组')
  const auditLogs = repositories.listAuditLogs({ traceId, pageSize: 10 })
  assert.equal(auditLogs.total, 1, '响应检查切后备应写入一条完整审计事件')
  const metadataPayloads = await gatewayMetadataPayloads(auditLogs.items[0]?.id ?? '')
  assert(metadataPayloads.some((metadata) => metadata.label === 'response_inspection_server_retry'
    && metadata.metadata?.policyName === '回归：未写下游污染流切后备'), '审计 metadata 应记录配置化响应检查服务端重试')
  assert(metadataPayloads.some((metadata) => metadata.label === 'api_key_group_route_fallback'
    && metadata.metadata?.reason === 'response_inspection_server_retry_exhausted'
    && metadata.metadata?.fromGroupId === primaryGroup.id
    && metadata.metadata?.toGroupId === fallbackGroup.id), '审计 metadata 应记录响应检查耗尽当前分组后的跨分组后备切换')
}

async function assertCrossGroupFallbackAfterUpstreamAccountsExhausted(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'route_exhausted_fallback_owner',
    displayName: '上游账号耗尽切后备用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const primaryGroup = repositories.createGroup({
    name: '上游账号耗尽主号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const fallbackGroup = repositories.createGroup({
    name: '上游账号耗尽后备号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const primaryUpstreamKey = 'sk-route-upstream-exhausted-primary'
  failingUpstreamKeys.add(primaryUpstreamKey)
  const primaryAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '上游账号耗尽主号池账号',
    type: 'api_key',
    credentials: {
      api_key: primaryUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: primaryGroup.id,
    status: 'active',
    schedulable: true,
  }, access)
  const fallbackUpstreamKey = 'sk-route-upstream-exhausted-fallback'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '上游账号耗尽后备账号',
    type: 'api_key',
    credentials: {
      api_key: fallbackUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: fallbackGroup.id,
    status: 'active',
    schedulable: true,
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '上游账号耗尽切后备 API Key',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: fallbackGroup.id, priority: 2, status: 'active' }
    ]
  }, access)
  gatewayCache.clearGatewayRuntimeCache()

  const runtime = await dbServiceHandlers.handleDbServiceOperation({ type: 'read_gateway_runtime', key: apiKey.key })
  assert.equal(runtime.apiKey?.selected_group_id, primaryGroup.id, '基础运行时应先选中可承接的主号池')

  const beforeCount = upstreamRequests.length
  const traceId = 'trace-route-upstream-exhausted-fallback'
  const clientIp = '203.0.113.10'
  const response = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.5', traceId, clientIp)
  const responseText = response.text
  assert.equal(response.status, 200, `主号池真实上游失败且账号耗尽后应切后备并成功，实际 ${response.status}: ${responseText}`)
  const newRequests = upstreamRequests.slice(beforeCount)
  assert.equal(newRequests.length, 2, '账号耗尽切后备应先命中主号池，再命中后备号池')
  assert.equal(newRequests[0]?.accountKey, primaryUpstreamKey, '账号耗尽切后备应先尝试主号池账号')
  assert.equal(newRequests[1]?.accountKey, fallbackUpstreamKey, '主号池账号耗尽后应命中后备号池账号')

  usageRecordQueue.flushAllUsageRecordQueue()
  auditLogQueue.flushAllAuditLogQueue()
  const usageRecords = usageRecordsByTraceId(traceId)
  assert(usageRecords.some((record) => record.groupId === primaryGroup.id && record.success === false), '主号池真实上游失败应记录失败尝试并归属主分组')
  assert(usageRecords.some((record) => record.groupId === fallbackGroup.id && record.success === true), '后备分组成功应记录成功尝试并归属后备分组')
  const auditLogs = repositories.listAuditLogs({ traceId, pageSize: 10 })
  assert.equal(auditLogs.total, 1, '账号耗尽切后备应写入一条完整审计事件')
  const auditLog = auditLogs.items[0]
  assert.equal(auditLog?.groupId, fallbackGroup.id, '账号耗尽切后备成功后审计主记录必须归属实际命中的后备分组')
  const auditDetail = repositories.getAuditLogDetail(auditLog?.id ?? '')
  assert(auditDetail, '账号耗尽切后备审计详情应可读取')
  assert(auditDetail.attempts.some((attempt) => attempt.groupId === primaryGroup.id && attempt.success === false), '账号耗尽切后备审计应保留主号池失败尝试')
  assert(auditDetail.attempts.some((attempt) => attempt.groupId === fallbackGroup.id && attempt.success === true), '账号耗尽切后备审计应记录后备分组成功尝试')
  const metadataPayloads = await gatewayMetadataPayloads(auditLog?.id ?? '')
  assert(metadataPayloads.some((metadata) => metadata.label === 'api_key_group_route_fallback'
    && metadata.metadata?.reason === 'upstream_accounts_exhausted'
    && metadata.metadata?.fromGroupId === primaryGroup.id
    && metadata.metadata?.toGroupId === fallbackGroup.id), '审计 metadata 应记录真实上游失败耗尽当前分组后的跨分组后备切换')
  const clientIpAvoidanceSnapshot = clientIpAccountAvoidance.getClientIpAccountAvoidanceSnapshotForTest()
  assert(clientIpAvoidanceSnapshot.some((entry) => entry.accountId === primaryAccount.id
    && entry.apiKeyId === apiKey.id
    && entry.clientIp === clientIp), '账号耗尽切后备成功后应保留主号池失败账号的客户端 IP 级回避记录')
}

async function assertAllRouteStrategiesFallbackAfterUpstreamAccountsExhausted(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const cases: Array<{
    strategy: SimulatedGroupRouteStrategy
    suffix: string
    displayName: string
    weights?: [number, number, number]
  }> = [
    { strategy: 'priority_failover', suffix: 'priority', displayName: '主备优先' },
    { strategy: 'round_robin', suffix: 'round-robin', displayName: '轮询分配' },
    { strategy: 'weighted_round_robin', suffix: 'weighted', displayName: '权重分配', weights: [5, 1, 1] }
  ]

  for (const item of cases) {
    await assertRouteStrategyFallbackAfterUpstreamAccountsExhausted(gatewayBaseUrl, upstreamBaseUrl, item)
  }
}

async function assertRouteStrategyFallbackAfterUpstreamAccountsExhausted(
  gatewayBaseUrl: string,
  upstreamBaseUrl: string,
  item: {
    strategy: SimulatedGroupRouteStrategy
    suffix: string
    displayName: string
    weights?: [number, number, number]
  }
): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: `route_strategy_exhausted_${item.suffix.replace(/-/g, '_')}_owner`,
    displayName: `策略仿真${item.displayName}用户`,
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const primaryGroup = repositories.createGroup({
    name: `策略仿真 ${item.displayName} A 号池`,
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const fallbackGroup = repositories.createGroup({
    name: `策略仿真 ${item.displayName} B 号池`,
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const thirdGroup = repositories.createGroup({
    name: `策略仿真 ${item.displayName} C 号池`,
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const primaryUpstreamKey = `sk-route-strategy-${item.suffix}-primary-fail`
  const fallbackUpstreamKey = `sk-route-strategy-${item.suffix}-fallback-b`
  const thirdUpstreamKey = `sk-route-strategy-${item.suffix}-fallback-c`
  failingUpstreamKeys.add(primaryUpstreamKey)
  repositories.createAccount({
    providerCode: 'gpt',
    name: `策略仿真 ${item.displayName} A 账号`,
    type: 'api_key',
    credentials: {
      api_key: primaryUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: primaryGroup.id,
    status: 'active',
    schedulable: true,
  }, access)
  repositories.createAccount({
    providerCode: 'gpt',
    name: `策略仿真 ${item.displayName} B 账号`,
    type: 'api_key',
    credentials: {
      api_key: fallbackUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: fallbackGroup.id,
    status: 'active',
    schedulable: true,
  }, access)
  repositories.createAccount({
    providerCode: 'gpt',
    name: `策略仿真 ${item.displayName} C 账号`,
    type: 'api_key',
    credentials: {
      api_key: thirdUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: thirdGroup.id,
    status: 'active',
    schedulable: true,
  }, access)
  const weights = item.weights ?? [1, 1, 1]
  const apiKey = repositories.createApiKeyRecord({
    name: `策略仿真 ${item.displayName} API Key`,
    groupRouteStrategy: item.strategy,
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, weight: weights[0], status: 'active' },
      { groupId: fallbackGroup.id, priority: 2, weight: weights[1], status: 'active' },
      { groupId: thirdGroup.id, priority: 3, weight: weights[2], status: 'active' }
    ]
  }, access)
  gatewayCache.clearGatewayRuntimeCache()

  const beforeCount = upstreamRequests.length
  const traceId = `trace-route-strategy-${item.suffix}-exhausted-fallback`
  const response = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.5', traceId)
  assert.equal(response.status, 200, `${item.displayName} 下 A 号池真实失败耗尽后应切 B 并成功，实际 ${response.status}: ${response.text}`)
  const newRequests = upstreamRequests.slice(beforeCount)
  assert.deepEqual(
    newRequests.map((request) => request.accountKey),
    [primaryUpstreamKey, fallbackUpstreamKey],
    `${item.displayName} 下账号耗尽 fallback 应按本次候选顺序从 A 切到 B，不应跳到 C`
  )
  assert(!newRequests.some((request) => request.accountKey === thirdUpstreamKey), `${item.displayName} 下 B 可承接时不应继续尝试 C`)

  usageRecordQueue.flushAllUsageRecordQueue()
  auditLogQueue.flushAllAuditLogQueue()
  const usageRecords = usageRecordsByTraceId(traceId)
  assert(usageRecords.some((record) => record.groupId === primaryGroup.id && record.success === false), `${item.displayName} 下 A 失败尝试应归属 A 分组`)
  assert(usageRecords.some((record) => record.groupId === fallbackGroup.id && record.success === true), `${item.displayName} 下 B 成功尝试应归属 B 分组`)
  const auditLog = repositories.listAuditLogs({ traceId, pageSize: 10 }).items[0]
  assert.equal(auditLog?.groupId, fallbackGroup.id, `${item.displayName} 下最终审计主记录应归属实际命中的 B 分组`)
  const metadataPayloads = await gatewayMetadataPayloads(auditLog?.id ?? '')
  assert(metadataPayloads.some((metadata) => metadata.label === 'api_key_group_route_fallback'
    && metadata.metadata?.reason === 'upstream_accounts_exhausted'
    && metadata.metadata?.fromGroupId === primaryGroup.id
    && metadata.metadata?.toGroupId === fallbackGroup.id), `${item.displayName} 下审计 metadata 应记录 A 到 B 的账号耗尽切换`)
}

async function assertKeyRedistributionWrapsToRecoveredPrimaryAccount(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'route_wrap_recovered_owner',
    displayName: '回绕重分配恢复用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const primaryGroup = repositories.createGroup({
    name: '回绕重分配 A 号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const secondGroup = repositories.createGroup({
    name: '回绕重分配 B 号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const thirdGroup = repositories.createGroup({
    name: '回绕重分配 C 号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const primaryFailKey = 'sk-route-wrap-a-fail'
  const primaryRecoveredKey = 'sk-route-wrap-a-recovered'
  const secondFailKey = 'sk-route-wrap-b-fail'
  const thirdFailKey = 'sk-route-wrap-c-fail'
  failingUpstreamKeys.add(primaryFailKey)
  failingUpstreamKeys.add(secondFailKey)
  failingUpstreamKeys.add(thirdFailKey)
  releaseLocalSuppressionsBeforeRespondingKeys.add(thirdFailKey)
  repositories.createAccount({
    providerCode: 'gpt',
    name: '回绕重分配 A 失败账号',
    type: 'api_key',
    credentials: {
      api_key: primaryFailKey,
      base_url: upstreamBaseUrl
    },
    groupId: primaryGroup.id,
    status: 'active',
    schedulable: true,
  }, access)
  const recoveredAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '回绕重分配 A 恢复账号',
    type: 'api_key',
    credentials: {
      api_key: primaryRecoveredKey,
      base_url: upstreamBaseUrl
    },
    groupId: primaryGroup.id,
    status: 'active',
    schedulable: true,
  }, access)
  repositories.createAccount({
    providerCode: 'gpt',
    name: '回绕重分配 B 失败账号',
    type: 'api_key',
    credentials: {
      api_key: secondFailKey,
      base_url: upstreamBaseUrl
    },
    groupId: secondGroup.id,
    status: 'active',
    schedulable: true,
  }, access)
  repositories.createAccount({
    providerCode: 'gpt',
    name: '回绕重分配 C 失败账号',
    type: 'api_key',
    credentials: {
      api_key: thirdFailKey,
      base_url: upstreamBaseUrl
    },
    groupId: thirdGroup.id,
    status: 'active',
    schedulable: true,
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '回绕重分配 API Key',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: secondGroup.id, priority: 2, status: 'active' },
      { groupId: thirdGroup.id, priority: 3, status: 'active' }
    ]
  }, access)
  accountSideEffects.suppressGatewayAccountLocallyForTest(recoveredAccount.id, 60_000, '模拟 A 号池恢复前暂不可用')
  gatewayCache.clearGatewayRuntimeCache()

  const beforeCount = upstreamRequests.length
  const traceId = 'trace-route-wrap-recovered-primary'
  const response = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.5', traceId)
  assert.equal(response.status, 200, `A/B/C 都尝试失败后，如果 A 有恢复账号，应回到 A 继续承接，实际 ${response.status}: ${response.text}`)
  const newRequests = upstreamRequests.slice(beforeCount)
  assert.deepEqual(
    newRequests.map((request) => request.accountKey),
    [primaryFailKey, secondFailKey, thirdFailKey, primaryRecoveredKey],
    '回到 Key 层重新分配时，应跳过本请求已失败账号，并在 A 号池恢复账号可用后继续使用 A'
  )

  usageRecordQueue.flushAllUsageRecordQueue()
  auditLogQueue.flushAllAuditLogQueue()
  const usageRecords = usageRecordsByTraceId(traceId)
  assert(usageRecords.some((record) => record.groupId === primaryGroup.id && record.success === false), '回绕重分配应记录 A 初始失败尝试')
  assert(usageRecords.some((record) => record.groupId === secondGroup.id && record.success === false), '回绕重分配应记录 B 失败尝试')
  assert(usageRecords.some((record) => record.groupId === thirdGroup.id && record.success === false), '回绕重分配应记录 C 失败尝试')
  assert(usageRecords.some((record) => record.groupId === primaryGroup.id && record.success === true), '回绕重分配最终成功应归属恢复后的 A 分组')
  const auditLog = repositories.listAuditLogs({ traceId, pageSize: 10 }).items[0]
  assert.equal(auditLog?.groupId, primaryGroup.id, '回绕重分配成功后审计主记录应归属最终恢复承接的 A 分组')
  const metadataPayloads = await gatewayMetadataPayloads(auditLog?.id ?? '')
  assert(metadataPayloads.some((metadata) => metadata.label === 'api_key_group_route_fallback'
    && metadata.metadata?.reason === 'upstream_accounts_exhausted'
    && metadata.metadata?.fromGroupId === thirdGroup.id
    && metadata.metadata?.toGroupId === primaryGroup.id), '审计 metadata 应记录从 C 回到恢复后的 A 分组')
}

async function assertCapabilityFallback(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'route_capability_owner',
    displayName: '请求能力路由用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const primaryGroup = repositories.createGroup({
    name: '请求能力主号池 OAuth',
    providerCode: 'gpt',
    groupType: 'high_concurrency'
  }, access)
  const fallbackGroup = repositories.createGroup({
    name: '请求能力后备号池 API Key',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  repositories.createAccount({
    providerCode: 'gpt',
    name: '请求能力主号池 OAuth 账号',
    type: 'oauth',
    credentials: {
      access_token: 'oauth-route-capability-primary',
      refresh_token: 'refresh-route-capability-primary',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: primaryGroup.id,
    status: 'active',
    schedulable: true
  }, access)
  const fallbackUpstreamKey = 'sk-route-capability-fallback'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '请求能力后备 API Key 账号',
    type: 'api_key',
    credentials: {
      api_key: fallbackUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: fallbackGroup.id,
    status: 'active',
    schedulable: true,
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '请求能力路由 API Key',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: fallbackGroup.id, priority: 2, status: 'active' }
    ]
  }, access)
  gatewayCache.clearGatewayRuntimeCache()

  const runtime = await dbServiceHandlers.handleDbServiceOperation({ type: 'read_gateway_runtime', key: apiKey.key })
  assert.equal(runtime.apiKey?.selected_group_id, primaryGroup.id, '基础运行时应先选中有 OAuth 账号的主号池，回归才能覆盖请求级切换')

  const beforeCount = upstreamRequests.length
  const traceId = traceIdForBucket((bucket) => bucket < 1000, 'trace-route-capability-fallback')
  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  let apiKeyIdSelects = 0
  database.prepare = ((sql: string) => {
    if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+api_keys\b/i.test(sql) && /\bid\s*=\s*\?/i.test(sql)) {
      apiKeyIdSelects += 1
    }
    return originalPrepare(sql)
  }) as typeof database.prepare
  let response: Awaited<ReturnType<typeof requestChatCompletion>>
  try {
    response = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.4', traceId)
  } finally {
    database.prepare = originalPrepare
  }
  assert.equal(apiKeyIdSelects, 0, '请求级分组回退必须复用 runtime 中的 API Key 记录，不应在网关预检里按 apiKeyId 同步补查 api_keys')
  assert.equal(response.status, 200, `能力不匹配时应切后备并成功，实际 ${response.status}: ${response.text}`)
  const newRequests = upstreamRequests.slice(beforeCount)
  assert.equal(newRequests.length, 1, '能力不匹配切换后只应请求一次可承接后备上游')
  assert.equal(newRequests[0]?.accountKey, fallbackUpstreamKey, '公开 chat/completions 请求应命中 API Key 后备号池')
  assert.equal(newRequests[0]?.path, '/v1/chat/completions')

  usageRecordQueue.flushAllUsageRecordQueue()
  auditLogQueue.flushAllAuditLogQueue()
  const usageRecords = usageRecordsByTraceId(traceId)
  assert.equal(usageRecords.length, 1, '能力不匹配切后备成功后应写入一条使用记录')
  assert.equal(usageRecords[0]?.groupId, fallbackGroup.id, '能力不匹配切后备成功后使用记录必须归属实际命中的后备分组')
  assert.equal(usageRecords[0]?.accountId !== undefined, true, '能力不匹配切后备成功后使用记录应保留实际命中账号')
  markUsageRecordReadyForStats(usageRecords[0])
  assert.equal(usageStatsRepository.aggregateUsageStatsBatch(100), 1, '能力不匹配切后备成功后的使用记录应进入后台统计聚合')
  assert.equal(usageStatsRequestCount(owner.id, 'group', fallbackGroup.id), 1, '能力不匹配切后备成功后分组统计必须归属实际命中的后备分组')
  assert.equal(usageStatsRequestCount(owner.id, 'group', primaryGroup.id) ?? 0, 0, '能力不匹配切后备成功后主分组统计不应增加')
  assert.equal(usageStatsRequestCount(owner.id, 'api_key', apiKey.id), 1, '能力不匹配切后备成功后 API Key 统计仍应归属同一个 Key')
  const auditLogs = repositories.listAuditLogs({ traceId, pageSize: 10 })
  assert.equal(auditLogs.total, 1, '命中成功采样的 fallback 请求应写入一条审计事件')
  const auditLog = auditLogs.items[0]
  assert.equal(auditLog?.groupId, fallbackGroup.id, '能力不匹配切后备成功后审计主记录必须归属实际命中的后备分组')
  const auditDetail = repositories.getAuditLogDetail(auditLog?.id ?? '')
  assert(auditDetail, '能力不匹配切后备成功后审计详情应可读取')
  assert.equal(auditDetail.attempts.length, 1, '能力不匹配切后备成功后只应记录一次后备上游尝试')
  assert.equal(auditDetail.attempts[0]?.groupId, fallbackGroup.id, '能力不匹配切后备成功后审计 attempt 必须归属实际命中的后备分组')
  const metadataPayloads = await gatewayMetadataPayloads(auditLog?.id ?? '')
  assert(metadataPayloads.some((metadata) => metadata.label === 'api_key_group_route_fallback'
    && metadata.metadata?.reason === 'request_capability_mismatch'
    && metadata.metadata?.fromGroupId === primaryGroup.id
    && metadata.metadata?.toGroupId === fallbackGroup.id), '能力不匹配切后备成功后审计 metadata 应记录原分组、后备分组和切换原因')
}

async function assertCapabilityThenBusyMultiHopFallback(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'route_multi_hop_owner',
    displayName: '多跳分组路由用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const primaryGroup = repositories.createGroup({
    name: '多跳能力不匹配主号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const busyGroup = repositories.createGroup({
    name: '多跳繁忙中间号池',
    providerCode: 'gpt',
    groupType: 'high_concurrency',
    schedulingPolicy: {
      maxQueueWaitMs: 5
    }
  }, access)
  const finalGroup = repositories.createGroup({
    name: '多跳最终可承接号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  repositories.createAccount({
    providerCode: 'gpt',
    name: '多跳主号池 OAuth 账号',
    type: 'oauth',
    credentials: {
      access_token: 'oauth-route-multi-hop-primary',
      refresh_token: 'refresh-route-multi-hop-primary',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: primaryGroup.id,
    status: 'active',
    schedulable: true
  }, access)
  const busyUpstreamKey = 'sk-route-multi-hop-busy'
  const busyAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '多跳中间繁忙账号',
    type: 'api_key',
    credentials: {
      api_key: busyUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: busyGroup.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 1,
  }, access)
  const finalUpstreamKey = 'sk-route-multi-hop-final'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '多跳最终可承接账号',
    type: 'api_key',
    credentials: {
      api_key: finalUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: finalGroup.id,
    status: 'active',
    schedulable: true,
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '多跳分组路由 API Key',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: busyGroup.id, priority: 2, status: 'active' },
      { groupId: finalGroup.id, priority: 3, status: 'active' }
    ]
  }, access)
  gatewayCache.clearGatewayRuntimeCache()

  const runtime = await dbServiceHandlers.handleDbServiceOperation({ type: 'read_gateway_runtime', key: apiKey.key })
  assert.equal(runtime.apiKey?.selected_group_id, primaryGroup.id, '基础运行时应先选中能力不匹配主号池，回归才能覆盖多跳切换')

  const heldSlot = tryAcquireAccountConcurrency(busyAccount.id, 1)
  assert.equal(heldSlot.acquired, true, '多跳回归前应占用中间号池账号并发')
  try {
    const beforeCount = upstreamRequests.length
    const traceId = traceIdForBucket((bucket) => bucket < 1000, 'trace-route-multi-hop-fallback')
    const response = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.5', traceId)
    assert.equal(response.status, 200, `主号池能力不匹配且中间号池繁忙时应继续切到第三号池，实际 ${response.status}: ${response.text}`)
    const newRequests = upstreamRequests.slice(beforeCount)
    assert.equal(newRequests.length, 1, '多跳切换后只应请求一次最终可承接上游')
    assert.equal(newRequests[0]?.accountKey, finalUpstreamKey, '多跳切换应命中第三号池账号')
    assert(!newRequests.some((request) => request.accountKey === busyUpstreamKey), '中间繁忙号池账号不应进入真实上游派发')

    usageRecordQueue.flushAllUsageRecordQueue()
    auditLogQueue.flushAllAuditLogQueue()
    const usageRecords = usageRecordsByTraceId(traceId)
    assert.equal(usageRecords.length, 1, '多跳切换成功后应写入一条使用记录')
    assert.equal(usageRecords[0]?.groupId, finalGroup.id, '多跳切换成功后使用记录必须归属最终命中分组')
    const auditLog = repositories.listAuditLogs({ traceId, pageSize: 10 }).items[0]
    assert.equal(auditLog?.groupId, finalGroup.id, '多跳切换成功后审计主记录必须归属最终命中分组')
    const metadataPayloads = await gatewayMetadataPayloads(auditLog?.id ?? '')
    assert(metadataPayloads.some((metadata) => metadata.label === 'api_key_group_route_fallback'
      && metadata.metadata?.reason === 'request_capability_mismatch'
      && metadata.metadata?.fromGroupId === primaryGroup.id
      && metadata.metadata?.toGroupId === busyGroup.id), '多跳切换应记录从主号池进入中间号池的原因')
    assert(metadataPayloads.some((metadata) => metadata.label === 'api_key_group_route_fallback'
      && metadata.metadata?.reason === 'high_concurrency_group_busy'
      && metadata.metadata?.fromGroupId === busyGroup.id
      && metadata.metadata?.toGroupId === finalGroup.id), '多跳切换应记录中间号池繁忙后继续进入最终号池')
  } finally {
    heldSlot.release()
  }
}

async function assertModelFallback(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'route_model_owner',
    displayName: '模型路由用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const primaryGroup = repositories.createGroup({
    name: '模型主号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const fallbackGroup = repositories.createGroup({
    name: '模型后备号池',
    providerCode: 'gpt',
    groupType: 'high_concurrency'
  }, access)
  const primaryUpstreamKey = 'sk-route-model-primary'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '模型主号池账号',
    type: 'api_key',
    credentials: {
      api_key: primaryUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: primaryGroup.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.4']
  }, access)
  const fallbackUpstreamKey = 'sk-route-model-fallback'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '模型后备号池账号',
    type: 'api_key',
    credentials: {
      api_key: fallbackUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: fallbackGroup.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5']
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '模型路由 API Key',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: fallbackGroup.id, priority: 2, status: 'active' }
    ]
  }, access)
  gatewayCache.clearGatewayRuntimeCache()

  const runtime = await dbServiceHandlers.handleDbServiceOperation({ type: 'read_gateway_runtime', key: apiKey.key })
  assert.equal(runtime.apiKey?.selected_group_id, primaryGroup.id, '基础运行时应先选中模型不匹配的主号池，回归才能覆盖请求级切换')

  const beforeCount = upstreamRequests.length
  const response = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.5')
  assert.equal(response.status, 200, `模型不匹配时应切后备并成功，实际 ${response.status}: ${response.text}`)
  const newRequests = upstreamRequests.slice(beforeCount)
  assert.equal(newRequests.length, 1, '模型不匹配切换后只应请求一次可承接后备上游')
  assert.equal(newRequests[0]?.accountKey, fallbackUpstreamKey, '请求模型应命中支持该模型的后备号池')
  assert.equal(newRequests[0]?.model, 'gpt-5.5')
  assert(!newRequests.some((request) => request.accountKey === primaryUpstreamKey), '模型不匹配的主号池账号不应被派发')
}

async function assertModelSpecificAccountPreferred(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'route_model_specific_account_owner',
    displayName: '模型显式账号优先用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const group = repositories.createGroup({
    name: '模型显式账号优先号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const unrestrictedUpstreamKey = 'sk-route-model-specific-unrestricted'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '模型显式账号优先-无限制高优先账号',
    type: 'api_key',
    credentials: {
      api_key: unrestrictedUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0,
    superPriorityEnabled: true
  }, access)
  const directUpstreamKey = 'sk-route-model-specific-direct'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '模型显式账号优先-显式支持账号',
    type: 'api_key',
    credentials: {
      api_key: directUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 100,
    supportedModels: ['gpt-5.5']
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '模型显式账号优先 API Key',
    groupBindings: [
      { groupId: group.id, priority: 1, status: 'active' }
    ]
  }, access)
  gatewayCache.clearGatewayRuntimeCache()

  const beforeCount = upstreamRequests.length
  const response = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.5', 'trace-route-model-specific-preferred')
  assert.equal(response.status, 200, `显式支持模型账号应优先承接，实际 ${response.status}: ${response.text}`)
  const preferredRequests = upstreamRequests.slice(beforeCount)
  assert.equal(preferredRequests.length, 1, '显式支持模型账号正常时不应先打无限制账号')
  assert.equal(preferredRequests[0]?.accountKey, directUpstreamKey, '请求模型应优先命中显式支持该模型的账号')
  assert(!preferredRequests.some((request) => request.accountKey === unrestrictedUpstreamKey), '无限制账号优先级更高也不应抢在显式支持模型账号前面')

  try {
    failingUpstreamKeys.add(directUpstreamKey)
    const beforeFailoverCount = upstreamRequests.length
    const failoverResponse = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.5', 'trace-route-model-specific-failover')
    assert.equal(failoverResponse.status, 200, `显式支持模型账号失败后应继续切到无限制账号，实际 ${failoverResponse.status}: ${failoverResponse.text}`)
    const failoverRequests = upstreamRequests.slice(beforeFailoverCount)
    assert.deepEqual(
      failoverRequests.map((request) => request.accountKey),
      [directUpstreamKey, unrestrictedUpstreamKey],
      '显式支持模型账号真实上游失败后，应在同组继续尝试无限制账号，不能影响客户端可用性'
    )
  } finally {
    failingUpstreamKeys.delete(directUpstreamKey)
  }
}

async function assertHighConcurrencyModelSpecificAccountPreferred(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'route_hc_model_specific_account_owner',
    displayName: '高并发模型显式账号优先用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const group = repositories.createGroup({
    name: '高并发模型显式账号优先号池',
    providerCode: 'gpt',
    groupType: 'high_concurrency'
  }, access)
  const unrestrictedUpstreamKey = 'sk-route-hc-model-specific-unrestricted'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '高并发模型显式账号优先-无限制高优先账号',
    type: 'api_key',
    credentials: {
      api_key: unrestrictedUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0,
    superPriorityEnabled: true
  }, access)
  const directUpstreamKey = 'sk-route-hc-model-specific-direct'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '高并发模型显式账号优先-显式支持账号',
    type: 'api_key',
    credentials: {
      api_key: directUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 100,
    supportedModels: ['gpt-5.5']
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '高并发模型显式账号优先 API Key',
    groupBindings: [
      { groupId: group.id, priority: 1, status: 'active' }
    ]
  }, access)
  gatewayCache.clearGatewayRuntimeCache()

  const beforeCount = upstreamRequests.length
  const response = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.5', 'trace-route-hc-model-specific-preferred')
  assert.equal(response.status, 200, `高并发号池显式支持模型账号应优先承接，实际 ${response.status}: ${response.text}`)
  const preferredRequests = upstreamRequests.slice(beforeCount)
  assert.equal(preferredRequests.length, 1, '高并发号池显式支持模型账号正常时不应先打无限制账号')
  assert.equal(preferredRequests[0]?.accountKey, directUpstreamKey, '高并发号池请求模型应优先命中显式支持该模型的账号')
  assert(!preferredRequests.some((request) => request.accountKey === unrestrictedUpstreamKey), '高并发号池无限制账号超级优先也不应抢在显式支持模型账号前面')

  try {
    failingUpstreamKeys.add(directUpstreamKey)
    const beforeFailoverCount = upstreamRequests.length
    const failoverResponse = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.5', 'trace-route-hc-model-specific-failover')
    assert.equal(failoverResponse.status, 200, `高并发号池显式支持模型账号失败后应继续切到无限制账号，实际 ${failoverResponse.status}: ${failoverResponse.text}`)
    const failoverRequests = upstreamRequests.slice(beforeFailoverCount)
    assert.deepEqual(
      failoverRequests.map((request) => request.accountKey),
      [directUpstreamKey, unrestrictedUpstreamKey],
      '高并发号池显式支持模型账号真实上游失败后，应继续尝试无限制账号，不能影响客户端可用性'
    )
  } finally {
    failingUpstreamKeys.delete(directUpstreamKey)
  }
}

async function assertPersonalQualityTieBreakWithMockAI(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'route_personal_quality_tie_owner',
    displayName: '普通分组质量分同级选择用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const group = repositories.createGroup({
    name: '普通分组质量分同级选择号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const slowerUpstreamKey = 'sk-route-personal-quality-slower'
  const slowerAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '普通分组质量分同级选择-慢账号',
    type: 'api_key',
    credentials: {
      api_key: slowerUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0,
    supportedModels: ['gpt-5.5']
  }, access)
  const fasterUpstreamKey = 'sk-route-personal-quality-faster'
  const fasterAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '普通分组质量分同级选择-快账号',
    type: 'api_key',
    credentials: {
      api_key: fasterUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0,
    supportedModels: ['gpt-5.5']
  }, access)
  seedQualityScore(owner.id, slowerAccount.id, 500)
  seedQualityScore(owner.id, fasterAccount.id, 100)
  const apiKey = repositories.createApiKeyRecord({
    name: '普通分组质量分同级选择 API Key',
    groupBindings: [
      { groupId: group.id, priority: 1, status: 'active' }
    ]
  }, access)
  gatewayCache.clearGatewayRuntimeCache()

  const beforeCount = upstreamRequests.length
  const response = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.5', 'trace-route-personal-quality-tie')
  assert.equal(response.status, 200, `普通分组同级多账号应按历史质量分选择，实际 ${response.status}: ${response.text}`)
  const requests = upstreamRequests.slice(beforeCount)
  assert.equal(requests.length, 1, '普通分组质量分同级选择应只派发一次上游')
  assert.equal(requests[0]?.accountKey, fasterUpstreamKey, '普通分组同级 direct 命中账号应优先命中质量分更低的账号')
  assert(!requests.some((request) => request.accountKey === slowerUpstreamKey), '普通分组质量分较差账号不应抢在同级更快账号前面')
}

async function assertHighConcurrencyQualityTieBreakWithMockAI(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'route_hc_quality_tie_owner',
    displayName: '高并发质量分同级选择用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const group = repositories.createGroup({
    name: '高并发质量分同级选择号池',
    providerCode: 'gpt',
    groupType: 'high_concurrency'
  }, access)
  const slowerUpstreamKey = 'sk-route-hc-quality-slower'
  const slowerAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '高并发质量分同级选择-慢账号',
    type: 'api_key',
    credentials: {
      api_key: slowerUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0,
    supportedModels: ['gpt-5.5']
  }, access)
  const fasterUpstreamKey = 'sk-route-hc-quality-faster'
  const fasterAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '高并发质量分同级选择-快账号',
    type: 'api_key',
    credentials: {
      api_key: fasterUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0,
    supportedModels: ['gpt-5.5']
  }, access)
  seedQualityScore(owner.id, slowerAccount.id, 500)
  seedQualityScore(owner.id, fasterAccount.id, 100)
  const apiKey = repositories.createApiKeyRecord({
    name: '高并发质量分同级选择 API Key',
    groupBindings: [
      { groupId: group.id, priority: 1, status: 'active' }
    ]
  }, access)
  gatewayCache.clearGatewayRuntimeCache()

  const beforeCount = upstreamRequests.length
  const response = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.5', 'trace-route-hc-quality-tie')
  assert.equal(response.status, 200, `高并发分组同级多账号应按历史质量分选择，实际 ${response.status}: ${response.text}`)
  const requests = upstreamRequests.slice(beforeCount)
  assert.equal(requests.length, 1, '高并发质量分同级选择应只派发一次上游')
  assert.equal(requests[0]?.accountKey, fasterUpstreamKey, '高并发同级 direct 命中账号应优先命中质量分更低的账号')
  assert(!requests.some((request) => request.accountKey === slowerUpstreamKey), '高并发质量分较差账号不应抢在同级更快账号前面')
}

async function assertNormalApiKeyCrossProviderModelRoute(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'route_normal_cross_provider_owner',
    displayName: '普通Key跨供应商模型路由用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const gptGroup = repositories.createGroup({
    name: '普通 Key 跨供应商 GPT 号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const deepSeekGroup = repositories.createGroup({
    name: '普通 Key 跨供应商 DeepSeek 号池',
    providerCode: 'deepseek',
    providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
    groupType: 'personal'
  }, access)
  const gptUpstreamKey = 'sk-route-normal-cross-provider-gpt'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '普通 Key 跨供应商 GPT 账号',
    type: 'api_key',
    credentials: {
      api_key: gptUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: gptGroup.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5']
  }, access)
  const deepSeekUpstreamKey = 'sk-route-normal-cross-provider-deepseek'
  repositories.createAccount({
    providerCode: 'deepseek',
    providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
    name: '普通 Key 跨供应商 DeepSeek 账号',
    type: 'api_key',
    credentials: {
      api_key: deepSeekUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: deepSeekGroup.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['deepseek-ai-v4-flash']
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '普通 Key 跨供应商模型路由 API Key',
    routeMode: 'normal',
    groupBindings: [
      { groupId: gptGroup.id, priority: 1, status: 'active' },
      { groupId: deepSeekGroup.id, priority: 2, status: 'active' }
    ],
    explicitHybridRouteRules: [
      {
        id: 'route_normal_cross_provider_gpt_to_deepseek',
        enabled: true,
        priority: 1,
        sourceClientProfile: 'auto',
        sourceEndpointFamily: 'chat_completions',
        sourceModel: 'gpt-5.5',
        targetGroupId: deepSeekGroup.id,
        upstreamEndpointFamily: 'chat_completions',
        upstreamModel: 'deepseek-ai-v4-flash',
        adapterMode: 'bridge'
      }
    ]
  }, access)
  gatewayCache.clearGatewayRuntimeCache()

  const runtime = await dbServiceHandlers.handleDbServiceOperation({ type: 'read_gateway_runtime', key: apiKey.key })
  assert.equal(runtime.apiKey?.selected_group_id, gptGroup.id, '基础运行时应按优先级先选 GPT 号池，回归才能覆盖请求模型切到 DeepSeek')

  const beforeCount = upstreamRequests.length
  const traceId = traceIdForBucket((bucket) => bucket < 1000, 'trace-route-normal-cross-provider-model')
  const response = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.5', traceId)
  assert.equal(response.status, 200, `普通 Key 跨供应商模型路由应切 DeepSeek 并成功，实际 ${response.status}: ${response.text}`)
  const newRequests = upstreamRequests.slice(beforeCount)
  assert.equal(newRequests.length, 1, '普通 Key 跨供应商模型路由切换后只应请求一次目标供应商上游')
  assert.equal(newRequests[0]?.accountKey, deepSeekUpstreamKey, 'DeepSeek 模型请求应命中 DeepSeek 号池账号')
  assert.equal(newRequests[0]?.model, 'deepseek-ai-v4-flash', 'DeepSeek 跨供应商路由应应用 API Key 显式混合路由后再打上游')
  assert(!newRequests.some((request) => request.accountKey === gptUpstreamKey), 'DeepSeek 模型请求不应派发到 GPT 号池账号')

  usageRecordQueue.flushAllUsageRecordQueue()
  auditLogQueue.flushAllAuditLogQueue()
  const usageRecords = usageRecordsByTraceId(traceId)
  assert.equal(usageRecords.length, 1, '普通 Key 跨供应商模型路由成功后应写入一条使用记录')
  assert.equal(usageRecords[0]?.groupId, deepSeekGroup.id, '普通 Key 跨供应商模型路由使用记录必须归属 DeepSeek 分组')
  const auditLogs = repositories.listAuditLogs({ traceId, pageSize: 10 })
  assert.equal(auditLogs.total, 1, '普通 Key 跨供应商模型路由应写入一条审计事件')
  const auditLog = auditLogs.items[0]
  assert.equal(auditLog?.groupId, deepSeekGroup.id, '普通 Key 跨供应商模型路由审计主记录必须归属 DeepSeek 分组')
  const metadataPayloads = await gatewayMetadataPayloads(auditLog?.id ?? '')
  assert(metadataPayloads.some((metadata) => metadata.label === 'explicit_hybrid_route'
    && metadata.metadata?.requestedModel === 'gpt-5.5'
    && metadata.metadata?.fromGroupId === gptGroup.id
    && metadata.metadata?.toGroupId === deepSeekGroup.id
    && metadata.metadata?.upstreamModel === 'deepseek-ai-v4-flash'
    && metadata.metadata?.upstreamEndpointFamily === 'chat_completions'), '审计 metadata 应记录 API Key 显式混合路由从 GPT 分组切到 DeepSeek 分组')
}

async function assertNormalApiKeyUnknownModelReturnsLocalError(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'route_normal_unknown_model_owner',
    displayName: '普通Key未知模型用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const gptGroup = repositories.createGroup({
    name: '普通 Key 未知模型 GPT 号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const deepSeekGroup = repositories.createGroup({
    name: '普通 Key 未知模型 DeepSeek 号池',
    providerCode: 'deepseek',
    providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
    groupType: 'personal'
  }, access)
  repositories.createAccount({
    providerCode: 'gpt',
    name: '普通 Key 未知模型 GPT 未限制账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-route-normal-unknown-model-gpt',
      base_url: upstreamBaseUrl
    },
    groupId: gptGroup.id,
    status: 'active',
    schedulable: true
  }, access)
  repositories.createAccount({
    providerCode: 'deepseek',
    providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
    name: '普通 Key 未知模型 DeepSeek 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-route-normal-unknown-model-deepseek',
      base_url: upstreamBaseUrl
    },
    groupId: deepSeekGroup.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['deepseek-v4-flash']
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '普通 Key 未知模型 API Key',
    routeMode: 'normal',
    groupBindings: [
      { groupId: gptGroup.id, priority: 1, status: 'active' },
      { groupId: deepSeekGroup.id, priority: 2, status: 'active' }
    ]
  }, access)
  gatewayCache.clearGatewayRuntimeCache()

  const beforeCount = upstreamRequests.length
  const traceId = traceIdForBucket((bucket) => bucket < 1000, 'trace-route-normal-unknown-model')
  const response = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'unknown-route-model-for-regression', traceId)
  assert.equal(response.status, 400, `普通 Key 未知模型应返回本地 400，实际 ${response.status}: ${response.text}`)
  const payload = parseJsonObject(response.text)
  assert.equal(payload.error && typeof payload.error === 'object' && !Array.isArray(payload.error)
    ? (payload.error as Record<string, unknown>).code
    : undefined, 'model_not_routable_for_api_key', '未知模型应返回模型不可路由错误码')
  assert.equal(upstreamRequests.length, beforeCount, '普通 Key 未知模型不应命中任何上游账号，即使首选供应商账号未配置 supportedModels')

  usageRecordQueue.flushAllUsageRecordQueue()
  auditLogQueue.flushAllAuditLogQueue()
  const usageRecords = usageRecordsByTraceId(traceId)
  assert.equal(usageRecords.length, 1, '普通 Key 未知模型本地拒绝也应写入一条失败使用记录')
  assert.equal(usageRecords[0]?.success, false, '普通 Key 未知模型使用记录应标记失败')
  assert.equal(usageRecords[0]?.groupId, gptGroup.id, '普通 Key 未知模型失败记录保留认证阶段初始分组归属')
  const auditLogs = repositories.listAuditLogs({ traceId, pageSize: 10 })
  assert.equal(auditLogs.total, 1, '普通 Key 未知模型本地拒绝应写入审计事件')
  const metadataPayloads = await gatewayMetadataPayloads(auditLogs.items[0]?.id ?? '')
  assert(metadataPayloads.some((metadata) => metadata.label === 'normal_model_route_failed'
    && metadata.metadata?.requestedModel === 'unknown-route-model-for-regression'
    && metadata.metadata?.reason === 'model_not_routable_for_api_key'), '审计 metadata 应记录普通 Key 未知模型被本地模型路由拒绝')
}

async function assertHighConcurrencyBusyFallback(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'route_busy_owner',
    displayName: '高并发繁忙路由用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const primaryGroup = repositories.createGroup({
    name: '高并发繁忙主号池',
    providerCode: 'gpt',
    groupType: 'high_concurrency',
    schedulingPolicy: {
      maxQueueWaitMs: 5
    }
  }, access)
  const fallbackGroup = repositories.createGroup({
    name: '高并发繁忙后备号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const primaryUpstreamKey = 'sk-route-busy-primary'
  const primaryAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '高并发繁忙主号池账号',
    type: 'api_key',
    credentials: {
      api_key: primaryUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: primaryGroup.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 1,
  }, access)
  const fallbackUpstreamKey = 'sk-route-busy-fallback'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '高并发繁忙后备账号',
    type: 'api_key',
    credentials: {
      api_key: fallbackUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: fallbackGroup.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 1,
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '高并发繁忙路由 API Key',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: fallbackGroup.id, priority: 2, status: 'active' }
    ]
  }, access)
  gatewayCache.clearGatewayRuntimeCache()

  const runtime = await dbServiceHandlers.handleDbServiceOperation({ type: 'read_gateway_runtime', key: apiKey.key })
  assert.equal(runtime.apiKey?.selected_group_id, primaryGroup.id, '基础运行时应先选中有账号的高并发主号池')

  const heldSlot = tryAcquireAccountConcurrency(primaryAccount.id, 1)
  assert.equal(heldSlot.acquired, true, '高并发繁忙回归前应占用主号池账号并发')
  try {
    const beforeCount = upstreamRequests.length
    const response = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.5')
    assert.equal(response.status, 200, `主高并发号池全忙时应先切后备并成功，实际 ${response.status}: ${response.text}`)
    const newRequests = upstreamRequests.slice(beforeCount)
    assert.equal(newRequests.length, 1, '高并发全忙切换后只应请求一次可承接后备上游')
    assert.equal(newRequests[0]?.accountKey, fallbackUpstreamKey, '高并发全忙时应命中后备号池账号')
    assert(!newRequests.some((request) => request.accountKey === primaryUpstreamKey), '全忙主号池账号不应进入真实上游派发')
  } finally {
    heldSlot.release()
  }
}

async function assertPersonalConcurrencyBusyFallback(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'route_personal_busy_owner',
    displayName: '个人分组繁忙路由用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const primaryGroup = repositories.createGroup({
    name: '个人繁忙主号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const fallbackGroup = repositories.createGroup({
    name: '个人繁忙后备高并发号池',
    providerCode: 'gpt',
    groupType: 'high_concurrency',
    schedulingPolicy: {
      maxQueueWaitMs: 5
    }
  }, access)
  const primaryUpstreamKey = 'sk-route-personal-busy-primary'
  const primaryAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '个人繁忙主号池账号',
    type: 'api_key',
    credentials: {
      api_key: primaryUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: primaryGroup.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 1,
  }, access)
  const fallbackUpstreamKey = 'sk-route-personal-busy-fallback'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '个人繁忙后备高并发账号',
    type: 'api_key',
    credentials: {
      api_key: fallbackUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: fallbackGroup.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 1,
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '个人繁忙路由 API Key',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: fallbackGroup.id, priority: 2, status: 'active' }
    ]
  }, access)
  gatewayCache.clearGatewayRuntimeCache()

  const runtime = await dbServiceHandlers.handleDbServiceOperation({ type: 'read_gateway_runtime', key: apiKey.key })
  assert.equal(runtime.apiKey?.selected_group_id, primaryGroup.id, '基础运行时应先选中有账号的个人主号池')

  const heldSlot = tryAcquireAccountConcurrency(primaryAccount.id, 1)
  assert.equal(heldSlot.acquired, true, '个人繁忙回归前应占用主号池账号并发')
  try {
    const beforeCount = upstreamRequests.length
    const traceId = traceIdForBucket((bucket) => bucket < 1000, 'trace-route-personal-busy-fallback')
    const response = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.5', traceId)
    assert.equal(response.status, 200, `个人主号池账号硬并发满时应先切后备并成功，实际 ${response.status}: ${response.text}`)
    const newRequests = upstreamRequests.slice(beforeCount)
    assert.equal(newRequests.length, 1, '个人主号池硬满切换后只应请求一次可承接后备上游')
    assert.equal(newRequests[0]?.accountKey, fallbackUpstreamKey, '个人主号池硬满时应命中后备高并发号池账号')
    assert(!newRequests.some((request) => request.accountKey === primaryUpstreamKey), '个人主号池硬满账号不应进入真实上游派发')

    usageRecordQueue.flushAllUsageRecordQueue()
    auditLogQueue.flushAllAuditLogQueue()
    const usageRecords = usageRecordsByTraceId(traceId)
    assert.equal(usageRecords.length, 1, '个人主号池硬满切后备成功后应写入一条使用记录')
    assert.equal(usageRecords[0]?.groupId, fallbackGroup.id, '个人主号池硬满切后备成功后使用记录必须归属实际命中的后备分组')
    const auditLog = repositories.listAuditLogs({ traceId, pageSize: 10 }).items[0]
    assert.equal(auditLog?.groupId, fallbackGroup.id, '个人主号池硬满切后备成功后审计主记录必须归属实际命中的后备分组')
    const metadataPayloads = await gatewayMetadataPayloads(auditLog?.id ?? '')
    assert(metadataPayloads.some((metadata) => metadata.label === 'api_key_group_route_fallback'
      && metadata.metadata?.reason === 'group_capacity_busy'
      && metadata.metadata?.fromGroupId === primaryGroup.id
      && metadata.metadata?.toGroupId === fallbackGroup.id), '个人主号池硬满切后备成功后审计 metadata 应记录原分组、后备分组和切换原因')
  } finally {
    heldSlot.release()
  }
}

async function assertLocalSuppressionFallback(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'route_local_suppression_owner',
    displayName: '本地屏蔽路由用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const access = { systemAccountId: owner.id, role: 'user' as const }
  const primaryGroup = repositories.createGroup({
    name: '本地屏蔽主号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const fallbackGroup = repositories.createGroup({
    name: '本地屏蔽后备号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, access)
  const primaryUpstreamKey = 'sk-route-local-suppression-primary'
  const primaryAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '本地屏蔽主号池账号',
    type: 'api_key',
    credentials: {
      api_key: primaryUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: primaryGroup.id,
    status: 'active',
    schedulable: true,
  }, access)
  const fallbackUpstreamKey = 'sk-route-local-suppression-fallback'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '本地屏蔽后备账号',
    type: 'api_key',
    credentials: {
      api_key: fallbackUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: fallbackGroup.id,
    status: 'active',
    schedulable: true,
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '本地屏蔽路由 API Key',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: fallbackGroup.id, priority: 2, status: 'active' }
    ]
  }, access)
  gatewayCache.clearGatewayRuntimeCache()

  const runtime = await dbServiceHandlers.handleDbServiceOperation({ type: 'read_gateway_runtime', key: apiKey.key })
  assert.equal(runtime.apiKey?.selected_group_id, primaryGroup.id, '基础运行时应先选中本地屏蔽主号池')

  accountSideEffects.suppressGatewayAccountLocallyForTest(primaryAccount.id, 60_000, '本地屏蔽切后备回归')
  try {
    const beforeCount = upstreamRequests.length
    const response = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.5')
    assert.equal(response.status, 200, `主号池本地短期屏蔽时应切后备并成功，实际 ${response.status}: ${response.text}`)
    const newRequests = upstreamRequests.slice(beforeCount)
    assert.equal(newRequests.length, 1, '本地屏蔽切换后只应请求一次可承接后备上游')
    assert.equal(newRequests[0]?.accountKey, fallbackUpstreamKey, '本地屏蔽时应命中后备号池账号')
    assert(!newRequests.some((request) => request.accountKey === primaryUpstreamKey), '本地屏蔽主号池账号不应进入真实上游派发')
  } finally {
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  }
}

async function assertAuthorizationQuotaFallback(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'route_authorization_quota_owner',
    displayName: '授权额度路由资源方',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'route_authorization_quota_grantee',
    displayName: '授权额度路由使用方',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const ownerSourceGroup = repositories.createGroup({
    name: '授权额度来源号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, ownerAccess)
  const primaryGroup = repositories.createGroup({
    name: '授权额度主号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, granteeAccess)
  const fallbackGroup = repositories.createGroup({
    name: '授权额度后备号池',
    providerCode: 'gpt',
    groupType: 'personal'
  }, granteeAccess)
  const primaryUpstreamKey = 'sk-route-authorization-quota-primary'
  const ownerAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '授权额度主号池授权账号',
    type: 'api_key',
    credentials: {
      api_key: primaryUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: ownerSourceGroup.id,
    status: 'active',
    schedulable: true,
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: ownerAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: primaryGroup.id,
    remark: '授权额度耗尽切后备回归',
    limits: {
      total: { enabled: true, limit: 1 }
    }
  }, ownerAccess)
  const runtimeAuthorization = databaseModule.getBusinessDatabase()
    .prepare("SELECT id FROM resource_authorizations WHERE resource_type = 'account' AND resource_id = ? AND grantee_system_account_id = ? LIMIT 1")
    .get(ownerAccount.id, grantee.id) as unknown as { id?: string } | undefined
  assert(runtimeAuthorization?.id, '授权额度切后备回归需要运行时授权记录')
  insertUsageTotal(databaseModule.getStatsDatabase(), grantee.id, 'account_authorization', runtimeAuthorization.id, 5)
  authorizationQuotaService.clearAuthorizationQuotaCache()

  const fallbackUpstreamKey = 'sk-route-authorization-quota-fallback'
  repositories.createAccount({
    providerCode: 'gpt',
    name: '授权额度后备自有账号',
    type: 'api_key',
    credentials: {
      api_key: fallbackUpstreamKey,
      base_url: upstreamBaseUrl
    },
    groupId: fallbackGroup.id,
    status: 'active',
    schedulable: true,
  }, granteeAccess)
  const apiKey = repositories.createApiKeyRecord({
    name: '授权额度路由 API Key',
    groupBindings: [
      { groupId: primaryGroup.id, priority: 1, status: 'active' },
      { groupId: fallbackGroup.id, priority: 2, status: 'active' }
    ]
  }, granteeAccess)
  gatewayCache.clearGatewayRuntimeCache()

  const runtime = await dbServiceHandlers.handleDbServiceOperation({ type: 'read_gateway_runtime', key: apiKey.key })
  assert.equal(runtime.apiKey?.selected_group_id, primaryGroup.id, '基础运行时应先选中只有授权账号的主号池，回归才能覆盖授权额度切换')

  const beforeCount = upstreamRequests.length
  const traceId = traceIdForBucket((bucket) => bucket < 1000, 'trace-route-authorization-quota-fallback')
  const response = await requestChatCompletion(gatewayBaseUrl, apiKey.key, 'gpt-5.5', traceId)
  assert.equal(response.status, 200, `授权额度耗尽时应切后备并成功，实际 ${response.status}: ${response.text}`)
  const newRequests = upstreamRequests.slice(beforeCount)
  assert.equal(newRequests.length, 1, '授权额度耗尽切换后只应请求一次可承接后备上游')
  assert.equal(newRequests[0]?.accountKey, fallbackUpstreamKey, '授权额度耗尽时应命中后备自有号池')
  assert(!newRequests.some((request) => request.accountKey === primaryUpstreamKey), '授权额度耗尽的主号池授权账号不应进入真实上游派发')

  usageRecordQueue.flushAllUsageRecordQueue()
  auditLogQueue.flushAllAuditLogQueue()
  const usageRecords = usageRecordsByTraceId(traceId)
  assert.equal(usageRecords.length, 1, '授权额度耗尽切后备成功后应写入一条使用记录')
  assert.equal(usageRecords[0]?.groupId, fallbackGroup.id, '授权额度耗尽切后备成功后使用记录必须归属实际命中的后备分组')
  const auditLogs = repositories.listAuditLogs({ traceId, pageSize: 10 })
  assert.equal(auditLogs.total, 1, '授权额度耗尽切后备成功后应写入一条审计事件')
  const auditLog = auditLogs.items[0]
  assert.equal(auditLog?.groupId, fallbackGroup.id, '授权额度耗尽切后备成功后审计主记录必须归属实际命中的后备分组')
  const metadataPayloads = await gatewayMetadataPayloads(auditLog?.id ?? '')
  assert(metadataPayloads.some((metadata) => metadata.label === 'api_key_group_route_fallback'
    && metadata.metadata?.reason === 'authorization_quota_exceeded'
    && metadata.metadata?.fromGroupId === primaryGroup.id
    && metadata.metadata?.toGroupId === fallbackGroup.id), '授权额度耗尽切后备成功后审计 metadata 应记录原分组、后备分组和切换原因')
}

function createGatewayServer(): http.Server {
  const app = express()
  app.set('trust proxy', true)
  app.use(requestContextMiddleware)
  app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
  return http.createServer(app)
}

function createMockOpenAIUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(Buffer.from(chunk)))
    req.on('end', () => {
      const body = parseJsonObject(Buffer.concat(chunks).toString('utf8'))
      const token = bearerToken(req.headers.authorization)
      upstreamRequests.push({
        path: String(req.url ?? '').split('?')[0] || '/',
        accountKey: token,
        model: typeof body.model === 'string' ? body.model : undefined
      })
      if (releaseLocalSuppressionsBeforeRespondingKeys.has(token)) {
        accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
        releaseLocalSuppressionsBeforeRespondingKeys.delete(token)
      }
      if (failingUpstreamKeys.has(token)) {
        res.writeHead(502, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({
          error: {
            message: 'primary group upstream failed after dispatch',
            type: 'server_error',
            code: 'bad_gateway'
          }
        }))
        return
      }
      if (token === 'sk-route-response-inspection-primary') {
        res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
        res.end([
          'event: response.created',
          'data: {"type":"response.created","response":{"id":"resp_route_polluted","status":"in_progress"}}',
          '',
          'event: response.output_text.delta',
          'data: {"type":"response.output_text.delta","delta":"route-stream-pollution"}',
          '',
          ''
        ].join('\n'))
        return
      }
      if (token === 'sk-route-response-inspection-fallback') {
        res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
        res.end([
          'event: response.created',
          'data: {"type":"response.created","response":{"id":"resp_route_stream","status":"in_progress"}}',
          '',
          'event: response.output_text.delta',
          'data: {"type":"response.output_text.delta","delta":"route stream ok"}',
          '',
          'event: response.completed',
          'data: {"type":"response.completed","response":{"id":"resp_route_stream","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}',
          '',
          ''
        ].join('\n'))
        return
      }
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({
        id: 'chatcmpl-route-capability',
        object: 'chat.completion',
        choices: [
          {
            index: 0,
            message: { role: 'assistant', content: 'route ok' },
            finish_reason: 'stop'
          }
        ],
        usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
      }))
    })
  })
}

async function requestResponseStream(baseUrl: string, apiKey: string, traceId?: string): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      ...(traceId ? { 'x-trace-id': traceId } : {})
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'route stream',
      stream: true
    })
  })
  return {
    status: response.status,
    text: await response.text()
  }
}

async function requestChatCompletion(baseUrl: string, apiKey: string, model: string, traceId?: string, clientIp?: string): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      ...(traceId ? { 'x-trace-id': traceId } : {}),
      ...(clientIp ? { 'x-forwarded-for': clientIp } : {})
    },
    body: JSON.stringify({
      model,
      messages: [{ role: 'user', content: 'route me' }]
    })
  })
  return {
    status: response.status,
    text: await response.text()
  }
}

function usageRecordsByTraceId(traceId: string): UsageRecordSummary[] {
  return repositories.listUsageRecords(undefined, { pageSize: 200 }).items.filter((record) => record.traceId === traceId)
}

function markUsageRecordReadyForStats(record: UsageRecordSummary | undefined): void {
  assert(record, '使用记录应存在，才能推进统计聚合窗口')
  const location = usageRecordShards.usageRecordShardLocationForRecord(record.id, record.createdAt)
  const createdAt = new Date(Date.now() - 20_000).toISOString()
  usageRecordShards.getUsageRecordShardDatabase(location)
    .prepare('UPDATE usage_records SET created_at = ? WHERE id = ?')
    .run(createdAt, record.id)
}

function usageStatsRequestCount(systemAccountId: string, scopeType: string, scopeId: string): number | undefined {
  const row = databaseModule.getStatsDatabase()
    .prepare('SELECT request_count FROM usage_stats_totals WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?')
    .get(systemAccountId, scopeType, scopeId) as { request_count?: number } | undefined
  return row?.request_count
}

function insertUsageTotal(database: ReturnType<typeof databaseModule.getStatsDatabase>, systemAccountId: string, scopeType: string, scopeId: string, totalCost: number) {
  database.prepare(`
    INSERT INTO usage_stats_totals (
      system_account_id, scope_type, scope_id, total_cost_usd, updated_at
    ) VALUES (?, ?, ?, ?, ?)
  `).run(systemAccountId, scopeType, scopeId, totalCost, new Date().toISOString())
}

function seedQualityScore(systemAccountId: string, accountId: string, qualityScore: number): void {
  const now = new Date().toISOString()
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO account_quality_scores (
        account_id, system_account_id, provider_code, quality_score, quality_state,
        recent_request_count, recent_success_count, recent_error_count, recent_first_token_sample_count,
        recent_avg_first_token_ms, ewma_first_token_ms, success_rate,
        window_started_at, window_ended_at, last_sample_at, updated_at
      ) VALUES (?, ?, 'gpt', ?, 'healthy', 10, 10, 0, 10, ?, ?, 1, ?, ?, ?, ?)
      ON CONFLICT(account_id) DO UPDATE SET
        quality_score = excluded.quality_score,
        quality_state = excluded.quality_state,
        recent_request_count = excluded.recent_request_count,
        recent_success_count = excluded.recent_success_count,
        recent_error_count = excluded.recent_error_count,
        recent_first_token_sample_count = excluded.recent_first_token_sample_count,
        recent_avg_first_token_ms = excluded.recent_avg_first_token_ms,
        ewma_first_token_ms = excluded.ewma_first_token_ms,
        success_rate = excluded.success_rate,
        window_started_at = excluded.window_started_at,
        window_ended_at = excluded.window_ended_at,
        last_sample_at = excluded.last_sample_at,
        updated_at = excluded.updated_at
    `)
    .run(accountId, systemAccountId, qualityScore, qualityScore, qualityScore, now, now, now, now)
}

async function gatewayMetadataPayloads(auditLogId: string): Promise<Array<{
  label?: string
  metadata?: Record<string, unknown>
}>> {
  const auditDetail = repositories.getAuditLogDetail(auditLogId)
  if (!auditDetail) return []
  const payloads = await Promise.all(auditDetail.payloads
    .filter((payload) => payload.partType === 'gateway_metadata')
    .map((payload) => repositories.getAuditLogPayload(auditLogId, payload.id)))
  return payloads
    .map((payload) => parseJsonObject(payload?.bodyText ?? ''))
    .filter((payload) => payload.type === 'gateway_metadata')
    .map((payload) => ({
      label: typeof payload.label === 'string' ? payload.label : undefined,
      metadata: isRecord(payload.metadata) ? payload.metadata : undefined
    }))
}

function traceIdForBucket(predicate: (bucket: number) => boolean, prefix: string): string {
  for (let index = 0; index < 10_000; index += 1) {
    const traceId = `${prefix}-${index}`
    if (predicate(sampleBucketForTraceId(traceId))) {
      return traceId
    }
  }
  throw new Error('无法构造采样 traceId')
}

function sampleBucketForTraceId(traceId: string): number {
  const digest = createHash('sha256').update(traceId).digest()
  return digest.readUInt32BE(0) % 10_000
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function bearerToken(value: unknown): string {
  const text = Array.isArray(value) ? value[0] : String(value ?? '')
  return text.replace(/^Bearer\s+/i, '')
}

function parseJsonObject(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
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
  if (!server || !server.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => {
      if (error) {
        rejectPromise(error)
        return
      }
      resolvePromise()
    })
  })
}
