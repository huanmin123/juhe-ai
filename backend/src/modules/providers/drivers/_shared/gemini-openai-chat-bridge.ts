import { StringDecoder } from 'node:string_decoder'
import type { Request } from 'express'

import type { AccountSupportedEndpointMode } from '../../../../domain/types.js'
import {
  geminiEndpointFamilyFromPath
} from '../../../../domain/gemini-endpoint-modes.js'
import {
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY
} from '../../../../domain/provider-protocol.js'
import {
  getGatewayRequestBodyState,
  gatewayJsonBodyInlineParseMaxBytes,
  type GatewayRawBodyRequest
} from '../../../gateway/request/body.js'
import {
  isGatewayJsonWorkerQueueFullError,
  parseGatewayJsonBodyInWorker
} from '../../../gateway/request/json-parser.js'
import { GatewayAgentGuidanceResponse, GatewayRequestValidationError } from '../../../gateway/request/validation-error.js'
import { splitPathAndQuery } from '../../../gateway/protocols/openai-v1/route-helpers.js'
import { parseOpenAISseEventText } from '../../../gateway/protocols/openai-v1/stream-events.js'
import type { GatewayUpstreamResponse } from '../../../gateway/upstream/request.js'

type JsonRecord = Record<string, unknown>

interface BuildGeminiGenerateContentChatBridgeBodyOptions {
  defaultModel: string
  guidanceProviderName?: string
  modelOverride?: string
}

interface TransformGeminiGenerateContentChatBridgeResponseOptions {
  enabled: boolean
  model: string
}

interface GeminiToolCallState {
  id: string
  name: string
  arguments: string
}

interface GeminiChatStreamState {
  model: string
  completed: boolean
  failed: boolean
  emittedContent: boolean
  finishReason?: string
  usage?: JsonRecord
  toolCalls: Map<number, GeminiToolCallState>
}

interface GeminiRequestToolCallIdState {
  idsByName: Map<string, string>
  nextIndex: number
}

export function isGeminiGenerateContentPostRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') return false
  const family = geminiGenerateContentEndpointFamily(req)
  return family === GEMINI_GENERATE_CONTENT_FAMILY || family === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
}

export function isGeminiGenerateContentStreamRequest(req: Request): boolean {
  return geminiGenerateContentEndpointFamily(req) === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
}

export function geminiGenerateContentChatBridgeRequiredEndpointMode(req: Request): AccountSupportedEndpointMode {
  return isGeminiGenerateContentStreamRequest(req) ? 'chat_sse' : 'chat_json'
}

export function prepareGeminiGenerateContentChatBridgeHeaders(headers: Headers, req: Request): void {
  headers.set('accept', isGeminiGenerateContentStreamRequest(req) ? 'text/event-stream' : 'application/json')
  headers.set('content-type', 'application/json')
  headers.delete('content-length')
  headers.delete('x-goog-api-client')
  headers.delete('x-goog-api-key')
}

export async function buildGeminiGenerateContentChatBridgeBody(
  req: Request,
  options: BuildGeminiGenerateContentChatBridgeBodyOptions,
  signal?: AbortSignal
): Promise<Buffer> {
  const body = await parseGatewayJsonObject(req, signal)
  const model = options.modelOverride ?? options.defaultModel
  validateGeminiGenerateContentChatBridgeBody(req, body, model, options.guidanceProviderName)
  const chatBody = geminiGenerateContentBodyToChatCompletionsBody(req, body, model, options.guidanceProviderName)
  return Buffer.from(JSON.stringify(chatBody), 'utf8')
}

export function transformGeminiGenerateContentChatBridgeUpstreamResponse(
  req: Request,
  response: GatewayUpstreamResponse,
  options: TransformGeminiGenerateContentChatBridgeResponseOptions
): GatewayUpstreamResponse {
  if (!options.enabled || !response.ok || !response.body || !isGeminiGenerateContentPostRequest(req)) {
    return response
  }
  const headers = new Headers(response.headers)
  headers.delete('content-length')
  if (isGeminiGenerateContentStreamRequest(req)) {
    headers.set('content-type', 'text/event-stream; charset=utf-8')
    return {
      status: response.status,
      ok: response.ok,
      headers,
      body: transformChatCompletionsSseToGeminiGenerateContentSse(response.body, options.model)
    }
  }
  headers.set('content-type', 'application/json; charset=utf-8')
  return {
    status: response.status,
    ok: response.ok,
    headers,
    body: transformChatCompletionsJsonToGeminiGenerateContentJson(response.body, options.model)
  }
}

function geminiGenerateContentEndpointFamily(req: Request): typeof GEMINI_GENERATE_CONTENT_FAMILY | typeof GEMINI_STREAM_GENERATE_CONTENT_FAMILY | undefined {
  const { path } = splitPathAndQuery(req.originalUrl || req.path || '')
  const family = geminiEndpointFamilyFromPath(path)
  return family === GEMINI_GENERATE_CONTENT_FAMILY || family === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
    ? family
    : undefined
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
      'Gemini GenerateContent 到 Chat Completions 桥接要求请求体是有效 JSON 对象',
      'invalid_gemini_chat_bridge_json_body'
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
      'Gemini GenerateContent 到 Chat Completions 桥接要求请求体是有效 JSON 对象',
      'invalid_gemini_chat_bridge_json_body'
    )
  }
  if (!isPlainObject(parsed)) {
    throw new GatewayRequestValidationError(
      'Gemini GenerateContent 到 Chat Completions 桥接要求请求体是 JSON 对象',
      'invalid_gemini_chat_bridge_json_body'
    )
  }
  return { ...parsed }
}

function validateGeminiGenerateContentChatBridgeBody(
  req: Request,
  body: JsonRecord,
  model: string,
  providerName?: string
): void {
  if (!Array.isArray(body.contents)) {
    throw new GatewayRequestValidationError(
      'Gemini GenerateContent 到 Chat Completions 桥接要求 contents 是数组',
      'invalid_gemini_chat_bridge_contents'
    )
  }
  if (body.cachedContent !== undefined) {
    throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Chat Completions 上游不能保真承载 Gemini cachedContent。请客户端改用真实支持 cachedContent 的 Gemini 原生上游，或移除 cachedContent 后重试。`, 'unsupported_gemini_cached_content')
  }
}

function geminiGenerateContentBodyToChatCompletionsBody(
  req: Request,
  body: JsonRecord,
  model: string,
  providerName?: string
): JsonRecord {
  const toolCallState: GeminiRequestToolCallIdState = {
    idsByName: new Map(),
    nextIndex: 0
  }
  const messages: JsonRecord[] = []
  appendGeminiSystemInstruction(req, messages, body.systemInstruction, model, providerName)
  for (const item of body.contents as unknown[]) {
    const content = objectValue(item)
    if (!content) continue
    appendGeminiContent(req, messages, content, toolCallState, model, providerName)
  }

  const output: JsonRecord = {
    model,
    messages,
    stream: isGeminiGenerateContentStreamRequest(req)
  }
  applyGeminiGenerationConfig(req, output, objectValue(body.generationConfig), model, providerName)
  const tools = geminiToolsToChatTools(req, body.tools, model, providerName)
  if (tools.length > 0) {
    output.tools = tools
    const toolChoice = geminiToolChoiceToChatToolChoice(req, objectValue(body.toolConfig), model, providerName)
    if (toolChoice !== undefined) output.tool_choice = toolChoice
  }
  return output
}

function appendGeminiSystemInstruction(
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
  const instruction = objectValue(value)
  if (!instruction) {
    throw new GatewayRequestValidationError(
      'Gemini systemInstruction 必须是字符串或 Content 对象',
      'invalid_gemini_chat_bridge_system_instruction'
    )
  }
  const parts = Array.isArray(instruction.parts) ? instruction.parts : []
  const text = parts.map((partValue) => {
    const part = objectValue(partValue)
    if (!part) return ''
    if (typeof part.text !== 'string') {
      throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Chat Completions 上游只支持把 Gemini systemInstruction text part 转换为 system 消息。请移除 systemInstruction 中的非 text part 后重试。`, 'unsupported_gemini_system_instruction_part')
    }
    return part.text
  }).filter(Boolean).join('\n')
  if (text) output.push({ role: 'system', content: text })
}

function appendGeminiContent(
  req: Request,
  output: JsonRecord[],
  content: JsonRecord,
  toolCallState: GeminiRequestToolCallIdState,
  model: string,
  providerName?: string
): void {
  const role = stringValue(content.role) ?? 'user'
  const parts = Array.isArray(content.parts) ? content.parts : []
  if (parts.length === 0) return
  if (parts.some((part) => objectValue(part)?.functionResponse !== undefined)) {
    appendGeminiFunctionResponses(req, output, parts, toolCallState, model, providerName)
    return
  }
  if (role === 'model') {
    output.push(geminiModelContentToChatAssistantMessage(req, parts, toolCallState, model, providerName))
    return
  }
  if (role !== 'user' && role !== 'function') {
    throw new GatewayRequestValidationError(
      'Gemini GenerateContent 到 Chat Completions 桥接只支持 user、model 和 function 角色',
      'invalid_gemini_chat_bridge_role'
    )
  }
  output.push({
    role: 'user',
    content: geminiUserPartsToChatContent(req, parts, model, providerName)
  })
}

function appendGeminiFunctionResponses(
  req: Request,
  output: JsonRecord[],
  parts: unknown[],
  toolCallState: GeminiRequestToolCallIdState,
  model: string,
  providerName?: string
): void {
  const pendingUserParts: JsonRecord[] = []
  const flushUserParts = () => {
    if (!pendingUserParts.length) return
    output.push({ role: 'user', content: chatUserContentFromParts(pendingUserParts) })
    pendingUserParts.length = 0
  }
  for (const partValue of parts) {
    const part = objectValue(partValue)
    if (!part) continue
    const response = objectValue(part.functionResponse)
    if (response) {
      flushUserParts()
      const name = stringValue(response.name)
      if (!name) {
        throw new GatewayRequestValidationError(
          'Gemini functionResponse 缺少 name',
          'invalid_gemini_chat_bridge_function_response'
        )
      }
      output.push({
        role: 'tool',
        tool_call_id: toolCallIdForName(toolCallState, name),
        content: JSON.stringify(response.response ?? {})
      })
      continue
    }
    appendGeminiPartToUserParts(req, pendingUserParts, part, model, providerName)
  }
  flushUserParts()
}

function geminiModelContentToChatAssistantMessage(
  req: Request,
  parts: unknown[],
  toolCallState: GeminiRequestToolCallIdState,
  model: string,
  providerName?: string
): JsonRecord {
  const textParts: string[] = []
  const toolCalls: JsonRecord[] = []
  for (const partValue of parts) {
    const part = objectValue(partValue)
    if (!part) continue
    if (typeof part.text === 'string') {
      textParts.push(part.text)
      continue
    }
    const call = objectValue(part.functionCall)
    if (call) {
      const name = stringValue(call.name)
      if (!name) {
        throw new GatewayRequestValidationError(
          'Gemini functionCall 缺少 name',
          'invalid_gemini_chat_bridge_function_call'
        )
      }
      toolCalls.push({
        id: toolCallIdForName(toolCallState, name),
        type: 'function',
        function: {
          name,
          arguments: JSON.stringify(objectValue(call.args) ?? {})
        }
      })
      continue
    }
    throw unsupportedGeminiPart(req, model, providerName, part)
  }
  const content = textParts.join('')
  const output: JsonRecord = {
    role: 'assistant',
    content: toolCalls.length > 0 && !content ? null : content
  }
  if (toolCalls.length > 0) output.tool_calls = toolCalls
  return output
}

function geminiUserPartsToChatContent(
  req: Request,
  parts: unknown[],
  model: string,
  providerName?: string
): string | JsonRecord[] {
  const output: JsonRecord[] = []
  for (const partValue of parts) {
    const part = objectValue(partValue)
    if (!part) continue
    appendGeminiPartToUserParts(req, output, part, model, providerName)
  }
  return chatUserContentFromParts(output)
}

function appendGeminiPartToUserParts(
  req: Request,
  output: JsonRecord[],
  part: JsonRecord,
  model: string,
  providerName?: string
): void {
  if (typeof part.text === 'string') {
    output.push({ type: 'text', text: part.text })
    return
  }
  const inlineData = objectValue(part.inlineData)
  if (inlineData) {
    output.push({ type: 'image_url', image_url: { url: geminiInlineDataToChatImageUrl(req, inlineData, model, providerName) } })
    return
  }
  const fileData = objectValue(part.fileData)
  if (fileData) {
    output.push({ type: 'image_url', image_url: { url: geminiFileDataToChatImageUrl(req, fileData, model, providerName) } })
    return
  }
  if (part.functionCall !== undefined || part.functionResponse !== undefined) {
    throw new GatewayRequestValidationError(
      'Gemini functionCall/functionResponse 只能出现在 model 或 function 响应消息中',
      'invalid_gemini_chat_bridge_function_part'
    )
  }
  throw unsupportedGeminiPart(req, model, providerName, part)
}

function geminiInlineDataToChatImageUrl(
  req: Request,
  inlineData: JsonRecord,
  model: string,
  providerName?: string
): string {
  const mimeType = stringValue(inlineData.mimeType) ?? stringValue(inlineData.mime_type) ?? 'application/octet-stream'
  const data = stringValue(inlineData.data)
  if (!data) {
    throw new GatewayRequestValidationError(
      'Gemini inlineData 缺少 data',
      'invalid_gemini_chat_bridge_inline_data'
    )
  }
  if (!mimeType.toLowerCase().startsWith('image/')) {
    throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Chat Completions 上游只支持把 Gemini inlineData 图片转换为 image_url。请移除非图片 inlineData，或改用真实支持该输入的 Gemini 原生上游。`, 'unsupported_gemini_inline_data')
  }
  return `data:${mimeType};base64,${data}`
}

function geminiFileDataToChatImageUrl(
  req: Request,
  fileData: JsonRecord,
  model: string,
  providerName?: string
): string {
  const mimeType = stringValue(fileData.mimeType) ?? stringValue(fileData.mime_type) ?? ''
  const fileUri = stringValue(fileData.fileUri) ?? stringValue(fileData.file_uri)
  if (!fileUri) {
    throw new GatewayRequestValidationError(
      'Gemini fileData 缺少 fileUri',
      'invalid_gemini_chat_bridge_file_data'
    )
  }
  if (!mimeType.toLowerCase().startsWith('image/') || !/^https?:\/\//i.test(fileUri)) {
    throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Chat Completions 上游只支持可公开访问的图片 fileData。请改用 https 图片 URL、inlineData 图片，或选择 Gemini 原生上游。`, 'unsupported_gemini_file_data')
  }
  return fileUri
}

function applyGeminiGenerationConfig(
  req: Request,
  output: JsonRecord,
  generationConfig: JsonRecord | undefined,
  model: string,
  providerName?: string
): void {
  if (!generationConfig) return
  if (typeof generationConfig.temperature === 'number') output.temperature = generationConfig.temperature
  if (typeof generationConfig.topP === 'number') output.top_p = generationConfig.topP
  const maxTokens = integerValue(generationConfig.maxOutputTokens)
  if (maxTokens !== undefined) output.max_tokens = maxTokens
  const stop = geminiStopSequencesToChatStop(generationConfig.stopSequences)
  if (stop !== undefined) output.stop = stop
  const candidateCount = integerValue(generationConfig.candidateCount)
  if (candidateCount !== undefined && candidateCount > 0) output.n = candidateCount
  const responseMimeType = stringValue(generationConfig.responseMimeType)
  if (responseMimeType && responseMimeType !== 'text/plain') {
    if (responseMimeType === 'application/json') {
      output.response_format = { type: 'json_object' }
    } else {
      throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Chat Completions 上游不支持 Gemini responseMimeType=${responseMimeType}。请改用 text/plain 或 application/json。`, 'unsupported_gemini_response_mime_type')
    }
  }
}

function geminiToolsToChatTools(
  req: Request,
  value: unknown,
  model: string,
  providerName?: string
): JsonRecord[] {
  if (value === undefined) return []
  if (!Array.isArray(value)) {
    throw new GatewayRequestValidationError(
      'Gemini tools 必须是数组',
      'invalid_gemini_chat_bridge_tools'
    )
  }
  const output: JsonRecord[] = []
  for (const item of value) {
    const tool = objectValue(item)
    if (!tool) continue
    const unsupportedToolKeys = Object.keys(tool).filter((key) => key !== 'functionDeclarations' && tool[key] !== undefined)
    if (unsupportedToolKeys.length > 0) {
      throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Chat Completions 上游不支持 Gemini 原生工具：${unsupportedToolKeys.join('、')}。请客户端改用 functionDeclarations 或改用真实支持这些工具的 Gemini 原生上游。`, 'unsupported_gemini_native_tools')
    }
    const declarations = Array.isArray(tool.functionDeclarations) ? tool.functionDeclarations : []
    for (const declarationValue of declarations) {
      const declaration = objectValue(declarationValue)
      if (!declaration) continue
      const name = stringValue(declaration.name)
      if (!name) {
        throw new GatewayRequestValidationError(
          'Gemini functionDeclaration 缺少 name',
          'invalid_gemini_chat_bridge_function_declaration'
        )
      }
      output.push({
        type: 'function',
        function: {
          name,
          description: stringValue(declaration.description) ?? '',
          parameters: objectValue(declaration.parameters) ?? { type: 'object', properties: {} }
        }
      })
    }
  }
  return output
}

function geminiToolChoiceToChatToolChoice(
  req: Request,
  toolConfig: JsonRecord | undefined,
  model: string,
  providerName?: string
): JsonRecord | string | undefined {
  const functionCallingConfig = objectValue(toolConfig?.functionCallingConfig)
  if (!functionCallingConfig) return undefined
  const mode = (stringValue(functionCallingConfig.mode) ?? 'AUTO').toUpperCase()
  if (mode === 'NONE') return 'none'
  if (mode === 'AUTO') return 'auto'
  if (mode === 'ANY') {
    const allowed = Array.isArray(functionCallingConfig.allowedFunctionNames)
      ? functionCallingConfig.allowedFunctionNames.filter((item): item is string => typeof item === 'string' && item.trim().length > 0)
      : []
    return allowed.length === 1
      ? { type: 'function', function: { name: allowed[0] } }
      : 'required'
  }
  throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Chat Completions 上游不支持 Gemini functionCallingConfig.mode=${mode}。请改用 AUTO、ANY 或 NONE。`, 'unsupported_gemini_tool_choice')
}

async function * transformChatCompletionsJsonToGeminiGenerateContentJson(
  body: AsyncIterable<Uint8Array>,
  fallbackModel: string
): AsyncIterable<Uint8Array> {
  let parsed: unknown
  try {
    parsed = await readJsonBody(body)
  } catch (error) {
    yield Buffer.from(JSON.stringify(geminiChatBridgeResponseGuidanceJson(
      fallbackModel,
      '上游 Chat Completions 返回了无法解析为 Gemini GenerateContent 的响应。客户端可以保持当前对话并重试，或换用更稳定的上游模型；网关已转换为 Gemini 文本提示，避免客户端因协议错误中断。'
    )), 'utf8')
    return
  }
  const message = chatCompletionJsonToGeminiGenerateContent(parsed, fallbackModel)
  yield Buffer.from(JSON.stringify(message), 'utf8')
}

function chatCompletionJsonToGeminiGenerateContent(value: unknown, fallbackModel: string): JsonRecord {
  const root = objectValue(value) ?? {}
  const choices = Array.isArray(root.choices) ? root.choices : []
  const candidates = choices.map((item, index) => {
    const choice = objectValue(item) ?? {}
    const message = objectValue(choice.message) ?? {}
    const parts = chatMessageToGeminiParts(message)
    const candidate: JsonRecord = {
      content: {
        role: 'model',
        parts: parts.length > 0 ? parts : [{ text: emptyChatCompletionGeminiGuidanceText() }]
      },
      finishReason: chatFinishReasonToGeminiFinishReason(stringValue(choice.finish_reason), parts),
      index
    }
    return candidate
  })
  const output: JsonRecord = {
    candidates: candidates.length > 0 ? candidates : [{
      content: { role: 'model', parts: [{ text: emptyChatCompletionGeminiGuidanceText() }] },
      finishReason: 'STOP',
      index: 0
    }],
    modelVersion: stringValue(root.model) ?? fallbackModel
  }
  const usage = chatUsageToGeminiUsage(objectValue(root.usage))
  if (usage) output.usageMetadata = usage
  return output
}

function geminiChatBridgeResponseGuidanceJson(model: string, text: string): JsonRecord {
  return {
    candidates: [{
      content: {
        role: 'model',
        parts: [{ text }]
      },
      finishReason: 'STOP',
      index: 0
    }],
    modelVersion: model
  }
}

function emptyChatCompletionGeminiGuidanceText(): string {
  return '上游 Chat Completions 返回了空 assistant 内容。客户端可以保持当前对话并重试，或换用更稳定的上游模型；网关已将空响应转换为 Gemini 文本提示，避免客户端因空消息中断。'
}

function chatMessageToGeminiParts(message: JsonRecord): JsonRecord[] {
  const output: JsonRecord[] = []
  const text = chatMessageTextContent(message)
  if (text) output.push({ text })
  const toolCalls = Array.isArray(message.tool_calls) ? message.tool_calls : []
  for (const item of toolCalls) {
    const toolCall = objectValue(item)
    const fn = objectValue(toolCall?.function)
    const name = stringValue(fn?.name)
    if (!toolCall || !name) continue
    output.push({
      functionCall: {
        name,
        args: parseToolArguments(stringValue(fn?.arguments))
      }
    })
  }
  return output
}

async function * transformChatCompletionsSseToGeminiGenerateContentSse(
  upstreamBody: AsyncIterable<Uint8Array>,
  fallbackModel: string
): AsyncIterable<Uint8Array> {
  const state = createGeminiChatStreamState(fallbackModel)
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
    for (const output of completeGeminiStream(state)) {
      yield Buffer.from(output, 'utf8')
    }
  }
}

function createGeminiChatStreamState(model: string): GeminiChatStreamState {
  return {
    model,
    completed: false,
    failed: false,
    emittedContent: false,
    toolCalls: new Map()
  }
}

function processChatCompletionsSseEvent(state: GeminiChatStreamState, rawEventText: string): string[] {
  if (state.completed || state.failed) return []
  const event = parseOpenAISseEventText(rawEventText)
  if (event.dataText === '[DONE]') {
    return completeGeminiStream(state)
  }
  if (event.dataParseError) {
    return failGeminiStream(state, '上游 Chat Completions SSE 返回了无法解析的事件', 'upstream_stream_parse_error')
  }
  const data = event.data
  if (!data) return []
  if (event.eventName === 'error' || event.eventType === 'error' || objectValue(data.error)) {
    const error = objectValue(data.error) ?? data
    return failGeminiStream(
      state,
      stringValue(error.message) ?? '上游 Chat Completions SSE 返回错误事件',
      stringValue(error.code) ?? stringValue(error.type) ?? 'upstream_error',
      stringValue(error.type) ?? 'INTERNAL'
    )
  }
  state.model = stringValue(data.model) ?? state.model
  const usage = chatUsageToGeminiUsage(objectValue(data.usage))
  if (usage) state.usage = usage
  const output: string[] = []
  const choices = Array.isArray(data.choices) ? data.choices : []
  for (const item of choices) {
    const choice = objectValue(item)
    if (!choice) continue
    const delta = objectValue(choice.delta)
    if (delta) {
      const text = stringValue(delta.content) ?? stringValue(delta.reasoning_content) ?? stringValue(delta.refusal)
      if (text) {
        state.emittedContent = true
        output.push(geminiSse({
          candidates: [{
            content: {
              role: 'model',
              parts: [{ text }]
            }
          }],
          modelVersion: state.model
        }))
      }
      const toolCalls = Array.isArray(delta.tool_calls) ? delta.tool_calls : []
      for (const toolCallValue of toolCalls) {
        appendGeminiStreamToolCallDelta(state, objectValue(toolCallValue))
      }
    }
    const finishReason = stringValue(choice.finish_reason)
    if (finishReason) {
      state.finishReason = chatFinishReasonToGeminiFinishReason(finishReason)
      output.push(...emitCompletedGeminiToolCalls(state))
    }
  }
  return output
}

function appendGeminiStreamToolCallDelta(state: GeminiChatStreamState, toolCall: JsonRecord | undefined): void {
  if (!toolCall) return
  const key = integerValue(toolCall.index) ?? 0
  const fn = objectValue(toolCall.function)
  let current = state.toolCalls.get(key)
  if (!current) {
    current = {
      id: stringValue(toolCall.id) ?? `call_${key}`,
      name: '',
      arguments: ''
    }
    state.toolCalls.set(key, current)
  }
  current.id = stringValue(toolCall.id) ?? current.id
  current.name = stringValue(fn?.name) ?? current.name
  current.arguments += stringValue(fn?.arguments) ?? ''
}

function emitCompletedGeminiToolCalls(state: GeminiChatStreamState): string[] {
  const parts = [...state.toolCalls.values()]
    .filter((toolCall) => toolCall.name)
    .sort((left, right) => left.id.localeCompare(right.id))
    .map((toolCall) => ({
      functionCall: {
        name: toolCall.name,
        args: parseToolArguments(toolCall.arguments)
      }
    }))
  if (!parts.length) return []
  state.toolCalls.clear()
  state.emittedContent = true
  return [geminiSse({
    candidates: [{
      content: {
        role: 'model',
        parts
      }
    }],
    modelVersion: state.model
  })]
}

function completeGeminiStream(state: GeminiChatStreamState): string[] {
  if (state.completed || state.failed) return []
  state.completed = true
  const output = emitCompletedGeminiToolCalls(state)
  if (!state.emittedContent) {
    output.push(geminiSse({
      candidates: [{
        content: {
          role: 'model',
          parts: [{ text: emptyChatCompletionGeminiGuidanceText() }]
        }
      }],
      modelVersion: state.model
    }))
  }
  const finalEvent: JsonRecord = {
    candidates: [{
      finishReason: state.finishReason ?? 'STOP'
    }],
    modelVersion: state.model
  }
  if (state.usage) finalEvent.usageMetadata = state.usage
  output.push(geminiSse(finalEvent))
  return output
}

function failGeminiStream(state: GeminiChatStreamState, message: string, code: string, status = 'INTERNAL'): string[] {
  if (state.completed || state.failed) return []
  state.failed = true
  return [geminiSse({
    error: {
      message,
      status,
      code
    }
  }, 'error')]
}

function chatUserContentFromParts(parts: JsonRecord[]): string | JsonRecord[] {
  const hasNonText = parts.some((part) => part.type !== 'text')
  if (hasNonText) return parts
  return parts.map((part) => stringValue(part.text) ?? '').join('')
}

function chatMessageTextContent(message: JsonRecord): string {
  const content = message.content
  if (typeof content === 'string') return content
  if (Array.isArray(content)) {
    return content.map((item) => {
      const part = objectValue(item)
      if (!part) return ''
      if (part.type === 'text' || part.type === 'output_text') return stringValue(part.text) ?? ''
      return ''
    }).join('')
  }
  return stringValue(message.reasoning_content) ?? stringValue(message.refusal) ?? ''
}

function chatUsageToGeminiUsage(usage: JsonRecord | undefined): JsonRecord | undefined {
  if (!usage) return undefined
  const promptTokens = integerValue(usage.prompt_tokens) ?? integerValue(usage.input_tokens) ?? 0
  const completionTokens = integerValue(usage.completion_tokens) ?? integerValue(usage.output_tokens) ?? 0
  const totalTokens = integerValue(usage.total_tokens) ?? promptTokens + completionTokens
  const output: JsonRecord = {
    promptTokenCount: promptTokens,
    candidatesTokenCount: completionTokens,
    totalTokenCount: totalTokens
  }
  const cachedTokens = integerValue(objectValue(usage.prompt_tokens_details)?.cached_tokens)
    ?? integerValue(objectValue(usage.input_tokens_details)?.cached_tokens)
  if (cachedTokens !== undefined) output.cachedContentTokenCount = cachedTokens
  const reasoningTokens = integerValue(objectValue(usage.completion_tokens_details)?.reasoning_tokens)
    ?? integerValue(objectValue(usage.output_tokens_details)?.reasoning_tokens)
  if (reasoningTokens !== undefined) output.thoughtsTokenCount = reasoningTokens
  return output
}

function chatFinishReasonToGeminiFinishReason(finishReason: string | undefined, parts?: JsonRecord[]): string {
  if (finishReason === 'length') return 'MAX_TOKENS'
  if (finishReason === 'content_filter') return 'SAFETY'
  if (finishReason === 'tool_calls' || finishReason === 'function_call') return 'STOP'
  if (parts?.some((part) => part.functionCall !== undefined)) return 'STOP'
  return 'STOP'
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

function toolCallIdForName(state: GeminiRequestToolCallIdState, name: string): string {
  const existing = state.idsByName.get(name)
  if (existing) return existing
  const id = `call_${sanitizeToolCallIdName(name)}_${state.nextIndex++}`
  state.idsByName.set(name, id)
  return id
}

function sanitizeToolCallIdName(value: string): string {
  return value.replace(/[^a-zA-Z0-9_-]/g, '_').slice(0, 48) || 'tool'
}

function geminiStopSequencesToChatStop(value: unknown): string | string[] | undefined {
  if (!Array.isArray(value)) return undefined
  const stops = value.filter((item): item is string => typeof item === 'string' && item.length > 0)
  if (stops.length === 0) return undefined
  return stops.length === 1 ? stops[0] : stops
}

function unsupportedGeminiPart(
  req: Request,
  model: string,
  providerName: string | undefined,
  part: JsonRecord
): never {
  const kind = ['executableCode', 'codeExecutionResult', 'videoMetadata', 'thoughtSignature']
    .find((key) => part[key] !== undefined)
    ?? Object.keys(part).find((key) => key !== 'thought')
    ?? 'unknown'
  throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Chat Completions 上游不支持 Gemini part：${kind}。请客户端改用真实支持该 part 的 Gemini 原生上游，或在本地 agent 中先转换/执行后再发起请求。`, 'unsupported_gemini_content_part')
}

function geminiGenerateContentGuidance(req: Request, model: string, message: string, code: string): GatewayAgentGuidanceResponse {
  return new GatewayAgentGuidanceResponse({
    message,
    code,
    protocol: 'gemini',
    stream: isGeminiGenerateContentStreamRequest(req),
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
      throw new Error('gemini_chat_bridge_response_too_large')
    }
  }
  const text = Buffer.concat(chunks).toString('utf8')
  return text.trim() ? JSON.parse(text) as unknown : {}
}

function geminiChatBridgeResponseErrorJson(error: unknown): JsonRecord {
  const tooLarge = error instanceof Error && error.message === 'gemini_chat_bridge_response_too_large'
  return {
    error: {
      status: tooLarge ? 'RESOURCE_EXHAUSTED' : 'INTERNAL',
      code: tooLarge ? 'upstream_chat_completions_response_too_large' : 'upstream_chat_completions_invalid_json',
      message: tooLarge
        ? '上游 Chat Completions 响应过大，无法转换为 Gemini GenerateContent 响应'
        : '上游 Chat Completions 返回了无法转换为 Gemini GenerateContent 响应的 JSON'
    }
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

function geminiSse(payload: JsonRecord, event?: string): string {
  const prefix = event ? `event: ${event}\n` : ''
  return `${prefix}data: ${JSON.stringify(payload)}\n\n`
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
