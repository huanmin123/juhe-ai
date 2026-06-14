import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import type { AccountModelMapping } from '../../domain/types.js'
import {
  buildOpenAIModelMappedJsonBody,
  resolveOpenAIAccountModelMapping
} from '../../modules/gateway/protocols/openai-v1/model-mapping.js'
import { recordCompletedUpstreamAttempt } from '../../modules/gateway/usage/records.js'
import { requestModel } from '../../modules/gateway/request/metadata.js'
import { OpenAIOAuthCodexAdapterError } from '../../modules/gateway/adapters/gpt-codex/oauth-adapter.js'
import { flushAllUsageRecordQueue } from '../../modules/gateway/usage/record-queue.service.js'
import { previewAccountImport } from '../../modules/accounts/account-import.service.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'
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
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const ownerAccess = { systemAccountId: 'sys_admin', role: 'admin' as const, systemAccountFilterId: 'sys_admin' }
const sourceModel = 'gpt-mapping-regression-source'
const upstreamModel = 'gpt-mapping-regression-upstream-personal'
const replacementUpstreamModel = 'gpt-mapping-regression-upstream-global'

try {
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: sourceModel,
    scope: 'global',
    visibility: 'public',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: upstreamModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId,
    visibility: 'mapping_target_only',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 3,
    outputUsdPer1M: 9,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: replacementUpstreamModel,
    scope: 'global',
    visibility: 'mapping_target_only',
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 4,
    outputUsdPer1M: 10,
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
    supportedModels: [sourceModel],
    modelMappings: [
      { sourceModel, upstreamModel, enabled: true }
    ],
    groupId: group.id
  }, ownerAccess)

  assert.deepEqual(account.modelMappings, [
    { sourceModel, upstreamModel, enabled: true }
  ], '创建账户应返回模型映射')
  assert.deepEqual(loadStoredMappings(account.id), [
    { sourceModel, upstreamModel, enabled: true }
  ], '创建账户应写入模型映射关系表')

  const runtimeAccount = repositories.listOpenAIAccountsForGroup(group.id, ownerAccess.systemAccountId)
    .find((item) => item.id === account.id)
  assert(runtimeAccount, '网关运行时账号快照应包含映射账户')
  assert.deepEqual(runtimeAccount.modelMappings, [
    { sourceModel, upstreamModel, enabled: true }
  ], '网关运行时账号快照应带上模型映射')

  const originalRequest = jsonRequest({ model: sourceModel, input: 'ping', stream: false, extra: { keep: true } })
  const mapping = resolveOpenAIAccountModelMapping(runtimeAccount, requestModel(originalRequest))
  assert.deepEqual(mapping, { sourceModel, upstreamModel }, '选中账号后应按下游模型命中账号映射')
  const mappedBody = JSON.parse((await buildOpenAIModelMappedJsonBody(originalRequest, upstreamModel)).toString('utf8')) as Record<string, unknown>
  assert.equal(mappedBody.model, upstreamModel, '上游请求体顶层 model 应改写为上游模型')
  assert.deepEqual(mappedBody.extra, { keep: true }, '模型映射不应丢弃未知字段')
  assert.equal(requestModel(originalRequest), sourceModel, 'requestModel 仍应保持下游请求模型')

  await assertInvalidMappingBodyRejected()
  await assertUsageRecordFields(runtimeAccount, group.id)

  const updated = repositories.updateAccount(account.id, {
    modelMappings: [
      { sourceModel, upstreamModel: replacementUpstreamModel, enabled: false }
    ]
  }, ownerAccess)
  assert.deepEqual(updated?.modelMappings, [
    { sourceModel, upstreamModel: replacementUpstreamModel, enabled: false }
  ], '更新账户应替换模型映射')
  assert.deepEqual(loadStoredMappings(account.id), [
    { sourceModel, upstreamModel: replacementUpstreamModel, enabled: false }
  ], '更新账户应替换模型映射关系表')

  const renamed = repositories.updateAccount(account.id, { name: '账号模型映射回归账户-改名' }, ownerAccess)
  assert.deepEqual(renamed?.modelMappings, [
    { sourceModel, upstreamModel: replacementUpstreamModel, enabled: false }
  ], '未提交 modelMappings 时不应清空已有映射')

  assert.throws(() => {
    repositories.updateAccount(account.id, {
      modelMappings: [
        { sourceModel: upstreamModel, upstreamModel: replacementUpstreamModel, enabled: true }
      ]
    }, ownerAccess)
  }, /映射下游模型不在可请求模型目录中/, 'mapping_target_only 模型不能作为下游映射源')

  assert.throws(() => {
    repositories.updateAccount(account.id, {
      modelMappings: [
        { sourceModel, upstreamModel: 'gpt-mapping-regression-missing', enabled: true }
      ]
    }, ownerAccess)
  }, /映射上游模型不在可用模型目录中/, '映射上游模型必须存在于可见模型目录')
  assertImportPreviewRejectsInvalidMapping(group.id)

  console.log('account model mapping regression passed')
} finally {
  try {
    flushAllUsageRecordQueue()
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
          { sourceModel: upstreamModel, upstreamModel: replacementUpstreamModel, enabled: true }
        ]
      }
    ]
  }, {}, ownerAccess)
  assert.equal(result.canImport, false, '非法模型映射导入预览不应允许确认导入')
  assert.equal(result.accounts[0]?.action, 'failed', '非法模型映射导入预览应标记账户失败')
  assert(result.accounts[0]?.messages.some((message) => message.includes('映射下游模型不在可请求模型目录中')), '导入预览应在预览阶段暴露模型映射目录错误')
}

function loadStoredMappings(accountId: string): AccountModelMapping[] {
  return (databaseModule.getBusinessDatabase()
    .prepare('SELECT source_model, upstream_model, enabled FROM account_model_mappings WHERE account_id = ? ORDER BY source_model ASC')
    .all(accountId) as unknown as Array<{ source_model: string; upstream_model: string; enabled: number }>)
    .map((row) => ({
      sourceModel: row.source_model,
      upstreamModel: row.upstream_model,
      enabled: row.enabled === 1
    }))
}

function jsonRequest(body: Record<string, unknown>): Request {
  const rawBody = Buffer.from(JSON.stringify(body), 'utf8')
  return {
    method: 'POST',
    path: '/v1/responses',
    originalUrl: '/v1/responses',
    headers: { 'content-type': 'application/json' },
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
      && error.code === 'account_model_mapping_request_invalid',
    '非法 JSON 命中映射时应返回本地请求错误'
  )
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
