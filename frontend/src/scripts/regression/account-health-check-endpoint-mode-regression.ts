import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  accountHealthCheckEndpointModeOptions,
  defaultAccountHealthCheckEndpointMode
} from '../../views/accounts/accountHealthCheckEndpointMode'
import { accountTestEndpointModesForAccount, accountTestEndpointModesForModel } from '../../views/accounts/accountEndpointModes'
import { providerModelsForProtocolProfile } from '../../views/accounts/accountEditFormPayload'

assert.equal(defaultAccountHealthCheckEndpointMode('gpt', 'profile_gpt_openai_v1', ['chat_json', 'responses_sse']), 'responses_sse')
assert.equal(defaultAccountHealthCheckEndpointMode('openai', 'profile_openai_openai_v1', ['responses_json', 'chat_json']), 'chat_json')
assert.equal(defaultAccountHealthCheckEndpointMode('anthropic', 'profile_anthropic_anthropic_v1', ['messages_json']), 'messages_json')
assert.equal(defaultAccountHealthCheckEndpointMode('gemini', 'profile_gemini_native_v1beta', ['generate_content_json']), 'generate_content_json')
assert.equal(defaultAccountHealthCheckEndpointMode('gpt', 'profile_gpt_openai_v1', ['chat_json']), 'chat_json')
assert.equal(defaultAccountHealthCheckEndpointMode('anthropic', 'profile_anthropic_anthropic_v1', ['messages_sse', 'message_token_counting']), 'messages_sse')
assert.equal(defaultAccountHealthCheckEndpointMode('gemini', 'profile_gemini_native_v1beta', ['generate_content_sse', 'count_tokens']), 'generate_content_sse')
assert.deepEqual(accountHealthCheckEndpointModeOptions(['responses_sse', 'chat_json', 'responses_json']), [
  { label: 'Chat Completions（JSON）', value: 'chat_json' },
  { label: 'Responses API（JSON）', value: 'responses_json' },
  { label: 'Responses API（Streaming）', value: 'responses_sse' }
])
assert.deepEqual(accountHealthCheckEndpointModeOptions([
  'messages_sse',
  'message_token_counting',
  'generate_content_sse',
  'count_tokens',
  'embed_content'
]), [
  { label: 'Messages API（Streaming）', value: 'messages_sse' },
  { label: 'GenerateContent（Streaming）', value: 'generate_content_sse' }
], '所有供应商的流式生成 mode 都应展示，工具型 endpoint mode 必须排除')
const draftEndpointModes = accountTestEndpointModesForAccount({
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  type: 'api_key',
  healthCheckEndpointMode: 'responses_sse',
  credentials: {
    supported_endpoint_modes: ['chat_sse', 'responses_sse', 'chat_json', 'responses_json']
  }
})
assert.equal(draftEndpointModes[0], 'responses_sse', '草稿账户默认测试必须优先使用保存的精确请求形态')
assert(draftEndpointModes.includes('responses_json'), '人工测试仍应保留显式选择其他已启用请求形态的能力')
const hybridEndpointModes = accountTestEndpointModesForAccount({
  providerCode: 'hybrid',
  providerProtocolProfileId: 'profile_hybrid_openai_chat_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  type: 'api_key',
  healthCheckEndpointMode: 'messages_sse',
  credentials: {
    supported_endpoint_modes: ['chat_json', 'messages_sse', 'generate_content_sse', 'count_tokens']
  }
})
assert.deepEqual(hybridEndpointModes, ['messages_sse', 'chat_json', 'generate_content_sse'], '混合供应商人工测试应保留全部已启用生成协议并排除工具接口')

const accountTestModelsSource = readFileSync(
  new URL('../../views/accounts/useAccountTestModels.ts', import.meta.url),
  'utf8'
)
assert.match(
  accountTestModelsSource,
  /testEndpointModes\.value = normalizeEndpointModes\(option\.testEndpointModes\)/,
  '模型请求形态必须直接使用选项响应携带的能力交集'
)
assert.doesNotMatch(
  accountTestModelsSource,
  /testModelCapabilities\(/,
  '模型切换不得再发起额外能力请求'
)
assert.doesNotMatch(
  accountTestModelsSource,
  /accountTestEndpointModesForModelOption/,
  '模型列表不得再承载已拆分的模型能力逻辑'
)

const imageEndpointModes = accountTestEndpointModesForModel({
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  type: 'api_key',
  credentials: { supported_endpoint_modes: ['responses_json', 'responses_sse'] }
}, undefined, { supportedApiProtocols: ['images'] })
assert.deepEqual(imageEndpointModes, ['images_json'], '纯图片模型必须直接收口到 Images API')
assert.deepEqual(providerModelsForProtocolProfile([
  { label: 'gpt-image-2', value: 'gpt-image-2', supportedApiProtocols: ['images'] }
], {
  id: 'profile_gpt_openai_v1',
  providerCode: 'gpt',
  name: 'GPT / OpenAI v1',
  enabled: true,
  protocolCode: 'openai',
  protocolVersion: 'v1',
  baseUrl: 'https://api.openai.com/v1',
  defaultHealthCheckModel: '',
  accountTypes: ['oauth', 'api_key'],
  capabilities: ['responses', 'chat'],
  endpointFamilies: [{ code: 'responses', name: 'Responses' }]
}, 'api_key').map((option) => option.value), ['gpt-image-2'], 'GPT API Key 模型选项必须保留 image-only 模型及其协议')

const healthCheckFieldSource = readFileSync(
  new URL('../../views/accounts/AccountHealthCheckModelField.vue', import.meta.url),
  'utf8'
)
assert.match(healthCheckFieldSource, /accountTestEndpointModesForModel/, '账户编辑检查协议必须联合模型目录能力计算')
assert.match(healthCheckFieldSource, /v-if="imageOnlyModel"[\s\S]*value="images_json"/, '纯图片模型必须显示只读 Images API')

const providerModelOptionsSource = readFileSync(
  new URL('../../views/accounts/useAccountProviderModelOptions.ts', import.meta.url),
  'utf8'
)
assert.match(providerModelOptionsSource, /supportedApiProtocols: item\.supportedApiProtocols/, '账户模型选项必须直接保留后端返回的目录协议')
assert.doesNotMatch(providerModelOptionsSource, /modelCapabilities\(/, '账户模型加载不得再逐模型请求能力接口')

for (const relativePath of [
  '../../views/accounts/accountBatchEditForm.ts',
  '../../views/accounts/accountSavePayload.ts'
]) {
  const source = readFileSync(new URL(relativePath, import.meta.url), 'utf8')
  assert.doesNotMatch(source, /检查协议必须选择.*非流式 JSON/, `${relativePath} 不得把健康检查限制为非流式 JSON`)
  assert.match(source, /检查请求形态必须选择.*JSON 或流式/, `${relativePath} 必须说明 JSON 和流式能力都可选择`)
}

console.log('前端 AI 账户健康检查请求形态回归通过')
