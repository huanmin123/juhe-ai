import { strict as assert } from 'node:assert'

import * as openAIOAuthService from '../../modules/openai-oauth/openai-oauth.service.js'

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
assert.equal(headerValue(refreshRequest.headers, 'user-agent'), undefined)
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
