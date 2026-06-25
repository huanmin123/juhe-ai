import { runtimeConfig, type McpProxyServerRuntimeConfig } from '../../config/runtime.js'
import { GatewayLocalProtocolResponse, GatewayRequestValidationError } from '../gateway/request/validation-error.js'
import type {
  OpenAIToAnthropicMcpProxyExecutor,
  OpenAIToAnthropicMcpProxyInput,
  OpenAIToAnthropicMcpProxyPreparedServer,
  OpenAIToAnthropicMcpProxyToolCallResult,
  OpenAIToAnthropicMcpProxyToolDefinition
} from '../providers/drivers/_shared/openai-anthropic-bridge.js'

type JsonRecord = Record<string, unknown>

interface McpToolDefinition {
  name: string
  description?: string
  input_schema: JsonRecord
  annotations: unknown
}

interface McpPreparedContext {
  server: McpProxyServerRuntimeConfig
  authorization?: string
  sessionId?: string
}

interface McpJsonRpcResult {
  result: unknown
  sessionId?: string
}

export function openAICompatibleMcpProxyExecutorForGatewayRequest(): OpenAIToAnthropicMcpProxyExecutor | undefined {
  const servers = runtimeConfig.mcpProxy.servers.filter((server) => server.enabled)
  if (!servers.length) return undefined
  return {
    async prepare(input) {
      return prepareOpenAICompatibleMcpProxy(input, servers)
    },
    async callTool(input) {
      return callOpenAICompatibleMcpProxyTool(input.prepared, input.toolName, input.arguments, input.signal)
    },
    async run(input) {
      return runOpenAICompatibleMcpProxy(input, servers)
    }
  }
}

async function runOpenAICompatibleMcpProxy(
  input: OpenAIToAnthropicMcpProxyInput,
  servers: McpProxyServerRuntimeConfig[]
): Promise<GatewayLocalProtocolResponse> {
  const prepared = await prepareOpenAICompatibleMcpProxy(input, servers)
  const context = mcpPreparedContext(prepared)
  const payload = await responsesMcpProxyResponsePayload(input, prepared, context)
  return new GatewayLocalProtocolResponse({
    code: 'openai_anthropic_bridge_mcp_proxy_runtime',
    message: 'Responses MCP proxy runtime completed',
    body: input.stream ? responsesMcpProxySse(payload) : JSON.stringify(payload.response),
    contentType: input.stream
      ? 'text/event-stream; charset=utf-8'
      : 'application/json; charset=utf-8'
  })
}

async function prepareOpenAICompatibleMcpProxy(
  input: Omit<OpenAIToAnthropicMcpProxyInput, 'model' | 'stream'>,
  servers: McpProxyServerRuntimeConfig[]
): Promise<OpenAIToAnthropicMcpProxyPreparedServer> {
  const server = resolveMcpProxyServer(input.tool, servers)
  const authorization = mcpProxyAuthorization(input.tool, server)
  const initialized = await initializeMcpServer(server, authorization, input.signal)
  const listed = await requestMcpJsonRpc(server, {
    method: 'tools/list',
    params: {},
    authorization,
    sessionId: initialized.sessionId,
    signal: input.signal
  })
  const tools = filteredMcpTools(input.tool, server, listed.result)
  return {
    serverLabel: server.label,
    serverUrl: server.serverUrl,
    tools: tools.map(toBridgeMcpToolDefinition),
    context: {
      server,
      authorization,
      sessionId: initialized.sessionId
    } satisfies McpPreparedContext
  }
}

async function callOpenAICompatibleMcpProxyTool(
  prepared: OpenAIToAnthropicMcpProxyPreparedServer,
  toolName: string,
  toolArguments: JsonRecord,
  signal?: AbortSignal
): Promise<OpenAIToAnthropicMcpProxyToolCallResult> {
  const context = mcpPreparedContext(prepared)
  const callResult = await requestMcpJsonRpc(context.server, {
    method: 'tools/call',
    params: {
      name: toolName,
      arguments: toolArguments
    },
    authorization: context.authorization,
    sessionId: context.sessionId,
    signal
  })
  const output = limitedMcpOutput(callResult.result)
  return {
    outputText: output.text,
    truncated: output.truncated,
    metadata: output.truncated
      ? { output_limit_bytes: runtimeConfig.mcpProxy.maxOutputBytes }
      : undefined
  }
}

function mcpPreparedContext(prepared: OpenAIToAnthropicMcpProxyPreparedServer): McpPreparedContext {
  const context = prepared.context as McpPreparedContext | undefined
  if (!context?.server) {
    throw new GatewayRequestValidationError(
      'MCP proxy prepared context missing server',
      'openai_anthropic_bridge_mcp_proxy_invalid_context',
      { statusCode: 500, type: 'server_error' }
    )
  }
  return context
}

function toBridgeMcpToolDefinition(tool: McpToolDefinition): OpenAIToAnthropicMcpProxyToolDefinition {
  return {
    name: tool.name,
    description: tool.description,
    inputSchema: tool.input_schema,
    annotations: tool.annotations
  }
}

async function initializeMcpServer(
  server: McpProxyServerRuntimeConfig,
  authorization: string | undefined,
  signal: AbortSignal | undefined
): Promise<{ sessionId?: string }> {
  const initialized = await requestMcpJsonRpc(server, {
    method: 'initialize',
    params: {
      protocolVersion: '2025-06-18',
      capabilities: {},
      clientInfo: {
        name: 'juhe-ai',
        version: '0.1.0'
      }
    },
    authorization,
    signal
  })
  if (initialized.sessionId) {
    await notifyMcpJsonRpc(server, {
      method: 'notifications/initialized',
      params: {},
      authorization,
      sessionId: initialized.sessionId,
      signal
    })
  }
  return { sessionId: initialized.sessionId }
}

async function responsesMcpProxyResponsePayload(
  input: OpenAIToAnthropicMcpProxyInput,
  prepared: OpenAIToAnthropicMcpProxyPreparedServer,
  context: McpPreparedContext
): Promise<{ response: JsonRecord; items: JsonRecord[]; text: string }> {
  const suffix = `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`
  const responseId = `resp_mcp_proxy_${suffix}`
  const createdAt = Math.floor(Date.now() / 1000)
  const listToolsItem: JsonRecord = {
    id: `mcpl_${safeIdSegment(responseId)}`,
    type: 'mcp_list_tools',
    server_label: prepared.serverLabel,
    tools: prepared.tools.map((tool) => ({
      annotations: tool.annotations ?? null,
      description: tool.description ?? '',
      input_schema: tool.inputSchema,
      name: tool.name
    }))
  }
  const items: JsonRecord[] = [listToolsItem]
  let text = ''
  const selectedTool = prepared.tools[0]
  if (!selectedTool) {
    text = 'MCP proxy found no allowed tools.'
    items.push(responsesMcpProxyMessageItem(responseId, text))
  } else {
    const approval = mcpApprovalResponseFromResponsesInput(input.body.input)
    if (approval?.approved === false) {
      text = 'MCP proxy tool call was not approved.'
      items.push(responsesMcpProxyMessageItem(responseId, text))
    } else if (mcpRequiresApproval(input.tool, selectedTool.name) && !approval?.approved) {
      items.push(responsesMcpProxyApprovalRequestItem(responseId, prepared.serverLabel, selectedTool.name, mcpProxyToolArguments(input.body, selectedTool)))
    } else {
      const toolArguments = mcpProxyToolArguments(input.body, selectedTool)
      const callResult = await callOpenAICompatibleMcpProxyTool(prepared, selectedTool.name, toolArguments, input.signal)
      items.push(responsesMcpProxyCallItem(responseId, prepared.serverLabel, selectedTool.name, toolArguments, callResult, approval?.approvalRequestId))
      text = 'MCP proxy completed remote tool call.'
      items.push(responsesMcpProxyMessageItem(responseId, text))
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
      model: input.model,
      output: items,
      output_text: text,
      parallel_tool_calls: false,
      previous_response_id: stringValue(input.body.previous_response_id) ?? null,
      reasoning: {
        effort: objectValue(input.body.reasoning)?.effort ?? null,
        summary: objectValue(input.body.reasoning)?.summary ?? null
      },
      store: false,
      temperature: null,
      text: { format: { type: 'text' } },
      tool_choice: 'auto',
      tools: [responsesMcpProxyToolSnapshot(input.tool, context.server)],
      top_p: null,
      truncation: 'disabled',
      usage: zeroResponsesUsage(),
      user: null,
      metadata: {
        gateway_runtime: 'local_runtime',
        gateway_tool: 'mcp'
      }
    },
    items,
    text
  }
}

function resolveMcpProxyServer(tool: JsonRecord, servers: McpProxyServerRuntimeConfig[]): McpProxyServerRuntimeConfig {
  const serverLabel = stringValue(tool.server_label)
  const serverUrl = stringValue(tool.server_url)
  const server = servers.find((item) => item.label === serverLabel && item.serverUrl === serverUrl)
  if (!server) {
    throw new GatewayRequestValidationError(
      `MCP server ${serverLabel ?? '<missing_label>'} 未命中本地 allowlist`,
      'openai_anthropic_bridge_mcp_server_not_allowed',
      { statusCode: 403, type: 'permission_error' }
    )
  }
  return server
}

function mcpProxyAuthorization(tool: JsonRecord, server: McpProxyServerRuntimeConfig): string | undefined {
  if (server.authorization) return server.authorization
  const requestAuthorization = stringValue(tool.authorization)
  if (requestAuthorization && server.allowRequestAuthorization) return requestAuthorization
  return undefined
}

function filteredMcpTools(
  tool: JsonRecord,
  server: McpProxyServerRuntimeConfig,
  listed: unknown
): McpToolDefinition[] {
  const result = objectValue(listed)
  const rawTools = Array.isArray(result?.tools) ? result.tools : []
  const serverAllowed = new Set(server.allowedTools)
  const requestAllowed = new Set(stringArrayValue(tool.allowed_tools))
  return rawTools
    .map(normalizeMcpToolDefinition)
    .filter((definition): definition is McpToolDefinition => Boolean(definition))
    .filter((definition) => !serverAllowed.size || serverAllowed.has(definition.name))
    .filter((definition) => !requestAllowed.size || requestAllowed.has(definition.name))
}

function normalizeMcpToolDefinition(value: unknown): McpToolDefinition | undefined {
  const tool = objectValue(value)
  const name = stringValue(tool?.name)
  if (!tool || !name) return undefined
  return {
    name,
    description: stringValue(tool.description),
    input_schema: objectValue(tool.input_schema) ?? objectValue(tool.inputSchema) ?? {
      type: 'object',
      properties: {}
    },
    annotations: tool.annotations ?? null
  }
}

function mcpProxyToolArguments(body: JsonRecord, tool: { inputSchema: JsonRecord }): JsonRecord {
  const schema = objectValue(tool.inputSchema)
  const properties = objectValue(schema?.properties)
  const required = stringArrayValue(schema?.required)
  const userText = responsesInputText(body.input) ?? ''
  const output: JsonRecord = {}
  for (const key of required) {
    const property = objectValue(properties?.[key])
    const type = stringValue(property?.type)
    if (type === 'string') {
      output[key] = userText || 'juhe-ai mcp proxy runtime'
    }
  }
  if (Object.keys(output).length > 0) return output
  if (properties?.query || properties?.input) {
    output[properties.query ? 'query' : 'input'] = userText || 'juhe-ai mcp proxy runtime'
  }
  return output
}

async function requestMcpJsonRpc(
  server: McpProxyServerRuntimeConfig,
  input: {
    method: string
    params?: JsonRecord
    authorization?: string
    sessionId?: string
    signal?: AbortSignal
  }
): Promise<McpJsonRpcResult> {
  const id = `mcp_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`
  const parsed = await postMcpJsonRpc(server, {
    body: {
      jsonrpc: '2.0',
      id,
      method: input.method,
      params: input.params ?? {}
    },
    authorization: input.authorization,
    sessionId: input.sessionId,
    signal: input.signal
  })
  const record = objectValue(parsed.body)
  if (!record) {
    throw invalidMcpResponse(`MCP ${input.method} 响应不是 JSON-RPC 对象`)
  }
  const error = objectValue(record.error)
  if (error) {
    throw new GatewayRequestValidationError(
      stringValue(error.message) ?? `MCP ${input.method} 返回 JSON-RPC error`,
      stringValue(error.code) ?? 'openai_anthropic_bridge_mcp_proxy_jsonrpc_error',
      { statusCode: 502, type: 'upstream_error' }
    )
  }
  return {
    result: record.result,
    sessionId: parsed.sessionId ?? input.sessionId
  }
}

async function notifyMcpJsonRpc(
  server: McpProxyServerRuntimeConfig,
  input: {
    method: string
    params?: JsonRecord
    authorization?: string
    sessionId?: string
    signal?: AbortSignal
  }
): Promise<void> {
  await postMcpJsonRpc(server, {
    body: {
      jsonrpc: '2.0',
      method: input.method,
      params: input.params ?? {}
    },
    authorization: input.authorization,
    sessionId: input.sessionId,
    signal: input.signal
  })
}

async function postMcpJsonRpc(
  server: McpProxyServerRuntimeConfig,
  input: {
    body: JsonRecord
    authorization?: string
    sessionId?: string
    signal?: AbortSignal
  }
): Promise<{ body: unknown; sessionId?: string }> {
  const headers = new Headers()
  headers.set('accept', 'application/json, text/event-stream')
  headers.set('content-type', 'application/json')
  headers.set('mcp-protocol-version', '2025-06-18')
  if (input.sessionId) headers.set('mcp-session-id', input.sessionId)
  if (input.authorization) headers.set('authorization', input.authorization)
  const timeoutController = new AbortController()
  const timeout = setTimeout(() => timeoutController.abort(), runtimeConfig.mcpProxy.timeoutMs)
  const requestSignal = input.signal
    ? AbortSignal.any([input.signal, timeoutController.signal])
    : timeoutController.signal
  try {
    const response = await fetch(server.serverUrl, {
      method: 'POST',
      headers,
      body: JSON.stringify(input.body),
      signal: requestSignal,
      redirect: 'error'
    })
    if (!response.ok) {
      const text = await readResponseTextWithLimit(response, runtimeConfig.mcpProxy.maxBodyBytes)
      throw new GatewayRequestValidationError(
        `MCP server ${server.label} 返回 HTTP ${response.status}${text ? `: ${truncateForError(text)}` : ''}`,
        'openai_anthropic_bridge_mcp_proxy_http_error',
        { statusCode: response.status >= 400 && response.status < 500 ? 400 : 502, type: 'upstream_error' }
      )
    }
    const text = await readResponseTextWithLimit(response, runtimeConfig.mcpProxy.maxBodyBytes)
    return {
      body: responseIsSse(response) ? parseMcpSseResponse(text) : safeParseJson(text),
      sessionId: response.headers.get('mcp-session-id') ?? undefined
    }
  } catch (error) {
    if (error instanceof GatewayRequestValidationError) throw error
    if (timeoutController.signal.aborted) {
      throw new GatewayRequestValidationError(
        `MCP server ${server.label} 请求超时`,
        'openai_anthropic_bridge_mcp_proxy_timeout',
        { statusCode: 504, type: 'upstream_error' }
      )
    }
    throw new GatewayRequestValidationError(
      error instanceof Error ? `MCP server ${server.label} 请求失败：${error.message}` : `MCP server ${server.label} 请求失败`,
      'openai_anthropic_bridge_mcp_proxy_request_failed',
      { statusCode: 502, type: 'upstream_error' }
    )
  } finally {
    clearTimeout(timeout)
  }
}

function parseMcpSseResponse(text: string): unknown {
  for (const frame of text.split(/\r?\n\r?\n/)) {
    const parsedFrame = parseSseFrame(frame)
    if (!parsedFrame || parsedFrame.data === '[DONE]') continue
    return safeParseJson(parsedFrame.data)
  }
  return {}
}

function parseSseFrame(frame: string): { event?: string; data: string } | undefined {
  const dataLines: string[] = []
  let event: string | undefined
  for (const rawLine of frame.split(/\r?\n/)) {
    if (!rawLine || rawLine.startsWith(':')) continue
    const colonIndex = rawLine.indexOf(':')
    const field = colonIndex >= 0 ? rawLine.slice(0, colonIndex) : rawLine
    const rawValue = colonIndex >= 0 ? rawLine.slice(colonIndex + 1) : ''
    const value = rawValue.startsWith(' ') ? rawValue.slice(1) : rawValue
    if (field === 'event') event = value
    if (field === 'data') dataLines.push(value)
  }
  if (!dataLines.length) return undefined
  return { event, data: dataLines.join('\n') }
}

function responseIsSse(response: Response): boolean {
  return (response.headers.get('content-type') ?? '').toLowerCase().includes('text/event-stream')
}

async function readResponseTextWithLimit(response: Response, maxBodyBytes: number): Promise<string> {
  if (!response.body) return await response.text()
  const reader = response.body.getReader()
  const chunks: Uint8Array[] = []
  let total = 0
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    if (!value) continue
    total += value.byteLength
    if (total > maxBodyBytes) {
      await reader.cancel()
      throw new GatewayRequestValidationError(
        'MCP server 响应体超过读取上限',
        'openai_anthropic_bridge_mcp_proxy_response_too_large',
        { statusCode: 502, type: 'upstream_error' }
      )
    }
    chunks.push(value)
  }
  return Buffer.concat(chunks.map((chunk) => Buffer.from(chunk))).toString('utf8')
}

function responsesMcpProxyCallItem(
  responseId: string,
  serverLabel: string,
  toolName: string,
  toolArguments: JsonRecord,
  result: OpenAIToAnthropicMcpProxyToolCallResult,
  approvalRequestId?: string
): JsonRecord {
  const item: JsonRecord = {
    id: `mcp_${safeIdSegment(responseId)}`,
    type: 'mcp_call',
    approval_request_id: approvalRequestId ?? null,
    arguments: JSON.stringify(toolArguments),
    error: null,
    name: toolName,
    output: result.outputText,
    server_label: serverLabel
  }
  if (result.truncated || result.metadata) {
    item.metadata = {
      ...(result.metadata ?? {}),
      ...(result.truncated ? { output_truncated: true } : {})
    }
  }
  return item
}

function limitedMcpOutput(value: unknown): { text: string; truncated: boolean } {
  const raw = mcpOutputText(value)
  const buffer = Buffer.from(raw, 'utf8')
  if (buffer.byteLength <= runtimeConfig.mcpProxy.maxOutputBytes) {
    return { text: raw, truncated: false }
  }
  const limited = buffer.subarray(0, runtimeConfig.mcpProxy.maxOutputBytes).toString('utf8')
  return {
    text: `${limited}\n[truncated by juhe-ai MCP proxy output limit]`,
    truncated: true
  }
}

function mcpOutputText(value: unknown): string {
  const record = objectValue(value)
  const content = Array.isArray(record?.content) ? record.content : []
  const texts = content.map((item) => {
    const part = objectValue(item)
    return part?.type === 'text' ? stringValue(part.text) : undefined
  }).filter((item): item is string => Boolean(item))
  if (texts.length) return texts.join('\n')
  return JSON.stringify(value ?? null)
}

function responsesMcpProxyApprovalRequestItem(
  responseId: string,
  serverLabel: string,
  toolName: string,
  toolArguments: JsonRecord
): JsonRecord {
  return {
    id: `mcpr_${safeIdSegment(responseId)}`,
    type: 'mcp_approval_request',
    arguments: JSON.stringify(toolArguments),
    name: toolName,
    server_label: serverLabel
  }
}

function responsesMcpProxyMessageItem(responseId: string, text: string): JsonRecord {
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

function responsesMcpProxyToolSnapshot(tool: JsonRecord, server: McpProxyServerRuntimeConfig): JsonRecord {
  const snapshot: JsonRecord = {
    type: 'mcp',
    server_label: server.label,
    server_url: server.serverUrl
  }
  const serverDescription = stringValue(tool.server_description)
  if (serverDescription) snapshot.server_description = serverDescription
  const allowedTools = stringArrayValue(tool.allowed_tools)
  if (allowedTools.length) snapshot.allowed_tools = allowedTools
  if (tool.require_approval !== undefined) snapshot.require_approval = tool.require_approval
  if (tool.defer_loading === true) snapshot.defer_loading = true
  return snapshot
}

function responsesMcpProxySse(input: { response: JsonRecord; items: JsonRecord[]; text: string }): string {
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
      item: mcpProxyInProgressItem(item)
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

function mcpProxyInProgressItem(item: JsonRecord): JsonRecord {
  if (item.type !== 'message') return item
  return {
    ...item,
    status: 'in_progress',
    content: []
  }
}

function mcpRequiresApproval(tool: JsonRecord, toolName: string): boolean {
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

function responsesInputText(value: unknown): string | undefined {
  if (typeof value === 'string') return value
  const items = Array.isArray(value)
    ? value
    : isPlainObject(value) ? [value] : []
  const texts: string[] = []
  for (const item of items) {
    if (!isPlainObject(item)) continue
    const content = Array.isArray(item.content) ? item.content : []
    for (const part of content) {
      const record = objectValue(part)
      if (!record) continue
      if (record.type === 'input_text' || record.type === 'output_text') {
        const text = stringValue(record.text)
        if (text) texts.push(text)
      }
    }
  }
  return texts.join('\n') || undefined
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

function invalidMcpResponse(message: string): GatewayRequestValidationError {
  return new GatewayRequestValidationError(
    message,
    'openai_anthropic_bridge_mcp_proxy_invalid_response',
    { statusCode: 502, type: 'upstream_error' }
  )
}

function sse(event: string, data: JsonRecord): string {
  return `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`
}

function safeParseJson(text: string): unknown {
  if (!text) return {}
  try {
    return JSON.parse(text) as unknown
  } catch {
    return {}
  }
}

function truncateForError(text: string): string {
  const normalized = text.replace(/\s+/g, ' ').trim()
  return normalized.length > 200 ? `${normalized.slice(0, 200)}...` : normalized
}

function safeIdSegment(value: string): string {
  const normalized = value.replace(/[^A-Za-z0-9_-]/g, '_')
  return normalized.slice(0, 96) || 'mcp'
}

function isPlainObject(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function objectValue(value: unknown): JsonRecord | undefined {
  return isPlainObject(value) ? value : undefined
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function stringArrayValue(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.filter((item): item is string => typeof item === 'string' && item.trim().length > 0).map((item) => item.trim())
}
