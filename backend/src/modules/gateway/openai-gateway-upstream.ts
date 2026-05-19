import * as http from 'node:http'
import * as https from 'node:https'
import type { IncomingHttpHeaders, IncomingMessage } from 'node:http'
import type { Request } from 'express'

import { createProxyAgent } from '../openai-oauth/openai-oauth.service.js'
import type { GatewaySettings } from './account-error-policy.service.js'
import {
  buildOpenAIOAuthCodexRequestParts,
  isOpenAIOAuthCodexCompactRequest,
  type OpenAIOAuthCodexIdentity
} from './openai-oauth-codex-adapter.js'
import { getGatewayRequestBodyState, type GatewayRawBodyRequest } from './openai-gateway-request-body.js'
import { requestStream } from './openai-gateway-usage.js'

export interface GatewayUpstreamResponse {
  readonly status: number
  readonly ok: boolean
  readonly headers: Headers
  readonly body: AsyncIterable<Uint8Array> | null
}

interface UpstreamRequestOptions {
  method: string
  headers: Headers
  body?: Buffer | string
  proxyUrl?: string
  timeoutMs?: number
  requestTimeoutMs?: number
  signal?: AbortSignal
}

interface UpstreamHeaderAccount {
  id?: string
  apiKey: string
  passthroughEnabled: boolean
  type?: string
  credentials?: Record<string, unknown>
}

export class UpstreamRequestTimeoutError extends Error {}
export class UpstreamRequestAbortedError extends Error {
  constructor(message: string, readonly upstreamRequestStarted = false) {
    super(message)
  }
}

const gatewayUpstreamAgentOptions: http.AgentOptions = {
  keepAlive: true,
  maxSockets: Infinity
}
let directHttpAgent: http.Agent | undefined
let directHttpsAgent: https.Agent | undefined
const proxyAgents = new Map<string, http.Agent>()

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

}

export function requestUpstream(upstreamUrl: string, options: UpstreamRequestOptions): Promise<GatewayUpstreamResponse> {
  return new Promise((resolve, reject) => {
    let settled = false
    const settleResolve = (response: GatewayUpstreamResponse) => {
      if (settled) return
      settled = true
      resolve(response)
    }
    const settleReject = (error: unknown) => {
      if (settled) return
      settled = true
      reject(error)
    }
    if (options.signal?.aborted) {
      settleReject(new UpstreamRequestAbortedError('请求已取消'))
      return
    }
    const url = new URL(upstreamUrl)
    const transport = url.protocol === 'http:' ? http : https
    let agent: http.Agent | undefined
    try {
      agent = gatewayUpstreamAgent(url, options.proxyUrl)
    } catch (error) {
      settleReject(error)
      return
    }
    const requestOptions: http.RequestOptions = {
      method: options.method,
      headers: headersToNodeHeaders(options.headers),
      agent
    }
    let requestTimeout: NodeJS.Timeout | undefined
    let upstreamRequestStarted = false
    const clearRequestTimeout = () => {
      if (requestTimeout) {
        clearTimeout(requestTimeout)
        requestTimeout = undefined
      }
    }
    const request = transport.request(url, requestOptions, (message) => {
      clearRequestTimeout()
      bindAbortSignalToIncomingMessage(message, options.signal)
      settleResolve(new NodeGatewayUpstreamResponse(message))
    })
    const abort = () => request.destroy(new Error('上游请求超时'))
    const abortBySignal = () => request.destroy(new UpstreamRequestAbortedError('请求已取消', upstreamRequestStarted))

    if (options.requestTimeoutMs !== undefined) {
      const seconds = Math.ceil(options.requestTimeoutMs / 1000)
      requestTimeout = setTimeout(() => request.destroy(new UpstreamRequestTimeoutError(`上游流式请求 ${seconds}s 后仍未返回首个响应`)), options.requestTimeoutMs)
    }
    request.setTimeout(options.timeoutMs ?? 120000, abort)
    options.signal?.addEventListener('abort', abortBySignal, { once: true })
    const cleanupAbortSignal = () => options.signal?.removeEventListener('abort', abortBySignal)
    request.on('error', (error) => {
      clearRequestTimeout()
      cleanupAbortSignal()
      settleReject(error)
    })
    request.on('response', cleanupAbortSignal)
    request.on('close', () => {
      clearRequestTimeout()
      cleanupAbortSignal()
    })
    if (options.body) {
      request.write(options.body)
    }
    upstreamRequestStarted = true
    request.end()
  })
}

export function closeGatewayUpstreamAgentsForTest(): void {
  directHttpAgent?.destroy()
  directHttpsAgent?.destroy()
  directHttpAgent = undefined
  directHttpsAgent = undefined
  for (const agent of proxyAgents.values()) {
    agent.destroy()
  }
  proxyAgents.clear()
}

function gatewayUpstreamAgent(url: URL, proxyUrl?: string): http.Agent {
  if (proxyUrl) {
    return cachedProxyAgent(proxyUrl)
  }
  if (url.protocol === 'http:') {
    directHttpAgent = directHttpAgent ?? new http.Agent(gatewayUpstreamAgentOptions)
    return directHttpAgent
  }
  directHttpsAgent = directHttpsAgent ?? new https.Agent(gatewayUpstreamAgentOptions)
  return directHttpsAgent
}

function cachedProxyAgent(proxyUrl: string): http.Agent {
  const cached = proxyAgents.get(proxyUrl)
  if (cached) {
    return cached
  }
  const agent = createProxyAgent(proxyUrl, gatewayUpstreamAgentOptions) as http.Agent
  proxyAgents.set(proxyUrl, agent)
  return agent
}

export function isUpstreamRequestAbortedError(error: unknown): boolean {
  return error instanceof UpstreamRequestAbortedError
    || (error instanceof Error && error.message === '请求已取消')
}

export async function readStreamChunkWithIdleTimeout(
  iterator: AsyncIterator<Uint8Array>,
  timeoutSeconds: number,
  signal?: AbortSignal
): Promise<IteratorResult<Uint8Array>> {
  return readStreamChunkWithTimeout(
    iterator,
    timeoutSeconds,
    () => new Error(`上游流 ${timeoutSeconds}s 无数据，已超时`),
    signal
  )
}

export async function readStreamChunkWithAbort(
  iterator: AsyncIterator<Uint8Array>,
  signal?: AbortSignal
): Promise<IteratorResult<Uint8Array>> {
  return readStreamChunkWithTimeout(iterator, undefined, () => new Error(''), signal)
}

export function upstreamSocketTimeoutMs(req: Request, settings: GatewaySettings, account?: { type?: string }): number {
  const isStreamRequest = isEffectiveOpenAIStreamRequest(req, account)
  if (!isStreamRequest || !settings.streamCircuitBreakerEnabled) {
    return 120000
  }
  return Math.max(settings.streamRequestTimeoutSeconds, settings.streamIdleTimeoutSeconds + 15, 30) * 1000
}

export function upstreamRequestTimeoutMs(req: Request, settings: GatewaySettings, account?: { type?: string }): number | undefined {
  if (!isEffectiveOpenAIStreamRequest(req, account) || !settings.streamCircuitBreakerEnabled) {
    return undefined
  }
  return Math.max(1, settings.streamRequestTimeoutSeconds) * 1000
}

export function isEffectiveOpenAIStreamRequest(req: Request, account?: { type?: string }): boolean {
  if (account?.type === 'oauth') {
    return !isOpenAIOAuthCodexCompactRequest(req)
  }
  return requestStream(req)
}

export function buildUpstreamRequestBody(req: Request, passthroughEnabled: boolean): Buffer | string | undefined {
  if (req.method === 'GET' || req.method === 'HEAD') {
    return undefined
  }
  const bodyCacheKey = passthroughEnabled ? 'passthrough' : 'normalized'
  const requestWithBodyCache = req as GatewayRawBodyRequest
  const cached = requestWithBodyCache.gatewayUpstreamBodyCache?.[bodyCacheKey]
  if (cached) {
    return cached.body
  }
  const rawBody = (req as GatewayRawBodyRequest).rawBody
  let body: Buffer | string | undefined
  if (!passthroughEnabled) {
    const bodyState = getGatewayRequestBodyState(req)
    if (bodyState?.jsonParseStatus === 'deferred_large_json' && rawBody && rawBody.length > 0) {
      body = rawBody
    } else {
      body = JSON.stringify(req.body ?? {})
    }
  } else if (rawBody && rawBody.length > 0) {
    body = rawBody
  } else if (req.body === undefined || isEmptyPlainObject(req.body)) {
    body = undefined
  } else {
    body = JSON.stringify(req.body)
  }
  requestWithBodyCache.gatewayUpstreamBodyCache = requestWithBodyCache.gatewayUpstreamBodyCache ?? {}
  requestWithBodyCache.gatewayUpstreamBodyCache[bodyCacheKey] = { body }
  return body
}

export async function buildUpstreamRequestParts(
  req: Request,
  account: UpstreamHeaderAccount,
  identity: OpenAIOAuthCodexIdentity,
  signal?: AbortSignal
): Promise<{ headers: Headers; body?: Buffer | string }> {
  if (account.type === 'oauth') {
    return await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity, signal)
  }
  return {
    headers: buildUpstreamHeaders(req.headers, account),
    body: buildUpstreamRequestBody(req, account.passthroughEnabled)
  }
}

export function buildUpstreamHeaders(inputHeaders: Record<string, string | string[] | undefined>, account: UpstreamHeaderAccount): Headers {
  const headers = new Headers()
  for (const [name, value] of Object.entries(inputHeaders)) {
    const lowerName = name.toLowerCase()
    if (shouldSkipUpstreamRequestHeader(lowerName)) {
      continue
    }
    if (Array.isArray(value)) {
      headers.set(name, value.join(', '))
    } else if (typeof value === 'string') {
      headers.set(name, value)
    }
  }
  headers.set('authorization', `Bearer ${account.apiKey}`)
  if (account.type === 'oauth') {
    applyOpenAICodexHeaders(headers, account)
  }
  if (!account.passthroughEnabled) {
    headers.set('content-type', headers.get('content-type') ?? 'application/json')
  }
  return headers
}

export function copyResponseHeaders(upstreamResponse: GatewayUpstreamResponse, res: { setHeader: (name: string, value: string) => void }): void {
  const dynamicSkippedHeaders = parseConnectionHeaderTokens(upstreamResponse.headers.get('connection'))
  upstreamResponse.headers.forEach((value, name) => {
    if (shouldSkipUpstreamResponseHeader(name, dynamicSkippedHeaders)) {
      return
    }
    res.setHeader(name, value)
  })
}

function shouldSkipUpstreamResponseHeader(name: string, dynamicSkippedHeaders?: Set<string>): boolean {
  const lowerName = name.toLowerCase()
  if (skippedUpstreamResponseHeaders.has(lowerName)) {
    return true
  }
  if (dynamicSkippedHeaders?.has(lowerName)) {
    return true
  }
  return skippedUpstreamResponseHeaderPrefixes.some((prefix) => lowerName.startsWith(prefix))
}

function parseConnectionHeaderTokens(value: string | null): Set<string> | undefined {
  if (!value) {
    return undefined
  }
  const tokens = value
    .split(',')
    .map((token) => token.trim().toLowerCase())
    .filter(Boolean)
  return tokens.length > 0 ? new Set(tokens) : undefined
}

function shouldSkipUpstreamRequestHeader(name: string): boolean {
  const lowerName = name.toLowerCase()
  if (skippedUpstreamRequestHeaders.has(lowerName)) {
    return true
  }
  return skippedUpstreamRequestHeaderPrefixes.some((prefix) => lowerName.startsWith(prefix))
}

function applyOpenAICodexHeaders(headers: Headers, account: UpstreamHeaderAccount): void {
  if (!headers.get('accept')) {
    headers.set('accept', 'text/event-stream')
  }
  if (!headers.get('content-type')) {
    headers.set('content-type', 'application/json')
  }
  if (!headers.get('user-agent')) {
    headers.set('user-agent', openAICodexUserAgent)
  }
  if (!headers.get('originator')) {
    headers.set('originator', 'codex_cli_rs')
  }
  if (!headers.get('version')) {
    headers.set('version', openAICodexVersion)
  }
  if (!headers.get('openai-beta')) {
    headers.set('openai-beta', 'responses=experimental')
  }
  const chatGPTAccountId = stringCredential(account.credentials, 'chatgpt_account_id') ?? stringCredential(account.credentials, 'account_id')
  if (chatGPTAccountId && !headers.get('chatgpt-account-id')) {
    headers.set('chatgpt-account-id', chatGPTAccountId)
  }
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

export async function readStreamChunkWithTimeout(
  iterator: AsyncIterator<Uint8Array>,
  timeoutSeconds: number | undefined,
  createError: () => Error,
  signal?: AbortSignal
): Promise<IteratorResult<Uint8Array>> {
  let timeout: NodeJS.Timeout | undefined
  let abortListener: (() => void) | undefined
  try {
    const races: Array<Promise<IteratorResult<Uint8Array>>> = [iterator.next()]
    if (timeoutSeconds !== undefined) {
      races.push(new Promise<IteratorResult<Uint8Array>>((_, reject) => {
        timeout = setTimeout(() => reject(createError()), timeoutSeconds * 1000)
      }))
    }
    if (signal) {
      if (signal.aborted) {
        throw new UpstreamRequestAbortedError('请求已取消', true)
      }
      races.push(new Promise<IteratorResult<Uint8Array>>((_, reject) => {
        abortListener = () => reject(new UpstreamRequestAbortedError('请求已取消', true))
        signal.addEventListener('abort', abortListener, { once: true })
      }))
    }
    return await Promise.race(races)
  } finally {
    if (timeout) {
      clearTimeout(timeout)
    }
    if (signal && abortListener) {
      signal.removeEventListener('abort', abortListener)
    }
  }
}

function bindAbortSignalToIncomingMessage(message: IncomingMessage, signal?: AbortSignal): void {
  if (!signal) return
  const abort = () => message.destroy(new UpstreamRequestAbortedError('请求已取消', true))
  if (signal.aborted) {
    abort()
    return
  }
  signal.addEventListener('abort', abort, { once: true })
  message.once('close', () => signal.removeEventListener('abort', abort))
}

function isEmptyPlainObject(value: unknown): boolean {
  return typeof value === 'object' && value !== null && !Array.isArray(value) && Object.keys(value).length === 0
}

function stringCredential(credentials: Record<string, unknown> | undefined, key: string): string | undefined {
  const value = credentials?.[key]
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
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
  'accept-encoding',
  'cookie',
  'set-cookie',
  'x-api-key',
  'x-goog-api-key',
  'api-key',
  'chatgpt-account-id',
  'openai-organization',
  'openai-project',
  'x-request-id',
  'traceparent',
  'tracestate',
  'baggage',
  'x-amzn-trace-id',
  'x-cloud-trace-context',
  'x-forwarded-for',
  'x-forwarded-host',
  'x-forwarded-port',
  'x-forwarded-proto',
  'x-forwarded-server',
  'x-real-ip',
  'forwarded',
  'via',
  'cf-connecting-ip'
])

const skippedUpstreamRequestHeaderPrefixes = [
  'x-forwarded-',
  'x-openai-',
  'x-stainless-',
  'x-vercel-'
]

const openAICodexVersion = '0.125.0'
const openAICodexUserAgent = `codex_cli_rs/${openAICodexVersion}`

const skippedUpstreamResponseHeaders = new Set([
  'connection',
  'content-encoding',
  'content-length',
  'keep-alive',
  'proxy-authenticate',
  'proxy-authorization',
  'set-cookie',
  'te',
  'trailer',
  'transfer-encoding',
  'upgrade'
])

const skippedUpstreamResponseHeaderPrefixes = [
  'cf-aig-',
  'helicone-',
  'x-bt-',
  'x-kong-',
  'x-litellm-',
  'x-portkey-'
]
