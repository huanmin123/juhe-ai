import { strict as assert } from 'node:assert'

import {
  buildReauthorizedOpenAIOAuthCredentials,
  buildSafeOpenAIOAuthCredentials
} from '../../modules/openai-oauth/openai-oauth.routes.js'

const legacyErrorHandlingRules = [{
  enabled: true,
  name: 'OAuth 限流规则',
  priority: 10,
  status_codes: [429],
  action: 'rate_limited',
  reset_strategy: 'duration',
  duration_hours: 5
}]

const accountResponseInspectionRules = [{
  enabled: true,
  name: 'OAuth 响应检查规则',
  priority: 11,
  match: {
    outputTextIncludes: ['响应污染']
  },
  action: 'retry_next_account'
}]

const maliciousPatch = {
  supported_endpoint_modes: ['responses_json', 'responses_sse'],
  service_tier_override: 'priority',
  reasoning_effort_override: 'high',
  error_handling_rules: legacyErrorHandlingRules,
  response_inspection_rules: accountResponseInspectionRules,
  access_token: 'attacker-access',
  refresh_token: 'attacker-refresh',
  expires_at: '2099-01-01T00:00:00.000Z',
  client_id: 'attacker-client',
  base_url: 'https://evil.example/v1'
} as unknown as Parameters<typeof buildSafeOpenAIOAuthCredentials>[1]

const tokenCredentials = buildSafeOpenAIOAuthCredentials({
  accessToken: 'server-access',
  refreshToken: 'server-refresh',
  expiresIn: 3600,
  expiresAt: '2026-01-01T00:00:00.000Z',
  clientId: 'server-client',
  email: 'owner@example.com'
}, maliciousPatch, { refreshToken: 'fallback-refresh' })

assert.equal(tokenCredentials.access_token, 'server-access', 'credentialsPatch 不能覆盖服务端交换得到的 access_token')
assert.equal(tokenCredentials.refresh_token, 'server-refresh', 'credentialsPatch 不能覆盖服务端交换得到的 refresh_token')
assert.equal(tokenCredentials.expires_at, '2026-01-01T00:00:00.000Z', 'credentialsPatch 不能覆盖服务端 token 过期时间')
assert.equal(tokenCredentials.client_id, 'server-client', 'credentialsPatch 不能覆盖 OAuth client_id')
assert.equal(tokenCredentials.base_url, 'https://api.openai.com/v1', 'credentialsPatch 不能覆盖 OpenAI base_url')
assert.deepEqual(tokenCredentials.supported_endpoint_modes, ['responses_json', 'responses_sse'], 'credentialsPatch 应保留 OAuth 上游接口能力')
assert.equal(tokenCredentials.service_tier_override, 'priority', 'credentialsPatch 应保留 OAuth 服务等级覆盖')
assert.equal(tokenCredentials.reasoning_effort_override, 'high', 'credentialsPatch 应保留 OAuth 思考级别覆盖')
assert.deepEqual(tokenCredentials.error_handling_rules, legacyErrorHandlingRules, 'credentialsPatch 应保留账户级错误处理规则')
assert.deepEqual(tokenCredentials.response_inspection_rules, accountResponseInspectionRules, 'credentialsPatch 应保留账户级响应检查规则')

const fallbackCredentials = buildSafeOpenAIOAuthCredentials({
  accessToken: 'server-access-with-fallback',
  expiresIn: 3600,
  expiresAt: '2026-01-01T01:00:00.000Z',
  clientId: 'server-client'
}, maliciousPatch, { refreshToken: 'fallback-refresh' })

assert.equal(fallbackCredentials.refresh_token, 'fallback-refresh', 'token 响应缺 refresh_token 时只能使用服务端传入的 fallback refresh token')

const reauthorizedCredentials = buildReauthorizedOpenAIOAuthCredentials(tokenCredentials, {
  accessToken: 'reauthorized-access',
  refreshToken: 'reauthorized-refresh',
  expiresIn: 7200,
  expiresAt: '2026-01-02T00:00:00.000Z',
  clientId: 'reauthorized-client'
})
assert.equal(reauthorizedCredentials.access_token, 'reauthorized-access', '重新授权必须替换服务端 token')
assert.equal(reauthorizedCredentials.service_tier_override, 'priority', '重新授权必须保留服务等级覆盖')
assert.equal(reauthorizedCredentials.reasoning_effort_override, 'high', '重新授权必须保留思考级别覆盖')
assert.deepEqual(reauthorizedCredentials.error_handling_rules, legacyErrorHandlingRules, '重新授权必须保留账户错误策略')

console.log('OpenAI OAuth credentialsPatch 回归通过：客户端补丁不能覆盖服务端 token 凭据，且保留请求覆盖、接口能力和账户策略')
