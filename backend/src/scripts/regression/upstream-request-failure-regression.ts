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

    const group = repositories.createGroup({ name: '上游失败回归分组', providerCode: 'openai', enabled: true })
    const firstAccount = repositories.createAccount({
      providerCode: 'openai',
      name: '01-上游失败回归账户',
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
      name: '02-上游失败回归账户',
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
      name: '03-上游失败回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-request-failure-3',
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true
    })
    const apiKey = createRegressionApiKey(group.id, 'sk-request-failure-regression')
    dispatchRaceSecondAccountId = secondAccount.id
    const waitGroup = repositories.createGroup({ name: '本地屏蔽等待回归分组', providerCode: 'openai', enabled: true })
    const waitAccount = repositories.createAccount({
      providerCode: 'openai',
      name: '单账号等待回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-request-failure-wait',
        base_url: upstreamBaseUrl
      },
      groupId: waitGroup.id,
      status: 'active',
      schedulable: true
    })
    const waitApiKey = createRegressionApiKey(waitGroup.id, 'sk-request-failure-wait-key')

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

    assert.equal(featureResponse.status, 503, `所有账号上游失败后应返回统一网关错误，实际 HTTP ${featureResponse.status}: ${featureResponseText}`)
    assert.match(featureResponseText, /没有可用的上游账户/, `所有账号失败不应透传上游原文，应返回网关统一错误：${featureResponseText}`)
    assert.notEqual(featureResponseText, invalidRequestRejectedRequestBody, '所有账号失败不应把上游原始错误体透传给客户端')
    assert.equal(invalidRequestUpstreamHitCount, 3, `通用失败流水线应尝试三个账号后再失败，实际上游命中 ${invalidRequestUpstreamHitCount} 次`)
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 3, '三个账号都失败后应全部进入本地短期屏蔽')
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()

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
    assert.equal(thirdSuccessResponse.status, 200, `前两个账号返回相同上游错误但第三账号可用时应救回请求，实际 HTTP ${thirdSuccessResponse.status}: ${thirdSuccessResponseText}`)
    assert.equal(thirdSuccessResponseText, thirdAccountSuccessBody, `第三账号救回响应体异常：${thirdSuccessResponseText}`)
    assert.equal(thirdAccountSuccessHitCount, 3, `第三账号救回应尝试三个账号，实际 ${thirdAccountSuccessHitCount}`)
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 2, '前两个失败账号应进入本地短期屏蔽，成功账号不应被屏蔽')
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    clientIpAccountAvoidanceService.clearClientIpAccountAvoidanceForTest()

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

    assert.equal(signatureResponse.status, 503, `多个账号返回相同上游错误也应走统一网关错误，实际 HTTP ${signatureResponse.status}: ${signatureResponseText}`)
    assert.match(signatureResponseText, /没有可用的上游账户/, `相同上游错误失败不应返回上游原文：${signatureResponseText}`)
    assert.notEqual(signatureResponseText, sameSignatureRejectedRequestBody, '相同上游错误失败不应保留上游原始错误体')
    assert.equal(sameSignatureUpstreamHitCount, 3, `同一错误应尝试全部三个账号，实际上游命中 ${sameSignatureUpstreamHitCount} 次`)
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 3, '相同上游错误失败后也应本地屏蔽失败账号')
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()

    const repeatedSignatureResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: sameSignatureRequestBody
    })
    const repeatedSignatureResponseText = await repeatedSignatureResponse.text()
    assert.equal(repeatedSignatureResponse.status, 503, `重复相同上游错误请求不应命中旧短路缓存，实际 HTTP ${repeatedSignatureResponse.status}: ${repeatedSignatureResponseText}`)
    assert.equal(sameSignatureUpstreamHitCount, 6, `重复相同上游错误请求清理本地屏蔽后应重新探测上游，实际上游命中 ${sameSignatureUpstreamHitCount} 次`)
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()

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
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 1, '首账号失败后即使后续账号成功，也应短期屏蔽首账号')
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    clientIpAccountAvoidanceService.clearClientIpAccountAvoidanceForTest()

    settingsRepository.updateSettings({
      temporaryUnschedulableRetryAttempts: 2,
      temporaryUnschedulableRetryIntervalSeconds: 0
    })
    gatewayCache.clearGatewayRuntimeCache()
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
    assert.equal(unknownSwitchFirstAccountHitCount, 1, `即使配置了同账号重试次数，未知失败首账号也只应命中 1 次，实际 ${unknownSwitchFirstAccountHitCount}`)
    assert.equal(unknownSwitchSecondAccountHitCount, 1, `未知失败切号场景后续账号应命中 1 次，实际 ${unknownSwitchSecondAccountHitCount}`)
    assert.equal(switchResponseText, unknownSwitchSuccessBody, `未知失败切号成功响应体异常：${switchResponseText}`)
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 1, '未知失败切到后续账号成功后应本地屏蔽首账号')
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    clientIpAccountAvoidanceService.clearClientIpAccountAvoidanceForTest()

    currentScenario = 'dispatch_loop_local_suppression_race'
    const dispatchRaceResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'second account becomes locally suppressed during dispatch' }],
        stream: false
      })
    })
    const dispatchRaceResponseText = await dispatchRaceResponse.text()
    assert.equal(dispatchRaceResponse.status, 200, `调度中途账号被本地屏蔽后应跳过并继续后续账号，实际 HTTP ${dispatchRaceResponse.status}: ${dispatchRaceResponseText}`)
    assert.equal(dispatchRaceResponseText, dispatchRaceSuccessBody, `调度中途屏蔽后第三账号响应体异常：${dispatchRaceResponseText}`)
    assert.equal(dispatchRaceFirstAccountHitCount, 1, `调度竞态场景应先命中首账号一次，实际 ${dispatchRaceFirstAccountHitCount}`)
    assert.equal(dispatchRaceSecondAccountHitCount, 0, `第二账号在首账号失败后被本地屏蔽，不应继续命中，实际 ${dispatchRaceSecondAccountHitCount}`)
    assert.equal(dispatchRaceThirdAccountHitCount, 1, `第二账号被屏蔽后应切到第三账号成功，实际 ${dispatchRaceThirdAccountHitCount}`)
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 2, '调度竞态后首账号和中途屏蔽账号都应处于本地短期屏蔽')
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    clientIpAccountAvoidanceService.clearClientIpAccountAvoidanceForTest()

    currentScenario = 'single_account_wait_recover_success'
    accountSideEffects.suppressGatewayAccountLocallyForTest(waitAccount.id, 40, '单账号等待回归')
    const waitResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${waitApiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'single account should wait until local suppression releases' }],
        stream: false
      })
    })
    const waitResponseText = await waitResponse.text()
    assert.equal(waitResponse.status, 200, `单账号处于本地屏蔽时应先等待释放再请求，实际 HTTP ${waitResponse.status}: ${waitResponseText}`)
    assert.equal(waitResponseText, singleAccountWaitSuccessBody, `单账号等待释放后的响应体异常：${waitResponseText}`)
    assert.equal(singleAccountWaitHitCount, 1, `单账号等待释放后应只命中一次上游，实际 ${singleAccountWaitHitCount}`)
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 0, '单账号屏蔽释放后本地屏蔽计数应恢复为 0')

    currentScenario = 'single_account_wait_extended_recover_success'
    accountSideEffects.suppressGatewayAccountLocallyForTest(waitAccount.id, 60, '单账号等待续期回归')
    const extendedWaitResponsePromise = fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${waitApiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'single account wait should continue after suppression extension' }],
        stream: false
      })
    })
    await delay(20)
    accountSideEffects.suppressGatewayAccountLocallyForTest(waitAccount.id, 120, '单账号等待续期回归-续期')
    const extendedWaitResponse = await extendedWaitResponsePromise
    const extendedWaitResponseText = await extendedWaitResponse.text()
    assert.equal(extendedWaitResponse.status, 200, `单账号本地屏蔽等待被续期时应继续等到释放，实际 HTTP ${extendedWaitResponse.status}: ${extendedWaitResponseText}`)
    assert.equal(extendedWaitResponseText, singleAccountExtendedWaitSuccessBody, `单账号等待续期释放后的响应体异常：${extendedWaitResponseText}`)
    assert.equal(singleAccountExtendedWaitHitCount, 1, `单账号续期等待释放后应只命中一次上游，实际 ${singleAccountExtendedWaitHitCount}`)
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 0, '单账号续期屏蔽释放后本地屏蔽计数应恢复为 0')

    usageRecordQueue.flushAllUsageRecordQueue()
    const accounts = repositories.listAccounts()
    for (const account of [firstAccount, secondAccount, thirdAccount, waitAccount]) {
      const updated = accounts.find((item) => item.id === account.id)
      assert(updated, `账号 ${account.name} 不存在`)
      assert.equal(updated.status, 'active', `账号 ${account.name} 不应被冷却或停用`)
      assert.equal(updated.schedulable, true, `账号 ${account.name} 不应变为不可调度`)
      assert.equal(updated.cooldownUntil, undefined, `账号 ${account.name} 不应写入冷却时间`)
      assert.equal(updated.lastErrorMessage, undefined, `账号 ${account.name} 不应写入最近错误`)
    }

    console.log('上游失败回归通过：无效 JSON 由网关拒绝；任意上游失败先本地屏蔽并切号；全部失败返回统一网关错误；重复请求不再命中旧请求级短路缓存；单账号屏蔽时会等待释放并支持续期等待')
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
  | 'unknown_failure_switch_account_success'
  | 'dispatch_loop_local_suppression_race'
  | 'single_account_wait_recover_success'
  | 'single_account_wait_extended_recover_success'

let currentScenario: RegressionScenario = 'invalid_request_confirmation'
let dispatchRaceSecondAccountId = ''
let invalidRequestUpstreamHitCount = 0
let thirdAccountSuccessHitCount = 0
let sameSignatureUpstreamHitCount = 0
let invalidRequestSwitchUpstreamHitCount = 0
let unknownSwitchFirstAccountHitCount = 0
let unknownSwitchSecondAccountHitCount = 0
let dispatchRaceFirstAccountHitCount = 0
let dispatchRaceSecondAccountHitCount = 0
let dispatchRaceThirdAccountHitCount = 0
let singleAccountWaitHitCount = 0
let singleAccountExtendedWaitHitCount = 0
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
const singleAccountWaitSuccessBody = JSON.stringify({
  id: 'chatcmpl-single-account-wait-regression',
  object: 'chat.completion',
  choices: [
    {
      index: 0,
      message: { role: 'assistant', content: 'ok after local suppression wait' },
      finish_reason: 'stop'
    }
  ],
  usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
})
const singleAccountExtendedWaitSuccessBody = JSON.stringify({
  id: 'chatcmpl-single-account-extended-wait-regression',
  object: 'chat.completion',
  choices: [
    {
      index: 0,
      message: { role: 'assistant', content: 'ok after extended local suppression wait' },
      finish_reason: 'stop'
    }
  ],
  usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
})
const dispatchRaceSuccessBody = JSON.stringify({
  id: 'chatcmpl-dispatch-race-regression',
  object: 'chat.completion',
  choices: [
    {
      index: 0,
      message: { role: 'assistant', content: 'ok after dispatch suppression skip' },
      finish_reason: 'stop'
    }
  ],
  usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
})

function createRejectedRequestUpstream(): http.Server {
  return http.createServer((req, res) => {
    if (currentScenario === 'single_account_wait_recover_success') {
      singleAccountWaitHitCount += 1
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(singleAccountWaitSuccessBody)
      return
    }
    if (currentScenario === 'single_account_wait_extended_recover_success') {
      singleAccountExtendedWaitHitCount += 1
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(singleAccountExtendedWaitSuccessBody)
      return
    }
    if (currentScenario === 'dispatch_loop_local_suppression_race') {
      const authorization = String(req.headers.authorization ?? '')
      if (authorization.includes('sk-request-failure-1')) {
        dispatchRaceFirstAccountHitCount += 1
        if (dispatchRaceSecondAccountId) {
          accountSideEffects.suppressGatewayAccountLocallyForTest(dispatchRaceSecondAccountId, 30_000, '调度中途屏蔽回归')
        }
        res.writeHead(502, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { message: 'first account failed before dispatch race', type: 'server_error', code: 'bad_gateway' } }))
        return
      }
      if (authorization.includes('sk-request-failure-2')) {
        dispatchRaceSecondAccountHitCount += 1
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ id: 'should-not-hit-second-account' }))
        return
      }
      dispatchRaceThirdAccountHitCount += 1
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(dispatchRaceSuccessBody)
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
    + unknownSwitchFirstAccountHitCount
    + unknownSwitchSecondAccountHitCount
    + dispatchRaceFirstAccountHitCount
    + dispatchRaceSecondAccountHitCount
    + dispatchRaceThirdAccountHitCount
    + singleAccountWaitHitCount
    + singleAccountExtendedWaitHitCount
}

function createRegressionApiKey(groupId: string, key: string): { id: string; key: string } {
  const id = databaseModule.newId('key')
  const now = databaseModule.nowIso()
  databaseModule.getDatabase()
    .prepare(`
      INSERT INTO api_keys (id, system_account_id, name, description, key_hash, key_prefix, key_secret_encrypted, status, group_id, expires_at, quota_limits_json, scopes_json, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      id,
      'sys_admin',
      `上游失败回归 Key ${key.slice(-8)}`,
      null,
      cryptoModule.hashSecret(key),
      key.slice(0, 8),
      cryptoModule.encryptJson({ key }),
      'active',
      groupId,
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

function delay(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}

await main()
