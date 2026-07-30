import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { readFileSync } from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import type { Request, Response } from 'express'

import type { AuditCaptureContext } from '../../modules/gateway/audit/capture.service.js'
import type { GatewaySettings } from '../../modules/gateway/policy/account-error-policy.service.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import type { GatewayUsageContext } from '../../modules/gateway/usage/records.js'

process.env.JUHE_AI_PROCESS_ROLE = 'worker'
process.env.JUHE_AI_WORKER_ROLE = 'ingest-worker'
process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_QUEUE_DRIVER = 'memory'

const [
  failureDispatch,
  jsonInspection,
  accountSideEffects,
  apiKeyFailureGuard,
  clientIpAvoidance,
  proxyHealth,
  hotQuality,
  upstreamBody,
  usageRecordQueue,
  accountPreparation,
  upstreamDispatch,
  routeCoordination,
  serverRetryBudget,
  upstreamRequest,
  accountConcurrency,
  { runtimeConfig }
] = await Promise.all([
  import('../../modules/gateway/response/failure-dispatch.js'),
  import('../../modules/gateway/response/non-stream-json-inspection.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/runtime/account-api-key-failure-guard.service.js'),
  import('../../modules/gateway/runtime/client-ip-account-avoidance.service.js'),
  import('../../modules/gateway/runtime/proxy-health.service.js'),
  import('../../modules/gateway/runtime/hot-quality-runtime.service.js'),
  import('../../modules/gateway/upstream/body.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/gateway/dispatch/account-preparation.js'),
  import('../../modules/gateway/dispatch/upstream-dispatch.js'),
  import('../../modules/gateway/routing/route-coordination.js'),
  import('../../modules/gateway/runtime/server-retry-budget.js'),
  import('../../modules/gateway/upstream/request.js'),
  import('../../shared/account-concurrency.js'),
  import('../../config/runtime.js')
])

runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true

const settings = {
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
  imageRequestWallTimeoutSeconds: 180,
  noAvailableAccountWaitTimeoutSeconds: 10,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 5
} as GatewaySettings

const account = {
  id: 'generic-failure-boundary-account',
  name: 'generic-failure-boundary-account',
  systemAccountId: 'generic-failure-boundary-owner',
  accountOwnerSystemAccountId: 'generic-failure-boundary-owner',
  providerCode: 'gpt',
  providerProtocolProfileId: 'openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  baseUrl: 'https://untrusted-provider.example/v1',
  type: 'api_key',
  proxyProfileId: 'generic-failure-boundary-proxy',
  status: 'active',
  schedulable: true,
  apiKeys: [{ id: 'single-key', key: 'secret-not-used' }]
} as unknown as UpstreamAccount

const sameProxyBackupAccount = {
  ...account,
  id: 'generic-failure-boundary-backup-account',
  name: 'generic-failure-boundary-backup-account',
  baseUrl: 'https://healthy-independent-origin.example/v1',
  apiKey: 'secret-backup-not-used',
  credentials: {
    api_key: 'secret-backup-not-used',
    base_url: 'https://healthy-independent-origin.example/v1'
  }
} as unknown as UpstreamAccount

const req = {
  method: 'POST',
  path: '/chat/completions',
  originalUrl: '/v1/chat/completions',
  headers: {},
  body: {
    model: 'gpt-5.5',
    messages: [{ role: 'user', content: 'one malformed session must stay request scoped' }],
    stream: false
  },
  header: () => undefined
} as unknown as Request

const usageContext = {
  traceId: 'generic-failure-boundary-trace',
  trafficSource: 'gateway',
  clientIp: '198.51.100.91',
  systemAccountId: 'generic-failure-boundary-owner',
  apiKeyId: 'generic-failure-boundary-gateway-key',
  groupId: 'generic-failure-boundary-group',
  endpoint: '/v1/chat/completions',
  requestSnapshot: {}
} as GatewayUsageContext

const auditCapture = {
  startAttempt: () => `generic-failure-attempt-${Date.now()}-${Math.random()}`,
  completeAttempt() {},
  recordFailedDispatchAttempt() {},
  addGatewayMetadata() {},
  finalize() {}
} as unknown as AuditCaptureContext

async function assertRequestTransportFailureDoesNotExcludeSiblingAccount(): Promise<void> {
  const tracker = clientIpAvoidance.createClientIpAccountAvoidanceTracker({
    systemAccountId: usageContext.systemAccountId,
    groupId: usageContext.groupId,
    apiKeyId: usageContext.apiKeyId,
    clientIp: usageContext.clientIp ?? '198.51.100.91'
  })
  const failedProxyDispatchKeys = new Map<string, string>()
  const before = sharedStateSnapshot()
  const result = await failureDispatch.handleUpstreamRequestError({
    req,
    usageContext,
    auditCapture,
    auditAttemptId: 'generic-transport-attempt',
    account,
    upstreamUrl: 'https://untrusted-provider.example/v1/chat/completions',
    attemptStartedAt: Date.now() - 5,
    attemptIndex: 0,
    auditAttemptIndex: 0,
    settings,
    failedProxyDispatchKeys,
    error: Object.assign(new Error('connection reset for this request'), { code: 'ECONNRESET' }),
    clientIpAccountAvoidanceTracker: tracker,
    accountStateMutationEnabled: true
  })

  assert.equal(result.action, 'skip_account', 'transport 失败仍应在当前请求内切换候选')
  assert.equal(
    accountPreparation.skipAccountForFailedProxyDispatch(failedProxyDispatchKeys, sameProxyBackupAccount),
    undefined,
    '一个源站的 transport 失败不得把共享代理的独立账户在发请求前跳过'
  )
  assert.equal(failedProxyDispatchKeys.size, 0, '无法证明代理自身失败时不得写 proxy-wide 请求内排除')
  await clientIpAvoidance.confirmClientIpAccountAvoidanceAfterFinalFailureAsync(tracker, settings)
  assertSharedStateUnchanged(before, 'generic request transport failure')
  assert.equal(proxyHealth.recordGatewayUpstreamBucketSuccess(account), false, 'transport 失败不得创建 proxy/upstream 失败桶')
}

async function assertSharedProxyIndependentOriginCanStillSucceed(): Promise<void> {
  const before = sharedStateSnapshot()
  const connectAuthorities: string[] = []
  let failingOriginHits = 0
  let healthyOriginHits = 0
  const failingOrigin = http.createServer((request) => {
    failingOriginHits += 1
    request.socket.destroy()
  })
  const healthyOrigin = http.createServer((request, response) => {
    healthyOriginHits += 1
    request.resume()
    response.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
    response.end(JSON.stringify({
      id: 'chatcmpl-shared-proxy-backup',
      object: 'chat.completion',
      choices: [{ index: 0, message: { role: 'assistant', content: 'ok' }, finish_reason: 'stop' }]
    }))
  })
  const proxy = http.createServer((_request, response) => {
    response.writeHead(405)
    response.end()
  })
  proxy.on('connect', (request, downstream, head) => {
    const authority = request.url ?? ''
    connectAuthorities.push(authority)
    const separator = authority.lastIndexOf(':')
    assert(separator > 0, `代理 CONNECT authority 无效：${authority}`)
    const host = authority.slice(0, separator)
    const port = Number(authority.slice(separator + 1))
    const upstream = net.connect({ host, port })
    upstream.once('connect', () => {
      downstream.write('HTTP/1.1 200 Connection Established\r\n\r\n')
      if (head.byteLength > 0) upstream.write(head)
      upstream.pipe(downstream)
      downstream.pipe(upstream)
    })
    upstream.once('error', () => downstream.destroy())
    downstream.once('error', () => upstream.destroy())
    downstream.once('close', () => upstream.destroy())
  })

  await Promise.all([listen(failingOrigin), listen(healthyOrigin), listen(proxy)])
  const proxyUrl = serverUrl(proxy)
  const failingBaseUrl = `${serverUrl(failingOrigin)}/v1`
  const healthyBaseUrl = `${serverUrl(healthyOrigin)}/v1`
  const failingAccount = buildDispatchAccount('shared-proxy-failing-origin', failingBaseUrl, proxyUrl, 2)
  const healthyAccount = buildDispatchAccount('shared-proxy-healthy-origin', healthyBaseUrl, proxyUrl)
  let results: Array<Awaited<ReturnType<typeof upstreamDispatch.fetchFirstAvailableUpstream>>> = []
  try {
    results = await Promise.all(['first', 'second'].map((requestId) => upstreamDispatch.fetchFirstAvailableUpstream(
      buildProxyDispatchRequest(requestId),
      [failingAccount, healthyAccount],
      sharedProxySettings(),
      buildProxyUsageContext(requestId),
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
      createRequestCoordination(requestId),
      false,
      false
    )))
    assert.deepEqual(results.map((result) => result.account.id), [healthyAccount.id, healthyAccount.id], '并发请求中 A 源站断开后都必须实际派发共享代理上的 B 账户')
    assert(results.every((result) => result.response.status === 200), '共享代理上的 B 账户应为每个并发请求返回成功响应')
    await Promise.all(results.map(async (result) => {
      for await (const _chunk of result.response.body ?? []) {
      }
    }))
    assert.equal(failingOriginHits, 4, '两个并发请求都应穷尽失败账户的两个 Key，且每个 Key 只尝试一次')
    assert.equal(healthyOriginHits, 2, '独立健康源站应为两个并发请求各接收一次物理请求')
    assert.equal(connectAuthorities.length, 6, '失败账户双 Key 与健康账户均必须经同一代理建立 CONNECT')
    const distinctOrigins = new Set(connectAuthorities)
    assert.equal(distinctOrigins.size, 2, '所有 CONNECT 只能落到测试定义的两个独立源站身份')
  } finally {
    results.forEach((result) => result.releaseConcurrency())
    upstreamRequest.closeGatewayUpstreamAgentsForTest()
    await Promise.all([closeServer(proxy), closeServer(healthyOrigin), closeServer(failingOrigin)])
  }
  assert.deepEqual(accountConcurrency.snapshotAccountConcurrency(), {}, '并发共享代理切号完成后不得泄漏账户并发槽')
  assertSharedStateUnchanged(before, 'concurrent multi-key shared-proxy origin failure')
  assert.equal(proxyHealth.recordGatewayUpstreamBucketSuccess(failingAccount), false, '失败源站 transport 不得创建共享 proxy/upstream health 桶')
  assert.equal(proxyHealth.recordGatewayUpstreamBucketSuccess(healthyAccount), false, '仅获取候选响应不得隐式创建共享 proxy/upstream health 桶')
}

async function assertCompleteInvalidProtocolStaysRequestScoped(): Promise<void> {
  const before = sharedStateSnapshot()
  const bodyText = JSON.stringify({ id: 'chatcmpl-invalid', choices: [] })
  const result = await jsonInspection.inspectBufferedGatewayJsonResponse({
    req,
    res: new MockResponse() as unknown as Response,
    account,
    upstreamResponse: {
      status: 200,
      ok: true,
      headers: new Headers({ 'content-type': 'application/json; charset=utf-8' }),
      body: null
    },
    upstreamUrl: 'https://untrusted-provider.example/v1/chat/completions',
    auditAttemptId: 'generic-invalid-protocol-attempt',
    auditCapture,
    settings,
    usageContext,
    startedAt: Date.now() - 5,
    responseBody: Buffer.from(bodyText),
    responseBodyText: bodyText,
    accountStateMutationEnabled: true,
    automaticAccountStateMutationEnabled: true,
    protocolValidationEnabled: true,
    downstreamCommitState: {} as never
  })

  assert.equal(result?.alreadyFinalized, false, '下游未提交时协议结构失败应由调用方在当前请求内重试')
  assert.equal(result && 'retryUpstream' in result ? result.retryUpstream : false, true, '协议结构失败应请求内切号')
  assertSharedStateUnchanged(before, 'complete 2xx invalid protocol response')
  assert.equal(proxyHealth.recordGatewayUpstreamBucketSuccess(account), false, '2xx 协议结构失败不得写 proxy/upstream health')
}

async function assertUpstreamAbortNamedReaderErrorIsNotClientCancellation(): Promise<void> {
  const readerAbortError = Object.assign(new Error('The operation was aborted by the remote body reader'), {
    name: 'AbortError'
  })
  const incompleteBody = {
    async *[Symbol.asyncIterator](): AsyncGenerator<Uint8Array> {
      yield Buffer.from('{"partial":')
      throw readerAbortError
    }
  }
  await assert.rejects(
    upstreamBody.readUpstreamBodyLimited(incompleteBody),
    (error: unknown) => {
      assert(error instanceof upstreamBody.UpstreamBodyReadIncompleteError)
      assert.equal(error.name, 'UpstreamBodyReadIncompleteError')
      assert.doesNotMatch(error.message, /aborted|cancelled|canceled/i, '上游 reader AbortError 不得伪装成客户端取消')
      return true
    }
  )
}

function assertSourceBoundaries(): void {
  const failureSource = readFileSync(new URL('../../modules/gateway/response/failure-dispatch.ts', import.meta.url), 'utf8')
  const requestFailureSource = sourceBetween(
    failureSource,
    'export async function handleUpstreamRequestError(',
    '\nexport function formatUpstreamRequestErrorMessage('
  )
  assert.doesNotMatch(
    requestFailureSource,
    /recordGatewayUpstreamBucketFailure|rememberClientIpAccountPendingFailure|suppressGatewayAccountLocally|recordGatewayAccountFailureForPrecheck|applyAccountErrorHandlingWithCacheInvalidation/,
    'generic transport 失败处理器不得写 proxy、IP、本地账户、precheck 或持久账户状态'
  )
  assert.doesNotMatch(
    requestFailureSource,
    /rememberFailedProxyForDispatch\(/,
    '无法证明代理 CONNECT/auth/handshake 失败的 generic transport 不得扩散为 proxy-wide 排除'
  )
  assert.match(
    failureSource,
    /isAccountDiagnosticTrafficSource\(usageContext\.trafficSource\)[\s\S]{0,160}action: 'return_response'/,
    '账户诊断完整 HTTP 应原样返回，不得被 generic takeover 隐藏'
  )

  const finalizationSource = readFileSync(new URL('../../modules/gateway/response/finalization.ts', import.meta.url), 'utf8')
  const incompleteStreamSource = sourceBetween(
    finalizationSource,
    'if (!streamResult.completed) {',
    '\n  return {\n    alreadyFinalized: false,'
  )
  assert.doesNotMatch(
    incompleteStreamSource,
    /rememberClientIpAccountPendingFailure|confirmClientIpAccountAvoidanceAfterFinalFailure/,
    '流式断尾不得因单会话失败写 IP×账户共享回避'
  )
  const committedBodyInterruptionSource = sourceBetween(
    finalizationSource,
    'error instanceof NonStreamUpstreamBodyPipeError && (res.headersSent || res.writableEnded || res.destroyed)',
    '\n    throw error'
  )
  assert.doesNotMatch(
    committedBodyInterruptionSource,
    /suppressGatewayAccountLocally|recordGatewayAccountFailureForPrecheck|applyAccountErrorHandlingWithCacheInvalidation|rememberClientIpAccountPendingFailure/,
    '已提交非流式正文中断只能记录本请求失败，不得共享写状态'
  )

  const inspectionSource = readFileSync(new URL('../../modules/gateway/response/non-stream-json-inspection.ts', import.meta.url), 'utf8')
  const protocolFailureSource = sourceBetween(
    inspectionSource,
    'async function finalizeBufferedJsonProtocolFailure(',
    '\nfunction plainObject('
  )
  assert.doesNotMatch(
    protocolFailureSource,
    /suppressGatewayAccountLocally|recordGatewayAccountFailureForPrecheck|applyAccountErrorHandlingWithCacheInvalidation/,
    '2xx 协议结构失败不得写账户共享状态'
  )
  assert.match(protocolFailureSource, /retryUpstream: true/, '下游未提交时应保留请求内切号')
  assert.doesNotMatch(
    finalizationSource,
    /confirmClientIpAccountAvoidanceAfterSuccessAsync/,
    '普通请求成功也不应顺带改写无 provenance 的 IP×账户共享状态'
  )
}

function sharedStateSnapshot(): {
  accountRuntime: ReturnType<typeof accountSideEffects.snapshotGatewayAccountRuntimeAvailability>
  sideEffectQueue: ReturnType<typeof accountSideEffects.getGatewayAccountSideEffectState>
  apiKeys: ReturnType<typeof apiKeyFailureGuard.getGatewayAccountApiKeyFailureGuardSnapshotForTest>
  clientIp: ReturnType<typeof clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest>
} {
  return {
    accountRuntime: accountSideEffects.snapshotGatewayAccountRuntimeAvailability(),
    sideEffectQueue: accountSideEffects.getGatewayAccountSideEffectState(),
    apiKeys: apiKeyFailureGuard.getGatewayAccountApiKeyFailureGuardSnapshotForTest(),
    clientIp: clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest()
  }
}

function assertSharedStateUnchanged(before: ReturnType<typeof sharedStateSnapshot>, scenario: string): void {
  assert.deepEqual(accountSideEffects.snapshotGatewayAccountRuntimeAvailability(), before.accountRuntime, `${scenario}: account runtime changed`)
  assert.deepEqual(accountSideEffects.getGatewayAccountSideEffectState(), before.sideEffectQueue, `${scenario}: persistent side-effect queue changed`)
  assert.deepEqual(apiKeyFailureGuard.getGatewayAccountApiKeyFailureGuardSnapshotForTest(), before.apiKeys, `${scenario}: API Key state changed`)
  assert.deepEqual(clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest(), before.clientIp, `${scenario}: IP×account state changed`)
}

function clearSharedState(): void {
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
  proxyHealth.clearGatewayProxyHealthForTest()
  hotQuality.resetGatewayHotQualityRuntimeForTest()
}

class MockResponse extends EventEmitter {
  destroyed = false
  writableEnded = false
  headersSent = false
  statusCode = 200
  private readonly headers = new Map<string, string | number | readonly string[]>()

  setHeader(name: string, value: string | number | readonly string[]): this {
    this.headers.set(name.toLowerCase(), value)
    return this
  }

  getHeader(name: string): string | number | readonly string[] | undefined {
    return this.headers.get(name.toLowerCase())
  }

  getHeaders(): Record<string, string | number | readonly string[]> {
    return Object.fromEntries(this.headers)
  }
}

function sourceBetween(source: string, startMarker: string, endMarker: string): string {
  const start = source.indexOf(startMarker)
  const end = source.indexOf(endMarker, start)
  assert(start >= 0 && end > start, `source markers missing: ${startMarker}`)
  return source.slice(start, end)
}

function buildDispatchAccount(id: string, baseUrl: string, proxyUrl: string, apiKeyCount = 1): UpstreamAccount {
  const apiKeys = Array.from({ length: apiKeyCount }, (_value, index) => `sk-${id}-${index + 1}`)
  const apiKey = apiKeys[0]
  return {
    ...account,
    id,
    name: id,
    providerProtocolProfileId: 'profile_gpt_openai_v1',
    baseUrl,
    apiKey,
    apiKeys,
    proxyUrl,
    proxyProfileId: 'shared-proxy-profile',
    credentials: {
      api_key: apiKey,
      api_keys: apiKeys,
      base_url: baseUrl
    },
    concurrencyLimit: 4,
    priority: 0,
    supportedModels: ['gpt-5.5']
  } as unknown as UpstreamAccount
}

function sharedProxySettings(): GatewaySettings {
  return {
    ...settings,
    temporaryUnschedulableRetryIntervalSeconds: 0,
    temporaryUnschedulableRetryAttempts: 0,
    noAvailableAccountWaitTimeoutSeconds: 1
  }
}

function buildProxyDispatchRequest(requestId: string): Request {
  return {
    ...req,
    headers: { ...req.headers, 'x-request-id': requestId },
    body: {
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: `shared proxy isolation ${requestId}` }],
      stream: false
    }
  } as unknown as Request
}

function buildProxyUsageContext(requestId: string): GatewayUsageContext {
  return {
    ...usageContext,
    traceId: `generic-failure-boundary-${requestId}`,
    requestSnapshot: {
      method: 'POST',
      path: '/v1/chat/completions',
      originalUrl: '/v1/chat/completions',
      traceId: `generic-failure-boundary-${requestId}`,
      headers: { 'x-request-id': requestId },
      body: buildProxyDispatchRequest(requestId).body
    }
  }
}

function createRequestCoordination(requestId: string): import('../../modules/gateway/dispatch/upstream-dispatch.js').GatewayUpstreamRequestCoordinationContext {
  return {
    scope: 'gateway_request',
    serverRetryBudget: new serverRetryBudget.ServerRetryBudget(1_000),
    gatewayRequestWallBudget: new routeCoordination.GatewayRequestWallBudget({ requestAcceptedAtMs: Date.now() }),
    routeCoordinationBudget: new routeCoordination.RouteCoordinationBudget({ requestId: `shared-proxy-independent-origin-${requestId}` }),
    requestAttemptTracker: new routeCoordination.GatewayRequestAttemptTracker()
  }
}

async function listen(server: http.Server): Promise<void> {
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('error', rejectPromise)
    server.listen(0, '127.0.0.1', () => resolvePromise())
  })
}

function serverUrl(server: http.Server): string {
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('测试服务器未监听 TCP 地址')
  return `http://127.0.0.1:${address.port}`
}

async function closeServer(server: http.Server): Promise<void> {
  if (!server.listening) return
  await new Promise<void>((resolvePromise) => server.close(() => resolvePromise()))
}

clearSharedState()
await assertRequestTransportFailureDoesNotExcludeSiblingAccount()
clearSharedState()
await assertSharedProxyIndependentOriginCanStillSucceed()
clearSharedState()
await assertCompleteInvalidProtocolStaysRequestScoped()
await assertUpstreamAbortNamedReaderErrorIsNotClientCancellation()
assertSourceBoundaries()
clearSharedState()
usageRecordQueue.clearUsageRecordQueueForTest()

console.log('普通网关失败共享状态边界回归通过：transport、已提交正文中断和 2xx 协议结构失败均保持请求级')
