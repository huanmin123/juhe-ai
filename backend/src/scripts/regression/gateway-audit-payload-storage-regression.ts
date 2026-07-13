import { strict as assert } from 'node:assert'
import { existsSync, mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express from 'express'

import { backendRoot, runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-audit-payload-storage-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-audit-payload-storage-secret'
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
  auditLogQueue,
  usageRecordQueue,
  gatewayCache,
  accountSideEffects,
  readWorkerPool
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../modules/gateway/request/body-middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const model = 'gpt-5.4-mini'

auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)

const nonStreamSuccessBody = JSON.stringify({
  id: 'resp_audit_non_stream_success',
  object: 'response',
  status: 'completed',
  model,
  output: [{ type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'audit success ok' }] }],
  usage: { input_tokens: 3, output_tokens: 2 }
})
const nonStreamImageResultBase64 = Buffer.from('audit non stream image result', 'utf8').toString('base64')
const nonStreamImageSuccessBody = JSON.stringify({
  id: 'resp_audit_non_stream_image_success',
  object: 'response',
  status: 'completed',
  model,
  output: [{
    id: 'img_audit_non_stream_success',
    type: 'image_generation_call',
    status: 'completed',
    result: nonStreamImageResultBase64,
    revised_prompt: 'audit non stream image prompt'
  }],
  usage: { input_tokens: 3, output_tokens: 2 }
})
const retryFailureBody = JSON.stringify({
  error: { message: 'first account failed before audit retry success', type: 'server_error', code: 'audit_retry_first_failed' }
})
const retrySuccessBody = JSON.stringify({
  id: 'chatcmpl_audit_retry_success',
  object: 'chat.completion',
  choices: [{ index: 0, message: { role: 'assistant', content: 'audit retry success ok' }, finish_reason: 'stop' }],
  usage: { prompt_tokens: 3, completion_tokens: 2, total_tokens: 5 }
})
const allFailureBody = JSON.stringify({
  error: { message: 'single upstream failed for audit storage', type: 'server_error', code: 'audit_single_failure' }
})

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  settingsRepository.updateSettings({
    temporaryUnschedulableRetryAttempts: 0,
    streamRequestTimeoutSeconds: 10,
    streamIdleTimeoutSeconds: 10
  })
  gatewayCache.clearGatewayRuntimeCache()

  const upstreamServer = createMockOpenAIUpstream()
  await listen(upstreamServer)
  const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`
  const appServer = http.createServer(app)
  await listen(appServer)
  const gatewayBaseUrl = `http://127.0.0.1:${serverPort(appServer)}`

  try {
    await assertHotRetainedNonStreamSuccess(gatewayBaseUrl, upstreamBaseUrl)
    await assertSuccessAfterRetryCapturesFinalUpstreamResponse(gatewayBaseUrl, upstreamBaseUrl)
    await assertAllUpstreamFailureCapturesUpstreamResponse(gatewayBaseUrl, upstreamBaseUrl)
    await assertHotRetainedStreamSuccess(gatewayBaseUrl, upstreamBaseUrl)
    await assertUnsampledStreamFailureCapturesUpstreamResponse(gatewayBaseUrl, upstreamBaseUrl)
    await assertImageStreamFailureOmissionPreservesRequestPayloads(gatewayBaseUrl, upstreamBaseUrl)
    await assertImageStreamSuccessOmissionRecordsMetadata(gatewayBaseUrl, upstreamBaseUrl)
    await assertNonStreamImageSuccessOmissionRecordsMetadata(gatewayBaseUrl, upstreamBaseUrl)
    await assertMissingPayloadBlobReportsStatusAndRepairsAsync(gatewayBaseUrl, upstreamBaseUrl)
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }

  console.log('网关审计 payload 存储回归通过：非流式成功、先失败后成功、全失败、流式成功、流式失败、图像流省略和非流式图像省略均符合预期')
} finally {
  usageRecordQueue.flushAllUsageRecordQueue()
  auditLogQueue.flushAllAuditLogQueue()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  try {
    cleanupAuditBlobFilesForTest()
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    await readWorkerPool.closeSqliteReadWorkerPool()
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertHotRetainedNonStreamSuccess(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const seeded = seedGatewayRoute(upstreamBaseUrl, '审计成功热保留', ['sk-audit-non-stream-success'])
  const traceId = 'trace-audit-non-stream-success-hot-retention'

  const response = await fetch(`${gatewayBaseUrl}/v1/responses`, {
    method: 'POST',
    headers: gatewayHeaders(seeded.apiKey, traceId),
    body: JSON.stringify({
      model,
      input: 'audit non stream success',
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `非流式成功应返回 200，实际 ${response.status}: ${text}`)
  assert.equal(text, nonStreamSuccessBody, '非流式成功响应体应完整透传')

  const detail = auditDetailByTrace(traceId)
  assert.equal(detail.auditOutcome, 'success', '成功热保留请求应写入 success 审计')
  assert(detail.httpCompletedAt, '成功审计应保存 HTTP 返回客户端时间')
  assert.equal(typeof detail.httpDurationMs, 'number', '成功审计应保存 HTTP 客户端耗时')
  assert((detail.durationMs ?? 0) >= (detail.httpDurationMs ?? 0), '审计总耗时不得早于 HTTP 客户端耗时')
  await assertPayloadBodyEquals(detail, 'upstream_response', nonStreamSuccessBody)
  await assertPayloadBodyEquals(detail, 'gateway_response', nonStreamSuccessBody)
  await assertPayloadBodyContains(detail, 'client_request', 'audit non stream success')
  await assertPayloadBodyContains(detail, 'upstream_request', 'audit non stream success')
}

async function assertSuccessAfterRetryCapturesFinalUpstreamResponse(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const seeded = seedGatewayRoute(upstreamBaseUrl, '审计先失败后成功', ['sk-audit-retry-fail', 'sk-audit-retry-success'])
  const traceId = 'trace-audit-success-after-retry'

  const response = await fetch(`${gatewayBaseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: gatewayHeaders(seeded.apiKey, traceId),
    body: JSON.stringify({
      model,
      messages: [{ role: 'user', content: 'audit retry success' }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `先失败后成功应返回 200，实际 ${response.status}: ${text}`)
  assert.equal(text, retrySuccessBody, '先失败后成功最终响应体应来自第二账号')

  const detail = auditDetailByTrace(traceId)
  assert.equal(detail.auditOutcome, 'success_after_retry', '先失败后成功应写入 success_after_retry 审计')
  assert(detail.attempts.length >= 2, `先失败后成功应至少有两次上游尝试，实际 ${detail.attempts.length}`)
  const failedAttemptId = detail.attempts[0]?.id
  const successAttemptId = detail.attempts[1]?.id
  assert(failedAttemptId && successAttemptId, '先失败后成功审计缺少 attempt id')
  await assertPayloadBodyEquals(detail, 'upstream_response', retryFailureBody, failedAttemptId)
  await assertPayloadBodyEquals(detail, 'upstream_response', retrySuccessBody, successAttemptId)
  await assertPayloadBodyEquals(detail, 'gateway_response', retrySuccessBody)
}

async function assertAllUpstreamFailureCapturesUpstreamResponse(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const seeded = seedGatewayRoute(upstreamBaseUrl, '审计全失败', ['sk-audit-all-fail'])
  const traceId = 'trace-audit-all-upstream-failure'

  const response = await fetch(`${gatewayBaseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: gatewayHeaders(seeded.apiKey, traceId),
    body: JSON.stringify({
      model,
      messages: [{ role: 'user', content: 'audit all upstream failure' }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 503, `全失败应返回统一 503，实际 ${response.status}: ${text}`)

  const detail = auditDetailByTrace(traceId)
  assert.equal(detail.auditOutcome, 'upstream_failed', '全失败应写入 upstream_failed 审计')
  await assertPayloadBodyEquals(detail, 'upstream_response', allFailureBody, detail.attempts[0]?.id)
  await assertPayloadBodyContains(detail, 'gateway_error', '没有可用的上游账户')
}

async function assertHotRetainedStreamSuccess(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const seeded = seedGatewayRoute(upstreamBaseUrl, '审计流式成功', ['sk-audit-stream-success'])
  const traceId = 'trace-audit-stream-success-hot-retention'

  const response = await fetch(`${gatewayBaseUrl}/v1/responses`, {
    method: 'POST',
    headers: gatewayHeaders(seeded.apiKey, traceId),
    body: JSON.stringify({
      model,
      input: 'audit stream success',
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `流式成功应返回 200，实际 ${response.status}: ${text}`)
  assert.match(text, /response\.completed/, '流式成功响应应包含 completed 事件')

  const detail = auditDetailByTrace(traceId)
  assert.equal(detail.auditOutcome, 'success', '成功热保留流式请求应写入 success 审计')
  await assertPayloadBodyContains(detail, 'upstream_response', 'response.completed')
  await assertPayloadBodyContains(detail, 'gateway_response', 'response.completed')
}

async function assertUnsampledStreamFailureCapturesUpstreamResponse(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const seeded = seedGatewayRoute(upstreamBaseUrl, '审计流式失败', ['sk-audit-stream-failure'])
  const traceId = 'trace-audit-stream-failure'

  const response = await fetch(`${gatewayBaseUrl}/v1/responses`, {
    method: 'POST',
    headers: gatewayHeaders(seeded.apiKey, traceId),
    body: JSON.stringify({
      model,
      input: 'audit stream failure',
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `流式失败会按 SSE 200 透传失败事件，实际 ${response.status}: ${text}`)
  assert.match(text, /response\.failed/, '流式失败响应应包含 failed 事件')

  const detail = auditDetailByTrace(traceId)
  assert.equal(detail.auditOutcome, 'stream_failed', '流式失败应写入 stream_failed 审计')
  await assertPayloadBodyContains(detail, 'upstream_response', 'response.failed')
  await assertPayloadBodyContains(detail, 'gateway_response', 'response.failed')
}

async function assertImageStreamFailureOmissionPreservesRequestPayloads(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const seeded = seedGatewayRoute(upstreamBaseUrl, '审计图像流失败省略', ['sk-audit-image-stream-failure'])
  const traceId = 'trace-audit-image-stream-failure-omission'

  const response = await fetch(`${gatewayBaseUrl}/v1/responses`, {
    method: 'POST',
    headers: gatewayHeaders(seeded.apiKey, traceId),
    body: JSON.stringify({
      model,
      input: 'audit image stream failure should keep request payload',
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `图像流失败会按 SSE 200 透传失败事件，实际 ${response.status}: ${text}`)
  assert.match(text, /response\.failed/, '图像流失败响应应包含 failed 事件')

  const detail = auditDetailByTrace(traceId)
  assert.equal(detail.auditOutcome, 'stream_failed', '图像流失败应写入 stream_failed 审计')
  await assertPayloadBodyContains(detail, 'client_request', 'audit image stream failure should keep request payload')
  await assertPayloadBodyContains(detail, 'upstream_request', 'audit image stream failure should keep request payload')
}

async function assertImageStreamSuccessOmissionRecordsMetadata(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const seeded = seedGatewayRoute(upstreamBaseUrl, '审计图像流成功省略', ['sk-audit-image-stream-success'])
  const traceId = 'trace-audit-image-stream-success-omission'

  const response = await fetch(`${gatewayBaseUrl}/v1/responses`, {
    method: 'POST',
    headers: gatewayHeaders(seeded.apiKey, traceId),
    body: JSON.stringify({
      model,
      input: 'audit image stream success should keep stream body',
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `图像流成功应返回 200，实际 ${response.status}: ${text}`)
  assert.match(text, /partial_image_b64/, '图像流成功响应应包含图片增量事件')

  const detail = auditDetailByTrace(traceId)
  assert.equal(detail.auditOutcome, 'success', '图像流成功热保留应写入 success 审计')
  await assertStreamBodyOmissionMetadata(detail)
}

async function assertNonStreamImageSuccessOmissionRecordsMetadata(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const seeded = seedGatewayRoute(upstreamBaseUrl, '审计非流式图像成功省略', ['sk-audit-image-json-success'])
  const traceId = 'trace-audit-image-json-success-omission'

  const response = await fetch(`${gatewayBaseUrl}/v1/responses`, {
    method: 'POST',
    headers: gatewayHeaders(seeded.apiKey, traceId),
    body: JSON.stringify({
      model,
      input: 'audit image non stream success should omit image result',
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `非流式图像成功应返回 200，实际 ${response.status}: ${text}`)
  assert.equal(text, nonStreamImageSuccessBody, '非流式图像成功响应体应完整返回给客户端')
  assert.match(text, new RegExp(nonStreamImageResultBase64.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), '客户端响应应包含图像 result')

  const detail = auditDetailByTrace(traceId)
  assert.equal(detail.auditOutcome, 'success', '非流式图像成功应写入 success 审计')
  await assertBodyOmissionMetadata(detail, 'non_stream_body_omission', 'image_json_payload', 0)
  await assertPayloadBodyContains(detail, 'client_request', 'audit image non stream success should omit image result')
  await assertPayloadBodyContains(detail, 'upstream_request', 'audit image non stream success should omit image result')
  for (const payload of detail.payloads) {
    if (!payload.hasBody || payload.partType === 'gateway_metadata') continue
    const payloadDetail = await repositories.getAuditLogPayload(detail.id, payload.id, { limit: 1024 * 1024 })
    assert(!payloadDetail?.bodyText?.includes(nonStreamImageResultBase64), `${detail.traceId} 非流式图像 result 不应保存在 ${payload.partType}`)
    assert(!payloadDetail?.bodyText?.includes('"result"'), `${detail.traceId} 非流式图像 response JSON 不应保存在 ${payload.partType}`)
  }
}

async function assertMissingPayloadBlobReportsStatusAndRepairsAsync(gatewayBaseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const seeded = seedGatewayRoute(upstreamBaseUrl, '审计缺失文件状态', ['sk-audit-missing-blob-status'])
  const traceId = 'trace-audit-missing-blob-status'

  const response = await fetch(`${gatewayBaseUrl}/v1/responses`, {
    method: 'POST',
    headers: gatewayHeaders(seeded.apiKey, traceId),
    body: JSON.stringify({
      model,
      input: 'audit missing blob status',
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `缺失文件状态样本应返回 200，实际 ${response.status}: ${text}`)

  const detail = auditDetailByTrace(traceId)
  const payload = detail.payloads.find((item) => item.partType === 'gateway_response')
  assert(payload?.hasBody, '缺失文件状态样本应先保存 gateway_response body')
  const payloadBefore = await repositories.getAuditLogPayload(detail.id, payload.id, { limit: 1024 * 1024 })
  assert.equal(payloadBefore?.bodyStorageStatus, 'available', '删除文件前 payload 应可读取')
  assert.equal(payloadBefore?.bodyText, nonStreamSuccessBody, '删除文件前 payload 正文应完整')

  const storageKey = payloadBodyStorageKey(payload.id)
  const filePath = auditBlobFilePath(storageKey)
  rmSync(filePath, { force: true })
  assert.equal(existsSync(filePath), false, '测试应能删除审计 blob 文件')

  const payloadMissing = await repositories.getAuditLogPayload(detail.id, payload.id, { limit: 1024 * 1024 })
  assert.equal(payloadMissing?.bodyStorageStatus, 'file_missing', 'blob 文件缺失时 payload API 应返回 file_missing')
  assert.equal(payloadMissing?.bodyBytesReturned, 0, 'blob 文件缺失时不应返回正文 bytes')
  assert.equal(payloadMissing?.bodyTotalBytes, Buffer.byteLength(nonStreamSuccessBody, 'utf8'), 'blob 文件缺失时仍应返回 DB 元数据中的原始大小')
  assert.equal(payloadMissing?.bodyText, undefined, 'blob 文件缺失时不应伪造正文')

  const now = new Date().toISOString()
  auditLogQueue.enqueueAuditLogsLocal([{
    traceId: 'trace-audit-missing-blob-async-repair',
    trafficSource: 'gateway',
    method: 'POST',
    path: '/v1/responses',
    model,
    stream: false,
    auditOutcome: 'success',
    success: true,
    finalStatusCode: 200,
    sampleBucket: 0,
    sampleReason: 'missing_blob_async_repair',
    startedAt: now,
    endedAt: now,
    attempts: [],
    payloads: [{
      partType: 'gateway_response',
      body: nonStreamSuccessBody,
      contentType: payload.contentType
    }]
  }])
  await auditLogQueue.flushAllAuditLogQueueAsync()
  assert.equal(existsSync(filePath), true, '异步审计批量写入遇到已有 blob 元数据时应补回缺失文件')

  const payloadRepaired = await repositories.getAuditLogPayload(detail.id, payload.id, { limit: 1024 * 1024 })
  assert.equal(payloadRepaired?.bodyStorageStatus, 'available', '补回文件后原 payload 应恢复可读取状态')
  assert.equal(payloadRepaired?.bodyText, nonStreamSuccessBody, '补回文件后原 payload 正文应可读')
}

function seedGatewayRoute(upstreamBaseUrl: string, label: string, upstreamKeys: string[]): { apiKey: string; groupId: string } {
  const group = repositories.createGroup({
    name: `${label}分组`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  for (const [index, upstreamKey] of upstreamKeys.entries()) {
    repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `${label}账户-${String(index + 1).padStart(2, '0')}`,
      type: 'api_key',
      credentials: {
        api_key: upstreamKey,
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true
    }, access)
  }
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `${label} API Key`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, `${label} API Key 未返回明文密钥`)
  gatewayCache.clearGatewayRuntimeCache()
  return { apiKey: apiKey.key, groupId: group.id }
}

function createMockOpenAIUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(Buffer.from(chunk)))
    req.on('end', () => {
      const url = new URL(req.url ?? '/', 'http://127.0.0.1')
      const body = parseJson(Buffer.concat(chunks).toString('utf8'))
      const authorization = String(req.headers.authorization ?? '')
      if (url.pathname === '/v1/chat/completions') {
        if (authorization.includes('sk-audit-retry-fail')) {
          sendJson(res, 502, retryFailureBody)
          return
        }
        if (authorization.includes('sk-audit-all-fail')) {
          sendJson(res, 418, allFailureBody)
          return
        }
        sendJson(res, 200, retrySuccessBody)
        return
      }
      if (url.pathname === '/v1/responses') {
        if (String(body.input ?? '').includes('image non stream success')) {
          sendJson(res, 200, nonStreamImageSuccessBody)
          return
        }
        if (body.stream === true && String(body.input ?? '').includes('image stream failure')) {
          sendImageStreamFailure(res)
          return
        }
        if (body.stream === true && String(body.input ?? '').includes('image stream success')) {
          sendImageStreamSuccess(res)
          return
        }
        if (body.stream === true && String(body.input ?? '').includes('failure')) {
          sendStreamFailure(res)
          return
        }
        if (body.stream === true) {
          sendStreamSuccess(res)
          return
        }
        sendJson(res, 200, nonStreamSuccessBody)
        return
      }
      sendJson(res, 404, JSON.stringify({ error: { message: 'mock upstream path not found' } }))
    })
  })
}

function sendJson(res: http.ServerResponse, statusCode: number, body: string): void {
  res.writeHead(statusCode, { 'content-type': 'application/json; charset=utf-8' })
  res.end(body)
}

function sendStreamSuccess(res: http.ServerResponse): void {
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache'
  })
  res.write(`event: response.output_text.delta\ndata: ${JSON.stringify({ type: 'response.output_text.delta', delta: 'OK' })}\n\n`)
  res.write(`event: response.completed\ndata: ${JSON.stringify({ type: 'response.completed', response: { status: 'completed', usage: { input_tokens: 3, output_tokens: 2 } } })}\n\n`)
  res.end()
}

function sendStreamFailure(res: http.ServerResponse): void {
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache'
  })
  res.write(`event: response.failed\ndata: ${JSON.stringify({
    type: 'response.failed',
    response: {
      status: 'failed',
      error: { message: 'mock stream failed for audit storage', code: 'audit_stream_failed' }
    }
  })}\n\n`)
  res.end()
}

function sendImageStreamSuccess(res: http.ServerResponse): void {
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache'
  })
  res.write(`event: response.output_item.added\ndata: ${JSON.stringify({
    type: 'response.output_item.added',
    item: { id: 'img_audit_success', type: 'image_generation_call' }
  })}\n\n`)
  res.write(`event: response.image_generation_call.partial_image\ndata: ${JSON.stringify({
    type: 'response.image_generation_call.partial_image',
    partial_image_b64: 'aW1hZ2UtYXVkaXQtc3VjY2Vzcw=='
  })}\n\n`)
  res.write(`event: response.completed\ndata: ${JSON.stringify({
    type: 'response.completed',
    response: {
      status: 'completed',
      output: [{ id: 'img_audit_success', type: 'image_generation_call' }],
      usage: { input_tokens: 3, output_tokens: 2 }
    }
  })}\n\n`)
  res.end()
}

function sendImageStreamFailure(res: http.ServerResponse): void {
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache'
  })
  res.write(`event: response.output_item.added\ndata: ${JSON.stringify({
    type: 'response.output_item.added',
    item: { id: 'img_audit_failure', type: 'image_generation_call' }
  })}\n\n`)
  res.write(`event: response.image_generation_call.partial_image\ndata: ${JSON.stringify({
    type: 'response.image_generation_call.partial_image',
    partial_image_b64: 'aW1hZ2UtYXVkaXQtZmFpbHVyZQ=='
  })}\n\n`)
  res.write(`event: response.failed\ndata: ${JSON.stringify({
    type: 'response.failed',
    response: {
      status: 'failed',
      error: { message: 'mock image stream failed for audit storage', code: 'audit_image_stream_failed' }
    }
  })}\n\n`)
  res.end()
}

function auditDetailByTrace(traceId: string): NonNullable<ReturnType<typeof repositories.getAuditLogDetail>> {
  auditLogQueue.flushAllAuditLogQueue()
  const list = repositories.listAuditLogs({ traceId, pageSize: 10 })
  assert.equal(list.total, 1, `trace ${traceId} 应只有一条审计记录，实际 ${list.total}`)
  const detail = repositories.getAuditLogDetail(list.items[0]?.id ?? '')
  assert(detail, `trace ${traceId} 审计详情不存在`)
  return detail
}

async function assertPayloadBodyEquals(
  detail: NonNullable<ReturnType<typeof repositories.getAuditLogDetail>>,
  partType: string,
  expectedBody: string,
  attemptId?: string
): Promise<void> {
  const payload = await readPayload(detail, partType, attemptId)
  assert.equal(payload.bodyText, expectedBody, `${detail.traceId} ${partType} 正文不匹配`)
  assert.equal(payload.bodyTotalBytes, Buffer.byteLength(expectedBody, 'utf8'), `${detail.traceId} ${partType} 正文字节数不匹配`)
}

async function assertPayloadBodyContains(
  detail: NonNullable<ReturnType<typeof repositories.getAuditLogDetail>>,
  partType: string,
  expectedText: string,
  attemptId?: string
): Promise<void> {
  const payload = await readPayload(detail, partType, attemptId)
  assert(
    payload.bodyText?.includes(expectedText),
    `${detail.traceId} ${partType} 正文应包含 ${expectedText}，实际 ${payload.bodyText ?? payload.bodyBase64 ?? '[empty]'}`
  )
  assert((payload.bodyTotalBytes ?? 0) > 0, `${detail.traceId} ${partType} 正文字节数应大于 0`)
}

async function assertStreamBodyOmissionMetadata(
  detail: NonNullable<ReturnType<typeof repositories.getAuditLogDetail>>
): Promise<void> {
  await assertBodyOmissionMetadata(detail, 'stream_body_omission', 'image_stream_payload')
}

async function assertBodyOmissionMetadata(
  detail: NonNullable<ReturnType<typeof repositories.getAuditLogDetail>>,
  label: string,
  reason: string,
  minOmittedPayloadCount = 1
): Promise<void> {
  let metadata: {
    label?: string
    metadata?: {
      reason?: string
      auditBodyPayloadsOmitted?: boolean
      omittedPayloadCount?: number
    }
  } | undefined
  for (const payload of detail.payloads.filter((item) => item.partType === 'gateway_metadata' && item.hasBody)) {
    const payloadDetail = await repositories.getAuditLogPayload(detail.id, payload.id, { limit: 1024 * 1024 })
    const parsed = JSON.parse(payloadDetail?.bodyText ?? '{}') as typeof metadata
    if (parsed?.label === label) {
      metadata = parsed
      break
    }
  }
  assert(metadata, `${detail.traceId} 应记录 ${label} 正文省略元数据`)
  assert.equal(metadata.metadata?.reason, reason, `${detail.traceId} 省略原因应为 ${reason}`)
  assert.equal(metadata.metadata?.auditBodyPayloadsOmitted, true, `${detail.traceId} 应标记审计 body 已省略`)
  assert((metadata.metadata?.omittedPayloadCount ?? 0) >= minOmittedPayloadCount, `${detail.traceId} 应至少省略 ${minOmittedPayloadCount} 个 payload body`)

  for (const payload of detail.payloads) {
    if (!payload.hasBody || payload.partType === 'gateway_metadata') continue
    const payloadDetail = await repositories.getAuditLogPayload(detail.id, payload.id, { limit: 1024 * 1024 })
    assert(!payloadDetail?.bodyText?.includes('partial_image_b64'), `${detail.traceId} 图像流正文不应继续保留在 ${payload.partType}`)
  }
}

async function readPayload(
  detail: NonNullable<ReturnType<typeof repositories.getAuditLogDetail>>,
  partType: string,
  attemptId?: string
): Promise<NonNullable<Awaited<ReturnType<typeof repositories.getAuditLogPayload>>>> {
  const payload = detail.payloads.find((item) => item.partType === partType && (attemptId === undefined || item.attemptId === attemptId))
  assert(payload, `${detail.traceId} 缺少 ${partType}${attemptId ? ` attempt=${attemptId}` : ''} payload`)
  assert(payload.hasBody, `${detail.traceId} ${partType} payload 没有 body blob`)
  const payloadDetail = await repositories.getAuditLogPayload(detail.id, payload.id, { limit: 1024 * 1024 })
  assert(payloadDetail, `${detail.traceId} ${partType} payload 详情不存在`)
  return payloadDetail
}

function payloadBodyStorageKey(payloadId: string): string {
  const row = databaseModule.getDatasetDatabase()
    .prepare(`
      SELECT b.storage_key
      FROM audit_payload_refs r
      INNER JOIN audit_payload_blobs b ON b.id = r.body_blob_id
      WHERE r.id = ?
    `)
    .get(payloadId) as { storage_key?: string } | undefined
  assert(row?.storage_key, `payload ${payloadId} 缺少 body blob storage_key`)
  return row.storage_key
}

function cleanupAuditBlobFilesForTest(): void {
  try {
    const rows = databaseModule.getDatasetDatabase()
      .prepare('SELECT storage_key FROM audit_payload_blobs')
      .all() as Array<{ storage_key?: string }>
    for (const row of rows) {
      if (row.storage_key) {
        rmSync(auditBlobFilePath(row.storage_key), { force: true })
      }
    }
  } catch {
  }
}

function auditBlobFilePath(storageKey: string): string {
  return resolve(backendRoot, 'data', 'audit', 'blobs', storageKey)
}

function gatewayHeaders(apiKey: string, traceId: string): Record<string, string> {
  return {
    authorization: `Bearer ${apiKey}`,
    'content-type': 'application/json',
    'x-trace-id': traceId
  }
}

function parseJson(value: string): Record<string, unknown> {
  if (!value.trim()) return {}
  try {
    const parsed = JSON.parse(value) as unknown
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
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
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}
