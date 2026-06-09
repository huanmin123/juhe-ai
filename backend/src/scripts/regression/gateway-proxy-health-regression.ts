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
  setGatewayProxyHealthNowForTest
} from '../../modules/gateway/openai-gateway-proxy-health.service.js'

logger.level = 'silent'
clearGatewayProxyHealthForTest()

testProxyBucket()
testProxyUrlBucketMetadataRedaction()
testBaseUrlBucket()
testBaseUrlBucketHalfOpen()

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
  assert(serialized.includes('proxy-user'), '代理 URL 桶元数据应保留代理用户名')
  assert(serialized.includes('proxy-pass'), '代理 URL 桶元数据应保留代理密码')
  assert(serialized.includes('proxy:url:http://proxy-user:proxy-pass@127.0.0.1:18080'), '代理 URL 桶元数据应保留代理 URL 原文')

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

  assert.equal(recordGatewayProxySuccess(first), true, '任一桶内账号成功应清理该账号关联的上游桶')
  order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
  assert.equal(order.applied, false, '上游桶成功恢复后不应继续避让')

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
  assert.deepEqual(order.accounts.map((item) => item.id), [first.id, third.id], '半开探测账号应优先获得一次恢复探测机会，其他同桶账号不进入本轮尝试')

  const decision = recordGatewayUpstreamBucketFailure(first, 'half_open_probe_failed')
  assert.equal(decision.suspected, true, '半开探测失败应直接重新打开上游桶，不重新等待多账号阈值')

  order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
  assert.equal(order.applied, true, '半开失败后应回到避让状态')
  assert.deepEqual(order.accounts.map((item) => item.id), [third.id, first.id, second.id], '半开失败后应再次优先使用其他上游桶')

  assert.equal(recordGatewayProxySuccess(first), true, '半开探测成功路径应能清理上游桶状态')
  order = orderOpenAIAccountsByGatewayProxyHealth([first, second, third])
  assert.equal(order.applied, false, '成功清理后不应继续避让')

  clearGatewayProxyHealthForTest()
}

function account(id: string, proxyProfileId: string | undefined, baseUrl = 'https://example.invalid/v1', proxyUrl?: string): OpenAIAccountSecret {
  return {
    id,
    systemAccountId: 'sys_admin',
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
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    openAIResponsesUpstreamMode: 'passthrough',
    schedulable: true,
    proxyProfileId,
    proxyUrl,
    accountOwnerSystemAccountId: 'sys_admin',
    groupOwnerSystemAccountId: 'sys_admin',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    streamFailureCount: 0
  } as OpenAIAccountSecret
}
