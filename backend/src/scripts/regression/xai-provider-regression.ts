import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  XAI_OPENAI_V1_PROFILE_ID,
  XAI_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import {
  DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS,
  DEFAULT_PROVIDER_SEEDS,
  XAI_OPENAI_V1_PROFILE_SEED,
  XAI_PROVIDER_SEED
} from '../../storage/schema-defaults.js'
import type { DispatchAccountSecret } from '../../storage/openai-account-selector.types.js'
import { normalizeAccountCredentialsForWrite } from '../../storage/account-credentials-normalization.js'
import { providerModelSupportsProtocolProfile } from '../../storage/account-model-normalization.js'
import {
  accountSupportsGatewayRequest,
  buildGatewayUpstreamRequestParts,
  buildGatewayUpstreamUrlsForAccount,
  providerDriverForAccount,
  usageSemanticForProfile
} from '../../modules/providers/drivers/registry.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-xai-provider-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'xai-provider-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
assert.equal(XAI_PROVIDER_CODE, 'xai')
assert.equal(XAI_OPENAI_V1_PROFILE_ID, 'profile_xai_openai_v1')
assert(DEFAULT_PROVIDER_SEEDS.some((seed) => seed.code === XAI_PROVIDER_CODE), '默认供应商种子应包含 xAI')
assert(DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS.some((seed) => seed.id === XAI_OPENAI_V1_PROFILE_ID), '默认协议档案种子应包含 xAI OpenAI v1')
assert.deepEqual(XAI_PROVIDER_SEED.defaultSupportedModels, ['grok-4.3'], 'xAI 默认模型应使用当前官方文本模型')
assert.deepEqual(XAI_OPENAI_V1_PROFILE_SEED.accountTypes, ['api_key'], 'xAI 官方 API 档案只允许 API Key')
assert.deepEqual(
  XAI_OPENAI_V1_PROFILE_SEED.endpointFamilies,
  ['chat_completions', 'responses'],
  'xAI OpenAI v1 档案应原生支持 Chat Completions 与 Responses'
)
assert.equal(XAI_OPENAI_V1_PROFILE_SEED.baseUrl, 'https://api.x.ai/v1')
assert.equal(
  providerModelSupportsProtocolProfile(['chat_completions', 'responses'], XAI_OPENAI_V1_PROFILE_SEED),
  true,
  'xAI 文本模型应进入 OpenAI v1 账户模型池'
)
assert.equal(
  providerModelSupportsProtocolProfile(['images'], XAI_OPENAI_V1_PROFILE_SEED),
  false,
  'xAI image-only 模型不得通过 OpenAI v1 文本档案的后端保存校验'
)

const normalizedCredentials = normalizeAccountCredentialsForWrite('api_key', {
  api_key: 'xai-upstream-key',
  base_url: XAI_OPENAI_V1_PROFILE_SEED.baseUrl
}, {
  accountType: 'api_key',
  providerCode: XAI_PROVIDER_CODE,
  providerProtocolProfileId: XAI_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION
})
assert.deepEqual(
  normalizedCredentials.supported_endpoint_modes,
  ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'],
  'xAI API Key 默认应启用 Chat 与 Responses 的 JSON/SSE 能力'
)

const account = xaiAccount()
assert.equal(providerDriverForAccount(account)?.id, 'xai', 'xAI 档案应由独立 xAI driver 处理')
assert.equal(usageSemanticForProfile(account), 'openai', 'xAI 文本 usage 应复用 OpenAI 语义')

const chatRequest = openAIRequest('/v1/chat/completions?trace=xai-chat', {
  model: 'grok-4.3',
  messages: [{ role: 'user', content: 'hello xAI' }],
  stream: false
})
assert.deepEqual(
  buildGatewayUpstreamUrlsForAccount(account, chatRequest),
  ['https://api.x.ai/v1/chat/completions?trace=xai-chat']
)
assert.equal(accountSupportsGatewayRequest(chatRequest, account), true)

const responsesRequest = openAIRequest('/v1/responses?trace=xai-responses', {
  model: 'grok-4.3',
  input: 'hello xAI responses',
  stream: true
})
assert.deepEqual(
  buildGatewayUpstreamUrlsForAccount(account, responsesRequest),
  ['https://api.x.ai/v1/responses?trace=xai-responses']
)
assert.equal(accountSupportsGatewayRequest(responsesRequest, account), true)

const requestParts = await buildGatewayUpstreamRequestParts(responsesRequest, account, {
  systemAccountId: 'sys_admin',
  groupId: 'grp_xai'
})
assert.equal(requestParts.headers.get('authorization'), 'Bearer xai-upstream-key', 'xAI API Key 应使用 Bearer Authorization')
assert.equal(requestParts.headers.get('x-api-key'), null, 'xAI 上游不应收到 Anthropic x-api-key')
assert.equal(JSON.parse(String(requestParts.body)).model, 'grok-4.3', 'xAI Responses 请求体应保留模型')

const anthropicRequest = openAIRequest('/v1/messages', {
  model: 'grok-4.3',
  messages: [{ role: 'user', content: 'not supported' }]
})
assert.deepEqual(buildGatewayUpstreamUrlsForAccount(account, anthropicRequest), [], 'xAI 档案不应承接 Anthropic Messages 原生请求')
assert.equal(accountSupportsGatewayRequest(anthropicRequest, account), false)

const oauthAccount = { ...account, type: 'oauth' }
assert.equal(accountSupportsGatewayRequest(chatRequest, oauthAccount), false, 'xAI driver 不应放宽到订阅 OAuth 账户')

const gptGroup = repositories.createGroup({
  providerCode: GPT_VENDOR_CODE,
  name: 'GPT 默认模型写入回归分组'
}, access)
const gptAccount = repositories.createAccount({
  providerCode: GPT_VENDOR_CODE,
  providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
  name: 'GPT 默认模型写入回归账户',
  type: 'api_key',
  credentials: {
    api_key: 'sk-gpt-default-model-write',
    base_url: 'https://api.openai.com/v1'
  },
  groupId: gptGroup.id
}, access)
assert.equal(gptAccount.supportedModels?.includes('gpt-image-2'), true, 'GPT OpenAI v1 账户默认写入必须保留可透传的 gpt-image-2')

const xaiGroup = repositories.createGroup({
  providerCode: XAI_PROVIDER_CODE,
  name: 'xAI 文本模型写入回归分组'
}, access)
const xaiCreateInput = {
  providerCode: XAI_PROVIDER_CODE,
  providerProtocolProfileId: XAI_OPENAI_V1_PROFILE_ID,
  name: 'xAI 文本模型写入回归账户',
  type: 'api_key',
  credentials: {
    api_key: 'xai-write-regression-key',
    base_url: 'https://api.x.ai/v1'
  },
  groupId: xaiGroup.id
}
assert.throws(() => repositories.createAccount({
  ...xaiCreateInput,
  name: 'xAI 图片模型创建拒绝回归账户',
  supportedModels: ['grok-imagine-image'],
  healthCheckModel: 'grok-imagine-image'
}, access), /账户支持模型不在供应商模型目录中：grok-imagine-image/, 'xAI 图片专用模型不得写入 Chat/Responses 文本账户')
const savedXaiAccount = repositories.createAccount({
  ...xaiCreateInput,
  supportedModels: ['grok-4.3'],
  healthCheckModel: 'grok-4.3'
}, access)
assert.throws(() => repositories.updateAccount(savedXaiAccount.id, {
  supportedModels: ['grok-imagine-image'],
  healthCheckModel: 'grok-imagine-image'
}, access), /账户支持模型不在供应商模型目录中：grok-imagine-image/, 'xAI 图片专用模型不得通过更新写入 Chat/Responses 文本账户')

console.log('xAI provider 回归通过：seed、API Key 凭据、Chat/Responses 路由、Bearer 请求构造和跨协议隔离符合预期')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function xaiAccount(): DispatchAccountSecret {
  return {
    id: 'acc_xai_api_key',
    providerCode: XAI_PROVIDER_CODE,
    providerProtocolProfileId: XAI_OPENAI_V1_PROFILE_ID,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    systemAccountId: 'sys_admin',
    accountOwnerSystemAccountId: 'sys_admin',
    groupOwnerSystemAccountId: 'sys_admin',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: 'xAI API Key',
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 20,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    supportedEndpointModes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'],
    supportedModels: ['grok-4.3'],
    healthCheckModel: 'grok-4.3',
    healthCheckEndpointMode: 'responses_json',
    baseUrl: 'https://api.x.ai/v1',
    apiKey: 'xai-upstream-key',
    streamFailureCount: 0,
    credentials: {
      api_key: 'xai-upstream-key',
      base_url: 'https://api.x.ai/v1',
      supported_endpoint_modes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
    }
  }
}

function openAIRequest(originalUrl: string, body: Record<string, unknown>): Request {
  const rawBody = Buffer.from(JSON.stringify(body))
  return {
    method: 'POST',
    originalUrl,
    path: originalUrl.split('?', 1)[0],
    headers: {
      accept: body.stream === true ? 'text/event-stream' : 'application/json',
      authorization: 'Bearer downstream-key',
      'content-type': 'application/json'
    },
    body,
    rawBody
  } as unknown as Request
}
