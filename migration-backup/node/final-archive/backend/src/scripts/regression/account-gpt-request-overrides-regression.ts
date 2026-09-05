import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  assertAccountGptRequestOverridesSupportedByCatalog
} from '../../modules/accounts/account-gpt-request-overrides.validation.js'
import type { ProviderModelCatalogItem } from '../../modules/model-pricing/model-catalog.service.js'
import { normalizeAccountCredentialsForWrite } from '../../storage/account-credentials-normalization.js'
import { runtimeOpenAIAccountCredentials } from '../../storage/openai-account-selector.repository.js'
import { applyGptAccountRequestOverridesToBody } from '../../modules/providers/drivers/gpt/request-override-body.js'
import { applyProviderAccountRequestOverridesToBody } from '../../modules/providers/drivers/_shared/provider-request-overrides.js'
import { serializeGatewayJsonObject } from '../../modules/gateway/request/serialized-json-body.js'
import { parseGatewayRequestJsonBody } from '../../modules/gateway/request/json-parser.js'
import { prepareAnthropicMessagesBodyForAttempt } from '../../modules/gateway/upstream/body-preparation.js'
import { setGptRequestOverrideModelCapabilitiesResolverForTest } from '../../modules/providers/drivers/gpt/request-override-capabilities.js'
import { buildGatewayUpstreamRequestParts } from '../../modules/providers/drivers/registry.js'
import type { DispatchAccountSecret } from '../../storage/openai-account-selector.types.js'

const catalog = [
  modelItem('gpt-a', ['priority', 'flex'], ['low', 'high']),
  modelItem('gpt-b', ['priority'], ['low', 'medium'])
]

assert.doesNotThrow(() => assertAccountGptRequestOverridesSupportedByCatalog({
  accountType: 'api_key',
  overrides: {
    serviceTier: 'priority',
    reasoningEffort: 'low'
  },
  supportedModels: ['gpt-a', 'gpt-b'],
  catalog
}), 'API Key 覆盖值可由不同已选模型分别声明')

assert.doesNotThrow(() => assertAccountGptRequestOverridesSupportedByCatalog({
  accountType: 'api_key',
  overrides: { serviceTier: 'flex' },
  supportedModels: ['gpt-a', 'gpt-b'],
  catalog
}), '至少一个已选模型支持 Flex 时必须允许保存账户级覆盖')

assert.doesNotThrow(() => assertAccountGptRequestOverridesSupportedByCatalog({
  accountType: 'api_key',
  overrides: { reasoningEffort: 'high' },
  supportedModels: ['gpt-a', 'gpt-b'],
  catalog
}), '至少一个已选模型支持目标思考级别时必须允许保存账户级覆盖')

assert.throws(() => assertAccountGptRequestOverridesSupportedByCatalog({
  accountType: 'api_key',
  overrides: { serviceTier: 'default' },
  supportedModels: ['gpt-unknown'],
  catalog
}), /没有模型声明服务等级覆盖/, '只有未知支持模型时不能保存无能力依据的覆盖')

assert.doesNotThrow(() => assertAccountGptRequestOverridesSupportedByCatalog({
  accountType: 'api_key',
  overrides: { serviceTier: 'priority' },
  supportedModels: ['gpt-a', 'gpt-unknown'],
  catalog
}), '未知模型不应阻断其他已知已选模型声明的覆盖能力')

assert.doesNotThrow(() => assertAccountGptRequestOverridesSupportedByCatalog({
  accountType: 'oauth',
  overrides: { serviceTier: 'flex' },
  supportedModels: ['gpt-a'],
  catalog
}), 'OAuth 与 API Key 必须按相同模型能力允许 Flex')

assert.doesNotThrow(() => assertAccountGptRequestOverridesSupportedByCatalog({
  providerCode: 'gemini',
  accountType: 'api_key',
  overrides: { serviceTier: 'priority' },
  supportedModels: ['gemini-test'],
  catalog: [{
    providerCode: 'gemini',
    model: 'gemini-test',
    supportedServiceTiers: ['priority'],
    supportedReasoningEfforts: ['low']
  } as unknown as ProviderModelCatalogItem]
}), 'Gemini Priority 服务等级有官方 service_tier wire 映射时应允许保存')

assert.doesNotThrow(() => assertAccountGptRequestOverridesSupportedByCatalog({
  providerCode: 'gemini',
  accountType: 'api_key',
  overrides: { reasoningEffort: 'low' },
  supportedModels: ['gemini-test'],
  catalog: [{
    providerCode: 'gemini',
    model: 'gemini-test',
    supportedServiceTiers: [],
    supportedReasoningEfforts: ['low']
  } as unknown as ProviderModelCatalogItem]
}), 'Gemini thinking level 有明确 wire 映射时应允许保存')

assert.throws(() => assertAccountGptRequestOverridesSupportedByCatalog({
  providerCode: 'deepseek',
  accountType: 'api_key',
  overrides: { reasoningEffort: 'high' },
  supportedModels: ['deepseek-test'],
  catalog: [{
    providerCode: 'deepseek',
    model: 'deepseek-test',
    supportedServiceTiers: [],
    supportedReasoningEfforts: ['high']
  } as unknown as ProviderModelCatalogItem]
}), /供应商 deepseek 没有可确认的账户请求覆盖 wire 映射/, '目录声明不能绕过 provider driver 映射边界')

const normalizedApiKeyCredentials = normalizeAccountCredentialsForWrite('api_key', {
  api_key: 'sk-regression',
  base_url: 'https://api.openai.com/v1',
  supported_endpoint_modes: ['responses_json'],
  service_tier_override: 'priority',
  reasoning_effort_override: 'low'
}, {
  providerCode: 'gpt',
  accountType: 'api_key',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1'
})
assert.equal(normalizedApiKeyCredentials.service_tier_override, 'priority')
assert.equal(normalizedApiKeyCredentials.reasoning_effort_override, 'low')

const runtimeCredentials = runtimeOpenAIAccountCredentials({
  account_id: 'runtime-account',
  service_tier_override: 'priority',
  reasoning_effort_override: 'high',
  api_key: 'must-not-leak-through-runtime-projection'
})
assert.equal(runtimeCredentials.service_tier_override, 'priority', '运行时凭据投影必须保留服务档位覆盖')
assert.equal(runtimeCredentials.reasoning_effort_override, 'high', '运行时凭据投影必须保留思考级别覆盖')
assert.equal(runtimeCredentials.api_key, undefined, '运行时凭据投影不能因此放宽密钥字段白名单')

const normalizedOAuthCredentials = normalizeAccountCredentialsForWrite('oauth', {
  refresh_token: 'refresh-regression',
  base_url: 'https://api.openai.com/v1',
  supported_endpoint_modes: ['responses_json'],
  service_tier_override: 'flex'
}, {
  providerCode: 'gpt',
  accountType: 'oauth',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1'
})
assert.equal(normalizedOAuthCredentials.service_tier_override, 'flex', 'OAuth 凭据归一化必须保留 Flex 覆盖')

assert.throws(() => normalizeAccountCredentialsForWrite('api_key', {
  api_key: 'sk-regression',
  base_url: 'https://api.openai.com/v1',
  supported_endpoint_modes: ['responses_json'],
  reasoning_effort_override: 'ultra'
}, {
  providerCode: 'gpt',
  accountType: 'api_key',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1'
}), /思考级别覆盖无效/, 'Ultra 只能用于 Codex 模型能力，不能作为账户 wire 覆盖')

const gatewayAccount = {
  id: 'gpt-request-override-gateway-regression',
  credentials: {
    service_tier_override: 'priority',
    reasoning_effort_override: 'high'
  }
} as unknown as DispatchAccountSecret

const originalJsonParse = JSON.parse
let serializedBuilderParseCount = 0
let structuredGptOverrideBody: Buffer | string | undefined
let structuredProviderOverrideBody: Buffer | string | undefined
try {
  JSON.parse = ((...args: Parameters<typeof JSON.parse>) => {
    serializedBuilderParseCount += 1
    return originalJsonParse(...args)
  }) as typeof JSON.parse
  structuredGptOverrideBody = await applyGptAccountRequestOverridesToBody(serializeGatewayJsonObject({
    model: 'gpt-a',
    input: 'x'.repeat(512 * 1024)
  }), {
    credentials: gatewayAccount.credentials,
    account: gatewayAccount,
    endpointFamily: 'responses',
    modelCapabilities: {
      supportedServiceTiers: ['priority'],
      supportedReasoningEfforts: ['high']
    }
  })
  structuredProviderOverrideBody = await applyProviderAccountRequestOverridesToBody(serializeGatewayJsonObject({
    model: 'gpt-a',
    messages: [{ role: 'user', content: 'x'.repeat(512 * 1024) }]
  }), {
    account: gatewayAccount,
    upstreamModel: 'gpt-a',
    wireFormat: 'openai_chat',
    modelCapabilities: {
      supportedServiceTiers: ['priority'],
      supportedReasoningEfforts: ['high']
    }
  })
} finally {
  JSON.parse = originalJsonParse
}
assert.equal(serializedBuilderParseCount, 0, 'builder 已绑定结构化对象时 GPT/provider 账户覆盖不得再次 JSON.parse')
assert.equal((originalJsonParse(String(structuredGptOverrideBody)) as Record<string, unknown>).service_tier, 'priority')
assert.equal((originalJsonParse(String(structuredProviderOverrideBody)) as Record<string, unknown>).reasoning_effort, 'high')

const requestScopedRawBody = Buffer.from(JSON.stringify({
  model: 'gpt-a',
  input: 'request-scoped'
}), 'utf8')
const requestScopedRequest = {
  method: 'POST',
  path: '/v1/responses',
  originalUrl: '/v1/responses',
  headers: { 'content-type': 'application/json' },
  rawBody: requestScopedRawBody
} as never
let requestScopedParseCount = 0
try {
  JSON.parse = ((...args: Parameters<typeof JSON.parse>) => {
    requestScopedParseCount += 1
    return originalJsonParse(...args)
  }) as typeof JSON.parse
  await parseGatewayRequestJsonBody(requestScopedRequest)
  await applyGptAccountRequestOverridesToBody(requestScopedRawBody, {
    credentials: gatewayAccount.credentials,
    account: gatewayAccount,
    endpointFamily: 'responses',
    modelCapabilities: {
      supportedServiceTiers: ['priority'],
      supportedReasoningEfforts: ['high']
    }
  })
} finally {
  JSON.parse = originalJsonParse
}
assert.equal(requestScopedParseCount, 1, '请求级物化后的原始 Buffer 进入账户覆盖时必须复用同一结构对象')

const unsupportedProviderBody = Buffer.from(JSON.stringify({
  model: 'gpt-a',
  messages: [{ role: 'user', content: 'x'.repeat(512 * 1024) }]
}), 'utf8')
const originalJsonStringify = JSON.stringify
let unsupportedProviderParseCount = 0
let unsupportedProviderStringifyCount = 0
let unsupportedProviderResult: Buffer | string | undefined
try {
  JSON.parse = ((...args: Parameters<typeof JSON.parse>) => {
    unsupportedProviderParseCount += 1
    return originalJsonParse(...args)
  }) as typeof JSON.parse
  JSON.stringify = ((...args: Parameters<typeof JSON.stringify>) => {
    unsupportedProviderStringifyCount += 1
    return originalJsonStringify(...args)
  }) as typeof JSON.stringify
  unsupportedProviderResult = await applyProviderAccountRequestOverridesToBody(unsupportedProviderBody, {
    account: gatewayAccount,
    upstreamModel: 'gpt-a',
    wireFormat: 'openai_chat',
    modelCapabilities: {
      supportedServiceTiers: [],
      supportedReasoningEfforts: []
    }
  })
} finally {
  JSON.parse = originalJsonParse
  JSON.stringify = originalJsonStringify
}
assert.equal(unsupportedProviderResult, unsupportedProviderBody, '能力不支持任何账户覆盖时必须原样复用 Body')
assert.equal(unsupportedProviderParseCount, 0, '能力不支持覆盖时不得反解析 builder Body')
assert.equal(unsupportedProviderStringifyCount, 0, '能力不支持覆盖时不得重新序列化大 Body')

const anthropicPreparedInput = structuredProviderOverrideBody as Buffer
let anthropicPreparationParseCount = 0
let anthropicPreparedBody: Buffer | string | undefined
try {
  JSON.parse = ((...args: Parameters<typeof JSON.parse>) => {
    anthropicPreparationParseCount += 1
    return originalJsonParse(...args)
  }) as typeof JSON.parse
  anthropicPreparedBody = prepareAnthropicMessagesBodyForAttempt({} as never, new Headers({
    'anthropic-version': '2023-06-01',
    'x-api-key': 'test-key'
  }), 'https://api.anthropic.com/v1/messages', anthropicPreparedInput)
} finally {
  JSON.parse = originalJsonParse
}
assert.equal(anthropicPreparationParseCount, 0, '覆盖后的结构化 Buffer 进入 Anthropic preparation 时不得再次 JSON.parse')
assert.ok(Buffer.isBuffer(anthropicPreparedBody))

const passthroughLargeBody = Buffer.from(originalJsonStringify({
  model: 'gpt-a',
  messages: [{ role: 'user', content: 'x'.repeat(512 * 1024) }]
}), 'utf8')
let passthroughParseCount = 0
try {
  JSON.parse = ((...args: Parameters<typeof JSON.parse>) => {
    passthroughParseCount += 1
    return originalJsonParse(...args)
  }) as typeof JSON.parse
  await applyProviderAccountRequestOverridesToBody(passthroughLargeBody, {
    account: gatewayAccount,
    upstreamModel: 'gpt-a',
    wireFormat: 'openai_chat',
    modelCapabilities: {
      supportedServiceTiers: ['priority'],
      supportedReasoningEfforts: ['high']
    }
  })
  await applyProviderAccountRequestOverridesToBody(passthroughLargeBody, {
    account: { ...gatewayAccount, id: 'gpt-request-override-switched-account' },
    upstreamModel: 'gpt-a',
    wireFormat: 'openai_chat',
    modelCapabilities: {
      supportedServiceTiers: ['priority'],
      supportedReasoningEfforts: ['high']
    }
  })
} finally {
  JSON.parse = originalJsonParse
}
assert.equal(passthroughParseCount, 1, '原样透传大 Body 跨账户覆盖时只能完整解析一次')

const unknownCapabilitiesBody = JSON.parse(String(await applyGptAccountRequestOverridesToBody(JSON.stringify({
  messages: [],
  service_tier: 'flex',
  reasoning_effort: 'low'
}), {
    credentials: gatewayAccount.credentials,
    account: gatewayAccount,
    endpointFamily: 'chat_completions'
  })))
assert.equal(unknownCapabilitiesBody.service_tier, 'flex', '目标模型能力未知时必须保留客户端服务等级')
assert.equal(unknownCapabilitiesBody.reasoning_effort, 'low', '目标模型能力未知时必须保留客户端思考级别')

const reasoningOnlyBody = JSON.parse(String(await applyGptAccountRequestOverridesToBody(JSON.stringify({
  model: 'gpt-a',
  messages: [],
  service_tier: 'flex',
  reasoning_effort: 'low'
}), {
    credentials: gatewayAccount.credentials,
    account: gatewayAccount,
    endpointFamily: 'chat_completions',
    modelCapabilities: {
      supportedServiceTiers: [],
      supportedReasoningEfforts: ['high']
    }
  })))
assert.equal(reasoningOnlyBody.service_tier, 'flex', '服务等级覆盖不支持时必须保留客户端字段')
assert.equal(reasoningOnlyBody.reasoning_effort, 'high', '思考级别覆盖支持时必须独立生效')

const serviceTierOnlyBody = JSON.parse(String(await applyGptAccountRequestOverridesToBody(JSON.stringify({
  model: 'gpt-b',
  messages: [],
  service_tier: 'flex',
  reasoning_effort: 'low'
}), {
  credentials: gatewayAccount.credentials,
  account: gatewayAccount,
  endpointFamily: 'chat_completions',
  modelCapabilities: {
    supportedServiceTiers: ['priority'],
    supportedReasoningEfforts: []
  }
})))
assert.equal(serviceTierOnlyBody.service_tier, 'priority', '服务等级覆盖支持时必须独立生效')
assert.equal(serviceTierOnlyBody.reasoning_effort, 'low', '思考级别覆盖不支持时必须保留客户端字段')

setGptRequestOverrideModelCapabilitiesResolverForTest(() => ({
  supportedServiceTiers: ['priority'],
  supportedReasoningEfforts: ['high']
}))
try {
  const requestBody = {
    model: 'gpt-a',
    input: 'hello',
    service_tier: 'flex',
    reasoning: { effort: 'low', summary: 'auto' }
  }
  const request = {
    method: 'POST',
    path: '/v1/responses',
    originalUrl: '/v1/responses',
    headers: { 'content-type': 'application/json' },
    body: requestBody,
    rawBody: Buffer.from(JSON.stringify(requestBody))
  } as never
  const parts = await buildGatewayUpstreamRequestParts(request, {
    ...gatewayAccount,
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_openai_v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    type: 'api_key',
    apiKey: 'sk-request-override-regression',
    baseUrl: 'https://api.openai.com/v1'
  }, {
    systemAccountId: 'sys-request-override-regression',
    groupId: 'group-request-override-regression'
  })
  const body = JSON.parse(String(parts.body)) as Record<string, unknown>
  assert.equal(body.service_tier, 'priority', 'GPT API Key driver 必须应用最终模型支持的服务等级覆盖')
  assert.deepEqual(body.reasoning, { effort: 'high', summary: 'auto' }, 'GPT API Key driver 必须应用最终模型支持的 Responses 思考级别覆盖')
} finally {
  setGptRequestOverrideModelCapabilitiesResolverForTest(undefined)
}

const gptDriverSource = readFileSync(resolve('src/modules/providers/drivers/gpt/driver.ts'), 'utf8')
assert.match(gptDriverSource, /normalizeGptRequestOverrideCapabilitiesForGateway/, 'GPT OAuth driver 必须复用 request-override-body 的统一网关错误归一化')
assert.doesNotMatch(gptDriverSource, /from '\.\/request-override-capabilities\.js'/, 'GPT driver 不得在统一归一化范围外直接解析覆盖能力')

console.log('GPT 账户请求覆盖回归通过：配置按能力合集开放、OAuth 支持 Flex、运行时按最终模型逐字段生效')

function modelItem(
  model: string,
  supportedServiceTiers: Array<'priority' | 'flex'>,
  supportedReasoningEfforts: Array<'none' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'>
): ProviderModelCatalogItem {
  return {
    providerCode: 'gpt',
    model,
    supportedServiceTiers,
    supportedReasoningEfforts
  } as ProviderModelCatalogItem
}
