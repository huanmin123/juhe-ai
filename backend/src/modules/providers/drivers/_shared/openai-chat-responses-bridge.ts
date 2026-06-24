import { StringDecoder } from 'node:string_decoder'
import type { Request } from 'express'

import {
  getGatewayRequestBodyState,
  gatewayJsonBodyInlineParseMaxBytes,
  type GatewayRawBodyRequest
} from '../../../gateway/request/body.js'
import {
  isGatewayJsonWorkerQueueFullError,
  parseGatewayJsonBodyInWorker
} from '../../../gateway/request/json-parser.js'
import { requestStream } from '../../../gateway/request/metadata.js'
import type { GatewayUpstreamResponse } from '../../../gateway/upstream/request.js'
import { GatewayRequestValidationError } from '../../../gateway/request/validation-error.js'

type JsonRecord = Record<string, unknown>

interface BuildOpenAIChatResponsesBridgeBodyOptions {
  defaultModel: string
  modelOverride?: string
}

interface TransformOpenAIChatResponsesBridgeResponseOptions {
  enabled: boolean
  model: string
}

interface ChatResponsesStreamToolCallState {
  index: number
  id: string
  name: string
  arguments: string
  added: boolean
}

interface ChatResponsesStreamState {
  id: string
  createdAt: number
  model: string
  roleSent: boolean
  completed: boolean
  failed: boolean
  hadToolCall: boolean
  toolCalls: Map<number, ChatResponsesStreamToolCallState>
  nextToolCallIndex: number
  usage?: JsonRecord
}

interface ParsedSseEvent {
  eventName?: string
  dataText?: string
  data?: JsonRecord
  dataParseError?: boolean
}

export function isOpenAIChatCompletionsPostRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') return false
  const path = (req.originalUrl || req.path || '').split('?', 1)[0]
  return (path.replace(/^\/v1(?=\/|$)/, '') || '/') === '/chat/completions'
}

export function prepareOpenAIChatResponsesBridgeHeaders(headers: Headers): void {
  headers.set('content-type', 'application/json')
  headers.delete('content-length')
}

export async function buildOpenAIChatResponsesBridgeBody(
  req: Request,
  options: BuildOpenAIChatResponsesBridgeBodyOptions,
  signal?: AbortSignal
): Promise<Buffer> {
  const body = await parseGatewayJsonObject(req, signal)
  validateChatResponsesBridgeBody(body)
  const model = options.modelOverride ?? stringValue(body.model) ?? options.defaultModel
  const responseBody = chatCompletionsBodyToResponsesBody(body, model)
  return Buffer.from(JSON.stringify(responseBody), 'utf8')
}

export function transformOpenAIChatResponsesBridgeUpstreamResponse(
  req: Request,
  response: GatewayUpstreamResponse,
  options: TransformOpenAIChatResponsesBridgeResponseOptions
): GatewayUpstreamResponse {
  if (!options.enabled || !response.ok || !response.body || !isOpenAIChatCompletionsPostRequest(req)) {
    return response
  }
  const headers = new Headers(response.headers)
  headers.delete('content-length')
  if (requestStream(req)) {
    headers.set('content-type', 'text/event-stream; charset=utf-8')
    return {
      status: response.status,
      ok: response.ok,
      headers,
      body: transformResponsesSseToChatCompletionsSse(response.body, options.model)
    }
  }
  headers.set('content-type', 'application/json; charset=utf-8')
  return {
    status: response.status,
    ok: response.ok,
    headers,
    body: transformResponsesJsonToChatCompletionsJson(response.body, options.model)
  }
}

async function parseGatewayJsonObject(req: Request, signal?: AbortSignal): Promise<JsonRecord> {
  if (isPlainObject(req.body)) {
    return { ...req.body }
  }
  const requestWithBody = req as GatewayRawBodyRequest
  if (requestWithBody.gatewayParsedJsonBodyAvailable && isPlainObject(requestWithBody.gatewayParsedJsonBody)) {
    return { ...requestWithBody.gatewayParsedJsonBody }
  }
  const bodyState = getGatewayRequestBodyState(req)
  if (bodyState?.jsonParseStatus === 'invalid_json') {
    throw new GatewayRequestValidationError(
      'Chat Completions 到 Responses 桥接要求请求体是有效 JSON 对象',
      'invalid_chat_responses_bridge_json_body'
    )
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
      throw new GatewayRequestValidationError(
        '网关请求解析繁忙，请稍后重试',
        'gateway_json_parser_busy',
        { statusCode: 503, type: 'server_overloaded' }
      )
    }
    throw new GatewayRequestValidationError(
      'Chat Completions 到 Responses 桥接要求请求体是有效 JSON 对象',
      'invalid_chat_responses_bridge_json_body'
    )
  }
  if (!isPlainObject(parsed)) {
    throw new GatewayRequestValidationError(
      'Chat Completions 到 Responses 桥接要求请求体是 JSON 对象',
      'invalid_chat_responses_bridge_json_body'
    )
  }
  return { ...parsed }
}

function validateChatResponsesBridgeBody(body: JsonRecord): void {
  const n = integerValue(body.n)
  if (n !== undefined && n !== 1) {
    throw new GatewayRequestValidationError(
      'Chat Completions 到 Responses 桥接暂不支持 n 大于 1 的多候选输出',
      'unsupported_chat_responses_bridge_n'
    )
  }
  if (!Array.isArray(body.messages)) {
    throw new GatewayRequestValidationError(
      'Chat Completions 到 Responses 桥接要求 messages 是数组',
      'invalid_chat_responses_bridge_messages'
    )
  }
}

function chatCompletionsBodyToResponsesBody(body: JsonRecord, model: string): JsonRecord {
  const output: JsonRecord = {
    ...body,
    model,
    input: chatMessagesToResponsesInput(body.messages)
  }
  delete output.messages
  delete output.functions
  delete output.function_call
  delete output.max_tokens
  delete output.max_completion_tokens
  delete output.n
  delete output.response_format
  if (integerValue(body.max_completion_tokens) !== undefined || integerValue(body.max_tokens) !== undefined) {
    output.max_output_tokens = integerValue(body.max_completion_tokens) ?? integerValue(body.max_tokens)
  }
  const tools = chatToolsToResponsesTools(body.tools, body.functions)
  if (tools.length > 0) {
    output.tools = tools
  } else {
    delete output.tools
  }
  const toolChoice = chatToolChoiceToResponsesToolChoice(body.tool_choice ?? body.function_call)
  if (toolChoice !== undefined) {
    output.tool_choice = toolChoice
  } else {
    delete output.tool_choice
  }
  const text = chatResponseFormatToResponsesText(body.response_format)
  if (text) {
    output.text = text
  }
  const reasoningEffort = stringValue(body.reasoning_effort)
  if (reasoningEffort) {
    output.reasoning = {
      ...objectValue(body.reasoning),
      effort: reasoningEffort
    }
    delete output.reasoning_effort
  }
  return output
}

function chatMessagesToResponsesInput(value: unknown): unknown[] {
  if (!Array.isArray(value)) return []
  const output: unknown[] = []
  for (const item of value) {
    if (!isPlainObject(item)) continue
    const role = stringValue(item.role)
    if (role === 'tool') {
      const callId = stringValue(item.tool_call_id)
      if (callId) {
        output.push({
          type: 'function_call_output',
          call_id: callId,
          output: chatMessageContentText(item.content)
        })
      }
      continue
    }
    if (role === 'function') {
      const name = stringValue(item.name)
      output.push({
        type: 'function_call_output',
        call_id: name ? `call_${name}` : 'call_function',
        output: chatMessageContentText(item.content)
      })
      continue
    }
    const message = chatMessageToResponsesMessage(item)
    if (message) {
      output.push(message)
    }
    const toolCalls = Array.isArray(item.tool_calls) ? item.tool_calls : []
    for (const toolCall of toolCalls) {
      const functionCall = chatToolCallToResponsesFunctionCall(toolCall)
      if (functionCall) {
        output.push(functionCall)
      }
    }
  }
  return output
}

function chatMessageToResponsesMessage(message: JsonRecord): JsonRecord | undefined {
  const role = stringValue(message.role)
  if (role !== 'system' && role !== 'developer' && role !== 'user' && role !== 'assistant') return undefined
  const content = chatContentToResponsesContent(message.content, role === 'assistant')
  if (content === undefined || (Array.isArray(content) && content.length === 0)) {
    return undefined
  }
  return {
    type: 'message',
    role,
    content
  }
}

function chatContentToResponsesContent(value: unknown, assistant: boolean): unknown {
  if (typeof value === 'string') {
    return value
  }
  if (!Array.isArray(value)) return undefined
  const output: JsonRecord[] = []
  for (const item of value) {
    if (!isPlainObject(item)) continue
    if (item.type === 'text') {
      const text = stringValue(item.text)
      if (text !== undefined) {
        output.push({ type: assistant ? 'output_text' : 'input_text', text })
      }
      continue
    }
    if (item.type === 'image_url') {
      const imageUrl = objectValue(item.image_url)
      const url = stringValue(imageUrl?.url)
      if (url) {
        const block: JsonRecord = { type: 'input_image', image_url: url }
        const detail = stringValue(imageUrl?.detail)
        if (detail) block.detail = detail
        output.push(block)
      }
      continue
    }
    if (item.type === 'file') {
      const file = objectValue(item.file)
      const block: JsonRecord = { type: 'input_file' }
      const fileId = stringValue(file?.file_id)
      const fileData = stringValue(file?.file_data)
      const filename = stringValue(file?.filename)
      if (fileId) block.file_id = fileId
      if (fileData) block.file_data = fileData
      if (filename) block.filename = filename
      if (Object.keys(block).length > 1) output.push(block)
      continue
    }
    output.push({ ...item })
  }
  return output
}

function chatToolCallToResponsesFunctionCall(value: unknown): JsonRecord | undefined {
  if (!isPlainObject(value)) return undefined
  const fn = objectValue(value.function)
  const name = stringValue(fn?.name)
  if (!name) return undefined
  return {
    type: 'function_call',
    id: stringValue(value.id),
    call_id: stringValue(value.id) ?? `call_${name}`,
    name,
    arguments: stringValue(fn?.arguments) ?? ''
  }
}

function chatToolsToResponsesTools(tools: unknown, functions: unknown): JsonRecord[] {
  const output: JsonRecord[] = []
  if (Array.isArray(tools)) {
    for (const item of tools) {
      const converted = chatToolToResponsesTool(item)
      if (converted) output.push(converted)
    }
  }
  if (Array.isArray(functions)) {
    for (const item of functions) {
      const converted = legacyFunctionToResponsesTool(item)
      if (converted) output.push(converted)
    }
  }
  return output
}

function chatToolToResponsesTool(value: unknown): JsonRecord | undefined {
  if (!isPlainObject(value)) return undefined
  if (value.type !== 'function') return { ...value }
  return legacyFunctionToResponsesTool(value.function)
}

function legacyFunctionToResponsesTool(value: unknown): JsonRecord | undefined {
  if (!isPlainObject(value)) return undefined
  const name = stringValue(value.name)
  if (!name) return undefined
  const output: JsonRecord = {
    type: 'function',
    name,
    parameters: isPlainObject(value.parameters) ? value.parameters : { type: 'object', properties: {} }
  }
  const description = stringValue(value.description)
  if (description) output.description = description
  if (typeof value.strict === 'boolean') output.strict = value.strict
  return output
}

function chatToolChoiceToResponsesToolChoice(value: unknown): unknown {
  if (value === undefined || value === null) return undefined
  if (typeof value === 'string') return value
  if (!isPlainObject(value)) return undefined
  if (value.type === 'function') {
    const name = stringValue(objectValue(value.function)?.name)
    return name ? { type: 'function', name } : undefined
  }
  const name = stringValue(value.name)
  return name ? { type: 'function', name } : { ...value }
}

function chatResponseFormatToResponsesText(value: unknown): JsonRecord | undefined {
  if (!isPlainObject(value)) return undefined
  if (value.type === 'json_object') {
    return { format: { type: 'json_object' } }
  }
  if (value.type === 'json_schema') {
    const schema = objectValue(value.json_schema)
    if (!schema) return undefined
    return {
      format: {
        type: 'json_schema',
        name: stringValue(schema.name) ?? 'response_schema',
        strict: typeof schema.strict === 'boolean' ? schema.strict : undefined,
        schema: isPlainObject(schema.schema) ? schema.schema : {}
      }
    }
  }
  return undefined
}

async function * transformResponsesJsonToChatCompletionsJson(
  body: AsyncIterable<Uint8Array>,
  fallbackModel: string
): AsyncIterable<Uint8Array> {
  const parsed = await readJsonBody(body)
  const chat = responsesJsonToChatCompletion(parsed, fallbackModel)
  yield Buffer.from(JSON.stringify(chat), 'utf8')
}

async function * transformResponsesSseToChatCompletionsSse(
  upstreamBody: AsyncIterable<Uint8Array>,
  fallbackModel: string
): AsyncIterable<Uint8Array> {
  const state = createChatResponsesStreamState(fallbackModel)
  const decoder = new StringDecoder('utf8')
  let pending = ''
  for await (const chunk of upstreamBody) {
    pending += decoder.write(Buffer.from(chunk))
    const events = takeCompleteSseEvents(pending)
    pending = events.rest
    for (const eventText of events.events) {
      for (const output of processResponsesSseEvent(state, eventText)) {
        yield Buffer.from(output, 'utf8')
      }
    }
  }
  pending += decoder.end()
  if (pending.trim()) {
    for (const output of processResponsesSseEvent(state, pending)) {
      yield Buffer.from(output, 'utf8')
    }
  }
  if (!state.completed && !state.failed) {
    for (const output of failChatStream(state, '上游 Responses SSE 在完成事件前中断', 'upstream_stream_interrupted')) {
      yield Buffer.from(output, 'utf8')
    }
  }
}

function responsesJsonToChatCompletion(value: unknown, fallbackModel: string): JsonRecord {
  const root = objectValue(value) ?? {}
  const created = integerValue(root.created_at) ?? Math.floor(Date.now() / 1000)
  const output = Array.isArray(root.output) ? root.output : []
  const text = stringValue(root.output_text) ?? outputTextFromResponsesOutput(output)
  const toolCalls = chatToolCallsFromResponsesOutput(output)
  const message: JsonRecord = {
    role: 'assistant',
    content: toolCalls.length > 0 ? null : text
  }
  if (toolCalls.length > 0) {
    message.tool_calls = toolCalls
  }
  const reasoningText = reasoningTextFromResponsesOutput(output)
  if (reasoningText) {
    message.reasoning_content = reasoningText
  }
  return {
    id: stringValue(root.id) ?? `chatcmpl_resp_${created}`,
    object: 'chat.completion',
    created,
    model: stringValue(root.model) ?? fallbackModel,
    choices: [{
      index: 0,
      message,
      finish_reason: toolCalls.length > 0 ? 'tool_calls' : finishReasonFromResponsesStatus(root.status)
    }],
    usage: responsesUsageToChatUsage(objectValue(root.usage))
  }
}

function processResponsesSseEvent(state: ChatResponsesStreamState, rawEventText: string): string[] {
  if (state.completed || state.failed) return []
  const event = parseSseEvent(rawEventText)
  if (!event) return []
  if (event.dataText === '[DONE]') {
    return completeChatStream(state)
  }
  if (event.dataParseError) {
    return failChatStream(state, '上游 Responses SSE 返回了无法解析的事件', 'upstream_stream_parse_error')
  }
  const data = event.data
  const type = stringValue(data?.type) ?? event.eventName
  if (!data) return []
  if (type === 'error' || event.eventName === 'error') {
    const error = objectValue(data.error) ?? data
    return failChatStream(
      state,
      stringValue(error.message) ?? '上游 Responses SSE 返回错误事件',
      stringValue(error.code) ?? stringValue(error.type) ?? 'upstream_error',
      stringValue(error.type) ?? 'upstream_error'
    )
  }
  const response = objectValue(data.response)
  if (response) {
    state.id = stringValue(response.id) ?? state.id
    state.model = stringValue(response.model) ?? state.model
    const usage = objectValue(response.usage)
    const chatUsage = responsesUsageToChatUsage(usage)
    if (chatUsage) state.usage = chatUsage
  }
  if (type === 'response.output_text.delta') {
    const delta = stringValue(data.delta)
    return delta ? appendChatTextDelta(state, delta) : []
  }
  if (type === 'response.output_item.added') {
    return addChatToolCallFromResponsesItem(state, integerValue(data.output_index), objectValue(data.item))
  }
  if (type === 'response.function_call_arguments.delta') {
    return appendChatToolCallArgumentsDelta(state, integerValue(data.output_index), stringValue(data.delta) ?? '')
  }
  if (type === 'response.output_item.done') {
    return doneChatToolCallFromResponsesItem(state, integerValue(data.output_index), objectValue(data.item))
  }
  if (type === 'response.failed') {
    const error = objectValue(response?.error)
    return failChatStream(
      state,
      stringValue(error?.message) ?? '上游 Responses 返回失败事件',
      stringValue(error?.code) ?? 'upstream_response_failed'
    )
  }
  if (type === 'response.completed') {
    return completeChatStream(state)
  }
  return []
}

function createChatResponsesStreamState(model: string): ChatResponsesStreamState {
  const createdAt = Math.floor(Date.now() / 1000)
  return {
    id: `chatcmpl_resp_${createdAt}_${Math.random().toString(36).slice(2, 8)}`,
    createdAt,
    model,
    roleSent: false,
    completed: false,
    failed: false,
    hadToolCall: false,
    toolCalls: new Map(),
    nextToolCallIndex: 0
  }
}

function ensureChatRoleChunk(state: ChatResponsesStreamState): string[] {
  if (state.roleSent) return []
  state.roleSent = true
  return [chatSseChunk(state, { role: 'assistant' })]
}

function appendChatTextDelta(state: ChatResponsesStreamState, delta: string): string[] {
  return [
    ...ensureChatRoleChunk(state),
    chatSseChunk(state, { content: delta })
  ]
}

function addChatToolCallFromResponsesItem(
  state: ChatResponsesStreamState,
  outputIndex: number | undefined,
  item: JsonRecord | undefined
): string[] {
  if (!item || item.type !== 'function_call') return []
  const key = outputIndex ?? state.nextToolCallIndex
  const existing = state.toolCalls.get(key)
  if (existing?.added) return []
  const index = existing?.index ?? state.nextToolCallIndex++
  const toolCall: ChatResponsesStreamToolCallState = existing ?? {
    index,
    id: stringValue(item.call_id) ?? stringValue(item.id) ?? `call_resp_${index}`,
    name: stringValue(item.name) ?? '',
    arguments: '',
    added: false
  }
  toolCall.added = true
  state.toolCalls.set(key, toolCall)
  state.hadToolCall = true
  return [
    ...ensureChatRoleChunk(state),
    chatSseChunk(state, {
      tool_calls: [{
        index: toolCall.index,
        id: toolCall.id,
        type: 'function',
        function: {
          name: toolCall.name,
          arguments: ''
        }
      }]
    })
  ]
}

function appendChatToolCallArgumentsDelta(
  state: ChatResponsesStreamState,
  outputIndex: number | undefined,
  delta: string
): string[] {
  if (!delta) return []
  const key = outputIndex ?? 0
  const toolCall = state.toolCalls.get(key)
  if (!toolCall) return []
  toolCall.arguments += delta
  return [chatSseChunk(state, {
    tool_calls: [{
      index: toolCall.index,
      function: { arguments: delta }
    }]
  })]
}

function doneChatToolCallFromResponsesItem(
  state: ChatResponsesStreamState,
  outputIndex: number | undefined,
  item: JsonRecord | undefined
): string[] {
  if (!item || item.type !== 'function_call') return []
  const added = addChatToolCallFromResponsesItem(state, outputIndex, item)
  const key = outputIndex ?? 0
  const toolCall = state.toolCalls.get(key)
  const fullArguments = stringValue(item.arguments) ?? ''
  if (!toolCall || !fullArguments || fullArguments === toolCall.arguments) return added
  const delta = fullArguments.slice(toolCall.arguments.length)
  toolCall.arguments = fullArguments
  return delta ? [...added, chatSseChunk(state, {
    tool_calls: [{
      index: toolCall.index,
      function: { arguments: delta }
    }]
  })] : added
}

function completeChatStream(state: ChatResponsesStreamState): string[] {
  if (state.completed || state.failed) return []
  state.completed = true
  return [
    ...ensureChatRoleChunk(state),
    chatSseData({
      id: state.id,
      object: 'chat.completion.chunk',
      created: state.createdAt,
      model: state.model,
      choices: [{
        index: 0,
        delta: {},
        finish_reason: state.hadToolCall ? 'tool_calls' : 'stop'
      }],
      usage: state.usage ?? null
    }),
    chatSseDone()
  ]
}

function failChatStream(state: ChatResponsesStreamState, message: string, code: string, type = 'upstream_error'): string[] {
  if (state.completed || state.failed) return []
  state.failed = true
  return [
    chatSseData({
      error: {
        message,
        type,
        code
      }
    }),
    chatSseDone()
  ]
}

function outputTextFromResponsesOutput(output: unknown[]): string {
  const parts: string[] = []
  for (const item of output) {
    if (!isPlainObject(item) || item.type !== 'message') continue
    const content = Array.isArray(item.content) ? item.content : []
    for (const block of content) {
      if (!isPlainObject(block)) continue
      const text = stringValue(block.text)
      if ((block.type === 'output_text' || block.type === 'text') && text) {
        parts.push(text)
      }
    }
  }
  return parts.join('')
}

function reasoningTextFromResponsesOutput(output: unknown[]): string | undefined {
  const parts: string[] = []
  for (const item of output) {
    if (!isPlainObject(item) || item.type !== 'reasoning') continue
    const summary = Array.isArray(item.summary) ? item.summary : []
    for (const block of summary) {
      const text = stringValue(objectValue(block)?.text)
      if (text) parts.push(text)
    }
  }
  return parts.length > 0 ? parts.join('\n') : undefined
}

function chatToolCallsFromResponsesOutput(output: unknown[]): JsonRecord[] {
  const toolCalls: JsonRecord[] = []
  let index = 0
  for (const item of output) {
    if (!isPlainObject(item) || item.type !== 'function_call') continue
    const name = stringValue(item.name)
    if (!name) continue
    toolCalls.push({
      id: stringValue(item.call_id) ?? stringValue(item.id) ?? `call_resp_${index}`,
      type: 'function',
      function: {
        name,
        arguments: stringValue(item.arguments) ?? ''
      }
    })
    index += 1
  }
  return toolCalls
}

function finishReasonFromResponsesStatus(status: unknown): string {
  if (status === 'incomplete') return 'length'
  return 'stop'
}

function responsesUsageToChatUsage(usage: JsonRecord | undefined): JsonRecord | null {
  if (!usage) return null
  const promptTokens = integerValue(usage.input_tokens) ?? 0
  const completionTokens = integerValue(usage.output_tokens) ?? 0
  const totalTokens = integerValue(usage.total_tokens) ?? promptTokens + completionTokens
  const outputDetails = objectValue(usage.output_tokens_details)
  return {
    prompt_tokens: promptTokens,
    completion_tokens: completionTokens,
    total_tokens: totalTokens,
    prompt_tokens_details: {
      cached_tokens: integerValue(objectValue(usage.input_tokens_details)?.cached_tokens) ?? 0
    },
    completion_tokens_details: {
      reasoning_tokens: integerValue(outputDetails?.reasoning_tokens) ?? 0
    }
  }
}

function chatMessageContentText(value: unknown): string {
  if (typeof value === 'string') return value
  if (!Array.isArray(value)) return ''
  return value
    .map((item) => isPlainObject(item) ? stringValue(item.text) ?? '' : '')
    .filter(Boolean)
    .join('\n')
}

async function readJsonBody(body: AsyncIterable<Uint8Array>): Promise<unknown> {
  const chunks: Buffer[] = []
  let total = 0
  for await (const chunk of body) {
    const buffer = Buffer.from(chunk)
    chunks.push(buffer)
    total += buffer.byteLength
    if (total > 16 * 1024 * 1024) {
      throw new Error('chat_responses_bridge_response_too_large')
    }
  }
  const text = Buffer.concat(chunks).toString('utf8')
  return text.trim() ? JSON.parse(text) as unknown : {}
}

function takeCompleteSseEvents(input: string): { events: string[]; rest: string } {
  const events: string[] = []
  let rest = input
  while (true) {
    const match = /\r?\n\r?\n/.exec(rest)
    if (!match || match.index === undefined) {
      return { events, rest }
    }
    const endIndex = match.index + match[0].length
    events.push(rest.slice(0, endIndex))
    rest = rest.slice(endIndex)
  }
}

function parseSseEvent(raw: string): ParsedSseEvent | undefined {
  const eventLines = raw.split(/\r?\n/)
  let eventName: string | undefined
  const dataLines: string[] = []
  for (const line of eventLines) {
    if (!line || line.startsWith(':')) continue
    if (line.startsWith('event:')) {
      eventName = line.slice('event:'.length).trim()
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice('data:'.length).trimStart())
    }
  }
  if (!eventName && dataLines.length === 0) return undefined
  const dataText = dataLines.join('\n')
  if (!dataText || dataText === '[DONE]') return { eventName, dataText }
  try {
    const data = JSON.parse(dataText) as unknown
    return {
      eventName,
      dataText,
      data: isPlainObject(data) ? data : undefined,
      dataParseError: !isPlainObject(data)
    }
  } catch {
    return { eventName, dataText, dataParseError: true }
  }
}

function chatSseChunk(state: ChatResponsesStreamState, delta: JsonRecord): string {
  return chatSseData({
    id: state.id,
    object: 'chat.completion.chunk',
    created: state.createdAt,
    model: state.model,
    choices: [{
      index: 0,
      delta,
      finish_reason: null
    }],
    usage: null
  })
}

function chatSseData(data: JsonRecord): string {
  return `data: ${JSON.stringify(data)}\n\n`
}

function chatSseDone(): string {
  return 'data: [DONE]\n\n'
}

function isPlainObject(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function objectValue(value: unknown): JsonRecord | undefined {
  return isPlainObject(value) ? value : undefined
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.length > 0 ? value : undefined
}

function integerValue(value: unknown): number | undefined {
  const number = typeof value === 'number'
    ? value
    : typeof value === 'string' && value.trim() ? Number(value) : NaN
  if (!Number.isFinite(number)) return undefined
  return Math.trunc(number)
}
