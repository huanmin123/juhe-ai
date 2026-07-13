import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { GPT_OPENAI_V1_PROFILE_ID, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '../../domain/provider-protocol.js'
import { clearAccountConcurrency, tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'
import { fetchFirstAvailableUpstream, UpstreamAttemptError } from '../../modules/gateway/dispatch/upstream-dispatch.js'
import { resolveOpenAIGatewayRequestLane } from '../../modules/gateway/protocols/openai-v1/request-lane.js'
import { clearHighConcurrencyGroupQueues } from '../../modules/gateway/runtime/high-concurrency-queue.service.js'
import type { GatewaySettings } from '../../modules/gateway/policy/account-error-policy.service.js'
import type { AuditCaptureContext } from '../../modules/gateway/audit/capture.service.js'
import type { GatewayUsageContext } from '../../modules/gateway/usage/records.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-concurrency-preparation-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-concurrency-preparation-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, usageRecordQueue, usageRecordShards] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/usage-record-shards.js')
])

const settings: GatewaySettings = {
  gatewayTextRawBodyLimitMegabytes: 8,
  defaultTemporaryUnschedulableMinutes: 5,
  temporaryUnschedulableRetryIntervalSeconds: 0,
  temporaryUnschedulableRetryAttempts: 0,
  streamCircuitBreakerEnabled: true,
  streamRequestTimeoutSeconds: 120,
  streamIdleTimeoutSeconds: 30,
  streamClientTotalWaitTimeoutSeconds: 270,
  streamMaxLifetimeSeconds: 1800,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 5
}

const usageContext: GatewayUsageContext = {
  traceId: 'trace_gateway_concurrency_preparation',
  trafficSource: 'gateway',
  clientIp: '127.0.0.1',
  systemAccountId: 'sys_admin',
  groupId: 'grp_gateway_concurrency_preparation',
  endpoint: 'POST /v1/chat/completions',
  requestSnapshot: {
    method: 'POST',
    path: '/v1/chat/completions',
    originalUrl: '/v1/chat/completions',
    traceId: 'trace_gateway_concurrency_preparation',
    headers: {},
    body: { model: 'gpt-5.5', messages: [{ role: 'user', content: 'hi' }] }
  }
}

const recordedFailedDispatchAttempts: Array<Parameters<AuditCaptureContext['recordFailedDispatchAttempt']>[0]> = []
const auditCapture = {
  startAttempt: () => '',
  completeAttempt: () => undefined,
  addGatewayMetadata: () => undefined,
  recordFailedDispatchAttempt: (input: Parameters<AuditCaptureContext['recordFailedDispatchAttempt']>[0]) => {
    recordedFailedDispatchAttempts.push(input)
    return `audit_attempt_${recordedFailedDispatchAttempts.length}`
  }
} as unknown as AuditCaptureContext

let holdAndReleaseServer: http.Server | undefined
let hitAccountIds: string[] = []

try {
  clearAccountConcurrency()
  clearHighConcurrencyGroupQueues()
  assert.equal(resolveOpenAIGatewayRequestLane(buildLaneRequest('/v1/images/generations')), 'image', 'OpenAI 图片接口应识别为图像通道')
  assert.equal(resolveOpenAIGatewayRequestLane(buildLaneRequest('/v1/responses', { model: 'gpt-image-1', prompt: 'x' })), 'image', 'gpt-image 模型应识别为图像通道')
  assert.equal(resolveOpenAIGatewayRequestLane(buildLaneRequest('/v1/chat/completions', { model: 'dall-e-3', prompt: 'x' })), 'image', 'dall-e 模型应识别为图像通道')
  assert.equal(resolveOpenAIGatewayRequestLane(buildLaneRequest('/v1/responses', { model: 'gpt-5.5', tools: [{ type: 'image_generation' }] })), 'image', 'Responses 图像生成工具应识别为图像通道')
  assert.equal(resolveOpenAIGatewayRequestLane(buildLaneRequest('/v1/responses', { model: 'gpt-5.5', tool_choice: { type: 'image_generation' } })), 'image', 'Responses 对象形态 tool_choice 应识别为图像通道')
  assert.equal(resolveOpenAIGatewayRequestLane(buildLaneRequest('/v1/responses', { model: 'gpt-5.5', input: [{ content: [{ type: 'input_text', text: 'hi' }] }] })), 'text', '普通文本 Responses 请求应保持文本通道')

  const holdAndReleaseUpstream = await createHoldAndReleaseServer()
  holdAndReleaseServer = holdAndReleaseUpstream.server
  const retryAccount = buildAccount({
    id: 'acct_retry',
    name: '短等后继续账号',
    concurrencyLimit: 1,
    type: 'api_key',
    baseUrl: holdAndReleaseUpstream.baseUrl
  })
  const retrySlot = tryAcquireAccountConcurrency(retryAccount.id, retryAccount.concurrencyLimit)
  assert.equal(retrySlot.acquired, true, '短等回归前应成功占用账号并发槽')
  setTimeout(() => retrySlot.release(), 950)

  const retryResult = await fetchFirstAvailableUpstream(
    buildRequest(),
    [retryAccount],
    settings,
    usageContext,
    auditCapture,
    undefined,
    new AbortController().signal
  )
  assert.equal(retryResult.account.id, retryAccount.id, '账号并发短等后应继续使用原账号')
  assert.equal(retryResult.upstreamUrl, `${retryAccount.baseUrl}/chat/completions`, '账号并发短等后应命中同一上游地址')
  assert.equal(retryResult.response.status, 200, '账号并发短等后应成功拿到上游响应')
  for await (const _chunk of retryResult.response.body ?? []) {
  }
  retryResult.releaseConcurrency()
  clearAccountConcurrency()

  const saturatedAccount = buildAccount({
    id: 'acct_saturated',
    name: '饱和账号',
    concurrencyLimit: 1,
    type: 'api_key',
    baseUrl: 'https://should-not-build.example'
  })
  const heldSlot = tryAcquireAccountConcurrency(saturatedAccount.id, saturatedAccount.concurrencyLimit)
  assert.equal(heldSlot.acquired, true, '测试前应成功占用账号并发槽')

  await assert.rejects(
    fetchFirstAvailableUpstream(
      buildRequest(),
      [saturatedAccount],
      settings,
      usageContext,
      auditCapture,
      undefined,
      new AbortController().signal
    ),
    (error: unknown) => error instanceof UpstreamAttemptError
      && error.lastAttempt?.upstreamUrl === 'concurrency:limit'
      && error.message.includes('账户并发已达到上限')
  )

  usageRecordQueue.flushAllUsageRecordQueue()
  const usage = latestUsageRecordForAccount(saturatedAccount.id)
  assert.equal(usage?.account_id, saturatedAccount.id, '账号并发满也应写入失败使用记录')
  assert.equal(usage?.success, 0, '账号并发满使用记录应标记失败')
  assert.match(usage?.error_message ?? '', /并发已达到上限/, '账号并发满使用记录应保留错误原因')
  const saturatedAuditAttempt = recordedFailedDispatchAttempts.find((item) => item.account.id === saturatedAccount.id)
  assert.equal(saturatedAuditAttempt?.upstreamUrl, 'concurrency:limit', '账号并发满写入使用记录时也必须写入审计 attempt')
  assert.equal(saturatedAuditAttempt?.errorCode, 'account_concurrency_limit', '账号并发满审计 attempt 应保留可检索错误码')

  heldSlot.release()
  clearAccountConcurrency()

  const highConcurrencyQueuedAccount = buildAccount({
    id: 'acct_high_concurrency_queue_after_slot_race',
    name: '高并发槽位竞态排队账号',
    concurrencyLimit: 1,
    type: 'api_key',
    baseUrl: holdAndReleaseUpstream.baseUrl
  })
  const highConcurrencyHeldSlot = tryAcquireAccountConcurrency(highConcurrencyQueuedAccount.id, highConcurrencyQueuedAccount.concurrencyLimit)
  assert.equal(highConcurrencyHeldSlot.acquired, true, '高并发排队测试前应先占用账号并发槽')
  const releaseHighConcurrencyHeldSlot = setTimeout(() => highConcurrencyHeldSlot.release(), 1500)
  try {
    const queuedDispatchResult = await fetchFirstAvailableUpstream(
      buildRequest(),
      [highConcurrencyQueuedAccount],
      settings,
      usageContext,
      auditCapture,
      undefined,
      new AbortController().signal,
      undefined,
      'text',
      {
        maxQueueWaitMs: 3000,
        maxQueueSize: 10,
        perApiKeyQueueLimit: 10
      }
    )
    assert.equal(queuedDispatchResult.account.id, highConcurrencyQueuedAccount.id, '高并发真实抢槽失败后应进入队列并在释放后继续调度')
    assert.equal(queuedDispatchResult.response.status, 200, '高并发队列唤醒后应成功拿到上游响应')
    await drainAndRelease(queuedDispatchResult)
  } finally {
    clearTimeout(releaseHighConcurrencyHeldSlot)
    highConcurrencyHeldSlot.release()
  }
  usageRecordQueue.flushAllUsageRecordQueue()
  assert.equal(latestUsageRecordForAccount(highConcurrencyQueuedAccount.id), undefined, '高并发队列等待期间不应写入账号并发满失败使用记录')
  assert.equal(recordedFailedDispatchAttempts.some((item) => item.account.id === highConcurrencyQueuedAccount.id), false, '高并发队列等待成功时也不应写入账号并发满审计 attempt')
  clearHighConcurrencyGroupQueues()
  clearAccountConcurrency()

  const imageLaneAccount = buildAccount({
    id: 'acct_image_lane_reserved_text',
    name: '图像通道预留文本槽账号',
    concurrencyLimit: 2,
    type: 'api_key',
    baseUrl: holdAndReleaseUpstream.baseUrl
  })
  const heldImageSlot = tryAcquireAccountConcurrency(imageLaneAccount.id, imageLaneAccount.concurrencyLimit, { lane: 'image', laneLimit: 1 })
  assert.equal(heldImageSlot.acquired, true, '测试前应先占用图像通道槽')

  await assert.rejects(
    fetchFirstAvailableUpstream(
      buildRequest({
        originalUrl: '/v1/images/generations',
        path: '/images/generations',
        body: { model: 'gpt-image-1', prompt: 'draw a small gateway diagram' }
      }),
      [imageLaneAccount],
      settings,
      usageContext,
      auditCapture,
      undefined,
      new AbortController().signal,
      undefined,
      'image'
    ),
    (error: unknown) => error instanceof UpstreamAttemptError
      && error.lastAttempt?.upstreamUrl === 'concurrency:limit'
      && error.message.includes('账户图像通道并发已达到上限 1/1')
      && error.message.includes('已为文本通道保留并发槽')
  )

  usageRecordQueue.flushAllUsageRecordQueue()
  const imageLaneFailureUsage = latestUsageRecordForAccount(imageLaneAccount.id)
  assert.equal(imageLaneFailureUsage?.success, 0, '图像通道满也应写入失败使用记录')
  assert.match(imageLaneFailureUsage?.error_message ?? '', /图像通道并发已达到上限/, '图像通道满使用记录应保留错误原因')

  const textResultWhileImageHeld = await fetchFirstAvailableUpstream(
    buildRequest(),
    [imageLaneAccount],
    settings,
    usageContext,
    auditCapture,
    undefined,
    new AbortController().signal,
    undefined,
    'text'
  )
  assert.equal(textResultWhileImageHeld.response.status, 200, '图像通道满时文本请求仍应使用预留并发槽')
  await drainAndRelease(textResultWhileImageHeld)
  heldImageSlot.release()
  clearAccountConcurrency()

  const configuredImageLaneAccount = buildAccount({
    id: 'acct_image_lane_configured',
    name: '图像通道自定义上限账号',
    concurrencyLimit: 3,
    type: 'api_key',
    baseUrl: holdAndReleaseUpstream.baseUrl
  })
  const configuredHeldImageSlotA = tryAcquireAccountConcurrency(configuredImageLaneAccount.id, configuredImageLaneAccount.concurrencyLimit, { lane: 'image', laneLimit: 2 })
  const configuredHeldImageSlotB = tryAcquireAccountConcurrency(configuredImageLaneAccount.id, configuredImageLaneAccount.concurrencyLimit, { lane: 'image', laneLimit: 2 })
  assert.equal(configuredHeldImageSlotA.acquired, true, '自定义图像通道上限测试前应占用第一个图像槽')
  assert.equal(configuredHeldImageSlotB.acquired, true, '自定义图像通道上限测试前应占用第二个图像槽')
  await assert.rejects(
    fetchFirstAvailableUpstream(
      buildRequest({
        originalUrl: '/v1/images/generations',
        path: '/images/generations',
        body: { model: 'gpt-image-1', prompt: 'draw another gateway diagram' }
      }),
      [configuredImageLaneAccount],
      settings,
      usageContext,
      auditCapture,
      undefined,
      new AbortController().signal,
      undefined,
      'image',
      { imageLaneMaxConcurrency: 2 }
    ),
    (error: unknown) => error instanceof UpstreamAttemptError
      && error.lastAttempt?.upstreamUrl === 'concurrency:limit'
      && error.message.includes('账户图像通道并发已达到上限 2/2')
  )
  const configuredTextResult = await fetchFirstAvailableUpstream(
    buildRequest(),
    [configuredImageLaneAccount],
    settings,
    usageContext,
    auditCapture,
    undefined,
    new AbortController().signal,
    undefined,
    'text',
    { imageLaneMaxConcurrency: 2 }
  )
  assert.equal(configuredTextResult.response.status, 200, '自定义图像通道满时文本请求仍应使用剩余总并发槽')
  await drainAndRelease(configuredTextResult)
  configuredHeldImageSlotA.release()
  configuredHeldImageSlotB.release()
  clearAccountConcurrency()

  const imageLaneBusyAccount = buildAccount({
    id: 'acct_image_lane_busy_order',
    name: '图像通道已满排序账号',
    concurrencyLimit: 2,
    type: 'api_key',
    baseUrl: `${holdAndReleaseUpstream.baseUrl}/accounts/acct_image_lane_busy_order`
  })
  const imageLaneAvailableAccount = buildAccount({
    id: 'acct_image_lane_available_order',
    name: '图像通道可用排序账号',
    concurrencyLimit: 2,
    type: 'api_key',
    baseUrl: `${holdAndReleaseUpstream.baseUrl}/accounts/acct_image_lane_available_order`
  })
  const busyOrderSlot = tryAcquireAccountConcurrency(imageLaneBusyAccount.id, imageLaneBusyAccount.concurrencyLimit, { lane: 'image', laneLimit: 1 })
  assert.equal(busyOrderSlot.acquired, true, '排序测试前应占用第一个账号图像通道')
  hitAccountIds = []
  const orderedImageResult = await fetchFirstAvailableUpstream(
    buildRequest({
      originalUrl: '/v1/images/generations',
      path: '/images/generations',
      body: { model: 'gpt-image-1', prompt: 'route to available image lane' }
    }),
    [imageLaneBusyAccount, imageLaneAvailableAccount],
    settings,
    usageContext,
    auditCapture,
    undefined,
    new AbortController().signal,
    undefined,
    'image'
  )
  assert.equal(orderedImageResult.account.id, imageLaneAvailableAccount.id, '图像请求应优先选择图像通道仍可用的账号')
  assert.deepEqual(hitAccountIds, [imageLaneAvailableAccount.id], '图像通道已满账号不应先短等并尝试上游')
  await drainAndRelease(orderedImageResult)
  busyOrderSlot.release()
  hitAccountIds = []
  clearAccountConcurrency()

  console.log('网关并发准备回归通过：短等复用、饱和失败记录、高并发真实抢槽排队、图像通道预留文本槽、自定义图像通道上限和图像 lane 排序均符合预期')
} finally {
  clearAccountConcurrency()
  clearHighConcurrencyGroupQueues()
  holdAndReleaseServer?.close()
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function latestUsageRecordForAccount(accountId: string): { account_id?: string; success?: number; error_message?: string; created_at?: string; id?: string } | undefined {
  return usageRecordShards.listUsageRecordShardLocations()
    .map((location) => usageRecordShards.getUsageRecordShardDatabase(location)
      .prepare('SELECT account_id, success, error_message, created_at, id FROM usage_records WHERE account_id = ? ORDER BY created_at DESC, id DESC LIMIT 1')
      .get(accountId) as { account_id?: string; success?: number; error_message?: string; created_at?: string; id?: string } | undefined)
    .filter((row): row is { account_id?: string; success?: number; error_message?: string; created_at?: string; id?: string } => Boolean(row))
    .sort((left, right) => String(right.created_at ?? '').localeCompare(String(left.created_at ?? '')) || String(right.id ?? '').localeCompare(String(left.id ?? '')))[0]
}

async function createHoldAndReleaseServer(): Promise<{ server: http.Server; baseUrl: string }> {
  const server = http.createServer((req, res) => {
    const accountId = /\/accounts\/([^/]+)/.exec(req.url ?? '')?.[1]
    if (accountId) {
      hitAccountIds.push(accountId)
    }
    res.writeHead(200, {
      'content-type': 'application/json; charset=utf-8'
    })
    res.end(JSON.stringify({
      id: 'resp_concurrency_retry',
      object: 'response'
    }))
  })
  await new Promise<void>((resolvePromise) => {
    server.listen(0, '127.0.0.1', () => resolvePromise())
  })
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('无法启动并发回归测试上游服务器')
  }
  return {
    server,
    baseUrl: `http://127.0.0.1:${address.port}/v1`
  }
}

async function drainAndRelease(result: Awaited<ReturnType<typeof fetchFirstAvailableUpstream>>): Promise<void> {
  for await (const _chunk of result.response.body ?? []) {
  }
  result.releaseConcurrency()
}

function buildRequest(input: {
  originalUrl?: string
  path?: string
  body?: unknown
} = {}): Parameters<typeof fetchFirstAvailableUpstream>[0] {
  return {
    method: 'POST',
    originalUrl: input.originalUrl ?? '/v1/chat/completions',
    path: input.path ?? '/chat/completions',
    headers: { 'content-type': 'application/json' },
    body: input.body ?? {
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'hi' }]
    },
    header(name: string): string | undefined {
      const value = this.headers[name.toLowerCase()]
      return Array.isArray(value) ? value.join(', ') : value
    }
  } as Parameters<typeof fetchFirstAvailableUpstream>[0]
}

function buildLaneRequest(path: string, body: unknown = {}): Request {
  return {
    path,
    originalUrl: path,
    body
  } as Request
}

function buildAccount(input: {
  id: string
  name: string
  concurrencyLimit: number
  type: 'api_key' | 'oauth'
  baseUrl: string
}): UpstreamAccount {
  return {
    id: input.id,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    systemAccountId: 'sys_admin',
    name: input.name,
    type: input.type,
    status: 'active',
    supportedModels: [],
    apiKey: 'sk-concurrency-preparation',
    baseUrl: input.baseUrl,
    credentials: {
      api_key: 'sk-concurrency-preparation',
      base_url: input.baseUrl
    },
    concurrencyLimit: input.concurrencyLimit,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    healthCheckEndpointFamily: 'responses',
    streamFailureCount: 0,
    accountOwnerSystemAccountId: 'sys_admin',
    groupOwnerSystemAccountId: 'sys_admin',
    accountAccessType: 'owner',
    groupAccessType: 'owner'
  }
}
