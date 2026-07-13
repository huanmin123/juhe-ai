import assert from 'node:assert/strict'

import {
  accountHealthCheckEndpointFamilyOptions,
  defaultAccountHealthCheckEndpointFamily
} from '../../views/accounts/accountHealthCheckEndpointFamily'

assert.equal(defaultAccountHealthCheckEndpointFamily('gpt', 'profile_gpt_openai_v1', ['chat_json', 'responses_json']), 'responses')
assert.equal(defaultAccountHealthCheckEndpointFamily('openai', 'profile_openai_openai_v1', ['responses_json', 'chat_json']), 'chat_completions')
assert.equal(defaultAccountHealthCheckEndpointFamily('anthropic', 'profile_anthropic_anthropic_v1', ['messages_json']), 'messages')
assert.equal(defaultAccountHealthCheckEndpointFamily('gemini', 'profile_gemini_native_v1beta', ['generate_content_json']), 'generate_content')
assert.equal(defaultAccountHealthCheckEndpointFamily('gpt', 'profile_gpt_openai_v1', ['chat_json']), 'chat_completions')
assert.deepEqual(accountHealthCheckEndpointFamilyOptions(['responses_sse', 'chat_json', 'responses_json']), [
  { label: 'Chat Completions（JSON）', value: 'chat_completions' },
  { label: 'Responses（JSON）', value: 'responses' }
])

console.log('前端 AI 账户健康检查协议族回归通过')
