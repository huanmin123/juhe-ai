import assert from 'node:assert/strict'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GLM_PROVIDER_CODE, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import type { AccountModelMapping } from '../../domain/types.js'
import {
  buildOpenAIModelMappedJsonBody,
  resolveOpenAIAccountModelMapping
} from '../../modules/gateway/protocols/openai-v1/model-mapping.js'
import { recordCompletedUpstreamAttempt } from '../../modules/gateway/usage/records.js'
import { requestModel } from '../../modules/gateway/request/metadata.js'
import { OpenAIOAuthCodexAdapterError } from '../../modules/gateway/adapters/gpt-codex/oauth-adapter.js'
import { flushAllUsageRecordQueue } from '../../modules/gateway/usage/record-queue.service.js'
import { createAuditCapture } from '../../modules/gateway/audit/capture.service.js'
import { flushAllAuditLogQueue } from '../../modules/audit-logs/audit-log-queue.service.js'
import { previewAccountImport } from '../../modules/accounts/account-import.service.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { customProviderModelBindings } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'
import { createTraceId, withRequestContext, type RequestContext } from '../../shared/request-context.js'
import { withRequestAuthContext } from '../../modules/auth/request-context.js'
import { handleOpenAIGatewayRequest } from '../../modules/gateway/routes.js'
import { MemoryGatewayRequest, MemoryGatewayResponse } from '../../modules/gateway/testing/memory-gateway-http.js'
import { createGatewayRequestBodyState, type GatewayRawBodyRequest } from '../../modules/gateway/request/body.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-model-mapping-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-model-mapping-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const ownerAccess = { systemAccountId: 'sys_admin', role: 'admin' as const, systemAccountFilterId: 'sys_admin' }
const sourceModel = 'gpt-mapping-regression-source'
const crossProviderSourceModel = 'glm-mapping-regression-source'
const crossProviderUpstreamModel = 'glm-mapping-regression-upstream'
const upstreamModel = 'gpt-mapping-regression-upstream-personal'
const replacementUpstreamModel = 'gpt-mapping-regression-upstream-global'
const unavailableSourceModel = 'gpt-mapping-regression-draft-source'

function responsesMapping(sourceModel: string, upstreamModel: string, enabled = true): AccountModelMapping {
  return {
    sourceModel,
    sourceEndpointFamily: 'responses',
    upstreamModel,
    upstreamEndpointFamily: 'responses',
    enabled
  }
}

try {
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: sourceModel,
    scope: 'global',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GLM_PROVIDER_CODE,
    model: crossProviderSourceModel,
    scope: 'global',
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GLM_PROVIDER_CODE,
    model: crossProviderUpstreamModel,
    scope: 'global',
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 5,
    outputUsdPer1M: 15,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: upstreamModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId,
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 3,
    outputUsdPer1M: 9,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: replacementUpstreamModel,
    scope: 'global',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 4,
    outputUsdPer1M: 10,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: unavailableSourceModel,
    scope: 'global',
    status: 'draft',
    supportedApiProtocols: ['responses'],
    actorSystemAccountId: ownerAccess.systemAccountId
  })

  const group = repositories.createGroup({
    name: '账号模型映射回归分组',
    providerCode: GPT_VENDOR_CODE
  }, ownerAccess)
  const account = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    name: '账号模型映射回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-model-mapping-regression',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    supportedModels: [upstreamModel, replacementUpstreamModel],
    modelMappings: [
      responsesMapping(sourceModel, upstreamModel)
    ],
    groupId: group.id
  }, ownerAccess)

  assert.deepEqual(account.modelMappings, [
    responsesMapping(sourceModel, upstreamModel)
  ], '创建账户应返回模型映射')
  assert.deepEqual(loadStoredMappings(account.id), [
    responsesMapping(sourceModel, upstreamModel)
  ], '创建账户应写入模型映射关系表')

  const runtimeAccount = repositories.listOpenAIAccountsForGroup(group.id, ownerAccess.systemAccountId)
    .find((item) => item.id === account.id)
  assert(runtimeAccount, '网关运行时账号快照应包含映射账户')
  assert.deepEqual(runtimeAccount.modelMappings, [
    responsesMapping(sourceModel, upstreamModel)
  ], '网关运行时账号快照应带上模型映射')

  const originalRequest = jsonRequest({ model: sourceModel, input: 'ping', stream: false, extra: { keep: true } })
  const mapping = resolveOpenAIAccountModelMapping(runtimeAccount, requestModel(originalRequest), 'responses')
  assert.deepEqual(mapping, {
    sourceModel,
    sourceEndpointFamily: 'responses',
    upstreamModel,
    upstreamEndpointFamily: 'responses'
  }, '选中账号后应按下游模型和协议命中账号映射')
  const mappedBody = JSON.parse((await buildOpenAIModelMappedJsonBody(originalRequest, upstreamModel)).toString('utf8')) as Record<string, unknown>
  assert.equal(mappedBody.model, upstreamModel, '上游请求体顶层 model 应改写为上游模型')
  assert.deepEqual(mappedBody.extra, { keep: true }, '模型映射不应丢弃未知字段')
  assert.equal(requestModel(originalRequest), sourceModel, 'requestModel 仍应保持下游请求模型')

  await assertInvalidMappingBodyRejected()
  await assertInvalidMappingBodyDoesNotSwitchAccount(group.id)
  await assertUsageRecordFields(runtimeAccount, group.id)
  await assertAuditLogFields(runtimeAccount, group.id)

  const updated = repositories.updateAccount(account.id, {
    modelMappings: [
      responsesMapping(sourceModel, replacementUpstreamModel, false)
    ]
  }, ownerAccess)
  assert.deepEqual(updated?.modelMappings, [
    responsesMapping(sourceModel, replacementUpstreamModel, false)
  ], '更新账户应替换模型映射')
  assert.deepEqual(loadStoredMappings(account.id), [
    responsesMapping(sourceModel, replacementUpstreamModel, false)
  ], '更新账户应替换模型映射关系表')

  const renamed = repositories.updateAccount(account.id, { name: '账号模型映射回归账户-改名' }, ownerAccess)
  assert.deepEqual(renamed?.modelMappings, [
    responsesMapping(sourceModel, replacementUpstreamModel, false)
  ], '未提交 modelMappings 时不应清空已有映射')

  assert.throws(() => repositories.updateAccount(account.id, {
    modelMappings: [
      responsesMapping(sourceModel, crossProviderUpstreamModel)
    ]
  }, ownerAccess), /映射上游模型不在当前账号可用模型池中/, '定制供应商映射上游模型必须来自当前供应商模型池')
  const crossProviderUpstreamBindings = customProviderModelBindings({
    providerCode: GLM_PROVIDER_CODE,
    model: crossProviderUpstreamModel,
    scope: 'global'
  })
  assert.equal(crossProviderUpstreamBindings.mappingUpstreamAccountCount, 0, '被供应商边界拒绝的上游映射不应计入绑定统计')

  const crossProviderSourceUpdated = repositories.updateAccount(account.id, {
    modelMappings: [
      responsesMapping(crossProviderSourceModel, replacementUpstreamModel)
    ]
  }, ownerAccess)
  assert.deepEqual(crossProviderSourceUpdated?.modelMappings, [
    responsesMapping(crossProviderSourceModel, replacementUpstreamModel)
  ], '映射下游模型允许来自 OpenAI 协议客户端模型池')
  const crossProviderSourceBindings = customProviderModelBindings({
    providerCode: GLM_PROVIDER_CODE,
    model: crossProviderSourceModel,
    scope: 'global'
  })
  assert.equal(crossProviderSourceBindings.mappingSourceAccountCount, 1, '跨供应商下游映射应计入 source 绑定统计')

  assert.throws(() => {
    repositories.updateAccount(account.id, {
      modelMappings: [
        responsesMapping(unavailableSourceModel, replacementUpstreamModel)
      ]
    }, ownerAccess)
  }, /映射下游模型不在 OpenAI 协议客户端模型池中/, '草稿模型不能作为下游映射源')

  assert.throws(() => {
    repositories.updateAccount(account.id, {
      modelMappings: [
        responsesMapping(sourceModel, 'gpt-mapping-regression-missing')
      ]
    }, ownerAccess)
  }, /映射上游模型不在当前账号可用模型池中/, '映射上游模型必须存在于当前账号可用模型池')
  assertImportPreviewRejectsInvalidMapping(group.id)

  console.log('account model mapping regression passed')
} finally {
  try {
    flushAllUsageRecordQueue()
    flushAllAuditLogQueue()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertImportPreviewRejectsInvalidMapping(groupId: string): void {
  const result = previewAccountImport({
    type: 'juhe-ai-account-import',
    version: 1,
    accounts: [
      {
        name: '账号模型映射非法导入预览',
        providerCode: GPT_VENDOR_CODE,
        type: 'api_key',
        status: 'active',
        groupId,
        credentials: {
          api_key: 'sk-account-model-mapping-import-preview',
          base_url: 'https://api.openai.com/v1'
        },
        modelMappings: [
          responsesMapping(unavailableSourceModel, replacementUpstreamModel)
        ]
      }
    ]
  }, {}, ownerAccess)
  assert.equal(result.canImport, false, '非法模型映射导入预览不应允许确认导入')
  assert.equal(result.accounts[0]?.action, 'failed', '非法模型映射导入预览应标记账户失败')
  assert(result.accounts[0]?.messages.some((message) => message.includes('映射下游模型不在 OpenAI 协议客户端模型池中')), '导入预览应在预览阶段暴露模型映射目录错误')
}

function loadStoredMappings(accountId: string): AccountModelMapping[] {
  return (databaseModule.getBusinessDatabase()
    .prepare('SELECT source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled FROM account_model_mappings WHERE account_id = ? ORDER BY source_model ASC, source_endpoint_family ASC')
    .all(accountId) as unknown as Array<{ source_model: string; source_endpoint_family: 'chat_completions' | 'responses'; upstream_model: string; upstream_endpoint_family: 'chat_completions' | 'responses'; enabled: number }>)
    .map((row) => ({
      sourceModel: row.source_model,
      sourceEndpointFamily: row.source_endpoint_family,
      upstreamModel: row.upstream_model,
      upstreamEndpointFamily: row.upstream_endpoint_family,
      enabled: row.enabled === 1
    }))
}

function jsonRequest(body: Record<string, unknown>): Request {
  const rawBody = Buffer.from(JSON.stringify(body), 'utf8')
  const headers: Record<string, string> = { 'content-type': 'application/json' }
  return {
    method: 'POST',
    path: '/v1/responses',
    originalUrl: '/v1/responses',
    headers,
    header(name: string) {
      const value = headers[name.toLowerCase()]
      return Array.isArray(value) ? value.join(', ') : value
    },
    body,
    rawBody,
    gatewayRequestBody: createGatewayRequestBodyState({
      rawBody,
      contentType: 'application/json',
      jsonParseStatus: 'parsed',
      parsedBody: body
    })
  } as unknown as Request
}

async function assertInvalidMappingBodyRejected(): Promise<void> {
  const rawBody = Buffer.from('{ invalid json', 'utf8')
  const req = {
    method: 'POST',
    path: '/v1/responses',
    originalUrl: '/v1/responses',
    headers: { 'content-type': 'application/json' },
    rawBody,
    gatewayRequestBody: createGatewayRequestBodyState({
      rawBody,
      contentType: 'application/json',
      jsonParseStatus: 'invalid_json',
      model: sourceModel
    })
  } as unknown as Request & GatewayRawBodyRequest

  await assert.rejects(
    () => buildOpenAIModelMappedJsonBody(req, upstreamModel),
    (error: unknown) => error instanceof OpenAIOAuthCodexAdapterError
      && error.statusCode === 400
      && error.code === 'account_model_mapping_request_invalid'
      && error.accountScoped === false,
    '非法 JSON 命中账号模型映射时应保持请求级错误，不能触发切号'
  )
}

async function assertInvalidMappingBodyDoesNotSwitchAccount(groupId: string): Promise<void> {
  let upstreamHitCount = 0
  const server = http.createServer((req, res) => {
    upstreamHitCount += 1
    req.resume()
    res.writeHead(200, { 'content-type': 'application/json' })
    res.end(JSON.stringify({
      id: 'chatcmpl_account_model_mapping_invalid_body',
      object: 'chat.completion',
      choices: [
        { index: 0, message: { role: 'assistant', content: 'SHOULD_NOT_HIT' }, finish_reason: 'stop' }
      ],
      usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
    }))
  })
  server.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  assert(address && typeof address !== 'string', '模型映射非法请求体 mock 上游地址应可用')
  try {
    const fallback = repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      name: '账号模型映射非法请求不应切到的后备账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-mapping-invalid-body-fallback',
        base_url: `http://127.0.0.1:${address.port}/v1`
      },
      status: 'active',
      supportedModels: [replacementUpstreamModel],
      groupId
    }, ownerAccess)
    assert(repositories.setAccountGroup(fallback.id, groupId, ownerAccess), '后备账户应能绑定到模型映射回归分组')
    const runtimeAccounts = repositories.listOpenAIAccountsForGroup(groupId, ownerAccess.systemAccountId)
      .filter((item) => item.id === fallback.id || item.modelMappings?.some((mapping) => mapping.sourceModel === sourceModel))
    const mappedAccount = runtimeAccounts.find((item) => item.modelMappings?.some((mapping) => mapping.sourceModel === sourceModel))
    const fallbackAccount = runtimeAccounts.find((item) => item.id === fallback.id)
    assert(mappedAccount, '运行时账号快照应包含带模型映射的账户')
    assert(fallbackAccount, '运行时账号快照应包含后备账户')

    const traceId = createTraceId()
    const startedAt = Date.now()
    const request = invalidJsonGatewayRequest(sourceModel)
    const response = new MemoryGatewayResponse(startedAt)
    const context: RequestContext = {
      traceId,
      startedAt,
      method: request.method,
      path: request.path,
      originalUrl: request.originalUrl,
      clientIp: request.ip,
      systemAccountId: ownerAccess.systemAccountId,
      groupId,
      logger
    }
    await withRequestContext(context, () => withRequestAuthContext(undefined, () => handleOpenAIGatewayRequest(
      request,
      response.asResponse(),
      {
        identity: {
          systemAccountId: ownerAccess.systemAccountId,
          groupId
        },
        candidateAccounts: [mappedAccount, fallbackAccount],
        disableSessionAffinity: true,
        disableAccountStateMutation: true,
        exposeUpstreamDiagnostics: true
      }
    )))
    assert.equal(response.statusCode, 400, '非法 JSON 命中模型映射时应直接返回请求级 400')
    assert.match(response.bodyText(), /invalid_request_error|合法 JSON|有效的 JSON 对象/, '响应体应保留请求级非法 JSON 错误语义')
    assert.equal(upstreamHitCount, 0, '非法 JSON 不应切到后备账户或发起任何上游请求')
  } finally {
    await closeServer(server)
  }
}

function invalidJsonGatewayRequest(model: string): Request {
  const rawBody = Buffer.from('{ invalid json', 'utf8')
  const request = new MemoryGatewayRequest({
    method: 'POST',
    originalUrl: '/v1/responses',
    path: '/v1/responses',
    headers: {
      accept: 'application/json',
      'content-type': 'application/json',
      'content-length': String(rawBody.length)
    },
    rawBody,
    ip: '127.0.0.1'
  } as ConstructorParameters<typeof MemoryGatewayRequest>[0]).asRequest() as Request & GatewayRawBodyRequest
  request.gatewayRequestBody = createGatewayRequestBodyState({
    rawBody,
    contentType: 'application/json',
    jsonParseStatus: 'invalid_json',
    model,
    stream: false
  })
  return request
}

async function onceListening(server: http.Server): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

async function closeServer(server: http.Server): Promise<void> {
  if (!server.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
    server.closeIdleConnections?.()
  })
}

async function assertUsageRecordFields(
  account: NonNullable<ReturnType<typeof repositories.listOpenAIAccountsForGroup>[number]>,
  groupId: string
): Promise<void> {
  const traceId = 'trace-account-model-mapping-regression'
  recordCompletedUpstreamAttempt(jsonRequest({ model: sourceModel, input: 'usage', stream: false }), {
    traceId,
    trafficSource: 'gateway',
    systemAccountId: 'sys_mapping_grantee',
    groupId,
    account,
    endpoint: 'POST /v1/responses',
    statusCode: 200,
    success: true,
    stream: false,
    startedAt: Date.now(),
    usage: {
      inputTokens: 1_000_000,
      outputTokens: 1_000_000
    }
  })
  flushAllUsageRecordQueue()
  const record = repositories.listUsageRecords(undefined, { page: 1, pageSize: 20 })
    .items
    .find((item) => item.traceId === traceId)
  assert(record, '模型映射调用应写入使用记录')
  assert.equal(record.model, sourceModel, '使用记录 model 应保留下游模型')
  assert.equal(record.upstreamModel, upstreamModel, '使用记录 upstreamModel 应记录实际上游模型')
  assert.equal(record.pricingModel, upstreamModel, '使用记录 pricingModel 应记录实际计价模型')
  assert.equal(record.modelMappingApplied, true, '使用记录应标记命中模型映射')
  assert.equal(record.modelMappingSource, 'account', '使用记录映射来源应固定为 account')
  assert.equal(record.costUsd, 12, '授权调用应按资源账号所有者个人映射目标模型计价')
}

async function assertAuditLogFields(
  account: NonNullable<ReturnType<typeof repositories.listOpenAIAccountsForGroup>[number]>,
  groupId: string
): Promise<void> {
  const traceId = 'trace-account-model-mapping-audit-regression'
  const req = jsonRequest({ model: sourceModel, input: 'audit', stream: false })
  const startedAtMs = Date.now()
  const auditCapture = createAuditCapture({
    req,
    traceId,
    startedAtMs,
    clientIp: '127.0.0.1',
    trafficSource: 'gateway'
  })
  auditCapture.bindContext({
    systemAccountId: 'sys_mapping_grantee',
    groupId,
    accountId: account.id,
    providerCode: account.providerCode,
    trafficSource: 'gateway'
  })
  const headers = new Headers({ 'content-type': 'application/json' })
  const upstreamBody = await buildOpenAIModelMappedJsonBody(req, upstreamModel)
  const attemptId = auditCapture.startAttempt({
    account,
    attemptIndex: 1,
    upstreamUrl: 'https://api.openai.com/v1/responses',
    method: 'POST',
    headers,
    body: upstreamBody
  })
  auditCapture.completeAttempt(attemptId, {
    statusCode: 200,
    responseHeaders: new Headers({ 'content-type': 'application/json' }),
    responseBody: JSON.stringify({ id: 'resp-audit-model-mapping-regression' }),
    success: true
  })
  auditCapture.finalize({
    outcome: 'success',
    success: true,
    statusCode: 200,
    responseHeaders: { 'content-type': 'application/json' },
    responseBody: JSON.stringify({ ok: true }),
    accountId: account.id
  })
  flushAllAuditLogQueue()

  const record = repositories.listAuditLogs({ model: sourceModel, page: 1, pageSize: 20 })
    .items
    .find((item) => item.traceId === traceId)
  assert(record, '模型映射调用应写入审计日志')
  assert.equal(record.model, sourceModel, '审计日志 model 应保留下游模型')
  assert.equal(record.upstreamModel, upstreamModel, '审计日志 upstreamModel 应记录实际上游模型')
  assert.equal(record.pricingModel, upstreamModel, '审计日志 pricingModel 应记录实际计价模型')
  assert.equal(record.modelMappingApplied, true, '审计日志应标记命中模型映射')
  assert.equal(record.modelMappingSource, 'account', '审计日志映射来源应固定为 account')

  const detail = repositories.getAuditLogDetail(record.id)
  assert.equal(detail?.upstreamModel, upstreamModel, '审计详情应返回实际上游模型')
  const upstreamRequestPayload = detail?.payloads.find((payload) => payload.partType === 'upstream_request')
  assert(upstreamRequestPayload, '审计详情应保留上游请求 payload 摘要')
}
