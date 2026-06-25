import { StringDecoder } from 'node:string_decoder'
import type { Request } from 'express'

import type {
  AccountModelMappingSourceEndpointFamily,
  AccountSupportedEndpointMode
} from '../../../../domain/types.js'
import { runtimeConfig } from '../../../../config/runtime.js'
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
import { GatewayAgentGuidanceResponse, GatewayLocalProtocolResponse, GatewayRequestValidationError } from '../../../gateway/request/validation-error.js'
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
  resolveOpenAIAccountModelMapping,
  type OpenAIModelMappingRuntimeAccount
} from '../../../gateway/protocols/openai-v1/model-mapping.js'
import {
  openAIHostedToolRuntimeCompatibilityDetail,
  resolveOpenAIHostedToolRuntimeDecision
} from './openai-hosted-tool-runtime-registry.js'

type JsonRecord = Record<string, unknown>

export interface OpenAIToAnthropicBridgeBodyOptions {
  defaultMaxTokens?: number
  guidanceProviderName?: string
  modelOverride?: string
  fileResolver?: OpenAIToAnthropicFileResolver
  fileSearchExecutor?: OpenAIToAnthropicFileSearchExecutor
  imageGenerationExecutor?: OpenAIToAnthropicImageGenerationExecutor
  mcpProxyExecutor?: OpenAIToAnthropicMcpProxyExecutor
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

export interface OpenAIToAnthropicImageGenerationToolConfig {
  action: 'auto' | 'generate' | 'edit'
  size?: string
  quality?: string
  outputFormat?: string
  outputCompression?: number
  partialImages?: number
  inputImageMask?: JsonRecord
  moderation?: string
  background?: string
}

export interface OpenAIToAnthropicImageGenerationInput {
  prompt: string
  tool: OpenAIToAnthropicImageGenerationToolConfig
  signal?: AbortSignal
}

export interface OpenAIToAnthropicImageGenerationResult {
  imageBase64: string
  revisedPrompt?: string
  outputFormat?: string
}

export interface OpenAIToAnthropicImageGenerationPartialResult {
  imageBase64: string
  partialImageIndex?: number
}

export type OpenAIToAnthropicImageGenerationStreamEvent =
  | { type: 'partial_image'; partial: OpenAIToAnthropicImageGenerationPartialResult }
  | { type: 'completed'; result: OpenAIToAnthropicImageGenerationResult }

export interface OpenAIToAnthropicImageGenerationExecutor {
  generate(input: OpenAIToAnthropicImageGenerationInput): Promise<OpenAIToAnthropicImageGenerationResult>
  generateStream?(input: OpenAIToAnthropicImageGenerationInput): AsyncIterable<OpenAIToAnthropicImageGenerationStreamEvent>
}

export interface OpenAIToAnthropicMcpProxyInput {
  body: JsonRecord
  tool: JsonRecord
  model: string
  stream: boolean
  signal?: AbortSignal
}

export interface OpenAIToAnthropicMcpProxyToolDefinition {
  name: string
  description?: string
  inputSchema: JsonRecord
  annotations?: unknown
}

export interface OpenAIToAnthropicMcpProxyPreparedServer {
  serverLabel: string
  serverUrl: string
  tools: OpenAIToAnthropicMcpProxyToolDefinition[]
  context?: unknown
}

export interface OpenAIToAnthropicMcpProxyToolCallResult {
  outputText: string
  truncated?: boolean
  metadata?: JsonRecord
}

export interface OpenAIToAnthropicMcpProxyExecutor {
  prepare?(input: Omit<OpenAIToAnthropicMcpProxyInput, 'model' | 'stream'>): Promise<OpenAIToAnthropicMcpProxyPreparedServer>
  callTool?(input: {
    prepared: OpenAIToAnthropicMcpProxyPreparedServer
    toolName: string
    arguments: JsonRecord
    signal?: AbortSignal
  }): Promise<OpenAIToAnthropicMcpProxyToolCallResult>
  run(input: OpenAIToAnthropicMcpProxyInput): Promise<GatewayLocalProtocolResponse>
}

interface OpenAIToAnthropicBridgeTransformOptions {
  model?: string
  previousResponseId?: string
  onResponsesCompleted?: CodexResponsesChatBridgeCompletionHandler
}

interface OpenAIToAnthropicBridgeRequestPlan {
  structuredOutput?: OpenAIToAnthropicStructuredOutputPlan
  reasoningEffort?: string
  reasoningSummary?: string
  chatStreamIncludeUsage?: boolean
  fileSearch?: OpenAIToAnthropicFileSearchPlan
  imageGeneration?: OpenAIToAnthropicImageGenerationPlan
  responsesToolSearch?: OpenAIToAnthropicResponsesToolSearchPlan
  codeInterpreterMock?: OpenAIToAnthropicCodeInterpreterMockPlan
  mcpMock?: OpenAIToAnthropicMcpMockPlan
  mcpProxy?: OpenAIToAnthropicMcpProxyPlan
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

interface OpenAIToAnthropicImageGenerationPlan {
  prompt: string
  tool: OpenAIToAnthropicImageGenerationToolConfig
  executor: OpenAIToAnthropicImageGenerationExecutor
  outputItemId?: string
}

interface OpenAIToAnthropicCodeInterpreterMockPlan {
  tool: JsonRecord
  includeOutputs: boolean
}

interface OpenAIToAnthropicMcpMockPlan {
  tool: JsonRecord
}

interface OpenAIToAnthropicMcpProxyPlan {
  tool: JsonRecord
  executor?: OpenAIToAnthropicMcpProxyExecutor
  prepared?: OpenAIToAnthropicMcpProxyPreparedServer
  adaptersByAnthropicName?: Map<string, OpenAIToAnthropicMcpProxyToolAdapter>
  emittedListTools?: boolean
}

interface OpenAIToAnthropicMcpProxyToolAdapter {
  anthropicName: string
  toolName: string
  serverLabel: string
  description: string
  inputSchema: JsonRecord
  annotations?: unknown
}

interface OpenAIToAnthropicResponsesToolSearchPlan {
  adapters: OpenAIToAnthropicResponsesToolAdapter[]
  adaptersByAnthropicName: Map<string, OpenAIToAnthropicResponsesToolAdapter>
  adaptersByResponsesKey: Map<string, OpenAIToAnthropicResponsesToolAdapter>
  expandableToolCount: number
}

interface OpenAIToAnthropicResponsesToolAdapter {
  anthropicName: string
  responsesName: string
  namespace: string
  description: string
  parameters: unknown
}

interface OpenAIAllowedFunctionTools {
  directNames: Set<string>
  qualifiedKeys: Set<string>
}

interface AnthropicMessage {
  role: 'user' | 'assistant'
  content: AnthropicContentBlock[]
}

type AnthropicContentBlock = JsonRecord

interface OpenAIToolResultHistory {
  toolCallIds: Set<string>
  completedToolCallIds: Set<string>
}

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
  toolCallIndex?: number
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
  nextToolCallIndex: number
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
const supportedAnthropicBridgeReasoningEfforts = new Set(['none', 'minimal', 'low', 'medium', 'high'])
const supportedAnthropicBridgeReasoningSummaries = new Set(['auto', 'concise', 'detailed', 'none'])
const supportedOpenAIResponsesIncludesForAnthropicBridge = new Set(['file_search_call.results'])
const openAIResponsesIncludesHandledByDedicatedValidators = new Set([
  'message.output_text.logprobs',
  'reasoning.encrypted_content'
])
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
  validateOpenAIToAnthropicMcpToolDefinitions(sourceEndpointFamily, body)
  validateOpenAIToAnthropicUnsupportedIncludes(sourceEndpointFamily, body, requestPlan)
  validateOpenAIToAnthropicReasoningOptions(body)
  validateOpenAIToAnthropicToolCallControlOptions(sourceEndpointFamily, body)
  validateOpenAIToAnthropicResponseStateOptions(sourceEndpointFamily, body)
  validateOpenAIToAnthropicRequestControlOptions(sourceEndpointFamily, body)
  validateOpenAIToAnthropicOutputShapeOptions(sourceEndpointFamily, body)
  validateOpenAIToAnthropicSemanticControlOptions(body)
  await applyOpenAIToAnthropicFileSearchEmulation(sourceEndpointFamily, body, requestPlan, options.fileSearchExecutor, signal)
  await prepareOpenAIToAnthropicMcpProxyTools(sourceEndpointFamily, body, requestPlan, options.mcpProxyExecutor, {
    model,
    stream: requestStream(req)
  }, signal)
  const guidance = openAIToAnthropicUnsupportedToolGuidance(sourceEndpointFamily, body, requestPlan, options, {
    model,
    stream: requestStream(req)
  })
  if (guidance) {
    throw guidance
  }
  const localMcpProxyResponse = await openAIToAnthropicMcpProxyRuntimeResponse(body, requestPlan, options.mcpProxyExecutor, {
    model,
    stream: requestStream(req)
  }, signal)
  if (localMcpProxyResponse) {
    throw localMcpProxyResponse
  }
  const localCodeInterpreterMockResponse = openAIToAnthropicCodeInterpreterMockResponse(body, requestPlan, {
    model,
    stream: requestStream(req)
  })
  if (localCodeInterpreterMockResponse) {
    throw localCodeInterpreterMockResponse
  }
  const localMcpMockResponse = openAIToAnthropicMcpMockResponse(body, requestPlan, {
    model,
    stream: requestStream(req)
  })
  if (localMcpMockResponse) {
    throw localMcpMockResponse
  }
  await applyOpenAIToAnthropicImageGenerationEmulation(sourceEndpointFamily, body, requestPlan, options.imageGenerationExecutor)
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
  const toolHistory = createOpenAIToolResultHistory()
  for (const item of inputMessages) {
    if (!isPlainObject(item)) continue
    const role = stringValue(item.role)
    if (role === 'system' || role === 'developer') {
      appendSystemText(systemParts, openAIContentToText(item.content))
      continue
    }
    if (role === 'tool') {
      const callId = validateOpenAIToolResultHistory(
        toolHistory,
        stringValue(item.tool_call_id) ?? stringValue(item.id),
        'Chat'
      )
      appendAnthropicMessage(messages, {
        role: 'user',
        content: [{
          type: 'tool_result',
          tool_use_id: callId,
          content: openAIContentToText(item.content)
        }]
      })
      continue
    }
    if (role !== 'user' && role !== 'assistant') continue
    const content = await openAIChatContentToAnthropicBlocks(item.content, contentContext)
    if (role === 'assistant') {
      const toolUseBlocks = chatToolCallsToAnthropicToolUseBlocks(item.tool_calls)
      rememberAnthropicToolUseBlocks(toolHistory, toolUseBlocks)
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
  appendImageGenerationPromptInstruction(systemParts, requestPlan.imageGeneration)
  const output = baseAnthropicBody(body, model, options)
  output.messages = messages
  const system = systemParts.join('\n\n').trim()
  if (system) output.system = system
  const tools = chatToolsToAnthropicTools(body.tools, body.tool_choice, requestPlan)
  applyStructuredOutputPlan(output, tools, body.tool_choice, requestPlan.structuredOutput, body.parallel_tool_calls === false, requestPlan)
  validateAnthropicThinkingToolChoiceCompatibility(output)
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
  const toolHistory = createOpenAIToolResultHistory()
  if (typeof input === 'string') {
    appendAnthropicMessage(messages, {
      role: 'user',
      content: [{ type: 'text', text: input }]
    })
  } else if (Array.isArray(input)) {
    for (const item of input) {
      await appendResponsesInputItemAsAnthropicMessage(messages, systemParts, item, contentContext, toolHistory, requestPlan)
    }
  } else if (isPlainObject(input)) {
    await appendResponsesInputItemAsAnthropicMessage(messages, systemParts, input, contentContext, toolHistory, requestPlan)
  }
  if (!messages.length) {
    appendAnthropicMessage(messages, { role: 'user', content: [{ type: 'text', text: '' }] })
  }
  appendFileSearchContext(systemParts, requestPlan.fileSearch)
  const output = baseAnthropicBody(body, model, options)
  output.messages = messages
  const system = systemParts.join('\n\n').trim()
  if (system) output.system = system
  const tools = responsesToolsToAnthropicTools(body.tools, body.tool_choice, requestPlan)
  applyStructuredOutputPlan(output, tools, body.tool_choice, requestPlan.structuredOutput, body.parallel_tool_calls === false, requestPlan)
  validateAnthropicThinkingToolChoiceCompatibility(output)
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
  const user = stringValue(body.safety_identifier) ?? stringValue(body.user)
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
  contentContext: OpenAIToAnthropicBridgeContentContext,
  toolHistory: OpenAIToolResultHistory,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan
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
    const anthropicName = anthropicToolNameForResponsesFunctionCall(name, stringValue(item.namespace), requestPlan)
    appendAnthropicMessage(messages, {
      role: 'assistant',
      content: [{
        type: 'tool_use',
        id: callId,
        name: anthropicName,
        input: anthropicToolInputFromOpenAIArguments(item.arguments)
      }]
    })
    rememberOpenAIToolCall(toolHistory, callId)
    return
  }
  if (item.type === 'function_call_output') {
    const callId = validateOpenAIToolResultHistory(toolHistory, stringValue(item.call_id), 'Responses')
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
    validateResponsesReasoningInputItem(item)
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

function validateResponsesReasoningInputItem(item: JsonRecord): void {
  if (!hasMeaningfulField(item, 'encrypted_content')) return
  throw bridgeValidationError(
    'OpenAI 到 Anthropic 桥接不能恢复或验证历史 reasoning.encrypted_content；请移除该 reasoning item、提供可读 summary/content，或改用原生 Responses 上游',
    'openai_anthropic_bridge_encrypted_reasoning_input_unsupported'
  )
}

function createOpenAIToolResultHistory(): OpenAIToolResultHistory {
  return {
    toolCallIds: new Set<string>(),
    completedToolCallIds: new Set<string>()
  }
}

function rememberAnthropicToolUseBlocks(history: OpenAIToolResultHistory, blocks: AnthropicContentBlock[]): void {
  for (const block of blocks) {
    if (block.type !== 'tool_use') continue
    const id = stringValue(block.id)
    if (id) rememberOpenAIToolCall(history, id)
  }
}

function rememberOpenAIToolCall(history: OpenAIToolResultHistory, callId: string): void {
  history.toolCallIds.add(callId)
}

function validateOpenAIToolResultHistory(
  history: OpenAIToolResultHistory,
  callId: string | undefined,
  sourceFamily: 'Chat' | 'Responses'
): string {
  if (!callId) {
    throw bridgeValidationError(
      sourceFamily === 'Chat'
        ? 'Chat role=tool 缺少 tool_call_id，无法匹配前文 assistant tool_call'
        : 'Responses function_call_output 缺少 call_id，无法匹配前文 function_call',
      'openai_anthropic_bridge_tool_result_missing_call_id'
    )
  }
  if (!history.toolCallIds.has(callId)) {
    throw bridgeValidationError(
      sourceFamily === 'Chat'
        ? `Chat role=tool 的 tool_call_id ${callId} 未匹配任何前文 assistant tool_call`
        : `Responses function_call_output 的 call_id ${callId} 未匹配任何前文 function_call`,
      'openai_anthropic_bridge_orphan_tool_result'
    )
  }
  if (history.completedToolCallIds.has(callId)) {
    throw bridgeValidationError(
      sourceFamily === 'Chat'
        ? `Chat role=tool 的 tool_call_id ${callId} 已经返回过工具结果`
        : `Responses function_call_output 的 call_id ${callId} 已经返回过工具结果`,
      'openai_anthropic_bridge_duplicate_tool_result'
    )
  }
  history.completedToolCallIds.add(callId)
  return callId
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
      continue
    }
    if (item.type === 'input_audio' || item.type === 'audio') throw unsupportedOpenAIAudioContentPart('Chat')
    throw unsupportedOpenAIContentPart('Chat', item.type)
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
      continue
    }
    if (item.type === 'input_audio' || item.type === 'audio') throw unsupportedOpenAIAudioContentPart('Responses')
    throw unsupportedOpenAIContentPart('Responses', item.type)
  }
  return blocks
}

function unsupportedOpenAIAudioContentPart(sourceFamily: 'Chat' | 'Responses'): never {
  throw bridgeValidationError(
    `${sourceFamily} input_audio 当前不能桥接到 Anthropic Messages；请使用可消费音频输入的原生 OpenAI 上游，或先在客户端 / 本地运行时转写为文本`,
    'openai_anthropic_bridge_audio_input_unsupported'
  )
}

function unsupportedOpenAIContentPart(sourceFamily: 'Chat' | 'Responses', type: unknown): never {
  const typeLabel = typeof type === 'string' && type.length > 0 ? type : 'unknown'
  throw bridgeValidationError(
    `${sourceFamily} content block ${typeLabel} 当前没有 OpenAI 到 Anthropic Messages 的等价映射；请先补能力矩阵和 mock 回归后再启用`,
    'openai_anthropic_bridge_unsupported_content_part'
  )
}

function anthropicImageBlockFromUrl(url: string): AnthropicContentBlock {
  const dataUrl = parseDataUrl(url)
  if (dataUrl) {
    const mediaType = normalizeOpenAIFileMediaType(dataUrl.mediaType, undefined)
    if (!isAnthropicSupportedImageMediaType(mediaType)) {
      throw bridgeValidationError(
        `OpenAI 到 Anthropic 桥接当前只支持 image/jpeg、image/png、image/gif、image/webp 图片 data URL，收到 ${dataUrl.mediaType}`,
        'openai_anthropic_bridge_unsupported_image_media_type'
      )
    }
    const base64Data = normalizedBase64Data(dataUrl.data)
    if (!base64Data) {
      throw bridgeValidationError(
        'OpenAI 图片 data URL 不是合法 base64 数据',
        'openai_anthropic_bridge_invalid_image_base64'
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
      input: anthropicToolInputFromOpenAIArguments(fn?.arguments)
    })
  }
  return blocks
}

function chatToolsToAnthropicTools(
  value: unknown,
  toolChoice: unknown,
  requestPlan?: OpenAIToAnthropicBridgeRequestPlan
): JsonRecord[] {
  if (!Array.isArray(value)) return []
  const allowedFunctionTools = allowedOpenAIFunctionTools(toolChoice)
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
    if (!isOpenAIFunctionToolAllowed(allowedFunctionTools, name)) continue
    tools.push(anthropicToolFromFunctionDefinition(name, fn?.description, fn?.parameters))
  }
  return tools
}

function responsesToolsToAnthropicTools(
  value: unknown,
  toolChoice: unknown,
  requestPlan?: OpenAIToAnthropicBridgeRequestPlan
): JsonRecord[] {
  if (!Array.isArray(value)) return []
  const allowedFunctionTools = allowedOpenAIFunctionTools(toolChoice)
  const tools: JsonRecord[] = []
  for (const item of value) {
    if (!isPlainObject(item) || item.type !== 'function') {
      if (isOpenAIFileSearchTool(item) && requestPlan?.fileSearch) {
        continue
      }
      if (isOpenAIImageGenerationTool(item) && requestPlan?.imageGeneration) {
        continue
      }
      if (isResponsesToolSearchTool(item) && requestPlan?.responsesToolSearch) {
        continue
      }
      if (isResponsesNamespaceTool(item) && requestPlan?.responsesToolSearch) {
        appendResponsesNamespaceToolToAnthropicTools(tools, item, allowedFunctionTools, requestPlan)
        continue
      }
      if (isOpenAIMcpTool(item) && requestPlan?.mcpProxy?.prepared) {
        appendMcpProxyToolsToAnthropicTools(tools, requestPlan)
        continue
      }
      throw unsupportedOpenAIToolError(item, 'Responses')
    }
    const name = stringValue(item.name)
    if (!name) {
      throw bridgeValidationError('Responses function tool 缺少 name', 'openai_anthropic_bridge_invalid_tool')
    }
    if (!isOpenAIFunctionToolAllowed(allowedFunctionTools, name)) continue
    tools.push(anthropicToolFromFunctionDefinition(name, item.description, item.parameters))
  }
  return tools
}

function appendMcpProxyToolsToAnthropicTools(
  tools: JsonRecord[],
  requestPlan: OpenAIToAnthropicBridgeRequestPlan
): void {
  const adapters = requestPlan.mcpProxy?.adaptersByAnthropicName
  if (!adapters) return
  for (const adapter of adapters.values()) {
    tools.push(anthropicToolFromFunctionDefinition(adapter.anthropicName, adapter.description, adapter.inputSchema))
  }
}

function responsesToolSearchPlanFromResponsesBody(body: JsonRecord): OpenAIToAnthropicResponsesToolSearchPlan | undefined {
  const tools = Array.isArray(body.tools) ? body.tools : []
  if (!tools.some(isResponsesToolSearchTool)) return undefined

  const plan: OpenAIToAnthropicResponsesToolSearchPlan = {
    adapters: [],
    adaptersByAnthropicName: new Map(),
    adaptersByResponsesKey: new Map(),
    expandableToolCount: 0
  }
  const usedAnthropicNames = new Set<string>()
  for (const tool of tools) {
    if (!isPlainObject(tool) || tool.type !== 'function') continue
    const name = stringValue(tool.name)
    if (name) {
      usedAnthropicNames.add(name)
      plan.expandableToolCount += 1
    }
  }
  for (const tool of tools) {
    if (isResponsesNamespaceTool(tool)) {
      appendResponsesNamespaceAdapters(plan, usedAnthropicNames, tool)
    }
  }
  return plan.expandableToolCount > 0 ? plan : undefined
}

function appendResponsesNamespaceAdapters(
  plan: OpenAIToAnthropicResponsesToolSearchPlan,
  usedAnthropicNames: Set<string>,
  item: JsonRecord,
  inheritedNamespace?: string
): void {
  const namespace = responsesNamespaceName(item, inheritedNamespace)
  if (!namespace) return
  const namespaceDescription = stringValue(item.description)
  const tools = Array.isArray(item.tools) ? item.tools : []
  for (const child of tools) {
    if (!isPlainObject(child)) continue
    if (child.type === 'function') {
      const name = stringValue(child.name)
      if (!name) continue
      const key = responsesFunctionToolKey(name, namespace)
      if (plan.adaptersByResponsesKey.has(key)) continue
      const anthropicName = uniqueAnthropicToolName(
        `${namespace}__${name}`,
        usedAnthropicNames
      )
      const adapter: OpenAIToAnthropicResponsesToolAdapter = {
        anthropicName,
        responsesName: name,
        namespace,
        description: responsesNamespacedFunctionDescription({
          namespace,
          namespaceDescription,
          functionDescription: stringValue(child.description)
        }),
        parameters: child.parameters
      }
      plan.adapters.push(adapter)
      plan.adaptersByAnthropicName.set(anthropicName, adapter)
      plan.adaptersByResponsesKey.set(key, adapter)
      plan.expandableToolCount += 1
      continue
    }
    if (child.type === 'namespace') {
      appendResponsesNamespaceAdapters(plan, usedAnthropicNames, child, namespace)
    }
  }
}

function appendResponsesNamespaceToolToAnthropicTools(
  tools: JsonRecord[],
  item: JsonRecord,
  allowedFunctionTools: OpenAIAllowedFunctionTools | undefined,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  inheritedNamespace?: string
): void {
  const namespace = responsesNamespaceName(item, inheritedNamespace)
  if (!namespace) {
    throw bridgeValidationError('Responses namespace tool 缺少 name', 'openai_anthropic_bridge_invalid_tool')
  }
  const children = Array.isArray(item.tools) ? item.tools : []
  for (const child of children) {
    if (!isPlainObject(child)) continue
    if (child.type === 'function') {
      const name = stringValue(child.name)
      if (!name) {
        throw bridgeValidationError('Responses namespace function tool 缺少 name', 'openai_anthropic_bridge_invalid_tool')
      }
      if (!isOpenAIFunctionToolAllowed(allowedFunctionTools, name, namespace)) continue
      const adapter = requestPlan.responsesToolSearch?.adaptersByResponsesKey.get(responsesFunctionToolKey(name, namespace))
      if (!adapter) continue
      tools.push(anthropicToolFromFunctionDefinition(adapter.anthropicName, adapter.description, adapter.parameters))
      continue
    }
    if (child.type === 'namespace') {
      appendResponsesNamespaceToolToAnthropicTools(tools, child, allowedFunctionTools, requestPlan, namespace)
      continue
    }
    throw unsupportedOpenAIToolError(child, 'Responses')
  }
}

function isResponsesToolSearchTool(tool: unknown): tool is JsonRecord {
  return isPlainObject(tool) && tool.type === 'tool_search'
}

function isResponsesNamespaceTool(tool: unknown): tool is JsonRecord {
  return isPlainObject(tool) && tool.type === 'namespace'
}

function responsesNamespaceName(item: JsonRecord, inheritedNamespace?: string): string | undefined {
  return stringValue(item.namespace) ?? stringValue(item.name) ?? inheritedNamespace
}

function responsesFunctionToolKey(name: string, namespace?: string): string {
  return `${namespace ?? ''}\u0000${name}`
}

function responsesNamespacedFunctionDescription(input: {
  namespace: string
  namespaceDescription?: string
  functionDescription?: string
}): string {
  return [
    `OpenAI Responses namespace: ${input.namespace}.`,
    input.namespaceDescription ? `Namespace description: ${input.namespaceDescription}` : undefined,
    input.functionDescription
  ].filter(Boolean).join('\n\n')
}

function uniqueAnthropicToolName(baseName: string, usedNames: Set<string>): string {
  const base = truncateAnthropicToolName(sanitizeAnthropicToolName(baseName), 64)
  let candidate = base
  let suffixIndex = 2
  while (usedNames.has(candidate)) {
    const suffix = `_${suffixIndex++}`
    candidate = `${truncateAnthropicToolName(base, 64 - suffix.length)}${suffix}`
  }
  usedNames.add(candidate)
  return candidate
}

function sanitizeAnthropicToolName(name: string): string {
  return name
    .replace(/[^A-Za-z0-9_-]+/g, '_')
    .replace(/^_+|_+$/g, '')
    || 'tool'
}

function truncateAnthropicToolName(name: string, maxLength: number): string {
  return name.length <= maxLength ? name : name.slice(0, Math.max(1, maxLength))
}

function anthropicToolFromFunctionDefinition(name: string, description: unknown, parameters: unknown): JsonRecord {
  return {
    name,
    description: stringValue(description) ?? '',
    input_schema: isPlainObject(parameters) ? parameters : { type: 'object', properties: {} }
  }
}

function applyTools(
  output: JsonRecord,
  tools: JsonRecord[],
  toolChoice: unknown,
  disableParallelToolUse: boolean,
  requestPlan?: OpenAIToAnthropicBridgeRequestPlan
): void {
  if (!tools.length) return
  output.tools = tools
  const anthropicToolChoice = anthropicToolChoiceFromOpenAI(toolChoice, requestPlan)
  if (anthropicToolChoice) {
    if (disableParallelToolUse && anthropicToolChoice.type !== 'none') {
      anthropicToolChoice.disable_parallel_tool_use = true
    }
    output.tool_choice = anthropicToolChoice
  } else if (disableParallelToolUse) {
    output.tool_choice = { type: 'auto', disable_parallel_tool_use: true }
  }
}

function applyStructuredOutputPlan(
  output: JsonRecord,
  tools: JsonRecord[],
  toolChoice: unknown,
  structuredOutput: OpenAIToAnthropicStructuredOutputPlan | undefined,
  disableParallelToolUse: boolean,
  requestPlan?: OpenAIToAnthropicBridgeRequestPlan
): void {
  if (!structuredOutput?.syntheticToolName) {
    applyTools(output, tools, toolChoice, disableParallelToolUse, requestPlan)
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

function anthropicToolChoiceFromOpenAI(
  value: unknown,
  requestPlan?: OpenAIToAnthropicBridgeRequestPlan
): JsonRecord | undefined {
  if (value === undefined || value === null) return undefined
  if (value === 'auto') return { type: 'auto' }
  if (value === 'none') return { type: 'none' }
  if (value === 'required') return { type: 'any' }
  if (typeof value === 'string') return undefined
  if (!isPlainObject(value)) return undefined
  if (value.type === 'function') {
    const fn = objectValue(value.function)
    const name = stringValue(fn?.name) ?? stringValue(value.name)
    if (!name) return undefined
    const namespace = stringValue(fn?.namespace) ?? stringValue(value.namespace)
    return {
      type: 'tool',
      name: namespace
        ? anthropicToolNameForResponsesFunctionCall(name, namespace, requestPlan)
        : name
    }
  }
  if (value.type === 'auto') return { type: 'auto' }
  if (value.type === 'none') return { type: 'none' }
  if (value.type === 'required') return { type: 'any' }
  if (value.type === 'allowed_tools') {
    return stringValue(value.mode) === 'required'
      ? { type: 'any' }
      : { type: 'auto' }
  }
  return undefined
}

function allowedOpenAIFunctionTools(toolChoice: unknown): OpenAIAllowedFunctionTools | undefined {
  if (!isPlainObject(toolChoice) || toolChoice.type !== 'allowed_tools') return undefined
  const directNames = new Set<string>()
  const qualifiedKeys = new Set<string>()
  const tools = Array.isArray(toolChoice.tools) ? toolChoice.tools : []
  for (const tool of tools) {
    if (typeof tool === 'string') {
      directNames.add(tool)
      continue
    }
    if (!isPlainObject(tool) || tool.type !== 'function') continue
    const fn = objectValue(tool.function)
    const name = stringValue(tool.name) ?? stringValue(fn?.name)
    if (!name) continue
    const namespace = stringValue(tool.namespace) ?? stringValue(fn?.namespace)
    if (namespace) {
      qualifiedKeys.add(responsesFunctionToolKey(name, namespace))
      continue
    }
    directNames.add(name)
  }
  return { directNames, qualifiedKeys }
}

function isOpenAIFunctionToolAllowed(
  allowed: OpenAIAllowedFunctionTools | undefined,
  name: string,
  namespace?: string
): boolean {
  if (!allowed) return true
  if (namespace && allowed.qualifiedKeys.has(responsesFunctionToolKey(name, namespace))) return true
  return allowed.directNames.has(name)
}

function anthropicToolNameForResponsesFunctionCall(
  name: string,
  namespace: string | undefined,
  requestPlan?: OpenAIToAnthropicBridgeRequestPlan
): string {
  if (!namespace) return name
  const adapter = requestPlan?.responsesToolSearch?.adaptersByResponsesKey.get(responsesFunctionToolKey(name, namespace))
  return adapter?.anthropicName ?? name
}

function responsesFunctionCallItemFromAnthropicTool(
  input: {
    idSegment: string
    callId: string
    name: string
    argumentsText: string
    status: 'in_progress' | 'completed'
  },
  requestPlan?: OpenAIToAnthropicBridgeRequestPlan
): JsonRecord {
  const adapter = requestPlan?.responsesToolSearch?.adaptersByAnthropicName.get(input.name)
  const item: JsonRecord = {
    id: `fc_${safeIdSegment(input.idSegment)}`,
    type: 'function_call',
    status: input.status,
    call_id: input.callId,
    name: adapter?.responsesName ?? input.name,
    arguments: input.argumentsText
  }
  if (adapter?.namespace) item.namespace = adapter.namespace
  return item
}

function validateAnthropicThinkingToolChoiceCompatibility(output: JsonRecord): void {
  if (!isPlainObject(output.thinking)) return
  const toolChoice = objectValue(output.tool_choice)
  const toolChoiceType = stringValue(toolChoice?.type)
  if (toolChoiceType !== 'any' && toolChoiceType !== 'tool') return
  throw bridgeValidationError(
    'Anthropic Messages 不支持同时启用 thinking 和强制工具调用；请关闭 reasoning / thinking，或把 tool_choice 改为 auto / none',
    'openai_anthropic_bridge_thinking_forced_tool_choice_unsupported'
  )
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
  const responsesToolSearch = sourceEndpointFamily === OPENAI_RESPONSES_FAMILY
    ? responsesToolSearchPlanFromResponsesBody(body)
    : undefined
  const codeInterpreterMock = sourceEndpointFamily === OPENAI_RESPONSES_FAMILY
    ? codeInterpreterMockPlanFromResponsesBody(body)
    : undefined
  const mcpMock = sourceEndpointFamily === OPENAI_RESPONSES_FAMILY
    ? mcpMockPlanFromResponsesBody(body)
    : undefined
  const mcpProxy = sourceEndpointFamily === OPENAI_RESPONSES_FAMILY
    ? mcpProxyPlanFromResponsesBody(body)
    : undefined
  return {
    structuredOutput: sourceEndpointFamily === OPENAI_RESPONSES_FAMILY
      ? structuredOutputPlanFromResponsesBody(body)
      : structuredOutputPlanFromChatBody(body),
    reasoningEffort: reasoningEffortFromOpenAIBody(body),
    reasoningSummary: reasoningSummaryFromOpenAIBody(body),
    chatStreamIncludeUsage: sourceEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY
      ? objectValue(body.stream_options)?.include_usage === true
      : undefined,
    responsesToolSearch,
    codeInterpreterMock,
    mcpMock,
    mcpProxy
  }
}

function validateOpenAIToAnthropicUnsupportedIncludes(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  body: JsonRecord,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan
): void {
  if (sourceEndpointFamily !== OPENAI_RESPONSES_FAMILY) return
  if (!hasMeaningfulField(body, 'include')) return
  if (!Array.isArray(body.include)) {
    throw bridgeValidationError(
      'OpenAI Responses include 必须是字符串数组；Anthropic bridge 当前只支持 file_search_call.results，其他 include 需要原生 Responses 或对应本地运行时',
      'openai_anthropic_bridge_include_unsupported'
    )
  }

  const include = body.include
  if (include.includes('reasoning.encrypted_content')) {
    throw bridgeValidationError(
      'OpenAI 到 Anthropic 桥接不能生成或验证 OpenAI reasoning.encrypted_content；请移除 include=reasoning.encrypted_content，或改用原生 Responses 上游',
      'openai_anthropic_bridge_encrypted_reasoning_unsupported'
    )
  }

  const unsupportedIncludes = include.filter((item) => {
    if (typeof item !== 'string' || !item.trim()) return true
    const normalized = item.trim()
    if (normalized === 'code_interpreter_call.outputs' && requestPlan.codeInterpreterMock) return false
    return !supportedOpenAIResponsesIncludesForAnthropicBridge.has(normalized)
      && !openAIResponsesIncludesHandledByDedicatedValidators.has(normalized)
  })
  if (!unsupportedIncludes.length) return
  const unsupportedList = unsupportedIncludes
    .map((item) => typeof item === 'string' && item.trim() ? item.trim() : '<non_string_include>')
    .slice(0, 4)
    .join('、')
  throw bridgeValidationError(
    `Anthropic Messages 不能等价返回 OpenAI Responses include 扩展字段：${unsupportedList}；请移除这些 include，或改用原生 Responses / 对应本地运行时`,
    'openai_anthropic_bridge_include_unsupported'
  )
}

function validateOpenAIToAnthropicReasoningOptions(body: JsonRecord): void {
  const effort = reasoningEffortFromOpenAIBody(body)
  if (hasOpenAIReasoningEffortRequest(body) && (effort === undefined || !supportedAnthropicBridgeReasoningEfforts.has(effort))) {
    throw bridgeValidationError(
      'OpenAI 到 Anthropic 桥接当前只支持 reasoning.effort / reasoning_effort 为 none、minimal、low、medium、high；xhigh 或未知值需要上游 profile 明确支持后才能启用',
      'openai_anthropic_bridge_reasoning_effort_unsupported'
    )
  }

  const summary = reasoningSummaryFromOpenAIBody(body)
  if (hasOpenAIReasoningSummaryRequest(body) && (summary === undefined || !supportedAnthropicBridgeReasoningSummaries.has(summary))) {
    throw bridgeValidationError(
      'OpenAI 到 Anthropic 桥接当前只支持 reasoning.summary 为 auto、concise、detailed、none；未知 summary 会导致客户端误判 reasoning 输出形态',
      'openai_anthropic_bridge_reasoning_summary_unsupported'
    )
  }
}

function validateOpenAIToAnthropicToolCallControlOptions(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  body: JsonRecord
): void {
  if (sourceEndpointFamily !== OPENAI_RESPONSES_FAMILY || !hasMeaningfulField(body, 'max_tool_calls')) return
  throw bridgeValidationError(
    'Anthropic Messages 不能等价承接 OpenAI Responses max_tool_calls 工具调用次数上限；请移除 max_tool_calls，或改用原生 Responses 上游',
    'openai_anthropic_bridge_max_tool_calls_unsupported'
  )
}

function validateOpenAIToAnthropicMcpToolDefinitions(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  body: JsonRecord
): void {
  if (sourceEndpointFamily !== OPENAI_RESPONSES_FAMILY && sourceEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY) return
  const tools = Array.isArray(body.tools) ? body.tools.filter(isOpenAIMcpTool) : []
  if (!tools.length) return

  const seenLabels = new Set<string>()
  for (const tool of tools) {
    const serverLabel = stringValue(tool.server_label)
    if (!serverLabel) {
      throw bridgeValidationError(
        'MCP tool 缺少 server_label；请为每个 MCP server 配置唯一 server_label',
        'openai_anthropic_bridge_mcp_definition_invalid'
      )
    }
    if (seenLabels.has(serverLabel)) {
      throw bridgeValidationError(
        `MCP tool server_label 重复：${serverLabel}；同一请求中每个 MCP server_label 必须唯一`,
        'openai_anthropic_bridge_mcp_definition_invalid'
      )
    }
    seenLabels.add(serverLabel)

    const hasServerUrl = hasMeaningfulField(tool, 'server_url')
    const hasConnectorId = hasMeaningfulField(tool, 'connector_id')
    if (hasServerUrl && hasConnectorId) {
      throw bridgeValidationError(
        `MCP tool ${serverLabel} 不能同时设置 server_url 和 connector_id；远程 MCP server 与 OpenAI connector 需要二选一`,
        'openai_anthropic_bridge_mcp_definition_invalid'
      )
    }
    if (!hasServerUrl && !hasConnectorId) {
      throw bridgeValidationError(
        `MCP tool ${serverLabel} 缺少 server_url 或 connector_id；当前网关不会猜测远程 MCP server`,
        'openai_anthropic_bridge_mcp_definition_invalid'
      )
    }
    if (hasServerUrl) {
      const serverUrl = stringValue(tool.server_url)
      if (!serverUrl || !isAllowedMcpServerUrlForBridge(serverUrl)) {
        throw bridgeValidationError(
          `MCP tool ${serverLabel} 的 server_url 必须是 HTTPS URL；仅 mockai / 本地回归可在私有上游 allowlist 开启时使用 loopback HTTP`,
          'openai_anthropic_bridge_mcp_definition_invalid'
        )
      }
    }
    validateMcpAllowedToolsDefinition(serverLabel, tool.allowed_tools)
    validateMcpRequireApprovalDefinition(serverLabel, tool.require_approval)
    validateMcpAuthorizationDefinition(serverLabel, tool)
  }
}

function validateMcpAllowedToolsDefinition(serverLabel: string, value: unknown): void {
  if (!hasMeaningfulValue(value)) return
  if (!Array.isArray(value) || value.some((item) => typeof item !== 'string' || !item.trim())) {
    throw bridgeValidationError(
      `MCP tool ${serverLabel} 的 allowed_tools 必须是字符串数组`,
      'openai_anthropic_bridge_mcp_definition_invalid'
    )
  }
}

function validateMcpRequireApprovalDefinition(serverLabel: string, value: unknown): void {
  if (!hasMeaningfulValue(value)) return
  if (value === 'always' || value === 'never') return
  const config = objectValue(value)
  const never = objectValue(config?.never)
  const always = objectValue(config?.always)
  if (never && always) {
    throw bridgeValidationError(
      `MCP tool ${serverLabel} 的 require_approval 不能同时设置 always 和 never 策略`,
      'openai_anthropic_bridge_mcp_definition_invalid'
    )
  }
  const neverToolNames = never ? never.tool_names : undefined
  const alwaysToolNames = always ? always.tool_names : undefined
  const validNever = never === undefined || stringArrayValue(neverToolNames).length === arrayLength(neverToolNames)
  const validAlways = always === undefined || stringArrayValue(alwaysToolNames).length === arrayLength(alwaysToolNames)
  if (config && validNever && validAlways && (never !== undefined || always !== undefined)) return
  throw bridgeValidationError(
    `MCP tool ${serverLabel} 的 require_approval 只支持 always、never，或包含 always/never.tool_names 的对象`,
    'openai_anthropic_bridge_mcp_definition_invalid'
  )
}

function validateMcpAuthorizationDefinition(serverLabel: string, tool: JsonRecord): void {
  if (!hasMeaningfulField(tool, 'authorization')) return
  const headers = objectValue(tool.headers)
  if (!headers) return
  if (hasMeaningfulField(headers, 'Authorization') || hasMeaningfulField(headers, 'authorization')) {
    throw bridgeValidationError(
      `MCP tool ${serverLabel} 不能同时设置 authorization 和 headers.Authorization；请只保留一个凭据来源`,
      'openai_anthropic_bridge_mcp_definition_invalid'
    )
  }
}

function arrayLength(value: unknown): number {
  return Array.isArray(value) ? value.length : -1
}

function isAllowedMcpServerUrlForBridge(value: string): boolean {
  let url: URL
  try {
    url = new URL(value)
  } catch {
    return false
  }
  if (url.protocol === 'https:') return true
  if (url.protocol !== 'http:' || !runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls) return false
  return isLoopbackMcpHost(url.hostname)
}

function isLoopbackMcpHost(hostname: string): boolean {
  const normalized = hostname.toLowerCase()
  return normalized === 'localhost'
    || normalized === '127.0.0.1'
    || normalized === '::1'
    || normalized === '[::1]'
}

function validateOpenAIToAnthropicResponseStateOptions(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  body: JsonRecord
): void {
  if (sourceEndpointFamily !== OPENAI_RESPONSES_FAMILY) return
  if (hasMeaningfulField(body, 'conversation')) {
    throw bridgeValidationError(
      'Anthropic Messages 不能直接承接 OpenAI Responses conversation 状态恢复和写回语义；请移除 conversation，或改用原生 Responses / 网关 Conversation 状态层',
      'openai_anthropic_bridge_conversation_unsupported'
    )
  }
  if (hasMeaningfulField(body, 'background') && body.background !== false) {
    throw bridgeValidationError(
      'Anthropic Messages 不能等价承接 OpenAI Responses background 后台响应语义；请移除 background 或设为 false，或改用原生 Responses 上游',
      'openai_anthropic_bridge_background_unsupported'
    )
  }
  const truncation = normalizedOpenAIEnumValue(body.truncation)
  if (hasMeaningfulField(body, 'truncation') && truncation !== 'disabled') {
    throw bridgeValidationError(
      'Anthropic Messages 不能等价承接 OpenAI Responses truncation=auto 上下文裁剪策略；请移除 truncation 或设为 disabled，或改用原生 Responses 上游',
      'openai_anthropic_bridge_truncation_unsupported'
    )
  }
  if (hasMeaningfulField(body, 'context_management')) {
    throw bridgeValidationError(
      'Anthropic Messages 不能等价承接 OpenAI Responses context_management 上下文管理配置；请移除 context_management，或改用原生 Responses 上游',
      'openai_anthropic_bridge_context_management_unsupported'
    )
  }
}

function validateOpenAIToAnthropicRequestControlOptions(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  body: JsonRecord
): void {
  const serviceTier = normalizedOpenAIEnumValue(body.service_tier)
  if (hasMeaningfulField(body, 'service_tier') && serviceTier !== 'auto' && serviceTier !== 'default') {
    throw bridgeValidationError(
      'Anthropic Messages 不能等价承接 OpenAI service_tier 服务档位；请移除 service_tier 或设为 auto/default，或改用原生 OpenAI 上游',
      'openai_anthropic_bridge_service_tier_unsupported'
    )
  }
  if (hasMeaningfulField(body, 'prompt_cache_retention')) {
    throw bridgeValidationError(
      'Anthropic Messages 不能等价承接 OpenAI prompt_cache_retention 缓存保留策略；请移除 prompt_cache_retention，或改用原生 OpenAI 上游',
      'openai_anthropic_bridge_prompt_cache_retention_unsupported'
    )
  }
  if (hasMeaningfulField(body, 'store') && body.store !== false) {
    throw bridgeValidationError(
      'Anthropic Messages 不能等价承接 OpenAI store=true 存储、后续检索或 distillation/evals 语义；请移除 store 或设为 false，或改用原生 OpenAI 上游',
      'openai_anthropic_bridge_store_unsupported'
    )
  }
  if (sourceEndpointFamily === OPENAI_RESPONSES_FAMILY && hasMeaningfulField(body, 'prompt')) {
    throw bridgeValidationError(
      'Anthropic Messages 不能解析 OpenAI Responses prompt template 引用；请展开为 instructions/input 后重试，或改用原生 Responses 上游',
      'openai_anthropic_bridge_prompt_template_unsupported'
    )
  }
  if (hasMeaningfulField(body, 'moderation')) {
    throw bridgeValidationError(
      'Anthropic Messages 不能等价承接 OpenAI 顶层 moderation 输入 / 输出审核配置；请移除 moderation，或改用具备审核策略的原生 OpenAI 上游',
      'openai_anthropic_bridge_moderation_unsupported'
    )
  }
}

function validateOpenAIToAnthropicOutputShapeOptions(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  body: JsonRecord
): void {
  if (sourceEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY) {
    const choiceCount = integerValue(body.n)
    if (choiceCount !== undefined && choiceCount !== 1) {
      throw bridgeValidationError(
        'Anthropic Messages 单次请求只能等价返回一个 Chat choice；请把 n 设为 1，或改用原生 OpenAI Chat 上游',
        'openai_anthropic_bridge_multiple_choices_unsupported'
      )
    }
    if (body.logprobs === true || requestedPositiveInteger(body.top_logprobs)) {
      throw bridgeValidationError(
        'Anthropic Messages 不能返回 OpenAI Chat token logprobs；请移除 logprobs / top_logprobs，或改用原生 OpenAI Chat 上游',
        'openai_anthropic_bridge_logprobs_unsupported'
      )
    }
    validateOpenAIToAnthropicChatOutputModalities(body)
    return
  }

  if (sourceEndpointFamily !== OPENAI_RESPONSES_FAMILY) return
  const include = Array.isArray(body.include) ? body.include : []
  if (!include.includes('message.output_text.logprobs') && !requestedPositiveInteger(body.top_logprobs)) return
  throw bridgeValidationError(
    'Anthropic Messages 不能返回 OpenAI Responses output_text logprobs；请移除 include=message.output_text.logprobs / top_logprobs，或改用原生 Responses 上游',
    'openai_anthropic_bridge_logprobs_unsupported'
  )
}

function validateOpenAIToAnthropicChatOutputModalities(body: JsonRecord): void {
  const modalities = stringArrayValue(body.modalities)
  const unsupportedModalities = modalities.filter((modality) => modality !== 'text')
  const hasAudioConfig = hasOwn(body, 'audio') && body.audio !== undefined && body.audio !== null
  if (!unsupportedModalities.length && !hasAudioConfig) return
  throw bridgeValidationError(
    'Anthropic Messages 不能返回 OpenAI Chat audio output；请移除 modalities 中的 audio / 其他非 text 输出模态和 audio 配置，或改用原生 OpenAI Chat 上游',
    'openai_anthropic_bridge_output_modality_unsupported'
  )
}

function validateOpenAIToAnthropicSemanticControlOptions(body: JsonRecord): void {
  const samplingControl = unsupportedOpenAISamplingControlName(body)
  if (samplingControl) {
    throw bridgeValidationError(
      `Anthropic Messages 不能等价承接 OpenAI ${samplingControl} 采样控制；请移除该字段，或改用原生 OpenAI 上游`,
      'openai_anthropic_bridge_sampling_control_unsupported'
    )
  }
  if (hasMeaningfulField(body, 'prediction')) {
    throw bridgeValidationError(
      'Anthropic Messages 不能等价承接 OpenAI Predicted Outputs prediction；请移除 prediction，或改用原生 OpenAI 上游',
      'openai_anthropic_bridge_prediction_unsupported'
    )
  }
  if (hasOpenAIVerbosityRequest(body)) {
    throw bridgeValidationError(
      'Anthropic Messages 不能等价承接 OpenAI verbosity 输出详细度控制；请移除 verbosity，或改用原生 OpenAI 上游',
      'openai_anthropic_bridge_verbosity_unsupported'
    )
  }
}

function unsupportedOpenAISamplingControlName(body: JsonRecord): string | undefined {
  const temperature = numberValue(body.temperature)
  if (temperature !== undefined && temperature > 1) return 'temperature>1'
  if (nonDefaultNumber(body.presence_penalty, 0)) return 'presence_penalty'
  if (nonDefaultNumber(body.frequency_penalty, 0)) return 'frequency_penalty'
  if (hasOpenAILogitBiasRequest(body.logit_bias)) return 'logit_bias'
  if (hasMeaningfulField(body, 'seed')) return 'seed'
  return undefined
}

function hasOpenAILogitBiasRequest(value: unknown): boolean {
  if (value === undefined || value === null) return false
  if (isPlainObject(value)) return Object.keys(value).length > 0
  return true
}

function hasOpenAIVerbosityRequest(body: JsonRecord): boolean {
  if (hasMeaningfulField(body, 'verbosity')) return true
  const text = objectValue(body.text)
  return text ? hasMeaningfulField(text, 'verbosity') : false
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
  await applyOpenAIToAnthropicImageGenerationEmulation(sourceEndpointFamily, body, requestPlan, options.imageGenerationExecutor)
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

async function applyOpenAIToAnthropicImageGenerationEmulation(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  body: JsonRecord,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  executor: OpenAIToAnthropicImageGenerationExecutor | undefined
): Promise<void> {
  if (sourceEndpointFamily !== OPENAI_RESPONSES_FAMILY) return
  const tool = responsesImageGenerationToolFromBody(body)
  if (!tool) return
  if (!executor) {
    throw bridgeValidationError(
      'Responses image_generation 需要配置本地图像生成 provider，当前不能桥接到 Anthropic Messages',
      'openai_anthropic_bridge_image_generation_provider_unavailable'
    )
  }
  if (tool.action === 'edit' || tool.inputImageMask || responsesBodyContainsImageGenerationEditContext(body)) {
    throw bridgeValidationError(
      'Responses image_generation 当前只支持无输入图片的 generate 路径，edit / mask / 历史图片复用尚未启用',
      'openai_anthropic_bridge_image_generation_edit_unsupported'
    )
  }
  const prompt = responsesUserQueryFromBody(body)
  if (!prompt) {
    throw bridgeValidationError(
      'Responses image_generation 桥接无法从请求中提取图像提示词',
      'openai_anthropic_bridge_image_generation_missing_prompt'
    )
  }
  requestPlan.imageGeneration = { prompt, tool, executor }
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

function codeInterpreterMockPlanFromResponsesBody(body: JsonRecord): OpenAIToAnthropicCodeInterpreterMockPlan | undefined {
  const tool = responsesCodeInterpreterToolFromBody(body) ?? responsesCodeInterpreterToolChoiceFromBody(body)
  if (!tool) return undefined
  const decision = resolveOpenAIHostedToolRuntimeDecision({
    toolType: stringValue(tool.type) ?? 'code_interpreter',
    sourceEndpointFamily: OPENAI_RESPONSES_FAMILY
  })
  if (decision?.mode !== 'mock') return undefined
  return {
    tool,
    includeOutputs: responseIncludesCodeInterpreterOutputs(body)
  }
}

function mcpMockPlanFromResponsesBody(body: JsonRecord): OpenAIToAnthropicMcpMockPlan | undefined {
  const tool = responsesMcpToolFromBody(body)
  if (!tool) return undefined
  const decision = resolveOpenAIHostedToolRuntimeDecision({
    toolType: 'mcp',
    sourceEndpointFamily: OPENAI_RESPONSES_FAMILY
  })
  if (decision?.mode !== 'mock') return undefined
  return { tool }
}

function mcpProxyPlanFromResponsesBody(body: JsonRecord): OpenAIToAnthropicMcpProxyPlan | undefined {
  const tool = responsesMcpToolFromBody(body)
  if (!tool) return undefined
  const decision = resolveOpenAIHostedToolRuntimeDecision({
    toolType: 'mcp',
    sourceEndpointFamily: OPENAI_RESPONSES_FAMILY
  })
  if (decision?.mode !== 'local_runtime') return undefined
  return { tool }
}

function reasoningEffortFromOpenAIBody(body: JsonRecord): string | undefined {
  const reasoning = objectValue(body.reasoning)
  return normalizedOpenAIEnumValue(reasoning?.effort) ?? normalizedOpenAIEnumValue(body.reasoning_effort)
}

function reasoningSummaryFromOpenAIBody(body: JsonRecord): string | undefined {
  const reasoning = objectValue(body.reasoning)
  return normalizedOpenAIEnumValue(reasoning?.summary)
}

function hasOpenAIReasoningEffortRequest(body: JsonRecord): boolean {
  const reasoning = objectValue(body.reasoning)
  return Boolean(reasoning && hasMeaningfulField(reasoning, 'effort')) || hasMeaningfulField(body, 'reasoning_effort')
}

function hasOpenAIReasoningSummaryRequest(body: JsonRecord): boolean {
  const reasoning = objectValue(body.reasoning)
  return Boolean(reasoning && hasMeaningfulField(reasoning, 'summary'))
}

function normalizedOpenAIEnumValue(value: unknown): string | undefined {
  const text = stringValue(value)
  return text === undefined ? undefined : text.trim().toLowerCase()
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

function responsesImageGenerationToolFromBody(body: JsonRecord): OpenAIToAnthropicImageGenerationToolConfig | undefined {
  const tools = Array.isArray(body.tools) ? body.tools : []
  const tool = tools.find(isOpenAIImageGenerationTool)
  return tool ? imageGenerationToolConfig(tool) : undefined
}

function responsesCodeInterpreterToolFromBody(body: JsonRecord): JsonRecord | undefined {
  const tools = Array.isArray(body.tools) ? body.tools : []
  return tools.find(isOpenAICodeInterpreterTool)
}

function responsesCodeInterpreterToolChoiceFromBody(body: JsonRecord): JsonRecord | undefined {
  const toolChoice = objectValue(body.tool_choice)
  return isOpenAICodeInterpreterTool(toolChoice) ? toolChoice : undefined
}

function responsesMcpToolFromBody(body: JsonRecord): JsonRecord | undefined {
  const tools = Array.isArray(body.tools) ? body.tools : []
  return tools.find(isOpenAIMcpTool)
}

function chatFileSearchToolFromBody(body: JsonRecord): OpenAIToAnthropicFileSearchToolConfig | undefined {
  const tools = Array.isArray(body.tools) ? body.tools : []
  const tool = tools.find(isOpenAIFileSearchTool)
  return tool ? fileSearchToolConfig(tool) : undefined
}

function isOpenAIFileSearchTool(value: unknown): value is JsonRecord {
  return isPlainObject(value) && stringValue(value.type) === 'file_search'
}

function isOpenAIImageGenerationTool(value: unknown): value is JsonRecord {
  return isPlainObject(value) && stringValue(value.type) === 'image_generation'
}

function isOpenAICodeInterpreterTool(value: unknown): value is JsonRecord {
  if (!isPlainObject(value)) return false
  const type = stringValue(value.type)
  return type === 'code_interpreter' || type === 'container'
}

function isOpenAIMcpTool(value: unknown): value is JsonRecord {
  return isPlainObject(value) && stringValue(value.type) === 'mcp'
}

function fileSearchToolConfig(value: JsonRecord): OpenAIToAnthropicFileSearchToolConfig {
  return {
    vectorStoreIds: stringArrayValue(value.vector_store_ids),
    maxNumResults: integerValue(value.max_num_results),
    filters: objectValue(value.filters) ?? objectValue(value.attribute_filter),
    rankingOptions: objectValue(value.ranking_options)
  }
}

function imageGenerationToolConfig(value: JsonRecord): OpenAIToAnthropicImageGenerationToolConfig {
  const action = stringValue(value.action)
  return {
    action: action === 'edit' || action === 'generate' ? action : 'auto',
    size: stringValue(value.size),
    quality: stringValue(value.quality),
    outputFormat: stringValue(value.output_format),
    outputCompression: integerValue(value.output_compression),
    partialImages: integerValue(value.partial_images),
    inputImageMask: objectValue(value.input_image_mask),
    moderation: stringValue(value.moderation),
    background: stringValue(value.background)
  }
}

function responsesBodyContainsImageGenerationEditContext(body: JsonRecord): boolean {
  return responsesInputContainsType(body.input, 'input_image') || responsesInputContainsType(body.input, 'image_generation_call')
}

function responsesInputContainsType(value: unknown, type: string): boolean {
  if (Array.isArray(value)) return value.some((item) => responsesInputContainsType(item, type))
  if (!isPlainObject(value)) return false
  if (stringValue(value.type) === type) return true
  return responsesInputContainsType(value.content, type)
}

function responseIncludesFileSearchResults(body: JsonRecord): boolean {
  return Array.isArray(body.include) && body.include.includes('file_search_call.results')
}

function responseIncludesCodeInterpreterOutputs(body: JsonRecord): boolean {
  return Array.isArray(body.include)
    && body.include.some((item) => typeof item === 'string' && item.trim() === 'code_interpreter_call.outputs')
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

function appendImageGenerationPromptInstruction(
  systemParts: string[],
  imageGeneration: OpenAIToAnthropicImageGenerationPlan | undefined
): void {
  if (!imageGeneration) return
  appendSystemText(systemParts, [
    'The user requested OpenAI Responses image_generation.',
    'Return only one concise, standalone revised image prompt for the image provider.',
    'Do not claim the image was already generated. Do not include Markdown, explanations, JSON, or surrounding quotes.',
    `Original user prompt: ${imageGeneration.prompt}`
  ].join('\n'))
}

function unsupportedOpenAIToolError(tool: unknown, source: 'Chat' | 'Responses'): GatewayRequestValidationError {
  const type = isPlainObject(tool) ? stringValue(tool.type) ?? 'unknown' : 'unknown'
  const detail = openAIHostedToolCompatibilityDetail(type)
  return bridgeValidationError(
    `${source} tool type "${type}" 当前不能直接桥接到 Anthropic Messages：${detail}`,
    'openai_anthropic_bridge_unsupported_hosted_tool'
  )
}

function openAIToAnthropicUnsupportedToolGuidance(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  body: JsonRecord,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  options: OpenAIToAnthropicBridgeBodyOptions,
  response: { model: string; stream: boolean }
): GatewayAgentGuidanceResponse | undefined {
  const rejectedTools = rejectedOpenAIHostedRuntimeLabels(sourceEndpointFamily, body, requestPlan, options)
  if (rejectedTools.length > 0) {
    throw bridgeValidationError(
      `当前配置拒绝执行以下 OpenAI 托管工具：${rejectedTools.join(', ')}`,
      'openai_anthropic_bridge_hosted_tool_runtime_rejected'
    )
  }
  const tools = unsupportedOpenAIHostedToolLabels(sourceEndpointFamily, body, requestPlan, options)
  if (tools.length === 0) return undefined
  return new GatewayAgentGuidanceResponse({
    code: 'agent_guidance_unsupported_hosted_tool',
    protocol: sourceEndpointFamily === OPENAI_RESPONSES_FAMILY ? 'responses' : 'chat_completions',
    stream: response.stream,
    model: response.model,
    message: unsupportedBridgeCapabilityGuidanceMessage({
      tools,
      providerName: options.guidanceProviderName,
      bridgeName: 'OpenAI 到 Anthropic Messages bridge'
    })
  })
}

function openAIToAnthropicCodeInterpreterMockResponse(
  body: JsonRecord,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  response: { model: string; stream: boolean }
): GatewayLocalProtocolResponse | undefined {
  if (!requestPlan.codeInterpreterMock) return undefined
  const payload = responsesCodeInterpreterMockResponsePayload(body, requestPlan, response.model)
  return new GatewayLocalProtocolResponse({
    code: 'openai_anthropic_bridge_code_interpreter_mock_runtime',
    message: 'Responses code_interpreter mock runtime completed',
    body: response.stream ? responsesCodeInterpreterMockSse(payload) : JSON.stringify(payload.response),
    contentType: response.stream
      ? 'text/event-stream; charset=utf-8'
      : 'application/json; charset=utf-8'
  })
}

function responsesCodeInterpreterMockResponsePayload(
  body: JsonRecord,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  model: string
): { response: JsonRecord; codeItem: JsonRecord; messageItem: JsonRecord; text: string } {
  const codeInterpreterMock = requestPlan.codeInterpreterMock
  if (!codeInterpreterMock) {
    throw new Error('code interpreter mock plan is required')
  }
  const suffix = `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`
  const responseId = `resp_ci_mock_${suffix}`
  const createdAt = Math.floor(Date.now() / 1000)
  const text = 'Code interpreter mock runtime completed without executing code. Configure a real sandbox runtime for production code execution.'
  const codeItem: JsonRecord = {
    id: `ci_${safeIdSegment(responseId)}`,
    type: 'code_interpreter_call',
    status: 'completed',
    container_id: mockCodeInterpreterContainerId(codeInterpreterMock.tool),
    code: 'print("juhe-ai code_interpreter mock runtime")',
    outputs: codeInterpreterMock.includeOutputs
      ? [{
          type: 'logs',
          logs: 'juhe-ai code_interpreter mock runtime: execution skipped'
        }]
      : []
  }
  const messageItem: JsonRecord = {
    id: `msg_${safeIdSegment(responseId)}`,
    type: 'message',
    status: 'completed',
    role: 'assistant',
    content: [{
      type: 'output_text',
      text,
      annotations: []
    }]
  }
  return {
    response: {
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
      output: [codeItem, messageItem],
      output_text: text,
      parallel_tool_calls: false,
      previous_response_id: stringValue(body.previous_response_id) ?? null,
      reasoning: {
        effort: requestPlan.reasoningEffort ?? null,
        summary: requestPlan.reasoningSummary ?? null
      },
      store: false,
      temperature: null,
      text: { format: { type: 'text' } },
      tool_choice: 'auto',
      tools: [responsesCodeInterpreterToolSnapshot(codeInterpreterMock.tool)],
      top_p: null,
      truncation: 'disabled',
      usage: zeroResponsesUsage(),
      user: null,
      metadata: {
        gateway_runtime: 'mock',
        gateway_tool: 'code_interpreter'
      }
    },
    codeItem,
    messageItem,
    text
  }
}

function responsesCodeInterpreterMockSse(input: {
  response: JsonRecord
  codeItem: JsonRecord
  messageItem: JsonRecord
  text: string
}): string {
  const response = input.response
  const responseId = stringValue(response.id) ?? ''
  const createdAt = integerValue(response.created_at) ?? Math.floor(Date.now() / 1000)
  const inProgressResponse: JsonRecord = {
    ...response,
    status: 'in_progress',
    completed_at: null,
    output: [],
    output_text: '',
    usage: null
  }
  const codeInProgress: JsonRecord = {
    ...input.codeItem,
    status: 'in_progress',
    outputs: []
  }
  const messageInProgress: JsonRecord = {
    ...input.messageItem,
    status: 'in_progress',
    content: []
  }
  const messageId = stringValue(input.messageItem.id) ?? `msg_${safeIdSegment(responseId)}`
  const outputTextPart: JsonRecord = {
    type: 'output_text',
    text: input.text,
    annotations: []
  }
  return [
    sse('response.created', {
      type: 'response.created',
      response: inProgressResponse
    }),
    sse('response.in_progress', {
      type: 'response.in_progress',
      response: inProgressResponse
    }),
    sse('response.output_item.added', {
      type: 'response.output_item.added',
      output_index: 0,
      item: codeInProgress
    }),
    sse('response.output_item.done', {
      type: 'response.output_item.done',
      output_index: 0,
      item: input.codeItem
    }),
    sse('response.output_item.added', {
      type: 'response.output_item.added',
      output_index: 1,
      item: messageInProgress
    }),
    sse('response.content_part.added', {
      type: 'response.content_part.added',
      item_id: messageId,
      output_index: 1,
      content_index: 0,
      part: { type: 'output_text', text: '', annotations: [] }
    }),
    sse('response.output_text.delta', {
      type: 'response.output_text.delta',
      item_id: messageId,
      output_index: 1,
      content_index: 0,
      delta: input.text
    }),
    sse('response.output_text.done', {
      type: 'response.output_text.done',
      item_id: messageId,
      output_index: 1,
      content_index: 0,
      text: input.text
    }),
    sse('response.content_part.done', {
      type: 'response.content_part.done',
      item_id: messageId,
      output_index: 1,
      content_index: 0,
      part: outputTextPart
    }),
    sse('response.output_item.done', {
      type: 'response.output_item.done',
      output_index: 1,
      item: input.messageItem
    }),
    sse('response.completed', {
      type: 'response.completed',
      response: {
        ...response,
        completed_at: integerValue(response.completed_at) ?? createdAt
      }
    })
  ].join('')
}

function responsesCodeInterpreterToolSnapshot(tool: JsonRecord): JsonRecord {
  const snapshot: JsonRecord = { type: 'code_interpreter' }
  const container = tool.container
  if (typeof container === 'string' && container.trim()) {
    snapshot.container = container.trim() === 'auto' ? { type: 'auto' } : container.trim()
    return snapshot
  }
  snapshot.container = { type: 'auto' }
  return snapshot
}

function mockCodeInterpreterContainerId(tool: JsonRecord): string {
  const container = tool.container
  const base = typeof container === 'string'
    ? container
    : isPlainObject(container)
      ? stringValue(container.id) ?? stringValue(container.container_id) ?? stringValue(container.type) ?? 'auto'
      : 'auto'
  return `cntr_mock_${safeIdSegment(base).slice(0, 32)}`
}

function zeroResponsesUsage(): JsonRecord {
  return {
    input_tokens: 0,
    input_tokens_details: { cached_tokens: 0 },
    output_tokens: 0,
    output_tokens_details: { reasoning_tokens: 0 },
    total_tokens: 0
  }
}

const mcpMockServerLabel = 'mock-mcp'
const mcpMockServerUrl = 'https://mock.mcp.local/mcp'
const mcpMockToolDefinitions: JsonRecord[] = [
  {
    annotations: null,
    description: 'Mock MCP echo tool for juhe-ai protocol regression.',
    input_schema: {
      type: 'object',
      properties: {
        query: { type: 'string' }
      },
      required: ['query'],
      additionalProperties: false
    },
    name: 'echo'
  },
  {
    annotations: null,
    description: 'Mock MCP status tool for juhe-ai protocol regression.',
    input_schema: {
      type: 'object',
      properties: {},
      additionalProperties: false
    },
    name: 'status'
  }
]

async function openAIToAnthropicMcpProxyRuntimeResponse(
  body: JsonRecord,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  executor: OpenAIToAnthropicMcpProxyExecutor | undefined,
  response: { model: string; stream: boolean },
  signal?: AbortSignal
): Promise<GatewayLocalProtocolResponse | undefined> {
  if (!requestPlan.mcpProxy) return undefined
  validateMcpProxyRuntimeDefinitions(body)
  if (
    !response.stream
    && requestPlan.mcpProxy.prepared
    && requestPlan.mcpProxy.executor?.callTool
    && mcpProxyPreparedToolsAreApprovalFree(requestPlan.mcpProxy.tool, requestPlan.mcpProxy.prepared.tools)
  ) {
    return undefined
  }
  if (!executor) {
    throw bridgeValidationError(
      'MCP local_runtime 已启用，但当前网关未配置 MCP proxy executor；请求不会转发给 Anthropic，也不会连接远程 MCP server',
      'openai_anthropic_bridge_mcp_proxy_unavailable',
      503,
      'service_unavailable'
    )
  }
  return executor.run({
    body,
    tool: requestPlan.mcpProxy.tool,
    model: response.model,
    stream: response.stream,
    signal
  })
}

async function prepareOpenAIToAnthropicMcpProxyTools(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  body: JsonRecord,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  executor: OpenAIToAnthropicMcpProxyExecutor | undefined,
  response: { model: string; stream: boolean },
  signal?: AbortSignal
): Promise<void> {
  const mcpProxy = requestPlan.mcpProxy
  if (!mcpProxy || sourceEndpointFamily !== OPENAI_RESPONSES_FAMILY || response.stream) return
  validateMcpProxyRuntimeDefinitions(body)
  if (!executor?.prepare || !executor.callTool) return
  if (!mcpProxyRequestDeclaresModelDrivenApprovalFree(mcpProxy.tool)) return
  const prepared = await executor.prepare({
    body,
    tool: mcpProxy.tool,
    signal
  })
  if (!mcpProxyPreparedToolsAreApprovalFree(mcpProxy.tool, prepared.tools)) return
  const usedNames = existingAnthropicToolNamesFromResponsesBody(body)
  const adaptersByAnthropicName = new Map<string, OpenAIToAnthropicMcpProxyToolAdapter>()
  for (const tool of prepared.tools) {
    const anthropicName = uniqueAnthropicToolName(
      `${prepared.serverLabel}__${tool.name}`,
      usedNames
    )
    adaptersByAnthropicName.set(anthropicName, {
      anthropicName,
      toolName: tool.name,
      serverLabel: prepared.serverLabel,
      description: mcpProxyToolDescription(prepared, tool),
      inputSchema: tool.inputSchema,
      annotations: tool.annotations
    })
  }
  mcpProxy.executor = executor
  mcpProxy.prepared = prepared
  mcpProxy.adaptersByAnthropicName = adaptersByAnthropicName
}

function mcpProxyRequestDeclaresModelDrivenApprovalFree(tool: JsonRecord): boolean {
  if (tool.require_approval === 'never') return true
  const requireApproval = objectValue(tool.require_approval)
  const never = objectValue(requireApproval?.never)
  const approvalFreeTools = new Set(stringArrayValue(never?.tool_names))
  const allowedTools = stringArrayValue(tool.allowed_tools)
  return allowedTools.length > 0 && allowedTools.every((toolName) => approvalFreeTools.has(toolName))
}

function mcpProxyPreparedToolsAreApprovalFree(
  tool: JsonRecord,
  tools: OpenAIToAnthropicMcpProxyToolDefinition[]
): boolean {
  return tools.length > 0 && tools.every((item) => !mcpProxyRequiresApproval(tool, item.name))
}

function mcpProxyRequiresApproval(tool: JsonRecord, toolName: string): boolean {
  const requireApproval = tool.require_approval
  if (requireApproval === 'never') return false
  if (requireApproval === 'always') return true
  const requireApprovalObject = objectValue(requireApproval)
  const never = objectValue(requireApprovalObject?.never)
  if (stringArrayValue(never?.tool_names).includes(toolName)) return false
  return true
}

function existingAnthropicToolNamesFromResponsesBody(body: JsonRecord): Set<string> {
  const usedNames = new Set<string>()
  const tools = Array.isArray(body.tools) ? body.tools : []
  for (const tool of tools) {
    if (!isPlainObject(tool)) continue
    if (tool.type === 'function') {
      const name = stringValue(tool.name)
      if (name) usedNames.add(name)
    }
  }
  return usedNames
}

function mcpProxyToolDescription(
  prepared: OpenAIToAnthropicMcpProxyPreparedServer,
  tool: OpenAIToAnthropicMcpProxyToolDefinition
): string {
  return [
    `OpenAI Responses MCP server: ${prepared.serverLabel}.`,
    tool.description
  ].filter(Boolean).join('\n\n')
}

function validateMcpProxyRuntimeDefinitions(body: JsonRecord): void {
  const tools = Array.isArray(body.tools) ? body.tools.filter(isOpenAIMcpTool) : []
  for (const tool of tools) {
    if (!hasMeaningfulField(tool, 'connector_id')) continue
    const serverLabel = stringValue(tool.server_label) ?? '<missing_server_label>'
    throw bridgeValidationError(
      `MCP tool ${serverLabel} 使用 connector_id；OpenAI connector 需要独立 connector adapter，不能由 remote MCP proxy 伪装支持`,
      'openai_anthropic_bridge_mcp_connector_unsupported'
    )
  }
}

function openAIToAnthropicMcpMockResponse(
  body: JsonRecord,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  response: { model: string; stream: boolean }
): GatewayLocalProtocolResponse | undefined {
  if (!requestPlan.mcpMock) return undefined
  validateMcpMockAllowlistForBody(body)
  const payload = responsesMcpMockResponsePayload(body, requestPlan, response.model)
  return new GatewayLocalProtocolResponse({
    code: 'openai_anthropic_bridge_mcp_mock_runtime',
    message: 'Responses MCP mock proxy completed',
    body: response.stream ? responsesMcpMockSse(payload) : JSON.stringify(payload.response),
    contentType: response.stream
      ? 'text/event-stream; charset=utf-8'
      : 'application/json; charset=utf-8'
  })
}

function responsesMcpMockResponsePayload(
  body: JsonRecord,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  model: string
): { response: JsonRecord; items: JsonRecord[]; text: string } {
  const mcpMock = requestPlan.mcpMock
  if (!mcpMock) {
    throw new Error('mcp mock plan is required')
  }
  const suffix = `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`
  const responseId = `resp_mcp_mock_${suffix}`
  const createdAt = Math.floor(Date.now() / 1000)
  const toolDefinitions = filteredMcpMockToolDefinitions(mcpMock.tool)
  const listToolsItem: JsonRecord = {
    id: `mcpl_${safeIdSegment(responseId)}`,
    type: 'mcp_list_tools',
    server_label: mcpMockServerLabel,
    tools: toolDefinitions
  }
  const items: JsonRecord[] = [listToolsItem]
  let text = ''

  const selectedTool = toolDefinitions[0]
  const selectedToolName = stringValue(selectedTool?.name)
  if (!selectedToolName) {
    text = 'MCP mock proxy found no allowed mock tools.'
    items.push(responsesMcpMockMessageItem(responseId, text))
  } else {
    const approval = mcpApprovalResponseFromResponsesInput(body.input)
    if (approval?.approved === false) {
      text = 'MCP mock proxy tool call was not approved.'
      items.push(responsesMcpMockMessageItem(responseId, text))
    } else if (mcpMockRequiresApproval(mcpMock.tool, selectedToolName) && !approval?.approved) {
      items.push(responsesMcpMockApprovalRequestItem(responseId, selectedToolName))
    } else {
      const callItem = responsesMcpMockCallItem(responseId, selectedToolName, approval?.approvalRequestId)
      items.push(callItem)
      text = 'MCP mock proxy completed without contacting a remote MCP server.'
      items.push(responsesMcpMockMessageItem(responseId, text))
    }
  }

  return {
    response: {
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
      output: items,
      output_text: text,
      parallel_tool_calls: false,
      previous_response_id: stringValue(body.previous_response_id) ?? null,
      reasoning: {
        effort: requestPlan.reasoningEffort ?? null,
        summary: requestPlan.reasoningSummary ?? null
      },
      store: false,
      temperature: null,
      text: { format: { type: 'text' } },
      tool_choice: 'auto',
      tools: [responsesMcpToolSnapshot(mcpMock.tool)],
      top_p: null,
      truncation: 'disabled',
      usage: zeroResponsesUsage(),
      user: null,
      metadata: {
        gateway_runtime: 'mock',
        gateway_tool: 'mcp'
      }
    },
    items,
    text
  }
}

function responsesMcpMockSse(input: { response: JsonRecord; items: JsonRecord[]; text: string }): string {
  const response = input.response
  const inProgressResponse: JsonRecord = {
    ...response,
    status: 'in_progress',
    completed_at: null,
    output: [],
    output_text: '',
    usage: null
  }
  const output: string[] = [
    sse('response.created', {
      type: 'response.created',
      response: inProgressResponse
    }),
    sse('response.in_progress', {
      type: 'response.in_progress',
      response: inProgressResponse
    })
  ]

  for (const [index, item] of input.items.entries()) {
    output.push(sse('response.output_item.added', {
      type: 'response.output_item.added',
      output_index: index,
      item: mcpMockInProgressItem(item)
    }))
    if (item.type === 'message') {
      const itemId = stringValue(item.id) ?? ''
      const content = Array.isArray(item.content) ? item.content.filter(isPlainObject) : []
      const part = content[0] ?? { type: 'output_text', text: '', annotations: [] }
      const text = stringValue(part.text) ?? ''
      output.push(
        sse('response.content_part.added', {
          type: 'response.content_part.added',
          item_id: itemId,
          output_index: index,
          content_index: 0,
          part: { type: 'output_text', text: '', annotations: [] }
        }),
        sse('response.output_text.delta', {
          type: 'response.output_text.delta',
          item_id: itemId,
          output_index: index,
          content_index: 0,
          delta: text
        }),
        sse('response.output_text.done', {
          type: 'response.output_text.done',
          item_id: itemId,
          output_index: index,
          content_index: 0,
          text
        }),
        sse('response.content_part.done', {
          type: 'response.content_part.done',
          item_id: itemId,
          output_index: index,
          content_index: 0,
          part
        })
      )
    }
    output.push(sse('response.output_item.done', {
      type: 'response.output_item.done',
      output_index: index,
      item
    }))
  }

  output.push(sse('response.completed', {
    type: 'response.completed',
    response
  }))
  return output.join('')
}

function validateMcpMockAllowlist(tool: JsonRecord): void {
  const serverLabel = stringValue(tool.server_label)
  const serverUrl = stringValue(tool.server_url)
  if (serverLabel === mcpMockServerLabel && serverUrl === mcpMockServerUrl && !hasMeaningfulField(tool, 'connector_id')) {
    return
  }
  throw bridgeValidationError(
    `MCP mock proxy 只允许 server_label=${mcpMockServerLabel} 且 server_url=${mcpMockServerUrl}；真实远程 MCP 需要后续 allowlist / auth / approval runtime`,
    'openai_anthropic_bridge_mcp_mock_server_not_allowed'
  )
}

function validateMcpMockAllowlistForBody(body: JsonRecord): void {
  const tools = Array.isArray(body.tools) ? body.tools.filter(isOpenAIMcpTool) : []
  for (const tool of tools) {
    validateMcpMockAllowlist(tool)
  }
}

function filteredMcpMockToolDefinitions(tool: JsonRecord): JsonRecord[] {
  const allowed = stringArrayValue(tool.allowed_tools)
  if (!allowed.length) return mcpMockToolDefinitions.map((definition) => ({ ...definition }))
  const allowedSet = new Set(allowed)
  return mcpMockToolDefinitions
    .filter((definition) => {
      const name = stringValue(definition.name)
      return name ? allowedSet.has(name) : false
    })
    .map((definition) => ({ ...definition }))
}

function responsesMcpMockCallItem(
  responseId: string,
  toolName: string,
  approvalRequestId?: string
): JsonRecord {
  return {
    id: `mcp_${safeIdSegment(responseId)}`,
    type: 'mcp_call',
    approval_request_id: approvalRequestId ?? null,
    arguments: JSON.stringify({ query: 'juhe-ai mcp mock runtime' }),
    error: null,
    name: toolName,
    output: JSON.stringify({
      ok: true,
      message: 'juhe-ai mcp mock runtime: remote MCP call skipped'
    }),
    server_label: mcpMockServerLabel
  }
}

function responsesMcpMockApprovalRequestItem(responseId: string, toolName: string): JsonRecord {
  return {
    id: `mcpr_${safeIdSegment(responseId)}`,
    type: 'mcp_approval_request',
    arguments: JSON.stringify({ query: 'juhe-ai mcp mock runtime' }),
    name: toolName,
    server_label: mcpMockServerLabel
  }
}

function responsesMcpMockMessageItem(responseId: string, text: string): JsonRecord {
  return {
    id: `msg_${safeIdSegment(responseId)}`,
    type: 'message',
    status: 'completed',
    role: 'assistant',
    content: [{
      type: 'output_text',
      text,
      annotations: []
    }]
  }
}

function mcpMockInProgressItem(item: JsonRecord): JsonRecord {
  if (item.type !== 'message') return item
  return {
    ...item,
    status: 'in_progress',
    content: []
  }
}

function mcpMockRequiresApproval(tool: JsonRecord, toolName: string): boolean {
  const requireApproval = tool.require_approval
  if (requireApproval === 'never') return false
  if (requireApproval === 'always') return true
  const requireApprovalObject = objectValue(requireApproval)
  const never = objectValue(requireApprovalObject?.never)
  if (stringArrayValue(never?.tool_names).includes(toolName)) return false
  return true
}

function mcpApprovalResponseFromResponsesInput(value: unknown): { approved: boolean; approvalRequestId?: string } | undefined {
  const items = Array.isArray(value)
    ? value
    : isPlainObject(value) ? [value] : []
  for (let index = items.length - 1; index >= 0; index -= 1) {
    const item = items[index]
    if (!isPlainObject(item)) continue
    if (stringValue(item.type) !== 'mcp_approval_response') continue
    return {
      approved: item.approve === true,
      approvalRequestId: stringValue(item.approval_request_id)
    }
  }
  return undefined
}

function responsesMcpToolSnapshot(tool: JsonRecord): JsonRecord {
  const snapshot: JsonRecord = {
    type: 'mcp',
    server_label: mcpMockServerLabel,
    server_url: mcpMockServerUrl
  }
  const serverDescription = stringValue(tool.server_description)
  if (serverDescription) snapshot.server_description = serverDescription
  const allowedTools = stringArrayValue(tool.allowed_tools)
  if (allowedTools.length) snapshot.allowed_tools = allowedTools
  if (tool.require_approval !== undefined) snapshot.require_approval = tool.require_approval
  if (tool.defer_loading === true) snapshot.defer_loading = true
  return snapshot
}

function rejectedOpenAIHostedRuntimeLabels(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  body: JsonRecord,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  options: OpenAIToAnthropicBridgeBodyOptions
): string[] {
  const labels: string[] = []
  const tools = Array.isArray(body.tools) ? body.tools : []
  for (const tool of tools) {
    const label = rejectedOpenAIHostedRuntimeLabel(sourceEndpointFamily, body, tool, requestPlan, options)
    if (label) labels.push(label)
  }
  const toolChoice = objectValue(body.tool_choice)
  const choiceType = stringValue(toolChoice?.type)
  if (choiceType && choiceType !== 'function' && choiceType !== 'allowed_tools') {
    const label = rejectedOpenAIHostedRuntimeLabel(sourceEndpointFamily, body, toolChoice, requestPlan, options)
    if (label) labels.push(label)
  }
  return [...new Set(labels)]
}

function rejectedOpenAIHostedRuntimeLabel(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  body: JsonRecord,
  tool: unknown,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  options: OpenAIToAnthropicBridgeBodyOptions
): string | undefined {
  if (!isPlainObject(tool)) return undefined
  const type = stringValue(tool.type)
  if (!type || type === 'function') return undefined
  if (isOpenAIFileSearchTool(tool) && requestPlan.fileSearch) return undefined
  if (isOpenAIImageGenerationTool(tool) && sourceEndpointFamily === OPENAI_RESPONSES_FAMILY) {
    const config = imageGenerationToolConfig(tool)
    const supportedGenerate = Boolean(options.imageGenerationExecutor)
      && config.action !== 'edit'
      && !config.inputImageMask
      && !responsesBodyContainsImageGenerationEditContext(body)
    if (supportedGenerate) return undefined
  }
  const decision = resolveOpenAIHostedToolRuntimeDecision({ toolType: type, sourceEndpointFamily })
  return decision?.mode === 'reject' ? decision.toolType : undefined
}

function unsupportedOpenAIHostedToolLabels(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  body: JsonRecord,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  options: OpenAIToAnthropicBridgeBodyOptions
): string[] {
  const labels: string[] = []
  const tools = Array.isArray(body.tools) ? body.tools : []
  for (const tool of tools) {
    const label = unsupportedOpenAIHostedToolLabel(sourceEndpointFamily, body, tool, requestPlan, options)
    if (label) labels.push(label)
  }
  const toolChoice = objectValue(body.tool_choice)
  const choiceType = stringValue(toolChoice?.type)
  if (choiceType && choiceType !== 'function' && choiceType !== 'allowed_tools') {
    const label = unsupportedOpenAIHostedToolLabel(sourceEndpointFamily, body, toolChoice, requestPlan, options)
    if (label) labels.push(label)
  }
  return [...new Set(labels)]
}

function unsupportedOpenAIHostedToolLabel(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  body: JsonRecord,
  tool: unknown,
  requestPlan: OpenAIToAnthropicBridgeRequestPlan,
  options: OpenAIToAnthropicBridgeBodyOptions
): string | undefined {
  if (!isPlainObject(tool)) return undefined
  const type = stringValue(tool.type)
  if (!type || type === 'function') return undefined
  if (isOpenAIFileSearchTool(tool) && requestPlan.fileSearch) return undefined
  if (isOpenAIImageGenerationTool(tool) && sourceEndpointFamily === OPENAI_RESPONSES_FAMILY) {
    const config = imageGenerationToolConfig(tool)
    const supportedGenerate = Boolean(options.imageGenerationExecutor)
      && config.action !== 'edit'
      && !config.inputImageMask
      && !responsesBodyContainsImageGenerationEditContext(body)
    return supportedGenerate ? undefined : type
  }
  if (sourceEndpointFamily === OPENAI_RESPONSES_FAMILY && isResponsesNamespaceTool(tool) && requestPlan.responsesToolSearch) {
    return undefined
  }
  if (sourceEndpointFamily === OPENAI_RESPONSES_FAMILY && isResponsesToolSearchTool(tool) && requestPlan.responsesToolSearch) {
    return undefined
  }
  if (sourceEndpointFamily === OPENAI_RESPONSES_FAMILY && isOpenAICodeInterpreterTool(tool) && requestPlan.codeInterpreterMock) {
    return undefined
  }
  if (sourceEndpointFamily === OPENAI_RESPONSES_FAMILY && isOpenAIMcpTool(tool) && requestPlan.mcpMock) {
    const isSelectedMockTool = requestPlan.mcpMock.tool === tool
    const isMcpToolChoice = !hasMeaningfulField(tool, 'server_label')
      && !hasMeaningfulField(tool, 'server_url')
      && !hasMeaningfulField(tool, 'connector_id')
    if (isSelectedMockTool || isMcpToolChoice) return undefined
  }
  if (sourceEndpointFamily === OPENAI_RESPONSES_FAMILY && isOpenAIMcpTool(tool) && requestPlan.mcpProxy) {
    return undefined
  }
  const runtimeDecision = resolveOpenAIHostedToolRuntimeDecision({ toolType: type, sourceEndpointFamily })
  if (runtimeDecision) return runtimeDecision.mode === 'reject' ? undefined : runtimeDecision.toolType
  return type
}

function unsupportedBridgeCapabilityGuidanceMessage(input: {
  tools: string[]
  providerName?: string
  bridgeName: string
}): string {
  const tools = input.tools.join(', ')
  const provider = input.providerName?.trim() || '当前上游供应商'
  const providerSpecificHint = provider.toLowerCase() === 'glm'
    ? '\n供应商提示：GLM 的联网搜索通常应通过该供应商提供的官方 MCP 或等价本地工具配置来完成。'
    : ''
  return [
    `能力未执行：${tools}`,
    '',
    `当前供应商：${provider}`,
    `当前协议：${input.bridgeName}`,
    `原因：当前上游供应商或协议档案未声明这些原生托管能力。中转层不会伪造工具结果，也不会把这些工具请求透传给不支持的上游。${providerSpecificHint}`,
    '',
    '建议下一步：',
    '1. 检查本地客户端是否已配置该供应商提供的 MCP、图像生成 provider、沙箱或等价工具。',
    '2. 如果已配置，请通过本地 MCP/工具执行所需能力后继续当前任务。',
    '3. 如果未配置，请提示用户配置对应工具，或切换到支持该能力的供应商/模型。',
    '',
    `注意：本轮没有执行 ${tools}，因此没有外部工具结果。`
  ].join('\n')
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
  const runtimeDetail = openAIHostedToolRuntimeCompatibilityDetail(type)
  if (runtimeDetail) return runtimeDetail
  return '需要先在高兼容能力矩阵中定义映射、模拟或 agent guidance 策略'
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
  const payload = await anthropicMessageToResponsesResponse(message, {
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
  if (options.requestPlan?.imageGeneration) {
    yield * transformAnthropicMessagesSseToResponsesImageGenerationSse(body, options)
    return
  }
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

async function * transformAnthropicMessagesSseToResponsesImageGenerationSse(
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
    for (const output of processAnthropicEventAsResponsesImageGenerationPrompt(state, event)) {
      yield Buffer.from(output, 'utf8')
    }
    if (state.terminalReceived && !state.completed && !state.failed) {
      for await (const output of completeResponsesImageGenerationStream(state)) {
        yield Buffer.from(output, 'utf8')
      }
      await notifyResponsesCompletion(state, options.onResponsesCompleted)
    }
  }
  if (!state.completed && !state.failed) {
    for (const output of failResponsesStream(state, {
      message: '上游 Anthropic Messages SSE 在图像提示词完成前中断',
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

async function anthropicMessageToResponsesResponse(
  message: JsonRecord,
  options: { model: string; requestPlan?: OpenAIToAnthropicBridgeRequestPlan; previousResponseId?: string }
): Promise<JsonRecord> {
  const model = stringValue(message.model) ?? options.model
  const responseId = responseIdFromAnthropicId(stringValue(message.id))
  const createdAt = Math.floor(Date.now() / 1000)
  const contentBlocks = Array.isArray(message.content) ? message.content : []
  if (options.requestPlan?.imageGeneration) {
    return await anthropicMessageToResponsesImageGenerationResponse({
      contentBlocks,
      createdAt,
      message,
      model,
      previousResponseId: options.previousResponseId,
      requestPlan: options.requestPlan,
      responseId
    })
  }
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
  const output = await anthropicContentBlocksToResponsesOutputItems(
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
      summary: options.requestPlan?.reasoningSummary ?? null
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
    metadata: responsesBridgeMetadata(options.requestPlan)
  }
}

function responsesBridgeMetadata(requestPlan?: OpenAIToAnthropicBridgeRequestPlan): JsonRecord {
  if (requestPlan?.mcpProxy?.prepared) {
    return {
      gateway_runtime: 'local_runtime',
      gateway_tool: 'mcp'
    }
  }
  return {}
}

async function anthropicContentBlocksToResponsesOutputItems(
  blocks: unknown[],
  responseId: string,
  requestPlan?: OpenAIToAnthropicBridgeRequestPlan,
  structuredOutputText?: string
): Promise<JsonRecord[]> {
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
        if (thinkingText && shouldEmitResponsesReasoningItem(requestPlan)) {
          output.push(responsesReasoningItem(responseId, output.length, thinkingText))
        }
        continue
      }
      if (block.type === 'tool_use') {
        const callId = stringValue(block.id) ?? `call_${output.length}`
        output.push(await responsesOutputItemFromAnthropicToolUseBlock({
          responseId,
          idSegment: stringValue(block.id) ?? `${responseId}_${output.length}`,
          status: 'completed',
          callId,
          name: stringValue(block.name) ?? '',
          input: isPlainObject(block.input) ? block.input : {}
        }, requestPlan))
      }
    }
  }
  const shouldAppendMessage = messageText || output.length === 0 || responsesOutputHasMcpProxyCall(output)
  if (shouldAppendMessage) {
    const text = messageText || 'MCP proxy completed remote tool call.'
    const item: JsonRecord = {
      id: `msg_${safeIdSegment(responseId)}_${textIndex++}`,
      type: 'message',
      status: 'completed',
      role: 'assistant',
      content: [{
        type: 'output_text',
        text,
        annotations: localToolAnnotationsForText(text, requestPlan)
      }]
    }
    if (messageText) {
      output.unshift(item)
    } else {
      output.push(item)
    }
  }
  return output
}

function responsesOutputHasMcpProxyCall(output: JsonRecord[]): boolean {
  return output.some((item) => item.type === 'mcp_call')
}

async function responsesOutputItemFromAnthropicToolUseBlock(
  input: {
    responseId: string
    idSegment: string
    callId: string
    name: string
    input: JsonRecord
    status: 'in_progress' | 'completed'
  },
  requestPlan?: OpenAIToAnthropicBridgeRequestPlan
): Promise<JsonRecord> {
  const mcpAdapter = requestPlan?.mcpProxy?.adaptersByAnthropicName?.get(input.name)
  const mcpPrepared = requestPlan?.mcpProxy?.prepared
  const mcpExecutor = requestPlan?.mcpProxy?.executor
  if (mcpAdapter && mcpPrepared && mcpExecutor?.callTool) {
    const callResult = await mcpExecutor.callTool({
      prepared: mcpPrepared,
      toolName: mcpAdapter.toolName,
      arguments: input.input
    })
    const item: JsonRecord = {
      id: `mcp_${safeIdSegment(input.idSegment)}`,
      type: 'mcp_call',
      approval_request_id: null,
      arguments: JSON.stringify(input.input),
      error: null,
      name: mcpAdapter.toolName,
      output: callResult.outputText,
      server_label: mcpAdapter.serverLabel
    }
    if (callResult.truncated || callResult.metadata) {
      item.metadata = {
        ...(callResult.metadata ?? {}),
        ...(callResult.truncated ? { output_truncated: true } : {})
      }
    }
    return item
  }
  return responsesFunctionCallItemFromAnthropicTool({
    idSegment: input.idSegment,
    status: input.status,
    callId: input.callId,
    name: input.name,
    argumentsText: JSON.stringify(input.input)
  }, requestPlan)
}

function shouldEmitResponsesReasoningItem(requestPlan?: OpenAIToAnthropicBridgeRequestPlan): boolean {
  return requestPlan?.reasoningSummary !== 'none'
}

function shouldEmitResponsesStreamBlock(state: AnthropicStreamState, block: AnthropicStreamBlockState): boolean {
  return block.type !== 'thinking' || shouldEmitResponsesReasoningItem(state.requestPlan)
}

async function anthropicMessageToResponsesImageGenerationResponse(input: {
  contentBlocks: unknown[]
  createdAt: number
  message: JsonRecord
  model: string
  previousResponseId?: string
  requestPlan: OpenAIToAnthropicBridgeRequestPlan
  responseId: string
}): Promise<JsonRecord> {
  const imageGeneration = input.requestPlan.imageGeneration
  if (!imageGeneration) {
    throw new Error('image generation plan is required')
  }
  const revisedPrompt = imageGenerationRevisedPromptFromAnthropicBlocks(input.contentBlocks, imageGeneration.prompt)
  let item: JsonRecord
  try {
    const result = await imageGeneration.executor.generate({
      prompt: revisedPrompt,
      tool: imageGeneration.tool
    })
    item = responsesImageGenerationCallItem(imageGeneration, input.responseId, result, revisedPrompt)
  } catch (error) {
    return failedResponsesResponseFromStructuredOutputError({
      responseId: input.responseId,
      createdAt: input.createdAt,
      model: input.model,
      previousResponseId: input.previousResponseId,
      requestPlan: input.requestPlan,
      usage: objectValue(input.message.usage),
      error: openAIErrorObjectFromBridgeError(error, '图像生成 provider 执行失败'),
      metadata: {
        gateway_generated_failure: true,
        gateway_failure_source: 'image_generation_provider'
      }
    })
  }
  return {
    id: input.responseId,
    object: 'response',
    created_at: input.createdAt,
    status: 'completed',
    completed_at: input.createdAt,
    error: null,
    incomplete_details: null,
    instructions: null,
    max_output_tokens: null,
    model: input.model,
    output: [item],
    output_text: '',
    parallel_tool_calls: false,
    previous_response_id: input.previousResponseId ?? null,
    reasoning: {
      effort: input.requestPlan.reasoningEffort ?? null,
      summary: input.requestPlan.reasoningSummary ?? null
    },
    store: false,
    temperature: null,
    text: { format: { type: 'text' } },
    tool_choice: 'auto',
    tools: [responsesImageGenerationToolSnapshot(imageGeneration.tool)],
    top_p: null,
    truncation: 'disabled',
    usage: anthropicUsageToResponsesUsage(objectValue(input.message.usage)),
    user: null,
    metadata: {}
  }
}

function imageGenerationRevisedPromptFromAnthropicBlocks(blocks: unknown[], fallback: string): string {
  const text = normalizeWhitespace(blocks.map(anthropicTextFromBlock).filter(Boolean).join('\n'))
  return text || fallback
}

function responsesImageGenerationCallItem(
  imageGeneration: OpenAIToAnthropicImageGenerationPlan,
  responseId: string,
  result: OpenAIToAnthropicImageGenerationResult,
  fallbackRevisedPrompt: string
): JsonRecord {
  const itemId = ensureResponsesImageGenerationOutputItemId(imageGeneration, responseId)
  const item: JsonRecord = {
    id: itemId,
    type: 'image_generation_call',
    status: 'completed',
    revised_prompt: result.revisedPrompt ?? fallbackRevisedPrompt,
    result: result.imageBase64
  }
  if (result.outputFormat) item.output_format = result.outputFormat
  return item
}

function responsesImageGenerationInProgressCallItem(
  imageGeneration: OpenAIToAnthropicImageGenerationPlan,
  responseId: string,
  revisedPrompt: string
): JsonRecord {
  const item: JsonRecord = {
    id: ensureResponsesImageGenerationOutputItemId(imageGeneration, responseId),
    type: 'image_generation_call',
    status: 'in_progress',
    revised_prompt: revisedPrompt
  }
  if (imageGeneration.tool.outputFormat) item.output_format = imageGeneration.tool.outputFormat
  return item
}

function ensureResponsesImageGenerationOutputItemId(
  imageGeneration: OpenAIToAnthropicImageGenerationPlan,
  responseId: string
): string {
  const itemId = imageGeneration.outputItemId ?? `ig_${safeIdSegment(responseId)}`
  imageGeneration.outputItemId = itemId
  return itemId
}

function responsesImageGenerationToolSnapshot(tool: OpenAIToAnthropicImageGenerationToolConfig): JsonRecord {
  const snapshot: JsonRecord = { type: 'image_generation' }
  if (tool.action !== 'auto') snapshot.action = tool.action
  if (tool.size) snapshot.size = tool.size
  if (tool.quality) snapshot.quality = tool.quality
  if (tool.outputFormat) snapshot.output_format = tool.outputFormat
  if (tool.outputCompression !== undefined) snapshot.output_compression = tool.outputCompression
  if (tool.partialImages !== undefined) snapshot.partial_images = tool.partialImages
  if (tool.moderation) snapshot.moderation = tool.moderation
  if (tool.background) snapshot.background = tool.background
  return snapshot
}

function openAIErrorObjectFromBridgeError(error: unknown, fallbackMessage: string): JsonRecord {
  if (error instanceof GatewayRequestValidationError) {
    return {
      message: error.message,
      type: error.type,
      code: error.code
    }
  }
  const imageGenerationProviderError = imageGenerationProviderErrorObject(error)
  if (imageGenerationProviderError) return imageGenerationProviderError
  return {
    message: error instanceof Error ? error.message : fallbackMessage,
    type: 'upstream_error',
    code: 'openai_anthropic_bridge_image_generation_execution_failed'
  }
}

function imageGenerationProviderErrorObject(error: unknown): JsonRecord | undefined {
  if (typeof error !== 'object' || error === null) return undefined
  const value = error as {
    message?: unknown
    type?: unknown
    code?: unknown
    moderationDetails?: unknown
  }
  const code = stringValue(value.code)
  const type = stringValue(value.type)
  const moderationDetails = objectValue(value.moderationDetails)
  if (!code && !type && !moderationDetails) return undefined
  const output: JsonRecord = {
    message: stringValue(value.message) ?? '图像生成 provider 执行失败',
    type: type ?? 'upstream_error',
    code: code ?? 'openai_anthropic_bridge_image_generation_provider_error'
  }
  if (moderationDetails) output.moderation_details = moderationDetails
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
    assignToolCallIndex(state, block)
    state.blocks.set(index, block)
    output.push(...ensureChatRoleChunk(state))
    if (block.type === 'tool_use' && !isStructuredOutputStreamBlock(state, block)) {
      output.push(chatSseChunk(state, {
        tool_calls: [{
          index: block.toolCallIndex ?? 0,
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
            index: block.toolCallIndex ?? 0,
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
    assignToolCallIndex(state, block)
    state.blocks.set(index, block)
    output.push(...ensureResponsesStreamStarted(state))
    if (!shouldEmitResponsesStreamBlock(state, block)) return output
    block.outputIndex = state.nextOutputIndex++
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
        item: responsesFunctionCallItemFromAnthropicTool({
          idSegment: block.id ?? `${index}`,
          status: 'in_progress',
          callId: block.id ?? `call_${index}`,
          name: block.name ?? '',
          argumentsText: ''
        }, state.requestPlan)
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

function processAnthropicEventAsResponsesImageGenerationPrompt(state: AnthropicStreamState, event: ParsedSseEvent): string[] {
  if (state.completed || state.failed) return []
  if (event.eventName === 'error' || event.data?.type === 'error') {
    const payload = openAIErrorFromAnthropicPayload(event.data)
    return failResponsesStream(state, objectValue(payload.error) ?? {
      message: '上游 Anthropic Messages 图像提示词流式响应失败',
      type: 'upstream_error',
      code: 'upstream_error'
    })
  }
  if (event.dataParseError) {
    return failResponsesStream(state, {
      message: '上游 Anthropic Messages SSE 返回了无法解析的图像提示词事件',
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
    assignToolCallIndex(state, block)
    state.blocks.set(index, block)
    return output
  }
  if (data.type === 'content_block_delta') {
    const index = integerValue(data.index) ?? 0
    const block = state.blocks.get(index)
    const delta = objectValue(data.delta) ?? {}
    if (delta.type === 'text_delta') {
      if (block) block.text += stringValue(delta.text) ?? ''
    } else if (delta.type === 'thinking_delta') {
      if (block) block.text += stringValue(delta.thinking) ?? stringValue(delta.text) ?? ''
    } else if (delta.type === 'input_json_delta') {
      if (block) block.inputJson += stringValue(delta.partial_json) ?? ''
    }
    return output
  }
  if (data.type === 'content_block_stop') {
    const index = integerValue(data.index) ?? 0
    const block = state.blocks.get(index)
    if (block) block.done = true
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
  }
  return output
}

async function * completeResponsesImageGenerationStream(state: AnthropicStreamState): AsyncIterable<string> {
  if (state.completed || state.failed) return
  const imageGeneration = state.requestPlan?.imageGeneration
  if (!imageGeneration) {
    yield * failResponsesStream(state, {
      message: '图像生成请求缺少本地 provider 计划',
      type: 'invalid_request_error',
      code: 'openai_anthropic_bridge_image_generation_plan_missing'
    })
    return
  }
  const revisedPrompt = imageGenerationRevisedPromptFromStreamState(state, imageGeneration.prompt)
  if (shouldStreamImageGenerationProvider(imageGeneration)) {
    yield * completeResponsesImageGenerationProviderStream(state, imageGeneration, revisedPrompt)
    return
  }
  let item: JsonRecord
  try {
    const result = await imageGeneration.executor.generate({
      prompt: revisedPrompt,
      tool: imageGeneration.tool
    })
    item = responsesImageGenerationCallItem(imageGeneration, state.responseId, result, revisedPrompt)
  } catch (error) {
    yield * failResponsesStream(state, openAIErrorObjectFromBridgeError(error, '图像生成 provider 执行失败'))
    return
  }
  const outputIndex = state.nextOutputIndex++
  state.outputItems.push(item)
  state.completed = true
  const inProgressItem: JsonRecord = { ...item, status: 'in_progress' }
  delete inProgressItem.result
  yield * [
    ...ensureResponsesStreamStarted(state),
    sse('response.output_item.added', {
      type: 'response.output_item.added',
      output_index: outputIndex,
      item: inProgressItem
    }),
    sse('response.image_generation_call.completed', {
      type: 'response.image_generation_call.completed',
      output_index: outputIndex,
      item_id: stringValue(item.id) ?? '',
      result: stringValue(item.result) ?? null
    }),
    sse('response.output_item.done', {
      type: 'response.output_item.done',
      output_index: outputIndex,
      item
    }),
    sse('response.completed', {
      type: 'response.completed',
      response: responseSnapshot(state, 'completed')
    })
  ]
}

function shouldStreamImageGenerationProvider(imageGeneration: OpenAIToAnthropicImageGenerationPlan): boolean {
  return typeof imageGeneration.tool.partialImages === 'number'
    && typeof imageGeneration.executor.generateStream === 'function'
}

async function * completeResponsesImageGenerationProviderStream(
  state: AnthropicStreamState,
  imageGeneration: OpenAIToAnthropicImageGenerationPlan,
  revisedPrompt: string
): AsyncIterable<string> {
  const itemId = ensureResponsesImageGenerationOutputItemId(imageGeneration, state.responseId)
  const outputIndex = state.nextOutputIndex++
  yield * [
    ...ensureResponsesStreamStarted(state),
    sse('response.output_item.added', {
      type: 'response.output_item.added',
      output_index: outputIndex,
      item: responsesImageGenerationInProgressCallItem(imageGeneration, state.responseId, revisedPrompt)
    })
  ]

  let finalResult: OpenAIToAnthropicImageGenerationResult | undefined
  try {
    const stream = imageGeneration.executor.generateStream?.({
      prompt: revisedPrompt,
      tool: imageGeneration.tool
    })
    if (!stream) {
      throw bridgeValidationError(
        '图像生成 provider 不支持 streaming partial image',
        'openai_anthropic_bridge_image_generation_provider_stream_unavailable',
        502,
        'upstream_error'
      )
    }
    for await (const event of stream) {
      if (event.type === 'partial_image') {
        const payload: JsonRecord = {
          type: 'response.image_generation_call.partial_image',
          output_index: outputIndex,
          item_id: itemId,
          partial_image_b64: event.partial.imageBase64
        }
        if (event.partial.partialImageIndex !== undefined) payload.partial_image_index = event.partial.partialImageIndex
        yield sse('response.image_generation_call.partial_image', payload)
      } else {
        finalResult = event.result
      }
    }
  } catch (error) {
    yield * failResponsesStream(state, openAIErrorObjectFromBridgeError(error, '图像生成 provider streaming 执行失败'))
    return
  }

  if (!finalResult) {
    yield * failResponsesStream(state, {
      message: '图像生成 provider streaming 响应缺少最终图片结果',
      type: 'upstream_error',
      code: 'openai_anthropic_bridge_image_generation_provider_invalid_response'
    })
    return
  }

  const item = responsesImageGenerationCallItem(imageGeneration, state.responseId, finalResult, revisedPrompt)
  state.outputItems.push(item)
  state.completed = true
  yield * [
    sse('response.image_generation_call.completed', {
      type: 'response.image_generation_call.completed',
      output_index: outputIndex,
      item_id: stringValue(item.id) ?? itemId,
      result: stringValue(item.result) ?? null
    }),
    sse('response.output_item.done', {
      type: 'response.output_item.done',
      output_index: outputIndex,
      item
    }),
    sse('response.completed', {
      type: 'response.completed',
      response: responseSnapshot(state, 'completed')
    })
  ]
}

function imageGenerationRevisedPromptFromStreamState(state: AnthropicStreamState, fallback: string): string {
  const blocks = [...state.blocks.values()].sort((left, right) => left.index - right.index)
  const text = normalizeWhitespace(blocks.map((block) => block.text).filter(Boolean).join('\n'))
  return text || fallback
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
  const usage = state.usage ?? estimatedChatUsageFromStreamState(state)
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
    usage: null
  }))
  if (state.requestPlan?.chatStreamIncludeUsage) {
    output.push(chatSseData({
      id: state.chatId,
      object: 'chat.completion.chunk',
      created: state.createdAt,
      model: state.model,
      choices: [],
      usage
    }))
  }
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
    if (!shouldEmitResponsesReasoningItem(state.requestPlan)) return []
    const item = responsesReasoningItem(state.responseId, block.index, block.text)
    state.outputItems.push(item)
    return [sse('response.output_item.done', {
      type: 'response.output_item.done',
      output_index: block.outputIndex ?? 0,
      item
    })]
  }
  if (block.type === 'tool_use') {
    const item = responsesFunctionCallItemFromAnthropicTool({
      idSegment: block.id ?? `${block.index}`,
      status: 'completed',
      callId: block.id ?? `call_${block.index}`,
      name: block.name ?? '',
      argumentsText: block.inputJson || '{}'
    }, state.requestPlan)
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
      summary: state.requestPlan?.reasoningSummary ?? null
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
    nextToolCallIndex: 0,
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
    inputJson: initialToolInputJsonFromContentBlock(block),
    done: false
  }
}

function initialToolInputJsonFromContentBlock(block: JsonRecord): string {
  if (block.type !== 'tool_use' || !isPlainObject(block.input)) return ''
  return Object.keys(block.input).length ? JSON.stringify(block.input) : ''
}

function assignToolCallIndex(state: AnthropicStreamState, block: AnthropicStreamBlockState): void {
  if (block.type !== 'tool_use') return
  block.toolCallIndex = state.nextToolCallIndex++
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
    responsesMcpProxyListToolsItem(requestPlan, responseId),
    responsesFileSearchCallItem(requestPlan, responseId)
  ].filter((item): item is JsonRecord => Boolean(item))
}

function responsesMcpProxyListToolsItem(
  requestPlan: OpenAIToAnthropicBridgeRequestPlan | undefined,
  responseId: string
): JsonRecord | undefined {
  const mcpProxy = requestPlan?.mcpProxy
  if (!mcpProxy?.prepared || mcpProxy.emittedListTools) return undefined
  mcpProxy.emittedListTools = true
  return {
    id: `mcpl_${safeIdSegment(responseId)}`,
    type: 'mcp_list_tools',
    server_label: mcpProxy.prepared.serverLabel,
    tools: mcpProxy.prepared.tools.map((tool) => ({
      annotations: tool.annotations ?? null,
      description: tool.description ?? '',
      input_schema: tool.inputSchema,
      name: tool.name
    }))
  }
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
  metadata?: JsonRecord
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
      summary: input.requestPlan?.reasoningSummary ?? null
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
    metadata: input.metadata ?? {}
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

function anthropicToolInputFromOpenAIArguments(value: unknown): JsonRecord {
  if (isPlainObject(value)) return value
  if (typeof value !== 'string' || !value.trim()) return {}
  try {
    const parsed = JSON.parse(value) as unknown
    return isPlainObject(parsed)
      ? parsed
      : { openai_arguments: parsed }
  } catch {
    return { openai_arguments_text: value }
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

function nonDefaultNumber(value: unknown, defaultValue: number): boolean {
  const number = numberValue(value)
  return number !== undefined && number !== defaultValue
}

function integerValue(value: unknown): number | undefined {
  const number = typeof value === 'number'
    ? value
    : typeof value === 'string' && value.trim() ? Number(value) : NaN
  if (!Number.isFinite(number)) return undefined
  return Math.trunc(number)
}

function requestedPositiveInteger(value: unknown): boolean {
  const integer = integerValue(value)
  return integer !== undefined && integer > 0
}

function hasMeaningfulField(value: JsonRecord, key: string): boolean {
  return hasOwn(value, key) && value[key] !== undefined && value[key] !== null
}

function hasMeaningfulValue(value: unknown): boolean {
  return value !== undefined && value !== null
}

function hasOwn(value: JsonRecord, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(value, key)
}
