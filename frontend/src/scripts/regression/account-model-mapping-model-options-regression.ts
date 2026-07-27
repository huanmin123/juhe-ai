import { readFileSync } from 'node:fs'

import {
  accountModelMappingEndpointFamilyProtocol,
  accountModelMappingSourceModelOptions,
  accountModelMappingUpstreamModelOptions,
  filterAccountModelMappingOptionsByEndpointFamily,
  type AccountModelMappingModelOption
} from '../../views/accounts/accountModelMappingModelOptions'
import {
  accountModelMappingProtocolValidationMessage,
  defaultAccountModelMappingSourceEndpointFamily,
  defaultAccountModelMappingUpstreamEndpointFamily,
  isAccountModelMappingProtocolAllowed,
  isAccountModelMappingSourceEndpointFamilyAllowed,
  shouldResetAccountModelMappingUpstreamEndpointFamily
} from '../../views/accounts/accountModelMappingProtocolMatrix'

const accountEditModalSource = readFileSync(new URL('../../views/accounts/AccountEditModal.vue', import.meta.url), 'utf8')
const accountApiKeySectionSource = readFileSync(new URL('../../views/accounts/AccountApiKeySection.vue', import.meta.url), 'utf8')
const accountEditFormSource = readFileSync(new URL('../../views/accounts/useAccountEditForm.ts', import.meta.url), 'utf8')
const accountSavePayloadSource = readFileSync(new URL('../../views/accounts/accountSavePayload.ts', import.meta.url), 'utf8')
const accountStrategySectionSource = readFileSync(new URL('../../views/accounts/AccountStrategySection.vue', import.meta.url), 'utf8')
const userHelpSource = readFileSync(new URL('../../../public/help/user/index.html', import.meta.url), 'utf8')
const adminHelpSource = readFileSync(new URL('../../../public/help/admin/index.html', import.meta.url), 'utf8')
const publicHelpSource = `${userHelpSource}\n${adminHelpSource}`

const options: AccountModelMappingModelOption[] = [
  { label: 'gpt-chat-only', value: 'gpt-chat-only', supportedApiProtocols: ['chat_completions'] },
  { label: 'gpt-responses-only', value: 'gpt-responses-only', supportedApiProtocols: ['responses'] },
  { label: 'gpt-dual', value: 'gpt-dual', supportedApiProtocols: ['chat_completions', 'responses'] },
  { label: 'claude-messages', value: 'claude-messages', supportedApiProtocols: ['messages', 'message_token_counting'] },
  { label: 'gemini-native', value: 'gemini-native', supportedApiProtocols: ['generate_content', 'stream_generate_content'] },
  { label: 'unknown-legacy', value: 'unknown-legacy' }
]

const ordinarySourceOptions = accountModelMappingSourceModelOptions({
  providerCode: 'deepseek',
  sourceEndpointFamily: 'responses',
  currentProviderOptions: options,
  openAIProtocolOptions: [],
  anthropicProtocolOptions: [],
  geminiProtocolOptions: []
})
assertDeepEqual(
  values(ordinarySourceOptions),
  values(options),
  '普通账户客户端模型必须使用当前供应商完整目录，不能按客户端或上游协议反向过滤'
)
assertDeepEqual(
  values(accountModelMappingSourceModelOptions({
    providerCode: 'hybrid',
    sourceEndpointFamily: 'messages',
    currentProviderOptions: options,
    openAIProtocolOptions: options,
    anthropicProtocolOptions: options,
    geminiProtocolOptions: options
  })),
  ['claude-messages'],
  'Hybrid 客户端模型必须按客户端协议使用对应全局协议池'
)
assertDeepEqual(
  values(accountModelMappingUpstreamModelOptions(options, 'responses')),
  ['gpt-responses-only', 'gpt-dual'],
  '上游模型必须按上游协议过滤'
)

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
assertIncludes(accountApiKeySectionSource, 'mode="multiple"', '支持模型必须只能从模型目录多选')
assertNotIncludes(accountApiKeySectionSource, 'mode="tags"', '支持模型不得开放任意模型 ID 输入')
assertMatch(
  accountEditFormSource,
  /function selectProvider[\s\S]*?resetForm\(providerCode, ''\)[\s\S]*?loadCurrentProviderModelOptions\(\)/,
  '切换供应商并带出默认账户类型后必须立即加载该供应商模型目录'
)
assertIncludes(
  accountEditModalSource,
  "credentialItem('supported_endpoint_modes', '上游接口能力'",
  '账户详情必须把 supported_endpoint_modes 展示为上游接口能力'
)
assertIncludes(accountStrategySectionSource, 'label="上游接口能力"', '账户表单必须使用上游接口能力标签')
assertIncludes(accountStrategySectionSource, 'placeholder="客户端模型"', '账户表单左侧必须明确使用客户端模型文案')
assertIncludes(accountStrategySectionSource, 'placeholder="上游模型"', '账户表单右侧必须明确使用上游模型文案')
assertIncludes(accountStrategySectionSource, '真实上游支持的接口形态', '账户表单提示必须解释真实上游能力语义')
assertNotIncludes(accountStrategySectionSource, '接口能力限制', '账户表单不得继续展示旧接口能力限制文案')
assertNotIncludes(accountStrategySectionSource, '可承接的接口形态', '账户表单不得把上游能力描述成客户端可承接请求')
assertIncludes(userHelpSource, '<h3>上游接口能力</h3>', '用户帮助必须使用上游接口能力标题')
assertIncludes(userHelpSource, '只声明账号真实上游支持的接口形态', '用户帮助必须解释真实上游能力边界')
assertIncludes(userHelpSource, '模型别名命中时按映射右侧的目标协议检查', '用户帮助必须解释模型映射按右侧上游能力检查')
assertNotIncludes(publicHelpSource, '接口能力限制', '公开帮助不得继续展示接口能力限制旧文案')
assertNotIncludes(publicHelpSource, '账号可承接的请求形态', '公开帮助不得展示派生的可承接请求形态')

const openAIProfile = { protocolCode: 'openai', protocolVersion: 'v1' }
const anthropicProfile = { protocolCode: 'anthropic', protocolVersion: 'v1' }
const geminiNativeProfile = {
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  protocolCode: 'gemini',
  protocolVersion: 'v1beta'
}

assertDefaultMappingFamilies(
  { providerProfile: openAIProfile, supportedEndpointModes: ['responses_json'] },
  'responses',
  'responses',
  'Responses-only OpenAI 新增映射'
)
assertDefaultMappingFamilies(
  { providerProfile: anthropicProfile, supportedEndpointModes: ['messages_json'] },
  'messages',
  'messages',
  'Anthropic 新增映射'
)
assertDefaultMappingFamilies(
  { providerProfile: geminiNativeProfile, supportedEndpointModes: ['generate_content_sse'] },
  'generate_content',
  'generate_content',
  'Gemini native 新增映射'
)

assertEqual(
  isAccountModelMappingSourceEndpointFamilyAllowed('chat_completions', {
    providerProfile: openAIProfile,
    supportedEndpointModes: ['responses_json']
  }),
  true,
  '来源 Chat Completions 是否可选只能看 profile 和转换白名单，不能被右侧 Chat 上游能力缺失禁用'
)
assertEqual(
  shouldResetAccountModelMappingUpstreamEndpointFamily({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'chat_completions',
    context: { providerProfile: openAIProfile, supportedEndpointModes: ['responses_json'] }
  }),
  false,
  '目标 Chat 能力缺失但转换结构仍合法时，watcher 不应静默重写映射目标族'
)
assertEqual(
  shouldResetAccountModelMappingUpstreamEndpointFamily({
    sourceEndpointFamily: 'chat_completions',
    upstreamEndpointFamily: 'responses',
    context: { providerProfile: openAIProfile, supportedEndpointModes: ['responses_json'] }
  }),
  true,
  '普通 OpenAI Chat -> Responses 转换结构非法时，watcher 仍应重置目标族'
)

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
assertIncludes(accountStrategySectionSource, 'upstreamEndpointFamilyDisabled(mapping.sourceEndpointFamily, option.value, mapping.enabled)', '前端目标协议下拉联动必须区分启用与停用映射')
assertIncludes(accountStrategySectionSource, 'shouldResetAccountModelMappingUpstreamEndpointFamily', '前端 watcher 必须仅按转换结构决定是否重写目标族')
assertIncludes(accountStrategySectionSource, 'defaultAccountModelMappingSourceEndpointFamily', '主编辑器新增映射和结构 fallback 必须使用共享默认来源族')
assertNotIncludes(accountStrategySectionSource, "sourceEndpointFamily: OPENAI_CHAT_COMPLETIONS_FAMILY", '主编辑器新增映射不得硬编码 Chat 来源族')
assertIncludes(accountStrategySectionSource, 'sourceEndpointFamilyBaseOptions.filter', '来源协议下拉必须直接过滤当前账号不支持的协议')
assertIncludes(accountStrategySectionSource, 'upstreamEndpointFamilyBaseOptions.filter', '目标协议下拉必须直接过滤当前账号不支持的协议')
assertNotIncludes(accountStrategySectionSource, 'disabled: !isAccountModelMappingSourceEndpointFamilyAllowed', '来源协议下拉不应保留不可选择项')
assertNotIncludes(accountStrategySectionSource, 'disabled: upstreamEndpointFamilyDisabled', '目标协议下拉不应保留不可选择项')
assertNotIncludes(accountStrategySectionSource, "mapping.sourceModel = ''", '模型目录异步刷新不得静默清空来源模型')
assertNotIncludes(accountStrategySectionSource, "mapping.upstreamModel = ''", '模型目录异步刷新不得静默清空上游模型')
assertNotIncludes(accountStrategySectionSource, 'sourceModelOptionsFingerprint', '协议结构 watcher 不应订阅异步模型目录指纹')
assertEqual(
  accountStrategySectionSource.indexOf('label="上游接口能力"') < accountStrategySectionSource.indexOf('label="账号模型别名"'),
  true,
  '上游接口能力必须展示在账号模型别名之前'
)

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

function assertDefaultMappingFamilies(
  context: Parameters<typeof defaultAccountModelMappingSourceEndpointFamily>[0],
  expectedSource: ReturnType<typeof defaultAccountModelMappingSourceEndpointFamily>,
  expectedUpstream: ReturnType<typeof defaultAccountModelMappingUpstreamEndpointFamily>,
  message: string
): void {
  const source = defaultAccountModelMappingSourceEndpointFamily(context)
  assertEqual(source, expectedSource, `${message}来源族错误`)
  assertEqual(
    defaultAccountModelMappingUpstreamEndpointFamily(source, context),
    expectedUpstream,
    `${message}目标族错误`
  )
}
