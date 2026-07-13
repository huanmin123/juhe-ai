import assert from 'node:assert/strict'

import {
  accountHealthCheckEndpointFamilyOptions,
  defaultAccountHealthCheckEndpointFamily
} from '../../views/accounts/accountHealthCheckEndpointFamily'
import { accountTestEndpointModesForAccount } from '../../views/accounts/accountEndpointModes'

assert.equal(defaultAccountHealthCheckEndpointFamily('gpt', 'profile_gpt_openai_v1', ['chat_json', 'responses_json']), 'responses')
assert.equal(defaultAccountHealthCheckEndpointFamily('openai', 'profile_openai_openai_v1', ['responses_json', 'chat_json']), 'chat_completions')
assert.equal(defaultAccountHealthCheckEndpointFamily('anthropic', 'profile_anthropic_anthropic_v1', ['messages_json']), 'messages')
assert.equal(defaultAccountHealthCheckEndpointFamily('gemini', 'profile_gemini_native_v1beta', ['generate_content_json']), 'generate_content')
assert.equal(defaultAccountHealthCheckEndpointFamily('gpt', 'profile_gpt_openai_v1', ['chat_json']), 'chat_completions')
assert.deepEqual(accountHealthCheckEndpointFamilyOptions(['responses_sse', 'chat_json', 'responses_json']), [
  { label: 'Chat Completions（JSON）', value: 'chat_completions' },
  { label: 'Responses（JSON）', value: 'responses' }
])
const draftEndpointModes = accountTestEndpointModesForAccount({
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  type: 'api_key',
  healthCheckEndpointFamily: 'responses',
  credentials: {
    supported_endpoint_modes: ['chat_sse', 'responses_sse', 'chat_json', 'responses_json']
  }
})
assert.equal(draftEndpointModes[0], 'responses_json', '草稿账户默认测试必须优先使用保存协议族对应的 JSON 请求')
assert(draftEndpointModes.includes('responses_sse'), '人工测试仍应保留显式选择 SSE 请求的能力')

console.log('前端 AI 账户健康检查协议族回归通过')
