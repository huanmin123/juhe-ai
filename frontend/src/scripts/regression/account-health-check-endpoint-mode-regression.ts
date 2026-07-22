import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  accountHealthCheckEndpointModeOptions,
  defaultAccountHealthCheckEndpointMode
} from '../../views/accounts/accountHealthCheckEndpointMode'
import { accountTestEndpointModesForAccount } from '../../views/accounts/accountEndpointModes'

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
  /api\.accounts\.testModelCapabilities\(/,
  '管理端人工测试切换模型时必须直接请求账户模型能力'
)
assert.match(
  accountTestModelsSource,
  /api\.myAccounts\.testModelCapabilities\(/,
  '个人端人工测试切换模型时必须直接请求账户模型能力'
)
assert.match(
  accountTestModelsSource,
  /testEndpointModes\.value = normalizeEndpointModes\(response\.testEndpointModes\)/,
  '模型请求形态必须以后端返回的模型能力为准'
)
assert.match(
  accountTestModelsSource,
  /modelAbortController = controller\s+testEndpointModes\.value = \[\]\s+input\.testForm\.testEndpointMode = 'account_default'/,
  '模型切换后必须先清理前一个模型的请求形态'
)
assert.doesNotMatch(
  accountTestModelsSource,
  /accountTestEndpointModesForModelOption/,
  '模型列表不得再承载已拆分的模型能力逻辑'
)

for (const relativePath of [
  '../../views/accounts/accountBatchEditForm.ts',
  '../../views/accounts/accountSavePayload.ts'
]) {
  const source = readFileSync(new URL(relativePath, import.meta.url), 'utf8')
  assert.doesNotMatch(source, /检查协议必须选择.*非流式 JSON/, `${relativePath} 不得把健康检查限制为非流式 JSON`)
  assert.match(source, /检查请求形态必须选择.*JSON 或流式/, `${relativePath} 必须说明 JSON 和流式能力都可选择`)
}

console.log('前端 AI 账户健康检查请求形态回归通过')
