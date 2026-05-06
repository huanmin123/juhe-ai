import * as http from 'node:http'
import * as https from 'node:https'
import type { IncomingHttpHeaders, IncomingMessage } from 'node:http'
import type { Request } from 'express'

import { createProxyAgent } from '../openai-oauth/openai-oauth.service.js'
import type { GatewaySettings } from './account-error-policy.service.js'
import {
  inspectOpenAIStreamText,
  isOpenAIStreamServerOverloadedErrorCode
} from './openai-gateway-usage.js'

export interface GatewayUpstreamResponse {
  readonly status: number
  readonly ok: boolean
  readonly headers: Headers
  readonly body: AsyncIterable<Uint8Array> | null
  arrayBuffer(): Promise<ArrayBuffer>
}

interface UpstreamRequestOptions {
  method: string
  headers: Headers
  body?: Buffer | string
  proxyUrl?: string
  timeoutMs?: number
  requestTimeoutMs?: number
}

interface UpstreamHeaderAccount {
  apiKey: string
  passthroughEnabled: boolean
  type?: string
  credentials?: Record<string, unknown>
}

type RawBodyRequest = Request & { rawBody?: Buffer }

export class UpstreamRequestTimeoutError extends Error {}
export class UpstreamStreamPreloadFailedError extends Error {
  constructor(
    message: string,
    readonly chunks: Buffer[],
    readonly errorCode?: string
  ) {
    super(message)
  }
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
    private readonly preloadedChunks: Buffer[]
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
    return iteratePreloadedStream(this.iterator, this.preloadedChunks)
  }

  async arrayBuffer(): Promise<ArrayBuffer> {
    const body = this.body
    return body ? collectAsyncIterableBody(body) : new ArrayBuffer(0)
  }
}

export function requestUpstream(upstreamUrl: string, options: UpstreamRequestOptions): Promise<GatewayUpstreamResponse> {
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

export async function preloadStreamResponseFirstChunk(
  response: GatewayUpstreamResponse,
  settings: GatewaySettings
): Promise<GatewayUpstreamResponse> {
  if (!response.body) {
    throw new Error('Upstream stream response has no body')
  }
  const iterator = response.body[Symbol.asyncIterator]()
  const chunks: Buffer[] = []
  try {
    while (true) {
      const result = await readStreamChunkWithTimeout(
        iterator,
        settings.streamRequestTimeoutSeconds,
        () => new UpstreamRequestTimeoutError(`Upstream stream request timeout after ${settings.streamRequestTimeoutSeconds}s`)
      )
      if (result.done) {
        throw new UpstreamStreamPreloadFailedError('Upstream stream ended before first output or terminal event', chunks)
      }

      const buffer = Buffer.from(result.value)
      chunks.push(buffer)

      const inspection = inspectOpenAIStreamText(Buffer.concat(chunks).toString('utf8'))
      if (inspection.outputReceived) {
        return new PreloadedGatewayUpstreamResponse(response, iterator, chunks)
      }
      if (inspection.failedReceived) {
        if (!isOpenAIStreamServerOverloadedErrorCode(inspection.errorCode)) {
          return new PreloadedGatewayUpstreamResponse(response, iterator, chunks)
        }
        const message = inspection.errorMessage ?? 'Upstream stream failed before first output'
        throw new UpstreamStreamPreloadFailedError(message, chunks, inspection.errorCode)
      }
      if (inspection.terminalReceived) {
        return new PreloadedGatewayUpstreamResponse(response, iterator, chunks)
      }
    }
  } catch (error) {
    await closeAsyncIterator(iterator)
    throw error
  }
}

export function isRetryableUpstreamStreamPreloadError(error: unknown): boolean {
  return error instanceof UpstreamRequestTimeoutError
    || (error instanceof UpstreamStreamPreloadFailedError && isOpenAIStreamServerOverloadedErrorCode(error.errorCode))
}

export async function readStreamChunkWithIdleTimeout(
  iterator: AsyncIterator<Uint8Array>,
  timeoutSeconds: number
): Promise<IteratorResult<Uint8Array>> {
  return readStreamChunkWithTimeout(
    iterator,
    timeoutSeconds,
    () => new Error(`Upstream stream idle timeout after ${timeoutSeconds}s`)
  )
}

export function upstreamSocketTimeoutMs(req: Request, settings: GatewaySettings): number {
  const isStreamRequest = req.body?.stream === true
  if (!isStreamRequest || !settings.streamCircuitBreakerEnabled) {
    return 120000
  }
  return Math.max(settings.streamRequestTimeoutSeconds, settings.streamIdleTimeoutSeconds + 15, 30) * 1000
}

export function upstreamRequestTimeoutMs(req: Request, settings: GatewaySettings): number | undefined {
  if (req.body?.stream !== true || !settings.streamCircuitBreakerEnabled) {
    return undefined
  }
  return Math.max(1, settings.streamRequestTimeoutSeconds) * 1000
}

export function isStreamResponse(response: GatewayUpstreamResponse): boolean {
  const contentType = response.headers.get('content-type') ?? ''
  return contentType.includes('text/event-stream') || contentType.includes('application/octet-stream')
}

export function buildUpstreamRequestBody(req: Request, passthroughEnabled: boolean): Buffer | string | undefined {
  if (req.method === 'GET' || req.method === 'HEAD') {
    return undefined
  }
  if (!passthroughEnabled) {
    return JSON.stringify(req.body ?? {})
  }
  const rawBody = (req as RawBodyRequest).rawBody
  if (rawBody && rawBody.length > 0) {
    return rawBody
  }
  if (req.body === undefined || isEmptyPlainObject(req.body)) {
    return undefined
  }
  return JSON.stringify(req.body)
}

export function buildUpstreamHeaders(inputHeaders: Record<string, string | string[] | undefined>, account: UpstreamHeaderAccount): Headers {
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

async function* iteratePreloadedStream(iterator: AsyncIterator<Uint8Array>, preloadedChunks: Buffer[]): AsyncIterable<Uint8Array> {
  for (const chunk of preloadedChunks) {
    yield chunk
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
