import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccountSummary, AccountUsageSummary } from '@/types/domain'
import {
  isAnthropicProtocolProfile,
  isGeminiProtocolProfile,
  isGatewaySupportedProtocolProfile,
  isOpenAIProtocolProfile
} from '@/shared/providerProtocol'
import {
  buildTestModelOptions,
  hasSingleProviderProfileForAccountSelection,
  isGatewaySupportedTestSelection
} from '../../views/accounts/accountDerivedState'
import { canTestAccount } from '../../views/accounts/accountRules'
import {
  accountEndpointModeLabel,
  accountTestEndpointModesForModel,
  validateAccountEndpointModes
} from '../../views/accounts/accountEndpointModes'

const openAIAccount = accountFixture({
  id: 'acct_openai_protocol_test',
  providerCode: 'gpt',
  protocolCode: 'openai',
  protocolVersion: 'v1'
})
const anthropicAccount = accountFixture({
  id: 'acct_anthropic_protocol_test',
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  protocolCode: 'anthropic',
  protocolVersion: 'v1',
  credentials: {
    supported_endpoint_modes: ['messages_json', 'messages_sse', 'message_token_counting']
  }
})
const unsupportedAccount = accountFixture({
  id: 'acct_gemini_protocol_test',
  providerCode: 'gemini',
  protocolCode: 'gemini',
  protocolVersion: 'v1'
})
const geminiAccount = accountFixture({
  id: 'acct_gemini_v1beta_protocol_test',
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  protocolCode: 'gemini',
  protocolVersion: 'v1beta',
  credentials: {
    supported_endpoint_modes: ['generate_content_json', 'generate_content_sse', 'count_tokens']
  }
})
const geminiOpenAIChatAccount = accountFixture({
  id: 'acct_gemini_openai_chat_protocol_test',
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_openai_chat_v1beta',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  credentials: {
    supported_endpoint_modes: ['chat_json', 'chat_sse']
  }
})

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const accountTestModelsSource = readFileSync(
  resolve(frontendRoot, 'src/views/accounts/useAccountTestModels.ts'),
  'utf8'
)
const updateSelectableTestModelSource = sourceSection(
  accountTestModelsSource,
  'function updateSelectableTestModel',
  'function resetTestModels'
)

assertTrue(isOpenAIProtocolProfile(openAIAccount), 'OpenAI v1 账户应识别为 OpenAI 协议档案')
assertTrue(isAnthropicProtocolProfile(anthropicAccount), 'Anthropic v1 账户应识别为 Anthropic 协议档案')
assertTrue(isGeminiProtocolProfile(geminiAccount), 'Gemini v1beta 账户应识别为 Gemini 协议档案')
assertTrue(isOpenAIProtocolProfile(geminiOpenAIChatAccount), 'Gemini OpenAI Chat 档案应识别为 OpenAI v1 协议档案')
assertTrue(isGatewaySupportedProtocolProfile(openAIAccount), 'OpenAI v1 账户应允许走前端账户测试入口')
assertTrue(isGatewaySupportedProtocolProfile(anthropicAccount), 'Anthropic v1 账户应允许走前端账户测试入口')
assertTrue(isGatewaySupportedProtocolProfile(geminiAccount), 'Gemini v1beta 账户应允许走前端账户测试入口')
assertTrue(isGatewaySupportedProtocolProfile(geminiOpenAIChatAccount), 'Gemini OpenAI Chat 账户应允许走前端账户测试入口')
assertFalse(isGatewaySupportedProtocolProfile(unsupportedAccount), '未接入网关测试的协议不应允许走前端账户测试入口')
assertEqual(accountEndpointModeLabel('images_json'), 'Images API', '图片模型测试形态必须显示为 Images API')
assertDeepEqual(
  accountTestEndpointModesForModel(openAIAccount, undefined, { supportedApiProtocols: ['images'] }),
  ['images_json'],
  '只声明 Images 的模型只能显示 Images API'
)
assertDeepEqual(
  accountTestEndpointModesForModel(openAIAccount, undefined, { supportedApiProtocols: ['responses', 'images'] }),
  ['responses_json', 'responses_sse', 'images_json'],
  '模型声明 Responses 与 Images 时必须完整显示两类可用请求形态'
)
assertDeepEqual(
  accountTestEndpointModesForModel(openAIAccount, undefined, { supportedApiProtocols: ['responses'] }),
  ['responses_json', 'responses_sse'],
  '模型协议选择只能来自目录能力，不能根据模型名猜测 Images'
)
assertFalse(
  accountTestEndpointModesForModel({ ...openAIAccount, type: 'oauth' }, undefined, { supportedApiProtocols: ['images'] }).includes('images_json'),
  'OAuth 账户没有 Images API Key 探针能力，不得显示 Images API'
)

assertTrue(canTestAccount(openAIAccount), 'OpenAI v1 正常账户应可测试')
assertTrue(canTestAccount(anthropicAccount), 'Anthropic API Key 正常账户应可测试')
assertTrue(canTestAccount(geminiAccount), 'Gemini API Key 正常账户应可测试')
assertTrue(canTestAccount(geminiOpenAIChatAccount), 'Gemini OpenAI Chat API Key 正常账户应可测试')
assertFalse(canTestAccount(unsupportedAccount), '未支持协议账户应不可测试')

assertEqual(
  validateAccountEndpointModes({ modes: [], type: 'api_key' }),
  '请至少选择一项上游接口能力',
  '空 endpoint mode 校验必须使用上游接口能力文案'
)
assertEqual(
  validateAccountEndpointModes({
    modes: ['responses_json'],
    allowedModes: ['chat_json', 'chat_sse'],
    type: 'api_key'
  }),
  '当前供应商协议不支持上游接口能力：Responses API (JSON)',
  '供应商 endpoint mode 校验必须使用上游接口能力文案'
)
assertEqual(
  validateAccountEndpointModes({ modes: ['chat_json', 'messages_json'], type: 'api_key' }),
  '不同协议的上游接口能力不能混选',
  '跨协议 endpoint mode 校验必须使用上游接口能力文案'
)
assertEqual(
  validateAccountEndpointModes({ modes: ['chat_json'], type: 'oauth' }),
  'OAuth 账户上游接口能力只能选择 Responses API (JSON) 或 Responses API (Streaming)',
  'OAuth endpoint mode 校验必须使用上游接口能力文案'
)
assertEqual(
  validateAccountEndpointModes({ modes: ['message_token_counting'], type: 'api_key' }),
  'Anthropic 账户上游接口能力至少需要启用 Messages API (JSON) 或 Messages API (Streaming)',
  'Anthropic 必选 endpoint mode 校验必须使用上游接口能力文案'
)
assertEqual(
  validateAccountEndpointModes({ modes: ['count_tokens'], type: 'api_key' }),
  'Gemini 上游接口能力至少需要启用 generateContent (JSON)、streamGenerateContent (SSE) 或 Interactions',
  'Gemini 必选 endpoint mode 校验必须使用上游接口能力文案'
)
assertEqual(
  validateAccountEndpointModes({ modes: ['responses_json'], type: 'oauth' }),
  'OAuth 账户上游接口能力必须启用 Responses API (Streaming)',
  'OAuth 流式 endpoint mode 校验必须使用上游接口能力文案'
)

assertTrue(isGatewaySupportedTestSelection(anthropicAccount), 'Anthropic 单账户选择应允许加载作用域默认模型信息')
assertTrue(isGatewaySupportedTestSelection(geminiAccount), 'Gemini 单账户选择应允许加载作用域默认模型信息')
assertFalse(hasSingleProviderProfileForAccountSelection([openAIAccount, anthropicAccount]), 'OpenAI 与 Anthropic 混合选择不应被视为同一供应商协议')
assertFalse(isGatewaySupportedTestSelection([openAIAccount, anthropicAccount]), 'OpenAI 与 Anthropic 混合选择不应加载单一供应商默认模型')
assertFalse(isGatewaySupportedTestSelection([anthropicAccount, unsupportedAccount]), '混入未支持协议时不应加载供应商默认模型')
assertFalse(isGatewaySupportedTestSelection([anthropicAccount, geminiAccount]), 'Anthropic 与 Gemini 混合选择不应加载单一供应商默认模型')
assertFalse(isGatewaySupportedTestSelection([geminiAccount, geminiOpenAIChatAccount]), 'Gemini 原生与 Gemini OpenAI Chat 混合选择不应被视为同一协议档案')

assertIncludes(accountTestModelsSource, 'response.testEndpointModes', '保存账户测试应保留后端返回的账户级请求形态作为兼容回退')
assertIncludes(accountTestModelsSource, 'account.healthCheckEndpointMode', '保存账户测试应把账户保存的精确健康检查请求形态排到首位')
assertIncludes(updateSelectableTestModelSource, 'normalizeEndpointModes(response.testEndpointModes)', '切换模型必须改用后端返回的模型与账户能力协议交集')
assertIncludes(updateSelectableTestModelSource, "input.testForm.testEndpointMode = testEndpointModes.value[0] ?? 'account_default'", '切换模型后必须清除前一模型遗留的无效检查协议')
assertNotIncludes(updateSelectableTestModelSource, 'supportedApiProtocols', '前端不能自行按模型协议标签二次推导检查协议')

assertDeepEqual(
  optionValues(buildTestModelOptions({
    ...anthropicAccount,
    supportedModels: ['claude-haiku-4-5', 'claude-opus-4-8']
  }, 'claude-haiku-4-5')),
  ['claude-haiku-4-5', 'claude-opus-4-8'],
  'Anthropic 测试模型选项只能使用账户支持模型并按默认模型排序'
)

console.log('账户测试协议回归通过：OpenAI、Anthropic 与 Gemini 协议均可测试，未支持协议仍被前端拦截')

function accountFixture(overrides: Partial<AccountSummary> = {}): AccountSummary {
  return {
    id: 'acct_protocol_test',
    providerCode: 'gpt',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    name: '协议测试账户',
    type: 'api_key',
    credentials: {},
    status: 'active',
    concurrencyLimit: 1,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    schedulable: true,
    todayUsage: emptyUsage(),
    usage: emptyUsage(),
    permissions: {
      canUse: true,
      canEdit: true,
      canDelete: true,
      canAuthorize: true,
      canViewCredentials: true,
      canBindToApiKey: true
    },
    ...overrides
  }
}

function emptyUsage(): AccountUsageSummary {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    cacheWriteTokens: 0,
    cacheWrite1hTokens: 0,
    cacheWriteCost: 0,
    thinkingTokens: 0,
    inputImageTokens: 0,
    outputImageTokens: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

function optionValues(options: Array<{ value: string }>): string[] {
  return options.map((option) => option.value)
}

function assertTrue(value: boolean, message: string): void {
  if (!value) throw new Error(message)
}

function assertFalse(value: boolean, message: string): void {
  if (value) throw new Error(message)
}

function assertDeepEqual(actual: unknown, expected: unknown, message: string): void {
  const actualJson = JSON.stringify(actual)
  const expectedJson = JSON.stringify(expected)
  if (actualJson !== expectedJson) {
    throw new Error(`${message}，实际 ${actualJson}，预期 ${expectedJson}`)
  }
}

function assertEqual<T>(actual: T, expected: T, message: string): void {
  if (!Object.is(actual, expected)) {
    throw new Error(`${message}，实际 ${String(actual)}，预期 ${String(expected)}`)
  }
}

function sourceSection(source: string, startMarker: string, endMarker: string): string {
  const start = source.indexOf(startMarker)
  const end = source.indexOf(endMarker, start)
  if (start < 0 || end < 0) {
    throw new Error(`无法提取源码片段：${startMarker} -> ${endMarker}`)
  }
  return source.slice(start, end)
}

function assertIncludes(source: string, expected: string, message: string): void {
  if (!source.includes(expected)) {
    throw new Error(`${message}，未找到 ${expected}`)
  }
}

function assertNotIncludes(source: string, unexpected: string, message: string): void {
  if (source.includes(unexpected)) {
    throw new Error(`${message}，不应包含 ${unexpected}`)
  }
}
