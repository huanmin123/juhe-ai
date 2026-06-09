import type { Request } from 'express'

import type { OpenAIResponsesUpstreamMode } from '../../domain/types.js'
import {
  getGatewayRequestBodyState,
  gatewayJsonBodyInlineParseMaxBytes,
  type GatewayRawBodyRequest
} from './openai-gateway-request-body.js'
import {
  isGatewayJsonWorkerQueueFullError,
  parseGatewayJsonBodyInWorker
} from './openai-gateway-json-parser.js'
import { splitPathAndQuery } from './openai-gateway-route-helpers.js'

export interface OpenAIResponsesChatBridgeAccount {
  type?: string
  openAIResponsesUpstreamMode?: OpenAIResponsesUpstreamMode
}

export class OpenAIResponsesChatBridgeError extends Error {
  constructor(
    message: string,
    readonly statusCode = 400,
    readonly code = 'responses_chat_bridge_error',
    readonly type = 'invalid_request_error'
  ) {
    super(message)
    this.name = 'OpenAIResponsesChatBridgeError'
  }
}

type BridgeBodyCacheRequest = GatewayRawBodyRequest & {
  gatewayResponsesChatBridgeBodyCache?: Map<string, Buffer>
}

interface ChatToolCallState {
  id: string
  name: string
  argumentsText: string
  outputIndex: number
  added: boolean
}

const defaultBridgeBaseUrlPath = '/v1/chat/completions'

export function isOpenAIResponsesChatBridgeAccount(account: OpenAIResponsesChatBridgeAccount): boolean {
  return account.type !== 'oauth' && account.openAIResponsesUpstreamMode === 'chat_completions_bridge'
}

export function isOpenAIResponsesPostRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') return false
  const { path } = splitPathAndQuery(req.originalUrl || req.path || '')
  return normalizedOpenAIPath(path) === '/responses'
}

export function isOpenAIResponsesChatBridgeRequest(req: Request, account: OpenAIResponsesChatBridgeAccount): boolean {
  return isOpenAIResponsesChatBridgeAccount(account) && isOpenAIResponsesPostRequest(req)
}

export function buildOpenAIResponsesChatBridgeUpstreamPathAndQuery(req: Request): string {
  const { query } = splitPathAndQuery(req.originalUrl || req.path || '')
  return `${defaultBridgeBaseUrlPath}${query}`
}

export async function buildOpenAIResponsesChatBridgeRequestBody(
  req: Request,
  _account: OpenAIResponsesChatBridgeAccount,
  signal?: AbortSignal,
  options: { modelOverride?: string } = {}
): Promise<Buffer> {
  const cacheKey = options.modelOverride ?? ''
  const requestWithCache = req as BridgeBodyCacheRequest
  const cached = requestWithCache.gatewayResponsesChatBridgeBodyCache?.get(cacheKey)
  if (cached) return cached

  const responsesBody = await parseResponsesBridgeJsonBody(req, signal)
  const chatBody = transformResponsesRequestToChatCompletions(responsesBody, options)
  const body = Buffer.from(JSON.stringify(chatBody), 'utf8')
  requestWithCache.gatewayResponsesChatBridgeBodyCache ??= new Map()
  requestWithCache.gatewayResponsesChatBridgeBodyCache.set(cacheKey, body)
  return body
}

export function transformChatCompletionResponseToResponsesBuffer(body: Buffer): Buffer {
  let parsed: unknown
  try {
    parsed = JSON.parse(body.toString('utf8')) as unknown
  } catch {
    throw new OpenAIResponsesChatBridgeError('上游 Chat Completions 响应不是有效 JSON，无法转换为 Responses', 502, 'responses_chat_bridge_upstream_json_invalid', 'upstream_response_error')
  }
  if (!isPlainObject(parsed)) {
    throw new OpenAIResponsesChatBridgeError('上游 Chat Completions 响应不是 JSON 对象，无法转换为 Responses', 502, 'responses_chat_bridge_upstream_shape_invalid', 'upstream_response_error')
  }
  return Buffer.from(JSON.stringify(transformChatCompletionResponseToResponses(parsed)), 'utf8')
}

export async function* transformChatCompletionSseToResponsesStream(
  upstreamBody: AsyncIterable<Uint8Array>,
  input: { model?: string } = {}
): AsyncIterable<Uint8Array> {
  const transformer = new ChatCompletionSseToResponsesTransformer(input)
  for await (const chunk of upstreamBody) {
    for (const output of transformer.push(Buffer.from(chunk))) {
      yield output
    }
  }
  for (const output of transformer.flush()) {
    yield output
  }
}

export function createChatCompletionSseToResponsesTransformer(input: { model?: string } = {}): {
  push: (chunk: Buffer) => Buffer[]
  flush: () => Buffer[]
} {
  const transformer = new ChatCompletionSseToResponsesTransformer(input)
  return {
    push: (chunk) => transformer.push(chunk),
    flush: () => transformer.flush()
  }
}

function transformResponsesRequestToChatCompletions(
  body: Record<string, unknown>,
  options: { modelOverride?: string } = {}
): Record<string, unknown> {
  const model = options.modelOverride ?? stringValue(body.model)
  if (!model) {
    throw new OpenAIResponsesChatBridgeError('Responses 转 Chat Completions 要求请求体包含 model')
  }
  const messages = responsesMessagesToChatMessages(body)
  if (messages.length === 0) {
    throw new OpenAIResponsesChatBridgeError('Responses 转 Chat Completions 要求 input 至少包含一条可转换消息')
  }

  const output: Record<string, unknown> = {
    model,
    messages
  }
  copyRequestField(body, output, 'temperature')
  copyRequestField(body, output, 'top_p')
  copyRequestField(body, output, 'presence_penalty')
  copyRequestField(body, output, 'frequency_penalty')
  copyRequestField(body, output, 'stop')
  copyRequestField(body, output, 'seed')
  copyRequestField(body, output, 'user')
  copyRequestField(body, output, 'response_format')
  copyRequestField(body, output, 'parallel_tool_calls')
  copyRequestField(body, output, 'logprobs')
  copyRequestField(body, output, 'top_logprobs')
  copyRequestField(body, output, 'n')

  const maxTokens = numberValue(body.max_tokens)
    ?? numberValue(body.max_completion_tokens)
    ?? numberValue(body.max_output_tokens)
  if (maxTokens !== undefined) {
    output.max_tokens = maxTokens
  }

  const reasoning = objectValue(body.reasoning)
  const reasoningEffort = stringValue(reasoning?.effort)
  if (reasoningEffort) {
    output.reasoning_effort = reasoningEffort
  }

  if (Array.isArray(body.tools)) {
    output.tools = body.tools.map((tool, index) => transformResponsesToolToChatTool(tool, index))
  }
  if (Object.prototype.hasOwnProperty.call(body, 'tool_choice')) {
    output.tool_choice = transformResponsesToolChoice(body.tool_choice)
  }

  if (body.stream === true) {
    output.stream = true
    output.stream_options = {
      ...objectValue(body.stream_options),
      include_usage: true
    }
  } else if (body.stream === false) {
    output.stream = false
  }

  return output
}

function responsesMessagesToChatMessages(body: Record<string, unknown>): Array<Record<string, unknown>> {
  const messages: Array<Record<string, unknown>> = []
  const instructions = stringValue(body.instructions)
  if (instructions) {
    messages.push({ role: 'system', content: instructions })
  }

  if (Array.isArray(body.messages) && !Object.prototype.hasOwnProperty.call(body, 'input')) {
    messages.push(...body.messages.map((item, index) => normalizeExistingChatMessage(item, index)))
    return messages
  }

  const input = body.input
  if (typeof input === 'string') {
    messages.push({ role: 'user', content: input })
    return messages
  }
  if (!Array.isArray(input)) {
    return messages
  }
  for (const item of input) {
    const converted = responsesInputItemToChatMessages(item)
    messages.push(...converted)
  }
  return messages
}

function normalizeExistingChatMessage(value: unknown, index: number): Record<string, unknown> {
  if (!isPlainObject(value)) {
    throw new OpenAIResponsesChatBridgeError(`messages[${index}] 必须是对象`)
  }
  const role = normalizeChatRole(value.role)
  const output: Record<string, unknown> = { ...value, role }
  if (Array.isArray(value.content)) {
    output.content = textFromResponsesContentParts(value.content, `messages[${index}].content`)
  }
  return output
}

function responsesInputItemToChatMessages(value: unknown): Array<Record<string, unknown>> {
  if (typeof value === 'string') {
    return [{ role: 'user', content: value }]
  }
  if (!isPlainObject(value)) {
    throw new OpenAIResponsesChatBridgeError('Responses input 数组只支持字符串或对象')
  }
  const type = stringValue(value.type)
  if (type === 'reasoning') {
    return []
  }
  if (type === 'function_call_output') {
    const callId = stringValue(value.call_id) || stringValue(value.id)
    if (!callId) {
      throw new OpenAIResponsesChatBridgeError('function_call_output 缺少 call_id，无法转换为 Chat tool 消息')
    }
    return [{
      role: 'tool',
      tool_call_id: callId,
      content: textValue(value.output)
    }]
  }
  if (type === 'function_call') {
    const name = stringValue(value.name)
    if (!name) {
      throw new OpenAIResponsesChatBridgeError('function_call 缺少 name，无法转换为 Chat tool_calls')
    }
    const callId = stringValue(value.call_id) || stringValue(value.id) || `call_${Math.random().toString(16).slice(2)}`
    return [{
      role: 'assistant',
      content: null,
      tool_calls: [{
        id: callId,
        type: 'function',
        function: {
          name,
          arguments: textValue(value.arguments)
        }
      }]
    }]
  }
  if (type && type !== 'message') {
    throw new OpenAIResponsesChatBridgeError(`Responses input item 类型 ${type} 暂不支持转为 Chat Completions`)
  }
  const role = normalizeChatRole(value.role ?? 'user')
  return [{
    role,
    content: responsesMessageContentToChatContent(value.content)
  }]
}

function responsesMessageContentToChatContent(value: unknown): string {
  if (typeof value === 'string') {
    return value
  }
  if (Array.isArray(value)) {
    return textFromResponsesContentParts(value, 'content')
  }
  if (value === undefined || value === null) {
    return ''
  }
  throw new OpenAIResponsesChatBridgeError('Responses message content 只支持文本内容')
}

function textFromResponsesContentParts(parts: unknown[], label: string): string {
  return parts.map((part, index) => {
    if (typeof part === 'string') return part
    if (!isPlainObject(part)) {
      throw new OpenAIResponsesChatBridgeError(`${label}[${index}] 必须是对象或字符串`)
    }
    const type = stringValue(part.type)
    if (type === 'input_text' || type === 'output_text' || type === 'text' || !type) {
      return textValue(part.text)
    }
    throw new OpenAIResponsesChatBridgeError(`Responses content part 类型 ${type} 暂不支持转为 Chat Completions`)
  }).filter((text) => text.length > 0).join('\n')
}

function transformResponsesToolToChatTool(value: unknown, index: number): Record<string, unknown> {
  if (!isPlainObject(value)) {
    throw new OpenAIResponsesChatBridgeError(`tools[${index}] 必须是对象`)
  }
  if (value.type !== 'function') {
    throw new OpenAIResponsesChatBridgeError(`Responses tool 类型 ${String(value.type)} 暂不支持转为 Chat Completions`)
  }
  const existingFunction = objectValue(value.function)
  const name = stringValue(existingFunction?.name) || stringValue(value.name)
  if (!name) {
    throw new OpenAIResponsesChatBridgeError(`tools[${index}] 缺少 function name`)
  }
  const fn: Record<string, unknown> = { name }
  const description = stringValue(existingFunction?.description) || stringValue(value.description)
  if (description) fn.description = description
  const parameters = existingFunction?.parameters ?? value.parameters
  if (parameters !== undefined) fn.parameters = parameters
  const strict = existingFunction?.strict ?? value.strict
  if (typeof strict === 'boolean') fn.strict = strict
  return {
    type: 'function',
    function: fn
  }
}

function transformResponsesToolChoice(value: unknown): unknown {
  if (value === undefined || value === null) return undefined
  if (typeof value === 'string') return value
  if (!isPlainObject(value)) {
    throw new OpenAIResponsesChatBridgeError('tool_choice 只支持字符串或 function 对象')
  }
  const type = stringValue(value.type)
  if (type === 'function') {
    const fn = objectValue(value.function)
    const name = stringValue(value.name) || stringValue(fn?.name)
    if (!name) {
      throw new OpenAIResponsesChatBridgeError('function tool_choice 缺少 name')
    }
    return { type: 'function', function: { name } }
  }
  if (type === 'auto' || type === 'none' || type === 'required') {
    return type
  }
  throw new OpenAIResponsesChatBridgeError(`tool_choice 类型 ${type || 'unknown'} 暂不支持转为 Chat Completions`)
}

function transformChatCompletionResponseToResponses(chat: Record<string, unknown>): Record<string, unknown> {
  const choice = firstChoice(chat)
  const message = objectValue(choice?.message) ?? {}
  const content = chatMessageContentText(message.content)
  const output: Array<Record<string, unknown>> = []
  if (content) {
    output.push(responseMessageItem(content, 'completed'))
  }
  const toolCalls = Array.isArray(message.tool_calls) ? message.tool_calls : []
  for (const toolCall of toolCalls) {
    const converted = chatToolCallToResponsesFunctionCall(toolCall)
    if (converted) output.push(converted)
  }
  return {
    id: responseId(chat.id),
    object: 'response',
    created_at: numberValue(chat.created) ?? Math.floor(Date.now() / 1000),
    status: 'completed',
    model: stringValue(chat.model),
    output,
    output_text: content,
    usage: chatUsageToResponsesUsage(chat.usage)
  }
}

class ChatCompletionSseToResponsesTransformer {
  private decoder = new TextDecoder()
  private eventName = ''
  private dataLines: string[] = []
  private pendingLine = ''
  private readonly responseIdValue: string
  private readonly createdAt = Math.floor(Date.now() / 1000)
  private model = ''
  private completed = false
  private started = false
  private messageOutputIndex: number | undefined
  private messageItemId = `msg_${Math.random().toString(16).slice(2)}`
  private contentPartStarted = false
  private outputText = ''
  private nextOutputIndex = 0
  private usage: unknown
  private finishSeen = false
  private readonly toolCallsByIndex = new Map<number, ChatToolCallState>()

  constructor(input: { model?: string } = {}) {
    this.responseIdValue = `resp_${Math.random().toString(16).slice(2)}`
    this.model = input.model ?? ''
  }

  push(chunk: Buffer): Buffer[] {
    return this.pushText(this.decoder.decode(chunk, { stream: true }))
  }

  flush(): Buffer[] {
    const output = this.pushText(this.decoder.decode())
    if (this.pendingLine) {
      this.flushPendingLine(output)
    }
    this.flushEvent(output)
    if (!this.completed && this.finishSeen) {
      output.push(...this.completeResponse())
    }
    return output
  }

  private pushText(text: string): Buffer[] {
    const output: Buffer[] = []
    let offset = 0
    while (offset < text.length) {
      const newlineIndex = text.indexOf('\n', offset)
      const segmentEnd = newlineIndex < 0 ? text.length : newlineIndex
      this.pendingLine += text.slice(offset, segmentEnd)
      if (newlineIndex < 0) break
      this.flushPendingLine(output)
      offset = newlineIndex + 1
    }
    return output
  }

  private flushPendingLine(output: Buffer[]): void {
    const line = this.pendingLine.endsWith('\r') ? this.pendingLine.slice(0, -1) : this.pendingLine
    this.pendingLine = ''
    if (line === '') {
      this.flushEvent(output)
    } else if (line.startsWith('event:')) {
      this.eventName = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      this.dataLines.push(line.slice(5).trimStart())
    }
  }

  private flushEvent(output: Buffer[]): void {
    if (this.dataLines.length === 0) {
      this.eventName = ''
      return
    }
    const dataText = this.dataLines.join('\n').trim()
    this.eventName = ''
    this.dataLines = []
    if (!dataText) return
    if (dataText === '[DONE]') {
      output.push(...this.completeResponse())
      return
    }
    let data: unknown
    try {
      data = JSON.parse(dataText) as unknown
    } catch {
      throw new Error('上游 Chat Completions SSE 事件不是有效 JSON')
    }
    if (!isPlainObject(data)) {
      throw new Error('上游 Chat Completions SSE 事件不是 JSON 对象')
    }
    output.push(...this.transformChatChunk(data))
  }

  private transformChatChunk(chunk: Record<string, unknown>): Buffer[] {
    const output: Buffer[] = []
    if (isPlainObject(chunk.error)) {
      output.push(...this.failResponse(chunk.error))
      return output
    }
    this.model ||= stringValue(chunk.model)
    if (chunk.usage !== undefined) {
      this.usage = chunk.usage
    }
    const choices = Array.isArray(chunk.choices) ? chunk.choices : []
    for (const rawChoice of choices) {
      const choice = objectValue(rawChoice)
      if (!choice) continue
      const delta = objectValue(choice.delta)
      if (delta) {
        const content = typeof delta.content === 'string' ? delta.content : ''
        if (content) {
          output.push(...this.emitTextDelta(content))
        }
        const toolCalls = Array.isArray(delta.tool_calls) ? delta.tool_calls : []
        for (const toolCall of toolCalls) {
          output.push(...this.emitToolCallDelta(toolCall))
        }
      }
      if (choice.finish_reason !== undefined && choice.finish_reason !== null) {
        this.finishSeen = true
      }
    }
    return output
  }

  private emitTextDelta(delta: string): Buffer[] {
    const output = this.ensureMessageOutputStarted()
    output.push(sseEvent('response.output_text.delta', {
      type: 'response.output_text.delta',
      item_id: this.messageItemId,
      output_index: this.messageOutputIndex ?? 0,
      content_index: 0,
      delta
    }))
    this.outputText += delta
    return output
  }

  private emitToolCallDelta(value: unknown): Buffer[] {
    if (!isPlainObject(value)) return []
    const index = numberValue(value.index) ?? 0
    const fn = objectValue(value.function)
    const existing = this.toolCallsByIndex.get(index)
    const id = stringValue(value.id) || existing?.id || `call_${index}_${Math.random().toString(16).slice(2)}`
    const name = stringValue(fn?.name) || existing?.name || ''
    const argumentsDelta = typeof fn?.arguments === 'string' ? fn.arguments : ''
    const state = existing ?? {
      id,
      name,
      argumentsText: '',
      outputIndex: this.nextOutputIndex++,
      added: false
    }
    state.name = name || state.name
    state.argumentsText += argumentsDelta
    this.toolCallsByIndex.set(index, state)

    const output = this.ensureResponseStarted()
    if (!state.added) {
      state.added = true
      output.push(sseEvent('response.output_item.added', {
        type: 'response.output_item.added',
        output_index: state.outputIndex,
        item: {
          id: state.id,
          type: 'function_call',
          status: 'in_progress',
          call_id: state.id,
          name: state.name,
          arguments: ''
        }
      }))
    }
    if (argumentsDelta) {
      output.push(sseEvent('response.function_call_arguments.delta', {
        type: 'response.function_call_arguments.delta',
        item_id: state.id,
        output_index: state.outputIndex,
        delta: argumentsDelta
      }))
    }
    return output
  }

  private ensureResponseStarted(): Buffer[] {
    if (this.started) return []
    this.started = true
    return [
      sseEvent('response.created', {
        type: 'response.created',
        response: this.responseObject('in_progress')
      }),
      sseEvent('response.in_progress', {
        type: 'response.in_progress',
        response: this.responseObject('in_progress')
      })
    ]
  }

  private ensureMessageOutputStarted(): Buffer[] {
    const output = this.ensureResponseStarted()
    if (this.messageOutputIndex === undefined) {
      this.messageOutputIndex = this.nextOutputIndex++
      output.push(sseEvent('response.output_item.added', {
        type: 'response.output_item.added',
        output_index: this.messageOutputIndex,
        item: {
          id: this.messageItemId,
          type: 'message',
          status: 'in_progress',
          role: 'assistant',
          content: []
        }
      }))
    }
    if (!this.contentPartStarted) {
      this.contentPartStarted = true
      output.push(sseEvent('response.content_part.added', {
        type: 'response.content_part.added',
        item_id: this.messageItemId,
        output_index: this.messageOutputIndex,
        content_index: 0,
        part: {
          type: 'output_text',
          text: '',
          annotations: []
        }
      }))
    }
    return output
  }

  private completeResponse(): Buffer[] {
    if (this.completed) return []
    this.completed = true
    const output = this.ensureResponseStarted()
    if (this.contentPartStarted && this.messageOutputIndex !== undefined) {
      output.push(sseEvent('response.output_text.done', {
        type: 'response.output_text.done',
        item_id: this.messageItemId,
        output_index: this.messageOutputIndex,
        content_index: 0,
        text: this.outputText
      }))
      output.push(sseEvent('response.content_part.done', {
        type: 'response.content_part.done',
        item_id: this.messageItemId,
        output_index: this.messageOutputIndex,
        content_index: 0,
        part: {
          type: 'output_text',
          text: this.outputText,
          annotations: []
        }
      }))
      output.push(sseEvent('response.output_item.done', {
        type: 'response.output_item.done',
        output_index: this.messageOutputIndex,
        item: responseMessageItem(this.outputText, 'completed', this.messageItemId)
      }))
    }
    for (const state of [...this.toolCallsByIndex.values()].sort((left, right) => left.outputIndex - right.outputIndex)) {
      output.push(sseEvent('response.function_call_arguments.done', {
        type: 'response.function_call_arguments.done',
        item_id: state.id,
        output_index: state.outputIndex,
        arguments: state.argumentsText
      }))
      output.push(sseEvent('response.output_item.done', {
        type: 'response.output_item.done',
        output_index: state.outputIndex,
        item: responseFunctionCallItem(state, 'completed')
      }))
    }
    output.push(sseEvent('response.completed', {
      type: 'response.completed',
      response: this.responseObject('completed')
    }))
    return output
  }

  private failResponse(error: Record<string, unknown>): Buffer[] {
    if (this.completed) return []
    this.completed = true
    const output = this.ensureResponseStarted()
    output.push(sseEvent('response.failed', {
      type: 'response.failed',
      response: this.responseObject('failed', {
        code: stringValue(error.code) || 'upstream_error',
        message: stringValue(error.message) || '上游 Chat Completions 流式响应失败'
      })
    }))
    return output
  }

  private responseObject(status: 'in_progress' | 'completed' | 'failed', error?: { code: string; message: string }): Record<string, unknown> {
    const output: Array<Record<string, unknown>> = []
    if (this.outputText || this.messageOutputIndex !== undefined) {
      output.push(responseMessageItem(this.outputText, status === 'completed' ? 'completed' : 'in_progress', this.messageItemId))
    }
    for (const state of [...this.toolCallsByIndex.values()].sort((left, right) => left.outputIndex - right.outputIndex)) {
      output.push(responseFunctionCallItem(state, status === 'completed' ? 'completed' : 'in_progress'))
    }
    return {
      id: this.responseIdValue,
      object: 'response',
      created_at: this.createdAt,
      status,
      model: this.model,
      output,
      output_text: this.outputText,
      usage: chatUsageToResponsesUsage(this.usage),
      ...(error ? { error } : {})
    }
  }
}

async function parseResponsesBridgeJsonBody(req: Request, signal?: AbortSignal): Promise<Record<string, unknown>> {
  if (isPlainObject(req.body)) {
    return req.body
  }
  const requestWithBody = req as GatewayRawBodyRequest
  if (requestWithBody.gatewayParsedJsonBodyAvailable && isPlainObject(requestWithBody.gatewayParsedJsonBody)) {
    return requestWithBody.gatewayParsedJsonBody
  }
  const bodyState = getGatewayRequestBodyState(req)
  if (bodyState?.jsonParseStatus === 'invalid_json') {
    throw new OpenAIResponsesChatBridgeError('Responses 转 Chat Completions 要求请求体是有效的 JSON 对象')
  }
  const rawBody = requestWithBody.rawBody
  if (!rawBody || rawBody.length === 0) {
    return {}
  }
  let parsed: unknown
  try {
    parsed = rawBody.length > gatewayJsonBodyInlineParseMaxBytes
      ? await parseGatewayJsonBodyInWorker(rawBody, undefined, signal)
      : JSON.parse(rawBody.toString('utf8')) as unknown
  } catch (error) {
    if (isGatewayJsonWorkerQueueFullError(error)) {
      throw new OpenAIResponsesChatBridgeError('网关请求解析繁忙，请稍后重试', 503, 'responses_chat_bridge_json_parser_busy', 'service_unavailable')
    }
    throw new OpenAIResponsesChatBridgeError('Responses 转 Chat Completions 要求请求体是有效的 JSON 对象')
  }
  if (!isPlainObject(parsed)) {
    throw new OpenAIResponsesChatBridgeError('Responses 转 Chat Completions 要求请求体是 JSON 对象')
  }
  return parsed
}

function normalizedOpenAIPath(path: string): string {
  return (path.replace(/^\/v1(?=\/|$)/, '') || '/')
}

function normalizeChatRole(value: unknown): string {
  const role = stringValue(value)
  if (role === 'developer') return 'system'
  if (role === 'system' || role === 'user' || role === 'assistant' || role === 'tool') return role
  return 'user'
}

function chatToolCallToResponsesFunctionCall(value: unknown): Record<string, unknown> | undefined {
  const toolCall = objectValue(value)
  const fn = objectValue(toolCall?.function)
  const name = stringValue(fn?.name)
  if (!toolCall || !name) return undefined
  const id = stringValue(toolCall.id) || `call_${Math.random().toString(16).slice(2)}`
  return {
    id,
    type: 'function_call',
    status: 'completed',
    call_id: id,
    name,
    arguments: textValue(fn?.arguments)
  }
}

function responseMessageItem(text: string, status: 'in_progress' | 'completed', id = `msg_${Math.random().toString(16).slice(2)}`): Record<string, unknown> {
  return {
    id,
    type: 'message',
    status,
    role: 'assistant',
    content: text ? [{
      type: 'output_text',
      text,
      annotations: []
    }] : []
  }
}

function responseFunctionCallItem(state: ChatToolCallState, status: 'in_progress' | 'completed'): Record<string, unknown> {
  return {
    id: state.id,
    type: 'function_call',
    status,
    call_id: state.id,
    name: state.name,
    arguments: state.argumentsText
  }
}

function chatUsageToResponsesUsage(value: unknown): Record<string, unknown> | undefined {
  const usage = objectValue(value)
  if (!usage) return undefined
  const promptDetails = objectValue(usage.prompt_tokens_details)
  const completionDetails = objectValue(usage.completion_tokens_details)
  const inputTokens = numberValue(usage.input_tokens) ?? numberValue(usage.prompt_tokens)
  const outputTokens = numberValue(usage.output_tokens) ?? numberValue(usage.completion_tokens)
  const totalTokens = numberValue(usage.total_tokens)
    ?? (inputTokens !== undefined || outputTokens !== undefined ? (inputTokens ?? 0) + (outputTokens ?? 0) : undefined)
  return {
    input_tokens: inputTokens ?? 0,
    output_tokens: outputTokens ?? 0,
    total_tokens: totalTokens ?? 0,
    input_tokens_details: {
      cached_tokens: numberValue(promptDetails?.cached_tokens) ?? 0
    },
    output_tokens_details: {
      reasoning_tokens: numberValue(completionDetails?.reasoning_tokens) ?? 0
    }
  }
}

function firstChoice(chat: Record<string, unknown>): Record<string, unknown> | undefined {
  const choices = Array.isArray(chat.choices) ? chat.choices : []
  return choices.map(objectValue).find((choice): choice is Record<string, unknown> => Boolean(choice))
}

function chatMessageContentText(value: unknown): string {
  if (typeof value === 'string') return value
  if (!Array.isArray(value)) return ''
  return value.map((part) => {
    if (typeof part === 'string') return part
    const row = objectValue(part)
    return stringValue(row?.text)
  }).filter(Boolean).join('\n')
}

function responseId(value: unknown): string {
  const id = stringValue(value)
  return id.startsWith('resp_') ? id : id ? `resp_${id}` : `resp_${Math.random().toString(16).slice(2)}`
}

function sseEvent(eventName: string, payload: Record<string, unknown>): Buffer {
  return Buffer.from(`event: ${eventName}\ndata: ${JSON.stringify(payload)}\n\n`, 'utf8')
}

function copyRequestField(input: Record<string, unknown>, output: Record<string, unknown>, key: string): void {
  if (Object.prototype.hasOwnProperty.call(input, key)) {
    output[key] = input[key]
  }
}

function stringValue(value: unknown): string {
  return typeof value === 'string' && value.trim() ? value.trim() : ''
}

function textValue(value: unknown): string {
  if (typeof value === 'string') return value
  if (value === undefined || value === null) return ''
  return JSON.stringify(value)
}

function numberValue(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : Number.NaN
  return Number.isFinite(number) ? Math.trunc(number) : undefined
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return isPlainObject(value) ? value : undefined
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
