import { EventEmitter } from 'node:events'
import type { IncomingHttpHeaders } from 'node:http'
import type { Request, Response } from 'express'

import type { AccountClientCompatibility } from '../../../domain/types.js'
import { BoundedBufferCollector } from '../../../shared/bounded-buffer.js'
import { OpenAIStreamInspector } from '../protocols/openai-v1/stream-inspection.js'

export const accountTestResponsePreviewBytes = 256 * 1024

export type MemoryGatewayRequestInput = {
  method: 'GET' | 'POST'
  path: string
  body?: Record<string, unknown>
  signal?: AbortSignal
}

type MemoryGatewayRequestAdapterInput = {
  method: string
  originalUrl: string
  path: string
  headers: IncomingHttpHeaders
  body?: Record<string, unknown>
  rawBody: Buffer
  ip: string
  signal?: AbortSignal
}

export function createGatewayTestRequest(
  path: string,
  body: Record<string, unknown>,
  rawBodyText: string,
  isOAuth: boolean,
  signal?: AbortSignal,
  clientCompatibility?: AccountClientCompatibility,
  extraHeaders?: IncomingHttpHeaders
): Request {
  const stream = body.stream === true || /:streamGenerateContent\b/i.test(path)
  const headers: IncomingHttpHeaders = {
    accept: stream ? isOAuth ? 'text/event-stream' : 'application/json, text/event-stream' : 'application/json',
    'content-type': 'application/json',
    'content-length': String(Buffer.byteLength(rawBodyText))
  }
  if (clientCompatibility === 'codex_responses' && stream && path.includes('/responses')) {
    const turnNonce = `${Date.now()}-${Math.random().toString(16).slice(2)}`
    headers['x-codex-turn-metadata'] = JSON.stringify({
      session_id: 'account-test-session',
      thread_id: 'account-test-thread',
      turn_id: `account-test-turn-${turnNonce}`
    })
  }
  for (const [name, value] of Object.entries(extraHeaders ?? {})) {
    if (value !== undefined) {
      headers[name.toLowerCase()] = value
    }
  }
  return createMemoryGatewayRequestFromAdapterInput({
    method: 'POST',
    originalUrl: path,
    path,
    headers,
    body,
    rawBody: Buffer.from(rawBodyText),
    ip: '127.0.0.1',
    signal
  })
}

export function createMemoryGatewayRequest(input: MemoryGatewayRequestInput): Request {
  const rawBody = input.body ? Buffer.from(JSON.stringify(input.body), 'utf8') : Buffer.alloc(0)
  const headers: IncomingHttpHeaders = {
    accept: input.body?.stream === true ? 'application/json, text/event-stream' : 'application/json'
  }
  if (input.body) {
    headers['content-type'] = 'application/json'
    headers['content-length'] = String(rawBody.length)
  }
  return createMemoryGatewayRequestFromAdapterInput({
    method: input.method,
    originalUrl: input.path,
    path: input.path.split('?')[0] || input.path,
    headers,
    body: input.body,
    rawBody,
    ip: '127.0.0.1',
    signal: input.signal
  })
}

function createMemoryGatewayRequestFromAdapterInput(input: MemoryGatewayRequestAdapterInput): Request {
  return new MemoryGatewayRequest(input).asRequest()
}

export class MemoryGatewayRequest extends EventEmitter {
  constructor(private readonly input: MemoryGatewayRequestAdapterInput) {
    super()
    if (this.input.signal?.aborted) {
      queueMicrotask(() => this.emit('aborted'))
    } else {
      this.input.signal?.addEventListener('abort', () => this.emit('aborted'), { once: true })
    }
  }

  get method(): string {
    return this.input.method
  }

  get originalUrl(): string {
    return this.input.originalUrl
  }

  get path(): string {
    return this.input.path
  }

  get headers(): IncomingHttpHeaders {
    return this.input.headers
  }

  get body(): Record<string, unknown> | undefined {
    return this.input.body
  }

  get rawBody(): Buffer {
    return this.input.rawBody
  }

  get ip(): string {
    return this.input.ip
  }

  get socket(): { remoteAddress: string } {
    return { remoteAddress: this.input.ip }
  }

  get aborted(): boolean {
    return this.input.signal?.aborted ?? false
  }

  header(name: string): string | undefined {
    const value = this.input.headers[name.toLowerCase()]
    if (Array.isArray(value)) return value.join(', ')
    return typeof value === 'string' ? value : undefined
  }

  asRequest(): Request {
    return this as unknown as Request
  }
}

export class MemoryGatewayResponse extends EventEmitter {
  statusCode = 200
  writableEnded = false
  destroyed = false
  locals: Record<string, unknown> = {}
  private readonly headers = new Map<string, string | string[]>()
  private readonly body = new BoundedBufferCollector(accountTestResponsePreviewBytes)
  private readonly streamInspector = new OpenAIStreamInspector()
  private firstOutputMs: number | undefined

  constructor(private readonly startedAt: number) {
    super()
  }

  status(code: number): this {
    this.statusCode = code
    return this
  }

  setHeader(name: string, value: number | string | readonly string[]): this {
    this.headers.set(name.toLowerCase(), Array.isArray(value) ? value.map(String) : String(value))
    return this
  }

  hasHeader(name: string): boolean {
    return this.headers.has(name.toLowerCase())
  }

  getHeaders(): Record<string, string | string[]> {
    return Object.fromEntries(this.headers.entries())
  }

  json(value: unknown): this {
    if (!this.hasHeader('content-type')) {
      this.setHeader('content-type', 'application/json; charset=utf-8')
    }
    return this.send(Buffer.from(JSON.stringify(value), 'utf8'))
  }

  send(value?: Buffer | string | object): this {
    if (Buffer.isBuffer(value)) {
      this.body.append(value)
    } else if (typeof value === 'string') {
      this.body.append(value)
    } else if (value !== undefined) {
      this.body.append(JSON.stringify(value))
    }
    return this.end()
  }

  write(value: Buffer | string | Uint8Array): boolean {
    const buffer = Buffer.isBuffer(value) ? value : Buffer.from(value)
    this.body.append(buffer)
    const inspection = this.streamInspector.pushChunk(buffer)
    if (this.firstOutputMs === undefined && inspection.outputReceived) {
      this.firstOutputMs = Date.now() - this.startedAt
    }
    return true
  }

  end(value?: Buffer | string | Uint8Array): this {
    if (value !== undefined) {
      this.write(value)
    }
    if (!this.writableEnded) {
      this.writableEnded = true
      this.emit('finish')
      this.emit('close')
    }
    return this
  }

  bodyText(): string {
    return this.body.text({ includeTruncationMarker: true })
  }

  bodyTruncated(): boolean {
    return this.body.truncated
  }

  headersObject(): Record<string, string | string[]> {
    const hiddenHeaders = new Set(['authorization', 'cookie', 'set-cookie', 'proxy-authorization'])
    const output: Record<string, string | string[]> = {}
    for (const [name, value] of this.headers) {
      output[name] = hiddenHeaders.has(name) ? '[redacted]' : value
    }
    return output
  }

  firstTokenMs(): number | undefined {
    return this.firstOutputMs
  }

  asResponse(): Response {
    return this as unknown as Response
  }
}
