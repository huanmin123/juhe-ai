import { StringDecoder } from 'node:string_decoder'
import type { Request } from 'express'

import type {
  AccountModelMappingSourceEndpointFamily,
  AccountSupportedEndpointMode
} from '../../../../domain/types.js'
import {
  ANTHROPIC_MESSAGES_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_RESPONSES_FAMILY
} from '../../../../domain/provider-protocol.js'
import { openAIEndpointFamilyFromPath } from '../../../../domain/openai-endpoint-modes.js'
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
import { requestModel, requestStream } from '../../../gateway/request/metadata.js'
import {
  estimateTokenCountFromText
} from '../../../gateway/protocols/openai-v1/stream-events.js'
import { splitPathAndQuery } from '../../../gateway/protocols/openai-v1/route-helpers.js'
import type { GatewayUpstreamResponse } from '../../../gateway/upstream/request.js'
import type { CodexResponsesChatBridgeCompletionHandler } from '../../../gateway/codex-responses/chat-bridge-state.js'
import {
  runtimeCodexResponsesWebSearchExecutor,
  type CodexResponsesWebSearchResult,
  type CodexResponsesWebSearchToolConfig
} from '../../../gateway/codex-responses/web-search-executor.js'
import {
  resolveOpenAIAccountModelMapping,
  type OpenAIModelMappingRuntimeAccount
} from '../../../gateway/protocols/openai-v1/model-mapping.js'

type JsonRecord = Record<string, unknown>

export interface OpenAIToAnthropicBridgeBodyOptions {
  defaultMaxTokens?: number
  modelOverride?: string
  fileResolver?: OpenAIToAnthropicFileResolver
  fileSearchExecutor?: OpenAIToAnthropicFileSearchExecutor
}

export interface OpenAIToAnthropicFileResolveInput {
  fileId: string
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily
  usage: 'input_image' | 'input_file' | 'chat_file'
  signal?: AbortSignal
}

export interface OpenAIToAnthropicResolvedFile {
  fileId: string
  filename?: string
  mediaType?: string
  bytes?: number
  contentBase64?: string
  contentText?: string
}

export interface OpenAIToAnthropicFileResolver {
  resolveFile(input: OpenAIToAnthropicFileResolveInput): Promise<OpenAIToAnthropicResolvedFile | undefined>
}

export interface OpenAIToAnthropicFileSearchInput {
  vectorStoreIds: string[]
  query: string
  maxNumResults?: number
  filters?: JsonRecord
  rankingOptions?: JsonRecord
  signal?: AbortSignal
}

export interface OpenAIToAnthropicFileSearchResult {
  fileId: string
  filename: string
  score: number
  contentText: string
}

export interface OpenAIToAnthropicFileSearchExecutor {
  search(input: OpenAIToAnthropicFileSearchInput): Promise<{
    queries?: string[]
    results: OpenAIToAnthropicFileSearchResult[]
  }>
}

interface OpenAIToAnthropicBridgeTransformOptions {
  model?: string
  previousResponseId?: string
  onResponsesCompleted?: CodexResponsesChatBridgeCompletionHandler
}

interface OpenAIToAnthropicBridgeRequestPlan {
  structuredOutput?: OpenAIToAnthropicStructuredOutputPlan
  reasoningEffort?: string
  webSearch?: OpenAIToAnthropicWebSearchPlan
  fileSearch?: OpenAIToAnthropicFileSearchPlan
}

interface OpenAIToAnthropicBridgeContentContext {
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily
  fileResolver?: OpenAIToAnthropicFileResolver
  signal?: AbortSignal
}

interface OpenAIToAnthropicStructuredOutputPlan {
  type: 'json_object' | 'json_schema'
  schema?: JsonRecord
  name?: string
  strict?: boolean
  syntheticToolName?: string
}

interface StructuredOutputResult {
  text: string
  error?: JsonRecord
}

interface OpenAIToAnthropicWebSearchPlan {
  query: string
  tool: CodexResponsesWebSearchToolConfig
  results: CodexResponsesWebSearchResult[]
  outputItemId?: string
  emitted?: boolean
}

interface OpenAIToAnthropicFileSearchToolConfig {
  vectorStoreIds: string[]
  maxNumResults?: number
  filters?: JsonRecord
  rankingOptions?: JsonRecord
}

interface OpenAIToAnthropicFileSearchPlan {
  query: string
  queries: string[]
  tool: OpenAIToAnthropicFileSearchToolConfig
  results: OpenAIToAnthropicFileSearchResult[]
  includeResults: boolean
  outputItemId?: string
  emitted?: boolean
}

interface AnthropicMessage {
  role: 'user' | 'assistant'
  content: AnthropicContentBlock[]
}

type AnthropicContentBlock = JsonRecord

interface ParsedSseEvent {
  eventName?: string
  dataText: string
  data?: JsonRecord
  dataParseError?: boolean
}

interface AnthropicStreamBlockState {
  index: number
  type: string
  id?: string
  name?: string
  text: string
  inputJson: string
  outputIndex?: number
  done: boolean
}

interface AnthropicStreamState {
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily
  id: string
  chatId: string
  responseId: string
  createdAt: number
  model: string
  requestPlan?: OpenAIToAnthropicBridgeRequestPlan
  previousResponseId?: string
  roleSent: boolean
  responsesStarted: boolean
  responseMessageId: string
  nextOutputIndex: number
  blocks: Map<number, AnthropicStreamBlockState>
  outputItems: JsonRecord[]
  usage?: JsonRecord
  stopReason?: string
  completed: boolean
  failed: boolean
  terminalReceived: boolean
  completionNotified: boolean
}

const defaultAnthropicMaxTokens = 4096
const structuredOutputSyntheticToolName = 'emit_structured_output'
const structuredOutputSchemaMismatchCode = 'openai_anthropic_bridge_structured_output_schema_mismatch'
const bridgeRequestPlanSymbol: unique symbol = Symbol('openAIToAnthropicBridgeRequestPlan')
let openAIToAnthropicBridgeFileResolverForTest: OpenAIToAnthropicFileResolver | undefined

type OpenAIToAnthropicBridgePlannedRequest = Request & {
  [bridgeRequestPlanSymbol]?: OpenAIToAnthropicBridgeRequestPlan
}

export function openAIToAnthropicBridgeEndpointFamily(req: Request): AccountModelMappingSourceEndpointFamily | undefined {
  if (req.method.toUpperCase() !== 'POST') return undefined
  const { path } = splitPathAndQuery(req.originalUrl || req.path || '')
  const normalizedPath = normalizedOpenAIPath(path)
  return openAIEndpointFamilyFromPath(normalizedPath) as AccountModelMappingSourceEndpointFamily | undefined
}

export function isOpenAIToAnthropicBridgeCandidateRequest(req: Request): boolean {
  const family = openAIToAnthropicBridgeEndpointFamily(req)
  return family === OPENAI_CHAT_COMPLETIONS_FAMILY || family === OPENAI_RESPONSES_FAMILY
}

export function isOpenAIToAnthropicMessagesModelMapping(
  req: Request,
  account: OpenAIModelMappingRuntimeAccount | undefined
): boolean {
  const mapping = resolveOpenAIAccountModelMapping(account, requestModel(req), openAIToAnthropicBridgeEndpointFamily(req))
  return mapping?.upstreamEndpointFamily === ANTHROPIC_MESSAGES_FAMILY
}

export function openAIToAnthropicBridgeUpstreamPath(req: Request): string | undefined {
  if (!isOpenAIToAnthropicBridgeCandidateRequest(req)) return undefined
  const { query } = splitPathAndQuery(req.originalUrl || req.path || '')
  return `/messages${query}`
}

export function openAIToAnthropicBridgeRequiredEndpointMode(req: Request): AccountSupportedEndpointMode | undefined {
  if (!isOpenAIToAnthropicBridgeCandidateRequest(req)) return undefined
  return requestStream(req) ? 'messages_sse' : 'messages_json'
}

export function openAIToAnthropicBridgeUpstreamModel(
  req: Request,
  account: OpenAIModelMappingRuntimeAccount | undefined
): string | undefined {
  const mapping = resolveOpenAIAccountModelMapping(account, requestModel(req), openAIToAnthropicBridgeEndpointFamily(req))
  return mapping?.upstreamEndpointFamily === ANTHROPIC_MESSAGES_FAMILY
    ? mapping.upstreamModel
    : requestModel(req)
}

export function prepareOpenAIToAnthropicBridgeHeaders(headers: Headers, req: Request): void {
  headers.set('accept', requestStream(req) ? 'text/event-stream' : 'application/json')
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

export function setOpenAIToAnthropicBridgeFileResolverForTest(
  resolver: OpenAIToAnthropicFileResolver | undefined
): void {
  openAIToAnthropicBridgeFileResolverForTest = resolver
}

export async function buildOpenAIToAnthropicBridgeBody(
  req: Request,
  options: OpenAIToAnthropicBridgeBodyOptions,
  signal?: AbortSignal
): Promise<Buffer> {
  const sourceEndpointFamily = openAIToAnthropicBridgeEndpointFamily(req)
  if (!sourceEndpointFamily) {
    throw bridgeValidationError('当前请求不是可桥接的 OpenAI Chat/Responses 请求', 'openai_anthropic_bridge_unsupported_endpoint')
  }
  const body = await parseGatewayJsonObject(req, signal)
  const model = options.modelOverride ?? stringValue(body.model)
  if (!model) {
    throw bridgeValidationError('OpenAI 到 Anthropic 桥接请求缺少 model', 'openai_anthropic_bridge_missing_model')
  }
  const requestPlan = createOpenAIToAnthropicBridgeRequestPlan(sourceEndpointFamily, body)
  await applyOpenAIToAnthropicLocalToolEmulation(sourceEndpointFamily, body, requestPlan, options, signal)
  setOpenAIToAnthropicBridgeRequestPlan(req, requestPlan)
  const contentContext: OpenAIToAnthropicBridgeContentContext = {
    sourceEndpointFamily,
    fileResolver: openAIToAnthropicBridgeFileResolverForTest ?? options.fileResolver,
    signal
  }
  const anthropicBody = sourceEndpointFamily === OPENAI_RESPONSES_FAMILY
    ? await responsesBodyToAnthropicMessages(body, model, options, requestPlan, contentContext)
    : await chatBodyToAnthropicMessages(body, model, options, requestPlan, contentContext)
  return Buffer.from(JSON.stringify(anthropicBody), 'utf8')
}

export function transformOpenAIToAnthropicBridgeUpstreamResponse(
  req: Request,
  response: GatewayUpstreamResponse,
  options: OpenAIToAnthropicBridgeTransformOptions = {}
): GatewayUpstreamResponse {
  const sourceEndpointFamily = openAIToAnthropicBridgeEndpointFamily(req)
  if (!sourceEndpointFamily || !response.body) {
    return response
  }
  if (!response.ok) {
    const headers = new Headers(response.headers)
    headers.set('content-type', 'application/json; charset=utf-8')
    headers.delete('content-length')
    return {
      status: response.status,
      ok: response.ok,
      headers,
      body: transformAnthropicErrorBodyToOpenAIErrorBody(response.body)
    }
  }
  const model = options.model ?? requestModel(req) ?? 'anthropic'
  const requestPlan = openAIToAnthropicBridgeRequestPlan(req)
  if (requestStream(req)) {
    const headers = new Headers(response.headers)
    headers.set('content-type', 'text/event-stream; charset=utf-8')
    headers.delete('content-length')
    return {
      status: response.status,
      ok: response.ok,
      headers,
      body: sourceEndpointFamily === OPENAI_RESPONSES_FAMILY
        ? transformAnthropicMessagesSseToResponsesSse(response.body, {
          model,
          requestPlan,
          previousResponseId: options.previousResponseId,
          onResponsesCompleted: options.onResponsesCompleted
        })
        : transformAnthropicMessagesSseToChatSse(response.body, { model, requestPlan })
    }
  }
  const headers = new Headers(response.headers)
  headers.set('content-type', 'application/json; charset=utf-8')
  headers.delete('content-length')
  return {
    status: response.status,
    ok: response.ok,
    headers,
    body: sourceEndpointFamily === OPENAI_RESPONSES_FAMILY
      ? transformAnthropicMessagesJsonToResponsesJson(response.body, {
        model,
        requestPlan,
        previousResponseId: options.previousResponseId,
        onResponsesCompleted: options.onResponsesCompleted
      })
      : transformAnthropicMessagesJsonToChatJson(response.body, { model, requestPlan })
  }
}

async function chatBodyToAnthropicMessages(
  body: JsonRecord,
  model: string,
  options: OpenAIToAnthropicBridgeBodyOptions,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  contentContext: OpenAIToAnthropicBridgeContentContext
): Promise<JsonRecord> {
  const messages: AnthropicMessage[] = []
  const systemParts: string[] = []
  const inputMessages = Array.isArray(body.messages) ? body.messages : []
  for (const item of inputMessages) {
    if (!isPlainObject(item)) continue
    const role = stringValue(item.role)
    if (role === 'system' || role === 'developer') {
      appendSystemText(systemParts, openAIContentToText(item.content))
      continue
    }
    if (role === 'tool') {
      appendAnthropicMessage(messages, {
        role: 'user',
        content: [{
          type: 'tool_result',
          tool_use_id: stringValue(item.tool_call_id) ?? stringValue(item.id) ?? `tool_${messages.length}`,
          content: openAIContentToText(item.content)
        }]
      })
      continue
    }
    if (role !== 'user' && role !== 'assistant') continue
    const content = await openAIChatContentToAnthropicBlocks(item.content, contentContext)
    if (role === 'assistant') {
      const toolUseBlocks = chatToolCallsToAnthropicToolUseBlocks(item.tool_calls)
      content.push(...toolUseBlocks)
    }
    appendAnthropicMessage(messages, {
      role,
      content: content.length ? content : [{ type: 'text', text: '' }]
    })
  }
  if (!messages.length) {
    appendAnthropicMessage(messages, { role: 'user', content: [{ type: 'text', text: '' }] })
  }
  appendFileSearchContext(systemParts, requestPlan.fileSearch)
  const output = baseAnthropicBody(body, model, options)
  output.messages = messages
  const system = systemParts.join('\n\n').trim()
  if (system) output.system = system
  const tools = chatToolsToAnthropicTools(body.tools, requestPlan)
  applyStructuredOutputPlan(output, tools, body.tool_choice, requestPlan.structuredOutput)
  if (!requestPlan.structuredOutput?.syntheticToolName) {
    appendJsonOutputInstruction(output, jsonInstructionFromStructuredOutputPlan(requestPlan.structuredOutput))
  }
  return output
}

async function responsesBodyToAnthropicMessages(
  body: JsonRecord,
  model: string,
  options: OpenAIToAnthropicBridgeBodyOptions,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  contentContext: OpenAIToAnthropicBridgeContentContext
): Promise<JsonRecord> {
  if (stringValue(body.previous_response_id)) {
    throw bridgeValidationError(
      'previous_response_id 尚未被网关上下文状态层恢复，不能直接转发到 Anthropic Messages',
      'openai_anthropic_bridge_previous_response_state_unavailable'
    )
  }
  const messages: AnthropicMessage[] = []
  const systemParts: string[] = []
  appendSystemText(systemParts, stringValue(body.instructions))
  const input = body.input
  if (typeof input === 'string') {
    appendAnthropicMessage(messages, {
      role: 'user',
      content: [{ type: 'text', text: input }]
    })
  } else if (Array.isArray(input)) {
    for (const item of input) {
      await appendResponsesInputItemAsAnthropicMessage(messages, systemParts, item, contentContext)
    }
  } else if (isPlainObject(input)) {
    await appendResponsesInputItemAsAnthropicMessage(messages, systemParts, input, contentContext)
  }
  if (!messages.length) {
    appendAnthropicMessage(messages, { role: 'user', content: [{ type: 'text', text: '' }] })
  }
  appendFileSearchContext(systemParts, requestPlan.fileSearch)
  const output = baseAnthropicBody(body, model, options)
  output.messages = messages
  const system = systemParts.join('\n\n').trim()
  if (system) output.system = system
  const tools = responsesToolsToAnthropicTools(body.tools, requestPlan)
  applyStructuredOutputPlan(output, tools, body.tool_choice, requestPlan.structuredOutput)
  if (!requestPlan.structuredOutput?.syntheticToolName) {
    appendJsonOutputInstruction(output, jsonInstructionFromStructuredOutputPlan(requestPlan.structuredOutput))
  }
  return output
}

function baseAnthropicBody(
  body: JsonRecord,
  model: string,
  options: OpenAIToAnthropicBridgeBodyOptions
): JsonRecord {
  const output: JsonRecord = {
    model,
    max_tokens: integerValue(body.max_tokens)
      ?? integerValue(body.max_completion_tokens)
      ?? integerValue(body.max_output_tokens)
      ?? options.defaultMaxTokens
      ?? defaultAnthropicMaxTokens,
    stream: requestStreamObject(body)
  }
  if (typeof body.temperature === 'number') output.temperature = body.temperature
  if (typeof body.top_p === 'number') output.top_p = body.top_p
  const stopSequences = stopSequencesValue(body.stop)
  if (stopSequences.length) output.stop_sequences = stopSequences
  const user = stringValue(body.user)
  if (user) {
    output.metadata = { user_id: user }
  }
  applyAnthropicThinkingFromOpenAIReasoning(output, body)
  return output
}

function requestStreamObject(body: JsonRecord): boolean {
  return body.stream === true
}

async function appendResponsesInputItemAsAnthropicMessage(
  messages: AnthropicMessage[],
  systemParts: string[],
  item: unknown,
  contentContext: OpenAIToAnthropicBridgeContentContext
): Promise<void> {
  if (!isPlainObject(item)) return
  if (item.type === 'message') {
    const role = stringValue(item.role)
    if (role === 'system' || role === 'developer') {
      appendSystemText(systemParts, responsesContentToText(item.content))
      return
    }
    if (role === 'user' || role === 'assistant') {
      appendAnthropicMessage(messages, {
        role,
        content: await responsesContentToAnthropicBlocks(item.content, contentContext)
      })
    }
    return
  }
  if (item.type === 'function_call') {
    const name = stringValue(item.name)
    const callId = stringValue(item.call_id) ?? stringValue(item.id)
    if (!name || !callId) return
    appendAnthropicMessage(messages, {
      role: 'assistant',
      content: [{
        type: 'tool_use',
        id: callId,
        name,
        input: parseJsonObjectString(item.arguments) ?? {}
      }]
    })
    return
  }
  if (item.type === 'function_call_output') {
    const callId = stringValue(item.call_id)
    if (!callId) return
    appendAnthropicMessage(messages, {
      role: 'user',
      content: [{
        type: 'tool_result',
        tool_use_id: callId,
        content: responsesTextFromValue(item.output)
      }]
    })
    return
  }
  if (item.type === 'reasoning') {
    const text = responsesReasoningTextFromItem(item)
    if (text) appendSystemText(systemParts, `历史推理摘要：\n${text}`)
    return
  }
  if (item.type === 'compaction' || item.type === 'compaction_summary') {
    const summary = responsesCompactionSummaryTextFromItem(item)
    if (summary) appendSystemText(systemParts, `上下文摘要：\n${summary}`)
  }
}

function appendAnthropicMessage(messages: AnthropicMessage[], next: AnthropicMessage): void {
  const previous = messages[messages.length - 1]
  if (previous?.role === next.role) {
    previous.content.push(...next.content)
    return
  }
  messages.push(next)
}

function appendSystemText(parts: string[], value: string | undefined): void {
  const text = value?.trim()
  if (text) parts.push(text)
}

async function openAIChatContentToAnthropicBlocks(
  value: unknown,
  contentContext: OpenAIToAnthropicBridgeContentContext
): Promise<AnthropicContentBlock[]> {
  if (typeof value === 'string') return [{ type: 'text', text: value }]
  if (!Array.isArray(value)) return []
  const blocks: AnthropicContentBlock[] = []
  for (const item of value) {
    if (!isPlainObject(item)) continue
    if (item.type === 'text') {
      const text = stringValue(item.text)
      if (text) blocks.push({ type: 'text', text })
      continue
    }
    if (item.type === 'image_url') {
      const imageUrl = imageUrlFromOpenAIImagePart(item.image_url)
      if (imageUrl) blocks.push(anthropicImageBlockFromUrl(imageUrl))
      else throw bridgeValidationError(
        'Chat image_url 缺少可桥接的 url，当前 OpenAI 到 Anthropic 桥接只支持 URL 或 data URL 图片输入',
        'openai_anthropic_bridge_unsupported_image_reference'
      )
      continue
    }
    if (item.type === 'file' || item.type === 'input_file') {
      blocks.push(await anthropicDocumentBlockFromOpenAIFilePart(item, 'Chat', contentContext))
    }
  }
  return blocks
}

async function responsesContentToAnthropicBlocks(
  value: unknown,
  contentContext: OpenAIToAnthropicBridgeContentContext
): Promise<AnthropicContentBlock[]> {
  if (typeof value === 'string') return [{ type: 'text', text: value }]
  if (!Array.isArray(value)) return []
  const blocks: AnthropicContentBlock[] = []
  for (const item of value) {
    if (!isPlainObject(item)) continue
    if (item.type === 'input_text' || item.type === 'output_text' || item.type === 'text' || item.type === undefined) {
      const text = stringValue(item.text)
      if (text) blocks.push({ type: 'text', text })
      continue
    }
    if (item.type === 'input_image') {
      const imageUrl = stringValue(item.image_url)
      if (imageUrl) blocks.push(anthropicImageBlockFromUrl(imageUrl))
      else if (stringValue(item.file_id)) {
        blocks.push(await anthropicImageBlockFromOpenAIFileId(stringValue(item.file_id)!, contentContext))
      } else {
        throw bridgeValidationError(
          'Responses input_image 缺少 image_url 或 file_id',
          'openai_anthropic_bridge_invalid_image_input'
        )
      }
      continue
    }
    if (item.type === 'input_file' || item.type === 'file') {
      blocks.push(await anthropicDocumentBlockFromOpenAIFilePart(item, 'Responses', contentContext))
    }
  }
  return blocks
}

function anthropicImageBlockFromUrl(url: string): AnthropicContentBlock {
  const dataUrl = parseDataUrl(url)
  if (dataUrl) {
    return {
      type: 'image',
      source: {
        type: 'base64',
        media_type: dataUrl.mediaType,
        data: dataUrl.data
      }
    }
  }
  return {
    type: 'image',
    source: {
      type: 'url',
      url
    }
  }
}

async function anthropicDocumentBlockFromOpenAIFilePart(
  item: JsonRecord,
  sourceFamily: 'Chat' | 'Responses',
  contentContext: OpenAIToAnthropicBridgeContentContext
): Promise<AnthropicContentBlock> {
  const file = objectValue(item.file) ?? item
  const filename = stringValue(file.filename) ?? stringValue(item.filename) ?? stringValue(file.name) ?? stringValue(item.name)
  const title = filename ? filename.split(/[\\/]/).pop() : undefined
  const fileId = stringValue(file.file_id) ?? stringValue(item.file_id)
  if (fileId) {
    const resolved = await resolveOpenAIFileReference(fileId, sourceFamily === 'Chat' ? 'chat_file' : 'input_file', contentContext)
    return anthropicDocumentBlockFromResolvedOpenAIFile(resolved, { filename, title })
  }

  const fileData = stringValue(file.file_data) ?? stringValue(item.file_data)
  if (fileData) {
    return anthropicDocumentBlockFromOpenAIFileData(fileData, {
      filename,
      title,
      mediaType: stringValue(file.media_type)
        ?? stringValue(file.mime_type)
        ?? stringValue(item.media_type)
        ?? stringValue(item.mime_type)
    })
  }

  const fileUrl = stringValue(file.file_url) ?? stringValue(item.file_url)
  if (fileUrl) {
    if (sourceFamily === 'Chat') {
      throw bridgeValidationError(
        'OpenAI Chat file content 当前只支持 file_data；file_url 只在 Responses input_file 中桥接',
        'openai_anthropic_bridge_invalid_file_input'
      )
    }
    return anthropicDocumentBlockFromOpenAIFileUrl(fileUrl, { filename, title })
  }

  throw bridgeValidationError(
    'OpenAI 文件输入缺少可桥接的 file_data、file_url 或 file_id',
    'openai_anthropic_bridge_invalid_file_input'
  )
}

async function anthropicImageBlockFromOpenAIFileId(
  fileId: string,
  contentContext: OpenAIToAnthropicBridgeContentContext
): Promise<AnthropicContentBlock> {
  const resolved = await resolveOpenAIFileReference(fileId, 'input_image', contentContext)
  const mediaType = normalizeOpenAIFileMediaType(resolved.mediaType, resolved.filename)
  if (!isAnthropicSupportedImageMediaType(mediaType)) {
    throw bridgeValidationError(
      `OpenAI 到 Anthropic 桥接只支持 JPEG、PNG、GIF 或 WEBP 图片文件，不能桥接 ${mediaType ?? 'unknown'} 文件`,
      'openai_anthropic_bridge_unsupported_file_media_type'
    )
  }
  const base64Data = normalizedBase64Data(resolved.contentBase64 ?? '')
  if (!base64Data) {
    throw bridgeValidationError(
      'OpenAI Files resolver 返回的图片内容缺少合法 base64 数据',
      'openai_anthropic_bridge_invalid_file_input'
    )
  }
  return {
    type: 'image',
    source: {
      type: 'base64',
      media_type: mediaType,
      data: base64Data
    }
  }
}

async function resolveOpenAIFileReference(
  fileId: string,
  usage: OpenAIToAnthropicFileResolveInput['usage'],
  contentContext: OpenAIToAnthropicBridgeContentContext
): Promise<OpenAIToAnthropicResolvedFile> {
  const resolver = contentContext.fileResolver
  if (!resolver) {
    throw bridgeValidationError(
      `OpenAI 到 Anthropic 桥接当前未配置本地 Files resolver，不能桥接 ${usage === 'input_image' ? 'input_image.file_id' : 'file_id 文件引用'}`,
      'openai_anthropic_bridge_file_reference_unsupported'
    )
  }
  try {
    const resolved = await resolver.resolveFile({
      fileId,
      sourceEndpointFamily: contentContext.sourceEndpointFamily,
      usage,
      signal: contentContext.signal
    })
    if (!resolved) {
      throw bridgeValidationError(
        `OpenAI Files resolver 未找到文件 ${fileId}`,
        'openai_anthropic_bridge_file_not_found',
        404
      )
    }
    return resolved
  } catch (error) {
    if (error instanceof GatewayRequestValidationError) throw error
    throw bridgeValidationError(
      error instanceof Error ? error.message : `OpenAI Files resolver 读取文件 ${fileId} 失败`,
      'openai_anthropic_bridge_file_resolver_failed',
      502,
      'upstream_error'
    )
  }
}

function anthropicDocumentBlockFromResolvedOpenAIFile(
  resolved: OpenAIToAnthropicResolvedFile,
  input: { filename?: string; title?: string }
): AnthropicContentBlock {
  const filename = input.filename ?? resolved.filename
  const title = input.title ?? filename?.split(/[\\/]/).pop()
  const mediaType = normalizeOpenAIFileMediaType(resolved.mediaType, filename)
  if (mediaType === 'application/pdf') {
    const base64Data = normalizedBase64Data(resolved.contentBase64 ?? '')
    if (!base64Data) {
      throw bridgeValidationError(
        'OpenAI Files resolver 返回的 PDF 内容缺少合法 base64 数据',
        'openai_anthropic_bridge_invalid_file_input'
      )
    }
    return withAnthropicDocumentTitle({
      type: 'document',
      source: {
        type: 'base64',
        media_type: 'application/pdf',
        data: base64Data
      }
    }, title)
  }

  if (isTextFileMediaType(mediaType)) {
    const normalizedBase64 = resolved.contentBase64 ? normalizedBase64Data(resolved.contentBase64) : undefined
    const text = resolved.contentText ?? (normalizedBase64 ? Buffer.from(normalizedBase64, 'base64').toString('utf8') : undefined)
    if (text === undefined) {
      throw bridgeValidationError(
        'OpenAI Files resolver 返回的文本文件内容缺少 text 或合法 base64 数据',
        'openai_anthropic_bridge_invalid_file_input'
      )
    }
    return withAnthropicDocumentTitle({
      type: 'document',
      source: {
        type: 'text',
        media_type: 'text/plain',
        data: text
      }
    }, title)
  }

  throw bridgeValidationError(
    `OpenAI 到 Anthropic 桥接当前只支持 PDF 或 text/plain 文件，不能桥接 ${mediaType ?? 'unknown'} 文件`,
    'openai_anthropic_bridge_unsupported_file_media_type'
  )
}

function anthropicDocumentBlockFromOpenAIFileData(
  fileData: string,
  input: { filename?: string; title?: string; mediaType?: string }
): AnthropicContentBlock {
  const parsedDataUrl = parseDataUrl(fileData)
  const mediaType = normalizeOpenAIFileMediaType(
    parsedDataUrl?.mediaType ?? input.mediaType,
    input.filename
  )
  const base64Data = normalizedBase64Data(parsedDataUrl?.data ?? fileData)
  if (!base64Data) {
    throw bridgeValidationError(
      'OpenAI 文件输入 file_data 不是合法 base64 或 data URL',
      'openai_anthropic_bridge_invalid_file_input'
    )
  }

  if (mediaType === 'application/pdf') {
    return withAnthropicDocumentTitle({
      type: 'document',
      source: {
        type: 'base64',
        media_type: 'application/pdf',
        data: base64Data
      }
    }, input.title)
  }

  if (isTextFileMediaType(mediaType)) {
    return withAnthropicDocumentTitle({
      type: 'document',
      source: {
        type: 'text',
        media_type: 'text/plain',
        data: Buffer.from(base64Data, 'base64').toString('utf8')
      }
    }, input.title)
  }

  throw bridgeValidationError(
    `OpenAI 到 Anthropic 桥接当前只支持 PDF 或 text/plain inline 文件，不能桥接 ${mediaType ?? 'unknown'} 文件`,
    'openai_anthropic_bridge_unsupported_file_media_type'
  )
}

function anthropicDocumentBlockFromOpenAIFileUrl(
  fileUrl: string,
  input: { filename?: string; title?: string }
): AnthropicContentBlock {
  if (!/^https?:\/\//i.test(fileUrl)) {
    throw bridgeValidationError(
      'Responses input_file.file_url 必须是 http(s) URL',
      'openai_anthropic_bridge_invalid_file_input'
    )
  }
  if (!isPdfFilename(input.filename) && !isPdfUrl(fileUrl)) {
    throw bridgeValidationError(
      'OpenAI 到 Anthropic 桥接当前只支持 PDF file_url；其他文件 URL 需要本地文件解析器',
      'openai_anthropic_bridge_unsupported_file_media_type'
    )
  }
  return withAnthropicDocumentTitle({
    type: 'document',
    source: {
      type: 'url',
      url: fileUrl
    }
  }, input.title)
}

function withAnthropicDocumentTitle(block: AnthropicContentBlock, title: string | undefined): AnthropicContentBlock {
  if (title) block.title = title
  return block
}

function chatToolCallsToAnthropicToolUseBlocks(value: unknown): AnthropicContentBlock[] {
  if (!Array.isArray(value)) return []
  const blocks: AnthropicContentBlock[] = []
  for (const item of value) {
    if (!isPlainObject(item)) continue
    const fn = objectValue(item.function)
    const name = stringValue(fn?.name)
    const id = stringValue(item.id)
    if (!name || !id) continue
    blocks.push({
      type: 'tool_use',
      id,
      name,
      input: parseJsonObjectString(fn?.arguments) ?? {}
    })
  }
  return blocks
}

function chatToolsToAnthropicTools(
  value: unknown,
  requestPlan?: OpenAIToAnthropicBridgeRequestPlan
): JsonRecord[] {
  if (!Array.isArray(value)) return []
  const tools: JsonRecord[] = []
  for (const item of value) {
    if (!isPlainObject(item) || item.type !== 'function') {
      if (isOpenAIFileSearchTool(item) && requestPlan?.fileSearch) {
        continue
      }
      throw unsupportedOpenAIToolError(item, 'Chat')
    }
    const fn = objectValue(item.function)
    const name = stringValue(fn?.name)
    if (!name) {
      throw bridgeValidationError('function tool 缺少 name', 'openai_anthropic_bridge_invalid_tool')
    }
    tools.push(anthropicToolFromFunctionDefinition(name, fn?.description, fn?.parameters))
  }
  return tools
}

function responsesToolsToAnthropicTools(
  value: unknown,
  requestPlan?: OpenAIToAnthropicBridgeRequestPlan
): JsonRecord[] {
  if (!Array.isArray(value)) return []
  const tools: JsonRecord[] = []
  for (const item of value) {
    if (!isPlainObject(item) || item.type !== 'function') {
      if (isOpenAIFileSearchTool(item) && requestPlan?.fileSearch) {
        continue
      }
      throw unsupportedOpenAIToolError(item, 'Responses')
    }
    const name = stringValue(item.name)
    if (!name) {
      throw bridgeValidationError('Responses function tool 缺少 name', 'openai_anthropic_bridge_invalid_tool')
    }
    tools.push(anthropicToolFromFunctionDefinition(name, item.description, item.parameters))
  }
  return tools
}

function anthropicToolFromFunctionDefinition(name: string, description: unknown, parameters: unknown): JsonRecord {
  return {
    name,
    description: stringValue(description) ?? '',
    input_schema: isPlainObject(parameters) ? parameters : { type: 'object', properties: {} }
  }
}

function applyTools(output: JsonRecord, tools: JsonRecord[], toolChoice: unknown): void {
  if (!tools.length) return
  output.tools = tools
  const anthropicToolChoice = anthropicToolChoiceFromOpenAI(toolChoice)
  if (anthropicToolChoice) output.tool_choice = anthropicToolChoice
}

function applyStructuredOutputPlan(
  output: JsonRecord,
  tools: JsonRecord[],
  toolChoice: unknown,
  structuredOutput: OpenAIToAnthropicStructuredOutputPlan | undefined
): void {
  if (!structuredOutput?.syntheticToolName) {
    applyTools(output, tools, toolChoice)
    return
  }
  if (tools.length) {
    throw bridgeValidationError(
      'OpenAI 到 Anthropic 桥接当前不支持在 strict JSON schema 输出中同时使用用户工具',
      'openai_anthropic_bridge_structured_output_with_tools_unsupported'
    )
  }
  const schema = structuredOutput.schema
  if (!schema) {
    throw bridgeValidationError(
      'OpenAI 到 Anthropic 桥接 strict JSON schema 缺少 schema',
      'openai_anthropic_bridge_invalid_json_schema'
    )
  }
  output.tools = [{
    name: structuredOutput.syntheticToolName,
    description: 'Emit the final assistant answer as JSON that matches the requested schema.',
    input_schema: schema
  }]
  output.tool_choice = { type: 'tool', name: structuredOutput.syntheticToolName }
}

function anthropicToolChoiceFromOpenAI(value: unknown): JsonRecord | undefined {
  if (value === undefined || value === null) return undefined
  if (value === 'auto') return { type: 'auto' }
  if (value === 'none') return { type: 'none' }
  if (value === 'required') return { type: 'any' }
  if (typeof value === 'string') return undefined
  if (!isPlainObject(value)) return undefined
  if (value.type === 'function') {
    const fn = objectValue(value.function)
    const name = stringValue(fn?.name) ?? stringValue(value.name)
    return name ? { type: 'tool', name } : undefined
  }
  if (value.type === 'auto') return { type: 'auto' }
  if (value.type === 'none') return { type: 'none' }
  if (value.type === 'required') return { type: 'any' }
  return undefined
}

function jsonInstructionFromStructuredOutputPlan(
  structuredOutput: OpenAIToAnthropicStructuredOutputPlan | undefined
): string | undefined {
  if (structuredOutput?.type === 'json_object') {
    return '请只输出一个合法 JSON 对象，不要输出 Markdown 或额外解释。'
  }
  if (structuredOutput?.type === 'json_schema') {
    return `请只输出符合以下 JSON Schema 要求的合法 JSON，不要输出 Markdown 或额外解释：\n${JSON.stringify(structuredOutput.schema ?? {})}`
  }
  return undefined
}

function appendJsonOutputInstruction(output: JsonRecord, instruction: string | undefined): void {
  if (!instruction) return
  output.system = [stringValue(output.system), instruction].filter(Boolean).join('\n\n')
}

function createOpenAIToAnthropicBridgeRequestPlan(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  body: JsonRecord
): OpenAIToAnthropicBridgeRequestPlan {
  return {
    structuredOutput: sourceEndpointFamily === OPENAI_RESPONSES_FAMILY
      ? structuredOutputPlanFromResponsesBody(body)
      : structuredOutputPlanFromChatBody(body),
    reasoningEffort: reasoningEffortFromOpenAIBody(body)
  }
}

async function applyOpenAIToAnthropicLocalToolEmulation(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  body: JsonRecord,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  options: OpenAIToAnthropicBridgeBodyOptions,
  signal?: AbortSignal
): Promise<void> {
  if (sourceEndpointFamily !== OPENAI_RESPONSES_FAMILY && sourceEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY) return
  await applyOpenAIToAnthropicFileSearchEmulation(sourceEndpointFamily, body, requestPlan, options.fileSearchExecutor, signal)
}

async function applyOpenAIToAnthropicFileSearchEmulation(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  body: JsonRecord,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  executor: OpenAIToAnthropicFileSearchExecutor | undefined,
  signal?: AbortSignal
): Promise<void> {
  const tool = sourceEndpointFamily === OPENAI_RESPONSES_FAMILY
    ? responsesFileSearchToolFromBody(body)
    : chatFileSearchToolFromBody(body)
  if (!tool) return
  if (!executor) return
  const query = sourceEndpointFamily === OPENAI_RESPONSES_FAMILY
    ? responsesUserQueryFromBody(body)
    : chatUserQueryFromBody(body)
  if (!query) {
    throw bridgeValidationError(
      `${sourceEndpointFamily === OPENAI_RESPONSES_FAMILY ? 'Responses' : 'Chat'} file_search 桥接无法从请求中提取检索查询`,
      'openai_anthropic_bridge_file_search_missing_query'
    )
  }
  if (!tool.vectorStoreIds.length) {
    throw bridgeValidationError(
      'file_search tool 缺少 vector_store_ids',
      'openai_anthropic_bridge_file_search_missing_vector_store'
    )
  }
  try {
    const result = await executor.search({
      vectorStoreIds: tool.vectorStoreIds,
      query,
      maxNumResults: tool.maxNumResults,
      filters: tool.filters,
      rankingOptions: tool.rankingOptions,
      signal
    })
    requestPlan.fileSearch = {
      query,
      queries: result.queries?.length ? result.queries : [query],
      tool,
      results: result.results,
      includeResults: responseIncludesFileSearchResults(body)
    }
  } catch (error) {
    if (error instanceof GatewayRequestValidationError) throw error
    throw bridgeValidationError(
      error instanceof Error ? error.message : `${sourceEndpointFamily === OPENAI_RESPONSES_FAMILY ? 'Responses' : 'Chat'} file_search 本地执行失败`,
      'openai_anthropic_bridge_file_search_execution_failed',
      502,
      'upstream_error'
    )
  }
}

function setOpenAIToAnthropicBridgeRequestPlan(req: Request, plan: OpenAIToAnthropicBridgeRequestPlan): void {
  ;(req as OpenAIToAnthropicBridgePlannedRequest)[bridgeRequestPlanSymbol] = plan
}

function openAIToAnthropicBridgeRequestPlan(req: Request): OpenAIToAnthropicBridgeRequestPlan | undefined {
  return (req as OpenAIToAnthropicBridgePlannedRequest)[bridgeRequestPlanSymbol]
}

function structuredOutputPlanFromChatBody(body: JsonRecord): OpenAIToAnthropicStructuredOutputPlan | undefined {
  const responseFormat = objectValue(body.response_format)
  const type = stringValue(responseFormat?.type)
  if (type === 'json_object') {
    return { type: 'json_object' }
  }
  if (type !== 'json_schema') return undefined
  const jsonSchema = objectValue(responseFormat?.json_schema)
  const schema = objectValue(jsonSchema?.schema) ?? objectValue(jsonSchema)
  return {
    type: 'json_schema',
    name: stringValue(jsonSchema?.name),
    schema,
    strict: jsonSchema?.strict === true,
    syntheticToolName: structuredOutputSyntheticToolName
  }
}

function structuredOutputPlanFromResponsesBody(body: JsonRecord): OpenAIToAnthropicStructuredOutputPlan | undefined {
  const text = objectValue(body.text)
  const format = objectValue(text?.format)
  const type = stringValue(format?.type)
  if (type === 'json_object') {
    return { type: 'json_object' }
  }
  if (type !== 'json_schema') return undefined
  return {
    type: 'json_schema',
    name: stringValue(format?.name),
    schema: objectValue(format?.schema),
    strict: format?.strict === true,
    syntheticToolName: structuredOutputSyntheticToolName
  }
}

function reasoningEffortFromOpenAIBody(body: JsonRecord): string | undefined {
  const reasoning = objectValue(body.reasoning)
  return stringValue(reasoning?.effort) ?? stringValue(body.reasoning_effort)
}

function applyAnthropicThinkingFromOpenAIReasoning(output: JsonRecord, body: JsonRecord): void {
  const effort = reasoningEffortFromOpenAIBody(body)
  if (!effort) return
  const budgetTokens = anthropicThinkingBudgetTokens(effort)
  if (!budgetTokens) return
  const maxTokens = integerValue(output.max_tokens) ?? defaultAnthropicMaxTokens
  if (maxTokens <= budgetTokens) {
    output.max_tokens = budgetTokens + 1024
  }
  output.thinking = {
    type: 'enabled',
    budget_tokens: budgetTokens
  }
}

function anthropicThinkingBudgetTokens(effort: string): number | undefined {
  const normalized = effort.toLowerCase()
  if (normalized === 'minimal') return 1024
  if (normalized === 'low') return 2048
  if (normalized === 'medium') return 4096
  if (normalized === 'high') return 8192
  return undefined
}

function responsesFileSearchToolFromBody(body: JsonRecord): OpenAIToAnthropicFileSearchToolConfig | undefined {
  const tools = Array.isArray(body.tools) ? body.tools : []
  const tool = tools.find(isOpenAIFileSearchTool)
  return tool ? fileSearchToolConfig(tool) : undefined
}

function chatFileSearchToolFromBody(body: JsonRecord): OpenAIToAnthropicFileSearchToolConfig | undefined {
  const tools = Array.isArray(body.tools) ? body.tools : []
  const tool = tools.find(isOpenAIFileSearchTool)
  return tool ? fileSearchToolConfig(tool) : undefined
}

function isOpenAIFileSearchTool(value: unknown): value is JsonRecord {
  return isPlainObject(value) && stringValue(value.type) === 'file_search'
}

function fileSearchToolConfig(value: JsonRecord): OpenAIToAnthropicFileSearchToolConfig {
  return {
    vectorStoreIds: stringArrayValue(value.vector_store_ids),
    maxNumResults: integerValue(value.max_num_results),
    filters: objectValue(value.filters) ?? objectValue(value.attribute_filter),
    rankingOptions: objectValue(value.ranking_options)
  }
}

function responseIncludesFileSearchResults(body: JsonRecord): boolean {
  return Array.isArray(body.include) && body.include.includes('file_search_call.results')
}

function responsesUserQueryFromBody(body: JsonRecord): string | undefined {
  const input = body.input
  if (typeof input === 'string') return normalizeWhitespace(input)
  const items = Array.isArray(input)
    ? input
    : isPlainObject(input) ? [input] : []
  for (let index = items.length - 1; index >= 0; index -= 1) {
    const item = items[index]
    if (!isPlainObject(item)) continue
    if (item.type === 'message') {
      const role = stringValue(item.role)
      if (role && role !== 'user') continue
      const text = normalizeWhitespace(responsesContentToText(item.content))
      if (text) return text
      continue
    }
    const text = normalizeWhitespace(responsesTextFromValue(item))
    if (text) return text
  }
  return undefined
}

function chatUserQueryFromBody(body: JsonRecord): string | undefined {
  const messages = Array.isArray(body.messages) ? body.messages : []
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const item = messages[index]
    if (!isPlainObject(item)) continue
    if (stringValue(item.role) !== 'user') continue
    const text = normalizeWhitespace(openAIContentToText(item.content))
    if (text) return text
  }
  return undefined
}

function appendFileSearchContext(
  systemParts: string[],
  fileSearch: OpenAIToAnthropicFileSearchPlan | undefined
): void {
  if (!fileSearch) return
  appendSystemText(systemParts, fileSearchContextText(fileSearch))
}

function fileSearchContextText(fileSearch: OpenAIToAnthropicFileSearchPlan): string {
  const lines = [
    'File search results are provided by the gateway for this request.',
    `Search query: ${fileSearch.query}`,
    'Use bracket markers such as [F1] in the final answer when citing these files.'
  ]
  if (fileSearch.results.length === 0) {
    lines.push('No file search results were returned.')
    return lines.join('\n')
  }
  for (const [index, result] of fileSearch.results.entries()) {
    lines.push(
      '',
      `[F${index + 1}] ${result.filename}`,
      `File ID: ${result.fileId}`,
      `Score: ${result.score.toFixed(4)}`,
      'Content:',
      result.contentText
    )
  }
  return lines.join('\n')
}

function unsupportedOpenAIToolError(tool: unknown, source: 'Chat' | 'Responses'): GatewayRequestValidationError {
  const type = isPlainObject(tool) ? stringValue(tool.type) ?? 'unknown' : 'unknown'
  const detail = openAIHostedToolCompatibilityDetail(type)
  return bridgeValidationError(
    `${source} tool type "${type}" 当前不能直接桥接到 Anthropic Messages：${detail}`,
    'openai_anthropic_bridge_unsupported_hosted_tool'
  )
}

function openAIHostedToolCompatibilityDetail(type: string): string {
  if (type === 'web_search' || type === 'web_search_preview' || type === 'web_search_preview_2025_03_11') {
    return '需要 Anthropic 上游原生 web search server tool；当前桥接不代执行搜索'
  }
  if (type === 'file_search') {
    return '需要网关本地 Files / Vector Store 检索运行时'
  }
  if (type === 'image_generation') {
    return 'Anthropic Messages 不生成图片，需要本地图像生成 provider'
  }
  if (type === 'code_interpreter' || type === 'container') {
    return '需要 Anthropic code execution 能力或网关本地安全沙箱'
  }
  if (type === 'computer') {
    return '需要 Anthropic computer use 或网关本地 computer adapter'
  }
  if (type === 'mcp') {
    return '需要 MCP server allowlist、认证、审批和审计映射'
  }
  if (type === 'tool_search' || type === 'shell' || type === 'skills') {
    return '需要 Codex 本地工具运行时，不能由 Anthropic Messages 字段转换凭空执行'
  }
  return '需要先在高兼容能力矩阵中定义映射、模拟或受控拒绝策略'
}

async function * transformAnthropicMessagesJsonToChatJson(
  body: AsyncIterable<Uint8Array>,
  options: { model: string; requestPlan?: OpenAIToAnthropicBridgeRequestPlan }
): AsyncIterable<Uint8Array> {
  const parsed = await readJsonBody(body)
  const message = isPlainObject(parsed) ? parsed : {}
  const payload = anthropicMessageToChatCompletion(message, options.model, options.requestPlan)
  yield Buffer.from(JSON.stringify(payload), 'utf8')
}

async function * transformAnthropicMessagesJsonToResponsesJson(
  body: AsyncIterable<Uint8Array>,
  options: {
    model: string
    requestPlan?: OpenAIToAnthropicBridgeRequestPlan
    previousResponseId?: string
    onResponsesCompleted?: CodexResponsesChatBridgeCompletionHandler
  }
): AsyncIterable<Uint8Array> {
  const parsed = await readJsonBody(body)
  const message = isPlainObject(parsed) ? parsed : {}
  const payload = anthropicMessageToResponsesResponse(message, {
    model: options.model,
    requestPlan: options.requestPlan,
    previousResponseId: options.previousResponseId
  })
  if (options.onResponsesCompleted && payload.status === 'completed') {
    await options.onResponsesCompleted({
      responseId: stringValue(payload.id) ?? '',
      createdAt: integerValue(payload.created_at) ?? Math.floor(Date.now() / 1000),
      model: stringValue(payload.model) ?? options.model,
      outputItems: Array.isArray(payload.output) ? payload.output.filter(isPlainObject).map((item) => ({ ...item })) : [],
      response: { ...payload }
    })
  }
  yield Buffer.from(JSON.stringify(payload), 'utf8')
}

async function * transformAnthropicErrorBodyToOpenAIErrorBody(body: AsyncIterable<Uint8Array>): AsyncIterable<Uint8Array> {
  let parsed: unknown
  try {
    parsed = await readJsonBody(body)
  } catch {
    parsed = undefined
  }
  yield Buffer.from(JSON.stringify(openAIErrorFromAnthropicPayload(parsed)), 'utf8')
}

async function * transformAnthropicMessagesSseToChatSse(
  body: AsyncIterable<Uint8Array>,
  options: { model: string; requestPlan?: OpenAIToAnthropicBridgeRequestPlan }
): AsyncIterable<Uint8Array> {
  const state = createAnthropicStreamState(OPENAI_CHAT_COMPLETIONS_FAMILY, options.model, undefined, options.requestPlan)
  for await (const event of iterateAnthropicSseEvents(body)) {
    for (const output of processAnthropicEventAsChat(state, event)) {
      yield Buffer.from(output, 'utf8')
    }
  }
  if (!state.completed && !state.failed) {
    for (const output of failChatStream(
      state,
      '上游 Anthropic Messages SSE 在正常结束事件前中断',
      'upstream_stream_interrupted'
    )) {
      yield Buffer.from(output, 'utf8')
    }
  }
}

async function * transformAnthropicMessagesSseToResponsesSse(
  body: AsyncIterable<Uint8Array>,
  options: {
    model: string
    requestPlan?: OpenAIToAnthropicBridgeRequestPlan
    previousResponseId?: string
    onResponsesCompleted?: CodexResponsesChatBridgeCompletionHandler
  }
): AsyncIterable<Uint8Array> {
  const state = createAnthropicStreamState(OPENAI_RESPONSES_FAMILY, options.model, options.previousResponseId, options.requestPlan)
  for await (const event of iterateAnthropicSseEvents(body)) {
    for (const output of processAnthropicEventAsResponses(state, event)) {
      yield Buffer.from(output, 'utf8')
    }
    await notifyResponsesCompletion(state, options.onResponsesCompleted)
  }
  if (!state.completed && !state.failed) {
    for (const output of failResponsesStream(state, {
      message: '上游 Anthropic Messages SSE 在正常结束事件前中断',
      type: 'upstream_error',
      code: 'upstream_stream_interrupted'
    })) {
      yield Buffer.from(output, 'utf8')
    }
    await notifyResponsesCompletion(state, options.onResponsesCompleted)
  }
}

function anthropicMessageToChatCompletion(
  message: JsonRecord,
  fallbackModel: string,
  requestPlan?: OpenAIToAnthropicBridgeRequestPlan
): JsonRecord {
  const model = stringValue(message.model) ?? fallbackModel
  const contentBlocks = Array.isArray(message.content) ? message.content : []
  const structuredOutput = structuredOutputResultFromAnthropicBlocks(contentBlocks, requestPlan)
  if (structuredOutput?.error) {
    return chatCompletionFromStructuredOutputError({
      id: chatCompletionIdFromAnthropicId(stringValue(message.id)),
      model,
      error: structuredOutput.error,
      usage: objectValue(message.usage)
    })
  }
  const structuredOutputText = structuredOutput?.text
  const text = structuredOutputText ?? contentBlocks.map(anthropicTextFromBlock).filter(Boolean).join('')
  const toolCalls = structuredOutputText === undefined ? anthropicToolUseBlocks(contentBlocks).map((block, index) => ({
    id: stringValue(block.id) ?? `call_${index}`,
    type: 'function',
    function: {
      name: stringValue(block.name) ?? '',
      arguments: JSON.stringify(isPlainObject(block.input) ? block.input : {})
    }
  })) : []
  const assistantMessage: JsonRecord = {
    role: 'assistant',
    content: text || (toolCalls.length ? null : '')
  }
  const annotations = localToolAnnotationsForText(text, requestPlan)
  if (annotations.length) {
    assistantMessage.annotations = annotations
  }
  if (toolCalls.length) assistantMessage.tool_calls = toolCalls
  return {
    id: chatCompletionIdFromAnthropicId(stringValue(message.id)),
    object: 'chat.completion',
    created: Math.floor(Date.now() / 1000),
    model,
    choices: [{
      index: 0,
      message: assistantMessage,
      finish_reason: structuredOutputText === undefined
        ? openAIChatFinishReasonFromAnthropic(stringValue(message.stop_reason))
        : 'stop'
    }],
    usage: anthropicUsageToChatUsage(objectValue(message.usage))
  }
}

function anthropicMessageToResponsesResponse(
  message: JsonRecord,
  options: { model: string; requestPlan?: OpenAIToAnthropicBridgeRequestPlan; previousResponseId?: string }
): JsonRecord {
  const model = stringValue(message.model) ?? options.model
  const responseId = responseIdFromAnthropicId(stringValue(message.id))
  const createdAt = Math.floor(Date.now() / 1000)
  const contentBlocks = Array.isArray(message.content) ? message.content : []
  const structuredOutput = structuredOutputResultFromAnthropicBlocks(contentBlocks, options.requestPlan)
  if (structuredOutput?.error) {
    return failedResponsesResponseFromStructuredOutputError({
      responseId,
      createdAt,
      model,
      previousResponseId: options.previousResponseId,
      requestPlan: options.requestPlan,
      usage: objectValue(message.usage),
      error: structuredOutput.error
    })
  }
  const output = anthropicContentBlocksToResponsesOutputItems(
    contentBlocks,
    responseId,
    options.requestPlan,
    structuredOutput?.text
  )
  prependResponsesLocalToolOutputItems(output, options.requestPlan, responseId)
  const outputText = output
    .flatMap((item) => Array.isArray(item.content) ? item.content : [])
    .filter(isPlainObject)
    .map((part) => stringValue(part.text) ?? '')
    .join('')
  return {
    id: responseId,
    object: 'response',
    created_at: createdAt,
    status: 'completed',
    completed_at: createdAt,
    error: null,
    incomplete_details: null,
    instructions: null,
    max_output_tokens: null,
    model,
    output,
    output_text: outputText,
    parallel_tool_calls: false,
    previous_response_id: options.previousResponseId ?? null,
    reasoning: {
      effort: options.requestPlan?.reasoningEffort ?? null,
      summary: null
    },
    store: false,
    temperature: null,
    text: { format: { type: 'text' } },
    tool_choice: 'auto',
    tools: [],
    top_p: null,
    truncation: 'disabled',
    usage: anthropicUsageToResponsesUsage(objectValue(message.usage)),
    user: null,
    metadata: {}
  }
}

function anthropicContentBlocksToResponsesOutputItems(
  blocks: unknown[],
  responseId: string,
  requestPlan?: OpenAIToAnthropicBridgeRequestPlan,
  structuredOutputText?: string
): JsonRecord[] {
  const output: JsonRecord[] = []
  let messageText = structuredOutputText ?? structuredOutputResultFromAnthropicBlocks(blocks, requestPlan)?.text ?? ''
  let textIndex = 0
  if (!messageText) {
    for (const block of blocks) {
      if (!isPlainObject(block)) continue
      if (block.type === 'text') {
        messageText += stringValue(block.text) ?? ''
        continue
      }
      if (block.type === 'thinking') {
        const thinkingText = anthropicThinkingTextFromBlock(block)
        if (thinkingText) {
          output.push(responsesReasoningItem(responseId, output.length, thinkingText))
        }
        continue
      }
      if (block.type === 'tool_use') {
        output.push({
          id: `fc_${safeIdSegment(stringValue(block.id) ?? `${responseId}_${output.length}`)}`,
          type: 'function_call',
          status: 'completed',
          call_id: stringValue(block.id) ?? `call_${output.length}`,
          name: stringValue(block.name) ?? '',
          arguments: JSON.stringify(isPlainObject(block.input) ? block.input : {})
        })
      }
    }
  }
  if (messageText || output.length === 0) {
    output.unshift({
      id: `msg_${safeIdSegment(responseId)}_${textIndex++}`,
      type: 'message',
      status: 'completed',
      role: 'assistant',
      content: [{
        type: 'output_text',
        text: messageText,
        annotations: localToolAnnotationsForText(messageText, requestPlan)
      }]
    })
  }
  return output
}

function processAnthropicEventAsChat(state: AnthropicStreamState, event: ParsedSseEvent): string[] {
  if (state.completed || state.failed) return []
  if (event.eventName === 'error' || event.data?.type === 'error') {
    state.failed = true
    return [chatSseData({ error: openAIErrorFromAnthropicPayload(event.data).error }), chatSseDone()]
  }
  if (event.dataParseError) {
    return failChatStream(state, '上游 Anthropic Messages SSE 返回了无法解析的事件', 'upstream_stream_parse_error')
  }
  const data = event.data
  if (!data) return []
  const output: string[] = []
  if (data.type === 'message_start') {
    const message = objectValue(data.message) ?? {}
    state.id = stringValue(message.id) ?? state.id
    state.chatId = chatCompletionIdFromAnthropicId(state.id)
    state.model = stringValue(message.model) ?? state.model
    state.usage = anthropicUsageToChatUsage(objectValue(message.usage))
    output.push(...ensureChatRoleChunk(state))
    return output
  }
  if (data.type === 'content_block_start') {
    const index = integerValue(data.index) ?? 0
    const contentBlock = objectValue(data.content_block) ?? {}
    const block = streamBlockFromContentBlock(index, contentBlock)
    state.blocks.set(index, block)
    output.push(...ensureChatRoleChunk(state))
    if (block.type === 'tool_use' && !isStructuredOutputStreamBlock(state, block)) {
      output.push(chatSseChunk(state, {
        tool_calls: [{
          index,
          id: block.id ?? `call_${index}`,
          type: 'function',
          function: {
            name: block.name ?? '',
            arguments: ''
          }
        }]
      }))
    }
    return output
  }
  if (data.type === 'content_block_delta') {
    const index = integerValue(data.index) ?? 0
    const block = state.blocks.get(index)
    const delta = objectValue(data.delta) ?? {}
    output.push(...ensureChatRoleChunk(state))
    if (delta.type === 'text_delta') {
      const text = stringValue(delta.text) ?? ''
      if (block) block.text += text
      if (text) output.push(chatSseChunk(state, { content: text }))
    } else if (delta.type === 'input_json_delta') {
      const partial = stringValue(delta.partial_json) ?? ''
      if (block) block.inputJson += partial
      if (partial && block && !isStructuredOutputStreamBlock(state, block)) {
        output.push(chatSseChunk(state, {
          tool_calls: [{
            index,
            function: { arguments: partial }
          }]
        }))
      }
    } else if (delta.type === 'thinking_delta') {
      const text = stringValue(delta.thinking) ?? stringValue(delta.text) ?? ''
      if (block) block.text += text
    }
    return output
  }
  if (data.type === 'content_block_stop') {
    const index = integerValue(data.index) ?? 0
    const block = state.blocks.get(index)
    if (block && isStructuredOutputStreamBlock(state, block)) {
      const result = structuredOutputResultFromInputJson(block.inputJson, state.requestPlan)
      if (result.error) {
        return failChatStream(
          state,
          stringValue(result.error.message) ?? 'Anthropic structured output 不符合 JSON Schema',
          structuredOutputSchemaMismatchCode,
          'invalid_response_error'
        )
      }
      if (result.text) output.push(chatSseChunk(state, { content: result.text }))
    }
    return output
  }
  if (data.type === 'message_delta') {
    const delta = objectValue(data.delta) ?? {}
    state.stopReason = stringValue(delta.stop_reason) ?? state.stopReason
    const usage = objectValue(data.usage)
    if (usage) state.usage = anthropicUsageToChatUsage(usage, state.usage)
    return output
  }
  if (data.type === 'message_stop') {
    state.terminalReceived = true
    output.push(...completeChatStream(state, openAIChatFinishReasonFromAnthropic(state.stopReason)))
  }
  return output
}

function processAnthropicEventAsResponses(state: AnthropicStreamState, event: ParsedSseEvent): string[] {
  if (state.completed || state.failed) return []
  if (event.eventName === 'error' || event.data?.type === 'error') {
    const payload = openAIErrorFromAnthropicPayload(event.data)
    return failResponsesStream(state, objectValue(payload.error) ?? {
      message: '上游 Anthropic Messages 流式响应失败',
      type: 'upstream_error',
      code: 'upstream_error'
    })
  }
  if (event.dataParseError) {
    return failResponsesStream(state, {
      message: '上游 Anthropic Messages SSE 返回了无法解析的事件',
      type: 'upstream_error',
      code: 'upstream_stream_parse_error'
    })
  }
  const data = event.data
  if (!data) return []
  const output: string[] = []
  if (data.type === 'message_start') {
    const message = objectValue(data.message) ?? {}
    state.id = stringValue(message.id) ?? state.id
    state.responseId = responseIdFromAnthropicId(state.id)
    state.responseMessageId = `msg_${safeIdSegment(state.responseId)}`
    state.model = stringValue(message.model) ?? state.model
    state.usage = anthropicUsageToResponsesUsage(objectValue(message.usage))
    output.push(...ensureResponsesStreamStarted(state))
    return output
  }
  if (data.type === 'content_block_start') {
    const index = integerValue(data.index) ?? 0
    const contentBlock = objectValue(data.content_block) ?? {}
    const block = streamBlockFromContentBlock(index, contentBlock)
    block.outputIndex = state.nextOutputIndex++
    state.blocks.set(index, block)
    output.push(...ensureResponsesStreamStarted(state))
    output.push(...ensureResponsesLocalToolPreface(state))
    if (block.type === 'text' || isStructuredOutputStreamBlock(state, block)) {
      output.push(sse('response.output_item.added', {
        type: 'response.output_item.added',
        output_index: block.outputIndex,
        item: {
          id: state.responseMessageId,
          type: 'message',
          status: 'in_progress',
          role: 'assistant',
          content: []
        }
      }))
      output.push(sse('response.content_part.added', {
        type: 'response.content_part.added',
        item_id: state.responseMessageId,
        output_index: block.outputIndex,
        content_index: 0,
        part: { type: 'output_text', text: '', annotations: [] }
      }))
    } else if (block.type === 'thinking') {
      output.push(sse('response.output_item.added', {
        type: 'response.output_item.added',
        output_index: block.outputIndex,
        item: {
          id: responsesReasoningItemId(state.responseId, block.index),
          type: 'reasoning',
          status: 'in_progress',
          summary: []
        }
      }))
    } else if (block.type === 'tool_use') {
      output.push(sse('response.output_item.added', {
        type: 'response.output_item.added',
        output_index: block.outputIndex,
        item: {
          id: `fc_${safeIdSegment(block.id ?? `${index}`)}`,
          type: 'function_call',
          status: 'in_progress',
          call_id: block.id ?? `call_${index}`,
          name: block.name ?? '',
          arguments: ''
        }
      }))
    }
    return output
  }
  if (data.type === 'content_block_delta') {
    const index = integerValue(data.index) ?? 0
    const block = state.blocks.get(index)
    const delta = objectValue(data.delta) ?? {}
    output.push(...ensureResponsesStreamStarted(state))
    if (delta.type === 'text_delta') {
      const text = stringValue(delta.text) ?? ''
      if (block) block.text += text
      if (block && text) {
        output.push(sse('response.output_text.delta', {
          type: 'response.output_text.delta',
          item_id: state.responseMessageId,
          output_index: block.outputIndex ?? 0,
          content_index: 0,
          delta: text
        }))
      }
    } else if (delta.type === 'input_json_delta') {
      const partial = stringValue(delta.partial_json) ?? ''
      if (block) block.inputJson += partial
    } else if (delta.type === 'thinking_delta') {
      const text = stringValue(delta.thinking) ?? stringValue(delta.text) ?? ''
      if (block) block.text += text
    }
    return output
  }
  if (data.type === 'content_block_stop') {
    const index = integerValue(data.index) ?? 0
    const block = state.blocks.get(index)
    if (block) output.push(...completeResponsesBlock(state, block))
    return output
  }
  if (data.type === 'message_delta') {
    const delta = objectValue(data.delta) ?? {}
    state.stopReason = stringValue(delta.stop_reason) ?? state.stopReason
    const usage = objectValue(data.usage)
    if (usage) state.usage = anthropicUsageToResponsesUsage(usage, state.usage)
    return output
  }
  if (data.type === 'message_stop') {
    state.terminalReceived = true
    output.push(...completeResponsesStream(state))
  }
  return output
}

function ensureChatRoleChunk(state: AnthropicStreamState): string[] {
  if (state.roleSent) return []
  state.roleSent = true
  return [chatSseChunk(state, { role: 'assistant' })]
}

function completeChatStream(state: AnthropicStreamState, finishReason: string | undefined): string[] {
  if (state.completed || state.failed) return []
  state.completed = true
  const output = ensureChatRoleChunk(state)
  output.push(chatSseData({
    id: state.chatId,
    object: 'chat.completion.chunk',
    created: state.createdAt,
    model: state.model,
    choices: [{
      index: 0,
      delta: {},
      finish_reason: finishReason ?? 'stop'
    }],
    usage: state.usage ?? estimatedChatUsageFromStreamState(state)
  }))
  output.push(chatSseDone())
  return output
}

function failChatStream(state: AnthropicStreamState, message: string, code: string, type = 'upstream_error'): string[] {
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

function ensureResponsesStreamStarted(state: AnthropicStreamState): string[] {
  if (state.responsesStarted) return []
  state.responsesStarted = true
  return [
    sse('response.created', {
      type: 'response.created',
      response: responseSnapshot(state, 'in_progress')
    }),
    sse('response.in_progress', {
      type: 'response.in_progress',
      response: responseSnapshot(state, 'in_progress')
    })
  ]
}

function ensureResponsesLocalToolPreface(state: AnthropicStreamState): string[] {
  const items = responsesLocalToolCallItems(state.requestPlan, state.responseId)
  if (!items.length) return []
  const output: string[] = []
  for (const item of items) {
    const outputIndex = state.nextOutputIndex++
    state.outputItems.push(item)
    output.push(
      sse('response.output_item.added', {
        type: 'response.output_item.added',
        output_index: outputIndex,
        item: {
          ...item,
          status: 'in_progress'
        }
      }),
      sse('response.output_item.done', {
        type: 'response.output_item.done',
        output_index: outputIndex,
        item
      })
    )
  }
  return output
}

function completeResponsesBlock(state: AnthropicStreamState, block: AnthropicStreamBlockState): string[] {
  if (block.done) return []
  block.done = true
  if (isStructuredOutputStreamBlock(state, block)) {
    const result = structuredOutputResultFromInputJson(block.inputJson, state.requestPlan)
    if (result.error) {
      return failResponsesStream(state, result.error)
    }
    return completeResponsesTextBlock(state, block, result.text)
  }
  if (block.type === 'thinking') {
    const item = responsesReasoningItem(state.responseId, block.index, block.text)
    state.outputItems.push(item)
    return [sse('response.output_item.done', {
      type: 'response.output_item.done',
      output_index: block.outputIndex ?? 0,
      item
    })]
  }
  if (block.type === 'tool_use') {
    const item = {
      id: `fc_${safeIdSegment(block.id ?? `${block.index}`)}`,
      type: 'function_call',
      status: 'completed',
      call_id: block.id ?? `call_${block.index}`,
      name: block.name ?? '',
      arguments: block.inputJson || '{}'
    }
    state.outputItems.push(item)
    return [sse('response.output_item.done', {
      type: 'response.output_item.done',
      output_index: block.outputIndex ?? 0,
      item
    })]
  }
  return completeResponsesTextBlock(state, block, block.text)
}

function completeResponsesTextBlock(state: AnthropicStreamState, block: AnthropicStreamBlockState, text: string): string[] {
  const item = {
    id: state.responseMessageId,
    type: 'message',
    status: 'completed',
    role: 'assistant',
    content: [{
      type: 'output_text',
      text,
      annotations: localToolAnnotationsForText(text, state.requestPlan)
    }]
  }
  state.outputItems.push(item)
  return [
    sse('response.output_text.done', {
      type: 'response.output_text.done',
      item_id: state.responseMessageId,
      output_index: block.outputIndex ?? 0,
      content_index: 0,
      text
    }),
    sse('response.content_part.done', {
      type: 'response.content_part.done',
      item_id: state.responseMessageId,
      output_index: block.outputIndex ?? 0,
      content_index: 0,
      part: item.content[0]
    }),
    sse('response.output_item.done', {
      type: 'response.output_item.done',
      output_index: block.outputIndex ?? 0,
      item
    })
  ]
}

function completeResponsesStream(state: AnthropicStreamState): string[] {
  if (state.completed || state.failed) return []
  const output = ensureResponsesStreamStarted(state)
  const blocks = [...state.blocks.values()].sort((left, right) => left.index - right.index)
  for (const block of blocks) {
    output.push(...completeResponsesBlock(state, block))
  }
  state.completed = true
  output.push(sse('response.completed', {
    type: 'response.completed',
    response: responseSnapshot(state, 'completed')
  }))
  return output
}

function failResponsesStream(state: AnthropicStreamState, error: JsonRecord): string[] {
  if (state.completed || state.failed) return []
  state.failed = true
  return [
    ...ensureResponsesStreamStarted(state),
    sse('response.failed', {
      type: 'response.failed',
      response: {
        ...responseSnapshot(state, 'in_progress'),
        status: 'failed',
        completed_at: Math.floor(Date.now() / 1000),
        error
      }
    })
  ]
}

function responseSnapshot(state: AnthropicStreamState, status: 'in_progress' | 'completed'): JsonRecord {
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
    output: status === 'completed' ? state.outputItems : [],
    parallel_tool_calls: false,
    previous_response_id: state.previousResponseId ?? null,
    reasoning: {
      effort: state.requestPlan?.reasoningEffort ?? null,
      summary: null
    },
    store: false,
    temperature: null,
    text: { format: { type: 'text' } },
    tool_choice: 'auto',
    tools: [],
    top_p: null,
    truncation: 'disabled',
    usage: status === 'completed'
      ? state.usage ?? estimatedResponsesUsageFromStreamState(state)
      : null,
    user: null,
    metadata: {}
  }
}

async function notifyResponsesCompletion(
  state: AnthropicStreamState,
  onCompleted: CodexResponsesChatBridgeCompletionHandler | undefined
): Promise<void> {
  if (!onCompleted || !state.completed || state.completionNotified) return
  state.completionNotified = true
  await onCompleted({
    responseId: state.responseId,
    createdAt: state.createdAt,
    model: state.model,
    outputItems: state.outputItems.map((item) => ({ ...item })),
    response: responseSnapshot(state, 'completed')
  })
}

function createAnthropicStreamState(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  model: string,
  previousResponseId?: string,
  requestPlan?: OpenAIToAnthropicBridgeRequestPlan
): AnthropicStreamState {
  const suffix = `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`
  return {
    sourceEndpointFamily,
    id: `msg_bridge_${suffix}`,
    chatId: `chatcmpl_anthropic_${suffix}`,
    responseId: `resp_anthropic_${suffix}`,
    createdAt: Math.floor(Date.now() / 1000),
    model,
    requestPlan,
    previousResponseId,
    roleSent: false,
    responsesStarted: false,
    responseMessageId: `msg_anthropic_${suffix}`,
    nextOutputIndex: 0,
    blocks: new Map(),
    outputItems: [],
    completed: false,
    failed: false,
    terminalReceived: false,
    completionNotified: false
  }
}

function streamBlockFromContentBlock(index: number, block: JsonRecord): AnthropicStreamBlockState {
  return {
    index,
    type: stringValue(block.type) ?? 'text',
    id: stringValue(block.id),
    name: stringValue(block.name),
    text: stringValue(block.text) ?? '',
    inputJson: block.type === 'tool_use' ? JSON.stringify(isPlainObject(block.input) ? block.input : {}) : '',
    done: false
  }
}

async function * iterateAnthropicSseEvents(body: AsyncIterable<Uint8Array>): AsyncIterable<ParsedSseEvent> {
  const decoder = new StringDecoder('utf8')
  let pending = ''
  for await (const chunk of body) {
    pending += decoder.write(Buffer.from(chunk))
    const split = takeCompleteSseEvents(pending)
    pending = split.rest
    for (const raw of split.events) {
      const parsed = parseSseEvent(raw)
      if (parsed) yield parsed
    }
  }
  pending += decoder.end()
  if (pending.trim()) {
    const parsed = parseSseEvent(pending)
    if (parsed) yield parsed
  }
}

function takeCompleteSseEvents(input: string): { events: string[]; rest: string } {
  const events: string[] = []
  let rest = input
  while (true) {
    const match = /\r?\n\r?\n/.exec(rest)
    if (!match || match.index === undefined) return { events, rest }
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

function chatSseChunk(state: AnthropicStreamState, delta: JsonRecord): string {
  return chatSseData({
    id: state.chatId,
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

function sse(event: string, data: JsonRecord): string {
  return `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`
}

function structuredOutputResultFromAnthropicBlocks(
  blocks: unknown[],
  requestPlan: OpenAIToAnthropicBridgeRequestPlan | undefined
): StructuredOutputResult | undefined {
  const toolName = requestPlan?.structuredOutput?.syntheticToolName
  if (!toolName) return undefined
  for (const block of blocks) {
    if (!isPlainObject(block) || block.type !== 'tool_use' || stringValue(block.name) !== toolName) continue
    return structuredOutputResultFromValue(isPlainObject(block.input) ? block.input : {}, requestPlan)
  }
  return undefined
}

function structuredOutputResultFromInputJson(
  value: string,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan | undefined
): StructuredOutputResult {
  const text = value.trim()
  if (!text) return structuredOutputResultFromValue({}, requestPlan)
  try {
    const parsed = JSON.parse(text) as unknown
    return structuredOutputResultFromValue(parsed, requestPlan)
  } catch {
    return {
      text,
      error: structuredOutputSchemaMismatchError('Anthropic structured output tool input 不是合法 JSON')
    }
  }
}

function structuredOutputResultFromValue(
  value: unknown,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan | undefined
): StructuredOutputResult {
  const text = JSON.stringify(value) ?? 'null'
  const structuredOutput = requestPlan?.structuredOutput
  if (structuredOutput?.type !== 'json_schema' || !structuredOutput.schema) {
    return { text }
  }
  const validationError = validateJsonSchemaSubset(value, structuredOutput.schema)
  return validationError
    ? { text, error: structuredOutputSchemaMismatchError(`Anthropic structured output 不符合 JSON Schema：${validationError}`) }
    : { text }
}

function structuredOutputSchemaMismatchError(message: string): JsonRecord {
  return {
    message,
    type: 'invalid_response_error',
    code: structuredOutputSchemaMismatchCode
  }
}

function prependResponsesLocalToolOutputItems(
  output: JsonRecord[],
  requestPlan: OpenAIToAnthropicBridgeRequestPlan | undefined,
  responseId: string
): void {
  const items = responsesLocalToolCallItems(requestPlan, responseId)
  if (items.length) output.unshift(...items)
}

function responsesLocalToolCallItems(
  requestPlan: OpenAIToAnthropicBridgeRequestPlan | undefined,
  responseId: string
): JsonRecord[] {
  return [
    responsesFileSearchCallItem(requestPlan, responseId)
  ].filter((item): item is JsonRecord => Boolean(item))
}

function responsesFileSearchCallItem(
  requestPlan: OpenAIToAnthropicBridgeRequestPlan | undefined,
  responseId: string
): JsonRecord | undefined {
  const fileSearch = requestPlan?.fileSearch
  if (!fileSearch || fileSearch.emitted) return undefined
  fileSearch.emitted = true
  const itemId = fileSearch.outputItemId ?? `fs_${safeIdSegment(responseId)}`
  fileSearch.outputItemId = itemId
  return {
    id: itemId,
    type: 'file_search_call',
    status: 'completed',
    queries: fileSearch.queries,
    results: fileSearch.includeResults
      ? fileSearch.results.map((result) => ({
        file_id: result.fileId,
        filename: result.filename,
        score: result.score,
        content: [{
          type: 'text',
          text: result.contentText
        }]
      }))
      : null
  }
}

function localToolAnnotationsForText(
  text: string,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan | undefined
): JsonRecord[] {
  return [
    ...fileSearchAnnotationsForText(text, requestPlan)
  ]
}

function fileSearchAnnotationsForText(
  text: string,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan | undefined
): JsonRecord[] {
  const results = requestPlan?.fileSearch?.results ?? []
  if (!text || results.length === 0) return []
  const annotations: JsonRecord[] = []
  for (const [index, result] of results.entries()) {
    const marker = `[F${index + 1}]`
    let searchFrom = 0
    while (searchFrom < text.length) {
      const startIndex = text.indexOf(marker, searchFrom)
      if (startIndex < 0) break
      const endIndex = startIndex + marker.length
      annotations.push({
        type: 'file_citation',
        index: startIndex,
        start_index: startIndex,
        end_index: endIndex,
        file_id: result.fileId,
        filename: result.filename
      })
      searchFrom = endIndex
    }
  }
  return annotations
}

function failedResponsesResponseFromStructuredOutputError(input: {
  responseId: string
  createdAt: number
  model: string
  previousResponseId?: string
  requestPlan?: OpenAIToAnthropicBridgeRequestPlan
  usage?: JsonRecord
  error: JsonRecord
}): JsonRecord {
  return {
    id: input.responseId,
    object: 'response',
    created_at: input.createdAt,
    status: 'failed',
    completed_at: input.createdAt,
    error: input.error,
    incomplete_details: null,
    instructions: null,
    max_output_tokens: null,
    model: input.model,
    output: [],
    output_text: '',
    parallel_tool_calls: false,
    previous_response_id: input.previousResponseId ?? null,
    reasoning: {
      effort: input.requestPlan?.reasoningEffort ?? null,
      summary: null
    },
    store: false,
    temperature: null,
    text: { format: { type: 'text' } },
    tool_choice: 'auto',
    tools: [],
    top_p: null,
    truncation: 'disabled',
    usage: anthropicUsageToResponsesUsage(input.usage),
    user: null,
    metadata: {}
  }
}

function chatCompletionFromStructuredOutputError(input: {
  id: string
  model: string
  error: JsonRecord
  usage?: JsonRecord
}): JsonRecord {
  const message = stringValue(input.error.message) ?? 'Anthropic structured output 不符合 JSON Schema'
  const code = stringValue(input.error.code) ?? structuredOutputSchemaMismatchCode
  return {
    id: input.id,
    object: 'chat.completion',
    created: Math.floor(Date.now() / 1000),
    model: input.model,
    choices: [{
      index: 0,
      message: {
        role: 'assistant',
        content: null,
        refusal: `${code}: ${message}`
      },
      finish_reason: 'stop'
    }],
    usage: anthropicUsageToChatUsage(input.usage)
  }
}

function validateJsonSchemaSubset(value: unknown, schema: unknown, path = '$', root: unknown = schema, depth = 0): string | undefined {
  if (depth > 64) return `${path} schema 递归过深`
  if (schema === true || schema === undefined) return undefined
  if (schema === false) return `${path} 不允许出现该值`
  if (!isPlainObject(schema)) return undefined

  const ref = stringValue(schema.$ref)
  if (ref) {
    const resolved = resolveJsonSchemaRef(ref, root)
    if (resolved === undefined) return `${path} 包含无法解析的 $ref ${ref}`
    return validateJsonSchemaSubset(value, resolved, path, root, depth + 1)
  }

  const allOf = Array.isArray(schema.allOf) ? schema.allOf : []
  for (const child of allOf) {
    const error = validateJsonSchemaSubset(value, child, path, root, depth + 1)
    if (error) return error
  }

  const anyOf = Array.isArray(schema.anyOf) ? schema.anyOf : []
  if (anyOf.length) {
    const matched = anyOf.some((child) => !validateJsonSchemaSubset(value, child, path, root, depth + 1))
    if (!matched) return `${path} 不匹配 anyOf 中任一 schema`
  }

  const oneOf = Array.isArray(schema.oneOf) ? schema.oneOf : []
  if (oneOf.length) {
    const matchCount = oneOf.filter((child) => !validateJsonSchemaSubset(value, child, path, root, depth + 1)).length
    if (matchCount !== 1) return `${path} 需要且只能匹配 oneOf 中一个 schema，实际匹配 ${matchCount} 个`
  }

  const notSchema = hasOwn(schema, 'not') ? schema.not : undefined
  if (notSchema !== undefined && !validateJsonSchemaSubset(value, notSchema, path, root, depth + 1)) {
    return `${path} 命中了 not 禁止的 schema`
  }

  if (hasOwn(schema, 'const') && !jsonDeepEqual(value, schema.const)) {
    return `${path} 不等于 const 要求`
  }
  if (Array.isArray(schema.enum) && !schema.enum.some((item) => jsonDeepEqual(value, item))) {
    return `${path} 不在 enum 允许值内`
  }

  const types = jsonSchemaTypes(schema.type)
  if (types.length && !types.some((type) => jsonSchemaTypeMatches(value, type))) {
    return `${path} 类型应为 ${types.join('|')}，实际为 ${jsonSchemaValueType(value)}`
  }

  const properties = objectValue(schema.properties)
  const required = Array.isArray(schema.required) ? schema.required.filter((item): item is string => typeof item === 'string') : []
  const hasObjectKeywords = Boolean(properties) || required.length > 0 || hasOwn(schema, 'additionalProperties')
  if (hasObjectKeywords) {
    if (!isPlainObject(value)) return `${path} 类型应为 object，实际为 ${jsonSchemaValueType(value)}`
    for (const property of required) {
      if (!hasOwn(value, property)) return `${path}.${property} 缺少 required 字段`
    }
    const propertySchemas = properties ?? {}
    for (const [key, childSchema] of Object.entries(propertySchemas)) {
      if (!hasOwn(value, key)) continue
      const error = validateJsonSchemaSubset(value[key], childSchema, `${path}.${key}`, root, depth + 1)
      if (error) return error
    }
    const additionalProperties = schema.additionalProperties
    if (additionalProperties === false) {
      const allowed = new Set(Object.keys(propertySchemas))
      const extra = Object.keys(value).find((key) => !allowed.has(key))
      if (extra) return `${path}.${extra} 不允许 additionalProperties`
    } else if (isPlainObject(additionalProperties) || additionalProperties === false) {
      const allowed = new Set(Object.keys(propertySchemas))
      for (const key of Object.keys(value)) {
        if (allowed.has(key)) continue
        const error = validateJsonSchemaSubset(value[key], additionalProperties, `${path}.${key}`, root, depth + 1)
        if (error) return error
      }
    }
  }

  if (hasOwn(schema, 'items')) {
    if (!Array.isArray(value)) {
      if (types.includes('array')) return `${path} 类型应为 array，实际为 ${jsonSchemaValueType(value)}`
      return undefined
    }
    const items = schema.items
    if (Array.isArray(items)) {
      for (let index = 0; index < Math.min(value.length, items.length); index += 1) {
        const error = validateJsonSchemaSubset(value[index], items[index], `${path}[${index}]`, root, depth + 1)
        if (error) return error
      }
    } else {
      for (let index = 0; index < value.length; index += 1) {
        const error = validateJsonSchemaSubset(value[index], items, `${path}[${index}]`, root, depth + 1)
        if (error) return error
      }
    }
  }

  return validateJsonSchemaStringAndNumberKeywords(value, schema, path)
}

function validateJsonSchemaStringAndNumberKeywords(value: unknown, schema: JsonRecord, path: string): string | undefined {
  if (typeof value === 'string') {
    const minLength = integerValue(schema.minLength)
    if (minLength !== undefined && value.length < minLength) return `${path} 长度小于 minLength ${minLength}`
    const maxLength = integerValue(schema.maxLength)
    if (maxLength !== undefined && value.length > maxLength) return `${path} 长度大于 maxLength ${maxLength}`
    const pattern = stringValue(schema.pattern)
    if (pattern) {
      try {
        if (!new RegExp(pattern).test(value)) return `${path} 不匹配 pattern`
      } catch {
        return `${path} schema pattern 无效`
      }
    }
  }
  if (typeof value === 'number' && Number.isFinite(value)) {
    const minimum = numberValue(schema.minimum)
    if (minimum !== undefined && value < minimum) return `${path} 小于 minimum ${minimum}`
    const maximum = numberValue(schema.maximum)
    if (maximum !== undefined && value > maximum) return `${path} 大于 maximum ${maximum}`
    const exclusiveMinimum = numberValue(schema.exclusiveMinimum)
    if (exclusiveMinimum !== undefined && value <= exclusiveMinimum) return `${path} 小于等于 exclusiveMinimum ${exclusiveMinimum}`
    const exclusiveMaximum = numberValue(schema.exclusiveMaximum)
    if (exclusiveMaximum !== undefined && value >= exclusiveMaximum) return `${path} 大于等于 exclusiveMaximum ${exclusiveMaximum}`
  }
  if (Array.isArray(value)) {
    const minItems = integerValue(schema.minItems)
    if (minItems !== undefined && value.length < minItems) return `${path} 数组长度小于 minItems ${minItems}`
    const maxItems = integerValue(schema.maxItems)
    if (maxItems !== undefined && value.length > maxItems) return `${path} 数组长度大于 maxItems ${maxItems}`
  }
  return undefined
}

function jsonSchemaTypes(value: unknown): string[] {
  if (typeof value === 'string') return [value]
  if (!Array.isArray(value)) return []
  return value.filter((item): item is string => typeof item === 'string' && item.length > 0)
}

function jsonSchemaTypeMatches(value: unknown, type: string): boolean {
  if (type === 'null') return value === null
  if (type === 'boolean') return typeof value === 'boolean'
  if (type === 'object') return isPlainObject(value)
  if (type === 'array') return Array.isArray(value)
  if (type === 'number') return typeof value === 'number' && Number.isFinite(value)
  if (type === 'integer') return typeof value === 'number' && Number.isInteger(value)
  if (type === 'string') return typeof value === 'string'
  return true
}

function jsonSchemaValueType(value: unknown): string {
  if (value === null) return 'null'
  if (Array.isArray(value)) return 'array'
  if (typeof value === 'number' && Number.isInteger(value)) return 'integer'
  return typeof value
}

function resolveJsonSchemaRef(ref: string, root: unknown): unknown {
  if (!ref.startsWith('#/')) return undefined
  let current = root
  for (const rawPart of ref.slice(2).split('/')) {
    const part = rawPart.replace(/~1/g, '/').replace(/~0/g, '~')
    if (!isPlainObject(current) && !Array.isArray(current)) return undefined
    current = (current as Record<string, unknown>)[part]
  }
  return current
}

function jsonDeepEqual(left: unknown, right: unknown): boolean {
  if (Object.is(left, right)) return true
  if (Array.isArray(left) || Array.isArray(right)) {
    if (!Array.isArray(left) || !Array.isArray(right) || left.length !== right.length) return false
    return left.every((item, index) => jsonDeepEqual(item, right[index]))
  }
  if (isPlainObject(left) || isPlainObject(right)) {
    if (!isPlainObject(left) || !isPlainObject(right)) return false
    const leftKeys = Object.keys(left).sort()
    const rightKeys = Object.keys(right).sort()
    if (leftKeys.length !== rightKeys.length) return false
    return leftKeys.every((key, index) => key === rightKeys[index] && jsonDeepEqual(left[key], right[key]))
  }
  return false
}

function isStructuredOutputStreamBlock(state: AnthropicStreamState, block: AnthropicStreamBlockState): boolean {
  const toolName = state.requestPlan?.structuredOutput?.syntheticToolName
  return Boolean(toolName && block.type === 'tool_use' && block.name === toolName)
}

function responsesReasoningItemId(responseId: string, index: number): string {
  return `rs_${safeIdSegment(`${responseId}_${index}`)}`
}

function responsesReasoningItem(responseId: string, index: number, text: string): JsonRecord {
  return {
    id: responsesReasoningItemId(responseId, index),
    type: 'reasoning',
    status: 'completed',
    summary: text
      ? [{ type: 'summary_text', text }]
      : []
  }
}

function anthropicToolUseBlocks(blocks: unknown[]): JsonRecord[] {
  return blocks.filter((block): block is JsonRecord => isPlainObject(block) && block.type === 'tool_use')
}

function anthropicTextFromBlock(block: unknown): string {
  return isPlainObject(block) && block.type === 'text' ? stringValue(block.text) ?? '' : ''
}

function anthropicThinkingTextFromBlock(block: unknown): string {
  if (!isPlainObject(block) || block.type !== 'thinking') return ''
  return stringValue(block.thinking) ?? stringValue(block.text) ?? ''
}

function anthropicUsageToChatUsage(usage: JsonRecord | undefined, previous?: JsonRecord): JsonRecord {
  const inputTokens = anthropicInputTokens(usage) || integerValue(previous?.prompt_tokens) || 0
  const outputTokens = integerValue(usage?.output_tokens) ?? integerValue(previous?.completion_tokens) ?? 0
  return {
    prompt_tokens: inputTokens,
    completion_tokens: outputTokens,
    total_tokens: inputTokens + outputTokens,
    prompt_tokens_details: {
      cached_tokens: integerValue(usage?.cache_read_input_tokens) ?? integerValue(objectValue(previous?.prompt_tokens_details)?.cached_tokens) ?? 0
    }
  }
}

function anthropicUsageToResponsesUsage(usage: JsonRecord | undefined, previous?: JsonRecord): JsonRecord {
  const inputTokens = anthropicInputTokens(usage) || integerValue(previous?.input_tokens) || 0
  const outputTokens = integerValue(usage?.output_tokens) ?? integerValue(previous?.output_tokens) ?? 0
  const reasoningTokens = anthropicThinkingTokens(usage)
    ?? integerValue(objectValue(previous?.output_tokens_details)?.reasoning_tokens)
    ?? 0
  return {
    input_tokens: inputTokens,
    input_tokens_details: {
      cached_tokens: integerValue(usage?.cache_read_input_tokens) ?? integerValue(objectValue(previous?.input_tokens_details)?.cached_tokens) ?? 0
    },
    output_tokens: outputTokens,
    output_tokens_details: {
      reasoning_tokens: reasoningTokens
    },
    total_tokens: inputTokens + outputTokens
  }
}

function anthropicInputTokens(usage: JsonRecord | undefined): number {
  if (!usage) return 0
  return (integerValue(usage.input_tokens) ?? 0)
    + (integerValue(usage.cache_creation_input_tokens) ?? 0)
    + (integerValue(usage.cache_read_input_tokens) ?? 0)
}

function anthropicThinkingTokens(usage: JsonRecord | undefined): number | undefined {
  if (!usage) return undefined
  const outputDetails = objectValue(usage.output_tokens_details)
  return integerValue(outputDetails?.thinking_tokens) ?? integerValue(usage.thinking_tokens)
}

function estimatedChatUsageFromStreamState(state: AnthropicStreamState): JsonRecord {
  const outputText = [...state.blocks.values()].map((block) => `${block.text}\n${block.inputJson}`).join('\n')
  const outputTokens = estimateTokenCountFromText(outputText)
  return {
    prompt_tokens: 0,
    completion_tokens: outputTokens,
    total_tokens: outputTokens
  }
}

function estimatedResponsesUsageFromStreamState(state: AnthropicStreamState): JsonRecord {
  const outputText = [...state.blocks.values()].map((block) => `${block.text}\n${block.inputJson}`).join('\n')
  const outputTokens = estimateTokenCountFromText(outputText)
  return {
    input_tokens: 0,
    input_tokens_details: { cached_tokens: 0 },
    output_tokens: outputTokens,
    output_tokens_details: { reasoning_tokens: 0 },
    total_tokens: outputTokens
  }
}

function openAIChatFinishReasonFromAnthropic(reason: string | undefined): string {
  if (reason === 'max_tokens') return 'length'
  if (reason === 'tool_use') return 'tool_calls'
  if (reason === 'stop_sequence' || reason === 'end_turn' || !reason) return 'stop'
  return reason
}

function openAIErrorFromAnthropicPayload(value: unknown): JsonRecord {
  const record = isPlainObject(value) ? value : {}
  const error = objectValue(record.error) ?? record
  const message = stringValue(error.message) ?? '上游 Anthropic Messages 请求失败'
  const type = stringValue(error.type) ?? stringValue(record.type) ?? 'upstream_error'
  return {
    error: {
      message,
      type,
      code: type
    }
  }
}

async function readJsonBody(body: AsyncIterable<Uint8Array>): Promise<unknown> {
  const chunks: Buffer[] = []
  let total = 0
  for await (const chunk of body) {
    const buffer = Buffer.from(chunk)
    total += buffer.length
    if (total > 16 * 1024 * 1024) {
      throw bridgeValidationError('Anthropic 桥接响应体超过读取上限', 'openai_anthropic_bridge_response_too_large', 502)
    }
    chunks.push(buffer)
  }
  const text = Buffer.concat(chunks).toString('utf8')
  return text ? JSON.parse(text) as unknown : {}
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
    throw bridgeValidationError('OpenAI 到 Anthropic 桥接要求请求体是有效 JSON 对象', 'openai_anthropic_bridge_invalid_json_body')
  }
  const rawBody = requestWithBody.rawBody
  if (!rawBody || rawBody.length === 0) {
    return {}
  }
  try {
    const parsed = rawBody.length > gatewayJsonBodyInlineParseMaxBytes
      ? await parseGatewayJsonBodyInWorker(rawBody, undefined, signal)
      : JSON.parse(rawBody.toString('utf8')) as unknown
    if (!isPlainObject(parsed)) {
      throw bridgeValidationError('OpenAI 到 Anthropic 桥接要求请求体是 JSON 对象', 'openai_anthropic_bridge_invalid_json_body')
    }
    return { ...parsed }
  } catch (error) {
    if (isGatewayJsonWorkerQueueFullError(error)) {
      throw bridgeValidationError('网关请求解析繁忙，请稍后重试', 'gateway_json_parser_busy', 503, 'server_overloaded')
    }
    if (error instanceof GatewayRequestValidationError) throw error
    throw bridgeValidationError('OpenAI 到 Anthropic 桥接要求请求体是有效 JSON 对象', 'openai_anthropic_bridge_invalid_json_body')
  }
}

function openAIContentToText(value: unknown): string {
  if (typeof value === 'string') return value
  if (!Array.isArray(value)) return ''
  return value
    .map((item) => {
      if (!isPlainObject(item)) return ''
      if (item.type === 'text' || item.type === 'input_text' || item.type === 'output_text' || item.type === undefined) {
        return stringValue(item.text) ?? ''
      }
      return ''
    })
    .filter(Boolean)
    .join('\n')
}

function responsesContentToText(value: unknown): string {
  if (typeof value === 'string') return value
  if (!Array.isArray(value)) return ''
  return value.map(responsesTextFromValue).filter(Boolean).join('\n')
}

function responsesTextFromValue(value: unknown): string {
  if (typeof value === 'string') return value
  if (!Array.isArray(value)) {
    return isPlainObject(value) ? stringValue(value.text) ?? '' : ''
  }
  return value
    .map((item) => isPlainObject(item) ? stringValue(item.text) ?? '' : '')
    .filter(Boolean)
    .join('\n')
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
    throw bridgeValidationError(
      'Codex compact snapshot 未完成服务端恢复，不能直接桥接到 Anthropic Messages',
      'openai_anthropic_bridge_compact_snapshot_unresolved'
    )
  }
  const prefix = 'juhecmp.v1.'
  if (!encryptedContent.startsWith(prefix)) return encryptedContent
  try {
    const parsed = JSON.parse(Buffer.from(encryptedContent.slice(prefix.length), 'base64url').toString('utf8')) as unknown
    return isPlainObject(parsed) ? stringValue(parsed.summary) ?? '' : ''
  } catch {
    return ''
  }
}

function imageUrlFromOpenAIImagePart(value: unknown): string | undefined {
  if (typeof value === 'string') return value
  return isPlainObject(value) ? stringValue(value.url) : undefined
}

function parseDataUrl(value: string): { mediaType: string; data: string } | undefined {
  const match = /^data:([^;,]+)(?:;[^,]*)?;base64,(.+)$/is.exec(value)
  if (!match) return undefined
  return {
    mediaType: match[1].toLowerCase(),
    data: match[2]
  }
}

function normalizedBase64Data(value: string): string | undefined {
  const compact = value.replace(/\s+/g, '').replace(/-/g, '+').replace(/_/g, '/')
  if (!compact || !/^[A-Za-z0-9+/]*={0,2}$/.test(compact)) return undefined
  const unpadded = compact.replace(/=+$/, '')
  const paddingLength = (4 - (unpadded.length % 4)) % 4
  const normalized = `${unpadded}${'='.repeat(paddingLength)}`
  return normalized.length % 4 === 0 ? normalized : undefined
}

function normalizeOpenAIFileMediaType(value: string | undefined, filename: string | undefined): string | undefined {
  const mediaType = value?.split(';', 1)[0]?.trim().toLowerCase()
  if (mediaType && mediaType !== 'application/octet-stream') return mediaType
  return mediaTypeFromFilename(filename)
}

function mediaTypeFromFilename(filename: string | undefined): string | undefined {
  const lower = filename?.toLowerCase()
  if (!lower) return undefined
  if (lower.endsWith('.pdf')) return 'application/pdf'
  if (lower.endsWith('.txt')) return 'text/plain'
  if (lower.endsWith('.md') || lower.endsWith('.markdown')) return 'text/markdown'
  if (lower.endsWith('.csv')) return 'text/csv'
  if (lower.endsWith('.png')) return 'image/png'
  if (lower.endsWith('.jpg') || lower.endsWith('.jpeg')) return 'image/jpeg'
  if (lower.endsWith('.gif')) return 'image/gif'
  if (lower.endsWith('.webp')) return 'image/webp'
  return undefined
}

function isTextFileMediaType(mediaType: string | undefined): boolean {
  return mediaType === 'text/plain' || Boolean(mediaType?.startsWith('text/'))
}

function isAnthropicSupportedImageMediaType(mediaType: string | undefined): mediaType is 'image/jpeg' | 'image/png' | 'image/gif' | 'image/webp' {
  return mediaType === 'image/jpeg'
    || mediaType === 'image/png'
    || mediaType === 'image/gif'
    || mediaType === 'image/webp'
}

function isPdfFilename(filename: string | undefined): boolean {
  return filename?.toLowerCase().endsWith('.pdf') === true
}

function isPdfUrl(value: string): boolean {
  try {
    return new URL(value).pathname.toLowerCase().endsWith('.pdf')
  } catch {
    return false
  }
}

function parseJsonObjectString(value: unknown): JsonRecord | undefined {
  if (isPlainObject(value)) return value
  if (typeof value !== 'string' || !value.trim()) return undefined
  try {
    const parsed = JSON.parse(value) as unknown
    return isPlainObject(parsed) ? parsed : undefined
  } catch {
    return undefined
  }
}

function stopSequencesValue(value: unknown): string[] {
  if (typeof value === 'string' && value) return [value]
  if (!Array.isArray(value)) return []
  return value.filter((item): item is string => typeof item === 'string' && item.length > 0).slice(0, 4)
}

function normalizeWhitespace(value: string | undefined): string | undefined {
  const normalized = value?.replace(/\s+/g, ' ').trim()
  return normalized || undefined
}

function normalizedOpenAIPath(path: string): string {
  const requestPath = path.startsWith('/') ? path : `/${path}`
  return requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
}

function chatCompletionIdFromAnthropicId(id: string | undefined): string {
  return `chatcmpl_${safeIdSegment(id ?? `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`)}`
}

function responseIdFromAnthropicId(id: string | undefined): string {
  return `resp_${safeIdSegment(id ?? `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`)}`
}

function safeIdSegment(value: string): string {
  const normalized = value.replace(/[^A-Za-z0-9_-]/g, '_')
  return normalized.slice(0, 96) || 'anthropic'
}

function bridgeValidationError(
  message: string,
  code: string,
  statusCode = 400,
  type = 'invalid_request_error'
): GatewayRequestValidationError {
  return new GatewayRequestValidationError(message, code, { statusCode, type })
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

function stringArrayValue(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.filter((item): item is string => typeof item === 'string' && item.length > 0)
}

function numberValue(value: unknown): number | undefined {
  const number = typeof value === 'number'
    ? value
    : typeof value === 'string' && value.trim() ? Number(value) : NaN
  return Number.isFinite(number) ? number : undefined
}

function integerValue(value: unknown): number | undefined {
  const number = typeof value === 'number'
    ? value
    : typeof value === 'string' && value.trim() ? Number(value) : NaN
  if (!Number.isFinite(number)) return undefined
  return Math.trunc(number)
}

function hasOwn(value: JsonRecord, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(value, key)
}
