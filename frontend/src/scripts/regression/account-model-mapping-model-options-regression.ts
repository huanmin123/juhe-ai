import { readFileSync } from 'node:fs'

import {
  accountModelMappingEndpointFamilyProtocol,
  filterAccountModelMappingOptionsByEndpointFamily,
  type AccountModelMappingModelOption
} from '../../views/accounts/accountModelMappingModelOptions'
import {
  accountModelMappingProtocolValidationMessage,
  isAccountModelMappingProtocolAllowed
} from '../../views/accounts/accountModelMappingProtocolMatrix'

const accountEditModalSource = readFileSync(new URL('../../views/accounts/AccountEditModal.vue', import.meta.url), 'utf8')
const accountSavePayloadSource = readFileSync(new URL('../../views/accounts/accountSavePayload.ts', import.meta.url), 'utf8')
const accountStrategySectionSource = readFileSync(new URL('../../views/accounts/AccountStrategySection.vue', import.meta.url), 'utf8')

const options: AccountModelMappingModelOption[] = [
  { label: 'gpt-chat-only', value: 'gpt-chat-only', supportedApiProtocols: ['chat_completions'] },
  { label: 'gpt-responses-only', value: 'gpt-responses-only', supportedApiProtocols: ['responses'] },
  { label: 'gpt-dual', value: 'gpt-dual', supportedApiProtocols: ['chat_completions', 'responses'] },
  { label: 'claude-messages', value: 'claude-messages', supportedApiProtocols: ['messages', 'message_token_counting'] },
  { label: 'gemini-native', value: 'gemini-native', supportedApiProtocols: ['generate_content', 'stream_generate_content'] },
  { label: 'unknown-legacy', value: 'unknown-legacy' }
]

assertDeepEqual(
  values(filterAccountModelMappingOptionsByEndpointFamily(options, 'chat_completions')),
  ['gpt-chat-only', 'gpt-dual'],
  'Chat Completions 来源只能展示声明支持 Chat 的模型'
)
assertDeepEqual(
  values(filterAccountModelMappingOptionsByEndpointFamily(options, 'responses')),
  ['gpt-responses-only', 'gpt-dual'],
  'Responses 来源只能展示声明支持 Responses 的模型'
)
assertDeepEqual(
  values(filterAccountModelMappingOptionsByEndpointFamily(options, 'messages')),
  ['claude-messages'],
  'Messages 来源只能展示 Anthropic Messages 模型'
)
assertDeepEqual(
  values(filterAccountModelMappingOptionsByEndpointFamily(options, 'generate_content')),
  ['gemini-native'],
  'Gemini GenerateContent 来源只能展示 Gemini native 模型'
)
assertDeepEqual(
  values(filterAccountModelMappingOptionsByEndpointFamily(options, 'stream_generate_content')),
  ['gemini-native'],
  'Gemini StreamGenerateContent 来源只能展示支持流式 Gemini native 的模型'
)
assertEqual(accountModelMappingEndpointFamilyProtocol('chat_completions'), 'chat_completions', 'Chat 协议映射错误')
assertEqual(accountModelMappingEndpointFamilyProtocol('responses'), 'responses', 'Responses 协议映射错误')
assertEqual(accountModelMappingEndpointFamilyProtocol('messages'), 'messages', 'Messages 协议映射错误')
assertIncludes(accountEditModalSource, 'for (const item of props.form.supportedModels)', '账号模型别名右侧下拉只能从账户支持模型构建')
assertNotIncludes(accountEditModalSource, 'buildAccountModelMappingUpstreamOptions', '账号模型别名右侧下拉不应合并整个供应商模型目录')

const openAIProfile = { protocolCode: 'openai', protocolVersion: 'v1' }
const anthropicProfile = { protocolCode: 'anthropic', protocolVersion: 'v1' }
const geminiNativeProfile = {
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  protocolCode: 'gemini',
  protocolVersion: 'v1beta'
}

assertMatch(
  accountModelMappingProtocolValidationMessage({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'chat_completions',
    enabled: true,
    context: { providerProfile: openAIProfile, supportedEndpointModes: ['responses_json'] }
  }) ?? '',
  /Chat Completions.*上游接口能力/,
  '前端启用的 Responses -> Chat 映射必须按右侧要求 Chat 上游能力'
)
assertEqual(
  accountModelMappingProtocolValidationMessage({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'chat_completions',
    enabled: false,
    context: { providerProfile: openAIProfile, supportedEndpointModes: ['responses_json'] }
  }),
  undefined,
  '前端应允许在 Chat 上游能力缺失时保留停用的 Responses -> Chat 映射'
)
assertEqual(
  isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'chat_completions',
    enabled: true,
    context: { providerProfile: openAIProfile, supportedEndpointModes: ['chat_sse'] }
  }),
  true,
  '前端不应使用左侧 Responses 能力限制 Responses -> Chat 映射'
)
assertMatch(
  accountModelMappingProtocolValidationMessage({
    sourceEndpointFamily: 'messages',
    upstreamEndpointFamily: 'messages',
    enabled: true,
    context: { providerProfile: anthropicProfile, supportedEndpointModes: ['message_token_counting'] }
  }) ?? '',
  /Messages.*上游接口能力/,
  '前端 Messages 目标族不能把 token-counting 当作请求能力'
)
assertMatch(
  accountModelMappingProtocolValidationMessage({
    sourceEndpointFamily: 'stream_generate_content',
    upstreamEndpointFamily: 'generate_content',
    enabled: true,
    context: { providerProfile: geminiNativeProfile, supportedEndpointModes: ['count_tokens'] }
  }) ?? '',
  /Gemini GenerateContent.*上游接口能力/,
  '前端 Gemini 目标族必须要求 GenerateContent JSON 或 SSE 上游能力'
)
assertIncludes(accountSavePayloadSource, 'enabled: item.enabled', '前端保存校验必须把映射启停状态传给统一协议矩阵')
assertIncludes(accountStrategySectionSource, 'mapping.upstreamEndpointFamily, mapping.enabled', '前端目标协议选项联动必须区分启用与停用映射')

console.log('账号模型别名协议模型选项回归通过')

function values(options: AccountModelMappingModelOption[]): string[] {
  return options.map((option) => option.value)
}

function assertEqual<T>(actual: T, expected: T, message: string): void {
  if (!Object.is(actual, expected)) {
    throw new Error(`${message}，实际 ${String(actual)}，预期 ${String(expected)}`)
  }
}

function assertDeepEqual(actual: unknown, expected: unknown, message: string): void {
  const actualJson = JSON.stringify(actual)
  const expectedJson = JSON.stringify(expected)
  if (actualJson !== expectedJson) {
    throw new Error(`${message}，实际 ${actualJson}，预期 ${expectedJson}`)
  }
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

function assertMatch(actual: string, expected: RegExp, message: string): void {
  if (!expected.test(actual)) {
    throw new Error(`${message}，实际 ${actual}，预期匹配 ${expected}`)
  }
}
