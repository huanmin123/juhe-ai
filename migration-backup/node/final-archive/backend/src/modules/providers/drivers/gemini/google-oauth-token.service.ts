/** Google OAuth 2.0 token endpoint used by Gemini user-authorized accounts. */
export const GOOGLE_OAUTH_TOKEN_ENDPOINT = 'https://oauth2.googleapis.com/token'
export const geminiGoogleOAuthTokenResponseMaxBytes = 256 * 1024
export const geminiGoogleOAuthTokenRequestTimeoutMs = 20_000

import {
  requestTokenExchange,
  type TokenExchangeTransport
} from '../_shared/token-exchange-transport.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../../../../shared/rfc3339.js'

export interface GeminiGoogleOAuthCredentials {
  access_token?: string
  refresh_token?: string
  client_id?: string
  client_secret?: string
  expires_at?: string
}

export interface GeminiGoogleOAuthTokenSnapshot {
  access_token: string
  expires_at?: string
}

export type GeminiGoogleOAuthTokenTransport = TokenExchangeTransport

export interface GeminiGoogleOAuthTokenProviderOptions {
  transport?: GeminiGoogleOAuthTokenTransport
  now?: () => number
  cacheLeadSeconds?: number
  proxyUrl?: string
  requestTimeoutMs?: number
}

export interface GeminiGoogleOAuthAccessTokenOptions {
  /** Used only to stop waiting for an exchange already in progress. */
  signal?: AbortSignal
}

interface GoogleOAuthTokenResponse {
  access_token?: unknown
  expires_in?: unknown
}

interface CachedToken extends GeminiGoogleOAuthTokenSnapshot {
  expiresAtMs: number
}

const defaultCacheLeadSeconds = 60

export class GeminiGoogleOAuthTokenProvider {
  private readonly credentials: GeminiGoogleOAuthCredentials
  private readonly transport: GeminiGoogleOAuthTokenTransport
  private readonly now: () => number
  private readonly cacheLeadMs: number
  private readonly proxyUrl?: string
  private readonly requestTimeoutMs: number
  private cachedToken: CachedToken | undefined
  private inFlight: Promise<CachedToken> | undefined

  constructor(
    credentials: GeminiGoogleOAuthCredentials,
    options: GeminiGoogleOAuthTokenProviderOptions = {}
  ) {
    this.credentials = validateCredentials(credentials)
    this.transport = options.transport ?? requestTokenExchange
    this.now = options.now ?? Date.now
    this.cacheLeadMs = normalizeCacheLeadMs(options.cacheLeadSeconds)
    this.proxyUrl = nonEmptyText(options.proxyUrl) || undefined
    this.requestTimeoutMs = normalizeRequestTimeoutMs(options.requestTimeoutMs)
    this.cachedToken = initialCachedToken(this.credentials)
  }

  async getAccessToken(options: GeminiGoogleOAuthAccessTokenOptions = {}): Promise<string> {
    const cached = this.cachedToken
    if (cached && cached.expiresAtMs - this.now() > this.cacheLeadMs) {
      return cached.access_token
    }
    const exchange = this.inFlight ?? this.startExchange()
    if (!options.signal) return (await exchange).access_token
    return await waitForAbortableToken(exchange, options.signal)
  }

  getTokenSnapshot(): GeminiGoogleOAuthTokenSnapshot | undefined {
    const cached = this.cachedToken
    if (!cached) return undefined
    return {
      access_token: cached.access_token,
      expires_at: cached.expires_at
    }
  }

  clearCache(): void {
    this.cachedToken = undefined
  }

  private startExchange(): Promise<CachedToken> {
    const exchange = this.exchangeToken()
    this.inFlight = exchange
    void exchange.then(
      (token) => {
        if (this.inFlight === exchange) {
          this.cachedToken = token
          this.inFlight = undefined
        }
      },
      () => {
        if (this.inFlight === exchange) this.inFlight = undefined
      }
    )
    return exchange
  }

  private async exchangeToken(): Promise<CachedToken> {
    if (!this.credentials.refresh_token) {
      throw new Error('Gemini Google OAuth refresh_token is required')
    }
    if (!this.credentials.client_id) {
      throw new Error('Gemini Google OAuth client_id is required')
    }
    if (!this.credentials.client_secret) {
      throw new Error('Gemini Google OAuth client_secret is required')
    }
    const form = new URLSearchParams({
      client_id: this.credentials.client_id,
      client_secret: this.credentials.client_secret,
      refresh_token: this.credentials.refresh_token,
      grant_type: 'refresh_token'
    })

    let response: Awaited<ReturnType<GeminiGoogleOAuthTokenTransport>>
    try {
      response = await this.transport({
        url: GOOGLE_OAUTH_TOKEN_ENDPOINT,
        headers: new Headers({
          accept: 'application/json',
          'content-type': 'application/x-www-form-urlencoded'
        }),
        body: form.toString(),
        proxyUrl: this.proxyUrl,
        timeoutMs: this.requestTimeoutMs,
        maxResponseBytes: geminiGoogleOAuthTokenResponseMaxBytes
      })
    } catch {
      throw new Error('Gemini Google OAuth token refresh request failed')
    }
    if (response.truncated) {
      throw new Error('Gemini Google OAuth token response is too large')
    }
    if (response.statusCode < 200 || response.statusCode >= 300) {
      throw new Error(`Gemini Google OAuth token refresh failed (HTTP ${response.statusCode})`)
    }

    let payload: GoogleOAuthTokenResponse
    try {
      payload = JSON.parse(response.bodyText) as GoogleOAuthTokenResponse
    } catch {
      throw new Error('Gemini Google OAuth token response returned invalid JSON')
    }
    const accessToken = nonEmptyText(payload.access_token)
    const expiresInSeconds = positiveFiniteSeconds(payload.expires_in)
    if (!accessToken) throw new Error('Gemini Google OAuth token response is missing access_token')
    if (expiresInSeconds === undefined) throw new Error('Gemini Google OAuth token response has invalid expires_in')
    const expiresAtMs = this.now() + expiresInSeconds * 1000
    return {
      access_token: accessToken,
      expires_at: new Date(expiresAtMs).toISOString(),
      expiresAtMs
    }
  }
}

export function createGeminiGoogleOAuthTokenProvider(
  credentials: GeminiGoogleOAuthCredentials,
  options?: GeminiGoogleOAuthTokenProviderOptions
): GeminiGoogleOAuthTokenProvider {
  return new GeminiGoogleOAuthTokenProvider(credentials, options)
}

function validateCredentials(credentials: GeminiGoogleOAuthCredentials): GeminiGoogleOAuthCredentials {
  if (!credentials || typeof credentials !== 'object') {
    throw new Error('Gemini Google OAuth credentials are required')
  }
  const normalized = {
    access_token: nonEmptyText(credentials.access_token) || undefined,
    refresh_token: nonEmptyText(credentials.refresh_token),
    client_id: nonEmptyText(credentials.client_id),
    client_secret: nonEmptyText(credentials.client_secret),
    expires_at: credentials.expires_at === undefined
      ? undefined
      : requiredRfc3339Instant(credentials.expires_at, 'Gemini Google OAuth credentials.expires_at')
  }
  if (!normalized.access_token && !normalized.refresh_token) {
    throw new Error('Gemini Google OAuth access_token or refresh_token is required')
  }
  if (normalized.refresh_token && !normalized.client_id) {
    throw new Error('Gemini Google OAuth client_id is required when refresh_token is configured')
  }
  if (normalized.refresh_token && !normalized.client_secret) {
    throw new Error('Gemini Google OAuth client_secret is required when refresh_token is configured')
  }
  return normalized
}

function initialCachedToken(credentials: GeminiGoogleOAuthCredentials): CachedToken | undefined {
  if (!credentials.access_token) return undefined
  if (!credentials.expires_at) {
    // An access-only credential cannot be refreshed; keep it usable until the upstream rejects it.
    if (credentials.refresh_token) return undefined
    return {
      access_token: credentials.access_token,
      expiresAtMs: Number.POSITIVE_INFINITY
    }
  }
  const expiresAtMs = rfc3339InstantMilliseconds(credentials.expires_at)
  if (expiresAtMs === undefined) {
    throw new Error('Gemini Google OAuth credentials.expires_at必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  return {
    access_token: credentials.access_token,
    expires_at: credentials.expires_at,
    expiresAtMs
  }
}

function normalizeCacheLeadMs(value: number | undefined): number {
  const seconds = value === undefined ? defaultCacheLeadSeconds : value
  if (!Number.isFinite(seconds) || seconds < 0 || seconds >= 86400) {
    throw new Error('Gemini Google OAuth cacheLeadSeconds is invalid')
  }
  return Math.trunc(seconds * 1000)
}

function normalizeRequestTimeoutMs(value: number | undefined): number {
  if (value === undefined) return geminiGoogleOAuthTokenRequestTimeoutMs
  if (!Number.isFinite(value) || value < 1 || value > 120_000) {
    throw new Error('Gemini Google OAuth requestTimeoutMs is invalid')
  }
  return Math.trunc(value)
}

function positiveFiniteSeconds(value: unknown): number | undefined {
  if (typeof value !== 'number' || !Number.isInteger(value) || !Number.isFinite(value) || value <= 0) return undefined
  return value
}

function nonEmptyText(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

async function waitForAbortableToken(
  promise: Promise<CachedToken>,
  signal: AbortSignal
): Promise<string> {
  if (signal.aborted) throw abortError()
  return await new Promise<string>((resolve, reject) => {
    const onAbort = () => {
      cleanup()
      reject(abortError())
    }
    const cleanup = () => signal.removeEventListener('abort', onAbort)
    signal.addEventListener('abort', onAbort, { once: true })
    void promise.then(
      (token) => {
        cleanup()
        resolve(token.access_token)
      },
      (error) => {
        cleanup()
        reject(error)
      }
    )
  })
}

function abortError(): Error {
  const error = new Error('Gemini Google OAuth token wait aborted')
  error.name = 'AbortError'
  return error
}
