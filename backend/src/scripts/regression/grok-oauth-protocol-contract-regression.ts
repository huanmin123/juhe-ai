import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  GROK_OAUTH_AUTHORIZE_URL,
  GROK_OAUTH_BASE_URL,
  GROK_OAUTH_CLIENT_ID,
  GROK_OAUTH_REDIRECT_URI,
  GROK_OAUTH_SCOPE,
  GROK_OAUTH_TOKEN_URL,
  buildGrokAuthorizeUrl,
  buildGrokOAuthCredentials,
  generateGrokAuthURL
} from '../../modules/grok-oauth/grok-oauth.service.js'

assert.equal(GROK_OAUTH_AUTHORIZE_URL, 'https://auth.x.ai/oauth2/authorize', 'Grok OAuth authorize URL 必须固定为 xAI 官方地址')
assert.equal(GROK_OAUTH_TOKEN_URL, 'https://auth.x.ai/oauth2/token', 'Grok OAuth token URL 必须固定为 xAI 官方端点')
assert.equal(GROK_OAUTH_CLIENT_ID, 'b1a00492-073a-47ea-816f-4c329264a828', 'Grok OAuth client id 必须对齐当前 CLI 授权实现')
assert.equal(GROK_OAUTH_REDIRECT_URI, 'http://127.0.0.1:56121/callback', 'Grok OAuth loopback redirect URI 必须保持当前固定端口')
assert.equal(GROK_OAUTH_SCOPE, 'openid profile email offline_access grok-cli:access api:access', 'Grok OAuth scope 必须覆盖身份、离线刷新、CLI 与 API 能力')
assert.equal(GROK_OAUTH_BASE_URL, 'https://cli-chat-proxy.grok.com/v1', 'Grok OAuth 默认上游必须固定为 CLI Chat Proxy')

const authorizeUrl = new URL(buildGrokAuthorizeUrl({
  state: 'contract-state',
  nonce: 'contract-nonce',
  codeChallenge: 'contract-challenge'
}))

assert.equal(authorizeUrl.origin + authorizeUrl.pathname, GROK_OAUTH_AUTHORIZE_URL)
assert.equal(authorizeUrl.searchParams.get('response_type'), 'code')
assert.equal(authorizeUrl.searchParams.get('client_id'), GROK_OAUTH_CLIENT_ID)
assert.equal(authorizeUrl.searchParams.get('redirect_uri'), GROK_OAUTH_REDIRECT_URI)
assert.equal(authorizeUrl.searchParams.get('scope'), GROK_OAUTH_SCOPE)
assert.equal(authorizeUrl.searchParams.get('state'), 'contract-state')
assert.equal(authorizeUrl.searchParams.get('nonce'), 'contract-nonce')
assert.equal(authorizeUrl.searchParams.get('code_challenge'), 'contract-challenge')
assert.equal(authorizeUrl.searchParams.get('code_challenge_method'), 'S256')
assert.equal(authorizeUrl.searchParams.get('plan'), 'generic')
assert.equal(authorizeUrl.searchParams.get('referrer'), 'sub2api')

const generatedSession = await generateGrokAuthURL('grok-oauth-contract-owner')
const generatedUrl = new URL(generatedSession.authUrl)
assert.match(generatedSession.sessionId, /^[a-f0-9]{32}$/u, 'Grok OAuth 授权会话 id 必须为 16 字节随机十六进制串')
assert.match(generatedSession.state, /^[a-f0-9]{64}$/u, 'Grok OAuth state 必须为 32 字节随机十六进制串')
assert.equal(generatedUrl.origin + generatedUrl.pathname, GROK_OAUTH_AUTHORIZE_URL)
assert.equal(generatedUrl.searchParams.get('client_id'), GROK_OAUTH_CLIENT_ID)
assert.equal(generatedUrl.searchParams.get('redirect_uri'), GROK_OAUTH_REDIRECT_URI)
assert.equal(generatedUrl.searchParams.get('scope'), GROK_OAUTH_SCOPE)
assert.equal(generatedUrl.searchParams.get('state'), generatedSession.state)
assert.match(generatedUrl.searchParams.get('nonce') ?? '', /^[a-f0-9]{32}$/u, 'Grok OAuth nonce 必须为 16 字节随机十六进制串')
assert.equal(generatedUrl.searchParams.get('code_challenge_method'), 'S256')
assert.match(generatedUrl.searchParams.get('code_challenge') ?? '', /^[A-Za-z0-9_-]{43}$/u, 'Grok OAuth PKCE challenge 必须为 SHA-256 base64url 值')

const credentialsWithFallbackRefresh = buildGrokOAuthCredentials({
  accessToken: 'access-token-a',
  expiresAt: '2026-07-28T00:00:00.000Z',
  tokenType: 'Bearer',
  clientId: GROK_OAUTH_CLIENT_ID,
  expiresIn: 21_600,
  idToken: 'id-token-a',
  scope: GROK_OAUTH_SCOPE,
  email: 'user@example.com',
  subject: 'subject-a',
  teamId: 'team-a',
  subscriptionTier: 'supergrok',
  entitlementStatus: 'active'
}, { refreshToken: 'refresh-fallback-a' })

assert.deepEqual(credentialsWithFallbackRefresh, {
  access_token: 'access-token-a',
  expires_at: '2026-07-28T00:00:00.000Z',
  token_type: 'Bearer',
  client_id: GROK_OAUTH_CLIENT_ID,
  base_url: GROK_OAUTH_BASE_URL,
  refresh_token: 'refresh-fallback-a',
  id_token: 'id-token-a',
  scope: GROK_OAUTH_SCOPE,
  email: 'user@example.com',
  sub: 'subject-a',
  team_id: 'team-a',
  subscription_tier: 'supergrok',
  entitlement_status: 'active'
}, 'Grok OAuth 凭据归一化必须完整保留 token、身份、团队、订阅、fallback refresh token 与固定 base_url')

const credentialsWithIssuedRefresh = buildGrokOAuthCredentials({
  accessToken: 'access-token-b',
  refreshToken: 'refresh-issued-b',
  expiresAt: '2026-07-28T01:00:00.000Z',
  tokenType: 'Bearer',
  clientId: 'custom-client-b',
  expiresIn: 21_600
}, { refreshToken: 'refresh-fallback-b' })

assert.equal(credentialsWithIssuedRefresh.refresh_token, 'refresh-issued-b', 'Grok OAuth token 响应携带 refresh_token 时必须优先使用响应值')
assert.equal(credentialsWithIssuedRefresh.base_url, GROK_OAUTH_BASE_URL, 'Grok OAuth 新凭据必须写入 CLI Chat Proxy 根路径')
assert.equal(credentialsWithIssuedRefresh.client_id, 'custom-client-b', 'Grok OAuth 凭据必须保留实际换码 client_id')

const routesSource = readFileSync(resolve('src/modules/grok-oauth/grok-oauth.routes.ts'), 'utf8')
assertRoute(routesSource, "post('/auth-url'", 'Grok OAuth 必须暴露 auth-url 接口')
assertRoute(routesSource, "post('/create-from-code'", 'Grok OAuth 必须暴露授权码建号接口')
assertRoute(routesSource, "post('/create-from-refresh-token'", 'Grok OAuth 必须暴露 Refresh Token 建号接口')
assertRoute(routesSource, "post('/accounts/:id/refresh-token'", 'Grok OAuth 必须暴露手动刷新 access token 接口')
assertRoute(routesSource, "post('/accounts/:id/reauthorize-from-code'", 'Grok OAuth 必须暴露授权码重新授权接口')
assertRoute(routesSource, "post('/accounts/:id/reauthorize-from-refresh-token'", 'Grok OAuth 必须暴露 Refresh Token 重新授权接口')
assertRoute(routesSource, 'Grok OAuth 账户缺少 Refresh Token', 'Grok OAuth 刷新接口必须显式拒绝缺少 Refresh Token 的账户')
assertRoute(routesSource, "const existingBaseUrl = stringCredential(account.credentials, 'base_url')", 'Grok OAuth 刷新必须读取账户现有 base_url')
assertRoute(routesSource, 'if (existingBaseUrl) credentials.base_url = existingBaseUrl', 'Grok OAuth 刷新必须保留账户现有 base_url')
assertRoute(routesSource, 'expectedConfigRevision: account.configRevision ?? 1', 'Grok OAuth 手动刷新必须使用 config revision CAS 保护 refresh token 轮换')
assertRoute(routesSource, 'runWithProviderOAuthRefreshLock(', 'Grok OAuth 手动刷新与重授权必须加入跨进程刷新锁')
assertRoute(routesSource, 'oauthTokensChanged(account.credentials, current.credentials)', 'Grok 手动刷新等待锁后必须识别其他节点已完成的刷新')
assert.ok((routesSource.match(/oauthTokensChanged\(account\.credentials, current\.credentials\)/gu) ?? []).length >= 3, 'Grok 手动刷新与两条重授权路径都必须拦截锁内凭据变化')
assertRoute(routesSource, 'error instanceof AccountConfigRevisionConflictError', 'Grok OAuth 并发写回冲突必须返回业务冲突')
assert.equal(routesSource.includes('clearAccountFailureStateAsync'), false, 'Grok OAuth 手动刷新不得清除限流或临时不可用等业务状态')
assertRoute(routesSource, 'sanitizeAccountCredentialCarrierResponse(updatedAccount)', 'Grok OAuth 刷新接口必须返回脱敏后的账户响应')

const appSource = readFileSync(resolve('src/modules/system-api/system-api-app.ts'), 'utf8')
assertRoute(appSource, '/my-grok-oauth', '系统 API 必须挂载 my-grok-oauth 自有入口')
assertRoute(appSource, '/grok-oauth', '系统 API 必须挂载 grok-oauth 管理员入口')

const dbAccessSource = readFileSync(resolve('src/modules/system-api/system-api-db-access.ts'), 'utf8')
assertRoute(dbAccessSource, "pattern: /^\\/(?:my-)?grok-oauth(?:\\/.*)?$/, mode: 'read'", 'system-api DB access 规则必须允许 Grok OAuth 读请求')

console.log('Grok OAuth 协议契约回归通过：固定端点、授权参数、PKCE、凭据保留、系统 API 挂载与路由面均符合当前实现')

function assertRoute(source: string, fragment: string, message: string): void {
  assert.equal(source.includes(fragment), true, message)
}
