import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { GatewaySettings } from '../../modules/gateway/policy/account-error-policy.service.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import type { StreamFailureContext } from '../../modules/gateway/response/stream.js'
import {
  clearGatewayAccountApiKeyFailureGuardsForTest,
  getGatewayAccountApiKeyFailureGuardSnapshotForTest
} from '../../modules/gateway/runtime/account-api-key-failure-guard.service.js'
import {
  clearGatewayAccountSideEffectQueueForTest,
  clearGatewayLocalAccountSuppressionsForTest,
  getGatewayAccountSideEffectState,
  snapshotGatewayAccountRuntimeAvailability
} from '../../modules/gateway/runtime/account-side-effects.service.js'
import { handleStreamFailure } from '../../modules/gateway/runtime/account-effects.js'
import {
  clearGatewayProxyHealthForTest,
  recordGatewayUpstreamBucketSuccess
} from '../../modules/gateway/runtime/proxy-health.service.js'
import type { GatewayUsageContext } from '../../modules/gateway/usage/records.js'
import type { OpenAIGatewayTrafficSource } from '../../modules/gateway/usage/traffic-source.js'

interface Scenario {
  name: string
  client: 'generic' | 'precise'
  failure: 'protocol_failure' | 'missing_terminal' | 'read_incomplete'
  context: StreamFailureContext
  trafficSource: OpenAIGatewayTrafficSource
  selectedApiKeyFingerprint?: string
  accountStateMutationEnabled?: boolean
}

const settings: GatewaySettings = {
  gatewayTextRawBodyLimitMegabytes: 16,
  accountCircuitConfirmationFailuresRequired: 2,
  defaultTemporaryUnschedulableMinutes: 5,
  temporaryUnschedulableRetryIntervalSeconds: 30,
  temporaryUnschedulableRetryAttempts: 3,
  streamCircuitBreakerEnabled: true,
  textFirstResponseTimeoutSeconds: 30,
  textStreamIdleTimeoutSeconds: 60,
  textUncommittedAttemptMaxLifetimeSeconds: 90,
  imageFirstResponseTimeoutSeconds: 60,
  imageStreamIdleTimeoutSeconds: 120,
  imageUncommittedAttemptMaxLifetimeSeconds: 180,
  noAvailableAccountWaitTimeoutSeconds: 10,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 5
}

const scenarios: Scenario[] = [
  {
    name: 'precise 协议失败事件，输出前，Key A',
    client: 'precise',
    failure: 'protocol_failure',
    context: { downstreamBytesWritten: 0, outputReceived: false, protocolFailureEventReceived: true },
    trafficSource: 'gateway',
    selectedApiKeyFingerprint: 'key-a'
  },
  {
    name: 'precise 协议失败事件，输出后，Key B',
    client: 'precise',
    failure: 'protocol_failure',
    context: { downstreamBytesWritten: 512, outputReceived: true, protocolFailureEventReceived: true },
    trafficSource: 'gateway',
    selectedApiKeyFingerprint: 'key-b'
  },
  {
    name: 'generic 不解释供应商 response.failed，输出前',
    client: 'generic',
    failure: 'protocol_failure',
    context: { downstreamBytesWritten: 0, outputReceived: false, protocolFailureEventReceived: false },
    trafficSource: 'gateway'
  },
  {
    name: 'generic 缺终止事件，输出前',
    client: 'generic',
    failure: 'missing_terminal',
    context: { downstreamBytesWritten: 0, outputReceived: false, protocolFailureEventReceived: false },
    trafficSource: 'gateway',
    selectedApiKeyFingerprint: 'key-a'
  },
  {
    name: 'precise 缺终止事件，输出后',
    client: 'precise',
    failure: 'missing_terminal',
    context: { downstreamBytesWritten: 1024, outputReceived: true, protocolFailureEventReceived: false },
    trafficSource: 'gateway',
    selectedApiKeyFingerprint: 'key-b'
  },
  {
    name: 'generic read incomplete，输出前',
    client: 'generic',
    failure: 'read_incomplete',
    context: { downstreamBytesWritten: 0, outputReceived: false, protocolFailureEventReceived: false },
    trafficSource: 'gateway'
  },
  {
    name: 'precise read incomplete，输出后',
    client: 'precise',
    failure: 'read_incomplete',
    context: { downstreamBytesWritten: 2048, outputReceived: true, protocolFailureEventReceived: false },
    trafficSource: 'gateway',
    selectedApiKeyFingerprint: 'key-a'
  },
  {
    name: '人工诊断协议失败',
    client: 'precise',
    failure: 'protocol_failure',
    context: { downstreamBytesWritten: 128, outputReceived: true, protocolFailureEventReceived: true },
    trafficSource: 'manual_account_test',
    selectedApiKeyFingerprint: 'key-a'
  },
  {
    name: '健康检查缺终止事件',
    client: 'generic',
    failure: 'missing_terminal',
    context: { downstreamBytesWritten: 64, outputReceived: false, protocolFailureEventReceived: false },
    trafficSource: 'account_health_check',
    selectedApiKeyFingerprint: 'key-b'
  },
  {
    name: '状态写入已由调用方关闭',
    client: 'precise',
    failure: 'read_incomplete',
    context: { downstreamBytesWritten: 0, outputReceived: false, protocolFailureEventReceived: false },
    trafficSource: 'runtime_recovery_probe',
    selectedApiKeyFingerprint: 'key-a',
    accountStateMutationEnabled: false
  }
]

clearGatewayProxyHealthForTest()
clearGatewayAccountApiKeyFailureGuardsForTest()
clearGatewayLocalAccountSuppressionsForTest()
clearGatewayAccountSideEffectQueueForTest()

for (const [index, scenario] of scenarios.entries()) {
  const account = mockAccount(scenario.selectedApiKeyFingerprint)
  const usageContext = mockUsageContext(index, scenario.trafficSource)
  const accountStateBefore = snapshotGatewayAccountRuntimeAvailability()
  const keyStateBefore = getGatewayAccountApiKeyFailureGuardSnapshotForTest()
  const queueStateBefore = getGatewayAccountSideEffectState()

  await handleStreamFailure(
    account,
    `${scenario.name}；供应商不可信错误文本`,
    settings,
    `vendor_${scenario.client}_${scenario.failure}_400_401_429_500_503`,
    scenario.context,
    usageContext,
    scenario.accountStateMutationEnabled
  )

  assert.deepEqual(
    snapshotGatewayAccountRuntimeAvailability(),
    accountStateBefore,
    `${scenario.name} 不得创建账户本地抑制、precheck 或恢复状态`
  )
  assert.deepEqual(
    getGatewayAccountApiKeyFailureGuardSnapshotForTest(),
    keyStateBefore,
    `${scenario.name} 不得写账户内 API Key transient 状态`
  )
  assert.deepEqual(
    getGatewayAccountSideEffectState(),
    queueStateBefore,
    `${scenario.name} 不得入队任何账户持久副作用`
  )
  assert.equal(
    recordGatewayUpstreamBucketSuccess(account),
    false,
    `${scenario.name} 不得写 proxy/upstream bucket health`
  )
}

const accountEffectsSource = readFileSync(
  resolve(dirname(fileURLToPath(import.meta.url)), '../../modules/gateway/runtime/account-effects.ts'),
  'utf8'
)
const handleStreamFailureSource = sourceBetween(
  accountEffectsSource,
  'export async function handleStreamFailure(',
  '\nexport function clearAccountStreamFailureStateWithCacheInvalidation('
)
const handleStreamFailureBody = handleStreamFailureSource.slice(
  handleStreamFailureSource.indexOf('): Promise<void> {') + '): Promise<void> {'.length
)

assert.doesNotMatch(
  handleStreamFailureBody,
  /recordGateway|suppressGateway|enqueueGateway|applyAccountErrorHandling|requestGatewayDbService|clearGatewayRuntimeCache|hotQuality|qualityStore|recordTerminal/,
  '普通/诊断流失败入口不得写账户、Key、proxy、质量、避让或持久共享状态'
)
assert.doesNotMatch(
  handleStreamFailureBody,
  /response\.failed|protocolFailureEventReceived|outputReceived|downstreamBytesWritten|errorCode|reason\s*[)=]|temporary_unavailable|rate_limited|disabled/,
  '流失败入口不得按供应商事件、错误码/文本、终止事件或输出位置推断共享状态'
)

console.log(`流式失败共享状态边界回归通过：${scenarios.length} 个普通/诊断、多 Key、客户端与输出阶段场景均保持共享状态不变`)

function mockAccount(selectedApiKeyFingerprint?: string): UpstreamAccount {
  return {
    id: 'stream-boundary-multi-key-account',
    name: 'stream-boundary-multi-key-account',
    systemAccountId: 'stream-boundary-owner',
    accountOwnerSystemAccountId: 'stream-boundary-owner',
    providerCode: 'openai',
    providerProtocolProfileId: 'openai_v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    baseUrl: 'https://untrusted-provider.example/v1',
    type: 'api_key',
    proxyProfileId: 'stream-boundary-proxy',
    selectedApiKeyFingerprint
  } as unknown as UpstreamAccount
}

function mockUsageContext(index: number, trafficSource: OpenAIGatewayTrafficSource): GatewayUsageContext {
  return {
    traceId: `stream-boundary-trace-${index}`,
    trafficSource,
    clientIp: `198.51.100.${index + 1}`,
    systemAccountId: 'stream-boundary-owner',
    apiKeyId: `gateway-key-${index % 2}`,
    groupId: 'stream-boundary-group',
    endpoint: '/v1/responses',
    requestSnapshot: {}
  } as GatewayUsageContext
}

function sourceBetween(source: string, startMarker: string, endMarker: string): string {
  const start = source.indexOf(startMarker)
  const end = source.indexOf(endMarker, start)
  assert(start >= 0 && end > start, '无法定位 handleStreamFailure 源码边界')
  return source.slice(start, end)
}
