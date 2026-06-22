import { StringDecoder } from 'node:string_decoder'
import type { Request } from 'express'

import type { AccountSupportedEndpointMode, ClientCompatibilityCapability } from '../../../../domain/types.js'
import {
  getGatewayRequestBodyState,
  gatewayJsonBodyInlineParseMaxBytes,
  type GatewayRawBodyRequest
} from '../../../gateway/request/body.js'
import { GatewayRequestValidationError } from '../../../gateway/request/validation-error.js'
import {
  isGatewayJsonWorkerQueueFullError,
  parseGatewayJsonBodyInWorker
} from '../../../gateway/request/json-parser.js'
import { requestStream } from '../../../gateway/request/metadata.js'
import {
  estimateTokenCountFromText,
  parseOpenAISseEventText
} from '../../../gateway/protocols/openai-v1/stream-events.js'
import type { GatewayUpstreamResponse } from '../../../gateway/upstream/request.js'
import { splitPathAndQuery } from '../../../gateway/protocols/openai-v1/route-helpers.js'
import type { CodexResponsesChatBridgeCompletionHandler } from '../../../gateway/codex-responses/chat-bridge-state.js'

type JsonRecord = Record<string, unknown>
const codexResponsesChatBridgeLocalValidationUrl = 'codex-responses-chat-bridge:local-validation'

interface CodexResponsesChatBridgeRequestOptions {
  enabled: boolean
  requestClientCompatibility?: ClientCompatibilityCapability
}

interface BuildCodexResponsesChatBridgeBodyOptions {
  defaultModel: string
  includeReasoningContent?: boolean
  modelOverride?: string
}

interface TransformCodexResponsesChatBridgeResponseOptions extends CodexResponsesChatBridgeRequestOptions {
  defaultModel: string
  idPrefix?: string
  model?: string
  previousResponseId?: string
  onCompleted?: CodexResponsesChatBridgeCompletionHandler
}

interface ChatToolCallState {
  id: string
  callId: string
  name: string
  arguments: string
  outputIndex: number
  added: boolean
  done: boolean
}

interface PendingChatToolCall {
  callId: string
  name: string
  arguments: string
  output?: string
}

interface PendingChatToolCallGroup {
  calls: PendingChatToolCall[]
  deferredMessages: JsonRecord[]
  reasoningText: string
}

interface ChatToResponsesState {
  responseId: string
  messageId: string
  reasoningId: string
  previousResponseId?: string
  idPrefix: string
  createdAt: number
  model: string
  started: boolean
  nextOutputIndex: number
  reasoningOutputIndex?: number
  reasoningStarted: boolean
  reasoningDone: boolean
  reasoningText: string
  textOutputIndex?: number
  textStarted: boolean
  textDone: boolean
  outputText: string
  outputItems: JsonRecord[]
  toolCalls: Map<number, ChatToolCallState>
  usage?: JsonRecord
  estimatedInputTokens?: number
  completed: boolean
  failed: boolean
  terminalReceived: boolean
  completionNotified: boolean
}

export function isCodexResponsesChatBridgeRequest(
  req: Request,
  options: CodexResponsesChatBridgeRequestOptions
): boolean {
  return options.enabled
    && options.requestClientCompatibility === 'codex_responses'
    && requestStream(req)
    && isOpenAIResponsesPostRequest(req)
}

export function isCodexResponsesChatBridgeCandidateRequest(
  req: Request,
  enabled: boolean
): boolean {
  return enabled && isOpenAIResponsesPostRequest(req)
}

export function isCodexResponsesChatBridgeUnsupportedCompactRequest(
  req: Request,
  options: CodexResponsesChatBridgeRequestOptions
): boolean {
  return options.enabled
    && options.requestClientCompatibility === 'codex_responses'
    && isOpenAIResponsesCompactPostRequest(req)
}

export function isCodexResponsesChatBridgeUnsupportedCompactCandidateRequest(
  req: Request,
  enabled: boolean
): boolean {
  return enabled && isOpenAIResponsesCompactPostRequest(req)
}

export function codexResponsesChatBridgeLocalValidationUpstreamUrl(): string {
  return codexResponsesChatBridgeLocalValidationUrl
}

export function rejectUnsupportedCodexResponsesChatBridgeCompactRequest(): never {
  throw new GatewayRequestValidationError(
    '当前 Chat-only Codex bridge 不支持 /responses/compact；需要使用原生 Responses 账号或供应商显式兼容的 compact 能力',
    'unsupported_codex_bridge_compact'
  )
}

export function codexResponsesChatBridgeUpstreamPath(req: Request): string | undefined {
  if (req.method.toUpperCase() !== 'POST') return undefined
  const { path, query } = splitPathAndQuery(req.originalUrl || req.path || '')
  const requestPath = path.startsWith('/') ? path : `/${path}`
  const normalizedPath = requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
  if (normalizedPath !== '/responses') return undefined
  return `/chat/completions${query}`
}

export async function buildCodexResponsesChatBridgeBody(
  req: Request,
  options: BuildCodexResponsesChatBridgeBodyOptions,
  signal?: AbortSignal
): Promise<Buffer> {
  const body = await parseGatewayJsonObject(req, signal)
  validateCodexResponsesChatBridgeBody(body)
  const chatBody: JsonRecord = {
    model: options.modelOverride ?? stringValue(body.model) ?? options.defaultModel,
    messages: responsesInputToChatMessages(body, {
      includeReasoningContent: options.includeReasoningContent === true
    }),
    stream: true
  }

  const tools = responsesToolsToChatTools(body.tools)
  if (tools.length > 0) {
    chatBody.tools = tools
    const toolChoice = responsesToolChoiceToChatToolChoice(body.tool_choice)
    if (toolChoice !== undefined) {
      chatBody.tool_choice = toolChoice
    }
    if (typeof body.parallel_tool_calls === 'boolean') {
      chatBody.parallel_tool_calls = body.parallel_tool_calls
    }
  }

  const maxTokens = integerValue(body.max_output_tokens) ?? integerValue(body.max_completion_tokens)
  if (maxTokens !== undefined) {
    chatBody.max_tokens = maxTokens
  }
  if (typeof body.temperature === 'number') {
    chatBody.temperature = body.temperature
  }
  if (typeof body.top_p === 'number') {
    chatBody.top_p = body.top_p
  }

  return Buffer.from(JSON.stringify(chatBody), 'utf8')
}

export function prepareCodexResponsesChatBridgeHeaders(headers: Headers): void {
  headers.set('accept', 'text/event-stream')
  headers.set('content-type', 'application/json')
  headers.delete('openai-beta')
  headers.delete('originator')
  headers.delete('session-id')
  headers.delete('thread-id')
  headers.delete('x-client-request-id')
  headers.delete('x-codex-beta-features')
  headers.delete('x-codex-turn-metadata')
  headers.delete('x-codex-window-id')
}

export function transformCodexResponsesChatBridgeUpstreamResponse(
  req: Request,
  response: GatewayUpstreamResponse,
  options: TransformCodexResponsesChatBridgeResponseOptions
): GatewayUpstreamResponse {
  if (!response.ok || !response.body || !isCodexResponsesChatBridgeRequest(req, options)) {
    return response
  }
  const headers = new Headers(response.headers)
  headers.set('content-type', 'text/event-stream; charset=utf-8')
  headers.delete('content-length')
  return {
    status: response.status,
    ok: response.ok,
    headers,
    body: transformChatCompletionsSseToResponsesSse(response.body, {
      idPrefix: options.idPrefix,
      estimatedInputTokens: estimateResponsesRequestInputTokens(req),
      model: options.model ?? stringValue((req.body as JsonRecord | undefined)?.model) ?? options.defaultModel,
      previousResponseId: options.previousResponseId,
      onCompleted: options.onCompleted
    })
  }
}

export function isOpenAIResponsesPostRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') return false
  const { path } = splitPathAndQuery(req.originalUrl || req.path || '')
  return (path.replace(/^\/v1(?=\/|$)/, '') || '/') === '/responses'
}

export function isOpenAIResponsesCompactPostRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') return false
  const { path } = splitPathAndQuery(req.originalUrl || req.path || '')
  return (path.replace(/^\/v1(?=\/|$)/, '') || '/') === '/responses/compact'
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
      'Codex Responses 到 Chat 桥接要求请求体是有效 JSON 对象',
      'invalid_codex_bridge_json_body'
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
      'Codex Responses 到 Chat 桥接要求请求体是有效 JSON 对象',
      'invalid_codex_bridge_json_body'
    )
  }
  if (!isPlainObject(parsed)) {
    throw new GatewayRequestValidationError(
      'Codex Responses 到 Chat 桥接要求请求体是 JSON 对象',
      'invalid_codex_bridge_json_body'
    )
  }
  return { ...parsed }
}

function validateCodexResponsesChatBridgeBody(body: JsonRecord): void {
  if (stringValue(body.previous_response_id)) {
    throw new GatewayRequestValidationError(
      'previous_response_id 尚未被网关上下文状态层恢复，不能继续无状态转发',
      'codex_bridge_previous_response_state_unavailable'
    )
  }
}

function responsesInputToChatMessages(
  body: JsonRecord,
  options: { includeReasoningContent?: boolean } = {}
): JsonRecord[] {
  const messages: JsonRecord[] = []
  const instructions = stringValue(body.instructions)
  if (instructions) {
    messages.push({ role: 'system', content: instructions })
  }
  let pendingToolGroup = createPendingChatToolCallGroup()
  const input = body.input
  if (typeof input === 'string') {
    messages.push({ role: 'user', content: input })
  } else if (Array.isArray(input)) {
    for (const item of input) {
      pendingToolGroup = appendResponsesInputItemAsChatMessage(messages, pendingToolGroup, item, options)
    }
  }
  flushPendingChatToolCallGroup(messages, pendingToolGroup, options)
  if (messages.length === 0) {
    messages.push({ role: 'user', content: '' })
  }
  return coalesceAdjacentSystemMessages(messages)
}

function createPendingChatToolCallGroup(): PendingChatToolCallGroup {
  return {
    calls: [],
    deferredMessages: [],
    reasoningText: ''
  }
}

function appendResponsesInputItemAsChatMessage(
  messages: JsonRecord[],
  pendingToolGroup: PendingChatToolCallGroup,
  item: unknown,
  options: { includeReasoningContent?: boolean }
): PendingChatToolCallGroup {
  if (!isPlainObject(item)) return pendingToolGroup
  if (item.type === 'compaction_summary') {
    const summary = responsesCompactionSummaryTextFromItem(item)
    if (!summary) return pendingToolGroup
    if (pendingToolGroup.calls.length > 0) {
      flushPendingChatToolCallGroup(messages, pendingToolGroup, options)
      pendingToolGroup = createPendingChatToolCallGroup()
    }
    messages.push({
      role: 'system',
      content: `上下文摘要：\n${summary}`
    })
    return pendingToolGroup
  }
  if (item.type === 'reasoning') {
    const reasoningText = responsesReasoningTextFromItem(item)
    if (reasoningText && options.includeReasoningContent === true) {
      pendingToolGroup.reasoningText = appendTextBlock(pendingToolGroup.reasoningText, reasoningText)
    }
    return pendingToolGroup
  }
  if (item.type === 'function_call') {
    const name = stringValue(item.name)
    const callId = stringValue(item.call_id) ?? stringValue(item.id)
    if (!name || !callId) return pendingToolGroup
    if (pendingToolGroup.calls.length > 0 && pendingToolCallsAllAnswered(pendingToolGroup)) {
      flushPendingChatToolCallGroup(messages, pendingToolGroup, options)
      pendingToolGroup = createPendingChatToolCallGroup()
    }
    pendingToolGroup.calls.push({
      callId,
      name,
      arguments: stringValue(item.arguments) ?? ''
    })
    return pendingToolGroup
  }
  if (item.type === 'function_call_output') {
    const callId = stringValue(item.call_id)
    if (!callId) return pendingToolGroup
    const call = pendingToolGroup.calls.find((candidate) => candidate.callId === callId && candidate.output === undefined)
    if (!call) return pendingToolGroup
    call.output = responsesTextFromValue(item.output)
    return pendingToolGroup
  }
  if (item.type !== 'message') return pendingToolGroup
  const message = responsesMessageItemAsChatMessage(item)
  if (!message) return pendingToolGroup
  if (pendingToolGroup.calls.length > 0) {
    if (pendingToolCallsAllAnswered(pendingToolGroup)) {
      flushPendingChatToolCallGroup(messages, pendingToolGroup, options)
      messages.push(message)
      return createPendingChatToolCallGroup()
    }
    pendingToolGroup.deferredMessages.push(message)
    return pendingToolGroup
  }
  messages.push(message)
  return createPendingChatToolCallGroup()
}

function pendingToolCallsAllAnswered(pendingToolGroup: PendingChatToolCallGroup): boolean {
  return pendingToolGroup.calls.length > 0
    && pendingToolGroup.calls.every((call) => call.output !== undefined)
}

function flushPendingChatToolCallGroup(
  messages: JsonRecord[],
  pendingToolGroup: PendingChatToolCallGroup,
  options: { includeReasoningContent?: boolean }
): void {
  const answeredCalls = pendingToolGroup.calls.filter((call) => call.output !== undefined)
  if (answeredCalls.length > 0) {
    const assistantMessage: JsonRecord = {
      role: 'assistant',
      content: null,
      tool_calls: answeredCalls.map((call) => ({
        id: call.callId,
        type: 'function',
        function: {
          name: call.name,
          arguments: call.arguments
        }
      }))
    }
    if (options.includeReasoningContent === true && pendingToolGroup.reasoningText) {
      assistantMessage.reasoning_content = pendingToolGroup.reasoningText
    }
    messages.push(assistantMessage)
    for (const call of answeredCalls) {
      messages.push({
        role: 'tool',
        tool_call_id: call.callId,
        content: call.output ?? ''
      })
    }
  }
  messages.push(...pendingToolGroup.deferredMessages)
}

function responsesMessageItemAsChatMessage(item: JsonRecord): JsonRecord | undefined {
  const role = chatRoleForResponsesRole(item.role)
  if (!role) return undefined
  const content = responsesContentFromValue(item.content)
  return { role, content }
}

function chatRoleForResponsesRole(role: unknown): string | undefined {
  if (role === 'developer' || role === 'system') return 'system'
  if (role === 'user' || role === 'assistant') return role
  return undefined
}

function coalesceAdjacentSystemMessages(messages: JsonRecord[]): JsonRecord[] {
  const output: JsonRecord[] = []
  for (const message of messages) {
    const previous = output[output.length - 1]
    if (message.role === 'system' && previous?.role === 'system') {
      previous.content = `${String(previous.content ?? '')}\n\n${String(message.content ?? '')}`.trim()
    } else {
      output.push(message)
    }
  }
  return output
}

function responsesTextFromValue(value: unknown): string {
  if (typeof value === 'string') return value
  if (!Array.isArray(value)) return ''
  const parts: string[] = []
  for (const item of value) {
    if (!isPlainObject(item)) continue
    const text = stringValue(item.text)
    if (
      text
      && (
        item.type === 'input_text'
        || item.type === 'output_text'
        || item.type === 'text'
        || item.type === undefined
      )
    ) {
      parts.push(text)
    }
  }
  return parts.join('\n')
}

function responsesReasoningTextFromItem(item: JsonRecord): string {
  const parts: string[] = []
  for (const value of [item.summary, item.content]) {
    if (!Array.isArray(value)) continue
    for (const part of value) {
      if (!isPlainObject(part)) continue
      const text = stringValue(part.text)
      if (text) parts.push(text)
    }
  }
  return parts.join('\n')
}

function responsesCompactionSummaryTextFromItem(item: JsonRecord): string {
  const encryptedContent = stringValue(item.encrypted_content)
  if (!encryptedContent) return ''
  const prefix = 'juhecmp.v1.'
  if (!encryptedContent.startsWith(prefix)) {
    return encryptedContent
  }
  const encoded = encryptedContent.slice(prefix.length)
  try {
    const parsed = JSON.parse(Buffer.from(encoded, 'base64url').toString('utf8')) as unknown
    if (isPlainObject(parsed)) {
      return stringValue(parsed.summary) ?? ''
    }
  } catch {
  }
  return ''
}

function appendTextBlock(base: string, next: string): string {
  if (!base) return next
  if (!next) return base
  return `${base}\n${next}`
}

function responsesContentFromValue(value: unknown): unknown {
  if (typeof value === 'string') return value
  if (!Array.isArray(value)) return ''
  const parts: JsonRecord[] = []
  for (const item of value) {
    if (!isPlainObject(item)) continue
    const text = stringValue(item.text)
    if (
      text
      && (
        item.type === 'input_text'
        || item.type === 'output_text'
        || item.type === 'text'
        || item.type === undefined
      )
    ) {
      parts.push({ type: 'text', text })
      continue
    }
    if (item.type === 'input_image') {
      const imageUrl = stringValue(item.image_url)
      if (!imageUrl) continue
      const imageUrlPayload: JsonRecord = { url: imageUrl }
      const detail = stringValue(item.detail)
      if (detail) {
        imageUrlPayload.detail = detail
      }
      parts.push({
        type: 'image_url',
        image_url: imageUrlPayload
      })
    }
  }
  if (!parts.length) return ''
  if (parts.every((item) => item.type === 'text')) {
    return parts.map((item) => stringValue(item.text) ?? '').filter(Boolean).join('\n')
  }
  return parts
}

function responsesToolsToChatTools(value: unknown): JsonRecord[] {
  if (!Array.isArray(value)) return []
  const tools: JsonRecord[] = []
  for (const item of value) {
    if (!isPlainObject(item) || item.type !== 'function') continue
    const name = stringValue(item.name)
    if (!name) continue
    const tool: JsonRecord = {
      type: 'function',
      function: {
        name,
        description: stringValue(item.description) ?? '',
        parameters: isPlainObject(item.parameters) ? item.parameters : { type: 'object', properties: {} }
      }
    }
    const strict = item.strict
    if (typeof strict === 'boolean') {
      ;(tool.function as JsonRecord).strict = strict
    }
    tools.push(tool)
  }
  return tools
}

function responsesToolChoiceToChatToolChoice(value: unknown): unknown {
  if (value === undefined || value === null) return undefined
  if (typeof value === 'string') return value
  if (!isPlainObject(value)) return undefined
  if (value.type === 'function') {
    const name = stringValue(value.name)
    return name ? { type: 'function', function: { name } } : undefined
  }
  return value
}

async function * transformChatCompletionsSseToResponsesSse(
  upstreamBody: AsyncIterable<Uint8Array>,
  input: {
    estimatedInputTokens?: number
    idPrefix?: string
    model: string
    previousResponseId?: string
    onCompleted?: CodexResponsesChatBridgeCompletionHandler
  }
): AsyncIterable<Uint8Array> {
  const decoder = new StringDecoder('utf8')
  const state = createChatToResponsesState(input.model, input.idPrefix, input.estimatedInputTokens, input.previousResponseId)
  let pending = ''
  for await (const chunk of upstreamBody) {
    pending += decoder.write(Buffer.from(chunk))
    const events = takeCompleteSseEvents(pending)
    pending = events.rest
    for (const eventText of events.events) {
      const outputs = processChatSseEvent(state, eventText)
      for (const output of outputs) {
        yield Buffer.from(output, 'utf8')
      }
      await notifyCodexResponsesChatBridgeCompletion(state, input.onCompleted)
    }
  }
  pending += decoder.end()
  if (pending.trim()) {
    const outputs = processChatSseEvent(state, pending)
    for (const output of outputs) {
      yield Buffer.from(output, 'utf8')
    }
    await notifyCodexResponsesChatBridgeCompletion(state, input.onCompleted)
  }
  const finalOutputs = state.terminalReceived || state.completed || state.failed
    ? []
    : failResponsesStream(state, '上游 Chat SSE 在正常结束事件前中断', 'upstream_stream_interrupted')
  for (const output of finalOutputs) {
    yield Buffer.from(output, 'utf8')
  }
  await notifyCodexResponsesChatBridgeCompletion(state, input.onCompleted)
}

function createChatToResponsesState(model: string, idPrefix = 'chat_bridge', estimatedInputTokens?: number, previousResponseId?: string): ChatToResponsesState {
  const suffix = `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`
  return {
    responseId: `resp_${idPrefix}_${suffix}`,
    messageId: `msg_${idPrefix}_${suffix}`,
    reasoningId: `rs_${idPrefix}_${suffix}`,
    previousResponseId,
    idPrefix,
    createdAt: Math.floor(Date.now() / 1000),
    model,
    started: false,
    nextOutputIndex: 0,
    reasoningStarted: false,
    reasoningDone: false,
    reasoningText: '',
    textStarted: false,
    textDone: false,
    outputText: '',
    outputItems: [],
    toolCalls: new Map(),
    estimatedInputTokens,
    completed: false,
    failed: false,
    terminalReceived: false,
    completionNotified: false
  }
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

function processChatSseEvent(state: ChatToResponsesState, rawEventText: string): string[] {
  if (state.completed || state.failed) return []
  const event = parseOpenAISseEventText(rawEventText)
  if (event.dataText === '[DONE]') {
    state.terminalReceived = true
    return completeResponsesStream(state)
  }
  if (event.dataParseError) {
    return failResponsesStream(state, '上游 Chat SSE 返回了无法解析的事件', 'upstream_stream_parse_error')
  }
  if (event.eventName === 'error' || event.eventType === 'error') {
    return failResponsesStream(
      state,
      event.errorMessage ?? '上游 Chat SSE 返回错误事件',
      event.errorCode ?? 'upstream_stream_error'
    )
  }
  const data = event.data
  if (!data) return []
  const error = objectValue(data.error)
  if (error) {
    return failResponsesStream(
      state,
      stringValue(error.message) ?? event.errorMessage ?? '上游 Chat SSE 返回错误对象',
      stringValue(error.code) ?? stringValue(error.type) ?? event.errorCode ?? 'upstream_stream_error'
    )
  }
  const output: string[] = ensureResponsesStreamStarted(state)
  state.model = stringValue(data.model) ?? state.model
  const usage = objectValue(data.usage)
  if (usage) {
    state.usage = chatUsageToResponsesUsage(usage)
  }
  const choices = Array.isArray(data.choices) ? data.choices : []
  for (const choice of choices) {
    if (!isPlainObject(choice)) continue
    const delta = objectValue(choice.delta)
    if (delta) {
      const reasoningText = stringValue(delta.reasoning_content) ?? stringValue(delta.reasoning)
      if (reasoningText) {
        output.push(...appendResponsesReasoningDelta(state, reasoningText))
      }
      const text = stringValue(delta.content) ?? stringValue(delta.refusal)
      if (text) {
        output.push(...appendResponsesTextDelta(state, text))
      }
      const toolCalls = Array.isArray(delta.tool_calls) ? delta.tool_calls : []
      for (const toolCall of toolCalls) {
        output.push(...appendResponsesToolCallDelta(state, toolCall))
      }
    }
    if (typeof choice.finish_reason === 'string') {
      state.terminalReceived = true
      output.push(...completeOpenOutputItems(state))
      output.push(...completeResponsesStream(state))
    }
  }
  return output
}

function ensureResponsesStreamStarted(state: ChatToResponsesState): string[] {
  if (state.started) return []
  state.started = true
  return [
    sse('response.created', {
      type: 'response.created',
      response: responseSnapshot(state, 'in_progress', [])
    }),
    sse('response.in_progress', {
      type: 'response.in_progress',
      response: responseSnapshot(state, 'in_progress', [])
    })
  ]
}

function appendResponsesReasoningDelta(state: ChatToResponsesState, text: string): string[] {
  const output: string[] = []
  if (!state.reasoningStarted) {
    state.reasoningStarted = true
    state.reasoningOutputIndex = state.nextOutputIndex++
    output.push(sse('response.output_item.added', {
      type: 'response.output_item.added',
      output_index: state.reasoningOutputIndex,
      item: {
        id: state.reasoningId,
        type: 'reasoning',
        status: 'in_progress',
        summary: []
      }
    }))
  }
  state.reasoningText += text
  output.push(sse('response.reasoning_summary_text.delta', {
    type: 'response.reasoning_summary_text.delta',
    item_id: state.reasoningId,
    output_index: state.reasoningOutputIndex ?? 0,
    summary_index: 0,
    delta: text
  }))
  return output
}

function appendResponsesTextDelta(state: ChatToResponsesState, text: string): string[] {
  const output: string[] = []
  if (!state.textStarted) {
    state.textStarted = true
    state.textOutputIndex = state.nextOutputIndex++
    output.push(sse('response.output_item.added', {
      type: 'response.output_item.added',
      output_index: state.textOutputIndex,
      item: {
        id: state.messageId,
        type: 'message',
        status: 'in_progress',
        role: 'assistant',
        content: []
      }
    }))
    output.push(sse('response.content_part.added', {
      type: 'response.content_part.added',
      item_id: state.messageId,
      output_index: state.textOutputIndex,
      content_index: 0,
      part: {
        type: 'output_text',
        text: '',
        annotations: []
      }
    }))
  }
  state.outputText += text
  output.push(sse('response.output_text.delta', {
    type: 'response.output_text.delta',
    item_id: state.messageId,
    output_index: state.textOutputIndex ?? 0,
    content_index: 0,
    delta: text
  }))
  return output
}

function appendResponsesToolCallDelta(state: ChatToResponsesState, value: unknown): string[] {
  if (!isPlainObject(value)) return []
  const index = integerValue(value.index) ?? 0
  let toolCall = state.toolCalls.get(index)
  if (!toolCall) {
    const callId = stringValue(value.id) ?? `call_${state.idPrefix}_${index}_${Date.now().toString(36)}`
    toolCall = {
      id: `fc_${state.idPrefix}_${index}_${Date.now().toString(36)}`,
      callId,
      name: '',
      arguments: '',
      outputIndex: state.nextOutputIndex++,
      added: false,
      done: false
    }
    state.toolCalls.set(index, toolCall)
  }
  const fn = objectValue(value.function)
  toolCall.name = stringValue(fn?.name) ?? toolCall.name
  const argumentsDelta = stringValue(fn?.arguments) ?? ''
  const output: string[] = []
  if (!toolCall.added && (toolCall.name || argumentsDelta)) {
    toolCall.added = true
    output.push(sse('response.output_item.added', {
      type: 'response.output_item.added',
      output_index: toolCall.outputIndex,
      item: {
        id: toolCall.id,
        type: 'function_call',
        status: 'in_progress',
        call_id: toolCall.callId,
        name: toolCall.name,
        arguments: ''
      }
    }))
  }
  if (argumentsDelta) {
    toolCall.arguments += argumentsDelta
  }
  return output
}

function completeOpenOutputItems(state: ChatToResponsesState): string[] {
  const output: string[] = []
  if (state.textStarted && !state.textDone) {
    state.textDone = true
    const item = {
      id: state.messageId,
      type: 'message',
      status: 'completed',
      role: 'assistant',
      content: [
        {
          type: 'output_text',
          text: state.outputText,
          annotations: []
        }
      ]
    }
    output.push(sse('response.output_text.done', {
      type: 'response.output_text.done',
      item_id: state.messageId,
      output_index: state.textOutputIndex ?? 0,
      content_index: 0,
      text: state.outputText
    }))
    output.push(sse('response.content_part.done', {
      type: 'response.content_part.done',
      item_id: state.messageId,
      output_index: state.textOutputIndex ?? 0,
      content_index: 0,
      part: item.content[0]
    }))
    output.push(sse('response.output_item.done', {
      type: 'response.output_item.done',
      output_index: state.textOutputIndex ?? 0,
      item
    }))
    state.outputItems.push(item)
  }
  if (state.reasoningStarted && !state.reasoningDone) {
    state.reasoningDone = true
    const item = {
      id: state.reasoningId,
      type: 'reasoning',
      status: 'completed',
      summary: [
        {
          type: 'summary_text',
          text: state.reasoningText
        }
      ],
      encrypted_content: null
    }
    output.push(sse('response.output_item.done', {
      type: 'response.output_item.done',
      output_index: state.reasoningOutputIndex ?? 0,
      item
    }))
    state.outputItems.push(item)
  }
  for (const toolCall of state.toolCalls.values()) {
    if (toolCall.done) continue
    if (!toolCall.added) {
      toolCall.added = true
      output.push(sse('response.output_item.added', {
        type: 'response.output_item.added',
        output_index: toolCall.outputIndex,
        item: {
          id: toolCall.id,
          type: 'function_call',
          status: 'in_progress',
          call_id: toolCall.callId,
          name: toolCall.name,
          arguments: ''
        }
      }))
    }
    toolCall.done = true
    const item = {
      id: toolCall.id,
      type: 'function_call',
      status: 'completed',
      call_id: toolCall.callId,
      name: toolCall.name,
      arguments: toolCall.arguments
    }
    output.push(sse('response.output_item.done', {
      type: 'response.output_item.done',
      output_index: toolCall.outputIndex,
      item
    }))
    state.outputItems.push(item)
  }
  return output
}

function completeResponsesStream(state: ChatToResponsesState): string[] {
  if (state.completed || state.failed) return []
  const output = ensureResponsesStreamStarted(state)
  output.push(...completeOpenOutputItems(state))
  state.completed = true
  output.push(sse('response.completed', {
    type: 'response.completed',
    response: responseSnapshot(state, 'completed', state.outputItems)
  }))
  return output
}

function failResponsesStream(state: ChatToResponsesState, message: string, code: string): string[] {
  if (state.completed || state.failed) return []
  const output = ensureResponsesStreamStarted(state)
  state.failed = true
  output.push(sse('response.failed', {
    type: 'response.failed',
    response: responseFailedSnapshot(state, message, code)
  }))
  return output
}

function responseSnapshot(state: ChatToResponsesState, status: 'in_progress' | 'completed', output: JsonRecord[]): JsonRecord {
  return {
    id: state.responseId,
    object: 'response',
    created_at: state.createdAt,
    status,
    completed_at: status === 'completed' ? Math.floor(Date.now() / 1000) : null,
    error: null,
    incomplete_details: null,
    instructions: null,
    max_output_tokens: null,
    model: state.model,
    output,
    parallel_tool_calls: false,
    previous_response_id: state.previousResponseId ?? null,
    reasoning: { effort: null, summary: null },
    store: false,
    temperature: null,
    text: { format: { type: 'text' } },
    tool_choice: 'auto',
    tools: [],
    top_p: null,
    truncation: 'disabled',
    usage: status === 'completed' ? completedResponsesUsage(state) : null,
    user: null,
    metadata: {}
  }
}

async function notifyCodexResponsesChatBridgeCompletion(
  state: ChatToResponsesState,
  onCompleted: CodexResponsesChatBridgeCompletionHandler | undefined
): Promise<void> {
  if (!onCompleted || !state.completed || state.completionNotified) return
  state.completionNotified = true
  await onCompleted({
    responseId: state.responseId,
    createdAt: state.createdAt,
    model: state.model,
    outputItems: state.outputItems.map((item) => ({ ...item })),
    response: responseSnapshot(state, 'completed', state.outputItems)
  })
}

function responseFailedSnapshot(state: ChatToResponsesState, message: string, code: string): JsonRecord {
  return {
    ...responseSnapshot(state, 'in_progress', state.outputItems),
    status: 'failed',
    completed_at: Math.floor(Date.now() / 1000),
    error: {
      code,
      message
    }
  }
}

function chatUsageToResponsesUsage(usage: JsonRecord): JsonRecord {
  const inputTokens = integerValue(usage.prompt_tokens) ?? 0
  const outputTokens = integerValue(usage.completion_tokens) ?? 0
  const totalTokens = integerValue(usage.total_tokens) ?? inputTokens + outputTokens
  const completionDetails = objectValue(usage.completion_tokens_details)
  return {
    input_tokens: inputTokens,
    input_tokens_details: {
      cached_tokens: integerValue(objectValue(usage.prompt_tokens_details)?.cached_tokens) ?? 0
    },
    output_tokens: outputTokens,
    output_tokens_details: {
      reasoning_tokens: integerValue(completionDetails?.reasoning_tokens) ?? 0
    },
    total_tokens: totalTokens
  }
}

function completedResponsesUsage(state: ChatToResponsesState): JsonRecord {
  if (state.usage) return state.usage
  const inputTokens = state.estimatedInputTokens ?? 0
  const outputTokens = estimatedBridgeOutputTokens(state)
  return {
    input_tokens: inputTokens,
    input_tokens_details: {
      cached_tokens: 0
    },
    output_tokens: outputTokens,
    output_tokens_details: {
      reasoning_tokens: estimateTokenCountFromText(state.reasoningText)
    },
    total_tokens: inputTokens + outputTokens
  }
}

function estimatedBridgeOutputTokens(state: ChatToResponsesState): number {
  const textParts = [state.outputText, state.reasoningText]
  for (const toolCall of state.toolCalls.values()) {
    textParts.push(toolCall.name, toolCall.arguments)
  }
  const estimated = estimateTokenCountFromText(textParts.join('\n'))
  return Math.max(0, estimated)
}

function sse(event: string, data: JsonRecord): string {
  return `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`
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

function estimateResponsesRequestInputTokens(req: Request): number | undefined {
  const requestWithBody = req as GatewayRawBodyRequest
  if (isPlainObject(req.body)) {
    return estimateTokenCountFromText(JSON.stringify(req.body))
  }
  const rawBody = requestWithBody.rawBody
  if (!rawBody || rawBody.length === 0) return undefined
  return Math.max(1, Math.ceil(rawBody.byteLength / 4))
}

export function codexResponsesChatBridgeRequiredEndpointMode(): AccountSupportedEndpointMode {
  return 'chat_sse'
}
