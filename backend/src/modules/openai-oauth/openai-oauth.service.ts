import { createHash, randomBytes } from 'node:crypto'
import type { AgentOptions } from 'node:http'
import { request as httpsRequest } from 'node:https'

import { runtimeConfig } from '../../config/runtime.js'
import { BoundedBufferCollector } from '../../shared/bounded-buffer.js'
import { sanitizeDiagnosticPayload } from '../gateway/audit/payload-sanitizer.js'
import { HttpsProxyAgent } from 'https-proxy-agent'
import { SocksProxyAgent } from 'socks-proxy-agent'

export const OPENAI_OAUTH_CLIENT_ID = 'app_EMoamEEZ73f0CkXaXp7hrann'
export const OPENAI_OAUTH_AUTHORIZE_URL = 'https://auth.openai.com/oauth/authorize'
export const OPENAI_OAUTH_TOKEN_URL = 'https://auth.openai.com/oauth/token'
export const OPENAI_OAUTH_DEFAULT_REDIRECT_URI = 'http://localhost:1455/auth/callback'
export const OPENAI_OAUTH_DEFAULT_SCOPES = 'openid profile email offline_access'
export const OPENAI_OAUTH_REFRESH_SCOPES = 'openid profile email'
export const openAIOAuthTokenResponseMaxBytes = 256 * 1024

interface OAuthSession {
  state: string
  codeVerifier: string
  redirectUri: string
  clientId: string
  createdAt: number
}

export interface OpenAIAuthURLResult {
  authUrl: string
  sessionId: string
}

export interface OpenAITokenInfo {
  accessToken: string
  refreshToken?: string
  idToken?: string
  expiresIn: number
  expiresAt: string
  clientId: string
  email?: string
  accountId?: string
  chatgptUserId?: string
  planType?: string
}

const sessions = new Map<string, OAuthSession>()
const sessionTtlMs = 30 * 60 * 1000

export function generateOpenAIAuthURL(): OpenAIAuthURLResult {
  cleanupExpiredSessions()
  const state = randomBytes(32).toString('hex')
  const codeVerifier = randomBytes(64).toString('hex')
  const codeChallenge = createHash('sha256').update(codeVerifier).digest('base64url')
  const sessionId = randomBytes(16).toString('hex')
  const redirectUri = OPENAI_OAUTH_DEFAULT_REDIRECT_URI
  const clientId = OPENAI_OAUTH_CLIENT_ID

  sessions.set(sessionId, {
    state,
    codeVerifier,
    redirectUri,
    clientId,
    createdAt: Date.now()
  })

  const params = new URLSearchParams({
    response_type: 'code',
    client_id: clientId,
    redirect_uri: redirectUri,
    scope: OPENAI_OAUTH_DEFAULT_SCOPES,
    state,
    code_challenge: codeChallenge,
    code_challenge_method: 'S256',
    codex_cli_simplified_flow: 'true'
  })

  return {
    authUrl: `${OPENAI_OAUTH_AUTHORIZE_URL}?${params.toString()}`,
    sessionId
  }
}

export async function exchangeOpenAIAuthCode(input: {
  sessionId: string
  code: string
  state: string
  proxyUrl?: string
  signal?: AbortSignal
}): Promise<OpenAITokenInfo> {
  cleanupExpiredSessions()
  const session = sessions.get(input.sessionId)
  if (!session) {
    throw new Error('OAuth 会话不存在或已过期')
  }
  if (!input.state || input.state !== session.state) {
    throw new Error('OAuth state 无效')
  }
  const tokenInfo = await requestOpenAIToken({
    grant_type: 'authorization_code',
    client_id: session.clientId,
    code: input.code,
    redirect_uri: session.redirectUri,
    code_verifier: session.codeVerifier
  }, input.proxyUrl, input.signal)
  sessions.delete(input.sessionId)
  return tokenInfo
}

export async function refreshOpenAIOAuthToken(input: { refreshToken: string; clientId?: string; proxyUrl?: string; signal?: AbortSignal }): Promise<OpenAITokenInfo> {
  const refreshToken = normalizeString(input.refreshToken)
  if (!refreshToken) {
    throw new Error('刷新令牌不能为空')
  }
  const clientId = normalizeString(input.clientId) || OPENAI_OAUTH_CLIENT_ID
  return requestOpenAIToken({
    grant_type: 'refresh_token',
    refresh_token: refreshToken,
    client_id: clientId,
    scope: OPENAI_OAUTH_REFRESH_SCOPES
  }, input.proxyUrl, input.signal)
}

export function buildOpenAIOAuthCredentials(tokenInfo: OpenAITokenInfo, fallback?: { refreshToken?: string }): Record<string, unknown> {
  const credentials: Record<string, unknown> = {
    access_token: tokenInfo.accessToken,
    expires_at: tokenInfo.expiresAt,
    client_id: tokenInfo.clientId,
    base_url: 'https://api.openai.com/v1'
  }
  const refreshToken = normalizeString(tokenInfo.refreshToken) || normalizeString(fallback?.refreshToken)
  if (refreshToken) credentials.refresh_token = refreshToken
  if (tokenInfo.idToken) credentials.id_token = tokenInfo.idToken
  if (tokenInfo.email) credentials.email = tokenInfo.email
  if (tokenInfo.accountId) credentials.account_id = tokenInfo.accountId
  if (tokenInfo.chatgptUserId) credentials.chatgpt_user_id = tokenInfo.chatgptUserId
  if (tokenInfo.planType) credentials.plan_type = tokenInfo.planType
  return credentials
}

export function extractCodeAndState(input: { callbackUrl: string }): { code: string; state: string } {
  const callbackUrl = normalizeString(input.callbackUrl)
  if (!callbackUrl) {
    throw new Error('回调 URL 不能为空')
  }
  const url = new URL(callbackUrl)
  const code = normalizeString(url.searchParams.get('code'))
  const state = normalizeString(url.searchParams.get('state'))
  if (!code || !state) {
    throw new Error('回调 URL 必须包含 code 和 state')
  }
  return { code, state }
}

export function shouldRefreshOpenAIOAuthCredentials(credentials: Record<string, unknown>): boolean {
  const refreshToken = normalizeString(credentials.refresh_token)
  if (!refreshToken) return false
  const expiresAtText = normalizeString(credentials.expires_at)
  if (!expiresAtText) return true
  const expiresAt = Date.parse(expiresAtText)
  if (!Number.isFinite(expiresAt)) return true
  return expiresAt - Date.now() < 60_000
}

export function sanitizeOpenAIOAuthErrorMessage(message: string): string {
  return sanitizeDiagnosticPayload(message)
}

async function requestOpenAIToken(form: Record<string, string>, proxyUrl?: string, signal?: AbortSignal): Promise<OpenAITokenInfo> {
  const bodyText = new URLSearchParams(form).toString()
  const response = await performTokenRequest(bodyText, proxyUrl, signal)

  const text = response.body
  let payload: Record<string, unknown> = {}
  if (text) {
    try {
      payload = JSON.parse(text) as Record<string, unknown>
    } catch {
      payload = { raw: text }
    }
  }

  if (response.statusCode < 200 || response.statusCode >= 300) {
    const errorDescription = sanitizeOpenAIOAuthErrorMessage(normalizeString(payload.error_description) || normalizeString(payload.error) || text)
    throw new Error(`OpenAI OAuth 令牌请求失败：HTTP ${response.statusCode}${errorDescription ? `，${errorDescription}` : ''}`)
  }

  const accessToken = normalizeString(payload.access_token)
  if (!accessToken) {
    throw new Error('OpenAI OAuth 令牌响应缺少访问令牌')
  }
  const expiresIn = typeof payload.expires_in === 'number' ? payload.expires_in : Number(payload.expires_in ?? 0)
  const idToken = normalizeString(payload.id_token)
  const refreshToken = normalizeString(payload.refresh_token)
  const clientId = normalizeString(form.client_id) || OPENAI_OAUTH_CLIENT_ID
  const claims = decodeJwtClaims(idToken) ?? decodeJwtClaims(accessToken)
  const openAIAuth = claims?.['https://api.openai.com/auth'] as Record<string, unknown> | undefined

  return {
    accessToken,
    refreshToken,
    idToken,
    expiresIn,
    expiresAt: new Date(Date.now() + Math.max(expiresIn, 0) * 1000).toISOString(),
    clientId,
    email: normalizeString(claims?.email),
    accountId: normalizeString(openAIAuth?.chatgpt_account_id),
    chatgptUserId: normalizeString(openAIAuth?.chatgpt_user_id) || normalizeString(openAIAuth?.user_id),
    planType: normalizeString(openAIAuth?.chatgpt_plan_type)
  }
}

async function performTokenRequest(bodyText: string, proxyUrl?: string, signal?: AbortSignal): Promise<{ statusCode: number; body: string }> {
  const resolvedProxyUrl = normalizeString(proxyUrl) || runtimeConfig.oauthProxyUrl
  const agent = resolvedProxyUrl ? createProxyAgent(resolvedProxyUrl) : undefined
  const response = await new Promise<{ statusCode: number; body: string }>((resolve, reject) => {
    if (signal?.aborted) {
      reject(new Error('请求已取消'))
      return
    }
    const request = httpsRequest(OPENAI_OAUTH_TOKEN_URL, {
      method: 'POST',
      headers: {
        'content-type': 'application/x-www-form-urlencoded',
        'content-length': Buffer.byteLength(bodyText),
        'user-agent': 'codex-cli/0.91.0'
      },
      agent,
      timeout: 120000
    }, (response) => {
      const body = new BoundedBufferCollector(openAIOAuthTokenResponseMaxBytes)
      response.on('data', (chunk: Buffer) => {
        body.append(chunk)
        if (body.truncated) {
          request.destroy(new Error('OpenAI OAuth 令牌响应体过大'))
        }
      })
      response.on('end', () => {
        resolve({ statusCode: response.statusCode ?? 0, body: body.text() })
      })
    })

    const abort = () => request.destroy(new Error('请求已取消'))
    signal?.addEventListener('abort', abort, { once: true })
    const cleanupAbortSignal = () => signal?.removeEventListener('abort', abort)
    request.on('error', (error) => {
      cleanupAbortSignal()
      reject(error)
    })
    request.on('response', cleanupAbortSignal)
    request.on('close', cleanupAbortSignal)
    request.on('timeout', () => request.destroy(new Error('OpenAI OAuth 令牌请求超时')))
    request.end(bodyText)
  })
  return response
}

export function createProxyAgent(proxyUrl: string, options: AgentOptions = {}): HttpsProxyAgent<string> | SocksProxyAgent {
  const parsed = new URL(proxyUrl)
  if (parsed.protocol === 'http:' || parsed.protocol === 'https:') {
    return new HttpsProxyAgent(proxyUrl, options)
  }
  if (parsed.protocol === 'socks4:' || parsed.protocol === 'socks4a:' || parsed.protocol === 'socks5:' || parsed.protocol === 'socks5h:') {
    return new SocksProxyAgent(proxyUrl, options)
  }
  throw new Error(`不支持的代理协议：${parsed.protocol}`)
}

function decodeJwtClaims(token?: string): Record<string, unknown> | undefined {
  const tokenText = normalizeString(token)
  if (!tokenText) return undefined
  const parts = tokenText.split('.')
  if (parts.length !== 3) return undefined
  try {
    return JSON.parse(Buffer.from(parts[1], 'base64url').toString('utf8')) as Record<string, unknown>
  } catch {
    return undefined
  }
}

function cleanupExpiredSessions(): void {
  const now = Date.now()
  for (const [sessionId, session] of sessions.entries()) {
    if (now - session.createdAt > sessionTtlMs) {
      sessions.delete(sessionId)
    }
  }
}

function normalizeString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}
