import { strict as assert } from 'node:assert'
import http from 'node:http'

import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import type { GatewaySettings } from '../../modules/gateway/policy/account-error-policy.service.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import type { GatewayUsageContext } from '../../modules/gateway/usage/records.js'
import type { AuditCaptureContext } from '../../modules/gateway/audit/capture.service.js'

Object.assign(runtimeConfig.gateway, {
  automaticProbeMaxConcurrency: 1,
  usageFinalizationMaxItems: 128,
  usageFinalizationMaxConcurrency: 1
})
runtimeConfig.runtimeMode = 'standalone'
runtimeConfig.cacheDriver = 'memory'
runtimeConfig.runtimeStateDriver = 'memory'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true

const [upstreamDispatch, routeCoordination, serverRetryBudget, upstreamRequest, accountConcurrency] = await Promise.all([
  import('../../modules/gateway/dispatch/upstream-dispatch.js'),
  import('../../modules/gateway/routing/route-coordination.js'),
  import('../../modules/gateway/runtime/server-retry-budget.js'),
  import('../../modules/gateway/upstream/request.js'),
  import('../../shared/account-concurrency.js')
])

const settings = {
  gatewayTextRawBodyLimitMegabytes: 16,
  accountCircuitConfirmationFailuresRequired: 2,
  defaultTemporaryUnschedulableMinutes: 5,
  temporaryUnschedulableRetryIntervalSeconds: 0,
  temporaryUnschedulableRetryAttempts: 0,
  streamCircuitBreakerEnabled: true,
  textFirstResponseTimeoutSeconds: 30,
  textStreamIdleTimeoutSeconds: 60,
  textUncommittedAttemptMaxLifetimeSeconds: 90,
  imageFirstResponseTimeoutSeconds: 60,
  imageStreamIdleTimeoutSeconds: 120,
  imageUncommittedAttemptMaxLifetimeSeconds: 180,
  imageRequestWallTimeoutSeconds: 180,
  noAvailableAccountWaitTimeoutSeconds: 1,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 5
} as GatewaySettings

const request = {
  method: 'POST',
  path: '/v1/chat/completions',
  originalUrl: '/v1/chat/completions',
  headers: {},
  body: {
    model: 'gpt-5.5',
    messages: [{ role: 'user', content: 'opaque upstream failure must remain visible' }],
    stream: false
  },
  header: () => undefined
} as unknown as Request

const usageContext: GatewayUsageContext = {
  traceId: 'gateway-generic-upstream-opaque',
  trafficSource: 'gateway',
  systemAccountId: 'gateway-generic-upstream-opaque-owner',
  apiKeyId: 'gateway-generic-upstream-opaque-key',
  groupId: 'gateway-generic-upstream-opaque-group',
  endpoint: '/v1/chat/completions',
  requestSnapshot: {
    method: 'POST',
    path: '/v1/chat/completions',
    originalUrl: '/v1/chat/completions',
    traceId: 'gateway-generic-upstream-opaque',
    headers: {},
    body: request.body
  }
}

const auditCapture = {
  startAttempt: () => 'gateway-generic-upstream-opaque-attempt',
  completeAttempt() {},
  recordFailedDispatchAttempt() {},
  addGatewayMetadata() {},
  finalize() {}
} as unknown as AuditCaptureContext

const authorizations: string[] = []
const upstream = http.createServer((incoming, outgoing) => {
  incoming.resume()
  const authorization = String(incoming.headers.authorization ?? '')
  authorizations.push(authorization)
  if (authorization === 'Bearer sk-opaque-primary') {
    outgoing.writeHead(402, { 'content-type': 'application/json; charset=utf-8' })
    outgoing.end(JSON.stringify({
      error: {
        code: 'new_vendor_quota_state',
        message: '当前供应商返回未配置的额度失败'
      }
    }))
    return
  }
  outgoing.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  outgoing.end(JSON.stringify({
    id: 'must-not-be-reached',
    choices: [{ message: { role: 'assistant', content: 'hidden fallback' } }]
  }))
})

function account(id: string, apiKey: string, baseUrl: string): UpstreamAccount {
  return {
    id,
    name: id,
    systemAccountId: usageContext.systemAccountId,
    accountOwnerSystemAccountId: usageContext.systemAccountId,
    groupOwnerSystemAccountId: usageContext.systemAccountId,
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_openai_v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    type: 'api_key',
    status: 'active',
    schedulable: true,
    baseUrl,
    apiKey,
    apiKeys: [apiKey],
    credentials: { api_key: apiKey, base_url: baseUrl },
    concurrencyLimit: 1,
    priority: 0,
    supportedModels: ['gpt-5.5']
  } as unknown as UpstreamAccount
}

function requestCoordination(): import('../../modules/gateway/dispatch/upstream-dispatch.js').GatewayUpstreamRequestCoordinationContext {
  return {
    scope: 'gateway_request',
    serverRetryBudget: new serverRetryBudget.ServerRetryBudget(1_000),
    gatewayRequestWallBudget: new routeCoordination.GatewayRequestWallBudget({ requestAcceptedAtMs: Date.now() }),
    routeCoordinationBudget: new routeCoordination.RouteCoordinationBudget({ requestId: usageContext.traceId }),
    requestAttemptTracker: new routeCoordination.GatewayRequestAttemptTracker()
  }
}

await new Promise<void>((resolvePromise, rejectPromise) => {
  upstream.once('error', rejectPromise)
  upstream.listen(0, '127.0.0.1', () => resolvePromise())
})

const address = upstream.address()
assert(address && typeof address !== 'string', '测试上游必须监听 TCP 地址')
const baseUrl = `http://127.0.0.1:${address.port}/v1`
const primary = account('opaque-primary', 'sk-opaque-primary', baseUrl)
const fallback = account('opaque-fallback', 'sk-opaque-fallback', baseUrl)
let result: Awaited<ReturnType<typeof upstreamDispatch.fetchFirstAvailableUpstream>> | undefined

try {
  result = await upstreamDispatch.fetchFirstAvailableUpstream(
    request,
    [primary, fallback],
    settings,
    usageContext,
    auditCapture,
    undefined,
    new AbortController().signal,
    undefined,
    'text',
    undefined,
    false,
    undefined,
    undefined,
    undefined,
    false,
    requestCoordination(),
    false,
    false
  )
  assert.equal(result.account.id, primary.id, '未配置的完整 HTTP 失败必须保留实际失败账户')
  assert.equal(result.response.status, 402, '未配置的完整 HTTP 失败必须保留真实状态码')
  const chunks: Buffer[] = []
  for await (const chunk of result.response.body ?? []) {
    chunks.push(Buffer.from(chunk))
  }
  const responseText = Buffer.concat(chunks).toString('utf8')
  assert.match(responseText, /new_vendor_quota_state/, '未配置错误必须保留上游错误码')
  assert.match(responseText, /当前供应商返回未配置的额度失败/, '未配置错误必须保留上游错误消息')
  assert.deepEqual(authorizations, ['Bearer sk-opaque-primary'], '未知完整 HTTP 失败不得请求同组后备账户')
} finally {
  result?.releaseConcurrency()
  upstreamRequest.closeGatewayUpstreamAgentsForTest()
  await new Promise<void>((resolvePromise) => upstream.close(() => resolvePromise()))
}

assert.deepEqual(accountConcurrency.snapshotAccountConcurrency(), {}, '终止失败后不得泄漏账户并发槽')
console.log('gateway generic upstream opaque regression passed')
