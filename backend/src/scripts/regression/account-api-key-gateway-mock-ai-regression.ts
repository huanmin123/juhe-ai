import { strict as assert } from 'node:assert'
import { spawn, type ChildProcess } from 'node:child_process'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import {
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

interface MockUpstreamHit {
  authorization: string
  path: string
  userAgent: string
}

type ApiKeyStrategy = 'round_robin' | 'weighted_round_robin'

const currentDir = dirname(fileURLToPath(import.meta.url))
const backendRoot = resolve(currentDir, '../../..')
const projectRoot = resolve(backendRoot, '..')
const useShellSpawn = process.platform === 'win32'
const tempRoot = resolve(tmpdir(), `juhe-ai-account-api-key-gateway-mock-ai-${Date.now()}-${Math.random().toString(16).slice(2)}`)

function profileIdForProvider(providerCode: string): string {
  return providerCode === OPENAI_COMPATIBLE_PROVIDER_CODE
    ? OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID
    : GPT_OPENAI_V1_PROFILE_ID
}

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'account-api-key-gateway-mock-ai-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  apiKeyRotation
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-api-key-rotation.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const mockHits: MockUpstreamHit[] = []
const forcedFailureAuthorizations = new Set<string>()

let mockUpstream: http.Server | undefined
let backendProcess: ChildProcess | undefined
let failoverBadKeyRecovered = false
let postBatchSequence = 0

try {
  mockUpstream = createMockOpenAIUpstream()
  mockUpstream.listen(0, '127.0.0.1')
  await onceListening(mockUpstream)
  const upstreamBaseUrl = `http://127.0.0.1:${serverPort(mockUpstream)}/v1`

  const roundRobinGatewayApiKey = createGatewayApiKeyScenario({
    name: '单账户多 Key 网关轮询',
    upstreamBaseUrl,
    apiKeys: ['sk-gateway-rr-a', 'sk-gateway-rr-b', 'sk-gateway-rr-c'],
    strategy: 'round_robin'
  })
  const weightedGatewayApiKey = createGatewayApiKeyScenario({
    name: '单账户多 Key 网关权重',
    upstreamBaseUrl,
    apiKeys: ['sk-gateway-weight-a', 'sk-gateway-weight-b'],
    strategy: 'weighted_round_robin',
    weights: [3, 1]
  })
  const openAICompatibleGatewayApiKey = createGatewayApiKeyScenario({
    name: 'OpenAI兼容 Key 池网关轮询',
    upstreamBaseUrl,
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    apiKeys: ['sk-gateway-openai-compatible-a', 'sk-gateway-openai-compatible-b'],
    strategy: 'round_robin'
  })
  const failoverGatewayApiKey = createGatewayApiKeyFailoverScenario(upstreamBaseUrl)
  const oauthNonIsolationScenario = createGptOAuthNonIsolationScenario(upstreamBaseUrl)
  const authorizedScenario = createAuthorizedApiKeyScenario(upstreamBaseUrl)
  const allBadScenario = createGatewayApiKeyAllBadScenario(upstreamBaseUrl)
  const superPriorityGatewayApiKey = createGatewaySuperPriorityScenario(upstreamBaseUrl)
  const fallbackGatewayApiKey = createGatewayFallbackScenario(upstreamBaseUrl)
  const fallbackFailureGatewayApiKey = createGatewayFallbackFailureScenario(upstreamBaseUrl)
  const trafficMigrationOverrideScenario = createTrafficMigrationOverrideScenario(upstreamBaseUrl)
  const oauthCandidate = repositories.selectOpenAIAccountForGroup(oauthNonIsolationScenario.groupId, access.systemAccountId)
  assert.equal(oauthCandidate?.type, 'oauth', 'GPT OAuth 非 Key 隔离场景应优先读到 OAuth 候选账户')
  assert.equal(oauthCandidate?.apiKeyRuntimeStates, undefined, 'GPT OAuth 候选账户不应挂载 API Key 运行态')
  assert.equal(apiKeyRotation.isAccountApiKeyPoolIsolationEnabled({
    providerCode: GPT_VENDOR_CODE,
    type: 'oauth',
    credentials: {
      access_token: 'sk-gateway-oauth-bad',
      base_url: upstreamBaseUrl
    }
  }), false, 'GPT OAuth 不应启用账户内 API Key 池隔离')
  assert.equal(
    repositories.listAccounts(access, { page: 1, pageSize: 20 }).filter((account) => account.name.includes('单账户多 Key 网关')).length,
    2,
    '两个策略场景各自只应创建一个账户，不应按 API Key 展开账户'
  )
  const admin = repositories.createSystemAccount({
    username: `api_key_gateway_mock_admin_${Date.now()}`.replace(/[^a-zA-Z0-9_]/g, '_'),
    displayName: 'APIKey网关Mock管理员',
    password: 'password',
    role: 'admin',
    status: 'active',
    mustChangePassword: false
  })
  const session = repositories.createSession(admin.id, 1)
  const cookie = `juhe_ai_session=${session.token}`
  databaseModule.closeStorageDatabases()

  const backendPort = await freePort()
  const backendBaseUrl = `http://127.0.0.1:${backendPort}`
  backendProcess = startBackendServer(backendPort)
  await waitForHealth(backendBaseUrl, backendProcess)
  await waitForApiReady(backendBaseUrl, cookie, backendProcess)

  const roundRobinBatch = await postChatCompletions(backendBaseUrl, roundRobinGatewayApiKey, 5)
  const roundRobinAuthorizations = authorizationsForBatches([roundRobinBatch])
  assert.deepEqual(
    roundRobinAuthorizations,
    [
      'Bearer sk-gateway-rr-a',
      'Bearer sk-gateway-rr-b',
      'Bearer sk-gateway-rr-c',
      'Bearer sk-gateway-rr-a',
      'Bearer sk-gateway-rr-b'
    ],
    '网关真实请求应在单个账户内按 API Key 轮询转发'
  )

  const weightedBatch = await postChatCompletions(backendBaseUrl, weightedGatewayApiKey, 8)
  const weightedAuthorizations = authorizationsForBatches([weightedBatch])
  assert.equal(
    weightedAuthorizations.filter((authorization) => authorization === 'Bearer sk-gateway-weight-a').length,
    6,
    '权重 3 的 API Key 在 8 次真实网关请求中应命中 6 次'
  )
  assert.equal(
    weightedAuthorizations.filter((authorization) => authorization === 'Bearer sk-gateway-weight-b').length,
    2,
    '权重 1 的 API Key 在 8 次真实网关请求中应命中 2 次'
  )

  const openAICompatibleBatch = await postChatCompletions(backendBaseUrl, openAICompatibleGatewayApiKey, 3)
  const openAICompatibleAuthorizations = authorizationsForBatches([openAICompatibleBatch])
  assert.deepEqual(
    openAICompatibleAuthorizations,
    [
      'Bearer sk-gateway-openai-compatible-a',
      'Bearer sk-gateway-openai-compatible-b',
      'Bearer sk-gateway-openai-compatible-a'
    ],
    'OpenAI 兼容供应商的 API Key 账户也应在单个账户内按 Key 轮询'
  )

  const firstFailoverBatch = await postChatCompletions(backendBaseUrl, failoverGatewayApiKey, 1)
  const firstFailoverAuthorizations = authorizationsForBatches([firstFailoverBatch])
  assert.deepEqual(
    firstFailoverAuthorizations,
    ['Bearer sk-gateway-failover-bad', 'Bearer sk-gateway-failover-good'],
    '多 Key 账户当前 Key 失败后，本次请求应优先尝试同账户下一个 Key'
  )
  const failoverBadKeyPersistedState = apiKeyRuntimeStateStatus('sk-gateway-failover-bad')
  assert.equal(failoverBadKeyPersistedState, undefined, '网关流量的单账号坏 Key 只进入本地短避让，不应持久写入运行态')
  const recoveredAccountBatch = await postChatCompletions(backendBaseUrl, failoverGatewayApiKey, 2)
  const recoveredAccountAuthorizations = authorizationsForBatches([recoveredAccountBatch])
  assert.deepEqual(
    recoveredAccountAuthorizations,
    ['Bearer sk-gateway-failover-good', 'Bearer sk-gateway-failover-bad', 'Bearer sk-gateway-failover-good'],
    '通用未知失败不得持久化屏蔽坏 Key，后续请求仍可按请求级轮换继续完成'
  )
  assert.equal(
    mockHits.filter((hit) => hit.authorization === 'Bearer sk-gateway-failover-bad').length,
    2,
    '通用未知失败只允许请求级排除，后续独立请求可以再次尝试该 Key'
  )
  failoverBadKeyRecovered = true

  const authorizedBatch = await postChatCompletions(backendBaseUrl, authorizedScenario.apiKey, 1)
  const authorizedAuthorizations = authorizationsForBatches([authorizedBatch])
  assert.deepEqual(
    authorizedAuthorizations,
    ['Bearer sk-gateway-authorized-bad', 'Bearer sk-gateway-authorized-good'],
    '被授权实例命中来源账户坏 Key 后，本次请求应优先尝试来源账户下一个 Key'
  )
  const authorizedRuntimeTarget = apiKeyRuntimeStateTargetAccountIdOrMissing('sk-gateway-authorized-bad')
  assert.equal(authorizedRuntimeTarget, undefined, '授权实例命中来源账户坏 Key 后，网关流量也只做本地短避让，不应持久写入来源账户 Key 运行态')
  const authorizedInstanceSummary = repositories.findAccountForTest(authorizedScenario.authorizedInstanceAccountId, authorizedScenario.granteeAccess)
  assert.equal(authorizedInstanceSummary?.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, '网关流量本地短避让不应污染被授权实例持久 Key 池摘要')
  assert.equal(authorizedInstanceSummary?.apiKeyRuntimeDetails, undefined, '被授权实例不应暴露来源账户 Key 明细')

  const allBadBatches = [
    await postChatCompletions(backendBaseUrl, allBadScenario.apiKey, 1),
    await postChatCompletions(backendBaseUrl, allBadScenario.apiKey, 1),
    await postChatCompletions(backendBaseUrl, allBadScenario.apiKey, 1)
  ]
  const allBadAuthorizations = authorizationsForBatches(allBadBatches)
  assert.deepEqual(
    allBadAuthorizations,
    [
      'Bearer sk-gateway-allbad-a',
      'Bearer sk-gateway-allbad-c',
      'Bearer sk-gateway-allbad-rescue',
      'Bearer sk-gateway-allbad-a',
      'Bearer sk-gateway-allbad-c',
      'Bearer sk-gateway-allbad-rescue',
      'Bearer sk-gateway-allbad-a',
      'Bearer sk-gateway-allbad-c',
      'Bearer sk-gateway-allbad-rescue'
    ],
    '通用未知失败应在每次请求内最多尝试两个 Key 后切后备账户，跨请求不得持久屏蔽 Key'
  )
  assert.equal(
    allBadAuthorizations.indexOf('Bearer sk-gateway-allbad-rescue'),
    2,
    '三个坏 Key 场景首个请求应只追加尝试一个账户内 Key 后切后备账户'
  )
  const allBadSourceSummary = repositories.findAccountForTest(allBadScenario.sourceAccountId, access)
  assert.equal(allBadSourceSummary?.apiKeyRuntime?.allUnavailable ?? false, false, '单来源打穿全部 Key 不应写成全局 Key 池不可用')
  assert.notEqual(allBadSourceSummary?.effectiveAvailability?.status, 'api_key_pool_unavailable', '单来源打穿全部 Key 不应让账户有效可用性变成 Key 池不可用')
  const allBadTemporaryStatusIds = repositories.listAccountsPage(access, { status: 'temporary_unavailable', page: 1, pageSize: 50 }).items.map((item) => item.id)
  assert(!allBadTemporaryStatusIds.includes(allBadScenario.sourceAccountId), '单来源打穿全部 Key 不应让账户归入临时不可调用状态筛选')
  const allBadActiveStatusIds = repositories.listAccountsPage(access, { status: 'active', page: 1, pageSize: 50 }).items.map((item) => item.id)
  assert(allBadActiveStatusIds.includes(allBadScenario.sourceAccountId), '单来源打穿全部 Key 后账户数据库状态仍应归入正常状态筛选')
  const allBadTemporaryOptionIds = repositories.listAccountOptions(access, { status: 'temporary_unavailable', limit: 50 }).map((item) => item.id)
  assert(!allBadTemporaryOptionIds.includes(allBadScenario.sourceAccountId), '单来源打穿全部 Key 后账户 options 不应归入临时不可调用状态筛选')

  const superPriorityBatch = await postChatCompletions(backendBaseUrl, superPriorityGatewayApiKey, 1)
  const superPriorityAuthorizations = authorizationsForBatches([superPriorityBatch])
  assert.deepEqual(
    superPriorityAuthorizations,
    ['Bearer sk-gateway-super-priority'],
    '真实网关请求应优先命中同分组超级优先账户'
  )

  const fallbackBatch = await postChatCompletions(backendBaseUrl, fallbackGatewayApiKey, 1)
  const fallbackAuthorizations = authorizationsForBatches([fallbackBatch])
  assert.deepEqual(
    fallbackAuthorizations,
    ['Bearer sk-gateway-fallback-primary'],
    '真实网关请求在主池可用时不应先打到降级备用账户'
  )

  const fallbackFailureBatch = await postChatCompletions(backendBaseUrl, fallbackFailureGatewayApiKey, 1)
  const fallbackFailureAuthorizations = authorizationsForBatches([fallbackFailureBatch])
  assert.equal(fallbackFailureAuthorizations[0], 'Bearer sk-gateway-fallback-bad-primary', '真实网关请求应先尝试主池账户')
  const fallbackReserveIndex = fallbackFailureAuthorizations.indexOf('Bearer sk-gateway-fallback-after-failure')
  assert(fallbackReserveIndex > 0, '真实网关请求在主池失败后应降级切到备用账户')
  assert(
    fallbackFailureAuthorizations.slice(0, fallbackReserveIndex).every((authorization) => authorization === 'Bearer sk-gateway-fallback-bad-primary'),
    `切到备用前不应提前命中其他备用或无关账户，实际 ${JSON.stringify(fallbackFailureAuthorizations)}`
  )

  await postAdminEnvelope(
    backendBaseUrl,
    `/__aisys__/api/accounts/${trafficMigrationOverrideScenario.sourceAccountId}/traffic-migration?systemAccountId=${access.systemAccountId}`,
    cookie,
    {
      targetAccountId: trafficMigrationOverrideScenario.targetAccountId,
      sourceStatus: 'temporary_unavailable'
    }
  )
  const trafficMigrationBatch = await postChatCompletions(backendBaseUrl, trafficMigrationOverrideScenario.apiKey, 1)
  const trafficMigrationAuthorizations = authorizationsForBatches([trafficMigrationBatch])
  assert.deepEqual(
    trafficMigrationAuthorizations,
    ['Bearer sk-gateway-migration-target'],
    '迁移流量后真实网关请求应短期优先命中目标账户，即使目标是备用且同组存在超级优先账户'
  )

  console.log(JSON.stringify({
    message: '单账户多 API Key 网关 mock AI 回归通过',
    backendBaseUrl,
    mockUpstreamBaseUrl: upstreamBaseUrl,
    roundRobin: roundRobinAuthorizations,
    weighted: weightedAuthorizations,
    openAICompatible: openAICompatibleAuthorizations,
    failover: {
      first: firstFailoverAuthorizations,
      afterKeyIsolation: recoveredAccountAuthorizations,
      persistedStateAfterConfirmation: failoverBadKeyPersistedState
    },
    oauthNonIsolation: 'excluded_from_api_key_pool',
    authorizedSourceRuntime: {
      authorizations: authorizedAuthorizations,
      runtimeStateAccountId: authorizedRuntimeTarget ?? 'not_written_for_single_source'
    },
    allBad: allBadAuthorizations,
    superPriority: superPriorityAuthorizations,
    fallbackPrimaryFirst: fallbackAuthorizations,
    fallbackAfterFailure: fallbackFailureAuthorizations,
    trafficMigrationOverride: trafficMigrationAuthorizations
  }, null, 2))
} finally {
  await stopBackendServer(backendProcess)
  await closeServer(mockUpstream)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  await removeTempRoot(tempRoot)
}

function createGatewayApiKeyScenario(input: {
  apiKeys: string[]
  name: string
  providerCode?: string
  strategy: ApiKeyStrategy
  upstreamBaseUrl: string
  weights?: number[]
}): string {
  const providerCode = input.providerCode ?? GPT_VENDOR_CODE
  const group = repositories.createGroup({
    name: `${input.name} 分组`,
    providerCode,
    enabled: true
  }, access)
  const account = createActiveAccount({
    providerCode,
    providerProtocolProfileId: profileIdForProvider(providerCode),
    name: `${input.name} 账户`,
    type: 'api_key',
    credentials: {
      api_key: input.apiKeys[0],
      api_keys: input.apiKeys,
      api_key_strategy: input.strategy,
      api_key_weights: input.weights,
      base_url: input.upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access)
  assert.deepEqual(account.credentials.api_keys, input.apiKeys, `${input.name} 应把多个 API Key 保存在同一个账户`)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `${input.name} 网关 Key`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, `${input.name} 未返回网关 API Key 明文`)
  return apiKey.key
}

function createGptOAuthNonIsolationScenario(upstreamBaseUrl: string): { groupId: string } {
  const group = repositories.createGroup({
    name: 'GPT OAuth 非 Key 隔离分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'A GPT OAuth 非 Key 隔离账户',
    type: 'oauth',
    credentials: {
      access_token: 'sk-gateway-oauth-bad',
      expires_at: new Date(Date.now() + 60 * 60 * 1000).toISOString(),
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'B GPT OAuth 非 Key 隔离救援账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-oauth-rescue',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 10
  }, access)
  return { groupId: group.id }
}

function createAuthorizedApiKeyScenario(upstreamBaseUrl: string): {
  apiKey: string
  authorizedInstanceAccountId: string
  granteeAccess: { systemAccountId: string; role: 'user' }
  sourceAccountId: string
} {
  const owner = repositories.createSystemAccount({
    username: `api_key_authorized_owner_${Date.now()}`.replace(/[^a-zA-Z0-9_]/g, '_'),
    displayName: 'Key隔离授权方',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: `api_key_authorized_grantee_${Date.now()}`.replace(/[^a-zA-Z0-9_]/g, '_'),
    displayName: 'Key隔离被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const ownerGroup = repositories.createGroup({
    name: '授权 Key 隔离来源分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, ownerAccess)
  const granteeGroup = repositories.createGroup({
    name: '授权 Key 隔离被授权分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, granteeAccess)
  const sourceAccount = createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'A 授权 Key 隔离来源账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-authorized-bad',
      api_keys: ['sk-gateway-authorized-bad', 'sk-gateway-authorized-good'],
      api_key_strategy: 'round_robin',
      base_url: upstreamBaseUrl
    },
    groupId: ownerGroup.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: sourceAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: 'Key 隔离授权实例来源账户回归'
  }, ownerAccess)
  const authorizedInstance = databaseModule.getBusinessDatabase()
    .prepare('SELECT id FROM accounts WHERE authorization_instance_source_account_id = ? AND system_account_id = ? AND deleted_at IS NULL LIMIT 1')
    .get(sourceAccount.id, grantee.id) as unknown as { id?: string } | undefined
  assert(authorizedInstance?.id, '授权 Key 隔离场景需要被授权实例账户')
  createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'B 授权 Key 隔离被授权人救援账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-authorized-rescue',
      base_url: upstreamBaseUrl
    },
    groupId: granteeGroup.id,
    status: 'active',
    schedulable: true,
    priority: 10
  }, granteeAccess)
  forcedFailureAuthorizations.add('Bearer sk-gateway-authorized-bad')
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '授权 Key 隔离网关 Key',
    groupBindings: [{ groupId: granteeGroup.id, priority: 1, status: 'active' }],
    status: 'active'
  }, granteeAccess)
  assert(apiKey.key, '授权 Key 隔离场景未返回网关 API Key 明文')
  return {
    apiKey: apiKey.key,
    authorizedInstanceAccountId: authorizedInstance.id,
    granteeAccess,
    sourceAccountId: sourceAccount.id
  }
}

function createGatewayApiKeyFailoverScenario(upstreamBaseUrl: string): string {
  const group = repositories.createGroup({
    name: '多 Key 摘除切号分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'A 多 Key 摘除来源账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-failover-bad',
      api_keys: ['sk-gateway-failover-bad', 'sk-gateway-failover-good'],
      api_key_strategy: 'round_robin',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'B 多 Key 摘除救援账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-failover-rescue',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 10
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '多 Key 摘除切号网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '多 Key 摘除切号场景未返回网关 API Key 明文')
  return apiKey.key
}

function createGatewayApiKeyAllBadScenario(upstreamBaseUrl: string): { apiKey: string; sourceAccountId: string } {
  const group = repositories.createGroup({
    name: '全部 Key 摘除切号分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const sourceAccount = createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'A 全部 Key 摘除来源账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-allbad-a',
      api_keys: ['sk-gateway-allbad-a', 'sk-gateway-allbad-b', 'sk-gateway-allbad-c'],
      api_key_strategy: 'round_robin',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'B 全部 Key 摘除救援账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-allbad-rescue',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 10
  }, access)
  forcedFailureAuthorizations.add('Bearer sk-gateway-allbad-a')
  forcedFailureAuthorizations.add('Bearer sk-gateway-allbad-b')
  forcedFailureAuthorizations.add('Bearer sk-gateway-allbad-c')
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '全部 Key 摘除切号网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '全部 Key 摘除切号场景未返回网关 API Key 明文')
  return { apiKey: apiKey.key, sourceAccountId: sourceAccount.id }
}

function createGatewaySuperPriorityScenario(upstreamBaseUrl: string): string {
  const group = repositories.createGroup({
    name: '超级优先真实网关分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'A 超级优先普通账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-super-normal',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'B 超级优先账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-super-priority',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 50,
    superPriorityEnabled: true
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '超级优先真实网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '超级优先真实网关场景未返回网关 API Key 明文')
  return apiKey.key
}

function createGatewayFallbackScenario(upstreamBaseUrl: string): string {
  const group = repositories.createGroup({
    name: '备用降级真实网关分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'A 备用降级主池账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-fallback-primary',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 50
  }, access)
  createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'B 备用降级备用账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-fallback-reserve',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0,
    fallbackEnabled: true
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '备用降级真实网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '备用降级真实网关场景未返回网关 API Key 明文')
  return apiKey.key
}

function createGatewayFallbackFailureScenario(upstreamBaseUrl: string): string {
  const group = repositories.createGroup({
    name: '备用失败接管真实网关分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'A 备用失败主池账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-fallback-bad-primary',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 50
  }, access)
  createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'B 备用失败接管账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-fallback-after-failure',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0,
    fallbackEnabled: true
  }, access)
  forcedFailureAuthorizations.add('Bearer sk-gateway-fallback-bad-primary')
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '备用失败接管真实网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '备用失败接管真实网关场景未返回网关 API Key 明文')
  return apiKey.key
}

function createTrafficMigrationOverrideScenario(upstreamBaseUrl: string): { apiKey: string; sourceAccountId: string; targetAccountId: string } {
  const group = repositories.createGroup({
    name: '迁移覆盖真实网关分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const sourceAccount = createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'A 迁移覆盖源账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-migration-source',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'B 迁移覆盖超级优先账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-migration-super',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0,
    superPriorityEnabled: true
  }, access)
  const targetAccount = createActiveAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'C 迁移覆盖目标备用账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-migration-target',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 50,
    fallbackEnabled: true
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '迁移覆盖真实网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '迁移覆盖真实网关场景未返回网关 API Key 明文')
  return {
    apiKey: apiKey.key,
    sourceAccountId: sourceAccount.id,
    targetAccountId: targetAccount.id
  }
}

function createActiveAccount(
  input: Parameters<typeof repositories.createAccount>[0],
  accountAccess: Parameters<typeof repositories.createAccount>[1]
) {
  const account = repositories.createAccount(input, accountAccess)
  assert(repositories.recordAccountHealthCheckSuccess(account.id, {
    intervalHours: 24,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), `Mock AI 测试账户 ${account.id} 应能通过后台健康检查激活`)
  return account
}

async function postChatCompletions(backendBaseUrl: string, apiKey: string, count: number): Promise<string> {
  const batchId = `gateway-api-key-regression-${++postBatchSequence}`
  for (let index = 0; index < count; index += 1) {
    const response = await fetch(`${backendBaseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey}`,
        'content-type': 'application/json',
        'user-agent': batchUserAgent(batchId)
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        messages: [{ role: 'user', content: `mock gateway api key rotation ${index + 1}` }],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `网关请求应成功，实际 HTTP ${response.status}: ${text}`)
  }
  return batchId
}

async function postAdminEnvelope(backendBaseUrl: string, path: string, cookie: string, body: Record<string, unknown>): Promise<unknown> {
  const response = await fetch(`${backendBaseUrl}${path}`, {
    method: 'POST',
    headers: {
      cookie,
      'content-type': 'application/json'
    },
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert.equal(response.ok, true, `${path} 应成功，实际 HTTP ${response.status}: ${text}`)
  return text ? JSON.parse(text) : undefined
}

function authorizationsForBatches(batchIds: string[]): string[] {
  const userAgents = new Set(batchIds.map(batchUserAgent))
  return mockHits
    .filter((hit) => userAgents.has(hit.userAgent))
    .map((hit) => hit.authorization)
}

function batchUserAgent(batchId: string): string {
  return `juhe-ai-account-api-key-gateway-mock-ai/${batchId}`
}

function createMockOpenAIUpstream(): http.Server {
  return http.createServer((req, res) => {
    const requestPath = req.url ?? ''
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      if (req.method !== 'POST' || (requestPath !== '/v1/chat/completions' && requestPath !== '/v1/responses')) {
        sendJsonError(res, 404, 'mock upstream path not found')
        return
      }
      mockHits.push({
        authorization: String(req.headers.authorization ?? ''),
        path: requestPath,
        userAgent: String(req.headers['user-agent'] ?? '')
      })
      const authorization = String(req.headers.authorization ?? '')
      if (shouldFailAuthorization(authorization)) {
        sendJsonError(res, 503, 'mock upstream key unavailable')
        return
      }
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify(successPayloadForPath(requestPath)))
    })
  })
}

function shouldFailAuthorization(authorization: string): boolean {
  if (authorization === 'Bearer sk-gateway-failover-bad') {
    return !failoverBadKeyRecovered
  }
  return forcedFailureAuthorizations.has(authorization)
}

function successPayloadForPath(requestPath: string): Record<string, unknown> {
  if (requestPath === '/v1/responses') {
    return {
      id: 'resp-account-api-key-gateway-mock-ai',
      object: 'response',
      output: [
        {
          type: 'message',
          content: [{ type: 'output_text', text: 'OK' }]
        }
      ],
      usage: {
        input_tokens: 3,
        output_tokens: 4,
        total_tokens: 7
      }
    }
  }
  return {
    id: 'chatcmpl-account-api-key-gateway-mock-ai',
    object: 'chat.completion',
    choices: [
      {
        index: 0,
        message: { role: 'assistant', content: 'mock api key gateway ok' },
        finish_reason: 'stop'
      }
    ],
    usage: {
      input_tokens: 3,
      output_tokens: 4,
      total_tokens: 7
    }
  }
}

function apiKeyRuntimeStateStatus(key: string): string | undefined {
  const fingerprint = apiKeyRotation.fingerprintAccountApiKey(key)
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT status FROM account_api_key_runtime_states WHERE key_fingerprint = ? LIMIT 1')
    .get(fingerprint) as unknown as { status?: string } | undefined
  return row?.status
}

function apiKeyRuntimeStateTargetAccountIdOrMissing(key: string): string | undefined {
  const fingerprint = apiKeyRotation.fingerprintAccountApiKey(key)
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT account_id FROM account_api_key_runtime_states WHERE key_fingerprint = ? LIMIT 1')
    .get(fingerprint) as unknown as { account_id?: string } | undefined
  return row?.account_id
}

function sendJsonError(res: http.ServerResponse, statusCode: number, message: string): void {
  res.writeHead(statusCode, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({ error: { message, code: `mock_${statusCode}` } }))
}

function startBackendServer(port: number): ChildProcess {
  const child = spawn('pnpm', ['--filter', 'juhe-ai-backend', 'exec', 'tsx', 'src/server.ts'], {
    cwd: projectRoot,
    env: {
      ...process.env,
      NODE_ENV: '',
      JUHE_AI_HOST: '127.0.0.1',
      JUHE_AI_PORT: String(port),
      JUHE_AI_DB_SERVICE_HTTP_HOST: '127.0.0.1',
      JUHE_AI_DB_SERVICE_HTTP_PORT: '0',
      JUHE_AI_DATABASE_PATH: runtimeConfig.databasePath,
      JUHE_AI_DATASET_DATABASE_PATH: runtimeConfig.datasetDatabasePath,
      JUHE_AI_STATS_DATABASE_PATH: runtimeConfig.statsDatabasePath,
      JUHE_AI_USAGE_SHARD_ROOT: runtimeConfig.usageShardRoot,
      JUHE_AI_SECRET: runtimeConfig.secret,
      JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS: 'true',
      JUHE_AI_LOG_LEVEL: 'warn',
      JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
      JUHE_AI_LOG_FILE_ENABLED: 'false'
    },
    shell: useShellSpawn,
    stdio: ['ignore', 'pipe', 'pipe']
  })
  child.stdout?.on('data', (chunk) => {
    process.stdout.write(`[api-key-gateway-backend] ${String(chunk)}`)
  })
  child.stderr?.on('data', (chunk) => {
    process.stderr.write(`[api-key-gateway-backend] ${String(chunk)}`)
  })
  return child
}

async function waitForHealth(baseUrl: string, child: ChildProcess): Promise<void> {
  const startedAt = Date.now()
  while (Date.now() - startedAt < 20_000) {
    if (child.exitCode !== null) {
      throw new Error(`临时后端提前退出，exitCode=${child.exitCode}`)
    }
    try {
      const response = await fetch(`${baseUrl}/__aisys__/health`)
      if (response.ok) return
    } catch {
    }
    await sleep(200)
  }
  throw new Error('临时后端健康检查等待超时')
}

async function waitForApiReady(baseUrl: string, cookie: string, child: ChildProcess): Promise<void> {
  const startedAt = Date.now()
  while (Date.now() - startedAt < 20_000) {
    if (child.exitCode !== null) {
      throw new Error(`临时后端提前退出，exitCode=${child.exitCode}`)
    }
    try {
      const response = await fetch(`${baseUrl}/__aisys__/api/auth/me`, { headers: { cookie } })
      if (response.ok) return
    } catch {
    }
    await sleep(200)
  }
  throw new Error('临时后端管理 API 等待超时')
}

async function waitFor(predicate: () => boolean, timeoutMs: number): Promise<void> {
  const startedAt = Date.now()
  while (Date.now() - startedAt <= timeoutMs) {
    if (predicate()) {
      return
    }
    await sleep(50)
  }
  assert.fail(`等待条件超时 ${timeoutMs}ms`)
}

async function onceListening(server: http.Server): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

async function closeServer(server?: http.Server): Promise<void> {
  if (!server?.listening) return
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

async function stopBackendServer(child?: ChildProcess): Promise<void> {
  if (!child || child.exitCode !== null) return
  if (process.platform === 'win32' && child.pid) {
    await killWindowsProcessTree(child.pid)
  } else {
    child.kill('SIGTERM')
  }
  await new Promise<void>((resolvePromise) => {
    const timeout = setTimeout(() => {
      child.kill('SIGKILL')
      resolvePromise()
    }, 3000)
    child.once('exit', () => {
      clearTimeout(timeout)
      resolvePromise()
    })
  })
  await sleep(500)
}

async function killWindowsProcessTree(pid: number): Promise<void> {
  await new Promise<void>((resolvePromise) => {
    const killer = spawn('taskkill', ['/pid', String(pid), '/t', '/f'], { stdio: 'ignore' })
    killer.once('error', () => resolvePromise())
    killer.once('exit', () => resolvePromise())
  })
}

async function removeTempRoot(path: string): Promise<void> {
  let lastError: unknown
  for (let attempt = 0; attempt < 10; attempt += 1) {
    try {
      rmSync(path, { recursive: true, force: true })
      return
    } catch (error) {
      lastError = error
      await sleep(200)
    }
  }
  throw lastError
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(address && typeof address !== 'string', 'mock AI upstream should be listening')
  return address.port
}

async function freePort(): Promise<number> {
  const server = net.createServer()
  server.listen(0, '127.0.0.1')
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
  const address = server.address()
  assert(address && typeof address !== 'string', 'free port server should be listening')
  const port = address.port
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => {
      if (error) {
        rejectPromise(error)
        return
      }
      resolvePromise()
    })
  })
  return port
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}
