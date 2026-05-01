import { randomUUID } from 'node:crypto'
import * as http from 'node:http'
import * as https from 'node:https'
import type { IncomingHttpHeaders, IncomingMessage } from 'node:http'
import { Router, type Request, type Response } from 'express'

import {
  clearAccountFailureState,
  createUsageRecord,
  getSettings,
  listOpenAIAccountsForGroup,
  markAccountCooldown,
  recordAccountStreamFailure,
  updateAccount,
  validateGatewayApiKey,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import { buildOpenAIOAuthCredentials, createProxyAgent, refreshOpenAIOAuthToken, shouldRefreshOpenAIOAuthCredentials } from '../openai-oauth/openai-oauth.service.js'

export const openAIGatewayRouter = Router()

openAIGatewayRouter.all('/*', async (req, res) => {
  const startedAt = Date.now()
  const requestId = randomUUID()
  const gatewayApiKey = extractBearerToken(req.header('authorization'))
  const gatewaySettings = readGatewaySettings()

  if (!gatewayApiKey) {
    res.status(401).json({ error: { message: 'Missing bearer token', type: 'invalid_request_error' } })
    return
  }

  const apiKeyRecord = validateGatewayApiKey(gatewayApiKey)
  if (!apiKeyRecord) {
    res.status(401).json({ error: { message: 'Invalid API key', type: 'invalid_request_error' } })
    return
  }

  const accounts = listOpenAIAccountsForGroup(apiKeyRecord.group_id)
  if (accounts.length === 0) {
    res.status(503).json({ error: { message: 'No available OpenAI account in bound group', type: 'service_unavailable' } })
    return
  }

  try {
    const upstreamResult = await fetchFirstAvailableUpstream(req, accounts, gatewaySettings)
    const { account, response: upstreamResponse } = upstreamResult

    const contentType = upstreamResponse.headers.get('content-type') ?? ''
    res.status(upstreamResponse.status)
    copyResponseHeaders(upstreamResponse, res)

    let usage = emptyUsage()
    if (contentType.includes('text/event-stream') || contentType.includes('application/octet-stream')) {
      if (!upstreamResponse.body) {
        res.end()
        return
      }
      const streamResult = await pipeUpstreamStream(upstreamResponse.body, res, account, gatewaySettings)
      if (!streamResult.completed) {
        createUsageRecord({
          requestId,
          apiKeyId: apiKeyRecord.id,
          groupId: apiKeyRecord.group_id,
          accountId: account.id,
          providerCode: 'openai',
          model: typeof req.body?.model === 'string' ? req.body.model : undefined,
          stream: req.body?.stream === true,
          statusCode: upstreamResponse.status,
          success: false,
          durationMs: Date.now() - startedAt,
          errorMessage: streamResult.message
        })
        return
      }
      usage = parseOpenAIUsageFromSseText(Buffer.concat(streamResult.chunks).toString('utf8'))
    } else {
      const responseBody = Buffer.from(await upstreamResponse.arrayBuffer())
      usage = parseOpenAIUsageFromJsonBuffer(responseBody)
      res.send(responseBody)
    }

    if (upstreamResponse.ok) {
      clearAccountFailureState(account.id)
    }

    createUsageRecord({
      requestId,
      apiKeyId: apiKeyRecord.id,
      groupId: apiKeyRecord.group_id,
      accountId: account.id,
      providerCode: 'openai',
      model: typeof req.body?.model === 'string' ? req.body.model : undefined,
      stream: req.body?.stream === true,
      statusCode: upstreamResponse.status,
      success: upstreamResponse.ok,
      durationMs: Date.now() - startedAt,
      inputTokens: usage.inputTokens,
      outputTokens: usage.outputTokens,
      cacheReadTokens: usage.cacheReadTokens,
      costUsd: estimateOpenAICost({
        model: typeof req.body?.model === 'string' ? req.body.model : undefined,
        inputTokens: usage.inputTokens,
        outputTokens: usage.outputTokens,
        cacheReadTokens: usage.cacheReadTokens
      })
    })
  } catch (error) {
    const lastAttempt = error instanceof UpstreamAttemptError ? error.lastAttempt : undefined
    const message = error instanceof Error ? error.message : 'Upstream request failed'
    const statusCode = 502
    createUsageRecord({
      requestId,
      apiKeyId: apiKeyRecord.id,
      groupId: apiKeyRecord.group_id,
      accountId: lastAttempt?.accountId,
      providerCode: 'openai',
      model: typeof req.body?.model === 'string' ? req.body.model : undefined,
      stream: req.body?.stream === true,
      statusCode,
      success: false,
      durationMs: Date.now() - startedAt,
      errorMessage: message
    })
    res.status(statusCode).json({ error: { message, type: 'upstream_error' } })
  }
})

type UpstreamAccount = OpenAIAccountSecret

type StreamFailureAction = 'cooldown' | 'disable' | 'none'

interface GatewaySettings {
  streamCircuitBreakerEnabled: boolean
  streamIdleTimeoutSeconds: number
  streamFailureAction: StreamFailureAction
  streamAccountCooldownMinutes: number
  streamFailureThresholdCount: number
  streamFailureThresholdWindowMinutes: number
  overloadCooldownEnabled: boolean
  overloadCooldownMinutes: number
}

interface UpstreamAttempt {
  accountId: string
  accountName: string
  upstreamUrl: string
  status?: number
  message?: string
}

class UpstreamAttemptError extends Error {
  constructor(message: string, readonly lastAttempt?: UpstreamAttempt) {
    super(message)
  }
}

function readGatewaySettings(): GatewaySettings {
  const settings = getSettings()
  return {
    streamCircuitBreakerEnabled: booleanSetting(settings.streamCircuitBreakerEnabled, false),
    streamIdleTimeoutSeconds: numberSetting(settings.streamIdleTimeoutSeconds, 180, 10, 3600),
    streamFailureAction: actionSetting(settings.streamFailureAction),
    streamAccountCooldownMinutes: numberSetting(settings.streamAccountCooldownMinutes, 5, 1, 1440),
    streamFailureThresholdCount: numberSetting(settings.streamFailureThresholdCount, 3, 1, 100),
    streamFailureThresholdWindowMinutes: numberSetting(settings.streamFailureThresholdWindowMinutes, 10, 1, 1440),
    overloadCooldownEnabled: booleanSetting(settings.overloadCooldownEnabled, true),
    overloadCooldownMinutes: numberSetting(settings.overloadCooldownMinutes, 10, 1, 1440)
  }
}

function booleanSetting(value: unknown, fallback: boolean): boolean {
  return typeof value === 'boolean' ? value : fallback
}

function numberSetting(value: unknown, fallback: number, min: number, max: number): number {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  if (!Number.isFinite(number)) return fallback
  return Math.min(Math.max(Math.trunc(number), min), max)
}

function actionSetting(value: unknown): StreamFailureAction {
  return value === 'disable' || value === 'none' || value === 'cooldown' ? value : 'cooldown'
}

function extractBearerToken(authorization?: string): string | undefined {
  if (!authorization) return undefined
  const match = authorization.match(/^Bearer\s+(.+)$/i)
  return match?.[1]?.trim()
}

function buildUpstreamUrl(baseUrl: string, pathAndQuery: string): string {
  const normalizedBase = baseUrl.trim().replace(/\/+$/, '')
  const requestPath = pathAndQuery.startsWith('/') ? pathAndQuery : `/${pathAndQuery}`
  const normalizedPath = normalizedBase.endsWith('/v1') ? requestPath.replace(/^\/v1/, '') || '/' : requestPath
  return `${normalizedBase}${normalizedPath}`
}

function buildUpstreamUrls(baseUrl: string, pathAndQuery: string): string[] {
  const primary = buildUpstreamUrl(baseUrl, pathAndQuery)
  const fallbackBase = baseUrl.trim().replace(/\/+$/, '')
  const fallback = fallbackBase.endsWith('/v1')
    ? `${fallbackBase}${(pathAndQuery.startsWith('/') ? pathAndQuery : `/${pathAndQuery}`).replace(/^\/v1/, '') || '/'}`
    : `${fallbackBase}${pathAndQuery.startsWith('/v1') ? pathAndQuery.replace(/^\/v1/, '') || '/' : pathAndQuery}`
  return [...new Set([primary, fallback])]
}

async function fetchFirstAvailableUpstream(req: Request, accounts: UpstreamAccount[], settings: GatewaySettings): Promise<{ account: UpstreamAccount; response: GatewayUpstreamResponse; upstreamUrl: string }> {
  const body = req.method === 'GET' || req.method === 'HEAD' ? undefined : JSON.stringify(req.body ?? {})
  let lastAttempt: UpstreamAttempt | undefined

  for (const originalAccount of accounts) {
    const account = await prepareUpstreamAccount(originalAccount)
    const headers = buildUpstreamHeaders(req.headers, account.apiKey)
    for (const upstreamUrl of buildUpstreamUrls(account.baseUrl, req.originalUrl)) {
      try {
        const response = await requestUpstream(upstreamUrl, {
          method: req.method,
          headers,
          body,
          proxyUrl: account.proxyUrl,
          timeoutMs: upstreamSocketTimeoutMs(req, settings)
        })
        lastAttempt = { accountId: account.id, accountName: account.name, upstreamUrl, status: response.status }
        if (response.ok || !shouldTryNextUpstream(response.status)) {
          return { account, response, upstreamUrl }
        }
        handleRetryableUpstreamStatus(account, response.status, settings)
        await response.arrayBuffer().catch(() => undefined)
      } catch (error) {
        lastAttempt = {
          accountId: account.id,
          accountName: account.name,
          upstreamUrl,
          message: error instanceof Error ? error.message : 'request failed'
        }
      }
    }
  }

  throw new UpstreamAttemptError(
    lastAttempt
      ? `All upstream accounts failed; last attempt ${lastAttempt.accountName} ${lastAttempt.upstreamUrl} returned ${lastAttempt.status ?? lastAttempt.message}`
      : 'All upstream accounts failed',
    lastAttempt
  )
}

interface GatewayUpstreamResponse {
  readonly status: number
  readonly ok: boolean
  readonly headers: Headers
  readonly body: AsyncIterable<Uint8Array> | null
  arrayBuffer(): Promise<ArrayBuffer>
}

interface UpstreamRequestOptions {
  method: string
  headers: Headers
  body?: string
  proxyUrl?: string
  timeoutMs?: number
}

class NodeGatewayUpstreamResponse implements GatewayUpstreamResponse {
  constructor(private readonly message: IncomingMessage) {}

  get status(): number {
    return this.message.statusCode ?? 0
  }

  get ok(): boolean {
    return this.status >= 200 && this.status < 300
  }

  get headers(): Headers {
    return headersFromIncoming(this.message.headers)
  }

  get body(): AsyncIterable<Uint8Array> | null {
    return this.message as AsyncIterable<Uint8Array>
  }

  async arrayBuffer(): Promise<ArrayBuffer> {
    const buffer = await collectIncomingBody(this.message)
    const arrayBuffer = new ArrayBuffer(buffer.byteLength)
    new Uint8Array(arrayBuffer).set(buffer)
    return arrayBuffer
  }
}

function requestUpstream(upstreamUrl: string, options: UpstreamRequestOptions): Promise<GatewayUpstreamResponse> {
  return new Promise((resolve, reject) => {
    const url = new URL(upstreamUrl)
    const transport = url.protocol === 'http:' ? http : https
    const requestOptions: http.RequestOptions = {
      method: options.method,
      headers: headersToNodeHeaders(options.headers),
      agent: options.proxyUrl ? createProxyAgent(options.proxyUrl) as http.Agent : undefined
    }
    const request = transport.request(url, requestOptions, (message) => {
      resolve(new NodeGatewayUpstreamResponse(message))
    })
    const abort = () => request.destroy(new Error('Upstream request timed out'))

    request.setTimeout(options.timeoutMs ?? 120000, abort)
    request.on('error', reject)
    if (options.body) {
      request.write(options.body)
    }
    request.end()
  })
}

function headersToNodeHeaders(headers: Headers): http.OutgoingHttpHeaders {
  const output: http.OutgoingHttpHeaders = {}
  headers.forEach((value, name) => {
    output[name] = value
  })
  return output
}

function headersFromIncoming(headers: IncomingHttpHeaders): Headers {
  const output = new Headers()
  for (const [name, value] of Object.entries(headers)) {
    if (value === undefined) continue
    if (Array.isArray(value)) {
      for (const item of value) output.append(name, item)
    } else {
      output.set(name, value)
    }
  }
  return output
}

async function collectIncomingBody(message: IncomingMessage): Promise<Buffer> {
  const chunks: Buffer[] = []
  for await (const chunk of message) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk))
  }
  return Buffer.concat(chunks)
}

interface StreamPipeResult {
  completed: boolean
  chunks: Buffer[]
  message: string
}

async function pipeUpstreamStream(
  upstreamBody: AsyncIterable<Uint8Array>,
  res: Response,
  account: UpstreamAccount,
  settings: GatewaySettings
): Promise<StreamPipeResult> {
  const chunks: Buffer[] = []
  const iterator = upstreamBody[Symbol.asyncIterator]()
  let completed = false

  try {
    while (true) {
      const result = settings.streamCircuitBreakerEnabled
        ? await readStreamChunkWithIdleTimeout(iterator, settings.streamIdleTimeoutSeconds)
        : await iterator.next()

      if (result.done) {
        completed = true
        break
      }

      const buffer = Buffer.from(result.value)
      chunks.push(buffer)
      res.write(buffer)
    }
  } catch (error) {
    const message = error instanceof Error ? error.message : 'Upstream stream interrupted'
    handleStreamFailure(account, message, settings)
    res.write(formatSseError(message))
    res.end()
    return { completed: false, chunks, message }
  }

  res.end()

  if (!completed) {
    const message = 'Upstream stream interrupted before completion'
    handleStreamFailure(account, message, settings)
    return { completed: false, chunks, message }
  }

  return { completed: true, chunks, message: 'completed' }
}

async function readStreamChunkWithIdleTimeout(
  iterator: AsyncIterator<Uint8Array>,
  timeoutSeconds: number
): Promise<IteratorResult<Uint8Array>> {
  let timeout: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      iterator.next(),
      new Promise<IteratorResult<Uint8Array>>((_, reject) => {
        timeout = setTimeout(() => reject(new Error(`Upstream stream idle timeout after ${timeoutSeconds}s`)), timeoutSeconds * 1000)
      })
    ])
  } finally {
    if (timeout) {
      clearTimeout(timeout)
    }
  }
}

function handleStreamFailure(account: UpstreamAccount, reason: string, settings: GatewaySettings): void {
  if (!settings.streamCircuitBreakerEnabled) {
    return
  }

  recordAccountStreamFailure({
    accountId: account.id,
    thresholdCount: settings.streamFailureThresholdCount,
    thresholdWindowMinutes: settings.streamFailureThresholdWindowMinutes,
    action: settings.streamFailureAction,
    cooldownMinutes: settings.streamAccountCooldownMinutes,
    reason
  })
}

function handleRetryableUpstreamStatus(account: UpstreamAccount, status: number, settings: GatewaySettings): void {
  if (!settings.overloadCooldownEnabled || (status !== 429 && status !== 503)) {
    return
  }

  const until = new Date(Date.now() + settings.overloadCooldownMinutes * 60_000).toISOString()
  markAccountCooldown(account.id, until, `Upstream returned ${status}; temporary cooldown`)
}

function formatSseError(message: string): string {
  return `event: error\ndata: ${JSON.stringify({ error: { message, type: 'upstream_stream_error' } })}\n\n`
}

async function prepareUpstreamAccount(account: UpstreamAccount): Promise<UpstreamAccount> {
  if (account.type !== 'oauth' || !shouldRefreshOpenAIOAuthCredentials(account.credentials) || !account.refreshToken) {
    return account
  }

  const tokenInfo = await refreshOpenAIOAuthToken({
    refreshToken: account.refreshToken,
    clientId: account.clientId,
    proxyUrl: account.proxyUrl
  })
  const credentials = {
    ...account.credentials,
    ...buildOpenAIOAuthCredentials(tokenInfo, { refreshToken: account.refreshToken })
  }
  updateAccount(account.id, { credentials, status: 'active' })
  const accessToken = typeof credentials.access_token === 'string' ? credentials.access_token : account.apiKey
  return {
    ...account,
    apiKey: accessToken,
    baseUrl: typeof credentials.base_url === 'string' && credentials.base_url ? credentials.base_url : account.baseUrl,
    refreshToken: typeof credentials.refresh_token === 'string' ? credentials.refresh_token : account.refreshToken,
    clientId: typeof credentials.client_id === 'string' ? credentials.client_id : account.clientId,
    expiresAt: typeof credentials.expires_at === 'string' ? credentials.expires_at : account.expiresAt,
    credentials
  }
}

function shouldTryNextUpstream(status: number): boolean {
  return status === 402 || status === 403 || status === 404 || status === 429 || status === 500 || status === 502 || status === 503 || status === 504
}

function upstreamSocketTimeoutMs(req: Request, settings: GatewaySettings): number {
  const isStreamRequest = req.body?.stream === true
  if (!isStreamRequest || !settings.streamCircuitBreakerEnabled) {
    return 120000
  }
  return Math.max(settings.streamIdleTimeoutSeconds + 15, 30) * 1000
}

const skippedUpstreamRequestHeaders = new Set([
  'host',
  'authorization',
  'content-length',
  'connection',
  'keep-alive',
  'proxy-authenticate',
  'proxy-authorization',
  'te',
  'trailer',
  'transfer-encoding',
  'upgrade',
  'expect',
  'content-encoding',
  'accept-encoding'
])

function buildUpstreamHeaders(inputHeaders: Record<string, string | string[] | undefined>, apiKey: string): Headers {
  const headers = new Headers()
  for (const [name, value] of Object.entries(inputHeaders)) {
    const lowerName = name.toLowerCase()
    if (skippedUpstreamRequestHeaders.has(lowerName)) {
      continue
    }
    if (Array.isArray(value)) {
      headers.set(name, value.join(', '))
    } else if (typeof value === 'string') {
      headers.set(name, value)
    }
  }
  headers.set('authorization', `Bearer ${apiKey}`)
  headers.set('content-type', headers.get('content-type') ?? 'application/json')
  return headers
}

function copyResponseHeaders(upstreamResponse: GatewayUpstreamResponse, res: { setHeader: (name: string, value: string) => void }): void {
  upstreamResponse.headers.forEach((value, name) => {
    const lowerName = name.toLowerCase()
    if (['content-length', 'content-encoding', 'connection', 'transfer-encoding'].includes(lowerName)) {
      return
    }
    res.setHeader(name, value)
  })
}

interface ParsedUsage {
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
}

function emptyUsage(): ParsedUsage {
  return {}
}

function parseOpenAIUsageFromJsonBuffer(responseBody: Buffer): ParsedUsage {
  if (responseBody.length === 0) return emptyUsage()
  try {
    const payload = JSON.parse(responseBody.toString('utf8')) as Record<string, unknown>
    return extractUsage(payload.usage)
  } catch {
    return emptyUsage()
  }
}

function parseOpenAIUsageFromSseText(text: string): ParsedUsage {
  let usage = emptyUsage()
  for (const line of text.split(/\r?\n/)) {
    if (!line.startsWith('data:')) continue
    const data = line.slice(5).trim()
    if (!data || data === '[DONE]') continue
    try {
      const event = JSON.parse(data) as Record<string, unknown>
      const eventType = typeof event.type === 'string' ? event.type : ''
      if (eventType !== 'response.completed' && eventType !== 'response.done' && eventType !== 'response.failed') {
        continue
      }
      const nextUsage = extractUsage(typeof event.response === 'object' && event.response !== null ? (event.response as Record<string, unknown>).usage : event.usage)
      if (nextUsage.inputTokens !== undefined || nextUsage.outputTokens !== undefined || nextUsage.cacheReadTokens !== undefined) {
        usage = nextUsage
      }
    } catch {
      continue
    }
  }
  return usage
}

function extractUsage(value: unknown): ParsedUsage {
  if (typeof value !== 'object' || value === null) return emptyUsage()
  const usage = value as Record<string, unknown>
  const details = typeof usage.input_tokens_details === 'object' && usage.input_tokens_details !== null
    ? usage.input_tokens_details as Record<string, unknown>
    : {}
  const inputTokens = numberValue(usage.input_tokens)
  const outputTokens = numberValue(usage.output_tokens)
  const cacheReadTokens = numberValue(details.cached_tokens)
  return { inputTokens, outputTokens, cacheReadTokens }
}

function numberValue(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) && number >= 0 ? Math.trunc(number) : undefined
}

interface CostInput {
  model?: string
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
}

interface OpenAIModelPricing {
  inputPerMillion: number
  outputPerMillion: number
  cachedInputPerMillion?: number
}

const openAIPricing: Array<{ pattern: RegExp; pricing: OpenAIModelPricing }> = [
  { pattern: /^gpt-5\.4-mini($|-)/i, pricing: { inputPerMillion: 0.75, outputPerMillion: 4.5, cachedInputPerMillion: 0.075 } },
  { pattern: /^gpt-5\.4-nano($|-)/i, pricing: { inputPerMillion: 0.2, outputPerMillion: 1.25, cachedInputPerMillion: 0.02 } },
  { pattern: /^gpt-5\.4($|-)/i, pricing: { inputPerMillion: 2.5, outputPerMillion: 15, cachedInputPerMillion: 0.25 } },
  { pattern: /^gpt-5-mini($|-)/i, pricing: { inputPerMillion: 0.25, outputPerMillion: 2, cachedInputPerMillion: 0.025 } },
  { pattern: /^gpt-5-nano($|-)/i, pricing: { inputPerMillion: 0.05, outputPerMillion: 0.4, cachedInputPerMillion: 0.005 } },
  { pattern: /^gpt-5($|-)/i, pricing: { inputPerMillion: 1.25, outputPerMillion: 10, cachedInputPerMillion: 0.125 } },
  { pattern: /^gpt-4\.1-mini($|-)/i, pricing: { inputPerMillion: 0.4, outputPerMillion: 1.6, cachedInputPerMillion: 0.1 } },
  { pattern: /^gpt-4\.1-nano($|-)/i, pricing: { inputPerMillion: 0.1, outputPerMillion: 0.4, cachedInputPerMillion: 0.025 } },
  { pattern: /^gpt-4\.1($|-)/i, pricing: { inputPerMillion: 2, outputPerMillion: 8, cachedInputPerMillion: 0.5 } },
  { pattern: /^gpt-4o-mini($|-)/i, pricing: { inputPerMillion: 0.15, outputPerMillion: 0.6, cachedInputPerMillion: 0.075 } },
  { pattern: /^gpt-4o($|-)/i, pricing: { inputPerMillion: 2.5, outputPerMillion: 10, cachedInputPerMillion: 1.25 } }
]

function estimateOpenAICost(input: CostInput): number | undefined {
  if (!input.model || (input.inputTokens === undefined && input.outputTokens === undefined && input.cacheReadTokens === undefined)) {
    return undefined
  }
  const pricing = openAIPricing.find((item) => item.pattern.test(input.model ?? ''))?.pricing
  if (!pricing) return undefined
  const cacheReadTokens = Math.min(input.cacheReadTokens ?? 0, input.inputTokens ?? 0)
  const uncachedInputTokens = Math.max((input.inputTokens ?? 0) - cacheReadTokens, 0)
  const cost = (uncachedInputTokens * pricing.inputPerMillion
    + cacheReadTokens * (pricing.cachedInputPerMillion ?? pricing.inputPerMillion)
    + (input.outputTokens ?? 0) * pricing.outputPerMillion) / 1_000_000
  return Number(cost.toFixed(10))
}



