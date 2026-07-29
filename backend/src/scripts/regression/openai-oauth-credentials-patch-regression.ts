import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import {
  buildReauthorizedOpenAIOAuthCredentials,
  buildSafeOpenAIOAuthCredentials
} from '../../modules/openai-oauth/openai-oauth.routes.js'

const oauthRoutesSource = readFileSync(new URL('../../modules/openai-oauth/openai-oauth.routes.ts', import.meta.url), 'utf8')
const rotationRepositorySource = readFileSync(new URL('../../storage/oauth-credential-rotation.repository.ts', import.meta.url), 'utf8')
const refreshServiceSource = readFileSync(new URL('../../modules/openai-oauth/openai-oauth-access-token-refresh.service.ts', import.meta.url), 'utf8')
const dbServiceHandlersSource = readFileSync(new URL('../../modules/db-service/db-service-handlers.ts', import.meta.url), 'utf8')
assert.match(oauthRoutesSource, /service_tier_override: z\.enum\(\['default', 'priority', 'flex'\]\)/, 'OAuth 创建接口 schema 必须接受 Flex 覆盖')
assert.match(oauthRoutesSource, /expectedConfigRevision: z\.number\(\)\.int\(\)\.min\(1\)/, '重新授权必须携带配置版本')
assert.doesNotMatch(oauthRoutesSource, /updateAccountAsync\(account\.id, \{\s*credentials/, '重新授权不得复用账户全量更新')
assert.match(oauthRoutesSource, /oauthRotationReceipt\(updated\)/, '重新授权只返回最小 mutation 回执')
assert.match(rotationRepositorySource, /SELECT id, system_account_id, provider_code, provider_protocol_profile_id,/, '重新授权必须使用用途专用窄投影')
assert.doesNotMatch(rotationRepositorySource, /todayUsage|usage_stats|permissions|supported_models/i, '重新授权投影不得读取列表统计或关系字段')
assert.match(rotationRepositorySource, /const credentialAssignments = credentialsChanged[\s\S]*credentials_encrypted = \?,[\s\S]*const revisionAssignment = configRevisionChanged[\s\S]*config_revision = config_revision \+ 1/, '凭据实际变化时只追加凭据列并推进配置版本')
assert.match(rotationRepositorySource, /const recoveryAssignments = recoverFailureState[\s\S]*expiredAtRecovery \? 'disabled' : 'pending_test'[\s\S]*last_error_code = \$\{expiredAtRecovery/, '受管 OAuth 错误恢复必须独立按需追加运行态字段并保护过期账户')
assert.match(rotationRepositorySource, /AND system_account_id = \?[\s\S]*AND provider_code = \?[\s\S]*AND config_revision = \?/, '重新授权写入必须在 SQL 中同时下推 owner/provider/CAS')
assert.match(refreshServiceSource, /findOAuthCredentialRotationAccountAsync\(/, '后台刷新必须使用 OAuth 专用窄查询')
assert.match(refreshServiceSource, /rotateOAuthCredentialsAsync\(/, '后台刷新必须使用 OAuth 专用字段级 CAS 写入')
assert.doesNotMatch(refreshServiceSource, /updateOpenAIOAuthCredentialsIfCurrent\(/, '后台刷新不得退回通用凭据更新路径')
assert.doesNotMatch(refreshServiceSource, /findAccountForTest\(/, '后台刷新不得读取账户测试宽 DTO')
assert.match(dbServiceHandlersSource, /case 'update_openai_oauth_credentials': \{[\s\S]*findOAuthCredentialRotationAccountAsync\([\s\S]*rotateOAuthCredentialsAsync\(/, 'DB service 异步路径必须使用 OAuth 专用窄仓储')
assert.match(dbServiceHandlersSource, /case 'update_openai_oauth_credentials':\s*case 'find_openai_oauth_account_for_refresh':\s*throw new Error\(`\$\{operation\.type\} 必须通过异步窄仓储处理`\)/, 'DB service 同步路径必须拒绝 OAuth 宽读写')

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
  service_tier_override: 'flex',
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
assert.equal(tokenCredentials.service_tier_override, 'flex', 'credentialsPatch 应按模型能力规则保留 OAuth Flex 服务等级覆盖')
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
assert.equal(reauthorizedCredentials.service_tier_override, 'flex', '重新授权必须保留 Flex 服务等级覆盖')
assert.equal(reauthorizedCredentials.reasoning_effort_override, 'high', '重新授权必须保留思考级别覆盖')
assert.deepEqual(reauthorizedCredentials.error_handling_rules, legacyErrorHandlingRules, '重新授权必须保留账户错误策略')

console.log('OpenAI OAuth credentialsPatch 回归通过：客户端补丁不能覆盖服务端 token 凭据，且保留请求覆盖、接口能力和账户策略')
