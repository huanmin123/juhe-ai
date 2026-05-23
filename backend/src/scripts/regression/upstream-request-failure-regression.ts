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
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
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
  clientIpAccountAvoidanceService,
  requestErrorSignatureCacheService,
  gatewayFailureDispatch,
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
  import('../../modules/gateway/openai-gateway-client-ip-account-avoidance.service.js'),
  import('../../modules/gateway/openai-gateway-request-error-signature-cache.service.js'),
  import('../../modules/gateway/openai-gateway-failure-dispatch.js'),
  import('../../modules/gateway/usage-record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const timeoutError = Object.assign(new Error(''), { code: 'ETIMEDOUT' })
assert.equal(
  gatewayFailureDispatch.formatUpstreamRequestErrorMessage(timeoutError),
  '请求失败：ETIMEDOUT',
  '空错误消息应回退到错误码，避免最后一次尝试文案只剩空白'
)

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
    const thirdAccount = repositories.createAccount({
      providerCode: 'openai',
      name: '03-请求级失败回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-request-failure-3',
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

    currentScenario = 'invalid_request_confirmation'
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

    assert.equal(featureResponse.status, 422, `invalid_request_error 应先用三个账号同签名确认后返回客户端，实际 HTTP ${featureResponse.status}: ${featureResponseText}`)
    assert.equal(featureResponseText, invalidRequestRejectedRequestBody, `客户端收到的 invalid_request_error 错误体应与上游原文一致：${featureResponseText}`)
    assert.equal(featureResponse.headers.get('content-type'), 'application/json; charset=utf-8', 'invalid_request_error 错误响应应保留上游 content-type')
    assert.equal(invalidRequestUpstreamHitCount, 3, `invalid_request_error 应尝试三个账号确认请求级失败，实际上游命中 ${invalidRequestUpstreamHitCount} 次`)
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 0, '同签名 invalid_request_error 不应本地屏蔽账号')

    currentScenario = 'same_signature_third_account_success'
    const thirdSuccessResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'third account can still recover request' }],
        stream: false
      })
    })
    const thirdSuccessResponseText = await thirdSuccessResponse.text()
    assert.equal(thirdSuccessResponse.status, 200, `前两个账号同签名但第三账号可用时应救回请求，实际 HTTP ${thirdSuccessResponse.status}: ${thirdSuccessResponseText}`)
    assert.equal(thirdSuccessResponseText, thirdAccountSuccessBody, `第三账号救回响应体异常：${thirdSuccessResponseText}`)
    assert.equal(thirdAccountSuccessHitCount, 3, `第三账号救回应尝试三个账号，实际 ${thirdAccountSuccessHitCount}`)

    currentScenario = 'same_signature_confirmation'
    const sameSignatureRequestBody = JSON.stringify({
      model: 'gpt-4o-mini',
      messages: [{ role: 'user', content: 'same upstream request error' }],
      stream: false
    })
    const signatureResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: sameSignatureRequestBody
    })
    const signatureResponseText = await signatureResponse.text()

    assert.equal(signatureResponse.status, 422, `同签名请求级失败应把上游 422 返回客户端，实际 HTTP ${signatureResponse.status}: ${signatureResponseText}`)
    assert.equal(signatureResponseText, sameSignatureRejectedRequestBody, `客户端收到的同签名错误体应与上游原文一致：${signatureResponseText}`)
    assert.equal(signatureResponse.headers.get('content-type'), 'application/json; charset=utf-8', '同签名错误响应应保留上游 content-type')
    assert.equal(sameSignatureUpstreamHitCount, 3, `同一错误应探测第三个账号确认后停止，实际上游命中 ${sameSignatureUpstreamHitCount} 次`)
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 0, '同签名请求级失败不应本地屏蔽账号')

    const cachedSignatureResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: sameSignatureRequestBody
    })
    const cachedSignatureResponseText = await cachedSignatureResponse.text()
    assert.equal(cachedSignatureResponse.status, 422, `重复同签名请求应命中短路缓存并保留上游 HTTP 状态，实际 HTTP ${cachedSignatureResponse.status}: ${cachedSignatureResponseText}`)
    assert.equal(cachedSignatureResponseText, sameSignatureRejectedRequestBody, `重复同签名请求缓存响应体应与上游原文一致：${cachedSignatureResponseText}`)
    assert.equal(sameSignatureUpstreamHitCount, 3, `重复同签名请求命中缓存后不应再打上游，实际上游命中 ${sameSignatureUpstreamHitCount} 次`)

    clientIpAccountAvoidanceService.clearClientIpAccountAvoidanceForTest()
    currentScenario = 'invalid_request_switch_account_success'
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

    assert.equal(instructionsRequiredResponse.status, 200, `首账号 invalid_request_error 但后续账号可用时应切号成功，实际 HTTP ${instructionsRequiredResponse.status}: ${instructionsRequiredResponseText}`)
    assert.equal(instructionsRequiredResponseText, invalidRequestSwitchSuccessBody, `invalid_request_error 切号成功响应体异常：${instructionsRequiredResponseText}`)
    assert.equal(invalidRequestSwitchUpstreamHitCount, 2, `invalid_request_error 切号成功应命中两个账号，实际 ${invalidRequestSwitchUpstreamHitCount}`)
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 0, 'invalid_request_error 切号成功不应本地屏蔽账号')

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

    settingsRepository.updateSettings({
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0
    })
    gatewayCache.clearGatewayRuntimeCache()
    clientIpAccountAvoidanceService.clearClientIpAccountAvoidanceForTest()
    requestErrorSignatureCacheService.clearRequestErrorSignatureCacheForTest()
    currentScenario = 'unknown_failure_switch_account_success'
    const switchResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'unknown first account failure should not cooldown' }],
        stream: false
      })
    })
    const switchResponseText = await switchResponse.text()

    assert.equal(switchResponse.status, 200, `未知失败切到后续账号成功时应返回成功响应，实际 HTTP ${switchResponse.status}: ${switchResponseText}`)
    assert.equal(unknownSwitchFirstAccountHitCount, 1, `未知失败切号场景首账号应命中 1 次，实际 ${unknownSwitchFirstAccountHitCount}`)
    assert.equal(unknownSwitchSecondAccountHitCount, 1, `未知失败切号场景后续账号应命中 1 次，实际 ${unknownSwitchSecondAccountHitCount}`)
    assert.equal(switchResponseText, unknownSwitchSuccessBody, `未知失败切号成功响应体异常：${switchResponseText}`)
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 0, '未知失败切到后续账号成功后不应本地屏蔽首账号')

    usageRecordQueue.flushAllUsageRecordQueue()
    const accounts = repositories.listAccounts()
    for (const account of [firstAccount, secondAccount, thirdAccount]) {
      const updated = accounts.find((item) => item.id === account.id)
      assert(updated, `账号 ${account.name} 不存在`)
      assert.equal(updated.status, 'active', `账号 ${account.name} 不应被冷却或停用`)
      assert.equal(updated.schedulable, true, `账号 ${account.name} 不应变为不可调度`)
      assert.equal(updated.cooldownUntil, undefined, `账号 ${account.name} 不应写入冷却时间`)
      assert.equal(updated.lastErrorMessage, undefined, `账号 ${account.name} 不应写入最近错误`)
    }

    console.log('请求级上游失败回归通过：无效 JSON 由网关拒绝；invalid_request_error 先切号确认，后续账号成功则救回，同签名才返回客户端；未知非 2xx 失败先同账号重试或切号；账号不冷却、不本地屏蔽')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    auditLogQueue.flushAllAuditLogQueue()
    await closeServer(appServer)
    await closeServer(upstreamServer)
    try {
      databaseModule.getDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

type RegressionScenario =
  | 'invalid_request_confirmation'
  | 'same_signature_third_account_success'
  | 'same_signature_confirmation'
  | 'invalid_request_switch_account_success'
  | 'unknown_failure_same_account_retry'
  | 'unknown_failure_switch_account_success'

let currentScenario: RegressionScenario = 'invalid_request_confirmation'
let invalidRequestUpstreamHitCount = 0
let thirdAccountSuccessHitCount = 0
let sameSignatureUpstreamHitCount = 0
let invalidRequestSwitchUpstreamHitCount = 0
let transient502UpstreamHitCount = 0
let unknownSwitchFirstAccountHitCount = 0
let unknownSwitchSecondAccountHitCount = 0
const invalidRequestRejectedRequestMessage = 'Invalid value for model level: expected one of low, medium, high.'
const invalidRequestRejectedRequestBody = JSON.stringify({
  error: {
    message: invalidRequestRejectedRequestMessage,
    type: 'invalid_request_error',
    code: null
  }
})
const sameSignatureRejectedRequestMessage = 'Regression request payload is invalid.'
const sameSignatureRejectedRequestBody = JSON.stringify({
  error: {
    message: sameSignatureRejectedRequestMessage,
    type: 'unprocessable_entity',
    code: 'invalid_payload'
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
const invalidRequestSwitchSuccessBody = JSON.stringify({
  id: 'chatcmpl-invalid-request-switch-regression',
  object: 'chat.completion',
  choices: [
    {
      index: 0,
      message: { role: 'assistant', content: 'ok after account switch' },
      finish_reason: 'stop'
    }
  ],
  usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
})
const thirdAccountSuccessBody = JSON.stringify({
  id: 'chatcmpl-third-account-success-regression',
  object: 'chat.completion',
  choices: [
    {
      index: 0,
      message: { role: 'assistant', content: 'ok from third account' },
      finish_reason: 'stop'
    }
  ],
  usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
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
const unknownSwitchSuccessBody = JSON.stringify({
  id: 'chatcmpl-unknown-switch-regression',
  object: 'chat.completion',
  choices: [
    {
      index: 0,
      message: { role: 'assistant', content: 'ok from second account' },
      finish_reason: 'stop'
    }
  ],
  usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
})

function createRejectedRequestUpstream(): http.Server {
  return http.createServer((req, res) => {
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
    if (currentScenario === 'unknown_failure_switch_account_success') {
      const authorization = String(req.headers.authorization ?? '')
      if (authorization.includes('sk-request-failure-1')) {
        unknownSwitchFirstAccountHitCount += 1
        res.writeHead(502, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { message: 'temporary first account upstream error', type: 'server_error', code: 'bad_gateway' } }))
        return
      }
      unknownSwitchSecondAccountHitCount += 1
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(unknownSwitchSuccessBody)
      return
    }
    if (currentScenario === 'invalid_request_switch_account_success') {
      invalidRequestSwitchUpstreamHitCount += 1
      const authorization = String(req.headers.authorization ?? '')
      if (authorization.includes('sk-request-failure-1')) {
        res.writeHead(400, { 'content-type': 'application/json; charset=utf-8' })
        res.end(instructionsRequiredRejectedRequestBody)
        return
      }
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(invalidRequestSwitchSuccessBody)
      return
    }
    if (currentScenario === 'same_signature_third_account_success') {
      thirdAccountSuccessHitCount += 1
      const authorization = String(req.headers.authorization ?? '')
      if (!authorization.includes('sk-request-failure-3')) {
        res.writeHead(422, { 'content-type': 'application/json; charset=utf-8' })
        res.end(sameSignatureRejectedRequestBody)
        return
      }
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(thirdAccountSuccessBody)
      return
    }

    const body = currentScenario === 'invalid_request_confirmation'
      ? invalidRequestRejectedRequestBody
      : sameSignatureRejectedRequestBody
    if (currentScenario === 'invalid_request_confirmation') {
      invalidRequestUpstreamHitCount += 1
    } else {
      sameSignatureUpstreamHitCount += 1
    }
    res.writeHead(422, { 'content-type': 'application/json; charset=utf-8' })
    res.end(body)
  })
}

function totalUpstreamHitCount(): number {
  return invalidRequestUpstreamHitCount
    + thirdAccountSuccessHitCount
    + sameSignatureUpstreamHitCount
    + invalidRequestSwitchUpstreamHitCount
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
