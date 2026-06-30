import type { AccountSummary, AccountUsageSummary, ProviderModelPricing } from '@/types/domain'
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

assertTrue(isOpenAIProtocolProfile(openAIAccount), 'OpenAI v1 账户应识别为 OpenAI 协议档案')
assertTrue(isAnthropicProtocolProfile(anthropicAccount), 'Anthropic v1 账户应识别为 Anthropic 协议档案')
assertTrue(isGeminiProtocolProfile(geminiAccount), 'Gemini v1beta 账户应识别为 Gemini 协议档案')
assertTrue(isOpenAIProtocolProfile(geminiOpenAIChatAccount), 'Gemini OpenAI Chat 档案应识别为 OpenAI v1 协议档案')
assertTrue(isGatewaySupportedProtocolProfile(openAIAccount), 'OpenAI v1 账户应允许走前端账户测试入口')
assertTrue(isGatewaySupportedProtocolProfile(anthropicAccount), 'Anthropic v1 账户应允许走前端账户测试入口')
assertTrue(isGatewaySupportedProtocolProfile(geminiAccount), 'Gemini v1beta 账户应允许走前端账户测试入口')
assertTrue(isGatewaySupportedProtocolProfile(geminiOpenAIChatAccount), 'Gemini OpenAI Chat 账户应允许走前端账户测试入口')
assertFalse(isGatewaySupportedProtocolProfile(unsupportedAccount), '未接入网关测试的协议不应允许走前端账户测试入口')

assertTrue(canTestAccount(openAIAccount), 'OpenAI v1 正常账户应可测试')
assertTrue(canTestAccount(anthropicAccount), 'Anthropic API Key 正常账户应可测试')
assertTrue(canTestAccount(geminiAccount), 'Gemini API Key 正常账户应可测试')
assertTrue(canTestAccount(geminiOpenAIChatAccount), 'Gemini OpenAI Chat API Key 正常账户应可测试')
assertFalse(canTestAccount(unsupportedAccount), '未支持协议账户应不可测试')

assertTrue(isGatewaySupportedTestSelection(anthropicAccount), 'Anthropic 单账户选择应允许加载供应商模型目录')
assertTrue(isGatewaySupportedTestSelection(geminiAccount), 'Gemini 单账户选择应允许加载供应商模型目录')
assertFalse(hasSingleProviderProfileForAccountSelection([openAIAccount, anthropicAccount]), 'OpenAI 与 Anthropic 混合选择不应被视为同一供应商协议')
assertFalse(isGatewaySupportedTestSelection([openAIAccount, anthropicAccount]), 'OpenAI 与 Anthropic 混合选择不应加载单一供应商模型目录')
assertFalse(isGatewaySupportedTestSelection([anthropicAccount, unsupportedAccount]), '混入未支持协议时不应加载供应商模型目录')
assertFalse(isGatewaySupportedTestSelection([anthropicAccount, geminiAccount]), 'Anthropic 与 Gemini 混合选择不应加载单一供应商模型目录')
assertFalse(isGatewaySupportedTestSelection([geminiAccount, geminiOpenAIChatAccount]), 'Gemini 原生与 Gemini OpenAI Chat 混合选择不应被视为同一协议档案')

assertDeepEqual(
  optionValues(buildTestModelOptions([
    providerModel('anthropic', 'claude-haiku-4-5'),
    providerModel('anthropic', 'claude-opus-4-8')
  ], anthropicAccount, 'claude-haiku-4-5')),
  ['claude-haiku-4-5', 'claude-opus-4-8'],
  'Anthropic 测试模型选项应合并默认模型和供应商模型目录'
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

function providerModel(providerCode: string, model: string): ProviderModelPricing {
  return {
    providerCode,
    model,
    source: 'built-in',
    scope: 'built_in',
    status: 'active',
    supportsPromptCaching: false,
    supportsServiceTier: false
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
