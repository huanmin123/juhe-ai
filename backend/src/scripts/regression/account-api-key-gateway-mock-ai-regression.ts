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
const forcedFailureStatusByAuthorization = new Map<string, number>()
const forcedTransportFailureAuthorizations = new Set<string>()
const heldSuccessResponseUserAgents = new Set<string>()
const heldSuccessResponseReleases = new Map<string, Array<() => void>>()

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
  const threeKeyExhaustionGatewayApiKey = createGatewayApiKeyScenario({
    name: '三 Key 请求内严格穷尽',
    upstreamBaseUrl,
    apiKeys: ['sk-gateway-three-a', 'sk-gateway-three-b', 'sk-gateway-three-good'],
    strategy: 'round_robin',
    concurrencyLimit: 64
  })
  forcedFailureStatusByAuthorization.set('Bearer sk-gateway-three-a', 401)
  forcedFailureStatusByAuthorization.set('Bearer sk-gateway-three-b', 503)
  const transportRecoveryGatewayApiKey = createGatewayApiKeyScenario({
    name: '三 Key transport 后同账户恢复',
    upstreamBaseUrl,
    apiKeys: ['sk-gateway-transport-bad', 'sk-gateway-transport-good', 'sk-gateway-transport-reserve'],
    strategy: 'round_robin',
    concurrencyLimit: 64
  })
  const transportRecoveryAccountId = accountIdByName('三 Key transport 后同账户恢复 账户')
  forcedTransportFailureAuthorizations.add('Bearer sk-gateway-transport-bad')
  const delayedFailureRaceGatewayApiKey = createGatewayApiKeyScenario({
    name: '迟到 Key transport 确认 fencing',
    upstreamBaseUrl,
    apiKeys: ['sk-gateway-race-bad', 'sk-gateway-race-good'],
    strategy: 'round_robin',
    supportedModels: ['gpt-5.5', 'gpt-4.1'],
    concurrencyLimit: 64
  })
  const delayedFailureRaceAccountId = accountIdByName('迟到 Key transport 确认 fencing 账户')
  forcedTransportFailureAuthorizations.add('Bearer sk-gateway-race-bad')
  const statusChaosKeys = [
    'sk-gateway-chaos-400',
    'sk-gateway-chaos-401',
    'sk-gateway-chaos-429',
    'sk-gateway-chaos-500',
    'sk-gateway-chaos-503',
    'sk-gateway-chaos-good'
  ]
  const statusChaosGatewayApiKey = createGatewayApiKeyScenario({
    name: '六 Key 状态码乱序',
    upstreamBaseUrl,
    apiKeys: statusChaosKeys,
    strategy: 'round_robin'
  })
  for (const [index, status] of [400, 401, 429, 500, 503].entries()) {
    forcedFailureStatusByAuthorization.set(`Bearer ${statusChaosKeys[index]}`, status)
  }
  const physicalKeyDedupeScenario = createGatewayPhysicalKeyDedupeScenario(upstreamBaseUrl)
  forcedFailureStatusByAuthorization.set('Bearer sk-gateway-physical-shared-bad', 503)
  forcedFailureStatusByAuthorization.set('Bearer sk-gateway-physical-a-bad', 401)
  const allDeadKeys = Array.from({ length: 5 }, (_, index) => `sk-gateway-all-dead-${index + 1}`)
  const allDeadGatewayApiKey = createGatewayApiKeyScenario({
    name: '五 Key 全坏稳定失败',
    upstreamBaseUrl,
    apiKeys: allDeadKeys,
    strategy: 'round_robin'
  })
  for (const [index, key] of allDeadKeys.entries()) {
    forcedFailureStatusByAuthorization.set(`Bearer ${key}`, [500, 401, 429, 400, 503][index]!)
  }
  const oversizedPoolScenario = createGatewayOversizedPoolScenario(upstreamBaseUrl)
  const oversizedPoolKeys = oversizedPoolScenario.apiKeys
  const oversizedPoolGatewayApiKey = oversizedPoolScenario.gatewayApiKey
  for (const [index, key] of oversizedPoolKeys.entries()) {
    forcedFailureStatusByAuthorization.set(`Bearer ${key}`, [400, 401, 429, 500, 503][index % 5]!)
  }
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
      account_id: 'acct-gateway-oauth-non-isolation-bad',
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
  assert.equal(failoverBadKeyPersistedState, undefined, '未知 HTTP 失败不得写入 Key 持久或共享运行态')
  const recoveredAccountBatch = await postChatCompletions(backendBaseUrl, failoverGatewayApiKey, 2)
  const recoveredAccountAuthorizations = authorizationsForBatches([recoveredAccountBatch])
  assert.deepEqual(
    recoveredAccountAuthorizations,
    ['Bearer sk-gateway-failover-good', 'Bearer sk-gateway-failover-bad', 'Bearer sk-gateway-failover-good'],
    '独立请求应继续按 Key 轮询事实选择，不能继承未知 HTTP 响应的 Key 死亡判断'
  )
  assert.equal(
    mockHits.filter((hit) => hit.authorization === 'Bearer sk-gateway-failover-bad').length,
    2,
    '新的独立请求再次轮到该 Key 时应重新验证，不能继承未知 HTTP 失败状态'
  )
  failoverBadKeyRecovered = true

  const threeKeyBatch = await postChatCompletions(backendBaseUrl, threeKeyExhaustionGatewayApiKey, 1)
  const threeKeyAuthorizations = authorizationsForBatches([threeKeyBatch])
  assert.deepEqual(
    threeKeyAuthorizations,
    ['Bearer sk-gateway-three-a', 'Bearer sk-gateway-three-b', 'Bearer sk-gateway-three-good'],
    '无后备账户时，三 Key 账户前两个失败后必须在同一请求命中第三个健康 Key'
  )
  const nextThreeKeyBatch = await postChatCompletions(backendBaseUrl, threeKeyExhaustionGatewayApiKey, 1)
  assert.deepEqual(
    authorizationsForBatches([nextThreeKeyBatch]),
    ['Bearer sk-gateway-three-b', 'Bearer sk-gateway-three-good'],
    '前一请求的请求内切号不得多次推进全局游标；新请求应从下一个轮换 Key 重新评估'
  )

  const concurrentBadSessionBatchIds = Array.from(
    { length: 20 },
    (_, index) => `gateway-api-key-concurrent-bad-session-${index + 1}`
  )
  const concurrentBadSessionResults = await Promise.all(concurrentBadSessionBatchIds.map((batchId, index) => (
    postSingleChatCompletion(backendBaseUrl, threeKeyExhaustionGatewayApiKey, batchId, {
      content: `same damaged session concurrent request ${index + 1}`,
      sessionId: 'damaged-session-shared-by-20-requests'
    })
  )))
  assert(concurrentBadSessionResults.every((result) => result.status === 200), `并发坏会话应全部由健康 Key 接管：${JSON.stringify(concurrentBadSessionResults)}`)
  for (const batchId of concurrentBadSessionBatchIds) {
    const hits = authorizationsForBatches([batchId])
    assert.equal(new Set(hits).size, hits.length, `同一并发请求内每个 Key 最多一次：${batchId} ${JSON.stringify(hits)}`)
    assert.equal(hits.at(-1), 'Bearer sk-gateway-three-good', `并发坏会话最终必须命中健康 Key：${batchId} ${JSON.stringify(hits)}`)
    assert(hits.length >= 1 && hits.length <= 3, `三 Key 请求命中次数必须位于 1..3：${batchId} ${JSON.stringify(hits)}`)
  }
  assert.equal(apiKeyRuntimeStateStatus('sk-gateway-three-a'), undefined, '并发未知上游失败不得把坏会话事实写成共享 Key 死亡')
  assert.equal(apiKeyRuntimeStateStatus('sk-gateway-three-b'), undefined, '并发未知上游失败不得把第二个 Key 写成共享死亡')
  assert.deepEqual(
    accountDatabaseAvailabilityByName('三 Key 请求内严格穷尽 账户'),
    { status: 'active', schedulable: 1 },
    '并发坏会话风暴后来源账户仍必须保持 active 且可调度'
  )

  const transportRecoveryBatchId = `gateway-api-key-transport-recovery-${++postBatchSequence}`
  const transportRecovery = await postSingleChatCompletion(
    backendBaseUrl,
    transportRecoveryGatewayApiKey,
    transportRecoveryBatchId,
    {
      content: 'first physical key drops transport, second key completes framing',
      sessionId: 'same-session-after-transport-key-rotation'
    }
  )
  assert.equal(transportRecovery.status, 200, `第二个物理 Key 必须接管首 Key transport 失败：${transportRecovery.text}`)
  assert.deepEqual(
    authorizationsForBatches([transportRecoveryBatchId]),
    ['Bearer sk-gateway-transport-bad', 'Bearer sk-gateway-transport-good'],
    '同请求必须先观测 transport 失败，再由同账户第二 Key 完成 framing，第三 Key 不应多余派发'
  )
  const transportIsolationBatchIds: string[] = []
  for (let index = 0; index < 3; index += 1) {
    const batchId = `gateway-api-key-transport-isolation-${++postBatchSequence}`
    transportIsolationBatchIds.push(batchId)
    const result = await postSingleChatCompletion(
      backendBaseUrl,
      transportRecoveryGatewayApiKey,
      batchId,
      {
        content: `confirmed bad fingerprint must stay locally isolated ${index + 1}`,
        sessionId: 'same-session-after-transport-key-rotation'
      }
    )
    assert.equal(result.status, 200, `短避让窗口内后续请求必须由兄弟 Key 成功接管：${result.text}`)
  }
  const transportIsolationAuthorizations = authorizationsForBatches(transportIsolationBatchIds)
  assert.equal(transportIsolationAuthorizations.length, 3, '短避让窗口内每个后续请求应只派发一个健康兄弟 Key')
  assert(
    transportIsolationAuthorizations.every((authorization) => authorization !== 'Bearer sk-gateway-transport-bad'),
    `兄弟 Key 协议成功确认后，坏 fingerprint 必须进入短避让：${JSON.stringify(transportIsolationAuthorizations)}`
  )
  assert.equal(apiKeyRuntimeStateStatus('sk-gateway-transport-bad'), undefined, '网关 transport 相对证据只能短避让，不能写 Key 持久状态')
  assert.deepEqual(
    accountDatabaseAvailabilityByName('三 Key transport 后同账户恢复 账户'),
    { status: 'active', schedulable: 1 },
    '单 Key transport 故障不得改变来源账户持久可用性'
  )
  assertNoOpenAccountCircuit(transportRecoveryAccountId, '单 Key transport 故障与兄弟 Key 成功不得误熔断整个账户')

  await sleep(3_100)
  forcedTransportFailureAuthorizations.delete('Bearer sk-gateway-transport-bad')
  const transportRecoveredBatchIds: string[] = []
  for (let index = 0; index < 3; index += 1) {
    const batchId = `gateway-api-key-transport-reincluded-${++postBatchSequence}`
    transportRecoveredBatchIds.push(batchId)
    const result = await postSingleChatCompletion(backendBaseUrl, transportRecoveryGatewayApiKey, batchId, {
      content: `expired local isolation must allow a protocol-success trial ${index + 1}`
    })
    assert.equal(result.status, 200, `短避让到期后的 Key 轮换请求必须成功：${result.text}`)
    if (authorizationsForBatches([batchId]).includes('Bearer sk-gateway-transport-bad')) break
  }
  assert(
    authorizationsForBatches(transportRecoveredBatchIds).includes('Bearer sk-gateway-transport-bad'),
    '短避让到期后，原坏 fingerprint 必须重新纳入轮换并能由协议成功恢复'
  )

  const delayedFailureBatchId = `gateway-api-key-delayed-failure-${++postBatchSequence}`
  holdMockSuccessResponses(delayedFailureBatchId)
  const delayedFailureRequest = postSingleChatCompletion(
    backendBaseUrl,
    delayedFailureRaceGatewayApiKey,
    delayedFailureBatchId,
    { content: 'hold sibling success while the old key failure remains pending' }
  )
  await waitFor(
    () => authorizationsForBatches([delayedFailureBatchId]).includes('Bearer sk-gateway-race-good'),
    2_000
  )
  forcedTransportFailureAuthorizations.delete('Bearer sk-gateway-race-bad')
  const newerSuccessBatchIds: string[] = []
  for (let index = 0; index < 2; index += 1) {
    const batchId = `gateway-api-key-newer-success-${++postBatchSequence}`
    newerSuccessBatchIds.push(batchId)
    const result = await postSingleChatCompletion(backendBaseUrl, delayedFailureRaceGatewayApiKey, batchId, {
      content: `newer protocol success on formerly failing fingerprint ${index + 1}`,
      model: 'gpt-4.1'
    })
    assert.equal(result.status, 200, `并发较新请求必须成功：${result.text}`)
    if (authorizationsForBatches([batchId]).includes('Bearer sk-gateway-race-bad')) break
  }
  assert(
    authorizationsForBatches(newerSuccessBatchIds).includes('Bearer sk-gateway-race-bad'),
    '释放旧确认前必须先取得同 fingerprint 的较新协议成功 observation'
  )
  releaseHeldMockSuccessResponses(delayedFailureBatchId)
  const delayedFailureResult = await delayedFailureRequest
  assert.equal(delayedFailureResult.status, 200, `旧请求最终应由兄弟 Key 成功完成：${delayedFailureResult.text}`)
  assert.deepEqual(
    authorizationsForBatches([delayedFailureBatchId]),
    ['Bearer sk-gateway-race-bad', 'Bearer sk-gateway-race-good'],
    '迟到确认场景必须先发生旧 transport failure，再由兄弟 Key 完成协议响应'
  )
  const postRaceBatchIds: string[] = []
  for (let index = 0; index < 2; index += 1) {
    const batchId = `gateway-api-key-post-race-${++postBatchSequence}`
    postRaceBatchIds.push(batchId)
    const result = await postSingleChatCompletion(backendBaseUrl, delayedFailureRaceGatewayApiKey, batchId, {
      content: `stale failure must not suppress newer success ${index + 1}`
    })
    assert.equal(result.status, 200, `迟到失败 fencing 后请求必须成功：${result.text}`)
    if (authorizationsForBatches([batchId]).includes('Bearer sk-gateway-race-bad')) break
  }
  assert(
    authorizationsForBatches(postRaceBatchIds).includes('Bearer sk-gateway-race-bad'),
    '迟到旧 failure 不得覆盖较新协议成功或重新短避让该 fingerprint'
  )
  assert.equal(apiKeyRuntimeStateStatus('sk-gateway-race-bad'), undefined, '迟到旧 failure 不得写 Key 持久状态')
  assertNoOpenAccountCircuit(delayedFailureRaceAccountId, '迟到 Key 局部 failure 不得误熔断整个账户')

  const statusChaosBatch = await postChatCompletions(backendBaseUrl, statusChaosGatewayApiKey, 1)
  const statusChaosAuthorizations = authorizationsForBatches([statusChaosBatch])
  assert.deepEqual(
    statusChaosAuthorizations,
    statusChaosKeys.map((key) => `Bearer ${key}`),
    '400/401/429/500/503 不得触发内部语义分流；六 Key 应按请求内顺序唯一尝试直到健康 Key'
  )
  assert(statusChaosKeys.slice(0, -1).every((key) => apiKeyRuntimeStateStatus(key) === undefined), '混乱状态码不得写入共享 Key 状态')
  assert.deepEqual(accountDatabaseAvailabilityByName('六 Key 状态码乱序 账户'), { status: 'active', schedulable: 1 }, '混乱状态码不得改变账户状态')

  const physicalKeyDedupeBatch = await postChatCompletions(backendBaseUrl, physicalKeyDedupeScenario, 1)
  assert.deepEqual(
    authorizationsForBatches([physicalKeyDedupeBatch]),
    [
      'Bearer sk-gateway-physical-shared-bad',
      'Bearer sk-gateway-physical-a-bad',
      'Bearer sk-gateway-physical-b-good'
    ],
    '跨账户重复物理 Key 在同一请求内只能命中一次，去重后仍必须继续第二账户的其他健康 Key'
  )

  const allDeadBatchId = `gateway-api-key-all-dead-${++postBatchSequence}`
  const allDeadResult = await postSingleChatCompletion(backendBaseUrl, allDeadGatewayApiKey, allDeadBatchId, {
    content: 'all five keys fail with unrelated upstream status codes'
  })
  assert.equal(allDeadResult.status, 503, `五 Key 全坏应返回网关稳定 503：${allDeadResult.text}`)
  assert(allDeadResult.text.includes('upstream_retryable_error'), `五 Key 全坏应返回稳定客户端可重试错误：${allDeadResult.text}`)
  assert.deepEqual(
    authorizationsForBatches([allDeadBatchId]),
    allDeadKeys.map((key) => `Bearer ${key}`),
    '五 Key 全坏也必须逐个且仅一次命中全部当前可调度 Key'
  )
  assert(allDeadKeys.every((key) => apiKeyRuntimeStateStatus(key) === undefined), '全坏请求仍不得写入共享 Key 死亡状态')
  assert.deepEqual(accountDatabaseAvailabilityByName('五 Key 全坏稳定失败 账户'), { status: 'active', schedulable: 1 }, '单请求全 Key 失败不得把账户写死')

  const oversizedPoolBatchId = `gateway-api-key-oversized-${++postBatchSequence}`
  const oversizedPoolResult = await postSingleChatCompletion(backendBaseUrl, oversizedPoolGatewayApiKey, oversizedPoolBatchId, {
    content: 'oversized key pool must stop at the explicit per-request safety limit'
  })
  assert.equal(oversizedPoolResult.status, 503, `超大 Key 池安全预算耗尽应返回稳定 503：${oversizedPoolResult.text}`)
  assert(oversizedPoolResult.text.includes('upstream_retryable_error'), `超大 Key 池预算耗尽应返回稳定客户端重试错误：${oversizedPoolResult.text}`)
  const oversizedPoolAuthorizations = authorizationsForBatches([oversizedPoolBatchId])
  assert.equal(oversizedPoolAuthorizations.length, 64, `超大 Key 池单请求应严格受 64 次唯一 Key 尝试上限保护：${oversizedPoolAuthorizations.length}`)
  assert.equal(new Set(oversizedPoolAuthorizations).size, 64, '超大 Key 池达到安全上限前，每个 Key 仍必须最多命中一次')
  assert.deepEqual(
    oversizedPoolAuthorizations,
    oversizedPoolKeys.slice(0, 64).map((key) => `Bearer ${key}`),
    '安全上限必须按可调度顺序截断，不能重复 Key 或偷偷宣称最后两个未尝试 Key 已穷尽'
  )
  assert(oversizedPoolKeys.every((key) => apiKeyRuntimeStateStatus(key) === undefined), '超大池预算截断不得写入任何共享 Key 死亡状态')

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
      'Bearer sk-gateway-allbad-b',
      'Bearer sk-gateway-allbad-c',
      'Bearer sk-gateway-allbad-rescue',
      'Bearer sk-gateway-allbad-b',
      'Bearer sk-gateway-allbad-c',
      'Bearer sk-gateway-allbad-a',
      'Bearer sk-gateway-allbad-rescue',
      'Bearer sk-gateway-allbad-c',
      'Bearer sk-gateway-allbad-a',
      'Bearer sk-gateway-allbad-b',
      'Bearer sk-gateway-allbad-rescue'
    ],
    '独立坏请求不得继承前次 Key 失败；400/401/429 等未知状态码均应在请求内唯一穷尽全部当前 Key 后才切后备'
  )
  assert.equal(
    allBadAuthorizations.indexOf('Bearer sk-gateway-allbad-rescue'),
    3,
    '三个坏 Key 场景首个请求应唯一尝试全部三个 Key 后才切后备账户'
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
    strictThreeKeyExhaustion: threeKeyAuthorizations,
    transportFailureThenHealthyKey: authorizationsForBatches([transportRecoveryBatchId]),
    confirmedTransportIsolation: transportIsolationAuthorizations,
    recoveredFingerprintReincluded: authorizationsForBatches(transportRecoveredBatchIds),
    delayedFailureFencedByNewerSuccess: authorizationsForBatches(postRaceBatchIds),
    concurrentBadSessionRequests: concurrentBadSessionResults.length,
    sixKeyStatusChaos: statusChaosAuthorizations,
    physicalKeyDedupe: authorizationsForBatches([physicalKeyDedupeBatch]),
    allDeadWithoutFallbackAttempts: authorizationsForBatches([allDeadBatchId]).length,
    oversizedPoolUniqueAttemptsBeforeSafetyCutoff: oversizedPoolAuthorizations.length,
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
  releaseAllHeldMockSuccessResponses()
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
  concurrencyLimit?: number
  supportedModels?: string[]
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
    schedulable: true,
    concurrencyLimit: input.concurrencyLimit,
    ...(input.supportedModels ? { supportedModels: input.supportedModels } : {})
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

function createGatewayOversizedPoolScenario(upstreamBaseUrl: string): { gatewayApiKey: string; apiKeys: string[] } {
  const group = repositories.createGroup({
    name: '跨账户超大 Key 池请求安全上限分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const firstAccountKeys = Array.from({ length: 50 }, (_, index) => `sk-gateway-oversized-a-${String(index + 1).padStart(2, '0')}`)
  const secondAccountKeys = Array.from({ length: 16 }, (_, index) => `sk-gateway-oversized-b-${String(index + 1).padStart(2, '0')}`)
  for (const [name, apiKeys] of [
    ['A 超大 Key 池第一账户', firstAccountKeys],
    ['B 超大 Key 池第二账户', secondAccountKeys]
  ] as const) {
    createActiveAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name,
      type: 'api_key',
      credentials: {
        api_key: apiKeys[0],
        api_keys: apiKeys,
        api_key_strategy: 'round_robin',
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      concurrencyLimit: 64
    }, access)
  }
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '跨账户超大 Key 池请求安全上限网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '跨账户超大 Key 池场景未返回网关 API Key 明文')
  return { gatewayApiKey: apiKey.key, apiKeys: [...firstAccountKeys, ...secondAccountKeys] }
}

function createGatewayPhysicalKeyDedupeScenario(upstreamBaseUrl: string): string {
  const group = repositories.createGroup({
    name: '跨账户重复物理 Key 去重分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  for (const [name, apiKeys] of [
    ['A 重复物理 Key 来源账户', ['sk-gateway-physical-shared-bad', 'sk-gateway-physical-a-bad']],
    ['B 重复物理 Key 接管账户', ['sk-gateway-physical-shared-bad', 'sk-gateway-physical-b-good']]
  ] as const) {
    createActiveAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name,
      type: 'api_key',
      credentials: {
        api_key: apiKeys[0],
        api_keys: [...apiKeys],
        api_key_strategy: 'round_robin',
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      priority: 0
    }, access)
  }
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '跨账户重复物理 Key 去重网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '跨账户重复物理 Key 去重场景未返回网关 API Key 明文')
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
      account_id: 'acct-gateway-oauth-non-isolation-bad',
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
  forcedFailureStatusByAuthorization.set('Bearer sk-gateway-authorized-bad', 429)
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
  forcedFailureStatusByAuthorization.set('Bearer sk-gateway-allbad-a', 400)
  forcedFailureStatusByAuthorization.set('Bearer sk-gateway-allbad-b', 401)
  forcedFailureStatusByAuthorization.set('Bearer sk-gateway-allbad-c', 429)
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
  forcedFailureStatusByAuthorization.set('Bearer sk-gateway-fallback-bad-primary', 503)
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
  const configuredSupportedModels = Array.isArray(input.supportedModels)
    ? input.supportedModels.filter((model): model is string => typeof model === 'string' && model.trim().length > 0)
    : []
  const supportedModels = configuredSupportedModels.length > 0
    ? configuredSupportedModels
    : ['gpt-5.5']
  const account = repositories.createAccount({
    ...input,
    supportedModels
  }, accountAccess)
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
    const result = await postSingleChatCompletion(backendBaseUrl, apiKey, batchId, {
      content: `mock gateway api key rotation ${index + 1}`
    })
    assert.equal(result.status, 200, `网关请求应成功，实际 HTTP ${result.status}: ${result.text}`)
  }
  return batchId
}

async function postSingleChatCompletion(
  backendBaseUrl: string,
  apiKey: string,
  batchId: string,
  input: { content: string; model?: string; sessionId?: string }
): Promise<{ status: number; text: string }> {
  const response = await fetch(`${backendBaseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      'user-agent': batchUserAgent(batchId)
    },
    body: JSON.stringify({
      model: input.model ?? 'gpt-5.5',
      messages: [{ role: 'user', content: input.content }],
      stream: false,
      ...(input.sessionId ? { session_id: input.sessionId } : {})
    })
  })
  return { status: response.status, text: await response.text() }
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
      if (forcedTransportFailureAuthorizations.has(authorization)) {
        req.socket.destroy()
        return
      }
      const failureStatus = failureStatusForAuthorization(authorization)
      if (failureStatus !== undefined) {
        sendJsonError(res, failureStatus, 'mock upstream key unavailable')
        return
      }
      const userAgent = String(req.headers['user-agent'] ?? '')
      if (heldSuccessResponseUserAgents.has(userAgent)) {
        const releases = heldSuccessResponseReleases.get(userAgent) ?? []
        releases.push(() => sendMockSuccess(res, requestPath))
        heldSuccessResponseReleases.set(userAgent, releases)
        return
      }
      sendMockSuccess(res, requestPath)
    })
  })
}

function sendMockSuccess(res: http.ServerResponse, requestPath: string): void {
  if (res.destroyed || res.writableEnded) return
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify(successPayloadForPath(requestPath)))
}

function holdMockSuccessResponses(batchId: string): void {
  heldSuccessResponseUserAgents.add(batchUserAgent(batchId))
}

function releaseHeldMockSuccessResponses(batchId: string): void {
  const userAgent = batchUserAgent(batchId)
  heldSuccessResponseUserAgents.delete(userAgent)
  const releases = heldSuccessResponseReleases.get(userAgent) ?? []
  heldSuccessResponseReleases.delete(userAgent)
  for (const release of releases) release()
}

function releaseAllHeldMockSuccessResponses(): void {
  heldSuccessResponseUserAgents.clear()
  for (const releases of heldSuccessResponseReleases.values()) {
    for (const release of releases) release()
  }
  heldSuccessResponseReleases.clear()
}

function failureStatusForAuthorization(authorization: string): number | undefined {
  if (authorization === 'Bearer sk-gateway-failover-bad') {
    return failoverBadKeyRecovered ? undefined : 401
  }
  return forcedFailureStatusByAuthorization.get(authorization)
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

function accountDatabaseAvailabilityByName(name: string): { status: string; schedulable: number } | undefined {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT status, schedulable FROM accounts WHERE name = ? AND deleted_at IS NULL LIMIT 1')
    .get(name) as unknown as { status: string; schedulable: number } | undefined
  return row ? { status: row.status, schedulable: row.schedulable } : undefined
}

function accountIdByName(name: string): string {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT id FROM accounts WHERE name = ? AND deleted_at IS NULL LIMIT 1')
    .get(name) as unknown as { id?: string } | undefined
  assert(row?.id, `缺少 Mock AI 测试账户：${name}`)
  return row.id
}

function assertNoOpenAccountCircuit(accountId: string, message: string): void {
  const row = databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT COUNT(*) AS count
      FROM account_circuit_incidents
      WHERE account_id = ?
        AND scope_kind = 'account'
        AND state <> 'CLOSED'
    `)
    .get(accountId) as unknown as { count: number }
  assert.equal(Number(row.count), 0, message)
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
  for (let attempt = 0; attempt < 30; attempt += 1) {
    try {
      rmSync(path, { recursive: true, force: true })
      return
    } catch (error) {
      const code = error && typeof error === 'object' && 'code' in error
        ? (error as { code?: unknown }).code
        : undefined
      if (code !== 'EBUSY' && code !== 'EPERM') throw error
      lastError = error
      await sleep(250)
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
