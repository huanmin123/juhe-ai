import { StringDecoder } from 'node:string_decoder'
import type { Request } from 'express'

import type { AccountSupportedEndpointMode } from '../../../../domain/types.js'
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
import { GatewayAgentGuidanceResponse, GatewayRequestValidationError } from '../../../gateway/request/validation-error.js'
import { splitPathAndQuery } from '../../../gateway/protocols/openai-v1/route-helpers.js'
import { parseOpenAISseEventText } from '../../../gateway/protocols/openai-v1/stream-events.js'
import type { GatewayUpstreamResponse } from '../../../gateway/upstream/request.js'

type JsonRecord = Record<string, unknown>

interface BuildAnthropicMessagesChatBridgeBodyOptions {
  defaultModel: string
  guidanceProviderName?: string
  modelOverride?: string
}

interface TransformAnthropicMessagesChatBridgeResponseOptions {
  enabled: boolean
  model: string
}

interface AnthropicToolChoiceResult {
  value?: JsonRecord | string
  parallelToolCalls?: boolean
}

interface AnthropicChatStreamToolCallState {
  key: number
  blockIndex: number
  id: string
  name: string
  arguments: string
  bufferedArguments: string
  started: boolean
  done: boolean
}

interface AnthropicChatStreamState {
  id: string
  createdAt: number
  model: string
  started: boolean
  completed: boolean
  failed: boolean
  nextContentBlockIndex: number
  textBlockIndex?: number
  textStarted: boolean
  textDone: boolean
  toolCalls: Map<number, AnthropicChatStreamToolCallState>
  usage?: JsonRecord
  stopReason?: string
  hadToolUse: boolean
}

export function isAnthropicMessagesPostRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') return false
  const { path } = splitPathAndQuery(req.originalUrl || req.path || '')
  const normalizedPath = (path.startsWith('/') ? path : `/${path}`).replace(/^\/v1(?=\/|$)/, '') || '/'
  return normalizedPath === '/messages'
}

export function anthropicMessagesChatBridgeUpstreamPath(req: Request): string | undefined {
  if (!isAnthropicMessagesPostRequest(req)) return undefined
  const { query } = splitPathAndQuery(req.originalUrl || req.path || '')
  return `/chat/completions${query}`
}

export function anthropicMessagesChatBridgeRequiredEndpointMode(stream: boolean): AccountSupportedEndpointMode {
  return stream ? 'chat_sse' : 'chat_json'
}

export function prepareAnthropicMessagesChatBridgeHeaders(headers: Headers, req: Request): void {
  headers.set('accept', requestStream(req) ? 'text/event-stream' : 'application/json')
  headers.set('content-type', 'application/json')
  headers.delete('anthropic-beta')
  headers.delete('anthropic-version')
  headers.delete('content-length')
  headers.delete('x-api-key')
}

export async function buildAnthropicMessagesChatBridgeBody(
  req: Request,
  options: BuildAnthropicMessagesChatBridgeBodyOptions,
  signal?: AbortSignal
): Promise<Buffer> {
  const body = await parseGatewayJsonObject(req, signal)
  const model = options.modelOverride ?? stringValue(body.model) ?? options.defaultModel
  validateAnthropicMessagesChatBridgeBody(req, body, model, options.guidanceProviderName)
  const chatBody = anthropicMessagesBodyToChatCompletionsBody(req, body, model, options.guidanceProviderName)
  return Buffer.from(JSON.stringify(chatBody), 'utf8')
}

export function transformAnthropicMessagesChatBridgeUpstreamResponse(
  req: Request,
  response: GatewayUpstreamResponse,
  options: TransformAnthropicMessagesChatBridgeResponseOptions
): GatewayUpstreamResponse {
  if (!options.enabled || !response.ok || !response.body || !isAnthropicMessagesPostRequest(req)) {
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
      body: transformChatCompletionsSseToAnthropicMessagesSse(response.body, options.model)
    }
  }
  headers.set('content-type', 'application/json; charset=utf-8')
  return {
    status: response.status,
    ok: response.ok,
    headers,
    body: transformChatCompletionsJsonToAnthropicMessagesJson(response.body, options.model)
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
      'Anthropic Messages 到 Chat Completions 桥接要求请求体是有效 JSON 对象',
      'invalid_anthropic_chat_bridge_json_body'
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
      'Anthropic Messages 到 Chat Completions 桥接要求请求体是有效 JSON 对象',
      'invalid_anthropic_chat_bridge_json_body'
    )
  }
  if (!isPlainObject(parsed)) {
    throw new GatewayRequestValidationError(
      'Anthropic Messages 到 Chat Completions 桥接要求请求体是 JSON 对象',
      'invalid_anthropic_chat_bridge_json_body'
    )
  }
  return { ...parsed }
}

function validateAnthropicMessagesChatBridgeBody(
  req: Request,
  body: JsonRecord,
  model: string,
  providerName?: string
): void {
  if (!Array.isArray(body.messages)) {
    throw new GatewayRequestValidationError(
      'Anthropic Messages 到 Chat Completions 桥接要求 messages 是数组',
      'invalid_anthropic_chat_bridge_messages'
    )
  }
  const unsupportedFields = [
    'thinking',
    'container',
    'context_management',
    'mcp_servers',
    'service_tier',
    'top_k'
  ].filter((field) => body[field] !== undefined)
  if (unsupportedFields.length > 0) {
    throw anthropicMessagesGuidance(req, model, `当前${providerLabel(providerName)} Chat Completions 上游不支持 Anthropic Messages 的 ${unsupportedFields.join('、')} 字段。请客户端改用真实支持这些能力的上游，或在本地 agent / MCP 中提供对应能力后再发起请求。`, 'unsupported_anthropic_messages_chat_bridge_fields')
  }
}

function anthropicMessagesBodyToChatCompletionsBody(
  req: Request,
  body: JsonRecord,
  model: string,
  providerName?: string
): JsonRecord {
  const chatMessages: JsonRecord[] = []
  appendSystemMessages(req, chatMessages, body.system, model, providerName)
  for (const item of body.messages as unknown[]) {
    const message = objectValue(item)
    if (!message) continue
    appendAnthropicMessage(req, chatMessages, message, model, providerName)
  }

  const output: JsonRecord = {
    model,
    messages: chatMessages,
    stream: requestStream(req)
  }
  const maxTokens = integerValue(body.max_tokens)
  if (maxTokens !== undefined) output.max_tokens = maxTokens
  if (typeof body.temperature === 'number') output.temperature = body.temperature
  if (typeof body.top_p === 'number') output.top_p = body.top_p
  const stop = anthropicStopSequencesToChatStop(body.stop_sequences)
  if (stop !== undefined) output.stop = stop
  const user = stringValue(objectValue(body.metadata)?.user_id)
  if (user) output.user = user
  const tools = anthropicToolsToChatTools(req, body.tools, model, providerName)
  if (tools.length > 0) {
    output.tools = tools
    const toolChoice = anthropicToolChoiceToChatToolChoice(req, body.tool_choice, model, providerName)
    if (toolChoice.value !== undefined) output.tool_choice = toolChoice.value
    if (toolChoice.parallelToolCalls !== undefined) output.parallel_tool_calls = toolChoice.parallelToolCalls
  }
  return output
}

function appendSystemMessages(
  req: Request,
  output: JsonRecord[],
  value: unknown,
  model: string,
  providerName?: string
): void {
  if (value === undefined) return
  if (typeof value === 'string') {
    if (value.trim()) output.push({ role: 'system', content: value })
    return
  }
  if (!Array.isArray(value)) {
    throw new GatewayRequestValidationError(
      'Anthropic Messages 到 Chat Completions 桥接要求 system 是字符串或 text block 数组',
      'invalid_anthropic_chat_bridge_system'
    )
  }
  const text = value.map((item) => {
    const block = objectValue(item)
    if (!block) return ''
    if (block.type !== 'text') {
      throw anthropicMessagesGuidance(req, model, `当前${providerLabel(providerName)} Chat Completions 上游只支持把 system text block 转换为 system 消息。请客户端移除 system 中的非 text block，或改用支持该 Anthropic 能力的上游。`, 'unsupported_anthropic_messages_system_block')
    }
    return stringValue(block.text) ?? ''
  }).filter(Boolean).join('\n')
  if (text) output.push({ role: 'system', content: text })
}

function appendAnthropicMessage(
  req: Request,
  output: JsonRecord[],
  message: JsonRecord,
  model: string,
  providerName?: string
): void {
  const role = stringValue(message.role)
  if (role !== 'user' && role !== 'assistant') {
    throw new GatewayRequestValidationError(
      'Anthropic Messages 到 Chat Completions 桥接只支持 user 和 assistant 消息',
      'invalid_anthropic_chat_bridge_message_role'
    )
  }
  if (role === 'assistant') {
    output.push(anthropicAssistantMessageToChatMessage(req, message, model, providerName))
    return
  }
  appendAnthropicUserMessage(req, output, message, model, providerName)
}

function appendAnthropicUserMessage(
  req: Request,
  output: JsonRecord[],
  message: JsonRecord,
  model: string,
  providerName?: string
): void {
  const content = message.content
  if (typeof content === 'string') {
    output.push({ role: 'user', content })
    return
  }
  if (!Array.isArray(content)) {
    throw new GatewayRequestValidationError(
      'Anthropic Messages 到 Chat Completions 桥接要求 user content 是字符串或 block 数组',
      'invalid_anthropic_chat_bridge_user_content'
    )
  }
  let pendingParts: JsonRecord[] = []
  const flushUserParts = () => {
    if (!pendingParts.length) return
    output.push({
      role: 'user',
      content: chatUserContentFromParts(pendingParts)
    })
    pendingParts = []
  }
  for (const blockValue of content) {
    const block = objectValue(blockValue)
    if (!block) continue
    const type = stringValue(block.type)
    if (type === 'text') {
      pendingParts.push({ type: 'text', text: stringValue(block.text) ?? '' })
      continue
    }
    if (type === 'image') {
      const imageUrl = anthropicImageBlockToChatImageUrl(req, block, model, providerName)
      pendingParts.push({ type: 'image_url', image_url: { url: imageUrl } })
      continue
    }
    if (type === 'tool_result') {
      flushUserParts()
      const toolUseId = stringValue(block.tool_use_id)
      if (!toolUseId) {
        throw new GatewayRequestValidationError(
          'Anthropic tool_result block 缺少 tool_use_id',
          'invalid_anthropic_chat_bridge_tool_result'
        )
      }
      output.push({
        role: 'tool',
        tool_call_id: toolUseId,
        content: anthropicContentText(block.content)
      })
      continue
    }
    throw unsupportedAnthropicContentBlock(req, model, providerName, type)
  }
  flushUserParts()
}

function anthropicAssistantMessageToChatMessage(
  req: Request,
  message: JsonRecord,
  model: string,
  providerName?: string
): JsonRecord {
  const content = message.content
  if (typeof content === 'string') {
    return { role: 'assistant', content }
  }
  if (!Array.isArray(content)) {
    throw new GatewayRequestValidationError(
      'Anthropic Messages 到 Chat Completions 桥接要求 assistant content 是字符串或 block 数组',
      'invalid_anthropic_chat_bridge_assistant_content'
    )
  }
  const textParts: string[] = []
  const toolCalls: JsonRecord[] = []
  for (const blockValue of content) {
    const block = objectValue(blockValue)
    if (!block) continue
    const type = stringValue(block.type)
    if (type === 'text') {
      textParts.push(stringValue(block.text) ?? '')
      continue
    }
    if (type === 'tool_use') {
      const name = stringValue(block.name)
      const id = stringValue(block.id)
      if (!name || !id) {
        throw new GatewayRequestValidationError(
          'Anthropic tool_use block 缺少 id 或 name',
          'invalid_anthropic_chat_bridge_tool_use'
        )
      }
      toolCalls.push({
        id,
        type: 'function',
        function: {
          name,
          arguments: JSON.stringify(objectValue(block.input) ?? {})
        }
      })
      continue
    }
    if (type === 'thinking' || type === 'redacted_thinking') {
      throw anthropicMessagesGuidance(req, model, `当前${providerLabel(providerName)} Chat Completions 上游不能保真承载 Anthropic thinking block。请客户端改用支持 thinking 的 Anthropic Messages 上游，或移除 thinking 内容后重试。`, 'unsupported_anthropic_messages_thinking_block')
    }
    throw unsupportedAnthropicContentBlock(req, model, providerName, type)
  }
  const output: JsonRecord = {
    role: 'assistant',
    content: toolCalls.length > 0 && textParts.join('') === '' ? null : textParts.join('')
  }
  if (toolCalls.length > 0) output.tool_calls = toolCalls
  return output
}

function anthropicImageBlockToChatImageUrl(
  req: Request,
  block: JsonRecord,
  model: string,
  providerName?: string
): string {
  const source = objectValue(block.source)
  const type = stringValue(source?.type)
  if (type === 'base64') {
    const mediaType = stringValue(source?.media_type) ?? 'application/octet-stream'
    const data = stringValue(source?.data)
    if (!data) {
      throw new GatewayRequestValidationError(
        'Anthropic image source 缺少 base64 data',
        'invalid_anthropic_chat_bridge_image'
      )
    }
    return `data:${mediaType};base64,${data}`
  }
  if (type === 'url') {
    const url = stringValue(source?.url)
    if (url) return url
  }
  throw anthropicMessagesGuidance(req, model, `当前${providerLabel(providerName)} Chat Completions 上游只支持 Anthropic base64/url image block 转换。请客户端改用支持该图片 source 的上游，或把图片转成 base64/url 后重试。`, 'unsupported_anthropic_messages_image_source')
}

function anthropicToolsToChatTools(
  req: Request,
  value: unknown,
  model: string,
  providerName?: string
): JsonRecord[] {
  if (value === undefined) return []
  if (!Array.isArray(value)) {
    throw new GatewayRequestValidationError(
      'Anthropic Messages 到 Chat Completions 桥接要求 tools 是数组',
      'invalid_anthropic_chat_bridge_tools'
    )
  }
  const output: JsonRecord[] = []
  for (const item of value) {
    const tool = objectValue(item)
    if (!tool) continue
    const explicitType = stringValue(tool.type)
    if (explicitType && explicitType !== 'custom') {
      throw anthropicMessagesGuidance(req, model, `当前${providerLabel(providerName)} Chat Completions 上游不支持 Anthropic server tool：${explicitType}。请客户端配置本地 MCP 或改用真实支持该工具的上游。`, 'unsupported_anthropic_messages_server_tool')
    }
    const name = stringValue(tool.name)
    if (!name) {
      throw new GatewayRequestValidationError(
        'Anthropic tool 缺少 name',
        'invalid_anthropic_chat_bridge_tool'
      )
    }
    output.push({
      type: 'function',
      function: {
        name,
        description: stringValue(tool.description) ?? '',
        parameters: objectValue(tool.input_schema) ?? { type: 'object', properties: {} }
      }
    })
  }
  return output
}

function anthropicToolChoiceToChatToolChoice(
  req: Request,
  value: unknown,
  model: string,
  providerName?: string
): AnthropicToolChoiceResult {
  if (value === undefined) return {}
  const toolChoice = objectValue(value)
  if (!toolChoice) {
    throw new GatewayRequestValidationError(
      'Anthropic tool_choice 必须是对象',
      'invalid_anthropic_chat_bridge_tool_choice'
    )
  }
  const type = stringValue(toolChoice.type)
  const parallelToolCalls = toolChoice.disable_parallel_tool_use === true ? false : undefined
  if (!type || type === 'auto') return { value: 'auto', parallelToolCalls }
  if (type === 'any') return { value: 'required', parallelToolCalls }
  if (type === 'none') return { value: 'none', parallelToolCalls }
  if (type === 'tool') {
    const name = stringValue(toolChoice.name)
    if (!name) {
      throw new GatewayRequestValidationError(
        'Anthropic tool_choice.type=tool 缺少 name',
        'invalid_anthropic_chat_bridge_tool_choice'
      )
    }
    return {
      value: { type: 'function', function: { name } },
      parallelToolCalls
    }
  }
  throw anthropicMessagesGuidance(req, model, `当前${providerLabel(providerName)} Chat Completions 上游不支持 Anthropic tool_choice：${type}。请客户端改用 auto/any/tool/none，或选择支持该工具选择能力的上游。`, 'unsupported_anthropic_messages_tool_choice')
}

async function * transformChatCompletionsJsonToAnthropicMessagesJson(
  body: AsyncIterable<Uint8Array>,
  fallbackModel: string
): AsyncIterable<Uint8Array> {
  const parsed = await readJsonBody(body)
  const message = chatCompletionJsonToAnthropicMessage(parsed, fallbackModel)
  yield Buffer.from(JSON.stringify(message), 'utf8')
}

function chatCompletionJsonToAnthropicMessage(value: unknown, fallbackModel: string): JsonRecord {
  const root = objectValue(value) ?? {}
  const choice = objectValue(Array.isArray(root.choices) ? root.choices[0] : undefined) ?? {}
  const message = objectValue(choice.message) ?? {}
  const contentBlocks = chatMessageToAnthropicContentBlocks(message)
  return {
    id: normalizeAnthropicMessageId(stringValue(root.id)),
    type: 'message',
    role: 'assistant',
    model: stringValue(root.model) ?? fallbackModel,
    content: contentBlocks.length > 0 ? contentBlocks : [{ type: 'text', text: '' }],
    stop_reason: chatFinishReasonToAnthropicStopReason(stringValue(choice.finish_reason), contentBlocks),
    stop_sequence: null,
    usage: chatUsageToAnthropicUsage(objectValue(root.usage))
  }
}

function chatMessageToAnthropicContentBlocks(message: JsonRecord): JsonRecord[] {
  const output: JsonRecord[] = []
  const text = chatMessageTextContent(message.content)
  if (text) output.push({ type: 'text', text })
  const toolCalls = Array.isArray(message.tool_calls) ? message.tool_calls : []
  for (const item of toolCalls) {
    const toolCall = objectValue(item)
    const fn = objectValue(toolCall?.function)
    const name = stringValue(fn?.name)
    if (!toolCall || !name) continue
    output.push({
      type: 'tool_use',
      id: stringValue(toolCall.id) ?? `toolu_${output.length}`,
      name,
      input: parseToolArguments(stringValue(fn?.arguments))
    })
  }
  return output
}

async function * transformChatCompletionsSseToAnthropicMessagesSse(
  upstreamBody: AsyncIterable<Uint8Array>,
  fallbackModel: string
): AsyncIterable<Uint8Array> {
  const state = createAnthropicChatStreamState(fallbackModel)
  const decoder = new StringDecoder('utf8')
  let pending = ''
  for await (const chunk of upstreamBody) {
    pending += decoder.write(Buffer.from(chunk))
    const events = takeCompleteSseEvents(pending)
    pending = events.rest
    for (const eventText of events.events) {
      for (const output of processChatCompletionsSseEvent(state, eventText)) {
        yield Buffer.from(output, 'utf8')
      }
    }
  }
  pending += decoder.end()
  if (pending.trim()) {
    for (const output of processChatCompletionsSseEvent(state, pending)) {
      yield Buffer.from(output, 'utf8')
    }
  }
  if (!state.completed && !state.failed) {
    for (const output of failAnthropicStream(state, '上游 Chat Completions SSE 在 message_stop 前中断', 'upstream_stream_interrupted')) {
      yield Buffer.from(output, 'utf8')
    }
  }
}

function createAnthropicChatStreamState(model: string): AnthropicChatStreamState {
  const createdAt = Math.floor(Date.now() / 1000)
  return {
    id: `msg_chat_${createdAt}_${Math.random().toString(36).slice(2, 8)}`,
    createdAt,
    model,
    started: false,
    completed: false,
    failed: false,
    nextContentBlockIndex: 0,
    textStarted: false,
    textDone: false,
    toolCalls: new Map(),
    hadToolUse: false
  }
}

function processChatCompletionsSseEvent(state: AnthropicChatStreamState, rawEventText: string): string[] {
  if (state.completed || state.failed) return []
  const event = parseOpenAISseEventText(rawEventText)
  if (event.dataText === '[DONE]') {
    return completeAnthropicStream(state)
  }
  if (event.dataParseError) {
    return failAnthropicStream(state, '上游 Chat Completions SSE 返回了无法解析的事件', 'upstream_stream_parse_error')
  }
  const data = event.data
  if (!data) return []
  if (event.eventName === 'error' || event.eventType === 'error' || objectValue(data.error)) {
    const error = objectValue(data.error) ?? data
    return failAnthropicStream(
      state,
      stringValue(error.message) ?? '上游 Chat Completions SSE 返回错误事件',
      stringValue(error.code) ?? stringValue(error.type) ?? 'upstream_error',
      stringValue(error.type) ?? 'api_error'
    )
  }
  state.id = normalizeAnthropicMessageId(stringValue(data.id)) ?? state.id
  state.model = stringValue(data.model) ?? state.model
  const usage = chatUsageToAnthropicUsage(objectValue(data.usage))
  if (usage) state.usage = usage
  const output: string[] = []
  const choices = Array.isArray(data.choices) ? data.choices : []
  for (const item of choices) {
    const choice = objectValue(item)
    if (!choice) continue
    const delta = objectValue(choice.delta)
    if (delta) {
      const text = stringValue(delta.content) ?? stringValue(delta.reasoning_content) ?? stringValue(delta.refusal)
      if (text) output.push(...appendAnthropicTextDelta(state, text))
      const toolCalls = Array.isArray(delta.tool_calls) ? delta.tool_calls : []
      for (const toolCallValue of toolCalls) {
        output.push(...appendAnthropicToolCallDelta(state, objectValue(toolCallValue)))
      }
    }
    const finishReason = stringValue(choice.finish_reason)
    if (finishReason) {
      state.stopReason = chatFinishReasonToAnthropicStopReason(finishReason)
      output.push(...completeAnthropicStream(state))
    }
  }
  return output
}

function ensureAnthropicMessageStarted(state: AnthropicChatStreamState): string[] {
  if (state.started) return []
  state.started = true
  return [anthropicSse('message_start', {
    type: 'message_start',
    message: {
      id: state.id,
      type: 'message',
      role: 'assistant',
      model: state.model,
      content: [],
      stop_reason: null,
      stop_sequence: null,
      usage: state.usage ?? { input_tokens: 0, output_tokens: 0 }
    }
  })]
}

function appendAnthropicTextDelta(state: AnthropicChatStreamState, text: string): string[] {
  const output = ensureAnthropicMessageStarted(state)
  output.push(...closeStartedToolBlocks(state))
  if (!state.textStarted) {
    const index = state.nextContentBlockIndex++
    state.textBlockIndex = index
    state.textStarted = true
    state.textDone = false
    output.push(anthropicSse('content_block_start', {
      type: 'content_block_start',
      index,
      content_block: { type: 'text', text: '' }
    }))
  }
  output.push(anthropicSse('content_block_delta', {
    type: 'content_block_delta',
    index: state.textBlockIndex ?? 0,
    delta: { type: 'text_delta', text }
  }))
  return output
}

function appendAnthropicToolCallDelta(
  state: AnthropicChatStreamState,
  toolCall: JsonRecord | undefined
): string[] {
  if (!toolCall) return []
  const key = integerValue(toolCall.index) ?? 0
  const fn = objectValue(toolCall.function)
  let current = state.toolCalls.get(key)
  if (!current) {
    current = {
      key,
      blockIndex: state.nextContentBlockIndex++,
      id: stringValue(toolCall.id) ?? `toolu_${key}`,
      name: stringValue(fn?.name) ?? '',
      arguments: '',
      bufferedArguments: '',
      started: false,
      done: false
    }
    state.toolCalls.set(key, current)
  }
  current.id = stringValue(toolCall.id) ?? current.id
  current.name = stringValue(fn?.name) ?? current.name
  const argumentDelta = stringValue(fn?.arguments) ?? ''
  const output = ensureAnthropicMessageStarted(state)
  output.push(...closeTextBlock(state))
  if (!current.started && current.name) {
    current.started = true
    state.hadToolUse = true
    output.push(anthropicSse('content_block_start', {
      type: 'content_block_start',
      index: current.blockIndex,
      content_block: {
        type: 'tool_use',
        id: current.id,
        name: current.name,
        input: {}
      }
    }))
    if (current.bufferedArguments) {
      output.push(anthropicSse('content_block_delta', {
        type: 'content_block_delta',
        index: current.blockIndex,
        delta: { type: 'input_json_delta', partial_json: current.bufferedArguments }
      }))
      current.arguments += current.bufferedArguments
      current.bufferedArguments = ''
    }
  }
  if (argumentDelta) {
    if (!current.started) {
      current.bufferedArguments += argumentDelta
    } else {
      current.arguments += argumentDelta
      output.push(anthropicSse('content_block_delta', {
        type: 'content_block_delta',
        index: current.blockIndex,
        delta: { type: 'input_json_delta', partial_json: argumentDelta }
      }))
    }
  }
  return output
}

function completeAnthropicStream(state: AnthropicChatStreamState): string[] {
  if (state.completed || state.failed) return []
  state.completed = true
  const output = ensureAnthropicMessageStarted(state)
  output.push(...closeTextBlock(state))
  output.push(...closeStartedToolBlocks(state, true))
  output.push(anthropicSse('message_delta', {
    type: 'message_delta',
    delta: {
      stop_reason: state.stopReason ?? (state.hadToolUse ? 'tool_use' : 'end_turn'),
      stop_sequence: null
    },
    usage: { output_tokens: integerValue(state.usage?.output_tokens) ?? 0 }
  }))
  output.push(anthropicSse('message_stop', { type: 'message_stop' }))
  return output
}

function closeTextBlock(state: AnthropicChatStreamState): string[] {
  if (!state.textStarted || state.textDone || state.textBlockIndex === undefined) return []
  state.textDone = true
  return [anthropicSse('content_block_stop', {
    type: 'content_block_stop',
    index: state.textBlockIndex
  })]
}

function closeStartedToolBlocks(state: AnthropicChatStreamState, includeUnstarted = false): string[] {
  const output: string[] = []
  for (const toolCall of [...state.toolCalls.values()].sort((left, right) => left.blockIndex - right.blockIndex)) {
    if (toolCall.done) continue
    if (!toolCall.started && includeUnstarted && (toolCall.name || toolCall.bufferedArguments)) {
      toolCall.started = true
      toolCall.name = toolCall.name || 'unknown_tool'
      state.hadToolUse = true
      output.push(anthropicSse('content_block_start', {
        type: 'content_block_start',
        index: toolCall.blockIndex,
        content_block: {
          type: 'tool_use',
          id: toolCall.id,
          name: toolCall.name,
          input: {}
        }
      }))
      if (toolCall.bufferedArguments) {
        output.push(anthropicSse('content_block_delta', {
          type: 'content_block_delta',
          index: toolCall.blockIndex,
          delta: { type: 'input_json_delta', partial_json: toolCall.bufferedArguments }
        }))
        toolCall.bufferedArguments = ''
      }
    }
    if (!toolCall.started) continue
    toolCall.done = true
    output.push(anthropicSse('content_block_stop', {
      type: 'content_block_stop',
      index: toolCall.blockIndex
    }))
  }
  return output
}

function failAnthropicStream(state: AnthropicChatStreamState, message: string, code: string, type = 'api_error'): string[] {
  if (state.completed || state.failed) return []
  state.failed = true
  return [anthropicSse('error', {
    type: 'error',
    error: {
      type,
      code,
      message
    }
  })]
}

function anthropicStopSequencesToChatStop(value: unknown): string | string[] | undefined {
  if (!Array.isArray(value)) return undefined
  const stops = value.filter((item): item is string => typeof item === 'string' && item.length > 0)
  if (stops.length === 0) return undefined
  return stops.length === 1 ? stops[0] : stops
}

function chatUserContentFromParts(parts: JsonRecord[]): string | JsonRecord[] {
  const hasNonText = parts.some((part) => part.type !== 'text')
  if (hasNonText) return parts
  return parts.map((part) => stringValue(part.text) ?? '').join('')
}

function chatMessageTextContent(value: unknown): string {
  if (typeof value === 'string') return value
  if (!Array.isArray(value)) return ''
  return value.map((item) => {
    const part = objectValue(item)
    if (!part) return ''
    if (part.type === 'text' || part.type === 'output_text') return stringValue(part.text) ?? ''
    return ''
  }).join('')
}

function anthropicContentText(value: unknown): string {
  if (typeof value === 'string') return value
  if (!Array.isArray(value)) return ''
  return value.map((item) => {
    const block = objectValue(item)
    if (!block) return ''
    if (block.type === 'text') return stringValue(block.text) ?? ''
    return ''
  }).filter(Boolean).join('\n')
}

function chatFinishReasonToAnthropicStopReason(finishReason: string | undefined, contentBlocks?: JsonRecord[]): string {
  if (finishReason === 'tool_calls' || finishReason === 'function_call') return 'tool_use'
  if (finishReason === 'length') return 'max_tokens'
  if (finishReason === 'content_filter') return 'refusal'
  if (contentBlocks?.some((block) => block.type === 'tool_use')) return 'tool_use'
  return 'end_turn'
}

function chatUsageToAnthropicUsage(usage: JsonRecord | undefined): JsonRecord | undefined {
  if (!usage) return undefined
  const promptTokens = integerValue(usage.prompt_tokens) ?? integerValue(usage.input_tokens) ?? 0
  const completionTokens = integerValue(usage.completion_tokens) ?? integerValue(usage.output_tokens) ?? 0
  const output: JsonRecord = {
    input_tokens: promptTokens,
    output_tokens: completionTokens
  }
  const cachedTokens = integerValue(objectValue(usage.prompt_tokens_details)?.cached_tokens)
    ?? integerValue(objectValue(usage.input_tokens_details)?.cached_tokens)
  if (cachedTokens !== undefined) {
    output.cache_read_input_tokens = cachedTokens
  }
  return output
}

function parseToolArguments(value: string | undefined): unknown {
  if (!value) return {}
  try {
    const parsed = JSON.parse(value) as unknown
    return isPlainObject(parsed) ? parsed : { value: parsed }
  } catch {
    return { _raw: value }
  }
}

function normalizeAnthropicMessageId(value: string | undefined): string | undefined {
  if (!value) return undefined
  return value.startsWith('msg_') ? value : `msg_${value}`
}

function unsupportedAnthropicContentBlock(
  req: Request,
  model: string,
  providerName: string | undefined,
  type: string | undefined
): never {
  throw anthropicMessagesGuidance(req, model, `当前${providerLabel(providerName)} Chat Completions 上游不支持 Anthropic content block：${type ?? 'unknown'}。请客户端改用真实支持该 block 的上游，或在本地 agent 中先转换/执行后再发起请求。`, 'unsupported_anthropic_messages_content_block')
}

function anthropicMessagesGuidance(req: Request, model: string, message: string, code: string): GatewayAgentGuidanceResponse {
  return new GatewayAgentGuidanceResponse({
    message,
    code,
    protocol: 'messages',
    stream: requestStream(req),
    model
  })
}

function providerLabel(providerName?: string): string {
  return providerName ? ` ${providerName}` : ''
}

async function readJsonBody(body: AsyncIterable<Uint8Array>): Promise<unknown> {
  const chunks: Buffer[] = []
  let total = 0
  for await (const chunk of body) {
    const buffer = Buffer.from(chunk)
    chunks.push(buffer)
    total += buffer.byteLength
    if (total > 16 * 1024 * 1024) {
      throw new Error('anthropic_chat_bridge_response_too_large')
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

function anthropicSse(event: string, payload: JsonRecord): string {
  return `event: ${event}\ndata: ${JSON.stringify(payload)}\n\n`
}

function objectValue(value: unknown): JsonRecord | undefined {
  return isPlainObject(value) ? value : undefined
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' ? value : undefined
}

function integerValue(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isInteger(value) ? value : undefined
}

function isPlainObject(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
