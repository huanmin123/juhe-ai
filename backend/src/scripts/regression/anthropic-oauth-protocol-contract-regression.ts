import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  ANTHROPIC_OAUTH_API_SCOPE,
  ANTHROPIC_OAUTH_AUTHORIZE_URL,
  ANTHROPIC_OAUTH_BROWSER_SCOPE,
  ANTHROPIC_OAUTH_CLIENT_ID,
  ANTHROPIC_OAUTH_REDIRECT_URI,
  ANTHROPIC_OAUTH_TOKEN_URL,
  buildAnthropicAuthorizeUrl,
  buildAnthropicOAuthCredentials,
  generateAnthropicAuthURL
} from '../../modules/anthropic-oauth/anthropic-oauth.service.js'

assert.equal(ANTHROPIC_OAUTH_CLIENT_ID, '9d1c250a-e61b-44d9-88ed-5944d1962f5e', 'Anthropic OAuth client id 必须对齐当前已验证实现')
assert.equal(ANTHROPIC_OAUTH_AUTHORIZE_URL, 'https://claude.ai/oauth/authorize', 'Anthropic OAuth authorize URL 必须固定为 Claude 官方地址')
assert.equal(ANTHROPIC_OAUTH_TOKEN_URL, 'https://api.anthropic.com/v1/oauth/token', 'Anthropic OAuth token URL 必须固定为 Anthropic 官方 token endpoint')
assert.equal(ANTHROPIC_OAUTH_REDIRECT_URI, 'http://localhost:1455/auth/callback', 'Anthropic OAuth loopback redirect URI 必须保持当前固定端口')
assert.equal(ANTHROPIC_OAUTH_BROWSER_SCOPE, 'org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload', 'Anthropic OAuth 浏览器 scope 必须覆盖当前托管授权所需能力')
assert.equal(ANTHROPIC_OAUTH_API_SCOPE, 'user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload', 'Anthropic OAuth API scope 必须保持与当前 Bearer 能力一致')

const authorizeUrl = new URL(buildAnthropicAuthorizeUrl({
  state: 'contract-state',
  codeChallenge: 'contract-challenge',
  scope: ANTHROPIC_OAUTH_BROWSER_SCOPE,
  redirectUri: ANTHROPIC_OAUTH_REDIRECT_URI,
  clientId: ANTHROPIC_OAUTH_CLIENT_ID
}))

assert.equal(authorizeUrl.origin + authorizeUrl.pathname, ANTHROPIC_OAUTH_AUTHORIZE_URL)
assert.equal(authorizeUrl.searchParams.get('code'), 'true')
assert.equal(authorizeUrl.searchParams.get('client_id'), ANTHROPIC_OAUTH_CLIENT_ID)
assert.equal(authorizeUrl.searchParams.get('response_type'), 'code')
assert.equal(authorizeUrl.searchParams.get('redirect_uri'), ANTHROPIC_OAUTH_REDIRECT_URI)
assert.equal(authorizeUrl.searchParams.get('scope'), ANTHROPIC_OAUTH_BROWSER_SCOPE)
assert.equal(authorizeUrl.searchParams.get('code_challenge'), 'contract-challenge')
assert.equal(authorizeUrl.searchParams.get('code_challenge_method'), 'S256')
assert.equal(authorizeUrl.searchParams.get('state'), 'contract-state')

const generatedSession = await generateAnthropicAuthURL('anthropic-oauth-contract-owner')
const generatedUrl = new URL(generatedSession.authUrl)
assert.ok(generatedSession.sessionId.length >= 16, 'Anthropic OAuth 授权会话 id 必须非空且保持随机性')
assert.equal(generatedUrl.origin + generatedUrl.pathname, ANTHROPIC_OAUTH_AUTHORIZE_URL)
assert.equal(generatedUrl.searchParams.get('client_id'), ANTHROPIC_OAUTH_CLIENT_ID)
assert.equal(generatedUrl.searchParams.get('redirect_uri'), ANTHROPIC_OAUTH_REDIRECT_URI)
assert.equal(generatedUrl.searchParams.get('scope'), ANTHROPIC_OAUTH_BROWSER_SCOPE)
assert.equal(generatedUrl.searchParams.get('code_challenge_method'), 'S256')
assert.ok(generatedUrl.searchParams.get('state'), 'Anthropic OAuth 授权链接必须包含 state')
assert.ok(generatedUrl.searchParams.get('code_challenge'), 'Anthropic OAuth 授权链接必须包含 PKCE code_challenge')

const credentialsWithFallbackRefresh = buildAnthropicOAuthCredentials({
  accessToken: 'access-token-a',
  clientId: ANTHROPIC_OAUTH_CLIENT_ID,
  expiresAt: '2026-07-27T00:00:00.000Z',
  email: 'user@example.com',
  accountId: 'account-a',
  organizationId: 'org-a',
  scope: ANTHROPIC_OAUTH_API_SCOPE,
  tokenType: 'Bearer'
}, { refreshToken: 'refresh-fallback-a' })

assert.deepEqual(credentialsWithFallbackRefresh, {
  access_token: 'access-token-a',
  base_url: 'https://api.anthropic.com/v1',
  client_id: ANTHROPIC_OAUTH_CLIENT_ID,
  refresh_token: 'refresh-fallback-a',
  expires_at: '2026-07-27T00:00:00.000Z',
  email: 'user@example.com',
  account_id: 'account-a',
  organization_id: 'org-a',
  scope: ANTHROPIC_OAUTH_API_SCOPE,
  token_type: 'Bearer'
}, 'Anthropic OAuth 凭据归一化必须补齐 base_url、client_id 与 fallback refresh token')

const credentialsWithIssuedRefresh = buildAnthropicOAuthCredentials({
  accessToken: 'access-token-b',
  refreshToken: 'refresh-issued-b',
  clientId: 'custom-client-b'
}, { refreshToken: 'refresh-fallback-b' })

assert.equal(credentialsWithIssuedRefresh.refresh_token, 'refresh-issued-b', 'Anthropic OAuth token 响应携带 refresh_token 时必须优先使用响应值')
assert.equal(credentialsWithIssuedRefresh.base_url, 'https://api.anthropic.com/v1', 'Anthropic OAuth 凭据必须固定写入官方 Messages 根路径')
assert.equal(credentialsWithIssuedRefresh.client_id, 'custom-client-b', 'Anthropic OAuth 凭据必须保留实际换码 client_id')

const routesSource = readFileSync(resolve('src/modules/anthropic-oauth/anthropic-oauth.routes.ts'), 'utf8')
assertRoute(routesSource, "post('/auth-url'", 'Anthropic OAuth 必须暴露 auth-url 接口')
assertRoute(routesSource, "post('/create-from-code'", 'Anthropic OAuth 必须暴露授权码建号接口')
assertRoute(routesSource, "post('/create-from-refresh-token'", 'Anthropic OAuth 必须暴露 Refresh Token 建号接口')
assertRoute(routesSource, "post('/accounts/:id/refresh-token'", 'Anthropic OAuth 必须暴露手动刷新 access token 接口')
assertRoute(routesSource, "post('/accounts/:id/reauthorize-from-code'", 'Anthropic OAuth 必须暴露授权码重新授权接口')
assertRoute(routesSource, "post('/accounts/:id/reauthorize-from-refresh-token'", 'Anthropic OAuth 必须暴露 Refresh Token 重新授权接口')
assertRoute(routesSource, 'Anthropic OAuth 账户缺少 Refresh Token', 'Anthropic OAuth 刷新接口必须对缺失 Refresh Token 做显式拒绝')
assertRoute(routesSource, "clearAccountFailureStateAsync(account.id, requestAccess) ?? updatedAccount", 'Anthropic OAuth 手动刷新成功后必须尝试清理旧失败态')
assertRoute(routesSource, "sanitizeAccountCredentialCarrierResponse(restoredAccount)", 'Anthropic OAuth 刷新接口必须返回脱敏后的账户响应')

const appSource = readFileSync(resolve('src/modules/system-api/system-api-app.ts'), 'utf8')
assertRoute(appSource, '/my-anthropic-oauth', '系统 API 必须挂载 my-anthropic-oauth 自有入口')
assertRoute(appSource, '/anthropic-oauth', '系统 API 必须挂载 anthropic-oauth 管理员入口')

const dbAccessSource = readFileSync(resolve('src/modules/system-api/system-api-db-access.ts'), 'utf8')
assertRoute(dbAccessSource, "pattern: /^\\/(?:my-)?anthropic-oauth(?:\\/.*)?$/, mode: 'read'", 'system-api DB access 规则必须允许 Anthropic OAuth 读请求')

console.log('Anthropic OAuth 协议契约回归通过：authorize 参数、凭据归一化、系统 API 挂载与路由面均符合当前实现')

function assertRoute(source: string, fragment: string, message: string): void {
  assert.equal(source.includes(fragment), true, message)
}
