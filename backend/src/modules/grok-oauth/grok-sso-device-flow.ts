import { request as httpsRequest } from 'node:https'

import { HttpsProxyAgent } from 'https-proxy-agent'
import { SocksProxyAgent } from 'socks-proxy-agent'

import { runtimeConfig } from '../../config/runtime.js'
import { BoundedBufferCollector } from '../../shared/bounded-buffer.js'
import { createProxyAgent } from '../openai-oauth/openai-oauth.service.js'

export const GROK_SSO_BUILD_SCOPE = 'openid profile email offline_access grok-cli:access api:access conversations:read conversations:write'
export const GROK_SSO_ACCOUNTS_URL = 'https://accounts.x.ai/'
export const GROK_SSO_DEVICE_URL = 'https://auth.x.ai/oauth2/device/code'
export const GROK_SSO_VERIFY_URL = 'https://auth.x.ai/oauth2/device/verify'
export const GROK_SSO_APPROVE_URL = 'https://auth.x.ai/oauth2/device/approve'
export const GROK_SSO_TOKEN_URL = 'https://auth.x.ai/oauth2/token'
export const GROK_SSO_CLIENT_ID = 'b1a00492-073a-47ea-816f-4c329264a828'
export const grokSSOConversionTimeoutMs = 90_000
export const grokSSOMaxAuthBodyBytes = 2 * 1024 * 1024

const grokSSODefaultUserAgent = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36'
const grokSSODefaultTokenTtlSeconds = 6 * 60 * 60
const grokSSODefaultPollIntervalMs = 5_000
const grokSSODefaultDeviceExpiresMs = 30 * 60 * 1000
const grokSSOMaxPollDurationMs = 75_000
const grokSSOMaxRedirects = 8

interface GrokSSOCookie {
  name: string
  value: string
  domain: string
  path: string
  secure: boolean
  hostOnly: boolean
  expiresAt?: number
}

export interface GrokSSORawTokenResponse {
  accessToken: string
  refreshToken?: string
  idToken?: string
  tokenType: string
  expiresIn: number
  scope?: string
}

export interface GrokSSODeviceRequest {
  method: 'GET' | 'POST'
  url: string
  headers: Record<string, string | number>
  body?: string
  proxyUrl?: string
  signal: AbortSignal
}

export interface GrokSSODeviceResponse {
  statusCode: number
  headers: Record<string, string | string[] | undefined>
  body: string
}

export interface GrokSSODeviceDependencies {
  request?: (request: GrokSSODeviceRequest) => Promise<GrokSSODeviceResponse>
  sleep?: (delayMs: number, signal: AbortSignal) => Promise<void>
  now?: () => number
}

export class GrokSSODeviceError extends Error {
  constructor(message: string, readonly statusCode: number = 502) {
    super(message)
    this.name = 'GrokSSODeviceError'
  }
}

export async function convertGrokSSOToOAuth(input: {
  ssoToken: string
  proxyUrl?: string
  signal?: AbortSignal
  dependencies?: GrokSSODeviceDependencies
}): Promise<GrokSSORawTokenResponse> {
  const ssoToken = normalizeGrokSSOToken(input.ssoToken)
  if (!ssoToken) throw new GrokSSODeviceError('xAI SSO 未授权', 400)

  const signal = input.signal ?? new AbortController().signal
  const flow = new GrokSSODeviceFlow({
    proxyUrl: input.proxyUrl,
    signal,
    request: input.dependencies?.request ?? performGrokSSODeviceRequest,
    sleep: input.dependencies?.sleep ?? sleepWithSignal,
    now: input.dependencies?.now ?? Date.now,
    ssoToken
  })
  try {
    return await flow.convert()
  } catch (error) {
    if (input.signal?.aborted && !(error instanceof GrokSSODeviceError)) {
      throw new GrokSSODeviceError('xAI SSO 转换已取消或超时', 502)
    }
    throw error
  }
}

export function normalizeGrokSSOToken(value: string): string {
  let normalized = value.trim()
  if (normalized.toLowerCase().startsWith('cookie:')) normalized = normalized.slice('cookie:'.length).trim()
  for (const part of normalized.split(';')) {
    const separator = part.indexOf('=')
    if (separator < 0) continue
    const name = part.slice(0, separator).trim().toLowerCase()
    if (name === 'sso' || name === 'sso-rw') return sanitizeSSOToken(part.slice(separator + 1))
  }
  const separator = normalized.indexOf(';')
  if (separator >= 0) normalized = normalized.slice(0, separator).trim()
  return sanitizeSSOToken(normalized)
}

export function normalizeGrokSSOImportTokens(tokens: string[], single?: string): string[] {
  const seen = new Set<string>()
  const output: string[] = []
  for (const input of [...(single?.trim() ? [single] : []), ...tokens]) {
    for (const item of input.replace(/[\r,]/gu, '\n').split('\n')) {
      const token = normalizeGrokSSOToken(item)
      if (!token || seen.has(token)) continue
      seen.add(token)
      output.push(token)
    }
  }
  return output
}

class GrokSSODeviceFlow {
  private readonly cookies = new Map<string, GrokSSOCookie>()

  constructor(private readonly options: {
    proxyUrl?: string
    signal: AbortSignal
    request: NonNullable<GrokSSODeviceDependencies['request']>
    sleep: NonNullable<GrokSSODeviceDependencies['sleep']>
    now: NonNullable<GrokSSODeviceDependencies['now']>
    ssoToken: string
  }) {
    this.storeCookie({ name: 'sso', value: options.ssoToken, domain: 'x.ai', path: '/', secure: true, hostOnly: false })
    this.storeCookie({ name: 'sso-rw', value: options.ssoToken, domain: 'x.ai', path: '/', secure: true, hostOnly: false })
  }

  async convert(): Promise<GrokSSORawTokenResponse> {
    let response = await this.request('GET', GROK_SSO_ACCOUNTS_URL)
    if (response.statusCode === 401 || response.finalUrl.includes('sign-in') || response.finalUrl.includes('sign-up')) {
      throw new GrokSSODeviceError('xAI SSO 未授权', 400)
    }
    if (response.statusCode < 200 || response.statusCode >= 400) {
      throw httpError('校验 Grok Web SSO 失败', response.statusCode)
    }

    response = await this.request('POST', GROK_SSO_DEVICE_URL, new URLSearchParams({
      client_id: GROK_SSO_CLIENT_ID,
      scope: GROK_SSO_BUILD_SCOPE
    }))
    if (response.statusCode < 200 || response.statusCode >= 300) {
      throw httpError('启动 xAI device flow 失败', response.statusCode)
    }
    const device = parseJsonObject(response.body, '解析 xAI device flow 响应失败')
    const deviceCode = stringValue(device.device_code)
    const userCode = stringValue(device.user_code)
    const verificationUrl = stringValue(device.verification_uri_complete)
    if (!deviceCode || !userCode || !isTrustedXAIAuthUrl(verificationUrl)) {
      throw new GrokSSODeviceError('xAI device flow 响应不完整')
    }
    const intervalMs = positiveInteger(device.interval) ? positiveInteger(device.interval)! * 1000 : grokSSODefaultPollIntervalMs
    const expiresInMs = positiveInteger(device.expires_in) ? positiveInteger(device.expires_in)! * 1000 : grokSSODefaultDeviceExpiresMs

    response = await this.request('GET', verificationUrl)
    if (response.statusCode < 200 || response.statusCode >= 400) {
      throw httpError('打开 xAI device 验证页失败', response.statusCode)
    }

    response = await this.request('POST', GROK_SSO_VERIFY_URL, new URLSearchParams({ user_code: userCode }))
    if (response.statusCode < 200 || response.statusCode >= 400) {
      throw httpError('校验 xAI device code 失败', response.statusCode)
    }
    if (!response.finalUrl.includes('consent')) {
      throw new GrokSSODeviceError('xAI device 验证未进入 consent 页面')
    }

    response = await this.request('POST', GROK_SSO_APPROVE_URL, new URLSearchParams({
      user_code: userCode,
      action: 'allow',
      principal_type: 'User',
      principal_id: ''
    }))
    if (response.statusCode < 200 || response.statusCode >= 400) {
      throw httpError('批准 xAI device code 失败', response.statusCode)
    }
    if (!response.finalUrl.includes('done')) {
      throw new GrokSSODeviceError('xAI device 授权未进入 done 页面')
    }

    return await this.pollToken(deviceCode, intervalMs, expiresInMs)
  }

  private async pollToken(deviceCode: string, initialIntervalMs: number, expiresInMs: number): Promise<GrokSSORawTokenResponse> {
    let intervalMs = Math.max(1_000, initialIntervalMs)
    const deadline = this.options.now() + Math.min(expiresInMs > 0 ? expiresInMs : grokSSOMaxPollDurationMs, grokSSOMaxPollDurationMs)
    while (this.options.now() < deadline) {
      await this.options.sleep(intervalMs, this.options.signal)
      const response = await this.request('POST', GROK_SSO_TOKEN_URL, new URLSearchParams({
        grant_type: 'urn:ietf:params:oauth:grant-type:device_code',
        client_id: GROK_SSO_CLIENT_ID,
        device_code: deviceCode
      }))
      const payload = parseJsonObject(response.body, '解析 xAI token 响应失败')
      const accessToken = stringValue(payload.access_token)
      if (response.statusCode >= 200 && response.statusCode < 300 && accessToken) {
        return {
          accessToken,
          refreshToken: stringValue(payload.refresh_token) || undefined,
          idToken: stringValue(payload.id_token) || undefined,
          tokenType: stringValue(payload.token_type) || 'Bearer',
          expiresIn: positiveInteger(payload.expires_in) ?? grokSSODefaultTokenTtlSeconds,
          scope: stringValue(payload.scope) || undefined
        }
      }
      const errorCode = stringValue(payload.error)
      if (errorCode === 'authorization_pending') continue
      if (errorCode === 'slow_down') {
        intervalMs += 5_000
        continue
      }
      if (errorCode === 'access_denied' || errorCode === 'expired_token') {
        throw new GrokSSODeviceError('xAI device 授权被拒绝或已过期', 400)
      }
      const detail = stringValue(payload.error_description) || errorCode
      if (response.statusCode >= 400) {
        throw new GrokSSODeviceError(`xAI token 轮询失败${detail ? `：${detail}` : ''}（HTTP ${response.statusCode}）`)
      }
      throw new GrokSSODeviceError(`xAI token 轮询失败${detail ? `：${detail}` : `：HTTP ${response.statusCode}`}`)
    }
    throw new GrokSSODeviceError('xAI device flow token 轮询超时')
  }

  private async request(method: 'GET' | 'POST', initialUrl: string, form?: URLSearchParams): Promise<GrokSSODeviceResponse & { finalUrl: string }> {
    if (!isTrustedXAIAuthUrl(initialUrl)) throw new GrokSSODeviceError('xAI OAuth URL 不受信任')
    let currentUrl = initialUrl
    let currentMethod = method
    let currentForm = form
    for (let redirects = 0; redirects <= grokSSOMaxRedirects; redirects += 1) {
      const body = currentForm?.toString()
      const headers: Record<string, string | number> = {
        accept: 'application/json, text/html;q=0.9, */*;q=0.8',
        'accept-language': 'zh-CN,zh;q=0.9,en;q=0.8',
        'user-agent': grokSSODefaultUserAgent
      }
      const cookie = this.cookieHeader(currentUrl)
      if (cookie) headers.cookie = cookie
      if (body !== undefined) {
        headers['content-type'] = 'application/x-www-form-urlencoded'
        headers['content-length'] = Buffer.byteLength(body)
      }
      const response = await this.options.request({
        method: currentMethod,
        url: currentUrl,
        headers,
        body,
        proxyUrl: this.options.proxyUrl,
        signal: this.options.signal
      })
      this.captureCookies(response.headers, currentUrl)
      if (Buffer.byteLength(response.body) > grokSSOMaxAuthBodyBytes) {
        throw new GrokSSODeviceError('xAI OAuth 响应超过 2 MiB')
      }
      if (response.statusCode < 300 || response.statusCode > 399) {
        return { ...response, finalUrl: currentUrl }
      }
      const location = firstHeader(response.headers.location)?.trim()
      if (!location) throw new GrokSSODeviceError('xAI OAuth 重定向缺少 Location')
      const nextUrl = new URL(location, currentUrl).toString()
      if (!isTrustedXAIAuthUrl(nextUrl)) throw new GrokSSODeviceError('xAI OAuth 重定向到不受信任的主机')
      currentUrl = nextUrl
      if (response.statusCode === 303 || ((response.statusCode === 301 || response.statusCode === 302) && currentMethod !== 'GET')) {
        currentMethod = 'GET'
        currentForm = undefined
      }
    }
    throw new GrokSSODeviceError('xAI OAuth 重定向次数过多')
  }

  private captureCookies(headers: GrokSSODeviceResponse['headers'], responseUrl: string): void {
    const response = new URL(responseUrl)
    const responseHost = response.hostname.toLowerCase()
    const values = headerValues(headers['set-cookie'])
    for (const value of values) {
      const [pair, ...attributes] = value.split(';')
      const separator = pair?.indexOf('=') ?? -1
      if (separator <= 0) continue
      const name = pair!.slice(0, separator).trim()
      const cookieValue = pair!.slice(separator + 1).trim()
      if (!name || name.length > 128 || cookieValue.length > 16_384 || /[\r\n\0]/u.test(name + cookieValue)) continue
      const parsedAttributes = cookieAttributes(attributes)
      const requestedDomain = parsedAttributes.domain?.replace(/^\./u, '').toLowerCase()
      if (requestedDomain && !cookieDomainMatches(responseHost, requestedDomain)) continue
      const domain = requestedDomain || responseHost
      const path = parsedAttributes.path?.startsWith('/')
        ? parsedAttributes.path
        : defaultCookiePath(response.pathname)
      const maxAge = parsedAttributes['max-age'] === undefined
        ? undefined
        : Number(parsedAttributes['max-age'])
      const expiresAt = Number.isFinite(maxAge)
        ? this.options.now() + Math.trunc(maxAge!) * 1000
        : parsedAttributes.expires
          ? Date.parse(parsedAttributes.expires)
          : undefined
      const cookie: GrokSSOCookie = {
        name,
        value: cookieValue,
        domain,
        path,
        secure: Object.prototype.hasOwnProperty.call(parsedAttributes, 'secure'),
        hostOnly: !requestedDomain,
        ...(Number.isFinite(expiresAt) ? { expiresAt } : {})
      }
      const key = cookieKey(cookie)
      if ((Number.isFinite(maxAge) && maxAge! <= 0) || (cookie.expiresAt !== undefined && cookie.expiresAt <= this.options.now())) {
        this.cookies.delete(key)
      } else {
        this.cookies.set(key, cookie)
      }
    }
  }

  private cookieHeader(requestUrl: string): string {
    const request = new URL(requestUrl)
    const host = request.hostname.toLowerCase()
    const now = this.options.now()
    return [...this.cookies.values()]
      .filter((cookie) => {
        if (cookie.expiresAt !== undefined && cookie.expiresAt <= now) return false
        if (cookie.secure && request.protocol !== 'https:') return false
        if (cookie.hostOnly ? cookie.domain !== host : !cookieDomainMatches(host, cookie.domain)) return false
        return cookiePathMatches(request.pathname, cookie.path)
      })
      .sort((left, right) => right.path.length - left.path.length || left.name.localeCompare(right.name))
      .map(({ name, value }) => `${name}=${value}`)
      .join('; ')
  }

  private storeCookie(cookie: GrokSSOCookie): void {
    this.cookies.set(cookieKey(cookie), cookie)
  }
}

async function performGrokSSODeviceRequest(input: GrokSSODeviceRequest): Promise<GrokSSODeviceResponse> {
  const resolvedProxyUrl = input.proxyUrl?.trim() || runtimeConfig.oauthProxyUrl
  const agent: HttpsProxyAgent<string> | SocksProxyAgent | undefined = resolvedProxyUrl
    ? createProxyAgent(resolvedProxyUrl)
    : undefined
  return await new Promise<GrokSSODeviceResponse>((resolve, reject) => {
    if (input.signal.aborted) {
      reject(input.signal.reason instanceof Error ? input.signal.reason : new Error('请求已取消'))
      return
    }
    const requestHandle = httpsRequest(input.url, {
      method: input.method,
      headers: input.headers,
      agent,
      signal: input.signal,
      timeout: grokSSOConversionTimeoutMs
    }, (response) => {
      const body = new BoundedBufferCollector(grokSSOMaxAuthBodyBytes)
      let settled = false
      const settle = (error?: Error) => {
        if (settled) return
        settled = true
        if (error) reject(error)
        else resolve({
          statusCode: response.statusCode ?? 0,
          headers: normalizeResponseHeaders(response.headers),
          body: body.text()
        })
      }
      response.on('data', (chunk: Buffer) => {
        if (settled) return
        body.append(chunk)
        if (body.truncated) {
          const error = new GrokSSODeviceError('xAI OAuth 响应超过 2 MiB')
          settle(error)
          requestHandle.destroy(error)
        }
      })
      response.once('aborted', () => settle(new Error('xAI OAuth 响应被中断')))
      response.once('error', (error) => settle(error))
      response.once('end', () => settle())
      response.once('close', () => {
        if (!response.complete) settle(new Error('xAI OAuth 响应提前关闭'))
      })
    })
    requestHandle.once('timeout', () => requestHandle.destroy(new Error('xAI OAuth 请求超时')))
    requestHandle.once('error', reject)
    requestHandle.end(input.body)
  })
}

function isTrustedXAIAuthUrl(raw: string): boolean {
  try {
    const url = new URL(raw)
    if (url.username || url.password || url.protocol !== 'https:') return false
    const host = url.hostname.toLowerCase()
    return host === 'x.ai' || host.endsWith('.x.ai')
  } catch {
    return false
  }
}

async function sleepWithSignal(delayMs: number, signal: AbortSignal): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    if (signal.aborted) {
      reject(signal.reason instanceof Error ? signal.reason : new Error('请求已取消'))
      return
    }
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', abort)
      resolve()
    }, delayMs)
    timer.unref()
    const abort = () => {
      clearTimeout(timer)
      reject(signal.reason instanceof Error ? signal.reason : new Error('请求已取消'))
    }
    signal.addEventListener('abort', abort, { once: true })
  })
}

function parseJsonObject(body: string, message: string): Record<string, unknown> {
  try {
    const value: unknown = JSON.parse(body)
    if (typeof value === 'object' && value !== null && !Array.isArray(value)) return value as Record<string, unknown>
  } catch {
    // The caller receives the protocol-specific parse failure below.
  }
  throw new GrokSSODeviceError(message)
}

function normalizeResponseHeaders(headers: import('node:http').IncomingHttpHeaders): GrokSSODeviceResponse['headers'] {
  const output: GrokSSODeviceResponse['headers'] = {}
  for (const [key, value] of Object.entries(headers)) output[key.toLowerCase()] = value
  return output
}

function firstHeader(value: string | string[] | undefined): string | undefined {
  return Array.isArray(value) ? value[0] : value
}

function headerValues(value: string | string[] | undefined): string[] {
  return Array.isArray(value) ? value : value ? [value] : []
}

function cookieAttributes(attributes: string[]): Record<string, string | undefined> {
  const output: Record<string, string | undefined> = {}
  for (const attribute of attributes) {
    const trimmed = attribute.trim()
    if (!trimmed) continue
    const separator = trimmed.indexOf('=')
    const name = (separator < 0 ? trimmed : trimmed.slice(0, separator)).trim().toLowerCase()
    if (!name || Object.prototype.hasOwnProperty.call(output, name)) continue
    output[name] = separator < 0 ? undefined : trimmed.slice(separator + 1).trim()
  }
  return output
}

function cookieKey(cookie: Pick<GrokSSOCookie, 'name' | 'domain' | 'path'>): string {
  return `${cookie.name}\0${cookie.domain}\0${cookie.path}`
}

function cookieDomainMatches(host: string, domain: string): boolean {
  return host === domain || host.endsWith(`.${domain}`)
}

function cookiePathMatches(requestPath: string, cookiePath: string): boolean {
  if (requestPath === cookiePath) return true
  if (!requestPath.startsWith(cookiePath)) return false
  return cookiePath.endsWith('/') || requestPath.charAt(cookiePath.length) === '/'
}

function defaultCookiePath(pathname: string): string {
  if (!pathname.startsWith('/') || pathname === '/') return '/'
  const lastSlash = pathname.lastIndexOf('/')
  return lastSlash <= 0 ? '/' : pathname.slice(0, lastSlash)
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function positiveInteger(value: unknown): number | undefined {
  const parsed = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(parsed) && parsed > 0 ? Math.trunc(parsed) : undefined
}

function sanitizeSSOToken(value: string): string {
  return value.trim().replace(/[\r\n\0]/gu, '')
}

function httpError(message: string, statusCode: number): GrokSSODeviceError {
  return new GrokSSODeviceError(`${message}：xAI OAuth HTTP ${statusCode}`)
}
