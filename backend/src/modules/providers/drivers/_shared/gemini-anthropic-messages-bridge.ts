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
import { GatewayAgentGuidanceResponse, GatewayRequestValidationError } from '../../../gateway/request/validation-error.js'
import { parseOpenAISseEventText } from '../../../gateway/protocols/openai-v1/stream-events.js'
import type { GatewayUpstreamResponse } from '../../../gateway/upstream/request.js'
import {
  isGeminiGenerateContentPostRequest,
  isGeminiGenerateContentStreamRequest
} from './gemini-openai-chat-bridge.js'

type JsonRecord = Record<string, unknown>

interface BuildGeminiGenerateContentAnthropicMessagesBridgeBodyOptions {
  defaultMaxTokens?: number
  defaultModel: string
  guidanceProviderName?: string
  modelOverride?: string
}

interface TransformGeminiGenerateContentAnthropicMessagesBridgeResponseOptions {
  enabled: boolean
  model: string
}

interface GeminiAnthropicToolCallIdState {
  idsByName: Map<string, string>
  nextIndex: number
}

interface AnthropicGeminiStreamBlockState {
  index: number
  type: string
  id?: string
  name?: string
  inputJson: string
}

interface AnthropicGeminiStreamState {
  model: string
  completed: boolean
  failed: boolean
  finishReason?: string
  usage?: JsonRecord
  blocks: Map<number, AnthropicGeminiStreamBlockState>
}

const defaultAnthropicMaxTokens = 4096

export function geminiGenerateContentAnthropicMessagesBridgeRequiredEndpointMode(req: Request): AccountSupportedEndpointMode {
  return isGeminiGenerateContentStreamRequest(req) ? 'messages_sse' : 'messages_json'
}

export function prepareGeminiGenerateContentAnthropicMessagesBridgeHeaders(headers: Headers, req: Request): void {
  headers.set('accept', isGeminiGenerateContentStreamRequest(req) ? 'text/event-stream' : 'application/json')
  headers.set('content-type', 'application/json')
  headers.delete('content-length')
  headers.delete('x-goog-api-client')
  headers.delete('x-goog-api-key')
}

export async function buildGeminiGenerateContentAnthropicMessagesBridgeBody(
  req: Request,
  options: BuildGeminiGenerateContentAnthropicMessagesBridgeBodyOptions,
  signal?: AbortSignal
): Promise<Buffer> {
  const body = await parseGatewayJsonObject(req, signal)
  const model = options.modelOverride ?? options.defaultModel
  validateGeminiGenerateContentAnthropicMessagesBridgeBody(req, body, model, options.guidanceProviderName)
  const anthropicBody = geminiGenerateContentBodyToAnthropicMessagesBody(req, body, {
    defaultMaxTokens: options.defaultMaxTokens ?? defaultAnthropicMaxTokens,
    model,
    providerName: options.guidanceProviderName
  })
  return Buffer.from(JSON.stringify(anthropicBody), 'utf8')
}

export function transformGeminiGenerateContentAnthropicMessagesBridgeUpstreamResponse(
  req: Request,
  response: GatewayUpstreamResponse,
  options: TransformGeminiGenerateContentAnthropicMessagesBridgeResponseOptions
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
      body: transformAnthropicMessagesSseToGeminiGenerateContentSse(response.body, options.model)
    }
  }
  headers.set('content-type', 'application/json; charset=utf-8')
  return {
    status: response.status,
    ok: response.ok,
    headers,
    body: transformAnthropicMessagesJsonToGeminiGenerateContentJson(response.body, options.model)
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
      'Gemini GenerateContent 到 Anthropic Messages 桥接要求请求体是有效 JSON 对象',
      'invalid_gemini_messages_bridge_json_body'
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
      'Gemini GenerateContent 到 Anthropic Messages 桥接要求请求体是有效 JSON 对象',
      'invalid_gemini_messages_bridge_json_body'
    )
  }
  if (!isPlainObject(parsed)) {
    throw new GatewayRequestValidationError(
      'Gemini GenerateContent 到 Anthropic Messages 桥接要求请求体是 JSON 对象',
      'invalid_gemini_messages_bridge_json_body'
    )
  }
  return { ...parsed }
}

function validateGeminiGenerateContentAnthropicMessagesBridgeBody(
  req: Request,
  body: JsonRecord,
  model: string,
  providerName?: string
): void {
  if (!Array.isArray(body.contents)) {
    throw new GatewayRequestValidationError(
      'Gemini GenerateContent 到 Anthropic Messages 桥接要求 contents 是数组',
      'invalid_gemini_messages_bridge_contents'
    )
  }
  if (body.cachedContent !== undefined) {
    throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Anthropic Messages 上游不能保真承载 Gemini cachedContent。请客户端改用真实支持 cachedContent 的 Gemini 原生上游，或移除 cachedContent 后重试。`, 'unsupported_gemini_cached_content')
  }
  if (Array.isArray(body.safetySettings) && body.safetySettings.length > 0) {
    throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Anthropic Messages 上游不能保真承载 Gemini safetySettings。请客户端切换 Gemini 原生上游，或移除 safetySettings 后重试。`, 'unsupported_gemini_safety_settings')
  }
}

function geminiGenerateContentBodyToAnthropicMessagesBody(
  req: Request,
  body: JsonRecord,
  input: {
    defaultMaxTokens: number
    model: string
    providerName?: string
  }
): JsonRecord {
  const toolCallState: GeminiAnthropicToolCallIdState = {
    idsByName: new Map(),
    nextIndex: 0
  }
  const messages: JsonRecord[] = []
  for (const item of body.contents as unknown[]) {
    const content = objectValue(item)
    if (!content) continue
    appendGeminiContentAsAnthropicMessage(req, messages, content, toolCallState, input.model, input.providerName)
  }

  const output: JsonRecord = {
    model: input.model,
    max_tokens: input.defaultMaxTokens,
    messages,
    stream: isGeminiGenerateContentStreamRequest(req)
  }
  const system = geminiSystemInstructionToAnthropicSystem(req, body.systemInstruction, input.model, input.providerName)
  if (system) output.system = system
  applyGeminiGenerationConfig(req, output, objectValue(body.generationConfig), input.model, input.providerName)
  const tools = geminiToolsToAnthropicTools(req, body.tools, input.model, input.providerName)
  if (tools.length > 0) {
    output.tools = tools
    const toolChoice = geminiToolChoiceToAnthropicToolChoice(req, objectValue(body.toolConfig), input.model, input.providerName)
    if (toolChoice !== undefined) output.tool_choice = toolChoice
  }
  return output
}

function geminiSystemInstructionToAnthropicSystem(
  req: Request,
  value: unknown,
  model: string,
  providerName?: string
): string | undefined {
  if (value === undefined) return undefined
  if (typeof value === 'string') return value.trim() ? value : undefined
  const instruction = objectValue(value)
  if (!instruction) {
    throw new GatewayRequestValidationError(
      'Gemini systemInstruction 必须是字符串或 Content 对象',
      'invalid_gemini_messages_bridge_system_instruction'
    )
  }
  const parts = Array.isArray(instruction.parts) ? instruction.parts : []
  const text = parts.map((partValue) => {
    const part = objectValue(partValue)
    if (!part) return ''
    if (typeof part.text !== 'string') {
      throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Anthropic Messages 上游只支持把 Gemini systemInstruction text part 转换为 system。请移除 systemInstruction 中的非 text part 后重试。`, 'unsupported_gemini_system_instruction_part')
    }
    return part.text
  }).filter(Boolean).join('\n')
  return text || undefined
}

function appendGeminiContentAsAnthropicMessage(
  req: Request,
  output: JsonRecord[],
  content: JsonRecord,
  toolCallState: GeminiAnthropicToolCallIdState,
  model: string,
  providerName?: string
): void {
  const role = stringValue(content.role) ?? 'user'
  const parts = Array.isArray(content.parts) ? content.parts : []
  if (parts.length === 0) return
  if (role === 'model') {
    appendAnthropicMessage(output, {
      role: 'assistant',
      content: geminiModelPartsToAnthropicBlocks(req, parts, toolCallState, model, providerName)
    })
    return
  }
  if (role !== 'user' && role !== 'function') {
    throw new GatewayRequestValidationError(
      'Gemini GenerateContent 到 Anthropic Messages 桥接只支持 user、model 和 function 角色',
      'invalid_gemini_messages_bridge_role'
    )
  }
  appendAnthropicMessage(output, {
    role: 'user',
    content: geminiUserPartsToAnthropicBlocks(req, parts, toolCallState, model, providerName)
  })
}

function appendAnthropicMessage(output: JsonRecord[], next: JsonRecord): void {
  const nextRole = next.role
  const nextContent = Array.isArray(next.content) ? next.content : []
  if (!nextContent.length) return
  const previous = output[output.length - 1]
  if (previous?.role === nextRole && Array.isArray(previous.content)) {
    previous.content.push(...nextContent)
    return
  }
  output.push(next)
}

function geminiModelPartsToAnthropicBlocks(
  req: Request,
  parts: unknown[],
  toolCallState: GeminiAnthropicToolCallIdState,
  model: string,
  providerName?: string
): JsonRecord[] {
  const output: JsonRecord[] = []
  for (const partValue of parts) {
    const part = objectValue(partValue)
    if (!part) continue
    if (typeof part.text === 'string') {
      output.push({ type: 'text', text: part.text })
      continue
    }
    const call = objectValue(part.functionCall)
    if (call) {
      const name = stringValue(call.name)
      if (!name) {
        throw new GatewayRequestValidationError(
          'Gemini functionCall 缺少 name',
          'invalid_gemini_messages_bridge_function_call'
        )
      }
      output.push({
        type: 'tool_use',
        id: toolUseIdForName(toolCallState, name),
        name,
        input: objectValue(call.args) ?? {}
      })
      continue
    }
    throw unsupportedGeminiPart(req, model, providerName, part)
  }
  return output
}

function geminiUserPartsToAnthropicBlocks(
  req: Request,
  parts: unknown[],
  toolCallState: GeminiAnthropicToolCallIdState,
  model: string,
  providerName?: string
): JsonRecord[] {
  const output: JsonRecord[] = []
  for (const partValue of parts) {
    const part = objectValue(partValue)
    if (!part) continue
    appendGeminiPartToAnthropicUserBlocks(req, output, part, toolCallState, model, providerName)
  }
  return output
}

function appendGeminiPartToAnthropicUserBlocks(
  req: Request,
  output: JsonRecord[],
  part: JsonRecord,
  toolCallState: GeminiAnthropicToolCallIdState,
  model: string,
  providerName?: string
): void {
  if (typeof part.text === 'string') {
    output.push({ type: 'text', text: part.text })
    return
  }
  const inlineData = objectValue(part.inlineData) ?? objectValue(part.inline_data)
  if (inlineData) {
    output.push(geminiInlineDataToAnthropicImageBlock(req, inlineData, model, providerName))
    return
  }
  const fileData = objectValue(part.fileData) ?? objectValue(part.file_data)
  if (fileData) {
    output.push(geminiFileDataToAnthropicImageBlock(req, fileData, model, providerName))
    return
  }
  const response = objectValue(part.functionResponse)
  if (response) {
    const name = stringValue(response.name)
    if (!name) {
      throw new GatewayRequestValidationError(
        'Gemini functionResponse 缺少 name',
        'invalid_gemini_messages_bridge_function_response'
      )
    }
    output.push({
      type: 'tool_result',
      tool_use_id: toolUseIdForName(toolCallState, name),
      content: JSON.stringify(response.response ?? {})
    })
    return
  }
  if (part.functionCall !== undefined) {
    throw new GatewayRequestValidationError(
      'Gemini functionCall 只能出现在 model 响应消息中',
      'invalid_gemini_messages_bridge_function_part'
    )
  }
  throw unsupportedGeminiPart(req, model, providerName, part)
}

function geminiInlineDataToAnthropicImageBlock(
  req: Request,
  inlineData: JsonRecord,
  model: string,
  providerName?: string
): JsonRecord {
  const mediaType = (stringValue(inlineData.mimeType) ?? stringValue(inlineData.mime_type) ?? 'application/octet-stream').toLowerCase()
  const data = stringValue(inlineData.data)
  if (!data) {
    throw new GatewayRequestValidationError(
      'Gemini inlineData 缺少 data',
      'invalid_gemini_messages_bridge_inline_data'
    )
  }
  if (!isAnthropicSupportedImageMediaType(mediaType)) {
    throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Anthropic Messages 上游只支持把 Gemini inlineData 图片转换为 image block。请移除非图片 inlineData，或改用真实支持该输入的 Gemini 原生上游。`, 'unsupported_gemini_inline_data')
  }
  if (!normalizedBase64Data(data)) {
    throw new GatewayRequestValidationError(
      'Gemini inlineData.data 必须是合法 base64',
      'invalid_gemini_messages_bridge_inline_data'
    )
  }
  return {
    type: 'image',
    source: {
      type: 'base64',
      media_type: mediaType,
      data
    }
  }
}

function geminiFileDataToAnthropicImageBlock(
  req: Request,
  fileData: JsonRecord,
  model: string,
  providerName?: string
): JsonRecord {
  const mimeType = (stringValue(fileData.mimeType) ?? stringValue(fileData.mime_type) ?? '').toLowerCase()
  const fileUri = stringValue(fileData.fileUri) ?? stringValue(fileData.file_uri)
  if (!fileUri) {
    throw new GatewayRequestValidationError(
      'Gemini fileData 缺少 fileUri',
      'invalid_gemini_messages_bridge_file_data'
    )
  }
  if (!isAnthropicSupportedImageMediaType(mimeType) || !/^https?:\/\//i.test(fileUri)) {
    throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Anthropic Messages 上游只支持可公开访问的图片 fileData。请改用 https 图片 URL、inlineData 图片，或选择 Gemini 原生上游。`, 'unsupported_gemini_file_data')
  }
  return {
    type: 'image',
    source: {
      type: 'url',
      url: fileUri
    }
  }
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
  if (typeof generationConfig.topK === 'number') output.top_k = generationConfig.topK
  const maxTokens = integerValue(generationConfig.maxOutputTokens)
  if (maxTokens !== undefined) output.max_tokens = maxTokens
  const stop = geminiStopSequencesToAnthropicStopSequences(generationConfig.stopSequences)
  if (stop !== undefined) output.stop_sequences = stop
  const candidateCount = integerValue(generationConfig.candidateCount)
  if (candidateCount !== undefined && candidateCount > 1) {
    throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Anthropic Messages 上游不支持 Gemini candidateCount=${candidateCount}。请把 candidateCount 调整为 1 或选择 Gemini 原生上游。`, 'unsupported_gemini_candidate_count')
  }
  const responseMimeType = stringValue(generationConfig.responseMimeType)
  if (responseMimeType && responseMimeType !== 'text/plain') {
    throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Anthropic Messages 上游不支持保真承载 Gemini responseMimeType=${responseMimeType}。请改用 text/plain，或选择支持结构化输出的上游协议。`, 'unsupported_gemini_response_mime_type')
  }
  if (generationConfig.responseSchema !== undefined) {
    throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Anthropic Messages 上游不支持保真承载 Gemini responseSchema。请移除 responseSchema，或由客户端 agent 改用支持结构化输出的协议。`, 'unsupported_gemini_response_schema')
  }
}

function geminiToolsToAnthropicTools(
  req: Request,
  value: unknown,
  model: string,
  providerName?: string
): JsonRecord[] {
  if (value === undefined) return []
  if (!Array.isArray(value)) {
    throw new GatewayRequestValidationError(
      'Gemini tools 必须是数组',
      'invalid_gemini_messages_bridge_tools'
    )
  }
  const output: JsonRecord[] = []
  for (const item of value) {
    const tool = objectValue(item)
    if (!tool) continue
    const unsupportedToolKeys = Object.keys(tool).filter((key) => key !== 'functionDeclarations' && tool[key] !== undefined)
    if (unsupportedToolKeys.length > 0) {
      throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Anthropic Messages 上游不支持 Gemini 原生工具：${unsupportedToolKeys.join('、')}。请客户端改用 functionDeclarations 或改用真实支持这些工具的 Gemini 原生上游。`, 'unsupported_gemini_native_tools')
    }
    const declarations = Array.isArray(tool.functionDeclarations) ? tool.functionDeclarations : []
    for (const declarationValue of declarations) {
      const declaration = objectValue(declarationValue)
      if (!declaration) continue
      const name = stringValue(declaration.name)
      if (!name) {
        throw new GatewayRequestValidationError(
          'Gemini functionDeclaration 缺少 name',
          'invalid_gemini_messages_bridge_function_declaration'
        )
      }
      output.push({
        name,
        description: stringValue(declaration.description) ?? '',
        input_schema: objectValue(declaration.parameters) ?? { type: 'object', properties: {} }
      })
    }
  }
  return output
}

function geminiToolChoiceToAnthropicToolChoice(
  req: Request,
  toolConfig: JsonRecord | undefined,
  model: string,
  providerName?: string
): JsonRecord | undefined {
  const functionCallingConfig = objectValue(toolConfig?.functionCallingConfig)
  if (!functionCallingConfig) return undefined
  const mode = (stringValue(functionCallingConfig.mode) ?? 'AUTO').toUpperCase()
  if (mode === 'NONE') return { type: 'none' }
  if (mode === 'AUTO') return { type: 'auto' }
  if (mode === 'ANY') {
    const allowed = Array.isArray(functionCallingConfig.allowedFunctionNames)
      ? functionCallingConfig.allowedFunctionNames.filter((item): item is string => typeof item === 'string' && item.trim().length > 0)
      : []
    return allowed.length === 1
      ? { type: 'tool', name: allowed[0] }
      : { type: 'any' }
  }
  throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Anthropic Messages 上游不支持 Gemini functionCallingConfig.mode=${mode}。请改用 AUTO、ANY 或 NONE。`, 'unsupported_gemini_tool_choice')
}

async function * transformAnthropicMessagesJsonToGeminiGenerateContentJson(
  body: AsyncIterable<Uint8Array>,
  fallbackModel: string
): AsyncIterable<Uint8Array> {
  let parsed: unknown
  try {
    parsed = await readJsonBody(body)
  } catch (error) {
    yield Buffer.from(JSON.stringify(geminiAnthropicBridgeResponseErrorJson(error)), 'utf8')
    return
  }
  const message = anthropicMessageJsonToGeminiGenerateContent(parsed, fallbackModel)
  yield Buffer.from(JSON.stringify(message), 'utf8')
}

function anthropicMessageJsonToGeminiGenerateContent(value: unknown, fallbackModel: string): JsonRecord {
  const root = objectValue(value) ?? {}
  const parts = anthropicContentBlocksToGeminiParts(Array.isArray(root.content) ? root.content : [])
  const output: JsonRecord = {
    candidates: [{
      content: {
        role: 'model',
        parts: parts.length > 0 ? parts : [{ text: '' }]
      },
      finishReason: anthropicStopReasonToGeminiFinishReason(stringValue(root.stop_reason), parts),
      index: 0
    }],
    modelVersion: stringValue(root.model) ?? fallbackModel
  }
  const usage = anthropicUsageToGeminiUsage(objectValue(root.usage))
  if (usage) output.usageMetadata = usage
  return output
}

function anthropicContentBlocksToGeminiParts(blocks: unknown[]): JsonRecord[] {
  const output: JsonRecord[] = []
  for (const blockValue of blocks) {
    const block = objectValue(blockValue)
    if (!block) continue
    if (block.type === 'text') {
      const text = stringValue(block.text)
      if (text) output.push({ text })
      continue
    }
    if (block.type === 'tool_use') {
      const name = stringValue(block.name)
      if (!name) continue
      output.push({
        functionCall: {
          name,
          args: objectValue(block.input) ?? {}
        }
      })
    }
  }
  return output
}

async function * transformAnthropicMessagesSseToGeminiGenerateContentSse(
  upstreamBody: AsyncIterable<Uint8Array>,
  fallbackModel: string
): AsyncIterable<Uint8Array> {
  const state = createAnthropicGeminiStreamState(fallbackModel)
  const decoder = new StringDecoder('utf8')
  let pending = ''
  for await (const chunk of upstreamBody) {
    pending += decoder.write(Buffer.from(chunk))
    const events = takeCompleteSseEvents(pending)
    pending = events.rest
    for (const eventText of events.events) {
      for (const output of processAnthropicSseEventAsGemini(state, eventText)) {
        yield Buffer.from(output, 'utf8')
      }
    }
  }
  pending += decoder.end()
  if (pending.trim()) {
    for (const output of processAnthropicSseEventAsGemini(state, pending)) {
      yield Buffer.from(output, 'utf8')
    }
  }
  if (!state.completed && !state.failed) {
    for (const output of completeGeminiStream(state)) {
      yield Buffer.from(output, 'utf8')
    }
  }
}

function createAnthropicGeminiStreamState(model: string): AnthropicGeminiStreamState {
  return {
    model,
    completed: false,
    failed: false,
    blocks: new Map()
  }
}

function processAnthropicSseEventAsGemini(state: AnthropicGeminiStreamState, rawEventText: string): string[] {
  if (state.completed || state.failed) return []
  const event = parseOpenAISseEventText(rawEventText)
  if (event.dataParseError) {
    return failGeminiStream(state, '上游 Anthropic Messages SSE 返回了无法解析的事件', 'upstream_stream_parse_error')
  }
  const data = event.data
  if (!data) return []
  if (event.eventName === 'error' || event.eventType === 'error' || data.type === 'error' || objectValue(data.error)) {
    const error = objectValue(data.error) ?? data
    return failGeminiStream(
      state,
      stringValue(error.message) ?? '上游 Anthropic Messages SSE 返回错误事件',
      stringValue(error.code) ?? stringValue(error.type) ?? 'upstream_error',
      stringValue(error.type) ?? 'INTERNAL'
    )
  }
  if (data.type === 'message_start') {
    const message = objectValue(data.message)
    state.model = stringValue(message?.model) ?? state.model
    const usage = anthropicUsageToGeminiUsage(objectValue(message?.usage))
    if (usage) state.usage = usage
    return []
  }
  if (data.type === 'content_block_start') {
    const index = integerValue(data.index) ?? 0
    const block = objectValue(data.content_block) ?? {}
    state.blocks.set(index, {
      index,
      type: stringValue(block.type) ?? 'unknown',
      id: stringValue(block.id),
      name: stringValue(block.name),
      inputJson: ''
    })
    return []
  }
  if (data.type === 'content_block_delta') {
    const index = integerValue(data.index) ?? 0
    const block = state.blocks.get(index)
    const delta = objectValue(data.delta) ?? {}
    if (delta.type === 'text_delta') {
      const text = stringValue(delta.text)
      return text ? [geminiSse({
        candidates: [{
          content: {
            role: 'model',
            parts: [{ text }]
          }
        }],
        modelVersion: state.model
      })] : []
    }
    if (delta.type === 'input_json_delta') {
      if (block) block.inputJson += stringValue(delta.partial_json) ?? ''
      return []
    }
    return []
  }
  if (data.type === 'content_block_stop') {
    const index = integerValue(data.index) ?? 0
    const block = state.blocks.get(index)
    if (!block || block.type !== 'tool_use' || !block.name) return []
    return [geminiSse({
      candidates: [{
        content: {
          role: 'model',
          parts: [{
            functionCall: {
              name: block.name,
              args: parseToolInput(block.inputJson)
            }
          }]
        }
      }],
      modelVersion: state.model
    })]
  }
  if (data.type === 'message_delta') {
    const delta = objectValue(data.delta)
    state.finishReason = anthropicStopReasonToGeminiFinishReason(stringValue(delta?.stop_reason))
    const usage = anthropicUsageToGeminiUsage(objectValue(data.usage), state.usage)
    if (usage) state.usage = usage
    return []
  }
  if (data.type === 'message_stop') {
    return completeGeminiStream(state)
  }
  return []
}

function completeGeminiStream(state: AnthropicGeminiStreamState): string[] {
  if (state.completed || state.failed) return []
  state.completed = true
  const finalEvent: JsonRecord = {
    candidates: [{
      finishReason: state.finishReason ?? 'STOP'
    }],
    modelVersion: state.model
  }
  if (state.usage) finalEvent.usageMetadata = state.usage
  return [geminiSse(finalEvent)]
}

function failGeminiStream(state: AnthropicGeminiStreamState, message: string, code: string, status = 'INTERNAL'): string[] {
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

function anthropicUsageToGeminiUsage(usage: JsonRecord | undefined, previous?: JsonRecord): JsonRecord | undefined {
  if (!usage && !previous) return undefined
  const promptTokens = anthropicInputTokens(usage) || integerValue(previous?.promptTokenCount) || 0
  const completionTokens = integerValue(usage?.output_tokens) ?? integerValue(previous?.candidatesTokenCount) ?? 0
  const output: JsonRecord = {
    promptTokenCount: promptTokens,
    candidatesTokenCount: completionTokens,
    totalTokenCount: promptTokens + completionTokens
  }
  const cachedTokens = integerValue(usage?.cache_read_input_tokens) ?? integerValue(previous?.cachedContentTokenCount)
  if (cachedTokens !== undefined) output.cachedContentTokenCount = cachedTokens
  const thinkingTokens = integerValue(objectValue(usage?.output_tokens_details)?.thinking_tokens) ?? integerValue(usage?.thinking_tokens)
  if (thinkingTokens !== undefined) output.thoughtsTokenCount = thinkingTokens
  return output
}

function anthropicInputTokens(usage: JsonRecord | undefined): number {
  if (!usage) return 0
  return (integerValue(usage.input_tokens) ?? 0)
    + (integerValue(usage.cache_creation_input_tokens) ?? 0)
    + (integerValue(usage.cache_read_input_tokens) ?? 0)
}

function anthropicStopReasonToGeminiFinishReason(reason: string | undefined, parts?: JsonRecord[]): string {
  if (reason === 'max_tokens') return 'MAX_TOKENS'
  if (reason === 'refusal') return 'SAFETY'
  if (reason === 'tool_use' || parts?.some((part) => part.functionCall !== undefined)) return 'STOP'
  return 'STOP'
}

function parseToolInput(value: string | undefined): JsonRecord {
  if (!value) return {}
  try {
    const parsed = JSON.parse(value) as unknown
    return isPlainObject(parsed) ? parsed : { value: parsed }
  } catch {
    return { _raw: value }
  }
}

function toolUseIdForName(state: GeminiAnthropicToolCallIdState, name: string): string {
  const existing = state.idsByName.get(name)
  if (existing) return existing
  const id = `toolu_${sanitizeToolUseIdName(name)}_${state.nextIndex++}`
  state.idsByName.set(name, id)
  return id
}

function sanitizeToolUseIdName(value: string): string {
  return value.replace(/[^a-zA-Z0-9_-]/g, '_').slice(0, 48) || 'tool'
}

function geminiStopSequencesToAnthropicStopSequences(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) return undefined
  const stops = value.filter((item): item is string => typeof item === 'string' && item.length > 0).slice(0, 4)
  return stops.length ? stops : undefined
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
  throw geminiGenerateContentGuidance(req, model, `当前${providerLabel(providerName)} Anthropic Messages 上游不支持 Gemini part：${kind}。请客户端改用真实支持该 part 的 Gemini 原生上游，或在本地 agent 中先转换/执行后再发起请求。`, 'unsupported_gemini_content_part')
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
      throw new Error('gemini_messages_bridge_response_too_large')
    }
  }
  const text = Buffer.concat(chunks).toString('utf8')
  return text.trim() ? JSON.parse(text) as unknown : {}
}

function geminiAnthropicBridgeResponseErrorJson(error: unknown): JsonRecord {
  const tooLarge = error instanceof Error && error.message === 'gemini_messages_bridge_response_too_large'
  return {
    error: {
      status: tooLarge ? 'RESOURCE_EXHAUSTED' : 'INTERNAL',
      code: tooLarge ? 'upstream_anthropic_messages_response_too_large' : 'upstream_anthropic_messages_invalid_json',
      message: tooLarge
        ? '上游 Anthropic Messages 响应过大，无法转换为 Gemini GenerateContent 响应'
        : '上游 Anthropic Messages 返回了无法转换为 Gemini GenerateContent 响应的 JSON'
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

function isAnthropicSupportedImageMediaType(mediaType: string | undefined): boolean {
  return mediaType === 'image/jpeg'
    || mediaType === 'image/png'
    || mediaType === 'image/gif'
    || mediaType === 'image/webp'
}

function normalizedBase64Data(value: string): string | undefined {
  const compact = value.replace(/\s+/g, '').replace(/-/g, '+').replace(/_/g, '/')
  if (!compact || !/^[A-Za-z0-9+/]*={0,2}$/.test(compact)) return undefined
  const unpadded = compact.replace(/=+$/, '')
  const paddingLength = (4 - (unpadded.length % 4)) % 4
  const normalized = `${unpadded}${'='.repeat(paddingLength)}`
  return normalized.length % 4 === 0 ? normalized : undefined
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
