import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import {
  gatewayTimeoutProfileForLane,
  type GatewayTimeoutSettings
} from '../../modules/gateway/policy/timeout-profile.js'
import { normalRouteFirstByteDeadlineAppliesToLane } from '../../modules/gateway/policy/speed-first-lane.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'
import { tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'

const untrustedImageFailureScenarios = [
  {
    id: 'generation_401_model_not_found',
    path: '/v1/images/generations',
    statusCode: 401,
    errorCode: 'model_not_found'
  },
  {
    id: 'generation_400_model_not_supported',
    path: '/v1/images/generations',
    statusCode: 400,
    errorCode: 'model_not_supported'
  },
  {
    id: 'edit_401_model_not_found',
    path: '/v1/images/edits',
    statusCode: 401,
    errorCode: 'model_not_found'
  },
  {
    id: 'edit_400_model_not_supported',
    path: '/v1/images/edits',
    statusCode: 400,
    errorCode: 'model_not_supported'
  }
] as const

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
const accountSideEffects = await import('../../modules/gateway/runtime/account-side-effects.service.js')
const accountCircuit = await import('../../modules/gateway/runtime/account-circuit.service.js')
const apiKeyFailureGuard = await import('../../modules/gateway/runtime/account-api-key-failure-guard.service.js')
const proxyHealth = await import('../../modules/gateway/runtime/proxy-health.service.js')

const gatewayRoutesSource = readFileSync(new URL('../../modules/gateway/routes.ts', import.meta.url), 'utf8')
const upstreamDispatchSource = readFileSync(new URL('../../modules/gateway/dispatch/upstream-dispatch.ts', import.meta.url), 'utf8')
assert.match(
  gatewayRoutesSource,
  /if \(handledResponse\.protocolValidatedSuccess === true\) \{\s*await confirmHalfOpenSuccess\(\)\s*await confirmSameAccountApiKeyFailures\(\)/,
  '只有协议验证成功才能确认 half-open 和同账号前序 Key 失败，未知 2xx 与资源非 2xx 均不得恢复运行态'
)
const preparedRequestIndex = upstreamDispatchSource.indexOf('const requestParts = await buildPreparedUpstreamRequestParts')
const upstreamAttemptIndex = upstreamDispatchSource.indexOf('const response = await performUpstreamRequestAttempt')
assert(preparedRequestIndex >= 0 && upstreamAttemptIndex > preparedRequestIndex, '真实上游 attempt 必须在账户、Key 和请求体准备完成后发起')
assert.doesNotMatch(upstreamDispatchSource, /OpaqueFailoverBudget|maxOpaqueFailoverAccountsPerRequest|opaqueFailoverBudget/, '通用请求不得保留固定四账户预算')
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
const untrustedImageBodyAcceptanceCounts = new Map<string, number>()
const realDateNow = Date.now.bind(Date)
const realSetTimeout = globalThis.setTimeout.bind(globalThis)
let imageLongRequestAccepted = false
let imageLongFailureRequestAccepted = false
let mappedImageLongFailureRequestAccepted = false
let imageTimeoutRequestAccepted = false
let upstreamServer: http.Server | undefined
let appServer: http.Server | undefined

try {
  settingsRepository.updateSettings({ temporaryUnschedulableRetryAttempts: 0 })
  gatewayCache.clearGatewayRuntimeCache()

  upstreamServer = http.createServer((req, res) => {
    hits.push(req.url ?? '')
    upstreamAuthorizations.push(String(req.headers.authorization ?? ''))
    const path = req.url?.split('?', 1)[0] ?? ''
    const untrustedImageFailureId = new URL(req.url ?? '/', 'http://127.0.0.1')
      .searchParams
      .get('mock_untrusted_image_failure')
    const untrustedImageFailure = untrustedImageFailureScenarios.find((scenario) => (
      scenario.id === untrustedImageFailureId && scenario.path === path
    ))
    if (
      untrustedImageFailure
      && req.headers.authorization === 'Bearer sk-generic-image-bad'
    ) {
      req.resume()
      req.once('end', () => {
        untrustedImageBodyAcceptanceCounts.set(
          untrustedImageFailure.id,
          (untrustedImageBodyAcceptanceCounts.get(untrustedImageFailure.id) ?? 0) + 1
        )
        res.writeHead(untrustedImageFailure.statusCode, {
          'content-type': 'application/json; charset=utf-8',
          'x-vendor-secret': 'must-not-reach-client'
        })
        res.end(JSON.stringify({
          error: {
            type: 'vendor_private_image_error',
            code: untrustedImageFailure.errorCode,
            message: `vendor-image-matrix-message-${untrustedImageFailure.id}`
          }
        }))
      })
      return
    }
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
    if (req.url?.includes('mock_codex_json_error=1')) {
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end('{"error":{"type":"server_error","code":"system_default_must_not_retry","message":"complete response remains transparent"}}')
      return
    }
    if (req.url?.includes('mock_explicit_policy=1')) {
      if (req.headers.authorization === 'Bearer sk-generic-opaque-good') {
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.end('{"id":"explicit_policy_fallback_success","choices":[{"message":{"role":"assistant","content":"explicit retry completed"}}]}')
        return
      }
      res.writeHead(429, { 'content-type': 'application/json; charset=utf-8' })
      res.end('{"error":{"type":"rate_limit_error","code":"explicit_retry_next","message":"configured retry"}}')
      return
    }
    if (
      path === '/v1/responses'
      && req.url?.includes('mock_mapped_image_transport_drop=1')
      && req.headers.authorization === 'Bearer sk-generic-image-mapping-transport-bad'
    ) {
      req.resume()
      req.once('end', () => res.destroy())
      return
    }
    if (
      path === '/v1/responses'
      && req.url?.includes('mock_mapped_image_transport_drop=1')
      && req.headers.authorization === 'Bearer sk-generic-image-mapping-transport-good'
    ) {
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end('{"id":"mapped_image_transport_fallback_success","object":"response","status":"completed","output":[]}')
      return
    }
    if (path === '/v1/responses' && req.url?.includes('mock_mapped_image_http_failure=1')) {
      if (req.headers.authorization === 'Bearer sk-generic-image-mapping-transport-good') {
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.end('{"id":"mapped_image_replay_was_not_blocked"}')
        return
      }
      req.resume()
      req.once('end', () => {
        mappedImageLongFailureRequestAccepted = true
        res.writeHead(418, { 'content-type': 'application/json; charset=utf-8' })
        res.end('{"error":{"code":"mapped_image_unknown_failure","message":"untrusted mapped image error"}}')
      })
      return
    }
    if (
      path === '/v1/responses'
      && req.url?.includes('mock_responses_image_tool_transport_drop=1')
      && req.headers.authorization === 'Bearer sk-generic-responses-image-tool-transport-bad'
    ) {
      req.resume()
      req.once('end', () => res.destroy())
      return
    }
    if (
      path === '/v1/responses'
      && req.url?.includes('mock_responses_image_tool_transport_drop=1')
      && req.headers.authorization === 'Bearer sk-generic-responses-image-tool-transport-good'
    ) {
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end('{"id":"image_tool_transport_fallback_success","object":"response","status":"completed","output":[]}')
      return
    }
    if (path === '/v1/responses' && req.url?.includes('mock_responses_image_tool_missing_terminal=1')) {
      res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
      if (req.headers.authorization === 'Bearer sk-generic-responses-image-tool-stream-good') {
        res.end([
          'event: response.completed',
          'data: {"type":"response.completed","response":{"id":"image_tool_replay_was_not_blocked","status":"completed"}}',
          '',
          ''
        ].join('\n'))
        return
      }
      res.end([
        'event: response.created',
        'data: {"type":"response.created","response":{"id":"image_tool_outcome_unknown","status":"in_progress"}}',
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
    if (
      path === '/v1/images/generations'
      && req.url?.includes('mock_image_transport_drop=1')
      && req.headers.authorization === 'Bearer sk-generic-image-transport-bad'
    ) {
      req.resume()
      req.once('end', () => res.destroy())
      return
    }
    if (
      path === '/v1/images/generations'
      && req.url?.includes('mock_image_bad_gzip=1')
      && req.headers.authorization === 'Bearer sk-generic-image-body-bad'
    ) {
      res.writeHead(200, {
        'content-type': 'application/json; charset=utf-8',
        'content-encoding': 'gzip'
      })
      res.end('provider-accepted-but-returned-invalid-gzip')
      return
    }
    if (
      path === '/v1/images/generations'
      && req.url?.includes('mock_image_long_running=1')
      && req.headers.authorization === 'Bearer sk-generic-image-long-running'
    ) {
      req.resume()
      req.once('end', () => {
        imageLongRequestAccepted = true
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        setTimeout(() => {
          res.end('{"created":1,"data":[{"b64_json":"bG9uZy1pbWFnZQ=="}]}')
        }, 25)
      })
      return
    }
    if (
      path === '/v1/images/generations'
      && req.url?.includes('mock_image_long_failure=1')
      && req.headers.authorization === 'Bearer sk-generic-image-long-running'
    ) {
      req.resume()
      req.once('end', () => {
        imageLongFailureRequestAccepted = true
        res.writeHead(502, {
          'content-type': 'application/json; charset=utf-8',
          'x-vendor-secret': 'long-image-private-error'
        })
        res.end('{"error":{"code":"vendor_long_image_failure","message":"long image failed after provider accepted it"}}')
      })
      return
    }
    if (
      path === '/v1/images/generations'
      && req.url?.includes('mock_image_first_response_timeout=1')
      && req.headers.authorization === 'Bearer sk-generic-image-long-running'
    ) {
      req.resume()
      req.once('end', () => {
        imageTimeoutRequestAccepted = true
      })
      return
    }
    if (
      req.headers.authorization === 'Bearer sk-generic-opaque-good'
      || req.headers.authorization === 'Bearer sk-generic-image-good'
      || req.headers.authorization === 'Bearer sk-generic-image-transport-good'
      || req.headers.authorization === 'Bearer sk-generic-image-transport-cross-group-good'
      || req.headers.authorization === 'Bearer sk-generic-image-body-good'
      || req.headers.authorization === 'Bearer sk-generic-image-policy-good'
      || req.headers.authorization === 'Bearer sk-generic-image-long-good'
      || req.headers.authorization === 'Bearer sk-generic-image-mapping-transport-good'
      || req.headers.authorization === 'Bearer sk-generic-responses-image-tool-transport-good'
      || req.headers.authorization === 'Bearer sk-generic-responses-image-tool-stream-good'
    ) {
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(path.startsWith('/v1/images/')
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
      base_url: upstreamBaseUrl,
      error_handling_rules: [{
        enabled: true,
        name: '显式 429 切号',
        priority: 1,
        status_codes: [429],
        action: 'retry_next'
      }, {
        enabled: true,
        name: '仅指定正文允许 418 切号',
        priority: 2,
        status_codes: [418],
        keywords: ['configured-body-marker'],
        action: 'retry_next'
      }]
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
  const imageLongGroup = repositories.createGroup({
    name: '图片独立长时限分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const imageLongAccounts = [
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '图片独立长时限首选账号',
      type: 'api_key',
      credentials: { api_key: 'sk-generic-image-long-running', base_url: upstreamBaseUrl },
      groupId: imageLongGroup.id,
      supportedModels: ['gpt-image-1'],
      healthCheckModel: 'gpt-image-1',
      status: 'active',
      schedulable: true,
      priority: 0
    }, access),
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '图片独立长时限后备账号',
      type: 'api_key',
      credentials: { api_key: 'sk-generic-image-long-good', base_url: upstreamBaseUrl },
      groupId: imageLongGroup.id,
      supportedModels: ['gpt-image-1'],
      healthCheckModel: 'gpt-image-1',
      status: 'active',
      schedulable: true,
      priority: 10,
      fallbackEnabled: true
    }, access)
  ]
  for (const imageLongAccount of imageLongAccounts) {
    repositories.recordAccountHealthCheckSuccess(imageLongAccount.id, {
      intervalHours: 12,
      jitterMinutes: 0,
      failureThreshold: 3,
      statusCode: 200
    })
  }
  const imageLongApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '图片独立长时限 Key',
    groupBindings: [{ groupId: imageLongGroup.id, priority: 1, status: 'active' }],
    normalRoutingConfig: {
      schedulingPreference: 'speed_first',
      firstByteDeadlineMs: 10_000
    },
    status: 'active'
  }, access)
  assert(imageLongApiKey.key)
  const createImageReplayScenario = (suffix: 'transport' | 'body') => {
    const scenarioGroup = repositories.createGroup({
      name: `图片统一切号-${suffix}`,
      providerCode: GPT_VENDOR_CODE,
      enabled: true
    }, access)
    const scenarioAccounts = [
      repositories.createAccount({
        providerCode: GPT_VENDOR_CODE,
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
        name: `图片统一切号-${suffix}-首选`,
        type: 'api_key',
        credentials: { api_key: `sk-generic-image-${suffix}-bad`, base_url: upstreamBaseUrl },
        groupId: scenarioGroup.id,
        supportedModels: ['gpt-image-1'],
        healthCheckModel: 'gpt-image-1',
        status: 'active',
        schedulable: true,
        priority: 0
      }, access),
      repositories.createAccount({
        providerCode: GPT_VENDOR_CODE,
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
        name: `图片统一切号-${suffix}-后备`,
        type: 'api_key',
        credentials: { api_key: `sk-generic-image-${suffix}-good`, base_url: upstreamBaseUrl },
        groupId: scenarioGroup.id,
        supportedModels: ['gpt-image-1'],
        healthCheckModel: 'gpt-image-1',
        status: 'active',
        schedulable: true,
        priority: 10,
        fallbackEnabled: true
      }, access)
    ]
    const groupBindings = [{ groupId: scenarioGroup.id, priority: 1, status: 'active' as const }]
    if (suffix === 'transport') {
      const crossGroup = repositories.createGroup({
        name: '图片统一切号-transport-跨组后备',
        providerCode: GPT_VENDOR_CODE,
        enabled: true
      }, access)
      scenarioAccounts.push(repositories.createAccount({
        providerCode: GPT_VENDOR_CODE,
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
        name: '图片统一切号-transport-跨组后备',
        type: 'api_key',
        credentials: { api_key: 'sk-generic-image-transport-cross-group-good', base_url: upstreamBaseUrl },
        groupId: crossGroup.id,
        supportedModels: ['gpt-image-1'],
        healthCheckModel: 'gpt-image-1',
        status: 'active',
        schedulable: true,
        priority: 0
      }, access))
      groupBindings.push({ groupId: crossGroup.id, priority: 2, status: 'active' })
    }
    for (const scenarioAccount of scenarioAccounts) {
      repositories.recordAccountHealthCheckSuccess(scenarioAccount.id, {
        intervalHours: 12,
        jitterMinutes: 0,
        failureThreshold: 3,
        statusCode: 200
      })
    }
    const scenarioApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: `图片统一切号-${suffix}-Key`,
      groupBindings,
      status: 'active'
    }, access)
    assert(scenarioApiKey.key)
    return { apiKey: scenarioApiKey, accounts: scenarioAccounts }
  }
  const imageTransportScenario = createImageReplayScenario('transport')
  const imageBodyScenario = createImageReplayScenario('body')
  const imagePolicyGroup = repositories.createGroup({
    name: '图片显式错误策略切换',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const imagePolicyAccounts = [
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '图片显式错误策略切换-首选',
      type: 'api_key',
      credentials: {
        api_key: 'sk-generic-image-policy-bad',
        base_url: upstreamBaseUrl,
        error_handling_rules: [{
          enabled: true,
          name: '用户显式配置图片 418 切号',
          priority: 1,
          status_codes: [418],
          action: 'retry_next'
        }]
      },
      groupId: imagePolicyGroup.id,
      supportedModels: ['gpt-image-1'],
      healthCheckModel: 'gpt-image-1',
      status: 'active',
      schedulable: true,
      priority: 0
    }, access),
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '图片显式错误策略切换-后备',
      type: 'api_key',
      credentials: { api_key: 'sk-generic-image-policy-good', base_url: upstreamBaseUrl },
      groupId: imagePolicyGroup.id,
      supportedModels: ['gpt-image-1'],
      healthCheckModel: 'gpt-image-1',
      status: 'active',
      schedulable: true,
      priority: 10,
      fallbackEnabled: true
    }, access)
  ]
  const imageMappingGroup = repositories.createGroup({
    name: '映射图片模型统一切号',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const mappedImageModel = 'gpt-5.5'
  const mappedImageUpstreamModel = 'flux-image-regression'
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: mappedImageUpstreamModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    supportedApiProtocols: ['responses', 'images'],
    outputUsdPerImage: 0.01,
    actorSystemAccountId: access.systemAccountId
  })
  const mappedImageModelRule = {
    sourceModel: mappedImageModel,
    sourceEndpointFamily: 'responses' as const,
    upstreamModel: mappedImageUpstreamModel,
    upstreamEndpointFamily: 'responses' as const,
    enabled: true
  }
  const imageMappingAccounts = [
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '映射图片模型统一切号-首选',
      type: 'api_key',
      credentials: {
        api_key: 'sk-generic-image-mapping-transport-bad',
        base_url: upstreamBaseUrl,
        error_handling_rules: [{
          enabled: true,
          name: '映射图片仅匹配指定正文',
          priority: 1,
          status_codes: [418],
          keywords: ['configured-mapped-image-marker'],
          action: 'retry_next'
        }]
      },
      groupId: imageMappingGroup.id,
      supportedModels: [mappedImageUpstreamModel],
      modelMappings: [mappedImageModelRule],
      healthCheckModel: mappedImageUpstreamModel,
      status: 'active',
      schedulable: true,
      priority: 0
    }, access),
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '映射图片模型统一切号-后备',
      type: 'api_key',
      credentials: { api_key: 'sk-generic-image-mapping-transport-good', base_url: upstreamBaseUrl },
      groupId: imageMappingGroup.id,
      supportedModels: [mappedImageUpstreamModel],
      modelMappings: [mappedImageModelRule],
      healthCheckModel: mappedImageUpstreamModel,
      status: 'active',
      schedulable: true,
      priority: 10,
      fallbackEnabled: true
    }, access)
  ]
  const responsesImageToolGroup = repositories.createGroup({
    name: 'Responses 图片工具统一切号',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const responsesImageToolAccounts = [
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: 'Responses 图片工具统一切号-首选',
      type: 'api_key',
      credentials: { api_key: 'sk-generic-responses-image-tool-transport-bad', base_url: upstreamBaseUrl },
      groupId: responsesImageToolGroup.id,
      supportedModels: ['gpt-5.5'],
      healthCheckModel: 'gpt-5.5',
      status: 'active',
      schedulable: true,
      priority: 0
    }, access),
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: 'Responses 图片工具统一切号-后备',
      type: 'api_key',
      credentials: { api_key: 'sk-generic-responses-image-tool-transport-good', base_url: upstreamBaseUrl },
      groupId: responsesImageToolGroup.id,
      supportedModels: ['gpt-5.5'],
      healthCheckModel: 'gpt-5.5',
      status: 'active',
      schedulable: true,
      priority: 10,
      fallbackEnabled: true
    }, access)
  ]
  const responsesImageToolStreamGroup = repositories.createGroup({
    name: 'Responses 图片工具预提交统一切号',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const responsesImageToolStreamAccounts = [
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: 'Responses 图片工具预提交统一切号-首选',
      type: 'api_key',
      credentials: { api_key: 'sk-generic-responses-image-tool-stream-bad', base_url: upstreamBaseUrl },
      groupId: responsesImageToolStreamGroup.id,
      supportedModels: ['gpt-5.5'],
      healthCheckModel: 'gpt-5.5',
      status: 'active',
      schedulable: true,
      priority: 0
    }, access),
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: 'Responses 图片工具预提交统一切号-后备',
      type: 'api_key',
      credentials: { api_key: 'sk-generic-responses-image-tool-stream-good', base_url: upstreamBaseUrl },
      groupId: responsesImageToolStreamGroup.id,
      supportedModels: ['gpt-5.5'],
      healthCheckModel: 'gpt-5.5',
      status: 'active',
      schedulable: true,
      priority: 10,
      fallbackEnabled: true
    }, access)
  ]
  for (const scenarioAccount of [
    ...imagePolicyAccounts,
    ...imageMappingAccounts,
    ...responsesImageToolAccounts,
    ...responsesImageToolStreamAccounts
  ]) {
    repositories.recordAccountHealthCheckSuccess(scenarioAccount.id, {
      intervalHours: 12,
      jitterMinutes: 0,
      failureThreshold: 3,
      statusCode: 200
    })
  }
  const imagePolicyApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '图片显式错误策略切换-Key',
    groupBindings: [{ groupId: imagePolicyGroup.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  const imageMappingApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '映射图片模型统一切号-Key',
    groupBindings: [{ groupId: imageMappingGroup.id, priority: 1, status: 'active' }],
    normalRoutingConfig: {
      schedulingPreference: 'speed_first',
      firstByteDeadlineMs: 10_000
    },
    status: 'active'
  }, access)
  const responsesImageToolApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'Responses 图片工具统一切号-Key',
    groupBindings: [{ groupId: responsesImageToolGroup.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  const responsesImageToolStreamApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'Responses 图片工具预提交统一切号-Key',
    groupBindings: [{ groupId: responsesImageToolStreamGroup.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(
    imagePolicyApiKey.key
    && imageMappingApiKey.key
    && responsesImageToolApiKey.key
    && responsesImageToolStreamApiKey.key
  )
  repositories.updateSystemAccount(access.systemAccountId, { imageGenerationEnabled: true })

  appServer = http.createServer(app)
  await listen(appServer)
  const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

  const timeoutProfile = gatewayTimeoutProfileForLane(
    settingsRepository.getSettings() as unknown as GatewayTimeoutSettings,
    'image'
  )
  assert.equal(timeoutProfile.firstResponseTimeoutMs, 600_000, '图片 lane 默认首响应时限应为独立 600 秒')
  assert.equal(timeoutProfile.uncommittedAttemptMaxLifetimeMs, 3_600_000, '图片 lane 未提交 attempt 应使用独立一小时上限')
  assert.equal(normalRouteFirstByteDeadlineAppliesToLane('image'), false, '图片 lane 不得继承文本 speed-first 首字切换')

  const nonStream = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: { authorization: `Bearer ${apiKey.key}`, 'content-type': 'application/json' },
    body: JSON.stringify({ model: 'gpt-5.5', messages: [{ role: 'user', content: 'opaque status' }], stream: false })
  })
  const nonStreamText = await nonStream.text()
  assert.equal(nonStream.status, 200, `通用客户端安全推理 HTTP 失败应请求内切换，实际 ${nonStream.status}: ${nonStreamText}`)
  assert.match(nonStreamText, /server failover completed/)
  assert.equal(upstreamAuthorizations.length, 3, '状态预筛选命中但正文不命中时应复用正文并依次尝试两个 Key 与后备账户')
  assert.deepEqual(new Set(upstreamAuthorizations.slice(0, 2)), new Set([
    'Bearer sk-generic-opaque-bad-a',
    'Bearer sk-generic-opaque-bad-b'
  ]))
  assert.equal(upstreamAuthorizations[2], 'Bearer sk-generic-opaque-good')

  const exactHitOffset = upstreamAuthorizations.length
  const exactNonStream = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json',
      'x-codex-turn-metadata': JSON.stringify({ turn_id: `turn-exact-http-${Date.now()}` })
    },
    body: JSON.stringify({ model: 'gpt-5.5', messages: [{ role: 'user', content: 'exact status' }], stream: false })
  })
  const exactNonStreamText = await exactNonStream.text()
  assert.equal(exactNonStream.status, 200, `精确客户端安全推理 HTTP 失败应请求内切换，实际 ${exactNonStream.status}: ${exactNonStreamText}`)
  assert.match(exactNonStreamText, /server failover completed/)
  assert.equal(upstreamAuthorizations.length - exactHitOffset, 3, '精确客户端未知完整非 2xx 也应只做当前请求内的有界接管')

  const explicitHitOffset = upstreamAuthorizations.length
  const explicitRetry = await fetch(`${baseUrl}/v1/chat/completions?mock_explicit_policy=1`, {
    method: 'POST',
    headers: { authorization: `Bearer ${apiKey.key}`, 'content-type': 'application/json' },
    body: JSON.stringify({ model: 'gpt-5.5', messages: [{ role: 'user', content: 'configured retry' }], stream: false })
  })
  const explicitRetryText = await explicitRetry.text()
  assert.equal(explicitRetry.status, 200, `显式 retry_next 仍应切换后备账号，实际 ${explicitRetry.status}: ${explicitRetryText}`)
  assert.match(explicitRetryText, /explicit retry completed/)
  assert.equal(upstreamAuthorizations.length - explicitHitOffset, 2, '显式 retry_next 应只切换一次账户，不得轮换同账户 Key')
  const explicitPolicyAccount = repositories.findOpenAIAccountForGroup(group.id, account.id, access.systemAccountId)
  assert(explicitPolicyAccount, '显式策略回归必须能读取真实调度账户')
  assert.equal(
    proxyHealth.recordGatewayUpstreamBucketSuccess(explicitPolicyAccount),
    false,
    '显式 retry_next 只能切换当前请求，不得附带自动上游桶失败状态'
  )

  const imageDispatchAccounts = [
    repositories.findOpenAIAccountForGroup(imageGroup.id, imageBadAccount.id, access.systemAccountId),
    repositories.findOpenAIAccountForGroup(imageGroup.id, imageGoodAccount.id, access.systemAccountId)
  ]
  assert(imageDispatchAccounts.every((candidate) => candidate), '图片状态码矩阵必须能读取首选与后备调度账户')
  const imageCircuitScopes = imageDispatchAccounts.flatMap((candidate) => {
    assert(candidate)
    const protocolModelScope = accountCircuit.gatewayAccountProtocolModelScope(candidate, 'image', 'gpt-image-1')
    return [
      { kind: 'account' as const, accountRuntimeKey: protocolModelScope.accountRuntimeKey },
      protocolModelScope
    ]
  })
  const imageCircuitStore = accountCircuit.getGatewayAccountCircuitStore()
  const imageAccountIds = [imageBadAccount.id, imageGoodAccount.id]
  const imageAccountAvailabilitySnapshot = () => imageAccountIds.map((accountId) => {
    const persisted = repositories.findAccountForTest(accountId, access)
    assert(persisted, `图片状态码矩阵缺少账户 ${accountId}`)
    return {
      accountId,
      status: persisted.status,
      schedulable: persisted.schedulable,
      temporaryUnavailable: persisted.apiKeyRuntime?.temporaryUnavailable ?? 0,
      allUnavailable: persisted.apiKeyRuntime?.allUnavailable ?? false
    }
  })
  const imageRuntimeSnapshot = () => {
    const runtime = accountSideEffects.snapshotGatewayAccountRuntimeAvailability()
    return imageAccountIds.map((accountId) => runtime[accountId])
  }
  const imageApiKeyGuardSnapshot = () => apiKeyFailureGuard
    .getGatewayAccountApiKeyFailureGuardSnapshotForTest()
    .filter((entry) => imageAccountIds.includes(entry.accountId))
  const imageCircuitIncidentSnapshot = () => databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT circuit_scope_key, state, generation, transition_id, updated_at_ms
      FROM account_circuit_incidents
      WHERE account_id IN (?, ?)
      ORDER BY circuit_scope_key
    `)
    .all(imageBadAccount.id, imageGoodAccount.id) as unknown[]
  const imageCircuitSnapshot = async () => await Promise.all(
    imageCircuitScopes.map(async (scope) => await imageCircuitStore.get(scope))
  )

  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  for (const scenario of untrustedImageFailureScenarios) {
    const hitOffset = hits.length
    const authorizationOffset: number = upstreamAuthorizations.length
    const availabilityBefore = imageAccountAvailabilitySnapshot()
    const runtimeBefore = imageRuntimeSnapshot()
    const apiKeyGuardBefore = imageApiKeyGuardSnapshot()
    const sideEffectStateBefore = accountSideEffects.getGatewayAccountSideEffectState()
    const circuitBefore = await imageCircuitSnapshot()
    const circuitIncidentsBefore = imageCircuitIncidentSnapshot()
    assert.deepEqual(runtimeBefore, [undefined, undefined], `${scenario.id} 前置条件不得已有账户 runtime 屏障`)
    assert.deepEqual(apiKeyGuardBefore, [], `${scenario.id} 前置条件不得已有物理 Key failure guard`)
    assert(circuitBefore.every((state) => state.phase === 'CLOSED'), `${scenario.id} 前置条件 circuit 必须全部 CLOSED`)

    const scenarioUrl = `${baseUrl}${scenario.path}?mock_untrusted_image_failure=${scenario.id}`
    const response = await fetch(scenarioUrl, {
      method: 'POST',
      headers: { authorization: `Bearer ${imageApiKey.key}`, 'content-type': 'application/json' },
      body: JSON.stringify({
        model: 'gpt-image-1',
        prompt: `${scenario.id} must use unified failover`,
        ...(scenario.path === '/v1/images/edits' ? { image: 'mock-image' } : {})
      })
    })
    const responseText = await response.text()
    assert.equal(response.status, 200, `${scenario.id} 必须切到健康后备并返回成功：${responseText}`)
    assert.match(responseText, /aW1hZ2U=/, `${scenario.id} 必须交付后备图片结果`)
    assert.equal(response.headers.get('x-vendor-secret'), null, `${scenario.id} 不得透传供应商私有响应头`)
    assert.doesNotMatch(
      responseText,
      /model_not_found|model_not_supported|vendor_private_image_error|vendor-image-matrix-message|upstream_retryable_error/,
      `${scenario.id} 不得泄漏供应商语义或提示客户端自动重放`
    )
    assert.equal(
      untrustedImageBodyAcceptanceCounts.get(scenario.id),
      1,
      `${scenario.id} 失败首选 Mock 必须完整接收请求体且只能执行一次`
    )
    assert.deepEqual(
      hits.slice(hitOffset),
      [
        `${scenario.path}?mock_untrusted_image_failure=${scenario.id}`,
        `${scenario.path}?mock_untrusted_image_failure=${scenario.id}`
      ],
      `${scenario.id} 必须各命中一次失败首选和健康后备`
    )
    assert.deepEqual(
      upstreamAuthorizations.slice(authorizationOffset),
      ['Bearer sk-generic-image-bad', 'Bearer sk-generic-image-good'],
      `${scenario.id} 必须按统一规则切到健康后备账户`
    )

    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    assert.deepEqual(imageAccountAvailabilitySnapshot(), availabilityBefore, `${scenario.id} 不得修改首选或后备账户可用状态`)
    assert.deepEqual(imageRuntimeSnapshot(), runtimeBefore, `${scenario.id} 不得写账户 runtime 屏障`)
    assert.deepEqual(imageApiKeyGuardSnapshot(), apiKeyGuardBefore, `${scenario.id} 不得写物理 Key failure guard`)
    assert.deepEqual(accountSideEffects.getGatewayAccountSideEffectState(), sideEffectStateBefore, `${scenario.id} 不得写账户副作用队列`)
    assert.deepEqual(await imageCircuitSnapshot(), circuitBefore, `${scenario.id} 不得修改 account 或 protocol/model circuit`)
    assert.deepEqual(imageCircuitIncidentSnapshot(), circuitIncidentsBefore, `${scenario.id} 不得持久化 circuit incident`)
  }

  const imageLongHitOffset = upstreamAuthorizations.length
  const imageLongAcceptedAtMs = realDateNow()
  let imageTextFirstTokenDeadlineScheduled = false
  Date.now = () => imageLongRequestAccepted
    ? imageLongAcceptedAtMs + 300_000
    : realDateNow()
  globalThis.setTimeout = ((callback: (...args: unknown[]) => void, delay?: number, ...args: unknown[]) => {
    const callStack = new Error().stack ?? ''
    if (delay === 10_000 && /gateway[\\/]upstream[\\/]request/.test(callStack)) {
      imageTextFirstTokenDeadlineScheduled = true
      return realSetTimeout(callback, 1, ...args)
    }
    return realSetTimeout(callback, delay, ...args)
  }) as typeof globalThis.setTimeout
  let imageLongResponse: Response
  try {
    imageLongResponse = await fetch(`${baseUrl}/v1/images/generations?mock_image_long_running=1`, {
      method: 'POST',
      headers: { authorization: `Bearer ${imageLongApiKey.key}`, 'content-type': 'application/json' },
      body: JSON.stringify({ model: 'gpt-image-1', prompt: 'long image must outlive text wall budget' })
    })
  } finally {
    Date.now = realDateNow
    globalThis.setTimeout = realSetTimeout
  }
  const imageLongText = await imageLongResponse.text()
  assert.equal(imageLongRequestAccepted, true, '长耗时图片 Mock 必须确认业务请求已经由上游接收')
  assert.equal(imageLongResponse.status, 200, `图片已在途时不得被文本 270 秒 wall budget 中止：${imageLongText}`)
  assert.match(imageLongText, /bG9uZy1pbWFnZQ==/)
  assert.equal(imageTextFirstTokenDeadlineScheduled, false, '速度优先策略不得给图片 attempt 安排文本首 token 截止计时器')
  assert.deepEqual(upstreamAuthorizations.slice(imageLongHitOffset), [
    'Bearer sk-generic-image-long-running'
  ], '图片跨过文本 wall budget 后仍由已成功首选交付，不应无故切号')

  const imageLongFailureHitOffset = upstreamAuthorizations.length
  const imageLongFailureAcceptedAtMs = realDateNow()
  Date.now = () => imageLongFailureRequestAccepted
    ? imageLongFailureAcceptedAtMs + 300_000
    : realDateNow()
  let imageLongFailureResponse: Response
  try {
    imageLongFailureResponse = await fetch(`${baseUrl}/v1/images/generations?mock_image_long_failure=1`, {
      method: 'POST',
      headers: { authorization: `Bearer ${imageLongApiKey.key}`, 'content-type': 'application/json' },
      body: JSON.stringify({ model: 'gpt-image-1', prompt: 'long failed image must still reach a healthy fallback' })
    })
  } finally {
    Date.now = realDateNow
  }
  const imageLongFailureText = await imageLongFailureResponse.text()
  assert.equal(imageLongFailureRequestAccepted, true, '长耗时失败图片 Mock 必须确认业务请求已经由首选上游接收')
  assert.equal(imageLongFailureResponse.status, 200, `图片跨过文本墙钟后失败仍必须切到后备账户：${imageLongFailureText}`)
  assert.match(imageLongFailureText, /aW1hZ2U=/)
  assert.doesNotMatch(imageLongFailureText, /vendor_long_image_failure|long image failed|upstream_retryable_error|upstream_outcome_unknown/)
  assert.equal(imageLongFailureResponse.headers.get('x-vendor-secret'), null, '长图片首选失败的私有响应头不得泄漏')
  assert.deepEqual(upstreamAuthorizations.slice(imageLongFailureHitOffset), [
    'Bearer sk-generic-image-long-running',
    'Bearer sk-generic-image-long-good'
  ], '图片跨过文本 270 秒后失败必须继续调用健康后备账户')

  settingsRepository.updateSettings({ imageFirstResponseTimeoutSeconds: 10 })
  gatewayCache.clearGatewayRuntimeCache()
  const imageTimeoutHitOffset = upstreamAuthorizations.length
  let imageTimeoutTimerIntercepted = false
  globalThis.setTimeout = ((callback: (...args: unknown[]) => void, delay?: number, ...args: unknown[]) => {
    const callStack = new Error().stack ?? ''
    if (
      !imageTimeoutTimerIntercepted
      && delay === 10_000
      && /gateway[\\/]upstream[\\/]request/.test(callStack)
    ) {
      imageTimeoutTimerIntercepted = true
      return realSetTimeout(callback, 25, ...args)
    }
    return realSetTimeout(callback, delay, ...args)
  }) as typeof globalThis.setTimeout
  let imageTimeoutResponse: Response
  try {
    imageTimeoutResponse = await fetch(`${baseUrl}/v1/images/generations?mock_image_first_response_timeout=1`, {
      method: 'POST',
      headers: { authorization: `Bearer ${imageLongApiKey.key}`, 'content-type': 'application/json' },
      body: JSON.stringify({ model: 'gpt-image-1', prompt: 'image-specific timeout must fail over' })
    })
  } finally {
    globalThis.setTimeout = realSetTimeout
    settingsRepository.updateSettings({ imageFirstResponseTimeoutSeconds: 600 })
    gatewayCache.clearGatewayRuntimeCache()
  }
  const imageTimeoutText = await imageTimeoutResponse.text()
  assert.equal(imageTimeoutTimerIntercepted, true, '图片专用首响应计时器必须由 Mock 实际触发')
  assert.equal(imageTimeoutRequestAccepted, true, '图片超时前上游必须已经接收业务请求')
  assert.equal(imageTimeoutResponse.status, 200, imageTimeoutText)
  assert.match(imageTimeoutText, /aW1hZ2U=/)
  assert.doesNotMatch(imageTimeoutText, /上游请求 10s|upstream_retryable_error|upstream_outcome_unknown/, '图片超时不得泄漏首选错误')
  assert.deepEqual(upstreamAuthorizations.slice(imageTimeoutHitOffset), [
    'Bearer sk-generic-image-long-running',
    'Bearer sk-generic-image-long-good'
  ], '图片专用首响应时限到达后必须切到健康后备账户')

  const imageHitOffset = upstreamAuthorizations.length
  const image = await fetch(`${baseUrl}/v1/images/generations`, {
    method: 'POST',
    headers: { authorization: `Bearer ${imageApiKey.key}`, 'content-type': 'application/json' },
    body: JSON.stringify({ model: 'gpt-image-1', prompt: 'server side failover' })
  })
  const imageText = await image.text()
  assert.equal(image.status, 200, `图片完整 HTTP 失败必须切到健康后备，实际 ${image.status}: ${imageText}`)
  assert.match(imageText, /aW1hZ2U=/)
  assert.doesNotMatch(imageText, /upstream_retryable_error|upstream_outcome_unknown/, '后备成功后不得提示客户端重试')
  assert.doesNotMatch(imageText, /opaque non-stream failure|made_up_418/, '图片失败不得把供应商状态语义或正文当作客户端指令')
  assert.deepEqual(upstreamAuthorizations.slice(imageHitOffset), [
    'Bearer sk-generic-image-bad',
    'Bearer sk-generic-image-good'
  ], '未配置显式规则的图片 HTTP 失败也必须切换后备账号')

  const imageEditHitOffset = upstreamAuthorizations.length
  const imageEdit = await fetch(`${baseUrl}/v1/images/edits`, {
    method: 'POST',
    headers: { authorization: `Bearer ${imageApiKey.key}`, 'content-type': 'application/json' },
    body: JSON.stringify({ model: 'gpt-image-1', prompt: 'edit must use unified failover', image: 'mock-image' })
  })
  const imageEditText = await imageEdit.text()
  assert.equal(imageEdit.status, 200, imageEditText)
  assert.match(imageEditText, /aW1hZ2U=/)
  assert.doesNotMatch(imageEditText, /opaque non-stream failure|made_up_418|upstream_retryable_error|upstream_outcome_unknown/)
  assert.deepEqual(upstreamAuthorizations.slice(imageEditHitOffset), [
    'Bearer sk-generic-image-bad',
    'Bearer sk-generic-image-good'
  ], '未配置显式规则的图片编辑 HTTP 失败也必须切换后备账户')

  const imageTransportHitOffset = upstreamAuthorizations.length
  const imageTransportFailure = await fetch(`${baseUrl}/v1/images/generations?mock_image_transport_drop=1`, {
    method: 'POST',
    headers: { authorization: `Bearer ${imageTransportScenario.apiKey.key}`, 'content-type': 'application/json' },
    body: JSON.stringify({ model: 'gpt-image-1', prompt: 'transport failure must use unified failover' })
  })
  const imageTransportFailureText = await imageTransportFailure.text()
  assert.equal(imageTransportFailure.status, 200, imageTransportFailureText)
  assert.match(imageTransportFailureText, /aW1hZ2U=/)
  assert.doesNotMatch(imageTransportFailureText, /upstream_outcome_unknown|upstream_retryable_error/)
  assert.deepEqual(upstreamAuthorizations.slice(imageTransportHitOffset), [
    'Bearer sk-generic-image-transport-bad',
    'Bearer sk-generic-image-transport-good'
  ], '图片请求体发出后 transport 失败必须优先调用同组健康后备账户')

  const imageBodyHitOffset = upstreamAuthorizations.length
  const imageBodyFailure = await fetch(`${baseUrl}/v1/images/generations?mock_image_bad_gzip=1`, {
    method: 'POST',
    headers: { authorization: `Bearer ${imageBodyScenario.apiKey.key}`, 'content-type': 'application/json' },
    body: JSON.stringify({ model: 'gpt-image-1', prompt: 'body failure must use unified failover' })
  })
  const imageBodyFailureText = await imageBodyFailure.text()
  assert.equal(imageBodyFailure.status, 200, imageBodyFailureText)
  assert.match(imageBodyFailureText, /aW1hZ2U=/)
  assert.doesNotMatch(imageBodyFailureText, /upstream_outcome_unknown|upstream_retryable_error/)
  assert.deepEqual(upstreamAuthorizations.slice(imageBodyHitOffset), [
    'Bearer sk-generic-image-body-bad',
    'Bearer sk-generic-image-body-good'
  ], '图片 2xx 响应头后的正文解码失败必须在下游未提交时调用后备账户')

  const imagePolicyHitOffset = upstreamAuthorizations.length
  const imagePolicyRetry = await fetch(`${baseUrl}/v1/images/generations`, {
    method: 'POST',
    headers: { authorization: `Bearer ${imagePolicyApiKey.key}`, 'content-type': 'application/json' },
    body: JSON.stringify({ model: 'gpt-image-1', prompt: 'explicit user policy may switch accounts' })
  })
  const imagePolicyRetryText = await imagePolicyRetry.text()
  assert.equal(imagePolicyRetry.status, 200, `显式状态策略不得改变统一切号准入，实际 ${imagePolicyRetry.status}: ${imagePolicyRetryText}`)
  assert.match(imagePolicyRetryText, /aW1hZ2U=/)
  assert.doesNotMatch(imagePolicyRetryText, /made_up_418|opaque non-stream failure|upstream_retryable_error|upstream_outcome_unknown/)
  assert.deepEqual(upstreamAuthorizations.slice(imagePolicyHitOffset), [
    'Bearer sk-generic-image-policy-bad',
    'Bearer sk-generic-image-policy-good'
  ], '图片请求的显式 retry_next 与统一规则均应调用同一个健康后备账户')

  const imageMappingHttpHitOffset = upstreamAuthorizations.length
  const mappedImageFailureAcceptedAtMs = realDateNow()
  let mappedImageTextFirstTokenDeadlineScheduled = false
  Date.now = () => mappedImageLongFailureRequestAccepted
    ? mappedImageFailureAcceptedAtMs + 300_000
    : realDateNow()
  globalThis.setTimeout = ((callback: (...args: unknown[]) => void, delay?: number, ...args: unknown[]) => {
    const callStack = new Error().stack ?? ''
    if (delay === 10_000 && /gateway[\\/]upstream[\\/]request/.test(callStack)) {
      mappedImageTextFirstTokenDeadlineScheduled = true
      return realSetTimeout(callback, 1, ...args)
    }
    return realSetTimeout(callback, delay, ...args)
  }) as typeof globalThis.setTimeout
  let imageMappingHttpFailure: Response
  try {
    imageMappingHttpFailure = await fetch(`${baseUrl}/v1/responses?mock_mapped_image_http_failure=1`, {
      method: 'POST',
      headers: { authorization: `Bearer ${imageMappingApiKey.key}`, 'content-type': 'application/json' },
      body: JSON.stringify({ model: mappedImageModel, input: 'mapped image HTTP must use unified failover after text wall', stream: false })
    })
  } finally {
    Date.now = realDateNow
    globalThis.setTimeout = realSetTimeout
  }
  const imageMappingHttpFailureText = await imageMappingHttpFailure.text()
  assert.equal(mappedImageLongFailureRequestAccepted, true, '模型映射升级后的图片首选必须先完整接收请求')
  assert.equal(imageMappingHttpFailure.status, 200, imageMappingHttpFailureText)
  assert.match(imageMappingHttpFailureText, /mapped_image_replay_was_not_blocked/)
  assert.doesNotMatch(imageMappingHttpFailureText, /untrusted mapped image error|mapped_image_unknown_failure/)
  assert.deepEqual(upstreamAuthorizations.slice(imageMappingHttpHitOffset), [
    'Bearer sk-generic-image-mapping-transport-bad',
    'Bearer sk-generic-image-mapping-transport-good'
  ], '账户映射升级后的图片 lane 跨过文本 270 秒后仍必须切到后备账户')
  assert.equal(
    mappedImageTextFirstTokenDeadlineScheduled,
    false,
    '账户模型映射升级为图片 lane 后也不得安排 speed-first 文本首 token 截止计时器'
  )

  const imageMappingHitOffset = upstreamAuthorizations.length
  const imageMappingTransportFailure = await fetch(`${baseUrl}/v1/responses?mock_mapped_image_transport_drop=1`, {
    method: 'POST',
    headers: { authorization: `Bearer ${imageMappingApiKey.key}`, 'content-type': 'application/json' },
    body: JSON.stringify({ model: mappedImageModel, input: 'mapped image transport must use unified failover', stream: false })
  })
  const imageMappingTransportFailureText = await imageMappingTransportFailure.text()
  assert.equal(imageMappingTransportFailure.status, 200, imageMappingTransportFailureText)
  assert.match(imageMappingTransportFailureText, /mapped_image_transport_fallback_success/)
  assert.doesNotMatch(imageMappingTransportFailureText, /upstream_outcome_unknown|upstream_retryable_error/)
  assert.deepEqual(upstreamAuthorizations.slice(imageMappingHitOffset), [
    'Bearer sk-generic-image-mapping-transport-bad',
    'Bearer sk-generic-image-mapping-transport-good'
  ], '账户模型映射到图片模型后，传输失败同样必须调用后备账户')

  const responsesImageToolStreamHitOffset = upstreamAuthorizations.length
  const responsesImageToolStreamFailure = await fetch(`${baseUrl}/v1/responses?mock_responses_image_tool_missing_terminal=1`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${responsesImageToolStreamApiKey.key}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({ turn_id: `turn-image-tool-unknown-${Date.now()}` })
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'generate an image',
      tools: [{ type: 'image_generation' }],
      stream: true
    })
  })
  const responsesImageToolStreamFailureText = await responsesImageToolStreamFailure.text()
  assert.equal(responsesImageToolStreamFailure.status, 200, responsesImageToolStreamFailureText)
  assert.match(responsesImageToolStreamFailureText, /image_tool_replay_was_not_blocked/)
  assert.doesNotMatch(responsesImageToolStreamFailureText, /upstream_retryable_error|upstream_outcome_unknown/)
  assert.deepEqual(upstreamAuthorizations.slice(responsesImageToolStreamHitOffset), [
    'Bearer sk-generic-responses-image-tool-stream-bad',
    'Bearer sk-generic-responses-image-tool-stream-good'
  ], 'Responses 图片工具缺少终止事件时必须按流式预提交失败调用后备账户')

  const responsesImageToolHitOffset = upstreamAuthorizations.length
  const responsesImageToolFailure = await fetch(`${baseUrl}/v1/responses?mock_responses_image_tool_transport_drop=1`, {
    method: 'POST',
    headers: { authorization: `Bearer ${responsesImageToolApiKey.key}`, 'content-type': 'application/json' },
    body: JSON.stringify({
      model: 'gpt-5.5',
      input: 'generate an image',
      tools: [{ type: 'image_generation' }],
      stream: false
    })
  })
  const responsesImageToolFailureText = await responsesImageToolFailure.text()
  assert.equal(responsesImageToolFailure.status, 200, responsesImageToolFailureText)
  assert.match(responsesImageToolFailureText, /image_tool_transport_fallback_success/)
  assert.doesNotMatch(responsesImageToolFailureText, /upstream_outcome_unknown|upstream_retryable_error/)
  assert.deepEqual(upstreamAuthorizations.slice(responsesImageToolHitOffset), [
    'Bearer sk-generic-responses-image-tool-transport-bad',
    'Bearer sk-generic-responses-image-tool-transport-good'
  ], 'Responses 图片工具请求即使使用文本模型，传输失败也必须调用后备账户')

  const streamHitOffset = upstreamAuthorizations.length
  const stream = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: { authorization: `Bearer ${apiKey.key}`, 'content-type': 'application/json', accept: 'text/event-stream' },
    body: JSON.stringify({ model: 'gpt-5.5', input: 'opaque stream event', stream: true })
  })
  const streamText = await stream.text()
  assert.equal(stream.status, 503, `Responses SSE 输出前结构失败应返回稳定网关错误，实际命中 ${JSON.stringify(upstreamAuthorizations.slice(streamHitOffset))}: ${streamText}`)
  assert.deepEqual(
    upstreamAuthorizations.slice(streamHitOffset),
    ['Bearer sk-generic-opaque-bad-b', 'Bearer sk-generic-opaque-good'],
    `Responses SSE 输出前结构失败应有界尝试当前请求候选：${streamText}`
  )
  assert.match(streamText, /upstream_retryable_error/, 'Responses SSE 候选耗尽后应返回网关稳定可重试错误')
  assert.doesNotMatch(streamText, /vendor_invented_stream_error/, '不可信上游错误码不得泄漏给客户端')

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
  assert.equal(upstreamAuthorizations.at(-1), 'Bearer sk-generic-opaque-bad-a', 'SSE/非流式传输冲突不得按响应类型切换上游账户')

  const accountAfter = repositories.findAccountForTest(account.id, access)
  assert.equal(accountAfter?.status, 'active', '通用响应状态和错误类型不得修改账号状态')
  assert.equal(accountAfter?.schedulable, true, '通用响应不得把账号改为不可调度')
  assert.equal(accountAfter?.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, '通用未知错误不得持久化 Key 临时不可用状态')
  const genericRuntimeSnapshot = accountSideEffects.snapshotGatewayAccountRuntimeAvailability()
  for (const genericAccount of [
    account,
    fallbackAccount,
    imageBadAccount,
    imageGoodAccount,
    ...imageTransportScenario.accounts,
    ...imageBodyScenario.accounts,
    ...imagePolicyAccounts,
    ...imageMappingAccounts,
    ...responsesImageToolAccounts,
    ...responsesImageToolStreamAccounts
  ]) {
    assert.equal(genericRuntimeSnapshot[genericAccount.id], undefined, `通用用户请求不得写账户运行态：${genericAccount.name}`)
    const persistedAccount = repositories.findAccountForTest(genericAccount.id, access)
    assert.equal(persistedAccount?.status, 'active', `图片未知结果不得写死账户：${genericAccount.name}`)
    assert.equal(persistedAccount?.schedulable, true, `图片未知结果不得取消账户调度：${genericAccount.name}`)
    assert.equal(
      persistedAccount?.apiKeyRuntime?.temporaryUnavailable ?? 0,
      0,
      `图片未知结果不得把账户 Key 写成临时不可用：${genericAccount.name}`
    )
  }

  const systemDefaultHitOffset = upstreamAuthorizations.length
  const codexJsonError = await fetch(`${baseUrl}/v1/responses?mock_codex_json_error=1`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json',
      'x-codex-turn-metadata': JSON.stringify({ turn_id: `turn-system-default-${Date.now()}` })
    },
    body: JSON.stringify({ model: 'gpt-5.5', input: 'system default must not dispatch', stream: false })
  })
  const codexJsonErrorText = await codexJsonError.text()
  assert.equal(codexJsonError.status, 200)
  assert.match(codexJsonErrorText, /system_default_must_not_retry/, 'system_default 只允许透明渲染，不得替换完整响应')
  assert.equal(upstreamAuthorizations.length - systemDefaultHitOffset, 1, 'system_default 响应检查不得触发服务端切号')

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
  assert.doesNotMatch(codexStreamText, /vendor_invented_stream_error/, '明确 Codex 画像也不得泄漏不可信上游失败码')
  assert.match(codexStreamText, /upstream_retryable_error/, '明确 Codex 画像应接收网关稳定可重试失败事件')
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
