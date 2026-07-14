import assert from 'node:assert/strict'
import http from 'node:http'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROVIDER_CODE,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_PROVIDER_CODE,
  GLM_PROVIDER_CODE,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
  HYBRID_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import type { AccountModelMapping } from '../../domain/types.js'
import {
  buildOpenAIModelMappedJsonBody,
  gatewayRequestEndpointFamily,
  openAIModelMappedUpstreamPathAndQuery,
  resolveOpenAIRequestModelMapping,
  setGatewayModelMappingSourceEndpointFamilyOverride,
  resolveOpenAIAccountModelMapping
} from '../../modules/gateway/protocols/openai-v1/model-mapping.js'
import { recordCompletedUpstreamAttempt } from '../../modules/gateway/usage/records.js'
import { requestModel } from '../../modules/gateway/request/metadata.js'
import { OpenAIOAuthCodexAdapterError } from '../../modules/gateway/adapters/gpt-codex/oauth-adapter.js'
import { flushAllUsageRecordQueue } from '../../modules/gateway/usage/record-queue.service.js'
import { createAuditCapture } from '../../modules/gateway/audit/capture.service.js'
import { flushAllAuditLogQueue } from '../../modules/audit-logs/audit-log-queue.service.js'
import { previewAccountImport } from '../../modules/accounts/account-import.service.js'
import { prepareAccountDraftTestSnapshot } from '../../modules/accounts/account-draft-test.service.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { customProviderModelBindings } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'
import { createTraceId, withRequestContext, type RequestContext } from '../../shared/request-context.js'
import { withRequestAuthContext } from '../../modules/auth/request-context.js'
import { handleOpenAIGatewayRequest } from '../../modules/gateway/routes.js'
import { MemoryGatewayRequest, MemoryGatewayResponse } from '../../modules/gateway/testing/memory-gateway-http.js'
import { createGatewayRequestBodyState, type GatewayRawBodyRequest } from '../../modules/gateway/request/body.js'
import {
  accountModelMappingProtocolRules,
  assertAccountModelMappingProtocolAllowed
} from '../../storage/account-model-mapping-protocol-matrix.js'

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
const caseSourceModel = 'gpt-mapping-regression-case-source'
const caseSourceModelUpper = 'GPT-mapping-regression-case-source'
const anthropicMessagesSourceModel = 'claude-mapping-regression-source'
const geminiGenerateContentSourceModel = 'gemini-3.5-flash'
const geminiNativeUpstreamModel = 'gemini-mapping-regression-upstream'
const anthropicMessagesUpstreamModel = 'claude-haiku-4-5'
const chatCompletionsUpstreamModel = 'gpt-mapping-regression-chat-upstream'
const replacementUpstreamModel = 'gpt-mapping-regression-upstream-global'
const unavailableSourceModel = 'gpt-mapping-regression-draft-source'
const unpricedUpstreamModel = 'gpt-mapping-regression-unpriced-upstream'

function createRegressionAccount(
  input: Record<string, unknown>,
  access = ownerAccess
) {
  const supportedModels = Array.isArray(input.supportedModels)
    ? input.supportedModels
    : undefined
  const normalized = supportedModels && supportedModels.length > 0
    ? {
        ...input,
        healthCheckModel: input.healthCheckModel ?? supportedModels[0],
        healthCheckEndpointFamily: input.healthCheckEndpointFamily ?? 'chat_completions' as const
      }
    : input
  const created = repositories.createAccount(
    normalized as Parameters<typeof repositories.createAccount>[0],
    access
  )
  repositories.recordAccountHealthCheckSuccess(created.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  return repositories.findAccountSummary(created.id, access) ?? created
}

function responsesMapping(sourceModel: string, upstreamModel: string, enabled = true): AccountModelMapping {
  return {
    sourceModel,
    sourceEndpointFamily: 'responses',
    upstreamModel,
    upstreamEndpointFamily: 'responses',
    enabled
  }
}

function profileIdForProvider(providerCode: string): string {
  if (providerCode === ANTHROPIC_PROVIDER_CODE) return ANTHROPIC_ANTHROPIC_V1_PROFILE_ID
  if (providerCode === GEMINI_PROVIDER_CODE) return GEMINI_NATIVE_V1BETA_PROFILE_ID
  if (providerCode === GLM_PROVIDER_CODE) return GLM_GENERAL_OPENAI_V1_PROFILE_ID
  if (providerCode === HYBRID_PROVIDER_CODE) return HYBRID_OPENAI_CHAT_V1_PROFILE_ID
  return GPT_OPENAI_V1_PROFILE_ID
}

function responsesToChatMapping(sourceModel: string, upstreamModel: string, enabled = true): AccountModelMapping {
  return {
    sourceModel,
    sourceEndpointFamily: 'responses',
    upstreamModel,
    upstreamEndpointFamily: 'chat_completions',
    enabled
  }
}

function chatToResponsesMapping(sourceModel: string, upstreamModel: string, enabled = true): AccountModelMapping {
  return {
    sourceModel,
    sourceEndpointFamily: 'chat_completions',
    upstreamModel,
    upstreamEndpointFamily: 'responses',
    enabled
  }
}

function messagesToChatMapping(sourceModel: string, upstreamModel: string, enabled = true): AccountModelMapping {
  return {
    sourceModel,
    sourceEndpointFamily: 'messages',
    upstreamModel,
    upstreamEndpointFamily: 'chat_completions',
    enabled
  }
}

function messagesToResponsesMapping(sourceModel: string, upstreamModel: string, enabled = true): AccountModelMapping {
  return {
    sourceModel,
    sourceEndpointFamily: 'messages',
    upstreamModel,
    upstreamEndpointFamily: 'responses',
    enabled
  }
}

function messagesToMessagesMapping(sourceModel: string, upstreamModel: string, enabled = true): AccountModelMapping {
  return {
    sourceModel,
    sourceEndpointFamily: 'messages',
    upstreamModel,
    upstreamEndpointFamily: 'messages',
    enabled
  }
}

function toGeminiGenerateContentMapping(
  sourceModel: string,
  upstreamModel: string,
  sourceEndpointFamily: 'chat_completions' | 'responses' | 'messages',
  enabled = true
): AccountModelMapping {
  return {
    sourceModel,
    sourceEndpointFamily,
    upstreamModel,
    upstreamEndpointFamily: 'generate_content',
    enabled
  }
}

function geminiGenerateContentToMessagesMapping(
  sourceModel: string,
  upstreamModel: string,
  enabled = true,
  sourceEndpointFamily: 'generate_content' | 'stream_generate_content' = 'generate_content'
): AccountModelMapping {
  return {
    sourceModel,
    sourceEndpointFamily,
    upstreamModel,
    upstreamEndpointFamily: 'messages',
    enabled
  }
}

function assertUnchangedModelConfigPatchSkipsProviderCatalogValidation(): void {
  const source = readFileSync(new URL('../../storage/repositories.ts', import.meta.url), 'utf8')
  assert.match(source, /normalizeSupportedModelsIfUnchanged\(input\.supportedModels, current\.supportedModels\)/, '账户 PATCH 相同 supportedModels 应先本地比较，避免重复查询供应商模型目录')
  assert.match(source, /normalizeModelMappingsIfUnchanged\(input\.modelMappings, current\.modelMappings\)/, '账户 PATCH 相同 modelMappings 应先本地比较，避免重复查询跨协议模型池')
  assert.match(source, /unchangedSupportedModelsInput \?\? await normalizeAccountSupportedModelsForProviderAsync/, 'PG 账号 PATCH 相同 supportedModels 不应继续走 provider catalog async 校验')
  assert.match(source, /unchangedModelMappingsInput \?\? await normalizeAccountModelMappingsForProviderAsync/, 'PG 账号 PATCH 相同 modelMappings 不应继续走 provider catalog async 校验')
  assert.match(
    source,
    /const nextModelMappings = hasModelMappingsInput \|\| endpointModesChanged[\s\S]{0,700}normalizeAccountModelMappingsForProviderAsync/,
    'PG async 更新仅修改 endpoint modes 时也必须进入模型映射能力重验'
  )
}

try {
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: sourceModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId,
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GLM_PROVIDER_CODE,
    model: crossProviderSourceModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId,
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GLM_PROVIDER_CODE,
    model: crossProviderUpstreamModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId,
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
    supportedApiProtocols: ['chat_completions', 'responses'],
    inputUsdPer1M: 3,
    outputUsdPer1M: 9,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  const anthropicMessagesModel = saveCustomProviderModel({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    model: anthropicMessagesSourceModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId,
    supportedApiProtocols: ['messages', 'message_token_counting'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  assert.deepEqual(anthropicMessagesModel.supportedApiProtocols, ['messages', 'message_token_counting'], 'Anthropic 自定义模型协议白名单应保留 messages 与 message_token_counting')
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: chatCompletionsUpstreamModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId,
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    model: sourceModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId,
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    model: upstreamModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId,
    supportedApiProtocols: ['chat_completions', 'responses'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    model: chatCompletionsUpstreamModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId,
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GEMINI_PROVIDER_CODE,
    model: geminiNativeUpstreamModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId,
    supportedApiProtocols: ['generate_content', 'stream_generate_content'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: replacementUpstreamModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId,
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 4,
    outputUsdPer1M: 10,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: unavailableSourceModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId,
    status: 'draft',
    supportedApiProtocols: ['responses'],
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: caseSourceModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId,
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: caseSourceModelUpper,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId,
    supportedApiProtocols: ['responses'],
    inputUsdPer1M: 3,
    outputUsdPer1M: 4,
    actorSystemAccountId: ownerAccess.systemAccountId
  })

  assertProtocolMatrixHelper()
  assertUnchangedModelConfigPatchSkipsProviderCatalogValidation()

  const group = repositories.createGroup({
    name: '账号模型映射回归分组',
    providerCode: GPT_VENDOR_CODE,
  }, ownerAccess)
  const account = createRegressionAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
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

  const caseAccount = createRegressionAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账号模型映射大小写回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-model-mapping-case-regression',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    supportedModels: [upstreamModel, replacementUpstreamModel],
    modelMappings: [
      responsesMapping(caseSourceModel, upstreamModel),
      responsesMapping(caseSourceModelUpper, replacementUpstreamModel)
    ],
    groupId: group.id
  }, ownerAccess)
  assert.equal(caseAccount.modelMappings?.length, 2, '仅大小写不同的下游模型应允许分别配置映射')
  assert.deepEqual(
    caseAccount.modelMappings?.find((mapping) => mapping.sourceModel === caseSourceModel),
    responsesMapping(caseSourceModel, upstreamModel),
    '小写下游模型映射应独立保留'
  )
  assert.deepEqual(
    caseAccount.modelMappings?.find((mapping) => mapping.sourceModel === caseSourceModelUpper),
    responsesMapping(caseSourceModelUpper, replacementUpstreamModel),
    '大写下游模型映射应独立保留'
  )
  assert.equal(customProviderModelBindings({
    providerCode: GPT_VENDOR_CODE,
    model: caseSourceModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId
  }).mappingSourceAccountCount, 1, '小写模型绑定统计不应合并大写模型映射')
  assert.equal(customProviderModelBindings({
    providerCode: GPT_VENDOR_CODE,
    model: caseSourceModelUpper,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId
  }).mappingSourceAccountCount, 1, '大写模型绑定统计不应合并小写模型映射')

  assertNativeResponsesUpstreamRequiresEndpointModes()
  await assertEndpointModeUpdateValidatesEnabledMappings(group.id)
  assertRuntimeIgnoresUnsupportedChatToResponsesMapping()
  assertRuntimeIgnoresPersistentCrossProtocolMappings()
  await assertCompactSyntheticChatUsesResponsesModelMapping()
  assertCrossProtocolAccountMappingsRejected(group.id)
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
  }, ownerAccess), /账号模型别名目标模型不在当前供应商模型目录中/, '定制供应商映射上游模型必须来自当前供应商模型池')
  const crossProviderUpstreamBindings = customProviderModelBindings({
    providerCode: GLM_PROVIDER_CODE,
    model: crossProviderUpstreamModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId
  })
  assert.equal(crossProviderUpstreamBindings.mappingUpstreamAccountCount, 0, '被供应商边界拒绝的上游映射不应计入绑定统计')

  assert.throws(() => repositories.updateAccount(account.id, {
    modelMappings: [
      responsesMapping(crossProviderSourceModel, replacementUpstreamModel)
    ]
  }, ownerAccess), /账号模型别名来源模型不在当前供应商的对应协议模型目录中/, '账号模型别名来源模型必须来自当前供应商协议模型池')
  const crossProviderSourceBindings = customProviderModelBindings({
    providerCode: GLM_PROVIDER_CODE,
    model: crossProviderSourceModel,
    scope: 'personal',
    systemAccountId: ownerAccess.systemAccountId
  })
  assert.equal(crossProviderSourceBindings.mappingSourceAccountCount, 0, '被供应商边界拒绝的下游映射不应计入 source 绑定统计')

  assert.throws(() => {
    repositories.updateAccount(account.id, {
      modelMappings: [
        responsesMapping(unavailableSourceModel, replacementUpstreamModel)
      ]
    }, ownerAccess)
  }, /账号模型别名来源模型不在当前供应商的对应协议模型目录中/, '草稿模型不能作为下游映射源')

  assert.throws(() => {
    repositories.updateAccount(account.id, {
      modelMappings: [
        responsesMapping(sourceModel, 'gpt-mapping-regression-missing')
      ]
    }, ownerAccess)
  }, /账号模型别名目标模型不在当前供应商模型目录中/, '映射上游模型必须存在于当前账号可用模型池')
  assert.throws(() => {
    repositories.updateAccount(account.id, {
      modelMappings: [
        responsesMapping(chatCompletionsUpstreamModel, replacementUpstreamModel)
      ]
    }, ownerAccess)
  }, /账号模型别名来源模型不在当前供应商的对应协议模型目录中/, 'Responses 来源模型必须声明支持 Responses 协议，不能选择 Chat-only 模型')
  assertHybridProtocolModelPools()
  assertImportPreviewRejectsInvalidMapping(group.id)
  assertImportPreviewRejectsNonNativeResponsesMapping(group.id)
  assertImportPreviewRejectsUnsupportedMessagesMapping(group.id)
  assertImportAndDraftValidateTargetCapabilities(group.id)

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

function assertNativeResponsesUpstreamRequiresEndpointModes(): void {
  const group = repositories.createGroup({
    name: '账号模型映射 Responses 原生能力约束分组',
    providerCode: GPT_VENDOR_CODE,
  }, ownerAccess)
  const openAICompatibleGroup = repositories.createGroup({
    name: '账号模型映射 OpenAI-compatible bridge 分组',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
  }, ownerAccess)
  assert.throws(() => {
    createRegressionAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: 'Chat-only 账号不能配置 Responses 上游',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-mapping-chat-only-responses-upstream',
        base_url: 'https://api.openai.com/v1',
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      supportedModels: [upstreamModel],
      modelMappings: [
        responsesMapping(sourceModel, upstreamModel)
      ],
      groupId: group.id
    }, ownerAccess)
  }, /上游协议 Responses 只能用于账号真实支持 Responses API 的原生上游/, 'Chat-only 账号不能把映射右侧配置成 Responses')

  assert.throws(() => {
    createRegressionAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: 'Chat-only 账号不能配置 Chat 转 Responses',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-mapping-chat-only-chat-to-responses',
        base_url: 'https://api.openai.com/v1',
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      supportedModels: [upstreamModel],
      modelMappings: [
        chatToResponsesMapping(sourceModel, upstreamModel)
      ],
      groupId: group.id
    }, ownerAccess)
  }, /账号模型别名只支持同协议映射/, 'Chat-only 账号不能配置 Chat Completions 转 Responses')

  assert.throws(() => {
    createRegressionAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '原生 Responses 账号也不能配置 Chat 转 Responses',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-mapping-native-chat-to-responses',
        base_url: 'https://api.openai.com/v1',
        supported_endpoint_modes: ['responses_json', 'responses_sse']
      },
      supportedModels: [upstreamModel],
      modelMappings: [
        chatToResponsesMapping(sourceModel, upstreamModel)
      ],
      groupId: group.id
    }, ownerAccess)
  }, /账号模型别名只支持同协议映射/, '即使账号真实支持 Responses，也不能配置 Chat Completions 转 Responses')

  assert.doesNotThrow(() => {
    createRegressionAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      name: 'OpenAI-compatible Chat-only 账号可以配置 Responses 转 Chat',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-mapping-chat-only-responses-to-chat',
        base_url: 'https://api.openai.com/v1',
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      supportedModels: [upstreamModel],
      modelMappings: [
        responsesToChatMapping(sourceModel, upstreamModel)
      ],
      groupId: openAICompatibleGroup.id
    }, ownerAccess)
  }, '通用 OpenAI-compatible Chat-only 账号应允许显式配置 Responses -> Chat Completions bridge')

  assert.doesNotThrow(() => {
    createRegressionAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      name: 'OpenAI-compatible Chat-only 账号可以配置 Codex Responses 转 Chat',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-mapping-codex-chat-only-responses-to-chat',
        base_url: 'https://api.openai.com/v1',
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      supportedModels: [chatCompletionsUpstreamModel],
      modelMappings: [
        responsesToChatMapping(sourceModel, chatCompletionsUpstreamModel)
      ],
      groupId: openAICompatibleGroup.id
    }, ownerAccess)
  }, 'OpenAI-compatible 账号应允许在账号模型别名里配置 Responses -> Chat Completions')

  assert.doesNotThrow(() => {
    createRegressionAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      name: 'Chat-only 模型可作为 Responses 到 Chat 来源别名',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-mapping-chat-source-responses-to-chat',
        base_url: 'https://api.openai.com/v1',
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      supportedModels: [chatCompletionsUpstreamModel],
      modelMappings: [
        responsesToChatMapping(chatCompletionsUpstreamModel, chatCompletionsUpstreamModel)
      ],
      groupId: openAICompatibleGroup.id
    }, ownerAccess)
  }, 'OpenAI v1 Responses -> Chat bridge 的下游别名允许选择当前供应商 Chat-only 模型')
}

async function assertEndpointModeUpdateValidatesEnabledMappings(groupId: string): Promise<void> {
  const account = createRegressionAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账号模型映射能力更新校验账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-model-mapping-capability-update',
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['chat_json', 'responses_json', 'responses_sse']
    },
    supportedModels: [chatCompletionsUpstreamModel],
    healthCheckEndpointFamily: 'responses',
    modelMappings: [
      responsesToChatMapping(sourceModel, chatCompletionsUpstreamModel)
    ],
    groupId
  }, ownerAccess)

  assert.throws(() => repositories.updateAccount(account.id, {
    credentials: {
      api_key: 'sk-account-model-mapping-capability-update',
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['responses_json', 'responses_sse']
    }
  }, ownerAccess), /Chat Completions.*上游接口能力/, '仅修改上游接口能力时，后端仍须校验已有启用映射的右侧目标族')

  const updated = repositories.updateAccount(account.id, {
    credentials: {
      api_key: 'sk-account-model-mapping-capability-update',
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['responses_json', 'responses_sse']
    },
    modelMappings: [
      responsesToChatMapping(sourceModel, chatCompletionsUpstreamModel, false)
    ]
  }, ownerAccess)
  assert.deepEqual(updated?.modelMappings, [
    responsesToChatMapping(sourceModel, chatCompletionsUpstreamModel, false)
  ], '停用映射在目标族能力缺失时应原样保留，不能被静默删除')

  const asyncAccount = createRegressionAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账号模型映射异步能力更新校验账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-model-mapping-async-capability-update',
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['chat_json', 'responses_json', 'responses_sse']
    },
    supportedModels: [chatCompletionsUpstreamModel],
    healthCheckEndpointFamily: 'responses',
    modelMappings: [responsesToChatMapping(sourceModel, chatCompletionsUpstreamModel)],
    groupId
  }, ownerAccess)
  await assert.rejects(
    repositories.updateAccountAsync(asyncAccount.id, {
      credentials: {
        api_key: 'sk-account-model-mapping-async-capability-update',
        base_url: 'https://api.openai.com/v1',
        supported_endpoint_modes: ['responses_json', 'responses_sse']
      }
    }, ownerAccess),
    /Chat Completions.*上游接口能力/,
    '异步账户更新仅修改 endpoint modes 时也必须重验已有启用映射'
  )
  const preserved = repositories.findAccountForTest(asyncAccount.id, ownerAccess)
  assert.deepEqual(
    preserved?.credentials.supported_endpoint_modes,
    ['chat_json', 'responses_json', 'responses_sse'],
    '异步 endpoint modes 更新被拒绝后必须保留原凭据能力'
  )
  assert.deepEqual(
    preserved?.modelMappings,
    [responsesToChatMapping(sourceModel, chatCompletionsUpstreamModel)],
    '异步 endpoint modes 更新被拒绝后必须保留原映射'
  )
}

function assertRuntimeIgnoresUnsupportedChatToResponsesMapping(): void {
  const mapping = resolveOpenAIAccountModelMapping({
    modelMappings: [
      chatToResponsesMapping(sourceModel, upstreamModel)
    ]
  }, sourceModel, 'chat_completions')
  assert.equal(mapping, undefined, '运行时解析器不应返回历史残留的 Chat Completions -> Responses 映射')
}

function assertRuntimeIgnoresPersistentCrossProtocolMappings(): void {
  const persistentResponsesToChat = resolveOpenAIAccountModelMapping({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: 'openai',
    protocolVersion: 'v1',
    modelMappings: [
      responsesToChatMapping(sourceModel, chatCompletionsUpstreamModel)
    ]
  }, sourceModel, 'responses')
  assert.deepEqual(persistentResponsesToChat, {
    sourceModel,
    sourceEndpointFamily: 'responses',
    upstreamModel: chatCompletionsUpstreamModel,
    upstreamEndpointFamily: 'chat_completions'
  }, '运行时解析器应执行 OpenAI v1 账号级 Responses -> Chat Completions 映射')

  const chatRequestWithoutExactMapping = resolveOpenAIAccountModelMapping({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: 'openai',
    protocolVersion: 'v1',
    modelMappings: [
      responsesToChatMapping(sourceModel, chatCompletionsUpstreamModel)
    ]
  }, sourceModel, 'chat_completions')
  assert.equal(chatRequestWithoutExactMapping, undefined, '运行时解析器没有当前请求协议精确映射时不应复用其它协议映射')

  const disabledExplicitChatMapping = resolveOpenAIAccountModelMapping({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: 'openai',
    protocolVersion: 'v1',
    modelMappings: [
      {
        sourceModel,
        sourceEndpointFamily: 'chat_completions',
        upstreamModel: chatCompletionsUpstreamModel,
        upstreamEndpointFamily: 'chat_completions',
        enabled: false
      },
      responsesToChatMapping(sourceModel, chatCompletionsUpstreamModel)
    ]
  }, sourceModel, 'chat_completions')
  assert.equal(disabledExplicitChatMapping, undefined, '显式停用当前协议映射时不应命中模型映射')

  const persistentMessagesToChat = resolveOpenAIAccountModelMapping({
    modelMappings: [
      messagesToChatMapping(anthropicMessagesSourceModel, chatCompletionsUpstreamModel)
    ]
  }, anthropicMessagesSourceModel, 'messages')
  assert.equal(persistentMessagesToChat, undefined, '运行时解析器不应执行历史残留的账号级 Messages -> Chat Completions 映射')

  const explicitRouteAccount = {
    modelMappings: [
      {
        ...responsesToChatMapping(sourceModel, chatCompletionsUpstreamModel),
        runtimeSource: 'explicit_hybrid_route',
        runtimeRouteRuleId: 'rule_runtime_responses_to_chat'
      }
    ]
  } as unknown as { modelMappings: AccountModelMapping[] }
  const explicitRouteMapping = resolveOpenAIAccountModelMapping(explicitRouteAccount, sourceModel, 'responses')
  assert.equal(explicitRouteMapping, undefined, '运行时解析器不应继续放行旧显式混合路由标记注入的 Responses -> Chat Completions 映射')
}

async function assertCompactSyntheticChatUsesResponsesModelMapping(): Promise<void> {
  const account = {
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: 'openai',
    protocolVersion: 'v1',
    modelMappings: [
      responsesMapping(sourceModel, upstreamModel)
    ]
  }
  const req = jsonRequestAtPath('/v1/chat/completions', {
    model: sourceModel,
    messages: [{ role: 'user', content: 'compact summary' }],
    stream: false
  })

  assert.equal(gatewayRequestEndpointFamily(req), 'chat_completions', '普通 Chat 请求应保持 Chat Completions 源协议')
  assert.equal(
    resolveOpenAIRequestModelMapping(req, account),
    undefined,
    '普通 Chat 请求不应自动复用 Responses 模型别名'
  )

  setGatewayModelMappingSourceEndpointFamilyOverride(req, 'responses')
  assert.equal(gatewayRequestEndpointFamily(req), 'responses', '内部 compact 摘要请求应允许按 Responses 源协议解析模型别名')
  const mapping = resolveOpenAIRequestModelMapping(req, account)
  assert.deepEqual(mapping, {
    sourceModel,
    sourceEndpointFamily: 'responses',
    upstreamModel,
    upstreamEndpointFamily: 'responses'
  }, '内部 compact 摘要请求应命中原始 Responses 模型别名')
  assert(mapping, '内部 compact 摘要请求应返回模型映射')
  assert.equal(
    openAIModelMappedUpstreamPathAndQuery(req, mapping),
    '/v1/chat/completions',
    '内部 compact 摘要请求即使命中 Responses 同协议别名，也必须继续转发 Chat Completions 路径'
  )
  const mappedBody = JSON.parse((await buildOpenAIModelMappedJsonBody(req, mapping.upstreamModel)).toString('utf8')) as {
    model?: string
    messages?: unknown[]
  }
  assert.equal(mappedBody.model, upstreamModel, '内部 compact 摘要请求应把上游 Chat body 的 model 改写为映射目标')
  assert(Array.isArray(mappedBody.messages), '内部 compact 摘要请求改写模型时不应丢失 Chat messages')
}

function assertCrossProtocolAccountMappingsRejected(groupId: string): void {
  assert.throws(() => {
    createRegressionAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '账号模型别名不能配置 Messages 到 Chat',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-alias-messages-to-chat',
        base_url: 'https://api.openai.com/v1'
      },
      supportedModels: [chatCompletionsUpstreamModel],
      modelMappings: [
        messagesToChatMapping(anthropicMessagesSourceModel, chatCompletionsUpstreamModel)
      ],
      groupId
    }, ownerAccess)
  }, /账号模型别名不支持 Anthropic Messages 跨协议映射/, '账号模型别名不应允许 Anthropic Messages -> Chat Completions')

  const anthropicGroup = repositories.createGroup({
    name: '账号模型别名拒绝 Gemini 到 Messages 分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
  }, ownerAccess)
  assert.throws(() => {
    createRegressionAccount({
      providerCode: ANTHROPIC_PROVIDER_CODE,
      providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
      name: '账号模型别名不能配置 Gemini 到 Messages',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-alias-gemini-to-messages',
        base_url: 'https://api.anthropic.com',
        supported_endpoint_modes: ['messages_json']
      },
      supportedModels: [anthropicMessagesUpstreamModel],
      modelMappings: [
        geminiGenerateContentToMessagesMapping(geminiGenerateContentSourceModel, anthropicMessagesUpstreamModel)
      ],
      groupId: anthropicGroup.id
    }, ownerAccess)
  }, /账号模型别名不支持 Gemini GenerateContent 跨协议映射/, '账号模型别名不应允许 Gemini GenerateContent -> Anthropic Messages')

  const geminiGroup = repositories.createGroup({
    name: '账号模型别名拒绝 OpenAI 到 Gemini 分组',
    providerCode: GEMINI_PROVIDER_CODE,
  }, ownerAccess)
  assert.throws(() => {
    createRegressionAccount({
      providerCode: GEMINI_PROVIDER_CODE,
      providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
      name: '账号模型别名不能配置 Responses 到 Gemini',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-alias-responses-to-gemini',
        base_url: 'https://generativelanguage.googleapis.com/v1beta',
        supported_endpoint_modes: ['generate_content_json']
      },
      supportedModels: [geminiNativeUpstreamModel],
      modelMappings: [
        toGeminiGenerateContentMapping(sourceModel, geminiNativeUpstreamModel, 'responses')
      ],
      groupId: geminiGroup.id
    }, ownerAccess)
  }, /账号模型别名只支持同协议映射/, '账号模型别名不应允许 OpenAI Responses -> Gemini GenerateContent')
}

function assertHybridProtocolModelPools(): void {
  const group = repositories.createGroup({
    name: '混合供应商协议模型池约束分组',
    providerCode: HYBRID_PROVIDER_CODE,
  }, ownerAccess)
  assert.throws(() => {
    createRegressionAccount({
      providerCode: HYBRID_PROVIDER_CODE,
      providerProtocolProfileId: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
      name: '混合供应商 Responses 不能选择 Chat-only 来源模型',
      type: 'api_key',
      credentials: {
        api_key: 'sk-hybrid-account-model-mapping-chat-only-source',
        base_url: 'https://api.openai.com/v1',
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      supportedModels: [chatCompletionsUpstreamModel],
      modelMappings: [
        responsesToChatMapping(chatCompletionsUpstreamModel, chatCompletionsUpstreamModel)
      ],
      groupId: group.id
    }, ownerAccess)
  }, /账号模型别名来源模型不在对应协议模型池中/, '混合供应商 Responses 来源模型必须来自支持 Responses 的模型池')

  assert.doesNotThrow(() => {
    const account = createRegressionAccount({
      providerCode: HYBRID_PROVIDER_CODE,
      providerProtocolProfileId: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
      name: '混合供应商 Responses 到 Chat 合法模型池',
      type: 'api_key',
      credentials: {
        api_key: 'sk-hybrid-account-model-mapping-responses-source',
        base_url: 'https://api.openai.com/v1',
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      supportedModels: [chatCompletionsUpstreamModel],
      modelMappings: [
        responsesToChatMapping(sourceModel, chatCompletionsUpstreamModel)
      ],
      groupId: group.id
    }, ownerAccess)
    assert.equal(account.modelMappings?.[0]?.sourceModel, sourceModel, '合法混合供应商映射应保留 Responses 来源模型')
  }, '混合供应商应允许 Responses 协议模型映射到 Chat 上游模型')
}

function assertProtocolMatrixHelper(): void {
  const openAIProfile = { protocolCode: 'openai', protocolVersion: 'v1' }
  const anthropicProfile = { protocolCode: 'anthropic', protocolVersion: 'v1' }
  const geminiNativeProfile = {
    providerProtocolProfileId: 'profile_gemini_native_v1beta',
    protocolCode: 'gemini',
    protocolVersion: 'v1beta'
  }
  const geminiOpenAIChatProfile = {
    providerProtocolProfileId: 'profile_gemini_openai_chat_v1beta',
    protocolCode: 'openai',
    protocolVersion: 'v1'
  }
  assert(accountModelMappingProtocolRules.some((rule) => (
    rule.source === 'chat_completions' && rule.upstream === 'chat_completions'
  )), '后端协议矩阵必须显式包含 Chat Completions 同协议别名')
  assert(accountModelMappingProtocolRules.some((rule) => (
    rule.source === 'responses' && rule.upstream === 'responses'
  )), '后端协议矩阵必须显式包含 Responses 同协议别名')
  assert(accountModelMappingProtocolRules.some((rule) => (
    rule.source === 'responses' && rule.upstream === 'chat_completions'
  )), '后端协议矩阵必须显式包含 OpenAI Responses 到 Chat Completions bridge')
  assert(accountModelMappingProtocolRules.some((rule) => (
    rule.source === 'messages' && rule.upstream === 'messages'
  )), '后端协议矩阵必须显式包含 Anthropic Messages 同协议别名')
  assert(accountModelMappingProtocolRules.some((rule) => (
    rule.source === 'stream_generate_content' && rule.upstream === 'generate_content'
  )), '后端协议矩阵必须显式包含 Gemini StreamGenerateContent 到 GenerateContent 别名')
  assert.throws(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'chat_completions',
    upstreamEndpointFamily: 'responses'
  }, {
    providerProfile: openAIProfile,
    supportedEndpointModes: ['chat_json', 'chat_sse']
  }), /账号模型别名只支持同协议映射/, '后端矩阵应拒绝无原生 Responses 能力的 Chat -> Responses')
  assert.doesNotThrow(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'chat_completions',
    enabled: true
  }, {
    providerProfile: openAIProfile,
    supportedEndpointModes: ['chat_sse']
  }), '后端矩阵应允许 OpenAI v1 Responses -> Chat Completions bridge')
  assert.throws(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'chat_completions',
    enabled: true
  }, {
    providerProfile: openAIProfile,
    supportedEndpointModes: ['responses_json']
  }), /Chat Completions.*上游接口能力/, 'Responses -> Chat 启用映射必须按右侧要求 Chat 上游能力，不能被左侧 Responses 能力放行')
  assert.doesNotThrow(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'chat_completions',
    enabled: false
  }, {
    providerProfile: openAIProfile,
    supportedEndpointModes: ['responses_json']
  }), 'Responses -> Chat 停用映射在 Chat 上游能力缺失时仍应允许保留')
  assert.throws(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'messages',
    upstreamEndpointFamily: 'responses'
  }, {
    providerProfile: openAIProfile,
    supportedEndpointModes: ['responses_json']
  }), /账号模型别名不支持 Anthropic Messages 跨协议映射/, '后端矩阵应拒绝 Anthropic Messages -> Responses')
  assert.doesNotThrow(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'responses'
  }, {
    providerProfile: openAIProfile,
    supportedEndpointModes: ['responses_json']
  }), '后端矩阵应允许原生 Responses 同协议别名')
  assert.doesNotThrow(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'messages',
    upstreamEndpointFamily: 'messages',
    enabled: true
  }, {
    providerProfile: anthropicProfile,
    supportedEndpointModes: ['messages_json']
  }), '后端矩阵应允许 Anthropic Messages 同协议别名')
  assert.throws(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'messages',
    upstreamEndpointFamily: 'messages',
    enabled: true
  }, {
    providerProfile: anthropicProfile,
    supportedEndpointModes: ['message_token_counting']
  }), /Messages.*上游接口能力/, 'Messages 启用映射不能把 token-counting 当作 Messages 请求能力')
  assert.doesNotThrow(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'stream_generate_content',
    upstreamEndpointFamily: 'generate_content',
    enabled: true
  }, {
    providerProfile: geminiNativeProfile,
    supportedEndpointModes: ['generate_content_json', 'generate_content_sse']
  }), '后端矩阵应允许 Gemini StreamGenerateContent 到 GenerateContent 别名')
  assert.throws(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'stream_generate_content',
    upstreamEndpointFamily: 'generate_content',
    enabled: true
  }, {
    providerProfile: geminiNativeProfile,
    supportedEndpointModes: ['count_tokens']
  }), /Gemini GenerateContent.*上游接口能力/, 'Gemini 启用映射必须要求 GenerateContent JSON 或 SSE 上游能力')
  assert.throws(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'generate_content'
  }, {
    providerProfile: geminiOpenAIChatProfile,
    supportedEndpointModes: ['chat_json']
  }), /Gemini OpenAI Chat 档案的账号模型别名(?:只能使用 Chat Completions|上游协议只能是 Chat Completions)|账号模型别名只支持同协议映射/, '后端矩阵应拒绝 Gemini OpenAI Chat 作为 Gemini native 右侧目标')
  assert.throws(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'messages',
    upstreamEndpointFamily: 'chat_completions'
  }, {
    providerProfile: geminiOpenAIChatProfile,
    supportedEndpointModes: ['chat_json']
  }), /Gemini OpenAI Chat 档案的账号模型别名只能使用 Chat Completions|账号模型别名不支持 Anthropic Messages 跨协议映射/, '后端矩阵应拒绝 Gemini OpenAI Chat 的 Messages 来源')
}

function assertUnsupportedAnthropicMessagesMappingsRejected(groupId: string): void {
  assert.throws(() => {
    createRegressionAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: 'Messages 不能桥接到 Responses',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-mapping-messages-to-responses',
        base_url: 'https://api.openai.com/v1',
        supported_endpoint_modes: ['responses_json']
      },
      supportedModels: [upstreamModel],
      modelMappings: [
        messagesToResponsesMapping(anthropicMessagesSourceModel, upstreamModel)
      ],
      groupId
    }, ownerAccess)
  }, /Anthropic Messages 下游协议当前只支持.*Chat Completions/, 'Messages 下游协议不能桥接到 Responses 上游')

  assert.throws(() => {
    createRegressionAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: 'Messages 不能配置为 Messages 上游',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-mapping-messages-to-messages',
        base_url: 'https://api.openai.com/v1'
      },
      supportedModels: [upstreamModel],
      modelMappings: [
        messagesToMessagesMapping(anthropicMessagesSourceModel, anthropicMessagesSourceModel)
      ],
      groupId
    }, ownerAccess)
  }, /Anthropic Messages 下游协议当前只支持.*Chat Completions/, 'Messages 下游协议不能桥接到 Messages 上游')

  assert.throws(() => {
    createRegressionAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: 'Messages source 必须来自 Anthropic 模型池',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-mapping-messages-source-pool',
        base_url: 'https://api.openai.com/v1',
        supported_endpoint_modes: ['chat_json']
      },
      supportedModels: [chatCompletionsUpstreamModel],
      modelMappings: [
        messagesToChatMapping(sourceModel, chatCompletionsUpstreamModel)
      ],
      groupId
    }, ownerAccess)
  }, /映射下游模型不在对应协议客户端模型池中/, 'Messages 下游模型必须来自 Anthropic 协议模型池')
}

function assertImportPreviewRejectsInvalidMapping(groupId: string): void {
  const result = previewAccountImport({
    type: 'juhe-ai-account-import',
    version: 1,
    accounts: [
      {
        name: '账号模型映射非法导入预览',
        providerCode: GPT_VENDOR_CODE,
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
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
  assert(result.accounts[0]?.messages.some((message) => message.includes('账号模型别名来源模型不在当前供应商的对应协议模型目录中')), '导入预览应在预览阶段暴露模型映射目录错误')
}

function assertImportPreviewRejectsNonNativeResponsesMapping(groupId: string): void {
  const result = previewAccountImport({
    type: 'juhe-ai-account-import',
    version: 1,
    accounts: [
      {
        name: '账号模型映射 Chat-only 非法 Responses 上游导入预览',
        providerCode: GPT_VENDOR_CODE,
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
        type: 'api_key',
        status: 'active',
        groupId,
        credentials: {
          api_key: 'sk-account-model-mapping-import-chat-only-responses-upstream',
          base_url: 'https://api.openai.com/v1',
          supported_endpoint_modes: ['chat_json', 'chat_sse']
        },
        modelMappings: [
          responsesMapping(sourceModel, replacementUpstreamModel)
        ]
      }
    ]
  }, {}, ownerAccess)
  assert.equal(result.canImport, false, '非原生 Responses 上游映射导入预览不应允许确认导入')
  assert.equal(result.accounts[0]?.action, 'failed', '非原生 Responses 上游映射导入预览应标记账户失败')
  assert(result.accounts[0]?.messages.some((message) => message.includes('上游协议 Responses 只能用于账号真实支持 Responses API 的原生上游')), '导入预览应暴露右侧 Responses 原生能力约束错误')
}

function assertImportPreviewRejectsUnsupportedMessagesMapping(groupId: string): void {
  const result = previewAccountImport({
    type: 'juhe-ai-account-import',
    version: 1,
    accounts: [
      {
        name: '账号模型映射非法 Messages 上游导入预览',
        providerCode: GPT_VENDOR_CODE,
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
        type: 'api_key',
        status: 'active',
        groupId,
        credentials: {
          api_key: 'sk-account-model-mapping-import-messages-to-responses',
          base_url: 'https://api.openai.com/v1',
          supported_endpoint_modes: ['responses_json']
        },
        modelMappings: [
          messagesToResponsesMapping(anthropicMessagesSourceModel, upstreamModel)
        ]
      }
    ]
  }, {}, ownerAccess)
  assert.equal(result.canImport, false, 'Messages 到 Responses 非法映射导入预览不应允许确认导入')
  assert.equal(result.accounts[0]?.action, 'failed', 'Messages 到 Responses 非法映射导入预览应标记账户失败')
  assert(result.accounts[0]?.messages.some((message) => message.includes('账号模型别名不支持 Anthropic Messages 跨协议映射')), '导入预览应暴露 Messages 下游协议方向约束错误')
}

function assertImportAndDraftValidateTargetCapabilities(groupId: string): void {
  const enabledMapping = responsesToChatMapping(sourceModel, chatCompletionsUpstreamModel)
  const disabledMapping = responsesToChatMapping(sourceModel, chatCompletionsUpstreamModel, false)
  const importAccount = (mapping: AccountModelMapping) => ({
    name: `目标能力导入回归-${mapping.enabled ? '启用' : '停用'}`,
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    type: 'api_key' as const,
    status: 'active' as const,
    groupId,
    credentials: {
      api_key: `sk-import-target-capability-${mapping.enabled ? 'enabled' : 'disabled'}`,
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['responses_json', 'responses_sse']
    },
    supportedModels: [chatCompletionsUpstreamModel],
    healthCheckModel: chatCompletionsUpstreamModel,
    healthCheckEndpointFamily: 'responses' as const,
    modelMappings: [mapping]
  })
  const rejectedImport = previewAccountImport({
    type: 'juhe-ai-account-import',
    version: 1,
    accounts: [importAccount(enabledMapping)]
  }, {}, ownerAccess)
  assert.equal(rejectedImport.canImport, false, '导入预览必须拒绝目标族能力缺失的启用映射')
  assert(rejectedImport.accounts[0]?.messages.some((message) => /Chat Completions.*上游接口能力/.test(message)), '导入预览应返回右侧目标族能力错误')
  const acceptedImport = previewAccountImport({
    type: 'juhe-ai-account-import',
    version: 1,
    accounts: [importAccount(disabledMapping)]
  }, {}, ownerAccess)
  assert.equal(acceptedImport.canImport, true, '导入预览应允许目标族能力缺失的停用映射')

  assert.throws(() => prepareAccountDraftTestSnapshot({
    accountInput: {
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '草稿健康协议族最终能力校验',
      type: 'api_key',
      credentials: {
        api_key: 'sk-draft-health-family-capability',
        base_url: 'https://api.openai.com/v1',
        supported_endpoint_modes: ['chat_json', 'responses_sse']
      },
      supportedModels: [chatCompletionsUpstreamModel],
      healthCheckModel: chatCompletionsUpstreamModel,
      healthCheckEndpointFamily: 'responses',
      groupId
    },
    requestAccess: ownerAccess
  }), /账户健康检查协议族 responses 未启用对应 JSON 能力/, '草稿测试必须按最终 endpoint modes 拒绝不可用健康检查协议族')

  const draftInput = (mapping: AccountModelMapping) => ({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `目标能力草稿回归-${mapping.enabled ? '启用' : '停用'}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-draft-target-capability-${mapping.enabled ? 'enabled' : 'disabled'}`,
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['responses_json', 'responses_sse']
    },
    supportedModels: [chatCompletionsUpstreamModel],
    healthCheckModel: chatCompletionsUpstreamModel,
    healthCheckEndpointFamily: 'responses' as const,
    modelMappings: [mapping],
    groupId
  })
  assert.throws(() => prepareAccountDraftTestSnapshot({
    accountInput: draftInput(enabledMapping),
    requestAccess: ownerAccess
  }), /Chat Completions.*上游接口能力/, '草稿测试必须拒绝目标族能力缺失的启用映射')
  const acceptedDraft = prepareAccountDraftTestSnapshot({
    accountInput: draftInput(disabledMapping),
    requestAccess: ownerAccess
  })
  assert.deepEqual(acceptedDraft.draftAccount.modelMappings, [disabledMapping], '草稿测试应保留目标族能力缺失的停用映射')
}

function loadStoredMappings(accountId: string): AccountModelMapping[] {
  return (databaseModule.getBusinessDatabase()
    .prepare('SELECT source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled FROM account_model_mappings WHERE account_id = ? ORDER BY source_model ASC, source_endpoint_family ASC')
    .all(accountId) as unknown as Array<{ source_model: string; source_endpoint_family: AccountModelMapping['sourceEndpointFamily']; upstream_model: string; upstream_endpoint_family: AccountModelMapping['upstreamEndpointFamily']; enabled: number }>)
    .map((row) => ({
      sourceModel: row.source_model,
      sourceEndpointFamily: row.source_endpoint_family,
      upstreamModel: row.upstream_model,
      upstreamEndpointFamily: row.upstream_endpoint_family,
      enabled: row.enabled === 1
    }))
}

function jsonRequest(body: Record<string, unknown>): Request {
  return jsonRequestAtPath('/v1/responses', body)
}

function jsonRequestAtPath(path: string, body: Record<string, unknown>): Request {
  const rawBody = Buffer.from(JSON.stringify(body), 'utf8')
  const headers: Record<string, string> = { 'content-type': 'application/json' }
  return {
    method: 'POST',
    path,
    originalUrl: path,
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
    const fallback = createRegressionAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
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
    requestedReasoningEffort: 'low',
    effectiveReasoningEffort: 'high',
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
  assert.equal(record.requestedReasoningEffort, 'low', '使用记录应保存客户端请求思考级别')
  assert.equal(record.effectiveReasoningEffort, 'high', '使用记录应保存最终上游思考级别')
  assert.equal(record.costUsd, 12, '授权调用应按资源账号所有者个人映射目标模型计价')

  const compactTraceId = 'trace-account-model-mapping-compact-summary-regression'
  const compactSummaryReq = jsonRequestAtPath('/v1/chat/completions', {
    model: sourceModel,
    messages: [{ role: 'user', content: 'compact usage' }],
    stream: false
  })
  setGatewayModelMappingSourceEndpointFamilyOverride(compactSummaryReq, 'responses')
  recordCompletedUpstreamAttempt(compactSummaryReq, {
    traceId: compactTraceId,
    trafficSource: 'gateway',
    systemAccountId: 'sys_mapping_grantee',
    groupId,
    account,
    endpoint: 'POST /v1/chat/completions',
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
  const compactRecord = repositories.listUsageRecords(undefined, { page: 1, pageSize: 20 })
    .items
    .find((item) => item.traceId === compactTraceId)
  assert(compactRecord, '内部 compact 摘要请求应写入使用记录')
  assert.equal(compactRecord.model, sourceModel, '内部 compact 摘要请求使用记录 model 应保留下游 Responses 模型')
  assert.equal(compactRecord.upstreamModel, upstreamModel, '内部 compact 摘要请求使用记录 upstreamModel 应记录模型映射目标')
  assert.equal(compactRecord.modelMappingApplied, true, '内部 compact 摘要请求使用记录应标记命中模型映射')

  const unpricedTraceId = 'trace-account-model-mapping-unpriced-upstream-regression'
  recordCompletedUpstreamAttempt(jsonRequest({ model: sourceModel, input: 'usage', stream: false }), {
    traceId: unpricedTraceId,
    trafficSource: 'gateway',
    systemAccountId: 'sys_mapping_grantee',
    groupId,
    account: {
      ...account,
      modelMappings: [
        responsesMapping(sourceModel, unpricedUpstreamModel)
      ]
    },
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
  const unpricedRecord = repositories.listUsageRecords(undefined, { page: 1, pageSize: 20 })
    .items
    .find((item) => item.traceId === unpricedTraceId)
  assert(unpricedRecord, '上游别名未在价格目录命中时也应写入使用记录')
  assert.equal(unpricedRecord.model, sourceModel, '上游别名未定价时使用记录仍应保留下游模型')
  assert.equal(unpricedRecord.upstreamModel, unpricedUpstreamModel, '上游别名未定价时使用记录仍应记录实际上游模型')
  assert.equal(unpricedRecord.pricingModel, sourceModel, '上游别名未定价时应回落到下游来源模型计价')
  assert.equal(unpricedRecord.costUsd, 3, '上游别名未定价时不应把成本错误记录为 0')
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
