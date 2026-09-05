import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GEMINI_PROTOCOL_CODE,
  GEMINI_PROTOCOL_VERSION,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION
} from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import type { DispatchAccountSecret } from '../../storage/openai-account-selector.types.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-provider-request-overrides-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'provider-request-overrides-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

let ownerSystemAccountId = ''
const anthropicModel = 'claude-opus-4-6'
const geminiModel = 'gemini-request-overrides-regression'

const [
  databaseModule,
  repositories,
  catalogService,
  { applyProviderAccountRequestOverridesToBody },
  { anthropicProviderDriver },
  { geminiProviderDriver },
  { openAICompatibleProviderDriver }
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/model-pricing/model-catalog.service.js'),
  import('../../modules/providers/drivers/_shared/provider-request-overrides.js'),
  import('../../modules/providers/drivers/anthropic/driver.js'),
  import('../../modules/providers/drivers/gemini/driver.js'),
  import('../../modules/providers/drivers/openai-compatible/driver.js')
])

try {
  ownerSystemAccountId = repositories.createSystemAccount({
    username: 'provider_request_overrides_owner',
    displayName: 'ProviderRequestOverridesOwner',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  }).id
  catalogService.saveCustomProviderModel({
    providerCode: 'anthropic',
    model: anthropicModel,
    scope: 'personal',
    systemAccountId: ownerSystemAccountId,
    supportedApiProtocols: ['messages', 'message_token_counting'],
    supportedServiceTiers: ['priority'],
    supportedReasoningEfforts: ['low', 'high'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: ownerSystemAccountId
  })
  catalogService.saveCustomProviderModel({
    providerCode: 'gemini',
    model: geminiModel,
    scope: 'personal',
    systemAccountId: ownerSystemAccountId,
    supportedApiProtocols: ['chat_completions', 'generate_content', 'count_tokens', 'embed_content'],
    supportedReasoningEfforts: ['low', 'high'],
    inputUsdPer1M: 1,
    outputUsdPer1M: 2,
    actorSystemAccountId: ownerSystemAccountId
  })

  await assertSharedWireMappings(applyProviderAccountRequestOverridesToBody)

  const anthropicAccount = account({
    providerCode: 'anthropic',
    profileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
    model: anthropicModel,
    credentials: {
      service_tier_override: 'priority',
      reasoning_effort_override: 'high'
    }
  })
  const anthropicMessage = bodyJson((await anthropicProviderDriver.buildUpstreamRequestParts(
    gatewayPostRequest('/v1/messages', {
      model: anthropicModel,
      max_tokens: 16,
      messages: [{ role: 'user', content: 'hello' }]
    }),
    anthropicAccount,
    requestIdentity()
  )).body)
  assert.equal(anthropicMessage.service_tier, 'priority')
  assert.deepEqual(anthropicMessage.output_config, { effort: 'high' })

  const anthropicCountInput = {
    model: anthropicModel,
    messages: [{ role: 'user', content: 'count me' }]
  }
  const anthropicCount = bodyJson((await anthropicProviderDriver.buildUpstreamRequestParts(
    gatewayPostRequest('/v1/messages/count_tokens', anthropicCountInput),
    anthropicAccount,
    requestIdentity()
  )).body)
  assert.deepEqual(anthropicCount, anthropicCountInput, 'Anthropic token counting 不能注入生成请求覆盖字段')

  const geminiNativeAccount = account({
    providerCode: 'gemini',
    profileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
    protocolCode: GEMINI_PROTOCOL_CODE,
    protocolVersion: GEMINI_PROTOCOL_VERSION,
    model: geminiModel,
    credentials: { reasoning_effort_override: 'low' }
  })
  const geminiGenerate = bodyJson((await geminiProviderDriver.buildUpstreamRequestParts(
    gatewayPostRequest(`/v1beta/models/${geminiModel}:generateContent`, {
      contents: [{ role: 'user', parts: [{ text: 'hello' }] }]
    }),
    geminiNativeAccount,
    requestIdentity()
  )).body)
  assert.deepEqual(geminiGenerate.generationConfig, { thinkingConfig: { thinkingLevel: 'low' } })

  for (const endpoint of ['countTokens', 'embedContent']) {
    const input = endpoint === 'countTokens'
      ? { contents: [{ role: 'user', parts: [{ text: 'count me' }] }] }
      : { content: { parts: [{ text: 'embed me' }] } }
    const output = bodyJson((await geminiProviderDriver.buildUpstreamRequestParts(
      gatewayPostRequest(`/v1beta/models/${geminiModel}:${endpoint}`, input),
      geminiNativeAccount,
      requestIdentity()
    )).body)
    assert.deepEqual(output, input, `Gemini ${endpoint} 不能注入 generationConfig`)
  }

  const geminiOpenAIAccount = account({
    providerCode: 'gemini',
    profileId: GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    model: geminiModel,
    credentials: { reasoning_effort_override: 'high' }
  })
  const geminiOpenAIChat = bodyJson((await openAICompatibleProviderDriver.buildUpstreamRequestParts(
    gatewayPostRequest('/v1/chat/completions', {
      model: geminiModel,
      messages: [{ role: 'user', content: 'hello' }]
    }),
    geminiOpenAIAccount,
    requestIdentity()
  )).body)
  assert.equal(geminiOpenAIChat.reasoning_effort, 'high', 'Gemini OpenAI-compatible profile 必须使用 OpenAI chat wire')

  const controller = new AbortController()
  controller.abort()
  const largeGeminiRequest = gatewayPostRequest(`/v1beta/models/${geminiModel}:generateContent`, {
    contents: [{ role: 'user', parts: [{ text: 'x'.repeat(300_000) }] }]
  })
  await assert.rejects(
    () => geminiProviderDriver.buildUpstreamRequestParts(
      largeGeminiRequest,
      geminiNativeAccount,
      requestIdentity(),
      controller.signal
    ),
    /账户请求覆盖要求请求体是有效的 JSON 对象/,
    '非 GPT driver 必须把 AbortSignal 传入有界 JSON worker'
  )

  console.log('跨供应商账户请求覆盖回归通过：真实目录缓存、driver/profile、生成与工具端点、AbortSignal 边界均已覆盖')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertSharedWireMappings(
  applyOverrides: typeof import('../../modules/providers/drivers/_shared/provider-request-overrides.js')['applyProviderAccountRequestOverridesToBody']
): Promise<void> {
  const anthropicBody = await applyOverrides(Buffer.from(JSON.stringify({
    model: 'claude-test',
    speed: 'fast',
    thinking: { type: 'enabled', budget_tokens: 4096 },
    output_config: { existing: true }
  })), {
    account: account({
      providerCode: 'anthropic',
      profileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
      protocolCode: ANTHROPIC_PROTOCOL_CODE,
      protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
      model: 'claude-test',
      credentials: { service_tier_override: 'priority', reasoning_effort_override: 'high' }
    }),
    upstreamModel: 'claude-test',
    wireFormat: 'anthropic_messages',
    modelCapabilities: { supportedServiceTiers: ['priority'], supportedReasoningEfforts: ['low', 'high'] }
  })
  const anthropic = bodyJson(anthropicBody)
  assert.equal(anthropic.service_tier, 'priority')
  assert.deepEqual(anthropic.output_config, { existing: true, effort: 'high' })
  assert.equal(anthropic.speed, 'fast', 'Anthropic speed 必须原样保护，不能被通用覆盖控件接管')
  assert.deepEqual(anthropic.thinking, { type: 'enabled', budget_tokens: 4096 }, 'Anthropic thinking budget 必须原样保护')

  const geminiBody = await applyOverrides(JSON.stringify({
    generationConfig: { thinkingConfig: { thinkingBudget: 2048 }, temperature: 0.2 }
  }), {
    account: account({
      providerCode: 'gemini',
      profileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
      protocolCode: GEMINI_PROTOCOL_CODE,
      protocolVersion: GEMINI_PROTOCOL_VERSION,
      model: 'gemini-test',
      credentials: { reasoning_effort_override: 'low' }
    }),
    upstreamModel: 'gemini-test',
    wireFormat: 'gemini_generate_content',
    modelCapabilities: { supportedServiceTiers: [], supportedReasoningEfforts: ['low', 'high'] }
  })
  const gemini = bodyJson(geminiBody)
  assert.deepEqual(gemini.generationConfig, {
    thinkingConfig: { thinkingBudget: 2048, thinkingLevel: 'low' },
    temperature: 0.2
  })

  const geminiPriorityBody = await applyOverrides('{}', {
    account: account({
      providerCode: 'gemini',
      profileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
      protocolCode: GEMINI_PROTOCOL_CODE,
      protocolVersion: GEMINI_PROTOCOL_VERSION,
      model: 'gemini-test',
      credentials: { service_tier_override: 'priority' }
    }),
    upstreamModel: 'gemini-test',
    wireFormat: 'gemini_generate_content',
    modelCapabilities: { supportedServiceTiers: ['priority'], supportedReasoningEfforts: [] }
  })
  assert.equal(bodyJson(geminiPriorityBody).service_tier, 'priority', 'Gemini 服务等级覆盖必须写入原生 service_tier')
}

function account(input: {
  providerCode: 'anthropic' | 'gemini'
  profileId: string
  protocolCode: string
  protocolVersion: string
  model: string
  credentials: Record<string, unknown>
}): DispatchAccountSecret {
  const apiKey = 'sk-provider-request-overrides'
  return {
    id: `account-${input.providerCode}-${input.profileId}`,
    providerCode: input.providerCode,
    providerProtocolProfileId: input.profileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion,
    systemAccountId: ownerSystemAccountId,
    accountOwnerSystemAccountId: ownerSystemAccountId,
    groupOwnerSystemAccountId: ownerSystemAccountId,
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: 'Provider request overrides regression',
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 1,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    supportedModels: [input.model],
    healthCheckEndpointMode: input.providerCode === 'anthropic' ? 'messages_json' : 'generate_content_json',
    baseUrl: input.providerCode === 'anthropic'
      ? 'https://api.anthropic.com'
      : input.profileId === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID
        ? 'https://generativelanguage.googleapis.com/v1beta/openai'
        : 'https://generativelanguage.googleapis.com/v1beta',
    apiKey,
    streamFailureCount: 0,
    credentials: {
      api_key: apiKey,
      ...input.credentials
    }
  }
}

function gatewayPostRequest(originalUrl: string, body: Record<string, unknown>): Request {
  const headers = { 'content-type': 'application/json' }
  return {
    method: 'POST',
    path: originalUrl.split('?', 1)[0],
    originalUrl,
    headers,
    body,
    rawBody: Buffer.from(JSON.stringify(body), 'utf8'),
    header(name: string): string | undefined {
      return headers[name.toLowerCase() as keyof typeof headers]
    }
  } as unknown as Request
}

function requestIdentity() {
  return {
    systemAccountId: ownerSystemAccountId,
    groupId: 'group-provider-request-overrides'
  }
}

function bodyJson(body: Buffer | string | undefined): Record<string, unknown> {
  assert(body !== undefined, '上游请求体不应为空')
  const parsed = JSON.parse(Buffer.isBuffer(body) ? body.toString('utf8') : body) as unknown
  assert(parsed && typeof parsed === 'object' && !Array.isArray(parsed), '上游请求体应是 JSON object')
  return parsed as Record<string, unknown>
}
