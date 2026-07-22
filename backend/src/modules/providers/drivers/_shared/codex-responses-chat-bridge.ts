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
import { splitPathAndQuery } from '../../../gateway/protocols/openai-v1/route-helpers.js'
import type { CodexResponsesChatBridgeCompletionHandler } from '../../../gateway/codex-responses/chat-bridge-state.js'
import { createCodexResponsesGuardMarker } from '../../../gateway/codex-responses/response-guard.js'
import type { GatewayUpstreamResponse } from '../../../gateway/upstream/request.js'
type JsonRecord = Record<string, unknown>
const codexResponsesChatBridgeLocalValidationUrl = 'codex-responses-chat-bridge:local-validation'
const codexCustomToolChatNamePrefix = 'custom__'

type CodexResponsesChatBridgeRuntimeRequest = Request & {
  codexResponsesChatBridgeToolAdaptersByChatName?: Map<string, CodexResponsesChatBridgeToolAdapter>
}

interface CodexResponsesChatBridgeRequestOptions {
  enabled: boolean
  explicitMappingBridge?: boolean
  requestClientCompatibility?: ClientCompatibilityCapability
}

interface BuildCodexResponsesChatBridgeBodyOptions {
  defaultModel: string
  guidanceProviderName?: string
  includeReasoningContent?: boolean
  modelOverride?: string
  streamOptionsIncludeUsage?: boolean
  thinking?: JsonRecord
  parallelToolCalls?: boolean
  toolStream?: boolean
}

interface TransformCodexResponsesChatBridgeResponseOptions extends CodexResponsesChatBridgeRequestOptions {
  defaultModel: string
  finishReasonFailures?: Record<string, CodexResponsesChatBridgeFinishReasonFailure>
  idPrefix?: string
  model?: string
  previousResponseId?: string
  onCompleted?: CodexResponsesChatBridgeCompletionHandler
  continueChatRequest?: unknown
}

interface CodexResponsesChatBridgeFinishReasonFailure {
  code: string
  message: string
}

interface CodexResponsesChatBridgeToolAdapter {
  kind: 'function' | 'custom'
  chatName: string
  responsesName: string
  namespace?: string
}

interface CodexResponsesChatBridgeToolPlan {
  chatTools: JsonRecord[]
  adaptersByChatName: Map<string, CodexResponsesChatBridgeToolAdapter>
  adaptersByResponsesKey: Map<string, CodexResponsesChatBridgeToolAdapter>
  unsupportedTools: string[]
}

interface ChatToolCallState {
  id: string
  itemType?: 'function_call' | 'custom_tool_call'
  callId: string
  name: string
  arguments: string
  adapter?: CodexResponsesChatBridgeToolAdapter
  outputIndex: number
  added: boolean
  done: boolean
}

interface PendingChatToolCallState {
  upstreamCallId?: string
  name: string
  arguments: string
  outputIndex: number
}
interface PendingChatToolCall {
  callId: string
  name: string
  arguments: string
  adapter?: CodexResponsesChatBridgeToolAdapter
  output?: string
}

interface PendingChatToolCallGroup {
  calls: PendingChatToolCall[]
  deferredMessages: JsonRecord[]
  reasoningText: string
}

interface ResponsesInputToChatMessagesOptions {
  includeReasoningContent?: boolean
  forcedToolChoiceMessage?: string
  unsupportedTools?: string[]
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
  pendingToolCalls: Map<number, PendingChatToolCallState>
  toolAdaptersByChatName: Map<string, CodexResponsesChatBridgeToolAdapter>
  usage?: JsonRecord
  estimatedInputTokens?: number
  finishReasonFailures: Map<string, CodexResponsesChatBridgeFinishReasonFailure>
  completed: boolean
  failed: boolean
  failureCode?: string
  failureMessage?: string
  terminalReceived: boolean
  completionNotified: boolean
}

export function isCodexResponsesChatBridgeRequest(
  req: Request,
  options: CodexResponsesChatBridgeRequestOptions
): boolean {
  return options.enabled
    && (options.explicitMappingBridge === true || options.requestClientCompatibility === 'codex_responses')
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
    '当前 Chat-only bridge 不支持 /responses/compact；需要使用原生 Responses 账号或供应商显式兼容的 compact 能力',
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
  const model = options.modelOverride ?? stringValue(body.model) ?? options.defaultModel
  const toolPlan = responsesToolsToChatToolPlan(body.tools)
  const runtimeRequest = req as CodexResponsesChatBridgeRuntimeRequest
  runtimeRequest.codexResponsesChatBridgeToolAdaptersByChatName = toolPlan.adaptersByChatName
  const toolChoice = responsesToolChoiceToChatToolChoice(body.tool_choice, toolPlan)
  const chatBody: JsonRecord = {
    model,
    messages: responsesInputToChatMessages(body, {
      includeReasoningContent: options.includeReasoningContent === true,
      forcedToolChoiceMessage: forcedToolChoiceSystemMessage(body.tool_choice, toolPlan),
      unsupportedTools: unsupportedToolsForSystemMessage(body.tool_choice, toolPlan)
    }),
    stream: true
  }
  if (options.streamOptionsIncludeUsage === true) {
    chatBody.stream_options = {
      ...objectValue(body.stream_options),
      include_usage: true
    }
  }
  if (options.thinking && isPlainObject(options.thinking)) {
    chatBody.thinking = options.thinking
  }
  const promptCacheKey = stringValue(body.prompt_cache_key)
  if (promptCacheKey !== undefined) {
    chatBody.prompt_cache_key = promptCacheKey
  }
  if (typeof body.service_tier === 'string' && body.service_tier.trim()) {
    chatBody.service_tier = body.service_tier.trim()
  }
  const reasoningEffort = stringValue(objectValue(body.reasoning)?.effort)
  if (reasoningEffort) {
    chatBody.reasoning_effort = reasoningEffort
  }

  if (toolPlan.chatTools.length > 0) {
    chatBody.tools = toolPlan.chatTools
    if (options.toolStream === true) {
      chatBody.tool_stream = true
    }
    if (toolChoice !== undefined) {
      chatBody.tool_choice = toolChoice
    }
    if (typeof options.parallelToolCalls === 'boolean') {
      chatBody.parallel_tool_calls = options.parallelToolCalls
    } else if (typeof body.parallel_tool_calls === 'boolean') {
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
  if (!response.ok || !response.body || !isCodexResponsesChatBridgeResponseTransformRequest(req, options)) {
    return response
  }
  const headers = new Headers(response.headers)
  headers.delete('content-length')
  const transformInput = {
    finishReasonFailures: options.finishReasonFailures,
    idPrefix: options.idPrefix,
    estimatedInputTokens: estimateResponsesRequestInputTokens(req),
    model: options.model ?? stringValue((req.body as JsonRecord | undefined)?.model) ?? options.defaultModel,
    previousResponseId: options.previousResponseId,
    toolAdaptersByChatName: (req as CodexResponsesChatBridgeRuntimeRequest).codexResponsesChatBridgeToolAdaptersByChatName,
    onCompleted: options.onCompleted
  }
  if (!requestStream(req)) {
    headers.set('content-type', 'application/json; charset=utf-8')
    return {
      status: response.status,
      ok: response.ok,
      headers,
      body: transformChatCompletionsSseToResponsesJson(response.body, transformInput),
      codexResponsesGuardMarker: createCodexResponsesGuardMarker('gateway_bridge')
    }
  }
  headers.set('content-type', 'text/event-stream; charset=utf-8')
  return {
    status: response.status,
    ok: response.ok,
    headers,
    body: transformChatCompletionsSseToResponsesSse(response.body, transformInput),
    codexResponsesGuardMarker: createCodexResponsesGuardMarker('gateway_bridge')
  }
}

function isCodexResponsesChatBridgeResponseTransformRequest(
  req: Request,
  options: TransformCodexResponsesChatBridgeResponseOptions
): boolean {
  if (!options.enabled) return false
  if (options.explicitMappingBridge === true) {
    return isOpenAIResponsesPostRequest(req)
  }
  return isCodexResponsesChatBridgeRequest(req, options)
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
  options: ResponsesInputToChatMessagesOptions = {}
): JsonRecord[] {
  const messages: JsonRecord[] = []
  const instructions = stringValue(body.instructions)
  if (instructions) {
    messages.push({ role: 'system', content: instructions })
  }
  const unsupportedToolsMessage = unsupportedToolsSystemMessage(options.unsupportedTools)
  if (unsupportedToolsMessage) {
    messages.push({ role: 'system', content: unsupportedToolsMessage })
  }
  if (options.forcedToolChoiceMessage) {
    messages.push({ role: 'system', content: options.forcedToolChoiceMessage })
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
  options: ResponsesInputToChatMessagesOptions
): PendingChatToolCallGroup {
  if (!isPlainObject(item)) return pendingToolGroup
  if (item.type === 'compaction' || item.type === 'compaction_summary') {
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
    const namespace = stringValue(item.namespace)
    if (pendingToolGroup.calls.length > 0 && pendingToolCallsAllAnswered(pendingToolGroup)) {
      flushPendingChatToolCallGroup(messages, pendingToolGroup, options)
      pendingToolGroup = createPendingChatToolCallGroup()
    }
    pendingToolGroup.calls.push({
      callId,
      name: namespace ? chatToolNameFromParts([namespace, name]) : name,
      arguments: stringValue(item.arguments) ?? ''
    })
    return pendingToolGroup
  }
  if (item.type === 'custom_tool_call') {
    const name = stringValue(item.name)
    const callId = stringValue(item.call_id) ?? stringValue(item.id)
    if (!name || !callId) return pendingToolGroup
    if (pendingToolGroup.calls.length > 0 && pendingToolCallsAllAnswered(pendingToolGroup)) {
      flushPendingChatToolCallGroup(messages, pendingToolGroup, options)
      pendingToolGroup = createPendingChatToolCallGroup()
    }
    pendingToolGroup.calls.push({
      callId,
      name: customChatToolName(name),
      arguments: JSON.stringify({ input: stringValue(item.input) ?? '' })
    })
    return pendingToolGroup
  }
  if (item.type === 'function_call_output' || item.type === 'custom_tool_call_output') {
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
  options: ResponsesInputToChatMessagesOptions
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
  if (encryptedContent.startsWith('juhecmp.v2.')) {
    throw new GatewayRequestValidationError(
      'Chat-only bridge compact snapshot 未完成服务端恢复，不能直接转发给 Chat 上游',
      'codex_bridge_compact_snapshot_unresolved'
    )
  }
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

function responsesToolsToChatToolPlan(value: unknown): CodexResponsesChatBridgeToolPlan {
  const plan: CodexResponsesChatBridgeToolPlan = {
    chatTools: [],
    adaptersByChatName: new Map(),
    adaptersByResponsesKey: new Map(),
    unsupportedTools: []
  }
  if (!Array.isArray(value)) return plan

  const usedChatNames = new Set<string>()
  for (const item of value) {
    appendResponsesToolToChatPlan(plan, usedChatNames, item)
  }
  plan.unsupportedTools = [...new Set(plan.unsupportedTools)]
  return plan
}

function appendResponsesToolToChatPlan(
  plan: CodexResponsesChatBridgeToolPlan,
  usedChatNames: Set<string>,
  item: unknown,
  namespace?: string
): void {
  if (!isPlainObject(item)) return
  const type = stringValue(item.type)
  if (type === 'function') {
    const name = stringValue(item.name)
    if (!name) return
    const chatName = uniqueChatToolName(namespace ? chatToolNameFromParts([namespace, name]) : sanitizeChatToolName(name), usedChatNames)
    const adapter: CodexResponsesChatBridgeToolAdapter = {
      kind: 'function',
      chatName,
      responsesName: name,
      namespace
    }
    plan.adaptersByChatName.set(chatName, adapter)
    plan.adaptersByResponsesKey.set(responsesToolAdapterKey('function', name, namespace), adapter)
    plan.chatTools.push(responsesFunctionToolToChatTool(item, chatName))
    return
  }
  if (type === 'custom') {
    const name = stringValue(item.name)
    if (!name) return
    const chatName = uniqueChatToolName(customChatToolName(name, namespace), usedChatNames)
    const adapter: CodexResponsesChatBridgeToolAdapter = {
      kind: 'custom',
      chatName,
      responsesName: name,
      namespace
    }
    plan.adaptersByChatName.set(chatName, adapter)
    plan.adaptersByResponsesKey.set(responsesToolAdapterKey('custom', name, namespace), adapter)
    plan.chatTools.push(responsesCustomToolToChatTool(item, chatName, name))
    return
  }
  if (type === 'web_search' || type === 'web_search_preview' || type === 'web_search_preview_2025_03_11') {
    plan.unsupportedTools.push(unsupportedResponsesToolLabel(item, namespace))
    return
  }
  if (type === 'namespace') {
    const nextNamespace = stringValue(item.namespace) ?? stringValue(item.name) ?? namespace
    const tools = Array.isArray(item.tools) ? item.tools : []
    for (const child of tools) {
      appendResponsesToolToChatPlan(plan, usedChatNames, child, nextNamespace)
    }
    if (tools.length === 0) {
      plan.unsupportedTools.push(unsupportedResponsesToolLabel(item, namespace))
    }
    return
  }
  if (type) {
    plan.unsupportedTools.push(unsupportedResponsesToolLabel(item, namespace))
  }
}

function responsesFunctionToolToChatTool(item: JsonRecord, chatName: string): JsonRecord {
  const tool: JsonRecord = {
    type: 'function',
    function: {
      name: chatName,
      description: stringValue(item.description) ?? '',
      parameters: isPlainObject(item.parameters) ? item.parameters : { type: 'object', properties: {} }
    }
  }
  const strict = item.strict
  if (typeof strict === 'boolean') {
    ;(tool.function as JsonRecord).strict = strict
  }
  return tool
}

function responsesCustomToolToChatTool(item: JsonRecord, chatName: string, responsesName: string): JsonRecord {
  if (responsesName === 'apply_patch') {
    return responsesApplyPatchToolToChatTool(item, chatName)
  }
  const descriptionParts = [
    `Use the Responses custom tool "${responsesName}" through this Chat bridge wrapper.`,
    'You must call this function with JSON arguments containing exactly one field named "input".',
    'Put the complete free-form custom tool payload inside the "input" string. Do not paste the payload as assistant text.',
    customToolFormatDescription(item)
  ].filter(Boolean)
  return {
    type: 'function',
    function: {
      name: chatName,
      description: descriptionParts.join('\n\n'),
      parameters: {
        type: 'object',
        properties: {
          input: {
            type: 'string',
            description: 'Complete free-form custom tool input.'
          }
        },
        required: ['input'],
        additionalProperties: false
      },
      strict: true
    }
  }
}

function responsesApplyPatchToolToChatTool(item: JsonRecord, chatName: string): JsonRecord {
  const descriptionParts = [
    'Use apply_patch to create or edit local files.',
    'Prefer the structured "files" array when creating complete files: provide one object per file with "path" and full "content".',
    'The gateway will convert "files" into a valid apply_patch payload.',
    'Alternatively, provide an exact apply_patch payload in "input" when you need manual patch control.',
    customToolFormatDescription(item)
  ].filter(Boolean)
  return {
    type: 'function',
    function: {
      name: chatName,
      description: descriptionParts.join('\n\n'),
      parameters: {
        type: 'object',
        properties: {
          input: {
            type: 'string',
            description: 'Exact apply_patch payload. Use only when not using files.'
          },
          files: {
            type: 'array',
            description: 'Complete files to add. Prefer this for new files.',
            items: {
              type: 'object',
              properties: {
                path: {
                  type: 'string',
                  description: 'Relative file path.'
                },
                content: {
                  type: 'string',
                  description: 'Full file content.'
                }
              },
              required: ['path', 'content'],
              additionalProperties: false
            }
          }
        },
        additionalProperties: false
      }
    }
  }
}

function customToolFormatDescription(item: JsonRecord): string | undefined {
  const format = objectValue(item.format)
  if (!format) return undefined
  const type = stringValue(format.type)
  const syntax = stringValue(format.syntax)
  const definition = stringValue(format.definition)
  const parts = [
    type ? `Original custom tool format type: ${type}.` : undefined,
    syntax ? `Grammar syntax: ${syntax}.` : undefined,
    definition ? `The "input" string must satisfy this grammar:\n${definition}` : undefined
  ].filter(Boolean)
  return parts.length ? parts.join('\n') : undefined
}

function responsesToolChoiceToChatToolChoice(
  value: unknown,
  plan: CodexResponsesChatBridgeToolPlan
): unknown {
  if (value === undefined || value === null) return undefined
  if (typeof value === 'string') {
    if (value === 'required' && plan.chatTools.length === 0) {
      if (plan.unsupportedTools.length > 0) return undefined
      throwUnsupportedNativeToolChoice('required', plan.unsupportedTools)
    }
    return value
  }
  if (!isPlainObject(value)) return undefined

  const type = stringValue(value.type)
  if (type === 'web_search' || type === 'web_search_preview' || type === 'web_search_preview_2025_03_11') {
    plan.unsupportedTools.push('web_search')
    plan.unsupportedTools = [...new Set(plan.unsupportedTools)]
    return undefined
  }
  if (type === 'function' || type === 'custom') {
    const name = stringValue(value.name)
    if (!name) return undefined
    const namespace = stringValue(value.namespace)
    const adapter = plan.adaptersByResponsesKey.get(responsesToolAdapterKey(type, name, namespace))
    if (!adapter) {
      if (plan.unsupportedTools.length > 0) return undefined
      throwUnsupportedNativeToolChoice(`${type}:${namespace ? `${namespace}.` : ''}${name}`, plan.unsupportedTools)
    }
    return { type: 'function', function: { name: adapter.chatName } }
  }

  if (type === 'allowed_tools') {
    const mode = stringValue(value.mode)
    const allowed = Array.isArray(value.tools) ? value.tools : []
    const allowedAdapters = allowed
      .map((tool) => allowedToolChoiceAdapter(tool, plan))
      .filter((adapter): adapter is CodexResponsesChatBridgeToolAdapter => adapter !== undefined)
    if (allowed.length > 0 && allowedAdapters.length === 0 && mode === 'required') {
      for (const tool of allowed) {
        if (isPlainObject(tool)) plan.unsupportedTools.push(unsupportedResponsesToolLabel(tool))
      }
      plan.unsupportedTools = [...new Set(plan.unsupportedTools)]
      if (plan.unsupportedTools.length > 0) return undefined
      throwUnsupportedNativeToolChoice('allowed_tools', plan.unsupportedTools)
    }
    return mode === 'required' ? 'required' : 'auto'
  }

  if (type) {
    plan.unsupportedTools.push(type)
    plan.unsupportedTools = [...new Set(plan.unsupportedTools)]
    return undefined
  }
  return undefined
}

function allowedToolChoiceAdapter(
  value: unknown,
  plan: CodexResponsesChatBridgeToolPlan
): CodexResponsesChatBridgeToolAdapter | undefined {
  if (!isPlainObject(value)) return undefined
  const type = stringValue(value.type)
  if (type === 'web_search' || type === 'web_search_preview' || type === 'web_search_preview_2025_03_11') {
    return undefined
  }
  if (type !== 'function' && type !== 'custom') return undefined
  const name = stringValue(value.name)
  if (!name) return undefined
  return plan.adaptersByResponsesKey.get(responsesToolAdapterKey(type, name, stringValue(value.namespace)))
}

function responsesToolAdapterKey(kind: 'function' | 'custom', name: string, namespace?: string): string {
  return `${kind}:${namespace ?? ''}:${name}`
}

function unsupportedResponsesToolLabel(item: JsonRecord, namespace?: string): string {
  const type = stringValue(item.type) ?? 'unknown'
  const label = stringValue(item.name) ?? stringValue(item.server_label) ?? stringValue(item.connector_id) ?? stringValue(item.server_url)
  const qualified = label ? `${type}:${label}` : type
  return namespace ? `${namespace}.${qualified}` : qualified
}

function unsupportedToolsSystemMessage(unsupportedTools: string[] | undefined): string | undefined {
  const tools = [...new Set((unsupportedTools ?? []).filter(Boolean))]
  if (tools.length === 0) return undefined
  return [
    `Chat-only bridge 当前不能代执行以下 Responses 原生托管工具：${tools.join(', ')}。`,
    '这是给模型看的内部能力约束，不要把本段说明原文输出给用户。',
    '继续使用当前请求中可用的 function/custom 工具、已有上下文和普通推理完成用户任务。',
    '不要假装已经调用这些不可用工具；只有任务确实无法在缺少这些工具时完成，才用简短自然语言说明缺少对应外部能力。'
  ].join(' ')
}

function forcedToolChoiceSystemMessage(
  toolChoice: unknown,
  plan: CodexResponsesChatBridgeToolPlan
): string | undefined {
  if (!isPlainObject(toolChoice)) return undefined
  const type = stringValue(toolChoice.type)
  if (type !== 'function' && type !== 'custom') return undefined
  const name = stringValue(toolChoice.name)
  if (!name) return undefined
  const adapter = plan.adaptersByResponsesKey.get(responsesToolAdapterKey(type, name, stringValue(toolChoice.namespace)))
  if (!adapter) return undefined
  return [
    `This request explicitly selected the "${adapter.chatName}" tool.`,
    'You must call that tool in this turn.',
    'Do not answer with a plan, status update, explanation, or plain text instead of the selected tool call.'
  ].join(' ')
}

function unsupportedToolsForSystemMessage(
  toolChoice: unknown,
  plan: CodexResponsesChatBridgeToolPlan
): string[] {
  if (plan.unsupportedTools.length === 0) return []
  if (toolChoice === undefined || toolChoice === null) return plan.unsupportedTools
  if (typeof toolChoice === 'string') {
    return toolChoice === 'auto' || toolChoice === 'required' ? plan.unsupportedTools : []
  }
  if (!isPlainObject(toolChoice)) return plan.unsupportedTools
  const type = stringValue(toolChoice.type)
  if (type === 'function' || type === 'custom') {
    const name = stringValue(toolChoice.name)
    if (!name) return plan.unsupportedTools
    return plan.adaptersByResponsesKey.has(responsesToolAdapterKey(type, name, stringValue(toolChoice.namespace)))
      ? []
      : plan.unsupportedTools
  }
  if (type === 'allowed_tools') {
    const allowed = Array.isArray(toolChoice.tools) ? toolChoice.tools : []
    return allowed.length > 0 && allowed.every((tool) => allowedToolChoiceAdapter(tool, plan))
      ? []
      : plan.unsupportedTools
  }
  return plan.unsupportedTools
}

function canIgnoreUnsupportedResponsesTools(
  toolChoice: unknown,
  plan: CodexResponsesChatBridgeToolPlan
): boolean {
  if (toolChoice === undefined || toolChoice === null) {
    return false
  }
  if (typeof toolChoice === 'string') {
    if (toolChoice === 'none') return true
    if (toolChoice === 'auto' || toolChoice === 'required') return false
    return false
  }
  if (!isPlainObject(toolChoice)) {
    return false
  }
  const type = stringValue(toolChoice.type)
  if (type === 'function' || type === 'custom') {
    const name = stringValue(toolChoice.name)
    if (!name) return false
    const namespace = stringValue(toolChoice.namespace)
    return plan.adaptersByResponsesKey.has(responsesToolAdapterKey(type, name, namespace))
  }
  if (type === 'allowed_tools') {
    const allowed = Array.isArray(toolChoice.tools) ? toolChoice.tools : []
    return allowed.length > 0 && allowed.every((tool) => allowedToolChoiceAdapter(tool, plan))
  }
  return false
}

function throwUnsupportedNativeToolChoice(choice: string, unsupportedTools: string[]): never {
  const suffix = unsupportedTools.length > 0 ? `；当前不可代执行工具：${unsupportedTools.join(', ')}` : ''
  throw new GatewayRequestValidationError(
    `当前 Chat-only bridge 不能执行 Responses 原生工具选择 ${choice}${suffix}`,
    'unsupported_codex_native_tool'
  )
}

function customChatToolName(name: string, namespace?: string): string {
  const parts = namespace ? [namespace, `${codexCustomToolChatNamePrefix}${name}`] : [`${codexCustomToolChatNamePrefix}${name}`]
  return chatToolNameFromParts(parts)
}

function chatToolNameFromParts(parts: string[]): string {
  const name = parts.map(sanitizeChatToolName).filter(Boolean).join('__')
  return name || 'tool'
}

function sanitizeChatToolName(name: string): string {
  return name.replace(/[^A-Za-z0-9_-]/g, '_').replace(/^_+|_+$/g, '') || 'tool'
}

function uniqueChatToolName(base: string, used: Set<string>): string {
  let name = base.slice(0, 64) || 'tool'
  let index = 2
  while (used.has(name)) {
    const suffix = `_${index++}`
    name = `${base.slice(0, Math.max(1, 64 - suffix.length))}${suffix}`
  }
  used.add(name)
  return name
}

async function * transformChatCompletionsSseToResponsesSse(
  upstreamBody: AsyncIterable<Uint8Array>,
  input: {
    estimatedInputTokens?: number
    finishReasonFailures?: Record<string, CodexResponsesChatBridgeFinishReasonFailure>
    idPrefix?: string
    model: string
    previousResponseId?: string
    toolAdaptersByChatName?: Map<string, CodexResponsesChatBridgeToolAdapter>
    onCompleted?: CodexResponsesChatBridgeCompletionHandler
  }
): AsyncIterable<Uint8Array> {
  const state = createChatToResponsesState(
    input.model,
    input.idPrefix,
    input.estimatedInputTokens,
    input.previousResponseId,
    input.finishReasonFailures,
    input.toolAdaptersByChatName
  )
  const decoder = new StringDecoder('utf8')
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

async function * transformChatCompletionsSseToResponsesJson(
  upstreamBody: AsyncIterable<Uint8Array>,
  input: {
    estimatedInputTokens?: number
    finishReasonFailures?: Record<string, CodexResponsesChatBridgeFinishReasonFailure>
    idPrefix?: string
    model: string
    previousResponseId?: string
    toolAdaptersByChatName?: Map<string, CodexResponsesChatBridgeToolAdapter>
    onCompleted?: CodexResponsesChatBridgeCompletionHandler
  }
): AsyncIterable<Uint8Array> {
  const state = createChatToResponsesState(
    input.model,
    input.idPrefix,
    input.estimatedInputTokens,
    input.previousResponseId,
    input.finishReasonFailures,
    input.toolAdaptersByChatName
  )
  const decoder = new StringDecoder('utf8')
  let pending = ''
  for await (const chunk of upstreamBody) {
    pending += decoder.write(Buffer.from(chunk))
    const events = takeCompleteSseEvents(pending)
    pending = events.rest
    for (const eventText of events.events) {
      processChatSseEvent(state, eventText)
      await notifyCodexResponsesChatBridgeCompletion(state, input.onCompleted)
    }
  }

  pending += decoder.end()
  if (pending.trim()) {
    processChatSseEvent(state, pending)
    await notifyCodexResponsesChatBridgeCompletion(state, input.onCompleted)
  }
  if (!state.terminalReceived && !state.completed && !state.failed) {
    failResponsesStream(state, '上游 Chat SSE 在正常结束事件前中断', 'upstream_stream_interrupted')
  }
  await notifyCodexResponsesChatBridgeCompletion(state, input.onCompleted)
  const payload = state.failed
    ? responseFailedSnapshot(
      state,
      state.failureMessage ?? '上游 Chat SSE 返回错误',
      state.failureCode ?? 'upstream_error'
    )
    : responseSnapshot(state, 'completed', state.outputItems)
  yield Buffer.from(JSON.stringify(payload), 'utf8')
}

function createChatToResponsesState(
  model: string,
  idPrefix = 'chat_bridge',
  estimatedInputTokens?: number,
  previousResponseId?: string,
  finishReasonFailures?: Record<string, CodexResponsesChatBridgeFinishReasonFailure>,
  toolAdaptersByChatName?: Map<string, CodexResponsesChatBridgeToolAdapter>
): ChatToResponsesState {
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
    pendingToolCalls: new Map(),
    toolAdaptersByChatName: new Map(toolAdaptersByChatName ?? []),
    estimatedInputTokens,
    finishReasonFailures: new Map(Object.entries(finishReasonFailures ?? {})),
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
    const failure = upstreamChatSseErrorFailure(objectValue(event.data?.error) ?? event.data)
    return failResponsesStream(state, failure.message, failure.code)
  }
  const data = event.data
  if (!data) return []
  const error = objectValue(data.error)
  if (error) {
    const failure = upstreamChatSseErrorFailure(error)
    return failResponsesStream(state, failure.message, failure.code)
  }
  const output: string[] = []
  const appendOutput = (events: string[]): void => {
    if (events.length === 0) return
    output.push(...ensureResponsesStreamStarted(state), ...events)
  }
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
        appendOutput(appendResponsesReasoningDelta(state, reasoningText))
      }
      const text = stringValue(delta.content) ?? stringValue(delta.refusal)
      if (text) {
        appendOutput(appendResponsesTextDelta(state, text))
      }
      for (const toolCall of chatToolCallDeltas(delta)) {
        appendOutput(appendResponsesToolCallDelta(state, toolCall))
      }
    }
    if (typeof choice.finish_reason === 'string') {
      state.terminalReceived = true
      const finishReasonFailure = state.finishReasonFailures.get(choice.finish_reason)
      if (finishReasonFailure) {
        appendOutput(completeOpenOutputItems(state))
        output.push(...failResponsesStream(state, finishReasonFailure.message, finishReasonFailure.code))
        continue
      }
      appendOutput(completeOpenOutputItems(state))
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

function chatToolCallDeltas(delta: JsonRecord): unknown[] {
  const output: unknown[] = []
  if (Array.isArray(delta.tool_calls)) {
    output.push(...delta.tool_calls)
  }
  return output
}

function appendResponsesToolCallDelta(state: ChatToResponsesState, value: unknown): string[] {
  if (!isPlainObject(value)) return []
  const index = integerValue(value.index) ?? 0
  const existingToolCall = state.toolCalls.get(index)
  const toolCall = existingToolCall ?? (() => {
    const callId = stringValue(value.id) ?? `call_${state.idPrefix}_${index}_${Date.now().toString(36)}`
    const created: ChatToolCallState = {
      id: `fc_${state.idPrefix}_${index}_${Date.now().toString(36)}`,
      callId,
      name: '',
      arguments: '',
      outputIndex: state.nextOutputIndex++,
      added: false,
      done: false
    }
    state.toolCalls.set(index, created)
    return created
  })()
  const activeToolCall = toolCall
  const fn = objectValue(value.function)
  const chatName = stringValue(fn?.name)
  if (chatName) {
    const adapter = state.toolAdaptersByChatName.get(chatName)
    activeToolCall.adapter = adapter ?? activeToolCall.adapter
    activeToolCall.name = adapter?.responsesName ?? chatName
  }
  const argumentsDelta = stringValue(fn?.arguments) ?? ''
  if (argumentsDelta) {
    activeToolCall.arguments += argumentsDelta
  }
  const pending = state.pendingToolCalls.get(index) ?? {
    upstreamCallId: stringValue(value.id),
    name: '',
    arguments: '',
    outputIndex: state.nextOutputIndex++
  }
  if (!pending.upstreamCallId) pending.upstreamCallId = stringValue(value.id)
  if (chatName) pending.name = mergeChatToolName(pending.name, chatName)
  if (argumentsDelta) pending.arguments += argumentsDelta
  const adapter = pending.name ? state.toolAdaptersByChatName.get(pending.name) : undefined
  if (!adapter) {
    state.pendingToolCalls.set(index, pending)
    return []
  }

  const suffix = `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`
  const identity = createCodexBridgeToolIdentity({
    adapterKind: adapter.kind,
    idPrefix: state.idPrefix,
    index,
    upstreamCallId: pending.upstreamCallId,
    suffix
  })
  const toolCall: ChatToolCallState = {
    id: identity.itemId,
    itemType: identity.itemType,
    callId: identity.callId,
    name: adapter.responsesName,
    arguments: pending.arguments,
    adapter,
    outputIndex: pending.outputIndex,
    added: false,
    done: false
  }
  state.pendingToolCalls.delete(index)
  state.toolCalls.set(index, toolCall)
  return emitToolCallAdded(toolCall)
}

function emitToolCallAdded(toolCall: ChatToolCallState): string[] {
  if (toolCall.added) return []
  toolCall.added = true
  return [sse('response.output_item.added', {
    type: 'response.output_item.added',
    output_index: toolCall.outputIndex,
    item: toolCallInProgressItem(toolCall)
  })]
}

function mergeChatToolName(current: string, incoming: string): string {
  if (!current || current === incoming || current.endsWith(incoming)) return current || incoming
  return `${current}${incoming}`
}

function completeOpenOutputItems(state: ChatToResponsesState): string[] {
  const output: string[] = []
  const completions: Array<{ outputIndex: number; complete: () => void }> = []
  if (state.textStarted && !state.textDone) {
    completions.push({
      outputIndex: state.textOutputIndex ?? 0,
      complete: () => completeTextOutputItem(state, output)
    })
  }
  if (state.reasoningStarted && !state.reasoningDone) {
    completions.push({
      outputIndex: state.reasoningOutputIndex ?? 0,
      complete: () => completeReasoningOutputItem(state, output)
    })
  }
  for (const toolCall of state.toolCalls.values()) {
    if (toolCall.done) continue
    completions.push({
      outputIndex: toolCall.outputIndex,
      complete: () => completeToolCallOutputItem(state, output, toolCall)
    })
  }
  completions.sort((a, b) => a.outputIndex - b.outputIndex)
  for (const completion of completions) {
    completion.complete()
  }
  return output
}

function completeTextOutputItem(state: ChatToResponsesState, output: string[]): void {
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

function completeReasoningOutputItem(state: ChatToResponsesState, output: string[]): void {
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

function completeToolCallOutputItem(
  state: ChatToResponsesState,
  output: string[],
  toolCall: ChatToolCallState
): void {
  if (!toolCall.added) {
    toolCall.added = true
    output.push(sse('response.output_item.added', {
      type: 'response.output_item.added',
      output_index: toolCall.outputIndex,
      item: toolCallInProgressItem(toolCall)
    }))
  }
  toolCall.done = true
  const item = completedToolCallItem(toolCall)
  output.push(sse('response.output_item.done', {
    type: 'response.output_item.done',
    output_index: toolCall.outputIndex,
    item
  }))
  state.outputItems.push(item)
}

function toolCallInProgressItem(toolCall: ChatToolCallState): JsonRecord {
  if (toolCall.adapter?.kind === 'custom') {
    return {
      id: toolCall.id,
      type: 'custom_tool_call',
      status: 'in_progress',
      call_id: toolCall.callId,
      name: toolCall.name,
      input: ''
    }
  }
  const item: JsonRecord = {
    id: toolCall.id,
    type: 'function_call',
    status: 'in_progress',
    call_id: toolCall.callId,
    name: toolCall.name,
    arguments: ''
  }
  if (toolCall.adapter?.namespace) {
    item.namespace = toolCall.adapter.namespace
  }
  return item
}

function completedToolCallItem(toolCall: ChatToolCallState): JsonRecord {
  if (toolCall.adapter?.kind === 'custom') {
    return {
      id: toolCall.id,
      type: 'custom_tool_call',
      status: 'completed',
      call_id: toolCall.callId,
      name: toolCall.name,
      input: customToolInputFromChatArguments(toolCall.arguments, toolCall.adapter?.responsesName)
    }
  }
  const item: JsonRecord = {
    id: toolCall.id,
    type: 'function_call',
    status: 'completed',
    call_id: toolCall.callId,
    name: toolCall.name,
    arguments: toolCall.arguments
  }
  if (toolCall.adapter?.namespace) {
    item.namespace = toolCall.adapter.namespace
  }
  return item
}

function customToolInputFromChatArguments(argumentsText: string, toolName?: string): string {
  try {
    const parsed = JSON.parse(argumentsText) as unknown
    if (isPlainObject(parsed)) {
      const input = stringValue(parsed.input)
      if (input) return input
      if (toolName === 'apply_patch') {
        const patch = applyPatchInputFromStructuredFiles(parsed.files)
        if (patch) return patch
      }
    }
  } catch {
  }
  return argumentsText
}

function applyPatchInputFromStructuredFiles(value: unknown): string | undefined {
  if (!Array.isArray(value)) return undefined
  const hunks: string[] = []
  for (const item of value) {
    if (!isPlainObject(item)) continue
    const path = stringValue(item.path)
    const content = stringValue(item.content)
    if (!path || content === undefined || path.includes('\n') || path.includes('\r')) continue
    hunks.push(`*** Add File: ${path}`)
    const lines = content.split(/\r?\n/)
    for (const line of lines) {
      hunks.push(`+${line}`)
    }
  }
  if (hunks.length === 0) return undefined
  return ['*** Begin Patch', ...hunks, '*** End Patch', ''].join('\n')
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
  const output = state.started ? ensureResponsesStreamStarted(state) : []
  state.failed = true
  state.failureMessage = message
  state.failureCode = code
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

function upstreamChatSseErrorFailure(error: JsonRecord | undefined): { message: string; code: string } {
  return {
    message: stringValue(error?.message) ?? '上游 Chat SSE 返回错误事件',
    code: stringValue(error?.code) ?? stringValue(error?.type) ?? 'upstream_error'
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
