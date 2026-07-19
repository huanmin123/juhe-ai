import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'
import { tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-generic-upstream-opaque-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-generic-upstream-opaque-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  readWorkerPool,
  repositories,
  settingsRepository,
  gatewayCache,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])
const { isOpaqueUpstreamFailoverAllowed } = await import('../../modules/gateway/response/failure-dispatch.js')
const accountSideEffects = await import('../../modules/gateway/runtime/account-side-effects.service.js')
const proxyHealth = await import('../../modules/gateway/runtime/proxy-health.service.js')

assert.equal(isOpaqueUpstreamFailoverAllowed({ method: 'POST', originalUrl: '/v1/chat/completions' } as express.Request), true)
assert.equal(isOpaqueUpstreamFailoverAllowed({ method: 'POST', originalUrl: '/v1/responses' } as express.Request), true)
for (const path of ['/v1/uploads', '/v1/batches', '/v1/fine_tuning/jobs', '/v1/unknown']) {
  assert.equal(
    isOpaqueUpstreamFailoverAllowed({ method: 'POST', originalUrl: path } as express.Request),
    false,
    `${path} 资源型或未知 POST 不得跨 Key/账户重放`
  )
}
const gatewayRoutesSource = readFileSync(new URL('../../modules/gateway/routes.ts', import.meta.url), 'utf8')
const upstreamDispatchSource = readFileSync(new URL('../../modules/gateway/dispatch/upstream-dispatch.ts', import.meta.url), 'utf8')
assert.match(
  gatewayRoutesSource,
  /if \(upstreamResponse\.ok\) \{\s*await confirmHalfOpenSuccess\(\)\s*await confirmSameAccountApiKeyFailures\(\)/,
  '只有真实成功响应才能确认 half-open 和同账号前序 Key 失败，资源非 2xx 不得恢复账户运行态'
)
const preparedRequestIndex = upstreamDispatchSource.indexOf('const requestParts = await buildPreparedUpstreamRequestParts')
const opaqueBudgetConsumeIndex = upstreamDispatchSource.indexOf('opaqueFailoverBudget.attemptedAccountIds.add(originalAccount.id)')
const upstreamAttemptIndex = upstreamDispatchSource.indexOf('const response = await performUpstreamRequestAttempt')
assert(preparedRequestIndex >= 0 && opaqueBudgetConsumeIndex > preparedRequestIndex, '通用四账户预算只能在账户、Key 和请求体准备完成后扣减')
assert(upstreamAttemptIndex > opaqueBudgetConsumeIndex, '通用四账户预算必须紧邻首次真实上游 attempt 前扣减')
assert.equal((gatewayRoutesSource.match(/automaticAccountStateMutationEnabled: false/g) ?? []).length, 3, '普通客户请求的流式、非流式和最终化路径都必须关闭系统自动账户状态副作用')
assert.match(gatewayRoutesSource, /const requestErrorResult = await handleUpstreamRequestError\(\{[\s\S]*?accountStateMutationEnabled: false[\s\S]*?nonStreamResponseStartedFailedAccountIds\.add/, '非流式正文读取异常 catch 也不得按精确客户端画像开启账户状态副作用')
const responseFinalizationSource = readFileSync(new URL('../../modules/gateway/response/finalization.ts', import.meta.url), 'utf8')
assert.match(responseFinalizationSource, /automaticAccountStateMutationEnabled !== false[\s\S]*?recordGatewayUpstreamBucketSuccessAsync/, '普通成功响应不得清理后台确认的全局上游桶运行态')

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const hits: string[] = []
const upstreamAuthorizations: string[] = []
let upstreamServer: http.Server | undefined
let appServer: http.Server | undefined

try {
  settingsRepository.updateSettings({ temporaryUnschedulableRetryAttempts: 0 })
  gatewayCache.clearGatewayRuntimeCache()

  upstreamServer = http.createServer((req, res) => {
    hits.push(req.url ?? '')
    upstreamAuthorizations.push(String(req.headers.authorization ?? ''))
    const path = req.url?.split('?', 1)[0] ?? ''
    if (req.url?.includes('mock_sse_wait_non_stream=1')) {
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end('{"id":"non_stream_after_wait","choices":[{"message":{"role":"assistant","content":"must not be written into sse transport"}}]}')
      return
    }
    if (req.url?.includes('mock_codex_success=1')) {
      res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
      res.end([
        'event: response.completed',
        'data: {"type":"response.completed","response":{"id":"resp_bucket_success","status":"completed"}}',
        '',
        ''
      ].join('\n'))
      return
    }
    if (path === '/v1/responses') {
      res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
      res.end([
        'event: response.failed',
        'data: {"type":"response.failed","response":{"error":{"code":"vendor_invented_stream_error","message":"opaque stream failure"}}}',
        '',
        ''
      ].join('\n'))
      return
    }
    if (req.headers.authorization === 'Bearer sk-generic-opaque-good' || req.headers.authorization === 'Bearer sk-generic-image-good') {
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(path === '/v1/images/generations'
        ? '{"created":1,"data":[{"b64_json":"aW1hZ2U="}]}'
        : '{"id":"generic_fallback_success","choices":[{"message":{"role":"assistant","content":"server failover completed"}}]}')
      return
    }
    res.writeHead(418, {
      'content-type': 'application/json; charset=utf-8',
      'x-vendor-error': 'invented'
    })
    res.end('{"error":{"type":"vendor_invented_error","code":"made_up_418","message":"opaque non-stream failure"}}')
  })
  await listen(upstreamServer)
  const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

  const group = repositories.createGroup({ name: '通用响应透传分组', providerCode: GPT_VENDOR_CODE, enabled: true }, access)
  const account = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '通用响应透传账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-generic-opaque-bad-a',
      api_keys: ['sk-generic-opaque-bad-a', 'sk-generic-opaque-bad-b'],
      api_key_strategy: 'round_robin',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 1,
    priority: 0,
    supportedModels: ['gpt-5.5', 'gpt-5.6-sol']
  }, access)
  repositories.recordAccountHealthCheckSuccess(account.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  const fallbackAccount = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '通用响应后备账号',
    type: 'api_key',
    credentials: { api_key: 'sk-generic-opaque-good', base_url: upstreamBaseUrl },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 1,
    priority: 10,
    fallbackEnabled: true,
    supportedModels: ['gpt-5.5', 'gpt-5.6-sol']
  }, access)
  repositories.recordAccountHealthCheckSuccess(fallbackAccount.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '通用响应透传 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key)
  const imageGroup = repositories.createGroup({ name: '通用图片接管分组', providerCode: GPT_VENDOR_CODE, enabled: true }, access)
  const imageBadAccount = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '通用图片首选失败账号',
    type: 'api_key',
    credentials: { api_key: 'sk-generic-image-bad', base_url: upstreamBaseUrl },
    groupId: imageGroup.id,
    supportedModels: ['gpt-image-1'],
    healthCheckModel: 'gpt-image-1',
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  const imageGoodAccount = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '通用图片后备成功账号',
    type: 'api_key',
    credentials: { api_key: 'sk-generic-image-good', base_url: upstreamBaseUrl },
    groupId: imageGroup.id,
    supportedModels: ['gpt-image-1'],
    healthCheckModel: 'gpt-image-1',
    status: 'active',
    schedulable: true,
    priority: 10,
    fallbackEnabled: true
  }, access)
  for (const imageAccount of [imageBadAccount, imageGoodAccount]) {
    repositories.recordAccountHealthCheckSuccess(imageAccount.id, {
      intervalHours: 12,
      jitterMinutes: 0,
      failureThreshold: 3,
      statusCode: 200
    })
  }
  const imageApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '通用图片接管 Key',
    groupBindings: [{ groupId: imageGroup.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(imageApiKey.key)
  repositories.updateSystemAccount(access.systemAccountId, { imageGenerationEnabled: true })

  appServer = http.createServer(app)
  await listen(appServer)
  const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

  const nonStream = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: { authorization: `Bearer ${apiKey.key}`, 'content-type': 'application/json' },
    body: JSON.stringify({ model: 'gpt-5.5', messages: [{ role: 'user', content: 'opaque status' }], stream: false })
  })
  const nonStreamText = await nonStream.text()
  assert.equal(nonStream.status, 200, `通用客户端应由服务端完成 HTTP 失败接管，实际 ${nonStream.status}: ${nonStreamText}`)
  assert.match(nonStreamText, /server failover completed/)
  assert.deepEqual(upstreamAuthorizations, [
    'Bearer sk-generic-opaque-bad-a',
    'Bearer sk-generic-opaque-good'
  ], '通用完整 HTTP 失败不得同账户换 Key 重放，只能切到下一账户')

  const imageHitOffset = upstreamAuthorizations.length
  const image = await fetch(`${baseUrl}/v1/images/generations`, {
    method: 'POST',
    headers: { authorization: `Bearer ${imageApiKey.key}`, 'content-type': 'application/json' },
    body: JSON.stringify({ model: 'gpt-image-1', prompt: 'server side failover' })
  })
  const imageText = await image.text()
  assert.equal(image.status, 200, `图片请求应复用通用服务端接管，实际 ${image.status}: ${imageText}`)
  assert.match(imageText, /aW1hZ2U=/)
  assert.deepEqual(upstreamAuthorizations.slice(imageHitOffset), [
    'Bearer sk-generic-image-bad',
    'Bearer sk-generic-image-good'
  ], '图片 HTTP 失败必须与文本一样切换后备账号')

  const stream = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: { authorization: `Bearer ${apiKey.key}`, 'content-type': 'application/json', accept: 'text/event-stream' },
    body: JSON.stringify({ model: 'gpt-5.5', input: 'opaque stream event', stream: true })
  })
  const streamText = await stream.text()
  assert.equal(stream.status, 200)
  assert.equal(hits.length, 5, `通用 SSE 必须继续派发上游，且完整 HTTP 失败不得同账户换 Key 重放，实际响应：${streamText}`)
  assert.match(streamText, /vendor_invented_stream_error/, '通用 SSE 必须原样保留上游失败事件')
  assert.doesNotMatch(streamText, /upstream_retryable_error/, '通用 SSE 不得改写成专用客户端错误码')

  const heldConflictSlot = tryAcquireAccountConcurrency(account.id, 1)
  const heldFallbackConflictSlot = tryAcquireAccountConcurrency(fallbackAccount.id, 1)
  assert.equal(heldConflictSlot.acquired, true, 'SSE/非流式传输冲突回归前应占用首账号并发槽')
  assert.equal(heldFallbackConflictSlot.acquired, true, 'SSE/非流式传输冲突回归前应占用后备账号并发槽')
  const conflictReleaseTimer = setTimeout(() => {
    heldConflictSlot.release()
    heldFallbackConflictSlot.release()
  }, 250)
  const transportConflict = await fetch(`${baseUrl}/v1/chat/completions?mock_sse_wait_non_stream=1`, {
    method: 'POST',
    headers: { authorization: `Bearer ${apiKey.key}`, 'content-type': 'application/json', accept: 'text/event-stream' },
    body: JSON.stringify({ model: 'gpt-5.5', messages: [{ role: 'user', content: 'wait then non-stream' }], stream: true })
  })
  assert.equal(transportConflict.status, 200, '等待阶段已发 SSE 心跳后，HTTP 状态已固定为 200')
  await assert.rejects(
    () => transportConflict.text(),
    /terminated|aborted|socket|closed/i,
    '上游改为非流式响应时应断开 SSE 连接交给通用客户端重试，不得把 JSON 拼进 SSE'
  )
  clearTimeout(conflictReleaseTimer)
  heldConflictSlot.release()
  heldFallbackConflictSlot.release()
  assert.equal(hits.length, 6, 'SSE/非流式传输冲突不得按响应类型切换上游账户')

  const accountAfter = repositories.findAccountForTest(account.id, access)
  assert.equal(accountAfter?.status, 'active', '通用响应状态和错误类型不得修改账号状态')
  assert.equal(accountAfter?.schedulable, true, '通用响应不得把账号改为不可调度')
  assert.equal(accountAfter?.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, '通用未知错误不得持久化 Key 临时不可用状态')
  const genericRuntimeSnapshot = accountSideEffects.snapshotGatewayAccountRuntimeAvailability()
  for (const genericAccount of [account, fallbackAccount, imageBadAccount, imageGoodAccount]) {
    assert.equal(genericRuntimeSnapshot[genericAccount.id], undefined, `通用用户请求不得写账户运行态：${genericAccount.name}`)
  }

  const codexStream = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({ turn_id: `turn-generic-opaque-${Date.now()}` })
    },
    body: JSON.stringify({ model: 'gpt-5.5', input: 'known codex stream event', stream: true })
  })
  const codexStreamText = await codexStream.text()
  assert.equal(codexStream.status, 200)
  assert.match(codexStreamText, /upstream_retryable_error/, '明确 Codex 画像应继续使用专用协议可重试信号')
  assert.equal(hits.length, 8, '明确客户端语义处理应尝试两个账号，耗尽后不得无限重派')
  const codexRuntimeSnapshot = accountSideEffects.snapshotGatewayAccountRuntimeAvailability()
  for (const codexAccount of [account, fallbackAccount]) {
    assert.equal(codexRuntimeSnapshot[codexAccount.id], undefined, `明确客户端失败只允许影响当前请求，不得写账户运行态：${codexAccount.name}`)
  }

  proxyHealth.clearGatewayProxyHealthForTest()
  const accountSecret = repositories.findOpenAIAccountForGroup(group.id, account.id, access.systemAccountId)
  const fallbackAccountSecret = repositories.findOpenAIAccountForGroup(group.id, fallbackAccount.id, access.systemAccountId)
  assert(accountSecret && fallbackAccountSecret, '上游桶 E2E 必须读取真实调度账户凭据')
  proxyHealth.recordGatewayUpstreamBucketFailure(accountSecret, 'background_probe_confirmed_failure', { bucketScope: 'upstream' })
  proxyHealth.recordGatewayUpstreamBucketFailure(fallbackAccountSecret, 'background_probe_confirmed_failure', { bucketScope: 'upstream' })
  const unrelatedBucketAccount = {
    ...accountSecret,
    id: 'account-unrelated-bucket-sentinel',
    baseUrl: 'https://unrelated-bucket.example/v1'
  }
  assert.equal(
    proxyHealth.orderOpenAIAccountsByGatewayProxyHealth([accountSecret, fallbackAccountSecret, unrelatedBucketAccount]).applied,
    true,
    '回归前应先建立后台确认的共享上游桶避让'
  )
  const codexBucketSuccess = await fetch(`${baseUrl}/v1/responses?mock_codex_success=1`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({ turn_id: `turn-bucket-success-${Date.now()}` })
    },
    body: JSON.stringify({ model: 'gpt-5.5', input: 'user success must not clear probe state', stream: true })
  })
  assert.equal(codexBucketSuccess.status, 200)
  assert.match(await codexBucketSuccess.text(), /response.completed/)
  assert.equal(
    proxyHealth.orderOpenAIAccountsByGatewayProxyHealth([accountSecret, fallbackAccountSecret, unrelatedBucketAccount]).applied,
    true,
    '精确客户端普通成功不得清理后台确认的全局上游桶运行态'
  )
  assert.equal(await proxyHealth.recordGatewayUpstreamBucketSuccessAsync(accountSecret), true, '后台探针成功入口应能清理上游桶运行态')
  assert.equal(
    proxyHealth.orderOpenAIAccountsByGatewayProxyHealth([accountSecret, fallbackAccountSecret, unrelatedBucketAccount]).applied,
    false,
    '后台探针确认成功后才允许恢复上游桶运行态'
  )
  proxyHealth.clearGatewayProxyHealthForTest()

  console.log('gateway generic upstream opaque regression passed')
} finally {
  await closeServer(appServer)
  await closeServer(upstreamServer)
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  await readWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.getBusinessDatabase().close()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
    server.listen(0, '127.0.0.1')
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert(typeof address === 'object' && address !== null)
  return { port: address.port }
}

function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server || !server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}
