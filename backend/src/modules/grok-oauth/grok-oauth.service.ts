import { createHash, randomBytes, timingSafeEqual } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import { createRuntimeStateStore, type RuntimeStateStore } from '../../shared/runtime-state-store.js'
import { sanitizeDiagnosticPayload } from '../gateway/diagnostics/diagnostic-sanitizer.js'
import { requestProviderOAuthToken } from '../providers/drivers/_shared/provider-oauth-token-transport.js'
import { convertGrokSSOToOAuth } from './grok-sso-device-flow.js'

export const GROK_OAUTH_AUTHORIZE_URL = 'https://auth.x.ai/oauth2/authorize'
export const GROK_OAUTH_TOKEN_URL = 'https://auth.x.ai/oauth2/token'
export const GROK_OAUTH_CLIENT_ID = 'b1a00492-073a-47ea-816f-4c329264a828'
export const GROK_OAUTH_SCOPE = 'openid profile email offline_access grok-cli:access api:access'
export const GROK_OAUTH_REDIRECT_URI = 'http://127.0.0.1:56121/callback'
export const GROK_OAUTH_BASE_URL = 'https://cli-chat-proxy.grok.com/v1'
export const grokOAuthResponseMaxBytes = 256 * 1024
export const grokOAuthRequestTimeoutMs = 60_000

const grokOAuthSessionTtlMs = 30 * 60 * 1000
const grokDefaultAccessTokenTtlSeconds = 6 * 60 * 60

export interface GrokOAuthSession {
  state: string
  nonce: string
  codeVerifier: string
  codeChallenge: string
  clientId: string
  scope: string
  redirectUri: string
  ownerSystemAccountId?: string
  createdAt: number
}

export interface GrokOAuthAuthURLResult {
  authUrl: string
  sessionId: string
  state: string
}

export interface GrokOAuthTokenInfo {
  accessToken: string
  refreshToken?: string
  idToken?: string
  tokenType: string
  expiresIn: number
  expiresAt: string
  clientId: string
  scope?: string
  email?: string
  subject?: string
  teamId?: string
  subscriptionTier?: string
  entitlementStatus?: string
}

export class GrokOAuthError extends Error {
  constructor(message: string, readonly statusCode: number) {
    super(message)
    this.name = 'GrokOAuthError'
  }
}

type GrokOAuthSessionStore = Pick<RuntimeStateStore, 'getJson' | 'setJson' | 'compareDeleteJson'>

export async function generateGrokAuthURL(ownerSystemAccountId?: string): Promise<GrokOAuthAuthURLResult> {
  const state = randomBytes(32).toString('hex')
  const nonce = randomBytes(16).toString('hex')
  const codeVerifier = randomBytes(32).toString('base64url')
  const codeChallenge = createHash('sha256').update(codeVerifier).digest('base64url')
  const sessionId = randomBytes(16).toString('hex')

  await grokOAuthSessionStore().setJson<GrokOAuthSession>(sessionId, {
    state,
    nonce,
    codeVerifier,
    codeChallenge,
    clientId: GROK_OAUTH_CLIENT_ID,
    scope: GROK_OAUTH_SCOPE,
    redirectUri: GROK_OAUTH_REDIRECT_URI,
    ownerSystemAccountId: normalizeString(ownerSystemAccountId) || undefined,
    createdAt: Date.now()
  }, grokOAuthSessionTtlMs)

  return {
    authUrl: buildGrokAuthorizeUrl({ state, nonce, codeChallenge }),
    sessionId,
    state
  }
}

export function buildGrokAuthorizeUrl(input: {
  state: string
  nonce: string
  codeChallenge: string
  clientId?: string
  scope?: string
  redirectUri?: string
}): string {
  const params = new URLSearchParams({
    response_type: 'code',
    client_id: normalizeString(input.clientId) || GROK_OAUTH_CLIENT_ID,
    redirect_uri: normalizeString(input.redirectUri) || GROK_OAUTH_REDIRECT_URI,
    scope: normalizeString(input.scope) || GROK_OAUTH_SCOPE,
    state: input.state,
    nonce: input.nonce,
    code_challenge: input.codeChallenge,
    code_challenge_method: 'S256',
    plan: 'generic',
    referrer: 'sub2api'
  })
  return `${GROK_OAUTH_AUTHORIZE_URL}?${params.toString()}`
}

export async function exchangeGrokAuthCode(input: {
  sessionId: string
  callbackUrl: string
  ownerSystemAccountId?: string
  proxyUrl?: string
  signal?: AbortSignal
}): Promise<GrokOAuthTokenInfo> {
  const authorization = parseGrokAuthorizationInput(input.callbackUrl)
  if (!authorization.code) throw new GrokOAuthError('Grok OAuth 授权码不能为空', 400)
  const sessionInput = {
    sessionId: input.sessionId,
    state: authorization.state,
    requiresState: authorization.requiresState,
    ownerSystemAccountId: input.ownerSystemAccountId
  }
  const session = await readGrokOAuthSession(sessionInput)

  const tokenInfo = await requestGrokToken({
    grant_type: 'authorization_code',
    client_id: session.clientId,
    code: authorization.code,
    redirect_uri: session.redirectUri,
    code_verifier: session.codeVerifier
  }, session.clientId, input.proxyUrl, input.signal)
  if (!await grokOAuthSessionStore().compareDeleteJson(input.sessionId, session)) {
    throw new GrokOAuthError('Grok OAuth 会话已消费，请重新发起授权', 400)
  }
  return tokenInfo
}

export async function refreshGrokAuthToken(input: {
  refreshToken: string
  clientId?: string
  proxyUrl?: string
  signal?: AbortSignal
}): Promise<GrokOAuthTokenInfo> {
  const refreshToken = normalizeString(input.refreshToken)
  if (!refreshToken) throw new GrokOAuthError('Grok Refresh Token 不能为空', 400)
  const clientId = normalizeString(input.clientId) || GROK_OAUTH_CLIENT_ID
  const tokenInfo = await requestGrokToken({
    grant_type: 'refresh_token',
    client_id: clientId,
    refresh_token: refreshToken
  }, clientId, input.proxyUrl, input.signal)
  if (!tokenInfo.refreshToken) tokenInfo.refreshToken = refreshToken
  return tokenInfo
}

export async function exchangeGrokSSOToken(input: {
  ssoToken: string
  proxyUrl?: string
  signal?: AbortSignal
}): Promise<GrokOAuthTokenInfo> {
  const payload = await convertGrokSSOToOAuth(input)
  return toGrokOAuthTokenInfo(payload, GROK_OAUTH_CLIENT_ID)
}

export function buildGrokOAuthCredentials(
  tokenInfo: GrokOAuthTokenInfo,
  fallback?: { refreshToken?: string }
): Record<string, unknown> {
  const credentials: Record<string, unknown> = {
    access_token: tokenInfo.accessToken,
    expires_at: tokenInfo.expiresAt,
    token_type: tokenInfo.tokenType,
    client_id: tokenInfo.clientId,
    base_url: GROK_OAUTH_BASE_URL
  }
  const refreshToken = normalizeString(tokenInfo.refreshToken) || normalizeString(fallback?.refreshToken)
  if (refreshToken) credentials.refresh_token = refreshToken
  if (tokenInfo.idToken) credentials.id_token = tokenInfo.idToken
  if (tokenInfo.scope) credentials.scope = tokenInfo.scope
  if (tokenInfo.email) credentials.email = tokenInfo.email
  if (tokenInfo.subject) credentials.sub = tokenInfo.subject
  if (tokenInfo.teamId) credentials.team_id = tokenInfo.teamId
  if (tokenInfo.subscriptionTier) credentials.subscription_tier = tokenInfo.subscriptionTier
  if (tokenInfo.entitlementStatus) credentials.entitlement_status = tokenInfo.entitlementStatus
  return credentials
}

export function sanitizeGrokOAuthErrorMessage(message: string): string {
  return sanitizeDiagnosticPayload(message)
}

async function readGrokOAuthSession(input: {
  sessionId: string
  state?: string
  requiresState: boolean
  ownerSystemAccountId?: string
}): Promise<GrokOAuthSession> {
  const sessionStore = grokOAuthSessionStore()
  const sessionId = normalizeString(input.sessionId)
  const session = sessionId ? await sessionStore.getJson<GrokOAuthSession>(sessionId) : undefined
  if (!session) throw new GrokOAuthError('Grok OAuth 会话不存在或已过期', 400)

  const actualState = normalizeString(input.state)
  if (input.requiresState && !actualState) throw new GrokOAuthError('Grok OAuth 回调缺少 state', 400)
  if (actualState && !constantTimeEqual(actualState, session.state)) {
    throw new GrokOAuthError('Grok OAuth state 无效', 400)
  }

  const expectedOwner = normalizeString(session.ownerSystemAccountId)
  const actualOwner = normalizeString(input.ownerSystemAccountId)
  if (expectedOwner && actualOwner !== expectedOwner) {
    throw new GrokOAuthError('Grok OAuth session owner 归属无效', 400)
  }
  return session
}

function parseGrokAuthorizationInput(raw: string): { code: string; state?: string; requiresState: boolean } {
  const trimmed = normalizeString(raw)
  if (!trimmed) return { code: '', requiresState: false }

  try {
    const parsed = new URL(trimmed)
    const code = normalizeString(parsed.searchParams.get('code'))
    if (code) return { code, state: normalizeString(parsed.searchParams.get('state')) || undefined, requiresState: true }
  } catch {
    // A bare code or query string is also accepted by the xAI CLI flow.
  }

  const queryCandidate = trimmed.startsWith('?') ? trimmed.slice(1) : trimmed
  if (queryCandidate.includes('=')) {
    const params = new URLSearchParams(queryCandidate)
    const code = normalizeString(params.get('code'))
    if (code) return { code, state: normalizeString(params.get('state')) || undefined, requiresState: true }
  }
  return { code: trimmed, requiresState: false }
}

async function requestGrokToken(
  form: Record<string, string>,
  clientId: string,
  proxyUrl?: string,
  signal?: AbortSignal
): Promise<GrokOAuthTokenInfo> {
  const requestBody = new URLSearchParams(form).toString()
  const response = await performGrokTokenRequest({
    body: requestBody,
    headers: {
      'content-type': 'application/x-www-form-urlencoded',
      'content-length': Buffer.byteLength(requestBody),
      accept: 'application/json',
      'user-agent': 'sub2api-grok-oauth/1.0'
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
    const detail = sanitizeGrokOAuthErrorMessage(
      normalizeString(payload.error_description) || normalizeString(payload.error) || response.body
    )
    const statusCode = response.statusCode === 403 && hasExplicitEntitlementDenial(payload, response.body) ? 403 : 502
    throw new GrokOAuthError(
      `Grok OAuth 令牌请求失败：HTTP ${response.statusCode}${detail ? `，${detail}` : ''}`,
      statusCode
    )
  }

  const accessToken = normalizeString(payload.access_token)
  if (!accessToken) throw new Error('Grok OAuth 令牌响应缺少 access_token')
  return toGrokOAuthTokenInfo({
    accessToken,
    refreshToken: normalizeString(payload.refresh_token) || undefined,
    idToken: normalizeString(payload.id_token) || undefined,
    tokenType: normalizeString(payload.token_type) || 'Bearer',
    expiresIn: finitePositiveInteger(payload.expires_in) ?? grokDefaultAccessTokenTtlSeconds,
    scope: normalizeString(payload.scope) || undefined
  }, clientId)
}

function toGrokOAuthTokenInfo(payload: {
  accessToken: string
  refreshToken?: string
  idToken?: string
  tokenType: string
  expiresIn: number
  scope?: string
}, clientId: string): GrokOAuthTokenInfo {
  const expiresIn = finitePositiveInteger(payload.expiresIn) ?? grokDefaultAccessTokenTtlSeconds
  const claims = mergeJwtClaims(payload.idToken, payload.accessToken)
  return {
    accessToken: payload.accessToken,
    refreshToken: payload.refreshToken,
    idToken: payload.idToken,
    tokenType: payload.tokenType || 'Bearer',
    expiresIn,
    expiresAt: new Date(Date.now() + expiresIn * 1000).toISOString(),
    clientId: normalizeString(clientId) || GROK_OAUTH_CLIENT_ID,
    scope: payload.scope,
    email: stringClaim(claims, 'email'),
    subject: stringClaim(claims, 'sub'),
    teamId: stringClaim(claims, 'team_id'),
    subscriptionTier: stringClaim(claims, 'subscription_tier'),
    entitlementStatus: stringClaim(claims, 'entitlement_status')
  }
}

async function performGrokTokenRequest(
  request: { body: string; headers: Record<string, string | number> },
  proxyUrl?: string,
  signal?: AbortSignal
): Promise<{ statusCode: number; body: string }> {
  const resolvedProxyUrl = normalizeString(proxyUrl) || runtimeConfig.oauthProxyUrl
  if (signal?.aborted) throw new Error('请求已取消')
  const headers = new Headers()
  for (const [key, value] of Object.entries(request.headers)) headers.set(key, String(value))
  const response = await requestProviderOAuthToken({
    url: GROK_OAUTH_TOKEN_URL,
    headers,
    body: request.body,
    proxyUrl: resolvedProxyUrl,
    signal,
    timeoutMs: grokOAuthRequestTimeoutMs,
    maxResponseBytes: grokOAuthResponseMaxBytes
  })
  if (response.truncated) throw new Error('Grok OAuth 令牌响应体过大')
  return { statusCode: response.statusCode, body: response.bodyText }
}

function hasExplicitEntitlementDenial(payload: Record<string, unknown>, body: string): boolean {
  const structuredValues = ['error', 'code', 'reason'].map((key) => normalizeString(payload[key]).toLowerCase())
  const denialValues = new Set([
    'access_denied',
    'entitlement_denied',
    'subscription_required',
    'no_active_subscription'
  ])
  if (structuredValues.some((value) => denialValues.has(value))) return true
  const lowerBody = body.toLowerCase()
  return lowerBody.includes('entitlement denied')
    || lowerBody.includes('subscription required')
    || lowerBody.includes('no active grok subscription')
}

function mergeJwtClaims(...tokens: Array<string | undefined>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  for (const token of tokens) {
    for (const [key, value] of Object.entries(decodeJwtClaims(token))) {
      if (output[key] === undefined || output[key] === null || output[key] === '') output[key] = value
    }
  }
  return output
}

function decodeJwtClaims(token: string | undefined): Record<string, unknown> {
  const parts = normalizeString(token).split('.')
  if (parts.length < 2 || !parts[1]) return {}
  try {
    const value: unknown = JSON.parse(Buffer.from(parts[1], 'base64url').toString('utf8'))
    return isPlainObject(value) ? value : {}
  } catch {
    return {}
  }
}

function stringClaim(claims: Record<string, unknown>, key: string): string | undefined {
  return normalizeString(claims[key]) || undefined
}

function constantTimeEqual(actual: string, expected: string): boolean {
  const actualBuffer = Buffer.from(actual)
  const expectedBuffer = Buffer.from(expected)
  return actualBuffer.length === expectedBuffer.length && timingSafeEqual(actualBuffer, expectedBuffer)
}

function finitePositiveInteger(value: unknown): number | undefined {
  const parsed = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(parsed) || parsed <= 0) return undefined
  return Math.trunc(parsed)
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function normalizeString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

let memoryGrokOAuthSessionStore: GrokOAuthMemorySessionStore | undefined

function grokOAuthSessionStore(): GrokOAuthSessionStore {
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    return createRuntimeStateStore('grok-oauth:sessions')
  }
  memoryGrokOAuthSessionStore = memoryGrokOAuthSessionStore ?? new GrokOAuthMemorySessionStore()
  return memoryGrokOAuthSessionStore
}

class GrokOAuthMemorySessionStore implements GrokOAuthSessionStore {
  private readonly entries = new Map<string, { value: GrokOAuthSession; expiresAt: number }>()
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
      value: value as GrokOAuthSession,
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
