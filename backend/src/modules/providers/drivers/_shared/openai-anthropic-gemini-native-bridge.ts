import { StringDecoder } from 'node:string_decoder'
import type { Request } from 'express'

import type { AccountSupportedEndpointMode } from '../../../../domain/types.js'
import {
  ANTHROPIC_MESSAGES_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_RESPONSES_FAMILY
} from '../../../../domain/provider-protocol.js'
import type { ResolvedOpenAIModelMapping } from '../../../gateway/protocols/openai-v1/model-mapping.js'
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
import type { GatewayUpstreamResponse } from '../../../gateway/upstream/request.js'

type JsonRecord = Record<string, unknown>

interface BuildGeminiNativeTargetBridgeBodyOptions {
  mapping: ResolvedOpenAIModelMapping
  providerName?: string
}

interface TransformGeminiNativeTargetBridgeResponseOptions {
  mapping: ResolvedOpenAIModelMapping
}

interface GeminiFunctionCall {
  id: string
  name: string
  args: JsonRecord
}

interface GeminiResponseSummary {
  text: string
  functionCalls: GeminiFunctionCall[]
  finishReason?: string
  usage?: JsonRecord
}

export function openAIOrAnthropicToGeminiNativeRequiredEndpointMode(req: Request): AccountSupportedEndpointMode {
  return requestStream(req) ? 'generate_content_sse' : 'generate_content_json'
}

export function prepareOpenAIOrAnthropicToGeminiNativeHeaders(headers: Headers, req: Request): void {
  headers.set('accept', requestStream(req) ? 'text/event-stream' : 'application/json')
  headers.set('content-type', 'application/json')
  headers.delete('content-length')
  headers.delete('authorization')
  headers.delete('anthropic-version')
  headers.delete('anthropic-beta')
  headers.delete('openai-beta')
}

export async function buildOpenAIOrAnthropicToGeminiNativeBody(
  req: Request,
  options: BuildGeminiNativeTargetBridgeBodyOptions,
  signal?: AbortSignal
): Promise<Buffer> {
  const body = await parseGatewayJsonObject(req, signal)
  const mapping = options.mapping
  validateCommonUnsupportedFields(req, body, mapping, options.providerName)
  const geminiBody = sourceBodyToGeminiGenerateContentBody(req, body, mapping, options.providerName)
  return Buffer.from(JSON.stringify(geminiBody), 'utf8')
}

export function transformGeminiNativeTargetBridgeUpstreamResponse(
  req: Request,
  response: GatewayUpstreamResponse,
  options: TransformGeminiNativeTargetBridgeResponseOptions
): GatewayUpstreamResponse {
  if (!response.ok || !response.body) {
    return response
  }
  const headers = new Headers(response.headers)
  headers.delete('content-length')
  const protocol = downstreamProtocol(options.mapping)
  if (requestStream(req)) {
    headers.set('content-type', 'text/event-stream; charset=utf-8')
    return {
      status: response.status,
      ok: response.ok,
      headers,
      body: transformGeminiSseToDownstreamSse(response.body, protocol, options.mapping.sourceModel)
    }
  }
  headers.set('content-type', 'application/json; charset=utf-8')
  return {
    status: response.status,
    ok: response.ok,
    headers,
    body: transformGeminiJsonToDownstreamJson(response.body, protocol, options.mapping.sourceModel)
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
      'Gemini native 目标桥接要求请求体是有效 JSON 对象',
      'invalid_gemini_target_bridge_json_body'
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
      'Gemini native 目标桥接要求请求体是有效 JSON 对象',
      'invalid_gemini_target_bridge_json_body'
    )
  }
  if (!isPlainObject(parsed)) {
    throw new GatewayRequestValidationError(
      'Gemini native 目标桥接要求请求体是 JSON 对象',
      'invalid_gemini_target_bridge_json_body'
    )
  }
  return { ...parsed }
}

function validateCommonUnsupportedFields(
  req: Request,
  body: JsonRecord,
  mapping: ResolvedOpenAIModelMapping,
  providerName?: string
): void {
  if (mapping.sourceEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY || mapping.sourceEndpointFamily === OPENAI_RESPONSES_FAMILY) {
    const protectedFields = ['service_tier', 'reasoning', 'reasoning_effort', 'thinking']
      .filter((field) => body[field] !== undefined && body[field] !== null)
    if (protectedFields.length > 0) {
      throw guidance(
        req,
        mapping,
        `Gemini native 上游不能保真映射 OpenAI 的 ${protectedFields.join('、')} 字段。请移除这些请求控制，或改用支持对应字段的原生上游。`,
        'unsupported_openai_request_controls_for_gemini_native'
      )
    }
  }
  if (mapping.sourceEndpointFamily === OPENAI_RESPONSES_FAMILY) {
    if (body.previous_response_id !== undefined || body.context_management !== undefined) {
      throw guidance(req, mapping, 'Gemini native 上游不能保真承载 OpenAI Responses 的 previous_response_id / context_management 状态链。请改用真实 Responses 上游，或移除状态链字段后重试。', 'unsupported_responses_state_for_gemini_native')
    }
    if (body.truncation !== undefined && body.truncation !== 'disabled' && body.truncation !== 'auto') {
      throw guidance(req, mapping, 'Gemini native 上游不能保真承载当前 Responses truncation 设置。请改用真实 Responses 上游，或移除该字段后重试。', 'unsupported_responses_truncation_for_gemini_native')
    }
  }
  if (mapping.sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY && (body.thinking !== undefined || body.cache_control !== undefined)) {
    throw guidance(req, mapping, `当前${providerLabel(providerName)} Gemini native 上游不能保真承载 Anthropic thinking / cache_control。请改用真实 Anthropic Messages 上游，或移除该能力后重试。`, 'unsupported_anthropic_state_for_gemini_native')
  }
}

function sourceBodyToGeminiGenerateContentBody(
  req: Request,
  body: JsonRecord,
  mapping: ResolvedOpenAIModelMapping,
  providerName?: string
): JsonRecord {
  if (mapping.sourceEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY) {
    return chatCompletionsBodyToGemini(req, body, mapping, providerName)
  }
  if (mapping.sourceEndpointFamily === OPENAI_RESPONSES_FAMILY) {
    return responsesBodyToGemini(req, body, mapping, providerName)
  }
  if (mapping.sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
    return anthropicMessagesBodyToGemini(req, body, mapping, providerName)
  }
  throw new GatewayRequestValidationError(
    '当前下游协议不能桥接到 Gemini native GenerateContent',
    'unsupported_gemini_target_bridge_source'
  )
}

function chatCompletionsBodyToGemini(
  req: Request,
  body: JsonRecord,
  mapping: ResolvedOpenAIModelMapping,
  providerName?: string
): JsonRecord {
  const messages = Array.isArray(body.messages) ? body.messages : undefined
  if (!messages) {
    throw new GatewayRequestValidationError('Chat Completions 到 Gemini native 桥接要求 messages 是数组', 'invalid_chat_gemini_bridge_messages')
  }
  const systemTexts: string[] = []
  const contents: JsonRecord[] = []
  for (const value of messages) {
    const message = objectValue(value)
    if (!message) continue
    const role = stringValue(message.role) ?? 'user'
    if (role === 'system' || role === 'developer') {
      systemTexts.push(...textPartsFromOpenAIContent(req, message.content, mapping, providerName))
      continue
    }
    if (role === 'assistant') {
      const parts = [
        ...openAIContentToGeminiParts(req, message.content, mapping, providerName),
        ...chatToolCallsToGeminiParts(message.tool_calls)
      ]
      if (parts.length > 0) contents.push({ role: 'model', parts })
      continue
    }
    if (role === 'tool') {
      const name = stringValue(message.name) ?? stringValue(message.tool_call_id) ?? 'tool_result'
      contents.push({ role: 'user', parts: [geminiFunctionResponsePart(name, message.content)] })
      continue
    }
    const parts = openAIContentToGeminiParts(req, message.content, mapping, providerName)
    if (parts.length > 0) contents.push({ role: 'user', parts })
  }
  return finalizeGeminiBody(req, body, mapping, contents, systemTexts.join('\n'), chatToolsToGeminiTools(req, body.tools, mapping, providerName), providerName)
}

function responsesBodyToGemini(
  req: Request,
  body: JsonRecord,
  mapping: ResolvedOpenAIModelMapping,
  providerName?: string
): JsonRecord {
  const systemTexts: string[] = []
  if (typeof body.instructions === 'string' && body.instructions.trim()) {
    systemTexts.push(body.instructions)
  }
  const contents: JsonRecord[] = []
  const input = body.input
  if (typeof input === 'string') {
    contents.push({ role: 'user', parts: [{ text: input }] })
  } else if (Array.isArray(input)) {
    for (const value of input) {
      const item = objectValue(value)
      if (!item) continue
      appendResponsesInputItem(req, contents, item, mapping, providerName)
    }
  } else {
    throw new GatewayRequestValidationError('Responses 到 Gemini native 桥接要求 input 是字符串或数组', 'invalid_responses_gemini_bridge_input')
  }
  return finalizeGeminiBody(req, body, mapping, contents, systemTexts.join('\n'), responsesToolsToGeminiTools(req, body.tools, mapping, providerName), providerName)
}

function anthropicMessagesBodyToGemini(
  req: Request,
  body: JsonRecord,
  mapping: ResolvedOpenAIModelMapping,
  providerName?: string
): JsonRecord {
  const messages = Array.isArray(body.messages) ? body.messages : undefined
  if (!messages) {
    throw new GatewayRequestValidationError('Anthropic Messages 到 Gemini native 桥接要求 messages 是数组', 'invalid_messages_gemini_bridge_messages')
  }
  const contents: JsonRecord[] = []
  for (const value of messages) {
    const message = objectValue(value)
    if (!message) continue
    const role = stringValue(message.role) === 'assistant' ? 'model' : 'user'
    const parts = anthropicContentToGeminiParts(req, message.content, mapping, providerName)
    if (parts.length > 0) contents.push({ role, parts })
  }
  const system = anthropicSystemToText(req, body.system, mapping, providerName)
  return finalizeGeminiBody(req, body, mapping, contents, system, anthropicToolsToGeminiTools(req, body.tools, mapping, providerName), providerName)
}

function finalizeGeminiBody(
  req: Request,
  body: JsonRecord,
  mapping: ResolvedOpenAIModelMapping,
  contents: JsonRecord[],
  systemText: string | undefined,
  tools: JsonRecord[],
  providerName?: string
): JsonRecord {
  if (contents.length === 0) {
    throw new GatewayRequestValidationError('Gemini native 目标桥接至少需要一条可发送内容', 'empty_gemini_target_bridge_contents')
  }
  const output: JsonRecord = {
    contents,
    generationConfig: generationConfigFromSource(req, body, mapping, providerName)
  }
  if (mapping.upstreamModel) {
    output.model = mapping.upstreamModel
  }
  if (systemText?.trim()) {
    output.systemInstruction = {
      role: 'user',
      parts: [{ text: systemText.trim() }]
    }
  }
  if (tools.length > 0) {
    output.tools = [{ functionDeclarations: tools }]
    const toolConfig = geminiToolConfigFromSource(body)
    if (toolConfig) output.toolConfig = toolConfig
  }
  const serviceTier = geminiServiceTierFromSource(body)
  if (serviceTier) output.service_tier = serviceTier
  return output
}

function appendResponsesInputItem(
  req: Request,
  output: JsonRecord[],
  item: JsonRecord,
  mapping: ResolvedOpenAIModelMapping,
  providerName?: string
): void {
  const type = stringValue(item.type)
  if (type === 'message' || item.role !== undefined) {
    const role = stringValue(item.role) === 'assistant' ? 'model' : 'user'
    const parts = openAIContentToGeminiParts(req, item.content, mapping, providerName)
    if (parts.length > 0) output.push({ role, parts })
    return
  }
  if (type === 'function_call') {
    const name = stringValue(item.name) ?? 'function_call'
    output.push({ role: 'model', parts: [{ functionCall: { name, args: jsonObjectFromUnknown(item.arguments) } }] })
    return
  }
  if (type === 'function_call_output') {
    const name = stringValue(item.name) ?? stringValue(item.call_id) ?? 'function_result'
    output.push({ role: 'user', parts: [geminiFunctionResponsePart(name, item.output)] })
    return
  }
  if (type === 'reasoning') {
    throw guidance(req, mapping, `当前${providerLabel(providerName)} Gemini native 上游不能保真承载 Responses reasoning item。请改用真实 Responses 上游，或移除 reasoning item 后重试。`, 'unsupported_responses_reasoning_for_gemini_native')
  }
}

function openAIContentToGeminiParts(
  req: Request,
  value: unknown,
  mapping: ResolvedOpenAIModelMapping,
  providerName?: string
): JsonRecord[] {
  if (value === undefined || value === null) return []
  if (typeof value === 'string') return value ? [{ text: value }] : []
  if (!Array.isArray(value)) {
    throw new GatewayRequestValidationError('OpenAI content 必须是字符串或数组', 'invalid_openai_gemini_bridge_content')
  }
  const parts: JsonRecord[] = []
  for (const raw of value) {
    const item = objectValue(raw)
    if (!item) continue
    const type = stringValue(item.type)
    if (type === 'text' || type === 'input_text' || type === 'output_text') {
      const text = stringValue(item.text)
      if (text) parts.push({ text })
      continue
    }
    if (type === 'image_url') {
      const imageUrl = objectValue(item.image_url)
      const url = stringValue(imageUrl?.url)
      if (url) parts.push(imageUrlToGeminiPart(req, url, mapping, providerName))
      continue
    }
    if (type === 'input_image') {
      const url = stringValue(item.image_url) ?? stringValue(item.file_id)
      if (url) parts.push(imageUrlToGeminiPart(req, url, mapping, providerName))
      continue
    }
    if (type === 'refusal') continue
    throw guidance(req, mapping, `当前${providerLabel(providerName)} Gemini native 上游不能保真承载 OpenAI content part: ${type ?? 'unknown'}。请移除该 part 后重试。`, 'unsupported_openai_content_part_for_gemini_native')
  }
  return parts
}

function textPartsFromOpenAIContent(
  req: Request,
  value: unknown,
  mapping: ResolvedOpenAIModelMapping,
  providerName?: string
): string[] {
  return openAIContentToGeminiParts(req, value, mapping, providerName).map((part) => stringValue(part.text)).filter((text): text is string => Boolean(text))
}

function anthropicContentToGeminiParts(
  req: Request,
  value: unknown,
  mapping: ResolvedOpenAIModelMapping,
  providerName?: string
): JsonRecord[] {
  if (typeof value === 'string') return value ? [{ text: value }] : []
  if (!Array.isArray(value)) return []
  const parts: JsonRecord[] = []
  for (const raw of value) {
    const item = objectValue(raw)
    if (!item) continue
    const type = stringValue(item.type)
    if (type === 'text') {
      const text = stringValue(item.text)
      if (text) parts.push({ text })
      continue
    }
    if (type === 'image') {
      const source = objectValue(item.source)
      if (source) parts.push(anthropicImageSourceToGeminiPart(req, source, mapping, providerName))
      continue
    }
    if (type === 'tool_use') {
      const name = stringValue(item.name) ?? 'tool_use'
      parts.push({ functionCall: { name, args: jsonObjectFromUnknown(item.input) } })
      continue
    }
    if (type === 'tool_result') {
      const name = stringValue(item.name) ?? stringValue(item.tool_use_id) ?? 'tool_result'
      parts.push(geminiFunctionResponsePart(name, item.content))
      continue
    }
    throw guidance(req, mapping, `当前${providerLabel(providerName)} Gemini native 上游不能保真承载 Anthropic content block: ${type ?? 'unknown'}。请移除该 block 后重试。`, 'unsupported_anthropic_content_part_for_gemini_native')
  }
  return parts
}

function anthropicSystemToText(
  req: Request,
  value: unknown,
  mapping: ResolvedOpenAIModelMapping,
  providerName?: string
): string | undefined {
  if (typeof value === 'string') return value
  if (!Array.isArray(value)) return undefined
  const texts: string[] = []
  for (const raw of value) {
    const item = objectValue(raw)
    if (!item) continue
    if (stringValue(item.type) !== 'text') {
      throw guidance(req, mapping, `当前${providerLabel(providerName)} Gemini native 上游只支持 Anthropic system text block。请移除非 text block 后重试。`, 'unsupported_anthropic_system_part_for_gemini_native')
    }
    const text = stringValue(item.text)
    if (text) texts.push(text)
  }
  return texts.join('\n') || undefined
}

function chatToolCallsToGeminiParts(value: unknown): JsonRecord[] {
  if (!Array.isArray(value)) return []
  const parts: JsonRecord[] = []
  for (const raw of value) {
    const call = objectValue(raw)
    const fn = objectValue(call?.function)
    const name = stringValue(fn?.name)
    if (!name) continue
    parts.push({ functionCall: { name, args: jsonObjectFromUnknown(fn?.arguments) } })
  }
  return parts
}

function chatToolsToGeminiTools(
  req: Request,
  value: unknown,
  mapping: ResolvedOpenAIModelMapping,
  providerName?: string
): JsonRecord[] {
  if (!Array.isArray(value)) return []
  const output: JsonRecord[] = []
  for (const raw of value) {
    const tool = objectValue(raw)
    if (!tool) continue
    if (stringValue(tool.type) !== 'function') {
      throw guidance(req, mapping, `当前${providerLabel(providerName)} Gemini native 上游只支持 OpenAI function tool。请移除 ${stringValue(tool.type) ?? 'unknown'} tool 后重试。`, 'unsupported_openai_tool_for_gemini_native')
    }
    const fn = objectValue(tool.function)
    const declaration = functionDeclarationFromOpenAITool(fn)
    if (declaration) output.push(declaration)
  }
  return output
}

function responsesToolsToGeminiTools(
  req: Request,
  value: unknown,
  mapping: ResolvedOpenAIModelMapping,
  providerName?: string
): JsonRecord[] {
  if (!Array.isArray(value)) return []
  const output: JsonRecord[] = []
  for (const raw of value) {
    const tool = objectValue(raw)
    if (!tool) continue
    const type = stringValue(tool.type)
    if (type !== 'function') {
      throw guidance(req, mapping, `当前${providerLabel(providerName)} Gemini native 上游只支持 Responses function tool。请改用本地可执行工具运行时或移除 ${type ?? 'unknown'} tool 后重试。`, 'unsupported_responses_tool_for_gemini_native')
    }
    const declaration = functionDeclarationFromOpenAITool(tool)
    if (declaration) output.push(declaration)
  }
  return output
}

function anthropicToolsToGeminiTools(
  req: Request,
  value: unknown,
  mapping: ResolvedOpenAIModelMapping,
  providerName?: string
): JsonRecord[] {
  if (!Array.isArray(value)) return []
  const output: JsonRecord[] = []
  for (const raw of value) {
    const tool = objectValue(raw)
    const explicitType = stringValue(tool?.type)
    if (explicitType && explicitType !== 'custom') {
      throw guidance(req, mapping, `当前${providerLabel(providerName)} Gemini native 上游不支持 Anthropic server tool：${explicitType}。请客户端配置本地 MCP 或改用真实支持该工具的上游。`, 'unsupported_anthropic_messages_server_tool')
    }
    const name = stringValue(tool?.name)
    if (!name) continue
    output.push({
      name,
      description: stringValue(tool?.description) ?? '',
      parameters: objectValue(tool?.input_schema) ?? { type: 'object', properties: {} }
    })
  }
  return output
}

function functionDeclarationFromOpenAITool(value: JsonRecord | undefined): JsonRecord | undefined {
  const name = stringValue(value?.name)
  if (!name) return undefined
  return {
    name,
    description: stringValue(value?.description) ?? '',
    parameters: objectValue(value?.parameters) ?? { type: 'object', properties: {} }
  }
}

function generationConfigFromSource(
  req: Request,
  body: JsonRecord,
  mapping: ResolvedOpenAIModelMapping,
  providerName?: string
): JsonRecord {
  const config: JsonRecord = {}
  const temperature = numberValue(body.temperature)
  if (temperature !== undefined) config.temperature = temperature
  const topP = numberValue(body.top_p)
  if (topP !== undefined) config.topP = topP
  const maxTokens = numberValue(body.max_tokens) ?? numberValue(body.max_completion_tokens) ?? numberValue(body.max_output_tokens)
  if (maxTokens !== undefined) config.maxOutputTokens = Math.trunc(maxTokens)
  const stop = body.stop ?? body.stop_sequences
  if (typeof stop === 'string') config.stopSequences = [stop]
  if (Array.isArray(stop)) config.stopSequences = stop.filter((item): item is string => typeof item === 'string')
  const responseMimeType = responseMimeTypeFromSource(body)
  if (responseMimeType) config.responseMimeType = responseMimeType
  const schema = responseSchemaFromSource(body)
  if (schema) config.responseSchema = schema
  const thinkingLevel = geminiThinkingLevelFromSource(body)
  if (thinkingLevel) config.thinkingConfig = { thinkingLevel }
  if (body.logprobs !== undefined || body.top_logprobs !== undefined) {
    throw guidance(req, mapping, `当前${providerLabel(providerName)} Gemini native 上游不能保真承载 OpenAI logprobs。请移除 logprobs 后重试。`, 'unsupported_logprobs_for_gemini_native')
  }
  return config
}

function geminiThinkingLevelFromSource(body: JsonRecord): string | undefined {
  const reasoning = objectValue(body.reasoning)
  const effort = stringValue(reasoning?.effort) ?? stringValue(body.reasoning_effort)
  if (effort === 'minimal' || effort === 'low' || effort === 'medium' || effort === 'high') {
    return effort
  }
  return undefined
}

function geminiServiceTierFromSource(body: JsonRecord): string | undefined {
  const tier = stringValue(body.service_tier)
  if (tier === 'priority' || tier === 'flex') return tier
  if (tier === 'default') return 'standard'
  return undefined
}

function responseMimeTypeFromSource(body: JsonRecord): string | undefined {
  const responseFormat = objectValue(body.response_format)
  const responseFormatType = stringValue(responseFormat?.type)
  if (responseFormatType === 'json_object' || responseFormatType === 'json_schema') return 'application/json'
  const text = objectValue(body.text)
  const textFormat = objectValue(text?.format)
  const textFormatType = stringValue(textFormat?.type)
  if (textFormatType === 'json_object' || textFormatType === 'json_schema') return 'application/json'
  return undefined
}

function responseSchemaFromSource(body: JsonRecord): JsonRecord | undefined {
  const responseFormat = objectValue(body.response_format)
  const jsonSchema = objectValue(responseFormat?.json_schema)
  const schema = objectValue(jsonSchema?.schema)
  if (schema) return schema
  const text = objectValue(body.text)
  const textFormat = objectValue(text?.format)
  const textJsonSchema = objectValue(textFormat?.json_schema)
  return objectValue(textJsonSchema?.schema)
}

function geminiToolConfigFromSource(body: JsonRecord): JsonRecord | undefined {
  const toolChoice = body.tool_choice
  if (toolChoice === undefined || toolChoice === 'auto') return undefined
  if (toolChoice === 'none') {
    return { functionCallingConfig: { mode: 'NONE' } }
  }
  if (toolChoice === 'required' || toolChoice === 'any') {
    return { functionCallingConfig: { mode: 'ANY' } }
  }
  const choice = objectValue(toolChoice)
  const fn = objectValue(choice?.function)
  const name = stringValue(fn?.name)
  if (name) {
    return { functionCallingConfig: { mode: 'ANY', allowedFunctionNames: [name] } }
  }
  return undefined
}

function imageUrlToGeminiPart(
  req: Request,
  url: string,
  mapping: ResolvedOpenAIModelMapping,
  providerName?: string
): JsonRecord {
  const dataUrl = parseDataUrl(url)
  if (dataUrl) {
    return { inlineData: { mimeType: dataUrl.mediaType, data: dataUrl.data } }
  }
  if (/^https?:\/\//i.test(url) || url.startsWith('gs://')) {
    return { fileData: { fileUri: url, mimeType: guessImageMimeType(url) } }
  }
  throw guidance(req, mapping, `当前${providerLabel(providerName)} Gemini native 上游不能直接读取 OpenAI file_id 图片引用。请提供 data URL、公开 HTTPS 图片 URL 或先上传到 Gemini Files 后重试。`, 'unsupported_openai_image_reference_for_gemini_native')
}

function anthropicImageSourceToGeminiPart(
  req: Request,
  source: JsonRecord,
  mapping: ResolvedOpenAIModelMapping,
  providerName?: string
): JsonRecord {
  const type = stringValue(source.type)
  if (type === 'base64') {
    const mediaType = stringValue(source.media_type) ?? 'image/png'
    const data = stringValue(source.data)
    if (data) return { inlineData: { mimeType: mediaType, data } }
  }
  if (type === 'url') {
    const url = stringValue(source.url)
    if (url) return imageUrlToGeminiPart(req, url, mapping, providerName)
  }
  throw guidance(req, mapping, `当前${providerLabel(providerName)} Gemini native 上游不能保真承载该 Anthropic image source。请使用 base64 或 URL 图片后重试。`, 'unsupported_anthropic_image_source_for_gemini_native')
}

function geminiFunctionResponsePart(name: string, value: unknown): JsonRecord {
  return {
    functionResponse: {
      name,
      response: isPlainObject(value) ? value : { content: value ?? '' }
    }
  }
}

function transformGeminiJsonToDownstreamJson(
  body: AsyncIterable<Uint8Array>,
  protocol: 'chat_completions' | 'responses' | 'messages',
  model: string
): AsyncIterable<Uint8Array> {
  const encoder = new TextEncoder()
  return (async function* () {
    const text = await readStreamText(body)
    const data = JSON.parse(text) as unknown
    const summary = summarizeGeminiResponse(isPlainObject(data) ? data : {})
    yield encoder.encode(JSON.stringify(renderJsonResponse(protocol, model, summary)))
  })()
}

function transformGeminiSseToDownstreamSse(
  body: AsyncIterable<Uint8Array>,
  protocol: 'chat_completions' | 'responses' | 'messages',
  model: string
): AsyncIterable<Uint8Array> {
  const encoder = new TextEncoder()
  const decoder = new StringDecoder('utf8')
  let buffer = ''
  const state = createSseRenderState(protocol, model)
  return (async function* () {
    for await (const chunk of body) {
      buffer += decoder.write(Buffer.from(chunk))
      const events = takeCompleteSseEvents()
      for (const eventText of events) {
        const event = parseSseEvent(eventText)
        if (!event || event.data === '[DONE]') continue
        const data = safeJsonParse(event.data)
        if (!isPlainObject(data)) continue
        const summary = summarizeGeminiResponse(data)
        const rendered = renderSseSummary(state, summary)
        if (rendered) yield encoder.encode(rendered)
      }
    }
    buffer += decoder.end()
    const events = takeCompleteSseEvents(true)
    for (const eventText of events) {
      const event = parseSseEvent(eventText)
      if (!event || event.data === '[DONE]') continue
      const data = safeJsonParse(event.data)
      if (!isPlainObject(data)) continue
      const rendered = renderSseSummary(state, summarizeGeminiResponse(data))
      if (rendered) yield encoder.encode(rendered)
    }
    yield encoder.encode(renderSseDone(state))
  })()

  function takeCompleteSseEvents(flush = false): string[] {
    const events: string[] = []
    while (true) {
      const match = /\r?\n\r?\n/.exec(buffer)
      if (!match) break
      events.push(buffer.slice(0, match.index))
      buffer = buffer.slice(match.index + match[0].length)
    }
    if (flush && buffer.trim()) {
      events.push(buffer)
      buffer = ''
    }
    return events
  }
}

function summarizeGeminiResponse(data: JsonRecord): GeminiResponseSummary {
  const candidates = Array.isArray(data.candidates) ? data.candidates : []
  const functionCalls: GeminiFunctionCall[] = []
  const text: string[] = []
  let finishReason: string | undefined
  for (const candidateValue of candidates) {
    const candidate = objectValue(candidateValue)
    if (!candidate) continue
    finishReason ||= stringValue(candidate.finishReason)
    const content = objectValue(candidate.content)
    const parts = Array.isArray(content?.parts) ? content.parts : []
    for (const partValue of parts) {
      const part = objectValue(partValue)
      if (!part) continue
      const partText = stringValue(part.text)
      if (partText) text.push(partText)
      const fn = objectValue(part.functionCall)
      const name = stringValue(fn?.name)
      if (name) {
        functionCalls.push({
          id: `call_${functionCalls.length}`,
          name,
          args: jsonObjectFromUnknown(fn?.args)
        })
      }
    }
  }
  return {
    text: text.join(''),
    functionCalls,
    finishReason,
    usage: objectValue(data.usageMetadata)
  }
}

function renderJsonResponse(
  protocol: 'chat_completions' | 'responses' | 'messages',
  model: string,
  summary: GeminiResponseSummary
): JsonRecord {
  if (protocol === 'chat_completions') return renderChatJson(model, summary)
  if (protocol === 'responses') return renderResponsesJson(model, summary)
  return renderAnthropicJson(model, summary)
}

function renderChatJson(model: string, summary: GeminiResponseSummary): JsonRecord {
  const message: JsonRecord = {
    role: 'assistant',
    content: summary.functionCalls.length > 0 ? null : summary.text
  }
  if (summary.functionCalls.length > 0) {
    message.tool_calls = summary.functionCalls.map((call, index) => ({
      id: call.id,
      type: 'function',
      function: { name: call.name, arguments: JSON.stringify(call.args) },
      index
    }))
  }
  return {
    id: `chatcmpl_${Date.now().toString(36)}`,
    object: 'chat.completion',
    created: unixNow(),
    model,
    choices: [{ index: 0, message, finish_reason: chatFinishReason(summary) }],
    usage: openAIUsage(summary.usage)
  }
}

function renderResponsesJson(model: string, summary: GeminiResponseSummary): JsonRecord {
  const output: JsonRecord[] = []
  if (summary.text) {
    output.push({
      id: `msg_${Date.now().toString(36)}`,
      type: 'message',
      status: 'completed',
      role: 'assistant',
      content: [{ type: 'output_text', text: summary.text, annotations: [] }]
    })
  }
  for (const call of summary.functionCalls) {
    output.push({
      id: call.id,
      type: 'function_call',
      call_id: call.id,
      name: call.name,
      arguments: JSON.stringify(call.args),
      status: 'completed'
    })
  }
  return {
    id: `resp_${Date.now().toString(36)}`,
    object: 'response',
    created_at: unixNow(),
    status: 'completed',
    model,
    output,
    usage: responsesUsage(summary.usage)
  }
}

function renderAnthropicJson(model: string, summary: GeminiResponseSummary): JsonRecord {
  const content: JsonRecord[] = []
  if (summary.text) content.push({ type: 'text', text: summary.text })
  for (const call of summary.functionCalls) {
    content.push({ type: 'tool_use', id: call.id, name: call.name, input: call.args })
  }
  return {
    id: `msg_${Date.now().toString(36)}`,
    type: 'message',
    role: 'assistant',
    model,
    content,
    stop_reason: summary.functionCalls.length > 0 ? 'tool_use' : anthropicStopReason(summary.finishReason),
    stop_sequence: null,
    usage: anthropicUsage(summary.usage)
  }
}

type SseRenderState = {
  protocol: 'chat_completions' | 'responses' | 'messages'
  model: string
  started: boolean
  textStarted: boolean
  completed: boolean
  contentIndex: number
}

function createSseRenderState(protocol: 'chat_completions' | 'responses' | 'messages', model: string): SseRenderState {
  return { protocol, model, started: false, textStarted: false, completed: false, contentIndex: 0 }
}

function renderSseSummary(state: SseRenderState, summary: GeminiResponseSummary): string {
  if (state.protocol === 'chat_completions') return renderChatSseSummary(state, summary)
  if (state.protocol === 'responses') return renderResponsesSseSummary(state, summary)
  return renderAnthropicSseSummary(state, summary)
}

function renderChatSseSummary(state: SseRenderState, summary: GeminiResponseSummary): string {
  const output: string[] = []
  if (!state.started) {
    output.push(chatSse({ id: `chatcmpl_${Date.now().toString(36)}`, object: 'chat.completion.chunk', created: unixNow(), model: state.model, choices: [{ index: 0, delta: { role: 'assistant' }, finish_reason: null }] }))
    state.started = true
  }
  if (summary.text) {
    output.push(chatSse({ id: `chatcmpl_${Date.now().toString(36)}`, object: 'chat.completion.chunk', created: unixNow(), model: state.model, choices: [{ index: 0, delta: { content: summary.text }, finish_reason: null }] }))
  }
  for (const call of summary.functionCalls) {
    output.push(chatSse({
      id: `chatcmpl_${Date.now().toString(36)}`,
      object: 'chat.completion.chunk',
      created: unixNow(),
      model: state.model,
      choices: [{
        index: 0,
        delta: { tool_calls: [{ index: state.contentIndex++, id: call.id, type: 'function', function: { name: call.name, arguments: JSON.stringify(call.args) } }] },
        finish_reason: null
      }]
    }))
  }
  if (summary.finishReason) {
    output.push(chatSse({ id: `chatcmpl_${Date.now().toString(36)}`, object: 'chat.completion.chunk', created: unixNow(), model: state.model, choices: [{ index: 0, delta: {}, finish_reason: chatFinishReason(summary) }] }))
    state.completed = true
  }
  return output.join('')
}

function renderResponsesSseSummary(state: SseRenderState, summary: GeminiResponseSummary): string {
  const output: string[] = []
  if (!state.started) {
    output.push(responsesSse('response.created', { type: 'response.created', response: { id: `resp_${Date.now().toString(36)}`, object: 'response', status: 'in_progress', model: state.model, output: [] } }))
    output.push(responsesSse('response.output_item.added', { type: 'response.output_item.added', output_index: 0, item: { id: 'msg_0', type: 'message', role: 'assistant', content: [] } }))
    output.push(responsesSse('response.content_part.added', { type: 'response.content_part.added', item_id: 'msg_0', output_index: 0, content_index: 0, part: { type: 'output_text', text: '' } }))
    state.started = true
    state.textStarted = true
  }
  if (summary.text) {
    output.push(responsesSse('response.output_text.delta', { type: 'response.output_text.delta', item_id: 'msg_0', output_index: 0, content_index: 0, delta: summary.text }))
  }
  for (const call of summary.functionCalls) {
    output.push(responsesSse('response.output_item.added', { type: 'response.output_item.added', output_index: ++state.contentIndex, item: { id: call.id, type: 'function_call', call_id: call.id, name: call.name, arguments: JSON.stringify(call.args), status: 'completed' } }))
  }
  if (summary.finishReason) state.completed = true
  return output.join('')
}

function renderAnthropicSseSummary(state: SseRenderState, summary: GeminiResponseSummary): string {
  const output: string[] = []
  if (!state.started) {
    output.push(anthropicSse('message_start', { type: 'message_start', message: { id: `msg_${Date.now().toString(36)}`, type: 'message', role: 'assistant', model: state.model, content: [], stop_reason: null, stop_sequence: null, usage: { input_tokens: 0, output_tokens: 0 } } }))
    state.started = true
  }
  if (summary.text) {
    if (!state.textStarted) {
      output.push(anthropicSse('content_block_start', { type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } }))
      state.textStarted = true
    }
    output.push(anthropicSse('content_block_delta', { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: summary.text } }))
  }
  for (const call of summary.functionCalls) {
    const index = ++state.contentIndex
    output.push(anthropicSse('content_block_start', { type: 'content_block_start', index, content_block: { type: 'tool_use', id: call.id, name: call.name, input: {} } }))
    output.push(anthropicSse('content_block_delta', { type: 'content_block_delta', index, delta: { type: 'input_json_delta', partial_json: JSON.stringify(call.args) } }))
    output.push(anthropicSse('content_block_stop', { type: 'content_block_stop', index }))
  }
  if (summary.finishReason) state.completed = true
  return output.join('')
}

function renderSseDone(state: SseRenderState): string {
  if (state.protocol === 'chat_completions') {
    return `${state.completed ? '' : chatSse({ id: `chatcmpl_${Date.now().toString(36)}`, object: 'chat.completion.chunk', created: unixNow(), model: state.model, choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] })}data: [DONE]\n\n`
  }
  if (state.protocol === 'responses') {
    return [
      responsesSse('response.content_part.done', { type: 'response.content_part.done', item_id: 'msg_0', output_index: 0, content_index: 0, part: { type: 'output_text', text: '' } }),
      responsesSse('response.output_item.done', { type: 'response.output_item.done', output_index: 0, item: { id: 'msg_0', type: 'message', role: 'assistant', content: [] } }),
      responsesSse('response.completed', { type: 'response.completed', response: { id: `resp_${Date.now().toString(36)}`, object: 'response', status: 'completed', model: state.model } })
    ].join('')
  }
  const output: string[] = []
  if (state.textStarted) output.push(anthropicSse('content_block_stop', { type: 'content_block_stop', index: 0 }))
  output.push(anthropicSse('message_delta', { type: 'message_delta', delta: { stop_reason: 'end_turn', stop_sequence: null }, usage: { output_tokens: 0 } }))
  output.push(anthropicSse('message_stop', { type: 'message_stop' }))
  return output.join('')
}

function downstreamProtocol(mapping: ResolvedOpenAIModelMapping): 'chat_completions' | 'responses' | 'messages' {
  if (mapping.sourceEndpointFamily === OPENAI_RESPONSES_FAMILY) return 'responses'
  if (mapping.sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) return 'messages'
  return 'chat_completions'
}

function guidance(
  req: Request,
  mapping: ResolvedOpenAIModelMapping,
  message: string,
  code: string
): GatewayAgentGuidanceResponse {
  return new GatewayAgentGuidanceResponse({
    message,
    code,
    protocol: downstreamProtocol(mapping),
    stream: requestStream(req),
    model: mapping.sourceModel
  })
}

async function readStreamText(body: AsyncIterable<Uint8Array>): Promise<string> {
  const decoder = new StringDecoder('utf8')
  let output = ''
  for await (const value of body) {
    output += decoder.write(Buffer.from(value))
  }
  output += decoder.end()
  return output
}

function parseSseEvent(text: string): { event?: string; data: string } | undefined {
  const data: string[] = []
  let event: string | undefined
  for (const line of text.split(/\r?\n/)) {
    if (line.startsWith('event:')) event = line.slice('event:'.length).trim()
    if (line.startsWith('data:')) data.push(line.slice('data:'.length).trimStart())
  }
  if (!data.length) return undefined
  return { event, data: data.join('\n') }
}

function safeJsonParse(value: string): unknown {
  try {
    return JSON.parse(value)
  } catch {
    return undefined
  }
}

function chatFinishReason(summary: GeminiResponseSummary): string {
  if (summary.functionCalls.length > 0) return 'tool_calls'
  const reason = (summary.finishReason ?? '').toUpperCase()
  if (reason === 'MAX_TOKENS') return 'length'
  if (reason === 'SAFETY' || reason === 'RECITATION') return 'content_filter'
  return 'stop'
}

function anthropicStopReason(reason?: string): string {
  const normalized = (reason ?? '').toUpperCase()
  if (normalized === 'MAX_TOKENS') return 'max_tokens'
  if (normalized === 'SAFETY' || normalized === 'RECITATION') return 'stop_sequence'
  return 'end_turn'
}

function openAIUsage(usage?: JsonRecord): JsonRecord {
  return {
    prompt_tokens: integerValue(usage?.promptTokenCount),
    completion_tokens: integerValue(usage?.candidatesTokenCount),
    total_tokens: integerValue(usage?.totalTokenCount)
  }
}

function responsesUsage(usage?: JsonRecord): JsonRecord {
  return {
    input_tokens: integerValue(usage?.promptTokenCount),
    output_tokens: integerValue(usage?.candidatesTokenCount),
    total_tokens: integerValue(usage?.totalTokenCount)
  }
}

function anthropicUsage(usage?: JsonRecord): JsonRecord {
  return {
    input_tokens: integerValue(usage?.promptTokenCount),
    output_tokens: integerValue(usage?.candidatesTokenCount)
  }
}

function chatSse(payload: JsonRecord): string {
  return `data: ${JSON.stringify(payload)}\n\n`
}

function responsesSse(event: string, payload: JsonRecord): string {
  return `event: ${event}\ndata: ${JSON.stringify(payload)}\n\n`
}

function anthropicSse(event: string, payload: JsonRecord): string {
  return `event: ${event}\ndata: ${JSON.stringify(payload)}\n\n`
}

function objectValue(value: unknown): JsonRecord | undefined {
  return isPlainObject(value) ? value : undefined
}

function isPlainObject(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' ? value : undefined
}

function numberValue(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function integerValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0
}

function jsonObjectFromUnknown(value: unknown): JsonRecord {
  if (isPlainObject(value)) return value
  if (typeof value === 'string') {
    const parsed = safeJsonParse(value)
    if (isPlainObject(parsed)) return parsed
  }
  return {}
}

function parseDataUrl(value: string): { mediaType: string; data: string } | undefined {
  const match = /^data:([^;,]+)(?:;[^,]*)?;base64,(.+)$/is.exec(value)
  if (!match?.[1] || !match[2]) return undefined
  return { mediaType: match[1], data: match[2] }
}

function guessImageMimeType(url: string): string {
  if (/\.jpe?g(?:[?#]|$)/i.test(url)) return 'image/jpeg'
  if (/\.webp(?:[?#]|$)/i.test(url)) return 'image/webp'
  if (/\.gif(?:[?#]|$)/i.test(url)) return 'image/gif'
  return 'image/png'
}

function unixNow(): number {
  return Math.floor(Date.now() / 1000)
}

function providerLabel(providerName?: string): string {
  return providerName ? ` ${providerName}` : ''
}
