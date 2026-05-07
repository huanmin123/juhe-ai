import { EventEmitter } from 'node:events'
import { randomUUID } from 'node:crypto'
import type { IncomingHttpHeaders } from 'node:http'
import type { Request, Response } from 'express'

import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'
import { withRequestContext, type RequestContext } from '../../shared/request-context.js'
import {
  findOpenAIAccountForGroup,
  resolveAccountSystemAccountId,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import { getRequestAuthContext, withRequestAuthContext } from '../auth/request-context.js'
import { handleOpenAIGatewayRequest } from '../gateway/openai-gateway.routes.js'
import { OpenAIStreamInspector } from '../gateway/openai-gateway-usage.js'

const defaultTestModel = 'gpt-5.5'
const defaultTestPrompt = 'hi'
const defaultOpenAITestInstructions = 'You are ChatGPT, a helpful assistant.'
const gatewayTestPath = '/v1/responses'
const gatewayModelsPath = '/v1/models'

export async function testOpenAIAccount(
  account: AccountSummary,
  input: { model?: string; prompt?: string; includeUnavailable?: boolean; signal?: AbortSignal; groupId?: string } = {}
): Promise<AccountTestResult> {
  const model = stringValue(input.model) || defaultTestModel
  const prompt = stringValue(input.prompt) || defaultTestPrompt
  const requestBody = createOpenAITestPayload(model, prompt, account.type === 'oauth')
  const requestBodyText = JSON.stringify(requestBody)
  const requestUrl = gatewayTestPath
  const modelsUrl = gatewayModelsPath
  const startedAt = Date.now()

  try {
    const resolved = resolveAccountTestCandidate(account, { groupId: stringValue(input.groupId) })
    const request = createGatewayTestRequest(requestBody, requestBodyText, account.type === 'oauth', input.signal)
    const response = new MemoryGatewayResponse(startedAt)
    const traceId = `acctest_${Date.now()}_${randomUUID()}`
    const context: RequestContext = {
      traceId,
      startedAt,
      method: request.method,
      path: request.path,
      originalUrl: request.originalUrl,
      clientIp: request.ip,
      systemAccountId: resolved.systemAccountId,
      groupId: resolved.groupId,
      logger: resolvedLogger()
    }

    await withRequestContext(context, () => withRequestAuthContext(undefined, () => handleOpenAIGatewayRequest(request, response.asResponse(), {
      identity: {
        systemAccountId: resolved.systemAccountId,
        groupId: resolved.groupId
      },
      candidateAccounts: [resolved.account],
      disableSessionAffinity: true,
      exposeUpstreamDiagnostics: true
    })))

    const finalAccount = findOpenAIAccountForGroup(resolved.groupId, account.id, resolved.systemAccountId, { ignoreAvailability: true }) ?? resolved.account
    const responseText = response.bodyText()
    const upstreamMessage = parseUpstreamMessage(responseText)
    const streamFailureMessage = parseOpenAIStreamFailureMessage(responseText)
    const outputText = extractOpenAIResponseOutputText(responseText)
    const success = response.statusCode >= 200 && response.statusCode < 300 && !streamFailureMessage
    const proxyFailureMessage = !success && finalAccount.proxyProfileUnavailable ? finalAccount.proxyProfileErrorMessage : undefined
    return {
      accountId: account.id,
      accountName: account.name,
      providerCode: account.providerCode,
      type: account.type,
      success,
      statusCode: response.statusCode,
      message: success ? 'OpenAI Responses 测试通过' : proxyFailureMessage || streamFailureMessage || upstreamMessage || `API 返回 HTTP ${response.statusCode}`,
      model,
      requestUrl,
      requestBody,
      responseHeaders: response.headersObject(),
      responseBody: parseJsonBody(responseText),
      responseText,
      outputText,
      modelsUrl,
      proxyUrl: accountTestProxyMarker(account, finalAccount),
      tokenRefreshed: didRefreshToken(account, finalAccount),
      durationMs: Date.now() - startedAt,
      firstTokenMs: response.firstTokenMs(),
      accountStatusChanged: finalAccount.status !== account.status,
      accountStatus: finalAccount.status
    }
  } catch (error) {
    const message = error instanceof Error ? error.message : 'OpenAI Responses 测试失败'
    return {
      accountId: account.id,
      accountName: account.name,
      providerCode: account.providerCode,
      type: account.type,
      success: false,
      message,
      model,
      requestUrl,
      requestBody,
      responseText: message,
      modelsUrl,
      proxyUrl: account.proxyProfileId ? '[configured]' : undefined,
      durationMs: Date.now() - startedAt,
      accountStatusChanged: false,
      accountStatus: account.status
    }
  }
}

function accountTestProxyMarker(account: AccountSummary, resolved: OpenAIAccountSecret): string | undefined {
  return account.proxyProfileId || resolved.proxyUrl || resolved.proxyProfileUnavailable ? '[configured]' : undefined
}

function resolveAccountTestCandidate(account: AccountSummary, input: { groupId?: string } = {}): {
  systemAccountId: string
  groupId: string
  account: OpenAIAccountSecret
} {
  const systemAccountId = account.systemAccountId ?? authorizedCallerSystemAccountId(account) ?? account.ownerSystemAccountId ?? resolveAccountSystemAccountId(account.id) ?? 'sys_admin'
  const groupId = input.groupId || account.boundGroupId
  if (!groupId) {
    throw new Error('账户未绑定可用分组，无法按客户真实链路测试')
  }
  const candidate = findOpenAIAccountForGroup(groupId, account.id, systemAccountId, { ignoreAvailability: true })
  if (!candidate) {
    throw new Error('账户不在当前分组或凭据不可用，无法执行网关测试')
  }
  return { systemAccountId, groupId, account: candidate }
}

function authorizedCallerSystemAccountId(account: AccountSummary): string | undefined {
  return account.accessType === 'authorized' ? getRequestAuthContext()?.systemAccountId : undefined
}

function createGatewayTestRequest(body: Record<string, unknown>, rawBodyText: string, isOAuth: boolean, signal?: AbortSignal): Request {
  const headers: IncomingHttpHeaders = {
    accept: isOAuth ? 'text/event-stream' : 'application/json, text/event-stream',
    'content-type': 'application/json',
    'content-length': String(Buffer.byteLength(rawBodyText))
  }
  return new MemoryGatewayRequest({
    method: 'POST',
    originalUrl: gatewayTestPath,
    path: gatewayTestPath,
    headers,
    body,
    rawBody: Buffer.from(rawBodyText),
    ip: '127.0.0.1',
    signal
  }).asRequest()
}

class MemoryGatewayRequest extends EventEmitter {
  constructor(private readonly input: {
    method: string
    originalUrl: string
    path: string
    headers: IncomingHttpHeaders
    body: Record<string, unknown>
    rawBody: Buffer
    ip: string
    signal?: AbortSignal
  }) {
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

  get body(): Record<string, unknown> {
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

class MemoryGatewayResponse extends EventEmitter {
  statusCode = 200
  writableEnded = false
  destroyed = false
  private readonly headers = new Map<string, string | string[]>()
  private readonly chunks: Buffer[] = []
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
      this.chunks.push(value)
    } else if (typeof value === 'string') {
      this.chunks.push(Buffer.from(value, 'utf8'))
    } else if (value !== undefined) {
      this.chunks.push(Buffer.from(JSON.stringify(value), 'utf8'))
    }
    return this.end()
  }

  write(value: Buffer | string | Uint8Array): boolean {
    const buffer = Buffer.isBuffer(value) ? value : Buffer.from(value)
    this.chunks.push(buffer)
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
    return Buffer.concat(this.chunks).toString('utf8')
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

function createOpenAITestPayload(model: string, prompt: string, isOAuth: boolean): Record<string, unknown> {
  const payload: Record<string, unknown> = {
    model,
    input: [
      {
        role: 'user',
        content: [
          {
            type: 'input_text',
            text: prompt
          }
        ]
      }
    ],
    instructions: defaultOpenAITestInstructions,
    stream: true
  }
  if (isOAuth) {
    payload.store = false
  }
  return payload
}

function didRefreshToken(original: AccountSummary, resolved: OpenAIAccountSecret): boolean | undefined {
  if (original.type !== 'oauth') return false
  const before = stringValue(original.credentials.access_token)
  const after = stringValue(resolved.credentials.access_token)
  return Boolean(after && before !== after)
}

function parseJsonBody(bodyText: string): unknown {
  if (!bodyText) return undefined
  try {
    return JSON.parse(bodyText) as unknown
  } catch {
    return undefined
  }
}

function extractOpenAIResponseOutputText(bodyText: string): string | undefined {
  if (!bodyText.trim()) return undefined
  const jsonOutput = extractTextFromOpenAIResponsePayload(parseJsonBody(bodyText))
  if (jsonOutput) return jsonOutput

  const outputParts: string[] = []
  for (const event of parseSseEvents(bodyText)) {
    if (event.type === 'response.output_text.delta' || event.type === 'response.refusal.delta') {
      const delta = stringValue(event.delta)
      if (delta) outputParts.push(delta)
      continue
    }
    if (event.type === 'response.output_text.done') {
      const text = stringValue(event.text)
      if (text && outputParts.join('') !== text) {
        return text
      }
      continue
    }
    if (event.type === 'response.completed' || event.type === 'response.done') {
      const responseText = extractTextFromOpenAIResponsePayload(event.response)
      if (responseText) return responseText
    }
  }

  const outputText = outputParts.join('').trim()
  return outputText || undefined
}

function extractTextFromOpenAIResponsePayload(payload: unknown): string | undefined {
  if (typeof payload !== 'object' || payload === null) return undefined
  const record = payload as Record<string, unknown>
  const outputText = stringValue(record.output_text)
  if (outputText) return outputText

  const outputParts: string[] = []
  const output = Array.isArray(record.output) ? record.output : []
  for (const item of output) {
    if (typeof item !== 'object' || item === null) continue
    const content = (item as Record<string, unknown>).content
    if (!Array.isArray(content)) continue
    for (const contentItem of content) {
      if (typeof contentItem !== 'object' || contentItem === null) continue
      const contentRecord = contentItem as Record<string, unknown>
      const text = stringValue(contentRecord.text)
      if (text) outputParts.push(text)
    }
  }

  const text = outputParts.join('').trim()
  return text || undefined
}

function parseOpenAIStreamFailureMessage(bodyText: string): string | undefined {
  if (!bodyText.includes('response.failed') && !bodyText.includes('response.incomplete') && !bodyText.includes('error')) {
    return undefined
  }
  for (const payload of parseSseEvents(bodyText)) {
    const type = stringValue(payload.type)
    if (type !== 'response.failed' && type !== 'response.incomplete' && type !== 'error') continue
    const error = payload.error ?? (payload.response as Record<string, unknown> | undefined)?.error
    const message = parseErrorMessage(error) || parseErrorMessage(payload)
    return message || type
  }
  return undefined
}

function parseSseEvents(bodyText: string): Array<Record<string, unknown>> {
  const events: Array<Record<string, unknown>> = []
  let eventName = ''
  let dataLines: string[] = []
  const flush = () => {
    const data = dataLines.join('\n').trim()
    const type = eventName
    eventName = ''
    dataLines = []
    if (!data || data === '[DONE]') return
    try {
      const payload = JSON.parse(data) as Record<string, unknown>
      if (type && typeof payload.type !== 'string') payload.type = type
      events.push(payload)
    } catch {
    }
  }
  for (const line of bodyText.split(/\r?\n/)) {
    if (!line) {
      flush()
      continue
    }
    if (line.startsWith('event:')) {
      eventName = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).trimStart())
    }
  }
  flush()
  return events
}

function parseUpstreamMessage(bodyText: string): string | undefined {
  if (!bodyText) return undefined
  try {
    const payload = JSON.parse(bodyText) as Record<string, unknown>
    const error = payload.error
    if (typeof error === 'object' && error !== null) {
      const message = (error as Record<string, unknown>).message
      if (typeof message === 'string' && message.trim()) {
        return message.trim()
      }
    }
    const message = payload.message
    if (typeof message === 'string' && message.trim()) {
      return message.trim()
    }
  } catch {
    return bodyText.slice(0, 240)
  }
  return undefined
}

function parseErrorMessage(value: unknown): string | undefined {
  if (typeof value === 'string' && value.trim()) return value.trim()
  if (typeof value !== 'object' || value === null) return undefined
  const payload = value as Record<string, unknown>
  return stringValue(payload.message) || stringValue(payload.code) || stringValue(payload.type)
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function resolvedLogger(): RequestContext['logger'] {
  return logger.child({ source: 'account_test' })
}
