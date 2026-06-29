import {
  accountModelMappingEndpointFamilyProtocol,
  filterAccountModelMappingOptionsByEndpointFamily,
  type AccountModelMappingModelOption
} from '../../views/accounts/accountModelMappingModelOptions'

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
