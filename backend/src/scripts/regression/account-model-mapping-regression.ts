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
  GPT_VENDOR_CODE
} from '../../domain/provider-protocol.js'
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
const anthropicMessagesSourceModel = 'claude-mapping-regression-source'
const geminiGenerateContentSourceModel = 'gemini-3.5-flash'
const geminiNativeUpstreamModel = 'gemini-mapping-regression-upstream'
const anthropicMessagesUpstreamModel = 'claude-haiku-4-5'
const chatCompletionsUpstreamModel = 'gpt-mapping-regression-chat-upstream'
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

function geminiGenerateContentToResponsesMapping(sourceModel: string, upstreamModel: string, enabled = true): AccountModelMapping {
  return {
    sourceModel,
    sourceEndpointFamily: 'generate_content',
    upstreamModel,
    upstreamEndpointFamily: 'responses',
    enabled
  }
}

function assertUnchangedModelConfigPatchSkipsProviderCatalogValidation(): void {
  const source = readFileSync(new URL('../../storage/repositories.ts', import.meta.url), 'utf8')
  assert.match(source, /normalizeSupportedModelsIfUnchanged\(input\.supportedModels, current\.supportedModels\)/, '账户 PATCH 相同 supportedModels 应先本地比较，避免重复查询供应商模型目录')
  assert.match(source, /normalizeModelMappingsIfUnchanged\(input\.modelMappings, current\.modelMappings\)/, '账户 PATCH 相同 modelMappings 应先本地比较，避免重复查询跨协议模型池')
  assert.match(source, /unchangedSupportedModelsInput \?\? await normalizeAccountSupportedModelsForProviderAsync/, 'PG 账号 PATCH 相同 supportedModels 不应继续走 provider catalog async 校验')
  assert.match(source, /unchangedModelMappingsInput \?\? await normalizeAccountModelMappingsForProviderAsync/, 'PG 账号 PATCH 相同 modelMappings 不应继续走 provider catalog async 校验')
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
  const anthropicMessagesModel = saveCustomProviderModel({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    model: anthropicMessagesSourceModel,
    scope: 'global',
    supportedApiProtocols: ['messages', 'message_token_counting'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  assert.deepEqual(anthropicMessagesModel.supportedApiProtocols, ['messages', 'message_token_counting'], 'Anthropic 自定义模型协议白名单应保留 messages 与 message_token_counting')
  saveCustomProviderModel({
    providerCode: GPT_VENDOR_CODE,
    model: chatCompletionsUpstreamModel,
    scope: 'global',
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: ownerAccess.systemAccountId
  })
  saveCustomProviderModel({
    providerCode: GEMINI_PROVIDER_CODE,
    model: geminiNativeUpstreamModel,
    scope: 'global',
    supportedApiProtocols: ['generate_content', 'stream_generate_content'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
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

  assertProtocolMatrixHelper()
  assertUnchangedModelConfigPatchSkipsProviderCatalogValidation()

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

  assertNativeResponsesUpstreamRequiresEndpointModes()
  assertRuntimeIgnoresUnsupportedChatToResponsesMapping()
  assertAnthropicMessagesToChatMapping(group.id)
  assertGeminiNativeToAnthropicMessagesMapping()
  assertOpenAIAndAnthropicToGeminiNativeMapping()
  assertUnsupportedAnthropicMessagesMappingsRejected(group.id)
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
  }, /映射下游模型不在对应协议客户端模型池中/, '草稿模型不能作为下游映射源')

  assert.throws(() => {
    repositories.updateAccount(account.id, {
      modelMappings: [
        responsesMapping(sourceModel, 'gpt-mapping-regression-missing')
      ]
    }, ownerAccess)
  }, /映射上游模型不在当前账号可用模型池中/, '映射上游模型必须存在于当前账号可用模型池')
  assertImportPreviewRejectsInvalidMapping(group.id)
  assertImportPreviewRejectsNonNativeResponsesMapping(group.id)
  assertImportPreviewRejectsUnsupportedMessagesMapping(group.id)

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
    providerCode: GPT_VENDOR_CODE
  }, ownerAccess)
  assert.throws(() => {
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      name: 'Chat-only 账号不能配置 Responses 上游',
      type: 'api_key',
      clientCompatibility: 'openai_standard',
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
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      name: 'Chat-only 账号不能配置 Chat 转 Responses',
      type: 'api_key',
      clientCompatibility: 'openai_standard',
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
  }, /暂不支持 Chat Completions 到 Responses 的协议转换/, 'Chat-only 账号不能配置 Chat Completions 转 Responses')

  assert.throws(() => {
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      name: '原生 Responses 账号也不能配置 Chat 转 Responses',
      type: 'api_key',
      clientCompatibility: 'openai_standard',
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
  }, /暂不支持 Chat Completions 到 Responses 的协议转换/, '即使账号真实支持 Responses，也不能配置 Chat Completions 转 Responses')

  const chatBridgeAccount = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    name: 'Chat-only 账号允许 Responses 转 Chat',
    type: 'api_key',
    clientCompatibility: 'openai_standard',
    credentials: {
      api_key: 'sk-account-model-mapping-chat-only-responses-to-chat',
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    supportedModels: [upstreamModel],
    modelMappings: [
      responsesToChatMapping(sourceModel, upstreamModel)
    ],
    groupId: group.id
  }, ownerAccess)
  assert.deepEqual(chatBridgeAccount.modelMappings, [
    responsesToChatMapping(sourceModel, upstreamModel)
  ], 'Chat-only 账号仍允许显式 Responses 转 Chat Completions bridge')

  const codexChatBridgeAccount = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    name: 'Codex Chat-only 账号允许 Responses 转 Chat',
    type: 'api_key',
    clientCompatibility: 'codex_responses',
    credentials: {
      api_key: 'sk-account-model-mapping-codex-chat-only-responses-to-chat',
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    supportedModels: [chatCompletionsUpstreamModel],
    modelMappings: [
      responsesToChatMapping(sourceModel, chatCompletionsUpstreamModel)
    ],
    groupId: group.id
  }, ownerAccess)
  assert.deepEqual(codexChatBridgeAccount.modelMappings, [
    responsesToChatMapping(sourceModel, chatCompletionsUpstreamModel)
  ], 'Codex Responses 兼容的 Chat-only 账号可通过显式 Responses -> Chat Completions 映射保存真实 Chat endpoint modes')
}

function assertRuntimeIgnoresUnsupportedChatToResponsesMapping(): void {
  const mapping = resolveOpenAIAccountModelMapping({
    modelMappings: [
      chatToResponsesMapping(sourceModel, upstreamModel)
    ]
  }, sourceModel, 'chat_completions')
  assert.equal(mapping, undefined, '运行时解析器不应返回历史残留的 Chat Completions -> Responses 映射')
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
    rule.source === 'generate_content' && rule.upstream === 'messages'
  )), '后端协议矩阵必须显式包含 Gemini GenerateContent -> Messages')
  assert(accountModelMappingProtocolRules.some((rule) => (
    rule.source === 'responses' && rule.upstream === 'generate_content'
  )), '后端协议矩阵必须显式包含 OpenAI Responses -> Gemini GenerateContent')
  assert(accountModelMappingProtocolRules.some((rule) => (
    rule.source === 'messages' && rule.upstream === 'generate_content'
  )), '后端协议矩阵必须显式包含 Anthropic Messages -> Gemini GenerateContent')
  assert.doesNotThrow(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'chat_completions'
  }, {
    providerProfile: openAIProfile,
    supportedEndpointModes: ['chat_json', 'chat_sse']
  }), '后端矩阵应允许 Responses -> Chat Completions 命中 Chat-only OpenAI 档案')
  assert.throws(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'chat_completions',
    upstreamEndpointFamily: 'responses'
  }, {
    providerProfile: openAIProfile,
    supportedEndpointModes: ['chat_json', 'chat_sse']
  }), /暂不支持 Chat Completions 到 Responses 的协议转换/, '后端矩阵应拒绝无原生 Responses 能力的 Chat -> Responses')
  assert.throws(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'chat_completions',
    upstreamEndpointFamily: 'responses'
  }, {
    providerProfile: openAIProfile,
    supportedEndpointModes: ['responses_json']
  }), /暂不支持 Chat Completions 到 Responses 的协议转换/, '后端矩阵应拒绝原生 Responses 能力下的 Chat -> Responses')
  assert.doesNotThrow(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'messages'
  }, {
    providerProfile: anthropicProfile,
    supportedEndpointModes: ['messages_json']
  }), '后端矩阵应允许 OpenAI Responses -> Anthropic Messages')
  assert.throws(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'messages',
    upstreamEndpointFamily: 'responses'
  }, {
    providerProfile: openAIProfile,
    supportedEndpointModes: ['responses_json']
  }), /Anthropic Messages 下游协议当前只支持.*Chat Completions/, '后端矩阵应拒绝 Anthropic Messages -> Responses')
  assert.doesNotThrow(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'generate_content',
    upstreamEndpointFamily: 'messages'
  }, {
    providerProfile: anthropicProfile,
    supportedEndpointModes: ['messages_json']
  }), '后端矩阵应允许 Gemini GenerateContent -> Anthropic Messages')
  assert.doesNotThrow(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'generate_content'
  }, {
    providerProfile: geminiNativeProfile,
    supportedEndpointModes: ['generate_content_json', 'generate_content_sse']
  }), '后端矩阵应允许 OpenAI Responses -> Gemini GenerateContent')
  assert.doesNotThrow(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'messages',
    upstreamEndpointFamily: 'generate_content'
  }, {
    providerProfile: geminiNativeProfile,
    supportedEndpointModes: ['generate_content_json']
  }), '后端矩阵应允许 Anthropic Messages -> Gemini GenerateContent')
  assert.throws(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'generate_content'
  }, {
    providerProfile: geminiOpenAIChatProfile,
    supportedEndpointModes: ['chat_json']
  }), /Gemini OpenAI Chat 档案的模型映射上游协议只能是 Chat Completions/, '后端矩阵应拒绝 Gemini OpenAI Chat 作为 Gemini native 右侧目标')
  assert.throws(() => assertAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'messages',
    upstreamEndpointFamily: 'chat_completions'
  }, {
    providerProfile: geminiOpenAIChatProfile,
    supportedEndpointModes: ['chat_json']
  }), /Gemini OpenAI Chat 档案不支持 Anthropic Messages 来源映射/, '后端矩阵应拒绝 Gemini OpenAI Chat 的 Messages 来源')
}

function assertOpenAIAndAnthropicToGeminiNativeMapping(): void {
  const group = repositories.createGroup({
    name: 'OpenAI Anthropic 到 Gemini Native 映射回归分组',
    providerCode: GEMINI_PROVIDER_CODE,
    providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID
  }, ownerAccess)
  const mappings = [
    toGeminiGenerateContentMapping(sourceModel, geminiNativeUpstreamModel, 'chat_completions'),
    toGeminiGenerateContentMapping(sourceModel, geminiNativeUpstreamModel, 'responses'),
    toGeminiGenerateContentMapping(anthropicMessagesSourceModel, geminiNativeUpstreamModel, 'messages')
  ]
  const account = repositories.createAccount({
    providerCode: GEMINI_PROVIDER_CODE,
    providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
    name: 'OpenAI Anthropic 到 Gemini Native 显式桥接账号',
    type: 'api_key',
    status: 'active',
    credentials: {
      api_key: 'sk-account-model-mapping-to-gemini-native',
      base_url: 'https://generativelanguage.googleapis.com',
      supported_endpoint_modes: ['generate_content_json', 'generate_content_sse']
    },
    supportedModels: [geminiNativeUpstreamModel],
    modelMappings: mappings,
    groupId: group.id
  }, ownerAccess)
  assert.deepEqual(account.modelMappings, mappings, 'Gemini native 上游账号允许 OpenAI Chat / Responses / Anthropic Messages 显式桥接到 GenerateContent')
  assert.deepEqual(loadStoredMappings(account.id), [mappings[2], mappings[0], mappings[1]], 'OpenAI / Anthropic 到 Gemini native 映射应写入模型映射关系表')

  for (const item of [
    { model: sourceModel, family: 'chat_completions' as const, expected: mappings[0] },
    { model: sourceModel, family: 'responses' as const, expected: mappings[1] },
    { model: anthropicMessagesSourceModel, family: 'messages' as const, expected: mappings[2] }
  ]) {
    const runtimeAccount = repositories.listOpenAIAccountsForGroup(group.id, ownerAccess.systemAccountId, {
      requestedModel: item.model,
      requestedEndpointFamily: item.family
    }).find((candidate) => candidate.id === account.id)
    assert(runtimeAccount, `模型感知候选窗口应能按 ${item.family} source endpoint family 找到 Gemini native 映射账号`)
    const mapping = resolveOpenAIAccountModelMapping(runtimeAccount, item.model, item.family)
    assert.deepEqual(mapping, {
      sourceModel: item.expected.sourceModel,
      sourceEndpointFamily: item.expected.sourceEndpointFamily,
      upstreamModel: item.expected.upstreamModel,
      upstreamEndpointFamily: item.expected.upstreamEndpointFamily
    }, `运行时账号应能按 ${item.family} 下游协议命中 Gemini GenerateContent 上游映射`)
  }

  assert.throws(() => {
    const anthropicGroup = repositories.createGroup({
      name: '非 Gemini 档案拒绝 GenerateContent 目标分组',
      providerCode: ANTHROPIC_PROVIDER_CODE,
      providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID
    }, ownerAccess)
    repositories.createAccount({
      providerCode: ANTHROPIC_PROVIDER_CODE,
      providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
      name: 'Anthropic 档案不能作为 Gemini GenerateContent 目标',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-mapping-anthropic-to-gemini-target',
        base_url: 'https://api.anthropic.com',
        supported_endpoint_modes: ['messages_json']
      },
      supportedModels: [anthropicMessagesUpstreamModel],
      modelMappings: [
        toGeminiGenerateContentMapping(sourceModel, anthropicMessagesUpstreamModel, 'responses')
      ],
      groupId: anthropicGroup.id
    }, ownerAccess)
  }, /只有 Gemini native 协议档案可以把上游协议配置为 Gemini GenerateContent/, '非 Gemini native 档案不能选择右侧 Gemini GenerateContent')
}

function assertAnthropicMessagesToChatMapping(groupId: string): void {
  const messagesBridgeAccount = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    name: 'Messages 到 Chat 显式桥接账号',
    type: 'api_key',
    status: 'active',
    clientCompatibility: 'openai_standard',
    credentials: {
      api_key: 'sk-account-model-mapping-messages-to-chat',
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    supportedModels: [chatCompletionsUpstreamModel],
    modelMappings: [
      messagesToChatMapping(anthropicMessagesSourceModel, chatCompletionsUpstreamModel)
    ],
    groupId
  }, ownerAccess)
  assert.deepEqual(messagesBridgeAccount.modelMappings, [
    messagesToChatMapping(anthropicMessagesSourceModel, chatCompletionsUpstreamModel)
  ], 'OpenAI Chat 上游账号允许显式 Anthropic Messages 转 Chat Completions bridge')
  assert.deepEqual(loadStoredMappings(messagesBridgeAccount.id), [
    messagesToChatMapping(anthropicMessagesSourceModel, chatCompletionsUpstreamModel)
  ], 'Messages 转 Chat 映射应写入模型映射关系表')

  const runtimeAccount = repositories.listOpenAIAccountsForGroup(groupId, ownerAccess.systemAccountId, {
    requestedModel: anthropicMessagesSourceModel,
    requestedEndpointFamily: 'messages'
  }).find((item) => item.id === messagesBridgeAccount.id)
  assert(runtimeAccount, '模型感知候选窗口应能按 messages source endpoint family 找到映射账号')
  const mapping = resolveOpenAIAccountModelMapping(runtimeAccount, anthropicMessagesSourceModel, 'messages')
  assert.deepEqual(mapping, {
    sourceModel: anthropicMessagesSourceModel,
    sourceEndpointFamily: 'messages',
    upstreamModel: chatCompletionsUpstreamModel,
    upstreamEndpointFamily: 'chat_completions'
  }, '运行时账号应能按 Anthropic Messages 下游协议命中 Chat Completions 上游映射')
}

function assertGeminiNativeToAnthropicMessagesMapping(): void {
  const group = repositories.createGroup({
    name: 'Gemini Native 到 Anthropic Messages 映射回归分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID
  }, ownerAccess)
  const account = repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    name: 'Gemini Native 到 Anthropic Messages 显式桥接账号',
    type: 'api_key',
    status: 'active',
    credentials: {
      api_key: 'sk-account-model-mapping-gemini-to-messages',
      base_url: 'https://api.anthropic.com',
      supported_endpoint_modes: ['messages_json', 'messages_sse']
    },
    supportedModels: [anthropicMessagesUpstreamModel],
    modelMappings: [
      geminiGenerateContentToMessagesMapping(geminiGenerateContentSourceModel, anthropicMessagesUpstreamModel),
      geminiGenerateContentToMessagesMapping(geminiGenerateContentSourceModel, anthropicMessagesUpstreamModel, true, 'stream_generate_content')
    ],
    groupId: group.id
  }, ownerAccess)
  assert.deepEqual(account.modelMappings, [
    geminiGenerateContentToMessagesMapping(geminiGenerateContentSourceModel, anthropicMessagesUpstreamModel),
    geminiGenerateContentToMessagesMapping(geminiGenerateContentSourceModel, anthropicMessagesUpstreamModel, true, 'stream_generate_content')
  ], 'Anthropic Messages 上游账号允许显式 Gemini GenerateContent / StreamGenerateContent 转 Messages bridge')
  assert.deepEqual(loadStoredMappings(account.id), [
    geminiGenerateContentToMessagesMapping(geminiGenerateContentSourceModel, anthropicMessagesUpstreamModel),
    geminiGenerateContentToMessagesMapping(geminiGenerateContentSourceModel, anthropicMessagesUpstreamModel, true, 'stream_generate_content')
  ], 'Gemini Native 转 Messages 映射应写入模型映射关系表')

  const runtimeAccount = repositories.listOpenAIAccountsForGroup(group.id, ownerAccess.systemAccountId, {
    requestedModel: geminiGenerateContentSourceModel,
    requestedEndpointFamily: 'generate_content'
  }).find((item) => item.id === account.id)
  assert(runtimeAccount, '模型感知候选窗口应能按 generate_content source endpoint family 找到 Anthropic Messages 映射账号')
  const mapping = resolveOpenAIAccountModelMapping(runtimeAccount, geminiGenerateContentSourceModel, 'generate_content')
  assert.deepEqual(mapping, {
    sourceModel: geminiGenerateContentSourceModel,
    sourceEndpointFamily: 'generate_content',
    upstreamModel: anthropicMessagesUpstreamModel,
    upstreamEndpointFamily: 'messages'
  }, '运行时账号应能按 Gemini GenerateContent 下游协议命中 Anthropic Messages 上游映射')

  assert.throws(() => {
    repositories.createAccount({
      providerCode: ANTHROPIC_PROVIDER_CODE,
      providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
      name: 'Gemini Native 不能桥接到 Responses',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-mapping-gemini-to-responses',
        base_url: 'https://api.anthropic.com',
        supported_endpoint_modes: ['messages_json']
      },
      supportedModels: [anthropicMessagesUpstreamModel],
      modelMappings: [
        geminiGenerateContentToResponsesMapping(geminiGenerateContentSourceModel, anthropicMessagesUpstreamModel)
      ],
      groupId: group.id
    }, ownerAccess)
  }, /Gemini GenerateContent 下游协议当前只支持.*Chat Completions 或 Messages/, 'Gemini GenerateContent 下游协议不能桥接到 Responses 上游')

  const openAIGroup = repositories.createGroup({
    name: 'OpenAI 档案拒绝 Gemini 到 Messages 映射分组',
    providerCode: GPT_VENDOR_CODE
  }, ownerAccess)
  assert.throws(() => {
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      name: 'OpenAI 档案不能配置 Gemini 到 Messages',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-mapping-gemini-to-messages-on-openai',
        base_url: 'https://api.openai.com/v1',
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      supportedModels: [chatCompletionsUpstreamModel],
      modelMappings: [
        geminiGenerateContentToMessagesMapping(geminiGenerateContentSourceModel, anthropicMessagesUpstreamModel)
      ],
      groupId: openAIGroup.id
    }, ownerAccess)
  }, /Gemini GenerateContent 到 Anthropic Messages 桥接只能配置在 Anthropic Messages 协议档案账号上/, 'Gemini GenerateContent 到 Anthropic Messages 只能配置在 Anthropic 协议档案账号上')
}

function assertUnsupportedAnthropicMessagesMappingsRejected(groupId: string): void {
  assert.throws(() => {
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
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
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
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
    repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
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
  assert(result.accounts[0]?.messages.some((message) => message.includes('映射下游模型不在对应协议客户端模型池中')), '导入预览应在预览阶段暴露模型映射目录错误')
}

function assertImportPreviewRejectsNonNativeResponsesMapping(groupId: string): void {
  const result = previewAccountImport({
    type: 'juhe-ai-account-import',
    version: 1,
    accounts: [
      {
        name: '账号模型映射 Chat-only 非法 Responses 上游导入预览',
        providerCode: GPT_VENDOR_CODE,
        type: 'api_key',
        clientCompatibility: 'openai_standard',
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
        type: 'api_key',
        clientCompatibility: 'openai_standard',
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
  assert(result.accounts[0]?.messages.some((message) => message.includes('Anthropic Messages 下游协议当前只支持')), '导入预览应暴露 Messages 下游协议方向约束错误')
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
