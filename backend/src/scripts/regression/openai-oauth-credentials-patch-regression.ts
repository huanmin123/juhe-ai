import { strict as assert } from 'node:assert'

import { buildSafeOpenAIOAuthCredentials } from '../../modules/openai-oauth/openai-oauth.routes.js'

const legacyErrorHandlingRules = [{
  enabled: true,
  name: 'OAuth 限流规则',
  priority: 10,
  status_codes: [429],
  action: 'rate_limited',
  reset_strategy: 'duration',
  duration_hours: 5
}]

const maliciousPatch = {
  error_handling_rules: legacyErrorHandlingRules,
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
assert(!Object.prototype.hasOwnProperty.call(tokenCredentials, 'error_handling_rules'), 'credentialsPatch 不再保留账号内嵌错误处理规则')

const fallbackCredentials = buildSafeOpenAIOAuthCredentials({
  accessToken: 'server-access-with-fallback',
  expiresIn: 3600,
  expiresAt: '2026-01-01T01:00:00.000Z',
  clientId: 'server-client'
}, maliciousPatch, { refreshToken: 'fallback-refresh' })

assert.equal(fallbackCredentials.refresh_token, 'fallback-refresh', 'token 响应缺 refresh_token 时只能使用服务端传入的 fallback refresh token')

console.log('OpenAI OAuth credentialsPatch 回归通过：客户端补丁不能覆盖服务端 token 凭据，也不能写入账号级错误规则')
