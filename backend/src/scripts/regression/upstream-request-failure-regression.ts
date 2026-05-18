import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { captureGatewayRawBody } from '../../modules/gateway/openai-gateway-request-body-middleware.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-upstream-request-failure-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'upstream-request-failure.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'upstream-request-failure-records.sqlite3')
runtimeConfig.secret = 'upstream-request-failure-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  cryptoModule,
  repositories,
  settingsRepository,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/openai-gateway.routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/crypto.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/gateway-runtime-cache.service.js'),
  import('../../modules/gateway/gateway-account-side-effects.service.js'),
  import('../../modules/gateway/usage-record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

async function main(): Promise<void> {
  let appServer: http.Server | undefined
  let upstreamServer: http.Server | undefined
  try {
    settingsRepository.updateSettings({ temporaryUnschedulableRetryAttempts: 0 })
    gatewayCache.clearGatewayRuntimeCache()

    upstreamServer = createRejectedRequestUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

    const group = repositories.createGroup({ name: '请求级失败回归分组', providerCode: 'openai', enabled: true })
    const firstAccount = repositories.createAccount({
      providerCode: 'openai',
      name: '01-请求级失败回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-request-failure-1',
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true
    })
    const secondAccount = repositories.createAccount({
      providerCode: 'openai',
      name: '02-请求级失败回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-request-failure-2',
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true
    })
    const apiKey = createRegressionApiKey(group.id)

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const invalidJsonHitsBefore = totalUpstreamHitCount()
    const invalidJsonResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: '{"model":"gpt-4o-mini",'
    })
    const invalidJsonText = await invalidJsonResponse.text()
    assert.equal(invalidJsonResponse.status, 400, `无效 JSON 应由网关直接拒绝，实际 HTTP ${invalidJsonResponse.status}: ${invalidJsonText}`)
    assert.match(invalidJsonText, /请求体不是合法 JSON/, `无效 JSON 响应应说明请求体错误：${invalidJsonText}`)
    assert.equal(totalUpstreamHitCount(), invalidJsonHitsBefore, '无效 JSON 不应转发到任何上游账号')

    currentScenario = 'tool_output_missing_feature'
    const featureResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'bad tool state feature' }],
        stream: false
      })
    })
    const featureResponseText = await featureResponse.text()

    assert.equal(featureResponse.status, 400, `工具输出缺失特征应把上游 400 返回客户端，实际 HTTP ${featureResponse.status}: ${featureResponseText}`)
    assert.equal(featureResponseText, toolOutputMissingRejectedRequestBody, `客户端收到的工具输出缺失错误体应与上游原文一致：${featureResponseText}`)
    assert.equal(featureResponse.headers.get('content-type'), 'application/json; charset=utf-8', '工具输出缺失错误响应应保留上游 content-type')
    assert.equal(toolOutputMissingUpstreamHitCount, 1, `工具输出缺失特征应首个账号命中后停止，实际上游命中 ${toolOutputMissingUpstreamHitCount} 次`)
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 0, '请求级失败不应本地屏蔽账号')

    currentScenario = 'same_signature_confirmation'
    const signatureResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'same upstream request error' }],
        stream: false
      })
    })
    const signatureResponseText = await signatureResponse.text()

    assert.equal(signatureResponse.status, 400, `同签名请求级失败应把上游 400 返回客户端，实际 HTTP ${signatureResponse.status}: ${signatureResponseText}`)
    assert.equal(signatureResponseText, sameSignatureRejectedRequestBody, `客户端收到的同签名错误体应与上游原文一致：${signatureResponseText}`)
    assert.equal(signatureResponse.headers.get('content-type'), 'application/json; charset=utf-8', '同签名错误响应应保留上游 content-type')
    assert.equal(sameSignatureUpstreamHitCount, 2, `同一错误应只用两个账号确认后停止，实际上游命中 ${sameSignatureUpstreamHitCount} 次`)
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 0, '同签名请求级失败不应本地屏蔽账号')

    currentScenario = 'instructions_required_feature'
    const instructionsRequiredResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'instructions required should continue next' }],
        stream: false
      })
    })
    const instructionsRequiredResponseText = await instructionsRequiredResponse.text()

    assert.equal(instructionsRequiredResponse.status, 400, `Instructions are required 特征应把上游 400 原样返回客户端，实际 HTTP ${instructionsRequiredResponse.status}: ${instructionsRequiredResponseText}`)
    assert.equal(instructionsRequiredResponseText, instructionsRequiredRejectedRequestBody, `客户端收到的 Instructions are required 错误体应与上游原文一致：${instructionsRequiredResponseText}`)
    assert.equal(instructionsRequiredResponse.headers.get('content-type'), 'application/json; charset=utf-8', 'Instructions are required 错误响应应保留上游 content-type')
    assert.equal(instructionsRequiredUpstreamHitCount, 1, `Instructions are required 特征应首个账号命中后停止，实际上游命中 ${instructionsRequiredUpstreamHitCount} 次`)
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 0, 'Instructions are required 特征不应本地屏蔽账号')

    settingsRepository.updateSettings({
      temporaryUnschedulableRetryAttempts: 2,
      temporaryUnschedulableRetryIntervalSeconds: 0
    })
    gatewayCache.clearGatewayRuntimeCache()
    currentScenario = 'unknown_failure_same_account_retry'
    const transientResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'transient 502 should retry same account' }],
        stream: false
      })
    })
    const transientResponseText = await transientResponse.text()

    assert.equal(transientResponse.status, 200, `未知非 2xx 失败应先同账号重试并在恢复后成功，实际 HTTP ${transientResponse.status}: ${transientResponseText}`)
    assert.equal(transient502UpstreamHitCount, 3, `未知非 2xx 失败应在同账号上重试两次后成功，实际上游命中 ${transient502UpstreamHitCount} 次`)
    assert.equal(transientResponseText, transient502SuccessBody, `同账号重试成功响应体异常：${transientResponseText}`)
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 0, '未知非 2xx 失败同账号重试成功前不应本地屏蔽账号')

    usageRecordQueue.flushAllUsageRecordQueue()
    const accounts = repositories.listAccounts()
    for (const account of [firstAccount, secondAccount]) {
      const updated = accounts.find((item) => item.id === account.id)
      assert(updated, `账号 ${account.name} 不存在`)
      assert.equal(updated.status, 'active', `账号 ${account.name} 不应被冷却或停用`)
      assert.equal(updated.schedulable, true, `账号 ${account.name} 不应变为不可调度`)
      assert.equal(updated.cooldownUntil, undefined, `账号 ${account.name} 不应写入冷却时间`)
      assert.equal(updated.lastErrorMessage, undefined, `账号 ${account.name} 不应写入最近错误`)
    }

    console.log('请求级上游失败回归通过：无效 JSON 由网关拒绝；工具输出缺失和 Instructions are required 特征直接返回客户端；未知非 2xx 失败先同账号重试；两个账号返回一致错误时直接返回客户端；账号不冷却、不本地屏蔽、不继续扫池')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    auditLogQueue.flushAllAuditLogQueue()
    await closeServer(appServer)
    await closeServer(upstreamServer)
    try {
      databaseModule.getDatabase().close()
      databaseModule.getRecordDatabase().close()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

type RegressionScenario = 'tool_output_missing_feature' | 'same_signature_confirmation' | 'instructions_required_feature' | 'unknown_failure_same_account_retry'

let currentScenario: RegressionScenario = 'tool_output_missing_feature'
let toolOutputMissingUpstreamHitCount = 0
let sameSignatureUpstreamHitCount = 0
let instructionsRequiredUpstreamHitCount = 0
let transient502UpstreamHitCount = 0
const toolOutputMissingRejectedRequestMessage = 'No tool output found for function call fc_request_failure_regression.'
const toolOutputMissingRejectedRequestBody = JSON.stringify({
  error: {
    message: toolOutputMissingRejectedRequestMessage,
    type: 'invalid_request_error',
    code: null
  }
})
const sameSignatureRejectedRequestMessage = 'Regression request payload is invalid.'
const sameSignatureRejectedRequestBody = JSON.stringify({
  error: {
    message: sameSignatureRejectedRequestMessage,
    type: 'invalid_request_error',
    code: null
  }
})
const instructionsRequiredRejectedRequestBody = JSON.stringify({
  error: {
    message: 'Instructions are required',
    type: 'invalid_request_error',
    param: '',
    code: null
  }
})
const transient502SuccessBody = JSON.stringify({
  id: 'chatcmpl-transient-502-regression',
  object: 'chat.completion',
  choices: [
    {
      index: 0,
      message: { role: 'assistant', content: 'ok' },
      finish_reason: 'stop'
    }
  ],
  usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
})

function createRejectedRequestUpstream(): http.Server {
  return http.createServer((_req, res) => {
    if (currentScenario === 'unknown_failure_same_account_retry') {
      transient502UpstreamHitCount += 1
      if (transient502UpstreamHitCount <= 2) {
        res.writeHead(502, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { message: 'transient upstream error', type: 'server_error', code: 'bad_gateway' } }))
        return
      }
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(transient502SuccessBody)
      return
    }
    if (currentScenario === 'instructions_required_feature') {
      instructionsRequiredUpstreamHitCount += 1
      res.writeHead(400, { 'content-type': 'application/json; charset=utf-8' })
      res.end(instructionsRequiredRejectedRequestBody)
      return
    }

    const body = currentScenario === 'tool_output_missing_feature'
      ? toolOutputMissingRejectedRequestBody
      : sameSignatureRejectedRequestBody
    if (currentScenario === 'tool_output_missing_feature') {
      toolOutputMissingUpstreamHitCount += 1
    } else {
      sameSignatureUpstreamHitCount += 1
    }
    res.writeHead(400, { 'content-type': 'application/json; charset=utf-8' })
    res.end(body)
  })
}

function totalUpstreamHitCount(): number {
  return toolOutputMissingUpstreamHitCount
    + sameSignatureUpstreamHitCount
    + instructionsRequiredUpstreamHitCount
    + transient502UpstreamHitCount
}

function createRegressionApiKey(groupId: string): { id: string; key: string } {
  const key = 'sk-request-failure-regression'
  const id = databaseModule.newId('key')
  const now = databaseModule.nowIso()
  databaseModule.getDatabase()
    .prepare(`
      INSERT INTO api_keys (id, system_account_id, name, description, key_hash, key_prefix, key_secret_encrypted, status, group_id, group_authorization_id, expires_at, quota_limits_json, scopes_json, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      id,
      'sys_admin',
      '请求级失败回归 Key',
      null,
      cryptoModule.hashSecret(key),
      key.slice(0, 8),
      cryptoModule.encryptJson({ key }),
      'active',
      groupId,
      null,
      null,
      null,
      '[]',
      now,
      now
    )
  return { id, key }
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('服务地址不可用')
  }
  return { port: address.port }
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

await main()
