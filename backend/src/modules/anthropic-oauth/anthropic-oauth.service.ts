import { createHash, randomBytes } from 'node:crypto'
import { request as httpsRequest } from 'node:https'

import { runtimeConfig } from '../../config/runtime.js'
import { BoundedBufferCollector } from '../../shared/bounded-buffer.js'
import { createRuntimeStateStore, type RuntimeStateStore } from '../../shared/runtime-state-store.js'
import { sanitizeDiagnosticPayload } from '../gateway/diagnostics/diagnostic-sanitizer.js'
import { HttpsProxyAgent } from 'https-proxy-agent'
import { SocksProxyAgent } from 'socks-proxy-agent'
import { createProxyAgent } from '../openai-oauth/openai-oauth.service.js'

export const ANTHROPIC_OAUTH_CLIENT_ID = '9d1c250a-e61b-44d9-88ed-5944d1962f5e'
export const ANTHROPIC_OAUTH_AUTHORIZE_URL = 'https://claude.ai/oauth/authorize'
export const ANTHROPIC_OAUTH_TOKEN_URL = 'https://api.anthropic.com/v1/oauth/token'
export const ANTHROPIC_OAUTH_REDIRECT_URI = 'http://localhost:1455/auth/callback'
export const ANTHROPIC_OAUTH_BROWSER_SCOPE = 'org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload'
export const ANTHROPIC_OAUTH_API_SCOPE = 'user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload'
export const anthropicOAuthResponseMaxBytes = 256 * 1024
export const anthropicOAuthRequestTimeoutMs = 25_000

export interface AnthropicOAuthSession {
  state: string
  codeVerifier: string
  scope: string
  redirectUri: string
  clientId: string
  ownerSystemAccountId?: string
  createdAt: number
}

export interface AnthropicOAuthAuthURLResult {
  authUrl: string
  sessionId: string
}

export interface AnthropicOAuthTokenInfo {
  accessToken: string
  refreshToken?: string
  expiresIn?: number
  expiresAt?: string
  email?: string
  accountId?: string
  organizationId?: string
  scope?: string
  tokenType?: string
  clientId: string
}

type AnthropicOAuthSessionStore = Pick<RuntimeStateStore, 'getJson' | 'setJson' | 'compareDeleteJson'>

const sessionTtlMs = 30 * 60 * 1000

export async function generateAnthropicAuthURL(ownerSystemAccountId?: string): Promise<AnthropicOAuthAuthURLResult> {
  const state = randomBytes(32).toString('hex')
  const codeVerifier = base64Url(randomBytes(32))
  const codeChallenge = createHash('sha256').update(codeVerifier).digest('base64url')
  const sessionId = randomBytes(16).toString('hex')

  await anthropicOauthSessionStore().setJson<AnthropicOAuthSession>(sessionId, {
    state,
    codeVerifier,
    scope: ANTHROPIC_OAUTH_BROWSER_SCOPE,
    redirectUri: ANTHROPIC_OAUTH_REDIRECT_URI,
    clientId: ANTHROPIC_OAUTH_CLIENT_ID,
    ownerSystemAccountId: normalizeString(ownerSystemAccountId) || undefined,
    createdAt: Date.now()
  }, sessionTtlMs)

  return {
    authUrl: buildAnthropicAuthorizeUrl({
      state,
      codeChallenge,
      scope: ANTHROPIC_OAUTH_BROWSER_SCOPE,
      redirectUri: ANTHROPIC_OAUTH_REDIRECT_URI,
      clientId: ANTHROPIC_OAUTH_CLIENT_ID
    }),
    sessionId
  }
}

export function buildAnthropicAuthorizeUrl(input: {
  state: string
  codeChallenge: string
  scope: string
  redirectUri: string
  clientId: string
}): string {
  const params = new URLSearchParams({
    code: 'true',
    client_id: input.clientId,
    response_type: 'code',
    redirect_uri: input.redirectUri,
    scope: input.scope,
    code_challenge: input.codeChallenge,
    code_challenge_method: 'S256',
    state: input.state
  })
  return `${ANTHROPIC_OAUTH_AUTHORIZE_URL}?${params.toString()}`
}

export async function exchangeAnthropicAuthCode(input: {
  sessionId: string
  callbackUrl: string
  ownerSystemAccountId?: string
  proxyUrl?: string
  signal?: AbortSignal
}): Promise<AnthropicOAuthTokenInfo> {
  const { code, state } = extractCodeAndState(input.callbackUrl)
  const session = await consumeAnthropicOAuthSession({
    sessionId: input.sessionId,
    state,
    ownerSystemAccountId: input.ownerSystemAccountId
  })

  return await requestAnthropicToken({
    code,
    redirect_uri: session.redirectUri,
    client_id: session.clientId,
    grant_type: 'authorization_code',
    code_verifier: session.codeVerifier,
    state
  }, input.proxyUrl, input.signal)
}

export async function refreshAnthropicAuthToken(input: {
  refreshToken: string
  clientId?: string
  proxyUrl?: string
  signal?: AbortSignal
}): Promise<AnthropicOAuthTokenInfo> {
  const refreshToken = normalizeString(input.refreshToken)
  if (!refreshToken) throw new Error('Anthropic Refresh Token 不能为空')
  return await requestAnthropicToken({
    grant_type: 'refresh_token',
    refresh_token: refreshToken,
    client_id: normalizeString(input.clientId) || ANTHROPIC_OAUTH_CLIENT_ID
  }, input.proxyUrl, input.signal)
}

export function buildAnthropicOAuthCredentials(tokenInfo: AnthropicOAuthTokenInfo, fallback?: { refreshToken?: string }): Record<string, unknown> {
  const credentials: Record<string, unknown> = {
    access_token: tokenInfo.accessToken,
    base_url: 'https://api.anthropic.com/v1',
    client_id: tokenInfo.clientId
  }
  const refreshToken = normalizeString(tokenInfo.refreshToken) || normalizeString(fallback?.refreshToken)
  if (refreshToken) credentials.refresh_token = refreshToken
  if (tokenInfo.expiresAt) credentials.expires_at = tokenInfo.expiresAt
  if (tokenInfo.email) credentials.email = tokenInfo.email
  if (tokenInfo.accountId) credentials.account_id = tokenInfo.accountId
  if (tokenInfo.organizationId) credentials.organization_id = tokenInfo.organizationId
  if (tokenInfo.scope) credentials.scope = tokenInfo.scope
  if (tokenInfo.tokenType) credentials.token_type = tokenInfo.tokenType
  return credentials
}

export function sanitizeAnthropicOAuthErrorMessage(message: string): string {
  return sanitizeDiagnosticPayload(message)
}

async function consumeAnthropicOAuthSession(input: {
  sessionId: string
  state: string
  ownerSystemAccountId?: string
}): Promise<AnthropicOAuthSession> {
  const sessionStore = anthropicOauthSessionStore()
  const session = await sessionStore.getJson<AnthropicOAuthSession>(input.sessionId)
  if (!session) throw new Error('Anthropic OAuth 会话不存在或已过期')
  if (!input.state || input.state !== session.state) throw new Error('Anthropic OAuth state 无效')
  const expectedOwner = normalizeString(session.ownerSystemAccountId)
  const actualOwner = normalizeString(input.ownerSystemAccountId)
  if (expectedOwner && actualOwner !== expectedOwner) throw new Error('Anthropic OAuth session owner 归属无效')
  if (!await sessionStore.compareDeleteJson(input.sessionId, session)) {
    throw new Error('Anthropic OAuth 会话已消费，请重新发起授权')
  }
  return session
}

async function requestAnthropicToken(form: Record<string, string>, proxyUrl?: string, signal?: AbortSignal): Promise<AnthropicOAuthTokenInfo> {
  const requestBody = JSON.stringify(form)
  const response = await performAnthropicTokenRequest({
    body: requestBody,
    headers: {
      'content-type': 'application/json',
      'content-length': Buffer.byteLength(requestBody),
      accept: 'application/json, text/plain, */*',
      'user-agent': 'axios/1.13.6'
    }
  }, proxyUrl, signal)

  let payload: Record<string, unknown> = {}
  if (response.body) {
    try {
      payload = JSON.parse(response.body) as Record<string, unknown>
    } catch {
      payload = { raw: response.body }
    }
  }

  if (response.statusCode < 200 || response.statusCode >= 300) {
    const detail = sanitizeAnthropicOAuthErrorMessage(normalizeString(payload.error_description) || normalizeString(payload.error) || response.body)
    throw new Error(`Anthropic OAuth 令牌请求失败：HTTP ${response.statusCode}${detail ? `，${detail}` : ''}`)
  }

  const accessToken = normalizeString(payload.access_token)
  if (!accessToken) throw new Error('Anthropic OAuth 令牌响应缺少 access_token')
  const refreshToken = normalizeString(payload.refresh_token)
  const expiresIn = finitePositiveInteger(payload.expires_in)
  const expiresAt = expiresIn ? new Date(Date.now() + expiresIn * 1000).toISOString() : undefined
  const account = isPlainObject(payload.account) ? payload.account : undefined
  const organization = isPlainObject(payload.organization) ? payload.organization : undefined

  return {
    accessToken,
    refreshToken,
    expiresIn,
    expiresAt,
    email: normalizeString(account?.email_address),
    accountId: normalizeString(account?.uuid),
    organizationId: normalizeString(organization?.uuid),
    scope: normalizeString(payload.scope),
    tokenType: normalizeString(payload.token_type),
    clientId: normalizeString(form.client_id) || ANTHROPIC_OAUTH_CLIENT_ID
  }
}

async function performAnthropicTokenRequest(
  request: { body: string; headers: Record<string, string | number> },
  proxyUrl?: string,
  signal?: AbortSignal
): Promise<{ statusCode: number; body: string }> {
  const resolvedProxyUrl = normalizeString(proxyUrl) || runtimeConfig.oauthProxyUrl
  const agent: HttpsProxyAgent<string> | SocksProxyAgent | undefined = resolvedProxyUrl ? createProxyAgent(resolvedProxyUrl) : undefined

  return await new Promise<{ statusCode: number; body: string }>((resolve, reject) => {
    if (signal?.aborted) {
      reject(new Error('请求已取消'))
      return
    }
    const requestHandle = httpsRequest(ANTHROPIC_OAUTH_TOKEN_URL, {
      method: 'POST',
      headers: request.headers,
      agent,
      timeout: anthropicOAuthRequestTimeoutMs
    }, (response) => {
      const body = new BoundedBufferCollector(anthropicOAuthResponseMaxBytes)
      let settled = false
      const settle = (error?: Error) => {
        if (settled) return
        settled = true
        if (error) {
          reject(error)
          return
        }
        resolve({ statusCode: response.statusCode ?? 0, body: body.text() })
      }
      response.on('data', (chunk: Buffer) => {
        if (settled) return
        body.append(chunk)
        if (body.truncated) {
          const error = new Error('Anthropic OAuth 令牌响应体过大')
          settle(error)
          requestHandle.destroy(error)
        }
      })
      response.once('aborted', () => settle(new Error('Anthropic OAuth 令牌响应被中断')))
      response.once('error', (error) => settle(error))
      response.once('end', () => settle())
      response.once('close', () => settle(new Error('Anthropic OAuth 令牌响应提前关闭')))
    })
    const abort = () => requestHandle.destroy(new Error('请求已取消'))
    signal?.addEventListener('abort', abort, { once: true })
    const cleanupAbort = () => signal?.removeEventListener('abort', abort)
    requestHandle.on('error', (error) => {
      cleanupAbort()
      reject(error)
    })
    requestHandle.on('response', cleanupAbort)
    requestHandle.on('close', cleanupAbort)
    requestHandle.on('timeout', () => requestHandle.destroy(new Error('Anthropic OAuth 令牌请求超时')))
    requestHandle.end(request.body)
  })
}

function extractCodeAndState(callbackUrl: string): { code: string; state: string } {
  const url = new URL(normalizeString(callbackUrl))
  const code = normalizeString(url.searchParams.get('code'))
  const state = normalizeString(url.searchParams.get('state'))
  if (!code || !state) throw new Error('Anthropic 回调 URL 必须包含 code 和 state')
  return { code, state }
}

function finitePositiveInteger(value: unknown): number | undefined {
  const parsed = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(parsed) || parsed <= 0) return undefined
  return Math.trunc(parsed)
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function base64Url(input: Buffer): string {
  return input.toString('base64url')
}

function normalizeString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

let memoryAnthropicOauthSessionStore: AnthropicOAuthMemorySessionStore | undefined

function anthropicOauthSessionStore(): AnthropicOAuthSessionStore {
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    return createRuntimeStateStore('anthropic-oauth:sessions')
  }
  memoryAnthropicOauthSessionStore = memoryAnthropicOauthSessionStore ?? new AnthropicOAuthMemorySessionStore()
  return memoryAnthropicOauthSessionStore
}

class AnthropicOAuthMemorySessionStore implements AnthropicOAuthSessionStore {
  private readonly entries = new Map<string, { value: AnthropicOAuthSession; expiresAt: number }>()
  private readonly cleanupTimer: NodeJS.Timeout

  constructor() {
    this.cleanupTimer = setInterval(() => this.cleanup(), 60_000)
    this.cleanupTimer.unref()
  }

  async getJson<T>(key: string): Promise<T | undefined> {
    const entry = this.entries.get(key)
    if (!entry) return undefined
    if (entry.expiresAt <= Date.now()) {
      this.entries.delete(key)
      return undefined
    }
    return entry.value as T
  }

  async setJson<T>(key: string, value: T, ttlMs: number): Promise<void> {
    this.entries.set(key, {
      value: value as AnthropicOAuthSession,
      expiresAt: Date.now() + Math.max(1, Math.trunc(ttlMs))
    })
  }

  async compareDeleteJson<T>(key: string, expectedValue: T): Promise<boolean> {
    const current = await this.getJson<T>(key)
    if (!current || JSON.stringify(current) !== JSON.stringify(expectedValue)) return false
    this.entries.delete(key)
    return true
  }

  private cleanup(): void {
    const now = Date.now()
    for (const [key, entry] of this.entries) {
      if (entry.expiresAt <= now) this.entries.delete(key)
    }
  }
}
