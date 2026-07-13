import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  ACCOUNT_HEALTH_CHECK_ENDPOINT_FAMILIES,
  healthCheckEndpointMode,
  resolveDefaultHealthCheckEndpointFamily
} from '../../domain/account-health-check-endpoint-family.js'

assert.deepEqual(ACCOUNT_HEALTH_CHECK_ENDPOINT_FAMILIES, [
  'chat_completions',
  'responses',
  'messages',
  'generate_content'
])

assert.equal(healthCheckEndpointMode('chat_completions'), 'chat_json')
assert.equal(healthCheckEndpointMode('responses'), 'responses_json')
assert.equal(healthCheckEndpointMode('messages'), 'messages_json')
assert.equal(healthCheckEndpointMode('generate_content'), 'generate_content_json')

assert.equal(resolveDefaultHealthCheckEndpointFamily({
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  enabledEndpointModes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
}), 'responses')

assert.equal(resolveDefaultHealthCheckEndpointFamily({
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  enabledEndpointModes: ['chat_json', 'chat_sse']
}), 'chat_completions')

assert.equal(resolveDefaultHealthCheckEndpointFamily({
  providerCode: 'openai',
  providerProtocolProfileId: 'profile_openai_openai_v1',
  enabledEndpointModes: ['responses_json', 'chat_json']
}), 'chat_completions')

assert.equal(resolveDefaultHealthCheckEndpointFamily({
  providerCode: 'deepseek',
  providerProtocolProfileId: 'profile_deepseek_openai_v1',
  enabledEndpointModes: ['chat_json', 'responses_json']
}), 'chat_completions')

assert.equal(resolveDefaultHealthCheckEndpointFamily({
  providerCode: 'glm',
  providerProtocolProfileId: 'profile_glm_coding_anthropic_v1',
  enabledEndpointModes: ['messages_json', 'messages_sse']
}), 'messages')

assert.equal(resolveDefaultHealthCheckEndpointFamily({
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  enabledEndpointModes: ['messages_json', 'messages_sse']
}), 'messages')

assert.equal(resolveDefaultHealthCheckEndpointFamily({
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  enabledEndpointModes: ['generate_content_json', 'generate_content_sse']
}), 'generate_content')

assert.equal(resolveDefaultHealthCheckEndpointFamily({
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_openai_chat_v1beta',
  enabledEndpointModes: ['chat_json', 'chat_sse']
}), 'chat_completions')

assert.equal(resolveDefaultHealthCheckEndpointFamily({
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  enabledEndpointModes: ['messages_sse', 'generate_content_json', 'chat_json']
}), 'generate_content')

assert.throws(() => resolveDefaultHealthCheckEndpointFamily({
  providerCode: 'openai',
  providerProtocolProfileId: 'profile_openai_openai_v1',
  enabledEndpointModes: ['chat_sse', 'responses_sse']
}), /JSON/)

const draftServiceSource = readFileSync(new URL('../../modules/accounts/account-draft-test.service.ts', import.meta.url), 'utf8')
assert.equal(
  draftServiceSource.match(/resolveHealthCheckEndpointFamily\(\{/g)?.length,
  2,
  '同步和异步草稿账户构造都必须按最终 endpoint modes 解析健康检查协议族'
)

console.log('AI 账户健康检查协议族领域回归通过')
