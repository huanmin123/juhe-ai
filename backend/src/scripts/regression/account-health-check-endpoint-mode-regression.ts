import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES,
  resolveDefaultHealthCheckEndpointMode,
  resolveHealthCheckEndpointMode
} from '../../domain/account-health-check-endpoint-mode.js'
import { normalizeGptHealthCheckCredentials } from '../maintenance/account-health-check-endpoint-mode-migration.js'

assert.deepEqual(ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES, [
  'chat_json',
  'chat_sse',
  'responses_json',
  'responses_sse',
  'messages_json',
  'messages_sse',
  'generate_content_json',
  'generate_content_sse'
])

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  enabledEndpointModes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
}), 'responses_sse')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  enabledEndpointModes: ['chat_json', 'chat_sse']
}), 'chat_json')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'openai',
  providerProtocolProfileId: 'profile_openai_openai_v1',
  enabledEndpointModes: ['responses_json', 'chat_json']
}), 'chat_json')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'deepseek',
  providerProtocolProfileId: 'profile_deepseek_openai_v1',
  enabledEndpointModes: ['chat_json', 'responses_json']
}), 'chat_json')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'glm',
  providerProtocolProfileId: 'profile_glm_coding_anthropic_v1',
  enabledEndpointModes: ['messages_json', 'messages_sse']
}), 'messages_json')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  enabledEndpointModes: ['messages_json', 'messages_sse']
}), 'messages_json')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  enabledEndpointModes: ['generate_content_json', 'generate_content_sse']
}), 'generate_content_json')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_openai_chat_v1beta',
  enabledEndpointModes: ['chat_json', 'chat_sse']
}), 'chat_json')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  enabledEndpointModes: ['messages_sse', 'generate_content_json', 'chat_json']
}), 'generate_content_json')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'openai',
  providerProtocolProfileId: 'profile_openai_openai_v1',
  enabledEndpointModes: ['chat_sse', 'responses_sse']
}), 'chat_sse')

assert.equal(resolveHealthCheckEndpointMode({
  value: 'responses_sse',
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  enabledEndpointModes: ['responses_sse']
}), 'responses_sse')

assert.equal(resolveHealthCheckEndpointMode({
  value: 'messages_sse',
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  enabledEndpointModes: ['messages_json', 'messages_sse', 'message_token_counting']
}), 'messages_sse')

assert.equal(resolveHealthCheckEndpointMode({
  value: 'generate_content_sse',
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  enabledEndpointModes: ['generate_content_json', 'generate_content_sse', 'count_tokens', 'embed_content']
}), 'generate_content_sse')

assert.throws(() => resolveHealthCheckEndpointMode({
  value: 'responses_json',
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  enabledEndpointModes: ['responses_sse']
}), /未启用/)

assert.throws(() => resolveHealthCheckEndpointMode({
  value: 'count_tokens',
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  enabledEndpointModes: ['count_tokens']
}), /请求形态无效/)

assert.throws(() => resolveDefaultHealthCheckEndpointMode({
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  enabledEndpointModes: ['message_token_counting']
}), /至少需要启用一个可用于健康检查的请求形态/)

assert.deepEqual(normalizeGptHealthCheckCredentials({}, 'oauth'), {
  credentials: {
    supported_endpoint_modes: ['responses_json', 'responses_sse']
  },
  changed: true
})

assert.deepEqual(normalizeGptHealthCheckCredentials({}, 'api_key'), {
  credentials: {
    supported_endpoint_modes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
  },
  changed: true
})

assert.deepEqual(normalizeGptHealthCheckCredentials({
  supported_endpoint_modes: ['messages_sse']
}, 'api_key'), {
  credentials: {
    supported_endpoint_modes: ['messages_sse', 'responses_sse']
  },
  changed: true
})

const alreadyNormalizedCredentials = {
  supported_endpoint_modes: ['responses_json', 'responses_sse']
}
assert.deepEqual(normalizeGptHealthCheckCredentials(alreadyNormalizedCredentials, 'oauth'), {
  credentials: alreadyNormalizedCredentials,
  changed: false
})
assert.throws(
  () => normalizeGptHealthCheckCredentials({ supported_endpoint_modes: 'responses_sse' }, 'oauth'),
  /必须是字符串数组/
)
assert.throws(
  () => normalizeGptHealthCheckCredentials({ supported_endpoint_modes: ['responses_json', 1] }, 'oauth'),
  /必须是非空字符串数组/
)

const migrationSource = readFileSync(
  new URL('../maintenance/account-health-check-endpoint-mode-migration.ts', import.meta.url),
  'utf8'
)
const migrationCliSource = readFileSync(
  new URL('../maintenance/migrate-account-health-check-endpoint-mode.ts', import.meta.url),
  'utf8'
)
assert.match(migrationSource, /BEGIN[\s\S]+LOCK TABLE juhe_business\.accounts IN ACCESS EXCLUSIVE MODE/)
assert.match(migrationSource, /WHERE id > \$1[\s\S]+ORDER BY id ASC[\s\S]+LIMIT \$2/, '迁移扫描必须使用 keyset 分批')
assert.match(migrationSource, /decryptJson\(encrypted\)/, '迁移必须通过应用 codec 解密凭据')
assert.match(migrationSource, /encryptJson\(normalized\.credentials\)/, '迁移必须通过应用 codec 重新加密凭据')
assert.match(migrationSource, /RENAME COLUMN health_check_endpoint_family TO health_check_endpoint_mode/)
assert.match(migrationSource, /await verifyExactRows\(client, batchSize\)[\s\S]+await client\.query\('COMMIT'\)/, '正式迁移必须在提交前校验结果')
assert.match(migrationSource, /await client\.query\('ROLLBACK'\)\.catch/, '迁移失败必须回滚整个事务')
assert.match(migrationCliSource, /mode: execute \? 'execute' : verify \? 'verify' : 'dry-run'/, '维护命令默认必须是 dry-run')
assert.match(migrationCliSource, /JUHE_AI_OFFLINE_MAINTENANCE_CONFIRMED/, '正式迁移必须显式确认已停服')
assert.match(migrationCliSource, /args\.includes\('--verify'\)/, '维护命令必须提供独立 verify 模式')

const draftServiceSource = readFileSync(new URL('../../modules/accounts/account-draft-test.service.ts', import.meta.url), 'utf8')
assert.equal(
  draftServiceSource.match(/resolveHealthCheckEndpointMode\(\{/g)?.length,
  2,
  '同步和异步草稿账户构造都必须按最终 endpoint modes 解析健康检查请求形态'
)

for (const relativePath of [
  '../../modules/background/account-api-key-cooldown-retest.service.ts',
  '../../modules/background/account-health-check.service.ts',
  '../../modules/background/account-quality-failure-precheck.service.ts',
  '../../modules/background/cooldown-account-retest.service.ts',
  '../../modules/background/normal-route-speed-first-recovery-probe.service.ts',
  '../../modules/gateway/runtime/account-side-effects.service.ts'
]) {
  const source = readFileSync(new URL(relativePath, import.meta.url), 'utf8')
  assert.match(source, /testEndpointMode:\s*account\.healthCheckEndpointMode/, `${relativePath} 必须直接使用账户保存的精确 mode`)
  assert.doesNotMatch(source, /healthCheckEndpointMode\s*\(/, `${relativePath} 不得再次从协议族推导请求形态`)
}

for (const relativePath of [
  '../../modules/accounts/account-test.service.ts',
  '../../modules/accounts/account-test-options.service.ts'
]) {
  const source = readFileSync(new URL(relativePath, import.meta.url), 'utf8')
  assert.match(source, /const defaultMode = account\.healthCheckEndpointMode/, `${relativePath} 默认测试必须使用账户保存的精确 mode`)
}

console.log('AI 账户健康检查请求形态领域回归通过')
