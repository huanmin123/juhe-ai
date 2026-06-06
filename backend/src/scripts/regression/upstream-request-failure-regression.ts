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
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
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
const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
type RegressionAccount = { id: string; name: string }

async function main(): Promise<void> {
  let appServer: http.Server | undefined
  let upstreamServer: http.Server | undefined
  let closedTransportServer: http.Server | undefined
  try {
    settingsRepository.updateSettings({ temporaryUnschedulableRetryAttempts: 0 })
    gatewayCache.clearGatewayRuntimeCache()

    upstreamServer = createRejectedRequestUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

    const group = repositories.createGroup({ name: '上游失败回归分组', providerCode: 'openai', enabled: true }, access)
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
    }, access)
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
    }, access)
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
    }, access)
    const apiKey = createRegressionApiKey(group.id, 'sk-request-failure-regression')
    dispatchRaceSecondAccountId = secondAccount.id
    const fastFailGroup = repositories.createGroup({ name: '本地屏蔽快速失败回归分组', providerCode: 'openai', enabled: true }, access)
    const fastFailAccount = repositories.createAccount({
      providerCode: 'openai',
      name: '单账号快速失败回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-request-failure-fast-fail',
        base_url: upstreamBaseUrl
      },
      groupId: fastFailGroup.id,
      status: 'active',
      schedulable: true
    }, access)
    const fastFailApiKey = createRegressionApiKey(fastFailGroup.id, 'sk-request-failure-fast-fail-key')
    const singleFailureGroup = repositories.createGroup({ name: '单账号上游失败写状态回归分组', providerCode: 'openai', enabled: true }, access)
    const singleFailureAccount = repositories.createAccount({
      providerCode: 'openai',
      name: '单账号上游失败写状态回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-single-upstream-failure-cooldown',
        base_url: upstreamBaseUrl
      },
      groupId: singleFailureGroup.id,
      status: 'active',
      schedulable: true
    }, access)
    const singleFailureApiKey = createRegressionApiKey(singleFailureGroup.id, 'sk-single-upstream-failure-key')
    closedTransportServer = http.createServer()
    await listen(closedTransportServer)
    const closedTransportBaseUrl = `http://127.0.0.1:${serverAddress(closedTransportServer).port}/v1`
    await closeServer(closedTransportServer)
    closedTransportServer = undefined
    const directTransportFailureGroup = repositories.createGroup({ name: '直连传输失败回归分组', providerCode: 'openai', enabled: true }, access)
    const directTransportFailureAccounts: RegressionAccount[] = []
    for (let index = 0; index < 2; index += 1) {
      const account = repositories.createAccount({
        providerCode: 'openai',
        name: `直连传输失败回归账户-${index + 1}`,
        type: 'api_key',
        credentials: {
          api_key: `sk-direct-transport-failure-${index + 1}`,
          base_url: closedTransportBaseUrl
        },
        groupId: directTransportFailureGroup.id,
        status: 'active',
        schedulable: true
      }, access)
      directTransportFailureAccounts.push(account)
    }
    const directTransportFailureApiKey = createRegressionApiKey(directTransportFailureGroup.id, 'sk-direct-transport-failure-key')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const directTransportFailureResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${directTransportFailureApiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'direct transport failures should write account state' }],
        stream: false
      })
    })
    const directTransportFailureText = await directTransportFailureResponse.text()
    assert.equal(directTransportFailureResponse.status, 503, `直连上游传输失败仍应返回统一网关错误，实际 HTTP ${directTransportFailureResponse.status}: ${directTransportFailureText}`)
    assert.match(directTransportFailureText, /没有可用的上游账户/, `直连上游传输失败应返回网关统一错误：${directTransportFailureText}`)
    assertAccountsRuntimeSuppressedActive(directTransportFailureAccounts, /上游账号请求异常/, '直连上游传输失败应进入运行态屏障')
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()

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
    assertAccountsActive([firstAccount, secondAccount, thirdAccount], '无效 JSON 未命中上游账号，不应写账号状态')

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
    assertAccountsRuntimeSuppressedActive([firstAccount, secondAccount, thirdAccount], /上游账号返回非成功状态：HTTP 422|Invalid value/, '未配置账号错误策略的上游失败应进入运行态屏障')
    restoreRegressionAccounts([firstAccount, secondAccount, thirdAccount])

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
    assertAccountsRuntimeSuppressedActive([firstAccount, secondAccount], /上游账号返回非成功状态：HTTP 422|Regression request payload is invalid/, '后续账号成功也不能掩盖前序账号运行态屏障')
    assertAccountsActive([thirdAccount], '成功救回请求的第三账号应保持正常')
    restoreRegressionAccounts([firstAccount, secondAccount])
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
    assertAccountsRuntimeSuppressedActive([firstAccount, secondAccount, thirdAccount], /上游账号返回非成功状态：HTTP 422|Regression request payload is invalid/, '相同上游错误也应逐个进入运行态屏障')
    restoreRegressionAccounts([firstAccount, secondAccount, thirdAccount])

    const repeatedSignatureResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: sameSignatureRequestBody
    })
    const repeatedSignatureResponseText = await repeatedSignatureResponse.text()
    assert.equal(repeatedSignatureResponse.status, 503, `重复相同上游错误请求必须重新探测上游，实际 HTTP ${repeatedSignatureResponse.status}: ${repeatedSignatureResponseText}`)
    assert.equal(sameSignatureUpstreamHitCount, 6, `重复相同上游错误请求清理本地屏蔽后应重新探测上游，实际上游命中 ${sameSignatureUpstreamHitCount} 次`)
    assertAccountsRuntimeSuppressedActive([firstAccount, secondAccount, thirdAccount], /上游账号返回非成功状态：HTTP 422|Regression request payload is invalid/, '重复上游错误仍应进入运行态屏障')
    restoreRegressionAccounts([firstAccount, secondAccount, thirdAccount])

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
    assertAccountsRuntimeSuppressedActive([firstAccount], /上游账号返回非成功状态：HTTP 400|Instructions are required/, '首账号 invalid_request_error 也应进入运行态屏障')
    assertAccountsActive([secondAccount], '切号成功账号应保持正常')
    restoreRegressionAccounts([firstAccount])
    clientIpAccountAvoidanceService.clearClientIpAccountAvoidanceForTest()

    settingsRepository.updateSettings({
      temporaryUnschedulableRetryAttempts: 2,
      temporaryUnschedulableRetryIntervalSeconds: 0
    })
    gatewayCache.clearGatewayRuntimeCache()
    currentScenario = 'same_account_retry_success'
    const sameAccountRetryResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'same account retry should recover before account switch' }],
        stream: false
      })
    })
    const sameAccountRetryResponseText = await sameAccountRetryResponse.text()

    assert.equal(sameAccountRetryResponse.status, 200, `同账号原地重试成功时应直接返回成功，实际 HTTP ${sameAccountRetryResponse.status}: ${sameAccountRetryResponseText}`)
    assert.equal(sameAccountRetryResponseText, sameAccountRetrySuccessBody, `同账号原地重试成功响应体异常：${sameAccountRetryResponseText}`)
    assert.equal(sameAccountRetryFirstAccountHitCount, 2, `同账号原地重试成功场景首账号应命中 2 次，实际 ${sameAccountRetryFirstAccountHitCount}`)
    assert.equal(sameAccountRetrySecondAccountHitCount, 0, `同账号原地重试成功后不应切到第二账号，实际 ${sameAccountRetrySecondAccountHitCount}`)
    assertAccountsActive([firstAccount, secondAccount], '同账号原地重试救回后不应写账号状态或切号')
    clientIpAccountAvoidanceService.clearClientIpAccountAvoidanceForTest()

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
    assert.equal(unknownSwitchFirstAccountHitCount, 3, `未知失败应先按临时状态重试次数原地重试首账号，实际首账号命中 ${unknownSwitchFirstAccountHitCount} 次`)
    assert.equal(unknownSwitchSecondAccountHitCount, 1, `未知失败切号场景后续账号应命中 1 次，实际 ${unknownSwitchSecondAccountHitCount}`)
    assert.equal(switchResponseText, unknownSwitchSuccessBody, `未知失败切号成功响应体异常：${switchResponseText}`)
    assertAccountsRuntimeSuppressedActive([firstAccount], /上游账号返回非成功状态：HTTP 502|temporary first account upstream error/, '未知失败切到后续账号成功后应保留首账号运行态屏障')
    assertAccountsActive([secondAccount], '未知失败切号成功账号应保持正常')
    restoreRegressionAccounts([firstAccount])
    clientIpAccountAvoidanceService.clearClientIpAccountAvoidanceForTest()

    settingsRepository.updateSettings({
      streamRequestTimeoutSeconds: 10,
      temporaryUnschedulableRetryAttempts: 0
    })
    gatewayCache.clearGatewayRuntimeCache()
    currentScenario = 'non_stream_first_byte_timeout_switch_account_success'
    const nonStreamFirstByteTimeoutStartedAt = Date.now()
    const nonStreamFirstByteTimeoutResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'non stream first byte timeout should switch account' }],
        stream: false
      })
    })
    const nonStreamFirstByteTimeoutText = await nonStreamFirstByteTimeoutResponse.text()
    assert.equal(nonStreamFirstByteTimeoutResponse.status, 200, `非流式 2xx 首字节超时后应切号救回，实际 HTTP ${nonStreamFirstByteTimeoutResponse.status}: ${nonStreamFirstByteTimeoutText}`)
    assert.equal(nonStreamFirstByteTimeoutText, nonStreamFirstByteTimeoutSuccessBody, `非流式首字节超时切号成功响应体异常：${nonStreamFirstByteTimeoutText}`)
    assert.equal(nonStreamFirstByteTimeoutFirstAccountHitCount, 1, `非流式首账号首字节超时应命中 1 次，实际 ${nonStreamFirstByteTimeoutFirstAccountHitCount}`)
    assert.equal(nonStreamFirstByteTimeoutSecondAccountHitCount, 1, `非流式首字节超时后应切到第二账号，实际 ${nonStreamFirstByteTimeoutSecondAccountHitCount}`)
    assert(Date.now() - nonStreamFirstByteTimeoutStartedAt >= 9000, '非流式首字节超时应受首包等待上限控制，不应立即切号')
    assertAccountsRuntimeSuppressedActive([firstAccount], /上游账号请求异常|仍未返回首个响应/, '非流式首字节超时应进入首账号运行态屏障')
    assertAccountsActive([secondAccount], '非流式首字节超时切号成功账号应保持正常')
    restoreRegressionAccounts([firstAccount])
    clientIpAccountAvoidanceService.clearClientIpAccountAvoidanceForTest()

    currentScenario = 'non_stream_body_interrupted_after_output_client_retry'
    let interruptedRequestFailed = false
    try {
      const interruptedResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
        method: 'POST',
        headers: {
          authorization: `Bearer ${apiKey.key}`,
          'content-type': 'application/json'
        },
        body: JSON.stringify({
          model: 'gpt-4o-mini',
          messages: [{ role: 'user', content: 'non stream body interruption should fail current connection' }],
          stream: false
        })
      })
      await interruptedResponse.text()
    } catch {
      interruptedRequestFailed = true
    }
    assert.equal(interruptedRequestFailed, true, '非流式首字节已输出后正文中断应表现为客户端读取失败，不能优雅结束半截响应')
    assert.equal(nonStreamBodyInterruptedFirstAccountHitCount, 1, `非流式正文中断首请求应命中首账号 1 次，实际 ${nonStreamBodyInterruptedFirstAccountHitCount}`)
    assert.equal(nonStreamBodyInterruptedSecondAccountHitCount, 0, `非流式正文已输出后不应在同一 HTTP 响应里透明切到第二账号，实际 ${nonStreamBodyInterruptedSecondAccountHitCount}`)

    const interruptedRetryResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'client retry after non stream body interruption should switch account' }],
        stream: false
      })
    })
    const interruptedRetryText = await interruptedRetryResponse.text()
    assert.equal(interruptedRetryResponse.status, 200, `客户端重试后应避开刚中断账号并成功，实际 HTTP ${interruptedRetryResponse.status}: ${interruptedRetryText}`)
    assert.equal(interruptedRetryText, nonStreamBodyInterruptedRetrySuccessBody, `非流式正文中断客户端重试响应体异常：${interruptedRetryText}`)
    assert.equal(nonStreamBodyInterruptedFirstAccountHitCount, 1, `客户端重试应避开已本地避让的首账号，实际首账号命中 ${nonStreamBodyInterruptedFirstAccountHitCount}`)
    assert.equal(nonStreamBodyInterruptedSecondAccountHitCount, 1, `客户端重试应命中第二账号，实际 ${nonStreamBodyInterruptedSecondAccountHitCount}`)
    assertAccountsRuntimeSuppressedActive([firstAccount], /上游非流式响应正文中断|non stream body interrupted/, '非流式正文已输出后中断应进入首账号运行态屏障')
    assertAccountsActive([secondAccount], '非流式正文中断后客户端重试成功账号应保持正常')
    restoreRegressionAccounts([firstAccount])
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
    assert(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount >= 1, '调度竞态后显式中途屏蔽账号应处于本地短期屏蔽')
    assertAccountsRuntimeSuppressedActive([firstAccount], /上游账号返回非成功状态：HTTP 502|first account failed before dispatch race/, '调度竞态中真正上游失败的首账号应进入运行态屏障')
    assertAccountsActive([secondAccount, thirdAccount], '调度竞态中未命中或成功账号应保持正常')
    restoreRegressionAccounts([firstAccount])
    clientIpAccountAvoidanceService.clearClientIpAccountAvoidanceForTest()

    currentScenario = 'single_account_local_suppression_fast_fail'
    accountSideEffects.suppressGatewayAccountLocallyForTest(fastFailAccount.id, 1_000, '单账号快速失败回归')
    const fastFailStartedAtMs = Date.now()
    const waitResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${fastFailApiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'single account local suppression should fail fast' }],
        stream: false
      })
    })
    const waitResponseText = await waitResponse.text()
    const fastFailElapsedMs = Date.now() - fastFailStartedAtMs
    assert.equal(waitResponse.status, 503, `单账号处于本地屏蔽时应立即返回 503，实际 HTTP ${waitResponse.status}: ${waitResponseText}`)
    assert.match(waitResponseText, /所有上游账户正在临时隔离/, `单账号本地屏蔽快速失败应说明临时隔离：${waitResponseText}`)
    assert.equal(waitResponse.headers.get('retry-after'), '1', '单账号本地屏蔽快速失败应返回最短 Retry-After')
    assert(fastFailElapsedMs < 800, `单账号本地屏蔽不应等待释放，实际耗时 ${fastFailElapsedMs}ms`)
    assert.equal(singleAccountLocalSuppressionFastFailHitCount, 0, `单账号本地屏蔽快速失败不应命中上游，实际 ${singleAccountLocalSuppressionFastFailHitCount}`)
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()

    currentScenario = 'single_account_failure_default_cooldown'
    gatewayCache.clearGatewayRuntimeCache()
    const singleFailureResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${singleFailureApiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'single upstream failure should cooldown account' }],
        stream: false
      })
    })
    const singleFailureResponseText = await singleFailureResponse.text()
    assert.equal(singleFailureResponse.status, 503, `单账号上游失败无后备账号时仍应返回统一网关错误，实际 HTTP ${singleFailureResponse.status}: ${singleFailureResponseText}`)
    assert.match(singleFailureResponseText, /没有可用的上游账户/, `单账号上游失败网关响应应保持统一错误：${singleFailureResponseText}`)
    assert.equal(singleFailureHitCount, 1, `单账号上游失败默认冷却场景应命中上游一次，实际 ${singleFailureHitCount}`)
    assertAccountsRuntimeSuppressedActive([singleFailureAccount], /上游账号返回非成功状态：HTTP 418|generic upstream failure/, '单账号普通上游失败应进入运行态屏障而不是立即写库')

    usageRecordQueue.flushAllUsageRecordQueue()
    assertAccountsActive([firstAccount, secondAccount, thirdAccount, fastFailAccount], '已恢复的主测试账号最终应保持正常')

    console.log('上游失败回归通过：无效 JSON 由网关拒绝且不命中账号；普通上游失败会按临时状态配置原地重试，用尽后切号并进入运行态屏障；后续账号成功不掩盖前序账号屏障；全部失败返回统一网关错误；单账号本地屏蔽耗尽时快速失败且不命中上游')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    auditLogQueue.flushAllAuditLogQueue()
    await closeServer(appServer)
    await closeServer(upstreamServer)
    await closeServer(closedTransportServer)
    try {
      databaseModule.getBusinessDatabase().close()
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
  | 'same_account_retry_success'
  | 'unknown_failure_switch_account_success'
  | 'non_stream_first_byte_timeout_switch_account_success'
  | 'non_stream_body_interrupted_after_output_client_retry'
  | 'dispatch_loop_local_suppression_race'
  | 'single_account_local_suppression_fast_fail'
  | 'single_account_failure_default_cooldown'

let currentScenario: RegressionScenario = 'invalid_request_confirmation'
let dispatchRaceSecondAccountId = ''
let invalidRequestUpstreamHitCount = 0
let thirdAccountSuccessHitCount = 0
let sameSignatureUpstreamHitCount = 0
let invalidRequestSwitchUpstreamHitCount = 0
let sameAccountRetryFirstAccountHitCount = 0
let sameAccountRetrySecondAccountHitCount = 0
let unknownSwitchFirstAccountHitCount = 0
let unknownSwitchSecondAccountHitCount = 0
let nonStreamFirstByteTimeoutFirstAccountHitCount = 0
let nonStreamFirstByteTimeoutSecondAccountHitCount = 0
let nonStreamBodyInterruptedFirstAccountHitCount = 0
let nonStreamBodyInterruptedSecondAccountHitCount = 0
let dispatchRaceFirstAccountHitCount = 0
let dispatchRaceSecondAccountHitCount = 0
let dispatchRaceThirdAccountHitCount = 0
let singleAccountLocalSuppressionFastFailHitCount = 0
let singleFailureHitCount = 0
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
const sameAccountRetrySuccessBody = JSON.stringify({
  id: 'chatcmpl-same-account-retry-regression',
  object: 'chat.completion',
  choices: [
    {
      index: 0,
      message: { role: 'assistant', content: 'ok after same account retry' },
      finish_reason: 'stop'
    }
  ],
  usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
})
const nonStreamFirstByteTimeoutSuccessBody = JSON.stringify({
  id: 'chatcmpl-non-stream-first-byte-timeout-regression',
  object: 'chat.completion',
  choices: [
    {
      index: 0,
      message: { role: 'assistant', content: 'ok after non-stream first byte timeout' },
      finish_reason: 'stop'
    }
  ],
  usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
})
const nonStreamBodyInterruptedRetrySuccessBody = JSON.stringify({
  id: 'chatcmpl-non-stream-body-interrupted-client-retry-regression',
  object: 'chat.completion',
  choices: [
    {
      index: 0,
      message: { role: 'assistant', content: 'ok after client retry' },
      finish_reason: 'stop'
    }
  ],
  usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
})
const singleAccountLocalSuppressionShouldNotHitBody = JSON.stringify({
  id: 'chatcmpl-single-account-local-suppression-should-not-hit',
  object: 'chat.completion',
  choices: [
    {
      index: 0,
      message: { role: 'assistant', content: 'should not hit upstream while locally suppressed' },
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
    if (currentScenario === 'single_account_local_suppression_fast_fail') {
      singleAccountLocalSuppressionFastFailHitCount += 1
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(singleAccountLocalSuppressionShouldNotHitBody)
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
    if (currentScenario === 'same_account_retry_success') {
      const authorization = String(req.headers.authorization ?? '')
      if (authorization.includes('sk-request-failure-1')) {
        sameAccountRetryFirstAccountHitCount += 1
        if (sameAccountRetryFirstAccountHitCount === 1) {
          res.writeHead(502, { 'content-type': 'application/json; charset=utf-8' })
          res.end(JSON.stringify({ error: { message: 'temporary first account upstream error before same account retry', type: 'server_error', code: 'bad_gateway' } }))
          return
        }
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.end(sameAccountRetrySuccessBody)
        return
      }
      sameAccountRetrySecondAccountHitCount += 1
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({ id: 'should-not-switch-after-same-account-retry' }))
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
    if (currentScenario === 'non_stream_first_byte_timeout_switch_account_success') {
      const authorization = String(req.headers.authorization ?? '')
      if (authorization.includes('sk-request-failure-1')) {
        nonStreamFirstByteTimeoutFirstAccountHitCount += 1
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        return
      }
      nonStreamFirstByteTimeoutSecondAccountHitCount += 1
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(nonStreamFirstByteTimeoutSuccessBody)
      return
    }
    if (currentScenario === 'non_stream_body_interrupted_after_output_client_retry') {
      const authorization = String(req.headers.authorization ?? '')
      if (authorization.includes('sk-request-failure-1')) {
        nonStreamBodyInterruptedFirstAccountHitCount += 1
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.write('{"id":"chatcmpl-partial",')
        setTimeout(() => {
          res.destroy(new Error('non stream body interrupted regression'))
        }, 20).unref()
        return
      }
      nonStreamBodyInterruptedSecondAccountHitCount += 1
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(nonStreamBodyInterruptedRetrySuccessBody)
      return
    }
    if (currentScenario === 'single_account_failure_default_cooldown') {
      singleFailureHitCount += 1
      res.writeHead(418, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({ error: { message: 'generic upstream failure', type: 'server_error', code: 'generic_failure' } }))
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
    + sameAccountRetryFirstAccountHitCount
    + sameAccountRetrySecondAccountHitCount
    + unknownSwitchFirstAccountHitCount
    + unknownSwitchSecondAccountHitCount
    + nonStreamFirstByteTimeoutFirstAccountHitCount
    + nonStreamFirstByteTimeoutSecondAccountHitCount
    + nonStreamBodyInterruptedFirstAccountHitCount
    + nonStreamBodyInterruptedSecondAccountHitCount
    + dispatchRaceFirstAccountHitCount
    + dispatchRaceSecondAccountHitCount
    + dispatchRaceThirdAccountHitCount
    + singleAccountLocalSuppressionFastFailHitCount
    + singleFailureHitCount
}

function createRegressionApiKey(groupId: string, key: string): { id: string; key: string } {
  const apiKey = repositories.createApiKeyRecord({
    name: `上游失败回归 Key ${key}`,
    groupBindings: [{ groupId, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '回归 API Key 未返回明文密钥')
  return { id: apiKey.id, key: apiKey.key }
}

function assertAccountsRuntimeSuppressedActive(accounts: RegressionAccount[], messagePattern: RegExp, reason: string): void {
  const runtimeSnapshot = accountSideEffects.snapshotGatewayAccountRuntimeAvailability()
  for (const account of accounts) {
    const updated = repositories.findAccountSummary(account.id, access)
    assert(updated, `账号 ${account.name} 不存在`)
    assert.equal(updated.status, 'active', `${reason}：${account.name} 数据库状态应保持正常`)
    assert.equal(updated.schedulable, true, `${reason}：${account.name} 数据库可调度状态应保持正常`)
    assert.equal(updated.cooldownUntil, undefined, `${reason}：${account.name} 不应写入冷却结束时间`)
    assert.equal(updated.lastErrorMessage, undefined, `${reason}：${account.name} 不应写入最近错误`)
    const runtime = runtimeSnapshot[account.id]
    assert(runtime, `${reason}：${account.name} 应存在账号运行态屏障`)
    assert(
      runtime.status === 'local_suppressed' || runtime.status === 'half_open' || runtime.status === 'precheck_pending' || runtime.status === 'precheck_failed',
      `${reason}：${account.name} 运行态应为本地避让或事前确认，实际 ${runtime.status}`
    )
    assert.match(runtime.reason ?? '', messagePattern, `${reason}：${account.name} 运行态原因应保留真实上游摘要，实际 ${runtime.reason ?? ''}`)
  }
}

function assertAccountsActive(accounts: RegressionAccount[], reason: string): void {
  for (const account of accounts) {
    const updated = repositories.findAccountSummary(account.id, access)
    assert(updated, `账号 ${account.name} 不存在`)
    assert.equal(updated.status, 'active', `${reason}：${account.name} 应保持正常`)
    assert.equal(updated.schedulable, true, `${reason}：${account.name} 应保持可调度`)
    assert.equal(updated.cooldownUntil, undefined, `${reason}：${account.name} 不应写入冷却时间`)
    assert.equal(updated.lastErrorMessage, undefined, `${reason}：${account.name} 不应写入最近错误`)
  }
}

function restoreRegressionAccounts(accounts: RegressionAccount[]): void {
  for (const account of accounts) {
    repositories.clearAccountFailureState(account.id, access)
  }
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  gatewayCache.clearGatewayRuntimeCache()
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
