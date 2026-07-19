import { strict as assert } from 'node:assert'

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
  'openid profile email offline_access api.connectors.read api.connectors.invoke'
)
assert.equal(authorizeUrl.searchParams.get('id_token_add_organizations'), 'true')
assert.equal(authorizeUrl.searchParams.get('codex_cli_simplified_flow'), 'true')
assert.equal(authorizeUrl.searchParams.get('originator'), 'codex_cli_rs')

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
  client_id: 'contract-client'
})
assert.equal(headerValue(refreshRequest.headers, 'content-type'), 'application/json')
assert.equal(headerValue(refreshRequest.headers, 'content-length'), Buffer.byteLength(refreshRequest.body))
assert.equal(headerValue(refreshRequest.headers, 'originator'), 'codex_cli_rs')
assert.equal(headerValue(refreshRequest.headers, 'user-agent'), 'codex_cli_rs/0.144.4')
assert.deepEqual(JSON.parse(refreshRequest.body), {
  grant_type: 'refresh_token',
  refresh_token: 'contract-refresh-token',
  client_id: 'contract-client'
})
assert(!Object.hasOwn(JSON.parse(refreshRequest.body), 'scope'), 'refresh token 请求不得携带 scope')

console.log('OpenAI OAuth 协议契约回归通过：authorize 参数、授权码表单、refresh JSON 与 token headers 均对齐当前 Codex')

function headerValue(headers: Record<string, string | number>, name: string): string | number | undefined {
  const entry = Object.entries(headers).find(([headerName]) => headerName.toLowerCase() === name.toLowerCase())
  return entry?.[1]
}
