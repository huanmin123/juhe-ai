import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import * as openAIOAuthService from '../../modules/openai-oauth/openai-oauth.service.js'
import {
  OpenAIOAuthMemorySessionStore,
  type OpenAIOAuthSession
} from '../../modules/openai-oauth/openai-oauth.service.js'

interface TokenHttpRequestContract {
  body: string
  headers: Record<string, string | number>
}

type BuildAuthorizeUrl = (input: {
  clientId: string
  redirectUri: string
  state: string
  codeChallenge: string
}) => string

type BuildTokenHttpRequest = (form: Record<string, string>) => TokenHttpRequestContract

let memoryNow = 1_000
const memoryStore = new OpenAIOAuthMemorySessionStore({
  now: () => memoryNow,
  maxEntries: 3,
  maxOwnerSessions: 2,
  cleanupIntervalMs: 5
})
const memorySession = (state: string, ownerSystemAccountId: string): OpenAIOAuthSession => ({
  state,
  codeVerifier: `verifier-${state}`,
  redirectUri: 'http://localhost:1455/auth/callback',
  clientId: 'memory-contract-client',
  ownerSystemAccountId,
  createdAt: memoryNow
})
try {
  await memoryStore.setJson('owner-a-1', memorySession('owner-a-1', 'owner-a'), 100)
  memoryNow += 1
  await memoryStore.setJson('owner-a-2', memorySession('owner-a-2', 'owner-a'), 100)
  memoryNow += 1
  await memoryStore.setJson('owner-a-3', memorySession('owner-a-3', 'owner-a'), 100)
  assert.equal(await memoryStore.getJson('owner-a-1'), undefined, 'owner 达到活跃 session 上限时必须淘汰最旧会话')
  assert.ok(await memoryStore.getJson('owner-a-2'))
  assert.ok(await memoryStore.getJson('owner-a-3'))

  memoryNow += 1
  await memoryStore.setJson('owner-b-1', memorySession('owner-b-1', 'owner-b'), 100)
  memoryNow += 1
  await memoryStore.setJson('owner-c-1', memorySession('owner-c-1', 'owner-c'), 100)
  assert.equal(await memoryStore.getJson('owner-a-2'), undefined, '达到全局容量时必须淘汰最旧会话')
  assert.equal(memoryStore.size, 3, '内存 OAuth session 容量必须保持有界')

  memoryNow += 200
  await new Promise<void>((resolve) => setTimeout(resolve, 20))
  assert.equal(memoryStore.size, 0, 'TTL 到期后必须由主动维护定时器清扫，无需再次命中过期 key')
} finally {
  memoryStore.close()
}

const generatedSession = await openAIOAuthService.generateOpenAIAuthURL('oauth-owner-a')
const generatedState = new URL(generatedSession.authUrl).searchParams.get('state')
assert.ok(generatedState, '生成的 OAuth 授权链接必须包含 state')
await assert.rejects(
  () => openAIOAuthService.consumeOpenAIOAuthSession({
    sessionId: generatedSession.sessionId,
    state: generatedState,
    ownerSystemAccountId: 'oauth-owner-b'
  }),
  /owner|所有者|归属/,
  'OAuth session 所有者不匹配时必须拒绝且保留 session'
)
const consumedSession = await openAIOAuthService.consumeOpenAIOAuthSession({
  sessionId: generatedSession.sessionId,
  state: generatedState,
  ownerSystemAccountId: 'oauth-owner-a'
})
assert.equal(consumedSession.state, generatedState, '正确 owner/state 应能消费 OAuth session')
await assert.rejects(
  () => openAIOAuthService.consumeOpenAIOAuthSession({
    sessionId: generatedSession.sessionId,
    state: generatedState,
    ownerSystemAccountId: 'oauth-owner-a'
  }),
  /不存在|过期|已消费/,
  'OAuth session 正确消费后不得再次使用'
)

const serviceExports = openAIOAuthService as unknown as Record<string, unknown>
assert.equal(
  typeof serviceExports.buildOpenAIOAuthAuthorizeUrl,
  'function',
  'OAuth 服务必须提供可独立回归的 authorize URL 契约构造器'
)
assert.equal(
  typeof serviceExports.buildOpenAIOAuthTokenHttpRequest,
  'function',
  'OAuth 服务必须提供可独立回归的 token HTTP 请求契约构造器'
)

const buildAuthorizeUrl = serviceExports.buildOpenAIOAuthAuthorizeUrl as BuildAuthorizeUrl
const authorizeUrl = new URL(buildAuthorizeUrl({
  clientId: 'contract-client',
  redirectUri: 'http://localhost:1455/auth/callback',
  state: 'contract-state',
  codeChallenge: 'contract-challenge'
}))

assert.equal(authorizeUrl.origin + authorizeUrl.pathname, 'https://auth.openai.com/oauth/authorize')
assert.equal(authorizeUrl.searchParams.get('response_type'), 'code')
assert.equal(authorizeUrl.searchParams.get('client_id'), 'contract-client')
assert.equal(authorizeUrl.searchParams.get('redirect_uri'), 'http://localhost:1455/auth/callback')
assert.equal(authorizeUrl.searchParams.get('state'), 'contract-state')
assert.equal(authorizeUrl.searchParams.get('code_challenge'), 'contract-challenge')
assert.equal(authorizeUrl.searchParams.get('code_challenge_method'), 'S256')
assert.equal(
  authorizeUrl.searchParams.get('scope'),
  'openid profile email offline_access'
)
assert.equal(authorizeUrl.searchParams.get('id_token_add_organizations'), 'true')
assert.equal(authorizeUrl.searchParams.get('codex_cli_simplified_flow'), 'true')
assert.equal(authorizeUrl.searchParams.has('originator'), false)

const buildTokenHttpRequest = serviceExports.buildOpenAIOAuthTokenHttpRequest as BuildTokenHttpRequest
const authorizationCodeRequest = buildTokenHttpRequest({
  grant_type: 'authorization_code',
  client_id: 'contract-client',
  code: 'contract-code',
  redirect_uri: 'http://localhost:1455/auth/callback',
  code_verifier: 'contract-verifier'
})
assert.equal(headerValue(authorizationCodeRequest.headers, 'content-type'), 'application/x-www-form-urlencoded')
assert.equal(headerValue(authorizationCodeRequest.headers, 'content-length'), Buffer.byteLength(authorizationCodeRequest.body))
assert.equal(headerValue(authorizationCodeRequest.headers, 'user-agent'), undefined)
assert.deepEqual(Object.fromEntries(new URLSearchParams(authorizationCodeRequest.body)), {
  grant_type: 'authorization_code',
  client_id: 'contract-client',
  code: 'contract-code',
  redirect_uri: 'http://localhost:1455/auth/callback',
  code_verifier: 'contract-verifier'
})

const refreshRequest = buildTokenHttpRequest({
  grant_type: 'refresh_token',
  refresh_token: 'contract-refresh-token',
  client_id: 'contract-client',
  scope: 'openid profile email'
})
assert.equal(headerValue(refreshRequest.headers, 'content-type'), 'application/x-www-form-urlencoded')
assert.equal(headerValue(refreshRequest.headers, 'accept'), 'application/json')
assert.equal(headerValue(refreshRequest.headers, 'content-length'), Buffer.byteLength(refreshRequest.body))
assert.equal(headerValue(refreshRequest.headers, 'originator'), undefined)
assert.equal(headerValue(refreshRequest.headers, 'user-agent'), undefined)
assert.deepEqual(Object.fromEntries(new URLSearchParams(refreshRequest.body)), {
  grant_type: 'refresh_token',
  refresh_token: 'contract-refresh-token',
  client_id: 'contract-client',
  scope: 'openid profile email'
})

const customClientCredentials = openAIOAuthService.buildOpenAIOAuthCredentials({
  accessToken: 'custom-client-access-token',
  refreshToken: 'rotated-custom-client-refresh-token',
  expiresIn: 3600,
  expiresAt: '2026-07-28T12:00:00.000Z',
  clientId: 'custom-mobile-client'
})
assert.equal(customClientCredentials.client_id, 'custom-mobile-client', 'Refresh Token 实际兑换使用的 client_id 必须进入持久化凭据')

const routesSource = readFileSync(new URL('../../modules/openai-oauth/openai-oauth.routes.ts', import.meta.url), 'utf8')
const createFromRefreshRoute = sourceBetween(
  routesSource,
  "openAIOAuthRouter.post('/create-from-refresh-token'",
  "openAIOAuthRouter.post('/accounts/:id/refresh-token'"
)
assert.match(createFromRefreshRoute, /clientId:\s*parsed\.data\.clientId/u, 'Refresh Token 建号必须把可选 clientId 传给 token endpoint')
assert.match(createFromRefreshRoute, /clientId:\s*normalizedText\(bodyField\(req, 'clientId'\)\)/u, 'clientId 必须参与建号幂等指纹')

const reauthorizeFromCodeRoute = sourceBetween(
  routesSource,
  "openAIOAuthRouter.post('/accounts/:id/reauthorize-from-code'",
  "openAIOAuthRouter.post('/accounts/:id/reauthorize-from-refresh-token'"
)
const reauthorizeFromRefreshRoute = sourceBetween(
  routesSource,
  "openAIOAuthRouter.post('/accounts/:id/reauthorize-from-refresh-token'",
  '\ntype OpenAIOAuthProvider'
)
for (const [name, source] of [
  ['授权码重授权', reauthorizeFromCodeRoute],
  ['Refresh Token 重授权', reauthorizeFromRefreshRoute]
] as const) {
  assert.match(source, /runWithProviderOAuthRefreshLock\(GPT_VENDOR_CODE, account\.id/u, `${name}必须复用供应商 OAuth 刷新锁`)
  assert.match(source, /findEditableOpenAIOAuthAccount\(account\.id, requestAccess\)/u, `${name}必须在锁内重读账户`)
  assert.match(source, /oauthTokensChanged\(account\.credentials, current\.credentials\)/u, `${name}必须拒绝覆盖锁等待期间已更新的 token`)
  assert.doesNotMatch(source, /isBlockedOpenAIOAuthErrorAccount/u, `${name}不得阻断真正需要重新授权的 error 账户`)
}
assert.match(
  reauthorizeFromRefreshRoute,
  /clientId:\s*parsed\.data\.clientId \?\? stringCredential\(current\.credentials, 'client_id'\)/u,
  'Refresh Token 重授权必须优先使用用户提交的 clientId，缺省时沿用锁内最新账户 client_id'
)

const credentialUpdateSource = sourceBetween(
  routesSource,
  'async function updateOpenAIOAuthAccountCredentials',
  '\nexport function buildReauthorizedOpenAIOAuthCredentials'
)
assert.match(credentialUpdateSource, /expectedConfigRevision:\s*account\.configRevision \?\? 1/u, '重授权写回必须使用 config revision CAS')
assert.match(credentialUpdateSource, /updated\.status !== 'error' \|\| !updated\.lastErrorCode/u, '非 error 账户不得执行无条件失败态清理')
assert.match(credentialUpdateSource, /expectedLastErrorCodes:\s*\[updated\.lastErrorCode\]/u, '重授权成功后只能按写回时的旧错误码清理失败态')

console.log('OpenAI OAuth 协议契约回归通过：authorize、token wire、自定义 clientId 与重授权并发写回均符合契约')

function headerValue(headers: Record<string, string | number>, name: string): string | number | undefined {
  const entry = Object.entries(headers).find(([headerName]) => headerName.toLowerCase() === name.toLowerCase())
  return entry?.[1]
}

function sourceBetween(source: string, startMarker: string, endMarker: string): string {
  const start = source.indexOf(startMarker)
  const end = source.indexOf(endMarker, start + startMarker.length)
  assert.notEqual(start, -1, `缺少源码起点：${startMarker}`)
  assert.notEqual(end, -1, `缺少源码终点：${endMarker}`)
  return source.slice(start, end)
}
