import { AsyncLocalStorage } from 'node:async_hooks'
import { createHash, randomBytes } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import { OAuthUpstreamResponseError } from '../../shared/oauth-upstream-response-error.js'
import { createRuntimeStateStore, type RuntimeStateStore } from '../../shared/runtime-state-store.js'
import { readUpstreamBodyLimited } from '../gateway/upstream/body.js'
import { requestUpstream } from '../gateway/upstream/request.js'
import { requestProviderOAuthToken } from '../providers/drivers/_shared/provider-oauth-token-transport.js'

export const GEMINI_OAUTH_AUTHORIZE_URL = 'https://accounts.google.com/o/oauth2/v2/auth'
export const GEMINI_OAUTH_TOKEN_URL = 'https://oauth2.googleapis.com/token'
export const GEMINI_OAUTH_REDIRECT_URI = 'http://localhost:1455/auth/callback'
export const GEMINI_CLI_OAUTH_REDIRECT_URI = 'https://codeassist.google.com/authcode'
export const GEMINI_CLI_OAUTH_CLIENT_ID = '681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com'
export const GEMINI_CLI_OAUTH_CLIENT_SECRET = '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'
export const GEMINI_OAUTH_SCOPE = 'https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/generative-language.retriever'
export const GEMINI_CODE_ASSIST_OAUTH_SCOPE = 'https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile'
export const GEMINI_GOOGLE_ONE_OAUTH_SCOPE = GEMINI_CODE_ASSIST_OAUTH_SCOPE
export const GEMINI_OAUTH_DEFAULT_BASE_URL = 'https://generativelanguage.googleapis.com'
export const GEMINI_CLI_DEFAULT_BASE_URL = 'https://cloudcode-pa.googleapis.com'
export const GEMINI_CLI_USER_AGENT = 'GeminiCLI/0.1.5 (Windows; AMD64)'
export const GEMINI_OAUTH_TYPES = ['code_assist', 'google_one', 'ai_studio'] as const
export type GeminiOAuthType = typeof GEMINI_OAUTH_TYPES[number]

export const geminiOAuthResponseMaxBytes = 256 * 1024
export const geminiOAuthRequestTimeoutMs = 25_000

const geminiCliEndpointModes = ['generate_content_json', 'generate_content_sse'] as const
const sessionTtlMs = 30 * 60 * 1000
const gibibyte = 1024 ** 3
const tebibyte = 1024 * gibibyte

export interface GeminiOAuthProbeBaseUrlsForTest {
  cloudCodeBaseUrl?: string
  cloudResourceManagerBaseUrl?: string
  googleApisBaseUrl?: string
}

const geminiOAuthProbeBaseUrlsForTest = new AsyncLocalStorage<GeminiOAuthProbeBaseUrlsForTest>()

export async function runWithGeminiOAuthProbeBaseUrlsForTest<T>(
  baseUrls: GeminiOAuthProbeBaseUrlsForTest,
  task: () => Promise<T>
): Promise<T> {
  return await geminiOAuthProbeBaseUrlsForTest.run(Object.freeze({ ...baseUrls }), task)
}

export interface GeminiOAuthSession {
  state: string
  codeVerifier: string
  scope: string
  redirectUri: string
  clientId: string
  clientSecret: string
  oauthType: GeminiOAuthType
  projectId?: string
  tierId?: string
  quotaProjectId?: string
  baseUrl: string
  ownerSystemAccountId?: string
  createdAt: number
}

export interface GeminiOAuthAuthURLResult {
  authUrl: string
  sessionId: string
  state: string
}

export interface GeminiOAuthTokenInfo {
  accessToken: string
  refreshToken?: string
  expiresIn?: number
  expiresAt?: string
  scope?: string
  tokenType?: string
  clientId: string
  clientSecret: string
  oauthType: GeminiOAuthType
  projectId?: string
  tierId?: string
  quotaProjectId?: string
  baseUrl: string
  driveStorageLimit?: number
  driveStorageUsage?: number
  driveTierUpdatedAt?: string
}

export interface GeminiOAuthClientCredentials {
  clientId?: string
  clientSecret?: string
}

export interface GeminiOAuthCapabilities {
  defaultOAuthType: GeminiOAuthType
  oauthTypes: Array<{
    oauthType: GeminiOAuthType
    label: string
    usesBuiltInClient: boolean
    requiresClientCredentials: boolean
    redirectUri: string
    scope: string
    supportsProjectId: boolean
    supportsTierId: boolean
    supportedEndpointModes: string[]
  }>
}

type GeminiOAuthSessionStore = Pick<RuntimeStateStore, 'getJson' | 'setJson' | 'compareDeleteJson'>

export function getGeminiOAuthCapabilities(): GeminiOAuthCapabilities {
  return {
    defaultOAuthType: 'code_assist',
    oauthTypes: [
      capability('code_assist', 'Gemini Code Assist', true, true, true),
      capability('google_one', 'Google One', true, true, true),
      capability('ai_studio', 'Google AI Studio', false, true, true)
    ]
  }
}

export async function generateGeminiAuthURL(input: GeminiOAuthClientCredentials & {
  ownerSystemAccountId?: string
  oauthType?: GeminiOAuthType
  projectId?: string
  tierId?: string
  quotaProjectId?: string
  baseUrl?: string
}): Promise<GeminiOAuthAuthURLResult> {
  const oauthType = normalizeOAuthType(input.oauthType)
  const oauthClient = resolveGeminiOAuthClient({ ...input, oauthType })
  const state = randomBytes(32).toString('base64url')
  const codeVerifier = randomBytes(32).toString('base64url')
  const codeChallenge = createHash('sha256').update(codeVerifier).digest('base64url')
  const sessionId = randomBytes(16).toString('hex')
  const projectId = normalizeString(input.projectId) || undefined
  const tierId = canonicalGeminiTierId(oauthType, input.tierId) || undefined
  const quotaProjectId = normalizeString(input.quotaProjectId) || undefined
  const baseUrl = normalizeString(input.baseUrl) || defaultBaseUrl(oauthType)

  await geminiOAuthSessionStore().setJson<GeminiOAuthSession>(sessionId, {
    state,
    codeVerifier,
    scope: oauthClient.scope,
    redirectUri: oauthClient.redirectUri,
    clientId: oauthClient.clientId,
    clientSecret: oauthClient.clientSecret,
    oauthType,
    projectId,
    tierId,
    quotaProjectId,
    baseUrl,
    ownerSystemAccountId: normalizeString(input.ownerSystemAccountId) || undefined,
    createdAt: Date.now()
  }, sessionTtlMs)

  return {
    authUrl: buildGeminiAuthorizeUrl({
      state,
      codeChallenge,
      scope: oauthClient.scope,
      redirectUri: oauthClient.redirectUri,
      clientId: oauthClient.clientId,
      projectId
    }),
    sessionId,
    state
  }
}

export function buildGeminiAuthorizeUrl(input: {
  state: string
  codeChallenge: string
  scope: string
  redirectUri: string
  clientId: string
  projectId?: string
}): string {
  const params = new URLSearchParams({
    response_type: 'code',
    client_id: input.clientId,
    redirect_uri: input.redirectUri,
    scope: input.scope,
    state: input.state,
    code_challenge: input.codeChallenge,
    code_challenge_method: 'S256',
    access_type: 'offline',
    prompt: 'consent',
    include_granted_scopes: 'true'
  })
  const projectId = normalizeString(input.projectId)
  if (projectId) params.set('project_id', projectId)
  return `${GEMINI_OAUTH_AUTHORIZE_URL}?${params.toString()}`
}

export async function exchangeGeminiAuthCode(input: {
  sessionId: string
  callbackUrl: string
  oauthType?: GeminiOAuthType
  clientId?: string
  clientSecret?: string
  projectId?: string
  tierId?: string
  quotaProjectId?: string
  baseUrl?: string
  ownerSystemAccountId?: string
  proxyUrl?: string
  signal?: AbortSignal
}): Promise<GeminiOAuthTokenInfo> {
  const { code, state } = extractCodeAndState(input.callbackUrl)
  const sessionInput = {
    sessionId: input.sessionId,
    state,
    ownerSystemAccountId: input.ownerSystemAccountId,
    oauthType: input.oauthType,
    clientId: input.clientId,
    clientSecret: input.clientSecret,
    projectId: input.projectId,
    tierId: input.tierId,
    quotaProjectId: input.quotaProjectId,
    baseUrl: input.baseUrl
  }
  const session = await readGeminiOAuthSession(sessionInput)

  const tokenInfo = await requestGeminiToken({
    grant_type: 'authorization_code',
    client_id: session.clientId,
    client_secret: session.clientSecret,
    code,
    code_verifier: session.codeVerifier,
    redirect_uri: session.redirectUri
  }, {
    proxyUrl: input.proxyUrl,
    signal: input.signal,
    oauthType: session.oauthType,
    clientId: session.clientId,
    clientSecret: session.clientSecret,
    projectId: session.projectId,
    tierId: session.tierId,
    quotaProjectId: session.quotaProjectId,
    baseUrl: session.baseUrl,
    scope: session.scope
  })
  const enriched = await enrichGeminiTokenInfo(tokenInfo, input.proxyUrl, input.signal)
  if (!await geminiOAuthSessionStore().compareDeleteJson(input.sessionId, session)) {
    throw new Error('Gemini OAuth 会话已消费，请重新发起授权')
  }
  return enriched
}

export async function refreshGeminiAuthToken(input: GeminiOAuthClientCredentials & {
  refreshToken: string
  oauthType?: GeminiOAuthType
  projectId?: string
  tierId?: string
  quotaProjectId?: string
  baseUrl?: string
  scope?: string
  proxyUrl?: string
  signal?: AbortSignal
}): Promise<GeminiOAuthTokenInfo> {
  const refreshToken = normalizeString(input.refreshToken)
  if (!refreshToken) throw new Error('Gemini Refresh Token 不能为空')
  const oauthType = normalizeOAuthType(input.oauthType)
  const oauthClient = resolveGeminiOAuthClient({ ...input, oauthType })
  const form = {
    grant_type: 'refresh_token',
    refresh_token: refreshToken,
    client_id: oauthClient.clientId,
    client_secret: oauthClient.clientSecret
  }
  const options = {
    proxyUrl: input.proxyUrl,
    signal: input.signal,
    oauthType,
    clientId: oauthClient.clientId,
    clientSecret: oauthClient.clientSecret,
    projectId: normalizeString(input.projectId) || undefined,
    tierId: canonicalGeminiTierId(oauthType, input.tierId) || undefined,
    quotaProjectId: normalizeString(input.quotaProjectId) || undefined,
    baseUrl: normalizeString(input.baseUrl) || defaultBaseUrl(oauthType),
    scope: normalizeString(input.scope) || undefined
  }
  let tokenInfo: GeminiOAuthTokenInfo
  try {
    tokenInfo = await requestGeminiTokenWithRetry(form, options)
  } catch (error) {
    const fallbackClient = legacyGeminiOAuthRefreshClient(input, oauthType, oauthClient)
    if (!fallbackClient || !isUnauthorizedGeminiOAuthClientError(error)) throw error
    tokenInfo = await requestGeminiTokenWithRetry({
      ...form,
      client_id: fallbackClient.clientId,
      client_secret: fallbackClient.clientSecret
    }, {
      ...options,
      clientId: fallbackClient.clientId,
      clientSecret: fallbackClient.clientSecret
    })
  }
  return await enrichGeminiTokenInfo(tokenInfo, input.proxyUrl, input.signal)
}

export function buildGeminiOAuthCredentials(
  tokenInfo: GeminiOAuthTokenInfo,
  fallback?: {
    refreshToken?: string
    oauthType?: GeminiOAuthType
    projectId?: string
    tierId?: string
    quotaProjectId?: string
    baseUrl?: string
    scope?: string
  }
): Record<string, unknown> {
  const oauthType = normalizeOAuthType(tokenInfo.oauthType || fallback?.oauthType)
  const credentials: Record<string, unknown> = {
    access_token: tokenInfo.accessToken,
    client_id: tokenInfo.clientId,
    client_secret: tokenInfo.clientSecret,
    oauth_type: oauthType,
    base_url: normalizeString(tokenInfo.baseUrl) || normalizeString(fallback?.baseUrl) || defaultBaseUrl(oauthType)
  }
  const refreshToken = normalizeString(tokenInfo.refreshToken) || normalizeString(fallback?.refreshToken)
  const projectId = normalizeString(tokenInfo.projectId) || normalizeString(fallback?.projectId)
  const tierId = canonicalGeminiTierId(oauthType, tokenInfo.tierId || fallback?.tierId)
  const quotaProjectId = normalizeString(tokenInfo.quotaProjectId) || normalizeString(fallback?.quotaProjectId)
  const scope = normalizeString(tokenInfo.scope) || normalizeString(fallback?.scope)
  if (refreshToken) credentials.refresh_token = refreshToken
  if (tokenInfo.expiresAt) credentials.expires_at = tokenInfo.expiresAt
  if (tokenInfo.tokenType) credentials.token_type = tokenInfo.tokenType
  if (scope) credentials.scope = scope
  if (projectId) credentials.project_id = projectId
  if (tierId) credentials.tier_id = tierId
  if (quotaProjectId) credentials.quota_project_id = quotaProjectId
  if (oauthType !== 'ai_studio') credentials.supported_endpoint_modes = [...geminiCliEndpointModes]
  if (tokenInfo.driveStorageLimit !== undefined) credentials.drive_storage_limit = tokenInfo.driveStorageLimit
  if (tokenInfo.driveStorageUsage !== undefined) credentials.drive_storage_usage = tokenInfo.driveStorageUsage
  if (tokenInfo.driveTierUpdatedAt) credentials.drive_tier_updated_at = tokenInfo.driveTierUpdatedAt
  return credentials
}

export function inferGeminiGoogleOneTier(storageBytes: number): string {
  if (!Number.isFinite(storageBytes) || storageBytes <= 0) return 'google_one_unknown'
  if (storageBytes > 100 * tebibyte) return 'google_ai_ultra'
  if (storageBytes >= 2 * tebibyte) return 'google_ai_pro'
  if (storageBytes >= 15 * gibibyte) return 'google_one_free'
  return 'google_one_unknown'
}

export function sanitizeGeminiOAuthErrorMessage(message: string): string {
  return message
}

function capability(
  oauthType: GeminiOAuthType,
  label: string,
  usesBuiltInClient: boolean,
  supportsProjectId: boolean,
  supportsTierId: boolean
): GeminiOAuthCapabilities['oauthTypes'][number] {
  const config = oauthConfigForType(oauthType)
  return {
    oauthType,
    label,
    usesBuiltInClient,
    requiresClientCredentials: !usesBuiltInClient,
    redirectUri: config.redirectUri,
    scope: config.scope,
    supportsProjectId,
    supportsTierId,
    supportedEndpointModes: oauthType === 'ai_studio' ? [] : [...geminiCliEndpointModes]
  }
}

function resolveGeminiOAuthClient(input: GeminiOAuthClientCredentials & { oauthType: GeminiOAuthType }): {
  clientId: string
  clientSecret: string
  redirectUri: string
  scope: string
} {
  const config = oauthConfigForType(input.oauthType)
  if (config.usesBuiltInClient) {
    return {
      clientId: GEMINI_CLI_OAUTH_CLIENT_ID,
      clientSecret: normalizeString(process.env.GEMINI_CLI_OAUTH_CLIENT_SECRET) || GEMINI_CLI_OAUTH_CLIENT_SECRET,
      redirectUri: config.redirectUri,
      scope: config.scope
    }
  }
  const clientId = normalizeString(input.clientId) || normalizeString(process.env.GEMINI_OAUTH_CLIENT_ID)
  const clientSecret = normalizeString(input.clientSecret) || normalizeString(process.env.GEMINI_OAUTH_CLIENT_SECRET)
  if (!clientId || !clientSecret) {
    throw new Error('Gemini AI Studio OAuth 需要同时配置 Client ID 和 Client Secret')
  }
  return { clientId, clientSecret, redirectUri: config.redirectUri, scope: config.scope }
}

function legacyGeminiOAuthRefreshClient(
  input: GeminiOAuthClientCredentials,
  oauthType: GeminiOAuthType,
  primary: { clientId: string; clientSecret: string }
): { clientId: string; clientSecret: string } | undefined {
  if (!oauthConfigForType(oauthType).usesBuiltInClient) return undefined
  const clientId = normalizeString(input.clientId)
  const clientSecret = normalizeString(input.clientSecret)
  if (!clientId || !clientSecret) return undefined
  if (clientId === primary.clientId && clientSecret === primary.clientSecret) return undefined
  return { clientId, clientSecret }
}

function oauthConfigForType(oauthType: GeminiOAuthType): {
  usesBuiltInClient: boolean
  redirectUri: string
  scope: string
} {
  if (oauthType === 'ai_studio') {
    return { usesBuiltInClient: false, redirectUri: GEMINI_OAUTH_REDIRECT_URI, scope: GEMINI_OAUTH_SCOPE }
  }
  return {
    usesBuiltInClient: true,
    redirectUri: GEMINI_CLI_OAUTH_REDIRECT_URI,
    scope: oauthType === 'google_one' ? GEMINI_GOOGLE_ONE_OAUTH_SCOPE : GEMINI_CODE_ASSIST_OAUTH_SCOPE
  }
}

async function readGeminiOAuthSession(input: {
  sessionId: string
  state: string
  ownerSystemAccountId?: string
  oauthType?: GeminiOAuthType
  clientId?: string
  clientSecret?: string
  projectId?: string
  tierId?: string
  quotaProjectId?: string
  baseUrl?: string
}): Promise<GeminiOAuthSession> {
  const sessionStore = geminiOAuthSessionStore()
  const session = await sessionStore.getJson<GeminiOAuthSession>(input.sessionId)
  if (!session) throw new Error('Gemini OAuth 会话不存在或已过期')
  if (!input.state || input.state !== session.state) throw new Error('Gemini OAuth state 无效')
  const expectedOwner = normalizeString(session.ownerSystemAccountId)
  const actualOwner = normalizeString(input.ownerSystemAccountId)
  if (expectedOwner && actualOwner !== expectedOwner) throw new Error('Gemini OAuth session owner 归属无效')
  if (input.oauthType && input.oauthType !== session.oauthType) throw new Error('Gemini OAuth 类型与授权会话不一致')
  assertSessionFieldMatches('Client ID', input.clientId, session.clientId)
  assertSessionFieldMatches('Client Secret', input.clientSecret, session.clientSecret)
  assertSessionFieldMatches('Project ID', input.projectId, session.projectId)
  assertSessionFieldMatches('Tier ID', canonicalGeminiTierId(session.oauthType, input.tierId), session.tierId)
  assertSessionFieldMatches('Quota Project ID', input.quotaProjectId, session.quotaProjectId)
  assertSessionFieldMatches('Base URL', input.baseUrl, session.baseUrl)
  return session
}

async function requestGeminiToken(
  form: Record<string, string>,
  options: {
    proxyUrl?: string
    signal?: AbortSignal
    oauthType: GeminiOAuthType
    clientId: string
    clientSecret: string
    projectId?: string
    tierId?: string
    quotaProjectId?: string
    baseUrl: string
    scope?: string
  }
): Promise<GeminiOAuthTokenInfo> {
  if (options.signal?.aborted) throw new Error('请求已取消')
  const response = await requestProviderOAuthToken({
    url: GEMINI_OAUTH_TOKEN_URL,
    headers: new Headers({ accept: 'application/json', 'content-type': 'application/x-www-form-urlencoded' }),
    body: new URLSearchParams(form).toString(),
    proxyUrl: normalizeString(options.proxyUrl) || runtimeConfig.oauthProxyUrl,
    signal: options.signal,
    timeoutMs: geminiOAuthRequestTimeoutMs,
    maxResponseBytes: geminiOAuthResponseMaxBytes
  })
  if (options.signal?.aborted) throw new Error('请求已取消')
  if (response.truncated) throw new Error('Gemini OAuth 令牌响应体过大')
  const payload = parseJsonRecord(response.bodyText)
  if (response.statusCode < 200 || response.statusCode >= 300) {
    const errorCode = normalizeString(payload.error)
    const errorDescription = normalizeString(payload.error_description)
    const detail = sanitizeGeminiOAuthErrorMessage(
      errorCode
        ? `${errorCode}${errorDescription && errorDescription !== errorCode ? `: ${errorDescription}` : ''}`
        : errorDescription || response.bodyText
    )
    throw new OAuthUpstreamResponseError(`Gemini OAuth 令牌请求失败：HTTP ${response.statusCode}${detail ? `，${detail}` : ''}`, response.statusCode)
  }
  const accessToken = normalizeString(payload.access_token)
  if (!accessToken) throw new Error('Gemini OAuth 令牌响应缺少 access_token')
  const expiresIn = finitePositiveInteger(payload.expires_in)
  const safeExpiresIn = expiresIn ? Math.max(30, expiresIn - 300) : undefined
  return {
    accessToken,
    refreshToken: normalizeString(payload.refresh_token) || undefined,
    expiresIn,
    expiresAt: safeExpiresIn ? new Date(Date.now() + safeExpiresIn * 1000).toISOString() : undefined,
    scope: normalizeString(payload.scope) || options.scope,
    tokenType: normalizeString(payload.token_type) || undefined,
    clientId: options.clientId,
    clientSecret: options.clientSecret,
    oauthType: options.oauthType,
    projectId: options.projectId,
    tierId: options.tierId,
    quotaProjectId: options.quotaProjectId,
    baseUrl: options.baseUrl
  }
}

async function requestGeminiTokenWithRetry(
  form: Record<string, string>,
  options: Parameters<typeof requestGeminiToken>[1]
): Promise<GeminiOAuthTokenInfo> {
  let lastError: unknown
  for (let attempt = 0; attempt < 4; attempt += 1) {
    if (attempt > 0) await waitForGeminiOAuthRetry(2 ** (attempt - 1) * 1000, options.signal)
    try {
      return await requestGeminiToken(form, options)
    } catch (error) {
      if (isNonRetryableGeminiOAuthError(error)) throw error
      lastError = error
    }
  }
  throw lastError
}

function isNonRetryableGeminiOAuthError(error: unknown): boolean {
  const message = error instanceof Error ? error.message.toLowerCase() : String(error).toLowerCase()
  return ['invalid_grant', 'invalid_client', 'unauthorized_client', 'access_denied']
    .some((code) => message.includes(code))
}

function isUnauthorizedGeminiOAuthClientError(error: unknown): boolean {
  const message = error instanceof Error ? error.message.toLowerCase() : String(error).toLowerCase()
  return message.includes('unauthorized_client')
}

function waitForGeminiOAuthRetry(delayMs: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new Error('请求已取消'))
      return
    }
    const cleanup = () => signal?.removeEventListener('abort', abort)
    const timer = setTimeout(() => {
      cleanup()
      resolve()
    }, delayMs)
    const abort = () => {
      clearTimeout(timer)
      cleanup()
      reject(new Error('请求已取消'))
    }
    signal?.addEventListener('abort', abort, { once: true })
    timer.unref()
  })
}

async function enrichGeminiTokenInfo(
  tokenInfo: GeminiOAuthTokenInfo,
  proxyUrl?: string,
  signal?: AbortSignal
): Promise<GeminiOAuthTokenInfo> {
  if (tokenInfo.oauthType === 'ai_studio') {
    return { ...tokenInfo, tierId: canonicalGeminiTierId('ai_studio', tokenInfo.tierId) || 'aistudio_free' }
  }

  let projectId = tokenInfo.projectId
  let tierId = tokenInfo.tierId
  if (!projectId || (tokenInfo.oauthType === 'code_assist' && !tierId)) {
    try {
      const detected = await detectCodeAssistProjectAndTier(tokenInfo.accessToken, proxyUrl, signal, tierId)
      projectId = projectId || detected.projectId
      if (tokenInfo.oauthType === 'code_assist') tierId = detected.tierId || tierId
    } catch (error) {
      if (!projectId && tokenInfo.oauthType === 'code_assist') throw error
    }
  }

  if (tokenInfo.oauthType === 'code_assist') {
    if (!projectId) throw new Error('Gemini Code Assist 未探测到 project_id，请在授权时提供 GCP Project ID')
    return {
      ...tokenInfo,
      projectId,
      tierId: canonicalGeminiTierId('code_assist', tierId) || 'gcp_standard'
    }
  }

  if (!projectId) {
    throw new Error('Gemini Google One 未探测到 project_id，请在授权时提供 GCP Project ID')
  }

  let driveStorageLimit: number | undefined
  let driveStorageUsage: number | undefined
  let driveTierUpdatedAt: string | undefined
  if (hasGoogleDriveMetadataScope(tokenInfo.scope)) {
    try {
      const storage = await fetchGoogleDriveStorageQuota(tokenInfo.accessToken, proxyUrl, signal)
      driveStorageLimit = storage.limit
      driveStorageUsage = storage.usage
      driveTierUpdatedAt = new Date().toISOString()
      const detectedTier = inferGeminiGoogleOneTier(storage.limit)
      if (detectedTier !== 'google_one_unknown') tierId = detectedTier
    } catch {
      // Legacy grants may include Drive but still reject quota reads; keep the selected tier.
    }
  }
  return {
    ...tokenInfo,
    projectId,
    tierId: canonicalGeminiTierId('google_one', tierId) || 'google_one_free',
    driveStorageLimit,
    driveStorageUsage,
    driveTierUpdatedAt
  }
}

function hasGoogleDriveMetadataScope(scope: string | undefined): boolean {
  return normalizeString(scope)
    .split(/\s+/u)
    .includes('https://www.googleapis.com/auth/drive.metadata.readonly')
}

async function detectCodeAssistProjectAndTier(
  accessToken: string,
  proxyUrl?: string,
  signal?: AbortSignal,
  preferredTierId?: string
): Promise<{ projectId?: string; tierId?: string }> {
  let load: Record<string, unknown> = {}
  let loadError: unknown
  try {
    load = await requestGeminiJson({
      url: `${GEMINI_CLI_DEFAULT_BASE_URL}/v1internal:loadCodeAssist`,
      method: 'POST',
      accessToken,
      body: { metadata: codeAssistMetadata() },
      proxyUrl,
      signal
    })
  } catch (error) {
    loadError = error
  }

  const projectId = codeAssistProjectId(load)
  const tierId = codeAssistTierId(load) || canonicalGeminiTierId('code_assist', preferredTierId) || 'gcp_standard'
  if (projectId) return { projectId, tierId }

  const registered = Boolean(codeAssistTierId(load))
  if (registered) {
    const fallbackProject = await fetchResourceManagerProject(accessToken, proxyUrl, signal)
    if (fallbackProject) return { projectId: fallbackProject, tierId }
    throw new Error(`Gemini Code Assist 已注册档位 ${tierId}，但未找到 project_id，请在授权表单中提供 GCP Project ID`)
  }

  try {
    for (let attempt = 0; attempt < 5; attempt += 1) {
      const onboard = await requestGeminiJson({
        url: `${GEMINI_CLI_DEFAULT_BASE_URL}/v1internal:onboardUser`,
        method: 'POST',
        accessToken,
        body: { tierId: upstreamCodeAssistTier(tierId), metadata: codeAssistMetadata() },
        proxyUrl,
        signal
      })
      if (onboard.done === true) {
        const result = recordValue(onboard.response)
        const companion = result?.cloudaicompanionProject
        const onboardProject = normalizeString(companion) || normalizeString(recordValue(companion)?.id)
        if (onboardProject) return { projectId: onboardProject, tierId }
        break
      }
      await abortableDelay(2_000, signal)
    }
  } catch (onboardError) {
    const fallbackProject = await fetchResourceManagerProject(accessToken, proxyUrl, signal)
    if (fallbackProject) return { projectId: fallbackProject, tierId }
    throw onboardError
  }
  const fallbackProject = await fetchResourceManagerProject(accessToken, proxyUrl, signal)
  if (fallbackProject) return { projectId: fallbackProject, tierId }
  if (loadError instanceof Error) {
    throw new Error(`Gemini loadCodeAssist 失败且 onboardUser 未返回 project_id：${loadError.message}`, { cause: loadError })
  }
  throw new Error('Gemini onboardUser 未返回 project_id')
}

async function fetchResourceManagerProject(
  accessToken: string,
  proxyUrl?: string,
  signal?: AbortSignal
): Promise<string | undefined> {
  try {
    const payload = await requestGeminiJson({
      url: 'https://cloudresourcemanager.googleapis.com/v1/projects',
      method: 'GET',
      accessToken,
      proxyUrl,
      signal
    })
    const projects = Array.isArray(payload.projects) ? payload.projects.map(recordValue).filter(Boolean) as Record<string, unknown>[] : []
    const active = projects.filter((project) => project.lifecycleState === 'ACTIVE' && normalizeString(project.projectId))
    const preferred = active.find((project) => {
      const value = `${normalizeString(project.projectId)} ${normalizeString(project.name)}`.toLowerCase()
      return value.includes('cloud-ai-companion') || value.includes('cloud ai companion') || value.includes('code assist')
    }) ?? active.find((project) => `${normalizeString(project.projectId)} ${normalizeString(project.name)}`.toLowerCase().includes('default'))
      ?? active[0]
    return preferred ? normalizeString(preferred.projectId) || undefined : undefined
  } catch {
    return undefined
  }
}

async function fetchGoogleDriveStorageQuota(
  accessToken: string,
  proxyUrl?: string,
  signal?: AbortSignal
): Promise<{ limit: number; usage: number }> {
  const payload = await requestGeminiJson({
    url: 'https://www.googleapis.com/drive/v3/about?fields=storageQuota',
    method: 'GET',
    accessToken,
    proxyUrl,
    signal
  })
  const quota = recordValue(payload.storageQuota)
  return {
    limit: finiteNonNegativeNumber(quota?.limit) ?? 0,
    usage: finiteNonNegativeNumber(quota?.usage) ?? 0
  }
}

async function requestGeminiJson(input: {
  url: string
  method: 'GET' | 'POST'
  accessToken: string
  body?: Record<string, unknown>
  proxyUrl?: string
  signal?: AbortSignal
}): Promise<Record<string, unknown>> {
  if (input.signal?.aborted) throw new Error('请求已取消')
  const response = await requestUpstream(geminiOAuthProbeUrl(input.url), {
    method: input.method,
    headers: new Headers({
      accept: 'application/json',
      authorization: `Bearer ${input.accessToken}`,
      'content-type': 'application/json',
      'user-agent': GEMINI_CLI_USER_AGENT
    }),
    body: input.body ? JSON.stringify(input.body) : undefined,
    proxyUrl: normalizeString(input.proxyUrl) || runtimeConfig.oauthProxyUrl,
    timeoutMs: geminiOAuthRequestTimeoutMs,
    requestTimeoutMs: geminiOAuthRequestTimeoutMs,
    signal: input.signal
  })
  const body = await readUpstreamBodyLimited(response.body, { maxBytes: geminiOAuthResponseMaxBytes })
  if (body.truncated) throw new Error('Gemini OAuth 探测响应体过大')
  if (!response.ok) {
    throw new OAuthUpstreamResponseError(`Gemini OAuth 上游探测失败：HTTP ${response.status}${body.bodyText ? `，${sanitizeGeminiOAuthErrorMessage(body.bodyText)}` : ''}`, response.status)
  }
  return parseJsonRecord(body.bodyText)
}

function geminiOAuthProbeUrl(url: string): string {
  const overrides = geminiOAuthProbeBaseUrlsForTest.getStore()
  if (!overrides) return url
  const parsed = new URL(url)
  const overrideBaseUrl = parsed.origin === GEMINI_CLI_DEFAULT_BASE_URL
    ? overrides.cloudCodeBaseUrl
    : parsed.origin === 'https://cloudresourcemanager.googleapis.com'
      ? overrides.cloudResourceManagerBaseUrl
      : parsed.origin === 'https://www.googleapis.com'
        ? overrides.googleApisBaseUrl
        : undefined
  if (!overrideBaseUrl) return url
  const target = new URL(overrideBaseUrl)
  target.pathname = `${target.pathname.replace(/\/$/u, '')}${parsed.pathname}`
  target.search = parsed.search
  return target.toString()
}

function extractCodeAndState(callbackUrl: string): { code: string; state: string } {
  const value = normalizeString(callbackUrl)
  if (!value) throw new Error('Gemini 授权结果不能为空')
  let code = ''
  let state = ''
  try {
    const url = new URL(value)
    const error = normalizeString(url.searchParams.get('error'))
    if (error) throw new OAuthUpstreamResponseError(normalizeString(url.searchParams.get('error_description')) || error)
    code = normalizeString(url.searchParams.get('code'))
    state = normalizeString(url.searchParams.get('state'))
  } catch (error) {
    if (error instanceof Error && !error.message.includes('Invalid URL')) throw error
  }
  if (!code || !state) {
    const separator = value.lastIndexOf('#')
    if (separator > 0) {
      code = normalizeString(value.slice(0, separator))
      state = normalizeString(value.slice(separator + 1))
    } else {
      const query = new URLSearchParams(value.replace(/^\?/u, ''))
      code = normalizeString(query.get('code'))
      state = normalizeString(query.get('state'))
    }
  }
  if (!code || !state) throw new Error('Gemini 授权结果必须包含 code 和 state')
  return { code, state }
}

function codeAssistMetadata(): Record<string, string> {
  return { ideType: 'ANTIGRAVITY', platform: 'PLATFORM_UNSPECIFIED', pluginType: 'GEMINI' }
}

function codeAssistProjectId(payload: Record<string, unknown>): string | undefined {
  return normalizeString(payload.cloudaicompanionProject) || normalizeString(recordValue(payload.cloudaicompanionProject)?.id) || undefined
}

function codeAssistTierId(payload: Record<string, unknown>): string | undefined {
  const paid = normalizeString(recordValue(payload.paidTier)?.id) || normalizeString(payload.paidTier)
  const current = normalizeString(recordValue(payload.currentTier)?.id) || normalizeString(payload.currentTier)
  if (paid || current) return canonicalGeminiTierId('code_assist', paid || current) || undefined
  const allowed = Array.isArray(payload.allowedTiers) ? payload.allowedTiers.map(recordValue).filter(Boolean) as Record<string, unknown>[] : []
  const selected = allowed.find((tier) => tier.isDefault === true) ?? allowed[0]
  return canonicalGeminiTierId('code_assist', selected?.id) || undefined
}

function canonicalGeminiTierId(oauthType: GeminiOAuthType, raw: unknown): string {
  const value = normalizeString(raw).toLowerCase().replaceAll('-', '_')
  if (!value) return ''
  if (oauthType === 'google_one') {
    if (['ai_premium', 'google_ai_pro'].includes(value)) return 'google_ai_pro'
    if (['google_one_unlimited', 'google_ai_ultra'].includes(value)) return 'google_ai_ultra'
    if (value === 'google_one_unknown') return value
    return ['free', 'google_one_basic', 'google_one_standard', 'google_one_free'].includes(value) ? 'google_one_free' : ''
  }
  if (oauthType === 'ai_studio') {
    if (['aistudio_paid', 'paid'].includes(value)) return 'aistudio_paid'
    return ['aistudio_free', 'free'].includes(value) ? 'aistudio_free' : ''
  }
  if (['enterprise', 'ultra', 'gcp_enterprise', 'ultra_tier'].includes(value)) return 'gcp_enterprise'
  if (['legacy', 'standard', 'pro', 'gcp_standard', 'standard_tier', 'pro_tier'].includes(value)) return 'gcp_standard'
  return ''
}

function upstreamCodeAssistTier(tierId: string): string {
  return tierId === 'gcp_enterprise' ? 'ENTERPRISE' : 'LEGACY'
}

function defaultBaseUrl(oauthType: GeminiOAuthType): string {
  return oauthType === 'ai_studio' ? GEMINI_OAUTH_DEFAULT_BASE_URL : GEMINI_CLI_DEFAULT_BASE_URL
}

function normalizeOAuthType(value: unknown): GeminiOAuthType {
  return GEMINI_OAUTH_TYPES.includes(value as GeminiOAuthType) ? value as GeminiOAuthType : 'code_assist'
}

function parseJsonRecord(value: string): Record<string, unknown> {
  if (!value) return {}
  try {
    return recordValue(JSON.parse(value)) ?? {}
  } catch {
    return { raw: value }
  }
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined
}

function finitePositiveInteger(value: unknown): number | undefined {
  const parsed = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(parsed) || parsed <= 0) return undefined
  return Math.trunc(parsed)
}

function finiteNonNegativeNumber(value: unknown): number | undefined {
  const parsed = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : undefined
}

function assertSessionFieldMatches(label: string, provided: string | undefined, sessionValue: string | undefined): void {
  const normalizedProvided = normalizeString(provided)
  if (normalizedProvided && normalizedProvided !== normalizeString(sessionValue)) {
    throw new Error(`Gemini OAuth ${label} 与授权会话不一致`)
  }
}

function normalizeString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

async function abortableDelay(ms: number, signal?: AbortSignal): Promise<void> {
  if (signal?.aborted) throw new Error('请求已取消')
  await new Promise<void>((resolve, reject) => {
    const onAbort = () => {
      clearTimeout(timer)
      reject(new Error('请求已取消'))
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    timer.unref()
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

let memoryGeminiOAuthSessionStore: GeminiOAuthMemorySessionStore | undefined

function geminiOAuthSessionStore(): GeminiOAuthSessionStore {
  if (runtimeConfig.runtimeStateDriver === 'redis') return createRuntimeStateStore('gemini-oauth:sessions')
  memoryGeminiOAuthSessionStore = memoryGeminiOAuthSessionStore ?? new GeminiOAuthMemorySessionStore()
  return memoryGeminiOAuthSessionStore
}

class GeminiOAuthMemorySessionStore implements GeminiOAuthSessionStore {
  private readonly entries = new Map<string, { value: GeminiOAuthSession; expiresAt: number }>()
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
    this.entries.set(key, { value: value as GeminiOAuthSession, expiresAt: Date.now() + Math.max(1, Math.trunc(ttlMs)) })
  }

  async compareDeleteJson<T>(key: string, expectedValue: T): Promise<boolean> {
    const entry = this.entries.get(key)
    if (!entry) return false
    if (entry.expiresAt <= Date.now()) {
      this.entries.delete(key)
      return false
    }
    if (JSON.stringify(entry.value) !== JSON.stringify(expectedValue)) return false
    this.entries.delete(key)
    return true
  }

  private cleanup(): void {
    const now = Date.now()
    for (const [key, entry] of this.entries) if (entry.expiresAt <= now) this.entries.delete(key)
  }
}
