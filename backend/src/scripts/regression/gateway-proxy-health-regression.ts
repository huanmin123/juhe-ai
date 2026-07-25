import { strict as assert } from 'node:assert'

import { logger } from '../../shared/logger.js'
import { GPT_OPENAI_V1_PROFILE_ID, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '../../domain/provider-protocol.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'
import {
  clearGatewayProxyHealthForTest,
  orderOpenAIAccountsByGatewayProxyHealth,
  recordGatewayProxyFailure,
  recordGatewayProxySuccess,
  recordGatewayUpstreamBucketFailure,
  recordGatewayUpstreamBucketSuccess,
  suppressGatewayUpstreamBucketLocallyForSeconds,
  setGatewayProxyHealthNowForTest
} from '../../modules/gateway/runtime/proxy-health.service.js'

logger.level = 'silent'
clearGatewayProxyHealthForTest()

testProxyBucket()
testProxyUrlBucketMetadataRedaction()
testBaseUrlBucket()
testProxySuccessDoesNotClearUpstreamBuckets()
testBaseUrlBucketOwnerIsolation()
testAuthorizedInstancesDoNotForgeDistinctFailureEvidence()
testBaseUrlBucketHalfOpen()
testMemoryAvoidDeadlineAndExpiryDoNotShrink()
testPriorityBoundary()
testModelPriorityBoundary()

clearGatewayProxyHealthForTest()

console.log('上游桶运行态失败回归通过：同代理或同 baseUrl 多账号失败才避让，TTL 到期半开探测，成功恢复，全部命中时旁路保证可用性')

function testProxyBucket(): void {
  const first = account('account-proxy-1', 'proxy-shared')
  const second = account('account-proxy-2', 'proxy-shared')
  const third = account('account-proxy-3', 'proxy-other')

  let order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
  assert.equal(order.applied, false, '初始状态不应应用代理避让')
  assert.deepEqual(order.accounts.map((item) => item.id), [first.id, second.id, third.id], '初始排序应保持原样')

  let decision = recordGatewayProxyFailure(first, 'ECONNRESET')
  assert.equal(decision.recorded, true, '绑定代理的账号失败应记录代理桶')
  assert.equal(decision.suspected, false, '单账号代理失败不能直接判定代理问题')
  order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
  assert.equal(order.applied, false, '单账号代理失败不应避让整个代理')

  decision = recordGatewayProxyFailure(second, 'ECONNRESET')
  assert.equal(decision.suspected, true, '同代理两个不同账号短窗失败应判定代理可疑')
  order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
  assert.equal(order.applied, true, '存在其他代理可用账号时应应用代理避让')
  assert.deepEqual(order.accounts.map((item) => item.id), [third.id, first.id, second.id], '可疑代理绑定账号应被排到后面')
  assert.deepEqual(order.avoidedAccountIds.sort(), [first.id, second.id].sort(), '应避让共享可疑代理的账号')

  assert.equal(recordGatewayProxySuccess(first), true, '任一绑定账号成功应清理代理运行态失败桶')
  order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
  assert.equal(order.applied, false, '代理成功后不应继续避让')

  recordGatewayProxyFailure(first, 'ECONNRESET')
  recordGatewayProxyFailure(second, 'ECONNRESET')
  order = orderOpenAIAccountsByGatewayProxyHealth([first, second])
  assert.equal(order.applied, false, '所有候选账号都被代理桶命中时不应清空候选')
  assert.equal(order.bypassedAllAvoided, true, '所有候选都被避让时应旁路以保证可用性')
  assert.deepEqual(order.accounts.map((item) => item.id), [first.id, second.id], '旁路时应保持原候选顺序')

  clearGatewayProxyHealthForTest()
}

function testProxyUrlBucketMetadataRedaction(): void {
  const proxyUrl = 'http://proxy-user:proxy-pass@127.0.0.1:18080'
  const first = account('account-proxy-url-1', undefined, 'https://proxy-url-upstream.example/v1', proxyUrl)
  const second = account('account-proxy-url-2', undefined, 'https://proxy-url-upstream.example/v1', proxyUrl)
  const third = account('account-proxy-url-3', undefined, 'https://proxy-url-other.example/v1', 'http://proxy-user:other-pass@127.0.0.1:18081')

  recordGatewayProxyFailure(first, 'ECONNRESET')
  const decision = recordGatewayProxyFailure(second, 'ECONNRESET')
  const order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
  const serialized = JSON.stringify({
    decision,
    avoidedBucketKeys: order.avoidedBucketKeys,
    avoidedProxyKeys: order.avoidedProxyKeys,
    halfOpenBucketKeys: order.halfOpenBucketKeys
  })
  assert(decision.proxyKey?.startsWith('proxy:url:'), '代理 URL 桶元数据应保留代理 URL 桶类型')
  assert(!serialized.includes('proxy-user'), '代理 URL 桶元数据不应泄露代理用户名')
  assert(!serialized.includes('proxy-pass'), '代理 URL 桶元数据不应泄露代理密码')
  assert(!serialized.includes(proxyUrl), '代理 URL 桶元数据不应保留代理 URL 原文')

  clearGatewayProxyHealthForTest()
}

function testBaseUrlBucket(): void {
  const first = account('account-base-url-1', 'proxy-a', 'https://shared-upstream.example/v1')
  const second = account('account-base-url-2', 'proxy-b', 'https://shared-upstream.example')
  const third = account('account-base-url-3', 'proxy-c', 'https://other-upstream.example/v1')

  let order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
  assert.equal(order.applied, false, '初始状态不应应用上游桶避让')

  let decision = recordGatewayUpstreamBucketFailure(first, 'upstream_response_failed')
  assert.equal(decision.suspected, false, '单账号上游失败不能直接判定 baseUrl 问题')

  decision = recordGatewayUpstreamBucketFailure(second, 'upstream_response_failed')
  assert.equal(decision.suspected, true, '同 baseUrl 两个不同账号短窗失败应判定上游桶可疑')
  assert(decision.suspectedBucketKeys?.some((key) => key.startsWith('baseUrl:https://shared-upstream.example/v1')), '应打开共享 baseUrl 桶')

  order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
  assert.equal(order.applied, true, '存在其他 baseUrl 可用账号时应应用上游桶避让')
  assert.deepEqual(order.accounts.map((item) => item.id), [third.id, first.id, second.id], '可疑 baseUrl 账号应被排到后面')
  assert(order.avoidedBucketKeys.some((key) => key.startsWith('baseUrl:https://shared-upstream.example/v1')), '排序元数据应包含被避让的 baseUrl 桶')

  assert.equal(recordGatewayUpstreamBucketSuccess(first), true, '完整上游成功应清理该账号关联的上游桶')
  order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
  assert.equal(order.applied, false, '上游桶成功恢复后不应继续避让')

  clearGatewayProxyHealthForTest()
}

function testProxySuccessDoesNotClearUpstreamBuckets(): void {
  clearGatewayProxyHealthForTest()
  const first = account('account-proxy-success-scope-1', 'proxy-success-scope-a', 'https://proxy-success-scope.example/v1')
  const second = account('account-proxy-success-scope-2', 'proxy-success-scope-b', 'https://proxy-success-scope.example/v1')
  const fallback = account('account-proxy-success-scope-fallback', 'proxy-success-scope-c', 'https://proxy-success-scope-fallback.example/v1')

  recordGatewayUpstreamBucketFailure(first, 'upstream_response_failed')
  recordGatewayUpstreamBucketFailure(second, 'upstream_response_failed')
  assert.equal(
    orderOpenAIAccountsByGatewayProxyHealth([first, second, fallback]).applied,
    true,
    '共享 Base URL 失败证据应先打开上游桶'
  )

  assert.equal(recordGatewayProxySuccess(first), true, '代理成功应清理该账号的代理桶诊断')
  assert.equal(
    orderOpenAIAccountsByGatewayProxyHealth([first, second, fallback]).applied,
    true,
    '仅代理成功不得误清 Base URL 或 Provider 失败证据'
  )
  assert.equal(recordGatewayUpstreamBucketSuccess(first, { bucketScope: 'upstream' }), true, '完整上游成功才可清理上游桶')
  assert.equal(
    orderOpenAIAccountsByGatewayProxyHealth([first, second, fallback]).applied,
    false,
    '上游作用域成功后应恢复正常排序'
  )

  clearGatewayProxyHealthForTest()
}

function testBaseUrlBucketOwnerIsolation(): void {
  clearGatewayProxyHealthForTest()
  const firstOwnerAccount = account('account-owner-a-1', 'proxy-owner-a', 'https://owner-isolated.example/v1', undefined, { ownerSystemAccountId: 'owner-a' })
  const secondOwnerAccount = account('account-owner-b-1', 'proxy-owner-b', 'https://owner-isolated.example/v1', undefined, { ownerSystemAccountId: 'owner-b' })
  const firstOwnerPeer = account('account-owner-a-2', 'proxy-owner-a-2', 'https://owner-isolated.example/v1', undefined, { ownerSystemAccountId: 'owner-a' })

  recordGatewayUpstreamBucketFailure(firstOwnerAccount, 'upstream_response_failed')
  const crossOwnerDecision = recordGatewayUpstreamBucketFailure(secondOwnerAccount, 'upstream_response_failed')
  assert.equal(crossOwnerDecision.suspected, false, '不同物理账户所有者不能共同打开同一 Base URL 桶')

  const sameOwnerDecision = recordGatewayUpstreamBucketFailure(firstOwnerPeer, 'upstream_response_failed')
  assert.equal(sameOwnerDecision.suspected, true, '同一物理账户所有者的多个账户仍应共同触发短期桶')

  const order = orderOpenAIAccountsByGatewayProxyHealth([firstOwnerAccount, secondOwnerAccount, firstOwnerPeer])
  assert.deepEqual(
    order.avoidedAccountIds.sort(),
    [firstOwnerAccount.id, firstOwnerPeer.id].sort(),
    '桶避让只能影响相同物理账户所有者的候选'
  )
  assert(order.accounts.some((item) => item.id === secondOwnerAccount.id), '其他所有者的账户必须保留且不受桶状态影响')

  clearGatewayProxyHealthForTest()
}

function testAuthorizedInstancesDoNotForgeDistinctFailureEvidence(): void {
  clearGatewayProxyHealthForTest()
  const firstAuthorized = account(
    'account-authorized-instance-a',
    'proxy-authorized-shared',
    'https://authorized-shared.example/v1',
    undefined,
    { credentialSourceAccountId: 'physical-account-shared' }
  )
  const secondAuthorized = account(
    'account-authorized-instance-b',
    'proxy-authorized-shared',
    'https://authorized-shared.example/v1',
    undefined,
    { credentialSourceAccountId: 'physical-account-shared' }
  )
  const independent = account(
    'account-authorized-independent',
    'proxy-authorized-shared',
    'https://authorized-shared.example/v1',
    undefined,
    { credentialSourceAccountId: 'physical-account-independent' }
  )

  recordGatewayProxyFailure(firstAuthorized, 'first_authorized_failure')
  const duplicatePhysicalDecision = recordGatewayProxyFailure(secondAuthorized, 'second_authorized_failure')
  assert.equal(
    duplicatePhysicalDecision.suspected,
    false,
    '同一 credential source 的多个授权实例不能伪造成两个独立物理账号'
  )
  assert.equal(duplicatePhysicalDecision.distinctAccountCount, 1, '授权实例失败证据必须按物理凭据源去重')

  const independentDecision = recordGatewayProxyFailure(independent, 'independent_physical_failure')
  assert.equal(independentDecision.suspected, true, '第二个独立物理凭据源失败后才可达到共享代理阈值')
  assert.equal(independentDecision.distinctAccountCount, 2, '共享代理应统计两个独立物理凭据源')

  clearGatewayProxyHealthForTest()
}

function testBaseUrlBucketHalfOpen(): void {
  clearGatewayProxyHealthForTest()
  const startedAtMs = 10_000
  setGatewayProxyHealthNowForTest(startedAtMs)
  const first = account('account-half-open-1', 'proxy-half-open-a', 'https://half-open-upstream.example/v1')
  const second = account('account-half-open-2', 'proxy-half-open-b', 'https://half-open-upstream.example')
  const third = account('account-half-open-3', 'proxy-half-open-c', 'https://half-open-other.example/v1')

  recordGatewayUpstreamBucketFailure(first, 'upstream_response_failed')
  recordGatewayUpstreamBucketFailure(second, 'upstream_response_failed')

  let order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
  assert.equal(order.applied, true, '上游桶打开期间应避让同桶账号')
  assert.deepEqual(order.accounts.map((item) => item.id), [third.id, first.id, second.id], '打开期间应优先使用其他上游桶')

  setGatewayProxyHealthNowForTest(startedAtMs + 60_001)
  order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
  assert.equal(order.applied, true, '上游桶 TTL 到期后仍应保持单探针半开，而不是全量放行')
  assert.deepEqual(order.halfOpenAccountIds, [first.id], '半开阶段应只放行一个探测账号')
  assert(order.halfOpenBucketKeys.some((key) => key.startsWith('baseUrl:https://half-open-upstream.example/v1')), '半开元数据应包含共享 baseUrl 桶')
  assert.deepEqual(order.avoidedAccountIds, [second.id], '同桶其他账号应继续避让直到探针成功')
  assert.deepEqual(order.accounts.map((item) => item.id), [first.id, third.id, second.id], '半开探测账号应优先，但其他同桶账号仍必须保留为兜底候选')

  const decision = recordGatewayUpstreamBucketFailure(first, 'half_open_probe_failed')
  assert.equal(decision.suspected, true, '半开探测失败应直接重新打开上游桶，不重新等待多账号阈值')

  order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
  assert.equal(order.applied, true, '半开失败后应回到避让状态')
  assert.deepEqual(order.accounts.map((item) => item.id), [third.id, first.id, second.id], '半开失败后应再次优先使用其他上游桶')

  assert.equal(recordGatewayUpstreamBucketSuccess(first), true, '半开探测成功路径应能清理上游桶状态')
  order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
  assert.equal(order.applied, false, '成功清理后不应继续避让')

  clearGatewayProxyHealthForTest()
}

function testMemoryAvoidDeadlineAndExpiryDoNotShrink(): void {
  clearGatewayProxyHealthForTest()
  const startedAtMs = 100_000
  const first = account('account-memory-monotonic', 'proxy-memory-monotonic')
  const fallback = account('account-memory-monotonic-fallback', 'proxy-memory-monotonic-fallback')

  setGatewayProxyHealthNowForTest(startedAtMs)
  suppressGatewayUpstreamBucketLocallyForSeconds(first, 600, 'long_explicit_suppression', { bucketScope: 'proxy' })
  setGatewayProxyHealthNowForTest(startedAtMs + 1)
  recordGatewayProxyFailure(first, 'short_default_failure')
  suppressGatewayUpstreamBucketLocallyForSeconds(first, 1, 'short_explicit_suppression', { bucketScope: 'proxy' })

  setGatewayProxyHealthNowForTest(startedAtMs + 180_000)
  const order = orderOpenAIAccountsByGatewayProxyHealth([first, fallback])
  assert.equal(order.applied, true, '较短失败或 suppression 不得让长避让期限提前失效')
  assert.deepEqual(order.accounts.map((item) => item.id), [fallback.id, first.id], '内存 entry TTL 也不得被较短写入提前截断')

  clearGatewayProxyHealthForTest()
}

function testPriorityBoundary(): void {
  clearGatewayProxyHealthForTest()
  const first = account('account-priority-proxy-1', 'proxy-priority-shared', 'https://priority-shared.example/v1', undefined, { priority: 0 })
  const second = account('account-priority-proxy-2', 'proxy-priority-shared', 'https://priority-shared.example/v1', undefined, { priority: 0 })
  const lowPriorityFresh = account('account-priority-proxy-low', 'proxy-priority-other', 'https://priority-other.example/v1', undefined, { priority: 10 })

  recordGatewayProxyFailure(first, 'ECONNRESET')
  recordGatewayProxyFailure(second, 'ECONNRESET')
  const order = orderOpenAIAccountsByGatewayProxyHealth([first, second, lowPriorityFresh])
  assert.equal(order.applied, true, '代理桶避让命中时仍应报告排序规则已参与')
  assert.deepEqual(
    order.accounts.map((item) => item.id),
    [first.id, second.id, lowPriorityFresh.id],
    '上游桶避让不能让低优先级账号越过高优先级账号'
  )

  clearGatewayProxyHealthForTest()
}

function testModelPriorityBoundary(): void {
  clearGatewayProxyHealthForTest()
  const first = account('account-model-proxy-1', 'proxy-model-shared', 'https://model-shared.example/v1', undefined, { priority: 0 })
  const second = account('account-model-proxy-2', 'proxy-model-shared', 'https://model-shared.example/v1', undefined, { priority: 0 })
  const unrestrictedFresh = account('account-model-proxy-unrestricted', 'proxy-model-other', 'https://model-other.example/v1', undefined, { priority: 0 })

  recordGatewayProxyFailure(first, 'ECONNRESET')
  recordGatewayProxyFailure(second, 'ECONNRESET')
  const order = orderOpenAIAccountsByGatewayProxyHealth([first, second, unrestrictedFresh], {
    requestedModel: 'gpt-4.1',
    rankByAccountId: new Map([
      [first.id, 0],
      [second.id, 0],
      [unrestrictedFresh.id, 2]
    ])
  })
  assert.equal(order.applied, true, '代理桶避让命中时仍应报告排序规则已参与')
  assert.deepEqual(
    order.accounts.map((item) => item.id),
    [first.id, second.id, unrestrictedFresh.id],
    '上游桶避让不能让低模型匹配等级账号越过直连匹配账号'
  )

  clearGatewayProxyHealthForTest()
}

function account(
  id: string,
  proxyProfileId: string | undefined,
  baseUrl = 'https://example.invalid/v1',
  proxyUrl?: string,
  options: { priority?: number; ownerSystemAccountId?: string; credentialSourceAccountId?: string } = {}
): OpenAIAccountSecret {
  return {
    id,
    systemAccountId: options.ownerSystemAccountId ?? 'sys_admin',
    name: id,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    type: 'api_key',
    status: 'active',
    credentials: {},
    apiKey: 'sk-test',
    baseUrl,
    concurrencyLimit: 20,
    currentConcurrency: 0,
    priority: options.priority ?? 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    healthCheckEndpointMode: 'responses_sse',
    schedulable: true,
    proxyProfileId,
    proxyUrl,
    credentialSourceAccountId: options.credentialSourceAccountId,
    accountOwnerSystemAccountId: options.ownerSystemAccountId ?? 'sys_admin',
    groupOwnerSystemAccountId: options.ownerSystemAccountId ?? 'sys_admin',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    streamFailureCount: 0
  } as OpenAIAccountSecret
}
