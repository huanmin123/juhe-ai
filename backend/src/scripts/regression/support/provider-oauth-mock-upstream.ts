import { createHash, randomBytes } from 'node:crypto'
import http from 'node:http'

import type { TokenExchangeTransport } from '../../../modules/providers/drivers/_shared/token-exchange-transport.js'

type Provider = 'openai' | 'anthropic' | 'gemini' | 'grok'

interface AuthorizationGrant {
  provider: Provider
  clientId: string
  redirectUri: string
  state: string
  codeChallenge: string
}

interface ProviderContract {
  authorizeUrl: string
  tokenUrl: string
  clientId?: string
  redirectUri: string
  scope: string
  tokenEncoding: 'form' | 'json'
}

const contracts: Record<Provider, ProviderContract> = {
  openai: {
    authorizeUrl: 'https://auth.openai.com/oauth/authorize',
    tokenUrl: 'https://auth.openai.com/oauth/token',
    clientId: 'app_EMoamEEZ73f0CkXaXp7hrann',
    redirectUri: 'http://localhost:1455/auth/callback',
    scope: 'openid profile email offline_access',
    tokenEncoding: 'form'
  },
  anthropic: {
    authorizeUrl: 'https://claude.ai/oauth/authorize',
    tokenUrl: 'https://platform.claude.com/v1/oauth/token',
    clientId: '9d1c250a-e61b-44d9-88ed-5944d1962f5e',
    redirectUri: 'https://platform.claude.com/oauth/code/callback',
    scope: 'org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload',
    tokenEncoding: 'json'
  },
  gemini: {
    authorizeUrl: 'https://accounts.google.com/o/oauth2/v2/auth',
    tokenUrl: 'https://oauth2.googleapis.com/token',
    redirectUri: 'http://localhost:1455/auth/callback',
    scope: 'https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/generative-language.retriever',
    tokenEncoding: 'form'
  },
  grok: {
    authorizeUrl: 'https://auth.x.ai/oauth2/authorize',
    tokenUrl: 'https://auth.x.ai/oauth2/token',
    clientId: 'b1a00492-073a-47ea-816f-4c329264a828',
    redirectUri: 'http://127.0.0.1:56121/callback',
    scope: 'openid profile email offline_access grok-cli:access api:access',
    tokenEncoding: 'form'
  }
}

export interface MockAuthorizationResult {
  code: string
  state: string
  callbackUrl: string
}

export interface ProviderOAuthMockUpstream {
  authorize(provider: Provider, realAuthorizeUrl: string): Promise<MockAuthorizationResult>
  failNextTokenRequest(provider: Provider, statusCode?: number, errorCode?: string): void
  inferenceBaseUrl(provider: Provider): string
  inferenceRequestCount(provider: Provider): number
  tokenTransport: TokenExchangeTransport
  close(): Promise<void>
}

export async function startProviderOAuthMockUpstream(): Promise<ProviderOAuthMockUpstream> {
  const grants = new Map<string, AuthorizationGrant>()
  const refreshTokens = new Map<string, Provider>()
  const failNext = new Map<Provider, { statusCode: number; errorCode: string }>()
  const inferenceRequests = new Map<Provider, number>()
  const server = http.createServer(async (request, response) => {
    try {
      const url = new URL(request.url ?? '/', 'http://127.0.0.1')
      const inferenceMatch = /^\/inference\/(openai|anthropic|gemini|grok)(\/.*)$/u.exec(url.pathname)
      if (inferenceMatch) {
        const provider = inferenceMatch[1] as Provider
        const body = await readBody(request)
        handleInference(provider, inferenceMatch[2]!, request, body, response)
        inferenceRequests.set(provider, (inferenceRequests.get(provider) ?? 0) + 1)
        return
      }
      const match = /^\/(openai|anthropic|gemini|grok)\/(authorize|token)$/u.exec(url.pathname)
      if (!match) return json(response, 404, { error: 'unexpected_path' })
      const provider = match[1] as Provider
      if (match[2] === 'authorize') {
        if (request.method !== 'GET') return json(response, 405, { error: 'method_not_allowed' })
        return handleAuthorize(provider, url, grants, response)
      }
      if (request.method !== 'POST') return json(response, 405, { error: 'method_not_allowed' })
      const plannedFailure = failNext.get(provider)
      if (plannedFailure) {
        failNext.delete(provider)
        return json(response, plannedFailure.statusCode, {
          error: plannedFailure.errorCode,
          error_description: 'planned token failure'
        })
      }
      const body = await readBody(request)
      return handleToken(provider, request, body, grants, refreshTokens, response)
    } catch (error) {
      return json(response, 500, { error: 'mock_failure', error_description: error instanceof Error ? error.message : String(error) })
    }
  })
  await listen(server)
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('OAuth mock upstream 未取得监听端口')
  const baseUrl = `http://127.0.0.1:${address.port}`

  const tokenTransport: TokenExchangeTransport = async (input) => {
    const provider = providerForTokenUrl(input.url)
    const target = `${baseUrl}/${provider}/token`
    const response = await fetch(target, {
      method: 'POST',
      headers: input.headers,
      body: input.body,
      signal: input.signal
    })
    const bytes = new Uint8Array(await response.arrayBuffer())
    const truncated = bytes.byteLength > input.maxResponseBytes
    return {
      statusCode: response.status,
      bodyText: Buffer.from(bytes.subarray(0, input.maxResponseBytes)).toString('utf8'),
      truncated
    }
  }

  return {
    async authorize(provider, realAuthorizeUrl) {
      const realUrl = new URL(realAuthorizeUrl)
      assertEqual(realUrl.origin + realUrl.pathname, contracts[provider].authorizeUrl, `${provider} authorize endpoint`)
      const response = await fetch(`${baseUrl}/${provider}/authorize?${realUrl.searchParams.toString()}`)
      const payload = await response.json() as MockAuthorizationResult & { error?: string; error_description?: string }
      if (!response.ok) throw new Error(payload.error_description || payload.error || `authorize HTTP ${response.status}`)
      return payload
    },
    failNextTokenRequest(provider, statusCode = 503, errorCode = 'temporarily_unavailable') {
      failNext.set(provider, { statusCode, errorCode })
    },
    inferenceBaseUrl(provider) {
      return `${baseUrl}/inference/${provider}`
    },
    inferenceRequestCount(provider) {
      return inferenceRequests.get(provider) ?? 0
    },
    tokenTransport,
    close: () => closeServer(server)
  }
}

function handleInference(
  provider: Provider,
  path: string,
  request: http.IncomingMessage,
  bodyText: string,
  response: http.ServerResponse
): void {
  if (request.method !== 'POST') return json(response, 405, { error: 'method_not_allowed' })
  const authorization = required(request.headers.authorization, `${provider} inference authorization`)
  if (!authorization.startsWith('Bearer ')) throw new Error(`${provider} inference 必须使用 Bearer`)
  required(bodyText, `${provider} inference body`)
  if (provider === 'openai') {
    assertEqual(path, '/responses', 'OpenAI inference path')
    required(String(request.headers['chatgpt-account-id'] ?? ''), 'OpenAI ChatGPT-Account-Id')
    return json(response, 200, { id: 'resp_openai_mock', object: 'response', status: 'completed', output: [] })
  }
  if (provider === 'anthropic') {
    assertEqual(path, '/v1/messages', 'Anthropic inference path')
    required(String(request.headers['anthropic-version'] ?? ''), 'Anthropic version header')
    return json(response, 200, { id: 'msg_anthropic_mock', type: 'message', role: 'assistant', content: [], model: 'claude-mock', stop_reason: 'end_turn', usage: { input_tokens: 1, output_tokens: 1 } })
  }
  if (provider === 'gemini') {
    assertEqual(path, '/v1beta/models/gemini-2.5-flash:generateContent', 'Gemini inference path')
    assertEqual(request.headers['x-goog-api-key'], undefined, 'Gemini OAuth inference 不得发送 API Key header')
    return json(response, 200, { candidates: [{ content: { role: 'model', parts: [{ text: 'gemini mock' }] } }], usageMetadata: { promptTokenCount: 1, candidatesTokenCount: 1 } })
  }
  assertEqual(path, '/v1/responses', 'Grok inference path')
  assertEqual(request.headers['x-xai-token-auth'], undefined, '非 CLI mock base URL 不得泄漏 Grok CLI header')
  json(response, 200, { id: 'resp_grok_mock', object: 'response', status: 'completed', output: [] })
}

function handleAuthorize(
  provider: Provider,
  url: URL,
  grants: Map<string, AuthorizationGrant>,
  response: http.ServerResponse
): void {
  const contract = contracts[provider]
  assertEqual(url.searchParams.get('response_type'), 'code', `${provider} response_type`)
  const clientId = required(url.searchParams.get('client_id'), `${provider} client_id`)
  if (contract.clientId) assertEqual(clientId, contract.clientId, `${provider} client_id`)
  const redirectUri = required(url.searchParams.get('redirect_uri'), `${provider} redirect_uri`)
  assertEqual(redirectUri, contract.redirectUri, `${provider} redirect_uri`)
  assertEqual(url.searchParams.get('scope'), contract.scope, `${provider} scope`)
  assertEqual(url.searchParams.get('code_challenge_method'), 'S256', `${provider} PKCE method`)
  const state = required(url.searchParams.get('state'), `${provider} state`)
  const codeChallenge = required(url.searchParams.get('code_challenge'), `${provider} code_challenge`)
  if (provider === 'grok') required(url.searchParams.get('nonce'), 'grok nonce')
  if (provider === 'openai' && url.searchParams.has('originator')) throw new Error('OpenAI authorize 不应携带 originator')
  const code = `${provider}-code-${randomBytes(8).toString('hex')}`
  grants.set(code, { provider, clientId, redirectUri, state, codeChallenge })
  const callback = new URL(redirectUri)
  callback.searchParams.set('code', code)
  callback.searchParams.set('state', state)
  json(response, 200, { code, state, callbackUrl: callback.toString() })
}

function handleToken(
  provider: Provider,
  request: http.IncomingMessage,
  bodyText: string,
  grants: Map<string, AuthorizationGrant>,
  refreshTokens: Map<string, Provider>,
  response: http.ServerResponse
): void {
  const contract = contracts[provider]
  const contentType = String(request.headers['content-type'] ?? '')
  const fields = contract.tokenEncoding === 'json'
    ? parseJson(bodyText)
    : Object.fromEntries(new URLSearchParams(bodyText))
  assertEqual(contentType, contract.tokenEncoding === 'json' ? 'application/json' : 'application/x-www-form-urlencoded', `${provider} token content-type`)
  const grantType = required(fields.grant_type, `${provider} grant_type`)
  const clientId = required(fields.client_id, `${provider} token client_id`)
  if (contract.clientId) assertEqual(clientId, contract.clientId, `${provider} token client_id`)

  if (grantType === 'authorization_code') {
    const code = required(fields.code, `${provider} code`)
    const grant = grants.get(code)
    if (!grant || grant.provider !== provider) return json(response, 400, { error: 'invalid_grant' })
    assertEqual(fields.redirect_uri, grant.redirectUri, `${provider} token redirect_uri`)
    assertEqual(clientId, grant.clientId, `${provider} token grant client_id`)
    const verifier = required(fields.code_verifier, `${provider} code_verifier`)
    assertEqual(createHash('sha256').update(verifier).digest('base64url'), grant.codeChallenge, `${provider} PKCE verifier`)
    grants.delete(code)
    return issueTokens(provider, clientId, refreshTokens, response)
  }

  if (grantType === 'refresh_token') {
    const refreshToken = required(fields.refresh_token, `${provider} refresh_token`)
    if (refreshTokens.get(refreshToken) !== provider) return json(response, 400, { error: 'invalid_grant' })
    if (provider === 'openai') assertEqual(fields.scope, 'openid profile email', 'OpenAI refresh scope')
    refreshTokens.delete(refreshToken)
    return issueTokens(provider, clientId, refreshTokens, response)
  }
  json(response, 400, { error: 'unsupported_grant_type' })
}

function issueTokens(
  provider: Provider,
  clientId: string,
  refreshTokens: Map<string, Provider>,
  response: http.ServerResponse
): void {
  const refreshToken = `${provider}-refresh-${randomBytes(8).toString('hex')}`
  refreshTokens.set(refreshToken, provider)
  if (provider === 'openai') {
    const accessToken = jwt({
      sub: 'openai-user',
      'https://api.openai.com/auth': {
        chatgpt_account_id: 'chatgpt-account-mock',
        chatgpt_user_id: 'chatgpt-user-mock',
        chatgpt_plan_type: 'plus'
      }
    })
    return json(response, 200, {
      access_token: accessToken,
      refresh_token: refreshToken,
      id_token: jwt({ email: 'openai@example.test' }),
      expires_in: 3600,
      token_type: 'Bearer'
    })
  }
  if (provider === 'anthropic') {
    return json(response, 200, {
      access_token: `anthropic-access-${randomBytes(8).toString('hex')}`,
      refresh_token: refreshToken,
      expires_in: 3600,
      token_type: 'Bearer',
      scope: contracts.anthropic.scope,
      account: { uuid: 'anthropic-account-mock', email_address: 'anthropic@example.test' },
      organization: { uuid: 'anthropic-organization-mock' }
    })
  }
  if (provider === 'gemini') {
    return json(response, 200, {
      access_token: `gemini-access-${randomBytes(8).toString('hex')}`,
      refresh_token: refreshToken,
      expires_in: 3600,
      token_type: 'Bearer',
      scope: contracts.gemini.scope
    })
  }
  json(response, 200, {
    access_token: jwt({ team_id: 'grok-team-mock', subscription_tier: 'supergrok' }),
    refresh_token: refreshToken,
    id_token: jwt({ email: 'grok@example.test', sub: 'grok-user-mock', entitlement_status: 'active' }),
    expires_in: 21_600,
    token_type: 'Bearer',
    scope: contracts.grok.scope,
    client_id: clientId
  })
}

function providerForTokenUrl(url: string): Provider {
  for (const [provider, contract] of Object.entries(contracts) as Array<[Provider, ProviderContract]>) {
    if (contract.tokenUrl === url) return provider
  }
  throw new Error(`模拟器收到未知 token endpoint：${url}`)
}

function jwt(claims: Record<string, unknown>): string {
  const header = Buffer.from(JSON.stringify({ alg: 'none', typ: 'JWT' })).toString('base64url')
  const payload = Buffer.from(JSON.stringify(claims)).toString('base64url')
  return `${header}.${payload}.mock-signature`
}

function parseJson(body: string): Record<string, string> {
  const value = JSON.parse(body) as unknown
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('token JSON body 必须是对象')
  return Object.fromEntries(Object.entries(value).map(([key, field]) => [key, String(field)]))
}

function required(value: string | null | undefined, label: string): string {
  const normalized = value?.trim() ?? ''
  if (!normalized) throw new Error(`${label} 缺失`)
  return normalized
}

function assertEqual(actual: unknown, expected: unknown, label: string): void {
  if (actual !== expected) throw new Error(`${label} 不匹配：期望 ${String(expected)}，实际 ${String(actual)}`)
}

function readBody(request: http.IncomingMessage): Promise<string> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = []
    request.on('data', (chunk: Buffer) => chunks.push(chunk))
    request.once('error', reject)
    request.once('end', () => resolve(Buffer.concat(chunks).toString('utf8')))
  })
}

function json(response: http.ServerResponse, status: number, body: Record<string, unknown>): void {
  response.writeHead(status, { 'content-type': 'application/json' })
  response.end(JSON.stringify(body))
}

function listen(server: http.Server): Promise<void> {
  return new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      server.off('error', reject)
      resolve()
    })
  })
}

function closeServer(server: http.Server): Promise<void> {
  if (!server.listening) return Promise.resolve()
  return new Promise((resolve) => server.close(() => resolve()))
}
