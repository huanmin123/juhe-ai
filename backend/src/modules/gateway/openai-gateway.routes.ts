import { randomUUID } from 'node:crypto'
import * as http from 'node:http'
import * as https from 'node:https'
import type { IncomingHttpHeaders, IncomingMessage } from 'node:http'
import { Router, type Request, type Response } from 'express'

import {
  createUsageRecord,
  listOpenAIAccountsForGroup,
  recordAccountStreamFailure,
  updateAccount,
  validateGatewayApiKey,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import { estimateProviderCostUsd } from '../model-pricing/model-pricing.service.js'
import { buildOpenAIOAuthCredentials, createProxyAgent, refreshOpenAIOAuthToken, shouldRefreshOpenAIOAuthCredentials } from '../openai-oauth/openai-oauth.service.js'
import {
  applyAccountErrorHandling,
  parseErrorPayload,
  readGatewaySettings,
  type GatewaySettings
} from './account-error-policy.service.js'
import { persistOpenAICodexUsageHeaders } from './openai-codex-usage.service.js'

export const openAIGatewayRouter = Router()

openAIGatewayRouter.all('/*', async (req, res) => {
  const startedAt = Date.now()
  const requestId = randomUUID()
  const clientIp = extractClientIp(req)
  const endpoint = requestEndpoint(req)
  const requestSnapshot = buildUsageRequestSnapshot(req, requestId, clientIp)
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
    const statusCode = 503
    const responsePayload = { error: { message: 'No available upstream account', type: 'service_unavailable' } }
    createUsageRecord({
      requestId,
      clientIp,
      systemAccountId: apiKeyRecord.system_account_id,
      apiKeyId: apiKeyRecord.id,
      groupId: apiKeyRecord.group_id,
      endpoint,
      providerCode: 'openai',
      model: requestModel(req),
      stream: req.body?.stream === true,
      statusCode,
      success: false,
      durationMs: Date.now() - startedAt,
      errorMessage: responsePayload.error.message,
      requestSnapshot,
      responseSnapshot: buildGatewayErrorResponseSnapshot(statusCode, responsePayload)
    })
    res.status(statusCode).json(responsePayload)
    return
  }

  try {
    const upstreamResult = await fetchFirstAvailableUpstream(req, accounts, gatewaySettings, {
      requestId,
      clientIp,
      systemAccountId: apiKeyRecord.system_account_id,
      apiKeyId: apiKeyRecord.id,
      groupId: apiKeyRecord.group_id,
      endpoint,
      requestSnapshot
    })
    const { account, response: upstreamResponse, upstreamUrl } = upstreamResult

    const contentType = upstreamResponse.headers.get('content-type') ?? ''
    res.status(upstreamResponse.status)
    copyResponseHeaders(upstreamResponse, res)
    persistOpenAICodexHeadersIfNeeded(account, upstreamResponse.headers, 'gateway')

    let usage = emptyUsage()
    let firstTokenMs: number | undefined
    let responseBodyText: string | undefined
    let errorPayload: Record<string, unknown> = {}
    if (contentType.includes('text/event-stream') || contentType.includes('application/octet-stream')) {
      if (!upstreamResponse.body) {
        res.end()
        return
      }
      const streamResult = await pipeUpstreamStream(upstreamResponse.body, res, account, gatewaySettings, startedAt)
      firstTokenMs = streamResult.firstTokenMs
      responseBodyText = Buffer.concat(streamResult.chunks).toString('utf8')
      if (!streamResult.completed) {
        createUsageRecord({
          requestId,
          clientIp,
          systemAccountId: apiKeyRecord.system_account_id,
          apiKeyId: apiKeyRecord.id,
          groupId: apiKeyRecord.group_id,
          accountId: account.id,
          endpoint,
          providerCode: 'openai',
          model: requestModel(req),
          stream: req.body?.stream === true,
          statusCode: upstreamResponse.status,
          success: false,
          firstTokenMs: streamResult.firstTokenMs,
          durationMs: Date.now() - startedAt,
          errorMessage: streamResult.message,
          requestSnapshot,
          responseSnapshot: buildUsageResponseSnapshot({
            upstreamUrl,
            statusCode: upstreamResponse.status,
            headers: upstreamResponse.headers,
            bodyText: responseBodyText,
            errorMessage: streamResult.message
          })
        })
        return
      }
      usage = parseOpenAIUsageFromSseText(responseBodyText)
    } else {
      const responseBody = Buffer.from(await upstreamResponse.arrayBuffer())
      responseBodyText = responseBody.toString('utf8')
      firstTokenMs = Date.now() - startedAt
      usage = parseOpenAIUsageFromJsonBuffer(responseBody)
      if (!upstreamResponse.ok) {
        errorPayload = parseErrorPayload(responseBodyText, upstreamResponse.headers)
      }
      res.send(responseBody)
    }

    if (upstreamResponse.ok) {
      applyAccountErrorHandling(account, { success: true, settings: gatewaySettings })
    }

    createUsageRecord({
      requestId,
      clientIp,
      systemAccountId: apiKeyRecord.system_account_id,
      apiKeyId: apiKeyRecord.id,
      groupId: apiKeyRecord.group_id,
      accountId: account.id,
      endpoint,
      providerCode: 'openai',
      model: requestModel(req),
      stream: req.body?.stream === true,
      statusCode: upstreamResponse.status,
      success: upstreamResponse.ok,
      firstTokenMs,
      durationMs: Date.now() - startedAt,
      inputTokens: usage.inputTokens,
      outputTokens: usage.outputTokens,
      cacheReadTokens: usage.cacheReadTokens,
      costUsd: estimateProviderCostUsd({
        providerCode: 'openai',
        model: requestModel(req),
        inputTokens: usage.inputTokens,
        outputTokens: usage.outputTokens,
        cacheReadTokens: usage.cacheReadTokens
      }),
      errorCode: typeof errorPayload.code === 'string' ? errorPayload.code : undefined,
      errorMessage: typeof errorPayload.message === 'string' ? errorPayload.message : undefined,
      requestSnapshot: upstreamResponse.ok ? undefined : requestSnapshot,
      responseSnapshot: upstreamResponse.ok
        ? undefined
        : buildUsageResponseSnapshot({
          upstreamUrl,
          statusCode: upstreamResponse.status,
          headers: upstreamResponse.headers,
          bodyText: responseBodyText
        })
    })
  } catch (error) {
    const lastAttempt = error instanceof UpstreamAttemptError ? error.lastAttempt : undefined
    const message = error instanceof Error ? error.message : 'No available upstream account'
    const statusCode = 503
    const responsePayload = { error: { message: 'No available upstream account', type: 'service_unavailable' } }
    if (!lastAttempt) {
      createUsageRecord({
        requestId,
        clientIp,
        systemAccountId: apiKeyRecord.system_account_id,
        apiKeyId: apiKeyRecord.id,
        groupId: apiKeyRecord.group_id,
        endpoint,
        providerCode: 'openai',
        model: requestModel(req),
        stream: req.body?.stream === true,
        statusCode,
        success: false,
        durationMs: Date.now() - startedAt,
        errorMessage: message,
        requestSnapshot,
        responseSnapshot: buildGatewayErrorResponseSnapshot(statusCode, responsePayload)
      })
    }
    res.status(statusCode).json(responsePayload)
  }
})

type UpstreamAccount = OpenAIAccountSecret

interface UpstreamAttempt {
  accountId: string
  accountName: string
  upstreamUrl: string
  status?: number
  message?: string
  responseHeaders?: Record<string, string>
  responseBodyText?: string
}

class UpstreamAttemptError extends Error {
  constructor(message: string, readonly lastAttempt?: UpstreamAttempt) {
    super(message)
  }
}

class UpstreamRequestTimeoutError extends Error {}

interface GatewayUsageContext {
  requestId: string
  clientIp?: string
  systemAccountId: string
  apiKeyId: string
  groupId: string
  endpoint: string
  requestSnapshot: UsageRequestSnapshot
}

function extractBearerToken(authorization?: string): string | undefined {
  if (!authorization) return undefined
  const match = authorization.match(/^Bearer\s+(.+)$/i)
  return match?.[1]?.trim()
}

function extractClientIp(req: Request): string | undefined {
  const forwarded = firstHeaderValue(req.header('x-forwarded-for'))
  const realIp = firstHeaderValue(req.header('x-real-ip'))
  const cfIp = firstHeaderValue(req.header('cf-connecting-ip'))
  return normalizeClientIp(forwarded ?? realIp ?? cfIp ?? req.ip ?? req.socket.remoteAddress)
}

function firstHeaderValue(value?: string): string | undefined {
  return value?.split(',').map((item) => item.trim()).find(Boolean)
}

function normalizeClientIp(value?: string): string | undefined {
  if (!value) return undefined
  let ip = value.trim()
  if (!ip) return undefined
  if (ip.startsWith('[')) {
    const end = ip.indexOf(']')
    if (end > 0) ip = ip.slice(1, end)
  }
  if (/^\d{1,3}(?:\.\d{1,3}){3}:\d+$/.test(ip)) {
    ip = ip.replace(/:\d+$/, '')
  }
  if (ip.startsWith('::ffff:')) {
    ip = ip.slice('::ffff:'.length)
  }
  return ip === '::1' ? '127.0.0.1' : ip
}


function requestModel(req: Request): string | undefined {
  return typeof req.body?.model === 'string' ? req.body.model : undefined
}

function requestEndpoint(req: Request): string {
  return `${req.method.toUpperCase()} ${req.originalUrl.split('?')[0] || req.path}`
}

function buildUsageRequestSnapshot(req: Request, requestId: string, clientIp?: string): UsageRequestSnapshot {
  const snapshot: UsageRequestSnapshot = {
    method: req.method,
    path: req.path,
    originalUrl: req.originalUrl,
    clientIp,
    requestId,
    headers: sanitizeRequestHeaders(req.headers)
  }
  if (req.body !== undefined) {
    snapshot.body = req.body
  }
  return snapshot
}

function sanitizeRequestHeaders(headers: IncomingHttpHeaders): Record<string, string | string[]> {
  const output: Record<string, string | string[]> = {}
  const hidden = new Set(['authorization', 'proxy-authorization', 'cookie', 'set-cookie', 'x-api-key'])
  for (const [name, value] of Object.entries(headers)) {
    if (value === undefined) continue
    output[name] = hidden.has(name.toLowerCase()) ? '[redacted]' : value
  }
  return output
}

function buildUsageResponseSnapshot(input: {
  upstreamUrl?: string
  statusCode?: number
  headers?: Headers | Record<string, string>
  bodyText?: string
  errorMessage?: string
  generatedBy?: 'gateway'
}): UsageResponseSnapshot {
  return {
    upstreamUrl: input.upstreamUrl,
    statusCode: input.statusCode,
    headers: input.headers instanceof Headers ? headersToObject(input.headers) : input.headers,
    bodyText: input.bodyText,
    errorMessage: input.errorMessage,
    generatedBy: input.generatedBy
  }
}

function buildGatewayErrorResponseSnapshot(statusCode: number, payload: Record<string, unknown>, lastAttempt?: UpstreamAttempt): UsageResponseSnapshot {
  const snapshot = buildUsageResponseSnapshot({
    statusCode,
    headers: { 'content-type': 'application/json; charset=utf-8' },
    bodyText: JSON.stringify(payload),
    errorMessage: typeof payload.error === 'object' && payload.error !== null
      ? String((payload.error as Record<string, unknown>).message ?? '')
      : undefined,
    generatedBy: 'gateway'
  })

  if (lastAttempt) {
    snapshot.lastUpstreamAttempt = {
      accountId: lastAttempt.accountId,
      accountName: lastAttempt.accountName,
      upstreamUrl: lastAttempt.upstreamUrl,
      statusCode: lastAttempt.status,
      headers: lastAttempt.responseHeaders,
      bodyText: lastAttempt.responseBodyText,
      errorMessage: lastAttempt.message
    }
  }

  return snapshot
}

function headersToObject(headers: Headers): Record<string, string> {
  const output: Record<string, string> = {}
  headers.forEach((value, name) => {
    output[name] = value
  })
  return output
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

function recordFailedUpstreamAttempt(
  req: Request,
  usageContext: GatewayUsageContext,
  account: UpstreamAccount,
  input: {
    upstreamUrl: string
    startedAt: number
    statusCode?: number
    headers?: Headers | Record<string, string>
    bodyText?: string
    errorMessage?: string
  }
): void {
  const errorPayload = input.bodyText && input.headers instanceof Headers
    ? parseErrorPayload(input.bodyText, input.headers)
    : {}
  const errorMessage = input.errorMessage
    ?? (typeof errorPayload.message === 'string' ? errorPayload.message : undefined)
    ?? (typeof input.statusCode === 'number' ? `Upstream returned HTTP ${input.statusCode}` : 'Upstream request failed')

  createUsageRecord({
    requestId: usageContext.requestId,
    clientIp: usageContext.clientIp,
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    accountId: account.id,
    endpoint: usageContext.endpoint,
    providerCode: 'openai',
    model: requestModel(req),
    stream: req.body?.stream === true,
    statusCode: input.statusCode,
    success: false,
    durationMs: Date.now() - input.startedAt,
    errorCode: typeof errorPayload.code === 'string' ? errorPayload.code : undefined,
    errorMessage,
    requestSnapshot: usageContext.requestSnapshot,
    responseSnapshot: buildUsageResponseSnapshot({
      upstreamUrl: input.upstreamUrl,
      statusCode: input.statusCode,
      headers: input.headers,
      bodyText: input.bodyText,
      errorMessage
    })
  })
}

async function fetchFirstAvailableUpstream(
  req: Request,
  accounts: UpstreamAccount[],
  settings: GatewaySettings,
  usageContext: GatewayUsageContext
): Promise<{ account: UpstreamAccount; response: GatewayUpstreamResponse; upstreamUrl: string }> {
  const body = req.method === 'GET' || req.method === 'HEAD' ? undefined : JSON.stringify(req.body ?? {})
  const retryAttempts = Math.max(0, settings.temporaryUnschedulableRetryAttempts)
  const isStreamRequest = req.body?.stream === true
  let lastAttempt: UpstreamAttempt | undefined

  for (const originalAccount of accounts) {
    const account = await prepareUpstreamAccount(originalAccount)
    const headers = buildUpstreamHeaders(req.headers, account.apiKey)
    let skipAccount = false
    for (const upstreamUrl of buildUpstreamUrls(account.baseUrl, req.originalUrl)) {
      for (let attemptIndex = 0; attemptIndex <= retryAttempts; attemptIndex += 1) {
        const attemptStartedAt = Date.now()
        try {
          const response = await requestUpstream(upstreamUrl, {
            method: req.method,
            headers,
            body,
            proxyUrl: account.proxyUrl,
            timeoutMs: upstreamSocketTimeoutMs(req, settings),
            requestTimeoutMs: upstreamRequestTimeoutMs(req, settings)
          })
          lastAttempt = { accountId: account.id, accountName: account.name, upstreamUrl, status: response.status }
          if (response.ok) {
            if (isStreamRequest && settings.streamCircuitBreakerEnabled && isStreamResponse(response)) {
              try {
                const preloadedResponse = await preloadStreamResponseFirstChunk(response, settings)
                return { account, response: preloadedResponse, upstreamUrl }
              } catch (error) {
                const message = error instanceof Error ? error.message : 'Upstream stream request interrupted before first chunk'
                lastAttempt = { accountId: account.id, accountName: account.name, upstreamUrl, status: response.status, message }
                recordFailedUpstreamAttempt(req, usageContext, account, {
                  upstreamUrl,
                  startedAt: attemptStartedAt,
                  statusCode: response.status,
                  headers: response.headers,
                  errorMessage: message
                })
                handleStreamFailure(account, message, settings)
                skipAccount = true
                break
              }
            }
            return { account, response, upstreamUrl }
          }

          const responseBody = Buffer.from(await response.arrayBuffer())
          const responseBodyText = responseBody.toString('utf8')
          lastAttempt = {
            ...lastAttempt,
            responseHeaders: headersToObject(response.headers),
            responseBodyText
          }
          recordFailedUpstreamAttempt(req, usageContext, account, {
            upstreamUrl,
            startedAt: attemptStartedAt,
            statusCode: response.status,
            headers: response.headers,
            bodyText: responseBodyText
          })
          persistOpenAICodexHeadersIfNeeded(account, response.headers, 'gateway_error')
          applyAccountErrorHandling(account, {
            success: false,
            statusCode: response.status,
            headers: response.headers,
            bodyText: responseBodyText,
            settings
          })
          skipAccount = true
          break
        } catch (error) {
          const message = error instanceof Error ? error.message : 'request failed'
          lastAttempt = {
            accountId: account.id,
            accountName: account.name,
            upstreamUrl,
            message
          }
          recordFailedUpstreamAttempt(req, usageContext, account, {
            upstreamUrl,
            startedAt: attemptStartedAt,
            errorMessage: message
          })
          if (isStreamRequest && error instanceof UpstreamRequestTimeoutError) {
            handleStreamFailure(account, message, settings)
            skipAccount = true
            break
          }
          if (attemptIndex < retryAttempts) {
            await waitBeforeTemporaryUnschedulableRetry(settings)
            continue
          }
          applyAccountErrorHandling(account, { success: false, errorMessage: message, settings })
          skipAccount = true
          break
        }
      }
      if (skipAccount) {
        break
      }
    }
  }

  throw new UpstreamAttemptError(
    lastAttempt
      ? 'All upstream accounts failed; last attempt ' + lastAttempt.accountName + ' ' + lastAttempt.upstreamUrl + ' returned ' + (lastAttempt.message ?? lastAttempt.status)
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

interface UsageRequestSnapshot {
  method: string
  path: string
  originalUrl: string
  clientIp?: string
  requestId: string
  headers: Record<string, string | string[]>
  body?: unknown
}

interface UsageResponseSnapshot {
  upstreamUrl?: string
  statusCode?: number
  headers?: Record<string, string>
  bodyText?: string
  errorMessage?: string
  generatedBy?: 'gateway'
  lastUpstreamAttempt?: {
    accountId: string
    accountName: string
    upstreamUrl: string
    statusCode?: number
    headers?: Record<string, string>
    bodyText?: string
    errorMessage?: string
  }
}

interface UpstreamRequestOptions {
  method: string
  headers: Headers
  body?: string
  proxyUrl?: string
  timeoutMs?: number
  requestTimeoutMs?: number
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

class PreloadedGatewayUpstreamResponse implements GatewayUpstreamResponse {
  constructor(
    private readonly source: GatewayUpstreamResponse,
    private readonly iterator: AsyncIterator<Uint8Array>,
    private readonly firstResult: IteratorResult<Uint8Array>
  ) {}

  get status(): number {
    return this.source.status
  }

  get ok(): boolean {
    return this.source.ok
  }

  get headers(): Headers {
    return this.source.headers
  }

  get body(): AsyncIterable<Uint8Array> | null {
    return iteratePreloadedStream(this.iterator, this.firstResult)
  }

  async arrayBuffer(): Promise<ArrayBuffer> {
    const body = this.body
    return body ? collectAsyncIterableBody(body) : new ArrayBuffer(0)
  }
}

async function* iteratePreloadedStream(iterator: AsyncIterator<Uint8Array>, firstResult: IteratorResult<Uint8Array>): AsyncIterable<Uint8Array> {
  if (!firstResult.done) {
    yield firstResult.value
  }
  while (true) {
    const result = await iterator.next()
    if (result.done) {
      break
    }
    yield result.value
  }
}

async function collectAsyncIterableBody(body: AsyncIterable<Uint8Array>): Promise<ArrayBuffer> {
  const chunks: Buffer[] = []
  for await (const chunk of body) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk))
  }
  const buffer = Buffer.concat(chunks)
  const arrayBuffer = new ArrayBuffer(buffer.byteLength)
  new Uint8Array(arrayBuffer).set(buffer)
  return arrayBuffer
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
    let requestTimeout: NodeJS.Timeout | undefined
    const clearRequestTimeout = () => {
      if (requestTimeout) {
        clearTimeout(requestTimeout)
        requestTimeout = undefined
      }
    }
    const request = transport.request(url, requestOptions, (message) => {
      clearRequestTimeout()
      resolve(new NodeGatewayUpstreamResponse(message))
    })
    const abort = () => request.destroy(new Error('Upstream request timed out'))

    if (options.requestTimeoutMs !== undefined) {
      const seconds = Math.ceil(options.requestTimeoutMs / 1000)
      requestTimeout = setTimeout(() => request.destroy(new UpstreamRequestTimeoutError(`Upstream stream request timeout after ${seconds}s`)), options.requestTimeoutMs)
    }
    request.setTimeout(options.timeoutMs ?? 120000, abort)
    request.on('error', (error) => {
      clearRequestTimeout()
      reject(error)
    })
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
  firstTokenMs?: number
}

async function pipeUpstreamStream(
  upstreamBody: AsyncIterable<Uint8Array>,
  res: Response,
  account: UpstreamAccount,
  settings: GatewaySettings,
  startedAt: number
): Promise<StreamPipeResult> {
  const chunks: Buffer[] = []
  const iterator = upstreamBody[Symbol.asyncIterator]()
  let completed = false
  let firstTokenMs: number | undefined

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
      if (firstTokenMs === undefined && buffer.length > 0) {
        firstTokenMs = Date.now() - startedAt
      }
      chunks.push(buffer)
      res.write(buffer)
    }
  } catch (error) {
    const message = error instanceof Error ? error.message : 'Upstream stream interrupted'
    handleStreamFailure(account, message, settings)
    res.end()
    return { completed: false, chunks, message, firstTokenMs }
  }

  res.end()

  if (!completed) {
    const message = 'Upstream stream interrupted before completion'
    handleStreamFailure(account, message, settings)
    return { completed: false, chunks, message, firstTokenMs }
  }

  return { completed: true, chunks, message: 'completed', firstTokenMs }
}

async function readStreamChunkWithIdleTimeout(
  iterator: AsyncIterator<Uint8Array>,
  timeoutSeconds: number
): Promise<IteratorResult<Uint8Array>> {
  return readStreamChunkWithTimeout(
    iterator,
    timeoutSeconds,
    () => new Error(`Upstream stream idle timeout after ${timeoutSeconds}s`)
  )
}

async function preloadStreamResponseFirstChunk(
  response: GatewayUpstreamResponse,
  settings: GatewaySettings
): Promise<GatewayUpstreamResponse> {
  if (!response.body) {
    throw new Error('Upstream stream response has no body')
  }
  const iterator = response.body[Symbol.asyncIterator]()
  try {
    const firstResult = await readStreamChunkWithTimeout(
      iterator,
      settings.streamRequestTimeoutSeconds,
      () => new UpstreamRequestTimeoutError(`Upstream stream request timeout after ${settings.streamRequestTimeoutSeconds}s`)
    )
    if (firstResult.done) {
      throw new Error('Upstream stream ended before first chunk')
    }
    return new PreloadedGatewayUpstreamResponse(response, iterator, firstResult)
  } catch (error) {
    await closeAsyncIterator(iterator)
    throw error
  }
}

async function readStreamChunkWithTimeout(
  iterator: AsyncIterator<Uint8Array>,
  timeoutSeconds: number,
  createError: () => Error
): Promise<IteratorResult<Uint8Array>> {
  let timeout: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      iterator.next(),
      new Promise<IteratorResult<Uint8Array>>((_, reject) => {
        timeout = setTimeout(() => reject(createError()), timeoutSeconds * 1000)
      })
    ])
  } finally {
    if (timeout) {
      clearTimeout(timeout)
    }
  }
}

async function closeAsyncIterator(iterator: AsyncIterator<Uint8Array>): Promise<void> {
  if (!iterator.return) {
    return
  }
  try {
    await iterator.return()
  } catch {
  }
}

async function waitBeforeTemporaryUnschedulableRetry(settings: GatewaySettings): Promise<void> {
  const intervalMs = Math.max(0, settings.temporaryUnschedulableRetryIntervalSeconds) * 1000
  if (intervalMs <= 0) {
    return
  }
  await new Promise((resolve) => setTimeout(resolve, intervalMs))
}

function handleStreamFailure(account: UpstreamAccount, reason: string, settings: GatewaySettings): void {
  if (!settings.streamCircuitBreakerEnabled) {
    return
  }

  recordAccountStreamFailure({
    accountId: account.id,
    thresholdCount: settings.streamFailureThresholdCount,
    thresholdWindowMinutes: settings.streamFailureThresholdWindowMinutes,
    action: 'cooldown',
    cooldownMinutes: settings.defaultTemporaryUnschedulableMinutes,
    reason
  })
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

function upstreamSocketTimeoutMs(req: Request, settings: GatewaySettings): number {
  const isStreamRequest = req.body?.stream === true
  if (!isStreamRequest || !settings.streamCircuitBreakerEnabled) {
    return 120000
  }
  return Math.max(settings.streamRequestTimeoutSeconds, settings.streamIdleTimeoutSeconds + 15, 30) * 1000
}

function upstreamRequestTimeoutMs(req: Request, settings: GatewaySettings): number | undefined {
  if (req.body?.stream !== true || !settings.streamCircuitBreakerEnabled) {
    return undefined
  }
  return Math.max(1, settings.streamRequestTimeoutSeconds) * 1000
}

function isStreamResponse(response: GatewayUpstreamResponse): boolean {
  const contentType = response.headers.get('content-type') ?? ''
  return contentType.includes('text/event-stream') || contentType.includes('application/octet-stream')
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

function persistOpenAICodexHeadersIfNeeded(account: UpstreamAccount, headers: Headers, source: string): void {
  if (account.type !== 'oauth') return
  persistOpenAICodexUsageHeaders(account.id, headers, source)
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
