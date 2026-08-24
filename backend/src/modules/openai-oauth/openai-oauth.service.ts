import { createHash, randomBytes } from 'node:crypto'
import type { AgentOptions } from 'node:http'

import { runtimeConfig } from '../../config/runtime.js'
import { OAuthUpstreamResponseError } from '../../shared/oauth-upstream-response-error.js'
import { passiveScheduleDelayMs } from '../../shared/passive-schedule-jitter.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../../shared/rfc3339.js'
import { createRuntimeStateStore, type RuntimeStateStore } from '../../shared/runtime-state-store.js'
import { HttpsProxyAgent } from 'https-proxy-agent'
import { SocksProxyAgent } from 'socks-proxy-agent'
import { requestProviderOAuthToken } from '../providers/drivers/_shared/provider-oauth-token-transport.js'

export const OPENAI_OAUTH_CLIENT_ID = 'app_EMoamEEZ73f0CkXaXp7hrann'
export const OPENAI_OAUTH_AUTHORIZE_URL = 'https://auth.openai.com/oauth/authorize'
export const OPENAI_OAUTH_TOKEN_URL = 'https://auth.openai.com/oauth/token'
export const OPENAI_OAUTH_DEFAULT_REDIRECT_URI = 'http://localhost:1455/auth/callback'
export const OPENAI_OAUTH_DEFAULT_SCOPES = 'openid profile email offline_access'
export const OPENAI_OAUTH_REFRESH_SCOPES = 'openid profile email'
export const openAIOAuthTokenResponseMaxBytes = 256 * 1024
export const openAIOAuthTokenRequestTimeoutMs = 25_000

export interface OpenAIOAuthSession {
  state: string
  codeVerifier: string
  redirectUri: string
  clientId: string
  ownerSystemAccountId?: string
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

const sessionTtlMs = 30 * 60 * 1000
export const openAIOAuthMemorySessionMaxEntries = 1024
export const openAIOAuthOwnerSessionMaxEntries = 8
const openAIOAuthMemorySessionCleanupIntervalMs = 60 * 1000

type OpenAIOAuthSessionStore = Pick<RuntimeStateStore, 'getJson' | 'setJson' | 'compareDeleteJson'>

interface OpenAIOAuthMemorySessionStoreOptions {
  now?: () => number
  maxEntries?: number
  maxOwnerSessions?: number
  cleanupIntervalMs?: number
}

interface OpenAIOAuthMemorySessionEntry {
  value: OpenAIOAuthSession
  expiresAt: number
}

export class OpenAIOAuthMemorySessionStore implements OpenAIOAuthSessionStore {
  private readonly entries = new Map<string, OpenAIOAuthMemorySessionEntry>()
  private readonly now: () => number
  private readonly maxEntries: number
  private readonly maxOwnerSessions: number
  private cleanupTimer: NodeJS.Timeout

  constructor(options: OpenAIOAuthMemorySessionStoreOptions = {}) {
    this.now = options.now ?? Date.now
    this.maxEntries = positiveInteger(options.maxEntries, openAIOAuthMemorySessionMaxEntries, 'maxEntries')
    this.maxOwnerSessions = positiveInteger(options.maxOwnerSessions, openAIOAuthOwnerSessionMaxEntries, 'maxOwnerSessions')
    const cleanupIntervalMs = positiveInteger(
      options.cleanupIntervalMs,
      openAIOAuthMemorySessionCleanupIntervalMs,
      'cleanupIntervalMs'
    )
    this.cleanupTimer = this.scheduleNextMaintenance(cleanupIntervalMs)
  }

  private scheduleNextMaintenance(intervalMs: number): NodeJS.Timeout {
    const timer = setTimeout(() => {
      this.maintain()
      this.cleanupTimer = this.scheduleNextMaintenance(intervalMs)
    }, passiveScheduleDelayMs(intervalMs))
    timer.unref()
    return timer
  }

  get size(): number {
    this.maintain()
    return this.entries.size
  }

  async getJson<T>(key: string): Promise<T | undefined> {
    const entry = this.freshEntry(key)
    return entry?.value as T | undefined
  }

  async setJson<T>(key: string, value: T, ttlMs: number): Promise<void> {
    const session = value as OpenAIOAuthSession
    this.maintain()
    this.entries.delete(key)
    const owner = openAIOAuthSessionOwner(session)
    while (this.ownerSessionCount(owner) >= this.maxOwnerSessions) {
      if (!this.deleteOldest((entry) => openAIOAuthSessionOwner(entry.value) === owner)) break
    }
    while (this.entries.size >= this.maxEntries) {
      if (!this.deleteOldest()) break
    }
    this.entries.set(key, {
      value: session,
      expiresAt: this.now() + normalizeSessionTtlMs(ttlMs)
    })
  }

  async compareDeleteJson<T>(key: string, expectedValue: T): Promise<boolean> {
    const current = this.freshEntry(key)
    if (!current || JSON.stringify(current.value) !== JSON.stringify(expectedValue)) {
      return false
    }
    this.entries.delete(key)
    return true
  }

  maintain(): void {
    const now = this.now()
    for (const [key, entry] of this.entries) {
      if (entry.expiresAt <= now) this.entries.delete(key)
    }
  }

  close(): void {
    clearInterval(this.cleanupTimer)
    this.entries.clear()
  }

  private freshEntry(key: string): OpenAIOAuthMemorySessionEntry | undefined {
    const entry = this.entries.get(key)
    if (!entry) return undefined
    if (entry.expiresAt <= this.now()) {
      this.entries.delete(key)
      return undefined
    }
    return entry
  }

  private ownerSessionCount(owner: string): number {
    let count = 0
    for (const entry of this.entries.values()) {
      if (openAIOAuthSessionOwner(entry.value) === owner) count += 1
    }
    return count
  }

  private deleteOldest(predicate: (entry: OpenAIOAuthMemorySessionEntry) => boolean = () => true): boolean {
    for (const [key, entry] of this.entries) {
      if (!predicate(entry)) continue
      this.entries.delete(key)
      return true
    }
    return false
  }
}

export async function generateOpenAIAuthURL(ownerSystemAccountId?: string): Promise<OpenAIAuthURLResult> {
  const state = randomBytes(32).toString('hex')
  const codeVerifier = randomBytes(64).toString('hex')
  const codeChallenge = createHash('sha256').update(codeVerifier).digest('base64url')
  const sessionId = randomBytes(16).toString('hex')
  const redirectUri = OPENAI_OAUTH_DEFAULT_REDIRECT_URI
  const clientId = OPENAI_OAUTH_CLIENT_ID

  await oauthSessionStore().setJson<OpenAIOAuthSession>(sessionId, {
    state,
    codeVerifier,
    redirectUri,
    clientId,
    ownerSystemAccountId: normalizeString(ownerSystemAccountId) || undefined,
    createdAt: Date.now()
  }, sessionTtlMs)

  return {
    authUrl: buildOpenAIOAuthAuthorizeUrl({ clientId, redirectUri, state, codeChallenge }),
    sessionId
  }
}

export function buildOpenAIOAuthAuthorizeUrl(input: {
  clientId: string
  redirectUri: string
  state: string
  codeChallenge: string
}): string {
  const params = new URLSearchParams({
    response_type: 'code',
    client_id: input.clientId,
    redirect_uri: input.redirectUri,
    scope: OPENAI_OAUTH_DEFAULT_SCOPES,
    state: input.state,
    code_challenge: input.codeChallenge,
    code_challenge_method: 'S256',
    id_token_add_organizations: 'true',
    codex_cli_simplified_flow: 'true'
  })
  return `${OPENAI_OAUTH_AUTHORIZE_URL}?${params.toString()}`
}

export async function exchangeOpenAIAuthCode(input: {
  sessionId: string
  code: string
  state: string
  ownerSystemAccountId?: string
  proxyUrl?: string
  signal?: AbortSignal
}): Promise<OpenAITokenInfo> {
  const session = await readOpenAIOAuthSession(input)
  const tokenInfo = await requestOpenAIToken({
    grant_type: 'authorization_code',
    client_id: session.clientId,
    code: input.code,
    redirect_uri: session.redirectUri,
    code_verifier: session.codeVerifier
  }, input.proxyUrl, input.signal)
  if (!await oauthSessionStore().compareDeleteJson(input.sessionId, session)) {
    throw new Error('OAuth 会话已消费，请重新发起授权')
  }
  return tokenInfo
}

export async function consumeOpenAIOAuthSession(input: {
  sessionId: string
  state: string
  ownerSystemAccountId?: string
}): Promise<OpenAIOAuthSession> {
  const session = await readOpenAIOAuthSession(input)
  if (!await oauthSessionStore().compareDeleteJson(input.sessionId, session)) {
    throw new Error('OAuth 会话已消费，请重新发起授权')
  }
  return session
}

async function readOpenAIOAuthSession(input: {
  sessionId: string
  state: string
  ownerSystemAccountId?: string
}): Promise<OpenAIOAuthSession> {
  const sessionStore = oauthSessionStore()
  const session = await sessionStore.getJson<OpenAIOAuthSession>(input.sessionId)
  if (!session) {
    throw new Error('OAuth 会话不存在或已过期')
  }
  if (!input.state || input.state !== session.state) {
    throw new Error('OAuth state 无效')
  }
  const expectedOwner = normalizeString(session.ownerSystemAccountId)
  const actualOwner = normalizeString(input.ownerSystemAccountId)
  if (expectedOwner && actualOwner !== expectedOwner) {
    throw new Error('OAuth session owner 归属无效')
  }
  return session
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
    expires_at: requiredRfc3339Instant(tokenInfo.expiresAt, 'OpenAI OAuth expiresAt'),
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
  const { code, state } = parseOAuthAuthorizationInput(callbackUrl)
  if (!code || !state) {
    throw new Error('回调 URL 必须包含 code 和 state')
  }
  return { code, state }
}

function parseOAuthAuthorizationInput(value: string): { code: string; state: string } {
  try {
    const url = new URL(value)
    const error = normalizeString(url.searchParams.get('error'))
    if (error) throw new OAuthUpstreamResponseError(normalizeString(url.searchParams.get('error_description')) || error)
    const queryCode = normalizeString(url.searchParams.get('code'))
    const queryState = normalizeString(url.searchParams.get('state'))
    if (queryCode || queryState) return { code: queryCode, state: queryState }
    const fragment = new URLSearchParams(url.hash.replace(/^#/u, ''))
    return { code: normalizeString(fragment.get('code')), state: normalizeString(fragment.get('state')) }
  } catch (error) {
    if (error instanceof Error && !error.message.includes('Invalid URL')) throw error
  }
  const hashSeparator = value.lastIndexOf('#')
  if (hashSeparator > 0) {
    return {
      code: normalizeString(value.slice(0, hashSeparator)),
      state: normalizeString(value.slice(hashSeparator + 1))
    }
  }
  const query = new URLSearchParams(value.replace(/^\?/u, ''))
  return { code: normalizeString(query.get('code')), state: normalizeString(query.get('state')) }
}

export function shouldRefreshOpenAIOAuthCredentials(credentials: Record<string, unknown>): boolean {
  const expiresAtText = optionalOpenAIOAuthExpiresAt(credentials)
  const refreshToken = normalizeString(credentials.refresh_token)
  if (!refreshToken) return false
  if (!expiresAtText) return true
  const expiresAt = rfc3339InstantMilliseconds(expiresAtText)
  if (expiresAt === undefined) {
    throw new Error('OpenAI OAuth credentials.expires_at必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  return expiresAt - Date.now() < 60_000
}

export function parseOpenAIOAuthExpiresIn(value: unknown): number {
  const expiresIn = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(expiresIn) || expiresIn <= 0) {
    throw new Error('OpenAI OAuth 令牌响应的 expires_in 必须是有限正数')
  }
  const normalized = Math.trunc(expiresIn)
  if (normalized <= 0) {
    throw new Error('OpenAI OAuth 令牌响应的 expires_in 必须是至少 1 秒')
  }
  return normalized
}

export function sanitizeOpenAIOAuthErrorMessage(message: string): string {
  return message
}

async function requestOpenAIToken(form: Record<string, string>, proxyUrl?: string, signal?: AbortSignal): Promise<OpenAITokenInfo> {
  const tokenRequest = buildOpenAIOAuthTokenHttpRequest(form)
  const response = await performTokenRequest(tokenRequest, proxyUrl, signal)

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
    throw new OAuthUpstreamResponseError(`OpenAI OAuth 令牌请求失败：HTTP ${response.statusCode}${errorDescription ? `，${errorDescription}` : ''}`, response.statusCode)
  }

  const accessToken = normalizeString(payload.access_token)
  if (!accessToken) {
    throw new Error('OpenAI OAuth 令牌响应缺少访问令牌')
  }
  const expiresIn = parseOpenAIOAuthExpiresIn(payload.expires_in)
  const idToken = normalizeString(payload.id_token)
  const refreshToken = normalizeString(payload.refresh_token)
  const clientId = normalizeString(form.client_id) || OPENAI_OAUTH_CLIENT_ID
  const idClaims = decodeJwtClaims(idToken)
  const accessClaims = decodeJwtClaims(accessToken)
  const idOpenAIAuth = plainObject(idClaims?.['https://api.openai.com/auth'])
  const accessOpenAIAuth = plainObject(accessClaims?.['https://api.openai.com/auth'])

  return {
    accessToken,
    refreshToken,
    idToken,
    expiresIn,
    expiresAt: new Date(Date.now() + expiresIn * 1000).toISOString(),
    clientId,
    email: normalizeString(idClaims?.email) || normalizeString(accessClaims?.email),
    accountId: normalizeString(idOpenAIAuth?.chatgpt_account_id) || normalizeString(accessOpenAIAuth?.chatgpt_account_id),
    chatgptUserId: normalizeString(idOpenAIAuth?.chatgpt_user_id)
      || normalizeString(idOpenAIAuth?.user_id)
      || normalizeString(accessOpenAIAuth?.chatgpt_user_id)
      || normalizeString(accessOpenAIAuth?.user_id),
    planType: normalizeString(idOpenAIAuth?.chatgpt_plan_type) || normalizeString(accessOpenAIAuth?.chatgpt_plan_type)
  }
}

export function buildOpenAIOAuthTokenHttpRequest(form: Record<string, string>): {
  body: string
  headers: Record<string, string | number>
} {
  const body = new URLSearchParams(form).toString()
  const headers: Record<string, string | number> = {
    accept: 'application/json',
    'content-type': 'application/x-www-form-urlencoded',
    'content-length': Buffer.byteLength(body)
  }
  return {
    body,
    headers
  }
}

async function performTokenRequest(
  tokenRequest: ReturnType<typeof buildOpenAIOAuthTokenHttpRequest>,
  proxyUrl?: string,
  signal?: AbortSignal
): Promise<{ statusCode: number; body: string }> {
  const resolvedProxyUrl = normalizeString(proxyUrl) || runtimeConfig.oauthProxyUrl
  if (signal?.aborted) throw new Error('请求已取消')
  const response = await requestProviderOAuthToken({
    url: OPENAI_OAUTH_TOKEN_URL,
    headers: new Headers(Object.entries(tokenRequest.headers).map(([key, value]): [string, string] => [key, String(value)])),
    body: tokenRequest.body,
    proxyUrl: resolvedProxyUrl,
    signal,
    timeoutMs: openAIOAuthTokenRequestTimeoutMs,
    maxResponseBytes: openAIOAuthTokenResponseMaxBytes
  })
  if (response.truncated) throw new Error('OpenAI OAuth 令牌响应体过大')
  return { statusCode: response.statusCode, body: response.bodyText }
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

function plainObject(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function normalizeString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function optionalOpenAIOAuthExpiresAt(credentials: Record<string, unknown>): string | undefined {
  if (credentials.expires_at === undefined) return undefined
  return requiredRfc3339Instant(credentials.expires_at, 'OpenAI OAuth credentials.expires_at')
}

let memoryOpenAIOAuthSessionStore: OpenAIOAuthMemorySessionStore | undefined

function oauthSessionStore(): OpenAIOAuthSessionStore {
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    return createRuntimeStateStore('openai-oauth:sessions')
  }
  memoryOpenAIOAuthSessionStore = memoryOpenAIOAuthSessionStore ?? new OpenAIOAuthMemorySessionStore()
  return memoryOpenAIOAuthSessionStore
}

function openAIOAuthSessionOwner(session: OpenAIOAuthSession): string {
  return normalizeString(session.ownerSystemAccountId) || '__anonymous__'
}

function normalizeSessionTtlMs(ttlMs: number): number {
  return Number.isFinite(ttlMs) ? Math.max(1, Math.trunc(ttlMs)) : 1
}

function positiveInteger(value: number | undefined, fallback: number, field: string): number {
  const normalized = value === undefined ? fallback : Math.trunc(value)
  if (!Number.isFinite(normalized) || normalized < 1) {
    throw new Error(`OpenAI OAuth memory session ${field} 必须是正整数`)
  }
  return normalized
}
