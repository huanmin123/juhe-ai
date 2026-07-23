import * as http from 'node:http'
import * as https from 'node:https'
import type { IncomingHttpHeaders, IncomingMessage } from 'node:http'
import type { Readable } from 'node:stream'
import {
  createBrotliDecompress,
  createGunzip,
  createInflate,
  type BrotliDecompress,
  type Gunzip,
  type Inflate
} from 'node:zlib'
import type { Request } from 'express'

import { createProxyAgent } from '../../openai-oauth/openai-oauth.service.js'
import { prepareSafeUpstreamRequestUrl } from '../../../shared/upstream-url-policy.js'
import { createProcessLocalResourceCache } from '../../../shared/cache.js'
import type { GatewayTimeoutProfile } from '../policy/timeout-profile.js'
import {
  isOpenAIOAuthCodexCompactRequest
} from '../adapters/gpt-codex/oauth-adapter.js'
import { type GatewayRawBodyRequest } from '../request/body.js'
import { requestStream } from '../request/metadata.js'
import {
  gatewayClientProfileHeader
} from '../client-profiles/strategy.js'
import {
  isOpenAICodexClientHeaders,
  normalizeOpenAICodexClientHeaders,
  openAICodexResponsesLiteHeader
} from '../adapters/gpt-codex/client-headers.js'
import { isOpenAIProtocolProfile } from '../../../domain/provider-protocol.js'
import { runtimeConfig } from '../../../config/runtime.js'
import type { FirstByteDeadlineHandler } from './first-byte-deadline.js'
import { GatewayFirstByteTimeoutError } from './first-byte-timeout.js'

export interface GatewayUpstreamResponse {
  readonly status: number
  readonly ok: boolean
  readonly headers: Headers
  readonly body: AsyncIterable<Uint8Array> | null
  /** Internal-only provenance set by an explicit gateway transformer. */
  readonly codexResponsesGuardMarker?: import('../codex-responses/response-guard.js').CodexResponsesGuardMarker
}

interface UpstreamRequestOptions {
  method: string
  headers: Headers
  body?: Buffer | string
  proxyUrl?: string
  timeoutMs?: number
  requestTimeoutMs?: number
  firstByteDeadlineMs?: number
  firstByteDeadlineTransport?: 'stream' | 'non_stream'
  onFirstByteDeadline?: FirstByteDeadlineHandler
  signal?: AbortSignal
  transport?: 'node' | 'fetch'
}

interface UpstreamHeaderAccount {
  id?: string
  apiKey: string
  type?: string
  providerCode?: string
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
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
  maxSockets: runtimeConfig.gateway.upstreamAgentMaxSockets,
  maxFreeSockets: runtimeConfig.gateway.upstreamAgentMaxFreeSockets,
  maxTotalSockets: runtimeConfig.gateway.upstreamAgentMaxTotalSockets
}
const gatewayProxyAgentCacheMaxEntries = 256
const gatewayProxyAgentCacheTtlMs = 30 * 60 * 1000
let directHttpAgent: http.Agent | undefined
let directHttpsAgent: https.Agent | undefined
const proxyAgents = createProcessLocalResourceCache<string, http.Agent>({
  name: 'gateway:proxy-agents',
  max: gatewayProxyAgentCacheMaxEntries,
  ttlMs: gatewayProxyAgentCacheTtlMs,
  updateAgeOnGet: true,
  dispose: (agent) => {
    agent.destroy()
  }
})

class NodeGatewayUpstreamResponse implements GatewayUpstreamResponse {
  private decodedBody: AsyncIterable<Uint8Array> | null | undefined

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
    if (this.decodedBody !== undefined) {
      return this.decodedBody
    }
    this.decodedBody = decodeUpstreamResponseBody(this.message)
    return this.decodedBody
  }

}

export async function requestUpstream(upstreamUrl: string, options: UpstreamRequestOptions): Promise<GatewayUpstreamResponse> {
  const safeRequest = await prepareSafeUpstreamRequestUrl(upstreamUrl)
  if (options.transport === 'fetch') {
    return requestUpstreamWithFetch(safeRequest.url, options)
  }
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
    const url = safeRequest.url
    const transport = url.protocol === 'http:' ? http : https
    let agent: http.Agent | undefined
    try {
      agent = gatewayUpstreamAgent(url, options.proxyUrl)
    } catch (error) {
      settleReject(error)
      return
    }
    const headers = upstreamRequestHeaders(options.headers, options.body)
    const requestOptions: http.RequestOptions = {
      method: options.method,
      headers: headersToNodeHeaders(headers),
      agent,
      lookup: safeRequest.lookup
    }
    let requestTimeout: NodeJS.Timeout | undefined
    let firstByteDeadlineTimer: NodeJS.Timeout | undefined
    let upstreamRequestStarted = false
    const clearRequestTimeout = () => {
      if (requestTimeout) {
        clearTimeout(requestTimeout)
        requestTimeout = undefined
      }
      if (firstByteDeadlineTimer) {
        clearTimeout(firstByteDeadlineTimer)
        firstByteDeadlineTimer = undefined
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
      requestTimeout = setTimeout(() => request.destroy(new UpstreamRequestTimeoutError(`上游请求 ${seconds}s 后仍未返回首个响应`)), options.requestTimeoutMs)
    }
    if (options.firstByteDeadlineMs !== undefined) {
      const deadlineMs = options.firstByteDeadlineMs
      const deadlineStartedAtMs = Date.now()
      firstByteDeadlineTimer = setTimeout(() => {
        void Promise.resolve(options.onFirstByteDeadline?.({
          elapsedMs: Date.now() - deadlineStartedAtMs,
          timeoutMs: deadlineMs,
          transport: options.firstByteDeadlineTransport ?? 'non_stream'
        }) ?? 'abort').then((action) => {
          if (action === 'abort') {
            request.destroy(new GatewayFirstByteTimeoutError(
              `上游请求 ${Math.ceil(deadlineMs / 1000)}s 后仍未返回首个响应`,
              deadlineMs,
              'configured_deadline'
            ))
          }
        }).catch((error: unknown) => request.destroy(error instanceof Error ? error : new Error(String(error))))
      }, deadlineMs)
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

export function closeGatewayUpstreamAgents(): void {
  directHttpAgent?.destroy()
  directHttpsAgent?.destroy()
  directHttpAgent = undefined
  directHttpsAgent = undefined
  for (const agent of proxyAgents.values()) {
    agent.destroy()
  }
  proxyAgents.clear()
}

export function closeGatewayUpstreamAgentsForTest(): void {
  closeGatewayUpstreamAgents()
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
  proxyAgents.set(proxyUrl, agent, { ttlMs: gatewayProxyAgentCacheTtlMs })
  return agent
}

async function requestUpstreamWithFetch(url: URL, options: UpstreamRequestOptions): Promise<GatewayUpstreamResponse> {
  const headers = upstreamRequestHeaders(options.headers, options.body)
  const controller = new AbortController()
  let requestTimeout: NodeJS.Timeout | undefined
  let firstByteDeadlineTimer: NodeJS.Timeout | undefined
  let socketTimeout: NodeJS.Timeout | undefined
  const abortBySignal = () => controller.abort(new UpstreamRequestAbortedError('请求已取消', true))
  if (options.signal?.aborted) {
    throw new UpstreamRequestAbortedError('请求已取消')
  }
  options.signal?.addEventListener('abort', abortBySignal, { once: true })
  try {
    if (options.requestTimeoutMs !== undefined) {
      const seconds = Math.ceil(options.requestTimeoutMs / 1000)
      requestTimeout = setTimeout(() => {
        controller.abort(new UpstreamRequestTimeoutError(`上游请求 ${seconds}s 后仍未返回首个响应`))
      }, options.requestTimeoutMs)
    }
    if (options.firstByteDeadlineMs !== undefined) {
      const deadlineMs = options.firstByteDeadlineMs
      const deadlineStartedAtMs = Date.now()
      firstByteDeadlineTimer = setTimeout(() => {
        void Promise.resolve(options.onFirstByteDeadline?.({
          elapsedMs: Date.now() - deadlineStartedAtMs,
          timeoutMs: deadlineMs,
          transport: options.firstByteDeadlineTransport ?? 'non_stream'
        }) ?? 'abort').then((action) => {
          if (action === 'abort') {
            controller.abort(new GatewayFirstByteTimeoutError(
              `上游请求 ${Math.ceil(deadlineMs / 1000)}s 后仍未返回首个响应`,
              deadlineMs,
              'configured_deadline'
            ))
          }
        }).catch((error: unknown) => controller.abort(error))
      }, deadlineMs)
    }
    if (options.timeoutMs !== undefined) {
      socketTimeout = setTimeout(() => controller.abort(new Error('上游请求超时')), options.timeoutMs)
    }
    const fetchBody = typeof options.body === 'string' || options.body === undefined
      ? options.body
      : bufferToArrayBuffer(options.body)
    const response = await fetch(url, {
      method: options.method,
      headers,
      body: fetchBody,
      signal: controller.signal,
      redirect: 'manual'
    })
    if (requestTimeout) {
      clearTimeout(requestTimeout)
      requestTimeout = undefined
    }
    return new FetchGatewayUpstreamResponse(response, {
      timeoutMs: options.timeoutMs,
      signal: options.signal
    })
  } catch (error) {
    const reason = controller.signal.reason
    if (reason instanceof Error) {
      throw reason
    }
    throw error
  } finally {
    if (requestTimeout) clearTimeout(requestTimeout)
    if (firstByteDeadlineTimer) clearTimeout(firstByteDeadlineTimer)
    if (socketTimeout) clearTimeout(socketTimeout)
    options.signal?.removeEventListener('abort', abortBySignal)
  }
}

function bufferToArrayBuffer(buffer: Buffer): ArrayBuffer {
  const copy = new Uint8Array(buffer.byteLength)
  copy.set(buffer)
  return copy.buffer
}

class FetchGatewayUpstreamResponse implements GatewayUpstreamResponse {
  private decodedBody: AsyncIterable<Uint8Array> | null | undefined

  constructor(
    private readonly response: Response,
    private readonly options: { timeoutMs?: number; signal?: AbortSignal }
  ) {}

  get status(): number {
    return this.response.status
  }

  get ok(): boolean {
    return this.response.ok
  }

  get headers(): Headers {
    return this.response.headers
  }

  get body(): AsyncIterable<Uint8Array> | null {
    if (this.decodedBody !== undefined) {
      return this.decodedBody
    }
    this.decodedBody = this.response.body
      ? fetchReadableStreamBody(this.response.body, this.options)
      : null
    return this.decodedBody
  }
}

function fetchReadableStreamBody(
  body: ReadableStream<Uint8Array>,
  options: { timeoutMs?: number; signal?: AbortSignal }
): AsyncIterable<Uint8Array> {
  const reader = body.getReader()
  let released = false
  let closed = false
  const release = () => {
    if (released) return
    released = true
    try {
      reader.releaseLock()
    } catch {
    }
  }
  const cancel = async (reason?: unknown) => {
    if (closed) return
    closed = true
    try {
      await reader.cancel(reason)
    } catch {
    } finally {
      release()
    }
  }
  return {
    [Symbol.asyncIterator]() {
      return {
        async next(): Promise<IteratorResult<Uint8Array>> {
          if (closed) {
            return { done: true, value: undefined }
          }
          try {
            const result = await readFetchBodyChunk(reader, options)
            if (result.done) {
              closed = true
              release()
              return { done: true, value: undefined }
            }
            return { done: false, value: result.value ?? new Uint8Array(0) }
          } catch (error) {
            await cancel(error)
            throw error
          }
        },
        async return(): Promise<IteratorResult<Uint8Array>> {
          await cancel()
          return { done: true, value: undefined }
        }
      }
    }
  }
}

async function readFetchBodyChunk(
  reader: ReadableStreamDefaultReader<Uint8Array>,
  options: { timeoutMs?: number; signal?: AbortSignal }
): Promise<ReadableStreamReadResult<Uint8Array>> {
  let timeout: NodeJS.Timeout | undefined
  let abortListener: (() => void) | undefined
  try {
    const races: Array<Promise<ReadableStreamReadResult<Uint8Array>>> = [reader.read()]
    if (options.timeoutMs !== undefined) {
      races.push(new Promise<ReadableStreamReadResult<Uint8Array>>((_, reject) => {
        timeout = setTimeout(() => reject(new Error('上游请求超时')), options.timeoutMs)
      }))
    }
    if (options.signal) {
      if (options.signal.aborted) {
        throw new UpstreamRequestAbortedError('请求已取消', true)
      }
      races.push(new Promise<ReadableStreamReadResult<Uint8Array>>((_, reject) => {
        abortListener = () => reject(new UpstreamRequestAbortedError('请求已取消', true))
        options.signal!.addEventListener('abort', abortListener, { once: true })
      }))
    }
    return await Promise.race(races)
  } finally {
    if (timeout) clearTimeout(timeout)
    if (options.signal && abortListener) {
      options.signal.removeEventListener('abort', abortListener)
    }
  }
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

export function upstreamSocketTimeoutMs(req: Request, profile: GatewayTimeoutProfile, account?: { type?: string }): number {
  const isStreamRequest = isEffectiveOpenAIStreamRequest(req, account)
  if (!isStreamRequest) {
    return Math.max(profile.firstResponseTimeoutMs, 30_000)
  }
  return Math.max(profile.firstResponseTimeoutMs, profile.idleTimeoutMs + 15_000, 30_000)
}

export function upstreamRequestTimeoutMs(profile: GatewayTimeoutProfile): number {
  return profile.firstResponseTimeoutMs
}

export function isEffectiveOpenAIStreamRequest(req: Request, account?: { type?: string }): boolean {
  if (usesOpenAIOAuthCompactStreamRules(account)) {
    return !isOpenAIOAuthCodexCompactRequest(req)
  }
  return requestStream(req)
}

export function buildUpstreamRequestBody(req: Request): Buffer | undefined {
  if (req.method === 'GET' || req.method === 'HEAD') {
    return undefined
  }
  const requestWithBodyCache = req as GatewayRawBodyRequest
  const cached = requestWithBodyCache.gatewayUpstreamBodyCache?.passthrough
  if (cached) {
    return cached.body
  }
  const rawBody = (req as GatewayRawBodyRequest).rawBody
  const body = rawBody && rawBody.length > 0 ? rawBody : undefined
  requestWithBodyCache.gatewayUpstreamBodyCache = requestWithBodyCache.gatewayUpstreamBodyCache ?? {}
  requestWithBodyCache.gatewayUpstreamBodyCache.passthrough = { body }
  return body
}

export function buildUpstreamHeaders(inputHeaders: Record<string, string | string[] | undefined>, account: UpstreamHeaderAccount): Headers {
  const headers = copySafeUpstreamRequestHeaders(inputHeaders, {
    preserveCodexClientHeaders: isOpenAICodexClientHeaders(inputHeaders)
  })
  headers.set('authorization', `Bearer ${account.apiKey}`)
  if (usesOpenAIOAuthCompactStreamRules(account)) {
    applyOpenAICodexHeaders(headers, account)
  }
  return headers
}

function usesOpenAIOAuthCompactStreamRules(account?: {
  type?: string
  providerCode?: string
  protocolCode?: string
  protocolVersion?: string
  providerProtocolProfileId?: string
}): boolean {
  if (account?.type !== 'oauth') {
    return false
  }
  if (!account.protocolCode && !account.protocolVersion && !account.providerProtocolProfileId) {
    return true
  }
  return isOpenAIProtocolProfile(account)
}

export function copySafeUpstreamRequestHeaders(
  inputHeaders: Record<string, string | string[] | undefined>,
  options: { preserveCodexClientHeaders?: boolean } = {}
): Headers {
  const headers = new Headers()
  for (const [name, value] of Object.entries(inputHeaders)) {
    const lowerName = name.toLowerCase()
    if (shouldSkipUpstreamRequestHeader(lowerName, options.preserveCodexClientHeaders)) {
      continue
    }
    if (Array.isArray(value)) {
      headers.set(name, value.join(', '))
    } else if (typeof value === 'string') {
      headers.set(name, value)
    }
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

function shouldSkipUpstreamRequestHeader(name: string, preserveCodexClientHeaders = false): boolean {
  const lowerName = name.toLowerCase()
  if (preserveCodexClientHeaders && preservedCodexClientHeaders.has(lowerName)) {
    return false
  }
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
  normalizeOpenAICodexClientHeaders(headers)
  const accountId = stringCredential(account.credentials, 'account_id')
  if (accountId && !headers.get('chatgpt-account-id')) {
    headers.set('chatgpt-account-id', accountId)
  }
}

function headersToNodeHeaders(headers: Headers): http.OutgoingHttpHeaders {
  const output: http.OutgoingHttpHeaders = {}
  headers.forEach((value, name) => {
    output[name] = value
  })
  return output
}

function upstreamRequestHeaders(headers: Headers, body?: Buffer | string): Headers {
  const output = new Headers(headers)
  if (body !== undefined && !output.has('content-length')) {
    output.set('content-length', String(upstreamRequestBodyByteLength(body)))
  }
  return output
}

function upstreamRequestBodyByteLength(body: Buffer | string): number {
  return typeof body === 'string' ? Buffer.byteLength(body) : body.length
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

function decodeUpstreamResponseBody(message: IncomingMessage): AsyncIterable<Uint8Array> | null {
  const encodings = parseContentEncodings(message.headers['content-encoding'])
  if (encodings.length === 0 || encodings.every((encoding) => encoding === 'identity')) {
    return message as AsyncIterable<Uint8Array>
  }

  let stream = message as Readable
  for (const encoding of [...encodings].reverse()) {
    if (encoding === 'identity') {
      continue
    }
    const decoder = createUpstreamResponseDecoder(encoding)
    if (!decoder) {
      return unsupportedUpstreamResponseEncoding(message, encoding)
    }
    stream = stream.pipe(decoder)
  }
  return stream as AsyncIterable<Uint8Array>
}

function parseContentEncodings(value: string | string[] | undefined): string[] {
  const joined = Array.isArray(value) ? value.join(',') : value
  if (!joined) {
    return []
  }
  return joined
    .split(',')
    .map((item) => item.trim().toLowerCase())
    .filter(Boolean)
}

function createUpstreamResponseDecoder(encoding: string): BrotliDecompress | Gunzip | Inflate | undefined {
  switch (encoding) {
    case 'br':
      return createBrotliDecompress()
    case 'gzip':
    case 'x-gzip':
      return createGunzip()
    case 'deflate':
    case 'x-deflate':
      return createInflate()
    default:
      return undefined
  }
}

async function* unsupportedUpstreamResponseEncoding(
  message: IncomingMessage,
  encoding: string
): AsyncIterable<Uint8Array> {
  const error = new Error(`不支持的上游响应压缩编码: ${encoding}`)
  message.destroy(error)
  throw error
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
  'openai-api-key',
  'x-api-key',
  'anthropic-api-key',
  'x-goog-api-key',
  'api-key',
  'chatgpt-account-id',
  'x-oai-attestation',
  'openai-organization',
  'openai-project',
  gatewayClientProfileHeader,
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

const preservedCodexClientHeaders = new Set([
  'x-oai-attestation',
  'x-openai-subagent',
  openAICodexResponsesLiteHeader
])

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
